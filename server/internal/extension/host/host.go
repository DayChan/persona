// Package host runs plugins for the hub (docs/plans/plan-extensions.md "4" and "6"): it starts a plugin's program when
// something needs it and stops it when nothing does, restarts it with a backoff when it exits, speaks the plugin
// protocol with it (initialize, ping, namespaced calls over internal/rpc), and keeps the HTTP side of a plugin that
// serves one (GET /health). A plugin elsewhere is reached on its sockets instead of started.
package host

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
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
	RemoteRetry   = 10 * time.Second // an attached plugin that cannot be reached is tried again this often
	OfflineGrace  = 30 * time.Second // and is starting for this long after it went offline, then an error
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
	// Attached: a plugin elsewhere (Command "") that runs on its own. One that cannot be reached is tried again every
	// RemoteRetry and stays starting for OfflineGrace before it is an error; one that answers and is refused (another
	// protocol, id or version) is an error at once and tried again with the backoff. Link says how it is connected.
	Attached bool

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

func (s *Spec) attached() bool { return s.Attached && s.Command == "" }

// Link is how an attached plugin is connected.
type Link struct {
	Online bool
	// Since is when it came online, went offline, or was needed while it was stopped.
	Since time.Time
	// Why it is offline, also while it is still starting; "" online.
	Why string
	// Said is the version it gave when it last answered initialize as this plugin (its protocol and id), whether or not
	// the hub took it; "" once a try fails before that.
	Said string
}

// refused is an initialize answer the hub does not take: the program is there, so trying again soon changes nothing.
type refused struct{ error }

func (r refused) Unwrap() error { return r.error }

// session is one run's protocol connection.
type session struct {
	peer   *rpc.Peer
	result extension.InitializeResult
}

