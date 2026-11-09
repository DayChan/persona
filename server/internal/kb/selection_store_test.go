package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// selEngine is an engine over a new store with the selection s (nil: Defaults).
func selEngine(t *testing.T, s *Selection) (*Store, *Engine) {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return st, &Engine{Store: st, TextDays: 365, AttachmentDays: 30, Selection: func() *Selection { return s }}
}

func chatText(t *testing.T, st *Store, id string) string {
	t.Helper()
	var b strings.Builder
	for _, d := range st.Days(id) {
		raw, err := os.ReadFile(st.path(KindChat, id, "messages", d+".md"))
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
	}
	return b.String()
}

// senders lists who wrote the lines of a chat's day files, in file order ("我" is the owner).
func linesOf(t *testing.T, st *Store, id string) []string {
	t.Helper()
	var out []string
	for _, r := range strings.Split(chatText(t, st, id), "\n") {
		if clockOf(r) != "" {
			_, text, _ := strings.Cut(r[8:], ": ")
			out = append(out, text)
		}
	}
	return out
}

func TestIngestSelectionKeepsWhatTheRulesStore(t *testing.T) {
	s := sel(func(s *Selection) { setRule(s, RuleMine, func(r *MessageRule) { r.Context = intp(2) }) })
	st, e := selEngine(t, s)
	ctx := context.Background()
	base := time.Now().Add(-6 * time.Hour).Truncate(time.Hour)
	m := func(i int, text string, f func(*SourceMessage)) SourceMessage {
		x := SourceMessage{ID: fmt.Sprint("m", i), SenderID: "u1", SenderName: "Someone", Text: text, At: base.Add(time.Duration(i) * time.Minute)}
		if f != nil {
			f(&x)
		}
		return x
	}
	mine := func(x *SourceMessage) { x.Mine = true }
	mention := func(x *SourceMessage) { x.Mentioned = true }
	group := SourceChat{ID: "g", Name: "A group", Kind: "group"}
	n, err := e.Ingest(ctx, named("chat"), group, []SourceMessage{
		m(1, "a", nil), m(2, "b", nil), m(3, "c mention", mention), m(4, "d", nil), m(5, "e mine", mine), m(6, "f", nil), m(7, "g", nil),
	})
	if err != nil || n != 5 {
		t.Fatalf("stored %d, %v", n, err)
	}
	// After a restart (a new engine over the same files), the window goes on: h is not stored yet, then the owner's
	// i stores it as context.
	e2 := &Engine{Store: st, TextDays: 365, Selection: func() *Selection { return s }}
	if n, err := e2.Ingest(ctx, named("chat"), group, []SourceMessage{m(8, "h", nil)}); err != nil || n != 0 {
		t.Fatalf("h: %d, %v", n, err)
	}
	if n, err := e2.Ingest(ctx, named("chat"), group, []SourceMessage{m(9, "i mine", mine), m(10, "j", nil), m(11, "k", nil), m(12, "l", nil)}); err != nil || n != 4 {
		t.Fatalf("i: %d, %v", n, err)
	}
	got := linesOf(t, st, "chat-g")
	want := []string{"c mention", "d", "e mine", "f", "g", "h", "i mine", "j", "k"}
	if !slices.Equal(got, want) {
		t.Fatalf("lines %q, want %q", got, want)
	}
	// Replaying a batch adds nothing and does not move the window.
	if n, err := e2.Ingest(ctx, named("chat"), group, []SourceMessage{m(9, "i mine", mine), m(10, "j", nil), m(11, "k", nil), m(12, "l", nil)}); err != nil || n != 0 {
		t.Fatalf("replay: %d, %v", n, err)
	}
	c, _ := st.Chat("chat-g")
	if c.Rule != RuleGroup || c.Messages != 9 {
		t.Fatalf("chat %+v", c)
	}
	l, ok, err := st.readLedger("chat-g")
	if !ok || err != nil || l.Rule != RuleGroup {
		t.Fatalf("ledger %+v %v %v", l, ok, err)
	}
	entries := 0
	for _, keys := range l.Lines {
		for _, r := range keys {
			entries++
			if r != RuleMentions && r != RuleMine {
				t.Errorf("entry %q", r)
			}
		}
	}
	if entries != 9 {
		t.Fatalf("%d ledger entries, want every stored line (none is the group rule's): %+v", entries, l)
	}
	if strings.Contains(chatText(t, st, "chat-g"), "- ") && searchable(filepath.Join(KindChat, "chat-g", retentionFile)) {
		t.Fatal("the ledger is searchable")
	}
	if searchable(filepath.Join(selectDir, "chat-g.json")) {
		t.Fatal("the context is searchable")
	}
}

