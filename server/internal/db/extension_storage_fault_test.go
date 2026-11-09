package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

func TestStorageDurabilityAfterPoolRestartAndFailedCommit(t *testing.T) {
	d := open(t)
	row, scope := storageFixture(t, d, "fixture-restart-storage")
	cleanupStorageFixture(t, d, row.ID)
	ctx := t.Context()
	if _, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("durable")); err != nil {
		t.Fatal(err)
	}
	other, err := Open(ctx, os.Getenv("PERSONA_TEST_DATABASE_URL"), AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	scope.Start = false
	if value, err := other.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("read")); err != nil || value != "durable" {
		t.Fatal("fresh pool lost committed data", value, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	_, err = other.ExtensionStorage(cancelled, scope, fixtureInitial(), func(raw json.RawMessage, _ string) (json.RawMessage, any, bool, error) {
		cancel()
		return json.RawMessage(`{"value":"must rollback"}`), nil, true, nil
	})
	if err == nil {
		t.Fatal("canceled transaction reported success")
	}
	if value, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("read")); err != nil || value != "durable" {
		t.Fatal("canceled precommit update changed durable data", value, err)
	}
	// Invalid transformed state is refused before SQL UPDATE; this is not an
	// injected unknown commit or a database constraint-failure claim.
	if _, err := other.ExtensionStorage(ctx, scope, fixtureInitial(), func(raw json.RawMessage, _ string) (json.RawMessage, any, bool, error) {
		return json.RawMessage(`invalid json`), nil, true, nil
	}); !errors.Is(err, extension.ErrStorageQuota) {
		t.Fatal("invalid commit input admitted", err)
	}
}

func TestRetentionQuotaFailureKeepsDataAndCredentials(t *testing.T) {
	d := open(t)
	row, scope := storageFixture(t, d, "fixture-retention-full")
	cleanupStorageFixture(t, d, row.ID)
	ctx := t.Context()
	if _, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("live")); err != nil {
		t.Fatal(err)
	}
	if err := d.PutExtensionSecret(ctx, row.ID, "fixture", "fixture-credential"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		_, err := d.pool.Exec(ctx, `INSERT INTO extension_storage_spaces(id,extension_key,retained,state) VALUES($1,$2,true,'{"value":"retained"}')`, "fixture-retention-entry-"+string(rune('a'+i)), row.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := d.DeleteExtensionRecordRetainingData(ctx, row, true); !errors.Is(err, extension.ErrStorageQuota) {
		t.Fatal("full retention set did not refuse before delete", err)
	}
	if secret, err := d.ExtensionSecrets(ctx, row.ID); err != nil || secret["fixture"] != "fixture-credential" {
		t.Fatal("failed retention uninstall deleted credentials", err)
	}
	if value, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("read")); err != nil || value != "live" {
		t.Fatal("failed uninstall deleted live state", value, err)
	}
	if err := d.DeleteExtensionRecordRetainingData(ctx, row, false); err != nil {
		t.Fatal("explicit full deletion could not free quota", err)
	}
}

func TestStorageGlobalQuotaSerializesAndRollsBack(t *testing.T) {
	d := open(t)
	row, scope := storageFixture(t, d, "fixture-global-quota")
	cleanupStorageFixture(t, d, row.ID, "fixture-global-retained")
	ctx := t.Context()
	// Fill the namespace count, using small synthetic rows rather than allocating
	// a giant production-sized payload. The last active-space insert rolls back.
	for i := 0; i < extensionStorageSpacesMost; i++ {
		_, err := d.pool.Exec(ctx, `INSERT INTO extension_storage_spaces(id,extension_key,retained,state) VALUES($1,'fixture-global-retained',true,'{}')`, NewFixtureStorageID(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.ExtensionStorage(ctx, scope, fixtureInitial(), fixtureStorageUpdate("write")); !errors.Is(err, extension.ErrStorageQuota) {
		t.Fatal("aggregate namespace quota ignored", err)
	}
	var count int
	if err := d.pool.QueryRow(ctx, `SELECT count(*) FROM extension_storage_spaces WHERE plugin=$1`, row.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("quota failure partially inserted active state", count, err)
	}
}

func NewFixtureStorageID(index int) string {
	return "fixture-global-space-" + time.Unix(int64(index), 0).UTC().Format("150405")
}
