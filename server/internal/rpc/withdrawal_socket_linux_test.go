//go:build linux

package rpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpcio"
	"golang.org/x/sys/unix"
)

// socketPair is a real unix stream socketpair as two pollable connections.
func socketPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int, name string) *net.UnixConn {
		f := os.NewFile(uintptr(fd), name)
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		return c.(*net.UnixConn)
	}
	return conn(fds[0], "caller"), conn(fds[1], "remote")
}

// pacedReader reads 4 KiB per interval (a healthy slow reader); interval 0
// drains at full speed.
type pacedReader struct {
	r     io.Reader
	every atomic.Int64
	read  atomic.Int64
}

func (s *pacedReader) Read(b []byte) (int, error) {
	if len(b) > 4096 {
		b = b[:4096]
	}
	if d := time.Duration(s.every.Load()); d > 0 {
		time.Sleep(d) // throughput shaping, not synchronization
	}
	n, err := s.r.Read(b)
	s.read.Add(int64(n))
	return n, err
}

// A Linux socket wakes a blocked writer only after its peer drains a large part
// of the send buffer, so Write returns stay silent for seconds on a link that is
// steadily reading. Over a real socketpair at 8–64 KB/s, an owned peer with a
// withdrawal waiting behind a large frame must not be retired for a period well
// past the stall window; once the reader speeds up, the frame and the withdrawal
// complete on the same stream.
func TestWithdrawalQueueSlowUnixSocket(t *testing.T) {
	for _, rate := range []int{8, 32, 48, 64} {
		t.Run(fmt.Sprintf("%dKBps", rate), func(t *testing.T) {
			callerConn, remoteConn := socketPair(t)
			counted := &ProgressWriter{W: callerConn}
			signal := &largeWriteSignal{w: counted, entered: make(chan struct{})}
			caller, remote := New("a", signal), New("b", remoteConn)
			var interrupted atomic.Bool
			caller.Interrupt = func() { interrupted.Store(true); _ = callerConn.Close() }
			caller.Progress = ConsumedProgress(counted.Accepted, rpcio.OutstandingQuery(callerConn))
			if caller.Progress == nil {
				t.Fatal("FAIL unix socket consumption not measurable")
			}
			slow := &pacedReader{r: remoteConn}
			slow.every.Store(int64(4096 * time.Second / time.Duration(rate*1024)))
			h := newApprovalHandler(1)
			remote.OnRequest, caller.OnRequest = h.handle, h.handle
			caller.Cancels = map[string]bool{"approval": true}
			callerServed, remoteServed := make(chan struct{}), make(chan struct{})
			go func() { _ = caller.Serve(context.Background(), callerConn); close(callerServed) }()
			go func() { _ = remote.Serve(context.Background(), slow); close(remoteServed) }()
			t.Cleanup(func() {
				caller.Close(ErrClosed)
				remote.Close(ErrClosed)
				_ = callerConn.Close()
				_ = remoteConn.Close()
				withdrawalDone(t, callerServed, "caller reader exit")
				withdrawalDone(t, remoteServed, "remote reader exit")
			})
			callCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- caller.Call(callCtx, "approval", nil, nil) }()
			awaitCount(t, h.admitted, 1, "remote admission")
			big := make(chan error, 1)
			go func() { big <- caller.Call(context.Background(), "big", strings.Repeat("x", 2<<20), nil) }()
			withdrawalDone(t, signal.entered, "large frame write")
			base, progress := slow.read.Load(), time.After(30*time.Second)
			for slow.read.Load() < base+8192 {
				select {
				case <-progress:
					t.Fatal("TIMEOUT slow socket progress")
				case <-time.After(time.Millisecond):
				}
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("FAIL caller identity:", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("TIMEOUT canceled caller")
			}
			// The reader keeps going slowly for well past the stall window.
			before := slow.read.Load()
			select {
			case <-caller.Done():
				t.Fatalf("FAIL steadily read %d KB/s socket retired: %v (interrupted=%v)", rate, caller.Err(), interrupted.Load())
			case err := <-big:
				t.Fatal("FAIL large frame finished while still slow:", err)
			case <-time.After(7 * time.Second):
			}
			t.Logf("slow phase read %d bytes without retirement", slow.read.Load()-before)
			slow.every.Store(0)
			select {
			case err := <-big:
				if err != nil {
					t.Fatal("FAIL progressing large frame failed:", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("TIMEOUT large frame after speed-up")
			}
			awaitCount(t, h.withdrawn, 1, "remote withdrawal")
			close(h.approve)
			if caller.Err() != nil || interrupted.Load() {
				t.Fatal("FAIL healthy socket retired:", caller.Err())
			}
			ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			var out string
			if err := caller.Call(ctx, "ping", nil, &out); err != nil || out != "pong" {
				t.Fatal("FAIL round trip after withdrawal:", out, err)
			}
			if h.executed.Load() != 0 {
				t.Fatal("FAIL withdrawn call executed")
			}
		})
	}
}

// A unix socket whose reader stops entirely still retires within the window:
// a caller canceled during its large frame's Write is interrupted.
func TestWithdrawalQueueStalledUnixSocket(t *testing.T) {
	callerConn, remoteConn := socketPair(t)
	defer remoteConn.Close()
	counted := &ProgressWriter{W: callerConn}
	caller := New("a", counted)
	caller.Interrupt = func() { _ = callerConn.Close() }
	caller.Progress = ConsumedProgress(counted.Accepted, rpcio.OutstandingQuery(callerConn))
	if caller.Progress == nil {
		t.Fatal("FAIL unix socket consumption not measurable")
	}
	served := make(chan struct{})
	go func() { _ = caller.Serve(context.Background(), callerConn); close(served) }()
	defer func() { caller.Close(ErrClosed); withdrawalDone(t, served, "caller reader exit") }()
	// Nobody reads remoteConn: the frame fills the socket and stalls.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- caller.Call(ctx, "big", strings.Repeat("x", 2<<20), nil) }()
	deadline := time.After(10 * time.Second)
	for counted.Accepted() == 0 {
		select {
		case <-deadline:
			t.Fatal("TIMEOUT first piece")
		case <-time.After(time.Millisecond):
		}
	}
	began := time.Now()
	cancel()
	withdrawalDone(t, caller.Done(), "stalled socket retirement")
	elapsed := time.Since(began)
	if !errors.Is(caller.Err(), ErrStalled) {
		t.Fatal("FAIL stalled socket not retired by the stall bound:", caller.Err())
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrDropped) {
			t.Fatal("FAIL caller identity or ambiguity:", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("TIMEOUT caller")
	}
	if elapsed > StallWindow+2*time.Second {
		t.Fatal("FAIL retirement later than the stall bound:", elapsed)
	}
	t.Logf("PASS stalled unix socket retired after %v", elapsed)
}
