package kbwire

import (
	"time"
)

// SourceChat is a chat as a source lists it, newest activity first.
type SourceChat struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"` // dm | group | bot
	Topic     bool      `json:"topic,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// SourceMessage is one message as a source returns it. ID is stable within its
// source and chat; Engine.Ingest keeps the first durable version of each ID.
// Position is an opaque pagination token, not an identity. Missing IDs retain
// legacy append behavior and cannot provide replay protection.
type SourceMessage struct {
	ID         string    `json:"id"`
	Position   string    `json:"position,omitempty"`
	SenderID   string    `json:"sender_id,omitempty"`
	SenderName string    `json:"sender_name,omitempty"`
	Mine       bool      `json:"mine,omitempty"`
	Type       string    `json:"type,omitempty"` // TEXT, POST, IMAGE, FILE, CARD, SYSTEM…
	Text       string    `json:"text,omitempty"`
	At         time.Time `json:"at,omitempty"`
	Media      []string  `json:"media,omitempty"` // source paths of images / files, for Download
	// SenderHandles are hints of who the sender is (their address, number, login: handles.go), raw and in memory
	// only: the person keeps them hashed.
	SenderHandles []Handle `json:"sender_handles,omitempty"`
}
