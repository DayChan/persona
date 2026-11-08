package node

import (
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// agent is the live agent of session id, with a Claude's status as claudeStatus says.
func (n *Node) agent(id string) (host.Agent, bool) {
	n.mu.Lock()
	a, ok := n.agents[id]
	n.mu.Unlock()
	if ok {
		a.AgentStatus = n.claudeStatus(id, a, time.Now())
	}
	return a, ok
}

// claudeFresh: how recently a Claude transcript with an open turn must have been written to count as at work.
const claudeFresh = 45 * time.Second

// claudeStatus is a Claude's status with its own signs of work over its host's (#2069: herdr 0.9.3 said done through a
// whole turn started from the phone, so the app showed it as finished while it searched code). When the host says
// done, idle or does not know:
//
//   - a session that runs Persona's hooks is judged by their turn counters first (judgeHookTurn): a prompt no Stop has
//     answered is working, a Stop with no prompt after it is not;
//   - otherwise, or while the hooks have said nothing yet, it is working while its terminal title shows Claude Code's
//     spinner, or its transcript has a turn started and not finished (no turn_duration, no interrupt) written to
//     within claudeFresh. A transcript whose turn has finished keeps the host's word: a title read just before Claude
//     redrew it says nothing then.
//
// Hooks whose claude process is gone (or whose pid now belongs to another program) are forgotten.
func (n *Node) claudeStatus(id string, a host.Agent, now time.Time) string {
	if a.Agent != "claude" || a.AgentStatus != proto.StatusDone && a.AgentStatus != proto.StatusIdle && a.AgentStatus != proto.StatusUnknown {
		return a.AgentStatus
	}
	var m quickMeta
	var mod time.Time
	hasFile := false
	if p := n.transcriptPath(id); p != "" {
		if st, err := os.Stat(p); err == nil {
			m, mod, hasFile = n.meta(p, st), st.ModTime(), true
		}
	}
	// When the transcript last said something: its newest timed line, not its mtime (#5967); the mtime when the tail has
	// no time at all.
	if !m.contentAt.IsZero() {
		mod = m.contentAt
	}
	fresh := hasFile && now.Sub(mod) < claudeFresh
	spinner := claudeSpinnerTitle(a.RawTitle)
	if t, ok := n.hooks.get(id); ok {
		if t.pid > 0 && !processAlive(t.pid, t.start) {
			n.hooks.forget(id) // a dead process' turn is over, and a reused pid is not it
		} else {
			switch judgeHookTurn(hookSignals{turn: t, spinner: spinner, waiting: claudeWaitingTitle(a.RawTitle), hasFile: hasFile,
				fileMod: mod, turnOpen: m.claudeTurn, turnClosed: m.turnClosed, now: now}) {
			case hookWorking:
				return proto.StatusWorking
			case hookResting:
				return a.AgentStatus
			}
		}
	}
	switch {
	case m.turnClosed:
		return a.AgentStatus
	case spinner, m.claudeTurn && fresh:
		return proto.StatusWorking
	}
	return a.AgentStatus
}

// restingClaude: a Claude Code in a herdr pane whose host says its turn is over (or does not know), so a dialog on
// its screen is what it waits on (dialogstatus.go). Only herdr: where it was seen (#2553); Orca reports its own
// permission state, and botmux sessions have no screen of theirs to read.
func restingClaude(a host.Agent, status string) bool {
	if a.Agent != "claude" || a.PaneID == "" || host.ParsePane(a.PaneID).Kind != host.Herdr {
		return false
	}
	return status == proto.StatusDone || status == proto.StatusIdle || status == proto.StatusUnknown
}

// isDriving: Persona is working the session's TUI itself (modes.go): a picker it opened is not the user's to answer.
func (n *Node) isDriving(id string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.driving[id]
}

// claudeSpinnerTitle: the terminal title starts with the spinner Claude Code shows in it while it works — a quarter
// circle (◐◓◑◒, 2.1.3xx) or, in older versions, a braille dot; ✳ when it waits.
func claudeSpinnerTitle(title string) bool {
	r, _ := utf8.DecodeRuneInString(strings.TrimSpace(title))
	return r >= '◐' && r <= '◓' || r >= 0x2800 && r <= 0x28FF
}

// claudeWaitingTitle: the title starts with the mark Claude Code shows in it while it waits for input (✳).
func claudeWaitingTitle(title string) bool {
	r, _ := utf8.DecodeRuneInString(strings.TrimSpace(title))
	return r == '✳'
}
