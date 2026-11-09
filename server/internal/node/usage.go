package node

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sysmon"
)

// Token usage of every agent on this machine, for the Dynamic Island's TPM / today's tokens / requests (docs/apple/live-activity.md).
// The transcripts are the record: Claude Code writes each reply's usage on its assistant lines (main and subagent
// files), Codex and compatible variants a token_count event per request. The meter reads only what the files gained since the last
// scan, sums it per minute and model for the last usageWindow, and reports changed minutes to the hub as "usage".
// A node starts with the hub connection, so a fresh meter re-reads the window from the files and sends it whole
// ("full"): nothing is stored, and restarting either side recounts the same numbers; the manager keeps the week
// (docs/server/machines.md「Token 用量」).
//
// Each bucket also says which launcher ran it (the session's, as Persona started it; "" for one typed in a terminal),
// and a reply is counted once in the window even when it is in more than one file: a fork (Claude Code's
// --fork-session, Codex's fork) copies the history it starts from, timestamps and all, into the new file.

const (
	usageWindow  = 26 * time.Hour // "today" in any time zone, and the hour before
	usageEvery   = 10 * time.Second
	usageMaxRead = 64 << 20 // per file and scan; a backlog continues on the next scan
	usageMaxLine = 16 << 20 // longer lines (pictures) are skipped: usage lines are small
)

type usageKey struct {
	minute   int64
	model    string
	launcher string
}

type usageFile struct {
	session   string // its session or thread id (usageSession)
	off       int64
	lastID    string // Claude: every content block of a reply repeats its usage
	model     string // Codex: from the latest turn_context
	lastTotal int64  // Codex: the session's running total, to skip repeated token_count events
}

type usageMeter struct {
	files   map[string]*usageFile
	buckets map[usageKey]*proto.UsageBucket
	dirty   map[usageKey]bool
	// counted: every reply counted in the window (Claude's message id, Codex's timestamp and running total), and the
	// minute it is in, so a fork's copy of it in another file is not counted again.
	counted map[string]int64
	// launcherOf names the launcher of a session ("" unknown); nil: none.
	launcherOf func(session string) string
	sent       bool // the first report (full) is out
	sentAt     time.Time
}

func newUsageMeter() *usageMeter {
	return &usageMeter{files: map[string]*usageFile{}, buckets: map[usageKey]*proto.UsageBucket{}, dirty: map[usageKey]bool{},
		counted: map[string]int64{}}
}

// usageFiles lists the transcripts that may hold usage of the window: Claude Code's (every config directory, and
// their subagents), Codex's and the compatible CLI's rollouts.
func (n *Node) usageFiles(cutoff time.Time) []string {
	paths := n.globProjects("*", "*.jsonl")
	paths = append(paths, n.globProjects("*", "*", "subagents", "*.jsonl")...)
	for _, home := range []string{n.codexHome(), n.traexHome()} {
		for _, r := range rollouts(home) {
			paths = append(paths, r.path)
		}
	}
	out := paths[:0]
	for _, p := range paths {
		if st, err := os.Stat(p); err == nil && st.ModTime().After(cutoff) && n.scopedTranscriptAllows(p) {
			out = append(out, p)
		}
	}
	return out
}

