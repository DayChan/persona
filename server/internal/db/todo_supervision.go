package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// TodoSupervisionRow is a supervised task's ledger (#3124): its supervisor (the conversation that handed it out),
// what done means, how long it should take, and its review: State "" while it goes on, then pending_review, accepted,
// rework (Round counts them), needs_user or cancelled. Reviews is the assistant's reviews, oldest first, as it wrote
// them (JSON the assistant owns).
type TodoSupervisionRow struct {
	TodoID        string          `json:"todo_id"`
	Supervisor    string          `json:"supervisor"`
	DoneWhen      []string        `json:"done_when"`
	ExpectMinutes int             `json:"expect_minutes"`
	State         string          `json:"state"`
	Round         int             `json:"round"`
	Reviews       json.RawMessage `json:"reviews"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// ListTodoSupervision returns every supervised task's ledger.
func (d *DB) ListTodoSupervision(ctx context.Context) ([]TodoSupervisionRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT todo_id, supervisor, done_when, expect_minutes, state, round, reviews, updated_at
		FROM todo_supervision`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TodoSupervisionRow
	for rows.Next() {
		var r TodoSupervisionRow
		var when []byte
		if err := rows.Scan(&r.TodoID, &r.Supervisor, &when, &r.ExpectMinutes, &r.State, &r.Round, &r.Reviews, &r.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(when, &r.DoneWhen)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PutTodoSupervision writes a task's ledger whole (the assistant keeps it and writes it as it changes).
// ErrTodoNotFound when the todo is gone.
func (d *DB) PutTodoSupervision(ctx context.Context, r TodoSupervisionRow) error {
	when, _ := json.Marshal(r.DoneWhen)
	if r.DoneWhen == nil {
		when = []byte("[]")
	}
	reviews := r.Reviews
	if len(reviews) == 0 {
		reviews = json.RawMessage("[]")
	}
	tag, err := d.pool.Exec(ctx, `INSERT INTO todo_supervision (todo_id, supervisor, done_when, expect_minutes, state, round, reviews, updated_at)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8 WHERE EXISTS (SELECT 1 FROM todos WHERE id = $1)
		ON CONFLICT (todo_id) DO UPDATE SET supervisor = EXCLUDED.supervisor, done_when = EXCLUDED.done_when,
			expect_minutes = EXCLUDED.expect_minutes, state = EXCLUDED.state, round = EXCLUDED.round,
			reviews = EXCLUDED.reviews, updated_at = EXCLUDED.updated_at`,
		r.TodoID, r.Supervisor, when, r.ExpectMinutes, r.State, r.Round, reviews, r.UpdatedAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTodoNotFound
	}
	return nil
}

// ReviewDoneTodo closes todo id after an accepted review (DoneByReview) or cancellation (DoneByCancelled), with
// reason while it is open; false when it was done already. ErrTodoNotFound when it is gone.
func (d *DB) ReviewDoneTodo(ctx context.Context, id, reason string, accepted bool, at time.Time) (*TodoRow, bool, error) {
	doneBy := DoneByCancelled
	if accepted {
		doneBy = DoneByReview
	}
	var r TodoRow
	err := d.pool.QueryRow(ctx, `UPDATE todos SET done_at = $2, done_by = $3, done_reason = $4, updated_at = $2
		WHERE id = $1 AND done_at IS NULL RETURNING `+todoColumns, id, at, doneBy, reason).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, gerr := d.GetTodo(ctx, id); gerr != nil {
			return nil, false, gerr
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &r, true, nil
}
