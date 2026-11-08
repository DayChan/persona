package assistant

import "time"

// The processing conversation bound to an event (InboxItem.Handling) and the manager's suggestion on it
// (InboxItem.Suggestion), docs/assistant/inbox.md "Events and the handling chain (phase 1)".

// Handling states.
const (
	HandlingLooking = "looking" // the manager is on it ("In progress", "AI is looking")
	HandlingAgent   = "agent"   // a sub-session it started is working
	HandlingWaiting = "waiting" // a confirmed action went out: waiting for the other side
	HandlingDone    = "done"    // it finished this round (a suggestion out, or a conclusion)
	HandlingFailed  = "failed"  // failed or timed out: back to "Needs you" without a suggestion
)

// HandlingOwner is the only manager of phase 1.
const HandlingOwner = "manager"

// InboxHandling is the processing conversation bound to an event.
type InboxHandling struct {
	Owner     string     `json:"owner"`
	State     string     `json:"state"`
	MachineID string     `json:"machine_id,omitempty"`
	SessionID string     `json:"session_id,omitempty"`
	Since     *time.Time `json:"since,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// Suggestion kinds.
const (
	SuggestReply      = "reply"      // one message to send
	SuggestAgent      = "agent"      // an instruction for a new Agent
	SuggestConclusion = "conclusion" // done: just read it
)

// InboxSuggestion is the manager's one current suggestion on an event; Version bumps with every new one and a
// confirmation must name it.
type InboxSuggestion struct {
	Version   int         `json:"version"`
	Kind      string      `json:"kind"`
	Text      string      `json:"text"`
	Note      string      `json:"note,omitempty"`
	Handback  bool        `json:"handback"`
	MachineID string      `json:"machine_id,omitempty"`
	Changes   []DocChange `json:"changes,omitempty"`
	At        time.Time   `json:"at"`
	// Cwd is where an agent suggestion's Agent starts (the tool's cwd; "" home): kept, not listed.
	Cwd string `json:"cwd,omitempty"`
	// Conversation is the conversation the event was handed to that handed this suggestion in (Hand to AI, inbox_handoff.go);
	// nil for the event's processing conversation. What the user does with it is told there, not to the processing one.
	Conversation *SessionRef `json:"conversation,omitempty"`
}

// InboxHandoff is one hand-off of the event to Persona's main conversation (POST /api/v1/inbox/{id}/handoff): that
// conversation may write back with inbox_suggest. Op is the request's client id (a repeat sends nothing): kept, not
// listed.
type InboxHandoff struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
	Op        string    `json:"op,omitempty"`
}
