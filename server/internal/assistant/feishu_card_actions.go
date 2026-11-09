package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// --- card buttons ---

// FeishuAction is feishu.card_action: the owner pressed one of the bot's cards (the hub checked it is the owner);
// the answer is the toast and the card to show instead. Feishu waits 3 seconds for it.
func (s *Service) FeishuAction(ctx context.Context, p FeishuAction) (*feishubot.CardResponse, error) {
	var a feishubot.CardAction
	if err := json.Unmarshal(p.Event, &a); err != nil {
		return nil, badRequest(err)
	}
	resp := s.botCardAction(ctx, &a, p.Action)
	if resp != nil && resp.Card != nil {
		if c, ok := resp.Card.Data.(*feishubot.Card); ok {
			s.workbenchStamp(ctx, c) // the card drawn in its place keeps its way back to the workbench
		}
	}
	return resp, nil
}

// botCardAction answers a press; action is the owner's action for the card it presses (what answers it).
func (s *Service) botCardAction(ctx context.Context, a *feishubot.CardAction, action string) *feishubot.CardResponse {
	l := s.botLang(ctx)
	switch a.Str("a") {
	case "ix":
		return s.answerFromCard(ctx, a, action)
	case "accept":
		// The hub checked the owner pressed it; marking a task verified approves nothing.
		ok, err := false, error(nil)
		ask, _ := s.askCopy(a.Str("id"))
		if at := a.Str("asked_at"); at != "" { // a button of a briefing card: it accepts the round it was asked in (briefing.go)
			ok, err = s.acceptTryFromBrief(ctx, a.Str("id"), at)
		} else {
			ok, err = s.acceptTry(ctx, a.Str("id"), a.Context.MessageID)
		}
		if errors.Is(err, errAcceptStore) { // the asks could not be read or saved: not accepted, the button works again
			return toast("error", i18n.T(l, "http-status.unavailable"))
		}
		if err != nil { // a recommendation that did not reach the agent: not taken, nothing recorded, the button works again
			return toast("error", i18n.T(l, "assistant-reports.accept_not_taken", i18n.Args{"error": clip(oneLine(i18n.Localize(err, l)), 120)}))
		}
		if ok {
			return toast("success", acceptedNote(l, &ask)) // "verified", "got it", or the recommendation taken (conclusion.go)
		}
		return toast("info", i18n.T(l, "assistant-feishu_cards.already_handled"))
	case "later_done": // the Handled button of a question that can wait, on a briefing card (later.go)
		ok, err := s.laterHandled(ctx, a.Str("id"), time.Now())
		if err != nil { // the queue can't be read or written: the question stays, the button works again
			return toast("error", i18n.T(l, "http-status.unavailable"))
		}
		if ok {
			return toast("success", i18n.T(l, "assistant-briefing.later_handled"))
		}
		return toast("info", i18n.T(l, "assistant-feishu_cards.already_handled"))
	case "switch":
		f, _ := s.feishuFeatures(ctx)
		kind, m, sess, title := a.Str("k"), a.Str("m"), a.Str("s"), a.Str("t")
		t := botTarget{Kind: kind, MachineID: m, SessionID: sess}
		if kind == targetMain {
			t = botTarget{Kind: targetMain}
		}
		if err := botAllowed(l, f, t); err != nil {
			return toast("error", err.Error())
		}
		s.setBotCurrent(t)
		// The callback only carries the pressed card's own message id (no thread root, no chat beyond its id): bind
		// the card itself. A quote reply to it has it as the parent, and a topic started on it has it as the root,
		// so botTargetFor routes both to this target.
		s.rememberBotThread(a.Context.MessageID, t, title)
		return toast("success", i18n.T(l, "assistant-feishu_cards.now_to_reply", i18n.Args{"title": clip(title, 30)}))
	case "start_form":
		if f, _ := s.feishuFeatures(ctx); !f.Agents {
			return toast("error", i18n.T(l, "assistant-feishu_cards.agents_off_short"))
		}
		// Feishu gives the callback 3 s and the post goes over the bus to the feishu app, so the /start form is
		// posted from a goroutine (as botStartAgent posts its card), quoting the pressed list card.
		mid := a.Context.MessageID
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if _, err := s.botPost(cctx, mid, s.startAgentCard(cctx, ""), ""); err != nil {
				s.logf("bots: start form: %v", err)
			}
		}()
		return toast("info", i18n.T(l, "assistant-feishu_cards.start_form_opening"))
	case "start":
		if f, _ := s.feishuFeatures(ctx); !f.Agents {
			return toast("error", i18n.T(l, "assistant-feishu_cards.agents_off_short"))
		}
		machine, cwd, prompt, mode := a.Form("machine"), strings.TrimSpace(a.Form("cwd")), strings.TrimSpace(a.Form("prompt")), a.Form("mode")
		if machine == "" || cwd == "" {
			return toast("warning", i18n.T(l, "assistant-feishu_cards.pick_machine_dir"))
		}
		// A double tap, Feishu's 3 s redelivery, and the hub's own resend all arrive as presses of the same card
		// message: within the window only the first starts an agent, the rest get the same toast.
		mid := a.Context.MessageID
		var at time.Time
		if mid != "" {
			var pressed bool
			if pressed, at = s.startCardPressed(mid); pressed {
				return toast("info", i18n.T(l, "assistant-feishu_cards.starting"))
			}
		}
		go func() {
			if err := s.botStartAgent(machine, cwd, prompt, mode); err != nil && mid != "" {
				// Nothing started, so a retry press of the same card must go through. at makes this a compare-and-swap:
				// a failure arriving after the window only frees the slot if a later press has not re-stamped it.
				s.forgetStartCard(mid, at)
			}
		}()
		return toast("info", i18n.T(l, "assistant-feishu_cards.starting"))
	case "nav": // a workbench button (feishu_workbench.go)
		return s.workbenchNav(ctx, a)
	case "ib": // an Inbox button of the workbench's /inbox card
		return s.workbenchInboxAction(ctx, a)
	case "stop":
		// The progress card's Stop button: the run's own conversation, carried in the button when the card was built.
		f, _ := s.feishuFeatures(ctx)
		t := botTarget{Kind: a.Str("k"), MachineID: a.Str("m"), SessionID: a.Str("s")}
		out := s.stopConversation(ctx, f, t)
		return toast(out.kind, out.message)
	}
	return toast("info", i18n.T(l, "assistant-feishu_cards.card_expired"))
}

