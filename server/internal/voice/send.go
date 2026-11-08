package voice

import (
	"context"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Handing work to the bound conversation (protocol 2, docs/assistant/voice.md "Sending, the gate and 409"). A delegation is sent at once,
// as the model made it, once the user-speech gate lets it (gate.go); what it starts with decides what it is:
//   - "Status:" asks how the conversation stands: answered from its live state (status.go), never sent, no gate;
//   - "Stop:" stops it (the app's "Stop" button), behind the gate;
//   - while a question of it is asked by voice, anything else is the answer (asks.go), behind the gate;
//   - "Answer:" otherwise is the user's answer to what it asked in words: sent without the mark;
//   - anything else is an instruction, sent as it is: refused by the hub (409) it is told why and the app may send it
//     again (resend); while a card waits, a word that only approves or refuses is not sent (cards are tapped).
// A run that the hub took (200) folds the ones not answered yet on the same conversation (tracker.adopt): one refused
// or not known to have been sent is never folded (its message would be waited for in vain).

// leadMarks are the marks a delegation starts with, in the three languages and either colon.
var leadMarks = []struct {
	kind  string
	words []string
}{
	{"status", []string{"状态", "狀態", "状況", "status"}},
	{"stop", []string{"停止", "stop"}},
	{"answer", []string{"回答", "answer"}},
}

// openQuotes and closeQuotes are what a model may wrap a mark, or the whole task, in: the instructions write the marks
// in full-width corner brackets (around "Status:"), and a model may copy them.
const (
	openQuotes  = "「『\"“'‘"
	closeQuotes = "」』\"”'’"
)

// splitMark is what a delegation's text starts with ("" none) and the rest of it. The mark may be quoted (corner brackets around mark and colon,
// "Stop: …", brackets around the mark only with a full-width colon after) and have a space before its colon.
func splitMark(task string) (kind, rest string) {
	t := strings.TrimSpace(task)
	s := strings.TrimLeft(t, openQuotes)
	for _, m := range leadMarks {
		for _, w := range m.words {
			if len(s) <= len(w) || !strings.EqualFold(s[:len(w)], w) {
				continue
			}
			after := strings.TrimLeft(strings.TrimLeft(s[len(w):], " \t"), closeQuotes)
			if r, size := utf8.DecodeRuneInString(after); r == ':' || r == '：' {
				rest = strings.TrimSpace(after[size:])
				if len(s) < len(t) { // quoted from the start: its closer is not part of the rest
					rest = strings.TrimSpace(strings.TrimRight(rest, closeQuotes))
				}
				return m.kind, rest
			}
		}
	}
	return "", t
}

// The cards an approval word answers: tapped on the screen, never by voice (docs/assistant/voice.md decision 5). Not an
// elicitation: the hub lists one only to a client that said it renders them (hub/elicitation.go), never to the
// extension's hub token; a form in the terminal holds its keyboard, and the node refuses the send (dialog_open).
var approvalCards = []string{proto.IxPlan, proto.IxPermission, proto.IxScreen}

// handOverBound is protocol 2's handover (mu held; it goes on in the background). name is what the log calls it.
func (c *conversation) handOverBound(name, task, delegation string, gen int, at time.Time, reply string) {
	kind, rest := splitMark(task)
	switch kind {
	case "status":
		c.logf("%s: a status (not gated)", name)
		go c.answerStatus(name, delegation, gen)
		return
	case "stop":
		c.logf("%s: a stop", name)
		c.whenSpoken(name, at, func(spoken bool) {
			if !spoken {
				c.tell(&voiceRun{Delegation: delegation, Gen: gen}, plainWords(prompts.RenderVoiceRefused(prompts.VoiceRefused{
					Lang: string(c.language()), Code: "unprompted", Task: clipRunes(task, taskMost), To: c.target.to()})), true, false)
				return
			}
			c.stopTarget(name, delegation, gen)
		})
		return
	}
	if card := c.askingCardLocked(); card != "" { // a question asked by voice: anything else is its answer (asks.go)
		c.logf("%s: the answer to card %s (%s)", name, card, taskSize(task))
		c.answerCard(name, card, task, delegation, gen, at)
		return
	}
	what := "a task"
	if kind == "answer" {
		task, what = rest, "an answer in words"
	}
	c.logf("%s: %s for %s (%s)", name, what, c.target.logName(), taskSize(task))
	c.whenSaid(name, at, reply, func(spoken bool) {
		c.mu.Lock()
		if task == "" { // the model said nothing of what to do: what the user said
			task = c.lastUserLine()
		}
		recent := c.recent(recentLines)
		c.mu.Unlock()
		r := c.newBoundRun(task, delegation, gen)
		c.logf("run %s: for %s, in session %d", r.ID, name, sessionNo(gen))
		switch {
		case !spoken:
			c.runs.notSent(r, sendFail{state: runRefused, code: "unprompted", prompt: "unprompted"})
		case task == "":
			c.runs.notSent(r, sendFail{state: runRefused, code: "empty", prompt: "empty"})
		default:
			r.message = c.message(task, recent)
			c.runs.sendBound(r, sendOpts{}, true)
		}
	})
}

// newBoundRun is a run of task to the bound conversation; its words go back on delegation, of upstream session gen.
// Its id is its message's client_msg_id (a resend keeps it).
func (c *conversation) newBoundRun(task, delegation string, gen int) *voiceRun {
	r := &voiceRun{task: clipRunes(task, taskMost), Delegation: delegation, Gen: gen}
	r.Tasks = []string{r.task}
	r.ID = voiceMessageID()
	r.Channel, r.Probe, r.Started, r.MsgIDs = "voice", channel.Probe(task), time.Now(), []string{r.ID}
	r.MachineID, r.SessionID = c.target.MachineID, c.target.SessionID
	return r
}

// message is what goes to the bound conversation for task: an agent gets voice/agent_message, Persona's own
// conversations voice/delegated.
func (c *conversation) message(task string, recent []prompts.VoiceLine) string {
	l := string(c.language())
	if c.target.Kind == "agent" {
		return prompts.RenderVoiceAgentMessage(prompts.VoiceAgentMessage{Lang: l, Task: task, History: recent})
	}
	return prompts.RenderVoiceDelegated(prompts.VoiceDelegated{Lang: l, Task: task, History: recent})
}

// cardsWaiting says whether a card of one of kinds waits in the bound conversation (false when the hub cannot say).
func (c *conversation) cardsWaiting(ctx context.Context, kinds ...string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ixs, err := c.pl.hub.interactions(ctx)
	if err != nil {
		return false
	}
	return slices.ContainsFunc(ixs, func(ix proto.Interaction) bool {
		return ix.MachineID == c.target.MachineID && ix.SessionID == c.target.SessionID && slices.Contains(kinds, ix.Kind)
	})
}

// sendFail is why a run was not sent: its run frame's state and code, the hub's words, a 409's details; prompt is
// voice/refused's code; resendable: the app may send it again (a refusal of the hub's).
type sendFail struct {
	state, code, reason, prompt string
	draft, clearable            string
	plan, resendable            bool
}

// boundFailure is what a failed send to the bound conversation was (docs/assistant/voice.md "How refusals map").
func boundFailure(err error) sendFail {
	var he *hubError
	var ms maybeSentError
	switch {
	case errors.As(err, &ms):
		return sendFail{state: runUnknown, code: "unknown", prompt: "unknown"}
	case !errors.As(err, &he):
		return sendFail{state: runFailed, code: "unreachable", prompt: "unreachable"}
	case he.Delivered == "unknown", he.Message == "" && (he.Status == http.StatusBadGateway || he.Status == http.StatusGatewayTimeout):
		return sendFail{state: runUnknown, code: "unknown", prompt: "unknown"}
	case he.Status == http.StatusConflict:
		f := sendFail{state: runRefused, code: firstNonEmpty(he.Code, "conflict"), reason: he.Message, prompt: "failed", resendable: true}
		switch he.Code {
		case "dialog_open", "input_draft":
			f.prompt = he.Code
			f.draft, f.clearable = he.Draft, he.Clearable
		}
		return f
	}
	return sendFail{state: runFailed, code: firstNonEmpty(he.Code, "failed"), reason: he.Message, prompt: "failed"}
}

// notSent tells the app and the model that r was not sent, and why; a refusal of the hub's is kept for a resend.
func (t *tracker) notSent(r *voiceRun, f sendFail) {
	c := t.c
	r.Code, r.Message, r.Draft, r.Clearable = f.code, f.reason, f.draft, f.clearable
	t.set(r, f.state)
	c.tell(r, plainWords(prompts.RenderVoiceRefused(prompts.VoiceRefused{Lang: string(c.language()), Code: f.prompt, Task: r.task,
		Reason: f.reason, Plan: f.plan, To: c.target.to()})), true, false)
	if !f.resendable {
		return
	}
	select {
	case t.ops <- func() {
		if len(t.refused) >= refusedMost {
			for id := range t.refused {
				delete(t.refused, id) // the app shows only the latest ones
				break
			}
		}
		t.refused[r.ID] = r
	}:
	case <-c.ctx.Done():
	}
}

// sendBound sends r's message to the bound conversation in the background and follows the run it starts. first: the
// model's handover (a resend is the user's own: no approval-word check, the same id). While it is sent, what the
// conversation works on is not observed (observe.go): it may be this message's turn.
func (t *tracker) sendBound(r *voiceRun, opts sendOpts, first bool) {
	c := t.c
	t.obs.sending.Add(1)
	t.obs.sends.Add(1)
	go func() {
		handed := false // its count is handed to the tracker, which takes it off once the run is in its hands
		defer func() {
			if !handed {
				t.obs.sending.Add(-1)
			}
		}()
		ctx := c.ctx
		if first && channel.IsApprovalWord(r.task) && c.cardsWaiting(ctx, approvalCards...) {
			c.logf("run %s: only an approval word while a card waits: not sent", r.ID)
			t.notSent(r, sendFail{state: runRefused, code: "on_screen", prompt: "on_screen"})
			return
		}
		r.Code, r.Message, r.Draft, r.Clearable = "", "", "", ""
		r.State = "" // a resend is sending again
		t.set(r, runSending)
		err := t.sendTo(ctx, r, opts)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			f := boundFailure(err)
			if f.prompt == "dialog_open" {
				f.plan = c.cardsWaiting(ctx, proto.IxPlan)
			}
			t.notSent(r, f)
			if f.state == runUnknown {
				// It may have got there: a turn its message begins is its own, not one begun elsewhere (observe.go).
				select {
				case t.ops <- func() {
					t.obs.unknown(r)
					t.obs.sending.Add(-1)
				}:
					handed = true
				case <-ctx.Done():
				}
			}
			return
		}
		t.set(r, runSent)
		select {
		case t.ops <- func() {
			t.adopt(r)
			t.obs.sending.Add(-1) // followed from now on
		}:
			handed = true
		case <-ctx.Done():
			t.link.unwatch(linkKey{r.MachineID, r.SessionID})
		}
	}()
}

