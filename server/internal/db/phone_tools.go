package db

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- phone tools ----

// PhoneToolRegistration is one device's most recently announced tool list.
type PhoneToolRegistration struct {
	DeviceID     string
	Platform     string
	CredentialID string
	UpdatedAt    time.Time
	Tools        []proto.Tool
}

// ReplacePhoneTools replaces only this device's tools, including an empty list.
func (d *DB) ReplacePhoneTools(ctx context.Context, registration PhoneToolRegistration) error {
	return pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if registration.CredentialID != "" {
			var id string
			if err := tx.QueryRow(ctx, `SELECT id FROM device_tokens WHERE id = $1 AND revoked_at IS NULL FOR UPDATE`, registration.CredentialID).Scan(&id); err != nil {
				return err
			}
		}
		// The DELETE and every INSERT go out as one batch: one round trip instead of 1+N while the hub holds
		// phoneToolsMu on each announcement (about 26 tools per app).
		var b pgx.Batch
		b.Queue(`DELETE FROM phone_tools WHERE device_id = $1`, registration.DeviceID)
		for i, t := range registration.Tools {
			schema := t.InputSchema
			if len(schema) == 0 {
				schema = json.RawMessage(`{}`)
			}
			b.Queue(`INSERT INTO phone_tools (device_id, platform, credential_id, name, description, input_schema, position)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				ON CONFLICT (device_id, name) DO UPDATE SET description = $5, input_schema = $6, position = $7, updated_at = now()`,
				registration.DeviceID, registration.Platform, registration.CredentialID, t.Name, t.Description, []byte(schema), i)
		}
		return tx.SendBatch(ctx, &b).Close()
	})
}

func (d *DB) PhoneTools(ctx context.Context) ([]PhoneToolRegistration, error) {
	rows, err := d.pool.Query(ctx, `SELECT device_id, platform, credential_id, updated_at, name, description, input_schema
		FROM phone_tools ORDER BY device_id, position, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var registrations []PhoneToolRegistration
	for rows.Next() {
		var r PhoneToolRegistration
		var t proto.Tool
		if err := rows.Scan(&r.DeviceID, &r.Platform, &r.CredentialID, &r.UpdatedAt, &t.Name, &t.Description, &t.InputSchema); err != nil {
			return nil, err
		}
		if len(registrations) == 0 || registrations[len(registrations)-1].DeviceID != r.DeviceID {
			registrations = append(registrations, r)
		}
		last := &registrations[len(registrations)-1]
		last.Tools = append(last.Tools, t)
		if r.UpdatedAt.After(last.UpdatedAt) {
			last.UpdatedAt = r.UpdatedAt
		}
	}
	return registrations, rows.Err()
}
