package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Not-ready skips are neutral even between genuine failures, and the distinction survives storage in any locale.
func TestNotReadySkipsNeitherCountNorEndAStreak(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			st := scheduleStore(t)
			f := newFakeHub()
			f.hello.Locale = string(lang)
			s := newServiceOn(t, f, st)
			setTasks(s, Task{ID: "p", Title: "Patrol", Repeat: repeatDaily, Time: "02:00", Enabled: true})
			outcome(s, "p", runFailed, "API Error: 503")
			outcome(s, "p", runSkipped, "missed")
			for _, why := range []error{errNotReady, fmt.Errorf("readiness: %w", errNotReady), errNotReady, errNotReady} {
				if err := s.skipRun(ScheduleRun{Kind: "routine", RefID: "p"}, why); !errors.Is(err, errNotReady) {
					t.Fatal(err)
				}
				if p := f.pushed(); len(p) != 0 {
					t.Fatalf("not-ready skips must not turn two failures into a streak notification: %+v", p)
				}
			}
			s = newServiceOn(t, f, st)
			sc := s.loadSchedule(context.Background())
			sc.mu.Lock()
			n, _, told, in := runStreak(sc.runs, "p", sc.runs[len(sc.runs)-1].ID)
			sc.mu.Unlock()
			if n != 2 || told || in {
				t.Fatalf("after restart only two real failures count: n=%d told=%v in=%v", n, told, in)
			}
			_ = s.skipRun(ScheduleRun{Kind: "routine", RefID: "p"}, errTaskBusy)
			if r := lastRunOf(t, s, "p"); r.NotReady || !r.StreakTold {
				t.Fatalf("an ordinary skip still completes the streak: %+v", r)
			}
			if p := f.pushed(); len(p) != 1 || p[0].Title != i18n.T(lang, "assistant-schedule.streak", i18n.Args{"name": "Patrol", "count": 3}) {
				t.Fatalf("three genuine failures are still told once: %+v", p)
			}
		})
	}
}

// outcome records one run of task ref as it ends (recordRun is where the history and the streak are kept).
func outcome(h *Service, ref, status, why string) {
	h.recordRun(ScheduleRun{Kind: "routine", RefID: ref, Title: ref, Status: status, Error: why})
}

// The user's ask (#4658): a repeating task that keeps failing or being skipped was only a line in the run history.
// At streakLimit runs in a row that did not go through, the user gets one push naming the task, the count and the
// last reason; nothing more until it goes through once.
func TestARepeatingTaskThatKeepsNotGoingThroughIsToldOnce(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	setTasks(h, Task{ID: "p", Title: "夜间巡检", Prompt: "巡检", Repeat: repeatDaily, Time: "02:00", Enabled: true})

	outcome(h, "p", runFailed, "API Error: 503")
	outcome(h, "p", runSkipped, "Persona 停机，错过了时间")
	if p := n.pushed(); len(p) != 0 {
		t.Fatalf("two in a row is still the history's business: %+v", p)
	}

	outcome(h, "p", runFailed, "API Error: 503 upstream attempt budget is exhausted")
	p := n.pushed()
	if len(p) != 1 || !strings.Contains(p[0].Title, "夜间巡检") || !strings.Contains(p[0].Title, "3") ||
		!strings.Contains(p[0].Body, "API Error: 503 upstream attempt budget is exhausted") {
		t.Fatalf("one push naming the task, how many and the last reason: %+v", p)
	}

	// A fourth and a fifth: no second push. The streak only grows until a run goes through.
	outcome(h, "p", runFailed, "x")
	outcome(h, "p", runSkipped, "y")
	if p := n.pushed(); len(p) != 1 {
		t.Fatalf("nothing more until it goes through: %+v", p)
	}

	// Its automatic retry: one that fails too is the same failure over again and tells nothing, and one that went
	// through ends the streak (the task worked) and tells nothing either.
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "p", Status: runFailed, Retried: true, Error: "retry"})
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "p", Status: runOK, Retried: true})
	if p := n.pushed(); len(p) != 1 {
		t.Fatalf("a retry is quiet whichever way it went: %+v", p)
	}

	// Going through once ends it: three more in a row is news again.
	outcome(h, "p", runOK, "")
	if p := n.pushed(); len(p) != 1 {
		t.Fatalf("a run that went through is quiet: %+v", p)
	}
	outcome(h, "p", runFailed, "a")
	outcome(h, "p", runFailed, "b")
	outcome(h, "p", runFailed, "c")
	if p := n.pushed(); len(p) != 2 || !strings.Contains(p[1].Body, "c") {
		t.Fatalf("after a success the next three in a row are told again: %+v", p)
	}
}

