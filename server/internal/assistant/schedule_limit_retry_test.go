package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

type limitNotifyHub struct {
	*fakeHub
	fail     bool
	onNotify func()
}

func (h *limitNotifyHub) NotifyRequest(ctx context.Context, req NotifyRequest) error {
	if h.onNotify != nil {
		h.onNotify()
	}
	if h.fail {
		return errors.New("notification unavailable")
	}
	return h.fakeHub.NotifyRequest(ctx, req)
}

type limitOwnedStore struct {
	*limitTestStore
	acquired chan struct{}
	released chan struct{}
}

func (st *limitOwnedStore) TryScheduleOwner(ctx context.Context) (<-chan struct{}, func(), bool, error) {
	done := make(chan struct{})
	close(st.acquired)
	return done, func() { close(done); close(st.released) }, true, nil
}

type limitTestStore struct {
	*scheduleWriteStore
	mu                sync.Mutex
	revision          int64
	runsRevision      int64
	beforeTaskCompare func()
	beforeRunsCompare func()
}

func (st *limitTestStore) ReadSchedule(ctx context.Context) (json.RawMessage, int64, bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	var raw json.RawMessage
	ok, err := st.Setting(ctx, tasksKey, &raw)
	return raw, st.revision, ok, err
}

func (st *limitTestStore) CompareSchedule(ctx context.Context, revision int64, raw json.RawMessage) (bool, error) {
	st.mu.Lock()
	if hook := st.beforeTaskCompare; hook != nil {
		st.beforeTaskCompare = nil
		hook()
	}
	defer st.mu.Unlock()
	if revision != st.revision {
		return false, nil
	}
	if err := st.PutSetting(ctx, tasksKey, raw); err != nil {
		return false, err
	}
	st.revision++
	return true, nil
}

func (st *limitTestStore) ReadScheduleRuns(ctx context.Context) (json.RawMessage, int64, bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	var raw json.RawMessage
	ok, err := st.Setting(ctx, runsKey, &raw)
	return raw, st.runsRevision, ok, err
}

func (st *limitTestStore) CompareScheduleRuns(ctx context.Context, revision int64, raw json.RawMessage) (bool, error) {
	st.mu.Lock()
	if hook := st.beforeRunsCompare; hook != nil {
		st.beforeRunsCompare = nil
		st.mu.Unlock()
		hook()
		st.mu.Lock()
	}
	defer st.mu.Unlock()
	if revision != st.runsRevision {
		return false, nil
	}
	if err := st.PutSetting(ctx, runsKey, raw); err != nil {
		return false, err
	}
	st.runsRevision++
	return true, nil
}

func limitRetryFixture(t *testing.T) (*Service, *fakeHub, *limitTestStore, time.Time) {
	t.Helper()
	_, f, base := scheduleStorageFixture(t)
	st := &limitTestStore{scheduleWriteStore: base}
	s := newServiceOn(t, f, st)
	s.loadSchedule(t.Context())
	now := time.Now().UTC().Truncate(time.Second)
	started := now.Add(-48 * time.Hour)
	task := Task{ID: "limit", Title: "Limit", Prompt: "Example", Repeat: repeatOnce, At: started, LastRun: started,
		MachineID: "server", SessionID: "s1"}
	setTasks(s, task)
	if err := st.PutSetting(t.Context(), tasksKey, []Task{task}); err != nil {
		t.Fatal(err)
	}
	f.known["s1"] = true
	f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now})
	run := ScheduleRun{ID: "failed", RefID: "limit", At: started, Status: runFailed, MachineID: "server", SessionID: "s1", of: started}
	s.armLimitRetry(t.Context(), run, now, now.Add(time.Hour))
	return s, f, st, now
}

func TestLimitRetryArmStopsOnShutdownDuringStorageFailure(t *testing.T) {
	s, _, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	st.failWrites(tasksKey)
	attempted := make(chan struct{})
	st.mu.Lock()
	st.beforeTaskCompare = func() { close(attempted) }
	st.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan bool, 1)
	go func() { result <- s.armLimitRetry(ctx, w.Failed, w.Turn, now.Add(time.Hour)) }()
	awaitBusSignal(t, attempted)
	cancel()
	select {
	case saved := <-result:
		if saved {
			t.Fatal("failed write reported a durable wait")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("arm did not stop on shutdown")
	}
}

