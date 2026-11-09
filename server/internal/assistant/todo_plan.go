package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// A todo's plan (#1683, docs/assistant/todos.md "Due date, reminder, priority"): when it is due (a moment, or a day in the
// user's time zone), when the phone is reminded of it, and its priority. The apps set them through POST / PATCH
// /api/v1/todos, the assistant through its todo_* tools (todo_tools.go). remindLoop pushes each reminder once, when it is
// due: claimed in the store first, so neither a restart nor a second manager pushes it again.

// dayLayout is how a due day is written (the API's due_date, the tools' due).
const dayLayout = "2006-01-02"

// setPlan takes the plan of row (a stored todo, or one made in memory).
func (t *Todo) setPlan(row db.TodoRow) {
	t.DueAt, t.DueDate = row.DueAt, ""
	if row.DueDate != nil {
		t.DueDate = row.DueDate.UTC().Format(dayLayout)
	}
	t.RemindAt, t.RemindedAt, t.Priority = row.RemindAt, row.RemindedAt, row.Priority
}

// keepPlan writes t's plan into row: a row made in memory for a change that is not to the plan keeps it.
func (t *Todo) keepPlan(row *db.TodoRow) {
	row.DueAt, row.DueDate = t.DueAt, nil
	if d, err := time.Parse(dayLayout, t.DueDate); err == nil {
		row.DueDate = &d
	}
	row.RemindAt, row.RemindedAt, row.Priority = t.RemindAt, t.RemindedAt, t.Priority
}

// todoRowOf is todo t as a row (what memory has of it; the store-less PATCH changes it).
func todoRowOf(t Todo) db.TodoRow {
	row := db.TodoRow{ID: t.ID, Title: t.Title, Notes: t.Notes, DoneAt: t.DoneAt, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		Origin: t.Origin, PlanID: t.PlanID, StartError: t.StartError, DoneBy: t.DoneBy, DoneReason: t.DoneReason,
		ReopenedAt: t.ReopenedAt, ReopenGeneration: t.ReopenGeneration, Quiet: t.Quiet}
	t.keepPlan(&row)
	return row
}

// dueBy is the moment todo t is due by in loc: its due_at, or the end of its due day there; false without either.
func (t Todo) dueBy(loc *time.Location) (time.Time, bool) {
	if t.DueAt != nil {
		return *t.DueAt, true
	}
	if d, err := time.ParseInLocation(dayLayout, t.DueDate, loc); err == nil {
		return d.AddDate(0, 0, 1).Add(-time.Second), true
	}
	return time.Time{}, false
}

// overdue: t is open and its due moment has passed.
func (t Todo) overdue(now time.Time, loc *time.Location) bool {
	by, ok := t.dueBy(loc)
	return ok && t.DoneAt == nil && now.After(by)
}

// dueText is t's due date as the user reads it ("" without one): "11-12 18:00" or the day "11-12".
func (t Todo) dueText(loc *time.Location) string {
	if t.DueAt != nil {
		return t.DueAt.In(loc).Format("01-02 15:04")
	}
	if d, err := time.Parse(dayLayout, t.DueDate); err == nil {
		return d.Format("01-02")
	}
	return ""
}

// todoPlanIn is the plan in a POST or PATCH body: due_at (RFC 3339) or due_date (YYYY-MM-DD), remind_at (RFC 3339),
// priority. A field left out is kept, null (or "") clears it.
type todoPlanIn struct {
	DueAt    json.RawMessage `json:"due_at"`
	DueDate  json.RawMessage `json:"due_date"`
	RemindAt json.RawMessage `json:"remind_at"`
	Priority *string         `json:"priority"`
}

// edit puts the plan into e; an error says which field could not be read.
func (p todoPlanIn) edit(e *db.TodoEdit) error {
	if p.DueAt != nil || p.DueDate != nil {
		at, err := optionalField(p.DueAt, "due_at", func(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) })
		if err != nil {
			return err
		}
		day, err := optionalField(p.DueDate, "due_date", func(s string) (time.Time, error) { return time.Parse(dayLayout, s) })
		if err != nil {
			return err
		}
		if at != nil && day != nil {
			return errors.New("give due_at or due_date, not both")
		}
		e.SetDue, e.DueAt, e.DueDate = true, utcPtr(at), day
	}
	if p.RemindAt != nil {
		at, err := optionalField(p.RemindAt, "remind_at", func(s string) (time.Time, error) { return time.Parse(time.RFC3339, s) })
		if err != nil {
			return err
		}
		e.SetRemind, e.RemindAt = true, utcPtr(at)
	}
	if p.Priority != nil {
		pr, err := todoPriority(*p.Priority)
		if err != nil {
			return err
		}
		e.Priority = &pr
	}
	return nil
}

