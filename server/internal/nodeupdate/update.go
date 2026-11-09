// Package nodeupdate owns one resident process's executable update transaction.
// Uploads survive RPC reconnects; only the resident process may enable updates.
package nodeupdate

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/buildinfo"
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
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
)

const (
	MaxSize        = 256 << 20
	MaxChunk       = proto.UploadChunkBytes
	UploadTTL      = 10 * time.Minute
	ConfirmTimeout = 3 * time.Minute
)

// Check is emitted by node-update-check, without opening sockets or loading node state.
type Check struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	// Program is which program the executable is (nodeproto.ProgramPersonad / ProgramPersonaNode): scripts ask it
	// before they replace or run one (the ssh Redeploy script, install.sh, persona-link node switch). Builds from before
	// it leave it out (personad).
	Program string `json:"program,omitempty"`
}

type upload struct {
	ID        string
	Spec      proto.NodeUpdateBeginParams
	Path      string
	Offset    int64
	At        time.Time
	Accepted  bool
	Restarted bool
}

// Manager must be shared by all of a resident's connection-scoped Nodes.
type Manager struct {
	mu           sync.Mutex
	exe          string
	identity     os.FileInfo
	version      string
	reason       string
	needsRestart bool
	upload       *upload
	restart      func()
	timer        *time.Timer
	sweep        *time.Timer
	closed       bool
	// Tests can shorten deadlines without modifying the production environment.
	timeout    time.Duration
	startWatch func(string, string, string) error
}

// New resolves the actual running file once. The executable is never inferred
// from HOME, PATH, an attach socket, or the spelling of argv[0].
func New(version string, restart func()) *Manager {
	m := &Manager{version: version, restart: restart, timeout: ConfirmTimeout, startWatch: startWatchdog}
	xpc := os.Getenv("XPC_SERVICE_NAME")
	supervised := (runtime.GOOS == "linux" && os.Getenv("INVOCATION_ID") != "") || (runtime.GOOS == "darwin" && xpc != "" && xpc != "0")
	if !supervised {
		m.reason = "常驻 node 未由 launchd/systemd 管理，无法保证失败后恢复"
		return m
	}
	if _, err := exec.LookPath("ps"); err != nil {
		m.reason = "缺少 ps，无法核对回滚进程身份"
		return m
	}
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("systemd-run"); err != nil {
			m.reason = "缺少 systemd-run，无法启动独立回滚守护"
			return m
		}
	}
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err == nil {
		exe, err = filepath.Abs(exe)
	}
	if err != nil {
		m.reason = "无法定位常驻 node 的实际执行文件"
		return m
	}
	m.exe = exe
	m.identity, err = os.Stat(exe)
	if err != nil || !m.identity.Mode().IsRegular() {
		m.reason = "常驻 node 的执行文件不可用"
		return m
	}
	if restart == nil {
		m.reason = "常驻 node 未提供进程重启入口"
		return m
	}
	if err := m.recover(); err != nil {
		m.reason = "上次更新尚未安全恢复：" + err.Error()
		return m
	}
	f, err := os.CreateTemp(filepath.Dir(exe), ".node-update-probe-*")
	if err != nil {
		m.reason = "常驻 node 的执行文件目录不可写"
		return m
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	m.armSweepLocked()
	return m
}

func (m *Manager) Capability() (bool, string) {
	if m == nil {
		return false, "当前连接使用临时 node，请先安装常驻 node 服务"
	}
	return m.reason == "", m.reason
}

func (m *Manager) available() error {
	if ok, reason := m.Capability(); !ok {
		return errors.New(reason)
	}
	return nil
}

