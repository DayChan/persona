package manager

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/update"
)

// A flag mistake in a manager command (a flag it does not know, a flag without its value) is a usage mistake: Main
// returns ErrUsage carrying the flag's message, writes nothing to stdout and touches nothing. -h prints the command's
// flags on stdout and returns ErrHelp.
func TestFlagMistakesAreUsageMistakes(t *testing.T) {
	home := isolate(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"status", "--json"}, "flag provided but not defined: -json"},
		{[]string{"install", "--bogus"}, "flag provided but not defined: -bogus"},
		{[]string{"uninstall", "--bogus"}, "flag provided but not defined: -bogus"},
		{[]string{"run", "--bogus"}, "flag provided but not defined: -bogus"},
		{[]string{"join", "--bogus"}, "flag provided but not defined: -bogus"},
		{[]string{"join", "--dir"}, "flag needs an argument: -dir"},
		{[]string{"install", "--builtin", "--bogus"}, "flag provided but not defined: -bogus"},
		{[]string{"capabilities", "--bogus"}, "flag provided but not defined: -bogus"},
	} {
		var out bytes.Buffer
		err := Main(context.Background(), update.Build{}, c.args, &out)
		if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want a usage mistake saying %q", c.args, err, c.want)
		}
		if out.Len() != 0 {
			t.Errorf("%q: stdout %q, want nothing", c.args, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".persona")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a flag mistake wrote state: %v", err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"status", "-h"}, "-dir"},
		{[]string{"capabilities", "-h"}, "-json"},
	} {
		var out bytes.Buffer
		err := Main(context.Background(), update.Build{}, c.args, &out)
		if !errors.Is(err, ErrHelp) || !strings.Contains(out.String(), c.want) {
			t.Errorf("%q: %v, stdout %q; want ErrHelp and %q on stdout", c.args, err, out.String(), c.want)
		}
	}
}
