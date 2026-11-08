package assistantwire

import "time"

// ---- the todos (the assistant's, docs/assistant/todos.md) ----

// TodoReport is what the hub shows of the todos: the todo each agent works for now (the open todos' active links: the
// session lists' todo, the task named on its agents' cards) and the agents kept quiet (#1731: no pushes, badges or Live
// Activity; those of quiet tasks open or done in the last week). Small, whole every time.
type TodoReport struct {
	Links []TodoLink   `json:"links"`
	Quiet []SessionRef `json:"quiet,omitempty"`
}

// TodoLink is one agent that works for an open todo.
type TodoLink struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	TodoID    string `json:"todo_id"`
	Title     string `json:"title"` // the todo's
	// Task: the todo is a task an assistant conversation opened (its agents' cards name it: proto.Interaction.TaskTitle).
	Task bool `json:"task,omitempty"`
}

// TodoHandoff is assistant.todo_handoff's params: an agent is handed work for a todo (POST /api/v1/sessions or
// …/messages with todo_id); Text is what it is handed ("" when only the todo is wanted).
type TodoHandoff struct {
	TodoID string `json:"todo_id"`
	Text   string `json:"text,omitempty"`
	// New: Text is a new agent's first prompt; the background of the project its directory (Cwd on MachineID) belongs
	// to goes before it too (#2820). An older assistant ignores these.
	New       bool   `json:"new,omitempty"`
	MachineID string `json:"machine_id,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
}

// TodoHandoffResult is assistant.todo_handoff's answer: the todo (Found false: no such todo) and Text with the todo's
// decisions in effect before it (as it was without any, or when they could not be read). Status and Error, when set,
// say the hand-off may not go (404: no such todo, 409: it is done); the hub answers the app with them as they are.
type TodoHandoffResult struct {
	Found  bool       `json:"found"`
	Title  string     `json:"title,omitempty"`
	DoneAt *time.Time `json:"done_at,omitempty"`
	Text   string     `json:"text,omitempty"`
	Status int        `json:"status,omitempty"`
	Error  string     `json:"error,omitempty"`
}

// TodoLinkParams is assistant.todo_link's params: the agent handed work for a todo works for it from now on (a link
// by hand; title, cwd and agent as the session has them). Prompt is what the agent was started with: without a
// title, the link's comes from it (its first line past a context block).
type TodoLinkParams struct {
	TodoID    string `json:"todo_id"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Prompt    string `json:"prompt,omitempty"`
}

// TodoLinkResult is assistant.todo_link's answer: the todo's title (the session the start answers names it).
type TodoLinkResult struct {
	Title string `json:"title,omitempty"`
}

// TodoSpaceRenamed is todos.space_renamed's params: the hub renamed a workspace (PATCH …/spaces/{wid}); a todo linked
// to it by its name follows.
type TodoSpaceRenamed struct {
	MachineID string `json:"machine_id"`
	Old       string `json:"old"`
	New       string `json:"new"`
}

const (
	// MethodTodoHandoff: TodoHandoff → TodoHandoffResult (the hub calls it before an agent is handed work for a todo).
	MethodTodoHandoff = "assistant.todo_handoff"
	// MethodTodoLink: TodoLinkParams → TodoLinkResult (after the hand-off went through); an error says why it was not linked
	// (db.ErrTodoNotFound / ErrTodoCompleted's words).
	MethodTodoLink = "assistant.todo_link"
	// EventTodoSpaceRenamed: TodoSpaceRenamed, one way.
	EventTodoSpaceRenamed = "todos.space_renamed"
)
