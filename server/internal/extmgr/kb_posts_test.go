package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/proc"
	"github.com/nwverse/persona/server/internal/connector/serve"
)

// The posts a connector's program hands the knowledge base go to the assistant in this process (its archive is the
// knowledge base's, tested in internal/assistant): the manager checks the connector may write now ("Add to knowledge
// base") and passes each call on with the connector's names.

// postsManager is a manager with a fake assistant and a program connector "feed" (an archive, also filing under
// "posts").
func postsManager(t *testing.T) (*Manager, *fakeAssistant, *proc.Conn) {
	t.Helper()
	h := newTestManager(t)
	a := withAssistant(h)
	h.connectors = connector.NewRegistry(nil)
	c := proc.New(proc.Config{ID: "feed", Name: "Feed", KB: h.kbFor("feed"),
		Manifest: connector.Manifest{Capabilities: []connector.Capability{connector.CapArchive}, ArchiveSources: []string{"posts"}}})
	if err := h.connectors.Add(c); err != nil {
		t.Fatal(err)
	}
	return h, a, c
}

func progCall[T any](t *testing.T, c *proc.Conn, method string, params any) (T, error) {
	t.Helper()
	var out T
	raw, _ := json.Marshal(params)
	res, err := c.Handle(context.Background(), method, raw)
	if err != nil {
		return out, err
	}
	b, _ := json.Marshal(res)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out, nil
}

// posts.put, posts.comments, posts.pending and archive.chats reach the knowledge base with the connector's names, and
// its answers come back to the program; "Add to knowledge base" off, nothing goes; the knowledge base not there:
// assistant_offline.
func TestConnectorPostsIntoTheKnowledgeBase(t *testing.T) {
	h, a, c := postsManager(t)
	a.on(assistantwire.MethodKBPostsPut, func(json.RawMessage) (any, error) {
		return map[string]any{"statuses": []string{"new", "refused"}}, nil
	})
	a.on(assistantwire.MethodKBPostsPending, func(json.RawMessage) (any, error) {
		return map[string]any{"posts": []connector.PendingPost{{ID: "p1", CommentCount: 2}}}, nil
	})
	a.on(assistantwire.MethodKBArchivedChats, func(json.RawMessage) (any, error) {
		return map[string]any{"chats": []connector.ArchivedChat{{ID: "p2", Cursor: "1700000000000"}}}, nil
	})
	put := serve.PostsPutParams{Posts: []connector.PostPut{
		{Post: connector.Post{ID: "p1", Feed: "公司圈", Author: "王五", Text: "第一帖", CommentCount: 2}},
		{Post: connector.Post{ID: "other", Text: "抢别人的"}},
	}}
	res, err := progCall[serve.PostsPutResult](t, c, serve.MPostsPut, put)
	if err != nil || strings.Join(res.Statuses, ",") != "new,refused" {
		t.Fatalf("statuses %q, %v", res.Statuses, err)
	}
	var sent assistantwire.KBPosts
	if got := a.got(assistantwire.MethodKBPostsPut); len(got) != 1 || json.Unmarshal(got[0], &sent) != nil ||
		sent.Connector != "feed" || strings.Join(sent.Sources, ",") != "feed,posts" || len(sent.Posts) != 2 || sent.Posts[0].Post.Text != "第一帖" {
		t.Fatalf("posts.put to the knowledge base: %+v", sent)
	}
	cm := serve.PostsCommentsParams{ID: "p1", CommentCount: 2, Comments: []connector.PostComment{{Author: "我", Text: "谢谢"}}}
	if _, err := progCall[struct{}](t, c, serve.MPostsComments, cm); err != nil {
		t.Fatal(err)
	}
	if got := a.got(assistantwire.MethodKBPostsComments); len(got) != 1 || json.Unmarshal(got[0], &sent) != nil ||
		sent.Connector != "feed" || sent.ID != "p1" || sent.Count != 2 || len(sent.Comments) != 1 {
		t.Fatalf("comments to the knowledge base: %+v", sent)
	}
	pending, err := progCall[serve.PostsPendingResult](t, c, serve.MPostsPending, serve.PostsPendingParams{})
	if err != nil || len(pending.Posts) != 1 || pending.Posts[0].ID != "p1" {
		t.Fatalf("pending %+v, %v", pending, err)
	}
	chats, err := progCall[serve.ArchiveChatsResult](t, c, serve.MArchiveChats, serve.ArchiveChatsParams{Source: "posts"})
	if err != nil || len(chats.Chats) != 1 || chats.Chats[0].ID != "p2" || chats.Chats[0].Cursor != "1700000000000" {
		t.Fatalf("chats %+v, %v", chats, err)
	}
	if got := a.got(assistantwire.MethodKBArchivedChats); len(got) != 1 || json.Unmarshal(got[0], &sent) != nil || sent.Source != "posts" ||
		sent.Connector != "feed" {
		t.Fatalf("archived chats asked: %+v", sent)
	}
	// "Add to knowledge base" off: nothing goes in.
	if err := h.connectors.SetFeed("feed", connector.FeedArchive, false); err != nil {
		t.Fatal(err)
	}
	if _, err := progCall[serve.PostsPutResult](t, c, serve.MPostsPut, put); err == nil || len(a.got(assistantwire.MethodKBPostsPut)) != 1 {
		t.Fatalf("posts went in with 进知识库 off: %v", err)
	}
	// The knowledge base not there (the assistant's not open yet): the program's call fails with assistant_offline.
	if err := h.connectors.SetFeed("feed", connector.FeedArchive, true); err != nil {
		t.Fatal(err)
	}
	h.opts.Assistant = nil
	if _, err := progCall[serve.PostsPendingResult](t, c, serve.MPostsPending, serve.PostsPendingParams{}); err == nil || !kbOffline(err) {
		t.Fatalf("posts.pending with the assistant away: %v", err)
	}
}

