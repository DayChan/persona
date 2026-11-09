package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

type receiptSnapshotTestStore struct {
	memStore
	todoStore
	todos       []db.TodoRow
	laterTodos  []db.TodoRow
	agents      []db.TodoAgentRow
	todoReads   int
	agentReads  int
	failTodoAt  int
	failAgentAt int
	failCAS     bool
}

func (st *receiptSnapshotTestStore) ListTodos(context.Context) ([]db.TodoRow, error) {
	st.todoReads++
	if st.todoReads == st.failTodoAt {
		return nil, errors.New("todo read failed")
	}
	if st.todoReads > 1 && st.laterTodos != nil {
		return append([]db.TodoRow(nil), st.laterTodos...), nil
	}
	return append([]db.TodoRow(nil), st.todos...), nil
}

func (st *receiptSnapshotTestStore) ListAllTodoAgents(context.Context) ([]db.TodoAgentRow, error) {
	st.agentReads++
	if st.agentReads == st.failAgentAt {
		return nil, errors.New("agent read failed")
	}
	return append([]db.TodoAgentRow(nil), st.agents...), nil
}

func (st *receiptSnapshotTestStore) CompareSetting(ctx context.Context, key string, revision int64, value any) (bool, error) {
	if st.failCAS {
		return false, errors.New("setting write failed")
	}
	return st.memStore.CompareSetting(ctx, key, revision, value)
}

func receiptSnapshotService(t *testing.T, st *receiptSnapshotTestStore, looks []receiptLook) *Service {
	t.Helper()
	if err := st.PutSetting(context.Background(), receiptLooksSetting, receiptLookState{Revision: 1, Looks: looks}); err != nil {
		t.Fatal(err)
	}
	return New(Options{Hub: newFakeHub(), Store: func() Store { return st }})
}

func TestPruneReceiptLooksBatchesActiveLinks(t *testing.T) {
	ctx := context.Background()
	linked := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	st := &receiptSnapshotTestStore{
		todos:  []db.TodoRow{{ID: "a", Origin: "conv"}, {ID: "b", Origin: "conv"}},
		agents: []db.TodoAgentRow{{TodoID: "a", MachineID: "box", SessionID: "s1", LinkedAt: linked}, {TodoID: "b", MachineID: "box", SessionID: "s2", LinkedAt: linked}},
	}
	looks := []receiptLook{
		{Todo: "a", Machine: "box", Session: "s1", Linked: linked, ID: "one", Generation: "g1"},
		{Todo: "a", Machine: "box", Session: "s1", Linked: linked, ID: "two", Generation: "g2"},
		{Todo: "b", Machine: "box", Session: "s2", Linked: linked, ID: "three"},
	}
	s := receiptSnapshotService(t, st, looks)
	s.pruneReceiptLooks(ctx)
	var got receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &got); err != nil || len(got.Looks) != len(looks) {
		t.Fatalf("active waits = %+v, %v", got, err)
	}
	if st.todoReads != 1 || st.agentReads != 1 {
		t.Fatalf("table reads: todos=%d agents=%d", st.todoReads, st.agentReads)
	}
}

func TestPruneReceiptLooksRechecksReopenedTask(t *testing.T) {
	ctx := context.Background()
	linked := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	done := linked.Add(time.Hour)
	st := &receiptSnapshotTestStore{
		todos:      []db.TodoRow{{ID: "task", Origin: "conv", DoneAt: &done}},
		laterTodos: []db.TodoRow{{ID: "task", Origin: "conv"}},
		agents:     []db.TodoAgentRow{{TodoID: "task", MachineID: "box", SessionID: "s1", LinkedAt: linked}},
	}
	s := receiptSnapshotService(t, st, []receiptLook{{Todo: "task", Machine: "box", Session: "s1", Linked: linked, ID: "wait"}})
	s.pruneReceiptLooks(ctx)
	var got receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &got); err != nil || len(got.Looks) != 1 {
		t.Fatalf("reopened task lost wait = %+v, %v", got, err)
	}
	if st.todoReads != 2 || st.agentReads != 2 {
		t.Fatalf("recheck reads: todos=%d agents=%d", st.todoReads, st.agentReads)
	}
}

func TestPruneReceiptLooksKeepsConfirmedCancellationWhenOtherRecheckFails(t *testing.T) {
	ctx := context.Background()
	linked := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	done := linked.Add(time.Hour)
	st := &receiptSnapshotTestStore{
		todos: []db.TodoRow{{ID: "a", Origin: "conv", DoneAt: &done}, {ID: "b", Origin: "conv"}},
		agents: []db.TodoAgentRow{
			{TodoID: "a", MachineID: "box", SessionID: "a", LinkedAt: linked},
			{TodoID: "b", MachineID: "box", SessionID: "b", LinkedAt: linked.Add(time.Minute)},
		},
		failAgentAt: 2, // Snapshot succeeds; B's individual recheck fails.
	}
	looks := []receiptLook{
		{Todo: "a", Machine: "box", Session: "a", Linked: linked, ID: "cancelled"},
		{Todo: "b", Machine: "box", Session: "b", Linked: linked, ID: "uncertain"},
	}
	s := receiptSnapshotService(t, st, looks)
	s.pruneReceiptLooks(ctx)
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].ID != "uncertain" {
		t.Fatalf("individual read failure discarded a confirmed cancellation: %+v, %v", state, err)
	}
	// A is reopened with the same link after the transient B failure clears.
	// Its old wait must not return; B becomes valid and stays pending.
	st.todos[0].DoneAt = nil
	st.agents[1].LinkedAt = linked
	st.failAgentAt = 0
	s.pruneReceiptLooks(ctx)
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].ID != "uncertain" {
		t.Fatalf("cancelled wait revived after reopen: %+v, %v", state, err)
	}
}

func TestPruneReceiptLooksKeepsAllOnStoreFailure(t *testing.T) {
	for _, mode := range []string{"snapshot-todos", "snapshot-agents", "recheck-todos", "recheck-agents", "save"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			linked := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
			st := &receiptSnapshotTestStore{
				todos:  []db.TodoRow{{ID: "task", Origin: "conv"}},
				agents: []db.TodoAgentRow{{TodoID: "task", MachineID: "box", SessionID: "s1", LinkedAt: linked}},
			}
			looks := []receiptLook{
				{Todo: "task", Machine: "box", Session: "s1", Linked: linked, ID: "active"},
				{Todo: "task", Machine: "box", Session: "s1", Linked: linked.Add(-time.Minute), ID: "stale", Generation: "old"},
			}
			s := receiptSnapshotService(t, st, looks)
			switch mode {
			case "snapshot-todos":
				st.failTodoAt = 1
			case "snapshot-agents":
				st.failAgentAt = 1
			case "recheck-todos":
				st.failTodoAt = 2
			case "recheck-agents":
				st.failAgentAt = 2
			case "save":
				st.failCAS = true
			}
			s.pruneReceiptLooks(ctx)
			var got receiptLookState
			if _, err := st.Setting(ctx, receiptLooksSetting, &got); err != nil || len(got.Looks) != len(looks) {
				t.Fatalf("store failure lost waits = %+v, %v", got, err)
			}
			if len(s.tasks.receiptLooks) != len(looks) {
				t.Fatalf("store failure lost cached waits: %+v", s.tasks.receiptLooks)
			}
		})
	}
}

func TestPruneReceiptLooksRemovesOnlyInvalidLinks(t *testing.T) {
	ctx := context.Background()
	linked := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	done := linked.Add(time.Hour)
	st := &receiptSnapshotTestStore{
		todos: []db.TodoRow{{ID: "valid", Origin: "conv"}, {ID: "done", Origin: "conv", DoneAt: &done}, {ID: "manual"}, {ID: "released", Origin: "conv"}, {ID: "dismissed", Origin: "conv"}, {ID: "missing-agent", Origin: "conv"}},
		agents: []db.TodoAgentRow{
			{TodoID: "valid", MachineID: "box", SessionID: "v", LinkedAt: linked},
			{TodoID: "done", MachineID: "box", SessionID: "d", LinkedAt: linked},
			{TodoID: "manual", MachineID: "box", SessionID: "m", LinkedAt: linked},
			{TodoID: "released", MachineID: "box", SessionID: "r", LinkedAt: linked, ReleasedAt: &done},
			{TodoID: "dismissed", MachineID: "box", SessionID: "x", LinkedAt: linked, DismissedAt: &done},
		},
	}
	looks := []receiptLook{
		{Todo: "valid", Machine: "box", Session: "v", Linked: linked, ID: "valid"},
		{Todo: "done", Machine: "box", Session: "d", Linked: linked, ID: "done"},
		{Todo: "manual", Machine: "box", Session: "m", Linked: linked, ID: "manual"},
		{Todo: "released", Machine: "box", Session: "r", Linked: linked, ID: "released"},
		{Todo: "dismissed", Machine: "box", Session: "x", Linked: linked, ID: "dismissed"},
		{Todo: "missing-agent", Machine: "box", Session: "z", ID: "missing-agent"},
		{Todo: "missing-task", Machine: "box", Session: "n", Linked: linked, ID: "missing-task"},
	}
	s := receiptSnapshotService(t, st, looks)
	s.pruneReceiptLooks(ctx)
	var got receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &got); err != nil || len(got.Looks) != 1 || got.Looks[0].ID != "valid" {
		t.Fatalf("pruned waits = %+v, %v", got, err)
	}
	restarted := New(Options{Hub: newFakeHub(), Store: func() Store { return st }})
	restarted.tasks.receiptMu.Lock()
	err := restarted.receiptLooksLocked(ctx)
	remaining := copyReceiptLooks(restarted.tasks.receiptLooks)
	restarted.tasks.receiptMu.Unlock()
	if err != nil || len(remaining) != 1 {
		t.Fatalf("waits after manager replacement = %+v, %v", remaining, err)
	}
	for _, look := range remaining {
		if look.ID != "valid" {
			t.Fatalf("unexpected restored wait: %+v", look)
		}
	}
}

type deadlineBlockedSessionsHub struct {
	*fakeHub
	entered chan struct{}
	once    sync.Once
}

