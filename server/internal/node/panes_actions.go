package node

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// What the terminal browser's and the sidebar's context menus change on a computer (docs/integrations/terminal.md "Node menus"):
// close a tab or a pane, split a pane, rename a tab, run an agent in a plain shell pane, and fetch / pull / push in a
// pane's directory. The hub lets only a full device call them. Every call names a pane, tab or workspace the host
// lists; none of them names a directory or a command, so a menu cannot be turned into a way to run anything.

// maxLabel bounds a tab's new name.
const maxLabel = 80

// listedPane is the layout entry of a pane the host lists (a snapshot is taken when the one kept does not have it),
// with the kind of the host it is on.
func (n *Node) listedPane(ctx context.Context, pane string) (host.Agent, error) {
	if _, err := n.shellPane(ctx, pane); err != nil {
		return host.Agent{}, err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, l := range n.layouts {
		for _, p := range l.panes {
			if p.PaneID == pane {
				return p, nil
			}
		}
	}
	return host.Agent{}, fmt.Errorf("pane %s is not running", pane)
}

// hostOfKind is the host a tab or workspace id belongs to: the one of that kind, else the engine's.
func (n *Node) hostOfKind(kind string) (host.Host, error) {
	if kind == "" {
		kind = n.defaultKind()
	}
	h := n.hosts.Get(kind)
	if h == nil {
		return nil, fmt.Errorf("no %s host on this machine", kind)
	}
	return h, nil
}

// listedTab checks that the host lists the tab (refreshing once when it does not: a tab opened a moment ago).
func (n *Node) listedTab(ctx context.Context, h host.Host, tab string) error {
	has := func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		_, ok := n.layouts[h.Kind()].slots[tab]
		return ok
	}
	if tab == "" {
		return errors.New("no tab")
	}
	if has() {
		return nil
	}
	if err := n.refreshHosts(ctx, h); err == nil && has() {
		return nil
	}
	return fmt.Errorf("tab %s is not running", tab)
}

// closeTab closes a tab with every pane in it.
func (n *Node) closeTab(ctx context.Context, kind, tab string) error {
	h, err := n.hostOfKind(kind)
	if err != nil {
		return err
	}
	c, ok := h.(host.SlotCloser)
	if !ok {
		return unsupported(h)
	}
	if err := n.listedTab(ctx, h, tab); err != nil {
		return err
	}
	if err := c.CloseSlot(ctx, tab); err != nil {
		return fmt.Errorf("关闭标签页失败：%w", err)
	}
	_ = n.refreshHosts(ctx, h)
	return nil
}

// renameTab renames a tab; a name is one trimmed line.
func (n *Node) renameTab(ctx context.Context, kind, tab, label string) error {
	label = strings.Join(strings.Fields(label), " ")
	if label == "" || utf8.RuneCountInString(label) > maxLabel {
		return fmt.Errorf("标签页名称要 1 到 %d 个字", maxLabel)
	}
	h, err := n.hostOfKind(kind)
	if err != nil {
		return err
	}
	org, ok := h.(host.Organizer)
	if !ok {
		return unsupported(h)
	}
	if err := n.listedTab(ctx, h, tab); err != nil {
		return err
	}
	if err := org.RenameSlot(ctx, tab, label); err != nil {
		return err
	}
	_ = n.refreshHosts(ctx, h)
	return nil
}

// closePane closes a pane: an agent's goes through the session close (it is asked to quit first), a shell's is closed
// as it is. In a tab with more panes only the pane goes; in a tab with one, the tab (the host's own rule).
func (n *Node) closePane(ctx context.Context, pane string) error {
	p, err := n.listedPane(ctx, pane)
	if err != nil {
		return err
	}
	if id := n.sessionOfPane(pane); id != "" {
		return n.close(ctx, id)
	}
	org, ref, err := paneCap[host.Organizer](n, pane)
	if err != nil {
		return err
	}
	if err := org.Close(ctx, ref, p.WorkspaceID, p.TabID); err != nil {
		return fmt.Errorf("关闭窗格失败：%w", err)
	}
	_ = n.refreshHosts(ctx)
	return nil
}

// sessionOfPane is the agent session bound to a pane ("" for a plain shell); the same one every time when two claim it.
func (n *Node) sessionOfPane(pane string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	best := ""
	for id, a := range n.agents {
		if a.PaneID == pane && (best == "" || id < best) {
			best = id
		}
	}
	return best
}

// paneDir is the directory a pane works in: its foreground program's, else the pane's own.
func paneDir(p host.Agent) string {
	if p.ForegroundCwd != "" {
		return p.ForegroundCwd
	}
	return p.Cwd
}

