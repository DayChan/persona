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
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
)

func TestNextRun(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	weekdays := Task{Repeat: repeatDaily, Time: "09:00", Weekdays: []int{1, 2, 3, 4, 5}, Enabled: true}
	fri := time.Date(2026, 10, 30, 10, 0, 0, 0, loc) // Friday after the run
	if got := nextRun(weekdays, fri, loc); !got.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, loc)) {
		t.Fatalf("after Friday 10:00 the next weekday run is Monday 09:00, got %v", got)
	}
	before := time.Date(2026, 10, 26, 8, 59, 0, 0, loc)
	if got := nextRun(weekdays, before, loc); !got.Equal(time.Date(2026, 10, 26, 9, 0, 0, 0, loc)) {
		t.Fatalf("same day, got %v", got)
	}
	every := Task{Repeat: repeatDaily, Time: "21:30"}
	if got := nextRun(every, time.Date(2026, 10, 31, 22, 0, 0, 0, loc), loc); !got.Equal(time.Date(2026, 11, 1, 21, 30, 0, 0, loc)) {
		t.Fatalf("every day, got %v", got)
	}
	if got := nextRun(Task{Time: "9am"}, before, loc); !got.IsZero() {
		t.Fatalf("bad time should never run, got %v", got)
	}
	at := before.Add(time.Hour)
	if got := (Task{Repeat: repeatInterval, At: at, Enabled: true}).next(before, loc); !got.Equal(at) {
		t.Fatalf("an interval task's next run is its At, got %v", got)
	}
	if got := (Task{Repeat: repeatOnce, At: at}).next(before, loc); !got.IsZero() {
		t.Fatalf("a task that is off has no next run, got %v", got)
	}
}

func TestParseWhen(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 10, 26, 16, 0, 0, 0, loc)
	cases := map[string]time.Time{
		"30m":                       now.Add(30 * time.Minute),
		"2h":                        now.Add(2 * time.Hour),
		"1d":                        now.Add(24 * time.Hour),
		"2026-10-27T09:50:00+08:00": time.Date(2026, 10, 27, 9, 50, 0, 0, loc),
		"2026-10-27 09:50":          time.Date(2026, 10, 27, 9, 50, 0, 0, loc),
	}
	for in, want := range cases {
		got, err := parseWhen(in, now, loc)
		if err != nil || !got.Equal(want) {
			t.Fatalf("%q: got %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseWhen("tomorrow", now, loc); err == nil {
		t.Fatal("expected an error")
	}
}

// A day count whose duration does not fit is refused: 213504d wrapped around to about 25 minutes, so at=213504d ran
// soon instead of never. A negative one is refused too.
func TestRelativeRejectsOverflow(t *testing.T) {
	cases := map[string]bool{
		"213504d":      false,
		"106752d":      false, // the first day count past the largest duration
		"99999999999d": false,
		"-1d":          false,
		"106751d":      true,
		"1d":           true,
		"30m":          true,
	}
	for in, want := range cases {
		if d, ok := relative(in); ok != want || (ok && d <= 0) {
			t.Errorf("relative(%q) = %v, %v; want ok %v", in, d, ok, want)
		}
	}
	if _, err := parseWhen("213504d", time.Now(), time.UTC); err == nil {
		t.Error("at=213504d is refused, not scheduled about 25 minutes from now")
	}
}

// readyHub is a fake hub whose main conversation is ready (the computer "server" with Claude Code on it), so
// scheduled runs are not skipped for want of one (schedule_ready.go).
func readyHub() *fakeHub {
	f := newFakeHub().withMachine()
	f.doctor = map[string]string{"server": "2.1.308"}
	return f
}

func setTasks(s *Service, tasks ...Task) *schedule {
	sc := s.loadSchedule(context.Background())
	sc.mu.Lock()
	sc.tasks = tasks
	sc.mu.Unlock()
	return sc
}

func taskByID(s *Service, id string) (Task, bool) {
	sc := s.loadSchedule(context.Background())
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if i := sc.indexLocked(id); i >= 0 {
		return sc.tasks[i], true
	}
	return Task{}, false
}

func TestDueTasksRunOnceAndStaleDailyOnesAreSkipped(t *testing.T) {
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	at := func(hh, mm int) time.Time { return time.Date(2026, 10, 26, hh, mm, 0, 0, loc) }
	sc := setTasks(h,
		Task{ID: "r", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Enabled: true, LastRun: at(9, 0).Add(-24 * time.Hour)},
		Task{ID: "f1", Repeat: repeatOnce, At: at(9, 0).Add(30 * time.Second), Prompt: "y", Enabled: true},
		Task{ID: "f2", Repeat: repeatOnce, At: at(12, 0), Prompt: "z", Enabled: true},
		Task{ID: "p", Title: "巡检", Repeat: repeatInterval, EveryMinutes: 30, At: at(8, 50), Prompt: "w", Enabled: true})

	// 09:01: the daily task, the first one-off and the patrol are due (no machine, so the runs just log and give up).
	h.runDue(context.Background(), at(9, 1))
	h.sched.inflight.Wait() // the runs it started give up (no machine) before we look
	if r, _ := taskByID(h, "r"); !r.LastRun.Equal(at(9, 1)) {
		t.Fatalf("the daily task should have run at 09:01, last = %v", r.LastRun)
	}
	if f, _ := taskByID(h, "f1"); f.Enabled || !f.done() {
		t.Fatalf("a one-off that ran is done: %+v", f)
	}
	if f, _ := taskByID(h, "f2"); !f.Enabled {
		t.Fatal("f2 is still to come")
	}
	if p, _ := taskByID(h, "p"); !p.Enabled || !p.At.Equal(at(9, 20)) {
		t.Fatalf("a little late, the patrol runs now and stays on its half hours: %+v", p)
	}
	// Down for longer than an interval: once now, then every 30 minutes from here.
	sc.mu.Lock()
	sc.tasks[3].At = at(7, 0)
	sc.mu.Unlock()
	h.runDue(context.Background(), at(9, 2))
	h.sched.inflight.Wait() // the runs it started give up (no machine) before we look
	if p, _ := taskByID(h, "p"); !p.At.Equal(at(9, 32)) {
		t.Fatalf("after a long outage: %+v", p)
	}
	if len(h.tasksView(context.Background(), nil)) != 3 {
		t.Fatal("the app doesn't list a one-off that already ran")
	}
	// A hub that was down all morning does not send the morning briefing at noon.
	sc.mu.Lock()
	sc.tasks[0].LastRun = at(9, 0).Add(-24 * time.Hour)
	sc.mu.Unlock()
	h.runDue(context.Background(), at(11, 0))
	h.sched.inflight.Wait() // the runs it started give up (no machine) before we look
	if r, _ := taskByID(h, "r"); !r.LastRun.Equal(at(11, 0)) || r.LastStatus != runSkipped {
		t.Fatalf("a stale run is skipped but recorded: %+v", r)
	}
	// A day later the one-off that ran is gone.
	h.runDue(context.Background(), at(9, 2).Add(25*time.Hour))
	h.sched.inflight.Wait() // the runs it started give up (no machine) before we look
	if _, ok := taskByID(h, "f1"); ok {
		t.Fatal("a one-off that ran a day ago is only history")
	}
}

// Friday's morning briefing ran 72 hours before Monday's, a weekly task's last run 168 hours before: a hub that comes up five
// minutes late on Monday still sends them, and one too late records the miss, as on any other weekday (#3397: it looked
// back two minutes only, and the run was dropped without a trace).
func TestWeekdayTaskCatchesUpOnMonday(t *testing.T) {
	f := readyHub() // a set-up computer: a skip here is a missed time, not one for want of a computer
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	friday := time.Date(2026, 10, 30, 9, 0, 0, 0, loc)
	monday := time.Date(2026, 11, 2, 9, 0, 0, 0, loc)
	setTasks(h,
		Task{ID: "r", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Weekdays: []int{1, 2, 3, 4, 5}, Enabled: true, LastRun: friday},
		Task{ID: "w", Title: "周报", Prompt: "y", Repeat: repeatDaily, Time: "09:00", Weekdays: []int{1}, Enabled: true, LastRun: monday.Add(-7 * 24 * time.Hour)},
		Task{ID: "early", Title: "更早", Prompt: "z", Repeat: repeatDaily, Time: "08:00", Weekdays: []int{1, 2, 3, 4, 5}, Enabled: true, LastRun: friday.Add(-time.Hour)},
		Task{ID: "new", Title: "新建", Prompt: "n", Repeat: repeatDaily, Time: "09:00", Enabled: true})

	at := monday.Add(5 * time.Minute)
	h.runDue(context.Background(), at)
	h.sched.inflight.Wait() // the runs it started give up (no machine) before we look
	for _, id := range []string{"r", "w"} {
		if r, _ := taskByID(h, id); !r.LastRun.Equal(at) || r.LastStatus == runSkipped {
			t.Fatalf("%s, five minutes late on Monday, runs: %+v", id, r)
		}
	}
	if r, _ := taskByID(h, "early"); r.LastStatus != runSkipped || !r.LastRun.Equal(at) {
		t.Fatalf("an hour past its time it is not sent stale, but the miss is recorded: %+v", r)
	}
	if r, _ := taskByID(h, "new"); !r.LastRun.IsZero() {
		t.Fatalf("a task that never ran still only catches up the last couple of minutes (#664): %+v", r)
	}
	// Tuesday goes by the run Monday recorded, as before.
	h.runDue(context.Background(), monday.Add(24*time.Hour+time.Minute))
	h.sched.inflight.Wait()
	if r, _ := taskByID(h, "r"); !r.LastRun.Equal(monday.Add(24*time.Hour + time.Minute)) {
		t.Fatalf("Tuesday's run: %+v", r)
	}
	if r, _ := taskByID(h, "w"); !r.LastRun.Equal(at) {
		t.Fatalf("the weekly one waits for next Monday: %+v", r)
	}
}

// A run that ends after a newer run or skip of its task started is history only: the slow 09:01 run (an assistant
// that takes a while to give up) records its failure after the 11:00 skip, and the task still says skipped (#556).
func TestALateRunDoesNotOverwriteANewerStatus(t *testing.T) {
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	at := func(hh, mm int) time.Time { return time.Date(2026, 10, 26, hh, mm, 0, 0, loc) }
	setTasks(h, Task{ID: "r", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Enabled: true,
		LastRun: at(11, 0).UTC(), LastStatus: runSkipped, LastError: "Persona 停机，错过了时间"})

	h.recordRun(ScheduleRun{Kind: "routine", RefID: "r", Status: runFailed, Error: "assistant machine is not configured", of: at(9, 1).UTC()})
	r, _ := taskByID(h, "r")
	if r.LastStatus != runSkipped || r.LastError != "Persona 停机，错过了时间" {
		t.Fatalf("the late run overwrote the newer status: %+v", r)
	}
	sc := h.loadSchedule(context.Background())
	sc.mu.Lock()
	n := len(sc.runs)
	sc.mu.Unlock()
	if n != 1 {
		t.Fatalf("the late run is not in the history: %d runs", n)
	}
	// The run of the current LastRun, and "Run now" (started after it), do set it.
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "r", Status: runOK, SessionID: "s2", of: at(11, 0).UTC()})
	if r, _ := taskByID(h, "r"); r.LastStatus != runOK || r.LastSessionID != "s2" {
		t.Fatalf("the latest run's status: %+v", r)
	}
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "r", Status: runFailed, Error: "x", Manual: true, of: at(11, 30).UTC()})
	if r, _ := taskByID(h, "r"); r.LastStatus != runFailed {
		t.Fatalf("立即运行 after it: %+v", r)
	}
}

