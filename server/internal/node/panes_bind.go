package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

// sessionFromProcess reads the session a pane's claude runs from ~/.claude/sessions/<pid>.json, which Claude Code
// keeps current (a /clear moves it to a new session id). The pane's pid is cached for a minute; not finding it only
// for a few seconds: right after node starts the claudes' config directories are not all known yet, and a minute of
// "no pid file" let a stale pane binding show another session after every redeploy (2026-10-31).
func (n *Node) sessionFromProcess(pane string) string {
	retry := panePIDRetry
	if n.freshPane(pane) {
		retry = 0 // its claude is about to write the file: look every refresh, so the pane shows within a second of it
	}
	n.mu.Lock()
	c, ok := n.panePIDs[pane]
	n.mu.Unlock()
	if !ok || time.Since(c.checked) > time.Minute || (c.pid == 0 && time.Since(c.checked) > retry) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		pids, err := n.paneProcesses(ctx, pane)
		cancel()
		if err != nil {
			return n.sessionOfPID(pane, c.pid) // the pid found before, as the other hosts' refreshes read it
		}
		c = panePID{checked: time.Now()}
		for _, pid := range pids {
			if _, err := os.Stat(n.claudeSessionFile(pid)); err == nil {
				c.pid = pid
				break
			}
		}
		n.mu.Lock()
		n.panePIDs[pane] = c
		n.mu.Unlock()
	}
	return n.sessionOfPID(pane, c.pid)
}

// sessionFromKnownProcess is sessionFromProcess without asking the pane's host: from the pid an earlier look found,
// however long ago ("" when none was found).
func (n *Node) sessionFromKnownProcess(pane string) string {
	n.mu.Lock()
	c := n.panePIDs[pane]
	n.mu.Unlock()
	return n.sessionOfPID(pane, c.pid)
}

// sessionOfPID is the session the claude with pid runs, from its sessions/<pid>.json; with the file gone the pane's pid
// is forgotten, and the next look at the pane finds it again.
func (n *Node) sessionOfPID(pane string, pid int) string {
	if pid == 0 {
		return ""
	}
	f := n.claudeSessionFile(pid)
	if _, err := os.Stat(f); err != nil {
		n.mu.Lock()
		if c, ok := n.panePIDs[pane]; ok && c.pid == pid { // not a newer pid a concurrent look just found
			delete(n.panePIDs, pane) // claude exited / restarted: look again next time
		}
		n.mu.Unlock()
		return ""
	}
	return readClaudeSessionID(f)
}

// readClaudeSessionID reads the session id out of a claude's sessions/<pid>.json ("" when missing or invalid).
func readClaudeSessionID(path string) string { return nodeclient.ReadClaudeSessionID(path) }

// panePIDRetry: how soon a pane whose claude had no sessions/<pid>.json is looked at again; a fresh pane's
// (freshPane) every refresh while its claude starts.
const panePIDRetry = 5 * time.Second

// freshPaneGrace: how long a claude pane that appeared after node started goes without a guessed session. A claude
// writes its sessions/<pid>.json a few seconds after it starts (four or five on Windows), and until its first prompt it
// has no transcript of its own: a guess then is the newest transcript in the directory, an older session, which the
// pane showed for those seconds (2026-11-04, #1980). Past the grace a claude that never writes the file (older Claude
// Code) is guessed as before.
const freshPaneGrace = 20 * time.Second

// notePanes remembers when each claude pane without a host session id was first seen, and forgets the panes gone. The
// panes of the first call are there since before node started: zero, never fresh. Callers hold n.mu.
func (n *Node) notePanes(unknown []host.Agent) {
	first := n.paneSeen == nil
	if first {
		n.paneSeen = map[string]time.Time{}
	}
	now, here := time.Now(), make(map[string]bool, len(unknown))
	for _, a := range unknown {
		here[a.PaneID] = true
		if _, ok := n.paneSeen[a.PaneID]; !ok && !first {
			n.paneSeen[a.PaneID] = now
		} else if !ok {
			n.paneSeen[a.PaneID] = time.Time{}
		}
	}
	for p := range n.paneSeen {
		if !here[p] {
			delete(n.paneSeen, p)
		}
	}
}

// freshPane: the pane's claude appeared after node started, less than freshPaneGrace ago.
func (n *Node) freshPane(pane string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	t, ok := n.paneSeen[pane]
	return ok && !t.IsZero() && time.Since(t) < freshPaneGrace
}

type panePID struct {
	pid     int
	checked time.Time
}

// bindPane records which session runs in a pane (from start() and from hook events).
func (n *Node) bindPane(pane, id string) {
	if pane == "" || !validID(id) {
		return
	}
	n.mu.Lock()
	n.panes[pane] = id
	n.mu.Unlock()
	n.state.setPane(id, pane)
}

// guessSession picks a transcript for an agent herdr could not identify. Claude sets the terminal title to the
// session's AI title, so a title match wins; otherwise the newest unclaimed transcript in the project directory.
func (n *Node) guessSession(a host.Agent, taken map[string]host.Agent) string {
	if a.Cwd == "" {
		return ""
	}
	files := n.globProjects(nonAlnum.ReplaceAllString(a.Cwd, "-"), "*.jsonl")
	title := strings.TrimSpace(a.Title)
	best, bestT := "", time.Time{}
	for _, p := range files {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if _, used := taken[id]; used || !validID(id) {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if title != "" && n.meta(p, st).title == title {
			return id
		}
		if st.ModTime().After(bestT) {
			best, bestT = id, st.ModTime()
		}
	}
	return best
}