// splitPane splits a pane in the direction ("right": side by side, "down": stacked); the new shell starts in the
// pane's own directory and takes no focus.
func (n *Node) splitPane(ctx context.Context, pane, direction string) (proto.PaneSplitResult, error) {
	if direction != "right" && direction != "down" {
		return proto.PaneSplitResult{}, errors.New("direction must be right or down")
	}
	p, err := n.listedPane(ctx, pane)
	if err != nil {
		return proto.PaneSplitResult{}, err
	}
	sp, ref, err := paneCap[host.Splitter](n, pane)
	if err != nil {
		return proto.PaneSplitResult{}, err
	}
	created, err := sp.Split(ctx, ref, direction, paneDir(p))
	if err != nil {
		return proto.PaneSplitResult{}, fmt.Errorf("新建窗格失败：%w", err)
	}
	id := created.String()
	_ = n.refreshHosts(ctx) // the new pane is in the layout the terminal call checks
	return proto.PaneSplitResult{Pane: id, Terminal: panePrefix + id}, nil
}

// ── run an agent in a plain shell pane ───────────────────

type adoptedPaneKey struct{}

// adopted is the pane a user picked for a start, and the programs in front of it (its shell).
type adopted struct {
	created *host.Created
	shells  []string
}

// withAdoptedPane makes a start use the pane a user picked instead of opening a tab (newTab); shells are the programs
// in front of it.
func withAdoptedPane(ctx context.Context, c *host.Created, shells []string) context.Context {
	return context.WithValue(ctx, adoptedPaneKey{}, adopted{c, shells})
}

// adoptedPane is the pane a start was told to use, nil for a start that opens its own tab.
func adoptedPane(ctx context.Context) *host.Created {
	a, _ := ctx.Value(adoptedPaneKey{}).(adopted)
	return a.created
}

// paneShells are the shells that read what a start types (typedHerdrAgent): the programs in front of the pane the
// user picked, else the shell the host opened, falling back to herdr's shell when the host does not report one.
func paneShells(ctx context.Context, created *host.Created) []string {
	if a, ok := ctx.Value(adoptedPaneKey{}).(adopted); ok {
		return a.shells
	}
	if created.Shell != "" {
		return []string{created.Shell}
	}
	return []string{herdrPaneShell()}
}

// runAgentInPane starts an agent of a launcher in a plain shell pane, the way "New Agent" does in a new tab: same
// launcher command, same wait for the session, same binding. The pane has to be at its shell prompt: nothing but a
// shell in front, no agent bound to it. A start that fails leaves the pane as it is (it is not closed: it was not made
// for this).
func (n *Node) runAgentInPane(ctx context.Context, pane, launcher string) (*proto.Session, error) {
	launcher = strings.TrimSpace(launcher)
	if launcher == "" {
		return nil, errors.New("launcher is required")
	}
	p, err := n.listedPane(ctx, pane)
	if err != nil {
		return nil, err
	}
	if p.Agent != "" || n.sessionOfPane(pane) != "" {
		return nil, errors.New("这个窗格里已经有 Agent 在运行")
	}
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return nil, err
	}
	launch, err := n.launcherFor(n.defaultKind())
	if err != nil {
		return nil, err
	}
	if launch.(host.Host).Kind() != h.Kind() {
		return nil, unsupported(h)
	}
	f, ok := h.(host.ForegroundNamer)
	if !ok {
		return nil, unsupported(h)
	}
	names, err := f.PaneForeground(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("读不到窗格里运行的程序：%w", err)
	}
	if len(names) == 0 {
		return nil, errors.New("读不到窗格里运行的程序")
	}
	for _, name := range names {
		if !shellProgram(name) {
			return nil, fmt.Errorf("窗格里正在运行 %s，不在 shell 提示符下", name)
		}
	}
	cwd := paneDir(p)
	if cwd == "" {
		return nil, errors.New("不知道这个窗格的目录")
	}
	created := &host.Created{SlotID: p.TabID, SpaceID: p.WorkspaceID, Pane: ref}
	return n.start(withAdoptedPane(ctx, created, names), cwd, launcher, "", "", p.WorkspaceID, "", "", "", "", "")
}

// ── git in a pane's or workspace's directory ─────────────

const (
	gitOpTimeout = 60 * time.Second
	maxGitOutput = 2 << 10
)

// gitDirOf is the directory a git menu item works in: a pane's, or a workspace's own.
func (n *Node) gitDirOf(ctx context.Context, pane, workspace, kind string) (string, error) {
	if pane != "" {
		p, err := n.listedPane(ctx, pane)
		if err != nil {
			return "", err
		}
		return paneDir(p), nil
	}
	if workspace == "" {
		return "", errors.New("pane or workspace is required")
	}
	h, err := n.hostOfKind(kind)
	if err != nil {
		return "", err
	}
	space, ok := n.spaceOf(h.Kind(), workspace)
	if !ok {
		_ = n.refreshHosts(ctx, h)
		space, ok = n.spaceOf(h.Kind(), workspace)
	}
	if !ok {
		return "", fmt.Errorf("workspace %s is not running", workspace)
	}
	return space.Cwd, nil
}

