package kb

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// matchesReadingEveryFile is Matches as it was before it looked at a chat's days through Find's cache: every file
// read and folded. Matches must return exactly what it returns.
func matchesReadingEveryFile(e *Engine, kind, id, query string, limit int) []Match {
	if kind == KindRecording {
		id, _ = RecordingID(id)
	}
	ws := words(query)
	if len(ws) == 0 {
		return nil
	}
	match := func(l string) bool { return hasAll(fold(l), ws) }
	var out []Match
	look := func(p string, msgs bool) {
		raw, err := os.ReadFile(p)
		if err != nil || !hasAll(fold(string(raw)), ws) {
			return
		}
		rel, _ := filepath.Rel(e.Store.Dir, p)
		rel = filepath.ToSlash(rel)
		if !msgs {
			for i, l := range strings.Split(string(raw), "\n") {
				if len(out) >= limit {
					return
				}
				if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "# ") && match(t) {
					out = append(out, Match{Path: rel, N: i + 1, Line: clip(t, 600)})
				}
			}
			return
		}
		day := strings.TrimSuffix(filepath.Base(p), ".md")
		ms := messages(string(raw))
		for i := len(ms) - 1; i >= 0; i-- {
			if len(out) >= limit {
				return
			}
			m := ms[i]
			if !match(m.text) {
				continue
			}
			mt := Match{Path: rel, N: m.n, Line: clip(m.text, 1200), Day: day}
			for j := max(0, i-2); j < i; j++ {
				mt.Before = append(mt.Before, clip(ms[j].text, 300))
			}
			for j := i + 1; j < min(len(ms), i+3); j++ {
				mt.After = append(mt.After, clip(ms[j].text, 300))
			}
			out = append(out, mt)
		}
	}
	switch kind {
	case KindChat, KindPerson, KindProject:
		for _, f := range []string{"summary.md", "log.md", "meta.json"} {
			look(e.Store.path(kind, id, f), false)
		}
		if kind == KindChat {
			days := e.Store.Days(id)
			for i := len(days) - 1; i >= 0 && len(out) < limit; i-- {
				look(e.Store.path(KindChat, id, "messages", days[i]+".md"), true)
			}
		}
	case KindRecording:
		for _, f := range []string{"summary.md", "transcript.md", "meta.json"} {
			look(e.Store.path(kind, id, f), false)
		}
	case KindDoc, KindMeeting, KindEvent:
		look(filepath.Join(e.Store.Dir, kind, SafeID(id)+".json"), false)
	}
	return out
}

const (
	matchesChat = "lark-m1"
	matchesNFD  = "cafe\u0301" // decomposed, as macOS often writes it
	matchesNFC  = "caf\u00e9"
)

