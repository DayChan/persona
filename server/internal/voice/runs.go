package voice

import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A task the voice model hands over is a run (docs/assistant/voice.md "Turn tracking"): one channel.Run, as the assistant's channels
// have, found in the conversation by its message's client_msg_id (voice-<random>) or its first line, followed until
// that turn is over and then read whole (channel.ReadTurn over the hub's HTTP API), so what the voice model hears is
// that turn's whole answer: not the conversation's last 200 characters, not another turn's. Its words go back on the
// delegation it came from (that delegation's one commentary) while that is still open, else as something to say.
//
// The target is the conversation a protocol 2 call is bound to (target.go), else the Persona main conversation of the
// moment (protocol 1). Each conversation has a tracker: one goroutine that owns its runs and checks them when the hub
// link says their conversation changed, and every 5 s regardless. On protocol 2 the app follows each run by its run
// frames (docs/assistant/voice.md "Extension → App").

// voiceRun is one handed-over task being followed.
type voiceRun struct {
	channel.Run
	Tasks []string // what was asked, oldest first (a run that folded earlier ones answers them too)
	// Delegation is the upstream delegation its words go back on, of upstream session Gen; "" once it had its one
	// commentary (a held run's second answer is said).
	Delegation string
	Gen        int
	appIDs     []string // the app's  "Persona main chat working" lines that end with it

	checked  time.Time // its answer was last read
	notFound bool      // that read did not find its message
	ended    bool      // the hub said a turn ended there since
	waiting  bool      // it was seen waiting on the user (until it works again)
	rev      int64     // the conversation's transcript before the send (proto.Session.Rev)
	// owed: it waits on the user and no card of it was seen since (a card says itself, asks.go); past cardGrace
	// without one the turn says it waits (report needs_you). Zero once a card is seen or that is said.
	owed time.Time

	// task is its own task (Tasks has the folded ones' too); message what was sent (a resend sends it again).
	task, message string
	// State is the run frame's (protocol 2), with why it was refused or failed: Code, Message (the hub's words),
	// Draft and Clearable (409 input_draft).
	State, Code, Message, Draft, Clearable string
	// unsent: the app handed it back while it was being sent: past channel.QueuedMost without its message in the
	// conversation it is unknown, not answered with the conversation's last words (docs/assistant/voice.md "Reconnecting and resuming").
	unsent bool
	// foldedCalls is how many earlier runs folded into it handed work over on calls that get no answer of their own.
	foldedCalls int
	framed      int // the base_seq its last run frame said
	framedIDs   int // how many msg_ids it said

	// Observed: not handed over by voice: a turn the bound conversation began otherwise, followed and told as the
	// voice's are (observe.go); it has no run frames. foreign: on a Persona conversation, the turn came from elsewhere
	// than the app or voice (from: the channel's name, "" not a channel): one line of it, never its words.
	Observed bool
	foreign  bool
	from     string
	// stopAt: its turn was first seen stopped (not on the user) since it last worked, the transcript then stopRev;
	// transient: a turn of it stopped on a transient model error, continued: it worked again after one (a "Continue"); waitOn
	// what it was last told it waits on outside (said once a wait); lastSpoken when the user last heard of it; failure
	// the sort of model error its turn ended on (prompts.VoiceReport.Failure).
	stopAt     time.Time
	stopRev    int64
	transient  bool
	continued  bool
	waitOn     string
	lastSpoken time.Time
	failure    string
	// told: its answer was said and it is kept until the hub's triage of that turn is in (observe.go afterTold); toldRev
	// the conversation's transcript then (proto.Session.Rev), lastRev at its last check.
	told             bool
	toldRev, lastRev int64
}

// The run frame's states (docs/assistant/voice.md "Extension → App").
const (
	runSending  = "sending"
	runSent     = "sent"
	runWorking  = "working"
	runNeedsYou = "needs_you"
	runBlocked  = "blocked"
	runHeld     = "held"
	runDone     = "done"
	runFailed   = "failed"
	runRefused  = "refused"
	runUnknown  = "unknown"
	runFolded   = "folded"
)

// set moves r to state, telling the app (protocol 2) and the next session's instructions (what is still open). An
// observed run is the voice's to tell, not the app's.
func (t *tracker) set(r *voiceRun, state string) {
	t.c.runState(r, state)
	if r.Observed {
		r.State = state
		return
	}
	open := ""
	if openState(state) {
		open = r.task
	}
	t.c.setOpen(r.ID, open)
	if r.State == state && r.Base == r.framed && len(r.MsgIDs) == r.framedIDs {
		return // a hold moves the base on, a fold adds msg_ids: the app hands them back on a resume
	}
	r.State, r.framed, r.framedIDs = state, r.Base, len(r.MsgIDs)
	t.c.runFrame(r)
}

