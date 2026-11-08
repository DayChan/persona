package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// scheduleWriteStore rejects just one setting, without disrupting model settings or reads.
type scheduleWriteStore struct {
	Store
	gate sync.Mutex
	fail string
}

func (st *scheduleWriteStore) failWrites(key string) {
	st.gate.Lock()
	defer st.gate.Unlock()
	st.fail = key
}

func (st *scheduleWriteStore) PutSetting(ctx context.Context, key string, value any) error {
	st.gate.Lock()
	fail := st.fail == key
	st.gate.Unlock()
	if fail {
		return errors.New("injected schedule write failure")
	}
	return st.Store.PutSetting(ctx, key, value)
}

func scheduleState(s *Service) ([]Task, []ScheduleRun) {
	sc := s.loadSchedule(context.Background())
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return append([]Task{}, sc.tasks...), append([]ScheduleRun{}, sc.runs...)
}

func scheduleStorageFixture(t *testing.T) (*Service, *fakeHub, *scheduleWriteStore) {
	t.Helper()
	return scheduleStorageOn(t, &memStore{})
}

func scheduleStorageOn(t *testing.T, store Store) (*Service, *fakeHub, *scheduleWriteStore) {
	t.Helper()
	st := &scheduleWriteStore{Store: store}
	at := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	tasks := []Task{
		{ID: "once", Title: "Once", Prompt: "Example", Repeat: repeatOnce, At: at, Enabled: true,
			RunMachineID: "server", RunSessionID: "kept", RunLauncher: "claude"},
		{ID: "daily", Title: "Daily", Prompt: "Example", Repeat: repeatDaily, Time: "09:00", Enabled: true,
			Weekdays: []int{1, 3}, Launcher: "claude", PermissionMode: "default", LastStatus: runOK, LastSessionID: "previous"},
		{ID: "interval", Title: "Interval", Prompt: "Example", Repeat: repeatInterval, At: at, EveryMinutes: 30, Enabled: true},
	}
	if err := st.PutSetting(t.Context(), tasksKey, tasks); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSetting(t.Context(), runsKey, []ScheduleRun{{ID: "existing", RefID: "daily", SessionID: "previous", Status: runOK}}); err != nil {
		t.Fatal(err)
	}
	f := readyHub()
	f.known["kept"] = true
	s := newServiceOn(t, f, st)
	s.loadSchedule(t.Context())
	return s, f, st
}

func scheduleRequest(s *Service, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/assistant/"+path, strings.NewReader(body)))
	return w
}

func assertScheduleStored(t *testing.T, s *Service, st Store) {
	t.Helper()
	live, _ := scheduleState(s)
	var stored []Task
	if _, err := st.Setting(t.Context(), tasksKey, &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live, stored) {
		t.Fatalf("live tasks differ from storage:\nlive: %+v\nstored: %+v", live, stored)
	}
	restarted := newServiceOn(t, newFakeHub(), st)
	reloaded, _ := scheduleState(restarted)
	if !reflect.DeepEqual(live, reloaded) {
		t.Fatalf("tasks changed after restart:\nlive: %+v\nreloaded: %+v", live, reloaded)
	}
}

func TestScheduleMutationWriteFailure(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "tasks", `{"title":"New","prompt":"Example","repeat":"once","at":"2099-01-01T09:00:00Z","enabled":true}`},
		{"PUT", "tasks/daily", `{"title":"Edited","prompt":"Changed","repeat":"daily","time":"10:00","weekdays":[2,4],"enabled":false}`},
		{"DELETE", "tasks/once", ""},
		{"POST", "tasks/once/fresh", ""},
		{"PUT", "routines", `{"routines":[{"id":"daily","title":"Edited","prompt":"Changed","time":"10:00","enabled":false}]}`},
		{"DELETE", "followups/once", ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			s, f, st := scheduleStorageFixture(t)
			before, history := scheduleState(s)
			events := f.eventTypes()
			st.failWrites(tasksKey)
			w := scheduleRequest(s, tc.method, tc.path, tc.body)
			if w.Code != http.StatusServiceUnavailable || w.Body.Len() == 0 {
				t.Fatalf("failed save: HTTP %d: %s", w.Code, w.Body)
			}
			after, runs := scheduleState(s)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, runs) || f.eventTypes() != events {
				t.Fatal("failed mutation changed tasks, history or broadcasts")
			}
			assertScheduleStored(t, s, st)
			st.failWrites("")
			if w := scheduleRequest(s, tc.method, tc.path, tc.body); w.Code != http.StatusOK {
				t.Fatalf("retry: HTTP %d: %s", w.Code, w.Body)
			}
			assertScheduleStored(t, s, st)
			if _, runs := scheduleState(s); !reflect.DeepEqual(history, runs) {
				t.Fatal("successful edit changed existing history")
			}
		})
	}
}

