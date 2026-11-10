package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The ask a to-do waits on, on the to-do itself (#2056, docs/assistant/todos.md "Waiting for the owner"): the apps list
// the to-dos, not the briefing, and a to-do whose conclusion needs the owner's decision is where they show the
// recommendation and take it with one tap (POST /api/v1/todos/{id}/accept, briefing_feed.go). It is the briefing's
// `accept[]` row (briefing_feed.go), read from the same book (awaitingAcceptance): a "Please verify" ask, or a supervised
// task's conclusion (conclusion.go). A decision that recommends an option is also pushed with that option, and the
// push's OK takes it the same way (pushDecision).

// todoAsk is a to-do's open ask, as GET /api/v1/todos carries it: the verdict line in the words of its Lark card; outcome a
// conclusion's verdict (done, cannot or decide; none for a deployed task's "Please verify"); recommend a decision's
// recommended option, which OK takes; asked_at the round, which the accept route takes.
type todoAsk struct {
	Outcome   string    `json:"outcome,omitempty"`
	Verdict   string    `json:"verdict"`
	Recommend string    `json:"recommend,omitempty"`
	AskedAt   time.Time `json:"asked_at"`
}

// listedTodo is a to-do as GET /api/v1/todos lists it: with its ask while one waits (none: no `ask`, which a client that
// does not know it never reads).
type listedTodo struct {
	Todo
	Ask *todoAsk `json:"ask,omitempty"`
}

// todoAsks are the open asks by to-do id, in the request's language: the ones the briefing lists (not passed, not ended,
// not from before the to-do's latest reopen).
func (s *Service) todoAsks(ctx context.Context) map[string]todoAsk {
	l := s.botLang(ctx)
	out := map[string]todoAsk{}
	for _, w := range s.awaitingAcceptance(ctx) {
		a := todoAsk{Outcome: w.ask.Outcome, Verdict: verdictText(l, &w.ask), AskedAt: w.ask.AskedAt}
		if decideAsk(&w.ask) {
			a.Recommend = w.ask.Recommend
		}
		out[w.todo] = a
	}
	return out
}

// askChanged: a to-do's ask came up, was taken or ended: the apps read the to-dos again ({"type":"todos"}) and the board
// follows.
func (s *Service) askChanged(ctx context.Context) {
	s.event(ctx, map[string]any{"type": "todos"})
	s.boardChanged()
}

// pushDecision pushes task id's decision that waits for the owner and recommends an option (a "decide" push): what is to
// be decided and the option, in the words of its card, and what the notification's OK needs to take it (the to-do, the
// round). True when the hub took it. None for a decision without a recommendation (nothing for OK to take), for a task
// from a health conversation (the lock screen says nothing of those) or a muted one.
func (s *Service) pushDecision(ctx context.Context, id string) bool {
	a, ok := s.askCopy(id)
	t, found := s.todoNow(id)
	if !ok || !found || !decideAsk(&a) || a.Recommend == "" || !a.AcceptedAt.IsZero() || t.Origin == "" {
		return false
	}
	here := s.settings(ctx).MachineID
	if s.muted(here, t.Origin) || s.inHealth(ctx, t.Origin) {
		return false
	}
	l := s.lang(ctx)
	n := Notification{ID: "ntf_" + randomHex(8), Kind: assistantwire.NotifyDecide, Subtitle: "Persona", DeviceID: here, SessionID: t.Origin,
		Title: i18n.T(l, "assistant-reports.verdict_title", i18n.Args{"task": clip(oneLine(t.Title), 30)}),
		Body:  verdictText(l, &a) + "\n" + recommendText(l, &a),
		Todo:  &assistantwire.NotifyTodo{ID: id, AskedAt: a.AskedAt, Recommend: a.Recommend}}
	if d := deadlineText(l, s.location(ctx), a.Deadline, a.Reversible, time.Now()); d != "" {
		n.Body += "\n" + d
	}
	if err := s.hub.NotifyRequest(ctx, NotifyRequest{Notification: n}); err != nil {
		s.logf("task %s: decision push: %v", id, err)
		return false
	}
	return true
}
