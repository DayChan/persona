package hubcli

import (
	"context"
	"errors"
	"flag"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/update"
)

// emptyHome is a HOME without hub.json.
func emptyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("PERSONA_LISTEN", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	return home
}

// `hub pair`'s argument mistakes are usage mistakes (exit 2, no log timestamp), found before hub.json is read or made.
func TestPairArgumentMistakes(t *testing.T) {
	home := emptyHome(t)
	for _, args := range [][]string{
		{"x"},
		{"--peer", "x"},
		{"--bogus"},
		{"--peer", "--machine", "m1"},
		{"--peer", "--legacy"},
		{"--peer", "--android"},
		{"--peer", "--add"},
		{"--peer", "--name", "n"},
		{"--peer", "--name="},
		{"--machine="},
		{"--machine", "m1", "--legacy"},
		{"--machine", "m1", "--android"},
		{"--add"},
		{"--name", "n"},
		{"--machine", "m1", "--name", "n"},
		{"--legacy", "--android"},
	} {
		err := pair(context.Background(), args)
		var ue clitext.UsageError
		if !errors.As(err, &ue) {
			t.Errorf("pair %q: %v, want a usage mistake", args, err)
		} else if !strings.Contains(string(ue), "usage: ") {
			t.Errorf("pair %q: %q, want the usage with it", args, ue)
		}
	}
	if err := pair(context.Background(), []string{"-h"}); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("pair -h: %v, want flag.ErrHelp (exit 0)", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".persona", "hub.json")); !os.IsNotExist(err) {
		t.Errorf("a usage mistake touched hub.json: %v", err)
	}
}

// Hub hands the command's context to pair and peer: Ctrl-C ends their wait for a hub that takes the connection and
// never answers.
func TestPairAndPeerStopOnTheCommandsContext(t *testing.T) {
	home := emptyHome(t)
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
	if err := os.MkdirAll(filepath.Join(home, ".persona"), 0o700); err != nil {
		t.Fatal(err)
	}
	hubJSON := `{"listen":"` + ln.Addr().String() + `","token":"t"}`
	if err := os.WriteFile(filepath.Join(home, ".persona", "hub.json"), []byte(hubJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"pair", "--peer"},
		{"peer", "list"},
		{"pair"},
		{"pair", "--machine", "m1"},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(200*time.Millisecond, cancel)
		done := make(chan error, 1)
		start := time.Now()
		go func() { done <- Hub(ctx, update.Build{}, args) }()
		select {
		case err := <-done:
			t.Logf("%q returned after %s: %v", args, time.Since(start).Round(time.Millisecond), err)
			if err == nil {
				t.Errorf("%q: no error from a hub that never answered", args)
			}
		case <-time.After(2 * time.Second): // under the 5 s a pairing code's request allows itself
			t.Fatalf("%q still waits after Ctrl-C", args)
		}
	}
}