func (m *Manager) Begin(p proto.NodeUpdateBeginParams) (proto.NodeUpdateBeginResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.available(); err != nil {
		return proto.NodeUpdateBeginResult{}, err
	}
	sum, err := hex.DecodeString(p.SHA256)
	if p.Size <= 0 || p.Size > MaxSize || err != nil || len(sum) != sha256.Size || strings.TrimSpace(p.Version) == "" || len(p.Version) > 256 {
		return proto.NodeUpdateBeginResult{}, errors.New("invalid node executable size, sha256, or version")
	}
	p.SHA256 = strings.ToLower(p.SHA256)
	m.expireLocked()
	if u := m.upload; u != nil {
		if u.Spec == p {
			return proto.NodeUpdateBeginResult{ID: u.ID, Offset: u.Offset}, nil
		}
		return proto.NodeUpdateBeginResult{}, errors.New("another node update is in progress")
	}
	// The startup rollback barrier identifies loaded code by version. Refuse
	// reinstalling different bytes under the running version's same identity.
	if p.Version == m.version {
		return proto.NodeUpdateBeginResult{}, errors.New("node already runs the target version")
	}
	if _, err := os.Stat(markerPath(m.exe)); err == nil {
		return proto.NodeUpdateBeginResult{}, errors.New("a node update is awaiting confirmation")
	} else if !os.IsNotExist(err) {
		return proto.NodeUpdateBeginResult{}, err
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return proto.NodeUpdateBeginResult{}, err
	}
	f, err := os.CreateTemp(filepath.Dir(m.exe), ".node-update-stage-*")
	if err != nil {
		return proto.NodeUpdateBeginResult{}, err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return proto.NodeUpdateBeginResult{}, err
	}
	m.upload = &upload{ID: hex.EncodeToString(random[:]), Spec: p, Path: path, At: time.Now()}
	m.armExpiryLocked()
	return proto.NodeUpdateBeginResult{ID: m.upload.ID}, nil
}

func (m *Manager) Chunk(p proto.NodeUpdateChunkParams) (proto.NodeUpdateChunkResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	u := m.upload
	if u == nil || u.ID != p.ID {
		return proto.NodeUpdateChunkResult{}, errors.New("unknown or expired node upload")
	}
	if u.Accepted {
		return proto.NodeUpdateChunkResult{}, errors.New("node upload already committed")
	}
	if len(p.Data) == 0 || len(p.Data) > MaxChunk || p.Offset < 0 || p.Offset > u.Offset || int64(len(p.Data)) > u.Spec.Size-p.Offset {
		return proto.NodeUpdateChunkResult{}, errors.New("invalid node chunk size or offset")
	}
	f, err := os.OpenFile(u.Path, os.O_RDWR, 0)
	if err != nil {
		return proto.NodeUpdateChunkResult{}, err
	}
	defer f.Close()
	if p.Offset < u.Offset {
		if p.Offset+int64(len(p.Data)) > u.Offset {
			return proto.NodeUpdateChunkResult{}, errors.New("overlapping node chunk")
		}
		prior := make([]byte, len(p.Data))
		if _, err := f.ReadAt(prior, p.Offset); err != nil {
			return proto.NodeUpdateChunkResult{}, err
		}
		if !bytes.Equal(prior, p.Data) {
			return proto.NodeUpdateChunkResult{}, errors.New("retried node chunk differs")
		}
	} else {
		n, err := f.WriteAt(p.Data, p.Offset)
		u.Offset += int64(n)
		if err != nil {
			return proto.NodeUpdateChunkResult{}, err
		}
	}
	u.At = time.Now()
	m.armExpiryLocked()
	return proto.NodeUpdateChunkResult{Offset: u.Offset}, nil
}

