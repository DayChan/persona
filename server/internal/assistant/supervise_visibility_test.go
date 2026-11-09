package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentSendPreservesSideConversationProvenance(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	assigned, err := s.createTodo(ctx, "Example task", "Fix and verify the example task.", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, assigned.ID, "conv1", []string{"Tests pass"}, 30)
	if err := s.linkTodoAgent(ctx, assigned.ID, "box", "s1abcdef", "Example task", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	f.setSessions("box", proto.Session{SessionID: "s1abcdef", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle})
	args := json.RawMessage(`{"machine":"box","session_id":"s1abcdef","text":"Can you explain a different change?"}`)
	if _, err := s.runTool(ctx, "server", "conv-other", "agent_send", args); err != nil {
		t.Fatal(err)
	}
	messages := sentToAgent(f)
	if len(messages) != 1 || !strings.Contains(messages[0], `<persona-context kind="side-conversation">`) || !strings.Contains(messages[0], "From: conv-other") {
		t.Fatalf("side source missing: %q", messages)
	}
	if _, err := s.runTool(ctx, "server", "conv1", "agent_send", args); err != nil {
		t.Fatal(err)
	}
	messages = sentToAgent(f)
	if len(messages) != 2 || messages[1] != "Can you explain a different change?" {
		t.Fatalf("owner instruction misclassified: %q", messages)
	}
}

func TestSupervisionInfoNeverWakes(t *testing.T) {
	s, f := newQuietService(t)
	s.signal(superviseSignal{Todo: "todo_example", Kind: sigReport, Report: upReportProgress, Key: "step-1"})
	if len(s.supervise.pending) != 0 || s.supervise.timer != nil || len(f.notices()) != 0 {
		t.Fatal("an info update scheduled a main-conversation wake")
	}
	if (superviseSignal{Kind: sigIdle}).level() != supervisionTriage {
		t.Fatal("a stopped task is not triage")
	}
}

func TestDigestDedupDoesNotWakeMainTwice(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	ctx := context.Background()
	assigned, err := s.createTodo(ctx, "Example task", "Collect task evidence.", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, assigned.ID, "conv1", []string{"Evidence collected"}, 30)
	g := superviseSignal{Todo: assigned.ID, Session: "example-agent", Kind: sigIdle, Key: "first-turn", Detail: "No task handoff occurred."}
	s.signal(g)
	waitFor(t, "first synthetic supervision wake", func() bool { return len(f.notices()) == 1 })
	g.Key = "second-turn"
	g.At = time.Now().Add(-time.Minute)
	s.signal(g)
	// Make dispatch due synchronously, without a wall-clock sleep or changing the deduplication clock.
	b := &s.supervise
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer, b.last = nil, time.Time{}
	b.mu.Unlock()
	s.sendDigest(ctx)
	if notices := f.notices(); len(notices) != 1 {
		t.Fatalf("unchanged task/Agent/status/summary woke main again: %q", notices)
	}
}

func TestSupervisionSummaryDedupPersists(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	s, _ := newQuietServiceOn(t, st)
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	entry := prompts.SuperviseEntry{ID: "todo_example", AgentID: "agent-example", Last: "Still waiting for task evidence."}
	g := superviseSignal{Todo: entry.ID, Machine: "example-machine", Session: entry.AgentID, Kind: sigIdle, Key: "turn-one"}
	out, _, _, err := s.reserveSupervision(ctx, []prompts.SuperviseEntry{entry}, []superviseSignal{g}, now)
	if err != nil || len(out) != 1 {
		t.Fatalf("first reservation: %v, %v", out, err)
	}
	// A restarted manager has no in-memory sent keys; its Postgres setting still prevents the duplicate.
	restarted, _ := newQuietServiceOn(t, st)
	g.Key, g.Minutes = "turn-two", 12
	g.Detail = "A differently worded judge reason."
	out, _, _, err = restarted.reserveSupervision(ctx, []prompts.SuperviseEntry{entry}, []superviseSignal{g}, now.Add(12*time.Minute))
	if err != nil || len(out) != 0 {
		t.Fatalf("unchanged summary woke after restart: %v, %v", out, err)
	}
	out, _, _, err = restarted.reserveSupervision(ctx, []prompts.SuperviseEntry{entry}, []superviseSignal{g}, now.Add(30*time.Minute))
	if err != nil || len(out) != 1 {
		t.Fatalf("deduplication did not expire: %v, %v", out, err)
	}
}

func TestSupervisionDedupKeepsNewInformation(t *testing.T) {
	for _, changed := range []string{"task", "agent", "state", "summary", "question"} {
		t.Run(changed, func(t *testing.T) {
			s, _ := newQuietServiceOn(t, &memStore{})
			e := prompts.SuperviseEntry{ID: "todo_example", AgentID: "agent-example", Last: "No task delivery yet."}
			g := superviseSignal{Todo: e.ID, Session: e.AgentID, Kind: sigIdle, Key: "one"}
			now := time.Now()
			if _, _, _, err := s.reserveSupervision(context.Background(), []prompts.SuperviseEntry{e}, []superviseSignal{g}, now); err != nil {
				t.Fatal(err)
			}
			switch changed {
			case "task":
				g.Todo = "another-task"
			case "agent":
				g.Session = "another-agent"
			case "state":
				g.Kind = sigReview
			case "summary":
				e.Last = "Task evidence is available now."
			case "question":
				e.Ask = "Should the change be enabled?"
			}
			out, _, _, err := s.reserveSupervision(context.Background(), []prompts.SuperviseEntry{e}, []superviseSignal{g}, now.Add(time.Minute))
			if err != nil || len(out) != 1 {
				t.Fatalf("new information suppressed: %v, %v", out, err)
			}
		})
	}
}

func TestSupervisionReservationRollbackAndFailure(t *testing.T) {
	st := &memStore{}
	s, _ := newQuietServiceOn(t, st)
	ctx := context.Background()
	e := prompts.SuperviseEntry{ID: "todo_example", Last: "Synthetic summary."}
	g := superviseSignal{Todo: e.ID, Kind: sigIdle}
	now := time.Now()
	_, _, rollback, err := s.reserveSupervision(ctx, []prompts.SuperviseEntry{e}, []superviseSignal{g}, now)
	if err != nil {
		t.Fatal(err)
	}
	rollback()
	out, _, _, err := s.reserveSupervision(ctx, []prompts.SuperviseEntry{e}, []superviseSignal{g}, now.Add(time.Second))
	if err != nil || len(out) != 1 {
		t.Fatalf("failed delivery poisoned retry: %v, %v", out, err)
	}
	st.failKey(superviseDedupKey, errors.New("synthetic read failure"))
	_, kept, _, err := s.reserveSupervision(ctx, []prompts.SuperviseEntry{e}, []superviseSignal{g}, now.Add(time.Minute))
	if err == nil || len(kept) != 1 {
		t.Fatal("read failure was treated as an empty successful history")
	}
}

func TestSupervisionRegisteredBeforeDispatch(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	f.onAssistantSend = func(_ *fakeHub, _ map[string]any) {
		if _, err := s.toolSupervisionFinish(ctx, "conv1", json.RawMessage(`{"visibility":"silent","summary":"Handled during dispatch."}`)); err != nil {
			t.Errorf("fast tool call lost its supervision context: %v", err)
		}
	}
	if err := s.deliverRelay(ctx, "server", "conv1", relayMsg{Tool: superviseTool, Text: "Synthetic supervision."}); err != nil {
		t.Fatal(err)
	}
	if !s.supervising("conv1") {
		t.Fatal("dispatch released its own supervision hold")
	}
	f.onAssistantSend = nil
	f.failAssistant = errors.New("synthetic dispatch failure")
	if err := s.deliverRelay(ctx, "server", "conv1", relayMsg{Tool: superviseTool}); err == nil {
		t.Fatal("dispatch failure ignored")
	}
	if s.supervising("conv1") {
		t.Fatal("failed dispatch left unrelated user input held")
	}
}

func TestSupervisionFinishVisibility(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	args := json.RawMessage(`{"visibility":"silent","summary":"Checked: no task delivery occurred."}`)
	if _, err := s.toolSupervisionFinish(ctx, "normal-user-turn", args); err == nil {
		t.Fatal("normal user input could be hidden")
	}
	s.holdPushes(ctx, "conv1")
	res, err := s.toolSupervisionFinish(ctx, "conv1", args)
	var body map[string]string
	if err != nil || json.Unmarshal([]byte(res), &body) != nil || body["visibility"] != "silent" || body["level"] != supervisionTriage {
		t.Fatalf("silent finish: %q, %v", res, err)
	}
	if !s.supervising("conv1") {
		t.Fatal("silent finish released automatic notifications")
	}
	res, err = s.toolSupervisionFinish(ctx, "conv1", json.RawMessage(`{"visibility":"user","summary":"A decision is needed."}`))
	if err != nil || json.Unmarshal([]byte(res), &body) != nil || body["level"] != supervisionUser {
		t.Fatalf("visible finish: %q, %v", res, err)
	}
	if _, err := s.toolSupervisionFinish(ctx, "conv1", args); err == nil {
		t.Fatal("a promoted turn was hidden again")
	}
	s.holdPushes(ctx, "conv2")
	s.promoteSupervision("conv2")
	if _, err := s.toolSupervisionFinish(ctx, "conv2", args); err == nil {
		t.Fatal("a user notification or question could be hidden")
	}
}
