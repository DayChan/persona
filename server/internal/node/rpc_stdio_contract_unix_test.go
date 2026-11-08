//go:build unix

package node

import (
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

	"golang.org/x/sys/unix"

	"github.com/nwverse/persona/server/internal/rpc"
)

func TestNodeRPCInheritedSocketDeadline(t *testing.T) {
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
			if err != nil {
				t.Fatal(err)
			}
			// Only the parent's independent endpoint is made pollable. The child
			// inherits one genuinely blocking open description as BOTH stdin and
			// stdout, proving duplication/flag sharing on aliased descriptors.
			if err := unix.SetNonblock(fds[0], true); err != nil {
				t.Fatal(err)
			}
			parent := os.NewFile(uintptr(fds[0]), "parent-socket")
			child := os.NewFile(uintptr(fds[1]), "child-socket")
			defer parent.Close()
			defer child.Close()
			logs, logsW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer logs.Close()
			defer logsW.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestNodeRPCInheritedStdioChild$", "-test.timeout=20s")
			cmd.Env = stdioFixtureEnv("deadline")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = child, child, logsW
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = child.Close()
			_ = logsW.Close()
			wait := make(chan error, 1)
			go func() { wait <- cmd.Wait() }()
			finished := false
			t.Cleanup(func() {
				if !finished {
					_ = cmd.Process.Kill()
					select {
					case <-wait:
					case <-time.After(2 * time.Second):
						t.Error("TIMEOUT own socket child cleanup")
					}
				}
			})
			if err := parent.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Fatal(err)
			}
			prefix := make([]byte, 3)
			if _, err := io.ReadFull(parent, prefix); err != nil || string(prefix) != `{"i` {
				t.Fatalf("FAIL real socket prefix: %q %v", prefix, err)
			}
			select {
			case err := <-wait:
				finished = true
				if err != nil {
					b, _ := io.ReadAll(logs)
					t.Fatalf("FAIL child: %v %s", err, b)
				}
			// An admitted Write is interrupted once it makes no progress for the stall window.
			case <-time.After(rpc.StallWindow + 2*time.Second):
				t.Fatal("TIMEOUT active inherited socket I/O")
			}
			b, err := io.ReadAll(logs)
			if err != nil || !strings.Contains(string(b), "PASS inherited stdout flags=") {
				t.Fatalf("FAIL socket verdict: %s %v", b, err)
			}
			t.Log(strings.TrimSpace(string(b)))
		})
	}
}

func TestNodeRPCStdioRejectedSetupPreservesFlags(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	file, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	raw, err := r.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var fd uintptr
	if err := raw.Control(func(value uintptr) { fd = value }); err != nil {
		t.Fatal(err)
	}
	before, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openOwnedStdio(r, file); !errors.Is(err, errStdioNotInterruptible) {
		t.Fatal("FAIL regular-file rejection", err)
	}
	after, err := unix.FcntlInt(fd, unix.F_GETFL, 0)
	if err != nil || before != after {
		t.Fatalf("FAIL rejected setup changed original flags: %d/%d %v", before, after, err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(r, make([]byte, 1)); err != nil {
		t.Fatal("FAIL original closed", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := openOwnedStdio(r, file); !errors.Is(err, os.ErrClosed) {
		t.Fatal("FAIL closed descriptor setup", err)
	}
	t.Log("PASS rejected regular/closed descriptor setup preserves original flags and files")
}

func TestNodeRPCOwnedStdioEOFCancelsAcceptedWork(t *testing.T) {
	input, inputW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer inputW.Close()
	output, outputW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	defer outputW.Close()
	owned, err := openOwnedStdio(input, outputW)
	if err != nil {
		t.Fatal(err)
	}
	defer owned.interrupt()
	peer := newConnectionPeer(&updateResponseWriter{Writer: owned, responseWritten: func(string) {}}, owned)
	request, ended := make(chan context.Context, 1), make(chan struct{})
	peer.OnRequest = func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		request <- ctx
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Error("TIMEOUT accepted request cancellation")
		}
		close(ended)
		return nil, ctx.Err()
	}
	result := make(chan error, 1)
	go func() { result <- peer.Serve(context.Background(), owned) }()
	if _, err := fmt.Fprintln(inputW, `{"id":"h1","method":"approval"}`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-request:
	case <-time.After(time.Second):
		t.Fatal("TIMEOUT request admission")
	}
	_ = inputW.Close()
	select {
	case err := <-result:
		if !errors.Is(err, io.EOF) {
			t.Fatal("FAIL owned EOF", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TIMEOUT owned EOF")
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("TIMEOUT accepted work after EOF")
	}
	t.Log("PASS owned stdio EOF cancels accepted work and terminates active I/O")
}
