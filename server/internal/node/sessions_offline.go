package node

import (
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

const offlineWindow = 7 * 24 * time.Hour

var offlineCache struct {
	sync.Mutex
	at   time.Time
	home string // whose scan it is: another node's (tests run many in one process) scans its own
	list []proto.Session
}

// offline lists recent transcripts without a live agent; the directory scan is cached for 20s.
func (n *Node) offline(live map[string]bool) []proto.Session {
	offlineCache.Lock()
	defer offlineCache.Unlock()
	if time.Since(offlineCache.at) > 20*time.Second || offlineCache.home != n.offlineCacheKey() {
		offlineCache.list = n.scanOffline()
		offlineCache.at, offlineCache.home = time.Now(), n.offlineCacheKey()
	}
	var out []proto.Session
	for _, s := range offlineCache.list {
		if !live[s.SessionID] {
			out = append(out, s)
		}
	}
	return out
}

// setKept replaces the sessions that are always listed; the next list has them.
func (n *Node) setKept(ids []string) {
	m := map[string]bool{}
	for _, id := range ids {
		if validID(id) {
			m[id] = true
		}
	}
	n.mu.Lock()
	n.kept = m
	n.mu.Unlock()
	offlineCache.Lock()
	offlineCache.at = time.Time{}
	offlineCache.Unlock()
}

func (n *Node) keptSessions() map[string]bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.kept
}

func (n *Node) scanOffline() []proto.Session {
	var out []proto.Session
	files := n.globProjects("*", "*.jsonl")
	n.maybePruneGoneMetas(files)
	type cand struct {
		path string
		st   os.FileInfo
	}
	var cands, assistant, kept []cand
	keep := n.keptSessions()
	n.mu.Lock()
	forked := maps.Clone(n.forked) // "Branch to New Chat"'s copies (fork.go)
	n.mu.Unlock()
	own := n.assistantProject()
	for _, p := range files {
		id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
		if !validID(id) {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if keep[id] { // the main conversation: listed however long it has been quiet and however busy the machine
			kept = append(kept, cand{p, st})
			continue
		}
		if time.Since(st.ModTime()) > offlineWindow || (st.Size() < 2048 && !forked[id]) {
			continue
		}
		if isAssistantProject(filepath.Base(filepath.Dir(p)), own) {
			assistant = append(assistant, cand{p, st})
			continue
		}
		// Another platform's runs (agentfilters.go) go before the newest are counted: hundreds a day would crowd out
		// the user's own.
		if !forked[id] && n.agentFiltered("claude", p) {
			continue
		}
		cands = append(cands, cand{p, st})
	}
	newest := func(list []cand, limit int) []cand {
		sort.Slice(list, func(i, j int) bool { return list[i].st.ModTime().After(list[j].st.ModTime()) })
		if len(list) > limit {
			list = list[:limit]
		}
		return list
	}
	// The assistant's conversations are counted on their own: a script running `claude -p` hundreds of times a day
	// filled the 80 newest with its sessions, and the "Chat" tab lost every conversation quieter than a couple of hours (#2105).
	cands = append(newest(cands, 80), newest(assistant, maxAssistantOffline)...)
	cands = append(cands, kept...)
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].st.ModTime().After(cands[j].st.ModTime()) })
	listed := map[string]bool{}
	for _, c := range cands {
		id := strings.TrimSuffix(filepath.Base(c.path), ".jsonl")
		if listed[id] {
			continue // an older copy in another config directory (newest first): it used to win, and list the session twice
		}
		listed[id] = true
		m := n.meta(c.path, c.st)
		if !n.sessionScopeAllows(m.cwd) {
			continue
		}
		if m.cwd != "" && n.cwdBlocked(m.cwd) {
			continue
		}
		// Sessions that never produced output (hook probes): small files read whole with no title and no words. A bigger
		// one only had its tail read, and a tail of tool calls under one huge line says nothing; the main conversation is
		// listed whatever its tail holds. Dropping it left the app and the web with no session for the main chat, and its composer
		// disabled (#1163: its last turn stopped mid tool call, under a 377 KB line).
		if m.lastText == "" && m.lastTextAt.IsZero() && m.title == "" && !keep[id] && !forked[id] && c.st.Size() <= quickTail {
			continue
		}
		n.setPath(id, c.path)
		s := proto.Session{
			SessionID: id, Title: m.title, Cwd: m.cwd, Status: proto.StatusOffline, Model: m.model,
			Agent: "claude", AgentSurface: m.surface,
			LastText: snippet(m.lastText), UpdatedAt: lastUsed(m, c.st), Rev: c.st.Size(),
		}
		s.Activity, s.ActivityAt = m.activityOf()
		setContext(&s, m)
		s.ModelFallback = m.modelFallback
		s.PermissionMode, s.Effort = n.currentModes(id, m)
		untitled(&s, m)
		out = append(out, s)
	}
	return out
}

