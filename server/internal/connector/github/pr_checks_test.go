package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPRChecksPagesAndStatuses(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.RequestURI())
		switch r.URL.Path {
		case "/repos/example/repo/pulls/12":
			fmt.Fprint(w, `{"head":{"sha":"abc"}}`)
		case "/repos/example/repo/commits/abc/check-runs":
			runs := []map[string]any{}
			if r.URL.Query().Get("page") == "1" {
				for i := 0; i < 100; i++ {
					runs = append(runs, map[string]any{"id": i + 1, "name": fmt.Sprintf("check-%d", i), "status": "completed", "conclusion": "success"})
				}
			} else {
				runs = append(runs, map[string]any{"id": 101, "name": "lint", "status": "completed", "conclusion": "failure"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 101, "check_runs": runs})
		case "/repos/example/repo/commits/abc/status":
			fmt.Fprint(w, `{"total_count":1,"statuses":[{"context":"preview","state":"pending"}]}`)
		default:
			t.Errorf("unexpected request %s", r.URL.RequestURI())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := &machine{tokens: []string{oldToken}}
	c := New(Config{Auth: AuthConfig{FromMachine: "box"}, APIRoot: server.URL}, "box", m.fetch)
	result, err := c.PRChecks(context.Background(), "example/repo", 12)
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 102 || !result.Pending || len(result.Failures) != 1 || result.Failures[0] != "lint" {
		t.Fatalf("result %+v", result)
	}
	if len(calls) != 5 {
		t.Fatalf("requests %v", calls)
	}
}

func TestPRChecksRejectsChangingHeadAndTruncation(t *testing.T) {
	for _, scenario := range []string{"head", "truncated"} {
		t.Run(scenario, func(t *testing.T) {
			heads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/example/repo/pulls/12":
					heads++
					if scenario == "head" && heads > 1 {
						fmt.Fprint(w, `{"head":{"sha":"new"}}`)
					} else {
						fmt.Fprint(w, `{"head":{"sha":"abc"}}`)
					}
				case "/repos/example/repo/commits/abc/check-runs":
					if scenario == "truncated" {
						fmt.Fprint(w, `{"total_count":101,"check_runs":[]}`)
					} else {
						fmt.Fprint(w, `{"total_count":0,"check_runs":[]}`)
					}
				case "/repos/example/repo/commits/abc/status":
					fmt.Fprint(w, `{"total_count":0,"statuses":[]}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			m := &machine{tokens: []string{oldToken}}
			c := New(Config{Auth: AuthConfig{FromMachine: "box"}, APIRoot: server.URL}, "box", m.fetch)
			if _, err := c.PRChecks(context.Background(), "example/repo", 12); err == nil {
				t.Fatal("accepted incomplete snapshot")
			}
		})
	}
}

func TestPRChecksHonorsSecondaryRateLimit(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Retry-After", "180")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"secondary rate limit"}`)
	}))
	defer server.Close()
	m := &machine{tokens: []string{oldToken}}
	c := New(Config{Auth: AuthConfig{FromMachine: "box"}, APIRoot: server.URL}, "box", m.fetch)
	if _, err := c.PRChecks(context.Background(), "example/repo", 12); err == nil {
		t.Fatal("expected rate limit")
	}
	if c.PRChecksRateReset().Before(time.Now().Add(2 * time.Minute)) {
		t.Fatal("retry too early")
	}
	_, _ = c.PRChecks(context.Background(), "example/repo", 12)
	if requests != 1 {
		t.Fatalf("sent %d requests while rate limited", requests)
	}
}

func TestPRChecksRejectsOver1000Runs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/example/repo/pulls/12":
			fmt.Fprint(w, `{"head":{"sha":"abc"}}`)
		case "/repos/example/repo/commits/abc/check-runs":
			fmt.Fprint(w, `{"total_count":1001,"check_runs":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	m := &machine{tokens: []string{oldToken}}
	c := New(Config{Auth: AuthConfig{FromMachine: "box"}, APIRoot: server.URL}, "box", m.fetch)
	if _, err := c.PRChecks(context.Background(), "example/repo", 12); err == nil {
		t.Fatal("accepted an incomplete capped check set")
	}
}
