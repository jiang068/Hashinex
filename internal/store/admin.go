package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type GroupSource struct {
	Native string `json:"native"`
	Real   int64  `json:"real_id"`
}

type ListedIdentity struct {
	Namespace string        `json:"namespace"`
	Kind      string        `json:"kind"`
	Native    string        `json:"native"`
	Virtual   int64         `json:"virtual_id"`
	Real      int64         `json:"real_id"`
	Sources   []GroupSource `json:"sources,omitempty"`
}

type Entity struct {
	Kind    string           `json:"kind"`
	ID      int64            `json:"id"`
	Bound   bool             `json:"bound"`
	Aliases []ListedIdentity `json:"aliases"`
}

type Page[T any] struct {
	Rows  []T `json:"rows"`
	Page  int `json:"page"`
	Size  int `json:"size"`
	Total int `json:"total"`
}

const aliasesSQL = `WITH aliases AS (
	SELECT namespace,kind,native,id AS pending,0 AS real,id AS ordering FROM identities
	UNION ALL SELECT namespace,kind,native,0,real_id,COALESCE(previous_id,0) FROM identity_links
)`

const sourceRowsSQL = ` FROM identity_sources source LEFT JOIN identity_links grp
	ON (grp.namespace,grp.kind,grp.native)=(
		CASE WHEN source.namespace LIKE 'qq:%:c2c' THEN substr(source.namespace,1,length(source.namespace)-4)
		ELSE source.namespace END,'group',source.peer)
	WHERE a.kind='user' AND (source.namespace,source.native)=(a.namespace,a.native)`

const sourcesSQL = `(SELECT json_group_array(json_object('native',source.peer,'real_id',COALESCE(grp.real_id,0)))` + sourceRowsSQL + `)`

const sourceSearchSQL = `EXISTS(SELECT 1` + sourceRowsSQL + `
	AND (instr(source.peer,?1)>0 OR instr(CAST(grp.real_id AS TEXT),?1)>0))`

func queryPage[T any](ctx context.Context, db *sql.DB, page, size int, base, selectRows string, args []any,
	read func(*sql.Rows, *[]T) error) (Page[T], error) {
	result := Page[T]{Rows: []T{}, Page: page, Size: size}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err := tx.QueryRowContext(ctx, base+` SELECT COUNT(*) FROM selected`, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	result.Page = min(page, max(1, (result.Total+size-1)/size))
	rows, err := tx.QueryContext(ctx, base+selectRows, append(args, size, (result.Page-1)*size)...)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		if err := read(rows, &result.Rows); err != nil {
			return result, err
		}
	}
	return result, rows.Err()
}

func (s *Store) EntityPage(ctx context.Context, search, kind string, page, size int) (Page[Entity], error) {
	const base = aliasesSQL + `, selected AS (
		SELECT a.kind, CASE WHEN a.real=0 THEN a.pending+?3 ELSE a.real END AS id, MAX(ordering) AS latest
		FROM aliases a WHERE (?2='' OR a.kind=?2) AND
			(?1='' OR instr(a.native,?1)>0 OR instr(a.namespace,?1)>0
			OR instr(CAST(CASE WHEN a.real=0 THEN a.pending+?3 ELSE a.real END AS TEXT),?1)>0 OR ` + sourceSearchSQL + `)
		GROUP BY a.kind,id
	)`
	const selectRows = `, visible AS (SELECT * FROM selected ORDER BY latest DESC,kind,id LIMIT ?4 OFFSET ?5),
	page_aliases AS (
		SELECT e.kind,e.id,e.latest,l.namespace,l.native,0 AS pending,l.real_id AS real
		FROM visible e JOIN identity_links l ON l.kind=e.kind AND l.real_id=e.id
		UNION ALL
		SELECT e.kind,e.id,e.latest,i.namespace,i.native,i.id,0
		FROM visible e JOIN identities i ON i.id=e.id-?3
	)
		SELECT a.kind,a.id,a.namespace,a.native,a.pending,a.real,` + sourcesSQL + `
		FROM page_aliases a ORDER BY a.latest DESC,a.kind,a.id,a.namespace,a.native`
	return queryPage(ctx, s.db, page, size, base, selectRows, []any{search, kind, VirtualBase},
		func(rows *sql.Rows, result *[]Entity) error {
			var identity ListedIdentity
			var id int64
			var sources string
			if err := rows.Scan(&identity.Kind, &id, &identity.Namespace, &identity.Native, &identity.Virtual, &identity.Real, &sources); err != nil {
				return err
			}
			if identity.Virtual != 0 {
				identity.Virtual += VirtualBase
			}
			if err := json.Unmarshal([]byte(sources), &identity.Sources); err != nil {
				return err
			}
			index := len(*result) - 1
			if index < 0 || (*result)[index].Kind != identity.Kind || (*result)[index].ID != id {
				*result = append(*result, Entity{Kind: identity.Kind, ID: id, Bound: identity.Real != 0})
				index++
			}
			(*result)[index].Aliases = append((*result)[index].Aliases, identity)
			return nil
		})
}

