//go:build !unix

package supervise

import (
	"os"
	"os/exec"
)

// execHub runs `self hub run` and waits for it: there is no exec in place here.
func execHub(self string, env []string) error {
	cmd := exec.Command(self, "hub", "run")
	cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// ownGroup and killGroup: no process groups here.
func ownGroup(*exec.Cmd)  {}
func killGroup(*exec.Cmd) {}
