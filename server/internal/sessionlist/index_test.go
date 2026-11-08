package sessionlist

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type testPayload struct {
	ID      string              `json:"id"`
	Title   string              `json:"title"`
	Details map[string][]string `json:"details,omitempty"`
}

var testTime = time.Unix(1700000000, 0).UTC()

func testItem(id string, seconds int) Item {
	return Item{ID: id, Machine: "machine-a", Project: "project-a", Group: "group-a", State: "working", Source: "runtime", Provider: "provider-a", UpdatedAt: testTime.Add(time.Duration(seconds) * time.Second), SearchText: "Title " + id, Full: testPayload{ID: id, Title: "Full " + id}, Summary: testPayload{ID: id, Title: "Summary " + id}}
}

func mustQuery(t *testing.T, i *Index, o Options) Page {
	t.Helper()
	p, err := i.Query(o)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func sessionIDs(p Page) []string {
	ids := make([]string, 0, len(p.Sessions))
	for _, v := range p.Sessions {
		ids = append(ids, v.(testPayload).ID)
	}
	return ids
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error = %v, want typed %s", err, code)
	}
}

func TestExactIdentityLookup(t *testing.T) {
	var i Index
	items := make([]Item, 8000)
	for n := range items {
		items[n] = testItem(fmt.Sprintf("s%04d", n), n)
	}
	i.Replace("batch", items)
	o := Options{ID: "s0000", Machine: "machine-a", Limit: 50}
	p := mustQuery(t, &i, o) // the oldest row is not on the first ordinary page
	if p.Total != 1 || len(p.Sessions) != 1 || p.HasMore || p.NextCursor != "" || len(p.Remove) != 0 || p.Revision != i.Revision() ||
		!reflect.DeepEqual(p.Sessions[0], items[0].Full) {
		t.Fatalf("exact full row = %+v", p)
	}
	o.Summary = true
	if got := mustQuery(t, &i, o).Sessions[0]; !reflect.DeepEqual(got, items[0].Summary) {
		t.Fatalf("summary = %+v", got)
	}
	c, err := i.Counts(o)
	if err != nil || c.Total != 1 || c.Machine["machine-a"] != 1 || c.State["working"] != 1 {
		t.Fatalf("exact counts = %+v, %v", c, err)
	}
	for _, absent := range []Options{{ID: "missing"}, {ID: "s0000", Machine: "other"}, {ID: "s0000", State: "idle"}, {ID: "s0000", Q: "no match"}} {
		p := mustQuery(t, &i, absent)
		if p.Total != 0 || len(p.Sessions) != 0 || p.Sessions == nil || p.Remove == nil || p.HasMore || p.NextCursor != "" {
			t.Fatalf("missing/filter = %+v", p)
		}
	}
	// Exact queries never populate/reuse a fleet-wide ordered/counts cache.
	if len(i.cache) != 0 || len(i.snapshots) != 0 {
		t.Fatal("exact queries created cached collection views")
	}
	i.Replace("changes", []Item{testItem("unrelated", 10000)})
	if p := mustQuery(t, &i, o); p.Revision == c.Revision || p.Total != 1 {
		t.Fatalf("unrelated revision = %+v", p)
	}
	i.RemoveSource("batch")
	if p := mustQuery(t, &i, o); p.Total != 0 || len(p.Sessions) != 0 {
		t.Fatalf("deleted = %+v", p)
	}
	for _, invalid := range []Options{{ID: "s0000", Since: i.Revision()}, {ID: "s0000", Cursor: "cursor"}} {
		_, err := i.Query(invalid)
		requireCode(t, err, "invalid_query")
		_, err = i.Counts(invalid)
		requireCode(t, err, "invalid_query")
	}
}