func (h *deadlineBlockedSessionsHub) DeviceCall(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
	if call.Method == DeviceSessionsList {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if call.Device == "other" {
			h.once.Do(func() { close(h.entered) })
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return h.fakeHub.DeviceCall(ctx, call)
}

type contextIgnoringSessionsHub struct {
	*fakeHub
	entered chan struct{}
	release chan struct{}
	exited  chan struct{}
}

func (h *contextIgnoringSessionsHub) DeviceCall(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
	if call.Method == DeviceSessionsList && call.Device == "other" {
		close(h.entered)
		<-h.release // Deliberately ignores ctx until the test releases the RPC.
		close(h.exited)
		return nil, ctx.Err()
	}
	return h.fakeHub.DeviceCall(ctx, call)
}

type blockedReceiptStore struct {
	restartReceiptStore
	entered chan struct{}
	release chan struct{}
	block   sync.Once
	todo    Todo
}

func (st *blockedReceiptStore) setTodo(todo Todo) {
	st.muFeed.Lock()
	st.todo = todo
	st.muFeed.Unlock()
}

func (st *blockedReceiptStore) AppendTaskReceiptOnce(ctx context.Context, row db.FeedRow, machine, session string, linked time.Time) (bool, bool, error) {
	st.muFeed.Lock()
	current, active := receiptLink(st.todo, SessionRef{MachineID: machine, SessionID: session})
	valid := st.todo.ID == row.Ref && st.todo.Origin == row.Conversation && st.todo.DoneAt == nil && active && current.Equal(linked)
	st.muFeed.Unlock()
	if !valid {
		return false, false, nil
	}
	inserted, err := st.AppendFeedOnce(ctx, row)
	return inserted, true, err
}

func (st *blockedReceiptStore) AppendFeedOnce(_ context.Context, row db.FeedRow) (bool, error) {
	st.muFeed.Lock()
	inserted := true
	for _, prior := range st.rows {
		if prior.ID == row.ID {
			inserted = false
			break
		}
	}
	if inserted {
		st.rows = append(st.rows, row)
	}
	st.muFeed.Unlock()
	st.block.Do(func() { close(st.entered); <-st.release })
	return inserted, nil
}

type receiptSettingFailureStore struct {
	memStore
	faultMu   sync.Mutex
	failRead  bool
	failWrite bool
	feed      []db.FeedRow
}

func (st *receiptSettingFailureStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	st.faultMu.Lock()
	fail := key == receiptLooksSetting && st.failRead
	st.faultMu.Unlock()
	if fail {
		return false, errors.New("setting unavailable")
	}
	return st.memStore.Setting(ctx, key, out)
}

func (st *receiptSettingFailureStore) AppendFeed(_ context.Context, row db.FeedRow) error {
	st.faultMu.Lock()
	defer st.faultMu.Unlock()
	st.feed = append(st.feed, row)
	return nil
}

func (st *receiptSettingFailureStore) FeedItem(_ context.Context, id string) (*db.FeedRow, error) {
	st.faultMu.Lock()
	defer st.faultMu.Unlock()
	for _, row := range st.feed {
		if row.ID == id {
			copy := row
			return &copy, nil
		}
	}
	return nil, nil
}

func (st *receiptSettingFailureStore) AppendFeedOnce(_ context.Context, row db.FeedRow) (bool, error) {
	st.faultMu.Lock()
	defer st.faultMu.Unlock()
	for _, item := range st.feed {
		if item.ID == row.ID {
			return false, nil
		}
	}
	st.feed = append(st.feed, row)
	return true, nil
}

func (st *receiptSettingFailureStore) CompareSetting(ctx context.Context, key string, revision int64, value any) (bool, error) {
	st.faultMu.Lock()
	fail := key == receiptLooksSetting && st.failWrite
	st.faultMu.Unlock()
	if fail {
		return false, errors.New("setting unavailable")
	}
	return st.memStore.CompareSetting(ctx, key, revision, value)
}

type unlinkedReceiptStore struct{ receiptSettingFailureStore }

func (st *unlinkedReceiptStore) AppendTaskReceiptOnce(_ context.Context, _ db.FeedRow, _, _ string, _ time.Time) (bool, bool, error) {
	return false, false, nil
}

type receiptOnceStore struct {
	memStore
	feedMu       sync.Mutex
	feed         map[string]db.FeedRow
	loseFirstAck bool
	failFeedRead bool
}

func (st *receiptOnceStore) AppendFeedOnce(_ context.Context, row db.FeedRow) (bool, error) {
	st.feedMu.Lock()
	defer st.feedMu.Unlock()
	if st.feed == nil {
		st.feed = make(map[string]db.FeedRow)
	}
	if _, found := st.feed[row.ID]; found {
		return false, nil
	}
	st.feed[row.ID] = row
	if st.loseFirstAck {
		st.loseFirstAck = false
		return false, errors.New("lost acknowledgment")
	}
	return true, nil
}

func (st *receiptOnceStore) AppendFeed(_ context.Context, row db.FeedRow) error {
	st.feedMu.Lock()
	defer st.feedMu.Unlock()
	if st.feed == nil {
		st.feed = make(map[string]db.FeedRow)
	}
	st.feed[row.ID] = row
	return nil
}

func (st *receiptOnceStore) FeedItem(_ context.Context, id string) (*db.FeedRow, error) {
	st.feedMu.Lock()
	defer st.feedMu.Unlock()
	if st.failFeedRead {
		return nil, errors.New("feed unavailable")
	}
	row, ok := st.feed[id]
	if !ok {
		return nil, nil
	}
	return &row, nil
}

type receiptFailureStore struct {
	memStore
	fail bool
	rows []db.FeedRow
}

func (st *receiptFailureStore) AppendFeed(_ context.Context, row db.FeedRow) error {
	if st.fail {
		return errors.New("feed unavailable")
	}
	st.rows = append(st.rows, row)
	return nil
}

func TestTaskReceiptFeedFailureAllowsRetry(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub()
	st := &receiptFailureStore{fail: true}
	s := newServiceOn(t, f, st)
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	s.todos.todos["task"] = &Todo{ID: "task", Title: "Task", Origin: "conv", CreatedAt: time.Now().Add(-time.Hour),
		Agents: []TodoAgent{{MachineID: "box", SessionID: "s1"}}}
	s.todos.activeBySession[[2]string{"box", "s1"}] = "task"
	s.todos.mu.Unlock()
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, LastText: "Done"})
	s.taskTurnEnded(ctx, "box", "s1")
	s.tasks.mu.Lock()
	_, reserved := s.tasks.receipted["task"]
	s.tasks.mu.Unlock()
	if reserved || len(st.rows) != 0 {
		t.Fatal("failed feed write consumed the receipt")
	}
	st.fail = false
	s.taskTurnEnded(ctx, "box", "s1")
	s.taskTurnEnded(ctx, "box", "s1")
	if len(st.rows) != 1 || st.rows[0].Kind != feedReceipt {
		t.Fatalf("feed rows after retry = %+v", st.rows)
	}
}

func receiptTestService(t *testing.T, f *fakeHub, st Store, linked time.Time) *Service {
	t.Helper()
	s := newServiceOn(t, f, st)
	if err := s.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	s.todos.todos["task"] = &Todo{ID: "task", Origin: "conv", CreatedAt: linked, Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", LinkedAt: linked}}}
	s.todos.activeBySession[[2]string{"box", "s1"}] = "task"
	s.todos.mu.Unlock()
	return s
}

func TestReceiptLookRestoresAcrossRestart(t *testing.T) {
	for _, overdue := range []bool{false, true} {
		t.Run(map[bool]string{false: "before deadline", true: "after deadline"}[overdue], func(t *testing.T) {
			st := &memStore{}
			f := newFakeHub()
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
			linked := time.Now().Add(-time.Hour)
			first := receiptTestService(t, f, st, linked)
			second := receiptTestService(t, f, st, linked)
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				first.restoreTaskReceiptLooks(ctx)
				due := time.Now().Add(time.Hour)
				first.taskTurnEndedAfter(ctx, "box", "s1", due)
				cancel()
				if overdue {
					<-time.After(2 * time.Hour)
				}
				nextCtx, stop := context.WithCancel(context.Background())
				defer stop()
				second.restoreTaskReceiptLooks(nextCtx)
				f.mu.Lock()
				before := len(f.calls)
				f.mu.Unlock()
				if overdue {
					<-time.After(31 * time.Second)
				} else {
					<-time.After(time.Hour)
				}
				synctest.Wait()
				f.mu.Lock()
				after := len(f.calls)
				f.mu.Unlock()
				if after <= before {
					t.Fatal("restored look did not ask the node")
				}
				var state receiptLookState
				if ok, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || !ok || len(state.Looks) != 1 {
					t.Fatalf("unfinished look not retained: %+v %v", state, err)
				}
			})
		})
	}
}

func TestReceiptLookRegistrationRetriesStoreFailures(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(map[bool]string{false: "read", true: "write"}[write], func(t *testing.T) {
			st := &receiptSettingFailureStore{}
			f := newFakeHub()
			s := receiptTestService(t, f, st, time.Now().Add(-time.Hour))
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				s.restoreTaskReceiptLooks(ctx)
				st.faultMu.Lock()
				st.failRead, st.failWrite = !write, write
				st.faultMu.Unlock()
				s.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Hour))
				s.tasks.mu.Lock()
				n := len(s.tasks.limitLooks)
				s.tasks.mu.Unlock()
				if n != 0 {
					t.Fatal("failed store write fell back to an in-memory wait")
				}
				st.faultMu.Lock()
				st.failRead, st.failWrite = false, false
				st.faultMu.Unlock()
				<-time.After(time.Minute)
				synctest.Wait()
				var state receiptLookState
				if ok, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || !ok || len(state.Looks) != 1 {
					t.Fatalf("registration was not retried: %+v %v", state, err)
				}
			})
		})
	}
}

func TestReceiptLookRequiresTaskWithStore(t *testing.T) {
	st := &memStore{}
	f := newFakeHub()
	s := newServiceOn(t, f, st)
	s.taskTurnEndedAfter(context.Background(), "box", "missing", time.Now().Add(time.Hour))
	s.tasks.mu.Lock()
	n := len(s.tasks.limitLooks)
	s.tasks.mu.Unlock()
	if n != 0 {
		t.Fatal("unassociated persistent request became an in-memory wait")
	}
}

func TestReceiptLookRetriesAnOfflineNode(t *testing.T) {
	st := &memStore{}
	f := newFakeHub()
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
	f.offline["box"] = true
	s := receiptTestService(t, f, st, time.Now().Add(-time.Hour))
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.restoreTaskReceiptLooks(ctx)
		s.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Minute))
		<-time.After(time.Minute + 3*time.Second)
		synctest.Wait()
		var state receiptLookState
		if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || !state.Looks[0].At.After(time.Now()) {
			t.Fatalf("offline wait was not postponed: %+v %v", state, err)
		}
		f.mu.Lock()
		before := len(f.calls)
		f.offline["box"] = false
		f.mu.Unlock()
		<-time.After(3 * time.Minute)
		synctest.Wait()
		f.mu.Lock()
		after := len(f.calls)
		f.mu.Unlock()
		if after <= before {
			t.Fatal("postponed look did not retry the node")
		}
	})
}

func TestReceiptLookCancelledAndReplaced(t *testing.T) {
	st := &memStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour)
	s := receiptTestService(t, f, st, linked)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.restoreTaskReceiptLooks(ctx)
		at := time.Now().Add(time.Hour)
		s.taskTurnEndedAfter(ctx, "box", "s1", at)
		s.todos.mu.Lock()
		s.todos.todos["task2"] = &Todo{ID: "task2", Origin: "conv", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", LinkedAt: linked.Add(time.Minute)}}}
		s.todos.activeBySession[[2]string{"box", "s1"}] = "task2"
		s.todos.mu.Unlock()
		s.taskTurnEndedAfter(ctx, "box", "s1", at)
		<-time.After(time.Hour)
		synctest.Wait()
		var state receiptLookState
		if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].Todo != "task2" {
			t.Fatalf("old timer cleared replacement: %+v %v", state, err)
		}
		s.todos.mu.Lock()
		delete(s.todos.activeBySession, [2]string{"box", "s1"})
		s.todos.mu.Unlock()
		s.pruneReceiptLooks(ctx)
		if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 0 {
			t.Fatalf("cancelled look retained: %+v %v", state, err)
		}
	})
}

func TestReceiptLookRestoresFollowupAfterFeedWrite(t *testing.T) {
	st := &receiptOnceStore{}
	f := newFakeHub()
	linked := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	first := receiptTestService(t, f, st, linked)
	second := receiptTestService(t, f, st, linked)
	second.todos.mu.Lock()
	second.todos.todos["task"].Quiet = true
	second.todos.mu.Unlock()
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone,
		State: proto.StateUnread, TurnError: "service unavailable"})
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		first.restoreTaskReceiptLooks(ctx)
		first.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Minute))
		ref := SessionRef{MachineID: "box", SessionID: "s1"}
		if _, err := first.appendTaskReceipt(ctx, todoNow(first, "task"), ref, receiptBody{State: "interrupted"}); err != nil {
			t.Fatal(err)
		}
		// The old manager stopped after the feed insert, before the notice and report.
		cancel()
		restarted, stop := context.WithCancel(context.Background())
		defer stop()
		second.restoreTaskReceiptLooks(restarted)
		<-time.After(time.Minute)
		synctest.Wait()
		st.feedMu.Lock()
		n := len(st.feed)
		st.feedMu.Unlock()
		second.quiet.mu.Lock()
		notice := second.quiet.pending["task"]
		second.quiet.mu.Unlock()
		if n != 1 || notice == nil || notice.state != "interrupted" {
			t.Fatalf("feed count %d, follow-up notice %+v", n, notice)
		}
		var state receiptLookState
		if _, err := st.Setting(restarted, receiptLooksSetting, &state); err != nil || len(state.Looks) != 0 {
			t.Fatalf("followed-up look not cleared: %+v %v", state, err)
		}
	})
}

func TestReceiptLookDoesNotGuessJudgmentAfterFeedWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &receiptOnceStore{}
	f := newFakeHub()
	f.judge = `{"verdict":"done","reason":"Verified"}`
	linked := time.Now().Add(-time.Hour)
	first := receiptTestService(t, f, st, linked)
	second := receiptTestService(t, f, st, linked)
	f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: "Do the work"},
		proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Work verified"})
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone,
		State: proto.StateUnread, LastText: "Work verified", UpdatedAt: time.Now()})
	first.restoreTaskReceiptLooks(ctx)
	first.taskWorking(ctx, "box", "s1")
	todo := todoNow(first, "task")
	first.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", time.Now().Add(time.Hour))
	ref := SessionRef{MachineID: "box", SessionID: "s1"}
	if _, err := first.appendTaskReceipt(ctx, todoNow(first, "task"), ref, receiptBody{State: "done"}); err != nil {
		t.Fatal(err)
	}
	// Recreate the manager after the feed write and run its recovered check.
	second.taskTurnEnded(ctx, "box", "s1")
	f.mu.Lock()
	judged := f.judged
	f.mu.Unlock()
	if judged != 0 {
		t.Fatal("recovered receipt guessed the current work generation")
	}
	st.feedMu.Lock()
	n := len(st.feed)
	st.feedMu.Unlock()
	if n != 1 {
		t.Fatalf("feed has %d receipts", n)
	}
}

