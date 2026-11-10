//go:build unix

package osproc

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/nwverse/persona/server/internal/osproc/osproctest"
)

// TestMain lets the test binary be the processes of TestSealInherited: the reporter (osproctest) and the process in
// the middle (sealHelperEnv); and the process of TestReapInherited (reapHelperEnv).
func TestMain(m *testing.M) {
	osproctest.ReportIfAsked()
	if mode := os.Getenv(sealHelperEnv); mode != "" {
		os.Exit(sealHelper(mode))
	}
	if mode := os.Getenv(reapHelperEnv); mode != "" {
		os.Exit(reapHelper(mode))
	}
	os.Exit(m.Run())
}

// sealHelperEnv runs the test binary as the process in the middle: "seal" calls SealInherited first, "leave" does not.
const sealHelperEnv = "PERSONA_TEST_SEAL_HELPER"

const sealHelperPrefix = "seal-helper: "

// sealHelperResult is what the process in the middle saw of the children it started.
type sealHelperResult struct {
	Plain  osproctest.Report `json:"plain"`  // a child started with nothing handed to it
	Handed osproctest.Report `json:"handed"` // a child handed a pipe this process made after sealing (ExtraFiles)
}

func sealHelper(mode string) int {
	if mode == "seal" {
		SealInherited()
	}
	var res sealHelperResult
	var err error
	if res.Plain, err = osproctest.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	r, w, err := os.Pipe()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer r.Close()
	defer w.Close()
	if res.Handed, err = osproctest.Run(w); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	b, _ := json.Marshal(res)
	fmt.Printf("%s%s\n", sealHelperPrefix, b)
	return 0
}

// reapHelperEnv runs the test binary as the process of TestReapInherited: it starts a child it never waits for, then
// replaces itself with the test binary again (syscall.Exec), as the resident node does on a restart. The new image
// calls ReapInherited first when the value is "reap", not when it is "leave".
const reapHelperEnv = "PERSONA_TEST_REAP_HELPER"

// reapExecedEnv marks the image after that exec.
const reapExecedEnv = "PERSONA_TEST_REAP_EXECED"

const reapHelperPrefix = "reap-helper: "

func reapHelper(mode string) int {
	if os.Getenv(reapExecedEnv) == "" {
		child := exec.Command("sleep", "60")
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		fmt.Printf("%schild %d\n", reapHelperPrefix, child.Process.Pid)
		exe, err := os.Executable()
		if err == nil {
			err = syscall.Exec(exe, os.Args, append(os.Environ(), reapExecedEnv+"=1"))
		}
		fmt.Fprintln(os.Stderr, "exec:", err)
		return 1
	}
	if mode == "reap" {
		ReapInherited()
	}
	// A child this image starts is still its exec.Cmd's to wait for: ReapInherited waits for the pids it found only.
	own := "ok"
	if err := exec.Command("true").Run(); err != nil {
		own = err.Error()
	}
	fmt.Printf("%sready %s\n", reapHelperPrefix, own)
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

// TestSealInherited: a parent leaves a listening socket inheritable at descriptor 3, with socket activation's
// variables naming another process. A process that does not seal hands both on to every child it starts; one that
// calls SealInherited first hands neither, and a pipe it hands a child with ExtraFiles still gets there.
func TestSealInherited(t *testing.T) {
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
	run := func(mode string) sealHelperResult {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = append(os.Environ(), sealHelperEnv+"="+mode, "LISTEN_PID=1", "LISTEN_FDS=1", "LISTEN_FDNAMES=hub")
		cmd.ExtraFiles = []*os.File{f} // descriptor 3 in the child, inheritable, as a careless parent leaves one
		cmd.Stderr = os.Stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if rest, ok := strings.CutPrefix(line, sealHelperPrefix); ok {
				var res sealHelperResult
				if err := json.Unmarshal([]byte(rest), &res); err != nil {
					t.Fatal(err)
				}
				return res
			}
		}
		t.Fatalf("%s: no result in %q", mode, out)
		return sealHelperResult{}
	}

	// The control: without SealInherited the leak is there, so the check below can see one.
	leaked := run("leave")
	if !slices.Contains(leaked.Plain.Sockets(), 3) || len(leaked.Plain.ListenEnv) == 0 {
		t.Fatalf("without SealInherited the child got %+v; want the socket at 3 and LISTEN_*", leaked.Plain)
	}

	sealed := run("seal")
	if s := sealed.Plain.Sockets(); len(s) > 0 || len(sealed.Plain.ListenEnv) > 0 {
		t.Fatalf("after SealInherited a child got sockets %v, variables %v; want neither", s, sealed.Plain.ListenEnv)
	}
	if s := sealed.Handed.Sockets(); len(s) > 0 || sealed.Handed.FDs[3] != "pipe" {
		t.Fatalf("after SealInherited a child handed a pipe got %v; want the pipe at 3 and no socket", sealed.Handed.FDs)
	}
}

// TestSealInheritedKeepsOwnActivation: socket activation's variables for this very process stay (the hub takes its
// sockets after sealing), and what this process holds stays usable.
func TestSealInheritedKeepsOwnActivation(t *testing.T) {
	t.Setenv("LISTEN_PID", fmt.Sprint(os.Getpid()))
	t.Setenv("LISTEN_FDS", "1")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	SealInherited()
	if os.Getenv("LISTEN_PID") == "" || os.Getenv("LISTEN_FDS") != "1" {
		t.Fatal("SealInherited dropped this process's own LISTEN_* variables")
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatalf("a sealed descriptor no longer works here: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := r.Read(buf); err != nil || buf[0] != 'x' {
		t.Fatalf("read back %q, %v", buf, err)
	}
}

// TestListFDsNeedsItsOwnDescriptor: a directory that lists 0, 1 and 2 whatever is open, as /dev/fd does on FreeBSD
// without fdescfs, is not taken for the list of open descriptors, so SealInherited scans by number instead.
func TestListFDsNeedsItsOwnDescriptor(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"0", "1", "2"} {
		if err := os.WriteFile(dir+"/"+name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if fds, ok := listFDs(dir); ok {
		t.Fatalf("a static listing was trusted: %v", fds)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		fds, ok := listFDs(dir)
		if ok && (!slices.Contains(fds, int(r.Fd())) || !slices.Contains(fds, int(w.Fd()))) {
			t.Fatalf("%s: trusted a listing without the open pipe: %v", dir, fds)
		}
	}
	if fds := openFDs(); !slices.Contains(fds, int(w.Fd())) {
		t.Fatalf("openFDs misses the open pipe at %d: %v", w.Fd(), fds)
	}
}
