package devices

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/update"
)

var errOffline = apierr.ErrOffline

// MachineConfig is what the manager reads of a hub.json machines[] entry (the hub has the rest: launchers, Codex).
type MachineConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Trust limits access to personal data: empty/personal preserves existing access; work isolates this machine.
	Trust string `json:"trust,omitempty"`
	// Kind: local | service (the manager dials the computer's connector, service.go) | link (the computer's
	// Persona Link dials the hub, which relays it here, link.go) | ssh | tailcat (tailcat only, no ssh).
	Kind    string `json:"kind"`
	SSHHost string `json:"ssh_host,omitempty"`
	// Address is a service computer's connector, host:port; Proxy an optional socks5://host:port to dial it through.
	Address     string `json:"address,omitempty"`
	Proxy       string `json:"proxy,omitempty"`
	TailcatAddr string `json:"tailcat_addr,omitempty"`
	Icon        string `json:"icon,omitempty"`
	// RemoteBin is the personad path on the remote machine (default ~/.persona/bin/personad).
	RemoteBin        string   `json:"remote_bin,omitempty"`
	ClaudeConfigDirs []string `json:"claude_config_dirs,omitempty"`
	Botmux           *bool    `json:"botmux,omitempty"`
	HerdrAutostart   *bool    `json:"herdr_autostart,omitempty"`
	HerdrAutoinstall bool     `json:"herdr_autoinstall,omitempty"`
	ClaudeStartDir   string   `json:"claude_start_dir,omitempty"`
	// ClaudeGlobalHooks: the node puts report-only hooks into its default claude config directory (hub/config.go).
	ClaudeGlobalHooks bool `json:"claude_global_hooks,omitempty"`
	// UsageWindows false: the node does not read the agents' subscription rate-limit windows (hub/config.go, #2432).
	UsageWindows *bool `json:"usage_windows,omitempty"`
}

// Machine is the manager's connection to one node: a local subprocess, `ssh host personad node --stdio`, a tailcat
// connection to its resident node, a stream on the Persona Link it keeps to the hub (kind link, link.go), a
// connector's stream (kind service), or a stream through a member hub (a derived machine).
type Machine struct {
	cfg MachineConfig
	// named is what the computer is called now: cfg.Name until the hub renames it, which changes nothing else of the
	// running machine (startMachines); read through name().
	named atomic.Pointer[string]
	raw   json.RawMessage // the entry as the hub gave it (for a derived machine, the member's)
	d     *Devices
	ctx   context.Context
	stop  context.CancelFunc

	spooled spoolSeen // spool_ids of what a resident node replayed (spooldedupe.go)

	mu   sync.Mutex
	peer *rpc.Peer
	info proto.NodeInfo
	// online: shown online (its node answered on the current connection, or warm); connectedAt: when the node's
	// current connection began.
	online      bool
	connectedAt time.Time
	// warmUntil, warmed: shown as before a restart until then, while its node reconnects (warm.go); warmed is closed
	// when that ends.
	warmUntil time.Time
	warmed    chan struct{}
	err       string
	// dialErrAt: when err last said why connecting failed (markOffline, noPath), not why a connection ended.
	dialErrAt       time.Time
	lastSeen        time.Time
	sessions        []proto.Session
	sessionRevision uint64          // local ordering of session snapshots, independent of node transcript revisions
	watched         map[string]bool // sessions the hub watches (session.watch): watched again on a new connection
	followed        map[string]bool // sessions the manager itself follows (Follow: a channel's turn): watched as well
	lastSeq         map[string]int  // session id -> highest item seq seen
	// unsettled: sessions whose last Follow the node did not take (the next Follow asks it again; a new connection
	// settles them).
	unsettled map[string]bool
	kick      chan struct{}
	// deploying: the manager is sending this machine the hub's node build (deploy.go); deployErr: why the last try
	// failed.
	deploying   bool
	deployErr   string
	deployPhase string
	// via is how the connection runs (local | ssh | tailcat | link | service | hub), path how tailcat's packets go.
	via, path                  string
	directFailed, switchedBack time.Time
	flaps                      int
	timings                    tailcatTimings
	deployRetry                time.Duration // how long a node update waits before asking a busy node again (tests: shorter)
	reconnectGrace             time.Duration // shown as it was this long after its node disconnected (warm.go; tests: shorter)
	engineMu                   sync.Mutex
	link                       *mgrLink      // kind link: the link Persona Link keeps open, nil while there is none
	linkDown                   error         // why the last link went away
	linkUp                     chan struct{} // a link arrived
	// herdrAsked: when the node last said whether herdr's server answers (herdr.go, its row).
	herdrAsked time.Time
	// herdrInstall: what the node last said of installing herdr (herdr.go).
	herdrInstall struct {
		installing bool
		err        string
	}
	// first is closed once the first try to connect ended (the node answered, or it did not): until then, right after
	// the manager started, a call waits for it instead of failing at once (firstWait).
	first     chan struct{}
	firstOnce sync.Once
	// pending counts the node's questions waiting on the phone (interaction.create): no switching paths meanwhile.
	pending int
	// notes are the hub's one-way events waiting for the node (notes.go; their own lock, not m.mu).
	notes nodeNotes
	// opened is what the hub has open on the node through the bus connection of the moment (hubopen.go).
	opened hubOpened
	// claudeDirsMu serializes the read-modify-writes of the remembered and blocked Claude config dirs (claudedirs.go).
	claudeDirsMu sync.Mutex
	// nodeCmd replaces the node command (tests).
	nodeCmd func(context.Context) *exec.Cmd
	// localNote is why the last start of a local node passed a program beside it over (localNode), said once until it
	// changes.
	localNote string
	// dialTailcat replaces dialing the node over tailcat, probeDirect checking that ssh works (tests).
	dialTailcat func(context.Context) (net.Conn, error)
	probeDirect func(context.Context) error
	// dialNode replaces every way to the node with a connection to an in-process one (tests).
	dialNode func(context.Context) (net.Conn, error)

	derived   bool
	active    *nodePath
	transport string
	uid       string
	next      *nodeConn
	parked    string
	svc       serviceState
	// attWake wakes a service machine's sender of the "Needs you" summary (attentionLoop); one wake waits at most.
	attWake chan struct{}
}

