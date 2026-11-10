package voice

import (
	"context"
	"io"
	"log"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// stuckLink is an upstream session that stays connected but takes no frame: a write waits until its context ends, as
// a WebSocket whose peer stopped reading does once the buffers are full. Then it runs broke (the socket closing
// itself on the deadline, and the session's reader noticing it) and fails.
type stuckLink struct {
	broke func()
}

func (l *stuckLink) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}

func (l *stuckLink) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	<-ctx.Done()
	if l.broke != nil {
		l.broke()
	}
	return ctx.Err()
}

func (l *stuckLink) Close(websocket.StatusCode, string) error { return nil }
func (l *stuckLink) CloseNow() error                          { return nil }

// returnsSoon fails the test unless f returns well before the call's own context would end it (the test's end).
func returnsSoon(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s still waits on an upstream that takes no frame", what)
	}
}

// A line for an upstream session that stopped reading gives up after upWriteTimeout instead of waiting for the call
// to end: the speaker and the app's reader that write to it go on, and the app hanging up is seen.
func TestSendUpGivesUpOnAStuckUpstream(t *testing.T) {
	setVar(t, &upWriteTimeout, 100*time.Millisecond)
	c := &conversation{ctx: t.Context(), w: liveWire{}, ready: true, up: &stuckLink{}}

	var ok bool
	returnsSoon(t, "sendUp", func() { ok = c.sendUp(-1, liveWire{}.text("hi")) })
	if ok {
		t.Fatal("sendUp reported a frame the upstream never took as sent")
	}
}

// Audio kept while paused, for a woken session that stopped reading, stays kept when the write gives up: the session
// counts as dropped, and the one lost reconnects to gets it.
func TestFlushRingGivesUpOnAStuckUpstream(t *testing.T) {
	setVar(t, &upWriteTimeout, 100*time.Millisecond)
	c := &conversation{version: 2, pl: &Plugin{logger: log.New(io.Discard, "", 0)}, ctx: t.Context(), w: liveWire{},
		wake: make(chan struct{}, 1)}
	c.sus.state = linkWaking
	c.sus.ring.add([]byte{1, 0}, 1<<20)
	up := &stuckLink{}
	up.broke = func() { // the reader sees the socket close and lost takes the session off
		c.mu.Lock()
		if c.up == up {
			c.gen++
			c.up, c.ready = nil, false
		}
		c.mu.Unlock()
	}
	c.up, c.ready = up, true

	returnsSoon(t, "flushRing", c.flushRing)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sus.state != linkWaking || c.sus.flush || len(c.sus.ring.frames) != 1 || c.sus.ring.frames[0][0] != 1 {
		t.Fatalf("the kept audio went with the stuck session: %+v", c.sus)
	}
}
