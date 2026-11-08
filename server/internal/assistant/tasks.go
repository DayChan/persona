package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Tasks dispatched from an assistant conversation (docs/assistant/unified-entry.md, #930). The assistant hands the user a plan
// (task_plan); read-only plans, tasks the user asked for in so many words (#2559) and kinds the user trusts start at
// once, the rest wait for the user's "Start" on the plan card. A task is a todo (todos.go) whose origin is the conversation; its agents are ordinary agents
// (the Agents page shows them as always). What the assistant adds to the conversation — the plan card, a started
// task, an answered question, a receipt, a message sent straight to a task — is the conversation's feed, kept apart
// from Claude's transcript.

// PlanTask is one task of a plan, as the assistant proposed it and the user may have edited it.
type PlanTask struct {
	Title      string `json:"title"`
	Prompt     string `json:"prompt"`
	Machine    string `json:"machine"`
	Cwd        string `json:"cwd"`
	Launcher   string `json:"launcher,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
	Tier       string `json:"tier,omitempty"`
	Permission string `json:"permission,omitempty"`
	// Ultracode: the agent starts with Claude Code's ultracode on (#12). A pointer so that an edit from the card can
	// turn it off, and one from a client that does not know it keeps the assistant's choice.
	Ultracode *bool `json:"ultracode,omitempty"`
	Readonly  bool  `json:"readonly,omitempty"`
	// UserRequested: the user asked for this very task in the turn that planned it (#2559); with
	// assistant.start_requested_tasks on it starts without the card's "Start".
	UserRequested bool   `json:"user_requested,omitempty"`
	Why           string `json:"why,omitempty"`
	// DoneWhen: what done means, criteria the supervisor checks before it accepts the work; ExpectMinutes: how long it
	// should take (#3124, supervise.go). Both optional; the agent is told the criteria with its prompt.
	DoneWhen      []string `json:"done_when,omitempty"`
	ExpectMinutes int      `json:"expect_minutes,omitempty"`
	// ReuseTodo: the todo this task runs on instead of a new one (task_plan's todo_id, #5279): one this conversation
	// opened that has no agent yet, such as the todo a Lark topic opened (feishu_thread_open.go). Checked when the
	// plan is made and again when it starts; it is not what the apps send back with an edit.
	ReuseTodo string `json:"reuse_todo,omitempty"`
	// Set once started: the todo the task is, and why its agent did not start.
	TodoID string `json:"todo_id,omitempty"`
	Error  string `json:"error,omitempty"`
}

// TaskPlan is a plan the assistant handed the user.
type TaskPlan struct {
	ID           string     `json:"id"`
	Conversation string     `json:"conversation"`
	Tasks        []PlanTask `json:"tasks"`
	State        string     `json:"state"` // planOpen, planStarted, planDismissed
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const (
	planOpen      = "open"
	planStarted   = "started"
	planDismissed = "dismissed"
)

// FeedItem is one item the hub inserted into an assistant conversation; clients merge them with the transcript by At.
type FeedItem struct {
	ID           string          `json:"id"`
	Conversation string          `json:"conversation"`
	At           time.Time       `json:"at"`
	Kind         string          `json:"kind"` // feedPlan, feedTask, feedAnswered, feedReceipt, feedRouted
	Ref          string          `json:"ref,omitempty"`
	Body         json.RawMessage `json:"body,omitempty"`
}

const (
	feedPlan     = "plan"     // ref: the plan id
	feedTask     = "task"     // ref: the todo id
	feedAnswered = "answered" // ref: the todo id; body: answeredBody (answered.go)
	feedReceipt  = "receipt"  // ref: the todo id; body: title, state, summary, pr, machine
	feedRouted   = "routed"   // ref: the todo id; body: text, interrupted (agent_interrupt stopped its turn first)
	feedReport   = "report"   // ref: the todo id; body: reportBody (a supervised task's agent reported, agent_control.go)
	feedMessage  = "message"  // ref: the inbox item id; body: MessageCard (a message about a task, with its draft reply)
)

const trustSetting = "tasks.trust"

// settleDelay is how long a task's agents stay still before it counts as done: as long as the hub waits before its
// own finished push.
func (s *Service) settleDelay(ctx context.Context) time.Duration {
	return time.Duration(s.hello(ctx).DoneDelayMS) * time.Millisecond
}

type tasksState struct {
	mu sync.Mutex
	// completionMu serializes a resumed turn with committing an earlier judgment.
	completionMu sync.Mutex
	// Without a store (unit tests) plans and the feed live here.
	plans map[string]*TaskPlan
	feed  map[string][]FeedItem
	// working: when one of a task's agents last started a turn; receipted: when its last receipt went out. A
	// receipt is due when the task settles after working since the last one.
	working   map[string]time.Time
	receipted map[string]time.Time
	// awaitingAnswer remembers a stopped status whose transcript still ends in a tool.
	awaitingAnswer map[SessionRef]bool
	// retrying: the tasks whose agent is starting again after a failed start (one start at a time, #1396).
	retrying map[string]bool
	// outcomes: the completions whose outcome was recorded (todo_outcome.go, outcomeKey).
	outcomes map[string]time.Time
	// reusing: the todos a started plan took over (PlanTask.ReuseTodo) whose agent is not linked yet, so that a second
	// plan cannot take the same todo in that gap (takeTodoForTask).
	reusing map[string]bool
	// outcomeRuns are the outcome recordings still going (goRecordTaskOutcome, waitTaskOutcomes).
	outcomeRuns sync.WaitGroup
}

func newTasksState() tasksState {
	return tasksState{plans: map[string]*TaskPlan{}, feed: map[string][]FeedItem{},
		working: map[string]time.Time{}, receipted: map[string]time.Time{}, retrying: map[string]bool{},
		outcomes: map[string]time.Time{}, reusing: map[string]bool{}}
}

// ---- plans and the feed: storage ----

func (s *Service) savePlan(ctx context.Context, p *TaskPlan) error {
	st := s.store()
	if st == nil {
		s.tasks.mu.Lock()
		cp := *p
		cp.Tasks = append([]PlanTask(nil), p.Tasks...)
		s.tasks.plans[p.ID] = &cp
		s.tasks.mu.Unlock()
		return nil
	}
	raw, _ := json.Marshal(p.Tasks)
	return st.CreateTaskPlan(ctx, db.TaskPlanRow{ID: p.ID, Conversation: p.Conversation, Tasks: raw, State: p.State,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt})
}

var errPlanNotFound = errors.New("plan not found")

func (s *Service) loadPlan(ctx context.Context, id string) (*TaskPlan, error) {
	st := s.store()
	if st == nil {
		s.tasks.mu.Lock()
		defer s.tasks.mu.Unlock()
		p := s.tasks.plans[id]
		if p == nil {
			return nil, errPlanNotFound
		}
		cp := *p
		cp.Tasks = append([]PlanTask(nil), p.Tasks...)
		return &cp, nil
	}
	row, err := st.TaskPlan(ctx, id)
	if errors.Is(err, db.ErrTaskPlanNotFound) {
		return nil, errPlanNotFound
	}
	if err != nil {
		return nil, err
	}
	return planFromRow(*row), nil
}

func planFromRow(row db.TaskPlanRow) *TaskPlan {
	p := &TaskPlan{ID: row.ID, Conversation: row.Conversation, State: row.State, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	_ = json.Unmarshal(row.Tasks, &p.Tasks)
	return p
}

// movePlan moves p from state from to its State with its Tasks; false when someone else moved it first.
func (s *Service) movePlan(ctx context.Context, p *TaskPlan, from string) (bool, error) {
	p.UpdatedAt = time.Now().UTC()
	st := s.store()
	if st == nil {
		s.tasks.mu.Lock()
		defer s.tasks.mu.Unlock()
		cur := s.tasks.plans[p.ID]
		if cur == nil || cur.State != from {
			return false, nil
		}
		cp := *p
		cp.Tasks = append([]PlanTask(nil), p.Tasks...)
		s.tasks.plans[p.ID] = &cp
		return true, nil
	}
	raw, _ := json.Marshal(p.Tasks)
	return st.UpdateTaskPlan(ctx, p.ID, from, p.State, raw, p.UpdatedAt)
}

// appendFeed adds an item to a conversation's feed and tells the clients.
func (s *Service) appendFeed(ctx context.Context, conversation, kind, ref string, body any) (FeedItem, error) {
	return s.appendFeedAt(ctx, conversation, kind, ref, body, time.Now())
}

// appendFeedAt is appendFeed for an item that belongs at another time (the "Today" card: the start of the day).
func (s *Service) appendFeedAt(ctx context.Context, conversation, kind, ref string, body any, at time.Time) (FeedItem, error) {
	it := FeedItem{ID: "feed_" + randomHex(8), Conversation: conversation, At: at.UTC(), Kind: kind, Ref: ref}
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return FeedItem{}, err
		}
		it.Body = raw
	}
	if st := s.store(); st == nil {
		s.tasks.mu.Lock()
		s.tasks.feed[conversation] = append(s.tasks.feed[conversation], it)
		s.tasks.mu.Unlock()
	} else if err := st.AppendFeed(ctx, db.FeedRow{ID: it.ID, Conversation: conversation, At: it.At, Kind: kind, Ref: ref, Body: it.Body}); err != nil {
		return FeedItem{}, err
	}
	s.event(ctx, map[string]any{"type": "assistant_feed", "conversation": conversation, "item": it})
	return it, nil
}

const feedPage = 500

// feed is a conversation's items after since, oldest first (with the message cards sent after since: the client
// has them, not that they went out), and the plans they name.
func (s *Service) feed(ctx context.Context, conversation string, since time.Time) ([]FeedItem, map[string]*TaskPlan, error) {
	var items []FeedItem
	if st := s.store(); st == nil {
		s.tasks.mu.Lock()
		for _, it := range s.tasks.feed[conversation] {
			if at := cardSentAt(it); it.At.After(since) || it.Kind == feedMessage && at != nil && at.After(since) {
				items = append(items, it)
			}
		}
		s.tasks.mu.Unlock()
	} else {
		rows, err := st.Feed(ctx, conversation, since, feedPage)
		if err != nil {
			return nil, nil, err
		}
		for _, r := range rows {
			items = append(items, FeedItem{ID: r.ID, Conversation: r.Conversation, At: r.At, Kind: r.Kind, Ref: r.Ref, Body: r.Body})
		}
	}
	s.settleCards(ctx, items)
	s.fileCards(ctx, items)
	plans := map[string]*TaskPlan{}
	for _, it := range items {
		if it.Kind == feedPlan && plans[it.Ref] == nil {
			if p, err := s.loadPlan(ctx, it.Ref); err == nil {
				plans[it.Ref] = p
			}
		}
	}
	return items, plans, nil
}

// ---- the assistant's task_plan tool ----

// planTaskIn is a task as task_plan's arguments have it. Its todo_id is the todo to run on (PlanTask.ReuseTodo); the
// plan's own todo_id, the todo a started task became, is the hub's to set, and the shallower field wins in JSON.
type planTaskIn struct {
	PlanTask
	TodoID string `json:"todo_id"`
}

func (s *Service) toolTaskPlan(ctx context.Context, sessionID string, args json.RawMessage) (string, error) {
	var in struct {
		Tasks []planTaskIn `json:"tasks"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("tasks: %v", err)
	}
	var a struct{ Tasks []PlanTask }
	for _, t := range in.Tasks {
		t.PlanTask.ReuseTodo = strings.TrimSpace(t.TodoID)
		a.Tasks = append(a.Tasks, t.PlanTask)
	}
	if len(a.Tasks) == 0 {
		return "", fmt.Errorf("tasks is empty")
	}
	if len(a.Tasks) > 8 {
		return "", fmt.Errorf("at most 8 tasks in one plan")
	}
	reused := map[string]bool{}
	for i := range a.Tasks {
		t := &a.Tasks[i]
		t.TodoID, t.Error = "", ""
		t.Title, t.Prompt, t.Cwd = strings.TrimSpace(t.Title), strings.TrimSpace(t.Prompt), strings.TrimSpace(t.Cwd)
		t.DoneWhen, t.ExpectMinutes = cleanDoneWhen(t.DoneWhen), max(0, min(t.ExpectMinutes, 24*60))
		if t.Title == "" || t.Prompt == "" || t.Cwd == "" {
			return "", fmt.Errorf("task %d: title, prompt and cwd are required", i+1)
		}
		if !s.placeTask(ctx, t) {
			return "", fmt.Errorf("task %d: unknown machine %q (machines: %s)", i+1, t.Machine, s.machineIDs(ctx))
		}
		// Validate the tier now without pinning its model: it is resolved again at dispatch.
		preview := *t
		if err := s.applyTaskTier(ctx, &preview); err != nil {
			return "", fmt.Errorf("task %d: %w", i+1, err)
		}
		s.healthyPlace(ctx, &preview) // health follows the resolved launcher
		t.Machine, t.Cwd, t.Why = preview.Machine, preview.Cwd, preview.Why
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", fmt.Errorf("todos are unavailable: %v", err)
	}
	for i, t := range a.Tasks {
		if t.ReuseTodo == "" {
			continue
		}
		if reused[t.ReuseTodo] {
			return "", fmt.Errorf("task %d: todo_id %s is already the todo of another task in this plan", i+1, t.ReuseTodo)
		}
		reused[t.ReuseTodo] = true
		if err := s.reusableTodo(sessionID, t.ReuseTodo, false); err != nil {
			return "", fmt.Errorf("task %d: %v", i+1, err)
		}
	}
	dups := s.planDuplicates(ctx, a.Tasks) // before the plan's own todos exist
	now := time.Now().UTC()
	p := &TaskPlan{ID: "plan_" + randomHex(6), Conversation: sessionID, Tasks: a.Tasks, State: planOpen, CreatedAt: now, UpdatedAt: now}
	if err := s.savePlan(ctx, p); err != nil {
		return "", err
	}
	// The plan goes in the item's body too: a client that missed the task_plan event (or raced it) still draws the
	// card from the conversation itself.
	if _, err := s.appendFeed(ctx, sessionID, feedPlan, p.ID, map[string]any{"plan": p}); err != nil {
		s.logf("task plan %s: feed: %v", p.ID, err)
	}
	// A plan from the health space hands its prompts to agents outside it: it waits for the user's "Start" on its card,
	// whatever would start it at once, and its push names no task (health.go).
	health := s.inHealth(ctx, sessionID)
	if health || !s.planStartsAtOnce(ctx, p.Tasks) {
		s.event(ctx, map[string]any{"type": "task_plan", "plan": p})
		// A card below the answer is easy to miss: push it as well.
		titles := make([]string, 0, len(p.Tasks))
		for _, t := range p.Tasks {
			titles = append(titles, t.Title)
		}
		l := s.lang(ctx)
		title := i18n.T(l, "assistant-tasks.plan_waiting", i18n.Args{"n": len(p.Tasks)})
		if len(titles) == 1 {
			title = i18n.T(l, "assistant-tasks.plan_waiting_single")
		}
		here := s.settings(ctx).MachineID
		machineName := "Persona"
		if m, ok := s.device(ctx, here); ok {
			machineName = m.Name
		}
		body := strings.Join(titles, i18n.T(l, "assistant-tasks.list_sep"))
		if health {
			title, body = healthNotice(l)
		}
		s.push(ctx, Notification{Subtitle: machineName, DeviceID: here, SessionID: sessionID, Title: title, Body: body})
		return fmt.Sprintf("Plan %s is on the user's screen as a card and pushed to their phone; nothing has started. "+
			"Don't wait for it: carry on. When they start it, the tasks show up in the context snapshot.", p.ID) + dups, nil
	}
	started, err := s.startPlan(ctx, p.ID, nil, false)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Started %d task(s) without asking (read-only, asked for by the user, or a kind the user trusts):\n", len(started.Tasks))
	for _, t := range started.Tasks {
		fmt.Fprintf(&b, "- %s → todo %s on %s\n", t.Title, t.TodoID, t.Machine)
	}
	b.WriteString("Their progress shows in the context snapshot; the user sees them as cards.")
	if s.isMain(ctx, sessionID) {
		b.WriteString(" You supervise them: a 督办 message comes when one needs you (review it with task_review).")
	}
	b.WriteString(dups)
	return b.String(), nil
}

