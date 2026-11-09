package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func scheduleRunsIncludingPending(sc *schedule) []ScheduleRun {
	runs := slices.Clone(sc.runs)
	for _, r := range sc.pendingRuns {
		runs = append(runs, r)
	}
	return runs
}

type scheduleRunsStore interface {
	ReadScheduleRuns(context.Context) (json.RawMessage, int64, bool, error)
	CompareScheduleRuns(context.Context, int64, json.RawMessage) (bool, error)
}

// mutateScheduleRunsLocked applies one operation to the latest history. The version check protects this list
// from concurrent writers in other managers. sc.mu must be held.
func (s *Service) mutateScheduleRunsLocked(ctx context.Context, sc *schedule, mutate func([]ScheduleRun) []ScheduleRun) error {
	st := s.store()
	if st == nil {
		sc.runs = mutate(sc.runs)
		return nil
	}
	for range 8 {
		var stored []ScheduleRun
		var revision int64
		vs, versioned := st.(scheduleRunsStore)
		if versioned {
			raw, rev, found, err := vs.ReadScheduleRuns(ctx)
			if err != nil {
				return err
			}
			revision = rev
			if found && json.Unmarshal(raw, &stored) != nil {
				return errors.New("invalid schedule run history")
			}
		} else if _, err := st.Setting(ctx, runsKey, &stored); err != nil {
			return err
		}
		pending := make([]ScheduleRun, 0, len(sc.pendingRuns))
		for _, r := range sc.pendingRuns {
			pending = append(pending, r)
		}
		slices.SortFunc(pending, func(a, b ScheduleRun) int {
			if n := a.At.Compare(b.At); n != 0 {
				return n
			}
			return strings.Compare(a.ID, b.ID)
		})
		for _, r := range pending {
			if i := slices.IndexFunc(stored, func(existing ScheduleRun) bool { return existing.ID == r.ID }); i >= 0 {
				stored[i] = r
			} else {
				stored = append(stored, r)
			}
		}
		stored = mutate(stored)
		if len(stored) > maxRuns {
			stored = slices.Clone(stored[len(stored)-maxRuns:])
		}
		if stored == nil {
			stored = []ScheduleRun{}
		}
		if versioned {
			raw, err := json.Marshal(stored)
			if err != nil {
				return err
			}
			ok, err := vs.CompareScheduleRuns(ctx, revision, raw)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
		} else if err := st.PutSetting(ctx, runsKey, stored); err != nil {
			return err
		}
		sc.runs = stored
		for id := range sc.pendingRuns {
			if slices.ContainsFunc(stored, func(r ScheduleRun) bool { return r.ID == id }) {
				delete(sc.pendingRuns, id)
			}
		}
		return nil
	}
	return errors.New("schedule run history changed repeatedly")
}

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
func (s *Service) armLimitRetry(ctx context.Context, run ScheduleRun, turn, at time.Time) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		sc, err := s.lockedSchedule(ctx)
		if err == nil {
			i := sc.indexLocked(run.RefID)
			if i < 0 || retryCancelled(sc.tasks[i], i >= 0, run, scheduleRunsIncludingPending(sc), time.Now()) != "" {
				sc.mu.Unlock()
				return true // the task no longer needs this wait
			}
			tasks := slices.Clone(sc.tasks)
			tasks[i].LimitRetry = &scheduleLimitRetry{At: at.UTC(), Turn: turn.UTC(), LastRun: tasks[i].LastRun, Failed: run}
			err = s.saveLocked(sc, tasks)
			sc.mu.Unlock()
			if err == nil {
				s.broadcastSchedule()
				return true
			}
		}
		select {
		case <-ctx.Done():
			s.logf("task %s: could not persist its limit retry: %v", run.RefID, ctx.Err())
			return false
		case <-time.After(time.Second):
		}
	}
}

