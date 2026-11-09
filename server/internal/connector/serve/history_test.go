package serve

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// historyOnly archives and imports history, and cannot search it; it records the query it was given.
type historyOnly struct{ got *connectorwire.HistoryQuery }

func (historyOnly) ID() string   { return "example" }
func (historyOnly) Name() string { return "Example" }
func (historyOnly) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapArchive, connector.CapHistory}}
}
func (historyOnly) Changes(context.Context, string, int) ([]connector.ChatBatch, string, bool, error) {
	return nil, "", false, nil
}
func (historyOnly) HistoryChats(context.Context) ([]connectorwire.HistoryChat, error) {
	return nil, nil
}
func (h historyOnly) History(_ context.Context, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	*h.got = q
	return connectorwire.HistoryPage{Unreadable: true}, nil
}

func TestHistoryCall(t *testing.T) {
	ctx := context.Background()
	var got connectorwire.HistoryQuery
	c := historyOnly{&got}
	if err := connector.Check(c); err != nil {
		t.Fatal(err)
	}
	// No chats is an empty list on the wire, never null.
	res, err := historyCall(ctx, c, MHistoryChats, nil)
	if raw, _ := json.Marshal(res); err != nil || string(raw) != `{"chats":[]}` {
		t.Fatalf("history.chats %s %v", raw, err)
	}
	if res, err := historyCall(ctx, c, MHistory, json.RawMessage(`{"chat":"c1","before":"9","limit":900}`)); err != nil ||
		!res.(connectorwire.HistoryPage).Unreadable || got != (connectorwire.HistoryQuery{Chat: "c1", Before: "9", Limit: connectorwire.HistoryMost}) {
		t.Fatalf("archive.history %+v %v (the connector got %+v)", res, err, got)
	}
	got = connectorwire.HistoryQuery{}
	for _, bad := range []string{`{"before":"9"}`, `{"chat":"c1","before":"9","around":"3"}`, `{"chat":"c1","limit":-1}`, `[`} {
		if _, err := historyCall(ctx, c, MHistory, json.RawMessage(bad)); err == nil || got.Chat != "" {
			t.Errorf("%s reached the connector: %v", bad, err)
		}
	}
	var re *rpc.Error
	if _, err := historyCall(ctx, c, MHistoryAnchors, json.RawMessage(`{}`)); !errors.As(err, &re) || re.Code != CodeNoCapability {
		t.Fatalf("history.anchors without history_anchors: %v", err)
	}
}
