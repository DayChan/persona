package kb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The archive data below is Chinese like the archive's own (i18n-ignore: test data).

// histStore is a store in a fixed zone and an engine over it that keeps text ten years.
func histStore(t *testing.T) (*Store, *Engine, *time.Location) {
	t.Helper()
	zone := mergeZone(t)
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Rezone(zone); err != nil {
		t.Fatal(err)
	}
	return st, &Engine{Store: st, TextDays: 3650}, zone
}

func dayText(t *testing.T, st *Store, chatID string, at time.Time) string {
	t.Helper()
	raw, _ := os.ReadFile(st.Path(KindChat, chatID, "messages", at.In(st.Zone()).Format("2006-01-02")+".md"))
	return string(raw)
}

// History is archive only: lines merged among the live ones, people noted, the count up, but no Pending, Cursor or
// LastAt, so it is never due for compression and the live archive goes on as before.
func TestWriteHistoryIsArchiveOnly(t *testing.T) {
	st, e, zone := histStore(t)
	ctx := context.Background()
	now := time.Now().In(zone)
	base := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, zone).AddDate(0, 0, -3).Add(9 * time.Hour)
	src := named("chat")
	if _, err := e.Ingest(ctx, src, SourceChat{ID: "g", Name: "群", Kind: "group"}, []SourceMessage{
		{ID: "l1", SenderID: "u1", SenderName: "张三", Text: "实时一", At: base.Add(2 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	live, _ := st.Chat("chat-g")
	live.Cursor = "c-9" // as the archive left it
	live.Pending = 0    // as compression left it
	live.CompressedAt = now.UTC()
	if err := st.PutChat(live); err != nil {
		t.Fatal(err)
	}
	live, _ = st.Chat("chat-g")
	before := dayText(t, st, "chat-g", base)

	hist := []SourceMessage{
		{ID: "h3", SenderID: "u2", SenderName: "李四", Text: "历史三", At: base.Add(time.Hour)},
		{ID: "h1", Mine: true, SenderName: "本人", Text: "历史一", At: base},
		{ID: "h2", SenderName: "", Text: "无名", At: base.Add(30 * time.Minute)},
		{ID: "dup", SenderID: "u1", SenderName: "张三", Text: "实时一", At: base.Add(2*time.Hour + 5*time.Second)}, // the live one
		{ID: "old", SenderID: "u3", SenderName: "王五", Text: "太旧", At: now.AddDate(-11, 0, 0)},
		{ID: "sys", Type: "SYSTEM", At: base},
	}
	c := &Chat{Source: "chat", SourceID: "g", Kind: "group", Name: "群"}
	n, err := e.WriteHistory(ctx, c, hist, HistoryWrite{Mode: HistoryFull})
	if err != nil || n != 3 {
		t.Fatalf("wrote %d %v", n, err)
	}
	after := dayText(t, st, "chat-g", base)
	checkKept(t, before, after, 3)
	if !strings.Contains(after, "- 09:00 我: 历史一\n- 09:30 ?: 无名\n- 10:00 李四: 历史三\n- 11:00 张三: 实时一\n") || strings.Contains(after, "太旧") {
		t.Fatalf("day:\n%s", after)
	}
	got, _ := st.Chat("chat-g")
	if got.Pending != 0 || got.Cursor != "c-9" || !got.LastAt.Equal(live.LastAt) || got.Messages != live.Messages+3 ||
		got.HistoryMode != HistoryFull || !got.Mine || !slices.Contains(got.People, "chat-u2") || c.Messages != got.Messages {
		t.Fatalf("chat: %+v", got)
	}
	if p, ok := st.Person("chat-u2"); !ok || !slices.Contains(p.Chats, "chat-g") {
		t.Fatalf("person: %+v", p)
	}
	if due := e.DueForCompression(now.Add(48 * time.Hour)); len(due) != 0 {
		t.Fatalf("due after history: %v", due[0].ID)
	}
	// The same page again (a restart before its state was saved) adds nothing.
	if n, err := e.WriteHistory(ctx, &Chat{ID: "chat-g"}, hist, HistoryWrite{}); n != 0 || err != nil {
		t.Fatalf("again: %d %v", n, err)
	}
	// The live archive goes on: appended after, counted Pending, LastAt moved, due as usual.
	if n, err := e.Ingest(ctx, src, SourceChat{ID: "g"}, []SourceMessage{{ID: "l2", SenderID: "u1", SenderName: "张三", Text: "实时二", At: base.Add(3 * time.Hour)}}); n != 1 || err != nil {
		t.Fatalf("live: %d %v", n, err)
	}
	got, _ = st.Chat("chat-g")
	if got.Pending != 1 || !got.LastAt.Equal(base.Add(3*time.Hour)) || got.Messages != live.Messages+4 || got.HistoryMode != HistoryFull {
		t.Fatalf("after live: %+v", got)
	}
	if !strings.HasSuffix(dayText(t, st, "chat-g", base), "- 11:00 张三: 实时一\n- 12:00 张三: 实时二\n") {
		t.Fatalf("day:\n%s", dayText(t, st, "chat-g", base))
	}
	if due := e.DueForCompression(now.Add(48 * time.Hour)); len(due) != 1 {
		t.Fatalf("due after live: %d", len(due))
	}
}

// A chat only history knows is made with the source's name and kind, and has no LastAt (the live archive's own).
func TestWriteHistoryNewChat(t *testing.T) {
	st, e, zone := histStore(t)
	at := time.Now().In(zone).AddDate(-2, 0, 0)
	c := &Chat{Source: "wx", SourceID: "dm-1", Kind: "dm", Name: "某人"}
	if n, err := e.WriteHistory(context.Background(), c, nil, HistoryWrite{Mode: HistoryAnchor}); n != 0 || err != nil {
		t.Fatalf("empty: %d %v", n, err)
	}
	if _, ok := st.Chat("wx-dm-1"); ok {
		t.Fatal("an empty batch made a chat")
	}
	n, err := e.WriteHistory(context.Background(), c, []SourceMessage{{ID: "a", SenderID: "p", SenderName: "某人", Text: "两年前", At: at}}, HistoryWrite{Mode: HistoryAnchor})
	if err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	got, ok := st.Chat("wx-dm-1")
	if !ok || got.Source != "wx" || got.SourceID != "dm-1" || got.Kind != "dm" || got.Name != "某人" || !got.LastAt.IsZero() ||
		got.Pending != 0 || got.Messages != 1 || got.HistoryMode != HistoryAnchor || got.Created.IsZero() {
		t.Fatalf("chat: %+v", got)
	}
	if text := dayText(t, st, "wx-dm-1", at); !strings.HasPrefix(text, "# 某人 · "+at.Format("2006-01-02")+"\n\n- ") {
		t.Fatalf("day:\n%s", text)
	}
	if _, err := e.WriteHistory(context.Background(), &Chat{Name: "x"}, nil, HistoryWrite{}); err == nil {
		t.Fatal("a chat without an id was accepted")
	}
	if _, err := e.WriteHistory(context.Background(), &Chat{ID: "x-1"}, []SourceMessage{{ID: "a", Text: "x", At: at}}, HistoryWrite{}); err == nil {
		t.Fatal("a new chat without its source was accepted")
	}
}

// History downloads images only, within the source's attachment days; other attachments say what they were.
func TestWriteHistoryAttachments(t *testing.T) {
	st, e, zone := histStore(t)
	e.AttachmentDaysBySource = map[string]int{"wx": 3650}
	var got []string
	dl := func(_ context.Context, media, dst string) error {
		got = append(got, media)
		if media == "broken" {
			return fmt.Errorf("gone")
		}
		return writeFile(dst, []byte("img"))
	}
	old := time.Now().In(zone).AddDate(-2, 0, 0).Truncate(time.Hour)
	msgs := []SourceMessage{
		{ID: "i", Type: "IMAGE", SenderName: "甲", Media: []string{"pic.png"}, At: old},
		{ID: "p", Type: "POST", SenderName: "甲", Text: "图文", Media: []string{"inline"}, At: old.Add(time.Minute)},
		{ID: "v", Type: "VIDEO", SenderName: "甲", Media: []string{"clip"}, At: old.Add(2 * time.Minute)},
		{ID: "f", Type: "FILE", SenderName: "甲", Text: "报告", Media: []string{"doc.pdf"}, At: old.Add(3 * time.Minute)},
		{ID: "a", Type: "AUDIO", SenderName: "甲", Media: []string{"voice"}, At: old.Add(4 * time.Minute)},
		{ID: "s", Type: "STICKER", SenderName: "甲", Media: []string{"sticker"}, At: old.Add(5 * time.Minute)},
		{ID: "b", Type: "IMAGE", SenderName: "甲", Media: []string{"broken"}, At: old.Add(6 * time.Minute)},
		{ID: "vo", Type: "VOICE", SenderName: "甲", At: old.Add(7 * time.Minute)}, // no media reference
	}
	if _, err := e.WriteHistory(context.Background(), &Chat{Source: "wx", SourceID: "g", Name: "群"}, msgs, HistoryWrite{Download: dl}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"pic.png", "inline", "broken"}) {
		t.Fatalf("downloaded %v", got)
	}
	text := dayText(t, st, "wx-g", old)
	day := old.Format("2006-01-02")
	for _, want := range []string{
		"甲: [附件](attachments/" + day + "/messages/" + attachmentName("i", 0, "pic.png", "IMAGE") + ")\n", "甲: 图文 [附件](attachments/" + day + "/messages/" + attachmentName("p", 0, "inline", "POST") + ")\n",
		"甲: [视频]\n", "甲: 报告 [文件]\n", "甲: [语音]\n", "甲: [附件]\n", "甲: [附件]\n", "甲: [语音]\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	if _, err := os.Stat(st.Path(KindChat, "wx-g", "attachments", day, "messages", attachmentName("i", 0, "pic.png", "IMAGE"))); err != nil {
		t.Fatal(err)
	}
	// A source without its own days keeps the global 30: a two-year-old image is not downloaded.
	got = nil
	if _, err := e.WriteHistory(context.Background(), &Chat{Source: "other", SourceID: "g", Name: "群"}, msgs[:1], HistoryWrite{Download: dl}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || !strings.Contains(dayText(t, st, "other-g", old), "甲: [附件]\n") {
		t.Fatalf("downloaded %v", got)
	}
}

// The live path (line, through Ingest) goes by the source's attachment days too.
func TestLineAttachmentDaysBySource(t *testing.T) {
	_, e, zone := histStore(t)
	e.AttachmentDaysBySource = map[string]int{"kept": 3650, "zero": 0}
	old := time.Now().In(zone).AddDate(-2, 0, 0)
	for _, c := range []struct {
		source string
		want   bool
	}{{"kept", true}, {"zero", false}, {"plain", false}} {
		var n int
		dl := func(_ context.Context, _, dst string) error { n++; return writeFile(dst, []byte("x")) }
		l := e.line(context.Background(), dl, &Chat{ID: c.source + "-c", Source: c.source}, SourceMessage{ID: "m", Type: "IMAGE", Media: []string{"a.png"}, At: old})
		if (n == 1) != c.want || strings.Contains(l.Text, "(") != c.want {
			t.Fatalf("%s: %d downloads, %q", c.source, n, l.Text)
		}
	}
	if d := (&Engine{}).AttachmentDaysFor("any"); d != 30 {
		t.Fatalf("default %d", d)
	}
	if d := (&Engine{AttachmentDays: 7, AttachmentDaysBySource: map[string]int{"x": -1}}).AttachmentDaysFor("x"); d != 7 {
		t.Fatalf("negative %d", d)
	}
}

// Prune deletes attachment days by each chat's source: a two-year-old one stays for a source kept 3650 days.
func TestPruneAttachmentsBySource(t *testing.T) {
	st, e, _ := histStore(t)
	e.AttachmentDaysBySource = map[string]int{"kept": 3650}
	now := time.Now()
	old := now.AddDate(-2, 0, 0).In(st.Zone()).Format("2006-01-02")
	recent := now.AddDate(0, 0, -3).In(st.Zone()).Format("2006-01-02")
	for _, id := range []string{"kept-a", "plain-a"} {
		source, _, _ := strings.Cut(id, "-")
		if err := st.PutChat(&Chat{ID: id, Source: source, SourceID: "a"}); err != nil {
			t.Fatal(err)
		}
		for _, d := range []string{old, recent} {
			if err := writeFile(st.Path(KindChat, id, "attachments", d, "x.png"), []byte("x")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if n := e.Prune(now); n != 1 {
		t.Fatalf("pruned %d", n)
	}
	exists := func(id, d string) bool {
		_, err := os.Stat(st.Path(KindChat, id, "attachments", d))
		return err == nil
	}
	if !exists("kept-a", old) || exists("plain-a", old) || !exists("kept-a", recent) || !exists("plain-a", recent) {
		t.Fatal("wrong days pruned")
	}
	// Without per-source days nothing changes: Store.Prune with the global days.
	if n := st.Prune(now, 3650, 30); n != 1 || exists("kept-a", old) {
		t.Fatalf("global prune: %d", n)
	}
}

// Import states: kept per chat under history/state/, read back, invisible to lists and search, and gone with
// their source.
func TestHistoryState(t *testing.T) {
	st, e, _ := histStore(t)
	if _, ok, err := st.HistoryState("wx-a"); ok || err != nil {
		t.Fatalf("none: %v %v", ok, err)
	}
	fs := &AnchorFilterState{Ring: []SourceMessage{{ID: "r", Text: "缓冲", SenderHandles: []Handle{{Kind: "phone", Value: "+10000000000"}}}}, Older: 3}
	in := HistoryState{Source: "wx", Mode: HistoryAnchor, Cursor: "pos-1secretcursor", Read: 10, Written: 4, State: HistoryPaused, LastError: "rate limited", Filter: fs}
	for _, id := range []string{"wx-a", "wx-b"} {
		if err := st.PutHistoryState(id, in); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutHistoryState("wx-x-a", HistoryState{Source: "wx-x", Mode: HistoryFull, State: HistoryDone}); err != nil {
		t.Fatal(err)
	}
	out, ok, err := st.HistoryState("wx-a")
	if !ok || err != nil || out.Cursor != in.Cursor || out.State != HistoryPaused || out.UpdatedAt.IsZero() || out.Filter == nil ||
		out.Filter.Older != 3 || len(out.Filter.Ring) != 1 || out.Filter.Ring[0].SenderHandles != nil || out.Finished() {
		t.Fatalf("read back %+v %v %v", out, ok, err)
	}
	if fs.Ring[0].SenderHandles == nil {
		t.Fatal("writing the state changed the caller's filter state")
	}
	raw, _ := os.ReadFile(filepath.Join(st.Dir, "history", "state", "wx-a.json"))
	if strings.Contains(string(raw), "+10000000000") {
		t.Fatal("identity hints written to disk")
	}
	if fi, err := os.Stat(filepath.Join(st.Dir, "history", "state", "wx-a.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", fi, err)
	}
	if err := os.WriteFile(filepath.Join(st.Dir, "history", "state", "bad.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.HistoryState("bad"); err == nil {
		t.Fatal("an unreadable state read as none")
	}
	if len(e.Find("secretcursor", 10)) != 0 || len(e.Search("secretcursor", true, 10)) != 0 || len(e.Find("rate limited", 10)) != 0 {
		t.Fatal("import state found by search")
	}
	if slices.Contains(st.IDs(KindChat), "history") || len(st.Chats()) != 0 {
		t.Fatal("import states listed")
	}
	if n := e.Prune(time.Now().AddDate(20, 0, 0)); n != 0 {
		t.Fatalf("prune touched %d", n)
	}
	if _, err := e.ForgetSource("wx"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"wx-a": false, "wx-b": false, "wx-x-a": true} {
		if _, ok, _ := st.HistoryState(id); ok != want {
			t.Fatalf("%s after forgetting wx: %v", id, ok)
		}
	}
	for _, s := range []string{HistoryDone, HistoryError, HistoryUnreadable} {
		if !(HistoryState{State: s}).Finished() {
			t.Fatal(s)
		}
	}
}

// conversation is n messages a minute apart, oldest first, positions their indexes; anchors at the given ones.
func conversation(n int, anchors ...int) []SourceMessage {
	base := time.Date(2025, 3, 1, 8, 0, 0, 0, time.UTC)
	out := make([]SourceMessage, n)
	for i := range out {
		out[i] = SourceMessage{ID: fmt.Sprint(i), Position: fmt.Sprint(i), SenderID: "u", SenderName: "某人", Text: fmt.Sprint("消息", i), At: base.Add(time.Duration(i) * time.Minute)}
	}
	for _, a := range anchors {
		out[a].Mine = true
	}
	return out
}

func mine(m SourceMessage) bool { return m.Mine }

// expectAnchor is what an anchor import should keep, worked out directly: the indexes within span of an anchor, and
// the omission lines (count, index of the oldest left out) of each gap that borders a kept window.
func expectAnchor(msgs []SourceMessage, span int) (kept []int, gaps [][2]int) {
	keep := make([]bool, len(msgs))
	any := false
	for i, m := range msgs {
		if m.Mine {
			any = true
			for j := max(0, i-span); j <= min(len(msgs)-1, i+span); j++ {
				keep[j] = true
			}
		}
	}
	if !any {
		return nil, nil
	}
	for i := 0; i < len(msgs); {
		if keep[i] {
			kept = append(kept, i)
			i++
			continue
		}
		j := i
		for j < len(msgs) && !keep[j] {
			j++
		}
		gaps = append(gaps, [2]int{j - i, i})
		i = j
	}
	return kept, gaps
}

// runAnchor reads msgs backwards in pages of random sizes (each page oldest first, as sources return them) and
// returns everything the filter emitted; with resume, the filter is rebuilt from its JSON state after every page.
func runAnchor(t *testing.T, r *rand.Rand, msgs []SourceMessage, span int, resume bool) []SourceMessage {
	t.Helper()
	f := NewAnchorFilter(mine, span, nil)
	var out []SourceMessage
	for end := len(msgs); end > 0; {
		size := 1 + r.Intn(16) // around a position a source returns limit+10, backwards limit+5: never trust a count
		start := max(0, end-size)
		emitted := f.Push(msgs[start:end])
		if !slices.IsSortedFunc(emitted, func(a, b SourceMessage) int { return a.At.Compare(b.At) }) {
			t.Fatalf("a page's output is not oldest first: %v", emitted)
		}
		out = append(out, emitted...)
		end = start
		if resume {
			raw, err := json.Marshal(f.State())
			if err != nil {
				t.Fatal(err)
			}
			var st AnchorFilterState
			if err := json.Unmarshal(raw, &st); err != nil {
				t.Fatal(err)
			}
			f = NewAnchorFilter(mine, span, &st)
		}
	}
	return append(out, f.Flush()...)
}

func TestAnchorFilter(t *testing.T) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	cases := []struct {
		n       int
		span    int
		anchors []int
	}{
		{300, 50, []int{150}},
		{300, 50, []int{100, 160}}, // overlapping windows merge
		{300, 50, []int{100, 201}}, // touching: no gap between
		{300, 50, []int{100, 202}}, // one message between
		{300, 50, []int{0, 299}},   // at both ends
		{300, 50, nil},             // no anchor: nothing at all
		{40, 50, []int{20}},        // shorter than a window
		{1000, 5, []int{3, 500, 504, 990}},
	}
	for i := 0; i < 30; i++ {
		var a []int
		for j := r.Intn(8); j > 0; j-- {
			a = append(a, r.Intn(400))
		}
		cases = append(cases, struct {
			n       int
			span    int
			anchors []int
		}{400, 1 + r.Intn(30), a})
	}
	for _, c := range cases {
		msgs := conversation(c.n, c.anchors...)
		kept, gaps := expectAnchor(msgs, c.span)
		for _, resume := range []bool{false, true} {
			out := runAnchor(t, r, msgs, c.span, resume)
			var gotKept []int
			var gotGaps [][2]int
			for _, m := range out {
				if m.Type == TypeOmitted {
					var n int
					if _, err := fmt.Sscanf(strings.TrimPrefix(m.Text, "[……此处省略 "), "%d", &n); err != nil {
						t.Fatalf("omission line %q", m.Text)
					}
					gotGaps = append(gotGaps, [2]int{n, int(m.At.Sub(msgs[0].At) / time.Minute)})
					continue
				}
				var idx int
				fmt.Sscan(m.Position, &idx)
				gotKept = append(gotKept, idx)
			}
			slices.Sort(gotKept)
			slices.SortFunc(gotGaps, func(a, b [2]int) int { return a[1] - b[1] })
			if !slices.Equal(gotKept, kept) || !slices.Equal(gotGaps, gaps) {
				t.Fatalf("n=%d span=%d anchors=%v resume=%v:\nkept %v\nwant %v\ngaps %v\nwant %v", c.n, c.span, c.anchors, resume, gotKept, kept, gotGaps, gaps)
			}
		}
	}
}

// What an anchor import writes reads in order: the windows, and between them one omission line, sender "·".
func TestAnchorImportWritesOmissions(t *testing.T) {
	st, e, _ := histStore(t)
	msgs := conversation(30, 5, 25)
	f := NewAnchorFilter(mine, 2, nil)
	c := &Chat{Source: "wx", SourceID: "g", Name: "群", Kind: "group"}
	for end := len(msgs); end > 0; end -= 7 {
		if _, err := e.WriteHistory(context.Background(), c, f.Push(msgs[max(0, end-7):end]), HistoryWrite{Mode: HistoryAnchor}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.WriteHistory(context.Background(), c, f.Flush(), HistoryWrite{Mode: HistoryAnchor}); err != nil {
		t.Fatal(err)
	}
	text := dayText(t, st, "wx-g", msgs[0].At)
	var got []string
	for _, l := range rowsOf(text)[2:] {
		_, rest, _ := strings.Cut(l[2:], " ")
		got = append(got, rest)
	}
	want := []string{"·: [……此处省略 3 条]", "某人: 消息3", "某人: 消息4", "我: 消息5", "某人: 消息6", "某人: 消息7", "·: [……此处省略 15 条]",
		"某人: 消息23", "某人: 消息24", "我: 消息25", "某人: 消息26", "某人: 消息27", "·: [……此处省略 2 条]"}
	if !slices.Equal(got, want) {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got, _ := st.Chat("wx-g"); got.Messages != len(want) || !slices.Equal(got.People, []string{"wx-u"}) {
		t.Fatalf("chat %+v", got)
	}
	if line := OmissionLine(msgs[0].At, 7); line.Type != TypeOmitted || line.SenderName != "·" || line.Text != "[……此处省略 7 条]" {
		t.Fatalf("%+v", line)
	}
}

// Windows counts windows, merged ones once: anchors whose windows overlap or touch are one window, a gap makes another
// (also across a resume from the saved state).
func TestAnchorFilterCountsMergedWindowsOnce(t *testing.T) {
	for _, c := range []struct {
		anchors []int
		windows int
	}{
		{nil, 0},
		{[]int{150}, 1},
		{[]int{100, 160}, 1}, // overlapping
		{[]int{100, 201}, 1}, // touching
		{[]int{100, 202}, 2}, // one message between
		{[]int{10, 12, 14, 200, 290}, 2},
	} {
		msgs := conversation(300, c.anchors...)
		f := NewAnchorFilter(mine, 50, nil)
		for end := len(msgs); end > 0; end -= 13 {
			f.Push(msgs[max(0, end-13):end])
			st := f.State()
			f = NewAnchorFilter(mine, 50, &st)
		}
		f.Flush()
		if got := f.State().Windows; got != c.windows {
			t.Fatalf("anchors %v: %d windows, want %d", c.anchors, got, c.windows)
		}
	}
}

// downloading is a source that saves every attachment (or fails, when fail is set).
type downloading struct {
	named
	fail bool
}

func (d downloading) Download(_ context.Context, _, dst string) error {
	if d.fail {
		return fmt.Errorf("gone")
	}
	return writeFile(dst, []byte("x"))
}

// Where an import meets the live archive, a message with attachments is the same message however its attachments
// were written: a saved link, one "[附件]" for an old message's, or history's placeholder naming the kind.
func TestWriteHistoryOverlapsLiveMedia(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		live Source // how the live archive saved them
		dl   DownloadFunc
		days int // the live archive's attachment days (the messages are 3 days old)
	}{
		{"saved, history downloads nothing", downloading{named: "chat"}, nil, 0},
		{"saved, history download fails", downloading{named: "chat"}, func(context.Context, string, string) error { return fmt.Errorf("gone") }, 0},
		{"saved, history downloads again", downloading{named: "chat"}, func(_ context.Context, _, dst string) error { return writeFile(dst, []byte("x")) }, 0},
		{"live download failed", downloading{named: "chat", fail: true}, nil, 0},
		{"too old for the live archive", downloading{named: "chat"}, nil, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, e, zone := histStore(t)
			e.AttachmentDays = tc.days
			now := time.Now().In(zone)
			base := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, zone).AddDate(0, 0, -3)
			msgs := []SourceMessage{
				{ID: "i", Type: "IMAGE", SenderID: "u", SenderName: "甲", Media: []string{"pic.png"}, At: base},
				{ID: "f", Type: "FILE", SenderID: "u", SenderName: "甲", Text: "报告", Media: []string{"doc.pdf"}, At: base.Add(time.Minute)},
				{ID: "a", Type: "AUDIO", SenderID: "u", SenderName: "甲", Media: []string{"voice.opus"}, At: base.Add(2 * time.Minute)},
				{ID: "v", Type: "VIDEO", SenderID: "u", SenderName: "甲", Media: []string{"a.mp4", "b.mp4"}, At: base.Add(3 * time.Minute)},
				{ID: "p", Type: "POST", SenderID: "u", SenderName: "甲", Text: "图文", Media: []string{"x", "y"}, At: base.Add(4 * time.Minute)},
			}
			if n, err := e.Ingest(ctx, tc.live, SourceChat{ID: "g", Name: "群", Kind: "group"}, msgs); n != len(msgs) || err != nil {
				t.Fatalf("live: %d %v", n, err)
			}
			before := dayText(t, st, "chat-g", base)
			n, err := e.WriteHistory(ctx, &Chat{ID: "chat-g"}, msgs, HistoryWrite{Download: tc.dl})
			if err != nil || n != 0 {
				t.Fatalf("history added %d %v; day:\n%s", n, err, dayText(t, st, "chat-g", base))
			}
			if after := dayText(t, st, "chat-g", base); after != before {
				t.Fatalf("day changed:\n%s\nwas:\n%s", after, before)
			}
		})
	}
}

// Attachments count the same however they were written; the text before them still tells messages apart.
func TestDupKey(t *testing.T) {
	same := [][]string{
		{"- 09:00 甲: [附件](attachments/2026-07-15/i-0.png)", "- 09:00 甲: [附件]", "- 09:00 甲:  [附件]"},
		{"- 09:00 甲: 报告 [附件](attachments/2026-07-15/f-0.pdf)", "- 09:00 甲: 报告 [文件]", "- 09:00 甲: 报告 [附件]"},
		{"- 09:00 甲: [附件](attachments/d/a-0.opus)", "- 09:00 甲: [语音]"},
		{"- 09:00 甲: [附件](attachments/d/v-0.mp4) [附件](attachments/d/v-1.mp4)", "- 09:00 甲: [视频] [视频]", "- 09:00 甲: [附件]"},
		{"- 09:00 甲: 两行\n  第二行 [附件](attachments/d/p-0.jpg)", "- 09:00 甲: 两行\n  第二行 [附件]"},
	}
	for _, g := range same {
		for _, m := range g[1:] {
			if dupKey(m) != dupKey(g[0]) {
				t.Errorf("%q and %q differ", m, g[0])
			}
		}
	}
	differ := [][2]string{
		{"- 09:00 甲: 报告 [文件]", "- 09:00 甲: 报告"},       // a file, and text alone
		{"- 09:00 甲: 报告 [文件]", "- 09:00 甲: 总结 [文件]"},  // other text
		{"- 09:00 甲: [附件] 之后还有字", "- 09:00 甲: 之后还有字"}, // not at the end: text
		{"- 09:00 甲: [文件]", "- 09:00 乙: [文件]"},
		{"- 09:00 甲: [附件](a b)", "- 09:00 甲: [附件]"}, // not a saved file's link (paths have no spaces)
	}
	for _, d := range differ {
		if dupKey(d[0]) == dupKey(d[1]) {
			t.Errorf("%q and %q match", d[0], d[1])
		}
	}
}

// Attachments never outlive their lines: a source's attachment days are capped by the text retention, for downloads
// and for cleanup alike.
func TestAttachmentDaysCappedByText(t *testing.T) {
	st, e, _ := histStore(t)
	e.TextDays = 365
	e.AttachmentDaysBySource = map[string]int{"kept": 3650}
	if d := e.AttachmentDaysFor("kept"); d != 365 {
		t.Fatalf("capped %d", d)
	}
	if d := (&Engine{TextDays: 3650, AttachmentDaysBySource: map[string]int{"kept": 3650}}).AttachmentDaysFor("kept"); d != 3650 {
		t.Fatalf("within the text retention %d", d)
	}
	if d := (&Engine{TextDays: 10}).AttachmentDaysFor("any"); d != 10 {
		t.Fatalf("global days capped %d", d)
	}
	now := time.Now()
	old := now.AddDate(-2, 0, 0).In(st.Zone()).Format("2006-01-02")
	if err := st.PutChat(&Chat{ID: "kept-a", Source: "kept", SourceID: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(st.Path(KindChat, "kept-a", "attachments", old, "x.png"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	e.Prune(now)
	if _, err := os.Stat(st.Path(KindChat, "kept-a", "attachments", old)); !os.IsNotExist(err) {
		t.Fatalf("an attachment past the text retention stayed: %v", err)
	}
}

// Imported history downloads a rich-text post's inline images, but not a video or file it embeds (by the reference's
// file type): those keep a placeholder naming their kind.
func TestWriteHistoryPostMedia(t *testing.T) {
	st, e, zone := histStore(t)
	e.AttachmentDaysBySource = map[string]int{"wx": 3650}
	var got []string
	dl := func(_ context.Context, media, dst string) error {
		got = append(got, media)
		return writeFile(dst, []byte("img"))
	}
	at := time.Now().In(zone).AddDate(0, -1, 0).Truncate(time.Hour)
	msgs := []SourceMessage{{ID: "p", Type: "POST", SenderName: "甲", Text: "图文", Media: []string{"inline-image", "clip.mp4", "report.pdf", "photo.jpeg"}, At: at}}
	if _, err := e.WriteHistory(context.Background(), &Chat{Source: "wx", SourceID: "g", Name: "群"}, msgs, HistoryWrite{Download: dl}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{"inline-image", "photo.jpeg"}) {
		t.Fatalf("downloaded %v", got)
	}
	text := dayText(t, st, "wx-g", at)
	if !strings.Contains(text, "[视频]") || !strings.Contains(text, "[文件]") || strings.Count(text, "[附件](attachments/") != 2 {
		t.Fatalf("archived:\n%s", text)
	}
}

// A download that returns ErrDownloadAborted aborts the whole write: nothing of the batch goes in, no chat is made,
// and the downloads after it are not asked for, so the caller can read the same messages again later.
func TestWriteHistoryDownloadAborted(t *testing.T) {
	st, e, zone := histStore(t)
	e.AttachmentDaysBySource = map[string]int{"wx": 3650}
	var got []string
	dl := func(_ context.Context, media, dst string) error {
		got = append(got, media)
		if media == "second" {
			return fmt.Errorf("stopping: %w", ErrDownloadAborted)
		}
		return writeFile(dst, []byte("img"))
	}
	at := time.Now().In(zone).AddDate(0, 0, -3).Truncate(time.Hour)
	msgs := []SourceMessage{
		{ID: "t", Type: "TEXT", SenderName: "A", Text: "before", At: at},
		{ID: "i1", Type: "IMAGE", SenderName: "A", Media: []string{"first"}, At: at.Add(time.Minute)},
		{ID: "i2", Type: "IMAGE", SenderName: "A", Media: []string{"second"}, At: at.Add(2 * time.Minute)},
		{ID: "i3", Type: "IMAGE", SenderName: "A", Media: []string{"third"}, At: at.Add(3 * time.Minute)},
	}
	added, err := e.WriteHistory(context.Background(), &Chat{Source: "wx", SourceID: "g", Name: "G"}, msgs, HistoryWrite{Download: dl})
	if !errors.Is(err, ErrDownloadAborted) || added != 0 {
		t.Fatalf("added %d, err %v", added, err)
	}
	if !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("downloaded %v", got)
	}
	if _, ok := st.Chat("wx-g"); ok {
		t.Fatal("an aborted write made the chat")
	}
}

// A new chat that names no source fails before anything is downloaded into its directory.
func TestWriteHistoryNewChatFailsFirst(t *testing.T) {
	st, e, zone := histStore(t)
	called := false
	dl := func(context.Context, string, string) error { called = true; return nil }
	at := time.Now().In(zone).Add(-time.Hour)
	_, err := e.WriteHistory(context.Background(), &Chat{ID: "x-1"}, []SourceMessage{{ID: "i", Type: "IMAGE", Media: []string{"p"}, At: at}}, HistoryWrite{Download: dl})
	if err == nil || called {
		t.Fatalf("err %v, downloaded %v", err, called)
	}
	if _, err := os.Stat(st.Path(KindChat, "x-1")); !os.IsNotExist(err) {
		t.Fatalf("chat directory: %v", err)
	}
}

// importing is a model that has a page of history written into the chat while it writes the summary.
type importing struct {
	e    *Engine
	at   time.Time
	next Model
}

func (m importing) JSONMax(ctx context.Context, system, user string, n int, out any) error {
	if strings.Contains(system, "项目档案") {
		return m.next.JSONMax(ctx, system, user, n, out)
	}
	if _, err := m.e.WriteHistory(ctx, &Chat{ID: "chat-g"}, []SourceMessage{
		{ID: "h1", SenderID: "u9", SenderName: "赵六", Text: "很久以前", At: m.at},
	}, HistoryWrite{Mode: HistoryFull}); err != nil {
		return err
	}
	if _, err := m.e.Ingest(ctx, named("chat"), SourceChat{ID: "g"}, []SourceMessage{
		{ID: "l2", SenderID: "u1", SenderName: "张三", Text: "又一条", At: time.Now().Add(-time.Minute)},
	}); err != nil {
		return err
	}
	return m.next.JSONMax(ctx, system, user, n, out)
}

// What a history import and the live archive write while a compression waits on the model stays: compression writes
// back only what it decided, and the message archived meanwhile stays pending.
func TestCompressKeepsWhatHistoryWrote(t *testing.T) {
	st, e, zone := histStore(t)
	ctx := context.Background()
	now := time.Now().In(zone)
	e.Model = importing{e: e, at: now.AddDate(-1, 0, 0), next: &fakeModel{}}
	if _, err := e.Ingest(ctx, named("chat"), SourceChat{ID: "g", Name: "群", Kind: "group"}, []SourceMessage{
		{ID: "l1", SenderID: "u1", SenderName: "张三", Text: "实时一", At: now.Add(-2 * time.Hour)},
	}); err != nil {
		t.Fatal(err)
	}
	c, _ := st.Chat("chat-g")
	if err := e.Compress(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Chat("chat-g")
	if got.Messages != 3 || got.HistoryMode != HistoryFull || !slices.Contains(got.People, "chat-u9") || got.Pending != 2 ||
		got.CompressedAt.IsZero() || got.OneLine == "" || got.CoveredUntil.After(now) {
		t.Fatalf("after compression: %+v", got)
	}
}

// A state names its source, or forgetting another source whose name starts the same could take it.
func TestHistoryStateNeedsSource(t *testing.T) {
	st, _, _ := histStore(t)
	if err := st.PutHistoryState("a-b-1", HistoryState{Mode: HistoryFull, State: HistoryRunning}); err == nil {
		t.Fatal("a state without its source was written")
	}
}

// Keep keeps the messages it picks on their own (a mention of the owner), each splitting the gap it falls in, and a
// negative span keeps an anchor alone; worked out directly and compared over random conversations and page sizes, with
// and without a resume from the saved state after every page.
func TestAnchorFilterKeepsMentionsAlone(t *testing.T) {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	mention := func(m SourceMessage) bool { return m.Mentioned }
	for i := 0; i < 200; i++ {
		n, span := 1+r.Intn(300), r.Intn(20)-3 // a negative span: anchors alone
		msgs := conversation(n)
		for j := r.Intn(6); j > 0; j-- {
			msgs[r.Intn(n)].Mine = true
		}
		for j := r.Intn(6); j > 0; j-- {
			msgs[r.Intn(n)].Mentioned = true
		}
		width := max(span, 0)
		if span == 0 {
			width = AnchorSpan
		}
		keep := make([]bool, n)
		any := false
		for k, m := range msgs {
			switch {
			case m.Mine:
				for j := max(0, k-width); j <= min(n-1, k+width); j++ {
					keep[j] = true
				}
				any = true
			case m.Mentioned:
				keep[k], any = true, true
			}
		}
		var kept []int
		var gaps [][2]int
		for k := 0; any && k < n; {
			if keep[k] {
				kept = append(kept, k)
				k++
				continue
			}
			j := k
			for j < n && !keep[j] {
				j++
			}
			gaps = append(gaps, [2]int{j - k, k})
			k = j
		}
		for _, resume := range []bool{false, true} {
			f := NewAnchorFilter(mine, span, nil)
			f.Keep = mention
			var out []SourceMessage
			for end := n; end > 0; {
				start := max(0, end-1-r.Intn(16))
				out = append(out, f.Push(msgs[start:end])...)
				end = start
				if resume {
					raw, _ := json.Marshal(f.State())
					var st AnchorFilterState
					if err := json.Unmarshal(raw, &st); err != nil {
						t.Fatal(err)
					}
					f = NewAnchorFilter(mine, span, &st)
					f.Keep = mention
				}
			}
			out = append(out, f.Flush()...)
			var gotKept []int
			var gotGaps [][2]int
			for _, m := range out {
				if m.Type == TypeOmitted {
					var c int
					if _, err := fmt.Sscanf(strings.TrimPrefix(m.Text, "[……此处省略 "), "%d", &c); err != nil { // i18n-ignore: archive data
						t.Fatalf("omission line %q", m.Text)
					}
					gotGaps = append(gotGaps, [2]int{c, int(m.At.Sub(msgs[0].At) / time.Minute)})
					continue
				}
				var idx int
				fmt.Sscan(m.Position, &idx)
				gotKept = append(gotKept, idx)
			}
			slices.Sort(gotKept)
			slices.SortFunc(gotGaps, func(a, b [2]int) int { return a[1] - b[1] })
			if !slices.Equal(gotKept, kept) || !slices.Equal(gotGaps, gaps) {
				t.Fatalf("n=%d span=%d resume=%v:\nkept %v\nwant %v\ngaps %v\nwant %v", n, span, resume, gotKept, kept, gotGaps, gaps)
			}
		}
	}
}
