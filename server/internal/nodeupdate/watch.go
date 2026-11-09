package nodeupdate

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
)

type pending struct {
	ID             string    `json:"id"`
	Exe            string    `json:"exe"`
	Backup         string    `json:"backup"`
	Stage          string    `json:"stage"`
	Version        string    `json:"version"`
	From           string    `json:"from"`
	OldHash        string    `json:"old_sha256"`
	NewHash        string    `json:"new_sha256"`
	PID            int       `json:"pid"`
	ProcessStart   string    `json:"process_start"`
	ProcessPrecise string    `json:"process_start_precise,omitempty"` // exact instance identity; absent in older markers
	WatchPID       int       `json:"watch_pid"`
	WatchStart     string    `json:"watch_start"`
	WatchPrecise   string    `json:"watch_start_precise,omitempty"`
	Deadline       time.Time `json:"deadline"`
}

func markerPath(exe string) string { return exe + ".node-update-pending.json" }

func savePending(p pending) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p.Exe), ".node-update-marker-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), markerPath(p.Exe)); err != nil {
		return err
	}
	return syncDir(filepath.Dir(p.Exe))
}

func loadPending(path string) (pending, error) {
	var p pending
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 16384))
	if err := decoder.Decode(&p); err != nil {
		return p, err
	}
	// Managed watchdog startup appends a readiness record through an existing
	// descriptor. A late writer cannot recreate a transaction canceled by the
	// parent; a partial append cannot corrupt the durable first JSON object.
	var ready struct {
		ID      string `json:"id"`
		PID     int    `json:"watch_pid"`
		Start   string `json:"watch_start"`
		Precise string `json:"watch_start_precise"`
	}
	if decoder.Decode(&ready) == nil && ready.ID == p.ID && ready.PID > 1 && ready.Start != "" {
		p.WatchPID, p.WatchStart, p.WatchPrecise = ready.PID, ready.Start, ready.Precise
	}
	id, idErr := hex.DecodeString(p.ID)
	if idErr != nil || len(id) != 16 || filepath.Clean(p.Exe) != p.Exe || filepath.IsAbs(p.Exe) == false || markerPath(p.Exe) != path || p.Backup != p.Exe+".node-update-backup-"+p.ID || filepath.Dir(p.Stage) != filepath.Dir(p.Exe) || !strings.HasPrefix(filepath.Base(p.Stage), ".node-update-stage-") || p.PID < 2 || len(p.OldHash) != 64 || len(p.NewHash) != 64 || p.Deadline.IsZero() {
		return p, errors.New("invalid node update transaction marker")
	}
	return p, nil
}

// Recover startup before opening sockets. A new version that missed its deadline
// must exec the restored old file before accepting work.
func (m *Manager) recover() error {
	return locked(m.exe, func() error {
		p, err := loadPending(markerPath(m.exe))
		if os.IsNotExist(err) {
			return m.cleanStaleStages("")
		}
		if err != nil {
			return err
		}
		sum, err := fileHash(m.exe)
		if err != nil {
			return err
		}
		if sum == p.OldHash {
			// A supervisor may have loaded the new inode just before rollback.
			// Retain the barrier until the restored executable actually starts.
			if m.version != p.From {
				m.needsRestart = true
				return nil
			}
			return cleanup(p)
		}
		if sum != p.NewHash {
			// An external deploy superseded this update; never undo its file.
			return cleanup(p)
		}
		if !time.Now().Before(p.Deadline) {
			if err := restore(p); err != nil {
				if errors.Is(err, errSuperseded) {
					// A deployment we did not stage replaced the file after the
					// check above; never undo its file.
					return cleanup(p)
				}
				return err
			}
			m.needsRestart = true
			return nil
		}
		if m.version != p.Version {
			return errors.New("installed update is not the running node version")
		}
		p.PID = os.Getpid()
		p.ProcessStart, err = processStart(p.PID)
		if err != nil {
			return err
		}
		p.ProcessPrecise = osproc.Identify(p.PID).Start
		if err := savePending(p); err != nil {
			return err
		}
		// The existing watchdog normally survives startup crashes. Starting another
		// is harmless (directory lock + transaction id) and recovers a dead watchdog.
		watchAlive := aliveIs(p.WatchPID, p.WatchStart, p.WatchPrecise)
		if !watchAlive {
			if err := m.startWatch(p.Backup, markerPath(m.exe), p.ID); err != nil {
				return err
			}
		}
		return m.cleanStaleStages(p.Stage)
	})
}

