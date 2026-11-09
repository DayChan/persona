package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
)

// The knowledge base (docs/plans/plan-connectors.md "4 · Knowledge base"): the PRs and issues the user is involved in
// (involves:@me — author, assignee, mentioned, commented, review requested), never whole repositories. One chat per
// PR / issue, kind pr or issue: its description, then comments, reviews and inline review comments in time order.

var (
	archiveFirstDays = 30                  // how far back the first run looks
	archiveForget    = 90 * 24 * time.Hour // threads quiet this long leave the cursor
	archiveText      = 4000                // characters of one message kept
	archiveListPages = 10                  // pages of 100 read per list and call; a longer list goes on in the next call
)

// archiveCursor is the connector's cursor: the updated_at reached, and per thread the time its messages are archived up to.
type archiveCursor struct {
	Since   time.Time            `json:"since"`
	Threads map[string]time.Time `json:"threads,omitempty"`
}

// Changes reads the threads updated since the cursor, oldest first, with the messages not archived yet. It waits while
// either the hour's core quota or the search API's minute runs low.
func (c *Connector) Changes(ctx context.Context, cursor string, limit int) ([]connector.ChatBatch, string, bool, error) {
	if !c.signedIn() || c.ratesLow() || c.searchQuotaLow() {
		return nil, cursor, false, nil
	}
	var cur archiveCursor
	if cursor != "" {
		if err := json.Unmarshal([]byte(cursor), &cur); err != nil {
			return nil, cursor, false, fmt.Errorf("cursor: %v", err)
		}
	}
	if cur.Threads == nil {
		cur.Threads = map[string]time.Time{}
	}
	if cur.Since.IsZero() {
		cur.Since = time.Now().UTC().AddDate(0, 0, -archiveFirstDays)
	}
	me := c.Status(ctx).Account
	q := url.Values{"q": {"involves:@me updated:>=" + cur.Since.UTC().Format(time.RFC3339)}, "sort": {"updated"}, "order": {"asc"},
		"per_page": {fmt.Sprint(max(1, min(limit, 100)))}}
	var res struct {
		Total int     `json:"total_count"`
		Items []issue `json:"items"`
	}
	if _, err := c.Get(ctx, "/search/issues?"+q.Encode(), &res); err != nil {
		return nil, cursor, false, err
	}
	var batches []connector.ChatBatch
	partial := "" // a thread read only in part, which the next call goes on with
	for _, it := range res.Items {
		thread := it.repo() + "#" + fmt.Sprint(it.Number)
		msgs, upTo, whole, err := c.threadMessages(ctx, it, cur.Threads[thread], me)
		if err != nil {
			return batches, encode(cur), false, err // what was read so far is archived; this thread comes again
		}
		if !upTo.IsZero() {
			cur.Threads[thread] = upTo
		}
		if len(msgs) > 0 {
			kind := "issue"
			if it.PullRequest != nil {
				kind = "pr"
			}
			owner, name, _ := strings.Cut(it.repo(), "/")
			batches = append(batches, connector.ChatBatch{Chat: kb.SourceChat{
				ID:   fmt.Sprintf("%s-%s-%d", owner, name, it.Number),
				Name: fmt.Sprintf("%s#%d %s", name, it.Number, it.Title), Kind: kind, UpdatedAt: it.UpdatedAt}, Messages: msgs})
		}
		if !whole {
			partial = thread // cur.Since stays before it, so the next search finds it again
			break
		}
		if it.UpdatedAt.After(cur.Since) {
			cur.Since = it.UpdatedAt
		}
	}
	for t, at := range cur.Threads {
		if time.Since(at) > archiveForget && t != partial {
			delete(cur.Threads, t)
		}
	}
	return batches, encode(cur), partial != "" || res.Total > len(res.Items), nil
}

func encode(cur archiveCursor) string {
	b, _ := json.Marshal(cur)
	return string(b)
}

