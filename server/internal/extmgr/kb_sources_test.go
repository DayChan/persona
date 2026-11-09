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
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/kbwire"
)

// sourceExample has an archive (polled every minute, filing under "posts" too, with attachments) and items.
type sourceExample struct {
	asked []string
	items exampleItems
}

func (*sourceExample) ID() string   { return "example" }
func (*sourceExample) Name() string { return "Example" }
func (*sourceExample) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapArchive, connector.CapItems}, Poll: time.Minute,
		ArchiveSources: []string{"posts", "mail"}}
}
func (s *sourceExample) Changes(_ context.Context, cursor string, limit int) ([]connector.ChatBatch, string, bool, error) {
	s.asked = append(s.asked, "changes "+cursor)
	return []connector.ChatBatch{{Chat: kb.SourceChat{ID: "g1", Name: "示例群", Kind: "group"},
		Messages: []kb.SourceMessage{{ID: "m1", SenderName: "示例用户", Type: "TEXT", Text: "周报", At: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)}}}}, "2", true, nil
}
func (s *sourceExample) ArchiveMedia(_ context.Context, ref string) (connector.Media, error) {
	s.asked = append(s.asked, "media "+ref)
	if ref == "big" {
		return connector.Media{Data: make([]byte, connector.ArchiveMediaMost+1)}, nil
	}
	return connector.Media{Mime: "application/pdf", Data: []byte("%PDF-1.7")}, nil
}
func (s *sourceExample) ItemSource() kb.ItemSource { return &s.items }

type exampleItems struct{ reads, titles int }

func (s *exampleItems) Items(context.Context) ([]kbwire.Item, error) {
	s.reads++
	return []kbwire.Item{{Kind: kb.KindMeeting, ID: "m1", Title: "周会"}}, nil
}
func (s *exampleItems) DocTitle(context.Context, string) string {
	s.titles++
	return "发布计划"
}

