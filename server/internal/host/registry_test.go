package host

import (
	"context"
	"errors"
	"testing"
)

// stub is a Host of some kind that does nothing.
type stub struct{ kind string }

func (s stub) Kind() string                                          { return s.kind }
func (stub) Version(context.Context) string                          { return "" }
func (stub) Snapshot(context.Context) (*Layout, error)               { return &Layout{}, nil }
func (stub) Agents(context.Context) ([]Agent, error)                 { return nil, nil }
func (stub) Agent(context.Context, PaneRef) (*Agent, error)          { return &Agent{}, nil }
func (stub) Read(context.Context, PaneRef, ReadOpts) (string, error) { return "", nil }
func (stub) SendText(context.Context, PaneRef, string) error         { return nil }
func (stub) SendKeys(context.Context, PaneRef, ...string) error      { return nil }
func (stub) Prompt(context.Context, PaneRef, string) error           { return nil }
func (stub) PaneProcesses(context.Context, PaneRef) ([]int, error)   { return nil, nil }
func (stub) PaneEnv() string                                         { return "" }

func TestPaneRefStoredForm(t *testing.T) {
	r := NewRegistry(stub{Herdr}, stub{"orca"})
	for _, c := range []struct {
		stored string
		want   PaneRef
	}{
		{"w1:p2", PaneRef{Herdr, "w1:p2"}}, // herdr ids keep their colon and no prefix
		{"p7", PaneRef{Herdr, "p7"}},
		{"", PaneRef{Herdr, ""}},
		{"herdr:w1:p2", PaneRef{Herdr, "herdr:w1:p2"}}, // herdr is never a prefix
		{"orca:tab1:leaf2", PaneRef{"orca", "tab1:leaf2"}},
		{"tmux:%3", PaneRef{Tmux, "%3"}},               // a tmux pane (#2562)
		{"nonesuch:%3", PaneRef{Herdr, "nonesuch:%3"}}, // not a known host kind
		{"orca:a:b", PaneRef{Orca, "a:b"}},
		// An Orca pane key without the prefix is not an Orca pane: unprefixed means herdr.
		{"0b0e2c1a-0000-4000-8000-000000000001:0b0e2c1a-0000-4000-8000-000000000002",
			PaneRef{Herdr, "0b0e2c1a-0000-4000-8000-000000000001:0b0e2c1a-0000-4000-8000-000000000002"}},
	} {
		got := r.Ref(c.stored)
		if got != c.want {
			t.Errorf("Ref(%q) = %+v, want %+v", c.stored, got, c.want)
		}
		if got.String() != c.stored {
			t.Errorf("Ref(%q).String() = %q", c.stored, got.String())
		}
	}
	if h, ref := r.Pane("orca:x"); h == nil || h.Kind() != "orca" || ref.ID != "x" {
		t.Errorf("Pane(orca:x) = %v, %+v", h, ref)
	}
	if h, _ := NewRegistry(stub{"orca"}).Pane("w1:p1"); h != nil {
		t.Errorf("a herdr pane without a herdr host has no host, got %v", h)
	}
	// Parsing does not depend on what is registered: an Orca pane on a machine without Orca stays an Orca pane (no
	// host), never a herdr pane.
	herdrOnly := NewRegistry(stub{Herdr})
	if h, ref := herdrOnly.Pane("orca:a:b"); h != nil || ref != (PaneRef{Orca, "a:b"}) || ref.String() != "orca:a:b" {
		t.Errorf("Pane(orca:a:b) without orca = %v, %+v", h, ref)
	}
	if ParsePane("w79:p2") != (PaneRef{Herdr, "w79:p2"}) {
		t.Error("herdr ids stay herdr")
	}
}

func TestErrUnsupported(t *testing.T) {
	err := Mark(errors.New("unsupported: orca cannot do this"), ErrUnsupported)
	if !errors.Is(err, ErrUnsupported) || ErrUnsupported.Error() != "unsupported" {
		t.Fatalf("ErrUnsupported = %v", err)
	}
}

func TestRegistryKeepsFirstOfAKind(t *testing.T) {
	a, b := stub{Herdr}, stub{Herdr}
	r := NewRegistry(a, nil, b, stub{"orca"})
	if len(r.All()) != 2 || r.All()[0].Kind() != Herdr || r.All()[1].Kind() != "orca" {
		t.Fatalf("All = %v", r.All())
	}
	var nilReg *Registry
	if nilReg.Get(Herdr) != nil || nilReg.All() != nil {
		t.Fatal("a nil registry has no hosts")
	}
}

func TestMark(t *testing.T) {
	base := errors.New("agent_not_found: no agent")
	err := Mark(base, ErrNoAgent)
	if err.Error() != base.Error() || !errors.Is(err, ErrNoAgent) || !errors.Is(err, base) {
		t.Fatalf("Mark = %v", err)
	}
	if Mark(nil, ErrNoAgent) != nil {
		t.Fatal("Mark(nil) must stay nil")
	}
}

func TestParsePaneHerdrCore(t *testing.T) {
	ref := ParsePane("herdr-core:t1b")
	if ref != (PaneRef{HerdrCore, "t1b"}) || ref.String() != "herdr-core:t1b" {
		t.Errorf("ParsePane(herdr-core:t1b) = %+v (%s)", ref, ref.String())
	}
	if h, ref := NewRegistry(stub{Herdr}).Pane("herdr-core:t7"); h != nil || ref.Kind != HerdrCore {
		t.Errorf("a herdr-core pane without that host went to %v, %+v", h, ref)
	}
}
