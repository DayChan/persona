//go:build linux

package osproc

import (
	"bufio"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/procinfo"
)

// TestReapInherited: a process that starts a child and then replaces itself (syscall.Exec, as the resident node, the
// hub and a manager do) leaves that child to the new image, whose exec.Cmd values know nothing of it. Killed, the
// child stays a zombie of the new image unless that called ReapInherited first. A child the new image starts itself
// is still its exec.Cmd's to wait for.
func TestReapInherited(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip(err)
	}
	for _, tc := range []struct {
		mode string
		want string // the killed child's state: Z for a zombie, gone once reaped
	}{
		{"leave", "Z"}, // the control: without ReapInherited the harness sees the zombie
		{"reap", "gone"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			helper, child := startReapHelper(t, tc.mode)
			if got := childState(child, helper); got == "gone" || got == "Z" {
				t.Fatalf("child %d of the helper %d: %s before it was killed", child, helper, got)
			}
			if err := syscall.Kill(child, syscall.SIGKILL); err != nil {
				t.Fatal(err)
			}
			if got := pollChildState(child, helper, tc.want, 10*time.Second); got != tc.want {
				t.Fatalf("killed child %d of the helper %d: %s after 10s, want %s", child, helper, got, tc.want)
			}
		})
	}
}

// startReapHelper starts the test binary as reapHelper in mode and returns its pid and its child's once the image after
// the exec is ready. Cleanup kills both and waits for the helper.
func startReapHelper(t *testing.T, mode string) (helper, child int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), reapHelperEnv+"="+mode)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout = w
	err = cmd.Start()
	_ = w.Close()
	if err != nil {
		_ = r.Close()
		t.Fatal(err)
	}
	helper = cmd.Process.Pid
	t.Cleanup(func() {
		// A child not killed yet (the test failed first) still is the helper's: its pid names no other process.
		if child > 0 && childState(child, helper) != "gone" {
			_ = syscall.Kill(child, syscall.SIGKILL)
		}
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = r.Close()
	})
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if rest, ok := strings.CutPrefix(sc.Text(), reapHelperPrefix); ok {
				lines <- rest
			}
		}
	}()
	next := func() string {
		t.Helper()
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("the helper exited early")
			}
			return line
		case <-time.After(20 * time.Second):
			t.Fatal("the helper did not answer within 20s")
		}
		return ""
	}
	line := next()
	pid, err := strconv.Atoi(strings.TrimPrefix(line, "child "))
	if err != nil || pid < 2 {
		t.Fatalf("helper said %q, want its child's pid", line)
	}
	child = pid
	if line := next(); line != "ready ok" {
		t.Fatalf("helper after the exec said %q, want ready and its own child waited for", line)
	}
	return helper, child
}

// childState is pid's state as /proc shows it (R, S, Z…) while it is parent's child, or "gone".
func childState(pid, parent int) string {
	f, err := procinfo.Stat(pid)
	if err != nil || len(f) < 2 || f[1] != strconv.Itoa(parent) {
		return "gone"
	}
	return f[0]
}

// pollChildState polls childState until it is want or timeout has passed, and returns the last state it saw.
func pollChildState(pid, parent int, want string, timeout time.Duration) string {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(timeout)
	for {
		got := childState(pid, parent)
		if got == want {
			return got
		}
		select {
		case <-deadline:
			return got
		case <-tick.C:
		}
	}
}
