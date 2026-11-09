package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	acquired  chan struct{}
	released  chan struct{}
	onAcquire func()
}

func (st *limitOwnedStore) TryScheduleOwner(ctx context.Context) (<-chan struct{}, func(), bool, error) {
	done := make(chan struct{})
	close(st.acquired)
	if st.onAcquire != nil {
		st.onAcquire()
	}
	return done, func() { close(done); close(st.released) }, true, nil
}

type limitTestStore struct {
	*scheduleWriteStore
	mu                sync.Mutex
	revision          int64
	runsRevision      int64
	failModelRead     atomic.Bool
	blockTitleRead    atomic.Bool
	titleReadStarted  chan struct{}
	titleReadOnce     sync.Once
	blockOriginRead   atomic.Bool
	failOriginRead    atomic.Bool
	originFailStarted chan struct{}
	originFailOnce    sync.Once
	originReadStarted chan struct{}
	originReadRelease chan struct{}
	originReadOnce    sync.Once
	blockScheduleRead atomic.Bool
	failScheduleRead  atomic.Bool
	failReadStarted   chan struct{}
	failReadOnce      sync.Once
	failNextTaskRead  atomic.Bool
	readStarted       chan struct{}
	readOnce          sync.Once
	beforeTaskCompare func()
	afterTaskWrite    func()
	beforeRunsCompare func()
	afterRunsWrite    func()
}

func (st *limitTestStore) Setting(ctx context.Context, key string, value any) (bool, error) {
	if key == originsKey && st.failOriginRead.Load() {
		if st.originFailStarted != nil {
			st.originFailOnce.Do(func() { close(st.originFailStarted) })
		}
		return false, context.DeadlineExceeded
	}
	if key == originsKey && st.blockOriginRead.Load() {
		st.originReadOnce.Do(func() { close(st.originReadStarted) })
		select {
		case <-st.originReadRelease:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if key == titlesKey && st.blockTitleRead.Load() {
		st.titleReadOnce.Do(func() { close(st.titleReadStarted) })
		<-ctx.Done()
		return false, ctx.Err()
	}
	if st.failModelRead.Load() && strings.HasPrefix(key, "model_config.") {
		return false, errors.New("model configuration unavailable")
	}
	return st.scheduleWriteStore.Setting(ctx, key, value)
}

func (st *limitTestStore) ReadSchedule(ctx context.Context) (json.RawMessage, int64, bool, error) {
	if st.failNextTaskRead.Swap(false) {
		return nil, 0, false, errors.New("task readback failed")
	}
	if st.failScheduleRead.Load() {
		if st.failReadStarted != nil {
			st.failReadOnce.Do(func() { close(st.failReadStarted) })
		}
		return nil, 0, false, context.DeadlineExceeded
	}
	if st.blockScheduleRead.Load() {
		st.readOnce.Do(func() { close(st.readStarted) })
		<-ctx.Done()
		return nil, 0, false, ctx.Err()
	}
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
	if hook := st.afterTaskWrite; hook != nil {
		st.afterTaskWrite = nil
		hook()
	}
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
	if hook := st.afterRunsWrite; hook != nil {
		st.afterRunsWrite = nil
		hook()
	}
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

func TestLimitRetryHiddenFromLegacyTaskViewsAndEdits(t *testing.T) {
	for _, repeat := range []string{repeatDaily, repeatInterval} {
		t.Run(repeat, func(t *testing.T) {
			s, f, st, now := limitRetryFixture(t)
			sc, err := s.lockedSchedule(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			tasks := slices.Clone(sc.tasks)
			wait := *tasks[0].LimitRetry
			tasks[0].LimitRetry = nil
			err = s.saveLocked(sc, tasks)
			if err == nil {
				tasks = slices.Clone(sc.tasks)
				tasks[0].Repeat, tasks[0].Enabled = repeat, true
				if repeat == repeatDaily {
					tasks[0].Time, tasks[0].Weekdays, tasks[0].At = "09:00", []int{1, 2, 3, 4, 5}, time.Time{}
					tasks[0].EveryMinutes = 0
				} else {
					tasks[0].Time, tasks[0].Weekdays = "", nil
					tasks[0].EveryMinutes, tasks[0].At = 30, now.Add(time.Hour)
				}
				tasks[0].LimitRetry = &wait
				err = s.saveLocked(sc, tasks)
			}
			sc.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			paths := []string{"tasks"}
			if repeat == repeatDaily {
				paths = append(paths, "routines")
			} else {
				paths = append(paths, "followups")
			}
			var tasksJSON map[string]any
			for _, path := range paths {
				res := scheduleRequest(s, "GET", path, "")
				if res.Code != 200 || strings.Contains(res.Body.String(), "limit_retry") {
					t.Fatalf("%s exposed retry: %d %s", path, res.Code, res.Body)
				}
				if path == "tasks" {
					if err := json.Unmarshal(res.Body.Bytes(), &tasksJSON); err != nil {
						t.Fatal(err)
					}
				}
			}
			items := tasksJSON["tasks"].([]any)
			in := items[0].(map[string]any)
			in["prompt"] = "Updated prompt"
			body, err := json.Marshal(in)
			if err != nil {
				t.Fatal(err)
			}
			res := scheduleRequest(s, "PUT", "tasks/limit", string(body))
			if res.Code != 200 || strings.Contains(res.Body.String(), "limit_retry") {
				t.Fatalf("PUT: %d %s", res.Code, res.Body)
			}
			if repeat == repeatDaily {
				routines := scheduleRequest(s, "GET", "routines", "")
				legacy := scheduleRequest(s, "PUT", "routines", routines.Body.String())
				if legacy.Code != 200 || strings.Contains(legacy.Body.String(), "limit_retry") {
					t.Fatalf("legacy routines PUT: %d %s", legacy.Code, legacy.Body)
				}
			}
			restarted := newServiceOn(t, f, st)
			stored, ok := taskByID(restarted, "limit")
			if !ok || stored.Prompt != "Updated prompt" || stored.LimitRetry == nil || stored.LimitRetry.Failed.ID != wait.Failed.ID {
				t.Fatalf("legacy edit lost wait: %+v", stored)
			}
		})
	}
}

func TestLimitRetryArmStopsOnShutdownDuringStorageFailure(t *testing.T) {
	s, _, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	sc, err := s.lockedSchedule(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tasks := slices.Clone(sc.tasks)
	tasks[0].LimitRetry = nil
	err = s.saveLocked(sc, tasks)
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
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

func TestLimitRetryOwnerFindsWaitRegisteredByAnotherManager(t *testing.T) {
	owner, f, st, now := limitRetryFixture(t)
	ownerStore := &limitOwnedStore{limitTestStore: st, acquired: make(chan struct{}), released: make(chan struct{})}
	owner.store = func() Store { return ownerStore }
	_, release, acquired, err := ownerStore.TryScheduleOwner(t.Context())
	if err != nil || !acquired {
		t.Fatal("owner lock was not acquired")
	}
	defer release()
	task, _ := taskByID(owner, "limit")
	w := *task.LimitRetry
	sc, err := owner.lockedSchedule(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tasks := slices.Clone(sc.tasks)
	tasks[sc.indexLocked("limit")].LimitRetry = nil
	err = owner.saveLocked(sc, tasks)
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	other := newServiceOn(t, f, st)
	if !other.armLimitRetry(t.Context(), w.Failed, w.Turn, now.Add(time.Hour)) {
		t.Fatal("arm failed")
	}
	if task, _ := taskByID(owner, "limit"); task.LimitRetry != nil {
		t.Fatal("owner cache unexpectedly updated")
	}
	if !owner.refreshLimitSchedule(t.Context()) {
		t.Fatal("refresh failed")
	}
	if task, _ := taskByID(owner, "limit"); task.LimitRetry == nil {
		t.Fatal("owner did not discover remote wait")
	}
	owner.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	owner.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("sent %d times", got)
	}
}

func TestLimitRetryClaimPreventsPeriodicAdmission(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	sc, err := s.lockedSchedule(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tasks := slices.Clone(sc.tasks)
	w := *tasks[0].LimitRetry
	tasks[0].LimitRetry = nil
	err = s.saveLocked(sc, tasks)
	if err == nil {
		tasks = slices.Clone(sc.tasks)
		tasks[0].Repeat, tasks[0].Enabled, tasks[0].EveryMinutes = repeatInterval, true, 30
		tasks[0].At, tasks[0].LimitRetry = now.Add(-time.Minute), &w
		err = s.saveLocked(sc, tasks)
	}
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, ok := s.consumeLimitRetry(t.Context(), "limit", w, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("retry claim failed")
	}
	s.runDue(t.Context(), now)
	s.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 0 {
		t.Fatalf("periodic send during claimed retry: %d", got)
	}
	if !s.releaseUnsentLimitClaim("limit", w) {
		t.Fatal("could not return unsent claim")
	}
	if task, ok := taskByID(newServiceOn(t, f, st), "limit"); !ok || task.LimitRetry == nil || task.LimitRetry.Claimed {
		t.Fatal("pending retry lost after release")
	}
}

func TestLimitRetryPreservedWhenNextRunIsSkipped(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	sc, err := s.lockedSchedule(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tasks := slices.Clone(sc.tasks)
	w := *tasks[0].LimitRetry
	tasks[0].LimitRetry = nil
	err = s.saveLocked(sc, tasks)
	if err == nil {
		tasks = slices.Clone(sc.tasks)
		tasks[0].Repeat, tasks[0].Enabled, tasks[0].EveryMinutes = repeatInterval, true, 30
		tasks[0].At = now.Add(-time.Minute)
		tasks[0].LimitRetry = &w
		err = s.saveLocked(sc, tasks)
	}
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !s.sched.begin("limit") {
		t.Fatal("could not hold task busy")
	}
	s.runDue(t.Context(), now)
	s.sched.inflight.Wait()
	s.sched.end("limit")
	restarted := newServiceOn(t, f, st)
	if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry == nil {
		t.Fatal("skipped run discarded retry")
	}
	restarted.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	restarted.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("retry sends: %d", got)
	}
}

func TestLimitRetryWaitsForAdmittedRunBeforeClaim(t *testing.T) {
	s, _, _, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	s.sched.mu.Lock()
	s.sched.scheduledSending = map[string]int{"limit": 1}
	s.sched.mu.Unlock()
	if _, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); ok {
		t.Fatal("claimed while another run was admitted")
	}
	s.sched.mu.Lock()
	delete(s.sched.scheduledSending, "limit")
	s.sched.mu.Unlock()
	if _, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("did not claim after admitted run ended")
	}
}

func TestLimitRetryLaterRunOutcomeControlsCancellation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      string
		session     string
		wantPending bool
	}{
		{"not_started", runFailed, "", true},
		{"skipped", runSkipped, "", true},
		{"sent", runOK, "new-session", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, st, now := limitRetryFixture(t)
			sc, err := s.lockedSchedule(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			tasks := slices.Clone(sc.tasks)
			tasks[0].LastRun = now
			err = s.saveLocked(sc, tasks)
			sc.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			s.recordRun(ScheduleRun{RefID: "limit", At: now, Status: tc.status, SessionID: tc.session})
			restarted := newServiceOn(t, f, st)
			task, ok := taskByID(restarted, "limit")
			if !ok || (task.LimitRetry != nil) != tc.wantPending {
				t.Fatalf("pending = %v, want %v", ok && task.LimitRetry != nil, tc.wantPending)
			}
		})
	}
}

func TestLimitRetryOriginReadFailureRecoversWithoutGrantingPending(t *testing.T) {
	_, f, st, _ := limitRetryFixture(t)
	f.mu.Lock()
	f.hello.Assistant.Machines = []string{"server", "other"}
	f.mu.Unlock()
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) { v.MachineID = "other" })
	st.originFailStarted = make(chan struct{})
	st.failOriginRead.Store(true)
	s.origins.mu.Lock()
	s.origins.loaded = false
	s.origins.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { s.retryStartedOrigin(ctx, "server", "retry-session", OriginTask, "limit"); close(done) }()
	awaitBusSignal(t, st.originFailStarted)
	if s.assistantCaller(t.Context(), "server", "retry-session") {
		t.Fatal("pending origin granted tool authorization")
	}
	st.failOriginRead.Store(false)
	awaitBusSignal(t, done)
	if !s.assistantCaller(t.Context(), "server", "retry-session") {
		t.Fatal("recorded origin did not authorize its session")
	}
	if s.assistantCaller(t.Context(), "wrong-machine", "retry-session") {
		t.Fatal("origin authorized a different machine")
	}
	restarted := newServiceOn(t, f, st)
	setSettings(t, restarted, func(v *Settings) { v.MachineID = "other" })
	if !restarted.assistantCaller(t.Context(), "server", "retry-session") {
		t.Fatal("stored origin did not survive restart")
	}
}

func TestLimitRetryOriginWriteFailureDoesNotGrantAuthorization(t *testing.T) {
	_, f, st, _ := limitRetryFixture(t)
	f.mu.Lock()
	f.hello.Assistant.Machines = []string{"server", "other"}
	f.mu.Unlock()
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) { v.MachineID = "other" })
	st.failWrites(originsKey)
	if err := s.recordRetryOrigin(t.Context(), "server", "retry-session", OriginTask, "limit"); err == nil {
		t.Fatal("origin write unexpectedly succeeded")
	}
	if s.assistantCaller(t.Context(), "server", "retry-session") {
		t.Fatal("failed origin write granted authorization")
	}
	st.failWrites("")
	if err := s.recordRetryOrigin(t.Context(), "server", "retry-session", OriginTask, "limit"); err != nil {
		t.Fatal(err)
	}
	if !s.assistantCaller(t.Context(), "server", "retry-session") {
		t.Fatal("persisted origin did not authorize the session")
	}
}

