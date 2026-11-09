package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
)

// keyedViewsStore adds the key reads; its xmin stands in for Postgres's by changing whenever a card's contents do.
type keyedViewsStore struct {
	fakeViewsStore
	read int // cards read by id
}

func fakeXmin(v db.AssistantViewRow) uint32 { return crc32.ChecksumIEEE(v.View) }

func (f *keyedViewsStore) AssistantViewKeys(_ context.Context, c string) ([]db.AssistantViewKey, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	out := []db.AssistantViewKey{}
	for _, v := range f.views {
		if v.Conversation == c {
			out = append(out, db.AssistantViewKey{ID: v.ID, Version: v.Version, Xmin: fakeXmin(v)})
		}
	}
	return out, nil
}

func (f *keyedViewsStore) AssistantViewsByID(_ context.Context, c string, ids []string) ([]db.AssistantViewKeyed, error) {
	f.lock.Lock()
	defer f.lock.Unlock()
	out := []db.AssistantViewKeyed{}
	for _, id := range ids {
		if v, ok := f.views[id]; ok && v.Conversation == c {
			f.read++
			out = append(out, db.AssistantViewKeyed{AssistantViewRow: v, Xmin: fakeXmin(v)})
		}
	}
	return out, nil
}

func (f *keyedViewsStore) put(v db.AssistantViewRow) {
	f.lock.Lock()
	defer f.lock.Unlock()
	if f.views == nil {
		f.views = map[string]db.AssistantViewRow{}
	}
	f.views[v.ID] = v
}

// snapshotJSON is the views snapshot as the feed GET writes it (apierr.WriteJSON).
func snapshotJSON(t *testing.T, views any) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := json.NewEncoder(&b).Encode(map[string]any{"views": views}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// The snapshot is byte for byte what decoding every card into wire.AssistantView wrote, from both stores and from the
// memo, and a card that does not decode fails the request without being cached.
func TestViewsSnapshotSameBytes(t *testing.T) {
	at := time.Date(2026, 11, 1, 8, 0, 0, 123456000, time.UTC)
	card := func(id, view string) db.AssistantViewRow {
		return db.AssistantViewRow{ID: id, Conversation: "conversation-a", MachineID: "server", View: json.RawMessage(view), Version: 3, CreatedAt: at, UpdatedAt: at.Add(time.Minute)}
	}
	other := card("view_other", `{"fallback":"Elsewhere","sections":[]}`)
	other.Conversation = "conversation-b"
	for _, tc := range []struct {
		name  string
		cards []db.AssistantViewRow
		bad   bool
	}{
		{name: "empty"},
		{name: "several", cards: []db.AssistantViewRow{
			card("view_b", `{"title":"Café ✓","fallback":"Build 2/3","sections":[{"items":[{"type":"progress","label":"Build","ratio":0.66}]}]}`),
			card("view_a", `{"fallback":"Pick one","sections":[{"items":[{"type":"row","title":"A","action":{"id":"a","reply":"Choose A"}}]}],"unknown":1}`),
			other,
		}},
		{name: "escaping", cards: []db.AssistantViewRow{card("view_html", `{"fallback":"a < b && c > d","sections":[{"items":[{"type":"html","html":"<p>&amp;</p> "}]}]}`)}},
		{name: "undecodable", cards: []db.AssistantViewRow{card("view_bad", `{"fallback":1,"sections":[]}`)}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := map[string]wire.AssistantView{}
			var wantErr error
			for _, row := range tc.cards {
				v, err := storedView(row)
				if err != nil {
					wantErr = err
				}
				if row.Conversation == "conversation-a" {
					want[row.ID] = v
				}
			}
			full, keyed := &fakeViewsStore{}, &keyedViewsStore{}
			for _, row := range tc.cards {
				_ = full.CreateAssistantView(context.Background(), row, db.FeedRow{})
				keyed.put(row)
			}
			f := newFakeHub().withMachine()
			for _, s := range []*Service{newServiceOn(t, f, full), newServiceOn(t, f, keyed), newServiceOn(t, f, keyed)} {
				for round := 0; round < 2; round++ {
					got, err := s.conversationViews(context.Background(), "conversation-a")
					if tc.bad {
						if err == nil || err.Error() != wantErr.Error() || len(s.viewMemo.index) != 0 {
							t.Fatalf("error %v, want %v; memo %d", err, wantErr, len(s.viewMemo.index))
						}
						continue
					}
					if err != nil || !bytes.Equal(snapshotJSON(t, got), snapshotJSON(t, want)) {
						t.Fatalf("round %d: %s\nwant %s (%v)", round, snapshotJSON(t, got), snapshotJSON(t, want), err)
					}
				}
			}
			if cards := len(want); !tc.bad && keyed.read != 2*cards {
				t.Fatalf("read %d cards by id for %d cards in two services: the memo was not used", keyed.read, cards)
			}
		})
	}
}

