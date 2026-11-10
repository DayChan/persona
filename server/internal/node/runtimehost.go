package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/acceptloop"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/runtimepkg"
)

// RuntimeHost is the runtime socket (runtime.sock, runtimes.go) and the runtimes connected to it. A resident node
// (`personad node --attach`, attach.go) makes one when it starts and shares it with the Node it makes for each hub
// connection, so runtimes connect before the hub does and stay connected while it reconnects: their sessions are in
// the first list the next connection sends, and nothing on the runtime's side notices the gap. A node serving its one
// connection itself (`--stdio` with no resident node) has one of its own, for that connection (listenRuntimeSocket).
//
// Between hub connections runtimes are still answered: hello (with hub: false), their lists are kept, what they push
// for the phone is dropped (the hub re-watches from where the phone is when it is back), and an ask answers node_gone
// so the runtime asks again, with the same id, once a hub is connected. Runtimes are told when the hub comes and goes
// (runtime.hub), so they do not wait on a phone that cannot answer.
type RuntimeHost struct {
	reg     *runtimeRegistry
	path    string
	version string
	logger  *log.Logger

	mu   sync.Mutex
	node *Node // the Node serving a hub connection now; nil between connections

	// The daemons it dials (daemons.go), by id: their ids are reserved against runtimes on the socket.
	dmu        sync.Mutex
	daemons    map[string]*daemonRun
	daemonsSum [32]byte      // daemons.json as last applied
	dtimes     daemonTimings // zero: defaultDaemonTimings (tests make them quick)

	// ctx is Listen's: what runs for the socket's sake (the Paseo bridge) ends with it.
	ctx context.Context
	// paseo runs the Paseo bridge while the machine's engine is Paseo (paseobridge.go).
	paseo paseoBridge
	// orca runs the bridge to Orca's structured sessions while the machine's engine is Orca (orcabridge.go).
	orca orcaBridgeState
}

// NewRuntimeHost is a runtime socket at path (RuntimeSocketPath when empty) for a resident node; Attach starts it.
func NewRuntimeHost(version, path string) *RuntimeHost {
	if path == "" {
		path = RuntimeSocketPath()
	}
	rh := &RuntimeHost{reg: newRuntimeRegistry(), path: path, version: version,
		logger: log.New(os.Stderr, "node: ", log.LstdFlags|log.Lmsgprefix)}
	rh.reg.logf = rh.logger.Printf
	return rh
}

// current is the Node serving the hub now, nil between connections.
func (rh *RuntimeHost) current() *Node {
	rh.mu.Lock()
	defer rh.mu.Unlock()
	return rh.node
}

// attach makes n the Node serving the hub: it lists and drives the runtimes' sessions from now on. What the last hub
// connection watched is forgotten (the new one watches afresh); runtimes hear that a hub is there.
func (rh *RuntimeHost) attach(n *Node) {
	rh.mu.Lock()
	rh.node = n
	rh.mu.Unlock()
	n.runtimes = rh.reg
	n.runtimeHost = rh
	rh.reg.resetWatches()
	rh.notifyHub(true)
}

// detach: n no longer serves the hub (its connection ended). A newer Node may have attached already: then nothing.
func (rh *RuntimeHost) detach(n *Node) {
	rh.mu.Lock()
	if rh.node != n {
		rh.mu.Unlock()
		return
	}
	rh.node = nil
	rh.mu.Unlock()
	rh.reg.resetWatches()
	rh.notifyHub(false)
}

// notifyHub tells every connected runtime whether a hub is connected.
func (rh *RuntimeHost) notifyHub(connected bool) {
	for _, h := range rh.reg.ready() {
		_ = h.peer.Notify("runtime.hub", map[string]any{"connected": connected})
	}
}

// Listen serves runtimes on the socket until ctx ends, re-creating the socket file if something removes it (an older
// node shutting down), as the hook socket does. It dials the daemons in daemons.json beside the socket too.
func (rh *RuntimeHost) Listen(ctx context.Context) error {
	if _, err := rh.listenOnce(ctx); err != nil {
		return err
	}
	rh.ctx = ctx
	rh.startDaemons(ctx)
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			if _, err := os.Stat(rh.path); os.IsNotExist(err) {
				rh.logger.Printf("runtime socket vanished, re-creating")
				if _, err := rh.listenOnce(ctx); err != nil {
					rh.logger.Printf("runtime socket: %v", err)
				}
			}
		}
	}()
	return nil
}

func (rh *RuntimeHost) listenOnce(ctx context.Context) (net.Listener, error) {
	_ = os.Remove(rh.path)
	ln, err := net.Listen("unix", rh.path)
	if err != nil {
		return nil, err
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false) // by then the path may belong to a newer node
	_ = os.Chmod(rh.path, 0o600)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go acceptloop.Run(ctx, ln, rh.logger.Printf, func(c net.Conn) { go rh.serve(ctx, c) })
	return ln, nil
}

