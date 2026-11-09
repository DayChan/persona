package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The hub's end-to-end tests of tasks (real todos, a real node) are hub/tasks_test.go; these need the assistant's own
// state or timing.

// newTaskService is an assistant on a fake hub with machine box, whose task agents are followed at once.
func newTaskService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	return newTaskServiceOn(t, nil)
}

// newTaskServiceOn is newTaskService with st as its database (nil: none).
func newTaskServiceOn(t *testing.T, st Store) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub()
	f.sessions["box"] = nil
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	if st == nil {
		return newStorelessService(t, f), f
	}
	return newServiceOn(t, f, st), f
}

func planArgs(readonly bool) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
		"title": "修登录崩溃", "prompt": "iPad 横屏时登录页崩溃，修好并补测试", "machine": "box", "cwd": "/code/app",
		"readonly": readonly, "why": "这个仓库最近在 box 上", "permission": "acceptEdits",
	}}})
	return raw
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func feedKinds(s *Service, conv string) string {
	items, _, _ := s.feed(context.Background(), conv, time.Time{})
	var out []string
	for _, it := range items {
		out = append(out, it.Kind)
	}
	return strings.Join(out, ",")
}

// startedTask opens a read-only task from conv1 and waits for its agent (s1 on box) to be linked.
func startedTask(t *testing.T, s *Service, f *fakeHub) Todo {
	t.Helper()
	if _, err := s.toolTaskPlan(context.Background(), "conv1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	return onlyTodo(s)
}

// A dropped link is retried once: the machine back by then, the task starts as usual; still down, the task ends.
func TestTaskStartRetriesADroppedLink(t *testing.T) {
	s, f := newTaskService(t)
	s.taskRetry = 300 * time.Millisecond
	f.offline["box"] = true // offline when the task starts
	go func() {
		time.Sleep(100 * time.Millisecond)
		f.mu.Lock()
		f.offline["box"] = false
		f.mu.Unlock()
	}()
	if _, err := s.toolTaskPlan(context.Background(), "conv1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the retried start to link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	if todo := onlyTodo(s); todo.DoneAt != nil || todo.StartError != "" {
		t.Fatalf("todo = %+v", todo)
	}
	if len(f.starts) != 1 {
		t.Fatalf("%d starts", len(f.starts))
	}

	s2, f2 := newTaskService(t)
	s2.taskRetry = 300 * time.Millisecond
	f2.offline["box"] = true // and it stays down
	if _, err := s2.toolTaskPlan(context.Background(), "conv1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the todo to end", func() bool { return onlyTodo(s2).DoneAt != nil })
	if todo := onlyTodo(s2); !strings.Contains(todo.StartError, "offline") || len(todo.Agents) != 0 {
		t.Fatalf("todo = %+v", todo)
	}
	waitFor(t, "the receipt", func() bool { return strings.Contains(feedKinds(s2, "conv1"), "receipt") })
	if len(f2.starts) != 0 {
		t.Fatalf("%d starts reached a machine that was down", len(f2.starts))
	}
}

func TestTransientStartErr(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{rpc.ErrClosed, true},
		{fmt.Errorf("box: %w", apierr.ErrOffline), true},
		{fmt.Errorf("wrapped: %w", rpc.ErrClosed), true},
		{&rpc.Error{Message: "rpc: connection closed"}, true}, // a link further on dropped
		{&rpc.Error{Code: proto.CodeTerminalNotReady, Message: "pane_send_failed: …"}, true},
		{&rpc.Error{Message: "session.start: no such launcher"}, false},
		{errors.New("launcher not found"), false},
	} {
		if got := transientStartErr(c.err); got != c.want {
			t.Errorf("transientStartErr(%v) = %v", c.err, got)
		}
	}
}

// When the task's agent ends a turn and waits for nothing, the conversation gets one receipt with the agent's last
// words and its PR; the next turn's end after more work gets another.
func TestTaskReceipt(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	ctx := context.Background()
	turn := func(status, text string) {
		f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: status, LastText: text,
			UpdatedAt: time.Now()})
		if status == proto.StatusWorking {
			s.taskWorking(ctx, "box", "s1")
		} else {
			s.taskTurnEnded(ctx, "box", "s1")
		}
	}
	turn(proto.StatusWorking, "")
	turn(proto.StatusDone, "修好了，提了 https://github.com/o/r/pull/901")
	items, _, _ := s.feed(ctx, "conv1", time.Time{})
	var rb receiptBody
	_ = json.Unmarshal(items[len(items)-1].Body, &rb)
	if items[len(items)-1].Kind != feedReceipt || rb.State != "done" || rb.PR != "https://github.com/o/r/pull/901" || rb.Title != "修登录崩溃" {
		t.Fatalf("receipt = %+v", rb)
	}
	// The same stillness again is no new receipt.
	turn(proto.StatusIdle, "修好了")
	if n := strings.Count(feedKinds(s, "conv1"), "receipt"); n != 1 {
		t.Fatalf("%d receipts", n)
	}
	time.Sleep(5 * time.Millisecond)
	turn(proto.StatusWorking, "")
	s.tasks.mu.Lock()
	noted := s.tasks.working[todo.ID].After(s.tasks.receipted[todo.ID])
	s.tasks.mu.Unlock()
	if !noted {
		t.Fatal("the new work is not noted")
	}
	turn(proto.StatusDone, "又改了一处")
	if n := strings.Count(feedKinds(s, "conv1"), "receipt"); n != 2 {
		t.Fatalf("%d receipts after more work", n)
	}
}