func TestOrderingAndZeroValue(t *testing.T) {
	var i Index
	empty := mustQuery(t, &i, Options{})
	if empty.Total != 0 || empty.HasMore || empty.Sessions == nil || empty.Remove == nil || empty.Revision == "" {
		t.Fatalf("empty = %+v", empty)
	}
	before := i.Revision()
	i.Replace("batch", []Item{testItem("c", 0), testItem("b", 1), testItem("a", 1)})
	page := mustQuery(t, &i, Options{})
	if got := sessionIDs(page); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("order = %v", got)
	}
	if page.Revision == before || page.Revision != i.Revision() {
		t.Fatal("revision was not advanced")
	}
	if page.HasMore || page.NextCursor != "" || page.Total != 3 {
		t.Fatalf("page = %+v", page)
	}
	summary := mustQuery(t, &i, Options{Summary: true})
	if summary.Sessions[0].(testPayload).Title != "Summary a" {
		t.Fatal("summary projection was not selected")
	}
	counts, err := i.Counts(Options{})
	if err != nil || counts.Total != 3 || counts.Machine["machine-a"] != 3 || counts.Revision != page.Revision {
		t.Fatalf("counts = %+v, %v", counts, err)
	}
	counts.Machine["machine-a"] = 99
	again, _ := i.Counts(Options{})
	if again.Machine["machine-a"] != 3 {
		t.Fatal("caller changed cached counts")
	}
	wire, err := json.Marshal(empty)
	if err != nil || !strings.Contains(string(wire), `"sessions":[]`) || !strings.Contains(string(wire), `"remove":[]`) || strings.Contains(string(wire), "next_cursor") {
		t.Fatalf("wire = %s, %v", wire, err)
	}
}

func TestEightThousandFrozenPagination(t *testing.T) {
	var i Index
	items := make([]Item, 8000)
	for n := range items {
		items[n] = testItem(fmt.Sprintf("session-%05d", n), n/3)
	}
	i.Replace("batch", items)
	expected := append([]Item(nil), items...)
	sort.Slice(expected, func(a, b int) bool {
		if expected[a].UpdatedAt.Equal(expected[b].UpdatedAt) {
			return expected[a].ID < expected[b].ID
		}
		return expected[a].UpdatedAt.After(expected[b].UpdatedAt)
	})
	o := Options{Limit: 200}
	first := mustQuery(t, &i, o)
	revision := first.Revision
	all := sessionIDs(first)
	// Publish a different list after page one: move old entries, delete one,
	// and insert a newer one. None may change the continuation's contents.
	items[0].UpdatedAt = testTime.Add(24 * time.Hour)
	items[0].Full = testPayload{ID: items[0].ID, Title: "changed"}
	items = append(items[1:], testItem("new", 100000))
	i.Replace("batch", items)
	o.Cursor = first.NextCursor
	pages := 1
	for o.Cursor != "" {
		p := mustQuery(t, &i, o)
		if p.Revision != revision || p.Total != 8000 {
			t.Fatalf("continuation moved revision/total: %+v", p)
		}
		all = append(all, sessionIDs(p)...)
		o.Cursor = p.NextCursor
		pages++
		if pages > 40 {
			t.Fatal("pagination did not terminate")
		}
	}
	if pages != 40 || len(all) != 8000 {
		t.Fatalf("pages=%d, sessions=%d", pages, len(all))
	}
	for n, id := range all {
		if id != expected[n].ID {
			t.Fatalf("position %d = %s, want %s", n, id, expected[n].ID)
		}
	}
	if i.Revision() == revision {
		t.Fatal("updated live revision should differ")
	}
	c, err := i.Counts(Options{Limit: 200, Cursor: first.NextCursor})
	if err != nil || c.Revision != revision || c.Total != 8000 {
		t.Fatalf("frozen counts = %+v, %v", c, err)
	}
}

