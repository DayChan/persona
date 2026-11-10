package db

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nwverse/persona/server/internal/extension"
)

// ---- extensions (docs/extensions/extensions.md) ----

// Extensions are the registry's rows.
func (d *DB) Extensions(ctx context.Context) ([]extension.Record, error) {
	rows, err := d.pool.Query(ctx, `SELECT id, source, origin, version, previous_version, manifest, granted, granted_digest, enabled,
		config, remote, disabled_contributions, installed_at, updated_at, market_key, previous_origin, previous_market_key, public_id, previous_id, pack_off,
 ARRAY(SELECT name FROM plugin_identities WHERE plugin = plugins.id AND name <> COALESCE(NULLIF(public_id, ''), plugins.id) ORDER BY name)
 FROM plugins ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []extension.Record
	for rows.Next() {
		var r extension.Record
		var granted, remote, disabled, packOff []byte
		var publicID string
		if err := rows.Scan(&r.ID, &r.Source, &r.Origin, &r.Version, &r.PreviousVersion, &r.Manifest, &granted, &r.GrantedDigest, &r.Enabled,
			&r.Config, &remote, &disabled, &r.InstalledAt, &r.UpdatedAt, &r.MarketKey, &r.PreviousOrigin, &r.PreviousMarketKey, &publicID, &r.PreviousID, &packOff, &r.Aliases); err != nil {
			return nil, err
		}
		if publicID != "" {
			r.StorageID, r.ID = r.ID, publicID
		}
		if len(r.Aliases) == 0 {
			r.Aliases = nil
		}
		if err := json.Unmarshal(granted, &r.Granted); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(disabled, &r.DisabledContributions); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(packOff, &r.PackOff); err != nil {
			return nil, err
		}
		if len(r.PackOff) == 0 {
			r.PackOff = nil
		}
		if len(remote) > 0 && string(remote) != "null" {
			r.Remote = &extension.Remote{}
			if err := json.Unmarshal(remote, r.Remote); err != nil {
				return nil, err
			}
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutExtension writes an extension's row.
func (d *DB) PutExtension(ctx context.Context, r extension.Record) error {
	return d.PutExtensionPlan(ctx, []extension.Record{r})
}

type extensionExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// PutExtensionPlan changes all records atomically, without deleting or rewriting any existing secrets.
func (d *DB) PutExtensionPlan(ctx context.Context, records []extension.Record) error {
	ordered := slices.Clone(records)
	slices.SortFunc(ordered, func(a, b extension.Record) int { return strings.Compare(a.StorageKey(), b.StorageKey()) })
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	for _, record := range ordered {
		if err = putExtension(ctx, tx, record); err != nil {
			return err
		}
	}
	return extensionCommitResult(tx.Commit(ctx))
}

func extensionCommitResult(err error) error {
	if err == nil {
		return nil
	}
	var pgError *pgconn.PgError
	definite := errors.As(err, &pgError) && (strings.HasPrefix(pgError.Code, "23") ||
		strings.HasPrefix(pgError.Code, "40") && pgError.Code != "40003" || pgError.Code == "P0001" || pgError.Code == "25P02")
	if errors.Is(err, pgx.ErrTxCommitRollback) || definite || pgconn.SafeToRetry(err) {
		return err
	}
	return &extension.CommitUnknownError{Err: err}
}

func putExtension(ctx context.Context, executor extensionExecutor, r extension.Record) error {
	if r.ExpectedID != "" {
		var id, version string
		if err := executor.QueryRow(ctx, `SELECT COALESCE(NULLIF(public_id, ''), id), version FROM plugins WHERE id = $1 FOR UPDATE`, r.StorageKey()).Scan(&id, &version); err != nil {
			return err
		}
		if id != r.ExpectedID || version != r.ExpectedVersion {
			return extension.ErrIdentity
		}
	}
	granted, err := json.Marshal(r.Granted)
	if err != nil {
		return err
	}
	var remote []byte
	if r.Remote != nil {
		if remote, err = json.Marshal(r.Remote); err != nil {
			return err
		}
	}
	config := r.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}
	disabled := r.DisabledContributions
	if disabled == nil {
		disabled = []string{}
	}
	off, err := json.Marshal(disabled)
	if err != nil {
		return err
	}
	packOff := r.PackOff
	if packOff == nil {
		packOff = []string{}
	}
	packOffJSON, err := json.Marshal(packOff)
	if err != nil {
		return err
	}
	publicID := ""
	if r.StorageID != "" {
		publicID = r.ID
	}
	tag, err := executor.Exec(ctx, `INSERT INTO plugins (id, source, origin, version, previous_version, manifest, granted, granted_digest, enabled,
		config, remote, installed_at, updated_at, disabled_contributions, market_key, previous_origin, previous_market_key, public_id, previous_id, pack_off)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $23)
		ON CONFLICT (id) DO UPDATE SET source = $2, origin = $3, version = $4, previous_version = $5, manifest = $6, granted = $7,
		granted_digest = $8, enabled = $9, config = $10, remote = $11, updated_at = $13, disabled_contributions = $14, market_key = $15,
		previous_origin = $16, previous_market_key = $17, public_id = $18, previous_id = $19, pack_off = $23
 WHERE NOT $22 AND ($20 = '' OR COALESCE(NULLIF(plugins.public_id, ''), plugins.id) = $20 AND plugins.version = $21)`,
		r.StorageKey(), r.Source, r.Origin, r.Version, r.PreviousVersion, []byte(r.Manifest), granted, r.GrantedDigest, r.Enabled,
		[]byte(config), remote, r.InstalledAt, r.UpdatedAt, off, r.MarketKey, r.PreviousOrigin, r.PreviousMarketKey, publicID, r.PreviousID, r.ExpectedID, r.ExpectedVersion, r.NewInstallation, packOffJSON)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return extension.ErrIdentity
	}
	names := append(slices.Clone(r.Aliases), r.ID)
	// Even after a rename, the original storage key stays reserved to this slot.
	names = append(names, r.StorageKey())
	slices.Sort(names)
	names = slices.Compact(names)
	for _, name := range names {
		tag, err = executor.Exec(ctx, `INSERT INTO plugin_identities (name, plugin) VALUES ($1, $2)
   ON CONFLICT (name) DO UPDATE SET plugin = plugin_identities.plugin WHERE plugin_identities.plugin = EXCLUDED.plugin`, name, r.StorageKey())
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return extension.ErrIdentity
		}
	}
	return nil
}

// DeleteExtensionRecord deletes only the installation identity/version that was
// prepared for uninstall, keeping aliases and secret ownership in the same transaction.
func (d *DB) DeleteExtensionRecord(ctx context.Context, row extension.Record) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	tag, err := tx.Exec(ctx, `DELETE FROM plugins WHERE id=$1 AND COALESCE(NULLIF(public_id,''),id)=$2 AND version=$3 AND installed_at=$4`, row.StorageKey(), row.ID, row.Version, row.InstalledAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return extension.ErrIdentity
	}
	return extensionCommitResult(tx.Commit(ctx))
}

// DeleteExtension deletes an extension's row and (cascading) its secrets.
func (d *DB) DeleteExtension(ctx context.Context, id string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM plugins WHERE id = $1`, id)
	return err
}