// Commit checks the executable and activity before touching the installed file.
// The caller must invoke ResponseWritten only after successfully writing its RPC reply.
func (m *Manager) Commit(ctx context.Context, id string, idle func() error) (proto.NodeUpdateCommitResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	u := m.upload
	if u == nil || u.ID != id {
		return proto.NodeUpdateCommitResult{}, errors.New("unknown or expired node upload")
	}
	result := proto.NodeUpdateCommitResult{ID: id, Accepted: true}
	if u.Accepted {
		return result, nil
	}
	if u.Offset != u.Spec.Size {
		return proto.NodeUpdateCommitResult{}, errors.New("node executable upload is incomplete")
	}
	// The hub sends the kind of program the node runs (devices/deploy.go nodeBinary); this keeps a personad that may
	// run a hub or a manager too from becoming persona-node all the same.
	if err := update.CheckReplace(m.exe, u.Path); err != nil {
		return proto.NodeUpdateCommitResult{}, err
	}
	if err := validate(ctx, u.Path, u.Spec); err != nil {
		return proto.NodeUpdateCommitResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return proto.NodeUpdateCommitResult{}, err
	}
	err := locked(m.exe, func() error {
		if _, err := os.Stat(markerPath(m.exe)); err == nil {
			return errors.New("a node update is awaiting confirmation")
		} else if !os.IsNotExist(err) {
			return err
		}
		if m.identity != nil {
			current, err := os.Stat(m.exe)
			if err != nil {
				return err
			}
			if !os.SameFile(m.identity, current) {
				return errSuperseded
			}
		}
		oldHash, err := fileHash(m.exe)
		if err != nil {
			return err
		}
		backup := m.exe + ".node-update-backup-" + id
		if err := copyFile(m.exe, backup); err != nil {
			return err
		}
		birth, err := processStart(os.Getpid())
		if err != nil {
			_ = os.Remove(backup)
			return err
		}
		p := pending{ID: id, Exe: m.exe, Backup: backup, Stage: u.Path, Version: u.Spec.Version, From: m.version, OldHash: oldHash, NewHash: u.Spec.SHA256, PID: os.Getpid(), ProcessStart: birth, ProcessPrecise: osproc.Identify(os.Getpid()).Start, Deadline: time.Now().Add(m.timeout)}
		if err := savePending(p); err != nil {
			_ = os.Remove(backup)
			return err
		}
		if err := m.startWatch(backup, markerPath(m.exe), id); err != nil {
			_ = os.Remove(markerPath(m.exe))
			_ = os.Remove(backup)
			return fmt.Errorf("start node rollback watchdog: %w", err)
		}
		// Recheck after self-check, backup, and watchdog startup: any of them can
		// take long enough for an agent to become active again.
		if err := ctx.Err(); err != nil {
			_ = cleanupPendingOnly(p)
			return err
		}
		if idle != nil {
			if err := idle(); err != nil {
				_ = cleanupPendingOnly(p)
				return err
			}
		}
		// The directory lock orders Persona's own writers, but an external
		// deployment (a plain copy, a package manager) never takes it. The swap
		// lands only while the installed file is still the one this transaction
		// examined, checked through one descriptor right before the rename.
		if err := renameStill(u.Path, m.exe, oldHash); err != nil {
			_ = cleanupPendingOnly(p)
			return err
		}
		// Once renamed, keep the marker and backup even if directory fsync fails:
		// the watchdog can still restore the old executable.
		return syncDir(filepath.Dir(m.exe))
	})
	if err != nil {
		return proto.NodeUpdateCommitResult{}, err
	}
	u.Accepted = true
	if m.timer != nil {
		m.timer.Stop()
	}
	return result, nil
}

// ResponseWritten signals the whole resident, at most once, after the acceptance
// was written to the transport. Failed writes leave the watchdog to restore it.
func (m *Manager) ResponseWritten(id string) {
	m.mu.Lock()
	u := m.upload
	if u == nil || u.ID != id || !u.Accepted || u.Restarted {
		m.mu.Unlock()
		return
	}
	u.Restarted = true
	restart := m.restart
	m.mu.Unlock()
	restart()
}

func (m *Manager) Abort(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.upload == nil || m.upload.ID != id {
		return nil
	}
	if m.upload.Accepted {
		return errors.New("node update already committed")
	}
	m.clearLocked()
	return nil
}

// Confirm is safe on every exact-version reconnect, including after a hub restart.
func (m *Manager) Confirm(version string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.available(); err != nil {
		return err
	}
	if version != m.version {
		return errors.New("confirmation does not match the running node version")
	}
	return locked(m.exe, func() error {
		p, err := loadPending(markerPath(m.exe))
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if p.Version != version {
			return errors.New("confirmation does not match pending node version")
		}
		if !time.Now().Before(p.Deadline) {
			return errors.New("node update confirmation deadline expired")
		}
		sum, err := fileHash(m.exe)
		if err != nil {
			return err
		}
		if sum != p.NewHash {
			return errors.New("running node executable no longer matches update")
		}
		if err := os.Remove(markerPath(m.exe)); err != nil {
			return err
		}
		_ = os.Remove(p.Backup)
		_ = os.Remove(p.Stage)
		return syncDir(filepath.Dir(m.exe))
	})
}

