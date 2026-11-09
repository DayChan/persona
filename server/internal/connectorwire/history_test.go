package connectorwire

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// The history types keep every field across the wire, under the snake_case names the protocol documents, and leave
// out what is empty.
func TestHistoryWireRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 4, 8, 24, 0, 0, time.UTC)
	hit := kbwire.SourceMessage{ID: "m9", Position: "9", SenderName: "Example B", Text: "a mention", At: at, Mentioned: true}
	for _, c := range []struct {
		v    any
		want string
	}{
		{HistoryChat{ID: "c1", Name: "Example team", Kind: HistoryKindGroup, Muted: true, Bot: true, Estimate: 1200, Source: "examplefeed"},
			`{"id":"c1","name":"Example team","kind":"group","muted":true,"bot":true,"estimate":1200,"source":"examplefeed"}`},
		{HistoryChat{ID: "c2", Name: "Example A", Kind: HistoryKindDM}, `{"id":"c2","name":"Example A","kind":"dm"}`},
		{HistoryChat{ID: "c3", Name: "Example box", Kind: HistoryKindOfficial, Collapsed: true, Done: true},
			`{"id":"c3","name":"Example box","kind":"official","collapsed":true,"done":true}`},
		{ChatBatch{Chat: kbwire.SourceChat{ID: "c1", Name: "Example team", Kind: "group", Topic: true, Muted: true, Collapsed: true, Done: true, UpdatedAt: at},
			Messages: []kbwire.SourceMessage{hit}},
			`{"chat":{"id":"c1","name":"Example team","kind":"group","topic":true,"muted":true,"collapsed":true,"done":true,` +
				`"updated_at":"2026-11-04T08:24:00Z"},"messages":[{"id":"m9","position":"9","sender_name":"Example B","mentioned":true,` +
				`"text":"a mention","at":"2026-11-04T08:24:00Z"}]}`},
		{ChatBatch{Chat: kbwire.SourceChat{ID: "c1", Name: "Example team", Kind: "group", UpdatedAt: at},
			Messages: []kbwire.SourceMessage{{ID: "m10", Text: "to everyone", At: at, MentionedAll: true}, {ID: "m11", Text: "both", At: at, Mentioned: true, MentionedAll: true}}},
			`{"chat":{"id":"c1","name":"Example team","kind":"group","updated_at":"2026-11-04T08:24:00Z"},"messages":[{"id":"m10","mentioned_all":true,"text":"to everyone",` +
				`"at":"2026-11-04T08:24:00Z"},{"id":"m11","mentioned":true,"mentioned_all":true,"text":"both","at":"2026-11-04T08:24:00Z"}]}`},
		{HistoryQuery{Chat: "c1", Around: "17", Limit: 20}, `{"chat":"c1","around":"17","limit":20}`},
		{HistoryQuery{Chat: "c1"}, `{"chat":"c1"}`},
		{HistoryPage{Messages: []kbwire.SourceMessage{hit}, Older: "8", Newer: "10", More: true,
			Pace: &Pace{RetryAfterMS: 30000, Stop: true, Reason: "sign in again"}},
			`{"messages":[{"id":"m9","position":"9","sender_name":"Example B","mentioned":true,"text":"a mention","at":"2026-11-04T08:24:00Z"}],` +
				`"older":"8","newer":"10","more":true,"pace":{"retry_after_ms":30000,"stop":true,"reason":"sign in again"}}`},
		{HistoryPage{Unreadable: true}, `{"unreadable":true}`},
		{HistoryPage{}, `{}`},
		{HistoryAnchorsQuery{Chat: "c1", Since: at, Until: at.Add(time.Hour), Cursor: "n1", Limit: 50},
			`{"chat":"c1","since":"2026-11-04T08:24:00Z","until":"2026-11-04T09:24:00Z","cursor":"n1","limit":50}`},
		{HistoryAnchorsQuery{}, `{}`},
		{HistoryAnchors{Anchors: []HistoryAnchor{{Chat: "c1", Position: "9", At: at, Mention: true, Message: &hit}, {Chat: "c2", Position: "3", Mine: true}},
			Next: "n2", More: true, Pace: &Pace{RetryAfterMS: 1000}},
			`{"anchors":[{"chat":"c1","position":"9","at":"2026-11-04T08:24:00Z","mention":true,"message":{"id":"m9","position":"9",` +
				`"sender_name":"Example B","mentioned":true,"text":"a mention","at":"2026-11-04T08:24:00Z"}},{"chat":"c2","position":"3","mine":true}],` +
				`"next":"n2","more":true,"pace":{"retry_after_ms":1000}}`},
	} {
		raw, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != c.want {
			t.Errorf("%T on the wire:\n got %s\nwant %s", c.v, raw, c.want)
		}
		back := reflect.New(reflect.TypeOf(c.v))
		if err := json.Unmarshal(raw, back.Interface()); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back.Elem().Interface(), c.v) {
			t.Errorf("%T back from the wire: %+v, want %+v", c.v, back.Elem().Interface(), c.v)
		}
	}
}

