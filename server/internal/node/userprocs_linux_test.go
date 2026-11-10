//go:build linux

package node

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeProc writes root/<pid>/cmdline for each entry (a nil cmdline leaves the file out).
func fakeProc(t *testing.T, procs map[string][]byte) string {
	t.Helper()
	root := t.TempDir()
	for name, cmd := range procs {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if cmd == nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, "cmdline"), cmd, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestReadProcArgv(t *testing.T) {
	root := fakeProc(t, map[string][]byte{
		"101":  []byte("claude\x00--resume\x00abc\x00"),
		"102":  []byte("node\x00/usr/lib/codex/bin/codex.js\x00app-server\x00"),
		"103":  {},  // zombie or kernel thread
		"104":  nil, // exited between ReadDir and the read
		"self": []byte("personad\x00"),
	})
	uid := uint32(os.Getuid())
	got := readProcArgv(root, uid)
	want := map[int][]string{
		101: {"claude", "--resume", "abc"},
		102: {"node", "/usr/lib/codex/bin/codex.js", "app-server"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("readProcArgv = %v, want %v", got, want)
	}
	// Every entry above belongs to the test's user; asked for another user, none is read.
	if got := readProcArgv(root, uid+1); len(got) != 0 {
		t.Fatalf("readProcArgv for another uid = %v, want none", got)
	}
	if got := readProcArgv(filepath.Join(root, "missing"), uid); len(got) != 0 {
		t.Fatalf("readProcArgv of a missing root = %v, want none", got)
	}
}

// A walk allocates what it returns and little more: the 300 processes below, with command lines of a few hundred bytes
// to a few kilobytes (a browser's, a language server's), cost 2.41 MB a walk through os.ReadDir, DirEntry.Info and
// os.ReadFile, which grows a new buffer for each; 1.53 MB with one buffer for the walk. The bound is that, plus 3%.
func TestReadProcArgvAllocatesWhatItReturns(t *testing.T) {
	procs := map[string][]byte{}
	for i := range 300 {
		arg := strings.Repeat(fmt.Sprintf("--flag-%d=value ", i), 10+(i%7)*40)
		procs[strconv.Itoa(1000+i)] = []byte("/usr/lib/app/bin/app\x00" + strings.ReplaceAll(strings.TrimSpace(arg), " ", "\x00") + "\x00")
	}
	root := fakeProc(t, procs)
	uid := uint32(os.Getuid())
	if got := len(readProcArgv(root, uid)); got != 300 {
		t.Fatalf("read %d processes, want 300", got)
	}
	if per := bytesPerRun(10, func() { readProcArgv(root, uid) }); per > readProcArgvBudget {
		t.Errorf("a walk of 300 processes allocates %.0f bytes, want at most %d", per, readProcArgvBudget)
	}
}

// readProcArgvBudget is what TestReadProcArgvAllocatesWhatItReturns lets a walk allocate.
const readProcArgvBudget = 1_572_600 // 1,526,777 measured (1,537,762 with -race)

func TestUserProcessesSharesOneWalk(t *testing.T) {
	procSnapshot.Lock()
	procSnapshot.argv, procSnapshot.at = nil, time.Time{}
	procSnapshot.Unlock()
	before := time.Now()
	first := userProcesses()
	if _, ok := first[os.Getpid()]; !ok {
		t.Fatalf("userProcesses does not list the test process %d", os.Getpid())
	}
	if !procSnapshotAfter(before) {
		t.Fatal("procSnapshotAfter(before) = false right after a walk")
	}
	if procSnapshotAfter(time.Now()) {
		t.Fatal("procSnapshotAfter(now) = true for an older snapshot")
	}
	if second := userProcesses(); reflect.ValueOf(second).Pointer() != reflect.ValueOf(first).Pointer() {
		t.Fatal("a second call within procSnapshotTTL walked /proc again")
	}
	procSnapshot.Lock()
	procSnapshot.at = time.Now().Add(-procSnapshotTTL)
	procSnapshot.Unlock()
	if procSnapshotAfter(before) {
		t.Fatal("procSnapshotAfter = true for an expired snapshot")
	}
	if third := userProcesses(); reflect.ValueOf(third).Pointer() == reflect.ValueOf(first).Pointer() {
		t.Fatal("an expired snapshot was reused")
	}
}

// BenchmarkScanProcesses is one 30 s discovery round on this machine's real /proc: the Claude scan walks, the Codex
// scan reuses the walk. cpu-ns/op is the thread's user+system CPU time, steadier than wall time on a loaded machine.
func BenchmarkScanProcesses(b *testing.B) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	b.ReportAllocs()
	start := threadCPU(b)
	for b.Loop() {
		procSnapshot.Lock()
		procSnapshot.argv = nil
		procSnapshot.Unlock()
		scanClaudeProcesses()
		scanCodexProcesses()
	}
	b.ReportMetric(float64(threadCPU(b)-start)/float64(b.N), "cpu-ns/op")
}

// threadCPU is the calling thread's user+system CPU time in nanoseconds.
func threadCPU(b *testing.B) int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_THREAD, &ru); err != nil {
		b.Fatal(err)
	}
	return ru.Utime.Nano() + ru.Stime.Nano()
}
