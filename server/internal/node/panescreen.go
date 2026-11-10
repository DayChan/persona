package node

import (
	"context"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// screen reads the session's terminal. source "visible" is the screen; "recent" also reaches back into the pane's
// scrollback (the phone pages it: ask for more lines until fewer come back than were asked for). ansi keeps the colors
// (SGR escapes; rows end in \r\n).
func (n *Node) screen(ctx context.Context, id string, lines int, source string, ansi bool) (string, string, error) {
	if n.isAssistantSession(id) {
		return "", proto.StatusIdle, nil // headless: no terminal; live text arrives as session.delta
	}
	if source != "recent" {
		source = "visible"
	}
	if limit := map[string]int{"visible": 400, "recent": 20000}[source]; lines <= 0 || lines > limit {
		lines = min(max(lines, 60), limit)
	}
	a, ok := n.agent(id)
	if !ok {
		// A claude the user started in tmux has no host agent: its own record names the pane (#2562).
		if pane := n.outsidePaneOf(id); pane != "" {
			return n.readOutsideScreen(ctx, id, pane, source, lines, ansi)
		}
		pane, err := n.terminalPane(ctx, id) // also a "pane:" address (panes.go)
		if err != nil {
			return "", proto.StatusOffline, nil
		}
		a, _ = n.agent(id)
		a.PaneID = pane
	}
	if ansi && n.plainScreen(a.PaneID) {
		// No colors in this host: the plain rows, ended as colored ones are.
		text, err := n.readPane(ctx, a.PaneID, source, lines)
		if err == nil && text != "" {
			text = strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\r\n") + "\r\n"
		}
		return text, a.AgentStatus, err
	}
	if ansi {
		text, err := n.readPaneANSI(ctx, a.PaneID, source, lines)
		return text, a.AgentStatus, err
	}
	if lines == screen.DialogLines && source == "visible" { // a screen card's read: what the answer will read too (#6318)
		text, err := n.dialogScreen(ctx, a.PaneID)
		return text, a.AgentStatus, err
	}
	text, err := n.readPane(ctx, a.PaneID, source, lines)
	return text, a.AgentStatus, err
}

// readOutsideScreen reads the pane of a claude no host lists as an agent (a Claude Code the user started in tmux,
// #2562): the same reads a host agent's pane gets, with the status its own record gives it.
func (n *Node) readOutsideScreen(ctx context.Context, id, pane, source string, lines int, ansi bool) (string, string, error) {
	status := proto.StatusUnknown
	if c, ok := n.outsideClaude(id); ok {
		if s, ok := claudeBgStatus[c.Status]; ok {
			status = s
		}
	}
	if ansi && n.plainScreen(pane) {
		text, err := n.readPane(ctx, pane, source, lines)
		if err == nil && text != "" {
			text = strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\r\n") + "\r\n"
		}
		return text, status, err
	}
	if ansi {
		text, err := n.readPaneANSI(ctx, pane, source, lines)
		return text, status, err
	}
	if lines == screen.DialogLines && source == "visible" { // a screen card's read: what the answer will read too
		text, err := n.dialogScreen(ctx, pane)
		return text, status, err
	}
	text, err := n.readPane(ctx, pane, source, lines)
	return text, status, err
}

// herdr keeps about 1000 lines per pane readable (pane read caps there whatever --lines asks); older output is only
// in Claude's transcript.
const herdrScrollbackLines = 1000

// screenBefore returns up to `lines` lines of the pane's scrollback right before the anchor (the first lines the phone
// already shows). Located by content, not by offset from the end, so output arriving meanwhile shifts nothing.
// loaded is how many lines the phone already has (from the end): Claude's redraws push the same screens into the
// scrollback again and again, so the anchor can match in several places — the one nearest len-loaded is ours.
// With ansi the page keeps its colors; the anchor is still matched on the plain text.
func (n *Node) screenBefore(ctx context.Context, id string, anchor []string, lines, loaded int, ansi bool) (map[string]any, error) {
	if lines <= 0 || lines > 500 {
		lines = 200
	}
	a, ok := n.agent(id)
	if !ok {
		pane, err := n.terminalPane(ctx, id) // also a "pane:" address (panes.go)
		if err != nil {
			return nil, err
		}
		a, _ = n.agent(id)
		a.PaneID = pane
	}
	text, err := n.readPane(ctx, a.PaneID, "recent", herdrScrollbackLines)
	if err != nil {
		return nil, err
	}
	all := strings.Split(strings.TrimRight(text, "\n"), "\n")
	at := nearestBlock(all, anchor, len(all)-loaded)
	if at < 0 { // the anchor scrolled out of the kept window: nothing older can be placed before it
		return map[string]any{"text": "", "has_more": false, "at_limit": len(all) >= herdrScrollbackLines-20}, nil
	}
	start := max(at-lines, 0)
	page := all[start:at]
	if ansi {
		// The same rows with their colors: herdr reads both from one grid, so row i is row i — unless output arrived
		// between the two reads, then the plain page is still right.
		if colored, err := n.readPaneANSI(ctx, a.PaneID, "recent", herdrScrollbackLines); err == nil {
			rows := strings.Split(strings.TrimRight(strings.ReplaceAll(colored, "\r\n", "\n"), "\n"), "\n")
			if len(rows) == len(all) {
				page = rows[start:at]
			}
		}
	}
	return map[string]any{
		"text":     strings.Join(page, "\n"),
		"has_more": start > 0,
		// The start of what herdr keeps: older output was dropped (long panes) or never existed (short ones).
		"at_limit": start == 0 && len(all) >= herdrScrollbackLines-20,
	}, nil
}

// nearestBlock is the match of block whose start is closest to want, -1 when it does not appear.
func nearestBlock(all, block []string, want int) int {
	best := -1
	for i := len(all) - len(block); i >= 0; {
		j := findBlock(all[:i+len(block)], block)
		if j < 0 {
			break
		}
		if best < 0 || abs(j-want) < abs(best-want) {
			best = j
		}
		i = j - 1
	}
	return best
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// findBlock returns where the lines of block start in all (the last match), -1 if they do not appear.
func findBlock(all, block []string) int {
	if len(block) == 0 {
		return len(all)
	}
	for i := len(all) - len(block); i >= 0; i-- {
		match := true
		for j, b := range block {
			if strings.TrimRight(all[i+j], " ") != strings.TrimRight(b, " ") {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
