//go:build unix

package osproc

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

// startSleeper starts a child that waits for SIGKILL and returns its command.
func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exec sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func TestIdentifyGivesExactStableIdentity(t *testing.T) {
	self := Identify(os.Getpid())
	if self.Start == "" {
		t.Fatal("no exact start time for this process")
	}
	if Identify(os.Getpid()) != self {
		t.Fatalf("identity unstable: %q then %q", self.Start, Identify(os.Getpid()).Start)
	}
	// Two processes forked within one tick of the platform's resolution can
	// share a start time (Linux counts 100 per second); identity only has to
	// separate a pid from whatever reuses it much later, never two live ones.
	child := startSleeper(t)
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	if id := Identify(child.Process.Pid); id.Start == "" {
		t.Fatal("no exact start time for a child process")
	}
	if Identify(-1).Start != "" || Identify(0).Start != "" {
		t.Fatal("impossible pid gave an identity")
	}
}

func TestIdentityStillIsAndKillIfStill(t *testing.T) {
	child := startSleeper(t)
	id := Identify(child.Process.Pid)
	if id.Start == "" {
		t.Skip("platform gives no exact start time")
	}
	if !id.StillIs(child.Process.Pid) {
		t.Fatal("running child not recognized as itself")
	}
	// A near miss — the identity of another instance — must not kill.
	changed := Identify(child.Process.Pid)
	changed.Start += "0"
	if err := changed.KillIfStill(child.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(child.Process.Pid, 0); err != nil {
		t.Fatal("mismatched identity killed the child")
	}
	// The recorded identity kills exactly its own process.
	if err := id.KillIfStill(child.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("child exited successfully instead of being killed")
	}
	if id.StillIs(child.Process.Pid) {
		t.Fatal("exited child still matched its identity")
	}
	if err := id.KillIfStill(child.Process.Pid); err != nil {
		t.Fatalf("killing a gone process: %v", err)
	}
}
