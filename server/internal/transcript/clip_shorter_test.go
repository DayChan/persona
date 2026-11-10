package transcript

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// The saved prefix cancels only if the UTF-8 cut also preserves JSON escape boundaries. Compare the optimized
// decision against marshalling both complete strings, including every byte position in multibyte characters.
func TestClipIfShorterMatchesFullJSON(t *testing.T) {
	check := func(s string, n int) {
		t.Helper()
		want := s
		c := clipTo(s, n)
		if jsonLen(c) < jsonLen(s) {
			want = c
		}
		if got := clipIfShorter(s, n); got != want {
			t.Fatalf("length %d, share %d: got %q, want %q (input %q)", len(s), n, got, want, s)
		}
	}

	// A backslash next to a quote, control character or literal escape sequence must remain independently encoded.
	for _, token := range []string{"x", `\"`, `\u003c`, "\x00\b\f\n\r\t\x1f", "<>&", "é", "界", "🙂", "\u2028\u2029"} {
		s := strings.Repeat(token, 60)
		for n := 0; n <= len(s)+1; n++ {
			check(s, n)
		}
	}

	rng := rand.New(rand.NewPCG(2907, 4))
	alphabet := []rune("abc\\\"\x00\b\f\n\r\t\x1f<>&é界🙂\u2028\u2029")
	for i := 0; i < 10_000; i++ {
		var s strings.Builder
		for range rng.IntN(300) {
			s.WriteRune(alphabet[rng.IntN(len(alphabet))])
		}
		value := s.String()
		// Concentrate on the comparison band, also exercising the unchanged and unconditional-cut paths.
		check(value, max(0, len(value)+1-rng.IntN(clipNoteRoom+5)))
		check(value, rng.IntN(len(value)+2))
	}
}

func TestClipIfShorterKeepsEqualJSONLength(t *testing.T) {
	// With a three-digit byte count, the note costs 37 JSON bytes (excluding its surrounding quotes).
	s := strings.Repeat("x", minInputShare+37)
	if c := clipTo(s, minInputShare); c == s || jsonLen(c) != jsonLen(s) {
		t.Fatalf("fixture must tie after clipping: original %d JSON bytes, clipped %d", jsonLen(s), jsonLen(c))
	}
	for _, extra := range []int{36, 37, 38} {
		s := strings.Repeat("x", minInputShare+extra)
		got := clipIfShorter(s, minInputShare)
		if extra <= 37 && got != s {
			t.Errorf("a %d-byte tail must stay whole", extra)
		}
		if extra > 37 && got != clipTo(s, minInputShare) {
			t.Errorf("a %d-byte tail must be clipped", extra)
		}
	}
}
