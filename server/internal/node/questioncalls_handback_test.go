package node

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func handBackInput() hookInput {
	input, _ := json.Marshal(map[string]any{"questions": []map[string]any{{"question": "Which?"}}})
	return hookInput{HookEventName: "PermissionRequest", SessionID: decidedSession, ToolName: "AskUserQuestion", ToolInput: input}
}

// Once an ambiguous attempt is handed to the terminal, its original request cannot later recreate a phone wait.
func TestQuestionHandBackPreventsLaterOriginalPair(t *testing.T) {
	q := &questionCalls{}
	in := handBackInput()
	pre := in
	pre.HookEventName, pre.ToolUseID = "PreToolUse", "toolu_handback"
	now := time.Now()
	q.before(pre, now)
	if !q.handBack(in, "a1", nil, now) {
		t.Fatal("an unpaired ambiguous attempt was not handed back")
	}
	if id := q.pair(in, "a1", false, now); id != "" {
		t.Fatalf("the original request paired %q after the attempt was handed back", id)
	}
}

// Handing back an ambiguous generation must not leak its first pending id into the next identical question.
func TestQuestionHandBackDoesNotLeakPendingIntoNextQuestion(t *testing.T) {
	q := &questionCalls{}
	in := handBackInput()
	now := time.Now()
	for _, id := range []string{"toolu_first", "toolu_second"} {
		pre := in
		pre.HookEventName, pre.ToolUseID = "PreToolUse", id
		q.before(pre, now)
	}
	if !q.handBack(in, "a1", nil, now) {
		t.Fatal("the ambiguous attempt was not handed back")
	}
	if id := q.pair(in, "a2", false, now); id != "" {
		t.Fatalf("the next question inherited the handed-back pending id %q", id)
	}
	for _, id := range []string{"toolu_first", "toolu_second"} {
		post := in
		post.HookEventName, post.ToolUseID = "PostToolUse", id
		q.complete(post, now)
	}
	pre := in
	pre.HookEventName, pre.ToolUseID = "PreToolUse", "toolu_third"
	q.before(pre, now)
	if id := q.pair(in, "a3", false, now); id != "toolu_third" {
		t.Fatalf("a completed handed-back generation blocked the next new question: %q", id)
	}
}

// A completion that preceded handBack must not keep the whole generation closed after the other call ends.
func TestQuestionHandBackSkipsAlreadyCompletedPending(t *testing.T) {
	q := &questionCalls{}
	in := handBackInput()
	now := time.Now()
	for _, id := range []string{"toolu_first", "toolu_second"} {
		pre := in
		pre.HookEventName, pre.ToolUseID = "PreToolUse", id
		q.before(pre, now)
	}
	firstPost := in
	firstPost.HookEventName, firstPost.ToolUseID = "PostToolUse", "toolu_first"
	q.complete(firstPost, now)
	if !q.handBack(in, "a1", nil, now) {
		t.Fatal("the ambiguous attempt was not handed back")
	}
	if id := q.pair(in, "a2", false, now); id != "" {
		t.Fatalf("the outstanding question paired the completed id %q", id)
	}
	secondPost := in
	secondPost.HookEventName, secondPost.ToolUseID = "PostToolUse", "toolu_second"
	q.complete(secondPost, now)
	pre := in
	pre.HookEventName, pre.ToolUseID = "PreToolUse", "toolu_third"
	q.before(pre, now)
	if id := q.pair(in, "a3", false, now); id != "toolu_third" {
		t.Fatalf("an already-completed pending id blocked the next question: %q", id)
	}
	_, answered, stop, id := q.watch(context.Background(), in, "a0", false)
	defer stop()
	if id != "toolu_first" || !answered() {
		t.Fatalf("the late first PermissionRequest lost its completed pairing: id=%q answered=%v", id, answered())
	}
}

// If the original request paired while the transcript was being read, the stale ambiguity cannot cancel its card.
func TestQuestionOriginalPairBeforeHandBackWins(t *testing.T) {
	q := &questionCalls{}
	in := handBackInput()
	pre := in
	pre.HookEventName, pre.ToolUseID = "PreToolUse", "toolu_original"
	now := time.Now()
	q.before(pre, now)
	if id := q.pair(in, "a1", false, now); id != "toolu_original" {
		t.Fatalf("original pair %q", id)
	}
	if q.handBack(in, "a1", nil, now) {
		t.Fatal("a stale ambiguous read handed back an already-paired attempt")
	}
	if id := q.pair(in, "a1", true, now); id != "toolu_original" {
		t.Fatalf("the safe original pairing was lost: %q", id)
	}
}