// The 30-minute patrol from 2026-10-31: each run ended with schedule_followup(new_conversation, at=30m, the same
// prompt), so it was a new one-off every time, gone from the app while a run was stuck. Now its first re-schedule
// turns it into one task every 30 minutes, with the launcher and mode the user picked, and later ones change nothing.
func TestARunReschedulingItselfKeepsOneTask(t *testing.T) {
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	m := "server"
	prompt := "按手册巡检一次，最后用 schedule_followup（new_conversation=true，at=30m，prompt 和本条相同）安排下一次。"
	setTasks(h, Task{ID: "fu_1", Title: "30 分钟巡检", Prompt: prompt, Repeat: repeatOnce, At: time.Now().Add(-time.Minute),
		LastRun: time.Now(), MachineID: "server", PermissionMode: "acceptEdits", Launcher: "claude-fast"})
	h.recordRun(ScheduleRun{Kind: "followup", RefID: "fu_1", Status: runOK, MachineID: "server", SessionID: "run-1"})

	copied := "⏰ 30 分钟巡检（你之前安排的跟进）\n\n" + prompt // the run copies its whole message
	args, _ := json.Marshal(map[string]any{"at": "30m", "prompt": copied, "title": "30 分钟巡检", "new_conversation": true})
	out, err := h.toolScheduleFollowup(context.Background(), m, "run-1", args)
	if err != nil || !strings.Contains(out, "every 30 分钟") {
		t.Fatalf("schedule: %q, %v", out, err)
	}
	tasks := h.tasksView(context.Background(), nil)
	if len(tasks) != 1 {
		t.Fatalf("still one task, got %+v", tasks)
	}
	p := tasks[0]
	if p.ID != "fu_1" || p.Repeat != repeatInterval || p.EveryMinutes != 30 || !p.Enabled || p.Prompt != prompt ||
		p.PermissionMode != "acceptEdits" || p.Launcher != "" {
		t.Fatalf("one repeating task with the user's settings and the prompt without the ⏰ line: %+v", p)
	}
	if got := taskPrompt(p, p.At, p.At, time.UTC, false, false, false); !strings.Contains(got, "不用再用 schedule_followup") ||
		strings.Count(got, "⏰") != 1 {
		t.Fatalf("a repeating run is told it repeats by itself: %q", got)
	}

	// The next run still asks for "the next one": nothing new, the schedule stays.
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "fu_1", Status: runOK, MachineID: "server", SessionID: "run-2"})
	next := p.At
	if out, err = h.toolScheduleFollowup(context.Background(), m, "run-2", args); err != nil || !strings.Contains(out, "already repeats") {
		t.Fatalf("again: %q, %v", out, err)
	}
	if tasks = h.tasksView(context.Background(), nil); len(tasks) != 1 || !tasks[0].At.Equal(next) {
		t.Fatalf("no second task, same next run: %+v", tasks)
	}
	// Turned off in the app: a run doesn't turn it back on.
	sc := h.loadSchedule(context.Background())
	sc.mu.Lock()
	sc.tasks[0].Enabled = false
	sc.mu.Unlock()
	if out, _ = h.toolScheduleFollowup(context.Background(), m, "run-2", args); !strings.Contains(out, "turned off") {
		t.Fatalf("off: %q", out)
	}
	if p, _ := taskByID(h, "fu_1"); p.Enabled {
		t.Fatal("stays off")
	}
	// Another conversation, or another follow-up from this one, is a new task.
	other, _ := json.Marshal(map[string]any{"at": "2h", "prompt": "看看 CI", "title": "看 CI"})
	if _, err := h.toolScheduleFollowup(context.Background(), m, "run-2", other); err != nil {
		t.Fatal(err)
	}
	if n := len(h.tasksView(context.Background(), nil)); n != 2 {
		t.Fatalf("a different follow-up is its own task, have %d", n)
	}
}

