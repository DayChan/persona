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

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// git.review against real repositories (docs/web/review.md, #151): each scope, renames, new files, binaries,
// deletions, odd names, generated files, a detached HEAD, no default branch, no commit yet, guarded files, and files
// too big for a page.

type reviewRepo struct {
	t    *testing.T
	node *Node
	dir  string
}

func reviewGitEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Sample Dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Sample Dev")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.com")
	return home
}

// newReviewRepo is a clone of a remote whose main has the files given, so origin/HEAD names origin/main.
func newReviewRepo(t *testing.T, files map[string]string) *reviewRepo {
	t.Helper()
	home := reviewGitEnv(t)
	remote, seed, local := filepath.Join(home, "remote.git"), filepath.Join(home, "seed"), filepath.Join(home, "code", "app")
	runPullGit(t, home, "init", "-q", "--bare", "--initial-branch=main", remote)
	runPullGit(t, home, "init", "-q", "--initial-branch=main", seed)
	s := &reviewRepo{t: t, node: &Node{home: home, state: newState()}, dir: seed}
	for name, body := range files {
		s.write(name, body)
	}
	s.git("add", "-A")
	s.git("commit", "-q", "-m", "Start")
	s.git("remote", "add", "origin", remote)
	s.git("push", "-q", "-u", "origin", "main")
	runPullGit(t, home, "clone", "-q", remote, local)
	return &reviewRepo{t: t, node: s.node, dir: local}
}

func (r *reviewRepo) write(name, body string) {
	r.t.Helper()
	p := filepath.Join(r.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *reviewRepo) git(args ...string) string {
	r.t.Helper()
	return runPullGit(r.t, r.dir, args...)
}

func (r *reviewRepo) review(scope, base string, paths ...string) *proto.GitReview {
	r.t.Helper()
	res, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{Cwd: r.dir, Scope: scope, Base: base, Paths: paths})
	if err != nil {
		r.t.Fatalf("git.review %s %s: %v", scope, base, err)
	}
	return res
}

func fileOf(t *testing.T, res *proto.GitReview, path string) proto.GitReviewFile {
	t.Helper()
	for _, f := range res.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("%s is not among %v", path, pathsOf(res))
	return proto.GitReviewFile{}
}

func pathsOf(res *proto.GitReview) []string {
	var out []string
	for _, f := range res.Files {
		out = append(out, f.Path)
	}
	return out
}

func reviewLines(n int, word string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(word)
		b.WriteString(" ")
		b.WriteString(strings.Repeat("x", i%7))
		b.WriteString("\n")
	}
	return b.String()
}

