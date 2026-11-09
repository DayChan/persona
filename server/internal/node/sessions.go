package node

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// hostTextSlack: the transcript's words this long after the host's record of the answer still belong to that turn.
const hostTextSlack = 10 * time.Minute

// hostNotices applies what the host says beyond the state: a failure notice (FailureReason, the state stays the
// turn's), and an attention the agent is past (a message came in after it: the turn's state, no reason). A host only
// clears its notices when a message comes through it; one sent another way leaves them standing.
func (n *Node) hostNotices(id string, s *proto.Session, a host.Agent) {
	if a.Failure == "" && a.AttentionAt.IsZero() {
		return
	}
	var promptAt time.Time
	if p := n.transcriptPath(id); p != "" {
		if st, err := os.Stat(p); err == nil {
			promptAt = n.meta(p, st).lastPromptAt
		}
	}
	if a.Failure != "" && (a.FailureAt.IsZero() || !promptAt.After(a.FailureAt)) {
		s.FailureReason = a.Failure
	}
	if !a.AttentionAt.IsZero() && promptAt.After(a.AttentionAt) && s.Status == proto.StatusBlocked {
		s.Status = a.TurnStatus
	}
}

// sessionList returns live herdr sessions plus recent offline transcripts.
func (n *Node) sessionList(ctx context.Context, refreshAgents bool) []proto.Session {
	if refreshAgents {
		_ = n.refreshHosts(ctx)
	}
	n.mu.Lock()
	agents := make(map[string]host.Agent, len(n.agents))
	for k, v := range n.agents {
		agents[k] = v
	}
	onList := n.onAgentsList // replaced whole with n.agents (setAgentsAt), never changed in place
	n.mu.Unlock()

	var out []proto.Session
	seen := map[string]bool{}
	livePanes := map[string]bool{} // the resting Claudes' panes, whose screens are read for a dialog
	now := time.Now()
	for id, a := range agents {
		if !onList[id] { // a pane on Claude's agents list stays idle: the transcript is its parked session's (claudebg.go)
			a.AgentStatus = n.claudeStatus(id, a, now)
		}
		s := proto.Session{
			SessionID: id, Cwd: a.Cwd, Status: a.AgentStatus, PaneID: a.PaneID, TabID: a.TabID,
			WorkspaceID: a.WorkspaceID, AgentName: a.Name, Title: a.Title, Agent: a.Agent,
			ExternalURL: a.ExternalURL, Chat: a.Chat, Topic: a.Topic, Dispatched: a.Dispatched,
		}
		viaBotmux := host.ParsePane(a.PaneID).Kind == host.Botmux
		switch {
		case viaBotmux && (a.Agent == "codex" || a.Agent == traexAgent):
			// The thread's name, else botmux's title of the topic; messages go through botmux, neither route.
			if t := n.codexTitle(id); a.Agent == "codex" && t != "" {
				s.Title = t
			} else if t := n.traexTitle(id); a.Agent == traexAgent && t != "" {
				s.Title = t
			}
		case a.Agent == "codex":
			s.Title = n.codexTitle(id)                        // the terminal title is "dots | codex | Ready"
			s.CodexRoute, s.CodexRouteNote = n.codexRoute(id) // codex_daemon.go
		case a.Agent == traexAgent:
			s.Title = n.traexTitle(id) // the thread's name; the terminal title adds " | <directory>"
			s.CodexRoute, s.CodexRouteNote = codexRouteTerminal, "TRAEX 没有共享的 app-server"
		}
		n.placeIn(&s, a)
		s.Ports = n.portsOf(a.PaneID)
		if a.Agent == "codex" && s.Status == proto.StatusWorking && n.codexTurnOver(id, time.Now()) {
			s.Status = proto.StatusDone
		}
		n.hostNotices(id, &s, a)
		n.mu.Lock()
		if n.driving[id] && s.Status == proto.StatusBlocked {
			s.Status = proto.StatusIdle
		}
		n.mu.Unlock()
		if onList[id] { // its pane is on Claude's agents list: shown in its tab, picked from in its terminal (claudebg.go)
			s.Capabilities, s.ReadOnlyReason = &proto.SessionCapabilities{Read: true, Terminal: true}, claudeAgentsList
		}
		if s.Status == proto.StatusBlocked { // only while it still waits (not once the phone drives it)
			s.AttentionReason = a.Attention
		}
		// Resting is what its host says, before a lost first message marks the row blocked: the dialog that kept the
		// message out is read and named, not only the generic sentence.
		resting := restingClaude(a, s.Status)
		if why := n.lostFirstPrompt(id, s.Status); why != "" { // its first message did not go in (firstprompt.go)
			s.Status, s.AttentionReason = proto.StatusBlocked, why
		}
		if resting && !onList[id] && !n.isDriving(id) {
			livePanes[a.PaneID] = true
			if head := n.dialogOn(id, a.PaneID); head != "" { // a dialog holds its terminal (dialogstatus.go)
				s.Status, s.AttentionReason = proto.StatusBlocked, head
			}
		}
		if !onList[id] { // the list's prompt is no suggestion of its
			s.Suggestion = n.noteStatus(id, a.Agent, a.PaneID, s.Status) // suggestion.go
		}
		// textAt: when the transcript last had the agent's words; rollout: a Codex pane's rollout meta, for its effort.
		var textAt, promptAt time.Time
		var rollout quickMeta
		if p := n.transcriptPath(id); p != "" {
			if st, err := os.Stat(p); err == nil {
				m := n.meta(p, st)
				textAt = m.lastTextAt
				rollout = m
				if m.title != "" && a.Agent != "codex" && a.Agent != traexAgent {
					s.Title = m.title
				}
				if s.Title == "" || s.Title == "Claude Code" {
					s.Title = ""
					untitled(&s, m)
				}
				s.Model, s.LastText, s.UpdatedAt, s.Rev = m.model, snippet(m.lastText), lastUsed(m, st), st.Size()
				s.AgentSurface = m.surface
				s.Asks, s.AsksRead = m.asks, true // the list's last_text is only the reply's start (#5096)
				promptAt = m.lastPromptAt
				if a.Agent != "codex" && a.Agent != traexAgent {
					s.Model = n.shownModel(id, m.model) // the pick made in the app until a reply shows the real one (model_switch.go)
				}
				m.turnErr.set(&s)
				s.Activity, s.ActivityAt = m.activityOf()
				setContext(&s, m)
				s.ModelFallback = m.modelFallback
				if a.Agent != "codex" && a.Agent != traexAgent {
					s.PermissionMode, s.Effort = n.currentModes(id, m)
				}
				s.WokeByTask = m.wake
				if s.Status == proto.StatusWorking || s.Status == proto.StatusBlocked {
					s.Queued = m.queue // an idle agent has taken everything; stale entries are from outside the tail
				}
				// Background shells keep running after the turn ends; background subagents are listed as subagents.
				n.setBackground(&s, m)
				s.BackgroundWaiting = n.backgroundWaits(&s) // bgwait.go (#6045)
				s.Loop, s.Goal, s.Plan = m.loop, m.goal, m.plan
				s.Subagents = n.subagents(id)
			}
		}
		// The host's record of the answer (botmux's reply in the topic), unless the transcript has words well after it:
		// the session went on outside the host (a `claude --resume` in a terminal). Text written in the same turn
		// right after the reply (the sentinel, a closing line) comes within minutes.
		if a.LastText != "" && (a.LastTextAt.IsZero() || !textAt.After(a.LastTextAt.Add(hostTextSlack))) {
			s.LastText = snippet(a.LastText)
			// A message to the agent after the host's answer answered its question (#5096); a host that gives no time
			// for its answer is taken as the latest word.
			if !a.LastTextAt.IsZero() && promptAt.After(a.LastTextAt) {
				s.Asks = ""
			} else {
				s.Asks = transcript.AskOf(a.LastText)
			}
			s.AsksRead = true
		}
		if a.Agent == "codex" {
			n.codexEffortRow(id, &s.Efforts, &s.Effort, rollout) // joined on its app-server: its effort (codex_effort.go)
		}
		if a.Agent == piAgent {
			n.mergePiRow(&s) // its conversation is the extension's (pi.go)
		}
		switch {
		case a.Agent == opencodeAgent:
			s.Title = opencodeTitle(a.Title)
			n.mergeOpencodeRow(&s) // its conversation is the plugin's (opencode.go)
		case otherAgent(a):
			s.ReadOnlyReason = otherNoReader(a.Agent)
		}
		if n.codexBlocked(id) {
			markCodexConflict(&s)
		}
		n.decorate(&s)
		out = append(out, s)
		seen[id] = true
	}

	n.forgetDialogs(livePanes)

	for _, s := range n.starts.listed(seen) { // starting.go
		n.decorate(&s)
		out = append(out, s)
		seen[s.SessionID] = true
	}
	for _, s := range n.runtimes.sessions() { // runtimes (runtimes.go): listed as they say
		if !seen[s.SessionID] {
			if n.runtimeAssistant(s.SessionID) { // the assistant keeps its mode (assistant_runtime.go)
				if m := n.assistantModeOf(s.SessionID); m != "" {
					s.PermissionMode = m
				}
			}
			// A conversation in a Claude transcript here (runtimetranscript.go): what it says, and its own row goes.
			if t := n.mergeRuntimeTranscript(&s); t != "" {
				seen[t] = true
			}
			out = append(out, s)
			seen[s.SessionID] = true
		}
	}
	for _, s := range n.assistantSessions() {
		if !seen[s.SessionID] {
			out = append(out, s)
			seen[s.SessionID] = true
		}
	}
	for _, s := range n.claudeSessions(n.liveClaudes(), seen) { // claudes outside Persona's panes (claudebg.go)
		out = append(out, s)
		seen[s.SessionID] = true
	}
	for _, s := range n.codexSessions(seen) {
		out = append(out, s)
	}
	for _, s := range n.codexAssistantsAtRest(seen) {
		out = append(out, s)
		seen[s.SessionID] = true
	}
	offlineRows := n.offline(seen)
	n.markInterrupted(offlineRows, seen) // the restart cut these off (#1917)
	for _, s := range offlineRows {
		n.decorate(&s)
		out = append(out, s)
	}
	// Terminal-owned Codex parents were already seen before history was added.
	resolveCodexHierarchy(out)
	var keptSessions []proto.Session
	for _, s := range out {
		if !n.sessionScopeAllows(s.Cwd) {
			continue
		}
		if s.Cwd != "" && n.cwdBlocked(s.Cwd) && !n.isAssistantSession(s.SessionID) && !n.keptSessions()[s.SessionID] {
			continue
		}
		if s.Status == proto.StatusWorking && (s.Agent == "" || s.Agent == "claude" || s.Agent == "codex") {
			s.ToolLoop = n.toolLoopOf(s.SessionID)
		}
		s.WorkspaceCwd = n.sessionWorkspaceCwd(s.Cwd)
		s.CanFork = n.forkable(s) // "Branch to New Chat" (fork.go)
		keptSessions = append(keptSessions, s)
	}
	out = keptSessions
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (n *Node) decorate(s *proto.Session) {
	s.AlsoOpenIn = n.alsoOpenIn(s.SessionID) // live in another host too (ownership.go)
	if s.PaneID != "" && s.Host == "" {
		s.Host = host.ParsePane(s.PaneID).Kind // the host it runs in (#2576)
	}
	if st, ok := n.state.get(s.SessionID); ok {
		s.StartedByPersona = true
		s.Launcher = st.Launcher
		s.Compaction = st.Compaction
		if s.Cwd == "" {
			s.Cwd = st.Cwd
		}
	}
	if s.Launcher == "" {
		s.Launcher = n.inferLauncher(s.SessionID)
	}
	if s.Title == "" {
		s.Title = filepath.Base(s.Cwd)
	}
	n.ultracodeRow(s)
}

func snippet(s string) string {
	const most = 200
	s = wire.AssistantCardText(s)
	// One pass instead of strings.Fields, Join and []rune over the whole reply: runs of white space become one space,
	// leading and trailing space go, and the walk stops once there is more than most runes to show.
	var out strings.Builder
	out.Grow(min(len(s), most*utf8.UTFMax))
	runes, space := 0, false
	for i, r := range s {
		if unicode.IsSpace(r) {
			space = out.Len() > 0
			continue
		}
		if space {
			if runes == most {
				return truncatedSnippet(out.String())
			}
			out.WriteByte(' ')
			runes++
			space = false
		}
		if runes == most {
			return truncatedSnippet(out.String())
		}
		_, n := utf8.DecodeRuneInString(s[i:])
		out.WriteString(s[i : i+n])
		runes++
	}
	return out.String()
}

// truncatedSnippet ends a cut snippet with "…". Like the string([]rune) it replaces, a cut snippet has each
// invalid UTF-8 byte as U+FFFD; an uncut one keeps the bytes it had.
func truncatedSnippet(s string) string {
	if !utf8.ValidString(s) {
		s = string([]rune(s))
	}
	return s + "…"
}

// pollAgents refreshes the hosts' state every second (only the ones due) and pushes sessions.changed when anything
// visible changes. Each watcher host is snapshotted on its own stream's changes and at most every slowSnapshot while
// its stream is live, independently of the other hosts (the combined engine): a herdr-core event never snapshots
// herdr and vice versa.
func (n *Node) pollAgents(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last := map[string]time.Time{} // host kind → its last snapshot
	var listed time.Time
	for {
		sigs := n.signalSnapshot()
		kicked, ok := nextChange(ctx, t.C, sigs, listed)
		if !ok {
			return
		}
		var due []host.Host
		for _, h := range n.hosts.All() {
			if n.hostDue(h.Kind(), last[h.Kind()], kicked[h.Kind()]) {
				due = append(due, h)
			}
		}
		// The transcripts (revs) are checked every tick regardless; a host that does not answer (not installed or not
		// running) does not stop the list: runtimes, the assistant and the transcripts do not need it. Its agents stay
		// as last seen (refreshHosts).
		if len(due) > 0 {
			_ = n.refreshHosts(ctx, due...)
			now := time.Now()
			for _, h := range due {
				last[h.Kind()] = now
			}
		}
		list := n.sessionList(ctx, false)
		listed = time.Now()
		if revs := n.publish(list); len(revs) > 0 {
			n.kickPorts() // a tool call ran: it may have started (or stopped) a server (ports.go)
		}
	}
}

// publish pushes a rebuilt list to the hub: the whole list when anything it shows changed, else only the revs that
// moved (a transcript grew: a few bytes instead of the list, so the app can pull those sessions). It returns the
// revs that moved. Runtimes (runtimes.go) publish through it too when their list changes. Nothing goes while the hub
// has yet to say whether botmux's sessions are listed (botmuxPending).
func (n *Node) publish(list []proto.Session) map[string]int64 {
	if n.botmuxPending() {
		return nil
	}
	n.mu.Lock()
	size := len(n.lastList)
	n.mu.Unlock()
	key := listKeySized(list, size)
	n.mu.Lock()
	changed := key != n.lastList
	n.lastList = key
	revs := n.revsChanged(list)
	n.mu.Unlock()
	if n.peer == nil {
		return revs
	}
	if changed {
		_ = n.peer.Notify("sessions.changed", map[string]any{"sessions": list})
	} else if len(revs) > 0 {
		_ = n.peer.Notify("sessions.revs", map[string]any{"revs": revs})
	}
	return revs
}

// revsChanged records the list's revs and returns those that differ from the last pushed. Callers hold n.mu.
func (n *Node) revsChanged(list []proto.Session) map[string]int64 {
	var out map[string]int64
	next := make(map[string]int64, len(list))
	for _, s := range list {
		next[s.SessionID] = s.Rev
		if s.Rev != 0 && n.lastRevs[s.SessionID] != s.Rev {
			if out == nil {
				out = map[string]int64{}
			}
			out[s.SessionID] = s.Rev
		}
	}
	n.lastRevs = next
	return out
}
