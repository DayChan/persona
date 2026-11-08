package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc/osproctest"
	"github.com/nwverse/persona/server/internal/update"
)

// TestMain lets the test binary be the manager of TestManagerSealsInherited (managerEntryEnv).
func TestMain(m *testing.M) {
	if os.Getenv(managerEntryEnv) == "1" {
		os.Exit(managerEntry())
	}
	os.Exit(m.Run())
}

// managerEntryEnv set to 1 runs the test binary as a manager on a directory without manager.json, then holds what it
// was handed until stdin closes.
const managerEntryEnv = "PERSONA_TEST_MANAGER_ENTRY"

func managerEntry() int {
	dir := filepath.Join(os.Getenv("HOME"), "no-manager")
	err := Run(context.Background(), update.Build{Version: "test"}, dir, io.Discard)
	if !errors.As(err, new(ConfigError)) {
		fmt.Fprintf(os.Stderr, "manager: %v; want its configuration missing\n", err)
		return 1
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	return 0
}

// TestManagerSealsInherited: a manager started holding an inheritable listening socket, as an older hub's reaches it,
// makes it close-on-exec before anything else (before it reads its configuration), so none of the nodes, extensions
// and agents it starts gets it. A manager needs its database to run further; this one stops at its missing
// manager.json.
func TestManagerSealsInherited(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = osproctest.HomeEnv(t, managerEntryEnv+"=1")
	cmd.Stderr = os.Stderr
	if err := osproctest.StartHolding(t, cmd).WaitSealed(20 * time.Second); err != nil {
		t.Fatal(err)
	}
}