func TestLimitRetryOriginDoesNotReplaceEstablishedSource(t *testing.T) {
	_, f, st, _ := limitRetryFixture(t)
	f.mu.Lock()
	f.hello.Assistant.Machines = []string{"server", "other"}
	f.mu.Unlock()
	old := &convOrigin{MachineID: "other", Origin: OriginUser, By: originByStart}
	if err := st.PutSetting(t.Context(), originsKey, map[string]*convOrigin{"retry-session": old}); err != nil {
		t.Fatal(err)
	}
	s := newServiceOn(t, f, st)
	setSettings(t, s, func(v *Settings) { v.MachineID = "other" })
	if err := s.recordRetryOrigin(t.Context(), "server", "retry-session", OriginTask, "limit"); err != nil {
		t.Fatal(err)
	}
	var stored map[string]*convOrigin
	if _, err := st.Setting(t.Context(), originsKey, &stored); err != nil {
		t.Fatal(err)
	}
	if got := stored["retry-session"]; got == nil || got.MachineID != "other" || got.Origin != OriginUser {
		t.Fatalf("established origin overwritten: %+v", got)
	}
	if s.assistantCaller(t.Context(), "server", "retry-session") {
		t.Fatal("conflicting origin granted authorization")
	}
}

func TestLimitRetryStartKeepsAuthorizationUntilOriginRecorded(t *testing.T) {
	_, f, st, _ := limitRetryFixture(t)
	s := newServiceOn(t, f, st)
	st.originReadStarted, st.originReadRelease = make(chan struct{}), make(chan struct{})
	st.blockOriginRead.Store(true)
	s.origins.mu.Lock()
	s.origins.loaded = false
	s.origins.mu.Unlock()
	ctx := context.WithValue(t.Context(), limitStartKey{}, true)
	sess, err := s.startConversationOn(ctx, StartParams{Origin: OriginTask, OriginRef: "limit"}, false)
	if err != nil || sess.SessionID == "" {
		t.Fatalf("start: %+v %v", sess, err)
	}
	awaitBusSignal(t, st.originReadStarted)
	s.startingAssistants.mu.Lock()
	active := s.startingAssistants.machines["server"]
	s.startingAssistants.mu.Unlock()
	if active != 1 {
		t.Fatalf("startup authorization released before origin: %d", active)
	}
	close(st.originReadRelease)
	waitFor(t, "origin and startup authorization", func() bool {
		s.startingAssistants.mu.Lock()
		n := s.startingAssistants.machines["server"]
		s.startingAssistants.mu.Unlock()
		if n != 0 {
			return false
		}
		s.origins.mu.Lock()
		origin := s.origins.m[sess.SessionID]
		s.origins.mu.Unlock()
		return origin != nil && origin.Origin == OriginTask
	})
}

