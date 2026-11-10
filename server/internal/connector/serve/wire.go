package serve

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// Methods the hub calls.
const (
	MInit        = "init" // InitParams → Manifest: build the connector and start it
	MManifest    = "manifest"
	MStatus      = "status"
	MLoginStep   = "login.step"
	MLogout      = "logout"
	MChanges     = "archive.changes"
	MCollect     = "inbox.collect"
	MReply       = "inbox.reply"
	MCatalog     = "catalog.list"
	MCatalogCall = "catalog.call"
	MTools       = "tools.list"
	MCall        = "tools.call"
	MActions     = "actions.list"
	MDescribe    = "actions.describe"
	MDo          = "actions.do" // only once the hub's gate let it through
	MRecall      = "recall"     // legacy recall: kept for programs and hubs before recall.can / recall.do
	MRecallCan   = "recall.can"
	MRecallDo    = "recall.do" // only once the user agreed on their own full device
	MThread      = "thread.page"
	MMedia       = "thread.media"
	// The knowledge base's items (connector.Items) and an archived message's attachment (connector.ArchiveMedia).
	MItems        = "items.list"
	MDocTitle     = "items.doc_title"
	MArchiveMedia = "archive.media"
	// The live state for the assistant's snapshot (connector.Snapshot): SnapshotParams → connector.SnapshotData.
	MSnapshot = "snapshot.get"
	// History import (connector.History, connector.HistoryAnchors; docs/protocol/connector-protocol.md "History"),
	// read-only: the conversations to import, one page of one of them, and the owner's messages to read around.
	MHistoryChats   = "history.chats"   // {} → HistoryChatsResult
	MHistory        = "archive.history" // HistoryParams → connectorwire.HistoryPage
	MHistoryAnchors = "history.anchors" // HistoryAnchorsParams → connectorwire.HistoryAnchors
	// Relating new messages to events (connector.Grouper): connector.GroupToolCall → any JSON,
	// connector.GroupFallback → connector.GroupFallbackResult; with meta (connector.EventWriter)
	// connector.GroupAppend → connector.InboxThing.
	MGroupTool     = "group.tool"
	MGroupAppend   = "group.append"
	MGroupFallback = "group.fallback"
	// The user's own settings of the connector (connector.Options): {} → OptionsResult, OptionsParams → {}.
	MOptionsGet = "options.get"
	MOptionsSet = "options.set"
	// Closing inbox items the hub still has open (connector.InboxSettler): SettleParams → SettleResult.
	MSettle = "inbox.settle"
	// One of the user's computers connected (connector.Machines): MachineParams → {}.
	MMachineOnline = "machines.online"
	// Where the connector's sign-in comes from, the hub keeping the user's choice (GitHub's "Authorized from",
	// docs/protocol/connector-protocol.md): {} → AuthView, AuthSource → AuthView.
	MAuthGet = "auth.get"
	MAuthSet = "auth.set"
)

// Methods the program calls on the hub, and its event.
const (
	MSecretsLoad   = "secrets.load"
	MSecretsSave   = "secrets.save"
	MSecretsDelete = "secrets.delete"
	// The knowledge base (connector.KnowledgeBase): PostsPutParams → PostsPutResult, PostsPendingParams →
	// PostsPendingResult, ArchiveChatsParams → ArchiveChatsResult.
	MPostsPut      = "posts.put"
	MPostsComments = "posts.comments"
	MPostsPending  = "posts.pending"
	MArchiveChats  = "archive.chats"
	EChanged       = "changed" // ChangedEvent
	// A computer's gh login (GitHub's token there), for a program the hub granted gh_login: GhTokenParams →
	// GhTokenResult. Errors carry the Code* below.
	MGhToken = "credentials.gh_token"
)

