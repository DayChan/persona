package node

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeProcRoot writes a /proc lookalike with n pid directories, each holding a stat line shaped like the kernel's,
// next to the non-pid entries a real /proc has.
func fakeProcRoot(tb testing.TB, n int) string {
	tb.Helper()
	root := tb.TempDir()
	for _, d := range []string{"self", "net", "sys"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			tb.Fatal(err)
		}
	}
	writeProcFile(tb, root, "uptime", "1.00 2.00\n")
	for i := range n {
		pid := 100 + i
		writeProcFile(tb, root, fmt.Sprint(pid, "/stat"), fakeStatLine(pid))
	}
	return root
}

func writeProcFile(tb testing.TB, root, name, s string) {
	tb.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		tb.Fatal(err)
	}
}

// fakeStatLine is pid's line in fakeProcRoot: every third process has a terminal, every fifth a name with spaces and
// parentheses, and the one after it a name with a ')'.
func fakeStatLine(pid int) string {
	comm, tty := fmt.Sprintf("worker-%d", pid%97), 0
	switch pid % 5 {
	case 0:
		comm = fmt.Sprintf("node (vite %d) x", pid%13)
	case 1:
		comm = "a) S 1 (b"
	}
	if pid%3 == 0 {
		tty = 34816 + pid%8
	}
	return fmt.Sprintf("%d (%s) S %d %d %d %d %d 4194560 1513 0 0 0 4 2 0 0 20 0 1 0 %d 12345678 512 "+
		"18446744073709551615 1 1 0 0 0 0 0 4096 0 0 0 0 17 %d 0 0 0 0 0 0 0 0 0 0 0 0 0\n",
		pid, comm, pid/2, pid, pid, tty, pid, 1000+pid, pid%64)
}

// The reused read buffer never leaks between processes: every one reads as the Fields parser reads its own line,
// a line longer than the buffer included, and what can't be read or parsed is left out.
func TestProcfsProcs(t *testing.T) {
	root := fakeProcRoot(t, 300)
	long := strings.Repeat("long name ", 400) // past the first buffer: the fields after it read only when it grows
	writeProcFile(t, root, "4000/stat", "4000 ("+long+") S 1 4000 4000 34820 -1"+strings.Repeat(" 123456789", 50)+"\n")
	writeProcFile(t, root, "4001/stat", "")
	writeProcFile(t, root, "4002/stat", "4002 (short) S 1\n")
	if err := os.Mkdir(filepath.Join(root, "4003"), 0o755); err != nil { // exited: no stat
		t.Fatal(err)
	}
	want := map[int]proc{}
	for i := range 300 {
		p, ok := parseStatFields(fakeStatLine(100 + i))
		if !ok {
			t.Fatalf("fake line %d does not parse", 100+i)
		}
		want[100+i] = p
	}
	want[4000] = proc{ppid: 1, tty: "34820", cmd: long}
	got, err := procfsProcs(root)
	if err != nil || !maps.Equal(got, want) {
		t.Fatalf("procfsProcs = %d processes %v, want %d; 100 %+v, 4000 ok %v", len(got), err, len(want), got[100], got[4000] == want[4000])
	}
	if _, err := procfsProcs(filepath.Join(root, "missing")); err == nil {
		t.Fatal("a missing root reads")
	}
}

// On the real /proc this process finds itself under its parent.
func TestProcfsProcsSelf(t *testing.T) {
	comm, err := os.ReadFile("/proc/self/comm")
	if err != nil {
		t.Skip("no /proc here")
	}
	procs, err := procfsProcs("/proc")
	if err != nil {
		t.Fatal(err)
	}
	if p := procs[os.Getpid()]; p.ppid != os.Getppid() || p.cmd != strings.TrimSuffix(string(comm), "\n") {
		t.Fatalf("self = %+v, want parent %d, name %q", p, os.Getppid(), comm)
	}
}

func BenchmarkProcfsProcs(b *testing.B) {
	b.Run("fake", func(b *testing.B) {
		root := fakeProcRoot(b, 2000)
		b.ReportAllocs()
		for b.Loop() {
			if procs, err := procfsProcs(root); err != nil || len(procs) != 2000 {
				b.Fatalf("procfsProcs = %d processes, %v", len(procs), err)
			}
		}
	})
	b.Run("proc", func(b *testing.B) {
		if _, err := os.Stat("/proc/self/stat"); err != nil {
			b.Skip("no /proc here")
		}
		b.ReportAllocs()
		for b.Loop() {
			if _, err := procfsProcs("/proc"); err != nil {
				b.Fatal(err)
			}
		}
	})
}
