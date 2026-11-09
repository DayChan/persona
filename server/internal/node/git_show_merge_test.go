package node

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// showRepo is a throwaway repository kept in a temporary home (so file.read's guard applies to it), with git's
// global and system config out of the way and a fixed author.
func showRepo(t *testing.T) (n *Node, home string, write func(rel, body string)) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Sample Dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Sample Dev")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.com")
	write = func(rel, body string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runPullGit(t, home, "init", "-q", "--initial-branch=main")
	return &Node{home: home}, home, write
}

// A merge is shown as its first-parent diff: the files the merge brought in are listed and each has its section in
// the patch. Before, numstat listed them while the patch was the combined diff, empty for a clean merge, so every
// listed file opened an empty diff. The user's log.diffMerges does not change that.
func TestGitShowMergeIsItsFirstParentDiff(t *testing.T) {
	n, home, write := showRepo(t)
	write("f.txt", "one\n")
	write("g.txt", "two\n")
	runPullGit(t, home, "add", "-A")
	runPullGit(t, home, "commit", "-q", "-m", "Add sample files")
	runPullGit(t, home, "checkout", "-q", "-b", "side")
	write("f.txt", "one, edited\n")
	runPullGit(t, home, "commit", "-q", "-am", "Edit f.txt")
	runPullGit(t, home, "checkout", "-q", "main")
	runPullGit(t, home, "merge", "-q", "--no-ff", "--no-edit", "side")
	ctx := context.Background()

	for _, diffMerges := range []string{"", "combined", "off"} {
		if diffMerges != "" {
			runPullGit(t, home, "config", "log.diffMerges", diffMerges)
		}
		s, err := n.gitShow(ctx, "~", "HEAD")
		if err != nil {
			t.Fatalf("log.diffMerges=%q: %v", diffMerges, err)
		}
		if len(s.Files) != 1 || s.Files[0].Path != "f.txt" || s.Files[0].Index != "M" ||
			s.Files[0].Added != 1 || s.Files[0].Deleted != 1 {
			t.Errorf("log.diffMerges=%q: files %+v, want f.txt M +1 -1", diffMerges, s.Files)
		}
		if !strings.Contains(s.Diff, "diff --git a/f.txt b/f.txt") || !strings.Contains(s.Diff, "+one, edited") {
			t.Errorf("log.diffMerges=%q: the merge's diff has no f.txt section:\n%s", diffMerges, s.Diff)
		}
	}
}

// With diff.renames off, a committed rename is still one file: numstat pairs it like name-status and the patch do,
// so the list does not show the old name as a second file with no section of its own in the patch.
func TestGitShowRenameIsOneFileWithRenamesOff(t *testing.T) {
	n, home, write := showRepo(t)
	var lines strings.Builder
	for i := 1; i <= 40; i++ {
		lines.WriteString("line " + strconv.Itoa(i) + "\n")
	}
	write("old.txt", lines.String())
	runPullGit(t, home, "add", "-A")
	runPullGit(t, home, "commit", "-q", "-m", "Add old.txt")
	runPullGit(t, home, "mv", "old.txt", "new.txt")
	write("new.txt", strings.Replace(lines.String(), "line 20\n", "line twenty\n", 1))
	runPullGit(t, home, "commit", "-q", "-am", "Rename old.txt")
	runPullGit(t, home, "config", "diff.renames", "false")

	s, err := n.gitShow(context.Background(), "~", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 1 || s.Files[0].Path != "new.txt" || s.Files[0].Index != "R" ||
		s.Files[0].Added != 1 || s.Files[0].Deleted != 1 {
		t.Errorf("files %+v, want only new.txt R +1 -1", s.Files)
	}
	if !strings.Contains(s.Diff, "rename from old.txt") || !strings.Contains(s.Diff, "+line twenty") {
		t.Errorf("the rename's diff is missing:\n%s", s.Diff)
	}
}

// A merge's first-parent diff carries what the other branch changed: a guarded file changed only there, and a guarded
// file whose conflict the merge resolved, are listed but left out of the diff, the rest of the merge still diffs.
func TestGitShowMergeLeavesOutGuardedFiles(t *testing.T) {
	n, home, write := showRepo(t)
	write("f.txt", "one\n")
	write("code/id_ed25519", "SECRET-KEY-1\n")
	write(".aws/credentials", "SECRET-AWS-1\n")
	runPullGit(t, home, "add", "-A")
	runPullGit(t, home, "commit", "-q", "-m", "Add sample files")
	runPullGit(t, home, "checkout", "-q", "-b", "side")
	write("f.txt", "one, side\n")
	write("code/id_ed25519", "SECRET-KEY-SIDE\n")
	write(".aws/credentials", "SECRET-AWS-SIDE\n") // changed only on the side branch: a clean part of the merge
	runPullGit(t, home, "commit", "-q", "-am", "Side edit")
	runPullGit(t, home, "checkout", "-q", "main")
	write("code/id_ed25519", "SECRET-KEY-MAIN\n")
	runPullGit(t, home, "commit", "-q", "-am", "Main edit")
	ctx := context.Background()
	if _, err := git(ctx, home, "merge", "-q", "side"); err == nil {
		t.Fatal("the sample merge was meant to conflict")
	}
	write("code/id_ed25519", "SECRET-KEY-MERGED\n")
	runPullGit(t, home, "commit", "-q", "-am", "Merge side")

	s, err := n.gitShow(ctx, "~", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.Diff, "SECRET") {
		t.Errorf("the merge's diff shows a guarded file:\n%s", s.Diff)
	}
	if !strings.Contains(s.Diff, "diff --git a/f.txt b/f.txt") {
		t.Errorf("the merge's diff has no f.txt section:\n%s", s.Diff)
	}
	listed := map[string]bool{}
	for _, f := range s.Files {
		listed[f.Path] = true
	}
	for _, p := range []string{"f.txt", "code/id_ed25519", ".aws/credentials"} {
		if !listed[p] {
			t.Errorf("the merge does not list %s: %+v", p, s.Files)
		}
	}
}
