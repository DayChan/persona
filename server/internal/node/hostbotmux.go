package node

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// botmuxHost is botmux, the Feishu topic ↔ AI CLI bridge (docs/integrations/engines.md "botmux"): each topic is a session of a
// coding CLI that botmux runs (in tmux) and answers in Feishu. It is not an engine to choose: a node on a machine with
// botmux registers it next to the engine's hosts (nodeHosts). A pane is a botmux session, its id the botmux session
// id: for Claude Code also the Claude session id botmux starts claude with; for Codex and compatible variants the thread is found in
// their rollouts (botmuxThreads). Listing and state (one `botmux observe` for every daemon's sessions) and replies,
// which go to botmux's own delivery (POST /api/trigger on its dashboard: queued while a turn runs, a parked session
// resumed by botmux) — never typed into the pane, never a `claude --resume` of Persona's. No screens, keys, closing or
// renaming: those stay in Feishu and on the computer.
type botmuxHost struct {
	// run runs the botmux CLI (tests: a fake).
	run func(ctx context.Context, args ...string) ([]byte, error)
	// client posts to the dashboard.
	client *http.Client

	mu     sync.Mutex
	at     time.Time // when rows were observed
	rows   []botmuxRow
	err    error
	base   string // the dashboard on loopback, "" until asked
	token  string // its token: an admin credential, kept in memory only and never logged
	logged bool   // a failed observe was logged since the last good one
	logf   func(format string, args ...any)
	dryRun bool // botmux renders the turn and dispatches nothing (the live test)
	// dataDir is botmux's data directory (botmuxDataDir), "" for none (tests without one).
	dataDir string
	// bin is the botmux CLI run ("" in tests on a fake), and tooOld its botmuxStamp when it last printed its usage
	// instead of observing ("" otherwise): until bin changes, that answer holds for botmuxTooOldReuse.
	bin    string
	tooOld string
}

var (
	_ host.Host        = (*botmuxHost)(nil)
	_ host.PlainScreen = (*botmuxHost)(nil)
	_ host.TextTyper   = (*botmuxHost)(nil)
)

// botmuxPaneEnv is the variable botmux sets in the processes of a session: its id.
const botmuxPaneEnv = "BOTMUX_SESSION_ID"

// botmuxReuse: an observe answers every refresh for this long. botmux pushes nothing, so the node polls every second;
// one observe (all daemons at once, a Node.js CLI) took 0.3–0.9 s with ~500 sessions. A delivered message looks again
// at once.
const botmuxReuse = 5 * time.Second

// botmuxTooOldReuse: a botmux without `observe` prints its usage until it is upgraded, so that answer is reused while
// the CLI file is unchanged (botmuxStamp, a stat per refresh) and for at most this long: one run cost 0.6 s of CPU
// every 5 s for nothing (#5337). The upper bound covers an upgrade behind a launcher script that stays as it was.
const botmuxTooOldReuse = 10 * time.Minute

// botmuxBin is the botmux CLI, "" when not installed (tests: "", so the hosts of an engine stay as they were).
var botmuxBin = findBotmux

