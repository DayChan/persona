package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// historyExample is sourceExample that can import its history (and search it when anchors): it records each query.
type historyExample struct {
	*sourceExample
	anchors bool
	queries []connectorwire.HistoryQuery
	found   []connectorwire.HistoryAnchorsQuery
}

func (h *historyExample) Manifest() connector.Manifest {
	m := h.sourceExample.Manifest()
	m.Capabilities = append(m.Capabilities, connector.CapHistory)
	if h.anchors {
		m.Capabilities = append(m.Capabilities, connector.CapHistoryAnchors)
	}
	return m
}

func (h *historyExample) HistoryChats(context.Context) ([]connectorwire.HistoryChat, error) {
	return []connectorwire.HistoryChat{{ID: "g1", Name: "Example group", Kind: connectorwire.HistoryKindGroup, Estimate: 40}}, nil
}

func (h *historyExample) History(ctx context.Context, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	h.queries = append(h.queries, q)
	switch q.Chat {
	case "slow": // answers only when the caller gives up
		<-ctx.Done()
		return connectorwire.HistoryPage{}, ctx.Err()
	case "gone":
		return connectorwire.HistoryPage{Unreadable: true}, nil
	case "expired":
		return connectorwire.HistoryPage{}, &rpc.Error{Code: "not_signed_in", Message: "sign in again"}
	}
	return connectorwire.HistoryPage{Messages: []kbwire.SourceMessage{{ID: "m1", Position: "1", Text: "hello",
		At: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC), Mentioned: true}}, Older: "0", More: true,
		Pace: &connectorwire.Pace{RetryAfterMS: 2000}}, nil
}

// historyAnchorsExample can search its history too.
type historyAnchorsExample struct{ *historyExample }

func (h historyAnchorsExample) HistoryAnchors(_ context.Context, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error) {
	h.found = append(h.found, q)
	return connectorwire.HistoryAnchors{Anchors: []connectorwire.HistoryAnchor{{Chat: "g1", Position: "1", Mine: true}}, Next: "n1"}, nil
}

// The history reads go to a connector that archives into the knowledge base now and declares them, with the query
// checked and its limit cut, and come back as the connector said (a coded error too); otherwise a clear error.
func TestKBHistoryIsRoutedToTheConnector(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	hist := &historyExample{sourceExample: &sourceExample{}, anchors: true}
	if err := h.addConnector(historyAnchorsExample{hist}); err != nil {
		t.Fatal(err)
	}
	ask := func(method, params string) (string, error) {
		t.Helper()
		raw, ok, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: method, Params: json.RawMessage(params)})
		if !ok {
			t.Fatalf("%s not answered", method)
		}
		return string(raw), err
	}
	if out, err := ask(assistantwire.ConnectorsArchives, `{}`); err != nil ||
		!strings.Contains(out, `{"id":"example","poll_ms":60000,"sources":["posts","mail"],"media":true,"history":true,"history_anchors":true}`) {
		t.Fatalf("archives: %s %v", out, err)
	}
	if out, err := ask(assistantwire.ConnectorsHistoryChats, `{"id":"example"}`); err != nil ||
		out != `{"chats":[{"id":"g1","name":"Example group","kind":"group","estimate":40}]}` {
		t.Fatalf("history chats: %s %v", out, err)
	}
	out, err := ask(assistantwire.ConnectorsHistory, `{"id":"example","query":{"chat":"g1","before":"5","limit":9999}}`)
	if err != nil || out != `{"messages":[{"id":"m1","position":"1","mentioned":true,"text":"hello","at":"2026-11-04T09:00:00Z"}],"older":"0","more":true,"pace":{"retry_after_ms":2000}}` {
		t.Fatalf("history: %s %v", out, err)
	}
	if got := hist.queries[0]; got != (connectorwire.HistoryQuery{Chat: "g1", Before: "5", Limit: connectorwire.HistoryMost}) {
		t.Fatalf("the connector was asked %+v", got)
	}
	if out, err := ask(assistantwire.ConnectorsHistory, `{"id":"example","query":{"chat":"gone"}}`); err != nil || out != `{"unreadable":true}` {
		t.Fatalf("unreadable: %s %v", out, err)
	}
	var re *rpc.Error
	if _, err := ask(assistantwire.ConnectorsHistory, `{"id":"example","query":{"chat":"expired"}}`); !errors.As(err, &re) || re.Code != "not_signed_in" {
		t.Fatalf("a coded error: %v", err)
	}
	asked := len(hist.queries)
	for _, bad := range []string{`{"id":"example"}`, `{"id":"example","query":{"chat":"g1","before":"5","after":"1"}}`} {
		if _, err := ask(assistantwire.ConnectorsHistory, bad); err == nil {
			t.Errorf("%s went through", bad)
		}
	}
	if len(hist.queries) != asked {
		t.Fatal("a refused query reached the connector")
	}
	if out, err := ask(assistantwire.ConnectorsHistoryAnchors, `{"id":"example","query":{"since":"2026-10-01T00:00:00Z","limit":10}}`); err != nil ||
		out != `{"anchors":[{"chat":"g1","position":"1","mine":true}],"next":"n1"}` || !hist.found[0].Since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("anchors: %s %v", out, err)
	}
	if _, err := ask(assistantwire.ConnectorsHistory, `{"id":"nope","query":{"chat":"g1"}}`); err == nil {
		t.Fatal("history of a connector the hub does not have")
	}

	// "Add to knowledge base" off: no history either, and the connector is not asked.
	if err := h.connectors.SetFeed("example", connector.FeedArchive, false); err != nil {
		t.Fatal(err)
	}
	asked = len(hist.queries)
	if _, err := ask(assistantwire.ConnectorsHistory, `{"id":"example","query":{"chat":"g1"}}`); !errors.Is(err, errArchiveOff) || len(hist.queries) != asked {
		t.Fatalf("history with the archive off: %v", err)
	}
	if _, err := ask(assistantwire.ConnectorsHistoryChats, `{"id":"example"}`); !errors.Is(err, errArchiveOff) {
		t.Fatalf("history chats with the archive off: %v", err)
	}
}

