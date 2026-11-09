package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/hubcli"
)

// runHub runs main as persona-hub with args in a child process (this test binary again, through
// TestPersonaHubCommandLine; never with no command: that runs a hub) and returns what it printed and its exit code.
func runHub(t *testing.T, home string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPersonaHubCommandLine$")
	cmd.Env = append(os.Environ(), "PERSONA_HUB_TEST_ARGS="+strings.Join(args, "\x1f"), "HOME="+home,
		"PERSONA_HOME="+filepath.Join(home, ".persona"))
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

// The hub's command line, run as persona-hub: the version, the usage, and the mistakes, each with its exit code;
// `persona-hub hub <command>` is the same command.
func TestPersonaHubCommandLine(t *testing.T) {
	if args := os.Getenv("PERSONA_HUB_TEST_ARGS"); args != "" {
		os.Args = append([]string{"persona-hub"}, strings.Split(args, "\x1f")...)
		main()
		os.Exit(0)
	}
	home := t.TempDir()
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"version", "-v"}, 0, "channel "},
		{[]string{"--help"}, 0, "persona-hub setup"},
		{[]string{"help"}, 0, "persona-hub setup"},
		{[]string{"hub", "-h"}, 0, "persona-hub setup"},
		{[]string{"peer", "-h"}, 0, "usage: persona-hub hub peer add"},
		{[]string{"hub", "peer", "--help"}, 0, "usage: persona-hub hub peer add"},
		{[]string{"run", "--listen", ":1"}, 2, "usage: persona-hub [run]"},
		{[]string{"hub", "run", "--listen", ":1"}, 2, "usage: persona-hub [run]"},
		{[]string{"slot", "x"}, 2, "persona-hub slot takes no arguments"},
		{[]string{"doctor", "x"}, 2, "persona-hub doctor takes no arguments"},
		{[]string{"update-watch"}, 2, "usage: persona-hub update-watch <pending.json>"},
	} {
		stdout, stderr, code := runHub(t, home, c.args...)
		if out := stdout + stderr; code != c.code || !strings.Contains(out, c.want) {
			t.Errorf("%q: exit %d, want %d and %q in %q", c.args, code, c.code, c.want, out)
		}
	}
	// An unknown command is a usage mistake: what was wrong and persona-hub's usage on stderr, exit 2, no log timestamp.
	for _, args := range [][]string{{"bogus"}, {"hub", "bogus"}, {"assistant"} /* the manager's: persona-manager assistant */, {"tailcat-key"}} {
		want := fmt.Sprintf("unknown command %q\n%s\n", args[len(args)-1], usage)
		if stdout, stderr, code := runHub(t, home, args...); code != 2 || stdout != "" || stderr != want {
			t.Errorf("%q: exit %d, stdout %q, stderr %.200q; want exit 2 and %.200q on stderr", args, code, stdout, stderr, want)
		}
	}
}

// Every flag of the hub's commands with flags is in both usage texts (persona-hub's and `personad hub`'s): each
// command's -h, run as persona-hub, lists them.
func TestUsageNamesEveryFlag(t *testing.T) {
	home := t.TempDir()
	flagLine := regexp.MustCompile(`(?m)^  -(\S+)`)
	for _, command := range []string{"setup", "switch", "pair"} {
		_, stderr, code := runHub(t, home, command, "-h")
		names := flagLine.FindAllStringSubmatch(stderr, -1)
		if code != 0 || len(names) == 0 {
			t.Fatalf("%s -h: exit %d, no flags in %q", command, code, stderr)
		}
		for _, m := range names {
			flag := regexp.MustCompile(`--` + regexp.QuoteMeta(m[1]) + `\b`)
			if !flag.MatchString(usage) {
				t.Errorf("persona-hub's usage lacks %s --%s", command, m[1])
			}
			if !flag.MatchString(hubcli.Usage) {
				t.Errorf("hubcli.Usage lacks hub %s --%s", command, m[1])
			}
		}
	}
}
