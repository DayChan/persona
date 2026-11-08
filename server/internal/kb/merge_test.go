package kb

import (
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The archive data below is Chinese like the archive's own (i18n-ignore: test data).

func mergeZone(t testing.TB) *time.Location {
	t.Helper()
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("no zone data")
	}
	return l
}

// rowsOf splits a day file into its lines, without the empty string after a final "\n".
func rowsOf(text string) []string {
	if text == "" {
		return nil
	}
	rows := strings.Split(text, "\n")
	if strings.HasSuffix(text, "\n") {
		rows = rows[:len(rows)-1]
	}
	return rows
}

// checkKept fails unless every line of before is in after, byte for byte and in order (an exact subsequence), and
// after has exactly extra lines more: nothing of before changed, went missing or moved.
func checkKept(t testing.TB, before, after string, extra int) {
	t.Helper()
	b, a := rowsOf(before), rowsOf(after)
	if len(a) != len(b)+extra {
		t.Fatalf("%d lines before, %d after, %d added\nbefore:\n%q\nafter:\n%q", len(b), len(a), extra, before, after)
	}
	i := 0
	for _, r := range a {
		if i < len(b) && r == b[i] {
			i++
		}
	}
	if i != len(b) {
		t.Fatalf("line %d of before (%q) is not in after in order\nbefore:\n%q\nafter:\n%q", i, b[i], before, after)
	}
	if strings.HasSuffix(before, "\n") && !strings.HasSuffix(after, "\n") {
		t.Fatalf("the final newline went missing: %q", after)
	}
}

func TestMergeDayTargeted(t *testing.T) {
	zone := mergeZone(t)
	at := func(hhmm string) time.Time {
		v, _ := time.ParseInLocation("2006-01-02 15:04", "2026-07-15 "+hhmm, zone)
		return v.Add(17 * time.Second)
	}
	header := "# 群 · 2026-07-15\n\n"
	cases := []struct {
		name  string
		text  string
		lines []Line
		want  string
	}{
		{"in between", header + "- 09:00 张三: 早\n- 11:00 李四: 午\n",
			[]Line{{At: at("10:00"), Sender: "我", Text: "十点"}},
			header + "- 09:00 张三: 早\n- 10:00 我: 十点\n- 11:00 李四: 午\n"},
		{"before all, after the header", header + "- 09:00 张三: 早\n",
			[]Line{{At: at("08:00"), Sender: "我", Text: "八点"}},
			header + "- 08:00 我: 八点\n- 09:00 张三: 早\n"},
		{"same minute goes after", header + "- 09:00 张三: 早\n- 09:01 李四: b\n",
			[]Line{{At: at("09:00"), Sender: "我", Text: "也早"}},
			header + "- 09:00 张三: 早\n- 09:00 我: 也早\n- 09:01 李四: b\n"},
		{"a continuation stays with its message", header + "- 09:00 张三: 第一行\n  第二行\n- 11:00 李四: 午\n",
			[]Line{{At: at("09:30"), Sender: "我", Text: "半"}},
			header + "- 09:00 张三: 第一行\n  第二行\n- 09:30 我: 半\n- 11:00 李四: 午\n"},
		{"an unreadable line stays with the line above", header + "- 09:00 张三: 早\n乱码一行\n- 11:00 李四: 午\n",
			[]Line{{At: at("10:00"), Sender: "我", Text: "十点"}},
			header + "- 09:00 张三: 早\n乱码一行\n- 10:00 我: 十点\n- 11:00 李四: 午\n"},
		{"duplicates dropped, against the file and the batch", header + "- 09:00 张三: 早\n",
			[]Line{{At: at("09:00"), Sender: "张三", Text: "早"}, {At: at("10:00"), Sender: "我", Text: "x"}, {At: at("10:00"), Sender: "我", Text: "x"}},
			header + "- 09:00 张三: 早\n- 10:00 我: x\n"},
		{"same minute, other sender or text: not a duplicate", header + "- 09:00 张三: 早\n",
			[]Line{{At: at("09:00"), Sender: "李四", Text: "早"}, {At: at("09:00"), Sender: "张三", Text: "早!"}},
			header + "- 09:00 张三: 早\n- 09:00 李四: 早\n- 09:00 张三: 早!\n"},
		{"a multi-line message is one duplicate", header + "- 09:00 张三: a\n  b\n",
			[]Line{{At: at("09:00"), Sender: "张三", Text: "a\nb"}},
			header + "- 09:00 张三: a\n  b\n"},
		{"existing lines out of order stay so", header + "- 12:00 李四: 后\n- 09:00 张三: 先\n",
			[]Line{{At: at("10:00"), Sender: "我", Text: "中"}},
			header + "- 10:00 我: 中\n- 12:00 李四: 后\n- 09:00 张三: 先\n"},
		{"duplicates already there stay", header + "- 09:00 张三: 早\n- 09:00 张三: 早\n",
			[]Line{{At: at("09:00"), Sender: "张三", Text: "早"}, {At: at("13:00"), Sender: "我", Text: "新"}},
			header + "- 09:00 张三: 早\n- 09:00 张三: 早\n- 13:00 我: 新\n"},
		{"no final newline", header + "- 09:00 张三: 早",
			[]Line{{At: at("10:00"), Sender: "我", Text: "十点"}},
			header + "- 09:00 张三: 早\n- 10:00 我: 十点\n"},
		{"no final newline, inserted before", header + "- 09:00 张三: 早",
			[]Line{{At: at("08:00"), Sender: "我", Text: "八点"}},
			header + "- 08:00 我: 八点\n- 09:00 张三: 早"},
		{"new lines sorted among themselves", header,
			[]Line{{At: at("10:00"), Sender: "我", Text: "b"}, {At: at("09:00"), Sender: "我", Text: "a"}, {At: at("10:00"), Sender: "我", Text: "c"}},
			header + "- 09:00 我: a\n- 10:00 我: b\n- 10:00 我: c\n"},
		{"crlf lines kept", "# x\r\n\r\n- 09:00 张三: 早\r\n",
			[]Line{{At: at("10:00"), Sender: "我", Text: "十点"}},
			"# x\r\n\r\n- 09:00 张三: 早\r\n- 10:00 我: 十点\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, n := mergeDay(c.text, c.lines, zone)
			if got != c.want {
				t.Fatalf("got:\n%q\nwant:\n%q", got, c.want)
			}
			checkKept(t, c.text, got, len(rowsOf(got))-len(rowsOf(c.text)))
			if again, m := mergeDay(got, c.lines, zone); m != 0 || again != got {
				t.Fatalf("merging again added %d:\n%q", m, again)
			}
			if n == 0 && got != c.text {
				t.Fatal("changed the text without adding")
			}
		})
	}
}

