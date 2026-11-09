//go:build linux

package osproc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// identify reads the process's start time from /proc (field 22 of stat, clock
// ticks since boot): a resolution a reused pid cannot land in twice.
func identify(pid int) ProcIdentity {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcIdentity{}
	}
	// The comm field can hold spaces and parentheses; every field after it
	// follows its last ')'. starttime is field 22, the 20th from state (3).
	rest := bytes.LastIndexByte(b, ')')
	if rest < 0 || rest+2 >= len(b) {
		return ProcIdentity{}
	}
	fields := strings.Fields(string(b[rest+2:]))
	if len(fields) < 20 {
		return ProcIdentity{}
	}
	return ProcIdentity{Start: "ticks:" + fields[19]}
}

// killPinned pins the instance with a pidfd and signals that: a pid reused
// between the identity check and the kill is never hit.
func (id ProcIdentity) killPinned(pid int) (bool, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		// No pidfd here (a kernel before 5.3, or the process already gone):
		// fall back to the checked-by-pid kill.
		return false, nil
	}
	defer unix.Close(fd)
	// The fd names the instance pidfd_open saw; the pid may already name a
	// newer one. Only signal the pinned instance while the pid still does.
	if identify(pid) != id {
		return true, nil
	}
	if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, syscall.ESRCH) {
		return true, err
	}
	return true, nil
}
