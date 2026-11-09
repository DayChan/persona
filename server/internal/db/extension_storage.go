package db

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/extension"
)

const (
	extensionStorageDocumentMost  = 12 << 20
	extensionStorageAggregateMost = 128 << 20
	extensionStorageSpacesMost    = 512
)

// One fixed transaction lock protects aggregate quotas across manager processes.
// It is acquired after the installation row by every storage mutation/lifecycle.
func storageBudget(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(1187, 1)`)
	return err
}

func checkStorageBudget(ctx context.Context, tx pgx.Tx) error {
	var bytes, count int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(octet_length(state::text)),0),count(*) FROM extension_storage_spaces`).Scan(&bytes, &count); err != nil {
		return err
	}
	if bytes > extensionStorageAggregateMost || count > extensionStorageSpacesMost {
		return extension.ErrStorageQuota
	}
	return nil
}

// StorageUpdate is a detached business-data transformation supplied only by the
// manager. The DB layer does not depend on manager implementations or accept SQL
// from an extension. The plugin row lock fences disable/uninstall and changes in
// grants, public identity, installation epoch or configuration revision.
type StorageUpdate func(data json.RawMessage, previousRun string) (next json.RawMessage, result any, changed bool, err error)

func storageRollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func storageOwner(ctx context.Context, tx pgx.Tx, scope extension.StorageScope) error {
	var publicID string
	var installed, updated time.Time
	var enabled bool
	var granted json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(public_id,''),id), installed_at, updated_at, enabled, granted FROM plugins WHERE id=$1 FOR UPDATE`, scope.Plugin).Scan(&publicID, &installed, &updated, &enabled, &granted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return extension.ErrStorageScope
		}
		return err
	}
	// pgx/Postgres persist timestamptz at microsecond precision. Registry records
	// can still carry time.Now's nanoseconds before a reload; compare the actual
	// persisted precision, without dropping a representable epoch/revision change.
	if publicID != scope.PublicID || installed.UnixMicro() != scope.InstalledAt.UnixMicro() || updated.UnixMicro() != scope.UpdatedAt.UnixMicro() || !enabled && !scope.Owner {
		return extension.ErrStorageScope
	}
	var permission extension.Permissions
	if json.Unmarshal(granted, &permission) != nil || permission.Storage == nil || scope.Granted == nil {
		return extension.ErrStorageDenied
	}
	want, _ := json.Marshal(scope.Granted)
	have, _ := json.Marshal(permission.Storage)
	if string(want) != string(have) {
		return extension.ErrStorageScope
	}
	return nil
}

// ExtensionStorage commits a logical mutation and its replay receipt together.
// New installations create empty private spaces; retained same-ID spaces are not
// discovered or rebound by this method. It never falls back to in-memory state.
func (d *DB) ExtensionStorage(ctx context.Context, scope extension.StorageScope, initial json.RawMessage, update StorageUpdate) (any, error) {
	if scope.Plugin == "" || scope.PublicID == "" || scope.Run == "" || scope.InstalledAt.IsZero() || scope.Granted == nil || !json.Valid(initial) {
		return nil, extension.ErrStorageDenied
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer storageRollback(tx)
	if err := storageOwner(ctx, tx, scope); err != nil {
		return nil, err
	}
	if err := storageBudget(ctx, tx); err != nil {
		return nil, err
	}
	var id, previousRun string
	var data json.RawMessage
	err = tx.QueryRow(ctx, `SELECT id, runtime, state FROM extension_storage_spaces WHERE plugin=$1 AND installed_at=$2 AND NOT retained FOR UPDATE`, scope.Plugin, scope.InstalledAt).Scan(&id, &previousRun, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		id, data = rand.Text(), initial
		_, err = tx.Exec(ctx, `INSERT INTO extension_storage_spaces(id,plugin,extension_key,installed_at,state) VALUES($1,$2,$3,$4,$5)`, id, scope.Plugin, scope.PublicID, scope.InstalledAt, []byte(data))
	}
	if err != nil {
		return nil, err
	}
	if !scope.Start && previousRun != scope.Run {
		return nil, extension.ErrStorageScope
	}
	next, result, changed, err := update(data, previousRun)
	if err != nil {
		return nil, err
	}
	if changed || previousRun != scope.Run {
		if len(next) > extensionStorageDocumentMost || !json.Valid(next) {
			return nil, extension.ErrStorageQuota
		}
		if _, err := tx.Exec(ctx, `UPDATE extension_storage_spaces SET state=$1,runtime=$2,revision=revision+1 WHERE id=$3`, []byte(next), scope.Run, id); err != nil {
			return nil, err
		}
	}
	if err := checkStorageBudget(ctx, tx); err != nil {
		return nil, err
	}
	if err := extensionCommitResult(tx.Commit(ctx)); err != nil {
		return nil, err
	}
	return result, nil
}

type ExtensionStorageRetained struct {
	ID        string    `json:"id"`
	RetiredAt time.Time `json:"retired_at"`
	Bytes     int       `json:"bytes"`
}

// RetainedExtensionStorage is owner-only metadata. A retained opaque ID is not a
// source capability; data remains inaccessible until an explicit owner restore.
func (d *DB) RetainedExtensionStorage(ctx context.Context, scope extension.StorageScope) ([]ExtensionStorageRetained, error) {
	if !scope.Owner || scope.PublicID == "" {
		return nil, extension.ErrStorageDenied
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer storageRollback(tx)
	if err := storageOwner(ctx, tx, scope); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id,retired_at,octet_length(state::text) FROM extension_storage_spaces WHERE retained AND extension_key=$1 ORDER BY retired_at DESC,id LIMIT 32`, scope.PublicID)
	if err != nil {
		return nil, err
	}
	out := []ExtensionStorageRetained{}
	for rows.Next() {
		var row ExtensionStorageRetained
		if err := rows.Scan(&row.ID, &row.RetiredAt, &row.Bytes); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = extensionCommitResult(tx.Commit(ctx)); err != nil {
		return nil, err
	}
	return out, nil
}