// findBotmux: botmux on PATH, else the launcher botmux's installer writes, ~/.botmux/bin/botmux — the resident node
// (launchd) has only the system's PATH.
func findBotmux() string {
	if p, err := exec.LookPath("botmux"); err == nil {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".botmux", "bin", "botmux")
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// nodeHosts are the hosts a node drives: its engine's, and botmux's when botmux is installed and wanted (useBotmux).
func nodeHosts(engine string, botmux bool, logf func(format string, args ...any)) []host.Host {
	return nodeHostsFor(proto.EngineHostsOf(engine), botmux, logf)
}

// nodeHostsFor are a set's hosts, and botmux's as for nodeHosts.
func nodeHostsFor(c proto.EngineChoice, botmux bool, logf func(format string, args ...any)) []host.Host {
	hs := engineHostsFor(c, logf)
	// tmux is not an engine to choose: it lists nothing and opens nothing. It is how a pane a Claude Code waits in is
	// read and answered (#2562), so it is always there, next to whichever engine the machine chose.
	hs = append(hs, newTmuxHost())
	if !botmux {
		return hs
	}
	if bin := botmuxBin(); bin != "" {
		hs = append(hs, newBotmux(bin, logf))
	}
	return hs
}

// newBotmux makes the botmux host (tests: one on a fake CLI).
var newBotmux = func(bin string, logf func(format string, args ...any)) host.Host { return newBotmuxHost(bin, logf) }

// useBotmux lists this machine's botmux sessions, or stops (hub.json machines[].botmux): the hub's word in
// node.configure, or on as before when the hub lists sessions without having said (fallback: a hub older than the
// setting). Until one of them, botmux is not a host, so a machine turned off never runs botmux at all, not even while
// the node starts (and no list goes to the hub meanwhile: botmuxPending). Off, botmux is no host: no new `botmux
// observe`, no `botmux dashboard current` (one observe under way finishes, its answer dropped), and its sessions are
// out of the next list pushed, within a second; on, they come with the next refresh. A fallback never overrides the
// hub's word.
func (n *Node) useBotmux(on, fallback bool) {
	n.eng.mu.Lock()
	if n.eng.botmux != nil && (fallback || *n.eng.botmux == on) {
		n.eng.mu.Unlock()
		return
	}
	n.eng.botmux = &on
	n.eng.botmuxSaid.Store(true)
	// The engine's hosts stay as they are (their watchers hold them); only botmux comes or goes.
	all := n.hosts.All()
	hs := slices.DeleteFunc(slices.Clone(all), func(h host.Host) bool { return h.Kind() == host.Botmux })
	removed := !on && len(hs) < len(all)
	switch bin := botmuxBin(); {
	case on && len(hs) < len(all): // registered already
	case on && bin != "":
		n.hosts.Set(append(hs, newBotmux(bin, n.logf))...)
	case removed:
		n.hosts.Set(hs...)
	}
	n.eng.mu.Unlock()
	if !removed {
		return
	}
	// Its sessions go now, not when the hosts next answer: a refresh nobody answers (herdr not running) keeps the
	// agents as they were. A refresh still asking botmux drops its answer (refreshHosts), so they do not come back.
	n.mu.Lock()
	n.engEpoch++
	delete(n.hostAgents, host.Botmux)
	for id, a := range n.agents {
		if host.ParsePane(a.PaneID).Kind == host.Botmux {
			delete(n.agents, id)
			delete(n.onAgentsList, id)
		}
	}
	n.mu.Unlock()
}

// botmuxPending: botmux is installed and the hub has not said yet whether its sessions are listed. No list goes to
// the hub until it has (publish): one without botmux's sessions would take them off the hub's list for the seconds
// before the word (node.info comes first), and their coming back would read as new turns. The hub lists the sessions
// right after its word.
func (n *Node) botmuxPending() bool {
	return !n.eng.botmuxSaid.Load() && botmuxBin() != ""
}

func newBotmuxHost(bin string, logf func(format string, args ...any)) *botmuxHost {
	return &botmuxHost{
		run: func(ctx context.Context, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, bin, args...).Output()
		},
		// A redirect is the dashboard's login page: the token is stale (Prompt asks for the current one).
		client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		logf:    logf,
		dataDir: botmuxDataDir(),
		bin:     bin,
	}
}

// botmuxStamp names the botmux CLI file's version: its resolved path, size and mtime ("" when it cannot be read). An
// upgrade replaces the file or points the link elsewhere.
func botmuxStamp(bin string) string {
	if bin == "" {
		return ""
	}
	p, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return ""
	}
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s\x00%d\x00%d", p, st.Size(), st.ModTime().UnixNano())
}

func (h *botmuxHost) Kind() string      { return host.Botmux }
func (h *botmuxHost) PaneEnv() string   { return botmuxPaneEnv }
func (h *botmuxHost) PlainScreen() bool { return true }
func (h *botmuxHost) CanTypeText() bool { return false }

// CanDriveScreen: botmux owns its sessions' terminals (errBotmuxScreen).
func (h *botmuxHost) CanDriveScreen() bool { return false }

