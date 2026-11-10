package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The todos (todos.go, todo_decisions.go, todo_sessions.go) as the apps reach them: their routes as the hub relays
// them, the machines' session lists as sessions.changed, what the hub shows as the snapshot's report.

// todoTestDB is a clean database for the tests that need Postgres; they skip without one.
func todoTestDB(t *testing.T) *db.DB {
	t.Helper()
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set (scripts/test/test-server.sh runs this on the hub machine)")
	}
	database, err := db.Open(context.Background(), url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.TruncateForTest(context.Background()); err != nil {
		t.Fatal(err)
	}
	return database
}

// todoEnv is the assistant on a fake hub, with st as its store (nil: in memory).
type todoEnv struct {
	t *testing.T
	s *Service
	f *fakeHub
}

func newTodoEnv(t *testing.T, st Store) *todoEnv {
	t.Helper()
	f := newFakeHub().withMachine()
	s := newStorelessService(t, f)
	if st != nil {
		s = newServiceOn(t, f, st)
	}
	return &todoEnv{t: t, s: s, f: f}
}

// todoStores runs body in memory and, when there is a test database, on it.
func todoStores(t *testing.T, body func(t *testing.T, e *todoEnv, database *db.DB)) {
	t.Run("memory", func(t *testing.T) { body(t, newTodoEnv(t, nil), nil) })
	t.Run("store", func(t *testing.T) {
		database := todoTestDB(t)
		body(t, newTodoEnv(t, database), database)
	})
}

// do is an app's request as the hub relays it, from a device with role ("" a full one).
func (e *todoEnv) doAs(role, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	e.s.ServeHTTP(w, r)
	return w
}

func (e *todoEnv) do(method, path, body string) *httptest.ResponseRecorder {
	return e.doAs("", method, path, body)
}

// must is do, failing the test on anything but 200.
func (e *todoEnv) must(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	w := e.do(method, path, body)
	if w.Code != http.StatusOK {
		e.t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
	}
	return w
}

// create makes a todo by hand.
func (e *todoEnv) create(title string) Todo {
	e.t.Helper()
	var res struct{ Todo Todo }
	_ = json.Unmarshal(e.must("POST", "/api/v1/todos", `{"title":"`+title+`"}`).Body.Bytes(), &res)
	return res.Todo
}

// list is GET /api/v1/todos.
func (e *todoEnv) list() []Todo {
	e.t.Helper()
	var res struct{ Todos []Todo }
	_ = json.Unmarshal(e.must("GET", "/api/v1/todos", "").Body.Bytes(), &res)
	return res.Todos
}

// get is todo id as the apps get it (nil when it is not listed).
func (e *todoEnv) get(id string) *Todo {
	for _, t := range e.list() {
		if t.ID == id {
			return &t
		}
	}
	return nil
}

// sessions is machine's node sending its list (sessions.changed).
func (e *todoEnv) sessions(machine string, list ...proto.Session) {
	copied := make([]proto.Session, len(list))
	for i, s := range list {
		s.MachineID = machine
		copied[i] = s
	}
	e.f.setSessions(machine, copied...)
	e.s.SessionsChanged(context.Background(), DeviceSessions{MachineID: machine, Sessions: copied})
}

// todoOf is the todo the agent works for, as the hub is told (the report: what its session list shows); "" for none.
func (e *todoEnv) todoOf(machine, sid string) string {
	for _, l := range e.s.todoReport(context.Background()).Links {
		if l.MachineID == machine && l.SessionID == sid {
			return l.TodoID
		}
	}
	return ""
}

// reload reads the todos from the store again (a restart).
func reloadTodos(t *testing.T, s *Service) {
	t.Helper()
	s.todos.mu.Lock()
	s.todos.loaded = false
	s.todos.mu.Unlock()
	if err := s.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func agentOf(t Todo, sid string) *TodoAgent {
	for i := range t.Agents {
		if t.Agents[i].SessionID == sid {
			return &t.Agents[i]
		}
	}
	return nil
}

func TestTodosCRUDAndPatch(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		if w := e.do("POST", "/api/v1/todos", `{"title":""}`); w.Code != http.StatusBadRequest {
			t.Fatalf("empty title: %d", w.Code)
		}
		w := e.must("POST", "/api/v1/todos", `{"title":"天气 App 发 1.3 版","notes":"测试说明"}`)
		if !strings.Contains(w.Body.String(), `"agents":[]`) {
			t.Fatalf("created = %s", w.Body)
		}
		var created struct{ Todo Todo }
		_ = json.Unmarshal(w.Body.Bytes(), &created)
		id := created.Todo.ID
		if id == "" || created.Todo.Title != "天气 App 发 1.3 版" || created.Todo.Notes != "测试说明" {
			t.Fatalf("created = %+v", created.Todo)
		}
		if w := e.must("GET", "/api/v1/todos", ""); !strings.Contains(w.Body.String(), `"agents":[]`) || e.list()[0].ID != id {
			t.Fatalf("list = %s", w.Body)
		}

		var patched struct{ Todo Todo }
		_ = json.Unmarshal(e.must("PATCH", "/api/v1/todos/"+id, `{"title":"新标题"}`).Body.Bytes(), &patched)
		if patched.Todo.Title != "新标题" || patched.Todo.Notes != "测试说明" {
			t.Fatalf("patched = %+v", patched.Todo)
		}
		_ = json.Unmarshal(e.must("PATCH", "/api/v1/todos/"+id, `{"done":true}`).Body.Bytes(), &patched)
		if patched.Todo.DoneAt == nil {
			t.Fatal("not done")
		}
		_ = json.Unmarshal(e.must("PATCH", "/api/v1/todos/"+id, `{"done":false}`).Body.Bytes(), &patched)
		if patched.Todo.DoneAt != nil || patched.Todo.ReopenedAt == nil {
			t.Fatalf("reopened = %+v", patched.Todo)
		}
		if got := e.f.eventTypes(); !strings.Contains(got, "todos") {
			t.Fatalf("the apps were not told: %q", got)
		}

		e.must("DELETE", "/api/v1/todos/"+id, "")
		if w := e.do("PATCH", "/api/v1/todos/"+id, `{"done":false}`); w.Code != http.StatusNotFound {
			t.Fatalf("PATCH after delete: %d", w.Code)
		}
	})
}

