package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant's read tools (prompts/mcp/github): they only read, so they run without asking.

var readTools = []string{"github_search", "github_read", "github_diff", "github_checks"}

func (c *Connector) Tools() []proto.Tool {
	out := make([]proto.Tool, 0, len(readTools))
	for _, n := range readTools {
		t := prompts.Tool(n)
		out = append(out, proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

// Limits of what a tool returns, so one answer does not fill the model's context.
const (
	bodyCap    = 4000  // a description
	commentCap = 1500  // one comment or review
	patchCap   = 20000 // one file's patch
	logTail    = 6000  // the end of a failed job's log
	failedLogs = 3     // jobs whose log is read
)

var repoName = lazyre.New(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// validRepo is "owner/name" and nothing that changes the API path ("..").
func validRepo(r string) bool {
	owner, name, _ := strings.Cut(r, "/")
	return repoName.MatchString(r) && owner != "." && owner != ".." && name != "." && name != ".."
}

// ValidRepo reports whether r is a safe GitHub owner/name pair.
func ValidRepo(r string) bool { return validRepo(r) }

func (c *Connector) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	var a struct {
		Query    string `json:"query"`
		Kind     string `json:"kind"`
		Limit    int    `json:"limit"`
		Repo     string `json:"repo"`
		Number   int    `json:"number"`
		Comments *int   `json:"comments"`
		Path     string `json:"path"`
		Ref      string `json:"ref"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if name != "github_search" && !validRepo(a.Repo) {
		return "", fmt.Errorf("repo must be owner/name, not %q", a.Repo)
	}
	switch name {
	case "github_search":
		return c.search(ctx, a.Query, a.Kind, a.Limit)
	case "github_read":
		n := 15
		if a.Comments != nil {
			n = min(max(*a.Comments, 0), 50)
		}
		return c.read(ctx, a.Repo, a.Number, n)
	case "github_diff":
		return c.diff(ctx, a.Repo, a.Number, a.Path)
	case "github_checks":
		return c.checks(ctx, a.Repo, a.Number, a.Ref)
	}
	return "", fmt.Errorf("unknown tool %s", name)
}

type user struct {
	Login string `json:"login"`
	Type  string `json:"type"` // User | Bot
}

func (u user) String() string {
	if u.Login == "" {
		return "(deleted user)"
	}
	return u.Login
}

type issue struct {
	Number      int                     `json:"number"`
	Title       string                  `json:"title"`
	State       string                  `json:"state"`
	User        user                    `json:"user"`
	Body        string                  `json:"body"`
	HTMLURL     string                  `json:"html_url"`
	RepoURL     string                  `json:"repository_url"`
	CreatedAt   time.Time               `json:"created_at"`
	UpdatedAt   time.Time               `json:"updated_at"`
	Comments    int                     `json:"comments"`
	Draft       bool                    `json:"draft"`
	Labels      []struct{ Name string } `json:"labels"`
	Assignees   []user                  `json:"assignees"`
	PullRequest *struct {
		MergedAt *time.Time `json:"merged_at"`
	} `json:"pull_request"`
}

// repo is "owner/name" of a search result (its repository_url ends with it).
func (it issue) repo() string {
	parts := strings.Split(it.RepoURL, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

func (it issue) state() string {
	switch {
	case it.PullRequest != nil && it.PullRequest.MergedAt != nil:
		return "merged"
	case it.Draft && it.State == "open":
		return "draft"
	}
	return it.State
}

func (c *Connector) search(ctx context.Context, query, kind string, limit int) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", fmt.Errorf("query is required")
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	q := url.Values{"q": {query}, "per_page": {fmt.Sprint(limit)}}
	var b strings.Builder
	if kind == "code" {
		var res struct {
			Total int `json:"total_count"`
			Items []struct {
				Path       string `json:"path"`
				HTMLURL    string `json:"html_url"`
				Repository struct {
					FullName string `json:"full_name"`
				} `json:"repository"`
			} `json:"items"`
		}
		if _, err := c.Get(ctx, "/search/code?"+q.Encode(), &res); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "%d results", res.Total)
		for _, it := range res.Items {
			fmt.Fprintf(&b, "\n%s %s %s", it.Repository.FullName, it.Path, it.HTMLURL)
		}
		return b.String(), nil
	}
	var res struct {
		Total int     `json:"total_count"`
		Items []issue `json:"items"`
	}
	if _, err := c.Get(ctx, "/search/issues?"+q.Encode()+"&sort=updated", &res); err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "%d results", res.Total)
	if res.Total > len(res.Items) {
		fmt.Fprintf(&b, " (the %d most recently updated)", len(res.Items))
	}
	for _, it := range res.Items {
		what := "issue"
		if it.PullRequest != nil {
			what = "PR"
		}
		fmt.Fprintf(&b, "\n%s#%d [%s %s] %s — %s, updated %s, %d comments %s", it.repo(), it.Number, what, it.state(),
			it.Title, it.User, day(it.UpdatedAt), it.Comments, it.HTMLURL)
	}
	return b.String(), nil
}

type comment struct {
	User      user      `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	// reviews
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
}

func (c *Connector) read(ctx context.Context, repo string, number, comments int) (string, error) {
	var it issue
	base := fmt.Sprintf("/repos/%s/issues/%d", repo, number)
	if _, err := c.Get(ctx, base, &it); err != nil {
		return "", err
	}
	var b strings.Builder
	what := "Issue"
	if it.PullRequest != nil {
		what = "Pull request"
	}
	fmt.Fprintf(&b, "%s %s#%d: %s\nState: %s · by %s · opened %s · updated %s\n%s\n", what, repo, number, it.Title, it.state(),
		it.User, day(it.CreatedAt), day(it.UpdatedAt), it.HTMLURL)
	if len(it.Labels) > 0 {
		names := make([]string, len(it.Labels))
		for i, l := range it.Labels {
			names[i] = l.Name
		}
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(names, ", "))
	}
	if len(it.Assignees) > 0 {
		fmt.Fprintf(&b, "Assignees: %s\n", logins(it.Assignees))
	}

	var reviews []comment
	if it.PullRequest != nil {
		var pr struct {
			Head struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
			Mergeable      *bool  `json:"mergeable"`
			MergeableState string `json:"mergeable_state"`
			Additions      int    `json:"additions"`
			Deletions      int    `json:"deletions"`
			ChangedFiles   int    `json:"changed_files"`
			Reviewers      []user `json:"requested_reviewers"`
			Teams          []struct {
				Slug string `json:"slug"`
			} `json:"requested_teams"`
		}
		if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), &pr); err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "Branch: %s → %s (head %s) · %d files, +%d −%d", pr.Head.Ref, pr.Base.Ref, short(pr.Head.SHA),
			pr.ChangedFiles, pr.Additions, pr.Deletions)
		if pr.MergeableState != "" {
			fmt.Fprintf(&b, " · mergeable: %s", pr.MergeableState)
		}
		b.WriteString("\n")
		requested := logins(pr.Reviewers)
		for _, t := range pr.Teams {
			requested = strings.TrimPrefix(requested+", team "+t.Slug, ", ")
		}
		if requested != "" {
			fmt.Fprintf(&b, "Review requested from: %s\n", requested)
		}
		if comments > 0 {
			if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, number), &reviews); err != nil {
				return "", err
			}
		}
	}
	fmt.Fprintf(&b, "\n%s\n", cut(strings.TrimSpace(it.Body), bodyCap, "(no description)"))
	if comments == 0 {
		return b.String(), nil
	}

	// The newest comments: ask for the last page's worth.
	var cs []comment
	page := max(1, (it.Comments+99)/100)
	if _, err := c.Get(ctx, fmt.Sprintf("%s/comments?per_page=100&page=%d", base, page), &cs); err != nil {
		return "", err
	}
	type entry struct {
		at   time.Time
		text string
	}
	var all []entry
	for _, cm := range cs {
		all = append(all, entry{cm.CreatedAt, fmt.Sprintf("%s %s:\n%s", day(cm.CreatedAt), who(cm.User), cut(strings.TrimSpace(cm.Body), commentCap, ""))})
	}
	for _, r := range reviews {
		verb := map[string]string{"APPROVED": "approved", "CHANGES_REQUESTED": "requested changes", "COMMENTED": "reviewed",
			"DISMISSED": "review dismissed"}[r.State]
		if verb == "" || (r.State == "COMMENTED" && strings.TrimSpace(r.Body) == "") {
			continue
		}
		text := fmt.Sprintf("%s %s %s", day(r.SubmittedAt), who(r.User), verb)
		if body := strings.TrimSpace(r.Body); body != "" {
			text += ":\n" + cut(body, commentCap, "")
		}
		all = append(all, entry{r.SubmittedAt, text})
	}
	// newest first
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].at.After(all[j-1].at); j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	total := it.Comments + len(all) - len(cs) // the comments on GitHub, and the reviews listed
	if len(all) > comments {
		all = all[:comments]
	}
	fmt.Fprintf(&b, "\n--- %d comments and reviews, newest first", total)
	if total > len(all) {
		fmt.Fprintf(&b, " (the newest %d)", len(all))
	}
	for _, e := range all {
		fmt.Fprintf(&b, "\n\n%s", e.text)
	}
	return b.String(), nil
}

