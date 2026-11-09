package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// "Please verify" (#2768, docs/extensions/bots.md "Please verify"): once a task's work is deployed, the hub keeps an ask for the user to try it —
// three lines: what changed, where to look (a link straight to the page), how to check. With the bot on (and its task reports
// on) the ask goes out as a Feishu card with "Pass" on it; without, it is kept all the same (NoCard) and the briefing, the
// board and the web app show it. "Pass" (the button, a reply saying so, or the web Accept button) marks the task verified; any other reply to the card is written into the todo's
// decisions and sent to the agent that did the work. One still not accepted is asked again the next morning.

const acceptanceKey = "assistant.acceptance"

// acceptMorningHour is when, in the user's zone, the cards not accepted the day before are posted again, when the
// morning briefing (the "Morning briefing" routine) is off or gone; while it is on, its own time.
const acceptMorningHour = 9

type acceptAsk struct {
	MessageID string   `json:"message_id"`         // the first card ("" while none went out, see NoCard)
	Messages  []string `json:"messages,omitempty"` // every card asking it (the first and the reminders)
	// Earlier are the cards of the task's rounds before this one (#4853): dead, so a press or a reply on one is answered
	// "already handled" and never accepts this round or reaches the chat.
	Earlier    []string  `json:"earlier,omitempty"`
	MachineID  string    `json:"machine_id"`
	SessionID  string    `json:"session_id"`
	Changed    string    `json:"changed"`
	Link       string    `json:"link,omitempty"`
	How        string    `json:"how"`
	AskedAt    time.Time `json:"asked_at"`
	RemindedAt time.Time `json:"reminded_at,omitzero"`
	AcceptedAt time.Time `json:"accepted_at,omitzero"`
	// NoCard: no card is due (the bot is off or not paired, or task reports are off, when the ask was made or when its
	// card was tried again), or its card could not be posted within acceptPostWindow, so MessageID stays empty for good;
	// the owner is asked by the briefing, the board and the web app instead. An empty MessageID without it (and with
	// AskedAt) is a card that did not go out yet: retryAcceptance posts it.
	NoCard bool `json:"no_card,omitempty"`
	// Conclusion: the ask is a supervised task's conclusion (#1226, conclusion.go), told once its review gives a result: its
	// card says the verdict, one sentence of why and one link instead of the three lines. Outcome is the verdict
	// (outcomeDone, outcomeCannot, outcomeDecide; "" reads as done: a deployed task's ask says that on top of its three
	// lines), Reason the sentence, Recommend the option a decision recommends, which "OK" takes.
	Conclusion bool   `json:"conclusion,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Recommend  string `json:"recommend,omitempty"`
}

// acceptPostWindow is how long after an ask its card that did not go out (the bot's status could not be read, the send
// failed or timed out) is tried again, once a tick of acceptLoop. Every try goes under the round's own key
// (acceptCardKey), and Feishu drops a repeated key only for about an hour: a try later than that could post a second
// card, so past the window the ask stays card-less (NoCard) instead. The window, not a count of tries, is what keeps
// that true when ticks come late (a restart in between); with ticks every 10 minutes it is at most six sends (the
// first and five retries).
const acceptPostWindow = 45 * time.Minute

// acceptCardKey is the key (feishu.post's uuid) the card of task id's round asked at goes out under: the same for every
// try of that round, so a try that did land though its answer was lost (a timeout) is not posted again by the next.
func acceptCardKey(id string, asked time.Time) string {
	sum := sha256.Sum256([]byte("accept\x00" + id + "\x00" + strconv.FormatInt(asked.UnixNano(), 10)))
	return "pv:" + hex.EncodeToString(sum[:16])
}

// acceptCardDue says a's card is still to be posted: asked (not a claim in flight), not passed, no card and none
// meant to be.
func acceptCardDue(a *acceptAsk) bool {
	return a != nil && !a.AskedAt.IsZero() && a.AcceptedAt.IsZero() && a.MessageID == "" && !a.NoCard
}

type acceptBook struct {
	mu     sync.Mutex
	loaded bool
	asks   map[string]*acceptAsk // by todo id
}

// loadAcceptLocked reads the asks once and reports whether they are in memory; s.accept.mu is held. A failed read
// leaves them unloaded (the next look tries again): writing the book after it would put an empty or partial map over
// the stored cards, whose replies, reminders and briefing lines would then be lost.
func (s *Service) loadAcceptLocked(ctx context.Context) bool {
	b := &s.accept
	if b.loaded {
		return true
	}
	asks := map[string]*acceptAsk{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, acceptanceKey, &asks); err != nil {
			s.logf("acceptance: load: %v", err)
			return false
		}
		if asks == nil {
			asks = map[string]*acceptAsk{}
		}
	}
	// What is read was written before this process asked anything. An entry with no time is a claim whose card was
	// going out when another save wrote the book: nothing will finish it, and left in place it would keep its task from
	// ever being asked again (a claim is never stale). A null entry is nothing.
	for id, a := range asks {
		if a == nil || a.AskedAt.IsZero() {
			s.logf("acceptance: load: task %s: dropped a claim left in flight", id)
			delete(asks, id)
		}
	}
	b.asks, b.loaded = asks, true
	return true
}

// errAcceptStore: the asks could not be read, or an acceptance could not be saved. Nothing was accepted and the ask still
// waits; the owner is told the service is unavailable (try again), never "already handled" or "verified".
var errAcceptStore = errors.New("acceptance: the asks could not be read or saved")

// saveAcceptLocked writes the asks, once they were read (see loadAcceptLocked); a failed write is logged.
func (s *Service) saveAcceptLocked(ctx context.Context) {
	if err := s.saveAcceptErrLocked(ctx); err != nil {
		s.logf("acceptance: save: %v", err)
	}
}

// saveAcceptErrLocked is saveAcceptLocked returning the failed write instead of logging it.
func (s *Service) saveAcceptErrLocked(ctx context.Context) error {
	if !s.accept.loaded {
		return nil
	}
	if st := s.store(); st != nil {
		return st.PutSetting(ctx, acceptanceKey, s.accept.asks)
	}
	return nil
}

var urlRe = lazyre.New(`https?://[^\s<>()\[\]"'，。；、）]+`)

// askAcceptance keeps task t's "Please verify" ask once (reply: what its agent said last, on machine/session) and posts its
// card when the bot can. The ask is a fact of the hub, not of the card: with no card due it is kept all the same (NoCard),
// and the briefing, the board and the snapshot list it. A card that did not go out (the bot's status unreadable, the send
// failed) is kept as an ask without one yet, listed the same, and retryAcceptance posts it.
func (s *Service) askAcceptance(t Todo, machine, session, reply string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return
	}
	var earlier []string
	if old := b.asks[t.ID]; old != nil && (old.AskedAt.IsZero() || !old.Conclusion) && !acceptAskStale(old, t) {
		b.mu.Unlock()
		return
	} else if old != nil {
		// asked before the task's latest reopen, or a conclusion (conclusion.go): the new round gets its own card, the old
		// ones stay dead
		earlier = lastOf(slices.Concat(old.Earlier, old.Messages), acceptEarlierMost)
		delete(b.asks, t.ID)
	}
	b.asks[t.ID] = &acceptAsk{Earlier: earlier} // claimed: a second turn ending meanwhile does not ask twice
	b.mu.Unlock()
	a := s.acceptLines(ctx, t, machine, session, reply)
	a.MachineID, a.SessionID, a.AskedAt, a.Earlier = machine, session, time.Now().UTC(), earlier
	s.postAsk(ctx, t, a)
}