// The knowledge base's calls on the wire.
type (
	PostsPutParams struct {
		Posts []connector.PostPut `json:"posts"`
	}
	PostsPutResult struct {
		Statuses []string `json:"statuses"` // per post: connector.PostNew, PostChanged, PostSame, PostRefused
	}
	PostsCommentsParams struct {
		ID           string                  `json:"id"`
		CommentCount int                     `json:"comment_count"`
		Comments     []connector.PostComment `json:"comments"`
	}
	SnapshotParams struct {
		TZ string `json:"tz,omitempty"` // the user's time zone ("" the program's own)
	}
	PostsPendingParams struct {
		Limit int `json:"limit,omitempty"`
	}
	PostsPendingResult struct {
		Posts []connector.PendingPost `json:"posts"`
	}
	ArchiveChatsParams struct {
		Source string `json:"source,omitempty"` // "" is the connector's id
	}
	ArchiveChatsResult struct {
		Chats []connector.ArchivedChat `json:"chats"`
	}
)

// Namespace is the connector extension point's (docs/extensions/extensions.md): under the plugin protocol every method above is
// Namespace + "." + method, with the connector's id as the contribution.
const Namespace = "connector"

// InPlugin is a method's name under the plugin protocol.
func InPlugin(method string) string { return Namespace + "." + method }

// Protocol is the version of this protocol (docs/protocol/connector-protocol.md): the hub sends it with init, the program
// answers with its own in the manifest, and the hub refuses a program of another version.
const Protocol = 2

// Outcomes recall.do reports. Only done and already_recalled mean the message is known to be gone.
const (
	RecallDone            = "done"
	RecallAlreadyRecalled = "already_recalled"
	RecallNotRecallable   = "not_recallable"
	RecallUnavailable     = "unavailable"
	RecallRefused         = "refused"
	RecallUnknown         = "unknown"
)

