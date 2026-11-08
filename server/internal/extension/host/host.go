// Package host runs plugins for the hub (docs/plans/plan-extensions.md "4" and "6"): it starts a plugin's program when
// something needs it and stops it when nothing does, restarts it with a backoff when it exits, speaks the plugin
// protocol with it (initialize, ping, namespaced calls over internal/rpc), and keeps the HTTP side of a plugin that
// serves one (GET /health). A plugin elsewhere is reached on its sockets instead of started.
package host

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/rpcio"

	"github.com/nwverse/persona/server/internal/i18n"
)

// States a plugin's process is in.
const (
	StateStopped  = "stopped"  // not running, and nothing needs it (not an error)
	StateStarting = "starting" // started, not ready yet
	StateRunning  = "running"
	StateError    = "error" // exited, failed to start, or not answering; it is tried again after a backoff
)

// Timings (variables: tests shorten them).
var (
	RestartFirst  = 2 * time.Second
	RestartLast   = 5 * time.Minute
	RanAWhile     = time.Minute
	ReadyWithin   = 30 * time.Second // initialize, or a first good /health for a lazily started one
	DefaultIdle   = 10 * time.Minute
	DefaultCall   = 30 * time.Second
	PingEvery     = 30 * time.Second
	PingTimeout   = 10 * time.Second
	HealthFirst   = time.Second      // /health while it is starting
	HealthEvery   = 10 * time.Second // and once it is up
	HealthTimeout = 5 * time.Second  // for one /health answer
	StopGrace     = 5 * time.Second  // after stdin closes, and again after SIGTERM
	StrikesToKill = 3                // timeouts or missed pings in a row before it is restarted
	LogLineMost   = 4 << 10
)

// ErrNotRunning is a call to a plugin that is not running and is not started by calls.
var ErrNotRunning = i18n.ErrEnglish("extension-status.not_running")

// Spec is how to run one plugin.
type Spec struct {
	ID, Name string

	// Command is the program (an absolute path); "" when the hub does not run it (RPCSocket / Socket elsewhere).
	Command string
	Args    []string
	Dir     string
	Env     []string // added to the hub's environment
	// StartEnv is more environment, asked at each start of the program (a credential made new each time).
	StartEnv func() []string
	// CleanEnv: the program gets only the basics of the hub's environment (PassEnv), not all of it: the hub's may
	// hold its own credentials (a database URL). An installed plugin's is clean; the hub's own and hub.json's are not.
	CleanEnv bool
	// Wrap, when set, is the command line that really runs Command and Args (a sandbox around it), and more environment
	// for that one start; asked at each start.
	Wrap func(command string, args []string) (string, []string, []string, error)
	// Prepare is Wrap with per-run resource ownership (for sealed artifacts).
	// It takes precedence over Wrap. Disposal runs on every start failure and
	// after the wrapper exits. Files are passed only to the trusted launcher;
	// its namespace setup must consume them, never retain them in the guest.
	Prepare func(command string, args []string) (string, []string, []string, []*os.File, func(), error)

	// RPC: it speaks the plugin protocol, on stdin / stdout when the hub runs it, else on RPCSocket.
	RPCBounds rpc.Limits // source-runtime budgets; zero preserves legacy process peers
	RPC       bool
	RPCSocket string
	// HTTP: it serves HTTP on Socket (the hub's path for a program it runs: removed before each start and after each run).
	HTTP   bool
	Socket string

	// Startup: it runs from the start. OnCall / OnHTTP: a call / a relayed request starts it, and it stops after
	// Idle without either. Holds (Hold) keep it running too.
	Startup, OnCall, OnHTTP bool
	Idle                    time.Duration
	CallTimeout             time.Duration

	// Init is what initialize sends, asked at each start (the configuration and secrets of the moment).
	Init func(ctx context.Context) (extension.InitializeParams, error)
	// Check runs before each start (a built-in program must be the hub's build); its error is the status's.
	Check func(ctx context.Context) error
	// Accept holds what the program said to what the hub expects; an error stops it.
	Accept func(extension.InitializeResult) error
	// Handle answers the program's calls; OnEvent takes its events.
	Handle  rpc.Handler
	OnEvent rpc.EventHandler
	// OnUp is called each time it becomes ready.
	OnUp func()
	Logf func(string, ...any)
	// Observe, when set, is told what the host sees of the plugin (docs/extensions/extensions.md "Runtime status"): every call, every
	// run, every line of its log and of the host's own about it.
	Observe Observer
}

