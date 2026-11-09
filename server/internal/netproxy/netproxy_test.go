package netproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:8080", " http://user:pass@proxy.example:3128 ", "http://[::1]:8080/", "http://proxy.example"} {
		if u, err := Parse(ok); err != nil || u == nil {
			t.Errorf("%q: %v %v", ok, u, err)
		}
	}
	if u, err := Parse("  "); u != nil || err != nil {
		t.Errorf("blank: %v %v", u, err)
	}
	// A left-out port is 80 in the URL itself, not only in net/http: mail dials u.Host for its CONNECT tunnels.
	for raw, host := range map[string]string{"http://proxy.example": "proxy.example:80", "http://user:pass@proxy.example/": "proxy.example:80",
		"http://[::1]": "[::1]:80", "http://proxy.example:3128": "proxy.example:3128"} {
		if u, err := Parse(raw); err != nil || u.Host != host || (u.User != nil) != strings.Contains(raw, "@") {
			t.Errorf("%q: %v %v, want host %q", raw, u, err, host)
		}
	}
	for _, bad := range []string{"socks5://127.0.0.1:1080", "127.0.0.1:8080", "http://", "http://h:1/x", "http://h:1?a=b", "https://h:1",
		"http://:8080", "http://proxy.example:", "http://proxy.example:0", "http://proxy.example:99999", "http://[::1]:", "http://h:1#x"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// No part of a password shows in the error, also when a '/', '?', '#' or bad escape in it makes url.Parse read part of it as
	// the port, path, query or fragment; the error still names the proxy.
	for _, c := range []struct{ raw, secret string }{
		{"socks5://user:s3cret@proxy.example:1", "s3cret"},
		{"http://user:s3cret@proxy.example:1/x", "s3cret"},
		{"http://user:s3cret@proxy.example:%zz", "s3cret"},
		{"user:s3cret@proxy.example:8080", "s3cret"},
		{"http://user:4242/ss@proxy.example:8080", "4242"},
		{"http://user:4242?ss@proxy.example:8080", "4242"},
		{"http://user:4242#ss@proxy.example:8080", "4242"},
		{"http://user:s3cr?t@proxy.example:8080", "s3cr"},
		{"http://user:s3cr#t@proxy.example:8080", "s3cr"},
		{"http://user:s3cr/t@proxy.example:8080", "s3cr"},
		{"http://user:ab%zz@proxy.example:8080", "%zz"},
	} {
		if _, err := Parse(c.raw); err == nil || strings.Contains(err.Error(), c.secret) || !strings.Contains(err.Error(), "proxy.example") {
			t.Errorf("%q: %v", c.raw, err)
		}
	}
}

// With a proxy every request (http and https alike) goes to the proxy; without one it goes straight to the server,
// even when the environment names a proxy.
func TestHTTPClient(t *testing.T) {
	var proxied atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "" { // not a proxy request: a node's port probe (GET /, node/ports.go) asks this server directly
			http.NotFound(w, r)
			return
		}
		proxied.Add(1)
		if r.Method == http.MethodConnect {
			w.WriteHeader(http.StatusForbidden) // enough to know the client came here; no tunnel needed
			return
		}
		_, _ = io.WriteString(w, "via proxy "+r.URL.String())
	}))
	t.Cleanup(proxy.Close)
	var direct atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/origin" { // only this test's request counts: a node's port probe (node/ports.go) asks GET /
			http.NotFound(w, r)
			return
		}
		direct.Add(1)
		_, _ = io.WriteString(w, "direct")
	}))
	t.Cleanup(origin.Close)

	u, err := Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := HTTPClient(u, 5*time.Second)
	resp, err := c.Get("http://github.example/login/device/code")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "via proxy http://github.example/login/device/code" {
		t.Fatalf("http through the proxy: %q", body)
	}
	if _, err := c.Get("https://api.github.example/user"); err == nil {
		t.Fatal("https got through a proxy that refuses CONNECT")
	}
	if proxied.Load() != 2 || direct.Load() != 0 {
		t.Fatalf("proxied %d, direct %d", proxied.Load(), direct.Load())
	}

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	c = HTTPClient(nil, 5*time.Second)
	resp, err = c.Get(origin.URL + "/origin")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "direct" || proxied.Load() != 2 || direct.Load() != 1 {
		t.Fatalf("no proxy: %q, proxied %d, direct %d", body, proxied.Load(), direct.Load())
	}
	if c.Timeout != 5*time.Second {
		t.Fatalf("timeout %v", c.Timeout)
	}
}
