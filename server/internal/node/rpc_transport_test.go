package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// This owned net.Conn honors Close's interruption contract even at the gate.
type nodeRPCGate struct {
	net.Conn
	writes           atomic.Int32
	entered, release chan struct{}
	once             sync.Once
}

func (w *nodeRPCGate) unblock() { w.once.Do(func() { close(w.release) }) }
func (w *nodeRPCGate) Write(b []byte) (int, error) {
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
func (w *nodeRPCGate) Close() error {
	err := w.Conn.Close()
	w.unblock()
	return err
}
func nodeRPCAwait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(rpc.StallWindow + 5*time.Second):
		t.Fatal("TIMEOUT node RPC gate")
	}
}
func TestNodeRPCPendingApprovalWithdrawalThroughUpdateWrapper(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(map[bool]string{false: "delivery", true: "termination"}[stalled], func(t *testing.T) {
			a, b := net.Pipe()
			transport := &nodeRPCGate{Conn: a, entered: make(chan struct{}), release: make(chan struct{})}
			owned := &ownedRPCTransport{Reader: a, Writer: transport, interrupt: func() { _ = transport.Close() }}
			owned.measure = owned.accepted.Load // net.Pipe hands bytes straight to the reader
			writer := &updateResponseWriter{Writer: owned, responseWritten: func(string) { t.Error("FAIL unrelated frame acknowledged an update") }}
			node := newConnectionPeer(writer, owned)
			if !node.Cancels["mcp.call"] || node.Cancels["ordinary"] || node.Interrupt == nil {
				t.Fatal("FAIL existing node withdrawal/ownership contract")
			}
			hub := rpc.New("h", b)
			hub.Interrupt = func() { _ = b.Close() }
			request := make(chan context.Context, 1)
			approval, done := make(chan struct{}), make(chan bool, 1)
			hub.OnRequest = func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
				request <- ctx
				select {
				case <-ctx.Done():
				case <-approval:
				}
				executed := ctx.Err() == nil
				done <- executed
				return executed, ctx.Err()
			}
			serveDone := make(chan struct{}, 2)
			go func() { _ = node.Serve(context.Background(), a); serveDone <- struct{}{} }()
			go func() { _ = hub.Serve(context.Background(), b); serveDone <- struct{}{} }()
			t.Cleanup(func() {
				node.Close(rpc.ErrClosed)
				hub.Close(rpc.ErrClosed)
				nodeRPCAwait(t, serveDone)
				nodeRPCAwait(t, serveDone)
			})
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			result, notify := make(chan error, 1), make(chan error, 1)
			go func() { result <- node.Call(ctx, "mcp.call", nil, nil) }()
			var remote context.Context
			select {
			case remote = <-request:
			case <-time.After(time.Second):
				t.Fatal("TIMEOUT approval admission")
			}
			go func() { notify <- node.Notify("unrelated", nil) }()
			nodeRPCAwait(t, transport.entered)
			select {
			case err := <-result:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("FAIL caller deadline", err)
				}
			case <-time.After(time.Second):
				t.Fatal("TIMEOUT caller")
			}
			if !stalled {
				transport.unblock()
			}
			nodeRPCAwait(t, remote.Done())
			close(approval)
			select {
			case executed := <-done:
				if executed {
					t.Fatal("FAIL late approval executed withdrawn node call")
				}
			case <-time.After(time.Second):
				t.Fatal("TIMEOUT remote handler")
			}
			select {
			case err := <-notify:
				if stalled == (err == nil) {
					t.Fatal("FAIL healthy delivery/transport termination", err)
				}
				if stalled && !errors.Is(node.Err(), rpc.ErrStalled) {
					t.Fatal("FAIL owned transport not retired by the stall bound", node.Err())
				}
			case <-time.After(time.Second):
				t.Fatal("TIMEOUT unrelated writer")
			}
			if !stalled {
				if err := node.Notify("next", nil); err != nil {
					t.Fatal("FAIL healthy stream", err)
				}
			}
			t.Log("PASS existing node mcp.call contract through wrapped writer: deliver withdrawal or terminate owned transport")
		})
	}
}
func TestNodeRPCWrappedStandardPipesInterruptPartialWrite(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "io-pipe", true: "file-pipe"}[native], func(t *testing.T) {
			var reader io.ReadCloser
			var writer io.WriteCloser
			if native {
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				reader, writer = r, w
			} else {
				reader, writer = io.Pipe()
			}
			defer reader.Close()
			defer writer.Close()
			// These newly created pipes are explicitly owned by this fixture.
			// Inherited os.Stdout is separately covered in a real child process.
			owned := &ownedRPCTransport{Writer: writer, interrupt: func() { _ = writer.Close() }}
			if !native {
				owned.measure = owned.accepted.Load // io.Pipe hands bytes straight to the reader
			}
			wrapped := &updateResponseWriter{Writer: owned, responseWritten: func(string) { t.Error("FAIL partial write acknowledged update") }}
			peer := newConnectionPeer(wrapped, owned)
			if peer.Interrupt == nil {
				t.Fatal("FAIL wrapped pipe lost actual owner")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- peer.Call(ctx, "mcp.call", strings.Repeat("x", 2<<20), nil) }()
			prefix := make(chan error, 1)
			go func() { _, err := io.ReadFull(reader, make([]byte, 3)); prefix <- err }()
			select {
			case err := <-prefix:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("TIMEOUT actual partial frame")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) || !errors.Is(err, rpc.ErrDropped) || !errors.Is(peer.Err(), rpc.ErrStalled) {
					t.Fatal("FAIL owned partial write cancellation identity/ambiguity", err)
				}
			case <-time.After(rpc.StallWindow + 3*time.Second):
				t.Fatal("TIMEOUT actual pipe interrupt")
			}
			if err := peer.Notify("next", nil); !errors.Is(err, rpc.ErrClosed) {
				t.Fatal("FAIL appended frame after abandoned prefix", err)
			}
			t.Log("PASS actual wrapped pipe owner interrupts partial frame without retry")
		})
	}
}

