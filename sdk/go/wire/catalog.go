package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrCatalogNotReadOnly identifies a catalog tool whose declarations do not
// permit a read. Callers refuse it before dispatch, with code not_read_only.
var ErrCatalogNotReadOnly = errors.New("catalog tool is not read-only")

// CheckConnectorCatalog holds a typed catalog to the schema and the identities,
// tool declarations and recipe references that cannot be expressed in it.
func CheckConnectorCatalog(c ConnectorCatalog) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return CheckConnectorCatalogJSON(raw)
}

// CheckConnectorCatalogJSON checks the original result before decoding, so a
// mistyped field or an explicit zero limit cannot disappear during decoding.
func CheckConnectorCatalogJSON(raw []byte) error {
	if err := ValidateJSON("ConnectorCatalog", raw); err != nil {
		return err
	}
	var c ConnectorCatalog
	if err := json.Unmarshal(raw, &c); err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, s := range c.Sources {
		if strings.TrimSpace(s.ID) == "" || ids[s.ID] {
			return fmt.Errorf("catalog source id is empty or repeated: %q", s.ID)
		}
		ids[s.ID] = true
		if strings.TrimSpace(s.Name) == "" {
			return fmt.Errorf("catalog source %q has no name", s.ID)
		}
		tools := map[string]bool{}
		for _, t := range s.Tools {
			if tools[t.Name] {
				return fmt.Errorf("catalog source %q repeats tool %q", s.ID, t.Name)
			}
			tools[t.Name] = true
			if !t.ReadOnly || t.Annotations != nil &&
				(t.Annotations.ReadOnlyHint != nil && !*t.Annotations.ReadOnlyHint ||
					t.Annotations.DestructiveHint != nil && *t.Annotations.DestructiveHint) {
				return fmt.Errorf("%w: source %q tool %q", ErrCatalogNotReadOnly, s.ID, t.Name)
			}
			if strings.TrimSpace(t.Description) == "" {
				return fmt.Errorf("catalog tool %q has no description", t.Name)
			}
			var schema map[string]json.RawMessage
			if json.Unmarshal(t.InputSchema, &schema) != nil || schema == nil {
				return fmt.Errorf("catalog tool %q input_schema is not an object", t.Name)
			}
		}
		for i, r := range s.Recipes {
			if !tools[r.Tool] {
				return fmt.Errorf("catalog source %q recipe %d names unknown tool %q", s.ID, i, r.Tool)
			}
			if len(r.Args) > 0 && !catalogObject(r.Args) {
				return fmt.Errorf("catalog source %q recipe %d args is not an object", s.ID, i)
			}
		}
	}
	return nil
}

func catalogObject(raw json.RawMessage) bool {
	var v map[string]json.RawMessage
	return json.Unmarshal(raw, &v) == nil && v != nil
}
