//go:build unix

package node

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// Closing only the pollable duplicates would free Call but leave original stdout
// open, hiding transport termination from a pending approval at the hub. This
// observes actual pipe EOF while the child is still alive behind a separate ack.
func TestNodeRPCInheritedOwnedClosePublishesEOFBeforeExit(t *testing.T) {
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
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
			ack, ackW, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer ack.Close()
			defer ackW.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestNodeRPCInheritedStdioChild$", "-test.timeout=20s")
			cmd.Env = append(stdioFixtureEnv("deadline"), "PERSONA_RPC_STDIO_EOF_TEST=1")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = input, outputW, logsW
			cmd.ExtraFiles = []*os.File{ack}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = input.Close()
			_ = outputW.Close()
			_ = logsW.Close()
			_ = ack.Close()
			wait := make(chan error, 1)
			go func() { wait <- cmd.Wait() }()
			finished := false
			t.Cleanup(func() {
				if !finished {
					_ = cmd.Process.Kill()
					select {
					case <-wait:
					case <-time.After(2 * time.Second):
						t.Error("TIMEOUT own EOF child cleanup")
					}
				}
			})
			prefix := make(chan error, 1)
			go func() {
				b := make([]byte, 3)
				_, err := io.ReadFull(output, b)
				if err == nil && string(b) != `{"i` {
					err = fmt.Errorf("FAIL request admission prefix %q", b)
				}
				prefix <- err
			}()
			select {
			case err := <-prefix:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("TIMEOUT real inherited request admission")
			}
			verdict := make(chan string, 1)
			go func() { line, _ := bufio.NewReader(logs).ReadString('\n'); verdict <- line }()
			select {
			case line := <-verdict:
				if !strings.HasPrefix(line, "PASS inherited stdout flags=") {
					t.Fatalf("FAIL Call/Close verdict: %s", line)
				}
			case <-time.After(rpc.StallWindow + 2*time.Second):
				t.Fatal("TIMEOUT owned Call/Close with parent reader stopped")
			}
			select {
			case <-wait:
				finished = true
				t.Fatal("FAIL EOF evidence came from child exit")
			default:
			}
			eof := make(chan error, 1)
			go func() { _, err := io.Copy(io.Discard, output); eof <- err }()
			select {
			case err := <-eof:
				if err != nil {
					t.Fatal("FAIL actual pipe EOF", err)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatal("TIMEOUT stdout alias hid EOF after owned Close")
			}
			select {
			case <-wait:
				finished = true
				t.Fatal("FAIL child exited before parent EOF acknowledgement")
			default:
			}
			if _, err := ackW.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-wait:
				finished = true
				if err != nil {
					t.Fatal("FAIL own child", err)
				}
			case <-time.After(time.Second):
				t.Fatal("TIMEOUT acknowledged own child reap")
			}
			t.Log("PASS admitted inherited Call/Close within the stall bound while reader stopped; actual pipe EOF before child exit, no hidden stdout alias")
		})
	}
}
