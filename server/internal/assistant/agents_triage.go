package assistant

import (
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The triage's model (purpose agent.triage, "Model Settings › Triage"'s): the hub's models.json, the one place the Agents
// business uses a model besides the tab names (agents_start.go).

// triageOut is what the triage model answers (prompts/agents/triage.md).
type triageOut struct {
	Phase     string           `json:"phase"`
	On        string           `json:"on"`
	Line      string           `json:"line"`
	Questions []proto.Question `json:"questions"`
	Link      *struct {
		URL     string `json:"url"`
		Minutes int    `json:"minutes"`
	} `json:"link"`
	Wait *struct {
		What         string `json:"what"`
		Then         string `json:"then"`
		CheckMinutes int    `json:"check_minutes"`
		Agent        string `json:"agent"`
		Risky        bool   `json:"risky"`
		// Approved: the model says this is the very wait and next step the user already approved (same thing waited
		// for, same step on the same target); only asked about when there is such an approval (#3824).
		Approved bool `json:"approved"`
	} `json:"wait"`
	Artifacts []proto.WorkArtifact `json:"artifacts"`
}

// triageTurnModel asks the triage model where c stands after its turn. An answer it cannot read is unsure: a work state
// with no phase (nothing shown, nothing done; the turn is not triaged again).
// same: the model says the turn still waits on the very step the user approved (#3824), false when there is none.
func (s *Service) triageTurnModel(ctx context.Context, v *workView, machine string, c proto.Session) (w *proto.WorkState, same bool, err error) {
	ask, reply := s.lastTurnOf(ctx, machine, c.SessionID)
	if reply == "" {
		reply = strings.TrimSpace(c.LastText)
	}
	if reply == "" {
		return &proto.WorkState{}, false, nil
	}
	others, byShort := triageOthers(machine, c.SessionID, v.sessions(machine))
	name := machine
	if d, ok := v.device(machine); ok && d.Name != "" {
		name = d.Name
	}
	in := prompts.AgentTriageInput{
		Now: time.Now().In(s.location(ctx)).Format("2006-01-02 15:04 Mon"), Agent: agentLabel(c), Machine: name,
		Cwd: c.Cwd, Ask: clipMiddleRunes(ask, 500), Reply: clipMiddleRunes(reply, 1500), Others: others,
		Project: strings.Join(first(s.projectsFor(ctx, machine, c.Cwd), 3), "、"),
	}
	s.work.mu.Lock()
	if a, ok := s.work.approved[[2]string{machine, c.SessionID}]; ok {
		in.ApprovedWait, in.ApprovedThen = a.wait, a.then
	}
	s.work.mu.Unlock()
	var out triageOut
	if err := s.purposeModel("agent.triage").JSON(ctx, prompts.RenderAgentTriage(prompts.AgentTriage{Owner: s.owner(ctx), Language: answerLanguage(s.hello(ctx).Locale)}),
		prompts.RenderAgentTriageInput(in), &out); err != nil {
		return nil, false, err
	}
	same = out.Wait != nil && out.Wait.Approved && in.ApprovedThen != ""
	return triageWork(out, byShort, time.Now(), s.lang(ctx)), same, nil
}

// projectsFor are the names of the knowledge base's projects whose directories hold cwd on machine (none when it
// cannot say): the assistant's own knowledge base (kb.go).
func (s *Service) projectsFor(ctx context.Context, machine, cwd string) []string {
	e, err := s.kbGet(ctx)
	if err != nil || e == nil {
		return nil
	}
	var names []string
	for _, p := range e.ProjectsForDir(machine, cwd) {
		names = append(names, p.Name)
	}
	return names
}

// triageWork turns the model's answer into a work state, keeping only what it may say.
// The texts the hub fills in itself are in lang (the user's).
func triageWork(out triageOut, others map[string]proto.WorkAgent, now time.Time, lang i18n.Lang) *proto.WorkState {
	w := &proto.WorkState{Line: clip(oneLine(out.Line), 60)}
	for _, a := range first(out.Artifacts, 5) {
		if k := strings.ToLower(strings.TrimSpace(a.Kind)); slices.Contains([]string{"pr", "commit", "doc", "build", "url"}, k) && strings.TrimSpace(a.Ref) != "" {
			w.Artifacts = append(w.Artifacts, proto.WorkArtifact{Kind: k, Ref: clip(strings.TrimSpace(a.Ref), 300)})
		}
	}
	switch strings.ToLower(strings.TrimSpace(out.Phase)) {
	case proto.WorkDone:
		w.Phase = proto.WorkDone
		if w.Line == "" {
			w.Line = i18n.T(lang, "assistant-agents_triage.done")
		}
		return w
	case proto.WorkBlocked:
		w.Phase = proto.WorkBlocked
	default:
		return &proto.WorkState{} // unsure, or made up
	}
	w.On = strings.ToLower(strings.TrimSpace(out.On))
	if wt := out.Wait; wt != nil {
		w.Wait, w.Then, w.Every = clip(oneLine(wt.What), 80), clip(oneLine(wt.Then), 80), wt.CheckMinutes
		if a, ok := others[strings.TrimSpace(wt.Agent)]; ok && w.On == proto.WaitAgent {
			w.Agent = &a
		}
		if wt.Risky {
			w.On = proto.WaitUser // never a wait the manager ends itself (docs/assistant/agent-work.md "Safety boundary")
		}
	}
	switch w.On {
	case proto.WaitAgent:
		if w.Agent == nil {
			w.On = proto.WaitExternal // it named no agent we know: check back on a timer
		}
	case proto.WaitExternal, proto.WaitUser:
	default:
		w.On = proto.WaitUser
	}
	if (w.On == proto.WaitExternal || w.On == proto.WaitAgent) && w.Wait == "" {
		w.Wait = w.Line
	}
	if w.On == proto.WaitUser {
		w.Questions = cardQuestions(out.Questions, lang)
		if l := out.Link; l != nil && httpsLink(l.URL) {
			w.Link = &proto.WorkLink{URL: strings.TrimSpace(l.URL)}
			if l.Minutes > 0 && l.Minutes <= 24*60 {
				at := now.Add(time.Duration(l.Minutes) * time.Minute).UTC()
				w.Link.ExpiresAt = &at
			}
		}
	}
	if w.Line == "" {
		w.Line = i18n.T(lang, "assistant-agents_triage.waiting_for_you")
	}
	return w
}

