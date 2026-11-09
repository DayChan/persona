// Command persona-node is the device's program: the node and the commands a computer runs for its Agents (hook, mcp,
// ask, notify, launch, …), without the hub or the manager (internal/nodecmd; personad runs the same commands).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/nwverse/persona/server/internal/nodecmd"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/update"
)

// What the build wrote in (-X), as personad's: the version, and the channel and build number `version -v` adds
// (nodeupdate and the publisher check a node build by them).
var (
	version = "dev"
	channel = ""
	build   = ""
)

const usage = `usage:
` + nodecmd.LaunchUsage + `
` + nodecmd.NodeUsage + `
` + nodecmd.Usage + `
` + nodecmd.SysmonUsage + `
  personad version [-v]     the version; -v adds the channel and build number`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, nodecmd.Named(usage))
		os.Exit(2)
	}
	if nodecmd.IsHelpArg(os.Args[1]) || os.Args[1] == "help" {
		fmt.Println(nodecmd.Named(usage))
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Println(version)
		if len(os.Args) > 2 && os.Args[2] == "-v" {
			b := update.Make(version, channel, build, "")
			fmt.Printf("channel %s, build %d\n", b.Channel, b.Number)
		}
		return
	}
	handled, err := nodecmd.Run(ctx, stop, os.Args[1:], nodecmd.Build{Version: version, Program: nodeproto.ProgramPersonaNode})
	if !handled {
		fmt.Fprintln(os.Stderr, nodecmd.Named(usage))
		// Say where the commands it leaves out are rather than only listing its own.
		where := "the hub's and the manager's commands are personad's"
		switch os.Args[1] {
		case "hub":
			where = "a hub needs personad (the install command with --hub, on the one computer that runs the hub; docs/operations/release.md)"
		case "manager", "extension", "assistant":
			where = "a manager needs personad (the manager's install command from the app installs both; docs/assistant/managers.md)"
		}
		fmt.Fprintf(os.Stderr, "\npersona-node, the device's own program, has no %q command; %s.\n", os.Args[1], where)
		os.Exit(2)
	}
	var ue nodecmd.UsageError
	if errors.As(err, &ue) {
		fmt.Fprintln(os.Stderr, string(ue))
		os.Exit(2)
	}
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(0)
	}
	if err != nil {
		log.Fatal(err)
	}
}
