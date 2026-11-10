package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const actionSession = "11111111-2222-4333-8444-555555555555"

// actionNode is a node that has listed a workspace with an agent tab (w1:t1), a one-pane shell tab (w1:t2) and a
// split shell tab (w1:t3, two panes), as the terminal browser's menus see them.
func actionNode(t *testing.T) (*Node, *fakeHerdr, string) {
	t.Helper()
	streamingHerdr()
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	shell := func(pane, tab string) map[string]any {
		return map[string]any{"pane_id": pane, "tab_id": tab, "workspace_id": "w1", "cwd": cwd, "terminal_title": "zsh", "terminal_title_stripped": "zsh"}
	}
	agent := claudeAgent(actionSession, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "fix it")
	f.reply("session.snapshot", snapshot(
		[]any{map[string]any{"workspace_id": "w1", "label": "proj", "number": 1, "cwd": cwd, "branch": "main"}},
		[]any{map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "label": "agent-1", "number": 1},
			map[string]any{"tab_id": "w1:t2", "workspace_id": "w1", "label": "shell", "number": 2},
			map[string]any{"tab_id": "w1:t3", "workspace_id": "w1", "label": "split", "number": 3}},
		[]any{agent},
		[]any{agent, shell("w1:p2", "w1:t2"), shell("w1:p3", "w1:t3"), shell("w1:p4", "w1:t3")},
	))
	listSessions(t, n)
	f.reset()
	return n, f, cwd
}

func TestTabCloseNamesAListedTabOnly(t *testing.T) {
	n, f, _ := actionNode(t)
	if _, err := request(t, n, "tabs.close", map[string]any{"tab_id": "w1:t2"}); err != nil {
		t.Fatal(err)
	}
	assertTrace(t, f.trace("tab.close"), []string{call("tab.close", map[string]any{"tab_id": "w1:t2"})})

	f.reset()
	for _, id := range []string{"", "w9:t9", "--help"} {
		if _, err := request(t, n, "tabs.close", map[string]any{"tab_id": id}); err == nil {
			t.Errorf("closed tab %q the host does not list", id)
		}
	}
	if got := f.trace("tab.close", "pane.close"); len(got) != 0 {
		t.Errorf("herdr was told to close for a refused request: %v", got)
	}
}

func TestTabRenameTrimsAndBoundsTheName(t *testing.T) {
	n, f, _ := actionNode(t)
	if _, err := request(t, n, "tabs.rename", map[string]any{"tab_id": "w1:t2", "label": "  build \n logs "}); err != nil {
		t.Fatal(err)
	}
	assertTrace(t, f.trace("tab.rename"), []string{call("tab.rename", map[string]any{"tab_id": "w1:t2", "label": "build logs"})})

	f.reset()
	for _, label := range []string{"", "   ", strings.Repeat("长", maxLabel+1)} {
		if _, err := request(t, n, "tabs.rename", map[string]any{"tab_id": "w1:t2", "label": label}); err == nil {
			t.Errorf("renamed a tab to %q", label)
		}
	}
	if _, err := request(t, n, "tabs.rename", map[string]any{"tab_id": "w9:t9", "label": "x"}); err == nil {
		t.Error("renamed a tab the host does not list")
	}
	if got := f.trace("tab.rename"); len(got) != 0 {
		t.Errorf("tab.rename for a refused request: %v", got)
	}
}

// A shell's pane closes with its tab when it is the tab's only pane and alone when the tab is split; a pane the host
// does not list is never closed.
func TestPaneCloseShells(t *testing.T) {
	n, f, _ := actionNode(t)
	f.reply("pane.list", map[string]any{"panes": []any{map[string]any{"tab_id": "w1:t2"}}})
	if _, err := request(t, n, "panes.close", map[string]any{"pane": "w1:p2"}); err != nil {
		t.Fatal(err)
	}
	assertTrace(t, f.trace("pane.close", "tab.close"), []string{call("tab.close", map[string]any{"tab_id": "w1:t2"})})

	f.reset()
	f.reply("pane.list", map[string]any{"panes": []any{map[string]any{"tab_id": "w1:t3"}, map[string]any{"tab_id": "w1:t3"}}})
	if _, err := request(t, n, "panes.close", map[string]any{"pane": "w1:p4"}); err != nil {
		t.Fatal(err)
	}
	assertTrace(t, f.trace("pane.close", "tab.close"), []string{call("pane.close", map[string]any{"pane_id": "w1:p4"})})

	f.reset()
	if _, err := request(t, n, "panes.close", map[string]any{"pane": "w9:p9"}); err == nil {
		t.Error("closed a pane the host does not list")
	}
	if got := f.trace("pane.close", "tab.close"); len(got) != 0 {
		t.Errorf("close for a pane that is not there: %v", got)
	}
}