func TestFiltersAndCountsReuse(t *testing.T) {
	var i Index
	items := []Item{testItem("a", 1), testItem("b", 2), testItem("c", 3), testItem("d", 4), testItem("e", 5)}
	items[0].SearchText = "ALPHA Atlas"
	items[1].SearchText, items[1].Group, items[1].State, items[1].Provider = "beta", "group-b", "done", "provider-b"
	items[2].SearchText, items[2].Project, items[2].Source = "Alpha Beta", "project-b", "extension"
	items[3].SearchText, items[3].Machine, items[3].State, items[3].Source, items[3].Provider = "Gamma", "machine-b", "blocked", "extension", "provider-b"
	items[4].SearchText, items[4].Machine, items[4].Project, items[4].Group = "ALPHA", "machine-b", "project-b", "group-b"
	i.Replace("owner", items)
	tests := []struct {
		name    string
		options Options
		ids     []string
	}{
		{"machine", Options{Machine: "machine-b"}, []string{"e", "d"}},
		{"project", Options{Project: "project-b"}, []string{"e", "c"}},
		{"group", Options{Group: "group-b"}, []string{"e", "b"}},
		{"state", Options{State: "working"}, []string{"e", "c", "a"}},
		{"source", Options{Source: "extension"}, []string{"d", "c"}},
		{"provider", Options{Provider: "provider-b"}, []string{"d", "b"}},
		{"query", Options{Q: "aLpHa"}, []string{"e", "c", "a"}},
		{"substring", Options{Q: "Ha bE"}, []string{"c"}},
		{"all", Options{Machine: "machine-a", Project: "project-b", Group: "group-a", State: "working", Source: "extension", Provider: "provider-a", Q: "alpha"}, []string{"c"}},
		{"empty", Options{Q: "missing"}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := mustQuery(t, &i, tt.options)
			if got := sessionIDs(p); !reflect.DeepEqual(got, tt.ids) {
				t.Fatalf("IDs = %v, want %v", got, tt.ids)
			}
			o, _ := validate(tt.options)
			cached := i.cache[cacheKey{filters: o.filters()}].result
			c, err := i.Counts(tt.options)
			if err != nil || c.Total != len(tt.ids) || c.Revision != p.Revision {
				t.Fatalf("counts = %+v, %v", c, err)
			}
			for _, grouped := range []map[string]int{c.Machine, c.Project, c.Group, c.State} {
				total := 0
				for _, n := range grouped {
					total += n
				}
				if total != c.Total {
					t.Fatalf("grouped counts = %v", grouped)
				}
			}
			if i.cache[cacheKey{filters: o.filters()}].result != cached {
				t.Fatal("Counts did not reuse filtered ordered view")
			}
			mustQuery(t, &i, tt.options)
			if i.cache[cacheKey{filters: o.filters()}].result != cached {
				t.Fatal("warm Query did not reuse view")
			}
		})
	}
	c, _ := i.Counts(Options{})
	if c.Machine["machine-a"] != 3 || c.Project["project-a"] != 3 || c.Group["group-a"] != 3 || c.State["working"] != 3 || c.State["done"] != 1 {
		t.Fatalf("counts = %+v", c)
	}
}

func TestUnchangedReplaceAndBatchOwnership(t *testing.T) {
	var i Index
	a, b := testItem("a", 1), testItem("b", 2)
	i.Replace("owner-a", []Item{a, b})
	first := mustQuery(t, &i, Options{})
	cached := i.cache[cacheKey{}].result
	i.Replace("owner-a", []Item{b, a})
	if i.Revision() != first.Revision || i.cache[cacheKey{}].result != cached {
		t.Fatal("unchanged replacement invalidated revision/cache")
	}
	// Source filters are payload attributes, not the batch's ownership key.
	i.Replace("owner-b", []Item{testItem("c", 3)})
	if p := mustQuery(t, &i, Options{Source: "runtime"}); p.Total != 3 {
		t.Fatalf("source-filter total = %d", p.Total)
	}
	i.RemoveSource("owner-a")
	if got := sessionIDs(mustQuery(t, &i, Options{})); !reflect.DeepEqual(got, []string{"c"}) {
		t.Fatalf("remaining IDs = %v", got)
	}
	revision := i.Revision()
	i.RemoveSource("owner-a")
	i.RemoveSource("unknown")
	if i.Revision() != revision {
		t.Fatal("no-op removal bumped revision")
	}
}

