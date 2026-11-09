package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Questions an Agent leaves for the next briefing (later.go, #6287). All ids are placeholders.

// laterTask is a task of conv1 that conv1 supervises, with the agent s1abcdef on box working for it. The service has the
// default morning briefing (enabled, facts on).
func laterTask(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietService(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "迁移数据库", "x", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"迁移完成"}, 0)
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1abcdef", "迁移数据库", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	return s, f, todo
}

// askSupervisor is the agent's call of report_to_supervisor with these arguments.
func askSupervisor(s *Service, args string) proto.ToolResult {
	return s.agentTool(context.Background(), "box", "s1abcdef", "report_to_supervisor", json.RawMessage(args))
}

// reportItems are the report items in conv1's feed, with their bodies decoded.
func reportItems(t *testing.T, s *Service) []reportBody {
	t.Helper()
	items, _, err := s.feed(context.Background(), "conv1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []reportBody
	for _, it := range items {
		if it.Kind != feedReport {
			continue
		}
		var b reportBody
		if err := json.Unmarshal(it.Body, &b); err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func pendingSignals(s *Service) int {
	s.supervise.mu.Lock()
	defer s.supervise.mu.Unlock()
	return len(s.supervise.pending)
}

// A question with urgent false wakes nobody: no supervision signal, so no message into the main conversation, and the task
// is not marked as asking.
func TestLaterQuestionWakesNobody(t *testing.T) {
	s, f, todo := laterTask(t)
	res := askSupervisor(s, `{"kind":"question","text":"新接口用 snake_case 还是 camelCase？","urgent":false}`)
	if res.IsError || !strings.Contains(res.Content[0].Text, "Queued") {
		t.Fatalf("report: %+v", res)
	}
	if n := pendingSignals(s); n != 0 {
		t.Fatalf("%d supervision signals", n)
	}
	time.Sleep(4 * shortSuperviseTimings.gather) // a signal would have gone out by now
	if n := len(f.notices()); n != 0 {
		t.Fatalf("%d messages into the main conversation: %q", n, f.notices())
	}
	if p := todoNow(s, todo.ID).Supervision.Progress; p != nil {
		t.Fatalf("the task shows %+v as its agent's last report", p)
	}
}

// It is kept in the queue (who asked, for which to-do, what, when) and shown in the supervisor's conversation as a report
// marked as one that can wait, with its evidence.
func TestLaterQuestionIsQueuedAndShownInTheChat(t *testing.T) {
	s, _, todo := laterTask(t)
	before := time.Now()
	askSupervisor(s, `{"kind":"question","text":"新接口用 snake_case 还是 camelCase？","evidence":["https://github.com/o/r/pull/9"],"urgent":false}`)
	open := s.laterOpen(context.Background(), time.Now())
	if len(open) != 1 {
		t.Fatalf("queue: %+v", open)
	}
	a := open[0]
	if a.TodoID != todo.ID || a.MachineID != "box" || a.SessionID != "s1abcdef" || a.Text != "新接口用 snake_case 还是 camelCase？" ||
		a.AskedAt.Before(before.UTC().Add(-time.Second)) || !strings.HasPrefix(a.ID, "lt_") {
		t.Fatalf("entry: %+v", a)
	}
	items := reportItems(t, s)
	if len(items) != 1 || !items[0].Later || items[0].Kind != "question" || items[0].Text != a.Text ||
		len(items[0].Evidence) != 1 {
		t.Fatalf("report items: %+v", items)
	}
}

// The queue is in settings: a hub that starts again has it.
func TestLaterQueueSurvivesARestart(t *testing.T) {
	e := newBriefEnv(t)
	e.s.todos.todos["todo_a"] = &Todo{ID: "todo_a", Title: "迁移数据库", Origin: "conv1"}
	ctx := context.Background()
	if _, ok, _ := e.s.laterQueue(ctx, "todo_a", "box", "ag1", "要不要先停写？", e.clk.Now()); !ok {
		t.Fatal("not queued")
	}
	again := newBoardEnvOn(t, e.st) // the same database, a new process
	again.s.todos.todos["todo_a"] = &Todo{ID: "todo_a", Title: "迁移数据库", Origin: "conv1"}
	open := again.s.laterOpen(ctx, again.clk.Now())
	if len(open) != 1 || open[0].Text != "要不要先停写？" || open[0].SessionID != "ag1" {
		t.Fatalf("after the restart: %+v", open)
	}
}

// urgent missing or true is the report as ever: a supervision signal, nothing in the queue, and the report is not marked.
func TestUrgentOrMissingChangesNothing(t *testing.T) {
	for _, args := range []string{`{"kind":"question","text":"要不要先停写？"}`, `{"kind":"question","text":"要不要先停写？","urgent":true}`} {
		s, f, _ := laterTask(t)
		res := askSupervisor(s, args)
		if res.IsError || !strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
			t.Fatalf("%s: %+v", args, res)
		}
		waitFor(t, "the message", func() bool { return len(f.notices()) == 1 })
		if n := len(s.laterOpen(context.Background(), time.Now())); n != 0 {
			t.Fatalf("%s: %d in the queue", args, n)
		}
		if items := reportItems(t, s); len(items) != 1 || items[0].Later {
			t.Fatalf("%s: report items %+v", args, items)
		}
	}
}

// Nothing shows the queue unless a morning or evening routine would run with facts on: then urgent false is ignored, the
// report goes as an urgent one, and the answer says so first.
func TestLaterNeedsABriefingThatListsIt(t *testing.T) {
	routine := func(id string, enabled, facts bool, repeat string) Task {
		return Task{ID: id, Title: id, Prompt: "p", Repeat: repeat, Time: "09:00", Enabled: enabled, Facts: facts}
	}
	for _, c := range []struct {
		name   string
		tasks  []Task
		queued bool
	}{
		{"the morning briefing", []Task{routine("morning", true, true, repeatDaily)}, true},
		{"only the evening review", []Task{routine("morning", false, true, repeatDaily), routine("evening", true, true, repeatDaily)}, true},
		{"both switched off", []Task{routine("morning", false, true, repeatDaily), routine("evening", false, true, repeatDaily)}, false},
		{"a routine without facts", []Task{routine("morning", true, false, repeatDaily)}, false},
		{"no routine at all", nil, false},
		{"a task every half hour", []Task{{ID: "morning", Title: "m", Prompt: "p", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, Facts: true}}, false},
		{"a daily routine with no time of day", []Task{{ID: "morning", Title: "m", Prompt: "p", Repeat: repeatDaily, Enabled: true, Facts: true}}, false},
	} {
		s, f, _ := laterTask(t)
		setTasks(s, c.tasks...)
		res := askSupervisor(s, `{"kind":"question","text":"要不要先停写？","urgent":false}`)
		if res.IsError {
			t.Fatalf("%s: %+v", c.name, res)
		}
		text, queued := res.Content[0].Text, len(s.laterOpen(context.Background(), time.Now())) == 1
		if queued != c.queued {
			t.Errorf("%s: queued %v, want %v (%s)", c.name, queued, c.queued, text)
		}
		if c.queued {
			continue
		}
		if !strings.HasPrefix(text, "urgent:false was ignored") || !strings.Contains(text, "Sent to your supervisor") {
			t.Errorf("%s: the answer: %s", c.name, text)
		}
		waitFor(t, c.name+": the message", func() bool { return len(f.notices()) == 1 })
		if items := reportItems(t, s); len(items) != 1 || items[0].Later {
			t.Errorf("%s: report items %+v", c.name, items)
		}
	}
}

// urgent is for questions: on another kind it changes nothing, and the answer says it was not used.
func TestLaterIsForQuestionsOnly(t *testing.T) {
	s, f, _ := laterTask(t)
	res := askSupervisor(s, `{"kind":"blocked","text":"CI 挂了，等不到","urgent":false}`)
	if res.IsError || !strings.Contains(res.Content[0].Text, "urgent only applies to a question") || !strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
		t.Fatalf("answer: %+v", res)
	}
	waitFor(t, "the message", func() bool { return len(f.notices()) == 1 })
	if n := len(s.laterOpen(context.Background(), time.Now())); n != 0 {
		t.Fatalf("%d in the queue", n)
	}
}

// The queue holds laterMax questions: one more goes as an urgent question, and the answer says so.
func TestLaterQueueIsBounded(t *testing.T) {
	s, f, todo := laterTask(t)
	ctx := context.Background()
	for i := 0; i < laterMax; i++ {
		if _, ok, _ := s.laterQueue(ctx, todo.ID, "box", "s1abcdef", fmt.Sprintf("问题 %d", i), time.Now()); !ok {
			t.Fatalf("question %d was refused", i)
		}
	}
	res := askSupervisor(s, `{"kind":"question","text":"多出来的一个","urgent":false}`)
	if res.IsError || !strings.Contains(res.Content[0].Text, "could not take it") || !strings.Contains(res.Content[0].Text, "Sent to your supervisor") {
		t.Fatalf("answer: %+v", res)
	}
	waitFor(t, "the message", func() bool { return len(f.notices()) == 1 })
	if n := len(s.laterOpen(ctx, time.Now())); n != laterMax {
		t.Fatalf("%d in the queue", n)
	}
}

// A question older than 7 days is dropped, one a minute younger is kept; the queue is written again without it.
func TestLaterQuestionsExpireAfterSevenDays(t *testing.T) {
	e := newBriefEnv(t)
	e.s.todos.todos["todo_a"] = &Todo{ID: "todo_a", Title: "迁移数据库", Origin: "conv1"}
	ctx := context.Background()
	now := e.clk.Now()
	e.s.laterQueue(ctx, "todo_a", "box", "ag1", "旧的问题", now.Add(-laterKeep))
	e.s.laterQueue(ctx, "todo_a", "box", "ag1", "刚好没过期", now.Add(-laterKeep+time.Minute))
	open := e.s.laterOpen(ctx, now)
	if len(open) != 1 || open[0].Text != "刚好没过期" {
		t.Fatalf("queue: %+v", open)
	}
	var stored []laterAsk
	if _, err := e.st.Setting(ctx, laterKey, &stored); err != nil || len(stored) != 1 {
		t.Fatalf("stored: %+v %v", stored, err)
	}
}

// A question goes with its to-do: closed or deleted, it is dropped from the queue; the questions of a to-do still open stay.
func TestLaterQuestionsGoWithTheirTodo(t *testing.T) {
	e := newBriefEnv(t)
	ctx := context.Background()
	for _, id := range []string{"todo_open", "todo_closed", "todo_deleted"} {
		e.s.todos.todos[id] = &Todo{ID: id, Title: id, Origin: "conv1"}
		e.s.laterQueue(ctx, id, "box", "ag1", "问题 "+id, e.clk.Now())
	}
	done := e.clk.Now()
	e.s.todos.todos["todo_closed"].DoneAt = &done
	delete(e.s.todos.todos, "todo_deleted")
	open := e.s.laterOpen(ctx, e.clk.Now())
	if len(open) != 1 || open[0].TodoID != "todo_open" {
		t.Fatalf("queue: %+v", open)
	}
}

// Marking a question handled takes it out of the queue, and out of the stored one; again, it is not there.
func TestLaterHandledLeavesTheQueue(t *testing.T) {
	e := newBriefEnv(t)
	e.s.todos.todos["todo_a"] = &Todo{ID: "todo_a", Title: "迁移数据库", Origin: "conv1"}
	ctx := context.Background()
	first, _, _ := e.s.laterQueue(ctx, "todo_a", "box", "ag1", "第一个", e.clk.Now())
	e.s.laterQueue(ctx, "todo_a", "box", "ag1", "第二个", e.clk.Now().Add(time.Second))
	if ok, err := e.s.laterHandled(ctx, first.ID, e.clk.Now()); !ok || err != nil {
		t.Fatalf("not handled: %v", err)
	}
	for _, id := range []string{first.ID, "lt_unknown"} {
		if ok, err := e.s.laterHandled(ctx, id, e.clk.Now()); ok || err != nil {
			t.Fatalf("a question that is not in the queue (%s): %v %v", id, ok, err)
		}
	}
	open := e.s.laterOpen(ctx, e.clk.Now().Add(time.Minute))
	if len(open) != 1 || open[0].Text != "第二个" {
		t.Fatalf("queue: %+v", open)
	}
	var stored []laterAsk
	if _, err := e.st.Setting(ctx, laterKey, &stored); err != nil || len(stored) != 1 || stored[0].Text != "第二个" {
		t.Fatalf("stored: %+v %v", stored, err)
	}
}

// A Handled press whose change can't be written is an error, not "not there": the question stays, in memory and stored. A
// queue that can't be read (a fresh service on an unreadable store) is an error too.
func TestLaterHandledStoreFailureIsNotHandled(t *testing.T) {
	e := newBriefEnv(t)
	e.s.todos.todos["todo_a"] = &Todo{ID: "todo_a", Title: "迁移数据库", Origin: "conv1"}
	ctx := context.Background()
	a, _, _ := e.s.laterQueue(ctx, "todo_a", "box", "ag1", "要不要先停写？", e.clk.Now())

	e.st.failPut = errors.New("database unavailable")
	if ok, err := e.s.laterHandled(ctx, a.ID, e.clk.Now()); ok || err == nil {
		t.Fatalf("not written: %v %v, want an error", ok, err)
	}
	e.st.failPut = nil
	if open := e.s.laterOpen(ctx, e.clk.Now()); len(open) != 1 || open[0].ID != a.ID {
		t.Fatalf("queue: %+v", open)
	}
	var stored []laterAsk
	if _, err := e.st.Setting(ctx, laterKey, &stored); err != nil || len(stored) != 1 || stored[0].ID != a.ID {
		t.Fatalf("stored: %+v %v", stored, err)
	}

	fresh := newBoardEnvOn(t, e.st)
	e.st.failSetting = errors.New("database unavailable")
	if ok, err := fresh.s.laterHandled(ctx, a.ID, e.clk.Now()); ok || err == nil {
		t.Fatalf("not read: %v %v, want an error", ok, err)
	}
}

// The same question of the same Agent asked again while it is in the queue is not put in twice, and is not shown in the chat
// twice; the answer says it was there. A different question, or the same words from another Agent, is a new entry.
func TestLaterSameQuestionIsQueuedOnce(t *testing.T) {
	s, _, todo := laterTask(t)
	ctx := context.Background()
	first := askSupervisor(s, `{"kind":"question","text":"要不要先停写？","urgent":false}`)
	again := askSupervisor(s, `{"kind":"question","text":"要不要先停写？","urgent":false}`)
	if !strings.Contains(first.Content[0].Text, "Queued") || again.IsError || !strings.Contains(again.Content[0].Text, "Already queued") {
		t.Fatalf("answers: %+v / %+v", first, again)
	}
	if n := len(s.laterOpen(ctx, time.Now())); n != 1 {
		t.Fatalf("%d in the queue", n)
	}
	if items := reportItems(t, s); len(items) != 1 {
		t.Fatalf("%d report items", len(items))
	}
	askSupervisor(s, `{"kind":"question","text":"另一个问题","urgent":false}`)
	s.laterQueue(ctx, todo.ID, "box", "s2other", "要不要先停写？", time.Now())
	if n := len(s.laterOpen(ctx, time.Now())); n != 3 {
		t.Fatalf("%d in the queue, want 3", n)
	}
}

// A question that is over but was not looked at yet (7 days, its to-do ended) is gone for the Handled button too: nothing to
// mark.
func TestLaterHandledIsRefusedForAQuestionThatIsOver(t *testing.T) {
	e := newBriefEnv(t)
	ctx := context.Background()
	now := e.clk.Now()
	e.s.todos.todos["todo_a"] = &Todo{ID: "todo_a", Title: "迁移数据库", Origin: "conv1"}
	e.s.todos.todos["todo_b"] = &Todo{ID: "todo_b", Title: "改设置页", Origin: "conv1"}
	old, _, _ := e.s.laterQueue(ctx, "todo_a", "box", "ag1", "旧的问题", now.Add(-laterKeep-time.Minute))
	ended, _, _ := e.s.laterQueue(ctx, "todo_b", "box", "ag2", "待办结束了的问题", now)
	done := now
	e.s.todos.todos["todo_b"].DoneAt = &done
	for _, a := range []laterAsk{old, ended} {
		if ok, err := e.s.laterHandled(ctx, a.ID, now); ok || err != nil {
			t.Errorf("%q: %v %v", a.Text, ok, err)
		}
	}
}