// A connector without an archive cannot write to the knowledge base.
func TestConnectorPostsNeedAnArchive(t *testing.T) {
	h := newTestManager(t)
	c := proc.New(proc.Config{ID: "tools", Name: "Tools", KB: h.kbFor("tools"),
		Manifest: connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus}}})
	if _, err := progCall[serve.PostsPutResult](t, c, serve.MPostsPut, serve.PostsPutParams{}); err == nil || !strings.Contains(err.Error(), "archive") {
		t.Fatalf("err %v", err)
	}
}

// manager.json's kb.enabled false (the hub's hello says): nothing goes to a knowledge base.
func TestConnectorPostsWithTheKnowledgeBaseOff(t *testing.T) {
	h, a, c := postsManager(t)
	h.hello.Store(&assistantwire.Hello{KB: &assistantwire.KBSettings{Off: true}})
	if _, err := progCall[serve.PostsPutResult](t, c, serve.MPostsPut, serve.PostsPutParams{}); err == nil || len(a.got(assistantwire.MethodKBPostsPut)) != 0 {
		t.Fatalf("posts with the knowledge base off: %v", err)
	}
}

// A running program cannot bypass either switch through any knowledge-base call.
func TestConnectorPostsHonorBothSwitches(t *testing.T) {
	for _, tc := range []struct {
		name             string
		enabled, archive bool
	}{
		{"archive_off", true, false},
		{"connector_off", false, true},
		{"both_off", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, a, c := postsManager(t)
			if err := h.connectors.SetEnabled("feed", tc.enabled); err != nil {
				t.Fatal(err)
			}
			if err := h.connectors.SetFeed("feed", connector.FeedArchive, tc.archive); err != nil {
				t.Fatal(err)
			}
			calls := []struct {
				method, forwarded string
				params            any
			}{
				{serve.MPostsPut, assistantwire.MethodKBPostsPut, serve.PostsPutParams{
					Posts: []connector.PostPut{{Post: connector.Post{ID: "p1", Text: "Example post"}}}}},
				{serve.MPostsComments, assistantwire.MethodKBPostsComments, serve.PostsCommentsParams{
					ID: "p1", CommentCount: 1, Comments: []connector.PostComment{{Author: "Example author", Text: "Example comment"}}}},
				{serve.MPostsPending, assistantwire.MethodKBPostsPending, serve.PostsPendingParams{}},
				{serve.MArchiveChats, assistantwire.MethodKBArchivedChats, serve.ArchiveChatsParams{Source: "posts"}},
			}
			for _, call := range calls {
				if _, err := progCall[struct{}](t, c, call.method, call.params); !errors.Is(err, errArchiveOff) {
					t.Fatalf("%s while switched off: %v", call.method, err)
				}
				if got := a.got(call.forwarded); len(got) != 0 {
					t.Fatalf("%s forwarded while switched off: %s", call.method, got)
				}
			}
			if err := h.connectors.SetEnabled("feed", true); err != nil {
				t.Fatal(err)
			}
			if err := h.connectors.SetFeed("feed", connector.FeedArchive, true); err != nil {
				t.Fatal(err)
			}
			for _, call := range calls {
				if _, err := progCall[struct{}](t, c, call.method, call.params); err != nil {
					t.Fatalf("%s after re-enabling: %v", call.method, err)
				}
				if got := a.got(call.forwarded); len(got) != 1 {
					t.Fatalf("%s not forwarded after re-enabling: %s", call.method, got)
				}
			}
		})
	}
}

