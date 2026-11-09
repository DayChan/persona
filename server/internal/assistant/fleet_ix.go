package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
	"github.com/nwverse/persona/server/internal/transcript"
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

// ixMap is the pending interactions, made on first use. Callers hold s.fleet.mu.
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

// ---- screen cards ----

// syncScreenInteractions turns blocked agents without a hook-driven interaction into "screen" interactions
// (folder trust, model pickers, any TUI dialog hooks cannot see), and clears them once the agent moves on.
func (s *Service) syncScreenInteractions(machine string, list []proto.Session) {
	blocked := map[string]proto.Session{}
	for _, sess := range list {
		// A session without a terminal (a source whose capabilities leave it out: Codex app-server, an agent runtime)
		// has no screen to read a dialog off: it asks through its own cards, and blocked there means it waits in its
		// own UI (docs/integrations/runtimes.md).
		if sess.Status == proto.StatusBlocked && (sess.SourceKind == "" || sess.Capabilities != nil && sess.Capabilities.Terminal) {
			blocked[sess.SessionID] = sess
		}
	}
	s.fleet.mu.Lock()
	for k, a := range s.fleet.screenAnswered {
		if _, still := blocked[a.session]; a.machine == machine && !still || time.Now().After(a.until) {
			delete(s.fleet.screenAnswered, k)
		}
	}
	for k, g := range s.fleet.screenHandedBack {
		if _, still := blocked[g.session]; g.machine == machine && !still || time.Since(g.at) > screenHandedBackFor {
			delete(s.fleet.screenHandedBack, k)
		}
	}
	now := time.Now()
	for id, g := range s.fleet.screenDetached {
		// Too old, or the agent moved on while its node was away (anything but blocked, or unknown while the node is
		// gone): the same dialog asked later is a new question (screenBackLocked).
		if now.Sub(g.at) > resumeGrace || g.machine == machine && slices.ContainsFunc(list, func(sess proto.Session) bool {
			return sess.SessionID == g.session && sess.Status != proto.StatusBlocked && sess.Status != proto.StatusUnknown
		}) {
			delete(s.fleet.screenDetached, id)
		}
	}
	var stale []string
	hasHook := map[string]bool{}
	for id, p := range s.fleet.interactions {
		if p.ix.MachineID != machine {
			continue
		}
		if p.ix.Kind == proto.IxScreen {
			if _, still := blocked[p.ix.SessionID]; !still {
				stale = append(stale, id)
			}
			hasHook[p.ix.SessionID] = true
		} else if coversScreen(p) {
			hasHook[p.ix.SessionID] = true
		}
	}
	s.fleet.mu.Unlock()
	for _, id := range stale {
		s.recordEnd(id, "cancelled", nil) // answered at the computer, or gone: not pending in the history either
		s.finishInteraction(id)
	}
	for sid, sess := range blocked {
		if hasHook[sid] {
			continue
		}
		go s.createScreenInteraction(machine, sess)
	}
}

