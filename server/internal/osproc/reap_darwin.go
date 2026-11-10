//go:build darwin

package osproc

import (
	"os"

	"golang.org/x/sys/unix"
)

// ownChildren are the processes the kernel lists with this one as their parent (kern.proc.all), zombies included.
// None when the list cannot be read: nothing is reaped then, as before.
func ownChildren() []int {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	self := int32(os.Getpid())
	var pids []int
	for _, p := range procs {
		if p.Eproc.Ppid == self {
			pids = append(pids, int(p.Proc.P_pid))
		}
	}
	return pids
}