func TestGitReviewUncommitted(t *testing.T) {
	r := newReviewRepo(t, map[string]string{
		"app.go":      "package app\n\nfunc A() {}\n\nfunc B() {}\n",
		"old.txt":     reviewLines(12, "moved"),
		"gone.txt":    "bye\n",
		"logo.png":    "\x89PNG\r\n\x1a\n\x00\x00",
		"docs/a b.md": "spaces\n",
		"日本.txt":      "一\n",
		"tab\tx.txt":  "tab\n",
	})
	r.write("app.go", "package app\n\nfunc A() { println() }\n\nfunc B() {}\n")
	r.git("add", "app.go")
	r.write("app.go", "package app\n\nfunc A() { println() }\n\nfunc B() {}\n\nfunc C() {}\n") // staged, then more
	r.git("mv", "old.txt", "new.txt")
	r.git("rm", "-q", "gone.txt")
	r.write("logo.png", "\x89PNG\r\n\x1a\n\x00\x01")
	r.write("docs/a b.md", "spaces, edited\n")
	r.write("日本.txt", "二\n")
	r.write("tab\tx.txt", "tab, edited\n")
	r.write("fresh/new.ts", "export const a = 1\nexport const b = 2") // untracked, no newline at the end
	r.write("fresh/empty.txt", "")

	res := r.review(proto.ReviewUncommitted, "")
	head := r.git("rev-parse", "HEAD")
	if res.Base.Hash != head || res.Head == nil || res.Head.Hash != head || res.Head.Branch != "main" || res.Scope != "uncommitted" {
		t.Fatalf("base %+v head %+v", res.Base, res.Head)
	}
	want := []string{"app.go", "docs/a b.md", "fresh/empty.txt", "fresh/new.ts", "gone.txt", "logo.png", "new.txt", "tab\tx.txt", "日本.txt"}
	if got := pathsOf(res); !slices.Equal(got, want) {
		t.Fatalf("files %q, want %q", got, want)
	}

	app := fileOf(t, res, "app.go")
	if app.Status != "modified" || app.Added != 3 || app.Deleted != 1 || app.Revision == "" || len(app.Hunks) != 1 {
		t.Fatalf("app.go %+v", app)
	}
	if h := app.Hunks[0]; h.OldStart != 1 || h.NewStart != 1 || !slices.Contains(h.Lines, "+func A() { println() }") || !slices.Contains(h.Lines, "-func A() {}") || !slices.Contains(h.Lines, "+func C() {}") {
		t.Fatalf("app.go hunk %+v", h)
	}
	if f := fileOf(t, res, "new.txt"); f.Status != "renamed" || f.OldPath != "old.txt" || f.Added != 0 || f.Deleted != 0 || len(f.Hunks) != 0 || f.Big {
		t.Fatalf("rename %+v", f)
	}
	if f := fileOf(t, res, "gone.txt"); f.Status != "deleted" || f.Deleted != 1 || len(f.Hunks) != 1 || f.Hunks[0].Lines[0] != "-bye" || f.Hunks[0].NewStart != 1 {
		t.Fatalf("deleted %+v", f)
	}
	if f := fileOf(t, res, "logo.png"); !f.Binary || len(f.Hunks) != 0 || f.Big {
		t.Fatalf("binary %+v", f)
	}
	for _, p := range []string{"docs/a b.md", "日本.txt", "tab\tx.txt"} {
		if f := fileOf(t, res, p); len(f.Hunks) != 1 || f.Added != 1 || f.Deleted != 1 || f.Big {
			t.Fatalf("%q %+v", p, f)
		}
	}
	nw := fileOf(t, res, "fresh/new.ts")
	if !nw.Untracked || nw.Status != "added" || nw.Added != 2 || len(nw.Hunks) != 1 ||
		!slices.Equal(nw.Hunks[0].Lines, []string{"+export const a = 1", "+export const b = 2", `\ No newline at end of file`}) {
		t.Fatalf("untracked %+v", nw)
	}
	if f := fileOf(t, res, "fresh/empty.txt"); !f.Untracked || f.Added != 0 || len(f.Hunks) != 0 {
		t.Fatalf("empty %+v", f)
	}

	// the same through the RPC, from a folder inside the repository
	params, _ := json.Marshal(map[string]any{"cwd": filepath.Join(r.dir, "docs"), "scope": "uncommitted"})
	out, err := r.node.handle(context.Background(), "git.review", params)
	if err != nil {
		t.Fatal(err)
	}
	if got := pathsOf(out.(*proto.GitReview)); !slices.Equal(got, want) {
		t.Fatalf("from docs/: %q", got)
	}
	// a clean tree has nothing to review
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "Save")
	if res := r.review(proto.ReviewUncommitted, ""); len(res.Files) != 0 {
		t.Fatalf("clean tree: %q", pathsOf(res))
	}
}

func TestGitReviewHunkNumbers(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"list.txt": reviewLines(40, "line")})
	lines := strings.Split(strings.TrimSuffix(reviewLines(40, "line"), "\n"), "\n")
	// line 5 deleted, line 30 changed: two hunks; the deletion's new side starts after line 4
	edited := append(append([]string{}, lines[:4]...), lines[5:]...)
	edited[28] = "changed"
	r.write("list.txt", strings.Join(edited, "\n")+"\n")
	f := fileOf(t, r.review(proto.ReviewUncommitted, ""), "list.txt")
	if len(f.Hunks) != 2 {
		t.Fatalf("hunks %+v", f.Hunks)
	}
	if h := f.Hunks[0]; h.OldStart != 2 || h.NewStart != 2 || !slices.Contains(h.Lines, "-"+lines[4]) {
		t.Fatalf("first %+v", h)
	}
	if h := f.Hunks[1]; h.OldStart != 27 || h.NewStart != 26 || !slices.Contains(h.Lines, "+changed") {
		t.Fatalf("second %+v", h)
	}
	// a hunk that only takes lines away at the very start: the new side starts at 1
	r.write("list.txt", strings.Join(lines[3:], "\n")+"\n")
	h := fileOf(t, r.review(proto.ReviewUncommitted, ""), "list.txt").Hunks[0]
	if h.OldStart != 1 || h.NewStart != 1 || h.Lines[0] != "-"+lines[0] {
		t.Fatalf("deleted at the start %+v", h)
	}
	if hunkStart("4", "0") != 5 || hunkStart("0", "0") != 1 || hunkStart("7", "") != 7 || hunkStart("7", "3") != 7 {
		t.Fatal("hunkStart")
	}
}