// An agent works for one todo at a time: linked to another, it moved (the first lists it as having worked for it);
// a done todo is on no session, back when reopened; removed by hand, it is off.
func TestTodoAgentLinkTransferAndUnlink(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		e.f.setSessions("macbook", proto.Session{MachineID: "macbook", SessionID: "s1", Title: "测试 Agent", Cwd: "/code/weather", Agent: "claude"})
		t1, t2 := e.create("待办 1"), e.create("待办 2")
		e.must("PUT", "/api/v1/todos/"+t1.ID+"/agents/macbook/s1", "")
		if got := e.todoOf("macbook", "s1"); got != t1.ID {
			t.Fatalf("s1 on %q", got)
		}
		if a := agentOf(*e.get(t1.ID), "s1"); a == nil || a.Title != "测试 Agent" || a.Cwd != "/code/weather" || a.Agent != "claude" {
			t.Fatalf("agent = %+v", a)
		}
		snap := e.f.lastSnapshot()
		if snap.Todos == nil || len(snap.Todos.Links) != 1 || snap.Todos.Links[0].Title != "待办 1" {
			t.Fatalf("the hub was told %+v", snap.Todos)
		}

		e.must("PUT", "/api/v1/todos/"+t2.ID+"/agents/macbook/s1", "")
		if got := e.todoOf("macbook", "s1"); got != t2.ID {
			t.Fatalf("s1 on %q after the move", got)
		}
		if a := agentOf(*e.get(t1.ID), "s1"); a == nil || a.ReleasedAt == nil {
			t.Fatalf("todo 1's agent = %+v", a)
		}
		if a := agentOf(*e.get(t2.ID), "s1"); a == nil || a.ReleasedAt != nil {
			t.Fatalf("todo 2's agent = %+v", a)
		}

		e.must("PATCH", "/api/v1/todos/"+t2.ID, `{"done":true}`)
		if got := e.todoOf("macbook", "s1"); got != "" {
			t.Fatalf("s1 on a done todo: %q", got)
		}
		if w := e.do("PUT", "/api/v1/todos/"+t2.ID+"/agents/macbook/s2", ""); w.Code != http.StatusConflict {
			t.Fatalf("link to a done todo: %d", w.Code)
		}
		e.must("PATCH", "/api/v1/todos/"+t2.ID, `{"done":false}`)
		if got := e.todoOf("macbook", "s1"); got != t2.ID {
			t.Fatalf("s1 after the reopen: %q", got)
		}
		e.must("DELETE", "/api/v1/todos/"+t2.ID+"/agents/macbook/s1", "")
		if got := e.todoOf("macbook", "s1"); got != "" {
			t.Fatalf("s1 after its removal: %q", got)
		}
		if w := e.do("DELETE", "/api/v1/todos/"+t2.ID+"/agents/macbook/nope", ""); w.Code != http.StatusNotFound {
			t.Fatalf("removing an agent it never had: %d", w.Code)
		}
	})
}

// countingStore counts the agents' title writes.
type countingStore struct {
	*db.DB
	snapshotCalls atomic.Int64
}

func (c *countingStore) UpdateTodoAgentSnapshot(ctx context.Context, machineID, sessionID, title, startError string) error {
	c.snapshotCalls.Add(1)
	return c.DB.UpdateTodoAgentSnapshot(ctx, machineID, sessionID, title, startError)
}

// The links keep the agents' titles and start errors as their sessions say: written once however many lists come
// while it is on its way.
func TestTodoSnapshotAsyncUpdateAndDedupe(t *testing.T) {
	database := todoTestDB(t)
	cStore := &countingStore{DB: database}
	e := newTodoEnv(t, cStore)
	e.f.setSessions("macbook", proto.Session{SessionID: "s1", Title: "初始标题", Cwd: "/code", Agent: "claude"})
	todo := e.create("待办 A")
	e.must("PUT", "/api/v1/todos/"+todo.ID+"/agents/macbook/s1", "")
	for range 5 {
		e.sessions("macbook", proto.Session{SessionID: "s1", Title: "新 Agent 标题", Cwd: "/code", Agent: "claude", StartError: "start failed reason"})
	}
	waitFor(t, "the title", func() bool {
		a := agentOf(*e.get(todo.ID), "s1")
		return a != nil && a.Title == "新 Agent 标题" && a.StartError == "start failed reason"
	})
	time.Sleep(100 * time.Millisecond)
	if calls := cStore.snapshotCalls.Load(); calls != 1 {
		t.Fatalf("%d writes, want 1", calls)
	}
	agents, err := database.ListAllTodoAgents(context.Background())
	if err != nil || len(agents) != 1 || agents[0].Title != "新 Agent 标题" || agents[0].StartError != "start failed reason" {
		t.Fatalf("stored: %+v %v", agents, err)
	}
}