func TestReceiptLookLostFeedAcknowledgment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := &receiptOnceStore{loseFirstAck: true}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour)
	first := receiptTestService(t, f, st, linked)
	second := receiptTestService(t, f, st, linked)
	first.restoreTaskReceiptLooks(ctx)
	first.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Hour))
	ref := SessionRef{MachineID: "box", SessionID: "s1"}
	if _, err := first.appendTaskReceipt(ctx, todoNow(first, "task"), ref, receiptBody{State: "done"}); err == nil {
		t.Fatal("lost acknowledgment was not reported")
	}
	receiptID, err := second.appendTaskReceipt(ctx, todoNow(second, "task"), ref, receiptBody{State: "done"})
	if err != nil || receiptID == "" {
		t.Fatalf("repeat insert = %q, %v", receiptID, err)
	}
	second.finishTaskReceipt(ctx, "task", receiptID)
	st.feedMu.Lock()
	n := len(st.feed)
	st.feedMu.Unlock()
	if n != 1 {
		t.Fatalf("feed has %d receipts", n)
	}
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 0 {
		t.Fatalf("completed look retained: %+v %v", state, err)
	}
}

func TestReceiptLookCASPreservesOtherManager(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	f := newFakeHub()
	a := receiptTestService(t, f, st, time.Now())
	b := receiptTestService(t, f, st, time.Now())
	ref1 := receiptKey{Ref: SessionRef{MachineID: "box", SessionID: "s1"}}
	ref2 := receiptKey{Ref: SessionRef{MachineID: "box", SessionID: "s2"}}
	a.tasks.receiptMu.Lock()
	if err := a.receiptLooksLocked(ctx); err != nil {
		t.Fatal(err)
	}
	b.tasks.receiptMu.Lock()
	if err := b.receiptLooksLocked(ctx); err != nil {
		t.Fatal(err)
	}
	b.tasks.receiptLooks[ref2] = receiptLook{Machine: "box", Session: "s2", Todo: "task2", ID: "new"}
	if err := b.saveReceiptLooksLocked(ctx); err != nil {
		t.Fatal(err)
	}
	b.tasks.receiptMu.Unlock()
	a.tasks.receiptLooks[ref1] = receiptLook{Machine: "box", Session: "s1", Todo: "task1", ID: "old"}
	if err := a.saveReceiptLooksLocked(ctx); err != nil {
		t.Fatal(err)
	}
	a.tasks.receiptMu.Unlock()
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil {
		t.Fatal(err)
	}
	if got := receiptLookMap(state); len(got) != 2 || got[ref1].ID != "old" || got[ref2].ID != "new" {
		t.Fatalf("lost look: %+v", got)
	}

	// A stale manager cannot delete a replacement of the same session.
	a.tasks.receiptMu.Lock()
	if err := a.receiptLooksLocked(ctx); err != nil {
		t.Fatal(err)
	}
	b.tasks.receiptMu.Lock()
	if err := b.receiptLooksLocked(ctx); err != nil {
		t.Fatal(err)
	}
	b.tasks.receiptLooks[ref1] = receiptLook{Machine: "box", Session: "s1", Todo: "task1", ID: "replacement"}
	if err := b.saveReceiptLooksLocked(ctx); err != nil {
		t.Fatal(err)
	}
	b.tasks.receiptMu.Unlock()
	delete(a.tasks.receiptLooks, ref1)
	if err := a.saveReceiptLooksLocked(ctx); err == nil {
		t.Fatal("stale deletion succeeded")
	}
	a.tasks.receiptMu.Unlock()
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil {
		t.Fatal(err)
	}
	if got := receiptLookMap(state)[ref1].ID; got != "replacement" {
		t.Fatalf("replacement = %q", got)
	}
}

func TestTaskTurnEndedUnknownInitialBindingDoesNotBindLaterTask(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := newServiceOn(t, f, st)
	failing := &failingStore{}
	failing.failLoad.Store(true)
	var holderMu sync.RWMutex
	var active Store = failing
	s.store = func() Store { holderMu.RLock(); defer holderMu.RUnlock(); return active }
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.tasks.receiptMu.Lock()
		s.tasks.receiptCtx = ctx
		s.tasks.receiptMu.Unlock()
		s.taskTurnEnded(ctx, "box", "s1")
		holderMu.Lock()
		active = st
		holderMu.Unlock()
		if err := s.ensureTodosLoaded(ctx); err != nil {
			t.Fatal(err)
		}
		s.todos.mu.Lock()
		s.todos.todos["other"] = &Todo{ID: "other", Origin: "other-conv", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", LinkedAt: time.Now()}}}
		s.todos.activeBySession[[2]string{"box", "s1"}] = "other"
		s.todos.mu.Unlock()
		f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "other work"})
		<-time.After(2 * time.Minute)
		synctest.Wait()
		st.faultMu.Lock()
		n := len(st.feed)
		st.faultMu.Unlock()
		if n != 0 {
			t.Fatalf("unbound old event wrote %d receipts for the later task", n)
		}
	})
}

func TestTaskTurnEndedLoadFailureDoesNotFollowRebind(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "error"})
	failing := &failingStore{}
	failing.failLoad.Store(true)
	var holderMu sync.RWMutex
	var active Store = failing
	s.store = func() Store { holderMu.RLock(); defer holderMu.RUnlock(); return active }
	s.todos.mu.Lock()
	s.todos.loaded = false
	s.todos.mu.Unlock()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.tasks.receiptMu.Lock()
		s.tasks.receiptCtx = ctx
		s.tasks.receiptMu.Unlock()
		s.taskTurnEnded(ctx, "box", "s1")
		holderMu.Lock()
		active = st
		holderMu.Unlock()
		s.todos.mu.Lock()
		s.todos.todos["other"] = &Todo{ID: "other", Origin: "other-conv", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", LinkedAt: time.Now()}}}
		s.todos.activeBySession[[2]string{"box", "s1"}] = "other"
		s.todos.loaded = true
		s.todos.mu.Unlock()
		f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "other work"})
		<-time.After(time.Minute)
		synctest.Wait()
		st.faultMu.Lock()
		n := len(st.feed)
		st.faultMu.Unlock()
		if n != 0 {
			t.Fatalf("old event wrote %d receipts for the new task", n)
		}
	})
}

func TestTaskTurnEndedRetriesTodoLoadFailure(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "error"})
	failing := &failingStore{}
	failing.failLoad.Store(true)
	var holderMu sync.RWMutex
	var active Store = failing
	s.store = func() Store { holderMu.RLock(); defer holderMu.RUnlock(); return active }
	s.todos.mu.Lock()
	s.todos.loaded = false
	s.todos.mu.Unlock()
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.tasks.receiptMu.Lock()
		s.tasks.receiptCtx = ctx
		s.tasks.receiptMu.Unlock()
		s.taskTurnEnded(ctx, "box", "s1")
		holderMu.Lock()
		active = st
		holderMu.Unlock()
		s.todos.mu.Lock()
		s.todos.loaded = true
		s.todos.mu.Unlock()
		<-time.After(time.Minute)
		synctest.Wait()
		st.faultMu.Lock()
		n := len(st.feed)
		st.faultMu.Unlock()
		if n != 1 {
			t.Fatalf("receipts after todo load recovery = %d", n)
		}
	})
}

func TestBoundReceiptCompletionClearsSameGenerationLook(t *testing.T) {
	ctx := context.Background()
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Now().Add(-time.Hour))
	s.taskWorking(ctx, "box", "s1")
	todo := todoNow(s, "task")
	s.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("wait = %+v, %v", state, err)
	}
	look := state.Looks[0]
	bound := look
	bound.ID = ""
	bound.RetryID = "bound-retry"
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "error"})
	s.taskTurnEndedForLook(ctx, "box", "s1", &bound)
	s.taskTurnEndedForLook(ctx, "box", "s1", &look)
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 0 {
		t.Fatalf("completed look retained = %+v, %v", state, err)
	}
	st.faultMu.Lock()
	n := len(st.feed)
	st.faultMu.Unlock()
	if n != 1 {
		t.Fatalf("receipts = %d", n)
	}
}

func TestBoundReceiptRetryUsesStableID(t *testing.T) {
	ctx := context.Background()
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	s := receiptTestService(t, f, st, linked)
	todo := todoNow(s, "task")
	ref := SessionRef{MachineID: "box", SessionID: "s1"}
	expected := &receiptLook{Todo: todo.ID, Machine: "box", Session: "s1", Linked: linked, Work: todo.CreatedAt, RetryID: "bound-retry"}
	s.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Hour))
	for range 2 {
		id, err := s.appendTaskReceiptForLook(ctx, todo, ref, receiptBody{State: "interrupted"}, todo.CreatedAt, false, expected)
		if err != nil || id != expected.RetryID {
			t.Fatalf("bound retry ID = %q, %v", id, err)
		}
	}
	st.faultMu.Lock()
	n := len(st.feed)
	st.faultMu.Unlock()
	if n != 1 {
		t.Fatalf("bound retry wrote %d rows", n)
	}
}

func TestBoundReceiptRetryRejectsPersistedUnlink(t *testing.T) {
	ctx := context.Background()
	st := &unlinkedReceiptStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	s := receiptTestService(t, f, st, linked)
	todo := todoNow(s, "task")
	expected := &receiptLook{Todo: todo.ID, Machine: "box", Session: "s1", Linked: linked, Work: todo.CreatedAt, RetryID: "bound-retry"}
	// The local cache still has the association; the store represents a committed unlink.
	_, err := s.appendTaskReceiptForLook(ctx, todo, SessionRef{MachineID: "box", SessionID: "s1"}, receiptBody{State: "interrupted"}, todo.CreatedAt, false, expected)
	if err == nil {
		t.Fatal("bound retry ignored the stored unlink")
	}
	st.faultMu.Lock()
	n := len(st.feed)
	st.faultMu.Unlock()
	if n != 0 {
		t.Fatalf("unlinked retry wrote %d feed rows", n)
	}
}

func TestTaskReceiptFirstCheckRetriesMissingSession(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))
	f.setSessions("box")
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.restoreTaskReceiptLooks(ctx)
		s.taskTurnEnded(ctx, "box", "s1")
		f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "error"})
		<-time.After(time.Minute)
		synctest.Wait()
		st.faultMu.Lock()
		n := len(st.feed)
		st.faultMu.Unlock()
		if n != 1 {
			t.Fatalf("receipt count after session appeared = %d", n)
		}
	})
}

func TestTaskReceiptRetryWaitsForMissingSession(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))
	ended := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "error"}
	f.setSessions("box", ended)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.restoreTaskReceiptLooks(ctx)
		st.faultMu.Lock()
		st.failRead = true
		st.faultMu.Unlock()
		s.taskTurnEnded(ctx, "box", "s1")
		st.faultMu.Lock()
		st.failRead = false
		st.faultMu.Unlock()
		f.setSessions("box")
		<-time.After(time.Minute)
		synctest.Wait()
		st.faultMu.Lock()
		n := len(st.feed)
		st.faultMu.Unlock()
		if n != 0 {
			t.Fatal("missing session produced a receipt")
		}
		f.setSessions("box", ended)
		<-time.After(3 * time.Minute)
		synctest.Wait()
		st.faultMu.Lock()
		n = len(st.feed)
		st.faultMu.Unlock()
		if n != 1 {
			t.Fatalf("receipts after session returned = %d", n)
		}
	})
}

func TestTaskReceiptRetriesSettingReadFailure(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "error"})
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.restoreTaskReceiptLooks(ctx)
		st.faultMu.Lock()
		st.failRead = true
		st.faultMu.Unlock()
		s.taskTurnEnded(ctx, "box", "s1")
		st.faultMu.Lock()
		n := len(st.feed)
		st.failRead = false
		st.faultMu.Unlock()
		if n != 0 {
			t.Fatal("receipt emitted while setting read failed")
		}
		f.mu.Lock()
		f.offline["box"] = true
		f.mu.Unlock()
		<-time.After(time.Minute + 3*time.Second)
		synctest.Wait()
		f.mu.Lock()
		f.offline["box"] = false
		f.mu.Unlock()
		<-time.After(3 * time.Minute)
		synctest.Wait()
		st.faultMu.Lock()
		n = len(st.feed)
		st.faultMu.Unlock()
		if n != 1 {
			t.Fatalf("receipts after recovery = %d", n)
		}
	})
}

