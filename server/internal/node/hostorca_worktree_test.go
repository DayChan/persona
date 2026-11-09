package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
)

// "New worktree…" on Orca against a real temp Git repository and a fake Orca whose worktree.create does what Orca
// 1.4.250 does: a real `git worktree add` under <home>/orca/workspaces/<repo>/<name>, a terminal opened in it, and no
// failure when the folder (even an empty one) or the branch is taken: "-2" for both instead.

type orcaWorktreeFixture struct {
	n        *Node
	o        *orcarttest.Fake
	home     string
	repo     string // the source workspace: the repository itself, on branch dev
	src      string // its Orca worktree id
	devHead  string
	terminal bool // the fake opens a terminal in each new worktree
	dir      string
}

func orcaWorktreeNode(t *testing.T) *orcaWorktreeFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Sample Dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Sample Dev")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.com")
	n, _, o, _, home := orcaNode(t)
	repo := mkdir(t, filepath.Join(home, "proj"))
	runPullGit(t, repo, "init", "--initial-branch=main")
	runPullGit(t, repo, "commit", "--allow-empty", "-m", "first")
	runPullGit(t, repo, "branch", "spare")
	runPullGit(t, repo, "checkout", "-b", "dev")
	runPullGit(t, repo, "commit", "--allow-empty", "-m", "on dev only")
	w := &orcaWorktreeFixture{n: n, o: o, home: home, repo: repo, src: "repo1::" + repo, terminal: true,
		devHead: runPullGit(t, repo, "rev-parse", "HEAD"), dir: filepath.Join(home, "orca", "workspaces", "proj")}
	src := orcarttest.Worktree(w.src, repo, "refs/heads/dev")
	o.SetWorktrees(src)
	o.Handle("worktree.create", func(p map[string]any) any {
		name, branch, base := str(p["name"]), str(p["branchNameOverride"]), str(p["baseBranch"])
		if p["nameWasGenerated"] == true && branch == "" {
			branch = "devuser/" + name
		}
		path := filepath.Join(w.dir, name)
		at, _ := branchCheckedOutAt(t.Context(), repo, branch)
		if _, err := os.Lstat(path); err == nil || at != "" {
			path, branch = path+"-2", branch+"-2" // Orca's way out of a taken folder or branch
		}
		args := []string{"worktree", "add", "-b", branch, "--", path}
		if base != "" {
			args = append(args, base)
		}
		if _, err := git(t.Context(), repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			args = []string{"worktree", "add", "--", path, branch}
		}
		if out, err := gitRun(t.Context(), repo, gitOpTimeout, args...); err != nil {
			return orcarttest.Fail{Code: "git: " + out}
		}
		id := "repo1::" + path
		o.SetWorktrees(src, orcarttest.Worktree(id, path, "refs/heads/"+branch))
		if w.terminal {
			o.SetTerminals(orcarttest.Terminal("term_new", "tab-new", "leaf-new", id, path, "refs/heads/"+branch, "zsh", ""))
		}
		return map[string]any{"worktree": map[string]any{"id": id, "path": path, "branch": "refs/heads/" + branch}}
	})
	return w
}

