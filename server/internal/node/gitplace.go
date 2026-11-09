package node

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// gitPlace is where a working directory sits in git: the repository's main checkout (root), the working tree it is in
// (worktree: root itself, or a linked worktree's folder) and the branch checked out there ("" when detached). The apps
// list machine → project (root) → worktree (labelled branch) → sessions with it (#1805).
type gitPlace struct{ root, worktree, branch string }

const (
	// A list goes out every second or so while agents work; a branch switch shows within this.
	gitPlaceTTL   = 10 * time.Second
	gitPlaceLimit = 1024
)

type gitPlaceEntry struct {
	place gitPlace
	at    time.Time
}

var gitPlaces = struct {
	sync.Mutex
	entries map[string]gitPlaceEntry
}{entries: map[string]gitPlaceEntry{}}

// fillGitPlace sets s's RepoRoot (unless its host already named it: Paseo, a Codex project), Worktree and Branch from
// its working directory. The assistant's own conversations are not in a project.
func fillGitPlace(s *proto.Session) {
	if s.Cwd == "" || s.Assistant {
		return
	}
	p := cachedGitPlace(s.Cwd)
	if p.worktree == "" {
		return
	}
	if s.RepoRoot == "" {
		s.RepoRoot = p.root
	}
	if s.Worktree == "" {
		s.Worktree = p.worktree
	}
	if s.Branch == "" {
		s.Branch = p.branch
	}
}

func cachedGitPlace(cwd string) gitPlace {
	gitPlaces.Lock()
	e, ok := gitPlaces.entries[cwd]
	gitPlaces.Unlock()
	if ok && time.Since(e.at) < gitPlaceTTL {
		return e.place
	}
	p := readGitPlace(cwd)
	// Misses are cached the same short time: a later git init or a restored folder shows without a restart.
	gitPlaces.Lock()
	if len(gitPlaces.entries) >= gitPlaceLimit {
		clear(gitPlaces.entries)
	}
	gitPlaces.entries[cwd] = gitPlaceEntry{place: p, at: time.Now()}
	gitPlaces.Unlock()
	return p
}

// readGitPlace finds the .git nearest above cwd and reads it the way git does, without running git (this runs for every
// listed session): a .git folder is a main checkout; a .git file points at a linked worktree's (or a submodule's) git
// dir, whose commondir, when it has one, is the main checkout's .git. Paths stay as the session reports them (no
// symlink resolution), so a cwd always lies under its worktree.
func readGitPlace(cwd string) gitPlace {
	if cwd == "" || !filepath.IsAbs(cwd) {
		return gitPlace{}
	}
	for dir := filepath.Clean(cwd); ; {
		dotGit := filepath.Join(dir, ".git")
		if st, err := os.Stat(dotGit); err == nil {
			switch {
			case st.IsDir():
				return gitPlace{root: dir, worktree: dir, branch: headBranch(dotGit)}
			case st.Mode().IsRegular():
				gitDir := pointedGitDir(dotGit, dir)
				if gitDir == "" {
					return gitPlace{}
				}
				root := dir
				if common := commonDir(gitDir); common != "" {
					root = mainCheckout(common)
				}
				return gitPlace{root: root, worktree: dir, branch: headBranch(gitDir)}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return gitPlace{}
		}
		dir = parent
	}
}

// pointedGitDir reads a .git file ("gitdir: <path>", relative to the folder it is in).
func pointedGitDir(file, dir string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	path, ok := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
	if path = strings.TrimSpace(path); !ok || path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path)
}

// commonDir is a linked worktree's shared git dir (its "commondir" file, relative to gitDir); "" for a submodule or a
// main checkout, which have none.
func commonDir(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(string(b))
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(gitDir, path)
	}
	return filepath.Clean(path)
}

// mainCheckout is the working tree a linked worktree's shared git dir belongs to: the folder holding a ".git" dir, a
// submodule's checkout (git keeps a submodule's git dir under the parent's .git/modules and names its checkout in
// core.worktree), else the bare repository itself, which has no checkout of its own.
func mainCheckout(common string) string {
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	if tree := coreWorktree(common); tree != "" {
		return tree
	}
	return common
}

// coreWorktree is core.worktree from a git dir's config, relative to that git dir; "" when unset or unreadable.
func coreWorktree(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "config"))
	if err != nil {
		return ""
	}
	core := false
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			core = strings.EqualFold(strings.TrimSpace(strings.Trim(line, "[]")), "core")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !core || !ok || !strings.EqualFold(strings.TrimSpace(key), "worktree") {
			continue
		}
		path := strings.Trim(strings.TrimSpace(value), `"`)
		if path == "" {
			return ""
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(gitDir, path)
		}
		return filepath.Clean(path)
	}
	return ""
}

// headBranch is the branch HEAD names ("ref: refs/heads/<branch>"); "" when detached or unreadable.
func headBranch(gitDir string) string {
	b, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	ref, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "ref:")
	if !ok {
		return ""
	}
	branch, _ := strings.CutPrefix(strings.TrimSpace(ref), "refs/heads/")
	return branch
}
