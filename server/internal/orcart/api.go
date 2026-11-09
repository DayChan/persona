package orcart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Terminal is one terminal of terminal.list (the fields Persona reads).
type Terminal struct {
	// Handle is the runtime's id for the terminal ("term_<uuid>"), what terminal.read / send / close take.
	Handle string `json:"handle"`
	// IncarnationID names the process behind the handle; a call fenced with it (Target) fails instead of reaching a
	// new process that took the same handle. "" from hosts that do not know it.
	IncarnationID string `json:"incarnationId"`
	// TabID and LeafID place it: a tab of a worktree, split into leaves (panes). PaneKey joins them.
	TabID        string `json:"tabId"`
	LeafID       string `json:"leafId"`
	WorktreeID   string `json:"worktreeId"`
	WorktreePath string `json:"worktreePath"`
	Branch       string `json:"branch"`
	Title        string `json:"title"`
	Connected    bool   `json:"connected"`
	Writable     bool   `json:"writable"`
	Orphaned     bool   `json:"orphaned"`
	// ExecutionHostID is where the terminal runs: "local" for this machine (others are SSH targets).
	ExecutionHostID string `json:"executionHostId"`
	// AgentIdentity is the agent Orca sees in the terminal ("claude", "codex"…), "" for none.
	AgentIdentity string `json:"agentIdentity"`
	LastOutputAt  int64  `json:"lastOutputAt"`
}

// PaneKey is the terminal's pane as Orca names it in worktree.ps and in the processes it runs (ORCA_PANE_KEY).
func (t Terminal) PaneKey() string { return t.TabID + ":" + t.LeafID }

// Local: the terminal runs on this machine.
func (t Terminal) Local() bool { return t.ExecutionHostID == "" || t.ExecutionHostID == "local" }

// IsLocal: alias for Local.
func (t Terminal) IsLocal() bool { return t.Local() }

// Target is the terminal a call acts on: its handle, fenced to the process it had when listed.
func (t Terminal) Target() Target { return Target{Handle: t.Handle, Incarnation: t.IncarnationID} }

// Target is a terminal handle and the incarnation it must still have.
type Target struct {
	Handle      string
	Incarnation string
}

func (t Target) params() map[string]any {
	p := map[string]any{"terminal": t.Handle}
	if t.Incarnation != "" {
		p["expectedIncarnationId"] = t.Incarnation
	}
	return p
}

// Tab is one tab as Orca's layout shows it.
type Tab struct {
	ID         string
	Title      string
	WorktreeID string
}

// TerminalList is terminal.list: the terminals, the tabs of each worktree's layout in order (only when asked for),
// and each worktree's layout revision (it changes when tabs or panes are added, moved, closed).
type TerminalList struct {
	Terminals []Terminal
	Tabs      []Tab
	// HasTabs: the layouts were asked for (Tabs is complete, possibly empty).
	HasTabs           bool
	TopologyRevisions map[string]int64
}

// Terminals is terminal.list, with the layouts (costly for Orca to build) only when layouts is true.
func (c *Client) Terminals(ctx context.Context, layouts bool) (*TerminalList, error) {
	var res struct {
		Terminals         []Terminal       `json:"terminals"`
		TopologyRevisions map[string]int64 `json:"topologyRevisions"`
		VisualLayouts     []struct {
			WorktreeID string     `json:"worktreeId"`
			Root       layoutNode `json:"root"`
		} `json:"visualLayouts"`
	}
	params := map[string]any{"includeVisualLayouts": layouts, "includePreview": false}
	if err := c.Call(ctx, ListTimeout, "terminal.list", params, &res); err != nil {
		return nil, err
	}
	out := &TerminalList{Terminals: res.Terminals, HasTabs: layouts, TopologyRevisions: res.TopologyRevisions}
	for _, l := range res.VisualLayouts {
		l.Root.tabs(l.WorktreeID, &out.Tabs)
	}
	return out, nil
}

// layoutNode is a node of a worktree's layout: a group of tabs, or a split of two nodes (first, then second).
type layoutNode struct {
	Type string `json:"type"`
	Tabs []struct {
		TabID string `json:"tabId"`
		Title string `json:"title"`
	} `json:"tabs"`
	First  *layoutNode `json:"first"`
	Second *layoutNode `json:"second"`
}

