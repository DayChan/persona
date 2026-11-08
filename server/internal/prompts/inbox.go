package prompts

// InboxItem is an inbox item as the models read it (inbox/item): what kind of thing it is, then the conversation.
type InboxItem struct {
	Kind       string // dm | mention | group | bot | doc | moment | mail; a connector's own (inbox/kinds/<source>)
	MailDirect bool   // mail: sent to me, not cc / a list
	// Source is a connector's id ("github") for its items, "" for Feishu's and mail's.
	Source string
	// Described is what the item's kind is, as a plugin's connector says (its kind_descriptions); "" otherwise.
	Described string
	Context   string
}

func RenderInboxItem(it InboxItem) string { return render("inbox/item", it) }

// InboxTriage is the system prompt that sorts an item into "Needs You" / "FYI" / "Noise" (inbox/triage).
type InboxTriage struct {
	Owner string // "" is the Chinese "you"
	Rules string // the user's own rules, trimmed: the item's source's (InboxThing.Rules), else inbox.rules
	// Language is the summary's language by its own name ("English", "Japanese" written in Japanese); "" is Chinese, as before i18n.
	Language string
}

func RenderInboxTriage(t InboxTriage) string { return render("inbox/triage", t) }

// InboxGroup is the system prompt that relates a conversation's new messages to its events (inbox/group, #1680).
type InboxGroup struct {
	Owner string // "" is the Chinese "you"
	// MetaDescription is what the connector says its events' meta is (its manifest's meta_description), trimmed.
	MetaDescription string
	Rules           string // the user's own rules, trimmed: the batch's source's (InboxThing.Rules), else inbox.rules
	MaxCalls        int    // tool calls the batch may make
	NewMax          int    // new events one batch may open
	// Language is the titles' and summaries' language by its own name ("English", "Japanese" in Japanese); "" leaves the title's
	// unsaid and the summary in Chinese, as before i18n.
	Language string
}

func RenderInboxGroup(g InboxGroup) string { return render("inbox/group", g) }

// InboxGroupInput is one batch as the model reads it, with the tool calls made so far (inbox/group_input).
type InboxGroupInput struct {
	Thread  string
	New     []string // the new messages' ids
	Context string   // their text, as the connector wrote it
	Events  string   // the events tool's answer without a query (JSON)
	Calls   []InboxGroupCall
	Left    int // tool calls still allowed
}

// InboxGroupCall is one tool call and its result (JSON as the tool answered, cut).
type InboxGroupCall struct {
	Tool, Args, Result string
}

func RenderInboxGroupInput(in InboxGroupInput) string { return render("inbox/group_input", in) }

// InboxDraft is the system prompt for drafting a reply in the user's voice (inbox/draft).
type InboxDraft struct {
	Owner string // "" is the Chinese "you"
	Notes string // what the assistant knows about the user, trimmed
	// Language is the user's language by its own name ("English", "Japanese" in Japanese), for a WeChat reply when the other side's
	// is unclear; "" keeps WeChat replies in Chinese, as before i18n.
	Language string
}

func RenderInboxDraft(d InboxDraft) string { return render("inbox/draft", d) }

// InboxDraftInput is the item and how the user wants to answer (inbox/draft_input).
type InboxDraftInput struct {
	Item        InboxItem
	Instruction string
}

func RenderInboxDraftInput(in InboxDraftInput) string { return render("inbox/draft_input", in) }

// InboxJudge is the system prompt that links an item to one of the user's tasks (inbox/judge): since phase 1 every
// Needs you item goes to the manager, so only its task_id is used.
type InboxJudge struct {
	Owner string
	Rules string // manager.json inbox.agent.rules, trimmed
}

func RenderInboxJudge(j InboxJudge) string { return render("inbox/judge", j) }

// InboxJudgeInput is the item with its triage summary (inbox/judge_input).
type InboxJudgeInput struct {
	Summary string
	Item    InboxItem
	Tasks   []InboxTask // the user's tasks going on (docs/assistant/unified-entry.md), for the judge to link the item to one
}

