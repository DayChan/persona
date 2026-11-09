package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// workspaceStore models the transactional workspace/agent contract without a node
// or Postgres. Hooks pause after commit, before a handler publishes the result.
type workspaceStore struct {
	Store
	todoStore
	mu            sync.Mutex
	rows          []db.TodoRow
	spaces        map[string]db.TodoSpaceRow
	agents        []db.TodoAgentRow
	after         func(string, string)
	autoBefore    func()
	autoCommitted chan struct{}
	fail          error
}

func newWorkspaceStore() *workspaceStore {
	now := time.Now().UTC()
	return &workspaceStore{Store: &memStore{vals: map[string][]byte{}}, rows: []db.TodoRow{
		{ID: "task_a", Title: "Task A", CreatedAt: now, UpdatedAt: now},
		{ID: "task_b", Title: "Task B", CreatedAt: now, UpdatedAt: now}}, spaces: map[string]db.TodoSpaceRow{}}
}
func (st *workspaceStore) ListTodos(context.Context) ([]db.TodoRow, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return slices.Clone(st.rows), nil
}
func (st *workspaceStore) ListAllTodoSpaces(context.Context) ([]db.TodoSpaceRow, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	var rows []db.TodoSpaceRow
	for _, row := range st.spaces {
		rows = append(rows, row)
	}
	return rows, nil
}
func (st *workspaceStore) ListAllTodoAgents(context.Context) ([]db.TodoAgentRow, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return slices.Clone(st.agents), nil
}
func (*workspaceStore) ListTodoThreads(context.Context) (map[string]db.TodoThread, error) {
	return nil, nil
}
func (*workspaceStore) ListTodoSources(context.Context) (map[string]db.TodoSource, error) {
	return nil, nil
}
func (st *workspaceStore) removeAgents(todo, key string) {
	st.agents = slices.DeleteFunc(st.agents, func(a db.TodoAgentRow) bool { return a.TodoID == todo && a.Source == "space" && a.Via == key })
}
func (st *workspaceStore) LinkTodoSpace(_ context.Context, todo, mid, space string, at time.Time) (string, error) {
	st.mu.Lock()
	if st.fail != nil {
		err := st.fail
		st.mu.Unlock()
		return "", err
	}
	key := spaceKey(mid, space)
	old := st.spaces[key].TodoID
	if old != todo {
		st.removeAgents(old, key)
	}
	st.spaces[key] = db.TodoSpaceRow{TodoID: todo, MachineID: mid, Space: space, LinkedAt: at}
	hook := st.after
	st.mu.Unlock()
	if hook != nil {
		hook("link", todo)
	}
	return old, nil
}
func (st *workspaceStore) UnlinkTodoSpace(_ context.Context, todo, mid, space string) error {
	st.mu.Lock()
	if st.fail != nil {
		err := st.fail
		st.mu.Unlock()
		return err
	}
	key := spaceKey(mid, space)
	if st.spaces[key].TodoID != todo {
		st.mu.Unlock()
		return db.ErrTodoNotFound
	}
	delete(st.spaces, key)
	st.removeAgents(todo, key)
	hook := st.after
	st.mu.Unlock()
	if hook != nil {
		hook("unlink", todo)
	}
	return nil
}
func (st *workspaceStore) LinkTodoAgentWithSource(_ context.Context, todo, mid, sid, title, cwd, agent, startError, source, via string, at time.Time) ([]string, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	for i := range st.agents {
		a := &st.agents[i]
		if a.MachineID == mid && a.SessionID == sid {
			a.ReleasedAt = &at
		}
	}
	st.agents = slices.DeleteFunc(st.agents, func(a db.TodoAgentRow) bool { return a.TodoID == todo && a.MachineID == mid && a.SessionID == sid })
	st.agents = append(st.agents, db.TodoAgentRow{TodoID: todo, MachineID: mid, SessionID: sid, Title: title, Cwd: cwd, Agent: agent, StartError: startError, Source: source, Via: via, LinkedAt: at})
	return nil, nil
}
func (st *workspaceStore) AutoLinkUnlinkedTodoAgent(ctx context.Context, todo, mid, sid, title, cwd, agent, startError, source, via string, at time.Time) (bool, []string, error) {
	if st.autoBefore != nil {
		st.autoBefore()
	}
	if st.autoCommitted != nil {
		defer close(st.autoCommitted)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, a := range st.agents {
		if a.MachineID == mid && a.SessionID == sid && (a.TodoID == todo || a.ReleasedAt == nil) {
			return false, nil, nil
		}
	}
	st.agents = append(st.agents, db.TodoAgentRow{TodoID: todo, MachineID: mid, SessionID: sid, Title: title, Cwd: cwd, Agent: agent, StartError: startError, Source: source, Via: via, LinkedAt: at})
	return true, nil, nil
}
func (*workspaceStore) UpdateTodoAgentSnapshot(context.Context, string, string, string, string) error {
	return nil
}
func workspacePath(todo string) string { return "/api/v1/todos/" + todo + "/spaces/machine/work" }
func assertWorkspaceOwner(t *testing.T, e *todoEnv, st todoStore, owner string) {
	t.Helper()
	for _, td := range e.list() {
		count := 0
		for _, sp := range td.Spaces {
			if sp.MachineID == "machine" && sp.Space == "work" {
				count++
			}
		}
		want := 0
		if td.ID == owner {
			want = 1
		}
		if count != want {
			t.Errorf("GET task %s has %d workspace links, want %d", td.ID, count, want)
		}
	}
	e.s.todos.mu.RLock()
	indexed := e.s.todos.spacesByKey["machine/work"]
	e.s.todos.mu.RUnlock()
	rows, err := st.ListAllTodoSpaces(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	durable := ""
	for _, row := range rows {
		if row.MachineID == "machine" && row.Space == "work" {
			durable = row.TodoID
		}
	}
	if indexed != owner || durable != owner {
		t.Errorf("owner index=%q store=%q, want %q", indexed, durable, owner)
	}
}

// Mutex waits are not durably blocking in testing/synctest. Observe the competing
// handler reaching its write lock or finishing, so serialization is allowed without
// a guessed delay or requiring the second transaction to commit ahead of the first.
func waitWorkspaceRequest(t *testing.T, done <-chan struct{}, method string) {
	t.Helper()
	waitFor(t, "competing workspace request at its write boundary", func() bool {
		select {
		case <-done:
			return true
		default:
		}
		buf := make([]byte, 1<<20)
		buf = buf[:runtime.Stack(buf, true)]
		for _, stack := range strings.Split(string(buf), "\n\n") {
			if strings.Contains(stack, "[sync.Mutex.Lock]") && strings.Contains(stack, "(*Service)."+method+"(") {
				return true
			}
		}
		return false
	})
}
func TestTodoWorkspaceCommitPublicationOrder(t *testing.T) {
	for _, scenario := range []string{"move-move", "unlink-move", "move-unlink"} {
		t.Run(scenario, func(t *testing.T) {
			st := newWorkspaceStore()
			e := newTodoEnv(t, st)
			e.must("GET", "/api/v1/todos", "")
			if scenario != "move-move" {
				e.must("PUT", workspacePath("task_a"), "")
			}
			committed, release := make(chan struct{}), make(chan struct{})
			st.after = func(_, _ string) {
				st.mu.Lock()
				hook := st.after
				st.after = nil
				st.mu.Unlock()
				if hook != nil {
					close(committed)
					<-release
				}
			}
			var once sync.Once
			defer once.Do(func() { close(release) })
			first := make(chan int, 1)
			firstMethod := "PUT"
			if scenario == "unlink-move" {
				firstMethod = "DELETE"
			}
			go func() { first <- e.do(firstMethod, workspacePath("task_a"), "").Code }()
			select {
			case <-committed:
			case <-time.After(5 * time.Second):
				t.Fatal("first transaction did not commit")
			}
			second, secondDone := make(chan int, 1), make(chan struct{})
			method, target, fn, owner := "PUT", "task_b", "linkTodoSpace", "task_b"
			if scenario == "move-unlink" {
				method, target, fn, owner = "DELETE", "task_a", "unlinkTodoSpace", ""
			}
			go func() { second <- e.do(method, workspacePath(target), "").Code; close(secondDone) }()
			waitWorkspaceRequest(t, secondDone, fn)
			once.Do(func() { close(release) })
			for _, ch := range []<-chan int{first, second} {
				select {
				case code := <-ch:
					if code != http.StatusOK {
						t.Errorf("request returned %d", code)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("workspace request deadlocked")
				}
			}
			assertWorkspaceOwner(t, e, st, owner)
			reloadTodos(t, e.s)
			assertWorkspaceOwner(t, e, st, owner)
		})
	}
}

// Pause the old request in the actual session-list RPC, after ownership publication.
// The newer move/unlink must complete and retire that request's remaining fanout.
type workspaceSessionHub struct {
	*fakeHub
	entered, release chan struct{}
}
type workspaceRequestKey struct{}

func (f *workspaceSessionHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == DeviceSessionsList && ctx.Value(workspaceRequestKey{}) != nil {
		close(f.entered)
		<-f.release
	}
	return f.fakeHub.DeviceCall(ctx, p)
}
func TestTodoWorkspaceSupersededFanout(t *testing.T) {
	for _, stored := range []bool{false, true} {
		for _, action := range []string{"move", "unlink", "delete"} {
			t.Run(action+map[bool]string{false: "/memory", true: "/store"}[stored], func(t *testing.T) {
				st := newWorkspaceStore()
				var store Store
				if stored {
					store = st
				}
				e := newTodoEnv(t, store)
				a, b := "task_a", "task_b"
				if !stored {
					a, b = e.create("Task A").ID, e.create("Task B").ID
				}
				h := &workspaceSessionHub{fakeHub: e.f, entered: make(chan struct{}), release: make(chan struct{})}
				e.s.hub = h
				e.f.setSessions("machine", proto.Session{SessionID: "existing", SpaceName: "work", Status: proto.StatusWorking})
				var once sync.Once
				defer once.Do(func() { close(h.release) })
				first := make(chan int, 1)
				go func() {
					r := httptest.NewRequest("PUT", workspacePath(a), nil).WithContext(context.WithValue(context.Background(), workspaceRequestKey{}, true))
					w := httptest.NewRecorder()
					e.s.ServeHTTP(w, r)
					first <- w.Code
				}()
				select {
				case <-h.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("session lookup not reached")
				}
				// An unrelated workspace write and reads remain available during node lookup.
				e.must("PUT", "/api/v1/todos/"+b+"/spaces/machine/other", "")
				e.must("GET", "/api/v1/todos", "")
				if action == "move" {
					e.must("PUT", workspacePath(b), "")
					if code := e.do("DELETE", workspacePath(a), "").Code; code != http.StatusNotFound {
						t.Fatalf("unlink of former owner returned %d", code)
					}
				} else if action == "delete" {
					e.must("DELETE", "/api/v1/todos/"+a, "")
				} else {
					e.must("DELETE", workspacePath(a), "")
				}
				once.Do(func() { close(h.release) })
				select {
				case code := <-first:
					if code != http.StatusOK {
						t.Fatal(code)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("first request deadlocked")
				}
				owner := b
				if action != "move" {
					owner = ""
				}
				if got := e.todoOf("machine", "existing"); got != owner {
					t.Errorf("superseded fanout routed existing agent to %q, want %q", got, owner)
				}
				e.sessions("machine", proto.Session{SessionID: "future", SpaceName: "work", Status: proto.StatusWorking})
				if got := e.todoOf("machine", "future"); got != owner {
					t.Errorf("future agent routed to %q, want %q", got, owner)
				}
				if stored {
					waitFor(t, "automatic agent persistence", func() bool {
						rows, _ := st.ListAllTodoAgents(context.Background())
						for _, row := range rows {
							if row.SessionID == "future" && row.TodoID == owner {
								return true
							}
						}
						return owner == ""
					})
					assertWorkspaceOwner(t, e, st, owner)
					reloadTodos(t, e.s)
					if got := e.todoOf("machine", "existing"); got != owner {
						t.Errorf("reloaded existing agent owner=%q, want %q", got, owner)
					}
				}
			})
		}
	}
}
func TestTodoWorkspaceFailedWriteRetry(t *testing.T) {
	st := newWorkspaceStore()
	e := newTodoEnv(t, st)
	e.f.setSessions("machine", proto.Session{SessionID: "existing", SpaceName: "work", Status: proto.StatusWorking})
	e.must("PUT", workspacePath("task_a"), "")
	for _, method := range []string{"PUT", "DELETE"} {
		st.fail = errors.New("synthetic transaction failure")
		target := "task_a"
		if method == "PUT" {
			target = "task_b"
		}
		if code := e.do(method, workspacePath(target), "").Code; code != http.StatusInternalServerError {
			t.Fatal(code)
		}
		assertWorkspaceOwner(t, e, st, "task_a")
		if got := e.todoOf("machine", "existing"); got != "task_a" {
			t.Errorf("failed write changed agent owner to %q", got)
		}
		reloadTodos(t, e.s)
		assertWorkspaceOwner(t, e, st, "task_a")
		st.fail = nil
		e.must(method, workspacePath(target), "")
		owner := "task_b"
		if method == "DELETE" {
			owner = ""
		}
		assertWorkspaceOwner(t, e, st, owner)
		if got := e.todoOf("machine", "existing"); got != owner {
			t.Errorf("retried write left agent owner=%q, want %q", got, owner)
		}
		reloadTodos(t, e.s)
		assertWorkspaceOwner(t, e, st, owner)
		e.must("PUT", workspacePath("task_a"), "")
	}
}

func (st *workspaceStore) DeleteTodo(_ context.Context, todo string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.rows = slices.DeleteFunc(st.rows, func(row db.TodoRow) bool { return row.ID == todo })
	for key, row := range st.spaces {
		if row.TodoID == todo {
			delete(st.spaces, key)
		}
	}
	st.agents = slices.DeleteFunc(st.agents, func(row db.TodoAgentRow) bool { return row.TodoID == todo })
	return nil
}
func (st *workspaceStore) RenameTodoSpace(_ context.Context, mid, old, newName string) (bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	oldKey, newKey := spaceKey(mid, old), spaceKey(mid, newName)
	row, ok := st.spaces[oldKey]
	if !ok {
		return false, nil
	}
	delete(st.spaces, oldKey)
	if _, exists := st.spaces[newKey]; exists {
		st.removeAgents(row.TodoID, oldKey)
		return true, nil
	}
	row.Space = newName
	st.spaces[newKey] = row
	for i := range st.agents {
		if st.agents[i].Source == "space" && st.agents[i].Via == oldKey {
			st.agents[i].Via = newKey
		}
	}
	return false, nil
}

func TestTodoWorkspacePendingAutomaticWrite(t *testing.T) {
	for _, action := range []string{"move", "unlink", "rename"} {
		t.Run(action, func(t *testing.T) {
			st := newWorkspaceStore()
			e := newTodoEnv(t, st)
			e.must("PUT", workspacePath("task_a"), "")
			entered, release := make(chan struct{}), make(chan struct{})
			st.autoBefore = func() { close(entered); <-release }
			st.autoCommitted = make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			e.sessions("machine", proto.Session{SessionID: "discovered", SpaceName: "work", Status: proto.StatusWorking})
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("automatic store write not reached")
			}
			done := make(chan struct{})
			code := http.StatusOK
			fn := "linkTodoSpace"
			if action == "unlink" {
				fn = "unlinkTodoSpace"
			} else if action == "rename" {
				fn = "TodoSpaceRenamed"
			}
			go func() {
				switch action {
				case "move":
					code = e.do("PUT", workspacePath("task_b"), "").Code
				case "unlink":
					code = e.do("DELETE", workspacePath("task_a"), "").Code
				case "rename":
					e.s.TodoSpaceRenamed(context.Background(), TodoSpaceRenamed{MachineID: "machine", Old: "work", New: "renamed"})
				}
				close(done)
			}()
			waitWorkspaceRequest(t, done, fn)
			once.Do(func() { close(release) })
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("workspace mutation deadlocked")
			}
			if code != http.StatusOK {
				t.Fatal(code)
			}
			select {
			case <-st.autoCommitted:
			case <-time.After(5 * time.Second):
				t.Fatal("automatic transaction did not finish")
			}
			// Joining writing waits for any still-finishing automatic transaction; no guessed sleep.
			e.s.todos.writing.Lock()
			e.s.todos.writing.Unlock()
			owner := map[string]string{"move": "task_b", "unlink": "", "rename": "task_a"}[action]
			if got := e.todoOf("machine", "discovered"); got != owner {
				t.Errorf("live agent owner=%q, want %q", got, owner)
			}
			rows, _ := st.ListAllTodoAgents(context.Background())
			for _, row := range rows {
				if row.ReleasedAt == nil && (row.TodoID != owner || action == "rename" && row.Via != "machine/renamed") {
					t.Errorf("stale durable agent after %s: %+v", action, row)
				}
			}
			reloadTodos(t, e.s)
			if got := e.todoOf("machine", "discovered"); got != owner {
				t.Errorf("reloaded agent owner=%q, want %q", got, owner)
			}
		})
	}
}

func TestTodoWorkspaceKeepsExplicitMembershipSources(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
		target := e.create("Workspace task")
		e.must("PUT", "/api/v1/todos/"+target.ID+"/agents/machine/manual", "")
		e.sessions("machine", proto.Session{SessionID: "spawned", SpaceName: "work", SpawnFrom: target.ID + "@machine/manual", Status: proto.StatusWorking})
		if database != nil {
			waitFor(t, "spawned link stored", func() bool { rows, _ := database.ListAllTodoAgents(context.Background()); return len(rows) == 2 })
		}
		e.f.setSessions("machine", proto.Session{SessionID: "manual", SpaceName: "work", Status: proto.StatusWorking}, proto.Session{SessionID: "spawned", SpaceName: "work", Status: proto.StatusWorking})
		e.must("PUT", workspacePath(target.ID), "")
		e.must("PUT", workspacePath(target.ID), "")
		e.must("DELETE", workspacePath(target.ID), "")
		check := func() {
			td := e.get(target.ID)
			for sid, source := range map[string]string{"manual": "manual", "spawned": "spawned"} {
				a := agentOf(*td, sid)
				if a == nil || a.Source != source || a.ReleasedAt != nil || e.todoOf("machine", sid) != target.ID {
					t.Errorf("%s membership changed: %+v", source, a)
				}
			}
		}
		check()
		if database != nil {
			reloadTodos(t, e.s)
			check()
		}
	})
}

