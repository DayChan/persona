package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Cancellation survives later input and a persisted reload. Sending to the agent
// still works, but only the user's explicit Reopen restores task supervision.
func TestCancelledTaskStaysClosed(t *testing.T) {
	for _, tc := range []struct {
		name, trigger string
		supervised    bool
	}{
		{"user", "user", true},
		{"agent_send", "agent_send", true},
		{"background", "background", true},
		{"unsupervised", "background", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
				ctx := context.Background()
				s, f := e.s, e.f
				todo, err := s.createTodo(ctx, "Update the report", "Update the report and check the results.", "conv1", "")
				if err != nil {
					t.Fatal(err)
				}
				if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1000000", "Update the report", "/code/app", "claude"); err != nil {
					t.Fatal(err)
				}
				wantState := ""
				if tc.supervised {
					s.superviseTodo(ctx, todo.ID, "conv1", []string{"Report checked"}, 10)
					wantState = reviewCancelled
				}
				f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1000000", Status: proto.StatusWorking, State: proto.StateWorking})
				s.TurnStarted(ctx, SessionRef{MachineID: "box", SessionID: "s1000000"})
				if _, err := runJSON(t, s, "conv1", "task_review", map[string]any{
					"todo_id": todo.ID, "verdict": reviewCancelled, "note": "The report is no longer needed.",
				}); err != nil {
					t.Fatal(err)
				}
				if database != nil {
					reloadTodos(t, s)
				}
				closed := todoNow(s, todo.ID)
				if closed.DoneAt == nil || closed.DoneBy != db.DoneByCancelled || (closed.Supervision != nil) != tc.supervised || closed.Supervision.state(closed) != wantState {
					t.Fatalf("cancellation was not retained: %+v", closed)
				}
				view := e.get(todo.ID)
				if view == nil || view.DoneBy != db.DoneByReview || view.DoneReason != closed.DoneReason || view.Supervision.state(*view) != wantState {
					t.Fatalf("cancellation lost its compatible client representation: %+v", view)
				}
				if got := todoNow(s, todo.ID).DoneBy; got != db.DoneByCancelled {
					t.Fatalf("rendering changed the internal completion marker to %q", got)
				}
				beforeFeed, beforeNotices := feedKinds(s, "conv1"), len(f.notices())
				ref := SessionRef{MachineID: "box", SessionID: "s1000000"}
				switch tc.trigger {
				case "user":
					s.UserSpoke(ctx, ref)
					if err := s.sendToSession(ctx, "box", "s1000000", "Revert the changes."); err != nil {
						t.Fatal(err)
					}
				case "agent_send":
					// A message also resumes an agent whose idle pane was closed.
					f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1000000", Status: proto.StatusOffline})
					if _, err := runJSON(t, s, "conv1", "agent_send", map[string]any{
						"machine": "box", "session_id": "s1000000", "text": "Revert the changes.",
					}); err != nil {
						t.Fatal(err)
					}
				}
				if tc.trigger != "background" {
					if sent := f.sentToAgents(); len(sent) != 1 || sent[0] != "s1000000: Revert the changes." {
						t.Fatalf("follow-up input did not reach the agent: %v", sent)
					}
				}
				f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1000000", Status: proto.StatusWorking, State: proto.StateWorking})
				s.TurnStarted(ctx, ref)
				f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1000000", Status: proto.StatusDone, State: proto.StateUnread,
					LastText: "The changes were reverted.", UpdatedAt: time.Now()})
				s.taskTurnEnded(ctx, "box", "s1000000")
				s.superviseWatch(ctx, time.Now().Add(time.Hour))
				after := todoNow(s, todo.ID)
				if after.DoneAt == nil || !after.DoneAt.Equal(*closed.DoneAt) || after.ReopenedAt != nil ||
					after.DoneBy != closed.DoneBy || after.DoneReason != closed.DoneReason || after.Supervision.state(after) != wantState {
					t.Fatalf("later work changed the cancellation: %+v", after)
				}
				if done, err := s.completedTodo(ctx, "box", "s1000000"); err != nil || !done {
					t.Fatalf("automatic follow-ups are not suppressed: done %v, error %v", done, err)
				}
				if got := feedKinds(s, "conv1"); got != beforeFeed || len(f.notices()) != beforeNotices {
					t.Fatalf("cancelled task produced a receipt or notice: feed %q, notices %v", got, f.notices())
				}
				f.mu.Lock()
				judged := f.judged
				f.mu.Unlock()
				s.supervise.mu.Lock()
				pending := len(s.supervise.pending)
				s.supervise.mu.Unlock()
				if judged != 0 || pending != 0 {
					t.Fatalf("cancelled task was judged or supervised again: judgments %d, pending signals %d", judged, pending)
				}

				e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":false}`)
				if database != nil {
					reloadTodos(t, s)
				}
				s.TurnStarted(ctx, ref)
				reopened := todoNow(s, todo.ID)
				if reopened.DoneAt != nil || reopened.ReopenedAt == nil || reopened.DoneBy != "" || reopened.Supervision.state(reopened) != "" {
					t.Fatalf("explicit reopen did not restore the task: %+v", reopened)
				}
				if active, ok := s.taskOf(ctx, "box", "s1000000"); !ok || active.ID != todo.ID {
					t.Fatalf("explicit reopen did not restore the agent link: %+v", active)
				}
			})
		})
	}
}