func TestOrcaAutomaticWorktreeUsesHostNamingAndKeepsItsSourceAndParent(t *testing.T) {
	w := orcaWorktreeNode(t)
	parent := "folder-project::" + mkdir(t, filepath.Join(w.home, "collection"))
	w.o.SetWorktrees(orcarttest.Worktree(w.src, w.repo, "refs/heads/dev"),
		orcarttest.Worktree(parent, filepath.Join(w.home, "collection"), ""))
	prep, err := request(t, w.n, "worktree.prepare", map[string]any{"workspace_id": w.src})
	if err != nil || prep["automatic_name"] != true {
		t.Fatalf("automatic naming capability: %v, %v", prep, err)
	}
	res, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": w.src, "parent_workspace_id": parent, "auto_name": true})
	if err != nil {
		t.Fatal(err)
	}
	got := w.o.Params("worktree.create")
	if len(got) != 1 || got[0]["nameWasGenerated"] != true || got[0]["branchNameOverride"] != nil || got[0]["baseBranch"] != "dev" ||
		got[0]["parentWorktree"] != "id:"+parent || got[0]["activate"] != false {
		t.Fatalf("automatic create params: %v", got)
	}
	if res["parent_workspace_id"] != parent || res["branch"] != "devuser/"+str(got[0]["name"]) ||
		res["path"] != filepath.Join(w.dir, str(got[0]["name"])) {
		t.Fatalf("must return Orca's actual identities: %v", res)
	}
	if head := runPullGit(t, str(res["path"]), "rev-parse", "HEAD"); head != w.devHead {
		t.Fatal("automatic checkout did not start at the source HEAD")
	}
}

func TestOrcaAutomaticWorktreeRefusesMissingParentsAndExplicitOverrides(t *testing.T) {
	w := orcaWorktreeNode(t)
	for _, fields := range []map[string]any{
		{"parent_workspace_id": "gone"}, {"branch": "feature/x"}, {"path": filepath.Join(w.home, "custom")},
	} {
		fields["workspace_id"], fields["auto_name"] = w.src, true
		if _, err := request(t, w.n, "worktree.create", fields); err == nil {
			t.Fatalf("automatic create accepted invalid fields: %v", fields)
		}
	}
	if w.o.Count("worktree.create") != 0 {
		t.Fatal("refused requests reached Orca")
	}
}

func (w *orcaWorktreeFixture) create(t *testing.T, branch string) (map[string]any, error) {
	t.Helper()
	return request(t, w.n, "worktree.create", map[string]any{"workspace_id": w.src, "branch": branch,
		"path": filepath.Join(w.dir, strings.ReplaceAll(branch, "/", "-"))})
}

// The dialog offers Orca's folder; the new worktree is Orca's, on a new branch from the workspace's branch, under the
// workspace in Orca's sidebar, not activated, and the terminal Orca opened in it is the one to go to.
func TestOrcaWorktreeCreate(t *testing.T) {
	w := orcaWorktreeNode(t)
	prep, err := request(t, w.n, "worktree.prepare", map[string]any{"workspace_id": w.src})
	if err != nil {
		t.Fatal(err)
	}
	if prep["path"] != w.dir || prep["repoName"] != "proj" || prep["branch"] != "dev" || prep["choose_parent"] != true {
		t.Errorf("prepare = %v", prep)
	}
	res, err := w.create(t, "feat/x")
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(w.dir, "feat-x")
	if res["workspace_id"] != "repo1::"+dest || res["path"] != dest || res["branch"] != "feat/x" ||
		!strings.Contains(str(res["terminal"]), "tab-new") {
		t.Errorf("create = %v", res)
	}
	got := w.o.Params("worktree.create")
	if len(got) != 1 || got[0]["repo"] != "id:repo1" || got[0]["name"] != "feat-x" ||
		got[0]["branchNameOverride"] != "feat/x" || got[0]["baseBranch"] != "dev" ||
		got[0]["parentWorktree"] != "id:"+w.src || got[0]["activate"] != false {
		t.Errorf("worktree.create params %v", got)
	}
	if head := runPullGit(t, dest, "rev-parse", "HEAD"); head != w.devHead {
		t.Errorf("the new branch starts at %s, not the workspace's HEAD %s", head, w.devHead)
	}
	if bad := w.o.Invalid(); len(bad) != 0 {
		t.Errorf("Orca's schema refused %v", bad)
	}
}

