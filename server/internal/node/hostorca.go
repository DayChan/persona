package node

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
)

// orcaHost is the Orca desktop app (docs/plans/plan-orca-support.md "Orca source"), through its runtime's unix socket. A pane
// is one of Orca's terminals, its id Orca's pane key "<tabId>:<leafId>" — what worktree.ps names agents by and what
// Orca puts in the terminal's processes as ORCA_PANE_KEY; the terminal's handle and incarnation (what the runtime's
// terminal.* calls take) are looked up from it. Spaces are worktrees, slots their tabs.
//
// Phase 1: listing, state (Orca's own hooks, polled), plain-text reads, typing, keys, prompts, process → session,
// closing. No colors, no history, no live stream, no starting or renaming. The socket pushes nothing: no Watcher, the
// node polls every second.
//
// Orca writes whatever it is sent into the terminal, and pastes a prompt only when it recognises an agent in front:
// so every write that carries text first makes sure the agent is in front (front), and refuses with nothing typed
// when it cannot tell.
type orcaHost struct {
	c *orcart.Client
	// scan lists the agent processes in front in Orca's terminals, by pane key (scanOrcaProcs).
	scan func() (map[string][]orcaProc, error)

	mu       sync.Mutex
	terms    map[string]orcart.Terminal // pane key -> terminal, from the last terminal.list
	procs    map[string][]orcaProc
	procsErr error
	procsAt  time.Time
	failedAt time.Time // when the last terminal.list failed (failErr), for Agents right after a failed Snapshot
	failErr  error
	// The layouts (tabs) are costly for Orca: asked for only when the terminals or a worktree's topology changed, or
	// every orcaLayoutMaxAge.
	tabs     []orcart.Tab
	tabsAt   time.Time
	topology string // the terminal set and topology revisions the tabs were read for
	// worktree.ps answers "unchanged" to its last snapshot id: the worktrees are kept from the last full answer.
	trees    []orcart.Worktree
	treesID  string
	fallback map[string]orcaFallback // pane key -> terminal.agentStatus, for agent panes without a row
	view     *orcaView               // the last look, reused by Agents right after a Snapshot
	viewAt   time.Time
	launches map[string]orcaLaunch // fresh panes whose shell may receive one launch command
}

var (
	_ host.Host        = (*orcaHost)(nil)
	_ host.PlainScreen = (*orcaHost)(nil)
	_ host.Organizer   = (*orcaHost)(nil)
	_ host.Launcher    = (*orcaHost)(nil)
)

func newOrcaHost(c *orcart.Client) *orcaHost { return &orcaHost{c: c, scan: orcaScan} }

// orcaScan is how a new Orca host lists agent processes (tests swap in a fake process table).
var orcaScan = scanOrcaProcs

// Orca's pane variables in the processes of its terminals: the pane key, and the alias Claude Code does not scrub.
const (
	orcaPaneEnv       = nodeclient.OrcaPaneEnv
	orcaLegacyPaneEnv = nodeclient.OrcaLegacyPaneEnv
)

const (
	// orcaProcsMaxAge: how long one scan of the processes serves the listing (it polls every second).
	orcaProcsMaxAge = 1500 * time.Millisecond
	// orcaLayoutMaxAge: the layouts are read again at least this often (a tab renamed changes no revision).
	orcaLayoutMaxAge = 30 * time.Second
	// orcaFallbackMaxAge: how long a terminal.agentStatus answer serves a pane without a worktree.ps row.
	orcaFallbackMaxAge = 3 * time.Second
	// orcaViewReuse: Agents right after a Snapshot reuses its look instead of asking Orca again.
	orcaViewReuse = 500 * time.Millisecond
	// orcaRowSlack: a worktree.ps row last updated this long before its pane's agent started is the previous agent's.
	orcaRowSlack = 2 * time.Second
)

func (h *orcaHost) Kind() string      { return host.Orca }
func (h *orcaHost) PaneEnv() string   { return orcaPaneEnv }
func (h *orcaHost) PlainScreen() bool { return true }

func (h *orcaHost) Version(ctx context.Context) string { return h.c.Version(ctx) }

