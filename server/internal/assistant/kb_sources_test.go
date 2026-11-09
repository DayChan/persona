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

// archiveFake hands out one batch per cursor step and records the cursors it was given.
type archiveFake struct {
	cursors []string
}

func (f *archiveFake) Changes(_ context.Context, cursor string, _ int) ([]connector.ChatBatch, string, bool, error) {
	f.cursors = append(f.cursors, cursor)
	at := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	switch cursor {
	case "":
		return []connector.ChatBatch{{Chat: kb.SourceChat{ID: "example-org-example-repo-12", Name: "example-repo#12 Add the example", Kind: "pr"},
			Messages: []kb.SourceMessage{{ID: "body", SenderID: "alice", SenderName: "alice", Type: "TEXT", Text: "【Add the example】\n示例描述", At: at}}}}, "1", true, nil
	case "1":
		return []connector.ChatBatch{{Chat: kb.SourceChat{ID: "example-org-example-repo-12", Name: "example-repo#12 Add the example", Kind: "pr"},
			Messages: []kb.SourceMessage{{ID: "c1", SenderID: "me", SenderName: "me", Mine: true, Type: "TEXT", Text: "看过了", At: at.Add(time.Hour)}}}}, "2", false, nil
	}
	return nil, cursor, false, nil
}

// A connector's batches become one chat (ingested like mail), its cursor goes on and is kept for the next run.
func TestArchiveConnectorIngestsAndKeepsTheCursor(t *testing.T) {
	st, err := kb.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &kb.Engine{Store: st}
	f := &archiveFake{}
	n, err := archiveConnector(context.Background(), e, "example", f)
	if err != nil || n != 2 {
		t.Fatalf("%d lines, %v", n, err)
	}
	c, ok := st.Chat(kb.ChatID("example", "example-org-example-repo-12"))
	if !ok || c.Kind != "pr" || c.Messages != 2 || !c.Mine || c.Source != "example" {
		t.Fatalf("chat %+v", c)
	}
	text := st.Since(c.ID, time.Time{}, 10000)
	if !strings.Contains(text, "示例描述") || !strings.Contains(text, "我") {
		t.Fatalf("archived:\n%s", text)
	}
	raw, _ := os.ReadFile(filepath.Join(st.Dir, "cursors", "example.json"))
	if string(raw) != `{"cursor":"2"}` {
		t.Fatalf("cursor file %s", raw)
	}
	if _, err := archiveConnector(context.Background(), e, "example", f); err != nil {
		t.Fatal(err)
	}
	if last := f.cursors[len(f.cursors)-1]; last != "2" {
		t.Fatalf("the next run started from %q", last)
	}
}

// Mail's cursors from before (mail-cursors.json) are where its archive goes on from: nothing archived twice; the
// new file takes over, the old one stays.
func TestMailCursorGoesOnFromTheOldFile(t *testing.T) {
	st, _ := kb.NewStore(t.TempDir())
	e := &kb.Engine{Store: st}
	old := `{"acc1":12}`
	if err := os.WriteFile(filepath.Join(st.Dir, "mail-cursors.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	f := &archiveFake{}
	if _, err := archiveConnector(context.Background(), e, "mail", f); err != nil {
		t.Fatal(err)
	}
	if f.cursors[0] != old {
		t.Fatalf("started from %q, not the old cursors", f.cursors[0])
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "cursors", "mail.json")); err != nil {
		t.Fatal("no new cursor file")
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "mail-cursors.json")); err != nil {
		t.Fatal("the old file is gone")
	}
	// Another connector never reads mail's old file.
	g := &archiveFake{}
	if _, err := archiveConnector(context.Background(), e, "example", g); err != nil || g.cursors[0] != "" {
		t.Fatalf("example started from %q, %v", g.cursors[0], err)
	}
}

// mediaArchive hands out one message with two attachments, one of which it can fetch (connector.ArchiveMedia).
type mediaArchive struct{ asked []string }

func (*mediaArchive) Changes(_ context.Context, cursor string, _ int) ([]connector.ChatBatch, string, bool, error) {
	if cursor != "" {
		return nil, cursor, false, nil
	}
	return []connector.ChatBatch{{Chat: kb.SourceChat{ID: "g1", Name: "示例群", Kind: "group"},
		Messages: []kb.SourceMessage{{ID: "m1", SenderName: "示例用户", Type: "FILE", Text: "周报", At: time.Now().Add(-time.Hour),
			Media: []string{"file:ok", "file:gone"}}}}}, "1", false, nil
}

