package node

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
)

// errNoOrcaTerminal: Orca lists no terminal with that pane key (closed, or Orca restarted).
var errNoOrcaTerminal = errors.New("orca: no terminal")

// terminal is the pane's terminal: from the last listing, else from a new one (fresh: always a new one).
func (h *orcaHost) terminal(ctx context.Context, paneKey string, fresh bool) (orcart.Terminal, error) {
	if !fresh {
		h.mu.Lock()
		t, ok := h.terms[paneKey]
		h.mu.Unlock()
		if ok {
			return t, nil
		}
	}
	if _, _, err := h.terminals(ctx); err != nil {
		return orcart.Terminal{}, err
	}
	h.mu.Lock()
	t, ok := h.terms[paneKey]
	h.mu.Unlock()
	if !ok {
		return orcart.Terminal{}, host.Mark(fmt.Errorf("%w %s", errNoOrcaTerminal, paneKey), host.ErrNotDelivered)
	}
	return t, nil
}

// onTerminal runs fn on the pane's terminal. A handle Orca no longer knows (the terminal came back under a new one) is
// looked up again once; for a write only when the terminal still runs the same process (incarnation), so a write
// never lands in a new process behind the pane — that is refused, nothing typed.
func (h *orcaHost) onTerminal(ctx context.Context, t orcart.Terminal, write bool, fn func(orcart.Target) error) error {
	err := fn(t.Target())
	if !orcart.IsCode(err, "terminal_handle_stale") {
		return err
	}
	t2, lerr := h.terminal(ctx, t.PaneKey(), true)
	if lerr != nil {
		return lerr
	}
	if write && (t.IncarnationID == "" || t2.IncarnationID != t.IncarnationID) {
		return host.Mark(fmt.Errorf("orca: the terminal in %s runs another process now", t.PaneKey()), host.ErrNotDelivered)
	}
	return fn(t2.Target())
}

// errNotInFront: no agent is in front in the pane (it exited, is suspended, runs in the background or inside a nested
// host): text would go to whatever is in front instead (a shell would run it).
var errNotInFront = errors.New("no agent is in front in the pane")

// front makes sure an agent is in front in the pane before text goes in, and returns its terminal: our own process
// scan finds a claude or codex in front with this pane key (not suspended, not in the background, not inside a nested
// host). Anything else — including not being able to tell — refuses with nothing typed. Orca's terminal.agentStatus
// is asked too, for a permission dialog (prompt: the text is a prompt, which the dialog would take as its answer); its
// isRunningAgent is not required on macOS and Linux: Orca says false while Claude Code has background tasks, though
// its input box takes text then (#5463), and the scan already tells what is in front of the pane's own pty (ptyOwner).
// On Windows, whose scan cannot tell, it still is.
func (h *orcaHost) front(ctx context.Context, paneKey string, prompt bool) (orcart.Terminal, error) {
	t, err := h.terminal(ctx, paneKey, false)
	if err != nil {
		return t, orcaInputErr(err)
	}
	procs, err := h.agentProcs(0)
	if err != nil {
		return t, host.Mark(fmt.Errorf("orca: cannot tell what runs in %s: %w", paneKey, err), host.ErrNotDelivered)
	}
	if len(procs[paneKey]) == 0 {
		return t, host.Mark(fmt.Errorf("orca: %w %s", errNotInFront, paneKey), host.ErrNotDelivered)
	}
	var st *orcart.AgentStatus
	err = h.onTerminal(ctx, t, false, func(tg orcart.Target) error {
		var e error
		st, e = h.c.AgentStatus(ctx, tg)
		return e
	})
	if err != nil {
		return t, host.Mark(fmt.Errorf("orca: cannot tell what runs in %s: %w", paneKey, err), host.ErrNotDelivered)
	}
	if !st.IsRunningAgent && runtime.GOOS == "windows" {
		// Windows' scan has no foreground or stopped state to tell by (scan_windows.go): Orca's answer stays the gate.
		return t, host.Mark(fmt.Errorf("orca: %w %s (Orca sees none)", errNotInFront, paneKey), host.ErrNotDelivered)
	}
	if prompt && st.Status == "permission" {
		return t, host.Mark(host.Mark(fmt.Errorf("orca: the agent in %s waits on a permission dialog", paneKey),
			host.ErrNotDelivered), host.ErrDialogOpen)
	}
	return t, nil
}