// A connector that archives and does not declare history (or anchors) is never asked: a clear error instead.
func TestKBHistoryNeedsTheCapability(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	hist := &historyExample{sourceExample: &sourceExample{}}
	if err := h.addConnector(hist); err != nil {
		t.Fatal(err)
	}
	if err := h.addConnector(namedConnector("plain")); err != nil {
		t.Fatal(err)
	}
	ask := func(method, params string) error {
		t.Helper()
		_, ok, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: method, Params: json.RawMessage(params)})
		if !ok {
			t.Fatalf("%s not answered", method)
		}
		return err
	}
	raw, _, _ := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: assistantwire.ConnectorsArchives})
	if !strings.Contains(string(raw), `"history":true`) || strings.Contains(string(raw), `"history_anchors"`) {
		t.Fatalf("archives: %s", raw)
	}
	if err := ask(assistantwire.ConnectorsHistoryAnchors, `{"id":"example","query":{}}`); err == nil || !strings.Contains(err.Error(), "cannot search") {
		t.Fatalf("anchors of a connector without them: %v", err)
	}
	if err := ask(assistantwire.ConnectorsHistoryChats, `{"id":"plain"}`); err == nil || !strings.Contains(err.Error(), "has no archive") {
		t.Fatalf("history of a connector without an archive: %v", err)
	}
	if len(hist.queries) != 0 {
		t.Fatal("asked")
	}
}

// A read that times out comes back as the timeout, and the connector was asked once: the hub never sends it again on
// its own, since the source may have served it.
func TestKBHistoryTimeoutNotRetried(t *testing.T) {
	historyTimeout = 50 * time.Millisecond
	t.Cleanup(func() { historyTimeout = 2 * time.Minute })
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	hist := &historyExample{sourceExample: &sourceExample{}}
	if err := h.addConnector(hist); err != nil {
		t.Fatal(err)
	}
	_, ok, err := h.kbSourcesCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppConnectors,
		Method: assistantwire.ConnectorsHistory, Params: json.RawMessage(`{"id":"example","query":{"chat":"slow"}}`)})
	if !ok || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a read past the timeout: %v, want the timeout", err)
	}
	if len(hist.queries) != 1 {
		t.Fatalf("the connector was asked %d times, want once", len(hist.queries))
	}
}
