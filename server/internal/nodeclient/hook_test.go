package nodeclient

import (
	"regexp"
	"strings"
	"testing"
)

// sessionIDPattern is the regexp ValidSessionID replaced; its answers are the reference.
const sessionIDPattern = `^[0-9a-fA-F-]{8,64}$`

// ValidSessionID answers as the regexp did, around both length limits and for what the class leaves out: other
// letters, white space, a trailing newline, multibyte and invalid UTF-8.
func TestValidSessionIDMatchesTheRegexp(t *testing.T) {
	re := regexp.MustCompile(sessionIDPattern)
	for _, id := range []string{
		"", "0123456", "01234567", "0d000000-0000-4000-8000-000000000001", "ABCDEFab-cdef-0000", "--------",
		strings.Repeat("a", 64), strings.Repeat("a", 65), "0123456g", "01234567\n", " 01234567", "0123 4567",
		"c0de-0123456789", "0123456\u00e9", "01234567\xff", "agent-0123456789", "0d000000-0000-4000-8000-00000000000Z",
	} {
		if got, want := ValidSessionID(id), re.MatchString(id); got != want {
			t.Errorf("ValidSessionID(%q) = %v, the regexp says %v", id, got, want)
		}
	}
}

func FuzzValidSessionID(f *testing.F) {
	re := regexp.MustCompile(sessionIDPattern)
	for _, seed := range []string{"", "01234567", "0d000000-0000-4000-8000-000000000001", strings.Repeat("F", 64), "0123456é"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, id string) {
		if got, want := ValidSessionID(id), re.MatchString(id); got != want {
			t.Fatalf("ValidSessionID(%q) = %v, the regexp says %v", id, got, want)
		}
	})
}
