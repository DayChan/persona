package serve

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/rpc"
)

// sentRecallMessage is the core message of either the legacy or the current recall call. A missing target is an empty
// object, so the connector always receives valid JSON.
func sentRecallMessage(action, ref string, target map[string]any, at time.Time) connector.SentMessage {
	raw := json.RawMessage(`{}`)
	if len(target) > 0 {
		raw, _ = json.Marshal(target)
	}
	return connector.SentMessage{Action: action, Ref: ref, Target: raw, At: at}
}

// recallDoResult is the current protocol's honest outcome. The core interface treats an already-gone message as
// success, so a Go connector reports done; a program written in another language may report already_recalled itself.
func recallDoResult(err error) RecallDoResult {
	if err == nil {
		return RecallDoResult{Status: RecallDone}
	}
	res := RecallDoResult{Status: RecallRefused, Error: err.Error()}
	switch {
	case errors.Is(err, connector.ErrRecallUnknown):
		res.Status = RecallUnknown
	case errors.Is(err, connector.ErrCannotRecall):
		res.Status = RecallNotRecallable
	case errors.Is(err, connector.ErrRecallUnavailable):
		res.Status = RecallUnavailable
	}
	return res
}

// recallError is a connector's failed Recall as the wire says it: the code of the connector.Err* it wraps (one it
// could not act now is not_signed_in: the hub reads that like offline), none for the other side's refusal.
func recallError(err error) error {
	var re *rpc.Error
	switch {
	case errors.As(err, &re):
		return re
	case errors.Is(err, connector.ErrCannotRecall):
		return &rpc.Error{Code: CodeNotRecallable, Message: err.Error()}
	case errors.Is(err, connector.ErrRecallUnavailable):
		return &rpc.Error{Code: CodeNotSignedIn, Message: err.Error()}
	case errors.Is(err, connector.ErrRecallUnknown):
		return &rpc.Error{Code: CodeRecallUnknown, Message: err.Error()}
	}
	return &rpc.Error{Message: err.Error()}
}

// recallRuns shares one recall.do by request id. Successes are remembered (bounded by doneMost), so a repeat after a
// lost answer or a failed ledger mark confirms the same deletion. A failure is shared only with callers already waiting:
// after it ends the id may be used for the user's explicit retry, as connector.Recaller permits.
type recallRuns struct {
	mu    sync.Mutex
	runs  map[string]*recallRun
	order []string // successful request ids, oldest first
}

type recallRun struct {
	done    chan struct{}
	waiters int // callers that joined and have not left yet
	res     RecallDoResult
}

func (w *recallRuns) do(id string, run func() RecallDoResult) RecallDoResult {
	w.mu.Lock()
	if prev, ok := w.runs[id]; ok {
		prev.waiters++
		w.mu.Unlock()
		defer func() {
			w.mu.Lock()
			prev.waiters--
			w.mu.Unlock()
		}()
		<-prev.done
		return prev.res
	}
	if w.runs == nil {
		w.runs = map[string]*recallRun{}
	}
	cur := &recallRun{done: make(chan struct{})}
	w.runs[id] = cur
	w.mu.Unlock()
	cur.res = run()
	w.mu.Lock()
	if cur.res.Status == RecallDone || cur.res.Status == RecallAlreadyRecalled {
		w.order = append(w.order, id)
		if len(w.order) > doneMost {
			delete(w.runs, w.order[0])
			w.order = w.order[1:]
		}
	} else {
		delete(w.runs, id)
	}
	w.mu.Unlock()
	close(cur.done)
	return cur.res
}
