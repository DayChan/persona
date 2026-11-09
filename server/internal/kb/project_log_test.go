package kb

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// oldLogLines is how Log picked its lines before it scanned from the end: every "- " line of the whole timeline,
// then the last n of them.
func oldLogLines(raw []byte, n int) []string {
	var lines []string
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "- ") {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// oldSettleLog is settleLogLocked before it read only the header: it read the whole timeline every time.
func oldSettleLog(s *Store, slug string) (bool, error) {
	if s.readOnly {
		return false, nil
	}
	p := s.path(KindProject, slug, "log.md")
	raw, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	to := s.zoneLocked()
	text := string(raw)
	head, _, _ := strings.Cut(text, "\n")
	name := ""
	if z, ok := strings.CutPrefix(head, logTitle+" · "); ok {
		name = strings.TrimSpace(z)
	}
	if name == to.String() || (name == "" && to == time.Local) {
		return false, nil
	}
	from, err := loadZone(name)
	if err != nil {
		return false, fmt.Errorf("log.md names %q: %w", name, err)
	}
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if !strings.HasPrefix(l, "- ") {
			continue
		}
		if at, err := time.ParseInLocation("2006-01-02 15:04", safeSlice(l, 2, 18), from); err == nil {
			l = "- " + at.In(to).Format("2006-01-02 15:04") + l[18:]
		}
		lines = append(lines, l)
	}
	return true, s.writeLogLocked(slug, lines)
}

// logStore is a store in zone with a project "p" whose log.md is raw (none when raw is nil).
func logStore(t testing.TB, zone *time.Location, raw []byte) *Store {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Rezone(zone); err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		if err := writeFile(st.Path(KindProject, "p", "log.md"), raw); err != nil {
			t.Fatal(err)
		}
	}
	return st
}

