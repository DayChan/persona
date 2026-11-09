package node

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Codex support: herdr reports Codex panes as agent "codex"; their conversations are rollouts under
// ~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<session id>.jsonl (parsed by transcript/codex.go). Compatible Codex
// variants use the same layout in their own data directories. herdr's Codex integration reports the session id when
// Codex starts; a Codex started before that integration has none. Codex puts
// the thread's name in the terminal title ("<name> | <directory>"), which says which thread the pane shows now; the
// newest unclaimed rollout in the pane's directory is only the last resort (2026-10-31: five Codex panes in one
// repository, four of them showed another pane's thread, and the one at work looked idle).
//
// Subagents write rollouts of their own next to the conversation, in the same directory: Codex's thread_spawn helpers
// and the compatible CLI's guardian, which reviews each tool call before it runs. A pane never shows one: they are skipped when
// guessing, and a subagent's id maps to the conversation it belongs to (rolloutMeta).

const codexWindow = 14 * 24 * time.Hour

type codexRollout struct {
	id    string
	path  string
	mtime time.Time
	rolloutMeta
}

// rolloutMeta is what a rollout's first line (session_meta) says about its thread.
type rolloutMeta struct {
	cwd  string
	sub  bool   // a subagent: thread_source "subagent", or a source of {"subagent": …}
	root string // the conversation a subagent belongs to (session_id, else parent_thread_id); "" when not given
}

// rolloutCache is one rollout home's scan (Codex: ~/.codex, a compatible CLI: the CLI-specific data directory — the same layout).
type rolloutCache struct {
	at       time.Time
	rollouts []codexRollout
	older    []rolloutFile // rollouts in day directories before the window and archived ones, listed once a minute
	olderAt  time.Time
	// misses are the ids rolloutPath globbed for and found nowhere, with missStamp then: a thread with no rollout (an
	// app-server thread that never wrote one) was globbed across all of history every second. A miss holds while the
	// recent day directories and archived_sessions are unchanged, and until the older days are listed again.
	misses   map[string][]int64
	metas    map[string]rolloutMeta // path → its session_meta (never changes once written); gone paths leave it once a minute
	titles   map[string]string
	titlesAt time.Time
}

var rolloutCaches = struct {
	sync.Mutex
	byHome map[string]*rolloutCache
}{byHome: map[string]*rolloutCache{}}

var rolloutIDRe = lazyre.New(`([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$`)

// rolloutFile is a rollout's path and the thread id its name ends in.
type rolloutFile struct {
	path string
	id   string
}

// rolloutID is the thread id a rollout's name ends in ("rollout-<time>-<id>.jsonl", the id a UUID), "" when it has none:
// what rolloutIDRe captures, without a regular expression. rollouts reads it for every rollout on disk once a minute,
// where rolloutIDRe took most of each refresh's CPU time.
func rolloutID(path string) string {
	const ext, idLen = ".jsonl", 36
	if len(path) < idLen+len(ext) || !strings.HasSuffix(path, ext) {
		return ""
	}
	id := path[len(path)-len(ext)-idLen : len(path)-len(ext)]
	for i := range idLen {
		c := id[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return ""
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return ""
			}
		}
	}
	return id
}

func (n *Node) codexHome() string {
	if n.sessionScope != nil {
		return n.sessionScope.codex
	}
	return filepath.Join(n.home, ".codex")
}

func (n *Node) codexPath(id string) string {
	if s, ok := n.codexSource(id); ok {
		return rolloutPath(s.home, s.thread)
	}
	return rolloutPath(n.codexHome(), id)
}
func (n *Node) codexTitle(id string) string {
	if s, ok := n.codexSource(id); ok {
		return threadTitle(s.home, s.thread)
	}
	return threadTitle(n.codexHome(), id)
}

func cacheFor(home string) *rolloutCache {
	c := rolloutCaches.byHome[home]
	if c == nil {
		c = &rolloutCache{metas: map[string]rolloutMeta{}}
		rolloutCaches.byHome[home] = c
	}
	return c
}

