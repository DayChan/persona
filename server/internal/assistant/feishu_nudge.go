package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The nudge under a card nobody answered (#6084, docs/extensions/bots.md "Nudging a card nobody answered"): a card the Lark bot
// sent (a question, a plan, a permission) that has waited nudge_after without an answer gets one short reply under it,
// "Still waiting for you: …", so it reaches the user where they answer. Like the reply reminder (agents_reply.go) it is
// a sweep once a minute over what waits, rules only, costing no tokens; unlike it, it follows the card into Lark and
// does not need the card to have been seen.
//
// One nudge per card, ever: the card's record (botCardRef) keeps when it was nudged, so a restart does not nudge again.
// None for a card answered, dismissed or expired (its record is gone), or whose agent is gone. The hub's quiet hours
// (#5806) hold a nudge back: it goes out in the first sweep after them, if the card still waits.

// nudgeEvery is how often the cards are looked at, the reply reminder's.
const nudgeEvery = replyEvery

// nudgeOldest: a card still unnudged this long after it was due is let go (the user left it), as replyOldest.
const nudgeOldest = replyOldest

// A send that fails for a reason that may pass is tried again, at most nudgeMaxTries times in all and only within
// nudgeRetryWindow of the first: Feishu drops a repeated key only for about an hour, so a later try could land twice.
const (
	nudgeMaxTries    = 3
	nudgeRetryWindow = time.Hour
)

// nudgeAfterOf is the setting in the status, in minutes: the default when the hub did not say, 0 off.
func nudgeAfterOf(st FeishuStatus) time.Duration {
	n := assistantwire.NudgeAfterDefault
	if st.NudgeAfter != nil {
		n = *st.NudgeAfter
	}
	return time.Duration(max(n, 0)) * time.Minute
}

func (s *Service) nudgeLoop(ctx context.Context) {
	t := time.NewTicker(nudgeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cctx, cancel := context.WithTimeout(ctx, time.Minute)
			s.nudgeTick(cctx)
			cancel()
		}
	}
}

