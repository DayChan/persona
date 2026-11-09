package assistantwire

import (
	"github.com/nwverse/persona/server/internal/proto"
)

// What comes from the nodes is the manager's (docs/server/machines.md "Who handles the computers' events"): it creates and answers the
// interactions, works out each Agent's state, decides the pushes and the Live Activity's content, runs the nodes' MCP
// tools and keeps the machines' monitoring. The hub shows and delivers what it reports: the session lists, the
// pending interactions and the fleet (kept while the manager is away), and the pushes (APNs, the relay, the apps'
// notify events).

// Manager → hub, one way.
const (
	// EventFleetSessions is a machine's session list as the apps get it (FleetSessions): after every change.
	EventFleetSessions = "fleet.sessions"
	// EventFleetInteraction is a pending interaction that is new or shown again (FleetInteraction).
	EventFleetInteraction = "fleet.interaction"
	// EventFleetResolved is an interaction that is over (FleetResolved).
	EventFleetResolved = "fleet.interaction_resolved"
	// EventFleetInteractions is every pending interaction (FleetInteractions): after each handshake, the hub's list
	// replaced by it.
	EventFleetInteractions = "fleet.interactions"
	// EventFleetLive is the Live Activity's content and the island's numbers (FleetLive), when they change.
	EventFleetLive = "fleet.live"
)

// Hub → manager, one way.
const (
	// EventClients is what the hub's connected apps can take now (Clients), after every change.
	EventClients = "fleet.clients"
	// EventMutes is the muted spaces / agents (FleetMutes), the hub's: once after each handshake (on the manager's
	// first word: they are in the hello's answer too, Hello.Mutes) and whenever the user changes them.
	EventMutes = "fleet.mutes"
	// EventSeen: an app looked at a session (its WebSocket's seen) (FleetSeen).
	EventSeen = "fleet.seen"
)

// The hub's apps the manager calls for what is still the hub's to run (app.call).
const (
	// AppPhone is the phone's own MCP tools: listing them and running one on a connected phone (waking it first).
	AppPhone   = "phone"
	PhoneTools = "phone.tools" // {} → {tools}
	PhoneCall  = "phone.call"  // {name, args} → proto.ToolResult
)

// InteractionsOwnerAction has the manager's channels take the owner action they handed over with a channel's press or
// message, for the card the assistant is about to answer with it in channel ({interaction_id, action, channel, kind}
// → {}, kind the card's in the queue; an error: refused).
// Answered in the manager's process (internal/extmgr channels_bus.go).
const InteractionsOwnerAction = "interactions.owner_action"

// MethodFleetAsk is a card one of the hub's own features asks the user (a mail send, Cua's apps, persona_settings):
// {interaction} → proto.Resolution, when it is answered, expires or the call ends.
const MethodFleetAsk = "fleet.ask"

// ElicitationHeader marks an app's request the hub relays to the manager as coming from an app that takes MCP
// elicitations ("1"): the interaction history keeps them out otherwise.
const ElicitationHeader = "X-Persona-Elicitation"

// FleetSessions is EventFleetSessions' params. Muted are the sessions whose pushes are off.
type FleetSessions struct {
	MachineID string          `json:"machine_id"`
	Sessions  []proto.Session `json:"sessions"`
	Muted     []string        `json:"muted,omitempty"`
}

// FleetInteraction is EventFleetInteraction's params.
type FleetInteraction struct {
	Interaction proto.Interaction `json:"interaction"`
}

// FleetResolved is EventFleetResolved's params; AnsweredBy says the assistant answered it, Reason why and Answers
// with what (docs/assistant/assistant.md "The assistant answers questions first").
type FleetResolved struct {
	ID         string            `json:"id"`
	AnsweredBy string            `json:"answered_by,omitempty"`
	Reason     string            `json:"reason,omitempty"`
	Answers    map[string]string `json:"answers,omitempty"`
}

// FleetInteractions is EventFleetInteractions' params, oldest first.
type FleetInteractions struct {
	Interactions []proto.Interaction `json:"interactions"`
}

// FleetLive is EventFleetLive's params.
type FleetLive struct {
	State FleetState `json:"state"`
	Stats FleetStats `json:"stats"`
}

// Clients is EventClients' params.
type Clients struct {
	// Elicitation: an app that takes MCP elicitations is connected (without one an elicitation is declined at once).
	Elicitation bool `json:"elicitation"`
}

// FleetSeen is EventSeen's params.
type FleetSeen struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
}

// The kinds of push the manager asks the hub for (Notification.Kind besides NotifyPush).
const (
	// NotifyDone is an agent's finished turn: the devices whose settings take a turn of TookMS (LongMS counts as
	// long) get it.
	NotifyDone = "done"
	// NotifyInput is a pending interaction (Notification.Interaction): the devices that take "needs you" pushes.
	NotifyInput = "input"
	// NotifyReply reminds the user of an agent still waiting on their answer (Session.AwaitsReply, #5096): the devices
	// that take "needs you" pushes; answerable from the notification as a finished turn is.
	NotifyReply = "reply"
)

