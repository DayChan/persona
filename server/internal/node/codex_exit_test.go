package node

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// A Codex that exits in its pane while its thread stays loaded on the daemon (codex_exit.go, #2965).

func codexRow(t *testing.T, n *Node, id string) proto.Session {
	t.Helper()
	for _, s := range n.sessionList(context.Background(), false) {
		if s.SessionID == id {
			return s
		}
	}
	t.Fatalf("session %s not listed", id)
	return proto.Session{}
}

func dropPane(n *Node, id string) {
	n.mu.Lock()
	delete(n.agents, id)
	n.mu.Unlock()
}

func TestCodexExitedPaneUnsubscribesAndListsNotRunning(t *testing.T) {
	old := codexExitGrace
	codexExitGrace = 0
	t.Cleanup(func() { codexExitGrace = old })
	n, f, _, _ := daemonPane(t)
	ctx := context.Background()
	n.joinCodexPanes(ctx)
	waitFor(t, "joined", func() bool { return n.codexJoined(remoteRaw) })
	n.releaseExitedCodex(ctx) // the pane still runs it: nothing let go
	if f.count("thread/unsubscribe") != 0 || n.codexExitedIdle(remoteRaw, proto.StatusDone) {
		t.Fatal("released a thread whose pane still runs it")
	}
	// A join from the phone (attach, or a send that joined) is remembered, so a reconnect would subscribe it again.
	n.codices.mu.Lock()
	n.codices.config.Joined[remoteRaw] = "daemon"
	n.codices.mu.Unlock()

	dropPane(n, remoteRaw) // the TUI quit: the terminal is back at its shell
	n.releaseExitedCodex(ctx)
	if got := f.sent("thread/unsubscribe"); len(got) != 1 || got[0]["threadId"] != remoteRaw {
		t.Fatalf("thread/unsubscribe %v", got)
	}
	if n.codexJoined(remoteRaw) {
		t.Error("still joined after unsubscribing")
	}
	n.codices.mu.Lock()
	_, remembered := n.codices.config.Joined[remoteRaw]
	n.codices.mu.Unlock()
	if remembered {
		t.Error("the remembered join survives: a reconnect would subscribe it again")
	}
	n.releaseExitedCodex(ctx)
	if f.count("thread/unsubscribe") != 1 {
		t.Errorf("unsubscribed again: %d", f.count("thread/unsubscribe"))
	}
	n.joinCodexPanes(ctx) // no pane: not joined again
	time.Sleep(50 * time.Millisecond)
	if n.codexJoined(remoteRaw) {
		t.Error("joined again without a pane")
	}

	// Still loaded on the daemon, but nothing runs it: not running, and out of the workspace with its directory.
	n.mu.Lock()
	n.layouts = map[string]layout{host.HerdrCore: {spaces: map[string]host.Space{
		"space": {ID: "space", Label: "Project", Cwd: n.home, Number: 1},
	}}}
	n.mu.Unlock()
	s := codexRow(t, n, remoteRaw)
	if s.Status != proto.StatusOffline || s.SourceKind != "codex_app_server" {
		t.Errorf("exited thread lists as %q (%q), want offline on its app-server", s.Status, s.SourceKind)
	}
	if s.SpaceID != "" {
		t.Errorf("exited thread placed in workspace %q", s.SpaceID)
	}
	// A turn on it (a message from the phone) is work: listed as running while it lasts.
	if n.codexExitedIdle(remoteRaw, proto.StatusWorking) || n.codexExitedIdle(remoteRaw, proto.StatusBlocked) {
		t.Error("a running turn lists as not running")
	}

	// The TUI resumes it in a pane: live again, and the next exit releases it again.
	n.mu.Lock()
	n.agents[remoteRaw] = host.Agent{Agent: "codex", PaneID: "w1:p2", Cwd: n.home, AgentStatus: proto.StatusIdle}
	n.mu.Unlock()
	n.releaseExitedCodex(ctx)
	if n.codexExitedIdle(remoteRaw, proto.StatusDone) {
		t.Error("a thread back in a pane still lists as exited")
	}
}

// A pane missing from one refresh is not an exit: the thread is let go only after codexExitGrace.
func TestCodexExitWaitsForTheGrace(t *testing.T) {
	n, f, _, _ := daemonPane(t)
	ctx := context.Background()
	n.joinCodexPanes(ctx)
	waitFor(t, "joined", func() bool { return n.codexJoined(remoteRaw) })
	n.releaseExitedCodex(ctx)
	dropPane(n, remoteRaw)
	n.releaseExitedCodex(ctx)
	if f.count("thread/unsubscribe") != 0 || !n.codexJoined(remoteRaw) {
		t.Fatal("released within the grace")
	}
	n.cxExit.mu.Lock()
	n.cxExit.inPane[remoteRaw] = time.Now().Add(-codexExitGrace)
	n.cxExit.mu.Unlock()
	n.releaseExitedCodex(ctx)
	if f.count("thread/unsubscribe") != 1 || n.codexJoined(remoteRaw) {
		t.Fatal("not released after the grace")
	}
}

// A thread the daemon unloaded is plain history: its exit mark goes.
func TestCodexExitMarkGoesWhenUnloaded(t *testing.T) {
	old := codexExitGrace
	codexExitGrace = 0
	t.Cleanup(func() { codexExitGrace = old })
	n, f, c, _ := daemonPane(t)
	ctx := context.Background()
	n.releaseExitedCodex(ctx)
	dropPane(n, remoteRaw)
	n.releaseExitedCodex(ctx)
	if !n.codexExitedIdle(remoteRaw, proto.StatusDone) {
		t.Fatal("not marked as exited")
	}
	if f.count("thread/unsubscribe") != 0 {
		t.Error("unsubscribed a thread it never joined")
	}
	f.mu.Lock()
	f.threads = nil
	f.mu.Unlock()
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	n.releaseExitedCodex(ctx)
	if n.codexExitedIdle(remoteRaw, proto.StatusDone) {
		t.Error("the mark outlives the loaded thread")
	}
}
