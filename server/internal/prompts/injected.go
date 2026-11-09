package prompts

import (
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// The hub types some messages into conversations as the user would, for the model: a quiet task's notice
// (assistant/task_notice), a scheduled task's run (assistant/task_run), a wake-up (agents/wake), the continue after a
// usage limit reset (agents/limit_continue), the background tasks
// a node restart stopped (assistant/lost_tasks), news of an inbox item for a conversation it was handed to
// (inbox/handoff_note). The model reads them as they are; the apps show one line instead
// (docs/protocol/protocol.md "Messages Persona injects"). ParseInjected reads them back off the transcript's text, so old lines are
// known too; its tests render every template and parse the result, which keeps the two in step.

var (
	noticeHeadRe   = lazyre.New(`^🔔 任务「(.+)」(做完了|在等用户|被模型服务出错打断了|没开起来)(?:（(.+)）)?$`)
	followupHeadRe = lazyre.New(`^⏰ (.+)（(你之前安排的跟进|定时任务(?:，每 [^）]+)?)）$`)
	wakeHeadRe     = lazyre.New(`^⏰（Persona 叫醒你，第 \d+ 次）你上一轮停下时说在等「(.*?)」`)
	superviseRe    = lazyre.New(`^📋 督办：你派出去的任务有 (\d+) 个要你处理$`)
	inboxHeadRe    = lazyre.New(`^📥 收件箱事件「(.+)」有了下文$`)
	leadContextRe  = lazyre.New(`^(?s)\s*<persona-context\b[^>]*>.*?</persona-context>`)
)

// noticeMarker is in every task notice (assistant/task_notice): a first line alone could be the user's.
const noticeMarker = "这是 Persona 的任务通知"

// superviseMarker is in every supervise (follow-up) message (assistant/supervise, #3124).
const superviseMarker = "这是 Persona 的督办消息"

// inboxMarker is in every inbox note for a conversation an item was handed to (inbox/handoff_note, #1194).
const inboxMarker = "这是 Persona 的收件箱通知"

// limitContinueHead starts agents/limit_continue: the manager continues an agent once its usage limit reset.
const limitContinueHead = "⏰（Persona 叫你接着做）订阅的用量额度已经重置了"

// lostTasksHead starts assistant/lost_tasks.
const lostTasksHead = "⚠️ 运行你的 node 刚重启过"

// kbHintHead starts assistant/routine_kb_hint, which a daily run's prompt may end with.
const kbHintHead = "（知识库"

var noticeStates = map[string]string{"做完了": "done", "在等用户": "needs", "被模型服务出错打断了": "interrupted", "没开起来": "failed"}

var noticeLines = map[string]string{"done": "做完了", "needs": "在等你", "interrupted": "被打断了", "failed": "没开起来"}

// ParseInjected says what text is when it is one of the messages Persona writes for the model; nil for anything
// else (what the user wrote stays theirs: only the templates' exact shapes are taken).
func ParseInjected(text string) *proto.Injected {
	if UserAuthoredContext(text) {
		return nil
	}
	for {
		loc := leadContextRe.FindStringIndex(text)
		if loc == nil {
			break
		}
		text = text[loc[1]:]
	}
	text = strings.TrimSpace(text)
	head, rest, _ := strings.Cut(text, "\n")
	switch {
	case strings.HasPrefix(head, "🔔 任务「"):
		m := noticeHeadRe.FindStringSubmatch(head)
		if m == nil || !strings.Contains(rest, noticeMarker) {
			return nil
		}
		state := noticeStates[m[2]]
		return &proto.Injected{Kind: proto.InjectedTaskNotice, State: state, Title: m[1], Machine: m[3],
			Line: "任务「" + m[1] + "」" + noticeLines[state]}
	case strings.HasPrefix(head, limitContinueHead):
		// Only hubs that write it mark it: it is not in the shared fixtures the apps' own recognition runs.
		return &proto.Injected{Kind: proto.InjectedFollowup, State: "limit_continue", Line: "用量额度重置了，Persona 让它接着做"}
	case strings.HasPrefix(head, "⏰（"):
		m := wakeHeadRe.FindStringSubmatch(head)
		if m == nil {
			return nil
		}
		return &proto.Injected{Kind: proto.InjectedFollowup, State: "wake", Title: m[1], Line: "Persona 叫醒了它：看看「" + m[1] + "」好了没有"}
	case strings.HasPrefix(head, "⏰ "):
		m := followupHeadRe.FindStringSubmatch(head)
		if m == nil {
			return nil
		}
		in := &proto.Injected{Kind: proto.InjectedFollowup, Title: m[1], Detail: followupDetail(rest)}
		switch {
		case m[2] == "你之前安排的跟进":
			in.State, in.Line = "once", "跟进「"+m[1]+"」到时间了"
		case strings.HasPrefix(m[2], "定时任务，每 "):
			in.State, in.Line = "interval", "定时任务「"+m[1]+"」开始运行"
		default:
			in.State, in.Line = "daily", "定时任务「"+m[1]+"」开始运行"
		}
		return in
	case strings.HasPrefix(head, "📋 督办："):
		// Only hubs that write it mark it: it is not in the shared fixtures the apps' own recognition runs.
		m := superviseRe.FindStringSubmatch(head)
		if m == nil || !strings.Contains(rest, superviseMarker) {
			return nil
		}
		level := "triage" // Messages from before levels were added are routine triage too.
		for _, candidate := range []string{"info", "triage", "user"} {
			if strings.Contains(rest, `<persona-supervision level="`+candidate+`" />`) {
				level = candidate
				break
			}
		}
		return &proto.Injected{Kind: proto.InjectedSystem, State: "supervise", Level: level, Line: "督办：" + m[1] + " 个派出去的任务要处理"}
	case strings.HasPrefix(head, "📥 收件箱事件「"):
		// Only hubs that write it mark it: it is not in the shared fixtures the apps' own recognition runs.
		m := inboxHeadRe.FindStringSubmatch(head)
		if m == nil || !strings.Contains(rest, inboxMarker) {
			return nil
		}
		return &proto.Injected{Kind: proto.InjectedSystem, State: "inbox", Title: m[1], Line: "收件箱「" + m[1] + "」有了下文"}
	case strings.HasPrefix(head, lostTasksHead):
		n := 0
		for _, l := range strings.Split(rest, "\n") {
			if strings.HasPrefix(l, "- ") {
				n++
			}
		}
		line := "node 重启过，后台任务停了"
		if n > 0 {
			line = fmt.Sprintf("node 重启过，%d 个后台任务停了", n)
		}
		return &proto.Injected{Kind: proto.InjectedSystem, State: "lost_tasks", Line: line}
	}
	return nil
}

// followupDetail is a run's prompt: after the notes under its first line, without the knowledge base hint.
func followupDetail(rest string) string {
	_, body, ok := strings.Cut("\n"+rest, "\n\n")
	if !ok {
		return ""
	}
	body = strings.TrimSpace(body)
	if i := strings.LastIndex(body, "\n\n"+kbHintHead); i >= 0 {
		body = strings.TrimSpace(body[:i])
	} else if strings.HasPrefix(body, kbHintHead) {
		body = ""
	}
	return body
}
