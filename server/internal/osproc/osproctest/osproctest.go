//go:build unix

// Package osproctest shows a test what a process inherited from the one that started it: its descriptors past
// stderr and socket activation's variables (osproc.SealInherited, the sockets systemd passes the hub). A test binary
// that calls ReportIfAsked first thing in TestMain is its own reporter: Reporter runs it as one. On Linux, StartHolding
// runs a process holding an inheritable socket, and a test watches it seal the socket while it runs.
package osproctest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Env set to 1 makes a test binary that calls ReportIfAsked print its report and exit.
const Env = "PERSONA_TEST_INHERITED_REPORT"

// prefix marks the report's line among whatever else the binary prints.
const prefix = "inherited-report: "

// Report is what a process found it holds when it started.
type Report struct {
	// FDs are its descriptors past stderr and what each is: socket, pipe, file or other.
	FDs map[int]string `json:"fds"`
	// ListenEnv are its LISTEN_* variables, as NAME=value.
	ListenEnv []string `json:"listen_env"`
}

// Sockets are the report's socket descriptors, in order.
func (r Report) Sockets() []int {
	var out []int
	for fd, kind := range r.FDs {
		if kind == "socket" {
			out = append(out, fd)
		}
	}
	sort.Ints(out)
	return out
}

// Describe is this process's report.
func Describe() Report {
	r := Report{FDs: map[int]string{}}
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		entries, _ = os.ReadDir("/dev/fd")
	}
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil || fd <= 2 {
			continue
		}
		var st syscall.Stat_t
		if syscall.Fstat(fd, &st) != nil {
			continue // the listing's own descriptor, closed by now
		}
		switch uint32(st.Mode) & syscall.S_IFMT {
		case syscall.S_IFSOCK:
			r.FDs[fd] = "socket"
		case syscall.S_IFIFO:
			r.FDs[fd] = "pipe"
		case syscall.S_IFREG:
			r.FDs[fd] = "file"
		default:
			r.FDs[fd] = "other"
		}
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "LISTEN_") {
			r.ListenEnv = append(r.ListenEnv, kv)
		}
	}
	slices.Sort(r.ListenEnv)
	return r
}

// Print writes r as the line Parse finds.
func Print(w io.Writer, r Report) {
	b, _ := json.Marshal(r)
	fmt.Fprintf(w, "%s%s\n", prefix, b)
}

// Parse finds the report line in out.
func Parse(out []byte) (Report, error) {
	for _, line := range bytes.Split(out, []byte("\n")) {
		if rest, ok := bytes.CutPrefix(line, []byte(prefix)); ok {
			var r Report
			err := json.Unmarshal(rest, &r)
			return r, err
		}
	}
	return Report{}, errors.New("no report in the output: " + strings.TrimSpace(string(out)))
}

// ReportIfAsked prints this process's report and exits when Env is set; it returns at once otherwise. Call it
// first thing in TestMain, before anything opens a descriptor.
func ReportIfAsked() {
	if os.Getenv(Env) != "1" {
		return
	}
	Print(os.Stdout, Describe())
	os.Exit(0)
}

// Reporter is the test binary run as a reporter, with this process's environment, and files at descriptors 3, 4, …
// (ExtraFiles). Nothing else is handed to it on purpose: what else it reports, it inherited.
func Reporter(files ...*os.File) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), Env+"=1")
	cmd.ExtraFiles = files
	cmd.Stderr = os.Stderr
	return cmd
}

// Run runs a reporter and returns its report.
func Run(files ...*os.File) (Report, error) {
	out, err := Reporter(files...).Output()
	if err != nil {
		return Report{}, fmt.Errorf("reporter: %w", err)
	}
	return Parse(out)
}
