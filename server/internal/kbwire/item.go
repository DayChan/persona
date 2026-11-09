package kbwire

import "time"

// Item is a document, meeting or calendar event as a connector hands it over (connectors.items, docs/extensions/connectors.md):
// the same JSON as the knowledge base's own item (kb.Item), which adds what only the store keeps.
type Item struct {
	Kind     string    `json:"kind"`             // docs | meetings | events
	Source   string    `json:"source,omitempty"` // the collector replaces this with its trusted connector ID
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	URL      string    `json:"url,omitempty"`
	At       time.Time `json:"at,omitempty"`
	Where    string    `json:"where,omitempty"` // where it came from (a chat id, "calendar"…)
	Projects []string  `json:"projects,omitempty"`
	Note     string    `json:"note,omitempty"`
	Seen     time.Time `json:"seen"`
	// Signals are why the item concerns the owner, as far as the source can tell (Signal*): the knowledge base's
	// selection decides by them which documents it keeps. A source that cannot tell one leaves it out.
	Signals []string `json:"signals,omitempty"`
	Tagging
}

// What a source can tell about a document and the owner (Item.Signals).
const (
	SignalMentions        = "mentions"         // the document mentions the owner
	SignalCommentMentions = "comment_mentions" // a comment or reply on it mentions the owner
	SignalAuthored        = "authored"         // the owner owns it
	SignalViewed          = "viewed"           // the owner opened it recently
)

// Signals are every signal an item may carry, in the selection's default order.
var Signals = []string{SignalMentions, SignalCommentMentions, SignalAuthored, SignalViewed}

// CanonicalSignals is a source's signals as the knowledge base keeps them: each known one once, in Signals' order, and
// nil for none. A source may list them in any order or repeat one; an unknown one is left out.
func CanonicalSignals(in []string) []string {
	var out []string
	for _, s := range Signals {
		for _, v := range in {
			if v == s {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// Tagging is how an entity is tagged, as the knowledge base writes it into the entity's metadata.
type Tagging struct {
	Importance string    `json:"importance,omitempty"` // P0 (critical) … P3 (ignorable)
	Relevance  int       `json:"relevance"`            // 0–100: how much it concerns the owner
	Tags       []string  `json:"tags,omitempty"`
	Locked     bool      `json:"locked,omitempty"`
	TaggedAt   time.Time `json:"tagged_at,omitempty"`
	// Pinned contains confirmed facts; pipeline writes cannot replace or clear it.
	Pinned string `json:"pinned,omitempty"`
	// LockedFields names update fields that automatic writes must preserve.
	LockedFields []string `json:"locked_fields,omitempty"`
	// Provenance says which extension holds a locked field and what it based the value on; a locked field without
	// an entry is the user's (docs/assistant/knowledge.md "Extensions organizing the knowledge base").
	Provenance map[string]Provenance `json:"provenance,omitempty"`
}

// Provenance is who last wrote a locked field and from what.
type Provenance struct {
	By       string     `json:"by"` // "ext:<extension id>"
	At       time.Time  `json:"at"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

// Evidence points at the source a written value rests on: a chat's day file and, optionally, its line (kb.day's n).
type Evidence struct {
	Chat string `json:"chat"`
	Day  string `json:"day"`         // YYYY-MM-DD in the knowledge base's time zone
	N    int    `json:"n,omitempty"` // 1-based line in that day's file
	Note string `json:"note,omitempty"`
}
