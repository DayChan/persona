package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// fakeCI is GitHub for CI notifications on made-up data: example-org/example-repo (default branch main), my open PR
// #5 from feat/x, runs per branch, branches that exist.
type fakeCI struct {
	mu            sync.Mutex
	notifications []map[string]any
	runs          map[string]string // branch -> workflow_runs JSON
	branches      map[string]bool
	prMerged      bool
}

func (f *fakeCI) note(id, title string, repo string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notifications = append(f.notifications, map[string]any{"id": id, "reason": "ci_activity", "updated_at": time.Now().UTC().Format(time.RFC3339),
		"subject":    map[string]any{"title": title, "url": nil, "type": "CheckSuite"},
		"repository": map[string]string{"full_name": repo, "html_url": "https://github.com/" + repo}})
}

// noteAt adds a notification updated at a given time; GitHub lists them newest first, so add the newest first.
func (f *fakeCI) noteAt(id, title, repo string, at time.Time) {
	f.note(id, title, repo)
	f.mu.Lock()
	f.notifications[len(f.notifications)-1]["updated_at"] = at.UTC().Format(time.RFC3339)
	f.mu.Unlock()
}

func (f *fakeCI) server(t *testing.T) *Connector {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p := r.URL.Path
		switch {
		case p == "/user":
			w.Write([]byte(`{"login":"me"}`))
		case p == "/notifications":
			json.NewEncoder(w).Encode(f.notifications)
		case p == "/repos/example-org/example-repo":
			w.Write([]byte(`{"default_branch":"main"}`))
		case p == "/repos/example-org/example-repo/pulls" && r.URL.Query().Get("head") == "example-org:feat/x":
			w.Write([]byte(`[{"number":5,"user":{"login":"me"}}]`))
		case p == "/repos/example-org/example-repo/pulls":
			w.Write([]byte(`[]`))
		case p == "/repos/example-org/example-repo/pulls/5":
			if f.prMerged {
				w.Write([]byte(`{"state":"closed","merged":true}`))
			} else {
				w.Write([]byte(`{"state":"open","merged":false}`))
			}
		case p == "/repos/example-org/example-repo/actions/runs":
			runs := f.runs[r.URL.Query().Get("branch")]
			if runs == "" {
				runs = "[]"
			}
			w.Write([]byte(`{"workflow_runs":` + runs + `}`))
		case strings.HasPrefix(p, "/repos/example-org/example-repo/branches/"):
			if f.branches[strings.TrimPrefix(p, "/repos/example-org/example-repo/branches/")] {
				w.Write([]byte(`{}`))
			} else {
				w.WriteHeader(404)
				w.Write([]byte(`{"message":"Branch not found"}`))
			}
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	c.check(context.Background())
	return c
}

// CI failures are one item per repository and branch (reruns join it), as urgent as the branch: the default
// branch's and my open PR's need me, other branches' are FYI, automation's (train/*) and muted repositories' none.
func TestCIFailuresPerBranchAndTier(t *testing.T) {
	f := &fakeCI{branches: map[string]bool{"main": true, "feat/x": true, "feat/y": true}}
	f.note("1", "CI workflow run failed for main branch", "example-org/example-repo")
	f.note("2", "CI workflow run failed for feat/x branch", "example-org/example-repo")
	f.note("3", "CI workflow run failed for feat/y branch", "example-org/example-repo")
	f.note("4", "CI workflow run, Attempt #2 failed for feat/y branch", "example-org/example-repo")
	f.note("5", "pr train/20261102-1 default workflow run failed for train/20261102-1 branch", "example-org/example-repo")
	f.note("6", "CI workflow run failed for main branch", "example-org/muted-repo")
	f.note("7", "Lint workflow run cancelled for feat/z branch", "example-org/example-repo")
	c := f.server(t)
	_ = c.SetOptions(json.RawMessage(`{"muted_repos":["example-org/muted-repo"]}`))
	if _, _, err := c.poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := things(t, c)
	if len(got) != 3 {
		t.Fatalf("%d things: %v", len(got), keys(got))
	}
	for thread, want := range map[string][2]string{
		"ci:example-org/example-repo@main":   {"action", "规则：默认分支的 CI 失败"},
		"ci:example-org/example-repo@feat/x": {"action", "规则：你开着的 PR 的 CI 失败"},
		"ci:example-org/example-repo@feat/y": {"fyi", "规则：功能分支的 CI 失败"},
	} {
		th := got[thread]
		if string(th.Tier) != want[0] || th.Why != want[1] || th.Kind != "ci_failed" || th.CanReply || th.Title == "" {
			t.Errorf("%s: %+v", thread, th)
		}
	}
	if title := got["ci:example-org/example-repo@main"].Title; title != "example-repo · main 的 CI 失败" {
		t.Errorf("title %q does not name the repository", title)
	}
	if u := got["ci:example-org/example-repo@feat/x"].URL; u != "https://github.com/example-org/example-repo/actions?query=branch%3Afeat%2Fx" {
		t.Errorf("url %s", u)
	}
	if ctx := got["ci:example-org/example-repo@feat/x"].Context; !strings.Contains(ctx, "PR #5") {
		t.Errorf("context %s", ctx)
	}
}

// An open CI item is over when the branch is green again (every workflow's newest finished run passed), the branch is
// gone or the PR merged; a success notification has it checked at once.
func TestCIFailuresSettleThemselves(t *testing.T) {
	old := ciRecheck
	ciRecheck = 0
	t.Cleanup(func() { ciRecheck = old })
	f := &fakeCI{branches: map[string]bool{"main": true, "feat/x": true, "feat/y": true, "feat/w": true}}
	f.note("1", "CI workflow run failed for main branch", "example-org/example-repo")
	f.note("2", "CI workflow run failed for feat/x branch", "example-org/example-repo")
	f.note("3", "CI workflow run failed for feat/y branch", "example-org/example-repo")
	f.note("4", "CI workflow run failed for feat/w branch", "example-org/example-repo")
	c := f.server(t)
	c.poll(context.Background())
	f.mu.Lock()
	f.runs = map[string]string{
		// CI passes again but Lint's newest run still failed: the branch is still failing.
		"main": `[{"name":"CI","status":"completed","conclusion":"success"},{"name":"Lint","status":"completed","conclusion":"failure"}]`,
		// Every workflow's newest finished run passed (one still running, one cancelled since).
		"feat/y": `[{"name":"Lint","status":"completed","conclusion":"success"},{"name":"CI","status":"in_progress"},` +
			`{"name":"CI","status":"completed","conclusion":"cancelled"},{"name":"CI","status":"completed","conclusion":"success"},` +
			`{"name":"Lint","status":"completed","conclusion":"failure"},{"name":"CI","status":"completed","conclusion":"failure"}]`,
	}
	f.prMerged = true
	delete(f.branches, "feat/w")
	f.mu.Unlock()
	c.followUpCI(context.Background())
	got := things(t, c)
	for thread, want := range map[string]string{
		"ci:example-org/example-repo@main":   "",
		"ci:example-org/example-repo@feat/x": "GitHub：PR 已合并",
		"ci:example-org/example-repo@feat/y": "GitHub：后来成功了",
		"ci:example-org/example-repo@feat/w": "GitHub：分支已删除",
	} {
		th := got[thread]
		if want == "" && th.Done != "" || want != "" && (th.Done != connector.DoneRead || th.Why != want) {
			t.Errorf("%s: done %q why %q, want %q", thread, th.Done, th.Why, want)
		}
	}

	// A success notification has the branch checked at once (not at the next round's recheck): over once every
	// workflow passes.
	ciRecheck = old
	f.mu.Lock()
	f.runs["main"] = `[{"name":"Lint","status":"completed","conclusion":"success"},{"name":"CI","status":"completed","conclusion":"success"},` +
		`{"name":"Lint","status":"completed","conclusion":"failure"}]`
	f.mu.Unlock()
	f.note("5", "Lint workflow run succeeded for main branch", "example-org/example-repo")
	c.inbox.fullAt = time.Time{}
	c.poll(context.Background())
	if th := things(t, c)["ci:example-org/example-repo@main"]; th.Done != connector.DoneRead || th.Why != "GitHub：后来成功了" {
		t.Errorf("success notification: %+v", th)
	}
}

// A branch failing for hours (#1527): its many unread notifications, read again after a restart a few per poll, leave
// the thing at the newest failure (not the oldest read); failed runs GitHub sent no notification of (another
// account's pushes) join the item once settled; a run just ended waits for its own notification.
func TestCIThingKeepsNewestFailure(t *testing.T) {
	oldDetails, oldRecheck := maxDetails, ciRecheck
	maxDetails, ciRecheck = 2, 0
	t.Cleanup(func() { maxDetails, ciRecheck = oldDetails, oldRecheck })
	base := time.Now().Add(-14 * time.Hour).Truncate(time.Second)
	f := &fakeCI{branches: map[string]bool{"main": true}}
	f.noteAt("9", "android workflow run failed for main branch", "example-org/example-repo", base.Add(2*time.Hour+1*time.Second))
	f.noteAt("8", "contracts workflow run failed for main branch", "example-org/example-repo", base.Add(2*time.Hour))
	f.noteAt("7", "kotlin workflow run failed for main branch", "example-org/example-repo", base.Add(time.Hour+48*time.Minute))
	f.noteAt("6", "contracts workflow run failed for main branch", "example-org/example-repo", base.Add(time.Hour+47*time.Minute))
	f.noteAt("5", "contracts workflow run failed for main branch", "example-org/example-repo", base.Add(time.Hour))
	f.noteAt("4", "contracts workflow run failed for main branch", "example-org/example-repo", base)
	c := f.server(t)
	const thread = "ci:example-org/example-repo@main"
	newest := base.Add(2*time.Hour + time.Second)
	for poll := range 4 {
		c.inbox.fullAt = time.Time{}
		if _, _, err := c.poll(context.Background()); err != nil {
			t.Fatal(err)
		}
		th := things(t, c)[thread]
		if !th.At.Equal(newest) || th.Last != newest.UTC().Format(time.RFC3339) || th.Preview != "android workflow run failed for main branch" {
			t.Fatalf("poll %d: the thing went back to %v (%s)", poll, th.At, th.Preview)
		}
	}

	// Later failures from another account's pushes: no notification; the follow-up finds them in the branch's runs.
	settled, fresh := time.Now().Add(-time.Hour).UTC().Truncate(time.Second), time.Now().Add(-30*time.Second).UTC().Truncate(time.Second)
	run := func(name, status, conclusion string, at time.Time) string {
		return `{"name":"` + name + `","status":"` + status + `","conclusion":"` + conclusion + `","updated_at":"` + at.Format(time.RFC3339) + `"}`
	}
	f.mu.Lock()
	f.runs = map[string]string{"main": "[" + run("contracts", "completed", "failure", settled.Add(-time.Minute)) + "," +
		run("android", "completed", "failure", settled) + "," + run("contracts", "completed", "success", base) + "]"}
	f.mu.Unlock()
	if !c.followUpCI(context.Background()) {
		t.Fatal("a newer failed run did not move the thing")
	}
	th := things(t, c)[thread]
	if th.Done != "" || !th.At.Equal(settled) || th.Last != settled.Format(time.RFC3339) || th.Preview != "android workflow run failed for main branch" ||
		!strings.Contains(th.Context, "android workflow run failed for main branch") {
		t.Fatalf("the newer failure did not join: %+v", th)
	}

	// A run that ended moments ago waits: its notification, if it is the user's, is the same failure.
	f.mu.Lock()
	f.runs["main"] = "[" + run("apple", "completed", "failure", fresh) + "," + f.runs["main"][1:]
	f.mu.Unlock()
	if c.followUpCI(context.Background()) {
		t.Fatalf("a run that just ended moved the thing: %+v", things(t, c)[thread])
	}
	// An older notification read after the fold leaves the thing where it is.
	f.noteAt("3", "contracts workflow run failed for main branch", "example-org/example-repo", settled.Add(-2*time.Minute))
	c.inbox.fullAt = time.Time{}
	c.poll(context.Background())
	if th := things(t, c)[thread]; !th.At.Equal(settled) {
		t.Fatalf("an older notification wound the thing back to %v", th.At)
	}
}

// The options: what comes in, checked, given back with defaults; what the hub still has open under older rules or
// a muted repository is settled.
func TestInboxOptionsAndSettle(t *testing.T) {
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", nil)
	var o InboxOptions
	_ = json.Unmarshal(c.Options(), &o)
	if o.CI != CIMine || len(o.IgnoreBranches) != 1 || o.IgnoreBranches[0] != "train/*" {
		t.Fatalf("defaults %s", c.Options())
	}
	for _, bad := range []string{`{"ci":"sometimes"}`, `{"muted_repos":["../x"]}`, `{"ignore_branches":["["]}`} {
		if err := c.SetOptions(json.RawMessage(bad)); err == nil {
			t.Errorf("%s was taken", bad)
		}
	}
	if err := c.SetOptions(json.RawMessage(`{"ci":"off","muted_repos":["example-org/noisy"]}`)); err != nil {
		t.Fatal(err)
	}
	over := c.Settle(context.Background(), []string{"notification:9", "ci:example-org/example-repo@train/1", "ci:example-org/example-repo@main",
		"example-org/noisy#3", "example-org/example-repo#12"})
	want := map[string]string{
		"notification:9":                      "GitHub 降噪：CI 失败改为按分支合并成一条",
		"ci:example-org/example-repo@train/1": "GitHub：自动分支的 CI 不提醒",
		"ci:example-org/example-repo@main":    "GitHub：CI 失败已设为不提醒",
		"example-org/noisy#3":                 "GitHub：仓库已静音",
	}
	if len(over) != len(want) {
		t.Fatalf("settled %v", over)
	}
	for k, v := range want {
		if over[k] != v {
			t.Errorf("%s: %q, want %q", k, over[k], v)
		}
	}
}

// People's asks need me; bots' are FYI; what the rules cannot tell is the model's.
func TestTierOfPeopleAndBots(t *testing.T) {
	alice, bot := user{Login: "alice"}, user{Login: "ci-bot[bot]", Type: "Bot"}
	for _, tc := range []struct {
		reason string
		author user
		moves  []move
		tier   string
	}{
		{"review_requested", alice, nil, "action"},
		{"review_requested", user{Login: "dependabot[bot]"}, nil, "fyi"},
		{"mention", alice, []move{{who: "bob"}}, "action"},
		{"mention", alice, []move{{who: "ci-bot[bot] (bot)", bot: true}}, "fyi"},
		{"assign", bot, nil, "action"},
		{"team_mention", alice, nil, "fyi"},
		{"author", alice, []move{{who: "bob", state: "CHANGES_REQUESTED"}}, "action"},
		{"author", alice, []move{{who: "bob", state: "APPROVED"}}, "fyi"},
		{"author", alice, []move{{who: "ci-bot[bot] (bot)", bot: true}}, "fyi"},
		{"author", alice, []move{{who: "bob"}}, ""},
	} {
		if tier, _ := tierOf(tc.reason, tc.author, tc.moves); string(tier) != tc.tier {
			t.Errorf("%s %+v: %q, want %q", tc.reason, tc.moves, tier, tc.tier)
		}
	}
}

func keys(m map[string]connector.InboxThing) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
