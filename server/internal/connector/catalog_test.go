package connector

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type catalogFake struct {
	sources []CatalogSource
	calls   int
	query   CatalogQuery
	err     error
	caps    []Capability
}

func (*catalogFake) ID() string                                         { return "example" }
func (*catalogFake) Name() string                                       { return "Example" }
func (f *catalogFake) Manifest() Manifest                               { return Manifest{Capabilities: f.caps} }
func (f *catalogFake) Catalog(context.Context) ([]CatalogSource, error) { return f.sources, nil }
func (f *catalogFake) CatalogCall(_ context.Context, q CatalogQuery) (string, error) {
	f.calls++
	f.query = q
	return "read", f.err
}
func validCatalogFake() *catalogFake {
	return &catalogFake{caps: []Capability{CapCatalog}, sources: []CatalogSource{{ID: "notices", Name: "Notices", Tools: []CatalogTool{{Name: "list", Description: "List notices", ReadOnly: true, InputSchema: json.RawMessage(`{"type":"object"}`)}}}}}
}

func TestCatalogDeclarationAndDispatch(t *testing.T) {
	ctx := context.Background()
	f := validCatalogFake()
	if err := Check(f); err != nil {
		t.Fatal(err)
	}
	if _, ok := As[Catalog](f); !ok {
		t.Fatal("catalog not found")
	}
	if text, err := CallCatalog(ctx, f, CatalogQuery{Source: "notices", Tool: "list"}); err != nil || text != "read" || f.calls != 1 || string(f.query.Args) != "{}" {
		t.Fatalf("%q %v %+v", text, err, f)
	}
	for _, q := range []CatalogQuery{{Source: "missing", Tool: "list"}, {Source: "notices", Tool: "write"}, {Source: "notices", Tool: "list", Args: json.RawMessage(`[]`)}} {
		if _, err := CallCatalog(ctx, f, q); err == nil {
			t.Fatal("invalid call accepted")
		}
	}
	if f.calls != 1 {
		t.Fatal("an invalid call reached the connector")
	}
	f.sources[0].Tools[0].ReadOnly = false
	var re *rpc.Error
	if _, err := CallCatalog(ctx, f, CatalogQuery{Source: "notices", Tool: "list"}); !errors.As(err, &re) || re.Code != "not_read_only" || f.calls != 1 {
		t.Fatalf("unsafe dispatch: %v, %d calls", err, f.calls)
	}
	f.caps = nil
	if _, err := CallCatalog(ctx, f, CatalogQuery{Source: "notices", Tool: "list"}); !errors.As(err, &re) || re.Code != "not_read_only" {
		t.Fatalf("undeclared catalog used: %v", err)
	}
}

func TestCatalogPreservesUpstreamRateLimit(t *testing.T) {
	f := validCatalogFake()
	want := &rpc.Error{Code: "rate_limited", Message: "Wait", Data: json.RawMessage(`{"retry_after":12.5}`)}
	f.err = want
	if _, err := CallCatalog(context.Background(), f, CatalogQuery{Source: "notices", Tool: "list"}); err != want {
		t.Fatalf("changed error: %v", err)
	}
}

type catalogLegacy struct {
	reads, writes int
	overlap       bool
}

func (*catalogLegacy) ID() string   { return "example" }
func (*catalogLegacy) Name() string { return "Example" }
func (*catalogLegacy) Manifest() Manifest {
	return Manifest{Capabilities: []Capability{CapTools, CapActions}}
}
func (*catalogLegacy) Tools() []proto.Tool {
	return []proto.Tool{{Name: "example_list", Description: "Read items", InputSchema: json.RawMessage(`{"type":"object"}`)}}
}
func (f *catalogLegacy) Call(_ context.Context, name string, args json.RawMessage) (string, error) {
	f.reads++
	return name + ":" + string(args), nil
}
func (f *catalogLegacy) Actions() []proto.Tool {
	name := "example_send"
	if f.overlap {
		name = "example_list"
	}
	return []proto.Tool{{Name: name}}
}
func (*catalogLegacy) Describe(context.Context, string, json.RawMessage) (Action, string, error) {
	return Action{}, "", nil
}
func (f *catalogLegacy) Do(context.Context, string, json.RawMessage) (string, error) {
	f.writes++
	return "sent", nil
}
func TestCatalogLegacyReadToolsOnly(t *testing.T) {
	ctx := context.Background()
	f := &catalogLegacy{}
	sources, err := ReadCatalog(ctx, f)
	if err != nil || len(sources) != 1 || sources[0].ID != "example" || sources[0].Tools[0].Name != "list" {
		t.Fatalf("%+v %v", sources, err)
	}
	if text, err := CallCatalog(ctx, f, CatalogQuery{Source: "example", Tool: "list"}); err != nil || text != "example_list:{}" || f.reads != 1 {
		t.Fatalf("%s %v", text, err)
	}
	for _, tool := range []string{"send", "example_send"} {
		if _, err := CallCatalog(ctx, f, CatalogQuery{Source: "example", Tool: tool}); err == nil {
			t.Fatal("action admitted")
		}
	}
	f.overlap = true
	if _, err := CallCatalog(ctx, f, CatalogQuery{Source: "example", Tool: "list"}); err == nil {
		t.Fatal("tool also declared as an action admitted")
	}
	if f.reads != 1 || f.writes != 0 {
		t.Fatalf("invalid dispatch: %d reads, %d writes", f.reads, f.writes)
	}
}

func TestCatalogUnsafeDeclarationsNeverDispatch(t *testing.T) {
	no, yes := false, true
	for _, tc := range []struct {
		name string
		edit func(*CatalogTool)
	}{
		{"flag", func(t *CatalogTool) { t.ReadOnly = false }},
		{"read hint", func(t *CatalogTool) { t.Annotations = &CatalogAnnotations{ReadOnlyHint: &no} }},
		{"destructive hint", func(t *CatalogTool) { t.Annotations = &CatalogAnnotations{DestructiveHint: &yes} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := validCatalogFake()
			tc.edit(&f.sources[0].Tools[0])
			var re *rpc.Error
			if _, err := CallCatalog(context.Background(), f, CatalogQuery{Source: "notices", Tool: "list"}); !errors.As(err, &re) || re.Code != "not_read_only" || f.calls != 0 {
				t.Fatalf("%v, calls %d", err, f.calls)
			}
		})
	}
}