func newMachine(d *Devices, cfg MachineConfig) *Machine {
	m := &Machine{cfg: cfg, d: d, watched: map[string]bool{}, lastSeq: map[string]int{}, kick: make(chan struct{}, 1),
		timings: defaultTailcatTimings, deployRetry: 2 * time.Second, reconnectGrace: reconnectGrace,
		linkUp: make(chan struct{}, 1), first: make(chan struct{}), attWake: make(chan struct{}, 1)}
	m.setName(cfg.Name)
	if dial := d.opts.DialNode; dial != nil {
		m.dialNode = dial(cfg.ID)
	}
	switch cfg.Kind {
	case kindLink:
		m.err = "waiting for Persona Link"
	case kindService:
		m.err = "waiting for the connector"
	}
	return m
}

// sameConfig: a and b are the same entry but perhaps for the name, which a running machine takes as it is (rename).
func sameConfig(a, b MachineConfig) bool {
	a.Name, b.Name = "", ""
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// name is what the computer is called now: as its entry last named it (renamed or not), else its id.
func (m *Machine) name() string {
	if n := m.named.Load(); n != nil {
		return *n
	}
	return cmp.Or(m.cfg.Name, m.cfg.ID)
}

// setName renames the computer ("": its id); true when that changed its name.
func (m *Machine) setName(name string) bool {
	name = cmp.Or(name, m.cfg.ID)
	if old := m.named.Load(); old != nil && *old == name {
		return false
	}
	m.named.Store(&name)
	return true
}

// snapshot is the machine as the apps get it, but for what the hub adds (computer, browser).
func (m *Machine) snapshot() proto.Machine {
	d := m.d
	engine := d.engineOf(m.cfg.ID) // before m.mu: it may read settings
	choice := d.engineChoiceOf(m.cfg.ID)
	m.mu.Lock()
	info := m.info
	m.mu.Unlock()
	// The build its platform resolves to (packages.go: the hub's own, or the newest release this manager drives), and
	// why none can be sent; before m.mu, as they read the hub's handover.
	version, noPackage := d.hubVersion(), error(nil)
	if m.cfg.Kind != "local" { // the hub's own machine runs the hub's binary: nothing to look up
		version, noPackage = d.nodeTarget(info), d.packageBlocked(info)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	engines := []proto.Engine{} // what the node found: none while offline
	warm := m.warmLocked(time.Now())
	var suggested *proto.EngineChoice
	if m.online || warm {
		engines = append(engines, m.info.Engines...)
		if len(engines) > 0 {
			s := proto.SuggestEngineHosts(engines)
			suggested = &s
		}
	}
	// A node without sets runs the single engine; say so when that is not the set chosen (docs/protocol/protocol.md "Engines").
	setsUnsupported := (m.online || warm) && m.info.EngineHosts == nil && !proto.LegacyExpresses(choice)
	icon := m.cfg.Icon
	if icon == "" {
		icon = "laptop"
	}
	blocked := m.canDeployLocked()
	if blocked == nil {
		blocked = noPackage
	}
	canDeploy, deployReason := blocked == nil, ""
	if blocked != nil {
		deployReason = blocked.Error()
	}
	// A failed update is moot once the node runs the hub's own build (it was updated by hand, or the try that failed
	// was made while the machine was away): the card would keep "Last deploy failed" until another deploy (#2868).
	deployErr := m.deployErr
	incompatible := ""
	if m.online {
		incompatible = compatOf(m.info).Incompatible
	}
	if m.online && version != "" && m.info.Version == version {
		deployErr = ""
	}
	var link *proto.MachineLink
	if m.link != nil {
		link = m.link.info()
	}
	return proto.Machine{
		ID: m.cfg.ID, Name: m.name(), Kind: m.cfg.Kind, Icon: icon, SSHHost: m.cfg.SSHHost, Online: m.online || warm, Error: m.err,
		Hostname: m.info.Hostname, OS: m.info.OS, Arch: m.info.Arch, Home: m.info.Home,
		HerdrVersion: m.info.HerdrVersion, ClaudeVersion: m.info.ClaudeVersion, LastSeen: m.lastSeen,
		NodeVersion: m.info.Version, NodeMismatch: m.online && nodeMismatch(version, m.info),
		NodeProtocol: m.info.Protocol, NodeIncompatible: incompatible,
		Deploying: m.deploying, DeployError: deployErr,
		CanDeploy: &canDeploy, DeployReason: deployReason, DeployPhase: m.deployPhase,
		Transport: m.via, TailcatPath: m.path, Tailcat: m.cfg.TailcatAddr != "",
		Engine: engine, Engines: engines, EngineHosts: choice.Hosts, EngineDefault: choice.Default,
		EngineSuggested: suggested, EngineSetsUnsupported: setsUnsupported, Link: link, System: d.metrics.system(m.cfg.ID),
	}
}

// state is the machine as devices.state reports it.
func (m *Machine) state() devicewire.State {
	s := m.d.decorate(m, m.snapshot())
	paths := m.d.pathInfos(m)
	connected := m.reachable()
	m.mu.Lock()
	defer m.mu.Unlock()
	return devicewire.State{Machine: s, Config: m.raw, Info: m.info, Connected: connected, ConnectedAt: m.connectedAt,
		WarmUntil: m.warmUntil, Derived: m.derived, Paths: paths}
}

func (m *Machine) reconnect() {
	m.mu.Lock()
	p := m.peer
	m.mu.Unlock()
	if p != nil {
		p.Close(errors.New("reconnect requested"))
	}
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

func (m *Machine) command(ctx context.Context) *exec.Cmd {
	if m.nodeCmd != nil {
		return m.nodeCmd(ctx)
	}
	if m.cfg.Kind == "local" {
		self, _ := os.Executable()
		return exec.CommandContext(ctx, m.localNode(ctx, self, nil), "node", "--stdio")
	}
	bin := m.cfg.RemoteBin
	if bin == "" {
		bin = "~/.persona/bin/personad"
	}
	host := m.cfg.SSHHost
	if host == "" {
		host = m.cfg.ID
	}
	// Compression: the RPC is JSON (transcript pages, screens) and shrinks about 4x. Over a slow link (a machine
	// abroad at ~15 KB/s, 2026-10-31) an 87 KB page took 5.6 s and a 500-item page passed the hub's 30 s deadline;
	// on a LAN it costs next to nothing.
	return exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=15", "-o", "Compression=yes",
		"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3", host, bin+" node --stdio")
}

// localNode is the program the node of the hub's computer runs as (docs/server/server.md "Programs"): persona-node
// beside self when it is self's build, else personad beside self when it is self's build (so the node and what it
// writes into hooks stay off the manager's program), else self. update.SplitEnv 0 keeps self. version is
// update.BuildOf when nil.
func (m *Machine) localNode(ctx context.Context, self string, version func(context.Context, string) (string, error)) string {
	if update.SplitOff() {
		return self
	}
	p, note := update.SplitBeside(ctx, self, nodeproto.ProgramPersonaNode, version)
	name := strings.TrimSuffix(filepath.Base(self), filepath.Ext(self))
	if p == "" && name != nodeproto.ProgramPersonad && name != compat.DaemonBinary {
		var also string
		p, also = update.SplitBeside(ctx, self, nodeproto.ProgramPersonad, version)
		if also != "" {
			note = strings.TrimPrefix(note+"; "+also, "; ")
		}
	}
	if p == "" {
		p = self
	}
	if ctx.Err() != nil {
		// A connection given up while asking (a reconnect) starts no node, so it says nothing about one.
		return p
	}
	m.mu.Lock()
	said := m.localNote
	m.localNote = note
	m.mu.Unlock()
	if note != "" && note != said {
		m.d.logf("[%s] %s: the node runs as %s", m.cfg.ID, note, filepath.Base(p))
	}
	return p
}

// nodePipe is one way to a node: what it says, where to write, and why it failed (ssh's last stderr line). It is an
// ssh (or local) process's stdout and stdin, a tailcat connection, or a stream on a Persona Link (dialLink);
// establish speaks the node protocol over each the same way.
type nodePipe struct {
	r       io.Reader
	w       io.Writer
	lastErr func() string
	close   func() // ends it and waits until it is gone
	// activity is when bytes last moved either way (tailcat only: watchTailcat pings only an idle connection).
	activity *activityConn
}

// shutdownStepTimeout bounds draining a node's pipes once its process is gone.
const shutdownStepTimeout = 2 * time.Second

func (m *Machine) open(ctx context.Context, via string) (*nodePipe, error) {
	if m.dialNode != nil {
		c, err := m.dialNode(ctx)
		if err != nil {
			return nil, err
		}
		return &nodePipe{r: c, w: c, lastErr: func() string { return "" }, close: func() { _ = c.Close() }}, nil
	}
	if via == "link" {
		return m.dialLink(ctx)
	}
	if via == kindService {
		return m.dialService(ctx)
	}
	if via == "tailcat" {
		dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		c, err := m.tailcatDial(dctx)
		if err != nil {
			return nil, err
		}
		ac := &activityConn{Conn: c, now: m.timings.now}
		ac.touch()
		return &nodePipe{r: ac, w: ac, lastErr: func() string { return "" }, close: func() { _ = c.Close() }, activity: ac}, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := m.command(ctx)
	// Bound pipe draining if a node descendant inherits stdout or stderr.
	cmd.WaitDelay = shutdownStepTimeout
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	var lastErr strings.Builder
	var errMu sync.Mutex
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			m.d.logf("[%s] %s", m.cfg.ID, line)
			errMu.Lock()
			lastErr.Reset()
			lastErr.WriteString(line)
			errMu.Unlock()
		}
	}()
	return &nodePipe{r: stdout, w: stdin,
		// Asked once the link has failed: stdout can close before ssh's last stderr line is read, so give the reader a
		// moment to finish (TestBothWaysFailing lost "ssh: connect to host …" that way on a loaded machine).
		lastErr: func() string {
			select {
			case <-stderrDone:
			case <-time.After(2 * time.Second):
			}
			errMu.Lock()
			defer errMu.Unlock()
			return lastErr.String()
		},
		// Wait closes the stderr pipe: let the reader take the line the process wrote as it ended first, or a lastErr
		// asked after close (establish's failed node.info) has lost it (#5254). A descendant holding stderr open
		// bounds the wait.
		close: func() {
			cancel()
			select {
			case <-stderrDone:
			case <-time.After(shutdownStepTimeout):
			}
			_ = cmd.Wait()
		},
	}, nil
}

func (m *Machine) call(ctx context.Context, method string, params any, out any) error {
	m.mu.Lock()
	p := m.peer
	info := m.info
	m.mu.Unlock()
	if info.Version != "" && info.EngineHosts == nil && method == "session.start" && proto.StartHost(params) != "" {
		// An older node ignores host and would start in its engine instead (#2576): refused, nothing started.
		return &rpc.Error{Code: proto.CodeNodeOutdated, Message: i18n.T(i18n.From(ctx), "hub-machine.node_no_engine_hosts")}
	}
	if c := compatOf(info); c.Incompatible != "" && !usableWhileIncompatible(method) {
		key := "hub-machine.node_protocol_old"
		if c.Incompatible == incompatibleNodeNew {
			key = "hub-machine.node_protocol_new"
		}
		return &rpc.Error{Code: proto.CodeNodeOutdated, Message: i18n.T(i18n.From(ctx), key)}
	}
	if !nodeHas(info, nodeproto.ModelConfiguration) && (method == "assistant.send" || method == "assistant.warm" || method == "session.start" || method == "session.send") {
		raw, _ := json.Marshal(params)
		var fields struct {
			Model       string `json:"model"`
			Fingerprint string `json:"model_fingerprint"`
		}
		_ = json.Unmarshal(raw, &fields)
		if fields.Model != "" || fields.Fingerprint != "" {
			return &rpc.Error{Code: proto.CodeNodeOutdated, Message: i18n.T(i18n.From(ctx), "hub-machine.node_no_model_config")}
		}
	}
	if method == "session.start" && !nodeHas(info, nodeproto.Ultracode) {
		raw, _ := json.Marshal(params)
		var fields struct {
			Ultracode *bool `json:"ultracode"`
		}
		_ = json.Unmarshal(raw, &fields)
		if fields.Ultracode != nil && *fields.Ultracode { // off is a new process's default: nothing to refuse
			return &rpc.Error{Code: proto.CodeNodeOutdated, Message: i18n.T(i18n.From(ctx), "hub-ultracode.node_outdated")}
		}
	}
	if method == proto.MethodSecretRun && !nodeHas(info, nodeproto.SecretRun) {
		// An older node would answer "unknown method"; this says what to do about it before the user types anything.
		return &rpc.Error{Code: proto.CodeNodeOutdated, Message: i18n.T(i18n.From(ctx), "hub-machine.node_no_secret_run")}
	}
	if p == nil && (m.waitWarm(ctx) || m.waitFirst(ctx)) { // the manager just started: its node is on its way (warm.go)
		m.mu.Lock()
		p = m.peer
		m.mu.Unlock()
	}
	if p == nil {
		return fmt.Errorf("%s: %w", m.name(), errOffline)
	}
	select {
	case <-p.Done(): // closed, a new connection on its way: nothing was sent
		return fmt.Errorf("%s: %w", m.name(), errOffline)
	default:
	}
	return p.Call(ctx, method, params, out)
}

// firstWait bounds how long a call waits for a machine's first try to connect (the manager just started: an answer
// the hub held for an agent while it was away, a start from the apps).
var firstWait = 20 * time.Second

// firstTried: the first try to connect ended, one way or the other.
func (m *Machine) firstTried() { m.firstOnce.Do(func() { close(m.first) }) }

// waitFirst waits, for a call made before the machine's first try to connect ended, until it did (true: there is a
// connection now), at most firstWait.
func (m *Machine) waitFirst(ctx context.Context) bool {
	if firstWait <= 0 {
		return false
	}
	select {
	case <-m.first:
		return false
	default:
	}
	t := time.NewTimer(firstWait)
	defer t.Stop()
	select {
	case <-m.first:
	case <-t.C:
	case <-ctx.Done():
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peer != nil
}

// reachable: a call would go out now (call answers errOffline otherwise).
func (m *Machine) reachable() bool {
	m.mu.Lock()
	p := m.peer
	m.mu.Unlock()
	if p == nil {
		return false
	}
	select {
	case <-p.Done():
		return false
	default:
		return true
	}
}

// noteWatch keeps which sessions the hub watches (its apps' subscriptions): a new connection watches them again.
func (m *Machine) noteWatch(method string, params json.RawMessage) {
	if method != "session.watch" && method != "session.unwatch" {
		return
	}
	var p struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(params, &p) != nil || p.SessionID == "" {
		return
	}
	m.mu.Lock()
	if method == "session.watch" {
		m.watched[p.SessionID] = true
	} else {
		delete(m.watched, p.SessionID)
	}
	m.mu.Unlock()
}

// setSessions keeps the node's session list (the hub gets it as the node sent it, devices.event).
func (m *Machine) setSessions(list []proto.Session) {
	for i := range list {
		list[i].MachineID = m.cfg.ID
	}
	m.mu.Lock()
	m.sessions = list
	m.sessionRevision++
	m.lastSeen = time.Now().UTC()
	m.mu.Unlock()
}

func (m *Machine) sessionList() []proto.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.sessions)
}