// rollouts lists a home's rollouts written in the last two weeks, newest first; cached for 5s. A rollout lives in the
// directory of the day its thread started (<home>/sessions/YYYY/MM/DD) and grows there for as long as the thread is
// resumed: one started six weeks earlier and in use every day was not found at all (2026-10-31), so the older days
// are listed too, once a minute, and kept when written lately. Every older rollout is still looked at on each refresh,
// so a thread resumed after weeks shows within 5 seconds.
func rollouts(home string) []codexRollout {
	rolloutCaches.Lock()
	defer rolloutCaches.Unlock()
	c := cacheFor(home)
	now := time.Now()
	if now.Sub(c.at) < 5*time.Second {
		return c.rollouts
	}
	var files []rolloutFile
	recent := map[string]bool{}
	for d := 0; d <= int(codexWindow/(24*time.Hour)); d++ {
		day := now.AddDate(0, 0, -d)
		dir := filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
		recent[dir] = true
		paths, _ := filepath.Glob(filepath.Join(dir, "rollout-*.jsonl"))
		for _, p := range paths {
			if id := rolloutID(p); id != "" {
				files = append(files, rolloutFile{path: p, id: id})
			}
		}
	}
	if now.Sub(c.olderAt) > time.Minute {
		all, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*.jsonl"))
		archived, _ := filepath.Glob(filepath.Join(home, "archived_sessions", "rollout-*.jsonl"))
		all = append(all, archived...)
		c.older = c.older[:0]
		for _, p := range all {
			if id := rolloutID(p); id != "" && !recent[filepath.Dir(p)] {
				c.older = append(c.older, rolloutFile{path: p, id: id})
			}
		}
		c.olderAt = now
		c.misses = nil
		c.pruneMetas(files)
	}
	firstOlder := len(files)
	files = append(files, c.older...)
	var out []codexRollout
	for i, f := range files {
		st, err := os.Stat(f.path)
		if err != nil || (i >= firstOlder && now.Sub(st.ModTime()) > codexWindow) {
			continue
		}
		meta, ok := c.metas[f.path]
		if !ok {
			meta = readRolloutMeta(f.path)
			c.metas[f.path] = meta
		}
		out = append(out, codexRollout{id: f.id, path: f.path, mtime: st.ModTime(), rolloutMeta: meta})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].mtime.After(out[j].mtime) })
	c.rollouts, c.at = out, now
	return out
}

// pruneMetas keeps the session_meta of the rollouts just listed (recent and older), so a deleted or moved rollout
// does not stay for the life of the node. Into a new map: deleting from a map never gives its memory back.
func (c *rolloutCache) pruneMetas(recent []rolloutFile) {
	metas := make(map[string]rolloutMeta, min(len(c.metas), len(recent)+len(c.older)))
	for _, list := range [][]rolloutFile{recent, c.older} {
		for _, f := range list {
			if m, ok := c.metas[f.path]; ok {
				metas[f.path] = m
			}
		}
	}
	c.metas = metas
}

