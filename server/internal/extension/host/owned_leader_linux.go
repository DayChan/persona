//go:build linux

package host

import "golang.org/x/sys/unix"

func observeOwnedLeader(pid int) (bool, error) {
	var info unix.Siginfo
	err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOHANG|unix.WNOWAIT, nil)
	return info.Signo != 0, err
}