// orcaErr marks Orca not running as host.ErrNotRunning: it then has no terminals.
func orcaErr(err error) error {
	if errors.Is(err, orcart.ErrNotRunning) {
		return host.Mark(err, host.ErrNotRunning)
	}
	return err
}

// terminals are this machine's terminals in Orca (it also lists terminals on SSH targets, which are not here), with
// the layout's tabs (read again only when something changed). A list that failed a moment ago fails again at once:
// the node asks for Agents right after a failed Snapshot, and a wedged Orca must not cost it twice.
func (h *orcaHost) terminals(ctx context.Context) ([]orcart.Terminal, []orcart.Tab, error) {
	h.mu.Lock()
	if h.failErr != nil && time.Since(h.failedAt) < time.Second {
		err := h.failErr
		h.mu.Unlock()
		return nil, nil, err
	}
	h.mu.Unlock()
	l, err := h.c.Terminals(ctx, false)
	if err == nil {
		key := orcaTopology(l)
		h.mu.Lock()
		stale := key != h.topology || time.Since(h.tabsAt) > orcaLayoutMaxAge
		h.mu.Unlock()
		if stale {
			if l, err = h.c.Terminals(ctx, true); err == nil {
				h.mu.Lock()
				h.tabs, h.tabsAt, h.topology = l.Tabs, time.Now(), orcaTopology(l)
				h.mu.Unlock()
			}
		}
	}
	if err != nil {
		err = orcaErr(err)
		h.mu.Lock()
		h.failedAt, h.failErr = time.Now(), err
		h.mu.Unlock()
		return nil, nil, err
	}
	var terms []orcart.Terminal
	byPane := map[string]orcart.Terminal{}
	for _, t := range l.Terminals {
		if t.IsLocal() && !t.Orphaned && t.TabID != "" && t.LeafID != "" {
			terms = append(terms, t)
			byPane[t.PaneKey()] = t
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failErr, h.terms = nil, byPane
	return terms, h.tabs, nil
}

// orcaTopology is what decides whether the layouts must be read again: the terminals (by pane) and each worktree's
// topology revision.
func orcaTopology(l *orcart.TerminalList) string {
	var parts []string
	for _, t := range l.Terminals {
		parts = append(parts, t.WorktreeID+"|"+t.PaneKey())
	}
	for _, w := range slices.Sorted(maps.Keys(l.TopologyRevisions)) {
		parts = append(parts, w+"#"+strconv.FormatInt(l.TopologyRevisions[w], 10))
	}
	slices.Sort(parts)
	return strings.Join(parts, "\n")
}

// worktrees is worktree.ps, the last full answer when Orca says nothing changed since. An error leaves the rows out:
// the panes are still listed, their states from the fallback.
func (h *orcaHost) worktrees(ctx context.Context) []orcart.Worktree {
	h.mu.Lock()
	after := h.treesID
	h.mu.Unlock()
	trees, id, unchanged, err := h.c.Worktrees(ctx, after)
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case err != nil:
		h.treesID = ""
		return nil
	case unchanged:
		h.treesID = id
	default:
		h.trees, h.treesID = trees, id
	}
	return h.trees
}

// orcaView is Orca's state at one moment: its terminals and the agents its hooks report, by pane key.
type orcaView struct {
	terms []orcart.Terminal
	tabs  []orcart.Tab
	trees []orcart.Worktree
	rows  map[string]orcart.AgentRow
	procs map[string][]orcaProc
	// procsKnown: the process scan worked (else the agent rows and Orca's agentIdentity are all there is).
	procsKnown bool
	// fallback: terminal.agentStatus of agent panes without a usable row.
	fallback map[string]string
}

func (h *orcaHost) look(ctx context.Context) (*orcaView, error) {
	terms, tabs, err := h.terminals(ctx)
	if err != nil {
		return nil, err
	}
	v := &orcaView{terms: terms, tabs: tabs, rows: map[string]orcart.AgentRow{}, fallback: map[string]string{}}
	v.trees = h.worktrees(ctx)
	for _, w := range v.trees {
		for _, a := range w.Agents {
			v.rows[a.PaneKey] = a
		}
	}
	procs, err := h.agentProcs(orcaProcsMaxAge)
	v.procs, v.procsKnown = procs, err == nil
	// Agent panes Orca's hooks have no (current) row for: Orca's own reading of the terminal.
	for _, t := range terms {
		if a := v.agent(t); a.Agent != "" && a.AgentStatus == proto.StatusUnknown {
			if s := h.fallbackStatus(ctx, t); s != "" {
				v.fallback[t.PaneKey()] = s
			}
		}
	}
	h.mu.Lock()
	h.view, h.viewAt = v, time.Now()
	h.mu.Unlock()
	return v, nil
}

// orcaFallback is one terminal.agentStatus answer, as the node's status.
type orcaFallback struct {
	status string
	at     time.Time
}

// orcaAgentStatus maps terminal.agentStatus's states to the node's.
var orcaAgentStatus = map[string]string{"working": proto.StatusWorking, "permission": proto.StatusBlocked, "idle": proto.StatusDone}

// fallbackStatus is a pane's state from terminal.agentStatus (cached a few seconds), "" when Orca does not know.
func (h *orcaHost) fallbackStatus(ctx context.Context, t orcart.Terminal) string {
	key := t.PaneKey()
	h.mu.Lock()
	f, ok := h.fallback[key]
	h.mu.Unlock()
	if ok && time.Since(f.at) < orcaFallbackMaxAge {
		return f.status
	}
	st, err := h.c.AgentStatus(ctx, t.Target())
	f = orcaFallback{at: time.Now()}
	if err == nil && st.IsRunningAgent {
		f.status = orcaAgentStatus[st.Status]
	}
	h.mu.Lock()
	if h.fallback == nil {
		h.fallback = map[string]orcaFallback{}
	}
	h.fallback[key] = f
	h.mu.Unlock()
	return f.status
}

func (h *orcaHost) Snapshot(ctx context.Context) (*host.Layout, error) {
	v, err := h.look(ctx)
	if err != nil {
		return nil, err
	}
	l := &host.Layout{}
	spaces := map[string]bool{}
	addSpace := func(id, path, branch, parent string) {
		if spaces[id] || id == "" {
			return
		}
		spaces[id] = true
		l.Spaces = append(l.Spaces, host.Space{ID: id, Label: orcaSpaceLabel(branch, path), Number: len(l.Spaces) + 1,
			Cwd: path, Branch: strings.TrimPrefix(branch, "refs/heads/"), ParentID: orcaParent(id, parent)})
	}
	// Orca's sidebar order (worktree.ps), then any worktree only the terminals name.
	for _, w := range v.trees {
		addSpace(w.WorktreeID, w.Path, w.Branch, w.ParentWorktreeID)
	}
	for _, t := range v.terms {
		addSpace(t.WorktreeID, t.WorktreePath, t.Branch, "")
	}
	perSpace := map[string]int{}
	seen := map[string]bool{}
	for _, tab := range v.tabs {
		if !spaces[tab.WorktreeID] || seen[tab.ID] {
			continue
		}
		seen[tab.ID] = true
		perSpace[tab.WorktreeID]++
		label := stripSpinner(tab.Title)
		if label == "" {
			label = strconv.Itoa(perSpace[tab.WorktreeID])
		}
		l.Slots = append(l.Slots, host.Slot{ID: tab.ID, SpaceID: tab.WorktreeID, Label: label, Number: perSpace[tab.WorktreeID]})
	}
	for _, t := range v.terms {
		a := v.agent(t)
		l.Panes = append(l.Panes, a)
		if a.Agent != "" {
			l.Agents = append(l.Agents, a)
		}
	}
	return l, nil
}

// Spaces reads the complete worktree catalog, including worktrees with no terminal.
// Unlike agent discovery's best-effort hook rows, catalog failures must reach the caller.
func (h *orcaHost) Spaces(ctx context.Context) ([]host.Space, error) {
	trees, _, _, err := h.c.Worktrees(ctx, "")
	if err != nil {
		return nil, orcaErr(err)
	}
	out := make([]host.Space, 0, len(trees))
	for _, w := range trees {
		if w.WorktreeID != "" {
			out = append(out, host.Space{ID: w.WorktreeID, Label: orcaSpaceLabel(w.Branch, w.Path),
				Number: len(out) + 1, Cwd: w.Path, Branch: strings.TrimPrefix(w.Branch, "refs/heads/"), ParentID: orcaParent(w.WorktreeID, w.ParentWorktreeID)})
		}
	}
	return out, nil
}

// orcaParent is a worktree's parent space, never itself.
func orcaParent(id, parent string) string {
	if parent == id {
		return ""
	}
	return parent
}

// orcaSpaceLabel names a worktree the way Orca's sidebar does: its branch, else its folder.
func orcaSpaceLabel(branch, path string) string {
	if b := strings.TrimPrefix(branch, "refs/heads/"); b != "" {
		return b
	}
	return firstOf(filepath.Base(path))
}

// Agents is the agent panes: from the look a Snapshot just took, else a new one.
func (h *orcaHost) Agents(ctx context.Context) ([]host.Agent, error) {
	h.mu.Lock()
	v := h.view
	if time.Since(h.viewAt) > orcaViewReuse {
		v = nil
	}
	h.mu.Unlock()
	if v == nil {
		var err error
		if v, err = h.look(ctx); err != nil {
			return nil, err
		}
	}
	var out []host.Agent
	for _, t := range v.terms {
		if a := v.agent(t); a.Agent != "" {
			out = append(out, a)
		}
	}
	return out, nil
}

func (h *orcaHost) Agent(ctx context.Context, pane host.PaneRef) (*host.Agent, error) {
	v, err := h.look(ctx)
	if err != nil {
		return nil, err
	}
	for _, t := range v.terms {
		if t.PaneKey() == pane.ID {
			a := v.agent(t)
			return &a, nil
		}
	}
	return nil, fmt.Errorf("%w %s", errNoOrcaTerminal, pane.ID)
}

// orcaAgents names the agents Orca reports (agentType, agentIdentity) the way the node does.
var orcaAgents = map[string]string{"claude": "claude", "codex": "codex"}

// orcaStatus maps Orca's agent states to the node's: waiting is Orca's word for an agent waiting on the user, as
// blocked is.
var orcaStatus = map[string]string{
	"working": proto.StatusWorking, "blocked": proto.StatusBlocked, "waiting": proto.StatusBlocked, "done": proto.StatusDone,
}

// agent is a terminal as an agent pane. Its agent is the claude or codex process in front in it (by its
// ORCA_PANE_KEY; not suspended, not in the background, not inside a herdr nested in the terminal): Orca keeps an
// agent's row (and its identity on the terminal) after the agent exits, so neither alone says one runs there. Only
// when the processes could not be listed do the row and the identity decide (for listing; writes are refused then).
//
// Its state is the row's main agent's own (a claude whose turn ended while its background subagents run is done, as
// herdr would say), except that a row blocked by a subagent waiting on the user is blocked (AgentRow.PaneState), when
// the row is of that agent and not older than its process; else terminal.agentStatus's.
func (v *orcaView) agent(t orcart.Terminal) host.Agent {
	key := t.PaneKey()
	a := host.Agent{
		PaneID: host.PaneRef{Kind: host.Orca, ID: key}.String(), WorkspaceID: t.WorktreeID, TabID: t.TabID,
		Cwd: t.WorktreePath, Title: stripSpinner(t.Title), RawTitle: t.Title,
	}
	row, hasRow := v.rows[key]
	var started time.Time
	if v.procsKnown {
		if ps := v.procs[key]; len(ps) > 0 {
			a.Agent, a.ForegroundCwd, started = ps[0].agent, ps[0].cwd, ps[0].start
		}
	} else if k := orcaAgents[t.AgentIdentity]; k != "" && hasRow && row.HasLivePty && orcaAgents[row.AgentType] == k {
		a.Agent = k
	}
	if a.Agent == "" {
		return a
	}
	a.AgentStatus = proto.StatusUnknown
	current := hasRow && orcaAgents[row.AgentType] == a.Agent &&
		(started.IsZero() || row.UpdatedAt == 0 || time.UnixMilli(row.UpdatedAt).After(started.Add(-orcaRowSlack)))
	if s := orcaStatus[row.PaneState()]; current && s != "" {
		a.AgentStatus = s
	} else if s := v.fallback[key]; s != "" {
		a.AgentStatus = s
	}
	return a
}