// sessionsWhere copies out the sessions keep accepts (Devices.SessionsWhere).
func (m *Machine) sessionsWhere(keep func(*proto.Session) bool) []proto.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []proto.Session
	for i := range m.sessions { // by index: a range value copies every (large) session it passes
		if keep(&m.sessions[i]) {
			out = append(out, m.sessions[i])
		}
	}
	return out
}

func (m *Machine) session(id string) (proto.Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.sessions { // by index: a range value copies every (large) session it passes
		if m.sessions[i].SessionID == id {
			return m.sessions[i], true
		}
	}
	return proto.Session{}, false
}

// onEvent takes what is the manager's of a node's event, and passes every event on to the hub as it came.
func (m *Machine) onEvent(event string, params json.RawMessage) {
	if event == "hook.event" && m.spooled.repeat(params) {
		return // replayed twice by a resident node (spooldedupe.go)
	}
	m.noteClosed(event, params)
	switch event {
	case "sessions.changed":
		var p struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if json.Unmarshal(params, &p) == nil {
			m.setSessions(p.Sessions)
		}
	case "sessions.revs":
		var p struct {
			Revs map[string]int64 `json:"revs"`
		}
		if json.Unmarshal(params, &p) == nil {
			m.mu.Lock()
			for i := range m.sessions {
				if r, ok := p.Revs[m.sessions[i].SessionID]; ok {
					m.sessions[i].Rev = r
				}
			}
			m.mu.Unlock()
		}
	case "session.items":
		var p struct {
			SessionID string       `json:"session_id"`
			Items     []proto.Item `json:"items"`
			ToSeq     *int         `json:"to_seq"`
		}
		if json.Unmarshal(params, &p) == nil {
			last := 0
			if len(p.Items) > 0 {
				last = p.Items[len(p.Items)-1].Seq
			}
			if p.ToSeq != nil {
				last = max(last, *p.ToSeq)
			}
			m.mu.Lock()
			if last > m.lastSeq[p.SessionID] {
				m.lastSeq[p.SessionID] = last
			}
			m.mu.Unlock()
		}
	case "state.put":
		var p struct {
			SessionID string         `json:"session_id"`
			State     db.NodeSession `json:"state"`
		}
		if s, ok := m.d.opts.Store.(nodeStateStore); ok && json.Unmarshal(params, &p) == nil && p.SessionID != "" {
			if err := s.PutNodeSession(context.Background(), m.cfg.ID, p.SessionID, p.State); err != nil {
				m.d.logf("[%s] save session state: %v", m.cfg.ID, err)
			}
		}
		return // the manager's alone
	case "claude.dirs":
		m.rememberClaudeDirs(params)
	case "metrics": // the monitoring is the manager's (metrics.go)
		if !m.stopped() { // a late one from a machine let go would bring it back (forgetMachine)
			m.d.onMetrics(m.cfg.ID, params)
			m.forgetIfStopped()
		}
		return
	case "usage":
		if !m.stopped() {
			m.d.onUsage(m.cfg.ID, params, time.Now())
			m.forgetIfStopped()
		}
		return
	}
	if f := m.d.opts.OnEvent; f != nil && f(m.cfg.ID, event, params) {
		return
	}
	m.d.forward(m.cfg.ID, event, params)
}

