package node

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
)

// questionCalls is the AskUserQuestion lifecycle across PreToolUse, PermissionRequest and PostToolUse. PermissionRequest
// omits tool_use_id, so PreToolUse supplies it and attempt keeps it across reconnects. PostToolUse completes only the
// same id: a late receipt for one identical question can never cancel the next (#2382).
type questionCalls struct {
	mu            sync.Mutex
	pending       map[string][]questionID      // PreToolUse ids awaiting PermissionRequest, by permissionKey
	attempts      map[string]questionID        // assigned ids, by permissionKey + hook attempt
	waiting       map[string][]*questionWaiter // live PermissionRequests, by permissionKey
	completed     map[string]time.Time         // PostToolUse receipts, by permissionKey + toolUseID
	handedBack    map[string]time.Time         // ambiguous attempts handed to the terminal, by permissionKey + attempt
	handedBackIDs map[string]time.Time         // ambiguous pending calls, by permissionKey + toolUseID
}

type questionID struct {
	id string
	at time.Time
}

type questionWaiter struct {
	id         string
	attemptKey string
	cancel     context.CancelFunc
	answered   bool
}

func questionAttemptKey(key, attempt string) string {
	if attempt == "" {
		return ""
	}
	return key + "\x00attempt:" + attempt
}

func questionExpired(at, now time.Time) bool {
	// A phone answer may arrive after the whole hook timeout, and standby may replay several minutes of history.
	return now.Sub(at) > nodeclient.HookTimeout+nodeclient.ResumeWait
}

func freshQuestionIDs(ids []questionID, now time.Time) []questionID {
	i := 0
	for i < len(ids) && questionExpired(ids[i].at, now) {
		i++
	}
	return ids[i:]
}

// forget expires state older than a hook's reconnect window. q.mu is held.
func (q *questionCalls) forget(now time.Time) {
	for key, ids := range q.pending {
		if ids = freshQuestionIDs(ids, now); len(ids) == 0 {
			delete(q.pending, key)
		} else {
			q.pending[key] = ids
		}
	}
	for key, id := range q.attempts {
		if questionExpired(id.at, now) {
			delete(q.attempts, key)
		}
	}
	for key, at := range q.completed {
		if questionExpired(at, now) {
			delete(q.completed, key)
		}
	}
	for key, at := range q.handedBack {
		if questionExpired(at, now) {
			delete(q.handedBack, key)
		}
	}
	for key, at := range q.handedBackIDs {
		if questionExpired(at, now) {
			delete(q.handedBackIDs, key)
		}
	}
}

// before remembers a PreToolUse id. Duplicate reports of the same call share one entry.
func (q *questionCalls) before(in hookInput, now time.Time) {
	if in.ToolUseID == "" {
		return
	}
	key := permissionKey(in)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.forget(now)
	for _, id := range q.pending[key] {
		if id.id == in.ToolUseID {
			return
		}
	}
	for _, id := range q.attempts {
		if id.id == in.ToolUseID {
			return
		}
	}
	if q.pending == nil {
		q.pending = map[string][]questionID{}
	}
	q.pending[key] = append(q.pending[key], questionID{id: in.ToolUseID, at: now})
	// A delayed replay carries the event's original time: it belongs before a live PreToolUse that arrived first.
	sort.SliceStable(q.pending[key], func(i, j int) bool { return q.pending[key][i].at.Before(q.pending[key][j].at) })
}

// handingBackLocked reports whether an ambiguous call of this input still needs its terminal answer. q.mu is held.
func (q *questionCalls) handingBackLocked(key string) bool {
	for completed := range q.handedBackIDs {
		if strings.HasPrefix(completed, key+"\x00") {
			return true
		}
	}
	return false
}

func (q *questionCalls) markHandBackLocked(attemptKey string, now time.Time) {
	if attemptKey == "" {
		return
	}
	if q.handedBack == nil {
		q.handedBack = map[string]time.Time{}
	}
	q.handedBack[attemptKey] = now
}

// pair returns this PermissionRequest's id. A resumed attempt reuses its assignment and never consumes the next
// question's pending id. Spool replay calls pair too, rebuilding the assignment without creating a waiter.
func (q *questionCalls) pair(in hookInput, attempt string, resume bool, now time.Time) string {
	key := permissionKey(in)
	attemptKey := questionAttemptKey(key, attempt)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.forget(now)
	if attemptKey != "" {
		if id, ok := q.attempts[attemptKey]; ok {
			return id.id
		}
		if _, ok := q.handedBack[attemptKey]; ok {
			return ""
		}
	}
	if q.handingBackLocked(key) {
		// Every call in an ambiguous generation goes to the terminal. Pairing any of them to a new identical id
		// would leave its eventual PostToolUse unable to release that waiter.
		q.markHandBackLocked(attemptKey, now)
		return ""
	}
	if resume {
		return ""
	}
	var id string
	if ids := q.pending[key]; len(ids) > 0 {
		// A terminal answer may arrive before its own PermissionRequest. Leave that completed id for the late request
		// instead of letting the next identical question consume both the id and its completion.
		which := 0
		for i, candidate := range ids {
			if _, done := q.completed[key+"\x00"+candidate.id]; !done {
				which = i
				break
			}
		}
		id = ids[which].id
		q.pending[key] = append(ids[:which:which], ids[which+1:]...)
		if len(q.pending[key]) == 0 {
			delete(q.pending, key)
		}
	}
	if id != "" && attemptKey != "" {
		q.assignLocked(key, attemptKey, id, now)
	}
	return id
}

