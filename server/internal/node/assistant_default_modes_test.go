package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// Exercise the real process startup, mode persistence, and acknowledged Claude control protocol.
func TestClaudeNewConversationPermissionModes(t *testing.T) {
	for _, mode := range proto.PermissionModes {
		t.Run(mode, func(t *testing.T) {
			f := sdkTest(t)
			id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, mode, "", sdkExec(proto.AssistantRuntimeAgentSDK))
			if err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)
			st, ok := f.n.state.get(id)
			if !ok || st.AppMode != mode {
				t.Fatalf("persisted mode: %+v", st)
			}
			p := f.proc(id)
			p.mu.Lock()
			applied := p.mode
			p.mu.Unlock()
			if applied != mode {
				t.Fatalf("applied mode: %q, want %q", applied, mode)
			}
			if mode == proto.ModeBypass {
				var start struct {
					Options struct{ PermissionMode string }
				}
				if err := json.Unmarshal([]byte(f.file("start.json")), &start); err != nil {
					t.Fatal(err)
				}
				if start.Options.PermissionMode != mode {
					t.Fatalf("start options: %+v", start)
				}
			} else if !strings.Contains(f.file("stdin.jsonl"), `"mode":"`+mode+`"`) {
				t.Fatalf("Claude did not receive mode %s: %s", mode, f.file("stdin.jsonl"))
			}
		})
	}
}
