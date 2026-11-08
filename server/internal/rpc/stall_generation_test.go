package rpc

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// generationWriter has two independently gated frames, then writes normally.
// Terminal interruption releases every gate without relying on fixture timers.
type generationWriter struct {
	count                                             atomic.Int32
	first, second, finishFirst, finishSecond, stopped chan struct{}
	stopOnce                                          sync.Once
}

func newGenerationWriter() *generationWriter {
	return &generationWriter{first: make(chan struct{}), second: make(chan struct{}), finishFirst: make(chan struct{}), finishSecond: make(chan struct{}), stopped: make(chan struct{})}
}

func (w *generationWriter) Write(b []byte) (int, error) {
	var entered, finished chan struct{}
	switch w.count.Add(1) {
	case 1:
		entered, finished = w.first, w.finishFirst
	case 2:
		entered, finished = w.second, w.finishSecond
	default:
		return len(b), nil
	}
	close(entered)
	select {
	case <-finished:
		return len(b), nil
	case <-w.stopped:
		return 0, io.ErrClosedPipe
	}
}

func (w *generationWriter) stop() { w.stopOnce.Do(func() { close(w.stopped) }) }

func generationWait(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("TIMEOUT " + label)
	}
}

func generationResult(t *testing.T, result <-chan error, label string) {
	t.Helper()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("FAIL %s: %v", label, err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("TIMEOUT " + label)
	}
}

func generationTimer(t *testing.T, duration time.Duration) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-time.After(duration + 2*time.Second):
		t.Fatal("TIMEOUT production stall window timer")
	}
}

func generationHealthy(t *testing.T, p *Peer) {
	t.Helper()
	select {
	case <-p.Done():
		t.Fatal("FAIL stale stall observation retired a healthy frame:", p.Err())
	case <-time.After(300 * time.Millisecond):
	}
}

// Regression from the independent review: the control watcher spans two
// withdrawals. Its Progress value for the first stalled frame is held until
// that frame completes and the second starts; the old window must not retire it.
func TestStallGenerationPausedControlSample(t *testing.T) {
	w := newGenerationWriter()
	counted := &ProgressWriter{W: w}
	p := New("generation", counted)
	p.Interrupt = w.stop
	sampled, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	var releaseOnce sync.Once
	p.Progress = func() int64 {
		value := counted.Accepted()
		if armed.CompareAndSwap(true, false) {
			close(sampled)
			<-release
		}
		return value
	}
	defer func() {
		releaseOnce.Do(func() { close(release) })
		p.mu.Lock()
		control := p.controlDone
		p.mu.Unlock()
		p.Close(ErrClosed)
		if control != nil {
			generationWait(t, control, "control worker exit")
		}
	}()
	p.cancelBestEffort("one")
	generationWait(t, w.first, "first withdrawal")
	p.cancelBestEffort("two")
	generationTimer(t, StallWindow-50*time.Millisecond)
	armed.Store(true)
	generationWait(t, sampled, "paused old-frame Progress sample")
	close(w.finishFirst)
	generationWait(t, w.second, "replacement withdrawal")
	p.mu.Lock()
	sequence, age := p.activeSeq, time.Since(p.activeSince)
	p.mu.Unlock()
	if sequence != 2 {
		t.Fatal("FAIL expected replacement active frame:", sequence)
	}
	releaseOnce.Do(func() { close(release) })
	generationHealthy(t, p)
	t.Logf("PASS old sample did not retire replacement: seq=%d initialAge=%v", sequence, age)
	close(w.finishSecond)
}

// Validation when sampling returns is not enough: frame completion/replacement
// can happen after observeStall reports true and before retirement commits.
func TestStallGenerationCommitRevalidatesFrame(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "new-ordinary-frame"}[replacement], func(t *testing.T) {
			w := newGenerationWriter()
			counted := &ProgressWriter{W: w}
			p := New("generation", counted)
			p.Interrupt, p.Progress = w.stop, counted.Accepted
			defer p.Close(ErrClosed)
			first := make(chan error, 1)
			go func() { first <- p.Notify("first", nil) }()
			generationWait(t, w.first, "first ordinary frame")
			var clock stallClock
			if p.observeStall(&clock) {
				t.Fatal("FAIL new frame already stalled")
			}
			generationTimer(t, StallWindow+100*time.Millisecond)
			if !p.observeStall(&clock) {
				t.Fatal("FAIL real unchanged frame was not observed stalled")
			}
			close(w.finishFirst)
			generationResult(t, first, "completed first frame")
			var next chan error
			if replacement {
				next = make(chan error, 1)
				go func() { next <- p.Notify("next", nil) }()
				generationWait(t, w.second, "new ordinary frame")
			}
			if p.retireStalled(&clock) {
				t.Fatal("FAIL stale observation committed retirement")
			}
			generationHealthy(t, p)
			if replacement {
				close(w.finishSecond)
				generationResult(t, next, "new ordinary frame remains usable")
			}
		})
	}
}

