package kb

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

const (
	sigMentions = kbwire.SignalMentions
	sigComments = kbwire.SignalCommentMentions
	sigAuthored = kbwire.SignalAuthored
	sigViewed   = kbwire.SignalViewed
)

func setDocRule(s *Selection, name string, on bool, days *int) {
	s.Docs.Rules[name] = DocRule{On: on, Days: days}
}

// The highest level with a matching signal decides; an off rule is a decision too; a level's rules combine; a
// per-item setting comes first.
func TestSelectionDocPriority(t *testing.T) {
	doc := func(id string, signals ...string) DocFacts { return DocFacts{Source: "s", ID: id, Signals: signals} }
	for _, tc := range []struct {
		name string
		sel  *Selection
		doc  DocFacts
		want Decision
	}{
		{"defaults: viewed", sel(nil), doc("a", sigViewed), Decision{Store: true, Rule: sigViewed, Days: 365}},
		{"defaults: authored", sel(nil), doc("a", sigAuthored), Decision{Store: true, Rule: sigAuthored, Days: 365}},
		{"defaults: every signal", sel(nil), doc("a", kbwire.Signals...), Decision{Store: true, Rule: sigMentions + "+" + sigComments, Days: 365}},
		{"a mention beats an off viewed rule", sel(func(s *Selection) { setDocRule(s, sigViewed, false, nil) }),
			doc("a", sigViewed, sigMentions), Decision{Store: true, Rule: sigMentions, Days: 365}},
		{"an off rule above does not fall through", sel(func(s *Selection) {
			setDocRule(s, sigAuthored, false, nil)
			setDocRule(s, sigViewed, true, intp(30))
		}), doc("a", sigAuthored, sigViewed), Decision{Rule: sigAuthored}},
		{"reordered: viewed above mentions", sel(func(s *Selection) {
			s.Docs.Order = [][]string{{sigViewed}, {sigMentions, sigComments}, {sigAuthored}}
			setDocRule(s, sigViewed, false, nil)
		}), doc("a", sigMentions, sigViewed), Decision{Rule: sigViewed}},
		{"same level: the largest days of the on rules", sel(func(s *Selection) {
			setDocRule(s, sigMentions, true, intp(30))
			setDocRule(s, sigComments, true, intp(400))
		}), doc("a", sigMentions, sigComments), Decision{Store: true, Rule: sigMentions + "+" + sigComments, Days: 400}},
		{"same level: one on, one off", sel(func(s *Selection) {
			setDocRule(s, sigMentions, false, nil)
			setDocRule(s, sigComments, true, intp(90))
		}), doc("a", sigMentions, sigComments), Decision{Store: true, Rule: sigComments, Days: 90}},
		{"per item read false beats a mention", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "s", Kind: ItemDoc, ID: "x", Read: false})
		}), doc("x", sigMentions), Decision{Rule: RuleItem}},
		{"per item store keeps a doc the rules leave out", sel(func(s *Selection) {
			setDocRule(s, sigViewed, false, nil)
			s.SetItem(SelectionItem{Source: "s", Kind: ItemDoc, ID: "x", Read: true, Store: true, Days: intp(20)})
		}), doc("x", sigViewed), Decision{Store: true, Rule: RuleItem, Days: 20}},
		{"per item of another source does not apply", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "other", Kind: ItemDoc, ID: "x", Read: false})
		}), doc("x", sigViewed), Decision{Store: true, Rule: sigViewed, Days: 365}},
		{"per item read without store: the rules decide", sel(func(s *Selection) {
			setDocRule(s, sigViewed, false, nil)
			s.SetItem(SelectionItem{Source: "s", Kind: ItemDoc, ID: "x", Read: true})
		}), doc("x", sigViewed), Decision{Rule: sigViewed}},
	} {
		if got := tc.sel.EvaluateDoc(testRet, tc.doc); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

func TestSelectionDocSelected(t *testing.T) {
	off := sel(func(s *Selection) {
		for _, name := range DocRuleNames {
			setDocRule(s, name, false, nil)
		}
		s.SetItem(SelectionItem{Source: "s", Kind: ItemDoc, ID: "skip", Read: false})
		s.SetItem(SelectionItem{Source: "s", Kind: ItemDoc, ID: "keep", Read: true, Store: true})
	})
	for _, tc := range []struct {
		name string
		sel  *Selection
		doc  DocFacts
		want bool
	}{
		{"defaults keep every signalled doc", sel(nil), DocFacts{Source: "s", ID: "a", Signals: []string{sigViewed}}, true},
		{"no signals: as before", off, DocFacts{Source: "s", ID: "a"}, true},
		{"every rule off", off, DocFacts{Source: "s", ID: "a", Signals: []string{sigMentions}}, false},
		{"per item read false, even without signals", off, DocFacts{Source: "s", ID: "skip"}, false},
		{"per item store", off, DocFacts{Source: "s", ID: "keep", Signals: []string{sigViewed}}, true},
	} {
		if got := tc.sel.DocSelected(testRet, tc.doc); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Only days the user set delete a document: the defaults, an off rule, a null days and a per-item read false keep it.
func TestSelectionDocDays(t *testing.T) {
	doc := func(id string, signals ...string) DocFacts { return DocFacts{Source: "s", ID: id, Signals: signals} }
	type want struct {
		days int
		ok   bool
	}
	for _, tc := range []struct {
		name string
		sel  *Selection
		doc  DocFacts
		want want
	}{
		{"defaults", sel(nil), doc("a", kbwire.Signals...), want{}},
		{"defaults, no signals", sel(nil), doc("a"), want{}},
		{"viewed 30", sel(func(s *Selection) { setDocRule(s, sigViewed, true, intp(30)) }), doc("a", sigViewed), want{30, true}},
		{"viewed 30 but authored null decides", sel(func(s *Selection) { setDocRule(s, sigViewed, true, intp(30)) }),
			doc("a", sigAuthored, sigViewed), want{}},
		{"viewed off with days", sel(func(s *Selection) { setDocRule(s, sigViewed, false, intp(30)) }), doc("a", sigViewed), want{}},
		{"same level, one null", sel(func(s *Selection) { setDocRule(s, sigMentions, true, intp(30)) }),
			doc("a", sigMentions, sigComments), want{}},
		{"same level, both set", sel(func(s *Selection) {
			setDocRule(s, sigMentions, true, intp(30))
			setDocRule(s, sigComments, true, intp(60))
		}), doc("a", sigMentions, sigComments), want{60, true}},
		{"same level, one off", sel(func(s *Selection) {
			setDocRule(s, sigMentions, false, nil)
			setDocRule(s, sigComments, true, intp(60))
		}), doc("a", sigMentions, sigComments), want{60, true}},
		{"per item days, no signals", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "s", Kind: ItemDoc, ID: "x", Read: true, Store: true, Days: intp(10)})
		}), doc("x"), want{10, true}},
		{"per item read false", sel(func(s *Selection) {
			setDocRule(s, sigViewed, true, intp(30))
			s.SetItem(SelectionItem{Source: "s", Kind: ItemDoc, ID: "x", Read: false})
		}), doc("x", sigViewed), want{}},
		{"per item store, null days: the rule's", sel(func(s *Selection) {
			setDocRule(s, sigViewed, true, intp(30))
			s.SetItem(SelectionItem{Source: "s", Kind: ItemDoc, ID: "x", Read: true, Store: true})
		}), doc("x", sigViewed), want{30, true}},
	} {
		days, ok := tc.sel.DocDays(tc.doc)
		if (want{days, ok}) != tc.want {
			t.Errorf("%s: (%d, %v), want %+v", tc.name, days, ok, tc.want)
		}
	}
}

