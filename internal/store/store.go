package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const VirtualBase int64 = 100_000_000_000_000
const transientBase int64 = 1_000_000_000_000_000
const maxCached = 16384

var ErrUnmapped = errors.New("no identity mapping for this bot")

//go:embed schema.sql
var schema string

type Identity struct {
	Namespace string
	Kind      string
	Native    string
}

type Record struct {
	Virtual int64
	Real    int64
}

func (r Record) ExternalID() int64 {
	if r.Real != 0 {
		return r.Real
	}
	return r.Virtual
}

type reverseKey struct {
	namespace string
	kind      string
	id        int64
}

type groupSourceKey struct {
	namespace string
	user      string
	group     string
}

type Store struct {
	db           *sql.DB
	writes       sync.Mutex
	mu           sync.RWMutex
	forward      map[Identity]Record
	reverse      map[reverseKey]string
	transient    map[reverseKey]string
	groupSources map[groupSourceKey]struct{}
}

func Open(ctx context.Context, path string) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	uri := sqliteURI(path, url.Values{"_pragma": {"busy_timeout(750)", "foreign_keys(ON)"}})
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{
		db: db, forward: make(map[Identity]Record), reverse: make(map[reverseKey]string),
		transient: make(map[reverseKey]string), groupSources: make(map[groupSourceKey]struct{}),
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func sqliteURI(path string, query url.Values) url.URL {
	name := filepath.ToSlash(path)
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	return url.URL{Scheme: "file", Path: name, RawQuery: query.Encode()}
}

func ReadRealIDs(ctx context.Context, path string, keys []Identity) (map[Identity]int64, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := sqliteURI(path, url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(750)"}})
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ids := make(map[Identity]int64, len(keys))
	for _, key := range keys {
		var id int64
		err := db.QueryRowContext(ctx, `SELECT real_id FROM identity_links
			WHERE namespace=? AND kind=? AND native=?`, key.Namespace, key.Kind, key.Native).Scan(&id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		ids[key] = id
	}
	return ids, nil
}

func (s *Store) migrate(ctx context.Context) error {
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&mode); err != nil {
		return err
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("could not enable SQLite WAL")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	switch version {
	case 0:
		if _, err := tx.ExecContext(ctx, schema); err != nil {
			return err
		}
		fallthrough
	case 1:
		if _, err := tx.ExecContext(ctx, `CREATE INDEX message_refs_group_profile
			ON message_refs(namespace, peer, user_native, received_at DESC, id DESC) WHERE incoming=1 AND chat_kind='group';
			CREATE INDEX message_refs_user_profile
			ON message_refs(namespace, chat_kind, user_native, received_at DESC, id DESC) WHERE incoming=1;
			PRAGMA user_version = 2`); err != nil {
			return err
		}
		fallthrough
	case 2:
		if _, err := tx.ExecContext(ctx, `CREATE TABLE private_challenges (
			namespace TEXT NOT NULL, native TEXT NOT NULL, code TEXT NOT NULL UNIQUE,
			group_id INTEGER NOT NULL, expires_at INTEGER NOT NULL,
			PRIMARY KEY (namespace, native)
		);
		CREATE INDEX private_challenges_expiry ON private_challenges(expires_at);
		PRAGMA user_version = 3`); err != nil {
			return err
		}
		fallthrough
	case 3:
		if _, err := tx.ExecContext(ctx, `ALTER TABLE private_challenges ADD COLUMN confirmed INTEGER NOT NULL DEFAULT 0;
			ALTER TABLE private_challenges ADD COLUMN reply_peer TEXT NOT NULL DEFAULT '';
			ALTER TABLE private_challenges ADD COLUMN reply_message TEXT NOT NULL DEFAULT '';
			ALTER TABLE private_challenges ADD COLUMN reply_until INTEGER NOT NULL DEFAULT 0;
			PRAGMA user_version = 4`); err != nil {
			return err
		}
		fallthrough
	case 4:
		if _, err := tx.ExecContext(ctx, `CREATE INDEX identity_links_entity ON identity_links(kind, real_id);
			CREATE TABLE private_group_messages (
				code TEXT NOT NULL REFERENCES private_challenges(code) ON DELETE CASCADE,
				group_id INTEGER NOT NULL, peer TEXT NOT NULL, message TEXT NOT NULL, reply_until INTEGER NOT NULL,
				PRIMARY KEY (code, group_id)
			);
			CREATE TABLE private_observations (
				code TEXT NOT NULL REFERENCES private_challenges(code) ON DELETE CASCADE,
				group_id INTEGER NOT NULL, user_id INTEGER NOT NULL,
				PRIMARY KEY (code, group_id, user_id)
			);
			PRAGMA user_version = 5`); err != nil {
			return err
		}
		fallthrough
	case 5:
		if _, err := tx.ExecContext(ctx, `ALTER TABLE identity_links RENAME TO previous_identity_links;
			CREATE TABLE identity_links (
				namespace TEXT NOT NULL, kind TEXT NOT NULL CHECK (kind IN ('user','group')), native TEXT NOT NULL,
				real_id INTEGER NOT NULL CHECK (real_id > 0 AND real_id < 100000000000000),
				previous_id INTEGER UNIQUE,
				PRIMARY KEY (namespace, kind, native), UNIQUE (namespace, kind, real_id)
			);
			INSERT INTO identity_links SELECT l.namespace,l.kind,l.native,l.real_id,i.id
				FROM previous_identity_links l JOIN identities i USING(namespace,kind,native);
			DROP TABLE previous_identity_links;
			CREATE INDEX identity_links_entity ON identity_links(kind, real_id);
			DELETE FROM identities WHERE EXISTS(SELECT 1 FROM identity_links l
				WHERE (l.namespace,l.kind,l.native)=(identities.namespace,identities.kind,identities.native));
			CREATE TABLE identity_sources (
				namespace TEXT NOT NULL, native TEXT NOT NULL, peer TEXT NOT NULL,
				PRIMARY KEY(namespace,native,peer)
			);
			INSERT OR IGNORE INTO identity_sources SELECT namespace,user_native,peer
				FROM message_refs m WHERE chat_kind='group' AND incoming=1 AND user_native<>''
				AND (EXISTS (SELECT 1 FROM identities i WHERE (i.namespace,i.kind,i.native)=(m.namespace,'user',m.user_native))
				OR EXISTS (SELECT 1 FROM identity_links l WHERE (l.namespace,l.kind,l.native)=(m.namespace,'user',m.user_native)));
			PRAGMA user_version = 6`); err != nil {
			return err
		}
	case 6:
	default:
		return fmt.Errorf("unsupported SQLite schema version %d", version)
	}
	return tx.Commit()
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Lookup reads an identity without registering a new unbound alias.
func (s *Store) Lookup(ctx context.Context, key Identity) (Record, error) {
	record, err := s.getIdentity(ctx, key, false)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, nil
	}
	return record, err
}

// Transient keeps a reversible business ID in memory without creating a pending identity.
func (s *Store) Transient(key Identity) int64 {
	hash := sha256.Sum256([]byte(key.Namespace + "\x00" + key.Kind + "\x00" + key.Native))
	id := transientBase + int64(binary.BigEndian.Uint64(hash[:8])&((1<<48)-1))
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		k := reverseKey{key.Namespace, key.Kind, id}
		if native := s.transient[k]; native == key.Native {
			return id
		} else if native == "" {
			if len(s.transient) >= maxCached {
				clear(s.transient)
			}
			s.transient[k] = key.Native
			return id
		}
		id++
	}
}

func (s *Store) RecordGroupSource(ctx context.Context, key Identity, peer string) error {
	source := groupSourceKey{namespace: key.Namespace, user: key.Native, group: peer}
	s.mu.RLock()
	_, recorded := s.groupSources[source]
	s.mu.RUnlock()
	if recorded {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO identity_sources(namespace,native,peer)
		VALUES(?,?,?) ON CONFLICT DO NOTHING`, key.Namespace, key.Native, peer)
	if err != nil {
		return err
	}
	// Sources are append-only. Cache only rows that SQLite has accepted.
	s.mu.Lock()
	if len(s.groupSources) >= maxCached {
		clear(s.groupSources)
	}
	s.groupSources[source] = struct{}{}
	s.mu.Unlock()
	return nil
}

func (s *Store) Resolve(ctx context.Context, key Identity) (Record, error) {
	return s.getIdentity(ctx, key, true)
}

func (s *Store) getIdentity(ctx context.Context, key Identity, register bool) (Record, error) {
	s.mu.RLock()
	record, ok := s.forward[key]
	s.mu.RUnlock()
	if ok {
		return record, nil
	}
	// Serialize cache fills with binding commits so a stale read cannot be
	// published after the binding has invalidated its cache entry.
	s.writes.Lock()
	defer s.writes.Unlock()
	s.mu.RLock()
	record, ok = s.forward[key]
	s.mu.RUnlock()
	if ok {
		return record, nil
	}
	record, err := lookup(ctx, s.db, key)
	if register && errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.ExecContext(ctx, `INSERT INTO identities(namespace, kind, native)
			VALUES (?, ?, ?) ON CONFLICT(namespace, kind, native) DO NOTHING`, key.Namespace, key.Kind, key.Native)
		if err == nil {
			record, err = lookup(ctx, s.db, key)
		}
	}
	if err == nil {
		s.mu.Lock()
		if len(s.forward) >= maxCached {
			clear(s.forward)
			clear(s.reverse)
		}
		s.forward[key] = record
		if record.Virtual != 0 {
			s.reverse[reverseKey{key.Namespace, key.Kind, record.Virtual}] = key.Native
		}
		if record.Real != 0 {
			s.reverse[reverseKey{key.Namespace, key.Kind, record.Real}] = key.Native
		}
		s.mu.Unlock()
	}
	return record, err
}

func (s *Store) Reverse(ctx context.Context, namespace, kind string, id int64) (string, error) {
	if id <= 0 {
		return "", ErrUnmapped
	}
	s.mu.RLock()
	if id >= transientBase {
		native := s.transient[reverseKey{namespace, kind, id}]
		s.mu.RUnlock()
		if native == "" {
			return "", ErrUnmapped
		}
		return native, nil
	}
	cached, ok := s.reverse[reverseKey{namespace, kind, id}]
	s.mu.RUnlock()
	if ok {
		return cached, nil
	}
	var native string
	var err error
	if id >= VirtualBase {
		err = s.db.QueryRowContext(ctx, `SELECT native FROM identities WHERE namespace=?1 AND kind=?2 AND id=?3
			UNION ALL SELECT native FROM identity_links WHERE namespace=?1 AND kind=?2 AND previous_id=?3`,
			namespace, kind, id-VirtualBase).Scan(&native)
	} else {
		err = s.db.QueryRowContext(ctx, `SELECT native FROM identity_links
			WHERE namespace=? AND kind=? AND real_id=?`, namespace, kind, id).Scan(&native)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnmapped
	}
	return native, err
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func lookup(ctx context.Context, db queryer, key Identity) (Record, error) {
	var record Record
	err := db.QueryRowContext(ctx, `SELECT COALESCE(previous_id,0), real_id FROM identity_links
		WHERE namespace=?1 AND kind=?2 AND native=?3
		UNION ALL SELECT id,0 FROM identities WHERE namespace=?1 AND kind=?2 AND native=?3`,
		key.Namespace, key.Kind, key.Native).Scan(&record.Virtual, &record.Real)
	if err == nil && record.Virtual != 0 {
		record.Virtual += VirtualBase
	}
	return record, err
}

func (s *Store) Clean(ctx context.Context, before, evidenceBefore time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, row := range []struct {
		sql string
		at  time.Time
	}{
		{"DELETE FROM message_refs WHERE expires_at <= ?", before},
		{"DELETE FROM private_challenges WHERE expires_at <= ?", before},
		{"DELETE FROM reply_sequences WHERE expires_at <= ?", before},
		{"DELETE FROM evidence WHERE recorded_at <= ?", evidenceBefore},
	} {
		if _, err := tx.ExecContext(ctx, row.sql, row.at.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
