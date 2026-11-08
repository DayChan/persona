package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- turns ----

// retireTodoWork withdraws the manager's pending follow-ups for the agents of a todo that just ended (completed or a
// failed start): the scheduled wake — one postponed while the machine was offline included — and the open work card
// are cleared, so neither can wake the finished agent nor let a card answer back into the finished turn. The turn's
// own work state is kept, as workStarted keeps it: with no wake and no card the tick never acts on the entry, and
// triage never redoes a turn it already triaged.
func (s *Service) retireTodoWork(keys ...[2]string) {
	now := time.Now().UTC()
	for _, k := range keys {
		s.work.mu.Lock()
		s.work.init()
		e, ok := s.work.entries[k]
		if !ok || (e.WakeAt == nil && e.InteractionID == "") {
			s.work.mu.Unlock()
			continue
		}
		if e.InteractionID != "" {
			delete(s.work.cards, e.InteractionID)
		}
		e.WakeAt, e.Work.WakeAt, e.InteractionID, e.Work.InteractionID = nil, nil, "", ""
		e.UpdatedAt = now
		saved := *e
		s.work.mu.Unlock()
		s.saveWork(saved)
		s.publishSoon()
	}
}

// workStarted: a session began working (again). Whatever the manager was going to do for its last turn is off; a card
// for it is withdrawn. How long the turn waited is recorded.
func (s *Service) workStarted(ctx context.Context, machine, sid string, now time.Time) {
	k := [2]string{machine, sid}
	s.work.mu.Lock()
	s.work.init()
	e, ok := s.work.entries[k]
	if !ok || e.Work.Phase == proto.WorkWorking {
		s.work.mu.Unlock()
		return
	}
	by := "user"
	if now.Sub(s.work.sent[k]) < 2*time.Minute {
		by = "hub"
	}
	if by == "user" {
		delete(s.work.approved, k) // the user took over: their earlier yes is not for what comes next
	}
	card := e.InteractionID
	prev := *e
	if card != "" {
		delete(s.work.cards, card)
	}
	changed := e.WakeAt != nil || card != ""
	e.WakeAt, e.Work.WakeAt, e.InteractionID, e.Work.InteractionID = nil, nil, "", ""
	e.UpdatedAt = now.UTC()
	saved := *e
	s.work.mu.Unlock()
	if card != "" {
		s.workEvent(machine, sid, "withdrawn", map[string]any{"by": by})
	}
	if prev.Work.Phase != "" {
		s.workEvent(machine, sid, "resumed", map[string]any{"after": now.Sub(prev.TurnAt).Seconds(), "phase": prev.Work.Phase,
			"on": prev.Work.On, "by": by})
	}
	s.saveWork(saved)
	if changed {
		s.publishSoon()
	}
}

// workSeen: the user looked at a session. The first look after a turn ended is how long it waited for them.
func (s *Service) workSeen(machine, sid string, at time.Time) {
	k := [2]string{machine, sid}
	s.work.mu.Lock()
	s.work.init()
	e, ok := s.work.entries[k]
	if !ok || e.Work.Phase == "" || !s.work.seen[k].Before(e.TurnAt) {
		s.work.mu.Unlock()
		return
	}
	s.work.seen[k] = at
	phase, on, turn := e.Work.Phase, e.Work.On, e.TurnAt
	s.work.mu.Unlock()
	s.workEvent(machine, sid, "seen", map[string]any{"after": at.Sub(turn).Seconds(), "phase": phase, "on": on})
}

// workTurnEnded: a session on machine stopped. Once it stays stopped a moment, its turn is triaged; agents waiting
// for this one are woken.
func (s *Service) workTurnEnded(ctx context.Context, machine, sid string) {
	if !s.workOn(ctx) || !s.loadWork(ctx) {
		return
	}
	s.wakeWaitersOn(machine, sid)
	time.Sleep(settleOf(s.triageCfg(ctx)))
	v := s.newWorkView(ctx)
	c, ok := v.session(machine, sid)
	if !ok || c.Status == proto.StatusWorking {
		return
	}
	s.triageTurn(ctx, v, machine, c)
}