// readRolloutMeta reads a rollout's first line (session_meta, tens of KB of instructions).
func readRolloutMeta(path string) rolloutMeta {
	f, err := os.Open(path)
	if err != nil {
		return rolloutMeta{}
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 256<<10)
	line, _ := r.ReadBytes('\n')
	var l struct {
		Type    string `json:"type"`
		Payload struct {
			ID           string          `json:"id"`
			Cwd          string          `json:"cwd"`
			SessionID    string          `json:"session_id"`
			ParentThread string          `json:"parent_thread_id"`
			ThreadSource string          `json:"thread_source"`
			Source       json.RawMessage `json:"source"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &l) != nil || l.Type != "session_meta" {
		return rolloutMeta{}
	}
	p := l.Payload
	m := rolloutMeta{cwd: p.Cwd}
	var source map[string]json.RawMessage
	m.sub = p.ThreadSource == "subagent" || (json.Unmarshal(p.Source, &source) == nil && source["subagent"] != nil)
	if m.sub {
		for _, id := range []string{p.SessionID, p.ParentThread} {
			if id != "" && id != p.ID {
				m.root = id
				break
			}
		}
	}
	return m
}

// metaOf is a rollout's session_meta, read once (rollouts older than the scan window are read on demand).
func metaOf(home, path string) rolloutMeta {
	rolloutCaches.Lock()
	m, ok := cacheFor(home).metas[path]
	rolloutCaches.Unlock()
	if ok {
		return m
	}
	m = readRolloutMeta(path)
	rolloutCaches.Lock()
	cacheFor(home).metas[path] = m
	rolloutCaches.Unlock()
	return m
}

// mainThread maps a subagent's thread to the conversation it belongs to; any other thread maps to itself.
func mainThread(home, id string) string {
	p := rolloutPath(home, id)
	if p == "" {
		return id
	}
	if m := metaOf(home, p); m.sub && m.root != "" && rolloutPath(home, m.root) != "" {
		return m.root
	}
	return id
}

// rolloutPath finds a session's rollout under a home by id.
func rolloutPath(home, id string) string {
	for _, r := range rollouts(home) {
		if r.id == id && fileExists(r.path) {
			return r.path
		}
	}
	if !validID(id) {
		return ""
	}
	looked := time.Now()
	stamp := missStamp(home)
	rolloutCaches.Lock()
	missed, ok := cacheFor(home).misses[id]
	rolloutCaches.Unlock()
	if ok && slices.Equal(missed, stamp) {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(home, "sessions", "*", "*", "*", "rollout-*-"+id+".jsonl"))
	if len(matches) == 0 {
		matches, _ = filepath.Glob(filepath.Join(home, "archived_sessions", "rollout-*-"+id+".jsonl"))
	}
	if len(matches) == 0 {
		// A directory changed in the last second may change again within its mtime's granularity (a coarse clock
		// tick) without its mtime moving: no miss kept on it.
		if slices.Max(stamp) < looked.Add(-time.Second).UnixNano() {
			rolloutCaches.Lock()
			c := cacheFor(home)
			if c.misses == nil {
				c.misses = map[string][]int64{}
			}
			c.misses[id] = stamp
			rolloutCaches.Unlock()
		}
		return ""
	}
	return newestFirst(matches)[0]
}

// missStamp is when the directories a new rollout appears in were last changed (0: not there): the day directories of
// the window, where a thread's rollout is created, and archived_sessions, where an archived one moves. A file created
// or moved into one changes its mtime.
func missStamp(home string) []int64 {
	now := time.Now()
	days := int(codexWindow / (24 * time.Hour))
	out := make([]int64, 0, days+2)
	for d := 0; d <= days; d++ {
		day := now.AddDate(0, 0, -d)
		out = append(out, dirStamp(filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))))
	}
	return append(out, dirStamp(filepath.Join(home, "archived_sessions")))
}

func dirStamp(dir string) int64 {
	st, err := os.Stat(dir)
	if err != nil {
		return 0
	}
	return st.ModTime().UnixNano()
}

// codexSession picks the rollout of a Codex pane herdr could not identify: the newest conversation in its directory
// not taken (a subagent's rollout there is newer than its conversation's, but never what the pane runs).
func (n *Node) codexSession(a host.Agent, taken map[string]host.Agent) string {
	cwd := a.ForegroundCwd
	if cwd == "" {
		cwd = a.Cwd
	}
	home := n.codexPaneHome(a.PaneID)
	for _, r := range rollouts(home) {
		id := n.codexPublicID(home, r.id)
		if _, used := taken[id]; !used && !r.sub && r.cwd != "" && (r.cwd == cwd || r.cwd == a.Cwd) {
			return id
		}
	}
	return ""
}

// codexByTitle finds the thread a Codex pane shows by the name in its title ("<name> | <directory>"; either part may
// be cut short with "..."). Of several threads with that name it keeps prefer (what herdr or a start said), else takes
// the most recently written one not taken. "" when the title carries no name (a new thread) or no thread has it.
func (n *Node) codexByTitle(a host.Agent, prefer string, taken map[string]host.Agent) string {
	i := strings.LastIndex(a.Title, " | ")
	if i <= 0 {
		return ""
	}
	name := strings.TrimSpace(a.Title[:i])
	cut := strings.TrimSuffix(strings.TrimSuffix(name, "..."), "…")
	home := n.codexPaneHome(a.PaneID)
	var best string
	var bestAt time.Time
	for _, id := range threadsNamed(home, func(t string) bool { return t == name || (cut != name && strings.HasPrefix(t, cut)) }) {
		if _, used := taken[n.codexPublicID(home, id)]; used {
			continue
		}
		p := rolloutPath(home, id)
		if p != "" && metaOf(home, p).sub {
			continue // a pane shows its conversation, never a subagent's thread
		}
		if id == prefer {
			return id
		}
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && (best == "" || st.ModTime().After(bestAt)) {
			best, bestAt = id, st.ModTime()
		}
	}
	return best
}

// threadTitle is the name the agent gave the thread (<home>/session_index.jsonl, last entry wins); re-read on change.
func threadTitle(home, id string) string {
	rolloutCaches.Lock()
	defer rolloutCaches.Unlock()
	return threadTitles(home)[id]
}

// threadsNamed lists the threads whose name passes match.
func threadsNamed(home string, match func(string) bool) []string {
	rolloutCaches.Lock()
	defer rolloutCaches.Unlock()
	var out []string
	for id, t := range threadTitles(home) {
		if match(t) {
			out = append(out, id)
		}
	}
	return out
}

// threadTitles reads a home's session_index.jsonl when it changed; the caller holds rolloutCaches.
func threadTitles(home string) map[string]string {
	path := filepath.Join(home, "session_index.jsonl")
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	c := cacheFor(home)
	if c.titles == nil || st.ModTime().After(c.titlesAt) {
		titles := map[string]string{}
		if f, err := os.Open(path); err == nil {
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 64<<10), 1<<20)
			for sc.Scan() {
				var e struct {
					ID   string `json:"id"`
					Name string `json:"thread_name"`
				}
				if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" && strings.TrimSpace(e.Name) != "" {
					titles[e.ID] = strings.TrimSpace(e.Name)
				}
			}
			f.Close()
		}
		c.titles, c.titlesAt = titles, st.ModTime()
	}
	return c.titles
}
