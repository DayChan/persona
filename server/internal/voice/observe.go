package voice

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// What the bound conversation does that the call did not hand over, and how a turn that is not over yet is told
// (docs/assistant/voice.md "Following a turn", "Announcements", #3503).
//
// Observed runs (protocol 2): the call watches the conversation it is bound to from ready on. When it works and no run
// of the call's is on it (none being sent, none held), the turn it works on is followed as a run of its own: the user
// opened the call while it worked, or it began a turn for another reason (a message typed on the phone or at the
// computer, a wake-up, a task reporting back). Bound to an agent, its answer is said as any run's is. Bound to a Persona
// conversation (the main one, another), a turn that came by another channel (a chat app's bot, a scheduled task) is
// only mentioned, once a channel a call (voice/observed): its answer went back there. Protocol 1 has no fixed target
// (the main conversation of the moment): no observed runs. Not observed either: the turn the user just stopped
// ("Stop:", its state behind the stop), and a turn of a message the call sent whose send ended unknown (it got there
// after all: followed as the call's run).
//
// A turn of an agent that stopped is not always over, and what says so is the hub's triage of it (docs/assistant/agent-work.md),
// when the user turned it on: about triage_settle_seconds (15 s) plus a model call after the turn stopped. A model
// error the manager goes on from ("Continue") is not the end: a turn that stopped on a model error waits for that triage when
// the manager would go on (auto_continue), and while it says a "Continue" is due. A turn it says waits on something outside
// (a deploy, CI, another agent) is told once (voice/progress#waiting) and then waited on: the answer of a turn that
// stopped is said at once (the user does not wait for the triage), and the run is kept until the triage of that turn
// is in; when it says the turn waits, the run goes on, blocked, and its later turns (the manager wakes the agent) are
// the call's, each waiting for its triage, since a woken turn often stops on the same wait. While it works with nothing
// heard in the call for heartbeatAfter, the user hears that it still works: the sort of step and how long, nothing it
// wrote (voice/progress).

var (
	// heartbeatAfter is how long a call is quiet while what was handed over is worked on before the user hears that it
	// still is (docs/lessons/voice.md "Suggested constant values": a say, which the model always speaks; not more often, or it chats).
	heartbeatAfter = 2 * time.Minute
	// triageWait is how long a stopped turn of an agent waits for the hub's triage of it: the manager triages a turn that
	// stayed stopped triage_settle_seconds (15 s by default), with a model call.
	triageWait = 30 * time.Second
	// triageEvery is how often the hub's triage switches (the user's settings) are read again.
	triageEvery = 2 * time.Minute
)

const (
	// heartbeatStale: a heartbeat not said by then is not said (the call was not quiet after all).
	heartbeatStale = 30 * time.Second
	// triageRetry: switches that could not be read count as off, and are asked again after this.
	triageRetry = 30 * time.Second
	// mineMost bounds the call's sends that ended unknown, kept to know their turns.
	mineMost = 20
)

// observer is a call's watch on the conversation it is bound to (the tracker's).
type observer struct {
	on  bool // protocol 2, from ready on
	key linkKey
	// sending counts the call's sends to it in flight: what it works on then may be the call's own. sends counts those
	// begun: a look one began during is dropped (its message may be in what the look read).
	sending atomic.Int32
	sends   atomic.Int64
	// doneSeq is the last line of it a finished run read: what works after that is a new turn.
	doneSeq int
	n       int               // observed runs made (their ids)
	told    map[string]bool   // what other channels' turns were mentioned (by name): once a call each
	names   map[string]string // the hub's channels, id -> name in the call's language (nil until read)
	// afterStop: the user stopped its turn ("Stop:"); until it is seen not working, or a message after stopSeq (its last
	// line then) begins another turn, what it works on is that turn, its state behind the stop.
	afterStop bool
	stopSeq   int
	// mine are the call's runs whose send ended unknown (a 502, a timeout), by message id: a turn their message begins
	// is theirs.
	mine map[string]*voiceRun
}

