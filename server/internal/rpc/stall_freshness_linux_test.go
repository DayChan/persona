//go:build linux

package rpc

import (
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpcio"
)

type freshnessSocket struct {
	peer    *Peer
	counted *ProgressWriter
	reader  io.ReadCloser
	live    func() int64
	written chan error
}

func newFreshnessSocket(t *testing.T) *freshnessSocket {
	t.Helper()
	writer, reader := socketPair(t)
	counted := &ProgressWriter{W: writer}
	live := ConsumedProgress(counted.Accepted, rpcio.OutstandingQuery(writer))
	if live == nil {
		t.Fatal("FAIL unix consumption signal missing")
	}
	peer := New("freshness", counted)
	peer.Interrupt = func() { _ = writer.Close() }
	peer.Progress = live
	f := &freshnessSocket{peer: peer, counted: counted, reader: reader, live: live, written: make(chan error, 1)}
	t.Cleanup(func() {
		peer.Close(ErrClosed)
		_ = reader.Close()
		select {
		case <-f.written:
		case <-time.After(10 * time.Second):
			t.Error("TIMEOUT own unix writer exit")
		}
	})
	return f
}

func (f *freshnessSocket) fill(t *testing.T) int64 {
	t.Helper()
	go func() { f.written <- f.peer.Notify("large", strings.Repeat("x", 600<<10)) }()
	deadline := time.NewTimer(5 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer deadline.Stop()
	defer tick.Stop()
	var prior int64
	for {
		select {
		case <-tick.C:
			accepted := f.counted.Accepted()
			if accepted > 0 && accepted == prior {
				return accepted
			}
			prior = accepted
		case <-deadline.C:
			t.Fatal("TIMEOUT full unix queue")
		}
	}
}

func (f *freshnessSocket) drainWhileDelayed(t *testing.T) (int, time.Duration) {
	t.Helper()
	if c, ok := f.reader.(interface{ SetReadDeadline(time.Time) error }); ok {
		if err := c.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	previous, largestGap := time.Now(), time.Duration(0)
	consumed, token := 0, f.live()
	for range 12 {
		select {
		case <-tick.C:
		case <-time.After(time.Second):
			t.Fatal("TIMEOUT paced full-buffer consumption")
		}
		if _, err := io.ReadFull(f.reader, make([]byte, 4096)); err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		if gap := now.Sub(previous); gap > largestGap {
			largestGap = gap
		}
		previous = now
		consumed += 4096
		next := f.live()
		if next == token {
			t.Fatal("FAIL full-buffer consumption was invisible")
		}
		token = next
	}
	if largestGap >= StallWindow {
		t.Fatal("FAIL fixture really paused for the stall window:", largestGap)
	}
	return consumed, largestGap
}

// The same real 600 KiB frame remains active while whole 4 KiB buffers are read
// every 500 ms. Delay both before and after obtaining a token: neither latency
// may prove a stall or backdate a newly obtained token's next window.
func TestStallFreshnessDelayedSameFrame(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-token", true: "after-token"}[after], func(t *testing.T) {
			f := newFreshnessSocket(t)
			sampled, release := make(chan struct{}), make(chan struct{})
			var armed atomic.Bool
			var releaseOnce sync.Once
			f.peer.Progress = func() int64 {
				if armed.CompareAndSwap(true, false) {
					var value int64
					if after {
						value = f.live()
					}
					close(sampled)
					<-release
					if !after {
						value = f.live()
					}
					return value
				}
				return f.live()
			}
			defer releaseOnce.Do(func() { close(release) })
			accepted := f.fill(t)
			var clock stallClock
			if f.peer.observeStall(&clock) {
				t.Fatal("FAIL fresh frame initially stalled")
			}
			sequence := clock.seq
			armed.Store(true)
			decision := make(chan bool, 1)
			go func() { decision <- f.peer.observeStall(&clock) }()
			generationWait(t, sampled, "same-frame delayed measurement")
			consumed, gap := f.drainWhileDelayed(t)
			f.peer.mu.Lock()
			same := f.peer.activeOn && f.peer.activeSeq == sequence
			f.peer.mu.Unlock()
			if !same || f.counted.Accepted() != accepted {
				t.Fatal("FAIL fixture did not preserve the same blocked frame")
			}
			releasedAt := time.Now()
			releaseOnce.Do(func() { close(release) })
			select {
			case observed := <-decision:
				if observed {
					t.Fatal("FAIL callback latency became a same-frame stall")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("TIMEOUT delayed measurement return")
			}
			if f.peer.retireStalled(&clock) || clock.since.Before(releasedAt) {
				t.Fatal("FAIL stale measurement committed or backdated progress")
			}
			if f.peer.observeStall(&clock) || clock.since.Before(releasedAt) {
				t.Fatal("FAIL recovered token reused the stale interval")
			}
			if errors.Is(f.peer.Err(), ErrStalled) {
				t.Fatal("FAIL healthy same frame retired")
			}
			t.Logf("PASS modeAfter=%v accepted=%d consumed=%d maxReadGap=%v seq=%d stale interval discarded", after, accepted, consumed, gap, sequence)
		})
	}
}

func currentFreshnessCandidate(t *testing.T, f *freshnessSocket) stallClock {
	t.Helper()
	f.fill(t)
	var clock stallClock
	if f.peer.observeStall(&clock) {
		t.Fatal("FAIL new frame initially stalled")
	}
	generationTimer(t, StallWindow+100*time.Millisecond)
	if !f.peer.observeStall(&clock) {
		t.Fatal("FAIL genuine zero progress did not produce a candidate")
	}
	return clock
}

