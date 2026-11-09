package kb

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// messagesRef is messages as it was before continuation lines were joined once (verbatim): the reference the
// linear version must match byte for byte.
func messagesRef(raw string) []message {
	var out []message
	for i, l := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(l, "- "):
			out = append(out, message{n: i + 1, text: strings.TrimPrefix(l, "- ")})
		case strings.HasPrefix(l, "  ") && len(out) > 0:
			out[len(out)-1].text += "\n" + strings.TrimSpace(l)
		}
	}
	return out
}

// sinceRef is Store.Since as it was before (verbatim), for the same comparison.
func (s *Store) sinceRef(chatID string, t time.Time, max int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.settleChatLocked(chatID)
	zone := s.chatReadZoneLocked(chatID)
	from := t.In(zone).Format("2006-01-02")
	var b strings.Builder
	for _, d := range s.Days(chatID) {
		if d < from {
			continue
		}
		raw, _ := os.ReadFile(s.path(KindChat, chatID, "messages", d+".md"))
		var keep []string
		cur := ""
		flush := func() {
			if cur != "" {
				keep = append(keep, cur)
			}
			cur = ""
		}
		for _, l := range strings.Split(string(raw), "\n") {
			switch {
			case strings.HasPrefix(l, "- "):
				flush()
				if len(l) >= 7 {
					if at, err := time.ParseInLocation("2006-01-02 15:04", d+" "+l[2:7], zone); err == nil && at.Before(t.Truncate(time.Minute)) {
						continue
					}
				}
				cur = l
			case strings.HasPrefix(l, "  ") && cur != "":
				cur += "\n" + l
			default:
				flush()
			}
		}
		flush()
		if len(keep) > 0 {
			fmt.Fprintf(&b, "## %s\n%s\n", d, strings.Join(keep, "\n"))
		}
	}
	out := b.String()
	if len(out) > max {
		out = "…\n" + out[len(out)-max:]
	}
	return out
}

// longDay is a day file with one message of n continuation lines (a pasted log) between two short ones.
func longDay(n int) string {
	var b strings.Builder
	b.WriteString("# Build chat · 2026-10-20\n\n- 09:00 Alice: before the paste\n- 09:30 Bob: the log:\n")
	for i := range n {
		fmt.Fprintf(&b, "  2026-10-20T09:29:%02dZ INFO worker step %05d finished in %d ms\n", i%60, i, i%97)
	}
	b.WriteString("- 10:00 Alice: after the paste\n")
	return b.String()
}

var continuationFixtures = map[string]string{
	"empty":    "",
	"newline":  "\n",
	"plain":    "# Chat · 2026-10-20\n\n- 09:00 Alice: hello\n  second line\n- 09:01 Bob: hi\n",
	"crlf":     "# Chat · 2026-10-20\r\n\r\n- 09:00 Alice: one\r\n  two\r\n  three\r\n- 09:01 Bob: four\r\n",
	"blank":    "- 09:00 Alice: one\n  two\n\n  three\n\n\n- 09:01 Bob: four\n  five\n",
	"orphans":  "  orphan\n  another\n- 09:00 Alice: one\n  two\n",
	"headers":  "# Chat · 2026-10-20\n\n- 09:00 Alice: one\n# a heading in the middle\n  after the heading\n- 09:01 Bob: two\n# end\n",
	"no-eol":   "- 09:00 Alice: one\n  two\n  three",
	"filtered": "- 08:59 Alice: early\n  dropped with it\n- 09:30 Bob: kept\n  kept too\n- 09:29 Carol: out of order\n  also dropped\n- 10:00 Dave: late\n",
	"odd":      "-\n- \n-  \n- 9:0 x\n  \n   \n\t tab\n - not a message\n  - indented dash\n- 09:00 a:\n  ",
	"long":     longDay(5000),
}

func TestMessagesJoinContinuationsAsBefore(t *testing.T) {
	for name, raw := range continuationFixtures {
		if got, want := messages(raw), messagesRef(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %d messages, want %d\ngot  %+v\nwant %+v", name, len(got), len(want), got, want)
		}
	}
	// A blank line inside a message does not end it; the next "- " line does.
	want := []message{{n: 1, text: "09:00 Alice: one\ntwo\nthree"}, {n: 7, text: "09:01 Bob: four\nfive"}}
	if got := messages(continuationFixtures["blank"]); !reflect.DeepEqual(got, want) {
		t.Fatalf("blank: %+v", got)
	}
	if got := messages(continuationFixtures["long"]); len(got) != 3 || strings.Count(got[1].text, "\n") != 5000 {
		t.Fatalf("long: %d messages", len(got))
	}
}

