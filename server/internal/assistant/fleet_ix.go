package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The interactions (docs/protocol/protocol.md "Interactions"): the cards the user answers on the phone — a hook's permission, question
// or plan (a node's interaction.create, waited for until it is answered, expires or its node goes; a resumable one
// outlives its node for its hook to claim again), a TUI dialog read off the screen (a "screen" card the manager
// answers itself with keys), a Codex question or approval the node lists, the assistant's own questions and cards,
// and what the hub's own features ask (fleet.ask). They are kept here, recorded in the interactions table, told the
// hub (fleet.interaction / fleet.interaction_resolved), pushed (fleet_push.go), and answered through the hub's
// relayed route (fleet_routes.go).

type pending struct {
	ix   proto.Interaction
	done chan proto.Resolution
	// resolved broadcasts a claimed answer to every overlapping resumable waiter.
	resolved       chan struct{}
	resolution     *proto.Resolution
	persisting     *questionPersistence
	persistenceErr error
	// local is set for interactions the manager resolves itself (screen dialogs, Codex cards): it acts on the answer,
	// and an error goes back to whoever answered (prompt_changed: nothing was pressed).
	local func(proto.Resolution) error
	// orphaned is set while no hook waits on a resumable interaction (its node went away, or the manager restarted):
	// it ends the interaction unless the hook claims it again in time.
	orphaned *time.Timer
	// checkedAt / checking bound transcript reads for a plan answered outside Persona.
	checkedAt time.Time
	checking  bool
	// answeredBy is who resolved it (Resolution.AnsweredBy), so the hub can say the assistant answered, with why
	// (Resolution.Reason) and its answers then (a question's: never a secret).
	answeredBy, answeredReason string
	answeredWith               map[string]string
	// codex: a card a node listed for a Codex TUI; it lasts as long as the node lists it.
	codex bool
	// claimed: an answer was taken for this hook-waited card (resolve). It stays in the map until await or
	// finishInteraction ends it, but a second answer finds it no longer pending.
	claimed bool
	// graceEnded: its grace period ran out (orphanLocked) and it is being ended: a hook back now hands over.
	graceEnded bool
	// expired: it ended because its time ran out, unanswered (await); finished() reads it.
	expired bool
	// announced: a screen card that rang (announce), carried through a detach (screenGone.announced), so the card
	// read again once the node is back rings at the window's end only if its predecessor never did (#4979).
	announced bool
}

// resumeGrace is how long a resumable interaction outlives its node: a little longer than hooks keep reconnecting
// (nodeclient.ResumeWait).
var resumeGrace = 3 * time.Minute

// ixMapLocked is the pending interactions, made on first use. Callers hold s.fleet.mu.
func (s *Service) ixMapLocked() map[string]*pending {
	if s.fleet.interactions == nil {
		s.fleet.interactions = map[string]*pending{}
	}
	return s.fleet.interactions
}

// hasHooks: Persona's hooks run in the session — it sent a hook event since the manager started, or Persona started
// it (with its hooks) — so they ask for most dialogs themselves and a screen card waits for them a little.
func (s *Service) hasHooks(machine string, sess proto.Session) bool {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	return s.fleet.hooked[[2]string{machine, sess.SessionID}] || sess.StartedByPersona
}

// hasElicitationClient: an app that takes MCP elicitations is connected (the hub says, fleet.clients).
func (s *Service) hasElicitationClient() bool {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	return s.fleet.clients.Elicitation
}

// ---- telling ----

// announce puts a new card on the apps (through the hub), pushes it, and gives it to the channels. A question the
// assistant may answer for the user goes to it first (fleet_delegate.go).
func (s *Service) announce(ix proto.Interaction) {
	if forget := s.delegable(ix); forget != nil {
		s.delegate(ix, forget)
		return
	}
	s.tell(ix)
}