// tabs appends the node's tabs in reading order: a group's own, a split's first side, then its second.
func (n *layoutNode) tabs(worktree string, out *[]Tab) {
	if n == nil {
		return
	}
	for _, t := range n.Tabs {
		if t.TabID != "" {
			*out = append(*out, Tab{ID: t.TabID, Title: t.Title, WorktreeID: worktree})
		}
	}
	n.First.tabs(worktree, out)
	n.Second.tabs(worktree, out)
}

// Worktree is one worktree of worktree.ps.
type Worktree struct {
	WorktreeID    string `json:"worktreeId"`
	HostID        string `json:"hostId"`
	Path          string `json:"path"`
	Branch        string `json:"branch"`
	DisplayName   string `json:"displayName"`
	Repo          string `json:"repo"`
	RepoID        string `json:"repoId"`
	IsMain        *bool  `json:"isMainWorktree"`
	Unread        *bool  `json:"unread"`
	SidebarStatus string `json:"sidebarStatus"`
	WorkspaceKind string `json:"workspaceKind"`
	// ParentWorktreeID is the worktree this one is a child of in Orca's sidebar (its "N children"); empty for a top-level one.
	ParentWorktreeID string `json:"parentWorktreeId"`
	// Agents are the agents Orca's own hooks reported in the worktree's panes.
	Agents []AgentRow `json:"agents"`
}

// WorktreeHeads reads Git identity in one catalog request, without inspecting terminals.
// Old runtimes may not provide this optional display metadata.
func (c *Client) WorktreeHeads(ctx context.Context) (map[string]string, error) {
	var res struct {
		Worktrees []struct {
			ID   string `json:"id"`
			Head string `json:"head"`
		} `json:"worktrees"`
	}
	if err := c.Call(ctx, ListTimeout, "worktree.list", map[string]any{}, &res); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(res.Worktrees))
	for _, w := range res.Worktrees {
		out[w.ID] = w.Head
	}
	return out, nil
}

// AgentRow is one agent of worktree.ps: its pane, kind and state from Orca's hooks.
type AgentRow struct {
	PaneKey   string `json:"paneKey"`
	AgentType string `json:"agentType"`
	// State: working | blocked | waiting | done, folding in the agent's children (a settled agent with live subagents
	// reads working); MainAgent.State is the agent's own, when Orca has it.
	State     string `json:"state"`
	MainAgent *struct {
		State string `json:"state"`
	} `json:"mainAgent"`
	HasLivePty bool `json:"hasLivePty"`
	// ConversationName, DisplayName and TaskTitle name the agent's conversation (Orca's sidebar: the first one set);
	// Prompt is the first message, LastAssistantMessage its latest answer.
	ConversationName     string `json:"conversationName"`
	DisplayName          string `json:"displayName"`
	TaskTitle            string `json:"taskTitle"`
	Prompt               string `json:"prompt"`
	LastAssistantMessage string `json:"lastAssistantMessage"`
	// UpdatedAt and StateStartedAt are epoch milliseconds.
	UpdatedAt      int64 `json:"updatedAt"`
	StateStartedAt int64 `json:"stateStartedAt"`
}

// OwnState is the pane's agent's own state: its main agent's when known, else the row's (Orca's mainAgentState).
func (a AgentRow) OwnState() string {
	if a.MainAgent != nil && a.MainAgent.State != "" {
		return a.MainAgent.State
	}
	return a.State
}

// PaneState is the state the pane's row is listed with: blocked (or waiting) when the row's state says so, as one of
// its agents — the main one or a subagent — waits on the user then; else the main agent's own (OwnState), so a settled
// agent whose background subagents still run reads done, not working.
func (a AgentRow) PaneState() string {
	if a.State == "blocked" || a.State == "waiting" {
		return a.State
	}
	return a.OwnState()
}