// triageView is the hub's triage switches as the tracker last read them.
type triageView struct {
	sw triageSwitches
	at time.Time // when they are read again
}

// observeTarget starts watching the bound conversation (protocol 2, once the call is ready, after the runs it handed
// back). It is looked at at once: it may be working as the call starts. The hub link follows it from its last line on
// (bind, asks.go), not from its first: the backlog would hold every old turn's end.
func (t *tracker) observeTarget(key linkKey) {
	select {
	case t.ops <- func() {
		t.obs.on, t.obs.key = true, key
		t.observe(t.c.ctx)
	}:
	case <-t.c.ctx.Done():
	}
}

// follows: what key's conversation does next is told by itself, as an observed turn (the bound conversation).
func (o *observer) follows(key linkKey) bool { return o.on && o.key == key }

// read: run r read its conversation to line last.
func (o *observer) read(r *voiceRun, last int) {
	if o.on && (linkKey{r.MachineID, r.SessionID}) == o.key && last > o.doneSeq {
		o.doneSeq = last
	}
}

// stopped: the user stopped the bound conversation's turn, its last line then at seq (math.MaxInt not known).
func (o *observer) stopped(seq int) {
	o.afterStop, o.stopSeq = true, seq
}

// stopOver: the turn the user stopped is over (seen not working, or another began): what it wrote is not a new turn.
func (o *observer) stopOver() {
	if !o.afterStop {
		return
	}
	o.afterStop = false
	if o.stopSeq != math.MaxInt {
		o.doneSeq = max(o.doneSeq, o.stopSeq)
	}
}

// unknown keeps r, whose send ended unknown, until its message begins a turn.
func (o *observer) unknown(r *voiceRun) {
	if o.mine == nil {
		o.mine = map[string]*voiceRun{}
	}
	if len(o.mine) >= mineMost {
		for id := range o.mine {
			delete(o.mine, id)
			break
		}
	}
	o.mine[r.ID] = r
}

// observe follows the turn the bound conversation works on when no run of the call's is on it.
func (t *tracker) observe(ctx context.Context) {
	o := &t.obs
	begun := o.sends.Load()
	if !o.on || o.sending.Load() > 0 || slices.ContainsFunc(t.runs, func(r *voiceRun) bool {
		return linkKey{r.MachineID, r.SessionID} == o.key
	}) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	hub := t.c.pl.hub
	sess, err := hub.session(ctx, o.key.machine, o.key.session)
	if err != nil {
		return
	}
	t.saw(o.key, sess)
	if sess == nil {
		return
	}
	if !working(sess) {
		o.stopOver()
		return
	}
	page, err := hub.lastTurns(ctx, o.key.machine, o.key.session, 1)
	if err != nil {
		return
	}
	if o.sending.Load() > 0 || o.sends.Load() != begun {
		return // a send of the call's began while it read: the turn may be that send's (its run follows it)
	}
	if o.afterStop {
		if !slices.ContainsFunc(page.Items, func(it proto.Item) bool {
			return it.Seq > o.stopSeq && it.Role == "user" && it.Kind == proto.KindText
		}) {
			return // the turn the user stopped, listed working a while after the stop
		}
		o.stopOver()
	}
	if o.doneSeq > 0 && page.last() <= o.doneSeq {
		return // nothing written since a run read its answer: its state is behind its transcript
	}
	msg, ended := turnStart(page.Items, o.doneSeq)
	if msg != nil && msg.ClientMsgID != "" && o.mine[msg.ClientMsgID] != nil {
		r := o.mine[msg.ClientMsgID]
		delete(o.mine, msg.ClientMsgID)
		t.revive(r, msg)
		return
	}
	r := &voiceRun{Observed: true, Gen: -1, State: runWorking}
	o.n++
	r.ID, r.Channel, r.MachineID, r.SessionID = fmt.Sprintf("observed-%d", o.n), "voice", o.key.machine, o.key.session
	r.Started, r.Seen = clock(), true
	if msg != nil {
		r.Base = msg.Seq - 1
		if msg.ClientMsgID != "" {
			r.MsgIDs = []string{msg.ClientMsgID}
		} else {
			r.Probe = channel.Probe(msg.Text)
		}
		r.foreign, r.from = t.sourceOf(ctx, *msg)
	} else {
		// Its message is not in yet, or it began without one (a task reporting back): what comes after the last turn.
		r.Base = max(o.doneSeq, ended)
		if r.Base == 0 && len(page.Items) > 0 {
			r.Base = page.Items[0].Seq - 1
		}
	}
	t.link.watch(o.key, r.Base)
	t.runs = append(t.runs, r)
	t.c.count(func(l *callLog) { l.observed++ })
	t.c.logf("run %s: the bound conversation works on a turn the call did not hand over: followed (base %d)", r.ID, r.Base)
}

