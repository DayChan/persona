package connector

import (
	"context"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connectorwire"
)

// historyFake archives and can import history; caps is what it declares.
type historyFake struct{ caps []Capability }

func (f historyFake) ID() string         { return "example" }
func (f historyFake) Name() string       { return "Example" }
func (f historyFake) Manifest() Manifest { return Manifest{Capabilities: f.caps} }
func (historyFake) Changes(context.Context, string, int) ([]ChatBatch, string, bool, error) {
	return nil, "", false, nil
}
func (historyFake) HistoryChats(context.Context) ([]connectorwire.HistoryChat, error) {
	return nil, nil
}
func (historyFake) History(context.Context, connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	return connectorwire.HistoryPage{}, nil
}

// anchorsFake can also search its history.
type anchorsFake struct{ historyFake }

func (anchorsFake) HistoryAnchors(context.Context, connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error) {
	return connectorwire.HistoryAnchors{}, nil
}

// archiveOnly archives and nothing more.
type archiveOnly struct{ caps []Capability }

func (f archiveOnly) ID() string         { return "example" }
func (f archiveOnly) Name() string       { return "Example" }
func (f archiveOnly) Manifest() Manifest { return Manifest{Capabilities: f.caps} }
func (archiveOnly) Changes(context.Context, string, int) ([]ChatBatch, string, bool, error) {
	return nil, "", false, nil
}

// Check holds history to the rules of the other capabilities (declared is implemented and the other way round), and to
// its own: history needs an archive, history_anchors needs history.
func TestCheckHistory(t *testing.T) {
	for _, c := range []struct {
		name string
		c    Connector
		want string // "" passes
	}{
		{"history declared and implemented", historyFake{[]Capability{CapArchive, CapHistory}}, ""},
		{"anchors declared and implemented", anchorsFake{historyFake{[]Capability{CapArchive, CapHistory, CapHistoryAnchors}}}, ""},
		{"history declared, not implemented", archiveOnly{[]Capability{CapArchive, CapHistory}}, `declares "history" but is no History`},
		{"anchors declared, not implemented", historyFake{[]Capability{CapArchive, CapHistory, CapHistoryAnchors}},
			`declares "history_anchors" but is no HistoryAnchors`},
		{"history implemented, not declared", historyFake{[]Capability{CapArchive}}, `is a History but does not declare "history"`},
		{"anchors implemented, not declared", anchorsFake{historyFake{[]Capability{CapArchive, CapHistory}}},
			`is a HistoryAnchors but does not declare "history_anchors"`},
		{"history without an archive", historyFake{[]Capability{CapHistory}}, "declares history without an archive"},
		{"anchors without history", anchorsFake{historyFake{[]Capability{CapArchive, CapHistoryAnchors}}}, "declares history_anchors without history"},
	} {
		err := Check(c.c)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want %q", c.name, err, c.want)
		}
	}
}

// The hub finds history only through a declaration, and lists it.
func TestHistoryIsFoundByItsDeclaration(t *testing.T) {
	declared := anchorsFake{historyFake{[]Capability{CapArchive, CapHistory, CapHistoryAnchors}}}
	if _, ok := As[History](declared); !ok {
		t.Fatal("declared history not found")
	}
	if _, ok := As[HistoryAnchors](declared); !ok {
		t.Fatal("declared history_anchors not found")
	}
	if got := strings.Join(Capabilities(declared), ","); got != "archive,history,history_anchors" {
		t.Fatalf("listed %s", got)
	}
	if _, ok := As[History](anchorsFake{historyFake{[]Capability{CapArchive}}}); ok {
		t.Fatal("history found without its declaration")
	}
}