// serve runs one runtime's connection until it ends; its sessions leave the list with it.
func (rh *RuntimeHost) serve(ctx context.Context, c net.Conn) {
	rh.serveConn(ctx, c, "")
}

// serveConn is serve for any stream: a socket connection, or a daemon's link (daemon: its id in daemons.json).
func (rh *RuntimeHost) serveConn(ctx context.Context, c io.ReadWriteCloser, daemon string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	defer c.Close()
	peer := rpc.New("r", c)
	pid := 0
	if nc, ok := c.(net.Conn); ok {
		pid, _ = peerPID(nc) // 0 for a pipe, or where the system does not tell
	}
	// This node or an ancestor of it is a relay, not the runtime's own process: the manager's egress proxy leads an
	// extension's sandbox here (extmgr/extension_sandbox.go), and the manager may run this node, so everything the node
	// runs would descend from it.
	if pid > 0 && slices.Contains(nodeLineage(), pid) {
		pid = 0
	}
	h := rh.reg.add(peer)
	h.mu.Lock()
	h.drop = cancel
	h.daemon = daemon
	h.pid = pid
	h.mu.Unlock()
	peer.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return rh.request(ctx, h, method, params)
	}
	peer.OnEvent = func(event string, params json.RawMessage) { rh.event(h, event, params) }
	err := peer.Serve(ctx, c)
	peer.Close(err)
	rh.reg.remove(h)
	if id := h.id(); id != "" {
		rh.logger.Printf("runtime %s #%d left: %v", id, h.serial, err)
	}
	if n := rh.current(); n != nil {
		n.notifySessions()
	}
}

