package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
)

// feedConnector has an archive and an inbox, and counts what it was asked.
type feedConnector struct{ changes, collects int }

func (*feedConnector) ID() string   { return "example" }
func (*feedConnector) Name() string { return "Example" }
func (*feedConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox, connector.CapArchive}, Kinds: []string{"dm"}}
}
func (f *feedConnector) Changes(_ context.Context, cursor string, _ int) ([]connector.ChatBatch, string, bool, error) {
	f.changes++
	return nil, cursor, false, nil
}
func (f *feedConnector) Collect(context.Context) ([]connector.InboxThing, error) {
	f.collects++
	return []connector.InboxThing{{Thread: "c1", Kind: "dm", Title: "示例用户", Preview: "示例消息", At: time.Now(), Last: "1"}}, nil
}
func (*feedConnector) Reply(context.Context, connector.InboxReply) error { return nil }

// feedItemsConnector also supplies documents, meetings and events.
type feedItemsConnector struct {
	feedConnector
	items exampleItems
}

func (f *feedItemsConnector) Manifest() connector.Manifest {
	m := f.feedConnector.Manifest()
	m.Capabilities = append(m.Capabilities, connector.CapItems)
	return m
}
func (f *feedItemsConnector) ItemSource() kb.ItemSource { return &f.items }

func listedExample(t *testing.T, h *Manager) map[string]any {
	t.Helper()
	_, out := call(t, h.routes(), "GET", "/api/v1/connectors", h.cfg.Token, "")
	for _, c := range out["connectors"].([]any) {
		if m := c.(map[string]any); m["id"] == "example" {
			return m
		}
	}
	t.Fatalf("example is not listed: %v", out)
	return nil
}

// "Add to knowledge base" off: the archive and items are not asked at all, while the inbox goes on; "Add to Inbox" off: nothing
// new comes and what came stays. Both are kept for the next start, and only a connector that has the feed can turn it
// off.
func TestConnectorFeedsTurnedOff(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	st := newKeepStore()
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	f := &feedItemsConnector{}
	if err := h.addConnector(f); err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	if c := listedExample(t, h); c["archive"] != true || c["inbox"] != true {
		t.Fatalf("feeds are not on to begin with: %v", c)
	}

	code, out := call(t, mux, "PUT", "/api/v1/connectors/example/settings", h.cfg.Token, `{"archive":false}`)
	if code != 200 || out["archive"] != false || out["inbox"] != true || out["enabled"] != true {
		t.Fatalf("archive off: %d %v", code, out)
	}
	// The knowledge base (the assistant's) reads the connectors the hub lists, through the hub (kb_sources.go).
	if a := h.kbArchives(ctx); len(a.Archives) != 0 || len(a.Items) != 0 {
		t.Fatalf("listed for the knowledge base with its feed off: %+v", a)
	}
	if _, _, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: assistantwire.ConnectorsChanges,
		Params: json.RawMessage(`{"id":"example"}`)}); err == nil || f.changes != 0 {
		t.Fatalf("asked for changes %d time(s) with 进知识库 off (%v)", f.changes, err)
	}
	for _, method := range []string{assistantwire.ConnectorsItems, assistantwire.ConnectorsDocTitle} {
		if _, _, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: method,
			Params: json.RawMessage(`{"id":"example","url":"https://docs.example.com/d1"}`)}); err == nil {
			t.Fatalf("%s succeeded with the knowledge-base feed off", method)
		}
	}
	if f.items.reads != 0 || f.items.titles != 0 {
		t.Fatalf("items read with the knowledge-base feed off: %+v", f.items)
	}
	if src := h.readConnectors(ctx); f.collects != 1 || len(src) != 1 || len(src[0].Things) != 1 {
		t.Fatalf("the inbox stopped with the archive: %d collect(s), %+v", f.collects, src)
	}

	if code, out := call(t, mux, "PUT", "/api/v1/connectors/example/settings", h.cfg.Token, `{"inbox":false}`); code != 200 || out["inbox"] != false {
		t.Fatalf("inbox off: %d %v", code, out)
	}
	// Read with nothing: nothing new comes, and what came stays (the manager's inbox leaves a source read with nothing
	// as it was).
	if src := h.readConnectors(ctx); f.collects != 1 || len(src) != 1 || len(src[0].Things) != 0 {
		t.Fatalf("collected %d time(s) with 进收件箱 off: %+v", f.collects, src)
	}

	next := newTestManager(t)
	next.store = st
	next.connectors = connector.NewRegistry(nil)
	_ = next.addConnector(&feedItemsConnector{})
	next.restoreConnectorSwitches(ctx)
	if next.connectors.Feeds("example", connector.FeedArchive) || next.connectors.Feeds("example", connector.FeedInbox) {
		t.Fatalf("the next hub forgot the feeds: %s", st.settings[connectorSettingsKey])
	}
	if !next.connectors.Enabled("example") {
		t.Fatal("turning the feeds off switched the connector off")
	}

	if code, out := call(t, mux, "PUT", "/api/v1/connectors/example/settings", h.cfg.Token, `{"archive":true}`); code != 200 || out["archive"] != true {
		t.Fatalf("archive back on: %d %v", code, out)
	}
	if a := h.kbArchives(ctx); len(a.Archives) != 1 || a.Archives[0].ID != "example" || len(a.Items) != 1 || a.Items[0] != "example" {
		t.Fatalf("not listed once back on: %+v", a)
	}
	if raw, _, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: assistantwire.ConnectorsChanges,
		Params: json.RawMessage(`{"id":"example","cursor":"c"}`)}); err != nil || f.changes != 1 || string(raw) != `{"batches":[],"next":"c"}` {
		t.Fatalf("not archived once back on: %d %s %v", f.changes, raw, err)
	}
	if _, _, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: assistantwire.ConnectorsItems,
		Params: json.RawMessage(`{"id":"example"}`)}); err != nil || f.items.reads != 1 {
		t.Fatalf("items not read once back on: %+v %v", f.items, err)
	}
	if !strings.Contains(st.settings[connectorSettingsKey], `"archive":{"example":true}`) {
		t.Fatalf("turning it back on is not kept: %s", st.settings[connectorSettingsKey])
	}

	other := newTestManager(t)
	other.connectors = connector.NewRegistry(nil)
	_ = other.addConnector(&exampleConnector{})
	if code, _ := call(t, other.routes(), "PUT", "/api/v1/connectors/example/settings", other.cfg.Token, `{"archive":false}`); code != 400 {
		t.Fatalf("a connector without an archive turned it off: %d", code)
	}
	_, list := call(t, other.routes(), "GET", "/api/v1/connectors", other.cfg.Token, "")
	if c := list["connectors"].([]any)[0].(map[string]any); c["archive"] != nil || c["inbox"] != nil {
		t.Fatalf("feeds listed for a connector without them: %v", c)
	}
}

