package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseImage(t *testing.T) {
	for _, c := range []struct {
		in, registry, repo, tag, name string
		pinned                        bool
	}{
		{"ghcr.io/Owner/Persona:latest", "ghcr.io", "owner/persona", "latest", "ghcr.io/owner/persona", false},
		{"ghcr.io/owner/persona", "ghcr.io", "owner/persona", "latest", "ghcr.io/owner/persona", false},
		{"ghcr.io/owner/persona:1.2.0", "ghcr.io", "owner/persona", "1.2.0", "ghcr.io/owner/persona", true},
		{"ghcr.io/owner/persona:edge@sha256:0123", "ghcr.io", "owner/persona", "edge", "ghcr.io/owner/persona", false},
		{"localhost:5000/owner/persona:v1.3.0-rc1", "localhost:5000", "owner/persona", "v1.3.0-rc1", "localhost:5000/owner/persona", true},
		{"me/persona:0.3.0", "registry-1.docker.io", "me/persona", "0.3.0", "me/persona", true},
		{"postgres", "registry-1.docker.io", "library/postgres", "latest", "postgres", false},
		{"docker.io/me/persona:0.3.0", "registry-1.docker.io", "me/persona", "0.3.0", "me/persona", true},
		{"docker.io/postgres", "registry-1.docker.io", "library/postgres", "latest", "postgres", false},
		{"docker.io/library/postgres:1.2.0", "registry-1.docker.io", "library/postgres", "1.2.0", "postgres", true},
		{"registry-1.docker.io/postgres", "registry-1.docker.io", "postgres", "latest", "postgres", false},
		{"ghcr.io/postgres", "ghcr.io", "postgres", "latest", "ghcr.io/postgres", false},
	} {
		im, err := ParseImage(c.in)
		if err != nil || im.Registry != c.registry || im.Repo != c.repo || im.Tag != c.tag || im.Name() != c.name || im.Pinned() != c.pinned {
			t.Errorf("%s: %+v name %s pinned %v, %v", c.in, im, im.Name(), im.Pinned(), err)
		}
	}
	for _, bad := range []string{"", "  ", "ghcr.io/owner/persona:", "a b", "docker.io/"} {
		if _, err := ParseImage(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

type imageRoundTripper func(*http.Request) (*http.Response, error)

func (f imageRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLatestVersionRegistrySchemes(t *testing.T) {
	for _, tc := range []struct{ registry, scheme string }{
		{"localhost:5000", "http"},
		{"127.0.0.1:5000", "http"},
		{"[::1]:5000", "http"},
		{"[0:0:0:0:0:0:0:1]:5000", "http"},
		{"[::1]", "http"},
		{"[2001:db8::1]:5000", "https"},
		{"registry.example:5000", "https"},
		{"localhost.example:5000", "https"},
		{"127.0.0.2:5000", "https"},
	} {
		t.Run(tc.registry, func(t *testing.T) {
			im, err := ParseImage(tc.registry + "/owner/persona:latest")
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			client := &http.Client{Transport: imageRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.Scheme != tc.scheme || r.URL.Host != tc.registry ||
					r.URL.Path != "/v2/owner/persona/tags/list" || r.URL.Query().Get("n") != "1000" {
					t.Errorf("unexpected tags request: %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tags":["1.2.0","1.3.0"]}`))}, nil
			})}
			got, err := LatestVersion(context.Background(), client, im, "")
			if err != nil || got != "1.3.0" || calls != 1 {
				t.Fatalf("latest %q, error %v, requests %d", got, err, calls)
			}
		})
	}
}

func TestLatestVersionIPv6LoopbackRegistry(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/owner/persona/tags/list" || r.URL.Query().Get("n") != "1000" {
			http.Error(w, "unexpected tags request", http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"tags":["1.2.0","1.3.0"]}`)
	}))
	srv.Listener.Close()
	srv.Listener = listener
	srv.Start()
	t.Cleanup(srv.Close)
	im, err := ParseImage(strings.TrimPrefix(srv.URL, "http://") + "/owner/persona:latest")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := LatestVersion(ctx, srv.Client(), im, "")
	if err != nil || got != "1.3.0" {
		t.Fatalf("latest %q, error %v", got, err)
	}
}

func TestLatestVersionExplicitDockerHub(t *testing.T) {
	im, err := ParseImage("docker.io/postgres")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := &http.Client{Transport: imageRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Host != "registry-1.docker.io" || r.URL.Path != "/v2/library/postgres/tags/list" || r.URL.Query().Get("n") != "1000" {
			t.Errorf("unexpected tags request: %s %s", r.Method, r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"tags":["1.2.0","1.3.0"]}`))}, nil
	})}
	got, err := LatestVersion(context.Background(), client, im, "")
	if err != nil || got != "1.3.0" || calls != 1 {
		t.Fatalf("latest %q, error %v, requests %d", got, err, calls)
	}
}

func TestNewerVersion(t *testing.T) {
	for _, c := range []struct {
		latest, running string
		newer           bool
	}{
		{"0.4.0", "0.3.0", true},
		{"0.10.0", "0.9.0", true},
		{"0.3.0", "0.3.0", false},
		{"0.3.0", "0.4.0", false},
		{"0.4.0", "0.4.0-rc1", true},
		{"0.4.0-rc2", "0.4.0-rc1", true},
		{"0.4.0", "v0.3.0", true},
		{"0.4.0", "v0.3.0-12-gabc1234-202611021500", false}, // a branch build: cannot tell
		{"0.4.0", "dev", false},
		{"edge", "0.3.0", false},
	} {
		if got := NewerVersion(c.latest, c.running); got != c.newer {
			t.Errorf("NewerVersion(%s, %s) = %v", c.latest, c.running, got)
		}
	}
}

// registry is a fake registry with ghcr.io's bearer flow: tags on two pages, a pull token from /token (anonymous
// unless private, then only for the password "secret").
func registry(t *testing.T, private bool) *httptest.Server {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.URL.Query().Get("scope") != "repository:owner/persona:pull" || r.URL.Query().Get("service") != "fake" {
				http.Error(w, "bad scope", http.StatusBadRequest)
				return
			}
			if _, pass, _ := r.BasicAuth(); private && pass != "secret" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			fmt.Fprint(w, `{"token":"pull-token"}`)
		case "/v2/owner/persona/tags/list":
			if r.Header.Get("Authorization") != "Bearer pull-token" {
				w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="fake",scope="repository:owner/persona:pull"`, srv.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("last") == "" {
				w.Header().Set("Link", `</v2/owner/persona/tags/list?last=latest&n=1000>; rel="next"`)
				fmt.Fprint(w, `{"name":"owner/persona","tags":["0.2.0","0.9.0","latest"]}`)
				return
			}
			fmt.Fprint(w, `{"name":"owner/persona","tags":["0.10.0","0.11.0-rc1","edge","sha-abc1234","v0.9.5"]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLatestVersion(t *testing.T) {
	ctx := context.Background()
	srv := registry(t, false)
	im, _ := ParseImage(strings.TrimPrefix(srv.URL, "http://") + "/owner/persona:latest")
	got, err := LatestVersion(ctx, srv.Client(), im, "")
	if err != nil || got != "0.10.0" {
		t.Fatalf("latest %q, %v (want 0.10.0: both pages, no pre-release, edge or sha tags)", got, err)
	}

	priv := registry(t, true)
	im, _ = ParseImage(strings.TrimPrefix(priv.URL, "http://") + "/owner/persona")
	if _, err := LatestVersion(ctx, priv.Client(), im, ""); !errors.Is(err, ErrPrivate) {
		t.Fatalf("a private image without a token: %v", err)
	}
	if _, err := LatestVersion(ctx, priv.Client(), im, "wrong"); !errors.Is(err, ErrPrivate) {
		t.Fatalf("a private image with a wrong token: %v", err)
	}
	if got, err := LatestVersion(ctx, priv.Client(), im, "secret"); err != nil || got != "0.10.0" {
		t.Fatalf("a private image with its token: %q, %v", got, err)
	}

	im, _ = ParseImage(strings.TrimPrefix(srv.URL, "http://") + "/owner/gone")
	if _, err := LatestVersion(ctx, srv.Client(), im, ""); err == nil || !strings.Contains(err.Error(), "没有") {
		t.Fatalf("an image the registry does not have: %v", err)
	}
}
