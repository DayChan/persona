package kb

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// jsonFields lists a struct's JSON field names, embedded structs flattened.
func jsonFields(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous {
			out = append(out, jsonFields(f.Type)...)
			continue
		}
		if !f.IsExported() {
			continue
		}
		out = append(out, f.Tag.Get("json"))
	}
	slices.Sort(out)
	return out
}

// kb.Item is what the store keeps; kbwire.Item what connectors hand over: the same JSON, field for field.
func TestItemMatchesTheContract(t *testing.T) {
	have, want := jsonFields(reflect.TypeFor[Item]()), jsonFields(reflect.TypeFor[kbwire.Item]())
	if !slices.Equal(have, want) {
		t.Fatalf("kb.Item's JSON fields %v differ from kbwire.Item's %v: change both (and ItemFromWire) together", have, want)
	}
	at := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	w := kbwire.Item{Kind: KindMeeting, Source: "example", ID: "m1", Title: "Weekly", URL: "https://example.com/m1", At: at, Where: "calendar",
		Projects: []string{"p"}, Note: "n", Seen: at, Tagging: kbwire.Tagging{Importance: "P1", Relevance: 70,
			Tags: []string{"t"}, Locked: true, TaggedAt: at, Pinned: "fact", LockedFields: []string{"note"}}}
	got := ItemFromWire(w)
	if !reflect.DeepEqual(jsonOf(t, got), jsonOf(t, w)) {
		t.Fatalf("ItemFromWire lost a field: %s vs %s", jsonOf(t, got), jsonOf(t, w))
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
