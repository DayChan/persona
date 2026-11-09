package node

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func stalledCodexNode(t *testing.T) (*Node, *fakeHerdr) {
	t.Helper()
	withCodexLaunchers(t, map[string][]string{"codex": {"codex"}})
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.reply("agent.get", map[string]any{"agent": agentOf("codex", "", proto.StatusBlocked, "w1:p2", "w1:t2", "w1", cwd, "")})
	f.setScreen("w1:p2", "Codex is waiting for setup\n")
	old := codexStartWait
	codexStartWait = 0
	t.Cleanup(func() { codexStartWait = old })
	return n, f
}

func TestCodexStalledStartClosesBeforeRetry(t *testing.T) {
	n, f := stalledCodexNode(t)
	tallPane(f, 51) // diagnostic fallback must retain the screen and code before closing the pane
	for range 2 {
		_, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "do the task"})
		var coded *rpc.Error
		if !errors.As(err, &coded) || coded.Code != proto.CodeAgentStartStalled || !strings.Contains(coded.Message, "Codex is waiting for setup") {
			t.Fatalf("stalled start lost its code or screen: %v", err)
		}
	}
	var lifecycle []string
	for _, c := range f.calls() {
		if c.Method == "tab.create" || c.Method == "tab.close" {
			lifecycle = append(lifecycle, c.Method)
		}
	}
	if got := strings.Join(lifecycle, ","); got != "tab.create,tab.close,tab.create,tab.close" {
		t.Fatalf("a retry opened beside an abandoned pane: %s", got)
	}
	if len(n.codexAbandoned) != 0 {
		t.Fatal("closed panes remain pending")
	}
}

func TestCodexStalledCleanupFailureBlocksAnotherTab(t *testing.T) {
	n, f := stalledCodexNode(t)
	f.reply("tab.close", herdrFail{Code: "unavailable", Message: "terminal unavailable"})
	for range 2 {
		_, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "do the task"})
		if code, _ := refusalOf(t, err); code != proto.CodeAgentStartStalled || !strings.Contains(err.Error(), "terminal unavailable") {
			t.Fatalf("cleanup failure: %v", err)
		}
	}
	if got := len(f.trace("tab.create")); got != 1 {
		t.Fatalf("opened %d tabs while cleanup was failing", got)
	}
	f.reply("tab.close", map[string]any{})
	_, _ = request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "do the task"})
	if got := len(f.trace("tab.create")); got != 2 || len(n.codexAbandoned) != 0 {
		t.Fatalf("cleanup did not release retry: %d tabs, %d pending", got, len(n.codexAbandoned))
	}
}

func TestCodexCancelledWaitStillCloses(t *testing.T) {
	n, f := stalledCodexNode(t)
	codexStartWait = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.after(func(c herdrCall) {
		if c.Method == "pane.read" {
			cancel()
		}
	})
	_, err := n.startCodex(ctx, n.defaultHost().(host.Launcher), filepath.Join(filepath.Dir(n.dir), "proj"), "codex", []string{"codex"}, "do the task", "w1", "", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if len(f.trace("tab.close")) != 1 || len(n.codexAbandoned) != 0 {
		t.Fatal("cancelled request prevented cleanup")
	}
}

func TestCodexRetryAfterAbandonedPaneWasRemoved(t *testing.T) {
	for _, missing := range []string{"pane_not_found", "tab_not_found", "workspace_not_found"} {
		t.Run(missing, func(t *testing.T) {
			n, f := stalledCodexNode(t)
			f.reply("tab.close", herdrFail{Code: "unavailable", Message: "terminal unavailable"})
			_, _ = request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "do the task"})
			f.reply("tab.close", herdrFail{Code: missing, Message: "already gone"})
			_, _ = request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "do the task"})
			if len(f.trace("tab.create")) != 2 || len(n.codexAbandoned) != 0 {
				t.Fatalf("gone pane blocked retry: %d tabs, %d pending", len(f.trace("tab.create")), len(n.codexAbandoned))
			}
		})
	}
}

func TestCodexCleanupNeedsFreshProofPaneIsGone(t *testing.T) {
	for _, state := range []string{"unreachable", "moved"} {
		t.Run(state, func(t *testing.T) {
			n, f := stalledCodexNode(t)
			f.reply("tab.close", herdrFail{Code: "unavailable", Message: "terminal unavailable"})
			_, _ = request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "do the task"})
			f.reply("tab.close", herdrFail{Code: "tab_not_found", Message: "old tab is gone"})
			if state == "unreachable" {
				f.reply("session.snapshot", herdrFail{Code: "unavailable", Message: "no fresh layout"})
			} else {
				f.reply("session.snapshot", snapshot(nil, nil, nil, []any{shellPane("w1:p2", "w1:t3", "w1", "", "")}))
			}
			_, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "do the task"})
			if code, _ := refusalOf(t, err); code != proto.CodeAgentStartStalled || len(f.trace("tab.create")) != 1 || len(n.codexAbandoned) != 1 {
				t.Fatalf("uncertain cleanup released retry: %v, %d tabs, %d pending", err, len(f.trace("tab.create")), len(n.codexAbandoned))
			}
		})
	}
}

func TestCodexTimeoutWithoutAgentKeepsGenericFailure(t *testing.T) {
	n, f := stalledCodexNode(t)
	f.reply("agent.get", herdrFail{Code: "agent_not_found", Message: "no agent"})
	_, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "launcher": "codex", "prompt": "do the task"})
	var coded *rpc.Error
	if err == nil || errors.As(err, &coded) {
		t.Fatalf("timeout without an agent should remain a launcher failure: %v", err)
	}
}

func TestCodexStalledCodeSurvivesStartLog(t *testing.T) {
	var starts startLog
	rec := &startRec{done: make(chan struct{})}
	starts.add("pending", rec)
	starts.finish("pending", fmt.Errorf("start failed: %w", &rpc.Error{Code: proto.CodeAgentStartStalled, Message: "stalled"}))
	if rec.session.StartErrorCode != proto.CodeAgentStartStalled {
		t.Fatalf("session lost code: %+v", rec.session)
	}
	if code, _ := refusalOf(t, starts.wait(context.Background(), "pending")); code != proto.CodeAgentStartStalled {
		t.Fatalf("wait lost code: %s", code)
	}
}