func (m *Manager) RestartRequired() bool { return m.needsRestart }
func (m *Manager) Executable() string    { return m.exe }

func (m *Manager) cleanStaleStages(keep string) error {
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(m.exe), ".node-update-stage-*"))
	if err != nil {
		return err
	}
	for _, path := range paths {
		if path == keep {
			continue
		}
		if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) >= UploadTTL {
			_ = os.Remove(path)
		}
	}
	return nil
}

func startWatchdog(backup, path, id string) error {
	if runtime.GOOS == "linux" && os.Getenv("INVOCATION_ID") != "" {
		return startSystemdWatchdog(backup, path, id)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	defer w.Close()
	cmd := exec.Command(backup, "node-update-watch", path, id)
	cmd.SysProcAttr = osproc.DetachedAttr()
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = w.Close()
	_ = r.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready [1]byte
	_, err = io.ReadFull(r, ready[:])
	if err != nil || ready[0] != 1 {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("watchdog did not become ready: %w", err)
	}
	p, err := loadPending(path)
	if err == nil {
		p.WatchPID = cmd.Process.Pid
		p.WatchStart, err = processStart(p.WatchPID)
		if err == nil {
			p.WatchPrecise = osproc.Identify(p.WatchPID).Start
			err = savePending(p)
		}
	}
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	return cmd.Process.Release()
}

// A detached child still belongs to its parent's systemd cgroup. Run the
// watchdog in a separate transient user service so a startup crash and
// KillMode=control-group cannot destroy the rollback process with the node.
func startSystemdWatchdog(backup, path, id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "systemd-run", "--user", "--quiet", "--collect", "--no-block", "--unit=persona-node-update-"+id, "--property=Type=exec", "--", backup, "node-update-watch", path, id, "--managed-ready")
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("start independent systemd watchdog: %w", err)
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		p, err := loadPending(path)
		if err != nil {
			return err
		}
		if p.ID != id {
			return errors.New("watchdog transaction changed")
		}
		if p.WatchPID > 1 && p.WatchStart != "" && aliveIs(p.WatchPID, p.WatchStart, p.WatchPrecise) {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("systemd watchdog did not become ready")
		case <-ticker.C:
		}
	}
}

// WatchManaged publishes readiness through the existing transaction marker.
// Its creator holds the directory lock until this handshake completes, so this
// one initial marker write must happen before the watchdog takes that lock.
func WatchManaged(ctx context.Context, path, id string) error {
	p, err := loadPending(path)
	if err != nil {
		return err
	}
	if p.ID != id {
		return errors.New("node watchdog transaction mismatch")
	}
	birth, err := processStart(os.Getpid())
	if err != nil {
		return err
	}
	precise := osproc.Identify(os.Getpid()).Start
	// Never create or rename the marker here. The creator holds the directory
	// lock while waiting; a delayed service must not resurrect a canceled swap.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	var current pending
	if err := json.NewDecoder(io.LimitReader(f, 16384)).Decode(&current); err != nil {
		_ = f.Close()
		return err
	}
	if current.ID != id {
		_ = f.Close()
		return errors.New("node watchdog transaction changed")
	}
	ready, err := json.Marshal(struct {
		ID      string `json:"id"`
		PID     int    `json:"watch_pid"`
		Start   string `json:"watch_start"`
		Precise string `json:"watch_start_precise,omitempty"`
	}{id, os.Getpid(), birth, precise})
	if err == nil {
		_, err = f.Write(append([]byte{'\n'}, ready...))
	}
	if err == nil {
		err = f.Sync()
	}
	_ = f.Close()
	if err != nil {
		return err
	}
	return Watch(ctx, path, id, nil)
}

