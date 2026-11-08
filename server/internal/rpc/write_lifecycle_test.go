package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type lifecycleShortWriter struct {
	n      int
	err    error
	writes int
}

func (w *lifecycleShortWriter) Write(b []byte) (int, error) {
	w.writes++
	if w.n < 0 {
		return len(b), w.err
	}
	return w.n, w.err
}
func TestWriteLifecyclePartialFrames(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		err  error
	}{
		{"zero-nil", 0, nil}, {"short-nil", 3, nil}, {"partial-error", 3, io.ErrUnexpectedEOF}, {"full-error", -1, io.ErrUnexpectedEOF}, {"oversized-count", 1000, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &lifecycleShortWriter{n: tc.n, err: tc.err}
			p := New("h", w)
			err := p.Notify("action", nil)
			if err == nil {
				t.Fatal("FAIL failed write succeeded")
			}
			if p.Err() == nil {
				t.Fatal("FAIL partial stream stayed open")
			}
			if err := p.Notify("next", nil); !errors.Is(err, ErrClosed) {
				t.Fatal(err)
			}
			if w.writes != 1 {
				t.Fatal("FAIL second frame written")
			}
		})
	}
}
func rpcLifecycleAwait(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(StallWindow + 5*time.Second):
		t.Fatal("TIMEOUT RPC completion")
		return nil
	}
}
func rpcLifecycleDone(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(StallWindow + 5*time.Second):
		t.Fatal("TIMEOUT lifecycle completion")
	}
}

// The first frame succeeds; a subsequent control frame blocks until explicitly
// interrupted. The handshake proves admission without scheduler sleeps.
type controlWriter struct {
	writes                               atomic.Int32
	request, admitted, release, finished chan struct{}
	once                                 sync.Once
}

func newControlWriter() *controlWriter {
	return &controlWriter{request: make(chan struct{}), admitted: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
}
func (w *controlWriter) Write(b []byte) (int, error) {
	n := w.writes.Add(1)
	if n == 1 {
		close(w.request)
		return len(b), nil
	}
	if n != 2 {
		panic("unexpected concurrent/retried control frame")
	}
	close(w.admitted)
	<-w.release
	close(w.finished)
	return 3, io.ErrClosedPipe
}
func (w *controlWriter) interrupt() { w.once.Do(func() { close(w.release) }) }
func TestWriteLifecycleCancelControlBound(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "owned"}[owned], func(t *testing.T) {
			w := newControlWriter()
			defer w.interrupt()
			counted := &ProgressWriter{W: w}
			p := New("h", counted)
			p.Cancels = map[string]bool{"action": true}
			if owned {
				p.Interrupt = w.interrupt
				p.Progress = counted.Accepted
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- p.Call(ctx, "action", nil, nil) }()
			rpcLifecycleDone(t, w.request)
			cancel()
			if err := rpcLifecycleAwait(t, result); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			rpcLifecycleDone(t, w.admitted)
			for i := 0; i < 1000; i++ {
				p.cancelBestEffort("unused")
			}
			if w.writes.Load() != 2 {
				t.Fatal("FAIL control workers grew")
			}
			p.mu.Lock()
			pending := len(p.pending)
			p.mu.Unlock()
			if pending != 0 {
				t.Fatal("FAIL pending retained")
			}
			if owned {
				rpcLifecycleDone(t, p.Done())
				rpcLifecycleDone(t, w.finished)
				if !errors.Is(p.Err(), ErrStalled) {
					t.Fatal("FAIL owned control write not retired by the stall bound", p.Err())
				}
				t.Log("PASS owned admitted control write interrupted within finite policy")
			} else {
				p.Close(ErrClosed)
				select {
				case <-w.finished:
					t.Fatal("FAIL legacy logical close claimed hard interrupt")
				default:
				}
				w.interrupt()
				rpcLifecycleDone(t, w.finished)
				t.Log("PASS legacy retains at most one worker until owner unblocks it")
			}
		})
	}
}
func TestWriteLifecycleActiveContextIdentity(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "deadline"}[deadline], func(t *testing.T) {
			r, w := io.Pipe()
			defer r.Close()
			defer w.Close()
			counted := &ProgressWriter{W: w}
			p := New("h", counted)
			p.Interrupt = func() { _ = w.Close() }
			p.Progress = counted.Accepted
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			}
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- p.Call(ctx, "action", nil, nil) }()
			// A read of a prefix establishes active Write; the remainder stays blocked.
			prefix := make([]byte, 3)
			read := make(chan error, 1)
			go func() { _, err := io.ReadFull(r, prefix); read <- err }()
			if err := rpcLifecycleAwait(t, read); err != nil {
				t.Fatal(err)
			}
			if !deadline {
				cancel()
			}
			err := rpcLifecycleAwait(t, result)
			want := context.Canceled
			if deadline {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) || !errors.Is(err, ErrDropped) {
				t.Fatal("FAIL cancellation identity or ambiguity", err)
			}
			if !errors.Is(p.Err(), ErrStalled) {
				t.Fatal("FAIL partial frame not closed by the stall bound", p.Err())
			}
			p.mu.Lock()
			pending := len(p.pending)
			p.mu.Unlock()
			if pending != 0 || len(p.wsem) != 0 {
				t.Fatal("FAIL pending/admission retained")
			}
		})
	}
}
func TestWriteLifecycleCloseReentrancy(t *testing.T) {
	p := New("h", io.Discard)
	var interrupts atomic.Int32
	p.Interrupt = func() { interrupts.Add(1); p.Close(ErrClosed) }
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Go(func() { p.Close(io.EOF) })
	}
	done := make(chan error, 1)
	go func() { wg.Wait(); done <- nil }()
	_ = rpcLifecycleAwait(t, done)
	if interrupts.Load() != 1 {
		t.Fatal("FAIL interrupt not exactly once")
	}
}
func TestWriteLifecycleControlBudgetAndActiveCapacity(t *testing.T) {
	p := New("h", io.Discard)
	p.cancelBestEffort(strings.Repeat("x", cancelFrameMost+1))
	p.mu.Lock()
	queued := p.controlDone != nil || len(p.controlQueue) != 0
	p.mu.Unlock()
	if queued || len(p.wsem) != 0 || p.Err() == nil {
		t.Fatal("FAIL oversized withdrawal left transport healthy")
	}
	p = New("h", io.Discard)
	p.Limits = Limits{ConcurrentRequests: 1}
	entered, canceled, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	p.OnRequest = func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		close(finished)
		return nil, ctx.Err()
	}
	r, w := net.Pipe()
	defer r.Close()
	defer w.Close()
	_ = r.SetDeadline(time.Now().Add(2 * time.Second))
	_ = w.SetDeadline(time.Now().Add(2 * time.Second))
	served := make(chan error, 1)
	go func() { served <- p.Serve(context.Background(), r) }()
	if _, err := fmt.Fprintln(w, `{"id":"one","method":"action"}`); err != nil {
		t.Fatal(err)
	}
	rpcLifecycleDone(t, entered)
	if _, err := fmt.Fprintln(w, `{"event":"rpc.cancel","params":{"id":"one"}}`); err != nil {
		t.Fatal(err)
	}
	rpcLifecycleDone(t, canceled)
	p.mu.Lock()
	running := len(p.running)
	p.mu.Unlock()
	if running != 1 {
		t.Fatal("FAIL canceled handler's actual capacity released early")
	}
	if _, err := fmt.Fprintln(w, `{"id":"two","method":"action"}`); err != nil {
		t.Fatal(err)
	}
	if err := rpcLifecycleAwait(t, served); err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatal("FAIL actual capacity not enforced", err)
	}
	unblock()
	rpcLifecycleDone(t, finished)
	t.Log("PASS cancellation retains accepted capacity until actual handler completion")
}

