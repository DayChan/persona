package managercli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc/osproctest"
	"github.com/nwverse/persona/server/internal/update"
)

// TestMain lets the test binary be the built-in manager of TestAssistantSealsInherited (assistantEntryEnv).
func TestMain(m *testing.M) {
	if os.Getenv(assistantEntryEnv) == "1" {
		os.Exit(assistantEntry())
	}
	os.Exit(m.Run())
}

// assistantEntryEnv set to 1 runs the test binary as `assistant` with no database configured, then holds what it was
// handed until stdin closes.
const assistantEntryEnv = "PERSONA_TEST_ASSISTANT_ENTRY"

func assistantEntry() int {
	err := Assistant(context.Background(), update.Build{Version: "test"}, nil, os.Stdin)
	if err == nil || !strings.Contains(err.Error(), "database_url") {
		fmt.Fprintf(os.Stderr, "assistant: %v; want no database configured\n", err)
		return 1
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

// TestAssistantSealsInherited: the built-in manager started holding an inheritable listening socket, as the hub that
// starts it can leave one, makes it close-on-exec before it reads its configuration, so none of the nodes, extensions
// and agents it starts gets it. It needs a database to run further; this one stops there.
func TestAssistantSealsInherited(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = osproctest.HomeEnv(t, assistantEntryEnv+"=1")
	cmd.Stderr = os.Stderr
	if err := osproctest.StartHolding(t, cmd).WaitSealed(20 * time.Second); err != nil {
		t.Fatal(err)
	}
}
