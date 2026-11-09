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

func (s *Service) hasSchedulePending() bool {
	s.sched.mu.Lock()
	defer s.sched.mu.Unlock()
	return len(s.sched.pendingRuns) > 0
}

// flushSchedulePending retries observed run outcomes after a storage outage. It only writes history through CAS and
// is safe even when this manager no longer owns the scheduler.
func (s *Service) flushSchedulePending(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return
	}
	if len(sc.pendingRuns) > 0 {
		if err := s.mutateScheduleRunsLocked(ctx, sc, func(runs []ScheduleRun) []ScheduleRun { return runs }); err != nil {
			s.logf("save pending schedule runs: %v", err)
		}
	}
	sc.mu.Unlock()
}

// scheduleLimitRetry lives with the task, independently of the bounded run history.
// LastRun identifies the task generation that created this wait.
type scheduleLimitRetry struct {
	At      time.Time   `json:"at"`
	Turn    time.Time   `json:"turn"`
	LastRun time.Time   `json:"last_run"`
	Failed  ScheduleRun `json:"failed"`
	Claimed bool        `json:"claimed,omitempty"`
	// ClaimToken changes after each proven-unsent release, fencing delayed workers from later claims.
	ClaimToken string `json:"claim_token,omitempty"`
	// A manual admission can advance LastRun before it sends; restore it on a proven pre-send failure.
	ManualAt      time.Time `json:"manual_at,omitempty"`
	ManualLastRun time.Time `json:"manual_last_run,omitempty"`
}

func limitClaimKey(w scheduleLimitRetry) string {
	if w.ClaimToken != "" {
		return w.ClaimToken
	}
	return "legacy:" + w.Failed.ID
}

func sameLimitClaim(current *scheduleLimitRetry, w scheduleLimitRetry) bool {
	return current != nil && current.Claimed && current.Failed.ID == w.Failed.ID && current.ClaimToken == w.ClaimToken
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
			if existing := sc.tasks[i].LimitRetry; existing != nil && existing.Failed.ID == run.ID {
				sc.mu.Unlock()
				return true // another watcher already armed or claimed this run
			}
			tasks := slices.Clone(sc.tasks)
			tasks[i].LimitRetry = &scheduleLimitRetry{At: at.UTC(), Turn: turn.UTC(), LastRun: tasks[i].LastRun, Failed: run, ClaimToken: randomHex(16)}
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
	s.scanLimitRetries(ctx, now, nil, ctx.Done())
}

func (s *Service) scanLimitRetries(ctx context.Context, now time.Time, ownerDone, workerStop <-chan struct{}) {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return
	}
	var due []struct {
		id   string
		wait scheduleLimitRetry
	}
	for _, t := range sc.tasks {
		if t.LimitRetry != nil && (t.LimitRetry.Claimed || !t.LimitRetry.At.After(now)) {
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
		w.ClaimToken = cur.limitClaimToken
		cur.limitRetryRunID = w.Failed.ID
		cur.limitOwnerDone, cur.limitWorkerStop = ownerDone, workerStop
		s.sched.inflight.Add(1)
		s.sched.limitInflight.Add(1)
		go func() {
			defer s.sched.inflight.Done()
			defer s.sched.limitInflight.Done()
			s.dispatchLimitRetry(cur, w, now, ownerDone, workerStop)
		}()
		return // the scheduler spaces overdue sends; unavailable conversations do not block the rest
	}
}

// errLimitRetryNotSent marks a failure known to have happened before assistant.send.
var errLimitRetryNotSent = errors.New("retry was not sent")

type limitPreSendError struct{ cause error }

func (e limitPreSendError) Error() string        { return e.cause.Error() }
func (e limitPreSendError) Unwrap() error        { return e.cause }
func (e limitPreSendError) Is(target error) bool { return target == errLimitRetryNotSent }

type limitStartKey struct{}

type limitStartSignals struct {
	ownerDone, workerStop <-chan struct{}
}

