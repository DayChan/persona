//go:build linux

package osproc

import (
	"os"
	"strconv"

	"github.com/nwverse/persona/server/internal/procinfo"
)

// ownChildren are the processes /proc lists with this one as their parent, zombies included. None when /proc cannot
// be read: nothing is reaped then, as before.
func ownChildren() []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getpid()
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if parent, err := procinfo.Parent(pid); err == nil && parent == self {
			pids = append(pids, pid)
		}
	}
	return pids
}
