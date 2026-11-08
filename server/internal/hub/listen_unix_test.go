//go:build unix

package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/osproc/osproctest"
	"github.com/nwverse/persona/server/internal/update"
)

// listenHelperEnv runs the test binary as a hub that systemd started with one socket at descriptor 3 (the variables
// but LISTEN_PID come from the test; it sets that one to itself, as systemd does). The value is what it does before
// it starts a child that reports what it inherited (osproctest): "run" (Run), "serve" (Serve), "exec"
// (execKeepingListeners on a program that is not there) or "none" (nothing: the control).
const listenHelperEnv = "PERSONA_TEST_LISTEN_HELPER"

const listenHelperPrefix = "listen-helper: "

type listenHelperResult struct {
	Err    string            `json:"err"`    // what Run, Serve or the exec returned
	Sealed bool              `json:"sealed"` // descriptor 3 is close-on-exec here afterwards
	Edge   bool              `json:"edge"`   // "serve": the edge was on (edgeOff said nothing)
	Child  osproctest.Report `json:"child"`
}

// listenHelpers is TestMain's first step: it returns unless the binary was started as a reporter or a helper, and
// exits in those cases.
func listenHelpers() {
	osproctest.ReportIfAsked()
	mode := os.Getenv(listenHelperEnv)
	if mode == "" {
		return
	}
	_ = os.Unsetenv(listenHelperEnv)
	_ = os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
	var res listenHelperResult
	errText := func(err error) string {
		if err == nil {
			return "<nil>"
		}
		return err.Error()
	}
	switch mode {
	case "run":
		res.Err = errText(Run(context.Background(), update.Build{Version: "test"}))
	case "serve":
		res.Edge = edgeOff(update.Build{Version: "test"}) == ""
		res.Err = errText(Serve(context.Background(), update.Build{Version: "test"}))
	case "exec":
		inheritedListeners()
		res.Err = errText(execKeepingListeners(filepath.Join(os.TempDir(), "persona-no-such-program"), []string{"x"}, os.Environ()))
	case "none":
	default:
		fmt.Fprintf(os.Stderr, "unknown %s %q\n", listenHelperEnv, mode)
		os.Exit(2)
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, 3, syscall.F_GETFD, 0)
	res.Sealed = errno == 0 && flags&syscall.FD_CLOEXEC != 0
	child, err := osproctest.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	res.Child = child
	b, _ := json.Marshal(res)
	fmt.Printf("%s%s\n", listenHelperPrefix, b)
	os.Exit(0)
}

// TestPassedSocketsReachNoChild: whatever a hub does first, the socket systemd passed it and socket activation's
// variables reach no process it starts. Run (the hub without its edge: runHub could start the manager before it
// listened, and the manager then took the port on to every node, extension and agent), Serve (with and without the
// edge) and a failed exec of a new binary all leave the socket close-on-exec; the control shows the check sees a leak.
func TestPassedSocketsReachNoChild(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	f, err := ln.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// A hub.json it cannot read: Run and Serve stop there, and nothing before it starts a process. Under /tmp: the
	// edge stays off where the path to its sockets is too long (edgeOff), as under macOS's temporary directory.
	home, err := os.MkdirTemp("/tmp", "phub")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".persona", "hub.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	var base []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "HOME" || strings.HasPrefix(k, "LISTEN_") || compat.ReservedEnv(k) {
			continue
		}
		base = append(base, kv)
	}
	base = append(base, "HOME="+home, "LISTEN_FDS=1", "LISTEN_FDNAMES=personad.socket")

	for _, c := range []struct {
		name, mode string
		env        []string
		stops      string // what Err says
		edge       bool   // "serve": the edge must be on
	}{
		{"control", "none", nil, "", false},
		{"run", "run", nil, "hub.json", false},
		{"serve with the edge", "serve", nil, "hub.json", true},
		{"serve without the edge", "serve", []string{"PERSONA_HUB_EDGE=0"}, "hub.json", false},
		{"failed exec", "exec", nil, "no such file", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.edge && runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
				t.Skip("the edge runs on Linux and macOS")
			}
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(append(slices.Clone(base), listenHelperEnv+"="+c.mode), c.env...)
			cmd.ExtraFiles = []*os.File{f} // descriptor 3, inheritable, as systemd passes it
			cmd.Stderr = os.Stderr
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("helper: %v\n%s", err, out)
			}
			var res listenHelperResult
			found := false
			for _, line := range strings.Split(string(out), "\n") {
				if rest, ok := strings.CutPrefix(line, listenHelperPrefix); ok {
					found = json.Unmarshal([]byte(rest), &res) == nil
				}
			}
			if !found {
				t.Fatalf("no result in %q", out)
			}
			if c.mode == "none" {
				if !slices.Contains(res.Child.Sockets(), 3) || len(res.Child.ListenEnv) == 0 {
					t.Fatalf("the control's child got %+v; want the socket at 3 and LISTEN_*: the check would see no leak", res.Child)
				}
				return
			}
			if c.mode == "serve" && res.Edge != c.edge {
				t.Fatalf("the edge on: %v; want %v", res.Edge, c.edge)
			}
			if !strings.Contains(res.Err, c.stops) {
				t.Fatalf("returned %q; want it to stop at %q, before it starts anything", res.Err, c.stops)
			}
			if s := res.Child.Sockets(); len(s) > 0 || len(res.Child.ListenEnv) > 0 || !res.Sealed {
				t.Fatalf("a child got sockets %v and variables %v (descriptor 3 close-on-exec here: %v); want neither",
					s, res.Child.ListenEnv, res.Sealed)
			}
		})
	}
}
