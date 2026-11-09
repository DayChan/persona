package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ---- the interaction queue as the assistant's own (its interaction_answer tool, the channels) ----

// answerQuestion resolves the pending question id of machine's session with answers, as the assistant answered it
// (and why, "" when it did not say); false when it is no longer pending.
func (s *Service) answerQuestion(machine, session, id string, answers map[string]string, reason string) (bool, error) {
	ix, ok := s.pendingIx(id)
	if !ok {
		return false, nil
	}
	if ix.Kind != proto.IxQuestion {
		return false, fmt.Errorf("interaction %s is a %s, not a question", id, ix.Kind)
	}
	if ix.MachineID != machine || ix.SessionID != session {
		return false, fmt.Errorf("interaction %s is not machine=%s session_id=%s's", id, machine, session)
	}
	if err := s.resolve(id, proto.Resolution{Answers: answers, AnsweredBy: proto.AnsweredByAssistant, Reason: reason}); err != nil {
		if errors.Is(err, errNotPending) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// endedStatus is how a card that resolve found no longer pending (err) ended: the claim's when another answer took
// it a moment ago (#1230), else the history's.
func (s *Service) endedStatus(ctx context.Context, id string, err error) string {
	if just := (*answeredJustNow)(nil); errors.As(err, &just) {
		return just.status
	}
	return s.interactionOutcome(ctx, id)
}

// interactionOutcome is how an interaction that is no longer pending ended, from the history: resolved, dismissed,
// expired, cancelled; "" when the history has none.
func (s *Service) interactionOutcome(ctx context.Context, id string) string {
	st := s.fleetStore()
	if st == nil {
		return ""
	}
	status, _, err := st.InteractionOutcome(ctx, id)
	if err != nil {
		return ""
	}
	return status
}

// ---- the assistant's own questions and cards ----

// askOnPhone shows the questions the assistant asks and its cards about agents' turns (its snapshot's interactions)
// like any agent's card: recorded, announced, pushed, expired (waitInteraction); the answer goes to its Action, or
// waits when it cannot take it (keepAnswer). A question that left the snapshot is withdrawn, as a hook's that went
// away, and nothing is said about it. One it still reports a while after its answer went, with no answer waiting for
// it, retries delivery from its durable outcome rather than asking the user again.
func (s *Service) askOnPhone(list []proto.Interaction) {
	f := &s.fleet
	f.askMu.Lock()
	defer f.askMu.Unlock()
	if f.asked == nil {
		f.asked = map[string]*askedOne{}
	}
	now := map[string]bool{}
	for _, ix := range list {
		now[ix.ID] = true
		if q := f.asked[ix.ID]; q != nil {
			if q.ended.IsZero() || time.Since(q.ended) < askAgainAfter || s.answerKept(ix.ID) {
				continue
			}
			q.cancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		q := &askedOne{cancel: cancel}
		f.asked[ix.ID] = q
		go func(ix proto.Interaction) {
			defer func() {
				f.askMu.Lock()
				q.ended = time.Now()
				f.askMu.Unlock()
			}()
			r, err := s.waitOwnedQuestion(ctx, ix)
			if err != nil {
				if ctx.Err() == nil {
					s.logf("assistant: question %s: %v", ix.ID, err)
				}
				return
			}
			if r.Dismiss && ctx.Err() != nil {
				return // withdrawn: it knows
			}
			if r.Dismiss && r.Reason == "" && r.AnsweredBy == "" && len(r.Answers) == 0 && s.ownQuestionExpired(ix) {
				r.Reason = proto.DismissedExpired // its card expired unanswered: the question becomes a to-do (ask_todo.go)
			}
			answer, _ := json.Marshal(r)
			act := Action{ActionID: "act_" + randomHex(6), SessionID: ix.SessionID, Op: "answer", InteractionID: ix.ID,
				InteractionVersion: 1, Answer: answer}
			_, err = s.Action(context.Background(), act)
			s.settleAnswer(ix, act, err)
		}(ix)
	}
	for id, q := range f.asked {
		if !now[id] {
			q.cancel()
			delete(f.asked, id)
		}
	}
}

// askedOne is a question or card of the assistant's on the phone: cancel withdraws it; ended is when its answer (or
// its expiry) went to its Action.
type askedOne struct {
	cancel context.CancelFunc
	ended  time.Time
}

// askAgainAfter is how long a question the assistant still reports after its answer went may stay off the phone.
var askAgainAfter = 30 * time.Second

// heldActs are the answers to the assistant's questions and cards its Action did not take yet, by interaction id:
// they go to it again after each snapshot (deliverAnswers) until it takes them, says they are for nothing it has
// any more (CodeGone), or their card would have expired.
type heldActs struct {
	mu   sync.Mutex
	acts map[string]heldAct
	busy bool
}

type heldAct struct {
	act         Action
	until, next time.Time
}

// answerRetryAfter is how long an answer the Action could not take waits before it is tried again.
var answerRetryAfter = 10 * time.Second

// keepAnswer holds act, which the Action did not take (err), until until; false when err is final (its question or
// card is gone): nothing is kept.
func (s *Service) keepAnswer(act Action, err error, until time.Time) bool {
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == assistantwire.CodeGone {
		return false
	}
	now := time.Now()
	if until.Before(now) {
		until = now.Add(24 * time.Hour) // as long as a card of the manager's lasts
	}
	u := &s.fleet.undelivered
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.acts == nil {
		u.acts = map[string]heldAct{}
	}
	u.acts[act.InteractionID] = heldAct{act: act, until: until, next: now.Add(answerRetryAfter)}
	return true
}

// settleAnswer ends act, the answer to the assistant's card ix, once its Action had it (err: it did not take it): one it
// did not take is kept for another try (keepAnswer). An answer to a card with a secret field goes once: it is never
// kept (what was typed into it would wait in memory for up to a day, for a call that is gone, a restart or a cancel,
// and that asks with a new card), and its bytes are cleared now; the Action decoded its own copy.
func (s *Service) settleAnswer(ix proto.Interaction, act Action, err error) {
	if hasSecretField(ix) {
		clear(act.Answer)
		if err != nil {
			s.logf("assistant: answer %s: %v (not kept: the card has a secret field)", ix.ID, err)
		}
		return
	}
	if err != nil && !s.keepAnswer(act, err, ix.ExpiresAt) {
		s.logf("assistant: answer %s: %v", ix.ID, err)
	}
}

// answerKept: an answer to interaction id is waiting for the Action.
func (s *Service) answerKept(id string) bool {
	u := &s.fleet.undelivered
	u.mu.Lock()
	defer u.mu.Unlock()
	_, ok := u.acts[id]
	return ok
}

// deliverAnswers gives the Action the answers kept that are due (after a snapshot). One delivery at a time.
func (s *Service) deliverAnswers() {
	u := &s.fleet.undelivered
	u.mu.Lock()
	if u.busy || len(u.acts) == 0 {
		u.mu.Unlock()
		return
	}
	u.busy = true
	now := time.Now()
	var due []heldAct
	for id, a := range u.acts {
		switch {
		case now.After(a.until):
			delete(u.acts, id)
			s.logf("assistant: answer %s: dropped, its card expired before it was taken", id)
		case !a.next.After(now):
			delete(u.acts, id)
			due = append(due, a)
		}
	}
	u.mu.Unlock()
	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()
	for _, a := range due {
		if _, err := s.Action(context.Background(), a.act); err != nil && !s.keepAnswer(a.act, err, a.until) {
			s.logf("assistant: answer %s: %v", a.act.InteractionID, err)
		}
	}
}
