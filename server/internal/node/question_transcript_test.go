package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func writeQuestionTranscript(t *testing.T, input json.RawMessage, id string, answered bool) string {
	t.Helper()
	at := time.Now().UTC()
	records := []any{map[string]any{
		"type": "assistant", "sessionId": decidedSession, "timestamp": at.Format(time.RFC3339Nano),
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": id, "name": "AskUserQuestion", "input": input,
		}}},
	}}
	if answered {
		records = append(records, map[string]any{
			"type": "user", "sessionId": decidedSession, "timestamp": at.Add(time.Millisecond).Format(time.RFC3339Nano),
			"message": map[string]any{"role": "user", "content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": id, "content": "Your questions have been answered",
			}}},
		})
	}
	var raw []byte
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, line...)
		raw = append(raw, '\n')
	}
	path := filepath.Join(t.TempDir(), decidedSession+".jsonl")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func questionHookInput(event string, input json.RawMessage, id, transcriptPath string) json.RawMessage {
	raw, _ := json.Marshal(hookInput{HookEventName: event, SessionID: decidedSession, ToolName: "AskUserQuestion",
		ToolUseID: id, ToolInput: input, TranscriptPath: transcriptPath})
	return raw
}

// A replacement Node has no in-memory PreToolUse pairing. The transcript restores the exact id without guessing.
func TestClaudeQuestionResumeAfterNodeReplacementUsesTranscript(t *testing.T) {
	old := codexTestNode(t)
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	path := writeQuestionTranscript(t, input, "toolu_restart", false)
	pre := questionHookInput("PreToolUse", input, "toolu_restart", path)
	if _, err := old.handleHook(context.Background(), sockRequest{Input: pre}); err != nil {
		t.Fatal(err)
	}

	fakeAgent(t)
	n := codexTestNode(t) // a new process: none of old's questionCalls state
	h := withCancelHub(t, n)
	permission := questionHookInput("PermissionRequest", input, "", path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan any, 1)
	go func() {
		out, _ := n.handleHook(ctx, sockRequest{Input: permission, Attempt: "a1", Resume: true, AgentPID: 4242, AgentStart: "linux:7"})
		done <- out
	}()
	waitFor(t, "the resumed card", func() bool { asked, _ := h.counts(); return asked == 1 })
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	post := questionHookInput("PostToolUse", postInput, "toolu_restart", path)
	if _, err := n.handleHook(context.Background(), sockRequest{Input: post}); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		if out != nil {
			t.Fatalf("the terminal answer was answered again: %v", out)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("the replacement Node's empty pairing left the resumed hook waiting")
	}
}

// If the terminal answered while no manager was available, Resume sees the result and creates no stale card.
func TestClaudeQuestionResumeAfterNodeReplacementSeesTerminalResult(t *testing.T) {
	n := codexTestNode(t)
	h := withCancelHub(t, n)
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	path := writeQuestionTranscript(t, input, "toolu_finished", true)
	permission := questionHookInput("PermissionRequest", input, "", path)
	out, err := n.handleHook(context.Background(), sockRequest{Input: permission, Attempt: "a1", Resume: true})
	waitFor(t, "the restored card to be withdrawn", func() bool { _, cancelled := h.counts(); return cancelled == 1 })
	asked, cancelled := h.counts()
	if err != nil || out != nil || asked != 0 || cancelled != 1 {
		t.Fatalf("the restored card was not withdrawn without a new card: %v, %v (asked=%d cancelled=%d)", out, err, asked, cancelled)
	}
}

// A replay can restore the attempt but lose PostToolUse; the unique transcript result still completes it.
func TestClaudeQuestionResumeWithPairStillReadsTerminalResult(t *testing.T) {
	n := codexTestNode(t)
	h := withCancelHub(t, n)
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	path := writeQuestionTranscript(t, input, "toolu_paired", true)
	pre := hookInput{HookEventName: "PreToolUse", SessionID: decidedSession, ToolName: "AskUserQuestion",
		ToolUseID: "toolu_paired", ToolInput: input, TranscriptPath: path}
	n.questions.before(pre, time.Now())
	permission := questionHookInput("PermissionRequest", input, "", path)
	var in hookInput
	_ = json.Unmarshal(permission, &in)
	n.questions.pair(in, "a1", false, time.Now()) // replay restored Pre + Permission, but Post was lost
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	out, err := n.handleHook(ctx, sockRequest{Input: permission, Attempt: "a1", Resume: true})
	waitFor(t, "the paired restored card to be withdrawn", func() bool { _, cancelled := h.counts(); return cancelled == 1 })
	asked, cancelled := h.counts()
	if err != nil || out != nil || asked != 0 || cancelled != 1 {
		t.Fatalf("the existing pair hid the transcript completion: %v, %v (asked=%d cancelled=%d)", out, err, asked, cancelled)
	}
}