type titledItems struct {
	items  []kbwire.Item
	titled []string
}

func (s *titledItems) Items(context.Context) ([]kbwire.Item, error) { return s.items, nil }
func (s *titledItems) DocTitle(_ context.Context, url string) string {
	s.titled = append(s.titled, url)
	return "Title of " + url
}

func docSelEngine(t *testing.T, s *Selection) *Engine {
	t.Helper()
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &Engine{Store: st, TextDays: 365, Selection: func() *Selection { return s }}
}

func TestCollectItemsBySelection(t *testing.T) {
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	s := sel(func(s *Selection) {
		setDocRule(s, sigViewed, false, nil)
		s.SetItem(SelectionItem{Source: "conn", Kind: ItemDoc, ID: "kept", Read: true, Store: true})
		s.SetItem(SelectionItem{Source: "conn", Kind: ItemDoc, ID: "never", Read: false})
		s.SetItem(SelectionItem{Source: "conn", Kind: ItemDoc, ID: "untitled", Read: false})
	})
	e := docSelEngine(t, s)
	// Stored before the viewed rule was turned off: left exactly as it is.
	before := &Item{Kind: KindDoc, Source: "conn", ID: "old", Title: "Old", At: at, Signals: []string{sigViewed}, Note: "a note"}
	if err := e.Store.PutItem(before); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(e.Store.entityPath(KindDoc, "old"))
	// A doc seen in a chat, never titled, that the user keeps from being read.
	if err := e.Store.PutItem(&Item{Kind: KindDoc, Source: "conn", ID: "untitled", URL: "https://example.com/u"}); err != nil {
		t.Fatal(err)
	}
	src := &titledItems{items: []kbwire.Item{
		{Kind: KindDoc, ID: "viewed", Title: "Viewed", At: at, Signals: []string{sigViewed}},
		{Kind: KindDoc, ID: "mention", Title: "Mention", At: at, Signals: []string{sigViewed, sigMentions}},
		{Kind: KindDoc, ID: "plain", Title: "No signals", At: at},
		{Kind: KindDoc, ID: "kept", Title: "Kept", At: at, Signals: []string{sigViewed}},
		{Kind: KindDoc, ID: "never", Title: "Never", At: at, Signals: []string{sigMentions}},
		{Kind: KindDoc, ID: "old", Title: "Old, renamed", At: at.Add(time.Minute), Signals: []string{sigViewed}},
		{Kind: KindMeeting, ID: "m1", Title: "Meeting", At: at},
	}}
	if err := e.CollectItems(ctx, src, "conn"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"viewed": false, "mention": true, "plain": true, "kept": true, "never": false} {
		if _, ok := e.Store.Item(KindDoc, id); ok != want {
			t.Errorf("doc %s stored %v, want %v", id, ok, want)
		}
	}
	if _, ok := e.Store.Item(KindMeeting, "m1"); !ok {
		t.Error("a meeting is not a document: the selection does not decide about it")
	}
	if now, _ := os.ReadFile(e.Store.entityPath(KindDoc, "old")); string(now) != string(raw) {
		t.Errorf("a stored doc the selection no longer selects was rewritten:\n%s\nwant\n%s", now, raw)
	}
	if slices.Contains(src.titled, "https://example.com/u") {
		t.Error("looked up the title of a doc the user keeps from being read")
	}
}

