package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

func untilService(t *testing.T) (*Service, *time.Location) {
	t.Helper()
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return newStorelessService(t, f), loc
}

func scheduleRuns(s *Service) []ScheduleRun {
	sc := s.loadSchedule(context.Background())
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return append([]ScheduleRun(nil), sc.runs...)
}

func TestUntilExpiresAnIntervalThatIsAlreadyDue(t *testing.T) {
	h, loc := untilService(t)
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, loc)
	at := now.Add(-time.Hour)
	end := now.Add(-time.Minute)
	setTasks(h, Task{ID: "p", Title: "巡检", Prompt: "看看", Repeat: repeatInterval, EveryMinutes: 30,
		At: at, Until: end.UTC(), Enabled: true})

	h.runDue(context.Background(), now)

	got, ok := taskByID(h, "p")
	reason := i18n.T(i18n.Default, "assistant-schedule.until")
	if !ok || got.Enabled || !got.At.Equal(at) || !got.LastRun.Equal(now.UTC()) || got.LastStatus != runSkipped || got.LastError != reason {
		t.Fatalf("an interval whose end time has passed is turned off, not run: %+v", got)
	}
	runs := scheduleRuns(h)
	if len(runs) != 1 || runs[0].Status != runSkipped || runs[0].Error != reason || runs[0].RefID != "p" ||
		runs[0].Kind != got.kind() || runs[0].Title != "巡检" || !runs[0].Due.Equal(end.UTC()) ||
		!runs[0].At.Equal(now.UTC()) || !runs[0].of.Equal(now.UTC()) || !runs[0].Ended {
		t.Fatalf("skipped run: %+v", runs)
	}
}

func TestUntilExpiresADailyThatIsNotDueYet(t *testing.T) {
	h, loc := untilService(t)
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, loc)
	last := time.Date(2026, 11, 9, 9, 0, 0, 0, loc)
	end := time.Date(2026, 11, 9, 9, 30, 0, 0, loc)
	setTasks(h, Task{ID: "d", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Enabled: true,
		LastRun: last.UTC(), Until: end.UTC()})

	h.runDue(context.Background(), now)
	h.sched.inflight.Wait()

	got, _ := taskByID(h, "d")
	reason := i18n.T(i18n.Default, "assistant-schedule.until")
	if got.Enabled || got.LastStatus != runSkipped || got.LastError != reason {
		t.Fatalf("a daily task past its end time is turned off even when today's run already happened: %+v", got)
	}
	runs := scheduleRuns(h)
	if len(runs) != 1 || runs[0].Status != runSkipped || runs[0].Error != reason || !runs[0].Due.Equal(end.UTC()) || runs[0].RefID != "d" {
		t.Fatalf("skipped run: %+v", runs)
	}
}

func TestUntilInTheFutureStillRuns(t *testing.T) {
	h, _ := untilService(t)
	now := time.Now()
	at := now.Add(-time.Minute)
	end := now.Add(time.Hour).UTC()
	setTasks(h, Task{ID: "p", Title: "巡检", Prompt: "w", Repeat: repeatInterval, EveryMinutes: 30,
		At: at, Until: end, Enabled: true})

	h.runDue(context.Background(), now)
	h.sched.inflight.Wait()

	got, _ := taskByID(h, "p")
	if !got.Enabled || !got.At.Equal(at.Add(30*time.Minute)) || !got.Until.Equal(end) {
		t.Fatalf("a due interval whose end time is still ahead runs and stays on: %+v", got)
	}
	reason := i18n.T(i18n.Default, "assistant-schedule.until")
	for _, r := range scheduleRuns(h) {
		if r.Error == reason {
			t.Fatalf("it was expired instead of run: %+v", r)
		}
	}
}

func TestUntilAtTheExactEndTimeDoesNotRun(t *testing.T) {
	h, _ := untilService(t)
	now := time.Now().UTC().Truncate(time.Second)
	at := now.Add(-time.Minute)
	setTasks(h, Task{ID: "p", Title: "巡检", Prompt: "w", Repeat: repeatInterval, EveryMinutes: 30,
		At: at, Until: now, Enabled: true})

	h.runDue(context.Background(), now)

	got, _ := taskByID(h, "p")
	if got.Enabled || !got.At.Equal(at) || got.LastStatus != runSkipped {
		t.Fatalf("a run at exactly the end time does not go: %+v", got)
	}
}