// An agent's pane is closed the way "Close Agent" does: asked to quit first, then closed.
func TestPaneCloseAnAgentQuitsItFirst(t *testing.T) {
	n, f, _ := actionNode(t)
	f.reply("pane.list", map[string]any{"panes": []any{map[string]any{"tab_id": "w1:t1"}}})
	if _, err := request(t, n, "panes.close", map[string]any{"pane": "w1:p1"}); err != nil {
		t.Fatal(err)
	}
	got := f.trace("pane.send_keys", "pane.close", "tab.close")
	if len(got) != 3 || !strings.Contains(got[0], "ctrl+c") || !strings.Contains(got[1], "ctrl+c") || got[2] != call("tab.close", map[string]any{"tab_id": "w1:t1"}) {
		t.Errorf("calls = %v", got)
	}
}

func TestPaneSplitStartsInThePanesDirectory(t *testing.T) {
	n, f, cwd := actionNode(t)
	f.reply("pane.split", map[string]any{"pane": map[string]any{"pane_id": "w1:p9", "tab_id": "w1:t2"}})
	res, err := request(t, n, "panes.split", map[string]any{"pane": "w1:p2", "direction": "right", "cwd": "/etc"})
	if err != nil {
		t.Fatal(err)
	}
	if res["pane"] != "w1:p9" || res["terminal"] != "pane:w1:p9" {
		t.Errorf("result = %v", res)
	}
	assertTrace(t, f.trace("pane.split"), []string{
		call("pane.split", map[string]any{"target_pane_id": "w1:p2", "direction": "right", "focus": false, "cwd": cwd}),
	})

	f.reset()
	for _, p := range []map[string]any{
		{"pane": "w1:p2", "direction": "diagonal"}, {"pane": "w1:p2"}, {"pane": "w9:p9", "direction": "down"}, {"direction": "down"},
	} {
		if _, err := request(t, n, "panes.split", p); err == nil {
			t.Errorf("split %v", p)
		}
	}
	if got := f.trace("pane.split"); len(got) != 0 {
		t.Errorf("pane.split for a refused request: %v", got)
	}
}

// "Run Agent here": the launcher runs in the pane itself, not in a new tab. The pane is a herdr pane like a new tab:
// herdr is told the agent is Claude, unless the pane's shell cannot take `VAR=val cmd` (tcsh).
func TestRunAgentInAShellPane(t *testing.T) {
	for _, tc := range []struct{ shell, typed string }{
		{"-zsh", "HERDR_AGENT=claude claude --session-id "},
		{"-tcsh", "claude --session-id "},
	} {
		t.Run(tc.shell, func(t *testing.T) {
			n, f, cwd := actionNode(t)
			f.reply("pane.process_info", map[string]any{"process_info": map[string]any{"foreground_processes": []any{
				map[string]any{"pid": 1, "name": strings.TrimPrefix(tc.shell, "-"), "argv0": tc.shell}}}})
			f.reply("agent.get", map[string]any{"agent": map[string]any{"agent": "claude", "agent_status": proto.StatusIdle, "pane_id": "w1:p2",
				"tab_id": "w1:t2", "workspace_id": "w1"}})
			f.screenSteps("w1:p2", isKeys(), screenFixture(t, "claude-2.1.312-trust-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.txt"))

			res, err := request(t, n, "panes.agent", map[string]any{"pane": "w1:p2", "launcher": "claude"})
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res["session_id"].(string)
			if id == "" {
				t.Fatalf("result = %v", res)
			}
			if got := f.trace("tab.create", "workspace.create"); len(got) != 0 {
				t.Errorf("a tab was opened: %v", got)
			}
			var typed []string
			for _, c := range f.calls() {
				if c.Method == "pane.send_input" {
					typed = append(typed, str(c.Params["pane_id"])+" "+str(c.Params["text"]))
				}
			}
			want := tc.typed
			if tc.shell == "-zsh" { // with the node's directory, without the assistant's environment (agentenv.go)
				want = inAgentEnv(n.dir, want, shellQuote)
			}
			if len(typed) != 1 || !strings.HasPrefix(typed[0], "w1:p2 "+want+id) {
				t.Errorf("typed = %q (cwd %s)", typed, cwd)
			}
			if f.trace("pane.close", "tab.close") != nil {
				t.Errorf("the pane was closed: %v", f.trace("pane.close", "tab.close"))
			}
			n.mu.Lock()
			bound := n.panes["w1:p2"]
			n.mu.Unlock()
			if bound != id {
				t.Errorf("the pane is bound to %q, not %q", bound, id)
			}
		})
	}
}

