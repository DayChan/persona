package github

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// limitedNotifications is a GitHub whose /notifications answers with code, the headers set by header and message;
// it counts the notifications requests.
func limitedNotifications(t *testing.T, code int, header func(http.Header), message string) (*Connector, func() int) {
	var mu sync.Mutex
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_, _ = w.Write([]byte(`{"login":"me"}`))
		case "/notifications":
			mu.Lock()
			polls++
			mu.Unlock()
			header(w.Header())
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"message":"` + message + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	c.check(context.Background())
	return c, func() int {
		mu.Lock()
		defer mu.Unlock()
		return polls
	}
}

// A rate-limited notifications poll waits until GitHub allows it again instead of failing, records the pause for
// the connector's other reads and sends nothing while it lasts.
func TestRateLimitedPollWaitsItOut(t *testing.T) {
	cases := []struct {
		name     string
		code     int
		header   func(http.Header)
		message  string
		min, max time.Duration
	}{
		{"429 with Retry-After", http.StatusTooManyRequests, func(h http.Header) { h.Set("Retry-After", "120") },
			"", 119 * time.Second, 120 * time.Second},
		{"secondary 403 with Retry-After", http.StatusForbidden, func(h http.Header) { h.Set("Retry-After", "90") },
			"You have exceeded a secondary rate limit.", 89 * time.Second, 90 * time.Second},
		{"secondary 403 without Retry-After", http.StatusForbidden, func(h http.Header) { // the quota's reset is not the end
			h.Set("X-RateLimit-Limit", "5000")
			h.Set("X-RateLimit-Remaining", "4000")
			h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(50*time.Minute).Unix(), 10))
		}, "You have exceeded a secondary rate limit.", 59 * time.Second, 60 * time.Second},
		{"403 with the hour used up", http.StatusForbidden, func(h http.Header) {
			h.Set("X-RateLimit-Limit", "5000")
			h.Set("X-RateLimit-Remaining", "0")
			h.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(300*time.Second).Unix(), 10))
		}, "Forbidden", 298 * time.Second, 300 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, polls := limitedNotifications(t, tc.code, tc.header, tc.message)
			wait, moved, err := c.poll(context.Background())
			if err != nil || moved || wait < tc.min || wait > tc.max {
				t.Fatalf("poll: wait %s moved %v err %v; want a wait in [%s, %s] and no error", wait, moved, err, tc.min, tc.max)
			}
			if !c.ratesLow() {
				t.Error("the pause is not recorded for the other reads")
			}
			again, _, err := c.poll(context.Background())
			if err != nil || again <= 0 || again > wait {
				t.Errorf("poll during the pause: wait %s err %v", again, err)
			}
			if n := polls(); n != 1 {
				t.Errorf("%d notifications requests, want 1", n)
			}
		})
	}
}

// Watch keeps waiting through a rate limit (watchOne would restart it far sooner than GitHub allows).
func TestWatchWaitsOutARateLimit(t *testing.T) {
	retry := func(h http.Header) { h.Set("Retry-After", "120") }
	c, polls := limitedNotifications(t, http.StatusTooManyRequests, retry, "")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.Watch(ctx, func(connector.Scope) {}); err != nil {
		t.Fatalf("Watch stopped: %v", err)
	}
	if n := polls(); n != 1 {
		t.Errorf("%d notifications requests, want 1", n)
	}
}

// Failures that are not rate limits still end the poll with an error, as before.
func TestPollFailuresOtherThanRateLimits(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusForbidden} {
		c, _ := limitedNotifications(t, code, func(http.Header) {}, "Resource not accessible by integration")
		if wait, _, err := c.poll(context.Background()); err == nil || wait != 0 {
			t.Errorf("%d: wait %s err %v; want an error", code, wait, err)
		}
		if c.ratesLow() {
			t.Errorf("%d recorded a pause", code)
		}
	}
}
