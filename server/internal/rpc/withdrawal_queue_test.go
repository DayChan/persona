package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpcio"
)

// withdrawalWait bounds every handshake in these tests; it is a failure timeout,
// not a scheduling assumption.
const withdrawalWait = StallWindow + 5*time.Second

func withdrawalDone(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(withdrawalWait):
		t.Fatal("TIMEOUT " + what)
	}
}

// withdrawalPair is two real Peers over os.Pipe, both reading continuously.
// owned gives the caller side an explicit transport interrupt.
type withdrawalPair struct {
	caller, remote             *Peer
	callerServed, remoteServed chan struct{}
	interrupted                atomic.Bool
	files                      []*os.File
}

func newWithdrawalPair(t *testing.T, owned bool, remoteReader func(io.Reader) io.Reader, callerWriter func(io.Writer) io.Writer) *withdrawalPair {
	t.Helper()
	callerR, remoteW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	remoteR, callerW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &withdrawalPair{callerServed: make(chan struct{}), remoteServed: make(chan struct{}), files: []*os.File{callerR, remoteW, remoteR, callerW}}
	// An owner counts accepted bytes below any test hook, as the node and host do.
	counted := &ProgressWriter{W: callerW}
	var w io.Writer = counted
	if callerWriter != nil {
		w = callerWriter(counted)
	}
	p.caller, p.remote = New("a", w), New("b", remoteW)
	if owned {
		p.caller.Interrupt = func() {
			p.interrupted.Store(true)
			_ = callerW.Close()
			_ = callerR.Close()
		}
		p.caller.Progress = ConsumedProgress(counted.Accepted, rpcio.OutstandingQuery(callerW))
		if p.caller.Progress == nil {
			t.Fatal("FAIL pipe consumption not measurable on this platform")
		}
	}
	var r io.Reader = remoteR
	if remoteReader != nil {
		r = remoteReader(remoteR)
	}
	go func() { _ = p.caller.Serve(context.Background(), callerR); close(p.callerServed) }()
	go func() { _ = p.remote.Serve(context.Background(), r); close(p.remoteServed) }()
	t.Cleanup(func() {
		p.caller.Close(ErrClosed)
		p.remote.Close(ErrClosed)
		for _, f := range p.files {
			_ = f.Close()
		}
		withdrawalDone(t, p.callerServed, "caller reader exit")
		withdrawalDone(t, p.remoteServed, "remote reader exit")
	})
	return p
}

// approvalHandler models a remote approval: each request reports admission, then
// waits for withdrawal or a later approval. A late approval executes only if the
// request's context is still live.
type approvalHandler struct {
	admitted  chan struct{}
	withdrawn chan struct{}
	approve   chan struct{}
	executed  atomic.Int32
}

func newApprovalHandler(n int) *approvalHandler {
	return &approvalHandler{admitted: make(chan struct{}, n), withdrawn: make(chan struct{}, n), approve: make(chan struct{})}
}

func (h *approvalHandler) handle(ctx context.Context, method string, _ json.RawMessage) (any, error) {
	if method == "ping" {
		return "pong", nil
	}
	if method != "approval" {
		return nil, nil
	}
	h.admitted <- struct{}{}
	select {
	case <-ctx.Done():
		h.withdrawn <- struct{}{}
	case <-h.approve:
	}
	<-h.approve
	if ctx.Err() == nil {
		h.executed.Add(1)
	}
	return nil, ctx.Err()
}

func awaitCount(t *testing.T, ch <-chan struct{}, n int, what string) {
	t.Helper()
	timeout := time.After(withdrawalWait)
	for i := 0; i < n; i++ {
		select {
		case <-ch:
		case <-timeout:
			t.Fatalf("TIMEOUT %s %d/%d", what, i, n)
		}
	}
}

func assertHealthy(t *testing.T, p *withdrawalPair) {
	t.Helper()
	if err := p.caller.Err(); err != nil {
		t.Fatal("FAIL healthy stream retired:", err)
	}
	if p.interrupted.Load() {
		t.Fatal("FAIL healthy owned transport interrupted")
	}
	if err := p.caller.Notify("after", nil); err != nil {
		t.Fatal("FAIL notification after withdrawals:", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), withdrawalWait)
	defer cancel()
	var out string
	if err := p.caller.Call(ctx, "ping", nil, &out); err != nil || out != "pong" {
		t.Fatal("FAIL round trip after withdrawals:", out, err)
	}
	var back string
	if err := p.remote.Call(ctx, "ping", nil, &back); err != nil || back != "pong" {
		t.Fatal("FAIL remote request after withdrawals:", back, err)
	}
}