func TestLimitRetryWaitStoredBeforeFailureNotice(t *testing.T) {
	_, f, st, _ := limitRetryFixture(t)
	s := newServiceOn(t, f, st)
	checked := make(chan bool, 1)
	s.hub = &limitNotifyHub{fakeHub: f, onNotify: func() {
		var tasks []Task
		_, err := st.Setting(context.Background(), tasksKey, &tasks)
		found := err == nil && slices.ContainsFunc(tasks, func(task Task) bool { return task.ID == "p" && task.LimitRetry != nil })
		checked <- found
	}}
	s.sched.limitWait = func(time.Time, time.Time) time.Duration { return time.Hour }
	limitRun(t, f, s, nil)
	if !<-checked {
		t.Fatal("failure notice was sent before the retry wait was stored")
	}
}

func TestLimitRetrySurvivesRestartAndIsConsumedOnce(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	s.runDue(t.Context(), now) // a one-off older than doneKeep still owns its wait
	if task, ok := taskByID(s, "limit"); !ok || task.LimitRetry == nil {
		t.Fatal("cleanup discarded a pending retry")
	}
	restarted := newServiceOn(t, f, st)
	if got := restarted.nextDue(t.Context(), now); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("next due %v", got)
	}
	restarted.runLimitRetries(t.Context(), now.Add(time.Minute))
	if got := scheduleSendCount(f); got != 0 {
		t.Fatalf("sent before due: %d", got)
	}
	restarted.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	restarted.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("past-due retry sent %d times", got)
	}
	again := newServiceOn(t, f, st)
	again.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	again.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("retry sent again after restart: %d", got)
	}
}

func TestLimitRetryCrashAfterClaimReportsUnknownOnce(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, dispatch := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); !dispatch {
		t.Fatal("retry was not admitted")
	}
	// Simulate the process stopping at this boundary, before runTask is called.
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	restarted.sched.inflight.Wait()
	if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry != nil || scheduleSendCount(f) != 0 {
		t.Fatal("restored claim was sent again or not settled")
	}
	runs := scheduleRuns(restarted)
	if len(runs) == 0 || runs[len(runs)-1].ID != "retry_unknown_failed" || runs[len(runs)-1].Error == "" {
		t.Fatalf("missing unknown result: %+v", runs)
	}
	f.mu.Lock()
	pushes := len(f.pushes)
	f.mu.Unlock()
	if pushes != 1 {
		t.Fatalf("got %d notifications, want 1", pushes)
	}
	again := newServiceOn(t, f, st)
	again.runLimitRetries(t.Context(), now.Add(4*time.Hour))
	f.mu.Lock()
	pushes = len(f.pushes)
	f.mu.Unlock()
	if pushes != 1 || scheduleSendCount(f) != 0 {
		t.Fatal("settled claim was reported or sent again")
	}
}

func TestLimitRetryCrashAfterSendBeforeResultReportsUnknown(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("claim failed")
	}
	if err := s.call(t.Context(), "server", DeviceAssistantSend, map[string]any{"session_id": "s1", "text": "Example"}, nil); err != nil {
		t.Fatal(err)
	}
	// The node accepted the send, but the manager stopped before recording its result.
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("accepted send repeated: %d", got)
	}
	runs := scheduleRuns(restarted)
	if len(runs) == 0 || runs[len(runs)-1].ID != "retry_unknown_failed" {
		t.Fatal("missing unknown result after the send")
	}
	f.mu.Lock()
	pushes := len(f.pushes)
	f.mu.Unlock()
	if pushes != 1 {
		t.Fatalf("got %d unknown notifications", pushes)
	}
}

