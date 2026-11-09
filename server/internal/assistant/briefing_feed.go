package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The `brief` item of a routine's conversation (docs/protocol/protocol.md "Conversation feed"): the facts of a run with facts on,
// written when the run is sent whether or not the Lark bot is on, so the apps show the same fixed part as the Lark card.

const feedBrief = "brief" // ref: the routine's task id; body: briefBody

// briefBody is a `brief` item's body. The rows are the facts the run started with: all the cards waiting and all the tasks
// to verify, and the newest of done (10) and failed (8), whose totals say how many there were. Texts the hub writes (a
// waiting card's line) are in the bot's language.
type briefBody struct {
	Title string    `json:"title"` // the routine's name
	At    time.Time `json:"at"`
	Hours int       `json:"hours"` // how far back done / failed look
	// WaitingUnknown: the cards waiting were not read yet (the hub had just started): `waiting` is empty for want of knowing.
	WaitingUnknown bool            `json:"waiting_unknown,omitempty"`
	Waiting        []briefBodyRow  `json:"waiting"`
	Done           []briefBodyDone `json:"done"`
	DoneTotal      int             `json:"done_total"`
	Failed         []todayFailed   `json:"failed"`
	FailedTotal    int             `json:"failed_total"`
	Accept         []briefBodyRow  `json:"accept"`
	// Later are the questions Agents left to wait for a briefing, the oldest first (at most 10), later_total how many there were; both
	// are left out when there are none (a client that does not draw them skips them).
	Later      []briefBodyRow `json:"later,omitempty"`
	LaterTotal int            `json:"later_total,omitempty"`
	// Progress are the tasks being worked on, the longest running first (at most 8); stage is plan / develop / review / merge / deploy / verify, "" when not known.
	Progress      []briefBodyProgress `json:"progress"`
	ProgressTotal int                 `json:"progress_total"`
}

// briefBodyRow is a row of what waits: a card (waiting) with its line, or a task to verify (accept) with its title.
type briefBodyRow struct {
	ID      string    `json:"id"`                // the card (waiting), the task (accept), the question (later); POST /api/v1/later/{id}/handled takes the last
	Title   string    `json:"title"`             // waiting: what the card asks, in a line; accept: the task's title
	Detail  string    `json:"detail,omitempty"`  // accept: what changed, in the words of its "Please verify" card
	From    string    `json:"from,omitempty"`    // who asked
	Link    string    `json:"link,omitempty"`    // the page in the web app (empty when hub.json has no public_url)
	WaitedS int64     `json:"waited_s"`          // seconds waited when the run started
	Nudged  bool      `json:"nudged,omitempty"`  // waiting: the bot nudged under its card
	AskedAt time.Time `json:"asked_at,omitzero"` // accept: when the round's "Please verify" ask was made (with or without a Lark card); POST /api/v1/todos/{id}/accept takes it back
	// accept: the verdict line, word for word as its Lark card says it (#1226): "done, deployed" for a deployed task, or a
	// supervised task's conclusion. outcome is a conclusion's verdict (done, cannot or decide; left out for a deployed task),
	// recommend a decision's recommended option, which Accept takes.
	Verdict   string `json:"verdict,omitempty"`
	Outcome   string `json:"outcome,omitempty"`
	Recommend string `json:"recommend,omitempty"`
}

// briefBodyDone is a task closed in the window.
type briefBodyDone struct {
	ID    string    `json:"id"`
	Title string    `json:"title"`
	At    time.Time `json:"at"`
}

// briefBodyProgress is a task being worked on.
type briefBodyProgress struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Stage string `json:"stage,omitempty"`
}

