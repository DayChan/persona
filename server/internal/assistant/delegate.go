package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// An agent's question comes here before the user (fleet_delegate.go, docs/assistant/assistant.md "The assistant answers questions first"): a model
// (purpose agent.delegate, "Model Settings › Answering questions") reads the question with what is known around it — the agent's
// recent turns, its task and the calls the user made on it, the project — and answers it for the user when it is the
// agent's own call, or leaves it to the user with the context filled in. What it may answer is checked here as well:
// the options it picks must be the question's, and none of them may set off a red line (riskyWords). The hub waits a
// few minutes at most (fleet_delegate.go) and asks the user as before when there is no answer.

// DelegateParams is a new question of an agent's, before the user is told of it (fleet_delegate.go).
type DelegateParams struct {
	Interaction proto.Interaction `json:"interaction"`
}

// DelegateResult is what the assistant made of it. Answer: it answers for the user, with Answers (option labels, by
// question) and Reason, as interaction_answer does. Otherwise the user is asked, with Ask's context when it filled
// that in (nil: the card as it came).
type DelegateResult struct {
	Answer  bool              `json:"answer"`
	Answers map[string]string `json:"answers,omitempty"`
	Reason  string            `json:"reason,omitempty"`
	Ask     *proto.Delegate   `json:"ask,omitempty"`
}

// delegateOut is what the model answers (prompts/agents/delegate.md).
type delegateOut struct {
	Decision string                     `json:"decision"`
	Answers  map[string]json.RawMessage `json:"answers"`
	Reason   string                     `json:"reason"`
	What     string                     `json:"what"`
	WhyNow   string                     `json:"why_now"`
	Rec      string                     `json:"recommend"`
	Cost     string                     `json:"cost"`
}

// delegateDeadline is how long the user has for a question left to them (the card's expiry when that is sooner).
const delegateDeadline = 24 * time.Hour

// Delegate is assistant.delegate: answer ix for the user, or say what the user should know to answer it.
func (s *Service) Delegate(ctx context.Context, p DelegateParams) (DelegateResult, error) {
	ix := p.Interaction
	asIs := DelegateResult{}
	if ix.Kind != proto.IxQuestion || len(ix.Questions) == 0 || len(ix.Fields) > 0 || ix.ToolName == "ask_user" {
		return asIs, nil
	}
	c, ok := s.session(ctx, ix.MachineID, ix.SessionID)
	if !ok || c.Assistant {
		return asIs, nil // an assistant conversation's question is the user's (interaction_answer refuses it too)
	}
	if ix.ToolName == workCardTool && !s.workCardDelegable(ix) {
		return asIs, nil
	}
	in := s.delegateInput(ctx, ix, c)
	var out delegateOut
	sys := prompts.RenderAgentDelegate(prompts.AgentDelegate{Owner: s.owner(ctx), Rules: strings.TrimSpace(s.hello(ctx).Agents.Delegate.Rules)})
	if err := s.purposeModel("agent.delegate").JSON(ctx, sys, prompts.RenderAgentDelegateInput(in), &out); err != nil {
		s.workEvent(ix.MachineID, ix.SessionID, "delegate_failed", map[string]any{"error": clip(err.Error(), 200)})
		return asIs, err
	}
	res := delegateResult(ix, out, time.Now())
	kind := "delegate_asked"
	if res.Answer {
		kind = "delegate_answered"
	}
	s.workEvent(ix.MachineID, ix.SessionID, kind, map[string]any{"questions": len(ix.Questions)})
	return res, nil
}

// delegateResult turns the model's answer into the hub's: an answer only when it is a full, valid one that sets off
// nothing on a red line; anything else is left to the user, with the context the model gave.
func delegateResult(ix proto.Interaction, out delegateOut, now time.Time) DelegateResult {
	ask := &proto.Delegate{What: clip(oneLine(out.What), 120), WhyNow: clip(oneLine(out.WhyNow), 120),
		Recommend: clip(oneLine(out.Rec), 120), Cost: clip(oneLine(out.Cost), 120), Reason: clip(oneLine(out.Reason), 120)}
	deadline := now.Add(delegateDeadline).UTC()
	if !ix.ExpiresAt.IsZero() && ix.ExpiresAt.Before(deadline) {
		deadline = ix.ExpiresAt.UTC()
	}
	ask.Deadline = &deadline
	if out.Decision != "answer" {
		return DelegateResult{Ask: ask}
	}
	raw, _ := json.Marshal(out.Answers)
	answers, err := interactionAnswers(ix, raw)
	if err != nil {
		return DelegateResult{Ask: ask}
	}
	if picksRisky(ix, answers) {
		// It would have the agent do something on a red line: the user's, with what the model picked recommended.
		ask.Reason, ask.Recommend = "", answerLine(ix, answers)
		return DelegateResult{Ask: ask}
	}
	return DelegateResult{Answer: true, Answers: answers, Reason: clip(oneLine(out.Reason), 120)}
}

