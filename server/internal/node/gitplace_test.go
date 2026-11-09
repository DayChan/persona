package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func writeGitFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The layouts git itself writes: a main checkout, a linked worktree elsewhere on disk (Orca, Claude Code and herdr keep
// them in their own folders), a submodule and a detached HEAD.
func TestReadGitPlace(t *testing.T) {
	home := t.TempDir()
	repo := filepath.Join(home, "Code", "app")
	writeGitFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
	if err := os.MkdirAll(filepath.Join(repo, "web", "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	// git worktree add ~/orca/workspaces/app/fix-login -b fix/login: an absolute gitdir, a relative commondir.
	tree := filepath.Join(home, "orca", "workspaces", "app", "fix-login")
	meta := filepath.Join(repo, ".git", "worktrees", "fix-login")
	writeGitFile(t, filepath.Join(tree, ".git"), "gitdir: "+meta+"\n")
	writeGitFile(t, filepath.Join(meta, "commondir"), "../..\n")
	writeGitFile(t, filepath.Join(meta, "HEAD"), "ref: refs/heads/fix/login\n")

	// A worktree inside the repository with a relative gitdir (git worktree add --relative-paths), detached.
	inner := filepath.Join(repo, ".claude", "worktrees", "probe")
	innerMeta := filepath.Join(repo, ".git", "worktrees", "probe")
	writeGitFile(t, filepath.Join(inner, ".git"), "gitdir: ../../../.git/worktrees/probe\n")
	writeGitFile(t, filepath.Join(innerMeta, "commondir"), "../..\n")
	writeGitFile(t, filepath.Join(innerMeta, "HEAD"), "0123456789abcdef0123456789abcdef01234567\n")

	// A submodule: a .git file, no commondir: its own project.
	sub := filepath.Join(repo, "vendor", "lib")
	subMeta := filepath.Join(repo, ".git", "modules", "lib")
	writeGitFile(t, filepath.Join(sub, ".git"), "gitdir: ../../.git/modules/lib\n")
	writeGitFile(t, filepath.Join(subMeta, "HEAD"), "ref: refs/heads/trunk\n")

	// git -C vendor/lib worktree add ../lib-linked -b linked: the shared git dir is under the parent's .git/modules,
	// and its config names the submodule's checkout.
	writeGitFile(t, filepath.Join(subMeta, "config"), "[core]\n\tbare = false\n\tworktree = ../../../vendor/lib\n")
	subTree := filepath.Join(repo, "vendor", "lib-linked")
	subTreeMeta := filepath.Join(subMeta, "worktrees", "lib-linked")
	writeGitFile(t, filepath.Join(subTree, ".git"), "gitdir: "+subTreeMeta+"\n")
	writeGitFile(t, filepath.Join(subTreeMeta, "commondir"), "../..\n")
	writeGitFile(t, filepath.Join(subTreeMeta, "HEAD"), "ref: refs/heads/linked\n")

	// A bare repository's worktree: the bare repository is the project.
	bare := filepath.Join(home, "bare.git")
	writeGitFile(t, filepath.Join(bare, "config"), "[core]\n\tbare = true\n")
	bareTree := filepath.Join(home, "bare-wt")
	writeGitFile(t, filepath.Join(bareTree, ".git"), "gitdir: "+filepath.Join(bare, "worktrees", "bare-wt")+"\n")
	writeGitFile(t, filepath.Join(bare, "worktrees", "bare-wt", "commondir"), "../..\n")
	writeGitFile(t, filepath.Join(bare, "worktrees", "bare-wt", "HEAD"), "ref: refs/heads/main\n")

	plain := filepath.Join(home, "notes")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name, cwd string
		want      gitPlace
	}{
		{"main checkout", repo, gitPlace{repo, repo, "main"}},
		{"folder inside the main checkout", filepath.Join(repo, "web", "src"), gitPlace{repo, repo, "main"}},
		{"trailing slash", repo + "/", gitPlace{repo, repo, "main"}},
		{"linked worktree elsewhere", tree, gitPlace{repo, tree, "fix/login"}},
		{"detached worktree inside the repo", inner, gitPlace{repo, inner, ""}},
		{"submodule", sub, gitPlace{sub, sub, "trunk"}},
		{"submodule's linked worktree", subTree, gitPlace{sub, subTree, "linked"}},
		{"bare repository's worktree", bareTree, gitPlace{bare, bareTree, "main"}},
		{"not a repository", plain, gitPlace{}},
		{"relative path", "Code/app", gitPlace{}},
		{"empty", "", gitPlace{}},
	} {
		if got := readGitPlace(c.cwd); got != c.want {
			t.Errorf("%s: readGitPlace(%q) = %+v, want %+v", c.name, c.cwd, got, c.want)
		}
	}
}

func TestFillGitPlaceKeepsHostRoot(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "app")
	writeGitFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")

	s := proto.Session{Cwd: repo}
	fillGitPlace(&s)
	if s.RepoRoot != repo || s.Worktree != repo || s.Branch != "main" {
		t.Fatalf("filled %q %q %q", s.RepoRoot, s.Worktree, s.Branch)
	}

	// A host that names the project (Paseo, a Codex project) keeps its own root.
	hosted := proto.Session{Cwd: repo, RepoRoot: "/elsewhere/app"}
	fillGitPlace(&hosted)
	if hosted.RepoRoot != "/elsewhere/app" || hosted.Worktree != repo || hosted.Branch != "main" {
		t.Fatalf("hosted %q %q %q", hosted.RepoRoot, hosted.Worktree, hosted.Branch)
	}

	// The assistant's conversations are not in a project.
	chat := proto.Session{Cwd: repo, Assistant: true}
	fillGitPlace(&chat)
	if chat.RepoRoot != "" || chat.Worktree != "" || chat.Branch != "" {
		t.Fatalf("assistant got %q %q %q", chat.RepoRoot, chat.Worktree, chat.Branch)
	}
}