// postAsk posts the card of ask a, which task t claimed in the book, when the bot can, and keeps a in the claim's place;
// true when its card went out. No card is due when the bot is off or not paired, or its task reports are off. A status
// that cannot be read (the manager away) is not that: the card is for later, and the ask waits for it without one (not
// NoCard), for retryAcceptance.
func (s *Service) postAsk(ctx context.Context, t Todo, a acceptAsk) bool {
	b := &s.accept
	st, stErr := s.feishuStatus(ctx)
	switch {
	case stErr != nil:
		s.logf("task %s: acceptance: %v (its card is tried again later)", t.ID, stErr)
	case st.Ready && st.Features.Reports:
		mid, err := s.botPostKeyed(ctx, "", s.acceptCard(ctx, t, a, false), askSummary(s.botLang(ctx), &a), acceptCardKey(t.ID, a.AskedAt))
		if err != nil {
			s.logf("task %s: acceptance: %v (its card is tried again later)", t.ID, err)
			break
		}
		a.MessageID, a.Messages = mid, []string{mid}
	default:
		a.NoCard = true
	}
	posted := a.MessageID != "" // read before a is shared: a retry may set its card once the book has it
	b.mu.Lock()
	if c := b.asks[t.ID]; c != nil && c.AskedAt.IsZero() {
		a.Earlier = c.Earlier // the claim's: it also has a card of the round before that a retry posted meanwhile
	}
	b.asks[t.ID] = &a
	s.saveAcceptLocked(ctx)
	b.mu.Unlock()
	s.askChanged(ctx) // it waits for the owner now: the apps and the board
	return posted
}