// revive follows r, a run of the call's whose send ended unknown, as its message begins the turn the conversation
// works on: it got there after all, and its result is the call's (the app sees the run go on).
func (t *tracker) revive(r *voiceRun, msg *proto.Item) {
	r.restart(clock())
	r.Base, r.MsgIDs, r.Probe, r.Seen = msg.Seq-1, []string{msg.ClientMsgID}, "", true
	r.Code, r.Message, r.Draft, r.Clearable = "", "", "", ""
	t.link.watch(linkKey{r.MachineID, r.SessionID}, r.Base)
	t.runs = append(t.runs, r)
	t.set(r, runWorking)
	t.c.logf("run %s: its send ended unknown, and its message begins the turn the conversation works on: followed", r.ID)
}

// turnStart is the message the turn the conversation works on began with, in its last exchange (items): the first
// user line after seq after with no turn ended after it; nil when its message is not in yet or it began without one.
// ended is the last line of the last turn that ended there.
func turnStart(items []proto.Item, after int) (msg *proto.Item, ended int) {
	for i := range items {
		it := &items[i]
		switch {
		case it.TurnDuration != nil:
			ended, msg = it.Seq, nil
		case msg == nil && it.Seq > after && it.Role == "user" && it.Kind == proto.KindText:
			msg = it
		}
	}
	return msg, ended
}

// placeObserved finds, as an observed run's turn is over, where it came from: by the message it began with, which is
// in by now (bound to an agent everything is said; a Persona conversation's turns come by channels too).
func (t *tracker) placeObserved(ctx context.Context, r *voiceRun) {
	if t.c.target == nil || t.c.target.Kind == "agent" {
		return
	}
	page, err := t.c.pl.hub.itemsAfter(ctx, r.MachineID, r.SessionID, r.Base)
	if err != nil {
		return // as it was placed when it was first seen
	}
	if msg := opener(page.Items); msg != nil {
		r.foreign, r.from = t.sourceOf(ctx, *msg)
	}
}

// opener is the message the last turn in items began with: its first user line after the last turn that ended before
// it (turn_duration); nil when there is none.
func opener(items []proto.Item) *proto.Item {
	var first *proto.Item
	ended := false
	for i := range items {
		it := &items[i]
		switch {
		case it.TurnDuration != nil:
			ended = true
		case it.Role == "user" && it.Kind == proto.KindText && (first == nil || ended):
			first, ended = it, false
		}
	}
	return first
}

