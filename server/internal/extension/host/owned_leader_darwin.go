//go:build darwin

package host

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func observeOwnedLeader(pid int) (bool, error) {
	// Darwin's kern.proc.pid does not reap. The sole Wait owner has not yet
	// called Wait, so its original child (including SZOMB) cannot be reused.
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	if info == nil || info.Proc.P_pid != int32(pid) || info.Eproc.Ppid != int32(os.Getpid()) {
		return false, fmt.Errorf("extension host: original child ownership unavailable")
	}
	const zombie = 5 // SZOMB in Darwin sys/proc.h
	return info.Proc.P_stat == zombie, nil
}
