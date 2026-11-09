package kb

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// clipReference is clip before its no-copy fast path, kept verbatim: the new clip must return exactly what it did.
func clipReference(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func TestClipMatchesReference(t *testing.T) {
	const n = 5
	inputs := map[string]string{
		"empty":                "",
		"ascii n-1":            "abcd",
		"ascii n":              "abcde",
		"ascii n+1":            "abcdef",
		"cjk n-1 runes":        "进度同步",
		"cjk n runes":          "进度同步了",
		"cjk n+1 runes":        "进度同步一下",
		"cjk bytes over n":     "周报", // 6 bytes, 2 runes
		"emoji n runes":        "😀😀😀😀😀",
		"emoji n+1 runes":      "😀😀😀😀😀😀",
		"mixed n+1 runes":      "ok 周报!",
		"combining mark":       "cafe\u0301s", // 6 runes: the mark counts as its own rune
		"invalid n-1 bytes":    "ab\xffd",
		"invalid n bytes":      "a\xff\xfecd",
		"invalid n+1 bytes":    "ab\xffcde",
		"invalid cut inside":   "a\xff\xfe\xfd\xfc\xfbz",
		"invalid after cut":    "abcde\xff\xfe",
		"literal U+FFFD":       "ab\uFFFDcdef",
		"truncated utf8":       "进度同\xe6\xad", // a cut-off rune is one U+FFFD per byte
		"truncated utf8 short": "进\xe6\xad",
		"lone continuation":    "\x80\x80\x80\x80\x80\x80",
		"newlines":             "a\nb\nc\nd",
	}
	for name, s := range inputs {
		for _, m := range []int{0, 1, n - 1, n, n + 1, len(s), utf8.RuneCountInString(s)} {
			if got, want := clip(s, m), clipReference(s, m); got != want {
				t.Errorf("%s: clip(%q, %d) = %q, want %q", name, s, m, got, want)
			}
		}
	}
}

// A line that is short enough comes back as it is and costs no allocation; a valid long one costs only the result.
func TestClipAllocations(t *testing.T) {
	short := strings.Repeat("今天 build ok ", 5)
	long := strings.Repeat(short, 10)
	invalid := "ab\xffcd"
	for _, c := range []struct {
		name  string
		s     string
		limit float64
	}{{"short", short, 0}, {"short invalid", invalid, 0}, {"long", long, 1}} {
		if got, want := clip(c.s, 300), clipReference(c.s, 300); got != want {
			t.Fatalf("%s: clip = %q, want %q", c.name, got, want)
		}
		if a := testing.AllocsPerRun(100, func() { clipSink = clip(c.s, 300) }); a > c.limit {
			t.Errorf("%s: clip allocates %v times, want at most %v", c.name, a, c.limit)
		}
	}
}

func FuzzClip(f *testing.F) {
	f.Add("", 0)
	f.Add("abcdef", 5)
	f.Add("进度同步一下", 5)
	f.Add("😀😀😀😀😀😀", 5)
	f.Add("ab\xffcde", 5)
	f.Add("进度同\xe6\xad", 3)
	f.Fuzz(func(t *testing.T, s string, n int) {
		n = int(uint(n) % uint(2*(len(s)+1))) // n >= 0 and near the string's length, where the cut happens
		if got, want := clip(s, n), clipReference(s, n); got != want {
			t.Fatalf("clip(%q, %d) = %q, want %q", s, n, got, want)
		}
	})
}

var clipSink string

func BenchmarkClip(b *testing.B) {
	line := "- 09:07 同事1: 今天的进度同步一下 build ok, next step review #13\n" // a bench archive line: 76 bytes, 54 runes
	for _, c := range []struct {
		name string
		s    string
		n    int
	}{
		{"short", line, 300},                                    // a Find or Search hit
		{"long", strings.Repeat(line, 8), 300},                  // 432 runes, cut at 300
		{"transcript", strings.Repeat(line, 1000), 12000},       // a recording's transcript in kb_get (recent_chars 12000)
		{"long invalid", "\xff" + strings.Repeat(line, 8), 300}, // the rare slow path
	} {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				clipSink = clip(c.s, c.n)
			}
		})
	}
}