func TestUntilDoesNotStopAOneOff(t *testing.T) {
	h, _ := untilService(t)
	h.runRetry = time.Hour // the failed start's retry is not this test
	now := time.Now()
	at := now.Add(-time.Minute)
	setTasks(h, Task{ID: "f", Title: "提醒", Prompt: "y", Repeat: repeatOnce, At: at,
		Until: now.Add(-time.Hour).UTC(), Enabled: true})

	h.runDue(context.Background(), now)
	h.sched.inflight.Wait()

	got, _ := taskByID(h, "f")
	reason := i18n.T(i18n.Default, "assistant-schedule.until")
	if got.Enabled || got.LastError == reason || got.LastStatus == runSkipped || !got.At.Equal(at) {
		t.Fatalf("a one-off with an end time still runs, and is turned off because it ran: %+v", got)
	}
	runs := scheduleRuns(h)
	if len(runs) == 0 {
		t.Fatal("the one-off was not started")
	}
	for _, r := range runs {
		if r.Error == reason {
			t.Fatalf("expiry reason on a one-off: %+v", r)
		}
	}
}

func TestUntilEditTaskDoesNotCopyIt(t *testing.T) {
	now := time.Now()
	kept := now.Add(3 * time.Hour).UTC()
	old := Task{ID: "p", Repeat: repeatDaily, Time: "09:00", Title: "早报", Prompt: "x", Enabled: true, Until: kept}
	in := old
	in.Until = time.Time{}
	in.Title = "晚报"
	got := editTask(old, in, now)
	if !got.Until.Equal(kept) || got.Title != "晚报" {
		t.Fatalf("a missing until must not wipe the stored one: %+v", got)
	}
}

