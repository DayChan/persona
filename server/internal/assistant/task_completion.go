package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

var (
	sideConversationContext = lazyre.New(`<persona-context\b[^>]*\bkind\s*=\s*["']side-conversation["'][^>]*>`)
	sideConversationMarker  = lazyre.New(`<persona-side-conversation\s*/>`)
	loopInspectionRequest   = lazyre.New(`^/loop(?:\s|$)`)
)

// A runtime's user role also carries peer messages and automated injections. Those
// exchanges cannot hand over this task merely because their answer sounds final.
// Persona and the recorded task supervisor may legitimately advance the task.
// Native background completion and own-task continuation are not side conversations.
func taskTurnSource(t Todo, items []proto.Item) (from, source string) {
	for _, it := range items {
		if it.Kind != proto.KindText && it.Kind != proto.KindNotice {
			continue
		}
		if it.Role != "user" && !(it.Role == "system" && it.Kind == proto.KindNotice) {
			continue
		}
		if f := strings.TrimSpace(it.From); f != "" {
			from = f
			if f != "Persona" && (t.Supervision == nil || f != t.Supervision.Supervisor) {
				return f, "peer"
			}
		}
		if source := prompts.ItemSource(it); source != nil && source.Kind == proto.SourceConversation {
			return source.ConversationID, "side-conversation"
		}
		text := strings.TrimSpace(it.Text)
		if it.Role == "user" && it.Kind == proto.KindText && loopInspectionRequest.MatchString(text) {
			return from, "inspection"
		}
		if sideConversationContext.MatchString(text) || sideConversationMarker.MatchString(text) {
			return from, "side-conversation"
		}
		if strings.Contains(text, "<cross-session-message") || strings.Contains(text, "<teammate-message") {
			return from, "peer"
		}
		injected := it.Injected
		if injected == nil {
			injected = prompts.ParseInjected(text)
		}
		if injected != nil {
			// These resume this session's work, rather than introduce another task.
			// Continue scanning: a queued peer message still makes the turn a side exchange.
			if injected.Kind == proto.InjectedFollowup && (injected.State == "limit_continue" || injected.State == "wake") {
				continue
			}
			return from, "injected"
		}
	}
	return from, ""
}

// A side turn can still submit the task's actual completion report. Require a
// successful task-scoped tool call in this turn, the corresponding live progress
// record, and evidence naming this task or one of its current acceptance criteria.
// Missing timestamps or evidence fail closed; an older done report is not a handoff.
func freshTaskDoneReport(t Todo, items []proto.Item, since time.Time) (text string, evidence []string, ok bool) {
	if t.Supervision == nil || t.Supervision.Progress == nil {
		return "", nil, false
	}
	v, p := t.Supervision, t.Supervision.Progress
	turnKnown := !since.IsZero()
	if t.CreatedAt.After(since) {
		since = t.CreatedAt
	}
	if t.ReopenedAt != nil && t.ReopenedAt.After(since) {
		since = *t.ReopenedAt
	}
	if v.UpdatedAt.After(since) {
		since = v.UpdatedAt
	}
	for _, it := range items {
		if (it.Role == "user" || it.Wake || it.Task != nil) && (it.Kind == proto.KindText || it.Kind == proto.KindNotice) && !it.TS.IsZero() {
			turnKnown = true
			if it.TS.After(since) {
				since = it.TS
			}
		}
	}
	if !turnKnown || p.Kind != upReportDone || p.At.IsZero() || p.At.Before(since) {
		return "", nil, false
	}
	for i, it := range items {
		if it.Kind != proto.KindToolUse || it.Role != "assistant" || it.ToolUseID == "" || it.TS.IsZero() || it.TS.Before(since) {
			continue
		}
		if it.ToolName != "report_to_supervisor" && !slices.Contains(compat.MCPToolNames([]string{"report_to_supervisor"}), it.ToolName) {
			continue
		}
		var report struct {
			Kind     string   `json:"kind"`
			Text     string   `json:"text"`
			Evidence []string `json:"evidence"`
		}
		if json.Unmarshal(it.ToolInput, &report) != nil || report.Kind != upReportDone || strings.TrimSpace(report.Text) == "" ||
			p.Text != clip(oneLine(clip(strings.TrimSpace(report.Text), 2000)), 200) {
			continue
		}
		related := false
		for _, e := range report.Evidence {
			for _, criterion := range append([]string{t.Title, t.Notes}, v.DoneWhen...) {
				if c := strings.TrimSpace(criterion); c != "" && strings.Contains(e, c) {
					related = true
				}
			}
		}
		if !related {
			continue
		}
		for _, result := range items[i+1:] {
			if result.Kind == proto.KindToolResult && result.ToolUseID == it.ToolUseID {
				if !result.IsError {
					return report.Text, report.Evidence, true
				}
				break
			}
		}
	}
	return "", nil, false
}