// threadMessages are a thread's messages after after in time order (all of them, the description first, when it is
// zero) and upTo, the thread's next cursor. When a list goes on past archiveListPages pages, whole is false and the
// messages from the last one read on are left to the next call, since the rest of that second may be on the next
// page; should that leave nothing, the messages up to that second are taken, and a list without since (reviews) gets
// no further.
func (c *Connector) threadMessages(ctx context.Context, it issue, after time.Time, me string) (out []kb.SourceMessage, upTo time.Time, whole bool, err error) {
	repo, n := it.repo(), it.Number
	add := func(id string, u user, at time.Time, text string) {
		text = strings.TrimSpace(text)
		if !at.After(after) || text == "" {
			return
		}
		if r := []rune(text); len(r) > archiveText {
			text = string(r[:archiveText]) + "…"
		}
		out = append(out, kb.SourceMessage{ID: id, SenderID: u.Login, SenderName: who(u), Mine: me != "" && strings.EqualFold(u.Login, me),
			Type: "TEXT", Text: text, At: at, SenderHandles: []kb.Handle{{Kind: kb.HandleGitHub, Value: u.Login}}})
	}
	var cut time.Time // the earliest end of a list read in part, among those past after
	end := func(last time.Time) {
		if last.After(after) && (cut.IsZero() || last.Before(cut)) {
			cut = last
		}
	}
	add("body", it.User, it.CreatedAt, "【"+it.Title+"】\n"+it.Body)

	since := ""
	if !after.IsZero() {
		since = "&since=" + url.QueryEscape(after.UTC().Format(time.RFC3339))
	}
	type listed struct {
		ID int64 `json:"id"`
		comment
	}
	cs, more, err := archivePages[listed](c, ctx, fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100%s", repo, n, since))
	if err != nil {
		return nil, after, false, err
	}
	for _, cm := range cs {
		add(fmt.Sprint("c", cm.ID), cm.User, cm.CreatedAt, cm.Body)
	}
	if more {
		end(cs[len(cs)-1].CreatedAt)
	}
	if it.PullRequest != nil {
		rs, more, err := archivePages[listed](c, ctx, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, n))
		if err != nil {
			return nil, after, false, err
		}
		for _, r := range rs {
			verb := map[string]string{"APPROVED": "批准了", "CHANGES_REQUESTED": "要求修改", "COMMENTED": "评审了"}[r.State]
			if verb == "" || r.State == "COMMENTED" && strings.TrimSpace(r.Body) == "" {
				continue
			}
			text := "（review）" + verb
			if b := strings.TrimSpace(r.Body); b != "" {
				text += "：" + b
			}
			add(fmt.Sprint("r", r.ID), r.User, r.SubmittedAt, text)
		}
		if more {
			end(rs[len(rs)-1].SubmittedAt)
		}
		type inline struct {
			ID        int64     `json:"id"`
			User      user      `json:"user"`
			Body      string    `json:"body"`
			Path      string    `json:"path"`
			Line      *int      `json:"line"`
			CreatedAt time.Time `json:"created_at"`
		}
		ics, more, err := archivePages[inline](c, ctx, fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100%s", repo, n, since))
		if err != nil {
			return nil, after, false, err
		}
		for _, ic := range ics {
			where := ic.Path
			if ic.Line != nil {
				where += fmt.Sprintf(":%d", *ic.Line)
			}
			add(fmt.Sprint("i", ic.ID), ic.User, ic.CreatedAt, "（"+where+"）"+ic.Body)
		}
		if more {
			end(ics[len(ics)-1].CreatedAt)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	upTo = after
	if !cut.IsZero() {
		keep := sort.Search(len(out), func(i int) bool { return !out[i].At.Before(cut) })
		if keep == 0 {
			keep, upTo = sort.Search(len(out), func(i int) bool { return out[i].At.After(cut) }), cut
		}
		out = out[:keep]
	}
	if len(out) > 0 && out[len(out)-1].At.After(upTo) {
		upTo = out[len(out)-1].At
	}
	return out, upTo, cut.IsZero(), nil
}

// archivePages reads a list (path asks per_page=100) up to archiveListPages pages; more says it goes on past them.
func archivePages[T any](c *Connector, ctx context.Context, path string) (all []T, more bool, err error) {
	for page := 1; page <= archiveListPages; page++ {
		var batch []T
		if _, err := c.Get(ctx, fmt.Sprintf("%s&page=%d", path, page), &batch); err != nil {
			return nil, false, err
		}
		all = append(all, batch...)
		if len(batch) < 100 {
			return all, false, nil
		}
	}
	return all, true, nil
}
