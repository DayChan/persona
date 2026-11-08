package osproctest

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// HomeEnv is an environment for a child of the test: PATH, and HOME and PERSONA_HOME in a new directory of its own
// (short, for the Unix sockets made in it); then more, as NAME=value. herdr's socket is put below a regular file there,
// so a node can never start herdr's server, which runs in a session of its own and would outlive the test: the node
// finds herdr on a PATH it extends itself (/usr/local/bin among others) and through the login shell, whatever this
// PATH holds. When the test ends, every process of this user still running with that HOME is killed, then the
// directory is removed.
func HomeEnv(t testing.TB, more ...string) []string {
	t.Helper()
	home, err := os.MkdirTemp("", "pfd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := killHomeProcs(t, home); err != nil {
			t.Error(err)
		}
		_ = os.RemoveAll(home)
	})
	blocker := filepath.Join(home, "no-herdr")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "PERSONA_HOME=" + home + "/.persona",
		"HERDR_SOCKET_PATH=" + filepath.Join(blocker, "herdr.sock")}, more...)
}

// killHomeProcs kills every process of this user whose environment has HOME=home, again until none is left, for at
// most 10 s. Each is checked and killed through a pidfd (os.FindProcess), so a pid reused in between is never hit.
func killHomeProcs(t testing.TB, home string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		pids := homeProcs(home)
		if len(pids) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("processes with HOME=%s still running: %v", home, pids)
		}
		for _, pid := range pids {
			p, err := os.FindProcess(pid)
			if err != nil {
				continue
			}
			if hasHome(pid, home) && p.Kill() == nil {
				comm, _ := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
				t.Logf("killed %d (%s), left running with HOME=%s", pid, bytes.TrimSpace(comm), home)
			}
			_ = p.Release()
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// homeProcs are this user's processes, other than this one, whose environment has HOME=home.
func homeProcs(home string) []int {
	entries, _ := os.ReadDir("/proc")
	uid := uint32(os.Getuid())
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		if st, err := e.Info(); err != nil || st.Sys().(*syscall.Stat_t).Uid != uid {
			continue
		}
		if hasHome(pid, home) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// hasHome says whether HOME=home is in the environment of process pid (none for a zombie, nor one not readable).
func hasHome(pid int, home string) bool {
	env, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return false
	}
	for _, kv := range bytes.Split(env, []byte{0}) {
		if string(kv) == "HOME="+home {
			return true
		}
	}
	return false
}

// Holder is a process StartHolding started.
type Holder struct {
	Pid    int
	socket string // the passed socket as /proc/<pid>/fd links name it: socket:[inode]
	exited chan struct{}
	err    error // how it ended, once exited is closed
}

// passedFD is where StartHolding's process holds the socket: its first descriptor past stderr.
const passedFD = 3

// errReplaced: descriptor passedFD is no longer the passed socket.
var errReplaced = errors.New("not the passed socket")

// StartHolding starts cmd holding a listening TCP socket on a free loopback port at descriptor 3, inheritable there
// as an older hub leaves its own; with stdin a pipe left open, so a process reading it stays; in a process group of
// its own, killed with everything it started when the test ends.
func StartHolding(t testing.TB, cmd *exec.Cmd) *Holder {
	t.Helper()
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
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.ExtraFiles = []*os.File{f}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	h := &Holder{Pid: cmd.Process.Pid, socket: fmt.Sprintf("socket:[%d]", st.Ino), exited: make(chan struct{})}
	go func() {
		h.err = cmd.Wait()
		close(h.exited)
	}()
	t.Cleanup(func() {
		_ = syscall.Kill(-h.Pid, syscall.SIGKILL)
		_ = stdin.Close()
		<-h.exited
	})
	return h
}

// CloseOnExec says whether the process holds the passed socket close-on-exec at descriptor 3, from the flags in
// /proc/<pid>/fdinfo/3. Descriptor 3 has to name that socket before and after the flags are read: one closed and
// reused (Go opens everything close-on-exec) is errReplaced, never sealed.
func (h *Holder) CloseOnExec() (bool, error) {
	if err := h.holdsSocket(); err != nil {
		return false, err
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/%d", h.Pid, passedFD))
	if err != nil {
		return false, err
	}
	if err := h.holdsSocket(); err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "flags:"); ok {
			flags, err := strconv.ParseUint(strings.TrimSpace(v), 8, 64)
			return err == nil && flags&syscall.O_CLOEXEC != 0, err
		}
	}
	return false, errors.New("no flags in fdinfo")
}

// holdsSocket: descriptor 3 of the process is the passed socket.
func (h *Holder) holdsSocket() error {
	link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", h.Pid, passedFD))
	if err != nil {
		return err
	}
	if link != h.socket {
		return fmt.Errorf("descriptor %d is %s, %w %s", passedFD, link, errReplaced, h.socket)
	}
	return nil
}

// WaitSealed waits, for at most timeout, until the process holds the passed socket close-on-exec at descriptor 3. It
// fails at once when the process ends or descriptor 3 is something else; a descriptor it no longer holds never counts
// as sealed.
func (h *Holder) WaitSealed(timeout time.Duration) error {
	deadline := time.After(timeout)
	for {
		sealed, err := h.CloseOnExec()
		if sealed {
			return nil
		}
		if errors.Is(err, errReplaced) {
			return err
		}
		select {
		case <-h.exited:
			return fmt.Errorf("it ended (%v) without sealing descriptor %d", h.err, passedFD)
		case <-deadline:
			if err != nil {
				return fmt.Errorf("descriptor %d after %s: %w", passedFD, timeout, err)
			}
			return fmt.Errorf("descriptor %d still inheritable after %s", passedFD, timeout)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// Running says whether the process has not ended yet.
func (h *Holder) Running() bool {
	select {
	case <-h.exited:
		return false
	default:
		return true
	}
}
