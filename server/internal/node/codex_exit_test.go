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

func remembered(n *Node, id string) bool {
	n.codices.mu.Lock()
	defer n.codices.mu.Unlock()
	_, ok := n.codices.config.Joined[id]
	return ok
}

// exitedPane is daemonPane with no grace, joined, then its pane's Codex gone and the release run until it let go.
func exitedPane(t *testing.T) (*Node, *remoteFixture, *codexRemote) {
	t.Helper()
	old := codexExitGrace
	codexExitGrace = 0
	t.Cleanup(func() { codexExitGrace = old })
	n, f, c, _ := daemonPane(t)
	ctx := context.Background()
	n.joinCodexPanes(ctx)
	waitFor(t, "joined", func() bool { return n.codexJoined(remoteRaw) })
	n.releaseExitedCodex(ctx) // the pane still runs it: nothing let go
	if f.count("thread/unsubscribe") != 0 || n.codexExitedIdle(remoteRaw, proto.StatusDone) {
		t.Fatal("released a thread whose pane still runs it")
	}
	// A join from the phone before the exit is remembered: a reconnect would subscribe it again.
	n.codices.mu.Lock()
	n.codices.config.Joined[remoteRaw] = "daemon"
	n.codices.mu.Unlock()
	dropPane(n, remoteRaw) // the TUI quit: the terminal is back at its shell
	n.releaseExitedCodex(ctx)
	waitFor(t, "let go", func() bool { return !n.codexJoined(remoteRaw) })
	return n, f, c
}

func TestCodexExitedPaneUnsubscribesAndListsNotRunning(t *testing.T) {
	n, f, _ := exitedPane(t)
	ctx := context.Background()
	if got := f.sent("thread/unsubscribe"); len(got) != 1 || got[0]["threadId"] != remoteRaw {
		t.Fatalf("thread/unsubscribe %v", got)
	}
	if remembered(n, remoteRaw) {
		t.Error("the remembered join survives: a reconnect would subscribe it again")
	}
	n.releaseExitedCodex(ctx)
	time.Sleep(50 * time.Millisecond)
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

	// The TUI resumes it in a pane: live again.
	n.mu.Lock()
	n.agents[remoteRaw] = host.Agent{Agent: "codex", PaneID: "w1:p2", Cwd: n.home, AgentStatus: proto.StatusIdle}
	n.mu.Unlock()
	n.releaseExitedCodex(ctx)
	if n.codexExitedIdle(remoteRaw, proto.StatusDone) {
		t.Error("a thread back in a pane still lists as exited")
	}
}

// A message from the phone to an exited thread joins it for that turn only: not remembered, kept while the turn has
// time to show, then let go again once idle (review of #2975).
func TestCodexExitedThreadPhoneMessageIsLetGoAgain(t *testing.T) {
	n, f, c := exitedPane(t)
	ctx := context.Background()
	if err := n.send(ctx, remoteRaw, "hi", nil, sendOpts{}); err != nil {
		t.Fatal(err)
	}
	if starts := f.sent("turn/start"); len(starts) != 1 || inputText(starts[0]) != "hi" {
		t.Fatalf("turn/start %v", starts)
	}
	if !n.codexJoined(remoteRaw) {
		t.Fatal("the message did not join the thread")
	}
	if remembered(n, remoteRaw) {
		t.Error("the message's join is remembered: a reconnect or restart would keep the thread loaded for good")
	}
	n.releaseExitedCodex(ctx) // held: its turn has not shown as running yet
	time.Sleep(50 * time.Millisecond)
	if f.count("thread/unsubscribe") != 1 || !n.codexJoined(remoteRaw) {
		t.Fatal("let go while the phone's turn may still be starting")
	}
	if n.codexReleasable(remoteRaw, proto.StatusWorking) {
		t.Error("a running turn is releasable")
	}
	n.cxExit.mu.Lock()
	n.cxExit.held[remoteRaw] = time.Now().Add(-time.Second) // the hold is over
	n.cxExit.mu.Unlock()
	if err := c.refresh(); err != nil { // the turn ended: the thread reads idle
		t.Fatal(err)
	}
	n.releaseExitedCodex(ctx)
	waitFor(t, "let go again", func() bool { return !n.codexJoined(remoteRaw) })
	if f.count("thread/unsubscribe") != 2 {
		t.Errorf("unsubscribes %d, want 2", f.count("thread/unsubscribe"))
	}
}

// An app-server that never answers thread/unsubscribe does not hold up the card loop, and the release gives up.
func TestCodexExitReleaseDoesNotBlock(t *testing.T) {
	old, oldLeave := codexExitGrace, codexLeaveTimeout
	codexExitGrace, codexLeaveTimeout = 0, 100*time.Millisecond
	t.Cleanup(func() { codexExitGrace, codexLeaveTimeout = old, oldLeave })
	n, f, _, _ := daemonPane(t)
	ctx := context.Background()
	n.joinCodexPanes(ctx)
	waitFor(t, "joined", func() bool { return n.codexJoined(remoteRaw) })
	n.releaseExitedCodex(ctx)
	f.mu.Lock()
	f.drop = "thread/unsubscribe"
	f.mu.Unlock()
	dropPane(n, remoteRaw)
	start := time.Now()
	n.releaseExitedCodex(ctx)
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Fatalf("the release pass waited %v for the app-server", d)
	}
	waitFor(t, "the release gave up", func() bool {
		n.cxExit.mu.Lock()
		defer n.cxExit.mu.Unlock()
		return f.count("thread/unsubscribe") == 1 && !n.cxExit.leaving[remoteRaw]
	})
	if !n.codexJoined(remoteRaw) {
		t.Error("an unanswered unsubscribe counted as done")
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
	time.Sleep(50 * time.Millisecond)
	if f.count("thread/unsubscribe") != 0 || !n.codexJoined(remoteRaw) {
		t.Fatal("released within the grace")
	}
	n.cxExit.mu.Lock()
	n.cxExit.inPane[remoteRaw] = time.Now().Add(-codexExitGrace)
	n.cxExit.mu.Unlock()
	n.releaseExitedCodex(ctx)
	waitFor(t, "released after the grace", func() bool { return f.count("thread/unsubscribe") == 1 && !n.codexJoined(remoteRaw) })
}

// The mark stays while the app-server reconnects (it may still have the thread) and goes once, connected, it no
// longer has it loaded.
func TestCodexExitMarkGoesOnlyWhenUnloaded(t *testing.T) {
	n, f, c := exitedPane(t)
	ctx := context.Background()
	c.mu.Lock()
	c.ready = false // reconnecting
	c.mu.Unlock()
	n.releaseExitedCodex(ctx)
	if !n.codexExitedIdle(remoteRaw, proto.StatusDone) {
		t.Fatal("the mark went while its app-server reconnects")
	}
	c.mu.Lock()
	c.ready = true
	c.mu.Unlock()
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
