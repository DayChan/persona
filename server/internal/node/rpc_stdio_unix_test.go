//go:build unix

package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
	"golang.org/x/sys/unix"
)

func stdioFixtureEnv(mode string) []string {
	var env []string
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "PERSONA_RPC_STDIO_TEST", "PERSONA_RPC_STDIO_EOF_TEST", "GORACE":
			continue
		}
		env = append(env, value)
	}
	return append(env, "PERSONA_RPC_STDIO_TEST="+mode, "GORACE=atexit_sleep_ms=0")
}

// This helper never creates/runs a production Node. It exercises the very same
// ServeStdio entry/initializer on genuine inherited os.Stdin/os.Stdout, then the
// actual newConnectionPeer and response wrapper, with only local RPC I/O.
func TestNodeRPCInheritedStdioChild(t *testing.T) {
	mode := os.Getenv("PERSONA_RPC_STDIO_TEST")
	if mode == "" {
		return
	}
	before, err := unix.FcntlInt(1, unix.F_GETFL, 0)
	if err != nil || before&unix.O_NONBLOCK != 0 {
		t.Fatalf("FAIL fixture stdout was not inherited blocking: flags=%d err=%v", before, err)
	}
	if mode == "reject" {
		// Even the exported entry must fail before invoking a nil Node's Serve.
		var n *Node
		if err := n.ServeStdio(context.Background()); err == nil {
			t.Fatal("FAIL regular-file stdio was accepted")
		}
		fmt.Fprintln(os.Stderr, "PASS exported ServeStdio fails closed before Node activity")
		return
	}
	err = serveOwnedStdio(context.Background(), func(ctx context.Context, r io.Reader, w io.Writer) error {
		after, err := unix.FcntlInt(1, unix.F_GETFL, 0) // do not call File.Fd after setup
		if err != nil || after&unix.O_NONBLOCK == 0 {
			return fmt.Errorf("FAIL shared stdout flags: %d %v", after, err)
		}
		for _, fd := range []uintptr{0, 1} {
			flags, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
			if err != nil || flags&unix.O_NONBLOCK == 0 {
				return fmt.Errorf("FAIL shared original flags before I/O: %d %v", flags, err)
			}
		}
		owned := w.(*ownedRPCTransport)
		wrapped := &updateResponseWriter{Writer: w, responseWritten: func(string) { t.Error("FAIL partial frame acknowledged an update") }}
		peer := newConnectionPeer(wrapped, w)
		if peer.Interrupt == nil {
			return errors.New("FAIL actual stdio owner capability missing")
		}
		callCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		expected := error(context.DeadlineExceeded)
		if mode == "cancel" {
			cancel()
			callCtx, cancel = context.WithCancel(ctx)
			expected = context.Canceled
			peer.OnEvent = func(event string, _ json.RawMessage) {
				if event == "fixture.cancel" {
					cancel()
				}
			}
		}
		defer cancel()
		readDone := make(chan error, 1)
		go func() { readDone <- peer.Serve(ctx, r) }()
		start := time.Now()
		err = peer.Call(callCtx, "mcp.call", strings.Repeat("界", 256<<10), nil)
		took := time.Since(start)
		if !errors.Is(err, expected) || !errors.Is(err, rpc.ErrDropped) || took > rpc.StallWindow+time.Second {
			return fmt.Errorf("FAIL inherited admitted call: %v after %v", err, took)
		}
		closed := make(chan struct{})
		go func() { peer.Close(rpc.ErrClosed); owned.interrupt(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(100 * time.Millisecond):
			return errors.New("TIMEOUT owned Close")
		}
		select {
		case <-readDone:
		case <-time.After(100 * time.Millisecond):
			return errors.New("TIMEOUT active inherited Read interruption")
		}
		if err := peer.Notify("next", nil); !errors.Is(err, rpc.ErrClosed) {
			return errors.New("FAIL subsequent frame after abandoned prefix")
		}
		// Originals were explicitly transferred, not borrowed. Keeping either
		// stdout alias open would hide transport EOF from the hub's approvals.
		for _, fd := range []uintptr{0, 1} {
			if _, err := unix.FcntlInt(fd, unix.F_GETFL, 0); !errors.Is(err, unix.EBADF) {
				return fmt.Errorf("FAIL transferred stdio endpoint still open: fd=%d err=%v", fd, err)
			}
		}
		fmt.Fprintf(os.Stderr, "PASS inherited stdout flags=%d/%d admitted deadline=400ms Call=%v Close/read complete before 700ms\n", before, after, took)
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if os.Getenv("PERSONA_RPC_STDIO_EOF_TEST") == "1" {
		// Keep the child alive after owned Close. The parent must observe EOF
		// from descriptor ownership, not rescue the test by killing/exiting us.
		fds := []unix.PollFd{{Fd: 3, Events: unix.POLLIN}}
		if n, err := unix.Poll(fds, 2000); err != nil || n != 1 {
			fmt.Fprintln(os.Stderr, "TIMEOUT parent EOF acknowledgement", err)
			os.Exit(2)
		}
		if n, err := unix.Read(3, make([]byte, 1)); err != nil || n != 1 {
			fmt.Fprintln(os.Stderr, "FAIL parent EOF acknowledgement", err)
			os.Exit(2)
		}
	}
	// The testing runner otherwise writes PASS to the now-full inherited stdout.
	// Do not drain the pipe or mask that unrelated output as protocol admission.
	os.Exit(0)
}

func TestNodeRPCInheritedStdioDeadline(t *testing.T) {
	for i := 0; i < 6; i++ {
		mode := "deadline"
		if i >= 3 {
			mode = "cancel"
		}
		t.Run(mode+fmt.Sprint(i), func(t *testing.T) {
			input, inputW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			output, outputW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			logs, logsW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			defer inputW.Close()
			defer output.Close()
			defer outputW.Close()
			defer logs.Close()
			defer logsW.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestNodeRPCInheritedStdioChild$", "-test.timeout=20s")
			cmd.Env = stdioFixtureEnv(mode)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = input, outputW, logsW
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = input.Close()
			_ = outputW.Close()
			_ = logsW.Close()
			wait := make(chan error, 1)
			go func() { wait <- cmd.Wait() }()
			finished := false
			t.Cleanup(func() {
				if !finished {
					_ = cmd.Process.Kill() // exactly our child, never a name/PGID search
					select {
					case <-wait:
					case <-time.After(2 * time.Second):
						t.Error("TIMEOUT own child cleanup")
					}
				}
			})
			prefix := make(chan error, 1)
			go func() {
				b := make([]byte, 3)
				_, err := io.ReadFull(output, b)
				if err == nil && string(b) != `{"i` {
					err = fmt.Errorf("FAIL non-request prefix %q", b)
				}
				prefix <- err
			}()
			select {
			case err := <-prefix:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("TIMEOUT inherited stdout prefix admission")
			}
			if mode == "cancel" {
				if _, err := fmt.Fprintln(inputW, `{"event":"fixture.cancel"}`); err != nil {
					t.Fatal(err)
				}
			}
			// No drain, and do not close the parent's reader to rescue the child.
			// The old inherited blocking transport remained stuck at this point.
			select {
			case err := <-wait:
				finished = true
				if err != nil {
					b, _ := io.ReadAll(logs)
					t.Fatalf("FAIL child: %v %s", err, b)
				}
			case <-time.After(rpc.StallWindow + 2*time.Second):
				t.Fatal("TIMEOUT admitted inherited stdout Call/Close")
			}
			b, err := io.ReadAll(logs) // child reaped, all its stderr writers closed
			if err != nil || !strings.Contains(string(b), "PASS inherited stdout flags=") {
				t.Fatalf("FAIL child contract verdict: %s %v", b, err)
			}
			t.Log(strings.TrimSpace(string(b)))
		})
	}
}

func TestNodeRPCServeStdioRejectsRegularFile(t *testing.T) {
	input, inputW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer inputW.Close()
	output, err := os.CreateTemp(t.TempDir(), "stdio-output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNodeRPCInheritedStdioChild$", "-test.timeout=2s")
	cmd.Env = stdioFixtureEnv("reject")
	cmd.Stdin, cmd.Stdout = input, output
	logs, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	sc := bufio.NewScanner(logs)
	for sc.Scan() {
		text.WriteString(sc.Text())
	}
	if err := cmd.Wait(); err != nil || ctx.Err() != nil || !strings.Contains(text.String(), "PASS exported ServeStdio") {
		t.Fatalf("FAIL regular-file preflight: %v %s", err, text.String())
	}
	t.Log("PASS actual ServeStdio refuses regular-file stdout before protocol/background activity")
}

func TestNodeRPCBorrowedFileFlagsAndClose(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	fd := w.Fd() // inherited-like blocking descriptor
	before, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &updateResponseWriter{Writer: w, responseWritten: func(string) {}}
	peer := newConnectionPeer(wrapped, w)
	if peer.Interrupt != nil {
		t.Fatal("FAIL borrowed file gained owner capability")
	}
	peer.Close(rpc.ErrClosed)
	after, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err != nil || before != after {
		t.Fatal("FAIL borrowed descriptor closed/flags changed", err)
	}
	if _, err := w.Write([]byte("ok")); err != nil {
		t.Fatal("FAIL borrowed file unusable", err)
	}
	b := make([]byte, 2)
	if _, err := io.ReadFull(r, b); err != nil || string(b) != "ok" {
		t.Fatal("FAIL borrowed pipe", err)
	}
	t.Log("PASS borrowed inherited-like file and update wrapper retain flags/ownership; no hard-interrupt promise")
}