// InboxTask is a task the user started from the assistant's conversation: a todo and where its agents are.
type InboxTask struct {
	ID, Title, State string
	Last             string // what its latest agent said last, in a line
}

func RenderInboxJudgeInput(in InboxJudgeInput) string { return render("inbox/judge_input", in) }

// InboxAgent is the first message of the AI twin's conversation for an item (inbox/agent). At most one of Lark, Doc,
// Moment and Mail is set: where the item came from.
type InboxAgent struct {
	Owner   string
	EventID string // the inbox item's id: the conversation is bound to it
	Title   string
	Item    InboxItem
	Task    string // what to do: the triage's summary ("" the default)
	Reason  string // the triage's reason
	// Resumed: the conversation that handled the event is gone; Recap and Chain are where it got to.
	Resumed bool
	Recap   string
	Chain   []string // the event's chain so far, one line an entry, oldest first
	Notes   []string // what came since (the user's answers…), each rendered (inbox/handling/*)
	// Linked: the user's task the item is about (the judge's task_id), its progress for the reply.
	Linked *InboxTask
	Lark   *InboxAgentLark
	Doc    *InboxAgentDoc
	Moment *InboxAgentMoment
	Mail   *InboxAgentMail
	// Connector: an item of a connector's (GitHub…).
	Connector *InboxAgentConnector
	KB        *KBContext // what the knowledge base knows about the item's chat
	// KnowledgeDir: the knowledge base directory the KB part names; "" is the default ~/.persona/data.
	KnowledgeDir string
}

// InboxAgentLark is the item's Feishu chat; Account is the gateway account it is in when that is not lark.account.
type InboxAgentLark struct {
	FeedID  string
	Account int
}

// InboxAgentConnector is the thread of a connector's item: Source is the connector's id.
type InboxAgentConnector struct {
	Source  string
	URL     string
	ReplyTo string
}

type InboxAgentDoc struct {
	URL  string
	Rows bool // rows the agent may move the status of
}

type InboxAgentMoment struct{ PostID string }

type InboxAgentMail struct {
	Account, Mailbox string
	UID              uint32 // the newest message
}

func RenderInboxAgent(a InboxAgent) string { return render("inbox/agent", a) }

// InboxHandlingNote is what the program tells an event's processing conversation (inbox/handling/<name>): new
// messages (Context), the user's confirmation, rejection or words (Text, Edited), the Agent it started (Machine,
// Session), or that the event is closed (Text says how: self_replied, replied, dismissed, read, user).
type InboxHandlingNote struct {
	Owner            string
	Text             string
	Edited           bool
	Context          string
	Machine, Session string
	Handoff          bool // the note goes to a conversation the event was handed to (wrapped by inbox/handoff_note)
}

// The notes (their templates).
const (
	NoteNewMessages  = "inbox/handling/new_messages"
	NoteSent         = "inbox/handling/sent"
	NoteAgentStarted = "inbox/handling/agent_started"
	NoteHandback     = "inbox/handling/handback"
	NoteRejected     = "inbox/handling/rejected"
	NoteInstructed   = "inbox/handling/instructed"
	NoteClosed       = "inbox/handling/closed"
)

func RenderInboxHandlingNote(name string, n InboxHandlingNote) string {
	return render(name, n)
}

// InboxHandoff is the message that hands an event to Persona's main conversation (inbox/handoff): the user's Text
// after a context block naming the event, so that conversation can write back with inbox_suggest.
type InboxHandoff struct {
	ID, Title string
	Text      string
}

func RenderInboxHandoff(h InboxHandoff) string { return render("inbox/handoff", h) }

// InboxHandoffNote wraps a handling note (Note) for a conversation the event was handed to (inbox/handoff_note).
type InboxHandoffNote struct {
	ID, Title string
	Note      string
}

func RenderInboxHandoffNote(n InboxHandoffNote) string { return render("inbox/handoff_note", n) }
