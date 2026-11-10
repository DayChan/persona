package transcript

import (
	"encoding/json"
	"html"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// What a <task-notification> summary says, in the shapes Claude Code 2.1.3xx writes (the corpus):
//
//	Background command "<description or command>" completed (exit code 0) / failed with exit code 1 / was stopped
//	Monitor event: "<description>"            (the event itself in <event>)
//	Monitor "<description>" stream ended / stopped
//	Agent "<description>" finished / failed: … / is waiting on its own background work
//	Dynamic workflow "<description>" completed / failed / was stopped
var (
	taskSummaryRe = lazyre.New(`(?s)^(Background command|Monitor event:|Monitor|Agent|Dynamic workflow)\s+"(.*)$`)
	// Where the quoted description ends: the last quote followed by how it went (descriptions may hold quotes).
	taskOutcomeRe = lazyre.New(`"\s+(completed|failed|was stopped|was killed|stream ended|stopped|finished|is waiting|timed out)`)
	exitCodeRe    = lazyre.New(`exit code (-?\d+)`)
)

// noteTitleLen: a title is one line of at most this many characters (the whole command is the detail).
const noteTitleLen = 100

// taskNote reads a task notification: what it is, and the one line a notice shows for it (apps that do not know
// TaskNote show that line; it used to be the whole summary — a background heredoc filled screens with it).
func taskNote(xml string) (*proto.TaskNote, string) {
	n := &proto.TaskNote{
		Kind:       "task",
		TaskID:     firstTag(xml, "task-id"),
		ToolUseID:  firstTag(xml, "tool-use-id"),
		OutputFile: firstTag(xml, "output-file"),
		Status:     firstTag(xml, "status"),
	}
	summary := html.UnescapeString(firstTag(xml, "summary"))
	full, outcome := summary, ""
	if m := taskSummaryRe.FindStringSubmatch(summary); m != nil {
		rest := m[2]
		if loc := taskOutcomeRe.FindAllStringSubmatchIndex(rest, -1); loc != nil {
			last := loc[len(loc)-1]
			full, outcome = rest[:last[0]], strings.TrimSpace(rest[last[0]+1:])
		} else if q := strings.LastIndex(rest, `"`); q >= 0 { // "Monitor event: "…"", or words not known yet
			full, outcome = rest[:q], strings.TrimSpace(rest[q+1:])
		}
		switch m[1] {
		case "Background command":
			n.Kind = "shell"
		case "Monitor event:":
			n.Kind, n.Status = "monitor", "event"
		case "Monitor":
			n.Kind = "monitor"
		case "Agent":
			n.Kind = "agent"
		case "Dynamic workflow":
			n.Kind = "workflow"
		}
	}
	switch {
	case n.Kind == "monitor" && strings.HasPrefix(outcome, "stream ended"):
		n.Status = "ended"
	case strings.HasPrefix(outcome, "stopped") || strings.HasPrefix(outcome, "was stopped"):
		if n.Status == "" {
			n.Status = "stopped"
		}
	case (strings.HasPrefix(outcome, "finished") || strings.HasPrefix(outcome, "completed")) && n.Status == "":
		n.Status = "completed"
	case strings.HasPrefix(outcome, "failed") && n.Status == "":
		n.Status = "failed"
	case strings.HasPrefix(outcome, "is waiting"):
		n.Status = "waiting"
	}
	if m := exitCodeRe.FindStringSubmatch(outcome); m != nil {
		if c, err := strconv.Atoi(m[1]); err == nil {
			n.ExitCode = &c
		}
	}
	n.Title = oneLine(full, noteTitleLen)
	switch {
	case firstTag(xml, "event") != "":
		n.Detail = clipTo(html.UnescapeString(firstTag(xml, "event")), pageOutputLen)
	case firstTag(xml, "result") != "":
		n.Detail = clipTo(html.UnescapeString(firstTag(xml, "result")), pageOutputLen)
	case n.Title != strings.TrimSpace(full):
		n.Detail = clipTo(full, pageOutputLen) // a long or multi-line command: the title is its first line
	}
	// Each field is a piece of xml, which would be kept whole for as long as any of them is (Meta.taskCalls keeps the
	// ids for the node's lifetime): copied, they keep only themselves.
	for _, f := range []*string{&n.TaskID, &n.ToolUseID, &n.OutputFile, &n.Status, &n.Title, &n.Detail} {
		*f = strings.Clone(*f)
	}
	return n, noteText(n)
}

// noteText is the notice's one line: "Background command done · <title>", "Monitor event · <the event's first line>"… (in Chinese).
func noteText(n *proto.TaskNote) string {
	failed := "失败"
	if n.ExitCode != nil {
		failed = "失败（退出码 " + strconv.Itoa(*n.ExitCode) + "）"
	}
	var head string
	switch n.Kind {
	case "shell":
		head = map[string]string{"completed": "后台命令完成", "failed": "后台命令" + failed, "killed": "后台命令已停止",
			"stopped": "后台命令已停止"}[n.Status]
		if head == "" {
			head = "后台命令"
		}
	case "monitor":
		if n.Status == "event" {
			if ev := oneLine(n.Detail, noteTitleLen); ev != "" {
				return "Monitor 事件 · " + ev
			}
			head = "Monitor 事件"
		} else {
			head = map[string]string{"ended": "Monitor 结束", "stopped": "Monitor 已停止", "killed": "Monitor 已停止",
				"failed": "Monitor " + failed, "completed": "Monitor 结束"}[n.Status]
			if head == "" {
				head = "Monitor"
			}
		}
	case "agent":
		head = map[string]string{"completed": "子 Agent 完成", "failed": "子 Agent 失败", "waiting": "子 Agent 在等它的后台任务",
			"killed": "子 Agent 已停止", "stopped": "子 Agent 已停止"}[n.Status]
		if head == "" {
			head = "子 Agent"
		}
	case "workflow":
		head = map[string]string{"completed": "后台 workflow 完成", "failed": "后台 workflow 失败",
			"killed": "后台 workflow 已停止", "stopped": "后台 workflow 已停止"}[n.Status]
		if head == "" {
			head = "后台 workflow"
		}
	default:
		if n.Title == "" {
			return "后台任务有更新"
		}
		return n.Title
	}
	if n.Title == "" {
		return head
	}
	return head + " · " + n.Title
}

// oneLine is the first non-blank line of s, cut to n characters.
func oneLine(s string, n int) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			if r := []rune(l); len(r) > n {
				return string(r[:n-1]) + "…"
			}
			return l
		}
	}
	return ""
}