// A changed card is read again, a deleted one leaves the snapshot, and only those reach the database.
func TestViewsMemoRereadsChanges(t *testing.T) {
	st := &keyedViewsStore{}
	at := time.Now().UTC()
	for _, id := range []string{"view_a", "view_b", "view_c"} {
		st.put(db.AssistantViewRow{ID: id, Conversation: "conversation-a", MachineID: "server", View: json.RawMessage(`{"fallback":"` + id + `","sections":[]}`), Version: 1, CreatedAt: at, UpdatedAt: at})
	}
	s := newServiceOn(t, newFakeHub().withMachine(), st)
	ctx := context.Background()
	if _, err := s.conversationViews(ctx, "conversation-a"); err != nil || st.read != 3 {
		t.Fatalf("cold read %d, %v", st.read, err)
	}
	if _, err := st.UpdateAssistantView(ctx, "conversation-a", "view_a", 1, json.RawMessage(`{"fallback":"Changed","sections":[]}`), at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// Same version, new contents: only xmin tells.
	rewritten := st.views["view_b"]
	rewritten.View = json.RawMessage(`{"fallback":"Repaired","sections":[]}`)
	st.put(rewritten)
	st.lock.Lock()
	delete(st.views, "view_c")
	st.lock.Unlock()
	got, err := s.conversationViews(ctx, "conversation-a")
	if err != nil || st.read != 5 || len(got) != 2 || !bytes.Contains(got["view_a"], []byte(`"Changed"`)) || !bytes.Contains(got["view_b"], []byte(`"Repaired"`)) {
		t.Fatalf("read %d, %s, %v", st.read, snapshotJSON(t, got), err)
	}
	if cards := s.viewMemo.index["conversation-a"].Value.(*viewMemoConversation).cards; len(cards) != 2 {
		t.Fatalf("memo kept %d cards", len(cards))
	}
}

// The memo stays under its byte cap by dropping the least recently read conversations.
func TestViewsMemoEvicts(t *testing.T) {
	st := &keyedViewsStore{}
	at := time.Now().UTC()
	for i := 0; i < 3; i++ {
		st.put(db.AssistantViewRow{ID: fmt.Sprintf("view_%d", i), Conversation: fmt.Sprintf("conversation-%d", i), MachineID: "server", View: json.RawMessage(`{"fallback":"Example","sections":[]}`), Version: 1, CreatedAt: at, UpdatedAt: at})
	}
	s := newServiceOn(t, newFakeHub().withMachine(), st)
	ctx := context.Background()
	one, err := s.conversationViews(ctx, "conversation-0")
	if err != nil {
		t.Fatal(err)
	}
	s.viewMemo.most = 2 * (len("view_0") + len(one["view_0"]))
	for _, c := range []string{"conversation-1", "conversation-0", "conversation-2"} {
		if _, err := s.conversationViews(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	m := &s.viewMemo
	if _, kept := m.index["conversation-1"]; kept || len(m.index) != 2 || m.bytes > m.most || m.lru.Len() != 2 {
		t.Fatalf("memo %d conversations, %d of %d bytes, conversation-1 kept %v", len(m.index), m.bytes, m.most, kept)
	}
	before := st.read
	if _, err := s.conversationViews(ctx, "conversation-1"); err != nil || st.read != before+1 {
		t.Fatalf("evicted conversation read %d cards, %v", st.read-before, err)
	}
}