// A delayed sample after the final control frame completes must not turn an
// idle stream into a retired one; the next ordinary frame must still work.
func TestStallGenerationCompletedControlSample(t *testing.T) {
	w := newGenerationWriter()
	counted := &ProgressWriter{W: w}
	p := New("generation", counted)
	p.Interrupt = w.stop
	sampled, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	var releaseOnce sync.Once
	p.Progress = func() int64 {
		value := counted.Accepted()
		if armed.CompareAndSwap(true, false) {
			close(sampled)
			<-release
		}
		return value
	}
	defer func() {
		releaseOnce.Do(func() { close(release) })
		p.mu.Lock()
		control := p.controlDone
		p.mu.Unlock()
		p.Close(ErrClosed)
		if control != nil {
			generationWait(t, control, "completed control watcher exit")
		}
	}()
	p.cancelBestEffort("one")
	generationWait(t, w.first, "final withdrawal")
	generationTimer(t, StallWindow-50*time.Millisecond)
	armed.Store(true)
	generationWait(t, sampled, "paused final-frame sample")
	close(w.finishFirst)
	deadline := time.NewTimer(10 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer deadline.Stop()
	defer tick.Stop()
	for {
		p.mu.Lock()
		active := p.activeOn
		p.mu.Unlock()
		if !active {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("TIMEOUT final frame completion")
		}
	}
	releaseOnce.Do(func() { close(release) })
	generationHealthy(t, p)
	next := make(chan error, 1)
	go func() { next <- p.Notify("ordinary", nil) }()
	generationWait(t, w.second, "ordinary frame after idle")
	close(w.finishSecond)
	generationResult(t, next, "ordinary frame after completed control")
}

// Current-frame cancellation still retires a true stall after the production
// window; Progress and the reentrant Interrupt callback must both run outside mu.
func TestStallGenerationCurrentWriteAndReentrantHook(t *testing.T) {
	w := newGenerationWriter()
	counted := &ProgressWriter{W: w}
	p := New("generation", counted)
	var interrupts atomic.Int32
	p.Progress = func() int64 { p.mu.Lock(); p.mu.Unlock(); return counted.Accepted() }
	p.Interrupt = func() {
		interrupts.Add(1)
		p.Close(ErrClosed)
		p.mu.Lock()
		p.mu.Unlock()
		w.stop()
	}
	defer p.Close(ErrClosed)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- p.Call(ctx, "blocked", nil, nil) }()
	generationWait(t, w.first, "current write")
	began := time.Now()
	cancel()
	generationWait(t, p.Done(), "real current-frame stall retirement")
	if !errors.Is(p.Err(), ErrStalled) {
		t.Fatal("FAIL true stall identity:", p.Err())
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("FAIL cancellation identity:", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("TIMEOUT reentrant interrupt or current writer exit")
	}
	if elapsed := time.Since(began); elapsed < StallWindow-100*time.Millisecond || elapsed > StallWindow+2*time.Second {
		t.Fatal("FAIL true stall did not use the production window:", elapsed)
	}
	if interrupts.Load() != 1 {
		t.Fatal("FAIL reentrant interrupt was not exactly once:", interrupts.Load())
	}
}

// Admission has its own stall observer. A sampled ordinary frame that completes
// while that observer is paused must leave its queued withdrawal deliverable.
func TestStallGenerationAdmitControlSample(t *testing.T) {
	w := newGenerationWriter()
	counted := &ProgressWriter{W: w}
	p := New("generation", counted)
	p.Interrupt = w.stop
	sampled, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	var releaseOnce sync.Once
	p.Progress = func() int64 {
		value := counted.Accepted()
		if armed.CompareAndSwap(true, false) {
			close(sampled)
			<-release
		}
		return value
	}
	defer func() {
		releaseOnce.Do(func() { close(release) })
		p.mu.Lock()
		control := p.controlDone
		p.mu.Unlock()
		p.Close(ErrClosed)
		if control != nil {
			generationWait(t, control, "admission worker exit")
		}
	}()
	first := make(chan error, 1)
	go func() { first <- p.Notify("ordinary", nil) }()
	generationWait(t, w.first, "ordinary frame before queued withdrawal")
	p.cancelBestEffort("queued")
	generationTimer(t, StallWindow-50*time.Millisecond)
	armed.Store(true)
	generationWait(t, sampled, "paused admission Progress sample")
	close(w.finishFirst)
	generationResult(t, first, "ordinary frame completion")
	releaseOnce.Do(func() { close(release) })
	generationWait(t, w.second, "withdrawal admission after stale sample")
	generationHealthy(t, p)
	close(w.finishSecond)
	p.mu.Lock()
	control := p.controlDone
	p.mu.Unlock()
	if control != nil {
		generationWait(t, control, "queued withdrawal completion")
	}
}

func TestStallGenerationCloseCallbacksAreUnlocked(t *testing.T) {
	p := New("generation", io.Discard)
	var canceled, interrupted atomic.Int32
	p.runtimeCancel = func() {
		canceled.Add(1)
		p.Close(ErrClosed)
		p.mu.Lock()
		p.mu.Unlock()
	}
	p.Interrupt = func() {
		interrupted.Add(1)
		p.Close(ErrClosed)
		p.mu.Lock()
		p.mu.Unlock()
	}
	done := make(chan struct{})
	go func() { p.Close(ErrStalled); close(done) }()
	generationWait(t, done, "reentrant runtime and transport closure")
	if canceled.Load() != 1 || interrupted.Load() != 1 || !errors.Is(p.Err(), ErrStalled) {
		t.Fatal("FAIL reentrant close lost once-only hooks or original reason")
	}
}
