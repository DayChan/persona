package node

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// lsof -a -p … -nP -iTCP -sTCP:LISTEN -Fpcn on macOS: vite on IPv6 localhost, a Python server on every address,
// next on both stacks (two sockets, one port).
const lsofOut = `p4242
cnode
f23
n[::1]:5173
p4300
cPython
f3
n*:8000
p4401
cnext-server (v15.2.0)
f20
n*:3000
f21
n[::]:3000
`

func TestParseLsof(t *testing.T) {
	got := parseLsof(lsofOut)
	want := []listener{
		{pid: 4242, port: 5173, addr: "::1", cmd: "node"},
		{pid: 4300, port: 8000, addr: "*", cmd: "Python"},
		{pid: 4401, port: 3000, addr: "*", cmd: "next-server (v15.2.0)"},
		{pid: 4401, port: 3000, addr: "::", cmd: "next-server (v15.2.0)"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("parseLsof:\n got %+v\nwant %+v", got, want)
	}
}

// ps -Ao pid=,ppid=,tty=,comm= on macOS: daemons have no terminal (??); comm is a path.
func TestParsePs(t *testing.T) {
	procs := parsePs(`    1     0 ??       /sbin/launchd
  900     1 ttys003  -zsh
  950   900 ttys003  /Users/me/.local/bin/claude
  960   950 ttys003  /opt/homebrew/bin/node
  970     1 ttys003  /usr/bin/python3
  980     1 ??       /usr/libexec/some daemon
`)
	if p := procs[960]; p.ppid != 950 || p.tty != "ttys003" || p.cmd != "node" {
		t.Fatalf("960 = %+v", p)
	}
	if procs[1].tty != "" || procs[980].cmd != "some daemon" {
		t.Fatalf("launchd %+v, daemon %+v", procs[1], procs[980])
	}
}

// /proc/<pid>/stat on Linux: comm may hold spaces and parentheses; tty_nr 0 is no terminal.
func TestParseStat(t *testing.T) {
	p, ok := parseStat([]byte("4242 (node (vite) x) S 4200 4242 4100 34817 4242 4194304 1 0 0 0"))
	if !ok || p.ppid != 4200 || p.tty != "34817" || p.cmd != "node (vite) x" {
		t.Fatalf("parseStat = %+v %v", p, ok)
	}
	if p, _ := parseStat([]byte("1 (systemd) S 0 1 1 0 -1 4194560")); p.tty != "" {
		t.Fatalf("no terminal: %+v", p)
	}
}

// parseStatFields is the strings.Fields parser parseStat replaced, kept as its reference.
func parseStatFields(s string) (proc, bool) {
	open, end := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || end < open {
		return proc{}, false
	}
	f := strings.Fields(s[end+1:])
	if len(f) < 5 {
		return proc{}, false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return proc{}, false
	}
	tty := f[4]
	if tty == "0" {
		tty = ""
	}
	return proc{ppid: ppid, tty: tty, cmd: s[open+1 : end]}, true
}

// The byte parser reads every line the way the strings.Fields one did, odd names and broken lines included.
func TestParseStatBytes(t *testing.T) {
	for _, tc := range []struct {
		line string
		want proc
		ok   bool
	}{
		{"4242 (node) S 4200 4242 4100 34817 4242 4194304\n", proc{ppid: 4200, tty: "34817", cmd: "node"}, true},
		{"7 (my server) R 1 7 7 0 -1\n", proc{ppid: 1, cmd: "my server"}, true},
		{"8 (a) S 1 (b) S 2 8 8 34818 -1", proc{ppid: 2, tty: "34818", cmd: "a) S 1 (b"}, true},
		{"9 ((sd-pam)) S 1 9 9 0 -1", proc{ppid: 1, cmd: "(sd-pam)"}, true},
		{"10 () Z 3 10 10 0", proc{ppid: 3, cmd: ""}, true},
		{"11 (tab\tname)\tS\t5\t11 11  \t34816\n", proc{ppid: 5, tty: "34816", cmd: "tab\tname"}, true},
		{"12 (x)S 6 12 12 00", proc{ppid: 6, tty: "00", cmd: "x"}, true},
		{"13 (x) S +6 13 13 0 -1 extra fields 1 2 3 4 5 6 7 8 9", proc{ppid: 6, cmd: "x"}, true},
		{"14 (x) S 6 14 14", proc{}, false},             // truncated before tty_nr
		{"15 (x) S 6 15 15 \n", proc{}, false},          // tty_nr missing, trailing space
		{"16 (x) S notapid 16 16 0", proc{}, false},     // ppid not a number
		{"17 (unterminated S 1 17 17 0", proc{}, false}, // no ')'
		{"18 x) S 1 18 18 0", proc{}, false},            // no '('
		{"19 )x( S 1 19 19 0", proc{}, false},           // ')' before '('
		{"", proc{}, false},
	} {
		got, ok := parseStat([]byte(tc.line))
		if got != tc.want || ok != tc.ok {
			t.Errorf("parseStat(%q) = %+v %v, want %+v %v", tc.line, got, ok, tc.want, tc.ok)
		}
		if ref, refOK := parseStatFields(tc.line); got != ref || ok != refOK {
			t.Errorf("parseStat(%q) = %+v %v, the Fields parser %+v %v", tc.line, got, ok, ref, refOK)
		}
	}
}

// /proc/net/tcp and tcp6: only LISTEN (0A) sockets, addresses in little-endian words.
func TestParseProcNet(t *testing.T) {
	tcp := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1435 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0000000000000000 100 0 0 10 0
   1: 00000000:1F40 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 222 1 0000000000000000 100 0 0 10 0
   2: 0100007F:1435 0100007F:C350 01 00000000:00000000 00:00000000 00000000  1000        0 333 1 0000000000000000 20 4 30 10 -1
`
	tcp6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 444 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000000000000:0BB8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 555 1 0000000000000000 100 0 0 10 0
   2: 0000000000000000FFFF00000100007F:22B8 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 666 1 0000000000000000 100 0 0 10 0
`
	got := parseProcNet(tcp)
	if len(got) != 2 || got[111] != (listener{port: 5173, addr: "127.0.0.1"}) || got[222] != (listener{port: 8000, addr: "0.0.0.0"}) {
		t.Fatalf("tcp = %+v", got)
	}
	got6 := parseProcNet(tcp6)
	if got6[444] != (listener{port: 3000, addr: "::1"}) || got6[555] != (listener{port: 3000, addr: "::"}) ||
		got6[666] != (listener{port: 8888, addr: "127.0.0.1"}) {
		t.Fatalf("tcp6 = %+v", got6)
	}
}

// A fake /proc: the pids' descriptors point at socket inodes; only listening ones count.
func TestProcfsListeners(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("net/tcp", "header\n   0: 0100007F:1435 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 111 1 0 100 0 0 10 0\n")
	write("net/tcp6", "header\n")
	_ = os.MkdirAll(filepath.Join(root, "42", "fd"), 0o755)
	_ = os.Symlink("socket:[111]", filepath.Join(root, "42", "fd", "20"))
	_ = os.Symlink("socket:[999]", filepath.Join(root, "42", "fd", "21")) // a connected socket
	_ = os.Symlink("/dev/null", filepath.Join(root, "42", "fd", "0"))
	got := procfsListeners(root, []int{42, 43})
	if len(got) != 1 || got[0] != (listener{pid: 42, port: 5173, addr: "127.0.0.1"}) {
		t.Fatalf("procfsListeners = %+v", got)
	}
}

// The agent's processes: what runs under it, and what runs on its terminal after its parent exited.
func TestOwned(t *testing.T) {
	procs := map[int]proc{
		1:   {ppid: 0},
		900: {ppid: 1, tty: "ttys003"},   // the pane's shell
		950: {ppid: 900, tty: "ttys003"}, // claude
		955: {ppid: 950},                 // a Bash tool's shell, no terminal
		960: {ppid: 955},                 // npm run dev
		961: {ppid: 960},                 // vite
		970: {ppid: 1, tty: "ttys003"},   // `python -m http.server &` whose shell exited
		980: {ppid: 1, tty: "ttys004"},   // another terminal
		990: {ppid: 1},                   // a daemon
	}
	got := owned([]int{950}, procs)
	for _, pid := range []int{950, 955, 960, 961, 970, 900} {
		if !got[pid] {
			t.Errorf("%d should be the agent's", pid)
		}
	}
	for _, pid := range []int{1, 980, 990} {
		if got[pid] {
			t.Errorf("%d is not the agent's", pid)
		}
	}
}

func TestPageTitle(t *testing.T) {
	for page, want := range map[string]string{
		`<html><head><title>Vite + React</title></head>`: "Vite + React",
		"<TITLE lang=en>\n  My &amp; App\n</TITLE>":      "My & App",
		`<html><body>no title</body>`:                    "",
		"<title>" + strings.Repeat("长", 80) + "</title>": strings.Repeat("长", 60) + "…",
	} {
		if got := pageTitle(page); got != want {
			t.Errorf("pageTitle(%.30q) = %q, want %q", page, got, want)
		}
	}
}

// The probe: an HTML page gives its title, a JSON API is HTTP without one, a socket speaking something else is
// not listed, one that accepts and says nothing yet is pending, and a closed port is nothing.
func TestProbePort(t *testing.T) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Host, "localhost:") {
			http.Error(w, "blocked host "+r.Host, http.StatusForbidden) // vite's host check
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><title>Dev page</title>")
	}))
	defer page.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer api.Close()
	other := rawServer(t, func(c net.Conn) { _, _ = c.Write([]byte("SSH-2.0-OpenSSH_9\r\n")) })
	silent := rawServer(t, func(c net.Conn) { time.Sleep(portProbeWait + time.Second) })

	ctx := context.Background()
	if got := probePort(ctx, portOf(t, page.URL), "127.0.0.1"); !got.http || got.title != "Dev page" {
		t.Errorf("page: %+v", got)
	}
	if got := probePort(ctx, portOf(t, api.URL), "127.0.0.1"); !got.http || got.title != "" {
		t.Errorf("api: %+v", got)
	}
	if got := probePort(ctx, other, "0.0.0.0"); got.http {
		t.Errorf("ssh: %+v", got)
	}
	if got := probePort(ctx, silent, "127.0.0.1"); !got.http || !got.pending {
		t.Errorf("silent: %+v", got)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if got := probePort(ctx, closed, ""); got.http {
		t.Errorf("closed: %+v", got)
	}
}