// writeFixtureDays puts each fixture in its own chat as the day 2026-10-20, and the short ones in one chat on
// consecutive days. Returns the fixture chats by fixture name and the combined chat's id.
func writeFixtureDays(t *testing.T, st *Store) (map[string]string, string) {
	t.Helper()
	write := func(chat, day, raw string) {
		dir := st.path(KindChat, chat, "messages")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, day+".md"), []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	chats := map[string]string{}
	start := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	for i, name := range slices.Sorted(maps.Keys(continuationFixtures)) {
		raw := continuationFixtures[name]
		chats[name] = "lark-fx-" + name
		write(chats[name], "2026-10-20", raw)
		if name != "long" {
			write("lark-fx-all", start.AddDate(0, 0, i).Format("2006-01-02"), raw)
		}
	}
	return chats, "lark-fx-all"
}

// sameText reports where two long texts first differ, without printing them whole.
func sameText(t *testing.T, what, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	t.Errorf("%s: %d bytes, want %d; from byte %d got %q, want %q", what, len(got), len(want), i,
		got[i:min(len(got), i+80)], want[i:min(len(want), i+80)])
}

func TestDayMessagesAsBefore(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st}
	chats, _ := writeFixtureDays(t, st)
	for name, chat := range chats {
		got, _, _, err := e.Day(chat, "2026-10-20")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var want []DayMessage
		for _, m := range messagesRef(continuationFixtures[name]) {
			want = append(want, DayMessage{N: m.n, Text: m.text})
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\ngot  %+v\nwant %+v", name, got, want)
		}
	}
}

func TestSinceAsBefore(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	chats, all := writeFixtureDays(t, st)
	zone := st.Zone()
	at := func(d, hm string) time.Time {
		x, err := time.ParseInLocation("2006-01-02 15:04:05", d+" "+hm, zone)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	froms := []time.Time{{}, at("2026-10-20", "09:00:00"), at("2026-10-20", "09:01:00"), at("2026-10-20", "09:30:45"),
		at("2026-10-20", "10:00:00"), at("2026-10-21", "00:00:00"), at("2026-10-25", "09:30:00"), at("2027-01-01", "00:00:00")}
	maxes := []int{1 << 20, 24000, 100, 7, 0}
	for _, id := range append([]string{all}, slices.Sorted(maps.Values(chats))...) {
		if id == chats["long"] {
			continue // the reference is quadratic in it: checked once below
		}
		for _, from := range froms {
			for _, max := range maxes {
				sameText(t, fmt.Sprintf("%s since %v max %d", id, from, max), st.Since(id, from, max), st.sinceRef(id, from, max))
			}
		}
	}
	from := at("2026-10-20", "09:15:00")
	sameText(t, "long", st.Since(chats["long"], from, 1<<20), st.sinceRef(chats["long"], from, 1<<20))
	sameText(t, "long cut", st.Since(chats["long"], time.Time{}, 24000), st.sinceRef(chats["long"], time.Time{}, 24000))
	// A message before the instant goes with its continuation lines; a blank line or a heading ends a kept one.
	if got := st.Since(chats["filtered"], at("2026-10-20", "09:30:45"), 1<<20); got != "## 2026-10-20\n- 09:30 Bob: kept\n  kept too\n- 10:00 Dave: late\n" {
		t.Fatalf("filtered: %q", got)
	}
	if got := st.Since(chats["blank"], time.Time{}, 1<<20); got != "## 2026-10-20\n- 09:00 Alice: one\n  two\n- 09:01 Bob: four\n  five\n" {
		t.Fatalf("blank: %q", got)
	}
}

func FuzzMessages(f *testing.F) {
	for name, raw := range continuationFixtures {
		if name != "long" {
			f.Add(raw)
		}
	}
	f.Add(longDay(20))
	f.Fuzz(func(t *testing.T, raw string) {
		if got, want := messages(raw), messagesRef(raw); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q:\ngot  %+v\nwant %+v", raw, got, want)
		}
	})
}

// One message with 4,000 continuation lines, as a pasted log or stack trace is archived.
func BenchmarkDayMessagesLong(b *testing.B) {
	raw := longDay(4000)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if ms := messages(raw); len(ms) != 3 {
			b.Fatalf("%d messages", len(ms))
		}
	}
}

func BenchmarkSinceLong(b *testing.B) {
	st, err := NewStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	dir := st.path(KindChat, "lark-long", "messages")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		b.Fatal(err)
	}
	raw := longDay(4000)
	if err := os.WriteFile(filepath.Join(dir, "2026-10-20.md"), []byte(raw), 0o600); err != nil {
		b.Fatal(err)
	}
	from := time.Date(2026, 10, 20, 9, 15, 0, 0, st.Zone())
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if out := st.Since("lark-long", from, 1<<20); !strings.HasSuffix(out, "after the paste\n") {
			b.Fatalf("since: %d bytes", len(out))
		}
	}
}
