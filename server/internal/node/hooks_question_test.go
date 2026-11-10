package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func runQuestionHook(t *testing.T, n *Node, event string, questions []map[string]any) map[string]any {
	t.Helper()
	input, _ := json.Marshal(map[string]any{"questions": questions})
	raw, _ := json.Marshal(hookInput{HookEventName: event, ToolName: "AskUserQuestion",
		SessionID: "s1", Cwd: "/tmp/x", ToolInput: input})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := n.handleHook(ctx, sockRequest{Input: raw})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["hookSpecificOutput"].(map[string]any)
}

// The terminal must show Claude's own question while the same card waits for a phone answer (#2382).
func TestClaudeQuestionPreToolUseDoesNotWaitForPhone(t *testing.T) {
	fakeAgent(t)
	n := codexTestNode(t)
	withCancelHub(t, n)
	done := runHookAsync(t, n, map[string]any{
		"hook_event_name": "PreToolUse", "session_id": decidedSession, "tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"questions": []any{map[string]any{"question": "Which?", "header": "Pick",
			"options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}}}}},
	})
	select {
	case out := <-done:
		if out != nil {
			t.Fatalf("handing the question to the terminal must not answer it: %v", out)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PreToolUse still waits for the phone, so Claude cannot draw its terminal question")
	}
}

// A phone answer arrives through PermissionRequest while the terminal shows the same question.
func TestClaudeQuestionPermissionRequestAnswersFromPhone(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Which?": "A"}}
	}
	raw, _ := json.Marshal(map[string]any{"questions": []map[string]any{{"question": "Which?", "header": "Pick",
		"options": []map[string]any{{"label": "A"}, {"label": "B"}}}}})
	out, _ := runPermissionHook(t, n, hookInput{HookEventName: "PermissionRequest", ToolName: "AskUserQuestion",
		SessionID: decidedSession, ToolInput: raw})
	if out == nil {
		t.Fatal("PermissionRequest dropped the phone's answer")
	}
	decision := out.(map[string]any)["hookSpecificOutput"].(map[string]any)["decision"].(map[string]any)
	updated, _ := decision["updatedInput"].(map[string]any)
	answers, _ := updated["answers"].(map[string]string)
	if decision["behavior"] != "allow" || answers["Which?"] != "A" || len(fh.interactions()) != 1 {
		t.Fatalf("phone answer was not delivered: %v (cards=%d)", out, len(fh.interactions()))
	}
}

// A terminal answer wins over the waiting phone card, and the managed hooks report that answer (#2382).
func TestClaudeQuestionTerminalAnswerWithdrawsPhoneCard(t *testing.T) {
	fakeAgent(t)
	n := codexTestNode(t)
	h := withCancelHub(t, n)
	questions := []any{map[string]any{"question": "Which?", "header": "Pick",
		"options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	pre, _ := json.Marshal(hookInput{HookEventName: "PreToolUse", SessionID: decidedSession, ToolName: "AskUserQuestion",
		ToolUseID: "toolu_q1", ToolInput: input})
	if _, err := n.handleHook(context.Background(), sockRequest{Input: pre}); err != nil {
		t.Fatal(err)
	}
	done := runHookAsync(t, n, map[string]any{
		"hook_event_name": "PermissionRequest", "session_id": decidedSession, "tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"questions": questions},
	})
	waitFor(t, "the phone card", func() bool { asked, _ := h.counts(); return asked == 1 })
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	post, err := json.Marshal(hookInput{HookEventName: "PostToolUse", SessionID: decidedSession, ToolName: "AskUserQuestion",
		ToolUseID: "toolu_q1", ToolInput: postInput})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.handleHook(context.Background(), sockRequest{Input: post, Attempt: "a2"}); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		if out != nil {
			t.Fatalf("a question already answered in the terminal was answered again: %v", out)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("the terminal answer did not release PermissionRequest")
	}
	waitFor(t, "the phone card to be withdrawn", func() bool { _, cancelled := h.counts(); return cancelled == 1 })
	hooks := hookSettings("personad", "", false)["hooks"].(map[string]any)
	if _, ok := hooks["PostToolUse"]; !ok {
		t.Fatal("Persona-started Claude does not report AskUserQuestion PostToolUse")
	}
}