// Every linked agent must be observed. A missing session or disconnected node is
// not evidence that its work ended; raw runtime status also outranks a stale UI state.
func taskAgentsSettled(t Todo, agents []proto.Session) bool {
	expected := 0
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil {
			expected++
		}
	}
	if expected == 0 || len(agents) != expected {
		return false
	}
	for _, c := range agents {
		if c.Status != proto.StatusDone && c.Status != proto.StatusIdle ||
			c.State == proto.StateWorking || c.State == proto.StateNeedsYou ||
			c.PendingInteractions > 0 || backgroundRunning(c) {
			return false
		}
		if w := c.Work; w != nil && w.Phase == proto.WorkBlocked && w.On == proto.WaitTransient && w.WakeAt != nil {
			return false
		}
	}
	return true
}

// A status tick can briefly say idle between tool output and the next model
// response. Earlier assistant words cannot finish that still-open exchange.
func taskTurnAwaitingAnswer(items []proto.Item) bool {
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		switch it.Kind {
		case "turn_duration":
			return false
		case proto.KindText:
			return it.Role != "assistant"
		case proto.KindToolUse, proto.KindToolResult, proto.KindThinking:
			return true
		}
	}
	return false
}

// A final answer can arrive after an idle tick without another working tick.
// Retry only a receipt held for that answer, rather than notifying the turn twice.
func (s *Service) takeTaskAnswerRetry(machine, sid string) bool {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	ref := SessionRef{MachineID: machine, SessionID: sid}
	if !s.tasks.awaitingAnswer[ref] {
		return false
	}
	delete(s.tasks.awaitingAnswer, ref)
	return true
}

func (s *Service) taskWorkCurrent(id string, work time.Time) bool {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	return !s.tasks.working[id].After(work)
}

// Model calls and transcript reads can outlive a turn. Check both the observed
// turn and fresh runtime state before applying their result, including review state.
func (s *Service) taskCompletionCurrent(ctx context.Context, t Todo, work time.Time) bool {
	current, ok := s.todoNow(t.ID)
	if !ok || current.DoneAt != nil || current.Title != t.Title || current.Notes != t.Notes || autoDoneSkip(current, work, true) != "" || !s.taskWorkCurrent(t.ID, work) {
		return false
	}
	if (current.Supervision == nil) != (t.Supervision == nil) {
		return false
	}
	if current.Supervision != nil && (!slices.Equal(current.Supervision.DoneWhen, t.Supervision.DoneWhen) || current.Supervision.Round != t.Supervision.Round) {
		return false
	}
	return s.taskRuntimeSettled(ctx, current) && s.taskWorkCurrent(t.ID, work)
}

func (s *Service) taskRuntimeSettled(ctx context.Context, t Todo) bool {
	agents, reached := s.taskAgentsReached(ctx, t)
	return reached && taskAgentsSettled(t, agents)
}

// A turn that starts before this lock invalidates the judgment. One that starts
// afterwards reopens the automatically closed task through taskWorking.
func (s *Service) finishCurrentTask(ctx context.Context, id, reason string, work time.Time) (bool, error) {
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	if !s.taskWorkCurrent(id, work) {
		return false, nil
	}
	return s.autoDoneTodo(ctx, id, reason, work)
}
