package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A session with workflow runs on disk the way Claude Code 2.1.308 lays them out (#2935).
func workflowSession(t *testing.T) (*Node, string, string) {
	t.Helper()
	const parent = "aaaaaaaa-0000-0000-0000-000000000002"
	home := t.TempDir()
	tp := filepath.Join(home, ".claude", "projects", "-src-app", parent+".jsonl")
	sess := strings.TrimSuffix(tp, ".jsonl")
	for _, d := range []string{filepath.Join(sess, "subagents", "workflows"), filepath.Join(sess, "workflows", "scripts")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(tp, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Node{home: home, paths: map[string]string{parent: tp}}, parent, sess
}

func writeRun(t *testing.T, sess, run, journal string, agents map[string]string, age time.Duration) {
	t.Helper()
	dir := filepath.Join(sess, "subagents", "workflows", run)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-age)
	files := map[string]string{"journal.jsonl": journal}
	for id, text := range agents {
		files["agent-"+id+".jsonl"] = `{"type":"user","timestamp":"` + at.UTC().Format(time.RFC3339) + `","isSidechain":true}` + "\n" +
			`{"type":"assistant","isSidechain":true,"timestamp":"` + at.UTC().Format(time.RFC3339) + `","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
}

const twoPhaseScript = "export const meta = {\n  name: 'parity-audit',\n  description: 'x',\n" +
	"  phases: [{ title: 'Audit', detail: 'one per domain' }, { title: 'Verify' }],\n}\n\nconst X = 1\n"

func TestWorkflowProgress(t *testing.T) {
	n, parent, sess := workflowSession(t)
	script := filepath.Join(sess, "workflows", "scripts", "parity-audit-wf_r1.js")
	if err := os.WriteFile(script, []byte(twoPhaseScript), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRun(t, sess, "wf_r1", `{"type":"launched"}
{"type":"started","agentId":"a1","label":"audit:chat","phase":"Audit"}
{"type":"started","agentId":"a2","label":"audit:inbox","phase":"Audit"}
{"type":"result","agentId":"a1","result":{"gaps":[]}}
{"type":"started","agentId":"a3","label":"verify:chat#0","phase":"Verify"}
`, map[string]string{"a1": "found 2 gaps", "a2": "reading web/src", "a3": "checking"}, time.Minute)

	started := time.Now().Add(-10 * time.Minute)
	runs := []proto.Workflow{{TaskID: "w1", RunID: "wf_r1", Status: proto.WorkflowRunning, Running: true, StartedAt: started}}
	m := quickMeta{wf: runs, wfScript: map[string]string{"wf_r1": script},
		bg: []proto.BackgroundTask{{ID: "w1", Kind: "workflow", RunID: "wf_r1"}, {ID: "b1", Kind: "shell"}, {ID: "x", Kind: "agent"}}}
	s := proto.Session{SessionID: parent}
	n.setBackground(&s, m)
	if len(s.Workflows) != 1 {
		t.Fatalf("workflows: %+v", s.Workflows)
	}
	w := s.Workflows[0]
	if !w.Running || w.Status != proto.WorkflowRunning || w.Name != "parity-audit" || strings.Join(w.Phases, ",") != "Audit,Verify" ||
		w.Phase != "Verify" || w.PhaseIndex != 2 || w.AgentsStarted != 3 || w.AgentsDone != 1 || w.AgentsRunning != 2 {
		t.Fatalf("progress: %+v", w)
	}
	if w.PhaseCount != 2 || w.Agents[0].Status != "done" || w.Agents[1].Status != "running" {
		t.Fatalf("phase count / agent status: %+v", w)
	}
	if len(w.Agents) != 3 || w.Agents[0].ID != "a1" || w.Agents[0].Running || !w.Agents[1].Running ||
		w.Agents[0].SessionID != parent+"@a1" || w.Agents[0].LastText != "found 2 gaps" || w.Agents[2].Phase != "Verify" {
		t.Fatalf("agents: %+v", w.Agents)
	}
	if len(s.Background) != 2 || s.Background[0].ID != "w1" || s.Background[1].ID != "b1" {
		t.Fatalf("background: %+v", s.Background)
	}
	// A workflow agent reads like a subagent.
	if f, err := n.subagentFile(parent + "@a2"); err != nil || !strings.HasSuffix(f.Path, filepath.Join("wf_r1", "agent-a2.jsonl")) {
		t.Fatalf("agent file: %v %v", f, err)
	}
	if _, err := n.subagentFile(parent + "@a*"); err == nil {
		t.Fatal("a glob in the id found a file")
	}
}

func TestWorkflowEnded(t *testing.T) {
	n, parent, sess := workflowSession(t)
	writeRun(t, sess, "wf_k", `{"type":"started","agentId":"k1","label":"audit:x","phase":"Audit"}`+"\n", map[string]string{"k1": "…"}, time.Minute)
	// An hour ago: ended runs are listed for a day after they end (workflowKeep), so a fixed time stops being one (#5916).
	started := time.Now().Add(-time.Hour).UnixMilli()
	state := `{"runId":"wf_k","taskId":"wk","status":"killed","error":"Error: Workflow aborted\n    at S (x.js:1:2)","startTime":` +
		fmt.Sprint(started) + `,"durationMs":14725,"workflowName":"parity-audit","phases":[{"title":"Audit"},{"title":"Verify"}]}`
	if err := os.WriteFile(filepath.Join(sess, "workflows", "wf_k.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	// The state file says it ended even though the transcript has no notification yet.
	runs := []proto.Workflow{{TaskID: "wk", RunID: "wf_k", Status: proto.WorkflowRunning, Running: true, StartedAt: time.Now().Add(-time.Hour)}}
	s := proto.Session{SessionID: parent}
	n.setBackground(&s, quickMeta{wf: runs, bg: []proto.BackgroundTask{{ID: "wk", Kind: "workflow"}}})
	if len(s.Background) != 0 {
		t.Fatalf("an ended run is still background: %+v", s.Background)
	}
	if len(s.Workflows) != 1 || len(s.Workflows[0].Agents) == 0 {
		t.Fatalf("workflows: %+v", s.Workflows)
	}
	w := s.Workflows[0]
	if w.Running || w.Status != proto.WorkflowKilled || w.Error != "Error: Workflow aborted" || w.Name != "parity-audit" ||
		w.Agents[0].Running || w.Agents[0].Status != "stopped" || w.AgentsRunning != 0 || w.EndedAt == nil {
		t.Fatalf("ended: %+v", w)
	}

	// The transcript's word wins: a notification seen means completed.
	runs[0].Status, runs[0].Running, runs[0].EndedAt = proto.WorkflowCompleted, false, ptrTime(time.Now())
	n.setBackground(&s, quickMeta{wf: runs})
	if w := s.Workflows[0]; w.Running || w.Status != proto.WorkflowCompleted || w.Error != "" {
		t.Fatalf("notified: %+v", w)
	}
}

func TestWorkflowStalled(t *testing.T) {
	n, parent, sess := workflowSession(t)
	writeRun(t, sess, "wf_s", `{"type":"started","agentId":"s1","label":"a","phase":"P"}`+"\n", map[string]string{"s1": "…"}, 2*time.Hour)
	runs := []proto.Workflow{{TaskID: "ws", RunID: "wf_s", Status: proto.WorkflowRunning, Running: true, StartedAt: time.Now().Add(-3 * time.Hour)}}
	s := proto.Session{SessionID: parent}
	n.setBackground(&s, quickMeta{wf: runs, bg: []proto.BackgroundTask{{ID: "ws", Kind: "workflow"}}})
	if w := s.Workflows[0]; w.Running || w.Status != proto.WorkflowStalled || len(s.Background) != 0 {
		t.Fatalf("stalled: %+v %+v", w, s.Background)
	}
	// Ended more than a day ago: not listed.
	runs[0].Status, runs[0].Running, runs[0].EndedAt = proto.WorkflowCompleted, false, ptrTime(time.Now().Add(-25*time.Hour))
	writeRun(t, sess, "wf_s", "", nil, 25*time.Hour)
	n.setBackground(&s, quickMeta{wf: runs})
	if len(s.Workflows) != 0 {
		t.Fatalf("old run listed: %+v", s.Workflows)
	}
}

// The transcript's launch line reaches the session's background through the background scan (the main turn has ended:
// the session would be "Unread" without it).
func TestWorkflowFromTranscript(t *testing.T) {
	n, parent, sess := workflowSession(t)
	n.bgScans, n.metas = map[string]*bgScan{}, map[string]quickMeta{}
	writeRun(t, sess, "wf_t", `{"type":"started","agentId":"t1","label":"a","phase":"P"}`+"\n", map[string]string{"t1": "…"}, time.Second)
	tp := n.paths[parent]
	launch := `"Workflow launched in background. Task ID: wt1\nSummary: s\nRun ID: wf_t\n"`
	body := `{"type":"assistant","uuid":"a1","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Workflow","input":{"name":"saved-one"}}]}}` + "\n" +
		`{"type":"user","uuid":"u1","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":` + launch + `}]}}` + "\n" +
		`{"type":"system","subtype":"turn_duration","durationMs":1000,"pendingWorkflowCount":1,"timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `"}` + "\n"
	if err := os.WriteFile(tp, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(tp)
	m := n.meta(tp, st)
	s := proto.Session{SessionID: parent}
	n.setBackground(&s, m)
	if len(s.Background) != 1 || s.Background[0].Kind != "workflow" || len(s.Workflows) != 1 || !s.Workflows[0].Running ||
		s.Workflows[0].Name != "saved-one" || s.Workflows[0].AgentsRunning != 1 {
		t.Fatalf("background %+v workflows %+v", s.Background, s.Workflows)
	}
}

func ptrTime(t time.Time) *time.Time { return &t }

// expireWorkflow makes the next readWorkflow of the run in dir read it again, as if its last read were long ago.
func expireWorkflow(dir string) {
	wfCache.Lock()
	e, _ := wfCache.m.peek(dir)
	wfCache.Unlock()
	if e != nil {
		e.mu.Lock()
		e.readAt = time.Time{}
		e.mu.Unlock()
	}
}

func TestWorkflowPhaseStats(t *testing.T) {
	for _, tt := range []struct {
		name, status string
		phases       []string
		want         []proto.WorkflowPhaseStats
	}{
		{"declared and unknown phases", proto.WorkflowRunning, []string{"Audit", "Verify", "Later"}, []proto.WorkflowPhaseStats{
			{Title: "Audit", Started: 1, Done: 1}, {Title: "Verify", Started: 1, Running: 1}, {Title: "Later"},
			{Title: "Extra", Started: 1, Done: 1}, {Title: "", Started: 1, Running: 1},
		}},
		{"stopped agents are neither done nor running", proto.WorkflowKilled, []string{"Audit", "Verify"}, []proto.WorkflowPhaseStats{
			{Title: "Audit", Started: 1, Done: 1}, {Title: "Verify", Started: 1},
			{Title: "Extra", Started: 1, Done: 1}, {Title: "", Started: 1},
		}},
		{"undeclared phases keep start order", proto.WorkflowRunning, nil, []proto.WorkflowPhaseStats{
			{Title: "Audit", Started: 1, Done: 1}, {Title: "Verify", Started: 1, Running: 1},
			{Title: "Extra", Started: 1, Done: 1}, {Title: "", Started: 1, Running: 1},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, parent, sess := workflowSession(t)
			writeRun(t, sess, "wf_stats", `{"type":"started","agentId":"a","phase":"Audit"}
{"type":"started","agentId":"b","phase":"Verify"}
{"type":"started","agentId":"c","phase":"Extra"}
{"type":"started","agentId":"d"}
{"type":"result","agentId":"a"}
{"type":"result","agentId":"c"}
`, map[string]string{"a": "done", "b": "working", "c": "done", "d": "working"}, time.Minute)
			ended := time.Now()
			run := proto.Workflow{TaskID: "task", RunID: "wf_stats", Status: tt.status, Running: tt.status == proto.WorkflowRunning,
				Phases: tt.phases, StartedAt: ended.Add(-time.Hour)}
			if !run.Running {
				run.EndedAt = &ended
			}
			w, _ := workflowProgress(&wfJournal{}, filepath.Join(sess, "subagents", "workflows", "wf_stats"), "", parent, run, "")
			if !reflect.DeepEqual(w.PhaseStats, tt.want) {
				t.Fatalf("phase_stats = %+v, want %+v", w.PhaseStats, tt.want)
			}
			if w.AgentsStarted != 4 || w.AgentsDone != 2 || w.AgentsRunning != map[bool]int{true: 2, false: 0}[run.Running] {
				t.Fatalf("run counters = %+v", w)
			}
			for _, a := range w.Agents {
				if a.Running != (a.EndedAt == nil) {
					t.Fatalf("running/ended_at mismatch: %+v", a)
				}
				if a.Status == "stopped" && !a.EndedAt.Equal(ended) {
					t.Fatalf("stopped agent ended_at = %v, want run end %v", a.EndedAt, ended)
				}
			}
			wire, err := json.Marshal(w.PhaseStats)
			if err != nil || !strings.Contains(string(wire), `"title":""`) {
				t.Fatalf("unknown phase title must be sent: %s (%v)", wire, err)
			}
		})
	}
}

func TestWorkflowTotalsBeforeAgentLimit(t *testing.T) {
	_, parent, sess := workflowSession(t)
	var journal strings.Builder
	const count = proto.WorkflowAgentsMax + 5
	for i := range count {
		fmt.Fprintf(&journal, "{\"type\":\"started\",\"agentId\":\"a%d\",\"phase\":\"Audit\"}\n", i)
		if i < 10 {
			fmt.Fprintf(&journal, "{\"type\":\"result\",\"agentId\":\"a%d\"}\n", i)
		}
	}
	writeRun(t, sess, "wf_many", journal.String(), nil, time.Minute)
	dir := filepath.Join(sess, "subagents", "workflows", "wf_many")
	for i := range count {
		body := edgeReply(fmt.Sprint(i), "2026-01-01T00:00:00Z", "model-a", `[{"type":"tool_use","id":"tool","name":"Read","input":{"file_path":"/work/project/example.go"}}]`,
			`{"input_tokens":1,"cache_read_input_tokens":2,"cache_creation_input_tokens":3,"output_tokens":4}`)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("agent-a%d.jsonl", i)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := proto.Workflow{TaskID: "task", RunID: "wf_many", Status: proto.WorkflowRunning, Running: true, StartedAt: time.Now().Add(-time.Hour)}
	w, _ := workflowProgress(&wfJournal{}, dir, "", parent, run, "")
	if len(w.Agents) != proto.WorkflowAgentsMax || w.Agents[0].ID != "a5" || w.Tokens != count*10 || w.Calls != count ||
		w.AgentsStarted != count || w.AgentsDone != 10 || w.AgentsRunning != count-10 {
		t.Fatalf("totals before limit = %+v", w)
	}
	if want := []proto.WorkflowPhaseStats{{Title: "Audit", Started: count, Done: 10, Running: count - 10}}; !reflect.DeepEqual(w.PhaseStats, want) {
		t.Fatalf("phase_stats = %+v, want %+v", w.PhaseStats, want)
	}
	for _, a := range w.Agents {
		if a.Model != "model-a" || a.Tokens != 10 || a.Calls != 1 || a.LatestTool == nil || a.LatestTool.Name != "Read" || a.LatestTool.Summary != "在读 example.go" {
			t.Fatalf("agent details = %+v", a)
		}
	}
}

func TestWorkflowFinalRefreshesAgentDetails(t *testing.T) {
	for _, completedAgent := range []bool{true, false} {
		t.Run(fmt.Sprintf("agent done %v", completedAgent), func(t *testing.T) {
			_, parent, sess := workflowSession(t)
			writeRun(t, sess, "wf_final", `{"type":"started","agentId":"a","phase":"Audit"}`+"\n", nil, time.Minute)
			dir := filepath.Join(sess, "subagents", "workflows", "wf_final")
			path := filepath.Join(dir, "agent-a.jsonl")
			first := edgeReply("first", "2026-01-01T00:00:00Z", "model-a", `[{"type":"tool_use","id":"one","name":"Read","input":{}}]`, `{"input_tokens":10}`)
			if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
				t.Fatal(err)
			}
			run := proto.Workflow{TaskID: "task", RunID: "wf_final", Status: proto.WorkflowRunning, Running: true, StartedAt: time.Now().Add(-time.Hour)}
			subagents := filepath.Join(sess, "subagents")
			if w := readWorkflow(subagents, parent, run, ""); w.Tokens != 10 || w.Calls != 1 {
				t.Fatalf("initial details = %+v", w)
			}
			last := edgeReply("last", "2026-01-01T00:01:00Z", "model-b", `[{"type":"tool_use","id":"two","name":"Bash","input":{"command":"go test ./..."}},{"type":"text","text":"Finished"}]`, `{"input_tokens":20}`)
			if err := os.WriteFile(path, []byte(first+last), 0o600); err != nil {
				t.Fatal(err)
			}
			if completedAgent {
				journal := `{"type":"started","agentId":"a","phase":"Audit"}` + "\n" + `{"type":"result","agentId":"a"}` + "\n"
				if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(journal), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(sess, "workflows", "wf_final.json"), []byte(`{"status":"completed"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			expireWorkflow(dir)
			w := readWorkflow(subagents, parent, run, "")
			if w.Running || w.Tokens != 20 || w.Calls != 2 || w.Agents[0].Model != "model-b" || w.Agents[0].EndedAt == nil ||
				w.Agents[0].LastText != "Finished" || w.Agents[0].LatestTool.Name != "Bash" {
				t.Fatalf("final details froze before final append: %+v, agent %+v", w, w.Agents[0])
			}
			if completedAgent && w.Agents[0].EndedAt.Format(time.RFC3339) != "2026-01-01T00:01:00Z" {
				t.Fatalf("done agent end = %v", w.Agents[0].EndedAt)
			}
			if again := readWorkflow(subagents, parent, run, ""); !reflect.DeepEqual(again, w) {
				t.Fatalf("final cache changed: %+v -> %+v", w, again)
			}
		})
	}
}

