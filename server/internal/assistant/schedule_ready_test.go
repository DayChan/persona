package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// readyStore is scheduleStore with the readiness record emptied too.
func readyStore(t *testing.T) Store {
	t.Helper()
	st := scheduleStore(t)
	if err := st.PutSetting(context.Background(), readinessKey, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.PutSetting(context.Background(), readinessKey, nil) })
	return st
}

// storedWaiting is the readiness record's RoutinesWaiting as Postgres has it.
func storedWaiting(t *testing.T, st Store) *time.Time {
	t.Helper()
	var rec readinessRecord
	if _, err := st.Setting(context.Background(), readinessKey, &rec); err != nil {
		t.Fatal(err)
	}
	return rec.RoutinesWaiting
}

func notices(t *testing.T, s *Service, conversation string) []string {
	t.Helper()
	items, _, err := s.feed(context.Background(), conversation, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range items {
		if it.Kind == feedNotice {
			var b noticeBody
			if err := json.Unmarshal(it.Body, &b); err != nil {
				t.Fatal(err)
			}
			out = append(out, b.Text)
		}
	}
	return out
}

func lastRunOf(t *testing.T, s *Service, id string) ScheduleRun {
	t.Helper()
	sc := s.loadSchedule(context.Background())
	sc.mu.Lock()
	defer sc.mu.Unlock()
	for i := len(sc.runs) - 1; i >= 0; i-- {
		if sc.runs[i].RefID == id {
			return sc.runs[i]
		}
	}
	t.Fatalf("no run of %s in %+v", id, sc.runs)
	return ScheduleRun{}
}

// A new hub with no computer yet: the morning briefing that comes due is skipped and recorded in plain words, nothing is pushed or
// sent. Once a computer is set up (here after a restart, the mark kept in Postgres) the next run goes as usual and the
// main conversation gets one line saying so; the runs after that add nothing.
func TestRoutineWaitsUntilTheMainConversationIsReady(t *testing.T) {
	st := readyStore(t)
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	day := func(n int) time.Time { return time.Date(2026, 11, 2+n, 9, 1, 0, 0, loc) } // Monday 09:01, then on
	main := "main-" + randomHex(6)

	f := newFakeHub()
	f.settings = &Settings{Timezone: loc.String(), Main: main}
	s := newServiceOn(t, f, st)
	setTasks(s, Task{ID: "morning", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Weekdays: []int{1, 2, 3, 4, 5},
		Enabled: true, LastRun: day(0).Add(-72 * time.Hour)})
	for d := range 4 {
		s.runDue(ctx, day(d))
		s.sched.inflight.Wait()
		if p := f.pushed(); len(p) != 0 {
			t.Fatalf("not-ready skip %d pushes nothing: %+v", d+1, p)
		}
		if r := lastRunOf(t, s, "morning"); !r.NotReady || r.StreakTold {
			t.Fatalf("not-ready skip %d is marked without telling a streak: %+v", d+1, r)
		}
	}
	if r := lastRunOf(t, s, "morning"); r.Status != runSkipped || r.Error != "还没接好电脑或模型，这次跳过" {
		t.Fatalf("with no computer the run is skipped: %+v", r)
	}
	if tk, _ := taskByID(s, "morning"); tk.LastStatus != runSkipped || !tk.Enabled {
		t.Fatalf("the task stays on, its last run skipped: %+v", tk)
	}
	if p := f.pushed(); len(p) != 0 {
		t.Fatalf("a skip pushes nothing: %+v", p)
	}
	if len(f.sends) != 0 || storedWaiting(t, st) == nil {
		t.Fatalf("nothing sent (%v), the wait is marked in Postgres", f.sends)
	}
	if n := notices(t, s, main); len(n) != 0 {
		t.Fatalf("no notice while waiting: %v", n)
	}

	// The computer is set up; the hub restarts.
	f2 := readyHub()
	f2.settings = f.settings
	s2 := newServiceOn(t, f2, st)
	if r := lastRunOf(t, s2, "morning"); !r.NotReady {
		t.Fatalf("the not-ready marker survives a restart: %+v", r)
	}
	s2.runDue(ctx, day(4))
	s2.sched.inflight.Wait()
	if r := lastRunOf(t, s2, "morning"); r.Status != runOK || r.SessionID == "" || r.NotReady {
		t.Fatalf("ready, the run goes: %+v", r)
	}
	if len(f2.sends) != 1 {
		t.Fatalf("sent %v", f2.sends)
	}
	if n := notices(t, s2, main); len(n) != 1 || n[0] != "电脑和模型接好了，定时任务恢复运行，这次是「早报」" {
		t.Fatalf("one line in the main conversation: %v", n)
	}
	if storedWaiting(t, st) != nil {
		t.Fatal("the mark is cleared")
	}
	s2.runDue(ctx, day(7))
	s2.sched.inflight.Wait()
	if n := notices(t, s2, main); len(n) != 1 || len(f2.sends) != 2 {
		t.Fatalf("the next run says nothing again: %v, sent %d", n, len(f2.sends))
	}
	if p := f2.pushed(); slices.ContainsFunc(p, func(n Notification) bool { return n.Title == "定时任务失败：早报" }) {
		t.Fatalf("pushed %+v", p)
	}
}

// A hub that works sees no change: the run goes, no notice, the readiness record is not marked; and nothing spent
// tokens to find out (no launcher check).
func TestRoutineOnAWorkingSetupRunsAsBefore(t *testing.T) {
	st := readyStore(t)
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	at := time.Date(2026, 11, 2, 9, 1, 0, 0, loc)
	main := "main-" + randomHex(6)
	f := readyHub()
	f.settings = &Settings{Timezone: loc.String(), Main: main}
	s := newServiceOn(t, f, st)
	setTasks(s, Task{ID: "morning", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Enabled: true, LastRun: at.Add(-24 * time.Hour)})
	s.runDue(ctx, at)
	s.sched.inflight.Wait()
	if r := lastRunOf(t, s, "morning"); r.Status != runOK || len(f.sends) != 1 {
		t.Fatalf("run %+v, sent %v", r, f.sends)
	}
	if n := notices(t, s, main); len(n) != 0 || storedWaiting(t, st) != nil {
		t.Fatalf("notices %v", n)
	}
	if len(f.checked) != 0 {
		t.Fatalf("spent tokens: %v", f.checked)
	}
}

// Setup problems and temporary offline states stay quiet across repeated skips and a hub restart.
func TestRoutineNotReadySkipsNeverPush(t *testing.T) {
	for _, scenario := range []string{"signed_out", "invalid_key", "offline"} {
		t.Run(scenario, func(t *testing.T) {
			st := readyStore(t)
			ctx := context.Background()
			f := readyHub()
			f.settings = &Settings{Timezone: "UTC", Main: "main-" + randomHex(6)}
			if scenario == "signed_out" {
				f.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeNotLoggedIn, AuthMethod: "none"}}
			}
			if scenario == "offline" {
				f.devices = []Device{{ID: "server", Name: "Computer"}}
			}
			s := newServiceOn(t, f, st)
			if scenario == "invalid_key" {
				f.checks = map[string]proto.LauncherCheck{"server/claude": {Error: "Invalid API key"}}
				if r := getReadiness(t, s, "probe=1", "en"); readinessStep(t, r, StepModel).Code != proto.TurnErrorAuthFailed {
					t.Fatalf("expected a cached authentication failure: %+v", r)
				}
			}
			checks := len(f.checked)
			at := time.Date(2026, 11, 2, 9, 1, 0, 0, time.UTC)
			setTasks(s, Task{ID: "morning", Title: "Morning brief", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Enabled: true,
				LastRun: at.Add(-24 * time.Hour)})
			for d := range 4 {
				s.runDue(ctx, at.AddDate(0, 0, d))
				s.sched.inflight.Wait()
				if r := lastRunOf(t, s, "morning"); r.Status != runSkipped || !r.NotReady || r.StreakTold ||
					r.Error != i18n.T(s.lang(ctx), "assistant-schedule.not_ready_skipped") {
					t.Fatalf("not-ready skip %d: %+v", d+1, r)
				}
				if len(f.sends) != 0 || len(f.pushed()) != 0 || len(f.checked) != checks {
					t.Fatalf("skip %d sent, pushed or probed: sends %v, pushes %v, checks %v", d+1, f.sends, f.pushed(), f.checked)
				}
				if d == 1 {
					s = newServiceOn(t, f, st)
					if r := lastRunOf(t, s, "morning"); !r.NotReady {
						t.Fatalf("not-ready marker lost after restart: %+v", r)
					}
				}
			}
			if waiting := storedWaiting(t, st) != nil; waiting != (scenario != "offline") {
				t.Fatalf("setup problems mark a wait, temporary offline does not: waiting %v", waiting)
			}
			if n := notices(t, s, f.settings.Main); len(n) != 0 {
				t.Fatalf("no ready notice while blocked: %v", n)
			}
		})
	}
}