// sourceOf says whether a message of the bound conversation came from elsewhere than the app or voice
// (docs/extensions/channels.md "Source marks"), and the channel's name when it came by one: one Persona wrote (a scheduled task, a
// task reporting back, a wake-up) or another session sent is from elsewhere, not a channel. Bound to an agent nothing
// is (what is typed at its terminal is the user's).
func (t *tracker) sourceOf(ctx context.Context, it proto.Item) (foreign bool, from string) {
	if t.c.target == nil || t.c.target.Kind == "agent" {
		return false, ""
	}
	if it.Injected != nil || it.From != "" || it.Source != nil && it.Source.Kind != "" {
		return true, ""
	}
	head, _, ok := strings.Cut(it.ClientMsgID, "-")
	if !ok || !channel.ValidID(head) || head == channel.App || head == "voice" {
		return false, "" // the apps' own (a UUID), voice, or typed with no id
	}
	names := t.channelNames(ctx)
	if names == nil {
		return true, "" // the hub's channels could not be read: some channel's
	}
	name, ok := names[head]
	return ok, name
}

// channelNames are the hub's channels by id, their names in the call's language (nil when they could not be read).
func (t *tracker) channelNames(ctx context.Context) map[string]string {
	if t.obs.names != nil {
		return t.obs.names
	}
	list, err := t.c.pl.hub.channels(i18n.With(ctx, t.c.language()))
	if err != nil {
		t.c.logf("the hub's channels: %s", hubOutcome(err))
		return nil
	}
	t.obs.names = map[string]string{}
	for _, v := range list {
		t.obs.names[v.ID] = v.Name
	}
	return t.obs.names
}

// mention tells the model that a turn from elsewhere is over, once a call for each channel (voice/observed).
func (t *tracker) mention(r *voiceRun) {
	if t.obs.told == nil {
		t.obs.told = map[string]bool{}
	}
	if t.obs.told[r.from] {
		return
	}
	t.obs.told[r.from] = true
	t.c.tell(r, plainWords(prompts.RenderVoiceObserved(prompts.VoiceObserved{Lang: string(t.c.language()), To: t.c.target.to(),
		Channel: r.from})), true, false)
}

// say tells the model something of r that is not its result (it waits on the user, on something outside): said, not
// on its delegation; agent: it holds an agent's words (gate.go).
func (t *tracker) say(r *voiceRun, w words, agent bool) {
	r.lastSpoken = clock()
	t.c.tell(r, w, false, agent)
}

// heartbeat tells the user that r's conversation still works on it, once the call was quiet heartbeatAfter (since r
// began, since they last heard of it, since anyone spoke): the sort of step it is on and how long it has worked, a
// say that goes after everything else and is dropped once stale. Not of a held run (they were told it goes on in the
// background), nor of a turn from another channel.
func (t *tracker) heartbeat(r *voiceRun, s *proto.Session, now time.Time) {
	if r.Held || r.Observed && r.foreign || t.c.paused() { // none while the upstream is paused (suspend.go)
		return
	}
	if now.Sub(latest(r.Started, r.lastSpoken, t.c.heard())) < heartbeatAfter {
		return
	}
	r.lastSpoken = now
	from := r.Started
	if r.Observed && !s.StateSince.IsZero() && s.StateSince.Before(from) {
		from = s.StateSince // it worked before the call saw it
	}
	l := t.c.language()
	text := prompts.RenderVoiceProgress(prompts.VoiceProgress{Lang: string(l), To: t.c.target.to(), Do: "working",
		Activity: activityIn(l, s.Activity), Elapsed: channel.Duration(l, now.Sub(from))})
	t.c.logf("run %s: quiet for %v while it works: a heartbeat", r.ID, heartbeatAfter)
	t.c.deliver(&sentWords{words: plainWords(text), prio: prioHeartbeat, run: r.ID, expires: time.Now().Add(heartbeatStale)})
}

// activityIn is the sort of step an activity is (activityKind) in l, as the hub words it for the apps
// (hub-transcript_text): the node writes it in Chinese; "" for one the hub does not know (never the node's words in
// another language). Not the hub's own translation of the whole line: some languages put the command first.
func activityIn(l i18n.Lang, activity string) string {
	kind := strings.TrimSpace(activity)
	if key, _ := transcript.ActivityKey(kind); key == "" {
		kind = activityKind(kind) // "running npm test" in Chinese: "running" (a fixed line keeps its space: "waiting on CI")
	}
	if kind == "" || l == i18n.ZhHans || !i18n.Valid(l) {
		return kind
	}
	key, _ := transcript.ActivityKey(kind)
	if key == "" {
		return ""
	}
	return i18n.T(l, "hub-transcript_text."+key)
}