func TestLimitRetryStartPreSendClassification(t *testing.T) {
	_, f, st, _ := limitRetryFixture(t)
	s := newServiceOn(t, f, st)
	preErr := errors.New("prepare failed")
	f.mu.Lock()
	f.nodes = map[string]nodeAnswer{DeviceAssistantPrepare: func(string, json.RawMessage) (any, error) { return nil, preErr }}
	f.mu.Unlock()
	_, ordinary := s.startOn(t.Context(), "server", StartParams{}, false)
	if !errors.Is(ordinary, preErr) || errors.Is(ordinary, errLimitRetryNotSent) || ordinary.Error() != preErr.Error() {
		t.Fatalf("ordinary error changed: %v", ordinary)
	}
	ctx := context.WithValue(t.Context(), limitStartKey{}, true)
	_, marked := s.startOn(ctx, "server", StartParams{}, false)
	if !errors.Is(marked, preErr) || !errors.Is(marked, errLimitRetryNotSent) || marked.Error() != preErr.Error() {
		t.Fatalf("pre-send classification: %v", marked)
	}
	f.mu.Lock()
	f.nodes[DeviceAssistantPrepare] = func(string, json.RawMessage) (any, error) { return nil, ErrNoDevice }
	f.mu.Unlock()
	_, noMachine := s.startConversationOn(ctx, StartParams{}, false)
	if !errors.Is(noMachine, errLimitRetryNotSent) || !errors.Is(noMachine, errNoAssistantMachine) {
		t.Fatalf("missing machine classification: %v", noMachine)
	}
	_, ordinaryNoMachine := s.startConversationOn(t.Context(), StartParams{}, false)
	if !errors.Is(ordinaryNoMachine, errNoAssistantMachine) || errors.Is(ordinaryNoMachine, errLimitRetryNotSent) {
		t.Fatalf("ordinary missing-machine error: %v", ordinaryNoMachine)
	}
	f.mu.Lock()
	f.hello.Assistant.WorkMachines = []string{"server"}
	f.mu.Unlock()
	_, work := s.startConversationOn(ctx, StartParams{MachineID: "server"}, false)
	if !errors.Is(work, errWorkAssistant) || !errors.Is(work, errLimitRetryNotSent) {
		t.Fatalf("work machine classification: %v", work)
	}
	_, ordinaryWork := s.startConversationOn(t.Context(), StartParams{MachineID: "server"}, false)
	if !errors.Is(ordinaryWork, errWorkAssistant) || errors.Is(ordinaryWork, errLimitRetryNotSent) || ordinaryWork.Error() != errWorkAssistant.Error() {
		t.Fatalf("ordinary work-machine error changed: %v", ordinaryWork)
	}
}

func TestLimitRetryManualOwnConversationPreSendAndAmbiguousErrors(t *testing.T) {
	for _, mode := range []string{"prepare", "response_lost", "success"} {
		t.Run(mode, func(t *testing.T) {
			s, f, st, now := limitRetryFixture(t)
			sc, err := s.lockedSchedule(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			tasks := slices.Clone(sc.tasks)
			w := *tasks[0].LimitRetry
			tasks[0].LimitRetry = nil
			err = s.saveLocked(sc, tasks)
			if err == nil {
				tasks = slices.Clone(sc.tasks)
				tasks[0].SessionID, tasks[0].RunSessionID, tasks[0].LimitRetry = "", "", &w
				err = s.saveLocked(sc, tasks)
			}
			sc.ownerHeld = true
			sc.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			if mode == "prepare" {
				f.nodes = map[string]nodeAnswer{DeviceAssistantPrepare: func(string, json.RawMessage) (any, error) { return nil, errors.New("prepare failed") }}
			} else if mode == "response_lost" {
				f.failAssistant = errors.New("response lost")
			}
			f.mu.Unlock()
			res := scheduleRequest(s, "POST", "tasks/limit/run", "")
			if (res.Code == 200) != (mode == "success") {
				t.Fatalf("%s: HTTP %d: %s", mode, res.Code, res.Body)
			}
			restarted := newServiceOn(t, f, st)
			task, ok := taskByID(restarted, "limit")
			if mode == "prepare" {
				if !ok || task.LimitRetry == nil || task.LimitRetry.Claimed || scheduleSendCount(f) != 0 {
					t.Fatal("pre-send failure did not restore wait")
				}
			} else if mode == "response_lost" {
				if !ok || task.LimitRetry != nil || scheduleSendCount(f) != 1 {
					t.Fatal("ambiguous send was retried or lost its outcome")
				}
				runs := scheduleRuns(restarted)
				if !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.ID == "retry_unknown_failed" && r.Manual }) {
					t.Fatal("missing manual unknown outcome")
				}
			} else if !ok || task.LimitRetry != nil || task.RunSessionID == "" || task.RunMachineID == "" {
				t.Fatalf("successful new conversation was not adopted: %+v", task)
			}
			_ = now
		})
	}
}

func TestLimitRetryManualRunRequiresOwner(t *testing.T) {
	owner, f, st, _ := limitRetryFixture(t)
	owner.sched.mu.Lock()
	owner.sched.ownerHeld = true
	owner.sched.mu.Unlock()
	other := newServiceOn(t, f, st)
	res := scheduleRequest(other, "POST", "tasks/limit/run", "")
	if res.Code != 503 || scheduleSendCount(f) != 0 {
		t.Fatalf("non-owner sent or was accepted: %d", res.Code)
	}
	if task, ok := taskByID(newServiceOn(t, f, st), "limit"); !ok || task.LimitRetry == nil || task.LimitRetry.Claimed {
		t.Fatal("non-owner consumed wait")
	}
}

func TestLimitRetryUnknownRecordsActualAttemptMode(t *testing.T) {
	for _, manual := range []bool{false, true} {
		t.Run(fmt.Sprintf("manual=%v", manual), func(t *testing.T) {
			s, f, st, now := limitRetryFixture(t)
			sc, err := s.lockedSchedule(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			tasks := slices.Clone(sc.tasks)
			wait := *tasks[0].LimitRetry
			wait.Failed.Manual = !manual // the failed run's mode is not the retry attempt's mode
			tasks[0].LimitRetry = &wait
			err = s.saveLocked(sc, tasks)
			sc.ownerHeld = true
			sc.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if manual {
				if _, w, _, _, err := s.admitManualTask(t.Context(), "limit", now); err != nil || w == nil {
					t.Fatalf("manual claim: %v", err)
				}
				s.sched.limitInflight.Done()
				s.sched.inflight.Done()
			} else {
				c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
				if _, ok := s.consumeLimitRetry(t.Context(), "limit", wait, c, now.Add(2*time.Hour)); !ok {
					t.Fatal("automatic claim failed")
				}
			}
			restarted := newServiceOn(t, f, st)
			restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
			runs := scheduleRuns(restarted)
			if !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.ID == "retry_unknown_failed" && r.Manual == manual }) {
				t.Fatalf("wrong attempt mode: %+v", runs)
			}
		})
	}
}

