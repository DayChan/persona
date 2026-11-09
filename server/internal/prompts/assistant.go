package prompts

import "time"

// AssistantMachine is one computer in the assistant's CLAUDE.md, as seen from where the assistant runs.
type AssistantMachine struct {
	Name, ID string
	Local    bool   // the machine the assistant runs on
	SSHHost  string // "" uses ID
	// AgentsOnly: no command reaches it from where the assistant runs (the ssh aliases are the hub machine's, and a
	// link or tailcat machine has none), only the Agents tools do.
	AgentsOnly bool
}

// AssistantClaudeMD is the assistant directory's CLAUDE.md (assistant/claude_md).
type AssistantClaudeMD struct {
	Owner        string // "" is "user"
	Machines     []AssistantMachine
	Notes        string // Settings › Assistant › About me, trimmed
	ExtraDirs    []string
	StateDir     string
	KnowledgeDir string
	OnHub        bool
	// LarkCLI: hub.json configures lark-cli (docs/plans/plan-lark-cli.md), on the assistant's PATH.
	LarkCLI bool
	// PersonaView: every app draws ```persona-view (hub.json assistant.persona_view); else the assistant writes
	// ```persona-card, which they all draw.
	PersonaView bool
	// Language is the language the user reads, by its own name ("English", or Japanese written in Japanese); "" is Chinese, as before i18n.
	Language string
}

func RenderAssistantClaudeMD(d AssistantClaudeMD) string { return render("assistant/claude_md", d) }

// AssistantHealthClaudeMD is the health space's CLAUDE.md (docs/assistant/health.md).
type AssistantHealthClaudeMD struct {
	Owner    string // "" is "user"
	Notes    string // Settings › Assistant › About me, trimmed
	StateDir string
	// HealthDir is the health space's directory name in StateDir: "" is the built-in manager's "health", another's
	// "health-<id>" (identity.HealthDirName).
	HealthDir string
	Language  string
}

func RenderAssistantHealthClaudeMD(d AssistantHealthClaudeMD) string {
	return render("assistant/health_claude_md", d)
}

// AssistantGuestClaudeMD is a guest conversation's CLAUDE.md (docs/assistant/assistant.md ("Guest conversations")).
type AssistantGuestClaudeMD struct {
	Owner    string // An empty name uses the generic user label.
	Language string
}

func RenderAssistantGuestClaudeMD(d AssistantGuestClaudeMD) string {
	return render("assistant/guest_claude_md", d)
}

type AssistantViewRepair struct {
	Errors []string
}

func RenderAssistantViewRepair(d AssistantViewRepair) string {
	return render("assistant/view_repair", d)
}

// SnapshotMachine, SnapshotAgent… are the parts of the live snapshot (assistant/snapshot).
type SnapshotMachine struct {
	Name, ID string
	Online   bool
}

type SnapshotAgent struct {
	Label, Machine, Session string             // Session: the short id
	Line                    string             // what it waits for, or what it concluded ("" when not known)
	Link                    string             // its page in the hub's web app ("" when the hub has no public_url)
	Ultracode               string             // "on" / "off"; empty when the node does not know
	Plan                    *SnapshotAgentPlan // its own task list (Session.Plan), nil when it keeps none
}

// SnapshotAgentPlan is an agent's own task list (TodoWrite / TaskCreate, docs/integrations/agent-goal.md "Plan"): Done of Total
// items completed, and the one in progress ("" when none is).
type SnapshotAgentPlan struct {
	Done, Total int
	Current     string
}

type SnapshotChat struct {
	Name    string
	Unread  int
	Preview string // @me chats: the clipped preview
}

type SnapshotChats struct {
	AtMe, Other []SnapshotChat
}

func (c SnapshotChats) Total() int { return len(c.AtMe) + len(c.Other) }

type SnapshotEvent struct {
	Start, End time.Time // in the user's zone
	Title      string
}

type SnapshotCalendar struct {
	Events []SnapshotEvent
}

type SnapshotFollowup struct {
	At    time.Time // in the user's zone
	Label string
	Every string // "30 minutes" for a repeating one
}

// SnapshotPlan is a plan card in the main conversation still waiting for the user's "Start" (#4453): nothing of it has
// started, and it shows nowhere else.
type SnapshotPlan struct {
	At     time.Time // when it was put up, in the user's zone
	Titles []string  // its tasks' titles (at most three)
	Tasks  int       // how many tasks it has
}