func (h *botmuxHost) Version(context.Context) string { return "" }

// botmuxRow is one session of `botmux observe --include-raw` (schema 1): the canonical fields, and from the raw row
// the title and the attention botmux raised (`botmux send --attention`).
type botmuxRow struct {
	Identity struct {
		SessionID string `json:"sessionId"`
		LarkAppID string `json:"larkAppId"`
	} `json:"identity"`
	CLI struct {
		ID string `json:"id"`
	} `json:"cli"`
	Backend struct {
		Type string `json:"type"`
	} `json:"backend"`
	Liveness         string `json:"liveness"`
	Turn             string `json:"turn"`
	Closed           any    `json:"closed"` // true, false or "unknown"
	TUIPromptActive  bool   `json:"tuiPromptActive"`
	LastActivityAt   int64  `json:"lastActivityAt"` // epoch ms
	WorkingDirectory string `json:"workingDirectory"`
	Raw              struct {
		Title          string `json:"title"`
		AgentAttention *struct {
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
			At     int64  `json:"at"` // epoch ms
		} `json:"agentAttention"`
		// FeishuThreadLink opens the session's topic in Feishu (a raw field: shown when present).
		FeishuThreadLink string `json:"feishuThreadLink"`
		// The last reply botmux posted in the topic (the full text and its first words) and whether it answers the
		// latest message ("replied") or that one still waits ("waiting").
		PreviewBotFullText string `json:"previewBotFullText"`
		PreviewBotText     string `json:"previewBotText"`
		PreviewBotState    string `json:"previewBotState"`
		PreviewBotAt       int64  `json:"previewBotAt"` // epoch ms
		// The Feishu chat and the topic's root message the session answers in (a dispatched topic's root is the key
		// of its dispatch, botmuxDispatched).
		ChatID        string `json:"chatId"`
		RootMessageID string `json:"rootMessageId"`
	} `json:"raw"`
}

// observe is every daemon's sessions, reused for botmuxReuse. botmux not installed or no daemon running is
// host.ErrNotRunning: no sessions. Any other failure (a timeout, a CLI error) is not: the node keeps the sessions it
// listed last, so a reply in the gap does not take them for not running (and resume one outside botmux).
func (h *botmuxHost) observe(parent context.Context) ([]botmuxRow, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.at.IsZero() && time.Since(h.at) < botmuxReuse {
		return h.rows, h.err
	}
	stamp := botmuxStamp(h.bin)
	if !h.at.IsZero() && h.tooOld != "" && h.tooOld == stamp && time.Since(h.at) < botmuxTooOldReuse {
		return h.rows, h.err
	}
	h.tooOld = ""
	ctx, cancel := context.WithTimeout(parent, 10*time.Second) // 0.3 s as a rule; seconds on a machine under load
	defer cancel()
	h.rows, h.err = nil, nil
	out, err := h.run(ctx, "observe", "--include-raw")
	var doc struct {
		SchemaVersion int `json:"schemaVersion"`
		Daemons       []struct {
			Probe struct {
				Status string `json:"status"`
			} `json:"probe"`
			Sessions []botmuxRow `json:"sessions"`
		} `json:"daemons"`
	}
	switch {
	case errors.Is(err, exec.ErrNotFound):
		h.err = host.Mark(fmt.Errorf("botmux observe: %w", err), host.ErrNotRunning)
	case err != nil:
		h.err = fmt.Errorf("botmux observe: %w", err)
	case !json.Valid(out):
		// A botmux from before `observe` prints its usage instead (and exits 0): it cannot list sessions here, which is
		// botmux not running for the node, with what to do about it.
		h.err = host.Mark(errors.New("botmux is too old to list its sessions (no `botmux observe`): run `botmux upgrade`"),
			host.ErrNotRunning)
		h.tooOld = stamp
	case json.Unmarshal(out, &doc) != nil || doc.SchemaVersion != 1:
		h.err = fmt.Errorf("botmux observe: unexpected output (schema %d)", doc.SchemaVersion)
	default:
		online := false
		for _, d := range doc.Daemons {
			if d.Probe.Status == "ok" {
				online = true
				h.rows = append(h.rows, d.Sessions...)
			}
		}
		if !online {
			h.err = host.Mark(errors.New("botmux: no daemon is running"), host.ErrNotRunning)
		}
	}
	h.at = time.Now()
	if parent.Err() != nil {
		h.at = time.Time{} // the caller gave up: says nothing of botmux
	}
	if h.err != nil && !h.logged && h.logf != nil {
		h.logf("botmux: %v", h.err)
	}
	h.logged = h.err != nil
	return h.rows, h.err
}