// A fast terminal answer may reach node before PermissionRequest; its id still prevents a phone card.
func TestClaudeQuestionTerminalCompletionBeforePermissionRequest(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	pre, _ := json.Marshal(hookInput{HookEventName: "PreToolUse", SessionID: "s1", ToolName: "AskUserQuestion",
		ToolUseID: "toolu_early", ToolInput: input})
	if _, err := n.handleHook(context.Background(), sockRequest{Input: pre}); err != nil {
		t.Fatal(err)
	}
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	post, _ := json.Marshal(hookInput{HookEventName: "PostToolUse", SessionID: "s1", ToolName: "AskUserQuestion",
		ToolUseID: "toolu_early", ToolInput: postInput})
	if _, err := n.handleHook(context.Background(), sockRequest{Input: post}); err != nil {
		t.Fatal(err)
	}
	out, took := runPermissionHook(t, n, hookInput{HookEventName: "PermissionRequest", SessionID: "s1",
		ToolName: "AskUserQuestion", ToolInput: input})
	if out != nil || len(fh.interactions()) != 0 || took > time.Second {
		t.Fatalf("an already-answered question asked the phone again: %v after %v (cards=%d)", out, took, len(fh.interactions()))
	}
}

// A resumed PermissionRequest keeps the id assigned on its first attempt, never the next question's id.
func TestClaudeQuestionResumeKeepsOriginalToolUseID(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Which?": "A"}}
	}
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	pre := func(id string) {
		raw, _ := json.Marshal(hookInput{HookEventName: "PreToolUse", SessionID: "s1", ToolName: "AskUserQuestion",
			ToolUseID: id, ToolInput: input})
		if _, err := n.handleHook(context.Background(), sockRequest{Input: raw}); err != nil {
			t.Fatal(err)
		}
	}
	in := hookInput{HookEventName: "PermissionRequest", SessionID: "s1", ToolName: "AskUserQuestion", ToolInput: input}
	pre("toolu_first")
	raw, _ := json.Marshal(in)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if out, err := n.handleHook(ctx, sockRequest{Input: raw, Attempt: "a1"}); err != nil || out == nil {
		t.Fatalf("the first answer was not delivered: %v, %v", out, err)
	}
	pre("toolu_second")
	out, err := n.handleHook(ctx, sockRequest{Input: raw, Attempt: "a1", Resume: true})
	if err != nil || out == nil {
		t.Fatalf("the resumed answer was not delivered: %v, %v", out, err)
	}
	cards := fh.interactions()
	if len(cards) != 2 || cards[1].ToolUseID != "toolu_first" {
		t.Fatalf("resume took the next question's id: %+v", cards)
	}
}

// An old connection and its resumed request may overlap briefly; one PostToolUse releases every waiter of that id.
func TestClaudeQuestionTerminalAnswerReleasesOverlappingResume(t *testing.T) {
	fakeAgent(t)
	n := codexTestNode(t)
	h := withCancelHub(t, n)
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	pre, _ := json.Marshal(hookInput{HookEventName: "PreToolUse", SessionID: "s1", ToolName: "AskUserQuestion",
		ToolUseID: "toolu_overlap", ToolInput: input})
	if _, err := n.handleHook(context.Background(), sockRequest{Input: pre}); err != nil {
		t.Fatal(err)
	}
	permission, _ := json.Marshal(hookInput{HookEventName: "PermissionRequest", SessionID: "s1", ToolName: "AskUserQuestion", ToolInput: input})
	call := func(resume bool) <-chan any {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan any, 1)
		go func() {
			out, _ := n.handleHook(ctx, sockRequest{Input: permission, Attempt: "a1", Resume: resume, AgentPID: 4242, AgentStart: "linux:7"})
			done <- out
		}()
		t.Cleanup(func() { cancel() })
		return done
	}
	first, second := call(false), call(true)
	waitFor(t, "both overlapping cards", func() bool { asked, _ := h.counts(); return asked == 2 })
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	post, _ := json.Marshal(hookInput{HookEventName: "PostToolUse", SessionID: "s1", ToolName: "AskUserQuestion",
		ToolUseID: "toolu_overlap", ToolInput: postInput})
	if _, err := n.handleHook(context.Background(), sockRequest{Input: post}); err != nil {
		t.Fatal(err)
	}
	for name, done := range map[string]<-chan any{"first": first, "resumed": second} {
		select {
		case out := <-done:
			if out != nil {
				t.Fatalf("%s waiter answered a terminal-completed question: %v", name, out)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("%s waiter stayed blocked after PostToolUse", name)
		}
	}
}