// acceptLines are the card's three lines: the small model reads the task and the reply; without it (or when it
// answers nothing usable) the task's title, its page and a plain check.
func (s *Service) acceptLines(ctx context.Context, t Todo, machine, session, reply string) acceptAsk {
	l := s.botLang(ctx)
	web := s.webBase(ctx)
	var links []string
	add := func(u string) {
		if u != "" && !slices.Contains(links, u) {
			links = append(links, u)
		}
	}
	for _, u := range urlRe.FindAllString(reply, -1) {
		add(strings.TrimRight(u, ".,;:!?"))
	}
	add(todoLink(web, t.ID))
	if web != "" && session != "" {
		add(sessionLink(web, machine, session, false))
	}
	a := acceptAsk{Changed: clip(oneLine(t.Title), 40), Link: todoLink(web, t.ID), How: i18n.T(l, "assistant-reports.accept_how_default")}
	var out struct {
		Changed string `json:"changed"`
		Link    string `json:"link"`
		How     string `json:"how"`
	}
	input := prompts.RenderAcceptanceInput(prompts.Acceptance{Title: t.Title, Prompt: clipMiddle(strings.TrimSpace(t.Notes), 800),
		Reply: clipMiddle(reply, 2000), Links: links})
	if err := s.purposeModel("inbox.judge").JSON(ctx, prompts.RenderAcceptance(), input, &out); err != nil {
		s.logf("task %s: acceptance lines: %v (plain ones)", t.ID, err)
		return a
	}
	if c := clip(oneLine(out.Changed), 60); c != "" {
		a.Changed = c
	}
	if slices.Contains(links, strings.TrimSpace(out.Link)) {
		a.Link = strings.TrimSpace(out.Link) // only one it was given: never a link it made up
	}
	if h := clip(oneLine(out.How), 120); h != "" {
		a.How = h
	}
	return a
}

// acceptCard is the "Please verify" card (again: the morning's reminder), or how it ended (accepted): the verdict line on
// top, then the three lines. A conclusion has its own card (conclusionCard).
func (s *Service) acceptCard(ctx context.Context, t Todo, a acceptAsk, again bool) *feishubot.Card {
	if a.Conclusion {
		return s.conclusionCard(ctx, t, a)
	}
	l := s.botLang(ctx)
	head := "assistant-reports.accept_title"
	if again {
		head = "assistant-reports.accept_again"
	}
	title := i18n.T(l, head, i18n.Args{"task": clip(oneLine(t.Title), 30)})
	c := feishubot.NewCard("🧪 "+title, feishubot.Purple, title)
	link := i18n.T(l, "assistant-reports.accept_no_link")
	if a.Link != "" {
		link = "[" + feishubot.EscapeMarkdown(clip(a.Link, 80)) + "](" + a.Link + ")"
	}
	c.Markdown("**" + feishubot.EscapeMarkdown(verdictText(l, &a)) + "**")
	c.Markdown(i18n.T(l, "assistant-reports.accept_lines", i18n.Args{"changed": feishubot.EscapeMarkdown(a.Changed), "link": link,
		"how": feishubot.EscapeMarkdown(a.How)}))
	if !a.AcceptedAt.IsZero() {
		return c.Note(i18n.T(l, "assistant-reports.accepted"))
	}
	bs := []feishubot.Button{feishubot.Btn(i18n.T(l, "assistant-reports.accept_pass"), feishubot.Primary, map[string]any{"a": "accept", "id": t.ID})}
	if a.Link != "" {
		bs = append(bs, feishubot.LinkBtn(i18n.T(l, "assistant-reports.accept_open"), a.Link))
	}
	c.Buttons(bs...)
	return c.Note(i18n.T(l, "assistant-reports.accept_reply_hint"))
}