// A live scan on this machine: a server this test process runs is found as the pane's, with its title; pushed
// again only when that changes; gone once it stops.
func TestScanPortsFindsTheAgentsServer(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("ports are scanned on Linux and macOS")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<title>Scanned</title>")
	}))
	port := portOf(t, srv.URL)
	// The pane's agent (its first process) is not there; the test process is the pane's other process, as the
	// shell a server runs under would be.
	h := &procHost{bareHost: bareHost{kind: host.Herdr}, pids: []int{1 << 30, os.Getpid()}}
	n := &Node{hosts: host.NewRegistry(h), agents: map[string]host.Agent{"s1": {Agent: "claude", PaneID: "w1:p1"}},
		logger: log.New(os.Stderr, "", 0)}
	ctx := context.Background()
	if !n.scanPorts(ctx) {
		t.Fatal("the first scan with a server should be a change")
	}
	ps := n.portsOf("w1:p1")
	i := slices.IndexFunc(ps, func(p proto.Port) bool { return p.Port == port })
	if i < 0 {
		t.Fatalf("port %d not found in %+v", port, ps)
	}
	if ps[i].Title != "Scanned" || ps[i].PID != os.Getpid() {
		t.Fatalf("port = %+v", ps[i])
	}
	if n.scanPorts(ctx) {
		t.Fatal("nothing changed: no push")
	}
	srv.Close()
	n.scanPorts(ctx)
	if slices.ContainsFunc(n.portsOf("w1:p1"), func(p proto.Port) bool { return p.Port == port }) {
		t.Fatal("a stopped server is still listed")
	}
}