func TestReceiptRegistrationRetryAfterContinueCancelled(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.restoreTaskReceiptLooks(ctx)
		reset := time.Now().Add(time.Hour)
		limited := limitTurn("s1", reset, true)
		limited.MachineID = "box"
		f.setSessions("box", limited)
		todo := todoNow(s, "task")
		st.faultMu.Lock()
		st.failWrite = true
		st.faultMu.Unlock()
		s.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", reset.Add(limitResumeGrace))
		st.faultMu.Lock()
		st.failWrite = false
		st.faultMu.Unlock()
		limited.TurnErrorResumes = false
		f.setSessions("box", limited)
		<-time.After(time.Minute)
		synctest.Wait()
		st.faultMu.Lock()
		rows := append([]db.FeedRow(nil), st.feed...)
		st.faultMu.Unlock()
		if len(rows) != 1 || !strings.Contains(string(rows[0].Body), `"state":"interrupted"`) {
			t.Fatalf("receipt after cancellation = %+v", rows)
		}
	})
}

func TestReceiptRegistrationRetryAfterLimitDeadline(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.restoreTaskReceiptLooks(ctx)
		reset := time.Now().Add(-limitResumeGrace + 30*time.Second)
		limited := limitTurn("s1", reset, true)
		limited.MachineID = "box"
		f.setSessions("box", limited)
		todo := todoNow(s, "task")
		st.faultMu.Lock()
		st.failWrite = true
		st.faultMu.Unlock()
		s.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", reset.Add(limitResumeGrace))
		st.faultMu.Lock()
		st.failWrite = false
		st.faultMu.Unlock()
		<-time.After(time.Minute)
		synctest.Wait()
		st.faultMu.Lock()
		n := len(st.feed)
		st.faultMu.Unlock()
		if n != 1 {
			t.Fatalf("expired limit receipt count = %d", n)
		}
	})
}

func TestReceiptRegistrationRetryKeepsSameLimit(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.restoreTaskReceiptLooks(ctx)
		limited := limitTurn("s1", time.Now().Add(time.Hour), true)
		limited.MachineID = "box"
		f.setSessions("box", limited)
		todo := todoNow(s, "task")
		st.faultMu.Lock()
		st.failWrite = true
		st.faultMu.Unlock()
		s.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", time.Now().Add(time.Hour))
		st.faultMu.Lock()
		st.failWrite = false
		st.faultMu.Unlock()
		<-time.After(time.Minute)
		synctest.Wait()
		var state receiptLookState
		if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
			t.Fatalf("same limited turn not retried: %+v %v", state, err)
		}
	})
}

func TestReceiptRegistrationRetryRejectsNewWork(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.restoreTaskReceiptLooks(ctx)
		todo := todoNow(s, "task")
		st.faultMu.Lock()
		st.failWrite = true
		st.faultMu.Unlock()
		s.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", time.Now().Add(time.Hour))
		st.faultMu.Lock()
		st.failWrite = false
		st.faultMu.Unlock()
		<-time.After(time.Second)
		s.taskWorking(ctx, "box", "s1")
		f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "error"})
		s.taskTurnEnded(ctx, "box", "s1")
		<-time.After(time.Minute)
		synctest.Wait()
		var state receiptLookState
		if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 0 {
			t.Fatalf("old retry restored a wait: %+v %v", state, err)
		}
		st.faultMu.Lock()
		n := len(st.feed)
		st.faultMu.Unlock()
		if n != 1 {
			t.Fatalf("new work receipts = %d", n)
		}
	})
}

func TestReceiptLookRunStopAfterFeedCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := &blockedReceiptStore{entered: make(chan struct{}), release: make(chan struct{})}
		linked := time.Now().Add(-time.Hour)
		todo := Todo{ID: "task", Title: "Task", Origin: "conv", Quiet: true, CreatedAt: linked,
			Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", LinkedAt: linked}}}
		node := newFakeHub()
		node.sessions["box"] = []proto.Session{{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "error"}}
		s := New(Options{Hub: node, Store: func() Store { return st }})
		st.setTodo(todo)
		putTodo(s, todo)
		ctx, stop := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); s.Run(ctx) }()
		var releaseOnce sync.Once
		defer func() { stop(); releaseOnce.Do(func() { close(st.release) }); <-done }()
		synctest.Wait()
		s.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Minute))
		select {
		case <-st.entered:
		case <-time.After(2 * time.Minute):
			t.Fatal("feed insert did not reach the acknowledgment barrier")
		}
		stop()
		<-done
		releaseOnce.Do(func() { close(st.release) })
		synctest.Wait()
		var state receiptLookState
		if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
			t.Fatalf("stopped manager removed recoverable look: %+v %v", state, err)
		}
		s.quiet.mu.Lock()
		pending := s.quiet.pending["task"]
		s.quiet.mu.Unlock()
		if pending != nil {
			t.Fatal("stopped manager queued an in-memory notice")
		}
	})
}

func TestReceiptLookRunRestartNewWorkingAfterFeed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		st := &receiptOnceStore{}
		linked := time.Now().Add(-time.Hour)
		todo := Todo{ID: "task", Title: "Task", Origin: "conv", CreatedAt: linked,
			Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", LinkedAt: linked}}}
		oldNode := newFakeHub()
		oldNode.sessions["box"] = []proto.Session{{MachineID: "box", SessionID: "s1", Status: proto.StatusDone}}
		old := New(Options{Hub: oldNode, Store: func() Store { return st }})
		putTodo(old, todo)
		oldCtx, stopOld := context.WithCancel(context.Background())
		oldDone := make(chan struct{})
		go func() { defer close(oldDone); old.Run(oldCtx) }()
		synctest.Wait()
		old.taskTurnEndedAfter(oldCtx, "box", "s1", time.Now().Add(time.Hour))
		ref := SessionRef{MachineID: "box", SessionID: "s1"}
		if _, err := old.appendTaskReceipt(oldCtx, todo, ref, receiptBody{State: "done"}); err != nil {
			t.Fatal(err)
		}
		stopOld()
		<-oldDone

		node := newFakeHub()
		working := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking}
		node.sessions["box"] = []proto.Session{working}
		restarted := New(Options{Hub: node, Store: func() Store { return st }})
		putTodo(restarted, todo)
		ctx, stop := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); restarted.Run(ctx) }()
		defer func() {
			stop()
			<-done
			synctest.Wait()
			if restarted.fleet.q.ch != nil {
				close(restarted.fleet.q.ch)
			}
		}()
		synctest.Wait()
		restarted.setSessions("box", []proto.Session{working})
		barrier := make(chan struct{})
		restarted.inOrder(func() { close(barrier) })
		<-barrier
		restarted.tasks.mu.Lock()
		generation := restarted.tasks.workIDs["task"]
		restarted.tasks.mu.Unlock()
		if generation == "" {
			t.Fatal("working after a committed receipt did not start a new generation")
		}
		ended := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "new turn failed"}
		node.setSessions("box", ended)
		restarted.taskTurnEnded(ctx, "box", "s1")
		st.feedMu.Lock()
		n := len(st.feed)
		st.feedMu.Unlock()
		if n != 2 {
			t.Fatalf("old and new turn receipts = %d", n)
		}
	})
}

func TestReceiptLookReadsLegacyWithoutGeneration(t *testing.T) {
	var state receiptLookState
	if err := json.Unmarshal([]byte(`{"revision":1,"looks":[{"machine":"box","session":"s1","todo":"task","id":"old","at":"2000-01-01T01:00:00Z"}]}`), &state); err != nil {
		t.Fatal(err)
	}
	key := receiptKey{Ref: SessionRef{MachineID: "box", SessionID: "s1"}}
	if look, ok := receiptLookMap(state)[key]; !ok || look.ID != "old" || !look.Work.IsZero() {
		t.Fatalf("legacy wait = %+v, %v", look, ok)
	}
}

func TestReceiptLookInitialSnapshotDoesNotDependOnFeedRead(t *testing.T) {
	ctx := context.Background()
	st := &receiptOnceStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour)
	old := receiptTestService(t, f, st, linked)
	old.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Hour))
	if _, err := old.appendTaskReceipt(ctx, todoNow(old, "task"), SessionRef{MachineID: "box", SessionID: "s1"}, receiptBody{State: "done"}); err != nil {
		t.Fatal(err)
	}
	restarted := receiptTestService(t, f, st, linked)
	st.feedMu.Lock()
	st.failFeedRead = true
	st.feedMu.Unlock()
	restarted.taskWorking(ctx, "box", "s1")
	restarted.tasks.mu.Lock()
	generation := restarted.tasks.workIDs["task"]
	restarted.tasks.mu.Unlock()
	if generation == "" {
		t.Fatal("initial working observation did not get an independent generation")
	}
	st.feedMu.Lock()
	st.failFeedRead = false
	st.feedMu.Unlock()
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "new turn failed"})
	restarted.taskTurnEnded(ctx, "box", "s1")
	st.feedMu.Lock()
	n := len(st.feed)
	st.feedMu.Unlock()
	if n != 2 {
		t.Fatalf("receipts after initial feed read recovered = %d", n)
	}
}

func TestReceiptLookInitialSnapshotKeepsAllCandidateGenerations(t *testing.T) {
	ctx := context.Background()
	st := &receiptOnceStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour)
	a := receiptTestService(t, f, st, linked)
	b := receiptTestService(t, f, st, linked)
	a.taskWorking(ctx, "box", "s1")
	t1 := todoNow(a, "task")
	a.taskTurnEndedForTaskAfter(ctx, &t1, "box", "s1", time.Now().Add(time.Hour))
	if _, err := a.appendTaskReceipt(ctx, t1, SessionRef{MachineID: "box", SessionID: "s1"}, receiptBody{State: "interrupted", Error: "old"}); err != nil {
		t.Fatal(err)
	}
	b.taskWorking(ctx, "box", "s1")
	t2 := todoNow(b, "task")
	b.taskTurnEndedForTaskAfter(ctx, &t2, "box", "s1", time.Now().Add(2*time.Hour))
	var before receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &before); err != nil || len(before.Looks) != 2 {
		t.Fatalf("candidate waits: %+v %v", before, err)
	}
	s := receiptTestService(t, f, st, linked)
	s.taskWorking(ctx, "box", "s1")
	s.tasks.mu.Lock()
	id := s.tasks.workIDs["task"]
	s.tasks.mu.Unlock()
	for _, look := range before.Looks {
		if look.Generation == id {
			t.Fatal("initial observation reused a candidate generation")
		}
	}
	var after receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &after); err != nil || len(after.Looks) != 2 {
		t.Fatalf("candidate wait lost: %+v %v", after, err)
	}
}

func TestReceiptLookWithoutFeedDoesNotAttributeNewTurn(t *testing.T) {
	ctx := context.Background()
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour)
	old := receiptTestService(t, f, st, linked)
	old.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("old wait = %+v %v", state, err)
	}
	oldLook := state.Looks[0]
	s := receiptTestService(t, f, st, linked)
	s.taskWorking(ctx, "box", "s1")
	newTask := todoNow(s, "task")
	s.taskTurnEndedForTaskAfter(ctx, &newTask, "box", "s1", time.Now().Add(2*time.Hour))
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
	f.mu.Lock()
	before := len(f.calls)
	f.mu.Unlock()
	s.taskTurnEndedForLook(ctx, "box", "s1", &oldLook)
	f.mu.Lock()
	reads := len(f.calls) - before
	f.mu.Unlock()
	if reads == 0 {
		t.Fatal("old wait did not recheck the working agent")
	}
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 2 {
		t.Fatalf("working agent lost a wait: %+v %v", state, err)
	}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "new error"})
	oldID := oldLook.ID
	s.taskTurnEndedForLook(ctx, "box", "s1", &oldLook)
	st.faultMu.Lock()
	rows := append([]db.FeedRow(nil), st.feed...)
	st.faultMu.Unlock()
	if len(rows) != 1 || rows[0].ID == oldID || !strings.Contains(string(rows[0].Body), "new error") {
		t.Fatalf("fresh recheck was attributed to old receipt: %+v", rows)
	}
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].ID == oldID {
		t.Fatalf("old cleanup lost the new wait: %+v %v", state, err)
	}
}