// body is the facts as a `brief` item's body.
func (f *briefFacts) body(s *Service, title string) briefBody {
	b := briefBody{Title: title, At: f.At, Hours: int(briefWindow / time.Hour), WaitingUnknown: f.WaitingUnknown, DoneTotal: f.DoneTotal,
		Failed: f.Failed, FailedTotal: f.FailedTotal, Waiting: []briefBodyRow{}, Done: []briefBodyDone{}, Accept: []briefBodyRow{}, ProgressTotal: f.ProgressTotal, Progress: []briefBodyProgress{}}
	if b.Failed == nil {
		b.Failed = []todayFailed{}
	}
	row := func(r boardRow) briefBodyRow {
		return briefBodyRow{ID: r.id, Title: r.summary, From: r.from, Link: r.link, WaitedS: int64(r.waited / time.Second), Nudged: r.nudged}
	}
	for _, r := range f.Waiting {
		b.Waiting = append(b.Waiting, row(r))
	}
	for _, r := range f.Accept {
		x := row(r)
		x.Title, x.Detail, x.AskedAt = r.id, f.Changed[r.id], r.asked
		if a, ok := f.Asks[r.id]; ok {
			x.Verdict, x.Outcome = verdictText(f.l, &a), a.Outcome
			if decideAsk(&a) {
				x.Recommend = a.Recommend
			}
		}
		if t, ok := s.todoNow(r.id); ok {
			x.Title = t.Title
		}
		b.Accept = append(b.Accept, x)
	}
	for _, r := range f.Later {
		b.Later = append(b.Later, row(r))
	}
	b.LaterTotal = f.LaterTotal
	for _, d := range f.Done {
		b.Done = append(b.Done, briefBodyDone{ID: d.ID, Title: d.Title, At: d.At})
	}
	for _, p := range f.Progress {
		b.Progress = append(b.Progress, briefBodyProgress{ID: p.ID, Title: p.Title, Stage: p.Stage})
	}
	return b
}

// briefFeed writes a run's facts into its conversation, with the time it was sent as its time: after the run's ⏰ message.
func (s *Service) briefFeed(session, task, title string, f *briefFacts) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.appendFeed(ctx, session, feedBrief, task, f.body(s, title)); err != nil {
		s.logf("brief: the feed item of %s: %v", task, err)
	}
}

func (s *Service) briefRoutes() {
	// POST /api/v1/later/{id}/handled: the Handled button of a question in a `brief` item's later list. It leaves the queue; a
	// question already handled, dropped (its to-do ended, or 7 days) or unknown is 409. A queue that can't be read or
	// written is 503, and the question stays.
	s.handle("POST /api/v1/later/{id}/handled", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change the queue"))
			return
		}
		ok, err := s.laterHandled(r.Context(), r.PathValue("id"), time.Now())
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		if !ok {
			apierr.Write(w, http.StatusConflict, i18n.Err("assistant-feishu_cards.already_handled"))
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})

	// POST /api/v1/todos/{id}/accept {"asked_at"}: the Accept button of a `brief` item. It verifies the task when asked_at is
	// the round that waits now; a button of an earlier round, or a task already verified, is 409. A decision whose
	// recommendation could not be sent to the task's agent is 502, and asks that cannot be read or an acceptance that
	// cannot be saved is 503; either way nothing was taken (conclusion.go, errAcceptStore).
	s.handle("POST /api/v1/todos/{id}/accept", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change todos"))
			return
		}
		var in struct {
			AskedAt string `json:"asked_at"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.AskedAt == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("asked_at is required"))
			return
		}
		ok, err := s.acceptTryFromBrief(r.Context(), r.PathValue("id"), in.AskedAt)
		if errors.Is(err, errAcceptStore) { // nothing accepted: the button works again once the store answers
			apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		if err != nil { // a decision's recommendation that did not reach the agent: nothing taken, the button works again
			// In the request's language, the reason too when the hub words it (an offline computer, no agent).
			apierr.Write(w, http.StatusBadGateway, i18n.Err("assistant-reports.accept_not_taken",
				i18n.Args{"error": clip(oneLine(i18n.Localize(err, s.requestCtxLang(r.Context()))), 120)}))
			return
		}
		if !ok {
			apierr.Write(w, http.StatusConflict, i18n.Err("assistant-feishu_cards.already_handled"))
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}
