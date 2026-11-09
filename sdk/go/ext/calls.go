package ext

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Typed calls, both ways, for the extension points whose calls the schemas describe (wire.Calls: the connector, the
// channel, the knowledge base and the sent-as-user ledger; schema/extension/connector.schema.json, channel.schema.json
// and kb.schema.json).

// MethodFunc answers one method of a namespace.
type MethodFunc func(ctx context.Context, params json.RawMessage) (any, error)

// Method is a MethodFunc from a function of the method's params and answer types (wire.ConnectorThreadParams →
// wire.ConnectorThreadPage): params that do not decode are refused.
func Method[P, R any](f func(ctx context.Context, p P) (R, error)) MethodFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p P
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, &Error{Message: "params: " + err.Error()}
			}
		}
		return f(ctx, p)
	}
}

// Methods are a namespace's methods by name, without the namespace ("status" for connector.status).
type Methods map[string]MethodFunc

// For is the namespace's Handler, for the contributions given (none: any): a call naming another contribution is
// no_contribution, a method not in m unknown_method. A connector program answers what its capabilities declare:
//
//	Namespaces: map[string]ext.Handler{ext.ConnectorNamespace: ext.Methods{
//		"status": ext.Method(func(ctx context.Context, p wire.ConnectorParams) (wire.ConnectorStatus, error) {
//			return wire.ConnectorStatus{State: wire.ConnectorStateOk}, nil
//		}),
//	}.For("example")}
func (m Methods) For(contributions ...string) Handler {
	return func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if c := Contribution(params); c != "" && len(contributions) > 0 && !slices.Contains(contributions, c) {
			return nil, NoContribution(c)
		}
		f, ok := m[method]
		if !ok {
			return nil, UnknownMethod(method)
		}
		return f(ctx, params)
	}
}

// Empty is the answer of a call that answers nothing ({}: inbox.reply, logout, options.set…).
type Empty = wire.Empty

// ConnectorNamespace is the connectors extension point's: every call of docs/protocol/connector-protocol.md is
// connector.<method>, with the connector's id as the contribution.
const ConnectorNamespace = "connector"

// SentNamespace is the hub's own namespace for the sent-as-user ledger: RecordSent is sent.record.
const SentNamespace = "sent"

// ConnectorChanged tells the hub a connector's chats, items or inbox changed (wire.ConnectorScope*): it reads them
// soon.
func (h *Hub) ConnectorChanged(contribution, scope string) error {
	return h.Notify(ConnectorNamespace+".changed", wire.ConnectorChangedEvent{Contribution: contribution, Scope: scope})
}

// LoadSecret is what the connector keeps in the hub (a session, a token): nil when nothing is.
func (h *Hub) LoadSecret(ctx context.Context, contribution string) ([]byte, error) {
	var s wire.ConnectorSecret
	if err := h.Call(ctx, ConnectorNamespace+".secrets.load", wire.ConnectorParams{Contribution: contribution}, &s); err != nil {
		return nil, err
	}
	if s.Data == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(s.Data)
}

// SaveSecret replaces what the connector keeps in the hub. Secrets go only here: never into files or logs.
func (h *Hub) SaveSecret(ctx context.Context, contribution string, data []byte) error {
	return h.Call(ctx, ConnectorNamespace+".secrets.save",
		wire.ConnectorSecretSaveParams{Contribution: contribution, Data: base64.StdEncoding.EncodeToString(data)}, nil)
}

// DeleteSecret forgets what the connector keeps in the hub (at logout).
func (h *Hub) DeleteSecret(ctx context.Context, contribution string) error {
	return h.Call(ctx, ConnectorNamespace+".secrets.delete", wire.ConnectorParams{Contribution: contribution}, nil)
}

// PutPosts keeps a feed's public posts in the knowledge base's post archive (a connector that declares archive).
func (h *Hub) PutPosts(ctx context.Context, p wire.ConnectorPostsPutParams) (wire.ConnectorPostsPut, error) {
	return callHub[wire.ConnectorPostsPut](ctx, h, ConnectorNamespace+".posts.put", p)
}

// PutComments replaces the comments of one of the connector's archived posts.
func (h *Hub) PutComments(ctx context.Context, p wire.ConnectorPostsCommentsParams) error {
	return h.Call(ctx, ConnectorNamespace+".posts.comments", p, nil)
}

// PendingPosts are the archived posts whose comments to read again.
func (h *Hub) PendingPosts(ctx context.Context, p wire.ConnectorPostsPendingParams) (wire.ConnectorPostsPending, error) {
	return callHub[wire.ConnectorPostsPending](ctx, h, ConnectorNamespace+".posts.pending", p)
}

// ArchivedChats are the chats the knowledge base has under one of the connector's sources, with how far each is.
func (h *Hub) ArchivedChats(ctx context.Context, p wire.ConnectorArchiveChatsParams) (wire.ConnectorArchivedChats, error) {
	return callHub[wire.ConnectorArchivedChats](ctx, h, ConnectorNamespace+".archive.chats", p)
}

// RecordSent tells the hub about a message the connector sent in the user's name outside the hub's write gate (a read
// tool that also replies, say), for the hub's sent-as-user ledger. The connector must declare sends_as_user in the
// manifest (connectors[].sends_as_user), else the hub answers permission_denied; the hub itself says it was this
// extension's and that it was let through on its own (auto), so p carries only where it went and what it said (Target
// is a JSON object of short values: a chat, a thread, recipients…). Call it after the send went out: a failure here
// never undoes it, so a send that cannot be recorded is still a send.
func (h *Hub) RecordSent(ctx context.Context, p wire.ConnectorSentRecordParams) error {
	return h.Call(ctx, SentNamespace+".record", p, nil)
}