func TestLimitRetryManualClaimRestoresUnknownAfterRestart(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	s.sched.mu.Lock()
	s.sched.ownerHeld = true
	s.sched.mu.Unlock()
	_, w, _, _, err := s.admitManualTask(t.Context(), "limit", now)
	if err != nil || w == nil {
		t.Fatalf("manual claim: %v", err)
	}
	// Simulate stopping after the claim and before the manual send.
	s.sched.limitInflight.Done()
	s.sched.inflight.Done()
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	if scheduleSendCount(f) != 0 {
		t.Fatal("manual claim resent")
	}
	runs := scheduleRuns(restarted)
	if !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.ID == "retry_unknown_failed" && r.Manual }) {
		t.Fatal("missing manual unknown result")
	}
}

func TestLimitRetryManualClaimBeforeDeadlineReportsPromptly(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	s.sched.mu.Lock()
	s.sched.ownerHeld = true
	s.sched.mu.Unlock()
	_, w, _, _, err := s.admitManualTask(t.Context(), "limit", now)
	if err != nil || w == nil {
		t.Fatalf("manual claim: %v", err)
	}
	// A scan by the live owner must not mistake its own claim for a crash.
	s.runLimitRetries(t.Context(), now.Add(time.Minute))
	f.mu.Lock()
	before := len(f.pushes)
	f.mu.Unlock()
	if before != 0 {
		t.Fatal("live manual claim was reported as unknown")
	}
	s.sched.limitInflight.Done()
	s.sched.inflight.Done()
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(time.Minute))
	f.mu.Lock()
	pushes := len(f.pushes)
	f.mu.Unlock()
	if pushes != 1 || scheduleSendCount(f) != 0 {
		t.Fatalf("before deadline: pushes=%d sends=%d", pushes, scheduleSendCount(f))
	}
}

func TestLimitRetryManualRecordedSendRestoresConversation(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	s.sched.mu.Lock()
	s.sched.ownerHeld = true
	s.sched.mu.Unlock()
	_, w, _, _, err := s.admitManualTask(t.Context(), "limit", now)
	if err != nil || w == nil {
		t.Fatalf("manual claim: %v", err)
	}
	runs := append(scheduleRuns(s), ScheduleRun{ID: "manual-result", RefID: "limit", Status: runOK, RetryOf: w.Failed.ID,
		Adopt: true, MachineID: "server", SessionID: "new-session", Launcher: "example"})
	if err := st.PutSetting(t.Context(), runsKey, runs); err != nil {
		t.Fatal(err)
	}
	s.sched.limitInflight.Done()
	s.sched.inflight.Done()
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(time.Minute))
	task, ok := taskByID(newServiceOn(t, f, st), "limit")
	if !ok || task.LimitRetry != nil || task.RunMachineID != "server" || task.RunSessionID != "new-session" || task.RunLauncher != "example" {
		t.Fatalf("conversation was not adopted during recovery: %+v", task)
	}
	if scheduleSendCount(f) != 0 {
		t.Fatal("recorded manual send was repeated")
	}
}

func TestLimitRetryManualSendBeforeRestartIsNotRepeated(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	s.sched.mu.Lock()
	s.sched.ownerHeld = true
	s.sched.mu.Unlock()
	_, w, _, _, err := s.admitManualTask(t.Context(), "limit", now)
	if err != nil || w == nil {
		t.Fatalf("manual claim: %v", err)
	}
	if err := s.call(t.Context(), "server", DeviceAssistantSend, map[string]any{"session_id": "s1", "text": "Example"}, nil); err != nil {
		t.Fatal(err)
	}
	s.sched.limitInflight.Done()
	s.sched.inflight.Done()
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	if scheduleSendCount(f) != 1 {
		t.Fatal("manual send was repeated")
	}
	if runs := scheduleRuns(restarted); !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.ID == "retry_unknown_failed" }) {
		t.Fatal("missing unknown outcome")
	}
}

func TestLimitRetryManualPreSendFailureRestoresDailyDue(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	if _, err := s.updateSettings(t.Context(), func(v *Settings) error { v.Timezone = "UTC"; return nil }); err != nil {
		t.Fatal(err)
	}
	day := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.UTC)
	previous := day.Add(-24 * time.Hour)
	sc, err := s.lockedSchedule(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tasks := slices.Clone(sc.tasks)
	w := *tasks[0].LimitRetry
	tasks[0].LimitRetry = nil
	err = s.saveLocked(sc, tasks)
	if err == nil {
		tasks = slices.Clone(sc.tasks)
		tasks[0].Repeat, tasks[0].Enabled = repeatDaily, true
		tasks[0].Time, tasks[0].Weekdays, tasks[0].At = "09:00", []int{int(day.Weekday())}, time.Time{}
		tasks[0].LastRun, tasks[0].LimitRetry = previous, &w
		err = s.saveLocked(sc, tasks)
	}
	sc.ownerHeld = true
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	st.failModelRead.Store(true)
	res := scheduleRequest(s, "POST", "tasks/limit/run", "")
	if res.Code == 200 {
		t.Fatal("manual run unexpectedly succeeded")
	}
	st.failModelRead.Store(false)
	restarted := newServiceOn(t, f, st)
	task, ok := taskByID(restarted, "limit")
	if !ok || !task.LastRun.Equal(previous) || task.LimitRetry == nil {
		t.Fatalf("manual failure changed daily marker: %+v", task)
	}
	loc := restarted.location(t.Context())
	due := time.Date(day.Year(), day.Month(), day.Day(), 9, 5, 0, 0, loc)
	restarted.runDue(t.Context(), due)
	restarted.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("due daily sends: %d", got)
	}
}

