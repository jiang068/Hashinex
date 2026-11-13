package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var ErrMessageNotFound = errors.New("message context missing or expired")
var errMessageCommit = errors.New("could not commit message context")

type Message struct {
	ID          int64
	Namespace   string
	ChatKind    string
	Peer        string
	UserNative  string
	NativeID    string
	EventID     string
	ReferenceID string
	Incoming    bool
	Packet      json.RawMessage
	ReceivedAt  time.Time
	ExpiresAt   time.Time
}

// CreateIncomingMessage retries temporary write contention within the event deadline.
func (s *Store) CreateIncomingMessage(ctx context.Context, msg Message, build func(int64) ([]byte, error)) (Message, bool, error) {
	const maxAttempts = 3
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return Message{}, false, err
		}
		saved, created, err := s.CreateMessage(ctx, msg, build)
		var sqliteErr *sqlite.Error
		if attempt == maxAttempts || errors.Is(err, errMessageCommit) || !errors.As(err, &sqliteErr) {
			return saved, created, err
		}
		// The low byte is the primary result code, including for extended errors.
		switch sqliteErr.Code() & 0xff {
		case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		default:
			return saved, created, err
		}
		timer := time.NewTimer(time.Duration(attempt) * 25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Message{}, false, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *Store) CreateMessage(ctx context.Context, msg Message, build func(int64) ([]byte, error)) (Message, bool, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return Message{}, false, err
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, err
	}
	defer tx.Rollback()
	var packet []byte
	err = tx.QueryRowContext(ctx, `SELECT id, packet FROM message_refs
		WHERE namespace=? AND chat_kind=? AND peer=? AND native_id=? AND incoming=?`,
		msg.Namespace, msg.ChatKind, msg.Peer, msg.NativeID, msg.Incoming).Scan(&msg.ID, &packet)
	if err == nil {
		msg.Packet = packet
		return msg, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Message{}, false, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO message_refs(namespace, chat_kind, peer, user_native, native_id,
		event_id, reference_id, incoming, received_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`, msg.Namespace, msg.ChatKind, msg.Peer, msg.UserNative,
		msg.NativeID, msg.EventID, msg.ReferenceID, msg.Incoming, msg.ReceivedAt.UnixMilli(), msg.ExpiresAt.UnixMilli()).Scan(&msg.ID)
	if err != nil {
		return Message{}, false, err
	}
	if build != nil {
		msg.Packet, err = build(msg.ID)
		if err != nil {
			return Message{}, false, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE message_refs SET packet=? WHERE id=?", msg.Packet, msg.ID); err != nil {
			return Message{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		// SQLite can leave a transaction open after a failed COMMIT.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return Message{}, false, errors.Join(errMessageCommit, err)
	}
	return msg, true, nil
}

func (s *Store) Message(ctx context.Context, namespace string, id int64, now time.Time) (Message, error) {
	msg := Message{ID: id, Namespace: namespace}
	var packet []byte
	var received, expires int64
	err := s.db.QueryRowContext(ctx, `SELECT chat_kind, peer, user_native, native_id, event_id, reference_id,
		incoming, packet, received_at, expires_at FROM message_refs
		WHERE namespace=? AND id=? AND expires_at>?`, namespace, id, now.UnixMilli()).
		Scan(&msg.ChatKind, &msg.Peer, &msg.UserNative, &msg.NativeID, &msg.EventID, &msg.ReferenceID,
			&msg.Incoming, &packet, &received, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrMessageNotFound
	}
	msg.Packet = packet
	msg.ReceivedAt, msg.ExpiresAt = time.UnixMilli(received), time.UnixMilli(expires)
	return msg, err
}

func (s *Store) FindReference(ctx context.Context, namespace, kind, peer, raw string, now time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM message_refs WHERE namespace=? AND chat_kind=? AND peer=?
		AND reference_id=? AND expires_at>? ORDER BY id DESC LIMIT 1`, namespace, kind, peer, raw, now.UnixMilli()).Scan(&id)
	return id, err
}

func (s *Store) NextSequence(ctx context.Context, namespace, peer, credential string, expires time.Time) (int, error) {
	var seq int
	err := s.db.QueryRowContext(ctx, `INSERT INTO reply_sequences(namespace, peer, credential, sequence, expires_at)
		VALUES (?, ?, ?, 1, ?) ON CONFLICT(namespace, peer, credential) DO UPDATE
		SET sequence = sequence + 1 RETURNING sequence`, namespace, peer, credential, expires.UnixMilli()).Scan(&seq)
	return seq, err
}
