package assistant

import (
	"context"
	"crypto/sha256"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ---- the turn, as it looks in Feishu ----

// botTargetOf is what a run's conversation is to the chat: replies to the bot's messages about it go there.
func botTargetOf(r *channel.Run) botTarget {
	kind := r.TargetKind
	if kind == "" || kind == targetMain {
		kind = targetPersona
	}
	return botTarget{Kind: kind, MachineID: r.MachineID, SessionID: r.SessionID}
}

// feishuSink is the Feishu bot as the pipeline's sink: a progress card replying to the message after a few seconds,
// rewritten every few, the answer written into it (or cards of their own when long), and the message's reaction going
// from OnIt to DONE.
type feishuSink struct{ s *Service }

// The Feishu card comes a second or two after the message and its elapsed time moves every few seconds, new steps
// or not: a long turn must not look stuck.
const (
	feishuProgressAfter = time.Second
	feishuProgressIdle  = 3 * time.Second
)

func (feishuSink) progressIdle() time.Duration { return feishuProgressIdle }

// When Feishu says the card is updated too often, updates stop for 5 s, then 10, 20, at most 30; the answer is not held.
const (
	cardBackoffFirst = 5 * time.Second
	cardBackoffMost  = 30 * time.Second
)

// chansNow is the channels' clock.
func (s *Service) chansNow() time.Time {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clock()
}

func (f feishuSink) progress(ctx context.Context, r *channel.Run, took time.Duration, steps []string, live string) {
	s := f.s
	if f.cotProgressNow(ctx, r) {
		return
	}
	c := progressCard(s.botLang(ctx), r.Label, took, steps, live, s.runWaiting(r), s.runLiveView(r.ID), botTargetOf(r))
	_, prog := s.channelRunByID(r.ID)
	if prog == nil {
		prog = &chanLive{} // a run not followed any more: nothing to remember
	}
	sum := sha256.Sum256([]byte(c.JSON()))
	if card := s.runExt(r, "card"); card != "" {
		now := s.chansNow()
		if sum == prog.sentCard || now.Before(prog.quietUntil) {
			return // nothing new, or Feishu asked us to slow down: a later tick sends the latest
		}
		err := s.botUpdateCard(ctx, card, c)
		switch {
		case err == nil:
			prog.sentCard, prog.backoff = sum, 0
		case feishubot.RateLimited(err):
			prog.backoff = min(max(2*prog.backoff, cardBackoffFirst), cardBackoffMost)
			prog.quietUntil = now.Add(prog.backoff)
			s.logf("bots: progress update: %v; quiet for %s", err, prog.backoff)
		case feishubot.MessageGone(err):
			// Withdrawn or deleted: the next tick posts a new card, and the answer does not go into this one.
			s.setRunExt(r, "card", "")
			prog.sentCard = [32]byte{}
			s.logf("bots: progress card gone: %v", err)
		default:
			s.logf("bots: progress update: %v", err)
		}
		return
	}
	to := runTo(s.runExts(r, "chat", "thread"))
	mid, err := s.botPostTo(ctx, to, r.ReplyTo, c, "", "")
	if err != nil {
		s.logf("bots: progress card: %v", err)
		return
	}
	prog.sentCard, prog.backoff, prog.quietUntil = sum, 0, time.Time{}
	s.setRunExt(r, "card", mid)
	if to.ChatID == "" { // a topic's messages never reach the private chat's routing
		s.rememberBotThread(mid, botTargetOf(r), r.Label)
	}
}

// liveView is what the progress card shows besides the steps (a copy of chanLive's notes) and the time now.
type liveView struct {
	open                []openCall
	calls, read, edited int
	subs                []subtask
	subsMore            int
	now                 time.Time
}

// runLiveView is run id's liveView now.
func (s *Service) runLiveView(id string) liveView {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	v := liveView{now: c.clock()}
	if p := c.live[id]; p != nil {
		v.open, v.subs = slices.Clone(p.open), slices.Clone(p.subs)
		v.calls, v.read, v.edited, v.subsMore = p.calls, len(p.read), len(p.edited), p.subsMore
	}
	return v
}

// runWaiting is the note for a run whose message is not found in its conversation yet: queued behind a turn that
// works ("queued_note"), or not in yet ("preparing_note"); "" once it is.
func (s *Service) runWaiting(r *channel.Run) string {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	switch p := c.live[r.ID]; {
	case p == nil || p.from != 0:
		return ""
	case r.Seen:
		return "assistant-feishu.queued_note"
	}
	return "assistant-feishu.preparing_note"
}

func (f feishuSink) folded(ctx context.Context, o *channel.Run) {
	if o.Ext["card"] != "" {
		_ = f.s.botUpdateCard(ctx, o.Ext["card"], feishubot.NewCard("", "", "").Note(i18n.T(f.s.botLang(ctx), "assistant-feishu.folded")))
	}
	if cot := o.Ext["cot"]; cot != "" {
		_ = f.s.cotCall(ctx, FeishuCotComplete, map[string]any{"cot_id": cot, "message_id": o.Ext["cot_msg"], "reason": "done"})
	}
}

// recalled takes a recalled message's marks off when the run it was folded into goes on without it: its reaction,
// and its own progress card (which said it is answered with the next message) says it was recalled.
func (f feishuSink) recalled(ctx context.Context, m channel.Folded) {
	s := f.s
	if rx := m.Ext["reaction"]; rx != "" && m.ReplyTo != "" {
		s.botUnreact(ctx, m.ReplyTo, rx)
	}
	if card := m.Ext["card"]; card != "" {
		l := s.botLang(ctx)
		c := feishubot.NewCard("", "", "").Note(i18n.T(l, "assistant-feishu.recalled_folded"))
		endHeader(l, c, channel.EndRecalled)
		if err := s.botUpdateCard(ctx, card, c); err != nil {
			s.logf("bots: a recalled message's card: %v", err)
		}
	}
}

// answer writes the answer into the progress card (more cards when long), and marks the messages DONE. When nothing
// of it got out because the hub could not be reached it says so (tried again later); once a piece is out, the rest
// is best effort (a card sent twice would show twice). The card's last line says how the run ended; a quiet end
// still turns the progress card into that line, so it never stays on "Working".
func (f feishuSink) answer(ctx context.Context, r *channel.Run, answer, note string, took time.Duration, final, quiet bool, end channel.End) error {
	s := f.s
	l := s.botLang(ctx)
	all := f.s.runSteps(r.ID)
	foot := endFoot(l, r.Label, took, len(all), final, end)
	if note != "" {
		foot += "\n" + note
	}
	// The process is in the agent message above the answer, or else folded into the answer's first card.
	var steps []string
	if !f.cotFinishNow(ctx, r) {
		steps = append([]string{}, all...)
	}
	card := r.Ext["card"]
	cards := answerCards(l, answer, quiet, steps, foot)
	if quiet && card != "" {
		c := feishubot.NewCard("", "", foot)
		processPanel(l, c, steps)
		cards = []botAnswerCard{{card: c.Note(foot)}}
	}
	// r is a copy of the run (channels_answer.go): its Ext is read without the lock.
	to := runTo(r.Ext)
	var missing []int // the pieces that did not get out (1-based): said at the end, not sent again
	defer func() {
		if len(missing) > 0 {
			s.botNoteTo(ctx, to, r.ReplyTo, missingPiecesNote(l, missing))
		}
	}()
	for i, ac := range cards {
		c, piece := ac.card, ac.fallback
		if i == len(cards)-1 && r.SessionID != "" {
			// The conversation's web link goes under the last piece only, as on pushed and mirrored turns.
			c.Buttons(s.openButton(ctx, r.MachineID, r.SessionID))
		}
		if i == 0 {
			endHeader(l, c, end)
		}
		if quiet && i == 0 {
			if err := s.botUpdateCard(ctx, card, c); err != nil {
				s.logf("bots: the progress card's end: %v", err)
			}
			break
		}
		var mid string
		var err error
		if i == 0 && card != "" {
			if err = s.botUpdateCard(ctx, card, c); err == nil {
				mid = card
			} else {
				s.logf("bots: answer into the progress card: %v; sending it", err)
			}
		}
		if mid == "" {
			mid, err = s.botPostTo(ctx, to, r.ReplyTo, c, piece, "")
		}
		if err != nil {
			if i == 0 && unreachable(err) {
				return err // nothing went out: the whole answer is tried again later
			}
			s.logf("bots: answer: %v", err)
			missing = append(missing, i+1)
			continue
		}
		if to.ChatID == "" {
			s.rememberBotThread(mid, botTargetOf(r), r.Label)
		}
	}
	if !final {
		return nil
	}
	for _, m := range append(r.Folded, channel.Folded{ReplyTo: r.ReplyTo, Ext: r.Ext}) {
		if m.ReplyTo == "" {
			continue
		}
		if rx := m.Ext["reaction"]; rx != "" {
			s.botUnreact(ctx, m.ReplyTo, rx)
		}
		if end == channel.EndRecalled {
			continue // the message is gone: nothing to mark done
		}
		if _, err := s.botReact(ctx, m.ReplyTo, "DONE"); err != nil {
			s.logf("bots: react: %v", err)
		}
	}
	return nil
}

// missingPiecesNote says, in l, which pieces of an answer did not get to Feishu (1-based) and where the whole is.
func missingPiecesNote(l i18n.Lang, missing []int) string {
	nums := make([]string, len(missing))
	for i, n := range missing {
		nums[i] = strconv.Itoa(n)
	}
	return i18n.T(l, "assistant-feishu.missing_pieces", i18n.Args{"count": len(missing), "pieces": strings.Join(nums, listSep(l))})
}

// listSep joins the items of a list in l: Chinese and Japanese use an enumeration comma, English ", ".
func listSep(l i18n.Lang) string {
	if strings.HasPrefix(string(l), "en") {
		return ", "
	}
	return "、"
}

// endFoot is an answer's last line: how it ended, the label and time it took, and how many tools it called (one
// answered first while background work goes on says no end).
func endFoot(l i18n.Lang, label string, took time.Duration, tools int, final bool, end channel.End) string {
	foot := i18n.T(l, "assistant-feishu.took", i18n.Args{"label": label, "took": channel.Duration(l, took)})
	if final {
		key := "assistant-feishu.end_done"
		if end != channel.EndDone {
			key = "assistant-feishu.end_" + string(end)
		}
		foot = i18n.T(l, key) + " · " + foot
	}
	if tools > 0 {
		foot += " · " + i18n.T(l, "assistant-feishu.tools_called", i18n.Args{"count": tools})
	}
	return foot
}

// endHeader gives a card of a run that did not end normally a header saying so, unless the answer has its own.
func endHeader(l i18n.Lang, c *feishubot.Card, end channel.End) {
	if end == channel.EndDone || c.Header != nil {
		return
	}
	color := feishubot.Grey
	if end == channel.EndError || end == channel.EndOffline {
		color = feishubot.Orange
	}
	c.Header = feishubot.NewCard(i18n.T(l, "assistant-feishu.end_"+string(end)), color, "").Header
}

// unreachable: the call did not reach the hub, or did not come back (worth trying again).
func unreachable(err error) bool {
	return errors.Is(err, bus.ErrOffline) || errors.Is(err, rpc.ErrClosed) || errors.Is(err, context.DeadlineExceeded)
}

// progressCard is a run's card while it goes on; waiting: the note while its message is not found in the
// conversation yet (runWaiting), "" once it is. A zero t means no live run: the card carries no Stop button.
func progressCard(l i18n.Lang, label string, took time.Duration, steps []string, live, waiting string, v liveView, t botTarget) *feishubot.Card {
	summary := i18n.T(l, "assistant-feishu.working_summary_took", i18n.Args{"took": channel.Duration(l, took)})
	note := "assistant-feishu.working_note"
	switch waiting {
	case "":
	case "assistant-feishu.queued_note":
		summary, note = i18n.T(l, "assistant-feishu.queued_summary"), waiting
	default:
		summary, note = i18n.T(l, "assistant-feishu.preparing_summary"), waiting
	}
	c := feishubot.NewCard("", "", summary)
	c.Note("⏳ " + i18n.T(l, note, i18n.Args{"label": label, "took": channel.Duration(l, took)}))
	for _, md := range progressLines(l, steps, v) {
		c.Markdown(md)
	}
	if n := v.calls; n > 0 {
		parts := []string{i18n.T(l, "assistant-feishu.count_calls", i18n.Args{"count": n})}
		for _, f := range []struct {
			key string
			n   int
		}{{"assistant-feishu.count_read", v.read}, {"assistant-feishu.count_edited", v.edited}} {
			if f.n == 0 {
				continue
			}
			shown := strconv.Itoa(f.n)
			if f.n >= liveFilesMost {
				shown += "+" // noteFile stops counting there
			}
			parts = append(parts, i18n.T(l, f.key, i18n.Args{"count": f.n, "n": shown}))
		}
		c.Note(strings.Join(parts, " · "))
	}
	if live = strings.TrimSpace(live); live != "" {
		r := []rune(live)
		if len(r) > 400 {
			live = "…" + string(r[len(r)-400:])
		}
		c.Markdown(feishubot.EscapeMarkdown(live))
	}
	// The button is the /stop command on this run's conversation; it is only on the card of a run still in flight.
	if t.Kind != "" {
		c.Buttons(feishubot.Btn(i18n.T(l, "assistant-feishu_cards.stop"), feishubot.Danger,
			map[string]any{"a": "stop", "k": t.Kind, "m": t.MachineID, "s": t.SessionID}))
	}
	return c
}

// progressLines are the progress card's Markdown blocks: the newest call still running and for how long, the last
// steps, the subagents.
func progressLines(l i18n.Lang, steps []string, v liveView) []string {
	var out []string
	if n := len(v.open); n > 0 {
		o := v.open[n-1]
		now := i18n.T(l, "assistant-feishu.now_running", i18n.Args{"step": o.step, "took": channel.Duration(l, max(v.now.Sub(o.since), 0))})
		if n > 1 {
			now += " " + i18n.T(l, "assistant-feishu.and_more", i18n.Args{"count": n - 1})
		}
		out = append(out, now)
	}
	if n := len(steps); n > 0 {
		var b strings.Builder
		if n > 5 {
			b.WriteString(i18n.T(l, "assistant-feishu.earlier_steps", i18n.Args{"count": n - 5}))
			steps = steps[n-5:]
		}
		for _, st := range steps {
			b.WriteString("- " + st + "\n")
		}
		out = append(out, strings.TrimSpace(b.String()))
	}
	if n := len(v.subs); n > 0 {
		var b strings.Builder
		b.WriteString(i18n.T(l, "assistant-feishu.subtasks") + "\n")
		subs := v.subs
		if n > 6 {
			subs = subs[n-6:]
		}
		for _, st := range subs {
			line := feishubot.EscapeMarkdown(st.desc)
			if line == "" {
				line = i18n.T(l, "assistant-feishu.step_subtask")
			}
			if st.kind != "" {
				line += " " + feishubot.Code(st.kind)
			}
			state := "assistant-feishu.subtask_running"
			if st.done {
				state = "assistant-feishu.subtask_done"
			}
			b.WriteString("- " + line + " · " + i18n.T(l, state) + "\n")
		}
		if more := n - len(subs) + v.subsMore; more > 0 {
			b.WriteString(i18n.T(l, "assistant-feishu.and_more", i18n.Args{"count": more}))
		}
		out = append(out, strings.TrimSpace(b.String()))
	}
	return out
}
