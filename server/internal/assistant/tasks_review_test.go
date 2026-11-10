package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A supervised task's stopped turn needs Persona's attention before the judge runs, unless it waits on the user.
func TestTaskReceiptReview(t *testing.T) {
	for _, c := range []struct {
		name        string
		state       string
		criteria    []string
		verdict     string
		ordinary    bool
		interrupted bool
		silent      bool
		wantReview  bool
	}{
		{name: "awaiting with criteria", criteria: []string{"Tests pass", "Changes reviewed", "Documentation updated"}, verdict: verdictDone, wantReview: true},
		{name: "awaiting without criteria", verdict: verdictDone, wantReview: true},
		{name: "ordinary task", ordinary: true, verdict: verdictDone},
		{name: "unfinished", verdict: verdictNotDone, wantReview: true},
		{name: "rework", state: reviewRework, verdict: verdictNotDone, wantReview: true},
		{name: "needs user", state: reviewNeedsUser, verdict: verdictNeedsUser},
		{name: "reopened accepted", state: reviewAccepted, verdict: verdictNotDone, wantReview: true},
		{name: "reopened cancelled", state: reviewCancelled, verdict: verdictNotDone, wantReview: true},
		{name: "judge unavailable", wantReview: true},
		{name: "interrupted", state: reviewPending, interrupted: true},
		{name: "silent", state: reviewPending, silent: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := newTaskService(t)
			shortSupervise(t, s)
			todo := startedTask(t, s, f)
			if c.verdict != "" {
				f.judge = `{"verdict":"` + c.verdict + `","reason":"Checked the work"}`
			}
			ctx := context.Background()
			if !c.ordinary {
				s.superviseTodo(ctx, todo.ID, "conv1", c.criteria, 0)
			}
			agentTurn(s, f, proto.StatusWorking, "", 0)
			if !c.ordinary {
				if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
					v.State = c.state
					return true
				}); err != nil {
					t.Fatal(err)
				}
			}
			session := proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
				Status: proto.StatusDone, State: proto.StateUnread, LastText: "Work checked.", UpdatedAt: time.Now()}
			wantState, wantJudgments := "done", 1
			if c.verdict == "" {
				fastJudgeRetry(t)
				wantJudgments = 2 // a failed judgment is asked once more (#3111)
			}
			if c.interrupted || c.silent {
				wantState, wantJudgments = "interrupted", 0
				if c.interrupted {
					session.TurnError = "API Error: 529 overloaded"
				} else {
					f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: "Check the work"})
				}
			}
			f.setSessions("box", session)
			receipted := false
			f.hookReads(receiptHooks{before: func(reads int) {
				if reads == 2 && !receipted {
					t.Fatal("the judge started reading the turn before the receipt was written")
				}
			}, receipt: func(FeedItem) {
				receipted = true
				f.mu.Lock()
				judged := f.judged
				f.mu.Unlock()
				if judged != 0 {
					t.Fatalf("the receipt waited for %d judgments", judged)
				}
				s.supervise.mu.Lock()
				defer s.supervise.mu.Unlock()
				if len(s.supervise.pending) != 0 || len(s.supervise.sent) != 0 {
					t.Error("the supervisor was signaled before the receipt was written")
				}
			}})
			s.taskTurnEnded(ctx, "box", "s1")
			if !receipted {
				t.Fatal("the receipt was not broadcast")
			}
			items, _, err := s.feed(ctx, "conv1", time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			last := items[len(items)-1]
			var body receiptBody
			if err := json.Unmarshal(last.Body, &body); err != nil {
				t.Fatal(err)
			}
			if last.Kind != feedReceipt || body.State != wantState {
				t.Fatalf("receipt = %s %s", last.Kind, last.Body)
			}
			var wire map[string]json.RawMessage
			if err := json.Unmarshal(last.Body, &wire); err != nil {
				t.Fatal(err)
			}
			if c.wantReview {
				wantReviewState := "checking"
				if c.state == reviewPending {
					wantReviewState = "awaiting"
				}
				if body.Review == nil || body.Review.State != wantReviewState || body.Review.Criteria != len(c.criteria) {
					t.Fatalf("review = %+v, body = %s", body.Review, last.Body)
				}
				var review map[string]json.RawMessage
				if err := json.Unmarshal(wire["review"], &review); err != nil || review["criteria"] == nil {
					t.Fatalf("criteria must be present even at zero: %s (%v)", wire["review"], err)
				}
			} else if _, present := wire["review"]; present {
				t.Fatalf("review must be omitted: %s", last.Body)
			}
			f.mu.Lock()
			judgments := f.judged
			f.mu.Unlock()
			if judgments != wantJudgments {
				t.Fatalf("judged %d times, want %d", judgments, wantJudgments)
			}
		})
	}
}

// receiptHooks run around the fake's session.items reads (counted from when they are set) and on receipt broadcasts.
// They live on the fake, not on a hub swapped in, because the service's goroutines read s.hub at any time.
type receiptHooks struct {
	before  func(read int)
	after   func(read int)
	receipt func(FeedItem)
}

func (f *fakeHub) hookReads(h receiptHooks) {
	f.mu.Lock()
	f.readHooks, f.itemReads = h, 0
	f.mu.Unlock()
}

func TestTaskReceiptReviewUsesLatestTodo(t *testing.T) {
	for _, state := range []string{"completed", reviewNeedsUser} {
		t.Run(state, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			agentTurn(s, f, proto.StatusWorking, "", 0)
			changed := false
			f.hookReads(receiptHooks{before: func(reads int) {
				if reads != 1 {
					return
				}
				// The task changes while its receipt summary is being read, before any judgment.
				s.todos.mu.Lock()
				if state == "completed" {
					now := time.Now()
					s.todos.todos[todo.ID].DoneAt = &now
				} else {
					s.todos.todos[todo.ID].Supervision.State = state
				}
				s.todos.mu.Unlock()
				changed = true
			}})
			agentTurn(s, f, proto.StatusDone, "Work checked.", 0)
			if !changed {
				t.Fatal("the task was not changed while reading its receipt summary")
			}
			items, _, err := s.feed(context.Background(), "conv1", time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			last := items[len(items)-1]
			var body map[string]json.RawMessage
			if err := json.Unmarshal(last.Body, &body); err != nil {
				t.Fatal(err)
			}
			if last.Kind != feedReceipt || string(body["state"]) != `"done"` {
				t.Fatalf("receipt = %s %s", last.Kind, last.Body)
			}
			if _, present := body["review"]; present {
				t.Fatalf("a task now %s must not await review: %s", state, last.Body)
			}
		})
	}
}

func TestTaskReceiptReviewOmittedForFailedStart(t *testing.T) {
	s, f := newTaskService(t)
	shortSupervise(t, s)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Check the work", "Check the work", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"Tests pass"}, 0)
	f.failStart = errors.New("launcher unavailable")
	s.startPlanTask("", "conv1", 0, PlanTask{TodoID: todo.ID, Title: todo.Title, Prompt: todo.Notes, Machine: "box", Cwd: "/code/app"}, false)
	items, _, err := s.feed(ctx, "conv1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("no failure receipt")
	}
	last := items[len(items)-1]
	var body map[string]json.RawMessage
	if err := json.Unmarshal(last.Body, &body); err != nil {
		t.Fatal(err)
	}
	if last.Kind != feedReceipt || string(body["state"]) != `"failed"` {
		t.Fatalf("receipt = %s %s", last.Kind, last.Body)
	}
	if _, present := body["review"]; present {
		t.Fatalf("a failed start must not await review: %s", last.Body)
	}
}
