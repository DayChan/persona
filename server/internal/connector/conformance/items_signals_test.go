package conformance

import (
	"strings"
	"testing"
)

// An item's signals are optional; one it reports is one the knowledge base's selection knows, and only once.
func TestCheckItemsHoldsSignals(t *testing.T) {
	for _, c := range []struct {
		name, raw, want string
	}{
		{"no signals", `{"items":[{"kind":"docs","id":"d1","title":"Example doc"}]}`, ""},
		{"every known signal", `{"items":[{"kind":"docs","id":"d1","title":"Example doc",` +
			`"signals":["mentions","comment_mentions","authored","viewed"]}]}`, ""},
		{"an unknown signal", `{"items":[{"kind":"docs","id":"d1","title":"Example doc","signals":["shared"]}]}`,
			`signal "shared" is none of`},
		{"a signal twice", `{"items":[{"kind":"docs","id":"d1","title":"Example doc","signals":["viewed","viewed"]}]}`,
			`signal "viewed" twice`},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := checkItems([]byte(c.raw))
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("want an error with %q, got %v", c.want, err)
			}
		})
	}
}
