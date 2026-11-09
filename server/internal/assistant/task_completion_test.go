package assistant

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

type receiptSettingFailureStore struct {
	memStore
	faultMu   sync.Mutex
	failRead  bool
	failWrite bool
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

func (st *receiptSettingFailureStore) CompareSetting(ctx context.Context, key string, revision int64, value any) (bool, error) {
	st.faultMu.Lock()
	fail := key == receiptLooksSetting && st.failWrite
	st.faultMu.Unlock()
	if fail {
		return false, errors.New("setting unavailable")
	}
	return st.memStore.CompareSetting(ctx, key, revision, value)
}

type receiptOnceStore struct {
	memStore
	feedMu       sync.Mutex
	feed         map[string]db.FeedRow
	loseFirstAck bool
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
		second.reports.mu.Lock()
		report := second.reports.pending["task"]
		second.reports.mu.Unlock()
		if n != 1 || report == nil || report.state != reportInterrupted {
			t.Fatalf("feed count %d, follow-up report %+v", n, report)
		}
		var state receiptLookState
		if _, err := st.Setting(restarted, receiptLooksSetting, &state); err != nil || len(state.Looks) != 0 {
			t.Fatalf("followed-up look not cleared: %+v %v", state, err)
		}
	})
}

func TestReceiptLookRestoresJudgmentAfterFeedWrite(t *testing.T) {
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
	first.taskTurnEndedAfter(ctx, "box", "s1", time.Now().Add(time.Hour))
	ref := SessionRef{MachineID: "box", SessionID: "s1"}
	if _, err := first.appendTaskReceipt(ctx, todoNow(first, "task"), ref, receiptBody{State: "done"}); err != nil {
		t.Fatal(err)
	}
	// Recreate the manager after the feed write and run its recovered check.
	second.taskTurnEnded(ctx, "box", "s1")
	f.mu.Lock()
	judged := f.judged
	f.mu.Unlock()
	if judged == 0 {
		t.Fatal("recovered receipt skipped the completion judgment")
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
	ref1 := SessionRef{MachineID: "box", SessionID: "s1"}
	ref2 := SessionRef{MachineID: "box", SessionID: "s2"}
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
