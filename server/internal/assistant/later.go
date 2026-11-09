package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Questions that can wait (#6287, docs/extensions/bots.md "Questions that can wait"): an Agent of a supervised task that asks with
// report_to_supervisor kind "question" and urgent false does not wake anyone. The question is kept here, and the next
// morning or evening briefing (with `facts` on) lists it under "Can wait". It leaves the queue in three ways only: the
// owner marks it handled, its to-do ends (closed or deleted), or laterKeep passes. Nothing guesses from the chat that it
// was answered, and nothing asks again. The assistant's own questions nobody answered wait here too (ask_todo.go): marked
// handled, one of those ends with its to-do (laterHandled).

const laterKey = "assistant.later"

const (
	// laterKeep is how long a question stays in the queue.
	laterKeep = 7 * 24 * time.Hour
	// laterMax is how many questions the queue holds; past it a question is sent as an urgent one.
	laterMax = 100
	// laterSummaryRunes is how much of the question a row quotes.
	laterSummaryRunes = 160
)

// laterAsk is one question in the queue.
type laterAsk struct {
	ID        string    `json:"id"`
	TodoID    string    `json:"todo_id"`
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	Text      string    `json:"text"`
	AskedAt   time.Time `json:"asked_at"`
}

type laterBook struct {
	mu     sync.Mutex
	loaded bool
	asks   []laterAsk
}

// loadLaterLocked reads the queue once; nil means it is in memory. s.later.mu is held. A failed read leaves it unloaded:
// writing after it would put an empty queue over the stored one.
func (s *Service) loadLaterLocked(ctx context.Context) error {
	b := &s.later
	if b.loaded {
		return nil
	}
	var asks []laterAsk
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, laterKey, &asks); err != nil {
			s.logf("later: load: %v", err)
			return err
		}
	}
	b.asks, b.loaded = asks, true
	return nil
}

// saveLaterLocked writes the queue; s.later.mu is held and the queue is loaded.
func (s *Service) saveLaterLocked(ctx context.Context) error {
	st := s.store()
	if st == nil {
		return nil
	}
	asks := s.later.asks
	if asks == nil {
		asks = []laterAsk{}
	}
	return st.PutSetting(ctx, laterKey, asks)
}

// laterBriefed says a morning or evening routine would run with its `facts` on (enabled, daily, with a time of day), so a
// briefing will list the queue. Without one a question left in the queue would never be seen.
func (s *Service) laterBriefed(ctx context.Context) bool {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return false
	}
	defer sc.mu.Unlock()
	for _, t := range sc.tasks {
		if t.Enabled && t.Facts && t.Repeat == repeatDaily && hhmm.MatchString(t.Time) {
			return true
		}
	}
	return false
}

// laterQueue puts a question of the task's agent into the queue; false when it can't be kept (the queue can't be read
// or written, or is full), and the question is then sent as an urgent one. The same question of the same Agent that is
// in the queue already is not put in again (again: it was there).
func (s *Service) laterQueue(ctx context.Context, todo, machine, session, text string, now time.Time) (a laterAsk, queued, again bool) {
	s.laterOpen(ctx, now) // what is over makes room
	b := &s.later
	b.mu.Lock()
	defer b.mu.Unlock()
	if s.loadLaterLocked(ctx) != nil {
		return laterAsk{}, false, false
	}
	for _, x := range b.asks {
		if x.TodoID == todo && x.MachineID == machine && x.SessionID == session && x.Text == text {
			return x, true, true
		}
	}
	if len(b.asks) >= laterMax {
		return laterAsk{}, false, false
	}
	a = laterAsk{ID: "lt_" + randomHex(8), TodoID: todo, MachineID: machine, SessionID: session, Text: text, AskedAt: now.UTC()}
	b.asks = append(b.asks, a)
	if err := s.saveLaterLocked(ctx); err != nil {
		s.logf("later: save: %v", err)
		b.asks = b.asks[:len(b.asks)-1]
		return laterAsk{}, false, false
	}
	return a, true, false
}

// laterOpen are the questions still waiting, the oldest first. The ones that are over (older than laterKeep, or their
// to-do closed or gone) are dropped on the way, and the queue written again.
func (s *Service) laterOpen(ctx context.Context, now time.Time) []laterAsk {
	b := &s.later
	b.mu.Lock()
	if s.loadLaterLocked(ctx) != nil {
		b.mu.Unlock()
		return nil
	}
	list := slices.Clone(b.asks)
	b.mu.Unlock()
	var open []laterAsk
	over := map[string]bool{}
	for _, a := range list {
		if now.Sub(a.AskedAt) >= laterKeep || s.laterTodoEnded(ctx, a.TodoID) {
			over[a.ID] = true
			continue
		}
		open = append(open, a)
	}
	if len(over) > 0 {
		b.mu.Lock()
		b.asks = slices.DeleteFunc(b.asks, func(a laterAsk) bool { return over[a.ID] })
		if err := s.saveLaterLocked(ctx); err != nil {
			s.logf("later: save: %v", err)
		}
		b.mu.Unlock()
	}
	slices.SortStableFunc(open, func(x, y laterAsk) int {
		if c := x.AskedAt.Compare(y.AskedAt); c != 0 {
			return c
		}
		return strings.Compare(x.ID, y.ID)
	})
	return open
}

