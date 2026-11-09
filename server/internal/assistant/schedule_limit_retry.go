package assistant

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// scheduleLimitRetry lives with the task, independently of the bounded run history.
// LastRun identifies the task generation that created this wait.
type scheduleLimitRetry struct {
	At      time.Time   `json:"at"`
	Turn    time.Time   `json:"turn"`
	LastRun time.Time   `json:"last_run"`
	Failed  ScheduleRun `json:"failed"`
	Claimed bool        `json:"claimed,omitempty"`
}

// armLimitRetry saves the wait before letting the scheduler see it. If storage is temporarily unavailable,
// keep trying while the watcher is alive; a failed save must not be mistaken for a durable wait.
func (s *Service) armLimitRetry(ctx context.Context, run ScheduleRun, turn, at time.Time) {
	for {
		sc, err := s.lockedSchedule(ctx)
		if err == nil {
			i := sc.indexLocked(run.RefID)
			if i < 0 || (!run.of.IsZero() && sc.tasks[i].LastRun.After(run.of)) ||
				retryCancelled(sc.tasks[i], i >= 0, run, sc.runs, time.Now()) != "" {
				sc.mu.Unlock()
				return
			}
			tasks := slices.Clone(sc.tasks)
			tasks[i].LimitRetry = &scheduleLimitRetry{At: at.UTC(), Turn: turn.UTC(), LastRun: tasks[i].LastRun, Failed: run}
			err = s.saveLocked(sc, tasks)
			sc.mu.Unlock()
			if err == nil {
				s.broadcastSchedule()
				return
			}
		}
		select {
		case <-ctx.Done():
			s.logf("task %s: could not persist its limit retry: %v", run.RefID, ctx.Err())
			return
		case <-time.After(time.Second):
		}
	}
}

// runLimitRetries drives a scan directly in tests; the scheduler holds an owner while it scans.
func (s *Service) runLimitRetries(ctx context.Context, now time.Time) {
	s.scanLimitRetries(ctx, now, nil)
}

func (s *Service) scanLimitRetries(ctx context.Context, now time.Time, ownerDone <-chan struct{}) {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return
	}
	var due []struct {
		id   string
		wait scheduleLimitRetry
	}
	for _, t := range sc.tasks {
		if t.LimitRetry != nil && !t.LimitRetry.At.After(now) {
			due = append(due, struct {
				id   string
				wait scheduleLimitRetry
			}{t.ID, *t.LimitRetry})
		}
	}
	sc.mu.Unlock()
	for _, item := range due {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ownerDone:
			return
		default:
		}
		w := item.wait
		if w.Claimed {
			s.recoverLimitClaim(ctx, item.id, w, now)
			continue
		}
		// An unavailable node tells us nothing about whether the user continued the conversation.
		c, found := s.session(ctx, w.Failed.MachineID, w.Failed.SessionID)
		if !found {
			continue
		}
		cur, dispatch := s.consumeLimitRetry(ctx, item.id, w, c, now)
		if !dispatch {
			continue
		}
		cur.MachineID, cur.SessionID, cur.limitRetry = w.Failed.MachineID, w.Failed.SessionID, true
		cur.limitRetryRunID = w.Failed.ID
		s.sched.inflight.Add(1)
		s.sched.limitInflight.Add(1)
		go func() {
			defer s.sched.inflight.Done()
			defer s.sched.limitInflight.Done()
			s.dispatchLimitRetry(cur, w, now, ownerDone)
		}()
		return // the scheduler spaces overdue sends; unavailable conversations do not block the rest
	}
}

// dispatchLimitRetry is separated from the durable claim so tests can stop an owner at the boundary.
func (s *Service) dispatchLimitRetry(cur Task, w scheduleLimitRetry, now time.Time, ownerDone <-chan struct{}) {
	select {
	case <-ownerDone:
		s.sched.mu.Lock()
		delete(s.sched.limitActive, w.Failed.ID)
		s.sched.mu.Unlock()
		return // leave the claim for the next owner to report
	default:
	}
	if _, err := s.runTask(cur, now, now, false, true); err != nil {
		s.logf("task %s retry: %v", cur.ID, err)
	}
	for !s.finishLimitClaim(cur.ID, w) {
		select {
		case <-ownerDone:
			s.sched.mu.Lock()
			delete(s.sched.limitActive, w.Failed.ID)
			s.sched.mu.Unlock()
			return // the durable claim remains for the next owner
		case <-time.After(time.Second):
		}
	}
}

