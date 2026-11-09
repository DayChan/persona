package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- following a task's agents ----

// taskAgents are the sessions of t's active agents, as the clients see them (with state).
func (s *Service) taskAgents(ctx context.Context, t Todo) []proto.Session {
	var out []proto.Session
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		list, err := s.sessions(ctx, a.MachineID)
		if err != nil {
			continue
		}
		for _, c := range list {
			if c.SessionID == a.SessionID {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// sessionsReached lists machine's sessions, retrying a dropped or offline link a bounded number of times within
// ctx. reached is false when the node could not be asked after the retries: the caller must not read that as
// "the machine has no sessions". ErrNoDevice is deliberately different: the hub itself answers that it has no
// such machine (it was removed from the hub's configuration), a definite answer rather than a flaky node — an
// agent registered on a machine the hub no longer knows cannot still be waiting, so it answers with an empty
// list and reached true. Retrying it would spend the budget and wrongly keep a needs notice for a machine gone
// for good.
func (s *Service) sessionsReached(ctx context.Context, machine string) ([]proto.Session, bool) {
	backoff := agentReachBackoff
	for attempt := 0; ; attempt++ {
		list, err := s.sessions(ctx, machine)
		if err == nil {
			return list, true
		}
		if errors.Is(err, ErrNoDevice) {
			return nil, true // definite: the hub no longer knows this machine
		}
		if attempt+1 >= agentReachAttempts || ctx.Err() != nil {
			return nil, false
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
		backoff *= 2
	}
}

// taskAgentsReached is taskAgents plus reachability. Each distinct machine of t's active agents is asked once
// within one agentReachMax deadline so retries across machines cannot stack; reached is false when one stayed
// unreachable, so callers keep "still waits" rather than read an unreachable node as an answer (ErrNoDevice: none).
func (s *Service) taskAgentsReached(ctx context.Context, t Todo) (agents []proto.Session, reached bool) {
	return s.taskAgentsReachedFirst(ctx, t, "")
}

// taskAgentsReachedFirst asks distinct machines concurrently under one deadline.
// A stalled machine cannot consume another machine's chance to answer.
func (s *Service) taskAgentsReachedFirst(ctx context.Context, t Todo, first string) (agents []proto.Session, reached bool) {
	ctx, cancel := context.WithTimeout(ctx, agentReachMax)
	defer cancel()
	type result struct {
		machine string
		list    []proto.Session
		ok      bool
	}
	var machines []string
	asked := map[string]bool{}
	add := func(machine string) {
		if !asked[machine] {
			asked[machine] = true
			machines = append(machines, machine)
		}
	}
	if first != "" {
		for _, a := range t.Agents {
			if a.MachineID == first && a.ReleasedAt == nil && a.DismissedAt == nil {
				add(first)
				break
			}
		}
	}
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil {
			add(a.MachineID)
		}
	}
	results := make(chan result, len(machines))
	for _, machine := range machines {
		go func() {
			list, ok := s.sessionsReached(ctx, machine)
			results <- result{machine: machine, list: list, ok: ok}
		}()
	}
	reached = true
	lists := make(map[string][]proto.Session, len(machines))
	collect := func(r result) {
		if !r.ok {
			reached = false
		} else {
			lists[r.machine] = r.list
		}
	}
collecting:
	for range machines {
		select {
		case r := <-results:
			collect(r)
		case <-ctx.Done():
			reached = false
			// Keep answers already delivered even if the deadline raced them.
			for {
				select {
				case r := <-results:
					collect(r)
				default:
					break collecting
				}
			}
		}
	}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		for _, c := range lists[a.MachineID] {
			if c.SessionID == a.SessionID {
				c.MachineID = a.MachineID
				agents = append(agents, c)
				break
			}
		}
	}
	return agents, reached
}

// taskNeedsRecheck freshly asks t's agents' machines whether one still waits for the user. known is false when an
// agent machine stayed unreachable after the bounded retries, so a caller can keep the notice rather than take
// an unreachable node for an answer.
func (s *Service) taskNeedsRecheck(ctx context.Context, t Todo) (waits, known bool) {
	agents, reached := s.taskAgentsReached(ctx, t)
	if !reached {
		return false, false
	}
	for _, c := range agents {
		if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
			return true, true
		}
	}
	return false, true
}

// taskWaits: one of t's agents still waits for the user. A machine the recheck cannot reach is not an answer: it
// is taken as still waiting, so a needs notice is never dropped on a transient RPC failure.
func (s *Service) taskWaits(ctx context.Context, t Todo) bool {
	waits, known := s.taskNeedsRecheck(ctx, t)
	return waits || !known
}

// taskWorking: one of the task's agents started a turn. The agent of a task the hub closed meanwhile (judged done,
// reviewed) works on it again — sent more by agent_send, which resumes a closed one, or by the user: the task is open
// again first, as a change asked for on its "Please verify" card reopens it (#3457), so this turn is followed
// (interrupted or done receipt, judgment) and the task shows in progress, not done (#5892).
func (s *Service) taskWorking(ctx context.Context, mid, sid string) {
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	t, ok := s.taskOf(ctx, mid, sid)
	if !ok {
		closed, found := s.closedTaskOf(ctx, mid, sid)
		if !found {
			return
		}
		if err := s.reopenTodo(ctx, closed.ID); err != nil {
			s.logf("task %s: reopen: %v", closed.ID, err)
			return
		}
		if t, ok = s.taskOf(ctx, mid, sid); !ok {
			return
		}
		s.logf("task %s: reopened: its agent works again", t.ID)
	}
	s.tasks.mu.Lock()
	s.tasks.working[t.ID] = time.Now()
	if s.tasks.workIDs == nil {
		s.tasks.workIDs = make(map[string]string)
	}
	s.tasks.workIDs[t.ID] = randomHex(16)
	s.tasks.mu.Unlock()
	s.setTaskStage(ctx, t.ID, "develop", "")
	if t.Supervision != nil {
		s.supervisedWorking(ctx, t)
	}
}

type receiptBody struct {
	Title   string         `json:"title"`
	State   string         `json:"state"` // done, failed, interrupted
	Summary string         `json:"summary,omitempty"`
	PR      string         `json:"pr,omitempty"`
	Machine string         `json:"machine,omitempty"`
	Review  *receiptReview `json:"review,omitempty"`
	// Error: what the model service answered when the turn was cut off (interrupted), for the card's detail.
	Error string `json:"error,omitempty"`
}

type receiptReview struct {
	State    string `json:"state"` // checking: Persona follows up; awaiting: completion already judged
	Criteria int    `json:"criteria"`
}

// interruptedSummary is an interrupted task's receipt: the error itself is no conclusion (#1516).
const interruptedSummary = assistantwire.InterruptedSummary

// silentTurnErrorIn is the interrupted receipt's detail for a turn with no output at all (silentTurn, #2349), in l.
// Not kept in a package variable: that would read the catalogs whenever personad starts, a hook too (#1308).
func silentTurnErrorIn(l i18n.Lang) string { return i18n.T(l, "assistant-tasks.silent_turn") }

var prLink = lazyre.New(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+`)

func (s *Service) receiptTurnMatches(t Todo, machine, sid string, expected *receiptLook) bool {
	if expected == nil || expected.Todo == "" {
		return true
	}
	current, found := s.taskOf(context.Background(), machine, sid)
	linked, ok := receiptLink(current, SessionRef{MachineID: machine, SessionID: sid})
	if !found || !ok || current.ID != expected.Todo || t.ID != expected.Todo || !linked.Equal(expected.Linked) {
		return false
	}
	if expected.ID == "" {
		s.tasks.mu.Lock()
		generation, observed := s.tasks.workIDs[t.ID]
		s.tasks.mu.Unlock()
		return !observed || generation == expected.Generation
	}
	s.tasks.receiptMu.Lock()
	defer s.tasks.receiptMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.receiptLooksLocked(ctx); err != nil {
		s.logf("task %s: check receipt generation: %v", t.ID, err)
		return false
	}
	look, ok := s.tasks.receiptLooks[receiptKeyOf(*expected)]
	return ok && look.ID == expected.ID && look.Work.Equal(expected.Work)
}

func (s *Service) retryTaskTurnEnded(ctx context.Context, machine, sid string, expected *receiptLook) {
	s.tasks.receiptMu.Lock()
	lifetime := s.tasks.receiptCtx
	s.tasks.receiptMu.Unlock()
	if lifetime == nil {
		lifetime = context.WithoutCancel(ctx)
	}
	retry := receiptLook{Machine: machine, Session: sid}
	if expected != nil {
		retry = *expected
	}
	if retry.Attempts < 6 {
		retry.Attempts++
	}
	delay := time.Minute * time.Duration(1<<(retry.Attempts-1))
	time.AfterFunc(delay, func() {
		if lifetime.Err() == nil {
			s.taskTurnEndedForLook(lifetime, machine, sid, &retry)
		}
	})
}

func (s *Service) retryTaskReceipt(ctx context.Context, t Todo, machine, sid string, work time.Time, expected *receiptLook) {
	if expected == nil {
		linked, ok := receiptLink(t, SessionRef{MachineID: machine, SessionID: sid})
		if !ok {
			return
		}
		s.tasks.mu.Lock()
		generation := s.tasks.workIDs[t.ID]
		s.tasks.mu.Unlock()
		expected = &receiptLook{Todo: t.ID, Machine: machine, Session: sid, Linked: linked, Work: work, Generation: generation}
	}
	if expected.ID == "" && expected.RetryID == "" {
		copy := *expected
		copy.RetryID = "feed_" + randomHex(16)
		expected = &copy
	}
	s.retryTaskTurnEnded(ctx, machine, sid, expected)
}

// receiptRecheckID names a fresh observation made when the old turn cannot be identified.
func receiptRecheckID(id string) string { return "feed_recheck_" + strings.TrimPrefix(id, "feed_") }

// recoverTaskReceipt resumes follow-up from the committed feed, never from a later turn's transcript.
func (s *Service) recoverTaskReceipt(ctx context.Context, t Todo, look *receiptLook) (bool, error) {
	if look == nil || look.ID == "" || s.store() == nil {
		return false, nil
	}
	if _, ok := s.store().(interface {
		AppendFeedOnce(context.Context, db.FeedRow) (bool, error)
	}); !ok {
		return false, nil
	}
	row, err := s.store().FeedItem(ctx, look.ID)
	if err != nil {
		return false, err
	}
	if row == nil {
		row, err = s.store().FeedItem(ctx, receiptRecheckID(look.ID))
		if err != nil {
			return false, err
		}
		if row == nil {
			return false, nil
		}
	}
	if row.Kind != feedReceipt || row.Ref != t.ID || row.Conversation != t.Origin {
		return true, errors.New("receipt feed identity changed")
	}
	valid, err := s.receiptLookLinked(ctx, *look)
	if err != nil {
		return true, err
	}
	if !valid {
		return true, errors.New("receipt task association changed")
	}
	var body receiptBody
	if err := json.Unmarshal(row.Body, &body); err != nil {
		return true, err
	}
	if !s.taskReceiptActive(ctx, look.ID) {
		return true, nil
	}
	switch body.State {
	case "interrupted":
		s.taskNotice(ctx, t, body.Machine, "interrupted", "", body.Error)
	case "done":
		s.taskNotice(ctx, t, body.Machine, "done", "", body.Summary)
		// A manager-local generation cannot exclude another manager seeing new work.
		// The feed summary is truncated, so it cannot safely restore stage or acceptance.
		s.logf("task %s: recovered receipt judgment skipped: current work generation is not confirmed", t.ID)
	default:
		return true, errors.New("unknown stored task receipt state")
	}
	if s.taskReceiptActive(ctx, look.ID) {
		s.finishTaskReceipt(ctx, t.ID, look.ID)
	}
	return true, nil
}

// cachedTaskForReceipt uses an existing link only; it never guesses one after a failed load.
func (s *Service) cachedTaskForReceipt(machine, sid string) (Todo, bool) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	id := s.todos.activeBySession[[2]string{machine, sid}]
	t := s.todos.todos[id]
	if t == nil || t.Origin == "" || t.DoneAt != nil {
		return Todo{}, false
	}
	return copyTodoLocked(t), true
}

// taskTurnEnded: one of the task's agents (sid on machine) ended a turn. Once none of them works or waits for the
// user any more, the conversation gets a receipt (once per stretch of work), and the todo is closed if the judge
// finds the work finished.
func (s *Service) taskTurnEnded(ctx context.Context, machine, sid string) {
	s.taskTurnEndedForLook(ctx, machine, sid, nil)
}

func (s *Service) taskTurnEndedForLook(ctx context.Context, machine, sid string, expected *receiptLook) {
	if expected != nil && expected.Todo != "" && expected.ID == "" && expected.RetryID == "" {
		copy := *expected
		copy.RetryID = "feed_" + randomHex(16)
		expected = &copy
	}
	time.Sleep(s.settleDelay(ctx)) // a turn that ends to ask, or goes on at once, is not the end
	if err := s.ensureTodosLoaded(ctx); err != nil {
		s.logf("task turn ended: load todos: %v", err)
		if expected == nil || expected.Todo == "" {
			if cached, ok := s.cachedTaskForReceipt(machine, sid); ok {
				s.tasks.mu.Lock()
				work, worked := s.tasks.working[cached.ID]
				s.tasks.mu.Unlock()
				if !worked {
					work = cached.CreatedAt
				}
				s.retryTaskReceipt(ctx, cached, machine, sid, work, nil)
			} else {
				s.logf("task turn ended: no known task association for %s/%s", machine, sid)
			}
		} else {
			s.retryTaskTurnEnded(ctx, machine, sid, expected)
		}
		return
	}
	t, ok := s.taskOf(ctx, machine, sid)
	if !ok {
		return
	}
	if expected != nil && expected.Todo == "" {
		s.logf("task turn ended: retry has no known task association for %s/%s", machine, sid)
		return
	}
	if !s.receiptTurnMatches(t, machine, sid, expected) {
		return
	}
	if handled, err := s.recoverTaskReceipt(ctx, t, expected); err != nil {
		s.logf("task %s: recover receipt: %v", t.ID, err)
		return
	} else if handled {
		return
	}
	agents, reached := s.taskAgentsReachedFirst(ctx, t, machine)
	waitingForLimit := false
	for _, c := range agents {
		if c.Status != proto.StatusDone && c.Status != proto.StatusIdle || c.State == proto.StateWorking ||
			c.State == proto.StateNeedsYou || c.PendingInteractions > 0 || backgroundRunning(c) {
			continue
		}
		if w := c.Work; w != nil && w.Phase == proto.WorkBlocked && w.On == proto.WaitTransient && w.WakeAt != nil {
			continue
		}
		now := time.Now()
		if !limitTakenUp(c, now) {
			continue
		}
		s.tasks.mu.Lock()
		current, observed := s.tasks.workIDs[t.ID]
		s.tasks.mu.Unlock()
		if expected != nil && !expected.Work.IsZero() && (!observed || current == expected.Generation) {
			s.registerTaskReceiptLook(ctx, &t, c.MachineID, c.SessionID, limitLookAgain(c, now), expected.Work, expected.Generation, false)
		} else {
			s.taskTurnEndedForTaskAfter(ctx, &t, c.MachineID, c.SessionID, limitLookAgain(c, now))
		}
		waitingForLimit = true
	}
	if !reached {
		if expected == nil {
			s.tasks.mu.Lock()
			work, worked := s.tasks.working[t.ID]
			s.tasks.mu.Unlock()
			if !worked {
				work = t.CreatedAt
			}
			s.retryTaskReceipt(ctx, t, machine, sid, work, nil)
		} else if expected.ID == "" {
			s.retryTaskTurnEnded(ctx, machine, sid, expected)
		}
		return
	}
	if !taskAgentsSettled(t, agents) {
		active := 0
		for _, a := range t.Agents {
			if a.ReleasedAt == nil && a.DismissedAt == nil {
				active++
			}
		}
		if len(agents) < active && (expected == nil || expected.ID == "") {
			s.tasks.mu.Lock()
			work, worked := s.tasks.working[t.ID]
			s.tasks.mu.Unlock()
			if !worked {
				work = t.CreatedAt
			}
			s.retryTaskReceipt(ctx, t, machine, sid, work, expected)
		} else if expected != nil && expected.ID == "" {
			s.retryTaskTurnEnded(ctx, machine, sid, expected)
		}
		return
	}
	if !s.receiptTurnMatches(t, machine, sid, expected) {
		return
	}
	if waitingForLimit {
		return
	}
	var last, cut proto.Session
	for _, c := range agents {
		if c.State == proto.StateWorking || c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
			return
		}
		if w := c.Work; w != nil && w.Phase == proto.WorkBlocked && w.On == proto.WaitTransient && w.WakeAt != nil {
			return // "Continue" goes by itself (agents_work.go): the receipt waits for how that ends
		}
		if c.UpdatedAt.After(last.UpdatedAt) {
			last = c
		}
		// The node's word, whatever the state now; or, once it stopped, Claude Code's last words when they are the
		// error ("API Error: …", "Not logged in · …"): the node's raw list has no turn error for those (fleet.go
		// turnError reads them for the apps).
		text, kind := strings.TrimSpace(c.TurnError), c.TurnErrorKind
		if text == "" {
			text, kind = turnError(c)
		}
		if text != "" && (cut.SessionID == "" || c.UpdatedAt.After(cut.UpdatedAt)) {
			cut = c
			cut.TurnError, cut.TurnErrorKind = text, kind
		}
	}
	s.tasks.mu.Lock()
	since, worked := s.tasks.working[t.ID]
	if !worked {
		since = t.CreatedAt // a hub restarted since it began: count from the start
		if expected != nil && !expected.Work.IsZero() {
			since = expected.Work
		}
	}
	generation := s.tasks.workIDs[t.ID]
	if expected != nil && expected.ID != "" {
		generation = expected.Generation
	}
	previous, hadPrevious := s.tasks.receipted[t.ID]
	previousGeneration, hadPreviousGeneration := s.tasks.receiptWorkIDs[t.ID]
	if hadPrevious && (hadPreviousGeneration && previousGeneration == generation ||
		!hadPreviousGeneration && !previous.Before(since)) {
		completed, done := s.tasks.receiptCompleted[t.ID]
		ref := SessionRef{MachineID: machine, SessionID: sid}
		otherAgent := expected != nil && expected.ID != "" && done && completed == generation && s.tasks.receiptCompletedRef[t.ID] != ref
		if !otherAgent {
			s.tasks.mu.Unlock()
			if done && completed == generation && expected != nil && expected.ID != "" {
				s.finishTaskReceipt(ctx, t.ID, expected.ID)
			}
			return
		}
	}
	reservation := time.Now()
	s.tasks.receipted[t.ID] = reservation
	if s.tasks.receiptWorkIDs == nil {
		s.tasks.receiptWorkIDs = make(map[string]string)
	}
	s.tasks.receiptWorkIDs[t.ID] = generation
	s.tasks.mu.Unlock()
	committed := false
	defer func() {
		if committed {
			return
		}
		s.tasks.mu.Lock()
		defer s.tasks.mu.Unlock()
		// A later turn may already have reserved its own receipt.
		if current, ok := s.tasks.receipted[t.ID]; ok && current == reservation {
			if hadPrevious {
				s.tasks.receipted[t.ID] = previous
			} else {
				delete(s.tasks.receipted, t.ID)
			}
			if hadPreviousGeneration {
				s.tasks.receiptWorkIDs[t.ID] = previousGeneration
			} else {
				delete(s.tasks.receiptWorkIDs, t.ID)
			}
		}
	}()
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	name := machine
	if m, ok := s.device(cctx, machine); ok {
		name = m.Name
	}
	if !s.taskWorkCurrent(t.ID, since) || !s.taskRuntimeSettled(cctx, t) {
		return
	}
	if cut.SessionID != "" {
		// A turn cut off by the model service is no end of the work: an interrupted receipt (with "Continue" on the card),
		// and nothing to judge or close (#1516).
		s.logf("task %s: interrupted: %s", t.ID, clip(oneLine(cut.TurnError), 200))
		where := [2]string{cut.MachineID, cut.SessionID}
		body := receiptBody{Title: t.Title, State: "interrupted", Summary: i18n.T(s.lang(cctx), "assistant-tasks.interrupted"), Machine: name,
			Error: clip(strings.TrimSpace(cut.TurnError), 300)}
		receiptID, err := s.appendTaskReceiptForLook(cctx, t, SessionRef{MachineID: machine, SessionID: sid}, body, since, worked, expected)
		if err != nil {
			s.logf("task %s: receipt: %v", t.ID, err)
			s.retryTaskReceipt(ctx, t, machine, sid, since, expected)
			return
		}
		if !s.taskReceiptActive(ctx, receiptID) {
			return
		}
		s.taskNotice(cctx, t, name, "interrupted", "", body.Error)
		if receiptFollowupCurrent(expected, receiptID) {
			s.taskReport(t, reportInterrupted, name, where, body.Error, nil)
		}
		if !s.taskReceiptActive(ctx, receiptID) {
			return
		}
		s.markReceiptCompleted(t.ID, generation, SessionRef{MachineID: machine, SessionID: sid})
		committed = true
		s.finishTaskReceipt(ctx, t.ID, receiptID)
		return
	}
	items, read := s.lastTurnItems(cctx, last.MachineID, last.SessionID)
	if !s.taskWorkCurrent(t.ID, since) || !s.taskRuntimeSettled(cctx, t) {
		return
	}
	if read && taskTurnAwaitingAnswer(items) && !silentTurn(items) {
		// Do not consume this stretch's receipt while the runtime is between tools and its answer.
		s.tasks.mu.Lock()
		if !s.tasks.working[t.ID].After(since) {
			delete(s.tasks.receipted, t.ID)
			delete(s.tasks.receiptWorkIDs, t.ID)
			if s.tasks.awaitingAnswer == nil {
				s.tasks.awaitingAnswer = make(map[SessionRef]bool)
			}
			s.tasks.awaitingAnswer[SessionRef{MachineID: machine, SessionID: sid}] = true
		}
		s.tasks.mu.Unlock()
		// The answer's update may have arrived during the transcript read, before
		// the retry was registered. Reconcile that update instead of waiting for another tick.
		if latest, ok := s.session(cctx, last.MachineID, last.SessionID); ok &&
			(latest.Status != last.Status || latest.Rev != last.Rev || latest.LastText != last.LastText || !latest.UpdatedAt.Equal(last.UpdatedAt)) &&
			s.takeTaskAnswerRetry(machine, sid) {
			go s.taskTurnEnded(context.WithoutCancel(ctx), machine, sid)
		}
		return
	}
	if read && silentTurn(items) {
		// No reply and no tool call, yet no error either (the model service answered 200 with nothing in it): no end
		// of the work. Interrupted, as a cut-off turn is, with nothing to judge or close (#2349).
		s.logf("task %s: interrupted: the turn had no output", t.ID)
		where := [2]string{last.MachineID, last.SessionID}
		l := s.lang(cctx)
		body := receiptBody{Title: t.Title, State: "interrupted", Summary: i18n.T(l, "assistant-tasks.interrupted"), Machine: name,
			Error: silentTurnErrorIn(l)}
		receiptID, err := s.appendTaskReceiptForLook(cctx, t, SessionRef{MachineID: machine, SessionID: sid}, body, since, worked, expected)
		if err != nil {
			s.logf("task %s: receipt: %v", t.ID, err)
			s.retryTaskReceipt(ctx, t, machine, sid, since, expected)
			return
		}
		if !s.taskReceiptActive(ctx, receiptID) {
			return
		}
		s.taskNotice(cctx, t, name, "interrupted", "", body.Error)
		if receiptFollowupCurrent(expected, receiptID) {
			s.taskReport(t, reportInterrupted, name, where, body.Error, nil)
		}
		if !s.taskReceiptActive(ctx, receiptID) {
			return
		}
		s.markReceiptCompleted(t.ID, generation, SessionRef{MachineID: machine, SessionID: sid})
		committed = true
		s.finishTaskReceipt(ctx, t.ID, receiptID)
		return
	}
	// The whole last reply, as Markdown: the session list's last_text is one line cut at 200 characters.
	text := strings.TrimSpace(last.LastText)
	if _, answer := turnExchange(items); answer != "" {
		text = answer
	}
	body := receiptBody{Title: t.Title, State: "done", Summary: clipMarkdown(text, 600), Machine: name}
	// Persona follows up on a supervised stop through sigReview or sigIdle after judgment; the receipt need not wait.
	// needs_user waits on the user instead. Read the latest todo in case it changed while reading the reply.
	if current, ok := s.todoNow(t.ID); ok && current.DoneAt == nil {
		if v := current.Supervision; v != nil && v.state(current) != reviewNeedsUser {
			state := "checking" // receipt stays ahead of the model; a stopped turn is not a completion verdict
			if v.state(current) == reviewPending {
				state = "awaiting"
			}
			body.Review = &receiptReview{State: state, Criteria: len(v.DoneWhen)}
		}
	}
	if body.Summary == "" {
		body.Summary = i18n.T(s.lang(cctx), "assistant-tasks.done")
	}
	if pr := prLink.FindAllString(text, -1); len(pr) > 0 {
		body.PR = pr[len(pr)-1]
	}
	receiptID, err := s.appendTaskReceiptForLook(cctx, t, SessionRef{MachineID: machine, SessionID: sid}, body, since, worked, expected)
	if err != nil {
		s.logf("task %s: receipt: %v", t.ID, err)
		s.retryTaskReceipt(ctx, t, machine, sid, since, expected)
		return
	}
	if !s.taskReceiptActive(ctx, receiptID) {
		return
	}
	s.taskNotice(cctx, t, name, "done", "", clipMarkdown(text, 1500))
	if receiptFollowupCurrent(expected, receiptID) {
		stage, pr := stageOf(text)
		s.setTaskStage(cctx, t.ID, stage, pr)
		s.taskReport(t, reportDone, name, [2]string{last.MachineID, last.SessionID}, text, nil)
		if stageIndex(stage) >= stageIndex("deploy") {
			go s.askAcceptance(t, last.MachineID, last.SessionID, text) // the user tries it (acceptance.go)
		}
		s.autoFinishTask(t, since, worked) // the work may be finished: then the todo is done (done.go)
	} else {
		s.logf("task %s: receipt judgment skipped: current work generation is not confirmed", t.ID)
	}
	if !s.taskReceiptActive(ctx, receiptID) {
		return
	}
	s.markReceiptCompleted(t.ID, generation, SessionRef{MachineID: machine, SessionID: sid})
	committed = true
	s.finishTaskReceipt(ctx, t.ID, receiptID)
}

// A retry or durable look can outlive another manager's unseen turn, so its
// local generation alone cannot authorize changes to current task state.
func receiptFollowupCurrent(expected *receiptLook, receiptID string) bool {
	return expected == nil && receiptID == ""
}

func (s *Service) markReceiptCompleted(todoID, generation string, ref SessionRef) {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	if current, ok := s.tasks.receiptWorkIDs[todoID]; !ok || current != generation {
		return
	}
	if s.tasks.receiptCompleted == nil {
		s.tasks.receiptCompleted = make(map[string]string)
		s.tasks.receiptCompletedRef = make(map[string]SessionRef)
	}
	s.tasks.receiptCompleted[todoID] = generation
	s.tasks.receiptCompletedRef[todoID] = ref
}

func (s *Service) taskReceiptActive(ctx context.Context, receiptID string) bool {
	if receiptID == "" {
		return true
	}
	s.tasks.receiptMu.Lock()
	lifetime := s.tasks.receiptCtx
	s.tasks.receiptMu.Unlock()
	if lifetime == nil {
		lifetime = ctx
	}
	return lifetime.Err() == nil
}

// appendTaskReceipt uses a pending look's ID for both ordinary stops and timer checks.
func (s *Service) appendTaskReceipt(ctx context.Context, t Todo, ref SessionRef, body receiptBody) (string, error) {
	return s.appendTaskReceiptForLook(ctx, t, ref, body, time.Time{}, false, nil)
}

func (s *Service) appendTaskReceiptForLook(ctx context.Context, t Todo, ref SessionRef, body receiptBody, work time.Time, worked bool, expected *receiptLook) (string, error) {
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	if !work.IsZero() && !s.taskWorkCurrent(t.ID, work) {
		return "", errors.New("task work changed")
	}
	current, ok := s.taskOf(ctx, ref.MachineID, ref.SessionID)
	currentLink, linkedOK := receiptLink(current, ref)
	oldLink, wasLinked := receiptLink(t, ref)
	if expected != nil && (!ok || current.ID != t.ID || !linkedOK || !wasLinked || !currentLink.Equal(oldLink) ||
		t.ID != expected.Todo || !oldLink.Equal(expected.Linked)) {
		return "", errors.New("receipt look association changed")
	}
	s.tasks.receiptMu.Lock()
	defer s.tasks.receiptMu.Unlock()
	if err := s.receiptLooksLocked(ctx); err != nil {
		return "", err
	}
	if expected != nil && expected.Todo != "" && expected.ID == "" && expected.RetryID != "" {
		return s.appendBoundTaskReceipt(ctx, t, ref, oldLink, expected.RetryID, body)
	}
	var look receiptLook
	pending := false
	s.tasks.mu.Lock()
	generation := s.tasks.workIDs[t.ID]
	s.tasks.mu.Unlock()
	for _, candidate := range s.tasks.receiptLooks {
		if candidate.Todo != t.ID {
			continue
		}
		pending = true
		if candidate.Machine != ref.MachineID || candidate.Session != ref.SessionID {
			continue
		}
		if expected != nil && expected.ID != "" {
			if candidate.ID != expected.ID {
				continue
			}
		} else if worked && candidate.Generation != generation {
			continue
		}
		look = candidate
		break
	}
	if expected != nil && expected.ID != "" && look.ID == "" {
		return "", errors.New("receipt look was replaced")
	}
	if look.ID == "" {
		if pending {
			if !ok || current.ID != t.ID || !linkedOK || !wasLinked || !currentLink.Equal(oldLink) {
				return "", errors.New("receipt task association changed")
			}
			return s.appendBoundTaskReceipt(ctx, t, ref, oldLink, "feed_"+randomHex(16), body)
		}
		if _, atomic := s.store().(interface {
			AppendTaskReceiptOnce(context.Context, db.FeedRow, string, string, time.Time) (bool, bool, error)
		}); atomic || s.todoDB() != nil {
			if !ok || current.ID != t.ID || !linkedOK || !wasLinked || !currentLink.Equal(oldLink) {
				return "", errors.New("receipt task association changed")
			}
			_, err := s.appendBoundTaskReceipt(ctx, t, ref, oldLink, "feed_"+randomHex(16), body)
			return "", err
		}
		_, err := s.appendFeed(ctx, t.Origin, feedReceipt, t.ID, body)
		return "", err
	}
	// Refuse a stale task snapshot after an unlink, transfer, or close.
	valid, err := s.receiptLookLinked(ctx, look)
	if err != nil {
		return "", err
	}
	if !valid {
		return "", errors.New("receipt look no longer belongs to this task")
	}
	type onceStore interface {
		AppendFeedOnce(context.Context, db.FeedRow) (bool, error)
	}
	st, ok := s.store().(onceStore)
	if !ok {
		if s.store() != nil {
			return "", errors.New("receipt looks require idempotent feed writes")
		}
		_, err := s.appendFeed(ctx, t.Origin, feedReceipt, t.ID, body)
		return look.ID, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	// A local generation cannot exclude another manager seeing a newer turn.
	// Keep the fresh observation separate from the older wait's receipt.
	feedID := receiptRecheckID(look.ID)
	item := FeedItem{ID: feedID, Conversation: t.Origin, At: time.Now().UTC(), Kind: feedReceipt, Ref: t.ID, Body: raw}
	row := db.FeedRow{ID: item.ID, Conversation: item.Conversation, At: item.At, Kind: item.Kind, Ref: item.Ref, Body: item.Body}
	var inserted bool
	if atomicStore, ok := s.store().(interface {
		AppendTaskReceiptOnce(context.Context, db.FeedRow, string, string, time.Time) (bool, bool, error)
	}); ok {
		var valid bool
		inserted, valid, err = atomicStore.AppendTaskReceiptOnce(ctx, row, look.Machine, look.Session, look.Linked)
		if err == nil && !valid {
			return "", errors.New("receipt task association changed")
		}
	} else if s.todoDB() != nil {
		return "", errors.New("receipt storage cannot atomically validate task association")
	} else {
		inserted, err = st.AppendFeedOnce(ctx, row)
	}
	if err != nil {
		return "", err
	}
	if inserted {
		s.event(ctx, map[string]any{"type": "assistant_feed", "conversation": t.Origin, "item": item})
	}
	// A duplicate feed row can mean the process stopped before doing the follow-up.
	// Keep the wait until the caller has attempted that work.
	return look.ID, nil
}

// appendBoundTaskReceipt preserves the association check even when a retry has no durable look.
func (s *Service) appendBoundTaskReceipt(ctx context.Context, t Todo, ref SessionRef, linked time.Time, id string, body receiptBody) (string, error) {
	st := s.store()
	if st == nil {
		_, err := s.appendFeed(ctx, t.Origin, feedReceipt, t.ID, body)
		return id, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	item := FeedItem{ID: id, Conversation: t.Origin, At: time.Now().UTC(), Kind: feedReceipt, Ref: t.ID, Body: raw}
	row := db.FeedRow{ID: item.ID, Conversation: item.Conversation, At: item.At, Kind: item.Kind, Ref: item.Ref, Body: item.Body}
	var inserted bool
	if atomicStore, ok := st.(interface {
		AppendTaskReceiptOnce(context.Context, db.FeedRow, string, string, time.Time) (bool, bool, error)
	}); ok {
		var valid bool
		inserted, valid, err = atomicStore.AppendTaskReceiptOnce(ctx, row, ref.MachineID, ref.SessionID, linked)
		if err == nil && !valid {
			return "", errors.New("receipt task association changed")
		}
	} else if s.todoDB() != nil {
		return "", errors.New("receipt storage cannot atomically validate task association")
	} else if once, ok := st.(interface {
		AppendFeedOnce(context.Context, db.FeedRow) (bool, error)
	}); ok {
		inserted, err = once.AppendFeedOnce(ctx, row)
	} else {
		return "", errors.New("receipt storage cannot deduplicate bound retries")
	}
	if err != nil {
		return "", err
	}
	if inserted {
		s.event(ctx, map[string]any{"type": "assistant_feed", "conversation": t.Origin, "item": item})
	}
	return id, nil
}

func (s *Service) finishTaskReceipt(ctx context.Context, todoID, receiptID string) {
	if receiptID == "" {
		return
	}
	s.tasks.receiptMu.Lock()
	defer s.tasks.receiptMu.Unlock()
	lifetime := s.tasks.receiptCtx
	if lifetime == nil {
		lifetime = context.WithoutCancel(ctx)
	}
	if lifetime.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(lifetime, 30*time.Second)
	defer cancel()
	retry := func() {
		if lifetime.Err() == nil {
			s.finishTaskReceipt(lifetime, todoID, receiptID)
		}
	}
	if err := s.receiptLooksLocked(ctx); err != nil {
		s.logf("task %s: load receipt look to finish: %v", todoID, err)
		time.AfterFunc(time.Minute, retry)
		return
	}
	changed := false
	for ref, look := range s.tasks.receiptLooks {
		if look.Todo == todoID && look.ID == receiptID {
			delete(s.tasks.receiptLooks, ref)
			changed = true
		}
	}
	if changed {
		if err := s.saveReceiptLooksLocked(ctx); err != nil {
			s.logf("task %s: finish receipt look: %v", todoID, err)
			time.AfterFunc(time.Minute, retry)
		}
	}
}

// taskTurnEndedAfter has taskTurnEnded look at sid's task again at at. What happened meanwhile decides as ever: a turn
// that went on and ended had its own receipt, one that works again has none yet. A look already set for as early is
// enough (one still waiting then sets the next): the wait's wakes (a background task reporting, a /loop) end a turn
// each, and each comes here.
const receiptLooksSetting = "tasks.receipt_looks"

type receiptLook struct {
	Machine    string    `json:"machine"`
	Session    string    `json:"session"`
	Todo       string    `json:"todo"`
	Linked     time.Time `json:"linked"`
	Work       time.Time `json:"work,omitempty"`
	Generation string    `json:"generation,omitempty"`
	ID         string    `json:"id"`
	At         time.Time `json:"at"`
	Attempts   int       `json:"attempts,omitempty"`
	RetryID    string    `json:"-"`
}

type receiptKey struct {
	Ref        SessionRef
	Generation string
}

func receiptKeyOf(look receiptLook) receiptKey {
	return receiptKey{Ref: SessionRef{MachineID: look.Machine, SessionID: look.Session}, Generation: look.Generation}
}

func receiptLink(t Todo, ref SessionRef) (time.Time, bool) {
	for _, a := range t.Agents {
		if a.MachineID == ref.MachineID && a.SessionID == ref.SessionID && a.ReleasedAt == nil && a.DismissedAt == nil {
			return a.LinkedAt.UTC().Truncate(time.Microsecond), true
		}
	}
	return time.Time{}, false
}

type receiptLookState struct {
	Revision int64         `json:"revision"`
	Looks    []receiptLook `json:"looks"`
}

func receiptLookMap(state receiptLookState) map[receiptKey]receiptLook {
	out := make(map[receiptKey]receiptLook, len(state.Looks))
	for _, look := range state.Looks {
		out[receiptKeyOf(look)] = look
	}
	return out
}

func copyReceiptLooks(m map[receiptKey]receiptLook) map[receiptKey]receiptLook {
	out := make(map[receiptKey]receiptLook, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func sameReceiptLook(a, b receiptLook) bool {
	return a.Machine == b.Machine && a.Session == b.Session && a.Todo == b.Todo && a.ID == b.ID && a.Generation == b.Generation &&
		a.Linked.Equal(b.Linked) && a.Work.Equal(b.Work) && a.At.Equal(b.At) && a.Attempts == b.Attempts
}

// receiptLookLinked checks the stored association, not a manager's possibly stale todo cache.
func (s *Service) receiptLookLinked(ctx context.Context, look receiptLook) (bool, error) {
	if st := s.todoDB(); st != nil {
		todos, err := st.ListTodos(ctx)
		if err != nil {
			return false, err
		}
		active := false
		for _, t := range todos {
			if t.ID == look.Todo && t.DoneAt == nil && t.Origin != "" {
				active = true
				break
			}
		}
		if !active {
			return false, nil
		}
		agents, err := st.ListAllTodoAgents(ctx)
		if err != nil {
			return false, err
		}
		for _, a := range agents {
			if a.TodoID == look.Todo && a.MachineID == look.Machine && a.SessionID == look.Session && a.ReleasedAt == nil && a.DismissedAt == nil && a.LinkedAt.Equal(look.Linked) {
				return true, nil
			}
		}
		return false, nil
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return false, err
	}
	t, ok := s.taskOf(ctx, look.Machine, look.Session)
	linked, found := receiptLink(t, SessionRef{MachineID: look.Machine, SessionID: look.Session})
	return ok && t.ID == look.Todo && found && linked.Equal(look.Linked), nil
}

// receiptLookSnapshot reads active tasks and links once for a pruning pass.
func receiptLookSnapshot(ctx context.Context, st todoStore) (map[string]bool, map[[3]string]time.Time, error) {
	todos, err := st.ListTodos(ctx)
	if err != nil {
		return nil, nil, err
	}
	agents, err := st.ListAllTodoAgents(ctx)
	if err != nil {
		return nil, nil, err
	}
	active := make(map[string]bool, len(todos))
	for _, t := range todos {
		if t.DoneAt == nil && t.Origin != "" {
			active[t.ID] = true
		}
	}
	links := make(map[[3]string]time.Time, len(agents))
	for _, a := range agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil {
			links[[3]string{a.TodoID, a.MachineID, a.SessionID}] = a.LinkedAt
		}
	}
	return active, links, nil
}

// pruneReceiptLooks removes waits whose todo or association was cancelled.
func (s *Service) pruneReceiptLooks(ctx context.Context) {
	s.tasks.receiptMu.Lock()
	defer s.tasks.receiptMu.Unlock()
	if err := s.receiptLooksLocked(ctx); err != nil {
		s.logf("task receipt looks: prune: %v", err)
		return
	}
	if len(s.tasks.receiptLooks) == 0 {
		return
	}
	var active map[string]bool
	var links map[[3]string]time.Time
	if st := s.todoDB(); st != nil {
		var err error
		active, links, err = receiptLookSnapshot(ctx, st)
		if err != nil {
			s.logf("task receipt looks: read links: %v", err)
			return
		}
	}
	remove := make(map[receiptKey]receiptLook)
	for ref, look := range s.tasks.receiptLooks {
		if active != nil && active[look.Todo] {
			linked, ok := links[[3]string{look.Todo, look.Machine, look.Session}]
			if ok && linked.Equal(look.Linked) {
				continue
			}
		}
		// A batch can predate a reopen or relink. Recheck candidates before
		// removing them rather than treating the snapshot as current.
		valid, err := s.receiptLookLinked(ctx, look)
		if err != nil {
			s.logf("task %s: check receipt link: %v", look.Todo, err)
			continue
		}
		if !valid {
			remove[ref] = look
		}
	}
	for ref := range remove {
		delete(s.tasks.receiptLooks, ref)
	}
	if len(remove) != 0 {
		if err := s.saveReceiptLooksLocked(ctx); err != nil {
			for ref, look := range remove {
				s.tasks.receiptLooks[ref] = look
			}
			s.logf("task receipt looks: prune: %v", err)
		}
	}
}

// receiptLooksLocked refreshes across overlapping managers before each decision.
func (s *Service) receiptLooksLocked(ctx context.Context) error {
	if s.store() == nil {
		if !s.tasks.receiptLoaded {
			s.tasks.receiptLooks = make(map[receiptKey]receiptLook)
			s.tasks.receiptLoaded = true
		}
		s.tasks.receiptBase = copyReceiptLooks(s.tasks.receiptLooks)
		return nil
	}
	var state receiptLookState
	if _, err := s.store().Setting(ctx, receiptLooksSetting, &state); err != nil {
		return err
	}
	s.tasks.receiptLooks = receiptLookMap(state)
	s.tasks.receiptBase = copyReceiptLooks(s.tasks.receiptLooks)
	s.tasks.receiptLoaded = true
	return nil
}

// saveReceiptLooksLocked applies only this manager's changes to the latest revision.
func (s *Service) saveReceiptLooksLocked(ctx context.Context) error {
	st := s.store()
	if st == nil {
		s.tasks.receiptBase = copyReceiptLooks(s.tasks.receiptLooks)
		return nil
	}
	cas, ok := st.(interface {
		CompareSetting(context.Context, string, int64, any) (bool, error)
	})
	if !ok {
		return errors.New("receipt looks require conditional setting updates")
	}
	base, desired := s.tasks.receiptBase, copyReceiptLooks(s.tasks.receiptLooks)
	keys := make(map[receiptKey]bool, len(base)+len(desired))
	for k := range base {
		keys[k] = true
	}
	for k := range desired {
		keys[k] = true
	}
	for attempt := 0; attempt < 8; attempt++ {
		var state receiptLookState
		if _, err := st.Setting(ctx, receiptLooksSetting, &state); err != nil {
			return err
		}
		latest := receiptLookMap(state)
		for k := range keys {
			old, had := base[k]
			next, wants := desired[k]
			if had == wants && (!had || sameReceiptLook(old, next)) {
				continue
			}
			current, has := latest[k]
			if has != had || has && !sameReceiptLook(current, old) {
				if has == wants && (!has || sameReceiptLook(current, next)) {
					continue
				}
				return fmt.Errorf("receipt look changed concurrently")
			}
			if wants {
				latest[k] = next
			} else {
				delete(latest, k)
			}
		}
		updated := receiptLookState{Revision: state.Revision + 1}
		for _, look := range latest {
			updated.Looks = append(updated.Looks, look)
		}
		won, err := cas.CompareSetting(ctx, receiptLooksSetting, state.Revision, updated)
		if err != nil {
			return err
		}
		if won {
			s.tasks.receiptLooks = latest
			s.tasks.receiptBase = copyReceiptLooks(latest)
			return nil
		}
	}
	return errors.New("receipt looks changed repeatedly")
}

func (s *Service) taskTurnEndedAfter(ctx context.Context, machine, sid string, at time.Time) {
	s.taskTurnEndedForTaskAfter(ctx, nil, machine, sid, at)
}

func (s *Service) taskTurnEndedForTaskAfter(ctx context.Context, expected *Todo, machine, sid string, at time.Time) {
	var work time.Time
	var generation string
	if expected != nil {
		work = expected.CreatedAt
		s.tasks.mu.Lock()
		if started, ok := s.tasks.working[expected.ID]; ok {
			work = started
		}
		generation = s.tasks.workIDs[expected.ID]
		s.tasks.mu.Unlock()
	}
	s.registerTaskReceiptLook(ctx, expected, machine, sid, at, work, generation, false)
}

func (s *Service) registerTaskReceiptLook(ctx context.Context, expected *Todo, machine, sid string, at, work time.Time, generation string, retrying bool) {
	ref := SessionRef{MachineID: machine, SessionID: sid}
	s.tasks.receiptMu.Lock()
	defer s.tasks.receiptMu.Unlock()
	timerCtx := s.tasks.receiptCtx
	if timerCtx == nil {
		timerCtx = context.WithoutCancel(ctx)
	}
	retry := func() {
		if timerCtx.Err() == nil {
			s.registerTaskReceiptLook(timerCtx, expected, machine, sid, at, work, generation, true)
		}
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		s.logf("task receipt looks: load todos: %v", err)
		time.AfterFunc(time.Minute, retry)
		return
	}
	t, found := s.taskOf(ctx, machine, sid)
	linked, linkedOK := receiptLink(t, ref)
	if expected != nil {
		expectedLink, ok := receiptLink(*expected, ref)
		if !ok {
			return
		}
		valid, err := s.receiptLookLinked(ctx, receiptLook{Machine: machine, Session: sid, Todo: expected.ID, Linked: expectedLink})
		if err != nil {
			s.logf("task %s: check receipt link: %v", expected.ID, err)
			time.AfterFunc(time.Minute, retry)
			return
		}
		if !valid {
			return
		}
		if !found || t.ID != expected.ID || !linkedOK || !linked.Equal(expectedLink) {
			t, found, linked, linkedOK = *expected, true, expectedLink, true
		}
	}
	if (!found || !linkedOK) && s.store() != nil {
		s.logf("task receipt look: no active task for %s/%s", machine, sid)
		return
	}
	if work.IsZero() && found {
		work = t.CreatedAt
	}
	if expected != nil {
		s.tasks.mu.Lock()
		currentGeneration, observed := s.tasks.workIDs[t.ID]
		s.tasks.mu.Unlock()
		if observed && currentGeneration != generation {
			return
		}
	}
	if expected == nil && found {
		s.tasks.mu.Lock()
		generation = s.tasks.workIDs[t.ID]
		s.tasks.mu.Unlock()
	}
	if retrying && expected != nil {
		s.tasks.mu.Lock()
		receiptGeneration, hasReceipt := s.tasks.receiptWorkIDs[t.ID]
		s.tasks.mu.Unlock()
		if hasReceipt && receiptGeneration == generation {
			return
		}
		checkCtx, cancel := context.WithTimeout(timerCtx, agentReachMax)
		sessions, reached := s.sessionsReached(checkCtx, machine)
		cancel()
		if !reached {
			time.AfterFunc(time.Minute, retry)
			return
		}
		waiting, present, cancelled := false, false, false
		for _, c := range sessions {
			if c.SessionID != sid {
				continue
			}
			present = true
			now := time.Now()
			if limitTakenUp(c, now) {
				waiting = true
				break
			}
			// The same limit may have passed its deadline, or its automatic
			// continuation may have been cancelled while storage was down.
			if reset, ok := usageLimitReset(c); ok && reset.Add(limitResumeGrace).Equal(at) {
				if !now.Before(at) {
					waiting = true
				} else {
					cancelled = true
				}
			}
			break
		}
		if !present {
			time.AfterFunc(time.Minute, retry)
			return
		}
		if cancelled {
			bound := receiptLook{Machine: machine, Session: sid, Todo: t.ID, Linked: linked, Work: work, Generation: generation}
			time.AfterFunc(0, func() {
				if timerCtx.Err() == nil {
					s.taskTurnEndedForLook(timerCtx, machine, sid, &bound)
				}
			})
			return
		}
		if !waiting {
			return
		}
	}
	if err := s.receiptLooksLocked(ctx); err != nil {
		s.logf("task receipt looks: read: %v", err)
		time.AfterFunc(time.Minute, retry)
		return
	}
	key := receiptKey{Ref: ref, Generation: generation}
	pending, exists := s.tasks.receiptLooks[key]
	if exists && pending.Todo == t.ID && pending.Linked.Equal(linked) && !pending.At.After(at) {
		return
	}
	if !found {
		s.tasks.mu.Lock()
		old, exists := s.tasks.limitLooks[ref]
		s.tasks.mu.Unlock()
		if exists && !old.After(at) {
			return
		}
	}
	look := receiptLook{Machine: machine, Session: sid, Todo: t.ID, Linked: linked, Work: work, Generation: generation, ID: "feed_" + randomHex(16), At: at.UTC()}
	if exists && pending.Todo == t.ID && pending.Linked.Equal(linked) {
		look.ID = pending.ID
	}
	// Unit tests may set up an unassociated timer; it remains memory-only.
	if found && linkedOK {
		previous, had := s.tasks.receiptLooks[key]
		s.tasks.receiptLooks[key] = look
		if err := s.saveReceiptLooksLocked(ctx); err != nil {
			if had {
				s.tasks.receiptLooks[key] = previous
			} else {
				delete(s.tasks.receiptLooks, key)
			}
			s.logf("task %s: save receipt look: %v", t.ID, err)
			time.AfterFunc(time.Minute, retry)
			return
		}
		look = s.tasks.receiptLooks[key]
	}
	s.armReceiptLook(timerCtx, look, at)
}

// restoreTaskReceiptLooks arms persisted rechecks and returns without waiting for their deadlines.
func (s *Service) restoreTaskReceiptLooks(ctx context.Context) {
	s.tasks.receiptMu.Lock()
	defer s.tasks.receiptMu.Unlock()
	if s.tasks.receiptCtx != nil && s.tasks.receiptCtx.Err() != nil {
		s.tasks.mu.Lock()
		s.tasks.limitLooks = nil
		s.tasks.limitLookTokens = nil
		s.tasks.receiptTimers = nil
		s.tasks.mu.Unlock()
	}
	s.tasks.receiptCtx = ctx
	if err := s.receiptLooksLocked(ctx); err != nil {
		s.logf("task receipt looks: restore: %v", err)
		time.AfterFunc(time.Minute, func() {
			if ctx.Err() == nil {
				s.restoreTaskReceiptLooks(ctx)
			}
		})
		return
	}
	for _, look := range s.tasks.receiptLooks {
		at := look.At
		if at.Before(time.Now()) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(look.ID))
			at = time.Now().Add(time.Duration(h.Sum32()%30000) * time.Millisecond)
		}
		key := receiptKeyOf(look)
		s.tasks.mu.Lock()
		armed, ok := s.tasks.receiptTimers[key]
		s.tasks.mu.Unlock()
		if ok && !armed.After(at) {
			continue
		}
		s.armReceiptLook(ctx, look, at)
	}
	// Another manager can register a wait during a blue-green overlap.
	time.AfterFunc(time.Minute, func() {
		if ctx.Err() == nil {
			s.restoreTaskReceiptLooks(ctx)
		}
	})
}

// armReceiptLook retains the durable record until the check resolves. A failed or offline check is retried.
func (s *Service) armReceiptLook(ctx context.Context, look receiptLook, at time.Time) {
	ref := SessionRef{MachineID: look.Machine, SessionID: look.Session}
	key := receiptKeyOf(look)
	s.tasks.mu.Lock()
	if s.tasks.limitLooks == nil {
		s.tasks.limitLooks = map[SessionRef]time.Time{}
		s.tasks.limitLookTokens = map[receiptKey]string{}
		s.tasks.receiptTimers = map[receiptKey]time.Time{}
	}
	token := randomHex(16)
	s.tasks.limitLooks[ref] = at
	s.tasks.receiptTimers[key] = at
	s.tasks.limitLookTokens[key] = token
	s.tasks.mu.Unlock()
	time.AfterFunc(time.Until(at), func() {
		s.tasks.mu.Lock()
		if s.tasks.limitLookTokens[key] != token {
			s.tasks.mu.Unlock()
			return
		}
		delete(s.tasks.limitLooks, ref)
		delete(s.tasks.receiptTimers, key)
		delete(s.tasks.limitLookTokens, key)
		s.tasks.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if look.Todo != "" {
			s.tasks.receiptMu.Lock()
			err := s.receiptLooksLocked(ctx)
			current, ok := s.tasks.receiptLooks[key]
			s.tasks.receiptMu.Unlock()
			if err != nil {
				s.armReceiptLook(ctx, look, time.Now().Add(time.Minute))
				return
			}
			if !ok || current.ID != look.ID || !current.At.Equal(look.At) {
				return
			}
			valid, err := s.receiptLookLinked(ctx, look)
			if err == nil && !valid {
				s.clearReceiptLook(ctx, key, look.ID)
				return
			}
			if err != nil {
				s.armReceiptLook(ctx, look, time.Now().Add(time.Minute))
				return
			}
		}
		if look.Todo == "" {
			s.taskTurnEnded(ctx, look.Machine, look.Session)
		} else {
			s.taskTurnEndedForLook(ctx, look.Machine, look.Session, &look)
		}
		if look.Todo == "" || ctx.Err() != nil {
			return
		}
		s.tasks.receiptMu.Lock()
		defer s.tasks.receiptMu.Unlock()
		if err := s.receiptLooksLocked(ctx); err != nil {
			s.armReceiptLook(ctx, look, time.Now().Add(time.Minute))
			return
		}
		current, ok := s.tasks.receiptLooks[key]
		if !ok || current.ID != look.ID || !current.At.Equal(look.At) {
			return
		}
		if current.Attempts < 6 {
			current.Attempts++
		}
		delay := time.Minute * time.Duration(1<<current.Attempts)
		current.At = time.Now().Add(delay).UTC()
		s.tasks.receiptLooks[key] = current
		if err := s.saveReceiptLooksLocked(ctx); err != nil {
			s.logf("task %s: save receipt retry: %v", look.Todo, err)
			s.armReceiptLook(ctx, look, time.Now().Add(time.Minute))
			return
		}
		s.armReceiptLook(ctx, current, current.At)
	})
}

func (s *Service) clearReceiptLook(ctx context.Context, key receiptKey, id string) {
	s.tasks.receiptMu.Lock()
	defer s.tasks.receiptMu.Unlock()
	if err := s.receiptLooksLocked(ctx); err != nil {
		return
	}
	look, ok := s.tasks.receiptLooks[key]
	if !ok || look.ID != id {
		return
	}
	delete(s.tasks.receiptLooks, key)
	if err := s.saveReceiptLooksLocked(ctx); err != nil {
		s.tasks.receiptLooks[key] = look
		s.logf("task %s: clear receipt look: %v", look.Todo, err)
	}
}

// routeToTask sends text straight to the agent of task todoID that most needs it, and keeps it in the conversation as
// the user's (no from: a task card's Message, or a decision the owner made in Lark).
func (s *Service) routeToTask(ctx context.Context, conversation, todoID, text string) (FeedItem, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return FeedItem{}, err
	}
	t, ok, err := s.todo(ctx, todoID)
	if err != nil {
		return FeedItem{}, err
	}
	if !ok || t.Origin != conversation {
		return FeedItem{}, i18n.Err("assistant-tasks.route_no_task")
	}
	agents := s.taskAgents(ctx, t)
	if len(agents) == 0 {
		return FeedItem{}, i18n.Err("assistant-tasks.route_no_agent")
	}
	c := pickTaskAgent(agents)
	if _, ok := s.device(ctx, c.MachineID); !ok {
		return FeedItem{}, i18n.Err("assistant-tasks.route_offline")
	}
	// An agent closed after its task was done (tasks_close.go) is resumed first: that can take minutes, and goes
	// on when the phone stops waiting, as a send from the chat does.
	wait := 90 * time.Second
	if c.Status == proto.StatusOffline {
		wait = 5 * time.Minute
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait)
	defer cancel()
	if err := s.call(sctx, c.MachineID, DeviceSessionSend, map[string]any{"session_id": c.SessionID, "text": text}, nil); err != nil {
		return FeedItem{}, err
	}
	return s.appendFeed(sctx, conversation, feedRouted, todoID, map[string]string{"text": text})
}