func TestOrcaWorktreeParentCanBeAnEmptyFolderInAnotherProject(t *testing.T) {
	w := orcaWorktreeNode(t)
	parent := "folder-project::" + mkdir(t, filepath.Join(w.home, "collection"))
	w.o.SetWorktrees(orcarttest.Worktree(w.src, w.repo, "refs/heads/dev"),
		orcarttest.Worktree(parent, filepath.Join(w.home, "collection"), ""))
	_, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": w.src, "parent_workspace_id": parent,
		"branch": "nested", "path": filepath.Join(w.dir, "nested")})
	if err != nil {
		t.Fatal(err)
	}
	got := w.o.Params("worktree.create")
	if len(got) != 1 || got[0]["repo"] != "id:repo1" || got[0]["parentWorktree"] != "id:"+parent || got[0]["baseBranch"] != "dev" {
		t.Fatalf("source and parent were conflated: %v", got)
	}
}

func TestOrcaWorktreeMissingParentIsRefusedBeforeCreation(t *testing.T) {
	w := orcaWorktreeNode(t)
	_, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": w.src, "parent_workspace_id": "gone",
		"branch": "nested", "path": filepath.Join(w.dir, "nested")})
	if err == nil || w.o.Count("worktree.create") != 0 {
		t.Fatalf("missing parent created a checkout: %v", err)
	}
}

// An existing local branch is checked out as it is: no base is sent, Orca does not make a new branch.
func TestOrcaWorktreeChecksOutAnExistingBranch(t *testing.T) {
	w := orcaWorktreeNode(t)
	if _, err := w.create(t, "spare"); err != nil {
		t.Fatal(err)
	}
	got := w.o.Params("worktree.create")
	if len(got) != 1 || got[0]["baseBranch"] != nil {
		t.Errorf("worktree.create params %v", got)
	}
	if b := branchOf(t, filepath.Join(w.dir, "spare")); b != "spare" {
		t.Errorf("checked out %q", b)
	}
}

// Orca would quietly make "<branch>-2" for a branch checked out elsewhere; the node refuses before asking it.
func TestOrcaWorktreeRefusesABranchInUse(t *testing.T) {
	w := orcaWorktreeNode(t)
	runPullGit(t, w.repo, "worktree", "add", "--", filepath.Join(w.home, "busy"), "spare")
	for _, b := range []string{"dev", "spare"} {
		if _, err := w.create(t, b); err == nil || !strings.Contains(err.Error(), "已经在") {
			t.Errorf("branch %s in use: err = %v", b, err)
		}
	}
	if n := w.o.Count("worktree.create"); n != 0 {
		t.Errorf("Orca was asked %d times", n)
	}
}

// Orca's folder setting is its own: when Orca puts the worktree elsewhere the answer says where it is; with no
// terminal in it (yet) the worktree is still made.
func TestOrcaWorktreeWhereOrcaPutIt(t *testing.T) {
	w := orcaWorktreeNode(t)
	w.dir, w.terminal = filepath.Join(w.home, "elsewhere", "proj"), false
	saved := orcaWorktreeTerminalWait
	orcaWorktreeTerminalWait = 100 * time.Millisecond
	t.Cleanup(func() { orcaWorktreeTerminalWait = saved })
	res, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": w.src, "branch": "feat-y",
		"path": filepath.Join(w.home, "orca", "workspaces", "proj", "feat-y")})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(w.dir, "feat-y"); res["path"] != want || res["terminal"] != nil {
		t.Errorf("create = %v, want path %s and no terminal", res, want)
	}
}

// Orca would put an empty folder's worktree at "<name>-2" on "<branch>-2"; the node refuses before asking it.
func TestOrcaWorktreeRefusesATakenFolder(t *testing.T) {
	w := orcaWorktreeNode(t)
	mkdir(t, filepath.Join(w.dir, "feat-z"))
	if _, err := w.create(t, "feat-z"); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Errorf("err = %v", err)
	}
	if n := w.o.Count("worktree.create"); n != 0 {
		t.Errorf("Orca was asked %d times", n)
	}
}