// refreshLimitSchedule picks up waits registered by another manager even when this process received no wakeup.
func (s *Service) refreshLimitSchedule(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	vs, ok := s.store().(scheduleVersionStore)
	if !ok {
		return s.store() == nil
	}
	sc := &s.sched
	sc.mu.Lock()
	_, revision, found, err := vs.ReadSchedule(ctx)
	if err != nil {
		sc.mu.Unlock()
		return false
	}
	if !found || !sc.loaded || revision != sc.revision {
		sc.loaded = false
	}
	loaded := sc.loaded
	sc.mu.Unlock()
	if !loaded {
		sc = s.loadSchedule(ctx)
		sc.mu.Lock()
		loaded = sc.loaded
		sc.mu.Unlock()
	}
	return loaded
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
			s.recoverLimitClaim(ctx, item.id, w, now, "assistant-schedule.retry_restart_unknown")
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

// errLimitRetryNotSent is used only when the node was unavailable before calling assistant.send.
var errLimitRetryNotSent = errors.New("retry conversation's machine is unavailable")

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
		if errors.Is(err, errLimitRetryNotSent) {
			for !s.releaseUnsentLimitClaim(cur.ID, w) {
				select {
				case <-ownerDone:
					s.sched.mu.Lock()
					delete(s.sched.limitActive, w.Failed.ID)
					s.sched.mu.Unlock()
					return
				case <-time.After(time.Second):
				}
			}
			return
		}
		s.sched.mu.Lock()
		delete(s.sched.limitActive, w.Failed.ID)
		s.sched.mu.Unlock()
		select {
		case <-ownerDone:
			return
		default:
		}
		reportCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.recoverLimitClaim(reportCtx, cur.ID, w, now, "assistant-schedule.retry_delivery_unknown")
		cancel()
		return
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

// releaseUnsentLimitClaim makes a proven-unsent retry eligible again, without recreating a canceled task.
func (s *Service) releaseUnsentLimitClaim(id string, w scheduleLimitRetry) bool {
	sc, err := s.lockedSchedule(context.Background())
	if err != nil {
		return false
	}
	i := sc.indexLocked(id)
	if i < 0 || sc.tasks[i].LimitRetry == nil || sc.tasks[i].LimitRetry.Failed.ID != w.Failed.ID {
		delete(sc.limitActive, w.Failed.ID)
		sc.mu.Unlock()
		return true
	}
	wait := *sc.tasks[i].LimitRetry
	wait.Claimed = false
	tasks := slices.Clone(sc.tasks)
	tasks[i].LimitRetry = &wait
	if s.saveLocked(sc, tasks) != nil {
		sc.mu.Unlock()
		return false
	}
	delete(sc.limitActive, w.Failed.ID)
	sc.mu.Unlock()
	s.broadcastSchedule()
	return true
}

// consumeLimitRetry claims before sending. A restored claim is reported as unknown instead of being sent again.
func (s *Service) consumeLimitRetry(ctx context.Context, id string, w scheduleLimitRetry, c proto.Session, now time.Time) (Task, bool) {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return Task{}, false
	}
	i := sc.indexLocked(id)
	if sc.scheduledSending[id] > 0 || sc.sending[id] {
		sc.mu.Unlock()
		return Task{}, false // an admitted run may still send; do not consume the wait yet
	}
	if i < 0 || sc.tasks[i].LimitRetry == nil || sc.tasks[i].LimitRetry.Claimed || sc.tasks[i].LimitRetry.Failed.ID != w.Failed.ID ||
		!sc.tasks[i].LimitRetry.At.Equal(w.At) {
		sc.mu.Unlock()
		return Task{}, false
	}
	cur := sc.tasks[i]
	cancelled := retryCancelled(cur, true, w.Failed, scheduleRunsIncludingPending(sc), now) != "" ||
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
	// recordRun keeps outcomes in memory on storage failure; flush this retry's result before dropping its claim.
	var pending *ScheduleRun
	for _, r := range sc.runs {
		if r.RetryOf == w.Failed.ID || r.ID == "retry_unknown_"+w.Failed.ID {
			copy := r
			pending = &copy
			break
		}
	}
	if s.store() != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = s.mutateScheduleRunsLocked(ctx, sc, func(runs []ScheduleRun) []ScheduleRun {
			if pending != nil && !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.ID == pending.ID }) {
				runs = append(runs, *pending)
			}
			return runs
		})
		cancel()
		if err != nil {
			return false
		}
	}
	if !slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool {
		return r.RetryOf == w.Failed.ID || r.ID == "retry_unknown_"+w.Failed.ID
	}) {
		return false
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
func (s *Service) recoverLimitClaim(ctx context.Context, id string, w scheduleLimitRetry, now time.Time, messageKey string) {
	l := s.lang(ctx)
	message := i18n.T(l, messageKey)
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
	runID := "retry_unknown_" + w.Failed.ID
	known := false
	err = s.mutateScheduleRunsLocked(ctx, sc, func(runs []ScheduleRun) []ScheduleRun {
		known = slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.RetryOf == w.Failed.ID })
		if !known && !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.ID == runID }) {
			runs = append(runs, ScheduleRun{ID: runID, Kind: task.kind(), RefID: id, Title: task.label(), Due: w.At, At: now.UTC(), Status: runFailed,
				Error: message, MachineID: w.Failed.MachineID, SessionID: w.Failed.SessionID, Retried: true})
		}
		return runs
	})
	if err != nil {
		sc.mu.Unlock()
		return
	}
	if j := slices.IndexFunc(sc.runs, func(r ScheduleRun) bool { return r.ID == runID }); j >= 0 {
		message = sc.runs[j].Error // retries use the message saved with the unknown outcome
	}
	// Mark this recovery active while the notification is sent, to serialize overlapping scans.
	if sc.limitActive == nil {
		sc.limitActive = make(map[string]bool)
	}
	sc.limitActive[w.Failed.ID] = true
	sc.mu.Unlock()
	if known {
		if !s.finishLimitClaim(id, w) {
			sc.mu.Lock()
			delete(sc.limitActive, w.Failed.ID)
			sc.mu.Unlock()
		}
		return
	}
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
