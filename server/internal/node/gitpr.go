package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The current branch's pull request for the Git panel (#6121), through `gh` on this machine as it is signed in: what
// it is, its checks and its open review threads; creating one, merging it or setting it to merge when ready. gh not
// installed or not signed in is said as such (proto.CodeNoGh, proto.CodeNotSignedIn), never its raw error.

const (
	gitCodeNotPushed = "git_not_pushed" // a pull request for a branch whose commits are not all on its upstream yet
	gitCodeNoPR      = "git_no_pr"      // no pull request for the branch
	gitCodeNotGitHub = "git_not_github" // no GitHub remote to make one on
)

// ghLookPath is exec.LookPath("gh"); tests point it at a script.
var ghLookPath = func() (string, error) { return exec.LookPath("gh") }

// ghRun runs gh in dir without prompting; its output, or an rpc.Error saying what kept it from answering.
func ghRun(ctx context.Context, dir string, timeout time.Duration, args ...string) ([]byte, error) {
	gh, err := ghLookPath()
	if err != nil {
		return nil, &rpc.Error{Code: proto.CodeNoGh, Message: "gh is not installed on this machine"}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gh, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1", "GH_SPINNER_DISABLED=1",
		"GIT_TERMINAL_PROMPT=0")
	cmd.WaitDelay = time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, &rpc.Error{Code: gitCodeFailed, Message: fmt.Sprintf("gh %s timed out after %s", args[0], timeout)}
		}
		words := clipGitOutput(strings.TrimSpace(errb.String()))
		low := strings.ToLower(words)
		switch {
		case strings.Contains(low, "gh auth login") || strings.Contains(low, "not logged in") || strings.Contains(low, "authentication"):
			return nil, &rpc.Error{Code: proto.CodeNotSignedIn, Message: "gh is not signed in on this machine: " + firstLine(words)}
		case strings.Contains(low, "no pull requests found"):
			return nil, &rpc.Error{Code: gitCodeNoPR, Message: firstLine(words)}
		case strings.Contains(low, "none of the git remotes") || strings.Contains(low, "not a github") ||
			strings.Contains(low, "no git remotes"):
			return nil, &rpc.Error{Code: gitCodeNotGitHub, Message: firstLine(words)}
		}
		if words == "" {
			words = err.Error()
		}
		return nil, &rpc.Error{Code: gitCodeFailed, Message: "gh " + args[0] + ": " + words}
	}
	return out.Bytes(), nil
}

// GitPR is the branch's pull request as the panel shows it.
type GitPR struct {
	Number         int           `json:"number"`
	URL            string        `json:"url"`
	Title          string        `json:"title"`
	State          string        `json:"state"` // OPEN, CLOSED, MERGED
	Draft          bool          `json:"draft,omitempty"`
	Base           string        `json:"base"`
	Head           string        `json:"head"`
	HeadOID        string        `json:"head_oid,omitempty"`
	Mergeable      string        `json:"mergeable,omitempty"`       // MERGEABLE, CONFLICTING, UNKNOWN
	MergeState     string        `json:"merge_state,omitempty"`     // gh's mergeStateStatus: CLEAN, BLOCKED, BEHIND, DIRTY…
	Review         string        `json:"review,omitempty"`          // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED
	AutoMerge      bool          `json:"auto_merge,omitempty"`      // set to merge when ready
	Checks         []GitCheck    `json:"checks"`                    // the head commit's checks
	ChecksState    string        `json:"checks_state,omitempty"`    // passing, failing, pending ("" when there are none)
	ChecksComplete bool          `json:"checks_complete,omitempty"` // every rollup context was returned and its status was checked
	Threads        []GitPRThread `json:"threads"`                   // the open (unresolved) review threads
}

type GitCheck struct {
	Name  string `json:"name"`
	State string `json:"state"` // pass, fail, pending, skipped
	URL   string `json:"url,omitempty"`
}

// GitPRThread is an unresolved review thread: where, and what was said.
type GitPRThread struct {
	Path     string         `json:"path"`
	Line     int            `json:"line,omitempty"`
	Outdated bool           `json:"outdated,omitempty"`
	Comments []GitPRComment `json:"comments"`
}

type GitPRComment struct {
	Author string    `json:"author"`
	Body   string    `json:"body"`
	URL    string    `json:"url,omitempty"`
	At     time.Time `json:"at"`
}

