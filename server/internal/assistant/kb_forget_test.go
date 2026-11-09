package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/kbwire"
)

func TestKBForgetConnectorContentAndIndex(t *testing.T) {
	st := newKBStore()
	s := kbServiceOn(t, newFakeHub(), st, t.TempDir(), nil)
	e := kbEng(t, s)
	ctx := context.Background()
	for _, source := range []string{"example", "feed", "other"} {
		if err := e.Store.PutChat(&kb.Chat{ID: source + "-chat", Source: source, Name: source}); err != nil {
			t.Fatal(err)
		}
		if err := e.Store.PutPerson(&kb.Person{ID: source + "-person", Source: source, Name: source}); err != nil {
			t.Fatal(err)
		}
		st.rows["chats/"+source+"-chat"] = source
		st.rows["people/"+source+"-person"] = source
	}
	for _, source := range []string{"example", "other"} {
		for _, kind := range []string{kb.KindDoc, kb.KindMeeting, kb.KindEvent} {
			if err := e.Store.PutItem(&kb.Item{Kind: kind, ID: source + "-item", Source: source, Title: source}); err != nil {
				t.Fatal(err)
			}
			st.rows[kind+"/"+source+"-item"] = source
		}
		if err := e.Store.PutMomentPost(&kb.MomentPost{ID: source + "-post", Source: source, Text: source, At: time.Now()}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Store.WriteMomentIndex(); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(assistantwire.KBForget{Source: "example", ArchiveSources: []string{"feed"}})
	out, handled, err := s.kbRequest(ctx, assistantwire.MethodKBForget, params)
	want := assistantwire.KBForgotten{Chats: 2, People: 2, Posts: 1, Items: 3}
	if err != nil || !handled || out != want {
		t.Fatalf("forget = %+v, %v, %v; want %+v", out, handled, err, want)
	}
	if len(st.rows) != 5 || len(st.deleted) != 7 {
		t.Fatalf("index rows = %v, deleted = %v", st.rows, st.deleted)
	}
	for key, name := range st.rows {
		if name != "other" {
			t.Errorf("deleted source remains indexed: %s", key)
		}
	}
	for _, source := range []string{"example", "feed"} {
		if _, ok := e.Store.Chat(source + "-chat"); ok {
			t.Errorf("chat from %s remains", source)
		}
	}
	if _, ok := e.Store.MomentPost("other-post"); !ok {
		t.Fatal("another connector's post was deleted")
	}
	raw, err := os.ReadFile(filepath.Join(e.Store.Dir, "moments", "index.jsonl"))
	if err != nil || strings.Contains(string(raw), "example-post") || !strings.Contains(string(raw), "other-post") {
		t.Fatalf("post index = %s, %v", raw, err)
	}
	if again, _, err := s.kbRequest(ctx, assistantwire.MethodKBForget, params); err != nil || again != (assistantwire.KBForgotten{}) {
		t.Fatalf("second deletion = %+v, %v", again, err)
	}
}

// postingItemsHub models a connector that submits a post while answering Items.
type postingItemsHub struct {
	*fakeHub
	s *Service
}

func (h *postingItemsHub) AppCall(ctx context.Context, p AppCall) (json.RawMessage, error) {
	if p.AppID == AppConnectors && p.Method == assistantwire.ConnectorsItems {
		_, err := h.s.kbPutPosts(ctx, assistantwire.KBPosts{Connector: "example", Posts: []connector.PostPut{{Post: connector.Post{ID: "during-read", Text: "Example post", At: time.Now()}}}})
		return json.RawMessage(`{"items":[]}`), err
	}
	return h.fakeHub.AppCall(ctx, p)
}

func TestKBConnectorReadCanSubmitPosts(t *testing.T) {
	h := &postingItemsHub{fakeHub: newFakeHub()}
	h.hello.KB = &KBSettings{}
	h.answerApp(AppConnectors, assistantwire.ConnectorsArchives, func(json.RawMessage) (any, error) {
		return assistantwire.Archives{Items: []string{"example"}}, nil
	})
	h.s = New(Options{Hub: h, KB: &KBOptions{Dir: t.TempDir()}, Logf: testLogf(t)})
	t.Cleanup(h.s.kbClose)
	setSettings(t, h.s, func(v *Settings) { v.Timezone = "UTC" })
	e := kbEng(t, h.s)
	done := make(chan struct{})
	go func() {
		h.s.kbCollectItems(context.Background(), e)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a connector could not submit posts while answering an item read")
	}
	if _, ok := e.Store.MomentPost("during-read"); !ok {
		t.Fatal("the post submitted during the item read was not stored")
	}
}

func TestKBForgetWaitsForItemCollection(t *testing.T) {
	f := newFakeHub()
	entered, release := make(chan struct{}), make(chan struct{})
	f.answerApp(AppConnectors, assistantwire.ConnectorsArchives, func(json.RawMessage) (any, error) {
		return assistantwire.Archives{Items: []string{"example"}}, nil
	})
	f.answerApp(AppConnectors, assistantwire.ConnectorsItems, func(json.RawMessage) (any, error) {
		close(entered)
		<-release
		return assistantwire.KBItems{Items: []kbwire.Item{{Kind: kb.KindMeeting, ID: "meeting", Title: "Example meeting"}}}, nil
	})
	s := newKBService(t, f)
	e := kbEng(t, s)
	e.Model = nil
	e.ModelFor = nil
	collected := make(chan struct{})
	go func() {
		s.kbCollectItems(context.Background(), e)
		close(collected)
	}()
	<-entered
	forgotten := make(chan assistantwire.KBForgotten, 1)
	go func() {
		out, err := s.kbForget(context.Background(), "example")
		if err != nil {
			t.Error(err)
		}
		forgotten <- out
	}()
	select {
	case <-forgotten:
		t.Fatal("deletion completed before the admitted item read")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)
	<-collected
	if out := <-forgotten; out.Items != 1 {
		t.Fatalf("deleted = %+v", out)
	}
	if _, ok := e.Store.Item(kb.KindMeeting, "meeting"); ok {
		t.Fatal("the item read before deletion survived")
	}
}
