package devices

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

func seqOf(m *Machine, id string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seq, ok := m.lastSeq[id]
	return seq, ok
}

// A session nobody watches any more keeps no last seq: on a long-running manager every session an app ever opened
// would stay otherwise, per machine. A batch that comes in late, after the unwatch, does not bring it back.
func TestUnwatchedSessionsForgetTheirSeq(t *testing.T) {
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "box", Name: "box"})
	for i := range 100 {
		id := json.RawMessage(fmt.Sprintf(`{"session_id":"s%d"}`, i))
		items := json.RawMessage(fmt.Sprintf(`{"session_id":"s%d","items":[{"seq":3}]}`, i))
		m.noteWatch("session.watch", id)
		m.onEvent("session.items", items)
		if seq, _ := seqOf(m, fmt.Sprintf("s%d", i)); seq != 3 {
			t.Fatalf("s%d while watched: seq %d, want 3", i, seq)
		}
		m.noteWatch("session.unwatch", id)
		m.onEvent("session.items", items) // late, after the unwatch
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.lastSeq) != 0 {
		t.Fatalf("%d seqs kept for unwatched sessions", len(m.lastSeq))
	}
}

// A session the manager follows keeps its seq when the hub stops watching it (Call answers that unwatch itself), and
// forgets it once the manager stops following too.
func TestAFollowedSessionKeepsItsSeqAfterTheHubUnwatches(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{}
	m := connectedMachine(t, d, "box", n)
	ctx := context.Background()
	if _, err := d.Call(ctx, devicewire.NodeCall{MachineID: "box", Method: "session.watch",
		Params: json.RawMessage(`{"session_id":"s1","since_seq":0}`)}); err != nil {
		t.Fatal(err)
	}
	if err := d.Follow(ctx, "box", "s1", true); err != nil {
		t.Fatal(err)
	}
	_ = n.current().Notify("session.items", map[string]any{"session_id": "s1", "items": []proto.Item{{Seq: 5}}})
	eventually(t, 2*time.Second, "the item seen", func() bool { seq, _ := seqOf(m, "s1"); return seq == 5 })
	if _, err := d.Call(ctx, devicewire.NodeCall{MachineID: "box", Method: "session.unwatch",
		Params: json.RawMessage(`{"session_id":"s1"}`)}); err != nil {
		t.Fatal(err)
	}
	if seq, _ := seqOf(m, "s1"); seq != 5 {
		t.Fatalf("followed after the hub's unwatch: seq %d, want 5", seq)
	}
	m.reconnect()
	eventually(t, 5*time.Second, "followed again from its seq", func() bool {
		return slices.Contains(n.seen("session.watch"), `session.watch {"session_id":"s1","since_seq":5}`)
	})
	if err := d.Follow(ctx, "box", "s1", false); err != nil {
		t.Fatal(err)
	}
	if seq, ok := seqOf(m, "s1"); ok {
		t.Fatalf("neither watched nor followed: seq %d kept", seq)
	}
}

// A session unwatched and watched again is asked since the seq of its new watch on the next connection, not since one
// it kept from before or one a late batch brought after the unwatch; once unwatched again it keeps none.
func TestARewatchedSessionAsksSinceItsSeqOnlyWhileWatched(t *testing.T) {
	d := newTestDevices(t)
	n := &testNode{}
	m := connectedMachine(t, d, "box", n)
	ctx := context.Background()
	call := func(method string) {
		t.Helper()
		if _, err := d.Call(ctx, devicewire.NodeCall{MachineID: "box", Method: method,
			Params: json.RawMessage(`{"session_id":"s1","since_seq":0}`)}); err != nil {
			t.Fatal(err)
		}
	}
	notify := func(seq int) {
		t.Helper()
		_ = n.current().Notify("session.items", map[string]any{"session_id": "s1", "items": []proto.Item{{Seq: seq}}})
	}
	seen := func(seq int) {
		t.Helper()
		eventually(t, 2*time.Second, "the item seen", func() bool { got, _ := seqOf(m, "s1"); return got == seq })
	}
	call("session.watch")
	notify(4)
	seen(4)
	call("session.unwatch")
	if seq, ok := seqOf(m, "s1"); ok {
		t.Fatalf("unwatched: seq %d kept", seq)
	}
	notify(9) // a late batch, sent before the node took the unwatch
	call("session.watch")
	notify(6) // the new watch's catch-up; events arrive in order, so the late batch was handled before it
	seen(6)
	m.reconnect()
	eventually(t, 5*time.Second, "watched again from its new seq", func() bool {
		return slices.Contains(n.seen("session.watch"), `session.watch {"session_id":"s1","since_seq":6}`)
	})
	call("session.unwatch")
	if seq, ok := seqOf(m, "s1"); ok {
		t.Fatalf("unwatched again: seq %d kept", seq)
	}
}
