package node

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/herdrcore"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// herdrCoreHost is herdr-core (hc), the runtime behind the Herdr desktop app (docs/plans/plan-orca-support.md "herdr-core
// host"). A pane is a terminal, its id the terminal's address ("t7"), which a core never gives to another terminal.
// Listing, state, plain-text reads, prompts and keys, process → session, events, closing; starting and renaming through
// the app's layout document (hostherdrcore_launch.go). No colors, no typing without submitting, no live stream.
type herdrCoreHost struct {
	c *herdrcore.Client

	mu       sync.Mutex
	failedAt time.Time // when the last terminal.list failed (failErr), for Agents right after a failed Snapshot
	failErr  error
}

var (
	_ host.Host        = (*herdrCoreHost)(nil)
	_ host.Watcher     = (*herdrCoreHost)(nil)
	_ host.PlainScreen = (*herdrCoreHost)(nil)
	_ host.TextTyper   = (*herdrCoreHost)(nil)
	_ host.Organizer   = (*herdrCoreHost)(nil)
	_ host.Launcher    = (*herdrCoreHost)(nil)
)

// herdrCorePaneEnv is the variable herdr-core sets in every terminal: its address.
const herdrCorePaneEnv = nodeclient.HerdrCorePaneEnv

// herdrAppDoc is the Herdr app's layout document on the core: its spaces, each with tabs holding terminals. Terminals
// the app opened also carry its ids as labels (herdrSpaceLabel, herdrTabLabel) and its "<space> / <tab>" as where.
const herdrAppDoc = "herdr-app"

const (
	herdrSpaceLabel = "herdr.space"
	herdrTabLabel   = "herdr.tab"
	herdrWhereLabel = "where"
)

// dirSpace prefixes the space of a terminal the Herdr app does not place (started with hc run): its directory.
const dirSpace = "dir:"

func (h *herdrCoreHost) Kind() string      { return host.HerdrCore }
func (h *herdrCoreHost) PaneEnv() string   { return herdrCorePaneEnv }
func (h *herdrCoreHost) PlainScreen() bool { return true }
func (h *herdrCoreHost) CanTypeText() bool { return false }

func (h *herdrCoreHost) Version(ctx context.Context) string { return h.c.Version(ctx) }

// hcErr marks herdr-core not running (no socket) as host.ErrNotRunning: it then has no terminals.
func hcErr(err error) error {
	if errors.Is(err, herdrcore.ErrNoSocket) {
		return host.Mark(err, host.ErrNotRunning)
	}
	return err
}

// terminals are this machine's live terminals (a core also lists the machines connected to it: their own nodes list
// those). A list that failed a moment ago fails again at once: the node asks for Agents right after a failed Snapshot,
// and a wedged core must not cost it twice.
func (h *herdrCoreHost) terminals(ctx context.Context) ([]herdrcore.Terminal, error) {
	h.mu.Lock()
	if h.failErr != nil && time.Since(h.failedAt) < time.Second {
		err := h.failErr
		h.mu.Unlock()
		return nil, err
	}
	h.mu.Unlock()
	all, err := h.c.List(ctx)
	if err != nil {
		err = hcErr(err)
		h.mu.Lock()
		h.failedAt, h.failErr = time.Now(), err
		h.mu.Unlock()
		return nil, err
	}
	h.mu.Lock()
	h.failErr = nil
	h.mu.Unlock()
	out := all[:0]
	for _, t := range all {
		if !t.Remote() && t.State == "live" {
			out = append(out, t)
		}
	}
	return out, nil
}

func (h *herdrCoreHost) Snapshot(ctx context.Context) (*host.Layout, error) {
	return h.snapshot(ctx, false)
}

func (h *herdrCoreHost) Spaces(ctx context.Context) ([]host.Space, error) {
	l, err := h.snapshot(ctx, true)
	if err != nil {
		return nil, err
	}
	return l.Spaces, nil
}

