package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

func storageFixture(t *testing.T, d *DB, id string) (extension.Record, extension.StorageScope) {
	t.Helper()
	at := time.Now().UTC()
	grant := &extension.StoragePermission{Patterns: []string{"state/*", "queue/*", "media/*"}, Queues: true, Blobs: true}
	r := extension.Record{ID: id, Source: extension.SourceInstalled, Version: "1.0.0", Manifest: json.RawMessage(`{"id":"` + id + `"}`), Granted: extension.Permissions{Storage: grant}, Enabled: true, InstalledAt: at, UpdatedAt: at}
	if err := d.PutExtension(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	return r, extension.StorageScope{Plugin: id, PublicID: id, InstalledAt: at, UpdatedAt: at, Run: "fixture-run", Granted: grant, Start: true}
}
func fixtureInitial() json.RawMessage { return json.RawMessage(`{"value":""}`) }
func fixtureStorageUpdate(value string) StorageUpdate {
	return func(data json.RawMessage, previous string) (json.RawMessage, any, bool, error) {
		var old map[string]string
		if json.Unmarshal(data, &old) != nil {
			return nil, nil, false, errors.New("invalid fixture state")
		}
		if value == "read" {
			return data, old["value"], false, nil
		}
		out, _ := json.Marshal(map[string]string{"value": value})
		return out, value, true, nil
	}
}
func cleanupStorageFixture(t *testing.T, d *DB, ids ...string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, id := range ids {
			_, _ = d.pool.Exec(ctx, `DELETE FROM extension_storage_spaces WHERE extension_key=$1`, id)
			_, _ = d.pool.Exec(ctx, `DELETE FROM plugins WHERE id=$1`, id)
		}
	})
}

func TestExtensionStorageRealCommitRollbackAndRunFence(t *testing.T) {
	d := open(t)
	row, scope := storageFixture(t, d, "fixture-store-db")
	cleanupStorageFixture(t, d, row.ID)
	ctx := t.Context()
	if value, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("committed")); err != nil || value != "committed" {
		t.Fatal(value, err)
	}
	scope.Start = false
	failed := errors.New("fixture transaction refused")
	if _, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), func(data json.RawMessage, _ string) (json.RawMessage, any, bool, error) {
		return json.RawMessage(`{"value":"lost"}`), nil, true, failed
	}); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if value, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("read")); err != nil || value != "committed" {
		t.Fatal("failed mutation partially committed", value, err)
	}
	old := scope
	scope.Run = "replacement-run"
	scope.Start = true
	if _, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("read")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExtensionStorage(ctx, old, fixtureInitial(), fixtureStorageUpdate("stale-write")); !errors.Is(err, extension.ErrStorageScope) {
		t.Fatal("retired run rebound its own capability", err)
	}
	for name, change := range map[string]func(*extension.StorageScope){
		"epoch":     func(s *extension.StorageScope) { s.InstalledAt = s.InstalledAt.Add(time.Second) },
		"public ID": func(s *extension.StorageScope) { s.PublicID = "other" },
		"revision":  func(s *extension.StorageScope) { s.UpdatedAt = s.UpdatedAt.Add(time.Second) },
		"grant":     func(s *extension.StorageScope) { s.Granted = &extension.StoragePermission{Keys: []string{"private"}} },
	} {
		t.Run(name, func(t *testing.T) {
			wrong := scope
			change(&wrong)
			if _, err := d.ExtensionStorage(ctx, wrong, fixtureInitial(), fixtureStorageUpdate("forged")); err == nil {
				t.Fatal("wrong scope admitted")
			}
		})
	}
	if _, err := d.pool.Exec(ctx, `UPDATE plugins SET enabled=false WHERE id=$1`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("disabled")); !errors.Is(err, extension.ErrStorageScope) {
		t.Fatal("disabled installation wrote state", err)
	}
}

