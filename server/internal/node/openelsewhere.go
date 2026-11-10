package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// A Claude session already open in another client — Claude's desktop app, a terminal Persona does not drive — is not
// resumed from the phone: a second `claude --resume` would write the same transcript as the one running. Without a
// terminal host Persona cannot see those as live (they list as not running), so the check is on the session files
// every running claude writes (sessions/<pid>.json in its config directory).

// Refusals of a start or resume, with nothing started or typed (the app shows the reason): a running claude has the
// session open; the machine's engine (herdr) is not running or not installed.
const (
	openElsewhere = "open_elsewhere"
	engineMissing = "engine_missing"
)

// claudeOpenElsewhere is the running claude that has session id open, outside any pane Persona drives (callers ask
// for sessions with no live agent), and where it runs, in the app's words.
func (n *Node) claudeOpenElsewhere(id string) (pid int, where string, ok bool) {
	if id == "" {
		return 0, "", false
	}
	for _, c := range n.liveClaudes() {
		if c.SessionID == id {
			return c.pid, whereOpen(c.Entrypoint), true
		}
	}
	return 0, "", false
}

// claudeProc is a running claude's sessions/<pid>.json: the session it has open and how it runs it.
type claudeProc struct {
	pid        int
	SessionID  string `json:"sessionId"`
	Entrypoint string `json:"entrypoint"`
	Cwd        string `json:"cwd"`
	Name       string `json:"name"`
	// Kind is "interactive" for a claude in a terminal, "bg" for a background session Claude's daemon runs; Status is
	// busy, idle or waiting (claudebg.go).
	Kind   string `json:"kind"`
	Status string `json:"status"`
	// ParkedJobID is set while an interactive claude has sent its own session to the background (claudebg.go).
	ParkedJobID string `json:"parkedJobId"`
	JobID       string `json:"jobId"` // a background session's job (claudebg.go)
	Spare       bool   `json:"spare"` // a warm background process no session has claimed yet
	// ProcStart is when that process started, as Claude Code writes it (on Linux the starttime of /proc/<pid>/stat): a
	// file left by a claude that exited names a pid that may belong to another program by now.
	ProcStart string `json:"procStart"`
	// The claude's peer inbox (claudepeer.go): where it listens and the protocol it speaks.
	MessagingSocketPath string `json:"messagingSocketPath"`
	PeerProtocol        int    `json:"peerProtocol"`
	// Tmux is the pane Claude Code says it runs in ("orc:@11.%11", its session:window.pane), "" for one that is not in
	// tmux: the approval in such a pane can be answered from the app (#2562).
	Tmux string `json:"tmux"`
	dir  string // the sessions directory the file is in (its key file is next to it)
}

// liveClaudes reads the sessions/<pid>.json of every claude still running, in every config directory, once per pid.
func (n *Node) liveClaudes() []claudeProc {
	var out []claudeProc
	seen := map[int]bool{}
	for _, dir := range n.claudeConfigDirs() {
		sessions := filepath.Join(dir, "sessions")
		files, _ := filepath.Glob(filepath.Join(sessions, "*.json"))
		for _, f := range files {
			p, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".json"))
			if err != nil || p <= 0 || seen[p] {
				continue
			}
			st, err := os.Stat(f)
			if err != nil || st.Size() > 64<<10 {
				continue
			}
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			c := claudeProc{pid: p, dir: sessions}
			if json.Unmarshal(b, &c) != nil || !sameProcess(p, claudeFileStart(c.ProcStart)) {
				continue // a file left by a claude that exited (its pid may have been reused)
			}
			seen[p] = true
			out = append(out, c)
		}
	}
	return out
}

// whereOpen names the client a claude runs in, from its entrypoint. The desktop app writes "claude-desktop" and, in
// some builds, a suffix ("claude-desktop-3p"); all of them are the desktop app.
func whereOpen(entrypoint string) string {
	switch {
	case desktopEntrypoint(entrypoint):
		return "Claude 桌面版"
	case entrypoint == "cli":
		return "电脑上的一个终端"
	case strings.HasPrefix(entrypoint, "sdk"):
		return "电脑上的另一个程序"
	default:
		return "电脑上的另一个地方"
	}
}

// desktopEntrypoint: the claude runs in Claude's desktop app.
func desktopEntrypoint(entrypoint string) bool {
	return strings.HasPrefix(entrypoint, "claude-desktop")
}

// openElsewhereRefusal is the refusal for a resume of a session open in another client, nil when none has it open.
func (n *Node) openElsewhereRefusal(id string) error {
	pid, where, ok := n.claudeOpenElsewhere(id)
	if !ok {
		return nil
	}
	w := where
	if w != "" && w[0] < 0x80 {
		w = " " + w // "in the Claude desktop app" in Chinese: a space between Chinese and a Latin word
	}
	return refusal(openElsewhere, "这个会话正在"+w+"里开着，Persona 不会再起一个 Claude 同时写它：在"+w+
		"里接着用，或者先在那边关掉它再从手机发。这次没有发送。", map[string]any{"pid": pid})
}
