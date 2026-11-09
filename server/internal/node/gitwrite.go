package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The Git panel's writes (#6121): stage, commit, push, switch branches, stash. Each is one click of the user's in a
// client, which confirms the ones that leave the machine or may lose work first; nothing here forces, resets, stashes
// on its own or skips a hook. A failure comes back as an rpc.Error with a code the clients explain (gitCode*) and git's
// own words in the message.

// Codes a Git panel write refuses with (rpc.Error.Code; the hub answers 409 with them).
const (
	gitCodeNotRepo          = "git_not_repo"          // the folder is not in a Git repository
	gitCodeDetached         = "git_detached"          // HEAD is detached: no branch to commit to or push
	gitCodeNothingStaged    = "git_nothing_staged"    // a commit with nothing staged
	gitCodeHook             = "git_hook_failed"       // a commit refused before it was made (a pre-commit or commit-msg hook, signing); its output is the message
	gitCodeIdentity         = "git_identity"          // git has no user.name / user.email on that machine
	gitCodeNoUpstream       = "git_no_upstream"       // a push of a branch with no upstream, without set_upstream
	gitCodeNoRemote         = "git_no_remote"         // no remote to push to
	gitCodeRejected         = "git_push_rejected"     // the remote has commits this branch does not: pull first
	gitCodeAuth             = "git_auth"              // the remote refused the credentials, or there are none
	gitCodeConflict         = "git_conflict"          // uncommitted changes would be overwritten (Data.files)
	gitCodeBusy             = "git_busy"              // another git holds the repository's lock (the agent's own)
	gitCodeNoBranch         = "git_no_branch"         // no such branch, or a new branch's name is taken or not valid
	gitCodeStashStale       = "git_stash_stale"       // refresh and reselect: the index no longer names the selected object
	gitCodeStashUnsupported = "git_stash_unsupported" // safe stash mutation is unavailable on this repository
	gitCodeStashConflicted  = "git_stash_conflicted"  // an apply / pop stopped on conflicts: the content is in the tree with markers, the entry kept (Data.files)
	gitCodeNoStash          = "git_no_stash"          // no such stash entry, or nothing to stash
	gitCodeFailed           = "git_failed"            // anything else git refused; its words are the message
)

