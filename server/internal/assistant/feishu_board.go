package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The standing board (#6052, docs/extensions/bots.md "The board"): with the bot's `board` switch on, /tasks keeps one card in the
// owner's chat that lists everything waiting for them, and edits that card in place instead of sending another. It is
// edited again, about boardSettle after a card opens or ends or a to-do enters or leaves "Please verify", and once
// when the assistant connects to the hub. Only a card that can no longer be edited (Feishu allows boardEditFor after
// it was sent) or was deleted is replaced by a new one, which is then the board. The message id and the time it was
// sent are in the chat's state (botState.Board). With the switch off /tasks sends its snapshot as it always did.

const (
	// boardSettle is how long a change waits before the board is edited: what comes right behind it is one edit.
	boardSettle = 3 * time.Second
	// boardEditFor is how long after it was sent Feishu lets a card be edited.
	boardEditFor = 14 * 24 * time.Hour
	// boardRowsMax is the most rows one board lists (it is one card); the rest are counted.
	boardRowsMax = 30
	// boardSummaryRunes is how much of a card's question a row quotes.
	boardSummaryRunes = 60
	// boardOffFor is how long after the switch was seen off (or the bot not ready) changes stop scheduling a look: the
	// to-dos change all the time, and each look is a call for the bot's status.
	boardOffFor = time.Minute
	// boardTailBytes is kept free for the "and N more" line and the note under the rows.
	boardTailBytes = 1500
)

// botBoardRef is the board in the chat's state: its message and when it was sent (the edit period counts from then).
type botBoardRef struct {
	MessageID string    `json:"message_id"`
	At        time.Time `json:"at"`
}

// standingBoard is what keeps the board current: the settle timer, and the lock that lets one send or edit at a time
// (the /tasks command and the timer). now and after are the clock and the timer; tests replace them.
type standingBoard struct {
	mu    sync.Mutex
	armed bool // an edit is scheduled
	run   sync.Mutex
	now   func() time.Time
	after func(d time.Duration, f func())
	// done, when set, is called when an edit has been through (tests: it runs on a timer's goroutine).
	done func()
	// written is what the board last said (its message and its rows): a settle-time edit that would say the same is
	// not made, so a to-do that changes something the board does not show (its title, its agents) costs no call.
	written struct{ id, rows string }
	// fleetUp: the fleet has been brought up since this process started (FleetUp), so the pending cards it lists are
	// the real ones. Before that the list is empty because it is not read yet, and a board written then would say
	// "nothing is waiting".
	fleetUp atomic.Bool
	// offAt is when the refresh last found the switch off or the bot not ready (zero: on); under mu.
	offAt time.Time
}

func (p *standingBoard) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

func (p *standingBoard) later(d time.Duration, f func()) {
	if p.after != nil {
		p.after(d, f)
		return
	}
	time.AfterFunc(d, f)
}

// boardRef is the board the chat has, nil when it has none.
func (s *Service) boardRef(ctx context.Context) *botBoardRef {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(ctx)
	if b.st.Board == nil {
		return nil
	}
	ref := *b.st.Board
	return &ref
}

// setBoardRef remembers the board (nil: forgets it) in the chat's state.
func (s *Service) setBoardRef(ctx context.Context, ref *botBoardRef) {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(ctx)
	if !b.loaded {
		return
	}
	b.st.Board = ref
	s.saveBotStateLocked()
}

// boardChanged says something the board shows changed: a card opened or ended, a to-do entered or left "Please
// verify", the assistant connected. The board is edited boardSettle later, once for all that came meanwhile. A chat with
// no board has nothing to edit, and nothing is scheduled.
func (s *Service) boardChanged() {
	if s.boardRef(context.Background()) == nil {
		return
	}
	p := &s.standing
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.armed || !p.offAt.IsZero() && p.clock().Sub(p.offAt) < boardOffFor {
		return
	}
	p.armed = true
	p.later(boardSettle, s.boardRefresh)
}

// fleetReady: the pending cards can be read. Without devices there is no fleet and nothing to wait for.
func (s *Service) fleetReady() bool { return !s.fleetOn() || s.standing.fleetUp.Load() }

// boardFleetUp is FleetUp having read the pending cards: the board is brought up to date once.
func (s *Service) boardFleetUp() {
	s.standing.fleetUp.Store(true)
	s.boardChanged()
}

// boardRefresh edits the board to what is waiting now (or sends a new one when it can no longer be edited), unless the
// switch is off or the bot is not ready.
func (s *Service) boardRefresh() {
	p := &s.standing
	p.mu.Lock()
	p.armed = false // a change from here on is for the next edit
	p.mu.Unlock()
	if p.done != nil {
		defer p.done()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p.run.Lock()
	defer p.run.Unlock()
	if !s.fleetReady() {
		return
	}
	// The status first: it starts the chat afresh when the bot was paired again, and the board of the old owner goes
	// with it.
	// The workbench (feishu_workbench_pin.go) is the board drawn as a home card: either switch keeps it current.
	f, ready := s.feishuFeatures(ctx)
	on := ready && (f.Board || s.workbenchOn(f, ready))
	p.mu.Lock()
	if on {
		p.offAt = time.Time{}
	} else {
		p.offAt = p.clock()
	}
	p.mu.Unlock()
	if !on {
		return
	}
	ref := s.boardRef(ctx)
	if ref == nil {
		return
	}
	if _, err := s.boardPut(ctx, ref, true); err != nil {
		s.logf("bots: board: %v", err)
	}
}

// standingBoardCommand is /tasks with the switch on: the first time the board is sent; afterwards it is edited and
// the owner is told so in a line that replies to the board (the quote in the reply takes them to it). false: nothing
// was done for the command (the board could not be sent or edited), so it answers as it does with the switch off.
func (s *Service) standingBoardCommand(ctx context.Context) bool {
	p := &s.standing
	p.run.Lock()
	defer p.run.Unlock()
	p.mu.Lock()
	p.offAt = time.Time{} // the switch is on: it was just read
	p.mu.Unlock()
	ref := s.boardRef(ctx)
	edited, err := s.boardPut(ctx, ref, false)
	if err != nil {
		s.logf("bots: board: %v", err)
		return false
	}
	if edited {
		s.botNote(ctx, ref.MessageID, i18n.T(s.botLang(ctx), "assistant-feishu_cards.board_pointer"))
	}
	return true
}

// boardPut writes the board as it is now: edits ref in place, or sends a new board (and remembers it) when there is
// none, its edit period is over, or the edit fails for any reason that is not a passing one (feishubot.Transient): it
// was deleted or withdrawn, the period is over (Feishu's 230031: messages can be edited within 14 days of sending,
// open.feishu.cn "Update sent message card"), it is not a card (a refused card goes out as rich text), or Feishu does not
// take it for another reason; a board that cannot be edited must not stay the board for good. edited is whether it was
// edited in place. A passing failure (the network, a rate limit, a server error, a token) leaves the board as it was and
// is returned. The new board goes out under a key made of the old one, so a send that timed out and is tried again is
// still one message. After it, the board is looked at again: what changed while it was going out. quiet: an edit
// that would say what the board already says is not made (the settle-time refresh; /tasks always writes). p.run is held.
func (s *Service) boardPut(ctx context.Context, ref *botBoardRef, quiet bool) (edited bool, err error) {
	p := &s.standing
	card, fallback, rows := s.standingCard(ctx)
	if ref != nil && p.clock().Sub(ref.At) < boardEditFor {
		if quiet && p.written.id == ref.MessageID && p.written.rows == rows {
			return false, nil
		}
		err := s.botUpdateCard(ctx, ref.MessageID, card)
		if err == nil {
			p.written.id, p.written.rows = ref.MessageID, rows
			return true, nil
		}
		if feishubot.Transient(err) {
			return false, err
		}
		s.logf("bots: the board can't be edited any more (%v): sending a new one", err)
	}
	mid, err := s.botPostKeyed(ctx, "", card, fallback, s.boardKey(ctx, ref))
	if err != nil {
		return false, err
	}
	if s.workbench.on.Load() {
		s.workbenchPin(ctx, mid) // the workbench that replaces one is pinned in its place
	}
	s.setBoardRef(ctx, &botBoardRef{MessageID: mid, At: p.clock()})
	p.written.id, p.written.rows = mid, rows
	s.boardChanged()
	return false, nil
}

// boardKey is the idempotency key of the board that replaces old (nil: the first one of this pairing): the same
// replacement always has the same key, the next one a different key.
func (s *Service) boardKey(ctx context.Context, old *botBoardRef) string {
	from := ""
	if old != nil {
		from = old.MessageID + "@" + old.At.UTC().Format(time.RFC3339Nano)
	} else {
		b := &s.fbot
		b.mu.Lock()
		s.loadBotLocked(ctx)
		if b.st.Pairing != nil {
			from = *b.st.Pairing
		}
		b.mu.Unlock()
	}
	sum := sha256.Sum256([]byte("board|" + from))
	return "bd:" + hex.EncodeToString(sum[:])[:32]
}

// boardRow is one thing waiting for the owner.
type boardRow struct {
	blocking bool // an Agent (or the main conversation) is held up by it; a to-do waiting to be verified is not
	waited   time.Duration
	id       string // breaks a tie
	summary  string
	from     string
	link     string
	nudged   bool // the bot nudged under its card in the chat (feishu_nudge.go, #6084)
	// asked is when the "Please verify" ask of a to-do waiting to be verified was made (its card, if it has one, went out then) (zero: the row is not one); id is the to-do.
	asked time.Time
}

// boardRows are the things waiting, in the order the board shows them: what holds up an Agent first, the longest
// waiting first, then the to-dos waiting for the owner to verify them, the longest waiting first.
func (s *Service) boardRows(ctx context.Context, l i18n.Lang, web string, now time.Time) []boardRow {
	var rows []boardRow
	for _, ix := range s.pendingInteractions() {
		if s.interactionEnded(ix.ID) { // over, though its entry is still being taken away
			continue
		}
		link := web
		if ix.SessionID != "" {
			link = sessionLink(web, ix.MachineID, ix.SessionID, s.sessionAssistantLocal(ix.MachineID, ix.SessionID))
		}
		rows = append(rows, boardRow{blocking: true, waited: boardWaited(now, ix.CreatedAt), id: ix.ID,
			summary: boardSummary(l, ix), from: s.boardFrom(l, ix.MachineID, ix.SessionID, ix.SessionTitle), link: link, nudged: s.cardNudged(ix.ID)})
	}
	for _, a := range s.awaitingAcceptance(ctx) {
		summary := i18n.T(l, "assistant-feishu_cards.board_verify", i18n.Args{"title": clip(oneLine(a.title), 40)})
		if a.ask.Conclusion { // the task's conclusion, as its card says it
			summary = i18n.T(l, "assistant-feishu_cards.board_verdict", i18n.Args{"title": clip(oneLine(a.title), 40), "verdict": verdictText(l, &a.ask)})
		}
		rows = append(rows, boardRow{waited: boardWaited(now, a.since), id: a.todo, summary: summary,
			from: s.boardFrom(l, a.machine, a.session, ""), link: todoLink(web, a.todo), asked: a.since})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].blocking != rows[j].blocking {
			return rows[i].blocking
		}
		if rows[i].waited != rows[j].waited {
			return rows[i].waited > rows[j].waited
		}
		return rows[i].id < rows[j].id
	})
	return rows
}

