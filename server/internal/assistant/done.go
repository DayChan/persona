package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Closing a finished task (docs/assistant/todos.md "Auto-complete", #1092). When a task's agents have all stopped and its receipt
// went out, the judge model reads the task's title, the prompt it was handed out with and what its agents said last,
// and only "done" closes the todo (done_by auto, with why). Anything else — not finished, waiting on someone,
// needing the user, unsure, no model, a model error, an agent whose turn the model service cut off (#1516) — leaves
// it open. A todo the user reopened is not closed again by the same stretch of work. A supervised task (supervise.go)
// is not closed at all: finished, it waits for its supervisor's review. While assistant.done_needs_delivery is on (off
// by default), done also needs the work live and checked (or nothing to ship): one done but held at a PR, merged or off
// by default stays open as undelivered, with where it is held (delivery.go).

const (
	verdictDone      = "done"
	verdictNotDone   = "not_done"
	verdictWaiting   = "waiting"
	verdictNeedsUser = "needs_user"
	verdictUnsure    = "unsure"
	// verdictUndelivered: done as asked, but its work is not live yet (delivery.go); the hub's own, not the model's.
	verdictUndelivered = "undelivered"
)

// taskDoneTimeout bounds one judgment (reading the replies and the model).
var taskDoneTimeout = 90 * time.Second

// taskDoneSignals are cheap hints found in the replies, handed to the model (never enough on their own).
type taskDoneSignals struct {
	MergedPR bool `json:"merged_pr"`
	Build    bool `json:"build"`
}

var (
	mergedWords  = lazyre.New(`(?i)\bmerged\b|\bsquash(ed)?\b|已合并|合并了|已合入|合入了|自行合并`)
	buildNumbers = lazyre.New(`(?i)testflight[^\n]{0,40}?\d{3,}|构建号?\s*[:：#]?\s*\d{3,}|\bbuild\s*#?\s*\d{3,}`)
)

func doneSignals(replies []prompts.TaskDoneReply) taskDoneSignals {
	var s taskDoneSignals
	for _, r := range replies {
		if prLink.MatchString(r.Text) && mergedWords.MatchString(r.Text) {
			s.MergedPR = true
		}
		if buildNumbers.MatchString(r.Text) {
			s.Build = true
		}
	}
	return s
}

// taskDoneCheck is one judgment, as POST /api/v1/todos/{id}/done-check answers it.
type taskDoneCheck struct {
	Verdict    string          `json:"verdict"`
	Reason     string          `json:"reason"`
	WouldClose bool            `json:"would_close"`
	Skip       string          `json:"skip"` // why the automatic pass would not judge it now; "" when it would
	Replies    int             `json:"replies"`
	Signals    taskDoneSignals `json:"signals"`
	// Delivery: how far the work went out, when the delivery is judged (done_needs_delivery).
	Delivery *TodoDelivery `json:"delivery,omitempty"`
}

// autoDoneSkip says why the hub would not judge t now: "" when it would. work is when one of its agents last started
// a turn (worked false: not known, a hub restarted since).
func autoDoneSkip(t Todo, work time.Time, worked bool) string {
	switch {
	case t.Origin == "":
		return "not_a_task" // a todo the user made by hand: only they close it
	case t.DoneAt != nil:
		return "done"
	case t.ReopenedAt != nil && !(worked && work.After(*t.ReopenedAt)):
		return "reopened" // no new turn since the user reopened it
	}
	return ""
}

// taskReplies are the last replies of t's active agents: the whole last answer read from the agent's transcript, or
// the session list's clipped last_text when that cannot be read.
func (s *Service) taskReplies(ctx context.Context, t Todo) (out []prompts.TaskDoneReply, sideTurnBlocked bool) {
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		m, ok := s.device(ctx, a.MachineID)
		if !ok {
			continue
		}
		items, read := s.lastTurnItems(ctx, m.ID, a.SessionID)
		if read && taskTurnAwaitingAnswer(items) {
			return nil, false // one unfinished agent invalidates the whole task's completion evidence
		}
		ask, text := turnExchange(items)
		from, source := taskTurnSource(t, items)
		if source != "" {
			current, ok := s.todoNow(t.ID)
			s.tasks.mu.Lock()
			since := s.tasks.working[t.ID]
			s.tasks.mu.Unlock()
			if report, evidence, valid := freshTaskDoneReport(current, items, since); ok && valid {
				text = strings.Join(append([]string{text, report}, evidence...), "\n")
			} else {
				sideTurnBlocked = true
			}
		}
		if text == "" {
			if c, ok := s.session(ctx, m.ID, a.SessionID); ok {
				text = strings.TrimSpace(c.LastText)
			}
		}
		if text == "" {
			continue
		}
		if source == "" && sameRequest(ask, t.Notes) {
			ask = "" // the turn that answers the task's own prompt: the prompt is already in the input
		}
		out = append(out, prompts.TaskDoneReply{Machine: m.Name, From: from, Source: source, Ask: clipMiddle(ask, 500), Text: clipMiddle(text, 1500)})
	}
	return out, sideTurnBlocked
}