func TestLimitRetryManualReleaseDoesNotOverwriteNewerRunMarker(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	s.sched.mu.Lock()
	s.sched.ownerHeld = true
	s.sched.mu.Unlock()
	_, w, _, _, err := s.admitManualTask(t.Context(), "limit", now)
	if err != nil || w == nil {
		t.Fatalf("manual claim: %v", err)
	}
	sc, err := s.lockedSchedule(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tasks := slices.Clone(sc.tasks)
	tasks[0].LastRun = now.Add(time.Minute)
	tasks[0].Prompt = "User updated prompt"
	err = s.saveLocked(sc, tasks)
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !s.releaseUnsentLimitClaim("limit", *w) {
		t.Fatal("release failed")
	}
	s.sched.limitInflight.Done()
	s.sched.inflight.Done()
	task, ok := taskByID(newServiceOn(t, f, st), "limit")
	if !ok || !task.LastRun.Equal(now.Add(time.Minute)) || task.Prompt != "User updated prompt" || task.LimitRetry == nil {
		t.Fatalf("release overwrote newer changes: %+v", task)
	}
}

func TestLimitRetryManualPreSendFailuresRetainWait(t *testing.T) {
	for _, mode := range []string{"busy", "model"} {
		t.Run(mode, func(t *testing.T) {
			s, f, st, now := limitRetryFixture(t)
			s.sched.mu.Lock()
			s.sched.ownerHeld = true
			s.sched.mu.Unlock()
			if mode == "busy" {
				if !s.sched.begin("limit") {
					t.Fatal("cannot hold busy")
				}
			} else {
				st.failModelRead.Store(true)
			}
			res := scheduleRequest(s, "POST", "tasks/limit/run", "")
			if res.Code == 200 {
				t.Fatal("manual run unexpectedly succeeded")
			}
			if mode == "busy" {
				s.sched.end("limit")
			} else {
				st.failModelRead.Store(false)
			}
			restarted := newServiceOn(t, f, st)
			if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry == nil || task.LimitRetry.Claimed {
				t.Fatal("manual pre-send failure lost the retry")
			}
			restarted.runLimitRetries(t.Context(), now.Add(2*time.Hour))
			restarted.sched.inflight.Wait()
			if got := scheduleSendCount(f); got != 1 {
				t.Fatalf("retry sends after recovery: %d", got)
			}
		})
	}
}

func TestLimitRetryManualRunReplacesUnclaimedWait(t *testing.T) {
	s, f, st, _ := limitRetryFixture(t)
	s.sched.mu.Lock()
	s.sched.ownerHeld = true
	s.sched.mu.Unlock()
	w := scheduleRequest(s, "POST", "tasks/limit/run", "")
	if w.Code != 200 {
		t.Fatalf("manual run: HTTP %d: %s", w.Code, w.Body)
	}
	restarted := newServiceOn(t, f, st)
	if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry != nil {
		t.Fatal("manual run did not clear the pending wait")
	}
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("manual sends: %d", got)
	}
}

func TestLimitRetryManualRunRejectsClaimedWait(t *testing.T) {
	s, f, _, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("claim failed")
	}
	if res := scheduleRequest(s, "POST", "tasks/limit/run", ""); res.Code != 409 {
		t.Fatalf("claimed manual run: HTTP %d", res.Code)
	}
	if got := scheduleSendCount(f); got != 0 {
		t.Fatalf("claimed manual run sent %d messages", got)
	}
}

func TestLimitRetryModelReadFailureRearmsWithoutSend(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	st.failModelRead.Store(true)
	s.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	s.sched.inflight.Wait()
	if task, ok := taskByID(s, "limit"); !ok || task.LimitRetry == nil || task.LimitRetry.Claimed || scheduleSendCount(f) != 0 {
		t.Fatal("model read failure consumed or sent retry")
	}
	st.failModelRead.Store(false)
	s.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	s.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("sends after model recovery: %d", got)
	}
}

func TestLimitRetryTitleDoesNotBlockSentClaimOnShutdown(t *testing.T) {
	s, f, st, _ := limitRetryFixture(t)
	sc, err := s.lockedSchedule(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tasks := slices.Clone(sc.tasks)
	w := *tasks[0].LimitRetry
	tasks[0].LimitRetry = nil
	err = s.saveLocked(sc, tasks)
	if err == nil {
		tasks = slices.Clone(sc.tasks)
		tasks[0].SessionID, tasks[0].RunSessionID, tasks[0].LimitRetry = "", "", &w
		err = s.saveLocked(sc, tasks)
	}
	stop := make(chan struct{})
	sc.ownerHeld, sc.workerStop = true, stop
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	st.titleReadStarted, st.readStarted = make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	f.onAssistantSend = func(_ *fakeHub, _ map[string]any) {
		st.blockTitleRead.Store(true)
		s.titles.mu.Lock()
		s.titles.loaded = false
		s.titles.mu.Unlock()
		st.blockScheduleRead.Store(true)
		s.sched.mu.Lock()
		s.sched.loaded = false
		s.sched.mu.Unlock()
	}
	f.mu.Unlock()
	done := make(chan struct{})
	go func() { scheduleRequest(s, "POST", "tasks/limit/run", ""); close(done) }()
	awaitBusSignal(t, st.titleReadStarted)
	awaitBusSignal(t, st.readStarted)
	close(stop)
	awaitBusSignal(t, done)
	st.blockTitleRead.Store(false)
	st.blockScheduleRead.Store(false)
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("sent %d times", got)
	}
	restarted := newServiceOn(t, f, st)
	if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry == nil || !task.LimitRetry.Claimed {
		t.Fatal("shutdown lost the claimed send")
	}
}

func TestLimitRetrySessionErrorRequiresMatchingFinishedTurn(t *testing.T) {
	for _, scenario := range []string{"different_prompt", "starting", "stale_error_same_prompt", "other_turn_same_prompt"} {
		t.Run(scenario, func(t *testing.T) {
			s, f, _, now := limitRetryFixture(t)
			s.turnPoll = 10 * time.Millisecond
			text := "Example"
			if scenario == "different_prompt" {
				text = "Different"
			}
			f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now,
				TurnError: "stale error", Starting: scenario == "starting"})
			checked := make(chan struct{}, 2)
			f.mu.Lock()
			f.nodes = map[string]nodeAnswer{DeviceSessionItems: func(_ string, raw json.RawMessage) (any, error) {
				var params map[string]any
				_ = json.Unmarshal(raw, &params)
				if params["turns"] != nil {
					select {
					case checked <- struct{}{}:
					default:
					}
				}
				return map[string]any{"items": []proto.Item{{ID: "new", Role: "user", Kind: "text", Text: text}}}, nil
			}}
			f.mu.Unlock()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan bool, 1)
			go func() { ended, _ := s.waitTurnEndFrom(ctx, "server", "s1", "old", true, "Example"); result <- ended }()
			awaitBusSignal(t, checked)
			select {
			case ended := <-result:
				t.Fatalf("unconfirmed turn ended: %v", ended)
			case <-checked:
			case <-time.After(5 * time.Second):
				t.Fatal("watcher did not continue")
			}
			cancel()
			if <-result {
				t.Fatal("canceled watcher reported completion")
			}
		})
	}
}

func TestLimitRetryOtherTurnSamePromptDoesNotAttributeError(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	s.turnPoll = 10 * time.Millisecond
	task, _ := taskByID(s, "limit")
	task.limitRetry, task.limitBeforeKnown, task.limitBeforeID = true, true, "before"
	run := ScheduleRun{RefID: task.ID, At: now, Status: runOK, MachineID: "server", SessionID: "s1", Retried: true, RetryOf: "failed"}
	run.ID = s.recordRun(run)
	f.mu.Lock()
	f.items = []proto.Item{{ID: "other-prompt", Role: "user", Kind: "text", Text: "Example"}, {ID: "other-error", Role: "assistant", Kind: "text", Text: "API Error: 503 other turn"}}
	f.mu.Unlock()
	s.watchScheduledRun(run, task, Device{ID: "server"})
	runs := scheduleRuns(newServiceOn(t, f, st))
	for _, r := range runs {
		if r.ID == run.ID {
			if r.Status != runOK || r.Error != "" {
				t.Fatalf("attributed unrelated turn: %+v", r)
			}
			f.mu.Lock()
			pushes := len(f.pushes)
			f.mu.Unlock()
			if pushes != 0 {
				t.Fatal("unverified turn sent a notification")
			}
			return
		}
	}
	t.Fatal("run not found")
}

