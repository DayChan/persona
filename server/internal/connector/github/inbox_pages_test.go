package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// pagedNotifications is GitHub's unread notifications served a page at a time (per_page, page), newest first, with
// the issue the known thing is about, on made-up data.
type pagedNotifications struct {
	mu          sync.Mutex
	list        []string // notification JSON objects, newest first
	failPage    int      // the page answered with a server error
	limited     string   // the page ("page=N") or path answered with a secondary rate limit (429, Retry-After 600)
	details     []string // the requests for anything but notifications
	notModified bool     // answer 304 when If-Modified-Since comes
	pages       []int    // the page of every notifications request
	since       []string // its If-Modified-Since
}

func (f *pagedNotifications) server(t *testing.T) *Connector {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4000")
		if r.URL.Path != "/notifications" && r.URL.Path != "/user" {
			f.details = append(f.details, r.URL.Path)
		}
		if f.limited != "" && (f.limited == r.URL.Path || r.URL.Path == "/notifications" && f.limited == "page="+r.URL.Query().Get("page")) {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit."}`))
			return
		}
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"login":"me"}`))
		case "/notifications":
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
			page, per = max(page, 1), max(per, 1)
			f.pages, f.since = append(f.pages, page), append(f.since, r.Header.Get("If-Modified-Since"))
			switch {
			case f.notModified && r.Header.Get("If-Modified-Since") != "":
				w.WriteHeader(http.StatusNotModified)
				return
			case page == f.failPage:
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"message":"Server Error"}`))
				return
			}
			from, to := min(len(f.list), (page-1)*per), min(len(f.list), page*per)
			w.Header().Set("Last-Modified", "Mon, 02 Nov 2026 10:00:00 GMT")
			_, _ = w.Write([]byte("[" + strings.Join(f.list[from:to], ",") + "]"))
		case api7:
			_, _ = w.Write([]byte(`{"title":"Example bug","state":"open","user":{"login":"bob"},"body":"Something broke",` +
				`"html_url":"https://github.com/example-org/example-repo/issues/7","comments":0}`))
		case api8:
			_, _ = w.Write([]byte(`{"title":"Another bug","state":"open","user":{"login":"bob"},"body":"Broke again",` +
				`"html_url":"https://github.com/example-org/example-repo/issues/8","comments":0}`))
		case api7 + "/comments", api8 + "/comments":
			_, _ = w.Write([]byte(`[]`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	c.check(context.Background())
	return c
}

const api8 = "/repos/example-org/example-repo/issues/8"

var issue7 = note("7", "assign", "Issue", "https://api.github.com"+api7, t1)

// older is n unread notifications the inbox skips (subscribed), all older than issue7.
func older(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = note(strconv.Itoa(500+i), "subscribed", "Issue", fmt.Sprintf("https://api.github.com/repos/example-org/example-repo/issues/%d", 500+i),
			"2026-11-02T08:00:00Z")
	}
	return out
}

// newer is n unread notifications the inbox skips (subscribed), all newer than issue7.
func newer(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = note(strconv.Itoa(100+i), "subscribed", "Issue", fmt.Sprintf("https://api.github.com/repos/example-org/example-repo/issues/%d", 100+i), t2)
	}
	return out
}

// knownIssue is a connector that has read the unread assignment example-org/example-repo#7, with the fake's
// request log cleared and list set for the next poll.
func knownIssue(t *testing.T, f *pagedNotifications, list []string) *Connector {
	f.list = []string{issue7}
	c := f.server(t)
	if _, moved, err := c.poll(context.Background()); err != nil || !moved {
		t.Fatalf("first poll: moved %v, %v", moved, err)
	}
	if th, ok := things(t, c)["example-org/example-repo#7"]; !ok || th.Read {
		t.Fatalf("first poll: %+v", things(t, c))
	}
	f.mu.Lock()
	f.list, f.pages, f.since = list, nil, nil
	f.mu.Unlock()
	return c
}