func TestDifferentAgentsEachRecheckTheirOwnWait(t *testing.T) {
	ctx := context.Background()
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	s := receiptTestService(t, f, st, linked)
	s.todos.mu.Lock()
	s.todos.todos["task"].Agents = append(s.todos.todos["task"].Agents, TodoAgent{MachineID: "box", SessionID: "s2", LinkedAt: linked})
	s.todos.activeBySession[[2]string{"box", "s2"}] = "task"
	s.todos.mu.Unlock()
	s.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Hour))
	s.taskTurnEndedAfter(ctx, "box", "s2", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 2 {
		t.Fatalf("waits = %+v, %v", state, err)
	}
	var a, b receiptLook
	for _, look := range state.Looks {
		if look.Session == "s1" {
			a = look
		} else if look.Session == "s2" {
			b = look
		}
	}
	if a.ID == "" || b.ID == "" || a.ID == b.ID {
		t.Fatalf("agent waits do not have distinct IDs: %+v %+v", a, b)
	}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "first"},
		proto.Session{MachineID: "box", SessionID: "s2", Status: proto.StatusDone, State: proto.StateUnread})
	s.taskTurnEndedForLook(ctx, "box", "s1", &a)
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].ID != b.ID {
		t.Fatalf("agent B wait changed: %+v, %v", state, err)
	}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread},
		proto.Session{MachineID: "box", SessionID: "s2", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "second"})
	f.mu.Lock()
	before := len(f.calls)
	f.mu.Unlock()
	s.taskTurnEndedForLook(ctx, "box", "s2", &b)
	f.mu.Lock()
	calls := len(f.calls) - before
	f.mu.Unlock()
	if calls == 0 {
		t.Fatal("agent B did not recheck the node")
	}
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 0 {
		t.Fatalf("agent B did not finish its wait: %+v, %v", state, err)
	}
	st.faultMu.Lock()
	rows := append([]db.FeedRow(nil), st.feed...)
	st.faultMu.Unlock()
	if len(rows) != 2 || rows[0].ID == rows[1].ID || !strings.Contains(string(rows[1].Body), "second") {
		t.Fatalf("agent B did not record its own recheck: %+v", rows)
	}
}

func TestOtherAgentWaitRemainsAfterTriggerReceipt(t *testing.T) {
	ctx := context.Background()
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	s := receiptTestService(t, f, st, linked)
	s.todos.mu.Lock()
	s.todos.todos["task"].Agents = append(s.todos.todos["task"].Agents, TodoAgent{MachineID: "box", SessionID: "s2", LinkedAt: linked})
	s.todos.activeBySession[[2]string{"box", "s2"}] = "task"
	s.todos.mu.Unlock()
	s.taskTurnEndedAfter(ctx, "box", "s2", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("wait = %+v, %v", state, err)
	}
	look := state.Looks[0]
	id, err := s.appendTaskReceiptForLook(ctx, todoNow(s, "task"), SessionRef{MachineID: "box", SessionID: "s1"}, receiptBody{State: "interrupted"}, time.Time{}, false, nil)
	if err != nil || id == "" || id == look.ID {
		t.Fatalf("trigger receipt = %q, %v", id, err)
	}
	s.finishTaskReceipt(ctx, "task", id)
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].ID != look.ID {
		t.Fatalf("other agent wait changed = %+v, %v", state, err)
	}
}

func TestBoundRetryDoesNotMutateOtherManagerWork(t *testing.T) {
	ctx := context.Background()
	st := &receiptOnceStore{}
	f := newFakeHub()
	f.judge = `{"verdict":"done","reason":"Verified"}`
	linked := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	a := receiptTestService(t, f, st, linked)
	b := receiptTestService(t, f, st, linked)
	a.taskWorking(ctx, "box", "s1")
	todo := todoNow(a, "task")
	a.tasks.mu.Lock()
	work, generation := a.tasks.working[todo.ID], a.tasks.workIDs[todo.ID]
	a.tasks.mu.Unlock()
	expected := &receiptLook{Todo: todo.ID, Machine: "box", Session: "s1", Linked: linked, Work: work, Generation: generation, RetryID: "bound-retry"}
	b.taskWorking(ctx, "box", "s1")
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "task"}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "deployed new work"})
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, LastText: "deployed new work"})
	b.taskTurnEnded(ctx, "box", "s1")
	f.mu.Lock()
	before := f.judged
	f.mu.Unlock()
	if before == 0 {
		t.Fatal("manager B did not complete the new work")
	}
	a.taskTurnEndedForLook(ctx, "box", "s1", expected)
	f.mu.Lock()
	judged := f.judged
	f.mu.Unlock()
	current := todoNow(a, "task")
	if judged != before || current.DoneAt != nil || current.Stage == "deploy" {
		t.Fatalf("bound retry mutated new work: judgments=%d before=%d todo=%+v", judged, before, current)
	}
	a.reports.mu.Lock()
	report := a.reports.pending["task"]
	a.reports.mu.Unlock()
	if report != nil {
		t.Fatalf("bound retry replaced the current report: %+v", report)
	}
	st.feedMu.Lock()
	_, exists := st.feed[expected.RetryID]
	st.feedMu.Unlock()
	if !exists {
		t.Fatal("bound retry did not write its receipt")
	}
}

func TestFirstDurableLookDoesNotMutateOtherManagerWork(t *testing.T) {
	ctx := context.Background()
	st := &receiptOnceStore{}
	f := newFakeHub()
	f.judge = `{"verdict":"done","reason":"Verified"}`
	linked := time.Now().Add(-time.Hour)
	a := receiptTestService(t, f, st, linked)
	b := receiptTestService(t, f, st, linked)
	a.taskWorking(ctx, "box", "s1")
	todo := todoNow(a, "task")
	a.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("wait = %+v, %v", state, err)
	}
	look := state.Looks[0]
	b.taskWorking(ctx, "box", "s1")
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "task"}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "deployed new work"})
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, LastText: "deployed new work"})
	a.taskTurnEndedForLook(ctx, "box", "s1", &look)
	f.mu.Lock()
	judged := f.judged
	f.mu.Unlock()
	current := todoNow(a, "task")
	if judged != 0 || current.DoneAt != nil || current.Stage == "deploy" {
		t.Fatalf("old check mutated new work: judgments=%d todo=%+v", judged, current)
	}
	a.reports.mu.Lock()
	report := a.reports.pending["task"]
	a.reports.mu.Unlock()
	if report != nil {
		t.Fatalf("old check replaced the current report: %+v", report)
	}
	st.feedMu.Lock()
	_, original := st.feed[look.ID]
	_, recheck := st.feed[receiptRecheckID(look.ID)]
	st.feedMu.Unlock()
	if original || !recheck {
		t.Fatalf("receipt IDs: original=%v recheck=%v", original, recheck)
	}
}

func TestRecoveredReceiptDoesNotJudgeAfterOtherManagerWork(t *testing.T) {
	ctx := context.Background()
	st := &receiptOnceStore{}
	f := newFakeHub()
	f.judge = `{"verdict":"done","reason":"Verified"}`
	linked := time.Now().Add(-time.Hour)
	a := receiptTestService(t, f, st, linked)
	b := receiptTestService(t, f, st, linked)
	a.taskWorking(ctx, "box", "s1")
	todo := todoNow(a, "task")
	a.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("wait = %+v, %v", state, err)
	}
	look := state.Looks[0]
	if _, err := a.appendTaskReceipt(ctx, todo, SessionRef{MachineID: "box", SessionID: "s1"}, receiptBody{State: "done", Summary: "old result"}); err != nil {
		t.Fatal(err)
	}
	// Only B sees the next turn; A's local work ID still matches the old receipt.
	b.taskWorking(ctx, "box", "s1")
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "task"}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "new result"})
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, LastText: "new result"})
	a.taskTurnEndedForLook(ctx, "box", "s1", &look)
	f.mu.Lock()
	judged := f.judged
	f.mu.Unlock()
	if judged != 0 || todoNow(a, "task").DoneAt != nil {
		t.Fatalf("old receipt judged new work: judgments=%d todo=%+v", judged, todoNow(a, "task"))
	}
}

func TestReceiptRecheckFeedNeverFinishesOldWork(t *testing.T) {
	ctx := context.Background()
	st := &receiptOnceStore{}
	f := newFakeHub()
	f.judge = `{"verdict":"done","reason":"Verified"}`
	linked := time.Now().Add(-time.Hour)
	a := receiptTestService(t, f, st, linked)
	a.taskWorking(ctx, "box", "s1")
	todo := todoNow(a, "task")
	a.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("wait = %+v, %v", state, err)
	}
	look := state.Looks[0]
	a.taskWorking(ctx, "box", "s1")
	a.tasks.mu.Lock()
	current := a.tasks.working["task"]
	a.tasks.mu.Unlock()
	if _, err := a.appendTaskReceiptForLook(ctx, todo, SessionRef{MachineID: "box", SessionID: "s1"}, receiptBody{State: "done", Summary: "rechecked result"}, current, true, &look); err != nil {
		t.Fatal(err)
	}
	st.feedMu.Lock()
	_, inserted := st.feed[receiptRecheckID(look.ID)]
	st.feedMu.Unlock()
	if !inserted {
		t.Fatal("manager A did not write a recheck receipt")
	}
	s := receiptTestService(t, f, st, linked)
	s.tasks.mu.Lock()
	s.tasks.working["task"] = look.Work
	s.tasks.workIDs["task"] = look.Generation
	s.tasks.mu.Unlock()
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread})
	s.taskTurnEndedForLook(ctx, "box", "s1", &look)
	f.mu.Lock()
	judged := f.judged
	f.mu.Unlock()
	if judged != 0 {
		t.Fatal("a recheck receipt triggered old-work judgment")
	}
	s.reports.mu.Lock()
	report := s.reports.pending["task"]
	s.reports.mu.Unlock()
	if report != nil {
		t.Fatal("a recheck receipt changed the latest task report")
	}
}

func TestReceiptLookRecoveredDoneDoesNotAssumeSameGeneration(t *testing.T) {
	for _, mode := range []string{"same", "new", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st := &receiptOnceStore{}
			f := newFakeHub()
			f.judge = `{"verdict":"done","reason":"Verified"}`
			linked := time.Now().Add(-time.Hour)
			old := receiptTestService(t, f, st, linked)
			old.taskWorking(ctx, "box", "s1")
			todo := todoNow(old, "task")
			old.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", time.Now().Add(time.Hour))
			var state receiptLookState
			if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
				t.Fatalf("wait: %+v %v", state, err)
			}
			look := state.Looks[0]
			if _, err := old.appendTaskReceipt(ctx, todo, SessionRef{MachineID: "box", SessionID: "s1"}, receiptBody{State: "done", Summary: "old result"}); err != nil {
				t.Fatal(err)
			}
			s := receiptTestService(t, f, st, linked)
			if mode == "same" {
				s.tasks.mu.Lock()
				s.tasks.working["task"] = look.Work
				s.tasks.workIDs["task"] = look.Generation
				s.tasks.mu.Unlock()
			} else if mode == "new" {
				s.taskWorking(ctx, "box", "s1")
			}
			f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "task"}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: "new result"})
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, LastText: "new result"})
			s.taskTurnEndedForLook(ctx, "box", "s1", &look)
			f.mu.Lock()
			judged := f.judged
			f.mu.Unlock()
			if judged != 0 {
				t.Fatalf("judgment count = %d for %s", judged, mode)
			}
			s.reports.mu.Lock()
			report := s.reports.pending["task"]
			s.reports.mu.Unlock()
			if report != nil {
				t.Fatalf("recovered report for %s = %+v", mode, report)
			}
			if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 0 {
				t.Fatalf("recovered wait = %+v %v", state, err)
			}
		})
	}
}

func TestRecoveredOldReceiptDoesNotReplaceNewPROrReport(t *testing.T) {
	ctx := context.Background()
	st := &receiptOnceStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour)
	old := receiptTestService(t, f, st, linked)
	old.taskWorking(ctx, "box", "s1")
	todo := todoNow(old, "task")
	old.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("wait = %+v, %v", state, err)
	}
	body := receiptBody{State: "done", Summary: strings.Repeat("x", 560) + " deployed", PR: "https://github.com/o/r/pull/1"}
	if _, err := old.appendTaskReceipt(ctx, todo, SessionRef{MachineID: "box", SessionID: "s1"}, body); err != nil {
		t.Fatal(err)
	}
	s := receiptTestService(t, f, st, linked)
	s.taskWorking(ctx, "box", "s1")
	s.todos.mu.Lock()
	s.todos.todos["task"].Quiet = true
	s.todos.mu.Unlock()
	s.setTaskStage(ctx, "task", "review", "https://github.com/o/r/pull/2")
	current := todoNow(s, "task")
	s.taskReport(current, reportDone, "box", [2]string{"box", "s1"}, "new result", nil)
	s.taskTurnEndedForLook(ctx, "box", "s1", &state.Looks[0])
	current = todoNow(s, "task")
	if current.Stage != "review" || current.PRURL != "https://github.com/o/r/pull/2" {
		t.Fatalf("new task changed: %+v", current)
	}
	s.reports.mu.Lock()
	report := s.reports.pending["task"]
	s.reports.mu.Unlock()
	if report == nil || report.text != "new result" {
		t.Fatalf("new report changed: %+v", report)
	}
	s.quiet.mu.Lock()
	notice := s.quiet.pending["task"]
	s.quiet.mu.Unlock()
	if notice == nil || notice.text != body.Summary {
		t.Fatalf("old user notice = %+v", notice)
	}
}

