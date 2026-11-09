package db

import (
	"encoding/json"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// storageBytesRows is how many of key's spaces there are, failing when one's stored state_bytes is not the encoded
// size of its state (what the budget summed before 0452).
func storageBytesRows(t *testing.T, d *DB, key string) int {
	t.Helper()
	var rows, wrong int
	if err := d.pool.QueryRow(t.Context(), `SELECT count(*), count(*) FILTER (WHERE state_bytes IS DISTINCT FROM octet_length(state::text))
		FROM extension_storage_spaces WHERE extension_key=$1`, key).Scan(&rows, &wrong); err != nil {
		t.Fatal(err)
	}
	if wrong != 0 {
		t.Fatalf("%d of %d spaces of %s have a state_bytes that is not their encoded size", wrong, rows, key)
	}
	return rows
}

// Every way a space is written (created, updated, retained on uninstall, inserted raw, restored) leaves state_bytes at
// the encoded size, so the budget and the retained list read the same numbers as before.
func TestStorageBytesColumnTracksEveryWrite(t *testing.T) {
	d := open(t)
	row, scope := storageFixture(t, d, "fixture-storage-bytes")
	cleanupStorageFixture(t, d, row.ID)
	ctx := t.Context()
	if _, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("created")); err != nil {
		t.Fatal(err)
	}
	if n := storageBytesRows(t, d, row.ID); n != 1 {
		t.Fatalf("%d spaces after the first write, want 1", n)
	}
	// Multi-byte text: the bound is on encoded bytes, not characters.
	longer := strings.Repeat("caf\u00e9 ", 2000)
	scope.Start = false
	if _, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate(longer)); err != nil {
		t.Fatal(err)
	}
	var stored int64
	if err := d.pool.QueryRow(ctx, `SELECT state_bytes FROM extension_storage_spaces WHERE plugin=$1`, row.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if want, _ := json.Marshal(map[string]string{"value": longer}); stored < int64(len(want)) {
		t.Fatalf("state_bytes %d after an update, want at least %d", stored, len(want))
	}
	storageBytesRows(t, d, row.ID)
	if _, err := d.pool.Exec(ctx, `UPDATE extension_storage_spaces SET state_bytes=1 WHERE plugin=$1`, row.ID); err == nil {
		t.Fatal("state_bytes was written directly")
	}

	if err := d.DeleteExtensionRecordRetainingData(ctx, row, true); err != nil {
		t.Fatal(err)
	}
	var retired string
	if err := d.pool.QueryRow(ctx, `SELECT id FROM extension_storage_spaces WHERE retained AND extension_key=$1`, row.ID).Scan(&retired); err != nil {
		t.Fatal(err)
	}
	if _, err := d.pool.Exec(ctx, `INSERT INTO extension_storage_spaces(id,extension_key,retained,retired_at,state) VALUES('fixture-storage-bytes-raw-retained',$1,true,now(),'{"value":"caf\u00e9"}')`, row.ID); err != nil {
		t.Fatal(err)
	}
	if n := storageBytesRows(t, d, row.ID); n != 2 {
		t.Fatalf("%d spaces after retaining, want 2", n)
	}

	_, fresh := storageFixture(t, d, row.ID)
	fresh.Owner = true
	retained, err := d.RetainedExtensionStorage(ctx, fresh)
	if err != nil || len(retained) != 2 {
		t.Fatal(retained, err)
	}
	for _, r := range retained {
		var want int
		if err := d.pool.QueryRow(ctx, `SELECT octet_length(state::text) FROM extension_storage_spaces WHERE id=$1`, r.ID).Scan(&want); err != nil {
			t.Fatal(err)
		}
		if r.Bytes != want {
			t.Fatalf("retained %s lists %d bytes, want %d", r.ID, r.Bytes, want)
		}
	}
	if err := d.RestoreExtensionStorage(ctx, fresh, retired, "fixture-bytes-request", "fixture-bytes-fingerprint", fixtureInitial(), fixtureStorageUpdate("restored")); err != nil {
		t.Fatal(err)
	}
	if n := storageBytesRows(t, d, row.ID); n != 2 {
		t.Fatalf("%d spaces after restoring, want 2", n)
	}

	// The budget sums the same number the old query did, over every space there is.
	var summed, encoded int64
	if err := d.pool.QueryRow(ctx, `SELECT coalesce(sum(state_bytes),0), coalesce(sum(octet_length(state::text)),0) FROM extension_storage_spaces`).Scan(&summed, &encoded); err != nil {
		t.Fatal(err)
	}
	if summed != encoded {
		t.Fatalf("budget sums %d bytes, the documents encode to %d", summed, encoded)
	}
}

