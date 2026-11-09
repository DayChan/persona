package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// #3036: a push that finds a client's queue full used to be dropped without a word, so the tail of a burst never
// reached a page that was behind, and nothing told it (the next batch would, but the agent had stopped). A client that
// cannot take what it is sent is now dropped like on every other path: it reconnects and asks again from its cursor.
func TestItemsBurstPastTheQueueNeverLeavesAGap(t *testing.T) {
	c := &client{out: make(chan []byte, 4), subs: map[[2]string]bool{{"m1", "s1"}: true}}
	h := &Hub{clients: map[*client]struct{}{c: {}}}
	const batches = 50
	for i := 0; i < batches; i++ {
		from, to := i, i+1
		h.itemsToSubscribers("m1", "s1", []proto.Item{{ID: fmt.Sprintf("i%d", i+1), Seq: i + 1, Role: "assistant", Kind: proto.KindText}}, itemSpan{History: "h1", FromSeq: &from, ToSeq: &to}, false)
	}
	got := 0
	for len(c.out) > 0 {
		var e struct {
			Items []proto.Item `json:"items"`
		}
		_ = json.Unmarshal(<-c.out, &e)
		got += len(e.Items)
	}
	if got != batches && !c.dropped.Load() {
		t.Fatalf("the client got %d of %d items and was not dropped: the rest are gone with no way to know", got, batches)
	}
}

// Live text and status lines are not kept, but a client too slow for them is just as behind: same rule.
func TestLiveTextPastTheQueueDropsTheClient(t *testing.T) {
	c := &client{out: make(chan []byte, 2), subs: map[[2]string]bool{{"m1", "s1"}: true}}
	h := &Hub{clients: map[*client]struct{}{c: {}}}
	for i := 0; i < 20; i++ {
		h.toSubscribers("m1", "s1", map[string]any{"type": "delta", "machine_id": "m1", "session_id": "s1", "text": strings.Repeat("x", i)})
	}
	if !c.dropped.Load() {
		t.Fatal("a client 20 pushes behind a queue of 2 is still being sent to")
	}
}

// A page that subscribed with the id an absorbed hub.json entry used to have (a bookmarked link, a saved selection)
// reaches the computer, so it must hear it: the subscription is kept under the computer's own id, which is what the
// pushes carry.
func TestSubscribeByAbsorbedAliasHearsThePushes(t *testing.T) {
	h := newTestHub(t)
	boardNode(t, h, "canonical", func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method == "session.items" {
			return map[string]any{"items": []proto.Item{}, "history": "h1", "to_seq": 0}, nil
		}
		return map[string]any{"ok": true}, nil
	})
	h.devices.absorbed = map[string]string{"alias": "canonical"}
	c := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}}
	h.clients[c] = struct{}{}
	h.onClientMessage(context.Background(), c, []byte(`{"type":"subscribe","machine_id":"alias","session_id":"s1"}`))
	h.itemsToSubscribers("canonical", "s1", []proto.Item{{ID: "i1", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: "live"}}, itemSpan{History: "h1"}, false)
	deadline := time.After(2 * time.Second)
	for {
		select {
		case b := <-c.out:
			if strings.Contains(string(b), `"live"`) {
				h.onClientMessage(context.Background(), c, []byte(`{"type":"unsubscribe","machine_id":"alias","session_id":"s1"}`))
				if len(c.subs) != 0 {
					t.Fatalf("unsubscribe by the alias left %v", c.subs)
				}
				return
			}
		case <-deadline:
			t.Fatalf("a page subscribed by an absorbed id never heard the push; subscriptions %v", c.subs)
		}
	}
}
