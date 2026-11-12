package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrMessageNotFound = errors.New("message context missing or expired")

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

func (s *Store) CreateMessage(ctx context.Context, msg Message, build func(int64) ([]byte, error)) (Message, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, false, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT id, packet FROM message_refs
		WHERE namespace=? AND chat_kind=? AND peer=? AND native_id=? AND incoming=?`,
		msg.Namespace, msg.ChatKind, msg.Peer, msg.NativeID, msg.Incoming).Scan(&msg.ID, &msg.Packet)
	if err == nil {
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
		return Message{}, false, err
	}
	return msg, true, nil
}

func (s *Store) Message(ctx context.Context, namespace string, id int64, now time.Time) (Message, error) {
	msg := Message{ID: id, Namespace: namespace}
	var received, expires int64
	err := s.db.QueryRowContext(ctx, `SELECT chat_kind, peer, user_native, native_id, event_id, reference_id,
		incoming, packet, received_at, expires_at FROM message_refs
		WHERE namespace=? AND id=? AND expires_at>?`, namespace, id, now.UnixMilli()).
		Scan(&msg.ChatKind, &msg.Peer, &msg.UserNative, &msg.NativeID, &msg.EventID, &msg.ReferenceID,
			&msg.Incoming, &msg.Packet, &received, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Message{}, ErrMessageNotFound
	}
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