// triageTurn works out where c stands after its turn and acts on it. Once per turn.
func (s *Service) triageTurn(ctx context.Context, v *workView, machine string, c proto.Session) {
	if c.Assistant || c.Status == proto.StatusOffline || c.Status == proto.StatusWorking || c.Starting {
		return
	}
	k := [2]string{machine, c.SessionID}
	s.work.mu.Lock()
	s.work.init()
	prev, had := s.work.entries[k]
	s.work.tried[k] = triedTurn{c.UpdatedAt, time.Now()}
	if had && sameTurn(c, prev.TurnAt) && (prev.Work.Phase != "" || prev.Work.By != "") || s.work.triaging[k] {
		s.work.mu.Unlock()
		return
	}
	s.work.triaging[k] = true
	var last db.SessionWork
	if had {
		last = *prev
	}
	s.work.mu.Unlock()
	defer func() {
		s.work.mu.Lock()
		delete(s.work.triaging, k)
		s.work.mu.Unlock()
	}()

	start := time.Now()
	var same bool // the model says it still waits on the step the user approved
	w, by, skip := triageRules(c, s.location(ctx), s.lang(ctx))
	if skip {
		return // the agent asks itself (or its terminal does): the list shows that
	}
	if w == nil {
		mctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		var err error
		w, same, err = s.triageTurnModel(mctx, v, machine, c)
		if err != nil {
			s.logf("triage %s/%s: %v", machine, short(c.SessionID), err)
			s.workEvent(machine, c.SessionID, "failed", map[string]any{"error": clip(err.Error(), 200)})
			return
		}
		by = "model"
	}
	s.applyApproval(k, w, same)
	w.TurnAt, w.At, w.By = c.UpdatedAt, time.Now().UTC(), by
	e := db.SessionWork{MachineID: machine, SessionID: c.SessionID, TurnAt: c.UpdatedAt, Work: *w}
	// The same wait goes on (the manager woke it, it still waits): its counts carry over.
	if had && last.Work.Phase == proto.WorkBlocked && w.Phase == proto.WorkBlocked && last.Work.On == w.On {
		e.Retries, e.Wakes = last.Retries, last.Wakes
	}
	if now, ok := s.session(ctx, machine, c.SessionID); ok && !sameTurn(now, e.TurnAt) {
		return // it went on while the model read it: the next end decides
	}
	s.actOn(ctx, v, machine, c, &e)
	s.putWork(ctx, e)
	s.logf("triage %s/%s: %s%s (%s %.1fs): %s", machine, short(c.SessionID), w.Phase, orEmpty("/", e.Work.On), by,
		time.Since(start).Seconds(), e.Work.Line)
	s.workEvent(machine, c.SessionID, "triaged", map[string]any{"phase": w.Phase, "on": e.Work.On, "by": by,
		"line": e.Work.Line, "ms": time.Since(start).Milliseconds()})
}

// applyApproval: a turn still waiting on the step the user already said yes to (same: the model says so, and the
// words agree) is a wait the manager ends: no second card for it. A turn that is no such wait ends the approval, so
// what is left in s.work.approved always held for the latest triaged turn.
func (s *Service) applyApproval(k [2]string, w *proto.WorkState, same bool) {
	s.work.mu.Lock()
	defer s.work.mu.Unlock()
	a, ok := s.work.approved[k]
	if !ok {
		return
	}
	if !same || !a.covers(w) || len(w.Questions) > 1 { // a second question is the user's all the same
		delete(s.work.approved, k)
		return
	}
	if w.On == proto.WaitUser && risky(w.Then) {
		w.On, w.Questions = proto.WaitExternal, nil // only the approved step made it the user's
	}
}

func orEmpty(sep, s string) string {
	if s == "" {
		return ""
	}
	return sep + s
}