func TestLimitRetryOwnerLostAfterClaimDoesNotSend(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	cur, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour))
	if !ok {
		t.Fatal("claim failed")
	}
	done := make(chan struct{})
	close(done)
	s.dispatchLimitRetry(cur, w, now.Add(2*time.Hour), done)
	if got := scheduleSendCount(f); got != 0 {
		t.Fatalf("sent after owner loss: %d", got)
	}
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	if got := scheduleSendCount(f); got != 0 {
		t.Fatalf("restored claim was resent: %d", got)
	}
	runs := scheduleRuns(restarted)
	if len(runs) == 0 || runs[len(runs)-1].ID != "retry_unknown_failed" {
		t.Fatal("owner loss did not leave an unknown outcome")
	}
}

func TestLimitRetryPostgresManagersClaimOnce(t *testing.T) {
	st := scheduleStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	started := now.Add(-48 * time.Hour)
	run := ScheduleRun{ID: "pg-failed", RefID: "pg-limit", At: started, MachineID: "server", SessionID: "s1"}
	task := Task{ID: "pg-limit", Title: "Limit", Prompt: "Example", Repeat: repeatOnce, At: started, LastRun: started,
		LimitRetry: &scheduleLimitRetry{At: now, Turn: now, LastRun: started, Failed: run}}
	if err := st.PutSetting(t.Context(), tasksKey, []Task{task}); err != nil {
		t.Fatal(err)
	}
	f := readyHub()
	first := newServiceOn(t, f, st)
	second := newServiceOn(t, f, st)
	first.loadSchedule(t.Context())
	second.loadSchedule(t.Context())
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	_, a := first.consumeLimitRetry(t.Context(), task.ID, *task.LimitRetry, c, now)
	_, b := second.consumeLimitRetry(t.Context(), task.ID, *task.LimitRetry, c, now)
	if !a || b {
		t.Fatalf("Postgres claim results: first=%v second=%v", a, b)
	}
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(time.Minute))
	var runs []ScheduleRun
	if _, err := st.Setting(t.Context(), runsKey, &runs); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.ID == "retry_unknown_pg-failed" }) {
		t.Fatalf("Postgres recovery did not store the unknown outcome: %+v", runs)
	}
}

func TestLimitRetryOverlappingManagersClaimOnce(t *testing.T) {
	first, f, st, now := limitRetryFixture(t)
	second := newServiceOn(t, f, st)
	second.loadSchedule(t.Context())
	task, _ := taskByID(first, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	_, a := first.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour))
	_, b := second.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour))
	if !a || b {
		t.Fatalf("stale manager claimed: first=%v second=%v", a, b)
	}
}

func TestLimitRetryStaleManagerCannotRestoreCancelledTask(t *testing.T) {
	first, f, st, now := limitRetryFixture(t)
	second := newServiceOn(t, f, st)
	second.loadSchedule(t.Context())
	task, _ := taskByID(second, "limit")
	w := *task.LimitRetry
	if _, err := first.deleteTask(t.Context(), task.ID); err != nil {
		t.Fatal(err)
	}
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, ok := second.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); ok {
		t.Fatal("stale manager claimed a deleted task")
	}
	restarted := newServiceOn(t, f, st)
	if _, ok := taskByID(restarted, "limit"); ok {
		t.Fatal("stale manager restored a deleted task")
	}
}

func TestLimitRetryRestartAfterStoredResultDoesNotReportUnknown(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("claim failed")
	}
	runs := append(scheduleRuns(s), ScheduleRun{ID: "result", RefID: task.ID, Status: runOK, Retried: true, RetryOf: w.Failed.ID})
	if err := st.PutSetting(t.Context(), runsKey, runs); err != nil {
		t.Fatal(err)
	}
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry != nil || scheduleSendCount(f) != 0 {
		t.Fatal("stored result was resent or claim retained")
	}
	f.mu.Lock()
	pushes := len(f.pushes)
	f.mu.Unlock()
	if pushes != 0 {
		t.Fatalf("stored result incorrectly reported unknown: %d notifications", pushes)
	}
}

