package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Closing a finished task (docs/assistant/todos.md "Auto-complete", #1092). Every test runs without a store (h.store nil: nothing
// reaches a database) and with a stubbed judge model.

// judgeStub answers the judge's calls with reply (status 200) or fails them (status ≠ 200), counting them and keeping
// the last input.
type judgeStub struct {
	triage, kb *ai.Config // the manager's triage and kb models, pointed at the stubs
	calls      atomic.Int32
	input      atomic.Value
}

func stubJudge(t *testing.T, h *Hub, status int, reply string) *judgeStub {
	t.Helper()
	js := &judgeStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		js.input.Store(string(raw))
		js.calls.Add(1)
		if status != http.StatusOK {
			http.Error(w, "upstream down", status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": reply}}})
	}))
	t.Cleanup(srv.Close)
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("k"), 0o600)
	// inbox.judge inherits "Triage" (inbox.triage) when nothing else is set, as in the inbox's own tests.
	triage := ai.Config{BaseURL: srv.URL, APIKeyFile: key, Model: "m"}
	// Naming inherits kb; its requests must not change the judge's count or input.
	naming := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": "example-task"}}})
	}))
	t.Cleanup(naming.Close)
	kbAI := ai.Config{BaseURL: naming.URL, APIKeyFile: key, Model: "m"}
	js.triage, js.kb = &triage, &kbAI
	// The manager's models (its manager.json): a connected one reads them again.
	setManagerJSON(t, h, "inbox", map[string]any{"triage_ai": triage})
	setManagerJSON(t, h, "kb", map[string]any{"ai": kbAI})
	return js
}

func TestJudgeStubSeparatesNamingCalls(t *testing.T) {
	h, _ := newTaskHub(t)
	js := stubJudge(t, h, http.StatusOK, `{"verdict":"done","reason":"Example completed task"}`)
	if _, err := ai.New(*js.kb).Complete(context.Background(), "Example naming system", "Example task", 200); err != nil {
		t.Fatal(err)
	}
	if js.calls.Load() != 0 {
		t.Fatal("a naming request was counted as a completion judgment")
	}
	var result struct{ Verdict string }
	if err := ai.New(*js.triage).JSON(context.Background(), "Example judgment system", "Example completed work", &result); err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "done" || js.calls.Load() != 1 {
		t.Fatalf("judgment result %q, calls %d", result.Verdict, js.calls.Load())
	}
	input := js.input.Load()
	probe, err := http.Get(js.triage.BaseURL)
	if err != nil {
		t.Fatal(err)
	}
	probe.Body.Close()
	if js.calls.Load() != 1 || js.input.Load() != input {
		t.Fatal("a non-model probe changed the completion judgment's count or input")
	}
	if _, err := ai.New(*js.kb).Complete(context.Background(), "Example naming system", "Another example task", 200); err != nil {
		t.Fatal(err)
	}
	if js.calls.Load() != 1 || js.input.Load() != input {
		t.Fatal("late naming changed the completion judgment's count or input")
	}
}

// startedTask opens a read-only task from conv1 and waits for its agent (s1 on box) to be linked.
func startedTask(t *testing.T, h *Hub) Todo {
	t.Helper()
	if _, err := taskPlan(h, "conv1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the link", func() bool { ts := taskTodos(h); return len(ts) == 1 && len(ts[0].Agents) == 1 })
	return taskTodos(h)[0]
}

// runTurn makes s1 work and then stop with text as its last words.
func runTurn(h *Hub, text string) {
	m := h.byID["box"]
	sess := func(status, text string) proto.Session {
		return proto.Session{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: status, LastText: text, UpdatedAt: time.Now()}
	}
	m.setSessions([]proto.Session{sess(proto.StatusWorking, "")})
	time.Sleep(20 * time.Millisecond) // taskWorking runs in its own goroutine
	m.setSessions([]proto.Session{sess(proto.StatusDone, text)})
}

func receipts(h *Hub) int { return strings.Count(strings.Join(feedKinds(h, "conv1"), ","), "receipt") }