// A follow-up the user deleted while one of its runs was going: that run's "schedule the next one" does not bring it
// back, and another conversation's run of the same task's text is not confused with it (#4792).
func TestARunOfADeletedTaskDoesNotBringItBack(t *testing.T) {
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	prompt := "按手册巡检一次，最后用 schedule_followup（new_conversation=true，at=30m，prompt 和本条相同）安排下一次。"
	setTasks(h, Task{ID: "fu_1", Title: "30 分钟巡检", Prompt: prompt, Repeat: repeatOnce, At: time.Now().Add(-time.Minute),
		LastRun: time.Now(), MachineID: "server"},
		Task{ID: "fu_2", Title: "看 CI", Prompt: "看看 CI", Repeat: repeatOnce, At: time.Now().Add(time.Hour), Enabled: true, MachineID: "server"})
	h.recordRun(ScheduleRun{Kind: "followup", RefID: "fu_2", Title: "看 CI", At: time.Now().Add(-time.Hour), Status: runOK, MachineID: "server", SessionID: "run-1"})
	h.recordRun(ScheduleRun{Kind: "followup", RefID: "fu_1", Title: "30 分钟巡检", At: time.Now(), Status: runOK, MachineID: "server", SessionID: "run-1"})
	if ok, err := h.deleteTask(context.Background(), "fu_1"); !ok || err != nil {
		t.Fatalf("delete: %v %v", ok, err)
	}

	copied := "⏰ 30 分钟巡检（你之前安排的跟进）\n\n" + prompt
	for _, a := range []map[string]any{
		{"at": "30m", "prompt": copied, "title": "30 分钟巡检", "new_conversation": true},
		{"at": "30m", "prompt": copied, "new_conversation": true}, // no title: the prompt says which task it is
	} {
		args, _ := json.Marshal(a)
		out, err := h.toolScheduleFollowup(context.Background(), "server", "run-1", args)
		if err != nil || !strings.Contains(out, "fu_1") {
			t.Fatalf("told it was deleted: %q, %v", out, err)
		}
		tasks := h.tasksView(context.Background(), nil)
		if len(tasks) != 1 || tasks[0].ID != "fu_2" || !tasks[0].At.After(time.Now().Add(50*time.Minute)) {
			t.Fatalf("the deleted one stays deleted, the other is untouched: %+v", tasks)
		}
	}
	// Something else asked from that conversation is still a new task.
	other, _ := json.Marshal(map[string]any{"at": "2h", "prompt": "提醒我交周报", "title": "交周报"})
	if _, err := h.toolScheduleFollowup(context.Background(), "server", "run-1", other); err != nil {
		t.Fatal(err)
	}
	if n := len(h.tasksView(context.Background(), nil)); n != 2 {
		t.Fatalf("a different follow-up is its own task, have %d", n)
	}
}

func TestScheduleFollowupEvery(t *testing.T) {
	h := newStorelessService(t, newFakeHub())
	m := "server"
	setTasks(h)
	out, err := h.toolScheduleFollowup(context.Background(), m, "s1", json.RawMessage(`{"every":"2h","prompt":"看看部署","title":"部署巡检"}`))
	if err != nil || !strings.Contains(out, "every 2 小时") {
		t.Fatalf("%q, %v", out, err)
	}
	tasks := h.tasksView(context.Background(), nil)
	if len(tasks) != 1 || tasks[0].Repeat != repeatInterval || tasks[0].EveryMinutes != 120 || tasks[0].SessionID != "s1" ||
		tasks[0].CreatedBy != "assistant" || tasks[0].PermissionMode != "" {
		t.Fatalf("%+v", tasks)
	}
	if time.Until(tasks[0].At) < 119*time.Minute {
		t.Fatalf("the first run is one interval away: %v", tasks[0].At)
	}
	if _, err := h.toolScheduleFollowup(context.Background(), m, "s1", json.RawMessage(`{"every":"1m","prompt":"x"}`)); err == nil {
		t.Fatal("more often than every 5 minutes is refused")
	}
}

// every is held to the range checkTask holds the app to: a task saved every 8 days could not be saved again from the
// app (not even switched off), so the tool refuses it too, and saves nothing.
func TestScheduleFollowupRejectsIntervalOverAWeek(t *testing.T) {
	h := newStorelessService(t, newFakeHub())
	setTasks(h)
	for _, every := range []string{"8d", "169h", "213504d"} {
		args, _ := json.Marshal(map[string]any{"every": every, "prompt": "Check the deploy", "title": "Deploy check"})
		if out, err := h.toolScheduleFollowup(context.Background(), "server", "s1", args); err == nil || !strings.Contains(err.Error(), "7d") {
			t.Fatalf("every=%s is refused with the range: %q, %v", every, out, err)
		}
		if tasks := h.tasksView(context.Background(), nil); len(tasks) != 0 {
			t.Fatalf("every=%s saves nothing: %+v", every, tasks)
		}
	}
	if _, err := h.toolScheduleFollowup(context.Background(), "server", "s1", json.RawMessage(`{"every":"7d","prompt":"x"}`)); err != nil {
		t.Fatalf("every 7 days is the longest allowed: %v", err)
	}
	tasks := h.tasksView(context.Background(), nil)
	if len(tasks) != 1 || tasks[0].EveryMinutes != maxEvery {
		t.Fatalf("one task every 7 days: %+v", tasks)
	}
	if err := checkTask(&tasks[0], time.Now()); err != nil {
		t.Fatalf("the app can save it again: %v", err)
	}
}

// A run rescheduling its one-off task more than 7 days out keeps it a one-off: promoted into an interval it would be
// one the app refuses to save again.
func TestScheduleOwnKeepsLongRelativeAOneOff(t *testing.T) {
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	prompt := "Check the renewal, then use schedule_followup (at=30d, the same prompt) to schedule the next one."
	setTasks(h, Task{ID: "fu_1", Title: "Renewal", Prompt: prompt, Repeat: repeatOnce, At: time.Now().Add(-time.Minute),
		LastRun: time.Now(), MachineID: "server"})
	h.recordRun(ScheduleRun{Kind: "followup", RefID: "fu_1", Status: runOK, MachineID: "server", SessionID: "run-1"})

	args, _ := json.Marshal(map[string]any{"at": "30d", "prompt": prompt, "title": "Renewal"})
	out, err := h.toolScheduleFollowup(context.Background(), "server", "run-1", args)
	if err != nil || !strings.Contains(out, "Rescheduled fu_1") {
		t.Fatalf("rescheduled: %q, %v", out, err)
	}
	tasks := h.tasksView(context.Background(), nil)
	if len(tasks) != 1 || tasks[0].ID != "fu_1" || tasks[0].Repeat != repeatOnce || tasks[0].EveryMinutes != 0 || !tasks[0].Enabled ||
		time.Until(tasks[0].At) < 29*24*time.Hour {
		t.Fatalf("the same one-off, 30 days out: %+v", tasks)
	}
	if err := checkTask(&tasks[0], time.Now()); err != nil {
		t.Fatalf("the app can save it again: %v", err)
	}
}

