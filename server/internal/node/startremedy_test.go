package node

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// keptByAQuestion has n start in w1 and stop on a startup question, so the tab w1:p2 is kept (shell_waiting).
func keptByAQuestion(t *testing.T, n *Node, f *fakeHerdr, home string) {
	t.Helper()
	watchShell(n)
	n.shellAsk = 300 * time.Millisecond
	newShellTab(f, "zsh")
	f.setScreen("w1:p2", "[oh-my-zsh] Would you like to update? [Y/n] ")
	if _, err := request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude"}); rpcCode(err) != proto.CodeShellWaiting {
		t.Fatalf("start: %v", err)
	}
}

// What the remedying main conversation types (#2570) goes only into a failed start's kept tab, while no agent runs
// there, and only a short answer: text as is, then the keys.
func TestStartKeysGoOnlyIntoAKeptTab(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, time.Minute, time.Minute)
	keptByAQuestion(t, n, f, home)
	if _, err := request(t, n, "start.keys", map[string]any{"pane": "w1:p2", "text": "n", "keys": []string{"enter"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.trace("pane.send_text", "pane.send_keys"); len(got) != 2 ||
		got[0] != call("pane.send_text", map[string]any{"pane_id": "w1:p2", "text": "n"}) ||
		got[1] != call("pane.send_keys", map[string]any{"pane_id": "w1:p2", "keys": []string{"enter"}}) {
		t.Errorf("typed %v", got)
	}
	for _, tc := range []struct {
		name   string
		params map[string]any
		code   string
	}{
		{"another pane", map[string]any{"pane": "w1:p9", "keys": []string{"y"}}, "start_tab_gone"},
		{"a long text", map[string]any{"pane": "w1:p2", "text": strings.Repeat("y", 33)}, "too_large"},
		{"too many keys", map[string]any{"pane": "w1:p2", "keys": []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}}, "too_large"},
	} {
		if _, err := request(t, n, "start.keys", tc.params); rpcCode(err) != tc.code {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	n.mu.Lock()
	n.agents["later"] = host.Agent{Agent: "claude", PaneID: "w1:p2"} // an agent came up there after all
	n.mu.Unlock()
	if _, err := request(t, n, "start.keys", map[string]any{"pane": "w1:p2", "keys": []string{"y"}}); rpcCode(err) != "start_tab_gone" {
		t.Errorf("typed into an agent's pane: %v", err)
	}
}

// A start named with in_pane runs in that kept tab instead of opening another, and takes it over: it is no longer
// kept. A pane that is not kept gets a new tab.
func TestStartInAKeptTab(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, f, home := herdrNode(t)
	withStartWaits(n, time.Second, time.Minute)
	keptByAQuestion(t, n, f, home)
	f.setScreen("w1:p2", "[oh-my-zsh] Would you like to update? [Y/n] n\n% ") // answered
	n.shellQuiet = 200 * time.Millisecond
	_, _ = request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude", "in_pane": "w1:p2"})
	if got := f.trace("tab.create"); len(got) != 1 {
		t.Errorf("opened another tab: %v", got)
	}
	if got := sent(f, "w1:p2"); len(got) != 1 {
		t.Errorf("typed %q into the kept tab", got)
	}
	n.mu.Lock()
	_, kept := n.keptStarts["w1:p2"]
	n.mu.Unlock()
	if kept {
		t.Error("still kept after a start took it over")
	}
	_, _ = request(t, n, "session.start", map[string]any{"cwd": home, "workspace_id": "w1", "launcher": "claude", "in_pane": "w1:p7"})
	if got := f.trace("tab.create"); len(got) != 2 {
		t.Errorf("a pane that is not kept: tabs %v", got)
	}
}