func (a *mediaArchive) ArchiveMedia(_ context.Context, ref string) (connector.Media, error) {
	a.asked = append(a.asked, ref)
	if ref != "file:ok" {
		return connector.Media{}, os.ErrNotExist
	}
	return connector.Media{Mime: "application/pdf", Data: []byte("%PDF-1.7")}, nil
}

// An archived message's attachments come from the connector (archive.media): kept next to the chat and linked from
// the line; one it cannot hand over stays the attachment placeholder.
func TestArchiveConnectorKeepsAttachments(t *testing.T) {
	st, err := kb.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &mediaArchive{}
	if _, err := archiveConnector(context.Background(), &kb.Engine{Store: st}, "example", a); err != nil {
		t.Fatal(err)
	}
	c, _ := st.Chat(kb.ChatID("example", "g1"))
	text := st.Since(c.ID, time.Time{}, 10000)
	if len(a.asked) != 2 || !strings.Contains(text, "[附件](attachments/") || strings.Count(text, "[附件]") != 2 {
		t.Fatalf("asked %v, archived:\n%s", a.asked, text)
	}
	var kept []string
	_ = filepath.Walk(filepath.Join(st.Dir), func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.Contains(p, "attachments") {
			kept = append(kept, p)
		}
		return nil
	})
	if len(kept) != 1 {
		t.Fatalf("attachments kept: %v", kept)
	}
	if b, _ := os.ReadFile(kept[0]); string(b) != "%PDF-1.7" {
		t.Fatalf("attachment holds %q", b)
	}
}

// sourcedArchive hands one batch under the connector's other name, one under a name it never declared.
type sourcedArchive struct{}

func (sourcedArchive) Changes(_ context.Context, cursor string, _ int) ([]connector.ChatBatch, string, bool, error) {
	at := time.Now().Add(-time.Hour)
	msg := []kb.SourceMessage{{ID: "c1", SenderName: "王五", Type: "TEXT", Text: "评论", At: at, Position: "1"}}
	return []connector.ChatBatch{
		{Source: "posts", Chat: kb.SourceChat{ID: "p9", Name: "公司圈 · 帖子", Kind: "moment"}, Messages: msg},
		{Source: "mail", Chat: kb.SourceChat{ID: "x", Name: "冒名", Kind: "mail"}, Messages: msg},
		{Chat: kb.SourceChat{ID: "chat1", Name: "群", Kind: "group"}, Messages: msg},
	}, "1", false, nil
}

// A batch filed under one of the connector's archive sources becomes "<source>-<id>"; one under any other name is
// dropped.
func TestArchiveConnectorFilesUnderItsSources(t *testing.T) {
	st, _ := kb.NewStore(t.TempDir())
	e := &kb.Engine{Store: st}
	if _, err := archiveConnector(context.Background(), e, "feed", sourcedArchive{}, "posts"); err != nil {
		t.Fatal(err)
	}
	if c, ok := st.Chat(kb.ChatID("posts", "p9")); !ok || c.Source != "posts" || c.Connector != "feed" || c.Kind != "moment" {
		t.Fatalf("posts-p9 %+v", c)
	}
	if _, ok := st.Chat(kb.ChatID("mail", "x")); ok {
		t.Fatal("a batch under an undeclared name went in")
	}
	if _, ok := st.Chat(kb.ChatID("feed", "chat1")); !ok {
		t.Fatal("its own chat is missing")
	}
}

// The connectors the hub lists are asked when they are due (their manifest's Poll), not on every round of the loop.
func TestKBConnectorsWhenDue(t *testing.T) {
	f := newFakeHub()
	f.answerApp(AppConnectors, assistantwire.ConnectorsArchives, func(json.RawMessage) (any, error) {
		return assistantwire.Archives{Archives: []assistantwire.ArchiveSource{{ID: "example", PollMS: time.Minute.Milliseconds()}}}, nil
	})
	f.answerApp(AppConnectors, assistantwire.ConnectorsChanges, func(json.RawMessage) (any, error) {
		return assistantwire.ChangesResult{Next: "1"}, nil
	})
	s := newKBService(t, f)
	e := kbEng(t, s)
	s.kbConnectors(context.Background(), e)
	s.kbConnectors(context.Background(), e)
	if n := len(f.appParams(AppConnectors, assistantwire.ConnectorsChanges)); n != 1 {
		t.Fatalf("asked %d times within its minute", n)
	}
	s.kb.mu.Lock()
	s.kb.archivedAt["example"] = time.Now().Add(-2 * time.Minute)
	s.kb.mu.Unlock()
	s.kbConnectors(context.Background(), e)
	p := f.appParams(AppConnectors, assistantwire.ConnectorsChanges)
	if len(p) != 2 || p[0]["id"] != "example" || p[0]["cursor"] != "" || p[1]["cursor"] != "1" || p[1]["limit"] != 20.0 {
		t.Fatalf("not asked again once due, from where it was: %v", p)
	}
}

