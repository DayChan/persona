package node

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Workflows: Claude Code's Workflow tool runs a script that drives many agents in the background. The transcript says
// when one was launched and when it ended (transcript.Meta.Workflows); its progress is on disk:
//
//	<session>/subagents/workflows/<run id>/journal.jsonl  {"type":"launched"}, {"type":"started","key","agentId","label","phase"},
//	                                                      {"type":"result","key","agentId","result"} — one line per event;
//	                                                      key names the agent() call: a retry starts it again (wfJournal.line)
//	<session>/subagents/workflows/<run id>/agent-<id>.jsonl / .meta.json  each agent's transcript ({description, workflowPhase})
//	<session>/workflows/<run id>.json                     written when the run ends: {status, error, durationMs, agentCount, …}
//	<session>/workflows/scripts/<name>-<run id>.js        the script: export const meta = {name, phases: [{title}, …]}
//
// docs/protocol/agent-states.md "Background workflows".

// workflowStall: a run that has written nothing for this long, with neither a notification nor a state file, is not
// running any more (its claude went away). Longer than a subagent's 20 minutes: one agent can sit in a long tool call.
const workflowStall = 30 * time.Minute

// workflowKeep: ended runs are listed for this long after they ended.
const workflowKeep = 24 * time.Hour

// Leave one session-list polling interval for publishing a journal update within three seconds.
const workflowReadInterval = 1500 * time.Millisecond

var (
	wfPhasesRe = lazyre.New("(?s)phases:\\s*\\[(.*?)\\]")
	wfTitleRe  = lazyre.New("title:\\s*['\"`]([^'\"`\\n]+)['\"`]")
	wfMetaRe   = lazyre.New("(?s)export\\s+const\\s+meta\\s*=\\s*\\{(.*?)\\n\\}")
	wfMetaName = lazyre.New("(?m)^\\s*name:\\s*['\"`]([^'\"`\\n]+)['\"`]")
)

// wfCache remembers each run's progress by run directory (the least recently asked for forgotten first). A running
// run is read again at most every workflowReadInterval (the session list is rebuilt every second), its journal only for
// the lines it gained; an ended one never again, except once more when its state file shows up after the transcript
// said it ended.
var wfCache = struct {
	sync.Mutex
	m *lru[string, *wfEntry]
}{m: newLRU[string, *wfEntry](wfCacheMax)}

const wfCacheMax = 512

// wfEntry is one run's; its lock keeps two list requests from reading the same run at once.
type wfEntry struct {
	mu        sync.Mutex
	journal   wfJournal
	read      bool
	status    string // the run's status in the transcript when w was read
	readAt    time.Time
	final     bool // ended: w does not change any more
	waitState bool // final, though without the state file: read once more when it appears
	w         proto.Workflow
}

// setBackground fills s's background work from m: the background tasks (background subagents are listed as
// subagents) and the workflow runs. A workflow's task stays in Background only while its run is running, so a
// client that only knows Background still sees it, and one that has ended (or stalled) is not counted.
func (n *Node) setBackground(s *proto.Session, m quickMeta) {
	s.Workflows = n.workflows(s.SessionID, m.wf, m.wfScript)
	running := map[string]bool{}
	for _, w := range s.Workflows {
		running[w.TaskID] = w.Running
	}
	s.Background = nil
	for _, t := range m.bg {
		switch {
		case t.Kind == "agent":
		case t.Kind == "workflow" && !running[t.ID]:
		default:
			s.Background = append(s.Background, t)
		}
	}
}

