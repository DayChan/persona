package assistant

import (
	"context"
	"fmt"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// When a "finished" push is worth sending (docs/server/server.md "Push"), and the pushes of the cards. Borrowed from Ovis
// (github.com/devswha/ovis, server/push.ts, MIT): a push is for someone away from the computer, so
//   - wait a while after the turn ends; if the agent resumes, or the user looks at it (herdr idle, or opened in the
//     app), nothing is sent;
//   - only turns that took a while are worth it ("long"); a quick answer is read where it was asked;
//   - background tasks and subagents still running mean the work is not done: hold, and push once when the turn that
//     follows their reports ends.
// The manager decides; the hub delivers (notify.request): each device picks what it wants (its NotifyPrefs), the
// timings are hub.json "notify" (hello's notify).

// fleetTimings are hello's notify timings with their defaults.
type fleetTimings assistantwire.NotifyTimings

func (s *Service) notifyTimings() fleetTimings {
	return fleetTimings(s.hello(context.Background()).Notify)
}

func msOr(ms int64, def time.Duration) time.Duration {
	if ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return def
}

func (t fleetTimings) doneDelay() time.Duration { return msOr(t.DoneDelayMS, 60*time.Second) }
func (t fleetTimings) longTurn() time.Duration  { return msOr(t.LongTurnMS, 60*time.Second) }
func (t fleetTimings) backgroundHold() time.Duration {
	return msOr(t.BackgroundHoldMS, 120*time.Minute)
}
func (t fleetTimings) hookTimeout() time.Duration { return msOr(t.HookTimeoutMS, 30*time.Minute) }

// turnTracker follows each session's turns between status snapshots: when the turn started, which end is the latest
// (gen), and whether it is held for background work.
type turnTracker struct {
	mu    sync.Mutex
	turns map[string]*turn
}

type turn struct {
	start      time.Time
	endedAt    time.Time
	gen        int
	background bool // ended once with background work still running; the next end decides
	// sawBackground: background work was seen running during the turn (holdIfBackground)
	sawBackground bool
}

func newTurnTracker() *turnTracker { return &turnTracker{turns: map[string]*turn{}} }

// working: the session is working (again). A turn that ended moments ago and resumes is the same turn, so its start
// is kept; any pending end is cancelled.
func (t *turnTracker) working(key string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr := t.turns[key]
	if tr == nil {
		tr = &turn{start: now}
		t.turns[key] = tr
	}
	tr.gen++
}

// startOf: when the session's current (or last held) turn started; zero when not tracked.
func (t *turnTracker) startOf(key string) time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tr := t.turns[key]; tr != nil {
		return tr.start
	}
	return time.Time{}
}

// ended: working → done / idle; background is whether background work still runs as it stops. Returns the generation
// the delayed check must still see. A turn that stops on background work is held work from here on, not only once the
// delayed check sees it still running: work that reports back within the delay wakes the agent, the check of this end
// is stale by then, and the woken turn's end is the one push (#4187).
func (t *turnTracker) ended(key string, now time.Time, background bool) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr := t.turns[key]
	if tr == nil { // the manager came up mid-turn: count from now
		tr = &turn{start: now}
		t.turns[key] = tr
	}
	tr.gen++
	tr.endedAt = now
	if background {
		tr.background = true
	}
	return tr.gen
}

// sawBackground: the session's turn has background work running (as a working session's list shows it).
func (t *turnTracker) sawBackground(key string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr := t.turns[key]
	if tr == nil { // the manager came up mid-turn: count from now
		tr = &turn{start: now}
		t.turns[key] = tr
	}
	tr.sawBackground = true
}

// holdIfBackground: the session's turn stopped and a report woke it unseen (stop and wake in one list): held as
// ended would have, when the turn had background work running.
func (t *turnTracker) holdIfBackground(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if tr := t.turns[key]; tr != nil && tr.sawBackground {
		tr.background = true
	}
}

// drop forgets the session's turn that ended as gen, when nothing happened since, and says when it started (ok): a
// turn told elsewhere is over all the same, and left tracked its start would be the next turn's (#4133).
func (t *turnTracker) drop(key string, gen int) (start time.Time, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr := t.turns[key]
	if tr == nil || tr.gen != gen {
		return time.Time{}, false
	}
	delete(t.turns, key)
	return tr.start, true
}

// finishVerdict is what the delayed check decided.
type finishVerdict int

const (
	finishStale finishVerdict = iota // something happened since; a later check decides
	finishDrop                       // no push for this turn
	finishHold                       // background work still running: wait for the turn its reports start
	finishPush                       // push, to the devices whose prefs take a turn this long
)

