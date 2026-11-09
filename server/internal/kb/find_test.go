package kb

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFindRanksEntriesAndMatchesLocateMessages(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st}
	day := func(d string, h int) time.Time {
		at, _ := time.ParseInLocation("2006-01-02 15:04", d+" 00:00", time.Local)
		return at.Add(time.Duration(h) * time.Hour)
	}
	low := &Chat{ID: "lark-low", Name: "闲聊群", Kind: "group", Tagging: Tagging{Importance: "P3", Relevance: 10}}
	high := &Chat{ID: "lark-high", Name: "发布群", Kind: "group", Tagging: Tagging{Importance: "P1", Relevance: 80}}
	named := &Chat{ID: "lark-named", Name: "周报提醒", Kind: "bot", Tagging: Tagging{Importance: "P3"}}
	for _, c := range []*Chat{low, high, named} {
		if err := st.PutChat(c); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.AppendMessages(low, []Line{{At: day("2026-10-01", 9), Sender: "甲", Text: "今天写周报了吗"}})
	_ = st.AppendMessages(high, []Line{
		{At: day("2026-10-20", 9), Sender: "乙", Text: "早"},
		{At: day("2026-10-20", 10), Sender: "丙", Text: "周报发出来了\n第二行"},
		{At: day("2026-10-20", 11), Sender: "乙", Text: "收到"},
		{At: day("2026-10-21", 9), Sender: "乙", Text: "下周周报我来写"},
	})

	res := e.Find("周报", 10)
	if len(res) != 3 {
		t.Fatalf("results: %+v", res)
	}
	// The name match first, then by importance.
	if res[0].Entry.ID != "lark-named" || !res[0].InName || res[1].Entry.ID != "lark-high" || res[2].Entry.ID != "lark-low" {
		t.Fatalf("order: %s %s %s", res[0].Entry.ID, res[1].Entry.ID, res[2].Entry.ID)
	}
	if res[1].Count != 2 || len(res[1].Hits) != 2 || !strings.Contains(res[1].Hits[0].Path, "2026-10-21") {
		t.Fatalf("high: %+v", res[1])
	}

	m := e.Matches(KindChat, "lark-high", "周报", 10)
	if len(m) != 2 || m[0].Day != "2026-10-21" || m[1].Day != "2026-10-20" {
		t.Fatalf("matches: %+v", m)
	}
	// The message with its continuation line, and its neighbours.
	if !strings.Contains(m[1].Line, "第二行") || len(m[1].Before) != 1 || len(m[1].After) != 1 || !strings.Contains(m[1].After[0], "收到") {
		t.Fatalf("context: %+v", m[1])
	}

	msgs, prev, next, err := e.Day("lark-high", "2026-10-20")
	if err != nil || len(msgs) != 3 || prev != "" || next != "2026-10-21" {
		t.Fatalf("day: %+v %q %q %v", msgs, prev, next, err)
	}
	found := false
	for _, x := range msgs {
		if x.N == m[1].N {
			found = strings.Contains(x.Text, "周报发出来了")
		}
	}
	if !found {
		t.Fatalf("the match's line %d is not a message of the day: %+v", m[1].N, msgs)
	}
	if _, _, _, err := e.Day("lark-high", "../meta"); err != ErrNotFound {
		t.Fatalf("bad day: %v", err)
	}

	// A doc names the chat it came from; a project's docs come with its links.
	_ = st.PutItem(&Item{Kind: KindDoc, ID: "d1", Title: "周报模板", Where: "lark-high", At: day("2026-10-20", 9)})
	_ = st.PutProject(&Project{Slug: "p1", Name: "P1", Kind: "short", Status: "active", Items: []string{"docs/d1", "docs/gone"}})
	if d, err := e.Get(KindDoc, "d1", 0); err != nil || len(d.Links) != 1 || d.Links[0].ID != "lark-high" {
		t.Fatalf("doc links: %+v %v", d, err)
	}
	if d, err := e.Get(KindProject, "p1", 0); err != nil || len(d.Links) != 1 || d.Links[0].Kind != KindDoc {
		t.Fatalf("project links: %+v %v", d, err)
	}
	if tags := KindTags([]Entry{{Tags: []string{"a", "b"}}, {Tags: []string{"b"}}}, 5); len(tags) != 2 || tags[0] != "b" {
		t.Fatalf("tags: %v", tags)
	}
}

