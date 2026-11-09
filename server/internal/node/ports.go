package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The ports the agents listen on (docs/operations/ports.md): each live agent's processes — what runs under it and what else
// runs on its terminal — and the TCP ports they listen on that answer HTTP, with the page's title. They go out with
// the session list; the list is pushed again only when they change.
//
// A scan costs a process listing and a socket listing: on Linux both are read from /proc, on macOS `ps` and `lsof`
// (about 0.17 s of CPU with ~3,700 processes), on Windows a Toolhelp snapshot and GetExtendedTcpTable
// (ports_windows.go). It runs every portSweep while there are live agents, and sooner
// (portKickGap after the last) when an agent's transcript grew — a dev server is usually started by a tool call — or
// a forward found its port gone.

const (
	portSweep   = 15 * time.Second
	portKickGap = 4 * time.Second
	// portProbeWait: a dev server compiling its first page (next dev) takes seconds to answer; one that accepted the
	// connection and said nothing by then is listed without a title and asked again next time.
	portProbeWait = 3 * time.Second
	maxPortTitle  = 60 // characters
	// portProbeAgent is the User-Agent of the probe's GET /: a server can tell it from its real clients (the tests'
	// servers do, as a node on the machine probes them too). A node installed before the Persona-only rename (#6079)
	// sends its original User-Agent until it is replaced, so what recognizes the probe has to accept both (the tests'
	// portProbeAgents). Renaming it again leaves every installed node's probe unrecognized the same way.
	portProbeAgent = "persona-ports"
)

type portState struct {
	mu     sync.Mutex
	byPane map[string][]proto.Port // pane -> its ports, as last found
	probes map[string]portProbe    // "pid:port" -> what asking it found
	kick   chan struct{}
	// scanErr: the last scan's error, logged when it first happens and when scans work again, not every scan
	// (only scanPorts, on watchPorts' goroutine, touches it).
	scanErr string
}

type portProbe struct {
	http    bool // it answered HTTP, or accepted the connection and has not answered yet (pending)
	pending bool
	title   string
	tries   int // probes so far
}

// listener is one listening TCP socket.
type listener struct {
	pid  int
	port int
	addr string // what it is bound to: 127.0.0.1, 0.0.0.0, ::, ::1, a LAN address
	cmd  string
}

// proc is one process: its parent and its controlling terminal ("" when none).
type proc struct {
	ppid int
	tty  string
	cmd  string
}

// kickPorts asks for a scan soon (a transcript grew, a forward found nothing listening).
func (n *Node) kickPorts() {
	select {
	case n.ports.kick <- struct{}{}:
	default:
	}
}

// watchPorts scans while the node serves the hub.
func (n *Node) watchPorts(ctx context.Context) {
	t := time.NewTicker(portSweep)
	defer t.Stop()
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-n.ports.kick:
			select {
			case <-ctx.Done():
				return
			case <-time.After(max(500*time.Millisecond, time.Until(last.Add(portKickGap)))):
			}
		}
		last = time.Now()
		if n.scanPorts(ctx) {
			n.notifySessions()
		}
	}
}

// scanPorts finds each live agent's ports; changed: they differ from the last scan.
func (n *Node) scanPorts(ctx context.Context) (changed bool) {
	n.mu.Lock()
	panes := map[string]bool{}
	for _, a := range n.agents {
		if a.PaneID != "" {
			panes[a.PaneID] = true
		}
	}
	n.mu.Unlock()
	roots := map[string][]int{}
	for pane := range panes {
		pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		pids, err := n.paneProcesses(pctx, pane)
		cancel()
		if err == nil && len(pids) > 0 {
			roots[pane] = pids
		}
	}
	var found map[string][]listener
	if len(roots) > 0 {
		var err error
		if found, err = scanListeners(ctx, roots); err != nil {
			if e := err.Error(); e != n.ports.scanErr {
				n.logger.Printf("ports: %v (logged again only if it changes)", err)
				n.ports.scanErr = e
			}
			return false
		}
	}
	if n.ports.scanErr != "" {
		n.logger.Printf("ports: scanning again")
		n.ports.scanErr = ""
	}
	next := n.probePanes(ctx, found)
	n.ports.mu.Lock()
	defer n.ports.mu.Unlock()
	changed = portsKey(next) != portsKey(n.ports.byPane)
	n.ports.byPane = next
	return changed
}