// Worktrees is worktree.ps. after is the snapshot id of the last answer ("" for none): when nothing changed since,
// Orca says so (unchanged true, no worktrees) instead of sending them all again. snapshot is the id to pass next time
// ("" from hosts without snapshots).
func (c *Client) Worktrees(ctx context.Context, after string) (trees []Worktree, snapshot string, unchanged bool, err error) {
	var res struct {
		Worktrees  []Worktree `json:"worktrees"`
		SnapshotID string     `json:"snapshotId"`
		Unchanged  bool       `json:"unchanged"`
	}
	var a any // null: the first poll of a snapshot series
	if after != "" {
		a = after
	}
	err = c.Call(ctx, ListTimeout, "worktree.ps", map[string]any{"afterSnapshotId": a}, &res)
	return res.Worktrees, res.SnapshotID, res.Unchanged && after != "", err
}

// Screen is terminal.read with screen: the visible screen as plain text.
type Screen struct {
	Lines []string
	// Wrapped[i]: line i continues on the next one (a soft wrap).
	Wrapped []bool
	// Source is what was read ("screen"; "stream" is the output's tail, not the screen).
	Source string
}

// Text is the screen as text, soft-wrapped rows joined, one line per row, ending in a newline.
func (s Screen) Text() string {
	var b strings.Builder
	for i, l := range s.Lines {
		b.WriteString(l)
		if i < len(s.Wrapped) && s.Wrapped[i] && i+1 < len(s.Lines) {
			continue
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// ErrNoScreen: Orca has no screen for the terminal right now (terminal.read's source "screen-unavailable").
var ErrNoScreen = errors.New("orca has no screen for the terminal")

// Read is terminal.read {screen: true}: the terminal's visible screen, plain text.
func (c *Client) Read(ctx context.Context, t Target) (*Screen, error) {
	var res struct {
		Terminal struct {
			Tail     []string `json:"tail"`
			TailRows []struct {
				Wrapped bool `json:"wrapped"`
			} `json:"tailRows"`
			Source string `json:"source"`
		} `json:"terminal"`
	}
	p := t.params()
	p["screen"] = true
	if err := c.Call(ctx, CallTimeout, "terminal.read", p, &res); err != nil {
		return nil, err
	}
	if res.Terminal.Source == "screen-unavailable" {
		return nil, fmt.Errorf("orca terminal.read: %w", ErrNoScreen)
	}
	s := &Screen{Lines: res.Terminal.Tail, Source: res.Terminal.Source}
	for _, r := range res.Terminal.TailRows {
		s.Wrapped = append(s.Wrapped, r.Wrapped)
	}
	return s, nil
}

// AgentStatus is terminal.agentStatus: whether an agent Orca recognises is running in front in the terminal, and its
// state when Orca knows it ("working", "permission", "idle"; "" unknown).
type AgentStatus struct {
	IsRunningAgent bool   `json:"isRunningAgent"`
	Status         string `json:"status"`
}

// AgentStatus is terminal.agentStatus.
func (c *Client) AgentStatus(ctx context.Context, t Target) (*AgentStatus, error) {
	var res struct {
		AgentStatus *AgentStatus `json:"agentStatus"`
	}
	if err := c.Call(ctx, CallTimeout, "terminal.agentStatus", t.params(), &res); err != nil {
		return nil, err
	}
	if res.AgentStatus == nil {
		return nil, fmt.Errorf("orca terminal.agentStatus: no status in the answer")
	}
	return res.AgentStatus, nil
}

// ClientID is how Persona names itself to Orca as the driver of its input (terminal.send's client.id, required).
const ClientID = "persona"

// client is the driver terminal.send names: Persona as a desktop client (the only type Orca pastes agent prompts for).
func client() map[string]any { return map[string]any{"id": ClientID, "type": "desktop"} }

// ErrRefused: Orca accepted the request but wrote nothing (terminal.send's accepted false); Reason says why.
var ErrRefused = errors.New("orca refused the input")

// Refusal is a terminal.send Orca turned down with nothing written: Reason "no-agent" (no prompt-ready agent in
// front), "permission" (the agent waits on a permission dialog) or "not accepted" (input paused, a guard).
type Refusal struct{ Reason string }

func (r *Refusal) Error() string { return "orca: input refused: " + r.Reason }
func (r *Refusal) Unwrap() error { return ErrRefused }

// Sent is what terminal.send says it did.
type Sent struct {
	BytesWritten int
	// Prompt: Orca delivered the text as an agent prompt (its receipt); false when it wrote it raw.
	Prompt bool
}

// send is terminal.send; a refusal (nothing written) is a *Refusal.
func (c *Client) send(ctx context.Context, params map[string]any) (*Sent, error) {
	params["client"] = client()
	var res struct {
		Send struct {
			Accepted      *bool           `json:"accepted"`
			BytesWritten  int             `json:"bytesWritten"`
			RefusedReason string          `json:"refusedReason"`
			Prompt        json.RawMessage `json:"prompt"`
		} `json:"send"`
	}
	if err := c.Call(ctx, SendTimeout, "terminal.send", params, &res); err != nil {
		return nil, err
	}
	if a := res.Send.Accepted; a != nil && !*a {
		reason := res.Send.RefusedReason
		if reason == "" {
			reason = "not accepted"
		}
		return nil, &Refusal{Reason: reason}
	}
	p := res.Send.Prompt
	return &Sent{BytesWritten: res.Send.BytesWritten, Prompt: len(p) > 0 && string(p) != "null"}, nil
}

// Send is terminal.send of raw text, without enter: written to the terminal as is (key bytes too), no paste.
func (c *Client) Send(ctx context.Context, t Target, text string) error {
	p := t.params()
	p["text"] = text
	_, err := c.send(ctx, p)
	return err
}

// Prompt is terminal.send of text and enter as an agent prompt: Orca pastes it (bracketed) and submits it when the
// agent it recognises is in front; otherwise it writes the text raw followed by a carriage return (Sent.Prompt false).
func (c *Client) Prompt(ctx context.Context, t Target, text string) (*Sent, error) {
	p := t.params()
	p["text"], p["enter"], p["agentPrompt"] = text, true, true
	return c.send(ctx, p)
}

// Close is terminal.close: Orca closes the terminal's pane (its tab when it was the only one).
func (c *Client) Close(ctx context.Context, t Target) error {
	return c.Call(ctx, CloseTimeout, "terminal.close", t.params(), nil)
}

// CreateTerminal opens a fresh shell in an existing Orca worktree. The ordinary create RPC ignores cwd, so the
// launcher changes directory explicitly when it runs its command. It never uses the currently focused terminal.
func (c *Client) CreateTerminal(ctx context.Context, worktree, title, mutationID string) (*Terminal, error) {
	var res struct {
		Terminal struct {
			Terminal
			PaneKey string `json:"paneKey"`
		} `json:"terminal"`
	}
	err := c.Call(ctx, SendTimeout, "terminal.create", map[string]any{
		"worktree": worktree, "title": title, "clientMutationId": mutationID,
		// Explicit "background" skips desktop tab registration entirely. Omit it to create an inactive tab;
		// Orca awaits the renderer's creation receipt before returning, without switching the user's active tab.
		"focus": false, "activate": false,
	}, &res)
	if err != nil {
		return nil, err
	}
	t := res.Terminal.Terminal
	if t.Handle == "" || !t.IsLocal() {
		return nil, fmt.Errorf("orca terminal.create: no local terminal handle in the answer; creation may have completed")
	}
	// Orca 1.4.242/243 return paneKey instead of leafId, and may omit the incarnation in the create receipt.
	// Resolve only the exact returned handle; a newer terminal or a matching title is not proof of ownership.
	list, err := c.Terminals(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("orca terminal.create: created %s but could not resolve its identity: %w", t.Handle, err)
	}
	var found *Terminal
	for _, current := range list.Terminals {
		if current.Handle != t.Handle {
			continue
		}
		if current.Orphaned {
			return nil, fmt.Errorf("orca terminal.create: created %s but Orca did not register its tab; no command was sent", t.Handle)
		}
		if found != nil || !current.IsLocal() || current.IncarnationID == "" || current.TabID == "" || current.LeafID == "" ||
			t.IncarnationID != "" && current.IncarnationID != t.IncarnationID ||
			t.TabID != "" && current.TabID != t.TabID || t.LeafID != "" && current.LeafID != t.LeafID ||
			res.Terminal.PaneKey != "" && current.PaneKey() != res.Terminal.PaneKey ||
			t.WorktreeID != "" && current.WorktreeID != t.WorktreeID {
			return nil, fmt.Errorf("orca terminal.create: created %s but its identity changed; no command was sent", t.Handle)
		}
		found = &current
	}
	if found == nil {
		return nil, fmt.Errorf("orca terminal.create: created %s is absent from terminal.list; no command was sent", t.Handle)
	}
	return found, nil
}

// RenameTerminal sets the title of the terminal tab in Orca's layout.
func (c *Client) RenameTerminal(ctx context.Context, t Target, title string) error {
	p := t.params()
	p["title"] = title
	return c.Call(ctx, CallTimeout, "terminal.rename", p, nil)
}

// SetPaneTitle sets the pane title in Orca.
func (c *Client) SetPaneTitle(ctx context.Context, t Target, title string) error {
	p := t.params()
	p["title"] = title
	return c.Call(ctx, CallTimeout, "terminal.setPaneTitle", p, nil)
}

// RenameWorktree changes only the sidebar display name, leaving its branch, path and parent alone.
func (c *Client) RenameWorktree(ctx context.Context, id, name string) error {
	return c.Call(ctx, CallTimeout, "worktree.set", map[string]any{"worktree": "id:" + id, "displayName": name}, nil)
}

// RepoAdd registers a new git repository or folder workspace with Orca.
func (c *Client) RepoAdd(ctx context.Context, path, kind string) error {
	return c.Call(ctx, CallTimeout, "repo.add", map[string]any{"path": path, "kind": kind}, nil)
}

// WorktreeCreate asks worktree.create for a worktree of Repo (a repo selector, "id:<repo id>") named Name (its folder
// under Orca's workspace directory) on Branch: an existing local branch is checked out, else Branch is made from Base
// (Orca's base ref when empty). Parent (a worktree selector) is the worktree it is listed under in Orca's sidebar.
type WorktreeCreate struct {
	Repo, Name, Branch, Base, Parent string
	// Generated lets Orca retire collisions and keeps its first-work branch naming eligible.
	Generated bool
}

// CreatedWorktree is the worktree worktree.create made. Orca picks another folder and branch (a "-2" suffix) when the
// asked ones are taken, so these are what it is, not what was asked.
type CreatedWorktree struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Branch string `json:"branch"`
}

// CreateWorktree is worktree.create: Orca makes the checkout and keeps it as one of its worktrees, without activating
// it (no focus change, no setup hooks).
func (c *Client) CreateWorktree(ctx context.Context, w WorktreeCreate) (*CreatedWorktree, error) {
	p := map[string]any{"repo": w.Repo, "name": w.Name, "activate": false}
	if w.Generated {
		p["nameWasGenerated"] = true
	} else {
		p["branchNameOverride"] = w.Branch
	}
	if w.Base != "" {
		p["baseBranch"] = w.Base
	}
	if w.Parent != "" {
		p["parentWorktree"] = w.Parent
	}
	var res struct {
		Worktree CreatedWorktree `json:"worktree"`
	}
	if err := c.Call(ctx, WorktreeTimeout, "worktree.create", p, &res); err != nil {
		return nil, err
	}
	if res.Worktree.ID == "" || res.Worktree.Path == "" {
		return nil, fmt.Errorf("orca worktree.create: no worktree in the answer")
	}
	return &res.Worktree, nil
}

// Probe is status.get without the compatibility gate: the runtime's status (nil when it did not answer) and whether
// this client can use it (ErrIncompatible says why not).
func (c *Client) Probe(ctx context.Context) (*Status, error) {
	var st Status
	err := c.call(ctx, CallTimeout, "status.get", nil, &st, false)
	if !gaveUp(ctx) {
		c.note("status.get", err)
	}
	if err != nil {
		return nil, err
	}
	return &st, st.compatible()
}

// Version is the running Orca's app version, "" when it does not answer or speaks a protocol this client does not.
func (c *Client) Version(ctx context.Context) string {
	st, err := c.Probe(ctx)
	if err != nil {
		return ""
	}
	return st.AppVersion
}