// Find keeps what it read between searches; every result must still be what reading every file gives.
func TestFindCacheFollowsEdits(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st}
	c := &Chat{ID: "lark-a", Name: "Team", Kind: "group"}
	if err := st.PutChat(c); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	_ = st.AppendMessages(c, []Line{{At: at, Sender: "甲", Text: "Alpha ships today"}, {At: at.Add(time.Hour), Sender: "乙", Text: "ok"}})
	dayFile := st.path(KindChat, c.ID, "messages", "2026-10-20.md")
	age := func(d time.Duration) {
		t.Helper()
		if err := ageFiles(st.Dir, d); err != nil {
			t.Fatal(err)
		}
	}
	// same: the warm engine finds what a new one (nothing kept) finds.
	same := func(step, q string) []Result {
		t.Helper()
		got := e.Find(q, 10)
		want := (&Engine{Store: st}).Find(q, 10)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: cached %+v, uncached %+v", step, got, want)
		}
		return got
	}
	age(time.Hour)
	if r := same("first", "alpha"); len(r) != 1 || r[0].Count != 1 {
		t.Fatalf("first: %+v", r)
	}
	if e.find.texts[dayFile] == nil || e.find.known == nil {
		t.Fatal("an old file and the lists are kept")
	}
	if r := same("again", "alpha"); len(r) != 1 || r[0].Count != 1 {
		t.Fatalf("again: %+v", r)
	}

	// Appended (size changes).
	_ = st.AppendMessages(c, []Line{{At: at.Add(2 * time.Hour), Sender: "甲", Text: "alpha again"}})
	if r := same("appended", "alpha"); len(r) != 1 || r[0].Count != 2 {
		t.Fatalf("appended: %+v", r)
	}
	if e.find.texts[dayFile] != nil {
		t.Fatal("a file written just now is not kept")
	}

	// Rewritten to the same size: the mtime tells.
	age(time.Hour)
	same("settled", "alpha")
	raw, _ := os.ReadFile(dayFile)
	if err := os.WriteFile(dayFile, []byte(strings.ReplaceAll(string(raw), "Alpha", "Gamma")), 0o600); err != nil {
		t.Fatal(err)
	}
	// Without aging: only the rewrite's own mtime tells the cached text is stale.
	if r := same("same size, just written", "gamma"); len(r) != 1 || r[0].Count != 1 {
		t.Fatalf("same size, just written: %+v", r)
	}
	age(30 * time.Minute)
	if r := same("same size", "gamma"); len(r) != 1 || r[0].Count != 1 {
		t.Fatalf("same size: %+v", r)
	}

	// A new day (a new file in a kept directory listing).
	_ = st.AppendMessages(c, []Line{{At: at.AddDate(0, 0, 1), Sender: "乙", Text: "gamma tomorrow"}})
	// Without aging: the messages directory's own mtime tells its kept listing is stale.
	if r := same("new day, just written", "gamma"); len(r) != 1 || r[0].Count != 2 {
		t.Fatalf("new day, just written: %+v", r)
	}
	age(20 * time.Minute)
	if r := same("new day", "gamma"); len(r) != 1 || r[0].Count != 2 || !strings.Contains(r[0].Hits[0].Path, "2026-10-21") {
		t.Fatalf("new day: %+v", r)
	}
	same("new day, kept", "gamma") // the listings were read again after aging; kept from now on
	if len(e.find.dirs) == 0 || !e.find.walked {
		t.Fatal("settled directory listings and the walk are kept")
	}

	// The chat renamed: the lists' entries are built again.
	c.Name = "Alpha squad"
	if err := st.PutChat(c); err != nil {
		t.Fatal(err)
	}
	age(10 * time.Minute)
	if r := same("renamed", "squad"); len(r) != 1 || !r[0].InName || r[0].Entry.Name != "Alpha squad" {
		t.Fatalf("renamed: %+v", r)
	}

	// The day deleted: its lines go, and so does what was kept of it.
	if err := os.Remove(dayFile); err != nil {
		t.Fatal(err)
	}
	if r := same("deleted", "gamma"); len(r) != 1 || r[0].Count != 1 || !strings.Contains(r[0].Hits[0].Path, "2026-10-21") {
		t.Fatalf("deleted: %+v", r)
	}
	if e.find.texts[dayFile] != nil {
		t.Fatal("a deleted file is dropped")
	}
	n := 0
	for _, x := range e.find.texts {
		n += x.cost()
	}
	if n != e.find.bytes {
		t.Fatalf("bytes %d, kept %d", e.find.bytes, n)
	}
}