func toast(kind, text string) *feishubot.CardResponse {
	return &feishubot.CardResponse{Toast: &feishubot.Toast{Type: kind, Content: text}}
}

// cardAnswerWait is how long a press waits for its answer to go through before it says "Processing…": the hub has
// Feishu's 3 seconds, and the press came over the bus.
var cardAnswerWait = 1500 * time.Millisecond

// answerFromCard turns a press on an interaction card into the same answer the app would give, and resolves it.
func (s *Service) answerFromCard(ctx context.Context, a *feishubot.CardAction, action string) *feishubot.CardResponse {
	l := s.botLang(ctx)
	id := a.Str("id")
	var got struct {
		Interaction proto.Interaction `json:"interaction"`
		OK          bool              `json:"ok"`
		Status      string            `json:"status"`
	}
	if err := s.appCall(ctx, AppInteractions, InteractionsGet, map[string]any{"interaction_id": id}, &got); err != nil {
		return toast("error", clip(oneLine(err.Error()), 80))
	}
	if !got.OK && !finalStatus(got.Status) {
		// Not up now, but not known to be over either (in the history, not restored yet; or the history could not
		// say): the card stays as it is.
		return toast("info", endedText(l, got.Status, "assistant-feishu_cards.already_handled"))
	}
	if !got.OK {
		// Over for good (answered elsewhere, maybe while the assistant was away): this card is closed as such, its
		// buttons gone.
		b := &s.fbot
		b.mu.Lock()
		s.loadBotLocked(ctx)
		card, had := b.cards[id]
		s.keepBotCardLocked(id, nil)
		b.mu.Unlock()
		ix := proto.Interaction{ID: id}
		if had {
			ix = card.Ix
		}
		ended := endedText(l, got.Status, "assistant-channels.ended")
		return &feishubot.CardResponse{Toast: &feishubot.Toast{Type: "info", Content: endedText(l, got.Status, "assistant-feishu_cards.already_handled")},
			Card: s.answeredCard(ctx, ix, ended).Raw()}
	}
	ix := got.Interaction
	yes, no := true, false
	var r proto.Resolution
	switch {
	case a.Str("dismiss") == "1":
		r.Dismiss = true
	case ix.Kind == proto.IxQuestion && a.Str("form") == "1":
		r.Answers = map[string]string{}
		for i, q := range ix.Questions {
			ans := strings.TrimSpace(a.Form("t" + strconv.Itoa(i)))
			if ans == "" {
				ans = a.Form("q" + strconv.Itoa(i))
			}
			if ans != "" {
				r.Answers[q.Question] = ans
			}
		}
		if len(r.Answers) < len(ix.Questions) {
			return toast("warning", i18n.T(l, "assistant-feishu_cards.answer_each"))
		}
	case ix.Kind == proto.IxQuestion:
		i, _ := strconv.Atoi(a.Str("q"))
		if i < 0 || i >= len(ix.Questions) {
			return toast("error", i18n.T(l, "assistant-feishu_cards.question_mismatch"))
		}
		ans := a.Str("v")
		if a.Str("free") == "1" {
			ans = strings.TrimSpace(a.Form("text"))
		}
		if ans == "" {
			return toast("warning", i18n.T(l, "assistant-feishu_cards.write_answer"))
		}
		r.Answers = map[string]string{ix.Questions[i].Question: ans}
	case ix.Kind == proto.IxPlan && a.Str("approve") == "1":
		r.Approve, r.Mode = &yes, a.Str("mode")
	case ix.Kind == proto.IxPlan:
		r.Approve, r.Feedback = &no, strings.TrimSpace(a.Form("feedback"))
	case ix.Kind == proto.IxPermission:
		if len(ix.Fields) > 0 {
			return toast("warning", i18n.T(l, "assistant-feishu_cards.fill_in_app"))
		}
		if a.Str("allow") == "1" {
			r.Allow, r.Always = &yes, a.Str("always") == "1"
		} else {
			r.Allow = &no
		}
	case ix.Kind == proto.IxScreen && a.Str("opt") != "":
		r.Option = a.Str("opt")
	case ix.Kind == proto.IxScreen && a.Str("typed") == "1":
		text := a.Form("text")
		if text == "" {
			return toast("warning", i18n.T(l, "assistant-feishu_cards.write_text_first"))
		}
		r.Keys, r.Text = []string{"enter"}, text
	case ix.Kind == proto.IxScreen && a.Str("keys") != "":
		r.Keys = []string{a.Str("keys")}
	default:
		return toast("error", i18n.T(l, "assistant-feishu_cards.unknown_action"))
	}
	// This card is closed by the answer below, not again when the interaction ends.
	b := &s.fbot
	b.mu.Lock()
	card, had := b.cards[id]
	s.keepBotCardLocked(id, nil)
	b.mu.Unlock()
	restore := func(ctx context.Context) {
		if had {
			s.botRestoreCard(ctx, id, card)
		}
	}
	how := resolutionText(l, ix, r)
	type outcome struct {
		ok  bool
		err error
	}
	// Screen and Codex cards act on the terminal and can take a few seconds; Feishu waits 3 s for this answer.
	done := make(chan outcome, 1)
	go func() {
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		ok, err := s.resolveInteraction(rctx, channel.Feishu, id, r, action)
		done <- outcome{ok, err}
	}()
	select {
	case o := <-done:
		switch {
		case o.err != nil:
			restore(ctx)
			return toast("error", clip(oneLine(o.err.Error()), 80))
		case !o.ok:
			return toast("info", i18n.T(l, "assistant-feishu_cards.already_handled"))
		}
		return &feishubot.CardResponse{Toast: &feishubot.Toast{Type: "success", Content: how}, Card: s.answeredCard(ctx, ix, how).Raw()}
	case <-time.After(cardAnswerWait):
		msg := a.Context.MessageID
		go func() {
			o := <-done
			if o.err != nil || !o.ok {
				if o.err != nil {
					s.logf("bots: answer %s: %v", id, o.err)
				}
				rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				restore(rctx)
				return
			}
			if msg != "" {
				cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				s.botCardDone(cctx, msg, ix, how)
			}
		}()
		return toast("info", i18n.T(l, "assistant-feishu_cards.working_on_it"))
	}
}

