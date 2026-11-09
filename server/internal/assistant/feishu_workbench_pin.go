package assistant

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/proto"
)

// The pinned workbench (#1961, docs/extensions/bots.md "The workbench"). With the `workbench` switch on, the standing
// board (feishu_board.go) is drawn as the home card (homeCard), and it is kept where the owner finds it: summoning it
// (/home, or the workbench button every bot card then carries) posts a fresh one at the bottom of the chat, pins it,
// takes the bot's other pins off and withdraws the card before, so there is only ever one. Between changes the board
// already follows (a card opening or ending), it is looked at every workbenchEvery for what else it shows (the agents,
// the Inbox, the next meeting) and redrawn only when that changed.

const (
	// workbenchEvery is how often the pinned workbench is looked at for changes the board's own triggers miss.
	workbenchEvery = time.Minute
	// workbenchStampMost keeps a card the workbench button is added to inside the size cards are budgeted to
	// (reportCardMaxBytes, about 24 KB): a card near it goes as it is.
	workbenchStampMost = 22000
)

// workbenchState is what the workbench keeps in memory: whether the switch was on when last read (the posts read it
// without a call), and the loop that looks at the pinned card.
type workbenchState struct {
	on   atomic.Bool
	loop sync.Once
}

// workbenchOn notes the switch as just read: the stamp follows it, and the first time it is on the loop starts.
func (s *Service) workbenchOn(f FeishuFeatures, ready bool) bool {
	on := ready && f.Workbench
	s.workbench.on.Store(on)
	if on {
		s.workbench.loop.Do(func() { go s.workbenchLoop() })
	}
	return on
}

func (s *Service) workbenchLoop() {
	t := time.NewTicker(workbenchEvery)
	defer t.Stop()
	for range t.C {
		if !s.workbench.on.Load() || s.boardRef(context.Background()) == nil {
			continue
		}
		s.boardRefresh()
	}
}

// standingCard is what the standing board shows: the home card with the workbench on, the waiting list without.
func (s *Service) standingCard(ctx context.Context) (card *feishubot.Card, fallback, said string) {
	if f, ready := s.feishuFeatures(ctx); s.workbenchOn(f, ready) {
		return s.homeCard(ctx, f)
	}
	return s.boardCard(ctx)
}

// workbenchStamp adds the workbench button to a card the bot posts or redraws, with the switch on: any card can bring
// up the latest workbench. Not to one that has a way back already (the workbench's own cards) or is near its size.
func (s *Service) workbenchStamp(ctx context.Context, c *feishubot.Card) *feishubot.Card {
	if c == nil || !s.workbench.on.Load() {
		return c
	}
	js := c.JSON()
	if strings.Contains(js, `"a":"nav"`) || len(js) > workbenchStampMost {
		return c
	}
	return c.Buttons(wbNavButton(wbT(s.botLang(ctx), "btn_home"), feishubot.Default, "home"))
}

// summonWorkbench posts a fresh workbench at the bottom of the chat, pins it as the only pin of the bot's, and
// withdraws the one before (too old to withdraw, it says where the workbench went). It is the board from then on.
func (s *Service) summonWorkbench(ctx context.Context) {
	p := &s.standing
	p.run.Lock()
	defer p.run.Unlock()
	card, fallback, said := s.standingCard(ctx)
	if !s.workbench.on.Load() {
		return // switched off meanwhile
	}
	old := s.boardRef(ctx)
	mid, err := s.botPost(ctx, "", card, fallback)
	if err != nil {
		s.logf("bots: workbench: %v", err)
		return
	}
	s.workbenchPin(ctx, mid)
	s.setBoardRef(ctx, &botBoardRef{MessageID: mid, At: p.clock()})
	p.written.id, p.written.rows = mid, said
	if old != nil && old.MessageID != mid {
		s.workbenchRetire(ctx, old.MessageID)
	}
}

// workbenchPin pins the workbench and takes the bot's other pins off, so an older workbench this state lost track of
// does not stay pinned. A pin that fails leaves the card where it is: still the workbench.
func (s *Service) workbenchPin(ctx context.Context, mid string) {
	if err := s.appCall(ctx, AppFeishu, FeishuPin, map[string]any{"message_id": mid, "exclusive": true}, nil); err != nil {
		s.logf("bots: workbench pin: %v", err)
	}
}

// workbenchRetire withdraws a workbench that is no longer the one; one Feishu no longer lets the bot withdraw is
// redrawn as a line that says where the workbench went.
func (s *Service) workbenchRetire(ctx context.Context, mid string) {
	if err := s.appCall(ctx, AppFeishu, FeishuRecall, map[string]any{"message_id": mid}, nil); err == nil {
		return
	}
	l := s.botLang(ctx)
	stub := feishubot.NewCard(wbT(l, "desk_moved_title"), feishubot.Grey, wbT(l, "desk_moved_title")).Markdown(wbT(l, "desk_moved"))
	if err := s.botUpdateCard(ctx, mid, stub); err != nil {
		s.logf("bots: workbench retire: %v", err)
	}
}

// workbenchRefresh redraws the workbench now (its Refresh button), whatever it said before.
func (s *Service) workbenchRefresh(ctx context.Context) {
	p := &s.standing
	p.run.Lock()
	defer p.run.Unlock()
	ref := s.boardRef(ctx)
	if ref == nil {
		return
	}
	if _, err := s.boardPut(ctx, ref, false); err != nil {
		s.logf("bots: workbench refresh: %v", err)
	}
}

// workbenchBringCard posts a waiting question or approval again at the bottom of the chat, its old card withdrawn:
// the home card's "Answer" button, for a card far up the chat.
func (s *Service) workbenchBringCard(ctx context.Context, id string) {
	var ix proto.Interaction
	found := false
	for _, p := range s.pendingInteractions() {
		if p.ID == id {
			ix, found = p, true
			break
		}
	}
	if !found || s.interactionEnded(id) {
		return
	}
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(ctx)
	old := ""
	if c, ok := b.cards[id]; ok {
		old = c.MessageID
	}
	s.keepBotCardLocked(id, nil)
	b.mu.Unlock()
	if old != "" {
		if err := s.appCall(ctx, AppFeishu, FeishuRecall, map[string]any{"message_id": old}, nil); err != nil {
			s.logf("bots: workbench: withdraw a card: %v", err)
		}
	}
	s.postInteractionCard(ctx, ix)
}