// portsOf is a pane's ports from the last scan.
func (n *Node) portsOf(pane string) []proto.Port {
	if pane == "" {
		return nil
	}
	n.ports.mu.Lock()
	defer n.ports.mu.Unlock()
	return n.ports.byPane[pane]
}

// listedPort is the listener the last scan found on port, if any (where to dial it).
func (n *Node) listedPort(port int) (proto.Port, bool) {
	n.ports.mu.Lock()
	defer n.ports.mu.Unlock()
	for _, ps := range n.ports.byPane {
		for _, p := range ps {
			if p.Port == port {
				return p, true
			}
		}
	}
	return proto.Port{}, false
}

func portsKey(m map[string][]proto.Port) string {
	panes := make([]string, 0, len(m))
	for p := range m {
		panes = append(panes, p)
	}
	slices.Sort(panes)
	var b strings.Builder
	for _, pane := range panes {
		b.WriteString(pane)
		for _, p := range m[pane] {
			fmt.Fprintf(&b, "|%d/%d/%s/%s", p.Port, p.PID, p.Process, p.Title)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// probePanes is each pane's listeners that answer HTTP. The results of asking are kept for every pid:port this scan
// found, in any pane; the others go (a process gone, or no longer listening there).
func (n *Node) probePanes(ctx context.Context, found map[string][]listener) map[string][]proto.Port {
	next := map[string][]proto.Port{}
	live := map[string]bool{}
	for pane, ls := range found {
		if ps := n.httpPorts(ctx, ls, live); len(ps) > 0 {
			next[pane] = ps
		}
	}
	n.ports.mu.Lock()
	for key := range n.ports.probes {
		if !live[key] {
			delete(n.ports.probes, key)
		}
	}
	n.ports.mu.Unlock()
	return next
}

// httpPorts keeps the listeners that answer HTTP, one per port, with their titles; each pid:port is asked once
// (again while it has not answered). It adds the pid:ports it saw to live.
func (n *Node) httpPorts(ctx context.Context, ls []listener, live map[string]bool) []proto.Port {
	var out []proto.Port
	seen := map[int]bool{}
	for _, l := range ls {
		if seen[l.port] {
			continue
		}
		key := strconv.Itoa(l.pid) + ":" + strconv.Itoa(l.port)
		live[key] = true
		n.ports.mu.Lock()
		if n.ports.probes == nil {
			n.ports.probes = map[string]portProbe{}
		}
		pr, ok := n.ports.probes[key]
		n.ports.mu.Unlock()
		if !ok || (pr.pending && pr.tries < 5) {
			got := probePort(ctx, l.port, l.addr)
			got.tries = pr.tries + 1
			pr = got
			n.ports.mu.Lock()
			n.ports.probes[key] = pr
			n.ports.mu.Unlock()
		}
		if !pr.http {
			continue
		}
		seen[l.port] = true
		out = append(out, proto.Port{Port: l.port, Addr: l.addr, PID: l.pid, Process: l.cmd, Title: pr.title})
	}
	slices.SortFunc(out, func(a, b proto.Port) int { return a.Port - b.Port })
	return out
}

// dialTargets are the addresses to reach a listener bound to addr, from this machine.
func dialTargets(port int, addr string) []string {
	p := strconv.Itoa(port)
	switch addr {
	case "", "0.0.0.0", "*":
		return []string{"127.0.0.1:" + p, "[::1]:" + p}
	case "::", "[::]":
		return []string{"[::1]:" + p, "127.0.0.1:" + p}
	}
	return []string{net.JoinHostPort(strings.Trim(addr, "[]"), p)}
}

func dialPort(ctx context.Context, port int, addr string) (net.Conn, error) {
	var d net.Dialer
	var err error
	for _, a := range dialTargets(port, addr) {
		var c net.Conn
		if c, err = d.DialContext(ctx, "tcp", a); err == nil {
			return c, nil
		}
	}
	return nil, err
}

var titleRe = lazyre.New(`(?is)<title[^>]*>(.*?)</title>`)

// probePort asks GET / of a listener as a browser on this machine would (Host localhost:port, which dev servers'
// host checks accept) and reads the page's title.
func probePort(ctx context.Context, port int, addr string) portProbe {
	ctx, cancel := context.WithTimeout(ctx, portProbeWait)
	defer cancel()
	conn, err := dialPort(ctx, port, addr)
	if err != nil {
		return portProbe{}
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(portProbeWait))
	host := "localhost:" + strconv.Itoa(port)
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: %s\r\nUser-Agent: %s\r\nAccept: text/html\r\nConnection: close\r\n\r\n", host, portProbeAgent)
	br := bufio.NewReader(conn)
	head, err := br.Peek(5)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() && len(head) == 0 {
			return portProbe{http: true, pending: true} // accepted, still working on it
		}
		return portProbe{}
	}
	if string(head) != "HTTP/" {
		return portProbe{}
	}
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		return portProbe{http: true}
	}
	defer res.Body.Close()
	if !strings.Contains(res.Header.Get("Content-Type"), "html") {
		return portProbe{http: true}
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	return portProbe{http: true, title: pageTitle(string(body))}
}

func pageTitle(page string) string {
	m := titleRe.FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	t := strings.Join(strings.Fields(html.UnescapeString(m[1])), " ")
	if r := []rune(t); len(r) > maxPortTitle {
		t = strings.TrimSpace(string(r[:maxPortTitle])) + "…"
	}
	return t
}

// scanListeners finds, for each pane, the listeners of the processes it owns (owned).
func scanListeners(ctx context.Context, roots map[string][]int) (map[string][]listener, error) {
	var procs map[int]proc
	var err error
	switch runtime.GOOS {
	case "linux":
		procs, err = procfsProcs("/proc")
	case "windows":
		procs, err = winProcs()
	default:
		procs, err = psProcs(ctx)
	}
	if err != nil {
		return nil, err
	}
	owners := map[int]string{} // pid -> pane
	var all []int
	for pane, pids := range roots {
		agent := 0
		if len(pids) > 0 {
			agent = pids[0] // claude / codex itself: its own sockets (IDE bridges, MCP) are not the agent's servers
		}
		for pid := range owned(pids, procs) {
			if pid == agent {
				continue
			}
			if _, dup := owners[pid]; !dup {
				owners[pid] = pane
				all = append(all, pid)
			}
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	var ls []listener
	switch runtime.GOOS {
	case "linux":
		ls = procfsListeners("/proc", all)
	case "windows":
		ls, err = winListeners(all)
	default:
		ls, err = lsofListeners(ctx, all)
	}
	if err != nil {
		return nil, err
	}
	out := map[string][]listener{}
	for _, l := range ls {
		if pane, ok := owners[l.pid]; ok {
			if l.cmd == "" {
				l.cmd = procs[l.pid].cmd
			}
			out[pane] = append(out[pane], l)
		}
	}
	return out, nil
}

// owned are the processes under roots, and those on the same terminal as a root (a server started with `&` whose
// shell has exited is no longer under it, but keeps the terminal).
func owned(roots []int, procs map[int]proc) map[int]bool {
	children := map[int][]int{}
	for pid, p := range procs {
		children[p.ppid] = append(children[p.ppid], pid)
	}
	ttys := map[string]bool{}
	out := map[int]bool{}
	var walk func(int)
	walk = func(pid int) {
		if out[pid] {
			return
		}
		out[pid] = true
		for _, c := range children[pid] {
			walk(c)
		}
	}
	for _, r := range roots {
		if _, ok := procs[r]; !ok {
			continue
		}
		if t := procs[r].tty; t != "" {
			ttys[t] = true
		}
		walk(r)
	}
	for pid, p := range procs {
		if p.tty != "" && ttys[p.tty] {
			walk(pid)
		}
	}
	return out
}

// psProcs lists processes with ps (macOS): "pid ppid tty comm".
func psProcs(ctx context.Context) (map[int]proc, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ps", "-Ao", "pid=,ppid=,tty=,comm=").Output()
	if err != nil {
		return nil, fmt.Errorf("ps: %w", err)
	}
	return parsePs(string(out)), nil
}

func parsePs(out string) map[int]proc {
	procs := map[int]proc{}
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		tty := f[2]
		if tty == "??" || tty == "?" || tty == "-" {
			tty = ""
		}
		cmd := ""
		if len(f) > 3 {
			cmd = filepath.Base(strings.Join(f[3:], " "))
		}
		procs[pid] = proc{ppid: ppid, tty: tty, cmd: cmd}
	}
	return procs
}

// lsofListeners: the TCP listeners of pids (macOS), from `lsof -Fpcn`: "p<pid>", "c<command>", "n<addr>:<port>".
func lsofListeners(ctx context.Context, pids []int) ([]listener, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	out, err := exec.CommandContext(ctx, "lsof", "-a", "-p", strings.Join(list, ","), "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpcn").Output()
	// lsof exits 1 when one of the pids has nothing to list: none listens, or one is gone or a zombie (ps saw it, a
	// short-lived command has exited since) while it still prints what the others have. Its complaints go to
	// stderr, so stdout is the listing either way.
	var ee *exec.ExitError
	if err != nil && (ctx.Err() != nil || !errors.As(err, &ee) || ee.ExitCode() != 1) {
		return nil, fmt.Errorf("lsof: %w", err)
	}
	return parseLsof(string(out)), nil
}

func parseLsof(out string) []listener {
	var ls []listener
	pid, cmd := 0, ""
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" {
			continue
		}
		v := line[1:]
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(v)
			cmd = ""
		case 'c':
			cmd = v
		case 'n':
			i := strings.LastIndexByte(v, ':')
			if i < 0 {
				continue
			}
			port, err := strconv.Atoi(v[i+1:])
			if err != nil || port <= 0 {
				continue
			}
			ls = append(ls, listener{pid: pid, port: port, addr: strings.Trim(v[:i], "[]"), cmd: cmd})
		}
	}
	return ls
}