func TestUntilThroughTheTaskRoutes(t *testing.T) {
	h := newStorelessService(t, newFakeHub())
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	base := `{"title":"巡检","prompt":"看看","repeat":"interval","every_minutes":30,"enabled":true`
	rec := do("POST", "/api/v1/assistant/tasks", base+`,"until":"2099-06-01T12:00:00+08:00"}`)
	var created struct{ Task Task }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &created) != nil || created.Task.ID == "" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	want := time.Date(2099, 6, 1, 4, 0, 0, 0, time.UTC)
	if !created.Task.Until.Equal(want) {
		t.Fatalf("stored until = %v, want %v", created.Task.Until, want)
	}
	id := created.Task.ID

	rec = do("PUT", "/api/v1/assistant/tasks/"+id, base+`}`)
	if rec.Code != 200 {
		t.Fatalf("put without until: %d %s", rec.Code, rec.Body)
	}
	if got, _ := taskByID(h, id); !got.Until.Equal(want) {
		t.Fatalf("omitting until wiped it: %+v", got)
	}

	rec = do("PUT", "/api/v1/assistant/tasks/"+id, base+`,"until":null}`)
	if rec.Code != 200 {
		t.Fatalf("clear: %d %s", rec.Code, rec.Body)
	}
	if got, _ := taskByID(h, id); !got.Until.IsZero() {
		t.Fatalf("null did not clear until: %+v", got)
	}

	future := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	rec = do("PUT", "/api/v1/assistant/tasks/"+id, base+`,"until":"`+future.Format(time.RFC3339)+`"}`)
	if rec.Code != 200 {
		t.Fatalf("future: %d %s", rec.Code, rec.Body)
	}
	if got, _ := taskByID(h, id); !got.Until.Equal(future) || got.Until.Location() != time.UTC {
		t.Fatalf("future until not stored in UTC: %v", got.Until)
	}

	past := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	before, _ := taskByID(h, id)
	rec = do("PUT", "/api/v1/assistant/tasks/"+id, `{"title":"不该保存","prompt":"看看","repeat":"interval","every_minutes":30,"enabled":true,"until":"`+past+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a past until on an enabled task: %d %s", rec.Code, rec.Body)
	}
	if got, _ := taskByID(h, id); got.Title != before.Title || !got.Until.Equal(before.Until) {
		t.Fatalf("the refused put was saved: %+v", got)
	}
	for _, bad := range []string{`""`, `1`, `{}`, `[]`} {
		rec = do("PUT", "/api/v1/assistant/tasks/"+id, base+`,"until":`+bad+`}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("until %s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	if got, _ := taskByID(h, id); !got.Until.Equal(before.Until) {
		t.Fatalf("a bad until changed it: %+v", got)
	}

	rec = do("PUT", "/api/v1/assistant/tasks/"+id, `{"title":"巡检","prompt":"看看","repeat":"interval","every_minutes":30,"enabled":false,"until":"`+past+`"}`)
	if rec.Code != 200 {
		t.Fatalf("a disabled task may keep a past until: %d %s", rec.Code, rec.Body)
	}
	pastAt, _ := time.Parse(time.RFC3339, past)
	if got, _ := taskByID(h, id); got.Enabled || !got.Until.Equal(pastAt) {
		t.Fatalf("disabled past until: %+v", got)
	}

	rec = do("POST", "/api/v1/assistant/tasks", `{"title":"喝水","prompt":"提醒我喝水","repeat":"once","at":"2099-01-01T09:00:00Z","enabled":true,"until":"2099-06-01T12:00:00Z"}`)
	var once struct{ Task Task }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &once) != nil || !once.Task.Until.IsZero() {
		t.Fatalf("a one-off drops until: %d %s", rec.Code, rec.Body)
	}

	rel := time.Now()
	rec = do("POST", "/api/v1/assistant/tasks", `{"title":"稍后","prompt":"看看","repeat":"interval","every_minutes":30,"enabled":true,"until":"2h"}`)
	var relative struct{ Task Task }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &relative) != nil ||
		relative.Task.Until.Before(rel.Add(2*time.Hour-time.Second)) || relative.Task.Until.After(rel.Add(2*time.Hour+time.Second)) {
		t.Fatalf("relative until: %d %s", rec.Code, rec.Body)
	}
}

func TestEnablingDropsAPastUntilTheClientLeftOut(t *testing.T) {
	h := newStorelessService(t, newFakeHub())
	do := func(method, path, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
		return rec
	}
	past := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	setTasks(h, Task{ID: "p", Title: "巡检", Prompt: "看看", Repeat: repeatInterval, EveryMinutes: 30, Enabled: false, Until: past})
	body := `{"title":"巡检","prompt":"看看","repeat":"interval","every_minutes":30,"enabled":true`
	rec := do("PUT", "/api/v1/assistant/tasks/p", body+"}")
	got, _ := taskByID(h, "p")
	if rec.Code != 200 || !got.Enabled || !got.Until.IsZero() {
		t.Fatalf("turning it back on without until: %d %+v %s", rec.Code, got, rec.Body)
	}
	// Saying the past time while turning it on is still refused, and nothing is saved.
	setTasks(h, Task{ID: "p", Title: "巡检", Prompt: "看看", Repeat: repeatInterval, EveryMinutes: 30, Enabled: false, Until: past})
	rec = do("PUT", "/api/v1/assistant/tasks/p", body+`,"until":"`+past.Format(time.RFC3339)+`"}`)
	got, _ = taskByID(h, "p")
	if rec.Code != http.StatusBadRequest || got.Enabled || !got.Until.Equal(past) {
		t.Fatalf("an explicit past until while enabling: %d %+v", rec.Code, got)
	}
}

func TestUntilLegacyRoutinesPutKeepsIt(t *testing.T) {
	h := newStorelessService(t, newFakeHub())
	end := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	setTasks(h, Task{ID: "morning", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Enabled: true, Until: end})
	rec := httptest.NewRecorder()
	body := `{"routines":[{"id":"morning","title":"早报","prompt":"x2","time":"08:30","enabled":true}]}`
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant/routines", strings.NewReader(body)))
	got, _ := taskByID(h, "morning")
	if rec.Code != 200 || got.Time != "08:30" || got.Prompt != "x2" || !got.Until.Equal(end) {
		t.Fatalf("routines put %d kept until %v: %+v", rec.Code, end, got)
	}
}

func TestUntilOnScheduleTools(t *testing.T) {
	h, loc := untilService(t)
	ctx := context.Background()
	setTasks(h)
	endText := "2099-06-01 12:00"
	want := time.Date(2099, 6, 1, 12, 0, 0, 0, loc).UTC()
	suffix := " Stops at " + endText + "."

	out, err := h.toolScheduleRoutine(ctx, "", json.RawMessage(`{"title":"周报","prompt":"起草周报","time":"17:00","until":"`+endText+`"}`))
	if err != nil || !strings.HasSuffix(out, suffix) || strings.Contains(strings.TrimSuffix(out, suffix), "Stops at") {
		t.Fatalf("set: %q %v", out, err)
	}
	sc := h.loadSchedule(ctx)
	sc.mu.Lock()
	rt := sc.tasks[len(sc.tasks)-1]
	sc.mu.Unlock()
	if rt.Repeat != repeatDaily || !rt.Until.Equal(want) {
		t.Fatalf("routine until: %+v", rt)
	}

	out, err = h.toolScheduleRoutine(ctx, "", json.RawMessage(`{"id":"`+rt.ID+`","title":"周报二"}`))
	if err != nil || !strings.HasSuffix(out, suffix) {
		t.Fatalf("omit keeps it, and the reply still says when it stops: %q %v", out, err)
	}
	if got, _ := taskByID(h, rt.ID); got.Title != "周报二" || !got.Until.Equal(want) {
		t.Fatalf("omit: %+v", got)
	}

	out, err = h.toolScheduleRoutine(ctx, "", json.RawMessage(`{"id":"`+rt.ID+`","until":null}`))
	if err != nil || strings.Contains(out, "Stops at") {
		t.Fatalf("null: %q %v", out, err)
	}
	if got, _ := taskByID(h, rt.ID); !got.Until.IsZero() {
		t.Fatalf("null did not clear: %+v", got)
	}

	plain, err := h.toolScheduleRoutine(ctx, "", json.RawMessage(`{"title":"无截止","prompt":"看看","time":"09:00"}`))
	if err != nil || strings.Contains(plain, "Stops at") {
		t.Fatalf("unset reply changed: %q %v", plain, err)
	}

	n := len(h.tasksView(ctx, nil))
	if _, err := h.toolScheduleRoutine(ctx, "", json.RawMessage(`{"title":"过期","prompt":"看看","time":"09:00","enabled":true,"until":"2000-01-01 00:00"}`)); err == nil || err.Error() != i18n.T(i18n.Default, "assistant-schedule.until_passed") {
		t.Fatalf("past until: %v", err)
	}
	if len(h.tasksView(ctx, nil)) != n {
		t.Fatal("a past until was saved")
	}

	out, err = h.toolScheduleFollowup(ctx, "server", "s1", json.RawMessage(`{"every":"30m","prompt":"看看部署","title":"部署巡检","until":"`+endText+`"}`))
	if err != nil || !strings.HasSuffix(out, suffix) {
		t.Fatalf("every: %q %v", out, err)
	}
	var patrol Task
	for _, task := range h.tasksView(ctx, nil) {
		if task.Title == "部署巡检" {
			patrol = task
		}
	}
	if patrol.Repeat != repeatInterval || !patrol.Until.Equal(want) {
		t.Fatalf("follow-up until: %+v", patrol)
	}

	out, err = h.toolScheduleFollowup(ctx, "server", "s1", json.RawMessage(`{"at":"2h","prompt":"提醒我喝水","title":"喝水","until":"`+endText+`"}`))
	if err != nil || strings.Contains(out, "Stops at") {
		t.Fatalf("a one-off ignores until: %q %v", out, err)
	}
	var once Task
	for _, task := range h.tasksView(ctx, nil) {
		if task.Title == "喝水" {
			once = task
		}
	}
	if once.Repeat != repeatOnce || !once.Until.IsZero() {
		t.Fatalf("one-off stored until: %+v", once)
	}
	if _, err := h.toolScheduleFollowup(ctx, "server", "s1", json.RawMessage(`{"at":"2h","prompt":"另一个提醒","until":"tomorrow"}`)); err != nil {
		t.Fatalf("a bad until on a one-off is ignored: %v", err)
	}

	n = len(h.tasksView(ctx, nil))
	if _, err := h.toolScheduleFollowup(ctx, "server", "s1", json.RawMessage(`{"every":"30m","prompt":"过期的","until":"2000-01-01 00:00"}`)); err == nil || err.Error() != i18n.T(i18n.Default, "assistant-schedule.until_passed") {
		t.Fatalf("past every: %v", err)
	}
	if len(h.tasksView(ctx, nil)) != n {
		t.Fatal("a past until on a new interval was saved")
	}

	// A run rescheduling its own task: omit keeps, null clears, a string sets, a past one saves nothing.
	kept := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	setTasks(h, Task{ID: "own", Title: "巡检", Prompt: "看看", Repeat: repeatInterval, EveryMinutes: 30,
		At: time.Now().Add(time.Hour), Enabled: true, Until: kept})
	h.recordRun(ScheduleRun{RefID: "own", SessionID: "run-1", Status: runOK})
	own := func(args string) (string, error) {
		return h.toolScheduleFollowup(ctx, "server", "run-1", json.RawMessage(args))
	}
	if out, err = own(`{"prompt":"看看","title":"巡检","at":"2h"}`); err != nil || !strings.Contains(out, "Stops at") {
		t.Fatalf("omit on its own task: %q %v", out, err)
	}
	if got, _ := taskByID(h, "own"); !got.Until.Equal(kept) {
		t.Fatalf("omit did not keep it: %+v", got)
	}
	if out, err = own(`{"prompt":"看看","title":"巡检","at":"2h","until":null}`); err != nil || strings.Contains(out, "Stops at") {
		t.Fatalf("null on its own task: %q %v", out, err)
	}
	if got, _ := taskByID(h, "own"); !got.Until.IsZero() {
		t.Fatalf("null did not clear its own task: %+v", got)
	}
	if out, err = own(`{"prompt":"看看","title":"巡检","at":"2h","until":"` + endText + `"}`); err != nil || !strings.HasSuffix(out, suffix) {
		t.Fatalf("set on its own task: %q %v", out, err)
	}
	if got, _ := taskByID(h, "own"); !got.Until.Equal(want) {
		t.Fatalf("set on its own task stored %v", got.Until)
	}
	if _, err = own(`{"prompt":"看看","title":"巡检","at":"2h","until":"2000-01-01 00:00"}`); err == nil || err.Error() != i18n.T(i18n.Default, "assistant-schedule.until_passed") {
		t.Fatalf("past on its own task: %v", err)
	}
	if got, _ := taskByID(h, "own"); !got.Until.Equal(want) || !got.Enabled {
		t.Fatalf("the refused reschedule was saved: %+v", got)
	}

	// A one-off this call turns into an interval takes until. One that stays a one-off does not.
	setTasks(h, Task{ID: "fu", Title: "巡检", Prompt: "看看", Repeat: repeatOnce, At: time.Now().Add(-time.Minute),
		Enabled: false, LastRun: time.Now(), Until: kept})
	h.recordRun(ScheduleRun{RefID: "fu", SessionID: "run-2", Status: runOK})
	out, err = h.toolScheduleFollowup(ctx, "server", "run-2", json.RawMessage(`{"at":"30m","prompt":"看看","title":"巡检","until":"`+endText+`"}`))
	if err != nil || !strings.HasSuffix(out, suffix) {
		t.Fatalf("converted: %q %v", out, err)
	}
	if got, _ := taskByID(h, "fu"); got.Repeat != repeatInterval || !got.Until.Equal(want) {
		t.Fatalf("a one-off turned into an interval keeps until: %+v", got)
	}
}

func TestUntilInDescribeSchedule(t *testing.T) {
	h, loc := untilService(t)
	end := time.Date(2026, 12, 1, 18, 0, 0, 0, loc)
	hidden := time.Date(2026, 12, 2, 18, 0, 0, 0, loc)
	setTasks(h,
		Task{ID: "d", Title: "早报", Prompt: "x", Repeat: repeatDaily, Time: "09:00", Enabled: true, Until: end.UTC()},
		Task{ID: "e", Title: "晚报", Prompt: "y", Repeat: repeatDaily, Time: "21:00", Enabled: true},
		Task{ID: "f", Title: "一次", Prompt: "z", Repeat: repeatOnce, At: time.Now().Add(time.Hour), Enabled: true, Until: hidden.UTC()},
	)
	text := h.describeSchedule(context.Background(), "")
	line := func(id string) string {
		t.Helper()
		for _, l := range strings.Split(text, "\n") {
			if strings.Contains(l, "- "+id+" ") {
				return l
			}
		}
		t.Fatalf("no line for %s in %s", id, text)
		return ""
	}
	if !strings.Contains(line("d"), ", until 2026-12-01 18:00") {
		t.Fatalf("daily: %s", text)
	}
	if strings.Contains(line("e"), ", until") || strings.Contains(line("f"), ", until") || strings.Contains(text, "2026-12-02 18:00") {
		t.Fatalf("end time showed where it should not: %s", text)
	}
}