// A turn that ends to wait for its background workflow is no end of the task (#2935): the hub reports the agent
// working (backgroundState) while the run goes on, so no receipt, nothing judged or closed; the turn its notification
// wakes is judged as usual.
func TestTaskReceiptWaitsForWorkflow(t *testing.T) {
	s, f := newTaskService(t)
	startedTask(t, s, f)
	ctx := context.Background()
	run := proto.Workflow{TaskID: "w1", RunID: "wf_1", Name: "parity-audit", Status: proto.WorkflowRunning, Running: true}
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking,
		State: proto.StateWorking, UpdatedAt: time.Now()})
	s.taskWorking(ctx, "box", "s1")
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateWorking, LastText: "审计在后台跑，等它的结果", UpdatedAt: time.Now(), Workflows: []proto.Workflow{run},
		Background: []proto.BackgroundTask{{ID: "w1", Kind: "workflow", RunID: "wf_1"}}})
	s.taskTurnEnded(ctx, "box", "s1")
	if n := strings.Count(feedKinds(s, "conv1"), "receipt"); n != 0 {
		t.Fatalf("%d receipts while the workflow runs", n)
	}
	run.Status, run.Running = proto.WorkflowCompleted, false
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateUnread, LastText: "审计做完了，清单在 #1", UpdatedAt: time.Now(), Workflows: []proto.Workflow{run}, WokeByTask: true})
	s.taskTurnEnded(ctx, "box", "s1")
	if n := strings.Count(feedKinds(s, "conv1"), "receipt"); n != 1 {
		t.Fatalf("%d receipts after the workflow", n)
	}
}

func TestResolutionAnswer(t *testing.T) {
	yes, no := true, false
	q := proto.Interaction{Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "怎么改？"}}}
	for _, c := range []struct {
		ix   proto.Interaction
		r    proto.Resolution
		want string
	}{
		{q, proto.Resolution{Answers: map[string]string{"怎么改？": "B"}}, "B"},
		{q, proto.Resolution{Dismiss: true}, "忽略了"},
		{proto.Interaction{Kind: proto.IxPlan}, proto.Resolution{Approve: &yes}, "批准了"},
		{proto.Interaction{Kind: proto.IxPermission}, proto.Resolution{Allow: &no, Message: "先别跑"}, "拒绝了：先别跑"},
	} {
		if got := resolutionAnswer("", c.ix, c.r); got != c.want {
			t.Errorf("resolutionAnswer(%+v) = %q, want %q", c.r, got, c.want)
		}
	}
	if got := interactionQuestion("", proto.Interaction{Kind: proto.IxPlan}); got != "批准计划" {
		t.Errorf("plan question = %q", got)
	}
}

// The "Today" card: one a day per conversation, at the start of the day, with what finished and what waits.
func TestTodayCard(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	ctx := context.Background()
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusBlocked,
		State: proto.StateNeedsYou, PendingInteractions: 1, UpdatedAt: time.Now()})
	if _, err := s.appendFeed(ctx, "conv1", feedReceipt, "todo_old", receiptBody{Title: "夜里的活", State: "done"}); err != nil {
		t.Fatal(err)
	}
	it, made, err := s.ensureToday(ctx, "conv1")
	if err != nil || !made || it.Kind != feedToday {
		t.Fatalf("today = %+v made=%v err=%v", it, made, err)
	}
	var body todayBody
	_ = json.Unmarshal(it.Body, &body)
	if len(body.Done) != 1 || body.Done[0].Title != "夜里的活" || body.Date != it.Ref {
		t.Fatalf("body = %+v", body)
	}
	if len(body.Waiting) != 1 || body.Waiting[0].ID != todo.ID {
		t.Fatalf("waiting = %+v (state %s)", body.Waiting, taskState(todo, s.taskAgents(ctx, todo)))
	}
	loc := s.location(ctx)
	now := time.Now().In(loc)
	if want := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc); !it.At.Equal(want) {
		t.Fatalf("at %v, want the start of the day %v", it.At, want)
	}
	again, made, err := s.ensureToday(ctx, "conv1")
	if err != nil || made || again.ID != it.ID {
		t.Fatalf("second call made another: %+v made=%v err=%v", again, made, err)
	}
}

// The "Today" card says what did not go through overnight (#4124): the conversation's tasks whose agent did not start and
// the scheduled runs that failed or were skipped since yesterday 18:00, newest first, each with a short reason.
func TestTodayCardListsWhatFailed(t *testing.T) {
	s, f := newTaskService(t)
	s.taskRetry = 300 * time.Millisecond
	ctx := context.Background()
	f.offline["box"] = true // the task's agent does not start
	if _, err := s.toolTaskPlan(ctx, "conv1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the task to fail", func() bool { return onlyTodo(s).StartError != "" })
	failed := onlyTodo(s)
	now := time.Now()
	sc := s.loadSchedule(ctx)
	sc.mu.Lock()
	sc.runs = []ScheduleRun{
		{ID: "run_old", RefID: "p", Title: "巡检", At: now.Add(-40 * time.Hour), Status: runFailed, Error: "前天的，不算"},
		{ID: "run_ok", RefID: "m", Title: "早报", At: now.Add(-2 * time.Hour), Status: runOK},
		{ID: "run_skip", RefID: "w", Title: "值守", At: now.Add(-90 * time.Minute), Status: runSkipped, Error: "Persona 停机，错过了时间"},
		{ID: "run_fail", RefID: "p", Title: "巡检", At: now.Add(-time.Hour), Status: runFailed,
			Error: "API Error: 503 upstream attempt budget is exhausted, " + strings.Repeat("重试也一样", 20)},
	}
	sc.mu.Unlock()

	it, _, err := s.ensureToday(ctx, "conv1")
	if err != nil {
		t.Fatal(err)
	}
	var body todayBody
	_ = json.Unmarshal(it.Body, &body)
	if body.FailedTotal != 3 || len(body.Failed) != 3 {
		t.Fatalf("failed: %d of %d, want 3 of 3 (the run before yesterday 18:00 and the one that worked left out)", len(body.Failed), body.FailedTotal)
	}
	for i := 1; i < len(body.Failed); i++ { // newest first
		if body.Failed[i].At.After(body.Failed[i-1].At) {
			t.Fatalf("not newest first: %+v", body.Failed)
		}
	}
	byID := map[string]todayFailed{}
	for _, x := range body.Failed {
		byID[x.Kind+"/"+x.ID] = x
	}
	if x, ok := byID["task/"+failed.ID]; !ok || !strings.Contains(x.Why, "offline") || x.At.IsZero() {
		t.Fatalf("the task that did not start: %+v", body.Failed)
	}
	if x := byID["routine/p"]; x.Ref != "run_fail" || x.Skipped || !strings.HasPrefix(x.Why, "API Error: 503") {
		t.Fatalf("the failed run: %+v", x)
	}
	if x := byID["routine/w"]; !x.Skipped || !strings.Contains(x.Why, "停") {
		t.Fatalf("the skipped run: %+v (%+v)", x, body.Failed)
	}
	for _, x := range body.Failed {
		if n := len([]rune(x.Why)); n > 61 {
			t.Fatalf("a reason is kept short: %d runes: %q", n, x.Why)
		}
	}

	// A run that started before 18:00 but ended (was recorded) after one that started later is still left out, and the
	// later one still counted: the history is in the order runs ended, not by At.
	// Then ten more overnight: the scheduled runs are the hub's, so another conversation's card shows them too (not
	// conv1's task), the newest eight and how many there are.
	sc.mu.Lock()
	sc.runs = append(sc.runs, ScheduleRun{ID: "run_late", RefID: "q", Title: "慢的", At: now.Add(-30 * time.Hour), Status: runFailed})
	for i := 0; i < 10; i++ {
		sc.runs = append(sc.runs, ScheduleRun{ID: "run_x", RefID: "x", Title: "值守", At: now.Add(-time.Duration(10-i) * time.Minute), Status: runFailed})
	}
	sc.mu.Unlock()
	it2, _, err := s.ensureToday(ctx, "conv2")
	if err != nil {
		t.Fatal(err)
	}
	var body2 todayBody
	_ = json.Unmarshal(it2.Body, &body2)
	if len(body2.Failed) != 8 || body2.FailedTotal != 12 {
		t.Fatalf("capped: %d of %d, want 8 of 12", len(body2.Failed), body2.FailedTotal)
	}
	if body2.Failed[0].At.Before(body2.Failed[7].At) {
		t.Fatalf("the newest eight, not the oldest: %+v", body2.Failed)
	}
}