// Observer takes what the host sees of one plugin. Its methods are called from the host's goroutines and must not
// block.
type Observer interface {
	// Call is one call: how long it took, and "" when it was answered, else the error code (the plugin's own code,
	// CodeTimeout, CodeNotRunning, CodeCanceled when the caller gave up, CodeError for one without a code).
	Call(method string, took time.Duration, code string)
	// Run is a run starting (RunStart), ending because nothing needed it (RunStop), or ending in error (RunExit,
	// detail is why; it is started again after a backoff).
	Run(event, detail string)
	// Log is a line of the program's own log (LogProgram: its stderr, or its stdout without the protocol) or of the
	// host's about it (LogHub).
	Log(source, line string)
}

// What an Observer is told.
const (
	RunStart = "start"
	RunStop  = "stop"
	RunExit  = "exit"

	LogProgram = "program"
	LogHub     = "hub"

	CodeNotRunning = "not_running"
	CodeCanceled   = "canceled"
	CodeError      = "error"
)

func (s *Spec) idle() time.Duration {
	if s.Idle > 0 {
		return s.Idle
	}
	return DefaultIdle
}

func (s *Spec) callTimeout() time.Duration {
	if s.CallTimeout > 0 {
		return s.CallTimeout
	}
	return DefaultCall
}

// session is one run's protocol connection.
type session struct {
	peer   *rpc.Peer
	result extension.InitializeResult
}

// Proc is one plugin as the hub runs it.
type Proc struct {
	spec           Spec
	cancel         context.CancelFunc
	done           chan struct{}
	teardownFailed chan struct{}
	teardownErr    error // under mu; a failed reap keeps done open and prevents replacement
	kick           chan struct{}
	client         *http.Client // to its HTTP socket

	mu         sync.Mutex
	generation uint64 // manager-owned binding to this process run
	state      string
	err        string
	sess       *session
	ready      bool          // initialized (RPC) or answering /health (HTTP only)
	change     chan struct{} // closed and replaced whenever state, sess or ready change
	holds      int
	openHTTP   int
	calls      int // in flight
	lastUse    time.Time
	strikes    int
	kill       func() // ends the current run (a restart); nil between runs
}

// Start runs spec until Stop or ctx ends. Nothing starts before something needs it, unless spec.Startup.
func Start(ctx context.Context, spec Spec) *Proc {
	if spec.Logf == nil {
		spec.Logf = func(string, ...any) {}
	}
	if spec.Name == "" {
		spec.Name = spec.ID
	}
	ctx, cancel := context.WithCancel(ctx)
	p := &Proc{spec: spec, cancel: cancel, done: make(chan struct{}), teardownFailed: make(chan struct{}), kick: make(chan struct{}, 1), state: StateStopped,
		change: make(chan struct{})}
	if spec.Startup {
		p.state = StateStarting // it starts right away
	}
	if spec.HTTP {
		dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", spec.Socket)
		}
		p.client = &http.Client{Transport: &http.Transport{DialContext: dial}, Timeout: 5 * time.Second}
	}
	go func() {
		defer close(p.done)
		p.loop(ctx)
	}()
	return p
}

// Stop waits for actual reap/disposal, preserving callers' deferred resource
// holds. Use StopContext for bounded failure reporting; a failed result is not
// completion. The legacy void API cannot report failure and release safely.
func (p *Proc) Stop() {
	if err := p.StopContext(context.Background()); err != nil {
		p.note("stopping: %v", err)
		<-p.done
	}
}

// StopContext bounds the caller's wait and reports teardown failure without
// releasing owned resources or allowing a replacement before actual reap.
func (p *Proc) StopContext(ctx context.Context) error {
	p.cancel()
	if p.client != nil {
		p.client.CloseIdleConnections()
	}
	select {
	case <-p.done:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.teardownErr
	case <-p.teardownFailed:
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.teardownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done closes only after leader reap, proven owned-group absence and disposal.
func (p *Proc) Done() <-chan struct{} { return p.done }

// Generation identifies the active initialized process run, never a source claim.
func (p *Proc) Generation() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess == nil || !p.ready {
		return 0
	}
	return p.generation
}

// Spec is what the plugin runs as.
func (p *Proc) Spec() Spec { return p.spec }

// Status is its state and, in error, why.
func (p *Proc) Status() (state, err string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state, p.err
}

// Result is what the program said at initialize this run (false while not initialized).
func (p *Proc) Result() (extension.InitializeResult, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sess == nil {
		return extension.InitializeResult{}, false
	}
	return p.sess.result, true
}

func (p *Proc) setLocked(state, err string) {
	p.state, p.err = state, err
	close(p.change)
	p.change = make(chan struct{})
}

func (p *Proc) set(state, err string) {
	p.mu.Lock()
	p.setLocked(state, err)
	p.mu.Unlock()
}

func (p *Proc) poke() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *Proc) touch() {
	p.mu.Lock()
	p.lastUse = time.Now()
	p.mu.Unlock()
	p.poke()
}