// What a connector hands over comes through the hub as data: its batches (connectors.changes), under its other names
// too, and its attachments (connectors.archive_media) when the hub says it has them.
func TestKBConnectorsReadThroughTheHub(t *testing.T) {
	f := newFakeHub()
	f.answerApp(AppConnectors, assistantwire.ConnectorsArchives, func(json.RawMessage) (any, error) {
		return assistantwire.Archives{Archives: []assistantwire.ArchiveSource{{ID: "example", Sources: []string{"posts"}, Media: true}}}, nil
	})
	at := time.Now().Add(-time.Hour)
	f.answerApp(AppConnectors, assistantwire.ConnectorsChanges, func(params json.RawMessage) (any, error) {
		var a struct{ Cursor string }
		_ = json.Unmarshal(params, &a)
		if a.Cursor != "" {
			return assistantwire.ChangesResult{Next: a.Cursor}, nil
		}
		return assistantwire.ChangesResult{Next: "1", Batches: []connector.ChatBatch{
			{Chat: kb.SourceChat{ID: "g1", Name: "示例群", Kind: "group"}, Messages: []kb.SourceMessage{{ID: "m1", SenderName: "示例用户",
				Type: "FILE", Text: "周报", At: at, Media: []string{"file:ok"}}}},
			{Source: "posts", Chat: kb.SourceChat{ID: "p9", Name: "公司圈 · 帖子", Kind: "moment"}, Messages: []kb.SourceMessage{{ID: "c1",
				SenderName: "王五", Type: "TEXT", Text: "评论", At: at}}},
		}}, nil
	})
	f.answerApp(AppConnectors, assistantwire.ConnectorsArchiveMedia, func(params json.RawMessage) (any, error) {
		return map[string]any{"media": connector.Media{Mime: "application/pdf", Data: []byte("%PDF-1.7")}}, nil
	})
	s := newKBService(t, f)
	e := kbEng(t, s)
	st := e.Store
	s.kbConnectors(context.Background(), e)
	c, ok := st.Chat(kb.ChatID("example", "g1"))
	if !ok || c.Messages != 1 || !strings.Contains(st.Since(c.ID, time.Time{}, 10000), "[附件](attachments/") {
		t.Fatalf("chat %+v:\n%s", c, st.Since(kb.ChatID("example", "g1"), time.Time{}, 10000))
	}
	if p := f.appParams(AppConnectors, assistantwire.ConnectorsArchiveMedia); len(p) != 1 || p[0]["ref"] != "file:ok" || p[0]["id"] != "example" {
		t.Fatalf("media asked %v", p)
	}
	if _, ok := st.Chat(kb.ChatID("posts", "p9")); !ok {
		t.Fatal("the batch under its other name is missing")
	}
	if raw, _ := os.ReadFile(filepath.Join(st.Dir, "cursors", "example.json")); string(raw) != `{"cursor":"1"}` {
		t.Fatalf("cursor %s", raw)
	}
}

// The hub's connectors with documents, meetings and events are read through it (connectors.items, connectors.doc_title).
func TestKBCollectsItemsThroughTheHub(t *testing.T) {
	f := newFakeHub()
	f.answerApp(AppConnectors, assistantwire.ConnectorsArchives, func(json.RawMessage) (any, error) {
		return assistantwire.Archives{Items: []string{"example"}}, nil
	})
	f.answerApp(AppConnectors, assistantwire.ConnectorsItems, func(json.RawMessage) (any, error) {
		return assistantwire.KBItems{Items: []kbwire.Item{{Kind: kb.KindMeeting, ID: "m1", Title: "周会", At: time.Now()}}}, nil
	})
	f.answerApp(AppConnectors, assistantwire.ConnectorsDocTitle, func(json.RawMessage) (any, error) {
		return map[string]any{"title": "发布计划"}, nil
	})
	s := newKBService(t, f)
	e := kbEng(t, s)
	st := e.Store
	_ = st.PutItem(&kb.Item{Kind: kb.KindDoc, ID: "d1", Source: "example", URL: "https://docs.example.com/d1"})
	s.kbCollectItems(context.Background(), e)
	if it, ok := st.Item(kb.KindMeeting, "m1"); !ok || it.Title != "周会" || it.Source != "example" {
		t.Fatalf("meeting %+v", it)
	}
	if it, ok := st.Item(kb.KindDoc, "d1"); !ok || it.Title != "发布计划" {
		t.Fatalf("document %+v", it)
	}
}
