package node

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestClaudeQuestionCancelDeniesOnlyQuestionWithoutAnswer(t *testing.T) {
	n, _ := testNode(t)
	fh := withFakeHub(t, n)
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Cancel: true} }
	fh.mu.Unlock()
	out := runQuestionHook(t, n, "PermissionRequest", []map[string]any{{"question": "Which color?"}})
	decision, _ := out["decision"].(map[string]any)
	if decision["behavior"] != "deny" || decision["updatedInput"] != nil || out["continue"] != nil {
		t.Fatalf("question cancellation supplied input or stopped the task: %+v", out)
	}
	if ix := fh.interactions(); len(ix) != 1 || !ix[0].CanCancel {
		t.Fatalf("capability: %+v", ix)
	}
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Next question?": "Blue"}}
	}
	fh.mu.Unlock()
	out = runQuestionHook(t, n, "PermissionRequest", []map[string]any{{"question": "Next question?"}})
	decision, _ = out["decision"].(map[string]any)
	if decision["behavior"] != "allow" {
		t.Fatalf("later question cannot be answered: %+v", out)
	}
}

func TestOwnedCodexQuestionCancelReturnsNoAnswers(t *testing.T) {
	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Cancel: true} }
	fh.mu.Unlock()
	p, c, f := newFakeCodex(t, n, t.TempDir())
	p.id = testThread
	c.threadID, c.turnID = testThread, "turn-1"
	out := f.request(71, "item/tool/requestUserInput", map[string]any{"threadId": testThread, "turnId": "turn-1", "itemId": "question", "questions": []any{map[string]any{"id": "color", "question": "Which color?"}}})
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"answers":{}}` {
		t.Fatalf("cancellation supplied an answer: %s", b)
	}
	if ix := fh.interactions(); len(ix) != 1 || !ix[0].CanCancel {
		t.Fatalf("capability: %+v", ix)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.got {
		if m.Method == "turn/interrupt" || m.Method == "turn/start" || m.Method == "turn/steer" {
			t.Fatalf("cancellation changed the turn: %s", m.Method)
		}
	}
}

func TestSharedCodexQuestionCancelIsOneEmptyReplyWithoutInterrupt(t *testing.T) {
	n := remoteTestNode(t)
	fh := withFakeHub(t, n)
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Cancel: true} }
	fh.mu.Unlock()
	home := t.TempDir()
	f, c := connectRemoteFixture(t, n, home, "one", remoteRaw)
	id := n.registerCodex(home, remoteRaw, "")
	if err := n.attachCodex(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	req := map[string]any{"id": 71, "method": "item/tool/requestUserInput", "params": map[string]any{"threadId": remoteRaw, "itemId": "question", "questions": []any{map[string]any{"id": "color", "question": "Which color?"}}}}
	f.push(req)
	waitFor(t, "empty question resolution", func() bool { return f.replyCount() == 1 })
	f.mu.Lock()
	result := string(f.replies[0].Result)
	f.mu.Unlock()
	if result != `{"answers":{}}` {
		t.Fatalf("cancellation supplied an answer: %s", result)
	}
	f.push(req)
	time.Sleep(80 * time.Millisecond)
	if f.replyCount() != 1 || len(fh.interactions()) != 1 {
		t.Fatal("old question was asked or resolved twice")
	}
	if f.count("turn/interrupt") != 0 || f.count("turn/start") != 0 || f.count("turn/steer") != 0 {
		t.Fatal("cancel submitted input or interrupted task")
	}
	c.mu.Lock()
	askCount := len(c.asks)
	c.mu.Unlock()
	if askCount != 0 || c.ctx.Err() != nil {
		t.Fatal("request waiter retained or runtime stopped")
	}
}

func TestTerminalQuestionCancelNeverSendsEscape(t *testing.T) {
	n, _ := testNode(t)
	if err := n.codexAnswer(context.Background(), "session", "question", proto.Resolution{Cancel: true}); err == nil {
		t.Fatal("unsupported cancellation accepted")
	}
}