func TestScheduleToolWriteFailure(t *testing.T) {
	for _, tc := range []struct{ name, session, args string }{
		{"schedule_followup", "", `{"at":"30m","title":"New","prompt":"New task"}`},
		{"schedule_followup", "previous", `{"at":"30m","title":"Daily","prompt":"Example"}`},
		{"schedule_followup", "once-run", `{"at":"30m","title":"Once","prompt":"Example"}`},
		{"schedule_routine", "", `{"title":"New","prompt":"Example","time":"10:00"}`},
		{"schedule_routine", "", `{"id":"daily","title":"Edited","time":"10:00"}`},
		{"cancel_followup", "", `{"id":"once"}`},
		{"delete_routine", "", `{"id":"daily"}`},
	} {
		t.Run(tc.name+tc.args, func(t *testing.T) {
			s, f, st := scheduleStorageFixture(t)
			if tc.session == "once-run" {
				s.sched.mu.Lock()
				s.sched.tasks[s.sched.indexLocked("once")].Enabled = false
				s.sched.mu.Unlock()
				s.recordRun(ScheduleRun{RefID: "once", SessionID: tc.session, Status: runOK, At: time.Now().UTC()})
			}
			before, history := scheduleState(s)
			events := f.eventTypes()
			st.failWrites(tasksKey)
			out, err := s.runTool(t.Context(), "server", tc.session, tc.name, json.RawMessage(tc.args))
			if !errors.Is(err, errScheduleSave) || out != "" {
				t.Fatalf("failed tool save returned %q, %v", out, err)
			}
			after, runs := scheduleState(s)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, runs) || f.eventTypes() != events {
				t.Fatal("failed tool changed tasks, history or broadcasts")
			}
			assertScheduleStored(t, s, st)
			st.failWrites("")
			if _, err := s.runTool(t.Context(), "server", tc.session, tc.name, json.RawMessage(tc.args)); err != nil {
				t.Fatal(err)
			}
			assertScheduleStored(t, s, st)
		})
	}
}

func scheduleSendCount(f *fakeHub) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call.Method == DeviceAssistantSend {
			n++
		}
	}
	return n
}

func TestScheduleDueAdmissionWriteFailure(t *testing.T) {
	now := time.Date(2026, 11, 2, 9, 1, 0, 0, time.UTC)
	for _, repeat := range []string{repeatOnce, repeatInterval, repeatDaily, "stale daily"} {
		t.Run(repeat, func(t *testing.T) {
			s, f, st := scheduleStorageFixture(t)
			if _, err := s.updateSettings(t.Context(), func(v *Settings) error { v.Timezone = "UTC"; return nil }); err != nil {
				t.Fatal(err)
			}
			task := Task{ID: "due", Title: "Due", Prompt: "Example", Repeat: repeat, Enabled: true,
				At: now.Add(-time.Minute), EveryMinutes: 30, Time: "09:00", LastRun: now.Add(-24 * time.Hour)}
			if repeat == "stale daily" {
				task.Repeat, task.Time = repeatDaily, "08:00"
			}
			// Include a completed task that cleanup would remove in the same batch.
			tasks := []Task{task, {ID: "done", Repeat: repeatOnce, LastRun: now.Add(-48 * time.Hour)}}
			if err := st.PutSetting(t.Context(), tasksKey, tasks); err != nil {
				t.Fatal(err)
			}
			setTasks(s, tasks...)
			before, history := scheduleState(s)
			events := f.eventTypes()
			st.failWrites(tasksKey)
			s.runDue(t.Context(), now)
			s.sched.inflight.Wait()
			after, runs := scheduleState(s)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, runs) || scheduleSendCount(f) != 0 || f.eventTypes() != events {
				t.Fatalf("failed admission consumed or dispatched tasks: %+v, %+v", after, runs)
			}
			assertScheduleStored(t, s, st)
			st.failWrites("")
			s.runDue(t.Context(), now)
			s.sched.inflight.Wait()
			wantSends := 1
			if repeat == "stale daily" {
				wantSends = 0
			}
			if scheduleSendCount(f) != wantSends {
				t.Fatalf("recovered admission: sent %d, want %d", scheduleSendCount(f), wantSends)
			}
			assertScheduleStored(t, s, st)
			restarted := newServiceOn(t, readyHub(), st)
			restarted.runDue(t.Context(), now)
			restarted.sched.inflight.Wait()
			_, reloadedRuns := scheduleState(restarted)
			if len(reloadedRuns) != len(history)+1 {
				t.Fatalf("restart repeated a consumed run: %+v", reloadedRuns)
			}
		})
	}
}

