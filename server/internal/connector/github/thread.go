package github

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// A pull request or issue opened in the app as GitHub's own thread (connector.Threads, docs/extensions/connectors.md "Chat page"):
// its header (state, author, labels, branches, description) and its timeline, a page at a time, newest page first:
// comments, reviews, comments on lines with the diff before them, CI checks (a failure with the end of its log) and
// the gray lines of what happened (labeled, merged, renamed). Every list is read with If-None-Match: GitHub answers
// 304 for what has not changed, which costs nothing of the hour's requests.

const (
	threadListPages = 5                // retain the newest 500 comments, reviews or events per list
	threadFresh     = 15 * time.Second // a list read this recently is not asked again (the next page of the same thread)
	etagKeep        = 400              // answers kept for If-None-Match
	hunkLines       = 6                // lines of the diff shown above a comment on a line
	checkLogTail    = 8 << 10          // the end of a failed check's log
)

// etagCache keeps GitHub's answers by path with their ETag, the least recently used going first.
type etagCache struct {
	mu    sync.Mutex
	m     map[string]*etagEntry
	reads uint64           // request admission order, shared by discovery and tail pages
	logs  map[int64]string // failed checks' log tails, by job id (a finished job's log does not change)
	// avatars: a login's avatar URL, as the lists gave it, for Media.
	avatars map[string]string
}

type etagEntry struct {
	etag string
	link string // pagination belongs to the representation, including on fresh and 304 reads
	body []byte
	at   time.Time // response completion, for threadFresh
	used time.Time
	read uint64 // admission order of the request that validated this representation
}

// cachedGet reads path into out like Get, asking with If-None-Match for what it read before and not asking at all
// within threadFresh.
func (c *Connector) cachedGet(ctx context.Context, path string, out any) error {
	_, err := c.cachedPage(ctx, path, out, false)
	return err
}

// discover requires current pagination metadata on revalidation. An unchanged first-page
// body can have a new last page; a 304 without Link cannot confirm the old page count.
func (c *Connector) cachedPage(ctx context.Context, path string, out any, discover bool) (string, error) {
	link, _, err := c.cachedPageSince(ctx, path, out, discover, 0)
	return link, err
}

// Discovery establishes the earliest acceptable request admission for its tail
// pages. Completion times cannot order reads: an older partial tail can finish late.
func (c *Connector) cachedPageSince(ctx context.Context, path string, out any, discover bool, freshSince uint64) (string, uint64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	e := &c.etags
	e.mu.Lock()
	if e.m == nil {
		e.m = map[string]*etagEntry{}
	}
	old := e.m[path]
	var hdr http.Header
	if old != nil {
		old.used = time.Now()
		if time.Since(old.at) < threadFresh && old.read >= freshSince {
			body, link, read := old.body, old.link, old.read
			e.mu.Unlock()
			return link, read, decodeBody(path, body, out)
		}
		hdr = http.Header{"If-None-Match": {old.etag}}
	}
	e.reads++
	read := e.reads
	e.mu.Unlock()
	var body []byte
	code, h, err := c.request(ctx, http.MethodGet, path, hdr, nil, &body)
	if err != nil {
		return "", 0, err
	}
	if discover && code == http.StatusNotModified && h.Values("Link") == nil {
		// One unconditional discovery read is bounded independently of thread size.
		e.mu.Lock()
		e.reads++
		read = e.reads
		e.mu.Unlock()
		code, h, err = c.request(ctx, http.MethodGet, path, nil, nil, &body)
		if err != nil {
			return "", 0, err
		}
	}
	link := strings.Join(h.Values("Link"), ", ")
	tag := h.Get("ETag")
	if code == http.StatusNotModified && old != nil {
		// Published representations are immutable; a delayed 304 belongs to the body
		// and metadata captured when its conditional request was admitted.
		body = old.body
		if h.Values("Link") == nil {
			link = old.link
		}
		if tag == "" {
			tag = old.etag
		}
	}
	at := time.Now()
	e.mu.Lock()
	// Never let an older in-flight response overwrite a newer representation.
	if current := e.m[path]; current == nil || current.read <= read {
		if tag != "" {
			e.m[path] = &etagEntry{etag: tag, link: link, body: body, at: at, used: at, read: read}
			if len(e.m) > etagKeep {
				oldest := ""
				for k, v := range e.m {
					if oldest == "" || v.used.Before(e.m[oldest].used) {
						oldest = k
					}
				}
				delete(e.m, oldest)
			}
		} else {
			// A new response without an ETag must not leave an older tagged body fresh.
			delete(e.m, path)
		}
	}
	e.mu.Unlock()
	return link, read, decodeBody(path, body, out)
}