// Only a pane at its shell prompt takes an agent; one that fails to come up is left as it is.
func TestRunAgentRefusals(t *testing.T) {
	n, f, _ := actionNode(t)
	fg := func(names ...string) {
		list := []any{}
		for _, name := range names {
			list = append(list, map[string]any{"pid": 1, "name": name, "argv0": name})
		}
		f.reply("pane.process_info", map[string]any{"process_info": map[string]any{"foreground_processes": list}})
	}
	ctx := context.Background()
	if _, err := n.runAgentInPane(ctx, "w1:p1", "claude"); err == nil {
		t.Error("ran an agent in a pane that has one")
	}
	if _, err := n.runAgentInPane(ctx, "w9:p9", "claude"); err == nil {
		t.Error("ran an agent in a pane the host does not list")
	}
	if _, err := n.runAgentInPane(ctx, "w1:p2", " "); err == nil {
		t.Error("ran without a launcher")
	}
	fg("vim")
	if _, err := n.runAgentInPane(ctx, "w1:p2", "claude"); err == nil || !strings.Contains(err.Error(), "vim") {
		t.Errorf("ran in front of vim: %v", err)
	}
	fg()
	if _, err := n.runAgentInPane(ctx, "w1:p2", "claude"); err == nil {
		t.Error("ran where the foreground is unknown")
	}
	for _, c := range f.calls() {
		if strings.HasPrefix(c.Method, "pane.send") || c.Method == "tab.create" {
			t.Errorf("a refused run reached the pane: %s", c)
		}
	}

	// A claude that never comes up: the pane stays (a start in a new tab would close that tab).
	fg("zsh")
	f.reply("agent.get", map[string]any{"agent": map[string]any{"agent": "", "pane_id": "w1:p2", "tab_id": "w1:t2", "workspace_id": "w1"}})
	f.setScreen("w1:p2", "$ ")
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := n.runAgentInPane(ctx, "w1:p2", "claude"); err == nil {
		t.Error("a start that never came up succeeded")
	}
	if got := f.trace("pane.close", "tab.close"); len(got) != 0 {
		t.Errorf("the pane was closed after a failed start: %v", got)
	}
}