// One parent context cancels N admitted opted-in calls at once (an assistant
// turn stopping). On a healthy, continuously drained stream every withdrawal
// must arrive, later approvals must not execute and the connection stays up.
func TestWithdrawalQueueConcurrentHealthyStream(t *testing.T) {
	for _, owned := range []bool{false, true} {
		for _, n := range []int{2, 8, 32} {
			for rep := 0; rep < 30; rep++ {
				t.Run(fmt.Sprintf("%s/n%d/%02d", map[bool]string{false: "borrowed", true: "owned"}[owned], n, rep), func(t *testing.T) {
					p := newWithdrawalPair(t, owned, nil, nil)
					h := newApprovalHandler(n)
					p.remote.OnRequest = h.handle
					p.caller.OnRequest = h.handle
					p.caller.Cancels = map[string]bool{"approval": true}
					parent, cancel := context.WithCancel(context.Background())
					defer cancel()
					var wg sync.WaitGroup
					for i := 0; i < n; i++ {
						wg.Go(func() {
							if err := p.caller.Call(parent, "approval", nil, nil); !errors.Is(err, context.Canceled) {
								t.Error("FAIL caller cancellation identity:", err)
							}
						})
					}
					awaitCount(t, h.admitted, n, "remote admission")
					cancel()
					returned := make(chan struct{})
					go func() { wg.Wait(); close(returned) }()
					withdrawalDone(t, returned, "callers")
					awaitCount(t, h.withdrawn, n, "remote withdrawal")
					close(h.approve)
					assertHealthy(t, p)
					if got := h.executed.Load(); got != 0 {
						t.Fatalf("FAIL %d withdrawn calls executed on late approval", got)
					}
				})
			}
		}
	}
}

// throttledReader drains a healthy but slow link: 4 KiB every interval (10 ms
// is ~400 KB/s, 125 ms ~32 KB/s). pauseAt, if set, stops reading once for pause
// after that many bytes, like a hub busy handling an event.
type throttledReader struct {
	r               io.Reader
	every, pause    time.Duration
	pauseAt, read   atomic.Int64
	paused, resumed chan struct{}
}

func (s *throttledReader) Read(b []byte) (int, error) {
	if len(b) > 4096 {
		b = b[:4096]
	}
	if s.pause > 0 && s.paused != nil && s.read.Load() >= s.pauseAt.Load() {
		close(s.paused)
		s.paused = nil
		time.Sleep(s.pause) // the remote's own pause, not synchronization
		close(s.resumed)
	}
	time.Sleep(s.every) // throughput shaping, not synchronization
	n, err := s.r.Read(b)
	s.read.Add(int64(n))
	return n, err
}

// largeWriteSignal reports when a large frame's Write has begun.
type largeWriteSignal struct {
	w       io.Writer
	entered chan struct{}
	once    sync.Once
}

func (s *largeWriteSignal) Write(b []byte) (int, error) {
	if len(b) > 100<<10 {
		s.once.Do(func() { close(s.entered) })
	}
	return s.w.Write(b)
}

// A single withdrawal behind a large frame on a slow, draining link must wait
// for the progressing frame and then be delivered, not retire the connection,
// however long the frame takes. So must a remote that pauses reading for less
// than the stall window.
func TestWithdrawalQueueSlowLinkLargeFrame(t *testing.T) {
	cases := []struct {
		name         string
		every, pause time.Duration
		size, reps   int
	}{
		{"400KBps-200KB", 10 * time.Millisecond, 0, 200 << 10, 3},
		{"32KBps-200KB", 125 * time.Millisecond, 0, 200 << 10, 1},
		{"400KBps-200KB-pause", 10 * time.Millisecond, StallWindow * 3 / 5, 200 << 10, 1},
	}
	for _, owned := range []bool{false, true} {
		for _, tc := range cases {
			for rep := 0; rep < tc.reps; rep++ {
				t.Run(fmt.Sprintf("%s/%s/%d", map[bool]string{false: "borrowed", true: "owned"}[owned], tc.name, rep), func(t *testing.T) {
					testSlowLinkLargeFrame(t, owned, tc.every, tc.pause, tc.size)
				})
			}
		}
	}
}