// sendTo puts r's message into its conversation and fills in where its answer will be: the conversation's last line
// before it (kept from a first try), its transcript's revision. The conversation is watched from before the send on,
// so its turn's end is not missed. A dialog that is only a notice Enter closes (409 dialog_open, notice) is closed and
// the message sent again, the same id: the user does not see it.
func (t *tracker) sendTo(ctx context.Context, r *voiceRun, opts sendOpts) error {
	hub := t.c.pl.hub
	key := linkKey{r.MachineID, r.SessionID}
	sess, _ := hub.session(ctx, r.MachineID, r.SessionID)
	if sess != nil {
		r.rev = sess.Rev
	}
	if r.Base == 0 {
		base, err := hub.lastSeq(ctx, r.MachineID, r.SessionID)
		if err != nil {
			t.c.logf("run %s: the conversation's last line: %s (its message is found from the start)", r.ID, hubOutcome(err))
		}
		r.Base = base
	}
	t.link.watch(key, r.Base)
	sctx, cancel := context.WithTimeout(ctx, sendTimeout(sess))
	defer cancel()
	err := t.sendLogged(sctx, r, opts)
	var he *hubError
	if errors.As(err, &he) && he.Code == "dialog_open" && he.Notice && !opts.DismissNotice {
		t.c.logf("run %s: a notice holds the terminal: closing it and sending again", r.ID)
		opts.DismissNotice = true
		err = t.sendLogged(sctx, r, opts)
	}
	if err != nil {
		t.link.unwatch(key)
		return maybeSent(err)
	}
	return nil
}

