package node

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/tmux"
)

// tmuxHost is a pane of a tmux server Persona did not open: a Claude Code the user started in tmux (#2562). Only what
// an approval needs — read the pane's screen and press keys in it. The pane is named by tmux itself ("%11"), as
// Claude Code wrote it down in sessions/<pid>.json ("tmux": "orc:@11.%11"). A pane id repeats across servers, so the
// server travels in the id too: "%11" for the default one, "<socket>|%11" for a server started with -L or -S. The
// layout is not read and no space, slot or agent comes from here: the session that runs there is listed by
// claudebg.go, on its own pid file.
type tmuxHost struct {
	c tmux.Client // the tmux to run: Bin, and the socket used when a pane's id names none (tests)
}

var (
	_ host.Host         = (*tmuxHost)(nil)
	_ host.ScreenDriver = (*tmuxHost)(nil)
)

func newTmuxHost() *tmuxHost { return &tmuxHost{} }

func (h *tmuxHost) Kind() string    { return host.Tmux }
func (h *tmuxHost) PaneEnv() string { return "TMUX_PANE" }

// CanDriveScreen: a tmux pane is read and typed into through the tmux command, which works off the pane's own server.
func (h *tmuxHost) CanDriveScreen() bool { return true }

func (h *tmuxHost) Version(ctx context.Context) string { return h.client("").Version(ctx) }

// client is the tmux command for the server a pane's id names, the default one when it names none.
func (h *tmuxHost) client(socket string) tmux.Client {
	if socket == "" {
		socket = h.c.Socket
	}
	return tmux.Client{Bin: h.c.Bin, Socket: socket}
}

// Snapshot lists nothing: tmux sessions the user opened are not Persona's spaces or slots, and the agents in them are
// listed by claudebg.go. It never fails: a machine without tmux simply has no panes this host can reach.
func (h *tmuxHost) Snapshot(context.Context) (*host.Layout, error) { return &host.Layout{}, nil }

// Agents lists nothing, for Snapshot's reason.
func (h *tmuxHost) Agents(context.Context) ([]host.Agent, error) { return nil, nil }

// Agent is none: this host detects no agents.
func (h *tmuxHost) Agent(context.Context, host.PaneRef) (*host.Agent, error) {
	return nil, fmt.Errorf("%w: tmux", host.ErrNoAgent)
}

func (h *tmuxHost) Read(ctx context.Context, pane host.PaneRef, o host.ReadOpts) (string, error) {
	socket, id := splitTmuxPane(pane.ID)
	text, err := h.client(socket).Capture(ctx, id, o.Lines, o.ANSI)
	return text, tmuxErr(err)
}

func (h *tmuxHost) SendText(ctx context.Context, pane host.PaneRef, text string) error {
	socket, id := splitTmuxPane(pane.ID)
	return tmuxErr(h.client(socket).SendText(ctx, id, text))
}

func (h *tmuxHost) SendKeys(ctx context.Context, pane host.PaneRef, keys ...string) error {
	socket, id := splitTmuxPane(pane.ID)
	return tmuxErr(h.client(socket).Keys(ctx, id, keys...))
}

func (h *tmuxHost) Prompt(ctx context.Context, pane host.PaneRef, text string) error {
	socket, id := splitTmuxPane(pane.ID)
	return tmuxErr(h.client(socket).Prompt(ctx, id, text))
}

// PaneProcesses is not read: a tmux pane's processes are not needed to read its screen or press its keys.
func (h *tmuxHost) PaneProcesses(context.Context, host.PaneRef) ([]int, error) { return nil, nil }

// tmuxErr marks a tmux that is gone as the host being gone: the pane with it.
func tmuxErr(err error) error {
	if errors.Is(err, tmux.ErrNoServer) {
		return host.Mark(err, host.ErrNotRunning)
	}
	return err
}

// tmuxPaneOf is the pane tmux names in Claude Code's own record ("orc:@11.%11", its session:window.pane) as this
// node's tmux pane ref ("tmux:%11"), "" when it names none. The pane id is unique within its server, so the session and
// window before it add nothing; the server does not repeat, so it is kept (socket, "" for the default one).
func tmuxPaneOf(target, socket string) string {
	id := target
	if i := strings.LastIndex(id, "."); i >= 0 {
		id = id[i+1:]
	}
	if !strings.HasPrefix(id, "%") || len(id) < 2 {
		return ""
	}
	if socket != "" {
		id = socket + "|" + id
	}
	return host.PaneRef{Kind: host.Tmux, ID: id}.String()
}

// splitTmuxPane splits a pane id into the socket of its server and the pane's own id ("%11"), as tmuxPaneOf formed it.
func splitTmuxPane(id string) (socket, pane string) {
	if i := strings.LastIndex(id, "|"); i >= 0 {
		return id[:i], id[i+1:]
	}
	return "", id
}

// tmuxSocket is the server socket a process's TMUX names ("<socket>,<server pid>,<session index>"): "" for the
// default server, which is the one tmux runs against without -S.
func tmuxSocket(env string) string {
	socket, _, _ := strings.Cut(env, ",")
	if socket == "" || filepath.Base(socket) == "default" {
		return ""
	}
	return socket
}
