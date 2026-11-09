package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// An agent's question goes to the assistant before the user (manager.json agents.delegate, off by default;
// docs/assistant/assistant.md "The assistant answers questions first"): the card shows on the apps at once, marked delegating, but nothing is pushed.
// The assistant answers what is the agent's own call (a technical option, a default, whether to run again, which
// branch) the way interaction_answer does, with why (delegate.go decides); it leaves red lines (irreversible, or seen
// by other people) and the goal itself to the user, with the context it filled in. When it does not decide in time,
// the user is asked as before: a question never waits on it. Permissions, plans, terminal dialogs and forms are never
// delegated.

// delegable: ix is a question the assistant decides on first. When it is, ix is noted as being answered by the
// assistant at once (claimDelegate), so the same question of the same agent coming while it decides goes to the user;
// forget takes the note back when no answer goes. forget is nil when ix is not delegable.
func (s *Service) delegable(ix proto.Interaction) (forget func()) {
	if ix.Kind != proto.IxQuestion || len(ix.Questions) == 0 || len(ix.Fields) > 0 || ix.ToolName == "ask_user" || !asksForInput(ix) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !s.hello(ctx).Agents.Delegate.On() {
		return nil
	}
	forget, ok := s.claimDelegate(ctx, ix)
	if !ok {
		s.workEvent(ix.MachineID, ix.SessionID, "delegate_repeat", map[string]any{"questions": len(ix.Questions)})
		return nil
	}
	return forget
}

// asksForInput: ix really asks something. An agent's own question (AskUserQuestion, a Codex request) waits for input
// by itself. A card the manager made from a stopped turn's text (workCardTool) asks only when one of its questions
// does: it has a question mark, or options of its own to pick from. A turn that ended on a statement ("waiting for the
// runner and the release") gets the card with only "OK, do as you said" / "Not now" on it: answering that for the user only
// sends the agent its own statement back, and it says the same again.
func asksForInput(ix proto.Interaction) bool {
	if ix.ToolName != workCardTool {
		return true
	}
	for _, q := range ix.Questions {
		if strings.ContainsAny(q.Question, "?？") {
			return true
		}
		own := 0
		for _, o := range q.Options {
			if _, canned := answerKeys[canonicalAnswer(o.Label)]; !canned {
				own++
			}
		}
		if own >= 2 {
			return true
		}
	}
	return false
}

// delegatedAsk is a question the assistant answered (or is deciding on) for the user: its text as delegateText has it,
// when, and the interaction it was (empty for one read back from the events).
type delegatedAsk struct {
	text string
	at   time.Time
	id   string
}

// delegateRepeatFor is how long an answer the assistant gave keeps it from answering the same question of the same
// agent again.
const delegateRepeatFor = 24 * time.Hour