// A device that may only answer (the watch) writes no todo.
func TestTodoRoutesReadOnlyForbidden(t *testing.T) {
	e := newTodoEnv(t, nil)
	todo := e.create("待办")
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/todos", `{"title":"手表写待办"}`},
		{"PATCH", "/api/v1/todos/" + todo.ID, `{"title":"改"}`},
		{"DELETE", "/api/v1/todos/" + todo.ID, ""},
		{"PUT", "/api/v1/todos/" + todo.ID + "/agents/m/s", ""},
		{"DELETE", "/api/v1/todos/" + todo.ID + "/agents/m/s", ""},
		{"PUT", "/api/v1/todos/" + todo.ID + "/spaces/macbook/any", ""},
		{"DELETE", "/api/v1/todos/" + todo.ID + "/spaces/macbook/any", ""},
		{"POST", "/api/v1/todos/" + todo.ID + "/decisions", `{"question":"q","choice":"c"}`},
		{"PATCH", "/api/v1/todos/" + todo.ID + "/decisions/dec_x", `{"supersedes_id":""}`},
		{"POST", "/api/v1/todos/" + todo.ID + "/done-check", ""},
		{"POST", "/api/v1/todos/" + todo.ID + "/retry", ""},
	} {
		if w := e.doAs("answer", c.method, c.path, c.body); w.Code != http.StatusForbidden {
			t.Fatalf("%s %s as the watch: %d", c.method, c.path, w.Code)
		}
	}
	if w := e.doAs("answer", "GET", "/api/v1/todos", ""); w.Code != http.StatusOK {
		t.Fatalf("the watch reads the todos: %d", w.Code)
	}
}

// The hub asks before an agent is handed work for a todo (assistant.todo_handoff) and links it after
// (assistant.todo_link).
func TestTodoHandoffAndLink(t *testing.T) {
	e := newTodoEnv(t, nil)
	ctx := context.Background()
	if res, err := e.s.TodoHandoff(ctx, TodoHandoff{TodoID: "nope", Text: "x"}); err != nil || res.Found || res.Text != "x" || res.Status != 404 {
		t.Fatalf("unknown todo: %+v %v", res, err)
	}
	todo := e.create("发版")
	res, err := e.s.TodoHandoff(ctx, TodoHandoff{TodoID: todo.ID, Text: "去发版"})
	if err != nil || !res.Found || res.Title != "发版" || res.DoneAt != nil || res.Text != "去发版" || res.Status != 0 {
		t.Fatalf("open todo (no decisions without a store): %+v %v", res, err)
	}
	if linked, err := e.s.TodoLink(ctx, TodoLinkParams{TodoID: todo.ID, MachineID: "box", SessionID: "s9", Cwd: "/code",
		Prompt: "<persona-context kind=\"context\" title=\"待办\">说明</persona-context>\n去发版"}); err != nil || linked.Title != "发版" {
		t.Fatalf("link: %+v %v", linked, err)
	}
	if got := e.todoOf("box", "s9"); got != todo.ID {
		t.Fatalf("linked to %q", got)
	}
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
	if res, _ := e.s.TodoHandoff(ctx, TodoHandoff{TodoID: todo.ID}); res.DoneAt == nil || res.Status != 409 {
		t.Fatalf("done todo: %+v", res)
	}
	if _, err := e.s.TodoLink(ctx, TodoLinkParams{TodoID: todo.ID, MachineID: "box", SessionID: "s8"}); !errors.Is(err, db.ErrTodoCompleted) {
		t.Fatalf("link to a done todo: %v", err)
	}
}

// What the hub shows: the open todos' links (a task's marked), the agents of quiet tasks — those done more than
// quietKeep ago left out, and one that now works for a todo that is not quiet.
func TestTodoReport(t *testing.T) {
	e := newTodoEnv(t, nil)
	long := time.Now().Add(-quietKeep - time.Hour)
	recent := time.Now().Add(-time.Hour)
	putTodo(e.s, Todo{ID: "t_task", Title: "任务", Origin: "conv1", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}})
	putTodo(e.s, Todo{ID: "t_hand", Title: "手建", Agents: []TodoAgent{{MachineID: "box", SessionID: "s2"}}})
	putTodo(e.s, Todo{ID: "t_quiet", Title: "安静", Origin: "main", Quiet: true, DoneAt: &recent,
		Agents: []TodoAgent{{MachineID: "box", SessionID: "s3"}, {MachineID: "box", SessionID: "s2"}}})
	putTodo(e.s, Todo{ID: "t_old", Title: "很早", Origin: "main", Quiet: true, DoneAt: &long, Agents: []TodoAgent{{MachineID: "box", SessionID: "s4"}}})
	r := e.s.todoReport(context.Background())
	if len(r.Links) != 2 || r.Links[0].TodoID != "t_task" || !r.Links[0].Task || r.Links[1].TodoID != "t_hand" || r.Links[1].Task {
		t.Fatalf("links = %+v", r.Links)
	}
	if len(r.Quiet) != 1 || r.Quiet[0].SessionID != "s3" {
		t.Fatalf("quiet = %+v", r.Quiet)
	}
	e.s.publish(context.Background())
	if snap := e.f.lastSnapshot(); snap.Partial || snap.Todos == nil || len(snap.Todos.Links) != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
}

// A judgment that began before the user reopened the todo does not close it.
func TestAutoDoneKeepsTheUsersChange(t *testing.T) {
	e := newTodoEnv(t, nil)
	todo := e.create("修")
	before := time.Now()
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":false}`)
	if ok, err := e.s.autoDoneTodo(context.Background(), todo.ID, "做完了", before); ok || err != nil {
		t.Fatalf("a stale judgment closed it: %v %v", ok, err)
	}
	if ok, err := e.s.autoDoneTodo(context.Background(), todo.ID, "做完了", time.Now()); !ok || err != nil {
		t.Fatalf("a judgment of later work: %v %v", ok, err)
	}
	if got := e.get(todo.ID); got.DoneAt == nil || got.DoneBy != db.DoneByAuto || got.DoneReason != "做完了" {
		t.Fatalf("todo = %+v", got)
	}
}