// workflows is the session's workflow runs of the last day, newest first, with their progress read from disk.
func (n *Node) workflows(parent string, runs []proto.Workflow, scripts map[string]string) []proto.Workflow {
	if len(runs) == 0 {
		return nil
	}
	dir := n.subagentDir(parent)
	if dir == "" {
		return nil
	}
	var out []proto.Workflow
	for i := len(runs) - 1; i >= 0; i-- {
		w := readWorkflow(dir, parent, runs[i], scripts[runs[i].RunID])
		if !w.Running && time.Since(cmpTime(deref(w.EndedAt), w.UpdatedAt, w.StartedAt)) > workflowKeep {
			continue
		}
		out = append(out, w)
	}
	return out
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func cmpTime(ts ...time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// readWorkflow is run as the transcript tells it (launched, maybe ended), with its progress from disk. subagents is
// the session's subagents directory.
func readWorkflow(subagents, parent string, run proto.Workflow, script string) proto.Workflow {
	if run.RunID == "" || strings.ContainsAny(run.RunID, "/\\") || strings.Contains(run.RunID, "..") {
		return run
	}
	runDir := filepath.Join(subagents, "workflows", run.RunID)
	stateFile := filepath.Join(filepath.Dir(subagents), "workflows", run.RunID+".json")
	wfCache.Lock()
	e, ok := wfCache.m.get(runDir)
	if !ok {
		e = &wfEntry{}
		wfCache.m.put(runDir, e)
	}
	wfCache.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.read && e.status == run.Status {
		switch {
		case e.final && !e.waitState, time.Since(e.readAt) < workflowReadInterval:
			return e.w
		case e.final && !fileExists(stateFile):
			e.readAt = time.Now()
			return e.w
		}
	}
	readAt := time.Now()
	w, stated := workflowProgress(&e.journal, runDir, stateFile, parent, run, script)
	e.read, e.status, e.readAt, e.w = true, run.Status, readAt, w
	// Ended: the state file says so, or the transcript (its notification, a TaskStop) without one. A stalled run is
	// not: it may move again.
	e.final = !w.Running && (stated || !run.Running)
	e.waitState = e.final && !stated
	return w
}

type wfJournalLine struct {
	Type    string `json:"type"`
	Key     string `json:"key"`
	AgentID string `json:"agentId"`
	Label   string `json:"label"`
	Phase   string `json:"phase"`
}

// wfJournal is what a run's journal.jsonl said so far: its agents in the order they started, and how each ended. Read
// once, then only for the lines it gained: a result line holds the agent's whole answer.
type wfJournal struct {
	info   os.FileInfo
	size   int64
	mtime  time.Time
	off    int64
	head   []byte // detects a rewrite even if the new file is larger
	agents []wfJournalAgent
	index  map[string]int // by agent id
	calls  map[string]int // by key: the call's latest attempt
}

type wfJournalAgent struct {
	id, label, phase string
	outcome          string // "" while it runs, then wfAgentDone or wfAgentFailed
	// replaced: its call started again later (a retry): the later attempt stands for the call (#2323).
	replaced bool
}

// A workflow agent's statuses (WorkflowAgent.Status).
const (
	wfAgentRunning = "running"
	wfAgentDone    = "done"    // it returned (a result line)
	wfAgentFailed  = "failed"  // the journal says it failed (a failed line: an API error ended it)
	wfAgentStopped = "stopped" // its run ended before it came back
)

// update reads what the journal at path gained since the last read; all of it when it is new or was rewritten.
func (j *wfJournal) update(path string) {
	f, err := os.Open(path)
	if err != nil {
		*j = wfJournal{}
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	if j.info != nil && os.SameFile(j.info, st) && st.Size() == j.size && st.ModTime().Equal(j.mtime) {
		return
	}
	var buf [256]byte
	n, _ := f.ReadAt(buf[:min(st.Size(), int64(len(buf)))], 0)
	head := buf[:n]
	if j.info == nil || !os.SameFile(j.info, st) || st.Size() <= j.size || !bytes.HasPrefix(head, j.head) {
		*j = wfJournal{}
	}
	j.off += eachLine(io.NewSectionReader(f, j.off, st.Size()-j.off), j.line)
	j.head = append(j.head[:0], head[:min(int64(len(head)), j.off)]...)
	j.info, j.size, j.mtime = st, st.Size(), st.ModTime()
}

func (j *wfJournal) line(b []byte) {
	var l wfJournalLine
	if json.Unmarshal(b, &l) != nil || l.AgentID == "" {
		return
	}
	i, ok := j.index[l.AgentID]
	if !ok {
		if j.index == nil {
			j.index = map[string]int{}
		}
		i = len(j.agents)
		j.index[l.AgentID] = i
		j.agents = append(j.agents, wfJournalAgent{id: l.AgentID})
	}
	a := &j.agents[i]
	a.label, a.phase = cmpStr(l.Label, a.label), cmpStr(l.Phase, a.phase)
	switch l.Type {
	case "started":
		// key names the agent() call (two identical calls in one script get two keys). A retry from Claude Code's
		// workflow view starts the same call again under a new agent id, and the attempt it replaced never gets a
		// result or failed line (its transcript just ends "[Request interrupted by user]"): left as it is, it would run
		// for as long as the run does and keep its phase current (#2323). Journals without keys are read as before.
		if l.Key == "" {
			break
		}
		if j.calls == nil {
			j.calls = map[string]int{}
		}
		if prev, ok := j.calls[l.Key]; ok && prev != i {
			j.agents[prev].replaced = true
		}
		j.calls[l.Key] = i
	case "failed":
		a.outcome = wfAgentFailed
	default:
		a.outcome = wfAgentDone // "result", or however else it ended
	}
}

type wfState struct {
	Status     string `json:"status"`
	Error      string `json:"error"`
	StartTime  int64  `json:"startTime"` // Unix ms
	DurationMS int64  `json:"durationMs"`
	Name       string `json:"workflowName"`
	Summary    string `json:"summary"`
	Phases     []struct {
		Title string `json:"title"`
	} `json:"phases"`
}

// workflowProgress reads run's progress, j its journal as read so far; stated when the run's state file was read.
func workflowProgress(j *wfJournal, runDir, stateFile, parent string, run proto.Workflow, script string) (proto.Workflow, bool) {
	w := run
	w.Agents = nil
	w.PhaseStats = nil
	w.AgentsStarted, w.AgentsDone, w.AgentsFailed, w.AgentsRunning = 0, 0, 0, 0
	w.Tokens, w.Calls = 0, 0
	var state *wfState
	if b, err := os.ReadFile(stateFile); err == nil {
		var st wfState
		if json.Unmarshal(b, &st) == nil && st.Status != "" {
			state = &st
		}
	}
	if state != nil {
		w.Name, w.Summary = cmpStr(w.Name, state.Name), cmpStr(w.Summary, state.Summary)
		for _, p := range state.Phases {
			w.Phases = append(w.Phases, p.Title)
		}
		if state.StartTime > 0 && state.DurationMS > 0 && w.EndedAt == nil {
			at := time.UnixMilli(state.StartTime + state.DurationMS)
			w.EndedAt = &at
		}
	}
	if len(w.Phases) == 0 || w.Name == "" {
		name, phases := workflowScript(script)
		w.Name = cmpStr(w.Name, name)
		if len(w.Phases) == 0 {
			w.Phases = phases
		}
	}

	// The journal: which agents started, in which phase, and which returned or failed.
	j.update(filepath.Join(runDir, "journal.jsonl"))
	type agent struct {
		proto.WorkflowAgent
		outcome  string
		lastAt   time.Time
		replaced bool
	}
	agents := make([]*agent, 0, len(j.agents))
	updated := j.mtime
	for _, ja := range j.agents {
		a := &agent{WorkflowAgent: proto.WorkflowAgent{ID: ja.id, SessionID: parent + subagentSep + ja.id, Label: ja.label,
			Phase: ja.phase}, outcome: ja.outcome, replaced: ja.replaced}
		agents = append(agents, a)
		if a.Label == "" || a.Phase == "" {
			if m, ok := subagentMetaCached(filepath.Join(runDir, "agent-"+a.ID+".meta.json")); ok {
				a.Label, a.Phase = cmpStr(a.Label, m.Description), cmpStr(a.Phase, m.Phase)
			}
		}
		jp := filepath.Join(runDir, "agent-"+a.ID+".jsonl")
		if st, err := os.Stat(jp); err == nil {
			a.UpdatedAt = st.ModTime()
			if a.UpdatedAt.After(updated) {
				updated = a.UpdatedAt
			}
			// Read whenever it changed: this read is paced by wfCache, and the one that finalizes the run must see
			// the agent's last lines.
			edges := subagentEdgesCached(jp, st, true)
			a.StartedAt, a.LastText, a.lastAt = edges.started, edges.last, edges.lastAt
			a.Model, a.LatestTool, a.Tokens, a.Calls = edges.model, edges.latestTool, edges.tokens, edges.calls
		}
	}
	w.UpdatedAt = updated

	// Status: the transcript's word (the notification, a TaskStop) first, then the state file, then signs of life.
	switch {
	case !run.Running:
	case state != nil && state.Status != proto.WorkflowRunning:
		w.Status = map[string]string{"failed": proto.WorkflowFailed, "killed": proto.WorkflowKilled,
			"aborted": proto.WorkflowKilled}[state.Status]
		if w.Status == "" {
			w.Status = proto.WorkflowCompleted
		}
	case time.Since(cmpTime(updated, run.StartedAt)) > workflowStall:
		w.Status = proto.WorkflowStalled
	default:
		w.Status = proto.WorkflowRunning
	}
	w.Running = w.Status == proto.WorkflowRunning
	if !w.Running && w.EndedAt == nil {
		at := cmpTime(updated, run.StartedAt)
		w.EndedAt = &at
	}
	if state != nil && w.Status != proto.WorkflowCompleted && state.Error != "" {
		w.Error = snippet(strings.SplitN(strings.TrimSpace(state.Error), "\n", 2)[0])
	}

	var latest, latestRunning string
	phaseIndex := map[string]int{}
	phaseStats := func(title string) *proto.WorkflowPhaseStats {
		i, ok := phaseIndex[title]
		if !ok {
			i = len(w.PhaseStats)
			phaseIndex[title] = i
			w.PhaseStats = append(w.PhaseStats, proto.WorkflowPhaseStats{Title: title})
		}
		return &w.PhaseStats[i]
	}
	for _, title := range w.Phases {
		phaseStats(title)
	}
	listed := make([]*agent, 0, len(agents))
	for _, a := range agents {
		w.Tokens += a.Tokens
		w.Calls += a.Calls
		if a.replaced { // retried: its call is its later attempt's; only what this one spent still counts
			continue
		}
		listed = append(listed, a)
		w.AgentsStarted++
		p := phaseStats(a.Phase)
		p.Started++
		a.Running = w.Running && a.outcome == ""
		switch {
		case a.Running:
			a.Status = wfAgentRunning
			w.AgentsRunning++
			p.Running++
		case a.outcome == wfAgentDone:
			a.Status = wfAgentDone
			w.AgentsDone++
			p.Done++
		case a.outcome == wfAgentFailed:
			a.Status = wfAgentFailed
			w.AgentsFailed++
			p.Failed++
		default:
			a.Status = wfAgentStopped
		}
		if !a.Running {
			at := cmpTime(a.lastAt, a.UpdatedAt, deref(w.EndedAt))
			if a.outcome == "" { // stopped: when its run ended
				at = cmpTime(deref(w.EndedAt), at)
			}
			if !at.IsZero() {
				a.EndedAt = &at
			}
		}
		if a.Phase != "" {
			latest = a.Phase
			if a.Running {
				latestRunning = a.Phase
			}
		}
	}
	w.Phase = cmpStr(latestRunning, latest) // where the latest started agent still at it is, else the latest one
	for i, p := range w.Phases {
		if p == w.Phase {
			w.PhaseIndex = i + 1
		}
	}
	w.PhaseCount = len(w.Phases)
	if len(listed) > proto.WorkflowAgentsMax {
		listed = listed[len(listed)-proto.WorkflowAgentsMax:]
	}
	for _, a := range listed {
		w.Agents = append(w.Agents, a.WorkflowAgent)
	}
	return w, state != nil
}

// workflowScript reads a workflow's name and phase titles from its script's meta block.
func workflowScript(path string) (name string, phases []string) {
	if path == "" || !strings.HasSuffix(path, ".js") {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil
	}
	defer f.Close()
	head := make([]byte, 16<<10) // the meta block comes first
	k, _ := f.Read(head)
	m := wfMetaRe.FindSubmatch(head[:k])
	if m == nil {
		return "", nil
	}
	if nm := wfMetaName.FindSubmatch(m[1]); nm != nil {
		name = string(nm[1])
	}
	if p := wfPhasesRe.FindSubmatch(m[1]); p != nil {
		for _, t := range wfTitleRe.FindAllSubmatch(p[1], -1) {
			phases = append(phases, string(t[1]))
		}
	}
	return name, phases
}

func cmpStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// workflowAgentFile finds agent's transcript among the session's workflow runs (subagents/workflows/<run>/), "" when
// it is not one of theirs.
func workflowAgentFile(subagents, agent string) string {
	if strings.ContainsAny(agent, "*?[]") {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(subagents, "workflows", "wf_*", "agent-"+agent+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}
