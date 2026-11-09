package assistantwire

import (
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the Agents business: the manager's (docs/assistant/agent-work.md "Who owns what") ----
//
// The manager triages an agent's turn once it ends (rules first, then a model), sends "Continue" after a transient model
// error, wakes an agent when what it waits for should be over, and opens a card for what the user is to decide; it
// names the tabs of new agents and says what a new or resumed agent runs on ("Models" › Agents, a model picked for one
// start). The hub only carries it: it tells the manager what happened (session.turn_started / turn_ended,
// session.seen), shows what the manager reports (Snapshot.Work on the session lists, the cards among
// Snapshot.Interactions) and asks it to fill in a start (agents.start_params, agents.resume_params, agents.tab_name).

// AgentsConfig is manager.json "agents" (Hello.Agents, over the hub's hello): what the manager does about agents'
// turns by default.
type AgentsConfig struct {
	Triage   TriageConfig   `json:"triage,omitzero"`
	Delegate DelegateConfig `json:"delegate,omitzero"`
	// LauncherHealth is manager.json agents.launcher_health: checking each machine's launchers (docs/server/launcher-health.md).
	LauncherHealth LauncherHealthConfig `json:"launcher_health,omitzero"`
}

// LauncherHealthConfig is manager.json agents.launcher_health: how often each machine's launchers are checked, and the agent
// that repairs the ones a node cannot repair itself.
type LauncherHealthConfig struct {
	// Enabled: check on a schedule and repair what fails (unset: off). Off, failed starts are still recorded and
	// steer new agents away.
	Enabled *bool `json:"enabled,omitempty"`
	// IntervalMinutes between two checks of one launcher on one machine. Default 30.
	IntervalMinutes int                  `json:"interval_minutes,omitempty"`
	Repair          LauncherRepairConfig `json:"repair,omitzero"`
}

// LauncherRepairConfig is the agent that repairs a machine's launchers: started on Machine (default: the assistant's)
// in Cwd (default: home) with Launcher (default: a healthy one there) and Permission (a permission mode; default:
// bypassPermissions, so it repairs without waiting on approvals), told to use Skill. No Skill: no repair agent.
type LauncherRepairConfig struct {
	Skill      string `json:"skill,omitempty"`
	Machine    string `json:"machine,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Launcher   string `json:"launcher,omitempty"`
	Permission string `json:"permission,omitempty"`
}

// DelegateConfig is manager.json agents.delegate: an agent's question goes to the assistant before the user
// (docs/assistant/assistant.md "The assistant answers questions first").
type DelegateConfig struct {
	// Enabled: off unless true.
	Enabled *bool `json:"enabled,omitempty"`
	// TimeoutSeconds: how long the hub waits for the assistant before it asks the user as it would have. Default 180.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// Rules are the user's own, read with the built-in ones ("add the missing tests, don't ask me" …). They cannot take a red line off
	// the user: an answer that would set one off is left to them all the same.
	Rules string `json:"rules,omitempty"`
}

// On: the switch, off unless set true.
func (c DelegateConfig) On() bool { return c.Enabled != nil && *c.Enabled }

// Timeout is TimeoutSeconds, 3 minutes when unset.
func (c DelegateConfig) Timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return 3 * time.Minute
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// TriageConfig is manager.json agents.triage: the limits, and the switches a hub whose user never saved them starts with
// (unset: off).
type TriageConfig struct {
	Enabled      *bool `json:"enabled,omitempty"`
	AutoContinue *bool `json:"auto_continue,omitempty"`
	Wake         *bool `json:"wake,omitempty"`
	Decide       *bool `json:"decide,omitempty"`
	// MaxRetries: "Continue" sent for one run of transient errors before asking the user. Default 3.
	MaxRetries int `json:"max_retries,omitempty"`
	// MaxWakes: wakes for one wait before asking the user. Default 6.
	MaxWakes int `json:"max_wakes,omitempty"`
	// SettleSeconds: how long a stopped session stays stopped before its turn is triaged. Default 15.
	SettleSeconds int `json:"settle_seconds,omitempty"`
}

// WorkReport is the snapshot's work (Snapshot.Work): where each agent stands after its latest turn, as the manager
// found it. The hub puts a session's on the lists while the session is still stopped on that turn (Work.TurnAt), and
// none at all while On is false; what an agent shows while it works or asks on its own card is the hub's to say.
type WorkReport struct {
	On       bool          `json:"on"` // Settings › session triage is on
	Sessions []SessionWork `json:"sessions"`
}

// SessionWork is one session's work state in the report; Work.TurnAt is the end of the turn it is about.
type SessionWork struct {
	MachineID string          `json:"machine_id"`
	SessionID string          `json:"session_id"`
	Work      proto.WorkState `json:"work"`
}

// CodeGone is manager.action's refusal of an answer whose question or card is not there any more (answered, withdrawn,
// expired): final, the hub does not keep the answer to try again (any other error it does).
const CodeGone = "gone"

// WorkCardTool is the tool_name of the manager's cards about an agent's turn (among Snapshot.Interactions, answered
// like its other questions with manager.action): the lists put an agent's own question before one of these.
const WorkCardTool = "persona_triage"

// ReviewCardTool is the tool_name of the manager's cards with an independent reviewer's findings about a session's
// change (#1808, kind "review"): answered with manager.action {action:"send"} or {dismiss:true}.
const ReviewCardTool = "persona_review"

const (
	// MethodAgentStartParams: AgentStartParams → {params}: an Agent the app starts (POST /api/v1/sessions), with what
	// it runs on filled in and its tab named. An error with CodeBadRequest is the app's to see (400); while the manager
	// is not connected the hub starts it as the app asked.
	MethodAgentStartParams = "agents.start_params"
	// MethodAgentResumeParams: AgentResumeParams → {params}: a message that resumes an Agent nothing says the launcher
	// of (started outside Persona), with the launcher and model it is to run on.
	MethodAgentResumeParams = "agents.resume_params"
	// MethodAgentTabName: {prompt} → {name}: a herdr tab's name for an Agent started with prompt ("" when none).
	MethodAgentTabName = "agents.tab_name"
	// EventSessionSeen: SessionSeen, one way: the user looked at a session in an app.
	EventSessionSeen = "session.seen"
)

// AgentStartParams is agents.start_params's params: Params are POST /api/v1/sessions's (the node's session.start);
// NameFrom is what the tab is named from now ("": not now: it has a name, resumes a session, or is named later). Lang
// is the app's language, the one a refusal is said in ("": the user's; an older hub sends none).
type AgentStartParams struct {
	MachineID string         `json:"machine_id"`
	Params    map[string]any `json:"params"`
	NameFrom  string         `json:"name_from,omitempty"`
	Lang      string         `json:"lang,omitempty"`
}

// AgentResumeParams is agents.resume_params's params: Params are the message's (the node's session.send; the hub
// sends none and puts the answer's over its own: a message may carry pictures).
type AgentResumeParams struct {
	MachineID string         `json:"machine_id"`
	SessionID string         `json:"session_id"`
	Params    map[string]any `json:"params"`
}

// SessionSeen is session.seen's params.
type SessionSeen struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
}

// agentRoutes are the Agents business's routes the manager answers (agents_routes.go there): its switches, its
// numbers, renaming the tabs nobody named, an independent review of a session's change.
var agentRoutes = []string{
	"GET /api/v1/agents/triage",
	"PUT /api/v1/agents/triage",
	"GET /api/v1/agents/work/stats",
	"POST /api/v1/tabs/autoname",
	"POST /api/v1/machines/{mid}/sessions/{sid}/review",
	"GET /api/v1/machines/{mid}/sessions/{sid}/review",
}

func init() { Routes = append(Routes, agentRoutes...) }
