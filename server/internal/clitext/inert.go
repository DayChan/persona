package clitext

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Inert is untrusted text (a hub name any host on the LAN can advertise over mDNS) as it may be printed on a terminal:
// an ESC becomes <ESC> and every other control character (Unicode Cc: C0, DEL, C1, newline, carriage return and tab
// included) becomes <U+XXXX>, so none of them starts an escape sequence, rings the bell or begins a new line; bytes
// that are not UTF-8 become U+FFFD. Everything else, CJK and emoji included, is kept as it is.
func Inert(s string) string {
	if utf8.ValidString(s) && !strings.ContainsFunc(s, isControl) {
		return s
	}
	var b strings.Builder
	for _, r := range s { // an invalid byte comes out as utf8.RuneError (U+FFFD)
		switch {
		case r == '\x1b':
			b.WriteString("<ESC>")
		case isControl(r):
			fmt.Fprintf(&b, "<U+%04X>", r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isControl(r rune) bool { return unicode.Is(unicode.Cc, r) }