// Proc is one plugin as the hub runs it.
type Proc struct {
	spec   Spec
	cancel context.CancelFunc
	done   chan struct{}
	kick   chan struct{}
	client *http.Client // to its HTTP socket

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
	// An attached plugin's Link; linkKind is what failed last (failureKind; a refusal's whole text), so one that stays
	// away is not logged each try.
	linkSince time.Time
	linkWhy   string
	linkKind  string
	said      string
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
	p := &Proc{spec: spec, cancel: cancel, done: make(chan struct{}), kick: make(chan struct{}, 1), state: StateStopped,
		change: make(chan struct{}), linkSince: time.Now()}
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

// Stop stops the plugin and waits for its program to end. Its idle HTTP connections close too: a plugin served from
// elsewhere outlives the Proc, and its keep-alive connections would stay open for good.
func (p *Proc) Stop() {
	p.cancel()
	<-p.done
	if p.client != nil {
		p.client.CloseIdleConnections()
	}
}

type invocationRunKey struct{}

// InvocationRun identifies the trusted transport's exact process attempt, also
// during initialization. It is not a guest field or an authorization credential.
func InvocationRun(ctx context.Context) string {
	run, _ := ctx.Value(invocationRunKey{}).(string)
	return run
}

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

// Link is how an attached plugin is connected.
func (p *Proc) Link() Link {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Link{Online: p.ready, Since: p.linkSince, Why: p.linkWhy, Said: p.said}
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
		p.mu.Unlock()
		switch {
		case ready:
			return nil
		case state == StateError:
			return i18n.ErrEnglish("extension-status.host_failure", i18n.Args{"name": p.spec.Name, "err": why})
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
		p.strike(method + " timed out")
		return newCallTimeout(p.spec.Name, method, p.spec.callTimeout())
	}
	if err == nil {
		p.mu.Lock()
		p.strikes = 0
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
func (p *Proc) strike(why string) {
	p.mu.Lock()
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
		err := p.runOnce(ctx)
		p.mu.Lock()
		wasUp := p.ready
		p.sess, p.ready, p.kill, p.strikes = nil, false, nil, 0
		p.mu.Unlock()
		if ctx.Err() != nil {
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
		again := wait
		if p.spec.attached() {
			if errors.As(err, new(refused)) {
				wait = min(wait*2, RestartLast)
			} else {
				again, wait = RemoteRetry, RestartFirst
			}
			p.offline(err, wasUp, again)
		} else {
			p.set(StateError, err.Error())
			p.run(RunExit, err.Error())
			p.note("%v (again in %s)", err, wait)
			wait = min(wait*2, RestartLast)
		}
		select {
		case <-ctx.Done():
			p.set(StateStopped, "")
			return
		case <-time.After(again):
		}
		if p.spec.attached() {
			// Nothing needs it any more: stopped, not offline, so when it is needed again it gets its grace anew
			// (runOnce) and a call waits for it instead of failing on the error it was left in.
			p.mu.Lock()
			if !p.wantedLocked() {
				p.setLocked(StateStopped, "")
			}
			p.mu.Unlock()
		}
	}
}

// offline is an attached plugin's failed try or lost connection (wasUp). It stays starting for OfflineGrace after it
// went offline, then it is an error; one that answered and was refused is an error at once. A try that changes nothing
// changes nothing: no state flip, no log line, no exit told to the Observer.
func (p *Proc) offline(err error, wasUp bool, again time.Duration) {
	why, kind, refusal := err.Error(), failureKind(err), errors.As(err, new(refused))
	if refusal { // its details are the answer (another version is other news); the backoff bounds the lines
		kind = why
	}
	p.mu.Lock()
	now := time.Now()
	if wasUp {
		p.linkSince = now
	}
	if !refusal {
		p.said = ""
	}
	news := wasUp || kind != p.linkKind
	if news {
		p.linkKind, p.linkWhy = kind, why
	}
	state, msg := StateStarting, ""
	if refusal || now.Sub(p.linkSince) >= OfflineGrace {
		state, msg = StateError, p.linkWhy
	}
	if p.state != state || p.err != msg {
		p.setLocked(state, msg)
	}
	p.mu.Unlock()
	if news {
		p.run(RunExit, why)
		p.note("%v (again in %s)", err, again)
	}
}

// failureKind is what failed without its details (a message's key): tries that fail alike, a closed connection and
// then a broken pipe, are the same news.
func failureKind(err error) string {
	var m *i18n.Message
	if errors.As(err, &m) {
		return m.Key
	}
	return err.Error()
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
	// exited is closed once the program has ended, with why in waitErr (never, for a plugin elsewhere). Closed, not
	// sent on: the run and then end both wait for it, and a value taken by the first left end waiting forever (#2277).
	exited  chan struct{}
	waitErr error
}

// runOnce runs the plugin once: nil when it was stopped because nothing needed it, the reason otherwise.
func (p *Proc) runOnce(ctx context.Context) error {
	// An attached plugin keeps the state its last try left (offline): each try is not a start of its own.
	switch st, _ := p.Status(); {
	case !p.spec.attached():
		p.set(StateStarting, "")
	case st == StateStopped: // needed again: its grace counts from now
		p.mu.Lock()
		p.linkSince, p.linkWhy, p.linkKind = time.Now(), "", ""
		p.setLocked(StateStarting, "")
		p.mu.Unlock()
	}
	if p.spec.Check != nil {
		if err := p.spec.Check(ctx); err != nil {
			return err
		}
	}
	ch, out, err := p.spawn(ctx)
	if err != nil {
		return err
	}
	if !p.spec.attached() { // an attached one's run starts when it comes online (up)
		p.run(RunStart, "")
	}
	runCtx, stopRun := context.WithCancel(context.WithValue(ctx, invocationRunKey{}, rand.Text()))
	defer stopRun()
	var s *session
	if p.spec.RPC {
		peer := rpc.New("h", writerOf(ch))
		peer.Limits = p.spec.RPCBounds
		if p.spec.Handle != nil {
			peer.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
				call, cancel := context.WithCancel(context.WithValue(ctx, invocationRunKey{}, InvocationRun(runCtx)))
				stop := context.AfterFunc(runCtx, cancel)
				defer stop()
				defer cancel()
				if err := runCtx.Err(); err != nil {
					return nil, err
				}
				return p.spec.Handle(call, method, params)
			}
		}
		peer.OnEvent = p.spec.OnEvent
		go func() { _ = peer.Serve(context.Background(), out) }()
		s = &session{peer: peer}
		if err := p.initialize(runCtx, s); err != nil {
			stopRun()
			p.end(ch, peer)
			return err
		}
	}
	killed := make(chan struct{})
	var once sync.Once
	p.mu.Lock()
	p.generation++
	p.sess = s
	p.kill = func() { once.Do(func() { close(killed) }) }
	p.mu.Unlock()
	defer func() {
		// Fence host callbacks before waiting for child/peer cleanup. Neither
		// process loss nor slow cleanup may extend a storage invocation's life.
		stopRun()
		var peer *rpc.Peer
		if s != nil {
			peer = s.peer
		}
		p.end(ch, peer)
	}()
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
	var peerDone <-chan struct{}
	if s != nil {
		peerDone = s.peer.Done()
	}
	misses, healthMisses := 0, 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ch.exited:
			if ch.waitErr == nil {
				return i18n.ErrEnglish("extension-status.exited")
			}
			return ch.waitErr
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
	p.ready = true
	p.setLocked(StateRunning, "")
	attached, away, was := p.spec.attached(), time.Since(p.linkSince), p.linkWhy
	if attached {
		p.linkSince, p.linkWhy, p.linkKind = time.Now(), "", ""
	}
	p.mu.Unlock()
	if attached {
		p.run(RunStart, "")
		if was != "" {
			p.note("connected after %s offline", away.Round(time.Second))
		}
	}
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
	if p.spec.attached() && !p.spec.RPC { // its /health is its link
		switch {
		case ok && state != StateRunning:
			p.up()
			t.Reset(HealthEvery)
		case !ok:
			p.mu.Lock()
			p.ready = false
			p.mu.Unlock()
			p.offline(i18n.ErrEnglish("extension-status.socket_not_answering", i18n.Args{"path": p.spec.Socket}), state == StateRunning, RemoteRetry)
			t.Reset(RemoteRetry)
		}
		return missed
	}
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
			return refused{i18n.Err("extension-status.wrong_protocol", i18n.Args{"err": re.Message})}
		}
		return i18n.Err("extension-status.not_ready", i18n.Args{"err": err.Error()})
	}
	said := ""
	if res.Protocol == extension.Protocol && (res.ID == params.Extension.ID || params.Extension.ID == "") {
		said = res.Version // another program's version says nothing about this one
	}
	p.mu.Lock()
	p.said = said
	p.mu.Unlock()
	if res.Protocol != extension.Protocol {
		return refused{i18n.Err("extension-status.protocol_version", i18n.Args{"current": res.Protocol, "required": extension.Protocol})}
	}
	if res.ID != params.Extension.ID && params.Extension.ID != "" {
		return refused{i18n.ErrEnglish("extension-status.wrong_id", i18n.Args{"current": res.ID, "required": params.Extension.ID})}
	}
	if res.Version != params.Extension.Version && params.Extension.Version != "" {
		return refused{i18n.ErrEnglish("extension-status.wrong_version", i18n.Args{"current": res.Version, "required": params.Extension.Version})}
	}
	if p.spec.Accept != nil {
		if err := p.spec.Accept(res); err != nil {
			return refused{err}
		}
	}
	s.result = res
	if res.UIPageCancellation == 1 {
		// Negotiate before readiness; older installed SDKs never receive rpc.cancel.
		s.peer.Cancels = map[string]bool{"ui.page.mount": true, "ui.page.event": true, "ui.page.resume": true}
	}
	return nil
}

