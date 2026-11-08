package nodecmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc/osproctest"
)

// TestMain lets the test binary be the node of TestNodeSealsInherited (nodeEntryEnv).
func TestMain(m *testing.M) {
	if arg := os.Getenv(nodeEntryEnv); arg != "" {
		os.Exit(nodeEntry(arg))
	}
	os.Exit(m.Run())
}

// nodeEntryEnv runs the test binary as `node <value>` (--stdio, --attach), or, with "hold", as a process that only
// holds what it was handed until stdin closes.
const nodeEntryEnv = "PERSONA_TEST_NODE_ENTRY"

func nodeEntry(arg string) int {
	if arg == "hold" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return 0
	}
	if _, err := Run(context.Background(), func() {}, []string{"node", arg}, Build{Version: "test"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// TestNodeSealsInherited: the node on stdin / stdout and the resident node, started holding an inheritable listening
// socket as an older hub's reaches them through a manager, make it close-on-exec while they run, so none of the agents
// and programs they start gets it. The control, a process that does not seal, keeps it inheritable.
func TestNodeSealsInherited(t *testing.T) {
	start := func(t *testing.T, arg string) *osproctest.Holder {
		cmd := exec.Command(os.Args[0], "-test.run=^$")
		cmd.Env = osproctest.HomeEnv(t, nodeEntryEnv+"="+arg)
		cmd.Stderr = os.Stderr
		return osproctest.StartHolding(t, cmd)
	}
	t.Run("control", func(t *testing.T) {
		h := start(t, "hold")
		time.Sleep(300 * time.Millisecond)
		if sealed, err := h.CloseOnExec(); err != nil || sealed || !h.Running() {
			t.Fatalf("a process that does not seal: sealed %v (%v), running %v; want it holding the socket inheritable",
				sealed, err, h.Running())
		}
	})
	for _, arg := range []string{"--stdio", "--attach"} {
		t.Run(arg, func(t *testing.T) {
			if err := start(t, arg).WaitSealed(20 * time.Second); err != nil {
				t.Fatalf("node %s: %v", arg, err)
			}
		})
	}
}