func TestCheckTask(t *testing.T) {
	now := time.Now()
	ok := []Task{
		{Repeat: repeatDaily, Title: "早报", Prompt: "x", Time: "09:00", Weekdays: []int{5, 1, 1}},
		{Repeat: repeatInterval, Prompt: "x", EveryMinutes: 30, PermissionMode: "bypassPermissions", Effort: "high", Launcher: "claude-fast"},
		{Repeat: repeatOnce, Prompt: "x", At: now.Add(time.Hour), Enabled: true, PermissionMode: "acceptEdits"},
		{Repeat: repeatOnce, Prompt: "x", At: now.Add(-time.Hour)}, // off: a past time is fine
	}
	for _, task := range ok {
		if err := checkTask(&task, now); err != nil {
			t.Fatalf("%+v: %v", task, err)
		}
	}
	d := ok[0]
	_ = checkTask(&d, now)
	if len(d.Weekdays) != 2 || d.Weekdays[0] != 1 {
		t.Fatalf("weekdays tidied: %v", d.Weekdays)
	}
	bad := []Task{
		{Repeat: repeatDaily, Title: "x", Prompt: "x", Time: "9am"},
		{Repeat: repeatDaily, Title: "x", Prompt: "x", Time: "09:00", Weekdays: []int{0}},
		{Repeat: repeatInterval, Prompt: "x", EveryMinutes: 2},
		{Repeat: repeatOnce, Prompt: "x", At: now.Add(-time.Hour), Enabled: true},
		{Repeat: repeatOnce, Prompt: "x", At: now.Add(time.Hour), PermissionMode: "yolo"},
		{Repeat: repeatOnce, Prompt: "x", At: now.Add(time.Hour), Effort: "huge"},
		{Repeat: repeatOnce, Prompt: "x", At: now.Add(time.Hour), Launcher: "rm -rf"},
		{Repeat: "weekly", Prompt: "x"},
		{Repeat: repeatOnce, At: now.Add(time.Hour)},
	}
	for _, task := range bad {
		if err := checkTask(&task, now); err == nil {
			t.Fatalf("%+v should be refused", task)
		}
	}
}

func TestEditTask(t *testing.T) {
	now := time.Now()
	old := Task{ID: "p", Repeat: repeatInterval, EveryMinutes: 30, At: now.Add(10 * time.Minute), Enabled: true, SessionID: "s1",
		MachineID: "server", LastStatus: runOK, CreatedBy: "assistant"}
	in := old
	in.LastStatus, in.CreatedBy, in.SessionID, in.MachineID = "", "", "s-other", "elsewhere"
	in.PermissionMode = "bypassPermissions"
	got := editTask(old, in, now)
	if got.LastStatus != runOK || got.CreatedBy != "assistant" || got.SessionID != "s1" || got.MachineID != "server" ||
		got.PermissionMode != "bypassPermissions" || !got.At.Equal(old.At) {
		t.Fatalf("the hub's fields stay, the conversation can't be pointed elsewhere, the next run stays: %+v", got)
	}
	in.EveryMinutes, in.SessionID = 60, ""
	got = editTask(old, in, now)
	if got.SessionID != "" || !got.At.Equal(now.Add(time.Hour)) {
		t.Fatalf("a new interval starts from now; clearing the conversation means a new one each run: %+v", got)
	}
}

