package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- extension_tokens (docs/extensions/extensions.md "Permissions", #1156) ----

// Why an extension's token stopped (ExtensionToken.Reason).
const (
	// ExtensionTokenReplaced: a new start of its program was given a new one.
	ExtensionTokenReplaced = "replaced"
	// ExtensionTokenStopped: its run ended while it stays installed and on: upgraded, reconfigured, its grant or
	// secrets changed, a dependency or the manager's role changed, or it became incompatible.
	ExtensionTokenStopped = "stopped"
	// ExtensionTokenDisabled: it was switched off.
	ExtensionTokenDisabled = "disabled"
	// ExtensionTokenUninstalled: it was removed.
	ExtensionTokenUninstalled = "uninstalled"
	// ExtensionTokenManagerRestart: live when the manager that issued it stopped; revoked as it started again.
	ExtensionTokenManagerRestart = "manager_restart"
)

// extensionTokensKept is how long a revoked token's record is kept.
const extensionTokensKept = "7 days"

// ExtensionToken is the record of an extension's token: its hash, never the token.
type ExtensionToken struct {
	Hash      string
	Manager   string
	Extension string
	Name      string
	Scopes    []string
	All       bool // every scope (Scopes is empty)
	IssuedAt  time.Time
	RevokedAt *time.Time // nil while live
	Reason    string     // why it stopped, "" while live
}

// IssueExtensionToken records a token the manager issued: the live one its extension had is revoked as replaced, and
// the manager's revoked records older than a week go.
func (d *DB) IssueExtensionToken(ctx context.Context, t ExtensionToken) error {
	scopes := t.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE extension_tokens SET revoked_at = now(), reason = $3
			WHERE manager = $1 AND extension = $2 AND revoked_at IS NULL`, t.Manager, t.Extension, ExtensionTokenReplaced); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO extension_tokens (hash, manager, extension, name, scopes, all_scopes)
			VALUES ($1, $2, $3, $4, $5, $6)`, t.Hash, t.Manager, t.Extension, t.Name, scopes, t.All); err != nil {
			return err
		}
		return pruneExtensionTokens(ctx, tx, t.Manager)
	})
}

// RevokeExtensionToken records that the manager's token of hash stopped, and why. One revoked already keeps the
// reason it had.
func (d *DB) RevokeExtensionToken(ctx context.Context, manager, hash, reason string) error {
	_, err := d.pool.Exec(ctx, `UPDATE extension_tokens SET revoked_at = now(), reason = $3
		WHERE manager = $1 AND hash = $2 AND revoked_at IS NULL`, manager, hash, reason)
	return err
}

// RevokeExtensionTokens records that every token the manager has live stopped, and why, and drops its revoked
// records older than a week: what its last run issued, as it starts again. It says how many it revoked.
func (d *DB) RevokeExtensionTokens(ctx context.Context, manager, reason string) (int64, error) {
	var n int64
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE extension_tokens SET revoked_at = now(), reason = $2
			WHERE manager = $1 AND revoked_at IS NULL`, manager, reason)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		return pruneExtensionTokens(ctx, tx, manager)
	})
	return n, err
}

func pruneExtensionTokens(ctx context.Context, tx pgx.Tx, manager string) error {
	_, err := tx.Exec(ctx, `DELETE FROM extension_tokens
		WHERE manager = $1 AND revoked_at < now() - $2::interval`, manager, extensionTokensKept)
	return err
}

// ExtensionTokens are the manager's records, oldest first.
func (d *DB) ExtensionTokens(ctx context.Context, manager string) ([]ExtensionToken, error) {
	rows, err := d.pool.Query(ctx, `SELECT hash, manager, extension, name, scopes, all_scopes, issued_at, revoked_at, reason
		FROM extension_tokens WHERE manager = $1 ORDER BY issued_at, hash`, manager)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ExtensionToken, error) {
		var t ExtensionToken
		err := r.Scan(&t.Hash, &t.Manager, &t.Extension, &t.Name, &t.Scopes, &t.All, &t.IssuedAt, &t.RevokedAt, &t.Reason)
		return t, err
	})
}