func boardWaited(now, since time.Time) time.Duration {
	if since.IsZero() || since.After(now) {
		return 0
	}
	return now.Sub(since)
}

// boardSummary is the line a card gets on the board: what it asks, clipped. A permission says only that it needs the
// owner's approval, never the command or the path (the board sits in a chat that may be seen in a screenshot), and a
// terminal dialog only that it needs them (its text is the screen's).
func boardSummary(l i18n.Lang, ix proto.Interaction) string {
	switch ix.Kind {
	case proto.IxPermission:
		return i18n.T(l, "assistant-feishu_cards.board_needs_approval")
	case proto.IxScreen:
		return answeredT(l, "needs_you")
	}
	return clip(oneLine(interactionQuestion(l, ix)), boardSummaryRunes)
}

// boardFrom says who asked: Persona (a conversation of its own) or the Agent, by its title.
func (s *Service) boardFrom(l i18n.Lang, machine, session, title string) string {
	if session == "" || s.sessionAssistantLocal(machine, session) {
		return i18n.T(l, "assistant-feishu_cards.board_from_persona")
	}
	name := transcript.Title(title)
	if name == "" {
		if sess, ok := s.nodeSession(machine, session); ok {
			name = transcript.Title(sess.Title)
		}
	}
	if name == "" {
		return i18n.T(l, "assistant-feishu_cards.board_from_agent_unnamed")
	}
	return i18n.T(l, "assistant-feishu_cards.board_from_agent", i18n.Args{"name": clip(oneLine(name), 30)})
}

