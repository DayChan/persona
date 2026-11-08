package kb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A synthetic archive a little over the size the search was written for (~33 MB of text in ~37k files; the real one
// is ~25 MB, docs/assistant/knowledge.md): benchChats chats with a year of day files each, plus people, projects and docs.
// Built once per test binary; every benchmark reads it.
const (
	benchChats  = 100
	benchDays   = 365
	benchLines  = 12 // lines per day file (~60 bytes each, Chinese and ASCII mixed)
	benchPeople = 300
	benchDocs   = 200
)

var (
	benchOnce sync.Once
	benchDir  string
	benchErr  error
)

func benchStore(b *testing.B) *Store {
	b.Helper()
	benchOnce.Do(func() {
		benchDir, benchErr = os.MkdirTemp("", "kb-find-bench-")
		if benchErr != nil {
			return
		}
		benchErr = fillBenchStore(benchDir)
	})
	if benchErr != nil {
		b.Fatal(benchErr)
	}
	st, err := NewStore(benchDir)
	if err != nil {
		b.Fatal(err)
	}
	return st
}

func fillBenchStore(dir string) error {
	st, err := NewStore(dir)
	if err != nil {
		return err
	}
	start := time.Date(2025, 11, 1, 0, 0, 0, 0, time.UTC)
	for c := range benchChats {
		ch := &Chat{ID: fmt.Sprintf("lark-c%03d", c), Name: fmt.Sprintf("群聊 %03d", c), Kind: "group",
			Tagging: Tagging{Importance: fmt.Sprintf("P%d", c%4), Relevance: c % 100}, LastAt: start.AddDate(0, 0, benchDays)}
		if err := st.PutChat(ch); err != nil {
			return err
		}
		if err := os.WriteFile(st.path(KindChat, ch.ID, "summary.md"), []byte("# "+ch.Name+"\n\n讨论发布计划和日常事务。\n"), 0o600); err != nil {
			return err
		}
		msgs := st.path(KindChat, ch.ID, "messages")
		if err := os.MkdirAll(msgs, 0o700); err != nil {
			return err
		}
		var sb strings.Builder
		for d := range benchDays {
			day := start.AddDate(0, 0, d).Format("2006-01-02")
			sb.Reset()
			sb.WriteString("# " + day + "\n")
			for l := range benchLines {
				text := "今天的进度同步一下 build ok, next step review"
				if (c*benchDays+d)%97 == 0 && l == 3 { // about 1% of the days mention the rare word
					text = "Quarterly 周报 draft is ready"
				}
				fmt.Fprintf(&sb, "- %02d:%02d 同事%d: %s #%d\n", 9+l/6, (l*7)%60, l%5, text, d*benchLines+l)
			}
			if err := os.WriteFile(filepath.Join(msgs, day+".md"), []byte(sb.String()), 0o600); err != nil {
				return err
			}
		}
	}
	for p := range benchPeople {
		if err := st.PutPerson(&Person{ID: fmt.Sprintf("lark-u%03d", p), Name: fmt.Sprintf("同事 %03d", p), Note: "负责后端"}); err != nil {
			return err
		}
	}
	for i := range benchDocs {
		if err := st.PutItem(&Item{Kind: KindDoc, ID: fmt.Sprintf("d%03d", i), Title: fmt.Sprintf("设计文档 %03d", i), At: start}); err != nil {
			return err
		}
	}
	for i := range 20 {
		if err := st.PutProject(&Project{Slug: fmt.Sprintf("p%02d", i), Name: fmt.Sprintf("项目 %02d", i), Kind: "short", Status: "active"}); err != nil {
			return err
		}
	}
	return ageFiles(dir, time.Hour)
}

// ageFiles sets the mtime of every file and directory under dir back by d: a real archive's files are mostly old,
// and Find does not keep a file or listing changed in the last seconds.
func ageFiles(dir string, d time.Duration) error {
	at := time.Now().Add(-d)
	return filepath.WalkDir(dir, func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, at, at)
	})
}

func TestMain(m *testing.M) {
	code := m.Run()
	if benchDir != "" {
		_ = os.RemoveAll(benchDir)
	}
	os.Exit(code)
}

// BenchmarkFind is search-as-you-type: the same engine answers one query after another (the app sends one per
// keystroke), the archive unchanged in between.
func BenchmarkFind(b *testing.B) {
	e := &Engine{Store: benchStore(b)}
	if res := e.Find("周报", 80); len(res) == 0 {
		b.Fatal("the rare word is not found")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		e.Find([]string{"周", "周报", "周报 draft"}[i%3], 80)
	}
}

// BenchmarkFindCold is the first search of a new engine (nothing kept from an earlier one).
func BenchmarkFindCold(b *testing.B) {
	st := benchStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		(&Engine{Store: st}).Find("周报", 80)
	}
}

// BenchmarkSearch is kb_search over the same archive, messages included: a rare word (the days that have it are
// spread over the year, so most files are read before the limit is reached) and a word no file has (every file read).
func BenchmarkSearch(b *testing.B) {
	e := &Engine{Store: benchStore(b)}
	if hits := e.Search("周报 draft", true, 40); len(hits) == 0 {
		b.Fatal("the rare word is not found")
	}
	for _, q := range []string{"周报 draft", "nowhere"} {
		b.Run(q, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				e.Search(q, true, 40)
			}
		})
	}
}
