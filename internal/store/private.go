package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) PrivateChallenge(ctx context.Context, key Identity, group int64, candidate string, now, expires time.Time) (string, error) {
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var code string
	err = tx.QueryRowContext(ctx, `SELECT code FROM private_challenges WHERE namespace=? AND native=?
		AND group_id=? AND confirmed=0 AND expires_at>?
		AND NOT EXISTS (SELECT 1 FROM identity_links WHERE namespace=? AND kind='user' AND native=?)`,
		key.Namespace, key.Native, group, now.UnixMilli(), key.Namespace, key.Native).Scan(&code)
	if !errors.Is(err, sql.ErrNoRows) {
		return code, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM private_challenges
		WHERE namespace=? AND native=? AND (confirmed=0 OR expires_at<=?)`,
		key.Namespace, key.Native, now.UnixMilli()); err != nil {
		return "", err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO private_challenges(namespace, native, code, group_id, expires_at)
		SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (
			SELECT 1 FROM identity_links WHERE namespace=? AND kind='user' AND native=?
		)
		ON CONFLICT(namespace, native) DO NOTHING
		RETURNING code`, key.Namespace, key.Native, candidate, group, expires.UnixMilli(),
		key.Namespace, key.Native).Scan(&code)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return code, tx.Commit()
}

func (s *Store) ObservePrivateCode(ctx context.Context, code string, group, user int64, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO private_observations(code, group_id, user_id)
		SELECT code, ?, ? FROM private_challenges WHERE code=? AND (group_id=0 OR group_id=?)
			AND confirmed=0 AND expires_at>?
		ON CONFLICT DO NOTHING`, group, user, code, group, now.UnixMilli())
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count != 0, err
}

func (s *Store) PrivateCandidates(ctx context.Context, code string, group int64, now time.Time) (Identity, []int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT c.namespace, c.native, o.user_id
		FROM private_challenges c JOIN private_group_messages m USING(code)
		JOIN private_observations o ON o.code=c.code AND o.group_id=m.group_id
		WHERE c.code=? AND m.group_id=? AND c.confirmed=0 AND c.expires_at>? AND m.reply_until>?
		ORDER BY o.user_id`, code, group, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return Identity{}, nil, err
	}
	defer rows.Close()
	key := Identity{Kind: "user"}
	var users []int64
	for rows.Next() {
		var user int64
		if err := rows.Scan(&key.Namespace, &key.Native, &user); err != nil {
			return Identity{}, nil, err
		}
		users = append(users, user)
	}
	return key, users, rows.Err()
}

func (s *Store) RedeemPrivateChallenge(ctx context.Context, key Identity, code, groupNamespace string, group, qq int64, now time.Time) (bool, error) {
	if qq <= 0 || qq >= VirtualBase {
		return false, ErrInvalidBinding
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE private_challenges AS c
		SET confirmed=1, group_id=m.group_id, reply_peer=m.peer, reply_message=m.message, reply_until=m.reply_until
		FROM private_group_messages m JOIN private_observations o USING(code, group_id)
		WHERE c.code=m.code AND c.code=? AND m.group_id=? AND o.user_id=? AND c.namespace=? AND c.native=?
			AND c.confirmed=0 AND c.expires_at>? AND m.reply_until>?
			AND EXISTS (SELECT 1 FROM identity_links l WHERE l.namespace=? AND l.kind='group'
				AND l.native=m.peer AND l.real_id=m.group_id)`,
		code, group, qq, key.Namespace, key.Native, now.UnixMilli(), now.UnixMilli(), groupNamespace)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows == 0 {
		return false, err
	}
	changed, err := bind(ctx, tx, key, qq, "private-challenge", now, false)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO identity_sources(namespace,native,peer)
		SELECT namespace,native,reply_peer FROM private_challenges WHERE code=?`, code); err != nil {
		return false, err
	}
	return changed, s.commitBinding(tx, key, changed)
}

type PrivateConfirmation struct {
	Peer, Message string
	Expires       time.Time
}

func (s *Store) RememberPrivateGroup(ctx context.Context, namespace, code string, group int64, reply PrivateConfirmation, now time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO private_group_messages(code,group_id,peer,message,reply_until)
		SELECT code, ?, ?, ?, ? FROM private_challenges WHERE namespace=? AND code=?
			AND (group_id=0 OR group_id=?) AND confirmed=0 AND expires_at>?
		ON CONFLICT(code,group_id) DO UPDATE SET peer=excluded.peer, message=excluded.message, reply_until=excluded.reply_until
			WHERE private_group_messages.reply_until<=?`, group, reply.Peer, reply.Message, reply.Expires.UnixMilli(),
		namespace, code, group, now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count != 0, err
}

func (s *Store) ClaimPrivateConfirmation(ctx context.Context, namespace, code string, now time.Time) (PrivateConfirmation, error) {
	var reply PrivateConfirmation
	var expires int64
	err := s.db.QueryRowContext(ctx, `DELETE FROM private_challenges
		WHERE namespace=? AND code=? AND confirmed=1 AND reply_until>?
		RETURNING reply_peer, reply_message, reply_until`, namespace, code, now.UnixMilli()).
		Scan(&reply.Peer, &reply.Message, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return PrivateConfirmation{}, nil
	}
	reply.Expires = time.UnixMilli(expires)
	return reply, err
}