// triage is the hub's triage switches as they bear on the call's runs: off unless the call is bound to an agent (the
// hub triages only agents, not Persona's own conversations), and when they cannot be read (an older hub, its manager
// away: nothing triages then).
func (t *tracker) triage(ctx context.Context) triageSwitches {
	if t.c.target == nil || t.c.target.Kind != "agent" {
		return triageSwitches{}
	}
	if now := time.Now(); !now.Before(t.tri.at) {
		sw, err := t.c.pl.hub.triage(ctx)
		t.tri.sw, t.tri.at = sw, now.Add(triageEvery)
		if err != nil {
			t.c.logf("the hub's triage switches: %s (taken as off)", hubOutcome(err))
			t.tri.sw, t.tri.at = triageSwitches{}, now.Add(triageRetry)
		}
	}
	return t.tri.sw
}

// triaged is the hub's triage of the turn s stopped on (docs/assistant/agent-work.md "Triage"): nil until it is in (about
// triage_settle_seconds after the turn stopped, and only when the triage is on), and when it is of an earlier turn.
func triaged(s *proto.Session) *proto.WorkState {
	w := s.Work
	if w == nil || w.Phase == "" || w.Phase == proto.WorkWorking || !sameTurn(s, w) {
		return nil
	}
	return w
}

// sameTurn: w is about s's last turn (the hub's triage stamps it with the session's updated_at then, as its own
// sameTurn checks).
func sameTurn(s *proto.Session, w *proto.WorkState) bool {
	if w.TurnAt.IsZero() {
		return false
	}
	d := s.UpdatedAt.Sub(w.TurnAt)
	return d > -2*time.Second && d < 2*time.Second
}

// outside: the triage w says the turn waits on something outside (external: a deploy, CI, a review, a time) or on
// another agent finishing.
func outside(w *proto.WorkState) bool {
	return w != nil && w.Phase == proto.WorkBlocked && (w.On == proto.WaitExternal || w.On == proto.WaitAgent)
}

// untilTriaged: r's turn stopped, not on the user, and is not over yet (docs/assistant/voice.md "Following a turn" Look). The hub's triage
// of it says a "Continue" is due after a transient model error, or that it waits on something outside (told once, the run
// blocked); or that triage is not in yet and may say either: a model error the manager would go on from
// (auto_continue: Claude Code's say nothing of their kind, the triage tells), or a turn woken from a wait, which often
// stops on the same one. That wait is triageWait at most from when it was seen stopped; a "Continue" channel.QueuedMost.
func (t *tracker) untilTriaged(ctx context.Context, r *voiceRun, s *proto.Session, now time.Time) bool {
	tri, w := t.triage(ctx), triaged(s) // the switches read here are keepTold's too
	switch {
	case w != nil && w.Phase == proto.WorkBlocked && w.On == proto.WaitTransient:
		r.transient = r.transient || r.Seen
		return w.WakeAt != nil && now.Sub(r.stopAt) < channel.QueuedMost // none: the manager does not go on
	case outside(w) && r.Seen: // its own turn, not one before its message
		t.waitOutside(r, w)
		return true
	case w != nil:
		return false
	}
	if s.TurnErrorKind == proto.TurnErrorTransient && r.Seen { // not an earlier turn's
		r.transient = true
	}
	switch {
	case !tri.Enabled || now.Sub(r.stopAt) >= triageWait:
		return false
	case r.waitOn != "":
		return true // woken from a wait
	}
	return tri.AutoContinue && strings.TrimSpace(s.TurnError) != "" && s.TurnErrorKind != proto.TurnErrorUser
}

