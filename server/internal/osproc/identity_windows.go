//go:build windows

package osproc

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// identifyOf reads one process handle's creation time: the kernel's own
// instance identity, exact to 100 ns, that a reused pid cannot repeat.
func identifyOf(h windows.Handle) ProcIdentity {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return ProcIdentity{}
	}
	return ProcIdentity{Start: fmt.Sprintf("ft:%d", created.Nanoseconds())}
}

// identify opens the process to read the same creation time identifyOf reads.
func identify(pid int) ProcIdentity {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return ProcIdentity{}
	}
	defer windows.CloseHandle(h)
	return identifyOf(h)
}

// killPinned carries the check and the kill on one process handle — a handle
// names an instance, not a pid — so a pid reused in between is never hit.
func (id ProcIdentity) killPinned(pid int) (bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// No such process, or not ours to open: not the recorded instance.
		return true, nil
	}
	defer windows.CloseHandle(h)
	if identifyOf(h) != id {
		return true, nil
	}
	if err := windows.TerminateProcess(h, 1); err != nil && Alive(pid) {
		return true, err
	}
	return true, nil
}
