package proto

import "time"

// Work phases (WorkState.Phase) and what a blocked session waits on (WorkState.On), docs/assistant/agent-work.md.
const (
	WorkWorking = "working"
	WorkBlocked = "blocked"
	WorkDone    = "done"

	WaitUser      = "user"      // a decision, an answer, a link to open: the hub makes a card for it
	WaitAgent     = "agent"     // another agent finishing: the hub wakes this one when it stops
	WaitExternal  = "external"  // a deploy, CI, a review, a time: the hub wakes it to check
	WaitTransient = "transient" // the model service failed for a moment: the hub sends "Continue"
)

// WorkState is a session's work state (Session.Work).
type WorkState struct {
	Phase string `json:"phase"`
	On    string `json:"on,omitempty"`
	// Line is one line for the lists: the conclusion when done, what it waits for when blocked.
	Line string `json:"line,omitempty"`
	// Wait / Then: what it waits for and what it will do then (agent, external).
	Wait string `json:"wait,omitempty"`
	Then string `json:"then,omitempty"`
	// Every is how many minutes after the turn the hub first looks again for the wait (it doubles after each wake).
	Every int `json:"every_minutes,omitempty"`
	// WakeAt is when the hub acts next on its own: sends "Continue" (transient) or wakes it to check (external, agent).
	WakeAt *time.Time `json:"wake_at,omitempty"`
	// ResetsAt: the wait is for a subscription usage limit to reset (on external), at this time
	// (Session.TurnErrorResetsAt). The agent goes on after it by itself when its CLI said so, else the hub continues it
	// (WakeAt, under the auto_continue switch); on user, the card after the continues the limit kept refusing.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Attempts the hub made for this wait so far, of at most Max.
	Attempts int        `json:"attempts,omitempty"`
	Max      int        `json:"max,omitempty"`
	Agent    *WorkAgent `json:"agent,omitempty"` // the agent it waits for (on agent)
	Link     *WorkLink  `json:"link,omitempty"`  // a link the user must open in time (on user)
	// Artifacts are what the work produced (done): PRs, commits, documents, builds.
	Artifacts []WorkArtifact `json:"artifacts,omitempty"`
	// CI is the manager's durable watch of the pull request this turn awaits.
	CI *WorkCI `json:"ci,omitempty"`
	// RetiredAt marks a todo retirement; RetiredTurnAt is the stopped turn as
	// reported by its node, when it could be read at retirement.
	RetiredAt     *time.Time `json:"retired_at,omitempty"`
	RetiredTurnAt *time.Time `json:"retired_turn_at,omitempty"`
	// Questions are what the user is to decide (on user), as the card asks them.
	Questions []Question `json:"questions,omitempty"`
	// InteractionID is the card the hub made for a decision (on user), while it is pending.
	InteractionID string `json:"interaction_id,omitempty"`
	// Deferred: the user put the decision off on its card (Skip for now, Not now, Stop waiting, Hold off, or closed it): the agent is no
	// Awaiting Reply for it and is not reminded of (#5096).
	Deferred bool `json:"deferred,omitempty"`
	// TurnAt is the end of the turn this state is about (the session's updated_at then); At when it was found, By how:
	// "rule" or "model".
	TurnAt time.Time `json:"turn_at,omitzero"`
	At     time.Time `json:"at,omitzero"`
	By     string    `json:"by,omitempty"`
}

// WorkCI records the target and fallback across manager restarts.
type WorkCI struct {
	Repo         string    `json:"repo,omitempty"`
	Number       int       `json:"number"`
	FallbackAt   time.Time `json:"fallback_at"`
	Polls        int       `json:"polls,omitempty"`
	SourceErrors int       `json:"source_errors,omitempty"`
	Observed     string    `json:"observed,omitempty"`
	ObservedAt   time.Time `json:"observed_at,omitzero"`
	TodoProof    string    `json:"todo_proof,omitempty"` // closed todo versions checked for this turn
	Message      string    `json:"message,omitempty"`
}

// WorkAgent names another agent (WorkState.Agent).
type WorkAgent struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Label     string `json:"label,omitempty"`
}

// WorkLink is a link to open before it expires (WorkState.Link).
type WorkLink struct {
	URL       string     `json:"url"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// WorkArtifact is something a session's work produced: kind pr, commit, doc, build or url, and its reference.
type WorkArtifact struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}
