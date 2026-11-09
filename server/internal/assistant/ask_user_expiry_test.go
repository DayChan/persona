package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/mcpserver"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The main conversation's ask_user card against the Lark nudge (#6084, #9): the nudge waits nudge_after, 60 minutes
// unless set (assistantwire.NudgeAfterDefault), and a card that is gone is never nudged (feishu_nudge.go), so the card
// has to outlive the default wait. The user writing in the conversation takes a waiting question back.
// All ids are placeholders.

const askUserArgs = `{"questions":[{"question":"Push it to beta?","options":[{"label":"Yes"},{"label":"No"}]}]}`

// askUserCard has the main conversation ask with ask_user on s and returns the card it put up, as the real tool made it.
// The ask is withdrawn when the test ends.
func askUserCard(t *testing.T, s *Service) proto.Interaction {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		_, _ = s.toolAskUser(ctx, "mac", "s1", json.RawMessage(askUserArgs))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if qs := s.pendingQuestions(); len(qs) == 1 {
			return qs[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("ask_user put up no card")
	return proto.Interaction{}
}

// A question the main conversation asks must still be waiting when the default nudge falls due; else the nudge, which
// is there for the owner who is away from the phone, can never reach the question that most needs them.
func TestAskUserCardOutlivesTheDefaultNudge(t *testing.T) {
	r := newNudgeRig(t)
	ix := askUserCard(t, r.s)
	nudgeDue := ix.CreatedAt.Add(nudgeAfterOf(FeishuStatus{})) // the status says nothing: the default
	if !ix.ExpiresAt.After(nudgeDue) {
		t.Errorf("the ask_user card lives %v (expires %s) but the default nudge is due after %v (%s): the card is gone before the nudge",
			ix.ExpiresAt.Sub(ix.CreatedAt), ix.ExpiresAt.Format(time.TimeOnly), nudgeDue.Sub(ix.CreatedAt), nudgeDue.Format(time.TimeOnly))
	}
}

// The tool call that asked must live as long as the card: `personad mcp` ends a call at mcpserver.CallTimeout, and a
// shorter cap ended ask_user before its own deadline, before the nudge (#1498; it was 40 minutes). The margin is for
// the answer's way back from the phone.
func TestAskUserWaitFitsTheMCPServersCap(t *testing.T) {
	if margin := mcpserver.CallTimeout - askUserWait; margin < 5*time.Minute {
		t.Errorf("ask_user waits %v but personad mcp ends a tool call after %v: raise mcpserver.CallTimeout with it", askUserWait, mcpserver.CallTimeout)
	}
}

// askRig is a manager with the Lark bot paired and its interaction history kept (fleetMem), as nudgeRig sets up
// the rest. closed() is what the bot's card updates said so far.
type askRig struct {
	*nudgeRig
	s      *Service
	mem    *fleetMem
	closed func() string
}

func newAskRig(t *testing.T) *askRig {
	t.Helper()
	r := newNudgeRig(t)
	mem := newFleetMem()
	s := withSettings(t, r.f, New(Options{Hub: r.f, Store: func() Store { return mem }, Logf: testLogf(t), Devices: hookDevs{r.d, r}}))
	s.fleet.interactions = map[string]*pending{}
	s.fbot.now = func() time.Time { return r.now }
	var mu sync.Mutex
	updates := []string{}
	r.f.answerApp(AppFeishu, FeishuUpdateCard, func(p json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		updates = append(updates, string(p))
		return map[string]any{}, nil
	})
	return &askRig{nudgeRig: r, s: s, mem: mem, closed: func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(updates, "\n")
	}}
}

// ask has the conversation ask with ask_user and waits until its card is on the queue, with its Lark card posted in
// the owner's DM (the bot's record is kept as postInteractionCard would). The result of the tool call comes on done.
func (a *askRig) ask(t *testing.T) (ix proto.Interaction, done chan string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done = make(chan string, 1)
	t.Cleanup(cancel)
	go func() {
		text, err := a.s.toolAskUser(ctx, "mac", "s1", json.RawMessage(askUserArgs))
		if err != nil {
			text = "error: " + err.Error()
		}
		done <- text
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if qs := a.s.pendingQuestions(); len(qs) == 1 {
			ix = qs[0]
			if _, up := a.s.pendingIx(ix.ID); up {
				b := &a.s.fbot
				b.mu.Lock()
				a.s.keepBotCardLocked(ix.ID, &botCard{MessageID: "om_" + ix.ID, Thread: "om_run", Ix: ix, At: a.now})
				b.mu.Unlock()
				return ix, done
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("ask_user put up no card on the queue")
	return ix, done
}

func (a *askRig) result(t *testing.T, done chan string) string {
	t.Helper()
	select {
	case text := <-done:
		return text
	case <-time.After(5 * time.Second):
		t.Fatal("the ask_user call did not return")
		return ""
	}
}

// With the defaults, the Lark bot nudges once under a waiting ask_user card, a minute after nudge_after.
func TestAskUserCardIsNudgedAfterTheDefaultWait(t *testing.T) {
	a := newAskRig(t)
	ix, _ := a.ask(t)
	a.tick(a.s, time.Duration(assistantwire.NudgeAfterDefault-1)*time.Minute)
	wantNudges(t, a.nudgeRig, 0, "a minute before nudge_after")
	a.tick(a.s, 2*time.Minute)
	got := wantNudges(t, a.nudgeRig, 1, "a minute after nudge_after")
	if got[0]["reply_to"] != "om_"+ix.ID {
		t.Errorf("the nudge went under %v, want the ask_user card (om_%s)", got[0]["reply_to"], ix.ID)
	}
	if _, pending := a.s.pendingIx(ix.ID); !pending {
		t.Error("the ask_user card was no longer waiting when the nudge fell due")
	}
}

// The user writing in the conversation takes back the question it waits on: the tool returns at once with the reason,
// the card is closed as dismissed with "replied in the chat" (not "expired"), and a later answer finds nothing.
func TestUserWritingWithdrawsTheWaitingAskUser(t *testing.T) {
	a := newAskRig(t)
	ix, done := a.ask(t)
	a.s.UserSpoke(context.Background(), SessionRef{MachineID: "mac", SessionID: "s1"})
	if text := a.result(t, done); text != prompts.RenderQuestionUserSpoke() {
		t.Fatalf("tool result = %q, want the user-spoke text", text)
	}
	if len(a.s.pendingQuestions()) != 0 {
		t.Error("the question is still out after the user wrote")
	}
	if _, pending := a.s.pendingIx(ix.ID); pending {
		t.Error("the card is still pending")
	}
	status, res, _ := a.mem.InteractionOutcome(context.Background(), ix.ID)
	if status != "dismissed" || res == nil || res.Reason != proto.DismissedInChat {
		t.Errorf("history: %q %+v, want dismissed with reason %q", status, res, proto.DismissedInChat)
	}
	want := i18n.T(a.s.botLang(context.Background()), "assistant-feishu_cards.replied_in_chat")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(a.closed(), want) {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(a.closed(), want) {
		t.Errorf("the Lark card was not closed with %q: %q", want, a.closed())
	}
	if strings.Contains(a.closed(), i18n.T(a.s.botLang(context.Background()), "assistant-feishu_cards.card_expired")) {
		t.Error("the Lark card says it expired")
	}
	a.tick(a.s, 2*time.Hour)
	wantNudges(t, a.nudgeRig, 0, "a card that was taken back")
	_, err := a.s.Action(context.Background(), Action{Op: "answer", InteractionID: ix.ID, Answer: json.RawMessage(`{"answers":{"Push it to beta?":"Yes"}}`)})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != CodeGone {
		t.Fatalf("answer after the withdrawal: err = %v, want %s", err, CodeGone)
	}
}

// Only the conversation's own ask_user is taken back: another tool's question of the same conversation (persona_settings
// asks through the same queue), an ask_user of another conversation, and an Agent's question, stay.
func TestUserWritingLeavesOtherCardsAlone(t *testing.T) {
	a := newAskRig(t)
	ix, done := a.ask(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	settings := proto.Interaction{ID: "ix_settings", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, ToolName: "persona_settings",
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	go func() { _, _ = a.s.ask(ctx, settings) }() // as settings_tool.go asks
	go func() { _, _ = a.s.toolAskUser(ctx, "mac", "s2", json.RawMessage(askUserArgs)) }()
	agentQ := proto.Interaction{ID: "ix_agent_q", MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion, ToolName: "AskUserQuestion",
		Questions: []proto.Question{{Question: "Which branch?"}}}
	a.s.fleet.mu.Lock()
	a.s.fleet.interactions[agentQ.ID] = &pending{ix: agentQ, done: make(chan proto.Resolution, 1)}
	a.s.fleet.mu.Unlock()
	var otherChat string
	waitFor(t, "the other cards on the queue", func() bool {
		for _, q := range a.s.pendingQuestions() {
			if q.SessionID == "s2" {
				otherChat = q.ID
			}
		}
		_, s1 := a.s.pendingIx(settings.ID)
		_, s2 := a.s.pendingIx(otherChat)
		return s1 && s2 && otherChat != ""
	})

	a.s.UserSpoke(context.Background(), SessionRef{MachineID: "mac", SessionID: "s1"})
	a.result(t, done)
	time.Sleep(50 * time.Millisecond) // anything wrongly taken back with it would have gone by now
	for _, id := range []string{settings.ID, otherChat, agentQ.ID} {
		if _, pending := a.s.pendingIx(id); !pending {
			t.Errorf("%s was taken back", id)
		}
	}
	if _, pending := a.s.pendingIx(ix.ID); pending {
		t.Error("the conversation's own ask_user is still pending")
	}
}

// What the user writes after a question was taken back is a message, not a late answer to it: an option word typed
// next in the Lark chat reaches the conversation. (Within 10 minutes after a card that ended elsewhere it would be
// swallowed with "handled elsewhere".)
func TestMessageAfterAWithdrawnQuestionIsNotALateAnswer(t *testing.T) {
	a := newAskRig(t)
	_, done := a.ask(t)
	a.s.UserSpoke(context.Background(), SessionRef{MachineID: "mac", SessionID: "s1"})
	a.result(t, done)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && a.closed() == "" {
		time.Sleep(5 * time.Millisecond)
	}
	if a.s.botAnswerQuestion(context.Background(), "om_next", "Yes", "oa_1", "", "") {
		t.Fatal("a message after the withdrawal was taken as a late answer and swallowed")
	}
}

// Without the interaction queue (the hub keeps the cards), the question is handed the dismissal directly.
func TestUserWritingWithdrawsAskUserWithoutTheQueue(t *testing.T) {
	s := newTestService(t, newFakeHub())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan string, 1)
	go func() {
		text, _ := s.toolAskUser(ctx, "mac", "conversation", json.RawMessage(askUserArgs))
		done <- text
	}()
	for ctx.Err() == nil && len(s.pendingQuestions()) == 0 {
		time.Sleep(time.Millisecond)
	}
	s.UserSpoke(ctx, SessionRef{MachineID: "mac", SessionID: "conversation"})
	select {
	case text := <-done:
		if text != prompts.RenderQuestionUserSpoke() {
			t.Fatalf("tool result = %q", text)
		}
	case <-ctx.Done():
		t.Fatal("the question was not taken back")
	}
	if len(s.pendingQuestions()) != 0 {
		t.Error("the question is still out")
	}
}

// A question that expires after its wait ends for good: the history says expired, the Lark card closes saying it moved
// to the to-dos (the question becomes one, ask_todo.go), and it is never nudged. The wait is shortened to 300 ms here; the real one
// is askUserWait.
func TestExpiredAskUserCardIsEndedForGood(t *testing.T) {
	a := newAskRig(t)
	card, _ := a.ask(t) // the real tool's card, only for its shape
	short := card
	short.ID = "ix_expiring"
	short.CreatedAt = time.Now().UTC()
	short.ExpiresAt = short.CreatedAt.Add(300 * time.Millisecond)
	b := &a.s.fbot
	b.mu.Lock()
	a.s.keepBotCardLocked(short.ID, &botCard{MessageID: "om_" + short.ID, Thread: "om_run", Ix: short, At: a.now})
	b.mu.Unlock()
	res, err := a.s.waitInteraction(context.Background(), short)
	if err != nil || !res.Dismiss {
		t.Fatalf("expired card resolved with %+v, %v; want a dismiss", res, err)
	}
	ctx := context.Background()
	if status, _, _ := a.mem.InteractionOutcome(ctx, short.ID); status != "expired" {
		t.Errorf("history status = %q, want expired", status)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && a.closed() == "" {
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(a.closed(), i18n.T(a.s.botLang(ctx), "assistant-feishu_cards.moved_to_todos")) {
		t.Errorf("the Lark card was not closed as moved to the to-dos: %q", a.closed())
	}
	a.tick(a.s, 2*time.Hour)
	for _, n := range a.sent() { // the card the shape was taken from still waits, and is nudged
		if n["reply_to"] == "om_"+short.ID {
			t.Errorf("an expired card was nudged: %v", n)
		}
	}
}

// The tool's side of an expiry: the user did not answer, and an answer that comes after is refused as gone.
func TestAskUserLateAnswerFindsNoQuestion(t *testing.T) {
	r := newNudgeRig(t)
	s := r.s
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan string, 1)
	go func() {
		text, _ := s.toolAskUser(ctx, "mac", "s1", json.RawMessage(askUserArgs))
		done <- text
	}()
	var ix proto.Interaction
	for ctx.Err() == nil {
		if qs := s.pendingQuestions(); len(qs) == 1 {
			ix = qs[0]
			break
		}
		time.Sleep(time.Millisecond)
	}
	// What askOnPhone sends to the question when its card expires (fleet_ix_own.go): a dismiss.
	if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: ix.ID, Answer: json.RawMessage(`{"dismiss":true}`)}); err != nil {
		t.Fatal(err)
	}
	if text := <-done; !strings.Contains(text, "did not answer") {
		t.Fatalf("tool result after expiry: %q", text)
	}
	_, err := s.Action(ctx, Action{Op: "answer", InteractionID: ix.ID, Answer: json.RawMessage(`{"answers":{"Push it to beta?":"Yes"}}`)})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != CodeGone {
		t.Fatalf("late answer: err = %v, want %s", err, CodeGone)
	}
}