func TestScheduleManualAdmissionWriteFailure(t *testing.T) {
	for _, path := range []string{"tasks/once/run", "followups/once/run", "tasks/daily/run", "routines/daily/run", "tasks/interval/run"} {
		t.Run(path, func(t *testing.T) {
			s, f, st := scheduleStorageFixture(t)
			before, history := scheduleState(s)
			events := f.eventTypes()
			st.failWrites(tasksKey)
			if w := scheduleRequest(s, "POST", path, ""); w.Code != http.StatusServiceUnavailable {
				t.Fatalf("failed admission: HTTP %d: %s", w.Code, w.Body)
			}
			after, runs := scheduleState(s)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(history, runs) || scheduleSendCount(f) != 0 || f.eventTypes() != events {
				t.Fatal("failed manual admission changed state or dispatched work")
			}
			assertScheduleStored(t, s, st)
			st.failWrites("")
			if w := scheduleRequest(s, "POST", path, ""); w.Code != http.StatusOK {
				t.Fatalf("retry: HTTP %d: %s", w.Code, w.Body)
			}
			if scheduleSendCount(f) != 1 {
				t.Fatalf("retry sends: %d", scheduleSendCount(f))
			}
			assertScheduleStored(t, s, st)
		})
	}
}

func TestScheduleRuntimeWriteFailureRetainsOutcomes(t *testing.T) {
	for _, key := range []string{tasksKey, runsKey} {
		t.Run(key, func(t *testing.T) {
			s, _, st := scheduleStorageFixture(t)
			now := time.Now().UTC()
			st.failWrites(key)
			s.adoptConversation("daily", "server", "new-session", "claude")
			r := ScheduleRun{RefID: "daily", Kind: "routine", SessionID: "new-session", Status: runOK, At: now, of: now}
			r.ID = s.recordRun(r)
			if r.ID == "" {
				t.Fatal("an executed run lost its ID, so its watcher cannot update it")
			}
			s.updateRun(r, runFailed, "Example runtime failure")
			task, _ := taskByID(s, "daily")
			_, runs := scheduleState(s)
			if task.LastStatus != runFailed || task.RunSessionID != "new-session" || runs[len(runs)-1].Status != runFailed {
				t.Fatalf("runtime result or ownership rolled back: %+v, %+v", task, runs)
			}
			// A rejected user mutation must preserve even runtime state not yet saved, including history.
			st.failWrites(tasksKey)
			if ok, err := s.deleteTask(t.Context(), "daily"); ok || !errors.Is(err, errScheduleSave) {
				t.Fatalf("delete: %v, %v", ok, err)
			}
			if task, _ := taskByID(s, "daily"); task.LastStatus != runFailed || task.RunSessionID != "new-session" {
				t.Fatalf("rejected edit erased a real result: %+v", task)
			}
			st.failWrites("")
			s.updateRun(r, runFailed, "Example runtime failure")
			assertScheduleStored(t, s, st)
			restarted := newServiceOn(t, newFakeHub(), st)
			_, reloaded := scheduleState(restarted)
			if len(reloaded) != 2 || reloaded[0].ID != "existing" || reloaded[1].ID != r.ID || reloaded[1].Status != runFailed {
				t.Fatalf("runtime history did not recover: %+v", reloaded)
			}
		})
	}
}