// FleetState is the Live Activity's ContentState, field for field (apple/Shared/FleetState.swift). Times
// are Unix seconds: ActivityKit decodes pushed JSON with a default JSONDecoder, which would read a Date as seconds
// since 2001. The manager works out what it shows; the hub fills in each device's part (Details, Left, Right, Stats,
// Look) when it pushes.
type FleetState struct {
	Phase    string      `json:"phase"` // needsYou | working | done | "" (nothing to show)
	NeedsYou int         `json:"needsYou"`
	Working  int         `json:"working"`
	Unread   int         `json:"unread"`
	Items    []FleetItem `json:"items"`
	// WorkingSince is when the longest-running working agent started its turn (the phone runs the clock).
	WorkingSince float64 `json:"workingSince,omitempty"`
	// Details: the lock screen may show titles (the device's lock_details setting); otherwise only counts.
	Details bool `json:"details"`
	// Assistant: the assistant ("Chat" tab) is answering. Pushed only to devices that show it.
	Assistant bool `json:"assistant,omitempty"`
	// Left / Right: the compact Dynamic Island's sides (the device's island setting); empty means status counts.
	Left  string `json:"left,omitempty"`
	Right string `json:"right,omitempty"`
	// Stats: the numbers the device shows, rounded so that small changes do not cost a push.
	Stats *FleetStats `json:"stats,omitempty"`
	// Look: the persona the device's activity draws, as the app told the hub (prefs look); nil until it did.
	Look *FleetLook `json:"look,omitempty"`
}

// FleetLook is the persona the activity draws (apple/Shared/FleetState.swift FleetLook): the hub only passes on what
// the device told it.
type FleetLook struct {
	Design  string `json:"design"` // a built-in's drawing ("visor", "circuit"); a package's while its pictures cannot be read
	Light   uint32 `json:"light"`  // the accent, 0xRRGGBB, light and dark
	Dark    uint32 `json:"dark"`
	Package string `json:"package,omitempty"` // a hub package, "<id>/<rev>": its pictures are on the phone
}

// FleetStats are the island's numbers. A push carries only those the device shows; the app's own copy has them all.
type FleetStats struct {
	TPM      int64       `json:"tpm,omitempty"`
	Tokens   int64       `json:"tokens,omitempty"`
	Requests int         `json:"requests,omitempty"`
	Loads    []FleetLoad `json:"loads,omitempty"`
}

// FleetLoad is a machine's 1-minute load average over its cores, in percent (rounded to 5).
type FleetLoad struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Percent int    `json:"percent"`
}

// FleetItem is one agent the activity lists.
type FleetItem struct {
	ID     string `json:"id"` // machine/session, also the deep link
	Title  string `json:"title"`
	Place  string `json:"place"`
	Kind   string `json:"kind"` // permission | plan | question | screen | working | unread
	Detail string `json:"detail"`
	// Background: a working item whose turn ended while its background work runs (#2935, Session.BackgroundWorking);
	// Detail then says what runs ("parity-audit · Verify · 169/185"). Older apps ignore it and show it working.
	Background bool `json:"background,omitempty"`
	// Progress: the counts of the workflow Detail names, for the expanded island's bar (#3437). Nil when the item is not
	// a background workflow or its workflow has started no agent yet. It moves only when Detail does, so it costs no push.
	Progress    *FleetProgress `json:"progress,omitempty"`
	Interaction string         `json:"interaction,omitempty"` // what "Allow" / "Deny" / "Approve" resolve
	Since       float64        `json:"since,omitempty"`
}

// FleetProgress is a background workflow's agents done of those it started so far, as Detail prints them ("169/185"):
// the total is not known before the workflow ends, since scripts fan out as they go.
type FleetProgress struct {
	Done    int `json:"done"`
	Started int `json:"started"`
}

// UsageSummary is the token usage across the machines (GET /api/v1/usage, the manager's).
type UsageSummary struct {
	TPM      int64 `json:"tpm"`      // output tokens per minute, over the last two to three minutes
	Tokens   int64 `json:"tokens"`   // today: all tokens, cache reads included
	Output   int64 `json:"output"`   // today
	Requests int   `json:"requests"` // today
	// Input (not from the cache), CacheWrite and CacheRead: today, for the cache hit rate
	// (CacheRead / (Input + CacheWrite + CacheRead)) on 机器's card of today's tokens.
	Input      int64          `json:"input"`
	CacheWrite int64          `json:"cache_write"`
	CacheRead  int64          `json:"cache_read"`
	Machines   []MachineUsage `json:"machines"`
}

// MachineUsage is one machine's share and load; Load is the 1-minute load average over its cores, 0 when unknown.
type MachineUsage struct {
	ID     string  `json:"id"`
	TPM    int64   `json:"tpm"`
	Tokens int64   `json:"tokens"`
	Load   float64 `json:"load"`
}

// FleetMutes is fleet.mutes' params. The hub keeps the mutes (its settings; GET / PUT /api/v1/mutes); the manager
// decides the pushes by them.
type FleetMutes struct {
	// Mutes are the mutes now; nil from a hub before they came with the event (the manager keeps the ones it had).
	Mutes *Mutes `json:"mutes,omitempty"`
}

// Mutes: muted spaces ("machine/space name") and agents ("machine/session id"); Allowed are agents that keep their
// notifications although their space is muted.
type Mutes struct {
	Spaces   []string `json:"spaces"`
	Sessions []string `json:"sessions"`
	Allowed  []string `json:"allowed,omitempty"`
}