func TestLimitRetryWatcherTimeoutDoesNotReportUnconfirmedTurn(t *testing.T) {
	for _, state := range []string{"stopped", "offline", "working", "needs_you"} {
		t.Run(state, func(t *testing.T) {
			s, f, st, now := limitRetryFixture(t)
			s.turnPoll = 5 * time.Millisecond
			s.sched.limitWatchTimeout = 30 * time.Millisecond
			task, _ := taskByID(s, "limit")
			task.limitRetry, task.limitBeforeKnown, task.limitBeforeID = true, true, "before"
			run := ScheduleRun{RefID: task.ID, At: now, Status: runOK, MachineID: "server", SessionID: "s1", Retried: true, RetryOf: "failed"}
			run.ID = s.recordRun(run)
			switch state {
			case "offline":
				f.setSessions("server")
			case "working":
				f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
			case "needs_you":
				f.setSessions("server", proto.Session{SessionID: "s1", State: proto.StateNeedsYou})
			}
			s.watchScheduledRun(run, task, Device{ID: "server"})
			runs := scheduleRuns(newServiceOn(t, f, st))
			var result ScheduleRun
			for _, r := range runs {
				if r.ID == run.ID {
					result = r
				}
			}
			f.mu.Lock()
			pushes := len(f.pushes)
			f.mu.Unlock()
			if result.Status != runOK || pushes != 0 || scheduleSendCount(f) != 0 {
				t.Fatalf("unconfirmed watcher changed result: %+v, pushes %d", result, pushes)
			}
		})
	}
}

func TestLimitRetryWatcherStopsWithManager(t *testing.T) {
	s, f, _, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	task.limitRetry = true
	run := ScheduleRun{RefID: task.ID, At: now, Status: runOK, SessionID: "s1", MachineID: "server", Retried: true, RetryOf: "failed"}
	run.ID = s.recordRun(run)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.sched.mu.Lock()
	s.sched.limitArmCtx = ctx
	s.sched.mu.Unlock()
	s.watchScheduledRun(run, task, Device{ID: "server"})
	f.mu.Lock()
	pushes := len(f.pushes)
	f.mu.Unlock()
	if pushes != 0 {
		t.Fatalf("stopping manager sent %d notifications", pushes)
	}
}

func TestLimitRetryWatcherDoesNotTreatPromptAsCompletedTurn(t *testing.T) {
	s, f, _, _ := limitRetryFixture(t)
	s.turnPoll = 10 * time.Millisecond
	checked := make(chan struct{}, 2)
	f.mu.Lock()
	f.nodes = map[string]nodeAnswer{DeviceSessionItems: func(_ string, raw json.RawMessage) (any, error) {
		var params map[string]any
		_ = json.Unmarshal(raw, &params)
		if params["turns"] != nil {
			select {
			case checked <- struct{}{}:
			default:
			}
		}
		return map[string]any{"items": []proto.Item{{ID: "prompt", Role: "user", Kind: "text", Text: "Example"}}}, nil
	}}
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan bool, 1)
	go func() { ended, _ := s.waitTurnEndFrom(ctx, "server", "s1", "before", true, "Example"); result <- ended }()
	awaitBusSignal(t, checked)
	select {
	case ended := <-result:
		t.Fatalf("prompt ended turn: %v", ended)
	case <-checked:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not continue polling")
	}
	cancel()
	if ended := <-result; ended {
		t.Fatal("canceled watcher reported completion")
	}
}

func TestLimitRetryWatcherSeesFastFailureDuringRecordOutage(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	s.turnPoll = 10 * time.Millisecond
	st.failReadStarted = make(chan struct{})
	observed := make(chan struct{}, 1)
	f.mu.Lock()
	f.nodes = map[string]nodeAnswer{DeviceSessionItems: func(_ string, raw json.RawMessage) (any, error) {
		var params map[string]any
		_ = json.Unmarshal(raw, &params)
		if params["turns"] != nil {
			select {
			case observed <- struct{}{}:
			default:
			}
		}
		return map[string]any{"items": slices.Clone(f.items)}, nil
	}}
	f.items = []proto.Item{{ID: "before", Role: "assistant", Kind: "text", Text: "Earlier"}}
	f.onAssistantSend = func(_ *fakeHub, _ map[string]any) {
		f.items = []proto.Item{{ID: "prompt", Role: "user", Kind: "text", Text: "Example"}, {ID: "after", Role: "assistant", Kind: "text", Text: "API Error: 503 upstream unavailable"}}
		for i := range f.sessions["server"] {
			f.sessions["server"][i].Status, f.sessions["server"][i].State = proto.StatusDone, proto.StateIdle
			f.sessions["server"][i].TurnError = "API Error: 503 upstream unavailable"
		}
		st.failScheduleRead.Store(true)
		s.sched.mu.Lock()
		s.sched.loaded = false
		s.sched.mu.Unlock()
	}
	f.mu.Unlock()
	s.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	awaitBusSignal(t, st.failReadStarted)
	st.failScheduleRead.Store(false)
	awaitBusSignal(t, observed)
	s.sched.inflight.Wait()
	runs := scheduleRuns(newServiceOn(t, f, st))
	if !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.RetryOf == "failed" && r.Status == runOK }) {
		t.Fatalf("unverified fast failure altered the run: %+v", runs)
	}
	f.mu.Lock()
	pushes := len(f.pushes)
	f.mu.Unlock()
	if pushes != 0 {
		t.Fatal("unverified fast failure sent a notification")
	}
}

func TestLimitRetryObservedWorkingFailureIsReported(t *testing.T) {
	for _, tc := range []struct{ mode, kind, text string }{
		{"automatic", "limit", limitText}, {"manual", "limit", limitText},
		{"automatic", "ordinary", "API Error: 503 upstream unavailable"}, {"manual", "ordinary", "API Error: 503 upstream unavailable"},
	} {
		t.Run(tc.mode+"/"+tc.kind, func(t *testing.T) {
			mode := tc.mode
			s, f, st, now := limitRetryFixture(t)
			s.turnPoll = 10 * time.Millisecond
			s.runRetry = 10 * time.Millisecond
			s.sched.limitWait = func(time.Time, time.Time) time.Duration { return time.Hour }
			sc, err := s.lockedSchedule(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			tasks := slices.Clone(sc.tasks)
			tasks[0].LimitRetry = nil
			err = s.saveLocked(sc, tasks)
			sc.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			task, _ := taskByID(s, "limit")
			task.limitRetry, task.manualLimitRetry, task.limitBeforeKnown, task.limitBeforeID = mode == "automatic", mode == "manual", true, "before"
			run := ScheduleRun{RefID: task.ID, At: now, Status: runOK, MachineID: "server", SessionID: "s1", Retried: mode == "automatic", Manual: mode == "manual", RetryOf: "failed", of: task.LastRun}
			run.ID = s.recordRun(run)
			seen, notified, done := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
			s.hub = &limitNotifyHub{fakeHub: f, onNotify: func() {
				select {
				case notified <- struct{}{}:
				default:
				}
			}}
			f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: now})
			f.mu.Lock()
			f.nodes = map[string]nodeAnswer{DeviceSessionGet: func(machine string, _ json.RawMessage) (any, error) {
				for _, c := range f.sessions[machine] {
					if c.SessionID == "s1" {
						if c.Status == proto.StatusWorking {
							select {
							case seen <- struct{}{}:
							default:
							}
						}
						return map[string]any{"session": c, "ok": true}, nil
					}
				}
				return map[string]any{"ok": false}, nil
			}}
			f.mu.Unlock()
			go func() { s.watchScheduledRun(run, task, Device{ID: "server"}); close(done) }()
			awaitBusSignal(t, seen)
			f.mu.Lock()
			f.items = []proto.Item{{ID: "prompt", Role: "user", Kind: "text", Text: "Example"}, {ID: "failure", Role: "assistant", Kind: "text", Text: tc.text}}
			for i := range f.sessions["server"] {
				f.sessions["server"][i].Status, f.sessions["server"][i].State, f.sessions["server"][i].TurnError = proto.StatusDone, proto.StateIdle, tc.text
			}
			f.mu.Unlock()
			select {
			case <-notified:
			case <-done:
				t.Fatal("observed transcript error was not notified")
			case <-time.After(5 * time.Second):
				t.Fatal("watcher did not report error")
			}
			awaitBusSignal(t, done)
			restarted := newServiceOn(t, f, st)
			runs := scheduleRuns(restarted)
			if !slices.ContainsFunc(runs, func(r ScheduleRun) bool {
				return r.ID == run.ID && r.Status == runFailed && strings.Contains(r.Error, tc.text)
			}) {
				t.Fatalf("error not recorded: %+v", runs)
			}
			current, _ := taskByID(restarted, "limit")
			if current.LimitRetry != nil {
				t.Fatal("replacement failure armed another automatic retry")
			}
			if got := scheduleSendCount(f); got != 0 {
				t.Fatalf("watcher resent %d messages", got)
			}
		})
	}
}

