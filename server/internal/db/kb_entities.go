package db

import (
	"context"
	"time"
)

// ---- knowledge base index ----

// KBEntity is one indexed knowledge-base entity.
type KBEntity struct {
	Kind, ID, Name, Importance string
	Relevance                  int
	Tags                       []string
	OneLine                    string
	UpdatedAt                  *time.Time
}

// putKBEntitySQL inserts or updates one index row, and does nothing when the row is already there with the same
// values: the knowledge base indexes unchanged entities again often (compressing, updating, committing projects), and
// an upsert that rewrites an equal row still takes a transaction id, writes WAL, commits with a flush, leaves a dead
// tuple and updates the tags' GIN index. Skipped by NOT EXISTS, it writes nothing at all ("ON CONFLICT ... DO UPDATE
// ... WHERE ... IS DISTINCT FROM" would still lock the row: an xid and WAL). A changed row is updated as before.
const putKBEntitySQL = `INSERT INTO kb_entities (kind, id, name, importance, relevance, tags, one_line, updated_at)
	SELECT $1::text, $2::text, $3::text, $4::text, $5::int, $6::text[], $7::text, $8::timestamptz
	WHERE NOT EXISTS (SELECT 1 FROM kb_entities o WHERE o.kind = $1::text AND o.id = $2::text
		AND (o.name, o.importance, o.relevance, o.tags, o.one_line, o.updated_at) IS NOT DISTINCT FROM
			($3::text, $4::text, $5::int, $6::text[], $7::text, $8::timestamptz))
	ON CONFLICT (kind, id) DO UPDATE SET name = EXCLUDED.name, importance = EXCLUDED.importance,
		relevance = EXCLUDED.relevance, tags = EXCLUDED.tags, one_line = EXCLUDED.one_line, updated_at = EXCLUDED.updated_at`

// PutKBEntity upserts one row of the knowledge-base index; an unchanged row is left as it is (putKBEntitySQL).
func (d *DB) PutKBEntity(ctx context.Context, kind, id, name, importance string, relevance int, tags []string, oneLine string, updated time.Time) error {
	if tags == nil {
		tags = []string{}
	}
	var at *time.Time
	if !updated.IsZero() {
		at = &updated
	}
	_, err := d.pool.Exec(ctx, putKBEntitySQL, kind, id, name, importance, relevance, tags, oneLine, at)
	return err
}

// DeleteKBEntities removes the index rows of kind with these ids (a source the user took back, kb.ForgetSource).
func (d *DB) DeleteKBEntities(ctx context.Context, kind string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := d.pool.Exec(ctx, `DELETE FROM kb_entities WHERE kind = $1 AND id = ANY($2)`, kind, ids)
	return err
}

// KBEntities lists indexed entities of a kind (all kinds when ""), carrying tag when set, most important first.
func (d *DB) KBEntities(ctx context.Context, kind, tag string, limit int) ([]KBEntity, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.pool.Query(ctx, `SELECT kind, id, name, importance, relevance, tags, one_line, updated_at FROM kb_entities
		WHERE ($1 = '' OR kind = $1) AND ($2 = '' OR $2 = ANY(tags))
		ORDER BY CASE WHEN importance = '' THEN 'P9' ELSE importance END, relevance DESC, updated_at DESC NULLS LAST LIMIT $3`,
		kind, tag, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KBEntity
	for rows.Next() {
		var e KBEntity
		if err := rows.Scan(&e.Kind, &e.ID, &e.Name, &e.Importance, &e.Relevance, &e.Tags, &e.OneLine, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