func TestLimitRetryUnknownNotificationWaitsForHub(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("claim failed")
	}
	restarted := newServiceOn(t, f, st)
	h := &limitNotifyHub{fakeHub: f, fail: true}
	restarted.hub = h
	restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry == nil || !task.LimitRetry.Claimed {
		t.Fatal("failed notification removed the claim")
	}
	h.fail = false
	restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	f.mu.Lock()
	pushes := append([]Notification(nil), f.pushes...)
	f.mu.Unlock()
	if len(pushes) != 1 || pushes[0].ID != "ntf_retry_failed" {
		t.Fatalf("notification after recovery: %+v", pushes)
	}
}

func TestLimitRetryUnknownNotificationRetriedAfterClearFailure(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("claim failed")
	}
	st.failWrites(tasksKey)
	firstRestart := newServiceOn(t, f, st)
	firstRestart.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	if task, ok := taskByID(firstRestart, "limit"); !ok || task.LimitRetry == nil {
		t.Fatal("failed cleanup removed the claim")
	}
	st.failWrites("")
	secondRestart := newServiceOn(t, f, st)
	secondRestart.runLimitRetries(t.Context(), now.Add(4*time.Hour))
	f.mu.Lock()
	pushes := len(f.pushes)
	f.mu.Unlock()
	if pushes != 2 {
		t.Fatalf("got %d notification attempts, want two", pushes)
	}
	var unknown int
	for _, run := range scheduleRuns(secondRestart) {
		if run.ID == "retry_unknown_failed" {
			unknown++
		}
	}
	if unknown != 1 {
		t.Fatalf("got %d unknown records, want one", unknown)
	}
}

func TestLimitRetryOutlivesWeekAndTruncatedHistory(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	sc, err := s.lockedSchedule(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tasks := append([]Task(nil), sc.tasks...)
	w := *tasks[0].LimitRetry
	w.At = now.Add(7 * 24 * time.Hour)
	tasks[0].LimitRetry = &w
	err = s.saveLocked(sc, tasks)
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for range maxRuns + 1 {
		s.recordRun(ScheduleRun{RefID: "other", At: now, Status: runOK})
	}
	restarted := newServiceOn(t, f, st)
	if next := restarted.nextDue(t.Context(), now); !next.Equal(w.At) {
		t.Fatalf("after history truncation due %v, want %v", next, w.At)
	}
	restarted.runLimitRetries(t.Context(), w.At.Add(-time.Second))
	if got := scheduleSendCount(f); got != 0 {
		t.Fatalf("sent before reset: %d", got)
	}
	restarted.runLimitRetries(t.Context(), w.At)
	restarted.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("sent at reset: %d", got)
	}
}

func TestLimitRetryHistoryDoesNotRestoreTruncatedRuns(t *testing.T) {
	s, _, st, now := limitRetryFixture(t)
	for range maxRuns {
		s.recordRun(ScheduleRun{RefID: "old", At: now, Status: runOK})
	}
	var newer []ScheduleRun
	for i := range maxRuns {
		newer = append(newer, ScheduleRun{ID: fmt.Sprintf("new-%03d", i), RefID: "new", At: now.Add(time.Hour), Status: runOK})
	}
	raw, err := json.Marshal(newer)
	if err != nil {
		t.Fatal(err)
	}
	_, rev, _, err := st.ReadScheduleRuns(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := st.CompareScheduleRuns(t.Context(), rev, raw); err != nil || !ok {
		t.Fatalf("replace history: %v %v", ok, err)
	}
	s.recordRun(ScheduleRun{RefID: "latest", At: now.Add(2 * time.Hour), Status: runOK})
	var stored []ScheduleRun
	if _, err := st.Setting(t.Context(), runsKey, &stored); err != nil {
		t.Fatal(err)
	}
	for _, r := range stored {
		if r.RefID == "old" {
			t.Fatal("stale cached history replaced newer records")
		}
	}
	if len(stored) != maxRuns {
		t.Fatalf("history contains %d runs, want %d", len(stored), maxRuns)
	}
}

func TestLimitRetryUnknownHistoryMergesConcurrentRun(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("claim failed")
	}
	other := newServiceOn(t, f, st)
	other.loadSchedule(t.Context())
	restarted := newServiceOn(t, f, st)
	restarted.loadSchedule(t.Context())
	st.mu.Lock()
	st.beforeRunsCompare = func() { other.recordRun(ScheduleRun{RefID: "other", At: now, Status: runOK}) }
	st.mu.Unlock()
	restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	var stored []ScheduleRun
	if _, err := st.Setting(t.Context(), runsKey, &stored); err != nil {
		t.Fatal(err)
	}
	var foundOther, foundUnknown bool
	for _, r := range stored {
		foundOther = foundOther || r.RefID == "other"
		foundUnknown = foundUnknown || r.ID == "retry_unknown_failed"
	}
	if !foundOther || !foundUnknown {
		t.Fatalf("concurrent history was overwritten: %+v", stored)
	}
}