// RestoreExtensionStorage rebinding is explicit, authenticated and transactional.
// An occupied target is refused instead of overwriting newly created business
// data. The original source ID plus request fingerprint makes a lost reply
// replayable, while a changed request or another installation is refused.
func (d *DB) RestoreExtensionStorage(ctx context.Context, scope extension.StorageScope, source, requestID, fingerprint string, initial json.RawMessage, update StorageUpdate) error {
	if !scope.Owner || source == "" || len(source) > 80 || requestID == "" || len(requestID) > 80 || fingerprint == "" || len(fingerprint) > 128 {
		return extension.ErrStorageDenied
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer storageRollback(tx)
	if err = storageOwner(ctx, tx, scope); err != nil {
		return err
	}
	if err = storageBudget(ctx, tx); err != nil {
		return err
	}
	var plugin, run, previousRequest, previousFingerprint string
	var installed *time.Time
	var retained bool
	var data json.RawMessage
	err = tx.QueryRow(ctx, `SELECT coalesce(plugin,''),installed_at,retained,runtime,state,restore_request,restore_fingerprint FROM extension_storage_spaces WHERE id=$1 AND extension_key=$2 FOR UPDATE`, source, scope.PublicID).Scan(&plugin, &installed, &retained, &run, &data, &previousRequest, &previousFingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return extension.ErrStorageScope
	}
	if err != nil {
		return err
	}
	if !retained {
		if plugin == scope.Plugin && installed != nil && installed.UnixMicro() == scope.InstalledAt.UnixMicro() && previousRequest == requestID && previousFingerprint == fingerprint {
			return extensionCommitResult(tx.Commit(ctx))
		}
		return extension.ErrStorageScope
	}
	var occupied, pristine bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_storage_spaces WHERE plugin=$1), NOT EXISTS(SELECT 1 FROM extension_storage_spaces WHERE plugin=$1 AND state <> $2::jsonb)`, scope.Plugin, []byte(initial)).Scan(&occupied, &pristine); err != nil {
		return err
	}
	if occupied && !pristine {
		return extension.ErrStorageScope
	}
	if occupied {
		if _, err = tx.Exec(ctx, `DELETE FROM extension_storage_spaces WHERE plugin=$1 AND state=$2::jsonb`, scope.Plugin, []byte(initial)); err != nil {
			return err
		}
	}
	next, _, _, err := update(data, run)
	if err != nil {
		return err
	}
	if len(next) > extensionStorageDocumentMost || !json.Valid(next) {
		return extension.ErrStorageQuota
	}
	_, err = tx.Exec(ctx, `UPDATE extension_storage_spaces SET plugin=$1,installed_at=$2,retained=false,retired_at=NULL,runtime=$3,state=$4,restore_request=$5,restore_fingerprint=$6,revision=revision+1 WHERE id=$7`, scope.Plugin, scope.InstalledAt, "", []byte(next), requestID, fingerprint, source)
	if err != nil {
		return err
	}
	if err = checkStorageBudget(ctx, tx); err != nil {
		return err
	}
	return extensionCommitResult(tx.Commit(ctx))
}

// DeleteExtensionRecordRetainingData is the registry's atomic uninstall hook.
// Business data may be detached before deleting the plugin/credential rows, but
// a fresh installation cannot get it without authenticated explicit restore.
func (d *DB) DeleteExtensionRecordRetainingData(ctx context.Context, row extension.Record, keepData bool) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer storageRollback(tx)
	var publicID, version string
	var installed time.Time
	if err = tx.QueryRow(ctx, `SELECT COALESCE(NULLIF(public_id,''),id),version,installed_at FROM plugins WHERE id=$1 FOR UPDATE`, row.StorageKey()).Scan(&publicID, &version, &installed); err != nil {
		return err
	}
	if publicID != row.ID || version != row.Version || installed.UnixMicro() != row.InstalledAt.UnixMicro() {
		return extension.ErrIdentity
	}
	if err = storageBudget(ctx, tx); err != nil {
		return err
	}
	if keepData {
		// At most eight retained namespaces per logical extension identity. A full
		// retention set fails uninstall before any data/credential deletion.
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM extension_storage_spaces WHERE retained AND extension_key=$1`, row.ID).Scan(&count); err != nil {
			return err
		}
		var hasData bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM extension_storage_spaces WHERE plugin=$1)`, row.StorageKey()).Scan(&hasData); err != nil {
			return err
		}
		if hasData && count >= 8 {
			return extension.ErrStorageQuota
		}
		if _, err = tx.Exec(ctx, `UPDATE extension_storage_spaces SET plugin=NULL,installed_at=NULL,retained=true,retired_at=now(),runtime='',extension_key=$3 WHERE plugin=$1 AND installed_at=$2`, row.StorageKey(), row.InstalledAt, row.ID); err != nil {
			return err
		}
	} else {
		if _, err = tx.Exec(ctx, `DELETE FROM extension_storage_spaces WHERE plugin=$1 OR retained AND extension_key=$2`, row.StorageKey(), row.ID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM plugins WHERE id=$1`, row.StorageKey()); err != nil {
		return err
	}
	return extensionCommitResult(tx.Commit(ctx))
}
