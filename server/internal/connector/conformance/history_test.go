package conformance

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kbwire"
)

// historyConnector is a Go connector that archives and imports history, on made-up data.
type historyConnector struct{}

func (historyConnector) ID() string   { return "example" }
func (historyConnector) Name() string { return "Example" }
func (historyConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapArchive, connector.CapHistory, connector.CapHistoryAnchors}}
}

var historyAt = time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)

func (historyConnector) Changes(context.Context, string, int) ([]connector.ChatBatch, string, bool, error) {
	return nil, "c1", false, nil
}

func (historyConnector) HistoryChats(context.Context) ([]connectorwire.HistoryChat, error) {
	return []connectorwire.HistoryChat{{ID: "dm1", Name: "Example A", Kind: connectorwire.HistoryKindDM, Estimate: 3, Muted: true}}, nil
}

func (historyConnector) History(_ context.Context, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	if q.Before == "1" {
		return connectorwire.HistoryPage{Messages: []kbwire.SourceMessage{{ID: "m0", Position: "0", Text: "first", At: historyAt}}, Newer: "0"}, nil
	}
	return connectorwire.HistoryPage{Messages: []kbwire.SourceMessage{
		{ID: "m1", Position: "1", SenderID: "u1", SenderName: "Example A", Text: "hello", At: historyAt.Add(time.Minute), Mentioned: true},
		{ID: "m2", Position: "2", Mine: true, Text: "hi", At: historyAt.Add(2 * time.Minute), Media: []string{"file:1"}},
	}, Older: "1", More: true, Pace: &connectorwire.Pace{RetryAfterMS: 500}}, nil
}

func (historyConnector) HistoryAnchors(_ context.Context, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error) {
	return connectorwire.HistoryAnchors{Anchors: []connectorwire.HistoryAnchor{{Chat: "dm1", Position: "2", At: historyAt, Mine: true,
		Message: &kbwire.SourceMessage{ID: "m2", Text: "hi", At: historyAt}}}, Next: "a1"}, nil
}

// Check carries a connector's history through both openings and finds it the same as directly.
func TestCheckCarriesHistory(t *testing.T) {
	Check(t, historyConnector{})
}
