//go:build linux

package host

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"golang.org/x/sys/unix"
)

// The isolated worker is a subreaper only for its own synthetic helpers. This
// does not change the server, source guest exec, or another session's processes.
func TestOwnedGroupLifecycle(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestOwnedGroupLifecycleWorker$", "-test.v", "-test.timeout=25s")
	cmd.Env = append(os.Environ(), "HOST_OWNED_GROUP_WORKER=1", "HOST_TEST_PLUGIN=")
	out, err := cmd.CombinedOutput()
	t.Log(string(out))
	if err != nil {
		t.Fatalf("FAIL bounded owned group worker: %v", err)
	}
	t.Log("PASS bounded actual fork/group lifecycle worker")
}

func TestOwnedGroupLifecycleWorker(t *testing.T) {
	if os.Getenv("HOST_OWNED_GROUP_WORKER") != "1" {
		return
	}
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		t.Fatal(err)
	}
	defer unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 0, 0, 0, 0)
	old, ready, restart := StopGrace, ReadyWithin, RestartFirst
	StopGrace, ReadyWithin, RestartFirst = 30*time.Millisecond, 250*time.Millisecond, 20*time.Millisecond
	defer func() { StopGrace, ReadyWithin, RestartFirst = old, ready, restart }()
	for _, kind := range []string{"fork", "joined"} {
		for _, ending := range []string{"leader-exit", "startup-failure", "idle", "stop", "leader-exit-success"} {
			t.Run(kind+"/"+ending, func(t *testing.T) { ownedGroupCase(t, kind, ending) })
		}
	}
}

type ownedCountObserver struct {
	runCount, maximum atomic.Int32
}

func (*ownedCountObserver) Call(string, time.Duration, string) {}
func (*ownedCountObserver) Log(string, string)                 {}
func (o *ownedCountObserver) Run(event, _ string) {
	if event != RunStart {
		o.runCount.Add(-1)
		return
	}
	n := o.runCount.Add(1)
	for {
		previous := o.maximum.Load()
		if n <= previous || o.maximum.CompareAndSwap(previous, n) {
			return
		}
	}
}

// This Python leader really calls os.fork. Its child ignores TERM and has an
// independent control connection; neither inherited pipe EOF nor a timer is
// taken as group completion. The joined control is not called a descendant.
const ownedGroupHelper = `import json, os, signal, socket, sys, threading
mode, address = sys.argv[1:]
def connect(role):
    c = socket.socket(socket.AF_UNIX)
    c.settimeout(15)
    c.connect(address)
    c.sendall((role + " " + str(os.getpid()) + "\n").encode())
    return c
if mode == "member":
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    c = connect("member")
    while c.recv(256): pass
    sys.exit(0)
c = connect("leader")
def control():
    for line in c.makefile("rb"):
        if line.strip() == b"exit": os._exit(0)
threading.Thread(target=control, daemon=True).start()
if mode == "fork":
    child = os.fork()
    if child == 0:
        c.close()
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        for fd in (0, 1, 2):
            try: os.close(fd)
            except OSError: pass
        c = connect("member")
        while c.recv(256): pass
        os._exit(0)
for line in sys.stdin:
    message = json.loads(line)
    if message.get("method") == "initialize":
        result = {"protocol": message["params"]["protocol"], "id": "demo", "version": "1.0.0"}
    else: result = os.getpid()
    print(json.dumps({"id": message.get("id"), "result": result}), flush=True)
os._exit(0)
`