// Only the runs that were meant to happen count: "Run now" is answered in the app (and a failure of it there), and a
// one-off is not a repeating task (its failure is pushed and retried by the run itself).
func TestAnEndTimeDoesNotCountTowardTheStreak(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	setTasks(h, Task{ID: "p", Title: "夜间巡检", Prompt: "巡检", Repeat: repeatDaily, Time: "02:00", Enabled: false})
	reason := i18n.T(i18n.Default, "assistant-schedule.until")
	for range streakLimit - 1 {
		h.recordRun(ScheduleRun{Kind: "routine", RefID: "p", Title: "夜间巡检", Status: runSkipped, Error: "missed"})
	}
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "p", Title: "夜间巡检", Status: runSkipped, Error: reason, Ended: true})
	if p := n.pushed(); len(p) != 0 {
		t.Fatalf("reaching the end time is not a missed run: %+v", p)
	}
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "p", Title: "夜间巡检", Status: runSkipped, Error: "missed again"})
	if p := n.pushed(); len(p) != 0 {
		t.Fatalf("the end time should have ended the streak, one skip after it is not three: %+v", p)
	}
}

func TestOnlyRepeatingTasksAndTheirScheduledRunsCountTowardsTheStreak(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	setTasks(h,
		Task{ID: "p", Title: "夜间巡检", Prompt: "巡检", Repeat: repeatDaily, Time: "02:00", Enabled: true},
		Task{ID: "f", Title: "提醒", Prompt: "提醒", Repeat: repeatOnce, Enabled: true})

	for i := 0; i < streakLimit; i++ {
		h.recordRun(ScheduleRun{Kind: "routine", RefID: "p", Title: "夜间巡检", Status: runFailed, Error: "manual", Manual: true})
		h.recordRun(ScheduleRun{Kind: "followup", RefID: "f", Title: "提醒", Status: runFailed, Error: "one-off"})
	}
	if p := n.pushed(); len(p) != 0 {
		t.Fatalf("立即运行 and one-off runs are not a streak: %+v", p)
	}
	outcome(h, "p", runFailed, "scheduled")
	if p := n.pushed(); len(p) != 0 {
		t.Fatalf("one scheduled failure is not a streak either: %+v", p)
	}
}

// A task skipped night after night because its conversation is still on the last run is a streak like any other: a
// last run stuck working is not pushed anywhere else (#4506 only tells a wait on the user), so the user is told once.
// And an automatic retry that went through ends the streak: the task worked.
func TestABusyStreakIsToldAndARetryThatWorkedEndsIt(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	setTasks(h, Task{ID: "p", Title: "夜间巡检", Prompt: "巡检", Repeat: repeatDaily, Time: "02:00", Enabled: true})
	for i := 0; i < streakLimit; i++ {
		h.recordRun(ScheduleRun{Kind: "routine", RefID: "p", Title: "夜间巡检", Status: runSkipped, Error: "上一轮还在运行，这次跳过",
			MachineID: "server", SessionID: "s1"})
	}
	if p := n.pushed(); len(p) != 1 {
		t.Fatalf("three busy skips in a row are told once: %+v", p)
	}
	h.recordRun(ScheduleRun{Kind: "routine", RefID: "p", Status: runOK, Retried: true})
	outcome(h, "p", runFailed, "a")
	outcome(h, "p", runFailed, "b")
	if p := n.pushed(); len(p) != 1 {
		t.Fatalf("a retry that went through ended the streak, two more is not three: %+v", p)
	}
}

// Runs are recorded when they start and turned failed when their turn ends, so the count can jump past streakLimit
// (#4785): two failures; the third is sent and recorded ok; the fourth is skipped (the conversation is still on the
// third); then the third's turn ends in an error. That is four in a row at once, never exactly three: told then, and
// once only.
func TestAStreakThatJumpsPastTheLimitIsStillToldOnce(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	setTasks(h, Task{ID: "p", Title: "值守", Prompt: "巡检", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})

	outcome(h, "p", runFailed, "API Error: 503")
	outcome(h, "p", runFailed, "API Error: 503")
	third := h.recordRun(ScheduleRun{Kind: "routine", RefID: "p", Title: "值守", Status: runOK})
	outcome(h, "p", runSkipped, "上一轮还在运行，这次跳过")
	if p := n.pushed(); len(p) != 0 {
		t.Fatalf("the third went through as far as anyone knows: %+v", p)
	}

	if !h.updateRun(ScheduleRun{ID: third, RefID: "p"}, runFailed, "API Error: 529 overloaded") {
		t.Error("updateRun says the streak notice went out")
	}
	p := n.pushed()
	if len(p) != 1 || !strings.Contains(p[0].Title, "值守") || !strings.Contains(p[0].Body, "529") {
		t.Fatalf("four in a row is told, with the reason of the run that made it: %+v", p)
	}

	// The same streak growing on is not told again.
	outcome(h, "p", runFailed, "x")
	outcome(h, "p", runSkipped, "y")
	if p := n.pushed(); len(p) != 1 {
		t.Fatalf("one streak, one notice: %+v", p)
	}
	// A run that goes through ends it; the next three in a row are a new streak, told again.
	outcome(h, "p", runOK, "")
	outcome(h, "p", runFailed, "a")
	outcome(h, "p", runFailed, "b")
	outcome(h, "p", runFailed, "c")
	if p := n.pushed(); len(p) != 2 || !strings.Contains(p[1].Body, "c") {
		t.Fatalf("a new streak is told once more: %+v", p)
	}
}