func readLog(t *testing.T, st *Store) string {
	t.Helper()
	raw, err := os.ReadFile(st.Path(KindProject, "p", "log.md"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(raw)
}

var logTexts = map[string]string{
	"empty":                    "",
	"header only":              "# 时间线\n\n",
	"header without a newline": "# 时间线 · UTC",
	"one line":                 "# 时间线\n\n- 2026-11-01 09:00 · lark · a\n",
	"crlf":                     "# 时间线\r\n\r\n- 2026-11-01 09:00 · lark · a\r\n- 2026-11-01 10:00 · lark · b\r\n",
	"no trailing newline":      "# 时间线\n\n- 2026-11-01 09:00 · lark · a\n- 2026-11-01 10:00 · lark · b",
	"other lines between":      "# 时间线\n\n- a\n  continued\n\n-b\n## heading\n- \n-\n- c\n\n\n- d\n  more\n",
	"no header":                "- a\n- b\n- c",
	"blank lines only":         "\n\n\n",
	"no line":                  "# 时间线\n\nsome text\n-x\n",
}

// Log scans the timeline from its end but returns exactly what it did when it collected every line first: nil with no
// line, the same lines in the same order for any n.
func TestLogLinesMatchTheFullScan(t *testing.T) {
	var long strings.Builder
	long.WriteString("# 时间线\n\n")
	for i := range 100 {
		fmt.Fprintf(&long, "- 2026-11-01 09:00 · lark · %d\n", i)
		if i%7 == 0 {
			long.WriteString("  continued\n")
		}
	}
	texts := map[string]string{"long": long.String()}
	for k, v := range logTexts {
		texts[k] = v
	}
	for name, text := range texts {
		st := logStore(t, time.Local, []byte(text))
		for _, n := range []int{0, 1, 2, 3, 30, 1 << 30, math.MaxInt} {
			want := oldLogLines([]byte(text), n)
			if got := lastLogLines([]byte(text), n); !reflect.DeepEqual(got, want) || (got == nil) != (want == nil) {
				t.Errorf("%s, n=%d: %q (nil %v), want %q (nil %v)", name, n, got, got == nil, want, want == nil)
			}
			if got := st.Log("p", n); !reflect.DeepEqual(got, want) || (got == nil) != (want == nil) {
				t.Errorf("Log, %s, n=%d: %q, want %q", name, n, got, want)
			}
		}
	}
	if got := logStore(t, time.Local, nil).Log("p", 30); got != nil {
		t.Errorf("no file: %q", got)
	}
	// A negative n used to panic (a slice past the end); now it is n == 0.
	if got := lastLogLines([]byte(texts["long"]), -1); got == nil || len(got) != 0 {
		t.Errorf("n=-1: %q", got)
	}
	if got := lastLogLines(nil, -1); got != nil {
		t.Errorf("n=-1, no lines: %q", got)
	}
}

// settleLogLocked reads only the header now, but what it does with it is unchanged: the same result, the same error and
// a byte-identical timeline, moved or not, for every header against every zone; and AppendLog writes the same file.
func TestSettleLogMatchesTheFullRead(t *testing.T) {
	shanghai := zone(t, "Asia/Shanghai")
	body := "- 2026-10-31 17:30 · lark · a\n  continued\n- 2026-11-01 01:00 · lark · b\n- no time here\n"
	cases := []struct {
		name string
		raw  []byte // nil: no file
	}{
		{"no file", nil},
		{"empty file", []byte{}},
		{"another zone", []byte(logTitle + " · Asia/Shanghai\n\n" + body)},
		{"UTC", []byte(logTitle + " · UTC\n\n" + body)},
		{"no zone", []byte(logTitle + "\n\n" + body)},
		{"Local", []byte(logTitle + " · Local\n\n" + body)},
		{"unknown zone", []byte(logTitle + " · Nowhere/Atlantis\n\n" + body)},
		{"crlf header", []byte(logTitle + " · Asia/Shanghai\r\n\r\n" + body)},
		{"header without a newline", []byte(logTitle + " · Asia/Shanghai")},
		{"no header", []byte(body)},
	}
	moves := 0
	for _, to := range []*time.Location{time.Local, time.UTC, shanghai} {
		for _, c := range cases {
			label, raw := fmt.Sprintf("%s in %s", c.name, to), c.raw

			newSt, oldSt := logStore(t, to, raw), logStore(t, to, raw)
			newSt.mu.Lock()
			newMoved, newErr := newSt.settleLogLocked("p")
			newSt.mu.Unlock()
			oldMoved, oldErr := oldSettleLog(oldSt, "p")
			if newMoved {
				moves++
			}
			if newMoved != oldMoved || fmt.Sprint(newErr) != fmt.Sprint(oldErr) {
				t.Errorf("%s: settle %v, %v; was %v, %v", label, newMoved, newErr, oldMoved, oldErr)
			}
			if got, want := readLog(t, newSt), readLog(t, oldSt); got != want {
				t.Errorf("%s: settled\n%q\nwas\n%q", label, got, want)
			}

			at := time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
			newSt, oldSt = logStore(t, to, raw), logStore(t, to, raw)
			newErr = newSt.AppendLog("p", at, "lark", "c")
			if _, oldErr = oldSettleLog(oldSt, "p"); oldErr == nil {
				oldErr = appendFile(oldSt.path(KindProject, "p", "log.md"), logHeader(to),
					fmt.Sprintf("- %s · lark · c\n", at.In(to).Format("2006-01-02 15:04")))
			}
			if fmt.Sprint(newErr) != fmt.Sprint(oldErr) {
				t.Errorf("%s: append %v; was %v", label, newErr, oldErr)
			}
			if got, want := readLog(t, newSt), readLog(t, oldSt); got != want {
				t.Errorf("%s: appended\n%q\nwas\n%q", label, got, want)
			}
		}
	}
	if moves < 6 { // every zone moves at least the timelines naming the other two
		t.Fatalf("only %d timelines moved", moves)
	}
}

// The first line of a file, as strings.Cut gave it from the whole text.
func TestFirstLine(t *testing.T) {
	dir := t.TempDir()
	for _, text := range []string{"", "\n", "a", "a\n", "a\r\nb\n", "# 时间线 · UTC\n\n- x\n", strings.Repeat("long ", 500) + "\nrest"} {
		p := filepath.Join(dir, "f")
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		want, _, _ := strings.Cut(text, "\n")
		if got, err := firstLine(p); got != want || err != nil {
			t.Errorf("%q: %q, %v; want %q", text, got, err, want)
		}
	}
	if _, err := firstLine(filepath.Join(dir, "missing")); !os.IsNotExist(err) {
		t.Errorf("missing: %v", err)
	}
	if _, err := firstLine(dir); err == nil {
		t.Error("a directory reads as a file")
	}
}

// Appending to a long timeline reads its header, not the whole of it: a timeline only grows, and every append used to
// read it all (two copies of it allocated per line).
func TestAppendLogDoesNotReadTheWholeTimeline(t *testing.T) {
	raw := bigLog(time.UTC, benchLogLines)
	st := logStore(t, time.UTC, raw)
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	const appends = 20
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	for range appends {
		if err := st.AppendLog("p", at, "lark", "a new line"); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	// The file is over a megabyte; a header read is well under a kilobyte. The bound leaves room for whatever else
	// allocates meanwhile.
	if per := (after.TotalAlloc - before.TotalAlloc) / appends; per > uint64(len(raw)/8) {
		t.Fatalf("%d bytes allocated per append to a %d-byte timeline", per, len(raw))
	}
	got := readLog(t, st)
	if !strings.HasPrefix(got, string(raw)) || strings.Count(got[len(raw):], "- 2026-11-01 09:00 · lark · a new line\n") != appends {
		t.Fatalf("appended:\n%s", got[len(raw):])
	}
	if last := st.Log("p", 1); len(last) != 1 || last[0] != "- 2026-11-01 09:00 · lark · a new line" {
		t.Fatalf("last line %q", last)
	}
	if first := st.Log("p", benchLogLines+appends); len(first) != benchLogLines+appends ||
		!bytes.HasPrefix(raw[len(logHeader(time.UTC)):], []byte(first[0]+"\n")) {
		t.Fatalf("%d lines, first %q", len(first), first[0])
	}
}
