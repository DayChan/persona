//go:build linux || darwin

package host

import (
	"errors"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"golang.org/x/sys/unix"
)

func TestOwnedGroupPostReapObservationCannotSignal(t *testing.T) {
	grace := StopGrace
	StopGrace = 20 * time.Millisecond
	defer func() { StopGrace = grace }()
	old := exec.Command("sh", "-c", "exit 0")
	old.SysProcAttr = osproc.GroupAttr()
	if err := old.Run(); err != nil {
		t.Fatal(err)
	}
	neighbor := exec.Command("sh", "-c", "exec sleep 20")
	neighbor.SysProcAttr = osproc.GroupAttr()
	if err := neighbor.Start(); err != nil {
		t.Fatal(err)
	}
	var cleanup sync.Once
	endNeighbor := func() { cleanup.Do(func() { _ = neighbor.Process.Kill(); _ = neighbor.Wait() }) }
	defer endNeighbor()
	ch := &child{cmd: old, reaped: make(chan struct{}), exited: make(chan struct{}), stopRequest: make(chan struct{})}
	close(ch.reaped)
	observerErr := errors.New("synthetic group observer denied")
	var disposed atomic.Bool
	observed := make(chan struct{})
	var first atomic.Bool
	// This is an occupied-number seam, not actual PID reuse. We never force the
	// system PID allocator to cycle. The real neighbor must remain unaffected
	// while the post-reap completion path sees an occupied group or a query error.
	go ch.finishRun(func() { disposed.Store(true) }, func() (bool, error) {
		if first.CompareAndSwap(false, true) {
			close(observed)
			return true, observerErr
		}
		return ownedGroupAbsent(neighbor.Process.Pid)
	}, time.Millisecond)
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Fatal("TIMEOUT observation entry")
	}
	p := &Proc{}
	started := time.Now()
	err := p.terminate(ch)
	if !errors.Is(err, ErrTeardownTimeout) || !errors.Is(err, observerErr) {
		t.Fatal("FAIL observer error swallowed", err)
	}
	if time.Since(started) > 300*time.Millisecond {
		t.Fatal("FAIL bounded post-reap teardown")
	}
	if disposed.Load() {
		t.Fatal("FAIL occupied numeric group released ownership")
	}
	if unix.Kill(neighbor.Process.Pid, 0) != nil {
		t.Fatal("FAIL post-reap path signaled neighbor")
	}
	endNeighbor()
	select {
	case <-ch.exited:
	case <-time.After(time.Second):
		t.Fatal("TIMEOUT late group absence")
	}
	if !disposed.Load() {
		t.Fatal("FAIL disposal after actual query absence")
	}
	t.Log("PASS observation-only occupied-number seam, bounded observer error, actual neighbor isolation and late cleanup; no PID-reuse experiment")
}