func TestDeltaChangesAndFilteredExit(t *testing.T) {
	var i Index
	a, b, c := testItem("a", 1), testItem("b", 2), testItem("c", 3)
	c.State = "done"
	i.Replace("batch", []Item{a, b, c})
	baseline := i.Revision()
	// Unchanged timestamps still expose non-time field changes.
	a.Full = testPayload{ID: "a", Title: "new title"}
	a.Summary = testPayload{ID: "a", Title: "new summary"}
	a.SearchText = "new title"
	b.State = "done"
	c.State = "working"
	d := testItem("d", 4)
	i.Replace("batch", []Item{a, b, c, d})
	i.Replace("batch", []Item{a, b, c}) // d was never known at baseline
	full := mustQuery(t, &i, Options{Since: baseline})
	if got := sessionIDs(full); !reflect.DeepEqual(got, []string{"c", "b", "a"}) || len(full.Remove) != 0 || full.Total != 3 {
		t.Fatalf("full delta = %+v", full)
	}
	filtered := mustQuery(t, &i, Options{Since: baseline, State: "working"})
	if got := sessionIDs(filtered); !reflect.DeepEqual(got, []string{"c", "a"}) || !reflect.DeepEqual(filtered.Remove, []string{"b"}) || filtered.Total != 2 {
		t.Fatalf("filtered delta = %+v", filtered)
	}
	if filtered.Sessions[1].(testPayload).Title != "new title" {
		t.Fatal("non-timestamp title update was lost")
	}
	summary := mustQuery(t, &i, Options{Since: baseline, Summary: true})
	if summary.Sessions[2].(testPayload).Title != "new summary" {
		t.Fatal("summary update was lost")
	}
	// A query-specific exit needs a removal even though the live session exists.
	queryDelta := mustQuery(t, &i, Options{Since: baseline, Q: "Title a"})
	if len(queryDelta.Sessions) != 0 || !reflect.DeepEqual(queryDelta.Remove, []string{"a"}) {
		t.Fatalf("search exit = %+v", queryDelta)
	}
	secondBaseline := i.Revision()
	i.RemoveSource("batch")
	removed := mustQuery(t, &i, Options{Since: secondBaseline, State: "working"})
	if len(removed.Sessions) != 0 || !reflect.DeepEqual(removed.Remove, []string{"c", "a"}) || removed.Total != 0 {
		t.Fatalf("removals = %+v", removed)
	}
	noChanges := mustQuery(t, &i, Options{Since: i.Revision()})
	if noChanges.Total != 0 || len(noChanges.Sessions)+len(noChanges.Remove) != 0 {
		t.Fatalf("empty delta = %+v", noChanges)
	}
}

func TestDeltaCollapsesLatestAndFrozenHighwater(t *testing.T) {
	var i Index
	items := []Item{testItem("a", 1), testItem("b", 2), testItem("c", 3), testItem("d", 4)}
	i.Replace("batch", items)
	baseline := i.Revision()
	items[0].Full = testPayload{ID: "a", Title: "intermediate"}
	i.Replace("batch", items)
	items[0].Full = testPayload{ID: "a", Title: "latest"}
	items[1].State = "done"
	items[2].Full = testPayload{ID: "c", Title: "changed c"}
	items = append(items[:3], testItem("e", 5)) // delete d, insert e
	i.Replace("batch", items)
	o := Options{Limit: 2, Since: baseline, State: "working"}
	first := mustQuery(t, &i, o)
	if !first.HasMore || len(first.Sessions)+len(first.Remove) != 2 {
		t.Fatalf("first = %+v", first)
	}
	highwater := first.Revision
	o.Cursor = first.NextCursor
	// Even journal eviction during pagination cannot invalidate this snapshot.
	for n := 0; n < maxJournalRevisions+1; n++ {
		items[0].Full = testPayload{ID: "a", Title: fmt.Sprintf("later-%d", n)}
		i.Replace("batch", items)
	}
	allIDs, allRemove := sessionIDs(first), append([]string(nil), first.Remove...)
	for o.Cursor != "" {
		p := mustQuery(t, &i, o)
		if p.Revision != highwater || p.Total != 3 {
			t.Fatalf("delta highwater moved: %+v", p)
		}
		allIDs = append(allIDs, sessionIDs(p)...)
		allRemove = append(allRemove, p.Remove...)
		for _, v := range p.Sessions {
			if payload := v.(testPayload); payload.ID == "a" && payload.Title != "latest" {
				t.Fatalf("a = %+v", payload)
			}
		}
		o.Cursor = p.NextCursor
	}
	if !reflect.DeepEqual(allIDs, []string{"e", "c", "a"}) || !reflect.DeepEqual(allRemove, []string{"d", "b"}) {
		t.Fatalf("sessions=%v remove=%v", allIDs, allRemove)
	}
	// New changes must be requested from the snapshot's target, not baseline.
	_, err := i.Query(Options{Since: baseline})
	requireCode(t, err, "revision_expired")
}

func TestDeltaNetNoOpAndReinsert(t *testing.T) {
	var i Index
	a := testItem("a", 1)
	i.Replace("batch", []Item{a})
	baseline := i.Revision()
	changed := a
	changed.State = "done"
	i.Replace("batch", []Item{changed})
	i.RemoveSource("batch")
	i.Replace("batch", []Item{a})
	p := mustQuery(t, &i, Options{Since: baseline, State: "working"})
	if len(p.Sessions)+len(p.Remove) != 0 || p.Total != 1 {
		t.Fatalf("net-no-op = %+v", p)
	}
	// Delete and recreate with a new payload should upsert, not remove+insert.
	i.RemoveSource("batch")
	a.Full = testPayload{ID: "a", Title: "recreated"}
	i.Replace("batch", []Item{a})
	p = mustQuery(t, &i, Options{Since: baseline, State: "working"})
	if !reflect.DeepEqual(sessionIDs(p), []string{"a"}) || len(p.Remove) != 0 {
		t.Fatalf("reinsert = %+v", p)
	}
}

