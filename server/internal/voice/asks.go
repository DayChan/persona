package voice

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Cards by voice (docs/assistant/voice.md "Questions and cards"). The cards waiting in the conversation the call follows — the one it is
// bound to (protocol 2), else one a run waits on (protocol 1: the main conversation) — are read from the hub when the
// hub link says one came or went (interaction, interaction_resolved, a conversation's pending count) and every
// lookEvery:
//   - a question voice can answer (kind question, with its questions, no fields, no secret) is asked, a card at a time,
//     the oldest first, a question at a time, its options by their names (voice/ask). While it is asked, what the model
//     hands over is the answer (but "Status:" and "Stop:"): an option's number, ordinal or name, else the words
//     themselves (channel.ReadAnswer, MatchOption); once its last question is answered the card is resolved, in the
//     background (a Codex card waits for its node to type the answer). 404, 410, a 200 that says already and a 409
//     prompt_changed are an answer given elsewhere first; another 409 (a node's refusal: a draft in the box, another
//     dialog open) is one only when the card is gone, else the question is asked on;
//   - any other card (a plan, a permission, a terminal dialog, a form) is only said, by its kind (voice/needs_you): it
//     is tapped on the screen (decision 5), and a spoken "approve" is not sent (send.go, on_screen);
//   - a card answered elsewhere while it is asked is confirmed in a sentence (voice/answered); one not asked yet goes
//     without a word.
// Only a full device answers cards by voice (docs/assistant/voice.md "Permissions"): another extension's call keeps the turn's
// "check on the phone" (voice/report needs_you). The tracker's goroutine owns all this, from the moment the call is set up
// (arm: the tracker runs from the app's socket on, before its start is read). The conversation keeps which card the
// model was told it asks (askCur, askTold): what the model hands over is that card's answer only once it heard the
// question, not while the question still waits for the user to stop talking.

const (
	prioCard   = 1 // a card goes before a result (sentWords.prio)
	askTimeout = 10 * time.Second
	// resolveTimeout is longer than the hub waits for a node to type a Codex card's answer (a minute): a resolve the
	// hub still works on is not given up.
	resolveTimeout = 70 * time.Second
	askAgainMost   = 2 // answers in a row not heard before the question is left to the screen
	questionMost   = 300
	labelMost      = 80
	meaningMost    = 160
	optionsMost    = 10
)

// The ask frame's states and who answered (docs/assistant/voice.md "Extension → App").
const (
	askAsking   = "asking"
	askAnswered = "answered"
	askReleased = "released"
	byVoice     = "voice"
	byElsewhere = "elsewhere"
)

// voice/answered's cases.
const (
	saidVoice     = "voice"
	saidElsewhere = "elsewhere"
	saidLate      = "late"
	saidFailed    = "failed"
	saidScreen    = "screen"
	saidUnknown   = "unknown"
)

// asker is a call's cards (owned by the tracker's goroutine).
type asker struct {
	t  *tracker
	on bool // the caller answers cards (a full device)
	// armed: the call is set up (arm), its protocol (v2: 2) known; nothing of the conversation is read before.
	armed, v2 bool
	// target is the conversation the call is bound to (protocol 2), once bind went through.
	target  linkKey
	cur     *askState
	queue   []proto.Interaction          // questions to ask after cur, oldest first
	seen    map[string]bool              // cards asked, queued or said
	pending map[string]proto.Interaction // the cards of the call's conversations, as last read
	due     bool                         // the hub link said something changed
	last    time.Time                    // the last read
}

// askState is the card being asked, with its answers so far.
type askState struct {
	ix    proto.Interaction
	a     *channel.Ask
	at    time.Time // it began to be asked
	again int       // answers in a row not heard
	// resolving: its answers are with the hub; answers handed over meanwhile wait in held for how that went.
	resolving bool
	held      []func()
}

func newAsker(t *tracker, on bool) *asker {
	return &asker{t: t, on: on, seen: map[string]bool{}, pending: map[string]proto.Interaction{}}
}

func keyOf(ix proto.Interaction) linkKey { return linkKey{ix.MachineID, ix.SessionID} }