// genDay makes a random day file: a header or not, messages with continuation lines in time order or not, blank and
// unreadable lines, duplicates, unicode, a final newline or not. It returns the text and the messages written into it.
func genDay(r *rand.Rand, zone *time.Location, day time.Time) (string, []Line) {
	var have []Line
	var rows []string
	if r.Intn(4) > 0 {
		rows = append(rows, "# 某个群 · "+day.Format("2006-01-02"), "")
	}
	sorted := r.Intn(2) == 0
	minute := r.Intn(60)
	for n := r.Intn(30); n > 0; n-- {
		switch k := r.Intn(10); {
		case k < 6:
			if sorted {
				minute += r.Intn(3)
			} else {
				minute = r.Intn(24 * 60)
			}
			l := genLine(r, day, minute%(24*60))
			have = append(have, l)
			rows = append(rows, strings.Split(strings.TrimSuffix(l.Format(zone), "\n"), "\n")...)
		case k == 6:
			rows = append(rows, "")
		case k == 7:
			rows = append(rows, oddLines[r.Intn(len(oddLines))])
		case k == 8 && len(rows) > 0:
			rows = append(rows, rows[r.Intn(len(rows))]) // a line already there, again (maybe half of a message)
		default:
			rows = append(rows, "## "+randText(r))
		}
	}
	text := strings.Join(rows, "\n")
	if len(rows) > 0 && r.Intn(5) > 0 {
		text += "\n"
	}
	return text, have
}