// nudgeTick nudges the cards that waited nudge_after, outside quiet hours. With no card kept it does nothing, not even
// asking the hub for the bot's status.
func (s *Service) nudgeTick(ctx context.Context) {
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(ctx)
	idle := !b.loaded || len(b.cards) == 0
	b.mu.Unlock()
	if idle {
		return
	}
	st, err := s.feishuStatusKept(ctx)
	if err != nil || !st.Ready || st.Owner == "" {
		return
	}
	after := nudgeAfterOf(st)
	b.mu.Lock()
	if !b.loaded {
		b.mu.Unlock()
		return
	}
	now := b.clock()
	if after <= 0 {
		if b.st.NudgeOffAt.IsZero() {
			b.st.NudgeOffAt = now
			s.saveBotStateLocked()
		}
		b.mu.Unlock()
		return
	}
	turnedOn := !b.st.NudgeOffAt.IsZero() // off before: what is already due is not a burst of nudges, it is let go
	if turnedOn {
		b.st.NudgeOffAt = time.Time{}
		s.saveBotStateLocked()
	}
	var due []string
	for id, c := range b.cards {
		// A card left more than a day past its wait is let go (the user left it).
		if c.MessageID == "" || c.nudgeSettled() || now.Sub(c.At) < after || now.Sub(c.At) > after+nudgeOldest {
			continue
		}
		due = append(due, id)
	}
	slices.SortFunc(due, func(x, y string) int { return b.cards[x].At.Compare(b.cards[y].At) })
	if turnedOn {
		for _, id := range due {
			c := b.cards[id]
			c.SkippedAt = now
			s.keepBotCardLocked(id, &c)
		}
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	if len(due) == 0 {
		return
	}
	quiet, err := s.nudgeQuiet(ctx)
	if err != nil {
		s.logf("bots: nudge: quiet hours: %v", err)
		return
	}
	if quiet {
		return // still due after them: the first sweep outside them nudges
	}
	for _, id := range due {
		s.nudgeCard(ctx, id, st.Owner)
	}
}

// nudgeQuiet says whether the user is in their quiet hours now (the hub's, notify.quiet).
func (s *Service) nudgeQuiet(ctx context.Context) (bool, error) {
	var res struct {
		Quiet bool `json:"quiet"`
	}
	if err := s.appCall(ctx, AppNotify, NotifyQuietGet, map[string]any{}, &res); err != nil {
		return false, err
	}
	return res.Quiet, nil
}

// nudgeCard nudges under the card of interaction id if it still waits and has not been nudged.
func (s *Service) nudgeCard(ctx context.Context, id, owner string) {
	ix, end := s.botCardLook(ctx, id)
	if end != cardWaiting || s.agentGone(ix) { // answered or ended elsewhere, not restored yet, or its agent went away
		return
	}
	b := &s.fbot
	b.mu.Lock()
	c, ok := b.cards[id] // read again: it may have been answered while the hub was asked
	if !ok || c.MessageID == "" || c.nudgeSettled() {
		b.mu.Unlock()
		return
	}
	now := b.clock()
	if !c.TriedAt.IsZero() && now.Sub(c.TriedAt) > nudgeRetryWindow {
		// Feishu drops a repeated key only for about an hour: a send tried again later could land twice.
		c.SkippedAt = now
		s.keepBotCardLocked(id, &c)
		b.mu.Unlock()
		return
	}
	if c.TriedAt.IsZero() {
		c.TriedAt = now
	}
	c.Tries++
	c.NudgedAt = now
	s.keepBotCardLocked(id, &c) // marked before it is sent: a restart in between loses a nudge, never doubles one
	b.mu.Unlock()
	err := s.botNudge(ctx, c, ix, owner)
	if err == nil {
		s.boardChanged() // its line on the waiting-for-you board says it was nudged
		return
	}
	s.logf("bots: nudge under card %s: %v", id, err)
	// Refused (the card or its chat is gone): nothing to nudge under. Otherwise it may not have gone out, and the next
	// sweep tries again under the same key, so one that did go out lands once, up to nudgeMaxTries and nudgeRetryWindow.
	giveUp := chatRefused(err) || feishubot.MessageGone(err) || c.Tries >= nudgeMaxTries
	b.mu.Lock()
	if cur, still := b.cards[id]; still && cur.MessageID == c.MessageID {
		cur.NudgedAt = time.Time{}
		if giveUp {
			cur.SkippedAt = b.clock()
		}
		s.keepBotCardLocked(id, &cur)
	}
	b.mu.Unlock()
}

// agentGone: the agent ix asks for is no longer in its computer's list (a list that is read, and does not have it).
func (s *Service) agentGone(ix proto.Interaction) bool {
	if ix.SessionID == "" {
		return false
	}
	// Only whether it is listed: no copy of the list.
	found, listed := s.nodeSessionsWhere(ix.MachineID, func(c *proto.Session) bool { return c.SessionID == ix.SessionID })
	return listed > 0 && len(found) == 0
}

// botNudge replies under c with the nudge: in a topic group in the topic, @-mentioning the owner, which is what makes
// it reach them there; in the owner's private chat a plain reply, which notifies by itself.
func (s *Service) botNudge(ctx context.Context, c botCard, ix proto.Interaction, owner string) error {
	text := s.nudgeText(ctx, ix)
	to := feishuTarget{NoDetour: true} // under the card or nowhere: a card that was withdrawn is not nudged
	if c.ChatID != "" {
		to.ChatID, to.InThread = c.ChatID, true
		text = feishuMention(owner) + " " + text
	}
	p := to.params(map[string]any{"reply_to": c.MessageID, "text": text, "uuid": nudgeKey(ix.ID)})
	return s.appCall(ctx, AppFeishu, FeishuText, p, nil)
}

// feishuMention is a text message's @ of the user with open id (Feishu's text syntax, which the name inside the tag
// does not matter to: it shows the user's own).
func feishuMention(openID string) string {
	return `<at user_id="` + strings.NewReplacer(`"`, "", "<", "", ">", "").Replace(openID) + `"></at>`
}

// nudgeKey is the key a card's nudge goes out under, so one sent twice (a retry after an error) lands once.
func nudgeKey(id string) string {
	sum := sha256.Sum256([]byte("nudge:" + id))
	return "nudge-" + hex.EncodeToString(sum[:12])
}

// nudgeText is the one line: what the card asks, in the user's language. A permission's is the tool only, never the
// command or the path it would run on.
func (s *Service) nudgeText(ctx context.Context, ix proto.Interaction) string {
	l := s.botLang(ctx)
	what := ""
	switch ix.Kind {
	case proto.IxPermission:
		if ix.ToolName != "" {
			return i18n.T(l, "assistant-feishu_cards.nudge_permission", i18n.Args{"tool": toolDisplayName(ix.ToolName)})
		}
	case proto.IxQuestion:
		what = firstQuestion(ix)
	case proto.IxPlan:
		what = ix.Plan
	case proto.IxScreen:
		what = i18n.T(l, "assistant-feishu_cards.screen_summary")
	case proto.IxElicitation:
		if ix.Elicitation != nil {
			what = ix.Elicitation.Message
		}
	}
	what = clip(oneLine(firstLine(what)), 100)
	if what == "" {
		return i18n.T(l, "assistant-feishu_cards.nudge_bare")
	}
	return i18n.T(l, "assistant-feishu_cards.nudge", i18n.Args{"what": what})
}

// cardNudged: the card asking interaction id was nudged under (the waiting-for-you board marks its row).
func (s *Service) cardNudged(id string) bool {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(context.Background())
	return !b.cards[id].NudgedAt.IsZero()
}