func TestGitReviewBranch(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"app.go": "package app\n", "README.md": "# App\n"})
	start := r.git("rev-parse", "HEAD")
	r.git("switch", "-q", "-c", "feat/x")
	r.write("feature.go", "package app\n\nfunc F() {}\n")
	r.git("add", "feature.go")
	r.git("commit", "-q", "-m", "Add F")
	r.write("app.go", "package app\n\n// edited, not committed\n")
	// main moves on meanwhile: its commit is not the branch's change
	other := filepath.Join(filepath.Dir(r.dir), "other")
	runPullGit(t, filepath.Dir(r.dir), "clone", "-q", r.git("remote", "get-url", "origin"), other)
	if err := os.WriteFile(filepath.Join(other, "main-only.txt"), []byte("later\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runPullGit(t, other, "add", "-A")
	runPullGit(t, other, "commit", "-q", "-m", "Main moves on")
	runPullGit(t, other, "push", "-q")
	r.git("fetch", "-q")

	res := r.review(proto.ReviewBranch, "")
	if res.Base.Ref != "origin/main" || res.Base.Hash != start || res.Base.Subject != "Start" || res.Head.Branch != "feat/x" {
		t.Fatalf("base %+v head %+v", res.Base, res.Head)
	}
	if got := pathsOf(res); !slices.Equal(got, []string{"app.go", "feature.go"}) {
		t.Fatalf("branch files %q", got)
	}
	if f := fileOf(t, res, "feature.go"); f.Status != "added" || f.Untracked || f.Added != 3 {
		t.Fatalf("committed %+v", f)
	}
	// uncommitted alone is only the edit
	if got := pathsOf(r.review(proto.ReviewUncommitted, "")); !slices.Equal(got, []string{"app.go"}) {
		t.Fatalf("uncommitted %q", got)
	}
	// since a commit: the commit's own change is not in it
	since := r.review(proto.ReviewCommit, r.git("rev-parse", "--short", "HEAD"))
	if got := pathsOf(since); !slices.Equal(got, []string{"app.go"}) || since.Base.Hash != r.git("rev-parse", "HEAD") {
		t.Fatalf("since HEAD %q %+v", got, since.Base)
	}
	if got := pathsOf(r.review(proto.ReviewCommit, start)); !slices.Equal(got, []string{"app.go", "feature.go"}) {
		t.Fatalf("since start %q", got)
	}
	// against another branch named: where the branch left it
	if res := r.review(proto.ReviewBranch, "origin/main"); res.Base.Hash != start {
		t.Fatalf("named %+v", res.Base)
	}

	// detached: the same changes, no branch
	r.git("switch", "-q", "--detach")
	res = r.review(proto.ReviewBranch, "")
	if res.Head == nil || res.Head.Branch != "" || !slices.Equal(pathsOf(res), []string{"app.go", "feature.go"}) {
		t.Fatalf("detached %+v %q", res.Head, pathsOf(res))
	}
}

func TestGitReviewDefaultBranch(t *testing.T) {
	reviewGitEnv(t)
	dir := t.TempDir()
	n := &Node{home: dir, state: newState()}
	runPullGit(t, dir, "init", "-q", "--initial-branch=trunk")
	r := &reviewRepo{t: t, node: n, dir: dir}
	r.write("a.txt", "a\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "Start")
	r.git("switch", "-q", "-c", "work")
	r.write("a.txt", "b\n")
	ctx := context.Background()
	var re *rpc.Error
	_, err := n.gitReview(ctx, proto.GitReviewRequest{Cwd: dir, Scope: proto.ReviewBranch})
	if !errors.As(err, &re) || re.Code != proto.CodeNoDefaultBranch {
		t.Fatalf("no default branch: %v", err)
	}
	if res := r.review(proto.ReviewBranch, "trunk"); res.Base.Ref != "trunk" || len(res.Files) != 1 {
		t.Fatalf("named trunk %+v", res)
	}
	// a local main counts when there is no remote
	r.git("branch", "main", "trunk")
	if res := r.review(proto.ReviewBranch, ""); res.Base.Ref != "main" {
		t.Fatalf("local main %+v", res.Base)
	}
	// histories with nothing in common
	r.git("commit", "-q", "-am", "Work")
	r.git("switch", "-q", "--orphan", "alone")
	r.write("b.txt", "b\n")
	r.git("add", "b.txt")
	r.git("commit", "-q", "-m", "Alone")
	if _, err := n.gitReview(ctx, proto.GitReviewRequest{Cwd: dir, Scope: proto.ReviewBranch}); !errors.As(err, &re) || re.Code != proto.CodeNoMergeBase {
		t.Fatalf("unrelated: %v", err)
	}
}

func TestGitReviewRefusals(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "a\n"})
	ctx := context.Background()
	for _, c := range []struct {
		req  proto.GitReviewRequest
		code string
	}{
		{proto.GitReviewRequest{Cwd: r.dir, Scope: "everything"}, proto.CodeBadScope},
		{proto.GitReviewRequest{Cwd: r.dir, Scope: proto.ReviewCommit}, proto.CodeBadScope},
		{proto.GitReviewRequest{Cwd: r.dir, Scope: proto.ReviewCommit, Base: "nope"}, proto.CodeBadBase},
		{proto.GitReviewRequest{Cwd: r.dir, Scope: proto.ReviewCommit, Base: "--output=/tmp/x"}, proto.CodeBadBase},
		{proto.GitReviewRequest{Cwd: r.dir, Scope: proto.ReviewCommit, Base: "HEAD:a.txt"}, proto.CodeBadBase},
		{proto.GitReviewRequest{Cwd: r.dir, Scope: proto.ReviewBranch, Base: "no-such-branch"}, proto.CodeBadBase},
		{proto.GitReviewRequest{Cwd: t.TempDir(), Scope: proto.ReviewUncommitted}, proto.CodeNotGit},
	} {
		_, err := r.node.gitReview(ctx, c.req)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != c.code {
			t.Errorf("%+v: %v, want %s", c.req, err, c.code)
		}
	}
	if _, err := r.node.gitReview(ctx, proto.GitReviewRequest{Cwd: r.dir, Scope: proto.ReviewUncommitted, Paths: []string{"../x"}}); err == nil {
		t.Error("a path outside the repository was taken")
	}
}