// SnapshotAsked is an ask_user question that became a to-do (#1617): what was asked, the to-do, when it was asked (in the
// user's zone).
type SnapshotAsked struct {
	TodoID   string
	Question string
	At       time.Time
}

// SnapshotRoutine is a daily scheduled task (morning brief, evening brief…): its rule, and how its last run went (LastAt zero: it never
// ran). LastStatus is the run's status (ok / failed / skipped), LastWhy why it failed or was skipped, in short.
type SnapshotRoutine struct {
	Label, Rule         string
	LastAt              time.Time // in the user's zone
	LastStatus, LastWhy string
}

// SnapshotMiss is a scheduled run that failed or was skipped since yesterday evening, with why, in short (#4124): the
// The morning brief says "last night's check failed" without calling list_followups.
type SnapshotMiss struct {
	At         time.Time // in the user's zone
	Label, Why string
	Skipped    bool
}

// SnapshotTask is a task opened from an assistant conversation: a todo and its agents (docs/assistant/unified-entry.md).
type SnapshotTask struct {
	ID, Title, State string
	Machine, Session string // its latest agent, when it has one
	Last             string // what that agent said last, in a line
	// Doing: the step that agent is on (its latest tool call, transcript.Activity) while it works or has said nothing
	// yet, and DoingFor how long that step has gone on ("3 minutes"), "" when unknown or not working.
	Doing, DoingFor string
	Subagents       []string // that agent's running subagents, what each is for: on top of its own step, never instead
	// Workflows: that agent's background workflow runs still running (#2935): its turn ended to wait for them.
	Workflows []SnapshotWorkflow
	Link      string // the todo's page in the hub's web app ("" when the hub has no public_url)
	// Health: how a task the main conversation supervises stands (normal, stuck N minutes, waiting for an answer, to accept, rework round N, your call,
	// accepted; #3124), "" for any other; DoneWhen how many criteria it has.
	Health   string
	DoneWhen int
	// Delivery: how far its finished work was delivered when that is short of live ("stopped at the PR, stuck on 'CI queued'"), ""
	// otherwise (docs/assistant/todos.md "Delivery").
	Delivery string
	// Reported: what its agent last reported to the supervisor (report_to_supervisor), nil when it has not.
	Reported *SnapshotReport
	// Plan: its agent's own task list, nil when it keeps none or Reported is newer (a report says more).
	Plan *SnapshotAgentPlan
}

// SnapshotReport is a supervised task's agent's latest report: its kind (progress, question, blocked, done), the step
// of how many (0: not said), what it said in a line and how many minutes ago.
type SnapshotReport struct {
	Kind        string
	Step, Steps int
	Text        string
	Minutes     int
}

// SnapshotWorkflow is a Claude Code workflow run still running in an agent's background (#2935): the agent's turn
// ended to wait for its result, which wakes it.
type SnapshotWorkflow struct {
	Agent, Machine, Session string // whose run: its label, machine and short session id (left empty inside a task)
	Name, Phase             string
	PhaseIndex, Phases      int    // Phase's 1-based place among the script's phases; 0 when not known
	Done, Started           int    // its agents that returned, and started so far
	For                     string // how long it has run ("12 minutes"), "" under a minute
}

