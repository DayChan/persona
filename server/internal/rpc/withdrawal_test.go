package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Only the second (unrelated) frame is gated, before any bytes reach the peer.
// The connection owner can always release it and close the actual transport.
type withdrawalWriter struct {
	net.Conn
	writes           atomic.Int32
	entered, release chan struct{}
	once             sync.Once
}

func (w *withdrawalWriter) unblock() { w.once.Do(func() { close(w.release) }) }
func (w *withdrawalWriter) Write(b []byte) (int, error) {
	if w.writes.Add(1) == 2 {
		close(w.entered)
		select {
		case <-w.release:
		case <-time.After(30 * time.Second):
			// Far past every wait below: only the stall bound can end the test.
			return 0, context.DeadlineExceeded
		}
	}
	return w.Conn.Write(b)
}

func TestWriteLifecycleWithdrawalBehindUnrelatedFrame(t *testing.T) {
	for _, method := range []string{"mcp.call", "generic.approval"} {
		for _, stalled := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/delivery", true: "/fail-closed"}[stalled], func(t *testing.T) {
				a, b := net.Pipe()
				w := &withdrawalWriter{Conn: a, entered: make(chan struct{}), release: make(chan struct{})}
				// net.Pipe hands bytes straight to the reader: accepted is consumed.
				counted := &ProgressWriter{W: w}
				node, hub := New("n", counted), New("h", b)
				node.Cancels = map[string]bool{method: true}
				node.Interrupt = func() { _ = a.Close(); w.unblock() }
				node.Progress = counted.Accepted
				hub.Interrupt = func() { _ = b.Close() }
				ctx, stop := context.WithTimeout(context.Background(), StallWindow+10*time.Second)
				defer stop()
				served := make(chan error, 2)
				go func() { served <- node.Serve(ctx, a) }()
				go func() { served <- hub.Serve(ctx, b) }()
				t.Cleanup(func() {
					w.unblock()
					node.Close(ErrClosed)
					hub.Close(ErrClosed)
					_ = rpcLifecycleAwait(t, served)
					_ = rpcLifecycleAwait(t, served)
				})
				approval, request, ended := make(chan struct{}), make(chan context.Context, 1), make(chan bool, 1)
				hub.OnRequest = func(c context.Context, _ string, _ json.RawMessage) (any, error) {
					request <- c
					select {
					case <-c.Done():
						ended <- false
						return nil, c.Err()
					case <-approval:
						// A late approval must still check the withdrawal's context.
						executed := c.Err() == nil
						ended <- executed
						return executed, nil
					}
				}
				callCtx, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
				defer cancel()
				result := make(chan error, 1)
				go func() { result <- node.Call(callCtx, method, nil, nil) }()
				var remote context.Context
				select {
				case remote = <-request:
				case <-ctx.Done():
					t.Fatal("TIMEOUT approval admission")
				}
				unrelated := make(chan error, 1)
				go func() { unrelated <- node.Notify("unrelated", nil) }()
				rpcLifecycleDone(t, w.entered)
				if err := rpcLifecycleAwait(t, result); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("FAIL caller deadline", err)
				}
				if !stalled {
					w.unblock()
				}
				rpcLifecycleDone(t, remote.Done())
				close(approval)
				select {
				case executed := <-ended:
					if executed {
						t.Fatal("FAIL withdrawn call executed on late approval")
					}
				case <-ctx.Done():
					t.Fatal("TIMEOUT withdrawn handler")
				}
				err := rpcLifecycleAwait(t, unrelated)
				if stalled {
					if err == nil || !errors.Is(node.Err(), ErrStalled) {
						t.Fatal("FAIL stalled withdrawal did not retire the transport by the stall bound", err, node.Err())
					}
				} else {
					if err != nil || node.Err() != nil {
						t.Fatal("FAIL healthy withdrawal delivery", err, node.Err())
					}
					if err := node.Notify("next", nil); err != nil {
						t.Fatal("FAIL healthy stream retired", err)
					}
				}
				t.Log("PASS caller deadline, withdrawal before late approval, bounded delivery or actual transport termination")
			})
		}
	}
}
