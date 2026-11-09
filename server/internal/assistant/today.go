package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
)

// The "Today" card at the top of the day in an assistant conversation, and the kinds of tasks the user lets start
// without asking (docs/assistant/unified-entry.md "The shape of a chat", decision 1).

// todayBody is what the "Today" card shows: built from what the assistant can read already, no model in between.
type todayBody struct {
	Date string `json:"date"` // the user's day, 2006-01-02
	// Done: tasks of this conversation that finished since yesterday evening.
	Done []todayTask `json:"done,omitempty"`
	// PendingReview: open supervised tasks awaiting review, separate from Waiting / Working.
	PendingReview []todayTask `json:"pending_review,omitempty"`
	// Waiting / Working: the conversation's tasks waiting for the user, and going on.
	Waiting []todayTask `json:"waiting,omitempty"`
	Working int         `json:"working,omitempty"`
	// Meetings: the rest of today's Feishu meetings; AtMe: Feishu chats that @ the user, Unread the other unread ones.
	Meetings []todayMeeting `json:"meetings,omitempty"`
	AtMe     []todayChat    `json:"at_me,omitempty"`
	Unread   int            `json:"unread,omitempty"`
	// Failed: what did not go through since yesterday 18:00 (#4124) — the conversation's tasks whose agent did not
	// start and the scheduled runs that failed or were skipped, newest first. Clients that do not know it ignore it.
	Failed      []todayFailed `json:"failed,omitempty"`
	FailedTotal int           `json:"failed_total,omitempty"` // all of them (Failed is the newest todayFailedMax)
}

// todayFailedMax: how many of them the card lists.
const todayFailedMax = 8

// todayFailed is one thing that did not go through overnight (#4124): the "Today" card says it, so the user does not have
// to piece it together from the pushes they got at the time.
type todayFailed struct {
	Kind  string    `json:"kind"`          // "task": a dispatched task whose agent did not start; "routine": a scheduled run
	ID    string    `json:"id"`            // the todo / the scheduled task
	Ref   string    `json:"ref,omitempty"` // "routine": the run's id
	Title string    `json:"title"`
	At    time.Time `json:"at"`            // when it failed
	Why   string    `json:"why,omitempty"` // the reason, one line, short
	// Skipped: the run was skipped rather than failed (Persona was down at its time…).
	Skipped bool `json:"skipped,omitempty"`
}

type todayTask struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type todayMeeting struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Title string    `json:"title"`
}

type todayChat struct {
	Name    string `json:"name"`
	Unread  int    `json:"unread"`
	Preview string `json:"preview,omitempty"`
}

const feedToday = "today" // ref: the user's date; body: todayBody

// ensureToday is the conversation's "Today" card for the user's day: the one already there, or a new one.
func (s *Service) ensureToday(ctx context.Context, conversation string) (FeedItem, bool, error) {
	// One caller at a time per conversation: the first chat open of the day often comes from two devices or tabs at
	// once, and assistant_feed has no unique key to stop both from appending a card. The others then find this one's.
	s.tasks.todayLocks.acquire(conversation)
	defer s.tasks.todayLocks.release(conversation)
	loc := s.location(ctx)
	now := time.Now().In(loc)
	day := dayStart(now)
	date := now.Format("2006-01-02")
	items, err := s.feedItems(ctx, conversation, day.Add(-time.Second))
	if err != nil {
		return FeedItem{}, false, err
	}
	for _, it := range items {
		if it.Kind == feedToday && it.Ref == date {
			return it, false, nil
		}
	}
	body := todayBody{Date: date}
	since := day.Add(-6 * time.Hour) // yesterday from 18:00: what finished overnight
	if done, err := s.feedItems(ctx, conversation, since); err == nil {
		// A task gets a receipt per stretch of work: it is listed once, by its latest one (done, not cut off after).
		var order []string
		latest := map[string]receiptBody{}
		for _, it := range done {
			if it.Kind != feedReceipt {
				continue
			}
			var rb receiptBody
			if json.Unmarshal(it.Body, &rb) != nil {
				continue
			}
			if _, seen := latest[it.Ref]; !seen {
				order = append(order, it.Ref)
			}
			latest[it.Ref] = rb
		}
		for _, id := range order {
			rb := latest[id]
			if rb.State != "done" {
				continue
			}
			// A done receipt is the end of a stretch of work, not of the task: one still open is not done (#6153).
			// One gone from the list stands on its receipt.
			if t, ok, err := s.todo(ctx, id); err == nil && ok && t.DoneAt == nil {
				continue
			}
			body.Done = append(body.Done, todayTask{ID: id, Title: rb.Title})
		}
	}
	if s.ensureTodosLoaded(ctx) == nil {
		var mine []Todo
		for _, t := range s.todosOf(ctx, conversation) {
			if t.Origin == conversation && t.DoneAt == nil {
				mine = append(mine, t)
			}
		}
		slices.SortFunc(mine, func(a, b Todo) int { return a.CreatedAt.Compare(b.CreatedAt) })
		for _, t := range mine {
			if t.Supervision.state(t) == reviewPending {
				body.PendingReview = append(body.PendingReview, todayTask{ID: t.ID, Title: t.Title})
				continue
			}
			switch taskState(t, s.taskAgents(ctx, t)) {
			case "needs_you", "interrupted":
				body.Waiting = append(body.Waiting, todayTask{ID: t.ID, Title: t.Title})
			case "working":
				body.Working++
			}
		}
	}
	if cal := s.snapshotCalendar(ctx, now); cal != nil {
		for _, e := range cal.Events {
			body.Meetings = append(body.Meetings, todayMeeting{Start: e.Start, End: e.End, Title: e.Title})
		}
	}
	if chats := s.snapshotChats(ctx); chats != nil {
		for _, c := range chats.AtMe {
			body.AtMe = append(body.AtMe, todayChat{Name: c.Name, Unread: c.Unread, Preview: c.Preview})
		}
		body.Unread = len(chats.Other)
	}
	body.Failed, body.FailedTotal = s.todayFailures(ctx, conversation, since, loc)
	it, err := s.appendFeedAt(ctx, conversation, feedToday, date, body, day)
	return it, true, err
}