func limitSendReady(ctx context.Context, ownerDone, workerStop <-chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ownerDone:
		return context.Canceled
	case <-workerStop:
		return context.Canceled
	default:
		return nil
	}
}

func limitStartPreSend(ctx context.Context, err error) error {
	if err != nil && ctx.Value(limitStartKey{}) != nil {
		return limitPreSendError{cause: err}
	}
	return err
}

func (s *Service) titleLimitRetry(t Task, machine, session string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		select {
		case <-t.limitOwnerDone:
			cancel()
		case <-t.limitWorkerStop:
			cancel()
		case <-ctx.Done():
		}
	}()
	s.noteTitleContext(ctx, machine, session, time.Now(), titleTask, t.label())
}

// dispatchLimitRetry is separated from the durable claim so tests can stop an owner at the boundary.
func (s *Service) dispatchLimitRetry(cur Task, w scheduleLimitRetry, now time.Time, ownerDone, workerStop <-chan struct{}) {
	select {
	case <-ownerDone:
		s.forgetLimitActive(limitClaimKey(w))
		return
	case <-workerStop:
		s.forgetLimitActive(limitClaimKey(w))
		return // leave the claim for the next owner to report
	default:
	}
	if _, err := s.runTask(cur, now, now, false, true); err != nil {
		s.logf("task %s retry: %v", cur.ID, err)
		if errors.Is(err, errLimitRetryNotSent) || errors.Is(err, errTaskBusy) {
			s.settleLimitRetry(cur.ID, w, err, ownerDone, workerStop)
			return
		}
		s.sched.mu.Lock()
		delete(s.sched.limitActive, limitClaimKey(w))
		s.sched.mu.Unlock()
		select {
		case <-ownerDone:
			return
		case <-workerStop:
			return
		default:
		}
		reportCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.recoverLimitClaim(reportCtx, cur.ID, w, now, "assistant-schedule.retry_delivery_unknown")
		cancel()
		return
	}
	s.settleLimitRetry(cur.ID, w, nil, ownerDone, workerStop)
}

func (s *Service) forgetLimitActive(id string) {
	s.sched.mu.Lock()
	delete(s.sched.limitActive, id)
	s.sched.mu.Unlock()
}

var errManualTaskGone = errors.New("scheduled task is gone")

// admitManualTask claims an existing retry before a manual send; the owner tracks it until its result is settled.
func (s *Service) admitManualTask(ctx context.Context, id string, now time.Time) (Task, *scheduleLimitRetry, <-chan struct{}, <-chan struct{}, error) {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return Task{}, nil, nil, nil, err
	}
	defer sc.mu.Unlock()
	i := sc.indexLocked(id)
	if i < 0 {
		return Task{}, nil, nil, nil, errManualTaskGone
	}
	t := sc.tasks[i]
	var wait *scheduleLimitRetry
	if t.LimitRetry != nil {
		if t.LimitRetry.Claimed || sc.sending[id] || sc.scheduledSending[id] > 0 {
			return Task{}, nil, nil, nil, errTaskBusy
		}
		if s.store() != nil && !sc.ownerHeld {
			return Task{}, nil, nil, nil, errScheduleUnavailable
		}
		select {
		case <-sc.ownerDone:
			return Task{}, nil, nil, nil, errScheduleUnavailable
		case <-sc.workerStop:
			return Task{}, nil, nil, nil, errScheduleUnavailable
		default:
		}
		copy := *t.LimitRetry
		if copy.ClaimToken == "" {
			copy.ClaimToken = randomHex(16)
		}
		wait = &copy
	}
	tasks := slices.Clone(sc.tasks)
	tasks[i].LastRun = now.UTC()
	if t.Repeat == repeatOnce {
		tasks[i].Enabled = false
	}
	if wait != nil {
		claim := *wait
		claim.Claimed = true
		claim.ManualAt, claim.ManualLastRun = now.UTC(), t.LastRun
		tasks[i].LimitRetry = &claim
	}
	if err := s.saveLocked(sc, tasks); err != nil {
		return Task{}, nil, nil, nil, err
	}
	if wait != nil {
		i = sc.indexLocked(id)
		if i < 0 || !sameLimitClaim(sc.tasks[i].LimitRetry, *wait) {
			return Task{}, nil, nil, nil, errScheduleUnavailable
		}
		if sc.limitActive == nil {
			sc.limitActive = make(map[string]bool)
		}
		sc.limitActive[limitClaimKey(*wait)] = true
		sc.inflight.Add(1)
		sc.limitInflight.Add(1)
		t.manualLimitRetry, t.limitRetryRunID = true, wait.Failed.ID
		t.limitOwnerDone, t.limitWorkerStop = sc.ownerDone, sc.workerStop
	}
	return t, wait, sc.ownerDone, sc.workerStop, nil
}

