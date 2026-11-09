package hub

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/coder/websocket"
)

// gatedSocket is a connection whose first write hangs until gate closes, as a slow client's does.
type gatedSocket struct {
	gate chan struct{}

	mu      sync.Mutex
	written []int64 // event_seq of each message written, 0 for a message without one
}

func (s *gatedSocket) Write(ctx context.Context, _ websocket.MessageType, b []byte) error {
	s.mu.Lock()
	first := len(s.written) == 0
	var msg struct {
		Seq int64 `json:"event_seq"`
	}
	_ = json.Unmarshal(b, &msg)
	s.written = append(s.written, msg.Seq)
	s.mu.Unlock()
	if first {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *gatedSocket) Ping(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (s *gatedSocket) seqs() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.written...)
}

// A client dropped for falling behind (queue, dropSlow) lost the event that did not fit. Nothing after that gap may be
// written to it while its close is under way: the client's event_seq would move past the lost event and resuming from
// it would skip that event for good (docs/protocol/protocol.md "Event resume": nothing missed).
func TestHeartbeatDroppedClientIsWrittenNothingPastTheGap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		h := &Hub{clients: map[*client]struct{}{}}
		c := &client{out: make(chan []byte, 2), meta: clientMeta{updates: make(chan heartbeatConfig), done: ctx.Done()}}
		h.clients[c] = struct{}{}
		s := &gatedSocket{gate: make(chan struct{})}
		go func() { _ = c.writeWS(ctx, s, func(heartbeatConfig) {}) }()
		send := func() {
			h.broadcastRaw("notice", []byte(`{"type":"notice"}`))
			synctest.Wait()
		}

		send() // 1: taken by the writer, whose write hangs
		send() // 2 and 3 fill the queue
		send()
		if c.dropped.Load() {
			t.Fatal("dropped before the queue was full")
		}
		send() // 4 does not fit: lost, the client is dropped
		if !c.dropped.Load() {
			t.Fatal("a client whose queue was full was not dropped")
		}
		close(s.gate)
		synctest.Wait()
		send() // 5 would fit again now that the writer drains the queue

		got := s.seqs()
		for i, seq := range got {
			if seq != int64(i+1) {
				t.Fatalf("written event_seqs %v: not contiguous from 1, a resume from the last would skip what is missing", got)
			}
		}
		// Only the write already under way when the client was dropped goes out.
		if len(got) != 1 {
			t.Fatalf("written event_seqs %v after the drop, want only [1], the write in flight", got)
		}
	})
}
