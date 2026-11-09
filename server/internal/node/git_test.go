package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type pullFixture struct {
	node  *Node
	seed  string
	local string
}

func newPullFixture(t *testing.T) pullFixture {
	t.Helper()
	base := t.TempDir()
	t.Setenv("HOME", base)
	t.Setenv("USERPROFILE", base) // os.UserHomeDir on Windows
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Sample Dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Sample Dev")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.com")
	remote, seed, local := filepath.Join(base, "remote.git"), filepath.Join(base, "seed"), filepath.Join(base, "local")
	runPullGit(t, base, "init", "--bare", "--initial-branch=main", remote)
	// Cloning the empty remote would put seed on the client's default branch
	// unless protocol v2 advertises the unborn HEAD, so seed starts on main itself.
	runPullGit(t, base, "init", "--initial-branch=main", seed)
	runPullGit(t, seed, "remote", "add", "origin", remote)
	f := pullFixture{node: &Node{home: base, state: newState()}, seed: seed, local: local}
	f.commit(t, seed, "app.txt", "first\n")
	runPullGit(t, seed, "push", "-u", "origin", "main")
	runPullGit(t, base, "clone", remote, local)
	return f
}

func runPullGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := git(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

func (f pullFixture) commit(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	runPullGit(t, dir, "add", "--", name)
	runPullGit(t, dir, "commit", "-m", "Update sample file")
}

func (f pullFixture) upstreamCommit(t *testing.T) {
	t.Helper()
	f.commit(t, f.seed, "app.txt", "latest\n")
	runPullGit(t, f.seed, "push")
}

func TestGitPullFastForwardAndFreshStatus(t *testing.T) {
	f := newPullFixture(t)
	f.upstreamCommit(t)
	before, err := f.node.gitStatus(context.Background(), f.local)
	if err != nil || before.Behind != 0 {
		t.Fatalf("缓存的上游应尚未发现新提交：%+v, %v", before, err)
	}
	params, _ := json.Marshal(map[string]string{"cwd": f.local})
	out, err := f.node.handle(context.Background(), "git.pull", params)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(*GitPullResult)
	if !result.Updated || result.Status.Behind != 0 || result.Status.Ahead != 0 || len(result.Status.Commits) != 2 {
		t.Fatalf("拉取后状态不正确：%+v", result)
	}
	if result.Status.Head.Hash != runPullGit(t, f.seed, "rev-parse", "HEAD") {
		t.Fatal("没有拉取最新提交")
	}
	content, _ := os.ReadFile(filepath.Join(f.local, "app.txt"))
	if string(content) != "latest\n" {
		t.Fatal("工作区没有更新")
	}
	again, err := f.node.gitPull(context.Background(), f.local)
	if err != nil || again.Updated {
		t.Fatalf("已同步的拉取应成功但不更新：%+v, %v", again, err)
	}
}

func TestGitPullRefusesDirtyTree(t *testing.T) {
	for _, kind := range []string{"modified", "staged", "untracked"} {
		t.Run(kind, func(t *testing.T) {
			f := newPullFixture(t)
			f.upstreamCommit(t)
			name := "app.txt"
			if kind == "untracked" {
				name = "draft.txt"
			}
			if err := os.WriteFile(filepath.Join(f.local, name), []byte("local draft\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "staged" {
				runPullGit(t, f.local, "add", name)
			}
			runPullGit(t, f.local, "config", "rebase.autoStash", "true")
			head := runPullGit(t, f.local, "rev-parse", "HEAD")
			_, err := f.node.gitPull(context.Background(), f.local)
			if refusalCode(err) != gitOpUncommitted {
				t.Fatalf("应拒绝有改动的工作区：%v", err)
			}
			content, _ := os.ReadFile(filepath.Join(f.local, name))
			if string(content) != "local draft\n" || head != runPullGit(t, f.local, "rev-parse", "HEAD") {
				t.Fatal("本地改动或 HEAD 被修改")
			}
			if runPullGit(t, f.local, "stash", "list") != "" {
				t.Fatal("不应自动 stash")
			}
		})
	}
}

func TestGitPullRefusesDivergenceEvenWithRebaseConfigured(t *testing.T) {
	f := newPullFixture(t)
	f.upstreamCommit(t)
	f.commit(t, f.local, "local.txt", "local commit\n")
	runPullGit(t, f.local, "config", "pull.rebase", "true")
	runPullGit(t, f.local, "config", "branch.main.rebase", "true")
	runPullGit(t, f.local, "config", "merge.autoStash", "true")
	head := runPullGit(t, f.local, "rev-parse", "HEAD")
	_, err := f.node.gitPull(context.Background(), f.local)
	if refusalCode(err) != gitOpNotFastForward || !strings.Contains(err.Error(), "fast-forward") {
		t.Fatalf("应拒绝分叉：%v", err)
	}
	if runPullGit(t, f.local, "rev-parse", "HEAD") != head || runPullGit(t, f.local, "status", "--porcelain") != "" {
		t.Fatal("不应自动变基或生成合并状态")
	}
}

func TestGitPullPreservesStashes(t *testing.T) {
	f := newPullFixture(t)
	if err := os.WriteFile(filepath.Join(f.local, "app.txt"), []byte("draft\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runPullGit(t, f.local, "stash", "push")
	stash := runPullGit(t, f.local, "rev-parse", "refs/stash")
	f.upstreamCommit(t)
	result, err := f.node.gitPull(context.Background(), f.local)
	if err != nil || !result.Updated || result.Status.Stashes != 1 {
		t.Fatalf("已有 stash 不应阻止拉取：%+v, %v", result, err)
	}
	if runPullGit(t, f.local, "rev-parse", "refs/stash") != stash {
		t.Fatal("已有 stash 被修改")
	}
}

func TestGitPullRefusesUnsupportedStates(t *testing.T) {
	for _, state := range []string{"detached", "no-upstream", "not-repo", "missing-remote"} {
		t.Run(state, func(t *testing.T) {
			f := newPullFixture(t)
			cwd := f.local
			switch state {
			case "detached":
				runPullGit(t, cwd, "checkout", "--detach")
			case "no-upstream":
				runPullGit(t, cwd, "branch", "--unset-upstream")
			case "not-repo":
				cwd = t.TempDir()
			case "missing-remote":
				runPullGit(t, cwd, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
			}
			head := runPullGit(t, f.local, "rev-parse", "HEAD")
			if _, err := f.node.gitPull(context.Background(), cwd); err == nil {
				t.Fatal("应返回可见的失败")
			}
			if head != runPullGit(t, f.local, "rev-parse", "HEAD") {
				t.Fatal("失败后 HEAD 改变")
			}
		})
	}
}

func TestGitTimedCancellationAndTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gitTimed(ctx, t.TempDir(), time.Second, "status"); !errors.Is(err, context.Canceled) {
		t.Fatalf("应保留取消原因：%v", err)
	}
	if _, err := gitTimed(context.Background(), t.TempDir(), time.Nanosecond, "status"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应返回超时：%v", err)
	}
}
