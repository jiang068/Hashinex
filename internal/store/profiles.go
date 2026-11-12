package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrProfileNotFound = errors.New("no recent profile in this conversation scope")

type ObservedProfile struct {
	Native   string
	Nickname string
	LastSent int64
}

func (s *Store) ObservedUser(ctx context.Context, namespace, kind, user string, peers []string, now time.Time) (ObservedProfile, error) {
	allow, err := json.Marshal(peers)
	if err != nil {
		return ObservedProfile{}, err
	}
	profile, err := readObserved(s.db.QueryRowContext(ctx, `SELECT user_native,
		COALESCE(json_extract(packet, '$.sender.nickname'), ''), received_at
		FROM message_refs WHERE namespace=? AND chat_kind=? AND user_native=? AND incoming=1 AND expires_at>?
		AND (? OR peer IN (SELECT value FROM json_each(?)))
		ORDER BY received_at DESC, id DESC LIMIT 1`, namespace, kind, user, now.UnixMilli(), len(peers) == 0, string(allow)))
	if errors.Is(err, sql.ErrNoRows) {
		return ObservedProfile{}, ErrProfileNotFound
	}
	return profile, err
}

func (s *Store) ObservedGroupMembers(ctx context.Context, namespace, peer string, now time.Time) ([]ObservedProfile, error) {
	rows, err := s.db.QueryContext(ctx, `WITH recent AS (
		SELECT user_native, packet, received_at, ROW_NUMBER() OVER (
			PARTITION BY user_native ORDER BY received_at DESC, id DESC) AS position
		FROM message_refs WHERE namespace=? AND chat_kind='group' AND peer=? AND incoming=1 AND expires_at>?
	)
	SELECT user_native, COALESCE(json_extract(packet, '$.sender.nickname'), ''), received_at
	FROM recent WHERE position=1 ORDER BY received_at DESC, user_native`, namespace, peer, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var profiles []ObservedProfile
	for rows.Next() {
		profile, err := readObserved(rows)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, profile)
	}
	return profiles, rows.Err()
}

func readObserved(row interface{ Scan(...any) error }) (ObservedProfile, error) {
	var profile ObservedProfile
	var sentMillis int64
	err := row.Scan(&profile.Native, &profile.Nickname, &sentMillis)
	profile.LastSent = sentMillis / 1000
	return profile, err
}
