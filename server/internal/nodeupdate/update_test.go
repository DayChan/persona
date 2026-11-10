package nodeupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/update/updatetest"
)

var fixtureDir string
var fixtureOnce sync.Once
var fixtureErr error

func TestMain(m *testing.M) {
	var err error
	fixtureDir, err = os.MkdirTemp("", "node-update-fixtures-")
	if err != nil {
		panic(err)
	}
	// Keep all fixture processes away from the developer's runtime roots. Reuse
	// only Go's build/module caches so isolated HOME does not redownload tools.
	for _, key := range []string{"GOCACHE", "GOMODCACHE"} {
		if os.Getenv(key) == "" {
			if out, err := exec.Command("go", "env", key).Output(); err == nil {
				_ = os.Setenv(key, strings.TrimSpace(string(out)))
			}
		}
	}
	_ = os.Setenv("HOME", filepath.Join(fixtureDir, "home"))
	_ = os.Setenv("PERSONA_HOME", filepath.Join(fixtureDir, "runtime"))
	_ = os.Setenv("PERSONA_HOME", filepath.Join(fixtureDir, "runtime"))
	_ = os.MkdirAll(os.Getenv("HOME"), 0700)
	_ = os.Setenv("INVOCATION_ID", "")
	_ = os.Setenv("XPC_SERVICE_NAME", "")
	// Never contact the host's service manager, even on Linux CI. This fake
	// launches the real watchdog process but records the transient-unit command.
	fakeBin := filepath.Join(fixtureDir, "fake-bin")
	_ = os.MkdirAll(fakeBin, 0700)
	fake := "#!/bin/sh\nif [ -n \"$NODE_UPDATE_FAKE_SYSTEMD_LOG\" ]; then printf '%s\\n' \"$@\" >\"$NODE_UPDATE_FAKE_SYSTEMD_LOG\"; fi\nwhile [ \"$#\" -gt 0 ] && [ \"$1\" != \"--\" ]; do shift; done\n[ \"$#\" -gt 0 ] || exit 2\nshift\n\"$@\" </dev/null >/dev/null 2>&1 &\n"
	_ = os.WriteFile(filepath.Join(fakeBin, "systemd-run"), []byte(fake), 0700)
	_ = os.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	code := m.Run()
	_ = os.RemoveAll(fixtureDir)
	os.Exit(code)
}
func fixtures(t *testing.T) (string, string) {
	t.Helper()
	fixtureOnce.Do(func() {
		for _, version := range []string{"old", "next"} {
			cmd := exec.Command("go", "build", "-o", filepath.Join(fixtureDir, version), "-ldflags=-X main.version="+version, "./testdata")
			if out, err := cmd.CombinedOutput(); err != nil {
				fixtureErr = fmt.Errorf("build fixture: %w: %s", err, out)
				return
			}
		}
	})
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	return filepath.Join(fixtureDir, "old"), filepath.Join(fixtureDir, "next")
}
func testManager(t *testing.T) (*Manager, []byte, *atomic.Int32) {
	t.Helper()
	old, next := fixtures(t)
	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "resident")
	if err := copyFile(old, exe); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(next)
	if err != nil {
		t.Fatal(err)
	}
	count := new(atomic.Int32)
	m := &Manager{exe: exe, version: "old", timeout: time.Hour, restart: func() { count.Add(1) }, startWatch: func(string, string, string) error { return nil }}
	t.Cleanup(m.Close)
	return m, data, count
}
func spec(data []byte, version string) proto.NodeUpdateBeginParams {
	sum := sha256.Sum256(data)
	return proto.NodeUpdateBeginParams{Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Version: version}
}
func receive(t *testing.T, m *Manager, data []byte, p proto.NodeUpdateBeginParams) string {
	t.Helper()
	b, err := m.Begin(p)
	if err != nil {
		t.Fatal(err)
	}
	for offset := b.Offset; offset < int64(len(data)); {
		end := min(offset+MaxChunk, int64(len(data)))
		c, err := m.Chunk(proto.NodeUpdateChunkParams{ID: b.ID, Offset: offset, Data: data[offset:end]})
		if err != nil {
			t.Fatal(err)
		}
		offset = c.Offset
	}
	return b.ID
}
func hash(t *testing.T, path string) string {
	t.Helper()
	s, err := fileHash(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestVerifiedCommitResponseAndExactConfirmation(t *testing.T) {
	m, data, restarts := testManager(t)
	oldHash := hash(t, m.exe)
	id := receive(t, m, data, spec(data, "next"))
	first, err := m.Commit(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Accepted || first.ID != id || restarts.Load() != 0 {
		t.Fatal("commit restarted before a response was written")
	}
	p, err := loadPending(markerPath(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	if hash(t, p.Backup) != oldHash || hash(t, m.exe) != spec(data, "next").SHA256 {
		t.Fatal("backup or installed executable differs")
	}
	again, err := m.Commit(context.Background(), id, nil)
	if err != nil || again != first {
		t.Fatalf("commit retry: %+v %v", again, err)
	}
	m.ResponseWritten("wrong")
	m.ResponseWritten(id)
	m.ResponseWritten(id)
	if restarts.Load() != 1 {
		t.Fatal("resident restart must happen exactly once")
	}
	if err := m.Confirm("next"); err == nil {
		t.Fatal("old running process confirmed new executable")
	}
	running := &Manager{exe: m.exe, version: "next", startWatch: func(string, string, string) error { return nil }}
	if err := running.recover(); err != nil {
		t.Fatal(err)
	}
	if err := running.Confirm("old"); err == nil {
		t.Fatal("wrong version confirmed")
	}
	if err := running.Confirm("next"); err != nil {
		t.Fatal(err)
	}
	if err := running.Confirm("next"); err != nil {
		t.Fatal("confirmation retry", err)
	}
	if _, err := os.Stat(p.Backup); !os.IsNotExist(err) {
		t.Fatal("confirmed backup remains")
	}
}

func TestInvalidArtifactsNeverReplaceInstalledFile(t *testing.T) {
	_, next := fixtures(t)
	good, err := os.ReadFile(next)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
		p    proto.NodeUpdateBeginParams
	}{
		{"checksum", good, proto.NodeUpdateBeginParams{Size: int64(len(good)), SHA256: strings.Repeat("0", 64), Version: "next"}},
		{"truncated", good[:len(good)/2], spec(good, "next")},
		{"wrong-version", good, spec(good, "other")},
		{"not-executable", []byte("plain text"), spec([]byte("plain text"), "next")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _ := testManager(t)
			before := hash(t, m.exe)
			id := receive(t, m, tc.data, tc.p)
			if _, err := m.Commit(context.Background(), id, nil); err == nil {
				t.Fatal("accepted invalid artifact")
			}
			if hash(t, m.exe) != before {
				t.Fatal("invalid artifact replaced installed executable")
			}
			if _, err := os.Stat(markerPath(m.exe)); !os.IsNotExist(err) {
				t.Fatal("invalid artifact armed rollback transaction")
			}
		})
	}
	for _, target := range []struct{ os, arch string }{{"linux", runtime.GOARCH}, {runtime.GOOS, "amd64"}} {
		if target.os == runtime.GOOS && target.arch == runtime.GOARCH {
			target.arch = "arm64"
		}
		if target.os == runtime.GOOS && target.arch == runtime.GOARCH {
			continue
		}
		t.Run("foreign-"+target.os+"-"+target.arch, func(t *testing.T) {
			// A tiny real Go executable retains build metadata for platform rejection.
			dir := t.TempDir()
			src := filepath.Join(dir, "main.go")
			out := filepath.Join(dir, "foreign")
			if err := os.WriteFile(src, []byte("package main\nfunc main() {}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "build", "-o", out, src)
			cmd.Env = append(os.Environ(), "GOOS="+target.os, "GOARCH="+target.arch, "CGO_ENABLED=0")
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("build foreign fixture: %v %s", err, b)
			}
			data, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			m, _, _ := testManager(t)
			before := hash(t, m.exe)
			id := receive(t, m, data, spec(data, "next"))
			if _, err := m.Commit(context.Background(), id, nil); err == nil || !strings.Contains(err.Error(), "platform or architecture") {
				t.Fatalf("foreign executable: %v", err)
			}
			if hash(t, m.exe) != before {
				t.Fatal("foreign executable replaced working file")
			}
		})
	}
}

// A personad (it may run a hub or a manager too) never becomes persona-node through an update, whatever the hub sends.
func TestPersonaNodeNeverReplacesPersonad(t *testing.T) {
	out := updatetest.Program(t, "persona-node", "", "")
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	m, _, _ := testManager(t) // the resident is the fixture, a build of another package: personad
	before := hash(t, m.exe)
	id := receive(t, m, data, spec(data, "next"))
	if _, err := m.Commit(context.Background(), id, nil); err == nil || !strings.Contains(err.Error(), "not replacing it with persona-node") {
		t.Fatalf("persona-node over personad: %v", err)
	}
	if hash(t, m.exe) != before {
		t.Fatal("persona-node replaced personad")
	}
}

func TestResumeRetriesLimitsConcurrencyAndCancellation(t *testing.T) {
	m, data, _ := testManager(t)
	p := spec(data, "next")
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := m.Begin(p)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- b.ID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("concurrent begin created two uploads")
		}
		id = got
	}
	other := p
	other.Version = "another"
	if _, err := m.Begin(other); err == nil {
		t.Fatal("accepted concurrent different update")
	}
	chunk := data[:MaxChunk]
	if _, err := m.Chunk(proto.NodeUpdateChunkParams{ID: id, Data: chunk}); err != nil {
		t.Fatal(err)
	}
	b, err := m.Begin(p)
	if err != nil || b.ID != id || b.Offset != MaxChunk {
		t.Fatalf("resume %+v %v", b, err)
	}
	if c, err := m.Chunk(proto.NodeUpdateChunkParams{ID: id, Data: chunk}); err != nil || c.Offset != MaxChunk {
		t.Fatal("duplicate chunk", c, err)
	}
	changed := bytes.Clone(chunk)
	changed[0] ^= 1
	if _, err := m.Chunk(proto.NodeUpdateChunkParams{ID: id, Data: changed}); err == nil {
		t.Fatal("conflicting retry accepted")
	}
	if _, err := m.Chunk(proto.NodeUpdateChunkParams{ID: id, Offset: MaxChunk + 1, Data: chunk}); err == nil {
		t.Fatal("offset gap accepted")
	}
	if _, err := m.Chunk(proto.NodeUpdateChunkParams{ID: id, Offset: MaxChunk, Data: make([]byte, MaxChunk+1)}); err == nil {
		t.Fatal("oversized chunk accepted")
	}
	path := m.upload.Path
	if err := m.Abort(id); err != nil {
		t.Fatal(err)
	}
	if err := m.Abort(id); err != nil {
		t.Fatal("abort retry", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("abort left staged executable")
	}
	if _, err := m.Chunk(proto.NodeUpdateChunkParams{ID: id, Data: chunk}); err == nil {
		t.Fatal("aborted upload accepted data")
	}
	p.Size = MaxSize + 1
	if _, err := m.Begin(p); err == nil {
		t.Fatal("unbounded upload accepted")
	}
}

func TestSameVersionCannotHideDifferentLoadedCodeFromRollbackBarrier(t *testing.T) {
	m, data, _ := testManager(t)
	if _, err := m.Begin(spec(data, "old")); err == nil {
		t.Fatal("same-version reinstall would make loaded-code rollback identity ambiguous")
	}
}

func TestBusyAndCanceledCommitRetainRetryableUpload(t *testing.T) {
	m, data, _ := testManager(t)
	before := hash(t, m.exe)
	id := receive(t, m, data, spec(data, "next"))
	busy := &rpc.Error{Code: proto.CodeNodeUpdateBusy, Message: "busy"}
	if _, err := m.Commit(context.Background(), id, func() error { return busy }); !errors.Is(err, busy) {
		t.Fatal(err)
	}
	if hash(t, m.exe) != before {
		t.Fatal("busy node replaced executable")
	}
	if _, err := os.Stat(m.upload.Path); err != nil {
		t.Fatal("busy commit discarded retryable upload", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Commit(ctx, id, nil); err == nil {
		t.Fatal("canceled commit accepted")
	}
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal("retry failed", err)
	}
}

func TestExpiryAndCloseCleanStages(t *testing.T) {
	m, data, _ := testManager(t)
	b, err := m.Begin(spec(data, "next"))
	if err != nil {
		t.Fatal(err)
	}
	path := m.upload.Path
	m.upload.At = time.Now().Add(-UploadTTL - time.Second)
	if _, err := m.Chunk(proto.NodeUpdateChunkParams{ID: b.ID, Data: data[:1]}); err == nil {
		t.Fatal("expired upload accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("expired stage remains")
	}
	if _, err := m.Begin(spec(data, "next")); err != nil {
		t.Fatal(err)
	}
	path = m.upload.Path
	m.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("closed resident left stage")
	}
}

func TestWatchdogTimeoutRestoresAndStopsFailedProcess(t *testing.T) {
	m, data, _ := testManager(t)
	oldHash := hash(t, m.exe)
	id := receive(t, m, data, spec(data, "next"))
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	child := exec.Command("sh", "-c", "exec sleep 30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	p, err := loadPending(markerPath(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	p.PID = child.Process.Pid
	p.ProcessStart, err = processStart(p.PID)
	if err != nil {
		t.Fatal(err)
	}
	p.ProcessPrecise = osproc.Identify(p.PID).Start
	p.Deadline = time.Now().Add(-time.Second)
	if err := savePending(p); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Watch(ctx, markerPath(m.exe), id, nil); err != nil {
		t.Fatal(err)
	}
	if hash(t, m.exe) != oldHash {
		t.Fatal("watchdog failed to restore old executable")
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("failed node was not terminated")
		}
	case <-ctx.Done():
		t.Fatal("watchdog did not stop failed node")
	}
}

func replaceExternally(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.CreateTemp(filepath.Dir(path), ".external-deploy-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Chmod(0755); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.Name(), path); err != nil {
		t.Fatal(err)
	}
}

func TestStartupCrashRecoveryAndExternalReplacement(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprint(external), func(t *testing.T) {
			m, data, _ := testManager(t)
			oldHash := hash(t, m.exe)
			id := receive(t, m, data, spec(data, "next"))
			if _, err := m.Commit(context.Background(), id, nil); err != nil {
				t.Fatal(err)
			}
			p, err := loadPending(markerPath(m.exe))
			if err != nil {
				t.Fatal(err)
			}
			p.Deadline = time.Now().Add(-time.Second)
			if err := savePending(p); err != nil {
				t.Fatal(err)
			}
			if external {
				replaceExternally(t, m.exe, []byte("independent deployment"))
			}
			fresh := &Manager{exe: m.exe, version: "next", startWatch: func(string, string, string) error { return nil }}
			if err := fresh.recover(); err != nil {
				t.Fatal(err)
			}
			if external {
				got, err := os.ReadFile(m.exe)
				if err != nil || string(got) != "independent deployment" || hash(t, m.exe) == oldHash || fresh.RestartRequired() {
					t.Fatalf("overwrote external deployment: %q, %v", got, err)
				}
			} else {
				if hash(t, m.exe) != oldHash || !fresh.RestartRequired() {
					t.Fatal("startup did not restore and require re-exec")
				}
			}
		})
	}
}

func TestSupervisorReplacementLoadedBeforeRollbackMustExecRestoredBinary(t *testing.T) {
	m, data, _ := testManager(t)
	id := receive(t, m, data, spec(data, "next"))
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("/tmp", "nu-overlap-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(dir, "gate")
	proof := filepath.Join(dir, "proof")
	// This replacement has already loaded new code, but is paused before New
	// can publish its PID, exactly overlapping the independent rollback.
	cmd := exec.Command(m.exe, "node", "--attach")
	cmd.Env = append(os.Environ(), "HOME="+dir, "PERSONA_HOME="+dir, "INVOCATION_ID=fake-service", "XPC_SERVICE_NAME=fake-service", "FIXTURE_PROOF="+proof, "FIXTURE_GATE_VERSION=next", "FIXTURE_BOOT_GATE="+gate)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	waitFor(t, 5*time.Second, func() bool { b, _ := os.ReadFile(gate + ".loaded"); return string(b) == "next" })
	p, err := loadPending(markerPath(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	p.ProcessStart = "the previously crashed process"
	p.ProcessPrecise = "not the test process"
	p.Deadline = time.Now().Add(-time.Second)
	if err := savePending(p); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Watch(ctx, markerPath(m.exe), id, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(markerPath(m.exe)); err != nil {
		t.Fatal("rollback erased replacement-startup barrier", err)
	}
	if err := os.WriteFile(gate, []byte("continue"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { b, _ := os.ReadFile(proof); return bytes.Contains(b, []byte(`"version":"old"`)) })
	b, err := os.ReadFile(proof)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		PID     int
		Version string
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.PID != cmd.Process.Pid || got.Version != "old" {
		t.Fatal("replacement served new code after rollback")
	}
	if _, err := os.Stat(markerPath(m.exe)); !os.IsNotExist(err) {
		t.Fatal("restored old startup did not clear rollback barrier")
	}
}

func TestRealIndependentWatchdogSurvivesNodeStartupCrash(t *testing.T) {
	m, data, _ := testManager(t)
	before := hash(t, m.exe)
	m.startWatch = startWatchdog
	m.timeout = time.Hour
	id := receive(t, m, data, spec(data, "next"))
	// Use an already dead process identity so the real detached watchdog cannot
	// touch the Go test process. The new executable still fails to confirm.
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	err := locked(m.exe, func() error {
		p, err := loadPending(markerPath(m.exe))
		if err != nil {
			return err
		}
		p.ProcessStart = "not the test process"
		p.ProcessPrecise = "not the test process"
		p.Deadline = time.Now().Add(100 * time.Millisecond)
		return savePending(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { sum, _ := fileHash(m.exe); return sum == before })
	if _, err := os.Stat(markerPath(m.exe)); err != nil {
		t.Fatal("watchdog discarded rollback startup barrier", err)
	}
	oldStartup := &Manager{exe: m.exe, version: "old"}
	if err := oldStartup.recover(); err != nil {
		t.Fatal(err)
	}
	if hash(t, m.exe) != before {
		t.Fatal("independent old-binary watchdog failed rollback")
	}
}

func TestManagedWatchdogUsesIndependentTransientService(t *testing.T) {
	m, data, _ := testManager(t)
	log := filepath.Join(t.TempDir(), "systemd-command")
	t.Setenv("NODE_UPDATE_FAKE_SYSTEMD_LOG", log)
	m.startWatch = startSystemdWatchdog
	id := receive(t, m, data, spec(data, "next"))
	if _, err := m.Commit(context.Background(), id, nil); err != nil {
		t.Fatal(err)
	}
	p, err := loadPending(markerPath(m.exe))
	if err != nil {
		t.Fatal(err)
	}
	if p.WatchPID < 2 || p.WatchStart == "" {
		t.Fatal("transient watchdog did not acknowledge its own process identity")
	}
	args, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--user\n", "--collect\n", "--no-block\n", "--property=Type=exec\n", "--unit=persona-node-update-" + id, "--managed-ready"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("missing independent-service argument %q in %s", want, args)
		}
	}
	fresh := &Manager{exe: m.exe, version: "next"}
	if err := fresh.Confirm("next"); err != nil {
		t.Fatal(err)
	}
	if err := WatchManaged(context.Background(), markerPath(m.exe), id); !os.IsNotExist(err) {
		t.Fatalf("late managed startup: %v", err)
	}
	if _, err := os.Stat(markerPath(m.exe)); !os.IsNotExist(err) {
		t.Fatal("late managed startup recreated canceled transaction")
	}
}

func TestProcessIdentityIgnoresResidentAndWatchdogTimezone(t *testing.T) {
	t.Setenv("TZ", "Pacific/Honolulu")
	a, err := processStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", "Asia/Tokyo")
	b, err := processStart(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if a == "" || a != b {
		t.Fatalf("process identity changed with service timezone: %q != %q", a, b)
	}
}

func TestSupervisedProcessExecPreservesSymlinkArgsEnvironmentAndRoot(t *testing.T) {
	old, next := fixtures(t)
	dir, err := os.MkdirTemp("/tmp", "nu-process-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	root := filepath.Join(dir, "custom-root")
	binDir := filepath.Join(dir, "bin")
	for _, p := range []string{home, root, binDir} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(binDir, "resident")
	if err := copyFile(old, exe); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "via-symlink")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	proof := filepath.Join(dir, "proof.json")
	cmd := exec.Command(link, "node", "--attach", "kept-argument")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home, "PERSONA_HOME="+root, "INVOCATION_ID=fake-service", "XPC_SERVICE_NAME=fake-service", "FIXTURE_PROOF="+proof, "FIXTURE_SENTINEL=retained")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	path := filepath.Join(root, "fixture.sock")
	waitFor(t, 5*time.Second, func() bool { b, _ := os.ReadFile(proof); return bytes.Contains(b, []byte(`"version":"old"`)) })
	peer, c := fixturePeer(t, path)
	defer c.Close()
	defer peer.Close(io.EOF)
	data, err := os.ReadFile(next)
	if err != nil {
		t.Fatal(err)
	}
	var begin proto.NodeUpdateBeginResult
	call(t, peer, "begin", spec(data, "next"), &begin)
	// Reconnect midway through upload: the resident retains the same manager.
	cut := min(MaxChunk, len(data))
	var chunk proto.NodeUpdateChunkResult
	call(t, peer, "chunk", proto.NodeUpdateChunkParams{ID: begin.ID, Data: data[:cut]}, &chunk)
	_ = c.Close()
	peer.Close(io.EOF)
	peer, c = fixturePeer(t, path)
	defer c.Close()
	defer peer.Close(io.EOF)
	var resumed proto.NodeUpdateBeginResult
	call(t, peer, "begin", spec(data, "next"), &resumed)
	if resumed.ID != begin.ID || resumed.Offset != int64(cut) {
		t.Fatal("reconnection lost staged upload")
	}
	for offset := resumed.Offset; offset < int64(len(data)); {
		end := min(offset+MaxChunk, int64(len(data)))
		call(t, peer, "chunk", proto.NodeUpdateChunkParams{ID: begin.ID, Offset: offset, Data: data[offset:end]}, &chunk)
		offset = chunk.Offset
	}
	var accepted proto.NodeUpdateCommitResult
	call(t, peer, "commit", proto.NodeUpdateCommitParams{ID: begin.ID}, &accepted)
	if !accepted.Accepted {
		t.Fatal("commit not accepted")
	}
	before, _ := os.ReadFile(proof)
	if !bytes.Contains(before, []byte(`"version":"old"`)) {
		t.Fatal("process restarted before response acknowledgement")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = peer.Call(ctx, "response-written", proto.NodeUpdateCommitParams{ID: begin.ID}, nil)
	waitFor(t, 8*time.Second, func() bool { b, _ := os.ReadFile(proof); return bytes.Contains(b, []byte(`"version":"next"`)) })
	var got struct {
		Version                   string
		PID                       int
		Args                      []string
		Cwd, Home, Root, Sentinel string
	}
	b, err := os.ReadFile(proof)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.PID != cmd.Process.Pid || got.Cwd != dir || got.Home != home || got.Root != root || got.Sentinel != "retained" || len(got.Args) != 4 || got.Args[0] != link || got.Args[3] != "kept-argument" {
		t.Fatalf("exec did not preserve resident context: %+v", got)
	}
	peer, c = fixturePeer(t, path)
	defer c.Close()
	defer peer.Close(io.EOF)
	call(t, peer, "confirm", proto.NodeUpdateConfirmParams{Version: "next"}, nil)
	if _, err := os.Stat(markerPath(exe)); !os.IsNotExist(err) {
		t.Fatal("pending transaction not confirmed")
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatal("updater replaced symlink instead of its target")
	}
}

func fixturePeer(t *testing.T, path string) (*rpc.Peer, net.Conn) {
	t.Helper()
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	peer := rpc.New("test", c)
	go func() { _ = peer.Serve(context.Background(), c) }()
	return peer, c
}
func call(t *testing.T, p *rpc.Peer, method string, params, result any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := p.Call(ctx, method, params, result); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

// procState is pid's state and start time (fields 3 and 22 of /proc/<pid>/stat), both "" when it is gone or there is
// no /proc.
func procState(pid int) (state, start string) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", ""
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(f) < 20 {
		return "", ""
	}
	return f[0], f[19]
}

func waitFor(t *testing.T, timeout time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for fixture state")
}

func TestCapabilityRequiresResidentSupervisor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XPC_SERVICE_NAME", "")
	t.Setenv("INVOCATION_ID", "")
	if ok, reason := New("old", func() {}, Options{}).Capability(); ok || reason == "" {
		t.Fatal("unsupervised node advertised safe update")
	}
	var m *Manager
	if ok, reason := m.Capability(); ok || reason == "" {
		t.Fatal("stdio node advertised update")
	}
}

func TestNewProcessCrashAndMissingConfirmationRollbackWithoutHub(t *testing.T) {
	old, next := fixtures(t)
	for _, crash := range []bool{true, false} {
		t.Run(fmt.Sprintf("startup-crash=%v", crash), func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "nu-rollback-*")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			dir, err = filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(dir, "node")
			if err := copyFile(old, exe); err != nil {
				t.Fatal(err)
			}
			oldHash := hash(t, exe)
			proof := filepath.Join(dir, "proof")
			env := append(os.Environ(), "HOME="+dir, "PERSONA_HOME="+dir, "INVOCATION_ID=fake-service", "XPC_SERVICE_NAME=fake-service", "FIXTURE_PROOF="+proof)
			if crash {
				env = append(env, "FIXTURE_CRASH=next")
			}
			start := func() *exec.Cmd {
				cmd := exec.Command(exe, "node", "--attach")
				cmd.Env = env
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				return cmd
			}
			cmd := start()
			defer func() { _ = cmd.Process.Kill() }()
			waitFor(t, 5*time.Second, func() bool { b, _ := os.ReadFile(proof); return bytes.Contains(b, []byte(`"version":"old"`)) })
			peer, c := fixturePeer(t, filepath.Join(dir, "fixture.sock"))
			defer c.Close()
			defer peer.Close(io.EOF)
			data, err := os.ReadFile(next)
			if err != nil {
				t.Fatal(err)
			}
			var begin proto.NodeUpdateBeginResult
			call(t, peer, "begin", spec(data, "next"), &begin)
			for offset := int64(0); offset < int64(len(data)); {
				end := min(offset+MaxChunk, int64(len(data)))
				var got proto.NodeUpdateChunkResult
				call(t, peer, "chunk", proto.NodeUpdateChunkParams{ID: begin.ID, Offset: offset, Data: data[offset:end]}, &got)
				offset = got.Offset
			}
			call(t, peer, "commit", proto.NodeUpdateCommitParams{ID: begin.ID}, nil)
			if err := locked(exe, func() error {
				p, err := loadPending(markerPath(exe))
				if err != nil {
					return err
				}
				p.Deadline = time.Now().Add(800 * time.Millisecond)
				return savePending(p)
			}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = peer.Call(ctx, "response-written", proto.NodeUpdateCommitParams{ID: begin.ID}, nil)
			cancel()
			// The hub disappears here: no caller polls or confirms the new process.
			_ = c.Close()
			peer.Close(io.EOF)
			if !crash {
				waitFor(t, 5*time.Second, func() bool { b, _ := os.ReadFile(proof); return bytes.Contains(b, []byte(`"version":"next"`)) })
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("failed update exited successfully")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("watchdog did not terminate unconfirmed process")
			}
			waitFor(t, 5*time.Second, func() bool { sum, _ := fileHash(exe); return sum == oldHash })
			if hash(t, exe) != oldHash {
				t.Fatal("old executable was not restored")
			}
			// Model the existing supervisor's restart, without calling a real service.
			_ = os.Remove(proof)
			restored := start()
			defer func() { _ = restored.Process.Kill(); _ = restored.Wait() }()
			waitFor(t, 5*time.Second, func() bool { b, _ := os.ReadFile(proof); return bytes.Contains(b, []byte(`"version":"old"`)) })
		})
	}
}

// onlyPS makes PATH a folder with just ps: what a container without systemd has, and no systemd-run.
func onlyPS(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if script == "" {
		ps, err := exec.LookPath("ps")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(ps, filepath.Join(bin, "ps")); err != nil {
			t.Fatal(err)
		}
	} else if err := os.WriteFile(filepath.Join(bin, "ps"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func TestDeclaredSupervisorEnablesUpdatesWithoutServiceManager(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("a declared supervisor counts on Linux and macOS only")
	}
	t.Setenv("XPC_SERVICE_NAME", "")
	t.Setenv("INVOCATION_ID", "")
	onlyPS(t, "")
	declared := New("old", func() {}, Options{Supervised: true})
	t.Cleanup(declared.Close)
	if ok, reason := declared.Capability(); !ok {
		t.Fatalf("a node whose supervisor said so cannot take updates: %s", reason)
	}
	if declared.Executable() == "" {
		t.Fatal("no executable resolved")
	}
	undeclared := New("old", func() {}, Options{})
	t.Cleanup(undeclared.Close)
	if ok, _ := undeclared.Capability(); ok {
		t.Fatal("a node nobody restarts advertised safe update")
	}
}

func TestDeclaredSupervisorNeedsPsThatReportsStartTimes(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("a declared supervisor counts on Linux and macOS only")
	}
	t.Setenv("XPC_SERVICE_NAME", "")
	t.Setenv("INVOCATION_ID", "")
	// BusyBox's ps fails on -p / -o lstart; another may answer with nothing.
	for name, script := range map[string]string{
		"busybox": "#!/bin/sh\necho \"ps: invalid option -- 'p'\" >&2\nexit 1\n",
		"empty":   "#!/bin/sh\nexit 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			onlyPS(t, script)
			m := New("old", func() {}, Options{Supervised: true})
			t.Cleanup(m.Close)
			if ok, reason := m.Capability(); ok || reason != psNoStart {
				t.Fatalf("got %v %q, want the ps reason", ok, reason)
			}
		})
	}
	// The hub shows the node's reason in the app's language by matching this original text.
	if got := i18n.T(i18n.ZhHans, "devices-deploy.node_ps_no_start"); got != psNoStart {
		t.Fatalf("hub catalog has %q for the node's %q", got, psNoStart)
	}
}

// superviseFixture runs the fixture the way a container's entry script runs the node: `node --attach --supervised`,
// started again shortly after every exit, with neither systemd's nor launchd's environment.
type fixtureSupervisor struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	starts  int
	stopped bool
	done    chan struct{}
}

func superviseFixture(t *testing.T, exe string, env []string) *fixtureSupervisor {
	t.Helper()
	s := &fixtureSupervisor{done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			s.mu.Lock()
			if s.stopped {
				s.mu.Unlock()
				return
			}
			cmd := exec.Command(exe, "node", "--attach", "--supervised")
			cmd.Env = env
			if err := cmd.Start(); err != nil {
				s.mu.Unlock()
				t.Error(err)
				return
			}
			s.cmd, s.starts = cmd, s.starts+1
			s.mu.Unlock()
			_ = cmd.Wait()
			time.Sleep(50 * time.Millisecond) // sleep-ok: the supervisor's own restart delay, not a wait for state
		}
	}()
	t.Cleanup(func() {
		s.mu.Lock()
		s.stopped = true
		if s.cmd != nil {
			_ = s.cmd.Process.Kill()
		}
		s.mu.Unlock()
		<-s.done
	})
	return s
}

func (s *fixtureSupervisor) current() (pid, starts int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cmd.Process.Pid, s.starts
}

type fixtureProof struct {
	Version string
	PID     int
}

// proofOf reads what the running fixture wrote about itself; false while there is none (or it is being written).
func proofOf(path string) (fixtureProof, bool) {
	var p fixtureProof
	b, err := os.ReadFile(path)
	return p, err == nil && json.Unmarshal(b, &p) == nil
}

// A node whose supervisor is neither systemd nor launchd (a container's entry script) is updated the same way: the new
// program runs in place, and when it never confirms, the detached watchdog puts the old one back and stops it, and the
// supervisor's restart runs the old program again.
func TestDeclaredSupervisorUpdateConfirmsOrRollsBack(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("a declared supervisor counts on Linux and macOS only")
	}
	old, next := fixtures(t)
	data, err := os.ReadFile(next)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"confirmed", "unconfirmed", "startup-crash"} {
		t.Run(outcome, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "nu-declared-*")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(dir) })
			dir, err = filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(dir, "node")
			if err := copyFile(old, exe); err != nil {
				t.Fatal(err)
			}
			oldHash := hash(t, exe)
			proof := filepath.Join(dir, "proof")
			// The fake systemd-run on PATH records any call: a declared supervisor must not use it.
			systemdLog := filepath.Join(dir, "systemd-run-called")
			env := append(os.Environ(), "HOME="+dir, "PERSONA_HOME="+dir, "INVOCATION_ID=", "XPC_SERVICE_NAME=",
				"FIXTURE_PROOF="+proof, "NODE_UPDATE_FAKE_SYSTEMD_LOG="+systemdLog)
			if outcome == "startup-crash" {
				env = append(env, "FIXTURE_CRASH=next")
			}
			s := superviseFixture(t, exe, env)
			waitFor(t, 5*time.Second, func() bool { b, _ := os.ReadFile(proof); return bytes.Contains(b, []byte(`"version":"old"`)) })
			firstPID, _ := s.current()

			peer, c := fixturePeer(t, filepath.Join(dir, "fixture.sock"))
			var begin proto.NodeUpdateBeginResult
			call(t, peer, "begin", spec(data, "next"), &begin)
			for offset := int64(0); offset < int64(len(data)); {
				end := min(offset+MaxChunk, int64(len(data)))
				var got proto.NodeUpdateChunkResult
				call(t, peer, "chunk", proto.NodeUpdateChunkParams{ID: begin.ID, Offset: offset, Data: data[offset:end]}, &got)
				offset = got.Offset
			}
			var accepted proto.NodeUpdateCommitResult
			call(t, peer, "commit", proto.NodeUpdateCommitParams{ID: begin.ID}, &accepted)
			if !accepted.Accepted {
				t.Fatal("commit not accepted")
			}
			p, err := loadPending(markerPath(exe))
			if err != nil {
				t.Fatal(err)
			}
			if p.WatchPID < 2 || p.WatchStart == "" {
				t.Fatal("the detached watchdog did not record itself")
			}
			_, watchBirth := procState(p.WatchPID) // while it runs: the pid names it as long as this start does
			if outcome != "confirmed" {
				if err := locked(exe, func() error {
					p, err := loadPending(markerPath(exe))
					if err != nil {
						return err
					}
					p.Deadline = time.Now().Add(800 * time.Millisecond)
					return savePending(p)
				}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = peer.Call(ctx, "response-written", proto.NodeUpdateCommitParams{ID: begin.ID}, nil)
			cancel()
			_ = c.Close()
			peer.Close(io.EOF)

			switch outcome {
			case "confirmed":
				// The new program replaced the old one in the same process: the supervisor saw nothing exit.
				waitFor(t, 8*time.Second, func() bool { p, ok := proofOf(proof); return ok && p.Version == "next" })
				if got, _ := proofOf(proof); got.PID != firstPID {
					t.Fatalf("new version runs as pid %d, not in place of %d", got.PID, firstPID)
				}
				peer, c := fixturePeer(t, filepath.Join(dir, "fixture.sock"))
				call(t, peer, "confirm", proto.NodeUpdateConfirmParams{Version: "next"}, nil)
				_ = c.Close()
				peer.Close(io.EOF)
				if _, err := os.Stat(markerPath(exe)); !os.IsNotExist(err) {
					t.Fatal("pending transaction not confirmed")
				}
				if pid, starts := s.current(); pid != firstPID || starts != 1 {
					t.Fatalf("the supervisor restarted the node (%d starts)", starts)
				}
				// The watchdog the old image started is the new image's child, and exits now that the update is confirmed.
				// Only the new image can reap it (osproc.ReapInherited); unreaped, it stays a zombie as long as the node runs.
				if runtime.GOOS == "linux" {
					if watchBirth == "" {
						t.Fatalf("the watchdog %d did not run", p.WatchPID)
					}
					deadline := time.Now().Add(10 * time.Second)
					tick := time.NewTicker(20 * time.Millisecond)
					defer tick.Stop()
					for state, start := procState(p.WatchPID); start == watchBirth; state, start = procState(p.WatchPID) {
						if time.Now().After(deadline) {
							t.Fatalf("the watchdog %d was not reaped 10s after the exec: state %s", p.WatchPID, state)
						}
						<-tick.C
					}
				}
				data, err := os.ReadFile(next)
				if err != nil {
					t.Fatal(err)
				}
				if sum := sha256.Sum256(data); hash(t, exe) != hex.EncodeToString(sum[:]) {
					t.Fatal("installed file is not the new version")
				}
			default:
				// Nobody confirms: the watchdog restores the old file and stops the node; the supervisor's restart
				// runs the old program, which clears the transaction.
				if outcome == "unconfirmed" {
					waitFor(t, 5*time.Second, func() bool { p, ok := proofOf(proof); return ok && p.Version == "next" && p.PID == firstPID })
				}
				waitFor(t, 10*time.Second, func() bool {
					p, ok := proofOf(proof)
					pid, _ := s.current()
					return ok && p.Version == "old" && p.PID == pid && pid != firstPID
				})
				waitFor(t, 5*time.Second, func() bool { _, err := os.Stat(markerPath(exe)); return os.IsNotExist(err) })
				if hash(t, exe) != oldHash {
					t.Fatal("old executable was not restored")
				}
			}
			if _, err := os.Stat(systemdLog); !os.IsNotExist(err) {
				t.Fatal("a declared supervisor's update went through systemd-run")
			}
		})
	}
}