// Snapshot is the live context a starting assistant gets in its system prompt.
type Snapshot struct {
	ModelProfile  string    // always refreshed outside the cached sources
	Now           time.Time // in the user's zone
	Zone          string
	Machines      []SnapshotMachine
	Needs, Unread []SnapshotAgent
	// Waiting: stopped on something the hub waits out for them (a retry, a deploy, another agent); Done: finished
	// within a day, with the conclusion (docs/assistant/agent-work.md "Persona knows").
	Waiting, Done []SnapshotAgent
	// Awaiting: stopped on a question to the user, who looked at it and has not answered (docs/protocol/agent-states.md
	// "Awaiting your reply", #5096).
	Awaiting []SnapshotAgent
	// Muted: agents (or their spaces) the user turned notifications off for that would be in one of the above; in
	// none of them, as the Live Activity and the Agents badge count none of them (#4562).
	Muted         []SnapshotAgent
	Working       []SnapshotAgent
	Workflows     []SnapshotWorkflow // background workflow runs still running, newest first
	PhoneOnline   bool
	KB            string            // RenderKBDigest, "" leaves it out
	Chats         *SnapshotChats    // nil: Feishu did not answer in time
	Calendar      *SnapshotCalendar // nil: the calendar did not answer in time
	Followups     []SnapshotFollowup
	Routines      []SnapshotRoutine // the daily tasks that are on (at most eight)
	RoutinesTotal int               // all of them
	Missed        []SnapshotMiss    // scheduled runs that failed or were skipped since yesterday 18:00, newest first
	MissedTotal   int               // all of them (Missed is the newest six)
	Tasks         []SnapshotTask    // tasks opened from assistant conversations (task_plan), newest first
	Plans         []SnapshotPlan    // plan cards in the main conversation waiting for "Start", newest first (at most five)
	PlansTotal    int
	// Asked: the assistant's ask_user questions nobody answered in time, now to-dos waiting for the user's call (#1617),
	// oldest first.
	Asked []SnapshotAsked
	// Unaccepted: deployed tasks the user has not passed yet (their "Please verify" ask, with a Lark card or without) (#2768), and
	// supervised tasks' conclusions the user has not answered (#1226), oldest first; the morning briefing brings them up again.
	Unaccepted      []SnapshotAccept
	UnacceptedTotal int // all of them (Unaccepted is the oldest eight)
	// Approvals: open tasks whose finished work waits for the user's nod to go live (merge, deploy, turn on, send),
	// oldest first: told to the user as one list to approve at once (docs/assistant/todos.md "Delivery").
	Approvals []SnapshotApproval
}

// SnapshotApproval is a task held short of live until the user approves: what it is, how far it got, where it is held.
type SnapshotApproval struct {
	ID, Title, Stage, Gate, Link string
}

// SnapshotAccept is a deployed task waiting for the user to check it, or a conclusion waiting for the user: its verdict
// line as its card says it, what changed (deployed), where to look, since when.
type SnapshotAccept struct {
	Title, Verdict, Changed, Link string
	Since                         time.Time // in the user's zone
}

func (s Snapshot) Up() []SnapshotMachine   { return s.machines(true) }
func (s Snapshot) Down() []SnapshotMachine { return s.machines(false) }

func (s Snapshot) machines(online bool) []SnapshotMachine {
	var out []SnapshotMachine
	for _, m := range s.Machines {
		if m.Online == online {
			out = append(out, m)
		}
	}
	return out
}

// Upcoming is the calendar without the meetings over by Now (the snapshot's sources are read less often than it is
// rendered).
func (s Snapshot) Upcoming() *SnapshotCalendar {
	if s.Calendar == nil {
		return nil
	}
	out := SnapshotCalendar{}
	for _, e := range s.Calendar.Events {
		if e.End.After(s.Now) {
			out.Events = append(out.Events, e)
		}
	}
	return &out
}

func RenderSnapshot(s Snapshot) string { return render("assistant/snapshot", s) }

// RenderSnapshotWorkflow is one running workflow, in a line ("'parity-audit' stage Verify (2/2), 169/185 agents done, running 14 minutes").
func RenderSnapshotWorkflow(w SnapshotWorkflow) string {
	return render("assistant/snapshot_workflow", w)
}

// AssistantContext is the hub's answer to assistant.context: the snapshot, for the main conversation with a line
// saying so first (assistant/context). Node puts what changed of it at the start of each message (TurnContext).
type AssistantContext struct {
	Main     bool
	Owner    string
	Snapshot string
}

func RenderAssistantContext(c AssistantContext) string { return render("assistant/context", c) }

// TaskRun is the message a scheduled task's run starts with (assistant/task_run).
type TaskRun struct {
	Label          string
	Once, Interval bool   // neither: daily
	Every          string // interval: "30 minutes"
	Late           string // "" when on time; else how late ("25 minutes")
	Due            time.Time
	Prompt         string
	KBHint         bool // daily runs with the knowledge base on
	// KnowledgeDir: the knowledge base directory the hint names; "" is the default ~/.persona/data.
	KnowledgeDir string
	// Continued: it goes into the task's own conversation, which got the background on the first run.
	Continued bool
	// PushAsked: the task pushes only what the run sends with notify_user (its push "asked", #2396).
	PushAsked bool
	// Facts: the run's facts Persona worked out (RenderBriefFacts), above the task's prompt; "" for a task without them.
	Facts string
}

