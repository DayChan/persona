package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// digest is a stand-in for the n-th token's SHA-256 (64 hex digits).
func digest(n int) string { return fmt.Sprintf("%064x", n) }

// An extension's token is recorded by its hash with whose it is and what it opens (#1156). A new one replaces the one
// its extension had; a revoked one keeps when and why it stopped; a manager's start revokes only its own live ones; a
// week after revocation the record goes.
func TestExtensionTokens(t *testing.T) {
	d, ctx := open(t), context.Background()
	if _, err := d.pool.Exec(ctx, `TRUNCATE extension_tokens`); err != nil {
		t.Fatal(err)
	}
	h1, h2, h3, h4, h5, h6 := digest(1), digest(2), digest(3), digest(4), digest(5), digest(6)
	issue := func(hash, manager, ext string, scopes []string, all bool) {
		t.Helper()
		if err := d.IssueExtensionToken(ctx, ExtensionToken{Hash: hash, Manager: manager, Extension: ext, Name: "Example " + ext,
			Scopes: scopes, All: all}); err != nil {
			t.Fatal(err)
		}
	}
	byHash := func(manager string) map[string]ExtensionToken {
		t.Helper()
		rows, err := d.ExtensionTokens(ctx, manager)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]ExtensionToken{}
		for _, r := range rows {
			if r.IssuedAt.IsZero() || r.Manager != manager {
				t.Fatalf("row %+v", r)
			}
			out[r.Hash] = r
		}
		return out
	}
	live := func(r ExtensionToken) bool { return r.RevokedAt == nil && r.Reason == "" }

	issue(h1, "assistant", "example-a", []string{"kb.read"}, false)
	issue(h2, "assistant", "example-b", nil, true)
	issue(h3, "example-manager", "example-a", []string{"notify"}, false)
	issue(h4, "assistant", "example-a", []string{"kb.read", "inbox.read"}, false) // its next start
	rows := byHash("assistant")
	if len(rows) != 3 {
		t.Fatalf("the assistant's rows: %+v", rows)
	}
	if r := rows[h1]; r.RevokedAt == nil || r.Reason != ExtensionTokenReplaced || r.Extension != "example-a" {
		t.Fatalf("the token its next start replaced: %+v", r)
	}
	if r := rows[h4]; !live(r) || r.Name != "Example example-a" || !slices.Equal(r.Scopes, []string{"kb.read", "inbox.read"}) || r.All {
		t.Fatalf("its live token: %+v", r)
	}
	if r := rows[h2]; !live(r) || !r.All || len(r.Scopes) != 0 {
		t.Fatalf("a token of every scope: %+v", r)
	}
	if r := byHash("example-manager")[h3]; !live(r) {
		t.Fatalf("another manager's token of the same extension id is its own: %+v", r)
	}

	if err := d.RevokeExtensionToken(ctx, "example-manager", h2, ExtensionTokenDisabled); err != nil {
		t.Fatal(err)
	}
	if r := byHash("assistant")[h2]; !live(r) {
		t.Fatalf("another manager revoked this one's token: %+v", r)
	}
	if err := d.RevokeExtensionToken(ctx, "assistant", h2, ExtensionTokenDisabled); err != nil {
		t.Fatal(err)
	}
	if err := d.RevokeExtensionToken(ctx, "assistant", h2, ExtensionTokenUninstalled); err != nil {
		t.Fatal(err)
	}
	if r := byHash("assistant")[h2]; r.RevokedAt == nil || r.Reason != ExtensionTokenDisabled {
		t.Fatalf("revoked once, it keeps why: %+v", r)
	}

	// The manager starts again: what is still live from its last run is revoked, nothing else.
	n, err := d.RevokeExtensionTokens(ctx, "assistant", ExtensionTokenManagerRestart)
	if err != nil || n != 1 {
		t.Fatalf("revoked %d: %v", n, err)
	}
	rows = byHash("assistant")
	if r := rows[h4]; r.RevokedAt == nil || r.Reason != ExtensionTokenManagerRestart {
		t.Fatalf("live from the last run: %+v", r)
	}
	if rows[h1].Reason != ExtensionTokenReplaced || rows[h2].Reason != ExtensionTokenDisabled {
		t.Fatalf("revoked before the restart, they keep why: %+v", rows)
	}
	if r := byHash("example-manager")[h3]; !live(r) {
		t.Fatalf("another manager's start touched this one's token: %+v", r)
	}

	// At most one live token per extension of a manager, whatever writes the table.
	_, err = d.pool.Exec(ctx, `INSERT INTO extension_tokens (hash, manager, extension) VALUES ($1, 'example-manager', 'example-a')`, h5)
	if pe := (*pgconn.PgError)(nil); !errors.As(err, &pe) || pe.Code != "23505" {
		t.Fatalf("a second live token of one extension: %v", err)
	}
	// Only a digest is taken for the hash, and revoked_at and reason go together.
	for _, q := range []string{
		`INSERT INTO extension_tokens (hash, manager, extension) VALUES ('pext_` + digest(7)[:43] + `', 'example-manager', 'example-z')`,
		`INSERT INTO extension_tokens (hash, manager, extension) VALUES ('` + digest(8)[:63] + `', 'example-manager', 'example-z')`,
		`INSERT INTO extension_tokens (hash, manager, extension, reason) VALUES ('` + digest(9) + `', 'example-manager', 'example-z', 'disabled')`,
		`INSERT INTO extension_tokens (hash, manager, extension, revoked_at) VALUES ('` + digest(10) + `', 'example-manager', 'example-z', now())`,
	} {
		_, err := d.pool.Exec(ctx, q)
		if pe := (*pgconn.PgError)(nil); !errors.As(err, &pe) || pe.Code != "23514" {
			t.Fatalf("%s: %v, want a check violation", q, err)
		}
	}

	// A week after revocation the record goes, at the next issue or start; the live and the recent stay.
	if _, err := d.pool.Exec(ctx, `UPDATE extension_tokens SET revoked_at = now() - interval '8 days' WHERE hash = $1`, h1); err != nil {
		t.Fatal(err)
	}
	issue(h6, "assistant", "example-c", []string{"notify"}, false)
	rows = byHash("assistant")
	if _, ok := rows[h1]; ok || len(rows) != 3 {
		t.Fatalf("after a week: %+v", rows)
	}
	if _, err := d.pool.Exec(ctx, `UPDATE extension_tokens SET revoked_at = now() - interval '8 days' WHERE hash = $1`, h2); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RevokeExtensionTokens(ctx, "assistant", ExtensionTokenManagerRestart); err != nil {
		t.Fatal(err)
	}
	if rows = byHash("assistant"); len(rows) != 2 || rows[h4].Reason != ExtensionTokenManagerRestart || rows[h6].Reason != ExtensionTokenManagerRestart {
		t.Fatalf("after a week, at a start: %+v", rows)
	}
	if r := byHash("example-manager")[h3]; !live(r) {
		t.Fatalf("another manager's prune touched this one's token: %+v", r)
	}
}

// The record holds a token's hash and what it was for, never the token: no column could hold one.
func TestExtensionTokensColumns(t *testing.T) {
	d, ctx := open(t), context.Background()
	rows, err := d.pool.Query(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'extension_tokens' ORDER BY column_name`)
	if err != nil {
		t.Fatal(err)
	}
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"all_scopes", "extension", "hash", "issued_at", "manager", "name", "reason", "revoked_at", "scopes"}
	if !slices.Equal(cols, want) {
		t.Fatalf("columns %v, want %v", cols, want)
	}
}
