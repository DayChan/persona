package extension

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// rateLimitedAPI answers every request with GitHub's 403 for an hour used up.
func rateLimitedAPI(t *testing.T, calls *atomic.Int32) *httptest.Server {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fileHost serves files by path; anything else is a 404. It also counts what it was asked and whether a token came.
type fileHost struct {
	srv    *httptest.Server
	files  map[string][]byte
	asked  []string
	tokens atomic.Int32
}

func newFileHost(t *testing.T, files map[string][]byte) *fileHost {
	f := &fileHost{files: files}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.asked = append(f.asked, r.URL.Path)
		if r.Header.Get("Authorization") != "" {
			f.tokens.Add(1)
		}
		b, ok := f.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func signedIndexFiles(t *testing.T, extra map[string]any) ([]byte, []byte) {
	key, sign := testKey(t)
	ix := map[string]any{"market_version": 1, "name": "Example", "signing_key": key, "extensions": []any{}}
	for k, v := range extra {
		ix[k] = v
	}
	raw, _ := json.Marshal(ix)
	return sign(raw), nil
}

// A public GitHub market without a token that the API rate-limits is read from raw.githubusercontent.com: the same
// index and signature, nothing sent there but the request.
func TestGitHubRateLimitFallsBackToRaw(t *testing.T) {
	var apiCalls atomic.Int32
	api := rateLimitedAPI(t, &apiCalls)
	raw, _ := signedIndexFiles(t, nil)
	host := newFileHost(t, map[string][]byte{
		"/example/market/HEAD/" + IndexFile: raw,
	})
	s, _ := ParseMarket("github:example/market")
	mk := &Market{Spec: s, Client: host.srv.Client(), GitHubAPI: api.URL, GitHubRaw: host.srv.URL}
	mk.Client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = true // two test servers, one client
	f, err := mk.FetchSigned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(f.Raw) != string(raw) || f.Index.Name != "Example" {
		t.Fatalf("read %s", f.Raw)
	}
	if apiCalls.Load() == 0 {
		t.Fatal("the API was not asked first")
	}
	// A branch is its path on raw.
	host.asked = nil
	s, _ = ParseMarket("github:example/market@dev")
	host.files["/example/market/dev/"+IndexFile] = raw
	mk = &Market{Spec: s, Client: mk.Client, GitHubAPI: api.URL, GitHubRaw: host.srv.URL}
	if _, err := mk.FetchSigned(context.Background()); err != nil || host.asked[0] != "/example/market/dev/"+IndexFile {
		t.Fatalf("branch: %v %q", err, host.asked)
	}
}

// What the fallback does not change: a market with a token stays on the API (its limit is the token's, and the
// token goes nowhere else); a private repository, not on raw, still says it is rate-limited; a test's API never
// reaches the real raw host.
func TestGitHubRateLimitFallbackLimits(t *testing.T) {
	var apiCalls atomic.Int32
	api := rateLimitedAPI(t, &apiCalls)
	raw, _ := signedIndexFiles(t, nil)
	host := newFileHost(t, map[string][]byte{
		"/example/market/HEAD/" + IndexFile: raw,
	})
	s, _ := ParseMarket("github:example/market")
	client := host.srv.Client()
	client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = true

	mk := &Market{Spec: s, Token: "secret", Client: client, GitHubAPI: api.URL, GitHubRaw: host.srv.URL}
	if _, err := mk.FetchSigned(context.Background()); !errors.Is(err, ErrRateLimited) || len(host.asked) != 0 || host.tokens.Load() != 0 {
		t.Fatalf("with a token: %v, raw asked %q", err, host.asked)
	}
	s, _ = ParseMarket("github:example/private")
	mk = &Market{Spec: s, Client: client, GitHubAPI: api.URL, GitHubRaw: host.srv.URL}
	if _, err := mk.FetchSigned(context.Background()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("private repository: %v", err)
	}
	if raw, _, ok := (&Market{Spec: s, GitHubAPI: api.URL}).githubFallback(); ok {
		t.Fatalf("a test API falls back to %q", raw)
	}
	if raw, web, ok := (&Market{Spec: s}).githubFallback(); !ok || raw != "https://raw.githubusercontent.com" || web != "https://github.com" {
		t.Fatalf("real hosts: %q %q %v", raw, web, ok)
	}
	https, _ := ParseMarket("https://example.com/extensions/")
	if _, _, ok := (&Market{Spec: https}).githubFallback(); ok {
		t.Fatal("an https market falls back")
	}
}

// A raw-host response is one envelope: no detached signature race exists.
func TestGitHubRawSingleDocument(t *testing.T) {
	var apiCalls atomic.Int32
	api := rateLimitedAPI(t, &apiCalls)
	raw, _ := signedIndexFiles(t, nil)
	host := newFileHost(t, map[string][]byte{"/example/market/HEAD/" + IndexFile: raw})
	spec, _ := ParseMarket("github:example/market")
	client := host.srv.Client()
	client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = true
	mk := &Market{Spec: spec, Client: client, GitHubAPI: api.URL, GitHubRaw: host.srv.URL}
	if _, err := mk.FetchSigned(context.Background()); err != nil || len(host.asked) != 1 {
		t.Fatalf("single document: %v %v", err, host.asked)
	}
}

// Packages come along: a relative path from raw, a release asset from github.com's download link; both still
// checked against the index's sha256.
func TestGitHubRateLimitedDownloads(t *testing.T) {
	var apiCalls atomic.Int32
	api := rateLimitedAPI(t, &apiCalls)
	pkg := []byte("package bytes")
	sum := sha256.Sum256(pkg)
	host := newFileHost(t, map[string][]byte{
		"/example/market/HEAD/dist/x.tar.gz":                pkg,
		"/example/market/releases/download/v1.0.0/x.tar.gz": pkg,
	})
	s, _ := ParseMarket("github:example/market")
	client := host.srv.Client()
	client.Transport.(*http.Transport).TLSClientConfig.InsecureSkipVerify = true
	mk := &Market{Spec: s, Client: client, GitHubAPI: api.URL, GitHubRaw: host.srv.URL, GitHubWeb: host.srv.URL}
	for _, ref := range []string{"dist/x.tar.gz", "release:v1.0.0/x.tar.gz"} {
		file, _, err := mk.Download(context.Background(), &MarketPackage{URL: ref, SHA256: hex.EncodeToString(sum[:])}, filepath.Join(t.TempDir(), "d"))
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if b, _ := os.ReadFile(file); string(b) != string(pkg) {
			t.Fatalf("%s: %q", ref, b)
		}
	}
	other := sha256.Sum256([]byte("something else"))
	if _, _, err := mk.Download(context.Background(), &MarketPackage{URL: "dist/x.tar.gz", SHA256: hex.EncodeToString(other[:])}, filepath.Join(t.TempDir(), "d")); !errors.Is(err, ErrChecksum) {
		t.Fatalf("a package from raw is not checked: %v", err)
	}
}
