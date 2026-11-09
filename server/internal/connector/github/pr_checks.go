package github

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// PRCheckSnapshot is a PR head's check runs and commit statuses. An empty
// snapshot is not evidence that CI has finished.
type PRCheckSnapshot struct {
	Head        string
	Fingerprint string
	Pending     bool
	Failures    []string
	Count       int
	identities  []string
}

// AddRun and AddStatus collect the same check semantics from either source.
// Callers validate their own input and completeness before adding it.
func (s *PRCheckSnapshot) AddRun(id int64, name, status, conclusion string) {
	s.identities = append(s.identities, fmt.Sprintf("run:%d:%q:%q:%q", id, name, status, conclusion))
	s.Count++
	if name == "" || status != "completed" || conclusion == "" {
		s.Pending = true
		return
	}
	switch conclusion {
	case "success", "neutral", "skipped":
	case "failure", "timed_out", "cancelled", "action_required", "startup_failure", "stale":
		s.Failures = append(s.Failures, name)
	default:
		s.Pending = true
	}
}

func (s *PRCheckSnapshot) AddStatus(context, state string) {
	s.identities = append(s.identities, fmt.Sprintf("status:%q:%q", context, state))
	s.Count++
	if context == "" {
		s.Pending = true
		return
	}
	switch state {
	case "success":
	case "failure", "error":
		s.Failures = append(s.Failures, context)
	default:
		s.Pending = true
	}
}

// Finish seals a snapshot with a stable identity for its head and checks.
func (s *PRCheckSnapshot) Finish() {
	sort.Strings(s.Failures)
	sort.Strings(s.identities)
	hash := sha256.Sum256([]byte(s.Head + "\x00" + strings.Join(s.identities, "\x00")))
	s.Fingerprint = fmt.Sprintf("%x", hash)
	s.identities = nil
}

// PRChecks reads every page without retrieving job logs. It refuses an incomplete
// result, including a changed PR head, so callers never mistake truncation for completion.
func (c *Connector) PRChecks(ctx context.Context, repo string, number int) (PRCheckSnapshot, error) {
	var result PRCheckSnapshot
	if !validRepo(repo) || number <= 0 {
		return result, fmt.Errorf("invalid pull request")
	}
	if c.ratesLow() {
		return result, fmt.Errorf("GitHub rate limit low")
	}
	prPath := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	get := func(path string, out any) error {
		_, err := c.Get(ctx, path, out) // a rate limit pauses the connector (request, notePause)
		return err
	}
	readHead := func() (string, error) {
		var pr struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}
		err := get(prPath, &pr)
		if err == nil && pr.Head.SHA == "" {
			err = fmt.Errorf("missing PR head")
		}
		return pr.Head.SHA, err
	}
	head, err := readHead()
	if err != nil {
		return result, err
	}
	result.Head = head
	ref := url.PathEscape(head)
	// GitHub caps the check-runs endpoint at 1000 results. Refuse a larger
	// set instead of silently omitting checks.
	checkTotal := -1
	seenRuns := map[int64]bool{}
	for page := 1; ; page++ {
		if c.ratesLow() {
			return PRCheckSnapshot{}, fmt.Errorf("GitHub rate limit low")
		}
		var response struct {
			Total int `json:"total_count"`
			Runs  []struct {
				ID                       int64 `json:"id"`
				Name, Status, Conclusion string
			} `json:"check_runs"`
		}
		err := get(fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100&page=%d", repo, ref, page), &response)
		if err != nil {
			return PRCheckSnapshot{}, err
		}
		if response.Total > 1000 || response.Total < 0 || checkTotal >= 0 && checkTotal != response.Total {
			return PRCheckSnapshot{}, fmt.Errorf("incomplete check runs")
		}
		checkTotal = response.Total
		for _, run := range response.Runs {
			if run.ID <= 0 || seenRuns[run.ID] {
				return PRCheckSnapshot{}, fmt.Errorf("inconsistent check runs")
			}
			seenRuns[run.ID] = true
			result.AddRun(run.ID, run.Name, run.Status, run.Conclusion)
		}
		if result.Count > response.Total {
			return PRCheckSnapshot{}, fmt.Errorf("inconsistent check runs")
		}
		if result.Count == response.Total {
			break
		}
		if len(response.Runs) == 0 || page >= 10 {
			return PRCheckSnapshot{}, fmt.Errorf("incomplete check runs")
		}
	}
	statusCount, statusTotal := 0, -1
	seenStatuses := map[string]bool{}
	for page := 1; ; page++ {
		if c.ratesLow() {
			return PRCheckSnapshot{}, fmt.Errorf("GitHub rate limit low")
		}
		var response struct {
			Total    int                               `json:"total_count"`
			Statuses []struct{ Context, State string } `json:"statuses"`
		}
		err := get(fmt.Sprintf("/repos/%s/commits/%s/status?per_page=100&page=%d", repo, ref, page), &response)
		if err != nil {
			return PRCheckSnapshot{}, err
		}
		if response.Total > 1000 || response.Total < 0 || statusTotal >= 0 && statusTotal != response.Total {
			return PRCheckSnapshot{}, fmt.Errorf("incomplete commit statuses")
		}
		statusTotal = response.Total
		for _, status := range response.Statuses {
			if seenStatuses[status.Context] {
				return PRCheckSnapshot{}, fmt.Errorf("inconsistent commit statuses")
			}
			seenStatuses[status.Context] = true
			statusCount++
			result.AddStatus(status.Context, status.State)
		}
		if statusCount > response.Total {
			return PRCheckSnapshot{}, fmt.Errorf("inconsistent commit statuses")
		}
		if statusCount == response.Total {
			break
		}
		if len(response.Statuses) == 0 || page >= 10 {
			return PRCheckSnapshot{}, fmt.Errorf("incomplete commit statuses")
		}
	}
	if c.ratesLow() {
		return PRCheckSnapshot{}, fmt.Errorf("GitHub rate limit low")
	}
	current, err := readHead()
	if err != nil {
		return PRCheckSnapshot{}, err
	}
	if current != head {
		return PRCheckSnapshot{}, fmt.Errorf("PR head changed")
	}
	result.Finish()
	return result, nil
}

// PRChecksRateReset is the earliest time to resume nonessential GitHub reads
// when the connector is conserving its hourly quota or waiting out a pause
// GitHub asked for.
func (c *Connector) PRChecksRateReset() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.limitedUntil(time.Now())
}