// acceptEarlierMost is how many cards of earlier rounds a task's ask remembers.
const acceptEarlierMost = 20

// lastOf is list's last n.
func lastOf(list []string, n int) []string {
	if len(list) > n {
		return list[len(list)-n:]
	}
	return list
}

// acceptFor is the todo whose "Please verify" card message id is (or the thread it is in), "" when none; earlier when the card
// is one of an earlier round of that task (Earlier).
func (s *Service) acceptFor(ids ...string) (todo string, earlier bool) {
	b := &s.accept
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadAcceptLocked(context.Background()) {
		return "", false
	}
	for todo, a := range b.asks {
		for _, id := range ids {
			if id != "" && slices.Contains(a.Messages, id) {
				return todo, false
			}
		}
	}
	for todo, a := range b.asks {
		for _, id := range ids {
			if id != "" && slices.Contains(a.Earlier, id) {
				return todo, true
			}
		}
	}
	return "", false
}

// acceptAskLocked returns todo id's stored ask (nil: none), or errAcceptStore when the asks cannot be read; callers only
// read its immutable AskedAt.
func (s *Service) acceptAskLocked(id string) (*acceptAsk, error) {
	b := &s.accept
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadAcceptLocked(context.Background()) { // after a restart nothing may have read them yet (a button of an old briefing)
		return nil, errAcceptStore
	}
	return b.asks[id], nil
}

// passWords accepts bare approval, optionally prefixed with the Chinese word for "verify" and followed by one or two
// Chinese completion particles (as in "ok" plus a particle) or a stop. Question particles stay out, and
// both full-width and ASCII question marks are neither particles nor stops, so questions still go to the agent, and
// negations ("not passed", "can't pass", "no good") never match because the regex anchors on the approval word.
var passWords = lazyre.New(`(?i)^\s*(验收)?(通过|没问题|可以|好的?|ok|okay|lgtm|pass(ed)?|approved?|👍)(?:[了啦呀咯喽呗哒啊]{1,2})?\s*[。.!！]?\s*$`)