func TestLimitRetryWatcherSurvivesInitialRecordFailure(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	s.turnPoll = 10 * time.Millisecond
	seenWorking := make(chan struct{}, 1)
	observed := make(chan struct{}, 1)
	f.mu.Lock()
	f.items = []proto.Item{{ID: "prompt", Role: "user", Kind: "text", Text: "Example"}}
	f.nodes = map[string]nodeAnswer{DeviceSessionGet: func(machine string, _ json.RawMessage) (any, error) {
		for _, c := range f.sessions[machine] {
			if c.SessionID == "s1" {
				if c.Status == proto.StatusWorking {
					select {
					case seenWorking <- struct{}{}:
					default:
					}
				}
				return map[string]any{"session": c, "ok": true}, nil
			}
		}
		return map[string]any{"ok": false}, nil
	}, DeviceSessionItems: func(_ string, raw json.RawMessage) (any, error) {
		var params map[string]any
		_ = json.Unmarshal(raw, &params)
		if params["turns"] != nil {
			select {
			case observed <- struct{}{}:
			default:
			}
		}
		return map[string]any{"items": slices.Clone(f.items)}, nil
	}}
	f.onAssistantSend = func(_ *fakeHub, _ map[string]any) {
		for i := range f.sessions["server"] {
			f.sessions["server"][i].Status, f.sessions["server"][i].State = proto.StatusWorking, proto.StateWorking
		}
		st.failScheduleRead.Store(true)
		s.sched.mu.Lock()
		s.sched.loaded = false
		s.sched.mu.Unlock()
	}
	f.mu.Unlock()
	s.runLimitRetries(t.Context(), now.Add(2*time.Hour))
	awaitBusSignal(t, seenWorking)
	st.failScheduleRead.Store(false)
	f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now, TurnError: "API Error: 503 upstream unavailable"})
	awaitBusSignal(t, observed)
	s.sched.inflight.Wait()
	runs := scheduleRuns(newServiceOn(t, f, st))
	if !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.RetryOf == "failed" && r.Status == runOK }) {
		t.Fatalf("unverified session error altered the run: %+v", runs)
	}
	f.mu.Lock()
	pushes := len(f.pushes)
	f.mu.Unlock()
	if pushes != 0 {
		t.Fatal("unverified session error sent a notification")
	}
}

func TestLimitRetrySentRunRecordingStopsWithWorker(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	st.readStarted = make(chan struct{})
	f.mu.Lock()
	f.onAssistantSend = func(_ *fakeHub, _ map[string]any) {
		st.blockScheduleRead.Store(true)
		s.sched.mu.Lock()
		s.sched.loaded = false
		s.sched.mu.Unlock()
	}
	f.mu.Unlock()
	stop := make(chan struct{})
	s.scanLimitRetries(t.Context(), now.Add(2*time.Hour), nil, stop)
	awaitBusSignal(t, st.readStarted)
	close(stop)
	done := make(chan struct{})
	go func() { s.sched.limitInflight.Wait(); close(done) }()
	awaitBusSignal(t, done)
	st.blockScheduleRead.Store(false)
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("sent %d times", got)
	}
	restarted := newServiceOn(t, f, st)
	if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry == nil || !task.LimitRetry.Claimed {
		t.Fatal("stopped recording lost its durable claim")
	}
}

func TestLimitRetryWorkerStopsDuringStorageFailure(t *testing.T) {
	for _, mode := range []string{"release", "finish"} {
		t.Run(mode, func(t *testing.T) {
			s, f, st, now := limitRetryFixture(t)
			task, _ := taskByID(s, "limit")
			w := *task.LimitRetry
			c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
			cur, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour))
			if !ok {
				t.Fatal("claim failed")
			}
			cur.MachineID, cur.SessionID, cur.limitRetry, cur.limitRetryRunID = "server", "s1", true, w.Failed.ID
			attempted := make(chan struct{})
			if mode == "release" {
				f.mu.Lock()
				f.devices = []Device{}
				f.mu.Unlock()
				st.failWrites(tasksKey)
				st.mu.Lock()
				st.beforeTaskCompare = func() { close(attempted) }
				st.mu.Unlock()
			} else {
				f.mu.Lock()
				f.onAssistantSend = func(_ *fakeHub, _ map[string]any) { st.failWrites(runsKey) }
				f.mu.Unlock()
				st.mu.Lock()
				st.beforeRunsCompare = func() { close(attempted) }
				st.mu.Unlock()
			}
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() { s.dispatchLimitRetry(cur, w, now.Add(2*time.Hour), nil, stop); close(done) }()
			awaitBusSignal(t, attempted)
			close(stop)
			awaitBusSignal(t, done)
			st.failWrites("")
			restarted := newServiceOn(t, f, st)
			if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry == nil || !task.LimitRetry.Claimed {
				t.Fatal("shutdown lost the durable claim")
			}
		})
	}
}

func TestLimitRetryNodeUnavailableAfterClaimRearms(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	cur, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour))
	if !ok {
		t.Fatal("claim failed")
	}
	cur.MachineID, cur.SessionID, cur.limitRetry, cur.limitRetryRunID = "server", "s1", true, w.Failed.ID
	f.mu.Lock()
	f.devices = []Device{}
	f.mu.Unlock()
	s.dispatchLimitRetry(cur, w, now.Add(2*time.Hour), nil, nil)
	restarted := newServiceOn(t, f, st)
	if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry == nil || task.LimitRetry.Claimed {
		t.Fatal("unsent retry was not rearmed")
	}
	f.mu.Lock()
	f.devices = nil
	f.mu.Unlock()
	restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	restarted.sched.inflight.Wait()
	if got := scheduleSendCount(f); got != 1 {
		t.Fatalf("recovered node sends: %d", got)
	}
}

func TestLimitRetryNonOwnerFlushesObservedFailure(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	r := ScheduleRun{RefID: "limit", At: now, Status: runOK, SessionID: "s1", Retried: true, RetryOf: "failed"}
	r.ID = s.recordRun(r)
	if r.ID == "" {
		t.Fatal("run was not recorded")
	}
	st.failWrites(runsKey)
	s.updateRun(r, runFailed, "model failed")
	st.failWrites("")
	s.sched.mu.Lock()
	s.sched.ownerHeld = false
	s.sched.mu.Unlock()
	other := newServiceOn(t, f, st)
	other.sched.mu.Lock()
	other.sched.ownerHeld = true
	other.sched.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	flushed := make(chan struct{})
	st.mu.Lock()
	st.afterRunsWrite = func() { close(flushed); cancel() }
	st.mu.Unlock()
	done := make(chan struct{})
	go func() { s.scheduleLoop(ctx); close(done) }()
	awaitBusSignal(t, flushed)
	awaitBusSignal(t, done)
	var runs []ScheduleRun
	if _, err := st.Setting(t.Context(), runsKey, &runs); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(runs, func(got ScheduleRun) bool {
		return got.ID == r.ID && got.Status == runFailed && got.Error == "model failed"
	}) {
		t.Fatalf("non-owner did not persist observed failure: %+v", runs)
	}
	if got := scheduleSendCount(f); got != 0 {
		t.Fatalf("non-owner sent %d retries", got)
	}
}