// matchesStore is a chat with a month of days (a rare word on a few of them, written in different cases, a common word
// on every line, decomposed text, continuation lines, a day with only its heading) and a person, a project, a
// recording and a doc that have the same words.
func matchesStore(t *testing.T) *Store {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ch := &Chat{ID: matchesChat, Name: "Release", Kind: "group"}
	if err := st.PutChat(ch); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSummary(KindChat, ch.ID, "# Release\n\nWeekly review of the Zephyr rollout.\n"); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	for d := range 30 {
		day := start.AddDate(0, 0, d).Format("2006-01-02")
		var sb strings.Builder
		sb.WriteString("# " + day + "\n")
		if d != 12 { // the 13th has only its heading
			for l := range 5 {
				fmt.Fprintf(&sb, "- %02d:%02d Ann: build ok, next review #%d\n", 9+l, l*7, d*5+l)
			}
		}
		switch d {
		case 3:
			sb.WriteString("- 15:00 Bob: zephyr is green\n")
		case 17:
			sb.WriteString("- 15:00 Bob: ZEPHYR needs a review\n  and a second look at the zephyr dashboards\n")
		case 21:
			sb.WriteString("- 15:00 Cy: lunch at the " + matchesNFD + "\n- 15:05 Ann: the " + matchesNFC + " again\n")
		case 25:
			sb.WriteString("- 15:00 Bob: Zephyr shipped\n- 15:01 Ann: congrats\n")
		}
		matchesWrite(t, st.path(KindChat, ch.ID, "messages", day+".md"), sb.String())
	}
	if err := st.PutPerson(&Person{ID: "lark-u1", Name: "Bob", Note: "owns the Zephyr review"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSummary(KindPerson, "lark-u1", "# Bob\n\nzephyr lead, likes the "+matchesNFD+"\n"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(&Project{Slug: "zp", Name: "Zephyr", Kind: "short", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSummary(KindProject, "zp", "# Zephyr\n\nreview every Friday\nZEPHYR goes out in October\n"); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendLog("zp", start, "chat", "zephyr review moved"); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st}
	now := time.Now()
	if _, err := e.PutRecording(recID, recUpload(1, now.Add(-time.Hour), true, "zephyr review first", "the "+matchesNFD+" order", "ZEPHYR again"), now); err != nil {
		t.Fatal(err)
	}
	if err := st.PutItem(&Item{Kind: KindDoc, ID: "d1", Title: "Zephyr review notes", Note: "at the " + matchesNFD, At: start}); err != nil {
		t.Fatal(err)
	}
	return st
}

func matchesWrite(t *testing.T, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Matches gives what reading every file gives, for every kind of entry, whether Find's cache holds the files or not.
func TestMatchesAgreesWithReadingEveryFile(t *testing.T) {
	st := matchesStore(t)
	entries := []struct{ kind, id string }{
		{KindChat, matchesChat},
		{KindPerson, "lark-u1"},
		{KindProject, "zp"},
		{KindRecording, strings.ToUpper(recID)},
		{KindDoc, "d1"},
	}
	queries := []string{
		"zephyr",        // rare in the chat
		"review",        // on every line of the chat
		"nowhere",       // absent
		"ZEPHYR",        // upper case
		"zephyr review", // two words, both on one day only
		matchesNFD,      // decomposed query, both forms in the text
		matchesNFC,
		"",
	}
	warm := &Engine{Store: st}
	check := func(t *testing.T, round string) {
		t.Helper()
		seen := 0
		for _, q := range queries {
			warm.Find(q, 80)
			for _, en := range entries {
				for _, limit := range []int{1, 3, 40} {
					want := matchesReadingEveryFile(warm, en.kind, en.id, q, limit)
					if got := warm.Matches(en.kind, en.id, q, limit); !reflect.DeepEqual(got, want) {
						t.Fatalf("%s warm %s/%s %q limit %d:\n got %+v\nwant %+v", round, en.kind, en.id, q, limit, got, want)
					}
					if got := (&Engine{Store: st}).Matches(en.kind, en.id, q, limit); !reflect.DeepEqual(got, want) {
						t.Fatalf("%s cold %s/%s %q limit %d:\n got %+v\nwant %+v", round, en.kind, en.id, q, limit, got, want)
					}
					seen += len(want)
				}
			}
		}
		if seen == 0 {
			t.Fatalf("%s: the fixture matches nothing", round)
		}
	}
	// Fresh files are read every time (findSettle); old ones come from the cache.
	check(t, "fresh")
	if err := ageFiles(st.Dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	check(t, "aged")

	// The chat's days go through Find's cache: a new engine keeps them after one Matches.
	cold := &Engine{Store: st}
	if m := cold.Matches(KindChat, matchesChat, "zephyr", 40); len(m) != 4 {
		t.Fatalf("rare word: %+v", m)
	}
	for _, d := range st.Days(matchesChat) {
		if cold.find.texts[st.path(KindChat, matchesChat, "messages", d+".md")] == nil {
			t.Fatalf("day %s was not looked at through the cache", d)
		}
	}
	// The expected places, so the comparison above is not between two empty lists.
	m := warm.Matches(KindChat, matchesChat, "ZEPHYR", 40)
	var days []string
	for _, x := range m {
		days = append(days, x.Day)
	}
	if len(m) != 4 || m[0].Path != "chats/"+matchesChat+"/summary.md" ||
		strings.Join(days[1:], " ") != "2026-09-26 2026-09-18 2026-09-04" {
		t.Fatalf("matches: %v %+v", days, m)
	}
}

// After a day file changes or goes away, Matches reads what is there now: a change in the last seconds (not cached), an
// old one (cached until its stat changes), a rewrite of the same size, a delete.
func TestMatchesFollowsDayEdits(t *testing.T) {
	st := matchesStore(t)
	if err := ageFiles(st.Dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st}
	day := func(d string) string { return st.path(KindChat, matchesChat, "messages", d+".md") }
	same := func(step, q string, want int) {
		t.Helper()
		e.Find(q, 80)
		got := e.Matches(KindChat, matchesChat, q, 40)
		if ref := matchesReadingEveryFile(e, KindChat, matchesChat, q, 40); !reflect.DeepEqual(got, ref) {
			t.Fatalf("%s: %+v, reading every file %+v", step, got, ref)
		}
		if len(got) != want {
			t.Fatalf("%s: %d matches, want %d: %+v", step, len(got), want, got)
		}
	}
	same("before", "zephyr", 4)
	if e.find.texts[day("2026-09-10")] == nil {
		t.Fatal("an old day without the word is kept")
	}

	// A day that did not have the word gets it, just now: not cached, read as it is.
	raw, _ := os.ReadFile(day("2026-09-10"))
	matchesWrite(t, day("2026-09-10"), string(raw)+"- 18:00 Ann: zephyr retro\n")
	same("fresh write", "zephyr", 5)

	// Another one, its mtime old but newer than the cached stat.
	aged := func(p string, d time.Duration) {
		t.Helper()
		at := time.Now().Add(-d)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	if e.find.texts[day("2026-09-11")] == nil {
		t.Fatal("the day is not cached before its write")
	}
	raw, _ = os.ReadFile(day("2026-09-11"))
	matchesWrite(t, day("2026-09-11"), string(raw)+"- 18:00 Ann: zephyr notes\n")
	aged(day("2026-09-11"), 30*time.Minute)
	same("aged write", "zephyr", 6)

	// A day that had the word loses it, the file the same size: the mtime alone tells.
	raw, _ = os.ReadFile(day("2026-09-04"))
	matchesWrite(t, day("2026-09-04"), strings.Replace(string(raw), "zephyr", "aurora", 1))
	aged(day("2026-09-04"), 20*time.Minute)
	same("same size", "zephyr", 5)

	// A day deleted, while the cache still holds it (no Find in between to prune it).
	if err := os.Remove(day("2026-09-26")); err != nil {
		t.Fatal(err)
	}
	if e.find.texts[day("2026-09-26")] == nil {
		t.Fatal("the deleted day was not cached")
	}
	if got := e.Matches(KindChat, matchesChat, "zephyr", 40); len(got) != 4 {
		t.Fatalf("after delete: %+v", got)
	}
	same("delete", "zephyr", 4)
}