// sendLogged sends r's message to its conversation, and logs how that went: the hub's status and code, how long it took.
func (t *tracker) sendLogged(ctx context.Context, r *voiceRun, opts sendOpts) error {
	began := time.Now()
	err := t.c.pl.hub.send(ctx, r.MachineID, r.SessionID, r.message, r.ID, opts)
	t.c.logf("run %s: sent to %s/%s: %s in %v%s", r.ID, r.MachineID, r.SessionID, hubOutcome(err), logDur(time.Since(began)), optsLog(opts))
	return err
}

// optsLog is what a send asked besides its message, for the log.
func optsLog(o sendOpts) string {
	s := ""
	if o.ClearDraft != "" {
		s += ", clearing the draft"
	}
	if o.DismissNotice {
		s += ", closing a notice"
	}
	return s
}

// resend sends again a run the hub refused, as the app asks (its "Resend" / "Clear and send"): the same id, a new run (restart).
// clearDraft not "": clear the box first, what the refusal said can be cleared (its clearable, which the node checks
// against the box: an IDE's selection marker in the draft is not part of it). A run this connection does not know (not
// refused, or before a reconnect) is let be.
func (t *tracker) resend(id, clearDraft string) {
	select {
	case t.ops <- func() {
		r := t.refused[id]
		if r == nil {
			t.c.logf("resend %s: no such refused run", id)
			return
		}
		t.c.logf("run %s: the app sends it again", id)
		delete(t.refused, id)
		r.Delegation = "" // it had its one commentary (the refusal): what comes is said
		if clearDraft != "" && r.Clearable != "" {
			clearDraft = r.Clearable // what the hub said can be cleared, to the letter (the app may hand back the draft)
		}
		r.restart(clock())
		t.sendBound(r, sendOpts{ClearDraft: clearDraft}, false)
	}:
	case <-t.c.ctx.Done():
	}
}