func TestLimitRetryUnknownWaitsForHistoryStorage(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("claim failed")
	}
	st.failWrites(runsKey)
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry == nil || !task.LimitRetry.Claimed {
		t.Fatal("claim lost while history storage was unavailable")
	}
	f.mu.Lock()
	pushes := len(f.pushes)
	f.mu.Unlock()
	if pushes != 0 {
		t.Fatal("notification preceded durable history")
	}
	st.failWrites("")
	restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	f.mu.Lock()
	pushes = len(f.pushes)
	f.mu.Unlock()
	if pushes != 1 {
		t.Fatalf("after storage recovery got %d notifications", pushes)
	}
}

func TestLimitRetryOverdueScansDispatchOneAtATime(t *testing.T) {
	s, f, _, now := limitRetryFixture(t)
	sc, err := s.lockedSchedule(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tasks := append([]Task(nil), sc.tasks...)
	second := tasks[0]
	second.ID = "limit-second"
	wait := *second.LimitRetry
	wait.Failed.ID, wait.Failed.RefID = "failed-second", second.ID
	second.LimitRetry = &wait
	tasks = append(tasks, second)
	err = s.saveLocked(sc, tasks)
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	s.runLimitRetries(t.Context(), now.Add(8*24*time.Hour))
	s.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("first scan sent %d, want 1", got)
	}
	s.runLimitRetries(t.Context(), now.Add(8*24*time.Hour+time.Second))
	s.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 2 {
		t.Fatalf("second scan sent %d, want 2", got)
	}
}

func TestLimitRetryConcurrentScansDispatchOnce(t *testing.T) {
	s, f, _, now := limitRetryFixture(t)
	var scans sync.WaitGroup
	for range 2 {
		scans.Add(1)
		go func() {
			defer scans.Done()
			s.runLimitRetries(t.Context(), now.Add(2*time.Hour))
		}()
	}
	scans.Wait()
	s.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("concurrent scans sent %d times", got)
	}
}

func TestLimitRetryShutdownJoinsWorkerBeforeOwnerRelease(t *testing.T) {
	s, _, base, _ := limitRetryFixture(t)
	st := &limitOwnedStore{limitTestStore: base, acquired: make(chan struct{}), released: make(chan struct{})}
	s.store = func() Store { return st }
	joining := make(chan struct{})
	s.sched.limitJoining = func() { close(joining) }
	s.sched.limitInflight.Add(1)
	var worker sync.Once
	doneWorker := func() { worker.Do(s.sched.limitInflight.Done) }
	defer doneWorker()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { s.scheduleLoop(ctx); close(done) }()
	awaitBusSignal(t, st.acquired)
	cancel()
	awaitBusSignal(t, joining)
	select {
	case <-st.released:
		t.Fatal("owner released while a retry worker was in flight")
	default:
	}
	doneWorker()
	awaitBusSignal(t, st.released)
	awaitBusSignal(t, done)
}

