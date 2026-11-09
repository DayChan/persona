package assistantwire

import (
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kbwire"
)

// The knowledge base is the assistant's (docs/assistant/knowledge.md "Who owns what"): its engine and loop, its files under the data
// directory and its index rows. The hub keeps the sources: the assistant reads what the connectors deliver through
// app.call connectors.* (data in, data out; the cursors are the assistant's, so nothing is lost while it is away), and
// the hub's own features that read the knowledge base ask the assistant (kb.*), as the posts the connectors' programs
// hand the hub go to it. What the hub sees happen goes to it as events.

// What the hub's connectors offer the knowledge base (app.call connectors.*).
const (
	// ConnectorsArchives: {} → Archives: the connectors whose conversations go into the knowledge base now (signed in,
	// "Add to knowledge base" on), and those with documents, meetings and events.
	ConnectorsArchives = "connectors.archives"
	// ConnectorsChanges: {id, cursor, limit} → {batches: [connectorwire.ChatBatch], next, more}: what moved in a connector's
	// account since cursor ("" the first time), and the cursor to go on from (connector.Archive).
	ConnectorsChanges = "connectors.changes"
	// ConnectorsArchiveMedia: {id, ref} → {media: connectorwire.Media}: an archived message's attachment, at most
	// connectorwire.ArchiveMediaMost bytes (connector.ArchiveMedia).
	ConnectorsArchiveMedia = "connectors.archive_media"
	// ConnectorsItems: {id} → {items: [kbwire.Item]}: a connector's documents, meetings and events (connector.Items).
	ConnectorsItems = "connectors.items"
	// ConnectorsDocTitle: {id, url} → {title}: a document's title ("" when the connector cannot tell).
	ConnectorsDocTitle = "connectors.doc_title"
)

// Archives is connectors.archives' answer.
type Archives struct {
	Archives []ArchiveSource `json:"archives"`
	Items    []string        `json:"items"` // the ids of the connectors with documents, meetings and events
}

// ArchiveSource is a connector whose conversations the knowledge base reads now.
type ArchiveSource struct {
	ID string `json:"id"`
	// PollMS is how often it wants to be asked (its manifest's Poll); 0: the knowledge base's own default.
	PollMS int64 `json:"poll_ms,omitempty"`
	// Sources are the other names its batches may be filed under: its manifest's ArchiveSources, minus the ids of the
	// other connectors (one connector never writes into another's chats).
	Sources []string `json:"sources,omitempty"`
	// Media: it hands over its messages' attachments (connectors.archive_media).
	Media bool `json:"media,omitempty"`
}

// ChangesResult is connectors.changes' answer.
type ChangesResult struct {
	Batches []connectorwire.ChatBatch `json:"batches"`
	Next    string                    `json:"next"`
	More    bool                      `json:"more,omitempty"`
}

// The hub asks the knowledge base (calls), and tells it (events).
const (
	// MethodKBContext: {chat_id} → {context: kbwire.ChatContext | null}: what the knowledge base knows about one chat
	// and its first three projects (the inbox's AI twin starts with it); null without such a chat.
	MethodKBContext = "kb.context"
	// MethodKBProjects: {machine_id, cwd} → {projects: [KBProject]}: the projects whose code directories hold cwd on
	// that machine (an agent's turn is triaged with their names).
	MethodKBProjects = "kb.projects"
	// MethodKBForget: KBForget → KBForgotten: a connector's knowledge content goes, with its index rows, project
	// associations and cursors ("Delete saved data").
	MethodKBForget = "kb.forget"
	// The posts a connector's program hands the hub (connector.KnowledgeBase, docs/protocol/connector-protocol.md "Knowledge base"),
	// for that connector (KBPosts.Connector; the hub checked it may write now):
	//   kb.posts_put {connector, sources, posts} → {statuses}
	//   kb.posts_comments {connector, id, count, comments} → {}
	//   kb.posts_pending {connector, limit} → {posts: [connectorwire.PendingPost]}
	//   kb.archived_chats {connector, sources, source} → {chats: [connectorwire.ArchivedChat]}
	MethodKBPostsPut      = "kb.posts_put"
	MethodKBPostsComments = "kb.posts_comments"
	MethodKBPostsPending  = "kb.posts_pending"
	MethodKBArchivedChats = "kb.archived_chats"

	// EventKBKick: {}: a source has something new (a connector's realtime event, a feed turned on): go round soon.
	EventKBKick = "kb.kick"
	// EventKBAgentTurn: KBAgentTurn: an agent finished a turn (after the hub's done delay, not the assistant's own
	// conversations); the projects of its directory get a timeline line.
	EventKBAgentTurn = "kb.agent_turn"
)

