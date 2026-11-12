package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var ErrBindingConflict = errors.New("real identity already bound in this namespace")
var ErrInvalidBinding = errors.New("invalid binding or real ID overlaps the virtual range")

type Evidence struct {
	Identity
	RealID          int64
	OfficialMessage string
	Observation     string
	Sender          int64
	Distance        int
	At              time.Time
}

type Progress struct {
	Count    int  `json:"count"`
	Senders  int  `json:"senders"`
	Bound    bool `json:"bound"`
	Frozen   bool `json:"frozen"`
	Accepted bool `json:"accepted"`
}

func (s *Store) AddEvidence(ctx context.Context, ev Evidence, since time.Time, threshold, minSenders int, auto bool) (Progress, error) {
	if ev.RealID <= 0 || ev.RealID >= VirtualBase {
		return Progress{}, ErrInvalidBinding
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Progress{}, err
	}
	defer tx.Rollback()

	row, err := tx.ExecContext(ctx, `INSERT INTO evidence(namespace, kind, native, real_id,
		official_message, observation, sender, distance, recorded_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`, ev.Namespace, ev.Kind, ev.Native, ev.RealID, ev.OfficialMessage,
		ev.Observation, ev.Sender, ev.Distance, ev.At.UnixMilli())
	if err != nil {
		return Progress{}, err
	}
	inserted, err := row.RowsAffected()
	if err != nil || inserted == 0 {
		return Progress{}, err
	}
	result := Progress{Accepted: true}
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM identity_links WHERE namespace=? AND kind=?
			AND ((native=? AND real_id<>?) OR (native<>? AND real_id=?))
		UNION ALL SELECT 1 FROM evidence WHERE namespace=? AND kind=? AND recorded_at>=?
			AND ((native=? AND real_id<>?) OR (native<>? AND real_id=?))
		UNION ALL SELECT 1 FROM conflicts WHERE namespace=? AND kind=? AND (native=? OR real_id=?)
	)`, ev.Namespace, ev.Kind, ev.Native, ev.RealID, ev.Native, ev.RealID,
		ev.Namespace, ev.Kind, since.UnixMilli(), ev.Native, ev.RealID, ev.Native, ev.RealID,
		ev.Namespace, ev.Kind, ev.Native, ev.RealID).Scan(&result.Frozen)
	if err != nil {
		return Progress{}, err
	}
	if result.Frozen {
		_, err = tx.ExecContext(ctx, `INSERT INTO conflicts(namespace, kind, native, real_id, reason, recorded_at)
			VALUES(?, ?, ?, ?, 'competing identity evidence', ?) ON CONFLICT DO NOTHING`,
			ev.Namespace, ev.Kind, ev.Native, ev.RealID, ev.At.UnixMilli())
		if err != nil {
			return Progress{}, err
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT sender) FROM evidence
		WHERE namespace=? AND kind=? AND native=? AND real_id=? AND recorded_at>=?`,
		ev.Namespace, ev.Kind, ev.Native, ev.RealID, since.UnixMilli()).Scan(&result.Count, &result.Senders)
	if err != nil {
		return Progress{}, err
	}
	var changed bool
	if auto && !result.Frozen && result.Count >= threshold && result.Senders >= minSenders {
		changed, err = bind(ctx, tx, ev.Identity, ev.RealID, "matcher", ev.At, false)
		if err != nil {
			return Progress{}, err
		}
	}
	if err := s.commitBinding(tx, ev.Identity, changed); err != nil {
		return Progress{}, err
	}
	if changed {
		result.Bound = true
	}
	return result, nil
}

