//go:build unix

package osproc

import (
	"os"
	"runtime"
	"strconv"
	"syscall"
)

// SealInherited makes every descriptor this process holds above stderr close-on-exec, and drops socket activation's
// variables (LISTEN_PID, LISTEN_FDS, LISTEN_FDNAMES) when they were meant for another process. Call it first thing in
// a long-running process that starts others and was handed nothing past stderr on purpose (a manager, a node): a
// descriptor a parent left inheritable, such as an older hub's listening socket, then stays here instead of reaching
// every program this process starts, and through them every program those start (docs/operations/hub-restart.md).
//
// This process keeps using what it holds; only an exec drops it. A descriptor handed to a child on purpose through
// exec.Cmd.ExtraFiles still reaches it: the child gets a copy whatever the flag says. Never call it in a process that
// hands an inherited descriptor on by exec without ExtraFiles (the sandbox's launcher passes its mount data that way).
func SealInherited() {
	for _, fd := range openFDs() {
		if fd > 2 {
			syscall.CloseOnExec(fd)
		}
	}
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		for _, k := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
			_ = os.Unsetenv(k)
		}
	}
}

// fdScanMost bounds the scan by number when no directory lists the open descriptors.
const fdScanMost = 1 << 16

// openFDs are this process's descriptors as /proc/self/fd (Linux) or /dev/fd (macOS) lists them. On other systems,
// whose /dev/fd may hold fixed device nodes instead (OpenBSD and NetBSD: 0 to 63), or where neither gives a listing
// listFDs trusts, they are every number below the descriptor limit, at least 1024 and at most fdScanMost. Marking a
// closed or reused number does no harm.
func openFDs() []int {
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
			if fds, ok := listFDs(dir); ok {
				return fds
			}
		}
	}
	most := uint64(1024)
	var lim syscall.Rlimit
	if syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim) == nil {
		// Cur is signed on some systems; a negative value or RLIM_INFINITY clamps to fdScanMost.
		most = max(most, min(uint64(lim.Cur), fdScanMost))
	}
	fds := make([]int, most)
	for i := range fds {
		fds[i] = i
	}
	return fds
}

// listFDs reads dir as the list of this process's descriptors, trusted only when it shows the descriptor past stderr
// used to read it, which a fixed listing does not (FreeBSD's /dev/fd without fdescfs: 0, 1 and 2 whatever is open).
// That descriptor is closed by the time the list is returned.
func listFDs(dir string) ([]int, bool) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	own := int(f.Fd())
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, false
	}
	fds := make([]int, 0, len(entries))
	seen := false
	for _, e := range entries {
		if fd, err := strconv.Atoi(e.Name()); err == nil {
			fds = append(fds, fd)
			seen = seen || fd == own
		}
	}
	return fds, seen && own > 2
}