type lifecycleWriterFunc func([]byte) (int, error)

func (f lifecycleWriterFunc) Write(b []byte) (int, error) { return f(b) }
func TestWriteLifecycleLegacyFullWriteAfterCancel(t *testing.T) {
	w := newStuckWriter()
	var once sync.Once
	release := func() { once.Do(func() { close(w.release) }) }
	defer release()
	controlDone := make(chan struct{})
	p := New("h", lifecycleWriterFunc(func(b []byte) (int, error) {
		n, err := w.Write(b)
		if strings.Contains(string(b), cancelEvent) {
			close(controlDone)
		}
		return n, err
	}))
	p.Cancels = map[string]bool{"action": true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- p.Call(ctx, "action", nil, nil) }()
	rpcLifecycleDone(t, w.started)
	cancel()
	release()
	if err := rpcLifecycleAwait(t, result); !errors.Is(err, context.Canceled) || !errors.Is(err, ErrDropped) {
		t.Fatal(err)
	}
	rpcLifecycleDone(t, controlDone)
	lines := strings.Split(strings.TrimSpace(w.String()), "\n")
	if len(lines) != 2 {
		t.Fatal("FAIL legacy request retry or missing cancellation")
	}
	var request, control message
	if json.Unmarshal([]byte(lines[0]), &request) != nil || json.Unmarshal([]byte(lines[1]), &control) != nil {
		t.Fatal("FAIL frame corruption")
	}
	var params struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(control.Params, &params)
	if request.Method != "action" || control.Event != cancelEvent || params.ID != request.ID {
		t.Fatal("FAIL request-before-own-cancel causality")
	}
	if p.Err() != nil {
		t.Fatal("FAIL successful legacy frame disconnected stream")
	}
	t.Log("PASS full legacy write finishing after cancellation retains opted-in control")
}

func TestWriteLifecycleCancelAndCloseBeforeAdmission(t *testing.T) {
	for i := 0; i < 100; i++ {
		w := &lifecycleShortWriter{n: -1}
		p := New("h", w)
		p.Cancels = map[string]bool{"action": true}
		ctx, cancel := context.WithCancel(context.Background())
		params := callMarshalFunc(func() ([]byte, error) { cancel(); p.Close(ErrClosed); return []byte(`{}`), nil })
		err := p.Call(ctx, "action", params, nil)
		cancel()
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrDropped) {
			t.Fatal("FAIL unsent cancellation identity", err)
		}
		if w.writes != 0 {
			t.Fatal("FAIL canceled request or control reached wire")
		}
	}
	t.Log("PASS simultaneous close/cancel retains unsent context identity")
}

func TestWriteLifecycleFailedCallAmbiguity(t *testing.T) {
	for _, n := range []int{0, 3, -1} {
		t.Run(map[int]string{0: "zero", 3: "partial", -1: "full-error"}[n], func(t *testing.T) {
			p := New("h", &lifecycleShortWriter{n: n, err: io.ErrUnexpectedEOF})
			err := p.Call(context.Background(), "non-idempotent", nil, nil)
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal(err)
			}
			if errors.Is(err, ErrDropped) != (n != 0) {
				t.Fatal("FAIL sent uncertainty", err)
			}
		})
	}
}