// consumeLimitRetry claims before sending. A restored claim is reported as unknown instead of being sent again.
func (s *Service) consumeLimitRetry(ctx context.Context, id string, w scheduleLimitRetry, c proto.Session, now time.Time) (Task, bool) {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return Task{}, false
	}
	i := sc.indexLocked(id)
	if i < 0 || sc.tasks[i].LimitRetry == nil || sc.tasks[i].LimitRetry.Claimed || sc.tasks[i].LimitRetry.Failed.ID != w.Failed.ID ||
		!sc.tasks[i].LimitRetry.At.Equal(w.At) {
		sc.mu.Unlock()
		return Task{}, false
	}
	cur := sc.tasks[i]
	cancelled := !cur.LastRun.Equal(w.LastRun) || retryCancelled(cur, true, w.Failed, sc.runs, now) != "" ||
		c.Status == proto.StatusWorking || c.State == proto.StateWorking || (!w.Turn.IsZero() && !sameTurn(c, w.Turn))
	tasks := slices.Clone(sc.tasks)
	if cancelled {
		tasks[i].LimitRetry = nil
	} else {
		claim := w
		claim.Claimed = true
		tasks[i].LimitRetry = &claim
	}
	if err := s.saveLocked(sc, tasks); err != nil {
		sc.mu.Unlock()
		return Task{}, false
	}
	i = sc.indexLocked(id)
	if !cancelled && (i < 0 || sc.tasks[i].LimitRetry == nil || !sc.tasks[i].LimitRetry.Claimed || sc.tasks[i].LimitRetry.Failed.ID != w.Failed.ID) {
		cancelled = true // a newer task change replaced or cleared the claim
	}
	if !cancelled {
		if sc.limitActive == nil {
			sc.limitActive = make(map[string]bool)
		}
		sc.limitActive[w.Failed.ID] = true
	}
	sc.mu.Unlock()
	s.broadcastSchedule()
	return cur, !cancelled
}

// finishLimitClaim clears the claim only after runTask has recorded its result.
func (s *Service) finishLimitClaim(id string, w scheduleLimitRetry) bool {
	sc, err := s.lockedSchedule(context.Background())
	if err != nil {
		return false
	}
	defer sc.mu.Unlock()
	i := sc.indexLocked(id)
	if i < 0 || sc.tasks[i].LimitRetry == nil || sc.tasks[i].LimitRetry.Failed.ID != w.Failed.ID {
		delete(sc.limitActive, w.Failed.ID)
		return true
	}
	// recordRun keeps outcomes in memory on storage failure; flush them before dropping the durable claim.
	if st := s.store(); st != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = st.PutSetting(ctx, runsKey, sc.runs)
		cancel()
		if err != nil {
			return false
		}
	}
	tasks := slices.Clone(sc.tasks)
	tasks[i].LimitRetry = nil
	if s.saveLocked(sc, tasks) != nil {
		return false
	}
	delete(sc.limitActive, w.Failed.ID)
	return true
}

// recoverLimitClaim persists an unknown result before attempting the normal failure notification. The claim
// remains until after the push attempt, so a crash can repeat a notification but cannot silently skip it.
func (s *Service) recoverLimitClaim(ctx context.Context, id string, w scheduleLimitRetry, now time.Time) {
	l := s.lang(ctx)
	message := i18n.T(l, "assistant-schedule.retry_restart_unknown")
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return
	}
	i := sc.indexLocked(id)
	if i < 0 || sc.limitActive[w.Failed.ID] || sc.tasks[i].LimitRetry == nil || !sc.tasks[i].LimitRetry.Claimed || sc.tasks[i].LimitRetry.Failed.ID != w.Failed.ID {
		sc.mu.Unlock()
		return
	}
	task := sc.tasks[i]
	if slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.RetryOf == w.Failed.ID }) {
		// The send result was saved, but clearing the claim was interrupted.
		if sc.limitActive == nil {
			sc.limitActive = make(map[string]bool)
		}
		sc.limitActive[w.Failed.ID] = true
		sc.mu.Unlock()
		if !s.finishLimitClaim(id, w) {
			sc.mu.Lock()
			delete(sc.limitActive, w.Failed.ID)
			sc.mu.Unlock()
		}
		return
	}
	runID := "retry_unknown_" + w.Failed.ID
	runs := slices.Clone(sc.runs)
	if !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.ID == runID }) {
		r := ScheduleRun{ID: runID, Kind: task.kind(), RefID: id, Title: task.label(), Due: w.At, At: now.UTC(), Status: runFailed,
			Error: message, MachineID: w.Failed.MachineID, SessionID: w.Failed.SessionID, Retried: true}
		runs = append(runs, r)
		if len(runs) > maxRuns {
			runs = slices.Clone(runs[len(runs)-maxRuns:])
		}
		if st := s.store(); st != nil {
			if err := st.PutSetting(ctx, runsKey, runs); err != nil {
				sc.mu.Unlock()
				return
			}
		}
		sc.runs = runs
	}
	// Mark this recovery active while the notification is sent, to serialize overlapping scans.
	if sc.limitActive == nil {
		sc.limitActive = make(map[string]bool)
	}
	sc.limitActive[w.Failed.ID] = true
	sc.mu.Unlock()
	name := task.labelIn(l)
	if task.Space == SpaceHealth {
		name, _ = healthNotice(l)
	}
	n := Notification{ID: "ntf_retry_" + w.Failed.ID, Kind: NotifyPush, DeviceID: w.Failed.MachineID, SessionID: w.Failed.SessionID,
		Title: i18n.T(l, "assistant-schedule.run_failed", i18n.Args{"name": name}), Body: message}
	if err := s.hub.NotifyRequest(ctx, NotifyRequest{Notification: n}); err != nil {
		s.logf("task %s retry notice: %v", id, err)
		sc.mu.Lock()
		delete(sc.limitActive, w.Failed.ID)
		sc.mu.Unlock()
		return
	}
	if !s.finishLimitClaim(id, w) {
		sc.mu.Lock()
		delete(sc.limitActive, w.Failed.ID)
		sc.mu.Unlock()
	}
}