// parseStat reads one /proc/<pid>/stat line (procfsProcs, Linux): "pid (comm) state ppid pgrp session tty_nr …". comm
// may hold spaces and parentheses, so it ends at the last ')'. It runs for every process on every sweep, so it walks
// the bytes instead of splitting the line, and copies out only one string, from comm to tty_nr, that cmd and tty share.
func parseStat(b []byte) (proc, bool) {
	open, end := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 0 || end < open {
		return proc{}, false
	}
	// The fields after comm, as [from, to) offsets in b: 0 state, 1 ppid, 2 pgrp, 3 session, 4 tty_nr.
	var from, to [5]int
	i := end + 1
	for k := range from {
		for i < len(b) && isStatSpace(b[i]) {
			i++
		}
		from[k] = i
		for i < len(b) && !isStatSpace(b[i]) {
			i++
		}
		if i == from[k] {
			return proc{}, false // a short line
		}
		to[k] = i
	}
	ppid, err := strconv.Atoi(string(b[from[1]:to[1]]))
	if err != nil {
		return proc{}, false
	}
	s := string(b[open+1 : to[4]])
	tty := s[from[4]-open-1:]
	if tty == "0" {
		tty = ""
	}
	return proc{ppid: ppid, tty: tty, cmd: s[:end-open-1]}, true
}