// The judge finds the work finished: the todo is done by the hub, with why, and works like one the user closed (the
// agent no longer counts for it, the snapshot says it was closed by the hub).
func TestTaskAutoDone(t *testing.T) {
	h, _ := newTaskHub(t)
	js := stubJudge(t, h, http.StatusOK, `{"verdict":"done","reason":"已合并 PR 并发了 TestFlight"}`)
	todo := startedTask(t, h)
	runTurn(h, "修好了，PR https://github.com/o/r/pull/901 已合并，TestFlight 构建号 202611041200")
	waitFor(t, "auto done", func() bool { return todoNow(h, todo.ID).DoneAt != nil })
	got := todoNow(h, todo.ID)
	if got.DoneBy != db.DoneByAuto || got.DoneReason != "已合并 PR 并发了 TestFlight" || receipts(h) != 1 {
		t.Fatalf("todo = %+v, %d receipts", got, receipts(h))
	}
	in, _ := js.input.Load().(string)
	for _, want := range []string{"修登录崩溃", "iPad 横屏时登录页崩溃", "pull/901", "已合并的 PR", "TestFlight 构建号"} {
		if !strings.Contains(in, want) {
			t.Errorf("judge input lacks %q: %s", want, in)
		}
	}
	if _, active := h.todoLinkOf("box", "s1"); active {
		t.Fatal("the agent still counts for a done todo")
	}
	if _, ok := taskOfAgent(h, "box", "s1"); ok {
		t.Fatal("a done task is still followed")
	}
	if line := taskLine(h, todo); !strings.Contains(line, "）：已完成（hub 判断做完了，自动关的）") {
		t.Fatalf("the live context = %q", line)
	}
	for _, it := range inboxTasks(t, h) {
		if it.ID == todo.ID {
			t.Fatal("the inbox still links messages to a done task")
		}
	}
}

// Agents that stopped without finishing, the model failing or missing, and a verdict it made up: the todo stays open.
func TestTaskNotAutoDone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		reply  string
		last   string
	}{
		{"not finished", http.StatusOK, `{"verdict":"not_done","reason":"还差两处没改"}`, "没有全改完，剩下 iPad 的两处下次再说"},
		{"waiting", http.StatusOK, `{"verdict":"waiting","reason":"等 App 审核"}`, "已提交，等待审核"},
		{"needs the user", http.StatusOK, `{"verdict":"needs_user","reason":"要你选方案"}`, "两个方案你选一个"},
		{"made-up verdict", http.StatusOK, `{"verdict":"probably","reason":"大概好了"}`, "应该好了"},
		{"not JSON", http.StatusOK, `好像做完了`, "好了"},
		{"model down", http.StatusInternalServerError, "", "PR https://github.com/o/r/pull/9 已合并"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := newTaskHub(t)
			js := stubJudge(t, h, tc.status, tc.reply)
			todo := startedTask(t, h)
			runTurn(h, tc.last)
			waitFor(t, "the judge asked", func() bool { return js.calls.Load() > 0 })
			time.Sleep(50 * time.Millisecond)
			if got := todoNow(h, todo.ID); got.DoneAt != nil || got.DoneBy != "" {
				t.Fatalf("closed: %+v", got)
			}
			if receipts(h) != 1 {
				t.Fatalf("%d receipts", receipts(h))
			}
		})
	}
	t.Run("no model", func(t *testing.T) {
		h, _ := newTaskHub(t) // no judge configured at all
		todo := startedTask(t, h)
		runTurn(h, "PR https://github.com/o/r/pull/9 已合并")
		waitFor(t, "a receipt", func() bool { return receipts(h) == 1 })
		time.Sleep(50 * time.Millisecond)
		if got := todoNow(h, todo.ID); got.DoneAt != nil {
			t.Fatalf("closed without a model: %+v", got)
		}
	})
}

