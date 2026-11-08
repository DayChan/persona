package node

import (
	"context"
	"crypto/sha256"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

// Shell tabs left behind (docs/assistant/todos.md "Closing the agent of a finished task"). An agent Persona started in a tab of
// its own can end without Persona closing it: /exit, an API error it gives up on, a launcher that quits. Its tab then
// holds only the login shell, with "Resume this session with: claude --resume <id>" as its last words, and nobody
// closes it; a busy machine collected dozens. The node closes such a tab (only the pane when the tab is split) once
// nothing but the shell has run in it and its screen has not changed for shellTabIdle. The session stays as it was:
// the next message resumes it in a new tab with the launcher it was started with (send.go send).
//
// Only panes Persona opened for an agent are looked at: newTab marks them with personaTabToken (herdr keeps a pane's
// metadata tokens until the pane goes; a herdr restart drops them, and those tabs are then left alone). A pane the user
// picked for "Run Agent Here", a tab the user opened, an assistant conversation (they run headless), another tool's
// session or any pane where another program is in front is never closed.

// personaTabToken marks a pane Persona opened for an agent; its value is the session id when the start knew it.
const personaTabToken = "persona_tab"

// shellTabIdle is how long a Persona tab must show only its shell, its screen unchanged, before it is closed;
// shellTabSweep how often the node looks.
const (
	shellTabIdle  = 10 * time.Minute
	shellTabSweep = time.Minute
)

// markPersonaTab marks pane as one Persona opened for session id (empty when not known yet).
func markPersonaTab(ctx context.Context, launch host.Launcher, pane host.PaneRef, id string) {
	r, ok := launch.(host.MetadataReporter)
	if !ok || pane.ID == "" {
		return
	}
	if id == "" {
		id = "agent"
	}
	_ = r.ReportMetadata(ctx, pane, "persona", map[string]*string{personaTabToken: &id})
}

// shellTabs is what the sweep remembers between looks: each candidate pane's screen and since when it has been so.
// It belongs to the process, not to a Node: the resident node makes a new Node for every hub connection, and a hub
// redeployed every few minutes would otherwise restart each pane's wait every time, so its tab is never closed (#5720).
type shellTabs struct {
	mu   sync.Mutex
	seen map[string]shellLook
	// idle replaces shellTabIdle when set (tests).
	idle time.Duration
}

// processShellTabs is the sweep's memory for every Node of this process (Node.shellTabs nil).
var processShellTabs shellTabs

// tabSweep is the memory this node's sweep uses.
func (n *Node) tabSweep() *shellTabs {
	if n.shellTabs != nil {
		return n.shellTabs
	}
	return &processShellTabs
}

type shellLook struct {
	screen [32]byte
	since  time.Time
}

// watchShellTabs sweeps while the node serves the hub.
func (n *Node) watchShellTabs(ctx context.Context) {
	t := time.NewTicker(shellTabSweep)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n.sweepShellTabs(ctx, time.Now()) > 0 {
			_ = n.refreshHosts(ctx)
			n.notifySessions()
		}
	}
}

// sweepShellTabs closes the Persona tabs whose agent is gone and whose shell has sat untouched for shellTabIdle, and
// returns how many it closed. It looks at the panes of the hosts' last snapshots (refreshHosts).
func (n *Node) sweepShellTabs(ctx context.Context, now time.Time) (closed int) {
	mem := n.tabSweep()
	idle := shellTabIdle
	if mem.idle > 0 {
		idle = mem.idle
	}
	type candidate struct {
		h      host.Host
		pane   host.Agent
		closer host.Organizer
		fg     host.ForegroundNamer
	}
	var cands []candidate
	n.mu.Lock()
	inUse := map[string]bool{} // panes an agent of ours runs in (one herdr briefly stopped detecting stays here)
	for _, a := range n.agents {
		inUse[a.PaneID] = true
	}
	for _, h := range n.hosts.All() {
		org, ok := h.(host.Organizer)
		fg, ok2 := h.(host.ForegroundNamer)
		if !ok || !ok2 {
			continue
		}
		for _, p := range n.layouts[h.Kind()].panes {
			if p.PaneID == "" || p.Tokens[personaTabToken] == "" || p.Agent != "" || inUse[p.PaneID] {
				continue
			}
			cands = append(cands, candidate{h, p, org, fg})
		}
	}
	n.mu.Unlock()

	live := map[string]bool{}
	for _, c := range cands {
		pane := c.pane.PaneID
		if !n.shellTabClosable(c.pane.Tokens[personaTabToken]) {
			continue
		}
		ref := n.hosts.Ref(pane)
		names, err := c.fg.PaneForeground(ctx, ref)
		if err != nil || len(names) == 0 || !allShells(names) {
			continue // something else is in front (or herdr could not say): someone is using it
		}
		screen, err := c.h.Read(ctx, ref, host.ReadOpts{Source: "recent", Lines: 40})
		if err != nil {
			continue
		}
		live[pane] = true
		sum := sha256.Sum256([]byte(screen))
		mem.mu.Lock()
		look, ok := mem.seen[pane]
		if !ok || look.screen != sum {
			if mem.seen == nil {
				mem.seen = map[string]shellLook{}
			}
			mem.seen[pane] = shellLook{screen: sum, since: now} // new, or typed into since: wait again
			mem.mu.Unlock()
			continue
		}
		mem.mu.Unlock()
		if now.Sub(look.since) < idle {
			continue
		}
		if err := c.closer.Close(ctx, ref, c.pane.WorkspaceID, c.pane.TabID); err != nil {
			n.logf("shell tab %s (session %s): not closed: %v", pane, c.pane.Tokens[personaTabToken], err)
			continue
		}
		n.logf("shell tab %s (session %s): its agent exited %s ago, closed", pane, c.pane.Tokens[personaTabToken], now.Sub(look.since).Round(time.Second))
		delete(live, pane)
		closed++
	}
	mem.mu.Lock()
	for pane := range mem.seen {
		if !live[pane] {
			delete(mem.seen, pane)
		}
	}
	mem.mu.Unlock()
	return closed
}

// shellTabClosable: the session a Persona tab was opened for (id, "agent" when the start did not know it yet) may
// lose its empty tab: not being started or resumed right now, not an assistant conversation.
func (n *Node) shellTabClosable(id string) bool {
	if !validID(id) {
		return id == "agent"
	}
	if n.isAssistantSession(id) || n.runtimeAssistant(id) {
		return false
	}
	if r := n.starts.get(id); r != nil && r.ended.IsZero() {
		return false
	}
	return true
}

// allShells: every program in front of a pane is a shell.
func allShells(names []string) bool {
	for _, name := range names {
		if !shellProgram(name) {
			return false
		}
	}
	return true
}