// Kinds remembered with "Start these right away from now on" are listed and can be forgotten (Postgres: on the hub machine).
func TestTaskTrust(t *testing.T) {
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set (scripts/test/test-server.sh runs this on the hub machine)")
	}
	database, err := db.Open(context.Background(), url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.PutSetting(context.Background(), trustSetting, nil); err != nil {
		t.Fatal(err)
	}
	h := newServiceOn(t, newFakeHub(), database)
	h.trustKinds(context.Background(), []PlanTask{{Machine: "box", Cwd: "/code/app"}, {Machine: "box", Cwd: "/code/docs", Readonly: true}})
	list := h.trustList(context.Background())
	if len(list) != 2 || list[0].Key != "box|/code/app|edit" || list[1].Readonly != true {
		t.Fatalf("list = %+v", list)
	}
	if !h.planStartsAtOnce(context.Background(), []PlanTask{{Machine: "box", Cwd: "/code/app"}}) {
		t.Fatal("a trusted kind should start at once")
	}
	if ok, err := h.forgetTrust(context.Background(), "box|/code/app|edit"); !ok || err != nil {
		t.Fatalf("forget: %v %v", ok, err)
	}
	if h.planStartsAtOnce(context.Background(), []PlanTask{{Machine: "box", Cwd: "/code/app"}}) {
		t.Fatal("a forgotten kind still starts at once")
	}
	if ok, _ := h.forgetTrust(context.Background(), "nope"); ok {
		t.Fatal("forgot a kind that was never remembered")
	}
}

// A todo the user reopened is not closed again by the work before it, nor when that is not known (a receipt lost to
// a restart); a todo made by hand is never closed by the judge: in neither is the model asked.
func TestAutoFinishLeavesAReopenedTask(t *testing.T) {
	s, f := newTaskService(t)
	f.judge = `{"verdict":"done","reason":"做完了"}`
	reopened := time.Now().Add(-time.Minute)
	putTodo(s, Todo{ID: "todo_r", Title: "修", Origin: "conv1", ReopenedAt: &reopened, CreatedAt: reopened.Add(-time.Hour),
		Agents: []TodoAgent{{MachineID: "box", SessionID: "s9"}}})
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s9", Status: proto.StatusDone})
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "全部做完了，PR 已合并"})
	putTodo(s, Todo{ID: "todo_h", Title: "自己记的事", CreatedAt: reopened})
	s.autoFinishTask(todoNow(s, "todo_r"), reopened.Add(-time.Second), true)
	s.autoFinishTask(todoNow(s, "todo_r"), time.Time{}, false)
	s.autoFinishTask(todoNow(s, "todo_h"), time.Now(), true)
	if todoNow(s, "todo_r").DoneAt != nil || todoNow(s, "todo_h").DoneAt != nil || f.judged != 0 {
		t.Fatalf("closed: %+v %+v, %d judgments", todoNow(s, "todo_r"), todoNow(s, "todo_h"), f.judged)
	}
	// Work after the reopen is judged, and closes it.
	s.autoFinishTask(todoNow(s, "todo_r"), time.Now(), true)
	if got := todoNow(s, "todo_r"); got.DoneAt == nil || got.DoneBy != db.DoneByAuto || f.judged != 1 {
		t.Fatalf("not closed after new work: %+v, %d judgments", got, f.judged)
	}
}

