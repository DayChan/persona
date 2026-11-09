package nodeupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
)

// holderProc is a child standing in for the resident process; Done reports its exit.
type holderProc struct {
	PID  int
	Done chan error
}

func startHolder(t *testing.T) holderProc {
	t.Helper()
	cmd := exec.Command("sh", "-c", "exec sleep 30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return holderProc{PID: cmd.Process.Pid, Done: done}
}

func waitKilled(t *testing.T, ctx context.Context, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("process exited successfully instead of being killed")
		}
	case <-ctx.Done():
		t.Fatal("watchdog did not stop the failed process")
	}
}

// commitAccepted runs one full accepted transaction and returns its pending marker.
func commitAccepted(t *testing.T, m *Manager, data []byte) pending {
	t.Helper()
	id := receive(t, m, data, spec(data, "next"))
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	p, err := loadPending(markerPath(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// installAccepted builds the on-disk state of one accepted transaction without
// running a commit, whose self-check executes the staged binary and briefly
// holds a kernel write lock on it: what these tests arrange by hand instead.
func installAccepted(t *testing.T, m *Manager, data []byte) pending {
	t.Helper()
	old, _ := fixtures(t)
	id := strings.Repeat("ab", 8)
	dir := filepath.Dir(m.exe)
	oldHash := hash(t, m.exe)
	backup := m.exe + ".node-update-backup-" + id
	if err := copyFile(old, backup); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, ".node-update-stage-"+id)
	if err := os.WriteFile(staged, data, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(staged, m.exe); err != nil {
		t.Fatal(err)
	}
	p := pending{ID: id, Exe: m.exe, Backup: backup, Stage: staged, Version: "next", From: "old",
		OldHash: oldHash, NewHash: spec(data, "next").SHA256, PID: os.Getpid(), ProcessStart: "the resident process",
		Deadline: time.Now().Add(time.Hour)}
	if err := savePending(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// An external deployment that rewrites the installed file in place or renames
// its own file over it after the rollback's hash check must never be undone by
// the backup: the swap rechecks the installed file through one descriptor right
// before the rename and refuses instead.
func TestRestoreRefusesToOverwriteFileItDidNotStage(t *testing.T) {
	t.Run("rewritten-in-place", func(t *testing.T) {
		m, data, _ := testManager(t)
		p := installAccepted(t, m, data)
		if err := os.WriteFile(m.exe, []byte("someone else's build"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := restore(p); !errors.Is(err, errSuperseded) {
			t.Fatalf("in-place rewrite: %v", err)
		}
		if got, _ := os.ReadFile(m.exe); string(got) != "someone else's build" {
			t.Fatalf("rollback overwrote an unstaged file: %q", got)
		}
	})

	t.Run("renamed-into-place", func(t *testing.T) {
		m, data, _ := testManager(t)
		p := installAccepted(t, m, data)
		replaceExternally(t, m.exe, []byte("another build"))
		if err := restore(p); !errors.Is(err, errSuperseded) {
			t.Fatalf("renamed replacement: %v", err)
		}
		if got, _ := os.ReadFile(m.exe); string(got) != "another build" {
			t.Fatalf("rollback overwrote an unstaged file: %q", got)
		}
		if _, err := os.Stat(p.Backup); err != nil {
			t.Fatal("refused rollback consumed the backup", err)
		}
	})

	t.Run("staged-file-still-swaps", func(t *testing.T) {
		m, data, _ := testManager(t)
		oldHash := hash(t, m.exe)
		p := installAccepted(t, m, data)
		if err := restore(p); err != nil {
			t.Fatal(err)
		}
		if hash(t, m.exe) != oldHash {
			t.Fatal("rollback of the staged file failed")
		}
	})
}

// renameStill is the swap under every user (commit and rollback): it swaps only
// the file whose hash it read through one descriptor. A rewritten or renamed
// destination refuses; a destination holding the same bytes still swaps.
func TestRenameStillSwapsOnlyTheFileItChecked(t *testing.T) {
	m, _, _ := testManager(t)
	dir := filepath.Dir(m.exe)
	want := hash(t, m.exe)
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("replacement"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("rewritten-destination-refuses", func(t *testing.T) {
		if err := os.WriteFile(m.exe, []byte("rewritten"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := renameStill(src, m.exe, want); !errors.Is(err, errSuperseded) {
			t.Fatalf("rewritten destination: %v", err)
		}
		if got, _ := os.ReadFile(m.exe); string(got) != "rewritten" {
			t.Fatalf("swap overwrote an unstaged file: %q", got)
		}
		if _, err := os.Stat(src); err != nil {
			t.Fatal("refused swap consumed its source", err)
		}
	})

	t.Run("renamed-destination-refuses", func(t *testing.T) {
		replaceExternally(t, m.exe, []byte("replaced"))
		if err := renameStill(src, m.exe, want); !errors.Is(err, errSuperseded) {
			t.Fatalf("renamed destination: %v", err)
		}
		if got, _ := os.ReadFile(m.exe); string(got) != "replaced" {
			t.Fatalf("swap overwrote an unstaged file: %q", got)
		}
	})

	t.Run("checked-file-swaps", func(t *testing.T) {
		if err := renameStill(src, m.exe, hash(t, m.exe)); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(m.exe); string(got) != "replacement" {
			t.Fatalf("swap did not land: %q", got)
		}
		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Fatal("swapped source remains")
		}
	})

	t.Run("touched-before-check-swaps", func(t *testing.T) {
		m, _, _ := testManager(t)
		want := hash(t, m.exe)
		src := filepath.Join(filepath.Dir(m.exe), "src2")
		if err := os.WriteFile(src, []byte("replacement"), 0o755); err != nil {
			t.Fatal(err)
		}
		// A metadata touch that settled before the check (a backup tool) is not
		// an in-flight rewrite: the swap must not refuse on it.
		future := time.Now().Add(time.Minute)
		if err := os.Chtimes(m.exe, future, future); err != nil {
			t.Fatal(err)
		}
		if err := renameStill(src, m.exe, want); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(m.exe); string(got) != "replacement" {
			t.Fatalf("swap did not land: %q", got)
		}
	})
}

// A resident that recorded its executable's identity at startup still refuses
// to commit over a file renamed into place meanwhile (an in-place rewrite is
// the hub's call to supersede: the commit re-hashes what is there).
func TestCommitRefusesWhenExecutableIdentityChanged(t *testing.T) {
	m, data, _ := testManager(t)
	st, err := os.Stat(m.exe)
	if err != nil {
		t.Fatal(err)
	}
	m.identity = st
	id := receive(t, m, data, spec(data, "next"))
	replaceExternally(t, m.exe, []byte("independent deployment"))
	if _, err := m.Commit(context.Background(), id, nil); err == nil {
		t.Fatal("commit installed over a replaced executable")
	}
	if got, _ := os.ReadFile(m.exe); string(got) != "independent deployment" {
		t.Fatalf("commit overwrote an unstaged file: %q", got)
	}
	if _, err := os.Stat(markerPath(m.exe)); !os.IsNotExist(err) {
		t.Fatal("refused commit left a pending marker")
	}
}

// The exact identity, not the second-resolution start, decides: a marker whose
// exact identity names the process still stops it even when the portable start
// string was scrambled, and vice versa.
func TestWatchdogUsesExactProcessIdentity(t *testing.T) {
	t.Run("exact-identity-stops-process", func(t *testing.T) {
		m, data, _ := testManager(t)
		oldHash := hash(t, m.exe)
		p := commitAccepted(t, m, data)
		child := startHolder(t)
		p.PID = child.PID
		p.ProcessStart = "not the process start"
		p.ProcessPrecise = osproc.Identify(child.PID).Start
		if p.ProcessPrecise == "" {
			t.Skip("platform gives no exact start time")
		}
		p.Deadline = time.Now().Add(-time.Second)
		if err := savePending(p); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := Watch(ctx, markerPath(m.exe), p.ID, nil); err != nil {
			t.Fatal(err)
		}
		if hash(t, m.exe) != oldHash {
			t.Fatal("watchdog failed to restore the old executable")
		}
		waitKilled(t, ctx, child.Done)
	})

	t.Run("mismatched-exact-identity-spares-process", func(t *testing.T) {
		m, data, _ := testManager(t)
		oldHash := hash(t, m.exe)
		p := commitAccepted(t, m, data)
		child := startHolder(t)
		p.PID = child.PID
		p.ProcessStart = "not the process start"
		p.ProcessPrecise = osproc.Identify(os.Getpid()).Start // another instance
		if p.ProcessPrecise == "" {
			t.Skip("platform gives no exact start time")
		}
		p.Deadline = time.Now().Add(-time.Second)
		if err := savePending(p); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := Watch(ctx, markerPath(m.exe), p.ID, nil); err != nil {
			t.Fatal(err)
		}
		if hash(t, m.exe) != oldHash {
			t.Fatal("watchdog failed to restore the old executable")
		}
		select {
		case err := <-child.Done:
			t.Fatalf("watchdog killed a process whose exact identity mismatched: %v", err)
		default:
		}
	})
}