// inScope: k's cards are the call's (the bound conversation; protocol 1, one a run follows).
func (k *asker) inScope(key linkKey) bool {
	if !k.on || !k.armed || key == (linkKey{}) {
		return false
	}
	if k.v2 {
		return key == k.target
	}
	return slices.ContainsFunc(k.t.runs, func(r *voiceRun) bool { return key == linkKey{r.MachineID, r.SessionID} })
}

// event marks a look due when what the hub link said may concern the call's cards.
func (k *asker) event(ev linkEvent) {
	if ev.key == (linkKey{}) || k.inScope(ev.key) {
		k.due = true
	}
}

// tick reads the cards when something changed, or lookEvery after the last read.
func (k *asker) tick(ctx context.Context) {
	if k.on && k.armed && (k.due || time.Since(k.last) >= k.t.every) {
		k.look(ctx)
	}
}

// covers reads the cards now and says whether one waits in key: they say themselves, the turn does not say it waits.
func (k *asker) covers(ctx context.Context, key linkKey) bool {
	if !k.inScope(key) {
		return false
	}
	k.look(ctx)
	for _, ix := range k.pending {
		if keyOf(ix) == key {
			return true
		}
	}
	return false
}

// look reads the cards waiting in the call's conversations: the one asked and gone is over (answered elsewhere, or
// closed by a stop), new questions are queued, other new cards are said by their kind (and what was to be said of
// cards all gone since is not), and the next question is asked.
func (k *asker) look(ctx context.Context) {
	k.due, k.last = false, time.Now()
	if !k.on || !k.armed {
		return
	}
	hasScope := !k.v2 && len(k.t.runs) > 0 || k.target != (linkKey{})
	if !hasScope && k.cur == nil && len(k.queue) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	ixs, err := k.t.c.pl.hub.interactions(ctx)
	if err != nil {
		return // the next look
	}
	pending := map[string]proto.Interaction{}
	var fresh []proto.Interaction
	asking := func(id string) bool { return k.cur != nil && k.cur.ix.ID == id }
	for _, ix := range ixs {
		if !k.inScope(keyOf(ix)) {
			continue
		}
		pending[ix.ID] = ix
		// A question the assistant is deciding whether to answer for the user is not theirs yet (delegate,
		// docs/assistant/assistant.md "The assistant answers questions first"): it is asked once it is left to them.
		if !k.seen[ix.ID] && !asking(ix.ID) && !ix.Delegating {
			fresh = append(fresh, ix)
		}
	}
	k.pending = pending
	still := func(id string) bool { _, ok := pending[id]; return ok }
	k.t.c.unsay(still)
	// One being resolved may be out of the list meanwhile (the hub took it, a node types the answer): how the resolve
	// went says how it ended.
	if cur := k.cur; cur != nil && !cur.resolving && !still(cur.ix.ID) {
		switch {
		case k.t.c.stoppedSince(cur.at):
			k.end(askReleased, "", false) // the stop's Esc closed it: said by the stop
		default:
			k.end(askReleased, byElsewhere, k.inScope(keyOf(cur.ix))) // a run's conversation left the call: no word
		}
	}
	k.queue = slices.DeleteFunc(k.queue, func(ix proto.Interaction) bool { return !still(ix.ID) })
	for id := range k.seen {
		if !still(id) && !asking(id) {
			delete(k.seen, id)
		}
	}
	slices.SortStableFunc(fresh, func(a, b proto.Interaction) int { return a.CreatedAt.Compare(b.CreatedAt) })
	var screen []prompts.VoiceCard
	var ids []string
	for _, ix := range fresh {
		k.seen[ix.ID] = true
		if voiceAnswerable(ix) {
			k.queue = append(k.queue, ix)
			continue
		}
		card := cardOf(ix)
		card.Text = "" // its kind only
		screen = append(screen, card)
		ids = append(ids, ix.ID)
	}
	if len(screen) > 0 {
		c := k.t.c
		c.logf("cards %s: to be done on the screen, said by their kind", strings.Join(ids, ","))
		c.deliver(&sentWords{words: plainWords(prompts.RenderVoiceNeedsYou(prompts.VoiceNeedsYou{Lang: string(c.language()), To: k.to(),
			Cards: screen, Here: k.v2})), cards: ids, prio: prioCard})
	}
	k.next()
}