// Manifest is who the connector is and its connector.Manifest, on the wire.
type Manifest struct {
	Protocol     int      `json:"protocol"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Capabilities []string `json:"capabilities"`
	Hosts        []string `json:"hosts,omitempty"`
	AgentTools   []string `json:"agent_tools,omitempty"`
	Unlisted     []string `json:"unlisted,omitempty"`
	// ConfirmAlways: see connector.Manifest.
	ConfirmAlways []string `json:"confirm_always,omitempty"`
	PollSeconds   int      `json:"poll_seconds,omitempty"`
	Kinds         []string `json:"kinds,omitempty"`
	// ArchiveSources: connector.Manifest.ArchiveSources.
	ArchiveSources []string `json:"archive_sources,omitempty"`
	// MetaDescription: connector.Manifest.MetaDescription.
	MetaDescription string `json:"meta_description,omitempty"`
	// CLI: connector.Manifest.CLI.
	CLI string `json:"cli,omitempty"`
}

type (
	InitParams struct {
		Protocol int             `json:"protocol"`
		Config   json.RawMessage `json:"config,omitempty"`
	}
	LoginParams struct {
		Step   string            `json:"step,omitempty"`
		Values map[string]string `json:"values,omitempty"`
	}
	ChangesParams struct {
		Cursor string `json:"cursor,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}
	ChangesResult struct {
		Batches []connector.ChatBatch `json:"batches,omitempty"`
		Next    string                `json:"next,omitempty"`
		More    bool                  `json:"more,omitempty"`
	}
	// HistoryChatsResult is history.chats' answer.
	HistoryChatsResult struct {
		Chats []connectorwire.HistoryChat `json:"chats"`
	}
	CollectResult struct {
		Things []connector.InboxThing `json:"things,omitempty"`
	}
	// ReplyParams is the reply as the connector gets it.
	ReplyParams = connector.InboxReply
	ToolsResult struct {
		Tools []proto.Tool `json:"tools,omitempty"`
	}
	CallParams struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args,omitempty"`
	}
	CallResult struct {
		Text string `json:"text,omitempty"`
		// MessageRef is actions.do's: the id of the message the write made on the other side
		// (connector.DoResult.MessageRef), when the connector knows one.
		MessageRef string `json:"message_ref,omitempty"`
	}
	// DescribeResult is what the card says about a write (connector.Actions.Describe).
	DescribeResult struct {
		Action  connector.Action `json:"action"`
		Summary string           `json:"summary"`
	}
	ThreadParams struct {
		Thread string `json:"thread"`
		Before string `json:"before,omitempty"`
		Limit  int    `json:"limit,omitempty"`
	}
	MediaParams struct {
		Ref string `json:"ref"`
	}
	// ItemsResult is the knowledge base's items (connector.Items: documents, meetings, events).
	ItemsResult struct {
		Items []kbwire.Item `json:"items"`
	}
	DocTitleParams struct {
		URL string `json:"url"`
	}
	DocTitleResult struct {
		Title string `json:"title"`
	}
	// DoParams is a write the gate let through: the very args the card was made from, and the hub's id for this one
	// run, the same if the hub ever sends it again (the program runs it at most once).
	DoParams struct {
		Name      string          `json:"name"`
		Args      json.RawMessage `json:"args,omitempty"`
		RequestID string          `json:"request_id"`
	}
	// RecallParams is a message to take back (connector.Recaller): what the ledger row of the send says, none of what the
	// message said. At is when the row was written, right after the send.
	RecallParams struct {
		Action     string         `json:"action"`
		MessageRef string         `json:"message_ref"`
		Target     map[string]any `json:"target,omitempty"`
		At         time.Time      `json:"at,omitzero"`
	}
	// RecallResult: Recalled true is the legacy program's word that the service confirmed the message is no longer there.
	RecallResult struct {
		Recalled bool `json:"recalled"`
	}
	// RecallCanParams is a read-only, memory-only question about one kind of sent message.
	RecallCanParams struct {
		Action     string `json:"action"`
		AgeSeconds int64  `json:"age_seconds"`
	}
	RecallCanResult struct {
		Can bool `json:"can"`
	}
	// RecallDoParams adds a stable operation id to the legacy fields. At stays because a connector's own recall window
	// counts from the send; the program still gets none of the message's text or the rest of the ledger.
	RecallDoParams struct {
		RequestID  string         `json:"request_id"`
		Action     string         `json:"action"`
		MessageRef string         `json:"message_ref"`
		Target     map[string]any `json:"target,omitempty"`
		At         time.Time      `json:"at,omitzero"`
	}
	RecallDoResult struct {
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	SecretData struct {
		Data []byte `json:"data,omitempty"` // nil: nothing stored
	}
	ChangedEvent struct {
		Contribution string `json:"contribution,omitempty"` // under the plugin protocol: the connector's id
		Scope        string `json:"scope"`
	}
)

// The history queries go on the wire as they are (with the contribution under the plugin protocol).
type (
	HistoryParams        = connectorwire.HistoryQuery        // archive.history's
	HistoryAnchorsParams = connectorwire.HistoryAnchorsQuery // history.anchors'
)

// Error codes a program answers with (rpc.Error.Code), for callers that act on the kind of failure.
const (
	// Why credentials.gh_token has no token (the GitHub connector's errors of the same names).
	CodeNoMachine      = "no_machine"
	CodeOffline        = "offline"
	CodeNodeTooOld     = "node_too_old"
	CodeNoGh           = "no_gh"
	CodeNotSignedIn    = "not_signed_in"
	CodeNotPermitted   = "permission_denied"
	CodeNotInitialized = "not_initialized"
	CodeNoCapability   = "no_capability"
	CodeUnknownMethod  = "unknown_method"
	CodeWrongProtocol  = "wrong_protocol"
	// CodeRateLimited: tools.call / actions.do waits out a rate limit; data {retry_after} is how many seconds.
	CodeRateLimited = "rate_limited"
	// What recall answers besides those and CodeNotSignedIn / CodeOffline (it could not act now): nothing was sent
	// because the program cannot take that message back, or the request may have taken effect.
	CodeNotRecallable = "not_recallable"
	CodeRecallUnknown = "recall_unknown"
)