// runFrame tells the app how a run stands (protocol 2 only).
func (c *conversation) runFrame(r *voiceRun) {
	if c.version < 2 {
		return
	}
	f := map[string]any{"type": "run", "id": r.ID, "task": r.task, "state": r.State, "msg_ids": append([]string{}, r.MsgIDs...),
		"base_seq": r.Base, "started_at": stamp(r.Started)}
	// clearable is the hub's own (only a one-line draft has it): what the app hands back as clear_draft (resend), as
	// every client does; draft is all the box holds, an IDE's selection marker too.
	for k, v := range map[string]string{"code": r.Code, "message": r.Message, "draft": r.Draft, "clearable": r.Clearable} {
		if v != "" {
			f[k] = v
		}
	}
	c.sendApp(f)
}

const (
	taskMost     = 80 // characters of a task the voice model is reminded of
	checkTimeout = time.Minute
)

var (
	lookEvery = 5 * time.Second  // a run is checked this often whatever the hub link says
	readEvery = 10 * time.Second // a run not seen working, or whose message was not there, is read this often
	clock     = time.Now         // what a check takes as now (a test moves it on)
)

type tracker struct {
	c     *conversation
	link  *linkListener
	ops   chan func()
	runs  []*voiceRun
	last  time.Time             // the last check of everything
	ended map[linkKey]time.Time // when the hub last said a turn ended there (one may end while its message is sent)
	// refused are the runs the hub refused (409), by id, that the app may send again (resend).
	refused map[string]*voiceRun
	// stopAsked is when the voice side last asked the bound conversation to stop (stopTarget, unix ns): a run read as
	// stopped after that ends quietly, as stopped() ends it: the model already said it was stopped.
	stopAsked atomic.Int64
	asks      *asker        // the cards waiting in the call's conversations (asks.go)
	every     time.Duration // lookEvery as the call began
	obs       observer      // the bound conversation's turns the call did not hand over (observe.go)
	tri       triageView    // the hub's triage switches (observe.go)
	// busy: the bound conversation worked as it was last looked at (suspend.go).
	busy bool
}

const refusedMost = 20

func newTracker(c *conversation, link *linkListener) *tracker {
	t := &tracker{c: c, link: link, ops: make(chan func(), 16), ended: map[linkKey]time.Time{}, refused: map[string]*voiceRun{},
		every: lookEvery}
	t.asks = newAsker(t, c.role == "" || c.role == roleFull)
	return t
}

// loop owns the runs until the conversation ends: then they are dropped (they are not kept anywhere; what was sent goes
// on in its conversation as typed).
func (t *tracker) loop(ctx context.Context) {
	defer func() {
		for _, r := range t.runs {
			t.link.unwatch(linkKey{r.MachineID, r.SessionID})
		}
		t.runs = nil
		t.link.close()
	}()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		due := false
		select {
		case <-ctx.Done():
			return
		case op := <-t.ops:
			op()
		case ev := <-t.link.events:
			t.event(ev)
			t.asks.event(ev)
			due = true
		case now := <-tick.C:
			dirty := t.link.dirty.Swap(false)
			due = dirty || now.Sub(t.last) >= t.every
			t.asks.due = t.asks.due || dirty
		}
		if due {
			t.last = time.Now()
			if len(t.runs) > 0 {
				t.checkAll(ctx)
			}
			t.observe(ctx)
			t.c.setWorks(t.works()) // the upstream may pause while it works (suspend.go)
		}
		t.asks.tick(ctx) // on its own clock (asks.go): an op may have made it due
	}
}

// event marks the runs a turn ended on: they are read at once, not on readEvery.
func (t *tracker) event(ev linkEvent) {
	if !ev.ended {
		return
	}
	t.ended[ev.key] = time.Now()
	for _, r := range t.runs {
		if ev.key == (linkKey{r.MachineID, r.SessionID}) {
			r.ended, r.Seen = true, true
		}
	}
}

func (t *tracker) checkAll(ctx context.Context) {
	for _, r := range slices.Clone(t.runs) {
		if ctx.Err() != nil {
			return
		}
		t.check(ctx, r)
	}
}