func TestRevisionExpiryAndBounds(t *testing.T) {
	var i Index
	baseline := i.Revision()
	var atFloor string
	for n := 0; n < maxJournalRevisions+1; n++ {
		i.Replace("batch", []Item{testItem("a", n)})
		if n == 0 {
			atFloor = i.Revision()
		}
	}
	_, err := i.Query(Options{Since: baseline})
	requireCode(t, err, "revision_expired")
	mustQuery(t, &i, Options{Since: atFloor})
	if len(i.journal) != maxJournalRevisions || i.floor != 1 {
		t.Fatalf("journal=%d floor=%d", len(i.journal), i.floor)
	}
	var restarted Index
	_, err = restarted.Query(Options{Since: i.Revision()})
	requireCode(t, err, "revision_expired")
	_, err = restarted.Counts(Options{Since: i.Revision()})
	requireCode(t, err, "revision_expired")
	// A single oversized revision cannot be partially retained.
	large := make([]Item, maxJournalChanges+1)
	for n := range large {
		large[n] = testItem(fmt.Sprintf("large-%05d", n), n)
	}
	current := i.Revision()
	i.Replace("large", large)
	_, err = i.Query(Options{Since: current})
	requireCode(t, err, "revision_expired")
	if len(i.journal) != 0 || i.journalChanges != 0 || i.floor != i.revision {
		t.Fatalf("oversized journal: len=%d changes=%d floor=%d", len(i.journal), i.journalChanges, i.floor)
	}
	mustQuery(t, &i, Options{Since: i.Revision()})
}

func TestCursorExpiryBindingAndEviction(t *testing.T) {
	now := testTime
	i := Index{now: func() time.Time { return now }}
	i.Replace("batch", []Item{testItem("a", 1), testItem("b", 2), testItem("c", 3)})
	first := mustQuery(t, &i, Options{Limit: 1})
	original := Options{Cursor: first.NextCursor, Limit: 1}
	mutations := []Options{
		{Cursor: first.NextCursor, State: "working"},
		{Cursor: first.NextCursor, Machine: "machine-a"},
		{Cursor: first.NextCursor, Project: "project-a"},
		{Cursor: first.NextCursor, Group: "group-a"},
		{Cursor: first.NextCursor, Source: "runtime"},
		{Cursor: first.NextCursor, Provider: "provider-a"},
		{Cursor: first.NextCursor, Q: "title"},
		{Cursor: first.NextCursor, Summary: true},
		{Cursor: first.NextCursor, Since: i.Revision()},
	}
	for _, o := range mutations {
		_, err := i.Query(o)
		requireCode(t, err, "invalid_query")
	}
	// Page size is not query binding; a continuation may request a larger page.
	last := mustQuery(t, &i, Options{Cursor: first.NextCursor, Limit: 2})
	if last.HasMore || len(last.Sessions) != 2 {
		t.Fatalf("last = %+v", last)
	}
	// Replay is allowed until absolute TTL, including after the final page.
	mustQuery(t, &i, original)
	var restarted Index
	_, err := restarted.Query(original)
	requireCode(t, err, "cursor_expired")
	now = now.Add(snapshotTTL)
	_, err = i.Query(original)
	requireCode(t, err, "cursor_expired")
	if len(i.snapshots) != 0 || len(i.snapshotOrder) != 0 {
		t.Fatal("expired snapshots were not pruned")
	}
	first = mustQuery(t, &i, Options{Limit: 1})
	for n := 0; n < maxSnapshots; n++ {
		mustQuery(t, &i, Options{Limit: 1})
	}
	_, err = i.Query(Options{Cursor: first.NextCursor})
	requireCode(t, err, "cursor_expired")
	if len(i.snapshots) != maxSnapshots || len(i.snapshotOrder) != maxSnapshots {
		t.Fatal("snapshot bound exceeded")
	}
}

