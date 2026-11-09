// Package nodecmd is the device's command line: what a computer runs for its node and its Agents (`node --stdio`,
// `node --attach`, `hook`, `mcp`, `ask`, `notify`, `launch`, …). The device's own program `persona-node` is this alone;
// `personad` dispatches the same commands here and adds the hub's and the manager's. It must
// not link the hub, the manager or the database (cmd/persona-node/main_test.go guards that).
package nodecmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"

	"github.com/nwverse/persona/server/internal/mcpserver"
	"github.com/nwverse/persona/server/internal/node"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/nodeupdate"
	"github.com/nwverse/persona/server/internal/office"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/scriptcli"
	"github.com/nwverse/persona/server/internal/toolcli"
)

// Build is what the program was built as (its -X flags).
type Build struct {
	Version string
	Program string // nodeproto.ProgramPersonad or ProgramPersonaNode: what node.info says the node runs as
}

// Usage is the device commands' part of the usage text (the node part is NodeUsage).
const Usage = `  personad hook             Claude Code hook entry (reads hook JSON on stdin)
  personad mcp              Claude Code MCP server (stdio)
  personad ask "question" [--options a,b,c] [--timeout 10m] [--require-remote]
                           block until the question is answered on the phone; exit 0 allowed / option chosen (printed),
                           1 denied, 2 usage, 3 timeout, 4 phone unreachable (docs/integrations/script-cli.md)
  personad notify "title" [body] [--silent] [--error] [--strict]
                           one notification to the phone; exit 0 whatever happens unless --strict (docs/integrations/script-cli.md)
  personad tool <tool> [<json args> | -] | --cli <connector> [--format json|text] <words…>
                           a connector's tools and command line from the assistant's shell, through the hub
                           (docs/protocol/connector-protocol.md "Command line"; personad tool --help)
  personad office docx|pptx|xlsx|check …
                           write a Word document or a deck from Markdown, a workbook from JSON / CSV, or say what is in
                           one (the office skill): personad office --help
  personad codex-start <file>
                           run the Codex launcher in file (written by the node, removed once read) with the first
                           message as its last argument, no shell in between (docs/integrations/codex.md「从手机新建」)`

// SysmonUsage is `sysmon`'s line in the usage text.
const SysmonUsage = `  personad sysmon [n]       print this machine's system info and 1 + n metric samples (n default 3, at most 8640,
                           every 10 s), with what they cost`

// LaunchUsage is `launch`'s line in the usage text.
const LaunchUsage = `  personad launch <name> …   run claude with a launcher kept in 设置 › 模型 (from the local node)`

// Run runs args (args[0] is the command) when it is a device command, and says whether it was. stop undoes the caller's
// signal handling (launch execs claude and leaves signals as they came). A command that has an exit code of its own
// exits here; err is for the caller to report (UsageError: print it and exit 2).
func Run(ctx context.Context, stop func(), args []string, b Build) (handled bool, err error) {
	if len(args) == 0 {
		return false, nil
	}
	rest := args[1:]
	switch args[0] {
	case "node":
		return true, runNode(ctx, rest, b)
	case "hook":
		nodeclient.RunHook(ctx, rest, os.Stdin, os.Stdout)
	case "launch": // a launcher kept in Settings › Models: its settings from the local node, then claude (docs/integrations/launchers.md)
		if len(rest) < 1 {
			return true, UsageError("usage: " + CommandName() + " launch <launcher> [claude args…]")
		}
		return true, runLaunch(stop, rest[0], rest[1:]) // launch.go: bounded wait for the node, Ctrl-C ends it
	case "codex-start": // a Codex launcher with the phone's first message, read from a file (node/codex_start.go)
		return true, node.CodexStart(rest)
	case "tab-start": // a command a runtime gave to resume its session in a new tab (`pi --session …`, node/pi.go)
		return true, node.CodexStart(rest)
	case "mcp":
		return true, mcpserver.Serve(ctx, b.Version, os.Stdin, os.Stdout)
	case "tool": // a connector's tools and command line from the assistant's shell (internal/toolcli)
		os.Exit(toolcli.Main(ctx, rest, os.Stdin, os.Stdout, os.Stderr))
	case "ask": // a script asks the phone (internal/scriptcli)
		os.Exit(scriptcli.Ask(ctx, rest, os.Stdin, os.Stdout, os.Stderr))
	case "notify":
		os.Exit(scriptcli.Notify(ctx, rest, os.Stdout, os.Stderr))
	case "office":
		os.Exit(office.Main(rest, os.Stdout, os.Stderr))
	case "sysmon":
		n, err := sysmonCount(rest)
		if err != nil {
			return true, UsageError(err.Error())
		}
		runSysmon(ctx, n)
	case "node-update-check":
		return true, json.NewEncoder(os.Stdout).Encode(nodeupdate.Check{Version: b.Version, OS: runtime.GOOS, Arch: runtime.GOARCH, Program: b.Program})
	case "node-update-watch":
		return true, runNodeUpdateWatch(ctx, rest)
	default:
		return false, nil
	}
	return true, nil
}

// runNode is `node …`: the node protocol on stdin / stdout (relayed to a resident node when one runs), the resident
// node, tailcat, doctor, reset-uid.
func runNode(ctx context.Context, args []string, b Build) error {
	if len(args) == 1 && IsHelpArg(args[0]) {
		os.Stdout.WriteString(Named(NodeUsage) + "\n")
		return nil
	}
	if len(args) > 0 {
		switch args[0] {
		case "--attach":
			osproc.SealInherited() // as the node on stdin / stdout does, below
			return runResident(ctx, b)
		case "tailcat":
			return runNodeTailcat(ctx, args[1:])
		case "doctor":
			failed, err := node.DoctorCommand(ctx, args[1:], os.Stdout)
			if err == nil && failed {
				os.Exit(1)
			}
			return err
		case "reset-uid":
			if err := noExtraArgs("reset-uid", args[1:]); err != nil {
				exitNodeUsage(err)
			}
			return resetMachineUID()
		}
	}
	if err := checkNodeStdioArgs(args); err != nil {
		exitNodeUsage(err)
	}
	// What a parent left inheritable (an older hub's listening socket, through its manager) stops here, not in every
	// agent and program the node starts. Here, not in main: other commands of this binary hand descriptors on by exec.
	osproc.SealInherited()
	relayed, err := node.Relay(ctx, os.Stdin, os.Stdout)
	if relayed {
		if errors.Is(err, io.EOF) {
			err = nil
		}
		return err
	}
	n, err := node.New(b.Version)
	if err != nil {
		return err
	}
	n.Program = b.Program
	if err = n.ServeStdio(ctx); errors.Is(err, io.EOF) {
		err = nil
	}
	return err
}