// laterTodoEnded: the to-do a question belongs to is closed or gone. A to-do that can't be read is not judged over.
func (s *Service) laterTodoEnded(ctx context.Context, id string) bool {
	t, ok, err := s.todo(ctx, id)
	return err == nil && (!ok || t.DoneAt != nil)
}

// laterHandled takes question id out of the queue (the owner marked it handled); false when it is not there, which includes
// one that was over (7 days, its to-do ended) and not yet dropped. An error is a queue that can't be read or written: the
// question stays, and the press is a failure to try again, not "already handled".
//
// An Agent's question only leaves the queue: its supervised task goes on. A question of the assistant's own that nobody
// answered (its to-do came from the ask_user, ask_todo.go) ends with it (#1854): its to-do is closed first, as the owner's
// PATCH done does, so the conversation that asked is told once that it was dropped and the live snapshot stops holding it
// back. A to-do that can't be read or closed is an error too, and the question stays.
func (s *Service) laterHandled(ctx context.Context, id string, now time.Time) (bool, error) {
	s.laterOpen(ctx, now) // what is over goes first
	b := &s.later
	b.mu.Lock()
	if err := s.loadLaterLocked(ctx); err != nil {
		b.mu.Unlock()
		return false, fmt.Errorf("later: load: %w", err)
	}
	i := slices.IndexFunc(b.asks, func(a laterAsk) bool { return a.ID == id })
	if i < 0 {
		b.mu.Unlock()
		return false, nil
	}
	todo := b.asks[i].TodoID
	b.mu.Unlock()
	if isAskTodo(todo) {
		if err := s.closeAskedTodo(ctx, todo); err != nil {
			return false, err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if i = slices.IndexFunc(b.asks, func(a laterAsk) bool { return a.ID == id }); i < 0 {
		return false, nil // another press took it out meanwhile
	}
	gone := b.asks[i]
	b.asks = slices.Delete(slices.Clone(b.asks), i, i+1)
	if err := s.saveLaterLocked(ctx); err != nil {
		s.logf("later: save: %v", err)
		b.asks = slices.Insert(b.asks, i, gone) // not written: it stays
		return false, fmt.Errorf("later: save: %w", err)
	}
	return true, nil
}

// closeAskedTodo closes question to-do id, still open, as the owner's PATCH done does (editTodo tells the conversation that
// asked it was dropped); one closed or deleted already is left as it is.
func (s *Service) closeAskedTodo(ctx context.Context, id string) error {
	t, ok, err := s.todo(ctx, id)
	if err != nil {
		return fmt.Errorf("later: its to-do: %w", err)
	}
	if !ok || t.DoneAt != nil {
		return nil
	}
	done := true
	if _, err := s.editTodo(ctx, id, db.TodoEdit{Done: &done}); err != nil && !errors.Is(err, db.ErrTodoNotFound) {
		return fmt.Errorf("later: close its to-do: %w", err)
	}
	return nil
}

// laterRows are the queue as the briefing lists it, in the shape of the board's rows (what was asked, who asked, how long
// ago, a link to the Agent's chat), the oldest first, at most limit of them, and how many there are in all.
func (s *Service) laterRows(ctx context.Context, l i18n.Lang, web string, now time.Time, limit int) ([]boardRow, int) {
	open := s.laterOpen(ctx, now)
	var rows []boardRow
	for _, a := range open {
		if len(rows) >= limit {
			break
		}
		// Who asked is the Agent by its title; one that is gone or has no title is named by its to-do.
		title := ""
		if sess, ok := s.nodeSession(a.MachineID, a.SessionID); !ok || transcript.Title(sess.Title) == "" {
			if t, ok := s.todoNow(a.TodoID); ok {
				title = t.Title
			}
		}
		rows = append(rows, boardRow{waited: boardWaited(now, a.AskedAt), id: a.ID, summary: clip(oneLine(a.Text), laterSummaryRunes),
			from: s.boardFrom(l, a.MachineID, a.SessionID, title), link: sessionLink(web, a.MachineID, a.SessionID, false)})
	}
	return rows, len(open)
}