// KBSettings is the knowledge base's settings the manager goes by: its manager.json's kb section, laid over the hub's
// hello (an older hub still sends its hub.json's). Its ai is the models' (HubModels.KBAI).
type KBSettings struct {
	ReadOnly       bool     `json:"read_only,omitempty"`       // serve local archives without collection, models or writes
	Off            bool     `json:"off,omitempty"`             // kb.enabled false: no knowledge base at all
	TextDays       int      `json:"text_days,omitempty"`       // 0: 365
	AttachmentDays int      `json:"attachment_days,omitempty"` // 0: 30
	ExtraDirs      []string `json:"extra_dirs,omitempty"`      // as written; "~/" is the assistant's home
	// AttachmentDaysBySource: attachment_days for the chats of one source (kb.attachment_days_by_source); the sources
	// not in it, and non-positive values, keep attachment_days.
	AttachmentDaysBySource map[string]int `json:"attachment_days_by_source,omitempty"`
}

// CodeKBUnavailable: the knowledge base is not open yet (the user's time zone not known yet, another assistant process
// has the data directory); the hub answers it like assistant_offline (503, try again).
const CodeKBUnavailable = "kb_unavailable"

// CodeKBReadOnly refuses edits and ingestion while local archive reads remain available.
const CodeKBReadOnly = "kb_read_only"

// CodeKBDisabled means the manager has disabled knowledge; retrying cannot enable it.
const CodeKBDisabled = "kb_disabled"

// KBProject is a project as kb.projects names it.
type KBProject struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// KBForget identifies the connector and the additional sources its archive may write to.
// ArchiveSources comes from the registered manifest, never from a client deletion request.
type KBForget struct {
	Source         string   `json:"source"`
	ArchiveSources []string `json:"archive_sources,omitempty"`
}

// KBForgotten is kb.forget's answer: how many entities were removed.
type KBForgotten struct {
	Chats  int `json:"chats"`
	People int `json:"people"`
	Posts  int `json:"posts"`
	Items  int `json:"items"`
}

// KBContextResult is kb.context's answer.
type KBContextResult struct {
	Context *kbwire.ChatContext `json:"context"`
}

// KBPosts is the params of the kb.posts_* and kb.archived_chats calls (each takes the fields its line above names).
type KBPosts struct {
	Connector string `json:"connector"`
	// Sources are the names the connector's conversations are filed under: its id, then its manifest's ArchiveSources.
	Sources  []string                    `json:"sources,omitempty"`
	Posts    []connectorwire.PostPut     `json:"posts,omitempty"`
	ID       string                      `json:"id,omitempty"`
	Count    int                         `json:"count,omitempty"`
	Comments []connectorwire.PostComment `json:"comments"`
	Limit    int                         `json:"limit,omitempty"`
	Source   string                      `json:"source,omitempty"`
}

// KBAgentTurn is kb.agent_turn's params: the agent, where it works, and what its turn comes to (the hub's verdict
// when it found it done, else its last words).
type KBAgentTurn struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Title     string `json:"title,omitempty"`
	Line      string `json:"line"`
}

// KBItems is connectors.items' answer.
type KBItems struct {
	Items []kbwire.Item `json:"items"`
}