// placeTask names t's machine by its id and makes ~ in its directory that machine's home; false: no such machine.
func (s *Service) placeTask(ctx context.Context, t *PlanTask) bool {
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return false
	}
	t.Machine = m.ID
	t.Cwd = proto.ExpandHome(t.Cwd, m.Home)
	return true
}

// planStartsAtOnce: every task only reads, was asked for by the user in this turn (while start_requested_tasks is on),
// or is a kind the user said to start without asking.
func (s *Service) planStartsAtOnce(ctx context.Context, tasks []PlanTask) bool {
	trusted := s.trustedKinds(ctx)
	requested := s.startRequestedTasks(ctx)
	for _, t := range tasks {
		if !t.Readonly && !(requested && t.UserRequested) && !trusted[trustKey(t)] {
			return false
		}
	}
	return true
}

// trustKey names a kind of task the user can let start without asking: a machine's directory, reading or editing.
func trustKey(t PlanTask) string {
	kind := "edit"
	if t.Readonly {
		kind = "read"
	}
	return t.Machine + "|" + t.Cwd + "|" + kind
}

func (s *Service) trustedKinds(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	st := s.store()
	if st == nil {
		return out
	}
	var keys []string
	if ok, err := st.Setting(ctx, trustSetting, &keys); err == nil && ok {
		for _, k := range keys {
			out[k] = true
		}
	}
	return out
}