func TestRecoveredReceiptDoesNotInferStageFromTruncatedSummary(t *testing.T) {
	ctx := context.Background()
	st := &receiptOnceStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Now().Add(-time.Hour))
	s.taskWorking(ctx, "box", "s1")
	s.setTaskStage(ctx, "task", "review", "")
	todo := todoNow(s, "task")
	s.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("wait = %+v, %v", state, err)
	}
	body := receiptBody{State: "done", Summary: strings.Repeat("x", 560) + " deployed", PR: "https://github.com/o/r/pull/1"}
	if _, err := s.appendTaskReceipt(ctx, todo, SessionRef{MachineID: "box", SessionID: "s1"}, body); err != nil {
		t.Fatal(err)
	}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread})
	s.taskTurnEndedForLook(ctx, "box", "s1", &state.Looks[0])
	current := todoNow(s, "task")
	if current.Stage != "review" || current.PRURL != "" || current.DoneAt != nil {
		t.Fatalf("summary changed task state: %+v", current)
	}
	f.mu.Lock()
	judged := f.judged
	f.mu.Unlock()
	s.accept.mu.Lock()
	asks := len(s.accept.asks)
	s.accept.mu.Unlock()
	if judged != 0 || asks != 0 {
		t.Fatalf("recovered deployment triggered judgment=%d acceptance=%d", judged, asks)
	}
}

func TestReceiptLookRecoversOldFeedDuringNewWork(t *testing.T) {
	ctx := context.Background()
	st := &receiptOnceStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour)
	old := receiptTestService(t, f, st, linked)
	old.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("old wait: %+v %v", state, err)
	}
	oldLook := state.Looks[0]
	body := receiptBody{Title: "Task", State: "interrupted", Machine: "box", Error: "old turn error"}
	if _, err := old.appendTaskReceipt(ctx, todoNow(old, "task"), SessionRef{MachineID: "box", SessionID: "s1"}, body); err != nil {
		t.Fatal(err)
	}
	s := receiptTestService(t, f, st, linked)
	s.taskWorking(ctx, "box", "s1")
	s.todos.mu.Lock()
	s.todos.todos["task"].Quiet = true
	s.todos.mu.Unlock()
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, LastText: "new turn text"})
	current := todoNow(s, "task")
	s.taskReport(current, reportDone, "box", [2]string{"box", "s1"}, "new turn result", nil)
	s.taskTurnEndedForTaskAfter(ctx, &current, "box", "s1", time.Now().Add(2*time.Hour))
	s.taskTurnEndedForLook(ctx, "box", "s1", &oldLook)
	s.reports.mu.Lock()
	report := s.reports.pending["task"]
	s.reports.mu.Unlock()
	if report == nil || report.state != reportDone || report.text != "new turn result" {
		t.Fatalf("new report was overwritten = %+v", report)
	}
	s.quiet.mu.Lock()
	notice := s.quiet.pending["task"]
	s.quiet.mu.Unlock()
	if notice == nil || notice.text != "old turn error" {
		t.Fatalf("old notice = %+v", notice)
	}
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].ID == oldLook.ID {
		t.Fatalf("old follow-up did not retain new wait: %+v %v", state, err)
	}
}

func TestReceiptLookLateManagerKeepsBothGenerations(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour)
	a := receiptTestService(t, f, st, linked)
	b := receiptTestService(t, f, st, linked)
	a.taskWorking(ctx, "box", "s1")
	// A's clock is ahead: ordering by Work would discard B's newer generation.
	a.tasks.mu.Lock()
	a.tasks.working["task"] = time.Now().Add(time.Hour)
	a.tasks.mu.Unlock()
	oldTask := todoNow(a, "task")
	b.taskWorking(ctx, "box", "s1")
	newTask := todoNow(b, "task")
	b.taskTurnEndedForTaskAfter(ctx, &newTask, "box", "s1", time.Now().Add(time.Hour))
	a.taskTurnEndedForTaskAfter(ctx, &oldTask, "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 2 || state.Looks[0].Generation == state.Looks[1].Generation {
		t.Fatalf("parallel generations = %+v, %v", state, err)
	}
	a.tasks.mu.Lock()
	oldID := a.tasks.workIDs["task"]
	a.tasks.mu.Unlock()
	var receiptID string
	for _, look := range state.Looks {
		if look.Generation == oldID {
			receiptID = look.ID
		}
	}
	if receiptID == "" {
		t.Fatal("old generation missing")
	}
	a.finishTaskReceipt(ctx, "task", receiptID)
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].Generation == oldID {
		t.Fatalf("old cleanup removed newer wait: %+v, %v", state, err)
	}
}

func TestReceiptLookNewWorkKeepsNewWait(t *testing.T) {
	st := &memStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC))
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.restoreTaskReceiptLooks(ctx)
		s.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Hour))
		var old receiptLookState
		if _, err := st.Setting(ctx, receiptLooksSetting, &old); err != nil || len(old.Looks) != 1 {
			t.Fatalf("old wait: %+v %v", old, err)
		}
		<-time.After(time.Second)
		s.taskWorking(ctx, "box", "s1")
		todo := todoNow(s, "task")
		s.taskTurnEndedForTaskAfter(ctx, &todo, "box", "s1", time.Now().Add(2*time.Hour))
		var next receiptLookState
		if _, err := st.Setting(ctx, receiptLooksSetting, &next); err != nil || len(next.Looks) != 2 {
			t.Fatalf("parallel generations: %+v %v", next, err)
		}
		s.finishTaskReceipt(ctx, "task", old.Looks[0].ID)
		if _, err := st.Setting(ctx, receiptLooksSetting, &next); err != nil || len(next.Looks) != 1 || next.Looks[0].ID == old.Looks[0].ID {
			t.Fatalf("old completion cleared new wait: %+v %v", next, err)
		}
	})
}

func TestReceiptLookDoesNotFollowReboundSession(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Now().Add(-time.Hour))
	ctx := context.Background()
	s.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("wait: %+v %v", state, err)
	}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, State: proto.StateUnread, TurnError: "error"})
	s.hub = &workCardReadHookHub{fakeHub: f, beforeSessionRead: func() {
		s.todos.mu.Lock()
		s.todos.todos["other"] = &Todo{ID: "other", Origin: "other-conv", Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", LinkedAt: time.Now()}}}
		s.todos.activeBySession[[2]string{"box", "s1"}] = "other"
		s.todos.mu.Unlock()
	}}
	s.taskTurnEndedForLook(ctx, "box", "s1", &state.Looks[0])
	st.faultMu.Lock()
	n := len(st.feed)
	st.faultMu.Unlock()
	if n != 0 {
		t.Fatal("old wait wrote a receipt for the new task")
	}
}

func TestReceiptLookFollowsTheLimitedAgent(t *testing.T) {
	st := &memStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Now().Add(-time.Hour))
	s.todos.mu.Lock()
	s.todos.todos["task"].Agents = append(s.todos.todos["task"].Agents, TodoAgent{MachineID: "box", SessionID: "s2", LinkedAt: time.Now()})
	s.todos.activeBySession[[2]string{"box", "s2"}] = "task"
	s.todos.mu.Unlock()
	limited := limitTurn("s2", time.Now().Add(time.Hour), true)
	limited.MachineID = "box"
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone}, limited)
	s.taskTurnEnded(context.Background(), "box", "s1")
	var state receiptLookState
	if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].Session != "s2" {
		t.Fatalf("limit wait associated with wrong agent: %+v %v", state, err)
	}
	s.todos.mu.Lock()
	delete(s.todos.activeBySession, [2]string{"box", "s1"})
	now := time.Now()
	s.todos.todos["task"].Agents[0].ReleasedAt = &now
	s.todos.mu.Unlock()
	s.pruneReceiptLooks(context.Background())
	if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].Session != "s2" {
		t.Fatalf("removing other agent cleared limit wait: %+v %v", state, err)
	}
}

func TestOldReceiptLookRegistersCurrentLimitGeneration(t *testing.T) {
	ctx := context.Background()
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour)
	old := receiptTestService(t, f, st, linked)
	old.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("old wait = %+v, %v", state, err)
	}
	oldLook := state.Looks[0]
	s := receiptTestService(t, f, st, linked)
	s.taskWorking(ctx, "box", "s1")
	s.tasks.mu.Lock()
	current := s.tasks.workIDs["task"]
	s.tasks.mu.Unlock()
	limited := limitTurn("s1", time.Now().Add(2*time.Hour), true)
	limited.MachineID = "box"
	f.setSessions("box", limited)
	s.taskTurnEndedForLook(ctx, "box", "s1", &oldLook)
	if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil || len(state.Looks) != 2 {
		t.Fatalf("waits = %+v, %v", state, err)
	}
	found := false
	for _, look := range state.Looks {
		if look.Generation == current && look.ID != oldLook.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("new limited generation was not registered")
	}
}

func TestReceiptLookReturnsWhenPeerIgnoresContext(t *testing.T) {
	st := &memStore{}
	f := newFakeHub()
	linked := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	s := receiptTestService(t, f, st, linked)
	s.todos.mu.Lock()
	a := s.todos.todos["task"].Agents[0]
	s.todos.todos["task"].Agents = []TodoAgent{
		{MachineID: "other", SessionID: "s2", LinkedAt: linked},
		{MachineID: "third", SessionID: "s3", LinkedAt: linked}, a,
	}
	s.todos.activeBySession[[2]string{"other", "s2"}] = "task"
	s.todos.activeBySession[[2]string{"third", "s3"}] = "task"
	s.todos.mu.Unlock()
	hub := &contextIgnoringSessionsHub{fakeHub: f}
	s.hub = hub
	synctest.Test(t, func(t *testing.T) {
		hub.entered, hub.release, hub.exited = make(chan struct{}), make(chan struct{}), make(chan struct{})
		var release sync.Once
		defer release.Do(func() { close(hub.release) })
		reset := time.Now().Add(time.Hour)
		limited := limitTurn("s3", reset, true)
		limited.MachineID = "third"
		f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone})
		f.setSessions("third", limited)
		done := make(chan struct{})
		go func() { defer close(done); s.taskTurnEnded(context.Background(), "box", "s1") }()
		<-hub.entered
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			release.Do(func() { close(hub.release) })
			<-done
			t.Fatal("a context-ignoring peer blocked the completed results")
		}
		var state receiptLookState
		if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 ||
			state.Looks[0].Session != "s3" || !state.Looks[0].At.Equal(reset.Add(limitResumeGrace)) {
			t.Fatalf("completed agent result not retained: %+v, %v", state, err)
		}
		release.Do(func() { close(hub.release) })
		<-hub.exited
		synctest.Wait() // the late worker can send to the buffered result channel and exit
	})
}

func TestReceiptLookFindsOtherLimitBehindBlockedMachine(t *testing.T) {
	st := &memStore{}
	f := newFakeHub()
	linked := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	s := receiptTestService(t, f, st, linked)
	s.todos.mu.Lock()
	a := s.todos.todos["task"].Agents[0]
	s.todos.todos["task"].Agents = []TodoAgent{
		{MachineID: "other", SessionID: "s2", LinkedAt: linked},
		{MachineID: "third", SessionID: "s3", LinkedAt: linked}, a,
	}
	s.todos.activeBySession[[2]string{"other", "s2"}] = "task"
	s.todos.activeBySession[[2]string{"third", "s3"}] = "task"
	s.todos.mu.Unlock()
	hub := &deadlineBlockedSessionsHub{fakeHub: f, entered: make(chan struct{})}
	s.hub = hub
	synctest.Test(t, func(t *testing.T) {
		reset := time.Now().Add(time.Hour)
		limited := limitTurn("s3", reset, true)
		limited.MachineID = "third"
		f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone})
		f.setSessions("third", limited)
		s.taskTurnEnded(context.Background(), "box", "s1")
		select {
		case <-hub.entered:
		default:
			t.Fatal("other machine did not reach the deadline barrier")
		}
		var state receiptLookState
		if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 ||
			state.Looks[0].Session != "s3" || !state.Looks[0].At.Equal(reset.Add(limitResumeGrace)) {
			t.Fatalf("third agent limit lost behind blocked machine: %+v, %v", state, err)
		}
	})
}