// Watch runs from the old executable in an independent session. The ready writer
// is optional for tests; production uses inherited fd 3 to acknowledge startup.
func Watch(ctx context.Context, path, id string, ready io.Writer) error {
	p, err := loadPending(path)
	if err != nil {
		return err
	}
	if p.ID != id {
		return errors.New("node watchdog transaction mismatch")
	}
	if ready != nil {
		if _, err := ready.Write([]byte{1}); err != nil {
			return err
		}
		if c, ok := ready.(io.Closer); ok {
			_ = c.Close()
		}
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		done := false
		err := locked(p.Exe, func() error {
			current, err := loadPending(path)
			if os.IsNotExist(err) {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
			if current.ID != id {
				done = true
				return nil
			}
			p = current
			if time.Now().Before(p.Deadline) {
				return nil
			}
			sum, err := fileHash(p.Exe)
			if err != nil {
				return err
			}
			if sum != p.NewHash && sum != p.OldHash {
				done = true
				return cleanup(p)
			}
			if sum == p.NewHash {
				if err := restore(p); err != nil {
					if errors.Is(err, errSuperseded) {
						// A deployment we did not stage replaced the file after
						// the hash check above; never undo its file.
						done = true
						return cleanup(p)
					}
					return err
				}
			}
			// PID survives exec. A service restart records its replacement PID on
			// startup. The recorded identity — exact where the platform gives
			// one, the second-resolution start everywhere — prevents killing an
			// unrelated reused PID.
			if err := killRecorded(p.PID, p.ProcessStart, p.ProcessPrecise); err != nil {
				return err
			}
			// Keep the rollback barrier for a supervisor replacement that may
			// already have loaded the new inode. Old-version startup clears it.
			done = true
			return nil
		})
		if err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// restore puts the backup back only while the installed file is still this
// transaction's own staged build, rechecked through one descriptor right before
// the swap; an external deployment racing the rollback keeps its file.
func restore(p pending) error {
	sum, err := fileHash(p.Backup)
	if err != nil {
		return fmt.Errorf("node rollback backup: %w", err)
	}
	if sum != p.OldHash {
		return errors.New("node rollback backup checksum mismatch")
	}
	if err := renameStill(p.Backup, p.Exe, p.NewHash); err != nil {
		return err
	}
	return syncDir(filepath.Dir(p.Exe))
}
func cleanup(p pending) error {
	if err := os.Remove(markerPath(p.Exe)); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(p.Backup)
	_ = os.Remove(p.Stage)
	return syncDir(filepath.Dir(p.Exe))
}

// Cancel a prepared swap without discarding the still-retryable upload.
func cleanupPendingOnly(p pending) error {
	if err := os.Remove(markerPath(p.Exe)); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = os.Remove(p.Backup)
	return syncDir(filepath.Dir(p.Exe))
}
func processStart(pid int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ps", "-p", fmt.Sprint(pid), "-o", "lstart=")
	cmd.Env = append(os.Environ(), "LC_ALL=C", "TZ=UTC")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// aliveIs says whether pid is still the exact recorded process instance. The
// exact identity decides whenever the marker carries one and the platform can
// read it now; the second-resolution start — what every older version wrote —
// decides otherwise and stays the fallback when the exact identity cannot be
// read right now (a masked procfs): still a guard against pid reuse.
func aliveIs(pid int, start, precise string) bool {
	if pid < 2 || start == "" {
		return false
	}
	if precise != "" {
		if id := osproc.Identify(pid); id.Start != "" {
			return id.Start == precise
		}
	}
	birth, err := processStart(pid)
	return err == nil && birth != "" && birth == start
}

// killRecorded ends the recorded process, but only when it is still that exact
// instance: pinned by pidfd or process handle where the platform has one, so a
// pid reused between the identity check and the kill is never hit. Without a
// readable exact identity the portable start time decides.
func killRecorded(pid int, start, precise string) error {
	if pid < 2 || start == "" {
		return nil
	}
	if precise != "" && osproc.Identify(pid).Start != "" {
		return osproc.ProcIdentity{Start: precise}.KillIfStill(pid)
	}
	if !aliveIs(pid, start, precise) {
		return nil
	}
	return osproc.Kill(pid)
}
