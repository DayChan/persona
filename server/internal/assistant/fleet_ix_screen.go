package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
	"github.com/nwverse/persona/server/internal/transcript"
)

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
