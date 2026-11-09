package conformance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
)

type catalogConnector struct{}

func (catalogConnector) ID() string   { return "example" }
func (catalogConnector) Name() string { return "Example" }
func (catalogConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapCatalog}}
}
func (catalogConnector) Catalog(context.Context) ([]connector.CatalogSource, error) {
	return []connector.CatalogSource{{ID: "notices", Name: "Notices", Tools: []connector.CatalogTool{{Name: "list", Description: "Read notices", ReadOnly: true, InputSchema: json.RawMessage(`{"type":"object"}`)}}}}, nil
}
func (catalogConnector) CatalogCall(context.Context, connector.CatalogQuery) (string, error) {
	panic("conformance must not execute catalog tools")
}
func TestCatalogAcrossBothProtocolOpenings(t *testing.T) { Check(t, catalogConnector{}) }

func TestCheckProgramCatalog(t *testing.T) {
	f := &fixtureProgram{}
	if got := f.check(t); got != "" {
		t.Fatal(got)
	}
	if f.catalogAsked.Load() != 1 || f.wrote.Load() {
		t.Fatal("catalog was not read exactly once, or a write was called")
	}
}
func TestCheckProgramCatchesCatalogDrift(t *testing.T) {
	first := func(v map[string]any) map[string]any { return v["sources"].([]any)[0].(map[string]any) }
	for _, tc := range []struct {
		name, want string
		edit       func(map[string]any)
	}{
		{"write tool", "not read-only", func(v map[string]any) { first(v)["tools"].([]any)[0].(map[string]any)["read_only"] = false }},
		{"duplicate source", "source id", func(v map[string]any) { v["sources"] = append(v["sources"].([]any), first(v)) }},
		{"duplicate short name", "repeats tool", func(v map[string]any) { s := first(v); s["tools"] = append(s["tools"].([]any), s["tools"].([]any)[0]) }},
		{"nonobject schema", "not an object", func(v map[string]any) { first(v)["tools"].([]any)[0].(map[string]any)["input_schema"] = []any{} }},
		{"zero limit", "max_calls_per_hour", func(v map[string]any) { first(v)["max_calls_per_hour"] = 0 }},
		{"bad recipe reference", "unknown tool", func(v map[string]any) { first(v)["recipes"].([]any)[0].(map[string]any)["tool"] = "missing" }},
		{"malformed recipe", "items_path", func(v map[string]any) { first(v)["recipes"].([]any)[0].(map[string]any)["items_path"] = "$.items[*]" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fixtureProgram{edit: map[string]func(map[string]any){"catalog.list.json": tc.edit}}
			if got := f.check(t); !strings.Contains(got, tc.want) {
				t.Fatalf("want %q, got %s", tc.want, got)
			}
			if f.wrote.Load() {
				t.Fatal("a write was called")
			}
		})
	}
}
func TestUndeclaredCatalogIsNotProbed(t *testing.T) {
	remove := func(v map[string]any) {
		c := v["contributes"].(map[string]any)["connectors"].([]any)[0].(map[string]any)
		var caps []any
		for _, x := range c["capabilities"].([]any) {
			if x != "catalog" {
				caps = append(caps, x)
			}
		}
		c["capabilities"] = caps
	}
	var v map[string]any
	_ = json.Unmarshal(fixture(t, "persona-plugin.json"), &v)
	remove(v)
	raw, _ := json.Marshal(v)
	m, err := extension.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixtureProgram{manifest: m, edit: map[string]func(map[string]any){"initialize.json": remove}}
	if got := f.check(t); got != "" {
		t.Fatal(got)
	}
	if f.catalogAsked.Load() != 0 {
		t.Fatal("catalog read without declaration")
	}
}