func TestReceiptLookPrioritizesTriggerBeforeBlockedMachine(t *testing.T) {
	st := &memStore{}
	f := newFakeHub()
	linked := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	s := receiptTestService(t, f, st, linked)
	s.todos.mu.Lock()
	a := s.todos.todos["task"].Agents[0]
	s.todos.todos["task"].Agents = []TodoAgent{{MachineID: "other", SessionID: "s2", LinkedAt: linked}, a}
	s.todos.activeBySession[[2]string{"other", "s2"}] = "task"
	s.todos.mu.Unlock()
	hub := &deadlineBlockedSessionsHub{fakeHub: f, entered: make(chan struct{})}
	s.hub = hub
	synctest.Test(t, func(t *testing.T) {
		reset := time.Now().Add(time.Hour)
		limited := limitTurn("s1", reset, true)
		limited.MachineID = "box"
		f.setSessions("box", limited)
		s.taskTurnEnded(context.Background(), "box", "s1")
		select {
		case <-hub.entered:
		default:
			t.Fatal("other machine did not reach the deadline barrier")
		}
		var state receiptLookState
		if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 ||
			state.Looks[0].Session != "s1" || !state.Looks[0].At.Equal(reset.Add(limitResumeGrace)) {
			t.Fatalf("trigger deadline lost behind blocked machine: %+v, %v", state, err)
		}
	})
}

func TestReceiptLookRegistersLimitWhileOtherMachineOffline(t *testing.T) {
	st := &memStore{}
	f := newFakeHub()
	linked := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	s := receiptTestService(t, f, st, linked)
	s.todos.mu.Lock()
	s.todos.todos["task"].Agents = append(s.todos.todos["task"].Agents, TodoAgent{MachineID: "other", SessionID: "s2", LinkedAt: linked})
	s.todos.activeBySession[[2]string{"other", "s2"}] = "task"
	s.todos.mu.Unlock()
	f.sessions["other"] = nil
	f.offline["other"] = true
	synctest.Test(t, func(t *testing.T) {
		reset := time.Now().Add(time.Hour)
		limited := limitTurn("s1", reset, true)
		limited.MachineID = "box"
		f.setSessions("box", limited)
		s.taskTurnEnded(context.Background(), "box", "s1")
		var state receiptLookState
		if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].Session != "s1" || !state.Looks[0].At.Equal(reset.Add(limitResumeGrace)) {
			t.Fatalf("limited agent wait while other machine offline = %+v, %v", state, err)
		}
	})
}

func TestReceiptLookRegistersLimitWhileOtherAgentWorks(t *testing.T) {
	st := &memStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour)
	s := receiptTestService(t, f, st, linked)
	s.todos.mu.Lock()
	s.todos.todos["task"].Agents = append(s.todos.todos["task"].Agents, TodoAgent{MachineID: "box", SessionID: "s2", LinkedAt: linked})
	s.todos.activeBySession[[2]string{"box", "s2"}] = "task"
	s.todos.mu.Unlock()
	reset := time.Now().Add(time.Hour)
	limited := limitTurn("s1", reset, true)
	limited.MachineID = "box"
	f.setSessions("box", limited, proto.Session{MachineID: "box", SessionID: "s2", Status: proto.StatusWorking, State: proto.StateWorking})
	s.taskTurnEnded(context.Background(), "box", "s1")
	var state receiptLookState
	if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].Session != "s1" || !state.Looks[0].At.Equal(reset.Add(limitResumeGrace)) {
		t.Fatalf("limited agent wait while other works = %+v, %v", state, err)
	}
}

func TestReceiptLookKeepsEachLimitedAgent(t *testing.T) {
	st := &memStore{}
	f := newFakeHub()
	s := receiptTestService(t, f, st, time.Now().Add(-time.Hour))
	s.todos.mu.Lock()
	s.todos.todos["task"].Agents = append(s.todos.todos["task"].Agents, TodoAgent{MachineID: "box", SessionID: "s2", LinkedAt: time.Now()})
	s.todos.activeBySession[[2]string{"box", "s2"}] = "task"
	s.todos.mu.Unlock()
	a := limitTurn("s1", time.Now().Add(time.Hour), true)
	b := limitTurn("s2", time.Now().Add(time.Hour), true)
	a.MachineID, b.MachineID = "box", "box"
	f.setSessions("box", a, b)
	s.taskTurnEnded(context.Background(), "box", "s1")
	var state receiptLookState
	if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 2 {
		t.Fatalf("both limits: %+v %v", state, err)
	}
	s.todos.mu.Lock()
	now := time.Now()
	s.todos.todos["task"].Agents[0].ReleasedAt = &now
	delete(s.todos.activeBySession, [2]string{"box", "s1"})
	s.todos.mu.Unlock()
	s.pruneReceiptLooks(context.Background())
	if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].Session != "s2" {
		t.Fatalf("other agent's wait lost: %+v %v", state, err)
	}
}

func TestReceiptLookInitialWorkingSnapshotKeepsIndependentGeneration(t *testing.T) {
	st := &receiptSettingFailureStore{}
	f := newFakeHub()
	linked := time.Now().Add(-time.Hour)
	first := receiptTestService(t, f, st, linked)
	first.taskTurnEndedAfter(context.Background(), "box", "s1", time.Now().Add(time.Hour))
	var state receiptLookState
	if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("wait: %+v %v", state, err)
	}
	s := receiptTestService(t, f, st, linked)
	working := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking}
	flush := func() { done := make(chan struct{}); s.inOrder(func() { close(done) }); <-done }
	s.setSessions("box", []proto.Session{working})
	flush()
	s.tasks.mu.Lock()
	observed := s.tasks.working["task"]
	observedID := s.tasks.workIDs["task"]
	s.tasks.mu.Unlock()
	if observedID == "" || observedID == state.Looks[0].Generation {
		t.Fatal("first snapshot did not create an independent generation")
	}
	if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 {
		t.Fatalf("first snapshot removed the old wait: %+v %v", state, err)
	}
	oldID := state.Looks[0].ID
	current := todoNow(s, "task")
	s.taskTurnEndedForTaskAfter(context.Background(), &current, "box", "s1", time.Now().Add(2*time.Hour))
	s.finishTaskReceipt(context.Background(), "task", oldID)
	if _, err := st.Setting(context.Background(), receiptLooksSetting, &state); err != nil || len(state.Looks) != 1 || state.Looks[0].Generation != observedID {
		t.Fatalf("old follow-up cleared the new wait: %+v %v", state, err)
	}
	s.fleetKeepPrevious("box", []proto.Session{{MachineID: "box", SessionID: "s1", Status: proto.StatusDone}})
	s.setSessions("box", []proto.Session{working})
	flush()
	s.tasks.mu.Lock()
	started := s.tasks.working["task"]
	s.tasks.mu.Unlock()
	if !started.After(observed) {
		t.Fatal("actual new turn did not change generation")
	}
	newTask := receiptTestService(t, f, &memStore{}, linked)
	newTask.setSessions("box", []proto.Session{working})
	done := make(chan struct{})
	newTask.inOrder(func() { close(done) })
	<-done
	newTask.tasks.mu.Lock()
	newWork := newTask.tasks.working["task"]
	newTask.tasks.mu.Unlock()
	if !newWork.After(linked) {
		t.Fatal("first snapshot without a wait did not record work")
	}
}

func TestTaskCompletionRequiresEveryAgentToSettle(t *testing.T) {
	todo := Todo{Agents: []TodoAgent{{MachineID: "computer", SessionID: "agent"}}}
	for _, tc := range []struct {
		name   string
		agents []proto.Session
		want   bool
	}{
		{"missing", nil, false},
		{"tool running with stale UI state", []proto.Session{{Status: proto.StatusWorking, State: proto.StateIdle}}, false},
		{"waiting", []proto.Session{{Status: proto.StatusBlocked}}, false},
		{"unknown", []proto.Session{{Status: proto.StatusUnknown}}, false},
		{"background", []proto.Session{{Status: proto.StatusDone, Background: []proto.BackgroundTask{{ID: "tool"}}}}, false},
		{"done", []proto.Session{{Status: proto.StatusDone}}, true},
		{"idle", []proto.Session{{Status: proto.StatusIdle}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskAgentsSettled(todo, tc.agents); got != tc.want {
				t.Fatalf("settled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTaskCompletionRejectsAResumedTurnDuringJudgment(t *testing.T) {
	for _, supervised := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic close", true: "pending review"}[supervised], func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			if supervised {
				s.superviseTodo(ctx, todo.ID, "conv1", []string{"Verify delivery"}, 0)
				todo = todoNow(s, todo.ID)
			}
			f.judge = `{"verdict":"done","reason":"The previous answer claims success"}`
			s.taskWorking(ctx, "box", "s1")
			s.tasks.mu.Lock()
			work := s.tasks.working[todo.ID]
			s.tasks.mu.Unlock()
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, LastText: "Earlier work finished"})
			changed := false
			f.hookReads(receiptHooks{before: func(int) {
				if changed {
					return
				}
				changed = true
				s.taskWorking(ctx, "box", "s1")
				// Even a short resumed turn that already stopped invalidates the older judgment.
				f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, LastText: "More work remains"})
			}})
			s.autoFinishTask(todo, work, true)
			current := todoNow(s, todo.ID)
			if !changed || current.DoneAt != nil || current.Supervision != nil && current.Supervision.State == reviewPending {
				t.Fatalf("stale judgment applied: changed=%v todo=%+v", changed, current)
			}
			if ok, err := s.finishCurrentTask(ctx, todo.ID, "stale write", work); err != nil || ok {
				t.Fatalf("stale completion committed: %v %v", ok, err)
			}
		})
	}
}

func TestTaskReceiptDoesNotCompleteARunningToolOrMissingAgent(t *testing.T) {
	for _, missing := range []bool{false, true} {
		s, f := newTaskService(t)
		startedTask(t, s, f)
		f.judge = `{"verdict":"done","reason":"Progress looks finished"}`
		s.taskWorking(context.Background(), "box", "s1")
		if missing {
			f.setSessions("box")
		} else {
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateIdle,
				Activity: "python3", LastText: "The initial check passed", UpdatedAt: time.Now()})
		}
		s.taskTurnEnded(context.Background(), "box", "s1")
		if got := feedKinds(s, "conv1"); strings.Contains(got, "receipt") || onlyTodo(s).DoneAt != nil {
			t.Fatalf("receipt/completion while unresolved: missing=%v feed=%s", missing, got)
		}
	}
}

func TestTaskCompletionWaitsForTheAnswerAfterATool(t *testing.T) {
	for _, finalStatus := range []string{proto.StatusDone, proto.StatusIdle} {
		t.Run(finalStatus, func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			f.judge = `{"verdict":"done","reason":"Delivery verified"}`
			ctx := context.Background()
			s.taskWorking(ctx, "box", "s1")
			idle := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusIdle,
				LastText: "The first check passed", UpdatedAt: time.Now()}
			f.setSessions("box", idle)
			s.fleetKeepPrevious("box", []proto.Session{idle})
			f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "The first check passed"},
				proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "Bash", ToolUseID: "command"},
				proto.Item{Kind: proto.KindToolResult, Role: "user", ToolUseID: "command", Text: "still verifying"})
			s.taskTurnEnded(ctx, "box", "s1")
			if strings.Contains(feedKinds(s, "conv1"), "receipt") || onlyTodo(s).DoneAt != nil || f.judged != 0 {
				t.Fatal("a temporary idle/tool result was treated as the completed task")
			}
			// A later session update must finish the held receipt without another working tick.
			f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Delivery verified; all acceptance checks passed"},
				proto.Item{Kind: "turn_duration", Role: "system"})
			final := idle
			final.Status, final.LastText, final.UpdatedAt = finalStatus, "Delivery verified", time.Now()
			f.setSessions("box", final)
			s.setSessions("box", []proto.Session{final})
			waitFor(t, "completion after the final session update", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
			f.mu.Lock()
			judged := f.judged
			f.mu.Unlock()
			if judged != 1 {
				t.Fatalf("completion judgments = %d, want 1", judged)
			}
		})
	}
}