func (h *herdrCoreHost) snapshot(ctx context.Context, requireDoc bool) (*host.Layout, error) {
	terms, err := h.terminals(ctx)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Spaces []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Folder string `json:"folder"`
			Tabs   []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"tabs"`
		} `json:"spaces"`
	}
	// Agent discovery can place terminals by labels when the app document is unavailable.
	if err := h.c.Doc(ctx, herdrAppDoc, &doc); err != nil && requireDoc {
		return nil, hcErr(err)
	}
	// What the app shows for a space / tab: its name, else the terminal's where label, else the folder / number.
	whereSpace, whereTab := map[string]string{}, map[string]string{}
	for _, t := range terms {
		if i := strings.LastIndex(t.Labels[herdrWhereLabel], " / "); i >= 0 {
			whereSpace[t.Labels[herdrSpaceLabel]] = t.Labels[herdrWhereLabel][:i]
			whereTab[t.Labels[herdrTabLabel]] = t.Labels[herdrWhereLabel][i+3:]
		}
	}
	l := &host.Layout{}
	known := map[string]bool{}
	for i, s := range doc.Spaces {
		label := firstOf(s.Name, whereSpace[s.ID], filepath.Base(s.Folder))
		l.Spaces = append(l.Spaces, host.Space{ID: s.ID, Label: label, Number: i + 1, Cwd: s.Folder})
		known[s.ID] = true
		for j, t := range s.Tabs {
			l.Slots = append(l.Slots, host.Slot{ID: t.ID, SpaceID: s.ID, Label: firstOf(t.Name, whereTab[t.ID], strconv.Itoa(j+1)),
				Number: j + 1})
		}
	}
	for _, t := range terms {
		a := fromTerminal(t)
		if !known[a.WorkspaceID] {
			known[a.WorkspaceID] = true
			label := whereSpace[a.WorkspaceID]
			if strings.HasPrefix(a.WorkspaceID, dirSpace) {
				label = filepath.Base(t.Cwd)
			}
			l.Spaces = append(l.Spaces, host.Space{ID: a.WorkspaceID, Label: label, Number: len(l.Spaces) + 1})
		}
		l.Panes = append(l.Panes, a)
		if a.Agent != "" {
			l.Agents = append(l.Agents, a)
		}
	}
	fillSpaceDirectories(l.Spaces, l.Panes)
	return l, nil
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" && v != "." && v != "/" {
			return v
		}
	}
	return ""
}

func (h *herdrCoreHost) Agents(ctx context.Context) ([]host.Agent, error) {
	terms, err := h.terminals(ctx)
	if err != nil {
		return nil, err
	}
	var out []host.Agent
	for _, t := range terms {
		if a := fromTerminal(t); a.Agent != "" {
			out = append(out, a)
		}
	}
	return out, nil
}

func (h *herdrCoreHost) Agent(ctx context.Context, pane host.PaneRef) (*host.Agent, error) {
	t, err := h.c.Get(ctx, pane.ID)
	if err != nil {
		return nil, hcErr(err)
	}
	a := fromTerminal(*t)
	return &a, nil
}

// Read is terminal.read of the screen only (lines 0), as plain text with soft-wrapped rows joined: asking for history
// makes herdr-core scroll a full-screen program with wheel events to stitch it, holding the user's keys meanwhile, so
// "recent" reads the screen too. Lines cuts it to its last rows.
func (h *herdrCoreHost) Read(ctx context.Context, pane host.PaneRef, o host.ReadOpts) (string, error) {
	if o.ANSI {
		return "", fmt.Errorf("%w: herdr-core reads screens as plain text only", host.ErrUnsupported)
	}
	text, err := h.c.Read(ctx, pane.ID, 0)
	if err != nil {
		return "", hcErr(err)
	}
	if o.Lines > 0 {
		if rows := strings.Split(strings.TrimRight(text, "\n"), "\n"); len(rows) > o.Lines {
			text = strings.Join(rows[len(rows)-o.Lines:], "\n") + "\n"
		}
	}
	return text, nil
}

// SendText: herdr-core cannot type text without submitting it (terminal.send always presses enter; raw input exists
// only on an attach, which would take the terminal's size).
func (h *herdrCoreHost) SendText(context.Context, host.PaneRef, string) error {
	return fmt.Errorf("%w: herdr-core cannot type text without submitting it", host.ErrUnsupported)
}

// SendKeys passes Persona's key names through: herdr-core takes the same spelling ("enter", "esc", "shift+tab",
// "ctrl+u", "up", "backspace"…).
func (h *herdrCoreHost) SendKeys(ctx context.Context, pane host.PaneRef, keys ...string) error {
	return inputErr(h.c.Keys(ctx, pane.ID, keys...))
}

// Prompt is terminal.send: a paste and a submit. The terminal is looked at first: once the agent has exited, its last
// state stays on the terminal and the shell under it would run the text as a command, so that is refused with nothing
// typed.
func (h *herdrCoreHost) Prompt(ctx context.Context, pane host.PaneRef, text string) error {
	t, err := h.c.Get(ctx, pane.ID)
	if err != nil {
		return inputErr(err)
	}
	if fromTerminal(*t).Agent == "" {
		return host.Mark(fmt.Errorf("herdr-core: no agent runs in %s any more", pane.ID), host.ErrNotDelivered)
	}
	return inputErr(h.c.Send(ctx, pane.ID, text))
}

// inputErr marks the herdr-core refusals that typed nothing (host.ErrNotDelivered): a program reporting a dialog
// (blocked, also host.ErrDialogOpen), a status that changed, a terminal not ready, finished or gone.
func inputErr(err error) error {
	switch {
	case err == nil:
		return nil
	case herdrcore.IsKind(err, "blocked"):
		return host.Mark(host.Mark(err, host.ErrNotDelivered), host.ErrDialogOpen)
	case herdrcore.IsKind(err, "status_mismatch"), herdrcore.IsKind(err, "unavailable"), herdrcore.IsKind(err, "finished"),
		herdrcore.IsKind(err, "not_found"), errors.Is(err, herdrcore.ErrNoSocket):
		return host.Mark(hcErr(err), host.ErrNotDelivered)
	}
	return err
}

// Close ends the terminal with terminal.kill, which is what closing a pane in the Herdr app does: the app then drops
// the pane (its tab when it was the only one) on its own. A kill is final even for a keep terminal (the app's shells
// are kept; keep restarts a shell only after a loss). The finished record is left for the app, as it leaves it: no
// terminal.rm. A terminal that already finished or is gone counts as closed.
func (h *herdrCoreHost) Close(ctx context.Context, pane host.PaneRef, _, _ string) error {
	t, err := h.c.Kill(ctx, pane.ID)
	if herdrcore.IsKind(err, "not_found") {
		return nil
	}
	if err != nil {
		return hcErr(err)
	}
	if t.State == "live" {
		return fmt.Errorf("herdr-core: %s still runs after terminal.kill", pane.ID)
	}
	return nil
}

// PaneProcesses: the terminal's foreground process (claude's own pid, which names its sessions/<pid>.json), then its
// first process.
func (h *herdrCoreHost) PaneProcesses(ctx context.Context, pane host.PaneRef) ([]int, error) {
	t, err := h.c.Get(ctx, pane.ID)
	if err != nil {
		return nil, hcErr(err)
	}
	var pids []int
	if t.Foreground != nil && t.Foreground.Pid > 0 {
		pids = append(pids, t.Foreground.Pid)
	}
	if t.Pid > 0 && !slices.Contains(pids, t.Pid) {
		pids = append(pids, t.Pid)
	}
	return pids, nil
}

// Watch is one events.subscribe for every terminal (the core has no per-terminal subscriptions to keep up to date,
// so panes are not needed). Size and keep changes do not change a session. A status into or out of blocked is
// urgent.
func (h *herdrCoreHost) Watch(ctx context.Context, _ []host.PaneRef, changed func(urgent bool)) error {
	var w waits
	return h.c.Subscribe(ctx, func() { changed(false) }, func(ev herdrcore.Event) {
		if ev.Type == "size" || ev.Type == "keep" || strings.Contains(ev.Terminal, "/") {
			return
		}
		blocked := ev.Data.Status != nil && ev.Data.Status.State == "blocked"
		changed(ev.Type == "status" && w.changed(ev.Terminal, blocked))
	})
}

// hcAgents names the agents herdr-core's status reports (status.app, the program's own word) the way the node does.
var hcAgents = map[string]string{"claude-code": "claude", "claude": "claude", "codex": "codex"}

// hcStatus maps OSC 7501 states to the node's; error has no counterpart (the program said something failed, not what
// it does now).
var hcStatus = map[string]string{
	"idle": proto.StatusIdle, "working": proto.StatusWorking, "blocked": proto.StatusBlocked, "done": proto.StatusDone,
}

// fromTerminal is a terminal as an agent pane. Its agent is what its status names (claude or codex), else what runs in
// it (a claude that has not reported yet) — none when a shell, classic herdr or nothing is in the foreground; its
// space and slot are the Herdr app's when the app opened it, else its directory.
func fromTerminal(t herdrcore.Terminal) host.Agent {
	a := host.Agent{
		Name: t.Name, Cwd: t.Cwd, PaneID: host.PaneRef{Kind: host.HerdrCore, ID: t.Address}.String(),
		WorkspaceID: t.Labels[herdrSpaceLabel], TabID: t.Labels[herdrTabLabel],
		Title: stripSpinner(t.Title), RawTitle: t.Title,
	}
	if a.WorkspaceID == "" {
		a.WorkspaceID, a.TabID = dirSpace+t.Cwd, ""
	}
	if t.Foreground == nil {
		return a // nothing runs (finished): a status left behind is not an agent
	}
	a.ForegroundCwd = t.Foreground.Cwd
	if nestedHerdr(t.Foreground) || isShell(t.Foreground) {
		// Classic herdr's client: whatever runs inside belongs to herdr. A shell: the agent that reported the status
		// has exited (herdr-core keeps a program's last status until the terminal's own program ends).
		return a
	}
	if t.Status != nil && t.Status.App != "" {
		k, ok := hcAgents[t.Status.App]
		if !ok {
			return a // a program the node does not drive: left to the panes a compatible CLI may claim
		}
		a.Agent = k
		a.AgentStatus = hcStatus[t.Status.State]
		if a.AgentStatus == "" {
			a.AgentStatus = proto.StatusUnknown
		}
		return a
	}
	if len(t.Foreground.Argv) > 0 {
		if k := filepath.Base(t.Foreground.Argv[0]); k == "claude" || k == "codex" {
			a.Agent, a.AgentStatus = k, proto.StatusUnknown
		}
	}
	return a
}

// shells are the programs a terminal returns to when its agent exits.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true, "tcsh": true,
	"csh": true, "nu": true, "pwsh": true, "elvish": true, "xonsh": true}

// isShell: the foreground program is a shell (a login shell's name starts with "-").
func isShell(fg *herdrcore.Foreground) bool {
	return shellProgram(fg.Name) || len(fg.Argv) > 0 && shellProgram(fg.Argv[0])
}

// shellProgram: a program name or path ("-zsh", "/bin/bash") is a shell's.
func shellProgram(name string) bool { return shells[strings.TrimPrefix(filepath.Base(name), "-")] }

// nestedHerdr: the terminal's foreground program is classic herdr's client. The claude in one of herdr's panes may
// report its OSC 7501 state through it, so this terminal would look like claude's own; it is not an agent here.
func nestedHerdr(fg *herdrcore.Foreground) bool {
	if fg.Name == "herdr" {
		return true
	}
	return len(fg.Argv) > 0 && filepath.Base(fg.Argv[0]) == "herdr"
}

// stripSpinner drops the glyph a busy TUI puts before its title ("✳ fix the build", "⠸ fix the build").
func stripSpinner(title string) string {
	r := []rune(title)
	if len(r) >= 2 && r[1] == ' ' && !unicode.IsLetter(r[0]) && !unicode.IsDigit(r[0]) && r[0] > unicode.MaxASCII {
		return strings.TrimSpace(string(r[2:]))
	}
	return title
}