// In a muted conversation the streak notice stays quiet like the conversation's other pushes, but the run's own
// "Scheduled task failed" still goes out, as it did before the streak notice (#4785): it is not left out for a notice that
// was never sent.
func TestAFailureInAMutedConversationIsStillPushed(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	h.runRetry = 50 * time.Millisecond
	h.turnPoll = 200 * time.Millisecond
	h.fleet.mutes = &Pins{Spaces: []string{}, Sessions: []string{"server/s1"}}
	n.items = []proto.Item{{Role: "assistant", Kind: "text", Text: "API Error: 503 upstream attempt budget is exhausted"}}
	setTasks(h, Task{ID: "p", Title: "值守", Prompt: "go", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(-time.Minute),
		Enabled: true, MachineID: "server", CreatedBy: "assistant"})
	for i := 0; i < streakLimit-1; i++ {
		h.recordRun(ScheduleRun{Kind: "routine", RefID: "p", Title: "值守", Status: runFailed, Error: "API Error: 503",
			MachineID: "server", SessionID: "s1"})
	}

	n.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, Assistant: true,
		Cwd: "/home/me/.persona/assistant"})
	if _, err := runNow(t, h, "p"); err != nil {
		t.Fatal(err)
	}
	// The turn ends after the first poll saw it working (started after the run went, so the watcher's polls line up).
	go func() {
		time.Sleep(3 * h.turnPoll / 2)
		n.mu.Lock()
		for i := range n.sessions["server"] {
			n.sessions["server"][i].Status, n.sessions["server"][i].State = proto.StatusOffline, proto.StateIdle
		}
		n.mu.Unlock()
	}()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if slices.ContainsFunc(n.pushed(), func(p Notification) bool { return p.Title == "定时任务失败：值守" }) {
			if p := n.pushed(); slices.ContainsFunc(p, func(p Notification) bool { return strings.Contains(p.Title, "没跑成") }) {
				t.Fatalf("the streak notice stays quiet in a muted conversation: %+v", p)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the third failure in a row was not pushed at all: %+v", n.pushed())
}

// A streak already over the limit when the hub was upgraded was told by #4670's code, which marked nothing (#4881).
// Its next failure is not a new streak: no second notice. A run that goes through still starts one that is told.
func TestAStreakToldBeforeMarksExistedIsNotToldAgain(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	sc := setTasks(h, Task{ID: "p", Title: "值守", Prompt: "巡检", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	sc.mu.Lock()
	for i := range streakLimit + 1 { // the history as the old code left it: told at the third, no StreakTold
		sc.runs = append(sc.runs, ScheduleRun{ID: "run_old" + string(rune('a'+i)), Kind: "routine", RefID: "p", Title: "值守",
			Status: runFailed, Error: "API Error: 503"})
	}
	sc.mu.Unlock()

	outcome(h, "p", runFailed, "API Error: 503")
	if p := n.pushed(); len(p) != 0 {
		t.Fatalf("the fifth in a row of a streak told before the upgrade: %+v", p)
	}
	outcome(h, "p", runOK, "")
	for range streakLimit {
		outcome(h, "p", runFailed, "again")
	}
	if p := n.pushed(); len(p) != 1 || !strings.Contains(p[0].Body, "again") {
		t.Fatalf("a new streak after a run that went through is told: %+v", p)
	}
}

// The history keeps maxRuns runs for all tasks together (#4881): a task that keeps failing outlives the run that told
// its streak. The streak was over the limit before each of those runs, so it is not told again.
func TestAStreakWhoseToldRunFellOutOfTheHistoryIsNotToldAgain(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	sc := setTasks(h, Task{ID: "p", Title: "值守", Prompt: "巡检", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})

	for range streakLimit {
		outcome(h, "p", runFailed, "API Error: 503")
	}
	if p := n.pushed(); len(p) != 1 {
		t.Fatalf("three in a row are told: %+v", p)
	}
	for range maxRuns {
		outcome(h, "p", runFailed, "API Error: 503")
	}
	if p := n.pushed(); len(p) != 1 {
		t.Fatalf("the same streak is told once, its mark gone or not: %d pushes", len(p))
	}
	sc.mu.Lock()
	marked := slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.StreakTold })
	kept := len(sc.runs)
	sc.mu.Unlock()
	if marked || kept != maxRuns {
		t.Fatalf("the told run is out of the %d kept (marked %v, kept %d)", maxRuns, marked, kept)
	}
}