func TestIngestSelectionChatsAndItems(t *testing.T) {
	s := sel(func(s *Selection) {
		s.SetItem(SelectionItem{Source: "chat", Kind: ItemChat, ID: "off", Read: false})
		s.SetItem(SelectionItem{Source: "chat", Kind: ItemChat, ID: "kept", Read: true, Store: true})
		setRule(s, RuleMentions, func(r *MessageRule) { r.Days = intp(3650) })
		setRule(s, RuleDM, func(r *MessageRule) { r.Days = intp(30) })
	})
	st, e := selEngine(t, s)
	ctx := context.Background()
	now := time.Now()
	msg := func(id, text string, at time.Time, mine, mention bool) SourceMessage {
		return SourceMessage{ID: id, SenderName: "Someone", Text: text, At: at, Mine: mine, Mentioned: mention}
	}
	for _, tc := range []struct {
		chat SourceChat
		msgs []SourceMessage
		want []string
	}{
		{SourceChat{ID: "off", Kind: "group"}, []SourceMessage{msg("1", "mine", now.Add(-time.Hour), true, false)}, nil},
		{SourceChat{ID: "kept", Kind: "group", Muted: true}, []SourceMessage{msg("1", "plain", now.Add(-time.Hour), false, false)}, []string{"plain"}},
		{SourceChat{ID: "mdm", Kind: "dm", Muted: true}, []SourceMessage{msg("1", "plain", now.Add(-time.Hour), false, false)}, []string{"plain"}},
		{SourceChat{ID: "muted", Kind: "group", Muted: true}, []SourceMessage{msg("1", "plain", now.Add(-2*time.Hour), false, false),
			msg("2", "mention", now.Add(-time.Hour), false, true)}, []string{"mention"}},
		// Days: a dm line older than its 30 days is not written, a mention ten years back is.
		{SourceChat{ID: "old", Kind: "dm"}, []SourceMessage{msg("1", "too old", now.AddDate(0, 0, -100), false, false),
			msg("2", "old mention", now.AddDate(0, 0, -1000), false, true), msg("3", "new", now.Add(-time.Hour), false, false)}, []string{"old mention", "new"}},
		// Outside the selection: everything, as before it.
		{SourceChat{ID: "box", Kind: "mail"}, []SourceMessage{msg("1", "a mail", now.Add(-time.Hour), false, false)}, []string{"a mail"}},
	} {
		if _, err := e.Ingest(ctx, named("chat"), tc.chat, tc.msgs); err != nil {
			t.Fatal(err)
		}
		if got := linesOf(t, st, ChatID("chat", tc.chat.ID)); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %q, want %q", tc.chat.ID, got, tc.want)
		}
	}
	if _, ok := st.Chat("chat-off"); ok {
		t.Error("an unread chat was made")
	}
	if c, _ := st.Chat("chat-box"); c.Rule != "" {
		t.Errorf("mail chat rule %q", c.Rule)
	}
	if _, ok, _ := st.readLedger("chat-box"); ok {
		t.Error("a chat outside the selection has a ledger")
	}
}

