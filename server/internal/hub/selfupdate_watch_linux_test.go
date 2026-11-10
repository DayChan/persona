//go:build linux

package hub

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/procinfo"
)

// When the exec into the new version fails, the hub removes the pending marker, the watchdog exits, and the hub
// goes on running: it must reap the watchdog, or the watchdog stays a zombie for the hub's whole life. A stand-in
// for the old binary writes its pid and exits at once.
func TestTheUpdateWatchdogIsReaped(t *testing.T) {
	dir := t.TempDir()
	prev := filepath.Join(dir, "prev")
	script := `#!/bin/sh
d=$(dirname "$2")
echo $$ >"$d/pid.tmp" && mv "$d/pid.tmp" "$d/pid"
`
	if err := os.WriteFile(prev, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := startWatchdog(prev, filepath.Join(dir, "pending.json")); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.After(10 * time.Second)
	pid := 0
	for {
		if pid == 0 {
			if b, err := os.ReadFile(filepath.Join(dir, "pid")); err == nil {
				if pid, err = strconv.Atoi(strings.TrimSpace(string(b))); err != nil || pid < 2 {
					t.Fatalf("watchdog pid %q: %v", b, err)
				}
			}
		}
		var fields []string
		if pid != 0 {
			var err error
			if fields, err = procinfo.Stat(pid); err != nil {
				return // reaped
			}
		}
		select {
		case <-deadline:
			t.Fatalf("watchdog %d was not reaped within 10s: state %v", pid, fields[:min(2, len(fields))])
		case <-ticker.C:
		}
	}
}
