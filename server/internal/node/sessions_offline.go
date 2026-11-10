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

// appendOffline appends to dst the recent transcripts without a live agent, straight into the caller's list (sessionList's);
// the directory scan is cached for 20s.
func (n *Node) appendOffline(dst []proto.Session, live map[string]bool) []proto.Session {
	offlineCache.Lock()
	defer offlineCache.Unlock()
	if time.Since(offlineCache.at) > 20*time.Second || offlineCache.home != n.offlineCacheKey() {
		offlineCache.list = n.scanOffline()
		offlineCache.at, offlineCache.home = time.Now(), n.offlineCacheKey()
	}
	for i := range offlineCache.list {
		if !live[offlineCache.list[i].SessionID] {
			dst = append(dst, offlineCache.list[i])
		}
	}
	return dst
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
	// Which count a transcript is listed under: the assistant's conversations are counted on their own (a script running
	// `claude -p` hundreds of times a day filled the 80 newest with its sessions, and the "Chat" tab lost every conversation
	// quieter than a couple of hours, #2105); kept ones are not counted.
	const (
		others = iota
		assistants
		keptOnes
	)
	type cand struct {
		path  string
		st    os.FileInfo
		count int
	}
	var cands []cand
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
			cands = append(cands, cand{p, st, keptOnes})
			continue
		}
		if time.Since(st.ModTime()) > offlineWindow || (st.Size() < 2048 && !forked[id]) {
			continue
		}
		if isAssistantProject(filepath.Base(filepath.Dir(p)), own) {
			cands = append(cands, cand{p, st, assistants})
			continue
		}
		// Another platform's runs (agentfilters.go) go before the newest are counted: hundreds a day would crowd out
		// the user's own.
		if !forked[id] && n.agentFiltered("claude", p) {
			continue
		}
		cands = append(cands, cand{p, st, others})
	}
	// Newest first, and only what is listed counts: a session the filters below leave out (a blocked or out-of-scope
	// directory, a probe that never said anything, an older copy) took one of the 80 and pushed a listed one out.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].st.ModTime().After(cands[j].st.ModTime()) })
	limit := [...]int{others: 80, assistants: maxAssistantOffline, keptOnes: len(cands)}
	var counted [3]int
	listed := map[string]bool{}
	for _, c := range cands {
		id := strings.TrimSuffix(filepath.Base(c.path), ".jsonl")
		if listed[id] {
			continue // an older copy in another config directory (newest first): it used to win, and list the session twice
		}
		listed[id] = true
		if counted[c.count] >= limit[c.count] {
			continue
		}
		m := n.meta(c.path, c.st)
		if isLauncherCheckRun(c.path, m) {
			continue // a launcher health check's run, a hundred a day on each computer: no session of the user's (#2959)
		}
		// Where the session is: an assistant conversation's start directory, before the filters look at it.
		cwd := n.assistantHome(id, m.cwd)
		if !n.sessionScopeAllows(cwd) {
			continue
		}
		if cwd != "" && n.cwdBlocked(cwd) {
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
			SessionID: id, Title: m.title, Cwd: cwd, Status: proto.StatusOffline, Model: m.model,
			Agent: "claude", AgentSurface: m.surface,
			LastText: snippet(m.lastText), UpdatedAt: lastUsed(m, c.st), Rev: c.st.Size(),
		}
		s.Activity, s.ActivityAt = m.activityOf()
		setContext(&s, m)
		s.ModelFallback = m.modelFallback
		s.PermissionMode, s.Effort = n.currentModes(id, m)
		untitled(&s, m)
		out = append(out, s)
		counted[c.count]++
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

// assistantDirOfProject is the directory of this node's assistant (or its health space) whose projects directory
// Claude Code names base, "" for any other. Only this node's own names map back: the projects directory's name is
// lossy ("/", ".", "-" and "_" all become "-"), so the former state directory's or a symlinked home's cannot be told
// apart from another directory's (assistant-bin, another manager's assistant-m-<hex>).
func (n *Node) assistantDirOfProject(base string) string {
	switch own := n.assistantProject(); base {
	case own:
		return identity.AssistantDir(n.home)
	case strings.TrimSuffix(own, "-assistant") + "-health":
		return identity.HealthDir(n.home)
	}
	return ""
}

// assistantHome is the directory session id is listed in at rest: the directory its node record says it started in,
// when that is an assistant or health directory (the unresolved path its running process and resumes use), else cwd
// (what its transcript says: where an assistant conversation started, an agent's latest directory, #2368).
func (n *Node) assistantHome(id, cwd string) string {
	if n.state == nil {
		return cwd
	}
	if st, ok := n.state.get(id); ok && identity.IsAssistantDir(st.Cwd) {
		return st.Cwd
	}
	return cwd
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
	for _, suffix := range assistantProjectSuffixes() {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return false
}

// assistantProjectSuffixes are the ends isAssistantProject matches, built on first use (not at start: every short
// personad command would pay for the regexp) instead of on every call, once per Codex thread and transcript per list.
var assistantProjectSuffixes = sync.OnceValue(func() []string {
	former := "-" + nonAlnum.ReplaceAllString(compat.StateDir, "-") // "." becomes "-" as in the project's name
	return []string{"--persona-assistant", former + "-assistant", "--persona-health", former + "-health"}
})

// isLauncherCheckRun: the transcript at path is a launcher health check's: its directory — a temp directory the check
// made and removed when it ended — no longer exists, and the run started there (the head's start directory, which
// Claude Code names even after `cd`s; the check never runs a Bash that could move). A directory of the user's own with
// a similar name exists, so the name alone is not enough (#2959). The head is the meta the listing has already read.
func isLauncherCheckRun(path string, m quickMeta) bool {
	if !isLauncherCheckProject(filepath.Base(filepath.Dir(path))) {
		return false
	}
	if st, err := os.Stat(m.start); err == nil && st.IsDir() {
		return false // the directory exists: not one of the check's
	}
	// Before a big first read the head is still running: it holds the check's prompt or nothing, never a start
	// directory; decide on what is known, and let the next pass (the read finishes) be exact.
	return m.start == "" || isLauncherCheckDir(m.start)
}