// An early terminal answer can make the next identical PermissionRequest arrive before the answered call's own one.
func TestClaudeQuestionOutOfOrderPermissionRequestsKeepTheirIDs(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Which?": "A"}}
	}
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	hook := func(event, id string, raw json.RawMessage) {
		t.Helper()
		in, _ := json.Marshal(hookInput{HookEventName: event, SessionID: "s1", ToolName: "AskUserQuestion", ToolUseID: id, ToolInput: raw})
		if _, err := n.handleHook(context.Background(), sockRequest{Input: in}); err != nil {
			t.Fatal(err)
		}
	}
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	hook("PreToolUse", "toolu_first", input)
	hook("PostToolUse", "toolu_first", postInput)
	hook("PreToolUse", "toolu_second", input)
	permission, _ := json.Marshal(hookInput{HookEventName: "PermissionRequest", SessionID: "s1", ToolName: "AskUserQuestion", ToolInput: input})
	out, err := n.handleHook(context.Background(), sockRequest{Input: permission, Attempt: "a2"})
	if err != nil || out == nil {
		t.Fatalf("the second question took the first question's completed id: %v, %v", out, err)
	}
	cards := fh.interactions()
	if len(cards) != 1 || cards[0].ToolUseID != "toolu_second" {
		t.Fatalf("the second question was paired incorrectly: %+v", cards)
	}
	out, err = n.handleHook(context.Background(), sockRequest{Input: permission, Attempt: "a1"})
	if err != nil || out != nil || len(fh.interactions()) != 1 {
		t.Fatalf("the late PermissionRequest of the answered call asked again: %v, %v", out, err)
	}
}

// Standby replays the question lifecycle, not just hook.event: a terminal answer while away stays answered.
func TestClaudeQuestionStandbyReplayKeepsTerminalAnswer(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Which?": "A"}}
	}
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	pre, _ := json.Marshal(hookInput{HookEventName: "PreToolUse", SessionID: "s1", ToolName: "AskUserQuestion",
		ToolUseID: "toolu_spooled", ToolInput: input})
	permission, _ := json.Marshal(hookInput{HookEventName: "PermissionRequest", SessionID: "s1", ToolName: "AskUserQuestion", ToolInput: input})
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	post, _ := json.Marshal(hookInput{HookEventName: "PostToolUse", SessionID: "s1", ToolName: "AskUserQuestion",
		ToolUseID: "toolu_spooled", ToolInput: postInput})
	at := time.Now().Add(-5 * time.Minute) // standby may hold a reconnecting hook for ten minutes
	for i, raw := range []json.RawMessage{pre, permission, post} {
		if err := n.replayHook(spoolEntry{ID: string(rune('a' + i)), At: at.Add(time.Duration(i) * time.Second), Kind: "hook",
			Request: sockRequest{Kind: "hook", Input: raw, Attempt: "a1"}}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := n.handleHook(ctx, sockRequest{Input: permission, Attempt: "a1", Resume: true})
	if err != nil || out != nil || len(fh.interactions()) != 0 {
		t.Fatalf("the spooled terminal answer was lost: %v, %v (cards=%d)", out, err, len(fh.interactions()))
	}
}

// A newer live event suppresses stale hook.event reports, but must not erase the replayed question lifecycle.
func TestClaudeQuestionReplayAheadStillKeepsTerminalAnswer(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Which?": "A"}}
	}
	n.logger = log.New(io.Discard, "", 0)
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	pre, _ := json.Marshal(hookInput{HookEventName: "PreToolUse", SessionID: decidedSession, ToolName: "AskUserQuestion",
		ToolUseID: "toolu_ahead", ToolInput: input})
	permission, _ := json.Marshal(hookInput{HookEventName: "PermissionRequest", SessionID: decidedSession, ToolName: "AskUserQuestion", ToolInput: input})
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	post, _ := json.Marshal(hookInput{HookEventName: "PostToolUse", SessionID: decidedSession, ToolName: "AskUserQuestion",
		ToolUseID: "toolu_ahead", ToolInput: postInput})
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	for i, raw := range []json.RawMessage{pre, permission, post} {
		if _, err := sp.add(spoolEntry{ID: string(rune('a' + i)), At: time.Now(), Kind: "hook",
			Request: sockRequest{Kind: "hook", Input: raw, Attempt: "a1"}}); err != nil {
			t.Fatal(err)
		}
	}
	stop, _ := json.Marshal(hookInput{HookEventName: "Stop", SessionID: decidedSession})
	n.ahead.note(sockRequest{Kind: "hook", Input: stop}) // a newer live Stop makes every kept event "behind"
	n.replayed, n.stateReady = make(chan struct{}), make(chan struct{})
	close(n.stateReady)
	n.replaySpool(context.Background(), sp)
	out, err := n.handleHook(context.Background(), sockRequest{Input: permission, Attempt: "a1", Resume: true})
	if err != nil || out != nil || len(fh.interactions()) != 0 {
		t.Fatalf("ahead filtering lost the terminal answer: %v, %v (cards=%d)", out, err, len(fh.interactions()))
	}
}

