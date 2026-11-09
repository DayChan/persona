package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskCompletionRequiresEveryAgentToSettle(t *testing.T) {
	todo := Todo{Agents: []TodoAgent{{MachineID: "computer", SessionID: "agent"}}}
	for _, tc := range []struct {
		name   string
		agents []proto.Session
		want   bool
	}{
		{"missing", nil, false},
		{"tool running with stale UI state", []proto.Session{{Status: proto.StatusWorking, State: proto.StateIdle}}, false},
		{"waiting", []proto.Session{{Status: proto.StatusBlocked}}, false},
		{"unknown", []proto.Session{{Status: proto.StatusUnknown}}, false},
		{"background", []proto.Session{{Status: proto.StatusDone, Background: []proto.BackgroundTask{{ID: "tool"}}}}, false},
		{"done", []proto.Session{{Status: proto.StatusDone}}, true},
		{"idle", []proto.Session{{Status: proto.StatusIdle}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := taskAgentsSettled(todo, tc.agents); got != tc.want {
				t.Fatalf("settled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTaskCompletionRejectsAResumedTurnDuringJudgment(t *testing.T) {
	for _, supervised := range []bool{false, true} {
		t.Run(map[bool]string{false: "automatic close", true: "pending review"}[supervised], func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			if supervised {
				s.superviseTodo(ctx, todo.ID, "conv1", []string{"Verify delivery"}, 0)
				todo = todoNow(s, todo.ID)
			}
			f.judge = `{"verdict":"done","reason":"The previous answer claims success"}`
			s.taskWorking(ctx, "box", "s1")
			s.tasks.mu.Lock()
			work := s.tasks.working[todo.ID]
			s.tasks.mu.Unlock()
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, LastText: "Earlier work finished"})
			changed := false
			f.hookReads(receiptHooks{before: func(int) {
				if changed {
					return
				}
				changed = true
				s.taskWorking(ctx, "box", "s1")
				// Even a short resumed turn that already stopped invalidates the older judgment.
				f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone, LastText: "More work remains"})
			}})
			s.autoFinishTask(todo, work, true)
			current := todoNow(s, todo.ID)
			if !changed || current.DoneAt != nil || current.Supervision != nil && current.Supervision.State == reviewPending {
				t.Fatalf("stale judgment applied: changed=%v todo=%+v", changed, current)
			}
			if ok, err := s.finishCurrentTask(ctx, todo.ID, "stale write", work); err != nil || ok {
				t.Fatalf("stale completion committed: %v %v", ok, err)
			}
		})
	}
}

func TestTaskReceiptDoesNotCompleteARunningToolOrMissingAgent(t *testing.T) {
	for _, missing := range []bool{false, true} {
		s, f := newTaskService(t)
		startedTask(t, s, f)
		f.judge = `{"verdict":"done","reason":"Progress looks finished"}`
		s.taskWorking(context.Background(), "box", "s1")
		if missing {
			f.setSessions("box")
		} else {
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateIdle,
				Activity: "python3", LastText: "The initial check passed", UpdatedAt: time.Now()})
		}
		s.taskTurnEnded(context.Background(), "box", "s1")
		if got := feedKinds(s, "conv1"); strings.Contains(got, "receipt") || onlyTodo(s).DoneAt != nil {
			t.Fatalf("receipt/completion while unresolved: missing=%v feed=%s", missing, got)
		}
	}
}

func TestTaskCompletionWaitsForTheAnswerAfterATool(t *testing.T) {
	for _, finalStatus := range []string{proto.StatusDone, proto.StatusIdle} {
		t.Run(finalStatus, func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			f.judge = `{"verdict":"done","reason":"Delivery verified"}`
			ctx := context.Background()
			s.taskWorking(ctx, "box", "s1")
			idle := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusIdle,
				LastText: "The first check passed", UpdatedAt: time.Now()}
			f.setSessions("box", idle)
			s.fleetKeepPrevious("box", []proto.Session{idle})
			f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "The first check passed"},
				proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "Bash", ToolUseID: "command"},
				proto.Item{Kind: proto.KindToolResult, Role: "user", ToolUseID: "command", Text: "still verifying"})
			s.taskTurnEnded(ctx, "box", "s1")
			if strings.Contains(feedKinds(s, "conv1"), "receipt") || onlyTodo(s).DoneAt != nil || f.judged != 0 {
				t.Fatal("a temporary idle/tool result was treated as the completed task")
			}
			// A later session update must finish the held receipt without another working tick.
			f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Delivery verified; all acceptance checks passed"},
				proto.Item{Kind: "turn_duration", Role: "system"})
			final := idle
			final.Status, final.LastText, final.UpdatedAt = finalStatus, "Delivery verified", time.Now()
			f.setSessions("box", final)
			s.setSessions("box", []proto.Session{final})
			waitFor(t, "completion after the final session update", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
			f.mu.Lock()
			judged := f.judged
			f.mu.Unlock()
			if judged != 1 {
				t.Fatalf("completion judgments = %d, want 1", judged)
			}
		})
	}
}

func TestTaskCompletionReconcilesAnAnswerArrivingDuringTheRead(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	f.judge = `{"verdict":"done","reason":"Delivery verified"}`
	ctx := context.Background()
	s.taskWorking(ctx, "box", "s1")
	idle := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusIdle, Rev: 1}
	f.setSessions("box", idle)
	s.fleetKeepPrevious("box", []proto.Session{idle})
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Checking"},
		proto.Item{Kind: proto.KindToolResult, Role: "user", ToolUseID: "command", Text: "Tool output"})
	f.hookReads(receiptHooks{after: func(read int) {
		if read != 1 {
			return
		}
		// The final event overtakes the old tool transcript response.
		f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Delivery verified"},
			proto.Item{Kind: "turn_duration", Role: "system"})
		final := idle
		final.Status, final.Rev = proto.StatusDone, 2
		f.setSessions("box", final)
		s.setSessions("box", []proto.Session{final})
	}})
	s.taskTurnEnded(ctx, "box", "s1")
	waitFor(t, "completion without another session update", func() bool { return todoNow(s, todo.ID).DoneAt != nil })
}