// ExtensionSecrets are an extension's sealed secrets by name.
func (d *DB) ExtensionSecrets(ctx context.Context, id string) (map[string]string, error) {
	rows, err := d.pool.Query(ctx, `SELECT name, sealed FROM plugin_secrets WHERE plugin = $1`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, sealed string
		if err := rows.Scan(&name, &sealed); err != nil {
			return nil, err
		}
		out[name] = sealed
	}
	return out, rows.Err()
}

func (d *DB) PutExtensionSecret(ctx context.Context, id, name, sealed string) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO plugin_secrets (plugin, name, sealed, updated_at) VALUES ($1, $2, $3, now())
		ON CONFLICT (plugin, name) DO UPDATE SET sealed = $3, updated_at = now()`, id, name, sealed)
	return err
}

func (d *DB) DeleteExtensionSecret(ctx context.Context, id, name string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM plugin_secrets WHERE plugin = $1 AND name = $2`, id, name)
	return err
}

// AddExtensionAudit records a change to an extension.
func (d *DB) AddExtensionAudit(ctx context.Context, id, action, actor string, detail json.RawMessage) error {
	if len(detail) == 0 {
		detail = json.RawMessage(`{}`)
	}
	_, err := d.pool.Exec(ctx, `INSERT INTO plugin_audit (plugin, action, actor, detail) VALUES ($1, $2, $3, $4)`, id, action, actor, []byte(detail))
	return err
}

