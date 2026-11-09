package hubcli

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/hub"
	"github.com/nwverse/persona/server/internal/hubedge"
)

// fakeEdge answers on the hub's control socket as an edge whose active slot runs slot ("": nothing serves), and sends
// the binary each switch asks for.
func fakeEdge(t *testing.T, slot string) <-chan string {
	t.Helper()
	root, err := os.MkdirTemp("", "psw") // short: a unix socket's path has a length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	t.Setenv("PERSONA_HOME", root)
	if err := os.MkdirAll(filepath.Dir(hub.ControlPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", hub.ControlPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	asked := make(chan string, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var m hubedge.Msg
			if json.NewDecoder(c).Decode(&m) == nil {
				r := hubedge.Msg{T: hubedge.MsgStatus, Proto: hubedge.Proto, Version: "v1", Build: 1, Slot: "a", Binary: slot}
				if m.T == hubedge.MsgSwitch {
					asked <- m.Binary
					r = hubedge.Msg{T: hubedge.MsgDone, OK: true, Version: "v2", Build: 2}
				}
				_ = json.NewEncoder(c).Encode(r)
			}
			_ = c.Close()
		}
	}()
	return asked
}

// Without --binary, a switch goes on with the program the edge's active slot runs, whichever program asks for it
// (`persona-hub switch` or `personad hub switch`; here the test binary, neither): a hub on personad stays on personad,
// one on persona-hub stays on persona-hub (docs/operations/hub-restart.md "Blue-green switch").
func TestSwitchKeepsTheProgramTheHubRuns(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"personad", "personad.prev", "persona-hub", "persona-hub.prev"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	personad, split := filepath.Join(bin, "personad"), filepath.Join(bin, "persona-hub")
	for _, c := range []struct {
		name, slot, want string
		args             []string
	}{
		{name: "the slot runs personad", slot: personad, want: personad},
		{name: "the slot runs persona-hub", slot: split, want: split},
		// a deploy's switch that failed went back to the old build of the program
		{name: "the slot went back to personad.prev", slot: personad + ".prev", want: personad},
		{name: "the slot went back to persona-hub.prev", slot: split + ".prev", want: split},
		{name: "--binary says which", slot: personad, want: split, args: []string{"--binary", split}},
	} {
		t.Run(c.name, func(t *testing.T) {
			asked := fakeEdge(t, c.slot)
			if err := runHubSwitch(c.args); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-asked:
				if got != c.want {
					t.Fatalf("switched to %s, want %s", got, c.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("no switch asked for")
			}
		})
	}
}

// With nothing serving there is no program to go on with: the switch names the server binary, this one when it is not
// persona-hub. A real edge only takes such a switch once a slot is up again; while it backs off it refuses any.
func TestSwitchWithNothingServing(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(self)
	if err != nil {
		t.Fatal(err)
	}
	asked := fakeEdge(t, "")
	if err := runHubSwitch(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-asked:
		if got != want {
			t.Fatalf("switched to %s, want %s", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no switch asked for")
	}
}

// --status only says what serves now: with --binary, --prev or --timeout it refuses (exit 1 through main, as the other
// mistakes of hub switch; hub-systemd.sh reads any other code as "no edge") instead of printing the status and
// dropping them, before it asks the edge anything.
func TestSwitchStatusTakesNoOtherFlags(t *testing.T) {
	asked := fakeEdge(t, "/bin/personad")
	for _, args := range [][]string{{"--status", "--binary", "x"}, {"--prev", "x", "--status"}, {"--status", "--timeout", "1m"}} {
		if err := runHubSwitch(args); err == nil || !strings.Contains(err.Error(), "--status") {
			t.Errorf("%q: %v, want it refused", args, err)
		}
	}
	if err := runHubSwitch([]string{"--status"}); err != nil {
		t.Fatalf("--status: %v", err)
	}
	select {
	case b := <-asked:
		t.Fatalf("a status asked the edge to switch to %s", b)
	default:
	}
}