// acceptReply answers a reply to a "Please verify" card or a conclusion's (true: it was one): "Pass" marks it verified (a
// decision: takes the option recommended), anything else is the user's change (a decision: their choice), written into the
// todo's decisions and sent to its agent. media: the reply also carried pictures or files; only the words go on, and the
// answer says the rest did not (#5107).
func (s *Service) acceptReply(ctx context.Context, messageID, text string, media bool, ids ...string) bool {
	id, earlier := s.acceptFor(ids...)
	if id == "" {
		return false
	}
	l := s.botLang(ctx)
	text = strings.TrimSpace(text)
	if text == "" {
		if media {
			s.botNote(ctx, messageID, i18n.T(l, "assistant-reports.accept_media_dropped"))
		}
		return true
	}
	if earlier { // a card of a round before the one asking now: answered, it neither accepts nor changes this round
		s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
		return true
	}
	t, ok := s.todoNow(id)
	ask, _ := s.askCopy(id)
	if decideAsk(&ask) && !ask.AcceptedAt.IsZero() { // taken, or its review moved on: over
		s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
		return true
	}
	// "OK" to a decision with nothing recommended takes nothing: it is their answer, and goes on as one.
	if passWords.MatchString(text) && !(decideAsk(&ask) && ask.Recommend == "") {
		// Approval on a pre-reopen card is answered but neither accepts nor reopens; a gone todo keeps old behavior.
		if ok {
			if a, _ := s.acceptAskLocked(id); a != nil && acceptAskStale(a, t) { // acceptFor read the asks
				s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
				return true
			}
		}
		if ok, err := s.acceptTry(ctx, id, ""); errors.Is(err, errAcceptStore) {
			s.botNote(ctx, messageID, i18n.T(l, "http-status.unavailable"))
		} else if err != nil {
			s.botNote(ctx, messageID, i18n.T(l, "assistant-reports.accept_not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 120)}))
		} else if ok {
			s.botNote(ctx, messageID, acceptedNote(l, &ask))
		}
		return true
	}
	if !ok {
		s.botNote(ctx, messageID, i18n.T(l, "assistant-reports.accept_gone"))
		return true
	}
	if a, _ := s.acceptAskLocked(id); a != nil && acceptAskStale(a, t) { // old round's change is answered, not routed
		s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
		return true
	}
	question, msg := i18n.T(l, "assistant-reports.accept_question"), i18n.T(l, "assistant-reports.accept_to_agent", i18n.Args{"feedback": text})
	switch {
	case decideAsk(&ask):
		question = cmp.Or(ask.Reason, i18n.T(l, "assistant-reports.verdict_decide"))
		msg = i18n.T(l, "assistant-reports.verdict_decided_to_agent", i18n.Args{"decision": text})
	case ask.Conclusion:
		msg = i18n.T(l, "assistant-reports.verdict_to_agent", i18n.Args{"feedback": text})
	}
	if st := s.todoDB(); st != nil {
		at := decisionNow()
		if ok, err := s.insertDecision(ctx, st, db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: id,
			Question: question, Choice: decisionText(text), Source: db.DecisionManual,
			SourceMessage: "feishu-" + messageID, CreatedAt: at}); err != nil {
			s.logf("task %s: acceptance decision: %v", id, err)
		} else if ok {
			s.decisionsChanged(ctx, id, at)
		}
	}
	// Not accepted: the work goes on. A task the hub (or the user) closed meanwhile is open again before its agent is
	// told, so the turn the change starts is followed (receipt, judgment, the next "Please verify") and the task shows as in
	// progress, not done (#3457). It stays open when the send fails: the change is still to be made.
	if t.StartError == "" {
		if err := s.reopenTodo(ctx, id); err != nil {
			s.logf("task %s: acceptance reopen: %v", id, err)
		}
	}
	if _, err := s.routeToTask(ctx, t.Origin, id, msg); err != nil {
		s.logf("task %s: acceptance to agent: %v", id, err)
		s.botNote(ctx, messageID, withMediaDropped(l, i18n.T(l, "assistant-reports.accept_not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 120)}), media))
		return true
	}
	if decideAsk(&ask) {
		s.userDecided(ctx, id) // decided: its agent's next stop goes back to the supervisor
	}
	// Changed again: the next deploy asks again. A decision is answered: its card stays known, and says so to a reply.
	b := &s.accept
	b.mu.Lock()
	if a := b.asks[id]; a != nil && decideAsk(a) {
		a.AcceptedAt = time.Now().UTC()
	} else {
		delete(b.asks, id)
	}
	s.saveAcceptLocked(ctx)
	b.mu.Unlock()
	s.botNote(ctx, messageID, withMediaDropped(l, i18n.T(l, "assistant-reports.accept_sent"), media))
	return true
}

// withMediaDropped is note with, when the reply carried pictures or files, the line saying they did not go to the agent.
func withMediaDropped(l i18n.Lang, note string, media bool) string {
	if !media {
		return note
	}
	return note + "\n" + i18n.T(l, "assistant-reports.accept_media_dropped")
}

// accepted marks task id verified; false when already handled, never asked, or asked before the latest reopen. A card-less
// ask (NoCard) can be accepted too, from the web app. card is the message pressed ("" from a reply, which acceptFor already
// matched, from the web app, or when Feishu does not say): a card that is not this round's (an earlier round's, once the
// next one has its own) accepts nothing (#4853). A decision (a conclusion that needs the owner) is accepted by taking the
// option it recommends, which goes to the task's agent (takeRecommendation); one with nothing recommended, or no longer
// waiting, takes nothing; one whose recommendation could not reach the agent is not taken (acceptTry says why).
func (s *Service) accepted(ctx context.Context, id, card string) bool {
	ok, _ := s.acceptTry(ctx, id, card)
	return ok
}

// acceptTry is accepted, with the error of a decision whose recommendation did not reach the task's agent, or
// errAcceptStore when the asks cannot be read or the acceptance cannot be saved: either way nothing was taken, the ask
// still waits, and the owner can try again.
func (s *Service) acceptTry(ctx context.Context, id, card string) (bool, error) {
	t, todoOK := s.todoNow(id)
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return false, errAcceptStore
	}
	a, ok := b.asks[id]
	if !ok || a.AskedAt.IsZero() || !a.AcceptedAt.IsZero() || (todoOK && acceptAskStale(a, t)) || (card != "" && card != a.MessageID && !slices.Contains(a.Messages, card)) ||
		decideAsk(a) && (a.Recommend == "" || todoOK && acceptAskEnded(a, t)) {
		b.mu.Unlock()
		return false, nil
	}
	a.AcceptedAt = time.Now().UTC()
	if err := s.saveAcceptErrLocked(ctx); err != nil {
		// Kept only in memory, it would wait again after a restart: not accepted, and nothing that follows one runs.
		a.AcceptedAt = time.Time{}
		b.mu.Unlock()
		s.logf("task %s: acceptance: save: %v", id, err)
		return false, fmt.Errorf("%w: %w", errAcceptStore, err)
	}
	cp := *a
	b.mu.Unlock()
	s.askChanged(ctx) // it no longer waits
	switch {
	case decideAsk(&cp):
		if err := s.takeRecommendation(ctx, id, cp); err != nil {
			// Not taken after all: the owner can take it again, and is told why it did not go.
			b.mu.Lock()
			if a := b.asks[id]; a != nil && a.AskedAt.Equal(cp.AskedAt) {
				a.AcceptedAt = time.Time{}
				s.saveAcceptLocked(ctx)
			}
			b.mu.Unlock()
			s.askChanged(ctx)
			return false, err
		}
	case cp.Conclusion:
		// Seen: the review already closed the task and told where it came from; "verified" is for deployed work.
	default:
		s.setTaskStage(ctx, id, "verify", "")
		s.threadPost(id, threadEvent{kind: threadTodoAccepted}) // and the todo's chat thread
		s.sourceReportSoon(id, sourceReportWait)                // and where the request came from (todo_source.go)
	}
	if t, ok := s.todoNow(id); ok {
		for _, mid := range cp.Messages {
			if err := s.botUpdateCard(ctx, mid, s.acceptCard(ctx, t, cp, false)); err != nil {
				s.logf("task %s: acceptance card: %v", id, err)
			}
		}
	}
	return true, nil
}

// acceptLoop posts the cards that did not go out when their ask was made (retryAcceptance), and again, each morning, the
// cards not accepted by then (once a day each).
func (s *Service) acceptLoop(ctx context.Context) {
	tick := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now := time.Now()
			s.retryAcceptance(now)
			s.remindAcceptance(now)
		}
	}
}

// retryAcceptance posts the card of each ask still waiting for one (acceptCardDue), under the round's key, so a try that
// landed unseen is not posted twice. The round guards are a reminder's: an ask whose todo is gone or ended without the
// hub's auto-judge, or that is from before its latest reopen, is dropped, never posted. With the bot off or its reports
// off no card is due, and past acceptPostWindow none is tried: either way the ask stays card-less (NoCard), still
// listed. A status that cannot be read, or a send that fails, waits for the next tick.
func (s *Service) retryAcceptance(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return
	}
	var due []string
	seen := map[string]time.Time{} // each due ask's AskedAt: the round the sweep judges
	for id, a := range b.asks {
		if acceptCardDue(a) {
			due = append(due, id)
			seen[id] = a.AskedAt
		}
	}
	b.mu.Unlock()
	if len(due) == 0 {
		return // nothing waits: the bot's status is not even asked for
	}
	slices.Sort(due)
	st, stErr := s.feishuStatus(ctx)
	for _, id := range due {
		t, ok := s.todoNow(id)
		if f := s.acceptSweepHook; f != nil {
			f(id)
		}
		b.mu.Lock()
		a := b.asks[id]
		if !acceptCardDue(a) || !a.AskedAt.Equal(seen[id]) {
			// Gone, passed, or a new round asked while the book was unlocked (the todo read is from before it).
			b.mu.Unlock()
			continue
		}
		if !ok || acceptAskEnded(a, t) || acceptAskStale(a, t) {
			// The todo is gone, it ended without the hub's auto-judge, or the ask is from before its latest reopen: it
			// can never be passed, so its card is not posted and the ask is dropped (as remindAcceptance does).
			delete(b.asks, id)
			s.saveAcceptLocked(ctx)
			b.mu.Unlock()
			s.boardChanged()
			continue
		}
		late := now.Sub(a.AskedAt) > acceptPostWindow
		if late || stErr == nil && !(st.Ready && st.Features.Reports) {
			if late {
				s.logf("task %s: acceptance: its card did not go out within %v: kept without one", id, acceptPostWindow)
			}
			a.NoCard = true
			s.saveAcceptLocked(ctx)
			b.mu.Unlock()
			continue
		}
		if stErr != nil {
			b.mu.Unlock()
			continue
		}
		cp := *a
		b.mu.Unlock()
		mid, err := s.botPostKeyed(ctx, "", s.acceptCard(ctx, t, cp, false), askSummary(s.botLang(ctx), &cp), acceptCardKey(id, cp.AskedAt))
		if err != nil {
			s.logf("task %s: acceptance: %v (tried again on the next tick)", id, err)
			continue
		}
		b.mu.Lock()
		var done *acceptAsk
		if a := b.asks[id]; a != nil && a.AskedAt.Equal(cp.AskedAt) {
			a.MessageID, a.Messages = mid, append(a.Messages, mid)
			if !a.AcceptedAt.IsZero() {
				c := *a // passed while the card went out: it says so too
				done = &c
			}
		} else if a != nil {
			// Another round asks now (the todo reopened and deployed again meanwhile): this card is an earlier round's.
			a.Earlier = lastOf(append(slices.Clone(a.Earlier), mid), acceptEarlierMost)
		}
		s.saveAcceptLocked(ctx)
		b.mu.Unlock()
		if done != nil {
			if err := s.botUpdateCard(ctx, mid, s.acceptCard(ctx, t, *done, false)); err != nil {
				s.logf("task %s: acceptance card: %v", id, err)
			}
		}
	}
}