func (c *Connector) diff(ctx context.Context, repo string, number int, path string) (string, error) {
	type file struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
		Patch     string `json:"patch"`
		Previous  string `json:"previous_filename"`
	}
	var files []file
	for page := 1; page <= 10; page++ { // GitHub lists at most 3000 files
		var batch []file
		if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100&page=%d", repo, number, page), &batch); err != nil {
			return "", err
		}
		files = append(files, batch...)
		if len(batch) < 100 || path != "" && containsFile(batch, path, func(f file) string { return f.Filename }) {
			break
		}
	}
	if path != "" {
		for _, f := range files {
			if f.Filename == path {
				head := fmt.Sprintf("%s (%s, +%d −%d)\n", f.Filename, f.Status, f.Additions, f.Deletions)
				if f.Patch == "" {
					return head + "(no patch: binary, or too big for GitHub to show)", nil
				}
				return head + cut(f.Patch, patchCap, ""), nil
			}
		}
		return "", fmt.Errorf("%s is not among the files %s#%d changes", path, repo, number)
	}
	var b strings.Builder
	adds, dels := 0, 0
	for _, f := range files {
		adds, dels = adds+f.Additions, dels+f.Deletions
	}
	fmt.Fprintf(&b, "%d files changed, +%d −%d", len(files), adds, dels)
	for _, f := range files {
		name := f.Filename
		if f.Previous != "" {
			name = f.Previous + " → " + f.Filename
		}
		fmt.Fprintf(&b, "\n%s %s +%d −%d", f.Status, name, f.Additions, f.Deletions)
	}
	return b.String(), nil
}