// Frame identity is unchanged, but progress after the candidate observation
// must be freshly checked before publishing retirement.
func TestStallFreshnessProgressBeforeCommit(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-progress", true: "query-unavailable"}[unavailable], func(t *testing.T) {
			f := newFreshnessSocket(t)
			clock := currentFreshnessCandidate(t, f)
			if unavailable {
				f.peer.Progress = func() int64 { return progressUnavailable }
			} else if _, err := io.ReadFull(f.reader, make([]byte, 4096)); err != nil {
				t.Fatal(err)
			}
			progressAt := time.Now()
			if f.peer.retireStalled(&clock) {
				t.Fatal("FAIL progress or unavailability between sample and commit was ignored")
			}
			f.peer.Progress = f.live
			if clock.since.Before(progressAt) || f.peer.observeStall(&clock) {
				t.Fatal("FAIL new progress or reliable recovery was backdated")
			}
			generationHealthy(t, f.peer)
		})
	}
}

// The final confirmation callback is subject to the same freshness bound as
// observeStall, even when its sampled frame never changes.
func TestStallFreshnessDelayedCommitSample(t *testing.T) {
	f := newFreshnessSocket(t)
	clock := currentFreshnessCandidate(t, f)
	sampled, release := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	var once sync.Once
	f.peer.Progress = func() int64 {
		value := f.live()
		if armed.CompareAndSwap(true, false) {
			close(sampled)
			<-release
		}
		return value
	}
	defer once.Do(func() { close(release) })
	armed.Store(true)
	result := make(chan bool, 1)
	go func() { result <- f.peer.retireStalled(&clock) }()
	generationWait(t, sampled, "final confirmation sample")
	consumed, gap := f.drainWhileDelayed(t)
	releasedAt := time.Now()
	once.Do(func() { close(release) })
	select {
	case committed := <-result:
		if committed {
			t.Fatal("FAIL delayed confirmation retired a progressing frame")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("TIMEOUT final confirmation return")
	}
	if clock.since.Before(releasedAt) || f.peer.observeStall(&clock) {
		t.Fatal("FAIL confirmation recovery reused the stale interval")
	}
	t.Logf("PASS delayed confirmation discarded: consumed=%d maxReadGap=%v", consumed, gap)
}

// A callback can finish promptly but its consumer can wait for mu. Include that
// interval in confirmation freshness, without running owner I/O under mu.
func TestStallFreshnessCommitLockAdmission(t *testing.T) {
	f := newFreshnessSocket(t)
	clock := currentFreshnessCandidate(t, f)
	needLock, held, release, stop, exited := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	var releaseOnce, stopOnce sync.Once
	go func() {
		defer close(exited)
		select {
		case <-needLock:
		case <-stop:
			return
		}
		f.peer.mu.Lock()
		close(held)
		select {
		case <-release:
		case <-stop:
		}
		f.peer.mu.Unlock()
	}()
	defer func() {
		releaseOnce.Do(func() { close(release) })
		stopOnce.Do(func() { close(stop) })
		generationWait(t, exited, "own lock holder exit")
	}()
	f.peer.Progress = func() int64 {
		value := f.live()
		if armed.CompareAndSwap(true, false) {
			close(needLock)
			select {
			case <-held:
			case <-stop:
			}
		}
		return value
	}
	armed.Store(true)
	result := make(chan bool, 1)
	go func() { result <- f.peer.retireStalled(&clock) }()
	generationWait(t, held, "confirmation lock contention")
	if _, err := io.ReadFull(f.reader, make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	generationTimer(t, 2*controlStallPoll)
	releasedAt := time.Now()
	releaseOnce.Do(func() { close(release) })
	select {
	case committed := <-result:
		if committed {
			t.Fatal("FAIL lock admission made an expired confirmation authoritative")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("TIMEOUT confirmation lock admission")
	}
	if clock.since.Before(releasedAt) || f.peer.observeStall(&clock) {
		t.Fatal("FAIL expired publication reused an old token window")
	}
	generationHealthy(t, f.peer)
}

// Even a usable short invocation can acquire its changed token near return.
// Starting that new token's window at invocation start would retire it early.
func TestStallFreshnessRecentChangedToken(t *testing.T) {
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "observation", true: "confirmation"}[commit], func(t *testing.T) {
			f := newFreshnessSocket(t)
			var clock stallClock
			if commit {
				clock = currentFreshnessCandidate(t, f)
			} else {
				f.fill(t)
				if f.peer.observeStall(&clock) {
					t.Fatal("FAIL fresh frame initially stalled")
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var armed atomic.Bool
			var releaseOnce sync.Once
			f.peer.Progress = func() int64 {
				if armed.CompareAndSwap(true, false) {
					close(entered)
					<-release
				}
				return f.live()
			}
			defer releaseOnce.Do(func() { close(release) })
			armed.Store(true)
			result := make(chan bool, 1)
			go func() {
				if commit {
					result <- f.peer.retireStalled(&clock)
				} else {
					result <- f.peer.observeStall(&clock)
				}
			}()
			generationWait(t, entered, "usable delayed token acquisition")
			if _, err := io.ReadFull(f.reader, make([]byte, 4096)); err != nil {
				t.Fatal(err)
			}
			tokenAt := time.Now()
			releaseOnce.Do(func() { close(release) })
			select {
			case stalled := <-result:
				if stalled {
					t.Fatal("FAIL newly acquired token was treated as a stall")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("TIMEOUT usable token sampling")
			}
			if clock.progress == progressUnavailable {
				t.Fatal("FAIL usable fixture sample unexpectedly expired")
			}
			if clock.since.Before(tokenAt) {
				t.Fatal("FAIL changed token backdated to invocation start")
			}
		})
	}
}
