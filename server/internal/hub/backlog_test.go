package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeNode answers session.items after_seq from n items (seq 1..n, one item per line) the way the node does: at most
// 500 per call, more_after when items are left.
func fakeNode(t *testing.T, n int) *Machine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide := rpc.New("h", h2nW)
	nodeSide := rpc.New("n", n2hW)
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var p struct {
			AfterSeq *int `json:"after_seq"`
		}
		_ = json.Unmarshal(params, &p)
		if method != "session.items" || p.AfterSeq == nil {
			return nil, fmt.Errorf("unexpected %s %s", method, params)
		}
		var items []proto.Item
		for s := *p.AfterSeq + 1; s <= n && len(items) < 500; s++ {
			items = append(items, proto.Item{ID: fmt.Sprintf("i%d", s), Seq: s, Role: "assistant", Kind: proto.KindText})
		}
		more := len(items) > 0 && items[len(items)-1].Seq < n
		to := n
		if more {
			to = items[len(items)-1].Seq
		}
		return map[string]any{"items": items, "has_more": false, "more_after": more, "history": "h1", "from_seq": *p.AfterSeq, "to_seq": to}, nil
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(&Hub{}, MachineConfig{ID: "m1", Name: "m1"})
	m.peer = hubSide
	return m
}

type batch struct {
	Items     []proto.Item `json:"items"`
	MoreAfter bool         `json:"more_after"`
	HasMore   *bool        `json:"has_more"`
	History   string       `json:"history"`
	FromSeq   *int         `json:"from_seq"`
	ToSeq     *int         `json:"to_seq"`
}

func drain(t *testing.T, c *client) []batch {
	t.Helper()
	var out []batch
	for {
		select {
		case b := <-c.out:
			var x batch
			if err := json.Unmarshal(b, &x); err != nil {
				t.Fatal(err)
			}
			out = append(out, x)
		default:
			return out
		}
	}
}

func TestBacklogCatchUpSendsEverything(t *testing.T) {
	m := fakeNode(t, 1300)
	c := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}}
	(&Hub{}).sendBacklog(context.Background(), c, m, "s1", 10)
	batches := drain(t, c)
	next, covered := 11, 10
	for _, b := range batches {
		if b.MoreAfter || b.HasMore != nil {
			t.Fatalf("a complete catch-up says more_after %v / has_more %v", b.MoreAfter, b.HasMore)
		}
		// Every batch carries its span, and continues exactly where the previous one ended.
		if b.History != "h1" || b.FromSeq == nil || b.ToSeq == nil || *b.FromSeq != covered {
			t.Fatalf("batch span history %q from %v to %v, want from %d", b.History, b.FromSeq, b.ToSeq, covered)
		}
		covered = *b.ToSeq
		for _, it := range b.Items {
			if it.Seq != next {
				t.Fatalf("got seq %d, want %d", it.Seq, next)
			}
			next++
		}
	}
	if next != 1301 {
		t.Fatalf("catch-up ended at seq %d, want everything through 1300 (used to stop after 500)", next-1)
	}
}

func TestBacklogCatchUpCapsAndSaysSo(t *testing.T) {
	m := fakeNode(t, 10000)
	c := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}}
	(&Hub{}).sendBacklog(context.Background(), c, m, "s1", 1)
	batches := drain(t, c)
	if len(batches) != backlogPages {
		t.Fatalf("%d batches, want %d", len(batches), backlogPages)
	}
	if !batches[len(batches)-1].MoreAfter {
		t.Fatal("the last batch of a capped catch-up must say more_after")
	}
	for _, b := range batches[:len(batches)-1] {
		if b.MoreAfter {
			t.Fatal("only the last batch says more_after")
		}
	}
}

// A runtime's capped rewatch replay arrives as live events rather than through sendBacklog. Its final batch must
// still tell the subscriber to fetch the rest instead of looking like a complete, contiguous catch-up.
func TestRuntimeItemsForwardMoreAfter(t *testing.T) {
	m := newMachine(&Hub{}, MachineConfig{ID: "m1", Name: "m1"})
	c := &client{out: make(chan []byte, 2), subs: map[[2]string]bool{{"m1", "s1"}: true}}
	h := &Hub{clients: map[*client]struct{}{c: {}}}
	m.hub = h
	from, to := 3501, 4001
	m.onEvent("session.items", json.RawMessage(`{"session_id":"s1","items":[],"history":"h1","from_seq":3501,"to_seq":4001,"more_after":true}`))
	batches := drain(t, c)
	if len(batches) != 1 || !batches[0].MoreAfter {
		t.Fatalf("a capped runtime batch lost more_after: %+v", batches)
	}
	if batches[0].History != "h1" || batches[0].FromSeq == nil || *batches[0].FromSeq != from ||
		batches[0].ToSeq == nil || *batches[0].ToSeq != to || m.lastSeq["s1"] != to {
		t.Fatalf("the capped batch's continuity changed: %+v, last seq %d", batches[0], m.lastSeq["s1"])
	}
}

// An app that subscribes with fold_work gets the latest page with the agent's work folded; a catch-up (since_seq)
// is not folded: its items continue what the app holds, calls whose results it is waiting for included.
func TestBacklogFoldsOnlyTheLatestPage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide := rpc.New("h", h2nW)
	nodeSide := rpc.New("n", n2hW)
	asked := make(chan string, 4)
	nodeSide.OnRequest = func(_ context.Context, _ string, params json.RawMessage) (any, error) {
		asked <- string(params)
		return map[string]any{"items": []proto.Item{}, "history": "h1", "to_seq": 1}, nil
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(&Hub{}, MachineConfig{ID: "m1", Name: "m1"})
	m.peer = hubSide
	for _, tc := range []struct {
		fold  bool
		since int
		want  bool
	}{{true, 0, true}, {true, 5, false}, {false, 0, false}} {
		c := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, fold: tc.fold}
		(&Hub{}).sendBacklog(context.Background(), c, m, "s1", tc.since)
		var p struct {
			FoldWork bool `json:"fold_work"`
		}
		_ = json.Unmarshal([]byte(<-asked), &p)
		if p.FoldWork != tc.want {
			t.Fatalf("subscribed with fold_work %v, since %d: asked the node with fold_work %v", tc.fold, tc.since, p.FoldWork)
		}
	}
}