// cardQuestions are the model's questions as a card can ask them: at most 4, each with 2–4 options.
func cardQuestions(qs []proto.Question, lang i18n.Lang) []proto.Question {
	var out []proto.Question
	seen := map[string]bool{}
	for _, q := range qs {
		text := strings.TrimSpace(q.Question)
		if text == "" || seen[text] || len(out) == 4 {
			continue
		}
		seen[text] = true
		c := proto.Question{Question: clip(text, 500), Header: clip(oneLine(q.Header), 12), MultiSelect: q.MultiSelect}
		labels := map[string]bool{}
		for _, o := range q.Options {
			l := clip(oneLine(o.Label), 60)
			if l == "" || labels[l] || len(c.Options) == 4 {
				continue
			}
			labels[l] = true
			option := proto.QuestionOption{Label: l, Description: clip(oneLine(o.Description), 120)}
			// Only triage single-choice cards can defer; never combine it with other answers.
			if !q.MultiSelect && o.Action == proto.QuestionOptionDefer {
				option.Action = proto.QuestionOptionDefer
			}
			c.Options = append(c.Options, option)
		}
		if len(c.Options) < 2 {
			c.Options = append(c.Options, proto.QuestionOption{Label: answerLabel(lang, answerSkip)})
		}
		out = append(out, c)
	}
	return out
}

// triageOthers are the agents working on machine besides sid (what it may wait for), by short id.
func triageOthers(machine, sid string, list []proto.Session) ([]prompts.AgentTriageOther, map[string]proto.WorkAgent) {
	var out []prompts.AgentTriageOther
	by := map[string]proto.WorkAgent{}
	for _, o := range list {
		if o.SessionID == sid || o.Status != proto.StatusWorking || o.Assistant || len(out) == 12 {
			continue
		}
		id := short(o.SessionID)
		line := o.Activity
		if line == "" {
			line = o.LastText
		}
		out = append(out, prompts.AgentTriageOther{ID: id, Label: agentLabel(o), Line: clip(oneLine(line), 80)})
		by[id] = proto.WorkAgent{MachineID: machine, SessionID: o.SessionID, Label: agentLabel(o)}
	}
	return out, by
}

// lastTurnOf is a session's latest turn: what the user asked in it ("" when none) and its last reply ("" when the
// turn cannot be read).
func (s *Service) lastTurnOf(ctx context.Context, machine, sid string) (ask, answer string) {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := s.call(cctx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "turns": 1}, &res); err != nil {
		return "", ""
	}
	for _, it := range res.Items {
		if it.Kind == proto.KindText && it.Role == "user" && strings.TrimSpace(it.Text) != "" {
			ask = strings.TrimSpace(it.Text)
			break
		}
	}
	for i := len(res.Items) - 1; i >= 0; i-- {
		if it := res.Items[i]; it.Kind == proto.KindText && it.Role == "assistant" && strings.TrimSpace(it.Text) != "" {
			return ask, strings.TrimSpace(it.Text)
		}
	}
	return ask, ""
}

// clipMiddleRunes keeps a long text's first and last n runes: a last answer says what was done first and what is
// left last.
func clipMiddleRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= 2*n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "\n…\n" + string(r[len(r)-n:])
}

func first[T any](s []T, n int) []T {
	if len(s) > n {
		return s[:n]
	}
	return s
}

type agentWake = prompts.AgentWake

func renderWake(w agentWake) string { return prompts.RenderAgentWake(w) }

func renderAnswer(items []agentAnswerItem) string {
	a := prompts.AgentAnswer{}
	for _, it := range items {
		a.Items = append(a.Items, prompts.AgentAnswerItem{Question: oneLine(it.question), Answer: it.answer})
	}
	return prompts.RenderAgentAnswer(a)
}

// renderDelegatedAnswer is renderAnswer for answers the assistant gave for the user, and why (delegate.go): the agent
// hears that they are the assistant's, and that the user may change them.
func renderDelegatedAnswer(items []agentAnswerItem, reason string) string {
	a := prompts.AgentAnswer{ByAssistant: true, Reason: oneLine(reason)}
	for _, it := range items {
		a.Items = append(a.Items, prompts.AgentAnswerItem{Question: oneLine(it.question), Answer: it.answer})
	}
	return prompts.RenderAgentAnswer(a)
}

func renderAuthRelayed(app string) string {
	return prompts.RenderAgentAuthRelayed(prompts.AgentAuthRelayed{App: app})
}
