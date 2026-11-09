package prompts

// AgentTriage is the system prompt that reads a stopped agent's last reply and says where its work stands
// (agents/triage, docs/assistant/agent-work.md).
type AgentTriage struct {
	Owner string // "" is the user
}

func RenderAgentTriage(t AgentTriage) string { return render("agents/triage", t) }

func RenderAgentToolLoop() string { return render("agents/tool_loop", nil) }

// AgentTriageInput is the turn to triage (agents/triage_input).
type AgentTriageInput struct {
	Now     string // the user's local time, "2006-01-02 15:04 Mon"
	Agent   string // its tab / title
	Machine string
	Cwd     string
	Project string // the knowledge base's project for its directory, "" when none
	Ask     string // what the user asked in this turn, "" when it is not known
	Reply   string // its last reply, clipped
	Others  []AgentTriageOther
	// The risky step the user already said yes to, once what it waits for is over ("" when none): a turn still
	// waiting for it is no question for the user.
	ApprovedWait, ApprovedThen string
}

// AgentTriageOther is another agent working on the same machine: what this one may be waiting for.
type AgentTriageOther struct {
	ID, Label, Line string
}

func RenderAgentTriageInput(in AgentTriageInput) string { return render("agents/triage_input", in) }

// AgentWake is what the hub sends an agent whose wait should be over (agents/wake).
type AgentWake struct {
	Attempt   int
	Wait      string
	Then      string
	Agent     string // the agent it waited for, when it is that one stopping that wakes it
	AgentLine string // what that agent said last
	Approved  string // the risky step the user already said yes to, once the wait is over ("" when none)
}

func RenderAgentWake(w AgentWake) string { return render("agents/wake", w) }

// RenderAgentLimitContinue is what the manager sends an agent a subscription's usage limit stopped, once the limit has
// reset and its CLI did not say it goes on by itself (agents/limit_continue, docs/assistant/agent-work.md "Usage limits").
func RenderAgentLimitContinue() string { return render("agents/limit_continue", nil) }

// AgentAnswer is the user's answers to the hub's decision card, sent on to the agent (agents/answer).
type AgentAnswer struct {
	Items []AgentAnswerItem
	// ByAssistant: the assistant answered for the user (docs/assistant/assistant.md "The assistant answers questions first"), Reason says why.
	ByAssistant bool
	Reason      string
}

type AgentAnswerItem struct {
	Question, Answer string
}

func RenderAgentAnswer(a AgentAnswer) string { return render("agents/answer", a) }

// AgentAuthRelayed tells an agent an extension authorized the link it stopped at with the user's account
// (agents/auth_relayed, docs/operations/auth-relay.md).
type AgentAuthRelayed struct {
	App string // the app, as the authorization page names it; "" when it was not read
}

func RenderAgentAuthRelayed(a AgentAuthRelayed) string { return render("agents/auth_relayed", a) }

// LauncherRepair is the first message of the agent that repairs a machine's launchers (agents/launcher_repair,
// docs/server/launcher-health.md).
type LauncherRepair struct {
	Skill     string // the skill that sets up and repairs the agent CLIs
	Machine   string // the broken machine's id
	Name      string // its name, "" when the same as the id
	SSHHost   string // the hub machine's ssh alias for it, "" when none
	Launchers []LauncherRepairItem
}

// LauncherRepairItem is one launcher that failed its check there.
type LauncherRepairItem struct {
	Name  string
	Agent string // claude, codex…
	Kind  string // the hub's class of the error (model_route, auth, cli_outdated, timeout, other)
	Error string
}

func RenderLauncherRepair(r LauncherRepair) string { return render("agents/launcher_repair", r) }

// AgentReanswer is the user's answers to questions the assistant answered for them, sent on to the agent, which went
// on with the assistant's (agents/reanswer, docs/assistant/assistant.md "The assistant answers questions first").
type AgentReanswer struct {
	Items []AgentReanswerItem
}

type AgentReanswerItem struct {
	Question, Before, Answer string
}

func RenderAgentReanswer(a AgentReanswer) string { return render("agents/reanswer", a) }

// AgentDelegate is the system prompt that decides whether the assistant answers an agent's question for the user
// (agents/delegate, docs/assistant/assistant.md "The assistant answers questions first").
type AgentDelegate struct {
	Owner string // "" is the user
	Rules string // the user's own rules, trimmed: manager.json agents.delegate.rules
}

func RenderAgentDelegate(d AgentDelegate) string { return render("agents/delegate", d) }

// AgentDelegateInput is the question to decide on and what is known around it (agents/delegate_input).
type AgentDelegateInput struct {
	Now       string // the user's local time, "2006-01-02 15:04 Mon"
	Agent     string
	Machine   string
	Cwd       string
	Project   string   // the knowledge base's projects for its directory, "" when none
	Task      string   // the task (todo) it works for: its title and notes, "" when none
	Decisions string   // the calls the user made on that task (todo decisions), "" when none
	Asks      []string // the user's recent messages to the agent, clipped, oldest first
	Reply     string   // what the agent said last, clipped
	Questions []DelegateQuestion
}

// DelegateQuestion is one question of the card, as asked.
type DelegateQuestion struct {
	Question    string
	MultiSelect bool
	Options     []DelegateOption
}

type DelegateOption struct {
	Label, Description string
}

func RenderAgentDelegateInput(in AgentDelegateInput) string {
	return render("agents/delegate_input", in)
}

// AgentReview is the first message of the independent reviewer of a supervised task's finished change (agents/review,
// #2173, docs/assistant/unified-entry.md "Independent review"): it sees the task's ask, its criteria and the change,
// never the agent that did the work, and ends with a machine-readable count line.
type AgentReview struct {
	Language string   // the language to answer in by its own name ("" is Chinese)
	Title    string   // the task's title
	Ask      string   // what the task asked for (the todo's notes), clipped
	DoneWhen []string // what done means
	Scope    string   // what the diff is compared with, one line
	Diff     string   // the capped diff text
	Omitted  int      // files left out of the diff once the budget ran out
}

func RenderAgentReview(r AgentReview) string { return render("agents/review", r) }