func testSlowLinkLargeFrame(t *testing.T, owned bool, every, pause time.Duration, size int) {
	var slow *throttledReader
	signal := &largeWriteSignal{entered: make(chan struct{})}
	p := newWithdrawalPair(t, owned,
		func(r io.Reader) io.Reader {
			slow = &throttledReader{r: r, every: every}
			if pause > 0 {
				slow.pause, slow.paused, slow.resumed = pause, make(chan struct{}), make(chan struct{})
				slow.pauseAt.Store(math.MaxInt64)
			}
			return slow
		},
		func(w io.Writer) io.Writer { signal.w = w; return signal })
	h := newApprovalHandler(1)
	p.remote.OnRequest = h.handle
	p.caller.OnRequest = h.handle
	p.caller.Cancels = map[string]bool{"approval": true}
	callCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- p.caller.Call(callCtx, "approval", nil, nil) }()
	awaitCount(t, h.admitted, 1, "remote admission")
	big := make(chan error, 1)
	go func() { big <- p.caller.Call(context.Background(), "big", strings.Repeat("x", size), nil) }()
	withdrawalDone(t, signal.entered, "large frame write")
	// The frame is in progress: more than the pipe buffer is still unread.
	base, progress := slow.read.Load(), time.After(withdrawalWait)
	for slow.read.Load() < base+8192 {
		select {
		case err := <-big:
			t.Fatal("FAIL large frame finished before cancellation", err)
		case <-progress:
			t.Fatal("TIMEOUT slow link progress")
		case <-time.After(time.Millisecond):
		}
	}
	if pause > 0 {
		slow.pauseAt.Store(slow.read.Load() + 8192)
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("FAIL caller identity:", err)
		}
	case <-time.After(withdrawalWait):
		t.Fatal("TIMEOUT canceled caller")
	}
	// The whole frame at the link's rate, with headroom.
	transfer := time.Duration(size/4096+1)*every*3 + pause + withdrawalWait
	select {
	case err := <-big:
		if err != nil {
			t.Fatal("FAIL progressing large frame failed:", err)
		}
	case <-time.After(transfer):
		t.Fatal("TIMEOUT large frame")
	}
	if pause > 0 {
		withdrawalDone(t, slow.resumed, "remote pause")
	}
	awaitCount(t, h.withdrawn, 1, "remote withdrawal")
	close(h.approve)
	assertHealthy(t, p)
	if h.executed.Load() != 0 {
		t.Fatal("FAIL withdrawn call executed")
	}
}

// stallGate blocks the first Write after arm until its owner interrupts it.
type stallGate struct {
	w                io.Writer
	armed            atomic.Bool
	entered, release chan struct{}
	enteredOnce      sync.Once
	releaseOnce      sync.Once
}

func (g *stallGate) open() { g.releaseOnce.Do(func() { close(g.release) }) }
func (g *stallGate) Write(b []byte) (int, error) {
	if g.armed.CompareAndSwap(true, false) {
		g.enteredOnce.Do(func() { close(g.entered) })
		<-g.release
	}
	return g.w.Write(b)
}

