package sessionorigin

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestClientPaneSourceCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/pane_sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Want string
		Session    proto.Session
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			if got := Of(c.Session); got != c.Want {
				t.Fatalf("origin=%q, want %q", got, c.Want)
			}
		})
	}
}