// adopt starts following r; the runs not yet answered on the same conversation are folded into it (the user said more
// while it worked): one answer covers them, in the order the conversation took the messages. An observed run there
// (a turn the conversation began otherwise) is folded too when its words would have been said; one from another
// channel is let go (its answer went back there), as is one whose answer was said (told).
func (t *tracker) adopt(r *voiceRun) {
	keep := t.runs[:0]
	for _, o := range t.runs {
		if o.MachineID != r.MachineID || o.SessionID != r.SessionID {
			keep = append(keep, o)
			continue
		}
		if o.told || o.Observed && o.foreign {
			t.link.unwatch(linkKey{o.MachineID, o.SessionID})
			continue
		}
		r.Fold(&o.Run)
		if !o.Held {
			r.Tasks = append(slices.Clone(o.Tasks), r.Tasks...)
		}
		r.foldedCalls += o.foldedCalls
		if o.Delegation != "" {
			r.foldedCalls++ // its call gets no answer of its own: r's answer covers it
		}
		r.appIDs = append(o.appIDs, r.appIDs...)
		t.link.unwatch(linkKey{o.MachineID, o.SessionID}) // r holds its own watch
		t.c.logf("run %s: folded into run %s", o.ID, r.ID)
		t.set(o, runFolded)
	}
	t.runs = append(keep, r)
	t.set(r, firstNonEmpty(r.State, runSent))
	if at, ok := t.ended[linkKey{r.MachineID, r.SessionID}]; ok && !at.Before(r.Started) {
		r.ended, r.Seen = true, true // a turn ended while its message was being sent
	}
	t.last = time.Time{} // look at it on the next tick
}

func (t *tracker) drop(r *voiceRun) {
	t.runs = slices.DeleteFunc(t.runs, func(o *voiceRun) bool { return o == r })
	t.link.unwatch(linkKey{r.MachineID, r.SessionID})
	t.c.setOpen(r.ID, "")
}

func working(s *proto.Session) bool { return s.Status == proto.StatusWorking || s.Starting }

// waitingOnUser: its turn is open, stopped on the user (a permission, a plan, a question, a dialog on the screen).
func waitingOnUser(s *proto.Session) bool {
	return s.Status == proto.StatusBlocked || s.State == proto.StateNeedsYou || s.PendingInteractions > 0
}

// atWork: it works on its own, not stopped on the user. A headless conversation (the main one) stays working while a
// permission card waits on its hook, as does a Claude Code question asked through its hook (#5500): that is a wait on
// the user, not work to report as such (the run's state, a heartbeat, a status, a pause).
func atWork(s *proto.Session) bool { return working(s) && !waitingOnUser(s) }

// cardGrace is how long a turn seen waiting on the user may go without a card before it says so itself: a dialog on
// the screen is made a card by the node's reading of it, after the session already shows it blocked (1.5 s measured,
// #5500), and that card says itself (asks.go).
var cardGrace = 5 * time.Second

