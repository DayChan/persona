package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Workflow progress must reach the hub even when only its journal grows and the main turn stays stopped.
func TestWorkflowPollPushesJournalProgress(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := pollRepo(t)
	sess := workflowPollSession(t, n, f, home, cwd, nil)
	pushes, stop := pollNode(t, n)
	defer stop()
	before := nextWorkflowPush(t, pushes, 5*time.Second)
	if before.Rev == 0 || len(before.Workflows) != 1 || before.Workflows[0].AgentsDone != 0 || before.Workflows[0].AgentsRunning != 2 {
		t.Fatalf("initial session: %+v", before)
	}

	appendFile(t, filepath.Join(sess, "subagents", "workflows", "wf_poll", "journal.jsonl"), `{"type":"result","agentId":"a1","result":{"ok":true}}`+"\n")
	after := nextWorkflowPush(t, pushes, 3*time.Second)
	if after.Rev != before.Rev || after.Status != before.Status || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("main session changed: before=%+v after=%+v", before, after)
	}
	if len(after.Workflows) != 1 || after.Workflows[0].AgentsDone != 1 || after.Workflows[0].AgentsStarted != 2 || after.Workflows[0].AgentsRunning != 1 {
		t.Fatalf("journal progress was not pushed: %+v", after.Workflows)
	}
}

// An agent's next tool call reaches the hub within the workflow cache's pace although no count changed (#5511), the
// same tool called again included.
func TestWorkflowPollPushesAgentTools(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := pollRepo(t)
	call := func(id, tool, input string) string {
		return edgeReply(id, time.Now().UTC().Format(time.RFC3339), "model-a", `[{"type":"tool_use","id":"`+id+`","name":"`+tool+`","input":`+input+`}]`,
			`{"input_tokens":10,"output_tokens":1}`)
	}
	sess := workflowPollSession(t, n, f, home, cwd, map[string]string{"a1": call("t1", "Read", `{"file_path":"/x/a.go"}`)})
	agent := filepath.Join(sess, "subagents", "workflows", "wf_poll", "agent-a1.jsonl")
	pushes, stop := pollNode(t, n)
	defer stop()
	tool := func(s proto.Session) (string, int) {
		t.Helper()
		for _, a := range s.Workflows[0].Agents {
			if a.ID == "a1" && a.LatestTool != nil {
				return a.LatestTool.Name + " " + a.LatestTool.Summary, a.Calls
			}
		}
		t.Fatalf("no a1 tool in %+v", s.Workflows)
		return "", 0
	}
	if got, calls := tool(nextWorkflowPush(t, pushes, 5*time.Second)); got != "Read 在读 a.go" || calls != 1 {
		t.Fatalf("initial: %s, %d calls", got, calls)
	}
	for i, c := range []struct{ id, tool, input, want string }{
		{"t2", "Bash", `{"command":"go test ./..."}`, "Bash 在跑测试"},
		{"t3", "Bash", `{"command":"go vet ./..."}`, "Bash 在检查代码"}, // the same tool again: only its calls tell
	} {
		appendFile(t, agent, call(c.id, c.tool, c.input))
		if got, calls := tool(nextWorkflowPush(t, pushes, 3*time.Second)); got != c.want || calls != i+2 {
			t.Fatalf("after %s: %s, %d calls", c.id, got, calls)
		}
	}
}

// A retried agent reaches the hub although no count changes (#2323): the stalled attempt and its retry both run, and
// neither has called a tool yet, so only the agents' ids tell the lists apart. Clients would keep linking the
// replaced attempt otherwise.
func TestWorkflowPollPushesRetriedAgent(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := pollRepo(t)
	sess := workflowPollSession(t, n, f, home, cwd, nil)
	pushes, stop := pollNode(t, n)
	defer stop()
	ids := func(s proto.Session) string {
		var out []string
		for _, a := range s.Workflows[0].Agents {
			out = append(out, a.ID)
		}
		return strings.Join(out, ",")
	}
	before := nextWorkflowPush(t, pushes, 5*time.Second)
	if got := ids(before); got != "a1,a2" {
		t.Fatalf("initial agents: %s", got)
	}
	appendFile(t, filepath.Join(sess, "subagents", "workflows", "wf_poll", "journal.jsonl"),
		`{"type":"started","key":"v2:first","agentId":"a3","label":"review:first","phase":"Review"}`+"\n")
	after := nextWorkflowPush(t, pushes, 3*time.Second)
	w := after.Workflows[0]
	if got := ids(after); got != "a2,a3" || w.AgentsStarted != 2 || w.AgentsRunning != 2 {
		t.Fatalf("after the retry: agents %s, %+v", got, w)
	}
}

// workflowPollSession is a stopped session of n's whose turn launched the workflow run wf_poll, two agents started;
// agents are the transcripts of those that have one. It returns the session's directory.
func workflowPollSession(t *testing.T, n *Node, f *fakeHerdr, home, cwd string, agents map[string]string) string {
	t.Helper()
	at := time.Now().Add(-time.Minute)
	stamp := at.UTC().Format(time.RFC3339)
	cwdJSON, _ := json.Marshal(cwd)
	tp := writeTranscript(t, home, cwd, sidA, at,
		`{"type":"user","timestamp":"`+stamp+`","cwd":`+string(cwdJSON)+`,"message":{"role":"user","content":"Review this project"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"`+stamp+`","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Workflow","input":{"name":"review"}}]}}`,
		`{"type":"user","uuid":"u1","timestamp":"`+stamp+`","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"Workflow launched in background. Task ID: wt1\nSummary: Review\nRun ID: wf_poll\n"}]}}`,
		`{"type":"system","subtype":"turn_duration","durationMs":1000,"pendingWorkflowCount":1,"timestamp":"`+stamp+`"}`)
	sess := strings.TrimSuffix(tp, ".jsonl")
	writeRun(t, sess, "wf_poll", `{"type":"started","key":"v2:first","agentId":"a1","label":"review:first","phase":"Review"}`+"\n"+
		`{"type":"started","key":"v2:second","agentId":"a2","label":"review:second","phase":"Review"}`+"\n", nil, time.Minute)
	for id, body := range agents {
		if err := os.WriteFile(filepath.Join(sess, "subagents", "workflows", "wf_poll", "agent-"+id+".jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f.reply("session.snapshot", snapshot(nil, nil,
		[]any{claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "review")}, nil))
	return sess
}

// nextWorkflowPush is the next sessions.changed for sidA within timeout.
func nextWorkflowPush(t *testing.T, pushes <-chan hubPush, timeout time.Duration) proto.Session {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case push := <-pushes:
			if push.Event != "sessions.changed" {
				continue
			}
			var changed struct {
				Sessions []proto.Session `json:"sessions"`
			}
			if err := json.Unmarshal(push.Params, &changed); err != nil {
				t.Fatal(err)
			}
			for _, s := range changed.Sessions {
				if s.SessionID == sidA {
					return s
				}
			}
		case <-deadline.C:
			t.Fatalf("no sessions.changed within %s", timeout)
			return proto.Session{}
		}
	}
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString(s)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("append: write=%v close=%v", writeErr, closeErr)
	}
}