// Apply the same commit barrier to the real Postgres transaction in hosted CI.
type workspaceCommitGate struct {
	Store
	todoStore
	committed, release chan struct{}
	first              atomic.Bool
}

func (st *workspaceCommitGate) LinkTodoSpace(ctx context.Context, todo, mid, space string, at time.Time) (string, error) {
	old, err := st.todoStore.LinkTodoSpace(ctx, todo, mid, space, at)
	if err == nil && st.first.CompareAndSwap(false, true) {
		close(st.committed)
		<-st.release
	}
	return old, err
}
func TestTodoWorkspacePostgresPublicationOrder(t *testing.T) {
	database := todoTestDB(t)
	seedTodos(t, database, db.TodoRow{ID: "task_a", Title: "Task A"}, db.TodoRow{ID: "task_b", Title: "Task B"})
	st := &workspaceCommitGate{Store: database, todoStore: database, committed: make(chan struct{}), release: make(chan struct{})}
	e := newTodoEnv(t, st)
	e.must("GET", "/api/v1/todos", "")
	var once sync.Once
	defer once.Do(func() { close(st.release) })
	first := make(chan int, 1)
	go func() { first <- e.do("PUT", workspacePath("task_a"), "").Code }()
	select {
	case <-st.committed:
	case <-time.After(5 * time.Second):
		t.Fatal("first transaction did not commit")
	}
	second, done := make(chan int, 1), make(chan struct{})
	go func() { second <- e.do("PUT", workspacePath("task_b"), "").Code; close(done) }()
	waitWorkspaceRequest(t, done, "linkTodoSpace")
	once.Do(func() { close(st.release) })
	for _, ch := range []<-chan int{first, second} {
		select {
		case code := <-ch:
			if code != http.StatusOK {
				t.Fatal(code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("request deadlocked")
		}
	}
	assertWorkspaceOwner(t, e, st, "task_b")
	reloadTodos(t, e.s)
	assertWorkspaceOwner(t, e, st, "task_b")
}
