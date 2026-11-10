package hub

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/clitext"
)

// silentHub is a loopback listener that takes connections and never answers (a stuck hub, or another program on
// hub.json's port), with HOME's hub.json pointing at it. It returns the port.
func silentHub(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			c.Close()
		}
	})
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	peerCLIHubAt(t, port)
	return port
}

// peerCLIHubAt is an empty HOME whose hub.json points the commands at the hub on 127.0.0.1:port.
func peerCLIHubAt(t *testing.T, port string) {
	t.Helper()
	home := peerCLIHome(t)
	if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	hubJSON := `{"listen":"127.0.0.1:` + port + `","token":"t"}`
	if err := os.WriteFile(filepath.Join(home, ".persona", "hub.json"), []byte(hubJSON), 0o600); err != nil {
		t.Fatal(err)
	}
}

// peerCLIHome is an empty HOME for LoadConfig.
func peerCLIHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("PERSONA_LISTEN", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	return home
}

// within runs f and fails the test when it has not returned after limit.
func within(t *testing.T, limit time.Duration, name string, f func() error) error {
	t.Helper()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- f() }()
	select {
	case err := <-done:
		t.Logf("%s returned after %s: %v", name, time.Since(start).Round(time.Millisecond), err)
		return err
	case <-time.After(limit):
		t.Fatalf("%s still waits on a hub that never answers after %s", name, limit)
		return nil
	}
}

// `hub pair --peer` and `hub peer …` on a hub that takes the connection and never answers: Ctrl-C (the command's
// context) ends them at once, and without it they end after adminCallLimit instead of waiting forever.
func TestPeerCommandsOnAHubThatNeverAnswers(t *testing.T) {
	port := silentHub(t)
	commands := map[string]func(ctx context.Context) error{
		"pair --peer": PairPeer,
		"peer list": func(ctx context.Context) error {
			return PeerCommand(ctx, []string{"list"}, strings.NewReader(""), io.Discard)
		},
		"peer center": func(ctx context.Context) error {
			return PeerCommand(ctx, []string{"center", "--confirm"}, strings.NewReader(""), io.Discard)
		},
		"peer add -": func(ctx context.Context) error {
			return PeerCommand(ctx, []string{"add", "-"}, strings.NewReader("persona://peer?code=x\n"), io.Discard)
		},
	}
	for name, run := range commands {
		t.Run(name+" stopped", func(t *testing.T) {
			saved := adminCallLimit
			adminCallLimit = time.Minute
			t.Cleanup(func() { adminCallLimit = saved })
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(200*time.Millisecond, cancel)
			err := within(t, 2*time.Second, name, func() error { return run(ctx) })
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s: %v, want context.Canceled", name, err)
			}
		})
		t.Run(name+" out of time", func(t *testing.T) {
			saved := adminCallLimit
			adminCallLimit = 300 * time.Millisecond
			t.Cleanup(func() { adminCallLimit = saved })
			err := within(t, 3*time.Second, name, func() error { return run(context.Background()) })
			if err == nil || !strings.Contains(err.Error(), "did not answer within") || !strings.Contains(err.Error(), port) {
				t.Errorf("%s: %v, want the hub on port %s not answering", name, err, port)
			}
		})
	}
}

