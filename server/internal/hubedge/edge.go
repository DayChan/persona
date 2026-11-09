//go:build unix

package hubedge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/acceptloop"
	"github.com/nwverse/persona/server/internal/proxyproto"
)

// Policy budgets, not measurements: how long each step of a switch may take before it counts as failed.
const (
	defaultHoldMax      = 60 * time.Second  // a connection waits this long for a slot to serve it
	defaultStandbyWait  = 90 * time.Second  // a new binary starts and checks itself (config, database)
	defaultReleaseWait  = 20 * time.Second  // the old slot stops (the hub's own stop budget is 8 s)
	defaultActivateWait = 120 * time.Second // the new slot migrates its database and listens
	defaultConfirmWait  = 3 * time.Minute   // an update's new slot confirms it (selfupdate.go: 2 minutes)
	maxWaiting          = 4096              // connections held at once (the socket unit's backlog)
	maxFailedStarts     = 5                 // the active slot dying this many times in a row ends the edge (the service manager takes over)
)

// Options is how Run runs the edge.
type Options struct {
	Listeners []net.Listener // the hub's ports, held for as long as the edge runs
	Binary    string         // the hub binary the first slot runs
	Args      []string       // a slot's arguments ("hub", "slot")
	RunDir    string         // where the slots' sockets go
	Control   string         // the control socket (`personad hub switch`)
	Version   string         // the edge's own build, for status
	Logf      func(format string, args ...any)
	// RestoreUpdate puts back what ran before the update to build (the pending marker's binaries, selfupdate.go): its
	// new slot could not take over (cause), or did not confirm it in time (cause nil). It says which binary to run
	// now; ok false when no such update is under way (confirmed, or rolled back meanwhile).
	RestoreUpdate func(build int64, cause error) (binary string, ok bool)

	// Budgets (zero: the defaults), for the tests.
	HoldMax, StandbyWait, ReleaseWait, ActivateWait, ConfirmWait time.Duration
}

type switchReq struct {
	m        Msg
	from     *proc // the active slot that asked, if one did
	progress func(phase string)
	done     chan error
}

type edge struct {
	o   Options
	ctx context.Context

	mu     sync.Mutex
	active *proc
	gate   chan struct{} // closed while connections may go to active
	held   bool

	waiting atomic.Int32
	reqs    chan switchReq
	slots   int // started so far: names alternate a, b

	// The update the active slot is the result of, until it confirms it.
	confirmBuild int64
	confirmBy    *time.Timer

	// Nothing serves: start retryBinary again at retry (the edge's loop only).
	failures    int
	retry       <-chan time.Time
	retryBinary string
}

// Run serves until ctx ends: it starts the first slot on o.Binary, relays the listeners' connections to the active
// slot, and switches slots when the active one or `hub switch` asks. It returns when the active slot keeps failing
// to start (the service manager restarts the whole service then) or ctx ends.
func Run(ctx context.Context, o Options) error {
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	orDefault(&o.HoldMax, defaultHoldMax)
	orDefault(&o.StandbyWait, defaultStandbyWait)
	orDefault(&o.ReleaseWait, defaultReleaseWait)
	orDefault(&o.ActivateWait, defaultActivateWait)
	orDefault(&o.ConfirmWait, defaultConfirmWait)
	if err := os.MkdirAll(o.RunDir, 0o700); err != nil {
		return err
	}
	e := &edge{o: o, ctx: ctx, gate: make(chan struct{}), held: true, reqs: make(chan switchReq)}
	e.logf("hub edge %s: holding %d port(s), starting the hub (%s)", o.Version, len(o.Listeners), o.Binary)
	p, err := e.startActive(o.Binary)
	if err != nil {
		return fmt.Errorf("hub edge: the hub did not start: %w", err)
	}
	e.setActive(p)
	for _, ln := range o.Listeners {
		go e.accept(ln)
	}
	ctl, err := e.listenControl()
	if err != nil {
		e.logf("hub edge: control socket: %v (hub switch is not available)", err)
	} else {
		defer ctl.Close()
	}
	return e.supervise()
}

