package osproc

// ProcIdentity is a platform-exact identity of one process instance, taken once
// and matched later, so a pid a new process has reused is never mistaken for
// the process that was recorded. Start is the platform's native start time at
// its full resolution: clock ticks since boot on Linux, microseconds since the
// epoch on macOS, the 100 ns creation time on Windows. The zero value matches
// nothing, so a platform that gives no reading safely degrades to whatever
// weaker check the caller keeps on its own.
type ProcIdentity struct {
	Start string
}

// Identify takes the identity of pid. A process that does not exist, or one
// whose start time the platform cannot read, gives the zero value.
func Identify(pid int) ProcIdentity {
	if pid < 1 {
		return ProcIdentity{}
	}
	return identify(pid)
}

// StillIs says whether pid is still running and is still that exact process
// instance. A pid reused by another process starts at another time and fails.
func (id ProcIdentity) StillIs(pid int) bool {
	if pid < 1 || id.Start == "" {
		return false
	}
	return identify(pid) == id
}

// KillIfStill ends the process pid, but only when it is still the exact
// instance this identity was taken from. Where the platform can pin an
// instance (a pidfd on Linux, a process handle on Windows) the check and the
// kill act on that one instance, so a pid reused between the two is never hit.
// The process being gone, or no longer that instance, is not an error.
func (id ProcIdentity) KillIfStill(pid int) error {
	if pid < 1 || id.Start == "" {
		return nil
	}
	if handled, err := id.killPinned(pid); handled {
		return err
	}
	// No pinning here: verify the identity, then kill by pid — the window
	// between the two one syscall wide.
	if !id.StillIs(pid) {
		return nil
	}
	return Kill(pid)
}