// triageRules settles what needs no model. skip: the agent waits for the user on its own card or dialog.
// What it says is in l (the user's language).
func triageRules(c proto.Session, loc *time.Location, l i18n.Lang) (w *proto.WorkState, by string, skip bool) {
	if c.PendingInteractions > 0 || c.Status == proto.StatusBlocked {
		return nil, "", true
	}
	if e := c.TurnError; e != "" {
		if turnErrorTransient(c) {
			return &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitTransient, Line: workT(l, "transient_retrying"),
				Wait: clip(oneLine(e), 200)}, "rule", false
		}
		return &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser, Line: clip(oneLine(workT(l, "model_error", i18n.Args{"error": e})), 60),
			Questions: []proto.Question{{Question: workT(l, "model_error_ask", i18n.Args{"error": clip(oneLine(e), 300)}), Header: workT(l, "header_model"),
				Options: []proto.QuestionOption{{Label: answerLabel(l, answerHandled)}, {Label: answerLabel(l, answerSkip)}}}}}, "rule", false
	}
	if n := runningInBackground(c); n > 0 {
		return &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, Line: workT(l, "background_running", i18n.Args{"n": n})},
			"rule", false
	}
	if c.Loop != nil {
		line := workT(l, "loop_running")
		if !c.Loop.NextAt.IsZero() {
			line = workT(l, "loop_running_next", i18n.Args{"time": c.Loop.NextAt.In(loc).Format("15:04")})
		}
		return &proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, Line: line}, "rule", false
	}
	return nil, "", false
}

func runningInBackground(c proto.Session) int {
	n := len(c.Background)
	for _, a := range c.Subagents {
		if a.Running {
			n++
		}
	}
	return n
}

// turnErrorTransient: c's turn error is worth a "Continue". The agent's record says so when it gives a kind (Codex's error
// kinds, #2185); otherwise the text does.
func turnErrorTransient(c proto.Session) bool {
	switch c.TurnErrorKind {
	case proto.TurnErrorTransient:
		return true
	case proto.TurnErrorUser:
		return false
	}
	return transientError(c.TurnError)
}

// transientError: a model API error worth a "Continue" (the service failed for a moment), not one the user must fix
// (sign-in, key, the model picked).
func transientError(text string) bool {
	t := strings.ToLower(text)
	for _, w := range []string{"failed to authenticate", "oauth token has expired", "invalid api key", "not logged in", "issue with the selected model",
		" 401", " 403", "permission_error", "authentication_error", "credit balance", "billing",
		"ssl certificate", "self-signed certificate", "certificate verif"} {
		if strings.Contains(t, w) {
			return false
		}
	}
	for _, w := range []string{"overloaded", " 500", " 502", " 503", " 504", " 529", " 429", "rate limit", "rate_limit",
		"connection", "timed out", "timeout", "socket hang up", "econnreset", "fetch failed", "no eligible upstream",
		"internal server error", "service unavailable", "bad gateway", "network", "api_error", "upstream",
		"can't reach the api server", "temporarily unavailable", "no response from api", "unable to connect to api",
		"enotfound", "eai_again", "failedtoopensocket"} {
		if strings.Contains(t, w) {
			return true
		}
	}
	return false
}

// riskyWords mark a next step the manager must not set off itself (docs/assistant/agent-work.md "Safety boundary"): it is only ever a
// card.
var riskyWords = []string{"push", "deploy", "部署", "发布", "上线", "merge", "合并", "合入", "发消息", "回复", "评论", "testflight",
	"release", "推送", "推到", "comment", "发版", "提审", "send"}

func risky(texts ...string) bool {
	for _, t := range texts {
		if len(riskyWordsIn(t)) > 0 {
			return true
		}
	}
	return false
}

// riskyWordsIn are the risky words in t (lower case).
func riskyWordsIn(t string) []string {
	t = strings.ToLower(t)
	var out []string
	for _, w := range riskyWords {
		if strings.Contains(t, w) {
			out = append(out, w)
		}
	}
	return out
}