func TestTaskRoutes(t *testing.T) {
	h := newStorelessService(t, newFakeHub())
	setTasks(h, Task{ID: "morning", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Enabled: true},
		Task{ID: "fu_1", Title: "巡检", Prompt: "y", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(time.Minute), Enabled: true,
			MachineID: "server"})
	mux := h // the routes the hub relays
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	rec := do("GET", "/api/v1/assistant/tasks", "")
	if !strings.Contains(rec.Body.String(), `"repeat":"interval"`) || !strings.Contains(rec.Body.String(), `"next_run"`) {
		t.Fatalf("list: %s", rec.Body)
	}
	rec = do("PUT", "/api/v1/assistant/tasks/fu_1", `{"title":"巡检","prompt":"y","repeat":"interval","every_minutes":30,"enabled":true,"permission_mode":"acceptEdits","effort":"low","launcher":"claude-fast"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"permission_mode":"acceptEdits"`) {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	if rec = do("PUT", "/api/v1/assistant/tasks/fu_1", `{"title":"巡检","prompt":"y","repeat":"interval","every_minutes":30,"permission_mode":"yolo"}`); rec.Code != 400 {
		t.Fatalf("an unknown mode is refused: %d", rec.Code)
	}
	rec = do("POST", "/api/v1/assistant/tasks", `{"title":"喝水","prompt":"提醒我喝水","repeat":"once","at":"2099-01-01T09:00:00Z","enabled":true,"session_id":"s9"}`)
	var created struct{ Task Task }
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if rec.Code != 200 || created.Task.ID == "" || created.Task.SessionID != "" || created.Task.MachineID == "" && h.hello(context.Background()).Assistant.MachineID != "" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	// Builds from before keep working: routines are the daily tasks, follow-ups the rest.
	rec = do("GET", "/api/v1/assistant/routines", "")
	if strings.Contains(rec.Body.String(), "fu_1") || !strings.Contains(rec.Body.String(), "morning") {
		t.Fatalf("routines: %s", rec.Body)
	}
	rec = do("GET", "/api/v1/assistant/followups", "")
	if !strings.Contains(rec.Body.String(), "fu_1") || !strings.Contains(rec.Body.String(), created.Task.ID) || strings.Contains(rec.Body.String(), "morning") {
		t.Fatalf("follow-ups: %s", rec.Body)
	}
	rec = do("PUT", "/api/v1/assistant/routines", `{"routines":[{"id":"morning","title":"早报","prompt":"x2","time":"08:30","enabled":true}]}`)
	if rec.Code != 200 {
		t.Fatalf("put routines: %d %s", rec.Code, rec.Body)
	}
	if n := len(h.tasksView(context.Background(), nil)); n != 3 {
		t.Fatalf("saving the routines leaves the other tasks alone, have %d", n)
	}
	if p, _ := taskByID(h, "fu_1"); p.PermissionMode != "acceptEdits" {
		t.Fatal("and their settings")
	}
	if rec = do("DELETE", "/api/v1/assistant/followups/fu_1", ""); rec.Code != 200 {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec = do("DELETE", "/api/v1/assistant/tasks/fu_1", ""); rec.Code != 404 {
		t.Fatalf("delete twice: %d", rec.Code)
	}
}

func TestScheduleRoutineTool(t *testing.T) {
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	out, err := h.toolScheduleRoutine(context.Background(), "", json.RawMessage(`{"title":"周报","prompt":"起草周报","time":"17:00","weekdays":[5]}`))
	if err != nil || !strings.Contains(out, "周五 17:00") {
		t.Fatalf("create: %q, %v", out, err)
	}
	sc := h.loadSchedule(context.Background())
	r := sc.tasks[len(sc.tasks)-1]
	if r.CreatedBy != "assistant" || !r.Enabled || r.Title != "周报" || r.Repeat != repeatDaily {
		t.Fatalf("routine %+v", r)
	}
	if _, err := h.toolScheduleRoutine(context.Background(), "", json.RawMessage(`{"id":"`+r.ID+`","enabled":false,"weekdays":[1,2,3,4,5,6,7]}`)); err != nil {
		t.Fatal(err)
	}
	if r2 := sc.tasks[len(sc.tasks)-1]; r2.Enabled || r2.Weekdays != nil || r2.Prompt != "起草周报" {
		t.Fatalf("an update changes only the fields given: %+v", r2)
	}
	if _, err := h.toolScheduleRoutine(context.Background(), "", json.RawMessage(`{"title":"x","prompt":"y","time":"9am"}`)); err == nil {
		t.Fatal("a bad time is refused")
	}
	if _, err := h.toolScheduleRoutine(context.Background(), "", json.RawMessage(`{"title":"x","prompt":"y","time":"09:00","weekdays":[0]}`)); err == nil {
		t.Fatal("weekday 0 is refused")
	}
	if !strings.Contains(h.describeSchedule(context.Background(), ""), r.ID) {
		t.Fatal("list_followups shows task ids")
	}
	if ok, err := h.deleteTask(context.Background(), r.ID); !ok || err != nil {
		t.Fatalf("delete: %v, %v", ok, err)
	}
	if ok, _ := h.deleteTask(context.Background(), r.ID); ok {
		t.Fatal("delete once")
	}
}

func TestStripScheduleHeaderPreservesLegacyTaskIdentity(t *testing.T) {
	const body = "巡检"
	for _, brand := range []string{"Persona", "Persona"} {
		prompt := "⏰ 巡检（定时任务，每 30 分钟）\n（" + brand + " 会按时再运行它，不用再用 schedule_followup 安排下一次。）\n\n" + body
		if got := stripScheduleHeader(prompt); got != body {
			t.Fatalf("%s task header remained: %q", brand, got)
		}
		if !sameText(prompt, body) {
			t.Fatalf("%s task no longer matches its saved prompt", brand)
		}
		mixed := "（Persona 会按时再运行它。）\n（Persona 会按时再运行它。）\n\n" + prompt
		if got := stripScheduleHeader(mixed); got != body {
			t.Fatalf("mixed task headers remained: %q", got)
		}
	}
}

func TestStripScheduleHeader(t *testing.T) {
	in := "⏰ 30 分钟巡检（你之前安排的跟进）\n（本该 10-31 11:00 运行，Persona 当时没在运行，晚了 25 分钟；看看还有没有必要按原计划做。）\n\n" +
		"⏰ 30 分钟巡检（定时任务，每 30 分钟）\n（Persona 会按时再运行它，不用再用 schedule_followup 安排下一次。）\n\n跑 detect.sh"
	if got := stripScheduleHeader(in); got != "跑 detect.sh" {
		t.Fatalf("%q", got)
	}
	if got := stripScheduleHeader("⏰ 提醒我喝水"); got != "⏰ 提醒我喝水" {
		t.Fatalf("a prompt of the user's own is kept: %q", got)
	}
}

func scheduleStore(t *testing.T) *db.DB {
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
	for _, k := range []string{tasksKey, routinesKey, followupsKey, runsKey} {
		if err := database.PutSetting(context.Background(), k, nil); err != nil {
			t.Fatal(err)
		}
	}
	return database
}

func waitRuns(t *testing.T, s *Service, n int) []ScheduleRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		sc := s.loadSchedule(context.Background())
		sc.mu.Lock()
		runs := append([]ScheduleRun(nil), sc.runs...)
		sc.mu.Unlock()
		if len(runs) >= n {
			return runs
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d runs, have %+v", n, runs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The first hub with one task list moves the routines and follow-ups over: ids, times and last runs kept, the
// follow-up still runs once, the old list is emptied, and a second boot doesn't move anything again.
func TestTasksMovedFromTheOldLists(t *testing.T) {
	store := scheduleStore(t)
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	due := time.Now().Add(-time.Minute).Truncate(time.Second)
	// The morning briefing twelve hours from now, never a fixed 09:00: with no last run a daily routine catches up the last couple of
	// minutes, so a run just after 09:00 Shanghai time ran it too and saw two runs (#664).
	morning := time.Now().In(loc).Add(12 * time.Hour).Format("15:04")
	if err := store.PutSetting(ctx, routinesKey, []Routine{{ID: "morning", Title: "早报", Prompt: "x", Time: morning, Enabled: true,
		LastStatus: runOK}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSetting(ctx, followupsKey, []Followup{{ID: "fu_p", At: due, Title: "30 分钟巡检",
		Prompt: "⏰ 30 分钟巡检（你之前安排的跟进）\n\n巡检", MachineID: "server"}}); err != nil {
		t.Fatal(err)
	}
	boot := func() *Service {
		f := newFakeHub()
		f.settings = &Settings{Timezone: loc.String()}
		return newServiceOn(t, f, store)
	}
	h1 := boot()
	tasks := h1.tasksView(ctx, nil)
	if len(tasks) != 2 || tasks[0].ID != "morning" || tasks[0].Repeat != repeatDaily || tasks[0].LastStatus != runOK ||
		tasks[1].ID != "fu_p" || tasks[1].Repeat != repeatOnce || !tasks[1].At.Equal(due) || tasks[1].Prompt != "巡检" || !tasks[1].Enabled {
		t.Fatalf("moved: %+v", tasks)
	}
	var old []Followup
	if _, err := store.Setting(ctx, followupsKey, &old); err != nil || len(old) != 0 {
		t.Fatalf("the old follow-ups are emptied: %+v, %v", old, err)
	}
	h1.runDue(ctx, time.Now())
	waitRuns(t, h1, 1)

	h2 := boot()
	h2.runDue(ctx, time.Now())
	if runs := h2.loadSchedule(ctx).runs; len(runs) != 1 || runs[0].RefID != "fu_p" {
		t.Fatalf("after a restart the patrol doesn't run again: %+v", runs)
	}
	if n := len(h2.tasksView(ctx, nil)); n != 1 {
		t.Fatalf("nothing moved twice, the one-off that ran is hidden: %d", n)
	}
}

// The schedule lives in Postgres: after a restart nothing runs twice, a follow-up missed while the hub was down runs
// once (late), a routine missed by long is skipped, and the history keeps all of it.
func TestScheduleAcrossHubRestart(t *testing.T) {
	store := scheduleStore(t)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	at := func(h, m int) time.Time { return time.Date(2026, 10, 26, h, m, 0, 0, loc) }
	boot := func() *Service {
		f := newFakeHub()
		f.settings = &Settings{Timezone: "Asia/Shanghai"}
		return newServiceOn(t, f, store)
	}
	h1 := boot()
	sc := h1.loadSchedule(context.Background())
	sc.mu.Lock()
	sc.tasks = []Task{{ID: "r9", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Enabled: true, LastRun: at(9, 0).Add(-24 * time.Hour)},
		{ID: "r11", Title: "午报", Prompt: "x", Repeat: repeatDaily, Time: "11:00", Enabled: true, LastRun: at(11, 0).Add(-24 * time.Hour)},
		{ID: "f1", Repeat: repeatOnce, At: at(9, 0), Prompt: "y", Enabled: true},
		{ID: "f2", Repeat: repeatOnce, At: at(10, 0), Title: "看部署", Prompt: "z", Enabled: true}}
	if err := h1.saveLocked(sc, sc.tasks); err != nil {
		t.Fatal(err)
	}
	sc.mu.Unlock()

	h1.runDue(context.Background(), at(9, 0).Add(2*time.Second))
	runs := waitRuns(t, h1, 2) // no assistant machine here: the routine waits for one (skipped, schedule_ready.go), the
	for _, r := range runs {   // one-off is not held back and fails to start; both say so
		want := runSkipped
		if r.RefID == "f1" {
			want = runFailed
		}
		if r.Status != want || r.Error == "" || !r.Due.Equal(at(9, 0)) {
			t.Fatalf("run %+v", r)
		}
	}

	// The hub restarts at 09:01 and stays down until 11:45.
	h2 := boot()
	h2.runDue(context.Background(), at(9, 1))
	if n := len(h2.loadSchedule(context.Background()).runs); n != 2 {
		t.Fatalf("after the restart nothing is due again, have %d runs", n)
	}
	h2.runDue(context.Background(), at(11, 45))
	runs = waitRuns(t, h2, 4)
	byRef := map[string]ScheduleRun{}
	for _, r := range runs[2:] {
		byRef[r.RefID] = r
	}
	if r := byRef["r11"]; r.Status != runSkipped || !r.Due.Equal(at(11, 0)) {
		t.Fatalf("the 11:00 routine missed by 45 minutes is skipped: %+v", r)
	}
	if r := byRef["f2"]; r.Kind != "followup" || !r.Due.Equal(at(10, 0)) {
		t.Fatalf("the missed follow-up still runs: %+v", r)
	}
	late := Task{Repeat: repeatOnce, Title: "看部署", Prompt: "z"}
	if p := taskPrompt(late, at(10, 0), at(11, 45), loc, false, false, false); !strings.Contains(p, "晚了 1 小时") || !strings.Contains(p, "10-26 10:00") {
		t.Fatalf("a late follow-up is told so: %q", p)
	}
	if p := taskPrompt(late, at(10, 0), at(10, 0).Add(30*time.Second), loc, false, false, false); strings.Contains(p, "晚了") {
		t.Fatalf("an on-time follow-up is not: %q", p)
	}

	// A third boot sees the same history and the tasks' last status.
	h3 := boot()
	sc3 := h3.loadSchedule(context.Background())
	if len(sc3.runs) != 4 {
		t.Fatalf("reloaded %d runs", len(sc3.runs))
	}
	for _, r := range sc3.tasks {
		if r.LastStatus == "" {
			t.Fatalf("task %s has no last status", r.ID)
		}
	}
	if next := h3.nextDue(context.Background(), at(11, 46)); !next.Equal(at(9, 0).Add(24 * time.Hour)) {
		t.Fatalf("next due %v, want tomorrow 09:00", next)
	}
}

// flakyScheduleStore keeps settings in memory; reads fail while failRead is set.
type flakyScheduleStore struct {
	noPlans
	mu       sync.Mutex
	failRead bool
	data     map[string][]byte
	puts     int
}

func (s *flakyScheduleStore) Setting(_ context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failRead {
		return false, errors.New("database unavailable")
	}
	b, ok := s.data[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, out)
}

func (s *flakyScheduleStore) PutSetting(_ context.Context, key string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.data[key] = b
	s.puts++
	return nil
}

// failReads sets whether reads fail and starts counting writes again; the service's goroutines use the store
// meanwhile, so the test goes through the lock too.
func (s *flakyScheduleStore) failReads(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failRead, s.puts = fail, 0
}

// writes is how many writes there were since failReads.
func (s *flakyScheduleStore) writes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.puts
}

// stored is what is stored now, as text.
func (s *flakyScheduleStore) stored() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.data))
	for k, v := range s.data {
		out[k] = string(v)
	}
	return out
}

// A failed read used to mark the schedule loaded with the two default routines, and the next save wrote them over
// the stored routines and follow-ups (found in review, 2026-11-02). Now nothing is written, and nothing is changed in
// memory either, until a read succeeds: the tools and the app get an error, the scheduler skips its turn.
func TestScheduleReadFailureNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	store := &flakyScheduleStore{data: map[string][]byte{}}
	// Lists from before tasks were one: the first successful read moves them over (and writes).
	_ = store.PutSetting(ctx, routinesKey, []Routine{{ID: "rt_mine", Title: "我的", Prompt: "p", Time: "08:00", Enabled: true}})
	_ = store.PutSetting(ctx, followupsKey, []Followup{{ID: "fu_mine", At: time.Now().Add(time.Hour).UTC(), Prompt: "q"}})
	before := map[string]string{}
	for k, v := range store.data {
		before[k] = string(v)
	}
	store.puts, store.failRead = 0, true
	h := newServiceOn(t, newFakeHub(), store)
	m := "server"

	if _, err := h.toolScheduleRoutine(ctx, "", json.RawMessage(`{"title":"x","prompt":"y","time":"09:00"}`)); !errors.Is(err, errScheduleUnavailable) {
		t.Fatalf("schedule_routine while unreadable: %v", err)
	}
	if _, err := h.toolScheduleFollowup(ctx, m, "s1", json.RawMessage(`{"at":"30m","prompt":"y"}`)); !errors.Is(err, errScheduleUnavailable) {
		t.Fatalf("schedule_followup while unreadable: %v", err)
	}
	if _, err := h.deleteTask(ctx, "rt_mine"); !errors.Is(err, errScheduleUnavailable) {
		t.Fatalf("delete while unreadable: %v", err)
	}
	if got := h.describeSchedule(ctx, ""); got != errScheduleUnavailable.Error() {
		t.Fatalf("list_followups while unreadable: %q", got)
	}
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "rt_mine", Status: "ok", At: time.Now().UTC()})
	h.runDue(ctx, time.Now())
	if next := h.nextDue(ctx, time.Now()); !next.IsZero() {
		t.Fatalf("next due while unreadable: %v", next)
	}
	mux := h // the routes the hub relays
	for _, req := range []struct{ method, path, body string }{
		{"GET", "/api/v1/assistant/tasks", ""},
		{"GET", "/api/v1/assistant/routines", ""},
		{"GET", "/api/v1/assistant/followups", ""},
		{"GET", "/api/v1/assistant/runs", ""},
		{"POST", "/api/v1/assistant/tasks", `{"title":"喝水","prompt":"提醒我喝水","repeat":"once","at":"2099-01-01T09:00:00Z","enabled":true}`},
		{"PUT", "/api/v1/assistant/tasks/rt_mine", `{"title":"我的","prompt":"p","repeat":"daily","time":"08:00","enabled":false}`},
		{"PUT", "/api/v1/assistant/routines", `{"routines":[]}`},
		{"POST", "/api/v1/assistant/tasks/rt_mine/run", ""},
		{"POST", "/api/v1/assistant/tasks/rt_mine/fresh", ""},
		{"DELETE", "/api/v1/assistant/tasks/fu_mine", ""},
		{"DELETE", "/api/v1/assistant/followups/fu_mine", ""},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(req.method, req.path, strings.NewReader(req.body)))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s while unreadable: %d %s", req.method, req.path, rec.Code, rec.Body)
		}
	}
	if store.puts != 0 {
		t.Fatalf("%d writes while the schedule could not be read", store.puts)
	}
	for k, v := range store.data {
		if before[k] != string(v) {
			t.Fatalf("%s changed while unreadable: %s", k, v)
		}
	}

	// Once the database answers, the stored lists come back (not the defaults) and changes are saved again.
	store.mu.Lock()
	store.failRead = false
	store.mu.Unlock()
	tasks := h.tasksView(ctx, nil)
	if len(tasks) != 2 || tasks[0].ID != "rt_mine" || tasks[1].ID != "fu_mine" {
		t.Fatalf("tasks after recovery: %+v", tasks)
	}
	puts := store.puts
	if ok, err := h.deleteTask(ctx, "fu_mine"); !ok || err != nil {
		t.Fatalf("delete after recovery: %v, %v", ok, err)
	}
	if store.puts == puts {
		t.Fatal("a change after recovery is saved")
	}
}