// codexAssistantsAtRest lists the assistant's Codex conversations whose process has been let go (assistantIdleTimeout).
// scanOffline reads only Claude's projects and codexSessions keeps the assistant's out of Agents, so such a conversation
// had no row at all, and every client turned its composer off, with nothing left to send the message that resumes it (#2126).
func (n *Node) codexAssistantsAtRest(seen map[string]bool) []proto.Session {
	if n.state == nil || n.codices == nil {
		return nil
	}
	n.state.mu.Lock()
	cwds := map[string]string{}
	for id, st := range n.state.Sessions {
		if !seen[id] && validID(id) && identity.IsAssistantDir(st.Cwd) {
			cwds[id] = st.Cwd
		}
	}
	n.state.mu.Unlock()
	keep := n.keptSessions()
	type cand struct {
		id, path string
		st       os.FileInfo
	}
	var cands []cand
	for id := range cwds {
		path := n.codexPath(id)
		if path == "" {
			continue
		}
		st, err := os.Stat(path)
		if err != nil || !keep[id] && time.Since(st.ModTime()) > offlineWindow {
			continue
		}
		cands = append(cands, cand{id, path, st})
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].st.ModTime().After(cands[j].st.ModTime()) })
	var out []proto.Session
	quiet := 0
	for _, c := range cands {
		if !keep[c.id] {
			if quiet++; quiet > maxAssistantOffline {
				continue
			}
		}
		m := n.meta(c.path, c.st)
		s := proto.Session{
			SessionID: c.id, Title: m.title, Cwd: cwds[c.id], Status: proto.StatusOffline, Model: m.model, Agent: "codex",
			LastText: snippet(m.lastText), UpdatedAt: lastUsed(m, c.st), Rev: c.st.Size(),
		}
		s.Activity, s.ActivityAt = m.activityOf()
		setContext(&s, m)
		s.ModelFallback = m.modelFallback
		s.PermissionMode, s.Effort = n.currentModes(c.id, m)
		untitled(&s, m)
		n.decorate(&s)
		out = append(out, s)
	}
	return out
}

var nonAlnum = lazyre.New(`[^A-Za-z0-9]`)

// maxAssistantOffline is how many of the assistant directory's quiet transcripts the list keeps, apart from the 80
// newest of every other directory.
const maxAssistantOffline = 100

// assistantProject is the projects directory name Claude Code keeps for this node's assistant directory.
func (n *Node) assistantProject() string {
	return nonAlnum.ReplaceAllString(identity.AssistantDir(n.home), "-")
}

// isAssistantProject: base is the name of the projects directory Claude Code keeps for the assistant's directory
// (own: this node's, assistantProject). The assistant may have run under ~/.persona or the former state directory
// (compat.StateDir), through a symlinked home, so the name is matched by its end as well (#1423: a copy under the
// former name is the assistant's too).
// The health space's directory (docs/assistant/health.md) sits next to it and counts as the assistant's too.
func isAssistantProject(base, own string) bool {
	if base == own || base == strings.TrimSuffix(own, "-assistant")+"-health" {
		return true
	}
	former := "-" + nonAlnum.ReplaceAllString(compat.StateDir, "-") // "." becomes "-" as in the project's name
	for _, suffix := range []string{"--persona-assistant", former + "-assistant", "--persona-health", former + "-health"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}