func TestStorageKeepDataRequiresAuthenticatedExplicitRestore(t *testing.T) {
	d := open(t)
	row, scope := storageFixture(t, d, "fixture-retained-store")
	cleanupStorageFixture(t, d, row.ID)
	ctx := t.Context()
	if _, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("retained-business")); err != nil {
		t.Fatal(err)
	}
	if err := d.PutExtensionSecret(ctx, row.ID, "fixture-key", "fixture-ciphertext"); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteExtensionRecordRetainingData(ctx, row, true); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM plugin_secrets WHERE plugin=$1`, row.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("credential retained with business state", err)
	}
	var retainedID string
	if err := d.pool.QueryRow(ctx, `SELECT id FROM extension_storage_spaces WHERE retained AND extension_key=$1`, row.ID).Scan(&retainedID); err != nil {
		t.Fatal(err)
	}
	newRow, newScope := storageFixture(t, d, row.ID)
	if newRow.InstalledAt.Equal(row.InstalledAt) {
		t.Fatal("fixture failed to create a new epoch")
	}
	if value, err := d.ExtensionStorage(ctx, newScope, fixtureInitial(), fixtureStorageUpdate("read")); err != nil || value != "" {
		t.Fatal("same-ID reinstall automatically obtained old data", value, err)
	}
	requestID, fingerprint := "fixture-restore-request", "fixture-exact-fingerprint"
	if err := d.RestoreExtensionStorage(ctx, newScope, retainedID, requestID, fingerprint, fixtureInitial(), fixtureStorageUpdate("read")); !errors.Is(err, extension.ErrStorageDenied) {
		t.Fatal("unauthenticated restore admitted", err)
	}
	newScope.Owner = true
	rows, err := d.RetainedExtensionStorage(ctx, newScope)
	if err != nil || len(rows) != 1 || rows[0].ID != retainedID {
		t.Fatal(rows, err)
	}
	if err := d.RestoreExtensionStorage(ctx, newScope, retainedID, requestID, fingerprint, fixtureInitial(), fixtureStorageUpdate("read")); err != nil {
		t.Fatal(err)
	}
	if err := d.RestoreExtensionStorage(ctx, newScope, retainedID, requestID, fingerprint, fixtureInitial(), fixtureStorageUpdate("read")); err != nil {
		t.Fatal("lost restore response not replayable", err)
	}
	if err := d.RestoreExtensionStorage(ctx, newScope, retainedID, "changed-request", fingerprint, fixtureInitial(), fixtureStorageUpdate("read")); !errors.Is(err, extension.ErrStorageScope) {
		t.Fatal("changed restore replay admitted", err)
	}
	newScope.Owner = false
	newScope.Start = true
	if value, err := d.ExtensionStorage(ctx, newScope, fixtureInitial(), fixtureStorageUpdate("read")); err != nil || value != "retained-business" {
		t.Fatal("explicit restore lost business data", value, err)
	}
	if _, err := d.pool.Exec(ctx, `INSERT INTO extension_storage_spaces(id,extension_key,retained,state) VALUES('fixture-extra-retained-opaque', $1,true,'{"value":"other retained"}')`, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteExtensionRecordRetainingData(ctx, newRow, false); err != nil {
		t.Fatal(err)
	}
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM extension_storage_spaces WHERE extension_key=$1`, row.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("keep_data=0 left business records", count, err)
	}
}

func TestStorageRestoreCannotOverwriteNewDataOrCrossInstallations(t *testing.T) {
	d := open(t)
	row, scope := storageFixture(t, d, "fixture-restore-original")
	cleanupStorageFixture(t, d, row.ID, "fixture-restore-other")
	ctx := t.Context()
	if _, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("old")); err != nil {
		t.Fatal(err)
	}
	if err := d.DeleteExtensionRecordRetainingData(ctx, row, true); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := d.pool.QueryRow(ctx, `SELECT id FROM extension_storage_spaces WHERE retained AND extension_key=$1`, row.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	_, other := storageFixture(t, d, "fixture-restore-other")
	other.Owner = true
	if err := d.RestoreExtensionStorage(ctx, other, id, "fixture-request", "fixture-fingerprint", fixtureInitial(), fixtureStorageUpdate("read")); err == nil {
		t.Fatal("other installation stole retained data")
	}
	_, fresh := storageFixture(t, d, row.ID)
	fresh.Owner = true
	if _, err := d.ExtensionStorage(ctx, fresh, fixtureInitial(), fixtureStorageUpdate("new-business")); err != nil {
		t.Fatal(err)
	}
	if err := d.RestoreExtensionStorage(ctx, fresh, id, "fixture-request", "fixture-fingerprint", fixtureInitial(), fixtureStorageUpdate("read")); err == nil {
		t.Fatal("restore overwrote newly created business state")
	}
}

