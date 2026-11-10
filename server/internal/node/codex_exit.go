package node

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// A Codex that exits in its pane (#2965). Its thread runs on the shared app-server daemon, and the TUI is only one of
// the thread's clients (codex_daemon.go): when the TUI quits, Persona is still subscribed to it, so the daemon never
// unloads it ("no subscribers and idle" never comes), and the list kept showing it as a finished live conversation in
// its workspace instead of not running. Once the pane has had no Codex for this thread for codexExitGrace, the node
// unsubscribes (thread/unsubscribe), forgets a remembered join so no reconnect subscribes it again, and lists the
// thread as not running while it is idle. A new turn on it (a message from the phone) lists it as working again; it
// returns to a pane when the TUI resumes it. The daemon unloads it after its own idle timeout, and it is then ordinary
// history.

// codexExitGrace: a pane must be without this thread's Codex this long before the node lets the thread go, so a
// refresh that briefly misses the pane does not release a running session.
var codexExitGrace = 10 * time.Second

type codexExits struct {
	mu     sync.Mutex
	inPane map[string]time.Time // session id → when a pane last ran its Codex
	exited map[string]bool      // session id → its pane's Codex exited while the thread stayed loaded
}

// codexPaneThreads is the session ids of the live Codex panes (botmux's sessions stay botmux's, as in joinCodexPanes).
func (n *Node) codexPaneThreads() map[string]bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids := map[string]bool{}
	for id, a := range n.agents {
		if a.Agent == "codex" && a.PaneID != "" && host.ParsePane(a.PaneID).Kind != host.Botmux {
			ids[id] = true
		}
	}
	return ids
}

// releaseExitedCodex notes which Codex panes run which threads and lets go of the threads whose pane's Codex exited.
func (n *Node) releaseExitedCodex(ctx context.Context) {
	if n.codices == nil {
		return
	}
	panes := n.codexPaneThreads()
	now := time.Now()
	e := &n.cxExit
	e.mu.Lock()
	if e.inPane == nil {
		e.inPane, e.exited = map[string]time.Time{}, map[string]bool{}
	}
	for id := range panes {
		e.inPane[id] = now
		delete(e.exited, id)
	}
	var gone []string
	for id, at := range e.inPane {
		if !panes[id] && now.Sub(at) >= codexExitGrace {
			delete(e.inPane, id)
			gone = append(gone, id)
		}
	}
	var unloaded []string
	for id := range e.exited {
		unloaded = append(unloaded, id)
	}
	e.mu.Unlock()
	// A thread no app-server holds any more is history: the mark has done its work.
	for _, id := range unloaded {
		if _, _, err := n.codexOwner(id); errors.Is(err, errCodexNoOwner) {
			e.mu.Lock()
			delete(e.exited, id)
			e.mu.Unlock()
		}
	}
	changed := false
	for _, id := range gone {
		if n.releaseCodex(ctx, id) {
			changed = true
		}
	}
	if changed {
		n.notifyCodex()
	}
}

// releaseCodex lets go of thread id after its pane's Codex exited: no remembered join, no subscription. False when
// no app-server holds the thread (it is history already).
func (n *Node) releaseCodex(ctx context.Context, id string) bool {
	n.codices.mu.Lock()
	_, remembered := n.codices.config.Joined[id]
	delete(n.codices.config.Joined, id)
	n.codices.mu.Unlock()
	if remembered {
		n.rememberCodex()
	}
	c, t, err := n.codexOwner(id)
	if err != nil {
		return false
	}
	n.cxExit.mu.Lock()
	n.cxExit.exited[id] = true
	n.cxExit.mu.Unlock()
	if t.joined {
		if err := c.leave(ctx, id); err != nil {
			n.logf("codex %s: its pane's Codex exited; unsubscribing on app-server %s failed: %v", id, c.spec.ID, err)
		} else {
			n.logf("codex %s: its pane's Codex exited; unsubscribed on app-server %s", id, c.spec.ID)
		}
	}
	return true
}

// codexExitedIdle: id's pane's Codex exited and nothing runs on the thread now, so it lists as not running.
func (n *Node) codexExitedIdle(id, status string) bool {
	if status == proto.StatusWorking || status == proto.StatusBlocked {
		return false
	}
	n.cxExit.mu.Lock()
	defer n.cxExit.mu.Unlock()
	return n.cxExit.exited[id]
}

// leave is thread/unsubscribe: this connection stops being a client of the thread, and its cards go (nobody here can
// answer them any more; the daemon keeps the requests for whoever resumes it).
func (c *codexRemote) leave(ctx context.Context, id string) error {
	defer c.joins.lock(id)()
	owner, t, err := c.n.codexOwner(id)
	if err != nil || owner != c || !t.joined {
		return nil
	}
	var reply struct {
		Status string `json:"status"` // notLoaded, notSubscribed or unsubscribed: not subscribed any more, whichever
	}
	if err := c.wire.call(ctx, "thread/unsubscribe", map[string]any{"threadId": t.id}, &reply); err != nil {
		return err
	}
	c.mu.Lock()
	if current, ok := c.threads[t.id]; ok {
		current.joined = false
		current.revision++
		c.threads[t.id] = current
	}
	for key, ask := range c.asks {
		if ask.thread == t.id {
			delete(c.asks, key)
			ask.cancel()
		}
	}
	c.mu.Unlock()
	return nil
}