// tell is announce once nothing is in the way: the card on the apps, the push, the channels, or the main conversation
// for a quiet task.
func (s *Service) tell(ix proto.Interaction) {
	ix = s.withTask(ix)
	s.hub.Tell(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: ix})
	s.publishSessions(ix.MachineID)
	if s.quietSession(ix.MachineID, ix.SessionID) {
		// A quiet task's question is no push, bot card or channel message: the main conversation is told (#1731).
		go s.InteractionRaised(context.Background(), InteractionRaised{Interaction: ix})
		return
	}
	if !s.muted(ix.MachineID, ix.SessionID) {
		s.notifyInteraction(ix, s.machineName(ix.MachineID))
		go s.threadAgentAsks(ix) // and the topic of the todo this agent works on, if it has one
	}
	s.InteractionAnnounced(context.Background(), InteractionAnnounced{Interaction: ix}) // the Feishu bot, the channels
}

// show puts a card on every app without a push (one they were told about already).
func (s *Service) show(ix proto.Interaction) {
	s.hub.Tell(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: s.withTask(ix)})
	s.publishSessions(ix.MachineID)
}

// ---- waiting ----

// nodeInteraction is a node's interaction.create: allowed right away when "Always allow in this session" already covers it, else
// shown and waited for.
func (s *Service) nodeInteraction(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	if reservedCard(ix) {
		return proto.Resolution{}, errReservedCard
	}
	// Defense behind the node hook: question-text-keyed answers could not be kept apart on a duplicate card.
	if ix.Kind == proto.IxQuestion && proto.HasDuplicateQuestionText(ix.Questions) {
		return proto.Resolution{}, i18n.Err("hub-hub.duplicate_question")
	}
	if r, ok := s.alwaysAllowed(ctx, ix); ok {
		if ix.Resumable { // its hook's earlier attempt left the card pending: it is answered too
			_ = s.resolveRemembered(ix.ID, r, false)
		}
		return r, nil
	}
	return s.waitInteraction(ctx, ix)
}

// waitInteraction registers a hook interaction and blocks until it is resolved, cancelled or expires.
// A resumable one asked again with the same id (its hook reconnected after a manager or node restart) is the same
// interaction: the wait resumes on it, or returns the answer given meanwhile. One the history has as still pending
// (the manager restarted with it on the phone) is shown again without a second push.
func (s *Service) waitInteraction(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	ix = cleanTitle(ix)
	if ix.ExpiresAt.IsZero() {
		ix.ExpiresAt = time.Now().Add(s.notifyTimings().hookTimeout()).UTC()
	}
	again := false
	if ix.Resumable {
		s.fleet.mu.Lock()
		if p := s.fleet.interactions[ix.ID]; p != nil && p.local == nil {
			if p.graceEnded {
				// The grace period ended and the card is being ended outside the lock (the history, the apps): the
				// hook hands over, as one back just after would (#5043's kind of gap).
				s.fleet.mu.Unlock()
				return proto.Resolution{Dismiss: true}, nil
			}
			if p.orphaned != nil {
				p.orphaned.Stop() // fired but not decided yet: it finds p.orphaned nil and leaves the card to the hook
				p.orphaned = nil
			}
			s.fleet.mu.Unlock()
			if ix.Kind == proto.IxElicitation && !s.hasElicitationClient() {
				_ = s.resolve(ix.ID, proto.Resolution{Dismiss: true})
			}
			return s.await(ctx, p)
		}
		s.fleet.mu.Unlock()
		status, r, ended := s.outcome(ix.ID)
		if ended {
			return r, nil
		}
		again = status == "pending"
	}
	if ix.Kind == proto.IxElicitation && !s.hasElicitationClient() {
		return proto.Resolution{Dismiss: true}, nil
	}
	p := &pending{ix: ix, done: make(chan proto.Resolution, 1)}
	s.fleet.mu.Lock()
	// A screen interaction for the same session was a premature guess; the hook knows better.
	// Tell the app now: the wait below can last until the hook times out.
	var dropped []*pending
	m := s.ixMapLocked()
	for id, old := range m {
		if old.ix.Kind == proto.IxScreen && old.ix.MachineID == ix.MachineID && old.ix.SessionID == ix.SessionID {
			delete(m, id)
			dropped = append(dropped, old)
		}
	}
	m[ix.ID] = p
	s.fleet.mu.Unlock()
	for _, old := range dropped { // as a Codex card withdraws a screen card
		s.recordEnd(old.ix.ID, "cancelled", nil)
		s.finished(old)
	}
	if again {
		s.show(ix)
	} else {
		s.record(ix)
		s.announce(ix)
	}
	return s.await(ctx, p)
}

