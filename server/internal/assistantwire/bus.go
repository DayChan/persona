package assistantwire

// ---- the bus ----

// AssistantID is the bus identity the assistant connects as (a manager's token, hub/bustokens.go).
const AssistantID = "assistant"

// Capabilities a manager names in manager.hello's capabilities (stable names: never renamed or reused).
const (
	// CapabilityConfigOwn: the manager reads its own sections of the configuration from its manager.json (owner,
	// notes, Agents, the knowledge base, the models, a card's wait): the hello does not carry them any more, and a
	// manager without it runs on its built-in defaults (docs/assistant/managers.md "Configuration").
	CapabilityConfigOwn = "config.own"
	// CapabilityAgentSessionArchives: the manager owns recoverable Codex/Claude archives.
	CapabilityAgentSessionArchives = "agent_session_archives"
	// CapabilityAgentSessionArchiveClose: the manager's archive also takes ?close=1, "Archive and close", which closes
	// the agent on its computer (session.retire) once archived (docs/protocol/protocol.md "Agent session archives").
	CapabilityAgentSessionArchiveClose = "agent_session_archive_close"
	// CapabilityMachinesOwn: the manager keeps its own list of computers in its manager.json and takes the hub's edits
	// of it (devicewire MethodMachinesPut / Remove / Get) once the hub hands it MachinesOwn
	// (docs/assistant/managers.md "Each manager's computers").
	CapabilityMachinesOwn = "machines.own"
)

// The methods and events, by direction. The assistant's handshake is manager.hello {id, host, os, arch, deploy,
// version, capabilities}; its answer is Hello (with the hub's name, role and managers besides).
const (
	// The assistant calls the hub (Hub).
	MethodHello    = "manager.hello"
	MethodDevice   = "device.call"
	MethodApp      = "app.call"
	MethodNotify   = "notify.request"
	MethodSnapshot = "manager.snapshot"
	EventBroadcast = "ws.broadcast" // one way

	// The hub calls the assistant (Handler).
	MethodHTTP    = "http.request" // HTTPRequest → HTTPResponse
	MethodTools   = "assistant.tools"
	MethodTool    = "assistant.tool"
	MethodContext = "assistant.context"
	MethodAction  = "manager.action"

	// The hub tells the assistant (one way).
	EventUserSpoke           = "session.user_spoke"
	EventUserStopped         = "session.user_stopped" // the user stopped an assistant conversation's turn in an app
	EventTurnStarted         = "session.turn_started"
	EventTurnEnded           = "session.turn_ended"
	EventSessionsChanged     = "sessions.changed"
	EventInteractionResolved = "interaction.resolved"
	EventInteractionRaised   = "interaction.raised"
	EventUpdated             = "manager.updated" // Hello again: hub.json's assistant section or the machines changed
	EventResync              = "manager.resync"  // {}: report the whole snapshot again

	// EventStartEnded (StartEnded): how an app's start the node ran before answering went (POST /api/v1/sessions).
	EventStartEnded = "session.start_ended"
)

// Error codes that cross the bus where a Go error would lose its kind (rpc.Error.Code).
const (
	CodeNoDevice         = "no_device"         // ErrNoDevice
	CodeMachineOffline   = "machine_offline"   // the machine's node is not connected (apierr.ErrOffline)
	CodeNodeClosed       = "node_closed"       // the link to the machine's node dropped during the call (rpc.ErrClosed)
	CodeStaleSnapshot    = "stale_snapshot"    // ErrStaleSnapshot
	CodeAssistantOffline = "assistant_offline" // the hub has no connection from the assistant
	// CodeFeishuAPI: Feishu answered the bot's call with an error code (Data: {"code": n}): a refusal or a passing
	// failure the assistant tells apart (the agent messages, feishu_cot.go).
	CodeFeishuAPI = "feishu_api"
	// CodeChatDenied: the hub refused to send into a chat or to reply to a message in one, because the bot may only use
	// the owner's private chat and the group chats the owner opted in. Nothing was sent.
	CodeChatDenied = "chat_denied"
)