// wantedLocked says whether something needs it running now.
func (p *Proc) wantedLocked() bool {
	lazy := (p.spec.OnCall || p.spec.OnHTTP) && !p.lastUse.IsZero() && time.Since(p.lastUse) < p.spec.idle()
	return p.spec.Startup || p.holds > 0 || p.openHTTP > 0 || lazy
}

// Idle says whether nothing is using it now: no call in flight, no HTTP request or WebSocket open (a connector's hold
// does not count: it holds the plugin for as long as it is on).
func (p *Proc) Idle() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls == 0 && p.openHTTP == 0
}

// Hold keeps it running until release is called (a switched-on connector holds its plugin).
func (p *Proc) Hold() (release func()) {
	p.mu.Lock()
	p.holds++
	p.mu.Unlock()
	p.poke()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.holds--
			p.mu.Unlock()
			p.poke()
		})
	}
}

// Wait waits until it is ready or stops trying: nil once ready; ctx's error, or the run's error after it failed and
// nothing will start it again before a backoff.
func (p *Proc) Wait(ctx context.Context) error {
	for {
		p.mu.Lock()
		ready, state, why, ch := p.ready, p.state, p.err, p.change
		teardown := p.teardownErr
		p.mu.Unlock()
		switch {
		case ready:
			return nil
		case state == StateError:
			return &callFailure{message: i18n.ErrEnglish("extension-status.host_failure", i18n.Args{"name": p.spec.Name, "err": why}), cause: teardown}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

// Changes is closed at the next change of state (a connector refreshes when its plugin comes up).
func (p *Proc) Changes() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.change
}

// Ready says whether it is ready now.
func (p *Proc) Ready() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ready
}

// Call runs a method on the program: when it is not running, a plugin started by calls is started and waited for;
// any other is ErrNotRunning. A call that times out is answered with the timeout code, and the program is
// restarted after StrikesToKill of them in a row.
func (p *Proc) Call(ctx context.Context, method string, params, out any) error {
	if p.spec.Observe == nil {
		return p.call(ctx, method, params, out)
	}
	start := time.Now()
	err := p.call(ctx, method, params, out)
	p.spec.Observe.Call(method, time.Since(start), callCode(ctx, err))
	return err
}

// callCode is how an Observer is told a call ended.
func callCode(ctx context.Context, err error) string {
	var re *rpc.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &re) && re.Code != "":
		return re.Code
	case errors.Is(err, ErrNotRunning):
		return CodeNotRunning
	case ctx.Err() != nil:
		return CodeCanceled
	}
	return CodeError
}

func (p *Proc) call(ctx context.Context, method string, params, out any) error {
	if !p.spec.RPC {
		return i18n.ErrEnglish("extension-status.no_protocol", i18n.Args{"name": p.spec.Name})
	}
	p.mu.Lock()
	s := p.sess
	lazy := p.spec.OnCall
	starting := p.state == StateStarting
	p.calls++
	p.mu.Unlock()
	defer func() { p.mu.Lock(); p.calls--; p.mu.Unlock() }()
	if lazy {
		// The call keeps it running while it waits and runs (a slow start must not look idle).
		release := p.Hold()
		defer func() { p.touch(); release() }()
		p.touch()
	}
	if s == nil {
		if !lazy && !starting {
			return &callFailure{message: i18n.ErrEnglish("extension-status.host_failure", i18n.Args{"name": p.spec.Name, "err": ErrNotRunning}), cause: ErrNotRunning}
		}
		wctx, cancel := context.WithTimeout(ctx, ReadyWithin)
		err := p.Wait(wctx)
		cancel()
		if err != nil {
			return err
		}
		p.mu.Lock()
		s = p.sess
		p.mu.Unlock()
		if s == nil {
			return &callFailure{message: i18n.ErrEnglish("extension-status.host_failure", i18n.Args{"name": p.spec.Name, "err": ErrNotRunning}), cause: ErrNotRunning}
		}
	}
	cctx, cancel := context.WithTimeout(ctx, p.spec.callTimeout())
	defer cancel()
	err := s.peer.Call(cctx, method, params, out)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		p.strike(s, method+" timed out")
		timeout := newCallTimeout(p.spec.Name, method, p.spec.callTimeout())
		timeout.cause = errors.Join(timeout.cause, err)
		return timeout
	}
	if err == nil {
		p.mu.Lock()
		if p.sess == s {
			p.strikes = 0
		}
		p.mu.Unlock()
	}
	return err
}

// Notify sends the program an event.
func (p *Proc) Notify(event string, params any) error {
	p.mu.Lock()
	s := p.sess
	p.mu.Unlock()
	if s == nil {
		return ErrNotRunning
	}
	return s.peer.Notify(event, params)
}