// botRestoreCard puts back the card of interaction id, closed optimistically for an answer that did not go through,
// unless the hub says it is no longer pending: it ended while the answer was out, and its interaction.ended found no
// card to close, so putting it back would leave a card waiting that nothing answers (#5012). When that cannot be
// asked either, the card is put back, as before.
func (s *Service) botRestoreCard(ctx context.Context, id string, card botCard) {
	var got struct {
		OK bool `json:"ok"`
	}
	if err := s.appCall(ctx, AppInteractions, InteractionsGet, map[string]any{"interaction_id": id}, &got); err == nil && !got.OK {
		return
	}
	b := &s.fbot
	b.mu.Lock()
	s.keepBotCardLocked(id, &card)
	b.mu.Unlock()
}

// botStartCardWindow is how long a repeat press of the same /start card is ignored: a double tap, Feishu's 3 s
// redelivery, and the hub's ~2 s resend all fall inside it. It is deliberately shorter than botStartAgent's
// 2-minute timeout: if the first start is still running when the window ends, the owner pressing again is a
// deliberate manual retry and may start a second agent. The transport retries above arrive at ~2-3 s, while the
// slot is held for the whole outstanding call, so they can never reach that second start.
var botStartCardWindow = 60 * time.Second

// startCardPressed reports whether the /start card message mid was already pressed within the window, and records
// this press either way. A true answer means the caller must not start a second agent. The returned time is the
// timestamp this press stored (or the existing one for a duplicate), for forgetStartCard's compare-and-swap.
func (s *Service) startCardPressed(mid string) (bool, time.Time) {
	b := &s.fbot
	now := b.clock()
	b.mu.Lock()
	defer b.mu.Unlock()
	key := mid + "start"
	if at, ok := b.startAt[key]; ok && now.Sub(at) < botStartCardWindow {
		return true, at
	}
	if b.startAt == nil {
		b.startAt = map[string]time.Time{}
	}
	for k, at := range b.startAt { // forget presses past the window so the map cannot grow without bound
		if now.Sub(at) >= botStartCardWindow {
			delete(b.startAt, k)
		}
	}
	b.startAt[key] = now
	return false, now
}

