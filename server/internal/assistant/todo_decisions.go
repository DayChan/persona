package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A todo's decision record (#1618, docs/assistant/todos.md "Decision record"): the calls the user made on it, written by hand in the
// todo or from their answers to the question cards of the agents working on it (interaction.resolved), and handed to
// every agent the todo is given to (assistant.todo_handoff).

const decisionTextMax = 4000 // a question, a choice or a reason

var errNoDecisionStore = errors.New("decisions need the hub's database")

func (s *Service) todoDecisionRoutes() {
	// GET /api/v1/todos/{id}/decisions[?all=1]: the decisions in effect, oldest first; all=1 adds the superseded ones.
	s.handle("GET /api/v1/todos/{id}/decisions", func(w http.ResponseWriter, r *http.Request) {
		st, id, ok := s.decisionTodo(w, r)
		if !ok {
			return
		}
		all := r.URL.Query().Get("all")
		list, err := st.ListTodoDecisions(r.Context(), id, all == "1" || all == "true")
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"decisions": list})
	})

	// POST /api/v1/todos/{id}/decisions: write one {question, choice, reason?, supersedes_id?, source?, ref?}. A device
	// writes one by hand (source manual, the default) or an outcome; an extension (todos.write) writes only outcomes,
	// such as a scheduler's acceptance evidence, never the user's decisions and never superseding one. ref (an
	// extension's, at most decisionRefMax runes, no '#') makes the write idempotent: the same ref and question is
	// written once, and a replay answers inserted false.
	s.handle("POST /api/v1/todos/{id}/decisions", func(w http.ResponseWriter, r *http.Request) {
		if !todoWriter(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		st, id, ok := s.decisionTodo(w, r)
		if !ok {
			return
		}
		var in struct {
			Question     string `json:"question"`
			Choice       string `json:"choice"`
			Reason       string `json:"reason"`
			SupersedesID string `json:"supersedes_id"`
			Source       string `json:"source"`
			Ref          string `json:"ref"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		row := db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: id, Question: decisionText(in.Question),
			Choice: decisionText(in.Choice), Reason: decisionText(in.Reason), Source: db.DecisionManual,
			SupersedesID: strings.TrimSpace(in.SupersedesID), CreatedAt: decisionNow()}
		if err := decisionOrigin(r, &row, strings.TrimSpace(in.Source), strings.TrimSpace(in.Ref)); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if row.Question == "" || row.Choice == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("question and choice cannot be empty"))
			return
		}
		inserted, err := s.insertDecision(r.Context(), st, row)
		if err != nil {
			decisionErr(w, err)
			return
		}
		if inserted {
			s.decisionsChanged(r.Context(), id, row.CreatedAt)
		}
		apierr.WriteJSON(w, map[string]any{"decision": row, "inserted": inserted})
	})

	// PATCH /api/v1/todos/{id}/decisions/{did}: {supersedes_id}: mark it as replacing an earlier decision ("" undoes it).
	s.handle("PATCH /api/v1/todos/{id}/decisions/{did}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot modify todos"))
			return
		}
		st, id, ok := s.decisionTodo(w, r)
		if !ok {
			return
		}
		var in struct {
			SupersedesID *string `json:"supersedes_id"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if in.SupersedesID == nil {
			apierr.Write(w, http.StatusBadRequest, errors.New("supersedes_id is required"))
			return
		}
		at := decisionNow()
		row, err := st.SetTodoDecisionSupersedes(r.Context(), id, r.PathValue("did"), strings.TrimSpace(*in.SupersedesID), at)
		if err != nil {
			decisionErr(w, err)
			return
		}
		s.decisionsChanged(r.Context(), id, at)
		apierr.WriteJSON(w, map[string]any{"decision": row})
	})
}

// decisionTodo is the store and the request's todo id once the todos are loaded and it is one of them; otherwise it
// answers.
func (s *Service) decisionTodo(w http.ResponseWriter, r *http.Request) (todoStore, string, bool) {
	st := s.todoDB()
	if st == nil {
		apierr.Write(w, http.StatusServiceUnavailable, errNoDecisionStore)
		return nil, "", false
	}
	if err := s.ensureTodosLoaded(r.Context()); err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
		return nil, "", false
	}
	id := r.PathValue("id")
	if _, ok := s.todoNow(id); !ok {
		apierr.Write(w, http.StatusNotFound, errors.New("todo not found"))
		return nil, "", false
	}
	return st, id, true
}

func decisionErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrTodoNotFound), errors.Is(err, db.ErrDecisionNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, db.ErrDecisionSupersede):
		apierr.Write(w, http.StatusBadRequest, err)
	default:
		apierr.Write(w, http.StatusInternalServerError, err)
	}
}

// decisionRefMax bounds an extension's ref.
const decisionRefMax = 200

// decisionOrigin sets row's source and provenance from who asks: a device's source is manual (the default) or outcome;
// an extension's is outcome only, its machine "extension:<id>", and with a ref its message "ext:<id>:<ref>" (the key
// that keeps a replay from writing twice). An extension may not supersede.
func decisionOrigin(r *http.Request, row *db.TodoDecisionRow, source, ref string) error {
	ext := ""
	if r.Header.Get(RoleHeader) == roleExtension {
		ext = r.Header.Get(ExtensionHeader)
		if ext == "" {
			return errors.New("extension unknown")
		}
	}
	switch {
	case source == "" && ext != "", source == db.DecisionOutcome:
		row.Source = db.DecisionOutcome
	case source == "" || source == db.DecisionManual:
		if ext != "" {
			return errors.New("an extension writes outcomes only")
		}
	default:
		return errors.New("source must be manual or outcome")
	}
	if ext == "" {
		if ref != "" {
			return errors.New("ref is an extension's")
		}
		return nil
	}
	if row.SupersedesID != "" {
		return errors.New("an extension cannot supersede a decision")
	}
	if strings.Contains(ref, "#") || utf8.RuneCountInString(ref) > decisionRefMax {
		return fmt.Errorf("ref must be at most %d characters without '#'", decisionRefMax)
	}
	row.SourceMachine = "extension:" + ext
	if ref != "" {
		row.SourceMessage = "ext:" + ext + ":" + ref
	}
	return nil
}

// decisionNow is now as Postgres keeps it (microseconds), so the order checks compare what was stored.
func decisionNow() time.Time { return time.Now().UTC().Truncate(time.Microsecond) }

// decisionText is a decision's text as kept: trimmed, at most decisionTextMax runes.
func decisionText(s string) string { return clip(strings.TrimSpace(s), decisionTextMax) }

// cardDecisions: the user answered a question card (interaction.resolved); when its agent works on a todo, each
// answered question is a decision of that todo. Only Claude Code's option questions (AskUserQuestion) count: approvals
// and permissions are not decisions about the work, and a dismissed or failed card answered nothing.
func (s *Service) cardDecisions(ctx context.Context, ix proto.Interaction, r proto.Resolution, status string) {
	st := s.todoDB()
	if st == nil || status != "resolved" || r.Dismiss || ix.Kind != proto.IxQuestion || len(r.Answers) == 0 {
		return
	}
	if r.AnsweredBy == proto.AnsweredByAssistant {
		return // the assistant's call for the user (delegate.go), not one the user made; "Change answer" is
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.ensureTodosLoaded(ctx); err != nil {
		s.logf("decisions: card %s: todos not loaded: %v", ix.ID, err)
		return
	}
	s.todos.mu.RLock()
	todoID := s.todos.activeBySession[[2]string{ix.MachineID, ix.SessionID}]
	s.todos.mu.RUnlock()
	if todoID == "" {
		return
	}
	at := decisionNow()
	wrote, last := false, at
	for i, a := range proto.QuestionAnswers(ix, r) {
		q, c := decisionText(a.Question), decisionText(a.Answer)
		if q == "" || c == "" {
			continue
		}
		// A microsecond apart, so they list in the card's order (the list orders by time, then by the random id).
		at := at.Add(time.Duration(i) * time.Microsecond)
		ok, err := s.insertDecision(ctx, st, db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: todoID,
			Question: q, Choice: c, Source: db.DecisionCard, SourceMachine: ix.MachineID, SourceSession: ix.SessionID,
			SourceMessage: ix.ID, CreatedAt: at})
		if err != nil {
			s.logf("decisions: todo %s, card %s: %v", todoID, ix.ID, err)
			return
		}
		if ok {
			wrote, last = true, at
		}
	}
	if wrote {
		s.decisionsChanged(ctx, todoID, last)
	}
}