// restart makes a refused run new again for its resend at now: its turn is waited for from then (channel.QueuedMost,
// QuietGrace and a turn that ended before it are measured from Started), after the conversation's last line then
// (Base 0: sendTo reads it again).
func (r *voiceRun) restart(now time.Time) {
	r.Started, r.Base, r.rev, r.checked = now, 0, 0, time.Time{}
	r.Seen, r.ended, r.notFound, r.waiting, r.unsent = false, false, false, false, false
}

// resume follows the runs the app handed back of a call it picked up again (docs/assistant/voice.md "Reconnecting and resuming"): from their
// base, unseen, so a turn that ended while the call was away is read at once; their words are said (their calls are
// gone). One handed back while it was sending is not sent again: its message is looked for, and is unknown when it
// never shows.
func (t *tracker) resume(runs []resumeRun) {
	c := t.c
	slices.SortStableFunc(runs, func(a, b resumeRun) int {
		ta, _ := startedAt(a.StartedAt)
		tb, _ := startedAt(b.StartedAt)
		return ta.Compare(tb)
	})
	for _, x := range runs {
		r := &voiceRun{task: clipRunes(x.Task, taskMost), Gen: -1, State: x.State}
		r.Tasks = []string{r.task}
		r.ID, r.Channel, r.MachineID, r.SessionID = x.ID, "voice", c.target.MachineID, c.target.SessionID
		r.Probe, r.Base = channel.Probe(x.Task), max(0, x.BaseSeq)
		for _, id := range x.MsgIDs {
			if id != "" && len(id) <= 64 && len(r.MsgIDs) < resumeMost {
				r.MsgIDs = append(r.MsgIDs, id)
			}
		}
		var ok bool
		if r.Started, ok = startedAt(x.StartedAt); !ok {
			r.Started = time.Now()
		}
		switch x.State {
		case runHeld: // answered once, waiting for the turn its background work starts
			r.Held, r.MsgIDs, r.Probe = true, nil, ""
		case runSending:
			r.unsent = true
		case runBlocked: // waiting on something outside: its turns stop on that wait, each waits for its triage
			r.waitOn = runBlocked
		}
		c.logf("run %s: handed back by the app (%s)", r.ID, firstNonEmpty(x.State, "no state"))
		select {
		case t.ops <- func() {
			t.link.watch(linkKey{r.MachineID, r.SessionID}, r.Base)
			t.adopt(r)
		}:
		case <-c.ctx.Done():
			return
		}
	}
}

// stopped ends the runs on the bound conversation once its turn was stopped ("Stop:"): what the model was told of
// the stop is their end, nothing more is said (a run that reads the stop first ends the same way: stopAsked, finish).
// Nor is the stopped turn observed: the hub's interrupt returns before the conversation is listed as stopped (the
// agent gets an Esc), and it is no turn begun elsewhere.
func (t *tracker) stopped() {
	select {
	case t.ops <- func() {
		t.asks.quiet() // the stop closed its cards
		key := linkKey{t.c.target.MachineID, t.c.target.SessionID}
		for _, r := range slices.Clone(t.runs) {
			if (linkKey{r.MachineID, r.SessionID}) != key {
				continue
			}
			t.c.toolsDone(r.appIDs)
			t.drop(r)
			if !r.told { // its done went out already
				r.Code = "stopped"
				t.set(r, runDone)
			}
		}
		ctx, cancel := context.WithTimeout(t.c.ctx, statusTimeout)
		defer cancel()
		last, err := t.c.pl.hub.lastSeq(ctx, key.machine, key.session)
		if err != nil {
			last = math.MaxInt // until it is seen stopped
		}
		t.obs.stopped(last)
	}:
	case <-t.c.ctx.Done():
	}
}