func ownedGroupCase(t *testing.T, kind, ending string) {
	dir := t.TempDir()
	script, socket := filepath.Join(dir, "helper.py"), filepath.Join(dir, "control")
	if err := os.WriteFile(script, []byte(ownedGroupHelper), 0o600); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("FAIL real fork fixture needs Python", err)
	}
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_ = ln.(*net.UnixListener).SetDeadline(time.Now().Add(3 * time.Second))
	var connections []net.Conn
	accept := func(role string) (net.Conn, int) {
		t.Helper()
		c, err := ln.Accept()
		if err != nil {
			t.Fatal("TIMEOUT helper handshake", err)
		}
		connections = append(connections, c)
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		line, err := bufio.NewReader(c).ReadString('\n')
		if err != nil {
			t.Fatal("TIMEOUT helper marker", err)
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != role {
			t.Fatal("FAIL helper identity", line)
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatal(err)
		}
		return c, pid
	}
	t.Cleanup(func() {
		for _, c := range connections {
			_ = c.Close()
		}
	})
	var starts, holds, maximum atomic.Int32
	observer := &ownedCountObserver{}
	s := spec(t, "")
	s.Observe = observer
	s.Command, s.Args, s.Env = python, []string{"-I", script, kind, socket}, nil
	s.Startup, s.OnCall, s.Idle = true, true, time.Hour
	if ending == "idle" {
		s.Startup, s.Idle = false, 40*time.Millisecond
	}
	// Install the startup gate below using the real Spec signature.
	initGate := make(chan struct{})
	if ending == "startup-failure" {
		original := s.Init
		s.Init = func(ctx context.Context) (extension.InitializeParams, error) {
			select {
			case <-initGate:
				return extension.InitializeParams{}, errors.New("synthetic initialization failure")
			case <-ctx.Done():
				return original(ctx)
			}
		}
	}
	s.Prepare = func(command string, args []string) (string, []string, []string, []*os.File, func(), error) {
		if starts.Add(1) > 1 {
			args = []string{"-I", script, "plain", socket}
		}
		n := holds.Add(1)
		for {
			prev := maximum.Load()
			if n <= prev || maximum.CompareAndSwap(prev, n) {
				break
			}
		}
		return command, args, nil, nil, func() { holds.Add(-1) }, nil
	}
	p := Start(context.Background(), s)
	var releaseIdle func()
	if ending == "idle" {
		releaseIdle = p.Hold()
	}
	t.Cleanup(func() {
		p.cancel()
		select {
		case <-p.Done():
		case <-time.After(2 * time.Second):
			t.Error("TIMEOUT owned Proc cleanup")
		}
	})
	leader, leaderPID := accept("leader")
	var member *exec.Cmd
	var memberPID int
	var memberConn net.Conn
	var memberReaped sync.Once
	if kind == "joined" {
		member = exec.Command(python, "-I", script, "member", socket)
		member.SysProcAttr = &unix.SysProcAttr{Setpgid: true, Pgid: leaderPID}
		if err := member.Start(); err != nil {
			t.Fatal(err)
		}
		memberConn, memberPID = accept("member")
	} else {
		memberConn, memberPID = accept("member")
	}
	reapMember := func() {
		memberReaped.Do(func() {
			if member != nil {
				_ = member.Process.Kill() // the owned handle, never a numeric group
				_ = member.Wait()
				return
			}
			// The owned worker adopts only this fixture's real fork descendant.
			// Poll only its recorded PID, never all adopted/other processes.
			deadline := time.Now().Add(2 * time.Second)
			for {
				var status unix.WaitStatus
				pid, err := unix.Wait4(memberPID, &status, unix.WNOHANG, nil)
				if pid == memberPID {
					return
				}
				if err != nil && !errors.Is(err, unix.ECHILD) {
					t.Error("FAIL owned member reap", err)
					return
				}
				if time.Now().After(deadline) {
					t.Error("TIMEOUT owned member reap")
					return
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
	t.Cleanup(func() { p.cancel(); _ = memberConn.Close(); reapMember() })
	neighbor := exec.Command(python, "-I", script, "member", socket)
	neighbor.SysProcAttr = &unix.SysProcAttr{Setpgid: true}
	if err := neighbor.Start(); err != nil {
		t.Fatal(err)
	}
	neighborConn, neighborPID := accept("member")
	t.Cleanup(func() { _ = neighborConn.Close(); _ = neighbor.Process.Kill(); _ = neighbor.Wait() })
	if ending != "startup-failure" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := p.Wait(ctx); err != nil {
			t.Fatal("FAIL initialized Proc", err)
		}
	}
	if group, err := unix.Getpgid(memberPID); err != nil || group != leaderPID {
		t.Fatal("FAIL actual group membership", group, err)
	}
	if ending == "startup-failure" {
		close(initGate)
	}
	if strings.HasPrefix(ending, "leader-exit") {
		_, _ = fmt.Fprintln(leader, "exit")
	}
	if ending == "idle" {
		releaseIdle()
	}
	if ending == "stop" {
		p.cancel()
	}
	if ending == "leader-exit-success" {
		deadline := time.Now().Add(time.Second)
		for {
			var info unix.Siginfo
			err := unix.Waitid(unix.P_PID, memberPID, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
			if err == nil && info.Signo != 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("TIMEOUT owned member force exit", err)
			}
			time.Sleep(time.Millisecond)
		}
		if holds.Load() != 1 || starts.Load() != 1 {
			t.Fatal("FAIL ownership released before member reap")
		}
		reapMember()
		_, replacementPID := accept("leader")
		if replacementPID == leaderPID || maximum.Load() != 1 || starts.Load() != 2 || observer.maximum.Load() != 1 || observer.runCount.Load() != 1 {
			t.Fatal("FAIL replacement lifetime/capacity/runCount", starts.Load(), maximum.Load(), observer.runCount.Load(), observer.maximum.Load())
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := p.StopContext(ctx); err != nil {
			t.Fatal("FAIL successful stop after actual group end", err)
		}
		if holds.Load() != 0 || unix.Kill(neighborPID, 0) != nil {
			t.Fatal("FAIL final hold or neighbor")
		}
		t.Log("PASS actual " + kind + " group completion before successful replacement, capacity maximum one and neighbor isolation")
		return
	}
	select {
	case <-p.teardownFailed:
	case <-time.After(2 * time.Second):
		t.Fatal("TIMEOUT bounded group failure")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	started := time.Now()
	if err := p.StopContext(ctx); !errors.Is(err, ErrTeardownTimeout) {
		t.Fatal("FAIL group hold report", err)
	}
	if time.Since(started) > 300*time.Millisecond {
		t.Fatal("FAIL teardown reporting budget")
	}
	select {
	case <-p.Done():
		t.Fatal("FAIL group still exists but Done closed")
	default:
	}
	if starts.Load() != 1 || holds.Load() != 1 || maximum.Load() != 1 || observer.runCount.Load() != 1 || observer.maximum.Load() != 1 {
		t.Fatal("FAIL admission/runCount/replacement before owned group end", starts.Load(), holds.Load(), maximum.Load(), observer.runCount.Load(), observer.maximum.Load())
	}
	if unix.Kill(neighborPID, 0) != nil {
		t.Fatal("FAIL neighbor sentinel was terminated")
	}
	// The reporting budget is not a scheduler guarantee that SIGKILL/Wait
	// has already completed. Separately observe actual force-exit/reap,
	// retaining every ownership assertion while that work finishes.
	deadline := time.Now().Add(2 * time.Second)
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, memberPID, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
		if err != nil {
			t.Fatal("FAIL owned member observation", err)
		}
		if info.Signo != 0 {
			break
		}
		select {
		case <-p.Done():
			t.Fatal("FAIL Done before actual member exit/reap")
		default:
		}
		if holds.Load() != 1 || starts.Load() != 1 || observer.runCount.Load() != 1 {
			t.Fatal("FAIL ownership released while force-exit still pending")
		}
		if time.Now().After(deadline) {
			t.Fatal("TIMEOUT TERM-ignoring owned member force-exit", info)
		}
		time.Sleep(time.Millisecond)
	}
	for {
		err := unix.Waitid(unix.P_PID, leaderPID, &info, unix.WEXITED|unix.WNOWAIT|unix.WNOHANG, nil)
		if errors.Is(err, unix.ECHILD) {
			break
		}
		if err != nil {
			t.Fatal("FAIL leader reap observation", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("TIMEOUT actual leader reap")
		}
		time.Sleep(time.Millisecond)
	}
	t.Log("PASS actual leader reap distinct from retained group")
	// A replacement request cannot make a terminally failed run release its hold.
	if err := p.Call(ctx, "pid", struct{}{}, nil); err == nil {
		t.Fatal("FAIL replacement admitted after failed cleanup")
	}
	reapMember()
	select {
	case <-p.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("TIMEOUT late owned group cleanup")
	}
	if starts.Load() != 1 || holds.Load() != 0 || maximum.Load() != 1 || observer.runCount.Load() != 0 || observer.maximum.Load() != 1 {
		t.Fatal("FAIL late ownership/runCount release", starts.Load(), holds.Load(), maximum.Load(), observer.runCount.Load(), observer.maximum.Load())
	}
	if unix.Kill(neighborPID, 0) != nil {
		t.Fatal("FAIL late cleanup signaled neighbor")
	}
	t.Log("PASS actual " + kind + " " + ending + " bounded failure/hold/no replacement/late cleanup/neighbor")
}