// check is channel.Run's rules against the conversation now (the assistant's checkChannelRun, over HTTP): wait, read
// the answer, or give up; an answer, a stop or a card waiting for the user is told to the voice model.
func (t *tracker) check(ctx context.Context, r *voiceRun) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	hub, l := t.c.pl.hub, t.c.language()
	sess, err := hub.session(ctx, r.MachineID, r.SessionID)
	if err != nil {
		return // the hub could not be asked: the next look
	}
	t.saw(linkKey{r.MachineID, r.SessionID}, sess)
	now := clock()
	if r.told {
		t.afterTold(r, sess, now)
		return
	}
	look := channel.Look{Now: now, Exists: sess != nil, Lang: l}
	onUser := false // stopped on the user, whether or not it is listed working (busy)
	if sess != nil {
		r.lastRev = sess.Rev
		look.Working, look.Offline, look.Background = working(sess), sess.Status == proto.StatusOffline, channel.Background(*sess)
		onUser = waitingOnUser(sess)
		if !look.Working {
			look.Waiting = onUser
			look.Failed = !look.Waiting && strings.TrimSpace(sess.TurnError) != ""
		}
		if !r.Seen && !look.Working && sess.Rev != 0 && sess.Rev != r.rev {
			// Its transcript moved since the send: a short turn may have come and gone between two looks (no hub link
			// to say so). Reading finds out: a message not answered yet reads as not found.
			r.Seen = true
		}
	} else {
		look.Online, _ = hub.machineOnline(ctx, r.MachineID)
	}
	switch {
	case onUser:
		if !r.waiting {
			r.waiting, r.owed = true, now // once until it works again: the user answers on the screen or the phone
			t.set(r, runNeedsYou)
		}
		if !r.owed.IsZero() {
			key := linkKey{r.MachineID, r.SessionID}
			switch {
			case t.asks.covers(ctx, key): // a card waiting says itself (asks.go)
				r.owed = time.Time{}
			case !t.asks.inScope(key) || now.Sub(r.owed) >= cardGrace: // no card says itself here, or none came
				r.owed = time.Time{}
				t.say(r, plainWords(prompts.RenderVoiceReport(prompts.VoiceReport{Lang: string(l), To: t.c.target.to(), Status: "needs_you",
					Tasks: r.Tasks, Observed: r.Observed})), false)
			}
		}
	case look.Working:
		r.waiting, r.owed, r.continued, r.stopAt = false, time.Time{}, r.continued || r.transient, time.Time{}
		if !r.Held {
			t.set(r, runWorking)
			t.heartbeat(r, sess, now)
		}
	}
	step := r.Check(look)
	switch step.Do {
	case channel.Wait:
		return
	case channel.Finish:
		t.finish(r, &channel.Result{Note: step.Note, Quiet: step.Note == ""}, look)
		return
	}
	// Stopped, and not on the user: a turn that may not be over yet, as the hub's triage of it says (observe.go
	// untilTriaged, docs/assistant/voice.md "Turn tracking" Look).
	if sess != nil && !look.Working && !look.Waiting {
		if r.stopAt.IsZero() || sess.Rev != r.stopRev {
			// Stopped, or stopped again: a turn may come and go between two looks (woken, gone on from) with its
			// transcript the only sign.
			r.continued = r.continued || r.transient && !r.stopAt.IsZero()
			r.stopAt, r.stopRev = now, sess.Rev
		}
		if t.untilTriaged(ctx, r, sess, now) {
			return
		}
		r.failure = r.failureOf(sess)
	}
	// The end was not seen (it came between looks, or never), or the message was not there last time: read now and
	// then, not every look.
	if (!r.Seen || r.notFound) && !r.ended && now.Sub(r.checked) < readEvery {
		return
	}
	r.checked, r.ended = now, false
	src := httpItems{hub: hub, machine: r.MachineID, session: r.SessionID}
	rd, err := channel.ReadTurn(ctx, src, &r.Run)
	if err == nil && !rd.Found && r.Base > 0 {
		// The transcript ends before the base (rewound, rewritten): find the message from the start.
		if last, lerr := hub.lastSeq(ctx, r.MachineID, r.SessionID); lerr == nil && last < r.Base {
			t.c.logf("run %s: the conversation ends at %d, before its base %d: reading it from the start", r.ID, last, r.Base)
			r.Base = 0
			rd, err = channel.ReadTurn(ctx, src, &r.Run)
		}
	}
	if err != nil {
		t.c.logf("run %s: read the answer: %s", r.ID, hubOutcome(err))
		return
	}
	if sess != nil && sess.Rev != 0 {
		// The transcript moved on while it was read: the look (working, its background work) may be from before what
		// was read, a whole turn between the two (#5060: its answer read as done while its background work went on).
		// Look again at once.
		if again, err := hub.session(ctx, r.MachineID, r.SessionID); err == nil && again != nil && again.Rev != sess.Rev {
			r.ended, t.last = true, time.Time{}
			return
		}
	}
	r.notFound = !rd.Found
	if rd.Found {
		r.unsent = false
	}
	if !rd.Found {
		if now.Sub(r.Started) < channel.QueuedMost {
			return // it waits behind another turn: it starts the next one
		}
		if r.unsent {
			// Handed back while it was being sent, and never showed: it may or may not have got there.
			t.c.logf("run %s: handed back while sending, its message never showed: unknown", r.ID)
			t.drop(r)
			t.notSent(r, sendFail{state: runUnknown, code: "unknown", prompt: "unknown"})
			return
		}
		// The message never reached the conversation even past QueuedMost. Reading the whole tail would answer with
		// the previous turn's last words, and dropping the run would leave the real answer unclaimed: give up and
		// ask the user to say it again instead (#4513, as the assistant's channels do since #4455).
		t.c.logf("run %s: its message never showed in the conversation: asking to say it again", r.ID)
		t.finish(r, &channel.Result{Note: i18n.T(look.Lang, "voice-runs.not_delivered"), End: channel.EndGaveUp}, look)
		return
	}
	if r.failure != "" && sess != nil {
		rd.Answer = withoutError(rd.Answer, sess.TurnError) // its sort is said, not its text
	}
	if rd.Stopped == "" && !look.Working && sess != nil {
		rd.Stopped = channel.StopReason(sess.TurnError) // a Codex turn the model service failed writes no line of it
	}
	if res := r.Decide(rd, look); res != nil {
		if r.Observed {
			t.placeObserved(ctx, r)
		}
		t.finish(r, res, look)
	}
}

