package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// streakLimit: how many runs in a row a repeating task may fail or be skipped before the user is told (#4658). A
// nightly task skipped five nights in a row was only five lines in the run history nobody reads; three is late
// enough to be a real problem and early enough to act on.
const streakLimit = 3

// counted: whether r is one of the runs a streak counts (#4658). A failed "Run now" is answered in the app, and an
// automatic retry that failed is the same failure once more (watchScheduledRun): neither counts. A not-ready skip
// waits silently for its computer or model, so it neither counts nor ends the streak. Any run that went
// through, a retry or a "Run now" included, ends the streak: the task works.
func counted(r ScheduleRun) bool {
	return r.Status == runOK || !r.Manual && !r.Retried && !r.NotReady
}

// runStreak counts ref's runs in a row that did not go through, newest first, and says whether one of them was
// already told about (StreakTold) and whether the run id is one of them.
func runStreak(runs []ScheduleRun, ref, id string) (n, newer int, told, in bool) {
	for i := len(runs) - 1; i >= 0; i-- {
		r := runs[i]
		if r.RefID != ref {
			continue
		}
		if r.Ended {
			break // the task was turned off on purpose: the misses before that are a finished story
		}
		if !counted(r) {
			continue
		}
		if r.Status == runOK {
			break
		}
		n++
		told = told || r.StreakTold
		if r.ID == id {
			in = true
		} else if !in {
			newer++
		}
	}
	return n, newer, told, in
}

// streakToldLocked says whether sc.runs[i], just recorded (wasOK false) or just turned failed from ok (wasOK true),
// makes a repeating task's runs in a row that did not go through streakLimit or more, with none of them told yet; t is
// that task then, n how many. The run is then marked told, so this is once per streak whatever order runs record in:
// the count can jump past streakLimit when a run recorded ok turns failed after a later one (#4785). A run that goes
// through ends the streak and with it the mark. The schedule is locked.
//
// Only a streak that was below streakLimit before this run tells (#4881): one that was already over it was told, even
// when no run in it carries the mark — it was told before marks existed (#4670), or its marked run has fallen out of
// the maxRuns kept. Before this run the streak was n-1 for a new run; for one that turned failed it was the runs
// newer than it, which its ok had cut off from the older ones.
func streakToldLocked(sc *schedule, i int, wasOK bool) (t Task, n int, tell bool) {
	r := sc.runs[i]
	if r.Ended || r.Status == runOK || !counted(r) {
		return Task{}, 0, false
	}
	j := sc.indexLocked(r.RefID)
	if j < 0 || sc.tasks[j].Repeat == repeatOnce {
		return Task{}, 0, false
	}
	n, newer, told, in := runStreak(sc.runs, r.RefID, r.ID)
	before := n - 1
	if wasOK {
		before = newer
	}
	if !in || told || n < streakLimit || before >= streakLimit {
		return Task{}, 0, false
	}
	sc.runs[i].StreakTold = true
	return sc.tasks[j], n, true
}

// pushStreak tells the user t has not gone through its last count runs (#4658): which task, how many, and the last
// run's reason, in the conversation that run was in. Nothing repeats it until the task goes through once. It says
// whether it pushed: not into a muted conversation, where the run's own failure push is all the user gets (#4785).
func (s *Service) pushStreak(t Task, count int, last ScheduleRun) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if last.SessionID != "" && s.muted(last.MachineID, last.SessionID) {
		return false
	}
	l := s.lang(ctx)
	name := t.labelIn(l)
	if t.Space == SpaceHealth { // the lock screen does not name a health task (health.go)
		name, _ = healthNotice(l)
	}
	why := clip(oneLine(last.Error), 300)
	if why == "" {
		why = i18n.T(l, "assistant-schedule.streak_unknown")
	}
	n := Notification{SessionID: last.SessionID, DeviceID: last.MachineID,
		Title: i18n.T(l, "assistant-schedule.streak", i18n.Args{"name": name, "count": count}),
		Body:  i18n.T(l, "assistant-schedule.streak_body", i18n.Args{"reason": why, "count": count})}
	if last.MachineID != "" {
		if m, ok := s.device(ctx, last.MachineID); ok {
			n.Subtitle = m.Name
		}
	}
	s.push(ctx, n)
	return true
}
