package kb

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

func TestForgetConnectorArchiveEntities(t *testing.T) {
	e := testEngine(t, nil)
	s := e.Store
	at := time.Now().Add(-time.Hour)
	for _, c := range []Chat{
		{ID: "example-chat", Source: "example", Name: "Connector chat"},
		{ID: "feed-chat", Source: "feed", Name: "Alias chat"},
		{ID: "shared-owned", Source: "shared", Connector: "example", Name: "Owned alias"},
		{ID: "feed-other", Source: "feed", Connector: "other", Name: "Other owner"},
		{ID: "shared-legacy", Source: "shared", Name: "Unknown owner"},
		{ID: "example-extra-chat", Source: "example-extra", Name: "Prefix neighbor"},
	} {
		if err := s.PutChat(&c); err != nil {
			t.Fatal(err)
		}
		if err := s.PutHistoryState(c.ID, HistoryState{Source: c.Source}); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []Person{
		{ID: "feed-person", Source: "feed"},
		{ID: "shared-person", Source: "shared", Connector: "example"},
		{ID: "feed-other", Source: "feed", Connector: "other", Chats: []string{"feed-other", "example-chat"}},
	} {
		if err := s.PutPerson(&p); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{KindDoc, KindMeeting, KindEvent} {
		for _, it := range []Item{
			{Kind: kind, ID: "owned", Source: "example", Title: "Remove owned"},
			{Kind: kind, ID: "legacy-chat", Where: "feed-chat", Title: "Remove linked legacy"},
			{Kind: kind, ID: "other", Source: "other", Where: "feed-chat", Title: "Keep other"},
			{Kind: kind, ID: "unknown", Where: "calendar", Title: "Keep unknown"},
		} {
			if err := s.PutItem(&it); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, p := range []MomentPost{
		{ID: "owned", Source: "example", Text: "Remove post", At: at},
		{ID: "other", Source: "other", Text: "Keep post", At: at},
		{ID: "legacy", Text: "Keep unknown post", At: at},
	} {
		if err := s.PutMomentPost(&p, []MomentComment{{Text: "Comment", At: at}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.WriteMomentIndex(); err != nil {
		t.Fatal(err)
	}
	p := &Project{Slug: "project", Chats: []string{"feed-chat", "feed-other"}, People: []string{"feed-person", "feed-other"},
		Items: []string{"docs/owned", "docs/other"}, Moments: []ProjectMoment{{ID: "owned"}, {ID: "other"}},
		Merge: &ProjectMerge{Chats: []string{"feed-chat", "feed-other"}, People: []string{"feed-person", "feed-other"},
			Items: []string{"docs/owned", "docs/other"}, Moments: []string{"owned", "other"},
			Evicted: []ProjectMoment{{ID: "owned"}, {ID: "other"}}, Moved: []string{"chats/feed-chat", "docs/owned", "docs/other"}}}
	if err := s.PutProject(p); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(s.judgedPath(p.Slug), map[string]bool{"owned": true, "other": true}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog(p.Slug, at, "Alias chat", "Remove timeline"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLog(p.Slug, at, "Other owner", "Keep timeline"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"example", "feed", "other"} {
		if err := writeJSON(filepath.Join(s.Dir, "cursors", name+".json"), map[string]string{"cursor": "saved"}); err != nil {
			t.Fatal(err)
		}
	}
	f, err := e.ForgetSource("example", "feed")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Chats) != 3 || len(f.People) != 2 || len(f.Posts) != 1 || len(f.Projects) != 1 {
		t.Fatalf("forgotten: %+v", f)
	}
	for _, id := range []string{"example-chat", "feed-chat", "shared-owned"} {
		if _, ok := s.Chat(id); ok {
			t.Errorf("chat %s remains", id)
		}
		if _, ok, err := s.HistoryState(id); err != nil || ok {
			t.Errorf("history %s: %v, %v", id, ok, err)
		}
	}
	for _, id := range []string{"feed-other", "shared-legacy", "example-extra-chat"} {
		if _, ok := s.Chat(id); !ok {
			t.Errorf("unrelated chat %s removed", id)
		}
		if _, ok, err := s.HistoryState(id); err != nil || !ok {
			t.Errorf("unrelated history %s: %v, %v", id, ok, err)
		}
	}
	for _, kind := range []string{KindDoc, KindMeeting, KindEvent} {
		if len(f.Items[kind]) != 2 {
			t.Errorf("%s removed: %v", kind, f.Items[kind])
		}
		for _, id := range []string{"owned", "legacy-chat"} {
			if _, ok := s.Item(kind, id); ok {
				t.Errorf("%s/%s remains", kind, id)
			}
		}
		for _, id := range []string{"other", "unknown"} {
			if _, ok := s.Item(kind, id); !ok {
				t.Errorf("unrelated %s/%s removed", kind, id)
			}
		}
	}
	for _, ext := range []string{".json", ".md"} {
		if _, err := os.Stat(filepath.Join(s.momentsDir(), "owned"+ext)); !os.IsNotExist(err) {
			t.Errorf("post %s remains: %v", ext, err)
		}
	}
	index, err := os.ReadFile(filepath.Join(s.Dir, "moments", "index.jsonl"))
	if err != nil || strings.Contains(string(index), "Remove post") || !strings.Contains(string(index), "Keep post") {
		t.Errorf("post index: %s, %v", index, err)
	}
	p, _ = s.Project("project")
	if !p.Dirty || len(p.Chats) != 1 || p.Chats[0] != "feed-other" || len(p.Items) != 1 || p.Items[0] != "docs/other" || len(p.Moments) != 1 || p.Moments[0].ID != "other" {
		t.Errorf("project: %+v", p)
	}
	if len(p.Merge.Chats) != 1 || len(p.Merge.People) != 1 || len(p.Merge.Items) != 1 || len(p.Merge.Moments) != 1 || len(p.Merge.Evicted) != 1 || !slices.Equal(p.Merge.Moved, []string{"docs/other"}) {
		t.Errorf("merge retains removed references: %+v", p.Merge)
	}
	if judged := s.judged(p.Slug); judged["owned"] || !judged["other"] {
		t.Errorf("judged: %v", judged)
	}
	if log := s.Log(p.Slug, 10); len(log) != 1 || !strings.Contains(log[0], "Keep timeline") {
		t.Errorf("timeline: %v", log)
	}
	if other, _ := s.Person("feed-other"); !slices.Equal(other.Chats, []string{"feed-other"}) {
		t.Errorf("reverse links: %+v", other)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "cursors", "other.json")); err != nil {
		t.Error(err)
	}
	for _, name := range []string{"example", "feed"} {
		if _, err := os.Stat(filepath.Join(s.Dir, "cursors", name+".json")); !os.IsNotExist(err) {
			t.Errorf("cursor %s: %v", name, err)
		}
	}
	again, err := e.ForgetSource("example", "feed")
	if err != nil || len(again.Chats)+len(again.People)+len(again.Posts)+len(again.Items)+len(again.Projects) != 0 {
		t.Errorf("repeat: %+v, %v", again, err)
	}
}

type connectorItemSource struct {
	items  []kbwire.Item
	titles []string
}

func (s *connectorItemSource) Items(context.Context) ([]kbwire.Item, error) { return s.items, nil }
func (s *connectorItemSource) DocTitle(_ context.Context, url string) string {
	s.titles = append(s.titles, url)
	return "Resolved title"
}

func TestCollectItemsConnectorOwnership(t *testing.T) {
	e := testEngine(t, nil)
	if err := e.Store.PutChat(&Chat{ID: "feed-chat", Source: "feed", Connector: "example"}); err != nil {
		t.Fatal(err)
	}
	for _, it := range []Item{
		{Kind: KindEvent, ID: "owned", Source: "example", Title: "Unchanged"},
		{Kind: KindEvent, ID: "other", Source: "other", Title: "Other title"},
		{Kind: KindEvent, ID: "legacy", Title: "Unknown title", Where: "calendar"},
		{Kind: KindEvent, ID: "linked", Title: "Linked title", Where: "feed-chat"},
		{Kind: KindDoc, ID: "doc", Source: "example", URL: "https://example.com/doc"},
		{Kind: KindDoc, ID: "linked-doc", Where: "feed-chat", URL: "https://example.com/linked"},
		{Kind: KindDoc, ID: "other-doc", Source: "other", URL: "https://example.com/other"},
		{Kind: KindDoc, ID: "unknown-doc", URL: "https://example.com/unknown"},
	} {
		if err := e.Store.PutItem(&it); err != nil {
			t.Fatal(err)
		}
	}
	src := &connectorItemSource{items: []kbwire.Item{
		{Kind: KindEvent, ID: "new", Source: "spoofed", Title: "New event"},
		{Kind: KindEvent, ID: "owned", Title: "Unchanged"},
		{Kind: KindEvent, ID: "other", Source: "other", Title: "Must not replace"},
		{Kind: KindEvent, ID: "legacy", Title: "Must not claim"},
		{Kind: KindEvent, ID: "linked", Title: "Linked title"},
	}}
	if err := e.CollectItems(context.Background(), src, "example"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"new", "owned", "linked"} {
		if it, _ := e.Store.Item(KindEvent, id); it.Source != "example" {
			t.Errorf("%s attribution: %+v", id, it)
		}
	}
	if it, _ := e.Store.Item(KindEvent, "other"); it.Source != "other" || it.Title != "Other title" {
		t.Errorf("other overwritten: %+v", it)
	}
	if it, _ := e.Store.Item(KindEvent, "legacy"); it.Source != "" || it.Title != "Unknown title" {
		t.Errorf("legacy claimed: %+v", it)
	}
	if it, _ := e.Store.Item(KindDoc, "linked-doc"); it.Source != "example" || it.Title != "Resolved title" {
		t.Errorf("legacy linked document: %+v", it)
	}
	if !slices.Equal(src.titles, []string{"https://example.com/doc", "https://example.com/linked"}) {
		t.Errorf("title lookups crossed owners: %v", src.titles)
	}
}

type ownedArchiveSource struct {
	named
	owner string
}

func (s ownedArchiveSource) ConnectorID() string { return s.owner }

func TestIngestAttributesConnectorAndRejectsTakeover(t *testing.T) {
	e := testEngine(t, nil)
	at := time.Now().Add(-time.Hour)
	src := ownedArchiveSource{named: "feed", owner: "example"}
	chat := SourceChat{ID: "chat", Name: "Example chat"}
	messages := []SourceMessage{{ID: "one", At: at, SenderID: "person", SenderName: "Example sender", Text: "Synthetic text"}}
	if _, err := e.Ingest(context.Background(), src, chat, messages); err != nil {
		t.Fatal(err)
	}
	c, _ := e.Store.Chat("feed-chat")
	if c.Connector != "example" {
		t.Fatalf("chat attribution: %+v", c)
	}
	if p, _ := e.Store.Person("feed-person"); p.Connector != "example" {
		t.Errorf("person attribution: %+v", p)
	}
	e.noteDoc("https://example.com/doc", c, at)
	if it, _ := e.Store.Item(KindDoc, "doc"); it.Source != "example" {
		t.Errorf("document attribution: %+v", it)
	}
	src.owner = "other"
	if _, err := e.Ingest(context.Background(), src, chat, messages); err == nil {
		t.Fatal("another connector took over the alias chat")
	}
}

func TestForgetConnectorRefusesReadOnlyAndUnsafeSource(t *testing.T) {
	e := testEngine(t, nil)
	if _, err := e.ForgetSource("../outside"); err == nil {
		t.Fatal("unsafe source accepted")
	}
	readOnly, err := OpenReadOnly(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&Engine{Store: readOnly}).ForgetSource("example", "feed"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only deletion: %v", err)
	}
}

// The model returns after deletion, with now-stale references in its answer.
type forgettingConnectorModel struct {
	engine *Engine
	answer string
}

func (m forgettingConnectorModel) JSONMax(_ context.Context, _, _ string, _ int, out any) error {
	if _, err := m.engine.ForgetSource("example"); err != nil {
		return err
	}
	return json.Unmarshal([]byte(m.answer), out)
}

func TestTagItemsDoesNotRestoreForgottenConnector(t *testing.T) {
	e := testEngine(t, nil)
	if err := e.Store.PutProject(&Project{Slug: "project", Name: "Project", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.PutItem(&Item{Kind: KindEvent, ID: "event", Source: "example", Title: "Event"}); err != nil {
		t.Fatal(err)
	}
	e.Model = forgettingConnectorModel{engine: e, answer: `{"items":[{"id":"events/event","project":"Project","importance":"P1"}]}`}
	e.Index = func(_, _, _ string, _ Tagging, _ string, _ time.Time) { t.Error("deleted item indexed again") }
	if err := e.TagItems(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, exists := e.Store.Item(KindEvent, "event"); exists {
		t.Error("deleted item restored")
	}
	if p, _ := e.Store.Project("project"); len(p.Items) != 0 {
		t.Errorf("deleted item relinked: %v", p.Items)
	}
}

func TestMomentJudgmentDoesNotRestoreForgottenConnector(t *testing.T) {
	e := testEngine(t, nil)
	p := &Project{Slug: "project", Name: "Project", Status: "active"}
	if err := e.Store.PutProject(p); err != nil {
		t.Fatal(err)
	}
	post := &MomentPost{ID: "post", Source: "example", Text: "Project update", At: time.Now().Add(-time.Hour)}
	if err := e.Store.PutMomentPost(post, nil); err != nil {
		t.Fatal(err)
	}
	e.Model = forgettingConnectorModel{engine: e, answer: `{"posts":[{"id":"post","related":true,"note":"Stale answer"}]}`}
	if n, err := e.judgeMoments(context.Background(), p, []*MomentPost{post}, map[string]bool{}); err != nil || n != 0 {
		t.Fatalf("judgment: %d, %v", n, err)
	}
	if p, _ := e.Store.Project("project"); len(p.Moments) != 0 {
		t.Errorf("deleted post relinked: %v", p.Moments)
	}
	if len(e.Store.Log("project", 10)) != 0 || e.Store.judged("project")["post"] {
		t.Error("deleted post restored to project bookkeeping")
	}
}
