package transcript

import (
	"cmp"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Background tasks: Bash with run_in_background and subagents launched async answer with a task id right away
// ("Command running in background with ID: b85polnoj. Output is being written to: …" / "Async agent launched …
// agentId: …") and report back later with a <task-notification> carrying the same id. TaskStop / KillShell end them too.
// A workflow answers "Workflow launched in background. Task ID: w5k2nd1ap\nSummary: …\nTranscript dir: …\nScript file:
// …\nRun ID: wf_…"; a Monitor "Monitor started (task bqke1prjm, expires in 30m unless …)".
var (
	// agentId needs its colon: newer results first warn about "the agentId below", whose next word is not the id.
	bgIDRe     = lazyre.New(`(?:running in background with ID:?|agentId:|in background\. Task ID:?|Monitor started \(task:?)\s*([A-Za-z0-9_-]+)`)
	wfRunRe    = lazyre.New(`(?m)^Run ID:\s*(wf_[A-Za-z0-9_-]+)`)
	wfSumRe    = lazyre.New(`(?m)^Summary:\s*(.+)$`)
	wfScriptRe = lazyre.New(`(?m)^Script file:\s*(\S+\.js)\s*$`)
	wfDirRe    = lazyre.New(`(?m)^Transcript dir:\s*\S*/(wf_[A-Za-z0-9_-]+)\s*$`)
	wfNameRe   = lazyre.New("\\bname:\\s*['\"`]([^'\"`\\n]+)['\"`]")
	expiresRe  = lazyre.New(`expires in (\d+)(ms|s|m|h)\b`)
	wakeInRe   = lazyre.New(`Next wakeup scheduled for [^(]*\(in (\d+)s\)`)
	bgMovedRe  = lazyre.New(`moved to the background \(ID: ([A-Za-z0-9_-]+)\)`) // a foreground command that timed out
	bgOutputRe = lazyre.New(`Output is being written to:\s*(\S+?)\.?(?:\s|$)`)
)

func (m *Meta) trackBackground(toolUseID, name string, raw json.RawMessage, ts time.Time) {
	var in struct {
		Command     string `json:"command"`
		Description string `json:"description"`
		Background  bool   `json:"run_in_background"`
		TaskID      string `json:"task_id"`
		ShellID     string `json:"shell_id"`
		// ScheduleWakeup / CronCreate (the /loop skill)
		Delay     float64 `json:"delaySeconds"`
		Prompt    string  `json:"prompt"`
		Reason    string  `json:"reason"`
		Stop      bool    `json:"stop"`
		Cron      string  `json:"cron"`
		Recurring *bool   `json:"recurring"`
		// Workflow
		WorkflowName string `json:"name"`
		Script       string `json:"script"`
		ScriptPath   string `json:"scriptPath"`
	}
	_ = json.Unmarshal(raw, &in)
	switch name {
	case "ScheduleWakeup":
		if in.Stop {
			m.Loop = nil
			return
		}
		if m.loopPending == nil {
			m.loopPending = map[string]proto.LoopInfo{}
		}
		m.loopPending[toolUseID] = proto.LoopInfo{Kind: "wakeup", Prompt: in.Prompt, Reason: in.Reason,
			IntervalSeconds: int(in.Delay), NextAt: ts.Add(time.Duration(in.Delay) * time.Second)}
	case "CronCreate":
		if (in.Recurring != nil && !*in.Recurring) || onceCron(in.Cron) {
			return // a one-off reminder, not a loop
		}
		m.Loop = &proto.LoopInfo{Kind: "cron", Prompt: in.Prompt, Cron: in.Cron}
	case "CronDelete":
		if m.Loop != nil && m.Loop.Kind == "cron" {
			m.Loop = nil
		}
	case "Bash":
		if !in.Background {
			return
		}
		m.pend(toolUseID, proto.BackgroundTask{Kind: "shell", Command: in.Command, Description: in.Description, StartedAt: ts})
	case "Agent", "Task":
		// Whether it went to the background shows in the result ("Async agent launched").
		m.pend(toolUseID, proto.BackgroundTask{Kind: "agent", Description: in.Description, StartedAt: ts})
	case "Monitor":
		m.pend(toolUseID, proto.BackgroundTask{Kind: "monitor", Command: in.Command, Description: in.Description, StartedAt: ts})
	case "Workflow":
		name := in.WorkflowName
		if mm := wfNameRe.FindStringSubmatch(in.Script); name == "" && mm != nil {
			name = mm[1]
		}
		if name == "" {
			name = scriptName(in.ScriptPath)
		}
		m.pend(toolUseID, proto.BackgroundTask{Kind: "workflow", Description: name, StartedAt: ts})
	case "TaskStop", "KillShell", "KillBash":
		m.removeBackground(in.TaskID + in.ShellID)
		m.workflowEnded(in.TaskID+in.ShellID, proto.WorkflowKilled, ts)
	}
}

// onceCron reports a cron expression pinned to one date ("33 17 21 10 *": day of month and month both fixed).
func onceCron(expr string) bool {
	f := strings.Fields(expr)
	if len(f) < 5 {
		return false
	}
	_, errDay := strconv.Atoi(f[2])
	_, errMonth := strconv.Atoi(f[3])
	return errDay == nil && errMonth == nil
}

func (m *Meta) pend(toolUseID string, t proto.BackgroundTask) {
	if m.bgPending == nil {
		m.bgPending = map[string]proto.BackgroundTask{}
	}
	m.bgPending[toolUseID] = t
}

func (m *Meta) backgroundStarted(toolUseID, result string, isError bool, ts time.Time) {
	if l, ok := m.loopPending[toolUseID]; ok {
		delete(m.loopPending, toolUseID)
		if !isError {
			if mm := wakeInRe.FindStringSubmatch(result); mm != nil { // "Next wakeup scheduled for 11:57:00 (in 1859s)"
				if n, err := strconv.Atoi(mm[1]); err == nil {
					l.NextAt = ts.Add(time.Duration(n) * time.Second)
				}
			}
			if m.Loop != nil && m.Loop.Kind == "wakeup" {
				l.Ticks = m.Loop.Ticks
			}
			l.Ticks++
			m.Loop = &l
		}
		return
	}
	t, ok := m.bgPending[toolUseID]
	if !ok {
		if mv := bgMovedRe.FindStringSubmatch(result); mv != nil && !isError {
			m.removeBackground(mv[1])
			m.Background = append(m.Background, proto.BackgroundTask{ID: mv[1], Kind: "shell", Description: "超时转到后台的命令",
				OutputFile: outputFile(result), StartedAt: ts})
		}
		return
	}
	delete(m.bgPending, toolUseID)
	match := bgIDRe.FindStringSubmatch(result)
	if isError || match == nil {
		return // ran in the foreground after all, or failed to start
	}
	t.ID = match[1]
	t.OutputFile = outputFile(result)
	if t.StartedAt.IsZero() {
		t.StartedAt = ts
	}
	switch t.Kind {
	case "monitor":
		if e := expiresRe.FindStringSubmatch(result); e != nil {
			n, _ := strconv.Atoi(e[1])
			unit := map[string]time.Duration{"ms": time.Millisecond, "s": time.Second, "m": time.Minute, "h": time.Hour}[e[2]]
			at := ts.Add(time.Duration(n) * unit)
			t.ExpiresAt = &at
		}
	case "workflow":
		m.workflowStarted(&t, toolUseID, result)
	}
	m.removeBackground(t.ID)
	m.Background = append(m.Background, t)
}

// workflowsKept: how many workflow runs a session's Meta remembers.
const workflowsKept = 8

func (m *Meta) workflowStarted(t *proto.BackgroundTask, toolUseID, result string) {
	w := proto.Workflow{TaskID: t.ID, Name: t.Description, Status: proto.WorkflowRunning, Running: true, StartedAt: t.StartedAt,
		ToolUseID: toolUseID}
	if r := wfRunRe.FindStringSubmatch(result); r != nil {
		w.RunID = r[1]
	} else if r := wfDirRe.FindStringSubmatch(result); r != nil {
		w.RunID = r[1]
	}
	if r := wfSumRe.FindStringSubmatch(result); r != nil {
		w.Summary = strings.TrimSpace(r[1])
	}
	if r := wfScriptRe.FindStringSubmatch(result); r != nil && w.RunID != "" {
		if m.WorkflowScripts == nil {
			m.WorkflowScripts = map[string]string{}
		}
		m.WorkflowScripts[w.RunID] = r[1]
		if w.Name == "" {
			w.Name = scriptName(r[1])
		}
	}
	t.RunID = w.RunID
	if t.Description == "" {
		t.Description = cmp.Or(w.Name, w.Summary)
	}
	for i, o := range m.Workflows {
		if o.TaskID == w.TaskID {
			m.Workflows = append(m.Workflows[:i:i], m.Workflows[i+1:]...)
			break
		}
	}
	m.Workflows = append(m.Workflows, w)
	if n := len(m.Workflows) - workflowsKept; n > 0 {
		for _, o := range m.Workflows[:n] {
			delete(m.WorkflowScripts, o.RunID)
		}
		m.Workflows = append([]proto.Workflow(nil), m.Workflows[n:]...)
	}
}

// workflowEnded records that the workflow with task id ended with status (its notification, a TaskStop).
func (m *Meta) workflowEnded(id, status string, ts time.Time) {
	for i := range m.Workflows {
		w := &m.Workflows[i]
		if w.TaskID != id || !w.Running {
			continue
		}
		switch status {
		case proto.WorkflowFailed, proto.WorkflowKilled:
			status = strings.Clone(status) // a piece of the notification, which it would keep whole
		case "stopped":
			status = proto.WorkflowKilled
		default:
			status = proto.WorkflowCompleted
		}
		w.Status, w.Running, w.EndedAt = status, false, &ts
	}
}

// scriptName is a workflow's name from its script file: "<name>-wf_<run>.js" (a saved one: "<name>.js").
func scriptName(path string) string {
	base := strings.TrimSuffix(path[strings.LastIndexAny(path, "/\\")+1:], ".js")
	if i := strings.Index(base, "-wf_"); i > 0 {
		base = base[:i]
	}
	return base
}

func outputFile(result string) string {
	if o := bgOutputRe.FindStringSubmatch(result); o != nil {
		return o[1]
	}
	return ""
}

func (m *Meta) taskFinished(notification string, ts time.Time) {
	if id := firstTag(notification, "task-id"); id != "" {
		m.removeBackground(id)
		m.workflowEnded(id, firstTag(notification, "status"), ts)
	}
}

func (m *Meta) removeBackground(id string) {
	if id == "" {
		return
	}
	for i, t := range m.Background {
		if t.ID == id {
			m.Background = append(m.Background[:i:i], m.Background[i+1:]...)
			return
		}
	}
}

// markWake flags a background task's notice that arrives as a user message: the agent was idle and the notice
// started a new turn (ones absorbed mid-turn come as queued_command attachments instead).
func markWake(items []proto.Item) {
	for i := range items {
		if items[i].Kind == proto.KindNotice {
			items[i].Wake = true
		}
	}
}
