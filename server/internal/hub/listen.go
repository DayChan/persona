package hub

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"

	"github.com/nwverse/persona/server/internal/osproc"
)

// Socket activation (docs/operations/hub-restart.md "Zero gap"): systemd's personad.socket holds the hub's listening sockets and
// hands them to each new hub process (LISTEN_FDS). While one hub stops and the next starts, connections wait in the
// socket's backlog instead of being refused, and nothing ever sees the port closed. Without systemd (Docker, launchd,
// a hub run by hand) there are none and the hub listens itself, as before.

const listenFDsStart = 3 // SD_LISTEN_FDS_START

var inherited struct {
	once  sync.Once
	mu    sync.Mutex
	lns   []net.Listener
	files []*os.File // the fds systemd passed, kept open (close-on-exec) for a hub that execs another binary
	fds   []int      // their numbers (File.Fd would put the shared sockets in blocking mode)
	names string
}

// claimInherited is the first thing every hub process does (Serve, Run, RunSlot), before anything can start another
// process: it takes the sockets systemd passed (inheritedListeners) and makes whatever else it was handed past stderr
// close-on-exec (osproc.SealInherited). Taken any later, they reached what the hub had started by then, the manager
// first, and from it every node, extension and agent it ran: each held the hub's port open for as long as it lived.
func claimInherited() {
	inheritedListeners()
	osproc.SealInherited()
}

// inheritedListeners takes the sockets systemd passed to this process, once: the environment is cleared so the
// processes the hub starts (nodes, plugins) do not take them for theirs, and the fds are made close-on-exec so they
// do not inherit them either. The fds themselves stay open: execKeepingListeners passes them on to a binary this
// process execs (selfupdate.go without an edge), which would otherwise find the port taken by systemd's own socket and
// fail to listen (address already in use).
func inheritedListeners() []net.Listener {
	inherited.once.Do(func() {
		pid, err := strconv.Atoi(os.Getenv("LISTEN_PID"))
		n, err2 := strconv.Atoi(os.Getenv("LISTEN_FDS"))
		inherited.names = os.Getenv("LISTEN_FDNAMES")
		_ = os.Unsetenv("LISTEN_PID")
		_ = os.Unsetenv("LISTEN_FDS")
		_ = os.Unsetenv("LISTEN_FDNAMES")
		if err != nil || err2 != nil || pid != os.Getpid() || n <= 0 {
			return
		}
		for fd := listenFDsStart; fd < listenFDsStart+n; fd++ {
			f := os.NewFile(uintptr(fd), "listen-fd-"+strconv.Itoa(fd))
			closeOnExec(fd)
			inherited.files = append(inherited.files, f)
			inherited.fds = append(inherited.fds, fd)
			if ln, err := net.FileListener(f); err == nil { // a dup; f stays for execListenEnv
				inherited.lns = append(inherited.lns, ln)
			}
		}
	})
	inherited.mu.Lock()
	defer inherited.mu.Unlock()
	return inherited.lns
}

// execKeepingListeners replaces this process with path (argv, env), handing it the sockets systemd passed. They are
// inheritable only from just before the exec until it fails, with syscall.ForkLock held for reading all along so that
// no process this one starts meanwhile gets them; a failed exec makes them close-on-exec again.
func execKeepingListeners(path string, argv, env []string) error {
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	err := syscall.Exec(path, argv, append(env, execListenEnv()...))
	sealListeners()
	return err
}

// sealListeners makes the sockets systemd passed close-on-exec again (after execListenEnv, when the exec failed).
func sealListeners() {
	inherited.mu.Lock()
	defer inherited.mu.Unlock()
	for _, fd := range inherited.fds {
		closeOnExec(fd)
	}
}

// execListenEnv readies the sockets systemd passed for the binary this process is about to exec (the same process:
// LISTEN_PID stays right) and returns the environment that says so; none when systemd passed none. Only
// execKeepingListeners calls it.
func execListenEnv() []string {
	inheritedListeners()
	inherited.mu.Lock()
	defer inherited.mu.Unlock()
	if len(inherited.files) == 0 {
		return nil
	}
	for _, fd := range inherited.fds {
		keepOnExec(fd)
	}
	env := []string{"LISTEN_FDS=" + strconv.Itoa(len(inherited.files)), "LISTEN_PID=" + strconv.Itoa(os.Getpid())}
	if inherited.names != "" {
		env = append(env, "LISTEN_FDNAMES="+inherited.names)
	}
	return env
}

// listenTCP is the listener for addr (host:port as in hub.json): a socket systemd passed on the same port, else a
// new one. Each passed socket is handed out once.
func listenTCP(addr string) (net.Listener, bool, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, false, fmt.Errorf("listen %q: %w", addr, err)
	}
	inheritedListeners()
	inherited.mu.Lock()
	for i, ln := range inherited.lns {
		if a, ok := ln.Addr().(*net.TCPAddr); ok && strconv.Itoa(a.Port) == port {
			inherited.lns = append(inherited.lns[:i:i], inherited.lns[i+1:]...)
			inherited.mu.Unlock()
			return ln, true, nil
		}
	}
	inherited.mu.Unlock()
	ln, err := net.Listen("tcp", addr)
	return ln, false, err
}

// unclaimedInherited is the addresses of the sockets systemd passed that listenTCP has not handed out: ports hub.json
// no longer listens on (the admin UI changed listen or public.tls_listen, and personad.socket still has the old one).
func unclaimedInherited() []net.Addr {
	inheritedListeners()
	inherited.mu.Lock()
	defer inherited.mu.Unlock()
	var addrs []net.Addr
	for _, ln := range inherited.lns {
		addrs = append(addrs, ln.Addr())
	}
	return addrs
}

// logUnclaimedInherited says, once the hub's listeners are set up, which passed sockets nothing accepts on: systemd
// keeps holding those ports, so a client there waits in the backlog until it times out instead of being refused.
// Logging only; the sockets stay as they are.
func logUnclaimedInherited(logf func(string, ...any)) {
	for _, a := range unclaimedInherited() {
		logf("hub: systemd passed a socket on %s that hub.json does not listen on; connections there hang: rerun "+
			"personad hub setup (or scripts/deploy/hub-systemd.sh) to update personad.socket", a)
	}
}

// listen is the hub's listener for addr: the edge's socket for that port when the edge started this hub (a slot),
// else listenTCP's.
func (h *Hub) listen(addr string) (net.Listener, bool, error) {
	if h.slot != nil {
		ln, err := h.slot.Listen(addr)
		return ln, false, err
	}
	return listenTCP(addr)
}