func (s *Service) createScreenInteraction(machine string, sess proto.Session) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Hook dialogs flip the agent to blocked a moment before the hook reaches us; give them a head start. A Codex
	// node reads its approvals off the screen once a second: a little longer. A Claude Code without Persona's hooks
	// (the user's own profile) asks only on the screen: its card is made at once (#751).
	if wait := screenCardWait(sess, s.hasHooks(machine, sess)); wait > 0 {
		time.Sleep(wait)
	}
	if cur, ok := s.nodeSession(machine, sess.SessionID); !ok || cur.Status != proto.StatusBlocked {
		return
	}
	text, d, err := s.screenDialog(ctx, machine, sess.SessionID)
	if err != nil {
		return
	}
	id := "scr_" + machine + "_" + sess.SessionID
	if d != nil {
		id += "_" + d.Rev // another dialog is another card: nothing picked on the last one carries over
	}
	ix := proto.Interaction{
		ID: id, MachineID: machine, SessionID: sess.SessionID, SessionTitle: sess.Title, Cwd: sess.Cwd, Kind: proto.IxScreen,
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(24 * time.Hour).UTC(), Screen: trimScreen(text), Dialog: d,
		Secret: screen.SecretPrompt(text),
	}
	s.fleet.mu.Lock()
	if s.screenHandedBackLocked(machine, sess.SessionID, id, d != nil) {
		s.fleet.mu.Unlock()
		return
	}
	s.screenMovedOnLocked(machine, sess.SessionID, id)
	if left, first := s.screenJustAnsweredLocked(id, time.Now()); left > 0 {
		// The dialog just answered, still on screen: no second card, no second push. Still there after the quiet
		// time, the answer did not take, and it is asked again.
		s.fleet.mu.Unlock()
		if first {
			go func() {
				time.Sleep(left)
				if cur, ok := s.nodeSession(machine, sess.SessionID); ok && cur.Status == proto.StatusBlocked {
					s.createScreenInteraction(machine, cur)
				}
			}()
		}
		return
	}
	asking := false
	for _, p := range s.fleet.interactions {
		if p.ix.MachineID == machine && p.ix.SessionID == sess.SessionID {
			if p.ix.Kind == proto.IxScreen || coversScreen(p) {
				s.fleet.mu.Unlock()
				return
			}
			asking = true
		}
	}
	if asking && d == nil { // only the async question's input box on screen: its card says it all
		s.fleet.mu.Unlock()
		return
	}
	back, rang := s.screenBackLocked(machine, sess.SessionID, id, d != nil, time.Now())
	p := &pending{ix: ix, announced: rang || !back && d != nil, local: func(r proto.Resolution) error {
		if r.Dismiss {
			return nil
		}
		params, err := screenAnswer(sess.SessionID, d, r)
		if err != nil {
			s.forgetScreenAnswer(id) // nothing pressed: the dialog is the same question still
			return err
		}
		kctx, kcancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer kcancel()
		err = s.nodeCall(kctx, machine, "session.keys", params, nil)
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == screen.PromptChanged {
			// Both revisions and what the node read, from a node that says so (#5538).
			s.logf("[%s] screen card %s: prompt_changed, the node read %s", machine, id, re.Data)
			s.forgetScreenAnswer(id)                    // nothing pressed
			go s.createScreenInteraction(machine, sess) // the card for what is on screen now
		} else if err == nil {
			go s.recheckScreen(machine, sess.SessionID, ix.ID)
		}
		return err
	}}
	s.ixMapLocked()[id] = p
	s.fleet.mu.Unlock()
	s.record(ix)
	if back || d == nil {
		s.show(ix) // back: pushed before the node's connection ended (#4252); no dialog: the real card rings later (#4450)
		if back {
			go s.feishuCardBack(ix)
		}
	} else {
		s.announce(ix)
	}
	if d == nil {
		go s.upgradeScreenCard(machine, sess.SessionID, id, p)
	}
}

// screenGone is a screen card its node's connection ended while it was open (cancelMachineInteractions).
type screenGone struct {
	machine, session string
	at               time.Time
	announced        bool // the card had rung (pending.announced)
}

// screenBackLocked: card id is a screen card whose node's connection ended within resumeGrace, read again off the same
// dialog now that the node is back. Its push went out already, so it comes back without ringing the phone a second
// time (#4252), as a Codex card a reconnected node lists again does; the mark is used up. A card read before the
// dialog is drawn (no dialog: a half-drawn screen right after the reconnect) is that card too while the session has a
// mark, which it leaves for the card upgradeScreenCard makes once the dialog shows. announced is whether the card
// matched had rung: a placeholder (#4450) detached inside its window had not, and still rings once (#4979). Callers
// hold s.fleet.mu.
func (s *Service) screenBackLocked(machine, session, id string, dialog bool, now time.Time) (back, announced bool) {
	for k, g := range s.fleet.screenDetached {
		if now.Sub(g.at) > resumeGrace {
			delete(s.fleet.screenDetached, k)
		}
	}
	if g, ok := s.fleet.screenDetached[id]; ok {
		delete(s.fleet.screenDetached, id)
		return true, g.announced
	}
	if dialog {
		return false, false
	}
	for _, g := range s.fleet.screenDetached {
		if g.machine == machine && g.session == session {
			return true, g.announced
		}
	}
	return false, false
}

// screenUpgradeEvery: how often, and screenUpgradeFor how long, a screen card made without a dialog it could read
// looks at the screen again (upgradeScreenCard; fleetBook.upgradeEvery / upgradeFor override them).
const screenUpgradeEvery, screenUpgradeFor = 500 * time.Millisecond, 30 * time.Second