func TestConnectorDeletionWaitsForAdmittedPosts(t *testing.T) {
	for _, endpoint := range []string{"archive", "data"} {
		t.Run(endpoint, func(t *testing.T) {
			h, a, _ := postsManager(t)
			entered := make(chan struct{})
			release := make(chan struct{})
			var released sync.Once
			t.Cleanup(func() { released.Do(func() { close(release) }) })
			a.on(assistantwire.MethodKBPostsPut, func(json.RawMessage) (any, error) {
				close(entered)
				<-release
				return map[string]any{"statuses": []string{"new"}}, nil
			})
			posted := make(chan error, 1)
			go func() {
				_, err := h.kbFor("feed").PutPosts(t.Context(), []connector.PostPut{{Post: connector.Post{ID: "p1", Text: "Example post"}}})
				posted <- err
			}()
			<-entered
			deleted := make(chan int, 1)
			go func() {
				code, _ := call(t, h.routes(), "DELETE", "/api/v1/connectors/feed/"+endpoint, h.cfg.Token, "")
				deleted <- code
			}()
			// A mutex wait is not durably blocked for synctest. Bound this negative assertion while the admitted
			// call remains held by release; deletion must wait instead of allowing that post to arrive afterward.
			select {
			case code := <-deleted:
				t.Fatalf("deletion returned %d before the admitted post finished", code)
			case <-time.After(50 * time.Millisecond):
			}
			if got := a.got(assistantwire.MethodKBForget); len(got) != 0 {
				t.Fatalf("deletion reached the knowledge base before the admitted post finished: %s", got)
			}
			released.Do(func() { close(release) })
			select {
			case err := <-posted:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("admitted post did not finish")
			}
			select {
			case code := <-deleted:
				if code != 200 {
					t.Fatalf("deletion returned %d", code)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("deletion did not finish after the admitted post")
			}
			if got := a.got(assistantwire.MethodKBForget); len(got) != 1 {
				t.Fatalf("deletion calls after the admitted post: %s", got)
			}
			if _, err := h.kbFor("feed").PutPosts(t.Context(), nil); !errors.Is(err, errArchiveOff) {
				t.Fatalf("post accepted after deletion: %v", err)
			}
		})
	}
}
