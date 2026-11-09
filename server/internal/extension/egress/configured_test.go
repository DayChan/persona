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

// A loopback address in a configured target lets through that loopback port only (via LocalHost or literal loopback),
// and refuses other loopback ports, the proxy's own port and the hub's port.
func TestConfiguredLoopbackTarget(t *testing.T) {
	srv := newEcho(t)
	_, port, err := net.SplitHostPort(srv)
	if err != nil {
		t.Fatal(err)
	}
	p := &Proxy{HubAddr: "127.0.0.1:18660"}
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	_, ownPort, _ := net.SplitHostPort(p.Addr())

	for _, targetHost := range []string{"127.0.0.1", "localhost"} {
		purl := p.Register("aster", Rule{Targets: []Target{{Host: targetHost, Port: port, HTTP: true}}})
		c := client(purl)
		resp, err := c.Get("http://" + LocalHost + ":" + port + "/me/usage")
		if err != nil {
			t.Fatalf("%s via LocalHost: %v", targetHost, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s via LocalHost: status %d", targetHost, resp.StatusCode)
		}
		for _, forbiddenPort := range []string{"9", ownPort, "18660"} {
			resp, err := c.Get("http://" + LocalHost + ":" + forbiddenPort + "/me/usage")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s port %s: status %d, want 403", targetHost, forbiddenPort, resp.StatusCode)
			}
		}
	}
	if ln6, err := net.Listen("tcp6", "[::1]:0"); err == nil {
		srv6 := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })}
		go srv6.Serve(ln6)
		t.Cleanup(func() { srv6.Close() })
		_, port6, _ := net.SplitHostPort(ln6.Addr().String())
		purl := p.Register("aster6", Rule{Targets: []Target{{Host: "::1", Port: port6, HTTP: true}}})
		resp, err := client(purl).Get("http://" + LocalHost + ":" + port6 + "/me/usage")
		if err != nil {
			t.Fatalf("::1 via LocalHost: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("::1 via LocalHost: status %d", resp.StatusCode)
		}
	}
}