// A todo the user reopened is not closed again by the same work; after a new turn it is judged again.
func TestTaskReopenedNotAutoDoneAgain(t *testing.T) {
	h, _ := newTaskHub(t)
	js := stubJudge(t, h, http.StatusOK, `{"verdict":"done","reason":"做完了"}`)
	todo := startedTask(t, h)
	runTurn(h, "做完了")
	waitFor(t, "auto done", func() bool { return todoNow(h, todo.ID).DoneAt != nil })

	handler := h.routes()
	patch := func(body string) Todo {
		req := todoRequest("PATCH", "/api/v1/todos/"+todo.ID, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		var out struct {
			Todo Todo `json:"todo"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("PATCH %s: %d %s", body, w.Code, w.Body.String())
		}
		return out.Todo
	}
	re := patch(`{"done":false}`)
	if re.DoneAt != nil || re.DoneBy != "" || re.DoneReason != "" || re.ReopenedAt == nil {
		t.Fatalf("reopened = %+v", re)
	}
	if _, ok := taskOfAgent(h, "box", "s1"); !ok {
		t.Fatal("a reopened task is not followed again")
	}

	// The same stretch of work, judged again (a late turn end): left open, model not asked. (A receipt lost to a
	// restart is the assistant's own test, TestAutoFinishLeavesAReopenedTask.)
	calls := js.calls.Load()
	h.asst().TurnEnded(context.Background(), assistant.SessionRef{MachineID: "box", SessionID: "s1"})
	time.Sleep(50 * time.Millisecond)
	if got := todoNow(h, todo.ID); got.DoneAt != nil || js.calls.Load() != calls {
		t.Fatalf("closed again by the same work: %+v (%d calls)", got, js.calls.Load()-calls)
	}
	// (A judgment that began before the reopen and ends after it does not overrule the user either: the assistant's
	// TestAutoDoneKeepsTheUsersChange.)

	// The user sends it on; the agent works and stops again: judged again, and closed.
	time.Sleep(5 * time.Millisecond)
	runTurn(h, "又改了一处，全部做完了")
	waitFor(t, "auto done again", func() bool { return todoNow(h, todo.ID).DoneAt != nil })
	if got := todoNow(h, todo.ID); got.DoneBy != db.DoneByAuto {
		t.Fatalf("todo = %+v", got)
	}

	// Done by the user: the hub's mark goes.
	patch(`{"done":false}`)
	if done := patch(`{"done":true}`); done.DoneAt == nil || done.DoneBy != "" || done.DoneReason != "" {
		t.Fatalf("done by the user = %+v", done)
	}
}

// Only tasks opened from a conversation are closed by the hub; done-check judges without writing.
func TestTaskDoneSkipAndCheck(t *testing.T) {
	h, _ := newTaskHub(t)
	stubJudge(t, h, http.StatusOK, `{"verdict":"waiting","reason":"等审核"}`)
	todo := startedTask(t, h)
	h.byID["box"].setSessions([]proto.Session{{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle,
		LastText: "已提交，等待审核", UpdatedAt: time.Now()}})
	handler := h.routes()
	type doneCheck struct {
		Verdict    string `json:"verdict"`
		Reason     string `json:"reason"`
		WouldClose bool   `json:"would_close"`
		Skip       string `json:"skip"`
		Replies    int    `json:"replies"`
	}
	check := func(id string) (int, doneCheck) {
		req := todoRequest("POST", "/api/v1/todos/"+id+"/done-check", nil)
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		var c doneCheck
		_ = json.Unmarshal(w.Body.Bytes(), &c)
		return w.Code, c
	}
	code, c := check(todo.ID)
	if code != http.StatusOK || c.Verdict != "waiting" || c.Reason != "等审核" || c.WouldClose || c.Replies != 1 {
		t.Fatalf("done-check = %d %+v", code, c)
	}
	if got := todoNow(h, todo.ID); got.DoneAt != nil {
		t.Fatalf("done-check wrote: %+v", got)
	}
	if code, _ := check("todo_nope"); code != http.StatusNotFound {
		t.Fatalf("missing todo: %d", code)
	}

	// A todo made by hand: never closed by the hub, whatever the model says.
	stubJudge(t, h, http.StatusOK, `{"verdict":"done","reason":"做完了"}`)
	hand := plainTodo(t, h, "自己记的事")
	if _, err := h.asst().TodoLink(context.Background(), assistantwire.TodoLinkParams{TodoID: hand.ID, MachineID: "box", SessionID: "s2", Cwd: "/code/app"}); err != nil {
		t.Fatal(err)
	}
	// Its agent works and stops: no receipt, no judgment (it is no task of a conversation).
	m := h.byID["box"]
	s2 := func(status string) proto.Session {
		return proto.Session{SessionID: "s2", Cwd: "/code/app", Agent: "claude", Status: status, LastText: "做完了", UpdatedAt: time.Now()}
	}
	m.setSessions([]proto.Session{s2(proto.StatusWorking)})
	time.Sleep(20 * time.Millisecond)
	m.setSessions([]proto.Session{s2(proto.StatusDone)})
	time.Sleep(50 * time.Millisecond)
	if got := todoNow(h, hand.ID); got.DoneAt != nil {
		t.Fatalf("a hand-made todo was closed: %+v", got)
	}
	if _, c := check(hand.ID); c.Skip != "not_a_task" || c.WouldClose {
		t.Fatalf("done-check on a hand-made todo = %+v", c)
	}
}

// The judge reads the agent's whole latest turn without treating a follow-up as
// proof that the assigned task was already finished.
func TestTaskDoneReadsTheLatestTurn(t *testing.T) {
	h, tn := newTaskHub(t)
	js := stubJudge(t, h, http.StatusOK, `{"verdict":"done","reason":"已合并并发了 TestFlight"}`)
	todo := startedTask(t, h)
	tn.mu.Lock()
	tn.items = []proto.Item{
		{Kind: proto.KindText, Role: "user", Text: "再把「全部 N」挪到最左边"},
		{Kind: proto.KindText, Role: "assistant", Text: "我先看看"},
		{Kind: proto.KindText, Role: "assistant", Text: "挪好了，PR 已合并，TestFlight 构建号 202611040923。没截图的：Mac。"},
	}
	tn.mu.Unlock()
	runTurn(h, "挪好了")
	waitFor(t, "auto done", func() bool { return todoNow(h, todo.ID).DoneAt != nil })
	in, _ := js.input.Load().(string)
	for _, want := range []string{"这一轮收到的请求（不一定来自用户，不替代原任务）", "iPad 横屏时登录页崩溃", "全部 N", "构建号 202611040923", "没截图的：Mac"} {
		if !strings.Contains(in, want) {
			t.Errorf("judge input lacks %q: %s", want, in)
		}
	}
	if strings.Contains(in, "我先看看") {
		t.Errorf("judge input has an earlier reply of the turn: %s", in)
	}

}

// A turn the model service cut off (#1516) is no end of the work: the receipt says the task was interrupted (not the
// error as its summary), nothing judges or closes it, the snapshot and the today card say it waits for the user, and
// "Retry" tells its agent "continue" once however many taps come in. Once the agent works again the task is in progress, and
// a turn that ends normally is a done receipt and closes it as before.
func TestTaskInterruptedByAPIError(t *testing.T) {
	h, tn := newTaskHub(t)
	js := stubJudge(t, h, http.StatusOK, `{"verdict":"done","reason":"修好了"}`)
	todo := startedTask(t, h)
	cut := "API Error: Connection lost mid-response. The response above may be incomplete."
	runTurn(h, cut)
	waitFor(t, "a receipt", func() bool { return receipts(h) == 1 })
	items, _ := feed(h, "conv1")
	var rb receiptBody
	_ = json.Unmarshal(items[len(items)-1].Body, &rb)
	if rb.State != "interrupted" || rb.Summary != assistantwire.InterruptedSummary || rb.Error != cut || rb.PR != "" {
		t.Fatalf("receipt = %+v", rb)
	}
	time.Sleep(50 * time.Millisecond)
	if got := todoNow(h, todo.ID); got.DoneAt != nil || js.calls.Load() != 0 {
		t.Fatalf("todo = %+v, %d judge calls", got, js.calls.Load())
	}
	// Its state is the assistant's: the line the snapshot has for it (taskLine).
	if line := taskLine(h, todo); !strings.Contains(line, "模型服务出错，中断了（没做完，等用户让它继续）") {
		t.Fatalf("snapshot state = %q", line)
	}
	inbox := false
	for _, it := range inboxTasks(t, h) {
		inbox = inbox || it.ID == todo.ID
	}
	if !inbox {
		t.Fatal("an interrupted task is not going on for the inbox")
	}
	var today struct{ Item assistant.FeedItem }
	if code := assistantPost(h, "/api/v1/assistant/conversations/conv1/today", &today); code != http.StatusOK {
		t.Fatalf("today: %d", code)
	}
	var tb struct{ Waiting, Done []struct{ ID string } }
	_ = json.Unmarshal(today.Item.Body, &tb)
	if len(tb.Waiting) != 1 || len(tb.Done) != 0 {
		t.Fatalf("today = %+v", tb)
	}

	// "Retry": "continue" to the agent, once for eight taps; the todo is answered as it is.
	mux := h.routes()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := retryReq(h, mux, todo.ID); w.Code != http.StatusOK {
				t.Errorf("retry: %d %s", w.Code, w.Body)
			}
		}()
	}
	wg.Wait()
	_, sends := tn.calls()
	if len(sends) != 1 || sends[0]["session_id"] != "s1" || sends[0]["text"] != "继续" {
		t.Fatalf("sends = %v", sends)
	}

	m := h.byID["box"]
	m.setSessions([]proto.Session{{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, LastText: cut, UpdatedAt: time.Now()}})
	if line := taskLine(h, todo); !strings.Contains(line, "在做") {
		t.Fatalf("state after 继续 = %q", line)
	}
	time.Sleep(20 * time.Millisecond)
	m.setSessions([]proto.Session{{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone, LastText: "修好了，补了测试", UpdatedAt: time.Now()}})
	waitFor(t, "auto done", func() bool { return todoNow(h, todo.ID).DoneAt != nil })
	items, _ = feed(h, "conv1")
	rb = receiptBody{}
	_ = json.Unmarshal(items[len(items)-1].Body, &rb)
	if receipts(h) != 2 || rb.State != "done" || rb.Summary != "修好了，补了测试" || rb.Error != "" {
		t.Fatalf("%d receipts, last = %+v", receipts(h), rb)
	}
}

// "Retry" on an interrupted task whose machine is offline is refused and sends nothing; one that is not interrupted (in
// progress) is answered as it is.
func TestTaskContinueRefusedOrNotNeeded(t *testing.T) {
	h, tn := newTaskHub(t)
	todo := startedTask(t, h)
	m := h.byID["box"]
	m.setSessions([]proto.Session{{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, UpdatedAt: time.Now()}})
	mux := h.routes()
	if w := retryReq(h, mux, todo.ID); w.Code != http.StatusOK {
		t.Fatalf("retry of a working task: %d %s", w.Code, w.Body)
	}
	m.setSessions([]proto.Session{{SessionID: "s1", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone, LastText: "API Error: 529 Overloaded", UpdatedAt: time.Now()}})
	m.mu.Lock()
	p := m.peer
	m.peer = nil
	m.mu.Unlock()
	if w := retryReq(h, mux, todo.ID); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "离线") {
		t.Fatalf("retry offline: %d %s", w.Code, w.Body)
	}
	m.mu.Lock()
	m.peer = p
	m.mu.Unlock()
	if _, sends := tn.calls(); len(sends) != 0 {
		t.Fatalf("sends = %v", sends)
	}
	// Back online, "Retry" goes through (the refusal left no claim behind).
	if w := retryReq(h, mux, todo.ID); w.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	if _, sends := tn.calls(); len(sends) != 1 {
		t.Fatalf("sends = %v", sends)
	}
}

// The judge does not close a task whose agent's latest answer is the error that cut its turn off (as the done-check
// route asks it), and does not ask the model.
func TestTaskDoneJudgeSkipsACutOffTurn(t *testing.T) {
	h, tn := newTaskHub(t)
	js := stubJudge(t, h, http.StatusOK, `{"verdict":"done","reason":"做完了"}`)
	todo := startedTask(t, h)
	time.Sleep(50 * time.Millisecond)
	before := js.calls.Load() // naming the agent's tab may ask the model too
	tn.mu.Lock()
	tn.items = []proto.Item{
		{Kind: proto.KindText, Role: "assistant", Text: "改好了一半"},
		{Kind: proto.KindText, Role: "assistant", Text: "API Error: Connection lost mid-response. The response above may be incomplete."},
	}
	tn.mu.Unlock()
	var c struct {
		Verdict    string `json:"verdict"`
		WouldClose bool   `json:"would_close"`
	}
	if code := assistantPost(h, "/api/v1/todos/"+todo.ID+"/done-check", &c); code != http.StatusOK || c.Verdict != "not_done" || c.WouldClose ||
		js.calls.Load() != before {
		t.Fatalf("check = %d %+v, %d calls", code, c, js.calls.Load()-before)
	}
}

// receiptBody is a receipt in a conversation's feed, as the app reads it.
type receiptBody struct {
	Title   string `json:"title"`
	State   string `json:"state"`
	Summary string `json:"summary"`
	PR      string `json:"pr"`
	Machine string `json:"machine"`
	Error   string `json:"error"`
}

// assistantPost is a POST on the assistant's routes, as the hub relays it from a full device, once the assistant
// took what the hub told it.
func assistantPost(h *Hub, path string, out any) int {
	settleAssistant(h)
	r := todoRequest(http.MethodPost, path, nil)
	r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	w := httptest.NewRecorder()
	h.routes().ServeHTTP(w, r)
	_ = json.Unmarshal(w.Body.Bytes(), out)
	return w.Code
}
