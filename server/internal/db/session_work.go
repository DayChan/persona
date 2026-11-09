package db

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- agent work states (docs/assistant/agent-work.md) ----

// SessionWork is where one session stands in its work, about its latest turn (session_work).
type SessionWork struct {
	MachineID, SessionID string
	TurnAt               time.Time
	Work                 proto.WorkState
	WakeAt               *time.Time
	Retries, Wakes       int
	InteractionID        string
	UpdatedAt            time.Time
}

// PutSessionWork stores a session's work state, replacing what was there.
func (d *DB) PutSessionWork(ctx context.Context, w SessionWork) error {
	b, err := json.Marshal(w.Work)
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO session_work (machine_id, session_id, turn_at, work, wake_at, retries, wakes,
			interaction_id, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (machine_id, session_id) DO UPDATE SET turn_at = $3, work = $4, wake_at = $5, retries = $6, wakes = $7,
			interaction_id = $8, updated_at = $9`,
		w.MachineID, w.SessionID, w.TurnAt, b, w.WakeAt, w.Retries, w.Wakes, w.InteractionID, w.UpdatedAt)
	return err
}

// SessionWorks are recent states, pending CI watches, and recently retired turns.
// A recent retirement of an old turn must survive the manager restarting.
func (d *DB) SessionWorks(ctx context.Context, since time.Time) ([]SessionWork, error) {
	rows, err := d.pool.Query(ctx, `SELECT machine_id, session_id, turn_at, work, wake_at, retries, wakes, interaction_id,
			updated_at FROM session_work WHERE turn_at > $1 OR (wake_at IS NOT NULL AND work ? 'ci')
			OR (updated_at > $1 AND work ? 'retired_at')`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionWork
	for rows.Next() {
		var w SessionWork
		var b []byte
		if err := rows.Scan(&w.MachineID, &w.SessionID, &w.TurnAt, &b, &w.WakeAt, &w.Retries, &w.Wakes, &w.InteractionID,
			&w.UpdatedAt); err != nil {
			return nil, err
		}
		if json.Unmarshal(b, &w.Work) != nil {
			continue
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// PruneSessionWork keeps active CI watches and recent retirement markers.
func (d *DB) PruneSessionWork(ctx context.Context, before time.Time) error {
	if _, err := d.pool.Exec(ctx, `DELETE FROM session_work WHERE turn_at < $1 AND NOT (wake_at IS NOT NULL AND work ? 'ci')
		AND NOT (updated_at >= $1 AND work ? 'retired_at')`, before); err != nil {
		return err
	}
	_, err := d.pool.Exec(ctx, `DELETE FROM session_work_events WHERE at < $1`, before)
	return err
}

// WorkEvent is one thing that happened about a session's work (session_work_events).
type WorkEvent struct {
	At                   time.Time
	MachineID, SessionID string
	Kind                 string
	Detail               map[string]any
}

// AddWorkEvent records an event.
func (d *DB) AddWorkEvent(ctx context.Context, e WorkEvent) error {
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	b, err := json.Marshal(e.Detail)
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO session_work_events (at, machine_id, session_id, kind, detail)
		VALUES ($1, $2, $3, $4, $5)`, e.At, e.MachineID, e.SessionID, e.Kind, b)
	return err
}

// WorkEvents are the events since since, oldest first.
func (d *DB) WorkEvents(ctx context.Context, since time.Time) ([]WorkEvent, error) {
	rows, err := d.pool.Query(ctx, `SELECT at, machine_id, session_id, kind, detail FROM session_work_events
		WHERE at > $1 ORDER BY at, id`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkEvent
	for rows.Next() {
		var e WorkEvent
		var b []byte
		if err := rows.Scan(&e.At, &e.MachineID, &e.SessionID, &e.Kind, &b); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(b, &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}
