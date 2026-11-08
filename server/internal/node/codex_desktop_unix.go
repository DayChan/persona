//go:build !windows

package node

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
)

// The Codex App's bus on macOS: a socket in its Codex home (codex_desktop.go).

func codexDesktopSocket(home string) string { return filepath.Join(home, "ipc", "ipc.sock") }

// codexDesktopAvailable: the App runs on this machine for that Codex home (its bus socket is there).
func codexDesktopAvailable(home string) bool {
	if home == "" {
		return false
	}
	st, err := os.Stat(codexDesktopSocket(home))
	return err == nil && st.Mode()&os.ModeSocket != 0
}

func dialCodexDesktopBus(ctx context.Context, home string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", codexDesktopSocket(home))
}

// openCodexThread asks the App to open a thread (it comes to the front, showing it). A variable for tests.
var openCodexThread = func(thread string) error {
	return exec.Command("open", "codex://threads/"+thread).Run()
}
