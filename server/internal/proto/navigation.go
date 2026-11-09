package proto

import "time"

const FeatureSessionNavigation = "session_navigation_v1"

// NavigationMessage is a lightweight tick in the user's message navigator.
// It contains no tool payload, context block, image bytes, or full message body.
type NavigationMessage struct {
	ID       string    `json:"id"`
	Seq      int       `json:"seq"`
	TS       time.Time `json:"ts,omitempty"`
	Text     string    `json:"text"`
	Answer   string    `json:"answer,omitempty"`
	HasImage bool      `json:"has_image,omitempty"`
}

type SessionNavigation struct {
	Messages      []NavigationMessage `json:"messages"`
	HasMore       bool                `json:"has_more"`
	NextBeforeSeq int                 `json:"next_before_seq,omitempty"`
	History       string              `json:"history"`
}