func TestLimitRetryRequiresOwnerCapability(t *testing.T) {
	s, f, _, now := limitRetryFixture(t)
	sc, err := s.lockedSchedule(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tasks := append([]Task(nil), sc.tasks...)
	w := *tasks[0].LimitRetry
	w.At = now.Add(-time.Hour)
	tasks[0].LimitRetry = &w
	err = s.saveLocked(sc, tasks)
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.scheduleLoop(ctx) // the test store has versioning but no owner capability
	if task, ok := taskByID(s, "limit"); !ok || task.LimitRetry == nil || scheduleSendCount(f) != 0 {
		t.Fatal("retry ran without a scheduler owner")
	}
}

func TestLimitRetryWaitsInStandby(t *testing.T) {
	s, f, _, now := limitRetryFixture(t)
	s.sched.mu.Lock()
	s.sched.tasks[s.sched.indexLocked("limit")].LimitRetry.At = now.Add(-time.Hour)
	s.sched.mu.Unlock()
	f.hello.Assistant.Standby = true
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.scheduleLoop(ctx)
	if task, ok := taskByID(s, "limit"); !ok || task.LimitRetry == nil || scheduleSendCount(f) != 0 {
		t.Fatal("standby consumed or sent the retry")
	}
}

func TestLimitRetryWriteFailureLeavesItDue(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	st.failWrites(tasksKey)
	s.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	if got := scheduleSendCount(f); got != 0 {
		t.Fatalf("sent with failed persistence: %d", got)
	}
	st.failWrites("")
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	restarted.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("after recovery sent %d times", got)
	}
}

func TestLimitRetryWaitsForConversationToBeReadable(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	f.setSessions("server")
	s.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	if task, ok := taskByID(s, "limit"); !ok || task.LimitRetry == nil || scheduleSendCount(f) != 0 {
		t.Fatal("an unreadable conversation consumed or sent the retry")
	}
	restarted := newServiceOn(t, f, st)
	if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry == nil {
		t.Fatal("the retry did not survive a restart while its conversation was unavailable")
	}
	f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now})
	restarted.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	restarted.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("sent %d times after the conversation became readable", got)
	}
}

func TestLimitRetryDoesNotSendAgainAfterAmbiguousFailure(t *testing.T) {
	s, f, _, now := limitRetryFixture(t)
	f.mu.Lock()
	f.failAssistant = errors.New("response lost")
	f.mu.Unlock()
	s.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	s.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("ambiguous failure caused %d sends", got)
	}
	runs := scheduleRuns(s)
	if len(runs) == 0 || runs[len(runs)-1].ID != "retry_unknown_failed" || !strings.Contains(runs[len(runs)-1].Error, "可能") {
		t.Fatalf("ambiguous delivery was not recorded as unknown: %+v", runs)
	}
	f.mu.Lock()
	pushes := append([]Notification(nil), f.pushes...)
	f.mu.Unlock()
	if len(pushes) != 1 || !strings.Contains(pushes[0].Body, "可能") {
		t.Fatalf("ambiguous delivery was not reported: %+v", pushes)
	}
}

func TestLimitRetryCancelledOrConversationContinued(t *testing.T) {
	for _, action := range []string{"delete", "replace", "continue"} {
		t.Run(action, func(t *testing.T) {
			s, f, st, now := limitRetryFixture(t)
			switch action {
			case "delete":
				if _, err := s.deleteTask(context.Background(), "limit"); err != nil {
					t.Fatal(err)
				}
			case "replace":
				sc, err := s.lockedSchedule(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				tasks := append([]Task(nil), sc.tasks...)
				tasks[sc.indexLocked("limit")].Enabled = true
				err = s.saveLocked(sc, tasks)
				sc.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "continue":
				f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now.Add(time.Minute)})
			}
			restarted := newServiceOn(t, f, st)
			restarted.runLimitRetries(t.Context(), now.Add(2*time.Hour))
			restarted.sched.inflight.Wait()
			if got := scheduleSendCount(f); got != 0 {
				t.Fatalf("sent after %s: %d", action, got)
			}
			if task, ok := taskByID(restarted, "limit"); ok && task.LimitRetry != nil {
				t.Fatal("cancelled wait was retained")
			}
		})
	}
}
