package assistantwire

import "time"

// Extension-facing reads of the assistant's conversations (#1288, docs/extensions/extensions.md "Assistant
// conversations"): the hub checks the extension's assistant.read grant and forwards these as they are; the manager,
// which owns the conversations, answers them. Read-only.
const (
	// MethodAssistantConversations lists the assistant's own-space conversations active in a time range.
	MethodAssistantConversations = "assistant.conversations"
	// MethodAssistantMessages pages one conversation's words, the user's and the assistant's.
	MethodAssistantMessages = "assistant.messages"
)

// AssistantConversationsParams is assistant.conversations' params.
type AssistantConversationsParams struct {
	Since           string `json:"since"`                      // RFC 3339, required: last active at or after it
	Until           string `json:"until,omitempty"`            // RFC 3339: started before it (when known); none is now
	IncludeArchived bool   `json:"include_archived,omitempty"` // archived conversations too
	Limit           int    `json:"limit,omitempty"`            // default 50, at most 200
	Offset          int    `json:"offset,omitempty"`           // default 0
}

// AssistantConversation is one conversation as an extension sees it.
type AssistantConversation struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Main     bool   `json:"main,omitempty"` // the main conversation
	Archived bool   `json:"archived,omitempty"`
	Origin   string `json:"origin"` // user | channel | inbox | task | spawned | lark_agent | extension
	// Started is when it began, when the manager knows (conversations from before titles were kept have none).
	Started *time.Time `json:"started,omitempty"`
	Updated time.Time  `json:"updated"` // its last activity
}

// AssistantConversationsResult is assistant.conversations' result: newest first.
type AssistantConversationsResult struct {
	Items      []AssistantConversation `json:"items"`
	Total      int                     `json:"total"`
	NextOffset *int                    `json:"next_offset,omitempty"`
}

// AssistantMessagesParams is assistant.messages' params.
type AssistantMessagesParams struct {
	ID       string `json:"id"`                  // the conversation (assistant.conversations' id)
	Since    string `json:"since,omitempty"`     // RFC 3339: messages at or after it
	Until    string `json:"until,omitempty"`     // RFC 3339: messages before it
	AfterSeq *int   `json:"after_seq,omitempty"` // continue after this seq (the last page's next_seq)
	Limit    int    `json:"limit,omitempty"`     // default 200, at most 500
}

// AssistantMessage is one message: the user's words or the assistant's reply.
type AssistantMessage struct {
	Seq  int       `json:"seq"`
	At   time.Time `json:"at"`
	Role string    `json:"role"` // user | assistant
	Text string    `json:"text"`
	// Truncated: the text was cut for pages (a very long message).
	Truncated bool `json:"truncated,omitempty"`
}

// AssistantMessagesResult is assistant.messages' result, in time order. NextSeq is set while there is more to read
// (pass it as after_seq), even when this page has no messages.
type AssistantMessagesResult struct {
	Messages []AssistantMessage `json:"messages"`
	NextSeq  *int               `json:"next_seq,omitempty"`
}

// CodeConversationNotFound: assistant.messages names no conversation of the assistant's own space.
const CodeConversationNotFound = "not_found"
