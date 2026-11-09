package node

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A reviewer's copy (#2173): the agent's HEAD detached, with its staged, unstaged and untracked change, under the
// repository's checkouts directory; what the reviewer does there never reaches the agent's tree, and the copy is one
// worktree.remove takes away (forced: it carries the change).
func TestWorktreeSnapshot(t *testing.T) {
	w := worktreeNode(t)
	ctx := context.Background()
	prep, err := w.n.worktreePrepare(ctx, "", "w1", "")
	if err != nil {
		t.Fatal(err)
	}
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(dir, name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "<" + err.Error() + ">"
		}
		return string(b)
	}
	write(w.repo, "app.txt", "one\ntwo\n") // unstaged
	write(w.repo, "staged.txt", "staged\n")
	runPullGit(t, w.repo, "add", "staged.txt")
	write(w.repo, "notes/new.txt", "untracked\n")
	write(w.repo, ".gitignore", "build/\n")
	write(w.repo, "build/out.bin", "ignored\n")
	head := strings.TrimSpace(runPullGit(t, w.repo, "rev-parse", "HEAD"))

	snap, err := w.n.worktreeSnapshot(ctx, "", filepath.Join(w.repo, "notes"), "Fix the login crash")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Path != filepath.Join(prep.Path, "review-fix-the-login-crash") || snap.Head != head || resolved(t, snap.Root) != resolved(t, w.repo) {
		t.Fatalf("snapshot %+v (checkouts under %s, head %s)", snap, prep.Path, head)
	}
	for name, want := range map[string]string{"app.txt": "one\ntwo\n", "staged.txt": "staged\n", "notes/new.txt": "untracked\n", ".gitignore": "build/\n"} {
		if got := read(snap.Path, name); got != want {
			t.Errorf("%s in the copy = %q, want %q", name, got, want)
		}
	}
	if _, err := os.Stat(filepath.Join(snap.Path, "build", "out.bin")); !os.IsNotExist(err) {
		t.Errorf("an ignored file was copied: %v", err)
	}
	if b := strings.TrimSpace(runPullGit(t, snap.Path, "branch", "--show-current")); b != "" {
		t.Fatalf("the copy is on branch %q, not detached", b)
	}

	// The reviewer writes, commits and checks out in its copy: the agent's tree, branch and index stay as they were.
	write(snap.Path, "app.txt", "rewritten by the reviewer\n")
	write(snap.Path, "reviewer.txt", "scratch\n")
	runPullGit(t, snap.Path, "add", "-A")
	runPullGit(t, snap.Path, "commit", "-m", "reviewer scratch")
	if got := read(w.repo, "app.txt"); got != "one\ntwo\n" {
		t.Fatalf("the agent's file changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(w.repo, "reviewer.txt")); !os.IsNotExist(err) {
		t.Fatalf("the reviewer's file reached the agent's tree: %v", err)
	}
	if got := strings.TrimSpace(runPullGit(t, w.repo, "rev-parse", "HEAD")); got != head {
		t.Fatalf("the agent's HEAD moved: %s", got)
	}
	if got := strings.TrimSpace(runPullGit(t, w.repo, "diff", "--cached", "--name-only")); got != "staged.txt" {
		t.Fatalf("the agent's index changed: %q", got)
	}

	// A second copy gets a folder of its own.
	again, err := w.n.worktreeSnapshot(ctx, "", w.repo, "Fix the login crash")
	if err != nil || again.Path != snap.Path+"-2" {
		t.Fatalf("again: %+v %v", again, err)
	}

	// Removed (forced, it is dirty): gone, and the agent's tree untouched.
	if _, err := w.n.worktreeRemove(ctx, "", snap.Path, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snap.Path); !os.IsNotExist(err) {
		t.Fatalf("the copy is still there: %v", err)
	}
	if got := read(w.repo, "notes/new.txt"); got != "untracked\n" {
		t.Fatalf("the agent's untracked file: %q", got)
	}

	if _, err := w.n.worktreeSnapshot(ctx, "", t.TempDir(), "x"); codeOf(err) != "not_a_repository" {
		t.Fatalf("not a repository: %v", err)
	}
}