// forgetStartCard releases mid's dedup slot after its start failed, so pressing the same card again retries it
// instead of being answered with a bare "Starting". It only deletes the slot this press stamped (at): if the window
// passed and a later manual press re-stamped it while the first start was still hung, that newer slot must survive
// the first start's late failure.
func (s *Service) forgetStartCard(mid string, at time.Time) {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	key := mid + "start"
	if cur, ok := b.startAt[key]; ok && cur.Equal(at) {
		delete(b.startAt, key)
	}
}

// botStartAgent starts a coding agent from the /start card and points the chat at it; mode is the form's permission
// mode (the workbench's field), "" the launcher's own. A non-nil error was already told to the owner as a note; it
// means no agent was started.
func (s *Service) botStartAgent(machine, cwd, prompt, mode string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	l := s.botLang(ctx)
	d, ok := s.device(ctx, machine)
	if !ok {
		s.botSay(ctx, i18n.T(l, "assistant-feishu_cards.no_such_machine"))
		return ErrNoDevice
	}
	cwd = proto.ExpandHome(cwd, d.Home)
	params := map[string]any{"cwd": cwd, "prompt": prompt, "tab_name": s.tabName(ctx, prompt)} // agents_start.go
	if slices.Contains(proto.PermissionModes, mode) {
		params["permission_mode"] = mode
	}
	if err := s.applyModel(ctx, "agent.new", params); err != nil {
		s.botSay(ctx, i18n.T(l, "assistant-feishu_cards.cannot_start", i18n.Args{"error": err.Error()}))
		return err
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	if err := s.call(ctx, d.ID, DeviceSessionStart, params, &res); err != nil {
		s.botSay(ctx, i18n.T(l, "assistant-feishu_cards.cannot_start", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
		return err
	}
	t := botTarget{Kind: targetAgent, MachineID: d.ID, SessionID: res.Session.SessionID}
	label := agentLabel(res.Session)
	if label == "" || label == "." {
		label = cwd
	}
	label += " · " + d.Name
	s.setBotCurrent(t)
	c := feishubot.NewCard("", "", i18n.T(l, "assistant-feishu_cards.started")).Markdown(i18n.T(l, "assistant-feishu_cards.started_body", i18n.Args{"machine": feishubot.EscapeMarkdown(d.Name), "dir": feishubot.Code(cwd)}))
	if prompt != "" {
		c.Note(i18n.T(l, "assistant-feishu_cards.started_working"))
	}
	mid, err := s.botPost(ctx, "", c.Buttons(s.openButton(ctx, d.ID, res.Session.SessionID)), "")
	if err != nil {
		s.logf("bots: started note: %v", err)
	}
	s.rememberBotThread(mid, t, label)
	if prompt != "" && mid != "" {
		s.startChannelRun(&channel.Run{ID: "fb_" + mid, Channel: channel.Feishu, ReplyTo: mid, MachineID: t.MachineID, SessionID: t.SessionID,
			TargetKind: t.Kind, Label: label, Started: time.Now().UTC()})
	}
	return nil
}
