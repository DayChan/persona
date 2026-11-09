package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// A start types its command into a fresh pane a moment after the tab is made, often before the shell's line editor
// has taken the terminal: until then the tty is in canonical mode, whose line limit is 1024 bytes on macOS
// (MAX_CANON), and herdr refuses a longer line up front (pane_send_failed). The whole command — the unset of the
// assistant's environment, PERSONA_HOME, HERDR_AGENT, a managed launcher's flags, the first message — is often longer
// (#2022). So in a herdr pane whose shell can source a file, the node writes the command to a file only the user can
// read and types `. <file>`: the shell runs the same command as if typed (a launcher that is an alias or a function
// still works), and the file removes itself first. Other hosts get the command typed as before.

// terminalNotReady is the refusal for a start whose pane would not take the typed command: nothing ran, the tab is
// closed, a retry may work.
const terminalNotReady = proto.CodeTerminalNotReady

// startScriptPrefix names the files a start writes in the node's directory.
const startScriptPrefix = "agent-start-"

// startScript is the start file at path that runs cmd: it removes itself, then runs cmd.
func startScript(path, cmd string) string {
	return "command rm -f -- " + shellQuote(path) + "\n" + cmd + "\n"
}

// startLine is what is typed into a pane whose shell is shells to run the start file at path: `source` in fish, `.`
// elsewhere. A start's anchor (its session id, which startProbe finds the echo of the typed command by) follows as a
// word of a no-op: the file's path does not hold it, and the shell's not-found lines for the file's command start with
// that path (shellMisses skips the lines that hold the anchor).
func startLine(path, anchor string, shells ...string) string {
	line := ". " + shellQuote(path)
	for _, s := range shells {
		if strings.TrimPrefix(filepath.Base(s), "-") == "fish" {
			line = "source " + shellQuote(path)
		}
	}
	if anchor != "" {
		line += " && true " + shellQuote(anchor)
	}
	return line
}

// writeStartScript writes the start file for cmd in dir (0600; dir made 0700 when missing) and returns its path.
func writeStartScript(dir, cmd string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, startScriptPrefix+"*.sh")
	if err != nil {
		return "", err
	}
	_, err = f.WriteString(startScript(f.Name(), cmd))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// runAgentCommand types cmd, built with agentCommand for shells, into the new pane created: through a start file in
// the node's directory, for a herdr pane whose shells source one (takesAssign), with anchor on the typed line
// (startLine); else as it is. When the pane does not take it, the tab is
// closed (closeNewTab) and pane_send_failed comes back as terminalNotReady. done removes the file if the shell has
// not read it yet; call it once the start is over.
func (n *Node) runAgentCommand(ctx context.Context, launch host.Launcher, created *host.Created, ws, anchor, cmd string, shells ...string) (done func(), err error) {
	done = func() {}
	line := cmd
	if _, isHerdr := launch.(*herdrHost); isHerdr && n.dir != "" && takesAssign(shells...) {
		path, err := writeStartScript(n.dir, cmd)
		if err != nil {
			n.closeNewTab(ctx, launch, created, ws)
			return done, err
		}
		done = func() { _ = os.Remove(path) }
		line = startLine(path, anchor, shells...)
	}
	if err := launch.Run(ctx, created.Pane, line); err != nil {
		done()
		n.closeNewTab(ctx, launch, created, ws)
		return func() {}, startRunError(err)
	}
	return done, nil
}

// startRunError is the error of a start whose command the pane did not take: terminalNotReady for herdr's
// pane_send_failed, retryable with nothing delivered.
func startRunError(err error) error {
	var he *herdr.Error
	if (errors.As(err, &he) && he.Code == "pane_send_failed") || strings.Contains(err.Error(), "pane_send_failed") {
		return refusal(terminalNotReady, "the computer's terminal did not take the start command: "+err.Error(),
			map[string]any{"retryable": true})
	}
	return fmt.Errorf("run launcher: %w", err)
}

// closeNewTab closes the tab a start made (newTab) once the start has failed: left open it is an empty shell, and a
// retry opens another. A pane the user picked ("Run Agent Here") stays.
func (n *Node) closeNewTab(ctx context.Context, launch host.Launcher, created *host.Created, ws string) {
	org, ok := launch.(host.Organizer)
	if !ok || adoptedPane(ctx) != nil {
		return
	}
	space := ws
	if space == "" {
		space = created.SpaceID
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	_ = org.Close(cctx, created.Pane, space, created.SlotID)
	cancel()
}