func orDefault(d *time.Duration, def time.Duration) {
	if *d <= 0 {
		*d = def
	}
}

func (e *edge) logf(format string, args ...any) { e.o.Logf(format, args...) }

// acceptLogf is what acceptloop.Run logs about the edge's sockets (its ports, the control socket).
func (e *edge) acceptLogf(format string, args ...any) { e.logf("hub edge: "+format, args...) }

// --- relaying ---------------------------------------------------------------------------------------------------

func (e *edge) accept(ln net.Listener) {
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	acceptloop.Run(e.ctx, ln, e.acceptLogf, func(c net.Conn) { go e.relay(c, port) })
}

// relay waits for a slot that serves (a switch holds connections back meanwhile), dials its socket for port, says
// whom it relays (PROXY v2) and copies both ways.
func (e *edge) relay(c net.Conn, port string) {
	if e.waiting.Add(1) > maxWaiting {
		e.waiting.Add(-1)
		_ = c.Close()
		return
	}
	header := proxyproto.AppendV2(nil, addrPort(c.RemoteAddr()), addrPort(c.LocalAddr()))
	p, upstream := e.dialActive(port, header)
	e.waiting.Add(-1)
	if upstream == nil {
		_ = c.Close()
		return
	}
	r := &relay{client: c, up: upstream}
	go r.fromClient()
	r.toClient(func() net.Conn {
		// The slot answered nothing and let go: a connection still in its socket's queue when the edge released it
		// (a switch, the edge stopping). Once another slot serves, it gets what the client sent so far, as if it came
		// first. Not after a crash: the slot may have acted on a request it had not answered yet.
		if !p.released.Load() {
			return nil
		}
		np, nc := e.dialActive(port, header)
		p = np
		return nc
	})
}

// replayLimit is how much of what a client sends first is kept for another slot (relay.toClient): a request line,
// headers and a small body; past it, the connection is not replayed.
const replayLimit = 64 << 10

// halfCloseLinger is how long a client may go on sending once the slot half-closed the connection. net/http does that
// after an early answer to a request whose body it did not read (413, a 401 on an upload), then reads on for a moment
// so that the client's last bytes do not reset the connection and throw the answer away (its rstAvoidanceDelay). Past
// it the edge closes both ways: a client cannot hold the relay open.
const halfCloseLinger = 5 * time.Second

// relay is one relayed connection. Until the slot answers its first byte, what the client sends is kept, so that a
// connection the slot never took (it stopped with it still queued) goes to the next slot instead of failing.
type relay struct {
	client net.Conn
	linger time.Duration // after the slot's half-close (zero: halfCloseLinger)

	mu        sync.Mutex
	up        net.Conn
	kept      []byte
	keeping   bool // until the slot's first byte
	overflow  bool // more than replayLimit was sent before it: no replay
	clientEOF bool
	once      sync.Once
	ended     atomic.Int32 // directions done: the second one closes both
}

func (r *relay) fromClient() {
	r.mu.Lock()
	r.keeping = true
	r.mu.Unlock()
	buf := make([]byte, 32<<10)
	for {
		n, err := r.client.Read(buf)
		if n > 0 {
			r.mu.Lock()
			if r.keeping {
				if len(r.kept)+n > replayLimit {
					r.overflow, r.kept = true, nil
				} else if !r.overflow {
					r.kept = append(r.kept, buf[:n]...)
				}
			}
			up, keeping := r.up, r.keeping
			r.mu.Unlock()
			if _, werr := up.Write(buf[:n]); werr != nil && !keeping {
				r.closeAll()
				return
			}
		}
		if err != nil {
			r.mu.Lock()
			r.clientEOF = true
			up := r.up
			r.mu.Unlock()
			halfClose(up)
			r.end()
			return
		}
	}
}