// keepTold: r's answer was just said (state, res); it is kept until the hub's triage of that turn is in, which may
// say the turn waits on something outside (afterTold). Only an agent's runs (the triage's), when it is on.
func (t *tracker) keepTold(r *voiceRun, res *channel.Result, state string) bool {
	return state == runDone && !res.Held && !res.Quiet && res.End == channel.EndDone && !r.foreign && t.tri.sw.Enabled &&
		t.c.target != nil && t.c.target.Kind == "agent"
}

// afterTold looks at r, whose answer was said, against its conversation (s, nil when it is not listed) until the
// hub's triage of that turn is in: it waits on something outside, and the run goes on, blocked (said once), its later
// turns the call's; the triage said anything else, it never said, or another turn began (the user at its terminal, a
// hook): the run is over, and what comes is the observer's.
func (t *tracker) afterTold(r *voiceRun, s *proto.Session, now time.Time) {
	within := now.Sub(r.stopAt) < triageWait
	switch {
	case s == nil || waitingOnUser(s):
	case working(s):
		if s.Rev == r.toldRev && within {
			return // nothing written since it was read: its state behind its transcript
		}
	case outside(triaged(s)):
		t.c.logf("run %s: the hub's triage says its turn waits on something outside: followed on", r.ID)
		r.told = false
		t.waitOutside(r, triaged(s))
		return
	case triaged(s) == nil && within:
		return
	}
	t.drop(r)
}

// waitOutside: r's turn stopped to wait on something outside (w). The run is blocked; the user hears what it waits on
// once a wait (voice/progress#waiting), and it goes on being followed: the manager wakes the agent, and its answer is
// told when it is done.
func (t *tracker) waitOutside(r *voiceRun, w *proto.WorkState) {
	t.set(r, runBlocked)
	wait := clipRunes(oneLine(firstNonEmpty(w.Wait, w.Line)), 200)
	agent := ""
	if w.Agent != nil {
		agent = clipRunes(oneLine(w.Agent.Label), 80)
	}
	if key := w.On + "\x00" + wait + "\x00" + agent; r.waitOn != key {
		r.waitOn = key
		t.say(r, plainWords(prompts.RenderVoiceProgress(prompts.VoiceProgress{Lang: string(t.c.language()), To: t.c.target.to(),
			Do: "waiting", On: w.On, Wait: wait, Agent: agent})), wait != "" || agent != "")
	}
}

// failureOf is the sort of model error s's last turn ended on (prompts.VoiceReport.Failure), "" none: what is wrong
// when the hub can tell, never the error's text (it may say what the service runs on). A transient one is retried
// when a "Continue" was seen and it failed again.
func (r *voiceRun) failureOf(s *proto.Session) string {
	if strings.TrimSpace(s.TurnError) == "" {
		return ""
	}
	switch s.TurnErrorCode {
	case proto.TurnErrorNotLoggedIn, proto.TurnErrorAuthFailed, proto.TurnErrorModelRoute, proto.TurnErrorRateLimited:
		return s.TurnErrorCode
	}
	switch {
	case !r.transient && s.TurnErrorKind != proto.TurnErrorTransient:
		return "error"
	case r.continued:
		return "retried"
	}
	return "transient"
}

// withoutError is a turn's answer without the model error it ends on: Claude Code writes the error as its last words
// (the hub's TurnError is read off them, cut), a part of the answer of its own.
func withoutError(answer, turnError string) string {
	head := []rune(strings.TrimSuffix(strings.TrimSpace(turnError), "…"))
	if len(head) == 0 {
		return answer
	}
	head = head[:min(len(head), 40)]
	i := strings.LastIndex(answer, string(head))
	if i < 0 || i > 0 && !strings.HasSuffix(answer[:i], "\n\n") {
		return answer
	}
	return strings.TrimSpace(answer[:i])
}
