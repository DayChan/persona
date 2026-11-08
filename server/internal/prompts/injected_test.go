package prompts

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// injectedFixtures is shared with the apps (web, Android, Apple read the same cases for their fallback):
//
//	UPDATE_GOLDEN=1 go test ./internal/prompts -run Injected
const injectedFixtures = "../../../spec/fixtures/injected/cases.json"

type injectedCase struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Source      string          `json:"source"`
	Input       injectedInput   `json:"input"`
	Expected    *proto.Injected `json:"expected"`
}

type injectedInput struct {
	Text string `json:"text"`
}

func injectedCases() []injectedCase {
	due := time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)
	c := func(name, desc, source, text string, want *proto.Injected) injectedCase {
		return injectedCase{Name: name, Description: desc, Source: source, Input: injectedInput{text}, Expected: want}
	}
	notice := "server/internal/prompts/assistant/task_notice.md"
	run := "server/internal/prompts/assistant/task_run.md"
	return []injectedCase{
		c("task_done", "任务做完的通知：只留标题、电脑和一句话。", notice,
			RenderTaskNotice(TaskNotice{Title: "README 去掉扩展列表", Machine: "公司电脑", State: "done", Text: "改好了，PR 在这里。"}),
			&proto.Injected{Kind: "task_notice", State: "done", Title: "README 去掉扩展列表", Machine: "公司电脑", Line: "任务「README 去掉扩展列表」做完了"}),
		c("task_needs", "任务在等用户：带着提问也只显示一句话。", notice,
			RenderTaskNotice(TaskNotice{Title: "升级依赖", Machine: "家里的 Mac", State: "needs", Ask: "- 要不要一起升级测试框架？（选项：要 / 不要）"}),
			&proto.Injected{Kind: "task_notice", State: "needs", Title: "升级依赖", Machine: "家里的 Mac", Line: "任务「升级依赖」在等你"}),
		c("task_interrupted_after_needs", "先等过用户、后被打断：以最后的状态为准。", notice,
			RenderTaskNotice(TaskNotice{Title: "修登录页", State: "interrupted", Earlier: []string{"等过用户"}, Text: "API Error: overloaded"}),
			&proto.Injected{Kind: "task_notice", State: "interrupted", Title: "修登录页", Line: "任务「修登录页」被打断了"}),
		c("task_done_reporting", "从 /agent 这类对话派出的任务做完：说法不同，也只显示一句话。", notice,
			RenderTaskNotice(TaskNotice{Title: "提 MR 修掉它", Machine: "公司电脑", State: "done", Text: "MR 提好了。", Reporting: true}),
			&proto.Injected{Kind: "task_notice", State: "done", Title: "提 MR 修掉它", Machine: "公司电脑", Line: "任务「提 MR 修掉它」做完了"}),
		c("task_failed", "任务没开起来，没有电脑名。", notice,
			RenderTaskNotice(TaskNotice{Title: "整理周报", State: "failed", Text: "rpc: connection closed"}),
			&proto.Injected{Kind: "task_notice", State: "failed", Title: "整理周报", Line: "任务「整理周报」没开起来"}),
		c("task_title_with_brackets", "标题里有括号和书名号也能取对。", notice,
			RenderTaskNotice(TaskNotice{Title: "改「设置（外观）」页", Machine: "公司电脑", State: "done"}),
			&proto.Injected{Kind: "task_notice", State: "done", Title: "改「设置（外观）」页", Machine: "公司电脑", Line: "任务「改「设置（外观）」页」做完了"}),
		c("followup_once_late", "晚到的一次性跟进：说明行不进 detail。", run,
			RenderTaskRun(TaskRun{Label: "看看 CI", Once: true, Late: "25 分钟", Due: due, Prompt: "看一下 CI 有没有过，过了就合并。"}),
			&proto.Injected{Kind: "followup", State: "once", Title: "看看 CI", Line: "跟进「看看 CI」到时间了", Detail: "看一下 CI 有没有过，过了就合并。"}),
		c("followup_daily_kb", "每天的定时任务：去掉末尾的知识库提示。", run,
			RenderTaskRun(TaskRun{Label: "早报", Due: due, Prompt: "整理今天的日程。\n\n再看看邮件。", KBHint: true}),
			&proto.Injected{Kind: "followup", State: "daily", Title: "早报", Line: "定时任务「早报」开始运行", Detail: "整理今天的日程。\n\n再看看邮件。"}),
		c("followup_interval_continued", "按间隔运行、接着自己的对话。", run,
			RenderTaskRun(TaskRun{Label: "盯部署", Interval: true, Every: "30 分钟", Due: due, Prompt: "看部署好了没有。", Continued: true}),
			&proto.Injected{Kind: "followup", State: "interval", Title: "盯部署", Line: "定时任务「盯部署」开始运行", Detail: "看部署好了没有。"}),
		c("followup_interval_push_asked", "只在它自己提醒时推送的任务：那句说明在首段，不进 detail（#2396）。", run,
			RenderTaskRun(TaskRun{Label: "新邮件", Interval: true, Every: "5 分钟", Due: due, Prompt: "看看邮箱有没有新邮件，有就告诉我。", PushAsked: true}),
			&proto.Injected{Kind: "followup", State: "interval", Title: "新邮件", Line: "定时任务「新邮件」开始运行", Detail: "看看邮箱有没有新邮件，有就告诉我。"}),
		c("followup_wake", "叫醒等待中的 Agent。", "server/internal/prompts/agents/wake.md",
			RenderAgentWake(AgentWake{Attempt: 2, Wait: "CI 跑完", Then: "合并", Agent: "构建", AgentLine: "好了"}),
			&proto.Injected{Kind: "followup", State: "wake", Title: "CI 跑完", Line: "Persona 叫醒了它：看看「CI 跑完」好了没有"}),
		c("system_lost_tasks", "node 重启丢了后台任务：数出几个。", "server/internal/prompts/assistant/lost_tasks.md",
			RenderLostTasks([]string{"npm test", "监控 CI"}),
			&proto.Injected{Kind: "system", State: "lost_tasks", Line: "node 重启过，2 个后台任务停了"}),
		c("snapshot_before", "前面带着实时快照块的也认。", notice,
			RenderTurnContext(TurnContext{Sections: []string{"- 现在：……"}})+"\n"+RenderTaskNotice(TaskNotice{Title: "跑测试", State: "done"}),
			&proto.Injected{Kind: "task_notice", State: "done", Title: "跑测试", Line: "任务「跑测试」做完了"}),
		c("snapshot_before_persona", "旧名字 <persona-context> 的快照块也认（历史消息、旧 node，#3182）。", notice,
			"<persona-context>\n实时快照：……\n</persona-context>\n"+RenderTaskNotice(TaskNotice{Title: "跑测试", State: "done"}),
			&proto.Injected{Kind: "task_notice", State: "done", Title: "跑测试", Line: "任务「跑测试」做完了"}),
		c("contexts_before_persona", "前面带着有属性的 <persona-context> 块的也认。", notice,
			`<persona-context kind="context" title="引用的消息" url="">`+"\n引用\n</persona-context>\n"+RenderTaskNotice(TaskNotice{Title: "跑测试", State: "done"}),
			&proto.Injected{Kind: "task_notice", State: "done", Title: "跑测试", Line: "任务「跑测试」做完了"}),
		c("user_bell_without_marker", "用户自己打的「🔔 任务…」没有通知的说明行：不认。", notice,
			"🔔 任务「买菜」做完了\n帮我记一下", nil),
		c("user_alarm_plain", "用户自己打的「⏰ 明天提醒我」：不认。", run, "⏰ 明天八点提醒我开会", nil),
		c("plain_text", "普通的话不认。", "docs/protocol/protocol.md「Persona 注入的消息」", "帮我看看这个 PR", nil),
	}
}