func (s *Service) settleLimitRetry(id string, w scheduleLimitRetry, sendErr error, ownerDone, workerStop <-chan struct{}) bool {
	if sendErr != nil && !errors.Is(sendErr, errTaskBusy) && !errors.Is(sendErr, errLimitRetryNotSent) {
		s.forgetLimitActive(limitClaimKey(w))
		select {
		case <-ownerDone:
			return false
		case <-workerStop:
			return false
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		s.recoverLimitClaim(ctx, id, w, time.Now(), "assistant-schedule.retry_delivery_unknown")
		cancel()
		return false
	}
	for {
		select {
		case <-ownerDone:
			s.forgetLimitActive(limitClaimKey(w))
			return false
		case <-workerStop:
			s.forgetLimitActive(limitClaimKey(w))
			return false
		default:
		}
		finished := false
		if sendErr != nil {
			finished = s.releaseUnsentLimitClaim(id, w)
		} else {
			finished = s.finishLimitClaim(id, w)
		}
		if finished {
			return true
		}
		select {
		case <-ownerDone:
			s.forgetLimitActive(limitClaimKey(w))
			return false
		case <-workerStop:
			s.forgetLimitActive(limitClaimKey(w))
			return false
		case <-time.After(time.Second):
		}
	}
}

// releaseUnsentLimitClaim makes a proven-unsent retry eligible again, without recreating a canceled task.
func (s *Service) releaseUnsentLimitClaim(id string, w scheduleLimitRetry) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return false
	}
	i := sc.indexLocked(id)
	if i < 0 || !sameLimitClaim(sc.tasks[i].LimitRetry, w) {
		delete(sc.limitActive, limitClaimKey(w))
		sc.mu.Unlock()
		return true
	}
	wait := *sc.tasks[i].LimitRetry
	wait.Claimed = false
	tasks := slices.Clone(sc.tasks)
	if !wait.ManualAt.IsZero() && tasks[i].LastRun.Equal(wait.ManualAt) {
		tasks[i].LastRun = wait.ManualLastRun
	}
	wait.ManualAt, wait.ManualLastRun = time.Time{}, time.Time{}
	wait.ClaimToken = randomHex(16) // a later claim is a different operation even for the same failed run
	tasks[i].LimitRetry = &wait
	if s.saveScheduleLocked(ctx, sc, tasks) != nil {
		sc.mu.Unlock()
		return false
	}
	delete(sc.limitActive, limitClaimKey(w))
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
		sc.tasks[i].LimitRetry.ClaimToken != w.ClaimToken || !sc.tasks[i].LimitRetry.At.Equal(w.At) {
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
		if claim.ClaimToken == "" {
			claim.ClaimToken = randomHex(16)
		}
		w.ClaimToken = claim.ClaimToken
		tasks[i].LimitRetry = &claim
	}
	if err := s.saveLocked(sc, tasks); err != nil {
		sc.mu.Unlock()
		return Task{}, false
	}
	i = sc.indexLocked(id)
	if !cancelled && (i < 0 || !sameLimitClaim(sc.tasks[i].LimitRetry, w)) {
		cancelled = true // a newer task change replaced or cleared the claim
	}
	if !cancelled {
		if sc.limitActive == nil {
			sc.limitActive = make(map[string]bool)
		}
		sc.limitActive[limitClaimKey(w)] = true
		cur.limitClaimToken = w.ClaimToken
	}
	sc.mu.Unlock()
	s.broadcastSchedule()
	return cur, !cancelled
}

