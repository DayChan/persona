package kb

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// searchByWalk is Search as it was before it took the data dir's files from Find's walk: filepath.WalkDir over the
// whole data dir on every call, every file read afresh. Search must still return what it returns, but for the files
// under history/ (bookkeeping, never written as days by the store) and a data dir reached through a symlink, which
// WalkDir did not enter.
func searchByWalk(e *Engine, query string, messages bool, limit int) []Hit {
	match := matcher(query)
	if match == nil {
		return nil
	}
	ws := words(query)
	var hits []Hit
	scan := func(p string) {
		if len(hits) >= limit {
			return
		}
		raw, err := os.ReadFile(p)
		if err != nil || !hasAll(fold(string(raw)), ws) {
			return
		}
		rel, err := filepath.Rel(e.Store.Dir, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = p
		}
		for _, l := range strings.Split(string(raw), "\n") {
			if len(hits) >= limit {
				return
			}
			if match(l) {
				hits = append(hits, Hit{Path: rel, Line: clip(strings.TrimSpace(l), 300)})
			}
		}
	}
	var files, days, moments []string
	sep := string(filepath.Separator)
	_ = filepath.WalkDir(e.Store.Dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(e.Store.Dir, p)
		switch {
		case strings.Contains(p, sep+"moments"+sep+"posts"+sep):
			if strings.HasSuffix(p, ".md") {
				moments = append(moments, p)
			}
		case strings.Contains(p, sep+"attachments"+sep):
		case strings.Contains(p, sep+"messages"+sep):
			if messages {
				days = append(days, p)
			}
		case filepath.Base(p) == "segments.json":
		case rerr == nil && !searchable(rel):
		case strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".json"):
			files = append(files, p)
		}
		return nil
	})
	for _, dir := range e.ExtraDirs {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && p != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(p, ".md") {
				files = append(files, p)
			}
			return nil
		})
	}
	for _, p := range files {
		scan(p)
	}
	sort.Slice(days, func(i, j int) bool { return filepath.Base(days[i]) > filepath.Base(days[j]) })
	for _, p := range days {
		scan(p)
	}
	sort.Slice(moments, func(i, j int) bool { return moments[i] > moments[j] })
	for _, p := range moments {
		scan(p)
	}
	return hits
}

