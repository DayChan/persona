package assistant

import (
	"context"
	"testing"
	"time"
)

func TestMissedRunsAcrossTodayBriefAndSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name string
		runs []ScheduleRun
		want int
	}{
		{"automatic retry succeeds", []ScheduleRun{{Status: runFailed}, {Status: runOK}}, 0},
		{"manual retry succeeds", []ScheduleRun{{Status: runFailed}, {Status: runOK, Manual: true}}, 0},
		{"scheduled skip recovers", []ScheduleRun{{Status: runSkipped}, {Status: runOK}}, 0},
		{"manual busy skip then success", []ScheduleRun{{Status: runSkipped, Manual: true}, {Status: runOK}}, 0},
		{"manual failure alone", []ScheduleRun{{Status: runFailed, Manual: true}}, 0},
		{"manual skip alone", []ScheduleRun{{Status: runSkipped, Manual: true}}, 0},
		{"retry still fails", []ScheduleRun{{Status: runFailed}, {Status: runFailed}}, 2},
		{"success before new failure", []ScheduleRun{{Status: runOK}, {Status: runFailed}}, 1},
		{"other task succeeds", []ScheduleRun{{Status: runFailed}, {RefID: "other", Status: runOK}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBriefEnv(t)
			ctx := context.Background()
			now := e.clk.Now()
			loc := e.s.location(ctx)
			n := now.In(loc)
			since := time.Date(n.Year(), n.Month(), n.Day()-1, 18, 0, 0, 0, loc)
			sc := e.s.loadSchedule(ctx)
			sc.mu.Lock()
			for i, r := range tc.runs {
				if r.RefID == "" {
					r.RefID = "synthetic-task"
				}
				r.At = now.Add(-time.Hour + time.Duration(i)*3*time.Minute)
				sc.runs = append(sc.runs, r)
			}
			sc.mu.Unlock()
			failed, total := e.s.todayFailures(ctx, "*", since, loc)
			if len(failed) != tc.want || total != tc.want {
				t.Fatalf("Today: %v, total %d, want %d", failed, total, tc.want)
			}
			_, _, _, misses, total := e.s.snapshotSchedule(ctx, loc, now)
			if len(misses) != tc.want || total != tc.want {
				t.Fatalf("snapshot: %v, total %d, want %d", misses, total, tc.want)
			}
			facts := e.s.briefFactsAt(ctx, now, "")
			if len(facts.Failed) != tc.want || facts.FailedTotal != tc.want {
				t.Fatalf("brief: %v, total %d, want %d", facts.Failed, facts.FailedTotal, tc.want)
			}
		})
	}
}

func TestMissedRunsTimeAndAppendOrder(t *testing.T) {
	since := time.Date(2026, 11, 8, 18, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		runs []ScheduleRun
		want int
	}{
		{"at boundary", []ScheduleRun{{At: since, Status: runFailed}}, 1},
		{"before boundary", []ScheduleRun{{At: since.Add(-time.Second), Status: runFailed}}, 0},
		{"equal time later success", []ScheduleRun{{At: since, Status: runFailed}, {At: since, Status: runOK}}, 0},
		{"equal time earlier success", []ScheduleRun{{At: since, Status: runOK}, {At: since, Status: runFailed}}, 1},
		{"later start appended first", []ScheduleRun{{At: since.Add(time.Minute), Status: runOK}, {At: since, Status: runFailed}}, 0},
		{"earlier start appended last", []ScheduleRun{{At: since.Add(time.Minute), Status: runFailed}, {At: since, Status: runOK}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sc := &schedule{runs: tc.runs}
			sc.mu.Lock()
			defer sc.mu.Unlock()
			if got := sc.missedRuns(since); len(got) != tc.want {
				t.Fatalf("missedRuns = %v, want %d", got, tc.want)
			}
		})
	}
}