// botmuxTurns are botmux's turn states as the node's; the others (limited, stalled, interrupted: the turn ended
// without an answer) count as done, for the user to look at.
var botmuxTurns = map[string]string{
	"working": proto.StatusWorking, "starting": proto.StatusWorking, "analyzing": proto.StatusWorking,
	"idle": proto.StatusDone, "unknown": proto.StatusUnknown,
}

// botmuxCLIs are the CLIs whose botmux sessions are listed, as the node's agent kinds. Others (codex-app, which
// botmux drives through app-server and whose prompts carry no session id, pi, opencode…) are not.
var botmuxCLIs = map[string]string{"claude-code": "claude", "codex": "codex", "traex": traexAgent}

// agent is a session as an agent pane, false for the ones botmux does not drive here: closed ones, ones of a CLI not in
// botmuxCLIs, ones botmux runs in herdr (herdr's own panes already). A parked one is offline, so that a reply to it
// goes to botmux, which resumes it; Agents lists it only while its transcript is listed offline. A Claude Code session
// names its session (the botmux id); a Codex or a compatible CLI one is left for the node to find (botmuxThreads).
func (r botmuxRow) agent() (host.Agent, bool) {
	kind := botmuxCLIs[r.CLI.ID]
	if kind == "" || r.Backend.Type == "herdr" || r.Closed != false || !validID(r.Identity.SessionID) {
		return host.Agent{}, false
	}
	a := host.Agent{
		Agent: kind, Name: botmuxTitle(r.Raw.Title), Cwd: botmuxDir(r.WorkingDirectory), WorkspaceID: dirSpace + botmuxDir(r.WorkingDirectory),
		PaneID: host.PaneRef{Kind: host.Botmux, ID: r.Identity.SessionID}.String(),
	}
	if kind == "claude" {
		a.AgentSession = &host.AgentSession{Kind: "id", Value: r.Identity.SessionID}
	}
	switch {
	case r.Liveness == "not_running":
		a.AgentStatus = proto.StatusOffline
	case r.Liveness != "alive":
		return host.Agent{}, false
	default:
		a.TurnStatus = cmp.Or(botmuxTurns[r.Turn], proto.StatusDone)
		a.AgentStatus = a.TurnStatus
		at := r.Raw.AgentAttention
		switch {
		case r.TUIPromptActive:
			a.AgentStatus = proto.StatusBlocked
			if at != nil {
				a.Attention = botmuxTitle(at.Reason)
			}
		case at == nil || at.Kind == "":
		case botmuxFailure(at.Reason):
			// botmux's own notice that the turn failed and it stopped: nothing to answer, the turn is over.
			a.Failure = botmuxTitle(at.Reason)
			a.FailureAt = botmuxTime(at.At)
		default:
			a.AgentStatus = proto.StatusBlocked
			a.Attention, a.AttentionAt = botmuxTitle(at.Reason), botmuxTime(at.At)
		}
	}
	a.Title = a.Name
	// The answer is what botmux posted in the topic: the transcript's last words are often only botmux's sentinel. A
	// turn under way, or a message not answered yet, reads from the transcript (what it does now); so does a session
	// waiting for the user, whose reply in the topic is often the attention reason itself.
	if a.AgentStatus != proto.StatusWorking && a.AgentStatus != proto.StatusBlocked && r.Raw.PreviewBotState == "replied" {
		a.LastText = botmuxReply(cmp.Or(r.Raw.PreviewBotFullText, r.Raw.PreviewBotText))
		a.LastTextAt = botmuxTime(r.Raw.PreviewBotAt)
	}
	if strings.HasPrefix(r.Raw.FeishuThreadLink, "https://") {
		a.ExternalURL = r.Raw.FeishuThreadLink
	}
	a.Chat, a.Topic = r.Raw.ChatID, r.Raw.RootMessageID
	return a, true
}