// meterUsage scans every usageEvery and reports what changed.
func (n *Node) meterUsage(ctx context.Context, stateLoaded <-chan struct{}) {
	// A first full scan cannot be reattributed after offsets and reply IDs are consumed. Do not replace the hub's
	// window until launcher state has loaded successfully (stateReady); a failed first try leaves this waiting until a
	// later try on this connection loads it (reloadState), or the connection ends.
	select {
	case <-ctx.Done():
		return
	case <-stateLoaded:
	}
	if ctx.Err() != nil {
		return
	}
	m := newUsageMeter()
	m.launcherOf = func(session string) string {
		st, _ := n.state.get(session)
		return st.Launcher
	}
	t := time.NewTicker(usageEvery)
	defer t.Stop()
	for {
		now := time.Now()
		cutoff := now.Add(-usageWindow)
		for _, p := range n.usageFiles(cutoff) {
			if ctx.Err() != nil {
				return
			}
			m.scan(p, cutoff)
		}
		m.prune(cutoff)
		if n.peer != nil {
			if params, ok := m.report(now); ok {
				params["load"], params["cpus"] = loadAverage(), runtime.NumCPU()
				_ = n.peer.Notify("usage", params)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// scan reads what path gained since the last scan (a file seen for the first time: from about the cutoff).
func (m *usageMeter) scan(path string, cutoff time.Time) {
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	uf := m.files[path]
	if uf == nil {
		uf = &usageFile{session: usageSession(path), off: startOffset(path, st.Size(), cutoff)}
		m.files[path] = uf
	}
	if st.Size() < uf.off { // rewritten
		*uf = usageFile{session: uf.session}
	}
	if st.Size() == uf.off {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	end := min(st.Size(), uf.off+usageMaxRead)
	codex := strings.HasPrefix(filepath.Base(path), "rollout-")
	uf.off += eachLine(io.NewSectionReader(f, uf.off, end-uf.off), func(line []byte) {
		if codex {
			m.codexLine(uf, line, cutoff)
		} else {
			m.claudeLine(uf, line, cutoff)
		}
	})
}

// usageSession is the session a transcript belongs to: a Claude Code transcript's name (<id>.jsonl), the session
// folder over a subagent's (<id>/subagents/agent-….jsonl), a Codex rollout's thread id (its name ends with it).
func usageSession(path string) string {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if dir := filepath.Dir(path); filepath.Base(dir) == "subagents" {
		return filepath.Base(filepath.Dir(dir))
	}
	if strings.HasPrefix(name, "rollout-") && len(name) >= 36 {
		return name[len(name)-36:]
	}
	return name
}

// add counts one reply of uf's session; reply identifies it (empty: no way to tell a copy, counted).
func (m *usageMeter) add(uf *usageFile, reply string, ts time.Time, model string, u proto.UsageBucket) {
	if model == "" {
		model = "unknown"
	}
	minute := ts.Unix() / 60 * 60
	if reply != "" {
		if _, ok := m.counted[reply]; ok {
			return
		}
		m.counted[reply] = minute
	}
	launcher := ""
	if m.launcherOf != nil && uf.session != "" {
		launcher = m.launcherOf(uf.session)
	}
	k := usageKey{minute, model, launcher}
	b := m.buckets[k]
	if b == nil {
		b = &proto.UsageBucket{Minute: k.minute, Model: model, Launcher: launcher}
		m.buckets[k] = b
	}
	b.Input += u.Input
	b.CacheWrite += u.CacheWrite
	b.CacheRead += u.CacheRead
	b.Output += u.Output
	b.Requests++
	m.dirty[k] = true
}

func (m *usageMeter) claudeLine(uf *usageFile, line []byte, cutoff time.Time) {
	if !bytes.Contains(line, []byte(`"usage"`)) || !bytes.Contains(line, []byte(`"assistant"`)) {
		return
	}
	var e struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		Message   *struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage *struct {
				InputTokens              int64 `json:"input_tokens"`
				CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
				OutputTokens             int64 `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &e) != nil || e.Type != "assistant" || e.Message == nil || e.Message.Usage == nil {
		return
	}
	if e.Message.ID != "" && e.Message.ID == uf.lastID {
		return
	}
	uf.lastID = e.Message.ID
	if e.Message.Model == "<synthetic>" || e.Timestamp.Before(cutoff) { // <synthetic>: an error Claude Code wrote itself
		return
	}
	u := e.Message.Usage
	reply := ""
	if e.Message.ID != "" {
		reply = "claude:" + e.Message.ID
	}
	m.add(uf, reply, e.Timestamp, e.Message.Model, proto.UsageBucket{Input: u.InputTokens, CacheWrite: u.CacheCreationInputTokens,
		CacheRead: u.CacheReadInputTokens, Output: u.OutputTokens})
}

func (m *usageMeter) codexLine(uf *usageFile, line []byte, cutoff time.Time) {
	if !bytes.Contains(line, []byte(`"token_count"`)) && !bytes.Contains(line, []byte(`"turn_context"`)) {
		return
	}
	type tokens struct {
		Input  int64 `json:"input_tokens"`
		Cached int64 `json:"cached_input_tokens"`
		Output int64 `json:"output_tokens"`
		Total  int64 `json:"total_tokens"`
	}
	var l struct {
		Timestamp time.Time `json:"timestamp"`
		Type      string    `json:"type"`
		Payload   struct {
			Type  string `json:"type"`
			Model string `json:"model"`
			Info  *struct {
				Total tokens `json:"total_token_usage"`
				Last  tokens `json:"last_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &l) != nil {
		return
	}
	if l.Type == "turn_context" {
		if l.Payload.Model != "" {
			uf.model = l.Payload.Model
		}
		return
	}
	// Codex repeats token_count (rate limit updates carry the same totals): only a grown total is a new request.
	if l.Payload.Type != "token_count" || l.Payload.Info == nil || l.Payload.Info.Total.Total <= uf.lastTotal {
		return
	}
	uf.lastTotal = l.Payload.Info.Total.Total
	if l.Timestamp.Before(cutoff) {
		return
	}
	last := l.Payload.Info.Last
	model := uf.model
	if model == "" {
		model = "codex"
	}
	reply := "codex:" + l.Timestamp.UTC().Format(time.RFC3339Nano) + ":" + strconv.FormatInt(l.Payload.Info.Total.Total, 10)
	m.add(uf, reply, l.Timestamp, model, proto.UsageBucket{Input: max(last.Input-last.Cached, 0), CacheRead: last.Cached, Output: last.Output})
}

func (m *usageMeter) prune(cutoff time.Time) {
	for k := range m.buckets {
		if k.minute < cutoff.Unix()/60*60 {
			delete(m.buckets, k)
			delete(m.dirty, k)
		}
	}
	for r, minute := range m.counted {
		if minute < cutoff.Unix()/60*60 {
			delete(m.counted, r)
		}
	}
	for p := range m.files {
		if st, err := os.Stat(p); err != nil || st.ModTime().Before(cutoff) {
			delete(m.files, p)
		}
	}
}

// report returns the "usage" notification: every bucket the first time (full), then the changed ones; nothing when
// nothing changed, except a load update each minute. "launchers": the buckets say their launcher (an older node's
// have none, and the apps cannot filter its usage by launcher).
func (m *usageMeter) report(now time.Time) (map[string]any, bool) {
	full := !m.sent
	if !full && len(m.dirty) == 0 && now.Sub(m.sentAt) < time.Minute {
		return nil, false
	}
	list := []proto.UsageBucket{}
	for k, b := range m.buckets {
		if full || m.dirty[k] {
			list = append(list, *b)
		}
	}
	m.dirty = map[usageKey]bool{}
	m.sent, m.sentAt = true, now
	return map[string]any{"buckets": list, "full": full, "launchers": true}, true
}

// startOffset finds where in a big file the window starts: lines are written in time order, so step back from the
// end until a line is older than the cutoff. Small files are read whole (lines before the cutoff are skipped anyway).
func startOffset(path string, size int64, cutoff time.Time) int64 {
	const small = 4 << 20
	if size <= small {
		return 0
	}
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	buf := make([]byte, 256<<10)
	for step := int64(small); ; step *= 2 {
		pos := size - step
		if pos <= 0 {
			return 0
		}
		nr, _ := f.ReadAt(buf, pos)
		chunk := buf[:nr]
		nl := bytes.IndexByte(chunk, '\n')
		if nl < 0 {
			continue // inside a long line: step further back
		}
		if ts := firstTime(chunk[nl+1:]); !ts.IsZero() && ts.Before(cutoff) {
			return pos + int64(nl) + 1
		}
	}
}

// firstTime is the first "timestamp" value in b (escaped quotes inside messages do not match).
func firstTime(b []byte) time.Time {
	key := []byte(`"timestamp":"`)
	i := bytes.Index(b, key)
	if i < 0 {
		return time.Time{}
	}
	rest := b[i+len(key):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, string(rest[:j]))
	return t
}

// loadAverage is the 1-minute load average (0 when unknown). It uses sysmon's kernel read: the old path ran
// `sysctl -n vm.loadavg` and parsed its text, which under a decimal-comma locale (de_DE, fr_FR) made ParseFloat
// fail and the usage notification report a load of 0.
func loadAverage() float64 { return sysmon.LoadAverage() }
