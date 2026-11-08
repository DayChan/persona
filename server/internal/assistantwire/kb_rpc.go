package assistantwire

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// Extension-facing knowledge-base calls. The hub checks the extension's granted scope before forwarding these.
const (
	MethodKBList   = "kb.list"
	MethodKBGet    = "kb.get"
	MethodKBDay    = "kb.day"
	MethodKBSearch = "kb.search"
	MethodKBUpdate = "kb.update"
	// MethodKBRecent lists what came in from every source since a time, newest first (#6100).
	MethodKBRecent = "kb.recent"
	// MethodKBExtensionUpdate is how the manager hands the assistant an extension's kb.update: with the extension's
	// id, which the extension cannot set (KBExtensionUpdate). Not on the extensions' allowlist.
	MethodKBExtensionUpdate = "kb.extension_update"
	// CodeKBConflict: an extension's kb.update touches fields the user or another extension holds; data is
	// KBConflict.
	CodeKBConflict = "conflict"
)

// KBExtensionUpdate is an extension's kb.update params as it sent them, and who it is.
type KBExtensionUpdate struct {
	StorageID string          `json:"storage_id,omitempty"`
	Extension string          `json:"extension"`
	Params    json.RawMessage `json:"params"`
}

func (x KBExtensionUpdate) StorageKey() string {
	if x.StorageID != "" {
		return x.StorageID
	}
	return x.Extension
}

// KBConflict is the data of a conflict error: field → its holder ("user" or "ext:<id>"); fields of a linked entity
// are "<kind>/<id>.<field>".
type KBConflict struct {
	Owners map[string]string `json:"owners"`
}

type KBListParams struct {
	Kind   string `json:"kind"`
	Tag    string `json:"tag,omitempty"`
	Limit  int    `json:"limit,omitempty"`  // default 30, at most 200
	Offset int    `json:"offset,omitempty"` // default 0; pages reflect the current ranking, without a snapshot
	Since  string `json:"since,omitempty"`  // RFC 3339: only entities updated at or after it
}

type KBListResult struct {
	Items      []kbwire.Entry `json:"items"`
	Tags       []string       `json:"tags"`
	Total      int            `json:"total"`
	NextOffset *int           `json:"next_offset,omitempty"`
}

type KBRecentParams struct {
	Since  string   `json:"since"`            // RFC 3339, required
	Until  string   `json:"until,omitempty"`  // RFC 3339: before it; none is now
	Kinds  []string `json:"kinds,omitempty"`  // default chats, docs, meetings, events, recordings
	Limit  int      `json:"limit,omitempty"`  // default 50, at most 200
	Offset int      `json:"offset,omitempty"` // default 0
}

type KBRecentResult struct {
	Items      []kbwire.Entry `json:"items"`
	Total      int            `json:"total"`
	NextOffset *int           `json:"next_offset,omitempty"`
}

type KBGetParams struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	RecentChars *int   `json:"recent_chars,omitempty"` // default 12000; 0 omits recent messages; at most 20000
}

type KBGetResult struct {
	Detail *kbwire.Detail `json:"detail"`
}

type KBDayParams struct {
	ID  string `json:"id"`
	Day string `json:"day"` // YYYY-MM-DD in the knowledge base's time zone
}

type KBDayResult struct {
	Day      string              `json:"day"`
	Messages []kbwire.DayMessage `json:"messages"` // n is the first source-file line, 1-based
	Prev     string              `json:"prev"`
	Next     string              `json:"next"`
}

type KBSearchParams struct {
	Query    string `json:"query"`
	Messages *bool  `json:"messages,omitempty"` // default true
	Limit    int    `json:"limit,omitempty"`    // default 40, at most 200
}

type KBSearchResult struct {
	Hits []kbwire.Hit `json:"hits"`
}

type KBUpdateParams struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	kbwire.Update
	// Evidence: what the written values rest on, kept as the provenance of the fields this write holds (at most 20).
	Evidence []kbwire.Evidence `json:"evidence,omitempty"`
	// Unlock: fields this extension holds that it hands back to the built-in pipeline (unlocking pinned clears it).
	Unlock []string `json:"unlock,omitempty"`
}

type KBUpdateResult struct {
	ID string `json:"id"`
	// Before: the previous value of each field the update wrote (null when unset; summary is the old text), for undo.
	Before map[string]json.RawMessage `json:"before,omitempty"`
}