// voiceAnswerable: ix is a question voice can answer (docs/assistant/voice.md "Questions and cards"): not one with fields to fill in or a
// secret to type, nor two questions alike (an answer goes by its question).
func voiceAnswerable(ix proto.Interaction) bool {
	return ix.Kind == proto.IxQuestion && len(ix.Questions) > 0 && len(ix.Fields) == 0 && !ix.Secret &&
		!proto.HasDuplicateQuestionText(ix.Questions)
}

// next asks the next question waiting, when none is being asked.
func (k *asker) next() {
	if k.cur != nil || len(k.queue) == 0 {
		return
	}
	ix := k.queue[0]
	k.queue = k.queue[1:]
	now := time.Now()
	k.cur = &askState{ix: ix, a: channel.NewAsk("voice", ix, now), at: now}
	k.t.c.startAsk(ix.ID)
	k.say(k.question(false), ix.ID, true)
}

// say gives the model w to say, ahead of results; card: the card it asks (the model asks it once it is sent).
func (k *asker) say(w words, card string, agent bool) {
	k.t.c.deliver(&sentWords{words: w, agent: agent, card: card, prio: prioCard})
}

// to is who asks: the bound conversation's name, "" the main conversation (protocol 1's runs are on it).
func (k *asker) to() string {
	if k.v2 {
		return k.t.c.target.to()
	}
	return ""
}

// question is the card's question now (its step) as the model is to ask it: from the card's data, made speakable;
// its question and options are an agent's words (they close the user-speech gate).
func (k *asker) question(again bool) words {
	c, cur := k.t.c, k.cur
	l := c.language()
	q := cur.a.Questions[min(cur.a.Step, len(cur.a.Questions)-1)]
	spoken := func(s string, most int) string { return clipRunes(oneLine(speakable(l, s)), most) }
	d := prompts.VoiceAsk{Lang: string(l), To: k.to(), Tool: c.handTool(), Bound: k.v2, Question: spoken(q.Question, questionMost),
		Multi: q.MultiSelect, N: cur.a.Step + 1, Total: len(cur.a.Questions), Again: again}
	if h := strings.TrimSpace(q.Header); h != "" && h != strings.TrimSpace(q.Question) {
		d.Header = spoken(h, labelMost)
	}
	for i, o := range q.Options {
		if i == optionsMost {
			break
		}
		d.Options = append(d.Options, prompts.VoiceOption{Label: spoken(o.Label, labelMost), Description: spoken(o.Description, meaningMost)})
	}
	return plainWords(prompts.RenderVoiceAsk(d))
}

// answered is voice/answered's case by.
func (k *asker) answered(by string) words { return k.answeredWith(prompts.VoiceAnswered{By: by}) }

func (k *asker) answeredWith(d prompts.VoiceAnswered) words {
	d.Lang, d.To, d.Here = string(k.t.c.language()), k.to(), k.v2
	return plainWords(prompts.RenderVoiceAnswered(d))
}

// answer takes text, which the model handed over on delegation (of upstream session gen) while it asked card, as the
// card's answer (its "Answer:" already taken off): the next question, or the card resolved in the background; the
// model is told on that delegation.
func (k *asker) answer(card, text, delegation string, gen int) {
	c := k.t.c
	reply := func(w words, agent bool, asks string) {
		c.answerOn(delegation, gen, &sentWords{words: w, agent: agent, card: asks, prio: prioCard})
	}
	cur := k.cur
	if cur == nil || cur.ix.ID != card {
		reply(k.answered(saidLate), false, "") // it was answered elsewhere meanwhile
		return
	}
	if cur.resolving { // what the last answer did decides what this one is
		cur.held = append(cur.held, func() { k.answer(card, text, delegation, gen) })
		return
	}
	if text = strings.TrimSpace(text); text == "" {
		if cur.again++; cur.again >= askAgainMost {
			k.end(askReleased, "", false)
			reply(k.answered(saidScreen), false, "")
			k.next()
			return
		}
		reply(k.question(true), true, card)
		return
	}
	cur.again = 0
	res, done, again := channel.ReadAnswer(c.language(), cur.a, text)
	switch {
	case again != "": // nothing left to answer (it never is: the card is resolved with its last answer)
		k.end(askReleased, "", false)
		reply(k.answered(saidScreen), false, "")
		k.next()
		return
	case !done:
		reply(k.question(false), true, card) // the card's next question
		return
	}
	cur.resolving = true
	go func() {
		out := resolveCard(c.ctx, c.pl.hub, card, res)
		select {
		case k.t.ops <- func() { k.resolved(cur, out, delegation, gen) }:
		case <-c.ctx.Done():
		}
	}()
}

