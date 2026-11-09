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

// quotaAnswer is the quota headers of one answer; an empty resource leaves X-RateLimit-Resource out.
func quotaAnswer(h http.Header, resource string, left, limit int, reset time.Time) {
	if resource != "" {
		h.Set("X-RateLimit-Resource", resource)
	}
	h.Set("X-RateLimit-Limit", strconv.Itoa(limit))
	h.Set("X-RateLimit-Remaining", strconv.Itoa(left))
	h.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
}

// fakeQuotaAPI is a GitHub whose core hour is nearly full (4900 of 5000 left) while the search API's minute has only
// searchLeft of 30 left; it records the paths asked.
func fakeQuotaAPI(t *testing.T, searchLeft int) (*Connector, func() []string) {
	var mu sync.Mutex
	var seen []string
	reset := time.Now().Add(time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		seen = append(seen, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/user":
			quotaAnswer(w.Header(), "core", 4900, 5000, reset)
			_, _ = w.Write([]byte(`{"login":"me"}`))
		case "/search/issues":
			quotaAnswer(w.Header(), "search", searchLeft, 30, reset)
			_, _ = w.Write([]byte(`{"total_count":0,"items":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", (&machine{tokens: []string{oldToken}}).fetch)
	c.api = srv.URL
	c.check(context.Background())
	return c, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// A search API minute running low leaves the hour's core quota alone: the status stays OK and the other reads go on.
func TestLowSearchQuotaIsNotTheCoreQuota(t *testing.T) {
	c, _ := fakeQuotaAPI(t, 2)
	if _, _, _, err := c.Changes(context.Background(), "", 20); err != nil {
		t.Fatal(err)
	}
	if c.ratesLow() {
		t.Error("a low search quota marked the core quota low")
	}
	if st := c.Status(context.Background()); st.State != connector.StateOK {
		t.Errorf("status %+v", st)
	}
}

// The archive waits while the search API's minute runs low, even after a core answer, and searches again once it resets.
func TestChangesWaitsForTheSearchQuota(t *testing.T) {
	c, seen := fakeQuotaAPI(t, 2)
	_, cursor, _, err := c.Changes(context.Background(), "", 20)
	if err != nil {
		t.Fatal(err)
	}
	c.check(context.Background()) // a core answer must not lift the search pause
	before := len(seen())
	batches, next, more, err := c.Changes(context.Background(), cursor, 20)
	if err != nil || batches != nil || next != cursor || more {
		t.Fatalf("low search quota: %v, %q, %v, %v", batches, next, more, err)
	}
	if got := seen()[before:]; len(got) != 0 {
		t.Fatalf("asked GitHub with the search quota low: %v", got)
	}

	c.mu.Lock()
	c.searchReset = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if _, _, _, err := c.Changes(context.Background(), cursor, 20); err != nil {
		t.Fatal(err)
	}
	if got := seen()[before:]; len(got) != 1 || got[0] != "/search/issues" {
		t.Fatalf("after the reset: %v", got)
	}
}

// The core quota is still tracked: a low core answer, or one without X-RateLimit-Resource (older or proxied answers),
// marks it low; quotas the connector does not track change nothing.
func TestNoteRateByResource(t *testing.T) {
	reset := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		resource           string
		left, limit        int
		coreLow, searchLow bool
	}{
		{"core", 100, 5000, true, false},
		{"", 100, 5000, true, false},
		{"core", 4900, 5000, false, false},
		{"search", 2, 30, false, true},
		{"graphql", 10, 5000, false, false},
	} {
		c := New(Config{Auth: AuthConfig{FromMachine: "mac"}}, "Mac", nil)
		h := http.Header{}
		quotaAnswer(h, tc.resource, tc.left, tc.limit, reset)
		c.noteRate(h)
		if c.ratesLow() != tc.coreLow || c.searchQuotaLow() != tc.searchLow {
			t.Errorf("%q %d/%d: core low %v, search low %v", tc.resource, tc.left, tc.limit, c.ratesLow(), c.searchQuotaLow())
		}
	}
}