// finish tells the voice model a run's result: its answer, or why there is none. A held one (its background work goes
// on) is answered now and followed on for the turn that work starts. An observed run from another channel is only
// mentioned (observe.go); one not followed to its end, or that ended without words or a reason, is let go without a
// word (nobody asked for it here). An agent's run whose answer was said is kept until the hub's triage of that turn is
// in (keepTold): it may wait on something outside, and its run go on.
func (t *tracker) finish(r *voiceRun, res *channel.Result, look channel.Look) {
	l, to := string(t.c.language()), t.c.target.to()
	t.c.toolsDone(r.appIDs)
	r.appIDs = nil
	t.obs.read(r, res.Last)
	if res.End == channel.EndStopped {
		r.Code = "stopped" // the app shows it as stopped, not done
		if at := t.stopAsked.Load(); at != 0 && strings.TrimSpace(res.Answer) == "" && !time.Unix(0, at).Before(r.Started.Add(-time.Second)) {
			t.drop(r) // the voice side stopped it: what the model was told of the stop is its end (stopped)
			t.set(r, runDone)
			return
		}
	}
	state, empty, told := runDone, strings.TrimSpace(res.Answer) == "", true
	// The model service failed it: what sort of failure, never its text (docs/assistant/voice.md "Turn tracking").
	failed := res.End == channel.EndError && !res.Held && r.failure != ""
	switch {
	case res.Quiet:
		told = false // nothing to say: a held run whose background work ended without words
	case r.Observed && r.foreign:
		t.mention(r)
		res.Held = false // its answer went back to its channel, and so will the next
	case r.Observed && empty && !res.Held && (res.Note != "" || res.Stopped == "" && res.End != channel.EndError):
		told = false
	case res.Note != "" && empty && !res.Held:
		state = runFailed
		t.c.tell(r, plainWords(prompts.RenderVoiceReport(prompts.VoiceReport{Lang: l, To: to, Status: "failed", Tasks: r.Tasks,
			Folded: r.foldedCalls > 0, Reason: res.Note})), true, false)
	case failed && empty:
		state = runFailed
		t.c.tell(r, plainWords(prompts.RenderVoiceReport(prompts.VoiceReport{Lang: l, To: to, Status: "failed", Tasks: r.Tasks,
			Folded: r.foldedCalls > 0, Failure: r.failure, Observed: r.Observed})), true, false)
	default:
		rep := prompts.VoiceReport{Lang: l, To: to, Status: "done", Tasks: r.Tasks, Folded: r.foldedCalls > 0, Stopped: res.Stopped, Again: r.Held,
			Observed: r.Observed}
		if res.Held {
			rep.Background = look.Background
		}
		if failed {
			state, rep.Failure = runFailed, r.failure // what it wrote before the model service failed it
		}
		t.c.tell(r, reportWords(i18n.Lang(l), rep, res.Answer), true, true)
	}
	if res.Held {
		r.Hold(res.Last, time.Now())
		r.Delegation, r.waiting, r.foldedCalls = "", false, 0
		t.set(r, runHeld) // with its new base
		return
	}
	if told && t.keepTold(r, res, state) {
		r.told, r.toldRev = true, r.lastRev
		t.set(r, state)
		return
	}
	t.drop(r)
	t.set(r, state)
}