// The agent of a task the judge closed is sent more (agent_send resumes it) and works again: the task is open again
// while it does, and the turn's end is followed as ever — cut off by the model service, an interrupted receipt; a
// finished one, a done receipt and the judge, which may close it again (#5892).
func TestClosedTaskReopensWhenItsAgentWorksAgain(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	ctx := context.Background()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "设计稿写好了", 0)
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "做完了", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	if _, ok := s.taskOf(ctx, "box", "s1"); ok {
		t.Fatal("a closed task still follows its agent")
	}
	time.Sleep(5 * time.Millisecond)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.ReopenedAt == nil || got.DoneBy != "" || got.DoneReason != "" {
		t.Fatalf("still closed while its agent works again: %+v", got)
	}
	if _, ok := s.taskOf(ctx, "box", "s1"); !ok {
		t.Fatal("the agent no longer counts for the task")
	}
	s.tasks.mu.Lock()
	noted := s.tasks.working[todo.ID].After(*got.ReopenedAt)
	s.tasks.mu.Unlock()
	if !noted {
		t.Fatal("the work is not noted after the reopen: the judge would not look at it")
	}

	lastReceipt := func() receiptBody {
		items, _, _ := s.feed(ctx, "conv1", time.Time{})
		var rb receiptBody
		_ = json.Unmarshal(items[len(items)-1].Body, &rb)
		if items[len(items)-1].Kind != feedReceipt {
			t.Fatalf("last feed item is %s, not a receipt", items[len(items)-1].Kind)
		}
		return rb
	}
	agentTurn(s, f, proto.StatusDone, "API Error: 503 upstream unavailable", 0)
	if rb := lastReceipt(); rb.State != "interrupted" {
		t.Fatalf("a cut-off turn of the reopened task: receipt = %+v", rb)
	}
	if todoNow(s, todo.ID).DoneAt != nil {
		t.Fatal("closed on a cut-off turn")
	}

	f.judge = `{"verdict":"done","reason":"做完了"}`
	time.Sleep(5 * time.Millisecond)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "总览出好了", 0)
	if rb := lastReceipt(); rb.State != "done" {
		t.Fatalf("a finished turn of the reopened task: receipt = %+v", rb)
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil || got.DoneBy != db.DoneByAuto {
		t.Fatalf("the work after the reopen is not judged: %+v", got)
	}
}

// Only a task the hub judged done or its supervisor accepted opens again, and only for an agent that still belongs to it: not
// a cancelled task or one the user closed (done by hand while its agent still started, #2954), not one whose start failed (that goes
// through Retry), not one the agent was released or dismissed from, and not when the agent works for an open todo now.
func TestWhichClosedTaskReopens(t *testing.T) {
	done := time.Now().Add(-time.Hour)
	released := done.Add(time.Minute)
	task := func(doneBy string, a TodoAgent) Todo {
		return Todo{ID: "todo_a", Title: "a", Origin: "conv1", CreatedAt: done, DoneAt: &done, DoneBy: doneBy, Agents: []TodoAgent{a}}
	}
	s1 := TodoAgent{MachineID: "box", SessionID: "s1"}
	failed := task(db.DoneByAuto, s1)
	failed.StartError = "no such launcher"
	accepted := task(db.DoneByReview, s1)
	accepted.Supervision = &Supervision{State: reviewAccepted}
	legacyCancelled := task(db.DoneByReview, s1)
	legacyCancelled.Supervision = &Supervision{State: reviewCancelled}
	for _, c := range []struct {
		name    string
		todos   []Todo
		reopens bool
	}{
		{"judged done", []Todo{task(db.DoneByAuto, s1)}, true},
		{"reviewed", []Todo{task(db.DoneByReview, s1)}, true},
		{"accepted", []Todo{accepted}, true},
		{"cancelled", []Todo{task(db.DoneByCancelled, s1)}, false},
		{"legacy cancelled", []Todo{legacyCancelled}, false},
		{"closed by the user", []Todo{task("", s1)}, false},
		{"start failed", []Todo{failed}, false},
		{"released", []Todo{task(db.DoneByAuto, TodoAgent{MachineID: "box", SessionID: "s1", ReleasedAt: &released})}, false},
		{"dismissed", []Todo{task(db.DoneByAuto, TodoAgent{MachineID: "box", SessionID: "s1", DismissedAt: &released})}, false},
		{"works for an open todo", []Todo{task(db.DoneByAuto, s1), {ID: "todo_h", Title: "自己记的事", CreatedAt: done, Agents: []TodoAgent{s1}}}, false},
		{"another agent", []Todo{task(db.DoneByAuto, TodoAgent{MachineID: "box", SessionID: "s2"})}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := newTaskService(t)
			for _, td := range c.todos {
				putTodo(s, td)
			}
			agentTurn(s, f, proto.StatusWorking, "", 0)
			got := todoNow(s, "todo_a")
			if reopened := got.DoneAt == nil && got.ReopenedAt != nil; reopened != c.reopens {
				t.Fatalf("reopened = %v, want %v: %+v", reopened, c.reopens, got)
			}
		})
	}
}