func TestGitReviewBeforeTheFirstCommit(t *testing.T) {
	reviewGitEnv(t)
	dir := t.TempDir()
	runPullGit(t, dir, "init", "-q", "--initial-branch=main")
	r := &reviewRepo{t: t, node: &Node{home: dir, state: newState()}, dir: dir}
	r.write("staged.txt", "s\n")
	r.git("add", "staged.txt")
	r.write("loose.txt", "l\n")
	for _, scope := range []string{proto.ReviewUncommitted, proto.ReviewBranch} {
		res, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{Cwd: dir, Scope: scope, Base: map[string]string{"branch": "main"}[scope]})
		if scope == proto.ReviewBranch {
			// main has no commit yet: not a branch to compare with
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != proto.CodeBadBase {
				t.Fatalf("branch before the first commit: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !res.Base.Empty || res.Head != nil || !slices.Equal(pathsOf(res), []string{"loose.txt", "staged.txt"}) {
			t.Fatalf("%s: %+v %q", scope, res.Base, pathsOf(res))
		}
		if f := fileOf(t, res, "staged.txt"); f.Status != "added" || f.Untracked || f.Hunks[0].Lines[0] != "+s" {
			t.Fatalf("staged %+v", f)
		}
		if f := fileOf(t, res, "loose.txt"); !f.Untracked || f.Hunks[0].Lines[0] != "+l" {
			t.Fatalf("loose %+v", f)
		}
	}
}

func TestGitReviewLeavesOutGuardedFiles(t *testing.T) {
	// keys are refused wherever they are; a home's credentials folders where a home kept in Git has them
	r := newReviewRepo(t, map[string]string{"main.go": "package main\n", "deploy/id_ed25519": "SECRET-1\n", "keys/id_rsa": "SECRET-RSA\n"})
	r.write("main.go", "package main\n\n// edited\n")
	r.write("deploy/id_ed25519", "SECRET-2\n")
	r.git("mv", "keys/id_rsa", "keys/rsa.txt") // moved out from under its name: its diff is still the key
	r.write("new/id_ecdsa", "SECRET-NEW\n")
	res := r.review(proto.ReviewUncommitted, "")
	blob, _ := json.Marshal(res)
	if strings.Contains(string(blob), "SECRET") {
		t.Fatalf("a guarded file's content was sent: %s", blob)
	}
	for _, p := range []string{"deploy/id_ed25519", "new/id_ecdsa"} {
		if f := fileOf(t, res, p); !f.Refused || len(f.Hunks) != 0 || f.Added != 0 {
			t.Fatalf("%s %+v", p, f)
		}
	}
	if f := fileOf(t, res, "keys/rsa.txt"); !f.Refused || f.OldPath != "keys/id_rsa" {
		t.Fatalf("moved key %+v", f)
	}
	if f := fileOf(t, res, "main.go"); f.Refused || len(f.Hunks) != 1 {
		t.Fatalf("main.go %+v", f)
	}
	// asked for on its own, still refused
	alone := r.review(proto.ReviewUncommitted, "", "deploy/id_ed25519")
	if blob, _ := json.Marshal(alone); strings.Contains(string(blob), "SECRET") || len(alone.Files) != 1 || !alone.Files[0].Refused {
		t.Fatalf("alone: %s", blob)
	}

	// a home kept in Git (dotfiles)
	home := r.node.home
	h := &reviewRepo{t: t, node: r.node, dir: home}
	h.git("init", "-q", "--initial-branch=main")
	h.write(".gitignore", "code/\nremote.git/\nseed/\n")
	h.write("notes.md", "plain\n")
	h.write(".netrc", "SECRET-NETRC\n")
	h.git("add", "-A")
	h.git("commit", "-q", "-m", "Dotfiles")
	h.write("notes.md", "plain, edited\n")
	h.write(".netrc", "SECRET-NETRC-2\n")
	h.write(".aws/credentials", "SECRET-AWS\n")
	res = h.review(proto.ReviewUncommitted, "")
	if blob, _ := json.Marshal(res); strings.Contains(string(blob), "SECRET") {
		t.Fatalf("a home's credentials were sent: %s", blob)
	}
	if !fileOf(t, res, ".netrc").Refused || !fileOf(t, res, ".aws/credentials").Refused || fileOf(t, res, "notes.md").Refused {
		t.Fatalf("home %+v", res.Files)
	}
}

func TestGitReviewBigFiles(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"big.txt": reviewLines(4000, "old"), "small.txt": "a\n", "z.txt": "z\n"})
	r.write("big.txt", reviewLines(4000, "new"))
	r.write("small.txt", "b\n")
	r.write("z.txt", "zz\n")
	r.write("huge-new.txt", reviewLines(5000, "fresh"))
	res := r.review(proto.ReviewUncommitted, "")
	if f := fileOf(t, res, "big.txt"); !f.Big || len(f.Hunks) != 0 || f.Added != 4000 || f.Deleted != 4000 {
		t.Fatalf("big %+v", f)
	}
	if f := fileOf(t, res, "huge-new.txt"); !f.Big || len(f.Hunks) != 0 || f.Added != 5000 {
		t.Fatalf("big new %+v", f)
	}
	for _, p := range []string{"small.txt", "z.txt"} {
		if f := fileOf(t, res, p); f.Big || len(f.Hunks) != 1 {
			t.Fatalf("%s %+v", p, f)
		}
	}
	// asked for on its own, it comes
	alone := r.review(proto.ReviewCommit, res.Base.Hash, "big.txt")
	if len(alone.Files) != 1 || alone.Files[0].Big || len(alone.Files[0].Hunks) == 0 {
		t.Fatalf("alone %+v", alone.Files)
	}
	if alone := r.review(proto.ReviewUncommitted, "", "huge-new.txt"); alone.Files[0].Big || len(alone.Files[0].Hunks[0].Lines) != 5000 {
		t.Fatalf("new alone %+v", alone.Files[0].Big)
	}

	// a full page: the files after it come without their hunks, to be asked for
	saved := reviewPage
	t.Cleanup(func() { reviewPage = saved })
	reviewPage = reviewBudget{left: 8, fileBytes: 1 << 20, fileLines: 1 << 20}
	r.write("big.txt", reviewLines(4000, "old"))
	res = r.review(proto.ReviewUncommitted, "")
	if f := fileOf(t, res, "small.txt"); f.Big || len(f.Hunks) != 1 {
		t.Fatalf("first on a small page %+v", f)
	}
	if f := fileOf(t, res, "z.txt"); !f.Big || len(f.Hunks) != 0 || f.Added != 1 {
		t.Fatalf("after a full page %+v", f)
	}
	if f := fileOf(t, res, "huge-new.txt"); !f.Big {
		t.Fatalf("untracked after a full page %+v", f)
	}
}