func TestHistoryQueryCheck(t *testing.T) {
	for _, c := range []struct {
		q  HistoryQuery
		ok bool
	}{
		{HistoryQuery{Chat: "c1"}, true},
		{HistoryQuery{Chat: "c1", Before: "8", Limit: 20}, true},
		{HistoryQuery{Chat: "c1", After: "8"}, true},
		{HistoryQuery{Chat: "c1", Around: "8"}, true},
		{HistoryQuery{}, false},
		{HistoryQuery{Chat: "c1", Before: "8", After: "9"}, false},
		{HistoryQuery{Chat: "c1", Before: "8", Around: "9"}, false},
		{HistoryQuery{Chat: "c1", Limit: -1}, false},
	} {
		err := c.q.Check()
		if (err == nil) != c.ok || (err != nil && !errors.Is(err, ErrQuery)) {
			t.Errorf("%+v: %v", c.q, err)
		}
	}
	if q := (HistoryQuery{Chat: "c1", Limit: 5000}).Clamped(); q.Limit != HistoryMost {
		t.Errorf("clamped to %d", q.Limit)
	}
	if q := (HistoryQuery{Chat: "c1", Limit: 0}).Clamped(); q.Limit != 0 {
		t.Errorf("no limit became %d (0 is the source's own default)", q.Limit)
	}
	at := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	if err := (HistoryAnchorsQuery{Since: at, Until: at.Add(-time.Hour)}).Check(); !errors.Is(err, ErrQuery) {
		t.Error("a window ending before it starts passed")
	}
	if err := (HistoryAnchorsQuery{Since: at}).Check(); err != nil {
		t.Error(err)
	}
	if q := (HistoryAnchorsQuery{Limit: 999}).Clamped(); q.Limit != HistoryMost {
		t.Errorf("anchors clamped to %d", q.Limit)
	}
}

// A paced or unreadable page is as empty as the start of a conversation, with more false: only the last is the start,
// or a driver that took a 429 for it would mark the conversation done and never read further back.
func TestHistoryPageAtStart(t *testing.T) {
	msg := []kbwire.SourceMessage{{ID: "m1", Position: "0"}}
	for _, c := range []struct {
		name  string
		page  HistoryPage
		start bool
	}{
		{"nothing older", HistoryPage{Messages: msg}, true},
		{"an empty page", HistoryPage{}, true},
		{"an empty page saying more", HistoryPage{More: true}, true},
		{"more to read", HistoryPage{Messages: msg, Older: "c", More: true}, false},
		{"paced, empty", HistoryPage{Pace: &Pace{RetryAfterMS: 30000}}, false},
		{"stopped, empty", HistoryPage{Pace: &Pace{Stop: true, Reason: "signed out"}}, false},
		{"paced with its last messages", HistoryPage{Messages: msg, Pace: &Pace{RetryAfterMS: 1000}}, false},
		{"unreadable", HistoryPage{Unreadable: true}, false},
	} {
		if got := c.page.AtStart(); got != c.start {
			t.Errorf("%s: AtStart %v, want %v", c.name, got, c.start)
		}
	}
}