// A task's line in the snapshot follows its agent as the cards do (#1583): the step of one that has only run tools
// and said nothing, its words and step while it works, its words once it stopped, and its running subagents on top
// of its own progress, never instead of it.
func TestSnapshotTaskFollowsTheAgent(t *testing.T) {
	now := time.Date(2026, 11, 4, 11, 52, 0, 0, time.UTC)
	todo := Todo{ID: "todo_1", Title: "hub 重启无感"}
	started := now.Add(-3*time.Minute - 10*time.Second)
	lead := proto.Session{MachineID: "mac-a", SessionID: "b00502b8-82a3", State: proto.StateWorking, UpdatedAt: now,
		Activity: "在跑测试", ActivityAt: &started}

	got := snapshotTask(todo, "working", []proto.Session{lead}, now)
	if got.Last != "" || got.Doing != "在跑测试" || got.DoingFor != "3 分钟" || got.Session != "b00502b8" {
		t.Fatalf("tools only, no words: %+v", got)
	}

	lead.LastText = "已建 issue 并认领，\n两个只读子任务在调查"
	lead.Subagents = []proto.Subagent{
		{ID: "a1", Description: "Hub children lifecycle survey", Running: true, LastText: "子任务的话"},
		{ID: "a2", Description: "Client reconnect survey", Running: true},
		{ID: "a3", Description: "早就做完的", Running: false},
	}
	got = snapshotTask(todo, "working", []proto.Session{lead}, now)
	if got.Last != "已建 issue 并认领， 两个只读子任务在调查" || got.Doing != "在跑测试" ||
		!reflect.DeepEqual(got.Subagents, []string{"Hub children lifecycle survey", "Client reconnect survey"}) {
		t.Fatalf("with subagents: %+v", got)
	}
	golden(t, "assistant_snapshot.tasks", prompts.RenderSnapshot(prompts.Snapshot{Now: now, Zone: "UTC",
		Tasks: []prompts.SnapshotTask{
			snapshotTask(Todo{ID: "todo_2", Title: "只跑工具"}, "working", []proto.Session{{MachineID: "mac-a",
				SessionID: "c1", State: proto.StateWorking, UpdatedAt: now, Activity: "在读 tasks.go", ActivityAt: &started}}, now),
			got,
		}}))
	// #2935: a run in the background shows on its task and in its own line: the turn ended to wait for it.
	wfLead := proto.Session{MachineID: "mac-a", SessionID: "d4a7f6ff-4a9b", State: proto.StateWorking, UpdatedAt: now,
		LastText: "审计在后台跑，等结果", Workflows: []proto.Workflow{
			{TaskID: "w1", Name: "parity-audit", Running: true, Status: proto.WorkflowRunning, Phases: []string{"Audit", "Verify"},
				Phase: "Verify", PhaseIndex: 2, AgentsStarted: 185, AgentsDone: 169, StartedAt: now.Add(-14 * time.Minute)},
			{TaskID: "w0", Name: "早就停了的", Status: proto.WorkflowKilled},
		}}
	wfTask := snapshotTask(Todo{ID: "todo_3", Title: "各端对齐审计"}, "working", []proto.Session{wfLead}, now)
	if len(wfTask.Workflows) != 1 || wfTask.Workflows[0].For != "14 分钟" {
		t.Fatalf("task workflows: %+v", wfTask.Workflows)
	}
	running := runningWorkflows(wfLead, now)
	running[0].Agent, running[0].Machine, running[0].Session = "各端对齐审计", "mac-a", "d4a7f6ff"
	golden(t, "assistant_snapshot.workflows", prompts.RenderSnapshot(prompts.Snapshot{Now: now, Zone: "UTC",
		Working: []prompts.SnapshotAgent{{Label: "各端对齐审计"}}, Workflows: running, Tasks: []prompts.SnapshotTask{wfTask}}))
	linked := got
	linked.Link = todoLink("https://hub.example.com", "todo_1")
	golden(t, "assistant_snapshot.links", prompts.RenderSnapshot(prompts.Snapshot{Now: now, Zone: "UTC",
		Needs: []prompts.SnapshotAgent{{Label: "修 CI", Machine: "mac-a", Session: "b00502b8", Line: "要你批准推送",
			Link: sessionLink("https://hub.example.com", "mac-a", "b00502b8-82a3", false)}},
		Tasks: []prompts.SnapshotTask{linked},
		Unaccepted: []prompts.SnapshotAccept{{Title: "飞书机器人任务进展", Verdict: "✅ 做完了：已部署，按下面验收", Changed: "多了任务进展开关",
			Link: "https://hub.example.com/settings?tab=feishu", Since: now.Add(-24 * time.Hour)},
			{Title: "调研能不能直连", Verdict: "⛔ 做不了：对方接口不对外开放", Link: "https://hub.example.com/s/box/s1", Since: now}},
		UnacceptedTotal: 3}))
	// The calendar only carries the meetings still to come: a review in the evening must not read an empty one as a day
	// without meetings (#4596), so the line says what it leaves out.
	golden(t, "assistant_snapshot.calendar", prompts.RenderSnapshot(prompts.Snapshot{Now: now, Zone: "UTC",
		Calendar: &prompts.SnapshotCalendar{Events: []prompts.SnapshotEvent{
			{Start: now.Add(time.Hour), End: now.Add(90 * time.Minute), Title: "方案评审"},
			{Start: now.Add(3 * time.Hour), End: now.Add(4 * time.Hour), Title: "发布会"}}}}))

	lead.State, lead.Subagents = proto.StateUnread, nil
	if got = snapshotTask(todo, "unread", []proto.Session{lead}, now); got.Doing != "" || got.DoingFor != "" || got.Last == "" {
		t.Fatalf("stopped: its words, not the step it stopped on: %+v", got)
	}
}

// A task with several agents is shown with the one its state is about (#4192): "waiting for the user" names the agent that waits for
// the user and what it said, not another one that moved later; so do "interrupted", "done" and "in progress". Stopped: the latest.
func TestSnapshotTaskShowsTheAgentItsStateIsAbout(t *testing.T) {
	now := time.Date(2026, 11, 6, 21, 0, 0, 0, time.UTC)
	todo := Todo{ID: "todo_1", Title: "两台电脑一起改"}
	asking := proto.Session{MachineID: "mac-a", SessionID: "aaaa1111-0001", State: proto.StateNeedsYou, PendingInteractions: 1,
		UpdatedAt: now.Add(-10 * time.Minute), LastText: "要不要把旧接口一起删掉？"}
	cut := proto.Session{MachineID: "mac-a", SessionID: "cccc3333-0003", State: proto.StateIdle, TurnError: "API Error: 529 overloaded",
		UpdatedAt: now.Add(-8 * time.Minute), LastText: "API Error: 529 overloaded"}
	finished := proto.Session{MachineID: "mac-b", SessionID: "dddd4444-0004", State: proto.StateUnread,
		UpdatedAt: now.Add(-6 * time.Minute), LastText: "PR 开好了"}
	busy := proto.Session{MachineID: "mac-b", SessionID: "eeee5555-0005", State: proto.StateWorking,
		UpdatedAt: now.Add(-4 * time.Minute), LastText: "在跑测试"}
	seen := proto.Session{MachineID: "mac-b", SessionID: "bbbb2222-0002", State: proto.StateIdle,
		UpdatedAt: now.Add(-time.Minute), LastText: "已经提交了"}
	// Waits for the user too, longer: the newer of the two stands for the task.
	older := proto.Session{MachineID: "mac-a", SessionID: "ffff6666-0006", State: proto.StateNeedsYou, PendingInteractions: 1,
		UpdatedAt: now.Add(-20 * time.Minute), LastText: "先确认一下分支"}

	for _, c := range []struct {
		name   string
		agents []proto.Session
		state  string
		want   proto.Session
	}{
		{"needs you", []proto.Session{asking, cut, finished, busy, seen}, "needs_you", asking},
		{"needs you, two waiting", []proto.Session{older, asking, seen}, "needs_you", asking},
		{"interrupted", []proto.Session{cut, finished, busy, seen}, "interrupted", cut},
		{"unread", []proto.Session{finished, busy, seen}, "unread", finished},
		{"working", []proto.Session{busy, seen}, "working", busy},
		{"stopped", []proto.Session{asking, seen}, "followup", seen},
	} {
		if c.state == "followup" { // the agents as they were, all stopped now
			c.agents[0].State, c.agents[0].PendingInteractions = proto.StateIdle, 0
		}
		if st := taskState(todo, c.agents); st != c.state {
			t.Fatalf("%s: state %s, want %s", c.name, st, c.state)
		}
		got := snapshotTask(todo, c.state, c.agents, now)
		if got.Session != short(c.want.SessionID) || got.Machine != c.want.MachineID || got.Last != c.want.LastText {
			t.Fatalf("%s: shown with %s/%s %q, want %s/%s %q", c.name, got.Machine, got.Session, got.Last,
				c.want.MachineID, short(c.want.SessionID), c.want.LastText)
		}
	}
}

