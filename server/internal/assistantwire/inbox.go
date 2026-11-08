package assistantwire

import (
	"encoding/json"
	"strings"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// The inbox (docs/assistant/inbox.md) is the manager's: the items and their state (admission, triage, events, mutes, done,
// the AI twin, translations) and the /api/v1/inbox… routes, which the hub relays like Routes. The sources are the
// extension manager's (internal/extmgr, in the manager's process): it reads them when a round is due (its poll, a
// connector's changed, InboxCollect) and hands what it read over (MethodInboxCollected); what the inbox does with a
// source (its config, a mail body, a reply, a connector's group tools…) it asks the extension manager for on AppInbox.
// Neither way goes through the hub. These are the shapes, in their own file so the other moves merge apart.

// InboxRoutes are the inbox's routes (net/http patterns), the translation's with them (it is the manager's too): the hub
// relays the apps' requests on them to the manager as http.request, like Routes. The assistant registers exactly Routes
// and these (assistant.TestRoutesAreTheWireOnes).
var InboxRoutes = []string{
	"GET /api/v1/inbox",
	"GET /api/v1/inbox/{id}",
	"POST /api/v1/inbox/{id}/done",
	"POST /api/v1/inbox/{id}/draft",
	"POST /api/v1/inbox/{id}/reply",
	"POST /api/v1/inbox/{id}/suggestion",
	"POST /api/v1/inbox/{id}/handoff",
	"POST /api/v1/inbox/{id}/doc/status",
	"PUT /api/v1/inbox/{id}/parent",
	"GET /api/v1/inbox/tree/{kind}/{id...}",
	"POST /api/v1/inbox/refresh",
	"POST /api/v1/inbox/done",
	"POST /api/v1/inbox/bulk",
	"GET /api/v1/inbox/mutes",
	"POST /api/v1/inbox/mutes",
	"DELETE /api/v1/inbox/mutes/{id}",
	"GET /api/v1/inbox/policies",
	"POST /api/v1/inbox/policies",
	"PATCH /api/v1/inbox/policies/{id}",
	"DELETE /api/v1/inbox/policies/{id}",
	"POST /api/v1/translate",
	"POST /api/v1/translate/mail",
}

// What the extension manager asks of the manager about the inbox, and what the manager tells it (EventInboxChanged).
const (
	// MethodInboxCollected: InboxRound → {}: what one collection read of the sources. The manager makes items of it
	// (and triages, relates, files…) before it answers what it does at once; the rest goes on in its background. A
	// round older than one it applied is dropped; while it could not read what it keeps it refuses the round
	// (CodeInboxUnavailable) and the extension manager hands it again.
	MethodInboxCollected = "inbox.collected"
	// MethodInboxItem: {id} → {item, ok}: one item as it is stored (JSON), for an extension's inbox_item slot.
	MethodInboxItem = "inbox.item"
	// MethodInboxForget: {source} → {deleted}: the user deleted a source's data; its items go too.
	MethodInboxForget = "inbox.forget"
	// MethodInboxRepoMutes: InboxRepoMutes → {repos}: a source's muted repositories (GitHub's settings page lists and
	// sets them as its muted_repos); with set, they become the source's repository mutes first (add: added to them).
	MethodInboxRepoMutes = "inbox.repo_mutes"

	// EventInboxChanged (the manager tells the extension manager, one way): InboxChanged.
	EventInboxChanged = "inbox.changed"
)

// The manager asks the extension manager about the inbox's sources (AppInbox).
const (
	// InboxCollect: {round} → {}: collect now (a pull to refresh): the extension manager reads the sources and hands
	// them over (MethodInboxCollected) before it answers.
	InboxCollect = "inbox.collect"
	// InboxKick: {} → {}: a round soon (the triage got through a round with more to sort).
	InboxKick = "inbox.kick"
	// InboxConfigMethod: {} → InboxConfig.
	InboxConfigMethod = "inbox.config"
	// InboxMailBody: InboxMailRef → InboxMailMessage: a mail read in full (peeked: it stays unread).
	InboxMailBody = "inbox.mail_body"
	// InboxReplyMail: {account_id, uid, mailbox, text, send_id} → InboxSent: the reply to that mail, sent from the
	// account that got it.
	InboxReplyMail = "inbox.reply_mail"
	// InboxReply: {source, reply: connectorwire.InboxReply, send_id} → InboxSent: a reply (or a document's status moves)
	// through a connector (the source's, or the plugin's that took Feishu over).
	InboxReply = "inbox.reply"
	// InboxSettle: {source, threads} → {over: {thread: why}}: the connector's open threads its rules now leave out.
	InboxSettle = "inbox.settle"
	// InboxGroupTool: {source, call: connectorwire.GroupToolCall} → {result}: one of the connector's tools for relating a
	// batch, its answer cut for the model; the connector's coded refusal (unknown_method…) comes back as it is.
	InboxGroupTool = "inbox.group_tool"
	// InboxGroupFallback: {source, fallback: connectorwire.GroupFallback} → connectorwire.GroupFallbackResult.
	InboxGroupFallback = "inbox.group_fallback"
	// InboxGroupAppend: {source, append: connectorwire.GroupAppend, append_id} → {thing: connectorwire.InboxThing}: the same
	// append_id again (the answer lost on the way) is answered from what the connector said, not sent again.
	InboxGroupAppend = "inbox.group_append"
)

// TranslateLanguages are the languages translated into (BCP 47, as the apps' language detection names them; a
// device's translation targets are among them), with their names for the model.
var TranslateLanguages = map[string]string{
	"zh-Hans": "简体中文", "zh-Hant": "繁體中文", "en": "English", "ja": "日本語", "ko": "한국어",
	"fr": "Français", "de": "Deutsch", "es": "Español", "ru": "Русский", "pt": "Português",
}

// InboxSent is a reply's answer: Already, it had gone out before under the same send_id (a retry after the answer was
// lost, the bus dropping or the manager restarting): nothing was sent again. The extension manager keeps the send ids
// (sent_messages) for good.
type InboxSent struct {
	Already bool `json:"already,omitempty"`
}

// Error codes of the inbox on the bus.
const (
	// CodeInboxUnavailable: the inbox could not read what it keeps (the database did not answer): it takes nothing
	// and writes nothing until it can (its routes answer 503 with this code).
	CodeInboxUnavailable = "inbox_unavailable"
	// CodeAssistantInDoubt: the hub's link to the manager dropped while a request was with it: it may have been done
	// (a reply may have gone out). The apps get it with delivered "unknown"; sending the same reply again is safe.
	CodeAssistantInDoubt = "assistant_in_doubt"
)

// Rounds of InboxRound.Round.
const (
	RoundChats = "chats" // the Feishu chats only (a realtime event)
	RoundPoll  = "poll"  // every source but the slow ones
	RoundFull  = "full"  // mail too
)

// InboxRound is what one collection read: the sources it read (one that could not be read is not there and keeps
// what it had) and, on a full round, the mail. What the extension manager read while the manager could not take it
// comes with the next round that goes, merged in (a thread, an event, a mail as last read; what left a source's window
// meanwhile stays, within bounds).
type InboxRound struct {
	// Epoch names the extension manager's run and Seq orders its rounds in it: one older than a round applied is not
	// applied.
	Epoch   string        `json:"epoch,omitempty"`
	Seq     uint64        `json:"seq,omitempty"`
	Round   string        `json:"round"`
	Sources []InboxSource `json:"sources"`
	// Mail is the mail accounts read (nil: mail was not read this round, or the accounts could not be listed).
	Mail *InboxMailRead `json:"mail,omitempty"`
	// Names are the connectors the extension manager has now, by id (a source that is gone keeps no held-back count;
	// mutes are listed with these names).
	Names map[string]string `json:"names,omitempty"`
}

// InboxSource is one connector's things as a round read them. Things is empty for one that is switched off for the
// inbox, not signed in or waiting for the triage model (Off): it was read, and nothing of it waits.
type InboxSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Off  bool   `json:"off,omitempty"`
	// Extension: a plugin provides it (installed, or hub.json's; not a built-in one's): its new items are triaged
	// before they enter, and without a model and a fallback they wait.
	Extension bool `json:"extension,omitempty"`
	// Migrated: it took one of the hub's own connectors over (connectorwire.MigratableConnectors): its things with extra
	// keep the items' own blocks.
	Migrated bool `json:"migrated,omitempty"`
	// Group: it relates batches of new messages to events (group.tool, group.fallback); Meta: it writes the events'
	// meta itself (group.append), described by MetaDescription.
	Group           bool   `json:"group,omitempty"`
	Meta            bool   `json:"meta,omitempty"`
	MetaDescription string `json:"meta_description,omitempty"`
	// Settles: it closes open items its rules now leave out (InboxSettle).
	Settles bool `json:"settles,omitempty"`
	// Kinds are its kinds of things as it describes them, for the models.
	Kinds  map[string]string          `json:"kinds,omitempty"`
	Things []connectorwire.InboxThing `json:"things"`
	// ReadError: its Collect failed this round (Things is empty and means nothing; what it had stays): a short code,
	// never the error's text (ReadTimeout, ReadRateLimited, ReadFailed). The apps say which source is behind.
	ReadError string `json:"read_error,omitempty"`
}