// forget drops what is kept for repo's PR / issue number: a comment was just posted, the page reads it again.
func (c *Connector) forget(repo string, number int) {
	e := &c.etags
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range []string{fmt.Sprintf("/repos/%s/issues/%d", repo, number), fmt.Sprintf("/repos/%s/pulls/%d", repo, number)} {
		for k := range e.m {
			if k == p || strings.HasPrefix(k, p+"/") || strings.HasPrefix(k, p+"?") {
				delete(e.m, k)
			}
		}
	}
}

func decodeBody(path string, body []byte, out any) error {
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("GitHub GET %s: %v", path, err)
	}
	return nil
}

// listLastPage reads only page numbers from GitHub's Link header. Link targets may use
// canonical repository paths; requests always keep the caller's original endpoint.
func listLastPage(link string) (int, error) {
	last, next := 1, false
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		end := strings.IndexByte(part, '>')
		if !strings.HasPrefix(part, "<") || end < 1 {
			return 0, errors.New("GitHub list has malformed pagination")
		}
		_, params, err := mime.ParseMediaType("link" + part[end+1:])
		if err != nil {
			return 0, errors.New("GitHub list has malformed pagination")
		}
		rels := strings.Fields(params["rel"])
		if slices.Contains(rels, "next") {
			next = true
		}
		if !slices.Contains(rels, "last") {
			continue
		}
		u, err := url.Parse(part[1:end])
		if err != nil {
			return 0, errors.New("GitHub list has malformed last page")
		}
		pages := u.Query()["page"]
		if len(pages) != 1 {
			return 0, errors.New("GitHub list has malformed last page")
		}
		n, err := strconv.Atoi(pages[0])
		if err != nil || n < 1 {
			return 0, errors.New("GitHub list has malformed last page")
		}
		last = max(last, n)
	}
	if next && last == 1 {
		return 0, errors.New("GitHub list pagination has no last page")
	}
	return last, nil
}

// cachedList reads a bounded tail of GitHub's ascending lists. A partial final
// page needs up to six physical pages before trimming to the newest 500 entries.
func cachedList[T any](c *Connector, ctx context.Context, path string) ([]T, error) {
	u, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	pagePath := func(page int) string {
		q := u.Query()
		q.Set("per_page", "100")
		q.Set("page", strconv.Itoa(page))
		u.RawQuery = q.Encode()
		return u.String()
	}
	var first []T
	link, discoveredAt, err := c.cachedPageSince(ctx, pagePath(1), &first, true, 0)
	if err != nil {
		return nil, err
	}
	last, err := listLastPage(link)
	if err != nil {
		return nil, err
	}
	if last == 1 {
		return first, nil
	}
	var tail []T
	if _, _, err := c.cachedPageSince(ctx, pagePath(last), &tail, false, discoveredAt); err != nil {
		return nil, err
	}
	const window = threadListPages * 100
	start := max(1, last-(window-len(tail)+99)/100)
	var all []T
	for page := start; page < last; page++ {
		batch := first
		if page != 1 {
			if _, _, err := c.cachedPageSince(ctx, pagePath(page), &batch, false, discoveredAt); err != nil {
				return nil, err
			}
		}
		all = append(all, batch...)
	}
	all = append(all, tail...)
	if len(all) > window {
		all = all[len(all)-window:]
	}
	return all, nil
}