func TestScheduleSentRunSurvivesOutcomeWriteFailure(t *testing.T) {
	for _, key := range []string{tasksKey, runsKey} {
		t.Run(key, func(t *testing.T) {
			s, f, st := scheduleStorageFixture(t)
			if _, _, err := s.forgetConversation(t.Context(), "once"); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.onAssistantSend = func(_ *fakeHub, _ map[string]any) { st.failWrites(key) }
			f.mu.Unlock()
			w := scheduleRequest(s, "POST", "tasks/once/run", "")
			if w.Code != http.StatusOK || scheduleSendCount(f) != 1 {
				t.Fatalf("work was sent, but its real outcome was hidden: HTTP %d, sends %d: %s", w.Code, scheduleSendCount(f), w.Body)
			}
			task, _ := taskByID(s, "once")
			_, runs := scheduleState(s)
			r := runs[len(runs)-1]
			if task.Enabled || task.RunSessionID == "" || task.RunSessionID != r.SessionID || r.Status != runOK {
				t.Fatalf("executed work or its conversation was rolled back: %+v, %+v", task, r)
			}
			st.failWrites("")
			s.updateRun(r, runOK, "")
			assertScheduleStored(t, s, st)
			restarted := newServiceOn(t, newFakeHub(), st)
			_, reloaded := scheduleState(restarted)
			if len(reloaded) != len(runs) || reloaded[len(reloaded)-1].ID != r.ID {
				t.Fatalf("run history was lost: %+v", reloaded)
			}
		})
	}
}

