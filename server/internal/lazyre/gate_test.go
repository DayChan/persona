package lazyre

import (
	"regexp"
	"strings"
	"testing"
)

// A gated pattern matches and replaces exactly as regexp does; the gate only skips texts that cannot match. Patterns
// some match of which may start with anything have no gate.
func TestGatedMatchesLikeRegexp(t *testing.T) {
	texts := []string{"", "should I go", "you should i", "shoulder i", "x 你来定 y", "你来", "no decisions for you",
		"no decision", "a `b?` c", "see https://e.x/?q", "ab", "xaab", "ABC", "cat", "bat", "x ```go\nf()\n``` y",
		"\xff你好", "你", "zzq", "xyq", "q"}
	for _, c := range []struct {
		src   string
		gated bool
	}{
		{`\bshould i\b|你(?:来定|定一下)|no (?:questions|decisions?) for you`, true},
		{"`[^`\n]*`|https?://\\S+", true},
		{`(c|b)at`, true},
		{`[cb]at`, true},
		{`a*b`, false},
		{`(?i)abc`, false},
		{`.at|ab`, false},
		{"(?s)```.*?```", true},
		{`q|bc|你好|de|fg|hi|jk|lm|no|pr`, true}, // more than fewLits: looked for by their first two bytes
	} {
		r, want := NewGated(c.src), regexp.MustCompile(c.src)
		if r.Gated() != c.gated {
			t.Errorf("%s: gated = %v", c.src, r.Gated())
		}
		for _, s := range texts {
			if r.MatchString(s) != want.MatchString(s) {
				t.Errorf("%s: MatchString(%q) = %v", c.src, s, r.MatchString(s))
			}
			if got := r.ReplaceAllStringFunc(s, strings.ToUpper); got != want.ReplaceAllStringFunc(s, strings.ToUpper) {
				t.Errorf("%s: ReplaceAllStringFunc(%q) = %q", c.src, s, got)
			}
			if got := r.ReplaceAllString(s, "<$0>"); got != want.ReplaceAllString(s, "<$0>") {
				t.Errorf("%s: ReplaceAllString(%q) = %q", c.src, s, got)
			}
			if got := r.ReplaceAllLiteralString(s, "$0"); got != want.ReplaceAllLiteralString(s, "$0") {
				t.Errorf("%s: ReplaceAllLiteralString(%q) = %q", c.src, s, got)
			}
		}
	}
}

// MayMatchLowered reads an ASCII text as strings.ToLower has it, without lowering it: false exactly when the lowered
// text fails the gate, and never false for a lowered text that matches. A text with other runes is left to the match.
func TestMayMatchLoweredIsTheGateOnTheLoweredText(t *testing.T) {
	texts := []string{"", "Should I go", "YOU SHOULD I", "No Decisions For You", "xAAB", "ABC", "CAT", "Bat", "Q", "ZZQ",
		"a `B?` c", "See HTTPS://e.x/?q", "Example result.", "İ", "Ünïcode NO", "你来定 OK", "\xffNO"}
	for _, src := range []string{`\bshould i\b|你(?:来定|定一下)|no (?:questions|decisions?) for you`, "`[^`\n]*`|https?://\\S+",
		`(c|b)at`, `q|bc|你好|de|fg|hi|jk|lm|no|pr`, `Abc|xy`, `a*b`} {
		r, want := NewGated(src), regexp.MustCompile(src)
		for _, s := range texts {
			lowered := strings.ToLower(s)
			may := r.MayMatchLowered(s)
			if !may && want.MatchString(lowered) {
				t.Errorf("%s: MayMatchLowered(%q) = false, but the lowered text matches", src, s)
			}
			ascii := strings.IndexFunc(s, func(c rune) bool { return c >= 0x80 }) < 0
			if gated := r.gate != nil; ascii && gated && may != r.gate.open(lowered) || (!ascii || !gated) && !may {
				t.Errorf("%s: MayMatchLowered(%q) = %v", src, s, may)
			}
		}
	}
}
