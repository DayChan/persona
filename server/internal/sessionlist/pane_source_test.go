package sessionlist

import (
	"reflect"
	"testing"
)

func TestOriginOnlyChangeInvalidatesViews(t *testing.T) {
	var i Index
	items := []Item{testItem("a", 1), testItem("b", 2)}
	items[0].PaneSource, items[1].PaneSource = "terminal", "desktop"
	i.Replace("owner", items)
	first := mustQuery(t, &i, Options{PaneSource: "terminal"})
	_, _ = i.Counts(Options{PaneSource: "desktop"})
	// Deliberately keep both payloads, Source and UpdatedAt unchanged. The index
	// must detect the immutable adapter attribute independently of JSON changes.
	items[0].PaneSource, items[1].PaneSource = "desktop", "terminal"
	i.Replace("owner", items)
	delta := mustQuery(t, &i, Options{PaneSource: "terminal", Since: first.Revision})
	if delta.Revision == first.Revision || delta.Total != 1 || len(delta.Sessions) != 1 || !reflect.DeepEqual(delta.Remove, []string{"a"}) {
		t.Fatalf("delta: %+v", delta)
	}
	c, err := i.Counts(Options{PaneSource: "desktop"})
	if err != nil || c.Total != 1 || c.PaneSource["desktop"] != 1 || c.Revision != delta.Revision {
		t.Fatalf("counts: %+v %v", c, err)
	}
	c.PaneSource["desktop"] = 100
	c, _ = i.Counts(Options{PaneSource: "desktop"})
	if c.PaneSource["desktop"] != 1 {
		t.Fatal("counts exposed a mutable cached map")
	}
	all, _ := i.Counts(Options{})
	if !reflect.DeepEqual(all.PaneSource, map[string]int{"terminal": 1, "desktop": 1}) {
		t.Fatalf("unfiltered counters: %v", all.PaneSource)
	}
	i.RemoveSource("owner")
	all, _ = i.Counts(Options{})
	if all.Total != 0 || len(all.PaneSource) != 0 {
		t.Fatalf("removed counters: %+v", all)
	}
}