func (s *Service) remindAcceptance(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	loc := s.location(ctx)
	local := now.In(loc)
	hh, mm := s.briefingClock(ctx)
	if local.Hour()*60+local.Minute() < hh*60+mm+s.briefingGrace(ctx, local, hh*60+mm) {
		return
	}
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return
	}
	var due []string
	seen := map[string]time.Time{} // each due card's AskedAt: the round the sweep judges
	for id, a := range b.asks {
		// A card again only for an ask that has a card; a card-less one (NoCard) is listed by the briefing and the board,
		// and so is a conclusion: it was pushed once, and the next pushes are the briefing's (#1226).
		if a.MessageID != "" && !a.Conclusion && a.AcceptedAt.IsZero() && a.AskedAt.Before(today) && a.RemindedAt.Before(today) {
			due = append(due, id)
			seen[id] = a.AskedAt
		}
	}
	b.mu.Unlock()
	if len(due) == 0 {
		return
	}
	if f, ready := s.feishuFeatures(ctx); !ready || !f.Reports {
		return
	}
	slices.Sort(due)
	for _, id := range due {
		t, ok := s.todoNow(id)
		if f := s.acceptSweepHook; f != nil {
			f(id)
		}
		b.mu.Lock()
		a := b.asks[id]
		if a == nil || !a.AskedAt.Equal(seen[id]) {
			// Gone, or a new round asked while the book was unlocked (the todo read is from before it): not this card.
			b.mu.Unlock()
			continue
		}
		if !ok || acceptAskEnded(a, t) || acceptAskStale(a, t) {
			// The todo is gone, it ended without the hub's auto-judge (the owner closed it or a review did), or the
			// card is from before its latest reopen: it can never be passed, so stop asking and drop it.
			delete(b.asks, id)
			s.saveAcceptLocked(ctx)
			b.mu.Unlock()
			s.boardChanged()
			continue
		}
		cp := *a
		b.mu.Unlock()
		mid, err := s.botPost(ctx, "", s.acceptCard(ctx, t, cp, true), cp.Changed)
		if err != nil {
			s.logf("task %s: acceptance reminder: %v", id, err)
			return
		}
		b.mu.Lock()
		var done *acceptAsk
		if a := b.asks[id]; a != nil {
			a.RemindedAt = now.UTC()
			a.Messages = append(a.Messages, mid)
			if !a.AcceptedAt.IsZero() {
				cp := *a // passed while the reminder went out: that card says so too
				done = &cp
			}
		}
		s.saveAcceptLocked(ctx)
		b.mu.Unlock()
		if done != nil {
			if err := s.botUpdateCard(ctx, mid, s.acceptCard(ctx, t, *done, false)); err != nil {
				s.logf("task %s: acceptance card: %v", id, err)
			}
		}
	}
}