// A completed older question and an outstanding identical one do not say which call a resumed attempt belongs to.
func TestClaudeQuestionResumeWithIdenticalTranscriptCallsDoesNotGuess(t *testing.T) {
	n := codexTestNode(t)
	h := withCancelHub(t, n)
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	path := writeQuestionTranscript(t, input, "toolu_old", true)
	record := map[string]any{
		"type": "assistant", "sessionId": decidedSession, "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"message": map[string]any{"role": "assistant", "content": []any{map[string]any{
			"type": "tool_use", "id": "toolu_new", "name": "AskUserQuestion", "input": json.RawMessage(input),
		}}},
	}
	line, _ := json.Marshal(record)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write(append(line, '\n'))
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	permission := questionHookInput("PermissionRequest", input, "", path)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	out, err := n.handleHook(ctx, sockRequest{Input: permission, Attempt: "a1", Resume: true})
	waitFor(t, "the ambiguous restored card to be withdrawn", func() bool { _, cancelled := h.counts(); return cancelled == 1 })
	asked, cancelled := h.counts()
	if err != nil || out != nil || asked != 0 || cancelled != 1 {
		t.Fatalf("ambiguous Resume waited instead of handing back to the terminal: %v, %v (asked=%d cancelled=%d)", out, err, asked, cancelled)
	}

	// No PreToolUse is pending in this replacement Node. The outstanding transcript id still isolates Q2.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	out, err = n.handleHook(ctx2, sockRequest{Input: permission, Attempt: "a2"})
	waitFor(t, "the outstanding ambiguous card to be withdrawn", func() bool { _, cancelled := h.counts(); return cancelled == 2 })
	asked, cancelled = h.counts()
	if err != nil || out != nil || asked != 0 || cancelled != 2 {
		t.Fatalf("the outstanding ambiguous question created an unpaired wait: %v, %v (asked=%d cancelled=%d)", out, err, asked, cancelled)
	}
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	post := questionHookInput("PostToolUse", postInput, "toolu_new", path)
	if _, err := n.handleHook(context.Background(), sockRequest{Input: post}); err != nil {
		t.Fatal(err)
	}

	// Q1's Post is deliberately absent: its transcript result already completed it. Q2's Post frees Q3.
	pre := questionHookInput("PreToolUse", input, "toolu_third", path)
	if _, err := n.handleHook(context.Background(), sockRequest{Input: pre}); err != nil {
		t.Fatal(err)
	}
	ctx3, cancel3 := context.WithCancel(context.Background())
	done := make(chan any, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		out, _ := n.handleHook(ctx3, sockRequest{Input: permission, Attempt: "a3"})
		done <- out
	}()
	waitFor(t, "the next new question's card", func() bool { asked, _ := h.counts(); return asked == 1 })
	if ids := h.toolUseIDs(); len(ids) != 1 || ids[0] != "toolu_third" {
		t.Fatalf("the next question did not recover its own id: %v", ids)
	}
	cancel3()
	<-finished
}

// The replay gate opens when session state is delayed; a resumed question must still not race ahead of its PostToolUse.
func TestClaudeQuestionResumedBeforeDelayedReplayUsesTranscript(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Which?": "A"}}
	}
	n.logger = log.New(io.Discard, "", 0)
	questions := []map[string]any{{"question": "Which?", "options": []map[string]any{{"label": "A"}, {"label": "B"}}}}
	input, _ := json.Marshal(map[string]any{"questions": questions})
	path := writeQuestionTranscript(t, input, "toolu_delayed", true)
	pre := questionHookInput("PreToolUse", input, "toolu_delayed", path)
	permission := questionHookInput("PermissionRequest", input, "", path)
	postInput, _ := json.Marshal(map[string]any{"questions": questions, "answers": map[string]string{"Which?": "A"}})
	post := questionHookInput("PostToolUse", postInput, "toolu_delayed", path)
	sp := openSpool(filepath.Join(t.TempDir(), spoolFile))
	for i, raw := range []json.RawMessage{pre, permission, post} {
		if _, err := sp.add(spoolEntry{ID: string(rune('a' + i)), At: time.Now(), Kind: "hook",
			Request: sockRequest{Kind: "hook", Input: raw, Attempt: "a1"}}); err != nil {
			t.Fatal(err)
		}
	}
	n.replayed, n.stateReady, n.stateMissed = make(chan struct{}), make(chan struct{}), make(chan struct{})
	close(n.stateMissed) // the first state load failed: the gate opens, the replay waits for a later state
	replayed := make(chan struct{})
	go func() {
		n.replaySpool(context.Background(), sp)
		close(replayed)
	}()
	select {
	case <-n.replayed:
	case <-time.After(time.Second):
		t.Fatal("the delayed replay did not open the hook gate")
	}
	out, err := n.handleHook(context.Background(), sockRequest{Input: permission, Attempt: "a1", Resume: true})
	if err != nil || out != nil || len(fh.interactions()) != 0 {
		t.Fatalf("Resume raced the delayed replay and created a stale card: %v, %v", out, err)
	}
	close(n.stateReady)
	select {
	case <-replayed:
	case <-time.After(time.Second):
		t.Fatal("the delayed replay did not finish")
	}
}
