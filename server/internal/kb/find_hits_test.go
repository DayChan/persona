package kb

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"
)

// findReference is Find as it was before fileFound and the folded fast path: every matching line of every file
// becomes a Found, folded line by line, and add then keeps hitsPerResult of them and counts to foundCap. It reads
// the files itself rather than through findCache, so it is also what reading every file gives.
func findReference(e *Engine, query string, limit int) []Result {
	match := matcher(query)
	if match == nil || limit <= 0 {
		return nil
	}
	ws := words(query)
	w := e.files()
	files, days := walkPaths(w.files), walkPaths(w.days)
	type fileHits struct {
		key   string
		found []Found
	}
	var per []fileHits
	for _, p := range append(append([]string{}, files...), days...) {
		rel, _ := filepath.Rel(e.Store.Dir, p)
		kind, id, ok := entryOf(rel)
		if !ok || !shown(kind, rel) {
			continue
		}
		fh := fileHits{key: kind + "/" + id}
		if raw, err := os.ReadFile(p); err == nil && hasAll(fold(string(raw)), ws) {
			for n, l := range strings.Split(string(raw), "\n") {
				if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "# ") && match(t) {
					fh.found = append(fh.found, Found{Path: filepath.ToSlash(rel), Line: clip(t, 300), N: n + 1})
				}
			}
		}
		per = append(per, fh)
	}
	known := map[string]Entry{}
	for _, k := range []string{KindProject, KindChat, KindPerson, KindDoc, KindMeeting, KindEvent, KindRecording} {
		for _, en := range e.List(k, "") {
			known[k+"/"+en.ID] = en
		}
	}
	by := map[string]*Result{}
	add := func(key string, f Found) {
		r := by[key]
		if r == nil {
			r = &Result{Entry: known[key]}
			by[key] = r
		}
		if r.Count < foundCap {
			r.Count++
		}
		if len(r.Hits) < hitsPerResult && !hasLine(r.Hits, f.Line) {
			r.Hits = append(r.Hits, f)
		}
	}
	for _, fh := range per {
		if _, ok := known[fh.key]; !ok {
			continue
		}
		for _, f := range fh.found {
			add(fh.key, f)
		}
	}
	for key, en := range known {
		if match(en.Name) {
			if by[key] == nil {
				by[key] = &Result{Entry: en}
			}
			by[key].InName = true
		}
	}
	out := make([]Result, 0, len(by))
	for _, r := range by {
		r.Hits = sortHits(r.Hits)
		out = append(out, *r)
	}
	imp := func(s string) int {
		if s == "" {
			return 4
		}
		return int(s[1] - '0')
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.InName != b.InName {
			return a.InName
		}
		if imp(a.Entry.Importance) != imp(b.Entry.Importance) {
			return imp(a.Entry.Importance) < imp(b.Entry.Importance)
		}
		if a.Entry.Relevance != b.Entry.Relevance {
			return a.Entry.Relevance > b.Entry.Relevance
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if !a.Entry.Updated.Equal(b.Entry.Updated) {
			return a.Entry.Updated.After(b.Entry.Updated)
		}
		return a.Entry.Kind+a.Entry.ID < b.Entry.Kind+b.Entry.ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// checkFindAgainstReference compares Find with findReference for every query and limit, on a new engine for each
// query (its first search reads every file) and on one that keeps the texts between searches.
func checkFindAgainstReference(t *testing.T, st *Store, queries []string, limits []int) {
	t.Helper()
	want := map[string][]Result{}
	for _, q := range queries {
		want[q] = findReference(&Engine{Store: st}, q, 1<<30)
	}
	warm := &Engine{Store: st}
	warm.Find("warm up", 1)
	if len(warm.find.texts) == 0 {
		t.Fatal("warm-up search cached no text")
	}
	for _, round := range []string{"cold", "warm"} {
		for _, q := range queries {
			e := warm
			if round == "cold" {
				e = &Engine{Store: st}
			}
			for _, limit := range limits {
				w := want[q]
				if limit <= 0 {
					w = nil
				} else if len(w) > limit {
					w = w[:limit]
				}
				if got := e.Find(q, limit); !reflect.DeepEqual(got, w) {
					t.Fatalf("%s Find(%q, %d):\n got %+v\nwant %+v", round, q, limit, got, w)
				}
			}
		}
	}
}

// Find keeps only the lines and the count a result can show; what it returns must be what keeping every matching
// line gives: duplicates within and across files, a file with more than foundCap matches, lines that clip to the
// same text, upper-case and decomposed text (folded line by line), headings, CRLF, an entry spread over many files.
func TestFindBoundedHitsMatchReference(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
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
	day := func(id, d, text string) { write(st.path(KindChat, id, "messages", d+".md"), "# "+d+"\n"+text) }
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	// An entry spread over its profile, timeline, metadata and many days, with duplicates between them.
	a := &Chat{ID: "lark-a", Name: "Alpha chat", Kind: "group", Tagging: Tagging{Importance: "P1", Relevance: 50}, LastAt: at}
	if err := st.PutChat(a); err != nil {
		t.Fatal(err)
	}
	write(st.path(KindChat, a.ID, "summary.md"), "# Alpha build\n\nalpha build summary line\nalpha build summary line\n  # alpha build heading kept out\n")
	write(st.path(KindChat, a.ID, "log.md"), "- 2026-10-01 alpha build kicked off\n- alpha build summary line\n")
	var many strings.Builder
	for i := range 150 {
		fmt.Fprintf(&many, "- 09:%02d A: alpha build ok #%d\n", i%60, i%40) // 40 distinct lines, each up to four times
	}
	day(a.ID, "2026-10-05", many.String())
	long := strings.Repeat("长", 290) + " alpha build "
	day(a.ID, "2026-10-04", "- 09:00 A: "+long+"tail one\n- 09:01 A: "+long+"tail two\n- 09:02 A: "+long+"tail three\n- 09:03 A: alpha build short\n")
	day(a.ID, "2026-10-03", "- 09:00 B: Alpha BUILD crlf\r\n- 09:01 B: alpha build crlf\r\n\r\n- 09:02 B: alpha build crlf\r\n")
	day(a.ID, "2026-10-02", "- 10:00 C: cafe\u0301 alpha build\n- 10:01 C: caf\u00e9 alpha build\n- 10:02 C: CAF\u00c9 ALPHA BUILD\n")

	// Lines the entry already holds (from log.md, the first of its files) come first in later files: the newest day
	// still gives the line after them.
	b := &Chat{ID: "lark-b", Name: "Bee chat", Kind: "group", LastAt: at}
	if err := st.PutChat(b); err != nil {
		t.Fatal(err)
	}
	write(st.path(KindChat, b.ID, "summary.md"), "x1 zeta\n")
	write(st.path(KindChat, b.ID, "log.md"), "x1 zeta\nx1 zeta\nx2 zeta\n")
	day(b.ID, "2026-10-02", "x1 zeta\nx2 zeta\nx2 zeta\nx3 zeta\nx4 zeta\n")
	day(b.ID, "2026-10-01", "x5 zeta\n")

	// More than foundCap matches across files, none in one file.
	c := &Chat{ID: "lark-c", Name: "Gamma chat", Kind: "group", LastAt: at}
	if err := st.PutChat(c); err != nil {
		t.Fatal(err)
	}
	for d := range 30 {
		day(c.ID, at.AddDate(0, 0, d).Format("2006-01-02"), strings.Repeat("- 09:00 G: gamma alpha\n", 4))
	}

	// One file past foundCap before its second distinct line: the lines after the cap are still shown.
	d := &Chat{ID: "lark-d", Name: "Dee chat", Kind: "group", LastAt: at}
	if err := st.PutChat(d); err != nil {
		t.Fatal(err)
	}
	day(d.ID, "2026-10-01", strings.Repeat("- 09:00 D: delta alpha\n", 120)+"- 09:01 D: delta two\n- 09:02 D: delta three\n")

	if err := st.PutPerson(&Person{ID: "lark-u1", Name: "Alpha Person", Note: "alpha build reviewer"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(&Project{Slug: "p1", Name: "Build project", Kind: "short", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSummary(KindProject, "p1", "# P1\n\nThe alpha BUILD pipeline\n"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutItem(&Item{Kind: KindDoc, ID: "d1", Title: "Alpha build doc", Note: "alpha build notes", At: at}); err != nil {
		t.Fatal(err)
	}
	// An entry the lists do not show: its lines are not results.
	if err := st.PutItem(&Item{Kind: KindDoc, ID: "untitled", Note: "alpha build untitled", At: at}); err != nil {
		t.Fatal(err)
	}
	if err := ageFiles(st.Dir, time.Hour); err != nil {
		t.Fatal(err)
	}

	queries := []string{"alpha", "alpha build", "ALPHA", "build", "zeta", "gamma", "caf\u00e9", "cafe\u0301", "CAF\u00c9", "crlf", "tail",
		"长", "heading", "#", "nowhere", "x", "delta", "   "}
	checkFindAgainstReference(t, st, queries, []int{80, 5, 1, 0})

	// Spot checks of what the comparison relies on: both ways of matching ran, and the bounds were reached.
	cached := &Engine{Store: st}
	cached.Find("x", 1)
	same := map[bool]int{}
	for _, tx := range cached.find.texts {
		same[tx.same]++
	}
	if same[true] == 0 || same[false] == 0 {
		t.Fatalf("files that fold to themselves and not: %v", same)
	}
	for _, r := range (&Engine{Store: st}).Find("alpha", 80) {
		hits := map[string]int{a.ID: hitsPerResult, c.ID: 1} // c repeats one line
		switch r.Entry.ID {
		case a.ID, c.ID:
			if r.Count != foundCap || len(r.Hits) != hits[r.Entry.ID] {
				t.Fatalf("%s: count %d, hits %+v", r.Entry.ID, r.Count, r.Hits)
			}
		case "untitled", "":
			t.Fatalf("an entry the lists do not show is a result: %+v", r)
		}
	}
	if res := (&Engine{Store: st}).Find("delta", 80); len(res) != 1 || len(res[0].Hits) != 3 || res[0].Hits[2].Line != "- 09:02 D: delta three" ||
		res[0].Count != foundCap {
		t.Fatalf("delta: %+v", res)
	}
	if res := (&Engine{Store: st}).Find("zeta", 80); len(res) != 1 || len(res[0].Hits) != 3 || res[0].Hits[2].Line != "x3 zeta" ||
		res[0].Count != 10 {
		t.Fatalf("zeta: %+v", res)
	}
}

// The same comparison over random files: few distinct lines, mixed case, decomposed text, CRLF and headings, in
// entries of every kind that has several files.
func TestFindBoundedHitsMatchReferenceRandom(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewPCG(1, 2))
	vocab := []string{"alpha", "Alpha", "ALPHA", "beta", "caf\u00e9", "cafe\u0301", "build", "Build", "周报", "x", "#", "\u3000", "\u00a0"}
	line := func() string {
		switch rng.IntN(10) {
		case 0:
			return ""
		case 1:
			return "# alpha heading"
		case 2:
			return "  " + strings.Repeat("alpha ", 60) + vocab[rng.IntN(len(vocab))] // clips at 300 runes
		}
		n := 1 + rng.IntN(3)
		ws := make([]string, n)
		for i := range ws {
			ws[i] = vocab[rng.IntN(len(vocab))]
		}
		return "- 09:00 A: " + strings.Join(ws, " ")
	}
	text := func(lines int) string {
		var sb strings.Builder
		eol := "\n"
		if rng.IntN(5) == 0 {
			eol = "\r\n"
		}
		for range lines {
			sb.WriteString(line() + eol)
		}
		return sb.String()
	}
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := range 8 {
		ch := &Chat{ID: fmt.Sprintf("lark-r%d", i), Name: vocab[i%len(vocab)] + " chat", Kind: "group",
			Tagging: Tagging{Importance: fmt.Sprintf("P%d", i%4), Relevance: i % 3}, LastAt: at}
		if err := st.PutChat(ch); err != nil {
			t.Fatal(err)
		}
		write(st.path(KindChat, ch.ID, "summary.md"), text(rng.IntN(8)))
		write(st.path(KindChat, ch.ID, "log.md"), text(rng.IntN(8)))
		for d := range 12 {
			write(st.path(KindChat, ch.ID, "messages", at.AddDate(0, 0, d).Format("2006-01-02")+".md"), text(rng.IntN(150)))
		}
	}
	for i := range 3 {
		if err := st.PutProject(&Project{Slug: fmt.Sprintf("rp%d", i), Name: "Project " + vocab[i], Kind: "short", Status: "active"}); err != nil {
			t.Fatal(err)
		}
		write(st.path(KindProject, fmt.Sprintf("rp%d", i), "summary.md"), text(rng.IntN(40)))
		write(st.path(KindProject, fmt.Sprintf("rp%d", i), "log.md"), text(rng.IntN(120)))
	}
	if err := ageFiles(st.Dir, time.Hour); err != nil {
		t.Fatal(err)
	}
	checkFindAgainstReference(t, st, []string{"alpha", "ALPHA beta", "caf\u00e9", "cafe\u0301 build", "build", "周报", "x", "#", "chat",
		"nowhere"}, []int{80, 5, 1})
}

// PERSONA_KB_BENCH_ARCHIVE=1 runs the comparison over the bench archive (find_bench_test.go; building it and reading
// it for every query takes about 10 s, a minute with -race): the word on every line ("build", every chat past
// foundCap), rare words, words in names and a word in no file.
func TestFindBoundedHitsMatchReferenceBenchArchive(t *testing.T) {
	if os.Getenv("PERSONA_KB_BENCH_ARCHIVE") != "1" {
		t.Skip("PERSONA_KB_BENCH_ARCHIVE not set")
	}
	checkFindAgainstReference(t, benchStore(t), []string{"build", "周报 draft", "周", "群聊 05", "nowhere"}, []int{80, 5})
}

// The folded fast path matches the lines of a file whose text folds to itself as written: every line of such a text,
// trimmed as Find trims it, folds to itself too.
func FuzzFoldedTextHasFoldedLines(f *testing.F) {
	for _, s := range foldSeeds() {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		checkFoldedLines(t, s)
		checkFoldedLines(t, fold(s))
	})
}

// The same property over many random strings of the runes that could break it, so a plain test run covers more than
// the fuzz seeds.
func TestFoldedTextHasFoldedLines(t *testing.T) {
	for _, s := range foldSeeds() {
		checkFoldedLines(t, s)
		checkFoldedLines(t, fold(s))
	}
	var runes []rune
	for r := rune(0); r < 0x3100; r++ {
		if unicode.IsSpace(r) {
			runes = append(runes, r)
		}
	}
	runes = append(runes, 'a', 'A', 'e', 'E', 'k', '\u212a', '\u00c5', '\u0130', '\u03a3', '\u00df', '\u1e9e', '\u0300', '\u0301', '\u0323',
		'\u0345', '\u034f', '\u1100', '\u1161', '\u11a8', '\uac00', '\u0915', '\u093c', '\u0958', '\u2126', '\u00e9', '\ufb01')
	rng := rand.New(rand.NewPCG(3, 4))
	for range 20000 {
		rs := make([]rune, rng.IntN(48))
		for i := range rs {
			rs[i] = runes[rng.IntN(len(runes))]
		}
		if rng.IntN(8) == 0 { // a run of combining marks past norm's 30
			rs = append(rs[:len(rs)/2], append([]rune(strings.Repeat("\u0301", 25+rng.IntN(15))), rs[len(rs)/2:]...)...)
		}
		s := string(rs)
		checkFoldedLines(t, s)
		checkFoldedLines(t, fold(s))
	}
}

func foldSeeds() []string {
	marks := strings.Repeat("\u0301", 31)
	return []string{
		"plain ascii\nand 中文\n",
		"cafe\u0301\n\u0301 leading mark\n \u0301 after a space",
		"a\u00a0\u0301b\n\u00a0\u0301",
		"\u3000\u0301x\u3000\n\u3000",
		"\u1100\n\u1161\u11a8 \u1100 \u1161",
		"\u1100\u1161\n\u11a8",
		"x" + marks + "\n" + marks + "y\n " + marks,
		"\u2000space\u2001\n",
		"\u212a\u0130\u03a3\u1e9e\n",
		"crlf\r\n\u0301\r\n",
		"\u0085\u0301\u2028\u0301\u2029\n\ufeff",
		"invalid \xff utf-8\n\xe2\x80",
	}
}

// checkFoldedLines: when s folds to itself, so does every line of it as Find trims it, so matching the raw line with
// hasAll is matching its fold.
func checkFoldedLines(t *testing.T, s string) {
	t.Helper()
	if fold(s) != s {
		return
	}
	for _, l := range strings.Split(s, "\n") {
		if tl := strings.TrimSpace(l); fold(tl) != tl {
			t.Fatalf("text %+q folds to itself, its line %+q folds to %+q", s, tl, fold(tl))
		}
	}
}
