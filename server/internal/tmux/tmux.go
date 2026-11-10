// Package tmux drives a pane of a tmux server through the tmux command: what its screen shows and the keys pressed in
// it. A pane id is tmux's own ("%11"), unique within one server; the server is the default one unless Socket names
// another (a server started with -L or -S). Nothing here reads tmux's layout or streams its output: a pane Persona did
// not start is reached by the id Claude Code itself wrote down (sessions/<pid>.json "tmux").
package tmux

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
)

// Client runs tmux for one server.
type Client struct {
	Bin    string // the tmux to run, "" for the one on PATH
	Socket string // the server's socket, "" for the default server
}

// ErrNoServer marks a tmux that is not running (no server for the socket): the pane is gone.
var ErrNoServer = fmt.Errorf("tmux: no server")

func (c Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "tmux"
}

func (c Client) run(ctx context.Context, stdin string, args ...string) (string, error) {
	if c.Socket != "" {
		args = append([]string{"-S", c.Socket}, args...)
	}
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		if strings.Contains(errb.String(), "no server running") || strings.Contains(errb.String(), "error connecting to") {
			return out.String(), fmt.Errorf("%w: %s", ErrNoServer, strings.TrimSpace(errb.String()))
		}
		return out.String(), fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// Version is the tmux client's version ("tmux 3.6a"), "" when tmux is not installed or not running.
func (c Client) Version(ctx context.Context) string {
	out, err := c.run(ctx, "", "-V")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// Capture is what the pane shows: its visible screen, or its last lines rows of history with lines > 0. ansi keeps
// the colors tmux re-emits.
func (c Client) Capture(ctx context.Context, pane string, lines int, ansi bool) (string, error) {
	args := []string{"capture-pane", "-p", "-t", pane}
	if ansi {
		args = append(args, "-e")
	}
	if lines > 0 {
		args = append(args, "-S", fmt.Sprintf("-%d", lines))
	}
	out, err := c.run(ctx, "", args...)
	if err != nil {
		return "", err
	}
	return out, nil
}

// tmuxKeys translates Persona's key names (host.Host.SendKeys) to tmux's.
var tmuxKeys = map[string]string{
	"enter": "Enter", "esc": "Escape", "tab": "Tab", "shift+tab": "BTab", "backspace": "BSpace", "space": "Space",
	"up": "Up", "down": "Down", "left": "Left", "right": "Right", "home": "Home", "end": "End",
	"pageup": "PageUp", "pagedown": "PageDown", "delete": "DC", "insert": "IC",
}

// Keys presses keys in the pane; an unknown name is refused before any key is pressed.
func (c Client) Keys(ctx context.Context, pane string, keys ...string) error {
	args := []string{"send-keys", "-t", pane}
	for _, k := range keys {
		name, ok := tmuxKeys[k]
		if !ok {
			if rest, cut := strings.CutPrefix(k, "ctrl+"); cut && len(rest) == 1 {
				name = "C-" + rest
			} else if rest, cut := strings.CutPrefix(k, "alt+"); cut && len(rest) == 1 {
				name = "M-" + rest
			} else {
				name = k // an xterm key name tmux knows ("F1", "Escape")
			}
		}
		args = append(args, name)
	}
	_, err := c.run(ctx, "", args...)
	return err
}

var bufferSeq atomic.Uint64

// SendText types text into the pane without submitting it, through a tmux buffer so that a line break is text and not
// Enter (bracketed paste when the program takes it):
func (c Client) SendText(ctx context.Context, pane, text string) error {
	buf := fmt.Sprintf("persona-%d", bufferSeq.Add(1))
	if _, err := c.run(ctx, text, "load-buffer", "-b", buf, "-"); err != nil {
		return err
	}
	_, err := c.run(ctx, "", "paste-buffer", "-p", "-d", "-b", buf, "-t", pane)
	return err
}

// Prompt types text and presses Enter.
func (c Client) Prompt(ctx context.Context, pane, text string) error {
	if err := c.SendText(ctx, pane, text); err != nil {
		return err
	}
	return c.Keys(ctx, pane, "enter")
}