// optionalField reads a JSON string field with parse: nil for null or "".
func optionalField(raw json.RawMessage, name string, parse func(string) (time.Time, error)) (*time.Time, error) {
	if raw == nil || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("%s must be a string or null", name)
	}
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	t, err := parse(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("can't read %s %q", name, s)
	}
	return &t, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// todoPriority is priority as it is stored: "" for normal (or none given), high, low.
func todoPriority(p string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "normal":
		return "", nil
	case db.PriorityHigh:
		return db.PriorityHigh, nil
	case db.PriorityLow:
		return db.PriorityLow, nil
	}
	return "", fmt.Errorf("priority must be high, normal or low, not %q", p)
}

// createPlainTodo makes a todo of the user's (no conversation, no plan of tasks) with plan's due date, reminder and
// priority, and tells the apps.
func (s *Service) createPlainTodo(ctx context.Context, title, notes string, plan db.TodoEdit) (Todo, error) {
	now := time.Now().UTC()
	row := db.TodoRow{ID: "todo_" + randomHex(8), Title: title, Notes: notes, CreatedAt: now, UpdatedAt: now}
	plan.Title, plan.Notes, plan.Done = nil, nil, nil
	plan.Apply(&row, now)
	return s.insertTodo(ctx, row)
}

// editTodo makes edit e to todo id as the user's PATCH does: stored, put into memory, the apps and the hub told; a
// todo it closes has its outcome recorded and a question's to-do answered as dropped. db.ErrTodoNotFound when it is not
// there.
func (s *Service) editTodo(ctx context.Context, id string, e db.TodoEdit) (Todo, error) {
	s.todos.writing.Lock()
	cur, ok := s.todoNow(id)
	if !ok {
		s.todos.writing.Unlock()
		return Todo{}, db.ErrTodoNotFound
	}
	now := time.Now().UTC()
	var row *db.TodoRow
	if st := s.todoDB(); st != nil {
		var err error
		if row, err = st.EditTodo(ctx, id, e, now); err != nil {
			s.todos.writing.Unlock()
			return Todo{}, err
		}
	} else {
		if e.Done != nil && !*e.Done && cur.DoneAt != nil && cur.ReopenGeneration == 1<<63-1 {
			s.todos.writing.Unlock()
			return Todo{}, errors.New("todo reopen generation exhausted")
		}
		r := todoRowOf(cur)
		e.Apply(&r, now)
		row = &r
	}
	out, doneKeys, applied := s.setTodoRow(id, *row, nil)
	s.todos.writing.Unlock()
	if applied {
		s.todoRowSet(ctx, doneKeys)
	}
	if cur.DoneAt == nil && row.DoneAt != nil {
		// Done by the user: what the task came to goes in its decisions and its projects' timelines (#2820). cur
		// still has the agents that were working on it.
		s.goRecordTaskOutcome(cur, *row.DoneAt)
		s.askTodoClosed(context.WithoutCancel(ctx), id, row.Title) // a question's to-do closed unanswered (ask_todo.go)
	}
	return out, nil
}

// deleteTodo deletes todo id as the user's DELETE does; db.ErrTodoNotFound when it is not there.
func (s *Service) deleteTodo(ctx context.Context, id string) error {
	cur, ok := s.todoNow(id)
	if !ok {
		return db.ErrTodoNotFound
	}
	if err := s.removeTodo(ctx, id); err != nil {
		return err
	}
	s.askTodoClosed(context.WithoutCancel(ctx), id, cur.Title) // a question's to-do deleted unanswered (ask_todo.go)
	return nil
}

// ---- reminders ----

var (
	// remindIdle is how long remindLoop sleeps at most: it wakes early when the todos change.
	remindIdle = time.Minute
	// remindLate: a reminder pushed this much after its time (the manager was down) says when it was for.
	remindLate = 2 * time.Minute
)

// remindWakeup is the channel that wakes remindLoop (the todos changed).
func (b *todoBook) remindWakeup() chan struct{} {
	b.remindOnce.Do(func() { b.remindWake = make(chan struct{}, 1) })
	return b.remindWake
}