// oddLines are lines a day file may hold that Line.Format never writes (none starts with two spaces: those would
// join the message above, tested on their own).
var oddLines = []string{
	"- 99:99 坏: 时间不对", "- 12:3x 坏: 半个时间", "-nospace", "- ", "-", "— 09:00 破折号", "* 09:00 星号", "\t09:00 tab",
	"- 09:00", "- 09:00:", "[……此处省略 3 条]", "😀 表情开头", "e\u0301 组合字符", "\r", "x\r", "- 10:00 回车: 尾\r",
}

var senders = []string{"张三", "李四", "我", "?", "·", "Alice", "Ünïcödé", "名字: 带冒号", "😀"}

func randText(r *rand.Rand) string {
	parts := []string{"好的", "收到", "明天见", "ok", "see you", "😀👍", "a: b", "- 09:00 不是行首", "长" + strings.Repeat("字", r.Intn(50)),
		"e\u0301", "中文 English 混排", "[附件]", "https://example.com/x"}
	s := parts[r.Intn(len(parts))]
	if r.Intn(6) == 0 {
		s += "\n" + parts[r.Intn(len(parts))] // a continuation line
	}
	return s
}

func genLine(r *rand.Rand, day time.Time, minute int) Line {
	at := day.Add(time.Duration(minute)*time.Minute + time.Duration(r.Intn(60))*time.Second)
	return Line{At: at, Sender: senders[r.Intn(len(senders))], Text: randText(r)}
}

// TestMergeKeepsEveryLine is the guarantee merging into existing day files rests on: over many random files and
// batches, every line that was there is still there, unchanged, in its order, and nothing else is lost; exactly the
// new lines that were not duplicates are added; merging the same batch again adds nothing.
func TestMergeKeepsEveryLine(t *testing.T) {
	zone := mergeZone(t)
	day := time.Date(2026, 7, 15, 0, 0, 0, 0, zone)
	seed := time.Now().UnixNano()
	r := rand.New(rand.NewSource(seed))
	for iter := 0; iter < 5000; iter++ {
		text, have := genDay(r, zone, day)
		var batch []Line
		for n := r.Intn(20); n > 0; n-- {
			if r.Intn(4) == 0 && len(have) > 0 {
				// A message in the file again: same minute, sender and text, another second.
				l := have[r.Intn(len(have))]
				l.At = l.At.Truncate(time.Minute).Add(time.Duration(r.Intn(60)) * time.Second)
				batch = append(batch, l)
				continue
			}
			l := genLine(r, day, r.Intn(24*60))
			batch = append(batch, l)
			if r.Intn(5) == 0 {
				batch = append(batch, l) // twice in the batch
			}
		}
		// What should be added: the batch's distinct lines the file does not hold.
		want, rows := 0, 0
		seen := blocks(text)
		for _, l := range batch {
			f := strings.TrimSuffix(l.Format(zone), "\n")
			if !seen[dupKey(f)] {
				seen[dupKey(f)] = true
				want++
				rows += strings.Count(f, "\n") + 1
			}
		}
		got, n := mergeDay(text, batch, zone)
		if n != want {
			t.Fatalf("seed %d iter %d: added %d, want %d\nfile:\n%q\nbatch: %q\ngot:\n%q", seed, iter, n, want, text, batch, got)
		}
		checkKept(t, text, got, rows)
		if again, m := mergeDay(got, batch, zone); m != 0 || again != got {
			t.Fatalf("seed %d iter %d: merging again added %d", seed, iter, m)
		}
		if want == 0 && got != text {
			t.Fatalf("seed %d iter %d: changed a file it added nothing to", seed, iter)
		}
		// A file in time order stays in time order.
		if isSortedDay(text) && !isSortedDay(got) {
			t.Fatalf("seed %d iter %d: lost the time order\nfile:\n%q\ngot:\n%q", seed, iter, text, got)
		}
	}
}

// blocks is what a day file holds for telling duplicates: each message line with the continuation lines under it.
func blocks(text string) map[string]bool {
	out := map[string]bool{}
	rows := rowsOf(text)
	for i, r := range rows {
		if !strings.HasPrefix(r, "- ") {
			continue
		}
		b := r
		for _, c := range rows[i+1:] {
			if !strings.HasPrefix(c, "  ") {
				break
			}
			b += "\n" + c
		}
		out[dupKey(b)] = true
	}
	return out
}

