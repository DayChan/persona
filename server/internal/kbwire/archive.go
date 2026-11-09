package kbwire

import (
	"time"
)

// SourceChat is a chat as a source lists it, newest activity first. The flags are what the owner set at the source;
// the knowledge base's selection (docs/assistant/knowledge.md "What sources report for the selection") matches its rules
// on them and on the kind, and a source that does not report one never matches that rule.
type SourceChat struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind: the selection's kind rules match dm, group, bot, official and system; a source may report others (mail,
	// issue, pr…), which match no kind rule.
	Kind string `json:"kind"`
	// Topic: a topic (thread-first) group.
	Topic bool `json:"topic,omitempty"`
	// Muted: the owner muted it (a source's automatic muting of large groups included).
	Muted bool `json:"muted,omitempty"`
	// Collapsed: the owner folded it away (a chat box, folded groups).
	Collapsed bool `json:"collapsed,omitempty"`
	// Done: the owner marked it done.
	Done      bool      `json:"done,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// SourceMessage is one message as a source returns it. ID is stable within its
// source and chat; Engine.Ingest keeps the first durable version of each ID.
// Position is an opaque pagination token, not an identity. Missing IDs retain
// legacy append behavior and cannot provide replay protection.
type SourceMessage struct {
	ID         string `json:"id"`
	Position   string `json:"position,omitempty"`
	SenderID   string `json:"sender_id,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
	Mine       bool   `json:"mine,omitempty"`
	// Mentioned: the owner is mentioned by name in it. The connector decides (an @-mention of the owner, or a match
	// of the owner's display names), on archive.changes batches and history pages alike; the selection's mentions
	// rule keeps such messages and history import reads around them. An @all alone is MentionedAll, not this.
	Mentioned bool `json:"mentioned,omitempty"`
	// MentionedAll: it mentions everyone in the chat (an @all / @everyone). The selection's mentions rule counts it
	// only when its include_all is on, so the hub decides, not the connector.
	MentionedAll bool      `json:"mentioned_all,omitempty"`
	Type         string    `json:"type,omitempty"` // TEXT, POST, IMAGE, FILE, CARD, SYSTEM…
	Text         string    `json:"text,omitempty"`
	At           time.Time `json:"at,omitempty"`
	Media        []string  `json:"media,omitempty"` // source paths of images / files, for Download
	// SenderHandles are hints of who the sender is (their address, number, login: handles.go), raw and in memory
	// only: the person keeps them hashed.
	SenderHandles []Handle `json:"sender_handles,omitempty"`
}
