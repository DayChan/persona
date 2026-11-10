package db

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/extension"
)

func TestExtensionIdentityStableCiphertextAndCAS(t *testing.T) {
	d, ctx := open(t), t.Context()
	const slot = "identity-old"
	cleanup := func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := d.pool.Exec(c, `DELETE FROM plugins WHERE id IN ('identity-old','identity-target')`)
		if err != nil {
			t.Error(err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	at := time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)
	old := extension.Record{ID: slot, Source: extension.SourceInstalled, Origin: "market:example/identity-old", Version: "1.0.0", Manifest: json.RawMessage(`{"id":"identity-old"}`), Granted: extension.Permissions{Network: []string{"example.com"}}, GrantedDigest: "original-permission-digest", Config: json.RawMessage(`{"saved":"retained"}`), Enabled: false, DisabledContributions: []string{"ui/example"}, PackOff: []string{"member-a"}, InstalledAt: at, UpdatedAt: at}
	if err := d.PutExtension(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := d.PutExtensionSecret(ctx, slot, "example", "synthetic-sealed-ciphertext"); err != nil {
		t.Fatal(err)
	}
	var secretAt time.Time
	if err := d.pool.QueryRow(ctx, `SELECT updated_at FROM plugin_secrets WHERE plugin=$1`, slot).Scan(&secretAt); err != nil {
		t.Fatal(err)
	}
	next := old
	next.ID, next.StorageID, next.PreviousID = "identity-new", slot, slot
	next.Version, next.PreviousVersion = "2.0.0", old.Version
	next.ExpectedID, next.ExpectedVersion = slot, old.Version
	next.Aliases = []string{slot}
	next.Manifest = json.RawMessage(`{"id":"identity-new"}`)
	if err := d.PutExtensionPlan(ctx, []extension.Record{next}); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Extensions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(rows, func(r extension.Record) bool { return r.StorageKey() == slot })
	if i < 0 {
		t.Fatal("stable slot missing")
	}
	got := rows[i]
	if got.ID != next.ID || got.PreviousID != slot || !reflect.DeepEqual(got.Aliases, []string{slot}) || got.Enabled != old.Enabled || got.GrantedDigest != old.GrantedDigest || !reflect.DeepEqual(got.Granted, old.Granted) || !reflect.DeepEqual(got.DisabledContributions, old.DisabledContributions) || !reflect.DeepEqual(got.PackOff, old.PackOff) || !got.InstalledAt.Equal(at) {
		t.Fatalf("identity transition lost state: %+v", got)
	}
	var cfg map[string]string
	if json.Unmarshal(got.Config, &cfg) != nil || cfg["saved"] != "retained" {
		t.Fatal("configuration changed")
	}
	sealed, err := d.ExtensionSecrets(ctx, slot)
	if err != nil || sealed["example"] != "synthetic-sealed-ciphertext" {
		t.Fatal("ciphertext changed", err)
	}
	var after time.Time
	if err = d.pool.QueryRow(ctx, `SELECT updated_at FROM plugin_secrets WHERE plugin=$1`, slot).Scan(&after); err != nil || !after.Equal(secretAt) {
		t.Fatal("secret rewritten", err)
	}
	if err = d.PutExtensionPlan(ctx, []extension.Record{next}); err == nil {
		t.Fatal("stale identity CAS accepted")
	}
	conflicting := old
	conflicting.ID = "identity-target"
	conflicting.StorageID = "identity-target"
	conflicting.Aliases = []string{slot}
	if err = d.PutExtension(ctx, conflicting); err == nil {
		t.Fatal("another slot stole historical name")
	}
	var count int
	if err = d.pool.QueryRow(ctx, `SELECT count(*) FROM plugins WHERE id='identity-target'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("conflicting record partially committed")
	}
	if err = d.DeleteExtensionRecord(ctx, old); err == nil {
		t.Fatal("stale process uninstalled a renamed identity")
	}
	if err = d.DeleteExtensionRecord(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err = d.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_identities WHERE plugin=$1`, slot).Scan(&count); err != nil || count != 0 {
		t.Fatal("aliases survived uninstall")
	}
	if err = d.PutExtensionPlan(ctx, []extension.Record{next}); err == nil {
		t.Fatal("CAS resurrected an uninstalled slot")
	}
	old.NewInstallation = true
	if err = d.PutExtensionPlan(ctx, []extension.Record{old}); err != nil {
		t.Fatal("name not released", err)
	}
	if err = d.PutExtensionPlan(ctx, []extension.Record{old}); err == nil {
		t.Fatal("concurrent new installation overwrote existing slot")
	}
}

// A plugin's row round-trips; its secrets go with it; the audit keeps what happened.
func TestExtensionRows(t *testing.T) {
	d, ctx := open(t), context.Background()
	at := time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)
	r := extension.Record{ID: "example-notes", Source: extension.SourceInstalled, Origin: "dir:/tmp/notes", Version: "1.2.0",
		Manifest: json.RawMessage(`{"id": "example-notes"}`), Granted: extension.Permissions{Network: []string{"notes.example.com"}},
		GrantedDigest: "d1", Enabled: true, Config: json.RawMessage(`{"folders": ["a"]}`), InstalledAt: at, UpdatedAt: at}
	if err := d.PutExtension(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Version, r.PreviousVersion, r.Remote, r.Enabled = "1.3.0", "1.2.0", &extension.Remote{RPCSocket: "/tmp/r.sock"}, false
	if err := d.PutExtension(ctx, r); err != nil {
		t.Fatal(err)
	}
	rows, err := d.Extensions(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	got := rows[0]
	if got.Version != "1.3.0" || got.PreviousVersion != "1.2.0" || got.Enabled || got.Remote == nil || got.Remote.RPCSocket != "/tmp/r.sock" ||
		got.Granted.Network[0] != "notes.example.com" || !got.InstalledAt.Equal(at) {
		t.Fatalf("%+v", got)
	}
	var cfg map[string][]string
	if json.Unmarshal(got.Config, &cfg) != nil || cfg["folders"][0] != "a" {
		t.Fatal(string(got.Config))
	}
	if err := d.PutExtensionSecret(ctx, "nope", "k", "s"); err == nil {
		t.Fatal("a secret of no plugin")
	}
	for _, v := range []string{"sealed-1", "sealed-2"} {
		if err := d.PutExtensionSecret(ctx, "example-notes", "api_key", v); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.PutExtensionSecret(ctx, "example-notes", "other", "sealed-3")
	_ = d.DeleteExtensionSecret(ctx, "example-notes", "other")
	if s, err := d.ExtensionSecrets(ctx, "example-notes"); err != nil || len(s) != 1 || s["api_key"] != "sealed-2" {
		t.Fatal(s, err)
	}
	if err := d.AddExtensionAudit(ctx, "example-notes", "install", "cli", nil); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteExtension(ctx, "example-notes"); err != nil {
		t.Fatal(err)
	}
	if s, _ := d.ExtensionSecrets(ctx, "example-notes"); len(s) != 0 {
		t.Fatal("its secrets went with it")
	}
	if rows, _ := d.Extensions(ctx); len(rows) != 0 {
		t.Fatal(rows)
	}
}

// installed_at is when the row was first written and no later write of the row moves it, whatever the record says (a
// recall goes by it: what the ledger wrote before an install was not that extension's): upgrades, configuration and
// switches write the row again, one at a time or in a plan, and a restart reads it back.
func TestExtensionRowKeepsInstalledAt(t *testing.T) {
	d, ctx := open(t), context.Background()
	first := time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)
	r := extension.Record{ID: "example-notes", Source: extension.SourceInstalled, Origin: "dir:/tmp/notes", Version: "1.2.0",
		Manifest: json.RawMessage(`{"id": "example-notes"}`), GrantedDigest: "d1", Enabled: true, InstalledAt: first, UpdatedAt: first}
	if err := d.PutExtension(ctx, r); err != nil {
		t.Fatal(err)
	}
	read := func() extension.Record {
		t.Helper()
		rows, err := d.Extensions(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatal(rows, err)
		}
		return rows[0]
	}
	later := first.Add(48 * time.Hour)
	r.Version, r.PreviousVersion, r.Enabled, r.InstalledAt, r.UpdatedAt = "1.3.0", "1.2.0", false, later, later
	if err := d.PutExtension(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := read(); !got.InstalledAt.Equal(first) || got.Version != "1.3.0" || !got.UpdatedAt.Equal(later) {
		t.Fatalf("after another write: installed %v (first %v), version %s, updated %v", got.InstalledAt, first, got.Version, got.UpdatedAt)
	}
	r.Version, r.InstalledAt = "1.4.0", later.Add(time.Hour)
	if err := d.PutExtensionPlan(ctx, []extension.Record{r}); err != nil {
		t.Fatal(err)
	}
	if got := read(); !got.InstalledAt.Equal(first) || got.Version != "1.4.0" {
		t.Fatalf("after a plan: installed %v (first %v), version %s", got.InstalledAt, first, got.Version)
	}
	// Deleting the row and writing it again is a new install.
	if err := d.DeleteExtension(ctx, "example-notes"); err != nil {
		t.Fatal(err)
	}
	r.InstalledAt = later.Add(2 * time.Hour)
	if err := d.PutExtension(ctx, r); err != nil {
		t.Fatal(err)
	}
	if got := read(); !got.InstalledAt.Equal(r.InstalledAt) {
		t.Fatalf("installed again: %v, want %v", got.InstalledAt, r.InstalledAt)
	}
}

// A market's row round-trips, its index and token with it.
func TestExtensionMarketRows(t *testing.T) {
	d, ctx := open(t), context.Background()
	at := time.Date(2026, 11, 4, 1, 2, 3, 0, time.UTC)
	r := extension.MarketRecord{ID: "m1", Spec: "github:example/plugins", Name: "Example", TokenSealed: "sealed", AddedAt: at}
	if err := d.PutExtensionMarket(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.Index, r.Warnings, r.Error, r.FetchedAt = json.RawMessage(`{"market_version": 1}`), []string{"w"}, "", &at
	if err := d.PutExtensionMarket(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := d.PutExtensionMarket(ctx, extension.MarketRecord{ID: "m2", Spec: "github:example/plugins", AddedAt: at}); err == nil {
		t.Fatal("the same market twice")
	}
	rows, err := d.ExtensionMarkets(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	if g := rows[0]; g.TokenSealed != "sealed" || !strings.Contains(string(g.Index), "market_version") || g.Warnings[0] != "w" || g.FetchedAt == nil || !g.AddedAt.Equal(at) {
		t.Fatalf("%+v", g)
	}
	if err := d.DeleteExtensionMarket(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	if rows, _ := d.ExtensionMarkets(ctx); len(rows) != 0 {
		t.Fatal(rows)
	}
}

// The audit reads back newest first, by extension (or connector, or the bot) or everyone's, at most limit rows, with
// its details as they were written.
func TestExtensionAudits(t *testing.T) {
	d, ctx := open(t), context.Background()
	ids := []string{"example-audit-a", "example-audit-b"}
	clean := func() {
		if _, err := d.pool.Exec(context.Background(), `DELETE FROM plugin_audit WHERE plugin = ANY($1)`, ids); err != nil {
			t.Fatal(err)
		}
	}
	clean()
	t.Cleanup(clean)
	for _, r := range []struct{ id, action, actor, detail string }{
		{ids[0], "install", "personad extension", `{"version":"1.0.0"}`},
		{ids[1], "connector_signed_in", "device:dev_phone", `{"account":"Example"}`},
		{ids[0], "connector_logout", "hub token", ``},
	} {
		if err := d.AddExtensionAudit(ctx, r.id, r.action, r.actor, json.RawMessage(r.detail)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := d.ExtensionAudits(ctx, ids[0], 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Action != "connector_logout" || got[1].Action != "install" || got[1].Actor != "personad extension" ||
		string(got[0].Detail) != "{}" || got[1].At.After(got[0].At) {
		t.Fatalf("by extension: %+v", got)
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(got[1].Detail, &v); err != nil || v.Version != "1.0.0" {
		t.Fatalf("detail %s (%v)", got[1].Detail, err)
	}
	all, err := d.ExtensionAudits(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Plugin != ids[0] || all[0].Action != "connector_logout" || all[1].Plugin != ids[1] {
		t.Fatalf("everyone's, at most 2: %+v", all)
	}
	if none, err := d.ExtensionAudits(ctx, "example-audit-none", 10); err != nil || len(none) != 0 {
		t.Fatalf("an extension without rows: %+v (%v)", none, err)
	}
}

// Both audit statements stay on an index under a custom and a generic plan: everyone's walks plugin_audit_at and one
// plugin's reads plugin_audit_plugin; neither scans the table.
func TestExtensionAuditsIndexed(t *testing.T) {
	d, ctx := open(t), context.Background()
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The rows, the statistics and the settings go with the rollback.
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO plugin_audit (plugin, action, actor, at)
		SELECT 'example-audit-' || (g % 40), 'install', 'cli', now() - g * interval '1 minute'
		FROM generate_series(1, 6000) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ANALYZE plugin_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, sql, types, args, index string
	}{
		{"audit_all", extensionAuditsSQL, "int", "100", "plugin_audit_at"},
		{"audit_plugin", extensionAuditsPluginSQL, "text, int", "'example-audit-7', 100", "plugin_audit_plugin"},
	} {
		if _, err := tx.Exec(ctx, `PREPARE `+c.name+`(`+c.types+`) AS `+c.sql); err != nil {
			t.Fatal(err)
		}
		// A prepared statement outlives the transaction; the connection goes back to the pool.
		defer func() { _, _ = d.pool.Exec(ctx, `DEALLOCATE `+c.name) }()
		for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
			if _, err := tx.Exec(ctx, `SET LOCAL plan_cache_mode = `+mode); err != nil {
				t.Fatal(err)
			}
			// EXPLAIN EXECUTE takes no bind parameters, so the values are written in.
			rows, err := tx.Query(ctx, `EXPLAIN EXECUTE `+c.name+`(`+c.args+`)`)
			if err != nil {
				t.Fatal(err)
			}
			lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(lines, "\n")
			if !strings.Contains(plan, c.index) || strings.Contains(plan, "Seq Scan") {
				t.Fatalf("%s %s: want %s\nplan:\n%s", c.name, mode, c.index, plan)
			}
		}
	}
}