// dayStart is the first instant of now's calendar day in now's location. Where the clocks jump forward at midnight
// (America/Santiago, America/Havana, America/Asuncion), that midnight does not exist and time.Date answers 23:00 the
// evening before; the day then begins at the jump, 01:00.
func dayStart(now time.Time) time.Time {
	y, m, d := now.Date()
	t := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	if t.Day() != d {
		_, end := t.ZoneBounds()
		return end
	}
	return t
}

// todayFailures is what did not go through since since (#4124), newest first: the conversation's tasks whose agent did
// not start (conversation "*": any conversation's), and the scheduled runs that failed or were skipped. It also returns
// how many there were in all.
func (s *Service) todayFailures(ctx context.Context, conversation string, since time.Time, loc *time.Location) ([]todayFailed, int) {
	var out []todayFailed
	for _, t := range s.todosOf(ctx, conversation) {
		if conversation != "*" && t.Origin != conversation || t.DoneAt == nil || t.StartError == "" || t.DoneAt.Before(since) {
			continue
		}
		out = append(out, todayFailed{Kind: "task", ID: t.ID, Title: t.Title, At: t.DoneAt.In(loc),
			Why: clip(oneLine(t.StartError), 60)})
	}
	if sc, err := s.lockedSchedule(ctx); err == nil {
		for _, r := range sc.missedRuns(since) {
			out = append(out, todayFailed{Kind: "routine", ID: r.RefID, Ref: r.ID, Title: clip(r.Title, 60),
				At: r.At.In(loc), Why: clip(oneLine(r.Error), 60), Skipped: r.Status == runSkipped})
		}
		sc.mu.Unlock()
	}
	slices.SortStableFunc(out, func(a, b todayFailed) int { return b.At.Compare(a.At) })
	total := len(out)
	if len(out) > todayFailedMax {
		out = out[:todayFailedMax]
	}
	return out, total
}

// todayFinished is the tasks that were closed since since without a failed start, newest first (conversation "*": any
// conversation's): the finished side of todayFailures, read from the tasks themselves.
func (s *Service) todayFinished(ctx context.Context, conversation string, since time.Time, loc *time.Location) []todayDone {
	var out []todayDone
	for _, t := range s.todosOf(ctx, conversation) {
		if conversation != "*" && t.Origin != conversation || t.DoneAt == nil || t.StartError != "" || t.DoneAt.Before(since) {
			continue
		}
		out = append(out, todayDone{ID: t.ID, Title: t.Title, At: t.DoneAt.In(loc)})
	}
	slices.SortStableFunc(out, func(a, b todayDone) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// todayDone is one task closed in the window todayFinished looks at.
type todayDone struct {
	ID    string
	Title string
	At    time.Time
}

// ---- kinds that start without asking ----

// trustKind is a remembered kind of task (trustKey), as the settings page lists it.
type trustKind struct {
	Key      string `json:"key"`
	Machine  string `json:"machine"`
	Cwd      string `json:"cwd"`
	Readonly bool   `json:"readonly"`
}

func (s *Service) trustList(ctx context.Context) []trustKind {
	out := []trustKind{}
	for k := range s.trustedKinds(ctx) {
		parts := strings.Split(k, "|")
		if len(parts) != 3 {
			continue
		}
		out = append(out, trustKind{Key: k, Machine: parts[0], Cwd: parts[1], Readonly: parts[2] == "read"})
	}
	slices.SortFunc(out, func(a, b trustKind) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// forgetTrust stops a kind from starting without asking; false when it was not remembered. A failed read is an
// error, never an empty list written back.
func (s *Service) forgetTrust(ctx context.Context, key string) (bool, error) {
	st := s.store()
	if st == nil {
		return false, errors.New("no store")
	}
	s.tasks.trustMu.Lock()
	defer s.tasks.trustMu.Unlock()
	have, err := s.trustedKindsErr(ctx)
	if err != nil {
		return false, err
	}
	if !have[key] {
		return false, nil
	}
	delete(have, key)
	keys := make([]string, 0, len(have))
	for k := range have {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return true, st.PutSetting(ctx, trustSetting, keys)
}

func (s *Service) todayRoutes() {
	s.handle("POST /api/v1/assistant/conversations/{id}/today", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change conversations"))
			return
		}
		it, made, err := s.ensureToday(r.Context(), r.PathValue("id"))
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"item": it, "new": made})
	})
	s.handle("GET /api/v1/task-trust", func(w http.ResponseWriter, r *http.Request) {
		apierr.WriteJSON(w, map[string]any{"kinds": s.trustList(r.Context())})
	})
	s.handle("DELETE /api/v1/task-trust", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change settings"))
			return
		}
		ok, err := s.forgetTrust(r.Context(), r.URL.Query().Get("key"))
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		if !ok {
			apierr.Write(w, http.StatusNotFound, errors.New("not remembered"))
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}
