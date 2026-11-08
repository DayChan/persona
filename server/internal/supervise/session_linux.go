package supervise

import (
	"os"
	"strconv"
	"strings"
)

// sessionMembers are the live processes in session sid (zombies, which a kill cannot end, left out), from
// /proc/<pid>/stat: after the command's closing parenthesis come its state, parent, process group and session.
func sessionMembers(sid int) []int {
	// A /proc of another pid namespace (unshare without --mount-proc) names other processes by these numbers: none.
	if self, err := os.Readlink("/proc/self"); err != nil || self != strconv.Itoa(os.Getpid()) {
		return nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		s := string(b)
		i := strings.LastIndexByte(s, ')')
		if i < 0 {
			continue
		}
		f := strings.Fields(s[i+1:])
		if len(f) < 4 || f[0] == "Z" || f[0] == "X" {
			continue
		}
		if session, err := strconv.Atoi(f[3]); err == nil && session == sid {
			out = append(out, pid)
		}
	}
	return out
}