// The snapshot's links are the web app's own routes (web/src/lib/router.ts), on the hub's public_url; none without
// one (#2194).
func TestSnapshotLinks(t *testing.T) {
	web := "https://hub.example.com"
	for _, c := range []struct{ got, want string }{
		{sessionLink(web, "mac-a", "b00502b8-82a3", false), web + "/s/mac-a/b00502b8-82a3"},
		{sessionLink(web, "mac a", "x/y", false), web + "/s/mac%20a/x%2Fy"},
		{sessionLink(web, "mac-a", "c1", true), web + "/persona/c1"},
		{todoLink(web, "todo_1"), web + "/todos?id=todo_1"},
		{todoLink(web, "a&b"), web + "/todos?id=a%26b"},
		{sessionLink("", "mac-a", "c1", false), ""},
		{todoLink("", "todo_1"), ""},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

// A task the user asked for themselves starts without the card's "Start" while start_requested_tasks is on (the
// default, #2559); one the assistant came up with still waits, and with the setting off the asked-for one waits too.
func TestTaskPlanStartsWhatTheUserAskedFor(t *testing.T) {
	plan := func(requested bool) json.RawMessage {
		raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{
			"title": "修登录崩溃", "prompt": "iPad 横屏时登录页崩溃，修好并补测试", "machine": "box", "cwd": "/code/app",
			"readonly": false, "user_requested": requested, "why": "这个仓库最近在 box 上",
		}}})
		return raw
	}

	s, f := newTaskService(t)
	if !s.startRequestedTasks(context.Background()) {
		t.Fatal("start_requested_tasks should be on by default")
	}
	res, err := s.toolTaskPlan(context.Background(), "conv1", plan(true))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "Queued 1 task(s) for dispatch without asking") {
		t.Fatalf("result = %q", res)
	}
	waitFor(t, "the agent to start", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.starts) == 1 })

	s2, f2 := newTaskService(t)
	if res, err := s2.toolTaskPlan(context.Background(), "conv1", plan(false)); err != nil || !strings.Contains(res, "nothing has started") {
		t.Fatalf("an inferred task: %q %v", res, err)
	}
	// One inferred task keeps the whole plan waiting.
	mixed, _ := json.Marshal(map[string]any{"tasks": []map[string]any{
		{"title": "修登录崩溃", "prompt": "修", "machine": "box", "cwd": "/code/app", "user_requested": true, "why": "-"},
		{"title": "顺便升级依赖", "prompt": "升级", "machine": "box", "cwd": "/code/app", "user_requested": false, "why": "-"},
	}})
	if res, err := s2.toolTaskPlan(context.Background(), "conv1", mixed); err != nil || !strings.Contains(res, "nothing has started") {
		t.Fatalf("a mixed plan: %q %v", res, err)
	}

	setSettings(t, s2, func(v *Settings) { off := false; v.StartRequestedTasks = &off })
	if res, err := s2.toolTaskPlan(context.Background(), "conv1", plan(true)); err != nil || !strings.Contains(res, "nothing has started") {
		t.Fatalf("with the setting off: %q %v", res, err)
	}
	time.Sleep(50 * time.Millisecond)
	f2.mu.Lock()
	defer f2.mu.Unlock()
	if len(f2.starts) != 0 {
		t.Fatalf("%d agents started without the user", len(f2.starts))
	}
}