// strike counts a timeout or a missed ping; enough in a row restart the program.
func (p *Proc) strike(s *session, why string) {
	p.mu.Lock()
	if p.sess != s {
		p.mu.Unlock()
		return // a late call must not penalize or terminate a replacement run
	}
	p.strikes++
	kill := p.kill
	n := p.strikes
	if n >= StrikesToKill {
		p.strikes = 0
	}
	p.mu.Unlock()
	if n >= StrikesToKill && kill != nil {
		p.note("%s, %d times in a row: restarting it", why, n)
		kill()
	}
}

// BeginHTTP is a request about to be relayed to the plugin's HTTP side: a plugin started by requests is started and
// waited for. end is called when the request is over (a WebSocket's end included).
func (p *Proc) BeginHTTP(ctx context.Context) (end func(), err error) {
	p.mu.Lock()
	p.openHTTP++
	p.lastUse = time.Now()
	ready := p.ready
	p.mu.Unlock()
	p.poke()
	end = func() {
		p.mu.Lock()
		p.openHTTP--
		p.lastUse = time.Now()
		p.mu.Unlock()
		p.poke()
	}
	if !ready && p.spec.OnHTTP {
		wctx, cancel := context.WithTimeout(ctx, ReadyWithin)
		defer cancel()
		if err := p.Wait(wctx); err != nil {
			end()
			return nil, err
		}
	}
	return end, nil
}

