package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ---- settings ----

// Each side has its own settings (docs/server/server.md "Migrations"): the hub's in hub_settings, a manager's in settings.
// A database opened for the hub alone reads and writes the hub's; one opened for a manager (or for both, in tests)
// the manager's. Neither side reads the other's keys: what one needs of the other comes over the bus.

// hubKeys are the hub's settings keys from before hub_settings (migrations/hub/0421_hub_settings.sql copies them
// once), and hubKeyPrefixes the families of them. On a database shared with the default manager they are kept in
// settings too, for a hub build from before: every write of one goes through PutSetting / TakeSetting (CompareSetting
// does not keep them there; the hub uses it for none).
var (
	hubKeys = []string{"hub_id", "user_locale", "look", "hub_update", "hub.event_log", "hub.assistant_profile",
		"hub.extensions_report", "managers.default", "managers.machines", "sessions.deleted", "persona_tiers", "pins",
		"mutes", "mesh.epoch", "mesh.records"}
	hubKeyPrefixes = []string{"codex_inventory/"}
)

// isHubKey: key is one of the hub's from before hub_settings.
func isHubKey(key string) bool {
	if slices.Contains(hubKeys, key) {
		return true
	}
	for _, p := range hubKeyPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// keepForOlderHub writes (raw nil: deletes) a hub key in the shared settings table as well, in tx with the write to
// hub_settings (one now(): the same updated_at), so a hub build from before hub_settings that is switched back to
// starts from the values in effect, and followOlderHub then takes only what that build changed after.
func (d *DB) keepForOlderHub(ctx context.Context, tx pgx.Tx, key string, raw []byte) error {
	if raw == nil {
		_, err := tx.Exec(ctx, `DELETE FROM settings WHERE key = $1`, key)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, now())
		ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = now()`, key, raw)
	return err
}

// followOlderHub takes up what a hub build from before hub_settings wrote in the settings table it shares with the
// default manager, at every start of the hub: a switch back to such a build (blue-green, docs/operations/hub-restart.md) and
// forward again keeps the changes made meanwhile. A key it wrote later than this build is taken, its event log
// hand-over too; taking a hand-over removes it from both tables (a hand-over is read once: left there, a later switch
// back would resume a stale stream).
func (d *DB) followOlderHub(ctx context.Context) error {
	_, err := d.sharedWithOlderHub(ctx)
	return err
}

// sharedWithOlderHub: the hub's database has the default manager's settings table, where a hub build from before
// hub_settings reads and writes the hub's keys. It is looked for until it is there (on a fresh install the hub
// opens the database before its default manager makes that table); the first time it is, that build's later writes
// are taken up and the values in effect are written there too (syncOlderHub).
func (d *DB) sharedWithOlderHub(ctx context.Context) (bool, error) {
	if d.sets != HubSet {
		return false, nil
	}
	if d.olderHub.Load() {
		return true, nil
	}
	var shared bool
	if err := d.pool.QueryRow(ctx, `SELECT to_regclass('settings') IS NOT NULL`).Scan(&shared); err != nil || !shared {
		return false, err
	}
	d.olderMu.Lock()
	defer d.olderMu.Unlock()
	if d.olderHub.Load() {
		return true, nil
	}
	if err := d.syncOlderHub(ctx); err != nil {
		if d.olderHubGone(err) {
			return false, nil // dropped since it was looked for
		}
		return false, err
	}
	d.olderHub.Store(true)
	return true, nil
}

// syncOlderHub, in one transaction: takes up what an older build wrote to settings later than this one did (its
// event log hand-over too), drops this build's own hand-over that an older build took meanwhile (a hand-over is read
// once: resumed again, it would replay a stream that went on without it), and writes the hub's other values in effect
// to settings, so an older build switched back to starts from them. The shared table is locked first, as every write of
// a hub key starts there (keepForOlderHub, then hub_settings): one lock order, rows that are not there yet included.
func (d *DB) syncOlderHub(ctx context.Context) error {
	var like []string
	for _, p := range hubKeyPrefixes {
		like = append(like, strings.ReplaceAll(p, "_", `\_`)+"%")
	}
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `LOCK TABLE settings IN SHARE ROW EXCLUSIVE MODE`); err != nil {
			return fmt.Errorf("hub settings: lock the shared table: %w", err)
		}
		// Since when this database's hub keys are kept in settings too: only a hand-over written from then on was there
		// for an older build to take.
		if _, err := tx.Exec(ctx, `INSERT INTO hub_settings (key, value, updated_at) VALUES ($1, 'true', now())
			ON CONFLICT (key) DO NOTHING`, mirroredSinceKey); err != nil {
			return fmt.Errorf("hub settings: %s: %w", mirroredSinceKey, err)
		}
		// Written to both tables when this build stopped (keepForOlderHub), gone from settings: an older build served in
		// between and took it. Taking it removes both (TakeSetting); until then both copies stay.
		if _, err := tx.Exec(ctx, `DELETE FROM hub_settings h WHERE h.key = 'hub.event_log'
			AND NOT EXISTS (SELECT 1 FROM settings WHERE key = 'hub.event_log')
			AND h.updated_at >= (SELECT updated_at FROM hub_settings WHERE key = $1)`, mirroredSinceKey); err != nil {
			return fmt.Errorf("hub settings: the event log an older build took: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO hub_settings AS h (key, value, updated_at)
			SELECT key, value, updated_at FROM settings WHERE key = ANY($1) OR key LIKE ANY($2)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at
			WHERE h.updated_at < EXCLUDED.updated_at`, hubKeys, like); err != nil {
			return fmt.Errorf("hub settings: what an older build wrote: %w", err)
		}
		// Not the event log: a hand-over only in hub_settings was never there for an older build (written before
		// settings was), and stays this build's to take (loadEventLog).
		if _, err := tx.Exec(ctx, `INSERT INTO settings AS s (key, value, updated_at)
			SELECT key, value, updated_at FROM hub_settings WHERE (key = ANY($1) OR key LIKE ANY($2)) AND key <> 'hub.event_log'
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at
			WHERE s.updated_at < EXCLUDED.updated_at`, hubKeys, like); err != nil {
			return fmt.Errorf("hub settings: kept for an older build: %w", err)
		}
		return nil
	})
}

