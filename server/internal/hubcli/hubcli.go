// Package hubcli is the hub's command line: `persona-hub <command>` (cmd/persona-hub) and `personad hub <command>`
// (cmd/personad, the compatibility entry) run the same code. It links only the hub's side (docs/server/server.md
// "Programs"): the manager's commands are internal/managercli's.
package hubcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/hub"
	"github.com/nwverse/persona/server/internal/update"
)

// Usage is the hub's commands, as `personad hub …` (clitext.Named puts the program's name in).
const Usage = `  personad hub [run]        run the hub (config ~/.persona/hub.json); behind its edge on Linux and macOS, which
                           holds the ports and switches to a new binary with no restart (docs/operations/hub-restart.md「蓝绿切换」)
  personad hub switch [--binary <path>] [--prev <path>] [--timeout 8m] | --status
                           have the running edge start <path> (default: the program the hub runs now, personad or
                           persona-hub) and switch to it once it is up, waiting at most --timeout; --status only says what
                           serves now; exit 0 switched, 1 it did not (the hub serves on), 3 no edge here (restart the service instead)
  personad hub setup [--yes] [--listen :18660] [--tls :18662] [--outside] [--no-service | --manager-service] [--json]
                                           set this machine up as the hub: hub.json, database, service, pairing (docs/assistant/onboarding.md);
                                           --manager-service: an existing hub's built-in manager becomes a service of its own (a new one has that);
                                           PERSONA_SPLIT_PROGRAMS=0 keeps its services on personad (not persona-hub, persona-manager)
                                           and stays in their units until setup runs again without it (Linux; macOS stays on personad)
  personad hub pair [--legacy | --android] print a pairing QR code and optional Android download link
  personad hub pair --machine <id> [--add [--name <name>]]
                                           print a pairing link for that link machine's persona-link (docs/operations/link.md);
                                           --add adds it to hub.json first when it is not there
  personad hub pair --peer  print a code another hub pairs with this one by (hub mesh, docs/server/mesh.md)
  personad hub peer add <code>|-   pair this hub with the hub whose code it is
  personad hub peer list           the paired hubs and the ones found on the LAN
  personad hub peer remove <hub_id>
                           unpair a hub (its credential is revoked on both sides)
  personad hub peer center [<hub_id>] [--confirm]   make that hub (this one by default) the group's center
  personad hub doctor       check the hub's configuration, network and certificates (read only)
  personad hub admin …      the admin UI on this machine: access <scope>, reset-password, setup-code (docs/operations/admin.md)
  personad hub manager-mode [child|service]
                           the built-in manager: started by the hub (child, the default) or a service of its own;
                           show it, or set it in hub.json (taken at the hub's next start: personad hub switch)
  personad hub manager-code a one-time code that binds the built-in manager's own service to this hub
                           (service mode; persona-manager join --builtin, docs/assistant/managers.md)
  personad hub assistant-token  print a new bus token for an assistant run elsewhere (hub.json assistant.external)`

// Word is how the hub's commands are typed after the program's name in what they print: "hub " for personad
// (`personad hub run`), "" for persona-hub (`persona-hub run`), which sets it.
var Word = "hub "

// Help is the usage text -h prints, and an unknown command after saying so, printed as it is: persona-hub sets its
// own, written with its name (so what it says about personad stays personad). Empty: the hub's commands as
// `personad hub …`, with the program's name in (helpText).
var Help string

func helpText() string {
	if Help != "" {
		return Help
	}
	return clitext.Named("usage:\n" + Usage)
}

