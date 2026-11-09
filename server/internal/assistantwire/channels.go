package assistantwire

import (
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the channels: the Feishu bot and the chat app bots (docs/extensions/channels.md, docs/extensions/bots.md) ----

// Which conversation a channel talks to, following the turn and sending its answer back, what is asked there and its
// answer, the commands, the pushes and the sync mirror are the assistant's; the hub keeps the transport — the Feishu
// open platform's long connection and REST (pairing among them), the plugins' channel programs — and hands the
// assistant what comes in. The channels' routes are the assistant's (Routes) but binding (POST …/bind: credentials,
// sign-in fields, pairing): no secret of a channel or the bot passes through the assistant. /api/v1/bots/feishu* stays
// the hub's. What answers a card in a channel carries the owner's action (OwnerAction): the hub issued it when it
// handed the owner's press or message over, and takes it once — the assistant can't approve anything on its own.
const (
	// MethodChannelReceive: ChannelReceive → {}: the owner's message from a plugin's channel, taken (not its turn).
	MethodChannelReceive = "channels.receive"
	// MethodChannelUnbound: ChannelUnbound → {}: clear the channel's pending turns and text questions before
	// signing out finishes, including a connector that revoked its own authorization.
	MethodChannelUnbound = "channels.unbound"
	// MethodFeishuMessage: FeishuMessage → {}: the owner's message in the Feishu bot's private chat, taken.
	MethodFeishuMessage = "feishu.message"
	// MethodFeishuAction: FeishuAction → the card callback's answer (feishubot.CardResponse): the owner pressed one
	// of the bot's cards. Feishu waits 3 seconds for it.
	MethodFeishuAction = "feishu.card_action"
	// MethodFeishuView: {} → FeishuView, what GET /api/v1/bots/feishu shows of the bot's chat.
	MethodFeishuView = "feishu.view"

	// EventInteractionAnnounced: InteractionAnnounced, one way: a new card (not a quiet task's).
	EventInteractionAnnounced = "interaction.announced"
	// EventInteractionEnded: InteractionEnded, one way: a card is over — answered (with the resolution), withdrawn,
	// expired.
	EventInteractionEnded = "interaction.ended"
	// EventSessionItems and EventSessionDelta: SessionItems / SessionDelta, one way: a conversation the assistant
	// follows (DeviceSessionFollow) has new items, or is writing text.
	EventSessionItems = "session.items"
	EventSessionDelta = "session.delta"
	// EventFeishuOwner: FeishuOwner, one way: the Feishu bot was paired with its owner, or unpaired.
	EventFeishuOwner = "feishu.owner"
	// EventFeishuRecalled: FeishuRecalled, one way: the owner recalled a message in the bot's private chat.
	EventFeishuRecalled = "feishu.recalled"
	// EventChannelNoted: ChannelNoted, one way: a channel's message went into a conversation through the apps' routes
	// (a voice conversation's errand: client_msg_id voice-…).
	EventChannelNoted = "channels.noted"

	// The hub's device methods for the channels' turns.
	// DeviceSessionFollow: {session_id} → {}: the conversation's new items and the text it writes come as
	// EventSessionItems / EventSessionDelta until DeviceSessionUnfollow, or until the assistant disconnects.
	DeviceSessionFollow   = "session.follow"
	DeviceSessionUnfollow = "session.unfollow"
	// DeviceSessionSendOnce: the node's session.send params (with client_msg_id) → {}: sent under the apps' once-only
	// guard — a message id already delivered is not sent again, one whose send was cut off is refused.
	DeviceSessionSendOnce = "session.send_once"

	// AppFeishu is the Feishu bot's transport (the open platform, as the paired owner's bot).
	AppFeishu = "feishu"
	// FeishuStatusGet: {} → FeishuStatus.
	FeishuStatusGet = "feishu.status"
	// FeishuPost: {reply_to?, card, fallback?, uuid?, no_detour?} → {message_id}: a card (JSON 2.0) as a reply, else
	// into the owner's chat; one Feishu refuses goes again as rich text (fallback, Markdown). uuid is the caller's key
	// for a send that may be made again (a retried morning brief): the same key lands once, and a reply under it is
	// not dropped as a repeat of another with the same words. It may be made of letters, digits, ":", "_" and "-" only
	// (anything else is ignored, as if none was given) and is cut to 46 characters; without one a reply's key is made
	// of its parent and content. no_detour? makes a reply whose parent was withdrawn or deleted fail (feishubot.MessageGone
	// tells why) in place of going into the chat as a message of its own.
	// chat_id? names another chat to send into (and to fall back to when the parent was withdrawn) and in_thread? puts
	// a reply in the parent's thread; the hub refuses any chat but the owner's private chat and the group chats the
	// owner opted in, and with neither field the call is what it always was.
	FeishuPost = "feishu.post"
	// FeishuText: {reply_to?, text, chat_id?, in_thread?, uuid?, no_detour?} → {message_id}: plain text as a reply,
	// else into the owner's chat; chat_id, in_thread, uuid (cut to 50 characters here) and no_detour are FeishuPost's.
	// One more place is open to it (#6146): a reply to a message the hub took as the owner's "@bot /ai …" in a group that
	// is not opted in (FeishuFromElsewhere), which lands under that message and nowhere else in the group (at most
	// 2000 characters, never a detour into the chat); a send into that group, or a reply to any other message in it,
	// is refused as ever.
	FeishuText = "feishu.text"
	// FeishuUpdateCard: {message_id, card} → {}.
	FeishuUpdateCard = "feishu.update_card"
	// FeishuPin: {message_id, exclusive?} → {}: one of the bot's messages in the owner's private chat pinned there;
	// exclusive takes off the other pins the bot made in that chat (never the owner's own) (#1961, the workbench).
	FeishuPin = "feishu.pin"
	// FeishuRecall: {message_id} → {}: one of the bot's messages in the owner's private chat withdrawn (Lark lets a
	// bot withdraw its messages for a while after sending; past that the call fails).
	FeishuRecall = "feishu.recall"
	// FeishuCotCreate: {reply_to?} → {cot_id, message_id}: an agent message (the open platform's message_cot, a
	// collapsible process) under the message replied to, else into the owner's chat.
	FeishuCotCreate = "feishu.cot_create"
	// FeishuCotAppend: {cot_id, message_id, events: [feishubot.CotEvent]} → {}.
	FeishuCotAppend = "feishu.cot_append"
	// FeishuCotComplete: {cot_id, message_id, reason: done | error} → {}: one that will not get its RUN_FINISHED.
	FeishuCotComplete = "feishu.cot_complete"
	// FeishuReact: {message_id, emoji} → {reaction_id}; FeishuUnreact: {message_id, reaction_id} → {}.
	FeishuReact   = "feishu.react"
	FeishuUnreact = "feishu.unreact"
	// FeishuMessages: {message_id} → {items: [feishubot.Item]}: a message read back (a quote; a merged forward with
	// the messages in it).
	FeishuMessages = "feishu.messages"
	// FeishuResource: {message_id, key, kind} → {data, mime}: an image of a message the bot received (at most 3 MB).
	FeishuResource = "feishu.resource"
	// FeishuSave: {message_id, key, kind, name, machine_id} → {path}: a file (or a large image) of a message saved on
	// a machine (its temp directory, like the apps' uploads), at most 50 MB.
	FeishuSave = "feishu.save"
	// FeishuUserName: {open_id} → {name}: a user's name (the Chinese for "someone" when it can't be read; the owner's falls back to hub.json's).
	FeishuUserName = "feishu.user_name"
	// FeishuConfigure: {enabled?, features?: {id: on}} → {}: the bot switched, its feature switches changed (only
	// those given).
	FeishuConfigure = "feishu.configure"
	// FeishuTest: {} → {}: the test card to the owner.
	FeishuTest = "feishu.test"

	// AppChannels is the plugins' channel programs, and the list of every channel as the transport has it.
	AppChannels = "channels"
	// ChannelsList: {} → {channels: [channel.View]}: every channel without what is the assistant's (targets, the
	// plugin channels' features); the Feishu bot's features are its switches.
	ChannelsList = "channels.list"
	// ChannelsSend: {channel, ref, text, kind, reply_to?} → {message_id?}: one piece to the owner (retried with the same
	// ref); CodeCannotSend when there is nowhere to send to yet.
	ChannelsSend = "channels.send"
	// ChannelsTyping: {channel, on} → {}.
	ChannelsTyping = "channels.typing"
	// ChannelsEnable: {channel, on} → {}: the plugin (and its connector), or the Feishu bot, switched.
	ChannelsEnable = "channels.enable"
	// ChannelsUnbind: {channel} → {}: signed out, unpaired.
	ChannelsUnbind = "channels.unbind"

	// InteractionsResolve: {interaction_id, resolution, action, channel} → {ok, status}: the card answered as the user
	// answered it in channel. action is the owner's action the hub handed over with that press or message there
	// (single use; a card's is for that card only, a message's for a kind of card the channel takes a typed answer
	// for); without one, or with another channel's, the hub refuses. ok false (status as InteractionsGet's) when it is no
	// longer pending, an error when the answer does not fit it.
	InteractionsResolve = "interactions.resolve"

	// CodeCannotSend: a channel has nowhere to send to yet (channel.CodeCannotSend); CodeNeedsTriageModel: a plugin
	// can't be switched on before the inbox's triage model is set (409 to the app).
	CodeCannotSend       = "cannot_send"
	CodeNeedsTriageModel = "needs_triage_model"
	// CodeTryAgain: the assistant could not take an owner's message now (what it needs of the hub did not answer):
	// the hub hands it over again, as after a cut-off call.
	CodeTryAgain = "try_again"
)

// ChannelReceive is channels.receive's params.
type ChannelReceive struct {
	Channel string          `json:"channel"`
	Message json.RawMessage `json:"message"` // channel.Message
	// Action is the owner's action: what answers a card with this message passes it (InteractionsResolve).
	Action string `json:"action,omitempty"`
}

// ChannelUnbound is channels.unbound's params.
type ChannelUnbound struct {
	Channel string `json:"channel"`
}

// FeishuMessage is feishu.message's params: im.message.receive_v1's event as Feishu sent it (the owner's, in the
// bot's private chat), and the owner's action for what this message answers (InteractionsResolve).
type FeishuMessage struct {
	Event  json.RawMessage `json:"event"`
	Action string          `json:"action,omitempty"`
	// From says where the hub took the message from when it is not the owner's private chat or a topic of a group the
	// owner opted in: FeishuFromElsewhere is the owner's "@bot /ai …" in a group that is not opted in (#6146), the only
	// thing the hub lets through from one. "" for the usual two.
	From string `json:"from,omitempty"`
}

// FeishuFromElsewhere is FeishuMessage.From of a message the hub took from a group chat that is not opted in: the owner's
// "@bot /ai …" and nothing else. The assistant opens a to-do for it in the home topic group, or in the owner's private chat when there is none (assistant/feishu_elsewhere.go).
const FeishuFromElsewhere = "elsewhere"

// FeishuAction is feishu.card_action's params: card.action.trigger's event as Feishu sent it (the owner's press), and
// the owner's action for the card it presses (InteractionsResolve).
type FeishuAction struct {
	Event  json.RawMessage `json:"event"`
	Action string          `json:"action,omitempty"`
}

// FeishuView is feishu.view's answer.
type FeishuView struct {
	Target string        `json:"target,omitempty"` // where the chat's messages go now
	Agents []FeishuAgent `json:"agents,omitempty"` // agents bound to threads of the chat
}

// FeishuAgent is an agent bound to a thread of the bot's chat.
type FeishuAgent struct {
	Title     string `json:"title"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
}

// ChannelNoted is channels.noted's params.
type ChannelNoted struct {
	Channel   string `json:"channel"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
}

// FeishuOwner is feishu.owner's params.
type FeishuOwner struct {
	Paired bool `json:"paired"`
}

// FeishuRecalled is feishu.recalled's params: the message the owner recalled.
type FeishuRecalled struct {
	MessageID string `json:"message_id"`
}

// FeishuFeatures are the Feishu bot's switches (settings bots.feishu, the hub's with its credentials).
type FeishuFeatures struct {
	Main     bool `json:"main"`
	Cards    bool `json:"cards"`
	AllCards bool `json:"all_cards"`
	Pushes   bool `json:"pushes"`
	Sync     bool `json:"sync"`
	Agents   bool `json:"agents"`
	Reports  bool `json:"reports"` // the main conversation's tasks are told in the chat (#2768)
	Board    bool `json:"board"`   // /tasks keeps one standing "waiting for you" card, edited in place (#6052)
	// Workbench: /home, /today and /inbox, the standing board drawn as a pinned home card, a workbench button on every
	// card, a permission field in /start and the companion bots' hooks (#1961).
	Workbench bool `json:"workbench"`
	// OnlyConclusions: a task's finished turn, waiting and interrupted states are not pushed on their own; the task list
	// and the briefing carry them, and only its conclusion card goes out (#1226).
	OnlyConclusions bool `json:"only_conclusions"`
}

// FeishuStatus is feishu.status's answer.
type FeishuStatus struct {
	// Ready: switched on, connected and paired: the bot can post now.
	Ready    bool           `json:"ready"`
	Enabled  bool           `json:"enabled"`
	Features FeishuFeatures `json:"features"`
	Owner    string         `json:"owner,omitempty"` // the owner's open id ("" before pairing)
	// PairedAt is when the owner paired: with Owner, which pairing the chat's state belongs to (a new one, or none,
	// starts it afresh).
	PairedAt time.Time `json:"paired_at,omitzero"`
	// PublicURL is the hub's web app (a card's "Open web app").
	PublicURL string `json:"public_url,omitempty"`
	// Chats are the chats the bot may send into for the assistant (the hub's own rule for feishu.post / feishu.text):
	// the owner's private chat and the group chats the owner opted in. A thread the assistant posts into is checked
	// against it before the call, so nothing goes to a chat an agent made up.
	Chats []string `json:"chats,omitempty"`
	// HomeChat is the home topic group (#6146): the opted-in group the owner marked with "/topics home", else the first
	// one opted in; "" while none is. It is one of Chats. Where a request from a group that is not opted in opens its topic.
	HomeChat string `json:"home_chat,omitempty"`
	// NudgeAfter is the bot's setting nudge_after (#6084): minutes a card waits unanswered before the one nudge under it,
	// 0 off. nil (a status from before the setting) is NudgeAfterDefault.
	NudgeAfter *int `json:"nudge_after,omitempty"`
}

// NudgeAfterDefault is nudge_after when never set: minutes a card of the Lark bot waits before it is nudged.
const NudgeAfterDefault = 60

// InteractionAnnounced is interaction.announced's params.
type InteractionAnnounced struct {
	Interaction proto.Interaction `json:"interaction"`
}

// InteractionEnded is interaction.ended's params: Resolution when it was answered (how, for its cards), nil when it
// ended otherwise (withdrawn, expired, answered and gone).
type InteractionEnded struct {
	Interaction proto.Interaction `json:"interaction"`
	Resolution  *proto.Resolution `json:"resolution,omitempty"`
}

// SessionItems is session.items's params.
type SessionItems struct {
	MachineID string       `json:"machine_id"`
	SessionID string       `json:"session_id"`
	Items     []proto.Item `json:"items"`
}

// SessionDelta is session.delta's params: the text the conversation is writing now.
type SessionDelta struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// ChannelsReport is what the hub shows of the channels, in the snapshot (Snapshot.Channels): which channels each
// conversation's messages came from (the session lists' channels), and the conversations whose turn a channel follows
// now or answered lately (the hub's finished push leaves those out: the answer is in the channel). Small, whole every
// time; null while the channels' state could not be read (the hub keeps the last).
type ChannelsReport struct {
	Marks []ChannelMark `json:"marks"`
	Turns []ChannelTurn `json:"turns"`
}

// ChannelMark is one conversation's channels, newest first.
type ChannelMark struct {
	MachineID string   `json:"machine_id"`
	SessionID string   `json:"session_id"`
	Channels  []string `json:"channels"`
}

// ChannelTurn is a conversation a channel's turn is followed in (Answered zero) or was answered in at Answered.
type ChannelTurn struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	Answered  time.Time `json:"answered,omitzero"`
}