// botmuxCard is how botmux records a card it posted: "[card: <title>]" (the Chinese word for card) on the first line, then the card's text.
var botmuxCard = lazyre.New(`^\[卡片: ([^\]\n]*)\]`)

// botmuxReply is a reply as botmux recorded it, a card's title without botmux's label (the first words are the summary).
func botmuxReply(s string) string { return botmuxCard.ReplaceAllString(s, "$1") }

// botmuxFailure: an attention botmux raised itself on a turn it stopped (a failure it did not continue, a crash loop
// it no longer restarts), not one an agent raised with `botmux send --attention`. botmux's notices start with a
// warning sign and say what it stopped doing on its own; an agent's reason may start with the sign too, so both count.
func botmuxFailure(reason string) bool {
	r := strings.TrimSpace(reason)
	if !strings.HasPrefix(r, "⚠") {
		return false
	}
	l := strings.ToLower(r)
	return slices.ContainsFunc(botmuxStopWords, func(w string) bool { return strings.Contains(l, w) })
}

// botmuxStopWords are in every notice botmux raises on a turn it stopped (its worker.*recovery*, crash and
// continuation texts, Chinese and English).
var botmuxStopWords = []string{"自动续跑", "自动恢复", "自动重启", "自动处理", "automatic", "auto-restart"}

// botmuxTime is an epoch-ms time, zero when botmux gave none.
func botmuxTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// botmuxTitle is a session's title on one line (botmux keeps the first words of the topic until it is renamed).
func botmuxTitle(s string) string { return strings.Join(strings.Fields(s), " ") }

func (h *botmuxHost) Agents(ctx context.Context) ([]host.Agent, error) {
	rows, err := h.observe(ctx)
	if err != nil {
		return nil, err
	}
	dispatched := botmuxDispatched(h.dataDir)
	var out []host.Agent
	for _, r := range rows {
		a, ok := r.agent()
		if ok && (a.AgentStatus != proto.StatusOffline || time.Since(time.UnixMilli(r.LastActivityAt)) <= offlineWindow) {
			a.Dispatched = dispatched[r.Identity.SessionID]
			out = append(out, a)
		}
	}
	return out, nil
}

// botmuxDataDir is where botmux keeps its state, as botmux resolves it: SESSION_DATA_DIR, else the directory
// ~/.botmux/.data-dir names, else ~/.botmux/data.
func botmuxDataDir() string {
	if d := os.Getenv("SESSION_DATA_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if b, err := os.ReadFile(filepath.Join(home, ".botmux", ".data-dir")); err == nil {
		if d := strings.TrimSpace(string(b)); filepath.IsAbs(d) {
			return d
		}
	}
	return filepath.Join(home, ".botmux", "data")
}

