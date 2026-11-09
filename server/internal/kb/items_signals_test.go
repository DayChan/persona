package kb

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

type signalItems struct{ items []kbwire.Item }

func (s *signalItems) Items(context.Context) ([]kbwire.Item, error) { return s.items, nil }
func (s *signalItems) DocTitle(context.Context, string) string      { return "" }

// A document's signals are kept as the source last reported them; a change of signals alone does not tag it again.
func TestCollectItemsKeepsTheLatestSignals(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: st}
	at := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	src := &signalItems{items: []kbwire.Item{{Kind: KindDoc, ID: "d1", Title: "Example doc", At: at, Signals: []string{kbwire.SignalViewed}}}}
	if err := e.CollectItems(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	it, ok := st.Item(KindDoc, "d1")
	if !ok || !slices.Equal(it.Signals, []string{kbwire.SignalViewed}) {
		t.Fatalf("first collect: %+v", it)
	}
	tagged := at.Add(time.Hour)
	it.TaggedAt, it.Note = tagged, "a note"
	if err := st.PutItem(it); err != nil {
		t.Fatal(err)
	}

	src.items[0].Signals = []string{kbwire.SignalAuthored, kbwire.SignalViewed}
	if err := e.CollectItems(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	it, _ = st.Item(KindDoc, "d1")
	if !slices.Equal(it.Signals, []string{kbwire.SignalAuthored, kbwire.SignalViewed}) {
		t.Fatalf("signals after the second collect: %v", it.Signals)
	}
	if !it.TaggedAt.Equal(tagged) || it.Note != "a note" {
		t.Fatalf("a change of signals alone tagged it again or lost its note: %+v", it)
	}

	src.items[0].Signals = nil
	if err := e.CollectItems(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if it, _ = st.Item(KindDoc, "d1"); len(it.Signals) != 0 || it.Note != "a note" {
		t.Fatalf("a source that no longer reports signals: %+v", it)
	}
}

// Signals are kept canonical (each known one once, in kbwire.Signals' order): a source that lists the same ones in
// another order, or repeats one, changes nothing, so a collect neither rewrites the item nor tells of a change.
func TestCollectItemsSignalsAreASet(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var changes []Change
	e := &Engine{Store: st, Changed: func(c Change) { changes = append(changes, c) }}
	e.ObserveChanges()
	at := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	src := &signalItems{items: []kbwire.Item{{Kind: KindDoc, ID: "d1", Title: "Example doc", At: at,
		Signals: []string{kbwire.SignalViewed, "unknown", kbwire.SignalMentions, kbwire.SignalViewed}}}}
	if err := e.CollectItems(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	it, _ := st.Item(KindDoc, "d1")
	if want := []string{kbwire.SignalMentions, kbwire.SignalViewed}; !slices.Equal(it.Signals, want) {
		t.Fatalf("stored signals %v, want %v", it.Signals, want)
	}
	before := len(changes)
	src.items[0].Signals = []string{kbwire.SignalMentions, kbwire.SignalViewed, kbwire.SignalMentions}
	if err := e.CollectItems(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	src.items[0].Signals = []string{kbwire.SignalViewed, kbwire.SignalMentions}
	if err := e.CollectItems(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	if len(changes) != before {
		t.Fatalf("the same signals in another order were taken as a change: %v", changes[before:])
	}
}

func TestCanonicalSignals(t *testing.T) {
	for _, c := range []struct{ in, want []string }{
		{nil, nil},
		{[]string{"unknown"}, nil},
		{[]string{kbwire.SignalViewed, kbwire.SignalAuthored, kbwire.SignalViewed}, []string{kbwire.SignalAuthored, kbwire.SignalViewed}},
		{[]string{kbwire.SignalViewed, kbwire.SignalCommentMentions, kbwire.SignalAuthored, kbwire.SignalMentions}, kbwire.Signals},
	} {
		if got := kbwire.CanonicalSignals(c.in); !slices.Equal(got, c.want) {
			t.Errorf("CanonicalSignals(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
