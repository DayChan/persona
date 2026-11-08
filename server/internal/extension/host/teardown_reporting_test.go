//go:build linux

package host

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
)

type terminalObserver struct{ active, starts, stops, exits atomic.Int32 }

func (*terminalObserver) Call(string, time.Duration, string) {}
func (*terminalObserver) Log(string, string)                 {}
func (o *terminalObserver) Run(event, _ string) {
	switch event {
	case RunStart:
		o.active.Add(1)
		o.starts.Add(1)
	case RunStop:
		o.active.Add(-1)
		o.stops.Add(1)
	case RunExit:
		o.active.Add(-1)
		o.exits.Add(1)
	}
}
func TestWriteLifecycleIndependentTeardownReport(t *testing.T) {
	for _, grace := range []time.Duration{40 * time.Millisecond, 60 * time.Millisecond} {
		t.Run(grace.String(), func(t *testing.T) {
			old := StopGrace
			StopGrace = grace
			t.Cleanup(func() { StopGrace = old })
			accept, dispose := make(chan struct{}), make(chan struct{})
			acceptRelease, disposeRelease := make(chan struct{}), make(chan struct{})
			var acceptOnce, disposeOnce sync.Once
			unblockAccept := func() { acceptOnce.Do(func() { close(acceptRelease) }) }
			unblockDispose := func() { disposeOnce.Do(func() { close(disposeRelease) }) }
			observer := &terminalObserver{}
			var preparations atomic.Int32
			f := lifecycleStart(t, false, func(s *Spec) {
				s.Observe = observer
				s.Init = func(context.Context) (extension.InitializeParams, error) { return extension.InitializeParams{}, nil }
				s.Accept = func(extension.InitializeResult) error {
					close(accept)
					select {
					case <-acceptRelease:
						return nil
					case <-time.After(3 * time.Second):
						return context.DeadlineExceeded
					}
				}
				s.Prepare = func(command string, args []string) (string, []string, []string, []*os.File, func(), error) {
					preparations.Add(1)
					return command, args, nil, nil, func() {
						close(dispose)
						select {
						case <-disposeRelease:
						case <-time.After(3 * time.Second):
						}
					}, nil
				}
			})
			t.Cleanup(func() { unblockAccept(); unblockDispose() })
			releaseHold := f.p.Hold()
			defer releaseHold()
			f.command(t, "read")
			if f.line(t) != "read initialize" {
				t.Fatal("FAIL initialization frame")
			}
			select {
			case <-accept:
			case <-time.After(2 * time.Second):
				t.Fatal("TIMEOUT Accept gate")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := f.p.StopContext(ctx)
			took := time.Since(start)
			if !errors.Is(err, ErrTeardownTimeout) || took < 3*grace || took > 3*grace+100*time.Millisecond {
				t.Fatalf("FAIL independent report: %v in %v (budget %v)", err, took, 3*grace)
			}
			assertTeardownLocalized(t, err)
			state, why := f.p.Status()
			if state != StateError || why != err.Error() {
				t.Fatal("FAIL real Proc structured teardown status", state, why)
			}
			if waitErr := f.p.Wait(ctx); !errors.Is(waitErr, ErrTeardownTimeout) {
				t.Fatal("FAIL Wait lost teardown sentinel", waitErr)
			}
			select {
			case <-dispose:
			case <-time.After(time.Second):
				t.Fatal("TIMEOUT actual reap/disposal gate")
			}
			if alive(f.pid) {
				t.Fatal("FAIL leader not actually reaped")
			}
			f.p.mu.Lock()
			holds := f.p.holds
			f.p.mu.Unlock()
			if holds != 1 || observer.active.Load() != 1 || observer.exits.Load() != 0 {
				t.Fatal("FAIL ownership/active_runs released on failure")
			}
			select {
			case <-f.p.Done():
				t.Fatal("FAIL false completion")
			default:
			}
			// A repeated report is idempotent, even while Accept still owns the run loop.
			for i := 0; i < 10; i++ {
				if err := f.p.StopContext(ctx); !errors.Is(err, ErrTeardownTimeout) {
					t.Fatal(err)
				}
			}
			unblockAccept()
			select {
			case <-f.p.Done():
				t.Fatal("FAIL disposal ownership released early")
			case <-time.After(20 * time.Millisecond):
			}
			unblockDispose()
			select {
			case <-f.p.Done():
			case <-time.After(2 * time.Second):
				t.Fatal("TIMEOUT late completion")
			}
			if observer.active.Load() != 0 || observer.starts.Load() != 1 || observer.exits.Load() != 1 || observer.stops.Load() != 0 || preparations.Load() != 1 {
				t.Fatal("FAIL terminal failure event or replacement")
			}
			if f.p.Ready() || f.p.Generation() != 0 {
				t.Fatal("FAIL late Accept republished retired epoch")
			}
			t.Logf("PASS independent teardown failure in %v; actual holds retained, late single terminal event", took)
		})
	}
}

func TestWriteLifecycleExplicitStopBalancesRun(t *testing.T) {
	old := StopGrace
	StopGrace = 100 * time.Millisecond
	t.Cleanup(func() { StopGrace = old })
	observer := &terminalObserver{}
	f := lifecycleStart(t, true, func(s *Spec) { s.Observe = observer })
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i := 0; i < 10; i++ {
		if err := f.p.StopContext(ctx); err != nil {
			t.Fatal("FAIL successful Stop", err)
		}
	}
	if alive(f.pid) || observer.active.Load() != 0 || observer.starts.Load() != 1 || observer.stops.Load()+observer.exits.Load() != 1 {
		t.Fatal("FAIL actual Stop did not balance RunStart exactly once")
	}
	t.Log("PASS actual Stop/Done balances active_runs once")
}