func TestGitOpsNeverForce(t *testing.T) {
	f := newPullFixture(t)
	ctx := context.Background()

	// fetch: sees the remote's new commit without moving the branch
	f.upstreamCommit(t)
	res, err := f.node.gitOp(ctx, f.local, "fetch")
	if err != nil || res.Op != "fetch" || res.Outcome != proto.GitOutcomeFetched {
		t.Fatalf("fetch = %+v, %v", res, err)
	}
	if st, _ := f.node.gitStatus(ctx, f.local); st.Behind != 1 {
		t.Errorf("behind after fetch = %d", st.Behind)
	}

	// pull: fast-forward only
	res, err = f.node.gitOp(ctx, f.local, "pull")
	if err != nil || res.Outcome != proto.GitOutcomePulled || res.Message != "Pulled the latest commits" {
		t.Fatalf("pull = %+v, %v", res, err)
	}
	if res, err = f.node.gitOp(ctx, f.local, "pull"); err != nil || res.Outcome != proto.GitOutcomeUpToDate {
		t.Errorf("second pull = %+v, %v", res, err)
	}

	// push: nothing to send, then one commit to its upstream
	if res, err = f.node.gitOp(ctx, f.local, "push"); err != nil || res.Outcome != proto.GitOutcomeNothingToPush {
		t.Errorf("push of nothing = %+v, %v", res, err)
	}
	f.commit(t, f.local, "local.txt", "mine\n")
	if res, err = f.node.gitOp(ctx, f.local, "push"); err != nil || res.Outcome != proto.GitOutcomePushed {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if runPullGit(t, f.local, "rev-parse", "HEAD") != runPullGit(t, f.seed, "ls-remote", "origin", "refs/heads/main")[:40] {
		t.Error("the remote did not get the commit")
	}

	// a diverged remote: a plain push is rejected, and a configured forcing refspec does not make it a force push
	f.commit(t, f.seed, "theirs.txt", "theirs\n")
	runPullGit(t, f.seed, "pull", "--no-rebase", "--no-edit")
	runPullGit(t, f.seed, "push")
	runPullGit(t, f.local, "reset", "--hard", "HEAD~1")
	f.commit(t, f.local, "other.txt", "diverged\n")
	runPullGit(t, f.local, "config", "remote.origin.push", "+refs/heads/*:refs/heads/*")
	remoteBefore := runPullGit(t, f.seed, "ls-remote", "origin", "refs/heads/main")
	if _, err = f.node.gitOp(ctx, f.local, "push"); err == nil {
		t.Fatal("a diverged push went through")
	} else if refusalCode(err) != gitOpPushRejected || !strings.Contains(err.Error(), "rejected") {
		t.Errorf("a diverged push = %v", err)
	}
	if got := runPullGit(t, f.seed, "ls-remote", "origin", "refs/heads/main"); got != remoteBefore {
		t.Errorf("the remote moved: %s -> %s", remoteBefore, got)
	}
}

// A branch whose upstream was deleted on the remote and pruned (a merged pull request's branch) is not "up to date":
// the status says the upstream is gone, and Push publishes the branch again instead of finding nothing to push.
func TestGitUpstreamGone(t *testing.T) {
	f := newPullFixture(t)
	ctx := context.Background()
	runPullGit(t, f.local, "switch", "-q", "-c", "feature")
	f.commit(t, f.local, "feature.txt", "first\n")
	runPullGit(t, f.local, "push", "-q", "-u", "origin", "feature")
	runPullGit(t, f.seed, "push", "-q", "origin", "--delete", "feature")
	runPullGit(t, f.local, "fetch", "-q", "--prune")
	f.commit(t, f.local, "feature.txt", "after the merge\n")

	st, err := f.node.gitStatus(ctx, f.local)
	if err != nil {
		t.Fatal(err)
	}
	if st.Upstream != "origin/feature" || !st.Gone || st.Ahead != 0 || st.Behind != 0 {
		t.Fatalf("status = upstream %q gone %v ahead %d behind %d", st.Upstream, st.Gone, st.Ahead, st.Behind)
	}
	res, err := f.node.gitOp(ctx, f.local, "push")
	if err != nil || res.Outcome != proto.GitOutcomePushed {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if got := runPullGit(t, f.seed, "ls-remote", "origin", "refs/heads/feature"); !strings.HasPrefix(got, runPullGit(t, f.local, "rev-parse", "HEAD")) {
		t.Fatalf("the remote branch is %q", got)
	}
	if st, _ = f.node.gitStatus(ctx, f.local); st.Gone || st.Ahead != 0 {
		t.Fatalf("after the push: gone %v ahead %d", st.Gone, st.Ahead)
	}

	// the Git panel's push publishes it again the same way
	runPullGit(t, f.seed, "push", "-q", "origin", "--delete", "feature")
	runPullGit(t, f.local, "fetch", "-q", "--prune")
	w, err := f.node.gitPush(ctx, f.local, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.Status.Gone || w.Status.Upstream != "origin/feature" {
		t.Fatalf("after the panel's push: %+v", w.Status)
	}
}

func TestParseStatusV2Upstream(t *testing.T) {
	for _, c := range []struct {
		name, out     string
		gone          bool
		ahead, behind int
	}{
		{"tracked", "# branch.head main\x00# branch.upstream origin/main\x00# branch.ab +2 -1\x00", false, 2, 1},
		{"level", "# branch.head main\x00# branch.upstream origin/main\x00# branch.ab +0 -0\x00", false, 0, 0},
		{"gone", "# branch.head feature\x00# branch.upstream origin/feature\x00? new.txt\x00", true, 0, 0},
		{"none", "# branch.head feature\x00", false, 0, 0},
	} {
		st := &GitStatus{}
		parseStatusV2(c.out, st)
		if st.Gone != c.gone || st.Ahead != c.ahead || st.Behind != c.behind {
			t.Errorf("%s: gone %v ahead %d behind %d", c.name, st.Gone, st.Ahead, st.Behind)
		}
	}
}

func TestGitOpsRefuse(t *testing.T) {
	f := newPullFixture(t)
	ctx := context.Background()
	plain := t.TempDir()
	for _, op := range []string{"fetch", "pull", "push"} {
		if _, err := f.node.gitOp(ctx, plain, op); refusalCode(err) != gitOpNotRepo {
			t.Errorf("%s outside a repository: %v", op, err)
		}
	}
	if _, err := f.node.gitOp(ctx, "", "fetch"); err == nil {
		t.Error("no directory accepted")
	}
	if _, err := f.node.gitOp(ctx, f.local, "reset"); err == nil {
		t.Error("an unknown op accepted")
	}
	// a dirty tree is not pulled over
	f.upstreamCommit(t)
	if err := os.WriteFile(filepath.Join(f.local, "app.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.node.gitOp(ctx, f.local, "pull"); refusalCode(err) != gitOpUncommitted {
		t.Errorf("pulled over uncommitted changes: %v", err)
	}
	// a detached HEAD cannot be pushed
	runPullGit(t, f.local, "checkout", "--", "app.txt")
	runPullGit(t, f.local, "checkout", "--detach")
	if _, err := f.node.gitOp(ctx, f.local, "push"); refusalCode(err) != gitOpDetached {
		t.Errorf("pushed a detached HEAD: %v", err)
	}
	// a branch with no upstream is neither pushed nor pulled
	runPullGit(t, f.local, "switch", "-q", "-c", "alone")
	for _, op := range []string{"push", "pull"} {
		if _, err := f.node.gitOp(ctx, f.local, op); refusalCode(err) != gitOpNoUpstream {
			t.Errorf("%s with no upstream: %v", op, err)
		}
	}
	// a remote that cannot be reached: git's own words, under its code
	runPullGit(t, f.local, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	if _, err := f.node.gitOp(ctx, f.local, "fetch"); refusalCode(err) != gitOpFetchFailed || !strings.Contains(err.Error(), "git fetch:") {
		t.Errorf("fetch from a missing remote: %v", err)
	}
	if _, err := f.node.gitOp(ctx, "", "fetch"); refusalCode(err) != gitOpNoDirectory {
		t.Errorf("no directory: %v", err)
	}
}

// panes.git takes a pane or a workspace the host lists and works in the directory the host reports for it; the
// caller names no directory.
func TestPanesGitUsesTheListedDirectory(t *testing.T) {
	n, _, cwd := actionNode(t) // a directory that is not a repository
	for _, p := range []map[string]any{
		{"pane": "w1:p2", "op": "fetch", "cwd": "/etc"}, {"workspace_id": "w1", "op": "fetch", "cwd": "/etc"},
	} {
		if _, err := request(t, n, "panes.git", p); refusalCode(err) != gitOpNotRepo {
			t.Errorf("%v in %s: %v", p, cwd, err)
		}
	}
	for _, p := range []map[string]any{{"op": "fetch"}, {"pane": "w9:p9", "op": "fetch"}, {"workspace_id": "w9", "op": "fetch"}} {
		if _, err := request(t, n, "panes.git", p); err == nil {
			t.Errorf("%v accepted", p)
		}
	}
}
