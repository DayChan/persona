package node

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// git.diff and git.show stop reading git's output at maxDiffBytes: a huge change costs about the cap in memory, not the
// whole diff, and a cut diff ends on a whole line, so it is valid UTF-8 and has no half line for the viewers to draw.

// cjkLines is n numbered lines of CJK text, about 80 bytes each.
func cjkLines(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "%06d %s\n", i, strings.Repeat("漢字かな", 6))
	}
	return b.String()
}

func checkCutDiff(t *testing.T, what, diff string, truncated bool) {
	t.Helper()
	if !truncated {
		t.Errorf("%s: truncated = false for a diff over the cap", what)
	}
	if len(diff) > maxDiffBytes || len(diff) < maxDiffBytes-1024 {
		t.Errorf("%s: %d bytes, want just under %d", what, len(diff), maxDiffBytes)
	}
	if !utf8.ValidString(diff) {
		t.Errorf("%s: cut diff is not valid UTF-8 (ends %q)", what, diff[max(len(diff)-16, 0):])
	}
	if !strings.HasSuffix(diff, "\n") {
		t.Errorf("%s: cut diff ends mid-line: %q", what, diff[max(len(diff)-40, 0):])
	}
}

func TestGitDiffCapEndsOnWholeLine(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"notes.txt": "start\n"})
	ctx := context.Background()
	big := cjkLines(12000) // about 1 MB

	r.write("notes.txt", big)
	d, err := r.node.gitDiff(ctx, r.dir, "notes.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	checkCutDiff(t, "unstaged", d["diff"].(string), d["truncated"].(bool))

	r.git("add", "notes.txt")
	d, err = r.node.gitDiff(ctx, r.dir, "notes.txt", true)
	if err != nil {
		t.Fatal(err)
	}
	checkCutDiff(t, "staged", d["diff"].(string), d["truncated"].(bool))

	r.write("new.txt", big) // untracked: diff --no-index against /dev/null
	d, err = r.node.gitDiff(ctx, r.dir, "new.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	checkCutDiff(t, "untracked", d["diff"].(string), d["truncated"].(bool))

	r.git("commit", "-q", "-m", "Grow notes")
	s, err := r.node.gitShow(ctx, r.dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	checkCutDiff(t, "show", s.Diff, s.Truncated)
}

// allocated is the bytes the heap handed out while f ran.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

func TestGitDiffCapReadsOnlyTheCap(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"data.txt": "start\n"})
	ctx := context.Background()
	huge := strings.Repeat("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde\n", 320000) // 20 MB
	r.write("data.txt", huge)
	r.write("other.txt", huge) // untracked
	const budget = 8 << 20

	for _, path := range []string{"data.txt", "other.txt"} {
		var d map[string]any
		var err error
		n := allocated(func() { d, err = r.node.gitDiff(ctx, r.dir, path, false) })
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("git.diff %s: %.1f MB allocated for %d bytes", path, float64(n)/(1<<20), len(d["diff"].(string)))
		if n > budget {
			t.Errorf("git.diff %s allocated %.1f MB for a capped diff, want under %d MB", path, float64(n)/(1<<20), budget>>20)
		}
		if !d["truncated"].(bool) {
			t.Errorf("git.diff %s: truncated = false", path)
		}
	}

	r.git("add", "data.txt")
	r.git("commit", "-q", "-m", "Grow data")
	var s *GitShow
	var err error
	n := allocated(func() { s, err = r.node.gitShow(ctx, r.dir, "HEAD") })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("git.show: %.1f MB allocated for %d bytes", float64(n)/(1<<20), len(s.Diff))
	if n > budget {
		t.Errorf("git.show allocated %.1f MB for a capped diff, want under %d MB", float64(n)/(1<<20), budget>>20)
	}
	if !s.Truncated {
		t.Error("git.show: truncated = false")
	}
}

// A diff under the cap is git's output, byte for byte.
func TestGitDiffCapSmallDiffUnchanged(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\ntwo\n"})
	ctx := context.Background()
	r.write("a.txt", "one\n二\nthree") // no newline at the end
	want, err := git(ctx, r.dir, "--literal-pathspecs", "diff", "--no-color", "--", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.node.gitDiff(ctx, r.dir, "a.txt", false)
	if err != nil {
		t.Fatal(err)
	}
	if d["diff"] != want || d["truncated"] != false {
		t.Errorf("git.diff = %q (truncated %v), want %q", d["diff"], d["truncated"], want)
	}

	r.git("commit", "-q", "-am", "Edit a")
	want, err = git(ctx, r.dir, "show", "-M", "--no-color", "--format=", "HEAD", "--")
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.node.gitShow(ctx, r.dir, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if s.Diff != want || s.Truncated {
		t.Errorf("git.show = %q (truncated %v), want %q", s.Diff, s.Truncated, want)
	}
}

func TestGitCappedCutsAtTheLastWholeLine(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"f.txt": "one\ntwo\nthree\n"})
	ctx := context.Background()
	for _, c := range []struct {
		limit     int
		want      string
		truncated bool
	}{
		{14, "one\ntwo\nthree\n", false}, // exactly the cap
		{13, "one\ntwo\n", true},
		{8, "one\ntwo\n", true},
		{7, "one\n", true},
		{2, "on", true}, // a first line longer than the cap
	} {
		out, truncated, err := gitCapped(ctx, r.dir, c.limit, "cat-file", "blob", "HEAD:f.txt")
		if err != nil || out != c.want || truncated != c.truncated {
			t.Errorf("limit %d: %q, %v, %v; want %q, %v", c.limit, out, truncated, err, c.want, c.truncated)
		}
	}
	if got := cutAtLine("漢字", 4); got != "漢" {
		t.Errorf("cutAtLine split a character: %q", got)
	}

	// Failures read as git()'s do.
	if _, _, err := gitCapped(ctx, r.dir, 100, "cat-file", "blob", "HEAD:missing.txt"); err == nil || !strings.HasPrefix(err.Error(), "git cat-file: ") {
		t.Errorf("missing blob: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := gitCapped(canceled, r.dir, 100, "cat-file", "blob", "HEAD:f.txt"); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}
