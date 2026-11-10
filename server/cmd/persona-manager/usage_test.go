package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The usage points at the details of the manager commands with a command that prints them: their usage, with the
// flags of join, install and uninstall (status -h printed only status's -dir). It runs as persona-manager, through
// TestPersonaManagerCommandLine's child.
func TestPersonaManagerUsagePointsAtTheDetails(t *testing.T) {
	_, rest, _ := strings.Cut(usage, "docs/assistant/managers.md; ")
	pointer, _, ok := strings.Cut(rest, " for the details")
	args := strings.Fields(pointer)
	if !ok || len(args) < 2 || args[0] != "persona-manager" {
		t.Fatalf("no persona-manager command for the details in the usage:\n%s", usage)
	}
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPersonaManagerCommandLine$")
	cmd.Env = append(os.Environ(), "PERSONA_MANAGER_TEST_ARGS="+strings.Join(args[1:], "\x1f"), "HOME="+home,
		"PERSONA_HOME="+filepath.Join(home, ".persona"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", pointer, err, out)
	}
	for _, want := range []string{"persona-manager manager join", "--database-url", "--builtin", "--purge"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("%s prints no %q:\n%s", pointer, want, out)
		}
	}
}
