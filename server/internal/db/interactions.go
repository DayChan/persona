package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- interactions ----

func (d *DB) InsertInteraction(ctx context.Context, ix proto.Interaction) error {
	payload, err := json.Marshal(ix)
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO interactions (id, machine_id, session_id, kind, payload, created_at)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING`,
		ix.ID, ix.MachineID, ix.SessionID, ix.Kind, payload, ix.CreatedAt)
	return err
}

// FinishInteraction records how an interaction ended; only the first outcome counts.
func (d *DB) FinishInteraction(ctx context.Context, id, status string, res *proto.Resolution) error {
	var raw []byte
	if res != nil {
		raw, _ = json.Marshal(res)
	}
	_, err := d.pool.Exec(ctx, `UPDATE interactions SET status = $2, resolution = $3, resolved_at = now()
		WHERE id = $1 AND status = 'pending'`, id, status, raw)
	return err
}

// InteractionRecord is a past interaction with its outcome.
type InteractionRecord struct {
	Interaction proto.Interaction `json:"interaction"`
	Status      string            `json:"status"`
	Resolution  *proto.Resolution `json:"resolution,omitempty"`
	ResolvedAt  *time.Time        `json:"resolved_at,omitempty"`
}

// The statements of Interactions, one per combination of filters. The single statement they replace matched an
// empty filter with an OR ("$1 = empty OR machine_id = $1"). Once Postgres switched that cached statement to a generic
// plan (after five runs) it could not fold the empty test away, so it walked interactions_created over every row ever
// kept. TestInteractionsHistoryIndexed checks the plans of the cached ones.
const (
	interactionsSQL        = `SELECT payload, status, resolution, resolved_at FROM interactions ORDER BY created_at DESC LIMIT $1`
	interactionsMachineSQL = `SELECT payload, status, resolution, resolved_at FROM interactions
		WHERE machine_id = $1 ORDER BY created_at DESC LIMIT $2`
	interactionsSessionSQL = `SELECT payload, status, resolution, resolved_at FROM interactions
		WHERE session_id = $1 ORDER BY created_at DESC LIMIT $2`
	interactionsMachineSessionSQL = `SELECT payload, status, resolution, resolved_at FROM interactions
		WHERE machine_id = $1 AND session_id = $2 ORDER BY created_at DESC LIMIT $3`
)

// Interactions lists recent interactions, newest first, optionally for one machine and/or one session.
func (d *DB) Interactions(ctx context.Context, machine, session string, limit int) ([]InteractionRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// A machine alone or a session alone has no index that orders by created_at: the best plan walks
	// interactions_created for a busy one and reads interactions_session then sorts for a quiet one. A generic plan
	// settles on the walk, which reads every row ever kept for a quiet one, so these two go as the unnamed statement
	// of QueryExecModeExec, planned for each call with its values. TestInteractionsNotCached checks that.
	var q string
	var args []any
	switch {
	case machine != "" && session != "":
		q, args = interactionsMachineSessionSQL, []any{machine, session, limit}
	case machine != "":
		q, args = interactionsMachineSQL, []any{pgx.QueryExecModeExec, machine, limit}
	case session != "":
		q, args = interactionsSessionSQL, []any{pgx.QueryExecModeExec, session, limit}
	default:
		q, args = interactionsSQL, []any{limit}
	}
	rows, err := d.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (InteractionRecord, error) {
		var rec InteractionRecord
		var payload, res []byte
		if err := r.Scan(&payload, &rec.Status, &res, &rec.ResolvedAt); err != nil {
			return rec, err
		}
		if err := json.Unmarshal(payload, &rec.Interaction); err != nil {
			return rec, err
		}
		if len(res) > 0 {
			rec.Resolution = &proto.Resolution{}
			if err := json.Unmarshal(res, rec.Resolution); err != nil {
				return rec, err
			}
		}
		return rec, nil
	})
}

// InteractionRecord is one interaction with its outcome; nil when there is no such row.
func (d *DB) InteractionRecord(ctx context.Context, id string) (*InteractionRecord, error) {
	var rec InteractionRecord
	var payload, res []byte
	err := d.pool.QueryRow(ctx, `SELECT payload, status, resolution, resolved_at FROM interactions WHERE id = $1`, id).
		Scan(&payload, &rec.Status, &res, &rec.ResolvedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(payload, &rec.Interaction); err != nil {
		return nil, err
	}
	if len(res) > 0 {
		rec.Resolution = &proto.Resolution{}
		if err := json.Unmarshal(res, rec.Resolution); err != nil {
			return nil, err
		}
	}
	return &rec, nil
}

// The startup statements of ExpireStaleInteractions. Both must stay answerable from the interactions_pending
// partial index (migration 0393); TestPendingInteractionsIndexed checks the plans of these exact constants.
const (
	resumablePendingSQL = `SELECT payload FROM interactions WHERE status = 'pending'
		AND (payload->>'resumable')::boolean IS TRUE AND (payload->>'expires_at')::timestamptz > now()`
	cancelPendingSQL = `UPDATE interactions SET status = 'cancelled', resolved_at = now()
		WHERE status = 'pending' AND NOT (id = ANY($1))
		AND id NOT IN (SELECT interaction_id FROM session_work WHERE interaction_id <> '')
		AND id NOT IN (SELECT protected.id FROM settings,
			jsonb_array_elements(CASE WHEN jsonb_typeof(value) = 'array' THEN value ELSE '[]'::jsonb END) AS item,
			LATERAL unnest(ARRAY[item->'card'->>'id', item->>'expired_id']) AS protected(id)
			WHERE key = 'agents.tool_loops' AND protected.id IS NOT NULL)`
)

// ExpireStaleInteractions marks interactions left pending by a previous hub process cancelled, and returns the
// resumable ones not yet expired instead: their hooks reconnect and claim them again. The manager's cards about a
// turn (session_work's interaction_id) and loop cards in the manager's settings stay pending too. The manager restores
// them under the same id and ends cards it no longer needs.
func (d *DB) ExpireStaleInteractions(ctx context.Context) ([]proto.Interaction, error) {
	rows, err := d.pool.Query(ctx, resumablePendingSQL)
	if err != nil {
		return nil, err
	}
	keep, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (proto.Interaction, error) {
		var payload []byte
		var ix proto.Interaction
		if err := r.Scan(&payload); err != nil {
			return ix, err
		}
		return ix, json.Unmarshal(payload, &ix)
	})
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(keep))
	for i, ix := range keep {
		ids[i] = ix.ID
	}
	_, err = d.pool.Exec(ctx, cancelPendingSQL, ids)
	return keep, err
}

// InteractionOutcome returns an interaction's status and resolution; an empty status when there is no such row.
func (d *DB) InteractionOutcome(ctx context.Context, id string) (string, *proto.Resolution, error) {
	var status string
	var raw []byte
	err := d.pool.QueryRow(ctx, `SELECT status, resolution FROM interactions WHERE id = $1`, id).Scan(&status, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil || len(raw) == 0 {
		return status, nil, err
	}
	var res proto.Resolution
	return status, &res, json.Unmarshal(raw, &res)
}