// settle runs after the delay. sess is the session now, seen whether the user has looked at it since it finished
// (in herdr or in the app). Everything that makes a push pointless is checked here, at the last moment.
func (t *turnTracker) settle(key string, gen int, sess proto.Session, seen, muted, assistantPushed bool, now time.Time, hold time.Duration) (finishVerdict, time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr := t.turns[key]
	if tr == nil || tr.gen != gen {
		return finishStale, 0
	}
	if sess.Status == proto.StatusWorking {
		return finishStale, 0
	}
	took := tr.endedAt.Sub(tr.start)
	if completionChild(sess) || sess.Status != proto.StatusDone || seen || muted || assistantPushed {
		delete(t.turns, key)
		return finishDrop, took
	}
	if backgroundRunning(sess) {
		if now.Sub(tr.start) > hold {
			delete(t.turns, key)
			return finishDrop, took
		}
		tr.background = true
		return finishHold, took
	}
	// A turn started by a background task's report: on its own the user did not ask for anything; as the end of
	// held work it is exactly the "all done" they want.
	if sess.WokeByTask && !tr.background {
		delete(t.turns, key)
		return finishDrop, took
	}
	delete(t.turns, key)
	return finishPush, took
}

// noteStop keeps what a hook event says about completions for stopDuplicate: a Stop's reply signature, and that a
// prompt starts something new.
func (s *Service) noteStop(machine, sid string, p map[string]any) {
	event, _ := p["event"].(string)
	k := [2]string{machine, sid}
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	switch event {
	case "UserPromptSubmit":
		delete(s.fleet.pushedSig, k)
		delete(s.fleet.stopSig, k)
	case "Stop":
		sig, _ := p["stop_sig"].(string)
		if sig == "" {
			delete(s.fleet.stopSig, k)
			return
		}
		if s.fleet.stopSig == nil {
			s.fleet.stopSig = map[[2]string]string{}
		}
		s.fleet.stopSig[k] = sig
	}
}

// notifyFinished decides, after the delay, whether this end of a turn is worth a push and to which devices.
func (s *Service) notifyFinished(machine, id string, gen int) {
	t := s.notifyTimings()
	time.Sleep(t.doneDelay())
	sess, ok := s.decoratedSession(machine, id)
	if ok && s.completionChildrenRunning(machine, sess) {
		// Only the completion decision uses this copy; session state and reports stay intact.
		sess.Background = append([]proto.BackgroundTask{{ID: "completion-children", Kind: "agent"}}, sess.Background...)
	}
	if ok && limitTakenUp(sess, time.Now()) {
		// A usage limit stopped it until the reset, and its CLI or the manager goes on with it then (usage_limit.go): the
		// work did not finish, and nothing is the user's to do. The turn stays followed, so the end after the reset is
		// the one pushed, timed from this start. One nothing takes up is pushed below, as a usage limit (doneAlert).
		return
	}
	if ok && !sess.Assistant && sess.Status != proto.StatusWorking {
		s.kbAgentDone(machine, sess)
	}
	if ok && sess.Work != nil && sess.Work.Phase == proto.WorkBlocked && (sess.Work.WakeAt != nil || sess.Work.InteractionID != "") {
		return // the Agents business goes on with it ("Continue", a wake), or its card already rang
	}
	// An inbox agent's result is the draft in the inbox; a turn asked for through the bot is answered there; a quiet
	// task's is told in the main conversation (taskTurnEnded, #1731).
	// The turn is over all the same: its start and its report mark are not the next turn's (#4133).
	if !ok || s.isInboxAgent(id) || s.channelAnsweredPush(id) || s.quietSession(machine, id) {
		// Background work still running: the turn its reports start is this one's end (as settle holds it).
		if !ok || sess.Status != proto.StatusWorking && !backgroundRunning(sess) {
			if start, dropped := s.turnsBook().drop(machine+"/"+id, gen); dropped {
				s.reportTurn(id, start) // used up, as by a turn settle drops
			}
		}
		return
	}
	s.fleet.mu.Lock()
	pushed := time.Since(s.fleet.notified[id]) < 3*time.Minute // the assistant already pushed what matters (notify_user)
	s.fleet.mu.Unlock()
	seen := agentState(sess, s.seenAt(machine, id)) == proto.StateIdle
	start := s.turnsBook().startOf(machine + "/" + id)
	verdict, took := s.turnsBook().settle(machine+"/"+id, gen, sess, seen, s.muted(machine, id), pushed, time.Now(), t.backgroundHold())
	if verdict == finishStale || verdict == finishHold {
		return // the turn goes on: its mark waits for how it ends
	}
	// "Only long-running turns" is for turns the user started: a quick answer is read where it was asked. A morning briefing or a task notice
	// has nobody waiting for it; a quick one was never pushed and the user had to go and look (#3438). The mark is
	// used up by this turn whether it is pushed or dropped (notify_user, seen, muted): left over, the user's own next
	// turn took it.
	report := s.reportTurn(id, start)
	if verdict != finishPush {
		if completionChild(sess) {
			s.logf("assistant: completion suppressed for child %s/%s (parent=%s root=%s)", machine, id, sess.ParentSessionID, sess.RootSessionID)
		}
		return
	}
	long := t.longTurn()
	if report {
		long = 0
	}
	// The own conversation of a scheduled task that pushes only what its runs send, finished normally (#2396): its
	// news, if any, went out with notify_user. Decided after settle, not as a push: a turn held for its background
	// work stays followed, and the wake that ends it is judged on its own (an error there is pushed).
	if s.quietTaskFinish(sess) {
		return
	}
	// A turn a follow-up (supervision) message started, finished normally (#3124): what the user needs to know went out with notify_user.
	if s.supervising(id) && assistantwire.SupervisedFinish(sess, true) {
		return
	}
	if s.finishedDuplicate(machine, sess) {
		return
	}
	place := s.pushPlace(machine, sess)
	head, text := doneAlert(s.lang(context.Background()), sess)
	// A routine with facts says the counts it worked out (briefing.go) when it ended normally and asks nothing: a question
	// is what the owner acts on, and a health conversation says only that there is news.
	if !strings.HasPrefix(head, "⚠️") && !strings.HasPrefix(head, "⏳") && !sess.AwaitsReply && spaceOf(sess) != SpaceHealth {
		if counts := s.briefPush(id, s.lang(context.Background()), report); counts != "" {
			text = counts
		}
	}
	s.deliver(Notification{Kind: assistantwire.NotifyDone, DeviceID: machine, Subtitle: place, SessionID: id, Title: head,
		Body: text, TookMS: took.Milliseconds(), LongMS: long.Milliseconds()})
	// The same finished turn's one Feishu card (#4674); the sender re-checks the pushes switch and the conversations
	// with their own Feishu path (main, scheduled task, health).
	go s.feishuAgentDone(machine, sess, head, text, place)
	// And the topic of the todo this agent works on, if it has one (the same decision again, not a second one).
	s.threadAgentDone(machine, sess)
}