// Read is terminal.read of the visible screen as plain text: Orca reads no colors here, and the screen read is the one
// that never switches source, so "recent" is the screen too. Lines cuts it to its last rows.
func (h *orcaHost) Read(ctx context.Context, pane host.PaneRef, o host.ReadOpts) (string, error) {
	if o.ANSI {
		return "", fmt.Errorf("%w: Orca reads screens as plain text only", host.ErrUnsupported)
	}
	t, err := h.terminal(ctx, pane.ID, false)
	if err != nil {
		return "", orcaErr(err)
	}
	var text string
	err = h.onTerminal(ctx, t, false, func(tg orcart.Target) error {
		s, err := h.c.Read(ctx, tg)
		if err != nil {
			return err
		}
		text = s.Text()
		return nil
	})
	if err != nil {
		return "", orcaErr(err)
	}
	if o.Lines > 0 {
		if rows := strings.Split(strings.TrimRight(text, "\n"), "\n"); len(rows) > o.Lines {
			text = strings.Join(rows[len(rows)-o.Lines:], "\n") + "\n"
		}
	}
	return text, nil
}

// SendText types text as is (terminal.send without enter writes the bytes), once the agent is in front.
func (h *orcaHost) SendText(ctx context.Context, pane host.PaneRef, text string) error {
	t, err := h.front(ctx, pane.ID, false)
	if err != nil {
		return err
	}
	return orcaInputErr(h.onTerminal(ctx, t, true, func(tg orcart.Target) error { return h.c.Send(ctx, tg, text) }))
}

// orcaKeyGap: keys go one write each, a moment apart, so an escape is not read as the start of the next key's
// sequence (esc then shift+tab would arrive as alt+escape-sequence).
const orcaKeyGap = 40 * time.Millisecond

// SendKeys presses the keys one by one (their bytes, orcaKeyBytes); a key with no bytes fails before anything is
// pressed. Keys that type text (letters, digits, space) need the agent in front, as SendText does; the others (enter,
// esc, arrows, ctrl+c…) go to whatever is there, so a stuck pane can still be interrupted and closed.
func (h *orcaHost) SendKeys(ctx context.Context, pane host.PaneRef, keys ...string) error {
	seq := make([]string, 0, len(keys))
	typing := false
	for _, k := range keys {
		b, ok := orcaKeyBytes(k)
		if !ok {
			return host.Mark(fmt.Errorf("orca: unknown key %q", k), host.ErrNotDelivered)
		}
		typing = typing || orcaTypes(b)
		seq = append(seq, b)
	}
	var t orcart.Terminal
	var err error
	if typing {
		t, err = h.front(ctx, pane.ID, false)
	} else {
		t, err = h.terminal(ctx, pane.ID, false)
	}
	if err != nil {
		return orcaInputErr(err)
	}
	for i, b := range seq {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err() // some keys were pressed
			case <-time.After(orcaKeyGap):
			}
		}
		err := h.onTerminal(ctx, t, true, func(tg orcart.Target) error { return h.c.Send(ctx, tg, b) })
		if err != nil {
			if i > 0 {
				return orcaErr(err) // some keys were pressed: not safe to send again
			}
			return orcaInputErr(err)
		}
	}
	return nil
}

// Bracketed paste markers, and what an ESC inside the text becomes (Orca's own sanitizeAgentPromptText): an ESC in the
// text could end the paste early and turn the rest into keys.
const (
	pasteStart  = "\x1b[200~"
	pasteEnd    = "\x1b[201~"
	inertEscape = "<ESC>"
)

// orcaPasteGap is how long the agent gets to take a paste before the enter that submits it: a base, a little more per
// line, capped.
func orcaPasteGap(text string) time.Duration {
	return min(orcaSubmitGap+time.Duration(strings.Count(text, "\n"))*10*time.Millisecond, time.Second)
}

// orcaSubmitGap is the base of orcaPasteGap.
var orcaSubmitGap = 150 * time.Millisecond