// MatchingGroup returns a verified group mapping or an unambiguous shadow candidate.
func (s *Store) MatchingGroup(ctx context.Context, key Identity, since time.Time, threshold, minSenders int) (int64, error) {
	record, err := s.Lookup(ctx, key)
	if err != nil || record.Real != 0 {
		return record.Real, err
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT e.real_id FROM evidence e
		WHERE e.namespace=? AND e.kind='group' AND e.native=? AND e.recorded_at>=?
		AND NOT EXISTS (SELECT 1 FROM conflicts c WHERE c.namespace=e.namespace AND c.kind=e.kind
			AND (c.native=e.native OR c.real_id=e.real_id))
		GROUP BY e.real_id HAVING COUNT(*)>=? AND COUNT(DISTINCT sender)>=?`,
		key.Namespace, key.Native, since.UnixMilli(), threshold, minSenders).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func (s *Store) SetBinding(ctx context.Context, key Identity, realID int64, actor string) error {
	if key.Namespace == "" || key.Native == "" || (key.Kind != "user" && key.Kind != "group") || realID < 0 || realID >= VirtualBase {
		return ErrInvalidBinding
	}
	s.writes.Lock()
	defer s.writes.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed, err := bind(ctx, tx, key, realID, actor, time.Now(), true)
	if err != nil {
		return err
	}
	for _, table := range []string{"conflicts", "evidence"} {
		_, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE namespace=? AND kind=? AND native=?", key.Namespace, key.Kind, key.Native)
		if err != nil {
			return err
		}
	}
	return s.commitBinding(tx, key, changed)
}

func (s *Store) commitBinding(tx *sql.Tx, key Identity, changed bool) error {
	if !changed {
		return tx.Commit()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := tx.Commit(); err != nil {
		return err
	}
	if record, ok := s.forward[key]; ok {
		delete(s.forward, key)
		delete(s.reverse, reverseKey{key.Namespace, key.Kind, record.Real})
		delete(s.reverse, reverseKey{key.Namespace, key.Kind, record.Virtual})
	}
	return nil
}

func bind(ctx context.Context, tx *sql.Tx, key Identity, realID int64, actor string, at time.Time, manual bool) (bool, error) {
	var before int64
	err := tx.QueryRowContext(ctx, `SELECT real_id FROM identity_links
		WHERE namespace=? AND kind=? AND native=?`, key.Namespace, key.Kind, key.Native).Scan(&before)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if before == realID {
		return false, nil
	}
	if before != 0 && !manual {
		return false, ErrBindingConflict
	}
	if realID == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO identities(id,namespace,kind,native)
			SELECT previous_id,namespace,kind,native FROM identity_links
			WHERE namespace=? AND kind=? AND native=? AND previous_id IS NOT NULL`, key.Namespace, key.Kind, key.Native)
		if err == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM identity_links WHERE namespace=? AND kind=? AND native=?", key.Namespace, key.Kind, key.Native)
		}
	} else {
		var competitor string
		err = tx.QueryRowContext(ctx, `SELECT native FROM identity_links WHERE namespace=? AND kind=? AND real_id=?`,
			key.Namespace, key.Kind, realID).Scan(&competitor)
		if err == nil && competitor != key.Native {
			return false, ErrBindingConflict
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO identity_links(namespace,kind,native,real_id,previous_id)
			VALUES(?1,?2,?3,?4,(SELECT id FROM identities WHERE namespace=?1 AND kind=?2 AND native=?3))
			ON CONFLICT(namespace,kind,native) DO UPDATE SET real_id=excluded.real_id`, key.Namespace, key.Kind, key.Native, realID)
		if err == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM identities WHERE namespace=? AND kind=? AND native=?", key.Namespace, key.Kind, key.Native)
		}
	}
	if err != nil {
		return false, err
	}
	action := "bind"
	if realID == 0 {
		action = "unbind"
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit(at, actor, action, namespace, kind, native, before_id, after_id)
		VALUES(?, ?, ?, ?, ?, ?, NULLIF(?, 0), NULLIF(?, 0))`,
		at.UnixMilli(), actor, action, key.Namespace, key.Kind, key.Native, before, realID)
	return err == nil, err
}