// boardWait is how long something has waited, in l: "2 min", "1 h 20 min", "3 d".
func boardWait(l i18n.Lang, d time.Duration) string {
	switch {
	case d < time.Minute:
		return i18n.T(l, "assistant-feishu_cards.board_wait_now")
	case d < time.Hour:
		return i18n.T(l, "assistant-feishu_cards.board_wait_minutes", i18n.Args{"count": int(d / time.Minute)})
	case d < 24*time.Hour:
		h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
		if m == 0 {
			return i18n.T(l, "assistant-feishu_cards.board_wait_hours", i18n.Args{"count": h})
		}
		return i18n.T(l, "assistant-feishu_cards.board_wait_hours_minutes", i18n.Args{"hours": h, "minutes": m})
	}
	return i18n.T(l, "assistant-feishu_cards.board_wait_days", i18n.Args{"count": int(d / (24 * time.Hour))})
}

// awaitingAccept is a to-do waiting for the owner to verify it.
type awaitingAccept struct {
	todo, title, machine, session string
	since                         time.Time
	ask                           acceptAsk // its verdict for a conclusion's row (conclusion.go)
}

// awaitingAcceptance are the to-dos whose "Please verify" ask is up (with a card or without, NoCard) and not passed: the ones that can still be passed
// (not closed by the owner or a review, not asked before the latest reopen), in the order they were asked. Read only:
// the sweeps that forget the ended ones are the reminders' and the snapshot's.
func (s *Service) awaitingAcceptance(ctx context.Context) []awaitingAccept {
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return nil
	}
	asks := map[string]acceptAsk{}
	for id, a := range b.asks {
		if !a.AskedAt.IsZero() && a.AcceptedAt.IsZero() {
			asks[id] = *a
		}
	}
	b.mu.Unlock()
	var out []awaitingAccept
	for id, a := range asks {
		t, ok := s.todoNow(id)
		if !ok || acceptAskEnded(&a, t) || acceptAskStale(&a, t) {
			continue
		}
		out = append(out, awaitingAccept{todo: id, title: t.Title, machine: a.MachineID, session: a.SessionID, since: a.AskedAt, ask: a})
	}
	slices.SortFunc(out, func(x, y awaitingAccept) int {
		if c := x.since.Compare(y.since); c != 0 {
			return c
		}
		return strings.Compare(x.todo, y.todo)
	})
	return out
}

