package main

import (
	"bufio"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const modulePath = "github.com/nwverse/persona/server/"

// The device's program links the node and its clients, never the hub, the manager or the database, on any platform it
// ships for (the self-contained packaging plan, step 4).
func TestPersonaNodeDoesNotLinkTheHubOrTheManager(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	// The hub's and the manager's own packages, and their sources and channels (they run in the manager).
	forbidden := []string{"internal/hub", "internal/hubedge", "internal/assistant", "internal/manager", "internal/devices",
		"internal/extmgr", "internal/db", "internal/relay", "internal/voice", "internal/mail", "internal/lark",
		"internal/feishubot", "internal/apns", "internal/bus", "internal/workspaceboard",
		"internal/usagesource", "internal/admin"}
	// No database driver either, wherever it comes from.
	drivers := []string{"github.com/jackc/pgx", "github.com/lib/pq", "database/sql/driver"}
	for _, target := range []string{"linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64", "windows/amd64"} {
		goos, goarch, _ := strings.Cut(target, "/")
		cmd := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", ".")
		cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps (%s): %v", target, err)
		}
		for _, dep := range strings.Fields(string(out)) {
			for _, d := range drivers {
				if dep == d || strings.HasPrefix(dep, d+"/") {
					t.Errorf("persona-node (%s) links the database driver %s", target, dep)
				}
			}
			rel, ok := strings.CutPrefix(dep, modulePath)
			if !ok {
				continue
			}
			for _, f := range forbidden {
				if rel == f || strings.HasPrefix(rel, f+"/") {
					t.Errorf("persona-node (%s) depends on %s: the device's program reaches the manager only over the node "+
						"protocol; move what it needs into internal/node, internal/nodecmd or a contract package", target, dep)
				}
			}
		}
	}
}

// main in a child process (this test binary, re-executed): what persona-node answers and its exit codes.
func TestPersonaNodeCommandLine(t *testing.T) {
	if args := os.Getenv("PERSONA_NODE_TEST_ARGS"); args != "" {
		os.Args = append([]string{"persona-node"}, strings.Split(args, "\x1f")...)
		main()
		os.Exit(0)
	}
	home := t.TempDir()
	run := func(args ...string) (string, int) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPersonaNodeCommandLine$")
		cmd.Env = append(isolatedEnv(t, home), "PERSONA_NODE_TEST_ARGS="+strings.Join(args, "\x1f"), "HOME="+home,
			"PERSONA_HOME="+filepath.Join(home, ".persona"))
		out, _ := cmd.CombinedOutput()
		return string(out), cmd.ProcessState.ExitCode()
	}
	if out, code := run("version"); code != 0 || !strings.HasPrefix(out, "dev") {
		t.Fatalf("version: %d %q", code, out)
	}
	if out, code := run("version", "-v"); code != 0 || !strings.Contains(out, "channel dev, build 0") {
		t.Fatalf("version -v: %d %q", code, out)
	}
	if out, code := run("node", "-h"); code != 0 || !strings.Contains(out, "persona-node node --attach") {
		t.Fatalf("node -h: %d %q", code, out)
	}
	for _, args := range [][]string{{"hub"}, {"manager", "run"}, {"assistant"}, {"extension", "list"}} {
		where := "the manager's install command" // a manager's: never a second hub
		if args[0] == "hub" {
			where = "--hub"
		}
		if out, code := run(args...); code != 2 || !strings.Contains(out, "usage:") ||
			!strings.Contains(out, "persona-node, the device's own program, has no") || !strings.Contains(out, where) {
			t.Errorf("%v is not the device's: %d %q", args, code, out)
		}
	}
	for _, h := range []string{"-h", "--help", "help"} {
		if out, code := run(h); code != 0 || !strings.Contains(out, "usage:") || !strings.Contains(out, "persona-node tool ") || strings.Contains(out, "has no") {
			t.Errorf("%s: %d %q", h, code, out)
		}
	}
	if out, code := run("sysmon", "x"); code != 2 || !strings.Contains(out, "persona-node sysmon") {
		t.Fatalf("sysmon x: %d %q", code, out)
	}
	if out, code := run("hook"); code != 0 || out != "" { // empty stdin: nothing to say, as personad hook
		t.Fatalf("hook: %d %q", code, out)
	}
	// node.info says which program runs the node (an update brings the same kind): over stdio, as ssh runs it.
	info := exec.Command(os.Args[0], "-test.run=^TestPersonaNodeCommandLine$")
	info.Env = append(isolatedEnv(t, home), "PERSONA_NODE_TEST_ARGS=node", "HOME="+home, "PERSONA_HOME="+filepath.Join(home, ".persona"))
	stdin, _ := info.StdinPipe()
	stdout, _ := info.StdoutPipe()
	if err := info.Start(); err != nil {
		t.Fatal(err)
	}
	reply := make(chan string, 1)
	go func() {
		lines := bufio.NewScanner(stdout)
		lines.Buffer(make([]byte, 1<<20), 1<<20)
		for lines.Scan() {
			if strings.HasPrefix(lines.Text(), `{"id":"1",`) {
				reply <- lines.Text()
				return
			}
		}
		reply <- ""
	}()
	_, _ = io.WriteString(stdin, `{"id":"1","method":"node.info"}`+"\n")
	select {
	case got := <-reply:
		if !strings.Contains(got, `"program":"persona-node"`) {
			t.Errorf("node.info over stdio does not say persona-node: %.300q", got)
		}
	case <-time.After(30 * time.Second):
		t.Error("node.info over stdio: no answer in 30s")
	}
	_ = stdin.Close()
	_ = info.Process.Kill()
	_ = info.Wait()
	// Scripts tell persona-node from personad by this before they replace or run one (install.sh, the manager's install
	// script, Redeploy over ssh).
	if out, code := run("node-update-check"); code != 0 || !strings.Contains(out, `"version":"dev"`) ||
		!strings.Contains(out, `"program":"persona-node"`) {
		t.Fatalf("node-update-check: %d %q", code, out)
	}
}