// TestPruneSelectedByRule: a day file holding lines of different rules keeps each its own days, and the rewrite keeps
// every other row byte for byte; attachments go with the last line linking them; turning a rule off keeps its lines
// text_days.
func TestPruneSelectedByRule(t *testing.T) {
	s := sel(func(s *Selection) {
		setRule(s, RuleGroup, func(r *MessageRule) { r.On, r.Days = true, intp(30) })
		setRule(s, RuleMentions, func(r *MessageRule) { r.Days, r.AttachmentDays = intp(3650), intp(3650) })
	})
	st, e := selEngine(t, s)
	ctx := context.Background()
	now := time.Now()
	at := now.AddDate(0, 0, -60).Truncate(time.Hour)
	img := func(id string) SourceMessage {
		return SourceMessage{ID: id, Type: "IMAGE", Media: []string{"x.png"}, At: at, SenderName: "S"}
	}
	e.AttachmentDays = 100
	dl := func(_ context.Context, _, dst string) error { return writeFile(dst, []byte("img")) }
	src := selSource{name: "chat", dl: dl}
	plainImg, mentionImg := img("p"), img("q")
	plainImg.At, mentionImg.At = at.Add(time.Minute), at.Add(2*time.Minute)
	mentionImg.Mentioned = true
	msgs := []SourceMessage{
		{ID: "1", Text: "group line", At: at, SenderName: "S"},
		{ID: "2", Text: "a mention", At: at.Add(3 * time.Minute), SenderName: "S", Mentioned: true},
		plainImg, mentionImg,
		{ID: "3", Text: "recent", At: now.Add(-time.Hour), SenderName: "S"},
	}
	// Written while the group rule kept 90 days, so the 60-day-old lines are written.
	setRule(s, RuleGroup, func(r *MessageRule) { r.Days = intp(90) })
	if n, err := e.Ingest(ctx, src, SourceChat{ID: "g", Name: "G", Kind: "group"}, msgs); err != nil || n != 5 {
		t.Fatalf("ingest %d %v", n, err)
	}
	day := at.In(st.Zone()).Format("2006-01-02")
	before, _ := os.ReadFile(st.path(KindChat, "chat-g", "messages", day+".md"))
	attDir := st.path(KindChat, "chat-g", "attachments", day, "messages")
	files, _ := os.ReadDir(attDir)
	if len(files) != 2 {
		t.Fatalf("attachments %v", files)
	}
	// Nothing past its days yet.
	if n := e.Prune(now); n != 0 {
		t.Fatalf("pruned %d", n)
	}
	// The group rule now keeps 30 days: its 60-day-old lines go, the mention and its image stay.
	setRule(s, RuleGroup, func(r *MessageRule) { r.Days = intp(30) })
	if n := e.Prune(now); n != 2 { // the day rewritten, the plain image gone
		t.Fatalf("pruned %d", n)
	}
	after, _ := os.ReadFile(st.path(KindChat, "chat-g", "messages", day+".md"))
	var wantRows []string
	for _, r := range rowsOf(string(before)) {
		if !strings.Contains(r, "group line") && !(strings.Contains(r, "[附件](") && !strings.Contains(r, "02 S")) { // i18n-ignore: archive data
			wantRows = append(wantRows, r)
		}
	}
	if got := rowsOf(string(after)); !slices.Equal(got, wantRows) {
		t.Fatalf("rewritten\n%q\nwant\n%q", got, wantRows)
	}
	files, _ = os.ReadDir(attDir)
	if len(files) != 1 || !strings.Contains(string(after), files[0].Name()) {
		t.Fatalf("attachments left %v\n%s", files, after)
	}
	// Turning the mentions rule off keeps its lines text_days (365), not shorter: the 60-day-old mention stays.
	setRule(s, RuleMentions, func(r *MessageRule) { r.On = false })
	if n := e.Prune(now); n != 0 {
		t.Fatalf("an off rule pruned %d", n)
	}
	// Far in the future everything goes, ledger days too.
	if n := e.Prune(now.AddDate(20, 0, 0)); n == 0 {
		t.Fatal("nothing pruned in twenty years")
	}
	if l, _, _ := st.readLedger("chat-g"); len(l.Lines) != 0 {
		t.Fatalf("ledger days left %+v", l.Lines)
	}
}

type selSource struct {
	name string
	dl   DownloadFunc
}

func (d selSource) Name() string                                   { return d.name }
func (selSource) Chats(context.Context, int) ([]SourceChat, error) { return nil, nil }
func (selSource) Messages(context.Context, SourceChat, string, int) ([]SourceMessage, string, bool, error) {
	return nil, "", false, nil
}
func (d selSource) Download(ctx context.Context, media, dst string) error {
	return d.dl(ctx, media, dst)
}

