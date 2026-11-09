// Command persona-hub is the hub's program: the hub, its edge and its commands, without the manager (the assistant,
// the extensions, the computers: persona-manager) or the device's node (persona-node). personad, the compatibility
// entry, runs the same commands as `personad hub …` (internal/hubcli; docs/server/server.md "Programs").
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

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/hubcli"
	"github.com/nwverse/persona/server/internal/update"
)

// What the build wrote in (-X), as personad's (the hub's update checks a program by `version -v`).
var (
	version   = "dev"
	channel   = ""
	build     = ""
	updateURL = ""
	commit    = ""
	image     = ""
)

func buildInfo() update.Build {
	b := update.Make(version, channel, build, updateURL)
	b.Commit, b.Image = commit, image
	return b
}

// usage: the hub's commands without "hub " (persona-hub hub <command> works too: what the edge and older scripts run).
const usage = `usage:
  persona-hub [run]         run the hub (config ~/.persona/hub.json); behind its edge on Linux and macOS
  persona-hub switch [--binary <path>] [--prev <path>] [--timeout 8m] | --status
                           have the running edge switch to another binary, or say what serves now (docs/operations/hub-restart.md)
  persona-hub setup [--yes] [--listen :18660] [--tls :18662] [--outside] [--no-service | --manager-service] [--json]
                           set this machine up as the hub (docs/assistant/onboarding.md); --manager-service: an existing
                           hub's built-in manager becomes a service of its own; PERSONA_SPLIT_PROGRAMS=0 keeps
                           its services on personad until setup runs again without it (Linux; macOS stays on personad)
  persona-hub pair [--legacy | --android] | --machine <id> [--add [--name <name>]] | --peer
                           pairing codes for phones, a computer's persona-link, another hub
  persona-hub peer add|list|remove|center …    linked hubs (docs/server/mesh.md)
  persona-hub doctor        check the hub's configuration, network and certificates (read only)
  persona-hub admin …       the admin UI on this machine (docs/operations/admin.md)
  persona-hub manager-mode [child|service]
                           the built-in manager: started by the hub (child) or a service of its own (service)
  persona-hub manager-code  a one-time code that binds the built-in manager's own service (service mode)
  persona-hub assistant-token
                           print a new bus token for an assistant run elsewhere (hub.json assistant.external)
  persona-hub version [-v]  the version; -v adds the channel and build number`

func main() {
	hubcli.Word, hubcli.Help = "", usage
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "hub" { // persona-hub hub <command>: personad's form
		args = args[1:]
	}
	if len(args) > 0 && (clitext.IsHelpArg(args[0]) || args[0] == "help") {
		fmt.Println(usage)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch {
	case len(args) > 0 && (args[0] == "version" || args[0] == "--version" || args[0] == "-v"):
		fmt.Println(version)
		if len(args) > 1 && args[1] == "-v" {
			b := buildInfo()
			fmt.Printf("channel %s, build %d\n", b.Channel, b.Number)
		}
	case len(args) > 0 && args[0] == "update-watch": // the old binary watching an update come up (hub/selfupdate.go)
		err = hubcli.UpdateWatch(ctx, args[1:])
	default:
		err = hubcli.Hub(ctx, buildInfo(), args)
	}
	finish(err)
}

// finish reports how a command ended: a usage mistake with exit 2, -h with 0, any other error fatally.
func finish(err error) {
	var ue clitext.UsageError
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
