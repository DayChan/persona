package connectorwire

import (
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// Importing a conversation's older history into the knowledge base (docs/protocol/connector-protocol.md "History"):
// archive.changes only moves forward from the account's one cursor, so a conversation's past needs a read of its own,
// one conversation and one page at a time, at the pace the source sets. Everything here is read-only.

// Kinds of a conversation offered for history import (HistoryChat.Kind).
const (
	HistoryKindDM       = "dm"
	HistoryKindGroup    = "group"
	HistoryKindBot      = "bot"
	HistoryKindOfficial = "official" // an official or service account's posts
	HistoryKindSystem   = "system"   // the service's own notices
)

// HistoryKinds are every HistoryKind*, in the order the protocol lists them: the kinds the knowledge base's selection
// matches its kind rules on.
var HistoryKinds = []string{HistoryKindDM, HistoryKindGroup, HistoryKindBot, HistoryKindOfficial, HistoryKindSystem}

// HistoryMost is the largest page limit the hub passes on (HistoryQuery.Limit, HistoryAnchorsQuery.Limit): a larger
// one is cut to it. A limit is a hint, not a count: a source may return more (one returns limit+10 around a position,
// limit+5 paging back), and callers go on from the cursors it answers, never from how many they asked for.
const HistoryMost = 200

// HistoryChat is a conversation a connector can import the history of (history.chats).
type HistoryChat struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"` // HistoryKind*
	// Muted, Collapsed, Done: the owner muted it, folded it away or marked it done at the source, as SourceChat's flags
	// (the selection's rules match on them; a conversation is offered either way).
	Muted     bool `json:"muted,omitempty"`
	Collapsed bool `json:"collapsed,omitempty"`
	Done      bool `json:"done,omitempty"`
	// Bot: the other side is a bot (a dm with a bot, or a group that is only bots).
	Bot bool `json:"bot,omitempty"`
	// Estimate is roughly how many messages it has (0: the source cannot tell), to size the import.
	Estimate int `json:"estimate,omitempty"`
	// Source is the name the conversation is filed under, as ChatBatch.Source: "" is the connector's id, else one of its
	// manifest's ArchiveSources. It only files the conversation: ID alone names it, unique across the connector's
	// sources, since a query, a page and an anchor carry no source.
	Source string `json:"source,omitempty"`
}