// upgradeScreenCard: the agent may be called blocked before it draws its dialog (Orca does so from its PreToolUse
// hook; on Windows the hooks take seconds before AskUserQuestion's dialog shows), so the card made then has no dialog
// to answer. Look at the screen again; once a dialog shows, its card replaces this one. The loop is pinned to its own
// card generation (p), so after blocked -> working -> blocked a placeholder reusing this id belongs to a newer card.
// Failed reads cost one more poll; the last poll past the window rings the unreadable card once, unless it rang
// already (p.announced: a reconnect-back card whose predecessor rang before the detach, #4252/#4979).
func (s *Service) upgradeScreenCard(machine, session, id string, p *pending) {
	every, span := cmp.Or(s.fleet.upgradeEvery, screenUpgradeEvery), cmp.Or(s.fleet.upgradeFor, screenUpgradeFor)
	end := time.Now().Add(span)
	for {
		time.Sleep(every)
		cur, ok := s.nodeSession(machine, session)
		if !ok || cur.Status != proto.StatusBlocked {
			return
		}
		s.fleet.mu.Lock()
		still := s.fleet.interactions[id] == p
		s.fleet.mu.Unlock()
		if !still {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, d, err := s.screenDialog(ctx, machine, session)
		cancel()
		if err == nil && d != nil {
			s.fleet.mu.Lock()
			same := s.fleet.interactions[id] == p
			if same {
				delete(s.fleet.interactions, id)
				if p.orphaned != nil {
					p.orphaned.Stop()
				}
			}
			s.fleet.mu.Unlock()
			if same {
				s.finished(p)
				s.createScreenInteraction(machine, cur)
			}
			return
		}
		if !time.Now().Before(end) {
			// Window over and unreadable: re-pin (the read took time), then ring once unless it rang already.
			s.fleet.mu.Lock()
			ring := s.fleet.interactions[id] == p && !p.announced
			p.announced = true
			s.fleet.mu.Unlock()
			if ring {
				s.announce(p.ix)
			}
			return
		}
	}
}

// screenRecheck: how often, and screenRecheckFor how long, the screen is looked at again after a screen card's
// answer (recheckScreen).
var screenRecheck, screenRecheckFor = 250 * time.Millisecond, 2 * time.Second

// screenAnsweredQuiet: how long after a screen card's answer the same dialog read off the screen is taken for the
// answered one (its keys not landed yet, herdr still calling the agent blocked), not a new question (#4391).
var screenAnsweredQuiet = 5 * time.Second

// answeredScreen is a screen card answered lately: until when, and whether a look after the quiet time is scheduled.
// A new question that reads exactly like the answered one (the same prompt asked twice, herdr never reporting the
// agent working in between) waits out the quiet time too: shown late, never lost.
type answeredScreen struct {
	machine, session string
	until            time.Time // the end of its quiet time (screenAnsweredQuiet, taken when it was answered)
	retry            bool
}

// screenHandedBackFor: how long a screen card handed back to the computer is remembered at most (its card's own
// lifetime); a session that stays blocked longer, or a machine that never lists again, does not keep it forever.
const screenHandedBackFor = 24 * time.Hour

// screenHandedBackLocked: card id is the dialog its user handed back to the computer ({"dismiss":true}: "Hand back to computer"),
// still on screen: no new card, no push, until the agent is no longer blocked (syncScreenInteractions) or something
// else shows. A card handed back before its dialog could be read (no dialog in its id) is the first dialog drawn
// after it, as upgradeScreenCard takes it for the same card. Callers hold s.fleet.mu.
func (s *Service) screenHandedBackLocked(machine, session, id string, dialog bool) bool {
	bare := "scr_" + machine + "_" + session
	for k, g := range s.fleet.screenHandedBack {
		if g.machine != machine || g.session != session {
			continue
		}
		delete(s.fleet.screenHandedBack, k)
		switch {
		case k == id:
		case k == bare && dialog: // the half-drawn card handed back: this dialog is it
			g.at = time.Now()
		default: // another question
			continue
		}
		s.fleet.screenHandedBack[id] = g
		return true
	}
	return false
}

// screenMovedOnLocked: machine's session shows something other than screen card keep (another dialog, none, or it
// is no longer blocked): a dialog answered there and read again later is a new question. Callers hold s.fleet.mu.
func (s *Service) screenMovedOnLocked(machine, session, keep string) {
	for k, a := range s.fleet.screenAnswered {
		if a.machine == machine && a.session == session && k != keep {
			delete(s.fleet.screenAnswered, k)
		}
	}
}

// forgetScreenAnswer: screen card id's answer pressed nothing (refused before any key): no quiet time for it. Any
// other error keeps it: the keys may have gone, and the look after the quiet time asks again if they did not.
func (s *Service) forgetScreenAnswer(id string) {
	s.fleet.mu.Lock()
	delete(s.fleet.screenAnswered, id)
	s.fleet.mu.Unlock()
}

// screenJustAnsweredLocked: card id was answered within screenAnsweredQuiet; left is how much of it remains, and
// first whether this call is the one to look again after it. Callers hold s.fleet.mu.
func (s *Service) screenJustAnsweredLocked(id string, now time.Time) (left time.Duration, first bool) {
	for k, a := range s.fleet.screenAnswered {
		if !now.Before(a.until) {
			delete(s.fleet.screenAnswered, k)
		}
	}
	a, ok := s.fleet.screenAnswered[id]
	if !ok {
		return 0, false
	}
	first = !a.retry
	a.retry = true
	s.fleet.screenAnswered[id] = a
	return a.until.Sub(now), first
}

// recheckScreen: after a screen card's answer the agent may ask again at once (the next question of an
// AskUserQuestion, the next permission) while herdr keeps calling it blocked, so no status change makes that card.
// Look again until a dialog that is not the answered one shows (its card is made), the agent works again (a new
// question then comes with its own change to blocked), or another card is up (#751).
func (s *Service) recheckScreen(machine, session, answered string) {
	for end := time.Now().Add(screenRecheckFor); time.Now().Before(end); {
		time.Sleep(screenRecheck)
		cur, ok := s.nodeSession(machine, session)
		if !ok || cur.Status != proto.StatusBlocked {
			return
		}
		s.fleet.mu.Lock()
		asked := false
		for _, p := range s.fleet.interactions {
			asked = asked || p.ix.MachineID == machine && p.ix.SessionID == session
		}
		s.fleet.mu.Unlock()
		if asked {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, d, err := s.screenDialog(ctx, machine, session)
		cancel()
		if err != nil {
			return
		}
		// No dialog (the screen between two) or the answered one (the keys not landed yet): look again.
		if d != nil && "scr_"+machine+"_"+session+"_"+d.Rev != answered {
			s.createScreenInteraction(machine, cur)
			return
		}
	}
}

// screenDialog reads a session's screen as a screen card shows it, and the dialog on it. The dialog's revision is the
// one the node will check the answer against: the node's own (dialog_rev), so a hub and a node of different builds,
// whose parsing may differ, still agree (#5538). A node from before it sends none and checks the old formula
// (LegacyRev).
func (s *Service) screenDialog(ctx context.Context, machine, session string) (string, *screen.Dialog, error) {
	var scr struct {
		Text      string `json:"text"`
		DialogRev string `json:"dialog_rev"`
	}
	if err := s.nodeCall(ctx, machine, "session.screen", map[string]any{"session_id": session, "lines": screen.DialogLines}, &scr); err != nil {
		return "", nil, err
	}
	d := screen.ParseDialog(scr.Text)
	if d != nil {
		d.Rev = cmp.Or(scr.DialogRev, d.LegacyRev)
	}
	return scr.Text, d, nil
}

// screenCardWait is how long a blocked session's screen card waits for a card its agent asks for itself.
func screenCardWait(sess proto.Session, hooks bool) time.Duration {
	switch {
	case sess.Agent == "codex" || sess.Agent == "traex":
		return 3 * time.Second
	case hooks:
		return 1500 * time.Millisecond
	}
	return 0
}

// coversScreen: the interaction stands for what the terminal waits on (a hook's, a Codex approval or overlay
// question), so no screen card is made beside it. A Codex async question does not: Codex shows no dialog for it,
// though herdr calls the agent blocked while it is open.
func coversScreen(p *pending) bool { return !(p.codex && p.ix.ToolName == "request_user_input_async") }

// screenAnswer turns an answer to a screen card into session.keys parameters. With a dialog the node checks its
// revision before pressing anything; an option's keys come from the node's fresh reading of the screen, and the
// ones worked out here are for nodes that predate that.
func screenAnswer(session string, d *screen.Dialog, r proto.Resolution) (map[string]any, error) {
	params := map[string]any{"session_id": session, "keys": r.Keys, "text": r.Text, "pre_keys": r.PreKeys}
	if d == nil {
		return params, nil
	}
	params["rev"] = d.Rev
	if picked := d.Option(r.Option); d.Multi && !(len(r.Options) == 0 && picked != nil && picked.Kind == screen.KindChat) {
		// Checked and sent by the node one key at a time (node/dialogmulti.go); "Chat about this" is pressed as it is.
		// An app from before multi-select cards names one option: that one alone (the free-text field: the text alone).
		options := r.Options
		if len(options) == 0 && r.Option != "" {
			if o := d.Option(r.Option); o != nil && o.Kind == "" {
				options = []string{o.Key}
			}
		}
		for _, k := range options {
			if o := d.Option(k); o == nil || o.Kind != "" {
				return nil, fmt.Errorf("the dialog has no option %q to check", k)
			}
		}
		if len(options) == 0 && strings.TrimSpace(r.Text) == "" {
			return nil, i18n.Err("hub-hub.pick_or_answer")
		}
		return map[string]any{"session_id": session, "rev": d.Rev, "multi": true, "options": options, "free_text": r.Text}, nil
	}
	if r.Option == "" {
		return params, nil
	}
	o := d.Option(r.Option)
	if o == nil {
		return nil, fmt.Errorf("the dialog has no option %q", r.Option)
	}
	params["option"] = o.Key
	if o.Kind == screen.KindTypeSomething {
		params["pre_keys"], params["keys"] = o.Keys, []string{"enter"}
	} else {
		params["keys"], params["text"], params["pre_keys"] = o.Keys, "", nil
	}
	return params, nil
}

func trimScreen(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n "), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return strings.Join(lines, "\n")
}

// cleanTitle is ix with its conversation's title as the apps, pushes and channels show it: a node may name it by a
// first message that starts with injected blocks (an older one clipped inside one). Nothing left: the project.
func cleanTitle(ix proto.Interaction) proto.Interaction {
	if ix.SessionTitle == "" {
		return ix
	}
	if ix.SessionTitle = transcript.Title(ix.SessionTitle); ix.SessionTitle == "" && ix.Cwd != "" {
		ix.SessionTitle = path.Base(ix.Cwd)
	}
	return ix
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

// ---- Codex cards (docs/integrations/codex.md "Questions and approvals") ----

// A node lists the Codex questions and approvals open on its machine whenever the list changes (codex.interactions,
// node/codexask.go); the manager keeps a card for each — a new one is announced, one no longer listed (answered at
// the computer, the turn interrupted, the agent gone) ends. An answer goes back to the node (codex.answer), which
// types it into the TUI and says whether Codex took it: that error reaches whoever answered.

// codexAnswerTimeout: the node checks the screen, types, and waits for Codex to take the answer (its codexConfirm).
var codexAnswerTimeout = time.Minute

// codexAnnouncedFor: which cards were announced (pushed) lately is remembered this long: a node that reconnects
// lists them again, and the phone should not ring twice for one question.
const codexAnnouncedFor = 24 * time.Hour

// codexAskedAgain: a card the node stopped listing and lists again after this long is Codex asking again (the same
// approval in a later turn has the same id), not a reconnect or a screen read that failed once: it rings the phone
// again (#3766).
var codexAskedAgain = time.Minute

func (s *Service) syncCodexInteractions(machine string, params json.RawMessage) {
	var p struct {
		Interactions []proto.Interaction `json:"interactions"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	listed := map[string]proto.Interaction{}
	for _, ix := range p.Interactions {
		if reservedCard(ix) {
			s.logf("[%s] codex card %s: %v", machine, ix.ID, errReservedCard)
			continue
		}
		ix.MachineID = machine
		ix = cleanTitle(ix)
		listed[ix.ID] = ix
	}
	s.fleet.mu.Lock()
	m := s.ixMapLocked()
	var gone []string
	var screens []*pending
	for id, pd := range m {
		if !pd.codex || pd.ix.MachineID != machine {
			continue
		}
		if _, ok := listed[id]; ok {
			delete(listed, id) // already a card
		} else {
			gone = append(gone, id)
		}
	}
	var added []proto.Interaction
	for _, ix := range listed {
		if m[ix.ID] != nil {
			continue
		}
		pd := s.codexPending(machine, ix)
		// A screen card for the same session read the terminal without knowing Codex: an approval or an overlay
		// question card is that dialog read properly. An async question is not on the terminal's keyboard at all.
		for id, old := range m {
			if coversScreen(pd) && old.ix.Kind == proto.IxScreen && old.ix.MachineID == ix.MachineID && old.ix.SessionID == ix.SessionID {
				delete(m, id)
				screens = append(screens, old)
			}
		}
		m[ix.ID] = pd
		added = append(added, ix)
	}
	now := time.Now()
	quiet := map[string]bool{}
	if s.fleet.codexAnnounced == nil {
		s.fleet.codexAnnounced = map[string]time.Time{}
	}
	if s.fleet.codexEnded == nil {
		s.fleet.codexEnded = map[string]time.Time{}
	}
	for id, at := range s.fleet.codexAnnounced {
		if now.Sub(at) > codexAnnouncedFor {
			delete(s.fleet.codexAnnounced, id)
		}
	}
	for id, at := range s.fleet.codexEnded { // on its own: an id no longer announced (handed back) is kept here too
		if now.Sub(at) > codexAnnouncedFor {
			delete(s.fleet.codexEnded, id)
		}
	}
	for _, id := range gone {
		s.fleet.codexEnded[id] = now
	}
	for _, ix := range added {
		_, announced := s.fleet.codexAnnounced[ix.ID]
		ended, wasEnded := s.fleet.codexEnded[ix.ID]
		delete(s.fleet.codexEnded, ix.ID)
		if announced && !(wasEnded && now.Sub(ended) >= codexAskedAgain) {
			quiet[ix.ID] = true
		}
		s.fleet.codexAnnounced[ix.ID] = now
	}
	s.fleet.mu.Unlock()
	for _, old := range screens {
		s.recordEnd(old.ix.ID, "cancelled", nil)
		s.finished(old)
	}
	for _, id := range gone {
		s.recordEnd(id, "cancelled", nil)
		s.finishInteraction(id)
	}
	for _, ix := range added {
		s.record(ix)
		if quiet[ix.ID] {
			s.show(ix)
			go s.feishuCardBack(ix) // listed again by a reconnected node: its Feishu card asks again
		} else {
			s.announce(ix)
		}
	}
}

// codexPending is the manager's side of a Codex card: answering it asks the node. When the node could not type the
// answer (a draft in the box, the TUI elsewhere) the card comes back, still open on the computer; when the node says
// Codex no longer asks it (prompt_changed), the node's next list has what is there now.
func (s *Service) codexPending(machine string, ix proto.Interaction) *pending {
	p := &pending{ix: ix, codex: true}
	p.local = func(r proto.Resolution) error {
		if r.Dismiss { // handed back to the computer: the same question asked again later is announced (#3766)
			s.fleet.mu.Lock()
			delete(s.fleet.codexAnnounced, ix.ID)
			delete(s.fleet.codexEnded, ix.ID)
			s.fleet.mu.Unlock()
		}
		ctx, cancel := context.WithTimeout(context.Background(), codexAnswerTimeout)
		defer cancel()
		err := s.nodeCall(ctx, machine, "codex.answer", map[string]any{"session_id": ix.SessionID, "id": ix.ID, "resolution": r}, nil)
		var re *rpc.Error
		changed := errors.As(err, &re) && re.Code == screen.PromptChanged
		if (err == nil || changed) && !r.Dismiss {
			// Answered from the phone, or Codex no longer asks it (prompt_changed): either way the card is over and no
			// longer pending, so the node's next list does not count it as gone. It ended now: listed again after
			// codexAskedAgain, it is Codex asking again and rings (#4105).
			ended := time.Now()
			if err == nil && ix.Kind == proto.IxPermission {
				// An approval answered: the node saw its dialog leave the screen before it said done, and lists the
				// same approval only once a screen read shows it again, so that listing is Codex asking again and rings
				// at once (#4585), with no quiet minute.
				ended = ended.Add(-codexAskedAgain)
			}
			s.fleet.mu.Lock()
			if s.fleet.codexEnded == nil {
				s.fleet.codexEnded = map[string]time.Time{}
			}
			s.fleet.codexEnded[ix.ID] = ended
			s.fleet.mu.Unlock()
		}
		if err != nil && !r.Dismiss && !changed {
			s.fleet.mu.Lock()
			if m := s.ixMapLocked(); m[ix.ID] == nil {
				m[ix.ID] = p
			}
			s.fleet.mu.Unlock()
			s.show(ix)
		}
		return err
	}
	return p
}

// ---- plans and permissions answered in the terminal ----

// A plan or a tool's permission answered in the terminal can outlive its PermissionRequest hook: Claude Code does not
// end the hook when its own dialog is answered, so the card stayed on the phone until the hook's timeout (#3563).
// Reconcile the pending card with the completed tool call, not the agent's status or words: working is not proof
// that anyone approved anything.
func (s *Service) syncPlanInteractions(machine string) {
	now := time.Now()
	s.fleet.mu.Lock()
	var check []*pending
	for _, p := range s.fleet.interactions {
		if p.ix.MachineID != machine || !terminalReconciled(p) || p.checking || now.Sub(p.checkedAt) < 2*time.Second {
			continue
		}
		p.checking, p.checkedAt = true, now
		check = append(check, p)
	}
	s.fleet.mu.Unlock()
	for _, p := range check {
		go s.checkPlanInteraction(machine, p)
	}
}

// terminalReconciled: p is a hook's card whose tool call's end the transcript shows: a plan, or a Claude Code hook's
// permission that names its tool call (one without an id is never guessed at; Codex's and a runtime's cards end
// through their own sources). Callers hold s.fleet.mu.
func terminalReconciled(p *pending) bool {
	if p.local != nil || p.claimed {
		return false
	}
	switch p.ix.Kind {
	case proto.IxPlan:
		return p.ix.ToolName == "ExitPlanMode"
	case proto.IxPermission:
		return p.ix.ToolUseID != "" && p.ix.Agent == "" && p.ix.Resumable
	}
	return false
}

func (s *Service) checkPlanInteraction(machine string, p *pending) {
	defer func() {
		s.fleet.mu.Lock()
		p.checking = false
		s.fleet.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var items []proto.Item
	before := 0
	// No transcript-wide scan: most plans are in the latest two turns. A few older pages cover a user continuing
	// after answering in the terminal; pages have the node's item and parse budgets too.
	for page := 0; page < 3; page++ {
		var res struct {
			Items   []proto.Item `json:"items"`
			HasMore bool         `json:"has_more"`
		}
		if err := s.nodeCall(ctx, machine, "session.items", map[string]any{"session_id": p.ix.SessionID, "turns": 2,
			"before_seq": before, "lazy_images": true}, &res); err != nil || len(res.Items) == 0 {
			return
		}
		items = append(items, res.Items...)
		if planCallFinished(p.ix, items) {
			s.finishExternalPlan(p)
			return
		}
		next := res.Items[0].Seq
		if !res.HasMore || next <= 0 || (before > 0 && next >= before) {
			return
		}
		before = next
	}
}

// PermissionRequest in some Claude Code versions omits tool_use_id. In that case the call immediately preceding
// this hook is identified by its timestamp; ambiguous calls, missing timestamps and earlier results fail closed.
// The result means only that the call ended (approval, rejection or interruption); no resolution is invented.
func planCallFinished(ix proto.Interaction, items []proto.Item) bool {
	id := ix.ToolUseID
	if id == "" {
		if ix.Kind == proto.IxPermission || ix.CreatedAt.IsZero() { // only a plan's call is guessed at
			return false
		}
		for _, it := range items {
			if it.Role != "assistant" || it.Kind != proto.KindToolUse || it.ToolName != "ExitPlanMode" || it.ToolUseID == "" ||
				it.TS.IsZero() || it.TS.After(ix.CreatedAt) || ix.CreatedAt.Sub(it.TS) > 30*time.Second {
				continue
			}
			if id != "" && id != it.ToolUseID {
				return false // two candidate plans: neither can be safely attributed to this hook
			}
			id = it.ToolUseID
		}
	}
	if id == "" {
		return false
	}
	for _, it := range items {
		if it.Role == "user" && it.Kind == proto.KindToolResult && it.ToolUseID == id &&
			!it.TS.IsZero() && !it.TS.Before(ix.CreatedAt) {
			return true
		}
	}
	return false
}

func (s *Service) finishExternalPlan(p *pending) {
	s.fleet.mu.Lock()
	if s.fleet.interactions[p.ix.ID] != p || p.claimed {
		s.fleet.mu.Unlock()
		return // the phone answered (its answer is the history's) or this card was replaced while the read was in flight
	}
	delete(s.fleet.interactions, p.ix.ID)
	if p.orphaned != nil {
		p.orphaned.Stop()
	}
	s.fleet.mu.Unlock()
	s.recordEnd(p.ix.ID, "dismissed", nil)
	select {
	case p.done <- proto.Resolution{Dismiss: true}:
	default:
	}
	s.finished(p)
}

// ---- the interaction queue as the assistant's own (its interaction_answer tool, the channels) ----

// answerQuestion resolves the pending question id of machine's session with answers, as the assistant answered it
// (and why, "" when it did not say); false when it is no longer pending.
func (s *Service) answerQuestion(machine, session, id string, answers map[string]string, reason string) (bool, error) {
	ix, ok := s.pendingIx(id)
	if !ok {
		return false, nil
	}
	if ix.Kind != proto.IxQuestion {
		return false, fmt.Errorf("interaction %s is a %s, not a question", id, ix.Kind)
	}
	if ix.MachineID != machine || ix.SessionID != session {
		return false, fmt.Errorf("interaction %s is not machine=%s session_id=%s's", id, machine, session)
	}
	if err := s.resolve(id, proto.Resolution{Answers: answers, AnsweredBy: proto.AnsweredByAssistant, Reason: reason}); err != nil {
		if errors.Is(err, errNotPending) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// endedStatus is how a card that resolve found no longer pending (err) ended: the claim's when another answer took
// it a moment ago (#1230), else the history's.
func (s *Service) endedStatus(ctx context.Context, id string, err error) string {
	if just := (*answeredJustNow)(nil); errors.As(err, &just) {
		return just.status
	}
	return s.interactionOutcome(ctx, id)
}

// interactionOutcome is how an interaction that is no longer pending ended, from the history: resolved, dismissed,
// expired, cancelled; "" when the history has none.
func (s *Service) interactionOutcome(ctx context.Context, id string) string {
	st := s.fleetStore()
	if st == nil {
		return ""
	}
	status, _, err := st.InteractionOutcome(ctx, id)
	if err != nil {
		return ""
	}
	return status
}

// ---- the assistant's own questions and cards ----

// askOnPhone shows the questions the assistant asks and its cards about agents' turns (its snapshot's interactions)
// like any agent's card: recorded, announced, pushed, expired (waitInteraction); the answer goes to its Action, or
// waits when it cannot take it (keepAnswer). A question that left the snapshot is withdrawn, as a hook's that went
// away, and nothing is said about it. One it still reports a while after its answer went, with no answer waiting for
// it, retries delivery from its durable outcome rather than asking the user again.
func (s *Service) askOnPhone(list []proto.Interaction) {
	f := &s.fleet
	f.askMu.Lock()
	defer f.askMu.Unlock()
	if f.asked == nil {
		f.asked = map[string]*askedOne{}
	}
	now := map[string]bool{}
	for _, ix := range list {
		now[ix.ID] = true
		if q := f.asked[ix.ID]; q != nil {
			if q.ended.IsZero() || time.Since(q.ended) < askAgainAfter || s.answerKept(ix.ID) {
				continue
			}
			q.cancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		q := &askedOne{cancel: cancel}
		f.asked[ix.ID] = q
		go func(ix proto.Interaction) {
			defer func() {
				f.askMu.Lock()
				q.ended = time.Now()
				f.askMu.Unlock()
			}()
			r, err := s.waitOwnedQuestion(ctx, ix)
			if err != nil {
				if ctx.Err() == nil {
					s.logf("assistant: question %s: %v", ix.ID, err)
				}
				return
			}
			if r.Dismiss && ctx.Err() != nil {
				return // withdrawn: it knows
			}
			if r.Dismiss && r.Reason == "" && r.AnsweredBy == "" && len(r.Answers) == 0 && s.ownQuestionExpired(ix) {
				r.Reason = proto.DismissedExpired // its card expired unanswered: the question becomes a to-do (ask_todo.go)
			}
			answer, _ := json.Marshal(r)
			act := Action{ActionID: "act_" + randomHex(6), SessionID: ix.SessionID, Op: "answer", InteractionID: ix.ID,
				InteractionVersion: 1, Answer: answer}
			_, err = s.Action(context.Background(), act)
			s.settleAnswer(ix, act, err)
		}(ix)
	}
	for id, q := range f.asked {
		if !now[id] {
			q.cancel()
			delete(f.asked, id)
		}
	}
}

// askedOne is a question or card of the assistant's on the phone: cancel withdraws it; ended is when its answer (or
// its expiry) went to its Action.
type askedOne struct {
	cancel context.CancelFunc
	ended  time.Time
}

// askAgainAfter is how long a question the assistant still reports after its answer went may stay off the phone.
var askAgainAfter = 30 * time.Second

// heldActs are the answers to the assistant's questions and cards its Action did not take yet, by interaction id:
// they go to it again after each snapshot (deliverAnswers) until it takes them, says they are for nothing it has
// any more (CodeGone), or their card would have expired.
type heldActs struct {
	mu   sync.Mutex
	acts map[string]heldAct
	busy bool
}

type heldAct struct {
	act         Action
	until, next time.Time
}

// answerRetryAfter is how long an answer the Action could not take waits before it is tried again.
var answerRetryAfter = 10 * time.Second

// keepAnswer holds act, which the Action did not take (err), until until; false when err is final (its question or
// card is gone): nothing is kept.
func (s *Service) keepAnswer(act Action, err error, until time.Time) bool {
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == assistantwire.CodeGone {
		return false
	}
	now := time.Now()
	if until.Before(now) {
		until = now.Add(24 * time.Hour) // as long as a card of the manager's lasts
	}
	u := &s.fleet.undelivered
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.acts == nil {
		u.acts = map[string]heldAct{}
	}
	u.acts[act.InteractionID] = heldAct{act: act, until: until, next: now.Add(answerRetryAfter)}
	return true
}

// settleAnswer ends act, the answer to the assistant's card ix, once its Action had it (err: it did not take it): one it
// did not take is kept for another try (keepAnswer). An answer to a card with a secret field goes once: it is never
// kept (what was typed into it would wait in memory for up to a day, for a call that is gone, a restart or a cancel,
// and that asks with a new card), and its bytes are cleared now; the Action decoded its own copy.
func (s *Service) settleAnswer(ix proto.Interaction, act Action, err error) {
	if hasSecretField(ix) {
		clear(act.Answer)
		if err != nil {
			s.logf("assistant: answer %s: %v (not kept: the card has a secret field)", ix.ID, err)
		}
		return
	}
	if err != nil && !s.keepAnswer(act, err, ix.ExpiresAt) {
		s.logf("assistant: answer %s: %v", ix.ID, err)
	}
}

// answerKept: an answer to interaction id is waiting for the Action.
func (s *Service) answerKept(id string) bool {
	u := &s.fleet.undelivered
	u.mu.Lock()
	defer u.mu.Unlock()
	_, ok := u.acts[id]
	return ok
}

// deliverAnswers gives the Action the answers kept that are due (after a snapshot). One delivery at a time.
func (s *Service) deliverAnswers() {
	u := &s.fleet.undelivered
	u.mu.Lock()
	if u.busy || len(u.acts) == 0 {
		u.mu.Unlock()
		return
	}
	u.busy = true
	now := time.Now()
	var due []heldAct
	for id, a := range u.acts {
		switch {
		case now.After(a.until):
			delete(u.acts, id)
			s.logf("assistant: answer %s: dropped, its card expired before it was taken", id)
		case !a.next.After(now):
			delete(u.acts, id)
			due = append(due, a)
		}
	}
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()
	for _, a := range due {
		if _, err := s.Action(context.Background(), a.act); err != nil && !s.keepAnswer(a.act, err, a.until) {
			s.logf("assistant: answer %s: %v", a.act.InteractionID, err)
		}
	}
}

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