// briefingClock is when the morning briefing goes out (the enabled "Morning briefing" routine's time), else acceptMorningHour: the
// reminders come with it, and the briefing lists what waits (snapshotUnaccepted).
func (s *Service) briefingClock(ctx context.Context) (int, int) {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return acceptMorningHour, 0
	}
	defer sc.mu.Unlock()
	for _, t := range sc.tasks {
		if t.ID != "morning" || !t.Enabled || t.Repeat != repeatDaily {
			continue
		}
		if m := hhmm.FindStringSubmatch(t.Time); m != nil {
			hh, _ := strconv.Atoi(m[1])
			mm, _ := strconv.Atoi(m[2])
			return hh, mm
		}
	}
	return acceptMorningHour, 0
}

// snapshotUnaccepted are the deployed tasks still waiting for the user to check them, the oldest eight, and how many
// there are (the snapshot's "waiting for the user to verify", which the morning briefing brings up).
func (s *Service) snapshotUnaccepted(ctx context.Context, loc *time.Location) ([]prompts.SnapshotAccept, int) {
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return nil, 0
	}
	type ask struct {
		id string
		a  acceptAsk
	}
	var list []ask
	for id, a := range b.asks {
		if !a.AskedAt.IsZero() && a.AcceptedAt.IsZero() { // asked (a claim still going out has no time), card or not
			list = append(list, ask{id, *a})
		}
	}
	b.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].a.AskedAt.Before(list[j].a.AskedAt) })
	var out []prompts.SnapshotAccept
	var ended []ask
	total := 0
	for _, x := range list {
		t, ok := s.todoNow(x.id)
		if f := s.acceptSweepHook; f != nil {
			f(x.id)
		}
		if !ok {
			continue
		}
		if acceptAskEnded(&x.a, t) || acceptAskStale(&x.a, t) {
			ended = append(ended, x) // closed by the owner or a review, or asked before the todo's latest reopen
			continue
		}
		total++
		if len(out) < 8 { // the oldest eight; the total says how many more
			out = append(out, prompts.SnapshotAccept{Title: clip(oneLine(t.Title), 40), Verdict: verdictText(s.botLang(ctx), &x.a),
				Changed: x.a.Changed, Link: x.a.Link, Since: x.a.AskedAt.In(loc)})
		}
	}
	if len(ended) > 0 {
		b.mu.Lock()
		removed := false
		for _, x := range ended {
			// Only the card judged: a new round asked while the book was unlocked has another AskedAt and stays.
			if cur := b.asks[x.id]; cur != nil && cur.AskedAt.Equal(x.a.AskedAt) {
				delete(b.asks, x.id)
				removed = true
			}
		}
		if removed {
			s.saveAcceptLocked(ctx)
		}
		b.mu.Unlock()
	}
	return out, total
}