// `peer list` prints other hubs' names, ids and addresses: a discovered hub's come from an mDNS advert any host on the
// LAN can send, a paired one's from that hub. Every control character is shown, not sent to the terminal, and each
// hub stays on one line, so a hostile name cannot run terminal sequences or print a fake line.
func TestPeerListPrintsOtherHubsTextInert(t *testing.T) {
	const hostile = `Office\u001b]0;pwned\u0007\r\nfake line`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/admin/cli/peers":
			io.WriteString(w, `{"peers":[{"hub_id":"h1\u009b2J","name":"`+hostile+`","state":"offline","error":"dial\u001b[31m\nfailed","machines":1,"role":"member"}],`+
				`"discovered":[{"hub_id":"h2","name":"`+hostile+`","addresses":["https://192.0.2.6:18662\u0007"],"fp":""}]}`)
		case "/api/v1/admin/cli/mesh":
			io.WriteString(w, `{"role":"member","term":3,"center":{"hub_id":"h1","name":"`+hostile+`","online":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	peerCLIHubAt(t, port)
	var out strings.Builder
	if err := PeerCommand(context.Background(), []string{"list"}, strings.NewReader(""), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.ContainsFunc(got, func(r rune) bool { return r != '\n' && unicode.Is(unicode.Cc, r) }) {
		t.Errorf("raw control characters in %q", got)
	}
	if n := strings.Count(got, "\n"); n != 3 || strings.Contains(got, "\nfake line") {
		t.Errorf("%d lines, want 3 (center, one peer, one discovered) and no injected line: %q", n, got)
	}
	const name = "Office<ESC>]0;pwned<U+0007><U+000D><U+000A>fake line"
	for _, want := range []string{"中心是「" + name + "」（h1，",
		"h1<U+009B>2J  " + name + "  成员  连不上：dial<ESC>[31m<U+000A>failed  1 台电脑\n",
		"局域网里发现：h2  " + name + "  https://192.0.2.6:18662<U+0007>\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// `peer add -` waits for the code on stdin; Ctrl-C ends that wait too.
func TestPeerAddStopsWaitingForStdin(t *testing.T) {
	silentHub(t)
	stdin, w := io.Pipe()
	t.Cleanup(func() { w.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	err := within(t, 2*time.Second, "peer add -", func() error {
		return PeerCommand(ctx, []string{"add", "-"}, stdin, io.Discard)
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("peer add -: %v, want context.Canceled", err)
	}
}

// The peer commands refuse words they would otherwise drop or misread, as a usage mistake (exit 2), before reading
// or creating hub.json.
func TestPeerArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		ok   bool
	}{
		{args: nil},
		{args: []string{"bogus"}},
		{args: []string{"list"}, ok: true},
		{args: []string{"list", "x"}},
		{args: []string{"list", "--all"}},
		{args: []string{"add"}},
		{args: []string{"add", "persona://peer?code=x"}, ok: true},
		{args: []string{"add", "-"}, ok: true},
		{args: []string{"add", "a", "b"}},
		{args: []string{"add", "--code"}},
		{args: []string{"remove"}},
		{args: []string{"remove", "hub-abc123"}, ok: true},
		{args: []string{"remove", "hub-abc123", "hub-def456"}},
		{args: []string{"remove", "--force"}},
		{args: []string{"center"}, ok: true},
		{args: []string{"center", "--confirm"}, ok: true},
		{args: []string{"center", "hub-abc123"}, ok: true},
		{args: []string{"center", "--confirm", "hub-abc123"}, ok: true},
		{args: []string{"center", "hub-abc123", "--confirm"}, ok: true},
		{args: []string{"center", "a", "b"}},
		{args: []string{"center", "--bogus"}},
		{args: []string{"center", "-confirm"}},
		{args: []string{"center", "hub-abc123", "--yes"}},
	} {
		err := checkPeerArgs(tc.args)
		var ue clitext.UsageError
		switch {
		case tc.ok && err != nil:
			t.Errorf("peer %q: %v, want it accepted", tc.args, err)
		case !tc.ok && !errors.As(err, &ue):
			t.Errorf("peer %q: %v, want a usage mistake", tc.args, err)
		}
	}

	home := peerCLIHome(t)
	err := PeerCommand(context.Background(), []string{"center", "a", "b"}, strings.NewReader(""), io.Discard)
	var ue clitext.UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("peer center a b: %v, want a usage mistake", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".persona", "hub.json")); !os.IsNotExist(err) {
		t.Errorf("a usage mistake touched hub.json: %v", err)
	}
}

// Help asked for anywhere in a peer command (-h, --help, or help as the command) prints the usage on out and is no
// mistake (exit 0), before reading or creating hub.json; `personad hub pair -h` and `persona-hub hub -h` do the same.
func TestPeerHelp(t *testing.T) {
	home := peerCLIHome(t)
	for _, args := range [][]string{{"-h"}, {"--help"}, {"help"}, {"list", "-h"}, {"add", "--help"}, {"center", "hub-abc123", "-h"}} {
		var out strings.Builder
		if err := PeerCommand(context.Background(), args, strings.NewReader(""), &out); err != nil {
			t.Errorf("peer %q: %v, want the usage and no error", args, err)
		}
		if want := clitext.Named(peerUsage) + "\n"; out.String() != want {
			t.Errorf("peer %q printed %q, want %q", args, out.String(), want)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".persona", "hub.json")); !os.IsNotExist(err) {
		t.Errorf("peer help touched hub.json: %v", err)
	}
}