// ItemOf is a connector's manifest as a contribution to the connectors extension point.
func ItemOf(m Manifest) extension.ConnectorItem {
	return extension.ConnectorItem{ID: m.ID, Name: m.Name, Capabilities: m.Capabilities, Hosts: m.Hosts, AgentTools: m.AgentTools,
		Unlisted: m.Unlisted, ConfirmAlways: m.ConfirmAlways, PollSeconds: m.PollSeconds, Kinds: m.Kinds, ArchiveSources: m.ArchiveSources,
		MetaDescription: m.MetaDescription, CLI: m.CLI}
}

// ManifestOf is c's manifest on the wire.
func ManifestOf(c connector.Connector) Manifest {
	cm := c.Manifest()
	m := Manifest{Protocol: Protocol, ID: c.ID(), Name: c.Name(), Capabilities: []string{}, Hosts: cm.Hosts, AgentTools: cm.AgentTools,
		Unlisted: cm.Unlisted, ConfirmAlways: cm.ConfirmAlways, PollSeconds: int(cm.Poll / time.Second), Kinds: cm.Kinds,
		ArchiveSources: cm.ArchiveSources, MetaDescription: cm.MetaDescription, CLI: cm.CLI}
	for _, cp := range cm.Capabilities {
		m.Capabilities = append(m.Capabilities, string(cp))
	}
	return m
}

// Of is the connector.Manifest a program sent (a proxy's: it answers for the program).
func (m Manifest) Of() connector.Manifest {
	out := connector.Manifest{Hosts: m.Hosts, AgentTools: m.AgentTools, Unlisted: m.Unlisted, ConfirmAlways: m.ConfirmAlways,
		Poll: time.Duration(m.PollSeconds) * time.Second, Kinds: m.Kinds, ArchiveSources: m.ArchiveSources,
		MetaDescription: m.MetaDescription, CLI: m.CLI, Proxy: true}
	for _, cp := range m.Capabilities {
		out.Capabilities = append(out.Capabilities, connector.Capability(cp))
	}
	return out
}

// Auther is a connector whose sign-in comes from where the user picks, the hub keeping the choice (auth.get /
// auth.set): GitHub's computer or device flow.
type Auther interface {
	Auth(ctx context.Context) AuthView
	SetAuth(ctx context.Context, a AuthSource) (AuthView, error)
}

// OptionsResult is options.get's answer; OptionsParams is options.set's.
type OptionsResult struct {
	Options json.RawMessage `json:"options,omitempty"`
}

type OptionsParams struct {
	Options json.RawMessage `json:"options"`
}

// SettleParams and SettleResult are inbox.settle's: the open items' threads, and why each that is over is.
type SettleParams struct {
	Threads []string `json:"threads"`
}

type SettleResult struct {
	Settled map[string]string `json:"settled,omitempty"`
}

// MachineParams is machines.online's: the machine that connected.
type MachineParams struct {
	Machine string `json:"machine"`
}

// AuthSource is the user's choice of where the sign-in comes from: Source "machine" (a computer's gh login,
// Machine its id and MachineName what people call it) or "device" (the device flow).
type AuthSource struct {
	Source      string `json:"source"`
	Machine     string `json:"machine,omitempty"`
	MachineName string `json:"machine_name,omitempty"`
}

// AuthView is auth.get's and auth.set's answer: the choice in effect, and what the token in use may do (Scopes,
// once ScopesKnown).
type AuthView struct {
	Source      string   `json:"source"`
	Machine     string   `json:"machine,omitempty"`
	Scopes      []string `json:"scopes,omitempty"`
	ScopesKnown bool     `json:"scopes_known"`
}

// GhTokenParams and GhTokenResult are credentials.gh_token's.
type GhTokenParams struct {
	Machine string `json:"machine"`
	Host    string `json:"host,omitempty"`
}

type GhTokenResult struct {
	Token string `json:"token"`
}