// Only what the run needs counts: with the assistant's computer asleep, a follow-up into a conversation on another
// computer still goes, while a task that starts its own conversation waits. "Run now" is never held back.
func TestRoutineWaitsOnlyForWhatItNeeds(t *testing.T) {
	f := newFakeHub().withMachine()
	f.sessions["laptop"] = []proto.Session{{SessionID: "c1"}}
	f.devices = []Device{{ID: "server", Name: "server"}, {ID: "laptop", Name: "laptop", Online: true, Reachable: true}}
	f.settings = &Settings{Timezone: "UTC"}
	s := newStorelessService(t, f)
	ctx := context.Background()
	at := time.Date(2026, 11, 2, 9, 1, 0, 0, time.UTC)
	setTasks(s,
		Task{ID: "fu", Title: "跟进", Prompt: "y", Repeat: repeatInterval, EveryMinutes: 30, At: at.Add(-30 * time.Second), Enabled: true, MachineID: "laptop", SessionID: "c1"},
		Task{ID: "own", Title: "巡检", Prompt: "w", Repeat: repeatInterval, EveryMinutes: 30, At: at.Add(-30 * time.Second), Enabled: true})
	s.runDue(ctx, at)
	s.sched.inflight.Wait()
	if r := lastRunOf(t, s, "fu"); r.Status != runOK || r.SessionID != "c1" {
		t.Fatalf("the follow-up on the other computer goes: %+v", r)
	}
	if r := lastRunOf(t, s, "own"); r.Status != runSkipped || r.Error != "还没接好电脑或模型，这次跳过" {
		t.Fatalf("the task's own conversation waits for the assistant's computer: %+v", r)
	}
	if s.readinessRecordNow(ctx).RoutinesWaiting != nil {
		t.Fatal("a computer that is only asleep marks no wait: its waking is not news")
	}
	own, _ := taskByID(s, "own")
	if _, err := s.runTask(own, at, at, true, false); err != nil || lastRunOf(t, s, "own").Status == runSkipped || len(f.sends) != 2 {
		t.Fatalf("立即运行 is sent as before: %v %+v", err, lastRunOf(t, s, "own"))
	}
}

