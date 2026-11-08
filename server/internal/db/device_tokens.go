package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- device tokens ----

// DeviceToken is one device's credential; the token itself is never stored, only its hash.
type DeviceToken struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"`
	Role      string     `json:"role"`
	CreatedAt time.Time  `json:"created_at"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	// MachineID binds a persona-link credential (kind link) to its machine in hub.json; empty for every other kind.
	MachineID string `json:"machine_id,omitempty"`
}

func (d *DB) CreateDeviceToken(ctx context.Context, t DeviceToken, hash string) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO device_tokens (id, name, kind, role, token_hash, machine_id)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))`, t.ID, t.Name, t.Kind, t.Role, hash, t.MachineID)
	return err
}

// DeviceTokenByHash finds a token that is not revoked; nil when there is none.
func (d *DB) DeviceTokenByHash(ctx context.Context, hash string) (*DeviceToken, error) {
	var t DeviceToken
	err := d.pool.QueryRow(ctx, `SELECT id, name, kind, role, created_at, last_seen, COALESCE(machine_id, '') FROM device_tokens
		WHERE token_hash = $1 AND revoked_at IS NULL`, hash).Scan(&t.ID, &t.Name, &t.Kind, &t.Role, &t.CreatedAt, &t.LastSeen, &t.MachineID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (d *DB) DeviceTokens(ctx context.Context) ([]DeviceToken, error) {
	rows, err := d.pool.Query(ctx, `SELECT id, name, kind, role, created_at, last_seen, revoked_at, COALESCE(machine_id, '')
		FROM device_tokens WHERE kind <> $1 ORDER BY revoked_at IS NOT NULL, created_at DESC`, tombstoneKind)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (DeviceToken, error) {
		var t DeviceToken
		err := r.Scan(&t.ID, &t.Name, &t.Kind, &t.Role, &t.CreatedAt, &t.LastSeen, &t.RevokedAt, &t.MachineID)
		return t, err
	})
}

// RevokeDeviceToken reports whether a live token was revoked.
func (d *DB) RevokeDeviceToken(ctx context.Context, id string) (bool, error) {
	var revoked bool
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE device_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
		if err != nil {
			return err
		}
		revoked = tag.RowsAffected() > 0
		if revoked {
			err = dropDeviceRows(ctx, tx, id)
		}
		return err
	})
	return revoked, err
}

// dropDeviceRows invalidates its pairing credentials and removes what a revoked credential registered.
func dropDeviceRows(ctx context.Context, tx pgx.Tx, id string) error {
	if _, err := tx.Exec(ctx, `UPDATE pair_tokens SET expires_at = `+endedNow+` WHERE device_id = $1`, id, time.Now()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM devices WHERE credential_id = $1 OR device_id = $1`, id); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM phone_tools WHERE credential_id = $1 OR device_id = $1`, id)
	return err
}

func (d *DB) TouchDeviceToken(ctx context.Context, id string) error {
	_, err := d.pool.Exec(ctx, `UPDATE device_tokens SET last_seen = now() WHERE id = $1`, id)
	return err
}
