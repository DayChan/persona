//go:build linux || darwin

package osproc

import (
	"errors"
	"sync"
	"syscall"
)

var reapOnce sync.Once

// ReapInherited waits in the background for every child this process already has, so that one that exits is reaped
// instead of staying a zombie for as long as this process runs.
//
// A program that replaces itself with syscall.Exec keeps its pid, and with it every child its previous image started:
// the resident node restarting or updating in place (the update watchdog, the herdr server, bridges), the hub's
// self-update and rollback (its manager, its watchdog), a manager back by reexec. Those are this image's children now,
// but no exec.Cmd here knows them, so nothing else ever waits for them. An init process in a container does not help
// either: their parent is this process, not init.
//
// Call it first thing, next to SealInherited, before this process starts a child of its own: a child started earlier
// is waited for here too, and its exec.Cmd.Wait then fails. Only the first call does anything. It lists the children
// once, in /proc on Linux and kern.proc.all on macOS; elsewhere it does nothing.
func ReapInherited() {
	reapOnce.Do(func() {
		for _, pid := range ownChildren() {
			go reap(pid)
		}
	})
}

// reap waits for this one child until it exits. Never for any child (-1): that could take one an exec.Cmd waits for.
func reap(pid int) {
	var ws syscall.WaitStatus
	for {
		if _, err := syscall.Wait4(pid, &ws, 0, nil); !errors.Is(err, syscall.EINTR) {
			return
		}
	}
}