func isSortedDay(text string) bool {
	last := ""
	for _, r := range rowsOf(text) {
		if c := clockOf(r); c != "" {
			if c < last {
				return false
			}
			last = c
		}
	}
	return true
}

// TestMergeMessagesFiles goes through the store: day files made or merged into, atomically, with the permissions
// AppendMessages writes; the original lines of every day kept.
func TestMergeMessagesFiles(t *testing.T) {
	zone := mergeZone(t)
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Rezone(zone); err != nil {
		t.Fatal(err)
	}
	c := &Chat{ID: "lark-g", Name: "群"}
	d1 := time.Date(2026, 7, 15, 9, 0, 0, 0, zone)
	if err := st.AppendMessages(c, []Line{{At: d1, Sender: "张三", Text: "早"}, {At: d1.Add(2 * time.Hour), Sender: "李四", Text: "午"}}); err != nil {
		t.Fatal(err)
	}
	p1 := st.Path(KindChat, c.ID, "messages", "2026-07-15.md")
	before, _ := os.ReadFile(p1)
	// A hand edit the merge must keep as it is.
	before = append(before, []byte("手写的一行\n")...)
	if err := os.WriteFile(p1, before, 0o600); err != nil {
		t.Fatal(err)
	}
	batch := []Line{
		{At: d1.Add(time.Hour), Sender: "我", Text: "十点"},
		{At: d1, Sender: "张三", Text: "早"}, // there already
		{At: d1.AddDate(0, 0, -1), Sender: "王五", Text: "前一天"},
	}
	n, err := st.MergeMessages(c, batch)
	if err != nil || n != 2 {
		t.Fatalf("merged %d %v", n, err)
	}
	after, _ := os.ReadFile(p1)
	checkKept(t, string(before), string(after), 1)
	if !strings.Contains(string(after), "- 09:00 张三: 早\n- 10:00 我: 十点\n- 11:00 李四: 午\n") {
		t.Fatalf("day:\n%s", after)
	}
	prev, err := os.ReadFile(st.Path(KindChat, c.ID, "messages", "2026-07-14.md"))
	if err != nil || string(prev) != "# 群 · 2026-07-14\n\n- 09:00 王五: 前一天\n" {
		t.Fatalf("new day: %q %v", prev, err)
	}
	for _, p := range []string{p1, st.Path(KindChat, c.ID, "messages", "2026-07-14.md")} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", p, fi.Mode(), err)
		}
	}
	if left, _ := filepath.Glob(st.Path(KindChat, c.ID, "messages", "*.tmp")); len(left) > 0 {
		t.Fatalf("temporary files left: %v", left)
	}
	if n, err := st.MergeMessages(c, batch); n != 0 || err != nil {
		t.Fatalf("again: %d %v", n, err)
	}
	if again, _ := os.ReadFile(p1); string(again) != string(after) {
		t.Fatal("merging again changed the file")
	}
}

// FuzzMergeDay: whatever a day file holds, merging keeps all of it.
func FuzzMergeDay(f *testing.F) {
	f.Add("# 群 · 2026-07-15\n\n- 09:00 张三: 早\n", "我", "十点", 600)
	f.Add("- 99:99 x\n  y\nz", "a", "b\nc", 0)
	f.Add("", "", "", 1439)
	f.Add("\n\n\r\n", "·", "[……此处省略 2 条]", 61)
	zone := mergeZone(f)
	day := time.Date(2026, 7, 15, 0, 0, 0, 0, zone)
	f.Fuzz(func(t *testing.T, text, sender, msg string, minute int) {
		if minute < 0 {
			minute = -minute
		}
		l := Line{At: day.Add(time.Duration(minute%(24*60)) * time.Minute), Sender: sender, Text: msg}
		got, n := mergeDay(text, []Line{l}, zone)
		rows := 0
		if n > 0 {
			rows = strings.Count(strings.TrimSuffix(l.Format(zone), "\n"), "\n") + 1
		}
		checkKept(t, text, got, rows)
		if again, m := mergeDay(got, []Line{l}, zone); m != 0 || again != got {
			t.Fatalf("merging again added %d", m)
		}
	})
}