// Why a source could not be read (InboxSource.ReadError).
const (
	ReadTimeout     = "timeout"
	ReadRateLimited = "rate_limited"
	ReadFailed      = "failed"
)

// InboxMailRead is a round's mail: each account's latest inbox page (an account that could not be read is left out).
type InboxMailRead struct {
	Accounts []InboxMailAccount `json:"accounts"`
}

// InboxMailAccount is one mail account and its latest messages.
type InboxMailAccount struct {
	ID       string             `json:"id"`
	Email    string             `json:"email"`
	Messages []InboxMailSummary `json:"messages"`
}

// MailAddress is a mail's sender or recipient (mail.Address).
type MailAddress struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// InboxMailSummary is one message of a listing (mail.MailSummary's fields the inbox reads).
type InboxMailSummary struct {
	Mailbox   string        `json:"mailbox"`
	UID       uint32        `json:"uid"`
	MessageID string        `json:"message_id"`
	Subject   string        `json:"subject"`
	From      MailAddress   `json:"from"`
	To        []MailAddress `json:"to"`
	Date      string        `json:"date"` // RFC3339, UTC
	Snippet   string        `json:"snippet"`
	Seen      bool          `json:"seen"`
}

// InboxMailRef names one mail. Readable: a mail without a text part has its HTML as readable text (for translation:
// paragraphs and links kept), else as plain text.
type InboxMailRef struct {
	AccountID string `json:"account_id"`
	UID       uint32 `json:"uid"`
	Mailbox   string `json:"mailbox"`
	Readable  bool   `json:"readable,omitempty"`
}