// agentDecisionTool runs todo_decisions / todo_decision_add for agent machine/session (#5279): the decisions of the
// open todo it works on, read or noted by it. The todo is the agent's own (activeBySession), never one it names:
// an agent cannot read or write another todo's decisions.
func (s *Service) agentDecisionTool(ctx context.Context, machine, session, name string, args json.RawMessage, now time.Time) (string, error) {
	if session == "" { // an older node names no session for these tools
		return "", errors.New("Persona could not tell which agent session is calling (the node on this machine may be older than the hub)")
	}
	st := s.todoDB()
	if st == nil {
		return "", errNoDecisionStore
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", errTodosUnavailable
	}
	t, ok := s.openTodoOfAgent(machine, session)
	if !ok {
		return "You are not working on an open todo: there are no decisions to read or note.", nil
	}
	if name == "todo_decisions" {
		list, err := st.ListTodoDecisions(ctx, t.ID, false)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return fmt.Sprintf("Todo %s (%q) has no decisions yet.", t.ID, t.Title), nil
		}
		b, _ := json.Marshal(map[string]any{"todo": map[string]string{"id": t.ID, "title": t.Title}, "decisions": agentDecisionViews(list)})
		return string(b), nil
	}
	var a struct {
		Question     string `json:"question"`
		Choice       string `json:"choice"`
		Reason       string `json:"reason"`
		SupersedesID string `json:"supersedes_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	row := db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: t.ID, Question: decisionText(a.Question),
		Choice: decisionText(a.Choice), Reason: decisionText(a.Reason), Source: db.DecisionAgent, SourceMachine: machine,
		SourceSession: session, SupersedesID: strings.TrimSpace(a.SupersedesID), CreatedAt: now.UTC().Truncate(time.Microsecond)}
	if row.Question == "" || row.Choice == "" {
		return "", errors.New("question and choice cannot be empty")
	}
	if row.SupersedesID != "" {
		// The user's calls are theirs to change: an agent replacing one would drop it from what every later agent reads.
		all, err := st.ListTodoDecisions(ctx, t.ID, true)
		if err != nil {
			return "", err
		}
		i := slices.IndexFunc(all, func(d db.TodoDecisionRow) bool { return d.ID == row.SupersedesID })
		if i < 0 {
			return "", errors.New("supersedes_id must be an earlier decision of this todo (see todo_decisions)")
		}
		if src := all[i].Source; src != db.DecisionAgent && src != db.DecisionOutcome {
			return "", errors.New("that decision is the user's: ask them instead of replacing it")
		}
	}
	if !s.noteRate.allow(machine, session, now) {
		return "", fmt.Errorf("too many decisions: at most %d in %d minutes; note only the calls that change the work",
			reportLimit, int(reportWindow.Minutes()))
	}
	if _, err := s.insertDecision(ctx, st, row); err != nil {
		if errors.Is(err, db.ErrDecisionSupersede) {
			return "", errors.New("supersedes_id must be an earlier decision of this todo (see todo_decisions)")
		}
		return "", err
	}
	s.decisionsChanged(ctx, t.ID, row.CreatedAt)
	s.logf("todo %s: %s noted decision %s", t.ID, short(session), row.ID)
	return fmt.Sprintf("Noted on todo %s as decision %s (yours, not the user's: it shows as noted by an agent).", t.ID, row.ID), nil
}

// agentDecisionView is a decision as todo_decisions gives it to an agent: who made the call, in words.
type agentDecisionView struct {
	ID         string `json:"id"`
	Question   string `json:"question"`
	Choice     string `json:"choice"`
	Reason     string `json:"reason,omitempty"`
	By         string `json:"by"` // user / agent
	Supersedes string `json:"supersedes,omitempty"`
	At         string `json:"at"`
}

func agentDecisionViews(list []db.TodoDecisionRow) []agentDecisionView {
	out := make([]agentDecisionView, 0, len(list))
	for _, r := range list {
		by := "user"
		if r.Source == db.DecisionAgent || r.Source == db.DecisionOutcome {
			by = "agent"
		}
		out = append(out, agentDecisionView{ID: r.ID, Question: r.Question, Choice: r.Choice, Reason: r.Reason, By: by,
			Supersedes: r.SupersedesID, At: r.CreatedAt.UTC().Format(time.RFC3339)})
	}
	return out
}

// decisionsChanged: the store bumped the todo's updated_at with its decisions; the kept todo follows, and the apps
// refetch (they reload a todo's decisions when its updated_at moves).
func (s *Service) decisionsChanged(ctx context.Context, todoID string, at time.Time) {
	s.todos.mu.Lock()
	if t, ok := s.todos.todos[todoID]; ok && at.After(t.UpdatedAt) {
		t.UpdatedAt = at
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
}

// todoDecisionsContext is the context block of a todo's decisions in effect that goes before what is handed to an
// agent for it; empty when it has none or they cannot be read (the handoff goes on without them).
func (s *Service) todoDecisionsContext(ctx context.Context, todoID string) string {
	st := s.todoDB()
	if st == nil || todoID == "" {
		return ""
	}
	list, err := st.ListTodoDecisions(ctx, todoID, false)
	if err != nil {
		s.logf("decisions: todo %s: %v", todoID, err)
		return ""
	}
	if len(list) == 0 {
		return ""
	}
	var title string
	agents := map[[2]string]string{}
	if t, ok := s.todoNow(todoID); ok {
		title = t.Title
		for _, a := range t.Agents {
			agents[[2]string{a.MachineID, a.SessionID}] = a.Title
		}
	}
	loc := s.location(ctx)
	d := prompts.TodoDecisions{Title: contextText(title)}
	for i, r := range list {
		from := ""
		if r.Source == db.DecisionOutcome {
			from = "Agent 做完时记的，不是用户定的" // todo_outcome.go: pulled out of a reply, the user did not make it
		} else if r.Source == db.DecisionAgent {
			from = "Agent 干活时记的，不是用户定的" // i18n-ignore: a line of the prompt the model reads (prompts/todo/decisions.md); todo_decision_add, the agent's own call
		} else if r.Source == db.DecisionThread {
			from = "用户在飞书话题里回的" // i18n-ignore: a line of the prompt the model reads
		} else if r.Source == db.DecisionCard {
			from = "在 Agent 的提问里答的"
			if t := strings.TrimSpace(agents[[2]string{r.SourceMachine, r.SourceSession}]); t != "" {
				from = "在「" + contextText(oneLine(t)) + "」的提问里答的"
			}
		}
		d.Decisions = append(d.Decisions, prompts.TodoDecision{N: i + 1, Question: contextText(r.Question),
			Choice: contextText(r.Choice), Reason: contextText(r.Reason), When: r.CreatedAt.In(loc).Format("01-02 15:04"), From: from})
	}
	return prompts.RenderTodoDecisions(d)
}

// contextText keeps a text from closing the context block it is put in (prompts.ContextBody), and indents its later lines under the item, so a text cannot pass for a
// decision of its own (a "\n2. Chose: …" line).
func contextText(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(prompts.ContextBody(s)), "\n", "\n      ")
}

// withTodoDecisions puts the todo's decisions before text handed to an agent for it; text as it is without any.
func (s *Service) withTodoDecisions(ctx context.Context, todoID, text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	if c := s.todoDecisionsContext(ctx, todoID); c != "" {
		return c + "\n\n" + text
	}
	return text
}