// The knowledge base's sources are the hub's connectors, read through it as data (connectors.*): which to read now
// (signed in, "Add to knowledge base" on: its poll, its other names but another connector's id, whether it has
// attachments), what moved since a cursor, an attachment (not a too big one), the items and a document's title.
func TestKBSourcesAreTheHubsConnectors(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	f := &sourceExample{}
	if err := h.addConnector(f); err != nil {
		t.Fatal(err)
	}
	if err := h.addConnector(namedConnector("mail")); err != nil { // another connector: "mail" is its id, not one of example's names
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
	out, err := ask(assistantwire.ConnectorsArchives, `{}`)
	if err != nil || !strings.Contains(out, `{"id":"example","poll_ms":60000,"sources":["posts"],"media":true}`) || !strings.Contains(out, `"items":["example"]`) {
		t.Fatalf("archives: %s %v", out, err)
	}
	if out, err := ask(assistantwire.ConnectorsChanges, `{"id":"example","cursor":"1","limit":20}`); err != nil ||
		!strings.Contains(out, `"next":"2","more":true`) || !strings.Contains(out, `"text":"周报"`) || f.asked[0] != "changes 1" {
		t.Fatalf("changes: %s %v", out, err)
	}
	if out, err := ask(assistantwire.ConnectorsArchiveMedia, `{"id":"example","ref":"file:1"}`); err != nil || out != `{"media":{"mime":"application/pdf","data":"JVBERi0xLjc="}}` {
		t.Fatalf("media: %s %v", out, err)
	}
	if _, err := ask(assistantwire.ConnectorsArchiveMedia, `{"id":"example","ref":"big"}`); err == nil {
		t.Fatal("a too big attachment was handed over")
	}
	if out, err := ask(assistantwire.ConnectorsItems, `{"id":"example"}`); err != nil || !strings.Contains(out, `"title":"周会"`) {
		t.Fatalf("items: %s %v", out, err)
	}
	if out, err := ask(assistantwire.ConnectorsDocTitle, `{"id":"example","url":"https://docs.example.com/d1"}`); err != nil || out != `{"title":"发布计划"}` {
		t.Fatalf("doc title: %s %v", out, err)
	}
	if _, err := ask(assistantwire.ConnectorsChanges, `{"id":"nope"}`); err == nil {
		t.Fatal("changes of a connector the hub does not have")
	}

	// Either switch stops both discovery and direct calls from an earlier source list.
	for _, tc := range []struct {
		name             string
		enabled, archive bool
	}{
		{"archive_off", true, false},
		{"connector_off", false, true},
		{"both_off", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := h.connectors.SetFeed("example", connector.FeedArchive, tc.archive); err != nil {
				t.Fatal(err)
			}
			if err := h.connectors.SetEnabled("example", tc.enabled); err != nil {
				t.Fatal(err)
			}
			if out, err := ask(assistantwire.ConnectorsArchives, `{}`); err != nil || out != `{"archives":[],"items":[]}` {
				t.Fatalf("sources while switched off: %s %v", out, err)
			}
			beforeItems, beforeTitles := f.items.reads, f.items.titles
			for _, method := range []string{assistantwire.ConnectorsChanges, assistantwire.ConnectorsArchiveMedia,
				assistantwire.ConnectorsItems, assistantwire.ConnectorsDocTitle} {
				if _, err := ask(method, `{"id":"example","url":"https://docs.example.com/d1"}`); !errors.Is(err, errArchiveOff) {
					t.Fatalf("%s while switched off: %v", method, err)
				}
			}
			if len(f.asked) != 3 || f.items.reads != beforeItems || f.items.titles != beforeTitles {
				t.Fatalf("connector called while switched off: %v, items %+v", f.asked, f.items)
			}
			if err := h.connectors.SetFeed("example", connector.FeedArchive, true); err != nil {
				t.Fatal(err)
			}
			if err := h.connectors.SetEnabled("example", true); err != nil {
				t.Fatal(err)
			}
			if out, err := ask(assistantwire.ConnectorsArchives, `{}`); err != nil || !strings.Contains(out, `"items":["example"]`) {
				t.Fatalf("items not listed after re-enabling: %s %v", out, err)
			}
			for _, method := range []string{assistantwire.ConnectorsItems, assistantwire.ConnectorsDocTitle} {
				if _, err := ask(method, `{"id":"example","url":"https://docs.example.com/d1"}`); err != nil {
					t.Fatalf("%s after re-enabling: %v", method, err)
				}
			}
			if f.items.reads != beforeItems+1 || f.items.titles != beforeTitles+1 {
				t.Fatalf("items not read after re-enabling: %+v", f.items)
			}
		})
	}
	// Another app's methods are not these.
	if _, ok, _ := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: assistantwire.ConnectorsSignedIn}); ok {
		t.Fatal("connectors.signed_in taken here")
	}
}

// An items-only connector has no archive, but still needs a healthy, enabled source.
type itemsOnlySource struct {
	items exampleItems
	state string
}

func (*itemsOnlySource) ID() string   { return "example" }
func (*itemsOnlySource) Name() string { return "Example" }
func (*itemsOnlySource) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapItems, connector.CapStatus}}
}
func (s *itemsOnlySource) ItemSource() kb.ItemSource { return &s.items }
func (s *itemsOnlySource) Status(context.Context) connector.Status {
	return connector.Status{State: s.state}
}

func TestKBItemSourcesNeedReadyConnectors(t *testing.T) {
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	f := &itemsOnlySource{state: connector.StateOK}
	if err := h.addConnector(f); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, state := range []string{connector.StateOK, connector.StateNotSignedIn, connector.StateError, connector.StateOK} {
		f.state = state
		got := h.kbArchives(ctx)
		wantItems := 0
		if state == connector.StateOK {
			wantItems = 1
		}
		if len(got.Archives) != 0 || len(got.Items) != wantItems || (wantItems == 1 && got.Items[0] != "example") {
			t.Fatalf("sources in state %s: %+v", state, got)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if got := h.kbArchives(ctx); len(got.Archives) != 0 || len(got.Items) != 0 {
		t.Fatalf("sources after cancellation: %+v", got)
	}
}