// Hub runs `hub <args…>`: args[0] is the command (run when there is none). Setup that stopped exits 1 (the step that
// stopped said why).
func Hub(ctx context.Context, build update.Build, args []string) error {
	sub := "run"
	if len(args) > 0 {
		sub = args[0]
	}
	rest := []string{}
	if len(args) > 1 {
		rest = args[1:]
	}
	if clitext.IsHelpArg(sub) || sub == "help" { // asked for: the usage on stdout and exit 0, as `persona-hub hub -h`
		fmt.Println(helpText())
		return nil
	}
	switch sub {
	case "run":
		if len(rest) > 0 { // a flag such as --listen would otherwise be dropped and a hub started on hub.json's port
			return clitext.UsageError("usage: " + clitext.CommandName() + " " + Word + "[run]")
		}
		return hub.Serve(ctx, build)
	case "slot": // the hub as its edge runs it (hub_edge.go): not for people
		if len(rest) > 0 {
			return noArguments("slot")
		}
		return hub.RunSlot(ctx, build)
	case "switch":
		return runHubSwitch(rest)
	case "doctor":
		if len(rest) != 0 {
			return noArguments("doctor")
		}
		return hub.Doctor(ctx, os.Stdout)
	case "pair":
		return pair(ctx, rest)
	case "setup": // docs/assistant/onboarding.md 4.1
		err := hub.SetupCommand(ctx, rest, os.Stdout, os.Stderr)
		if errors.Is(err, hub.ErrSetupStopped) {
			os.Exit(1) // the step that stopped said why
		}
		return err
	case "admin":
		return hub.AdminCommand(ctx, rest, os.Stdin, os.Stdout, os.Stderr)
	case "peer":
		return hub.PeerCommand(ctx, rest, os.Stdin, os.Stdout)
	case "manager-mode": // the built-in manager: started by the hub (child) or a service of its own (service)
		return hub.ManagerMode(rest, os.Stdout)
	case "manager-code": // a one-time code that binds the built-in manager's own service (persona-manager join --builtin)
		if len(rest) != 0 {
			return noArguments("manager-code")
		}
		return hub.ManagerCode(ctx, os.Stdout)
	case "assistant-token": // a token for an assistant run elsewhere (hub.json assistant.external, docs/assistant/assistant.md)
		if len(rest) != 0 {
			return noArguments("assistant-token")
		}
		return hub.AssistantToken(ctx, os.Stdout)
	}
	return clitext.UsageError(fmt.Sprintf("unknown %scommand %q\n%s", Word, sub, helpText()))
}

// noArguments is the usage mistake of arguments after a command that takes none.
func noArguments(command string) error {
	return clitext.UsageError(clitext.CommandName() + " " + Word + command + " takes no arguments")
}

// UpdateWatch is `update-watch <pending.json>`: the old binary's watchdog over the hub's own update.
func UpdateWatch(ctx context.Context, args []string) error { return runUpdateWatch(ctx, args) }

// pair is `hub pair`: an argument mistake is a clitext.UsageError (exit 2), found before hub.json is read; ctx (the
// command's, Ctrl-C) ends the wait for the hub.
func pair(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet(Word+"pair", flag.ContinueOnError)
	legacy := flags.Bool("legacy", false, "使用主 token 打印旧格式二维码")
	android := flags.Bool("android", false, "同时打印 Android 安装包的临时下载链接")
	machine := flags.String("machine", "", "给 hub.json 里这台 kind 为 link 的电脑的 persona-link 配对")
	add := flags.Bool("add", false, "和 --machine 一起用：hub.json 里没有这台时先添加（电脑来连，直接是已确认的）")
	name := flags.String("name", "", "和 --add 一起用：新电脑的名字（可以是中文，默认同 id）")
	peer := flags.Bool("peer", false, "生成和另一台 hub 互联的互联码（docs/server/mesh.md）")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return clitext.UsageError(pairUsage()) // flag printed what was wrong and the flags
	}
	set := map[string]bool{}
	flags.Visit(func(f *flag.Flag) { set[f.Name] = true })
	switch {
	case flags.NArg() != 0:
		return clitext.UsageError(fmt.Sprintf("%s takes no positional arguments (got %q)\n%s", pairCommand(), flags.Arg(0), pairUsage()))
	case *peer && (set["machine"] || *legacy || *android || *add || set["name"]):
		return clitext.UsageError("--peer cannot be combined with --machine, --legacy, --android, --add or --name\n" + pairUsage())
	case *peer:
		return hub.PairPeer(ctx)
	case set["machine"] && *machine == "":
		return clitext.UsageError("--machine needs a machine id\n" + pairUsage())
	case *machine != "" && (*legacy || *android):
		return clitext.UsageError("--machine cannot be combined with --legacy or --android\n" + pairUsage())
	case (*add || *name != "") && *machine == "":
		return clitext.UsageError("--add and --name go with --machine\n" + pairUsage())
	case *name != "" && !*add:
		return clitext.UsageError("--name goes with --add\n" + pairUsage())
	case *legacy && *android:
		return clitext.UsageError("--android needs a temporary token, so it cannot be combined with --legacy\n" + pairUsage())
	case *machine != "":
		return hub.PairMachine(ctx, *machine, *add, *name)
	}
	return hub.Pair(ctx, *legacy, *android)
}

func pairCommand() string { return commandName() + " " + Word + "pair" }

// pairUsage is `hub pair`'s three forms.
func pairUsage() string {
	c := pairCommand()
	return "usage: " + c + " [--legacy | --android]\n" +
		"       " + c + " --machine <id> [--add [--name <name>]]\n" +
		"       " + c + " --peer"
}

func usageError(s string) error { return clitext.UsageError(s) }

func commandName() string { return clitext.CommandName() }
