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

	"github.com/nwverse/persona/server/internal/rpc"
	"golang.org/x/sys/unix"
)

func TestNodeRPCInheritedHealthyStdioChild(t *testing.T) {
	if os.Getenv("PERSONA_RPC_STDIO_TEST") != "healthy" {
		return
	}
	before, err := unix.FcntlInt(1, unix.F_GETFL, 0)
	if err != nil || before&unix.O_NONBLOCK != 0 {
		fmt.Fprintln(os.Stderr, "FAIL healthy fixture was not inherited blocking stdout")
		os.Exit(2)
	}
	err = serveOwnedStdio(context.Background(), func(ctx context.Context, r io.Reader, w io.Writer) error {
		peer := newConnectionPeer(w, w)
		peer.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			var body string
			if err := json.Unmarshal(params, &body); err != nil {
				return nil, err
			}
			if method != "large" || len(body) != 2<<20 {
				return nil, errors.New("FAIL large request body")
			}
			return body, nil
		}
		if err := peer.Serve(ctx, r); !errors.Is(err, io.EOF) {
			return err
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "PASS inherited stdio large request/response and actual EOF")
	os.Exit(0) // no testing output appended to protocol stdout
}

func TestNodeRPCInheritedStdioLargeBodyEOF(t *testing.T) {
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
	logs, logsW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	defer logsW.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestNodeRPCInheritedHealthyStdioChild$", "-test.timeout=6s")
	cmd.Env = stdioFixtureEnv("healthy")
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
			_ = cmd.Process.Kill()
			select {
			case <-wait:
			case <-time.After(2 * time.Second):
				t.Error("TIMEOUT own healthy child cleanup")
			}
		}
	})
	hub := rpc.New("h", inputW)
	hub.Interrupt = func() { _ = inputW.Close(); _ = output.Close() }
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- hub.Serve(ctx, output) }()
	var result string
	body := strings.Repeat("x", 2<<20)
	if err := hub.Call(ctx, "large", body, &result); err != nil || result != body {
		hub.Close(rpc.ErrClosed)
		t.Fatal("FAIL healthy inherited large-body call", err, len(result))
	}
	_ = inputW.Close() // actual child stdin EOF, after response admission/completion
	select {
	case err := <-wait:
		finished = true
		if err != nil {
			b, _ := io.ReadAll(logs)
			t.Fatalf("FAIL healthy child: %v %s", err, b)
		}
	case <-time.After(time.Second):
		t.Fatal("TIMEOUT inherited EOF child cleanup")
	}
	select {
	case <-served:
	case <-time.After(time.Second):
		t.Fatal("TIMEOUT healthy parent reader")
	}
	b, err := io.ReadAll(logs)
	if err != nil || !strings.Contains(string(b), "PASS inherited stdio large request/response") {
		t.Fatalf("FAIL healthy verdict: %s %v", b, err)
	}
	t.Log(strings.TrimSpace(string(b)))
}
