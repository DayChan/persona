package node

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Codex panes whose thread nothing names (Herdr App, a new thread without a title) and the bindings a phone start
// leaves (review of #2975).

const (
	guessA = "0b000000-0000-4000-8000-0000000000a1"
	guessB = "0b000000-0000-4000-8000-0000000000b2"
	guessC = "0b000000-0000-4000-8000-0000000000c3"
)

func codexPane(pane, cwd string) host.Agent {
	return host.Agent{Agent: "codex", PaneID: pane, Cwd: cwd, AgentStatus: proto.StatusIdle}
}

func paneOf(n *Node, id string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.agents[id].PaneID
}

// A binding names the Codex it was made for: once another Codex runs in the pane, it names nothing.
func TestCodexBindingEndsWithItsCodex(t *testing.T) {
	old := codexPaneStartup
	codexPaneStartup = 0
	t.Cleanup(func() { codexPaneStartup = old })
	n, home := testNode(t)
	n.metas, n.panes, n.panePIDs = map[string]quickMeta{}, map[string]string{}, map[string]panePID{}
	cwd := "/work/app"
	writeRollout(t, home, guessA, cwd, time.Now(), time.Now().Add(-time.Minute))
	p := codexPane("w1:p1", cwd)
	n.setAgents(nil)
	n.bindPane(p.PaneID, guessA) // a phone start: bound before its Codex shows
	n.setAgents([]host.Agent{p})
	if got := paneOf(n, guessA); got != p.PaneID {
		t.Fatalf("the bound pane runs %q", got)
	}
	n.setAgents([]host.Agent{p}) // the same Codex: still bound
	if got := paneOf(n, guessA); got != p.PaneID {
		t.Fatalf("the binding went while its Codex runs: %q", got)
	}
	n.setAgents(nil) // Codex quit
	time.Sleep(5 * time.Millisecond)
	n.setAgents([]host.Agent{p}) // codex typed again in the same terminal
	if got := paneOf(n, guessA); got != "" {
		t.Fatalf("a new Codex in the pane shows the closed thread through its old binding (%q)", got)
	}
	n.mu.Lock()
	_, kept := n.panes[p.PaneID]
	n.mu.Unlock()
	if kept {
		t.Error("the stale binding is kept")
	}

	// A binding kept from before node started belongs to a pane of unknown age: a pane there at the start.
	m, home2 := testNode(t)
	m.metas, m.panes, m.panePIDs = map[string]quickMeta{}, map[string]string{}, map[string]panePID{}
	writeRollout(t, home2, guessA, cwd, time.Now(), time.Now().Add(-time.Minute))
	m.mu.Lock()
	m.panes[p.PaneID] = guessA
	m.stampBinding(p.PaneID, paneBindAge{set: true})
	m.mu.Unlock()
	codexPaneStartup = time.Minute
	m.setAgents([]host.Agent{p}) // node's first look
	if got := paneOf(m, guessA); got != p.PaneID {
		t.Fatalf("a kept binding of a pane from before the start: %q", got)
	}
}

// Three Codex panes in one directory, none named: A started from the phone in P, quit; C opened by hand in Q; B opened
// by hand in P. The guess for Q moves from C to B once B writes, and back. Neither guess may let C go, and B is
// never joined on a guess.
func TestCodexGuessedPanesAreNeitherJoinedNorLetGo(t *testing.T) {
	oldGrace, oldStartup := codexExitGrace, codexPaneStartup
	codexExitGrace, codexPaneStartup = 0, 0
	t.Cleanup(func() { codexExitGrace, codexPaneStartup = oldGrace, oldStartup })
	n := remoteTestNode(t)
	withFakeHub(t, n)
	n.panes = map[string]string{}
	home := canonicalCodexHome(filepath.Join(n.home, ".codex"), n.home)
	f, c := connectRemoteFixture(t, n, home, "daemon", guessA, guessB, guessC)
	ctx := context.Background()
	cwd := n.home
	write := func(id string) {
		time.Sleep(5 * time.Millisecond)
		writeRollout(t, n.home, id, cwd, time.Now(), time.Now())
		delete(rolloutCaches.byHome, home)
		n.registerCodex(home, id, rolloutPath(home, id))
		time.Sleep(5 * time.Millisecond)
	}
	pass := func(panes ...host.Agent) {
		n.setAgents(panes)
		n.releaseExitedCodex(ctx)
		n.joinCodexPanes(ctx)
		time.Sleep(30 * time.Millisecond)
	}
	P, Q := codexPane("w1:p1", cwd), codexPane("w1:p2", cwd)

	n.setAgents(nil)
	n.bindPane(P.PaneID, guessA) // started from the phone
	write(guessA)
	pass(P)
	waitFor(t, "A joined", func() bool { return n.codexJoined(guessA) })

	pass(P, Q) // C opened by hand: no rollout yet
	write(guessC)
	pass(Q, P)
	if got := paneOf(n, guessC); got != Q.PaneID {
		t.Fatalf("C's first turn: Q runs %q", got)
	}
	if n.codexJoined(guessC) {
		t.Fatal("a guessed pane was joined")
	}
	if route, note := n.codexRoute(guessC); route != codexRouteTerminal || note == "" {
		t.Errorf("a guessed pane's route %q %q", route, note)
	}
	if err := n.attachCodex(ctx, guessC); err != nil { // the phone joins C itself
		t.Fatal(err)
	}

	pass(Q)    // A quits
	pass(Q, P) // B opened by hand in P
	waitFor(t, "A let go", func() bool { return !n.codexJoined(guessA) })
	if got := paneOf(n, guessA); got != "" {
		t.Fatalf("P shows the closed A through its old binding (%q)", got)
	}
	write(guessB) // B's first turn: Q's guess moves to B
	for range 3 {
		pass(Q, P)
	}
	if paneOf(n, guessB) != Q.PaneID || paneOf(n, guessC) != "" {
		t.Fatalf("not the review's case: B in %q, C in %q", paneOf(n, guessB), paneOf(n, guessC))
	}
	pass(Q) // B closed
	for range 3 {
		pass(Q)
	}
	if !n.codexJoined(guessC) || n.codexExitedIdle(guessC, proto.StatusDone) {
		t.Fatalf("C, running in Q, was let go: joined %v, exited %v", n.codexJoined(guessC), n.codexExitedIdle(guessC, proto.StatusDone))
	}
	if n.codexJoined(guessB) {
		t.Error("B was joined on a guess")
	}
	if got := f.sent("thread/unsubscribe"); len(got) != 1 || got[0]["threadId"] != guessA {
		t.Errorf("thread/unsubscribe %v, want only A", got)
	}
	if err := c.refresh(); err != nil {
		t.Fatal(err)
	}
	for _, s := range n.sessionList(ctx, false) {
		if s.SessionID == guessC && s.Status == proto.StatusOffline {
			t.Errorf("C lists as not running")
		}
	}
}