// assignLocked publishes an attempt's id and binds a resumed waiter that happened to arrive first. q.mu is held.
func (q *questionCalls) assignLocked(key, attemptKey, id string, now time.Time) {
	if q.attempts == nil {
		q.attempts = map[string]questionID{}
	}
	q.attempts[attemptKey] = questionID{id: id, at: now}
	_, done := q.completed[key+"\x00"+id]
	for _, w := range q.waiting[key] {
		if w.attemptKey == attemptKey && w.id == "" {
			w.id = id
			if done && !w.answered {
				w.answered = true
				w.cancel()
			}
		}
	}
}

// handBack atomically decides an ambiguous attempt. An original pair that won the race keeps its card; otherwise the
// decision is remembered so the original request cannot create a phone wait after the resumed one handed back.
func (q *questionCalls) handBack(in hookInput, attempt string, outstanding []string, now time.Time) bool {
	key := permissionKey(in)
	attemptKey := questionAttemptKey(key, attempt)
	if attemptKey == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.forget(now)
	if _, ok := q.attempts[attemptKey]; ok {
		return false
	}
	mark := func(id string) {
		if id == "" {
			return
		}
		if _, done := q.completed[key+"\x00"+id]; done {
			return // a receipt that already arrived needs no future PostToolUse to release this generation
		}
		if q.handedBackIDs == nil {
			q.handedBackIDs = map[string]time.Time{}
		}
		q.handedBackIDs[key+"\x00"+id] = now
	}
	for _, id := range outstanding {
		mark(id)
	}
	pending := q.pending[key]
	kept := pending[:0]
	for _, id := range pending {
		if _, done := q.completed[key+"\x00"+id.id]; done {
			kept = append(kept, id) // a late PermissionRequest still consumes this exact completion
			continue
		}
		mark(id.id)
	}
	if len(kept) == 0 {
		delete(q.pending, key)
	} else {
		q.pending[key] = kept
	}
	q.markHandBackLocked(attemptKey, now)
	return true
}

// restore installs a pairing recovered from the transcript after the Node that saw PreToolUse was replaced.
func (q *questionCalls) restore(in hookInput, attempt, id string, completed bool, now time.Time) {
	key := permissionKey(in)
	attemptKey := questionAttemptKey(key, attempt)
	q.mu.Lock()
	defer q.mu.Unlock()
	q.forget(now)
	if _, ok := q.handedBack[attemptKey]; ok {
		return
	}
	if q.handingBackLocked(key) {
		q.markHandBackLocked(attemptKey, now)
		return
	}
	if assigned, ok := q.attempts[attemptKey]; ok {
		if completed && assigned.id == id {
			q.completeLocked(key, id, now)
		}
		return
	}
	if completed {
		if q.completed == nil {
			q.completed = map[string]time.Time{}
		}
		q.completed[key+"\x00"+id] = now
	}
	q.assignLocked(key, attemptKey, id, now)
}

// watch waits for a phone answer or PostToolUse. A completion that arrived before PermissionRequest is observed here,
// before any card is created.
func (q *questionCalls) watch(ctx context.Context, in hookInput, attempt string, resume bool) (context.Context, func() bool, func(), string) {
	key := permissionKey(in)
	attemptKey := questionAttemptKey(key, attempt)
	id := q.pair(in, attempt, resume, time.Now())
	ctx, cancel := context.WithCancel(ctx)
	w := &questionWaiter{id: id, attemptKey: attemptKey, cancel: cancel}
	q.mu.Lock()
	q.forget(time.Now())
	if w.id == "" {
		if assigned, ok := q.attempts[attemptKey]; ok { // pair ran before the first request published its assignment
			w.id = assigned.id
		}
	}
	if _, ok := q.handedBack[attemptKey]; ok {
		w.answered = true
	} else if w.id != "" {
		if _, ok := q.completed[key+"\x00"+w.id]; ok {
			w.answered = true
		}
	}
	q.waiting = addHookWaiter(q.waiting, key, w)
	answered := w.answered
	id = w.id
	q.mu.Unlock()
	if answered {
		cancel()
	}
	return ctx, func() bool {
			q.mu.Lock()
			defer q.mu.Unlock()
			return w.answered
		}, func() {
			q.mu.Lock()
			q.waiting = removeHookWaiter(q.waiting, key, w)
			q.mu.Unlock()
			cancel()
		}, id
}

// complete records PostToolUse by exact id, canceling every overlapping waiter of that call. The receipt is also kept for a PermissionRequest
// or replay that has not arrived yet; a different identical question has a different id and is unaffected.
func (q *questionCalls) complete(in hookInput, now time.Time) {
	if in.ToolUseID == "" {
		return // no id is never evidence about an unpaired waiter
	}
	key, id := permissionKey(in), in.ToolUseID
	q.mu.Lock()
	defer q.mu.Unlock()
	q.forget(now)
	q.completeLocked(key, id, now)
}

// completeLocked records one exact completion and releases all of its overlapping waiters. q.mu is held.
func (q *questionCalls) completeLocked(key, id string, now time.Time) {
	for _, w := range q.waiting[key] {
		if !w.answered && w.id == id {
			w.answered = true
			w.cancel()
		}
	}
	if q.completed == nil {
		q.completed = map[string]time.Time{}
	}
	q.completed[key+"\x00"+id] = now
	delete(q.handedBackIDs, key+"\x00"+id)
}