func TestLimitRetryFailedHistoryWriteSurvivesAnotherRun(t *testing.T) {
	s, _, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("claim failed")
	}
	st.failWrites(runsKey)
	id := s.recordRun(ScheduleRun{RefID: task.ID, At: now, Status: runOK, SessionID: "s1", Retried: true, RetryOf: w.Failed.ID})
	st.failWrites("")
	s.sched.mu.Lock()
	s.sched.loaded = false // a cross-manager refresh must also preserve the unsaved result
	s.sched.mu.Unlock()
	s.recordRun(ScheduleRun{RefID: "other", At: now.Add(time.Second), Status: runOK})
	if !s.finishLimitClaim(task.ID, w) {
		t.Fatal("claim did not finish after storage recovered")
	}
	var runs []ScheduleRun
	if _, err := st.Setting(t.Context(), runsKey, &runs); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.ID == id && r.RetryOf == w.Failed.ID }) {
		t.Fatal("unsaved retry result was lost")
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
	if len(runs) == 0 || runs[len(runs)-1].ID != "retry_unknown_failed" || runs[len(runs)-1].Error == "" || runs[len(runs)-1].Manual {
		t.Fatalf("missing automatic unknown result: %+v", runs)
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

func TestLimitRetryOwnerLostDuringPreSendReadDoesNotSend(t *testing.T) {
	s, f, st, now := limitRetryFixture(t)
	task, _ := taskByID(s, "limit")
	w := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	cur, ok := s.consumeLimitRetry(t.Context(), task.ID, w, c, now.Add(2*time.Hour))
	if !ok {
		t.Fatal("claim failed")
	}
	ownerDone := make(chan struct{})
	cur.MachineID, cur.SessionID, cur.limitRetry, cur.limitRetryRunID = "server", "s1", true, w.Failed.ID
	cur.limitOwnerDone = ownerDone
	reading, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.mu.Lock()
	f.nodes = map[string]nodeAnswer{DeviceSessionItems: func(_ string, _ json.RawMessage) (any, error) {
		once.Do(func() { close(reading); <-resume })
		return map[string]any{"items": []proto.Item{}}, nil
	}}
	f.mu.Unlock()
	done := make(chan struct{})
	go func() { s.dispatchLimitRetry(cur, w, now.Add(2*time.Hour), ownerDone, nil); close(done) }()
	awaitBusSignal(t, reading)
	close(ownerDone)
	close(resume)
	awaitBusSignal(t, done)
	if got := scheduleSendCount(f); got != 0 {
		t.Fatalf("sent after owner loss: %d", got)
	}
	restarted := newServiceOn(t, f, st)
	if task, ok := taskByID(restarted, "limit"); !ok || task.LimitRetry == nil || !task.LimitRetry.Claimed {
		t.Fatal("owner loss lost the durable claim")
	}
	restarted.runLimitRetries(t.Context(), now.Add(3*time.Hour))
	if got := scheduleSendCount(f); got != 0 {
		t.Fatalf("recovered owner resent: %d", got)
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
	s.dispatchLimitRetry(cur, w, now.Add(2*time.Hour), done, nil)
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

func TestLimitRetryClaimTokenFencesStaleOwnerAfterReadbackLoss(t *testing.T) {
	first, f, st, now := limitRetryFixture(t)
	task, _ := taskByID(first, "limit")
	old := *task.LimitRetry
	c := proto.Session{SessionID: "s1", Status: proto.StatusDone, State: proto.StateIdle, UpdatedAt: now}
	if _, ok := first.consumeLimitRetry(t.Context(), task.ID, old, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("first claim failed")
	}
	committed, resume := make(chan struct{}), make(chan struct{})
	st.mu.Lock()
	st.afterTaskWrite = func() {
		close(committed)
		<-resume
		st.failNextTaskRead.Store(true)
	}
	st.mu.Unlock()
	released := make(chan bool, 1)
	go func() { released <- first.releaseUnsentLimitClaim(task.ID, old) }()
	awaitBusSignal(t, committed) // the write committed, but its readback has not returned
	close(resume)
	if <-released {
		t.Fatal("lost readback unexpectedly succeeded")
	}
	// The database lock is now lost, while the old owner's loss signal is still pending.
	second := newServiceOn(t, f, st)
	task, ok := taskByID(second, "limit")
	if !ok || task.LimitRetry == nil || task.LimitRetry.Claimed {
		t.Fatal("first release was not persisted")
	}
	next := *task.LimitRetry
	if next.ClaimToken == "" || next.ClaimToken == old.ClaimToken {
		t.Fatal("release reused the previous claim token")
	}
	if _, ok := second.consumeLimitRetry(t.Context(), task.ID, next, c, now.Add(2*time.Hour)); !ok {
		t.Fatal("second claim failed")
	}
	if err := second.call(t.Context(), "server", DeviceAssistantSend, map[string]any{"session_id": "s1", "text": "Example"}, nil); err != nil {
		t.Fatal(err)
	}
	// The old owner continues before it notices its lock was lost.
	if !first.releaseUnsentLimitClaim(task.ID, old) {
		t.Fatal("stale release did not stop")
	}
	if !first.finishLimitClaim(task.ID, old) {
		t.Fatal("stale finish did not stop")
	}
	first.recoverLimitClaim(t.Context(), task.ID, old, now.Add(3*time.Hour), "assistant-schedule.retry_restart_unknown")
	current, ok := taskByID(newServiceOn(t, f, st), task.ID)
	if !ok || current.LimitRetry == nil || !current.LimitRetry.Claimed || current.LimitRetry.ClaimToken != next.ClaimToken {
		t.Fatalf("stale owner changed successor claim: %+v", current)
	}
	f.mu.Lock()
	pushes := len(f.pushes)
	f.mu.Unlock()
	if pushes != 0 {
		t.Fatal("stale owner reported successor claim")
	}
	restarted := newServiceOn(t, f, st)
	restarted.runLimitRetries(t.Context(), now.Add(4*time.Hour))
	if scheduleSendCount(f) != 1 {
		t.Fatal("successor send was repeated after restart")
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
	firstRestart := newServiceOn(t, f, st)
	firstRestart.hub = &limitNotifyHub{fakeHub: f, onNotify: func() { st.failWrites(tasksKey) }}
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

func TestScheduleOwnerAcquiredAtCancellationDoesNotAdmit(t *testing.T) {
	s, f, base, now := limitRetryFixture(t)
	sc, err := s.lockedSchedule(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	tasks := slices.Clone(sc.tasks)
	tasks[0].LimitRetry = nil
	tasks[0].Enabled, tasks[0].At = true, now.Add(-time.Minute)
	before := tasks[0].LastRun
	err = s.saveLocked(sc, tasks)
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	st := &limitOwnedStore{limitTestStore: base, acquired: make(chan struct{}), released: make(chan struct{}), onAcquire: cancel}
	s.store = func() Store { return st }
	s.scheduleLoop(ctx)
	if got := scheduleSendCount(f); got != 0 {
		t.Fatalf("sent after cancellation: %d", got)
	}
	task, ok := taskByID(newServiceOn(t, f, base), "limit")
	if !ok || !task.Enabled || !task.LastRun.Equal(before) {
		t.Fatalf("task admitted after cancellation: %+v", task)
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
