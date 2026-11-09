package host

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/rpc"
)

// far is a plugin running on its own on a unix socket: it serves demo() on each connection, or closes it at once
// (mute), and counts the connections it took.
type far struct {
	ln      net.Listener
	accepts atomic.Int32
	mu      sync.Mutex
	conns   []net.Conn
}

func serveFar(t *testing.T, sock string, mute bool) *far {
	if mute {
		return serveFarAs(t, sock, nil)
	}
	return serveFarAs(t, sock, demo)
}

// serveFarAs is serveFar with the program each connection gets (nil: closed at once).
func serveFarAs(t *testing.T, sock string, program func() serve.Program) *far {
	if program == nil {
		return serveFarWith(t, sock, nil)
	}
	return serveFarWith(t, sock, func(c net.Conn) { _ = serve.Serve(context.Background(), c, c, program()) })
}

// serveFarWith is serveFar with what serves each connection (nil: closed at once).
func serveFarWith(t *testing.T, sock string, handle func(net.Conn)) *far {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	f := &far{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.accepts.Add(1)
			if handle == nil {
				c.Close()
				continue
			}
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			go func() { defer c.Close(); handle(c) }()
		}
	}()
	t.Cleanup(f.stop)
	return f
}

// stop is the program ending: its socket and every connection close.
func (f *far) stop() {
	f.ln.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.conns {
		c.Close()
	}
	f.conns = nil
}

func farDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hst") // a unix socket path stays short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func attachedSpec(t *testing.T, sock string, o Observer) Spec {
	s := spec(t, "")
	s.Command, s.RPCSocket, s.Attached, s.Startup, s.Observe = "", sock, true, true, o
	return s
}

