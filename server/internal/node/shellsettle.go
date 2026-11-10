package node

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A start types its line into a tab it has just made (typedstart.go). A fixed 300 ms after the tab was made, the
// shell was often still reading its startup files, and what those files do with the terminal decides what becomes of
// the typed line: oh-my-zsh's "Would you like to update? [Y/n]" reads one key, so it ate the line's first character
// and the shell printed "command not found" for the rest; the start then waited the full claudeStartWait for a claude
// that never came (#2570). settleShell waits for the shell to be at rest first: only shells in front of the pane (no
// git, nvm or brew an rc file runs) and the screen the same for a moment. A startup question is not answered: the
// start ends with shell_waiting and the tab is kept for someone to answer it (keepStartTab).

// shellSettleWait is how long a start waits at most for the new tab's shell to settle before typing anyway, as it did
// before. shellQuietFor is how long its screen must stay the same. shellQuestionWait is how long a startup question may
// stay on screen (it may time out by itself) before the start ends with shell_waiting. shellSettlePoll is how often the
// screen is read meanwhile. A node's shellSettle, shellQuiet and shellAsk replace the first three when set (tests).
const (
	shellSettleWait   = 15 * time.Second
	shellQuietFor     = 600 * time.Millisecond
	shellQuestionWait = 20 * time.Second
	shellSettlePoll   = 200 * time.Millisecond
)

// keptStartWait is how long the tab of a start that ended waiting on its shell (shell_waiting) or on the agent's own
// screen (agent_start_stalled) stays open for someone to look at or answer; then it is closed unless an agent was
// bound to it meanwhile.
const keptStartWait = 10 * time.Minute

// shellWaits is how long this node's starts wait for a new tab's shell: at most, its screen unchanged, a question.
func (n *Node) shellWaits() (settle, quiet, ask time.Duration) {
	settle, quiet, ask = shellSettleWait, shellQuietFor, shellQuestionWait
	if n.shellSettle > 0 {
		settle = n.shellSettle
	}
	if n.shellQuiet > 0 {
		quiet = n.shellQuiet
	}
	if n.shellAsk > 0 {
		ask = n.shellAsk
	}
	return settle, quiet, ask
}

// shellQuestions are the last lines of a screen whose shell waits for an answer before its prompt: a yes/no question
// ([Y/n], [y/N], (y/n), (yes/no)), a key to press, and oh-my-zsh's update question by its words.
var shellQuestions = []*lazyre.Regexp{
	lazyre.New(`\[[Yy]/[Nn]\]\s*[:?]?\s*$`),
	lazyre.New(`\((?:[Yy]|[Yy]es)/(?:[Nn]|[Nn]o)\)\s*[:?]?\s*$`),
	lazyre.New(`(?i)\bpress (?:enter|return|any key)\b`),
	lazyre.New(`(?i)would you like to (?:update|check for updates)\b`),
}

// shellQuestion is the last line of screen when it asks something (shellQuestions), else "".
func shellQuestion(screen string) string {
	last := lastLine(screen)
	for _, q := range shellQuestions {
		if q.MatchString(last) {
			return last
		}
	}
	return ""
}

