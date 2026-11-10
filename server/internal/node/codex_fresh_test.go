package node

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

// A Codex opened by hand in a pane (Herdr App names no thread) before its first message has no rollout: the newest one
// in its directory is the conversation just closed there, which it must not show (#2965).
func TestCodexNewPaneDoesNotTakeTheClosedConversation(t *testing.T) {
	old := codexPaneStartup
	codexPaneStartup = 0
	t.Cleanup(func() { codexPaneStartup = old })
	now := time.Now()
	n := &Node{home: t.TempDir()}
	writeMetaRollout(t, n.codexHome(), threadOld, `{"id":"`+threadOld+`","cwd":"/work/app","source":"cli"}`, now.Add(-time.Minute))
	pane := host.Agent{Agent: "codex", PaneID: "hc:term-2", Cwd: "/work/app"}
	n.noteCodexPanes(nil) // node's first look: no Codex yet
	n.noteCodexPanes([]host.Agent{pane})
	if got := n.codexSession(pane, map[string]host.Agent{}); got != "" {
		t.Fatalf("a new Codex took %s, the conversation closed before it started", got)
	}
	// Its first message writes its own rollout: that one.
	writeMetaRollout(t, n.codexHome(), threadNew, `{"id":"`+threadNew+`","cwd":"/work/app","source":"cli"}`, time.Now().Add(time.Second))
	delete(rolloutCaches.byHome, n.codexHome())
	if got := n.codexSession(pane, map[string]host.Agent{}); got != threadNew {
		t.Fatalf("codexSession = %q, want its own thread", got)
	}
}

// A pane there when node started (or shown up within codexPaneStartup) has no known age: guessed as before.
func TestCodexPaneFromBeforeNodeStartedIsGuessedAsBefore(t *testing.T) {
	now := time.Now()
	n := &Node{home: t.TempDir()}
	writeMetaRollout(t, n.codexHome(), threadOld, `{"id":"`+threadOld+`","cwd":"/work/app","source":"cli"}`, now.Add(-time.Hour))
	pane := host.Agent{Agent: "codex", PaneID: "hc:term-1", Cwd: "/work/app"}
	n.noteCodexPanes(nil)
	n.noteCodexPanes([]host.Agent{pane}) // a slow host answers right after the start
	if got := n.codexSession(pane, map[string]host.Agent{}); got != threadOld {
		t.Fatalf("codexSession = %q, want the thread it runs", got)
	}
}

// Codex quit and started again in the same terminal is a new Codex, however soon it comes back: the list it is missing
// from is every host's last answer, so a pane not in it is gone (review of #2975).
func TestCodexRestartedInTheSameTerminalDoesNotTakeTheClosedConversation(t *testing.T) {
	old := codexPaneStartup
	codexPaneStartup = 0
	t.Cleanup(func() { codexPaneStartup = old })
	n := &Node{home: t.TempDir()}
	pane := host.Agent{Agent: "codex", PaneID: "hc:term-3", Cwd: "/work/app"}
	n.noteCodexPanes(nil)
	n.noteCodexPanes([]host.Agent{pane})
	first := n.codexPaneSince(pane.PaneID)
	n.noteCodexPanes([]host.Agent{pane}) // still there: same age
	if got := n.codexPaneSince(pane.PaneID); !got.Equal(first) {
		t.Fatalf("a pane still listed restarted its age: %v, was %v", got, first)
	}
	// Its conversation is written up to the quit, then the terminal is back at its shell.
	writeMetaRollout(t, n.codexHome(), threadOld, `{"id":"`+threadOld+`","cwd":"/work/app","source":"cli"}`, time.Now())
	n.noteCodexPanes(nil)
	time.Sleep(time.Second) // codex typed again
	n.noteCodexPanes([]host.Agent{pane})
	if got := n.codexPaneSince(pane.PaneID); !got.After(first) {
		t.Fatalf("a Codex started again counts from %v, the first one's %v", got, first)
	}
	if got := n.codexSession(pane, map[string]host.Agent{}); got != "" {
		t.Fatalf("the Codex started again took %s, the conversation just closed", got)
	}
}