func timings(t *testing.T, retry, grace time.Duration) {
	r, g := RemoteRetry, OfflineGrace
	RemoteRetry, OfflineGrace = retry, grace
	t.Cleanup(func() { RemoteRetry, OfflineGrace = r, g })
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("never: " + what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// hubLines are the host's own lines about the plugin.
func hubLines(o *observed) []string {
	_, _, logs := o.snapshot()
	var out []string
	for _, l := range logs {
		if strings.HasPrefix(l, LogHub+": ") {
			out = append(out, l)
		}
	}
	return out
}

// An attached plugin that cannot be reached is tried every RemoteRetry, not with the doubling backoff, and stays
// starting within the grace; trying again with the same result is not logged or told as another exit.
func TestAttachedTriedEveryRemoteRetry(t *testing.T) {
	timings(t, 50*time.Millisecond, time.Hour)
	sock := filepath.Join(farDir(t), "far.sock")
	f := serveFar(t, sock, true) // takes each connection and closes it: initialize never answers
	o := &observed{}
	p := Start(context.Background(), attachedSpec(t, sock, o))
	defer p.Stop()
	time.Sleep(1500 * time.Millisecond)
	// every 50ms: about 30 tries; the backoff (50ms doubling) would have made 5
	if n := f.accepts.Load(); n < 10 {
		t.Fatalf("%d tries in 1.5s", n)
	}
	if st, why := p.Status(); st != StateStarting || why != "" {
		t.Fatalf("state %s (%s), want starting within the grace", st, why)
	}
	l := p.Link()
	if l.Online || l.Why == "" || time.Since(l.Since) < time.Second {
		t.Fatalf("link %+v", l)
	}
	_, runs, _ := o.snapshot()
	if len(runs) != 1 || !strings.HasPrefix(runs[0], RunExit+" ") {
		t.Fatalf("runs %q: want the one exit, no start", runs)
	}
	if lines := hubLines(o); len(lines) != 1 || !strings.Contains(lines[0], "again in 50ms") {
		t.Fatalf("the host's lines %q: want one", lines)
	}
}

// Offline past the grace it is an error, and stays one (no flip back to starting at each try); once it answers it is
// running and online; losing it again, after it was up for longer than the grace, is starting, then an error a grace
// after the loss.
func TestAttachedGraceThenErrorThenBack(t *testing.T) {
	timings(t, 20*time.Millisecond, 300*time.Millisecond)
	sock := filepath.Join(farDir(t), "far.sock")
	o := &observed{}
	began := time.Now()
	p := Start(context.Background(), attachedSpec(t, sock, o))
	defer p.Stop()
	waitState(t, p, StateError)
	if took := time.Since(began); took < OfflineGrace {
		t.Fatalf("an error after %s, before the grace", took)
	}
	if _, why := p.Status(); !strings.Contains(why, sock) {
		t.Fatal(why)
	}
	ch := p.Changes()
	time.Sleep(200 * time.Millisecond) // ten more tries
	select {
	case <-ch:
		st, _ := p.Status()
		t.Fatalf("a try that changes nothing changed the state (now %s)", st)
	default:
	}

	f := serveFar(t, sock, false)
	waitState(t, p, StateRunning)
	l := p.Link()
	if !l.Online || l.Why != "" || l.Said != "1.0.0" || time.Since(l.Since) > time.Second {
		t.Fatalf("link online %+v", l)
	}
	var n int
	if err := p.Call(context.Background(), "demo.pid", struct{}{}, &n); err != nil || n != os.Getpid() {
		t.Fatal(n, err)
	}
	if lines := hubLines(o); !strings.Contains(strings.Join(lines, "|"), "connected after") {
		t.Fatalf("the host's lines %q", lines)
	}

	time.Sleep(OfflineGrace + 100*time.Millisecond) // a program restarted after a long time up
	lost := time.Now()
	f.stop()
	waitFor(t, "lost", func() bool { st, _ := p.Status(); return st != StateRunning })
	if st, why := p.Status(); st != StateStarting {
		t.Fatalf("lost after a time up: %s (%s), want starting within the grace", st, why)
	}
	if l := p.Link(); l.Online || l.Why == "" {
		t.Fatalf("link offline %+v", l)
	}
	waitState(t, p, StateError)
	if took := time.Since(lost); took < OfflineGrace {
		t.Fatalf("an error %s after the loss, before the grace", took)
	}
	_, runs, _ := o.snapshot()
	var starts, exits int
	for _, r := range runs {
		switch {
		case r == RunStart:
			starts++
		case strings.HasPrefix(r, RunExit+" "):
			exits++
		}
	}
	// out at first, one start when it came online, its loss, and the reason it cannot be reached again
	if starts != 1 || exits < 2 || exits > 3 {
		t.Fatalf("runs %q", runs)
	}
}

// One started by calls gets its grace from when it is needed, not from when the host began or it was last up.
func TestAttachedGraceCountsFromWhenNeeded(t *testing.T) {
	timings(t, 20*time.Millisecond, 300*time.Millisecond)
	sock := filepath.Join(farDir(t), "far.sock")
	s := attachedSpec(t, sock, nil)
	s.Startup, s.OnCall = false, true
	p := Start(context.Background(), s)
	defer p.Stop()
	time.Sleep(400 * time.Millisecond) // past the grace, but nothing needed it
	if st, _ := p.Status(); st != StateStopped {
		t.Fatal(st)
	}
	needed := time.Now()
	release := p.Hold()
	defer release()
	time.Sleep(100 * time.Millisecond) // a few tries
	if st, why := p.Status(); st != StateStarting {
		t.Fatalf("state %s (%s), want starting within the grace", st, why)
	}
	waitState(t, p, StateError)
	if took := time.Since(needed); took < OfflineGrace {
		t.Fatalf("an error %s after it was needed, before the grace", took)
	}
}

// A call while it is offline within the grace waits for it, and works once the program is back.
func TestAttachedCallWaitsWithinGrace(t *testing.T) {
	timings(t, 20*time.Millisecond, time.Hour)
	sock := filepath.Join(farDir(t), "far.sock")
	f := serveFar(t, sock, false)
	p := Start(context.Background(), attachedSpec(t, sock, nil))
	defer p.Stop()
	waitState(t, p, StateRunning)
	f.stop()
	waitState(t, p, StateStarting)
	var n int
	done := make(chan error, 1)
	go func() { done <- p.Call(context.Background(), "demo.pid", struct{}{}, &n) }()
	select {
	case err := <-done:
		t.Fatalf("the call did not wait for the program: %v", err)
	case <-time.After(200 * time.Millisecond): // ten tries
	}
	serveFar(t, sock, false)
	select {
	case err := <-done:
		if err != nil || n != os.Getpid() {
			t.Fatalf("%v (%d)", err, n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call never came back")
	}
}

// One started by calls that failed and then went unused is stopped, not left in error: needed again, it gets its grace
// anew while the program is still away, and a call waits for it and works once the program is back.
func TestAttachedIdleAfterFailing(t *testing.T) {
	timings(t, 20*time.Millisecond, 300*time.Millisecond)
	sock := filepath.Join(farDir(t), "far.sock")
	s := attachedSpec(t, sock, nil)
	s.Startup, s.OnCall, s.Idle = false, true, 200*time.Millisecond
	p := Start(context.Background(), s)
	defer p.Stop()
	var n int
	if err := p.Call(context.Background(), "demo.pid", struct{}{}, &n); err == nil {
		t.Fatal("a call with nothing there worked")
	}
	waitState(t, p, StateStopped) // unused past Idle

	needed := time.Now()
	release := p.Hold()
	time.Sleep(100 * time.Millisecond) // a few tries, the program still away
	if st, why := p.Status(); st != StateStarting {
		release()
		t.Fatalf("needed again: %s (%s), want starting within a new grace", st, why)
	}
	waitState(t, p, StateError)
	if took := time.Since(needed); took < OfflineGrace {
		t.Fatalf("an error %s after it was needed again, before the grace", took)
	}
	release()
	waitState(t, p, StateStopped)

	serveFar(t, sock, false)
	if err := p.Call(context.Background(), "demo.pid", struct{}{}, &n); err != nil || n != os.Getpid() {
		t.Fatalf("needed once the program was back: %v (%d)", err, n)
	}
}

// A program the host runs, started by calls, that failed and then went unused keeps its error: only an attached plugin
// is stopped then.
func TestRunProgramIdleAfterFailingKeepsError(t *testing.T) {
	s := spec(t, "quit")
	s.OnCall, s.Idle = true, 100*time.Millisecond
	p := Start(context.Background(), s)
	defer p.Stop()
	var n int
	if err := p.Call(context.Background(), "demo.pid", struct{}{}, &n); err == nil {
		t.Fatal("a call to a program that quits worked")
	}
	time.Sleep(s.Idle + time.Second) // past Idle, and past the restart wait that was running when it ran out
	if st, why := p.Status(); st != StateError {
		t.Fatalf("state %s (%s), want the error it was left in", st, why)
	}
}

// What an attached plugin's run was handed does not outlive the run, though it came up: after an idle stop or a lost
// connection its program goes on elsewhere. A program the host runs is released only after a run that never came up.
func TestAttachedReleasedAfterEveryRun(t *testing.T) {
	timings(t, 20*time.Millisecond, time.Hour)
	sock := filepath.Join(farDir(t), "far.sock")
	f := serveFar(t, sock, false)
	s := attachedSpec(t, sock, nil)
	s.Startup, s.OnCall, s.Idle = false, true, 200*time.Millisecond
	var inits, released atomic.Int32
	init := s.Init
	s.Init = func(ctx context.Context) (extension.InitializeParams, error) { inits.Add(1); return init(ctx) }
	s.Release = func() { released.Add(1) }
	p := Start(context.Background(), s)
	defer p.Stop()
	pid(t, p)
	if n := released.Load(); n != 0 {
		t.Fatalf("released %d times while up", n)
	}
	waitState(t, p, StateStopped) // unused past Idle, the program still listening
	waitFor(t, "the idle stop released", func() bool { return released.Load() == 1 })

	hold := p.Hold()
	defer hold()
	waitState(t, p, StateRunning)
	f.mu.Lock()
	for _, c := range f.conns { // the connection drops, the program stays
		c.Close()
	}
	f.conns = nil
	f.mu.Unlock()
	waitFor(t, "the lost connection released, then a new run", func() bool { return released.Load() == 2 && inits.Load() == 3 })
	waitState(t, p, StateRunning)
	if n := released.Load(); n != 2 {
		t.Fatalf("released %d times, want once per run that ended", n)
	}

	r := spec(t, "rpc")
	r.OnCall, r.Idle = true, 200*time.Millisecond
	var ran atomic.Int32
	r.Release = func() { ran.Add(1) }
	q := Start(context.Background(), r)
	defer q.Stop()
	pid(t, q)
	waitState(t, q, StateStopped)
	if n := ran.Load(); n != 0 {
		t.Fatalf("a program the host runs was released %d times after a run that came up", n)
	}
}

// One that answers as another version is refused: an error at once, whatever the grace, tried again with the backoff,
// and Link says what it said.
func TestAttachedOtherVersionIsRefused(t *testing.T) {
	timings(t, 10*time.Millisecond, time.Hour)
	sock := filepath.Join(farDir(t), "far.sock")
	f := serveFar(t, sock, false)
	o := &observed{}
	s := attachedSpec(t, sock, o)
	init := s.Init
	s.Init = func(ctx context.Context) (params extension.InitializeParams, err error) {
		params, err = init(ctx)
		params.Extension.Version = "2.0.0"
		return params, err
	}
	p := Start(context.Background(), s)
	defer p.Stop()
	waitState(t, p, StateError)
	if _, why := p.Status(); !strings.Contains(why, "2.0.0") {
		t.Fatal(why)
	}
	time.Sleep(time.Second)
	// the backoff from 50ms: 4 or 5 tries in a second; every RemoteRetry would be 100
	if n := f.accepts.Load(); n > 8 {
		t.Fatalf("%d tries in a second", n)
	}
	if l := p.Link(); l.Online || l.Said != "1.0.0" {
		t.Fatalf("link %+v", l)
	}
	if lines := hubLines(o); len(lines) != 1 {
		t.Fatalf("the host's lines %q: want one", lines)
	}
}

// One that answers as another program (another id) is refused for that, and the version it says is not this plugin's.
func TestAttachedOtherProgramSaysNoVersion(t *testing.T) {
	timings(t, 10*time.Millisecond, time.Hour)
	sock := filepath.Join(farDir(t), "far.sock")
	serveFar(t, sock, false)
	s := attachedSpec(t, sock, nil)
	init := s.Init
	s.Init = func(ctx context.Context) (params extension.InitializeParams, err error) {
		params, err = init(ctx)
		params.Extension.ID, params.Extension.Version = "other", "2.0.0"
		return params, err
	}
	p := Start(context.Background(), s)
	defer p.Stop()
	waitState(t, p, StateError)
	if _, why := p.Status(); !strings.Contains(why, `"other"`) {
		t.Fatal(why)
	}
	if l := p.Link(); l.Said != "" {
		t.Fatalf("link %+v", l)
	}
}

// One that answers as this extension but in another protocol is refused for that, and the version it says, in a
// protocol this host does not speak, is not recorded.
func TestAttachedOtherProtocolSaysNoVersion(t *testing.T) {
	timings(t, 10*time.Millisecond, time.Hour)
	sock := filepath.Join(farDir(t), "far.sock")
	serveFarWith(t, sock, func(c net.Conn) {
		peer := rpc.New("p", c)
		peer.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
			return extension.InitializeResult{Protocol: extension.Protocol + 1, ID: "demo", Version: "1.1.0"}, nil
		}
		_ = peer.Serve(context.Background(), c)
	})
	p := Start(context.Background(), attachedSpec(t, sock, nil))
	defer p.Stop()
	waitState(t, p, StateError)
	if _, why := p.Status(); !strings.Contains(why, "协议版本") {
		t.Fatal(why)
	}
	if l := p.Link(); l.Said != "" {
		t.Fatalf("link %+v", l)
	}
}

// A refusal whose details change is news: the reason is the new one, told once.
func TestAttachedChangedRefusalIsTold(t *testing.T) {
	timings(t, 10*time.Millisecond, time.Hour)
	last := RestartLast
	RestartLast = 100 * time.Millisecond
	t.Cleanup(func() { RestartLast = last })
	sock := filepath.Join(farDir(t), "far.sock")
	var says atomic.Value
	says.Store("1.1.0")
	f := serveFarAs(t, sock, func() serve.Program { d := demo(); d.Version = says.Load().(string); return d })
	o := &observed{}
	p := Start(context.Background(), attachedSpec(t, sock, o)) // approved: 1.0.0
	defer p.Stop()
	waitState(t, p, StateError)
	tries := f.accepts.Load()
	waitFor(t, "more tries", func() bool { return f.accepts.Load() >= tries+2 })
	if lines := hubLines(o); len(lines) != 1 {
		t.Fatalf("the host's lines %q: want one", lines)
	}

	says.Store("1.2.0") // the program on the socket is now another version
	waitFor(t, "the new answer told", func() bool { return len(hubLines(o)) >= 2 })
	tries = f.accepts.Load()
	waitFor(t, "more tries", func() bool { return f.accepts.Load() >= tries+2 })
	if lines := hubLines(o); len(lines) != 2 || !strings.Contains(lines[1], "1.2.0") {
		t.Fatalf("the host's lines %q: want the new answer once", lines)
	}
	if _, why := p.Status(); !strings.Contains(why, "1.2.0") {
		t.Fatal(why)
	}
	if l := p.Link(); l.Said != "1.2.0" || !strings.Contains(l.Why, "1.2.0") {
		t.Fatalf("link %+v", l)
	}
}

// An attached HTTP-only plugin: its /health is its link, with the same grace.
func TestAttachedHTTPOnly(t *testing.T) {
	timings(t, 20*time.Millisecond, 300*time.Millisecond)
	every := HealthEvery
	HealthEvery = 20 * time.Millisecond
	defer func() { HealthEvery = every }()
	sock := filepath.Join(farDir(t), "far.sock")
	s := spec(t, "")
	s.Command, s.RPC, s.HTTP, s.Socket, s.Attached, s.Startup = "", false, true, sock, true, true
	began := time.Now()
	p := Start(context.Background(), s)
	defer p.Stop()
	if st, _ := p.Status(); st != StateStarting {
		t.Fatal(st)
	}
	waitState(t, p, StateError)
	if took := time.Since(began); took < OfflineGrace {
		t.Fatalf("an error after %s, before the grace", took)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
	go srv.Serve(ln)
	waitState(t, p, StateRunning)
	if l := p.Link(); !l.Online || l.Why != "" {
		t.Fatalf("link %+v", l)
	}
	srv.Close()
	waitState(t, p, StateStarting)
	if l := p.Link(); l.Online || !strings.Contains(l.Why, sock) {
		t.Fatalf("link %+v", l)
	}
	waitState(t, p, StateError)
}