// resolved is how cur's resolve went: the model is told on the delegation that answered it last, and the answers
// handed over meanwhile are taken now.
func (k *asker) resolved(cur *askState, out resolveOutcome, delegation string, gen int) {
	c := k.t.c
	card, mine := cur.ix.ID, k.cur == cur // not when a stop ended it meanwhile
	cur.resolving = false
	held := cur.held
	cur.held = nil
	d := prompts.VoiceAnswered{By: out.said}
	state, by := askReleased, ""
	switch out.said {
	case saidVoice:
		state, by = askAnswered, byVoice
		// Nothing follows its conversation but a run, or the observer on the bound one (observe.go): else its result
		// is not told by itself.
		key := keyOf(cur.ix)
		d.Unfollowed = !slices.ContainsFunc(k.t.runs, func(r *voiceRun) bool { return key == linkKey{r.MachineID, r.SessionID} }) &&
			!k.t.obs.follows(key)
	case saidLate:
		c.logf("card %s: answered elsewhere first (%s)", card, out.log)
		by = byElsewhere
	case saidUnknown:
		c.logf("card %s: resolve: %s, and it is no longer waiting", card, out.log)
	default:
		c.logf("card %s: resolve: %s", card, out.log)
		d.Code, d.Reason = out.code, out.reason
		if mine {
			unanswer(cur.a) // asked on: the user says it again
		}
	}
	if mine && out.said != saidFailed {
		k.end(state, by, false)
	}
	c.answerOn(delegation, gen, &sentWords{words: k.answeredWith(d), prio: prioCard})
	k.next()
	for _, f := range held {
		f()
	}
}

// resolveOutcome is how a resolve went, as voice/answered says it: said (voice, late, unknown, failed), and for
// failed the hub's code (said in words of its own: a node's refusal holds the box's draft or a dialog's text) or its
// own reason; log is for the log.
type resolveOutcome struct{ said, code, reason, log string }

// resolveCard resolves card id with res (docs/assistant/voice.md "Questions and cards"). The hub refuses one no longer waiting with 410
// (404: one it never had), says already of one answered before, and passes on a node's refusal of a Codex card as 409
// with its code: prompt_changed (Codex no longer asks it) is over; another (a draft in the box, another dialog open)
// leaves the card open again, so the card is looked for. With no answer from the hub (the connection, the deadline)
// the answer may have got there: gone, it is not known who answered; still waiting, it did not.
func resolveCard(ctx context.Context, hub *hubClient, id string, res proto.Resolution) resolveOutcome {
	rctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	already, err := hub.resolve(rctx, id, res)
	cancel()
	if err == nil {
		if already {
			return resolveOutcome{said: saidLate, log: "already"}
		}
		return resolveOutcome{said: saidVoice}
	}
	var he *hubError
	isHub := errors.As(err, &he)
	switch {
	case isHub && (he.Status == http.StatusNotFound || he.Status == http.StatusGone):
		return resolveOutcome{said: saidLate, log: hubOutcome(err)}
	case isHub && he.Status == http.StatusConflict && he.Code == "prompt_changed":
		return resolveOutcome{said: saidLate, log: he.Code}
	case isHub && he.Status == http.StatusConflict:
		failed := resolveOutcome{said: saidFailed, code: he.Code, log: "409 " + he.Code}
		if waiting, ok := cardWaiting(ctx, hub, id); ok && !waiting {
			return resolveOutcome{said: saidLate, log: failed.log + ", and it is gone"}
		}
		return failed
	case isHub:
		return resolveOutcome{said: saidFailed, reason: he.Message, log: hubOutcome(err)}
	}
	if waiting, ok := cardWaiting(ctx, hub, id); ok && !waiting {
		return resolveOutcome{said: saidUnknown, log: hubOutcome(err)}
	}
	return resolveOutcome{said: saidFailed, log: hubOutcome(err)}
}