// The resident node (node --attach, what the node service runs on every device) says it runs persona-node in
// node.info: the hub picks the program an update brings by it. Asked straight on its attach socket, as a relay does.
func TestPersonaNodeResidentSaysWhatItRuns(t *testing.T) {
	if os.Getenv("PERSONA_NODE_TEST_ARGS") != "" {
		t.Skip("child")
	}
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	home, err := os.MkdirTemp("/tmp", "pn-") // short: a unix socket path has about 100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if home, err = filepath.EvalSymlinks(home); err != nil { // macOS: /tmp is /private/tmp, which the node logs
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".persona", "bin", "persona-node") // where install.sh puts it
	if out, err := exec.Command(goBin, "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	state := filepath.Join(home, ".persona")
	resident := exec.Command(bin, "node", "--attach")
	var log syncBuffer
	resident.Stdout, resident.Stderr = &log, &log
	resident.Env = append(isolatedEnv(t, home), "HOME="+home, "PERSONA_HOME="+state, "INVOCATION_ID=",
		"XPC_SERVICE_NAME=")
	if err := resident.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- resident.Wait() }()
	t.Cleanup(func() {
		_ = resident.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = resident.Process.Kill()
		}
	})
	info := func() string { // node.info over the attach socket, once it answers (it is listening again after a restart)
		t.Helper()
		sock := filepath.Join(state, "node-attach.sock")
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
			select {
			case err := <-exited:
				t.Fatalf("the resident node exited: %v\n%s", err, log.String())
			default:
			}
			c, err := net.Dial("unix", sock)
			if err != nil {
				continue
			}
			_ = c.SetDeadline(time.Now().Add(10 * time.Second))
			_, _ = io.WriteString(c, `{"id":"1","method":"node.info"}`+"\n")
			lines := bufio.NewScanner(c)
			lines.Buffer(make([]byte, 1<<20), 1<<20)
			for lines.Scan() {
				if strings.HasPrefix(lines.Text(), `{"id":"1",`) {
					_ = c.Close()
					return lines.Text()
				}
			}
			_ = c.Close()
		}
		t.Fatalf("no node.info from the resident node\n%s", log.String())
		return ""
	}
	got := info()
	if !strings.Contains(got, `"program":"persona-node"`) || !strings.Contains(got, `"executable":`+strconv.Quote(bin)) {
		t.Fatalf("the resident node does not say persona-node at %s: %.300q", bin, got)
	}
}

// syncBuffer is what a child process writes, read while it runs.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// isolatedEnv is this environment for a node whose HOME is home, without anything of the developer's own: no herdr
// or Claude settings (HERDR_*, XDG_*, which is how herdr's sockets are found, CLAUDE_CONFIG_DIR), no PERSONA_*,
// the system's tools only on the PATH, a shell that reads no rc files of theirs (node.info asks a login zsh
// for claude's version). The node looks in ~/.local/bin first: stubs of claude and herdr there answer it instead.
func isolatedEnv(t *testing.T, home string) []string {
	t.Helper()
	stubs := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(stubs, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"claude": `[ "$1" = --version ] && echo "0.0.0 (Claude Code)"`,
		"herdr":  `exit 1`,
	} {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> \"$HOME/stub-calls\"\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(stubs, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "HERDR_"), strings.HasPrefix(k, "XDG_"), strings.HasPrefix(k, "PERSONA_"),
			k == "CLAUDE_CONFIG_DIR", k == "PATH", k == "ZDOTDIR", k == "SHELL":
			continue
		}
		env = append(env, kv)
	}
	return append(env, "PATH=/usr/bin:/bin", "ZDOTDIR="+home, "SHELL=/bin/sh")
}