// Close removes abandoned uploads; an accepted transaction belongs to the watchdog.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	if m.sweep != nil {
		m.sweep.Stop()
	}
	if m.timer != nil {
		m.timer.Stop()
	}
	if m.upload != nil && !m.upload.Accepted {
		m.clearLocked()
	}
}

func (m *Manager) expireLocked() {
	if u := m.upload; u != nil && !u.Accepted && time.Since(u.At) >= UploadTTL {
		m.clearLocked()
	}
}
func (m *Manager) clearLocked() {
	if m.timer != nil {
		m.timer.Stop()
	}
	if m.upload != nil {
		_ = os.Remove(m.upload.Path)
	}
	m.upload = nil
}
func (m *Manager) armExpiryLocked() {
	if m.timer != nil {
		m.timer.Stop()
	}
	m.timer = time.AfterFunc(UploadTTL, func() { m.mu.Lock(); defer m.mu.Unlock(); m.expireLocked() })
}

// Sweep uploads left by a crashed process too, even when this resident stays up
// for weeks. Its own active upload is governed by the activity-based TTL.
func (m *Manager) armSweepLocked() {
	m.sweep = time.AfterFunc(UploadTTL, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.closed {
			return
		}
		m.expireLocked()
		keep := ""
		if m.upload != nil {
			keep = m.upload.Path
		}
		_ = m.cleanStaleStages(keep)
		m.armSweepLocked()
	})
}

func validate(ctx context.Context, path string, spec proto.NodeUpdateBeginParams) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if st.Size() != spec.Size {
		return errors.New("node executable size mismatch")
	}
	sum, err := fileHash(path)
	if err != nil {
		return err
	}
	if sum != spec.SHA256 {
		return errors.New("node executable sha256 mismatch")
	}
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return fmt.Errorf("node executable build info: %w", err)
	}
	settings := map[string]string{}
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if settings["GOOS"] != runtime.GOOS || settings["GOARCH"] != runtime.GOARCH {
		return errors.New("node executable platform or architecture mismatch")
	}
	if err := os.Chmod(path, 0o755); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "node-update-check")
	var output limitedBuffer
	cmd.Stdout, cmd.Stderr = &output, io.Discard
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("node executable self-check: %w", err)
	}
	var check Check
	if err := json.Unmarshal(output.Bytes(), &check); err != nil {
		return fmt.Errorf("node executable self-check output: %w", err)
	}
	if check.Version != spec.Version || check.OS != runtime.GOOS || check.Arch != runtime.GOARCH {
		return errors.New("node executable self-check version or platform mismatch")
	}
	// Reopening an already executed signed Mach-O file for writing can make
	// macOS kill a subsequent self-check. Validation never needs write access.
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, errors.New("self-check output exceeds limit")
	}
	return b.Buffer.Write(p)
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// errSuperseded marks a swap that stopped because the installed file was
// replaced by a deployment this transaction did not stage: the file on disk is
// someone else's now and must be left in place, the transaction cleaned up.
var errSuperseded = errors.New("node executable was replaced by another deployment")

// renameStill replaces dst with src only while dst is still the file whose
// sha256 is want. The file's inode, size and modification time are snapshotted
// before and rechecked after the hash is read through one open descriptor, and
// once more against the path immediately before the rename, so a deployment
// that renamed dst into place or rewrote it in place (cp never takes the
// directory lock) between those points is detected, not overwritten. The
// window that remains is the one between the last check and the rename, two
// adjacent syscalls.
func renameStill(src, dst, want string) error {
	f, err := os.Open(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return err
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return err
	}
	if hex.EncodeToString(sum.Sum(nil)) != want {
		return errSuperseded
	}
	after, err := f.Stat()
	if err != nil {
		return err
	}
	if !after.ModTime().Equal(before.ModTime()) || after.Size() != before.Size() {
		return errSuperseded
	}
	now, err := os.Stat(dst)
	if err != nil {
		return err
	}
	if !os.SameFile(after, now) || !now.ModTime().Equal(after.ModTime()) || now.Size() != after.Size() {
		return errSuperseded
	}
	return os.Rename(src, dst)
}
func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return out.Close()
}
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// locked runs fn holding the lock on the executable's directory (lockDir).
func locked(exe string, fn func() error) error {
	unlock, err := lockDir(filepath.Dir(exe))
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}