// With the default document every doc is collected as before the selection.
func TestCollectItemsDefaultsKeepEverything(t *testing.T) {
	at := time.Date(2020, 1, 2, 3, 0, 0, 0, time.UTC) // old: documents had no retention, and the defaults add none
	e := docSelEngine(t, nil)
	var items []kbwire.Item
	for i, sig := range append([]string{""}, kbwire.Signals...) {
		w := kbwire.Item{Kind: KindDoc, ID: "d" + string(rune('a'+i)), Title: "Doc", At: at}
		if sig != "" {
			w.Signals = []string{sig}
		}
		items = append(items, w)
	}
	if err := e.CollectItems(context.Background(), &titledItems{items: items}, "conn"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.Store.Items(KindDoc)); n != len(items) {
		t.Fatalf("stored %d docs with the defaults, want %d", n, len(items))
	}
}

func seedDocs(t *testing.T, e *Engine, docs ...*Item) {
	t.Helper()
	for _, it := range docs {
		it.Kind = KindDoc
		if err := e.Store.PutItem(it); err != nil {
			t.Fatal(err)
		}
	}
}

func docIDs(st *Store) []string {
	var ids []string
	for _, it := range st.Items(KindDoc) {
		ids = append(ids, it.ID)
	}
	return ids
}

// SAFETY: with the default document Prune deletes no document, however old, with or without signals; a missing
// document (Selection returning nil) is the defaults too.
func TestPruneDefaultsDeleteNoDocument(t *testing.T) {
	now := time.Now().UTC()
	ancient := now.AddDate(-10, 0, 0)
	for _, s := range []*Selection{nil, sel(nil)} {
		e := docSelEngine(t, s)
		seedDocs(t, e,
			&Item{Source: "conn", ID: "a", At: ancient, Signals: kbwire.Signals},
			&Item{Source: "conn", ID: "b", At: ancient, Signals: []string{sigViewed}},
			&Item{Source: "conn", ID: "c", At: ancient},
			&Item{Source: "", ID: "d", Seen: ancient},
		)
		if n := e.Prune(now); n != 0 {
			t.Errorf("pruned %d with the defaults", n)
		}
		if got := docIDs(e.Store); len(got) != 4 {
			t.Errorf("docs after prune with the defaults: %v", got)
		}
	}
}