// lsof exits 1 when a pid it was given is gone or a zombie (an agent's short command that exited between ps and
// lsof); the others' listeners are still found (#541).
func TestLsofListenersWithAGonePid(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("lsof lists listeners on macOS")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	reaped := exec.Command("true")
	if err := reaped.Run(); err != nil {
		t.Fatal(err)
	}
	zombie := exec.Command("true")
	if err := zombie.Start(); err != nil {
		t.Fatal(err)
	}
	defer zombie.Wait()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(zombie.Process.Pid)).Output()
		if strings.HasPrefix(strings.TrimSpace(string(out)), "Z") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d is not a zombie: %q", zombie.Process.Pid, out)
		}
	}
	ls, err := lsofListeners(context.Background(), []int{os.Getpid(), zombie.Process.Pid, reaped.Process.Pid})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(ls, func(l listener) bool { return l.pid == os.Getpid() && l.port == port }) {
		t.Fatalf("port %d not found in %+v", port, ls)
	}
}

// A scan that keeps failing logs once, and once more when scans work again (#541: a line every 4 seconds).
func TestScanPortsLogsAFailureOnce(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("on Linux the scan reads /proc and has no command to fail")
	}
	var logs strings.Builder
	h := &procHost{bareHost: bareHost{kind: host.Herdr}, pids: []int{1 << 30, os.Getpid()}}
	n := &Node{hosts: host.NewRegistry(h), agents: map[string]host.Agent{"s1": {Agent: "claude", PaneID: "w1:p1"}},
		logger: log.New(&logs, "", 0)}
	ctx := context.Background()
	path := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir()) // no ps, no lsof
	for range 3 {
		n.scanPorts(ctx)
	}
	if got := strings.Count(logs.String(), "\n"); got != 1 {
		t.Fatalf("3 failed scans logged %d lines:\n%s", got, logs.String())
	}
	t.Setenv("PATH", path)
	n.scanPorts(ctx)
	n.scanPorts(ctx)
	if lines := strings.Split(strings.TrimSpace(logs.String()), "\n"); len(lines) != 2 || lines[1] != "ports: scanning again" {
		t.Fatalf("after recovering:\n%s", logs.String())
	}
}