// A task the user finishes while its agent is still starting (a busy machine takes minutes, #1396) stays done: no
// start error over it, the agent that did start stays in the task — so the close loop (tasks_close.go) retires it
// instead of it working for no task at all (#2954).
func TestTaskFinishedWhileItsAgentStarts(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	todo, err := s.createTodo(ctx, "修登录崩溃", "修好并补测试", "conv1", "plan_x")
	if err != nil {
		t.Fatal(err)
	}
	// "Done", tapped while the start is on its way: PATCH /api/v1/todos/{id} {"done":true}.
	done := time.Now().UTC()
	if _, ok := s.applyTodoRow(ctx, todo.ID, db.TodoRow{ID: todo.ID, Title: todo.Title, Notes: todo.Notes,
		CreatedAt: todo.CreatedAt, UpdatedAt: done, DoneAt: &done}, nil); !ok {
		t.Fatal("applyTodoRow")
	}
	// ... and the start finishes after it.
	s.startPlanTask("plan_x", "conv1", 0, PlanTask{Title: todo.Title, Prompt: "修好并补测试", Machine: "box",
		Cwd: "/code/app", TodoID: todo.ID}, false)

	f.mu.Lock()
	starts := len(f.starts)
	f.mu.Unlock()
	if starts != 1 {
		t.Fatalf("session.start calls = %d", starts)
	}
	got := todoNow(s, todo.ID)
	if got.DoneAt == nil || !got.DoneAt.Equal(done) {
		t.Fatalf("the task the user finished is not done any more: %v", got.DoneAt)
	}
	if got.StartError != "" {
		t.Fatalf("the task the user finished shows a start error: %q", got.StartError)
	}
	if st := taskState(got, nil); st != "done" {
		t.Fatalf("the task the user finished reads as %q", st)
	}
	if len(got.Agents) != 1 || got.Agents[0].SessionID != "s1" || got.Agents[0].MachineID != "box" || got.Agents[0].ReleasedAt != nil {
		t.Fatalf("the agent that started is not kept in the task: %+v", got.Agents)
	}

	// "Retry" would reopen the task the user finished: it is not a failed start, so it is refused.
	if _, err := s.retryTask(ctx, todo.ID); !errors.Is(err, errTaskNotRetryable) {
		t.Fatalf("retry answered %v", err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil {
		t.Fatalf("retry reopened the task the user finished")
	}

	// The agent that started is a done task's agent: once still, the close loop retires it.
	old := taskCloseIdle
	taskCloseIdle = 0
	t.Cleanup(func() { taskCloseIdle = old })
	f.setSessions("box", idleAgent(time.Now().Add(-time.Hour)))
	s.closeDoneTaskAgents(ctx, taskCloseRefused{})
	if got := f.retires(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("the agent that started is not retired: %v", got)
	}
}

// A task that ended several stretches of work overnight has several receipts: the "Today" card lists it once, by its
// latest receipt, and not as done when that one says it was cut off afterwards.
func TestTodayCardListsEachTaskOnce(t *testing.T) {
	s, _ := newTaskService(t)
	ctx := context.Background()
	for _, r := range []struct {
		ref   string
		state string
	}{{"todo_a", "done"}, {"todo_a", "done"}, {"todo_b", "done"}, {"todo_b", "interrupted"}, {"todo_c", "done"}} {
		if _, err := s.appendFeed(ctx, "conv1", feedReceipt, r.ref, receiptBody{Title: r.ref, State: r.state}); err != nil {
			t.Fatal(err)
		}
	}
	it, _, err := s.ensureToday(ctx, "conv1")
	if err != nil {
		t.Fatal(err)
	}
	var body todayBody
	_ = json.Unmarshal(it.Body, &body)
	var ids []string
	for _, d := range body.Done {
		ids = append(ids, d.ID)
	}
	if strings.Join(ids, ",") != "todo_a,todo_c" {
		t.Fatalf("done = %v, want each finished task once (todo_b was cut off after)", ids)
	}
}

// A task whose agent stopped with the work unfinished has a done receipt but an open todo: not listed as done (#6153).
func TestTodayCardListsOnlyFinishedTasks(t *testing.T) {
	s, f := newTaskService(t)
	open := startedTask(t, s, f)
	ctx := context.Background()
	if _, err := s.appendFeed(ctx, "conv1", feedReceipt, open.ID, receiptBody{Title: open.Title, State: "done"}); err != nil {
		t.Fatal(err)
	}
	it, _, err := s.ensureToday(ctx, "conv1")
	if err != nil {
		t.Fatal(err)
	}
	var body todayBody
	_ = json.Unmarshal(it.Body, &body)
	if len(body.Done) != 0 {
		t.Fatalf("done = %+v, want nothing: the todo is still open", body.Done)
	}
}

// A start that uses up all its time (a busy machine's shell never ran claude) still ends the task: recording the
// failure gets its own time. On the start's spent context the store refused the write and the task showed "starting" with
// no agent for ever (#4510).
func TestTaskStartTimingOutStillEndsTheTask(t *testing.T) {
	old := taskStartTimeout
	taskStartTimeout = 300 * time.Millisecond
	t.Cleanup(func() { taskStartTimeout = old })
	todoStores(t, func(t *testing.T, _ *todoEnv, database *db.DB) {
		ctx := context.Background()
		f := newFakeHub()
		f.sessions["box"] = nil
		f.settings = &Settings{Timezone: "Asia/Shanghai"}
		f.slowStart = true
		s := newStorelessService(t, f)
		if database != nil {
			s = newServiceOn(t, f, database)
		}
		todo, err := s.createTodo(ctx, "清理旧名字", "全仓清理", "conv1", "plan_x")
		if err != nil {
			t.Fatal(err)
		}
		s.startPlanTask("plan_x", "conv1", 0, PlanTask{Title: todo.Title, Prompt: "全仓清理", Machine: "box", Cwd: "/code/app",
			TodoID: todo.ID}, false)
		got := todoNow(s, todo.ID)
		if got.DoneAt == nil || !strings.Contains(got.StartError, "deadline") {
			t.Fatalf("the timed-out start left the task open: done %v, error %q", got.DoneAt, got.StartError)
		}
		if database != nil {
			row, err := database.GetTodo(ctx, todo.ID)
			if err != nil || row == nil || row.DoneAt == nil || row.StartError == "" {
				t.Fatalf("the store did not record the failed start: %+v %v", row, err)
			}
		}
	})
}

// A plan card in the main conversation waiting for the user's "Start" has no todo yet, so nothing else in the snapshot
// shows it: the snapshot lists it, so the morning brief brings it up (#4453). A started plan, and one from another
// conversation, are not listed.
func TestSnapshotListsPlansWaitingToStart(t *testing.T) {
	s, f := newTaskService(t)
	ctx := context.Background()
	setSettings(t, s, func(v *Settings) { v.Main, v.MachineID = "main1", "box" })
	_ = f
	if _, err := s.toolTaskPlan(ctx, "main1", planArgs(false)); err != nil { // edits code: waits for "Start"
		t.Fatal(err)
	}
	if _, err := s.toolTaskPlan(ctx, "conv1", planArgs(false)); err != nil { // another conversation's
		t.Fatal(err)
	}
	loc := s.location(ctx)
	plans, total := s.snapshotPlans(ctx, loc, time.Now())
	if total != 1 || len(plans) != 1 || plans[0].Tasks != 1 || len(plans[0].Titles) != 1 || plans[0].Titles[0] != "修登录崩溃" {
		t.Fatalf("plans waiting: %+v (%d)", plans, total)
	}
	text := prompts.RenderSnapshot(prompts.Snapshot{Now: time.Now().In(loc), Zone: loc.String(), Plans: plans, PlansTotal: total})
	if !strings.Contains(text, "等用户点「开始」的计划") || !strings.Contains(text, "修登录崩溃") {
		t.Fatalf("snapshot: %s", text)
	}
	// Started: it is a task now, no longer a card waiting.
	items, ps, _ := s.feed(ctx, "main1", time.Time{})
	for _, it := range items {
		if it.Kind == feedPlan {
			if _, err := s.startPlan(ctx, ps[it.Ref].ID, nil, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	if plans, total := s.snapshotPlans(ctx, loc, time.Now()); total != 0 || len(plans) != 0 {
		t.Fatalf("a started plan is not waiting: %+v", plans)
	}
}

// A task opened from a side conversation (neither quiet nor supervised) pushes a failure notification
// when its agent cannot start, with reason and next steps, unless muted (#5081).
func TestSideConversationTaskFailedToStartPushes(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	s.taskRetry = 10 * time.Millisecond
	f.hello.Assistant.MachineID = "box"
	f.hello.Assistant.Machines = []string{"box"}
	setSettings(t, s, func(v *Settings) { v.Main, v.MachineID = "main1", "box" })
	f.failStart = errors.New("Failed to authenticate")

	// 1. Normal failure push from side conversation.
	if _, err := s.toolTaskPlan(ctx, "side1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the task to fail", func() bool { return onlyTodo(s).StartError != "" })
	waitFor(t, "push to arrive", func() bool { return len(f.pushed()) == 1 })

	pushed := f.pushed()
	if pushed[0].SessionID != "side1" {
		t.Fatalf("SessionID = %q, want side1", pushed[0].SessionID)
	}
	if pushed[0].Title != "「修登录崩溃」没开起来" {
		t.Fatalf("Title = %q", pushed[0].Title)
	}
	if !strings.Contains(pushed[0].Body, "Failed to authenticate") || !strings.Contains(pushed[0].Body, "打开对话点『重试』，或换一台电脑再派") {
		t.Fatalf("Body = %q", pushed[0].Body)
	}

	// 2. Muted side conversation pushes nothing.
	s2, f2 := newTaskService(t)
	f2.hello.Assistant.MachineID = "box"
	f2.hello.Assistant.Machines = []string{"box"}
	setSettings(t, s2, func(v *Settings) { v.Main, v.MachineID = "main1", "box" })
	f2.failStart = errors.New("Failed to authenticate")
	s2.fleet.mutesMu.Lock()
	s2.fleet.mutes = &Pins{Sessions: []string{"box/side_muted"}}
	s2.fleet.mutesMu.Unlock()
	if _, err := s2.toolTaskPlan(ctx, "side_muted", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the task to fail", func() bool { return onlyTodo(s2).StartError != "" })
	time.Sleep(100 * time.Millisecond)
	if len(f2.pushed()) != 0 {
		t.Fatalf("muted conversation pushed: %+v", f2.pushed())
	}

	// 3. Health space conversation omits task title.
	s3, f3 := newTaskService(t)
	f3.hello.Assistant.MachineID = "box"
	f3.hello.Assistant.Machines = []string{"box"}
	setSettings(t, s3, func(v *Settings) { v.Main, v.MachineID = "main1", "box" })
	f3.setSessions("box", proto.Session{SessionID: "health1", Space: SpaceHealth})
	f3.failStart = errors.New("Failed to authenticate")
	if _, err := s3.toolTaskPlan(ctx, "health1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	f3.mu.Lock()
	f3.pushes = nil
	f3.mu.Unlock()

	items, ps, _ := s3.feed(ctx, "health1", time.Time{})
	for _, it := range items {
		if it.Kind == feedPlan {
			if _, err := s3.startPlan(ctx, ps[it.Ref].ID, nil, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	waitFor(t, "the task to fail", func() bool { return onlyTodo(s3).StartError != "" })
	waitFor(t, "push to arrive", func() bool { return len(f3.pushed()) == 1 })
	hpushed := f3.pushed()
	if hpushed[0].Title != "任务没开起来" || strings.Contains(hpushed[0].Title, "修登录崩溃") {
		t.Fatalf("health Title = %q, want no task name", hpushed[0].Title)
	}
}

// #5302: an agent's own task list is the task's progress in the snapshot, and the agents' too; a report to the
// supervisor newer than the plan stands alone.
func TestSnapshotShowsTheAgentsPlan(t *testing.T) {
	now := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	planAt := now.Add(-10 * time.Minute)
	lead := proto.Session{MachineID: "mac-a", SessionID: "e1f2a3b4-1", State: proto.StateWorking, UpdatedAt: now,
		LastText: "先复现", Plan: &proto.PlanInfo{Done: 3, Total: 5, Current: "Reproducing the crash in landscape", At: planAt}}
	planned := snapshotTask(Todo{ID: "todo_1", Title: "修横屏崩溃"}, "working", []proto.Session{lead}, now)
	if p := planned.Plan; p == nil || p.Done != 3 || p.Total != 5 || p.Current != "Reproducing the crash in landscape" {
		t.Fatalf("task plan: %+v", p)
	}
	older := planned
	older.ID, older.Title = "todo_4", "修横屏布局"
	reported(&older, &TaskProgress{Kind: "progress", Text: "在找视图", At: planAt.Add(-time.Minute)}, lead, now)
	if older.Plan == nil || older.Reported == nil {
		t.Fatalf("an older report hides the plan: %+v", older)
	}
	newer := snapshotTask(Todo{ID: "todo_2", Title: "修竖屏"}, "working", []proto.Session{lead}, now)
	reported(&newer, &TaskProgress{Kind: "progress", Step: 4, Steps: 5, Text: "崩溃复现了，在改", At: now.Add(-2 * time.Minute)}, lead, now)
	if newer.Plan != nil || newer.Reported == nil {
		t.Fatalf("a newer report stands alone: %+v", newer)
	}
	none := snapshotTask(Todo{ID: "todo_3", Title: "没有计划"}, "working", []proto.Session{{MachineID: "mac-a", SessionID: "c1",
		State: proto.StateWorking, UpdatedAt: now, LastText: "在看"}}, now)
	if none.Plan != nil {
		t.Fatalf("no plan: %+v", none.Plan)
	}
	idle := snapshotPlan(proto.Session{Plan: &proto.PlanInfo{Done: 2, Total: 2, At: planAt}})
	golden(t, "assistant_snapshot.plan", prompts.RenderSnapshot(prompts.Snapshot{Now: now, Zone: "UTC",
		Unread:  []prompts.SnapshotAgent{{Label: "整理文档", Machine: "mac-a", Session: "a9b8c7d6", Plan: idle}},
		Working: []prompts.SnapshotAgent{{Label: "修横屏崩溃", Plan: planned.Plan}, {Label: "没有计划"}},
		Tasks:   []prompts.SnapshotTask{planned, older, newer, none}}))
}