// lastTurn reads the latest exchange, not necessarily a user request about the task.
// Completion separately retains and gates its peer/injected provenance.
func (s *Service) lastTurn(ctx context.Context, machine, sid string) (ask, answer string) {
	items, _ := s.lastTurnItems(ctx, machine, sid)
	return turnExchange(items)
}

// lastTurnItems are the items of the agent's latest turn, as the node pages them (from its last user prompt on);
// ok is false when the node could not be asked.
func (s *Service) lastTurnItems(ctx context.Context, machine, sid string) (items []proto.Item, ok bool) {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.call(cctx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "turns": 1}, &res); err != nil {
		return nil, false
	}
	return res.Items, true
}

// turnExchange is what a turn's items say: the user's ask (its first user message; "" when none) and the agent's
// last reply ("" when none).
func turnExchange(items []proto.Item) (ask, answer string) {
	for _, it := range items {
		if it.Kind == proto.KindText && it.Role == "user" && strings.TrimSpace(it.Text) != "" {
			ask = strings.TrimSpace(it.Text)
			break
		}
	}
	for i := len(items) - 1; i >= 0; i-- {
		if it := items[i]; it.Kind == proto.KindText && it.Role == "assistant" && strings.TrimSpace(it.Text) != "" {
			return ask, strings.TrimSpace(it.Text)
		}
	}
	return ask, ""
}

// userStoppedNotice is the notice the transcript reader puts where the user stopped a turn (Claude Code's "[Request
// interrupted by user]", Codex's turn_aborted; transcript/user_text.go, codex.go).
const userStoppedNotice = transcript.InterruptedNotice

// silentTurn: a turn the node could read (it has items, at least the prompt) in which the agent neither replied nor
// called a tool. Claude Code ends a turn so, without an error, when the model service answers 200 with nothing in it
// (it retries once with a hidden "no visible output" prompt, then stops); Codex's rollout maps to the same items.
// Thinking alone is no output: the user sees nothing.
func silentTurn(items []proto.Item) bool {
	if len(items) == 0 {
		return false // nothing to go by (an older node, a runtime that does not page items): not judged silent
	}
	for _, it := range items {
		if it.Kind == proto.KindNotice && it.Text == userStoppedNotice {
			return false // the user stopped it (Esc): no failure to report
		}
		if it.Role != "assistant" {
			continue
		}
		switch it.Kind {
		case proto.KindToolUse, proto.KindImage:
			return false
		case proto.KindText:
			if strings.TrimSpace(it.Text) != "" {
				return false
			}
		}
	}
	return true
}

// sameRequest: ask is the task's own prompt (as sent, maybe with context around it).
func sameRequest(ask, prompt string) bool {
	ask, prompt = oneLine(ask), oneLine(prompt)
	if ask == "" || prompt == "" {
		return ask == prompt
	}
	if r := []rune(prompt); len(r) > 80 {
		prompt = string(r[:80])
	}
	return strings.Contains(ask, prompt)
}

// clipMiddle keeps a long text's first and last n runes: a final answer says what was done first and what is left last.
func clipMiddle(s string, n int) string {
	if utf8.RuneCountInString(s) <= 2*n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "\n…\n" + string(r[len(r)-n:])
}

// judgeTaskDone asks the judge model whether t's work is finished. An error: no model, or no answer to go by.
func (s *Service) judgeTaskDone(ctx context.Context, t Todo) (taskDoneCheck, error) {
	replies, sideTurnBlocked := s.taskReplies(ctx, t)
	c := taskDoneCheck{Replies: len(replies), Signals: doneSignals(replies)}
	if sideTurnBlocked {
		c.Verdict, c.Reason = verdictNotDone, verdictWords[verdictNotDone]
		s.logf("task %s: side-conversation reply without current task completion evidence: left open", t.ID)
		return c, nil
	}
	if len(replies) == 0 {
		c.Verdict, c.Reason = verdictUnsure, "没读到 Agent 的回复"
		return c, nil
	}
	for _, r := range replies {
		if cutOff(r.Text) { // its turn was cut off by the model service: not finished, whatever came before (#1516)
			c.Verdict, c.Reason = verdictNotDone, interruptedSummary
			return c, nil
		}
	}
	delivery := s.doneNeedsDelivery(ctx)
	system := prompts.RenderTaskDone(prompts.TaskDone{Owner: s.owner(ctx), Delivery: delivery})
	var criteria []string
	if t.Supervision != nil {
		criteria = t.Supervision.DoneWhen
	}
	input := prompts.RenderTaskDoneInput(prompts.TaskDoneInput{Title: t.Title, Prompt: clipMiddle(strings.TrimSpace(t.Notes), 1500), Criteria: criteria,
		Replies: replies, MergedPR: c.Signals.MergedPR, Build: c.Signals.Build})
	var out struct {
		Verdict  string `json:"verdict"`
		Reason   string `json:"reason"`
		Delivery string `json:"delivery"`
		Gate     string `json:"gate"`
		Unblock  string `json:"unblock"`
	}
	if err := s.purposeModel("inbox.judge").JSON(ctx, system, input, &out); err != nil {
		return c, err
	}
	switch v := strings.ToLower(strings.TrimSpace(out.Verdict)); v {
	case verdictDone, verdictNotDone, verdictWaiting, verdictNeedsUser:
		c.Verdict = v
	default:
		c.Verdict = verdictUnsure // a verdict it made up is no verdict
	}
	c.Reason = clip(oneLine(out.Reason), 40)
	c.WouldClose = c.Verdict == verdictDone
	if delivery {
		d := judgedDelivery(out.Delivery, out.Gate, out.Unblock, replies)
		d.At = time.Now()
		c.Delivery = &d
		if c.Verdict == verdictDone && !deliveryClosable(d.State) {
			c.Verdict, c.WouldClose = verdictUndelivered, false
		}
	}
	return c, nil
}