// gitPR is the current branch's pull request (nil, with can_create, when there is none).
func (n *Node) gitPR(ctx context.Context, cwd string) (map[string]any, error) {
	dir, err := n.gitDir(cwd)
	if err != nil {
		return nil, err
	}
	branch, err := git(ctx, dir, "symbolic-ref", "-q", "--short", "HEAD")
	if err != nil {
		return nil, &rpc.Error{Code: gitCodeDetached, Message: "HEAD is detached: no branch"}
	}
	branch = strings.TrimSpace(branch)
	out, err := ghRun(ctx, dir, 30*time.Second, "pr", "view", branch, "--json",
		"number,url,title,state,isDraft,baseRefName,headRefName,headRefOid,mergeable,mergeStateStatus,reviewDecision,autoMergeRequest,statusCheckRollup")
	if e, ok := err.(*rpc.Error); ok && e.Code == gitCodeNoPR {
		return map[string]any{"pr": nil, "branch": branch}, nil
	}
	if err != nil {
		return nil, err
	}
	var v struct {
		Number           int             `json:"number"`
		URL              string          `json:"url"`
		Title            string          `json:"title"`
		State            string          `json:"state"`
		IsDraft          bool            `json:"isDraft"`
		BaseRefName      string          `json:"baseRefName"`
		HeadRefName      string          `json:"headRefName"`
		HeadRefOID       string          `json:"headRefOid"`
		Mergeable        string          `json:"mergeable"`
		MergeStateStatus string          `json:"mergeStateStatus"`
		ReviewDecision   string          `json:"reviewDecision"`
		AutoMergeRequest json.RawMessage `json:"autoMergeRequest"`
		Checks           []struct {
			Name       string `json:"name"`
			Context    string `json:"context"` // a commit status's name
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"` // a commit status's state
			DetailsURL string `json:"detailsUrl"`
			TargetURL  string `json:"targetUrl"`
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, &rpc.Error{Code: gitCodeFailed, Message: "gh pr view: " + err.Error()}
	}
	pr := GitPR{Number: v.Number, URL: v.URL, Title: v.Title, State: v.State, Draft: v.IsDraft, Base: v.BaseRefName,
		Head: v.HeadRefName, HeadOID: v.HeadRefOID, Mergeable: v.Mergeable, MergeState: v.MergeStateStatus, Review: v.ReviewDecision,
		AutoMerge: len(v.AutoMergeRequest) > 0 && string(v.AutoMergeRequest) != "null", Checks: []GitCheck{}, Threads: []GitPRThread{}}
	for _, c := range v.Checks {
		check := GitCheck{Name: cmpOr(c.Name, c.Context), URL: cmpOr(c.DetailsURL, c.TargetURL)}
		switch {
		case c.Status != "" && !strings.EqualFold(c.Status, "COMPLETED"):
			check.State = "pending"
		default:
			switch strings.ToUpper(cmpOr(c.Conclusion, c.State)) {
			case "SUCCESS":
				check.State = "pass"
			case "FAILURE", "ERROR", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
				check.State = "fail"
			case "SKIPPED", "NEUTRAL":
				check.State = "skipped"
			default:
				check.State = "pending"
			}
		}
		pr.Checks = append(pr.Checks, check)
	}
	// gh's rollup has a 100-context page; at that boundary it cannot prove
	// that there are no more checks. Older nodes omit this capability marker.
	pr.ChecksComplete = v.HeadRefOID != "" && len(v.Checks) < 100
	for _, check := range v.Checks {
		if check.Name != "" && check.Status == "" || check.Context != "" && check.State == "" || check.Name == "" && check.Context == "" {
			pr.ChecksComplete = false
		}
	}
	pr.ChecksState = checksState(pr.Checks)
	if threads, err := prThreads(ctx, dir, pr.URL); err == nil {
		pr.Threads = threads
	}
	return map[string]any{"pr": pr, "branch": branch}, nil
}

// checksState is the checks in one word: failing over pending over passing.
func checksState(checks []GitCheck) string {
	state := ""
	for _, c := range checks {
		switch {
		case c.State == "fail":
			return "failing"
		case c.State == "pending":
			state = "pending"
		case c.State == "pass" && state == "":
			state = "passing"
		}
	}
	return state
}

const threadsQuery = `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) { reviewThreads(first: 50) { nodes {
    isResolved isOutdated path line originalLine
    comments(first: 20) { nodes { author { login } body url createdAt } }
  } } } }
}`

// prThreads are the pull request's unresolved review threads (prURL: https://<host>/<owner>/<repo>/pull/<n>).
func prThreads(ctx context.Context, dir, prURL string) ([]GitPRThread, error) {
	u, err := url.Parse(prURL)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 4 || parts[2] != "pull" {
		return nil, fmt.Errorf("not a pull request URL: %s", prURL)
	}
	args := []string{"api", "graphql", "-f", "query=" + threadsQuery, "-F", "owner=" + parts[0], "-F", "name=" + parts[1],
		"-F", "number=" + parts[3]}
	if u.Host != "github.com" {
		args = append(args, "--hostname", u.Host)
	}
	out, err := ghRun(ctx, dir, 30*time.Second, args...)
	if err != nil {
		return nil, err
	}
	var v struct {
		Data struct {
			Repository struct {
				PullRequest struct {
					ReviewThreads struct {
						Nodes []struct {
							IsResolved   bool   `json:"isResolved"`
							IsOutdated   bool   `json:"isOutdated"`
							Path         string `json:"path"`
							Line         int    `json:"line"`
							OriginalLine int    `json:"originalLine"`
							Comments     struct {
								Nodes []struct {
									Author    struct{ Login string } `json:"author"`
									Body      string                 `json:"body"`
									URL       string                 `json:"url"`
									CreatedAt time.Time              `json:"createdAt"`
								} `json:"nodes"`
							} `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	threads := []GitPRThread{}
	for _, t := range v.Data.Repository.PullRequest.ReviewThreads.Nodes {
		if t.IsResolved {
			continue
		}
		th := GitPRThread{Path: t.Path, Line: t.Line, Outdated: t.IsOutdated, Comments: []GitPRComment{}}
		if th.Line == 0 {
			th.Line = t.OriginalLine
		}
		for _, c := range t.Comments.Nodes {
			th.Comments = append(th.Comments, GitPRComment{Author: c.Author.Login, Body: c.Body, URL: c.URL, At: c.CreatedAt})
		}
		threads = append(threads, th)
	}
	return threads, nil
}

// gitPRCreate opens a pull request for the current branch, which must be pushed with nothing left to push.
func (n *Node) gitPRCreate(ctx context.Context, cwd, title, body, base string, draft bool) (map[string]any, error) {
	if strings.TrimSpace(title) == "" {
		return nil, &rpc.Error{Code: gitCodeFailed, Message: "empty title"}
	}
	if base != "" && (!branchName.MatchString(base) || strings.HasPrefix(base, "-")) {
		return nil, &rpc.Error{Code: gitCodeNoBranch, Message: "not a valid base branch: " + base}
	}
	root, done, err := n.writeRepo(ctx, cwd)
	if err != nil {
		return nil, err
	}
	defer done()
	st, err := n.gitStatus(ctx, root)
	if err != nil {
		return nil, err
	}
	switch {
	case st.Branch == "":
		return nil, &rpc.Error{Code: gitCodeDetached, Message: "HEAD is detached: no branch"}
	case st.Upstream == "" || st.Ahead > 0:
		return nil, &rpc.Error{Code: gitCodeNotPushed, Message: st.Branch + " has commits that are not pushed"}
	}
	args := []string{"pr", "create", "--head", st.Branch, "--title", title, "--body", body}
	if base != "" {
		args = append(args, "--base", base)
	}
	if draft {
		args = append(args, "--draft")
	}
	if _, err := ghRun(ctx, root, time.Minute, args...); err != nil {
		return nil, err
	}
	return n.gitPR(ctx, root)
}

// gitPRMerge merges the branch's pull request now (method squash / merge / rebase), sets it to merge when its checks
// and reviews allow (auto), or takes that back (autoOff). The branch is not deleted: gh would also switch this
// checkout to the base, under the agent's feet.
func (n *Node) gitPRMerge(ctx context.Context, cwd string, number int, method string, auto, autoOff bool) (map[string]any, error) {
	if number <= 0 {
		return nil, &rpc.Error{Code: gitCodeNoPR, Message: "no pull request number"}
	}
	root, done, err := n.writeRepo(ctx, cwd)
	if err != nil {
		return nil, err
	}
	defer done()
	args := []string{"pr", "merge", strconv.Itoa(number)}
	switch {
	case autoOff:
		args = append(args, "--disable-auto")
	default:
		switch method {
		case "squash", "merge", "rebase":
			args = append(args, "--"+method)
		default:
			return nil, &rpc.Error{Code: gitCodeFailed, Message: "unknown merge method " + method}
		}
		if auto {
			args = append(args, "--auto")
		}
	}
	if _, err := ghRun(ctx, root, 2*time.Minute, args...); err != nil {
		return nil, err
	}
	return n.gitPR(ctx, root)
}