func RenderTaskRun(t TaskRun) string { return render("assistant/task_run", t) }

// BriefFacts are the facts of a morning or evening routine's run, as the model is given them (assistant/routine_facts):
// the lines are ready to read; the totals count more than the lines when the lists were cut.
type BriefFacts struct {
	Hours                                                            int // how far back "done" and "not through" look
	WaitingTotal, DoneTotal, FailedTotal, AcceptTotal, ProgressTotal int
	WaitingUnknown                                                   bool // the cards waiting were not read yet: say nothing of whether any waits
	Card                                                             bool // a Lark card lists them for the user: the model need not
	LaterTotal                                                       int  // questions Agents left for a briefing: the model says how many, never lists them
	Waiting                                                          []string
	Done                                                             []string
	Failed                                                           []BriefFailed
	Accept                                                           []BriefAccept
	Progress                                                         []BriefProgress
}

// BriefFailed is one thing that did not go through: a task whose agent did not start, a scheduled run that failed
// (Run) or was skipped (Skipped).
type BriefFailed struct {
	Title, Why   string
	Run, Skipped bool
}

// BriefAccept is a task waiting to be verified, with what it changed in the words of its "Please verify" card.
type BriefAccept struct{ Line, Changed string }

// BriefProgress is a task being worked on and its stage ("plan", "develop", "review", "merge", "deploy", "verify"; "" not known).
type BriefProgress struct{ Title, Stage string }

func RenderBriefFacts(b BriefFacts) string { return render("assistant/routine_facts", b) }

// RenderMorningRoutine and RenderEveningRoutine are the default daily tasks' prompts.
func RenderMorningRoutine() string { return render("assistant/routine_morning", nil) }
func RenderEveningRoutine() string { return render("assistant/routine_evening", nil) }

// FormerMorningRoutines and FormerEveningRoutines are the earlier defaults of those prompts, oldest first: a hub that
// saved one unchanged gets the current default (assistant: upgradeDefaultRoutines). Never edited: each is what a hub
// may hold byte for byte.
func FormerMorningRoutines() []string {
	return []string{render("assistant/routine_morning_former1", nil), render("assistant/routine_morning_former2", nil), render("assistant/routine_morning_former3", nil)}
}
func FormerEveningRoutines() []string {
	return []string{render("assistant/routine_evening_former1", nil), render("assistant/routine_evening_former2", nil)}
}

// RenderLostTasks tells an assistant conversation that a restart stopped its background tasks (assistant/lost_tasks).
func RenderLostTasks(tasks []string) string {
	return render("assistant/lost_tasks", struct{ Tasks []string }{tasks})
}

// RenderUsageRetry sends a conversation's message to the model again after a gateway's impossible usage made Claude
// Code refuse it as too long and Persona repaired the transcript (assistant/usage_retry, node/assistant_usage.go). It
// is a <persona-context> block only: the apps show no bubble for it.
func RenderUsageRetry() string { return render("assistant/usage_retry", nil) }

// TaskNotice tells the main conversation (or a reporting one) about one of its quiet tasks (#1731): it was done, waits for the user, was
// cut off by the model service or did not start. The assistant decides what to tell the user.
type TaskNotice struct {
	Title, Machine string
	State          string   // done | needs | interrupted | failed
	Earlier        []string // what else it went through since the last notice ("waiting on the user", "done"), oldest first
	Ask            string   // needs: what it asks
	Text           string   // what its agent said last, or why it did not start
	// Reporting: the notice goes into the conversation that opened the task (one started with report_tasks, such as
	// a Feishu /agent's, #3192), not the main conversation.
	Reporting bool
}

func RenderTaskNotice(n TaskNotice) string { return render("assistant/task_notice", n) }

// AskTodoAnswer is the message to the conversation whose ask_user question became a to-do (#1617): the user's call on it
// (Thread: said in the to-do's Lark topic), or that the to-do was closed without one (Dropped). The questions and choices
// are escaped by the caller.
type AskTodoAnswer struct {
	When      string // when it was asked, MM-DD HH:MM in the user's zone
	TodoID    string
	Questions []string
	Choices   []AskTodoChoice
	Thread    bool
	Dropped   bool
	Several   bool // more than one question was asked
}

