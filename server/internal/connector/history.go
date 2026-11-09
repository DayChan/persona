package connector

import (
	"context"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// History is an Archive that can also read a conversation's older messages, for importing its past into the knowledge
// base (docs/protocol/connector-protocol.md "History"): HistoryChats lists the conversations it can import, History
// reads one page of one of them. Read-only, and asked only when the manifest declares CapHistory, which needs
// CapArchive: the history is filed as the archive files its conversations (connectorwire.HistoryChat.Source).
//
// A page's limit is a hint (a source may return more); a conversation the account can no longer read is a page with
// Unreadable, not an error; Pace is how the source slows the caller down or stops it. An error is passed to the
// caller as it is (its rpc code too, through a program), and a caller never retries a call that timed out.
type History interface {
	HistoryChats(ctx context.Context) ([]connectorwire.HistoryChat, error)
	History(ctx context.Context, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error)
}

// HistoryAnchors is a History that can search: the messages the owner sent or was mentioned in over a time window,
// so a conversation can be imported as windows around them instead of read in full. Optional (CapHistoryAnchors,
// which needs CapHistory), read-only.
type HistoryAnchors interface {
	HistoryAnchors(ctx context.Context, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error)
}
