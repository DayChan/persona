package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
)

// offArchive is a connector whose archive hands over one chat (two messages) the first time.
type offArchive struct{ calls int }

func (*offArchive) ID() string   { return "example" }
func (*offArchive) Name() string { return "Example" }
func (*offArchive) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapArchive}}
}
func (a *offArchive) Changes(_ context.Context, cursor string, _ int) ([]connector.ChatBatch, string, bool, error) {
	a.calls++
	if cursor != "" {
		return nil, cursor, false, nil
	}
	at := time.Now().Add(-time.Hour)
	return []connector.ChatBatch{{Chat: kb.SourceChat{ID: "g1", Name: "示例群", Kind: "group"},
		Messages: []kb.SourceMessage{{ID: "m1", SenderID: "u1", SenderName: "示例用户", Type: "TEXT", Text: "周五发布", At: at}}}}, "1", false, nil
}

// manager.json kb.enabled false (Off over the hello): the manager hands nothing over: no sources listed or read,
// no posts taken, nothing asked to delete.
func TestKBOffAtTheSources(t *testing.T) {
	h := newTestManager(t)
	h.hello.Store(&assistantwire.Hello{KB: &assistantwire.KBSettings{Off: true}})
	a := withAssistant(h)
	h.connectors = connector.NewRegistry(nil)
	f := &offArchive{}
	if err := h.addConnector(f); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	raw, ok, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: assistantwire.ConnectorsArchives})
	if !ok || err != nil || string(raw) != `{"archives":[],"items":[]}` {
		t.Fatalf("archives: %s %v", raw, err)
	}
	if _, _, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: assistantwire.ConnectorsChanges,
		Params: json.RawMessage(`{"id":"example"}`)}); err == nil || f.calls != 0 {
		t.Fatalf("changes read with the knowledge base off: %v", err)
	}
	for _, m := range []string{assistantwire.ConnectorsHistoryChats, assistantwire.ConnectorsHistory, assistantwire.ConnectorsHistoryAnchors} {
		if _, ok, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: m,
			Params: json.RawMessage(`{"id":"example","query":{"chat":"g1"}}`)}); !ok || !errors.Is(err, errKBOff) || f.calls != 0 {
			t.Fatalf("%s with the knowledge base off: %v", m, err)
		}
	}
	if _, err := h.kbFor("example").PendingPosts(ctx, 10); err == nil || err.Error() != "这个管理者没开知识库" {
		t.Fatalf("posts: %v", err)
	}
	if f, err := h.forgetArchive(ctx, "example"); f != (assistantwire.KBForgotten{}) || err != nil {
		t.Fatalf("forget asked: %+v %v", f, err)
	}
	if got := a.got(assistantwire.MethodKBForget); len(got) != 0 {
		t.Fatalf("the knowledge base was asked to forget: %v", got)
	}
}
