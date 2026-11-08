//go:build unix && !linux

package supervise

// sessionMembers: no listing of a session's processes here; killGroup kills the process group alone.
func sessionMembers(int) []int { return nil }
