//go:build !windows

package node

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// listenFakeCodexBus serves a fake App bus where the App on macOS does: a socket in the home's ipc directory.
func listenFakeCodexBus(t *testing.T) (home string, l net.Listener) {
	t.Helper()
	// A short path: a unix socket path over ~104 bytes (macOS's t.TempDir()) cannot be bound.
	home, err := os.MkdirTemp("/tmp", "cxbus")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	os.MkdirAll(filepath.Join(home, "ipc"), 0o700)
	l, err = net.Listen("unix", codexDesktopSocket(home))
	if err != nil {
		t.Fatal(err)
	}
	return home, l
}

// noFakeCodexBus: no bus is served where the test looks (each test home has its own socket path).
func noFakeCodexBus(*testing.T) {}