// toClient copies the slot's bytes to the client. When the slot ends before its first byte, next is asked for
// another upstream (nil: none) and gets what the client sent so far. When it half-closes after that, the client's side
// is half-closed too, and what the client still sends goes on to the slot for up to halfCloseLinger.
func (r *relay) toClient(next func() net.Conn) {
	buf := make([]byte, 32<<10)
	first := true
	for {
		r.mu.Lock()
		up := r.up
		r.mu.Unlock()
		n, err := up.Read(buf)
		if n > 0 {
			if first {
				first = false
				r.mu.Lock()
				r.keeping, r.kept = false, nil
				r.mu.Unlock()
			}
			if _, werr := r.client.Write(buf[:n]); werr != nil {
				r.closeAll()
				return
			}
		}
		if err == nil {
			continue
		}
		if !first && errors.Is(err, io.EOF) {
			linger := r.linger
			if linger <= 0 {
				linger = halfCloseLinger
			}
			halfClose(r.client)
			_ = r.client.SetReadDeadline(time.Now().Add(linger)) // fromClient ends then
			_ = up.SetWriteDeadline(time.Now().Add(linger))      // also when the slot stopped reading
			r.end()
			return
		}
		if first {
			r.mu.Lock()
			replay := !r.overflow && replayable(r.kept)
			r.mu.Unlock()
			if replay {
				if nc := next(); nc != nil {
					r.mu.Lock()
					_ = r.up.Close()
					r.up = nc
					_, werr := nc.Write(r.kept)
					eof := r.clientEOF
					r.mu.Unlock()
					if werr == nil {
						if eof {
							halfClose(nc)
						}
						continue
					}
				}
			}
		}
		r.closeAll()
		return
	}
}

// replayable: what the client sent can go to another slot without anything happening twice. A released slot may
// have run a request it never answered (its stop cut the handler off), so only what cannot have done anything there
// is replayed: a TLS handshake the slot never answered (no handshake, no request), and a request that changes nothing
// (GET, HEAD, OPTIONS; a WebSocket upgrade is a GET). Nothing sent yet: nothing to repeat either.
func replayable(sent []byte) bool {
	if len(sent) == 0 || sent[0] == 0x16 { // a TLS handshake record
		return true
	}
	for _, m := range []string{"GET ", "HEAD ", "OPTIONS "} {
		if bytes.HasPrefix(sent, []byte(m)) {
			return true
		}
	}
	return false
}

// end is one direction done: once both are, the connection closes both ways.
func (r *relay) end() {
	if r.ended.Add(1) == 2 {
		r.closeAll()
	}
}

func (r *relay) closeAll() {
	r.once.Do(func() {
		_ = r.client.Close()
		r.mu.Lock()
		_ = r.up.Close()
		r.mu.Unlock()
	})
}

func halfClose(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		_ = c.Close()
	}
}

// dialActive waits for a slot that serves (a switch holds connections back meanwhile), dials its socket for port
// and writes header; nil once HoldMax is over or the edge stops.
func (e *edge) dialActive(port string, header []byte) (*proc, net.Conn) {
	deadline := time.NewTimer(e.o.HoldMax)
	defer deadline.Stop()
	for {
		e.mu.Lock()
		gate, p := e.gate, e.active
		e.mu.Unlock()
		select {
		case <-gate:
		case <-deadline.C:
			return nil, nil
		case <-e.ctx.Done():
			return nil, nil
		}
		if p == nil {
			continue
		}
		path, ok := p.sockets[port]
		if !ok {
			return nil, nil
		}
		uc, err := net.DialTimeout("unix", path, 5*time.Second)
		if err == nil {
			if _, err = uc.Write(header); err == nil {
				return p, uc
			}
			_ = uc.Close()
		}
		// The slot is going away (a switch has not held connections back yet) or died: wait for the next one.
		select {
		case <-time.After(50 * time.Millisecond):
		case <-deadline.C:
			return nil, nil
		case <-e.ctx.Done():
			return nil, nil
		}
	}
}

func addrPort(a net.Addr) netip.AddrPort {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.AddrPort()
	}
	return netip.AddrPort{}
}

// hold makes new connections wait (a switch, a slot that died); unhold lets them through to the active slot.
func (e *edge) hold() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.held {
		e.gate, e.held = make(chan struct{}), true
	}
}

func (e *edge) unhold() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.held && e.active != nil {
		close(e.gate)
		e.held = false
	}
}

