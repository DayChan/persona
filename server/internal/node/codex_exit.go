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
// marks the thread exited, forgets a remembered join so no reconnect subscribes it again, unsubscribes
// (thread/unsubscribe), and lists it as not running while it is idle.
//
// A message from the phone to an exited thread is still a turn on the daemon: it joins without remembering the join
// (codexJoinRemembers), lists as working while the turn runs, and is let go again once it is idle. The mark goes when
// the thread is back in a pane, or when its app-server, connected, no longer has it loaded (it is ordinary history).

// codexExitGrace: a pane must be without this thread's Codex this long before the node lets the thread go, so a
// refresh that briefly misses the pane does not release a running session.
var codexExitGrace = 10 * time.Second

// codexLeaveTimeout bounds one thread/unsubscribe: an app-server that keeps its socket open but stops answering must
// not hold a release (and the thread's join lock) for ever.
var codexLeaveTimeout = 15 * time.Second

// codexSendHold: after a message or a join from the phone, an exited thread stays subscribed this long even while it
// reads as idle, so its turn has time to show as running before the release pass looks at it.
var codexSendHold = 30 * time.Second

type codexExits struct {
	mu      sync.Mutex
	inPane  map[string]time.Time // session id → when a pane last ran its Codex
	exited  map[string]bool      // session id → its pane's Codex exited while the thread stayed loaded
	held    map[string]time.Time // session id → subscribed by the phone: not released before this
	leaving map[string]bool      // session id → a thread/unsubscribe is running in the background
}

func (e *codexExits) initLocked() {
	if e.inPane == nil {
		e.inPane, e.exited, e.held, e.leaving = map[string]time.Time{}, map[string]bool{}, map[string]time.Time{}, map[string]bool{}
	}
}

// codexPaneThreads is the session ids of the live Codex panes (botmux's sessions stay botmux's, as in joinCodexPanes),
// each true when something names the pane's thread (herdr, a binding still valid, the title) and false when only a
// guess put it there (codexSession).
func (n *Node) codexPaneThreads() map[string]bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	ids := map[string]bool{}
	for id, a := range n.agents {
		if a.Agent == "codex" && a.PaneID != "" && host.ParsePane(a.PaneID).Kind != host.Botmux {
			ids[id] = !n.codexGuessed[id]
		}
	}
	return ids
}

// releaseExitedCodex notes which Codex panes run which threads, marks the threads whose pane's Codex exited, and lets
// go of every marked thread that is subscribed and idle. Unsubscribing runs in the background, bounded by
// codexLeaveTimeout, so a silent app-server never holds up the card loop this runs in.
func (n *Node) releaseExitedCodex(ctx context.Context) {
	if n.codices == nil {
		return
	}
	panes := n.codexPaneThreads()
	now := time.Now()
	e := &n.cxExit
	e.mu.Lock()
	e.initLocked()
	for id, named := range panes {
		// A thread only a guess put in a pane is not followed: the guess may move to another thread of the directory on
		// the next refresh, and the thread it left would read as exited while it runs (review of #2975). One already
		// followed stays followed while any pane has it.
		if _, followed := e.inPane[id]; !named && !followed {
			continue
		}
		e.inPane[id] = now
		delete(e.exited, id)
		delete(e.held, id)
	}
	var gone []string
	for id, at := range e.inPane {
		if _, here := panes[id]; !here && now.Sub(at) >= codexExitGrace {
			delete(e.inPane, id)
			gone = append(gone, id)
		}
	}
	e.mu.Unlock()
	marked := false
	for _, id := range gone {
		if _, _, err := n.codexOwner(id); err == nil {
			e.mu.Lock()
			e.exited[id] = true
			e.mu.Unlock()
			marked = true
		}
	}
	e.mu.Lock()
	var exited []string
	for id := range e.exited {
		exited = append(exited, id)
	}
	e.mu.Unlock()
	for _, id := range exited {
		n.forgetCodexJoin(id)
		c, t, err := n.codexOwner(id)
		if errors.Is(err, errCodexNoOwner) {
			// Not loaded on any connected app-server: history now, unless its app-server is only reconnecting (it
			// may still have the thread, and the mark keeps it listed as not running meanwhile).
			if src, ok := n.codexSource(id); !ok || !n.codexRemoteConnecting(src.home) {
				e.mu.Lock()
				delete(e.exited, id)
				delete(e.held, id)
				e.mu.Unlock()
			}
			continue
		}
		if err != nil || !t.joined || !n.codexReleasable(id, t.status) {
			continue
		}
		e.mu.Lock()
		busy := e.leaving[id]
		e.leaving[id] = true
		e.mu.Unlock()
		if busy {
			continue
		}
		go func() {
			lctx, cancel := context.WithTimeout(ctx, codexLeaveTimeout)
			defer cancel()
			if err := c.leave(lctx, id, n.codexReleasable); err != nil {
				n.logf("codex %s: its pane's Codex exited; unsubscribing on app-server %s failed: %v", id, c.spec.ID, err)
			} else {
				n.logf("codex %s: its pane's Codex exited; let go on app-server %s", id, c.spec.ID)
			}
			e.mu.Lock()
			delete(e.leaving, id)
			e.mu.Unlock()
			n.notifyCodex()
		}()
	}
	if marked {
		n.notifyCodex()
	}
}

// forgetCodexJoin drops a remembered join of id, so no reconnect or restart subscribes it again.
func (n *Node) forgetCodexJoin(id string) {
	n.codices.mu.Lock()
	_, remembered := n.codices.config.Joined[id]
	delete(n.codices.config.Joined, id)
	n.codices.mu.Unlock()
	if remembered {
		n.rememberCodex()
	}
}

// codexReleasable: exited thread id may be let go now: idle, and not just subscribed by the phone.
func (n *Node) codexReleasable(id, status string) bool {
	if status == proto.StatusWorking || status == proto.StatusBlocked {
		return false
	}
	e := &n.cxExit
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.exited[id] && !time.Now().Before(e.held[id])
}

// codexJoinRemembers says whether a join from the phone (a message, Join live) is remembered across reconnects: not
// for an exited thread, whose subscription is the phone's for this turn only; that one is held for codexSendHold.
// Call it before joining, so a release that starts meanwhile sees the hold.
func (n *Node) codexJoinRemembers(id string) bool {
	e := &n.cxExit
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.exited[id] {
		return true
	}
	e.held[id] = time.Now().Add(codexSendHold)
	return false
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

// leave is thread/unsubscribe when still(id, status) holds under the thread's join lock: this connection stops being
// a client of the thread, and its cards go (nobody here can answer them any more; the daemon keeps the requests for
// whoever resumes it). A join waiting for the lock (a message from the phone) subscribes it again afterwards.
func (c *codexRemote) leave(ctx context.Context, id string, still func(id, status string) bool) error {
	defer c.joins.lock(id)()
	owner, t, err := c.n.codexOwner(id)
	if err != nil || owner != c || !t.joined || !still(id, t.status) {
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
