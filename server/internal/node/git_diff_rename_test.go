package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// git.status lists a staged `git mv` as a rename, so its staged diff pairs the two names: given only the new one,
// git diff --cached shows the whole file as new. A source file.read refuses is never paired, so its old content stays
// out of the diff.
func TestGitDiffPairsAStagedRename(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Sample Dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Sample Dev")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.com")
	n := &Node{home: home}
	repo := filepath.Join(home, "repo")
	write := func(rel, body string) {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Each file's lines are its own, so git pairs every rename with the right source.
	lines := func(tag string, n int) string {
		var b strings.Builder
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "%s line %d\n", tag, i)
		}
		return b.String()
	}
	runPullGit(t, home, "init", "-q", "--initial-branch=main", repo)
	write("old.txt", lines("a", 40))
	write("old name.txt", lines("b", 40))
	write("code/id_ed25519", "SECRET-KEY\n"+lines("c", 40))
	write("notes.txt", lines("d", 40))
	runPullGit(t, repo, "add", "-A")
	runPullGit(t, repo, "commit", "-q", "-m", "Add sample files")
	for _, mv := range [][2]string{{"old.txt", "new.txt"}, {"old name.txt", "new name.txt"},
		{"code/id_ed25519", "code/key.txt"}, {"notes.txt", "moved.txt"}} {
		runPullGit(t, repo, "mv", "--", mv[0], mv[1])
	}
	write("new.txt", lines("a", 41))
	write("new name.txt", lines("b", 41))
	write("code/key.txt", lines("c", 40)) // the key's line is dropped: only a paired diff would show it
	write("notes.txt/id_ed25519", "SECRET-NESTED\n")
	runPullGit(t, repo, "add", "-A")
	ctx := context.Background()

	st, err := n.gitStatus(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, f := range st.Files {
		if f.Path == "new.txt" {
			listed = f.OrigPath == "old.txt" && f.Index == "R" && f.Added == 1 && f.Deleted == 0
		}
	}
	if !listed {
		t.Fatalf("git.status does not list new.txt as a +1 rename of old.txt: %+v", st.Files)
	}

	for _, c := range []struct{ from, to, tag string }{{"old.txt", "new.txt", "a"}, {"old name.txt", "new name.txt", "b"}} {
		d, err := n.gitDiff(ctx, repo, c.to, true)
		if err != nil {
			t.Fatalf("git.diff %s staged: %v", c.to, err)
		}
		s, _ := d["diff"].(string)
		for _, want := range []string{"rename from " + c.from, "rename to " + c.to, "+" + c.tag + " line 41"} {
			if !strings.Contains(s, want) {
				t.Errorf("git.diff %s staged lacks %q:\n%s", c.to, want, s)
			}
		}
		if strings.Contains(s, "new file mode") || strings.Contains(s, "+"+c.tag+" line 1\n") {
			t.Errorf("git.diff %s staged shows the whole file as new:\n%s", c.to, s)
		}
		if d["path"] != c.to || d["truncated"] != false {
			t.Errorf("git.diff %s staged: path %v, truncated %v", c.to, d["path"], d["truncated"])
		}
	}

	// A refused source, or a source name that now holds a folder with a key in it: the destination diffs alone, as
	// before, and neither key's text shows.
	for _, c := range []struct{ to, tag string }{{"code/key.txt", "c"}, {"moved.txt", "d"}} {
		d, err := n.gitDiff(ctx, repo, c.to, true)
		if err != nil {
			t.Fatalf("git.diff %s staged: %v", c.to, err)
		}
		s, _ := d["diff"].(string)
		if strings.Contains(s, "SECRET") {
			t.Errorf("git.diff %s staged shows a guarded file:\n%s", c.to, s)
		}
		if !strings.Contains(s, "+"+c.tag+" line 40") {
			t.Errorf("git.diff %s staged lacks its own content:\n%s", c.to, s)
		}
	}
}
