package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrTodoNotFound  = errors.New("todo not found")
	ErrTodoCompleted = errors.New("todo is already completed")
)

type TodoRow struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Notes     string     `json:"notes"`
	DoneAt    *time.Time `json:"done_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	// Origin is the assistant conversation that opened it as a task, PlanID the plan it came from (#930); empty
	// for a todo made by hand.
	Origin string `json:"origin"`
	PlanID string `json:"plan_id"`
	// StartError: the task's agent did not start, and why; such a todo is done (#1077). Empty otherwise.
	StartError string `json:"start_error"`
	// DoneBy is who marked it done: "" the user (or a failed start), DoneByAuto the hub's judge (#1092),
	// DoneByReview an accepted review, or DoneByCancelled a cancellation, with DoneReason.
	// ReopenedAt: when the user last reopened it after it was done.
	DoneBy     string     `json:"done_by"`
	DoneReason string     `json:"done_reason"`
	ReopenedAt *time.Time `json:"reopened_at"`
	// Quiet: a task from the main conversation while assistant.main_tasks_quiet was on (#1731): no pushes, Live
	// Activity or task badges; the main conversation hears about it instead. Set once, when it is made.
	Quiet bool `json:"quiet"`
	// DueAt is the moment it is due, DueDate the day (midnight UTC: a calendar day, read in the user's time zone);
	// at most one is set (#1683). RemindAt is when the phone is pushed about it, RemindedAt when that was done;
	// Priority is "" (normal), PriorityHigh or PriorityLow.
	DueAt      *time.Time `json:"due_at"`
	DueDate    *time.Time `json:"due_date"`
	RemindAt   *time.Time `json:"remind_at"`
	RemindedAt *time.Time `json:"reminded_at"`
	Priority   string     `json:"priority"`
}

// A todo's priorities besides normal ("").
const (
	PriorityHigh = "high"
	PriorityLow  = "low"
)

// DoneByAuto marks a todo the hub closed because its task's work was judged finished (docs/assistant/todos.md "Auto-complete").
const DoneByAuto = "auto"

// DoneByReview marks a todo its supervisor (the main conversation's assistant) accepted after reviewing the work, with
// DoneReason (#3124).
const DoneByReview = "review"

// DoneByCancelled marks a cancelled task; further agent work cannot reopen it (#1851).
const DoneByCancelled = "cancelled"

type TodoSpaceRow struct {
	TodoID    string    `json:"todo_id"`
	MachineID string    `json:"machine_id"`
	Space     string    `json:"space"`
	LinkedAt  time.Time `json:"linked_at"`
}

type TodoAgentRow struct {
	TodoID      string     `json:"todo_id"`
	MachineID   string     `json:"machine_id"`
	SessionID   string     `json:"session_id"`
	LinkedAt    time.Time  `json:"linked_at"`
	ReleasedAt  *time.Time `json:"released_at"`
	Title       string     `json:"title"`
	Cwd         string     `json:"cwd"`
	Agent       string     `json:"agent"`
	StartError  string     `json:"start_error"`
	Source      string     `json:"source"`
	Via         string     `json:"via"`
	DismissedAt *time.Time `json:"dismissed_at"`
}

const todoColumns = `id, title, notes, done_at, created_at, updated_at, origin, plan_id, start_error, done_by, done_reason, reopened_at, quiet,
	due_at, due_date, remind_at, reminded_at, priority`

// todoDest is where a row of todoColumns is scanned to.
func todoDest(r *TodoRow) []any {
	return []any{&r.ID, &r.Title, &r.Notes, &r.DoneAt, &r.CreatedAt, &r.UpdatedAt, &r.Origin, &r.PlanID, &r.StartError, &r.DoneBy,
		&r.DoneReason, &r.ReopenedAt, &r.Quiet, &r.DueAt, &r.DueDate, &r.RemindAt, &r.RemindedAt, &r.Priority}
}

// ListTodos returns all todos ordered by created_at DESC.
func (d *DB) ListTodos(ctx context.Context) ([]TodoRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+todoColumns+` FROM todos ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TodoRow
	for rows.Next() {
		var r TodoRow
		if err := rows.Scan(todoDest(&r)...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAllTodoSpaces returns all space links.
func (d *DB) ListAllTodoSpaces(ctx context.Context) ([]TodoSpaceRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT todo_id, machine_id, space, linked_at FROM todo_spaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TodoSpaceRow
	for rows.Next() {
		var r TodoSpaceRow
		if err := rows.Scan(&r.TodoID, &r.MachineID, &r.Space, &r.LinkedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAllTodoAgents returns all agent links.
func (d *DB) ListAllTodoAgents(ctx context.Context) ([]TodoAgentRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT todo_id, machine_id, session_id, linked_at, released_at, title, cwd, agent, start_error, source, via, dismissed_at FROM todo_agents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TodoAgentRow
	for rows.Next() {
		var r TodoAgentRow
		if err := rows.Scan(&r.TodoID, &r.MachineID, &r.SessionID, &r.LinkedAt, &r.ReleasedAt, &r.Title, &r.Cwd, &r.Agent, &r.StartError, &r.Source, &r.Via, &r.DismissedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetTodo returns one todo by id, or ErrTodoNotFound.
func (d *DB) GetTodo(ctx context.Context, id string) (*TodoRow, error) {
	var r TodoRow
	err := d.pool.QueryRow(ctx, `SELECT `+todoColumns+` FROM todos WHERE id = $1`, id).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTodoNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateTodo inserts a new todo.
func (d *DB) CreateTodo(ctx context.Context, t TodoRow) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO todos (id, title, notes, done_at, created_at, updated_at, origin, plan_id, start_error, done_by,
		done_reason, reopened_at, quiet, due_at, due_date, remind_at, reminded_at, priority)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)`,
		t.ID, t.Title, t.Notes, t.DoneAt, t.CreatedAt, t.UpdatedAt, t.Origin, t.PlanID, t.StartError, t.DoneBy, t.DoneReason, t.ReopenedAt, t.Quiet,
		t.DueAt, t.DueDate, t.RemindAt, t.RemindedAt, t.Priority)
	return err
}

// UpdateTodo updates only the fields that are non-nil.
func (d *DB) UpdateTodo(ctx context.Context, id string, title *string, notes *string, done *bool, updatedAt time.Time) (*TodoRow, error) {
	return d.EditTodo(ctx, id, TodoEdit{Title: title, Notes: notes, Done: done}, updatedAt)
}

// TodoEdit is a change to a todo by the user or the assistant on their behalf (#1683): the fields that are non-nil, and
// the due date and the reminder when SetDue / SetRemind (nil clears them).
type TodoEdit struct {
	Title, Notes *string
	Done         *bool
	SetDue       bool
	DueAt        *time.Time
	DueDate      *time.Time
	SetRemind    bool
	RemindAt     *time.Time
	Priority     *string
}

// Apply makes the edit to cur at the moment at: done stamps done_at (once), not done reopens it (reopened_at, no
// longer failed); a reminder at another moment is to be pushed again. The store and the store-less memory both
// change a todo with it.
func (e TodoEdit) Apply(cur *TodoRow, at time.Time) {
	if e.Title != nil {
		cur.Title = *e.Title
	}
	if e.Notes != nil {
		cur.Notes = *e.Notes
	}
	if e.Done != nil {
		if *e.Done {
			if cur.DoneAt == nil {
				cur.DoneAt = &at
				cur.DoneBy, cur.DoneReason = "", ""
			}
		} else {
			if cur.DoneAt != nil {
				cur.ReopenedAt = &at // the same stretch of work must not close it again
			}
			cur.DoneAt = nil
			cur.StartError = "" // reopened: no longer failed
			cur.DoneBy, cur.DoneReason = "", ""
		}
	}
	if e.SetDue {
		cur.DueAt, cur.DueDate = e.DueAt, e.DueDate
	}
	if e.SetRemind && !sameMoment(cur.RemindAt, e.RemindAt) {
		cur.RemindAt, cur.RemindedAt = e.RemindAt, nil
	}
	if e.Priority != nil {
		cur.Priority = *e.Priority
	}
	cur.UpdatedAt = at
}

func sameMoment(a, b *time.Time) bool {
	return a == nil && b == nil || a != nil && b != nil && a.Equal(*b)
}

// EditTodo makes edit e to todo id at updatedAt (TodoEdit.Apply) and returns the row as it is now; ErrTodoNotFound when
// it is not there.
func (d *DB) EditTodo(ctx context.Context, id string, e TodoEdit, updatedAt time.Time) (*TodoRow, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	var updated TodoRow
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var cur TodoRow
		err := tx.QueryRow(ctx, `SELECT `+todoColumns+` FROM todos WHERE id = $1 FOR UPDATE`, id).
			Scan(todoDest(&cur)...)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTodoNotFound
		}
		if err != nil {
			return err
		}
		e.Apply(&cur, updatedAt)
		_, err = tx.Exec(ctx, `UPDATE todos SET title = $2, notes = $3, done_at = $4, updated_at = $5, start_error = $6,
			done_by = $7, done_reason = $8, reopened_at = $9, due_at = $10, due_date = $11, remind_at = $12, reminded_at = $13,
			priority = $14 WHERE id = $1`,
			cur.ID, cur.Title, cur.Notes, cur.DoneAt, cur.UpdatedAt, cur.StartError, cur.DoneBy, cur.DoneReason, cur.ReopenedAt,
			cur.DueAt, cur.DueDate, cur.RemindAt, cur.RemindedAt, cur.Priority)
		if err != nil {
			return err
		}
		updated = cur
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// ClaimTodoReminder marks todo id reminded at now, but only while its reminder is still the one at remindAt, not yet
// pushed and the todo not done: true when this call claimed it (the caller pushes it), false when it is gone, done,
// moved or already claimed. Claimed before the push, so a push is never made twice (a lost one is not retried).
func (d *DB) ClaimTodoReminder(ctx context.Context, id string, remindAt, now time.Time) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE todos SET reminded_at = $3
		WHERE id = $1 AND remind_at = $2 AND reminded_at IS NULL AND done_at IS NULL`, id, remindAt, now)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// RetitleTodo replaces a todo's title with to only while it is still from: an automatic title (#2775) never writes
// over one the user gave meanwhile. False (and no error) when the title had changed or the todo is gone.
func (d *DB) RetitleTodo(ctx context.Context, id, from, to string, at time.Time) (*TodoRow, bool, error) {
	var r TodoRow
	err := d.pool.QueryRow(ctx, `UPDATE todos SET title = $3, updated_at = $4 WHERE id = $1 AND title = $2
		RETURNING `+todoColumns, id, from, to, at).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &r, true, nil
}

// FailTodo ends a todo whose agent did not start: done (unless it already was) with why (#1077).
func (d *DB) FailTodo(ctx context.Context, id, startError string, at time.Time) (*TodoRow, error) {
	var r TodoRow
	err := d.pool.QueryRow(ctx, `UPDATE todos SET done_at = COALESCE(done_at, $2), start_error = $3, updated_at = $2 WHERE id = $1
		RETURNING `+todoColumns, id, at, startError).
		Scan(todoDest(&r)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTodoNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// RetryTodo reopens a todo whose agent did not start, to start it again (#1396): not done, no error, reopened now.
// False (and no error) when it is not failed any more: a retry that came first already reopened it.
func (d *DB) RetryTodo(ctx context.Context, id string, at time.Time) (*TodoRow, bool, error) {
	var r TodoRow
	err := d.pool.QueryRow(ctx, `UPDATE todos SET done_at = NULL, start_error = '', done_by = '', done_reason = '',
		reopened_at = $2, updated_at = $2 WHERE id = $1 AND done_at IS NOT NULL AND start_error <> ''
		RETURNING `+todoColumns, id, at).
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

// AutoDoneTodo marks a task done by the hub (DoneByAuto, with why) when it is still open and the user has not
// reopened it since workSince, the start of the work that was judged; false (and no error) when either changed.
func (d *DB) AutoDoneTodo(ctx context.Context, id, reason string, at, workSince time.Time) (*TodoRow, bool, error) {
	var r TodoRow
	err := d.pool.QueryRow(ctx, `UPDATE todos SET done_at = $2, done_by = $3, done_reason = $4, updated_at = $2
		WHERE id = $1 AND done_at IS NULL AND (reopened_at IS NULL OR reopened_at < $5)
		RETURNING `+todoColumns, id, at, DoneByAuto, reason, workSince).
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

// DeleteTodo deletes a todo by id. Cascading deletes remove its todo_agents.
func (d *DB) DeleteTodo(ctx context.Context, id string) error {
	tag, err := d.pool.Exec(ctx, `DELETE FROM todos WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTodoNotFound
	}
	return nil
}

// LinkTodoSpace links a project space to a todo.
// If the space is already linked to another todo, that link is removed and its space-sourced agents are removed.
// Returns the old todo ID if one was unlinked.
func (d *DB) LinkTodoSpace(ctx context.Context, todoID, machineID, space string, linkedAt time.Time) (string, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()

	var oldTodoID string
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var targetDoneAt *time.Time
		err := tx.QueryRow(ctx, `SELECT done_at FROM todos WHERE id = $1 FOR UPDATE`, todoID).Scan(&targetDoneAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTodoNotFound
		}
		if err != nil {
			return err
		}
		if targetDoneAt != nil {
			return ErrTodoCompleted
		}

		// Check if space was linked to another todo
		var curTodoID string
		err = tx.QueryRow(ctx, `SELECT todo_id FROM todo_spaces WHERE machine_id = $1 AND space = $2 FOR UPDATE`, machineID, space).Scan(&curTodoID)
		if err == nil {
			if curTodoID != todoID {
				oldTodoID = curTodoID
				if _, err := tx.Exec(ctx, `DELETE FROM todo_spaces WHERE machine_id = $1 AND space = $2`, machineID, space); err != nil {
					return err
				}
				viaKey := machineID + "/" + space
				if _, err := tx.Exec(ctx, `DELETE FROM todo_agents WHERE todo_id = $1 AND source = 'space' AND via = $2`, oldTodoID, viaKey); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, oldTodoID, linkedAt); err != nil {
					return err
				}
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		// Insert or update space link
		_, err = tx.Exec(ctx, `INSERT INTO todo_spaces (todo_id, machine_id, space, linked_at)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (machine_id, space) DO UPDATE SET
				todo_id = $1,
				linked_at = $4`,
			todoID, machineID, space, linkedAt)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, todoID, linkedAt)
		return err
	})
	if err != nil {
		return "", err
	}
	return oldTodoID, nil
}

// UnlinkTodoSpace removes a space link and physically deletes source=space agents linked via this space.
func (d *DB) UnlinkTodoSpace(ctx context.Context, todoID, machineID, space string) error {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM todos WHERE id = $1)`, todoID).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			return ErrTodoNotFound
		}

		tag, err := tx.Exec(ctx, `DELETE FROM todo_spaces WHERE todo_id = $1 AND machine_id = $2 AND space = $3`, todoID, machineID, space)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrTodoNotFound
		}

		viaKey := machineID + "/" + space
		if _, err := tx.Exec(ctx, `DELETE FROM todo_agents WHERE todo_id = $1 AND source = 'space' AND via = $2`, todoID, viaKey); err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at = now() WHERE id = $1`, todoID)
		return err
	})
}

// RenameTodoSpace migrates todo_spaces and todo_agents via when a space is renamed via hub.
// If newSpace is already linked to another todo (unique constraint conflict), the old space's
// link is voided (deleted) rather than migrating, returning (voided: true, err: nil).
func (d *DB) RenameTodoSpace(ctx context.Context, machineID, oldSpace, newSpace string) (bool, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()

	var voided bool
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		// Check if newSpace already exists in todo_spaces for this machine.
		var existingTodoID string
		err := tx.QueryRow(ctx, `SELECT todo_id FROM todo_spaces WHERE machine_id = $1 AND space = $2 FOR UPDATE`, machineID, newSpace).Scan(&existingTodoID)
		if err == nil {
			// Conflict: newSpace is already linked to existingTodoID.
			// Void old link to avoid violating unique constraint.
			voided = true
			oldVia := machineID + "/" + oldSpace
			if _, err := tx.Exec(ctx, `DELETE FROM todo_spaces WHERE machine_id = $1 AND space = $2`, machineID, oldSpace); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM todo_agents WHERE source = 'space' AND via = $1`, oldVia); err != nil {
				return err
			}
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		if _, err := tx.Exec(ctx, `UPDATE todo_spaces SET space = $3 WHERE machine_id = $1 AND space = $2`, machineID, oldSpace, newSpace); err != nil {
			return err
		}
		oldVia := machineID + "/" + oldSpace
		newVia := machineID + "/" + newSpace
		if _, err := tx.Exec(ctx, `UPDATE todo_agents SET via = $2 WHERE source = 'space' AND via = $1`, oldVia, newVia); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return voided, nil
}

// lockTodoAgentMembership serializes one session's membership, then locks every affected parent in ID order.
func lockTodoAgentMembership(ctx context.Context, tx pgx.Tx, todoID, machineID, sessionID string) (*time.Time, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
		jsonb_build_array('todo-agent', $1::text, $2::text)::text, 0))`, machineID, sessionID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT id, done_at FROM todos
		WHERE id = $1 OR id IN (SELECT todo_id FROM todo_agents
			WHERE machine_id = $2 AND session_id = $3 AND released_at IS NULL)
		ORDER BY id FOR UPDATE`, todoID, machineID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found := false
	var targetDoneAt *time.Time
	for rows.Next() {
		var id string
		var doneAt *time.Time
		if err := rows.Scan(&id, &doneAt); err != nil {
			return nil, err
		}
		if id == todoID {
			found, targetDoneAt = true, doneAt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrTodoNotFound
	}
	return targetDoneAt, nil
}

// AutoLinkTodoAgent links an agent automatically if not already present.
// Unlike LinkTodoAgentWithSource, it uses INSERT ... ON CONFLICT DO NOTHING so that
// any existing row (such as a dismissed link from an immediate unlink) is NOT overwritten.
// Returns (inserted bool, releasedTodoIDs []string, err error).
func (d *DB) AutoLinkTodoAgent(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) (bool, []string, error) {
	return d.autoLinkTodoAgent(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, source, via, linkedAt, false)
}

// AutoLinkUnlinkedTodoAgent is AutoLinkTodoAgent for an agent that worked for no todo when the link was decided (the
// assistant's automatic links): it links nothing when the agent works for another open todo by the time it is
// written, or was linked to any todo after linkedAt, so a link by hand committed in between is never moved (#2704).
// A done todo it was on before took part only: that link is released, as a link by hand releases it (#2904).
func (d *DB) AutoLinkUnlinkedTodoAgent(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) (bool, []string, error) {
	return d.autoLinkTodoAgent(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, source, via, linkedAt, true)
}

func (d *DB) autoLinkTodoAgent(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time, onlyUnlinked bool) (bool, []string, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return false, nil, err
	}
	defer conn.Release()

	var inserted bool
	var releasedTodoIDs []string
	alreadyLinked := errors.New("todo agent link already exists")
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		targetDoneAt, err := lockTodoAgentMembership(ctx, tx, todoID, machineID, sessionID)
		if err != nil {
			return err
		}
		if targetDoneAt != nil {
			return ErrTodoCompleted
		}

		// An existing or dismissed target must not release another active membership.
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM todo_agents
			WHERE todo_id = $1 AND machine_id = $2 AND session_id = $3)`, todoID, machineID, sessionID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}

		// Find other active links for this agent and release them. held: one on an open todo, or made after linkedAt.
		rows, err := tx.Query(ctx, `SELECT a.todo_id, t.done_at IS NULL OR a.linked_at > $4 FROM todo_agents a JOIN todos t ON t.id = a.todo_id
			WHERE a.machine_id = $1 AND a.session_id = $2 AND a.released_at IS NULL AND a.todo_id != $3`,
			machineID, sessionID, todoID, linkedAt)
		if err != nil {
			return err
		}
		heldElsewhere := false
		for rows.Next() {
			var oid string
			var held bool
			if err := rows.Scan(&oid, &held); err != nil {
				rows.Close()
				return err
			}
			releasedTodoIDs = append(releasedTodoIDs, oid)
			heldElsewhere = heldElsewhere || held
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if onlyUnlinked && heldElsewhere {
			releasedTodoIDs = nil // it works for another todo now (under the membership lock): leave it there
			return nil
		}

		if len(releasedTodoIDs) > 0 {
			_, err = tx.Exec(ctx, `UPDATE todo_agents SET released_at = $3 WHERE machine_id = $1 AND session_id = $2 AND released_at IS NULL AND todo_id != $4`,
				machineID, sessionID, linkedAt, todoID)
			if err != nil {
				return err
			}
			for _, oid := range releasedTodoIDs {
				if _, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, oid, linkedAt); err != nil {
					return err
				}
			}
		}

		// Release the old active row before inserting: the partial unique index is immediate.
		tag, err := tx.Exec(ctx, `INSERT INTO todo_agents (todo_id, machine_id, session_id, linked_at, released_at, title, cwd, agent, start_error, source, via, dismissed_at)
			VALUES ($1, $2, $3, $4, NULL, $5, $6, $7, $8, $9, $10, NULL)
			ON CONFLICT (todo_id, machine_id, session_id) DO NOTHING`,
			todoID, machineID, sessionID, linkedAt, title, cwd, agent, startError, source, via)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return alreadyLinked // Roll back any releases if an unexpected target appeared.
		}
		inserted = true
		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, todoID, linkedAt)
		return err
	})
	if errors.Is(err, alreadyLinked) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	return inserted, releasedTodoIDs, nil
}

// LinkTodoAgent links an agent manually to a todo.
func (d *DB) LinkTodoAgent(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError string, linkedAt time.Time) ([]string, error) {
	return d.LinkTodoAgentWithSource(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, "manual", "", linkedAt)
}

// LinkTodoAgentWithSource links an agent with a specific source ("manual", "space", "spawned") and via.
func (d *DB) LinkTodoAgentWithSource(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) ([]string, error) {
	return d.linkTodoAgentWithSource(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, source, via, false, linkedAt)
}

// LinkTodoAgentOfDoneTask links an agent to a todo that is already done (see linkTodoAgentWithSource's allowDone):
// the agent of a task the user finished while it was still starting. Linking by hand stays refused (#2954).
func (d *DB) LinkTodoAgentOfDoneTask(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, linkedAt time.Time) ([]string, error) {
	return d.linkTodoAgentWithSource(ctx, todoID, machineID, sessionID, title, cwd, agent, startError, source, via, true, linkedAt)
}

// linkTodoAgentWithSource links an agent to a todo, refusing a todo that is done (ErrTodoCompleted) unless allowDone:
// a done todo keeps the agents started for it (tasks_close.go retires them), it just gets no new ones by hand.
func (d *DB) linkTodoAgentWithSource(ctx context.Context, todoID, machineID, sessionID, title, cwd, agent, startError, source, via string, allowDone bool, linkedAt time.Time) ([]string, error) {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	var releasedTodoIDs []string
	err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		targetDoneAt, err := lockTodoAgentMembership(ctx, tx, todoID, machineID, sessionID)
		if err != nil {
			return err
		}
		if targetDoneAt != nil && !allowDone {
			return ErrTodoCompleted
		}

		// Find other active links for this agent and release them.
		rows, err := tx.Query(ctx, `SELECT todo_id FROM todo_agents WHERE machine_id = $1 AND session_id = $2 AND released_at IS NULL AND todo_id != $3`,
			machineID, sessionID, todoID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var oid string
			if err := rows.Scan(&oid); err != nil {
				rows.Close()
				return err
			}
			releasedTodoIDs = append(releasedTodoIDs, oid)
		}
		rows.Close()

		if len(releasedTodoIDs) > 0 {
			_, err = tx.Exec(ctx, `UPDATE todo_agents SET released_at = $3 WHERE machine_id = $1 AND session_id = $2 AND released_at IS NULL AND todo_id != $4`,
				machineID, sessionID, linkedAt, todoID)
			if err != nil {
				return err
			}
			for _, oid := range releasedTodoIDs {
				if _, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, oid, linkedAt); err != nil {
					return err
				}
			}
		}

		// Upsert link on target todo. Clears dismissed_at and sets source/via.
		_, err = tx.Exec(ctx, `INSERT INTO todo_agents (todo_id, machine_id, session_id, linked_at, released_at, title, cwd, agent, start_error, source, via, dismissed_at)
			VALUES ($1, $2, $3, $4, NULL, $5, $6, $7, $8, $9, $10, NULL)
			ON CONFLICT (todo_id, machine_id, session_id) DO UPDATE SET
				linked_at = $4,
				released_at = NULL,
				dismissed_at = NULL,
				source = $9,
				via = $10,
				title = CASE WHEN $5 != '' THEN $5 ELSE todo_agents.title END,
				cwd = CASE WHEN $6 != '' THEN $6 ELSE todo_agents.cwd END,
				agent = CASE WHEN $7 != '' THEN $7 ELSE todo_agents.agent END,
				start_error = $8`,
			todoID, machineID, sessionID, linkedAt, title, cwd, agent, startError, source, via)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, todoID, linkedAt)
		return err
	})
	if err != nil {
		return nil, err
	}
	return releasedTodoIDs, nil
}

// UnlinkTodoAgent marks the agent as released and dismissed on a todo (soft unlink, #762).
func (d *DB) UnlinkTodoAgent(ctx context.Context, todoID, machineID, sessionID string) error {
	conn, err := d.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		if _, err := lockTodoAgentMembership(ctx, tx, todoID, machineID, sessionID); err != nil {
			return err
		}

		now := time.Now().UTC()
		_, err := tx.Exec(ctx, `INSERT INTO todo_agents (todo_id, machine_id, session_id, linked_at, released_at, dismissed_at)
			VALUES ($1, $2, $3, $4, $4, $4)
			ON CONFLICT (todo_id, machine_id, session_id) DO UPDATE SET
				released_at = $4,
				dismissed_at = $4`,
			todoID, machineID, sessionID, now)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE todos SET updated_at = $2 WHERE id = $1`, todoID, now)
		return err
	})
}

// UpdateTodoAgentSnapshot updates the title and start_error snapshot of an active agent link.
func (d *DB) UpdateTodoAgentSnapshot(ctx context.Context, machineID, sessionID, title, startError string) error {
	_, err := d.pool.Exec(ctx, `UPDATE todo_agents SET
		title = CASE WHEN $3 != '' THEN $3 ELSE title END,
		start_error = $4
		WHERE machine_id = $1 AND session_id = $2 AND released_at IS NULL`,
		machineID, sessionID, title, startError)
	return err
}
