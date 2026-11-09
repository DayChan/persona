package proto

import (
	"encoding/json"
	"testing"
)

func TestWorkspaceZeroIndexSignalsAnAuthoritativeCatalog(t *testing.T) {
	// Apple clients distinguish an old, flat catalog from a new root with no parent by the index field's presence.
	data, err := json.Marshal(Workspace{Source: "orca", SourceID: "root"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["index"]) != "0" {
		t.Fatalf("zero-index root lost its catalog version signal: %s", data)
	}
}