// await blocks on a registered interaction. When ctx ends — the node's connection is gone — a resumable
// interaction stays pending for its hook to claim again.
func (s *Service) await(ctx context.Context, p *pending) (proto.Resolution, error) {
	s.fleet.mu.Lock()
	if p.resolved == nil {
		p.resolved = make(chan struct{})
	}
	resolved := p.resolved
	s.fleet.mu.Unlock()
	id := p.ix.ID
	timer := time.NewTimer(time.Until(p.ix.ExpiresAt))
	defer timer.Stop()
	status := "cancelled"
	select {
	case <-resolved:
		s.finishInteraction(id)
		return *p.resolution, nil
	case r := <-p.done:
		s.finishInteraction(id)
		return r, nil
	case <-timer.C:
		status = "expired"
	case <-ctx.Done():
	}
	// Decide under the same lock as resolve: an answer already claimed must be
	// persisted before any waiter can record a competing timeout or disconnect.
	s.fleet.mu.Lock()
	if p.persisting != nil {
		attempt := p.persisting
		s.fleet.mu.Unlock()
		<-attempt.done
		if attempt.err != nil {
			return proto.Resolution{}, attempt.err
		}
		<-resolved
		s.finishInteraction(id)
		return *p.resolution, nil
	}
	if p.persistenceErr != nil {
		err := p.persistenceErr
		s.fleet.mu.Unlock()
		return proto.Resolution{}, err
	}
	if p.claimed {
		hasAnswer := p.resolution != nil
		s.fleet.mu.Unlock()
		if hasAnswer {
			<-resolved
			s.finishInteraction(id)
			return *p.resolution, nil
		}
		return proto.Resolution{Dismiss: true}, nil
	}
	if s.fleet.interactions[id] != p {
		s.fleet.mu.Unlock()
		return proto.Resolution{Dismiss: true}, nil
	}
	if status == "cancelled" && p.ix.Resumable && s.orphanLocked(p) {
		s.fleet.mu.Unlock()
		return proto.Resolution{Dismiss: true}, nil // disconnected hook; a new one can resume
	}
	p.claimed, p.graceEnded = true, true
	p.expired = status == "expired"
	s.fleet.mu.Unlock()
	s.recordEnd(id, status, nil)
	s.finishInteraction(id)
	return proto.Resolution{Dismiss: true}, nil
}

// orphanLocked starts p's grace period: it ends (cancelled) unless its hook claims it again first. Callers hold
// s.fleet.mu.
func (s *Service) orphanLocked(p *pending) bool {
	if p.orphaned != nil {
		return true
	}
	wait := min(resumeGrace, time.Until(p.ix.ExpiresAt))
	if wait <= 0 {
		return false
	}
	p.orphaned = time.AfterFunc(wait, func() {
		s.fleet.mu.Lock()
		// Claimed under the lock that decides it, as resolve claims an answer: an answer being written now is not
		// cancelled over, and one given while this end is written finds the card no longer pending.
		still := s.fleet.interactions[p.ix.ID] == p && p.orphaned != nil && !p.claimed
		if still {
			p.claimed, p.graceEnded = true, true
		}
		s.fleet.mu.Unlock()
		if still {
			s.recordEnd(p.ix.ID, "cancelled", nil)
			s.finishInteraction(p.ix.ID)
		}
	})
	return true
}

