//go:build !windows

package node

import (
	"bufio"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
)

// keptShell starts script under /bin/sh as a kept conversation's process, leading its own process group as
// spawnClaude's does, and returns once the script has printed its first line (whatever it set up is in place).
func keptShell(t *testing.T, k *assistantKeeper, id, script string) *assistantProc {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.SysProcAttr = osproc.GroupAttr()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		_ = osproc.StopGroup(cmd.Process.Pid, true)
		_ = cmd.Wait()
		t.Fatalf("%s did not start: %v", id, err)
	}
	p := &assistantProc{id: id, agent: "claude", cmd: cmd, stdin: stdin, exited: make(chan struct{}), born: time.Now()}
	go func() { _ = cmd.Wait(); close(p.exited) }()
	t.Cleanup(func() {
		if p.alive() {
			_ = osproc.StopGroup(cmd.Process.Pid, true)
		}
		select {
		case <-p.exited:
		case <-time.After(10 * time.Second):
			t.Errorf("%s still running after SIGKILL", id)
		}
	})
	k.procs[id] = p
	return p
}

// shortenKeeperStop sets stopAll's waits for one test.
func shortenKeeperStop(t *testing.T, grace, kill time.Duration) {
	oldGrace, oldKill := keeperStopGrace, keeperKillWait
	keeperStopGrace, keeperKillWait = grace, kill
	t.Cleanup(func() { keeperStopGrace, keeperKillWait = oldGrace, oldKill })
}

// exitSignal is the signal that ended p, or 0 if it exited on its own; p has exited.
func exitSignal(t *testing.T, p *assistantProc) syscall.Signal {
	t.Helper()
	ws, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("%s: no wait status", p.id)
	}
	if !ws.Signaled() {
		return 0
	}
	return ws.Signal()
}

// A kept conversation that ignores SIGTERM is killed before stopAll returns: the resident node exits or execs right
// after it, and the delayed SIGKILL stop() leaves to a goroutine would die with the node, leaving the conversation
// running orphaned. One that ends on SIGTERM next to it is not killed.
func TestStopAllKillsAConversationThatIgnoresTerm(t *testing.T) {
	shortenKeeperStop(t, 200*time.Millisecond, 10*time.Second)
	k := newAssistantKeeper()
	stubborn := keptShell(t, k, "11111111-2222-3333-4444-000000000021", `trap '' TERM; echo ready; exec sleep 60`)
	polite := keptShell(t, k, "11111111-2222-3333-4444-000000000022", `echo ready; exec cat`)

	k.stopAll()
	for _, p := range []*assistantProc{stubborn, polite} {
		select {
		case <-p.exited:
		default:
			t.Fatalf("stopAll returned with %s still running", p.id)
		}
	}
	if sig := exitSignal(t, stubborn); sig != syscall.SIGKILL {
		t.Fatalf("the conversation ignoring SIGTERM ended by %v, want SIGKILL", sig)
	}
	if sig := exitSignal(t, polite); sig == syscall.SIGKILL {
		t.Fatal("a conversation that ends on SIGTERM was killed")
	}
}

// Conversations that end when asked are waited for, not killed: stopAll returns once they have exited, without
// waiting out its grace.
func TestStopAllReturnsOnceConversationsEnd(t *testing.T) {
	shortenKeeperStop(t, time.Minute, 10*time.Second)
	k := newAssistantKeeper()
	ps := []*assistantProc{
		keptShell(t, k, "11111111-2222-3333-4444-000000000023", `echo ready; exec cat`),
		keptShell(t, k, "11111111-2222-3333-4444-000000000024", `echo ready; exec cat`),
	}

	start := time.Now()
	k.stopAll()
	if d := time.Since(start); d >= keeperStopGrace {
		t.Fatalf("stopAll took %s: it waited out its grace", d)
	}
	for _, p := range ps {
		select {
		case <-p.exited:
		default:
			t.Fatalf("stopAll returned before %s exited", p.id)
		}
		if sig := exitSignal(t, p); sig == syscall.SIGKILL {
			t.Fatalf("%s was killed although it ended when asked", p.id)
		}
	}
	if len(k.procs) != 0 {
		t.Fatal("stopAll left conversations in the keeper")
	}
}