func TestPruneDocsByTheirDays(t *testing.T) {
	now := time.Now().UTC()
	old, recent := now.AddDate(0, 0, -45), now.AddDate(0, 0, -5)
	s := sel(func(s *Selection) {
		setDocRule(s, sigViewed, true, intp(30))
		setDocRule(s, sigComments, false, intp(1)) // off: its days do not apply
		s.SetItem(SelectionItem{Source: "conn", Kind: ItemDoc, ID: "item-days", Read: true, Store: true, Days: intp(3)})
		s.SetItem(SelectionItem{Source: "conn", Kind: ItemDoc, ID: "item-skip", Read: false})
	})
	e := docSelEngine(t, s)
	var unindexed []string
	e.Unindex = func(kind string, ids []string) {
		if kind == KindDoc {
			unindexed = append(unindexed, ids...)
		}
	}
	seedDocs(t, e,
		&Item{Source: "conn", ID: "viewed-old", At: old, Signals: []string{sigViewed}},
		&Item{Source: "conn", ID: "viewed-recent", At: recent, Signals: []string{sigViewed}},
		&Item{Source: "conn", ID: "seen-old", Seen: old, Signals: []string{sigViewed}},
		&Item{Source: "conn", ID: "no-time", Signals: []string{sigViewed}},
		&Item{Source: "conn", ID: "authored-old", At: old, Signals: []string{sigAuthored, sigViewed}},
		&Item{Source: "conn", ID: "comments-off", At: old, Signals: []string{sigComments}},
		&Item{Source: "conn", ID: "no-signals", At: old},
		&Item{Source: "conn", ID: "item-days", At: recent},
		&Item{Source: "conn", ID: "item-skip", At: old, Signals: []string{sigViewed}},
		&Item{Source: "conn", ID: "pinned", At: old, Signals: []string{sigViewed}, Tagging: Tagging{Pinned: "a fact"}},
		&Item{Source: "conn", ID: "tagged", At: old, Signals: []string{sigViewed}, Projects: []string{"p"}},
		&Item{Source: "conn", ID: "linked", At: old, Signals: []string{sigViewed}},
		&Item{Source: "conn", ID: "locked", At: old, Signals: []string{sigViewed}, Tagging: Tagging{Locked: true}},
		&Item{Source: "conn", ID: "locked-field", At: old, Signals: []string{sigViewed}, Tagging: Tagging{LockedFields: []string{"note"}}},
		&Item{Source: "conn", ID: "merged", At: old, Signals: []string{sigViewed}},
	)
	if err := e.Store.PutProject(&Project{Slug: "p", Name: "P", Items: []string{KindDoc + "/linked"}}); err != nil {
		t.Fatal(err)
	}
	// Held only by what a merge gave the project kept (the project merged away records it for unmerge).
	if err := e.Store.PutProject(&Project{Slug: "q", Name: "Q", Merge: &ProjectMerge{At: now, Items: []string{KindDoc + "/merged"}}}); err != nil {
		t.Fatal(err)
	}
	if n := e.Prune(now); n != 3 {
		t.Errorf("pruned %d, want 3", n)
	}
	want := []string{"authored-old", "comments-off", "item-skip", "linked", "locked", "locked-field", "merged", "no-signals", "no-time", "pinned", "tagged", "viewed-recent"}
	if got := docIDs(e.Store); !slices.Equal(got, want) {
		t.Errorf("docs left %v, want %v", got, want)
	}
	slices.Sort(unindexed)
	if want := []string{"item-days", "seen-old", "viewed-old"}; !slices.Equal(unindexed, want) {
		t.Errorf("unindexed %v, want %v", unindexed, want)
	}
	// An old doc the source still lists is not written again only to be pruned the next day.
	src := &titledItems{items: []kbwire.Item{
		{Kind: KindDoc, ID: "viewed-old", Title: "Old", At: old, Signals: []string{sigViewed}},
		{Kind: KindDoc, ID: "viewed-new", Title: "New", At: recent, Signals: []string{sigViewed}},
	}}
	if err := e.CollectItems(context.Background(), src, "conn"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Store.Item(KindDoc, "viewed-old"); ok {
		t.Error("an old doc past its days was written again")
	}
	if _, ok := e.Store.Item(KindDoc, "viewed-new"); !ok {
		t.Error("a new doc within its days was not written")
	}
}

// A selection that cannot be read now prunes no document either.
func TestPruneDocsStopgap(t *testing.T) {
	k := KeepEverything()
	for name := range k.Docs.Rules {
		k.Docs.Rules[name] = DocRule{On: true, Days: intp(1)}
	}
	e := docSelEngine(t, &k)
	seedDocs(t, e, &Item{Source: "conn", ID: "a", At: time.Now().AddDate(-1, 0, 0), Signals: []string{sigViewed}})
	if n := e.Prune(time.Now()); n != 0 || len(docIDs(e.Store)) != 1 {
		t.Errorf("pruned %d under a stopgap", n)
	}
}

// A document link in a chat writes no stub for a doc the user keeps from being read, nor brings back one past the
// days the user set for it; other links are noted as before.
func TestNoteDocFollowsTheSelection(t *testing.T) {
	now := time.Now().UTC()
	e := docSelEngine(t, sel(func(s *Selection) {
		s.SetItem(SelectionItem{Source: "conn", Kind: ItemDoc, ID: "never", Read: false})
		s.SetItem(SelectionItem{Source: "conn", Kind: ItemDoc, ID: "short", Read: true, Store: true, Days: intp(3)})
	}))
	chat := &Chat{ID: "c1", Connector: "conn"}
	e.noteDoc("https://example.com/d/never", chat, now)
	e.noteDoc("https://example.com/d/short", chat, now.AddDate(0, 0, -10)) // an old message: past its days
	e.noteDoc("https://example.com/d/fresh", chat, now.AddDate(0, 0, -10))
	if got, want := docIDs(e.Store), []string{"fresh"}; !slices.Equal(got, want) {
		t.Errorf("docs %v, want %v", got, want)
	}
	e.noteDoc("https://example.com/d/short", chat, now) // a recent link: within its days
	if _, ok := e.Store.Item(KindDoc, "short"); !ok {
		t.Error("a link within the doc's days was not noted")
	}
	// The defaults note every link, however old.
	d := docSelEngine(t, nil)
	d.noteDoc("https://example.com/d/never", chat, now.AddDate(-10, 0, 0))
	if _, ok := d.Store.Item(KindDoc, "never"); !ok {
		t.Error("the defaults dropped a document link")
	}
}

// An update built from a stored doc that was deleted before it was written (a prune between the read and the write)
// does not bring the doc back.
func TestCollectUpdateDoesNotResurrectADeletedDoc(t *testing.T) {
	e := docSelEngine(t, nil)
	seedDocs(t, e, &Item{Source: "conn", ID: "d", Title: "Old", At: time.Now().UTC()})
	old, ok := e.Store.Item(KindDoc, "d")
	if !ok {
		t.Fatal("seeded doc missing")
	}
	if err := os.Remove(e.Store.entityPath(KindDoc, "d")); err != nil {
		t.Fatal(err)
	}
	it := ItemFromWire(kbwire.Item{Kind: KindDoc, ID: "d", Title: "New", At: time.Now().UTC()})
	carryOver(&it, old)
	if err := e.Store.PutItem(&it); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.Store.Item(KindDoc, "d"); ok {
		t.Error("an update of a deleted doc brought it back")
	}
}