// cardWaiting reads whether card id still waits (ok: the hub said).
func cardWaiting(ctx context.Context, hub *hubClient, id string) (waiting, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	ixs, err := hub.interactions(ctx)
	if err != nil {
		return false, false
	}
	return slices.ContainsFunc(ixs, func(ix proto.Interaction) bool { return ix.ID == id }), true
}

// unanswer takes back the answer channel.ReadAnswer took last (its resolve did not get through): that question is
// answered again.
func unanswer(a *channel.Ask) {
	if a.Step == 0 || a.Step > len(a.Questions) {
		return
	}
	a.Step--
	delete(a.Answers, a.Questions[a.Step].Question)
}

// end stops asking the card asked, and tells the app how it ended (when the model had asked it); say: confirm in a
// sentence that it was answered elsewhere.
func (k *asker) end(state, by string, say bool) {
	id := k.cur.ix.ID
	k.cur = nil
	if told := k.t.c.endAsk(id, state, by); told && say {
		k.say(k.answered(saidElsewhere), "", false)
	}
}

// quiet stops asking without a word (the turn was stopped: its cards went with it).
func (k *asker) quiet() {
	if k.cur != nil {
		k.end(askReleased, "", false)
	}
}

// reask asks the question asked again, to a new upstream session (the last one heard it). Not when it was told to this
// one already: what asks it waited in the queue for it (a card that woke a paused upstream, suspend.go), and the
// tracker came to this after.
func (k *asker) reask() {
	if k.cur == nil || !k.t.c.toldBefore(k.cur.ix.ID) {
		return // not told yet (what asks it is still queued), or told to this session
	}
	k.say(k.question(false), k.cur.ix.ID, true)
}

// arm starts the call's cards once the call is set up (its first upstream session, on the goroutine that read the
// app's start): from then on the tracker reads its protocol and what it is bound to. On protocol 2 it follows the
// bound conversation for the whole call (bind).
func (t *tracker) arm() {
	v2 := t.c.version >= 2
	select {
	case t.ops <- func() { t.asks.armed, t.asks.v2, t.asks.due = true, v2, true }:
	case <-t.c.ctx.Done():
		return
	}
	if v2 {
		t.bind()
	}
}

// bind follows the bound conversation for the whole call (protocol 2): its cards, from its last line now on.
func (t *tracker) bind() {
	c := t.c
	key := linkKey{c.target.MachineID, c.target.SessionID}
	go func() {
		ctx, cancel := context.WithTimeout(c.ctx, askTimeout)
		since, err := c.pl.hub.lastSeq(ctx, key.machine, key.session)
		cancel()
		if err != nil {
			// Its backlog from the start would hold an old turn's end: its cards are read every lookEvery instead.
			c.logf("bind %s/%s: its last line: %s (its cards are looked at every %s)", key.machine, key.session, hubOutcome(err), t.every)
		}
		select {
		case t.ops <- func() {
			if err == nil {
				t.link.watch(key, since)
			}
			t.asks.target, t.asks.due = key, true
		}:
		case <-c.ctx.Done():
		}
	}()
}

// answer hands what the model handed over while it asked card to the tracker as the card's answer.
func (t *tracker) answer(card, text, delegation string, gen int) {
	select {
	case t.ops <- func() { t.asks.answer(card, text, delegation, gen) }:
	case <-t.c.ctx.Done():
	}
}

// reconnected: a new upstream session (after a drop) is asked the question again.
func (t *tracker) reconnected() {
	select {
	case t.ops <- func() { t.asks.reask() }:
	case <-t.c.ctx.Done():
	}
}

