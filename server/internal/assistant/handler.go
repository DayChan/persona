package assistant

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/proto"
)

// Handler is what the hub asks of the assistant, the bus's other direction: the apps' requests it relays as they
// came, the tool calls of the assistant's conversations, and what it sees happen to sessions. Service implements
// it; each method is one message, named in its comment, that BusHub takes off the bus (bus.go).
type Handler interface {
	// ServeHTTP answers the apps' requests on Routes(), relayed unchanged (the caller's role in RoleHeader).
	http.Handler
	// Routes are the HTTP routes (net/http patterns) the hub relays to ServeHTTP: assistantwire.Routes, the list the
	// hub has (TestRoutesAreTheWireOnes).
	Routes() []string
	// Tools is assistant.tools: the MCP tools only the assistant's conversations get (the hub lists them with its
	// own, and sends the calls of these to Tool).
	Tools(ctx context.Context) []proto.Tool
	// Tool is assistant.tool: a conversation called one of the assistant's MCP tools.
	Tool(ctx context.Context, call ToolCall) proto.ToolResult
	// Context is assistant.context: the live context a message to conversation p.SessionID starts with (the node
	// asks for it on every message).
	Context(ctx context.Context, p ContextParams) ContextResult
	// Action is manager.action: the user answered one of the questions the assistant asked (Snapshot.Interactions).
	Action(ctx context.Context, a Action) (ActionResult, error)
	// Start is assistant.start: one of the hub's features starts a conversation of the assistant's.
	Start(ctx context.Context, p StartParams) (proto.Session, error)
	// SendParams is assistant.send_params: what a message for one of its conversations runs on, and its text.
	SendParams(ctx context.Context, p SendParams) (map[string]any, error)
	// Rename is assistant.rename: the user renames one of its conversations in the app (the node's answer).
	Rename(ctx context.Context, p RenameParams) (json.RawMessage, error)
	// UserSpoke is session.user_spoke: the user sent a message to a conversation.
	UserSpoke(ctx context.Context, s SessionRef)
	// UserStopped is session.user_stopped: the user stopped a conversation's turn in an app (its stop button).
	UserStopped(ctx context.Context, s SessionRef)
	// TurnStarted is session.turn_started: a session on a machine started a turn.
	TurnStarted(ctx context.Context, s SessionRef)
	// TurnEnded is session.turn_ended: a session on a machine finished a turn.
	TurnEnded(ctx context.Context, s SessionRef)
	// InteractionResolved is interaction.resolved: the user dealt with an agent's card (any agent's).
	InteractionResolved(ctx context.Context, r InteractionResolved)
	// InteractionRaised is interaction.raised: an agent of a quiet task asks the user something (#1731).
	InteractionRaised(ctx context.Context, r InteractionRaised)
	// ProfileChanged is manager.updated: hub.json's assistant section or the machines changed: what was read with the old
	// ones goes, and the hub gets the profile again.
	ProfileChanged(ctx context.Context)
	// SessionsChanged is sessions.changed: a machine's node sent its session list.
	SessionsChanged(ctx context.Context, d DeviceSessions)
	// Resync is manager.resync: report the whole snapshot again (the hub has none, or a partial one).
	Resync(ctx context.Context)

	// The todos (todos.go).
	// TodoHandoff is assistant.todo_handoff: an agent is about to be handed work for a todo: the todo, whether the
	// hand-off may go, and the text with its decisions before it.
	TodoHandoff(ctx context.Context, h TodoHandoff) (TodoHandoffResult, error)
	// TodoLink is assistant.todo_link: the agent handed the work works for the todo from now on; the todo's title.
	TodoLink(ctx context.Context, p TodoLinkParams) (TodoLinkResult, error)
	// TodoSpaceRenamed is todos.space_renamed: the hub renamed a workspace.
	TodoSpaceRenamed(ctx context.Context, r TodoSpaceRenamed)

	// The Agents business (agents_*.go).
	// AgentStartParams is agents.start_params: an Agent the app starts, with what it runs on and its tab's name.
	AgentStartParams(ctx context.Context, p AgentStartParams) (map[string]any, error)
	// AgentResumeParams is agents.resume_params: a message resuming an Agent started outside Persona, with what it runs on.
	AgentResumeParams(ctx context.Context, p AgentResumeParams) (map[string]any, error)
	// SessionSeen is session.seen: the user looked at a session in an app.
	SessionSeen(ctx context.Context, p SessionSeen)
	// The channels (channels.go, feishu.go): the hub hands over what comes in from the chat apps.
	// ChannelReceive is channels.receive: the owner's message from a plugin's channel.
	ChannelReceive(ctx context.Context, p ChannelReceive) error
	// ChannelUnbound is channels.unbound: discard the channel's pending replies and questions before rebinding.
	ChannelUnbound(ctx context.Context, p ChannelUnbound) error
	// FeishuMessage is feishu.message: the owner's message to the Feishu bot.
	FeishuMessage(ctx context.Context, p FeishuMessage) error
	// FeishuAction is feishu.card_action: the owner pressed one of the bot's cards.
	FeishuAction(ctx context.Context, p FeishuAction) (*feishubot.CardResponse, error)
	// FeishuView is feishu.view: what the bot's settings page shows of its chat.
	FeishuView(ctx context.Context) FeishuView
	// FeishuOwner is feishu.owner: the bot was paired or unpaired.
	FeishuOwner(ctx context.Context, p FeishuOwner)
	// InteractionAnnounced and InteractionEnded are interaction.announced / interaction.ended: a card came, a card
	// is over.
	InteractionAnnounced(ctx context.Context, p InteractionAnnounced)
	InteractionEnded(ctx context.Context, p InteractionEnded)
	// SessionItems and SessionDelta are session.items / session.delta: a conversation the channels follow.
	SessionItems(ctx context.Context, p SessionItems)
	SessionDelta(ctx context.Context, p SessionDelta)
	// ChannelNoted is channels.noted: a channel's message went in through the apps' routes.
	ChannelNoted(ctx context.Context, p ChannelNoted)
	// The inbox (inbox.go).
	// InboxCollected is inbox.collected: what one collection of the sources read.
	InboxCollected(ctx context.Context, r assistantwire.InboxRound) error
	// InboxItemJSON is inbox.item: one item as it is stored.
	InboxItemJSON(ctx context.Context, id string) (json.RawMessage, bool)
	// InboxForget is inbox.forget: a source's data was deleted; its items go.
	InboxForget(ctx context.Context, source string) (int, error)
	// InboxRepoMutes is inbox.repo_mutes: a source's muted repositories (set first when asked).
	InboxRepoMutes(ctx context.Context, p assistantwire.InboxRepoMutes) ([]string, error)
}

// roleFull is a device that may do everything (an answer-only watch may not).
const roleFull = "full"

// roleExtension is an extension's token (ExtensionHeader says which).
const roleExtension = "extension"
