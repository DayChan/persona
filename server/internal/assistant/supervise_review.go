package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the assistant's task_review tool ----

func (s *Service) toolTaskReview(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		TodoID  string        `json:"todo_id"`
		Verdict string        `json:"verdict"`
		Checks  []ReviewCheck `json:"checks"`
		Note    string        `json:"note"`
		// The conclusion card's (conclusion.go): accepted's outcome (done, or cannot be done), its one sentence of why and
		// its one link; needs_user's recommended option.
		Outcome   string `json:"outcome"`
		Reason    string `json:"reason"`
		Link      string `json:"link"`
		Recommend string `json:"recommend"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	a.Note = strings.TrimSpace(a.Note)
	switch a.Verdict {
	case reviewAccepted, reviewRework, reviewNeedsUser, reviewCancelled:
	default:
		return "", fmt.Errorf("verdict must be accepted, rework, needs_user or cancelled")
	}
	switch a.Outcome = strings.TrimSpace(a.Outcome); a.Outcome {
	case "", outcomeDone, outcomeCannot:
	default:
		return "", fmt.Errorf("outcome must be done or cannot")
	}
	if a.Verdict == reviewRework && a.Note == "" && !slices.ContainsFunc(a.Checks, func(c ReviewCheck) bool { return !c.Met }) {
		return "", fmt.Errorf("rework: say what is missing (a check that is not met, or note)")
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", err
	}
	t, ok := s.todoNow(strings.TrimSpace(a.TodoID))
	if !ok {
		return "", fmt.Errorf("no task %q", a.TodoID)
	}
	// The main conversation may review any supervised task: after the main conversation changes, the old tasks'
	// supervision messages come to the new one, which has to be able to close them (#4262).
	isMain := s.isMain(ctx, self)
	if t.Origin == "" || t.Origin != self && (t.Supervision == nil || t.Supervision.Supervisor != self && !isMain) {
		return "", fmt.Errorf("task %s was not handed out from this conversation", t.ID)
	}
	if t.DoneAt != nil && (a.Verdict == reviewRework || a.Verdict == reviewNeedsUser) {
		return "", fmt.Errorf("task %s is done already (%s): hand out a new task for what is left", t.ID, cmp.Or(t.DoneReason, "closed"))
	}
	checks := make([]ReviewCheck, 0, len(a.Checks))
	for _, c := range a.Checks {
		c.Criterion, c.Evidence = clip(oneLine(c.Criterion), 200), clip(strings.TrimSpace(c.Evidence), 600)
		if c.Criterion != "" {
			checks = append(checks, c)
		}
	}
	verdict, note := a.Verdict, clip(a.Note, 2000)
	// Only the main conversation supervises (task_plan, quietAgentTask): another conversation's task without a ledger
	// gets none here either, or its signals would go into the main conversation's supervision messages (#4262). It can still
	// be closed; rework and needs_user are what supervision is for.
	ledger := t.Supervision != nil || isMain
	if !ledger && (verdict == reviewRework || verdict == reviewNeedsUser) {
		return "", fmt.Errorf("task %s is not supervised: send what is missing to its agent with agent_send, or ask the user in your answer", t.ID)
	}
	over := false
	var v Supervision
	var err error
	if ledger {
		v, _, err = s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
			if isMain {
				v.Supervisor = self // the main conversation takes over an old main conversation's task
			} else if v.Supervisor == "" {
				v.Supervisor = t.Origin
			}
			if verdict == reviewRework && v.Round >= maxReworkRounds {
				verdict, over = reviewNeedsUser, true
			}
			r := TaskReview{At: time.Now().UTC(), Verdict: verdict, Checks: checks, Note: note}
			if verdict == reviewRework {
				v.Round++
				r.Round = v.Round
			}
			v.State = verdict
			v.Reviews = append(v.Reviews, r)
			if len(v.Reviews) > 20 {
				v.Reviews = v.Reviews[len(v.Reviews)-20:]
			}
			return true
		})
	}
	if err != nil {
		return "", err
	}
	b := &s.supervise
	b.mu.Lock()
	b.init()
	for _, k := range []string{sigReview, sigIdle, sigNeedsYou, sigStalled} {
		delete(b.pending, t.ID+"|"+k) // reviewed: what waited is answered
	}
	b.mu.Unlock()
	_, _ = s.appendFeed(ctx, t.Origin, feedReview, t.ID, reviewFeedBody{Title: t.Title, Verdict: verdict, Round: v.Round, Note: note, Checks: checks})
	if verdict != reviewNeedsUser && verdict != reviewAccepted {
		s.dropDecision(ctx, t.ID) // the review moved on: a decision still waiting on the owner is over (conclusion.go)
	}
	conclusion := conclusionInput{Outcome: cmp.Or(a.Outcome, outcomeDone), Reason: a.Reason, Link: a.Link, Note: note, Checks: checks}
	switch verdict {
	case reviewAccepted, reviewCancelled:
		reason := cmp.Or(clip(oneLine(note), 80), "验收通过")
		if verdict == reviewCancelled {
			reason = "取消：" + cmp.Or(clip(oneLine(note), 80), "不做了")
		}
		carded := false
		if t.DoneAt == nil {
			closed, err := s.reviewDoneTodo(ctx, t.ID, reason, verdict == reviewAccepted)
			if err != nil {
				return "", err
			}
			switch {
			case closed && ledger && verdict == reviewAccepted:
				// The conclusion is the one push about it: its card, not a line in the task report (#1226).
				s.dropTaskReport(t.ID)
				if cur, ok := s.todoNow(t.ID); ok {
					carded = s.askConclusion(ctx, cur, conclusion)
				}
			case closed:
				s.taskClosedReport(t, reason) // the bot's task report says it is closed, as for the judge's (reports.go)
			}
		}
		if verdict == reviewCancelled {
			return fmt.Sprintf("Task %s is cancelled and closed; its agent is closed once it stays still. Tell the user only if they need to know.", t.ID), nil
		}
		if carded {
			return fmt.Sprintf("Task %s is accepted and closed; its agent is closed once it stays still. Its conclusion card (the verdict, "+
				"why in a sentence, the link) went to the user in Lark: don't push the result again. Hand out or schedule anything that "+
				"follows (deploy, TestFlight, closing an issue).", t.ID), nil
		}
		return fmt.Sprintf("Task %s is accepted and closed; its agent is closed once it stays still. Tell the user the result in a line or two "+
			"if they are waiting for it, and hand out or schedule anything that follows (deploy, TestFlight, closing an issue).", t.ID), nil
	case reviewNeedsUser:
		conclusion.Outcome, conclusion.Recommend = outcomeDecide, a.Recommend
		if over && a.Reason == "" { // the note is the rework asked for, written to the agent: not the owner's question
			conclusion.Reason, conclusion.Note = i18n.T(s.botLang(ctx), "assistant-reports.verdict_over", i18n.Args{"rounds": maxReworkRounds}), ""
		}
		carded := s.askConclusion(ctx, t, conclusion)
		// A recommendation is also pushed to their phone, whose OK takes it (todo_ask.go), with the bot on or off. The hub
		// taking the push does not say a device got it, nor that the app shows its OK: without a card the supervisor still
		// asks, and is told OK may come first.
		pushed := ""
		if s.pushDecision(ctx, t.ID) {
			pushed = " A push with your recommendation also went to their phone; its \"OK\" takes the recommendation and sends it " +
				"to the task's agent, which ends the decision too (drop a question of yours about it then)."
		}
		if carded {
			said := "a card with what they are to decide went to them in Lark, and their answer goes to the task's agent"
			if conclusion.Recommend != "" {
				said = "a card with what they are to decide and your recommendation went to them in Lark; \"OK\" takes the " +
					"recommendation, and their answer goes to the task's agent"
			}
			if over {
				return fmt.Sprintf("Task %s was sent back %d times already: it now waits for the user (needs_user): %s. Don't ask them "+
					"again; review it once the agent stops.%s", t.ID, maxReworkRounds, said, pushed), nil
			}
			return fmt.Sprintf("Task %s waits for the user: %s. Don't ask them again; review it once the agent stops.%s", t.ID, said, pushed), nil
		}
		if over {
			return fmt.Sprintf("Task %s was sent back %d times already: it now waits for the user (needs_user). Tell the user what is "+
				"still missing and ask how to go on (ask_user or in your answer); review it again once they decide (that also ends the decision "+
				"the briefing lists for it).%s", t.ID, maxReworkRounds, pushed), nil
		}
		return fmt.Sprintf("Task %s waits for the user. Ask them (ask_user, or in your answer, pushed with notify_user if they are away); "+
			"review it again once they decide (that also ends the decision the briefing lists for it).%s", t.ID, pushed), nil
	}
	text := prompts.RenderSuperviseRework(prompts.SuperviseRework{Round: v.Round, Max: maxReworkRounds, Checks: reworkChecks(checks), Note: note})
	sent, err := s.sendToTask(ctx, t, text)
	if err != nil {
		return "", fmt.Errorf("recorded rework round %d, but sending it to the agent failed: %v (send it with agent_send)", v.Round, err)
	}
	return fmt.Sprintf("Rework round %d of %d is %s. When the agent stops again you get a 督办 message to review it again.", v.Round,
		maxReworkRounds, sent), nil
}

func reworkChecks(list []ReviewCheck) []prompts.SuperviseCheck {
	out := make([]prompts.SuperviseCheck, 0, len(list))
	for _, c := range list {
		out = append(out, prompts.SuperviseCheck{Criterion: c.Criterion, Met: c.Met, Evidence: c.Evidence})
	}
	return out
}

// feedReview is a review in the conversation's feed: ref the todo id, body reviewFeedBody.
const feedReview = "review"

type reviewFeedBody struct {
	Title   string        `json:"title"`
	Verdict string        `json:"verdict"`
	Round   int           `json:"round,omitempty"`
	Note    string        `json:"note,omitempty"`
	Checks  []ReviewCheck `json:"checks,omitempty"`
}

// reviewDoneTodo closes todo id after its review (done_by review or cancelled, with reason); false when it was done already. An
// accepted task's outcome is recorded, as when it closes by itself or the user closes it (todo_outcome.go, #2820); a
// cancelled one came to nothing and records none.
func (s *Service) reviewDoneTodo(ctx context.Context, id, reason string, accepted bool) (bool, error) {
	s.todos.writing.Lock() // memory takes the rows in the order the store committed them (todoBook.writing)
	now := time.Now().UTC()
	var row db.TodoRow
	if st := s.supervisionDB(); st != nil && s.todoDB() != nil {
		r, ok, err := st.ReviewDoneTodo(ctx, id, reason, accepted, now)
		if err != nil || !ok {
			s.todos.writing.Unlock()
			return false, err
		}
		row = *r
	} else {
		t, ok := s.todoNow(id)
		if !ok {
			s.todos.writing.Unlock()
			return false, db.ErrTodoNotFound
		}
		row = db.TodoRow{ID: t.ID, Title: t.Title, Notes: t.Notes, CreatedAt: t.CreatedAt, Origin: t.Origin, PlanID: t.PlanID,
			ReopenedAt: t.ReopenedAt, Quiet: t.Quiet}
		row.DoneAt, row.DoneBy, row.DoneReason, row.UpdatedAt = &now, db.DoneByReview, reason, now
		if !accepted {
			row.DoneBy = db.DoneByCancelled
		}
		t.keepPlan(&row)
	}
	var before Todo
	_, doneKeys, ok := s.setTodoRow(id, row, func(t *Todo) bool {
		if t.DoneAt == nil || s.supervisionDB() != nil {
			before = copyTodoLocked(t) // its agents as they worked on it, before done releases them
			return true
		}
		return false
	})
	s.todos.writing.Unlock()
	if ok {
		s.todoRowSet(ctx, doneKeys)
	}
	if ok && accepted && row.DoneAt != nil {
		s.goRecordTaskOutcome(before, *row.DoneAt)
	}
	return ok, nil
}

// pickTaskAgent is the agent of a task that most needs what is sent to it: one waiting for the user, then working,
// then done unseen, then the latest.
func pickTaskAgent(agents []proto.Session) proto.Session {
	rank := map[string]int{proto.StateNeedsYou: 0, proto.StateWorking: 1, proto.StateUnread: 2}
	list := slices.Clone(agents)
	sort.SliceStable(list, func(i, j int) bool {
		ri, ok := rank[list[i].State]
		if !ok {
			ri = 3
		}
		rj, ok := rank[list[j].State]
		if !ok {
			rj = 3
		}
		if ri != rj {
			return ri < rj
		}
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	return list[0]
}

// sendToTask sends text to task t's agent (pickTaskAgent) and keeps it in the conversation's feed as Persona's
// (routedFromAssistant); an agent closed after a stretch of work is resumed first (that can take minutes: it goes on in
// the background). What it says about it: "sent to the agent" or "being sent".
func (s *Service) sendToTask(ctx context.Context, t Todo, text string) (string, error) {
	agents := s.taskAgents(ctx, t)
	if len(agents) == 0 {
		return "", errors.New("the task has no agent")
	}
	c := pickTaskAgent(agents)
	if _, ok := s.device(ctx, c.MachineID); !ok {
		return "", errors.New("its machine is offline")
	}
	send := func(ctx context.Context) error {
		if err := s.call(ctx, c.MachineID, DeviceSessionSend, map[string]any{"session_id": c.SessionID, "text": text}, nil); err != nil {
			return err
		}
		_, err := s.appendFeed(ctx, t.Origin, feedRouted, t.ID, map[string]string{"text": text, "from": routedFromAssistant})
		return err
	}
	if c.Status == proto.StatusOffline {
		go func() {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			if err := send(sctx); err != nil {
				s.logf("task %s: rework: %v", t.ID, err)
			}
		}()
		return "being sent: its agent was closed and is resuming first", nil
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	if err := send(sctx); err != nil {
		return "", err
	}
	return "sent to the agent", nil
}