// The journal's failed lines (an API error ended the agent) are failures, not results (#5511): counted apart in the
// run and its phase, the agent's status says so, and its usage before the error still counts. An agent the journal
// names without a label or phase has them from its meta.
func TestWorkflowFailedAgents(t *testing.T) {
	_, parent, sess := workflowSession(t)
	writeRun(t, sess, "wf_fail", `{"type":"launched"}
{"type":"started","key":"v2:a","agentId":"a","label":"audit:a","phase":"Audit"}
{"type":"started","key":"v2:b","agentId":"b","label":"audit:b","phase":"Audit"}
{"type":"started","key":"v2:c","agentId":"c"}
{"type":"result","key":"v2:a","agentId":"a","result":{"ok":true},"toolsUsed":["Read"]}
{"type":"failed","key":"v2:b","agentId":"b"}
`, nil, time.Minute)
	dir := filepath.Join(sess, "subagents", "workflows", "wf_fail")
	const usage = `{"input_tokens":1,"cache_read_input_tokens":2,"cache_creation_input_tokens":3,"output_tokens":4}`
	files := map[string]string{
		"agent-a.jsonl": edgeReply("a1", "2026-01-01T00:00:00Z", "model-a", `[{"type":"text","text":"done"}]`, usage),
		"agent-b.jsonl": edgeReply("b1", "2026-01-01T00:00:00Z", "model-a", `[{"type":"tool_use","id":"tb","name":"Read","input":{}}]`, usage) +
			apiErrorReply("b2", "2026-01-01T00:02:00Z", "API Error: 500 Server error mid-response"),
		"agent-c.jsonl":     edgeReply("c1", "2026-01-01T00:00:00Z", "model-a", `[{"type":"text","text":"working"}]`, usage),
		"agent-c.meta.json": `{"agentType":"workflow-subagent","description":"verify:c","workflowPhase":"Verify"}`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := proto.Workflow{TaskID: "task", RunID: "wf_fail", Status: proto.WorkflowRunning, Running: true, Phases: []string{"Audit", "Verify"},
		StartedAt: time.Now().Add(-time.Hour)}
	w, _ := workflowProgress(&wfJournal{}, dir, "", parent, run, "")
	if w.AgentsStarted != 3 || w.AgentsDone != 1 || w.AgentsFailed != 1 || w.AgentsRunning != 1 || w.Tokens != 30 {
		t.Fatalf("run counters = %+v", w)
	}
	want := []proto.WorkflowPhaseStats{{Title: "Audit", Started: 2, Done: 1, Failed: 1}, {Title: "Verify", Started: 1, Running: 1}}
	if !reflect.DeepEqual(w.PhaseStats, want) {
		t.Fatalf("phase_stats = %+v, want %+v", w.PhaseStats, want)
	}
	byID := map[string]proto.WorkflowAgent{}
	for _, a := range w.Agents {
		byID[a.ID] = a
	}
	b, c := byID["b"], byID["c"]
	if byID["a"].Status != "done" || b.Status != "failed" || b.Running || b.Tokens != 10 || b.Calls != 1 ||
		b.EndedAt == nil || b.EndedAt.Format(time.RFC3339) != "2026-01-01T00:02:00Z" || b.LastText != "API Error: 500 Server error mid-response" {
		t.Fatalf("failed agent = %+v", b)
	}
	if c.Status != "running" || c.Label != "verify:c" || c.Phase != "Verify" {
		t.Fatalf("agent from its meta = %+v", c)
	}
	wire, _ := json.Marshal(w)
	if !strings.Contains(string(wire), `"agents_failed":1`) || !strings.Contains(string(wire), `{"title":"Audit","started":2,"done":1,"failed":1,"running":0}`) {
		t.Fatalf("wire: %s", wire)
	}

	// The run ends: the one still at it stopped, the failed one is still a failure.
	ended := time.Now()
	run.Status, run.Running, run.EndedAt = proto.WorkflowKilled, false, &ended
	w, _ = workflowProgress(&wfJournal{}, dir, "", parent, run, "")
	if w.AgentsDone != 1 || w.AgentsFailed != 1 || w.AgentsRunning != 0 {
		t.Fatalf("ended run counters = %+v", w)
	}
	for _, a := range w.Agents {
		if want := map[string]string{"a": "done", "b": "failed", "c": "stopped"}[a.ID]; a.Status != want {
			t.Fatalf("%s: status %q, want %q", a.ID, a.Status, want)
		}
	}
	// None failed: the fields are left out, as older nodes send it.
	_, _, sess2 := workflowSession(t)
	writeRun(t, sess2, "wf_ok", `{"type":"started","agentId":"a","phase":"Audit"}`+"\n"+`{"type":"result","agentId":"a"}`+"\n",
		map[string]string{"a": "done"}, time.Minute)
	ok, _ := workflowProgress(&wfJournal{}, filepath.Join(sess2, "subagents", "workflows", "wf_ok"), "", parent, run, "")
	if wire, _ := json.Marshal(ok); strings.Contains(string(wire), "failed") {
		t.Fatalf("no failures, yet: %s", wire)
	}
}

// How an agent fails in practice: the run was read while the agent ran, then its API error and the journal's failed
// line come and are read for what they added (#5511). The agent's usage before the error still counts in the run.
func TestWorkflowAgentFailsWhileRead(t *testing.T) {
	_, parent, sess := workflowSession(t)
	journal := `{"type":"started","key":"v2:a","agentId":"a","label":"audit:a","phase":"Audit"}
{"type":"started","key":"v2:b","agentId":"b","label":"audit:b","phase":"Audit"}
{"type":"started","key":"v2:c","agentId":"c","label":"verify:c","phase":"Verify"}
{"type":"result","key":"v2:a","agentId":"a","result":{"ok":true}}
`
	writeRun(t, sess, "wf_fails", journal, nil, 0)
	dir := filepath.Join(sess, "subagents", "workflows", "wf_fails")
	const usage = `{"input_tokens":1,"cache_read_input_tokens":2,"cache_creation_input_tokens":3,"output_tokens":4}`
	for id, body := range map[string]string{
		"a": edgeReply("a1", "2026-01-01T00:00:00Z", "model-a", `[{"type":"text","text":"done"}]`, usage),
		"b": edgeReply("b1", "2026-01-01T00:00:00Z", "model-a", `[{"type":"tool_use","id":"tb","name":"Read","input":{}}]`, usage),
		"c": edgeReply("c1", "2026-01-01T00:00:00Z", "model-a", `[{"type":"text","text":"working"}]`, usage),
	} {
		if err := os.WriteFile(filepath.Join(dir, "agent-"+id+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := proto.Workflow{TaskID: "task", RunID: "wf_fails", Status: proto.WorkflowRunning, Running: true, Phases: []string{"Audit", "Verify"},
		StartedAt: time.Now().Add(-time.Hour)}
	subagents := filepath.Join(sess, "subagents")
	agent := func(w proto.Workflow, id string) proto.WorkflowAgent {
		for _, a := range w.Agents {
			if a.ID == id {
				return a
			}
		}
		t.Fatalf("no agent %s in %+v", id, w.Agents)
		return proto.WorkflowAgent{}
	}
	w := readWorkflow(subagents, parent, run, "")
	if b := agent(w, "b"); w.AgentsRunning != 2 || w.AgentsFailed != 0 || w.Tokens != 30 || b.Status != "running" || b.Tokens != 10 {
		t.Fatalf("while b runs: %+v, b %+v", w, b)
	}

	const apiError = "API Error: 500 Server error mid-response"
	for name, line := range map[string]string{
		"agent-b.jsonl": apiErrorReply("b2", "2026-01-01T00:02:00Z", apiError),
		"journal.jsonl": `{"type":"failed","key":"v2:b","agentId":"b"}` + "\n",
	} {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString(line)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	expireWorkflow(dir)
	w = readWorkflow(subagents, parent, run, "")
	b := agent(w, "b")
	if w.AgentsDone != 1 || w.AgentsFailed != 1 || w.AgentsRunning != 1 || w.Tokens != 30 || w.Calls != 1 {
		t.Fatalf("after b failed: %+v", w)
	}
	if b.Status != "failed" || b.Running || b.Tokens != 10 || b.Model != "model-a" || b.Calls != 1 || b.LastText != apiError ||
		b.EndedAt == nil || b.EndedAt.Format(time.RFC3339) != "2026-01-01T00:02:00Z" {
		t.Fatalf("failed agent = %+v", b)
	}
	want := []proto.WorkflowPhaseStats{{Title: "Audit", Started: 2, Done: 1, Failed: 1}, {Title: "Verify", Started: 1, Running: 1}}
	if !reflect.DeepEqual(w.PhaseStats, want) {
		t.Fatalf("phase_stats = %+v, want %+v", w.PhaseStats, want)
	}
}

// interruptedLine is how a retried workflow agent's transcript ends (Claude Code 2.1.3xx, scrubbed): the user line the
// retry writes into the attempt it replaces. The journal says nothing about that attempt again.
func interruptedLine(agent, at string) string {
	return `{"parentUuid":"p-` + agent + `","isSidechain":true,"promptId":"prompt-example","agentId":"` + agent + `","type":"user",` +
		`"message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]},"uuid":"u-` + agent +
		`","timestamp":"` + at + `","userType":"external","entrypoint":"cli"}` + "\n"
}

// A stalled agent retried from Claude Code's workflow view (#2323): the same call (key) starts again under a new agent
// id, and the attempt it replaced never gets a result. The call counts once, as its latest attempt; the phase is not
// left running with one agent too many (it showed 2/3 and stayed current). What the replaced attempt spent still counts.
func TestWorkflowRetriedAgent(t *testing.T) {
	_, parent, sess := workflowSession(t)
	// The journal of the real run, scrubbed: probe a stalls, probe b returns, a is retried as a2, which returns; the
	// next phase starts.
	journal := `{"type":"launched"}
{"type":"started","key":"v2:probe-a","agentId":"a","label":"probe:model-a","phase":"Probe"}
{"type":"started","key":"v2:probe-b","agentId":"b","label":"probe:model-b","phase":"Probe"}
{"type":"result","key":"v2:probe-b","agentId":"b","result":"OK","remoteControlDeviceReport":false}
{"type":"started","key":"v2:probe-a","agentId":"a2","label":"probe:model-a","phase":"Probe"}
{"type":"result","key":"v2:probe-a","agentId":"a2","result":"OK","remoteControlDeviceReport":false}
{"type":"started","key":"v2:look","agentId":"c","label":"investigate:server","phase":"Investigate"}
`
	writeRun(t, sess, "wf_retry", journal, nil, 0)
	dir := filepath.Join(sess, "subagents", "workflows", "wf_retry")
	const usage = `{"input_tokens":1,"cache_read_input_tokens":2,"cache_creation_input_tokens":3,"output_tokens":4}`
	files := map[string]string{
		"agent-a.jsonl": edgeReply("a1", "2026-01-01T00:00:00Z", "model-a", `[{"type":"tool_use","id":"ta","name":"Read","input":{}}]`, usage) +
			interruptedLine("a", "2026-01-01T00:09:00Z"),
		"agent-b.jsonl":  edgeReply("b1", "2026-01-01T00:00:00Z", "model-b", `[{"type":"text","text":"OK"}]`, usage),
		"agent-a2.jsonl": edgeReply("a21", "2026-01-01T00:09:01Z", "model-a", `[{"type":"text","text":"OK"}]`, usage),
		"agent-c.jsonl":  edgeReply("c1", "2026-01-01T00:10:00Z", "model-b", `[{"type":"text","text":"reading"}]`, usage),
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := proto.Workflow{TaskID: "task", RunID: "wf_retry", Status: proto.WorkflowRunning, Running: true,
		Phases: []string{"Probe", "Investigate"}, StartedAt: time.Now().Add(-time.Hour)}
	w, _ := workflowProgress(&wfJournal{}, dir, "", parent, run, "")
	if w.AgentsStarted != 3 || w.AgentsDone != 2 || w.AgentsRunning != 1 || w.Phase != "Investigate" || w.PhaseIndex != 2 {
		t.Fatalf("run counters = %+v", w)
	}
	// The replaced attempt's usage and its tool call were spent: they stay in the run's totals.
	if w.Tokens != 40 || w.Calls != 1 {
		t.Fatalf("totals = tokens %d, calls %d", w.Tokens, w.Calls)
	}
	want := []proto.WorkflowPhaseStats{{Title: "Probe", Started: 2, Done: 2}, {Title: "Investigate", Started: 1, Running: 1}}
	if !reflect.DeepEqual(w.PhaseStats, want) {
		t.Fatalf("phase_stats = %+v, want %+v", w.PhaseStats, want)
	}
	var ids []string
	for _, a := range w.Agents {
		ids = append(ids, a.ID+"="+a.Status)
	}
	if got := strings.Join(ids, ","); got != "b=done,a2=done,c=running" {
		t.Fatalf("agents = %s", got)
	}

	// Ended: the replaced attempt is not a stopped agent either.
	ended := time.Now()
	run.Status, run.Running, run.EndedAt = proto.WorkflowCompleted, false, &ended
	w, _ = workflowProgress(&wfJournal{}, dir, "", parent, run, "")
	if w.AgentsStarted != 3 || w.AgentsDone != 2 || len(w.Agents) != 3 || w.PhaseStats[0] != (proto.WorkflowPhaseStats{Title: "Probe", Started: 2, Done: 2}) {
		t.Fatalf("ended run = %+v", w)
	}
}

// The retry is read the way it comes in practice: the run is read while the stalled attempt runs, then the retry's
// started line, then its result, each read for what the journal gained. A rewritten journal forgets the calls with
// the agents.
func TestWorkflowRetriedAgentWhileRead(t *testing.T) {
	_, parent, sess := workflowSession(t)
	writeRun(t, sess, "wf_retry_read", `{"type":"started","key":"v2:probe-a","agentId":"a","label":"probe:model-a","phase":"Probe"}
{"type":"started","key":"v2:probe-b","agentId":"b","label":"probe:model-b","phase":"Probe"}
{"type":"result","key":"v2:probe-b","agentId":"b","result":"OK"}
`, map[string]string{"a": "…", "b": "OK", "a2": "OK"}, 0)
	dir := filepath.Join(sess, "subagents", "workflows", "wf_retry_read")
	subagents := filepath.Join(sess, "subagents")
	run := proto.Workflow{TaskID: "task", RunID: "wf_retry_read", Status: proto.WorkflowRunning, Running: true,
		Phases: []string{"Probe"}, StartedAt: time.Now().Add(-time.Hour)}
	probe := func(w proto.Workflow) proto.WorkflowPhaseStats { return w.PhaseStats[0] }
	appendJournal := func(line string) {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(dir, "journal.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString(line)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		expireWorkflow(dir)
	}

	if w := readWorkflow(subagents, parent, run, ""); probe(w) != (proto.WorkflowPhaseStats{Title: "Probe", Started: 2, Done: 1, Running: 1}) {
		t.Fatalf("while a stalls: %+v", w.PhaseStats)
	}
	appendJournal(`{"type":"started","key":"v2:probe-a","agentId":"a2","label":"probe:model-a","phase":"Probe"}` + "\n")
	w := readWorkflow(subagents, parent, run, "")
	if probe(w) != (proto.WorkflowPhaseStats{Title: "Probe", Started: 2, Done: 1, Running: 1}) || w.AgentsStarted != 2 ||
		len(w.Agents) != 2 || w.Agents[1].ID != "a2" || !w.Agents[1].Running {
		t.Fatalf("while the retry runs: %+v", w)
	}
	appendJournal(`{"type":"result","key":"v2:probe-a","agentId":"a2","result":"OK"}` + "\n")
	w = readWorkflow(subagents, parent, run, "")
	if probe(w) != (proto.WorkflowPhaseStats{Title: "Probe", Started: 2, Done: 2}) || w.AgentsRunning != 0 {
		t.Fatalf("after the retry returned: %+v", w)
	}

	// A late line for the replaced attempt changes nothing: the call is its latest attempt's.
	appendJournal(`{"type":"failed","key":"v2:probe-a","agentId":"a"}` + "\n")
	if w = readWorkflow(subagents, parent, run, ""); w.AgentsDone != 2 || w.AgentsFailed != 0 || len(w.Agents) != 2 {
		t.Fatalf("after a late line for the replaced attempt: %+v", w)
	}

	// Rewritten: the calls start over with the agents.
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), []byte(`{"type":"started","key":"v2:probe-a","agentId":"x","phase":"Probe"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	expireWorkflow(dir)
	if w = readWorkflow(subagents, parent, run, ""); w.AgentsStarted != 1 || len(w.Agents) != 1 || w.Agents[0].ID != "x" || !w.Agents[0].Running {
		t.Fatalf("after a rewrite: %+v", w)
	}
}

// What a key does and does not merge: a retry of an agent that had already returned replaces it too (seen in a real
// journal), identical calls with keys of their own stay apart, and journals without keys are read as before.
func TestWorkflowAgentKeys(t *testing.T) {
	for _, tt := range []struct {
		name, journal, want string
		started, done       int
	}{
		{"retry of a finished agent", `{"type":"started","key":"v2:k","agentId":"a"}
{"type":"result","key":"v2:k","agentId":"a"}
{"type":"started","key":"v2:k","agentId":"a2"}
`, "a2=running", 1, 0},
		{"retried twice", `{"type":"started","key":"v2:k","agentId":"a"}
{"type":"started","key":"v2:k","agentId":"a2"}
{"type":"started","key":"v2:k","agentId":"a3"}
{"type":"result","key":"v2:k","agentId":"a3"}
`, "a3=done", 1, 1},
		{"identical calls have their own keys", `{"type":"started","key":"v2:one","agentId":"a"}
{"type":"started","key":"v2:two","agentId":"b"}
{"type":"result","key":"v2:one","agentId":"a"}
`, "a=done,b=running", 2, 1},
		{"no keys", `{"type":"started","agentId":"a"}
{"type":"started","agentId":"b"}
{"type":"result","agentId":"b"}
`, "a=running,b=done", 2, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, parent, sess := workflowSession(t)
			writeRun(t, sess, "wf_keys", tt.journal, nil, time.Minute)
			run := proto.Workflow{TaskID: "task", RunID: "wf_keys", Status: proto.WorkflowRunning, Running: true, StartedAt: time.Now().Add(-time.Hour)}
			w, _ := workflowProgress(&wfJournal{}, filepath.Join(sess, "subagents", "workflows", "wf_keys"), "", parent, run, "")
			var got []string
			for _, a := range w.Agents {
				got = append(got, a.ID+"="+a.Status)
			}
			if strings.Join(got, ",") != tt.want || w.AgentsStarted != tt.started || w.AgentsDone != tt.done {
				t.Fatalf("agents %v, started %d, done %d", got, w.AgentsStarted, w.AgentsDone)
			}
		})
	}
}

// A run's journal is read for what it gained: a line still being written waits for its end, a rewritten or replaced
// journal is read again from its start.
func TestWorkflowJournalIncremental(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.jsonl")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	appendTo := func(s string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(s); err != nil {
			t.Fatal(err)
		}
	}
	var j wfJournal
	state := func() string {
		var out []string
		for _, a := range j.agents {
			out = append(out, a.id+"="+cmpStr(a.outcome, "running"))
		}
		return strings.Join(out, ",")
	}
	write(`{"type":"launched"}` + "\n" + `{"type":"started","agentId":"a"}` + "\n" + `{"type":"started","agentId":"b"}` + "\n")
	j.update(path)
	if got := state(); got != "a=running,b=running" {
		t.Fatalf("first read: %s", got)
	}
	appendTo(`{"type":"result","agentId":"a","result":{"answer":"` + strings.Repeat("x", 4096) + `"}}` + "\n" + `{"type":"fai`)
	j.update(path)
	if got := state(); got != "a=done,b=running" {
		t.Fatalf("after a result and half a line: %s", got)
	}
	appendTo(`led","agentId":"b"}` + "\n")
	j.update(path)
	if got := state(); got != "a=done,b=failed" {
		t.Fatalf("after the line's end: %s", got)
	}
	write(`{"type":"started","agentId":"x"}` + "\n")
	j.update(path)
	if got := state(); got != "x=running" {
		t.Fatalf("after a rewrite: %s", got)
	}
	if err := os.WriteFile(path+".new", []byte(`{"type":"started","agentId":"y"}`+"\n"+`{"type":"started","agentId":"z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	j.update(path)
	if got := state(); got != "y=running,z=running" {
		t.Fatalf("after a replacement: %s", got)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	j.update(path)
	if got := state(); got != "" {
		t.Fatalf("without a journal: %s", got)
	}
}

// An ended run is read once (#5511): with the transcript saying it ended and no state file, its journal and agents are
// not read again every refresh, only once more when the state file shows up; then never. A stalled run is read again.
func TestWorkflowEndedRunIsCached(t *testing.T) {
	_, parent, sess := workflowSession(t)
	subagents := filepath.Join(sess, "subagents")
	journal := `{"type":"started","agentId":"a","phase":"Audit"}` + "\n" + `{"type":"result","agentId":"a"}` + "\n"
	writeRun(t, sess, "wf_done", journal, map[string]string{"a": "done"}, time.Minute)
	dir := filepath.Join(subagents, "workflows", "wf_done")
	appendJournal := func(dir, agent string) {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(dir, "journal.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(`{"type":"started","agentId":"` + agent + `","phase":"Audit"}` + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	ended := time.Now()
	run := proto.Workflow{TaskID: "t", RunID: "wf_done", Status: proto.WorkflowCompleted, StartedAt: ended.Add(-time.Hour), EndedAt: &ended}
	if w := readWorkflow(subagents, parent, run, ""); w.AgentsStarted != 1 || w.Running {
		t.Fatalf("first read: %+v", w)
	}
	appendJournal(dir, "b")
	expireWorkflow(dir)
	if w := readWorkflow(subagents, parent, run, ""); w.AgentsStarted != 1 {
		t.Fatalf("an ended run was read again: %+v", w)
	}
	if err := os.WriteFile(filepath.Join(sess, "workflows", "wf_done.json"), []byte(`{"status":"completed","workflowName":"named"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	expireWorkflow(dir)
	w := readWorkflow(subagents, parent, run, "")
	if w.AgentsStarted != 2 || w.Name != "named" {
		t.Fatalf("its state file showed up: %+v", w)
	}
	appendJournal(dir, "c")
	expireWorkflow(dir)
	if again := readWorkflow(subagents, parent, run, ""); !reflect.DeepEqual(again, w) {
		t.Fatalf("a run with its state file was read again: %+v", again)
	}

	// Stalled: the transcript says it runs, nothing moved for long. It may move again.
	writeRun(t, sess, "wf_stalled", `{"type":"started","agentId":"s","phase":"Audit"}`+"\n", map[string]string{"s": "…"}, 2*time.Hour)
	stalled := proto.Workflow{TaskID: "s", RunID: "wf_stalled", Status: proto.WorkflowRunning, Running: true, StartedAt: time.Now().Add(-3 * time.Hour)}
	if w := readWorkflow(subagents, parent, stalled, ""); w.Status != proto.WorkflowStalled {
		t.Fatalf("stalled: %+v", w)
	}
	sdir := filepath.Join(subagents, "workflows", "wf_stalled")
	appendJournal(sdir, "s2")
	expireWorkflow(sdir)
	if w := readWorkflow(subagents, parent, stalled, ""); w.Status != proto.WorkflowRunning || w.AgentsStarted != 2 {
		t.Fatalf("a stalled run that moved again: %+v", w)
	}
}