// mirroredSinceKey is the hub's setting whose updated_at is when it began keeping its keys in the shared settings
// table too (syncOlderHub's first run on the database).
const mirroredSinceKey = "hub.settings_mirrored_since"

// olderHubGone: err is the shared settings table missing (dropped since it was found): the hub's keys are no longer
// kept there, and the write is tried again on hub_settings alone.
func (d *DB) olderHubGone(err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "42P01" && d.sets == HubSet {
		d.olderHub.Store(false)
		return true
	}
	return false
}

// settingsTable is the table Setting and the rest use: hub_settings for the hub's database, settings otherwise.
func (d *DB) settingsTable() string {
	if d.sets == HubSet {
		return "hub_settings"
	}
	return "settings"
}

// Setting decodes the value stored under key into out; it reports false when the key is unset.
func (d *DB) Setting(ctx context.Context, key string, out any) (bool, error) {
	var raw []byte
	err := d.pool.QueryRow(ctx, `SELECT value FROM `+d.settingsTable()+` WHERE key = $1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, out)
}

func (d *DB) PutSetting(ctx context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	put := func(q execer) error {
		_, err := q.Exec(ctx, `INSERT INTO `+d.settingsTable()+` (key, value, updated_at) VALUES ($1, $2, now())
			ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = now()`, key, raw)
		return err
	}
	if !isHubKey(key) {
		return put(d.pool)
	}
	if shared, err := d.sharedWithOlderHub(ctx); err != nil || !shared {
		if err != nil {
			return err
		}
		return put(d.pool)
	}
	err = pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if err := d.keepForOlderHub(ctx, tx, key, raw); err != nil { // the shared table first: one lock order
			return err
		}
		return put(tx)
	})
	if d.olderHubGone(err) {
		return put(d.pool)
	}
	return err
}

// PutSettings writes several settings in one transaction: all of them or none (a model profile's switch writes the
// categories it sets, the launchers and what it replaced together, docs/assistant/model-profiles.md). Only keys of this
// database's own: a hub key, kept for an older hub too, goes through PutSetting.
func (d *DB) PutSettings(ctx context.Context, values map[string]any) error {
	keys := slices.Sorted(maps.Keys(values)) // one lock order
	raws := make([][]byte, len(keys))
	for i, key := range keys {
		if isHubKey(key) {
			return fmt.Errorf("put settings: %s is a hub key", key)
		}
		raw, err := json.Marshal(values[key])
		if err != nil {
			return err
		}
		raws[i] = raw
	}
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		for i, key := range keys {
			if _, err := tx.Exec(ctx, `INSERT INTO `+d.settingsTable()+` (key, value, updated_at) VALUES ($1, $2, now())
				ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = now()`, key, raws[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// execer is a pool or a transaction.
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// TakeSetting is Setting that also removes the key, in one statement: a value meant to be read once (the event log a
// stopping hub hands to the next one, docs/operations/hub-restart.md) is never read twice.
func (d *DB) TakeSetting(ctx context.Context, key string, out any) (bool, error) {
	var raw []byte
	shared := false
	if isHubKey(key) {
		var err error
		if shared, err = d.sharedWithOlderHub(ctx); err != nil {
			return false, err
		}
	}
	take := func(shared bool) error {
		return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
			if shared { // taken: not there for an older build either (the shared table first: one lock order)
				if err := d.keepForOlderHub(ctx, tx, key, nil); err != nil {
					return err
				}
			}
			return tx.QueryRow(ctx, `DELETE FROM `+d.settingsTable()+` WHERE key = $1 RETURNING value`, key).Scan(&raw)
		})
	}
	err := take(shared)
	if shared && d.olderHubGone(err) {
		err = take(false)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, out)
}

// CompareSetting atomically replaces a versioned setting without losing another writer's update.
// A cleared binding keeps its revision, so restoring defaults does not reset concurrency history.
func (d *DB) CompareSetting(ctx context.Context, key string, revision int64, value any) (bool, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	table := d.settingsTable()
	tag, err := d.pool.Exec(ctx, `INSERT INTO `+table+` AS s (key, value, updated_at)
		SELECT $1, $2::jsonb, now() WHERE $3::bigint = 0
		ON CONFLICT (key) DO UPDATE SET value = $2::jsonb, updated_at = now()
		WHERE COALESCE((s.value->>'revision')::bigint, 0) = $3`, key, raw, revision)
	if revision != 0 && err == nil && tag.RowsAffected() == 0 {
		tag, err = d.pool.Exec(ctx, `UPDATE `+table+` SET value = $2::jsonb, updated_at = now()
			WHERE key = $1 AND COALESCE((value->>'revision')::bigint, 0) = $3`, key, raw, revision)
	}
	return tag.RowsAffected() == 1, err
}
