package link

// The "Needs you" summary (docs/operations/link.md "Needs you"): the manager that carries this computer tells its
// Persona Link which agents wait for the user, so the menu bar app and the Windows tray can show and notify them while
// no web page is open. A full snapshot each time, never a delta. Active mode: an attention message on the link, to a
// link whose hello listed FeatureAttention. Passive mode: POST ConnectorAttentionPath, to a connector whose info
// listed it. persona-link puts the latest into its Status; it shows and approves nothing itself.

// FeatureAttention is in a hello's features (active mode) and the connector's info features (passive mode): this
// persona-link takes the summary.
const FeatureAttention = "attention"

// ConnectorAttentionPath is passive mode's endpoint for the summary: the body is an Attention, the answer 204.
const ConnectorAttentionPath = "/api/v1/connector/attention"

// AttentionBodyMax caps a posted summary. A full one is far below it (AttentionMaxItems items of clipped text).
const AttentionBodyMax = 64 << 10

// AttentionMaxItems is how many items a summary lists at most; More counts the rest.
const AttentionMaxItems = 20

// Attention is the summary. A computer that may not have it (waiting to be confirmed, a work computer) is sent one
// without items.
type Attention struct {
	// Rev is a hash of the snapshot: the same summary has the same Rev, so sending it again changes nothing.
	Rev string `json:"rev"`
	// NeedsYou is the web app's "Needs you" count, muted agents and ones waiting only on the assistant's decision
	// included; Items lists at most AttentionMaxItems of them and More is the rest.
	NeedsYou int `json:"needs_you"`
	More     int `json:"more"`
	// WebURL is where the web app is: the hub's https public_url (its sub-path included); in active mode without one,
	// the hub address the link went to. Missing when the manager knows neither.
	WebURL string          `json:"web_url,omitempty"`
	Items  []AttentionItem `json:"items"`
}

// AttentionItem is one agent that needs the user, by its oldest card handed to the user, else its oldest card.
type AttentionItem struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Title     string `json:"title"` // at most 80 runes, the ellipsis of a cut included
	Place     string `json:"place"` // "machine · space › tab", as Title
	// Kind is the card's: permission | plan | question | screen (also an agent waiting in a terminal dialog without a
	// card).
	Kind   string `json:"kind"`
	Detail string `json:"detail"` // at most 60 runes, the ellipsis of a cut included
	// Muted: the user muted the agent or its space; for showing it muted, it is listed and counted all the same.
	Muted bool `json:"muted"`
	// Notify: the phone's push rule would push it: once a card of the agent is the user's (announced, not a question
	// the assistant is still deciding on), and never when muted. Not for an agent without such a card: a terminal
	// dialog notifies once the screen card made for it is announced, as the phone is pushed then.
	// Clients notify on this alone and never work the rule out themselves, the first time an item shows with it: that
	// may be after it was listed without it (the assistant handed its question to the user).
	Notify bool `json:"notify"`
	// InteractionID is the card's id (a screen card's too); missing for an agent no card stands for.
	InteractionID string `json:"interaction_id,omitempty"`
	// Since is when it started waiting, RFC 3339 in UTC to the second; missing when unknown.
	Since string `json:"since,omitempty"`
	// Route is the web app's path for the session, joined to WebURL: /s/<machine>/<session>, percent-encoded.
	Route string `json:"route"`
}