// lastLine is screen's last line that is not blank, trimmed.
func lastLine(screen string) string {
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// settleShell waits until the shell of the tab a start made, pane, is at rest before the start types into it: only
// shells in front of the pane (or a host that cannot say) and its screen unchanged for shellQuietFor. After
// shellSettleWait it returns nil anyway, as the start went on before. A screen that cannot be read returns at once. A
// startup question on the last line is waited out for shellQuestionWait; one still there ends the start: shellWaiting.
func (n *Node) settleShell(ctx context.Context, pane string) error {
	if n.shellPause > 0 {
		time.Sleep(n.shellPause)
		return nil
	}
	settle, quiet, ask := n.shellWaits()
	began := time.Now()
	last, since := "", began
	var asked time.Time // since when a question has been on screen
	for {
		screen, err := n.readPane(ctx, pane, "visible", 60)
		if err != nil {
			return nil
		}
		now := time.Now()
		if screen != last {
			last, since = screen, now
		}
		if q := shellQuestion(screen); q != "" {
			if asked.IsZero() {
				asked = now
			}
			if now.Sub(asked) >= ask {
				full, rerr := n.readPane(ctx, pane, "recent-unwrapped", 30)
				if rerr != nil || strings.TrimSpace(full) == "" {
					full = screen
				}
				return shellWaiting(q, full, ask)
			}
		} else {
			asked = time.Time{}
			if strings.TrimSpace(screen) != "" && now.Sub(since) >= quiet && n.shellBackOrUnknown(ctx, pane) {
				return nil
			}
			if now.Sub(began) >= settle {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(shellSettlePoll):
		}
	}
}

// shellWaiting is the refusal of a start whose new tab's shell stayed on question for wait: nothing was typed. The
// tab's last lines go with it (screen) for whoever looks at it; the start adds where the tab is kept (keepStartTab).
func shellWaiting(question, screen string, wait time.Duration) error {
	return refusal(proto.CodeShellWaiting, "the new tab's shell is waiting for an answer: "+question+
		" (on screen for "+wait.String()+"; nothing was typed, and the tab is kept open)\n"+lastLines(screen, 12),
		map[string]any{"line": question, "screen": lastLines(screen, 12)})
}

// keptStart is the tab of a failed start left open (keepStartTab).
type keptStart struct {
	id      string // the session id the start would have had ("" for an agent whose id it learns later)
	launch  host.Launcher
	created *host.Created
	ws      string
	until   time.Time
}

// keepStartTab keeps the tab of a start that failed waiting on its shell or on the agent's screen, created, open for
// keptStartWait instead of closing it, and returns until when: someone may answer the question or look at what
// stopped. Then it is closed (closeNewTab), unless an agent was bound to its pane meanwhile.
func (n *Node) keepStartTab(launch host.Launcher, created *host.Created, ws, id string) time.Time {
	pane := created.Pane.String()
	until := time.Now().Add(keptStartWait)
	n.mu.Lock()
	if n.keptStarts == nil {
		n.keptStarts = map[string]keptStart{}
	}
	n.keptStarts[pane] = keptStart{id: id, launch: launch, created: created, ws: ws, until: until}
	n.mu.Unlock()
	time.AfterFunc(keptStartWait, func() { n.expireKeptStart(pane, until) })
	return until
}

// expireKeptStart closes the kept tab on pane kept until until, once that has passed, when no agent is bound to it.
func (n *Node) expireKeptStart(pane string, until time.Time) {
	n.mu.Lock()
	k, ok := n.keptStarts[pane]
	if !ok || !k.until.Equal(until) {
		n.mu.Unlock()
		return
	}
	delete(n.keptStarts, pane)
	_, bound := n.panes[pane]
	for _, a := range n.agents { // an agent that came up there after all (a stalled claude that went on)
		bound = bound || a.PaneID == pane
	}
	n.mu.Unlock()
	if bound {
		return
	}
	n.closeNewTab(context.Background(), k.launch, k.created, k.ws)
}

// withKeptTab is err, a start's refusal, saying where its tab is kept and until when (pane, kept_until), for whoever
// looks at or answers it. Any other error is returned as it is.
func withKeptTab(err error, pane string, until time.Time) error {
	var re *rpc.Error
	if !errors.As(err, &re) {
		return err
	}
	data := map[string]any{}
	if len(re.Data) > 0 {
		_ = json.Unmarshal(re.Data, &data)
	}
	data["pane"] = pane
	data["kept_until"] = until.UTC().Format(time.RFC3339)
	b, _ := json.Marshal(data)
	return &rpc.Error{Code: re.Code, Message: re.Message, Data: b}
}
