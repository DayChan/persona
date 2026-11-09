package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestNavigationKeepsATrustedUserCopyOfASupervisionTemplate(t *testing.T) {
	text := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{{ID: "task-demo", Title: "Verify a test change", Kind: "review"}}})
	line, _ := json.Marshal(map[string]any{"type": "user", "uuid": "user-copy", "message": map[string]any{"role": "user", "content": text}})
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	if err := os.WriteFile(path, append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	f := transcript.NewFile(path)
	n := &Node{home: t.TempDir(), paths: map[string]string{}, files: map[string]*transcript.File{"demo-session": f},
		procs: map[string]*assistantProc{}, agents: map[string]host.Agent{}, state: &stateFile{Sessions: map[string]sessionState{}}}
	n.echoes.expect(echoRecord{SessionID: "demo-session", ClientMsgID: "client-demo", Line: "user-copy", SentAt: time.Now()})
	out, err := n.handle(context.Background(), "session.navigation", json.RawMessage(`{"session_id":"demo-session"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	var index proto.SessionNavigation
	if err := json.Unmarshal(raw, &index); err != nil || len(index.Messages) != 1 || index.Messages[0].ID != "user-copy#0" {
		t.Fatalf("trusted user copy was dropped: %s err=%v", raw, err)
	}
}

func TestSessionNavigationAndTargetWindow(t *testing.T) {
	var lines strings.Builder
	for q := 1; q <= 60; q++ {
		fmt.Fprintf(&lines, `{"type":"user","uuid":"user-%d","timestamp":"2026-11-09T10:00:00Z","message":{"role":"user","content":"Question %d"}}`+"\n", q, q)
		fmt.Fprintf(&lines, `{"type":"assistant","uuid":"answer-%d","message":{"role":"assistant","content":[{"type":"text","text":"An answer"}]}}`+"\n", q)
	}
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	if err := os.WriteFile(path, []byte(lines.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: t.TempDir(), paths: map[string]string{}, files: map[string]*transcript.File{"demo-session": transcript.NewFile(path)},
		procs: map[string]*assistantProc{}, agents: map[string]host.Agent{}, state: &stateFile{Sessions: map[string]sessionState{}}}
	call := func(method string, params map[string]any, into any) error {
		t.Helper()
		params["session_id"] = "demo-session"
		raw, _ := json.Marshal(params)
		result, err := n.handle(context.Background(), method, raw)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(result)
		return json.Unmarshal(b, into)
	}
	var index proto.SessionNavigation
	if err := call("session.navigation", map[string]any{}, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Messages) != 40 || index.Messages[0].Text != "Question 21" || !index.HasMore || index.NextBeforeSeq != index.Messages[0].Seq {
		t.Fatalf("index=%+v", index)
	}
	var older proto.SessionNavigation
	if err := call("session.navigation", map[string]any{"before_seq": index.NextBeforeSeq, "history": index.History}, &older); err != nil || len(older.Messages) != 20 || older.HasMore {
		t.Fatalf("older=%+v err=%v", older, err)
	}
	target := older.Messages[4]
	var window struct {
		Items     []proto.Item `json:"items"`
		HasMore   bool         `json:"has_more"`
		MoreAfter bool         `json:"more_after"`
		History   string       `json:"history"`
		From      int          `json:"from_seq"`
		To        int          `json:"to_seq"`
	}
	if err := call("session.items", map[string]any{"around_seq": target.Seq, "limit": 20, "history": index.History, "fold_work": true, "lazy_images": true}, &window); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range window.Items {
		found = found || item.ID == target.ID
	}
	if !found || !window.MoreAfter || window.To >= 120 || window.History != index.History {
		t.Fatalf("around window=%+v", window)
	}
	for _, params := range []map[string]any{
		{"around_seq": target.Seq},
		{"after_seq": target.Seq},
		{"before_seq": target.Seq, "turns": 2},
		{"from_seq": target.Seq, "through_seq": target.Seq + 3},
	} {
		params["history"] = "stale-history"
		err := call("session.items", params, &window)
		var coded *rpc.Error
		if !errors.As(err, &coded) || coded.Code != "stale_history" {
			t.Fatalf("stale request %+v err=%v", params, err)
		}
	}
}