// delegateText is what ix asks, for telling the same question again apart: its questions' words, without spaces,
// punctuation or case.
func delegateText(ix proto.Interaction) string {
	var b strings.Builder
	for _, q := range ix.Questions {
		for _, r := range strings.ToLower(q.Question) {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// sameAsk: a and b (delegateText's) ask the same thing, word for word or nearly (an agent saying its line again
// rewords a little: "wait for the runner + release", "wait for the runner and release").
func sameAsk(a, b string) bool {
	if a == b {
		return true
	}
	pairs := func(s string) map[[2]rune]int {
		r := []rune(s)
		out := map[[2]rune]int{}
		for i := 0; i+1 < len(r); i++ {
			out[[2]rune{r[i], r[i+1]}]++
		}
		return out
	}
	pa, pb := pairs(a), pairs(b)
	na, nb, both := 0, 0, 0
	for k, n := range pa {
		na += n
		both += min(n, pb[k])
	}
	for _, n := range pb {
		nb += n
	}
	return na+nb > 0 && float64(2*both)/float64(na+nb) >= 0.8
}

// claimDelegate: unless the assistant answered (or is deciding on) the same question of ix's agent within
// delegateRepeatFor, notes ix as the assistant's, in the same lock as the check, so two of the same question never both
// go to it. It does not answer one again: the agent asking once more means the answer did not settle it, and the user
// should see it. forget takes the note back.
func (s *Service) claimDelegate(ctx context.Context, ix proto.Interaction) (forget func(), ok bool) {
	s.loadDelegated(ctx)
	text := delegateText(ix)
	if text == "" {
		return func() {}, true
	}
	k := [2]string{ix.MachineID, ix.SessionID}
	d := delegatedAsk{text: text, at: time.Now(), id: ix.ID}
	cutoff := d.at.Add(-delegateRepeatFor)
	s.work.mu.Lock()
	defer s.work.mu.Unlock()
	for _, o := range s.work.delegated[k] {
		if o.at.After(cutoff) && sameAsk(o.text, text) {
			return nil, false
		}
	}
	s.addDelegatedLocked(k, d)
	return func() {
		s.work.mu.Lock()
		defer s.work.mu.Unlock()
		list := s.work.delegated[k]
		for i, o := range list {
			if o == d {
				s.work.delegated[k] = append(list[:i:i], list[i+1:]...)
				return
			}
		}
	}, true
}

// rememberDelegated keeps the answer the assistant gave for ix (claimDelegate's) in the events, so a restart keeps it.
func (s *Service) rememberDelegated(ix proto.Interaction) {
	if text := delegateText(ix); text != "" {
		s.workEvent(ix.MachineID, ix.SessionID, "delegated", map[string]any{"text": text})
	}
}

func (s *Service) addDelegated(k [2]string, d delegatedAsk) {
	s.work.mu.Lock()
	defer s.work.mu.Unlock()
	s.addDelegatedLocked(k, d)
}

// addDelegatedLocked is addDelegated with s.work.mu held.
func (s *Service) addDelegatedLocked(k [2]string, d delegatedAsk) {
	cutoff := time.Now().Add(-delegateRepeatFor)
	if s.work.delegated == nil {
		s.work.delegated = map[[2]string][]delegatedAsk{}
	}
	kept := []delegatedAsk{d}
	for _, o := range s.work.delegated[k] {
		if o.at.After(cutoff) {
			kept = append(kept, o)
		}
	}
	s.work.delegated[k] = kept
}

// loadDelegated reads the answers the assistant gave within delegateRepeatFor back from the events, once.
func (s *Service) loadDelegated(ctx context.Context) {
	s.work.mu.Lock()
	loaded := s.work.delegatedLoaded
	s.work.mu.Unlock()
	st := s.workDB()
	if loaded || st == nil {
		return
	}
	events, err := st.WorkEvents(ctx, time.Now().Add(-delegateRepeatFor))
	if err != nil {
		s.logf("delegate: read back the answers given: %v", err)
		return // tried again next time
	}
	s.work.mu.Lock()
	if s.work.delegatedLoaded {
		s.work.mu.Unlock()
		return
	}
	s.work.delegatedLoaded = true
	s.work.mu.Unlock()
	for _, e := range events {
		if text, _ := e.Detail["text"].(string); e.Kind == "delegated" && text != "" {
			s.addDelegated([2]string{e.MachineID, e.SessionID}, delegatedAsk{text: text, at: e.At})
		}
	}
}

// delegate shows ix as delegating and has the assistant decide on it, in the background; forget is delegable's.
func (s *Service) delegate(ix proto.Interaction, forget func()) {
	s.fleet.mu.Lock()
	if p := s.fleet.interactions[ix.ID]; p != nil {
		p.ix.Delegating = true
	}
	s.fleet.mu.Unlock()
	shown := ix
	shown.Delegating = true
	s.display(shown)
	go s.delegateNow(ix, forget)
}

// delegateNow waits for the assistant's call on ix (agents.delegate's timeout at most), then answers it or tells the
// user. A call that comes too late is dropped. forget takes back delegable's note unless the answer went.
func (s *Service) delegateNow(ix proto.Interaction, forget func()) {
	answered := false
	defer func() {
		if !answered {
			forget()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	timeout := s.hello(ctx).Agents.Delegate.Timeout()
	cancel()
	ctx, cancel = context.WithTimeout(context.Background(), timeout)
	defer cancel()
	type decided struct {
		res DelegateResult
		err error
	}
	got := make(chan decided, 1)
	go func() {
		res, err := s.delegateCall(ctx, DelegateParams{Interaction: s.withTask(ix)})
		got <- decided{res, err}
	}()
	var d decided
	select {
	case d = <-got:
	case <-ctx.Done():
		d.err = ctx.Err()
	}
	switch {
	case d.err != nil:
		s.logf("delegate %s: %v; asking the user", ix.ID, d.err)
	case d.res.Answer:
		// Noted since delegable: the agent has the answer at once and may ask the same again before this goes on.
		ok, err := s.answerQuestion(ix.MachineID, ix.SessionID, ix.ID, d.res.Answers, d.res.Reason)
		if ok {
			answered = true
			s.rememberDelegated(ix)
		}
		if ok || err == nil {
			return // answered, or no longer pending (the user answered it meanwhile)
		}
		s.logf("delegate %s: answer: %v; asking the user", ix.ID, err)
	default:
		ix.Delegate = d.res.Ask
	}
	s.escalate(ix)
}

// delegateCall is Delegate, or what a test has in its place.
func (s *Service) delegateCall(ctx context.Context, p DelegateParams) (DelegateResult, error) {
	if f := s.delegateHook; f != nil {
		return f(ctx, p)
	}
	return s.Delegate(ctx, p)
}

// escalate tells the user of ix, still pending, with the assistant's context when it gave some.
func (s *Service) escalate(ix proto.Interaction) {
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	still := p != nil && !p.claimed
	if still {
		p.ix.Delegating, p.ix.Delegate = false, ix.Delegate
	}
	s.fleet.mu.Unlock()
	if still {
		s.tell(ix)
	}
}

// ---- "Change answer": the user changes an answer the assistant gave ----

// interactionRecorder is the store's one interaction with its outcome (db.DB).
type interactionRecorder interface {
	InteractionRecord(ctx context.Context, id string) (*db.InteractionRecord, error)
}

var (
	errNotDelegated = errors.New("only a question the assistant answered can be answered again")
	errNoAnswers    = errors.New("answers is required: an object of question → answer")
)

// serveReanswer is POST /api/v1/interactions/{id}/reanswer {answers}: a question the assistant answered for the user
// gets the user's answer instead. The agent went on with the first one already, so the new one goes to it as a
// message; the user's answer is kept as the task's (its record, its decisions).
func (s *Service) serveReanswer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Answers map[string]string `json:"answers"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	status, err := s.reanswer(r.Context(), r.PathValue("id"), body.Answers)
	if err != nil {
		apierr.Write(w, status, err)
		return
	}
	apierr.WriteJSON(w, map[string]any{"ok": true})
}

// reanswer sends the user's answers to a question the assistant answered (id) to its agent, once; the HTTP status
// goes with an error.
func (s *Service) reanswer(ctx context.Context, id string, answers map[string]string) (int, error) {
	st, ok := s.store().(interactionRecorder)
	if !ok {
		return http.StatusServiceUnavailable, errors.New("no interaction history")
	}
	rec, err := st.InteractionRecord(ctx, id)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if rec == nil || rec.Status != "resolved" || rec.Resolution == nil || rec.Resolution.AnsweredBy != proto.AnsweredByAssistant {
		return http.StatusConflict, errNotDelegated
	}
	ix := rec.Interaction
	var items []prompts.AgentReanswerItem
	kept := map[string]string{}
	for _, q := range ix.Questions {
		a := strings.TrimSpace(answers[q.Question])
		if a == "" {
			continue
		}
		kept[q.Question] = a
		items = append(items, prompts.AgentReanswerItem{Question: q.Question, Before: rec.Resolution.Answers[q.Question], Answer: a})
	}
	if len(items) == 0 {
		return http.StatusBadRequest, errNoAnswers
	}
	text := prompts.RenderAgentReanswer(prompts.AgentReanswer{Items: items})
	sum := sha256.Sum256([]byte(text))
	key := id + "_" + hex.EncodeToString(sum[:6])
	s.fleet.mu.Lock()
	sent := s.fleet.reanswered[key]
	s.fleet.mu.Unlock()
	if sent {
		return 0, nil // the same answer again: it went already
	}
	if err := s.sendToSession(ctx, ix.MachineID, ix.SessionID, text); err != nil {
		return http.StatusBadGateway, err
	}
	s.fleet.mu.Lock()
	if s.fleet.reanswered == nil {
		s.fleet.reanswered = map[string]bool{}
	}
	s.fleet.reanswered[key] = true
	s.fleet.mu.Unlock()
	res := proto.Resolution{Answers: kept}
	s.event(ctx, map[string]any{"type": "interaction_reanswered", "id": id, "resolution": res})
	go s.InteractionResolved(context.WithoutCancel(ctx), InteractionResolved{Interaction: ix, Resolution: res, Status: "resolved"})
	return 0, nil
}