// Healthy says whether its HTTP side answers GET /health with 200.
func (p *Proc) Healthy(ctx context.Context) bool {
	if p.client == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, HealthTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://plugin/health", nil)
	res, err := p.client.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

// Client is an HTTP client to its socket.
func (p *Proc) Client() *http.Client { return p.client }

// loop starts the program whenever something needs it, until ctx ends.
func (p *Proc) loop(ctx context.Context) {
	wait := RestartFirst
	for {
		if !p.waitWanted(ctx) {
			p.set(StateStopped, "")
			return
		}
		start := time.Now()
		started := false
		err := p.runOnce(ctx, &started)
		p.mu.Lock()
		p.sess, p.ready, p.kill, p.strikes = nil, false, nil, 0
		p.mu.Unlock()
		var teardown *teardownFailure
		if errors.As(err, &teardown) {
			// Keep the run's capacity and disposal owned by its one Wait goroutine.
			// StopContext reports now, but neither Done nor a new run may pass reap.
			p.publishTeardownFailure(err)
			<-teardown.child.exited
			if started {
				p.run(RunExit, err.Error())
			}
			if p.spec.HTTP && p.spec.Socket != "" {
				p.removeSocket()
			}
			return
		}
		if ctx.Err() != nil {
			if started {
				if err == nil || errors.Is(err, context.Canceled) {
					p.run(RunStop, "")
				} else {
					p.run(RunExit, err.Error())
				}
			}
			p.set(StateStopped, "")
			return
		}
		if err == nil { // nothing needed it any more
			p.run(RunStop, "")
			p.set(StateStopped, "")
			wait = RestartFirst
			continue
		}
		if time.Since(start) >= RanAWhile {
			wait = RestartFirst
		}
		p.set(StateError, err.Error())
		if started {
			p.run(RunExit, err.Error())
		}
		p.note("%v (again in %s)", err, wait)
		select {
		case <-ctx.Done():
			p.set(StateStopped, "")
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, RestartLast)
	}
}

// publishTeardownFailure is shared by the independent watcher and run loop.
// Reporting cannot depend on an Init/Accept callback returning. It does not
// complete a run, dispose it, or decrement the observer's active-run count.
func (p *Proc) publishTeardownFailure(err error) {
	var teardown *teardownFailure
	if !errors.As(err, &teardown) {
		return
	}
	p.mu.Lock()
	if p.teardownErr != nil {
		p.mu.Unlock()
		return
	}
	p.teardownErr = err
	p.sess, p.ready = nil, false
	p.setLocked(StateError, err.Error())
	close(p.teardownFailed)
	p.mu.Unlock()
	p.cancel()
	p.note("teardown failed while retaining owned run: %v", err)
}

// waitWanted waits until something needs it; false when ctx ended first.
func (p *Proc) waitWanted(ctx context.Context) bool {
	for {
		p.mu.Lock()
		wanted := p.wantedLocked()
		p.mu.Unlock()
		if wanted {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-p.kick:
		}
	}
}

// child is a run's program, or a remote plugin's connection.
type child struct {
	cmd   *exec.Cmd
	stdin io.Closer
	conn  net.Conn
	// leaderDone is only a lifecycle trigger, not owned-run completion. exited
	// requires leader reap, proven group absence, and disposal. One owner alone
	// may signal the pinned group and call cmd.Wait.
	leaderDone    chan struct{}
	stopRequest   chan struct{}
	stopOnce      sync.Once
	ownershipMu   sync.Mutex
	ownershipErr  error
	reaped        chan struct{} // cmd.Wait completed; group/disposal may still be active
	exited        chan struct{}
	waitErr       error
	transportOnce sync.Once
	endOnce       sync.Once
	endDone       chan struct{}
	endErr        error // published by closing endDone
}

// runOnce runs the plugin once: nil when it was stopped because nothing needed it, the reason otherwise.
func (p *Proc) runOnce(ctx context.Context, started *bool) (runErr error) {
	p.set(StateStarting, "")
	if p.spec.Check != nil {
		if err := p.spec.Check(ctx); err != nil {
			return err
		}
	}
	ch, out, err := p.spawn(ctx)
	if err != nil {
		return err
	}
	runCtx, stopRun := context.WithCancel(ctx)
	killed := make(chan struct{})
	var killOnce sync.Once
	p.mu.Lock()
	p.kill = func() { killOnce.Do(func() { close(killed) }) }
	p.mu.Unlock()
	var s *session
	var peer *rpc.Peer
	if p.spec.RPC {
		// Counted pieces plus the descriptor's unconsumed bytes let a waiting
		// withdrawal or a canceled caller tell a slow child that keeps reading
		// from one that stopped (rpc.Peer.Progress). The stdin pipe and the
		// RPCSocket unix connection are both measurable on Linux; elsewhere
		// Progress is nil and stalls never retire the transport.
		w := &rpc.ProgressWriter{W: writerOf(ch)}
		peer = rpc.New("h", w)
		peer.Interrupt = ch.closeTransport
		peer.Progress = rpc.ConsumedProgress(w.Accepted, rpcio.OutstandingQuery(w.W))
		peer.Limits = p.spec.RPCBounds
		peer.OnRequest = p.spec.Handle
		peer.OnEvent = p.spec.OnEvent
		s = &session{peer: peer}
	}
	// One run-local watcher is installed before initialization. It does not
	// depend on the run loop or any active RPC write (including inline ping).
	watchDone := make(chan struct{})
	var peerDone <-chan struct{}
	if peer != nil {
		peerDone = peer.Done()
	}
	go func() {
		defer close(watchDone)
		select {
		case <-runCtx.Done():
		case <-peerDone:
		case <-ch.leaderDone:
		case <-killed:
		}
		stopRun()
		p.publishTeardownFailure(p.end(ch, peer))
	}()
	leaderExit := false
	defer func() {
		stopRun()
		endErr := p.end(ch, peer)
		if leaderExit {
			select {
			case <-ch.reaped:
				if ch.waitErr != nil {
					runErr = ch.waitErr
				}
			default:
			}
		}
		runErr = errors.Join(runErr, endErr)
		<-watchDone
	}()
	if peer != nil {
		go func() { _ = peer.Serve(runCtx, out) }()
	}
	*started = true
	p.run(RunStart, "")
	if s != nil {
		if err := p.initialize(runCtx, s); err != nil {
			return err
		}
	}
	if err := runCtx.Err(); err != nil {
		return err // a late callback must not republish a retired run as ready
	}
	p.mu.Lock()
	if err := runCtx.Err(); err != nil || p.teardownErr != nil {
		p.mu.Unlock()
		return errors.Join(err, p.end(ch, peer))
	}
	p.generation++
	p.sess = s
	p.mu.Unlock()
	if p.spec.RPC {
		p.up()
	}

	// A program the hub started that speaks only HTTP is ready once /health answers; one that never does within
	// ReadyWithin is an error, so it is restarted with a backoff instead of staying starting for good.
	var readyBy time.Time
	if p.spec.Command != "" && p.spec.HTTP && !p.spec.RPC {
		readyBy = time.Now().Add(ReadyWithin)
	}
	health := time.NewTicker(HealthFirst)
	defer health.Stop()
	ping := time.NewTicker(PingEvery)
	defer ping.Stop()
	misses, healthMisses := 0, 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ch.leaderDone:
			leaderExit = true
			return i18n.ErrEnglish("extension-status.exited")
		case <-peerDone:
			return i18n.ErrEnglish("extension-status.connection_closed", i18n.Args{"err": s.peer.Err()})
		case <-killed:
			return i18n.ErrEnglish("extension-status.not_answering")
		case <-p.kick:
			p.mu.Lock()
			wanted := p.wantedLocked()
			p.mu.Unlock()
			if !wanted {
				return nil
			}
		case <-health.C:
			p.mu.Lock()
			wanted := p.wantedLocked()
			p.mu.Unlock()
			if !wanted {
				return nil
			}
			if !p.spec.HTTP {
				continue
			}
			if !p.checkHealth(runCtx, health) {
				healthMisses = 0
			} else if healthMisses++; healthMisses >= StrikesToKill && p.spec.Command != "" && !p.spec.RPC {
				// A hung HTTP-only program the hub runs: end the run so it is restarted. One elsewhere has
				// nothing to restart, and a protocol one is covered by its pings.
				return i18n.ErrEnglish("extension-status.missed_health", i18n.Args{"count": healthMisses})
			}
			if !readyBy.IsZero() && time.Now().After(readyBy) {
				if st, _ := p.Status(); st == StateStarting {
					return i18n.ErrEnglish("extension-status.health_timeout", i18n.Args{"duration": ReadyWithin.String()})
				}
				readyBy = time.Time{} // it came up; later failures are the health check's
			}
		case <-ping.C:
			if s == nil {
				continue
			}
			pctx, cancel := context.WithTimeout(runCtx, PingTimeout)
			err := s.peer.Call(pctx, extension.MPing, struct{}{}, nil)
			cancel()
			if err != nil {
				if misses++; misses >= StrikesToKill {
					return i18n.ErrEnglish("extension-status.missed_ping", i18n.Args{"count": misses})
				}
			} else {
				misses = 0
			}
		}
	}
}