type blockedScheduleWrite struct {
	Store
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (st *blockedScheduleWrite) PutSetting(ctx context.Context, key string, value any) error {
	blocked := false
	if key == tasksKey {
		st.once.Do(func() {
			blocked = true
			close(st.entered)
		})
	}
	if blocked {
		select {
		case <-st.release:
			return errors.New("injected delayed write failure")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return st.Store.PutSetting(ctx, key, value)
}

func TestScheduleFailedMutationPreservesConcurrentChanges(t *testing.T) {
	for _, outcome := range []bool{false, true} {
		t.Run(map[bool]string{false: "user edit", true: "runtime outcome"}[outcome], func(t *testing.T) {
			s, _, base := scheduleStorageFixture(t)
			st := &blockedScheduleWrite{Store: base, entered: make(chan struct{}), release: make(chan struct{})}
			s.store = func() Store { return st }
			failed := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				failed <- scheduleRequest(s, "PUT", "tasks/daily", `{"title":"Rejected","prompt":"Rejected","repeat":"daily","time":"10:00","enabled":false}`)
			}()
			select {
			case <-st.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("mutation did not reach the failing write")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				if outcome {
					s.recordRun(ScheduleRun{RefID: "once", Status: runFailed, Error: "Example failure"})
				} else {
					_, _, _ = s.forgetConversation(context.Background(), "once")
				}
			}()
			close(st.release)
			if w := <-failed; w.Code != http.StatusServiceUnavailable {
				t.Fatalf("failed write: HTTP %d: %s", w.Code, w.Body)
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("concurrent change did not finish")
			}
			daily, _ := taskByID(s, "daily")
			once, _ := taskByID(s, "once")
			if daily.Title != "Daily" || daily.Time != "09:00" || !daily.Enabled {
				t.Fatalf("rejected mutation leaked: %+v", daily)
			}
			if outcome && once.LastStatus != runFailed || !outcome && once.RunSessionID != "" {
				t.Fatalf("concurrent valid change was lost: %+v", once)
			}
			assertScheduleStored(t, s, st)
		})
	}
}

func TestScheduleLateOutcomesPreserveOwnership(t *testing.T) {
	s, _, st := scheduleStorageFixture(t)
	now := time.Now().UTC()
	s.sched.mu.Lock()
	i := s.sched.indexLocked("daily")
	s.sched.tasks[i].LastRun = now
	s.sched.tasks[i].RunSessionID = "current"
	err := s.saveLocked(&s.sched, s.sched.tasks)
	s.sched.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	st.failWrites(tasksKey)
	r := ScheduleRun{RefID: "daily", SessionID: "old", Status: runFailed, of: now.Add(-time.Hour)}
	r.ID = s.recordRun(r)
	s.updateRun(r, runFailed, "Old run failed")
	cur, _ := taskByID(s, "daily")
	if cur.LastStatus != runOK || cur.LastSessionID != "previous" || cur.RunSessionID != "current" {
		t.Fatalf("an old result took ownership of the current task: %+v", cur)
	}
	st.failWrites("")
	if _, err := s.deleteTask(t.Context(), "daily"); err != nil {
		t.Fatal(err)
	}
	s.adoptConversation("daily", "server", "late", "claude")
	s.updateRun(r, runFailed, "Old run failed")
	if _, ok := taskByID(s, "daily"); ok {
		t.Fatal("a late result or adoption resurrected a deleted task")
	}
	assertScheduleStored(t, s, st)
	restarted := newServiceOn(t, newFakeHub(), st)
	_, runs := scheduleState(restarted)
	if len(runs) != 2 || runs[1].ID != r.ID || runs[1].Error != "Old run failed" {
		t.Fatalf("deletion erased a real run's history: %+v", runs)
	}
}

func TestScheduleDefaultUpgradeWriteFailure(t *testing.T) {
	st := &scheduleWriteStore{Store: &memStore{}}
	old := Task{ID: "morning", Title: "Morning", Repeat: repeatDaily, Time: "09:00", Prompt: prompts.FormerMorningRoutines()[0],
		RunMachineID: "server", RunSessionID: "kept", LastSessionID: "previous", LastStatus: runOK}
	if err := st.PutSetting(t.Context(), tasksKey, []Task{old}); err != nil {
		t.Fatal(err)
	}
	st.failWrites(tasksKey)
	s := newServiceOn(t, newFakeHub(), st)
	if got, ok := taskByID(s, "morning"); !ok || !reflect.DeepEqual(got, old) {
		t.Fatalf("failed default upgrade changed the task: %+v", got)
	}
	assertScheduleStored(t, s, st)
	st.failWrites("")
	restarted := newServiceOn(t, newFakeHub(), st)
	got, ok := taskByID(restarted, "morning")
	if !ok || got.Prompt != prompts.RenderMorningRoutine() || !got.Facts || got.RunSessionID != old.RunSessionID || got.LastStatus != old.LastStatus {
		t.Fatalf("default upgrade did not recover or changed ownership: %+v", got)
	}
	assertScheduleStored(t, restarted, st)
}

func TestSchedulePostgresWriteFailure(t *testing.T) {
	s, f, st := scheduleStorageOn(t, scheduleStore(t))
	before, history := scheduleState(s)
	st.failWrites(tasksKey)
	body := `{"title":"New","prompt":"Example","repeat":"once","at":"2099-01-01T09:00:00Z","enabled":true}`
	if w := scheduleRequest(s, "POST", "tasks", body); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("Postgres write failure: HTTP %d: %s", w.Code, w.Body)
	}
	assertScheduleStored(t, s, st)
	st.failWrites("")
	if w := scheduleRequest(s, "POST", "tasks", body); w.Code != http.StatusOK {
		t.Fatalf("Postgres retry: HTTP %d: %s", w.Code, w.Body)
	}
	assertScheduleStored(t, s, st)
	if tasks, _ := scheduleState(s); len(tasks) != len(before)+1 {
		t.Fatal("successful retry did not create exactly one task")
	}
	// Admit real scheduled work against the same database, with only the node simulated.
	now := time.Now().UTC()
	tasks := []Task{{ID: "due", Title: "Due", Prompt: "Example", Repeat: repeatOnce, Enabled: true, At: now.Add(-time.Minute)}}
	if err := st.PutSetting(t.Context(), tasksKey, tasks); err != nil {
		t.Fatal(err)
	}
	setTasks(s, tasks...)
	st.failWrites(tasksKey)
	s.runDue(t.Context(), now)
	s.sched.inflight.Wait()
	if scheduleSendCount(f) != 0 {
		t.Fatal("failed consumed marker dispatched work")
	}
	assertScheduleStored(t, s, st)
	st.failWrites("")
	s.runDue(t.Context(), now)
	s.sched.inflight.Wait()
	assertScheduleStored(t, s, st)
	restarted := newServiceOn(t, readyHub(), st)
	restarted.runDue(t.Context(), now)
	restarted.sched.inflight.Wait()
	_, runs := scheduleState(restarted)
	if scheduleSendCount(f) != 1 || len(runs) != len(history)+1 {
		t.Fatalf("Postgres readback did not keep admission/history: sends %d, runs %+v", scheduleSendCount(f), runs)
	}
}
