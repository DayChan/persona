//go:build unix

package supervise

import (
	"os/exec"
	"syscall"
	"time"
)

// execHub replaces this process with `self hub run`: the hub runs as it did before the supervisor (its own pid, the
// container's signals straight to it).
func execHub(self string, env []string) error {
	return syscall.Exec(self, []string{self, "hub", "run"}, env)
}

// ownGroup has cmd start a session of its own, which is a process group of its own too. What it starts stays in that
// session even in a process group of its own (an extension's program), unless it makes a session of its own.
func ownGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// Rounds of killGroup's sweep of the session, and the pause between them: what a member forked while it was killed is
// found in the next one.
const (
	sweepRounds = 5
	sweepPause  = 20 * time.Millisecond
)

// killGroup kills what is left of cmd's session once it stopped: its process group, and every other process still
// in the session where the system lists them (sessionMembers: Linux, which the image is, with this pid namespace's
// /proc). A session's id is not given to another process while a member is left, so the sweep never reaches past it.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	sid := cmd.Process.Pid
	_ = syscall.Kill(-sid, syscall.SIGKILL)
	for i := 0; i < sweepRounds; i++ {
		members := sessionMembers(sid)
		if len(members) == 0 {
			return
		}
		for _, pid := range members {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(sweepPause)
	}
}
