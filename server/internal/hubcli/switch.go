package hubcli

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/hub"
	"github.com/nwverse/persona/server/internal/hubedge"
)

// exitNoEdge: nothing to switch on (no edge answers, or one older than this binary's protocol): the caller restarts
// the hub's service instead (scripts/deploy/hub-systemd.sh).
const exitNoEdge = 3

// runHubSwitch is `personad hub switch`: the running edge starts a binary next to the hub serving and switches to it
// once it is up (docs/operations/hub-restart.md "Blue-green switch"). One line per phase, then one with the outcome.
func runHubSwitch(args []string) error {
	fs := flag.NewFlagSet("hub switch", flag.ContinueOnError)
	binary := fs.String("binary", "", "the binary to switch to (default: the program the hub runs now; with nothing serving, personad)")
	prev := fs.String("prev", "", "the binary to go back to if the new one fails while taking over")
	status := fs.Bool("status", false, "only say what serves now")
	within := fs.Duration("timeout", 8*time.Minute, "give up waiting after this long (the edge finishes the switch by itself)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("hub switch takes no positional arguments")
	}
	if *status { // it only says what serves now: a switch's flags with it would be dropped without a word
		var err error
		fs.Visit(func(f *flag.Flag) {
			if f.Name != "status" && err == nil {
				err = fmt.Errorf("hub switch --status only says what serves now: it takes no --%s", f.Name)
			}
		})
		if err != nil {
			return err
		}
	}
	st, err := hubedge.Status(hub.ControlPath())
	if err != nil {
		fmt.Printf("hub switch: no edge here (%v)\n", err)
		os.Exit(exitNoEdge)
	}
	if *status {
		fmt.Printf("edge %s (protocol %d); slot %s serving %s, build %d, pid %d (%s)\n", st.Edge, st.Proto, st.Slot, st.Version, st.Build, st.PID, st.Binary)
		return nil
	}
	if st.Proto < hubedge.Proto {
		fmt.Printf("hub switch: the edge speaks protocol %d, this binary %d: restart the hub's service instead\n", st.Proto, hubedge.Proto)
		os.Exit(exitNoEdge)
	}
	if *binary == "" {
		p, err := slotProgram(st.Binary)
		if err != nil {
			return err
		}
		*binary = p
	}
	for _, p := range []*string{binary, prev} {
		if *p != "" {
			abs, err := filepath.Abs(*p)
			if err != nil {
				return err
			}
			*p = abs
		}
	}
	done, err := hubedge.Switch(hub.ControlPath(), *binary, *prev, *within, func(phase string) { fmt.Printf("hub switch: %s\n", phase) })
	if err != nil {
		return fmt.Errorf("hub switch: %w (serving: %s, build %d)", err, done.Version, done.Build)
	}
	fmt.Printf("hub switch: done, serving %s (build %d)\n", done.Version, done.Build)
	return nil
}

// slotProgram is the program the edge's active slot runs, what `hub switch` goes on with unless given another: a
// switch never moves the hub from personad to persona-hub or back (docs/operations/hub-restart.md "Blue-green
// switch"). A slot that went back to <program>.prev after a switch that failed runs that program. With nothing serving
// it is the server binary. The edge refuses any switch while it backs off a failing slot, so this only reaches a switch
// it takes once a slot is up again; on a hub that runs persona-hub that moves it to personad until its service
// restarts, so give --binary there.
func slotProgram(slot string) (string, error) {
	if slot == "" {
		return hub.ServerBinary()
	}
	if p, ok := strings.CutSuffix(slot, ".prev"); ok {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return slot, nil
}
