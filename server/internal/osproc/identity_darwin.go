//go:build darwin

package osproc

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// identify asks the kernel for the process's start time (kern.proc.pid), exact
// to the microsecond — far past what a pid reused within the same second can dodge.
func identify(pid int) ProcIdentity {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return ProcIdentity{}
	}
	st := info.Proc.P_starttime
	return ProcIdentity{Start: fmt.Sprintf("usec:%d", st.Sec*1000000+int64(st.Usec))}
}

// killPinned cannot pin a process instance on macOS; the unpinned path checks
// the exact identity and kills by pid, its window one syscall wide.
func (ProcIdentity) killPinned(int) (bool, error) { return false, nil }
