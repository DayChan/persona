package manager

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/update"
)

// The usage names only commands there are, under each name the program runs as: its own after "manager" (personad
// manager …, persona-manager manager …), and the hub's by the hub's program, persona-hub, which clitext.Named leaves
// as it is. Written as "personad hub …", Named made them "persona-manager hub …", which persona-manager does not have.
func TestUsageNamesCommandsThatExist(t *testing.T) {
	isolate(t)
	hubCommands := hubDispatch(t)
	arg0 := os.Args[0]
	t.Cleanup(func() { os.Args[0] = arg0 })
	for _, prog := range []string{"persona-manager", "personad"} {
		os.Args[0] = "/opt/persona/bin/" + prog
		words := strings.Fields(clitext.Named(Usage))
		for i := 0; i+1 < len(words); i++ {
			next := strings.Trim(words[i+1], "()[];,")
			switch strings.Trim(words[i], "()[];,") {
			case prog: // its own (<prog> manager <command>), or personad's hub command (personad hub <command>)
				switch {
				case next == "manager" && i+2 < len(words):
					cmd := words[i+2]
					if err := Main(context.Background(), update.Build{}, []string{cmd, "-h"}, io.Discard); errors.Is(err, errUsage) {
						t.Errorf("as %s, the usage names %s manager %s, which Main does not know", prog, prog, cmd)
					}
				case prog == "personad" && next == "hub" && i+2 < len(words) && hubCommands[strings.Trim(words[i+2], "()[];,")]:
				default:
					t.Errorf("as %s, the usage names %q, which is no command of %s", prog, prog+" "+next, prog)
				}
			case "persona-hub":
				if !hubCommands[next] {
					t.Errorf("the usage names persona-hub %s, which the hub's command line does not dispatch", next)
				}
			}
		}
	}
}

// hubDispatch is the commands the hub's command line dispatches (persona-hub <command>, personad hub <command>), read
// from internal/hubcli's source: the manager does not link the hub's code, not even in its tests.
func hubDispatch(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "hubcli", "hubcli.go"))
	if err != nil {
		t.Fatal(err)
	}
	commands := map[string]bool{}
	for _, line := range strings.Split(string(src), "\n") {
		list, ok := strings.CutPrefix(strings.TrimSpace(line), "case \"")
		if !ok {
			continue
		}
		list, _, _ = strings.Cut(list, ":")
		for _, c := range strings.Split(list, ",") {
			commands[strings.Trim(strings.TrimSpace(c), `"`)] = true
		}
	}
	if !commands["run"] || !commands["setup"] {
		t.Fatalf("read no hub commands from hubcli.go: %v", commands)
	}
	return commands
}