// up marks the plugin ready.
func (p *Proc) up() {
	p.mu.Lock()
	if p.teardownErr != nil {
		p.mu.Unlock()
		return
	}
	p.ready = true
	p.setLocked(StateRunning, "")
	p.mu.Unlock()
	if p.spec.OnUp != nil {
		go p.spec.OnUp()
	}
}

// checkHealth asks /health: an HTTP-only plugin is ready once it answers. A started one stays starting until then;
// one elsewhere is an error while it does not answer. It reports a missed check: no answer after it was up.
func (p *Proc) checkHealth(ctx context.Context, t *time.Ticker) (missed bool) {
	ok := p.Healthy(ctx)
	p.mu.Lock()
	state := p.state
	p.mu.Unlock()
	missed = !ok && (state == StateRunning || state == StateError)
	switch {
	case ok && !p.spec.RPC && state != StateRunning:
		p.up()
		t.Reset(HealthEvery)
	case ok && state == StateError:
		p.set(StateRunning, "")
	case !ok && state == StateRunning:
		p.mu.Lock()
		p.ready = p.spec.RPC
		p.setLocked(StateError, i18n.T(i18n.En, "extension-status.health_failed"))
		p.mu.Unlock()
		t.Reset(HealthFirst)
	case !ok && p.spec.Command == "" && state == StateStarting:
		p.set(StateError, i18n.T(i18n.En, "extension-status.socket_not_answering", i18n.Args{"path": p.spec.Socket}))
	}
	return missed
}

// initialize sends initialize and holds the answer to the spec.
func (p *Proc) initialize(ctx context.Context, s *session) error {
	params := extension.InitializeParams{Protocol: extension.Protocol}
	if p.spec.Init != nil {
		var err error
		if params, err = p.spec.Init(ctx); err != nil {
			return err
		}
		params.Protocol = extension.Protocol
	}
	ictx, cancel := context.WithTimeout(ctx, ReadyWithin)
	defer cancel()
	var res extension.InitializeResult
	if err := s.peer.Call(ictx, extension.MInitialize, params, &res); err != nil {
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == extension.CodeWrongProtocol {
			return i18n.Err("extension-status.wrong_protocol", i18n.Args{"err": re.Message})
		}
		return i18n.Err("extension-status.not_ready", i18n.Args{"err": err.Error()})
	}
	if res.Protocol != extension.Protocol {
		return i18n.Err("extension-status.protocol_version", i18n.Args{"current": res.Protocol, "required": extension.Protocol})
	}
	if res.ID != params.Extension.ID && params.Extension.ID != "" {
		return i18n.ErrEnglish("extension-status.wrong_id", i18n.Args{"current": res.ID, "required": params.Extension.ID})
	}
	if res.Version != params.Extension.Version && params.Extension.Version != "" {
		return i18n.ErrEnglish("extension-status.wrong_version", i18n.Args{"current": res.Version, "required": params.Extension.Version})
	}
	if p.spec.Accept != nil {
		if err := p.spec.Accept(res); err != nil {
			return err
		}
	}
	s.result = res
	return nil
}

