package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// ---- "Always allow in this conversation" on assistant conversations (assistant_always) ----

func alwaysKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// AllowAlways keeps keys as granted for the session (tool: what the card was about, for reading the table).
func (d *DB) AllowAlways(ctx context.Context, session, tool string, keys []string) error {
	hashes := make([]string, len(keys))
	for i, k := range keys {
		hashes[i] = alwaysKeyHash(k)
	}
	_, err := d.pool.Exec(ctx, `INSERT INTO assistant_always (session_id, key_hash, tool_name)
		SELECT $1::text, h, $2::text FROM unnest($3::text[]) AS h ON CONFLICT DO NOTHING`, session, tool, hashes)
	return err
}

// AlwaysAllowed reports whether every one of keys was granted for the session; false for none.
func (d *DB) AlwaysAllowed(ctx context.Context, session string, keys []string) (bool, error) {
	want := map[string]bool{}
	for _, k := range keys {
		want[alwaysKeyHash(k)] = true
	}
	if session == "" || len(want) == 0 {
		return false, nil
	}
	hashes := make([]string, 0, len(want))
	for h := range want {
		hashes = append(hashes, h)
	}
	var n int
	err := d.pool.QueryRow(ctx, `SELECT count(*) FROM assistant_always WHERE session_id = $1 AND key_hash = ANY($2::text[])`,
		session, hashes).Scan(&n)
	return err == nil && n == len(hashes), err
}

// AlwaysGrant identifies a stored grant without exposing the command or path used as its key.
// Older records keep only the hash, so their exact scope cannot be reconstructed.
type AlwaysGrant struct {
	KeyHash   string    `json:"key_hash"`
	ToolName  string    `json:"tool_name"`
	CreatedAt time.Time `json:"created_at"`
}

func (d *DB) AlwaysGrants(ctx context.Context, session string) ([]AlwaysGrant, error) {
	rows, err := d.pool.Query(ctx, `SELECT key_hash, tool_name, created_at FROM assistant_always
		WHERE session_id = $1 ORDER BY created_at, key_hash`, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	grants := []AlwaysGrant{}
	for rows.Next() {
		var grant AlwaysGrant
		if err := rows.Scan(&grant.KeyHash, &grant.ToolName, &grant.CreatedAt); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

// RevokeAlways is idempotent and never removes a grant from another conversation.
func (d *DB) RevokeAlways(ctx context.Context, session, hash string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM assistant_always WHERE session_id = $1 AND key_hash = $2`, session, hash)
	return err
}
