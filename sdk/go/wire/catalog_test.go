package wire

import (
	"encoding/json"
	"errors"
	"testing"
)

const catalogExample = `{"sources":[{"id":"announcements","name":"Announcements","icon":"bell","service":"mail:example.com","min_interval_seconds":300,"max_calls_per_hour":120,"tools":[{"name":"list","description":"Read announcements","input_schema":{"type":"object","properties":{"pageToken":{"type":"string"}}},"read_only":true,"annotations":{"read_only_hint":true,"destructive_hint":false}}],"recipes":[{"mode":"items","tool":"list","args":{"pageToken":"","since":"{{since}}"},"items_path":"$.items","fields":{"id":"$.id","title":"$.title","time":"$.time","body":"$.body"},"kind":"sub_notice","group_by":"source"}]}]}`

func TestConnectorCatalogValidation(t *testing.T) {
	if err := CheckConnectorCatalogJSON([]byte(catalogExample)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		edit     func(map[string]any)
		readOnly bool
	}{
		{"source repeated", func(s map[string]any) {}, false},
		{"tool repeated", func(s map[string]any) { s["tools"] = append(s["tools"].([]any), s["tools"].([]any)[0]) }, false},
		{"not read only", func(s map[string]any) { s["tools"].([]any)[0].(map[string]any)["read_only"] = false }, true},
		{"false read hint", func(s map[string]any) {
			s["tools"].([]any)[0].(map[string]any)["annotations"] = map[string]any{"read_only_hint": false}
		}, true},
		{"destructive hint", func(s map[string]any) {
			s["tools"].([]any)[0].(map[string]any)["annotations"] = map[string]any{"destructive_hint": true}
		}, true},
		{"array schema", func(s map[string]any) { s["tools"].([]any)[0].(map[string]any)["input_schema"] = []any{} }, false},
		{"null schema", func(s map[string]any) { s["tools"].([]any)[0].(map[string]any)["input_schema"] = nil }, false},
		{"zero limit", func(s map[string]any) { s["max_calls_per_hour"] = 0 }, false},
		{"negative interval", func(s map[string]any) { s["min_interval_seconds"] = -1 }, false},
		{"wrong limit type", func(s map[string]any) { s["min_interval_seconds"] = "300" }, false},
		{"bad icon", func(s map[string]any) { s["icon"] = "invented-icon" }, false},
		{"unknown recipe tool", func(s map[string]any) { s["recipes"].([]any)[0].(map[string]any)["tool"] = "missing" }, false},
		{"array arguments", func(s map[string]any) { s["recipes"].([]any)[0].(map[string]any)["args"] = []any{} }, false},
		{"null arguments", func(s map[string]any) { s["recipes"].([]any)[0].(map[string]any)["args"] = nil }, false},
		{"bad mode", func(s map[string]any) { s["recipes"].([]any)[0].(map[string]any)["mode"] = "expression" }, false},
		{"path expression", func(s map[string]any) { s["recipes"].([]any)[0].(map[string]any)["items_path"] = "$.items[?(@.title)]" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v map[string]any
			_ = json.Unmarshal([]byte(catalogExample), &v)
			s := v["sources"].([]any)[0].(map[string]any)
			tc.edit(s)
			if tc.name == "source repeated" {
				v["sources"] = append(v["sources"].([]any), s)
			}
			raw, _ := json.Marshal(v)
			err := CheckConnectorCatalogJSON(raw)
			if err == nil {
				t.Fatal("invalid catalog accepted")
			}
			if tc.readOnly && !errors.Is(err, ErrCatalogNotReadOnly) {
				t.Fatalf("wrong refusal: %v", err)
			}
		})
	}
}

func TestCatalogOptionalHintsAndSnapshotRecipe(t *testing.T) {
	raw := []byte(`{"sources":[{"id":"balance","name":"Balance","tools":[{"name":"read","description":"Read a balance","input_schema":{},"read_only":true}],"recipes":[{"mode":"snapshot","tool":"read","kind":"sub_update","group_by":"source"}]}]}`)
	if err := CheckConnectorCatalogJSON(raw); err != nil {
		t.Fatal(err)
	}
	if err := CheckConnectorCatalog(ConnectorCatalog{Sources: []ConnectorCatalogSource{}}); err != nil {
		t.Fatal(err)
	}
}