// The defaults a new hub starts with stay as they were: the weekday morning briefing at 09:00 on, the evening one off.
func TestDefaultRoutinesUnchanged(t *testing.T) {
	r := defaultRoutines("zh-Hans")
	if len(r) != 2 || r[0].ID != "morning" || !r[0].Enabled || r[0].Time != "09:00" || !slices.Equal(r[0].Weekdays, []int{1, 2, 3, 4, 5}) ||
		r[1].ID != "evening" || r[1].Enabled {
		t.Fatalf("defaults %+v", r)
	}
}

// A one-off (a reminder, a follow-up the assistant set for itself) is turned off once due: skipped it would be lost.
// It goes as before even while Claude Code is signed out, and its failure is pushed; no wait is marked.
func TestOneOffIsNotHeldBack(t *testing.T) {
	st := readyStore(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 2, 15, 0, 30, 0, time.UTC)
	f := readyHub()
	f.settings = &Settings{Timezone: "UTC", Main: "main-" + randomHex(6)}
	f.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeNotLoggedIn, AuthMethod: "none"}}
	s := newServiceOn(t, f, st)
	if r := s.readiness(ctx, false, readinessWaitGet); r.Mode == ReadinessFull {
		t.Fatalf("signed out is not ready: %+v", r)
	}
	setTasks(s, Task{ID: "fu_meet", Title: "提醒开会", Prompt: "提醒我开会", Repeat: repeatOnce, At: at.Add(-30 * time.Second), Enabled: true})
	s.runDue(ctx, at)
	s.sched.inflight.Wait()
	if r := lastRunOf(t, s, "fu_meet"); r.Status == runSkipped || len(f.sends) != 1 {
		t.Fatalf("the one-off is sent: %+v, sent %v", r, f.sends)
	}
	if storedWaiting(t, st) != nil {
		t.Fatal("a one-off marks no wait")
	}
}

// A routine on the main launcher waits while the main conversation's last turn said not logged in; once the
// launcher's health check (newer) answers, the next run goes: a skip adds no evidence of its own, so it must not
// stay gated on the old error.
func TestRoutineGoesOnceANewerHealthCheckAnswers(t *testing.T) {
	f, s, _ := readyMachine(t, proto.Session{SessionID: "m1", State: proto.StateIdle, TurnError: "Not logged in · Please run /login",
		UpdatedAt: time.Now().Add(-time.Hour)})
	f.auth = map[string]proto.ClaudeAuthStatus{"server/claude": {State: proto.ClaudeLoggedIn, AuthMethod: "api_key"}}
	ctx := context.Background()
	at := time.Date(2026, 11, 2, 9, 1, 0, 0, s.location(ctx))
	setTasks(s, Task{ID: "morning", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Enabled: true, LastRun: at.Add(-24 * time.Hour)})
	s.runDue(ctx, at)
	s.sched.inflight.Wait()
	if r := lastRunOf(t, s, "morning"); r.Status != runSkipped || r.Error != "还没接好电脑或模型，这次跳过" || len(f.sends) != 0 {
		t.Fatalf("the old not-logged-in turn holds it: %+v", r)
	}
	s.recordHealth(ctx, "server", s.launcherNow(ctx), healthResult{ok: true, model: "m", source: "check"})
	s.runDue(ctx, at.Add(24*time.Hour))
	s.sched.inflight.Wait()
	if r := lastRunOf(t, s, "morning"); r.Status != runOK || len(f.sends) != 1 {
		t.Fatalf("the newer health check lets it go: %+v, sent %v", r, f.sends)
	}
}