// AskTodoChoice is one call of the user's: the question as they wrote it ("" for a reply in the topic), the choice, why.
type AskTodoChoice struct {
	Question string
	Choice   string
	Reason   string
}

func RenderAskTodoAnswer(a AskTodoAnswer) string { return render("assistant/ask_answer", a) }

// Supervise is a supervise message: the tasks the main conversation handed out that need it now (#3124), what waits on a
// decision first.
type Supervise struct {
	Level   string
	Entries []SuperviseEntry
}

// SuperviseEntry is one task in it, with what happened (Kind: review, idle, needs_you, stalled, interrupted, failed).
type SuperviseEntry struct {
	ID, Title, Kind string
	Detail          string   // why: the judge's reason, the step it is stuck on, the model service's error, why it did not start
	Minutes         int      // how long it has waited (needs_you) or not moved (stalled)
	Ask             string   // needs_you: what it asks
	DoneWhen        []string // what done means
	Round           int      // reworks so far
	Machine         string   // where its agent runs (a name)
	Session         string   // its agent's session id (short)
	AgentID         string   // full id for durable deduplication; not rendered
	Last            string   // what its agent said last
	Link            string   // the task's page ("" when the hub has no public_url)
	Quiet           bool     // the user was not pushed about it
	// report: what its agent reported (progress, question, blocked, done), its words, evidence and step.
	Report      string
	Said        string
	Evidence    []string
	Step, Steps int
}

func RenderSupervise(s Supervise) string { return render("assistant/supervise", s) }

// SuperviseRework is what a task's agent is sent when its supervisor sends the work back.
type SuperviseRework struct {
	Round, Max int
	Checks     []SuperviseCheck
	Note       string
}

type SuperviseCheck struct {
	Criterion, Evidence string
	Met                 bool
}

func RenderSuperviseRework(r SuperviseRework) string { return render("assistant/supervise_rework", r) }

// RenderTaskDoneWhen is what follows a supervised task's prompt: what done means, and that the agent shows it.
func RenderTaskDoneWhen(criteria []string) string {
	return render("assistant/task_done_when", struct{ Criteria []string }{criteria})
}

// Relay is a message one assistant conversation sends another (conversation_send / conversation_new): what was
// written, behind a note saying where it comes from and how many hops it is from the user's own message.
type Relay struct {
	FromID, FromTitle string
	Tool              string // conversation_send | conversation_new
	Hop, MaxHops      int
	Text              string
}

// RenderRelay is the message the other conversation gets (assistant/relay).
func RenderRelay(r Relay) string { return render("assistant/relay", r) }

// RenderRefused is passed into a running Codex turn when a tool call was refused (assistant/refused).
func RenderRefused(reason string) string { return render("assistant/refused", reason) }

// CodexMemory tells the Codex assistant about the memory it shares with Claude (assistant/codex_memory).
type CodexMemory struct {
	Dir       string
	Index     string // MEMORY.md, trimmed
	Truncated bool   // Index is the start of a longer one
}

func RenderCodexMemory(m CodexMemory) string { return render("assistant/codex_memory", m) }

// RuntimeInstructions is what the assistant on an agent runtime (DSH…) gets with a new conversation's first message
// (assistant/runtime_instructions): the shared memory, and the skills' gated commands it runs as tools.
type RuntimeInstructions struct {
	Dir       string
	Index     string // MEMORY.md, trimmed
	Truncated bool   // Index is the start of a longer one
	// Gates are the skills' gated commands this machine runs as tools (a plugin's assistant.gate with a tool).
	Gates []RuntimeGate
}

// RuntimeGate is one gated command as a runtime's tool: what the card calls it, the command, the tool.
type RuntimeGate struct {
	Title, Command, Tool string
}

func RenderRuntimeInstructions(r RuntimeInstructions) string {
	return render("assistant/runtime_instructions", r)
}

// RenderCodexInstructions is the Codex assistant's developer instructions (assistant/codex_instructions): the memory.
// The live snapshot is not in them but in each message (TurnContext), like Claude's.
func RenderCodexInstructions(memory string) string {
	return render("assistant/codex_instructions", struct{ Memory string }{memory})
}

