package voice

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The test binary stands in for ssh when it runs under that name (fakeSSH links it into PATH): `ssh ... -W addr
// machine` connects to addr here and copies stdin and stdout over it. Any other name runs the tests as usual.
func init() {
	if filepath.Base(os.Args[0]) != "ssh" {
		return
	}
	addr := ""
	for i, a := range os.Args {
		if a == "-W" && i+1 < len(os.Args) {
			addr = os.Args[i+1]
		}
	}
	c, err := net.Dial("tcp", addr)
	if err != nil {
		os.Exit(255)
	}
	go func() {
		_, _ = io.Copy(c, os.Stdin)
		_ = c.(*net.TCPConn).CloseWrite()
	}()
	_, _ = io.Copy(os.Stdout, c)
	os.Exit(0)
}

// fakeSSH puts the test binary first in PATH under the name ssh.
func fakeSSH(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in ssh is a symlink to the test binary")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(dir, "ssh")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Lists through an SSH machine share one client, so a second list reuses the first one's `ssh -W` connection instead
// of leaving a new ssh process open each time, and that client closes idle connections.
func TestSSHListsReuseOneConnection(t *testing.T) {
	fakeSSH(t)
	var mu sync.Mutex
	states := map[net.Conn]http.ConnState{}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"models":[{"id":"m1"}]}`))
	}))
	// StateNew is set as the server accepts, before it reads the request, so it is in by the time a list returns.
	srv.Config.ConnState = func(c net.Conn, s http.ConnState) {
		mu.Lock()
		states[c] = s
		mu.Unlock()
	}
	srv.Start()
	t.Cleanup(srv.Close)
	p := &Provider{ID: "up", URL: "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/live", SSH: "jump.example"}
	t.Cleanup(func() {
		if c, ok := sshClients.LoadAndDelete(p.SSH); ok {
			c.(*http.Client).CloseIdleConnections()
		}
	})

	for i := range 3 {
		var out struct{ Models []struct{ ID string } }
		if err := getJSON(context.Background(), p, "/models", nil, &out); err != nil {
			t.Fatalf("list %d: %v", i+1, err)
		}
		if len(out.Models) != 1 || out.Models[0].ID != "m1" {
			t.Fatalf("list %d: %+v", i+1, out)
		}
	}
	mu.Lock()
	seen := len(states)
	mu.Unlock()
	if seen != 1 {
		t.Fatalf("three lists through ssh opened %d connections, want 1 reused", seen)
	}

	c := p.httpClient()
	if other := (&Provider{SSH: p.SSH}).httpClient(); other != c {
		t.Fatal("two providers on the same SSH machine got different clients")
	}
	if tr, ok := c.Transport.(*http.Transport); !ok || tr.IdleConnTimeout != 90*time.Second {
		t.Fatalf("the ssh client's transport does not close idle connections after 90 s: %#v", c.Transport)
	}

	// Closing the idle connection ends the stand-in ssh, which the server sees as a close.
	c.CloseIdleConnections()
	waitFor(t, "the idle ssh connection closed", func() bool {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range states {
			if s != http.StateClosed {
				return false
			}
		}
		return true
	})
}