// picksRisky: one of the options picked, by its label or its description, is a step the assistant must not set off
// (riskyWords: push, deploy, merge, a message to someone…). Holding off ("Don't merge yet", "Don't send") sets off nothing.
func picksRisky(ix proto.Interaction, answers map[string]string) bool {
	for _, q := range ix.Questions {
		for _, label := range strings.Split(answers[q.Question], ", ") {
			if holdsOff(label) {
				continue
			}
			texts := []string{label}
			for _, o := range q.Options {
				if o.Label == label {
					texts = append(texts, o.Description)
				}
			}
			if risky(texts...) {
				return true
			}
		}
	}
	return false
}

// answerLine is answers in a line, the questions' order ("A; B" with a full-width semicolon).
func answerLine(ix proto.Interaction, answers map[string]string) string {
	var parts []string
	for _, q := range ix.Questions {
		if a := answers[q.Question]; a != "" {
			parts = append(parts, a)
		}
	}
	return clip(strings.Join(parts, "；"), 120)
}

// workCardDelegable: one of the manager's own cards may be answered for the user. Not one that asks to open a link
// (an authorization) or to go ahead with a risky step: those are the user's by design (agents_work_act.go actOn).
func (s *Service) workCardDelegable(ix proto.Interaction) bool {
	e, ok := s.workEntry([2]string{ix.MachineID, ix.SessionID})
	if !ok || e.InteractionID != ix.ID {
		return true // its work moved on; the card is what it asks
	}
	return e.Work.Link == nil && !risky(e.Work.Then)
}

// delegateInput gathers what the model reads about ix: the agent, its recent turns, its task and the calls made on it.
func (s *Service) delegateInput(ctx context.Context, ix proto.Interaction, c proto.Session) prompts.AgentDelegateInput {
	name := ix.MachineID
	if d, ok := s.device(ctx, ix.MachineID); ok && d.Name != "" {
		name = d.Name
	}
	in := prompts.AgentDelegateInput{
		Now: time.Now().In(s.location(ctx)).Format("2006-01-02 15:04 Mon"), Agent: agentLabel(c), Machine: name, Cwd: c.Cwd,
		Project: strings.Join(first(s.projectsFor(ctx, ix.MachineID, c.Cwd), 3), "、"),
	}
	if t, ok := s.todoOfSession(ctx, ix.MachineID, ix.SessionID); ok {
		in.Task = clip(strings.TrimSpace(t.Title+"\n"+t.Notes), 600)
		in.Decisions = s.todoDecisionsContext(ctx, t.ID)
	} else if ix.TaskTitle != "" {
		in.Task = ix.TaskTitle
	}
	in.Asks, in.Reply = s.recentTurns(ctx, ix.MachineID, ix.SessionID)
	if in.Reply == "" {
		in.Reply = clipMiddleRunes(strings.TrimSpace(c.LastText), 800)
	}
	if ix.ToolName == workCardTool && ix.Summary != "" && in.Reply == "" {
		in.Reply = ix.Summary
	}
	for _, q := range ix.Questions {
		dq := prompts.DelegateQuestion{Question: q.Question, MultiSelect: q.MultiSelect}
		for _, o := range q.Options {
			dq.Options = append(dq.Options, prompts.DelegateOption{Label: o.Label, Description: o.Description})
		}
		in.Questions = append(in.Questions, dq)
	}
	return in
}

// recentTurns are the user's messages in the agent's last few turns (oldest first, clipped) and its last reply.
func (s *Service) recentTurns(ctx context.Context, machine, sid string) (asks []string, reply string) {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.call(cctx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "turns": 3, "fold_work": true}, &res); err != nil {
		return nil, ""
	}
	for _, it := range res.Items {
		if it.Kind != proto.KindText || strings.TrimSpace(it.Text) == "" {
			continue
		}
		switch it.Role {
		case "user":
			asks = append(asks, clipMiddleRunes(strings.TrimSpace(it.Text), 400))
		case "assistant":
			reply = it.Text
		}
	}
	if len(asks) > 3 {
		asks = asks[len(asks)-3:]
	}
	return asks, clipMiddleRunes(strings.TrimSpace(reply), 1200)
}

// todoOfSession is the open todo the agent machine/sid works for, a task or not.
func (s *Service) todoOfSession(ctx context.Context, machine, sid string) (Todo, bool) {
	if s.ensureTodosLoaded(ctx) != nil {
		return Todo{}, false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	t := s.todos.todos[s.todos.activeBySession[[2]string{machine, sid}]]
	if t == nil || t.DoneAt != nil {
		return Todo{}, false
	}
	return copyTodoLocked(t), true
}

// holdOffWords start an option that has the agent not do something.
var holdOffWords = []string{"先不", "暂不", "不要", "不用", "别", "先别", "不", "skip", "don't", "do not", "not now", "hold"}

// holdsOff: label is a short option that starts with not doing something ("Don't merge yet", "Not now"). A longer one may go
// on to something else ("no need to wait for tests, just release"): it is not taken for one.
func holdsOff(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	if utf8.RuneCountInString(l) > 10 {
		return false
	}
	for _, w := range holdOffWords {
		if strings.HasPrefix(l, w) {
			return true
		}
	}
	return false
}
