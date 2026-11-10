package egress

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// connectStatus asks the proxy at purl (with its credential) for a CONNECT tunnel to target and says the status.
func connectStatus(t *testing.T, purl, target string) int {
	t.Helper()
	u, _ := url.Parse(purl)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	pw, _ := u.User.Password()
	auth := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pw))
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

// An address from an extension's settings lets through that host and port only: not a subdomain, not another port,
// not another host (where a redirect would lead), and plain http only when the target allows it (#5906).
func TestConfiguredTargetOnly(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	p := newProxy(t, ln.Addr().String(), nil)
	purl := p.Register("aster", Rule{Targets: []Target{{Host: "gateway.example.com", Port: "443"}}})
	for target, want := range map[string]int{
		"gateway.example.com:443":     http.StatusOK,
		"gateway.example.com:8443":    http.StatusForbidden,
		"api.gateway.example.com:443": http.StatusForbidden,
		"other.example.org:443":       http.StatusForbidden,
	} {
		if got := connectStatus(t, purl, target); got != want {
			t.Errorf("CONNECT %s: %d, want %d", target, got, want)
		}
	}
	resp, err := client(purl).Get("http://gateway.example.com:443/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("plain http to an https-only target: %d", resp.StatusCode)
	}

	// The settings changed: the old address is refused at once, the new one let through.
	p.SetTargets("aster", []Target{{Host: "gw2.example.com", Port: "443"}})
	if got := connectStatus(t, purl, "gateway.example.com:443"); got != http.StatusForbidden {
		t.Fatalf("the old address after a change: %d", got)
	}
	if got := connectStatus(t, purl, "gw2.example.com:443"); got != http.StatusOK {
		t.Fatalf("the new address: %d", got)
	}
}

// A target that allows http takes plain http requests to it, still that host and port only.
func TestConfiguredTargetHTTP(t *testing.T) {
	srv := newEcho(t)
	p := newProxy(t, srv, nil)
	purl := p.Register("aster", Rule{Targets: []Target{{Host: "gateway.example.com", Port: "80", HTTP: true}}})
	c := client(purl)
	for u, want := range map[string]int{"http://gateway.example.com/me": 200, "http://gateway.example.com:81/me": 403} {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", u, resp.StatusCode, want)
		}
	}
}

func newEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

// An address from the settings that resolves to this machine, a link-local or metadata address, or the hub's own
// listener is not dialled — one bad answer refuses the name; another address is reached.
func TestConfiguredTargetRefusesInside(t *testing.T) {
	var dialled []string
	metadata := net.IPv4(169, 254, 169, 254) // the cloud metadata address, link-local
	p := &Proxy{HubAddr: "127.0.0.1:18660",
		LocalAddrs: func() []net.IP { return []net.IP{net.ParseIP("198.51.100.5")} },
		Resolve: func(_ context.Context, name string) ([]net.IP, error) {
			switch name {
			case "loop.example.com":
				return []net.IP{net.ParseIP("127.0.0.2")}, nil
			case "meta.example.com":
				return []net.IP{metadata}, nil
			case "meta6.example.com":
				return []net.IP{net.ParseIP("fd00:ec2::254")}, nil
			case "mixed.example.com":
				return []net.IP{net.ParseIP("192.0.2.1"), net.ParseIP("127.0.0.1")}, nil
			case "self.example.com":
				return []net.IP{net.ParseIP("198.51.100.5")}, nil
			}
			return []net.IP{net.ParseIP("203.0.113.9")}, nil
		},
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			dialled = append(dialled, addr)
			a, b := net.Pipe()
			b.Close()
			return a, nil
		},
		Upstream: func(*url.URL) (*url.URL, error) { return nil, nil }}
	ctx := context.Background()
	for _, c := range []struct{ host, port string }{
		{"loop.example.com", "443"}, {"meta.example.com", "80"}, {"meta6.example.com", "443"}, {"mixed.example.com", "443"},
		{"self.example.com", "18660"}, {"127.0.0.1", "443"}, {metadata.String(), "80"},
	} {
		if _, err := p.dialConfigured(ctx, c.host, c.port); !errors.Is(err, errNotForSettings) {
			t.Errorf("%s:%s: %v", c.host, c.port, err)
		}
	}
	if len(dialled) != 0 {
		t.Fatalf("dialled %v", dialled)
	}
	for _, c := range []struct{ host, port, want string }{
		{"lan.example.com", "443", "203.0.113.9:443"}, {"self.example.com", "8443", "198.51.100.5:8443"},
	} {
		conn, err := p.dialConfigured(ctx, c.host, c.port)
		if err != nil {
			t.Fatalf("%s: %v", c.host, err)
		}
		conn.Close()
		if got := dialled[len(dialled)-1]; got != c.want {
			t.Fatalf("%s dialled %s, want %s (the address checked)", c.host, got, c.want)
		}
	}
	if !strings.Contains(errNotForSettings.Error(), "metadata") {
		t.Fatal(errNotForSettings)
	}
}

// Through the hub's upstream proxy, a configured name is reached at the address checked: the CONNECT names that
// address, never the name, which the proxy could resolve again to this machine or the metadata service (#3121).
// One refused answer still refuses the name, and the proxy is not asked at all.
func TestConfiguredDialPinsUpstreamCONNECT(t *testing.T) {
	var mu sync.Mutex
	var targets []string
	answers := map[string][]net.IP{
		"api.example.com":   {net.ParseIP("192.0.2.1")},
		"mixed.example.com": {net.ParseIP("192.0.2.1"), net.IPv4(169, 254, 169, 254)},
	}
	p := &Proxy{
		Resolve:  func(_ context.Context, host string) ([]net.IP, error) { return answers[host], nil },
		Upstream: func(*url.URL) (*url.URL, error) { return url.Parse("http://proxy.example.com:3128") },
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			if addr != "proxy.example.com:3128" {
				t.Errorf("dialled %s, not the proxy", addr)
			}
			near, far := net.Pipe()
			go func() {
				defer far.Close()
				r, err := http.ReadRequest(bufio.NewReader(far))
				if err != nil {
					return
				}
				mu.Lock()
				targets = append(targets, r.Method+" "+r.Host)
				mu.Unlock()
				_, _ = io.WriteString(far, "HTTP/1.1 200 Connection established\r\n\r\n")
				_, _ = io.Copy(io.Discard, far)
			}()
			return near, nil
		},
	}
	ctx := context.Background()
	c, err := p.dialConfigured(ctx, "api.example.com", "443")
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	mu.Lock()
	got := append([]string(nil), targets...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "CONNECT 192.0.2.1:443" {
		t.Fatalf("upstream asked %v, want [CONNECT 192.0.2.1:443]", got)
	}
	if _, err := p.dialConfigured(ctx, "mixed.example.com", "443"); !errors.Is(err, errNotForSettings) {
		t.Fatalf("mixed answer: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(targets) != 1 {
		t.Fatalf("upstream asked %v after a refused name", targets)
	}
}