func (s *Store) Entities(ctx context.Context, search, kind string) ([]Entity, error) {
	page, err := s.EntityPage(ctx, search, kind, 1, 200)
	return page.Rows, err
}

type Candidate struct {
	Namespace string        `json:"namespace"`
	Kind      string        `json:"kind"`
	Native    string        `json:"native"`
	Real      int64         `json:"real_id"`
	Count     int           `json:"count"`
	Senders   int           `json:"senders"`
	Frozen    bool          `json:"frozen"`
	Bound     bool          `json:"bound"`
	Distance  int           `json:"distance"`
	Sources   []GroupSource `json:"sources,omitempty"`
}

func (s *Store) Identities(ctx context.Context, search string) ([]ListedIdentity, error) {
	rows, err := s.db.QueryContext(ctx, aliasesSQL+` SELECT namespace,kind,native,pending,real FROM aliases
		WHERE ?1='' OR instr(native,?1)>0 OR instr(namespace,?1)>0 OR CAST(real AS TEXT)=?1
			OR (pending<>0 AND CAST(pending+?2 AS TEXT)=?1) ORDER BY ordering DESC LIMIT 200`, search, VirtualBase)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ListedIdentity{}
	for rows.Next() {
		var i ListedIdentity
		if err := rows.Scan(&i.Namespace, &i.Kind, &i.Native, &i.Virtual, &i.Real); err != nil {
			return nil, err
		}
		if i.Virtual != 0 {
			i.Virtual += VirtualBase
		}
		result = append(result, i)
	}
	return result, rows.Err()
}

func (s *Store) Candidates(ctx context.Context, since time.Time) ([]Candidate, error) {
	page, err := s.CandidatePage(ctx, since, "", "", 1, 200)
	return page.Rows, err
}

func (s *Store) CandidatePage(ctx context.Context, since time.Time, search, status string, page, size int) (Page[Candidate], error) {
	const base = `WITH candidates AS (
		SELECT e.namespace,e.kind,e.native,e.real_id,COUNT(*) AS matches,COUNT(DISTINCT e.sender) AS senders,
		EXISTS(SELECT 1 FROM conflicts c WHERE c.namespace=e.namespace AND c.kind=e.kind AND (c.native=e.native OR c.real_id=e.real_id)) AS frozen,
		EXISTS(SELECT 1 FROM identity_links l WHERE l.namespace=e.namespace AND l.kind=e.kind AND l.native=e.native AND l.real_id=e.real_id) AS bound,
		MAX(e.distance) AS distance,MAX(e.recorded_at) AS latest
		FROM evidence e WHERE e.recorded_at>=?2 GROUP BY e.namespace,e.kind,e.native,e.real_id
	), selected AS (
		SELECT a.* FROM candidates a
		WHERE (?3='' OR ?3=CASE WHEN frozen THEN 'frozen' WHEN bound THEN 'bound' ELSE 'collecting' END)
		AND (?1='' OR instr(a.namespace,?1)>0 OR instr(a.native,?1)>0 OR instr(CAST(a.real_id AS TEXT),?1)>0 OR ` + sourceSearchSQL + `)
	)`
	const selectRows = ` SELECT a.namespace,a.kind,a.native,a.real_id,a.matches,a.senders,a.frozen,a.bound,a.distance,` + sourcesSQL +
		` FROM selected a ORDER BY latest DESC,namespace,kind,native,real_id LIMIT ?4 OFFSET ?5`
	return queryPage(ctx, s.db, page, size, base, selectRows, []any{search, since.UnixMilli(), status},
		func(rows *sql.Rows, result *[]Candidate) error {
			var c Candidate
			var sources string
			if err := rows.Scan(&c.Namespace, &c.Kind, &c.Native, &c.Real, &c.Count, &c.Senders, &c.Frozen, &c.Bound, &c.Distance, &sources); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(sources), &c.Sources); err != nil {
				return err
			}
			*result = append(*result, c)
			return nil
		})
}
