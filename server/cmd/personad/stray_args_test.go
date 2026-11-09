package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/hubcli"
)

// TestStrayArgsRefused runs main in a child process (this test binary, re-executed): arguments after hub run, hub
// slot, hub tailcat-key, hub doctor, hub manager-code, hub assistant-token and relay's config path, and an unknown hub
// command, are refused with exit 2 and the usage (no log timestamp), before anything starts or is written (no
// hub.json, no tailcat key, no listener).
func TestStrayArgsRefused(t *testing.T) {
	if args := os.Getenv("PERSONAD_TEST_MAIN_ARGS"); args != "" {
		os.Args = append([]string{"personad"}, strings.Split(args, "\x1f")...)
		main()
		os.Exit(0)
	}
	home := t.TempDir()
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"hub", "run", "--listen", ":1"}, "usage: personad hub [run]"},
		{[]string{"hub", "run", "x"}, "usage: personad hub [run]"},
		{[]string{"hub", "slot", "x"}, "personad hub slot takes no arguments"},
		{[]string{"hub", "tailcat-key", "x"}, "personad hub tailcat-key takes no arguments"},
		{[]string{"hub", "doctor", "x"}, "personad hub doctor takes no arguments"},
		{[]string{"hub", "manager-code", "x"}, "personad hub manager-code takes no arguments"},
		{[]string{"hub", "assistant-token", "x"}, "personad hub assistant-token takes no arguments"},
		{[]string{"hub", "bogus"}, "unknown hub command \"bogus\"\nusage:\n" + named(hubcli.Usage)},
		{[]string{"relay", "a", "b"}, "usage: personad relay [config]"},
	} {
		// A regression would start a server: the deadline keeps it from hanging the test.
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStrayArgsRefused$")
		cmd.Env = append(os.Environ(), "PERSONAD_TEST_MAIN_ARGS="+strings.Join(tc.args, "\x1f"), "HOME="+home,
			"PERSONA_HOME="+filepath.Join(home, ".persona"))
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		cancel()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != 2 || stdout.String() != "" || strings.TrimSpace(stderr.String()) != tc.want {
			t.Errorf("%q: exit %d, stdout %q, stderr %q; want exit 2 and %q", tc.args, code, stdout.String(), stderr.String(), tc.want)
		}
	}
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		if e.Name() != ".cache" && e.Name() != ".config" {
			t.Errorf("a refused command wrote %s in HOME", e.Name())
		}
	}
}