// spawn starts the program (or connects to the plugin elsewhere): what to write to and read from.
func (p *Proc) spawn(ctx context.Context) (*child, io.Reader, error) {
	if p.spec.Command == "" {
		ch := &child{exited: make(chan struct{}), endDone: make(chan struct{})}
		if !p.spec.RPC {
			return ch, nil, nil
		}
		var d net.Dialer
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := d.DialContext(dctx, "unix", p.spec.RPCSocket)
		cancel()
		if err != nil {
			return nil, nil, i18n.Err("extension-status.socket_connect", i18n.Args{"path": p.spec.RPCSocket, "err": err.Error()})
		}
		ch.conn = conn
		return ch, conn, nil
	}
	if p.spec.HTTP {
		if err := os.MkdirAll(filepath.Dir(p.spec.Socket), 0o700); err != nil {
			return nil, nil, err
		}
		_ = os.Remove(p.spec.Socket) // left by a run that was killed
	}
	if _, err := os.Stat(p.spec.Command); err != nil && filepath.IsAbs(p.spec.Command) {
		return nil, nil, i18n.Err("extension-status.program_missing", i18n.Args{"name": p.spec.Name, "path": p.spec.Command})
	}
	name, args := p.spec.Command, p.spec.Args
	var wrapEnv []string
	var files []*os.File
	var dispose func()
	started := false
	defer func() {
		if !started && dispose != nil {
			dispose()
		}
	}()
	if p.spec.Prepare != nil {
		var err error
		name, args, wrapEnv, files, dispose, err = p.spec.Prepare(name, args)
		if err != nil {
			return nil, nil, i18n.Err("extension-status.sandbox_start", i18n.Args{"name": p.spec.Name, "err": err.Error()})
		}
	} else if p.spec.Wrap != nil {
		var err error
		if name, args, wrapEnv, err = p.spec.Wrap(name, args); err != nil {
			return nil, nil, i18n.Err("extension-status.sandbox_start", i18n.Args{"name": p.spec.Name, "err": err.Error()})
		}
	}
	cmd := exec.Command(name, args...)
	cmd.ExtraFiles = files
	cmd.Dir = p.spec.Dir
	base := os.Environ()
	if p.spec.CleanEnv {
		base = basics(base)
	}
	env := p.spec.Env
	if p.spec.StartEnv != nil {
		env = append(slices.Clone(env), p.spec.StartEnv()...)
	}
	env = append(slices.Clone(env), wrapEnv...)
	cmd.Env = identity.Environment(base, env...)
	cmd.SysProcAttr = osproc.GroupAttr() // so stopping it takes its children too
	ch := &child{cmd: cmd, leaderDone: make(chan struct{}), stopRequest: make(chan struct{}), reaped: make(chan struct{}), exited: make(chan struct{}), endDone: make(chan struct{})}
	// The log pipe is ours, not cmd.StderrPipe: Wait closes those once the program exits, dropping whatever is
	// still in them, which is usually why it died. Ours is read to EOF, which comes when the program and its
	// children have all closed their end.
	logs, logsW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	cmd.Stderr = logsW
	var out io.Reader
	if p.spec.RPC {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			logs.Close()
			logsW.Close()
			return nil, nil, err
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			logs.Close()
			logsW.Close()
			stdin.Close()
			return nil, nil, err
		}
		ch.stdin, out = stdin, stdout
	} else {
		cmd.Stdout = logsW
	}
	err = cmd.Start()
	logsW.Close() // the program holds its own copy now; with ours open the reader never sees EOF
	if err != nil {
		logs.Close()
		ch.closeTransport()
		if closer, ok := out.(io.Closer); ok {
			_ = closer.Close()
		}
		return nil, nil, i18n.Err("extension-status.program_start", i18n.Args{"name": p.spec.Name, "err": err.Error()})
	}
	started = true
	go p.logLines(logs)
	go ch.ownRun(dispose)
	return ch, out, nil
}

func writerOf(ch *child) io.Writer {
	if ch.conn != nil {
		return ch.conn
	}
	if w, ok := ch.stdin.(io.Writer); ok {
		return w
	}
	return io.Discard
}

// closeTransport is independent of writer admission. spawn supplies only owned
// pollable stdin files or network connections, not arbitrary plugin io.Closers.
func (ch *child) closeTransport() {
	ch.transportOnce.Do(func() {
		if ch.conn != nil {
			_ = ch.conn.Close()
		}
		if ch.stdin != nil {
			_ = ch.stdin.Close()
		}
	})
}

var ErrTeardownTimeout = i18n.ErrEnglish("extension-status.teardown_timeout")

var errGroupCompletionUnsupported = i18n.ErrEnglish("extension-status.group_completion_unsupported")

type teardownFailure struct {
	child *child
	cause error
}