// gitWrite dispatches the Git panel's writes and pull request calls.
func (n *Node) gitWrite(ctx context.Context, method string, params json.RawMessage) (any, error) {
	var p struct {
		Cwd         string   `json:"cwd"`
		Paths       []string `json:"paths"`
		Unstage     bool     `json:"unstage"`
		Message     string   `json:"message"`
		SetUpstream bool     `json:"set_upstream"`
		Branch      string   `json:"branch"`
		Create      bool     `json:"create"`
		From        string   `json:"from"`
		Op          string   `json:"op"`
		Index       *int     `json:"index"`
		OID         string   `json:"oid"`
		Untracked   bool     `json:"untracked"`
		Title       string   `json:"title"`
		Body        string   `json:"body"`
		Base        string   `json:"base"`
		Draft       bool     `json:"draft"`
		Number      int      `json:"number"`
		Method      string   `json:"method"`
		Auto        bool     `json:"auto"`
		AutoOff     bool     `json:"auto_off"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	switch method {
	case "git.stage":
		return n.gitStage(ctx, p.Cwd, p.Paths, p.Unstage)
	case "git.commit":
		return n.gitCommit(ctx, p.Cwd, p.Message)
	case "git.push":
		return n.gitPush(ctx, p.Cwd, p.SetUpstream)
	case "git.branches":
		return n.gitBranches(ctx, p.Cwd)
	case "git.switch":
		return n.gitSwitch(ctx, p.Cwd, p.Branch, p.Create, p.From)
	case "git.stash":
		index := -1
		if p.Index != nil {
			index = *p.Index
		}
		return n.gitStash(ctx, p.Cwd, p.Op, index, p.Message, p.Untracked, p.OID)
	case "git.pr":
		return n.gitPR(ctx, p.Cwd)
	case "git.pr_create":
		return n.gitPRCreate(ctx, p.Cwd, p.Title, p.Body, p.Base, p.Draft)
	default: // git.pr_merge
		return n.gitPRMerge(ctx, p.Cwd, p.Number, p.Method, p.Auto, p.AutoOff)
	}
}

// gitWriteLocks: one write at a time per repository (by its root), so two clicks do not race each other's index.
var gitWriteLocks sync.Map // root → *sync.Mutex

func lockRepo(root string) func() {
	mu, _ := gitWriteLocks.LoadOrStore(root, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

// gitResult is what a write answers with: the repository as it is after it, and what the write made.
type gitResult struct {
	Status *GitStatus      `json:"status"`
	Commit *GitCommit      `json:"commit,omitempty"`
	Output string          `json:"output,omitempty"` // what git said (a push's summary)
	Stash  []GitStashEntry `json:"stashes,omitempty"`
}

type GitStashEntry struct {
	Index   int       `json:"index"`
	OID     string    `json:"oid"`
	Message string    `json:"message"`
	Date    time.Time `json:"date"`
}

type GitBranch struct {
	Name     string    `json:"name"`
	Remote   bool      `json:"remote,omitempty"`
	Current  bool      `json:"current,omitempty"`
	Upstream string    `json:"upstream,omitempty"`
	Hash     string    `json:"hash"`
	Subject  string    `json:"subject"`
	Date     time.Time `json:"date"`
}

// gitExec runs a git write in dir: its stdout and stderr apart (a hook writes to either), no prompt.
func gitExec(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := gitCmd(ctx, dir, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		err = fmt.Errorf("git %s timed out after %s", gitSubcommand(args), timeout)
	}
	return out.String(), errb.String(), err
}

// writeRepo is the repository root of cwd for a write, its write lock held until done is called.
func (n *Node) writeRepo(ctx context.Context, cwd string) (root string, done func(), err error) {
	dir, err := n.gitDir(cwd)
	if err != nil {
		return "", nil, err
	}
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil, &rpc.Error{Code: gitCodeNotRepo, Message: "not a Git repository: " + dir}
	}
	root = strings.TrimSpace(out)
	return root, lockRepo(root), nil
}

// gitRefusal is the rpc.Error for a write git refused: its code from what git said, its words as the message.
func gitRefusal(sub, stdout, stderr string, err error, data map[string]any) error {
	words := clipGitOutput(strings.TrimSpace(strings.TrimSpace(stderr) + "\n" + strings.TrimSpace(stdout)))
	if words == "" {
		words = err.Error()
	}
	code := gitCodeFailed
	low := strings.ToLower(stderr + "\n" + stdout)
	switch {
	case strings.Contains(low, "index.lock") || strings.Contains(low, "another git process"):
		code = gitCodeBusy
	case strings.Contains(low, "would be overwritten") || strings.Contains(low, "conflict"):
		code = gitCodeConflict
		if data == nil {
			data = map[string]any{}
		}
		if files := conflictFiles(stderr + "\n" + stdout); len(files) > 0 {
			data["files"] = files
		}
	case strings.Contains(low, "authentication failed") || strings.Contains(low, "permission denied") ||
		strings.Contains(low, "could not read username") || strings.Contains(low, "terminal prompts disabled"):
		code = gitCodeAuth
	case strings.Contains(low, "[rejected]") || strings.Contains(low, "non-fast-forward") || strings.Contains(low, "fetch first"):
		code = gitCodeRejected
	case strings.Contains(low, "tell me who you are") || strings.Contains(low, "unable to auto-detect email"):
		code = gitCodeIdentity
	case sub == "commit" || strings.Contains(low, "hook"):
		code = gitCodeHook
	}
	e := &rpc.Error{Code: code, Message: "git " + sub + ": " + words}
	if len(data) > 0 {
		e.Data, _ = json.Marshal(data)
	}
	return e
}

// clipGitOutput keeps what git or a hook said readable: no control characters but newlines and tabs, the last 4 KB.
func clipGitOutput(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= ' ' && r != 0x7f {
			return r
		}
		return -1
	}, ansiEscape.ReplaceAllString(s, ""))
	if len(s) > 4096 {
		s = "…" + s[len(s)-4096:]
	}
	return s
}

var ansiEscape = lazyre.New(`\x1b\[[0-9;?]*[A-Za-z]`)

// conflictFiles are the files git lists under "would be overwritten" or as CONFLICT lines.
func conflictFiles(out string) []string {
	var files []string
	listing := false
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "would be overwritten"):
			listing = true
		case listing && strings.HasPrefix(line, "\t"):
			files = append(files, strings.TrimSpace(line))
		case strings.HasPrefix(line, "CONFLICT"):
			if _, f, ok := strings.Cut(line, "Merge conflict in "); ok {
				files = append(files, strings.TrimSpace(f))
			}
		default:
			listing = false
		}
	}
	slices.Sort(files)
	return slices.Compact(files)
}

// settled is the result of a write: the repository as it is now.
func (n *Node) settled(ctx context.Context, root string, res *gitResult) (*gitResult, error) {
	st, err := n.gitStatus(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("done, but reading the status again failed: %w", err)
	}
	res.Status = st
	return res, nil
}

// gitStage stages paths, or takes them out of the index (unstage); the files themselves are not touched.
func (n *Node) gitStage(ctx context.Context, cwd string, paths []string, unstage bool) (*gitResult, error) {
	if len(paths) == 0 {
		return nil, &rpc.Error{Code: gitCodeFailed, Message: "no paths"}
	}
	root, done, err := n.writeRepo(ctx, cwd)
	if err != nil {
		return nil, err
	}
	defer done()
	for _, p := range paths {
		if err := n.guardRepoPath(root, p); err != nil {
			return nil, err
		}
	}
	args := append([]string{"add", "--"}, paths...)
	if unstage {
		args = append([]string{"restore", "--staged", "--"}, paths...)
		if _, err := git(ctx, root, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
			args = append([]string{"rm", "--cached", "-q", "--"}, paths...) // no commit yet: nothing to restore from
		}
	}
	if out, errOut, err := gitExec(ctx, root, 30*time.Second, args...); err != nil {
		return nil, gitRefusal(args[0], out, errOut, err, nil)
	}
	return n.settled(ctx, root, &gitResult{})
}

// gitCommit commits what is staged with message. Its hooks run (they may take a while: tests); one that refuses is
// said with its output.
func (n *Node) gitCommit(ctx context.Context, cwd, message string) (*gitResult, error) {
	if strings.TrimSpace(message) == "" {
		return nil, &rpc.Error{Code: gitCodeFailed, Message: "empty commit message"}
	}
	root, done, err := n.writeRepo(ctx, cwd)
	if err != nil {
		return nil, err
	}
	defer done()
	if _, err := git(ctx, root, "symbolic-ref", "-q", "HEAD"); err != nil {
		return nil, &rpc.Error{Code: gitCodeDetached, Message: "HEAD is detached: switch to a branch first"}
	}
	if _, _, err := gitExec(ctx, root, 30*time.Second, "diff", "--cached", "--quiet"); err == nil {
		return nil, &rpc.Error{Code: gitCodeNothingStaged, Message: "nothing is staged"}
	}
	out, errOut, err := gitExec(ctx, root, 4*time.Minute, "commit", "-q", "-m", message)
	if err != nil {
		return nil, gitRefusal("commit", out, errOut, err, nil)
	}
	res, err := n.settled(ctx, root, &gitResult{})
	if err == nil && res.Status.Head != nil {
		h := *res.Status.Head
		res.Commit = &h
	}
	return res, err
}

// gitPush pushes the current branch to its upstream branch by an explicit refspec with no "+" (a configured forcing
// refspec does not apply, as gitOp); setUpstream: a branch without one (a new branch) goes to origin under its own
// name and tracks it. Never forced.
func (n *Node) gitPush(ctx context.Context, cwd string, setUpstream bool) (*gitResult, error) {
	root, done, err := n.writeRepo(ctx, cwd)
	if err != nil {
		return nil, err
	}
	defer done()
	branch, err := git(ctx, root, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		return nil, &rpc.Error{Code: gitCodeDetached, Message: "HEAD is detached: switch to a branch first"}
	}
	branch = strings.TrimSpace(branch)
	remote, _ := git(ctx, root, "config", "--get", "branch."+branch+".remote")
	merge, _ := git(ctx, root, "config", "--get", "branch."+branch+".merge")
	remote, merge = strings.TrimSpace(remote), strings.TrimSpace(merge)
	var args []string
	switch {
	case remote != "" && remote != "." && !strings.HasPrefix(remote, "-") && strings.HasPrefix(merge, "refs/heads/"):
		args = []string{"push", "--porcelain", remote, "refs/heads/" + branch + ":" + merge}
	case !setUpstream:
		return nil, &rpc.Error{Code: gitCodeNoUpstream, Message: branch + " has no upstream"}
	default:
		remotes, _ := git(ctx, root, "remote")
		if !slices.Contains(strings.Fields(remotes), "origin") {
			return nil, &rpc.Error{Code: gitCodeNoRemote, Message: "no remote named origin"}
		}
		args = []string{"push", "--porcelain", "--set-upstream", "origin", "refs/heads/" + branch + ":refs/heads/" + branch}
	}
	out, errOut, err := gitExec(ctx, root, 2*time.Minute, args...)
	if err != nil {
		return nil, gitRefusal("push", out, errOut, err, nil)
	}
	return n.settled(ctx, root, &gitResult{Output: clipGitOutput(strings.TrimSpace(out))})
}

// gitBranches lists the local branches, then the remote ones without a local branch of the same name, newest first.
func (n *Node) gitBranches(ctx context.Context, cwd string) (map[string]any, error) {
	dir, err := n.gitDir(cwd)
	if err != nil {
		return nil, err
	}
	out, err := git(ctx, dir, "for-each-ref", "--sort=-committerdate", "--count=300",
		"--format=%(refname)%1f%(HEAD)%1f%(upstream:short)%1f%(objectname:short)%1f%(committerdate:iso-strict)%1f%(contents:subject)%1e",
		"refs/heads", "refs/remotes")
	if err != nil {
		return nil, &rpc.Error{Code: gitCodeNotRepo, Message: err.Error()}
	}
	var local, remote []GitBranch
	names := map[string]bool{}
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.Split(strings.TrimSpace(rec), "\x1f")
		if len(f) < 6 {
			continue
		}
		b := GitBranch{Current: f[1] == "*", Upstream: f[2], Hash: f[3], Subject: f[5]}
		b.Date, _ = time.Parse(time.RFC3339, f[4])
		switch ref := f[0]; {
		case strings.HasPrefix(ref, "refs/heads/"):
			b.Name = strings.TrimPrefix(ref, "refs/heads/")
			names[b.Name] = true
			local = append(local, b)
		case strings.HasPrefix(ref, "refs/remotes/") && !strings.HasSuffix(ref, "/HEAD"):
			b.Name, b.Remote = strings.TrimPrefix(ref, "refs/remotes/"), true
			remote = append(remote, b)
		}
	}
	remote = slices.DeleteFunc(remote, func(b GitBranch) bool {
		_, name, _ := strings.Cut(b.Name, "/")
		return names[name]
	})
	return map[string]any{"branches": append(local, remote...)}, nil
}

var branchName = lazyre.New(`^[A-Za-z0-9._/-]+$`)

// gitSwitch switches to branch (a remote one gets a local branch tracking it), or creates it from from (empty: HEAD).
// Uncommitted changes that the switch would overwrite are listed first and nothing is switched; the others go along,
// as git does.
func (n *Node) gitSwitch(ctx context.Context, cwd, branch string, create bool, from string) (*gitResult, error) {
	if !branchName.MatchString(branch) || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
		return nil, &rpc.Error{Code: gitCodeNoBranch, Message: "not a valid branch name: " + branch}
	}
	if from != "" && (!branchName.MatchString(from) || strings.HasPrefix(from, "-")) {
		return nil, &rpc.Error{Code: gitCodeNoBranch, Message: "not a valid branch: " + from}
	}
	root, done, err := n.writeRepo(ctx, cwd)
	if err != nil {
		return nil, err
	}
	defer done()
	target := branch
	if create {
		target = cmpOr(from, "HEAD")
	} else if _, err := git(ctx, root, "rev-parse", "--verify", "-q", "refs/heads/"+branch); err != nil {
		remote, err := git(ctx, root, "for-each-ref", "--format=%(refname:short)", "refs/remotes/*/"+branch)
		if err != nil || strings.TrimSpace(remote) == "" {
			return nil, &rpc.Error{Code: gitCodeNoBranch, Message: "no branch " + branch}
		}
		target = strings.Fields(remote)[0]
	}
	if _, err := git(ctx, root, "rev-parse", "--verify", "-q", target+"^{commit}"); err != nil {
		return nil, &rpc.Error{Code: gitCodeNoBranch, Message: "no branch " + target}
	}
	if files, err := switchConflicts(ctx, root, target); err != nil {
		return nil, err
	} else if len(files) > 0 {
		data, _ := json.Marshal(map[string]any{"files": files})
		return nil, &rpc.Error{Code: gitCodeConflict, Data: data,
			Message: "uncommitted changes to " + strconv.Itoa(len(files)) + " file(s) differ on " + branch + ": commit or stash them first"}
	}
	args := []string{"switch", "--no-guess", branch}
	switch {
	case create && from != "":
		args = []string{"switch", "-c", branch, "--no-track", from}
	case create:
		args = []string{"switch", "-c", branch}
	case target != branch:
		args = []string{"switch", "-c", branch, "--track", target}
	}
	if out, errOut, err := gitExec(ctx, root, time.Minute, args...); err != nil {
		if create && strings.Contains(errOut, "already exists") {
			return nil, &rpc.Error{Code: gitCodeNoBranch, Message: "a branch named " + branch + " already exists"}
		}
		return nil, gitRefusal("switch", out, errOut, err, nil)
	}
	return n.settled(ctx, root, &gitResult{})
}

// switchConflicts are the uncommitted files (staged, changed or untracked) that target has different from HEAD: a
// switch would have to overwrite them.
func switchConflicts(ctx context.Context, root, target string) ([]string, error) {
	dirty, err := git(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, gitRefusal("status", "", err.Error(), err, nil)
	}
	mine := map[string]bool{}
	recs := strings.Split(dirty, "\x00")
	for i := 0; i < len(recs); i++ {
		rec := recs[i]
		if len(rec) < 4 {
			continue
		}
		mine[rec[3:]] = true
		if rec[0] == 'R' || rec[0] == 'C' {
			i++ // the rename's source follows
			if i < len(recs) {
				mine[recs[i]] = true
			}
		}
	}
	if len(mine) == 0 {
		return nil, nil
	}
	head := "HEAD"
	if _, err := git(ctx, root, "rev-parse", "--verify", "-q", "HEAD"); err != nil {
		head = "4b825dc642cb6eb9a060e54bf8d69288fbee4904" // the empty tree: no commit yet
	}
	changed, err := git(ctx, root, "diff", "--name-only", "-z", "--no-renames", head, target, "--")
	if err != nil {
		return nil, gitRefusal("diff", "", err.Error(), err, nil)
	}
	var files []string
	for _, f := range strings.Split(changed, "\x00") {
		if f != "" && mine[f] {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return files, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// gitStash: push (the uncommitted changes, untracked too when asked, with a message), list, apply, pop or drop entry
// index and full object ID. An apply or pop that conflicts keeps the entry.
func (n *Node) gitStash(ctx context.Context, cwd, op string, index int, message string, untracked bool, oid string) (*gitResult, error) {
	root, done, err := n.writeRepo(ctx, cwd)
	if err != nil {
		return nil, err
	}
	defer done()
	var args []string
	switch op {
	case "list":
		list, err := stashList(ctx, root)
		if err != nil {
			return nil, err
		}
		return &gitResult{Stash: list}, nil
	case "push":
		args = []string{"stash", "push"}
		if untracked {
			args = append(args, "--include-untracked")
		}
		if m := strings.TrimSpace(message); m != "" {
			args = append(args, "-m", m)
		}
	case "apply", "pop", "drop":
		if !stashOIDPattern.MatchString(oid) || index < 0 {
			return nil, staleStash()
		}
		if op == "apply" {
			entries, err := stashList(ctx, root)
			if err != nil {
				return nil, err
			}
			if index >= len(entries) || entries[index].OID != oid {
				return nil, staleStash()
			}
			// A terminal writer may now move the stack; only the selected content is applied.
			args = []string{"stash", "apply", oid}
		} else {
			if err := mutateStash(ctx, root, op, index, oid); err != nil {
				return nil, err
			}
			res, err := n.settled(ctx, root, &gitResult{})
			if err == nil {
				res.Stash, err = stashList(ctx, root)
			}
			return res, err
		}
	default:
		return nil, &rpc.Error{Code: gitCodeFailed, Message: "unknown stash op " + op}
	}
	out, errOut, err := gitExec(ctx, root, time.Minute, args...)
	if err != nil && op == "apply" {
		return nil, stashApplyRefusal(ctx, root, out, errOut, err)
	} else if err != nil {
		return nil, gitRefusal("stash", out, errOut, err, nil)
	}
	if op == "push" && strings.Contains(out+errOut, "No local changes to save") {
		return nil, &rpc.Error{Code: gitCodeNoStash, Message: "no local changes to save"}
	}
	res, err := n.settled(ctx, root, &gitResult{})
	if err == nil {
		res.Stash, err = stashList(ctx, root)
	}
	return res, err
}

func stashList(ctx context.Context, root string) ([]GitStashEntry, error) {
	out, err := git(ctx, root, "stash", "list", "--format=%gd%x1f%H%x1f%gs%x1f%cI")
	if err != nil {
		return nil, gitRefusal("stash", "", err.Error(), err, nil)
	}
	list := []GitStashEntry{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) < 4 {
			continue
		}
		i, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(f[0], "stash@{"), "}"))
		if err != nil {
			continue
		}
		e := GitStashEntry{Index: i, OID: f[1], Message: f[2]}
		e.Date, _ = time.Parse(time.RFC3339, f[3])
		list = append(list, e)
	}
	return list, nil
}