// remindSoon has remindLoop look again now.
func (s *Service) remindSoon() {
	select {
	case s.todos.remindWakeup() <- struct{}{}:
	default:
	}
}

// remindLoop pushes the todos' reminders when they are due, sleeping until the next one.
func (s *Service) remindLoop(ctx context.Context) {
	wake := s.todos.remindWakeup()
	for {
		wait := remindIdle
		if s.todoDB() != nil && s.ensureTodosLoaded(ctx) == nil || s.todosLoaded() {
			if next := s.remindDue(ctx, time.Now()); !next.IsZero() {
				wait = min(wait, max(time.Until(next), 50*time.Millisecond))
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// remindDue pushes the reminders due by now and returns when the next one is (zero for none). A mesh member's
// wait while the center pushes the group's (docs/server/mesh.md), as its scheduled tasks do.
func (s *Service) remindDue(ctx context.Context, now time.Time) time.Time {
	type due struct {
		id string
		at time.Time
	}
	var dues []due
	var next time.Time
	s.todos.mu.RLock()
	for id, t := range s.todos.todos {
		if t.RemindAt == nil || t.RemindedAt != nil || t.DoneAt != nil {
			continue
		}
		if !t.RemindAt.After(now) {
			dues = append(dues, due{id, *t.RemindAt})
		} else if next.IsZero() || t.RemindAt.Before(next) {
			next = *t.RemindAt
		}
	}
	s.todos.mu.RUnlock()
	if len(dues) == 0 || s.hello(ctx).Assistant.Standby {
		return next
	}
	slices.SortFunc(dues, func(a, b due) int { return a.at.Compare(b.at) })
	for _, d := range dues {
		s.remind(ctx, d.id, d.at, now)
	}
	return next
}

// remind pushes todo id's reminder for at, once: claimed in the store first (another manager, or this one before a
// restart, may have pushed it), marked in memory either way so it is not looked at again.
func (s *Service) remind(ctx context.Context, id string, at, now time.Time) {
	claimed := true
	if st := s.todoDB(); st != nil {
		ok, err := st.ClaimTodoReminder(ctx, id, at, now.UTC())
		if err != nil {
			s.logf("todo %s: reminder: %v", id, err) // looked at again on the next round
			return
		}
		claimed = ok
	}
	s.todos.mu.Lock()
	t, ok := s.todos.todos[id]
	if !ok || t.RemindAt == nil || !t.RemindAt.Equal(at) || t.RemindedAt != nil {
		s.todos.mu.Unlock()
		return
	}
	stamp := now.UTC()
	t.RemindedAt = &stamp
	todo := copyTodoLocked(t)
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
	if !claimed {
		return
	}
	title, body := s.reminderText(ctx, todo, at, now)
	set := s.settings(ctx)
	s.push(ctx, Notification{Subtitle: i18n.T(s.lang(ctx), "assistant-todos.remind_subtitle"), DeviceID: set.MachineID,
		SessionID: set.Main, Title: title, Body: body})
	s.channelsPushed(set.MachineID, set.Main, title, body) // the Feishu bot and the channels with pushes on, as notify_user
	s.logf("todo %s: reminded (due %s)", id, at.Format(time.RFC3339))
}

// reminderText is the push of t's reminder for at, pushed at now: its title, and its due date, the first line of its
// notes and how late it is in the body.
func (s *Service) reminderText(ctx context.Context, t Todo, at, now time.Time) (title, body string) {
	l, loc := s.lang(ctx), s.location(ctx)
	var parts []string
	if due := t.dueText(loc); due != "" {
		key := "assistant-todos.remind_due"
		if t.overdue(now, loc) {
			key = "assistant-todos.remind_overdue"
		}
		parts = append(parts, i18n.T(l, key, i18n.Args{"when": due}))
	}
	if n := oneLine(t.Notes); n != "" {
		parts = append(parts, clip(n, 120))
	}
	if now.Sub(at) > remindLate {
		parts = append(parts, i18n.T(l, "assistant-todos.remind_late", i18n.Args{"when": at.In(loc).Format("01-02 15:04")}))
	}
	if len(parts) == 0 {
		parts = append(parts, i18n.T(l, "assistant-todos.remind_body"))
	}
	return clip(t.Title, 120), strings.Join(parts, " · ")
}