// treeOf is every file under dir with its bytes.
func treeOf(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out[rel+"/"] = ""
			return nil
		}
		raw, err := os.ReadFile(p)
		out[rel] = string(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPruneSelectedDefaultsPruneAsBefore: with the default document (every days null), pruning removes exactly what
// it removed before the selection, for chats of every kind and source, with and without a ledger and a usual rule.
func TestPruneSelectedDefaultsPruneAsBefore(t *testing.T) {
	seed := time.Now().UnixNano()
	r := rand.New(rand.NewSource(seed))
	kinds := []string{"dm", "group", "bot", "official", "system", "mail", "issue", ""}
	rules := []string{"", RuleDM, RuleGroup, RuleMuted, RuleMentions, "mentions+mine", RuleItem, "nonsense"}
	now := time.Now()
	for iter := 0; iter < 40; iter++ {
		a := t.TempDir()
		st, err := NewStore(a)
		if err != nil {
			t.Fatal(err)
		}
		for c := 0; c < 6; c++ {
			source := []string{"chat", "wide", "mail"}[r.Intn(3)]
			ch := &Chat{ID: fmt.Sprintf("%s-c%d", source, c), Source: source, SourceID: fmt.Sprint("c", c), Kind: kinds[r.Intn(len(kinds))],
				Name: "N", Rule: rules[r.Intn(len(rules))]}
			if err := st.PutChat(ch); err != nil {
				t.Fatal(err)
			}
			ledger := retentionLedger{Version: 1, Rule: rules[r.Intn(len(rules))], Lines: map[string]map[string]string{}}
			for n := r.Intn(12); n > 0; n-- {
				at := now.AddDate(0, 0, -r.Intn(800)).Add(-time.Duration(r.Intn(1440)) * time.Minute)
				l := Line{At: at, Sender: "S", Text: randText(r)}
				if err := st.AppendMessages(ch, []Line{l}); err != nil {
					t.Fatal(err)
				}
				day := at.In(st.Zone()).Format("2006-01-02")
				if r.Intn(2) == 0 {
					if ledger.Lines[day] == nil {
						ledger.Lines[day] = map[string]string{}
					}
					ledger.Lines[day][lineKey(strings.TrimSuffix(l.Format(st.Zone()), "\n"))] = rules[r.Intn(len(rules))]
				}
				if r.Intn(2) == 0 {
					_ = writeFile(st.path(KindChat, ch.ID, "attachments", day, "messages", fmt.Sprint("f", n)), []byte("x"))
				}
			}
			if r.Intn(2) == 0 {
				_ = writeJSON(st.ledgerPath(ch.ID), ledger)
			}
		}
		b := t.TempDir()
		if err := os.CopyFS(b, os.DirFS(a)); err != nil {
			t.Fatal(err)
		}
		sb, err := NewStore(b)
		if err != nil {
			t.Fatal(err)
		}
		e := &Engine{Store: st, TextDays: []int{365, 30, 3650}[r.Intn(3)], AttachmentDays: []int{0, 30, 7}[r.Intn(3)],
			AttachmentDaysBySource: map[string]int{"wide": 3650}}
		when := now.AddDate(0, 0, r.Intn(400))
		def := Defaults()
		n1 := st.PruneBySource(when, or(e.TextDays, 365), e.AttachmentDaysFor)
		n2 := sb.PruneSelected(when, &def, e.Retention())
		ta, tb := treeOf(t, a), treeOf(t, b)
		// The ledgers themselves may lose the days that went: compare everything else.
		for _, tree := range []map[string]string{ta, tb} {
			for k := range tree {
				if strings.HasSuffix(k, retentionFile) {
					delete(tree, k)
				}
			}
		}
		if n1 != n2 || len(ta) != len(tb) {
			t.Fatalf("seed %d: removed %d / %d, %d / %d files", seed, n1, n2, len(ta), len(tb))
		}
		for k, v := range ta {
			if w, ok := tb[k]; !ok || w != v {
				t.Fatalf("seed %d: %s differs", seed, k)
			}
		}
	}
}

// checkPruned fails unless after is before without exactly gone rows: every other row byte for byte, in order, each
// with the line end it had (a row that is not last in after kept its "\n").
func checkPruned(t testing.TB, before, after string, gone int) {
	t.Helper()
	b, a := rowsOf(before), rowsOf(after)
	if len(b) != len(a)+gone {
		t.Fatalf("%d rows before, %d after, %d gone\nbefore:\n%q\nafter:\n%q", len(b), len(a), gone, before, after)
	}
	i := 0
	for _, r := range b {
		if i < len(a) && r == a[i] {
			i++
		}
	}
	if i != len(a) {
		t.Fatalf("row %d of after (%q) is not in before in order\nbefore:\n%q\nafter:\n%q", i, a[i], before, after)
	}
	if strings.HasSuffix(before, "\n") && after != "" && !strings.HasSuffix(after, "\n") {
		t.Fatalf("the final newline went missing: %q", after)
	}
}

// TestPruneDayKeepsEveryOtherLine is the guarantee the rewrite rests on, as TestMergeKeepsEveryLine for merging: over
// many random day files and choices of lines to drop, every row not of a dropped line is still there, byte for byte
// and in order, and exactly the dropped lines' rows are gone.
func TestPruneDayKeepsEveryOtherLine(t *testing.T) {
	zone := mergeZone(t)
	day := time.Date(2026, 7, 15, 0, 0, 0, 0, zone)
	seed := time.Now().UnixNano()
	r := rand.New(rand.NewSource(seed))
	for iter := 0; iter < 5000; iter++ {
		text, _ := genDay(r, zone, day)
		if r.Intn(5) == 0 {
			text += "  an orphan continuation\n- 10:00 S: with\n  two\n  continuations\n"
		}
		drop := map[string]bool{}
		keep := func(l dayLine) bool {
			if l.key == "" {
				t.Fatal("keep asked about a loose row")
			}
			if _, ok := drop[l.text]; !ok {
				drop[l.text] = r.Intn(3) == 0
			}
			return !drop[l.text]
		}
		out, left, dropped := pruneDay(text, keep)
		// The oracle: rows the dropped lines span, found independently of dayLines.
		rows := rowsOf(text)
		gone := 0
		for i := 0; i < len(rows); i++ {
			row := rows[i]
			if row == "" || strings.HasPrefix(row, "# ") {
				continue
			}
			if clockOf(row) == "" {
				continue // a loose row: always kept
			}
			j := i
			for j+1 < len(rows) && strings.HasPrefix(rows[j+1], "  ") {
				j++
			}
			if drop[strings.Join(rows[i:j+1], "\n")] {
				gone += j - i + 1
			}
			i = j
		}
		checkPruned(t, text, out, gone)
		if (dropped == 0) != (out == text) {
			t.Fatalf("seed %d: dropped %d but the text %s", seed, dropped, map[bool]string{true: "is the same", false: "changed"}[out == text])
		}
		if len(left) > 0 {
			again, _, n := pruneDay(out, keep)
			if n != 0 || again != out {
				t.Fatalf("seed %d: pruning again dropped %d", seed, n)
			}
		}
		if all, _, n := pruneDay(text, func(dayLine) bool { return true }); n != 0 || all != text {
			t.Fatalf("seed %d: keeping every line changed the file", seed)
		}
	}
}

func FuzzPruneDay(f *testing.F) {
	f.Add("# G · 2026-07-15\n\n- 09:00 S: a\n  b\n- 09:01 S: c\n", uint64(1))
	f.Add("- 99:99 x\n  y\nz", uint64(3))
	f.Add("", uint64(0))
	f.Add("\n\n\r\n- 10:00 S: x", uint64(0xff))
	f.Fuzz(func(t *testing.T, text string, bits uint64) {
		i := 0
		keep := func(dayLine) bool { i++; return bits>>(i%64)&1 == 0 }
		out, _, dropped := pruneDay(text, keep)
		_, _, lines := dayLines(text)
		gone := 0
		i = 0
		for _, l := range lines {
			if l.key != "" && !keep(l) {
				gone += l.last - l.first + 1
			}
		}
		checkPruned(t, text, out, gone)
		if dropped == 0 && out != text {
			t.Fatal("nothing dropped, the text changed")
		}
		if bytes.Contains([]byte(out), []byte("\x00")) != bytes.Contains([]byte(text), []byte("\x00")) && dropped == 0 {
			t.Fatal("bytes changed")
		}
	})
}

// TestPruneSelectedPerLine: random day files with ledger entries of rules with different days; each line is kept
// exactly when its own days say, every kept row byte for byte.
func TestPruneSelectedPerLine(t *testing.T) {
	seed := time.Now().UnixNano()
	r := rand.New(rand.NewSource(seed))
	s := sel(func(s *Selection) {
		setRule(s, RuleMentions, func(r *MessageRule) { r.Days = intp(3650) })
		setRule(s, RuleGroup, func(r *MessageRule) { r.On, r.Days = true, intp(20) })
		setRule(s, RuleMine, func(r *MessageRule) { r.Days = intp(90) })
		setRule(s, RuleMuted, func(r *MessageRule) { r.Days = intp(5) }) // off: text_days
	})
	ret := Retention{TextDays: 365}
	rules := []string{RuleMentions, RuleGroup, RuleMine, RuleMuted, "group+mine", ""}
	now := time.Now()
	for iter := 0; iter < 30; iter++ {
		st, err := NewStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		zone := st.Zone()
		usual := rules[r.Intn(len(rules))]
		ch := &Chat{ID: "chat-g", Source: "chat", SourceID: "g", Kind: "group", Name: "G", Rule: usual}
		_ = st.PutChat(ch)
		ledger := retentionLedger{Version: 1, Rule: usual, Lines: map[string]map[string]string{}}
		type want struct{ text, day string }
		var all []want
		for d := 0; d < 15; d++ {
			at := now.AddDate(0, 0, -[]int{1, 10, 30, 60, 100, 400, 4000}[r.Intn(7)])
			for n := 1 + r.Intn(5); n > 0; n-- {
				l := Line{At: at.Add(time.Duration(r.Intn(60)) * time.Minute), Sender: "S", Text: fmt.Sprintf("line %d %d %s", d, n, randText(r))}
				dayStr := l.At.In(zone).Format("2006-01-02")
				if err := st.AppendMessages(ch, []Line{l}); err != nil {
					t.Fatal(err)
				}
				f := strings.TrimSuffix(l.Format(zone), "\n")
				rule := usual
				if r.Intn(2) == 0 {
					rule = rules[r.Intn(len(rules))]
					if ledger.Lines[dayStr] == nil {
						ledger.Lines[dayStr] = map[string]string{}
					}
					ledger.Lines[dayStr][lineKey(f)] = rule
				}
				text, _ := s.LineDays(ret, "chat", "g", rule)
				if dayStr >= now.AddDate(0, 0, -text).In(zone).Format("2006-01-02") {
					all = append(all, want{f, dayStr})
				}
			}
		}
		_ = writeJSON(st.ledgerPath(ch.ID), ledger)
		before := map[string]string{}
		for _, d := range st.Days(ch.ID) {
			raw, _ := os.ReadFile(st.path(KindChat, ch.ID, "messages", d+".md"))
			before[d] = string(raw)
		}
		st.PruneSelected(now, s, ret)
		for d, b := range before {
			raw, err := os.ReadFile(st.path(KindChat, ch.ID, "messages", d+".md"))
			var keep []string
			for _, w := range all {
				if w.day == d {
					keep = append(keep, w.text)
				}
			}
			if len(keep) == 0 {
				if err == nil {
					t.Fatalf("seed %d: %s kept with no line within its days:\n%s", seed, d, raw)
				}
				continue
			}
			if err != nil {
				t.Fatalf("seed %d: %s deleted with %d lines within their days", seed, d, len(keep))
			}
			checkPruned(t, b, string(raw), len(rowsOf(b))-len(rowsOf(string(raw))))
			for _, k := range keep {
				if !strings.Contains(string(raw), k+"\n") {
					t.Fatalf("seed %d: %s lost %q", seed, d, k)
				}
			}
			got := 0
			for _, row := range rowsOf(string(raw)) {
				if clockOf(row) != "" {
					got++
				}
			}
			if got != len(keep) {
				t.Fatalf("seed %d: %s has %d lines, want %d:\n%s", seed, d, got, len(keep), raw)
			}
		}
	}
}

func TestWriteHistorySelection(t *testing.T) {
	s := sel(func(s *Selection) { setRule(s, RuleMentions, func(r *MessageRule) { r.Days = intp(3650) }) })
	st, e := selEngine(t, s)
	ctx := context.Background()
	at := time.Now().AddDate(0, 0, -800).Truncate(time.Hour)
	near := map[string]bool{"n": true}
	msgs := []SourceMessage{
		{ID: "p", Text: "plain", At: at, SenderName: "S"},
		{ID: "n", Text: "near mine", At: at.Add(time.Minute), SenderName: "S"},
		{ID: "m", Text: "a mention", At: at.Add(2 * time.Minute), SenderName: "S", Mentioned: true},
		{ID: "r", Text: "recent mine", At: time.Now().Add(-time.Hour), SenderName: "S", Mine: true},
	}
	c := &Chat{Source: "chat", SourceID: "g", Kind: "group", Name: "G"}
	added, err := e.WriteHistory(ctx, c, msgs, HistoryWrite{Chat: &ChatFacts{ID: "g", Kind: "group", Muted: true},
		NearMine: func(m SourceMessage) bool { return near[m.ID] }})
	if err != nil || added != 2 {
		t.Fatalf("added %d, %v", added, err)
	}
	// "near mine" is 800 days old: past the mine rule's 365 days. The mention is within its 3650.
	if got := linesOf(t, st, "chat-g"); !slices.Equal(got, []string{"a mention", "recent mine"}) {
		t.Fatalf("lines %q", got)
	}
	if c.Rule != RuleMuted {
		t.Fatalf("rule %q", c.Rule)
	}
	l, ok, _ := st.readLedger("chat-g")
	if !ok || l.Rule != RuleMuted || len(l.Lines) != 2 {
		t.Fatalf("ledger %+v", l)
	}
	// A per-item read:false: nothing is written.
	s.SetItem(SelectionItem{Source: "chat", Kind: ItemChat, ID: "g", Read: false})
	if added, err := e.WriteHistory(ctx, c, []SourceMessage{{ID: "x", Text: "x", At: time.Now().Add(-time.Minute), Mine: true}}, HistoryWrite{}); err != nil || added != 0 {
		t.Fatalf("an unread chat got %d, %v", added, err)
	}
}

func TestRezoneMovesTheLedger(t *testing.T) {
	st, e := selEngine(t, sel(func(s *Selection) { setRule(s, RuleMentions, func(r *MessageRule) { r.Days = intp(3650) }) }))
	at := time.Date(2026, 7, 15, 23, 30, 0, 0, time.UTC)
	if _, err := st.Rezone(time.UTC); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WriteHistory(context.Background(), &Chat{Source: "chat", SourceID: "g", Kind: "group", Name: "G"},
		[]SourceMessage{{ID: "m", Text: "a mention", At: at, SenderName: "S", Mentioned: true}}, HistoryWrite{}); err != nil {
		t.Fatal(err)
	}
	zone := mergeZone(t)
	if _, err := st.Rezone(zone); err != nil {
		t.Fatal(err)
	}
	l, _, _ := st.readLedger("chat-g")
	day := at.In(zone).Format("2006-01-02")
	key := lineKey(strings.TrimSuffix(Line{At: at, Sender: "S", Text: "a mention"}.Format(zone), "\n"))
	if l.Lines[day][key] != RuleMentions || len(l.Lines) != 1 {
		t.Fatalf("ledger after rezone %+v, want %s/%s", l.Lines, day, key)
	}
}

// A ledger that does not read holds up no write, and pruning keeps that chat's lines the most days any rule has.
func TestUnreadableLedgerLosesNothing(t *testing.T) {
	s := sel(func(s *Selection) {
		setRule(s, RuleGroup, func(r *MessageRule) { r.On, r.Days = true, intp(10) })
		setRule(s, RuleMentions, func(r *MessageRule) { r.Days = intp(3650) })
	})
	st, e := selEngine(t, s)
	ctx := context.Background()
	old := time.Now().AddDate(0, 0, -100)
	if _, err := e.Ingest(ctx, named("chat"), SourceChat{ID: "g", Kind: "group"}, []SourceMessage{{ID: "1", Text: "mention", At: old, Mentioned: true}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.ledgerPath("chat-g"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, err := e.Ingest(ctx, named("chat"), SourceChat{ID: "g", Kind: "group"}, []SourceMessage{{ID: "2", Text: "another", At: old.Add(time.Minute), Mentioned: true}}); err != nil || n != 1 {
		t.Fatalf("ingest with a broken ledger: %d, %v", n, err)
	}
	if n := e.Prune(time.Now()); n != 0 {
		t.Fatalf("pruned %d with a broken ledger", n)
	}
	if got := linesOf(t, st, "chat-g"); len(got) != 2 {
		t.Fatalf("lines %q", got)
	}
}

// A selection that cannot be read stands in as KeepEverything: everything is written, and nothing is pruned (a line
// the user keeps longer than text_days must not go because the document did not read).
func TestStopgapPrunesNothing(t *testing.T) {
	keep := KeepEverything()
	clone, d0 := keep.Clone(), Defaults()
	if !keep.Stopgap() || !clone.Stopgap() || d0.Stopgap() {
		t.Fatal("the stopgap mark does not hold")
	}
	if raw, _ := json.Marshal(keep); bytes.Contains(raw, []byte("stopgap")) {
		t.Fatalf("the stopgap mark is sent: %s", raw)
	}
	st, e := selEngine(t, &keep)
	now := time.Now()
	ch := &Chat{ID: "chat-g", Source: "chat", SourceID: "g", Kind: "group", Name: "G"}
	_ = st.PutChat(ch)
	for _, ago := range []int{1, 400, 4000} {
		if err := st.AppendMessages(ch, []Line{{At: now.AddDate(0, 0, -ago), Sender: "S", Text: fmt.Sprint("kept ", ago)}}); err != nil {
			t.Fatal(err)
		}
	}
	before := treeOf(t, st.Dir)
	if n := e.Prune(now); n != 0 {
		t.Fatalf("pruned %d under the stopgap", n)
	}
	if after := treeOf(t, st.Dir); !maps.Equal(before, after) {
		t.Fatal("the stopgap changed the archive")
	}
	// The same archive under a document that reads prunes as it says.
	d := Defaults()
	e.Selection = func() *Selection { return &d }
	if n := e.Prune(now); n == 0 {
		t.Fatal("a document that reads pruned nothing")
	}
}

// A chat of a kind the selection does not decide about (mail…) is written and kept as daily ingestion has it,
// whatever kind the history import judges it by, and whatever a ledger made for it before says of the other lines.
func TestOutsideKindKeepsTextDays(t *testing.T) {
	s := sel(func(s *Selection) { setRule(s, RuleGroup, func(r *MessageRule) { r.On, r.Days = true, intp(30) }) })
	st, e := selEngine(t, s)
	ctx := context.Background()
	now := time.Now()
	ch := &Chat{ID: "chat-m", Source: "chat", SourceID: "m", Kind: "mail", Name: "M"}
	_ = st.PutChat(ch)
	daily := Line{At: now.AddDate(0, 0, -100), Sender: "S", Text: "daily, 100 days old"}
	if err := st.AppendMessages(ch, []Line{daily}); err != nil {
		t.Fatal(err)
	}
	c := &Chat{Source: "chat", SourceID: "m"}
	added, err := e.WriteHistory(ctx, c, []SourceMessage{{ID: "h", Text: "imported, 50 days old", SenderName: "S", At: now.AddDate(0, 0, -50)}},
		HistoryWrite{Chat: &ChatFacts{ID: "m", Kind: "group"}})
	if err != nil || added != 1 {
		t.Fatalf("added %d, %v", added, err)
	}
	if _, ok, _ := st.readLedger("chat-m"); ok {
		t.Fatal("a chat outside the selection got a ledger")
	}
	e.Prune(now)
	if got := linesOf(t, st, "chat-m"); !slices.Equal(got, []string{"daily, 100 days old", "imported, 50 days old"}) {
		t.Fatalf("lines %q", got)
	}
	// A ledger a group judgement made for it (before this rule): the lines it does not name keep text_days.
	_ = writeJSON(st.ledgerPath("chat-m"), retentionLedger{Version: 1, Rule: RuleGroup})
	e.Prune(now)
	if got := linesOf(t, st, "chat-m"); !slices.Equal(got, []string{"daily, 100 days old", "imported, 50 days old"}) {
		t.Fatalf("lines after a group ledger %q", got)
	}
	if l, _, _ := st.readLedger("chat-m"); l.Rule != RuleGroup {
		t.Fatalf("the ledger's rule was rewritten: %+v", l)
	}
}
