package node

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Status, history, diff and safe pull for the Git panel; every command has a timeout.

const maxDiffBytes = 512 << 10

type GitCommit struct {
	Hash    string    `json:"hash"`
	Short   string    `json:"short"`
	Subject string    `json:"subject"`
	Body    string    `json:"body,omitempty"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
	Refs    string    `json:"refs,omitempty"`
}

type GitFile struct {
	Path     string `json:"path"`
	OrigPath string `json:"orig_path,omitempty"` // renames
	// Index / Worktree are the porcelain status letters (M A D R C U ? or "."), e.g. "M." staged, ".M" unstaged.
	Index    string `json:"index"`
	Worktree string `json:"worktree"`
	Added    int    `json:"added"`
	Deleted  int    `json:"deleted"`
	Binary   bool   `json:"binary,omitempty"`
}

type GitStatus struct {
	IsRepo   bool        `json:"is_repo"`
	Root     string      `json:"root,omitempty"`
	Branch   string      `json:"branch,omitempty"` // empty when detached
	Head     *GitCommit  `json:"head,omitempty"`
	Upstream string      `json:"upstream,omitempty"`
	Ahead    int         `json:"ahead"`
	Behind   int         `json:"behind"`
	Files    []GitFile   `json:"files"`
	Stashes  int         `json:"stashes"`
	Commits  []GitCommit `json:"commits"`
}

type GitPullResult struct {
	Status  *GitStatus `json:"status"`
	Updated bool       `json:"updated"`
}

type GitShow struct {
	Commit    GitCommit `json:"commit"`
	Files     []GitFile `json:"files"`
	Diff      string    `json:"diff"`
	Truncated bool      `json:"truncated"`
}

// gitTimeout bounds each git command the panels run.
const gitTimeout = 15 * time.Second

func git(ctx context.Context, dir string, args ...string) (string, error) {
	return gitTimed(ctx, dir, gitTimeout, args...)
}

// gitCmd is a git command in dir that never prompts and never takes a lock (the status panels poll it).
func gitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "-c", "core.quotepath=off", "--no-pager"}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C",
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/usr/bin/false", "SSH_ASKPASS=/usr/bin/false", "GCM_INTERACTIVE=never")
	cmd.WaitDelay = time.Second
	return cmd
}

func gitTimed(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := gitCmd(ctx, dir, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), gitFailed(ctx, args, errb.String(), err)
	}
	return out.String(), nil
}

// gitCapped runs git as git() does, but reads at most limit bytes of what it prints: past that it stops git and returns
// the output up to its last whole line within limit, with truncated set. A huge diff then costs about limit in memory
// and time, and a cut one never ends mid-line or mid-character.
func gitCapped(ctx context.Context, dir string, limit int, args ...string) (out string, truncated bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := gitCmd(ctx, dir, args...)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	if err := cmd.Start(); err != nil {
		return "", false, gitFailed(ctx, args, "", err)
	}
	var b strings.Builder
	_, readErr := io.Copy(&b, io.LimitReader(stdout, int64(limit)+1))
	out = b.String()
	if truncated = len(out) > limit; truncated {
		cancel() // the rest is never read: stop git instead of waiting for it, whatever it exits with then
	}
	waitErr := cmd.Wait()
	if truncated {
		return cutAtLine(out, limit), true, nil
	}
	if waitErr == nil {
		waitErr = readErr
	}
	if waitErr != nil {
		return out, false, gitFailed(ctx, args, errb.String(), waitErr)
	}
	return out, false, nil
}

// gitFailed is the error for a git command that failed: timed out or canceled, or what git said on stderr.
func gitFailed(ctx context.Context, args []string, stderr string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("Git 操作超时或已取消：%w", ctx.Err())
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("git %s: %s", gitSubcommand(args), msg)
}

// cutAtLine is s cut to at most limit bytes, after its last newline there; with none (a first line longer than
// limit), at the last whole character.
func cutAtLine(s string, limit int) string {
	s = s[:min(limit, len(s))]
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[:i+1]
	}
	return string(trimPartialRune([]byte(s)))
}

// gitSubcommand is the command args run (after git's own options), for errors.
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-c" || a == "-C":
			i++ // and its value
		case !strings.HasPrefix(a, "-"):
			return a
		}
	}
	return ""
}

// guardRepoPath refuses a path in the repository at root (relative to it, as git/status gives it) that file.read
// would refuse: a key, a credentials folder, the data folder's secrets, iCloud Drive, through any link on the way. A
// path that is not there is not refused for that (a deleted file's diff, an older version).
func (n *Node) guardRepoPath(root, path string) error {
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	if _, err := n.realPath(filepath.Clean(p)); errors.Is(err, errOffLimits) || errors.Is(err, errICloud) {
		return err
	}
	return nil
}

func (n *Node) gitDir(cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return "", fmt.Errorf("empty cwd")
	}
	if cwd == "~" || strings.HasPrefix(cwd, "~/") {
		cwd = filepath.Join(n.home, strings.TrimPrefix(cwd, "~"))
	}
	if strings.Contains(cwd, "/Library/Mobile Documents") {
		return "", fmt.Errorf("iCloud Drive is not readable remotely")
	}
	return filepath.Clean(cwd), nil
}

// logFormat separates fields with \x1f and records with \x1e.
const logFormat = "--format=%H%x1f%h%x1f%s%x1f%an%x1f%aI%x1f%D%x1f%b%x1e"

func parseLog(out string) []GitCommit {
	var list []GitCommit
	for _, rec := range strings.Split(out, "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		f := strings.Split(rec, "\x1f")
		if len(f) < 7 {
			continue
		}
		t, _ := time.Parse(time.RFC3339, f[4])
		list = append(list, GitCommit{Hash: f[0], Short: f[1], Subject: f[2], Author: f[3], Date: t, Refs: f[5],
			Body: strings.TrimSpace(f[6])})
	}
	return list
}

func (n *Node) gitStatus(ctx context.Context, cwd string) (*GitStatus, error) {
	dir, err := n.gitDir(cwd)
	if err != nil {
		return nil, err
	}
	root, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return &GitStatus{IsRepo: false, Files: []GitFile{}, Commits: []GitCommit{}}, nil
	}
	st := &GitStatus{IsRepo: true, Root: strings.TrimSpace(root), Files: []GitFile{}, Commits: []GitCommit{}}
	out, err := git(ctx, st.Root, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	st.Files = parseStatusV2(out, st)
	// Line counts for the change list: unstaged and staged numstat merged per path.
	counts := map[string][2]int{}
	binary := map[string]bool{}
	for _, args := range [][]string{{"diff", "--numstat", "-z"}, {"diff", "--cached", "--numstat", "-z"}} {
		if o, err := git(ctx, st.Root, args...); err == nil {
			for path, c := range parseNumstat(o) {
				if c[0] < 0 {
					binary[path] = true
					continue
				}
				prev := counts[path]
				counts[path] = [2]int{prev[0] + c[0], prev[1] + c[1]}
			}
		}
	}
	for i := range st.Files {
		c := counts[st.Files[i].Path]
		st.Files[i].Added, st.Files[i].Deleted, st.Files[i].Binary = c[0], c[1], binary[st.Files[i].Path]
	}
	if o, err := git(ctx, st.Root, "stash", "list"); err == nil && strings.TrimSpace(o) != "" {
		st.Stashes = strings.Count(strings.TrimSpace(o), "\n") + 1
	}
	if o, err := git(ctx, st.Root, "log", "-n", "30", logFormat); err == nil {
		st.Commits = parseLog(o)
		if len(st.Commits) > 0 {
			h := st.Commits[0]
			st.Head = &h
		}
	}
	return st, nil
}

func (n *Node) gitPull(ctx context.Context, cwd string) (*GitPullResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	before, err := n.gitStatus(ctx, cwd)
	if err != nil {
		return nil, err
	}
	if !before.IsRepo {
		return nil, fmt.Errorf("不是 Git 仓库")
	}
	if before.Branch == "" {
		return nil, fmt.Errorf("当前是分离的 HEAD，请先切换到分支")
	}
	if before.Upstream == "" {
		return nil, fmt.Errorf("没有上游分支，请先在电脑上设置上游")
	}
	if len(before.Files) > 0 {
		return nil, fmt.Errorf("工作区有未提交的改动，请先在电脑上提交或 stash 后再拉取")
	}
	if _, err := gitTimed(ctx, before.Root, 60*time.Second,
		"-c", "pull.rebase=false", "-c", "merge.autoStash=false", "-c", "rebase.autoStash=false",
		"pull", "--ff-only", "--no-rebase", "--no-autostash", "--no-recurse-submodules"); err != nil {
		return nil, fmt.Errorf("拉取失败（不会自动合并、变基或覆盖本地改动）：%w", err)
	}
	after, err := n.gitStatus(ctx, before.Root)
	if err != nil {
		return nil, fmt.Errorf("拉取已执行，但刷新状态失败：%w", err)
	}
	updated := before.Head != nil && after.Head != nil && before.Head.Hash != after.Head.Hash
	return &GitPullResult{Status: after, Updated: updated}, nil
}

// parseStatusV2 reads `git status --porcelain=v2 --branch -z`.
func parseStatusV2(out string, st *GitStatus) []GitFile {
	files := []GitFile{}
	recs := strings.Split(out, "\x00")
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		switch {
		case strings.HasPrefix(r, "# branch.head "):
			if b := strings.TrimPrefix(r, "# branch.head "); b != "(detached)" {
				st.Branch = b
			}
		case strings.HasPrefix(r, "# branch.upstream "):
			st.Upstream = strings.TrimPrefix(r, "# branch.upstream ")
		case strings.HasPrefix(r, "# branch.ab "):
			for _, f := range strings.Fields(strings.TrimPrefix(r, "# branch.ab ")) {
				v, _ := strconv.Atoi(f[1:])
				if f[0] == '+' {
					st.Ahead = v
				} else {
					st.Behind = v
				}
			}
		case strings.HasPrefix(r, "1 "): // ordinary: 1 XY sub mH mI mW hH hI path
			f := strings.SplitN(r, " ", 9)
			if len(f) == 9 {
				files = append(files, GitFile{Path: f[8], Index: f[1][:1], Worktree: f[1][1:]})
			}
		case strings.HasPrefix(r, "2 "): // renamed / copied: 2 XY sub mH mI mW hH hI Xscore path, then origPath
			f := strings.SplitN(r, " ", 10)
			if len(f) == 10 {
				g := GitFile{Path: f[9], Index: f[1][:1], Worktree: f[1][1:]}
				if i+1 < len(recs) {
					g.OrigPath = recs[i+1]
					i++
				}
				files = append(files, g)
			}
		case strings.HasPrefix(r, "u "): // unmerged: u XY sub m1 m2 m3 mW h1 h2 h3 path
			f := strings.SplitN(r, " ", 11)
			if len(f) == 11 {
				files = append(files, GitFile{Path: f[10], Index: "U", Worktree: "U"})
			}
		case strings.HasPrefix(r, "? "):
			files = append(files, GitFile{Path: r[2:], Index: "?", Worktree: "?"})
		}
	}
	return files
}

// parseNumstat reads `git diff --numstat -z`; binary files get -1.
func parseNumstat(out string) map[string][2]int {
	res := map[string][2]int{}
	recs := strings.Split(out, "\x00")
	for i := 0; i < len(recs); i++ {
		f := strings.SplitN(recs[i], "\t", 3)
		if len(f) != 3 {
			continue
		}
		path := f[2]
		if path == "" && i+2 < len(recs) { // rename: "a\td\t" then old, new
			path = recs[i+2]
			i += 2
		}
		a, errA := strconv.Atoi(f[0])
		d, errD := strconv.Atoi(f[1])
		if errA != nil || errD != nil {
			res[path] = [2]int{-1, -1}
			continue
		}
		res[path] = [2]int{a, d}
	}
	return res
}

// gitDiff returns the working-tree (or staged) diff of one file; untracked files diff against /dev/null.
func (n *Node) gitDiff(ctx context.Context, cwd, path string, staged bool) (map[string]any, error) {
	dir, err := n.gitDir(cwd)
	if err != nil {
		return nil, err
	}
	root, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a git repository")
	}
	root = strings.TrimSpace(root)
	// The diff of an untracked file is the whole file: the path is guarded as file.read guards it, and taken as one
	// file, not a pattern (a "*" would diff every changed file, the guarded ones too).
	if err := n.guardRepoPath(root, path); err != nil {
		return nil, err
	}
	// A folder ("." or "code") is a pathspec for every changed file under it: each of those is guarded too.
	names := []string{"--literal-pathspecs", "diff", "--name-only", "-z"}
	if staged {
		names = append(names, "--cached")
	}
	if o, err := git(ctx, root, append(names, "--", path)...); err == nil {
		for _, name := range strings.Split(o, "\x00") {
			if name == "" {
				continue
			}
			if err := n.guardRepoPath(root, name); err != nil {
				return nil, err
			}
		}
	}
	var out string
	var truncated bool
	if staged {
		args := []string{"--literal-pathspecs", "diff", "--cached", "--no-color", "--", path}
		if from := n.stagedRenameSource(ctx, root, path); from != "" {
			args = []string{"--literal-pathspecs", "diff", "--cached", "-M", "--no-color", "--", from, path}
		}
		out, truncated, err = gitCapped(ctx, root, maxDiffBytes, args...)
	} else {
		out, truncated, err = gitCapped(ctx, root, maxDiffBytes, "--literal-pathspecs", "diff", "--no-color", "--", path)
		if err == nil && strings.TrimSpace(out) == "" {
			if o, _ := git(ctx, root, "--literal-pathspecs", "ls-files", "--others", "--exclude-standard", "--", path); strings.TrimSpace(o) != "" {
				// --no-index exits 1 when files differ; the output is still the diff.
				out, truncated, _ = gitCapped(ctx, root, maxDiffBytes, "diff", "--no-color", "--no-index", "--", "/dev/null", path)
				err = nil
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"path": path, "diff": out, "truncated": truncated}, nil
}

// stagedRenameSource is the source of the staged rename (or copy) whose destination is path, or "" when there is none.
// Rename detection pairs only files inside the pathspec, so without its source a renamed file diffs as wholly new. The
// source is left out (path diffs alone, as before) when file.read refuses it, or when its name covers more than the
// one file in the index (it is a folder now): a pathspec takes everything under that name into the diff.
func (n *Node) stagedRenameSource(ctx context.Context, root, path string) string {
	o, err := git(ctx, root, "diff", "--cached", "-M", "--name-status", "-z")
	if err != nil {
		return ""
	}
	from := ""
	recs := strings.Split(o, "\x00")
	for i := 0; i+1 < len(recs) && from == ""; i += 2 {
		k := recs[i]
		if k == "" {
			i--
			continue
		}
		if (k[0] == 'R' || k[0] == 'C') && i+2 < len(recs) { // from, to
			if recs[i+2] == path {
				from = recs[i+1]
			}
			i++
		}
	}
	if from == "" || n.guardRepoPath(root, from) != nil {
		return ""
	}
	names, err := git(ctx, root, "--literal-pathspecs", "diff", "--cached", "--no-renames", "--name-only", "-z", "--", from)
	if err != nil || names != from+"\x00" {
		return ""
	}
	return from
}

func (n *Node) gitShow(ctx context.Context, cwd, hash string) (*GitShow, error) {
	dir, err := n.gitDir(cwd)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(hash, "-") || strings.TrimSpace(hash) == "" {
		return nil, fmt.Errorf("bad commit")
	}
	o, err := git(ctx, dir, "log", "-n", "1", logFormat, hash)
	if err != nil {
		return nil, err
	}
	cs := parseLog(o)
	if len(cs) == 0 {
		return nil, fmt.Errorf("commit %s not found", hash)
	}
	res := &GitShow{Commit: cs[0], Files: []GitFile{}}
	// The three show calls below take the same flags, whatever the user's config says: -M pairs renames (diff.renames),
	// and a merge is diffed against its first parent, the changes it brought in (-m --first-parent, with log.diffMerges
	// pinned for the gits that read it). git's default for a merge, the combined diff, is empty for a clean merge while
	// numstat lists the first parent's files.
	show := func(args ...string) []string {
		return append([]string{"-c", "log.diffMerges=first-parent", "show", "-M", "-m", "--first-parent"}, args...)
	}
	if o, err := git(ctx, dir, show("--numstat", "-z", "--format=", hash)...); err == nil {
		for path, c := range parseNumstat(o) {
			f := GitFile{Path: path, Index: "M", Worktree: "."}
			if c[0] < 0 {
				f.Binary = true
			} else {
				f.Added, f.Deleted = c[0], c[1]
			}
			res.Files = append(res.Files, f)
		}
	}
	sortFiles(res.Files)
	// The diff is the content of every file the commit touched: the ones file.read refuses (either side of a rename
	// or copy) are left out of it, by name. Without the names, or without a working tree to check them against (a
	// bare repository, cwd inside .git), the commit and its files are shown without the diff. Renames and a merge's
	// files are named the same way here as in the patch (show).
	o, err = git(ctx, dir, show("--name-status", "-z", "--format=", hash)...)
	if err != nil {
		return res, nil
	}
	root, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return res, nil
	}
	root = strings.TrimSpace(root)
	kinds := map[string]string{}
	refused := map[string]bool{} // checked once: a range names a path once per commit
	isRefused := func(p string) bool {
		r, ok := refused[p]
		if !ok {
			r = n.guardRepoPath(root, p) != nil
			refused[p] = r
		}
		return r
	}
	excluded := map[string]bool{}
	var exclude []string
	recs := strings.Split(o, "\x00")
	for i := 0; i+1 < len(recs); i += 2 {
		k := recs[i]
		if k == "" {
			i--
			continue
		}
		paths := recs[i+1 : i+2]
		if (k[0] == 'R' || k[0] == 'C') && i+2 < len(recs) {
			paths = recs[i+1 : i+3] // from, to
			i++
		}
		p := paths[len(paths)-1]
		kinds[p] = k[:1]
		// Without its pair, the other side of a rename is diffed whole: both go when either is refused.
		if slices.ContainsFunc(paths, isRefused) {
			for _, p := range paths {
				if !excluded[p] {
					excluded[p] = true
					exclude = append(exclude, ":(top,exclude,literal)"+p)
				}
			}
		}
	}
	for i := range res.Files {
		if k, ok := kinds[res.Files[i].Path]; ok {
			res.Files[i].Index = k
		}
	}
	diff, truncated, err := gitCapped(ctx, dir, maxDiffBytes, append(show("--no-color", "--format=", hash, "--"), exclude...)...)
	if err == nil {
		res.Diff, res.Truncated = diff, truncated
	}
	return res, nil
}

func (n *Node) gitLog(ctx context.Context, cwd string, skip, limit int) ([]GitCommit, error) {
	dir, err := n.gitDir(cwd)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	o, err := git(ctx, dir, "log", "-n", strconv.Itoa(limit), "--skip", strconv.Itoa(max(skip, 0)), logFormat)
	if err != nil {
		return nil, err
	}
	list := parseLog(o)
	if list == nil {
		list = []GitCommit{}
	}
	return list, nil
}

func sortFiles(fs []GitFile) {
	sort.Slice(fs, func(i, j int) bool { return fs[i].Path < fs[j].Path })
}