func TestNodeRPCUnknownWriterKeepsLegacyNotificationBehavior(t *testing.T) {
	var output bytes.Buffer
	peer := newConnectionPeer(&output, &output)
	if peer.Interrupt != nil {
		t.Fatal("FAIL arbitrary writer gained an owner hook")
	}
	if err := peer.Notify("ordinary", nil); err != nil || peer.Err() != nil {
		t.Fatal("FAIL ordinary legacy notification", err)
	}
	t.Log("PASS ordinary legacy Notify and no-cap methods retain their policy")
}

type nodeRPCErrorCloser struct {
	io.WriteCloser
	closes atomic.Int32
}

func (w *nodeRPCErrorCloser) Close() error {
	w.closes.Add(1)
	_ = w.WriteCloser.Close()
	return errors.New("fixture close diagnostic")
}

func TestNodeRPCExplicitOwnerCloseErrorAndBorrowedCloser(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	closer := &nodeRPCErrorCloser{WriteCloser: w}
	borrowed := newConnectionPeer(closer, closer)
	borrowed.Close(rpc.ErrClosed)
	if borrowed.Interrupt != nil || closer.closes.Load() != 0 {
		t.Fatal("FAIL arbitrary borrowed Closer acquired interruption/ownership")
	}
	var closeErr error
	owned := &ownedRPCTransport{Writer: closer, interrupt: func() { closeErr = closer.Close() }}
	owned.measure = owned.accepted.Load // io.Pipe hands bytes straight to the reader
	peer := newConnectionPeer(owned, owned)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- peer.Call(ctx, "mcp.call", strings.Repeat("x", 1024), nil) }()
	prefix := make(chan error, 1)
	go func() { _, err := io.ReadFull(r, make([]byte, 3)); prefix <- err }()
	select {
	case err := <-prefix:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("TIMEOUT Close-error admission")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, rpc.ErrDropped) || !errors.Is(err, io.ErrClosedPipe) {
			t.Fatal("FAIL active I/O outcome identities after owner Close error", err)
		}
	case <-time.After(rpc.StallWindow + 3*time.Second):
		t.Fatal("TIMEOUT active I/O after owner Close error")
	}
	peer.Close(rpc.ErrClosed)
	if closeErr == nil || closer.closes.Load() != 1 {
		t.Fatal("FAIL single owner Close error")
	}
	t.Log("PASS only explicit ownership interrupts actual I/O; Close error does not hide retained context/transport ambiguity")
}
