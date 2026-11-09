package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/hubcli"
)

// TestHelpExitsZero runs main in a child process (this test binary, re-executed): -h, --help and help print the usage
// on stdout and exit 0, as persona-hub, persona-manager and persona-link do, and so do `hub -h` (the hub's commands,
// as `persona-hub hub -h`) and `hub peer -h`, without writing anything in HOME; the usage names every command, tool too.
func TestHelpExitsZero(t *testing.T) {
	if args := os.Getenv("PERSONAD_TEST_TOP_HELP_ARGS"); args != "" {
		os.Args = append([]string{"personad"}, strings.Split(args, "\x1f")...)
		main()
		os.Exit(0)
	}
	home := t.TempDir()
	hubHelp, peerHelp := named("usage:\n"+hubcli.Usage)+"\n", "usage: personad hub peer add <code>|- | list | remove <hub_id> | center [<hub_id>] [--confirm]\n"
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-h"}, named(usage) + "\n"},
		{[]string{"--help"}, named(usage) + "\n"},
		{[]string{"help"}, named(usage) + "\n"},
		{[]string{"hub", "-h"}, hubHelp},
		{[]string{"hub", "--help"}, hubHelp},
		{[]string{"hub", "help"}, hubHelp},
		{[]string{"hub", "peer", "-h"}, peerHelp},
		{[]string{"hub", "peer", "--help"}, peerHelp},
		{[]string{"hub", "peer", "help"}, peerHelp},
	} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelpExitsZero$")
		cmd.Env = append(os.Environ(), "PERSONAD_TEST_TOP_HELP_ARGS="+strings.Join(tc.args, "\x1f"), "HOME="+home,
			"PERSONA_HOME="+filepath.Join(home, ".persona"))
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != 0 || stdout.String() != tc.want || stderr.String() != "" {
			t.Errorf("%q: exit %d, stdout %.200q, stderr %.200q; want exit 0 and %.200q on stdout", tc.args, code, stdout.String(), stderr.String(), tc.want)
		}
	}
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		if e.Name() != ".cache" && e.Name() != ".config" {
			t.Errorf("help wrote %s in HOME", e.Name())
		}
	}
	for _, want := range []string{"\n  personad tool ", "\n" + hubcli.Usage + "\n"} {
		if !strings.Contains(usage, want) {
			t.Errorf("the usage lacks %q", want)
		}
	}
}

// TestSubcommandHelpExitsZero runs main in a child process (this test binary, re-executed): -h on a subcommand's
// flags prints its help and exits 0, without a "flag: help requested" line; a wrong flag still fails.
func TestSubcommandHelpExitsZero(t *testing.T) {
	if args := os.Getenv("PERSONAD_TEST_HELP_ARGS"); args != "" {
		os.Args = append([]string{"personad"}, strings.Split(args, "\x1f")...)
		main()
		os.Exit(0)
	}
	home := t.TempDir()
	run := func(args ...string) (string, int) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSubcommandHelpExitsZero$")
		cmd.Env = append(os.Environ(), "PERSONAD_TEST_HELP_ARGS="+strings.Join(args, "\x1f"), "HOME="+home,
			"PERSONA_HOME="+filepath.Join(home, ".persona"))
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	for _, args := range [][]string{{"node", "tailcat", "init", "-h"}, {"hub", "switch", "-h"}, {"hub", "pair", "--help"},
		{"release", "manifest", "-h"}, {"release", "check-next", "x", "-h"}, {"release", "check-next", "-h"}, {"release", "check-next", "--help"}, {"hub", "setup", "-h"}, {"hub", "admin", "access", "-h"}} {
		out, code := run(args...)
		if code != 0 || !strings.Contains(out, "Usage of") || strings.Contains(out, "help requested") {
			t.Errorf("%q: exit %d, output %q", args, code, out)
		}
	}
	if out, code := run("release", "manifest", "--bogus"); code == 0 || !strings.Contains(out, "bogus") {
		t.Errorf("a wrong flag: exit %d, output %q", code, out)
	}
}
