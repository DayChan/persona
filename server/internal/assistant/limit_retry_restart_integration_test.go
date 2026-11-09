package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Managers in this test never overlap. The real schedule loop still requires
// its ownership capability and both versioned storage interfaces.
type restartScheduleStore struct{ *limitTestStore }

func (*restartScheduleStore) TryScheduleOwner(context.Context) (<-chan struct{}, func(), bool, error) {
	done := make(chan struct{})
	return done, func() { close(done) }, true, nil
}

func TestClaimedLimitRetryManagerRestartIntegration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		store := &restartScheduleStore{&limitTestStore{scheduleWriteStore: &scheduleWriteStore{Store: &memStore{}}}}
		node := newFakeHub().withMachine()
		now := time.Now().UTC().Truncate(time.Second)
		started := now.Add(-time.Hour)
		task := Task{ID: "limit", Title: "Limit", Prompt: "Example", Repeat: repeatOnce, At: started, LastRun: started,
			MachineID: "server", SessionID: "session"}
		failed := ScheduleRun{ID: "failed", RefID: task.ID, At: started, Status: runFailed,
			MachineID: "server", SessionID: "session", of: started}
		if err := store.PutSetting(ctx, tasksKey, []Task{task}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutSetting(ctx, runsKey, []ScheduleRun{failed}); err != nil {
			t.Fatal(err)
		}
		node.known["session"] = true
		node.setSessions("server", proto.Session{SessionID: "session", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now})
		old := New(Options{Hub: node, Store: func() Store { return store }})
		oldCtx, stopOld := context.WithCancel(ctx)
		oldDone := make(chan struct{})
		go func() { defer close(oldDone); old.Run(oldCtx) }()
		synctest.Wait()
		if !old.armLimitRetry(oldCtx, failed, now, now.Add(time.Minute)) {
			t.Fatal("could not save retry")
		}
		current, ok := taskByID(old, task.ID)
		if !ok || current.LimitRetry == nil {
			t.Fatal("retry was not persisted")
		}
		wait := *current.LimitRetry
		ended := proto.Session{SessionID: "session", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
		if _, dispatch := old.consumeLimitRetry(oldCtx, task.ID, wait, ended, now.Add(time.Minute)); !dispatch {
			t.Fatal("retry could not be claimed")
		}
		// Simulate a process death after the durable claim and before the send.
		stopOld()
		<-oldDone
		restarted := New(Options{Hub: node, Store: func() Store { return store }})
		restartCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); restarted.Run(restartCtx) }()
		synctest.Wait()
		if got := scheduleSendCount(node); got != 0 {
			t.Errorf("sent before retry deadline: %d", got)
		}
		<-time.After(2 * time.Minute)
		synctest.Wait()
		stop()
		<-done
		if got := scheduleSendCount(node); got != 0 {
			t.Errorf("claimed retry was sent after restart: %d", got)
		}
		raw, _, found, err := store.ReadScheduleRuns(ctx)
		if err != nil || !found {
			t.Fatalf("read persisted run history: found %v, error %v", found, err)
		}
		var runs []ScheduleRun
		if err := json.Unmarshal(raw, &runs); err != nil {
			t.Fatal(err)
		}
		var unknown string
		for _, run := range runs {
			if run.ID == "retry_unknown_failed" && run.Status == runFailed {
				unknown = run.Error
			}
		}
		if unknown == "" {
			t.Fatalf("missing persisted unknown result: %+v", runs)
		}
		node.mu.Lock()
		pushes := append([]Notification(nil), node.pushes...)
		node.mu.Unlock()
		if len(pushes) != 1 || !strings.Contains(pushes[0].Body, unknown) {
			t.Fatalf("unknown result notification = %+v, want %q", pushes, unknown)
		}
		var stored []Task
		if _, err := store.Setting(ctx, tasksKey, &stored); err != nil || len(stored) != 1 || stored[0].LimitRetry != nil {
			t.Fatalf("claimed retry remains after recovery: %+v, error %v", stored, err)
		}
	})
}

func TestPendingLimitRetryManagerRestartIntegration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		store := &restartScheduleStore{&limitTestStore{scheduleWriteStore: &scheduleWriteStore{Store: &memStore{}}}}
		node := newFakeHub().withMachine()
		now := time.Now().UTC().Truncate(time.Second)
		started := now.Add(-time.Hour)
		task := Task{ID: "limit", Title: "Limit", Prompt: "Example", Repeat: repeatOnce, At: started, LastRun: started,
			MachineID: "server", SessionID: "session"}
		failed := ScheduleRun{ID: "failed", RefID: task.ID, At: started, Status: runFailed,
			MachineID: "server", SessionID: "session", of: started}
		if err := store.PutSetting(ctx, tasksKey, []Task{task}); err != nil {
			t.Fatal(err)
		}
		if err := store.PutSetting(ctx, runsKey, []ScheduleRun{failed}); err != nil {
			t.Fatal(err)
		}
		node.known["session"] = true
		node.setSessions("server", proto.Session{SessionID: "session", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now})
		old := New(Options{Hub: node, Store: func() Store { return store }})
		oldCtx, stopOld := context.WithCancel(ctx)
		oldDone := make(chan struct{})
		go func() { defer close(oldDone); old.Run(oldCtx) }()
		synctest.Wait()
		if !old.armLimitRetry(oldCtx, failed, now, now.Add(time.Minute)) {
			t.Fatal("could not save retry")
		}
		stopOld()
		<-oldDone
		restarted := New(Options{Hub: node, Store: func() Store { return store }})
		restartCtx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); restarted.Run(restartCtx) }()
		<-time.After(30 * time.Second)
		synctest.Wait()
		if got := scheduleSendCount(node); got != 0 {
			t.Errorf("retry sent before deadline: %d", got)
		}
		<-time.After(2 * time.Minute)
		synctest.Wait()
		// Let the dispatched run's watcher finish before the bubble closes.
		node.setSessions("server", proto.Session{SessionID: "session", Status: proto.StatusDone, State: proto.StateNeedsYou, UpdatedAt: now})
		<-time.After(5 * time.Second)
		synctest.Wait()
		stop()
		<-done
		if got := scheduleSendCount(node); got != 1 {
			t.Fatalf("retry sent %d times after deadline, want one", got)
		}
		var stored []Task
		if _, err := store.Setting(ctx, tasksKey, &stored); err != nil || len(stored) != 1 || stored[0].LimitRetry != nil {
			t.Fatalf("retry remains after dispatch: %+v, error %v", stored, err)
		}
		again := New(Options{Hub: node, Store: func() Store { return store }})
		againCtx, stopAgain := context.WithCancel(ctx)
		againDone := make(chan struct{})
		go func() { defer close(againDone); again.Run(againCtx) }()
		<-time.After(2 * time.Minute)
		synctest.Wait()
		stopAgain()
		<-againDone
		if got := scheduleSendCount(node); got != 1 {
			t.Errorf("retry sent %d times after another restart, want one", got)
		}
	})
}