// With nothing stored yet (a new hub), the defaults are the schedule and saving them is fine.
func TestScheduleDefaultsWhenNothingStored(t *testing.T) {
	ctx := context.Background()
	h := newServiceOn(t, newFakeHub(), &flakyScheduleStore{data: map[string][]byte{}})
	if err := h.scheduleReadable(ctx); err != nil {
		t.Fatal(err)
	}
	if tasks := h.tasksView(ctx, nil); len(tasks) != len(defaultRoutines("")) {
		t.Fatalf("defaults: %+v", tasks)
	}
}

// From the model configuration (docs/assistant/model-config.md): a task never carries a model of its own.
func TestTaskEditsCannotCreateModelOverrides(t *testing.T) {
	old := Task{ID: "one", Launcher: "old", Model: "old-model", Effort: "high"}
	in := Task{Title: "任务", Launcher: "requested", Model: "requested-model", Effort: "low", PermissionMode: "bypassPermissions"}
	task := editTask(old, in, old.CreatedAt)
	if task.Launcher != "" || task.Model != "" || task.Effort != "" || task.PermissionMode != "bypassPermissions" {
		t.Fatalf("task edit created a hidden model override: %+v", task)
	}
}

// A task switched on again, or retimed to earlier today, after its time has gone by: the run due before that is no
// run the hub missed, so nothing is recorded as "Persona was down and missed the time" and the next run comes as usual (#3816).
// Retimed to later today it still runs today; a hub that really was down still records the miss.
func TestNoMissRecordedForARunBeforeTheTaskWasArmed(t *testing.T) {
	f := readyHub() // a set-up computer: a skip here is a missed time, not one for want of a computer
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	wed := func(hh, mm int) time.Time { return time.Date(2026, 11, 4, hh, mm, 0, 0, loc) }
	off := Task{ID: "r", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Enabled: false, LastRun: wed(9, 0).Add(-5 * 24 * time.Hour)}
	on := off
	on.Enabled = true
	rearm(off, &on, wed(10, 0)) // switched on at 10:00, an hour after its time
	moved := Task{ID: "m", Title: "午报", Prompt: "y", Repeat: repeatDaily, Time: "11:00", Enabled: true, LastRun: wed(11, 0).Add(-24 * time.Hour)}
	earlier := moved
	earlier.Time = "09:00"
	rearm(moved, &earlier, wed(10, 0)) // at 10:00, 11:00 → 09:00
	later := Task{ID: "l", Title: "晚报", Prompt: "z", Repeat: repeatDaily, Time: "09:00", Enabled: true, LastRun: wed(9, 0).Add(-24 * time.Hour)}
	retimed := later
	retimed.Time = "10:20"
	rearm(later, &retimed, wed(10, 0)) // moved to later today
	down := Task{ID: "d", Title: "日报", Prompt: "w", Repeat: repeatDaily, Time: "09:00", Enabled: true, LastRun: wed(9, 0).Add(-24 * time.Hour)}
	setTasks(h, on, earlier, retimed, down)

	h.runDue(context.Background(), wed(10, 1))
	h.sched.inflight.Wait()
	for _, id := range []string{"r", "m"} {
		if r, _ := taskByID(h, id); r.LastStatus == runSkipped || !r.LastRun.Before(wed(10, 0)) {
			t.Fatalf("%s: armed after its time, nothing is due or missed today: %+v", id, r)
		}
	}
	if r, _ := taskByID(h, "d"); r.LastStatus != runSkipped {
		t.Fatalf("a task the hub really missed still records it: %+v", r)
	}
	sc := h.loadSchedule(context.Background())
	sc.mu.Lock()
	for _, run := range sc.runs {
		if run.RefID == "r" || run.RefID == "m" {
			sc.mu.Unlock()
			t.Fatalf("no run recorded for a task armed after its time: %+v", run)
		}
	}
	sc.mu.Unlock()

	h.runDue(context.Background(), wed(10, 21))
	h.sched.inflight.Wait()
	if r, _ := taskByID(h, "l"); !r.LastRun.Equal(wed(10, 21)) || r.LastStatus == runSkipped {
		t.Fatalf("retimed to later today, it runs today: %+v", r)
	}
	// Thursday they run as ever.
	thu := wed(9, 1).Add(24 * time.Hour)
	h.runDue(context.Background(), thu)
	h.sched.inflight.Wait()
	for _, id := range []string{"r", "m"} {
		if r, _ := taskByID(h, id); !r.LastRun.Equal(thu) || r.LastStatus == runSkipped {
			t.Fatalf("%s runs the next day: %+v", id, r)
		}
	}
}