// answerCard takes what the model handed over while it asked card as the card's answer, once the user-speech gate lets
// it (mu held; it goes on in the background): a resolve is a write, and a question's options are an agent's words.
func (c *conversation) answerCard(name, card, task, delegation string, gen int, at time.Time) {
	c.whenSpoken(name, at, func(spoken bool) {
		if !spoken {
			c.tell(&voiceRun{Delegation: delegation, Gen: gen}, plainWords(prompts.RenderVoiceRefused(prompts.VoiceRefused{
				Lang: string(c.language()), Code: "unprompted", Task: clipRunes(task, taskMost), To: c.target.to()})), true, false)
			return
		}
		answer := answerText(task)
		if answer == "" { // the model said nothing of the answer (a bare "Answer:" too): what the user said
			c.mu.Lock()
			answer = answerText(c.lastUserLine())
			c.mu.Unlock()
		}
		c.runs.answer(card, answer, delegation, gen)
	})
}

// answerText is what the model handed over as an answer without its "Answer:".
func answerText(task string) string {
	if kind, rest := splitMark(task); kind == "answer" {
		return rest
	}
	return strings.TrimSpace(task)
}

// handTool is the tool the model hands things over with ("" none: a delegation of its own).
func (c *conversation) handTool() string {
	tools := c.toolSet()
	for _, name := range []string{"delegate_task", "ask_persona"} {
		if slices.Contains(tools, name) {
			return name
		}
	}
	return ""
}

// startAsk: the tracker asks card now; the model asks it once what asks it is sent (sendNow, told).
func (c *conversation) startAsk(card string) {
	c.mu.Lock()
	c.askCur, c.askTold = card, false
	c.mu.Unlock()
}

// told notes words that ask a card were sent (mu held): from now on the model asks it, and the app is told (protocol
// 2). Words of a card no longer asked are nothing.
func (c *conversation) told(card string) {
	if card == "" || card != c.askCur {
		return
	}
	c.askGen = c.gen
	if c.askTold {
		return
	}
	c.askTold = true
	c.count(func(l *callLog) { l.cards++ })
	c.logf("card %s: asked by voice", card)
	if c.version >= 2 {
		c.sendApp(askFrame(card, askAsking, ""))
	}
}

// endAsk: card is no longer asked. What was to be said to ask it is dropped (a reply on a delegation still goes:
// that call has no other result), and the app is told how it ended when the model had asked it. Whether it had.
func (c *conversation) endAsk(card, state, by string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.askCur != card {
		return false
	}
	told := c.askTold
	c.askCur, c.askTold = "", false
	c.queue = slices.DeleteFunc(c.queue, func(s *sentWords) bool { return s.card == card && s.delegation == "" })
	switch {
	case !told:
		c.logf("card %s: %s before it was asked", card, state)
	case by != "":
		c.logf("card %s: %s (%s)", card, state, by)
	default:
		c.logf("card %s: %s", card, state)
	}
	if told && c.version >= 2 {
		c.sendApp(askFrame(card, state, by))
	}
	return told
}

// unsay drops what waits to be said of cards on the screen (voice/needs_you) none of which still waits: one tapped
// while the model spoke is not said after (a say waits for the insertion gate).
func (c *conversation) unsay(still func(id string) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue = slices.DeleteFunc(c.queue, func(s *sentWords) bool {
		return s.delegation == "" && len(s.cards) > 0 && !slices.ContainsFunc(s.cards, still)
	})
}

// stopping notes that "Stop:" interrupts the bound conversation now (done false: the interrupt failed, it did not).
func (c *conversation) stopping(done bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if done {
		c.stopAt = time.Now()
	} else {
		c.stopAt = time.Time{}
	}
}

// stoppedSince: a stop began at or after at (a card asked from at and gone since was closed by it).
func (c *conversation) stoppedSince(at time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.stopAt.IsZero() && !c.stopAt.Before(at)
}

// toldBefore: the model was told it asks card in an earlier upstream session, not in this one.
func (c *conversation) toldBefore(card string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return card != "" && c.askingCardLocked() == card && c.askGen != c.gen
}

func (c *conversation) askingCardLocked() string {
	if c.askTold {
		return c.askCur
	}
	return ""
}

func askFrame(card, state, by string) map[string]any {
	f := map[string]any{"type": "ask", "interaction_id": card, "state": state}
	if by != "" {
		f["by"] = by
	}
	return f
}