// A delayed replay is older than a live PreToolUse even though it arrives later; it must not take the live call's id.
func TestClaudeQuestionReplayDoesNotTakeLivePendingID(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Which?": "A"}}
	}
	n.logger = log.New(io.Discard, "", 0)
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	oldPre := questionHookInput("PreToolUse", input, "toolu_old", "")
	oldPermission := questionHookInput("PermissionRequest", input, "", "")
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	oldPost := questionHookInput("PostToolUse", postInput, "toolu_old", "")
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	for i, raw := range []json.RawMessage{oldPre, oldPermission, oldPost} {
		if _, err := sp.add(spoolEntry{ID: string(rune('a' + i)), At: time.Now().Add(-time.Minute), Kind: "hook",
			Request: sockRequest{Kind: "hook", Input: raw, Attempt: "a1"}}); err != nil {
			t.Fatal(err)
		}
	}
	livePre := questionHookInput("PreToolUse", input, "toolu_live", "")
	if _, err := n.handleHook(context.Background(), sockRequest{Input: livePre}); err != nil {
		t.Fatal(err)
	}
	n.ahead.note(sockRequest{Kind: "hook", Input: livePre})
	n.replayed, n.stateReady = make(chan struct{}), make(chan struct{})
	close(n.stateReady)
	n.replaySpool(context.Background(), sp)
	livePermission := questionHookInput("PermissionRequest", input, "", "")
	out, err := n.handleHook(context.Background(), sockRequest{Input: livePermission, Attempt: "a2"})
	if err != nil || out == nil {
		t.Fatalf("the old replay consumed the live question's id: %v, %v", out, err)
	}
	cards := fh.interactions()
	if len(cards) != 1 || cards[0].ToolUseID != "toolu_live" {
		t.Fatalf("the live question was paired to a replayed call: %+v", cards)
	}
}

// Without a PreToolUse pairing, an id-less PostToolUse is not evidence about any waiting question.
func TestClaudeQuestionEmptyPostIDDoesNotCancelUnknownWaiter(t *testing.T) {
	n := codexTestNode(t)
	h := withCancelHub(t, n)
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	permission := questionHookInput("PermissionRequest", input, "", "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan any, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		out, _ := n.handleHook(ctx, sockRequest{Input: permission, Attempt: "a1"})
		done <- out
	}()
	defer func() { cancel(); <-finished }()
	waitFor(t, "the unpaired card", func() bool { asked, _ := h.counts(); return asked == 1 })
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	post := questionHookInput("PostToolUse", postInput, "", "")
	if _, err := n.handleHook(context.Background(), sockRequest{Input: post}); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		t.Fatalf("an empty PostToolUse id cancelled an unknown waiter: %v", out)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestClaudeQuestionLateReceiptDoesNotCancelNextIdenticalQuestion(t *testing.T) {
	fakeAgent(t)
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	release := make(chan struct{})
	defer close(release)
	calls := 0
	fh.answer = func(proto.Interaction) proto.Resolution {
		calls++
		if calls == 1 {
			return proto.Resolution{Answers: map[string]string{"Which?": "A"}}
		}
		<-release
		return proto.Resolution{}
	}
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	for _, id := range []string{"toolu_first", "toolu_second"} {
		raw, _ := json.Marshal(hookInput{HookEventName: "PreToolUse", SessionID: "s1", ToolName: "AskUserQuestion",
			ToolUseID: id, ToolInput: input})
		if _, err := n.handleHook(context.Background(), sockRequest{Input: raw}); err != nil {
			t.Fatal(err)
		}
		if id == "toolu_first" {
			if out := runQuestionHook(t, n, "PermissionRequest", questions); out["decision"] == nil {
				t.Fatal("the first phone answer was not delivered")
			}
		}
	}
	done := runHookAsync(t, n, map[string]any{
		"hook_event_name": "PermissionRequest", "session_id": "s1", "tool_name": "AskUserQuestion",
		"tool_input": map[string]any{"questions": questions},
	})
	waitFor(t, "the second card", func() bool { return len(fh.interactions()) == 2 })
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	post, _ := json.Marshal(hookInput{HookEventName: "PostToolUse", SessionID: "s1", ToolName: "AskUserQuestion",
		ToolUseID: "toolu_first", ToolInput: postInput})
	if _, err := n.handleHook(context.Background(), sockRequest{Input: post}); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		t.Fatalf("the first question's late receipt cancelled the second: %v", out)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestClaudeQuestionPhoneCompletionDoesNotSuppressNextQuestion(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Which?": "A"}}
	}
	first := runQuestionHook(t, n, "PermissionRequest", questions)
	if first["decision"] == nil {
		t.Fatal("the phone's first answer was not delivered")
	}
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	post, _ := json.Marshal(hookInput{HookEventName: "PostToolUse", SessionID: "s1", ToolName: "AskUserQuestion",
		ToolUseID: "toolu_q1", ToolInput: postInput})
	if _, err := n.handleHook(context.Background(), sockRequest{Input: post}); err != nil {
		t.Fatal(err)
	}
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Which?": "B"}}
	}
	second := runQuestionHook(t, n, "PermissionRequest", questions)
	decision, _ := second["decision"].(map[string]any)
	updated, _ := decision["updatedInput"].(map[string]any)
	answers, _ := updated["answers"].(map[string]string)
	if decision["behavior"] != "allow" || answers["Which?"] != "B" || len(fh.interactions()) != 2 {
		t.Fatalf("the previous completion suppressed a new question: %v (cards=%d)", second, len(fh.interactions()))
	}
}