func TestParseInjected(t *testing.T) {
	cases := injectedCases()
	for _, c := range cases {
		got := ParseInjected(c.Input.Text)
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(c.Expected)
		if !bytes.Equal(g, w) {
			t.Errorf("%s: got %s, want %s", c.Name, g, w)
		}
	}
	// The shape every spec/fixtures domain has (spec/README.md "Fixture conventions").
	b, err := json.MarshalIndent(struct {
		Version int            `json:"version"`
		Cases   []injectedCase `json:"cases"`
	}{1, cases}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(injectedFixtures), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(injectedFixtures, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	have, err := os.ReadFile(injectedFixtures)
	if err != nil {
		t.Fatalf("%v (UPDATE_GOLDEN=1 writes it)", err)
	}
	if !bytes.Equal(have, b) {
		t.Errorf("%s is out of date (UPDATE_GOLDEN=1 rewrites it)", injectedFixtures)
	}
}

// The notice's first line comes first whatever else the template grows: what the apps key on.
func TestInjectedTemplatesKeepTheirHead(t *testing.T) {
	for _, s := range []string{"done", "needs", "interrupted", "failed"} {
		if ParseInjected(RenderTaskNotice(TaskNotice{Title: "x", State: s, Ask: "a", Text: "b", Earlier: []string{"做完过一次"}})) == nil {
			t.Errorf("task notice %s not recognised", s)
		}
	}
}

// The continue after a usage limit reset (#1710) is one line, as a wake is.
func TestParseInjectedLimitContinue(t *testing.T) {
	got := ParseInjected(RenderAgentLimitContinue())
	if got == nil || got.Kind != proto.InjectedFollowup || got.State != "limit_continue" || got.Line != "用量额度重置了，Persona 让它接着做" {
		t.Fatalf("ParseInjected = %+v", got)
	}
	if ParseInjected("⏰（Persona 叫你接着做）") != nil {
		t.Fatal("a bare head taken for the continue")
	}
}

// A follow-up (supervision) message (#3124) is a system line, read off its head and marker; a user's line that looks like its head is not.
func TestParseInjectedSupervise(t *testing.T) {
	text := RenderSupervise(Supervise{Entries: []SuperviseEntry{{ID: "todo_1", Title: "部署", Kind: "review"}, {ID: "todo_2", Title: "修崩溃", Kind: "idle"}}})
	got := ParseInjected("<persona-context>快照</persona-context>\n" + text)
	if got == nil || got.Kind != proto.InjectedSystem || got.State != "supervise" || got.Line != "督办：2 个派出去的任务要处理" {
		t.Fatalf("ParseInjected = %+v", got)
	}
	if ParseInjected("📋 督办：你派出去的任务有 2 个要你处理\n我自己写的") != nil {
		t.Fatal("a user's text taken for a 督办 message")
	}
}

// An inbox note for a conversation an item was handed to (#1194) is a system line, read off its head and marker; a
// user's line that looks like its head is not.
func TestParseInjectedInboxHandoff(t *testing.T) {
	text := RenderInboxHandoffNote(InboxHandoffNote{ID: "lark:f1:100", Title: "改「报价（周五）」", Note: "示例用户驳回了你的建议。"})
	got := ParseInjected(text)
	if got == nil || got.Kind != proto.InjectedSystem || got.State != "inbox" || got.Title != "改「报价（周五）」" ||
		got.Line != "收件箱「改「报价（周五）」」有了下文" {
		t.Fatalf("ParseInjected = %+v", got)
	}
	if ParseInjected("📥 收件箱事件「报价」有了下文\n我自己写的") != nil {
		t.Fatal("a user's text taken for an inbox note")
	}
}
