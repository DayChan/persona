package clitext

import "testing"

func TestInert(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "Office hub", "Office hub"},
		{"cjk and emoji", "家里的 hub 🏠「客厅」", "家里的 hub 🏠「客厅」"},
		{"empty", "", ""},
		{"esc csi", "a\x1b[2Jb", "a<ESC>[2Jb"},
		{"osc 52 with bel", "Home\x1b]52;c;aGk=\x07", "Home<ESC>]52;c;aGk=<U+0007>"},
		{"cr lf tab", "a\r\nb\tc", "a<U+000D><U+000A>b<U+0009>c"},
		{"nul and del", "a\x00b\x7f", "a<U+0000>b<U+007F>"},
		{"c1 csi and nel", "a\u009b2Jb\u0085", "a<U+009B>2Jb<U+0085>"},
		{"invalid utf-8", "a\x9b[2J\xffb", "a\uFFFD[2J\uFFFDb"},
	} {
		if got := Inert(tc.in); got != tc.want {
			t.Errorf("%s: Inert(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
}
