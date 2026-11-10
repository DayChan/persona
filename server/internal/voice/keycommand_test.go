//go:build !windows

package voice

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// backgroundKeyCommand is a key_command that starts `sleep 30` in the background, holding the command's output, and
// then runs the shell code in then. The background process is killed when the test ends.
func backgroundKeyCommand(t *testing.T, then string) *Provider {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return &Provider{ID: "rt", KeyCommand: "sleep 30 & echo $! > '" + pidFile + "'; " + then}
}

// credentialWithin runs p.credential and fails the test if it has not returned within 10 s.
func credentialWithin(t *testing.T, p *Provider) (string, error) {
	t.Helper()
	type result struct {
		key string
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		key, err := p.credential(context.Background())
		done <- result{key, err}
	}()
	select {
	case r := <-done:
		if took := time.Since(start); took > 10*time.Second {
			t.Fatalf("credential took %v: it waited for the background process", took)
		}
		return r.key, r.err
	case <-time.After(10 * time.Second):
		t.Fatal("credential still waits for the background process after 10s")
		return "", nil
	}
}

// A key_command that leaves a background process holding its output (an agent it starts, `cmd & echo token`) yields
// its credential once the command itself has exited: the call start is not held until that process ends.
func TestKeyCommandIsNotHeldByABackgroundProcessKeepingItsOutput(t *testing.T) {
	key, err := credentialWithin(t, backgroundKeyCommand(t, "echo k"))
	if err != nil || key != "k" {
		t.Fatalf("credential = %q, %v; want \"k\", nil", key, err)
	}
}

// A key_command that fails, or prints nothing, is still an error when a background process holds its output.
func TestKeyCommandFailureWithABackgroundProcessIsAnError(t *testing.T) {
	for then, want := range map[string]string{
		"echo k; exit 3": "rt: key_command: exit status 3",
		"true":           "rt: key_command printed nothing",
	} {
		if _, err := credentialWithin(t, backgroundKeyCommand(t, then)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: credential error = %v; want %q", then, err, want)
		}
	}
}