func (e *teardownFailure) Error() string { return e.Text(i18n.En) }
func (e *teardownFailure) Text(l i18n.Lang) string {
	if e.cause == nil {
		return i18n.T(l, "extension-status.teardown_timeout")
	}
	return i18n.T(l, "extension-status.teardown_detail", i18n.Args{"err": i18n.ExtensionDiagnosticText(l, e.cause.Error())})
}
func (e *teardownFailure) Unwrap() []error { return []error{ErrTeardownTimeout, e.cause} }

func (ch *child) ownershipError(err error) {
	ch.ownershipMu.Lock()
	ch.ownershipErr = err
	ch.ownershipMu.Unlock()
}

func (ch *child) teardownFailure() error {
	ch.ownershipMu.Lock()
	defer ch.ownershipMu.Unlock()
	return &teardownFailure{child: ch, cause: ch.ownershipErr}
}

// end has one run-local owner, with a finite final reap budget. cmd.Wait remains
// owned exclusively by spawn; its disposal must not run early on a timeout.
func (p *Proc) end(ch *child, peer *rpc.Peer) error {
	// Close hooks may reenter Peer.Close; neither an end-once nor lifecycle lock
	// is held here. The worker captures this child, never p's replacement session.
	if peer != nil {
		peer.Close(rpc.ErrClosed)
	}
	ch.closeTransport()
	ch.endOnce.Do(func() {
		if ch.endDone == nil {
			ch.endDone = make(chan struct{})
		}
		go func() {
			defer close(ch.endDone)
			ch.endErr = p.terminate(ch)
		}()
	})
	<-ch.endDone
	return ch.endErr
}

func (p *Proc) terminate(ch *child) error {
	if ch.cmd == nil || ch.cmd.Process == nil {
		return nil // the remote process is not ours
	}
	// Only ownRun may signal. It retains the unreaped original leader while
	// doing so; observing a channel followed by numeric PGID signaling races reap.
	ch.stopOnce.Do(func() { close(ch.stopRequest) })
	timer := time.NewTimer(3 * StopGrace)
	defer timer.Stop()
	select {
	case <-ch.exited:
		if p.spec.HTTP && p.spec.Socket != "" {
			p.removeSocket()
		}
		return nil
	case <-timer.C:
		return ch.teardownFailure()
	}
}

// removeSocket removes the HTTP socket of a program the hub ran, so none is left on disk after its run.
func (p *Proc) removeSocket() {
	if err := os.Remove(p.spec.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		p.note("removing %s: %v", p.spec.Socket, err)
	}
}

// logLines keeps what the program writes (its own log), a line at a time, each cut to LogLineMost. It owns the read
// end and is its only reader, so it reads to EOF whatever comes: the rest of a line too long to keep is dropped, not
// left in the pipe for the program to block on.
func (p *Proc) logLines(r io.ReadCloser) {
	defer r.Close()
	keep := LogLineMost + 256 // a little past the cut, so trailing spaces do not count as a longer line
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	dropped := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(line) == 0 {
			chunk = bytes.TrimLeftFunc(chunk, unicode.IsSpace)
		}
		if room := keep - len(line); len(chunk) > room {
			chunk, dropped = chunk[:room], true
		}
		line = append(line, chunk...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue // the same line goes on
		}
		p.logLine(line, dropped)
		line, dropped = line[:0], false
		if err == nil {
			continue
		}
		if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
			p.note("reading its output: %v", err)
		}
		return
	}
}

// logLine passes on one line of the program's log; dropped means some of it was not kept.
func (p *Proc) logLine(raw []byte, dropped bool) {
	line := strings.TrimSpace(string(raw))
	if line == "" {
		return
	}
	if dropped || len(line) > LogLineMost {
		line = line[:min(len(line), LogLineMost)] + "…"
	}
	p.spec.Logf("plugin %s: %s", p.spec.ID, line)
	if p.spec.Observe != nil {
		p.spec.Observe.Log(LogProgram, line)
	}
}

// note is a line of the host's own about the plugin: in the hub's log, and its Observer's.
func (p *Proc) note(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	p.spec.Logf("plugin %s: %s", p.spec.ID, line)
	if p.spec.Observe != nil {
		p.spec.Observe.Log(LogHub, line)
	}
}

// run tells the Observer about a run.
func (p *Proc) run(event, detail string) {
	if p.spec.Observe != nil {
		p.spec.Observe.Run(event, detail)
	}
}

// PassEnv are the variables of the hub's environment a plugin with a clean environment still gets: where things are,
// the locale and time zone, and how to reach the network (proxies, certificates).
var PassEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TMPDIR",
	"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy",
	"SSL_CERT_FILE", "SSL_CERT_DIR"}

func basics(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		for _, pass := range PassEnv {
			if k == pass {
				out = append(out, kv)
				break
			}
		}
	}
	return out
}