// reportSlack is how long before its turn started a scheduled run or notice may have been sent (the node starts the
// turn after the send is answered, a resumed conversation a little later).
const reportSlack = 2 * time.Minute

// noteReportTurn: a scheduled run or a task notice was just sent into conversation session; the turn it starts is
// pushed whatever its length (the device's "Finished" push off still means no push).
func (s *Service) noteReportTurn(session string) {
	if session == "" {
		return
	}
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.reportSent == nil {
		s.fleet.reportSent = map[string]time.Time{}
	}
	now := time.Now()
	for k, at := range s.fleet.reportSent {
		if now.Sub(at) > time.Hour {
			delete(s.fleet.reportSent, k)
		}
	}
	s.fleet.reportSent[session] = now
}

// reportLead is how long after the turn started its send may still be noted (a new conversation is working before
// startConversation returns).
const reportLead = 30 * time.Second

// reportTurn: the turn of session that started at start is one noteReportTurn marked; the mark is used up. A mark
// made after the turn started is for the next turn (the message waits behind this one, a turn of the user's) and is
// kept; a later turn the user starts goes by the device's setting again.
func (s *Service) reportTurn(session string, start time.Time) bool {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	at, ok := s.fleet.reportSent[session]
	switch {
	case !ok || start.IsZero():
		return false
	case at.After(start.Add(reportLead)):
		return false // sent during this turn: it starts the next one
	case at.Before(start.Add(-reportSlack)):
		delete(s.fleet.reportSent, session) // its turn went by unseen
		return false
	}
	delete(s.fleet.reportSent, session)
	return true
}

// pushPlace is a push's subtitle for sess on machine: "机器 · 空间 › 标签" without the parts it lacks (a tab named by
// its number is left out).
func (s *Service) pushPlace(machine string, sess proto.Session) string {
	place := s.machineName(machine)
	if sess.SpaceName != "" {
		place += " · " + sess.SpaceName
		if sess.SlotName != "" && sess.SlotName != fmt.Sprint(sess.SlotIndex) {
			place += " › " + sess.SlotName
		}
	}
	return place
}

// pushTitle is how a push names sess: its title, else its directory's name.
func pushTitle(sess proto.Session) string {
	title := transcript.Title(sess.Title)
	if title == "" || title == "Claude Code" {
		title = path.Base(sess.Cwd)
	}
	return title
}