// The 12 MiB document bound holds on state_bytes, for writes that do not go through ExtensionStorage too, and the
// constraint that converted each written document a second time is gone.
func TestStorageDocumentBoundChecksStateBytes(t *testing.T) {
	d := open(t)
	row, scope := storageFixture(t, d, "fixture-storage-bound")
	cleanupStorageFixture(t, d, row.ID)
	ctx := t.Context()
	if _, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("small")); err != nil {
		t.Fatal(err)
	}
	rows, err := d.pool.Query(ctx, `SELECT conname FROM pg_constraint WHERE conrelid='extension_storage_spaces'::regclass AND contype='c' ORDER BY conname`)
	if err != nil {
		t.Fatal(err)
	}
	checks, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(checks, "extension_storage_spaces_state_bytes_check") || slices.Contains(checks, "extension_storage_spaces_state_check") {
		t.Fatalf("check constraints %v: want the bound on state_bytes only", checks)
	}

	tx, err := d.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// jsonb's text form of {"value": "<n x>"} is n plus 13 bytes.
	fill := `UPDATE extension_storage_spaces SET state=jsonb_build_object('value', repeat('x', $2::int)) WHERE plugin=$1 RETURNING state_bytes`
	var stored int64
	if err := tx.QueryRow(ctx, fill, row.ID, extensionStorageDocumentMost-13).Scan(&stored); err != nil {
		t.Fatal("a document at the bound was refused:", err)
	}
	if stored != extensionStorageDocumentMost {
		t.Fatalf("state_bytes %d at the bound, want %d", stored, extensionStorageDocumentMost)
	}
	err = tx.QueryRow(ctx, fill, row.ID, extensionStorageDocumentMost-12).Scan(&stored)
	if pe := (*pgconn.PgError)(nil); !errors.As(err, &pe) || pe.Code != "23514" || pe.ConstraintName != "extension_storage_spaces_state_bytes_check" {
		t.Fatal("a document one byte over the bound was not refused by its check:", err)
	}
}

// The budget query sums state_bytes and converts no document to text. The scan may still hand the aggregate whole rows
// (a physical target list), which passes each stored document's pointer along without reading it.
func TestStorageBudgetReadsNoDocument(t *testing.T) {
	d, ctx := open(t), t.Context()
	rows, err := d.pool.Query(ctx, `EXPLAIN (VERBOSE) `+storageBudgetSQL)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(lines, "\n")
	if !strings.Contains(plan, "sum(state_bytes)") || strings.Contains(plan, "octet_length") || strings.Contains(plan, "::text") {
		t.Fatalf("the budget reads more than state_bytes:\n%s", plan)
	}
}

// 0452 fills state_bytes for the spaces a database already has.
func TestStorageBytesMigrationFillsExistingSpaces(t *testing.T) {
	ctx := t.Context()
	url := freshDatabase(t)
	d, err := Attach(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	before := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations, "migrations/manager")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if v, err := migrationVersion(e.Name()); err != nil || v >= 452 {
			continue
		}
		b, err := fs.ReadFile(migrations, "migrations/manager/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		before["migrations/manager/"+e.Name()] = &fstest.MapFile{Data: b}
	}
	if err := d.migrateFrom(ctx, before); err != nil {
		t.Fatal(err)
	}
	for i, state := range []string{`{}`, `{"value":"caf\u00e9"}`, `{"queue":[1,2,3],"value":"` + strings.Repeat("x", 4096) + `"}`} {
		if _, err := d.pool.Exec(ctx, `INSERT INTO extension_storage_spaces(id,extension_key,retained,state) VALUES($1,'fixture-storage-upgrade',true,$2)`, NewFixtureStorageID(i), state); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.migrateFrom(ctx, migrations); err != nil {
		t.Fatal(err)
	}
	if n := storageBytesRows(t, d, "fixture-storage-upgrade"); n != 3 {
		t.Fatalf("%d spaces after the upgrade, want 3", n)
	}
}
