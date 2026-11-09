package extmgr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/github"
)

func TestGitHubChecksUsesOnlyTheWatchedHost(t *testing.T) {
	for _, tc := range []struct {
		host    string
		allowed bool
	}{
		{"", true}, {"GITHUB.COM", true}, {"github.example.com", false},
	} {
		c := github.New(github.Config{Auth: github.AuthConfig{Host: tc.host}}, "", nil)
		if got := githubChecksPublicHost(c); got != tc.allowed {
			t.Errorf("host %q allowed %v, want %v", tc.host, got, tc.allowed)
		}
	}
}

func TestGitHubChecksDoesNotQueryOtherHost(t *testing.T) {
	var otherCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user" {
			_, _ = w.Write([]byte(`{"login":"tester"}`))
			return
		}
		otherCalls.Add(1)
		_, _ = w.Write([]byte(`{"head":{"sha":"abc"}}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := github.New(github.Config{Auth: github.AuthConfig{Host: "github.example.com", FromMachine: "box"}, APIRoot: server.URL}, "box", func(context.Context, string) (string, error) { return "fake-token", nil })
	go c.Run(ctx)
	waitLong(t, "GitHub connector sign-in", func() bool { return c.Status(ctx).State == connector.StateOK })
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	if err := h.connectors.Add(c); err != nil {
		t.Fatal(err)
	}
	_, _, available, err := h.PRChecks(ctx, "example/repo", 12)
	if err != nil || available || otherCalls.Load() != 0 {
		t.Fatalf("available %v, error %v, wrong-host calls %d", available, err, otherCalls.Load())
	}
}