// doneAlert is the title and body of the push for sess's finished turn, in l.
func doneAlert(l i18n.Lang, sess proto.Session) (head, text string) {
	if spaceOf(sess) == SpaceHealth { // the lock screen hears there is news, not what it is, nor the conversation's title
		return i18n.T(l, "push.health_title"), i18n.T(l, "push.health_body")
	}
	title := pushTitle(sess)
	head, text = title, sess.LastText // no mark: a finished turn is the usual news
	if ask := replyAsk(sess); sess.AwaitsReply && ask != "" {
		text = ask // what it asks, not the reply's first lines: the question is what the user acts on (#5096)
	}
	if text == "" {
		text = i18n.T(l, "hub-machine.done")
	}
	if w := sess.Work; w != nil && w.Line != "" {
		switch w.Phase {
		case proto.WorkDone:
			text = w.Line
		case proto.WorkBlocked:
			head, text = "⏳ "+title, w.Line
		}
	}
	// The model service cut the turn off: marked ⚠️, not told as done (#1516). A Codex turn's error is no words of its
	// answer: the session says it (#2185).
	if e := strings.TrimSpace(sess.TurnError); e != "" && sess.TurnErrorKind == proto.TurnErrorUsageLimit {
		// Not "the model service failed": the subscription's limit, which the user goes on from after it resets.
		head, text = "⚠️ "+title, i18n.T(l, "push.usage_limit", i18n.Args{"error": e})
	} else if e != "" {
		head, text = "⚠️ "+title, interruptedSummary+"："+e
	} else if assistantwire.CutOff(text) {
		head, text = "⚠️ "+title, interruptedSummary+"："+strings.TrimSpace(text)
	}
	return head, text
}

// kbAgentDone tells the knowledge base an agent finished a turn (its projects' timelines get what it came to).
func (s *Service) kbAgentDone(machine string, sess proto.Session) {
	if strings.TrimSpace(sess.LastText) == "" {
		return
	}
	s.kbAgentTurn(context.Background(), assistantwire.KBAgentTurn{MachineID: machine, SessionID: sess.SessionID, Cwd: sess.Cwd,
		Title: sess.Title, Line: workLogLine(sess)})
}

// notifyInteraction pushes a new card to the devices that take "needs you" pushes.
func (s *Service) notifyInteraction(ix proto.Interaction, machine string) {
	s.deliver(Notification{Kind: assistantwire.NotifyInput, DeviceID: ix.MachineID, Subtitle: machine, SessionID: ix.SessionID,
		Interaction: &ix})
}

// deliver has the hub deliver a push the manager decided on (notify.request), off the caller's way.
func (s *Service) deliver(n Notification) {
	if n.ID == "" {
		n.ID = "ntf_" + randomHex(8)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.hub.NotifyRequest(ctx, NotifyRequest{Notification: n}); err != nil {
			s.logf("assistant: push: %v", err)
		}
	}()
}

// noteAsked: the conversation pushed what matters itself (notify_user), or a scheduled run in it was failed and
// pushed as such: every finished push of the whole conversation is held back for the next three minutes.
func (s *Service) noteAsked(session string) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.notified == nil {
		s.fleet.notified = map[string]time.Time{}
	}
	s.fleet.notified[session] = time.Now()
}

// quietTaskConversation: session is the own conversation of a scheduled task that pushes only what its runs send
// (TaskView.Push, #2396), as the last snapshot has the tasks. By the session alone: an assistant conversation's id is
// unique, and a machine id from an older alias would miss it.
func (s *Service) quietTaskConversation(session string) bool {
	r := &s.fleet.rep
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.tasks {
		if t.Push == assistantwire.PushAsked && t.RunSessionID != "" && t.RunSessionID == session {
			return true
		}
	}
	return false
}

// mainSessionRep says whether session is the main conversation as the last snapshot's profile has it (the manager's
// copy of the assistant settings).
func (s *Service) mainSessionRep(machine, session string) bool {
	r := &s.fleet.rep
	r.mu.RLock()
	p := r.profile
	r.mu.RUnlock()
	return p != nil && p.Main == session && p.MachineID == machine
}

// taskSessionRep says whether session is a scheduled task's own conversation as the last snapshot has it, whatever
// the task's push mode: the pushes mirror owns those turns.
func (s *Service) taskSessionRep(machine, session string) bool {
	r := &s.fleet.rep
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.tasks {
		if t.RunSessionID == session && t.RunMachineID == machine {
			return true
		}
	}
	return false
}

// quietTaskFinish: sess's finished turn is not pushed: a quiet task's own conversation (quietTaskConversation) that
// may go unpushed now (assistantwire.QuietTaskFinish: Claude Code, not the main conversation, a normal finish).
func (s *Service) quietTaskFinish(sess proto.Session) bool {
	r := &s.fleet.rep
	r.mu.RLock()
	p := r.profile
	r.mu.RUnlock()
	if p == nil {
		return false // the assistant's settings (its main conversation) not known yet: pushed as ever
	}
	return s.quietTaskConversation(sess.SessionID) && assistantwire.QuietTaskFinish(sess, p.Main)
}