// boardCard is the board and the same as plain text (the fallback when Feishu refuses the card): one line a thing,
// with what is waiting and a note of when it was written; when nothing is, it says so. said is the card without that
// note, to tell an edit that would change nothing.
func (s *Service) boardCard(ctx context.Context) (card *feishubot.Card, fallback, said string) {
	l, web := s.botLang(ctx), s.webBase(ctx)
	now := s.standing.clock()
	rows := s.boardRows(ctx, l, web, now)
	title := i18n.T(l, "assistant-feishu_cards.board_title")
	summary := i18n.T(l, "assistant-feishu_cards.board_empty")
	color := feishubot.Green
	if len(rows) > 0 {
		summary = i18n.T(l, "assistant-feishu_cards.board_summary", i18n.Args{"count": len(rows)})
		color = feishubot.Blue
		if rows[0].blocking {
			color = feishubot.Orange
		}
	}
	c := feishubot.NewCard(title, color, summary)
	var plain []string
	if len(rows) == 0 {
		c.Markdown(i18n.T(l, "assistant-feishu_cards.board_empty"))
		plain = append(plain, i18n.T(l, "assistant-feishu_cards.board_empty"))
	}
	// One card: the rows that fit its byte and element budget, the rest counted.
	budget := reportCardMaxBytes - boardTailBytes - len(c.JSON())
	shown := 0
	for i, r := range rows {
		md := boardRowMarkdown(l, i+1, r)
		n := feishuElBytes(func(c *feishubot.Card) { c.Markdown(md) })
		if shown >= boardRowsMax || n > budget {
			break
		}
		budget -= n
		c.Markdown(md)
		plain = append(plain, fmt.Sprintf("%d. %s · %s · %s", i+1, r.summary, i18n.T(l, "assistant-feishu_cards.board_waited", i18n.Args{"wait": boardWait(l, r.waited)}), r.from))
		shown++
	}
	if more := len(rows) - shown; more > 0 {
		line := i18n.T(l, "assistant-feishu_cards.board_more", i18n.Args{"count": more})
		c.Markdown(feishubot.EscapeMarkdown(line))
		plain = append(plain, line)
	}
	said = c.JSON()
	c.Note(i18n.T(l, "assistant-feishu_cards.board_updated", i18n.Args{"time": now.In(s.location(ctx)).Format("15:04")}))
	return c, title + "\n" + clip(strings.Join(plain, "\n"), reportFallbackMax), said
}

// boardRowMarkdown is one row: its number and what it is, then how long it has waited, whether it was nudged, who asked and a link.
func boardRowMarkdown(l i18n.Lang, n int, r boardRow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%d** %s\n", n, feishubot.EscapeMarkdown(r.summary))
	b.WriteString(feishubot.EscapeMarkdown(i18n.T(l, "assistant-feishu_cards.board_waited", i18n.Args{"wait": boardWait(l, r.waited)})))
	if r.nudged {
		b.WriteString(" · " + i18n.T(l, "assistant-feishu_cards.board_nudged"))
	}
	b.WriteString(" · " + feishubot.EscapeMarkdown(r.from))
	if boardLinkOK(r.link) {
		b.WriteString(" · [" + i18n.T(l, "assistant-feishu_cards.board_open") + "](" + r.link + ")")
	}
	return b.String()
}

// boardLinkOK: u is an http(s) address with a host that a Markdown link can hold (card links are documented for
// http(s) only, as safeLinkButton checks).
func boardLinkOK(u string) bool {
	p, err := url.Parse(u)
	return err == nil && u != "" && (p.Scheme == "http" || p.Scheme == "https") && p.Host != "" && !strings.ContainsAny(u, "() \t\r\n")
}