// autoFinishTask: t's receipt went out (its agents all stopped). work is when one of them last started a turn. The
// todo is marked done when the judge finds the work finished; otherwise it stays as it is.
func (s *Service) autoFinishTask(t Todo, work time.Time, worked bool) {
	if skip := autoDoneSkip(t, work, worked); skip != "" {
		if skip == "reopened" {
			s.logf("task %s: reopened by the user and no new turn since: not judged", t.ID)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskDoneTimeout)
	defer cancel()
	if !worked {
		work = t.CreatedAt
	}
	if !s.taskCompletionCurrent(ctx, t, work) {
		return
	}
	c, err := s.judgeTaskDone(ctx, t)
	if !s.taskCompletionCurrent(ctx, t, work) {
		s.logf("task %s: work changed while judged: left open", t.ID)
		return
	}
	if err == nil && c.Delivery != nil && c.Verdict != verdictNotDone {
		s.setTaskDelivery(ctx, t.ID, *c.Delivery) // where it is held, on its card (delivery.go)
	}
	if t.Supervision != nil {
		s.tasks.completionMu.Lock()
		defer s.tasks.completionMu.Unlock()
		if !s.taskWorkCurrent(t.ID, work) {
			return
		}
		if !worked {
			work = t.CreatedAt
		}
		s.superviseJudged(ctx, t, work, c, err) // never closed here: its supervisor reviews it (supervise.go)
		return
	}
	if err != nil {
		if !ai.IsPaused(err) {
			s.logf("task %s: done judge: %v (left open)", t.ID, err)
		}
		return
	}
	if !c.WouldClose {
		s.logf("task %s: not closed: %s (%s)", t.ID, c.Verdict, c.Reason)
		return
	}
	reason := c.Reason
	if reason == "" {
		reason = "做完了"
	}
	if !worked {
		work = t.CreatedAt
	}
	ok, err := s.finishCurrentTask(ctx, t.ID, reason, work)
	switch {
	case err != nil:
		s.logf("task %s: auto done: %v", t.ID, err)
	case ok:
		s.logf("task %s: done (auto): %s", t.ID, reason)
		s.taskClosedReport(t, reason)
	default:
		s.logf("task %s: changed while judged: left as it is", t.ID)
	}
}

// doneCheckRoute: POST /api/v1/todos/{id}/done-check runs the judgment on a todo as it is and answers it; nothing is
// written (for looking into why a task was or was not closed).
func (s *Service) doneCheckRoute() {
	s.handle("POST /api/v1/todos/{id}/done-check", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot judge todos"))
			return
		}
		if err := s.ensureTodosLoaded(r.Context()); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("todos service unavailable"))
			return
		}
		id := r.PathValue("id")
		t, ok, err := s.todo(r.Context(), id)
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errors.New("todos service unavailable"))
			return
		}
		if !ok {
			apierr.Write(w, http.StatusNotFound, db.ErrTodoNotFound)
			return
		}
		s.tasks.mu.Lock()
		work, worked := s.tasks.working[id]
		s.tasks.mu.Unlock()
		ctx, cancel := context.WithTimeout(r.Context(), taskDoneTimeout)
		defer cancel()
		c, err := s.judgeTaskDone(ctx, t)
		if err != nil {
			apierr.Write(w, http.StatusBadGateway, err)
			return
		}
		c.Skip = autoDoneSkip(t, work, worked)
		if c.Skip != "" {
			c.WouldClose = false
		}
		apierr.WriteJSON(w, c)
	})
}