// ExtensionAudit is one row of plugin_audit: what was done to an extension, a connector (connector_… actions) or the
// Feishu bot (bot_…), by whom and when.
type ExtensionAudit struct {
	Plugin string          `json:"plugin"`
	Action string          `json:"action"`
	Actor  string          `json:"actor"`
	Detail json.RawMessage `json:"detail"`
	At     time.Time       `json:"at"`
}

// The statements of ExtensionAudits, everyone's rows and one plugin's. The single statement they replace matched an
// empty plugin with an OR ("$1 = empty OR plugin = $1"), which no index serves, so everyone's listing read the whole
// append-only table and sorted it. Everyone's walks plugin_audit_at; one plugin's reads plugin_audit_plugin.
// TestExtensionAuditsIndexed checks both plans.
const (
	extensionAuditsSQL = `SELECT plugin, action, actor, detail, at FROM plugin_audit
		ORDER BY at DESC, id DESC LIMIT $1`
	extensionAuditsPluginSQL = `SELECT plugin, action, actor, detail, at FROM plugin_audit
		WHERE plugin = $1 ORDER BY at DESC, id DESC LIMIT $2`
)

// ExtensionAudits is the audit newest first: plugin's rows, or everyone's when plugin is "", at most limit of them.
func (d *DB) ExtensionAudits(ctx context.Context, plugin string, limit int) ([]ExtensionAudit, error) {
	var rows pgx.Rows
	var err error
	if plugin == "" {
		rows, err = d.pool.Query(ctx, extensionAuditsSQL, limit)
	} else {
		rows, err = d.pool.Query(ctx, extensionAuditsPluginSQL, plugin, limit)
	}
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ExtensionAudit, error) {
		var a ExtensionAudit
		var detail []byte
		err := r.Scan(&a.Plugin, &a.Action, &a.Actor, &detail, &a.At)
		a.Detail = detail
		return a, err
	})
}

var _ extension.Store = (*DB)(nil)
var _ extension.PlanStore = (*DB)(nil)

// ---- extension markets (docs/extensions/extensions.md "Extension market") ----

// ExtensionMarkets are the registered markets, oldest first.
func (d *DB) ExtensionMarkets(ctx context.Context) ([]extension.MarketRecord, error) {
	rows, err := d.pool.Query(ctx, `SELECT id, spec, name, token_sealed, index_json, warnings, error, fetched_at, added_at,
		signing_key, signature FROM plugin_markets ORDER BY added_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []extension.MarketRecord
	for rows.Next() {
		var r extension.MarketRecord
		var index, warnings, signature []byte
		if err := rows.Scan(&r.ID, &r.Spec, &r.Name, &r.TokenSealed, &index, &warnings, &r.Error, &r.FetchedAt, &r.AddedAt,
			&r.SigningKey, &signature); err != nil {
			return nil, err
		}
		if len(signature) > 0 && string(signature) != "null" {
			if err := json.Unmarshal(signature, &r.Signature); err != nil {
				return nil, err
			}
		}
		if len(index) > 0 && string(index) != "null" {
			r.Index = index
		}
		if err := json.Unmarshal(warnings, &r.Warnings); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutExtensionMarket writes a market's row.
func (d *DB) PutExtensionMarket(ctx context.Context, r extension.MarketRecord) error {
	warnings, err := json.Marshal(append([]string{}, r.Warnings...))
	if err != nil {
		return err
	}
	var index []byte
	if len(r.Index) > 0 {
		index = r.Index
	}
	signature, err := json.Marshal(r.Signature)
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO plugin_markets (id, spec, name, token_sealed, index_json, warnings, error, fetched_at, added_at,
		signing_key, signature)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (id) DO UPDATE SET spec = $2, name = $3, token_sealed = $4, index_json = $5, warnings = $6, error = $7, fetched_at = $8,
			signing_key = $10, signature = $11`,
		r.ID, r.Spec, r.Name, r.TokenSealed, index, warnings, r.Error, r.FetchedAt, r.AddedAt, r.SigningKey, signature)
	return err
}

// DeleteExtensionMarket forgets a market (the extensions installed from it stay).
func (d *DB) DeleteExtensionMarket(ctx context.Context, id string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM plugin_markets WHERE id = $1`, id)
	return err
}