// failingStore fails reading the todos while failLoad is set.
type failingStore struct {
	*db.DB
	failLoad atomic.Bool
}

func (f *failingStore) ListTodos(ctx context.Context) ([]db.TodoRow, error) {
	if f.failLoad.Load() {
		return nil, errors.New("simulated db failure")
	}
	return f.DB.ListTodos(ctx)
}

// gatedTodoStore holds the first UpdateTodo or EditTodo after armed is set, once it has committed, until release is
// closed: a second device's write can then commit while the first one has not reached memory yet.
type gatedTodoStore struct {
	*db.DB
	armed     atomic.Bool
	committed chan struct{}
	release   chan struct{}
}

func newGatedTodoStore(database *db.DB) *gatedTodoStore {
	return &gatedTodoStore{DB: database, committed: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedTodoStore) UpdateTodo(ctx context.Context, id string, title, notes *string, done *bool, at time.Time) (*db.TodoRow, error) {
	row, err := g.DB.UpdateTodo(ctx, id, title, notes, done, at)
	g.hold()
	return row, err
}

// EditTodo is the PATCH route's write (editTodo), held like UpdateTodo.
func (g *gatedTodoStore) EditTodo(ctx context.Context, id string, e db.TodoEdit, at time.Time) (*db.TodoRow, error) {
	row, err := g.DB.EditTodo(ctx, id, e, at)
	g.hold()
	return row, err
}

// hold holds the first write after armed is set until release is closed.
func (g *gatedTodoStore) hold() {
	if g.armed.CompareAndSwap(true, false) {
		close(g.committed)
		<-g.release
	}
}

// raceTodoWrites runs first, held by g once its write committed, against second, which starts only then. Without
// the ordering second lands in memory before first does; with it, second waits for first. first is let go once
// second finished or had a second to (the wait only bounds how long the ordering is given to show; the result
// asserted does not depend on it).
func raceTodoWrites(g *gatedTodoStore, first, second func()) {
	g.armed.Store(true)
	firstDone, secondDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(firstDone); first() }()
	<-g.committed
	go func() { defer close(secondDone); second() }()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
	}
	close(g.release)
	<-firstDone
	<-secondDone
}

// todoDoneBoth is whether todo id is done in memory (as the apps list it) and in the store.
func todoDoneBoth(t *testing.T, e *todoEnv, database *db.DB, id string) (mem, row bool) {
	t.Helper()
	got := e.get(id)
	if got == nil {
		t.Fatalf("todo %s not listed", id)
	}
	rows, err := database.ListTodos(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == id {
			return got.DoneAt != nil, r.DoneAt != nil
		}
	}
	t.Fatalf("todo %s not in the store", id)
	return false, false
}