// files must list what walking the whole data dir lists, in the same order, kept or not.
func TestFindFilesMatchesWalk(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st}
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	for _, id := range []string{"lark-b", "lark-a", "mail-x"} {
		c := &Chat{ID: id, Name: id, Kind: "group"}
		_ = st.PutChat(c)
		for d := range 3 {
			_ = st.AppendMessages(c, []Line{{At: at.AddDate(0, 0, -d), Sender: "甲", Text: "hi"}})
		}
	}
	_ = st.PutSummary(KindChat, "lark-a", "# lark-a\n\nsummary\n")
	_ = os.MkdirAll(st.path(KindChat, "lark-a", "attachments"), 0o700)
	_ = os.WriteFile(st.path(KindChat, "lark-a", "attachments", "a.md"), []byte("skipped"), 0o600)
	_ = st.PutItem(&Item{Kind: KindDoc, ID: "d1", Title: "Doc"})
	_ = st.PutPerson(&Person{ID: "lark-u1", Name: "U"})
	walk := func() (files, days []string) {
		sep := string(filepath.Separator)
		_ = filepath.WalkDir(st.Dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if d.Name() == "attachments" {
					return filepath.SkipDir
				}
				return nil
			}
			switch {
			case strings.Contains(p, sep+"messages"+sep):
				days = append(days, p)
			case strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".json"):
				files = append(files, p)
			}
			return nil
		})
		sort.SliceStable(days, func(i, j int) bool { return filepath.Base(days[i]) > filepath.Base(days[j]) })
		return files, days
	}
	check := func(step string) {
		t.Helper()
		w := e.files()
		f, d := walkPaths(w.files), walkPaths(w.days)
		wf, wd := walk()
		if !reflect.DeepEqual(f, wf) || !reflect.DeepEqual(d, wd) {
			t.Fatalf("%s:\nfiles %v\nwalk  %v\ndays %v\nwalk %v", step, f, wf, d, wd)
		}
	}
	check("fresh")
	if err := ageFiles(st.Dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	check("settled")
	check("kept")
	if !e.find.walked {
		t.Fatal("the walk is kept once nothing changed")
	}
	_ = os.RemoveAll(st.path(KindChat, "mail-x"))
	check("chat removed")
	_ = st.AppendMessages(&Chat{ID: "lark-b"}, []Line{{At: at.AddDate(0, 0, 1), Sender: "甲", Text: "new"}})
	check("day added")
}