// finishLimitClaim clears the claim only after runTask has recorded its result.
func (s *Service) finishLimitClaim(id string, w scheduleLimitRetry) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return false
	}
	adopted := false
	defer func() {
		sc.mu.Unlock()
		if adopted {
			go s.keepListed(context.Background())
		}
	}()
	i := sc.indexLocked(id)
	if i < 0 || !sameLimitClaim(sc.tasks[i].LimitRetry, w) {
		delete(sc.limitActive, limitClaimKey(w))
		return true
	}
	// recordRun keeps outcomes in memory on storage failure; flush this retry's result before dropping its claim.
	var pending *ScheduleRun
	for _, r := range sc.runs {
		if (r.RetryOf == w.Failed.ID && r.Status != runSkipped) || r.ID == "retry_unknown_"+w.Failed.ID {
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
		return (r.RetryOf == w.Failed.ID && r.Status != runSkipped) || r.ID == "retry_unknown_"+w.Failed.ID
	}) {
		return false
	}
	tasks := slices.Clone(sc.tasks)
	for _, r := range slices.Backward(sc.runs) {
		if r.RetryOf == w.Failed.ID && r.Adopt && r.SessionID != "" && r.MachineID != "" {
			tasks[i].RunMachineID, tasks[i].RunSessionID, tasks[i].RunLauncher = r.MachineID, r.SessionID, r.Launcher
			adopted = true
			break
		}
	}
	tasks[i].LimitRetry = nil
	if s.saveScheduleLocked(ctx, sc, tasks) != nil {
		adopted = false
		return false
	}
	delete(sc.limitActive, limitClaimKey(w))
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
	if i < 0 || sc.limitActive[limitClaimKey(w)] || !sameLimitClaim(sc.tasks[i].LimitRetry, w) {
		sc.mu.Unlock()
		return
	}
	// Fence stale owners before they write an outcome or notify: a newer claim must not be reported by this one.
	if _, versioned := s.store().(scheduleVersionStore); versioned {
		if err := s.saveScheduleLocked(ctx, sc, slices.Clone(sc.tasks)); err != nil {
			sc.mu.Unlock()
			return
		}
		i = sc.indexLocked(id)
		if i < 0 || !sameLimitClaim(sc.tasks[i].LimitRetry, w) {
			sc.mu.Unlock()
			return
		}
	}
	task := sc.tasks[i]
	runID := "retry_unknown_" + w.Failed.ID
	known := false
	manual := !sc.tasks[i].LimitRetry.ManualAt.IsZero()
	err = s.mutateScheduleRunsLocked(ctx, sc, func(runs []ScheduleRun) []ScheduleRun {
		known = slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.RetryOf == w.Failed.ID && r.Status != runSkipped })
		if !known && !slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.ID == runID }) {
			runs = append(runs, ScheduleRun{ID: runID, Kind: task.kind(), RefID: id, Title: task.label(), Due: w.At, At: now.UTC(), Status: runFailed,
				Error: message, MachineID: w.Failed.MachineID, SessionID: w.Failed.SessionID, Retried: true, Manual: manual})
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
	sc.limitActive[limitClaimKey(w)] = true
	sc.mu.Unlock()
	if known {
		if !s.finishLimitClaim(id, w) {
			sc.mu.Lock()
			delete(sc.limitActive, limitClaimKey(w))
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
		delete(sc.limitActive, limitClaimKey(w))
		sc.mu.Unlock()
		return
	}
	if !s.finishLimitClaim(id, w) {
		sc.mu.Lock()
		delete(sc.limitActive, limitClaimKey(w))
		sc.mu.Unlock()
	}
}