// acceptAskEnded says the todo a "Please verify" card waits on has ended without the hub's auto-judge: the owner closed it in
// the todos themselves (DoneBy "") or a review did (DoneByReview). Such a card can never be passed, so it is neither
// reminded nor counted. An auto-closed task (DoneByAuto) is the normal state: the judge finished it, but the owner
// still has to pass the card, and so is a supervised task its review accepted (#1226: the review's conclusion is that
// card, with the verdict on top). A conclusion is told after its review closed the task, so that does not end it; a
// decision ends once the task is closed or no longer waits for the user (the supervisor reviewed it again).
func acceptAskEnded(a *acceptAsk, t Todo) bool {
	if a.Conclusion {
		return a.Outcome == outcomeDecide && (t.DoneAt != nil || t.Supervision.state(t) != reviewNeedsUser)
	}
	return t.DoneAt != nil && t.DoneBy != db.DoneByAuto && !(t.DoneBy == db.DoneByReview && t.Supervision.state(t) == reviewAccepted)
}

// acceptAskStale says an ask belongs to an earlier round: the todo was reopened (PATCH done:false, change feedback
// on its card, or a failed start's retry) after the ask, so the old card is dead and the next deploy asks fresh.
// A ReopenedAt the auto-judge's second close leaves is harmless (the new ask is younger); zero AskedAt is a claim.
func acceptAskStale(a *acceptAsk, t Todo) bool {
	return !a.AskedAt.IsZero() && t.ReopenedAt != nil && a.AskedAt.Before(*t.ReopenedAt)
}