// The hub answers searches concurrently (one request per keystroke): the cache is shared by them (run with -race).
func TestFindConcurrent(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	for _, id := range []string{"lark-a", "lark-b"} {
		c := &Chat{ID: id, Name: id, Kind: "group"}
		_ = st.PutChat(c)
		for d := range 5 {
			_ = st.AppendMessages(c, []Line{{At: at.AddDate(0, 0, -d), Sender: "甲", Text: "alpha beta"}})
		}
	}
	if err := ageFiles(st.Dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st}
	queries := []string{"alpha", "alpha beta"}
	want := make([][]Result, len(queries))
	for i, q := range queries {
		want[i] = (&Engine{Store: st}).Find(q, 10)
	}
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for g := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 8 {
				q := g % len(queries)
				if got := e.Find(queries[q], 10); !reflect.DeepEqual(got, want[q]) {
					errs <- fmt.Sprintf("%q: %+v, want %+v", queries[q], got, want[q])
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for s := range errs {
		t.Fatalf("concurrent search: %s", s)
	}
}

// Every result Find gives must be locatable once opened: Find only counts lines in the files Matches reads, so a hit
// in an entry's bookkeeping (a project's moments-judged.json, a person's handles.json) is not a result whose detail
// says "no matches" (docs/assistant/knowledge.md "Search").
func TestFindAgreesWithMatches(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st}
	at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	if err := st.PutProject(&Project{Slug: "pp", Name: "PP", Kind: "short", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutPerson(&Person{ID: "lark-u1", Name: "U"}); err != nil {
		t.Fatal(err)
	}
	ch := &Chat{ID: "lark-c1", Name: "C", Kind: "group"}
	if err := st.PutChat(ch); err != nil {
		t.Fatal(err)
	}
	write := func(p, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(st.path(KindProject, "pp", "moments-judged.json"), `{"post-1":"zebrapost"}`)
	write(st.path(KindPerson, "lark-u1", "handles.json"), `["handlezebra"]`)
	write(st.path(KindProject, "pp", "notes.md"), "strayzebra\n")
	write(st.path(KindPerson, "lark-u1", "messages", "2026-10-20.md"), "- 09:00 U: personzebra\n")
	if err := st.PutSummary(KindProject, "pp", "# PP\n\nsummaryzebra here\n"); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendMessages(ch, []Line{{At: at, Sender: "甲", Text: "chatzebra"}}); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, query, kind, id string
		count                 int // lines Find counts, and Matches shows, for kind/id
	}{
		{"moments-judged.json", "zebrapost", KindProject, "pp", 0},
		{"handles.json", "handlezebra", KindPerson, "lark-u1", 0},
		{"stray file", "strayzebra", KindProject, "pp", 0},
		{"messages outside a chat", "personzebra", KindPerson, "lark-u1", 0},
		{"summary line", "summaryzebra", KindProject, "pp", 1},
		{"chat message", "chatzebra", KindChat, "lark-c1", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := e.Find(c.query, 10)
			got := 0
			for _, r := range res {
				m := e.Matches(r.Entry.Kind, r.Entry.ID, c.query, 40)
				if r.Count > 0 && len(m) == 0 {
					t.Fatalf("Find counts %d in %s/%s, Matches shows none", r.Count, r.Entry.Kind, r.Entry.ID)
				}
				if r.Entry.Kind == c.kind && r.Entry.ID == c.id {
					got = r.Count
				}
			}
			if m := e.Matches(c.kind, c.id, c.query, 40); got != c.count || len(m) != c.count {
				t.Fatalf("Find count %d, Matches %+v; want %d", got, m, c.count)
			}
		})
	}
}

// A limit of zero or less is no results, not a panic on out[:limit].
func TestFindNonPositiveLimit(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(&Project{Slug: "pp", Name: "PP", Kind: "short", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSummary(KindProject, "pp", "# PP\n\nx marks it\n"); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st}
	if res := e.Find("x", 1); len(res) != 1 {
		t.Fatalf("limit 1: %+v", res)
	}
	for _, limit := range []int{0, -1} {
		if res := e.Find("x", limit); len(res) != 0 {
			t.Fatalf("limit %d: %+v", limit, res)
		}
	}
}