func (e *edge) setActive(p *proc) {
	e.mu.Lock()
	e.active = p
	e.mu.Unlock()
	if p != nil {
		e.unhold()
	}
}

func (e *edge) current() *proc {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active
}

// --- slots ------------------------------------------------------------------------------------------------------

// proc is one slot: a hub process the edge started.
type proc struct {
	name, binary string
	cmd          *exec.Cmd
	out          *lineWriter
	msgs         chan Msg // closed once the slot's pipe ends
	exited       chan struct{}
	exitCode     int
	hello        Msg
	sockets      map[string]string // port -> path
	started      time.Time
	released     atomic.Bool // the edge asked it to stop (release)
}

func (p *proc) send(m Msg) { _ = p.out.send(m) }

func (p *proc) alive() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// spawn starts binary as the next slot: standby waits for activate, otherwise it serves at once.
func (e *edge) spawn(binary string, standby bool) (*proc, error) {
	name := string(rune('a' + e.slots%2))
	e.slots++
	p := &proc{name: name, binary: binary, msgs: make(chan Msg, 8), exited: make(chan struct{}), sockets: map[string]string{}}
	var socks string
	for _, ln := range e.o.Listeners {
		_, port, _ := net.SplitHostPort(ln.Addr().String())
		path := filepath.Join(e.o.RunDir, "slot-"+name+"-"+port+".sock")
		p.sockets[port] = path
		if socks != "" {
			socks += ";"
		}
		socks += port + "=" + path
	}
	toSlotR, toSlotW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	fromSlotR, fromSlotW, err := os.Pipe()
	if err != nil {
		_ = toSlotR.Close()
		_ = toSlotW.Close()
		return nil, err
	}
	role := "active"
	if standby {
		role = "standby"
	}
	env := append(os.Environ(), EnvSlot+"="+name, EnvRole+"="+role, EnvSockets+"="+socks)
	cmd := exec.Command(binary, e.o.Args...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{toSlotR, fromSlotW}       // fds 3 and 4 (slot.go)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // its own group: what it started goes with it (kill)
	err = cmd.Start()
	_ = toSlotR.Close()
	_ = fromSlotW.Close()
	if err != nil {
		_ = toSlotW.Close()
		_ = fromSlotR.Close()
		return nil, err
	}
	p.cmd, p.out, p.started = cmd, &lineWriter{w: toSlotW}, time.Now()
	go readMsgs(fromSlotR, p.msgs)
	go func() {
		_ = cmd.Wait()
		p.exitCode = cmd.ProcessState.ExitCode()
		_ = toSlotW.Close()
		_ = fromSlotR.Close()
		for _, path := range p.sockets {
			_ = os.Remove(path)
		}
		close(p.exited)
	}()
	e.logf("hub edge: slot %s started (%s, %s, pid %d)", name, binary, role, cmd.Process.Pid)
	return p, nil
}

// await reads p's messages until want: hello is kept on the way. A failed message, the slot exiting or the time
// running out is the error.
func (e *edge) await(p *proc, want string, within time.Duration) error {
	t := time.NewTimer(within)
	defer t.Stop()
	for {
		select {
		case m, ok := <-p.msgs:
			if !ok {
				<-p.exited
				return fmt.Errorf("it exited (code %d)", p.exitCode)
			}
			switch m.T {
			case MsgHello:
				p.hello = m
			case MsgFailed:
				return errors.New(m.Reason)
			}
			if m.T == want {
				return nil
			}
		case <-t.C:
			return fmt.Errorf("no %s within %s", want, within)
		case <-e.ctx.Done():
			return e.ctx.Err()
		}
	}
}

// kill ends p and what it started, at once.
func (e *edge) kill(p *proc) {
	if p == nil || !p.alive() {
		return
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	<-p.exited
}

// release asks p to stop the way a planned stop does and waits for it; past ReleaseWait it is killed.
func (e *edge) release(p *proc) {
	if p == nil || !p.alive() {
		return
	}
	p.released.Store(true)
	p.send(Msg{T: MsgRelease})
	select {
	case <-p.exited:
	case <-time.After(e.o.ReleaseWait):
		e.logf("hub edge: slot %s did not stop within %s: killed", p.name, e.o.ReleaseWait)
		e.kill(p)
	}
}

// startActive runs binary as a slot that serves at once and waits until it does.
func (e *edge) startActive(binary string) (*proc, error) {
	p, err := e.spawn(binary, false)
	if err != nil {
		return nil, err
	}
	if err := e.await(p, MsgReady, e.o.ActivateWait); err != nil {
		e.kill(p)
		return nil, err
	}
	e.logf("hub edge: slot %s serving (%s, build %d)", p.name, p.hello.Version, p.hello.Build)
	return p, nil
}

// --- the edge's own loop ------------------------------------------------------------------------------------------

// startAgain has the loop start binary as the serving slot, after the backoff the failures so far earn.
func (e *edge) startAgain(binary string) {
	e.retryBinary = binary
	e.retry = time.After(backoff(e.failures))
}

func (e *edge) supervise() error {
	for {
		p := e.current()
		var msgs <-chan Msg
		var exited <-chan struct{}
		if p != nil {
			msgs, exited = p.msgs, p.exited
		}
		var confirm <-chan time.Time
		if e.confirmBy != nil {
			confirm = e.confirmBy.C
		}
		select {
		case <-e.ctx.Done():
			e.stop()
			return nil
		case m, ok := <-msgs:
			if !ok {
				<-p.exited
				continue // exited, next round
			}
			e.fromActive(p, m)
		case <-exited:
			e.setActive(nil)
			e.hold()
			e.clearConfirm()
			if p.exitCode == RespawnCode {
				e.logf("hub edge: slot %s asked to be started again", p.name)
				e.failures = 0
			} else {
				e.logf("hub edge: slot %s exited (code %d); starting it again", p.name, p.exitCode)
				if time.Since(p.started) > time.Minute {
					e.failures = 0
				}
				e.failures++
			}
			if e.failures >= maxFailedStarts {
				return fmt.Errorf("hub edge: the hub exited %d times in a row", e.failures)
			}
			e.startAgain(p.binary)
		case <-e.retry:
			e.retry = nil
			np, err := e.startActive(e.retryBinary)
			if err != nil {
				e.failures++
				e.logf("hub edge: the hub did not start: %v", err)
				if e.failures >= maxFailedStarts {
					return fmt.Errorf("hub edge: the hub did not start %d times in a row: %w", e.failures, err)
				}
				e.startAgain(e.retryBinary)
				continue
			}
			e.setActive(np)
		case r := <-e.reqs:
			if p == nil {
				r.done <- errors.New("no hub is serving right now (it is being started again)")
				continue
			}
			r.done <- e.switchTo(r)
		case <-confirm:
			e.unconfirmed()
		}
	}
}

func backoff(failures int) time.Duration {
	d := time.Second
	for i := 1; i < failures && d < 30*time.Second; i++ {
		d *= 2
	}
	return d
}

// fromActive handles what the serving slot says on its own.
func (e *edge) fromActive(p *proc, m Msg) {
	switch m.T {
	case MsgSwitch:
		if err := validBinary(m.Binary); err != nil {
			p.send(Msg{T: MsgSwitchFailed, Reason: err.Error()})
			return
		}
		if err := e.switchTo(switchReq{m: m, from: p, progress: func(string) {}}); err != nil && p.alive() && e.current() == p {
			p.send(Msg{T: MsgSwitchFailed, Reason: err.Error()})
		}
	case MsgConfirmed:
		if e.confirmBy != nil {
			e.logf("hub edge: the update to build %d is confirmed", e.confirmBuild)
		}
		e.clearConfirm()
	}
}

func (e *edge) clearConfirm() {
	if e.confirmBy != nil {
		e.confirmBy.Stop()
		e.confirmBy = nil
	}
}

// switchTo replaces the active slot by r's binary: the new one starts as a standby and checks itself while the old
// one serves; only then are connections held back, the old one released, the new one activated, and the route moved.
// A new binary that does not start leaves the old one serving; one that fails while taking over is replaced by the
// binary that ran before (r's Prev, else the old slot's).
func (e *edge) switchTo(r switchReq) error {
	m, old := r.m, e.current()
	progress := r.progress
	if progress == nil {
		progress = func(string) {}
	}
	e.logf("hub edge: switching to %s (%s)", m.Binary, m.Kind)
	progress("starting")
	green, err := e.spawn(m.Binary, true)
	if err != nil {
		return fmt.Errorf("start %s: %w", m.Binary, err)
	}
	err = e.await(green, MsgHello, e.o.StandbyWait)
	if err == nil && green.hello.Proto < 1 {
		err = errors.New("it does not speak the edge's protocol")
	}
	if err == nil && m.Expect != 0 && green.hello.Build != m.Expect {
		err = fmt.Errorf("it is build %d, not %d", green.hello.Build, m.Expect)
	}
	if err == nil {
		err = e.await(green, MsgStandby, e.o.StandbyWait)
	}
	if err != nil {
		e.kill(green)
		e.logf("hub edge: %s did not start: %v; slot %s goes on", m.Binary, err, nameOf(old))
		return fmt.Errorf("the new hub did not start: %w", err)
	}
	progress("handing over")
	e.hold()
	e.clearConfirm()
	e.release(old)
	e.setActive(nil)
	green.send(Msg{T: MsgActivate})
	if err := e.await(green, MsgReady, e.o.ActivateWait); err != nil {
		e.kill(green)
		back := m.Prev
		if m.Kind == KindUpdate && e.o.RestoreUpdate != nil {
			if b, ok := e.o.RestoreUpdate(m.Expect, err); ok {
				back = b // the update's files are back as they were: the binary at its path is the old one again
			}
		}
		if back == "" && old != nil {
			back = old.binary
		}
		e.logf("hub edge: %s did not take over: %v; going back to %s", m.Binary, err, back)
		progress("going back")
		np, berr := e.startActive(back)
		if berr != nil {
			// Nothing serves: the edge's loop starts it again, as after a crash (hold stays on meanwhile).
			e.logf("hub edge: %s did not start either: %v", back, berr)
			e.failures++
			e.startAgain(back)
			return fmt.Errorf("the new hub did not take over (%v), and %s did not start again (%v)", err, back, berr)
		}
		e.setActive(np)
		return fmt.Errorf("the new hub did not take over: %w; back on the one before", err)
	}
	e.setActive(green)
	e.logf("hub edge: slot %s serving (%s, build %d)", green.name, green.hello.Version, green.hello.Build)
	if m.Kind == KindUpdate {
		e.confirmBuild = m.Expect
		e.confirmBy = time.NewTimer(e.o.ConfirmWait)
	}
	progress("serving")
	return nil
}

func nameOf(p *proc) string {
	if p == nil {
		return "-"
	}
	return p.name
}

// unconfirmed: the slot an update started did not confirm it in time (it hangs, or cannot tell): what ran before is
// put back and runs again, the way the old watchdog did it (update-watch).
func (e *edge) unconfirmed() {
	build := e.confirmBuild
	e.clearConfirm()
	if e.o.RestoreUpdate == nil {
		return
	}
	binary, ok := e.o.RestoreUpdate(build, nil)
	if !ok {
		return // confirmed or rolled back meanwhile
	}
	e.logf("hub edge: the update to build %d was not confirmed within %s: going back", build, e.o.ConfirmWait)
	if err := e.switchTo(switchReq{m: Msg{T: MsgSwitch, Binary: binary, Kind: KindRollback}}); err != nil {
		e.logf("hub edge: going back: %v", err)
	}
}

// stop ends the edge: the listeners first (with socket activation, new connections wait in the socket for the next
// edge), then the serving slot the way a planned stop does.
func (e *edge) stop() {
	for _, ln := range e.o.Listeners {
		_ = ln.Close()
	}
	p := e.current()
	e.hold()
	if p != nil {
		e.logf("hub edge: stopping slot %s", p.name)
		e.release(p)
	}
}

func validBinary(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%q is not an absolute path", path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable file", path)
	}
	return nil
}
