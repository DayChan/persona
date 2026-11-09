package kbwire

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

// An item's signals cross the wire under "signals" and are left out when there are none.
func TestItemSignalsOnTheWire(t *testing.T) {
	at := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		it   Item
		want string // the signals key's JSON, "" when absent
	}{
		{Item{Kind: "docs", ID: "d1", Title: "Example doc", Seen: at, Signals: []string{SignalCommentMentions, SignalViewed}},
			`["comment_mentions","viewed"]`},
		{Item{Kind: "docs", ID: "d2", Title: "Example doc", At: at, Seen: at}, ""},
	} {
		raw, err := json.Marshal(c.it)
		if err != nil {
			t.Fatal(err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(raw, &keys); err != nil {
			t.Fatal(err)
		}
		if got := string(keys["signals"]); got != c.want {
			t.Errorf("%s: signals on the wire %q, want %q", raw, got, c.want)
		}
		var back Item
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back, c.it) {
			t.Errorf("back from the wire: %+v, want %+v", back, c.it)
		}
	}
}

// Signals lists each signal constant once.
func TestSignalsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Signals {
		if s == "" || seen[s] {
			t.Fatalf("signal %q empty or twice in %v", s, Signals)
		}
		seen[s] = true
	}
	if len(Signals) != 4 {
		t.Fatalf("Signals = %v, want mentions, comment_mentions, authored, viewed", Signals)
	}
}

// A chat's flags cross the wire and are left out when false.
func TestSourceChatFlagsOnTheWire(t *testing.T) {
	at := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	c := SourceChat{ID: "c1", Name: "Example team", Kind: "group", Muted: true, Collapsed: true, Done: true, UpdatedAt: at}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"id":"c1","name":"Example team","kind":"group","muted":true,"collapsed":true,"done":true,"updated_at":"2026-11-04T08:00:00Z"}`; string(raw) != want {
		t.Fatalf("got %s\nwant %s", raw, want)
	}
	raw, _ = json.Marshal(SourceChat{ID: "c2", Name: "Example A", Kind: "official", UpdatedAt: at})
	if want := `{"id":"c2","name":"Example A","kind":"official","updated_at":"2026-11-04T08:00:00Z"}`; string(raw) != want {
		t.Fatalf("got %s\nwant %s", raw, want)
	}
}
