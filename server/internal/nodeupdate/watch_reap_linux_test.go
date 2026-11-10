//go:build linux

package nodeupdate

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/procinfo"
)

// A Commit that stops after starting the real watchdog removes the marker, so the watchdog exits within one poll.
// The node keeps running, and it must reap that child: an unwaited one stays a zombie for the node's whole life.
func TestAnAbortedCommitReapsItsWatchdog(t *testing.T) {
	m, data, _ := testManager(t)
	var watchPID int
	var watchStart string
	m.startWatch = func(backup, path, id string) error {
		if err := startWatchdog(backup, path, id); err != nil {
			return err
		}
		p, err := loadPending(path)
		if err != nil {
			return err
		}
		watchPID, watchStart = p.WatchPID, p.WatchPrecise
		return nil
	}
	t.Cleanup(func() {
		// A watchdog that never exits would otherwise outlive the test until its deadline.
		if watchPID > 1 && watchStart != "" {
			_ = osproc.ProcIdentity{Start: watchStart}.KillIfStill(watchPID)
		}
	})
	id := receive(t, m, data, spec(data, "next"))
	busy := errors.New("an agent became active")
	if _, err := m.Commit(context.Background(), id, func() error { return busy }); !errors.Is(err, busy) {
		t.Fatalf("commit: %v, want %v", err, busy)
	}
	if watchPID < 2 {
		t.Fatal("the real watchdog did not start")
	}
	if _, err := os.Stat(markerPath(m.exe)); !os.IsNotExist(err) {
		t.Fatalf("aborted commit kept its marker: %v", err)
	}
	waitReaped(t, watchPID, 10*time.Second)
}

// waitReaped polls /proc until pid is gone (or holds a process that started later), and fails with the last state
// it saw, Z for a zombie nobody waited for.
func waitReaped(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	first, err := procinfo.Stat(pid)
	if err != nil {
		return
	}
	start := func(fields []string) string {
		if len(fields) > 19 {
			return fields[19] // field 22, starttime
		}
		return ""
	}
	birth := start(first)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(timeout)
	for {
		fields, err := procinfo.Stat(pid)
		if err != nil || start(fields) != birth {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("watchdog %d was not reaped within %s: state %v", pid, timeout, fields[:min(2, len(fields))])
		case <-ticker.C:
		}
	}
}