// Two devices: A's PATCH {done:true} commits, B's {done:false} commits after it. Memory ends as the store does
// (open), not with A's older row applied last.
func TestTodoPatchOrder(t *testing.T) {
	g := newGatedTodoStore(todoTestDB(t))
	e := newTodoEnv(t, g)
	todo := e.create("two devices")
	var a, b int
	raceTodoWrites(g,
		func() { a = e.do("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`).Code },
		func() { b = e.do("PATCH", "/api/v1/todos/"+todo.ID, `{"done":false}`).Code })
	if a != http.StatusOK || b != http.StatusOK {
		t.Fatalf("PATCH codes: %d %d", a, b)
	}
	if mem, row := todoDoneBoth(t, e, g.DB, todo.ID); mem || row {
		t.Fatalf("done in memory %v, in the store %v; want both open", mem, row)
	}
}

// reopenTodo (a rejected acceptance) against the user's PATCH {done:true} committed after it: memory ends done.
func TestTodoReopenOrder(t *testing.T) {
	g := newGatedTodoStore(todoTestDB(t))
	e := newTodoEnv(t, g)
	todo := e.create("reopened")
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
	var reopenErr error
	var b int
	raceTodoWrites(g,
		func() { reopenErr = e.s.reopenTodo(context.Background(), todo.ID) },
		func() { b = e.do("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`).Code })
	if reopenErr != nil || b != http.StatusOK {
		t.Fatalf("reopen %v, PATCH %d", reopenErr, b)
	}
	if mem, row := todoDoneBoth(t, e, g.DB, todo.ID); !mem || !row {
		t.Fatalf("done in memory %v, in the store %v; want both done", mem, row)
	}
}

// A supervisor finishing acceptance after the user's direct completion must not replace
// the user marker with done_by=review, which would reopen the task on later agent activity.
func TestUserCompletionWinsLateSupervisorAcceptance(t *testing.T) {
	g := newGatedTodoStore(todoTestDB(t))
	e := newTodoEnv(t, g)
	ctx := context.Background()
	todo, err := e.s.createTodo(ctx, "Review rollout", "Choose a rollout", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	e.f.setSessions("box", idleAgent(time.Now().Add(-time.Hour)))
	if err := e.s.linkTodoAgent(ctx, todo.ID, "box", "s1", "Rollout", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	e.s.superviseTodo(ctx, todo.ID, "conv1", nil, 0)
	var status int
	var closed bool
	var reviewErr error
	raceTodoWrites(g,
		func() { status = e.do("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`).Code },
		func() { closed, reviewErr = e.s.reviewDoneTodo(ctx, todo.ID, "Accepted later", true) })
	if status != http.StatusOK || closed || reviewErr != nil {
		t.Fatalf("PATCH %d, review closed %v: %v", status, closed, reviewErr)
	}
	reloadTodos(t, e.s)
	e.s.taskWorking(ctx, "box", "s1")
	stored, err := g.GetTodo(ctx, todo.ID)
	got := todoNow(e.s, todo.ID)
	if err != nil || stored.DoneAt == nil || stored.DoneBy != "" || got.DoneAt == nil || got.DoneBy != "" {
		t.Fatalf("the user's completion changed: memory %+v, store %+v, error %v", got, stored, err)
	}
}

// The todos not read yet (the store failing) are no empty list: the routes answer 503 and the report is left out
// (the hub keeps the last one); once the store answers they are read.
func TestTodosCacheReliability(t *testing.T) {
	database := todoTestDB(t)
	now := time.Now().UTC()
	if err := database.CreateTodo(context.Background(), db.TodoRow{ID: "t_exist", Title: "已存在的待办", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	fStore := &failingStore{DB: database}
	fStore.failLoad.Store(true)
	e := newTodoEnv(t, fStore)
	if w := e.do("GET", "/api/v1/todos", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET while failing: %d", w.Code)
	}
	if w := e.do("PATCH", "/api/v1/todos/t_exist", `{"title":"改"}`); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PATCH while failing: %d", w.Code)
	}
	e.s.publish(context.Background())
	if snap := e.f.lastSnapshot(); !snap.Partial || snap.Todos != nil {
		t.Fatalf("snapshot while failing: partial %v todos %+v", snap.Partial, snap.Todos)
	}
	e.sessions("macbook", proto.Session{SessionID: "s1", SpawnFrom: "t_exist@box/p1", Status: proto.StatusWorking}) // not read on a list
	fStore.failLoad.Store(false)
	if got := e.list(); len(got) != 1 || got[0].ID != "t_exist" {
		t.Fatalf("after recovery: %+v", got)
	}
}

// Writes, reads and session lists at once (run with -race).
func TestTodosConcurrencyRace(t *testing.T) {
	e := newTodoEnv(t, todoTestDB(t))
	var list []proto.Session
	for i := range 5 {
		list = append(list, proto.Session{MachineID: "macbook", SessionID: fmt.Sprintf("s_%d", i), Title: fmt.Sprintf("Agent %d", i), SpaceName: "w"})
	}
	e.f.setSessions("macbook", list...)
	todo := e.create("并发测试待办")
	stop := make(chan struct{})
	var wg sync.WaitGroup
	loop := func(f func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
					f(i)
					time.Sleep(2 * time.Millisecond)
				}
			}
		}()
	}
	loop(func(i int) { e.do("PATCH", "/api/v1/todos/"+todo.ID, fmt.Sprintf(`{"done":%v}`, i%2 == 0)) })
	loop(func(i int) {
		sid := fmt.Sprintf("s_%d", i%5)
		e.do("PUT", "/api/v1/todos/"+todo.ID+"/agents/macbook/"+sid, "")
		e.do("DELETE", "/api/v1/todos/"+todo.ID+"/agents/macbook/"+sid, "")
	})
	loop(func(int) {
		e.do("GET", "/api/v1/todos", "")
		_, _ = e.s.TodoHandoff(context.Background(), TodoHandoff{TodoID: todo.ID, Text: "x"})
	})
	loop(func(int) {
		e.s.SessionsChanged(context.Background(), DeviceSessions{MachineID: "macbook", Sessions: list})
		_ = e.s.todoReport(context.Background())
	})
	time.Sleep(500 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// ---- decisions ----

func listDecisions(t *testing.T, e *todoEnv, todoID, query string) []db.TodoDecisionRow {
	t.Helper()
	var out struct{ Decisions []db.TodoDecisionRow }
	if err := json.Unmarshal(e.must("GET", "/api/v1/todos/"+todoID+"/decisions"+query, "").Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Decisions
}

func TestTodoDecisionsAddAndSupersede(t *testing.T) {
	e := newTodoEnv(t, todoTestDB(t))
	todoID := e.create("天气 App 发 1.3 版").ID
	if w := e.do("POST", "/api/v1/todos/"+todoID+"/decisions", `{"question":"  ","choice":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty question: %d", w.Code)
	}
	if w := e.do("GET", "/api/v1/todos/todo_nope/decisions", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown todo: %d", w.Code)
	}
	if got := listDecisions(t, e, todoID, ""); got == nil || len(got) != 0 {
		t.Fatalf("a todo without decisions lists [] (not null): %#v", got)
	}

	var first struct{ Decision db.TodoDecisionRow }
	_ = json.Unmarshal(e.must("POST", "/api/v1/todos/"+todoID+"/decisions", `{"question":"先发哪个平台？","choice":"iOS","reason":"测试员多"}`).Body.Bytes(), &first)
	// The todo moves with it, so apps that reload decisions on updated_at see it.
	if got := e.get(todoID); !got.UpdatedAt.Equal(first.Decision.CreatedAt) {
		t.Fatalf("todo updated_at = %v, want the decision's %v", got.UpdatedAt, first.Decision.CreatedAt)
	}
	if first.Decision.ID == "" || first.Decision.Source != db.DecisionManual || first.Decision.Reason != "测试员多" {
		t.Fatalf("added = %+v", first.Decision)
	}

	var second struct{ Decision db.TodoDecisionRow }
	_ = json.Unmarshal(e.must("POST", "/api/v1/todos/"+todoID+"/decisions",
		`{"question":"先发哪个平台？","choice":"安卓","reason":"iOS 审核卡住了","supersedes_id":"`+first.Decision.ID+`"}`).Body.Bytes(), &second)
	if live := listDecisions(t, e, todoID, ""); len(live) != 1 || live[0].ID != second.Decision.ID || live[0].SupersedesID != first.Decision.ID {
		t.Fatalf("in effect = %+v, want only the second", live)
	}
	if all := listDecisions(t, e, todoID, "?all=1"); len(all) != 2 || all[0].ID != first.Decision.ID || all[0].SupersededBy != second.Decision.ID {
		t.Fatalf("all = %+v", all)
	}
	if w := e.do("PATCH", "/api/v1/todos/"+todoID+"/decisions/"+first.Decision.ID, `{"supersedes_id":"`+second.Decision.ID+`"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("superseding a later one: %d", w.Code)
	}
	if w := e.do("PATCH", "/api/v1/todos/"+todoID+"/decisions/dec_nope", `{"supersedes_id":""}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown decision: %d", w.Code)
	}
	e.must("PATCH", "/api/v1/todos/"+todoID+"/decisions/"+second.Decision.ID, `{"supersedes_id":""}`)
	if live := listDecisions(t, e, todoID, ""); len(live) != 2 {
		t.Fatalf("after undo, in effect = %+v", live)
	}
}

// Without the database there is nowhere to keep decisions: 503, not an empty list.
func TestTodoDecisionsNeedTheStore(t *testing.T) {
	e := newTodoEnv(t, nil)
	todo := e.create("x")
	if w := e.do("GET", "/api/v1/todos/"+todo.ID+"/decisions", ""); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("decisions without a store: %d", w.Code)
	}
}

// The user answers a question card of an agent working on the todo (interaction.resolved): each answered question
// becomes a decision, once; cards of agents on no todo, permissions and dismissed cards write nothing.
func TestCardAnswerRecordsDecision(t *testing.T) {
	e := newTodoEnv(t, todoTestDB(t))
	e.f.setSessions("box", proto.Session{SessionID: "s1", Title: "迁移 Agent"})
	todoID := e.create("天气 App 发 1.3 版").ID
	e.must("PUT", "/api/v1/todos/"+todoID+"/agents/box/s1", "")
	questions := []proto.Question{
		{Question: "用哪个迁移号？", Options: []proto.QuestionOption{{Label: "0311"}, {Label: "0271"}}},
		{Question: "要不要做安卓？", Options: []proto.QuestionOption{{Label: "另拆 issue"}, {Label: "一起做"}}},
	}
	resolved := func(ix proto.Interaction, r proto.Resolution) {
		e.s.InteractionResolved(context.Background(), InteractionResolved{Interaction: ix, Resolution: r, Status: map[bool]string{true: "dismissed", false: "resolved"}[r.Dismiss]})
	}
	answers := proto.Resolution{Answers: map[string]string{"用哪个迁移号？": "0311", "要不要做安卓？": "另拆 issue"}}
	resolved(proto.Interaction{ID: "ix_q", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion, Questions: questions}, answers)
	allow := true
	resolved(proto.Interaction{ID: "ix_p", MachineID: "box", SessionID: "s1", Kind: proto.IxPermission, ToolName: "Bash"}, proto.Resolution{Allow: &allow})
	resolved(proto.Interaction{ID: "ix_d", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion, Questions: questions[:1]}, proto.Resolution{Dismiss: true})
	resolved(proto.Interaction{ID: "ix_o", MachineID: "box", SessionID: "other", Kind: proto.IxQuestion, Questions: questions[:1]},
		proto.Resolution{Answers: map[string]string{"用哪个迁移号？": "0271"}})
	got := listDecisions(t, e, todoID, "?all=1")
	if len(got) != 2 {
		t.Fatalf("decisions = %+v, want the two answers of ix_q", got)
	}
	for i, want := range [][2]string{{"用哪个迁移号？", "0311"}, {"要不要做安卓？", "另拆 issue"}} {
		d := got[i]
		if d.Question != want[0] || d.Choice != want[1] || d.Source != db.DecisionCard || d.SourceMachine != "box" ||
			d.SourceSession != "s1" || d.SourceMessage != "ix_q" {
			t.Fatalf("decision %d = %+v", i, d)
		}
	}
	// The same answer reported again (a retried resolve) writes nothing new.
	resolved(proto.Interaction{ID: "ix_q", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion, Questions: questions}, answers)
	if again := listDecisions(t, e, todoID, "?all=1"); len(again) != 2 {
		t.Fatalf("after a second report: %+v", again)
	}
	// Handed to an agent: the decisions go first (the hub's TestHandoffCarriesDecisions has the whole text).
	if res, _ := e.s.TodoHandoff(context.Background(), TodoHandoff{TodoID: todoID, Text: "发版"}); !strings.Contains(res.Text, "在「迁移 Agent」的提问里答的") ||
		!strings.Contains(res.Text, "用哪个迁移号？") || !strings.HasSuffix(res.Text, "\n\n发版") {
		t.Fatalf("handoff = %q", res.Text)
	}
}

// An agent on a todo reads its decisions and notes its own (todo_decisions / todo_decision_add, #5279): always on
// the todo it works on, marked as the agent's; an agent on no todo has none, and a bad supersedes_id is refused.
func TestAgentReadsAndNotesDecisions(t *testing.T) {
	e := newTodoEnv(t, todoTestDB(t))
	ctx := context.Background()
	e.f.setSessions("box", proto.Session{SessionID: "s1", Title: "迁移 Agent"}, proto.Session{SessionID: "s2", Title: "闲着的 Agent"})
	todoID := e.create("天气 App 发 1.3 版").ID
	other := e.create("别的事").ID
	e.must("PUT", "/api/v1/todos/"+todoID+"/agents/box/s1", "")
	e.must("POST", "/api/v1/todos/"+other+"/decisions", `{"question":"别的事的问题","choice":"别的事的答案"}`)
	e.must("POST", "/api/v1/todos/"+todoID+"/decisions", `{"question":"先发哪个平台？","choice":"iOS"}`)
	tool := func(session, name, args string) proto.ToolResult {
		return e.s.agentTool(ctx, "box", session, name, json.RawMessage(args))
	}

	// Its own todo's decisions only, the user's marked as theirs.
	res := tool("s1", "todo_decisions", `{}`)
	var read struct {
		Todo      struct{ ID string }
		Decisions []agentDecisionView
	}
	if res.IsError || json.Unmarshal([]byte(res.Content[0].Text), &read) != nil || read.Todo.ID != todoID || len(read.Decisions) != 1 ||
		read.Decisions[0].Choice != "iOS" || read.Decisions[0].By != "user" {
		t.Fatalf("read: %+v", res)
	}
	userDecision := read.Decisions[0].ID

	// The user's call is theirs to change: replacing it is refused, and it stays in effect.
	if res := tool("s1", "todo_decision_add", `{"question":"先发哪个平台？","choice":"安卓","supersedes_id":"`+userDecision+`"}`); !res.IsError ||
		!strings.Contains(res.Content[0].Text, "the user's") {
		t.Fatalf("replace the user's: %+v", res)
	}
	// It notes its own: kept as the agent's, with where it came from; a later one of its own may replace it.
	res = tool("s1", "todo_decision_add", `{"question":"迁移号用哪个？","choice":"0311","reason":"0271 已占"}`)
	if res.IsError || !strings.Contains(res.Content[0].Text, todoID) {
		t.Fatalf("add: %+v", res)
	}
	own := listDecisions(t, e, todoID, "")[1]
	if own.Choice != "0311" || own.Source != db.DecisionAgent || own.SourceMachine != "box" || own.SourceSession != "s1" {
		t.Fatalf("after add: %+v", own)
	}
	if res := tool("s1", "todo_decision_add", `{"question":"迁移号用哪个？","choice":"0312","supersedes_id":"`+own.ID+`"}`); res.IsError {
		t.Fatalf("replace its own: %+v", res)
	}
	live := listDecisions(t, e, todoID, "")
	if len(live) != 2 || live[0].ID != userDecision || live[1].Choice != "0312" || live[1].SupersedesID != own.ID {
		t.Fatalf("in effect: %+v", live)
	}
	if res := tool("s1", "todo_decisions", `{}`); !strings.Contains(res.Content[0].Text, `"by":"agent"`) ||
		!strings.Contains(res.Content[0].Text, `"by":"user"`) {
		t.Fatalf("read again: %+v", res)
	}
	if h, _ := e.s.TodoHandoff(ctx, TodoHandoff{TodoID: todoID, Text: "发版"}); !strings.Contains(h.Text, "Agent 干活时记的，不是用户定的") {
		t.Fatalf("handoff = %q", h.Text)
	}

	// Refused: no question or choice, superseding another todo's decision; the other todo is untouched.
	otherDecision := listDecisions(t, e, other, "")[0].ID
	for _, bad := range []string{`{"question":"q"}`, `{"question":" ","choice":"c"}`,
		`{"question":"q","choice":"c","supersedes_id":"` + otherDecision + `"}`} {
		if res := tool("s1", "todo_decision_add", bad); !res.IsError {
			t.Fatalf("%s: %+v", bad, res)
		}
	}
	if got := listDecisions(t, e, other, "?all=1"); len(got) != 1 {
		t.Fatalf("other todo: %+v", got)
	}

	// An agent on no todo, or a call from no session: nothing to read or note, and nothing written.
	if res := tool("s2", "todo_decision_add", `{"question":"q","choice":"c"}`); res.IsError || !strings.Contains(res.Content[0].Text, "not working on an open todo") {
		t.Fatalf("no todo: %+v", res)
	}
	if res := tool("", "todo_decisions", `{}`); !res.IsError || !strings.Contains(res.Content[0].Text, "which agent session") {
		t.Fatalf("no session: %+v", res)
	}
	if got := listDecisions(t, e, todoID, "?all=1"); len(got) != 3 {
		t.Fatalf("decisions = %+v", got)
	}

	// At most reportLimit notes in reportWindow (two went in above).
	for i := 2; i < reportLimit; i++ {
		if res := tool("s1", "todo_decision_add", fmt.Sprintf(`{"question":"q%d","choice":"c"}`, i)); res.IsError {
			t.Fatalf("note %d: %+v", i, res)
		}
	}
	if res := tool("s1", "todo_decision_add", `{"question":"one more","choice":"c"}`); !res.IsError || !strings.Contains(res.Content[0].Text, "too many") {
		t.Fatalf("over the limit: %+v", res)
	}

	// Once the todo is done its agent is on no open todo.
	e.must("PATCH", "/api/v1/todos/"+todoID, `{"done":true}`)
	if res := tool("s1", "todo_decisions", `{}`); res.IsError || !strings.Contains(res.Content[0].Text, "not working on an open todo") {
		t.Fatalf("done todo: %+v", res)
	}
}

// Without the database an agent's decision tools say so instead of pretending.
func TestAgentDecisionToolsNeedTheStore(t *testing.T) {
	e := newTodoEnv(t, nil)
	if res := e.s.agentTool(context.Background(), "box", "s1", "todo_decisions", json.RawMessage(`{}`)); !res.IsError {
		t.Fatalf("without a store: %+v", res)
	}
}

// pausingAutoDoneStore closes the todo in the database, then tells the test and waits for it before answering, the
// way a slow round trip leaves room for other writers between the store's write and memory's.
type pausingAutoDoneStore struct {
	*db.DB
	stored, release chan struct{}
}

func (p *pausingAutoDoneStore) AutoDoneTodo(ctx context.Context, id, reason string, at, workSince time.Time) (*db.TodoRow, bool, error) {
	r, ok, err := p.DB.AutoDoneTodo(ctx, id, reason, at, workSince)
	p.stored <- struct{}{}
	<-p.release
	return r, ok, err
}

// The database closed the todo: memory follows it even when something that does not touch done (an agent's title
// synced, a link) bumped the todo's updated time while the store was writing.
func TestAutoDoneFollowsTheStoreAfterAnUnrelatedBump(t *testing.T) {
	database := todoTestDB(t)
	st := &pausingAutoDoneStore{DB: database, stored: make(chan struct{}), release: make(chan struct{})}
	e := newTodoEnv(t, st)
	todo := e.create("修")
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		ok, err := e.s.autoDoneTodo(context.Background(), todo.ID, "做完了", time.Now().Add(-time.Minute))
		done <- result{ok, err}
	}()
	<-st.stored
	e.s.todos.mu.Lock() // as updateTodoAgentSnapshots does (todo_sessions.go)
	e.s.todos.todos[todo.ID].UpdatedAt = time.Now().Add(time.Second)
	e.s.todos.mu.Unlock()
	close(st.release)
	if r := <-done; !r.ok || r.err != nil {
		t.Fatalf("autoDoneTodo = %v %v, want closed", r.ok, r.err)
	}
	if got := e.get(todo.ID); got.DoneAt == nil || got.DoneBy != db.DoneByAuto || got.DoneReason != "做完了" {
		t.Fatalf("memory = %+v, want done by auto", got)
	}
	rows, err := database.ListTodos(context.Background())
	if err != nil || len(rows) != 1 || rows[0].DoneAt == nil || rows[0].DoneBy != db.DoneByAuto {
		t.Fatalf("store = %+v %v", rows, err)
	}
}

// The user's reopen that reached the database first stands: the store refuses the stale judgment and memory stays
// open, as the database has it.
func TestAutoDoneRefusedByTheStoreLeavesItOpen(t *testing.T) {
	database := todoTestDB(t)
	e := newTodoEnv(t, database)
	todo := e.create("修")
	before := time.Now().Add(-time.Minute)
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":false}`)
	if ok, err := e.s.autoDoneTodo(context.Background(), todo.ID, "做完了", before); ok || err != nil {
		t.Fatalf("a stale judgment closed it: %v %v", ok, err)
	}
	if got := e.get(todo.ID); got.DoneAt != nil || got.ReopenedAt == nil {
		t.Fatalf("memory = %+v, want open", got)
	}
	rows, err := database.ListTodos(context.Background())
	if err != nil || len(rows) != 1 || rows[0].DoneAt != nil {
		t.Fatalf("store = %+v %v", rows, err)
	}
}

// raceAutoDone holds autoDoneTodo once its store write committed and runs other meanwhile (given a second to finish),
// then lets the judgment reach memory: a writer that committed after it must not be undone by its older row.
func raceAutoDone(t *testing.T, e *todoEnv, st *pausingAutoDoneStore, id string, other func()) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		ok, err := e.s.autoDoneTodo(context.Background(), id, "做完了", time.Now().Add(-time.Minute))
		if err == nil && !ok {
			err = errors.New("autoDoneTodo did not close it")
		}
		done <- err
	}()
	<-st.stored
	otherDone := make(chan struct{})
	go func() { defer close(otherDone); other() }()
	select {
	case <-otherDone:
	case <-time.After(time.Second):
	}
	close(st.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-otherDone
}

// A retitle committed after the judgment's close: memory keeps the new title, as the store has it.
func TestAutoDoneThenRetitleKeepsTheNewTitle(t *testing.T) {
	database := todoTestDB(t)
	st := &pausingAutoDoneStore{DB: database, stored: make(chan struct{}), release: make(chan struct{})}
	e := newTodoEnv(t, st)
	todo := e.create("old title")
	var retitled bool
	var retitleErr error
	raceAutoDone(t, e, st, todo.ID, func() {
		_, retitled, retitleErr = e.s.retitleTodo(context.Background(), todo.ID, "old title", "new title")
	})
	if !retitled || retitleErr != nil {
		t.Fatalf("retitleTodo = %v %v", retitled, retitleErr)
	}
	rows, err := database.ListTodos(context.Background())
	if err != nil || len(rows) != 1 || rows[0].Title != "new title" {
		t.Fatalf("store = %+v %v", rows, err)
	}
	if got := e.get(todo.ID); got.Title != "new title" || got.DoneAt == nil {
		t.Fatalf("memory title %q done %v; want the store's %q, done", got.Title, got.DoneAt != nil, rows[0].Title)
	}
}

// A failed start recorded after the judgment's close: memory keeps its start error, as the store has it.
func TestAutoDoneThenFailKeepsTheStartError(t *testing.T) {
	database := todoTestDB(t)
	st := &pausingAutoDoneStore{DB: database, stored: make(chan struct{}), release: make(chan struct{})}
	e := newTodoEnv(t, st)
	todo := e.create("修")
	var failErr error
	raceAutoDone(t, e, st, todo.ID, func() { failErr = e.s.failTodo(context.Background(), todo.ID, "agent did not start") })
	if failErr != nil {
		t.Fatal(failErr)
	}
	rows, err := database.ListTodos(context.Background())
	if err != nil || len(rows) != 1 || rows[0].StartError != "agent did not start" {
		t.Fatalf("store = %+v %v", rows, err)
	}
	if got := e.get(todo.ID); got.StartError != "agent did not start" {
		t.Fatalf("memory start error %q; want the store's %q", got.StartError, rows[0].StartError)
	}
}