func (s *Service) trustKinds(ctx context.Context, tasks []PlanTask) {
	st := s.store()
	if st == nil {
		return
	}
	have := s.trustedKinds(ctx)
	for _, t := range tasks {
		have[trustKey(t)] = true
	}
	keys := make([]string, 0, len(have))
	for k := range have {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if err := st.PutSetting(ctx, trustSetting, keys); err != nil {
		s.logf("tasks: trust: %v", err)
	}
}

// ---- HTTP ----

func (s *Service) taskRoutes() {
	s.doneCheckRoute()
	s.handle("GET /api/v1/task-plans/{id}", func(w http.ResponseWriter, r *http.Request) {
		p, err := s.loadPlan(r.Context(), r.PathValue("id"))
		if err != nil {
			planErr(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"plan": p})
	})
	s.handle("POST /api/v1/task-plans/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot start tasks"))
			return
		}
		var in struct {
			Tasks    []PlanTask `json:"tasks"`
			Remember bool       `json:"remember"`
		}
		if r.ContentLength != 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
				apierr.Write(w, http.StatusBadRequest, err)
				return
			}
		}
		p, err := s.startPlan(r.Context(), r.PathValue("id"), in.Tasks, in.Remember)
		if err != nil {
			planErr(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"plan": p})
	})
	s.handle("POST /api/v1/task-plans/{id}/dismiss", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change tasks"))
			return
		}
		p, err := s.dismissPlan(r.Context(), r.PathValue("id"))
		if err != nil {
			planErr(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"plan": p})
	})
	s.handle("POST /api/v1/todos/{id}/retry", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot start tasks"))
			return
		}
		t, err := s.retryTask(r.Context(), r.PathValue("id"))
		switch {
		case err == nil:
			apierr.WriteJSON(w, map[string]any{"todo": t})
		case errors.Is(err, db.ErrTodoNotFound):
			apierr.Write(w, http.StatusNotFound, err)
		case errors.Is(err, errTaskNotRetryable), errors.Is(err, errTaskNotInPlan), errors.Is(err, errMachineOffline):
			apierr.Write(w, http.StatusConflict, err)
		default:
			planErr(w, err)
		}
	})
	s.handle("GET /api/v1/assistant/conversations/{id}/feed", func(w http.ResponseWriter, r *http.Request) {
		var since time.Time
		if s := r.URL.Query().Get("since"); s != "" {
			t, err := time.Parse(time.RFC3339Nano, s)
			if err != nil {
				apierr.Write(w, http.StatusBadRequest, fmt.Errorf("since: %v", err))
				return
			}
			since = t
		}
		items, plans, err := s.feed(r.Context(), r.PathValue("id"), since)
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		if items == nil {
			items = []FeedItem{}
		}
		views, err := s.conversationViews(r.Context(), r.PathValue("id"))
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		actions, err := s.conversationViewActions(r.Context(), r.PathValue("id"))
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		blocks, err := s.conversationViewBlockKeys(r.Context(), r.PathValue("id"))
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"items": items, "plans": plans, "views": views, "view_actions": actions, "view_blocks": blocks})
	})
	s.handle("POST /api/v1/assistant/conversations/{id}/route", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot send"))
			return
		}
		var in struct {
			TodoID string `json:"todo_id"`
			Text   string `json:"text"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(in.Text) == "" || in.TodoID == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("todo_id and text are required"))
			return
		}
		it, err := s.routeToTask(r.Context(), r.PathValue("id"), in.TodoID, in.Text)
		if err != nil {
			apierr.Write(w, http.StatusConflict, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"item": it})
	})
}

func planErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errPlanNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, errPlanNotOpen):
		apierr.Write(w, http.StatusConflict, err)
	default:
		apierr.Write(w, http.StatusServiceUnavailable, err)
	}
}
