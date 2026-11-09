package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/rpc"
)

// gitCodeOf is err's rpc.Error code and the files its data lists.
func gitCodeOf(t *testing.T, err error) (string, []string) {
	t.Helper()
	var e *rpc.Error
	if !errors.As(err, &e) {
		t.Fatalf("not an rpc.Error: %v", err)
	}
	var data struct{ Files []string }
	_ = json.Unmarshal(e.Data, &data)
	return e.Code, data.Files
}

func putFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

// Stage, unstage and commit: only what is staged goes in; nothing staged or a detached HEAD is said as such.
func TestGitStageAndCommit(t *testing.T) {
	f := newPullFixture(t)
	ctx := context.Background()
	putFile(t, f.local, "app.txt", "changed\n")
	putFile(t, f.local, "notes.txt", "draft\n")
	if _, err := f.node.gitCommit(ctx, f.local, "nothing yet"); err == nil {
		t.Fatal("committed with nothing staged")
	} else if code, _ := gitCodeOf(t, err); code != gitCodeNothingStaged {
		t.Fatalf("code %s", code)
	}
	res, err := f.node.gitStage(ctx, f.local, []string{"app.txt", "notes.txt"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if staged := stagedPaths(res.Status); !slices.Equal(staged, []string{"app.txt", "notes.txt"}) {
		t.Fatalf("staged %v", staged)
	}
	if res, err = f.node.gitStage(ctx, f.local, []string{"notes.txt"}, true); err != nil {
		t.Fatal(err)
	}
	if staged := stagedPaths(res.Status); !slices.Equal(staged, []string{"app.txt"}) {
		t.Fatalf("after unstaging: %v", staged)
	}
	res, err = f.node.gitCommit(ctx, f.local, "Change the app")
	if err != nil {
		t.Fatal(err)
	}
	if res.Commit == nil || res.Commit.Subject != "Change the app" || res.Status.Ahead != 1 {
		t.Fatalf("commit %+v ahead %d", res.Commit, res.Status.Ahead)
	}
	if len(res.Status.Files) != 1 || res.Status.Files[0].Path != "notes.txt" {
		t.Fatalf("left %+v", res.Status.Files)
	}
	runPullGit(t, f.local, "checkout", "-q", "--detach")
	putFile(t, f.local, "app.txt", "detached\n")
	runPullGit(t, f.local, "add", "app.txt")
	if _, err := f.node.gitCommit(ctx, f.local, "x"); err == nil {
		t.Fatal("committed on a detached HEAD")
	} else if code, _ := gitCodeOf(t, err); code != gitCodeDetached {
		t.Fatalf("code %s", code)
	}
}

func stagedPaths(st *GitStatus) []string {
	var out []string
	for _, f := range st.Files {
		if f.Index != "." && f.Index != "?" {
			out = append(out, f.Path)
		}
	}
	return out
}

// A pre-commit hook that refuses keeps the commit from being made, and what it said comes back.
func TestGitCommitHookRefuses(t *testing.T) {
	f := newPullFixture(t)
	hook := filepath.Join(f.local, ".git", "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho 'lint: 2 problems in app.txt' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	putFile(t, f.local, "app.txt", "changed\n")
	runPullGit(t, f.local, "add", "app.txt")
	_, err := f.node.gitCommit(context.Background(), f.local, "Change")
	if err == nil {
		t.Fatal("the hook's refusal was a commit")
	}
	if code, _ := gitCodeOf(t, err); code != gitCodeHook || !strings.Contains(err.Error(), "lint: 2 problems") {
		t.Fatalf("%s: %v", code, err)
	}
}

// A push goes to the branch's own upstream and is never forced: a forcing push refspec in the config does not apply,
// a remote that moved on is "pull first", a new branch needs set_upstream.
func TestGitPush(t *testing.T) {
	f := newPullFixture(t)
	ctx := context.Background()
	runPullGit(t, f.local, "config", "remote.origin.push", "+refs/heads/*:refs/heads/*")
	f.commit(t, f.local, "app.txt", "mine\n")
	res, err := f.node.gitPush(ctx, f.local, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.Ahead != 0 {
		t.Fatalf("ahead %d after the push", res.Status.Ahead)
	}
	runPullGit(t, f.seed, "pull", "-q", "--ff-only")
	f.upstreamCommit(t)
	f.commit(t, f.local, "app.txt", "mine again\n")
	if _, err := f.node.gitPush(ctx, f.local, false); err == nil {
		t.Fatal("pushed over the remote's newer commit")
	} else if code, _ := gitCodeOf(t, err); code != gitCodeRejected {
		t.Fatalf("code %s: %v", code, err)
	}
	if got := runPullGit(t, f.seed, "ls-remote", "origin", "refs/heads/main"); strings.Contains(got, runPullGit(t, f.local, "rev-parse", "HEAD")) {
		t.Fatal("the remote has the local commit: forced")
	}
	runPullGit(t, f.local, "switch", "-q", "-c", "feature")
	if _, err := f.node.gitPush(ctx, f.local, false); err == nil {
		t.Fatal("a new branch pushed without set_upstream")
	} else if code, _ := gitCodeOf(t, err); code != gitCodeNoUpstream {
		t.Fatalf("code %s", code)
	}
	res, err = f.node.gitPush(ctx, f.local, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.Upstream != "origin/feature" || res.Status.Ahead != 0 {
		t.Fatalf("upstream %q ahead %d", res.Status.Upstream, res.Status.Ahead)
	}
}

// Switching lists the uncommitted files the other branch has differently and does not switch; other uncommitted
// changes go along. A remote-only branch gets a local branch tracking it; a new branch starts where asked.
func TestGitSwitch(t *testing.T) {
	f := newPullFixture(t)
	ctx := context.Background()
	runPullGit(t, f.seed, "switch", "-q", "-c", "other")
	f.commit(t, f.seed, "app.txt", "other\n")
	runPullGit(t, f.seed, "push", "-q", "origin", "other")
	runPullGit(t, f.local, "fetch", "-q")
	putFile(t, f.local, "app.txt", "uncommitted\n")
	putFile(t, f.local, "scratch.txt", "mine\n")
	_, err := f.node.gitSwitch(ctx, f.local, "other", false, "")
	if err == nil {
		t.Fatal("switched over an uncommitted change the branch has differently")
	}
	if code, files := gitCodeOf(t, err); code != gitCodeConflict || !slices.Equal(files, []string{"app.txt"}) {
		t.Fatalf("%s %v", code, files)
	}
	if b := runPullGit(t, f.local, "branch", "--show-current"); b != "main" {
		t.Fatalf("on %s", b)
	}
	runPullGit(t, f.local, "checkout", "--", "app.txt")
	res, err := f.node.gitSwitch(ctx, f.local, "other", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status.Branch != "other" || res.Status.Upstream != "origin/other" {
		t.Fatalf("branch %q upstream %q", res.Status.Branch, res.Status.Upstream)
	}
	if b, _ := os.ReadFile(filepath.Join(f.local, "scratch.txt")); string(b) != "mine\n" {
		t.Fatal("the untracked file did not go along")
	}
	if res, err = f.node.gitSwitch(ctx, f.local, "fix/login", true, "main"); err != nil {
		t.Fatal(err)
	}
	if res.Status.Branch != "fix/login" || res.Status.Head.Hash != runPullGit(t, f.local, "rev-parse", "main") {
		t.Fatalf("new branch %+v", res.Status)
	}
	for _, bad := range []string{"-f", "a..b", "x y"} {
		if _, err := f.node.gitSwitch(ctx, f.local, bad, true, ""); err == nil {
			t.Fatalf("switched to %q", bad)
		}
	}
	if _, err := f.node.gitSwitch(ctx, f.local, "main", true, ""); err == nil {
		t.Fatal("created a branch that exists")
	} else if code, _ := gitCodeOf(t, err); code != gitCodeNoBranch {
		t.Fatalf("code %s", code)
	}
}

// Stash push, list, apply (a conflict lists the files and keeps the entry), pop and drop.
func TestGitStash(t *testing.T) {
	f := newPullFixture(t)
	ctx := context.Background()
	if _, err := f.node.gitStash(ctx, f.local, "push", 0, "", false, ""); err == nil {
		t.Fatal("stashed nothing")
	} else if code, _ := gitCodeOf(t, err); code != gitCodeNoStash {
		t.Fatalf("code %s", code)
	}
	putFile(t, f.local, "app.txt", "work in progress\n")
	putFile(t, f.local, "new.txt", "untracked\n")
	res, err := f.node.gitStash(ctx, f.local, "push", 0, "half done", true, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Status.Files) != 0 || len(res.Stash) != 1 || !strings.Contains(res.Stash[0].Message, "half done") {
		t.Fatalf("after push: files %v stashes %+v", res.Status.Files, res.Stash)
	}
	putFile(t, f.local, "app.txt", "something else\n")
	_, err = f.node.gitStash(ctx, f.local, "apply", 0, "", false, res.Stash[0].OID)
	if err == nil {
		t.Fatal("applied over a change to the same file")
	}
	if code, files := gitCodeOf(t, err); code != gitCodeConflict || !slices.Contains(files, "app.txt") {
		t.Fatalf("%s %v: %v", code, files, err)
	}
	runPullGit(t, f.local, "checkout", "--", "app.txt")
	if err := os.Remove(filepath.Join(f.local, "new.txt")); err != nil { // the failed apply put the untracked file back
		t.Fatal(err)
	}
	if res, err = f.node.gitStash(ctx, f.local, "pop", 0, "", false, res.Stash[0].OID); err != nil {
		t.Fatal(err)
	}
	if len(res.Stash) != 0 || len(res.Status.Files) != 2 {
		t.Fatalf("after pop: files %v stashes %+v", res.Status.Files, res.Stash)
	}
	if _, err := f.node.gitStash(ctx, f.local, "drop", 3, "", false, ""); err == nil {
		t.Fatal("dropped a stash that is not there")
	} else if code, _ := gitCodeOf(t, err); code != gitCodeStashStale {
		t.Fatalf("code %s", code)
	}
	if res, err = f.node.gitStash(ctx, f.local, "push", 0, "", false, ""); err != nil {
		t.Fatal(err)
	}
	if res, err = f.node.gitStash(ctx, f.local, "drop", 0, "", false, res.Stash[0].OID); err != nil || len(res.Stash) != 0 {
		t.Fatalf("drop: %v %+v", err, res)
	}
}

// Branches: local ones (the current marked), then remote ones without a local branch of their name.
func TestGitBranches(t *testing.T) {
	f := newPullFixture(t)
	runPullGit(t, f.seed, "push", "-q", "origin", "main:release")
	runPullGit(t, f.local, "fetch", "-q")
	runPullGit(t, f.local, "branch", "-q", "topic")
	out, err := f.node.gitBranches(context.Background(), f.local)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, b := range out["branches"].([]GitBranch) {
		name := b.Name
		if b.Current {
			name += "*"
		}
		names = append(names, name)
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"main*", "origin/release", "topic"}) {
		t.Fatalf("branches %v", names)
	}
}

// Paths outside what file.read reads are not staged.
func TestGitStageRefusesOffLimits(t *testing.T) {
	f := newPullFixture(t)
	if err := os.MkdirAll(filepath.Join(f.local, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	putFile(t, f.local, ".ssh/id_ed25519", "key\n")
	if _, err := f.node.gitStage(context.Background(), f.local, []string{".ssh/id_ed25519"}, false); err == nil {
		t.Fatal("staged a key")
	}
}
