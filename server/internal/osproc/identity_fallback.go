//go:build !linux && !darwin && !windows

package osproc

// identify gives no exact start time on other platforms: the caller's portable
// checks (a second-resolution start time, ownership) stay the whole identity.
func identify(int) ProcIdentity { return ProcIdentity{} }

// killPinned cannot pin a process instance here; the unpinned path applies.
func (ProcIdentity) killPinned(int) (bool, error) { return false, nil }
