package hub

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/clitext"
)

// `hub admin` and `hub manager-mode` keep the command line's convention (#1762): help asked for prints the usage on
// stdout and succeeds (exit 0); a usage mistake is a clitext.UsageError (exit 2, no log timestamp), found before
// anything is read or made.

func TestAdminCommandUsageMistakesAreUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"bogus"}, {"setup-code", "x"}} {
		var out, errOut bytes.Buffer
		err := AdminCommand(context.Background(), args, strings.NewReader(""), &out, &errOut)
		var ue clitext.UsageError
		if !errors.As(err, &ue) {
			t.Errorf("admin %q: %T %v, want a clitext.UsageError", args, err, err)
			continue
		}
		if !strings.Contains(ue.Error(), "hub admin access") {
			t.Errorf("admin %q: no usage in %q", args, ue.Error())
		}
		if out.Len() != 0 || errOut.Len() != 0 {
			t.Errorf("admin %q printed %q / %q", args, out.String(), errOut.String())
		}
	}
}

func TestAdminCommandHelpPrintsUsage(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}, {"setup-code", "-h"}} {
		var out, errOut bytes.Buffer
		if err := AdminCommand(context.Background(), args, strings.NewReader(""), &out, &errOut); err != nil {
			t.Errorf("admin %q: %v", args, err)
			continue
		}
		if !strings.Contains(out.String(), "hub admin access") || !strings.Contains(out.String(), "setup-code") {
			t.Errorf("admin %q: stdout %q, want the usage", args, out.String())
		}
		if errOut.Len() != 0 {
			t.Errorf("admin %q: stderr %q", args, errOut.String())
		}
	}
}

func TestManagerModeUsageMistakesAreUsageErrors(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"bogus"}, {"child", "service"}, {"service", "x"}} {
		var out bytes.Buffer
		err := managerMode(dir, args, &out)
		var ue clitext.UsageError
		if !errors.As(err, &ue) {
			t.Errorf("manager-mode %q: %T %v, want a clitext.UsageError", args, err, err)
			continue
		}
		if !strings.Contains(ue.Error(), "hub manager-mode [child|service]") {
			t.Errorf("manager-mode %q: no usage in %q", args, ue.Error())
		}
		if out.Len() != 0 {
			t.Errorf("manager-mode %q printed %q", args, out.String())
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a usage mistake wrote %v", entries)
	}
}

// ManagerMode answers help and refuses a usage mistake before it makes the state directory: an empty HOME stays empty.
func TestManagerModeHelpAndMistakesLeaveHomeAlone(t *testing.T) {
	home := emptyHome(t)
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}, {"service", "-h"}} {
		var out bytes.Buffer
		if err := ManagerMode(args, &out); err != nil {
			t.Errorf("manager-mode %q: %v", args, err)
			continue
		}
		if !strings.Contains(out.String(), "hub manager-mode [child|service]") {
			t.Errorf("manager-mode %q: stdout %q, want the usage", args, out.String())
		}
	}
	for _, args := range [][]string{{"bogus"}, {"child", "service"}} {
		var out bytes.Buffer
		var ue clitext.UsageError
		if err := ManagerMode(args, &out); !errors.As(err, &ue) {
			t.Errorf("manager-mode %q: %T %v, want a clitext.UsageError", args, err, err)
		}
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("HOME is not empty: %v", entries)
	}
}