// A writer that makes no progress at all still retires an owned connection
// within the stall bound, and the session really ends: the remote reads EOF and
// its pending approval is withdrawn. A borrowed peer cannot end its session, so
// it must not be retired into a logically closed reader that keeps executing
// requests it can never answer; once its writer resumes, the withdrawal arrives.
func TestWithdrawalQueueStalledWriter(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "borrowed", true: "owned"}[owned], func(t *testing.T) {
			gate := &stallGate{entered: make(chan struct{}), release: make(chan struct{})}
			defer gate.open()
			p := newWithdrawalPair(t, owned, nil, func(w io.Writer) io.Writer { gate.w = w; return gate })
			if owned {
				interrupt := p.caller.Interrupt
				p.caller.Interrupt = func() { interrupt(); gate.open() }
			}
			h := newApprovalHandler(1)
			p.remote.OnRequest = h.handle
			var callerHandled atomic.Int32
			p.caller.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
				callerHandled.Add(1)
				return "pong", nil
			}
			p.caller.Cancels = map[string]bool{"approval": true}
			callCtx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- p.caller.Call(callCtx, "approval", nil, nil) }()
			awaitCount(t, h.admitted, 1, "remote admission")
			gate.armed.Store(true)
			unrelated := make(chan error, 1)
			go func() { unrelated <- p.caller.Notify("unrelated", nil) }()
			withdrawalDone(t, gate.entered, "stalled frame")
			began := time.Now()
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("FAIL caller identity:", err)
				}
			case <-time.After(withdrawalWait):
				t.Fatal("TIMEOUT canceled caller is not bounded by the stalled writer")
			}
			if owned {
				withdrawalDone(t, p.caller.Done(), "owned retirement")
				elapsed := time.Since(began)
				withdrawalDone(t, p.callerServed, "caller session end")
				withdrawalDone(t, p.remoteServed, "remote EOF")
				awaitCount(t, h.withdrawn, 1, "remote withdrawal by EOF")
				close(h.approve)
				if h.executed.Load() != 0 {
					t.Fatal("FAIL withdrawn call executed after retirement")
				}
				if !p.interrupted.Load() {
					t.Fatal("FAIL retirement did not interrupt the owned transport")
				}
				t.Logf("PASS owned stalled writer retired after %v and the session ended", elapsed)
				return
			}
			// Borrowed: the stall lasts well past any owned stall bound.
			select {
			case <-p.caller.Done():
				select {
				case <-p.callerServed:
				default:
					ctx, stop := context.WithTimeout(context.Background(), 500*time.Millisecond)
					_ = p.remote.Call(ctx, "ping", nil, nil)
					stop()
					t.Fatalf("FAIL borrowed peer retired into a zombie reader (Err=%v, handled=%d)", p.caller.Err(), callerHandled.Load())
				}
				t.Fatal("FAIL borrowed peer retired")
			case <-time.After(StallWindow + time.Second):
			}
			gate.open()
			if err := <-unrelated; err != nil {
				t.Fatal("FAIL resumed writer:", err)
			}
			awaitCount(t, h.withdrawn, 1, "remote withdrawal after resume")
			close(h.approve)
			if h.executed.Load() != 0 {
				t.Fatal("FAIL withdrawn call executed")
			}
			p.caller.OnRequest = h.handle
			assertHealthy(t, p)
		})
	}
}

// completedWrite delivers a request frame in full, then holds its Write open
// until released: the remote already has the frame while the caller's Write has
// not returned yet.
type completedWrite struct {
	w                  io.Writer
	armed              atomic.Bool
	delivered, release chan struct{}
}

func (c *completedWrite) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	if c.armed.CompareAndSwap(true, false) {
		close(c.delivered)
		<-c.release
	}
	return n, err
}

// A caller canceling after its frame reached the remote, but before its Write
// returned, must not retire a healthy owned stream: the Write still finishes
// within the stall window, the caller keeps its context identity plus
// ErrDropped, and the opted-in withdrawal follows on the same stream.
func TestWithdrawalQueueCancelDuringCompletedWrite(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			hold := &completedWrite{delivered: make(chan struct{}), release: make(chan struct{})}
			p := newWithdrawalPair(t, true, nil, func(w io.Writer) io.Writer { hold.w = w; return hold })
			interrupted := make(chan struct{})
			var once sync.Once
			interrupt := p.caller.Interrupt
			p.caller.Interrupt = func() { once.Do(func() { close(interrupted) }); interrupt() }
			h := newApprovalHandler(1)
			p.remote.OnRequest = h.handle
			p.caller.OnRequest = h.handle
			p.caller.Cancels = map[string]bool{"approval": true}
			ctx, cancel := context.WithCancel(context.Background())
			want := context.Canceled
			if deadline {
				// Expires shortly after delivery, while the Write is still held.
				ctx, cancel = context.WithTimeout(context.Background(), 500*time.Millisecond)
				want = context.DeadlineExceeded
			}
			defer cancel()
			hold.armed.Store(true)
			result := make(chan error, 1)
			go func() { result <- p.caller.Call(ctx, "approval", nil, nil) }()
			withdrawalDone(t, hold.delivered, "frame delivery")
			awaitCount(t, h.admitted, 1, "remote admission")
			if deadline {
				withdrawalDone(t, ctx.Done(), "caller deadline")
			} else {
				cancel()
			}
			// Old behavior interrupted at once; a progressing Write gets the window.
			select {
			case <-interrupted:
				t.Fatal("FAIL cancel after delivery interrupted a healthy owned stream", p.caller.Err())
			case <-time.After(StallWindow / 10):
			}
			close(hold.release)
			select {
			case err := <-result:
				if !errors.Is(err, want) || !errors.Is(err, ErrDropped) {
					t.Fatal("FAIL caller identity or ambiguity", err)
				}
			case <-time.After(withdrawalWait):
				t.Fatal("TIMEOUT caller")
			}
			awaitCount(t, h.withdrawn, 1, "remote withdrawal")
			close(h.approve)
			assertHealthy(t, p)
			if h.executed.Load() != 0 {
				t.Fatal("FAIL withdrawn call executed")
			}
		})
	}
}