func TestTaskCompletionReconcilesAnAnswerArrivingDuringTheRead(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	f.judge = `{"verdict":"done","reason":"Delivery verified"}`
	ctx := context.Background()
	s.taskWorking(ctx, "box", "s1")
	idle := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusIdle, Rev: 1}
	f.setSessions("box", idle)
	s.fleetKeepPrevious("box", []proto.Session{idle})
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Checking"},
		proto.Item{Kind: proto.KindToolResult, Role: "user", ToolUseID: "command", Text: "Tool output"})
	f.hookReads(receiptHooks{after: func(read int) {
		if read != 1 {
			return
		}
		// The final event overtakes the old tool transcript response.
		f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Delivery verified"},
			proto.Item{Kind: "turn_duration", Role: "system"})
		final := idle
		final.Status, final.Rev = proto.StatusDone, 2
		f.setSessions("box", final)
		s.setSessions("box", []proto.Session{final})
	}})
	s.taskTurnEnded(ctx, "box", "s1")
	waitFor(t, "completion without another session update", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
}

func TestTaskCompletionOwnBackgroundWorkNeedsNoReport(t *testing.T) {
	for _, kind := range []string{"shell", "monitor", "agent", "workflow"} {
		t.Run(kind, func(t *testing.T) {
			s, f := newTaskService(t)
			shortSupervise(t, s)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.superviseTodo(ctx, todo.ID, "conv1", []string{"Login regression test passes"}, 0)
			s.taskWorking(ctx, "box", "s1")
			f.judge = `{"verdict":"done","reason":"The original login task and its tests are complete"}`
			f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: todo.Notes},
				proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "Bash", ToolUseID: "login-tests", ToolInput: json.RawMessage(`{"command":"go test ./login"}`)})
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "codex", Status: proto.StatusDone,
				Background: []proto.BackgroundTask{{ID: "login-work", Kind: kind}}})
			s.taskTurnEnded(ctx, "box", "s1")
			if todoNow(s, todo.ID).Supervision.State == reviewPending || strings.Contains(feedKinds(s, "conv1"), "receipt") {
				t.Fatal("running background work must not complete the task")
			}
			f.setItems(proto.Item{Kind: proto.KindNotice, Role: "system", Wake: true, Task: &proto.TaskNote{Kind: kind, TaskID: "login-work", ToolUseID: "login-tests", Status: "completed", Title: "Verify the login correction"}},
				proto.Item{Kind: proto.KindText, Role: "assistant", Text: "The original login task is complete; its regression test passes and the correction is delivered."})
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "codex", Status: proto.StatusDone})
			s.taskTurnEnded(ctx, "box", "s1")
			if todoNow(s, todo.ID).Supervision.State != reviewPending {
				t.Fatal("own background work completion must not require report_to_supervisor")
			}
		})
	}
}

func TestTaskCompletionLimitContinueNeedsNoReport(t *testing.T) {
	for _, parsed := range []bool{false, true} {
		t.Run(map[bool]string{false: "raw continuation", true: "injected metadata"}[parsed], func(t *testing.T) {
			s, f := newTaskService(t)
			shortSupervise(t, s)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.superviseTodo(ctx, todo.ID, "conv1", []string{"Login regression test passes"}, 0)
			s.taskWorking(ctx, "box", "s1")
			continuation := proto.Item{Kind: proto.KindText, Role: "user", Text: prompts.RenderAgentLimitContinue()}
			if parsed {
				continuation.Injected = prompts.ParseInjected(continuation.Text)
			}
			f.setItems(continuation, proto.Item{Kind: proto.KindText, Role: "assistant", Text: "The login correction is delivered and the regression test passes after resuming the original task."})
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Agent: "codex", Status: proto.StatusDone})
			f.judge = `{"verdict":"done","reason":"The resumed original task is complete"}`
			s.taskTurnEnded(ctx, "box", "s1")
			if todoNow(s, todo.ID).Supervision.State != reviewPending {
				t.Fatal("limit continuation must not require report_to_supervisor")
			}
		})
	}
}

func TestTaskCompletionSideConversationGate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		from     string
		ask      string
		injected *proto.Injected
		queued   bool
		wantDone bool
	}{
		{name: "peer", from: "peer-agent", ask: "Did this session fix the unrelated parser problem?"},
		{name: "queued peer", from: "peer-agent", ask: "Did this session fix the unrelated parser problem?", queued: true},
		{name: "loop inspection", ask: "/loop 5m inspect the other agents"},
		{name: "loop command without arguments", ask: "/loop"},
		{name: "not a loop command", ask: "/loopfix continues the approved login correction", wantDone: true},
		{name: "peer envelope", ask: `<cross-session-message from="peer-agent">Did you fix the parser?</cross-session-message>`},
		{name: "teammate envelope", ask: `<teammate-message teammate_id="peer-agent">Did you fix the parser?</teammate-message>`},
		{name: "source envelope", ask: `<persona-context kind="side-conversation">Source: another conversation.</persona-context>Did you fix the parser?`},
		{name: "injected patrol", ask: "Check another task's progress", injected: &proto.Injected{Kind: proto.InjectedSystem, State: "supervise"}},
		{name: "legacy patrol", ask: "📋 督办：你派出去的任务有 1 个要你处理\n这是 Persona 的督办消息"}, // i18n-ignore: synthetic legacy injected message
		{name: "actual task", ask: "iPad 横屏时登录页崩溃，修好并补测试", wantDone: true},      // i18n-ignore: matches synthetic task fixture
		{name: "Persona continuation", from: "Persona", ask: "Continue the login fix and hand over the verified result", wantDone: true},
		{name: "task supervisor", from: "conv1", ask: "Continue the login fix and hand over the verified result", wantDone: true},
		{name: "user continuation", ask: "Continue with the approved fix", wantDone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			shortSupervise(t, s)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.superviseTodo(ctx, todo.ID, "conv1", []string{"Login regression test passes"}, 0)
			f.judge = `{"verdict":"done","reason":"The latest answer is complete"}`
			s.taskWorking(ctx, "box", "s1")
			answer := "已告知另一会话：本会话未处理该问题" // i18n-ignore: synthetic regression answer
			if tc.wantDone {
				answer = "The login fix is complete; the regression test passes and the result is ready for review."
			}
			f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: tc.ask, From: tc.from, Injected: tc.injected, Queued: tc.queued},
				proto.Item{Kind: proto.KindText, Role: "assistant", Text: answer})
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, LastText: answer, UpdatedAt: time.Now()})
			s.taskTurnEnded(ctx, "box", "s1")
			current := todoNow(s, todo.ID)
			if got := current.Supervision.State == reviewPending; got != tc.wantDone || current.DoneAt != nil {
				t.Fatalf("pending review = %v, want %v: %+v", got, tc.wantDone, current)
			}
			items, _, _ := s.feed(ctx, "conv1", time.Time{})
			var receipt receiptBody
			for _, it := range items {
				if it.Kind == feedReceipt {
					_ = json.Unmarshal(it.Body, &receipt)
				}
			}
			if receipt.Review == nil || receipt.Review.State != "checking" {
				t.Fatalf("receipt must not imply acceptance before judgment: %+v", receipt)
			}
		})
	}
}

func TestTaskCompletionSideTurnNeedsFreshTaskReport(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stale      bool
		noEvidence bool
		failed     bool
		unrelated  bool
		wantReview bool
	}{
		{name: "fresh current-task done report", wantReview: true},
		{name: "old done report", stale: true},
		{name: "done report without evidence", noEvidence: true},
		{name: "failed done report", failed: true},
		{name: "unrelated report evidence", unrelated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			shortSupervise(t, s)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.superviseTodo(ctx, todo.ID, "conv1", []string{"Login regression test passes"}, 0)
			s.taskWorking(ctx, "box", "s1")
			f.judge = `{"verdict":"done","reason":"The current task report is complete"}`
			at := time.Now()
			reportAt := at.Add(time.Second)
			if tc.stale {
				reportAt = todo.CreatedAt.Add(-time.Minute)
			}
			text := todo.Title + ": complete; Login regression test passes"
			evidence := []string{"Login regression test passes: go test ./login: PASS"}
			if tc.noEvidence {
				evidence = nil
			}
			if tc.unrelated {
				text = "Parser question answered"
				evidence = []string{"Another session received the message"}
			}
			args, _ := json.Marshal(map[string]any{"kind": "done", "text": text, "evidence": evidence})
			if !tc.failed {
				if _, err := s.reportToSupervisor(ctx, "box", "s1", args, reportAt); err != nil {
					t.Fatal(err)
				}
				if todoNow(s, todo.ID).Supervision.State == reviewPending {
					t.Fatal("report alone must not set pending review")
				}
			}
			f.setItems(proto.Item{Kind: proto.KindText, Role: "user", From: "peer-agent", Text: "Did you fix a separate parser problem?", TS: at},
				proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "mcp__persona__report_to_supervisor", ToolUseID: "done-report", ToolInput: args, TS: reportAt},
				proto.Item{Kind: proto.KindToolResult, Role: "user", ToolUseID: "done-report", Text: "Report delivered", IsError: tc.failed},
				proto.Item{Kind: proto.KindText, Role: "assistant", Text: "I told the other session that this session did not handle that problem."})
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone})
			todo = todoNow(s, todo.ID)
			s.tasks.mu.Lock()
			work := s.tasks.working[todo.ID]
			s.tasks.mu.Unlock()
			s.autoFinishTask(todo, work, true)
			if got := todoNow(s, todo.ID).Supervision.State == reviewPending; got != tc.wantReview {
				t.Fatalf("pending review = %v, want %v", got, tc.wantReview)
			}
		})
	}
}

func TestTaskCompletionDoneReportFreshness(t *testing.T) {
	at := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"current", "missing tool time", "missing turn boundary", "before side request", "before reopen", "before criteria update", "progress only", "untrusted tool namespace", "report is not a verdict"} {
		t.Run(name, func(t *testing.T) {
			work := at.Add(-time.Second)
			todo := Todo{Title: "Fix login", CreatedAt: at.Add(-time.Hour), Supervision: &Supervision{
				DoneWhen: []string{"Login test passes"}, Progress: &TaskProgress{Kind: upReportDone, Text: "Fix login complete", At: at.Add(time.Second)},
			}}
			items := []proto.Item{
				{Kind: proto.KindText, Role: "user", From: "peer", TS: at},
				{Kind: proto.KindToolUse, Role: "assistant", ToolName: "report_to_supervisor", ToolUseID: "report", TS: at.Add(time.Second),
					ToolInput: json.RawMessage(`{"kind":"done","text":"Fix login complete","evidence":["Login test passes: PASS"]}`)},
				{Kind: proto.KindToolResult, Role: "user", ToolUseID: "report", Text: "Delivered"},
			}
			want := false
			switch name {
			case "current":
				want = true
			case "missing tool time":
				items[1].TS = time.Time{}
			case "missing turn boundary":
				work = time.Time{}
				items[0].TS = time.Time{}
			case "before side request":
				items[0].TS = at.Add(2 * time.Second)
			case "before reopen":
				reopened := at.Add(2 * time.Second)
				todo.ReopenedAt = &reopened
			case "before criteria update":
				todo.Supervision.UpdatedAt = at.Add(2 * time.Second)
			case "progress only":
				items = items[:1]
			case "untrusted tool namespace":
				items[1].ToolName = "mcp__extension__report_to_supervisor"
			case "report is not a verdict":
				todo.Supervision.Progress.Kind = upReportProgress
			}
			if _, _, got := freshTaskDoneReport(todo, items, work); got != want {
				t.Fatalf("eligible=%v, want %v", got, want)
			}
		})
	}
}

func TestTaskCompletionRejectsChangedAcceptanceCriteria(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	ctx := context.Background()
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"Login test passes"}, 0)
	s.taskWorking(ctx, "box", "s1")
	todo = todoNow(s, todo.ID)
	f.judge = `{"verdict":"done","reason":"Previous acceptance criteria met"}`
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Login test passes"})
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone})
	f.hookReads(receiptHooks{after: func(int) {
		_, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
			v.DoneWhen = []string{"Login test passes", "Deployment verified"}
			return true
		})
		if err != nil {
			t.Fatal(err)
		}
	}})
	s.tasks.mu.Lock()
	work := s.tasks.working[todo.ID]
	s.tasks.mu.Unlock()
	s.autoFinishTask(todo, work, true)
	if todoNow(s, todo.ID).Supervision.State == reviewPending {
		t.Fatal("old criteria yielded pending review")
	}
}
