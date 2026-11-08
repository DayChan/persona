package extension

import (
	"context"
	"testing"
)

// upgradedNotes installs example-notes 1.2.0 and upgrades it to 1.3.0, both from directories.
func upgradedNotes(t *testing.T) (*Registry, *memStore) {
	t.Helper()
	ctx := context.Background()
	r, store, _ := newTestRegistry(t)
	pkg := t.TempDir()
	writePackage(t, pkg, nil)
	pv, err := r.Preview(ctx, pkg, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Install(ctx, pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	next := t.TempDir()
	writePackage(t, next, func(m map[string]any) { m["version"] = "1.3.0" })
	pv, err = r.Preview(ctx, next, "example-notes")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Upgrade(ctx, "example-notes", pv.ID, pv.Digest, "cli"); err != nil {
		t.Fatal(err)
	}
	return r, store
}

// rewriteRow edits the stored row and reloads the registry from it, as a restart does.
func rewriteRow(t *testing.T, r *Registry, store *memStore, edit func(*Record)) *Registry {
	t.Helper()
	store.mu.Lock()
	row := store.rows["example-notes"]
	edit(&row)
	store.rows["example-notes"] = row
	store.mu.Unlock()
	reopened := NewRegistry(r.dir, store, rot13{}, 202611100000)
	if err := reopened.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return reopened
}

// An upgrade keeps the provenance of the version it replaces next to PreviousVersion, and a rollback restores it.
func TestUpgradeKeepsPreviousProvenance(t *testing.T) {
	r, store := upgradedNotes(t)
	cur := r.Get("example-notes").Record
	if cur.PreviousVersion != "1.2.0" || cur.PreviousOrigin == "" || cur.PreviousMarketKey != "" {
		t.Fatalf("upgrade lost the previous version's provenance: %+v", cur)
	}
	r = rewriteRow(t, r, store, func(row *Record) {
		row.Origin, row.MarketKey = "market:official/example-notes", "ed25519:new"
		row.PreviousOrigin, row.PreviousMarketKey = "market:official/example-notes", "ed25519:old"
	})
	p, err := r.Rollback(context.Background(), "example-notes", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if p.Record.Version != "1.2.0" || p.Record.MarketKey != "ed25519:old" || p.Record.PreviousMarketKey != "" || p.Record.PreviousOrigin != "" {
		t.Fatalf("rollback did not restore the old version's provenance: %+v", p.Record)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if row := store.rows["example-notes"]; row.MarketKey != "ed25519:old" {
		t.Fatalf("stored key %q", row.MarketKey)
	}
}

// A rollback whose row has no snapshot of the old version's provenance (an upgrade from before it was kept) drops
// the verified key rather than lending the current version's to the old code.
func TestRollbackWithoutProvenanceSnapshotClearsMarketKey(t *testing.T) {
	r, store := upgradedNotes(t)
	r = rewriteRow(t, r, store, func(row *Record) {
		row.Origin, row.MarketKey = "market:official/example-notes", "ed25519:new"
		row.PreviousOrigin, row.PreviousMarketKey = "", ""
	})
	p, err := r.Rollback(context.Background(), "example-notes", "cli")
	if err != nil {
		t.Fatal(err)
	}
	if p.Record.Version != "1.2.0" || p.Record.MarketKey != "" {
		t.Fatalf("rollback kept the newer version's verified key: %+v", p.Record)
	}
	if p.Record.Origin != "market:official/example-notes" {
		t.Fatalf("an unknown previous origin changed the origin: %q", p.Record.Origin)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if row := store.rows["example-notes"]; row.MarketKey != "" {
		t.Fatalf("stored key %q", row.MarketKey)
	}
}