// HTTPRequest is http.request's params: an app's request on one of Routes, with an escaped URL path (header without
// the hub's credentials and hop-by-hop headers, the asking device's role in RoleHeader; body as base64).
type HTTPRequest struct {
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Query  string              `json:"query,omitempty"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
}

// HTTPResponse is http.request's answer, written back to the app unchanged.
type HTTPResponse struct {
	Status int                 `json:"status"`
	Header map[string][]string `json:"header,omitempty"`
	Body   []byte              `json:"body,omitempty"`
}

// Routes are the routes the assistant answers (net/http patterns): the hub relays the apps' requests on them as
// http.request. The assistant registers exactly these (assistant.TestRoutesAreTheWireOnes).
var Routes = []string{
	"GET /api/v1/sessions/archived",
	"POST /api/v1/sessions/{mid}/{sid}/archive",
	"POST /api/v1/sessions/{mid}/{sid}/unarchive",
	"POST /api/v1/assistant/conversations/{id}/archive",
	"GET /api/v1/assistant/conversations/{id}/always",
	"DELETE /api/v1/assistant/conversations/{id}/always/{hash}",
	"POST /api/v1/assistant/conversations/{id}/unarchive",
	"POST /api/v1/assistant/conversations/{id}/stop",
	"GET /api/v1/assistant/reporters",
	"GET /api/v1/assistant/conversations/{id}/feed",
	"POST /api/v1/assistant/conversations/{id}/route",
	"POST /api/v1/assistant/conversations/{id}/today",
	"GET /api/v1/assistant/tasks",
	"POST /api/v1/assistant/tasks",
	"PUT /api/v1/assistant/tasks/{id}",
	"DELETE /api/v1/assistant/tasks/{id}",
	"POST /api/v1/assistant/tasks/{id}/run",
	"POST /api/v1/assistant/tasks/{id}/fresh",
	"GET /api/v1/assistant/places",
	"POST /api/v1/assistant/places",
	"PUT /api/v1/assistant/places/{id}",
	"DELETE /api/v1/assistant/places/{id}",
	"GET /api/v1/assistant/runs",
	"GET /api/v1/assistant/routines",
	"PUT /api/v1/assistant/routines",
	"POST /api/v1/assistant/routines/{id}/run",
	"GET /api/v1/assistant/followups",
	"POST /api/v1/assistant/followups/{id}/run",
	"DELETE /api/v1/assistant/followups/{id}",
	"GET /api/v1/task-plans/{id}",
	"POST /api/v1/task-plans/{id}/start",
	"POST /api/v1/task-plans/{id}/dismiss",
	"GET /api/v1/task-trust",
	"DELETE /api/v1/task-trust",
	// The todos (the assistant's todos.go, docs/assistant/todos.md).
	"GET /api/v1/todos",
	"POST /api/v1/todos",
	"PATCH /api/v1/todos/{id}",
	"DELETE /api/v1/todos/{id}",
	"PUT /api/v1/todos/{id}/spaces/{mid}/{space...}",
	"DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}",
	"PUT /api/v1/todos/{id}/agents/{mid}/{sid}",
	"DELETE /api/v1/todos/{id}/agents/{mid}/{sid}",
	"GET /api/v1/todos/{id}/decisions",
	"POST /api/v1/todos/{id}/decisions",
	"PATCH /api/v1/todos/{id}/decisions/{did}",
	"PUT /api/v1/todos/{id}/thread",
	"DELETE /api/v1/todos/{id}/thread",
	"POST /api/v1/todos/{id}/accept",
	"POST /api/v1/later/{id}/handled", // a question that can wait, marked handled (later.go)
	"POST /api/v1/todos/{id}/done-check",
	"POST /api/v1/todos/{id}/retry",
	"POST /api/v1/todos/retitle",
	// Its settings and its main conversation (profile.go, start.go).
	"GET /api/v1/assistant",
	"PUT /api/v1/assistant",
	"POST /api/v1/assistant/warm",
	"GET /api/v1/assistant/readiness",
	"POST /api/v1/assistant/readiness/dismiss",
	"GET /api/v1/assistant/memory",
	"PUT /api/v1/assistant/memory/{name}",
	"DELETE /api/v1/assistant/memory/{name}",
	"GET /api/v1/assistant/capabilities",
	"POST /api/v1/assistant/conversations",
	"POST /api/v1/assistant/conversations/{id}/compact",
	"POST /api/v1/assistant/main/new",
	"POST /api/v1/assistant/origins/backfill",
	// The channels (channels.go there, docs/extensions/channels.md "API").
	"GET /api/v1/channels",
	"GET /api/v1/channels/{id}",
	"PUT /api/v1/channels/{id}",
	"DELETE /api/v1/channels/{id}/binding",
	"POST /api/v1/assistant/conversations/{id}/views/{key}/actions",
	"POST /api/v1/assistant/conversations/{id}/views/{key}/html-actions",
	"POST /api/v1/channels/{id}/test",
	// The Lark bot's workbench: companion bots and their notices (feishu_companions.go there, docs/extensions/bots.md
	// "Companion bots").
	"PUT /api/v1/assistant/feishu/companions",
	"GET /api/v1/assistant/feishu/notices",
	"POST /api/v1/assistant/feishu/notices/action",
	"POST /api/v1/assistant/feishu/workbench",
	// The knowledge base (the assistant's kb_routes.go, docs/assistant/knowledge.md "App API").
	"GET /api/v1/kb/{kind}",
	"GET /api/v1/kb/{kind}/{id}",
	"PUT /api/v1/kb/{kind}/{id}",
	"GET /api/v1/kb/moments/{id}",
	"GET /api/v1/kb/chats/{id}/context",
	"GET /api/v1/kb/chats/{id}/days/{day}",
	"GET /api/v1/kb-search",
	// The phone's recordings (kb_recordings.go there, docs/protocol/protocol.md "Recording sync").
	"PUT /api/v1/recordings/{id}",
	"DELETE /api/v1/recordings/{id}",
	"GET /api/v1/recordings",
}