// HistoryQuery is one page of a conversation (archive.history), named by its id alone (HistoryChat.ID). At most one of Before, After and Around: older than
// a position, newer than it, or the page around it; none is the newest page. Positions and cursors are the source's
// own strings (HistoryPage.Older / Newer, SourceMessage.Position), handed back as they are.
type HistoryQuery struct {
	Chat   string `json:"chat"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Around string `json:"around,omitempty"`
	// Limit is how many messages the caller would like (0: the source's own default; at most HistoryMost). A hint.
	Limit int `json:"limit,omitempty"`
}

// ErrQuery is what Check refuses a history query with (wrapped): the caller's mistake, permanent, never the source's.
var ErrQuery = errors.New("history query")

// Check refuses a query without a chat, or with more than one of Before, After and Around (wrapping ErrQuery).
func (q HistoryQuery) Check() error {
	if q.Chat == "" {
		return fmt.Errorf("%w: no chat", ErrQuery)
	}
	n := 0
	for _, s := range []string{q.Before, q.After, q.Around} {
		if s != "" {
			n++
		}
	}
	if n > 1 {
		return fmt.Errorf("%w: at most one of before, after and around", ErrQuery)
	}
	if q.Limit < 0 {
		return fmt.Errorf("%w: a negative limit", ErrQuery)
	}
	return nil
}

// Clamped is q with its limit cut to HistoryMost.
func (q HistoryQuery) Clamped() HistoryQuery {
	q.Limit = min(q.Limit, HistoryMost)
	return q
}

// HistoryPage is one page of a conversation's messages, oldest first.
//
// A caller reads one in this order, since a paced page and an unreadable one are empty with More false too:
//  1. Pace set: the source asked to wait (RetryAfterMS) or to stop for a person (Stop). The page never says the start
//     was reached; the caller keeps what messages it holds and reads the same place again later (or stops).
//  2. Unreadable: the account can no longer read the conversation (left, dissolved, gone: the source answers not
//     found). Permanent, with no messages: the caller skips it and never counts it as a failure.
//  3. Otherwise More false, or no messages, is the start of the conversation: there is nothing older, and Older is "".
//
// AtStart says the last.
type HistoryPage struct {
	Messages []kbwire.SourceMessage `json:"messages,omitempty"`
	// Older is the cursor to go further back (HistoryQuery.Before); "" when there is nothing older.
	Older string `json:"older,omitempty"`
	// Newer is the cursor to go forward (HistoryQuery.After); "" when this page is the newest.
	Newer string `json:"newer,omitempty"`
	// More: there is older history than this page.
	More bool `json:"more,omitempty"`
	// Unreadable: the account can no longer read this conversation. Permanent: skip it.
	Unreadable bool `json:"unreadable,omitempty"`
	// Pace, when set, is the source asking the caller to slow down or stop (with or without messages).
	Pace *Pace `json:"pace,omitempty"`
}

// AtStart says the page reached the start of the conversation: no pace, readable, and nothing older (More false or
// no messages). A paced or unreadable page is not the start, however empty.
func (p HistoryPage) AtStart() bool {
	return p.Pace == nil && !p.Unreadable && (!p.More || len(p.Messages) == 0)
}

// Pace is a source's word on how fast it may be read. RetryAfterMS: ask again no sooner than this (a 429 and its
// Retry-After). Stop: do not go on until a person did something (an expired sign-in, a 401, a policy refusal);
// Reason says what, for people, never a secret. A caller that timed out does not know whether a read happened and
// never retries it on its own; a page with Pace is still read for what it holds.
type Pace struct {
	RetryAfterMS int64  `json:"retry_after_ms,omitempty"`
	Stop         bool   `json:"stop,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

// HistoryAnchorsQuery asks a source that can search for the messages the owner sent or was mentioned in over a time
// window (history.anchors), so a conversation can be imported as windows around them instead of read in full.
type HistoryAnchorsQuery struct {
	// Chat is one conversation ("" all of them).
	Chat  string    `json:"chat,omitempty"`
	Since time.Time `json:"since,omitzero"`
	Until time.Time `json:"until,omitzero"`
	// Cursor is HistoryAnchors.Next of the call before ("" the first time).
	Cursor string `json:"cursor,omitempty"`
	// Limit is a hint, as HistoryQuery.Limit.
	Limit int `json:"limit,omitempty"`
}

// Check refuses a window that ends before it starts, or a negative limit (wrapping ErrQuery).
func (q HistoryAnchorsQuery) Check() error {
	if !q.Since.IsZero() && !q.Until.IsZero() && q.Until.Before(q.Since) {
		return fmt.Errorf("%w: until is before since", ErrQuery)
	}
	if q.Limit < 0 {
		return fmt.Errorf("%w: a negative limit", ErrQuery)
	}
	return nil
}

// Clamped is q with its limit cut to HistoryMost.
func (q HistoryAnchorsQuery) Clamped() HistoryAnchorsQuery {
	q.Limit = min(q.Limit, HistoryMost)
	return q
}

// HistoryAnchor is one message the owner sent (Mine) or was mentioned in (Mention): where it is, to read the page
// around it (HistoryQuery.Around = Position). Message is the hit itself, when the source has it: the only way to keep
// one from a conversation that is unreadable now, which search may still find. A search hit may carry no sender id.
// Source is the name the conversation is filed under, as HistoryChat.Source: a conversation the account left is often
// missing from history.chats, so the anchor says it itself.
type HistoryAnchor struct {
	Chat     string                `json:"chat"`
	Source   string                `json:"source,omitempty"`
	Position string                `json:"position"`
	At       time.Time             `json:"at,omitzero"`
	Mine     bool                  `json:"mine,omitempty"`
	Mention  bool                  `json:"mention,omitempty"`
	Message  *kbwire.SourceMessage `json:"message,omitempty"`
}

// HistoryAnchors is one page of anchors: Next is the cursor for the next call, More says it has more.
type HistoryAnchors struct {
	Anchors []HistoryAnchor `json:"anchors,omitempty"`
	Next    string          `json:"next,omitempty"`
	More    bool            `json:"more,omitempty"`
	Pace    *Pace           `json:"pace,omitempty"`
}