// When Orca's own folder is taken (a folder setting of its own, a branch checked out meanwhile), it makes another
// branch: that is an error naming what Orca made, not a quiet success on a branch the user did not ask for.
func TestOrcaWorktreeReportsARenamedBranch(t *testing.T) {
	w := orcaWorktreeNode(t)
	w.dir = filepath.Join(w.home, "elsewhere", "proj")
	mkdir(t, filepath.Join(w.dir, "feat-z"))
	_, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": w.src, "branch": "feat-z",
		"path": filepath.Join(w.home, "orca", "workspaces", "proj", "feat-z")})
	if err == nil || !strings.Contains(err.Error(), "feat-z-2") {
		t.Errorf("err = %v, want it to name the branch Orca made", err)
	}
}

// A detached workspace's new branch starts at its commit, as with herdr, not at Orca's base ref.
func TestOrcaWorktreeFromADetachedHead(t *testing.T) {
	w := orcaWorktreeNode(t)
	runPullGit(t, w.repo, "checkout", "--detach", "spare")
	spareHead := runPullGit(t, w.repo, "rev-parse", "HEAD")
	if _, err := w.create(t, "from-detached"); err != nil {
		t.Fatal(err)
	}
	if got := w.o.Params("worktree.create"); len(got) != 1 || got[0]["baseBranch"] != spareHead {
		t.Errorf("worktree.create params %v, want baseBranch %s", got, spareHead)
	}
	if head := runPullGit(t, filepath.Join(w.dir, "from-detached"), "rev-parse", "HEAD"); head != spareHead {
		t.Errorf("starts at %s, want %s", head, spareHead)
	}
}

// The assistant names the workspace by a directory in it (no space id): the deepest Orca worktree holding it is the
// source, and a directory outside every worktree is refused before Orca is asked.
func TestOrcaWorktreeByDirectory(t *testing.T) {
	w := orcaWorktreeNode(t)
	child := filepath.Join(w.dir, "other")
	runPullGit(t, w.repo, "worktree", "add", "-b", "other", "--", child)
	childID := "repo1::" + child
	w.o.SetWorktrees(orcarttest.Worktree(w.src, w.repo, "refs/heads/dev"), orcarttest.Worktree(childID, child, "refs/heads/other"))
	sub := mkdir(t, filepath.Join(child, "pkg"))
	prep, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": sub})
	if err != nil || prep["branch"] != "other" {
		t.Fatalf("prepare = %v, %v", prep, err)
	}
	if _, err := request(t, w.n, "worktree.create", map[string]any{"cwd": sub, "branch": "feat-c",
		"path": filepath.Join(w.dir, "feat-c")}); err != nil {
		t.Fatal(err)
	}
	if got := w.o.Params("worktree.create"); len(got) != 1 || got[0]["parentWorktree"] != "id:"+childID ||
		got[0]["baseBranch"] != "other" {
		t.Errorf("worktree.create params %v", got)
	}
	outside := mkdir(t, filepath.Join(w.home, "notes"))
	if _, err := request(t, w.n, "worktree.create", map[string]any{"cwd": outside, "branch": "feat-d",
		"path": filepath.Join(w.dir, "feat-d")}); err == nil || !strings.Contains(err.Error(), "不在") {
		t.Errorf("a directory outside every workspace: err = %v", err)
	}
	if n := w.o.Count("worktree.create"); n != 1 {
		t.Errorf("Orca was asked %d times", n)
	}
}

// A workspace Orca does not list (or another machine's) is not Orca's to branch from.
func TestOrcaWorktreeUnknownWorkspace(t *testing.T) {
	w := orcaWorktreeNode(t)
	w.o.SetWorktrees(orcarttest.Worktree(w.src, w.repo, "refs/heads/dev"))
	if _, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": "repo1::/nowhere", "branch": "b",
		"path": filepath.Join(w.dir, "b")}); err == nil {
		t.Error("created from a workspace Orca does not list")
	}
	if n := w.o.Count("worktree.create"); n != 0 {
		t.Errorf("Orca was asked %d times", n)
	}
}