// Multi-select labels and Other text use the same phone-answer channel without reinterpretation.
func TestClaudeQuestionPermissionRequestKeepsMultiAndOtherAnswers(t *testing.T) {
	for _, tc := range []struct{ name, answer string }{{"multi", "A, B"}, {"other", "Something else"}} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			fh := withFakeHub(t, n)
			fh.answer = func(proto.Interaction) proto.Resolution {
				return proto.Resolution{Answers: map[string]string{"Which?": tc.answer}}
			}
			questions := []map[string]any{{"question": "Which?", "multiSelect": tc.name == "multi",
				"options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
			out := runQuestionHook(t, n, "PermissionRequest", questions)
			decision, _ := out["decision"].(map[string]any)
			updated, _ := decision["updatedInput"].(map[string]any)
			answers, _ := updated["answers"].(map[string]string)
			if decision["behavior"] != "allow" || answers["Which?"] != tc.answer {
				t.Fatalf("answer changed in transit: %v", out)
			}
		})
	}
}

// Alike-worded questions are denied (answers are keyed by question text) with a reason for the model and no card;
// distinct questions behave as before (#4429). Trim/MultiSelect normalization is covered in proto's table test.
func TestHookAskUserQuestionDuplicateGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	t.Cleanup(func() { cancel(); n2hW.Close(); h2nW.Close(); n2hR.Close(); h2nR.Close() })
	n, _ := testNode(t)
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	created := 0
	reply := &proto.Resolution{}
	hub.OnRequest = func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
		created++
		return *reply, nil
	}
	go hub.Serve(ctx, n2hR)
	go n.peer.Serve(ctx, h2nR)

	dup := runQuestionHook(t, n, "PreToolUse", []map[string]any{
		{"question": "确认部署？"}, {"question": " 　确认部署？　 ", "multiSelect": true},
	})
	reason, _ := dup["permissionDecisionReason"].(string)
	if dup["permissionDecision"] != "deny" || created != 0 || !strings.Contains(reason, "AskUserQuestion") {
		t.Fatalf("duplicate questions must be denied without a card, telling the model how to ask again: %v (%d)", dup, created)
	}
	*reply = proto.Resolution{Answers: map[string]string{"发给谁？": "张三", "哪些部分？": "进展"}}
	ok := runQuestionHook(t, n, "PermissionRequest", []map[string]any{
		{"question": "发给谁？"}, {"question": "哪些部分？", "multiSelect": true},
	})
	decision, _ := ok["decision"].(map[string]any)
	if decision["behavior"] != "allow" || created != 1 {
		t.Fatalf("distinct questions must be allowed, one card created: %v (created=%d)", ok, created)
	}
	updated, _ := decision["updatedInput"].(map[string]any)
	answers, _ := updated["answers"].(map[string]string)
	if len(answers) != 2 || answers["发给谁？"] != "张三" {
		t.Fatalf("answers lost: %v", updated)
	}
}