// request answers a runtime's call.
func (rh *RuntimeHost) request(ctx context.Context, h *runtimeConn, method string, params json.RawMessage) (any, error) {
	switch method {
	case "runtime.hello":
		var hello runtimeHello
		if err := json.Unmarshal(params, &hello); err != nil {
			return nil, fmt.Errorf("bad runtime.hello: %w", err)
		}
		hello.Runtime = strings.TrimSpace(hello.Runtime)
		if hello.Runtime == "" || !runtimeIDRe.MatchString(hello.Runtime) {
			return nil, errors.New("runtime.hello needs a runtime id ([A-Za-z0-9][A-Za-z0-9_.-]*)")
		}
		hello.Name = strings.TrimSpace(hello.Name)
		if hello.Agent = strings.TrimSpace(hello.Agent); hello.Agent != "" && !runtimeIDRe.MatchString(hello.Agent) {
			return nil, fmt.Errorf("runtime.hello: bad agent %q", hello.Agent)
		}
		hello.Instance = strings.TrimSpace(hello.Instance)
		hello.Launchers = cleanLaunchers(hello.Launchers)
		stale, err := rh.reg.hello(h, hello, rh.reservedID)
		if err != nil {
			rh.logger.Printf("runtime %s #%d refused: %v", hello.Runtime, h.serial, err)
			return nil, err
		}
		for _, o := range stale { // this process again, on a new connection: the old one is dead
			o.mu.Lock()
			drop := o.drop
			o.mu.Unlock()
			o.peer.Close(errors.New("the runtime reconnected"))
			if drop != nil {
				drop()
			}
		}
		rh.logger.Printf("runtime %s %s #%d connected", hello.Runtime, hello.Version, h.serial)
		// features: what this node does beyond the first protocol. prefer_running: when two runtimes list a session, the
		// one running it answers for it (runtimes_registry.go recomputeOwners), so a runtime may list sessions it only
		// has on disk.
		// resume_argv: a session listed offline with resume_argv is resumed in the terminal engine (pi.go).
		// handover: a send refused with the handover code goes to the runtime that takes the session over (handOver).
		// transcript: a session listed with a Claude transcript has its content read from it here (runtimetranscript.go).
		// launchers: hello's launchers and runtime.launchers are offered; start_params: session.start carries the
		// launcher picked and the form's model, permission mode, effort and space; modes: session.modes reaches a
		// runtime with caps.modes; resume: a runtime with caps.resume that is the machine's engine is asked to take
		// sessions over (runtimecontrol.go).
		res := map[string]any{"protocol": RuntimeProtocol, "node_version": rh.version, "hub": rh.current() != nil,
			"features": []string{"prefer_running", "resume_argv", "handover", "transcript", "launchers", "start_params", "modes", "resume"}}
		if offer := rh.packageOffer(h, hello); offer != nil {
			res["update"] = offer
		}
		return res, nil
	case "runtime.package":
		return rh.servePackage(h, params)
	case "interaction.create":
		n := rh.current()
		if n == nil { // no hub to put it in front of the phone: ask again (same id) once one is connected
			return nil, &rpc.Error{Message: "no hub connected", Code: "node_gone"}
		}
		return n.runtimeAsk(ctx, h, params)
	case "mcp.tools":
		if h.isDaemon() { // its link answers these itself; a daemon gets no phone or Persona tools
			return nil, errUnsupportedForDaemons("Persona tools")
		}
		n := rh.current()
		if n == nil || n.peer == nil {
			return map[string]any{"tools": []proto.Tool{}}, nil
		}
		// The assistant's tools (ask_user, schedule_followup…) only for a runtime that runs the assistant, with the
		// command gates' tools among them (assistant_runtime.go).
		var p struct {
			Assistant bool `json:"assistant"`
		}
		_ = json.Unmarshal(params, &p)
		assistant := p.Assistant && h.assistant()
		var res struct {
			Tools []proto.Tool `json:"tools"`
		}
		if err := n.peer.Call(ctx, "mcp.tools", map[string]any{"assistant": assistant}, &res); err != nil {
			return nil, err
		}
		if assistant {
			res.Tools = append(res.Tools, n.gateTools()...)
		}
		if res.Tools == nil {
			res.Tools = []proto.Tool{}
		}
		return res, nil
	case "mcp.call":
		if h.isDaemon() {
			return nil, errUnsupportedForDaemons("Persona tools")
		}
		n := rh.current()
		if n == nil || n.peer == nil {
			return proto.TextResult("Persona hub is not connected", true), nil
		}
		var p struct {
			Name      string          `json:"name"`
			Args      json.RawMessage `json:"args"`
			SessionID string          `json:"session_id"`
			Assistant bool            `json:"assistant"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("bad mcp.call: %w", err)
		}
		// As the assistant only for one of the assistant's conversations that this runtime runs.
		assistant := p.Assistant && h.assistant() && rh.reg.owns(h, p.SessionID) && n.isAssistantSession(p.SessionID)
		if p.Name == toolMailSaveAttachment { // writes on this machine (mailattachment.go)
			return n.saveMailAttachment(ctx, p.Args, p.SessionID, assistant, n.guestOf(p.SessionID) != nil), nil
		}
		if g := n.gateTool(p.Name); assistant && g != nil {
			return n.assistantGateTool(ctx, g, p.SessionID, p.Args), nil
		}
		var res json.RawMessage
		err := n.peer.Call(ctx, "mcp.call", map[string]any{"name": p.Name, "args": p.Args, "session_id": p.SessionID, "assistant": assistant}, &res)
		return res, err
	}
	return nil, fmt.Errorf("unknown method %s", method)
}

// event takes a runtime's push: its list is kept whether or not a hub is connected; the rest goes to the hub, or
// nowhere between connections.
func (rh *RuntimeHost) event(h *runtimeConn, event string, params json.RawMessage) {
	h.mu.Lock()
	ready := h.ready
	h.mu.Unlock()
	if !ready {
		return // hello first
	}
	n := rh.current()
	if event == "runtime.launchers" { // its launchers changed (runtimecontrol.go)
		var p struct {
			Launchers []runtimeLauncher `json:"launchers"`
		}
		if json.Unmarshal(params, &p) == nil {
			h.setLaunchers(cleanLaunchers(p.Launchers))
		}
		return
	}
	if event == "runtime.sessions" {
		var p struct {
			Sessions []proto.Session `json:"sessions"`
		}
		// Decoded into one slice the size of the last list (a few rows to spare), which normalizeRuntimeSessions keeps
		// in place: not grown row by row from nil and then copied once more.
		p.Sessions = make([]proto.Session, 0, h.listLen()+4)
		if json.Unmarshal(params, &p) != nil {
			return
		}
		var moved []string
		if !h.isDaemon() { // a daemon's offline sessions are not resumed in this machine's terminal engine
			resume, ts := runtimeRowExtras(params)
			h.setResume(resume)
			// …nor read from this machine's transcripts: a daemon names none (runtimetranscript.go).
			if n != nil {
				ts = n.foundTranscripts(ts)
			}
			moved = h.setTranscripts(ts)
		}
		list := normalizeRuntimeSessions(h, p.Sessions)
		if cap(list) > 2*len(list)+8 { // the list shrank: keep no array of the old size
			list = append(make([]proto.Session, 0, len(list)), list...)
		}
		rewatch := rh.reg.setSessions(h, list)
		if n == nil {
			return
		}
		for _, id := range moved {
			n.transcriptMoved(h, id)
		}
		for id, since := range rewatch {
			go n.runtimeWatch(h, id, since)
		}
		n.publish(n.sessionList(context.Background(), false))
		return
	}
	if n != nil {
		n.runtimeEvent(h, event, params)
	}
}

// runtimeRowExtras is resumeArgvs and runtimeTranscripts of a runtime.sessions push, read in one pass over its rows
// rather than one each. A push one of the two cannot read (a field of the wrong type) has each read on its own, as
// before: the other may still read.
func runtimeRowExtras(params json.RawMessage) (map[string][]string, map[string]string) {
	var p struct {
		Sessions []struct {
			SessionID  string   `json:"session_id"`
			ResumeArgv []string `json:"resume_argv"`
			Transcript string   `json:"transcript"`
		} `json:"sessions"`
	}
	if json.Unmarshal(params, &p) != nil {
		return resumeArgvs(params), runtimeTranscripts(params)
	}
	resume, transcripts := map[string][]string{}, map[string]string{}
	for _, s := range p.Sessions {
		if resumable(s.SessionID, s.ResumeArgv) {
			resume[s.SessionID] = s.ResumeArgv
		}
		if namesTranscript(s.SessionID, s.Transcript) {
			transcripts[s.SessionID] = s.Transcript
		}
	}
	return resume, transcripts
}

// resumeArgvs reads the resume_argv of a runtime.sessions push: how the terminal engine resumes a session the runtime
// lists offline (a pi that ran in herdr, pi.go).
func resumeArgvs(params json.RawMessage) map[string][]string {
	var p struct {
		Sessions []struct {
			SessionID  string   `json:"session_id"`
			ResumeArgv []string `json:"resume_argv"`
		} `json:"sessions"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil
	}
	out := map[string][]string{}
	for _, s := range p.Sessions {
		if resumable(s.SessionID, s.ResumeArgv) {
			out[s.SessionID] = s.ResumeArgv
		}
	}
	return out
}

// resumable: argv is a resume_argv the node takes for session id. A command, then its arguments; bounded, no empty
// words.
func resumable(id string, argv []string) bool {
	if len(argv) == 0 || len(argv) > 16 || !runtimeIDRe.MatchString(id) {
		return false
	}
	size := 0
	for _, w := range argv {
		size += len(w)
		if w == "" || strings.ContainsAny(w, "\x00\n\r") {
			return false
		}
	}
	return size <= 4096
}

// packageOffer is the update a runtime gets in its hello reply (docs/integrations/runtimes.md "Auto-update"): this node's package of
// the name the runtime runs, when the hashes differ. A daemon gets none: its code is not this machine's to replace
// (its link does not pass the package on either).
func (rh *RuntimeHost) packageOffer(h *runtimeConn, hello runtimeHello) map[string]any {
	if hello.Package == nil || h.isDaemon() || !runtimepkg.NameRe.MatchString(hello.Package.Name) {
		return nil
	}
	pkg, err := runtimepkg.Load(hello.Package.Name)
	if err != nil {
		rh.logger.Printf("runtime %s: package %s: %v", hello.Runtime, hello.Package.Name, err)
		return nil
	}
	if pkg == nil || pkg.Hash == hello.Package.Hash {
		return nil
	}
	rh.logger.Printf("runtime %s #%d: offering package %s %s (it runs %s)", hello.Runtime, h.serial, pkg.Name, pkgHash(pkg.Hash), pkgHash(hello.Package.Hash))
	return map[string]any{"name": pkg.Name, "hash": pkg.Hash, "version": rh.version}
}

// servePackage answers runtime.package {name}: the files of this node's package, base64, with their hash.
func (rh *RuntimeHost) servePackage(h *runtimeConn, params json.RawMessage) (any, error) {
	if h.isDaemon() {
		return nil, errUnsupportedForDaemons("runtime packages")
	}
	if h.id() == "" {
		return nil, errors.New("runtime.package before runtime.hello")
	}
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(params, &p)
	if !runtimepkg.NameRe.MatchString(p.Name) {
		return nil, &rpc.Error{Message: fmt.Sprintf("bad package name %q", p.Name), Code: "bad_request"}
	}
	pkg, err := runtimepkg.Load(p.Name)
	if err != nil {
		return nil, err
	}
	if pkg == nil {
		return nil, &rpc.Error{Message: "this personad carries no package " + p.Name, Code: "not_found"}
	}
	files := make([]map[string]string, len(pkg.Files))
	for i, f := range pkg.Files {
		files[i] = map[string]string{"path": f.Path, "data": base64.StdEncoding.EncodeToString(f.Data)}
	}
	rh.logger.Printf("runtime %s #%d: sent package %s %s", h.id(), h.serial, pkg.Name, pkgHash(pkg.Hash))
	return map[string]any{"name": pkg.Name, "hash": pkg.Hash, "version": rh.version, "files": files}, nil
}

// pkgHash is the start of a package hash for the log; "none" for a runtime that has no package hash.
func pkgHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	if hash == "" {
		return "none"
	}
	return hash
}