// Prompt submits text to the agent the way herdr's agent.prompt does: a bracketed paste of the whole text (newlines
// stay in the input instead of each submitting), then a separate carriage return. Orca's own agentPrompt path is not
// used: Orca pastes only for an agent it has seen working, and writes a fresh agent's prompt raw, so a multi-line
// message would be submitted at its first newline.
//
// The agent must be in front (front) before each write: Orca writes whatever it is sent into whatever is there. A
// refusal before the paste typed nothing (host.ErrNotDelivered); anything failing after it leaves the text possibly
// sitting in the input (not marked: delivered unknown).
func (h *orcaHost) Prompt(ctx context.Context, pane host.PaneRef, text string) error {
	t, err := h.front(ctx, pane.ID, true)
	if err != nil {
		return err
	}
	paste := pasteStart + strings.ReplaceAll(text, "\x1b", inertEscape) + pasteEnd
	if err := h.onTerminal(ctx, t, true, func(tg orcart.Target) error { return h.c.Send(ctx, tg, paste) }); err != nil {
		return orcaInputErr(err)
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("orca: the text was pasted into %s but not submitted: %v", pane.ID, ctx.Err())
	case <-time.After(orcaPasteGap(text)):
	}
	afterPaste := func(err error) error {
		return fmt.Errorf("orca: the text was pasted into %s but not submitted: %v", pane.ID, err) // not marked: unknown
	}
	if t, err = h.front(ctx, pane.ID, false); err != nil {
		return afterPaste(err)
	}
	if err := h.onTerminal(ctx, t, true, func(tg orcart.Target) error { return h.c.Send(ctx, tg, "\r") }); err != nil {
		return afterPaste(err)
	}
	return nil
}

// orcaInputErr marks the refusals that typed nothing (host.ErrNotDelivered): Orca not running or incompatible, a
// terminal that is gone, exited, replaced or not writable, a request it rejected before writing, a send it turned
// down (accepted false; "permission": the agent waits on a dialog, also host.ErrDialogOpen).
func orcaInputErr(err error) error {
	var r *orcart.Refusal
	switch {
	case err == nil:
		return nil
	case errors.Is(err, host.ErrNotDelivered):
		return err
	case errors.As(err, &r):
		if r.Reason == "permission" {
			return host.Mark(host.Mark(err, host.ErrNotDelivered), host.ErrDialogOpen)
		}
		return host.Mark(err, host.ErrNotDelivered)
	case errors.Is(err, orcart.ErrNotRunning), errors.Is(err, orcart.ErrIncompatible), orcart.Refused(err):
		return host.Mark(orcaErr(err), host.ErrNotDelivered)
	}
	return err
}

func (h *orcaHost) RenameSpace(ctx context.Context, space, name string) error {
	trees, _, _, err := h.c.Worktrees(ctx, "")
	if err != nil {
		return orcaErr(err)
	}
	for _, tree := range trees {
		if tree.WorktreeID == space && (tree.HostID == "" || tree.HostID == "local") {
			return orcaErr(h.c.RenameWorktree(ctx, space, name))
		}
	}
	return fmt.Errorf("orca: the selected worktree is no longer available")
}

func (h *orcaHost) RenameSlot(ctx context.Context, slot, name string) error {
	terms, _, err := h.terminals(ctx)
	if err != nil {
		return orcaErr(err)
	}
	for _, term := range terms {
		if term.TabID == slot && term.IsLocal() && term.Writable && !term.Orphaned {
			return orcaErr(h.onTerminal(ctx, term, true, func(target orcart.Target) error {
				return h.c.RenameTerminal(ctx, target, name)
			}))
		}
	}
	return fmt.Errorf("orca: the selected tab is no longer available")
}

// Close is terminal.close: Orca closes the pane (its tab when it was the only one). A terminal already gone counts as
// closed.
func (h *orcaHost) Close(ctx context.Context, pane host.PaneRef, _, _ string) error {
	h.mu.Lock()
	delete(h.launches, pane.ID)
	h.mu.Unlock()
	t, err := h.terminal(ctx, pane.ID, false)
	if err == nil {
		err = h.onTerminal(ctx, t, true, func(tg orcart.Target) error { return h.c.Close(ctx, tg) })
	}
	if errors.Is(err, errNoOrcaTerminal) || orcart.IsCode(err, "terminal_handle_stale") ||
		orcart.IsCode(err, "terminal_gone") || orcart.IsCode(err, "terminal_exited") {
		return nil // no terminal with that pane key any more
	}
	return orcaErr(err)
}