func TestGitReviewTypeChange(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "alpha\n", "link": "plain file\n", "z.txt": "zed\n"})
	// a file that became a symlink: the patch has two sections for it, the deletion and then the creation
	if err := os.Remove(filepath.Join(r.dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(r.dir, "link")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	r.write("z.txt", "zed changed\n")
	wantLink := [][]string{{"-plain file"}, {"+a.txt", `\ No newline at end of file`}}
	linkHunks := func(f proto.GitReviewFile) [][]string {
		var out [][]string
		for _, h := range f.Hunks {
			out = append(out, h.Lines)
		}
		return out
	}
	res := r.review(proto.ReviewUncommitted, "")
	if f := fileOf(t, res, "link"); f.Status != "type_changed" || f.Big || !slices.EqualFunc(linkHunks(f), wantLink, slices.Equal) {
		t.Fatalf("type change %+v", f)
	}
	// the file after it keeps its hunks
	if f := fileOf(t, res, "z.txt"); f.Big || len(f.Hunks) != 1 || !slices.Equal(f.Hunks[0].Lines, []string{"-zed", "+zed changed"}) {
		t.Fatalf("after a type change %+v", f)
	}
	// asked for on its own, it has both halves
	if alone := r.review(proto.ReviewUncommitted, "", "link"); len(alone.Files) != 1 || !slices.EqualFunc(linkHunks(alone.Files[0]), wantLink, slices.Equal) {
		t.Fatalf("alone %+v", alone.Files)
	}

	// both halves count against the one file's share: three lines over a share of two make it Big
	saved := reviewPage
	t.Cleanup(func() { reviewPage = saved })
	reviewPage = reviewBudget{left: 1 << 20, fileBytes: 1 << 20, fileLines: 2}
	res = r.review(proto.ReviewUncommitted, "")
	if f := fileOf(t, res, "link"); !f.Big || len(f.Hunks) != 0 {
		t.Fatalf("type change over its share %+v", f)
	}
	if f := fileOf(t, res, "z.txt"); f.Big || len(f.Hunks) != 1 {
		t.Fatalf("after a big type change %+v", f)
	}
}

func TestGitReviewGeneratedAndConflicts(t *testing.T) {
	r := newReviewRepo(t, map[string]string{".gitattributes": "*.pb.go linguist-generated\n", "api.pb.go": "package api\n", "a.txt": "base\n"})
	r.write("api.pb.go", "package api\n\n// regenerated\n")
	r.write("hand.go", "package api\n")
	res := r.review(proto.ReviewUncommitted, "")
	if !fileOf(t, res, "api.pb.go").Generated || fileOf(t, res, "hand.go").Generated {
		t.Fatalf("generated %+v", res.Files)
	}

	// a conflict: the file with its markers, as modified
	r.git("checkout", "-q", "--", "api.pb.go")
	r.git("switch", "-q", "-c", "side")
	r.write("a.txt", "side\n")
	r.git("commit", "-q", "-am", "Side")
	r.git("switch", "-q", "main")
	r.write("a.txt", "main\n")
	r.git("commit", "-q", "-am", "Main")
	if _, err := git(context.Background(), r.dir, "merge", "-q", "side"); err == nil {
		t.Fatal("the merge did not conflict")
	}
	res = r.review(proto.ReviewUncommitted, "")
	f := fileOf(t, res, "a.txt")
	if f.Status != "modified" || len(f.Hunks) != 1 || !slices.Contains(f.Hunks[0].Lines, "+<<<<<<< HEAD") {
		t.Fatalf("conflict %+v in %q", f, pathsOf(res))
	}
	if n := strings.Count(strings.Join(pathsOf(res), "\n"), "a.txt"); n != 1 {
		t.Fatalf("a.txt listed %d times", n)
	}
}

func TestGitQuote(t *testing.T) {
	if got := gitQuote("a\tb\"c\\d\x01é"); got != `a\tb\"c\\d\001é` {
		t.Fatalf("got %q", got)
	}
	if !headerNames(`diff --git "a/x\ty" "b/x\ty"`, "x\ty") || !headerNames("diff --git a/x b/x", "x") || headerNames("diff --git a/xy b/xy", "y") {
		t.Fatal("headerNames")
	}
}