// isStatSpace separates the fields after comm, which the kernel writes in ASCII.
func isStatSpace(c byte) bool {
	return c == ' ' || c == '\n' || c == '\t' || c == '\r' || c == '\v' || c == '\f'
}

// procfsListeners: the TCP listeners of pids (Linux): the sockets in /proc/net/tcp{,6} in state LISTEN, matched by
// inode to the pids' file descriptors.
func procfsListeners(root string, pids []int) []listener {
	socks := map[uint64]listener{}
	for _, f := range []string{"net/tcp", "net/tcp6"} {
		b, err := os.ReadFile(filepath.Join(root, f))
		if err == nil {
			for ino, l := range parseProcNet(string(b)) {
				socks[ino] = l
			}
		}
	}
	if len(socks) == 0 {
		return nil
	}
	var ls []listener
	for _, pid := range pids {
		dir := filepath.Join(root, strconv.Itoa(pid), "fd")
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(dir, fd.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") {
				continue
			}
			ino, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]"), 10, 64)
			if err != nil {
				continue
			}
			if l, ok := socks[ino]; ok {
				l.pid = pid
				ls = append(ls, l)
			}
		}
	}
	slices.SortFunc(ls, func(a, b listener) int { return a.port - b.port })
	return ls
}

// parseProcNet reads /proc/net/tcp or tcp6: the listening sockets (st 0A) by inode. Addresses are hex, each 32-bit
// word in host (little-endian) order: 0100007F:1435 is 127.0.0.1:5173.
func parseProcNet(data string) map[uint64]listener {
	out := map[uint64]listener{}
	for i, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 10 || f[3] != "0A" {
			continue
		}
		host, port, ok := strings.Cut(f[1], ":")
		if !ok {
			continue
		}
		p, err := strconv.ParseUint(port, 16, 16)
		if err != nil {
			continue
		}
		ino, err := strconv.ParseUint(f[9], 10, 64)
		if err != nil || ino == 0 {
			continue
		}
		addr, ok := procNetAddr(host)
		if !ok {
			continue
		}
		out[ino] = listener{port: int(p), addr: addr}
	}
	return out
}