// spawn starts the program (or connects to the plugin elsewhere): what to write to and read from.
func (p *Proc) spawn(ctx context.Context) (*child, io.Reader, error) {
	if p.spec.Command == "" {
		ch := &child{exited: make(chan struct{})}
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
	ch := &child{cmd: cmd, exited: make(chan struct{})}
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
		return nil, nil, i18n.Err("extension-status.program_start", i18n.Args{"name": p.spec.Name, "err": err.Error()})
	}
	started = true
	go p.logLines(logs)
	go func() {
		ch.waitErr = cmd.Wait()
		if dispose != nil {
			dispose()
		}
		close(ch.exited)
	}()
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

// end stops a run: closes stdin (a program exits when the hub goes away), then SIGTERM, then SIGKILL, to its whole
// process group. A program that does not speak the protocol gets SIGTERM right away.
func (p *Proc) end(ch *child, peer *rpc.Peer) {
	if peer != nil {
		peer.Close(rpc.ErrClosed)
	}
	if ch.conn != nil {
		_ = ch.conn.Close()
	}
	if ch.cmd == nil || ch.cmd.Process == nil {
		return // a plugin elsewhere owns its socket
	}
	if p.spec.HTTP && p.spec.Socket != "" {
		defer p.removeSocket() // once the program is gone: a killed one leaves its socket file behind
	}
	pid := ch.cmd.Process.Pid
	if ch.stdin != nil {
		_ = ch.stdin.Close()
		select {
		case <-ch.exited:
			_ = osproc.StopGroup(pid, false) // its children, if it left any
			return
		case <-time.After(StopGrace):
		}
	}
	_ = osproc.StopGroup(pid, false)
	select {
	case <-ch.exited:
	case <-time.After(StopGrace):
		_ = osproc.StopGroup(pid, true)
		<-ch.exited
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