func containsFile[T any](list []T, path string, name func(T) string) bool {
	for _, f := range list {
		if name(f) == path {
			return true
		}
	}
	return false
}

func (c *Connector) checks(ctx context.Context, repo string, number int, ref string) (string, error) {
	if ref == "" {
		if number == 0 {
			return "", fmt.Errorf("number or ref is required")
		}
		var pr struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}
		if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), &pr); err != nil {
			return "", err
		}
		ref = pr.Head.SHA
	}
	var res struct {
		Total int `json:"total_count"`
		Runs  []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
			App        struct {
				Slug string `json:"slug"`
			} `json:"app"`
			Output struct {
				Title   string `json:"title"`
				Summary string `json:"summary"`
			} `json:"output"`
		} `json:"check_runs"`
	}
	if _, err := c.Get(ctx, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repo, url.PathEscape(ref)), &res); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d checks on %s", res.Total, short(ref))
	failed := 0
	for _, r := range res.Runs {
		state := r.Status
		if r.Status == "completed" {
			state = r.Conclusion
		}
		fmt.Fprintf(&b, "\n%s: %s %s", r.Name, state, r.HTMLURL)
		if r.Conclusion != "failure" && r.Conclusion != "timed_out" {
			continue
		}
		if t := strings.TrimSpace(r.Output.Title); t != "" {
			fmt.Fprintf(&b, "\n  %s", t)
		}
		if failed >= failedLogs || r.App.Slug != "github-actions" {
			continue
		}
		failed++
		// An Actions check run's id is its job's id; the log comes as a redirect to a short-lived URL (no token
		// goes there: Go drops Authorization on a redirect to another host).
		log, err := c.raw(ctx, fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, r.ID))
		if err != nil {
			fmt.Fprintf(&b, "\n  (log not readable: %v)", err)
			continue
		}
		fmt.Fprintf(&b, "\n  --- the end of the log:\n%s", tail(log, logTail))
	}
	return b.String(), nil
}

func logins(us []user) string {
	names := make([]string, len(us))
	for i, u := range us {
		names[i] = u.String()
	}
	return strings.Join(names, ", ")
}

// who is a commenter, with bots marked.
func who(u user) string {
	if u.Type == "Bot" || strings.HasSuffix(u.Login, "[bot]") {
		return u.String() + " (bot)"
	}
	return u.String()
}

func day(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.UTC().Format("2006-01-02 15:04Z")
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// cut keeps the first n bytes of s (at a rune boundary), saying how much is left out; empty s is none.
func cut(s string, n int, none string) string {
	if s == "" {
		return none
	}
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + fmt.Sprintf("\n[… %d more characters cut]", utf8.RuneCountInString(s[i:]))
}

// tail keeps the last n bytes of s, from a line start.
func tail(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return "[…]\n" + s
}