// "Delete saved knowledge base content" removes the connector's knowledge content and turns "Add
// to knowledge base" off for good; the connector keeps running and its inbox items stay.
func TestConnectorArchiveDelete(t *testing.T) {
	h := newTestManager(t)
	st := newKeepStore()
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	_ = h.addConnector(&feedItemsConnector{})
	mux := h.routes()
	a := forgettingAssistant(h) // the knowledge base is the assistant's: the manager asks it to delete (kb.forget)

	if code, _ := call(t, mux, "DELETE", "/api/v1/connectors/nope/archive", h.cfg.Token, ""); code != 404 {
		t.Fatalf("an unknown connector: %d", code)
	}
	code, out := call(t, mux, "DELETE", "/api/v1/connectors/example/archive", h.cfg.Token, "")
	if code != 200 || out["chats"] != 1.0 || out["people"] != 1.0 || out["posts"] != 2.0 || out["items"] != 3.0 || out["inbox"] != 0.0 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if h.connectors.Feeds("example", connector.FeedArchive) || !strings.Contains(st.settings[connectorSettingsKey], `"archive":{"example":false}`) {
		t.Fatalf("进知识库 not turned off for good: %s", st.settings[connectorSettingsKey])
	}
	if !h.connectors.Enabled("example") || !h.connectors.Feeds("example", connector.FeedInbox) {
		t.Fatal("the connector or its inbox was switched off")
	}
	if sources := h.kbArchives(context.Background()); len(sources.Archives) != 0 || len(sources.Items) != 0 {
		t.Fatalf("deleted content would be collected again: %+v", sources)
	}
	// The archive only: the knowledge base was asked about this source, the inbox's items were not touched.
	if got := a.got(assistantwire.MethodKBForget); len(got) != 1 || !strings.Contains(string(got[0]), `"source":"example"`) ||
		len(a.got(assistantwire.MethodInboxForget)) != 0 {
		t.Fatalf("asked of the assistant: kb.forget %s, inbox.forget %d", got, len(a.got(assistantwire.MethodInboxForget)))
	}
	// The assistant away: nothing is deleted, the app is told to try again (503 assistant_offline).
	h.opts.Assistant = nil
	if code, out := call(t, mux, "DELETE", "/api/v1/connectors/example/archive", h.cfg.Token, ""); code != 503 || out["code"] != assistantwire.CodeAssistantOffline {
		t.Fatalf("with the assistant away: %d %v", code, out)
	}

	other := newTestManager(t)
	other.connectors = connector.NewRegistry(nil)
	_ = other.addConnector(&exampleConnector{})
	if code, _ := call(t, other.routes(), "DELETE", "/api/v1/connectors/example/archive", other.cfg.Token, ""); code != 409 {
		t.Fatalf("a connector without an archive: %d", code)
	}
}