// gitRun is gitTimed that keeps what git printed on stderr too (push and fetch report there).
func gitRun(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.quotepath=off", "--no-pager"}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/usr/bin/false", "SSH_ASKPASS=/usr/bin/false", "GCM_INTERACTIVE=never")
	cmd.WaitDelay = time.Second
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	text := strings.TrimSpace(out.String())
	if err != nil {
		if ctx.Err() != nil {
			return text, fmt.Errorf("git timed out or was canceled: %w", ctx.Err())
		}
		if text == "" {
			text = err.Error()
		}
		return text, fmt.Errorf("git %s: %s", args[0], clip(text))
	}
	return text, nil
}

// clip keeps the end of long output (where git says why it stopped).
func clip(s string) string {
	if len(s) <= maxGitOutput {
		return s
	}
	s = s[len(s)-maxGitOutput:]
	for !utf8.ValidString(s) && len(s) > 0 {
		s = s[1:]
	}
	return "…" + s
}

// The refusals of panes.git and git.pull (#1888). The hub says each in the client's language (apierr rpcFailureKeys,
// i18n rpc-errors) and keeps the node's words, git's included, as the detail; the *_failed ones are said with git's own
// words, which name the reason.
const (
	gitOpNoDirectory    = "no_directory"
	gitOpNotRepo        = "not_a_repository"
	gitOpDetached       = "detached_head"
	gitOpNoUpstream     = "no_upstream"
	gitOpUncommitted    = "uncommitted_changes"
	gitOpNotFastForward = "not_fast_forward"
	gitOpPushRejected   = "push_rejected"
	gitOpPullFailed     = "pull_failed"
	gitOpPushFailed     = "push_failed"
	gitOpFetchFailed    = "fetch_failed"
)

// gitOp runs fetch, pull or push in dir. It never forces anything: a pull only fast-forwards and refuses a dirty
// tree (gitPull), a push sends the branch to its own upstream branch by an explicit refspec with no "+" (a
// configured forcing refspec does not apply), a fetch only updates remote-tracking refs. Not a repository: refused.
// What came of it is an outcome the clients say in their language, with an English message for older ones.
func (n *Node) gitOp(ctx context.Context, dir, op string) (proto.PaneGitResult, error) {
	res := proto.PaneGitResult{Op: op}
	if strings.TrimSpace(dir) == "" {
		return res, refusal(gitOpNoDirectory, "the folder of this pane is not known", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, gitOpTimeout+15*time.Second)
	defer cancel()
	st, err := n.gitStatus(ctx, dir)
	if err != nil {
		return res, err
	}
	if !st.IsRepo {
		return res, refusal(gitOpNotRepo, "the folder is not in a Git repository", nil)
	}
	switch op {
	case "fetch":
		out, err := gitRun(ctx, st.Root, gitOpTimeout, "fetch", "--no-recurse-submodules")
		if err != nil {
			return res, refusal(gitOpFetchFailed, err.Error(), nil)
		}
		res.Outcome, res.Message, res.Output = proto.GitOutcomeFetched, "Fetched", clip(out)
	case "pull":
		r, err := n.gitPull(ctx, st.Root)
		if err != nil {
			return res, err
		}
		res.Outcome, res.Message = proto.GitOutcomeUpToDate, "Already up to date"
		if r.Updated {
			res.Outcome, res.Message = proto.GitOutcomePulled, "Pulled the latest commits"
		}
	case "push":
		if st.Branch == "" {
			return res, refusal(gitOpDetached, "HEAD is detached: switch to a branch first", nil)
		}
		if st.Upstream == "" {
			return res, refusal(gitOpNoUpstream, st.Branch+" has no upstream branch", nil)
		}
		// a deleted upstream counts nothing ahead: the push below publishes the branch there again
		if st.Ahead == 0 && !st.Gone {
			res.Outcome, res.Message = proto.GitOutcomeNothingToPush, "No commits to push"
			return res, nil
		}
		remote, err := gitRun(ctx, st.Root, 10*time.Second, "config", "--get", "branch."+st.Branch+".remote")
		if err != nil || remote == "" || strings.HasPrefix(remote, "-") {
			return res, refusal(gitOpNoUpstream, "the remote of "+st.Branch+"'s upstream is not configured", nil)
		}
		merge, err := gitRun(ctx, st.Root, 10*time.Second, "config", "--get", "branch."+st.Branch+".merge")
		if err != nil || !strings.HasPrefix(merge, "refs/heads/") {
			return res, refusal(gitOpNoUpstream, "the upstream branch of "+st.Branch+" is not configured", nil)
		}
		out, err := gitRun(ctx, st.Root, gitOpTimeout, "push", remote, "refs/heads/"+st.Branch+":"+merge)
		if err != nil {
			code := gitOpPushFailed
			if low := strings.ToLower(err.Error()); strings.Contains(low, "[rejected]") || strings.Contains(low, "non-fast-forward") ||
				strings.Contains(low, "fetch first") {
				code = gitOpPushRejected
			}
			return res, refusal(code, err.Error()+" (nothing was forced)", nil)
		}
		res.Outcome, res.Message, res.Output = proto.GitOutcomePushed, "Pushed", clip(out)
	default:
		return res, errors.New("op must be fetch, pull or push")
	}
	return res, nil
}
