package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// secretary is an agent the main conversation did not hand out (a long-lived one the user opened), working on no todo.
func secretary(f *fakeHub) {
	f.setSessions("box", proto.Session{SessionID: "sec00001", MachineID: "box", Cwd: "/code/desk", Agent: "claude", Title: "秘书",
		Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: time.Now()})
}

// agent_send with task hands new work to an agent already running: the message goes as written, and the agent becomes
// a supervised task of the main conversation (a todo with the instruction as its notes, the agent linked, done_when
// kept, the task card and what it was told in the feed).
func TestAgentSendTaskMakesATask(t *testing.T) {
	s, f := newQuietService(t)
	secretary(f)
	res, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001", "text": "把周报数据拉一下",
		"task": map[string]any{"title": "拉周报数据", "done_when": []string{"表格链接贴回来"}, "expect_minutes": 40}})
	if err != nil {
		t.Fatal(err)
	}
	if sent := sentToAgent(f); len(sent) != 1 || sent[0] != "把周报数据拉一下" {
		t.Fatalf("sent %q", sent)
	}
	todo := onlyTodo(s)
	if !strings.Contains(res, todo.ID) || todo.Title != "拉周报数据" || todo.Origin != "conv1" || todo.Notes != "把周报数据拉一下" ||
		len(todo.Agents) != 1 || todo.Agents[0].SessionID != "sec00001" {
		t.Fatalf("task: %s\n%+v", res, todo)
	}
	if v := todo.Supervision; v == nil || v.Supervisor != "conv1" || len(v.DoneWhen) != 1 || v.DoneWhen[0] != "表格链接贴回来" || v.ExpectMinutes != 40 {
		t.Fatalf("supervision: %+v", todo.Supervision)
	}
	if got := feedKinds(s, "conv1"); got != "task,routed" {
		t.Fatalf("feed: %s", got)
	}
}

// A task handed without a title is named as task_plan's are (#3020): the instruction cleaned now, not its first 40
// characters, and the model's name for it once it answers; the instruction stays the notes, as written.
func TestAgentSendTaskWithoutATitleIsNamed(t *testing.T) {
	s, f := newQuietService(t)
	secretary(f)
	f.mu.Lock()
	f.todoTitleReply = "整理周报数据"
	f.mu.Unlock()
	text := "ultrathink 主人 11-05 19:47 的反馈，已授权直接开工：把周报数据拉一下，按部门汇总，再把表格链接贴回来给我看一眼" // i18n-ignore
	if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001", "text": text,
		"task": map[string]any{"done_when": []string{"表格链接贴回来"}}}); err != nil { // i18n-ignore
		t.Fatal(err)
	}
	todo := onlyTodo(s)
	if strings.HasPrefix(todo.Title, "ultrathink") || strings.Contains(todo.Title, "11-05") || todo.Notes != text {
		t.Fatalf("made with %q (notes %q)", todo.Title, todo.Notes)
	}
	s.todos.titling.Wait()
	if got := onlyTodo(s).Title; got != "整理周报数据" { // i18n-ignore
		t.Fatalf("not named by the model: %q", got)
	}
	f.mu.Lock()
	asked := f.todoTitleAsked
	f.mu.Unlock()
	if len(asked) != 1 || !strings.Contains(asked[0].User, "按部门汇总") { // i18n-ignore
		t.Fatalf("asked %+v", asked)
	}
}

// agent_supervise names its task the same way: a real title stays, one that reads as a prompt is cleaned (and named
// by the model later), and without one the session's title stays (TestAgentSuperviseAndReport).
func TestAgentSuperviseTitle(t *testing.T) {
	s, f := newQuietService(t)
	secretary(f)
	if _, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{"machine": "box", "session_id": "sec00001",
		"title": "ultrathink 已授权直接开工：拉周报数据"}); err != nil { // i18n-ignore
		t.Fatal(err)
	}
	if got := onlyTodo(s).Title; got != "拉周报数据" { // i18n-ignore
		t.Fatalf("title %q", got)
	}
	s.todos.titling.Wait()
}

// The same agent handed more of its task: no second todo, the new criteria replace the old once the text went (a send
// that fails changes nothing). Other work (another title) is refused, so it is not filed under that task.
func TestAgentSendTaskAgainKeepsOneTask(t *testing.T) {
	s, f := newQuietService(t)
	secretary(f)
	send := func(text string, doneWhen ...string) string {
		t.Helper()
		res, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001", "text": text,
			"task": map[string]any{"title": "拉周报数据", "done_when": doneWhen}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	send("把周报数据拉一下", "表格链接贴回来")
	id := onlyTodo(s).ID
	f.failSend = errors.New("dialog_open")
	if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001", "text": "再加一列",
		"task": map[string]any{"title": "拉周报数据", "done_when": []string{"别的标准"}}}); err == nil {
		t.Fatal("the failed send succeeded")
	}
	if v := onlyTodo(s).Supervision; len(v.DoneWhen) != 1 || v.DoneWhen[0] != "表格链接贴回来" {
		t.Fatalf("a failed send changed the criteria: %+v", v)
	}
	f.failSend = nil
	if res := send("再加上上周对比", "表格链接贴回来", "有上周对比列"); !strings.Contains(res, "already works for your task "+id) {
		t.Fatalf("again: %s", res)
	}
	todo := onlyTodo(s)
	if todo.ID != id || len(todo.Supervision.DoneWhen) != 2 {
		t.Fatalf("todos: %+v", todo)
	}
	if sent := sentToAgent(f); len(sent) != 2 {
		t.Fatalf("sent %q", sent)
	}
	if got := feedKinds(s, "conv1"); got != "task,routed,routed" {
		t.Fatalf("feed: %s", got)
	}
	_, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001", "text": "顺便订个会议室",
		"task": map[string]any{"title": "订会议室"}})
	if err == nil || !strings.Contains(err.Error(), id) || !strings.Contains(err.Error(), "nothing was sent") {
		t.Fatalf("other work: %v", err)
	}
	if n := len(sentToAgent(f)); n != 2 || len(s.todosOf(t.Context(), "*")) != 1 {
		t.Fatalf("other work: sent %d, %d todos", n, len(s.todosOf(t.Context(), "*")))
	}
}