type person struct {
	Login     string `json:"login"`
	Type      string `json:"type"`
	AvatarURL string `json:"avatar_url"`
}

type threadIssue struct {
	Title     string    `json:"title"`
	State     string    `json:"state"`
	User      person    `json:"user"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	Locked    bool      `json:"locked"`
	CreatedAt time.Time `json:"created_at"`
	Labels    []struct {
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"labels"`
	PullRequest *struct {
		MergedAt *time.Time `json:"merged_at"`
	} `json:"pull_request"`
}

type threadPull struct {
	Draft  bool `json:"draft"`
	Merged bool `json:"merged"`
	Head   struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type threadComment struct {
	ID        int64     `json:"id"`
	User      person    `json:"user"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// reviews
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
	// comments on a line
	Path         string `json:"path"`
	Line         int    `json:"line"`
	OriginalLine int    `json:"original_line"`
	DiffHunk     string `json:"diff_hunk"`
	InReplyTo    int64  `json:"in_reply_to_id"`
}

type threadEvent struct {
	ID        int64     `json:"id"`
	Event     string    `json:"event"`
	Actor     person    `json:"actor"`
	CreatedAt time.Time `json:"created_at"`
	Label     struct {
		Name string `json:"name"`
	} `json:"label"`
	Assignee          person `json:"assignee"`
	RequestedReviewer person `json:"requested_reviewer"`
	RequestedTeam     struct {
		Name string `json:"name"`
	} `json:"requested_team"`
	Rename struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"rename"`
}

type threadCheckRun struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	HTMLURL     string     `json:"html_url"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	App         struct {
		Slug string `json:"slug"`
	} `json:"app"`
}

// Thread is a page of a PR's or issue's timeline ("example-org/example-repo#12"), oldest first: the newest limit
// entries for before "", else those before the entry before names.
func (c *Connector) Thread(ctx context.Context, thread, before string, limit int) (connector.ThreadPage, error) {
	m := threadRef.FindStringSubmatch(thread)
	if m == nil || !validRepo(m[1]) {
		return connector.ThreadPage{}, fmt.Errorf("%s is not a pull request or an issue", thread)
	}
	repo, number := m[1], m[2]
	base := fmt.Sprintf("/repos/%s/issues/%s", repo, number)
	var it threadIssue
	if err := c.cachedGet(ctx, base, &it); err != nil {
		return connector.ThreadPage{}, err
	}
	me := c.Status(ctx).Account
	chat := connector.ThreadChat{ID: thread, Name: it.Title, Kind: "issue", URL: it.HTMLURL, State: it.State,
		Author: c.threadPerson(it.User), Body: strings.TrimSpace(it.Body), BodyFormat: "markdown"}
	if !it.Locked {
		chat.Reply = "github_comment"
	}
	for _, l := range it.Labels {
		chat.Labels = append(chat.Labels, connector.ThreadLabel{Name: l.Name, Color: l.Color})
	}
	var all []connector.ThreadMessage
	add := func(ms ...connector.ThreadMessage) { all = append(all, ms...) }

	comments, err := cachedList[threadComment](c, ctx, base+"/comments")
	if err != nil {
		return connector.ThreadPage{}, err
	}
	for _, cm := range comments {
		add(connector.ThreadMessage{ID: "c" + strconv.FormatInt(cm.ID, 10), At: cm.CreatedAt, Sender: *c.threadPerson(cm.User),
			Mine: mine(cm.User, me), Text: cm.Body, Format: "markdown", Edited: edited(cm), URL: cm.HTMLURL})
	}
	events, err := cachedList[threadEvent](c, ctx, base+"/events")
	if err != nil {
		return connector.ThreadPage{}, err
	}
	merged := map[int64]bool{} // a merge's second: GitHub adds a closed event to it, said by the merge
	for _, ev := range events {
		if ev.Event == "merged" {
			merged[ev.CreatedAt.Unix()] = true
		}
	}
	for _, ev := range events {
		if ev.Event == "closed" && (merged[ev.CreatedAt.Unix()] || merged[ev.CreatedAt.Unix()-1] || merged[ev.CreatedAt.Unix()+1]) {
			continue
		}
		if text := eventText(ev); text != "" {
			add(connector.ThreadMessage{ID: "e" + strconv.FormatInt(ev.ID, 10), At: ev.CreatedAt, Sender: *c.threadPerson(ev.Actor),
				Mine: mine(ev.Actor, me), Event: true, Text: text})
		}
	}

	if it.PullRequest != nil {
		chat.Kind = "pr"
		pulls := fmt.Sprintf("/repos/%s/pulls/%s", repo, number)
		var pr threadPull
		if err := c.cachedGet(ctx, pulls, &pr); err != nil {
			return connector.ThreadPage{}, err
		}
		chat.Branch = &connector.ThreadBranch{Head: pr.Head.Ref, Base: pr.Base.Ref}
		switch {
		case pr.Merged:
			chat.State = "merged"
		case pr.Draft && it.State == "open":
			chat.State = "draft"
		}
		ms, err := c.reviewMessages(ctx, pulls, me)
		if err != nil {
			return connector.ThreadPage{}, err
		}
		add(ms...)
		if it.State == "open" && pr.Head.SHA != "" {
			// The checks of the head commit; a list that cannot be read leaves the rest of the thread as it is.
			if ms, err := c.checkMessages(ctx, repo, pr.Head.SHA, it.CreatedAt); err == nil {
				add(ms...)
			} else if errors.Is(err, context.Canceled) {
				return connector.ThreadPage{}, err
			}
		}
	}
	slices.SortStableFunc(all, func(a, b connector.ThreadMessage) int { return a.At.Compare(b.At) })

	end := len(all)
	if before != "" {
		end = slices.IndexFunc(all, func(m connector.ThreadMessage) bool { return m.ID == before })
		if end < 0 {
			return connector.ThreadPage{}, fmt.Errorf("%s: no entry %s (the thread changed: read it again)", thread, before)
		}
	}
	limit = min(max(limit, 1), connector.ThreadMost)
	start := max(0, end-limit)
	page := connector.ThreadPage{Chat: chat, Messages: slices.Clone(all[start:end])}
	if start > 0 {
		page.Before = all[start].ID
	}
	return page, nil
}

// reviewMessages are a PR's reviews (not the empty ones that only hold comments on lines) and its comments on lines.
func (c *Connector) reviewMessages(ctx context.Context, pulls, me string) ([]connector.ThreadMessage, error) {
	reviews, err := cachedList[threadComment](c, ctx, pulls+"/reviews")
	if err != nil {
		return nil, err
	}
	var out []connector.ThreadMessage
	for _, r := range reviews {
		state := strings.ToLower(r.State)
		if state == "pending" || state == "" || state == "commented" && strings.TrimSpace(r.Body) == "" {
			continue
		}
		out = append(out, connector.ThreadMessage{ID: "r" + strconv.FormatInt(r.ID, 10), At: r.SubmittedAt, Sender: *c.threadPerson(r.User),
			Mine: mine(r.User, me), Kind: "review", Review: state, Text: r.Body, Format: "markdown", URL: r.HTMLURL})
	}
	lines, err := cachedList[threadComment](c, ctx, pulls+"/comments")
	if err != nil {
		return nil, err
	}
	byID := map[int64]threadComment{}
	for _, cm := range lines {
		byID[cm.ID] = cm
	}
	for _, cm := range lines {
		line := cm.Line
		if line == 0 {
			line = cm.OriginalLine // on a line the PR no longer has
		}
		msg := connector.ThreadMessage{ID: "rc" + strconv.FormatInt(cm.ID, 10), At: cm.CreatedAt, Sender: *c.threadPerson(cm.User),
			Mine: mine(cm.User, me), Kind: "code", Text: cm.Body, Format: "markdown", Edited: edited(cm), URL: cm.HTMLURL,
			Code: &connector.ThreadCode{Path: cm.Path, Line: line}}
		if q, ok := byID[cm.InReplyTo]; ok {
			msg.ReplyTo = &connector.ThreadQuote{ID: "rc" + strconv.FormatInt(q.ID, 10), From: q.User.Login, Text: firstLine(q.Body)}
		} else {
			msg.Code.Hunk = lastLines(cm.DiffHunk, hunkLines) // the first of a conversation shows the code; answers do not repeat it
		}
		out = append(out, msg)
	}
	return out, nil
}

// checkMessages are the head commit's checks, each at the time it finished (or started); a failed Actions job carries
// the end of its log.
func (c *Connector) checkMessages(ctx context.Context, repo, sha string, opened time.Time) ([]connector.ThreadMessage, error) {
	var res struct {
		Runs []threadCheckRun `json:"check_runs"`
	}
	if err := c.cachedGet(ctx, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", repo, url.PathEscape(sha)), &res); err != nil {
		return nil, err
	}
	var out []connector.ThreadMessage
	logs := 0
	for _, r := range res.Runs {
		state := checkState(r)
		at := opened
		switch {
		case r.CompletedAt != nil:
			at = *r.CompletedAt
		case r.StartedAt != nil:
			at = *r.StartedAt
		}
		ck := &connector.ThreadCheck{Name: r.Name, State: state, URL: r.HTMLURL}
		if state == "failure" && r.App.Slug == "github-actions" && logs < failedLogs {
			logs++
			ck.Log = c.checkLog(ctx, repo, r.ID)
		}
		out = append(out, connector.ThreadMessage{ID: "ck" + strconv.FormatInt(r.ID, 10), At: at,
			Sender: connector.ThreadPerson{Name: cmp.Or(r.App.Slug, "checks")}, Kind: "check", Check: ck, URL: r.HTMLURL})
	}
	return out, nil
}

// checkLog is the end of a failed job's log, read once per job; "" when it cannot be read.
func (c *Connector) checkLog(ctx context.Context, repo string, job int64) string {
	e := &c.etags
	e.mu.Lock()
	log, ok := e.logs[job]
	e.mu.Unlock()
	if ok {
		return log
	}
	raw, err := c.raw(ctx, fmt.Sprintf("/repos/%s/actions/jobs/%d/logs", repo, job))
	if err != nil {
		return ""
	}
	log = tail(raw, checkLogTail)
	e.mu.Lock()
	if e.logs == nil || len(e.logs) > etagKeep {
		e.logs = map[int64]string{}
	}
	e.logs[job] = log
	e.mu.Unlock()
	return log
}

func checkState(r threadCheckRun) string {
	if r.Status != "completed" {
		return "pending"
	}
	switch r.Conclusion {
	case "success", "neutral":
		return "success"
	case "failure", "timed_out", "action_required", "startup_failure":
		return "failure"
	case "cancelled":
		return "cancelled"
	}
	return "skipped" // skipped, stale
}

// eventText is the gray line of an event, "" for those the thread does not show (subscribed, mentioned, referenced…).
func eventText(ev threadEvent) string {
	who := ev.Actor.Login
	if who == "" {
		who = "有人"
	}
	switch ev.Event {
	case "labeled":
		return who + " 加了标签 " + ev.Label.Name
	case "unlabeled":
		return who + " 去掉了标签 " + ev.Label.Name
	case "assigned":
		if ev.Assignee.Login == ev.Actor.Login {
			return who + " 认领了"
		}
		return who + " 指派给 " + ev.Assignee.Login
	case "unassigned":
		return who + " 取消了指派 " + ev.Assignee.Login
	case "review_requested":
		if ev.RequestedTeam.Name != "" {
			return who + " 请团队 " + ev.RequestedTeam.Name + " review"
		}
		return who + " 请 " + ev.RequestedReviewer.Login + " review"
	case "review_request_removed":
		return who + " 撤回了给 " + cmp.Or(ev.RequestedReviewer.Login, ev.RequestedTeam.Name) + " 的 review 请求"
	case "renamed":
		return who + " 把标题从「" + ev.Rename.From + "」改成「" + ev.Rename.To + "」"
	case "closed":
		return who + " 关闭了"
	case "reopened":
		return who + " 重新打开了"
	case "merged":
		return who + " 合并了"
	case "head_ref_force_pushed":
		return who + " 强制推送了分支"
	case "head_ref_deleted":
		return who + " 删除了分支"
	case "ready_for_review":
		return who + " 标为可以 review"
	case "convert_to_draft":
		return who + " 改回了草稿"
	case "locked":
		return who + " 锁定了讨论"
	case "unlocked":
		return who + " 解锁了讨论"
	}
	return ""
}

// threadPerson is who wrote something, with their avatar's media ref ("avatar:<login>:<version>").
func (c *Connector) threadPerson(p person) *connector.ThreadPerson {
	tp := &connector.ThreadPerson{ID: p.Login, Name: p.Login}
	if p.Login == "" {
		tp.Name = "(deleted user)"
		return tp
	}
	if p.AvatarURL != "" {
		u, err := url.Parse(p.AvatarURL)
		if err == nil {
			tp.Avatar = "avatar:" + p.Login + ":" + cmp.Or(u.Query().Get("v"), "1")
			c.etags.mu.Lock()
			if c.etags.avatars == nil {
				c.etags.avatars = map[string]string{}
			}
			c.etags.avatars[p.Login] = p.AvatarURL
			c.etags.mu.Unlock()
		}
	}
	return tp
}

// Media is an avatar ("avatar:<login>:<version>"), small; GitHub's avatar host gets no token.
func (c *Connector) Media(ctx context.Context, ref string) (connector.Media, error) {
	parts := strings.Split(ref, ":")
	if len(parts) != 3 || parts[0] != "avatar" || parts[1] == "" {
		return connector.Media{}, fmt.Errorf("no media %q", ref)
	}
	login := parts[1]
	c.etags.mu.Lock()
	src := c.etags.avatars[login]
	c.etags.mu.Unlock()
	if src == "" { // not seen since the hub started: ask GitHub where it is
		var u person
		if err := c.cachedGet(ctx, "/users/"+url.PathEscape(login), &u); err != nil {
			return connector.Media{}, err
		}
		src = u.AvatarURL
	}
	au, err := url.Parse(src)
	if err != nil || au.Scheme != "https" {
		return connector.Media{}, fmt.Errorf("no avatar for %s", login)
	}
	q := au.Query()
	q.Set("s", "96")
	au.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, au.String(), nil)
	if err != nil {
		return connector.Media{}, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return connector.Media{}, errors.New(Scrub(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return connector.Media{}, fmt.Errorf("avatar of %s: %d", login, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, connector.MediaMost+1))
	if err != nil {
		return connector.Media{}, err
	}
	if len(data) > connector.MediaMost {
		return connector.Media{}, fmt.Errorf("avatar of %s is too big", login)
	}
	return connector.Media{Mime: resp.Header.Get("Content-Type"), Data: data}, nil
}

func mine(p person, me string) bool { return me != "" && strings.EqualFold(p.Login, me) }

func edited(cm threadComment) bool { return cm.UpdatedAt.Sub(cm.CreatedAt) > time.Minute }

// lastLines is the last n lines of s.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