// edgeEngine is a data dir with every kind of file the walks tell apart (ExtraDirs too), each line saying "alpha" and
// where it is.
func edgeEngine(t *testing.T) *Engine {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	extra := t.TempDir()
	write := func(root, rel string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("# heading\nalpha "+rel+" one\n\n  Alpha "+rel+" two  \n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"lark-b", "lark-a"} {
		if err := st.PutChat(&Chat{ID: id, Name: "alpha " + id, Kind: "group"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutPerson(&Person{ID: "lark-u1", Name: "alpha person"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(&Project{Slug: "p1", Name: "alpha project"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutItem(&Item{Kind: KindDoc, ID: "d1", Title: "alpha doc"}); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		"README.md",
		"notes.txt",
		"chats/lark-a/summary.md", "chats/lark-a/log.md",
		"chats/lark-a/messages/2026-10-01.md", "chats/lark-a/messages/2026-10-02.md",
		"chats/lark-b/messages/2026-10-01.md", "chats/lark-b/messages/2026-10-03.md",
		"chats/lark-a/messages/notes.txt", "chats/lark-a/messages/sub/2026-10-04.md",
		"chats/lark-a/attachments/a.md", "chats/lark-a/attachments/messages/2026-10-05.md",
		"attachments/top.md", "attachments/messages/2026-10-06.md",
		"cursors/imessage.json",
		"history/state.json", "history/lark-a/messages/2026-10-07.md",
		"people/lark-u1/handles.json", "people/lark-u1/summary.md",
		"projects/p1/moments-judged.json", "projects/p1/log.md",
		"recordings/r1/transcript.md", "recordings/r1/segments.json", "recordings/r1/summary.md",
		"moments/posts/001.md", "moments/posts/002.md", "moments/posts/002.json", "moments/posts/003.txt",
		"moments/index.md",
		"docs/sub/x.md", "docs/d2.md",
	} {
		write(st.Dir, rel)
	}
	for _, rel := range []string{"guide.md", "sub/b.md", ".git/HEAD.md", "notes.txt"} {
		write(extra, rel)
	}
	if err := ageFiles(st.Dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: st, ExtraDirs: []string{extra}}
}

// compareSearch checks Search against the old walk's hits but those under history/, on e (warm: it searched before)
// with each limit, and on a new engine (cold) with the largest. Search stops at the limit, so with a smaller one it
// returns the first of them.
func compareSearch(t *testing.T, step string, e *Engine, queries []string, limits []int) {
	t.Helper()
	most := slices.Max(limits)
	for _, q := range queries {
		for _, messages := range []bool{true, false} {
			var all []Hit
			for n := most; ; n *= 2 { // enough of the old hits for most once history's are dropped
				old := searchByWalk(e, q, messages, n)
				all = all[:0]
				for _, h := range old {
					if !strings.HasPrefix(filepath.ToSlash(h.Path), "history/") {
						all = append(all, h)
					}
				}
				if len(all) >= most || len(old) < n {
					break
				}
			}
			check := func(name string, e *Engine, limit int) {
				t.Helper()
				want := all[:min(limit, len(all))]
				if len(want) == 0 {
					want = nil
				}
				if got := e.Search(q, messages, limit); !reflect.DeepEqual(got, want) {
					t.Fatalf("%s, %s: Search(%q, %v, %d):\n got %d %+v\nwant %d %+v", step, name, q, messages, limit,
						len(got), got, len(want), want)
				}
			}
			check("cold", &Engine{Store: e.Store, ExtraDirs: e.ExtraDirs}, most)
			for _, limit := range limits {
				check("warm", e, limit)
			}
		}
	}
}

// Search returns what walking the whole data dir returned, in the same order: the files, then the days (sorted as
// before, same-named days of different chats included), then the Moments posts.
func TestSearchMatchesTheWalk(t *testing.T) {
	e := edgeEngine(t)
	if hits := e.Search("alpha", true, 1000); len(hits) == 0 {
		t.Fatal("nothing found")
	}
	limits := []int{0, 1, 2, 5, 9, 17, 40, 1000}
	compareSearch(t, "edge", e, []string{"alpha", "ALPHA two", "alpha 2026-10-01", "nowhere"}, limits)
	e.Find("alpha", 10) // Find shares the walk and the texts
	compareSearch(t, "edge after Find", e, []string{"alpha"}, limits)
	for _, h := range e.Search("alpha", true, 1000) {
		if strings.HasPrefix(filepath.ToSlash(h.Path), "history/") {
			t.Fatalf("history is not searched: %+v", h)
		}
	}
	day := e.Store.path(KindChat, "lark-b", "messages", "2026-10-09.md")
	if err := os.WriteFile(day, []byte("- 09:00 甲: alpha zqxnewday\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if hits := e.Search("zqxnewday", true, 10); len(hits) != 1 || hits[0].Path != filepath.Join(KindChat, "lark-b", "messages", "2026-10-09.md") {
		t.Fatalf("a day just created: %+v", hits)
	}
	compareSearch(t, "day created", e, []string{"alpha"}, limits)
	if err := os.Remove(day); err != nil {
		t.Fatal(err)
	}
	if hits := e.Search("zqxnewday", true, 10); len(hits) != 0 {
		t.Fatalf("a day just deleted: %+v", hits)
	}
	compareSearch(t, "day deleted", e, []string{"alpha"}, limits)
}

// The same on the benchmark archive: a hundred chats with a day file each for the same dates, where how the days of one
// date are ordered depends on the sort.
func TestSearchMatchesTheWalkOnTheArchive(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the ~37k-file benchmark archive")
	}
	e := &Engine{Store: benchStore(t)}
	compareSearch(t, "archive", e, []string{"周报 draft", "build"}, []int{1, 40, 500})
}

// A data dir reached through a symlink is searched (filepath.WalkDir did not enter it).
func TestSearchThroughASymlinkedDataDir(t *testing.T) {
	e := edgeEngine(t)
	link := filepath.Join(t.TempDir(), "data")
	if err := os.Symlink(e.Store.Dir, link); err != nil {
		t.Skip("no symlinks here:", err)
	}
	want := e.Search("alpha", true, 1000)
	if got := (&Engine{Store: &Store{Dir: link}, ExtraDirs: e.ExtraDirs}).Search("alpha", true, 1000); !reflect.DeepEqual(got, want) {
		t.Fatalf("through the link:\n got %+v\nwant %+v", got, want)
	}
}

func walkPaths(ents []*dirEnt) []string {
	var out []string
	for _, d := range ents {
		out = append(out, d.path)
	}
	return out
}

// filesByWalk is files as it was before the walk kept each file's entry: what Find looked at, in order (profile files,
// then the days newest first), as paths.
func filesByWalk(e *Engine) []string {
	var files, days []string
	sep := string(filepath.Separator)
	var walk func(dir string)
	walk = func(dir string) {
		des, _ := os.ReadDir(dir)
		for _, d := range des {
			p := filepath.Join(dir, d.Name())
			rel, _ := filepath.Rel(e.Store.Dir, p)
			switch {
			case d.IsDir():
				if d.Name() != "attachments" && (dir != e.Store.Dir || d.Name() != "history") {
					walk(p)
				}
			case strings.Contains(p, sep+"messages"+sep):
				days = append(days, p)
			case d.Name() == "segments.json":
			case !searchable(rel):
			case strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".json"):
				files = append(files, p)
			}
		}
	}
	walk(e.Store.Dir)
	sort.SliceStable(days, func(i, j int) bool { return filepath.Base(days[i]) > filepath.Base(days[j]) })
	return append(files, days...)
}

// Find looks at the files it did, in the same order, and what the walk keeps of each is what Find worked out for
// it on every search: the slash path and the entry of the files Matches reads.
func TestFindWalkKeepsWhatFindWorkedOut(t *testing.T) {
	check := func(step string, e *Engine) {
		t.Helper()
		w := e.files()
		if got, want := walkPaths(w.all), filesByWalk(e); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s:\n got %v\nwant %v", step, got, want)
		}
		for _, d := range w.all {
			rel, _ := filepath.Rel(e.Store.Dir, d.path)
			key := ""
			if kind, id, ok := entryOf(rel); ok && shown(kind, rel) {
				key = kind + "/" + id
			}
			got := ""
			if d.kind != "" {
				got = d.kind + "/" + d.id
			}
			if d.rel != filepath.ToSlash(rel) || got != key || d.name != filepath.Base(d.path) {
				t.Fatalf("%s: %s kept as rel %q entry %q name %q, want %q %q", step, d.path, d.rel, got, d.name, filepath.ToSlash(rel), key)
			}
		}
	}
	e := edgeEngine(t)
	check("edge", e)
	check("edge, kept", e)
	if !testing.Short() {
		check("archive", &Engine{Store: benchStore(t)})
	}
}

// Find gives what it gave before on the edge data dir, warm or cold: the same as a new engine, and the files it
// counts lines from are the ones filesByWalk lists.
func TestFindOnTheEdgeDataDir(t *testing.T) {
	e := edgeEngine(t)
	for _, q := range []string{"alpha", "alpha two", "2026-10-01", "nowhere"} {
		want := (&Engine{Store: e.Store}).Find(q, 100)
		for range 2 {
			if got := e.Find(q, 100); !reflect.DeepEqual(got, want) {
				t.Fatalf("Find(%q):\n got %+v\nwant %+v", q, got, want)
			}
		}
	}
	var paths []string
	for _, r := range e.Find("alpha", 100) {
		for _, h := range r.Hits {
			paths = append(paths, h.Path)
		}
	}
	for _, p := range paths {
		if strings.Contains(p, "attachments") || strings.HasPrefix(p, "history/") || strings.Contains(p, "handles.json") {
			t.Fatalf("Find returned %s", p)
		}
	}
}