// outcome reports how a resumable interaction no longer pending here ended: the answer given before its hook could
// read it (the manager or node restarted right then), or a dismiss when it was cancelled or expired. status is the
// history's ("pending": recorded, never ended).
func (s *Service) outcome(id string) (status string, r proto.Resolution, ended bool) {
	st := s.fleetStore()
	if st == nil {
		return "", proto.Resolution{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
	defer cancel()
	status, res, err := st.InteractionOutcome(ctx, id)
	switch {
	case err != nil || status == "" || status == "pending":
		return status, proto.Resolution{}, false
	case res != nil && (status == "resolved" || status == "cancelled" && res.Cancel):
		return status, *res, true
	default:
		return status, proto.Resolution{Dismiss: true}, true
	}
}

// historyTimeout bounds each interaction history read or write. They run before the apps see a new card and before
// a hook hears its answer, so a slow or locked database must not hold either back for longer than this.
var historyTimeout = 5 * time.Second

// record / recordEnd keep the interaction history in the database (best effort, within historyTimeout).
func (s *Service) record(ix proto.Interaction) {
	st := s.fleetStore()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
	defer cancel()
	if err := st.InsertInteraction(ctx, ix); err != nil {
		s.logf("record interaction: %v", err)
	}
}

func (s *Service) recordEnd(id, status string, r *proto.Resolution) {
	st := s.fleetStore()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
	defer cancel()
	if err := st.FinishInteraction(ctx, id, status, r); err != nil {
		s.logf("record interaction outcome: %v", err)
	}
}

// errNotPending: the interaction was answered, withdrawn or expired already.
var errNotPending = errors.New("no longer pending")

// answeredJustNow is errNotPending for a card another answer claimed a moment ago. That answer reaches the history
// only after the claim (recordEnd), so whoever asks in between is told it from the claim (#1230).
type answeredJustNow struct {
	id     string
	status string
	kept   proto.Resolution // as the history keeps it
}

func (e *answeredJustNow) Error() string {
	return fmt.Sprintf("interaction %s is %v", e.id, errNotPending)
}
func (e *answeredJustNow) Unwrap() error { return errNotPending }

// endStatus is how an answer ends its card in the history.
func endStatus(r proto.Resolution) string {
	switch {
	case r.Cancel:
		return "cancelled"
	case r.Dismiss:
		return "dismissed"
	}
	return "resolved"
}

var errInvalidElicitation = errors.New("elicitation requires action accept, decline or cancel")

// resolve answers an interaction. A screen card acts right away and its error comes back (the dialog changed:
// nothing pressed, a new card follows).
func (s *Service) resolve(id string, r proto.Resolution) error {
	return s.resolveRemembered(id, r, true)
}

// Automatically replayed grants must not write themselves back after a concurrent revocation.
func (s *Service) resolveRemembered(id string, r proto.Resolution, remember bool) error {
	s.fleet.mu.Lock()
	p := s.fleet.interactions[id]
	if p != nil && r.Cancel && (!p.ix.CanCancel || p.ix.Kind != proto.IxQuestion || !r.IsQuestionCancel()) {
		s.fleet.mu.Unlock()
		return errInvalidQuestionCancel
	}
	if p != nil && p.ix.Kind == proto.IxElicitation && !r.Dismiss &&
		r.Action != "accept" && r.Action != "decline" && r.Action != "cancel" {
		s.fleet.mu.Unlock()
		return errInvalidElicitation
	}
	if p != nil && r.Allow != nil && *r.Allow {
		for _, f := range p.ix.Fields {
			if !f.Optional && strings.TrimSpace(r.Values[f.Key]) == "" {
				s.fleet.mu.Unlock()
				return i18n.Err("hub-hub.fill_in", i18n.Args{"field": f.Label})
			}
		}
	}
	if p != nil {
		r.Values = declaredValues(p.ix, r.Values)
	}
	if p != nil && p.persisting != nil {
		attempt := p.persisting
		s.fleet.mu.Unlock()
		<-attempt.done
		if attempt.err != nil {
			return attempt.err
		}
		return fmt.Errorf("interaction %s is %w", id, errNotPending)
	}
	var first *answeredJustNow
	if p != nil && p.claimed {
		if p.resolution != nil {
			first = &answeredJustNow{id: id, status: endStatus(*p.resolution), kept: redacted(p.ix, *p.resolution)}
		}
		p = nil // answered a moment ago: the first answer is the one the hook and the history get
	}
	if p != nil && p.local != nil {
		delete(s.fleet.interactions, id) // claimed: a second answer to the same card is not pending any more
		if p.ix.Kind == proto.IxScreen && !r.Dismiss {
			// Marked before the keys go: the node's next list may come while they are on the way (#4391).
			if s.fleet.screenAnswered == nil {
				s.fleet.screenAnswered = map[string]answeredScreen{}
			}
			s.fleet.screenAnswered[id] = answeredScreen{machine: p.ix.MachineID, session: p.ix.SessionID,
				until: time.Now().Add(screenAnsweredQuiet)}
		} else if p.ix.Kind == proto.IxScreen {
			// Handed back to the computer: no key goes, the dialog stays, and the next list must not ask it again.
			if s.fleet.screenHandedBack == nil {
				s.fleet.screenHandedBack = map[string]screenGone{}
			}
			s.fleet.screenHandedBack[id] = screenGone{machine: p.ix.MachineID, session: p.ix.SessionID, at: time.Now()}
		}
	} else if p != nil {
		p.claimed = true
		p.resolution = &r
		if durableQuestion(p.ix) {
			p.persisting = &questionPersistence{done: make(chan struct{})}
			p.persistenceErr = nil
		}
		if p.resolved == nil {
			p.resolved = make(chan struct{})
		}
	}
	if p != nil {
		p.answeredBy, p.answeredReason = r.AnsweredBy, r.Reason // read by finished(), under this lock
		if r.AnsweredBy != "" {
			p.answeredWith = r.Answers
		}
	}
	s.fleet.mu.Unlock()
	if first != nil {
		return first
	}
	if p == nil {
		return fmt.Errorf("interaction %s is %w", id, errNotPending)
	}
	status := endStatus(r)
	// Everyone told of the answer (the channels, the task feeds) gets its secret fields masked, as the history keeps them;
	// only the one waiting on the card (p.local, p.done) gets what was typed into them.
	told := redacted(p.ix, r)
	if p.local != nil {
		err := p.local(r)
		if err != nil {
			status = "failed"
			s.fleet.mu.Lock()
			back := s.fleet.interactions[id] == p
			s.fleet.mu.Unlock()
			if back { // a Codex card the node could not answer is open again (codexPending): it is not over
				go s.InteractionResolved(context.Background(), InteractionResolved{Interaction: p.ix, Resolution: told, Status: status})
				return err
			}
		} else {
			s.channelsEnded(p.ix, &told)
		}
		// What was typed into a terminal may be a password: the history keeps that something was, not what.
		kept := told
		if kept.Text != "" {
			kept.Text = redactedValue
		}
		s.recordEnd(id, status, &kept)
		s.finished(p)
		go s.InteractionResolved(context.Background(), InteractionResolved{Interaction: p.ix, Resolution: told, Status: status})
		return err
	}
	if durableQuestion(p.ix) {
		if err := s.persistQuestion(p, status, told); err != nil {
			return err
		}
	} else {
		s.recordEnd(id, status, &told)
	}
	s.channelsEnded(p.ix, &told)
	go s.InteractionResolved(context.Background(), InteractionResolved{Interaction: p.ix, Resolution: told, Status: status})
	if remember {
		s.keepAlways(p.ix, r) // kept before the hook hears: its next card may follow at once
	}
	close(p.resolved)
	select {
	case p.done <- r:
	default:
	}
	s.finishInteraction(id)
	return nil
}

// channelsEnded tells the channels a card is over: r is how it was answered (nil: withdrawn, expired, or over after
// the answer that was told already).
func (s *Service) channelsEnded(ix proto.Interaction, r *proto.Resolution) {
	if f := s.fleetEnded; f != nil {
		f(ix, r)
	}
	s.InteractionEnded(context.Background(), InteractionEnded{Interaction: ix, Resolution: r})
}

// redactedValue stands for what was typed into a secret field (or a terminal) wherever the answer is kept or told.
const redactedValue = "<redacted>"

// redacted is the resolution as the history keeps it: secret fields say they were filled in, not what with. A secret
// card's typed text is masked here too; a terminal's other text reaches the channels as typed and only the history masks
// it (resolveRemembered's kept).
func redacted(ix proto.Interaction, r proto.Resolution) proto.Resolution {
	if ix.Secret && r.Text != "" {
		r.Text = redactedValue
	}
	if len(r.Values) > 0 {
		secret := map[string]bool{}
		for _, f := range ix.Fields {
			secret[f.Key] = f.Secret
		}
		vals := make(map[string]string, len(r.Values))
		for k, v := range r.Values {
			if secret[k] || secretName.MatchString(k) {
				v = redactedValue
			}
			vals[k] = v
		}
		r.Values = vals
	}
	if len(r.Content) > 0 {
		var schema struct {
			Properties map[string]struct {
				Format    string `json:"format"`
				WriteOnly bool   `json:"writeOnly"`
			} `json:"properties"`
		}
		if ix.Elicitation != nil {
			_ = json.Unmarshal(ix.Elicitation.RequestedSchema, &schema)
		}
		content := make(map[string]any, len(r.Content))
		for k, v := range r.Content {
			p := schema.Properties[k]
			if p.Format == "password" || p.WriteOnly || secretName.MatchString(k) {
				v = redactedValue
			}
			content[k] = v
		}
		r.Content = content
	}
	return r
}

// declaredValues are the values of the fields ix declares; any other key is dropped as the answer comes in: nothing
// reads one, and the history and everyone told of the answer would get it as typed.
func declaredValues(ix proto.Interaction, values map[string]string) map[string]string {
	if len(values) == 0 {
		return values
	}
	var out map[string]string
	for _, f := range ix.Fields {
		if v, ok := values[f.Key]; ok {
			if out == nil {
				out = map[string]string{}
			}
			out[f.Key] = v
		}
	}
	return out
}

// hasSecretField: ix asks for something the history redacts (a password).
func hasSecretField(ix proto.Interaction) bool {
	for _, f := range ix.Fields {
		if f.Secret {
			return true
		}
	}
	return false
}

func (s *Service) finishInteraction(id string) {
	s.fleet.mu.Lock()
	p, ok := s.fleet.interactions[id]
	delete(s.fleet.interactions, id)
	if ok && p.orphaned != nil {
		p.orphaned.Stop()
	}
	s.fleet.mu.Unlock()
	if ok {
		s.finished(p)
	}
}

// finished tells everyone an interaction, already out of the pending set, is over.
func (s *Service) finished(p *pending) {
	s.fleet.mu.Lock()
	done := assistantwire.FleetResolved{ID: p.ix.ID, AnsweredBy: p.answeredBy}
	if done.AnsweredBy != "" {
		done.Reason, done.Answers = p.answeredReason, p.answeredWith
	}
	var how *proto.Resolution
	if p.expired && p.ix.ToolName == "ask_user" { // the assistant's question becomes a to-do (ask_todo.go): its card says so
		how = &proto.Resolution{Dismiss: true, Reason: proto.DismissedExpired}
	}
	s.fleet.mu.Unlock()
	s.channelsEnded(p.ix, how)
	// the app marks a question the assistant answered, and why (docs/assistant/assistant.md)
	s.hub.Tell(assistantwire.EventFleetResolved, done)
	s.publishSessions(p.ix.MachineID)
}

// pendingInteractions are the pending interactions, oldest first, with the task each agent works for.
func (s *Service) pendingInteractions() []proto.Interaction {
	s.fleet.mu.Lock()
	out := []proto.Interaction{}
	for _, p := range s.fleet.interactions {
		out = append(out, p.ix)
	}
	s.fleet.mu.Unlock()
	for i := range out {
		out[i] = s.withTask(out[i])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// pendingIx is one pending interaction; false when it is not pending.
func (s *Service) pendingIx(id string) (proto.Interaction, bool) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if p := s.fleet.interactions[id]; p != nil {
		return p.ix, true
	}
	return proto.Interaction{}, false
}

func (s *Service) expireLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.retrySeen(ctx) // a database that failed when the manager started
		s.fleet.mu.Lock()
		var plans []string
		seen := map[string]bool{}
		for _, p := range s.fleet.interactions {
			if terminalReconciled(p) && !seen[p.ix.MachineID] {
				plans = append(plans, p.ix.MachineID)
				seen[p.ix.MachineID] = true
			}
		}
		s.fleet.mu.Unlock()
		for _, m := range plans {
			s.syncPlanInteractions(m) // also converge after a lost rev/status event
		}
		s.fleet.mu.Lock()
		var ids []string
		for id, p := range s.fleet.interactions {
			if p.local != nil && time.Now().After(p.ix.ExpiresAt) {
				ids = append(ids, id)
			}
		}
		s.fleet.mu.Unlock()
		for _, id := range ids {
			s.finishInteraction(id)
		}
	}
}

// errReservedCard: a card from a node (a hook, a runtime, a daemon, a Codex list) or an extension may not pass for the
// password card: only the assistant's secret_run tool makes one (secret_run.go), and the clients show it as that card,
// with no Agent's name on it.
var errReservedCard = errors.New("the tool name " + proto.SecretRunTool + " is reserved for the assistant's password card")

// reservedCard: ix calls itself the password card.
func reservedCard(ix proto.Interaction) bool { return ix.ToolName == proto.SecretRunTool }

// askHub is fleet.ask: a card one of the hub's own features asks (a mail send, Cua's apps, persona_settings),
// waited for like a hook's.
func (s *Service) askHub(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	if reservedCard(ix) {
		return proto.Resolution{}, errReservedCard
	}
	if ix.ID == "" {
		ix.ID = "ix_" + randomHex(6)
	}
	if ix.CreatedAt.IsZero() {
		ix.CreatedAt = time.Now().UTC()
	}
	if m, ok := s.fleetMachine(ix.MachineID); ok {
		ix.MachineID = m.ID
	}
	return s.waitInteraction(ctx, ix)
}

// ---- "Always allow in this session" (#2202, docs/assistant/assistant.md "Always allow in this session") ----

// The node names what a card grants (proto.Interaction.AlwaysKeys, only on assistant conversations). The manager
// remembers explicit Always answers and rechecks every later card against the stored keys, including after a
// process restart. Nodes must not cache session grants that would bypass a subsequent revocation (#5867).

// maxAlwaysKeys bounds what one card may name (a file change's paths, a gated command's calls).
const maxAlwaysKeys = 32

func alwaysCard(ix proto.Interaction) bool {
	return ix.Kind == proto.IxPermission && ix.SessionID != "" && len(ix.AlwaysKeys) > 0 &&
		len(ix.AlwaysKeys) <= maxAlwaysKeys && len(ix.Fields) == 0
}

// alwaysAllowed: every key ix names was granted with "Always allow in this session" in its conversation.
func (s *Service) alwaysAllowed(ctx context.Context, ix proto.Interaction) (proto.Resolution, bool) {
	st := s.fleetStore()
	if st == nil || !alwaysCard(ix) {
		return proto.Resolution{}, false
	}
	ok, err := st.AlwaysAllowed(ctx, ix.SessionID, ix.AlwaysKeys)
	if err != nil {
		s.logf("[%s] 本会话总是允许: %v", ix.MachineID, err)
		return proto.Resolution{}, false
	}
	if !ok {
		return proto.Resolution{}, false
	}
	s.logf("[%s] %s allowed: 本会话总是允许 in this conversation", ix.MachineID, ix.ToolName)
	yes := true
	return proto.Resolution{Allow: &yes, Always: true, Remembered: true}, true
}

// keepAlways keeps what a card answered with "Always allow in this session" grants, then answers the conversation's other cards
// waiting meanwhile that it covers now (parallel tool calls ask at once). It runs before the hook hears the answer,
// so each database call is bounded by historyTimeout: a grant that cannot be kept is only logged.
func (s *Service) keepAlways(ix proto.Interaction, r proto.Resolution) {
	st := s.fleetStore()
	if st == nil || !alwaysCard(ix) || r.Allow == nil || !*r.Allow || !r.Always {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
	err := st.AllowAlways(ctx, ix.SessionID, ix.ToolName, ix.AlwaysKeys)
	cancel()
	if err != nil {
		s.logf("[%s] keep 本会话总是允许: %v", ix.MachineID, err)
		return
	}
	s.fleet.mu.Lock()
	var waiting []proto.Interaction
	for _, p := range s.fleet.interactions {
		if p.ix.ID != ix.ID && p.ix.SessionID == ix.SessionID && p.ix.MachineID == ix.MachineID &&
			p.local == nil && !p.codex && !p.claimed && alwaysCard(p.ix) {
			waiting = append(waiting, p.ix)
		}
	}
	s.fleet.mu.Unlock()
	for _, w := range waiting {
		ctx, cancel := context.WithTimeout(context.Background(), historyTimeout)
		res, ok := s.alwaysAllowed(ctx, w)
		cancel()
		if ok {
			_ = s.resolveRemembered(w.ID, res, false)
		}
	}
}

// ---- a node gone, the manager restarted ----

// NodeDetached: a machine's node connection ended (devices.Options.Detached): the cards waiting on it that cannot be
// claimed again end (resumable ones wait for their hook through the next connection), its list shows it.
func (s *Service) NodeDetached(machine string) {
	if !s.fleetOn() {
		return
	}
	s.cancelMachineInteractions(machine)
	s.sessionsChanged(machine)
}

func (s *Service) cancelMachineInteractions(machineID string) {
	s.fleet.mu.Lock()
	var ids []string
	now := time.Now()
	for id, p := range s.fleet.interactions {
		// Resumable ones wait for their hook to come back through the next node (await).
		if p.ix.MachineID == machineID && !p.ix.Resumable {
			ids = append(ids, id)
			if p.ix.Kind == proto.IxScreen { // its dialog may still be there when the node is back (screenBackLocked)
				if s.fleet.screenDetached == nil {
					s.fleet.screenDetached = map[string]screenGone{}
				}
				s.fleet.screenDetached[id] = screenGone{machine: machineID, session: p.ix.SessionID, at: now, announced: p.announced}
			}
		}
	}
	s.fleet.mu.Unlock()
	s.feishuDetach(ids)
	for _, id := range ids {
		s.finishInteraction(id)
	}
}

// restoreInteractions takes back, as the manager starts, the resumable cards the last one left pending: their hooks
// reconnect through the next node and claim them again (waitInteraction), without a second push; the others it left
// are over (cancelled in the history).
func (s *Service) restoreInteractions(ctx context.Context) {
	st, ok := s.store().(interface {
		ExpireStaleInteractions(ctx context.Context) ([]proto.Interaction, error)
	})
	if !ok {
		return
	}
	resumable, err := func() (out []proto.Interaction, err error) {
		defer guard(&err) // a test's store may not keep the table
		return st.ExpireStaleInteractions(ctx)
	}()
	if err != nil {
		s.logf("expire stale interactions: %v", err)
		return
	}
	s.fleet.mu.Lock()
	m := s.ixMapLocked()
	for _, ix := range resumable {
		if m[ix.ID] == nil {
			m[ix.ID] = &pending{ix: ix, done: make(chan proto.Resolution, 1)}
			s.orphanLocked(m[ix.ID])
		}
	}
	s.fleet.mu.Unlock()
}