// TurnContext is the <persona-context> block a message to the assistant starts with (assistant/turn_context): the
// sections of the live snapshot (AssistantContext) the conversation hasn't seen yet. Full: all of them (the
// conversation is new, was compacted, or node lost track); otherwise only those that changed, the time always.
type TurnContext struct {
	Full     bool
	Sections []string
}

func RenderTurnContext(c TurnContext) string { return render("assistant/turn_context", c) }

// TaskDone is the system prompt deciding whether a task opened from an assistant conversation is finished, so the
// hub can mark its todo done (assistant/task_done, docs/assistant/todos.md "Auto-complete").
type TaskDone struct {
	Owner string
	// Delivery: also say how far the work was delivered (assistant.done_needs_delivery, docs/assistant/todos.md "Delivery"): only
	// live or nothing to ship closes the task.
	Delivery bool
}

func RenderTaskDone(d TaskDone) string { return render("assistant/task_done", d) }

// RenderTaskDelivery is the delivery standard a task's agent is handed after its prompt (assistant/task_delivery,
// docs/assistant/todos.md "Delivery"): done is live and checked, and its last line says how far it got.
func RenderTaskDelivery() string { return render("assistant/task_delivery", nil) }

// SideConversation preserves the provenance of a non-owner's message to a tasked Agent.
type SideConversation struct {
	From, Text string
}

func RenderSideConversation(in SideConversation) string {
	return render("assistant/side_conversation", in)
}

// TaskDoneInput is the task as it was handed out and what its agents said when they stopped (assistant/task_done_input).
type TaskDoneInput struct {
	Title, Prompt string
	Replies       []TaskDoneReply
	Criteria      []string `json:"criteria,omitempty"`
	// Hints the hub found in the replies: a merged pull request, a TestFlight build number.
	MergedPR, Build bool
}

// TaskDoneReply is one agent's last reply, and what the user last asked it in that turn when it was not the task's
// own prompt (a follow-up the user sent the agent later).
type TaskDoneReply struct {
	Machine, Ask, Text string
	From               string `json:"from,omitempty"`
	Source             string `json:"source,omitempty"`
}

func RenderTaskDoneInput(in TaskDoneInput) string { return render("assistant/task_done_input", in) }

// MainHandoff is the first message of a new main conversation: what the old one left (its last compaction summary,
// the messages after it), behind a note, then what the user wrote (docs/assistant/assistant.md "New main chat").
type MainHandoff struct {
	FromID  string
	Until   string // when the old one was archived, in the user's words
	Summary string
	Recent  []HandoffTurn
	Text    string
}

// HandoffTurn is one message of the old conversation: Role user or assistant.
type HandoffTurn struct {
	Role string
	Text string
}

// RenderMainHandoff is assistant/main_handoff.
func RenderMainHandoff(h MainHandoff) string { return render("assistant/main_handoff", h) }

// Acceptance is a deployed task, for the three lines of its "Please accept" card (assistant/acceptance_input; the system
// prompt is assistant/acceptance; docs/extensions/bots.md "Please accept").
type Acceptance struct {
	Title, Prompt, Reply string
	Links                []string // the only links it may pick
}

func RenderAcceptance() string                  { return render("assistant/acceptance", nil) }
func RenderAcceptanceInput(a Acceptance) string { return render("assistant/acceptance_input", a) }

// WorkspaceMention is what the assistant gets for a workspace the user @-mentioned (assistant/workspace_mention,
// #5594): where it is, the knowledge base's projects there and the agents that worked there lately. It replaces the
// client's <persona-context kind="workspace"> block in the message.
type WorkspaceMention struct {
	Title, Machine, MachineName, Cwd, Branch string
	Online                                   bool
	Projects                                 []WorkspaceProject
	Sessions                                 []WorkspaceSession
}

// WorkspaceProject is a knowledge-base project whose directories hold the workspace.
type WorkspaceProject struct {
	Name, Slug, OneLine string
}

// WorkspaceSession is an agent that worked in the workspace's directory: Cwd only when it is a folder under it.
type WorkspaceSession struct {
	Title, Agent, Status, When, ID, Cwd string
}

func RenderWorkspaceMention(w WorkspaceMention) string {
	return render("assistant/workspace_mention", w)
}