// GhToken is a computer's gh token, for an extension granted gh_login. Keep it in memory only.
func (h *Hub) GhToken(ctx context.Context, p wire.ConnectorGhTokenParams) (string, error) {
	t, err := callHub[wire.ConnectorGhToken](ctx, h, ConnectorNamespace+".credentials.gh_token", p)
	return t.Token, err
}

// ChannelNamespace is the channel extension point's (docs/extensions/channels.md "4. Contributing a channel"): the hub calls
// channel.send, channel.typing and channel.edit; the program calls ChannelReceive and ChannelStatus.
const ChannelNamespace = "channel"

// ChannelReceive hands the hub one of the owner's messages (only the owner's); it returns once the hub has it, not
// after the turn. The same message id again is taken once.
func (h *Hub) ChannelReceive(ctx context.Context, contribution string, m wire.ChannelMessage) error {
	return h.Call(ctx, ChannelNamespace+".receive", wire.ChannelReceiveParams{Contribution: contribution, Message: m}, nil)
}

// ChannelStatus says how a channel without a connector is doing (wire.ChannelState*).
func (h *Hub) ChannelStatus(ctx context.Context, p wire.ChannelStatusParams) error {
	return h.Call(ctx, ChannelNamespace+".status", p, nil)
}

// CannotSend is channel.send's error while there is nowhere to send to yet (before the owner's first message): the
// hub does not try again.
func CannotSend(why string) error { return &Error{Code: wire.ChannelErrorCodeCannotSend, Message: why} }

// The knowledge base (docs/extensions/extensions.md "Knowledge base RPC"): the permission kb.read (kb.write for KBUpdate) in
// the manifest's permissions.hub, granted by the user. Errors carry wire.KBErrorCode* or permission_denied.

// KBList is one kind's entities, a page at a time (next_offset).
func (h *Hub) KBList(ctx context.Context, p wire.KBListParams) (wire.KBListResult, error) {
	return callHub[wire.KBListResult](ctx, h, "kb.list", p)
}

// KBRecent is what came in from every source since a time, newest first, a page at a time (next_offset).
func (h *Hub) KBRecent(ctx context.Context, p wire.KBRecentParams) (wire.KBRecentResult, error) {
	return callHub[wire.KBRecentResult](ctx, h, "kb.recent", p)
}

// KBGet is one entity, with its most recent messages.
func (h *Hub) KBGet(ctx context.Context, p wire.KBGetParams) (wire.KBGetResult, error) {
	return callHub[wire.KBGetResult](ctx, h, "kb.get", p)
}

// KBDay is a chat's messages of one day.
func (h *Hub) KBDay(ctx context.Context, p wire.KBDayParams) (wire.KBDayResult, error) {
	return callHub[wire.KBDayResult](ctx, h, "kb.day", p)
}

// KBSearch is the lines that match a query.
func (h *Hub) KBSearch(ctx context.Context, p wire.KBSearchParams) (wire.KBSearchResult, error) {
	return callHub[wire.KBSearchResult](ctx, h, "kb.search", p)
}

// KBUpdate changes an entity's fields (kb.write).
func (h *Hub) KBUpdate(ctx context.Context, p wire.KBUpdateParams) (wire.KBUpdateResult, error) {
	return callHub[wire.KBUpdateResult](ctx, h, "kb.update", p)
}

// KBOwner is the user's name, aliases and identities.
func (h *Hub) KBOwner(ctx context.Context) (wire.KBOwner, error) {
	return callHub[wire.KBOwner](ctx, h, "kb.owner", wire.KBOwnerParams{})
}

// ModelComplete asks a model for one answer (permission models): it runs on the extension's own purpose in Settings ›
// Model configuration, which the user chooses; no key and no budget. With JSONSchema the result's JSON has the answer
// decoded. Errors carry wire.ModelErrorCode* or permission_denied; model_paused (wire.ModelPause) is worth trying
// again after its until, model_refused (wire.ModelRefusal) is not: the same input is declined again.
func (h *Hub) ModelComplete(ctx context.Context, p wire.ModelCompleteParams) (wire.ModelCompleteResult, error) {
	return callHub[wire.ModelCompleteResult](ctx, h, "model.complete", p)
}

// The user's conversations with the assistant (docs/extensions/extensions.md "Assistant conversations"): the
// permission assistant.read, read-only, only the assistant's own space and only words.

// AssistantConversations lists the conversations last active since a time, newest first, a page at a time.
func (h *Hub) AssistantConversations(ctx context.Context, p wire.AssistantConversationsParams) (wire.AssistantConversationsResult, error) {
	return callHub[wire.AssistantConversationsResult](ctx, h, "assistant.conversations", p)
}

// AssistantMessages is one conversation's messages after a seq, in time order; continue with NextSeq while set.
func (h *Hub) AssistantMessages(ctx context.Context, p wire.AssistantMessagesParams) (wire.AssistantMessagesResult, error) {
	return callHub[wire.AssistantMessagesResult](ctx, h, "assistant.messages", p)
}

func callHub[R any](ctx context.Context, h *Hub, method string, params any) (R, error) {
	var r R
	err := h.Call(ctx, method, params, &r)
	return r, err
}