// forgetIfStopped closes the window between onEvent's stopped check and its write: a machine is stopped before it
// is forgotten, so a write that raced the stop is either cleared by that forgetMachine or, seen stopped here, by
// this one.
func (m *Machine) forgetIfStopped() {
	if m.stopped() {
		m.d.forgetMachine(m.cfg.ID)
	}
}

// stopped: the machine was let go (no longer in hub.json, its entry replaced, or no longer reachable).
func (m *Machine) stopped() bool {
	return m.ctx != nil && m.ctx.Err() != nil
}

// onRequest answers what is the manager's of a node's requests and passes the rest on to the hub.
func (m *Machine) onRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "state.load":
		out := map[string]db.NodeSession{}
		if s, ok := m.d.opts.Store.(nodeStateStore); ok {
			var err error
			if out, err = s.NodeSessions(ctx, m.cfg.ID); err != nil {
				return nil, err
			}
		}
		return map[string]any{"sessions": out}, nil
	}
	if method == "notify" {
		if id := spoolID(params); id != "" { // replayed by a resident node (spooldedupe.go)
			dup, busy := m.spooled.begin(id)
			switch {
			case dup:
				return map[string]any{"sent": true, "duplicate": true}, nil
			case busy: // the last try is still with the hub: this one is not sent, nor taken for sent
				return nil, &rpc.Error{Code: nodeproto.ErrSpoolBusy, Message: "this notification is still being sent"}
			}
			out, err := m.passOn(ctx, method, params)
			// Remembered once the hub took it, and also once it went out to the hub and the link dropped before the
			// answer (rpc.ErrDropped): the hub may have pushed it, and it does not tell a repeat itself, so a try
			// again would push it twice (at most once). Anything else failed is tried again.
			dropped := errors.Is(err, rpc.ErrDropped)
			m.spooled.end(id, err == nil || dropped)
			if err != nil && !dropped && ctx.Err() == nil && (!m.d.busUp() || errors.Is(err, rpc.ErrClosed)) {
				// It never reached the hub: this manager has no connection to it now. The node keeps it, counting no
				// try.
				return nil, &rpc.Error{Code: nodeproto.ErrHubAway, Message: "the manager cannot reach the hub now"}
			}
			return out, err
		}
	}
	if method == "interaction.create" {
		m.mu.Lock()
		m.pending++
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			m.pending--
			m.mu.Unlock()
		}()
	}
	return m.passOn(ctx, method, params)
}

// passOn answers a node's request through the manager's own answers (Options.Answer), else the hub's.
func (m *Machine) passOn(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if a := m.d.opts.Answer; a != nil {
		if out, ok, err := a(ctx, m.cfg.ID, method, params); ok {
			return out, err
		}
	}
	if len(params) > maxForward {
		return nil, errTooLarge
	}
	var out json.RawMessage
	key := m.d.requestKey()
	err := m.d.opts.Hub.Call(ctx, devicewire.MethodRequest, devicewire.NodeCall{MachineID: m.cfg.ID, Method: method, Params: params,
		Request: key}, &out)
	if err != nil {
		if ctx.Err() != nil { // the node's connection ended (ctx is its): the hub's part ends too
			_ = m.d.opts.Hub.Notify(devicewire.EventRequestEnd, devicewire.RequestEnd{MachineID: m.cfg.ID, Request: key})
		}
		return nil, err
	}
	return out, nil
}

// machinePending: a question, plan or permission from this machine is waiting on the phone.
func (m *Machine) machinePending() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pending > 0
}

func (m *Machine) isOnline() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.online
}