func TestStorageConcurrentTransformsSerializeAcrossPools(t *testing.T) {
	d := open(t)
	row, scope := storageFixture(t, d, "fixture-storage-concurrent")
	cleanupStorageFixture(t, d, row.ID)
	if _, err := d.ExtensionStorage(t.Context(), scope, fixtureInitial(), fixtureStorageUpdate("0")); err != nil {
		t.Fatal(err)
	}
	other, err := Open(t.Context(), os.Getenv("PERSONA_TEST_DATABASE_URL"), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	scope.Start = false
	result := make(chan error, 12)
	for i := 0; i < 12; i++ {
		pool := d
		if i%2 == 1 {
			pool = other
		}
		go func() {
			_, err := pool.ExtensionStorage(t.Context(), scope, fixtureInitial(), func(data json.RawMessage, _ string) (json.RawMessage, any, bool, error) {
				var value map[string]string
				if err := json.Unmarshal(data, &value); err != nil {
					return nil, nil, false, err
				}
				var n int
				if _, err := fmt.Sscan(value["value"], &n); err != nil {
					return nil, nil, false, err
				}
				next, _ := json.Marshal(map[string]string{"value": fmt.Sprint(n + 1)})
				return next, n + 1, true, nil
			})
			result <- err
		}()
	}
	for i := 0; i < 12; i++ {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	if value, err := d.ExtensionStorage(t.Context(), scope, fixtureInitial(), fixtureStorageUpdate("read")); err != nil || value != "12" {
		t.Fatal("concurrent durable updates were lost", value, err)
	}
}

func TestStorageTimestampPrecisionMatchesPostgresWithoutLosingFences(t *testing.T) {
	d := open(t)
	row, scope := storageFixture(t, d, "fixture-storage-nanoseconds")
	cleanupStorageFixture(t, d, row.ID)
	row.InstalledAt = row.InstalledAt.Truncate(time.Microsecond).Add(731 * time.Nanosecond)
	row.UpdatedAt = row.InstalledAt
	scope.InstalledAt, scope.UpdatedAt = row.InstalledAt, row.UpdatedAt
	if err := d.PutExtension(t.Context(), row); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ExtensionStorage(t.Context(), scope, fixtureInitial(), fixtureStorageUpdate("committed")); err != nil {
		t.Fatal("fresh nanosecond registry record could not bind its real Postgres row", err)
	}
	scope.Start = false
	for _, field := range []string{"installation", "revision"} {
		t.Run(field, func(t *testing.T) {
			stale := scope
			if field == "installation" {
				stale.InstalledAt = stale.InstalledAt.Add(time.Microsecond)
			} else {
				stale.UpdatedAt = stale.UpdatedAt.Add(time.Microsecond)
			}
			if _, err := d.ExtensionStorage(t.Context(), stale, fixtureInitial(), fixtureStorageUpdate("must not commit")); !errors.Is(err, extension.ErrStorageScope) {
				t.Fatal("a representable changed timestamp crossed the lifecycle fence", err)
			}
		})
	}
	if value, err := d.ExtensionStorage(t.Context(), scope, fixtureInitial(), fixtureStorageUpdate("read")); err != nil || value != "committed" {
		t.Fatal("refused stale operation changed committed state", value, err)
	}
	if err := d.DeleteExtensionRecordRetainingData(t.Context(), row, true); err != nil {
		t.Fatal("nanosecond uninstall record could not match persisted precision", err)
	}
}
