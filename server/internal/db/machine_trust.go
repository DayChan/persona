package db

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// Machine trust (migrations/0413_machine_trust.sql): no row is a confirmed computer.

const (
	TrustPending   = "pending"
	TrustConfirmed = "confirmed"
)

// PendingMachine is a computer waiting for the user to say it is theirs.
type PendingMachine struct {
	ID        string
	AddedBy   string
	CreatedAt time.Time
}

// PendingMachines are the computers not confirmed yet, oldest first.
func (d *DB) PendingMachines(ctx context.Context) ([]PendingMachine, error) {
	rows, err := d.pool.Query(ctx, `SELECT machine_id, added_by, created_at FROM machine_trust
		WHERE state = $1 ORDER BY created_at, machine_id`, TrustPending)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (PendingMachine, error) {
		var p PendingMachine
		err := r.Scan(&p.ID, &p.AddedBy, &p.CreatedAt)
		return p, err
	})
}

// SetMachinePending marks a computer pending (added by a device: actor), starting its day again.
func (d *DB) SetMachinePending(ctx context.Context, id, actor string) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO machine_trust (machine_id, state, added_by) VALUES ($1, $2, $3)
		ON CONFLICT (machine_id) DO UPDATE SET state = $2, added_by = $3, created_at = now(), confirmed_at = NULL`,
		id, TrustPending, actor)
	return err
}

// ConfirmMachine makes a pending computer confirmed; false when it was not pending.
func (d *DB) ConfirmMachine(ctx context.Context, id string) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE machine_trust SET state = $2, confirmed_at = now() WHERE machine_id = $1 AND state = $3`,
		id, TrustConfirmed, TrustPending)
	return tag.RowsAffected() > 0, err
}

// ForgetMachineTrust drops a computer's row (it was rejected or removed).
func (d *DB) ForgetMachineTrust(ctx context.Context, id string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM machine_trust WHERE machine_id = $1`, id)
	return err
}

// RevokeMachineCredentials revokes every live Persona Link credential bound to the machine and expires its unused
// pairing codes; the ids revoked. Role link only: a peer hub's credential binds that hub's id in the same column.
func (d *DB) RevokeMachineCredentials(ctx context.Context, machine string) ([]string, error) {
	var ids []string
	err := pgx.BeginFunc(ctx, d.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE pair_tokens SET expires_at = `+endedNow+`
			WHERE machine_id = $1 AND used_at IS NULL AND expires_at > LEAST(now(), $2)`, machine, time.Now()); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `UPDATE device_tokens SET revoked_at = now() WHERE machine_id = $1 AND role = 'link' AND revoked_at IS NULL RETURNING id`, machine)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := dropDeviceRows(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

// MachineAudit is one row of a computer's audit.
type MachineAudit struct {
	MachineID string          `json:"machine_id"`
	Action    string          `json:"action"`
	Actor     string          `json:"actor"`
	Detail    json.RawMessage `json:"detail"`
	At        time.Time       `json:"at"`
}

// AddMachineAudit records what happened to a computer's trust. Never a credential.
func (d *DB) AddMachineAudit(ctx context.Context, machine, action, actor string, detail json.RawMessage) error {
	if len(detail) == 0 {
		detail = json.RawMessage(`{}`)
	}
	_, err := d.pool.Exec(ctx, `INSERT INTO machine_audit (machine_id, action, actor, detail) VALUES ($1, $2, $3, $4)`,
		machine, action, actor, []byte(detail))
	return err
}

// MachineAudits is a computer's audit, oldest first.
func (d *DB) MachineAudits(ctx context.Context, machine string) ([]MachineAudit, error) {
	rows, err := d.pool.Query(ctx, `SELECT machine_id, action, actor, detail, at FROM machine_audit WHERE machine_id = $1 ORDER BY at, id`, machine)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (MachineAudit, error) {
		var a MachineAudit
		var detail []byte
		err := r.Scan(&a.MachineID, &a.Action, &a.Actor, &detail, &a.At)
		a.Detail = detail
		return a, err
	})
}