// An agent working for the user's own todo or another conversation's task stays theirs, and only the main conversation
// hands out tasks: refused with why, nothing sent, no todo made.
func TestAgentSendTaskRefusesWhatIsNotItsOwn(t *testing.T) {
	s, f := newQuietService(t)
	ctx := t.Context()
	secretary(f)
	args := map[string]any{"machine": "box", "session_id": "sec00001", "text": "把周报数据拉一下", "task": map[string]any{"title": "拉周报数据"}}
	if _, err := runJSON(t, s, "conv2", "agent_send", args); err == nil || !strings.Contains(err.Error(), "only the main conversation") {
		t.Fatalf("from another conversation: %v", err)
	}
	for _, origin := range []string{"", "conv2"} { // the user's own todo, another conversation's task
		theirs, err := s.createTodo(ctx, "别人的", "x", origin, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.linkTodoAgent(ctx, theirs.ID, "box", "sec00001", "秘书", "/code/desk", "claude"); err != nil {
			t.Fatal(err)
		}
		before := len(s.todosOf(ctx, "*"))
		_, err = runJSON(t, s, "conv1", "agent_send", args)
		if err == nil || !strings.Contains(err.Error(), theirs.ID) || !strings.Contains(err.Error(), "nothing was sent") {
			t.Fatalf("origin %q: %v", origin, err)
		}
		if n := len(s.todosOf(ctx, "*")); n != before {
			t.Fatalf("origin %q: %d todos, %d before", origin, n, before)
		}
		if err := s.failTodo(ctx, theirs.ID, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if sent := sentToAgent(f); len(sent) != 0 {
		t.Fatalf("sent %q", sent)
	}
}

// A send that fails makes no task: the todo made for it is taken back (no closed task keeps the agent, whose agent
// would be closed with it), and the conversation shows nothing. When the machine only did not answer in time (a resume
// can go on after the wait), the text may still arrive: the task stays, with its card.
func TestAgentSendTaskFailedSend(t *testing.T) {
	s, f := newQuietService(t)
	secretary(f)
	args := map[string]any{"machine": "box", "session_id": "sec00001", "text": "把周报数据拉一下", "task": map[string]any{"title": "拉周报数据"}}
	f.failSend = errors.New("dialog_open")
	if _, err := runJSON(t, s, "conv1", "agent_send", args); err == nil || !strings.Contains(err.Error(), "dialog_open") {
		t.Fatalf("err %v", err)
	}
	if n := len(s.todosOf(t.Context(), "*")); n != 0 {
		t.Fatalf("%d todos", n)
	}
	if _, ok := s.openTodoOfAgent("box", "sec00001"); ok || feedKinds(s, "conv1") != "" {
		t.Fatalf("left behind: feed %q", feedKinds(s, "conv1"))
	}

	f.setSessions("box", proto.Session{SessionID: "sec00001", MachineID: "box", Cwd: "/code/desk", Agent: "claude", Title: "秘书",
		Status: proto.StatusOffline, UpdatedAt: time.Now()})
	f.failSend = context.DeadlineExceeded
	if _, err := runJSON(t, s, "conv1", "agent_send", args); err == nil || !strings.Contains(err.Error(), "may still arrive") {
		t.Fatalf("unknown: %v", err)
	}
	if todo := onlyTodo(s); todo.DoneAt != nil || len(todo.Agents) != 1 || feedKinds(s, "conv1") != "task" {
		t.Fatalf("unknown: %+v, feed %q", todo, feedKinds(s, "conv1"))
	}
}

// /exit closes the agent: it hands it no work, so it makes no task.
func TestAgentSendTaskExitIsNoWork(t *testing.T) {
	s, f := newQuietService(t)
	secretary(f)
	if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001", "text": "/exit",
		"task": map[string]any{"title": "关掉"}}); err == nil {
		t.Fatal("/exit made a task")
	}
	if len(sentToAgent(f)) != 0 || len(s.todosOf(t.Context(), "*")) != 0 {
		t.Fatal("something went")
	}
}

// Passed on: an agent the handed one starts for the work (herdr persona_from) joins the same task, so its status and
// its agent's end come back to the main conversation's task.
func TestAgentSendTaskFollowsTheAgentItStarts(t *testing.T) {
	s, f := newQuietService(t)
	secretary(f)
	if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{"machine": "box", "session_id": "sec00001", "text": "把周报数据拉一下",
		"task": map[string]any{"title": "拉周报数据"}}); err != nil {
		t.Fatal(err)
	}
	id := onlyTodo(s).ID
	s.autoLinkTodos("dev", []proto.Session{{SessionID: "worker01", MachineID: "dev", Status: proto.StatusWorking,
		SpawnFrom: id + "@box/sec00001"}})
	todo := onlyTodo(s)
	if len(todo.Agents) != 2 || todo.Agents[1].SessionID != "worker01" || todo.Agents[1].Source != "spawned" {
		t.Fatalf("agents: %+v", todo.Agents)
	}
}