// Since Claude Code 2.1.318 a subagent reports by calling SubagentHandback with its report as `message`; the session
// that launched it gets the report on a hidden user line (origin.handback, handbackReportText), and the agent's task
// notification then only points at it ("This agent's report was delivered to you as a message from "<id>" (its
// SubagentHandback call). Read it there; …"). So the report would be nowhere a client looks: it becomes the
// subagent's last words in its own transcript, and the notification's detail in the parent's.
const handbackTool = "SubagentHandback"

// HandbackTool is handbackTool for the node, which counts a subagent's steps without reading every line through ParseLine.
const HandbackTool = handbackTool

// handbacksKept: reports waiting for their notification; each one takes its own, so more is a session that lost them.
const handbacksKept = 64

// handbackCall reads a SubagentHandback call: the report it hands back. Its result ("Report delivered to your
// caller.") is not shown.
func (m *Meta) handbackCall(id string, input []byte) string {
	if id != "" {
		if m.handbackCalls == nil {
			m.handbackCalls = map[string]bool{}
		}
		m.handbackCalls[id] = true
	}
	var in struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(input, &in)
	return strings.TrimSpace(in.Message)
}

// handbackOf reads a user line's origin: the subagent and the report, when it is one handing its report back.
func handbackOf(origin json.RawMessage) (agent, report string, ok bool) {
	if len(origin) == 0 {
		return "", "", false
	}
	var o struct {
		From               string `json:"from"`
		Handback           bool   `json:"handback"`
		HandbackReportText string `json:"handbackReportText"`
	}
	if json.Unmarshal(origin, &o) != nil || !o.Handback || o.From == "" {
		return "", "", false
	}
	return o.From, o.HandbackReportText, true
}

// handedBack keeps the report a subagent handed back, for the task notification that comes after it.
func (m *Meta) handedBack(agent, report string) {
	if m.handbacks == nil || len(m.handbacks) >= handbacksKept {
		m.handbacks = map[string]string{}
	}
	m.handbacks[agent] = clipTo(strings.TrimSpace(report), pageOutputLen)
}

// reportFor gives an agent's notification the report its agent handed back, in place of the note that points at it;
// that note alone says nothing (the report is not repeated), so with no report known it is dropped.
func (m *Meta) reportFor(n *proto.TaskNote) {
	if n == nil || n.Kind != "agent" {
		return
	}
	if r, ok := m.handbacks[n.TaskID]; ok {
		delete(m.handbacks, n.TaskID)
		n.Detail = r
	} else if strings.HasPrefix(n.Detail, "This agent's report was delivered to you") && strings.Contains(n.Detail, handbackTool) {
		n.Detail = ""
	} else if n.Status == "waiting" && strings.HasPrefix(n.Detail, "This agent has not reported yet") && strings.Contains(n.Detail, handbackTool) {
		n.Detail = "" // Claude Code telling the session to wait for the hand-back: the notice's line already says it waits
	}
}

// noteCall gives a notification that names only its task the call that started it, as an earlier notification of
// the task named it. A subagent that waited on its own background work, then handed its report back, is announced
// done by one that names only the subagent: without the call, apps could not put the report on the subagent's card
// and kept the earlier "has not reported yet" one there. (They also look the call up by the launch receipt's id, and
// since Claude Code 2.1.319 the receipt's first sentence mentions "the agentId below", which they took for it.)
func (m *Meta) noteCall(n *proto.TaskNote) {
	if n == nil || n.TaskID == "" {
		return
	}
	if n.ToolUseID == "" {
		n.ToolUseID = m.taskCalls[n.TaskID]
		return
	}
	if m.taskCalls == nil {
		m.taskCalls = map[string]string{}
	}
	m.taskCalls[n.TaskID] = n.ToolUseID
}