// botmuxDispatched is the topics each session opened with `botmux dispatch`, by session: orchestrate-dispatch.json,
// keyed by the dispatched topic's root message, names the session that dispatched it (orchSessionId, the
// --session-id). Nil when there is none or it cannot be read: nothing follows then.
func botmuxDispatched(dataDir string) map[string][]string {
	if dataDir == "" {
		return nil
	}
	path := filepath.Join(dataDir, "orchestrate-dispatch.json")
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	botmuxRegistry.Lock()
	defer botmuxRegistry.Unlock()
	if c := botmuxRegistry.c; c.path == path && c.size == st.Size() && c.mtime.Equal(st.ModTime()) {
		return c.by
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var reg map[string]struct {
		OrchSessionID string `json:"orchSessionId"`
	}
	if json.Unmarshal(b, &reg) != nil {
		return nil
	}
	out := map[string][]string{}
	for root, d := range reg {
		if d.OrchSessionID != "" && root != "" {
			out[d.OrchSessionID] = append(out[d.OrchSessionID], root)
		}
	}
	for _, roots := range out {
		slices.Sort(roots) // the same list each time (listKey)
	}
	botmuxRegistry.c = botmuxRegistryRead{path: path, size: st.Size(), mtime: st.ModTime(), by: out}
	return out
}

// botmuxRegistry is the dispatch registry last read: read again when botmux wrote it (Agents runs every second or so).
var botmuxRegistry struct {
	sync.Mutex
	c botmuxRegistryRead
}

type botmuxRegistryRead struct {
	path  string
	size  int64
	mtime time.Time
	by    map[string][]string
}

// Snapshot: a space per working directory (as herdr-core's terminals the Herdr app did not place), no slots.
func (h *botmuxHost) Snapshot(ctx context.Context) (*host.Layout, error) {
	agents, err := h.Agents(ctx)
	if err != nil {
		return nil, err
	}
	l := &host.Layout{Agents: agents, Panes: agents}
	seen := map[string]bool{}
	for _, a := range agents {
		if !seen[a.WorkspaceID] {
			seen[a.WorkspaceID] = true
			l.Spaces = append(l.Spaces, host.Space{ID: a.WorkspaceID, Label: filepath.Base(a.Cwd), Number: len(l.Spaces) + 1, Cwd: a.Cwd})
		}
	}
	botmuxSpaceLabels(l.Spaces)
	return l, nil
}

// botmuxDir is a working directory as one space: botmux keeps it as typed, with or without the trailing slash (#1836).
func botmuxDir(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Clean(dir)
}

// botmuxSpaceLabels names a space whose directory reads like its parent's, ignoring case ("Botmux/botmux"), by both:
// next to the parent's own space the two looked like one listed twice (#1836). The label hangs on the space's own path
// only, never on which other spaces are listed now, so pins, mutes and todos keyed by it stay put.
func botmuxSpaceLabels(spaces []host.Space) {
	for i, sp := range spaces {
		if parent := filepath.Base(filepath.Dir(sp.Cwd)); strings.EqualFold(parent, sp.Label) {
			spaces[i].Label = parent + "/" + sp.Label
		}
	}
}

// row is a session's row by id, listed or parked for longer (paneFor finds it to reply to).
func (h *botmuxHost) row(ctx context.Context, id string) (botmuxRow, host.Agent, error) {
	rows, err := h.observe(ctx)
	if err != nil {
		return botmuxRow{}, host.Agent{}, err
	}
	for _, r := range rows {
		if r.Identity.SessionID == id {
			if a, ok := r.agent(); ok {
				return r, a, nil
			}
		}
	}
	return botmuxRow{}, host.Agent{}, fmt.Errorf("botmux: no session %s", id)
}

func (h *botmuxHost) Agent(ctx context.Context, pane host.PaneRef) (*host.Agent, error) {
	_, a, err := h.row(ctx, pane.ID)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// errBotmuxScreen: botmux sessions are read through their transcripts; their terminals stay botmux's.
var errBotmuxScreen = fmt.Errorf("%w: botmux 会话的终端不能从手机读或按键，只能发消息", host.ErrUnsupported)

func (h *botmuxHost) Read(context.Context, host.PaneRef, host.ReadOpts) (string, error) {
	return "", errBotmuxScreen
}
func (h *botmuxHost) SendText(context.Context, host.PaneRef, string) error    { return errBotmuxScreen }
func (h *botmuxHost) SendKeys(context.Context, host.PaneRef, ...string) error { return errBotmuxScreen }

// PaneProcesses: none needed, the session id is botmux's (Agent.AgentSession).
func (h *botmuxHost) PaneProcesses(context.Context, host.PaneRef) ([]int, error) { return nil, nil }

// Prompt hands text to botmux as a turn of the session (POST /api/trigger, target kind turn with the session id,
// fire and forget): botmux queues it behind a running turn, resumes a parked session, and answers in the Feishu topic
// as for a message there. A refusal (4xx, or 503: the bot's daemon is not up) or a dashboard that cannot be reached
// delivered nothing; anything else may have.
func (h *botmuxHost) Prompt(ctx context.Context, pane host.PaneRef, text string) error {
	r, _, err := h.row(ctx, pane.ID)
	if err != nil {
		return host.Mark(err, host.ErrNotDelivered)
	}
	req := map[string]any{
		"source":      map[string]any{"type": "webhook", "connectorId": "persona"},
		"target":      map[string]any{"kind": "turn", "botId": r.Identity.LarkAppID, "sessionId": r.Identity.SessionID},
		"instruction": text,
		"envelope":    map[string]any{"format": "text", "sourceName": "Persona", "trusted": false},
	}
	if h.dryRun {
		req["options"] = map[string]any{"dryRun": true}
	}
	body, _ := json.Marshal(req)
	for attempt := 0; ; attempt++ {
		status, msg, err := h.trigger(ctx, body, attempt > 0)
		stale := err != nil || status == http.StatusUnauthorized || status == http.StatusForbidden ||
			(status >= 300 && status < 400)
		switch {
		case stale && attempt == 0:
			continue // the dashboard moved or its token was rotated: ask botmux for the current one
		case err != nil:
			return host.Mark(err, host.ErrNotDelivered)
		case status >= 200 && status < 300:
			h.mu.Lock()
			h.at = time.Time{} // its state changes now: look again at the next refresh
			h.mu.Unlock()
			return nil
		case status < 500 || status == http.StatusServiceUnavailable:
			return host.Mark(fmt.Errorf("botmux refused the message (%d): %s", status, msg), host.ErrNotDelivered)
		}
		return fmt.Errorf("botmux: delivering the message failed (%d): %s", status, msg)
	}
}

// trigger posts body to the dashboard's /api/trigger; fresh asks botmux for the dashboard again. err: nothing was
// delivered (no dashboard, no connection).
func (h *botmuxHost) trigger(ctx context.Context, body []byte, fresh bool) (int, string, error) {
	base, token, err := h.dashboard(ctx, fresh)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/trigger", bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "botmux_dashboard_token", Value: token})
	resp, err := h.client.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return 0, "", fmt.Errorf("botmux: the dashboard does not answer: %w", err)
		}
		return http.StatusBadGateway, err.Error(), nil // it may have arrived
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var res struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(b, &res)
	msg := strings.TrimSpace(res.Error + " " + res.Message)
	if resp.StatusCode < 300 && !res.OK {
		return http.StatusBadGateway, msg, nil // accepted, then failed: it may have run
	}
	return resp.StatusCode, msg, nil
}

// dashboard is botmux's dashboard on loopback and its token, from `botmux dashboard current` (which makes the first
// token when there is none), kept until fresh.
func (h *botmuxHost) dashboard(ctx context.Context, fresh bool) (string, string, error) {
	h.mu.Lock()
	base, token := h.base, h.token
	h.mu.Unlock()
	if base != "" && !fresh {
		return base, token, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := h.run(ctx, "dashboard", "current")
	if err != nil {
		return "", "", fmt.Errorf("botmux dashboard current: %w", err)
	}
	for _, f := range strings.Fields(string(out)) {
		u, err := url.Parse(f)
		if err != nil || u.Scheme != "http" || u.Query().Get("t") == "" || u.Port() == "" {
			continue
		}
		base = "http://" + net.JoinHostPort("127.0.0.1", u.Port())
		token = u.Query().Get("t")
		h.mu.Lock()
		h.base, h.token = base, token
		h.mu.Unlock()
		return base, token, nil
	}
	return "", "", errors.New("botmux dashboard current: no dashboard URL in its output")
}

// botmuxThreads finds the thread of each Codex and compatible variants session botmux lists (pane → the session id the node keys it
// on). botmux does not report it (it keeps the CLI's own id in its store), but it opens every prompt it hands these
// CLIs with <session_id>botmux id</session_id>, so the rollout whose head carries the session's id is its thread —
// the bridge botmux uses itself (Codex's history.jsonl, the compatible CLI's threads table: both say what the rollouts say). Only
// rollouts in the session's working directory are read, each head once; of several (a fork copies the head) the most
// recently written. A session whose rollout is not found (none yet, or a CODEX_HOME / TRAE_HOME of its own that this
// node does not know) is not listed.
func (n *Node) botmuxThreads(agents []host.Agent) map[string]string {
	var out map[string]string
	for _, kind := range []string{"codex", traexAgent} {
		cwds := map[string]bool{}
		var mine []host.Agent
		for _, a := range agents {
			if a.Agent == kind && a.Cwd != "" && host.ParsePane(a.PaneID).Kind == host.Botmux {
				mine = append(mine, a)
				cwds[filepath.Clean(a.Cwd)] = true
			}
		}
		if len(mine) == 0 {
			continue
		}
		homes := []string{n.traexHome()}
		if kind == "codex" {
			homes = n.botmuxCodexHomes()
		}
		type hit struct {
			home, thread string
			at           time.Time
		}
		best := map[string]hit{} // botmux id → its newest rollout
		for _, home := range homes {
			for _, r := range rollouts(home) {
				if r.sub || r.cwd == "" || !cwds[filepath.Clean(r.cwd)] {
					continue
				}
				for _, id := range botmuxIDsIn(r.path) {
					if h, ok := best[id]; !ok || r.mtime.After(h.at) {
						best[id] = hit{home, r.id, r.mtime}
					}
				}
			}
		}
		for _, a := range mine {
			h, ok := best[strings.ToLower(host.ParsePane(a.PaneID).ID)]
			if !ok {
				continue
			}
			id := h.thread
			if kind == "codex" {
				id = n.codexPublicID(h.home, id)
			}
			if out == nil {
				out = map[string]string{}
			}
			out[a.PaneID] = id
		}
	}
	return out
}

// botmuxCodexHomes are the Codex homes this node reads (codex_registry.go), ~/.codex among them.
func (n *Node) botmuxCodexHomes() []string {
	if n.codices == nil {
		return []string{n.codexHome()}
	}
	var homes []string
	for _, h := range append(n.codexInventory().Homes, n.codexHome()) {
		if h = canonicalCodexHome(h, n.home); h != "" && !slices.Contains(homes, h) {
			homes = append(homes, h)
		}
	}
	return homes
}

// botmuxHeadMax: how much of a rollout's head is read for botmux's session id. The first line (session_meta, tens of
// KB of instructions) comes before the first prompt, whose first block is the id.
const botmuxHeadMax = 512 << 10

// botmuxSessionRe is botmux's session block, as written or with JSON's escapes of < and >.
var botmuxSessionRe = lazyre.New(`(?:<|\\u003c)session_id(?:>|\\u003e)([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})(?:<|\\u003c)/session_id(?:>|\\u003e)`)

// botmuxHeads caches what each rollout's head says: read again only while the head is still being written.
var botmuxHeads = struct {
	sync.Mutex
	byPath map[string]botmuxHead
}{byPath: map[string]botmuxHead{}}

type botmuxHead struct {
	size int64
	ids  []string
}

// botmuxIDsIn is the botmux session ids in a rollout's head.
func botmuxIDsIn(path string) []string {
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	botmuxHeads.Lock()
	h, ok := botmuxHeads.byPath[path]
	botmuxHeads.Unlock()
	// The first prompt, once written, is the head for good: no need to read a rollout that has its id again.
	if ok && (len(h.ids) > 0 || h.size == st.Size() || h.size >= botmuxHeadMax) {
		return h.ids
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	head, _ := io.ReadAll(io.LimitReader(f, botmuxHeadMax))
	f.Close()
	h = botmuxHead{size: int64(len(head))}
	for _, m := range botmuxSessionRe.FindAllSubmatch(head, -1) {
		if id := strings.ToLower(string(m[1])); !slices.Contains(h.ids, id) {
			h.ids = append(h.ids, id)
		}
	}
	botmuxHeads.Lock()
	botmuxHeads.byPath[path] = h
	botmuxHeads.Unlock()
	return h.ids
}