// rearm marks a daily task only when it starts counting anew: created, switched on, retimed or given other days.
func TestRearm(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	base := Task{Repeat: repeatDaily, Time: "09:00", Weekdays: []int{1, 2}, Enabled: true}
	for _, c := range []struct {
		name string
		old  Task
		edit func(*Task)
		want bool
	}{
		{"new", Task{}, func(*Task) {}, true},
		{"switched on", Task{Repeat: repeatDaily, Time: "09:00", Weekdays: []int{1, 2}}, func(*Task) {}, true},
		{"retimed", base, func(t *Task) { t.Time = "08:00" }, true},
		{"other days", base, func(t *Task) { t.Weekdays = []int{1} }, true},
		{"prompt only", base, func(t *Task) { t.Prompt = "新的" }, false},
		{"switched off", base, func(t *Task) { t.Enabled = false }, false},
	} {
		t2 := base
		c.edit(&t2)
		rearm(c.old, &t2, now)
		if got := !t2.ArmedAt.IsZero(); got != c.want {
			t.Errorf("%s: armed %v, want %v", c.name, got, c.want)
		}
	}
}

// A task created, or retimed, during its own minute still runs today: at 10:05:20 the owner sets one for 10:05 to
// try it (review of #3915: armed at 10:05:20 it waited until tomorrow). Through the app's routes, as the app does it.
func TestTaskArmedDuringItsMinuteRunsToday(t *testing.T) {
	f := readyHub() // a set-up computer: a skip here is a missed time, not one for want of a computer
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	at := time.Date(2026, 11, 4, 10, 5, 20, 0, loc)

	created := Task{ID: "c", Title: "试一下", Prompt: "x", Repeat: repeatDaily, Time: "10:05", Enabled: true}
	rearm(Task{}, &created, at) // created at 10:05:20
	ran := Task{ID: "r", Title: "早报", Prompt: "y", Repeat: repeatDaily, Time: "09:00", Enabled: true, LastRun: at.Add(-24 * time.Hour)}
	retimed := ran
	retimed.Time = "10:05"
	rearm(ran, &retimed, at) // retimed to 10:05 at 10:05:20
	setTasks(h, created, retimed)

	h.runDue(context.Background(), at.Add(time.Second))
	h.sched.inflight.Wait()
	for _, id := range []string{"c", "r"} {
		if r, _ := taskByID(h, id); !r.LastRun.Equal(at.Add(time.Second)) || r.LastStatus == runSkipped {
			t.Fatalf("%s, armed during its minute, runs today: %+v", id, r)
		}
	}

	// The app's PUT arms it the same way: switched on during its minute, it runs at once.
	h2 := newStorelessService(t, newFakeHub())
	if sec := time.Now().Second(); sec >= 57 {
		time.Sleep(time.Duration(61-sec) * time.Second) // not across a minute's end
	}
	now := time.Now().In(time.Local)
	hhmm := now.Format("15:04")
	setTasks(h2, Task{ID: "a", Title: "试", Prompt: "z", Repeat: repeatDaily, Time: hhmm, Enabled: false, LastRun: now.Add(-48 * time.Hour)})
	rec := httptest.NewRecorder()
	h2.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/v1/assistant/tasks/a",
		strings.NewReader(`{"title":"试","prompt":"z","repeat":"daily","time":"`+hhmm+`","enabled":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body)
	}
	a, _ := taskByID(h2, "a")
	if a.ArmedAt.IsZero() || !a.ArmedAt.Before(now.Truncate(time.Minute)) {
		t.Fatalf("armed from the start of the minute: %v (now %v)", a.ArmedAt, now)
	}
	if next := nextRun(a, a.ArmedAt, time.Local); next.After(now) {
		t.Fatalf("switched on during its minute, its run is due now, not %v", next)
	}
}

// The snapshot tells the morning / evening briefing what went wrong overnight without a tool call (#4124): each daily task with how
// its last run went, and the scheduled runs that failed or were skipped since yesterday 18:00, newest first, bounded.
func TestSnapshotScheduleTellsMisses(t *testing.T) {
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 11, 5, 8, 30, 0, 0, loc)
	setTasks(h,
		Task{ID: "m", Title: "早报", Repeat: repeatDaily, Time: "09:00", Weekdays: []int{1, 2, 3, 4, 5}, Enabled: true,
			LastRun: now.Add(-23 * time.Hour), LastStatus: runOK},
		Task{ID: "p", Title: "巡检", Repeat: repeatDaily, Time: "02:00", Enabled: true, LastRun: now.Add(-6 * time.Hour), LastStatus: runFailed,
			LastError: "API Error: 503 upstream attempt budget is exhausted"},
		Task{ID: "off", Title: "关了的", Repeat: repeatDaily, Time: "07:00", Enabled: false})
	sc := h.loadSchedule(context.Background())
	sc.mu.Lock()
	sc.runs = []ScheduleRun{
		{RefID: "p", Title: "巡检", At: now.Add(-40 * time.Hour), Status: runFailed, Error: "前天的，不算"},
		{RefID: "m", Title: "早报", At: now.Add(-23 * time.Hour), Status: runOK},
		{RefID: "p", Title: "巡检", At: now.Add(-6 * time.Hour), Status: runFailed, Error: "API Error: 503 upstream attempt budget is exhausted"},
	}
	for i := 0; i < 7; i++ {
		sc.runs = append(sc.runs, ScheduleRun{RefID: "x", Title: "值守", At: now.Add(-time.Duration(5-i/2) * time.Hour), Status: runSkipped, Error: "Persona 停机，错过了时间"})
	}
	sc.mu.Unlock()

	_, routines, routinesTotal, misses, total := h.snapshotSchedule(context.Background(), loc, now)
	if len(routines) != 2 || routinesTotal != 2 || routines[0].Rule != "工作日 09:00" || routines[0].LastStatus != runOK ||
		routines[1].LastStatus != runFailed || !strings.Contains(routines[1].LastWhy, "API Error: 503") {
		t.Fatalf("routines: %+v (%d)", routines, routinesTotal)
	}
	if total != 8 || len(misses) != 6 {
		t.Fatalf("misses: %d of %d, want 6 of 8 (the one before yesterday 18:00 left out)", len(misses), total)
	}
	if !misses[0].Skipped || misses[0].Label != "值守" {
		t.Fatalf("newest first: %+v", misses[0])
	}
	text := prompts.RenderSnapshot(prompts.Snapshot{Now: now, Zone: "Asia/Shanghai", Routines: routines, RoutinesTotal: routinesTotal,
		Missed: misses, MissedTotal: total})
	if !strings.Contains(text, "没成功的定时任务") || !strings.Contains(text, "值守跳过了") || !strings.Contains(text, "等 8 次") ||
		!strings.Contains(text, "早报（工作日 09:00），上次11-04 09:30 成功") || !strings.Contains(text, "上次11-05 02:30 失败：API Error: 503") {
		t.Fatalf("snapshot: %s", text)
	}
	for _, r := range misses {
		if len([]rune(r.Why)) > 61 {
			t.Fatalf("a reason is kept short: %q", r.Why)
		}
	}
}

// Runs are kept in the order they ended and At is when they started: a long 17:59 run recorded at 18:05 sits after an
// 18:01 failure, and must not hide it. More than eight daily tasks say how many there are (#4124 review).
func TestSnapshotScheduleLateRecordedRunAndTotals(t *testing.T) {
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	h := newStorelessService(t, f)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Date(2026, 11, 5, 8, 30, 0, 0, loc)
	at := func(d, hh, mm int) time.Time { return time.Date(2026, 11, d, hh, mm, 0, 0, loc) }
	var tasks []Task
	for i := 0; i < 10; i++ {
		tasks = append(tasks, Task{ID: fmt.Sprintf("d%d", i), Title: fmt.Sprintf("日报%d", i), Repeat: repeatDaily, Time: "09:00", Enabled: true})
	}
	setTasks(h, tasks...)
	sc := h.loadSchedule(context.Background())
	sc.mu.Lock()
	sc.runs = []ScheduleRun{
		{RefID: "x", Title: "巡检", At: at(4, 18, 1), Status: runFailed, Error: "18:01 的失败"},        // ended first
		{RefID: "y", Title: "长任务", At: at(4, 17, 59), Status: runOK},                              // started before 18:00, recorded later
		{RefID: "z", Title: "夜里", At: at(5, 2, 0), Status: runSkipped, Error: "Persona 停机，错过了时间"}, // newest
	}
	sc.mu.Unlock()

	_, routines, routinesTotal, misses, total := h.snapshotSchedule(context.Background(), loc, now)
	if len(routines) != 8 || routinesTotal != 10 {
		t.Fatalf("daily tasks: %d shown of %d, want 8 of 10", len(routines), routinesTotal)
	}
	if total != 2 || len(misses) != 2 || misses[0].Label != "夜里" || misses[1].Label != "巡检" {
		t.Fatalf("misses: %+v (%d), want the 02:00 skip then the 18:01 failure", misses, total)
	}
	text := prompts.RenderSnapshot(prompts.Snapshot{Now: now, Zone: "Asia/Shanghai", Routines: routines, RoutinesTotal: routinesTotal,
		Missed: misses, MissedTotal: total})
	if !strings.Contains(text, "等 10 个") {
		t.Fatalf("snapshot says how many daily tasks there are: %s", text)
	}
}

// A hub set up before kept its morning / evening briefing prompts as they were saved: one byte for byte an earlier default gets the
// current default when the schedule is read, so the morning briefing puts what waits on the owner first. One the owner changed,
// even by a character, and another task holding the old text, are left alone.
func TestUpgradeDefaultRoutines(t *testing.T) {
	formerM, formerE := prompts.FormerMorningRoutines(), prompts.FormerEveningRoutines()
	edited := formerM[0] + "\n另外提醒我喝水。"
	tasks := []Task{
		{ID: "morning", Repeat: repeatDaily, Prompt: formerM[0]},
		{ID: "evening", Repeat: repeatDaily, Prompt: formerE[0]},
		{ID: "rt_mine", Repeat: repeatDaily, Prompt: formerM[1]},
	}
	if !upgradeDefaultRoutines(tasks) {
		t.Fatal("nothing upgraded")
	}
	if tasks[0].Prompt != prompts.RenderMorningRoutine() || tasks[1].Prompt != prompts.RenderEveningRoutine() {
		t.Fatalf("the defaults are current: %q / %q", tasks[0].Prompt, tasks[1].Prompt)
	}
	if tasks[2].Prompt != formerM[1] {
		t.Fatal("another task holding the old text is not the default one: left alone")
	}
	if upgradeDefaultRoutines(tasks) {
		t.Fatal("current prompts are not upgraded again")
	}
	mine := []Task{{ID: "morning", Repeat: repeatDaily, Prompt: edited}, {ID: "evening", Repeat: repeatDaily, Prompt: formerE[0] + " "}}
	if upgradeDefaultRoutines(mine) || mine[0].Prompt != edited {
		t.Fatalf("an edited prompt is never touched: %q", mine[0].Prompt)
	}
	for _, p := range append(formerM, formerE...) {
		if !strings.HasPrefix(p, "给我今天的早报") && !strings.HasPrefix(p, "今天的晚间回顾") {
			t.Fatalf("a former default is what hubs saved: %q", p)
		}
	}

	// Read from the store: the upgrade is saved.
	store := &flakyScheduleStore{data: map[string][]byte{}}
	if err := store.PutSetting(context.Background(), tasksKey, []Task{{ID: "morning", Title: "早报", Repeat: repeatDaily, Time: "09:00",
		Enabled: true, Prompt: formerM[1]}}); err != nil {
		t.Fatal(err)
	}
	h := newServiceOn(t, newFakeHub(), store)
	if got, _ := taskByID(h, "morning"); got.Prompt != prompts.RenderMorningRoutine() {
		t.Fatalf("loaded: %q", got.Prompt)
	}
	var saved []Task
	if _, err := store.Setting(context.Background(), tasksKey, &saved); err != nil || len(saved) != 1 || saved[0].Prompt != prompts.RenderMorningRoutine() {
		t.Fatalf("saved: %+v %v", saved, err)
	}
}