type procHost struct {
	bareHost
	pids []int
}

func (h *procHost) PaneProcesses(context.Context, host.PaneRef) ([]int, error) { return h.pids, nil }

func rawServer(t *testing.T, serve func(net.Conn)) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); serve(c) }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func portOf(t *testing.T, u string) int {
	t.Helper()
	i := strings.LastIndexByte(u, ':')
	p, err := strconv.Atoi(u[i+1:])
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Two panes, a server each: one scan asks each server once, and the next asks neither (docs/operations/ports.md: results are
// kept by pid:port until the process or the port goes). A pane's own pass must not drop another pane's results.
func TestProbePanesKeepsEveryPanesProbes(t *testing.T) {
	var hits [2]atomic.Int32
	var ports [2]int
	for i := range hits {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits[i].Add(1)
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, "<title>pane %d</title>", i)
		}))
		t.Cleanup(srv.Close)
		ports[i] = portOf(t, srv.URL)
	}
	n := &Node{}
	found := map[string][]listener{
		"w1:p1": {{pid: 101, port: ports[0], addr: "127.0.0.1"}},
		"w1:p2": {{pid: 102, port: ports[1], addr: "127.0.0.1"}},
	}
	ctx := context.Background()
	for range 2 {
		got := n.probePanes(ctx, found)
		if len(got["w1:p1"]) != 1 || len(got["w1:p2"]) != 1 {
			t.Fatalf("ports = %+v", got)
		}
	}
	if a, b := hits[0].Load(), hits[1].Load(); a != 1 || b != 1 {
		t.Fatalf("two scans asked the servers %d and %d times, want once each", a, b)
	}

	// The second pane's server gone: its result goes, the first pane's stays.
	delete(found, "w1:p2")
	n.probePanes(ctx, found)
	n.ports.mu.Lock()
	_, kept := n.ports.probes["101:"+strconv.Itoa(ports[0])]
	_, stale := n.ports.probes["102:"+strconv.Itoa(ports[1])]
	n.ports.mu.Unlock()
	if !kept || stale {
		t.Fatalf("after the second pane went: first kept %v, second kept %v", kept, stale)
	}
	// No listener found anywhere: nothing is kept.
	n.probePanes(ctx, nil)
	n.ports.mu.Lock()
	left := len(n.ports.probes)
	n.ports.mu.Unlock()
	if left != 0 {
		t.Fatalf("%d results kept with no listener left", left)
	}
	if hits[0].Load() != 1 {
		t.Fatalf("the first server was asked %d times", hits[0].Load())
	}
}