// A thing is read elsewhere only when it is missing from the whole unread list, not just from its first page.
func TestNotificationPages(t *testing.T) {
	for _, tc := range []struct {
		name     string
		list     []string
		failPage int
		pages    int  // notifications pages the poll reads
		read     bool // #7 marked read elsewhere
	}{
		{"still unread on page 2", append(newer(notificationsPage), issue7), 0, 2, false},
		{"more unread than the pages read", append(newer(maxNotificationPages*notificationsPage), issue7), 0, maxNotificationPages, false},
		{"a later page fails", append(newer(notificationsPage), issue7), 2, 2, false},
		{"gone from every page", newer(notificationsPage + 3), 0, 2, true},
		// The list comes newest first: a thing that moved after its oldest notification read would have been in it.
		{"gone, newer than every page read", older(maxNotificationPages*notificationsPage + 1), 0, maxNotificationPages, true},
		{"gone, newer than page 1, page 2 fails", older(notificationsPage + 3), 2, 2, true},
		{"gone, older than every page read", newer(maxNotificationPages*notificationsPage + 1), 0, maxNotificationPages, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &pagedNotifications{failPage: tc.failPage}
			c := knownIssue(t, f, tc.list)
			if _, _, err := c.poll(context.Background()); err != nil {
				t.Fatalf("poll: %v", err)
			}
			if th, ok := things(t, c)["example-org/example-repo#7"]; !ok || th.Read != tc.read {
				t.Errorf("#7: in the inbox %v, read %v, want read %v", ok, th.Read, tc.read)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.pages) != tc.pages {
				t.Errorf("pages read: %v, want %d", f.pages, tc.pages)
			}
			for i, page := range f.pages {
				if page != i+1 || (f.since[i] != "") != (i == 0) { // only the first page asks If-Modified-Since
					t.Errorf("pages %v, If-Modified-Since %q", f.pages, f.since)
					break
				}
			}
		})
	}
}

// A 304 on the first page means nothing changed: no other page is read.
func TestNotificationPagesNotModified(t *testing.T) {
	f := &pagedNotifications{notModified: true}
	c := knownIssue(t, f, append(newer(notificationsPage), issue7))
	_, moved, err := c.poll(context.Background())
	f.mu.Lock()
	defer f.mu.Unlock()
	if err != nil || moved || len(f.pages) != 1 || f.since[0] == "" {
		t.Fatalf("moved %v, %v, pages %v, If-Modified-Since %q", moved, err, f.pages, f.since)
	}
}

// A rate limit on a later page or on a thread's details pauses the rest of the poll: no more details are read until
// GitHub allows it, and the next poll sends nothing.
func TestNotificationsRateLimitMidPoll(t *testing.T) {
	issue8 := note("8", "assign", "Issue", "https://api.github.com"+api8, t1)
	newer7 := note("7", "assign", "Issue", "https://api.github.com"+api7, t2)
	for _, tc := range []struct {
		name, limited string
		list          []string
		details       []string // the detail requests sent
	}{
		{"page 2", "page=2", append(newer(notificationsPage-1), issue7), nil},
		{"a thread's details", api7, []string{newer7, issue8}, []string{api7}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &pagedNotifications{list: tc.list, limited: tc.limited}
			c := f.server(t)
			if _, _, err := c.poll(context.Background()); err != nil {
				t.Fatalf("poll: %v", err)
			}
			if left := c.pauseLeft(); left < 590*time.Second || !c.ratesLow() {
				t.Errorf("paused for %s, want Retry-After's 600 s", left)
			}
			f.mu.Lock()
			if fmt.Sprint(f.details) != fmt.Sprint(tc.details) {
				t.Errorf("details read: %v, want %v", f.details, tc.details)
			}
			pages := len(f.pages)
			f.mu.Unlock()
			if wait, _, err := c.poll(context.Background()); err != nil || wait < 590*time.Second {
				t.Errorf("next poll: wait %s, %v", wait, err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.pages) != pages {
				t.Errorf("the next poll read notifications during the pause: pages %v", f.pages)
			}
		})
	}
}