// InboxMailMessage is one mail read in full: Text is its text part, else its HTML as text.
type InboxMailMessage struct {
	From    MailAddress   `json:"from"`
	To      []MailAddress `json:"to"`
	Cc      []MailAddress `json:"cc"`
	Date    string        `json:"date"`
	Subject string        `json:"subject"`
	Text    string        `json:"text"`
}

// InboxConfig is what the inbox goes by of the extension manager's (manager.json's inbox section, its settings, the
// devices' mail languages it gets from the hub): asked for when it is needed, kept a few seconds.
type InboxConfig struct {
	// Rules are the user's own triage rules (setting inbox.rules, else manager.json's inbox.rules); AgentRules guide the
	// judge linking a Needs you item to one of the user's tasks (inbox.agent_rules); what the AI twin takes on is set
	// by inbox policies (docs/assistant/inbox.md "Policies").
	Rules      string           `json:"rules,omitempty"`
	AgentRules string           `json:"agent_rules,omitempty"`
	Agent      InboxAgentConfig `json:"agent"`
	// LarkAccount is the default Feishu account, the gateway account Feishu items came to unless they say another: the
	// Feishu extension's default (GET accounts of its messaging source), else hub.json's lark.account (#5098).
	LarkAccount int `json:"lark_account,omitempty"`
	// Translate are the languages some device wants mail translated into (Settings › Translation).
	Translate []string `json:"translate,omitempty"`
	// Extensions are the connectors a plugin provides (installed, or hub.json's), whether or not a round read them:
	// their items have no fallback tier (a model sorts them, or they wait), from the manager's start on.
	Extensions []string `json:"extensions,omitempty"`
}

// InboxAgentConfig is manager.json's inbox.agent: the AI twin (docs/assistant/inbox.md).
type InboxAgentConfig struct {
	Enabled bool `json:"enabled"`
	// Machine and Launcher run the agent; default: the assistant's.
	Machine  string `json:"machine,omitempty"`
	Launcher string `json:"launcher,omitempty"`
	// Concurrency is how many agents work at once (default 1).
	Concurrency int `json:"concurrency,omitempty"`
	// TimeoutMinutes gives up on an agent that has not drafted by then (default 15).
	TimeoutMinutes int `json:"timeout_minutes,omitempty"`
	// MaxAgeHours: items whose newest message is older are not handed over (default 24), so turning this on does
	// not start an agent for every old item.
	MaxAgeHours int `json:"max_age_hours,omitempty"`
	// Rules is manager.json's inbox.agent.rules: no longer read (the judge only links items to tasks; what the twin takes
	// on is set by inbox policies). Kept so older hub.json files still parse.
	Rules string `json:"rules,omitempty"`
}

// InboxRepoMutes is MethodInboxRepoMutes's params.
type InboxRepoMutes struct {
	Source string `json:"source"`
	// Set: Repos become the source's repository mutes (the others are unmuted), or are added to them (Add).
	Set   bool     `json:"set,omitempty"`
	Add   bool     `json:"add,omitempty"`
	Repos []string `json:"repos,omitempty"`
}

// InboxChanged is EventInboxChanged's params: the items that changed and were stored (the extension manager tells the
// extensions, inbox.changed, and the apps through the hub, {"type":"inbox"}); none: only what the apps see moved (the
// triage status).
type InboxChanged struct {
	Items []InboxChangedItem `json:"items"`
}

// InboxChangedItem is one changed item as the extensions' inbox.changed event names it.
type InboxChangedItem struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
}

// InboxReport is what the hub shows of the inbox from the snapshot: the conversations the AI twin runs in (their
// finished turn is no push: the draft in the inbox is the result).
type InboxReport struct {
	Agents []SessionRef `json:"agents"`
}

// InboxItemResult is MethodInboxItem's answer.
type InboxItemResult struct {
	Item json.RawMessage `json:"item,omitempty"`
	OK   bool            `json:"ok"`
}

// AsksAgent: text posted as the user would come back as their request to start an agent (a plugin's /agent:
// "/agent", then nothing, a space or a colon; any case).
func AsksAgent(text string) bool {
	rest, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(text)), "/agent")
	return ok && (rest == "" || strings.HasPrefix(rest, " ") || strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, "：") || strings.HasPrefix(rest, "\n"))
}