// handOver sends task to the main conversation and follows the run it starts, in the background. delegation is where
// its words go back (of upstream session gen); appID the app's line for it.
func (t *tracker) handOver(task, delegation string, gen int, appID string, history []prompts.VoiceLine) {
	c := t.c
	l := c.language()
	r := &voiceRun{task: clipRunes(task, taskMost), Delegation: delegation, Gen: gen, appIDs: []string{appID}}
	r.Tasks = []string{r.task}
	r.ID, r.Channel, r.Probe, r.Started = appID, "voice", channel.Probe(task), time.Now()
	message := prompts.RenderVoiceDelegated(prompts.VoiceDelegated{Lang: string(l), Task: task, History: history})
	c.runState(r, runSending)
	go func() {
		if err := t.send(c.ctx, r, message); err != nil {
			if c.ctx.Err() != nil {
				return
			}
			code, reason := sendFailure(err)
			r.Code = code
			if code == "unknown" {
				c.runState(r, runUnknown)
			} else {
				c.runState(r, runFailed)
			}
			c.toolsDone(r.appIDs)
			c.tell(r, plainWords(prompts.RenderVoiceRefused(prompts.VoiceRefused{Lang: string(l), Code: code, Task: r.Tasks[0], Reason: reason})), true, false)
			return
		}
		select {
		case t.ops <- func() { t.adopt(r) }:
		case <-c.ctx.Done():
			t.link.unwatch(linkKey{r.MachineID, r.SessionID})
		}
	}()
}

// send puts the message into the main conversation (starting one when there is none) and fills in where r's answer
// will be: the conversation, its last line before the message, the message's id. The run's conversation is watched
// from before the send on, so its turn's end is not missed.
func (t *tracker) send(ctx context.Context, r *voiceRun, message string) error {
	hub := t.c.pl.hub
	machine, main, err := hub.assistant(ctx)
	if err != nil {
		t.c.logf("run %s: where the main conversation is: %s", r.ID, hubOutcome(err))
		return err
	}
	if main == "" {
		began := time.Now()
		sess, err := hub.startMain(ctx, message)
		t.c.logf("run %s: started a main conversation on %s: %s in %v", r.ID, machine, hubOutcome(err), logDur(time.Since(began)))
		if err != nil {
			return maybeSent(err)
		}
		r.MachineID, r.SessionID = firstNonEmpty(sess.MachineID, machine), sess.SessionID
		t.link.watch(linkKey{r.MachineID, r.SessionID}, 0)
		return nil // a new conversation: the message is its first line (found by the probe)
	}
	r.MachineID, r.SessionID = machine, main
	key := linkKey{machine, main}
	sess, _ := hub.session(ctx, machine, main)
	if sess != nil {
		r.rev = sess.Rev
	}
	base, err := hub.lastSeq(ctx, machine, main)
	if err != nil {
		t.c.logf("run %s: the conversation's last line: %s (its message is found from the start)", r.ID, hubOutcome(err))
	}
	r.Base = base
	t.link.watch(key, base)
	id := voiceMessageID()
	sctx, cancel := context.WithTimeout(ctx, sendTimeout(sess))
	defer cancel()
	began := time.Now()
	err = hub.send(sctx, machine, main, message, id)
	t.c.logf("run %s: sent to %s/%s as %s: %s in %v", r.ID, machine, main, id, hubOutcome(err), logDur(time.Since(began)))
	if err != nil {
		t.link.unwatch(key)
		return maybeSent(err)
	}
	r.MsgIDs = []string{id}
	return nil
}

// words is what the model is told: text, and short for a service that holds a commentary to an append's tokens (it
// refuses text, and short is sent instead: session.go, deliver).
type words struct{ text, short string }

func plainWords(text string) words { return words{text: text, short: clipTokens(text, appendTokens)} }

// reportWords is a done report with as much of the turn's answer, made speakable, as fits: in a commentary
// (commentaryRunes, with the report's own words and the tasks around it) and, for short, in an append's tokens. A cut
// answer keeps its start and its end (clipMiddle), so the block around it is always whole.
func reportWords(l i18n.Lang, rep prompts.VoiceReport, answer string) words {
	whole := speakable(l, answer)
	render := func(most int) string {
		rep.Answer = clipMiddle(l, whole, most)
		return prompts.RenderVoiceReport(rep)
	}
	top := min(spokenMost, runeLen(whole))
	short := fit(render, top, func(s string) bool { return tokens(s) <= appendTokens })
	return words{
		text:  fit(render, top, func(s string) bool { return runeLen(s) <= commentaryRunes }),
		short: clipTokens(short, appendTokens), // the tasks alone may be more
	}
}

// fit is render(most) for the largest most up to top whose text is ok (render(0) when none is).
func fit(render func(most int) string, top int, ok func(string) bool) string {
	if s := render(top); ok(s) {
		return s
	}
	best := render(0)
	for lo, hi := 1, top-1; lo <= hi; {
		mid := (lo + hi) / 2
		if s := render(mid); ok(s) {
			best, lo = s, mid+1
		} else {
			hi = mid - 1
		}
	}
	return best
}