func TestCacheBoundAndHistoricalReferences(t *testing.T) {
	var i Index
	i.Replace("batch", []Item{testItem("a", 1), testItem("b", 2)})
	first := mustQuery(t, &i, Options{Limit: 1})
	oldResult := i.cache[cacheKey{}].result
	for n := 0; n < maxCache+10; n++ {
		mustQuery(t, &i, Options{Q: fmt.Sprintf("query-%d", n)})
	}
	if len(i.cache) != maxCache {
		t.Fatalf("cache length = %d", len(i.cache))
	}
	mustQuery(t, &i, Options{Cursor: first.NextCursor}) // cache eviction does not evict snapshots
	i.Replace("batch", []Item{testItem("c", 3)})
	if len(i.cache) != 0 {
		t.Fatal("historical cache was not discarded")
	}
	for _, s := range i.snapshots {
		if s.result != oldResult {
			t.Fatal("unexpected historical snapshot")
		}
	}
	i.now = func() time.Time { return time.Now().Add(snapshotTTL + time.Second) }
	i.Revision() // prune even on lightweight reads
	if len(i.snapshots) != 0 {
		t.Fatal("stale snapshots survived pruning")
	}
}

func TestInputAndOutputDetachment(t *testing.T) {
	var i Index
	payload := &testPayload{ID: "a", Title: "original", Details: map[string][]string{"key": {"original"}}}
	a := testItem("a", 1)
	a.Full, a.Summary = payload, json.RawMessage(`{"id":"a","title":"original"}`)
	i.Replace("batch", []Item{a, testItem("b", 2)})
	revision := i.Revision()
	payload.Title = "input mutation"
	payload.Details["key"][0] = "input mutation"
	a.Summary.(json.RawMessage)[0] = '['
	p := mustQuery(t, &i, Options{})
	got := p.Sessions[1].(*testPayload)
	if got.Title != "original" || got.Details["key"][0] != "original" {
		t.Fatalf("input was retained: %+v", got)
	}
	got.Title, got.Details["key"][0] = "output mutation", "output mutation"
	again := mustQuery(t, &i, Options{})
	if v := again.Sessions[1].(*testPayload); v.Title != "original" || v.Details["key"][0] != "original" {
		t.Fatalf("output was retained: %+v", v)
	}
	summary := mustQuery(t, &i, Options{Summary: true})
	raw := summary.Sessions[1].(json.RawMessage)
	if string(raw) != `{"id":"a","title":"original"}` {
		t.Fatalf("raw = %s", raw)
	}
	raw[0] = '['
	if i.Revision() != revision {
		t.Fatal("caller mutation advanced revision")
	}
	summary = mustQuery(t, &i, Options{Summary: true})
	if !json.Valid(summary.Sessions[1].(json.RawMessage)) {
		t.Fatal("raw output mutation reached index")
	}
}

func TestConcurrentQueriesAndReplacement(t *testing.T) {
	var i Index
	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			owner := fmt.Sprintf("owner-%d", worker)
			for n := 0; n < 150; n++ {
				items := make([]Item, 8)
				for id := range items {
					items[id] = testItem(fmt.Sprintf("%s-%d", owner, id), n+id)
				}
				i.Replace(owner, items)
				p, err := i.Query(Options{Limit: 3, Q: "title"})
				if err != nil {
					t.Error(err)
					return
				}
				if len(p.Sessions) > 3 {
					t.Error("page exceeded limit")
					return
				}
				if p.NextCursor != "" {
					_, err = i.Query(Options{Limit: 3, Q: "title", Cursor: p.NextCursor})
					if err != nil {
						var e *Error
						if !errors.As(err, &e) || e.Code != "cursor_expired" {
							t.Error(err)
							return
						}
					}
				}
				if _, err := i.Counts(Options{}); err != nil {
					t.Error(err)
					return
				}
				baseline := i.Revision()
				if _, err := queryDeltaOrRefresh(&i, baseline); err != nil {
					t.Error(err)
					return
				}
				if n%5 == 0 {
					i.RemoveSource(owner)
				}
				// Mutations of returned detached values are also race-safe.
				if len(p.Sessions) > 0 {
					v := p.Sessions[0].(testPayload)
					v.Title = "local"
				}
			}
		}(worker)
	}
	wg.Wait()
	if len(i.snapshots) > maxSnapshots || len(i.cache) > maxCache || len(i.journal) > maxJournalRevisions || i.journalChanges > maxJournalChanges {
		t.Fatal("concurrent use exceeded bounds")
	}
}