func procNetAddr(h string) (string, bool) {
	if len(h) != 8 && len(h) != 32 {
		return "", false
	}
	ip := make(net.IP, len(h)/2)
	for w := 0; w < len(h)/8; w++ {
		v, err := strconv.ParseUint(h[w*8:w*8+8], 16, 32)
		if err != nil {
			return "", false
		}
		ip[w*4], ip[w*4+1], ip[w*4+2], ip[w*4+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	return ip.String(), true // ::ffff:a.b.c.d (tcp6 also lists those) reads as a.b.c.d
}

// portDial is port.dial: a stream (streams.Mux) to the port on this machine, at the address the scan found it on,
// else at loopback. Only this machine's own addresses are ever dialed.
func (n *Node) portDial(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		ID   string `json:"id"`
		Port int    `json:"port"`
		// The live screen (5900) is being watched: the Mac stays awake until it closes (awake.go).
		KeepAwake bool `json:"keep_awake"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.ID == "" || p.Port <= 0 || p.Port > 65535 {
		return nil, errors.New("port.dial: need id and port")
	}
	addr := ""
	if l, ok := n.listedPort(p.Port); ok {
		addr = l.Addr
	}
	dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c, err := dialPort(dctx, p.Port, addr)
	if err != nil {
		n.kickPorts() // it may be gone: the list says so sooner
		return nil, &rpc.Error{Code: "port_gone", Message: fmt.Sprintf("nothing is listening on port %d", p.Port)}
	}
	if err := n.streams.Open(p.ID); err != nil {
		_ = c.Close()
		return nil, err
	}
	if p.KeepAwake {
		c = &watchedConn{Conn: c, done: awake.watch()}
	}
	n.streams.Start(p.ID, c)
	return map[string]any{}, nil
}

// portProbeRPC is port.probe: whether anything answers on the port here, and its page's title.
func (n *Node) portProbeRPC(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct {
		Port int `json:"port"`
	}
	if err := json.Unmarshal(params, &p); err != nil || p.Port <= 0 || p.Port > 65535 {
		return nil, errors.New("port.probe: need a port")
	}
	addr := ""
	if l, ok := n.listedPort(p.Port); ok {
		addr = l.Addr
	}
	pr := probePort(ctx, p.Port, addr)
	if !pr.http && pr.title == "" {
		// Not HTTP, or nothing there: tell them apart by connecting.
		dctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		c, err := dialPort(dctx, p.Port, addr)
		cancel()
		if err != nil {
			return map[string]any{"port": p.Port, "listening": false}, nil
		}
		_ = c.Close()
	}
	return map[string]any{"port": p.Port, "listening": true, "http": pr.http, "title": pr.title}, nil
}
