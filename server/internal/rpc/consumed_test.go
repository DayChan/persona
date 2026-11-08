package rpc

import (
	"sync"
	"testing"
	"time"
)

func TestConsumedProgressUnavailableTransportIsFailSafe(t *testing.T) {
	if ConsumedProgress(func() int64 { return 0 }, nil) != nil {
		t.Fatal("unmeasurable transport acquired a progress signal")
	}
}

func TestConsumedProgressSeparatesQueueMemoryFromBytes(t *testing.T) {
	accepted, queued := int64(4096), int64(4864)
	progress := ConsumedProgress(func() int64 { return accepted }, func() (int64, bool) { return queued, true })
	first := progress()
	if again := progress(); again != first {
		t.Fatal("unchanged measurements advanced progress")
	}
	// Equal numeric changes in different units must not cancel each other out.
	accepted += 4096
	queued += 4096
	second := progress()
	if second <= first {
		t.Fatal("accepted bytes did not advance the opaque token")
	}
	queued -= 4864
	third := progress()
	if third <= second {
		t.Fatal("queue release while Write was blocked did not advance progress")
	}
	queued += 4096
	if progress() != third {
		t.Fatal("queue growth without accepted progress advanced the token")
	}
}

func TestConsumedProgressUnavailableMeasurementIsFailSafe(t *testing.T) {
	available := true
	progress := ConsumedProgress(func() int64 { return 0 }, func() (int64, bool) { return 0, available })
	base := progress()
	peer := New("test", nil)
	peer.Progress = progress
	peer.activeOn, peer.activeSeq, peer.activeBase = true, 1, base
	peer.activeSince = time.Now().Add(-2 * StallWindow)
	var clock stallClock
	available = false
	if progress() != progressUnavailable || peer.observeStall(&clock) {
		t.Fatal("failed descriptor query was treated as a measured stall")
	}
	available = true
	// Recovery with the same token needs a new window, not the old Write age.
	if peer.observeStall(&clock) || time.Since(clock.since) > time.Second {
		t.Fatal("measurement recovery used the unavailable interval as a stall")
	}
	clock.since = time.Now().Add(-2 * StallWindow)
	if !peer.observeStall(&clock) {
		t.Fatal("a reliably measured stall was suppressed after recovery")
	}
}

func TestConsumedProgressConcurrentObservers(t *testing.T) {
	progress := ConsumedProgress(func() int64 { return 0 }, func() (int64, bool) { return 0, true })
	first := progress()
	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			for range 100 {
				if progress() != first {
					t.Error("stable concurrent measurement changed the token")
				}
			}
		}()
	}
	group.Wait()
}
