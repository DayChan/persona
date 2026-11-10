package node

import (
	"errors"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A claude reaches Allow All from its terminal only when one of these flags started it (#1379).
func TestArgsCanBypass(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{[]string{"claude", "--dangerously-skip-permissions"}, true},
		{[]string{"node", "/opt/claude/cli.js", "--verbose", "--allow-dangerously-skip-permissions"}, true},
		{[]string{"claude", "--permission-mode", "bypassPermissions", "--resume", "s"}, true},
		{[]string{"claude", "--permission-mode=bypassPermissions"}, true},
		{[]string{"claude", "--permission-mode", "plan"}, false},
		{[]string{"claude", "--permission-mode"}, false},
		{[]string{"claude", "--resume", "s"}, false},
		{[]string{"claude", "--", "--dangerously-skip-permissions"}, false}, // after "--": the prompt
		{[]string{"claude", "explain --dangerously-skip-permissions"}, false},
		{nil, false},
	} {
		if got := argsCanBypass(c.args); got != c.want {
			t.Errorf("argsCanBypass(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

// The arguments come from the system's own record of the process (kern.procargs2, /proc/<pid>/cmdline).
func TestClaudeCanBypassReadsTheProcess(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("process arguments are read on Linux and macOS")
	}
	start := func(args ...string) int {
		cmd := exec.Command("/bin/sh", append([]string{"-c", "sleep 30"}, args...)...) // the rest are $0, $1…: in its argv
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd.Process.Pid
	}
	if pid := start("claude", "--dangerously-skip-permissions"); !claudeCanBypass(pid) {
		t.Errorf("a process started with --dangerously-skip-permissions (pid %d) cannot bypass", pid)
	}
	if pid := start("claude", "--resume", "s"); claudeCanBypass(pid) {
		t.Errorf("a process started without the flag (pid %d) can bypass", pid)
	}
	if claudeCanBypass(0) {
		t.Error("pid 0 can bypass")
	}
}

// The hooks name the claude they run under; its arguments are read once per process, never on every hook, and the
// session list says whether Allow All can be chosen while it runs.
func TestHookTurnsReadBypassOncePerProcess(t *testing.T) {
	reads := map[int]int{}
	argv := map[int][]string{100: {"claude", "--dangerously-skip-permissions"}, 200: {"claude", "--resume", "s"}}
	old := processArgv
	processArgv = func(pid int) ([]string, error) {
		reads[pid]++
		if a, ok := argv[pid]; ok {
			return a, nil
		}
		return nil, errors.New("no such process")
	}
	t.Cleanup(func() { processArgv = old })

	now := time.Now()
	var nilTurns *hookTurns
	if nilTurns.canBypass("s") {
		t.Fatal("a node without hook state says a session can bypass")
	}
	h := &hookTurns{}
	if h.canBypass("s") {
		t.Fatal("no hook yet: unknown is false")
	}
	h.agent("s", 100, "linux:7", now)
	h.prompt("s", now)
	h.agent("s", 100, "linux:7", now.Add(time.Second)) // every hook names it again
	if !h.canBypass("s") || reads[100] != 1 {
		t.Fatalf("started with the flag: canBypass %v after %d reads, want true after 1", h.canBypass("s"), reads[100])
	}
	h.agent("s", 200, "linux:9", now) // resumed in a claude without it
	if h.canBypass("s") || reads[200] != 1 {
		t.Fatalf("resumed without the flag: canBypass %v after %d reads", h.canBypass("s"), reads[200])
	}
	h.agent("s", 300, "", now) // a claude whose arguments cannot be read
	if h.canBypass("s") {
		t.Fatal("unreadable arguments count as the flag")
	}

	n := assistantNode(t)
	n.hooks = &hookTurns{}
	n.hooks.agent("a", 100, "linux:7", now)
	n.hooks.agent("b", 200, "linux:9", now)
	for id, want := range map[string]bool{"a": true, "b": false, "c": false} {
		s := proto.Session{SessionID: id, Cwd: "/work/app"}
		n.decorate(&s)
		if s.BypassAvailable != want {
			t.Errorf("session %s listed with bypass_available %v, want %v", id, s.BypassAvailable, want)
		}
	}
}
