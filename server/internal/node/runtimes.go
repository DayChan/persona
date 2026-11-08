package node

// Runtimes (docs/integrations/runtimes.md, wire in docs/protocol/protocol.md "Runtime ↔ node"): agent runtimes without a terminal that keep
// their own sessions — DSH's Web plugin first (docs/integrations/dsh.md) — connect to ~/.persona/runtime.sock and hang their
// sessions, status and interactions into this node. They are not hosts in the internal/host sense (where a terminal
// lives, the machine's engine): a runtime has no panes, it reports its own sessions and content, and it runs beside
// whichever engine the machine uses. Every runtime speaks the same protocol, so a new one needs no code here.
// The wire is the hub ↔ node JSON lines (internal/rpc) on a long-lived connection: the runtime pushes its session list,
// the items of watched sessions and live text; it asks the phone through interaction.create; the node sends it every
// session.* call for a session it lists. The hub and the apps see ordinary sessions and interactions.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeclient"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// RuntimeProtocol is the runtime wire's version, answered to runtime.hello; it grows only for changes old runtimes misread.
const RuntimeProtocol = 1

// RuntimeSocketPath is where runtimes reach the node.
func RuntimeSocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(identity.SocketDir(home), "runtime.sock")
}

// runtimeCallTimeout bounds one node → runtime call; a history page of a long session is the slow one.
var runtimeCallTimeout = 30 * time.Second

// runtimeIDRe: runtime ids and the session ids a runtime may use. Wider than validID (DSH names them session-<uuid>), never a path.
var runtimeIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// runtimeHello is what a runtime says about itself first.
type runtimeHello struct {
	// Runtime is the runtime's id ("dsh"): its sessions' default agent and space kind.
	Runtime string `json:"runtime"`
	// Name is what the apps call it ("DSH"): its sessions' agent_label, the text of errors about them. Default: Runtime.
	Name string `json:"name,omitempty"`
	// Agent is the agent its sessions run, when it is not the runtime itself (a runtime hosting Claude Code).
	Agent   string `json:"agent,omitempty"`
	Version string `json:"version,omitempty"`
	// Instance identifies the process behind the connection (the SDK makes one per runtime it creates): a reconnect
	// says the same, another process configured with the same runtime id does not and is refused (runtime_conflict).
	// Empty from older runtimes.
	Instance string `json:"instance,omitempty"`
	// Launcher, when set, is offered in the new-session form; session.start with it goes to this runtime.
	Launcher string `json:"launcher,omitempty"`
	// LauncherModel is what the form shows next to the launcher (e.g. the runtime's default model).
	LauncherModel string `json:"launcher_model,omitempty"`
	// Launchers are more entries for the form, each with the agent it starts and its models: a runtime running
	// several agents (Paseo: one per provider, runtimecontrol.go). runtime.launchers replaces them later.
	Launchers []runtimeLauncher `json:"launchers,omitempty"`
	// Package is the code the runtime runs when this node can keep it up to date (runtimepkg, docs/integrations/runtimes.md "Auto-update"):
	// its package name and hash. A different hash than the node's gets an update offer in the reply.
	Package *struct {
		Name string `json:"name"`
		Hash string `json:"hash"`
	} `json:"package,omitempty"`
	Caps struct {
		Start     bool `json:"start"`
		Send      bool `json:"send"`
		Interrupt bool `json:"interrupt"`
		Rename    bool `json:"rename"`
		Close     bool `json:"close"`
		// Assistant: its launcher may run the Persona assistant (assistant_runtime.go; docs/integrations/runtimes.md "Assistant").
		Assistant bool `json:"assistant"`
		// Modes: the phone's permission mode and effort for its sessions go to it (session.modes).
		Modes bool `json:"modes"`
		// Resume: session.start with resume takes a Claude session it does not have over, under that id (the machine's
		// engine continuing a session not running anywhere, runtimecontrol.go).
		Resume bool `json:"resume"`
		// KeepAlways: the runtime keeps no session rule of its own and asks the hub to remember "Always allow for this session" for it
		// (#3974): its permission cards that offer always get the node's key for the tool. A runtime that keeps its own
		// rule (opencode's save rule, Paseo's suggestions) does not say it, so its grant is never widened to the tool.
		KeepAlways bool `json:"keep_always"`
		// Archive: session.archive / session.unarchive go to it, for its own reversible archive (archive.go): a
		// session the user archives in Persona is archived there too, and restored with it.
		Archive bool `json:"archive"`
	} `json:"caps"`
}

// runtimeConn is one connected runtime.
type runtimeConn struct {
	serial int
	peer   *rpc.Peer

	mu sync.Mutex
	// drop ends the connection (RuntimeHost.serve's); nil in tests that make connections by hand.
	drop     func()
	hello    runtimeHello
	ready    bool            // runtime.hello arrived
	sessions []proto.Session // its last runtime.sessions
	watched  map[string]bool // sessions this connection was asked to watch
	// What its list said beyond the rows the apps see: the terminal pane a session runs in (pane_id: the extension of a
	// pi in a herdr pane, pi.go), how the terminal engine resumes one it lists offline (resume_argv), and the Claude
	// session whose transcript holds a session's conversation (transcript: a runtime running Claude Code through its
	// SDK, Paseo; runtimetranscript.go).
	panes       map[string]string
	resume      map[string][]string
	transcripts map[string]string
	// daemon is its id in daemons.json when the node dialed it (daemons.go), "" for a runtime on the socket.
	daemon string
	// pid is the process at the other end of the runtime socket; 0 when not known (a daemon's link, the Orca bridge,
	// a system that does not tell). A runtime running the assistant vouches for its descendants' MCP servers
	// (mcpAssistantOf).
	pid int
}

// isDaemon: the node dialed it (a daemon in daemons.json), it did not connect to the socket.
func (h *runtimeConn) isDaemon() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.daemon != ""
}

// id is the runtime's id ("" before hello).
func (h *runtimeConn) id() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hello.Runtime
}

// agent is the agent its sessions run.
func (h *runtimeConn) agent() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hello.Agent != "" {
		return h.hello.Agent
	}
	return h.hello.Runtime
}

// assistant: the runtime runs the Persona assistant's conversations (caps.assistant).
func (h *runtimeConn) assistant() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hello.Caps.Assistant
}

// label is what the apps call the runtime.
func (h *runtimeConn) label() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hello.Name != "" {
		return h.hello.Name
	}
	return h.hello.Runtime
}

// runtimeRegistry is the node's registry of connected runtimes.
type runtimeRegistry struct {
	mu     sync.Mutex
	serial int
	conns  map[int]*runtimeConn
	// owner: session id -> the runtime listing it (the newest connection wins when two list the same id).
	owner map[string]*runtimeConn
	// watching: sessions the hub watches -> the last seq pushed for each, so a runtime that reconnects is asked to
	// watch them again from where the phone is.
	watching map[string]int
	// known: session id -> the runtime that listed it or said it has it, kept after it leaves the list. A runtime
	// lists only its recent sessions (DSH: the latest 80 of thousands), but an older one is still its own: calls
	// about it go to that runtime, never to the terminal host as a Claude session to resume (that sent a DSH
	// session's message to herdr, on a machine without herdr).
	known map[string]knownSession
	// unclaimed: ids no connected runtime had when asked, and until when not to ask again.
	unclaimed map[string]time.Time
	// logf reports what an operator should fix (a launcher two runtimes offer); nil: nowhere. warned: said once each.
	logf   func(format string, args ...any)
	warned map[string]bool
}

// knownSession is the runtime a session belongs to, by the runtime's id (a reconnect is the same runtime).
type knownSession struct {
	runtime, label string
	at             time.Time
}

// maxKnown bounds the remembered sessions; the oldest go first.
const maxKnown = 8192

func newRuntimeRegistry() *runtimeRegistry {
	return &runtimeRegistry{conns: map[int]*runtimeConn{}, owner: map[string]*runtimeConn{}, watching: map[string]int{},
		known: map[string]knownSession{}, unclaimed: map[string]time.Time{}, warned: map[string]bool{}}
}

// warnOnce logs a message once per key. Callers hold b.mu.
func (b *runtimeRegistry) warnOnce(key, format string, args ...any) {
	if b.logf == nil || b.warned[key] {
		return
	}
	b.warned[key] = true
	b.logf(format, args...)
}

// besideShell drops the runtimes' launchers named like one of the login shell's (shell): a session.start with that
// name runs the shell's (runtimeStart), so the form lists it once. Logged once per name.
func (b *runtimeRegistry) besideShell(runtime, shell []proto.Launcher) []proto.Launcher {
	names := map[string]bool{}
	for _, l := range shell {
		names[l.Name] = true
	}
	out := make([]proto.Launcher, 0, len(runtime))
	for _, l := range runtime {
		if names[l.Name] {
			b.mu.Lock()
			b.warnOnce("shell launcher "+l.Name, "runtime %s: launcher %q has the name of a launcher in the login shell, which runs instead", l.Source, l.Name)
			b.mu.Unlock()
			continue
		}
		out = append(out, l)
	}
	return out
}

// shellLauncherNames are the login shell's launcher names as last listed (none before the first list; nothing is run
// to get them).
func shellLauncherNames() map[string]bool {
	launcherCache.Lock()
	defer launcherCache.Unlock()
	names := make(map[string]bool, len(launcherCache.list))
	for _, l := range launcherCache.list {
		names[l.Name] = true
	}
	return names
}

// hello records h's runtime.hello unless another process holds the same runtime id. A ready connection with that id
// and another instance is a second process configured alike: refused with runtime_conflict (one of them needs another
// id). One with the same instance is this process's earlier connection — it reconnected before the node saw the old
// socket close — returned as stale for the caller to close. A runtime that sends no instance (an older SDK) is let in
// as before: the newest connection answers for the id.
//
// The ids in daemons.json (reserved) are the daemons' (daemons.go): a runtime on the socket may not take one, and a
// daemon whose id a runtime on the socket already has stays out until that one leaves (it is dialed again). A daemon's
// link says hello with the config's id and an instance of its own, so its next connection is its own again.
func (b *runtimeRegistry) hello(h *runtimeConn, hello runtimeHello, reserved func(string) bool) (stale []*runtimeConn, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h.mu.Lock()
	daemon := h.daemon
	h.mu.Unlock()
	switch {
	case daemon != "" && hello.Runtime != daemon:
		return nil, &rpc.Error{Code: "runtime_conflict", Message: fmt.Sprintf("daemon %s said hello as %q", daemon, hello.Runtime)}
	case daemon == "" && reserved != nil && reserved(hello.Runtime):
		return nil, &rpc.Error{Code: "runtime_conflict",
			Message: fmt.Sprintf("runtime id %q is reserved by a daemon in daemons.json; give this runtime another id", hello.Runtime)}
	}
	for _, o := range b.conns {
		if o == h {
			continue
		}
		o.mu.Lock()
		same := o.ready && o.hello.Runtime == hello.Runtime
		instance := o.hello.Instance
		onSocket := o.daemon == ""
		o.mu.Unlock()
		if !same {
			continue
		}
		if daemon != "" && onSocket {
			return nil, &rpc.Error{Code: "runtime_conflict",
				Message: fmt.Sprintf("runtime id %q is in use by a runtime on this machine's socket", hello.Runtime)}
		}
		if instance == "" || hello.Instance == "" {
			continue
		}
		if instance != hello.Instance {
			return nil, &rpc.Error{Code: "runtime_conflict",
				Message: fmt.Sprintf("another process is connected to this node as runtime %q", hello.Runtime)}
		}
		stale = append(stale, o)
	}
	h.mu.Lock()
	h.hello, h.ready = hello, true
	h.mu.Unlock()
	return stale, nil
}

// remember records that id belongs to h's runtime. Callers hold b.mu and h.mu.
func (b *runtimeRegistry) remember(h *runtimeConn, id string, now time.Time) {
	label := h.hello.Name
	if label == "" {
		label = h.hello.Runtime
	}
	b.known[id] = knownSession{runtime: h.hello.Runtime, label: label, at: now}
	delete(b.unclaimed, id)
	if len(b.known) > maxKnown {
		oldest := make([]string, 0, len(b.known))
		for k := range b.known {
			oldest = append(oldest, k)
		}
		sort.Slice(oldest, func(i, j int) bool { return b.known[oldest[i]].at.Before(b.known[oldest[j]].at) })
		for _, k := range oldest[:len(oldest)-maxKnown*3/4] {
			delete(b.known, k)
		}
	}
}

// byRuntime is the newest ready connection of the runtime with this id, nil when it is not connected. Callers hold b.mu.
func (b *runtimeRegistry) byRuntime(rt string) *runtimeConn {
	var best *runtimeConn
	for _, h := range b.conns {
		h.mu.Lock()
		ok := h.ready && h.hello.Runtime == rt
		h.mu.Unlock()
		if ok && (best == nil || h.serial > best.serial) {
			best = h
		}
	}
	return best
}

// belongs is the runtime a session belongs to: the connection listing it, else the connected runtime that listed it
// before; known: it belongs to a runtime (connected or not), with that runtime's name. A subagent of a runtime's
// session ("<parent>@<child>", splitRuntimeChild) belongs to the parent's runtime, which answers for it.
func (b *runtimeRegistry) belongs(id string) (h *runtimeConn, label string, known bool) {
	if b == nil || id == "" {
		return nil, "", false
	}
	if parent, _, ok := splitRuntimeChild(id); ok {
		id = parent
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if o := b.owner[id]; o != nil {
		return o, "", true
	}
	k, ok := b.known[id]
	if !ok {
		return nil, "", false
	}
	return b.byRuntime(k.runtime), k.label, true
}

// splitRuntimeChild splits the id of a runtime session's subagent, "<parent>@<child>" (the id Claude's subagents have
// too: docs/protocol/protocol.md "Runtime ↔ node"); false for any other id.
func splitRuntimeChild(id string) (parent, child string, ok bool) {
	parent, child, ok = strings.Cut(id, "@")
	return parent, child, ok && runtimeIDRe.MatchString(parent) && runtimeIDRe.MatchString(child)
}

// Bounds on what one runtime row may carry about the work it left running.
const (
	maxRuntimeSubagents  = 50
	maxRuntimeBackground = 50
	maxRuntimeTaskText   = 500
)

// runtimeWork keeps a row's subagents and background tasks to what the apps can use: subagents with a valid id, each
// readable as "<session>@<id>" (whatever the runtime wrote there), background tasks with an id; text clipped, the
// lists bounded. A runtime's background task has no output file of this machine's to open.
func runtimeWork(s *proto.Session) {
	clip := func(t string) string {
		if r := []rune(t); len(r) > maxRuntimeTaskText {
			return string(r[:maxRuntimeTaskText]) + "…"
		}
		return t
	}
	subs := s.Subagents[:0:0]
	seen := map[string]bool{}
	for _, a := range s.Subagents {
		if len(subs) == maxRuntimeSubagents || !runtimeIDRe.MatchString(a.ID) || seen[a.ID] {
			continue
		}
		seen[a.ID] = true
		a.SessionID = s.SessionID + "@" + a.ID
		a.Description, a.LastText, a.Type = clip(a.Description), clip(a.LastText), clip(a.Type)
		subs = append(subs, a)
	}
	s.Subagents = subs
	if len(s.Subagents) == 0 {
		s.Subagents = nil
	}
	bg := s.Background[:0:0]
	for _, t := range s.Background {
		if len(bg) == maxRuntimeBackground || strings.TrimSpace(t.ID) == "" {
			continue
		}
		if t.Kind != "agent" {
			t.Kind = "shell"
		}
		t.ID, t.Description, t.Command, t.OutputFile = clip(t.ID), clip(t.Description), clip(t.Command), ""
		bg = append(bg, t)
	}
	s.Background = bg
	if len(s.Background) == 0 {
		s.Background = nil
	}
}

// owns: the session's calls and pushes are h's (it lists the session, or listed or claimed it before).
func (b *runtimeRegistry) owns(h *runtimeConn, id string) bool {
	got, _, _ := b.belongs(id)
	return got != nil && got == h
}

// claim records that h has the session (it answered for it).
func (b *runtimeRegistry) claim(h *runtimeConn, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h.mu.Lock()
	b.remember(h, id, time.Now())
	h.mu.Unlock()
}

// askable are the connections to ask about a session nobody claimed yet, newest first; none while the id was asked
// about lately.
func (b *runtimeRegistry) askable(id string) []*runtimeConn {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if until, ok := b.unclaimed[id]; ok && time.Now().Before(until) {
		return nil
	}
	var out []*runtimeConn
	for _, h := range b.conns {
		h.mu.Lock()
		if h.ready {
			out = append(out, h)
		}
		h.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].serial > out[j].serial })
	return out
}

// unclaim: no runtime had the session; it is not asked about again for a minute.
func (b *runtimeRegistry) unclaim(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, until := range b.unclaimed {
		if time.Now().After(until) {
			delete(b.unclaimed, k)
		}
	}
	b.unclaimed[id] = time.Now().Add(time.Minute)
}

// ready are the connections that said hello.
func (b *runtimeRegistry) ready() []*runtimeConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*runtimeConn
	for _, h := range b.conns {
		h.mu.Lock()
		if h.ready {
			out = append(out, h)
		}
		h.mu.Unlock()
	}
	return out
}

// resetWatches forgets what the hub watched (another hub connection, or none, from now on). Runtimes are not asked to
// stop: an unwatch could reach one after the next hub has watched the session again, and what they push meanwhile is
// harmless (the hub drops items nobody subscribed to).
func (b *runtimeRegistry) resetWatches() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.watching = map[string]int{}
	for _, h := range b.conns {
		h.mu.Lock()
		h.watched = map[string]bool{}
		h.mu.Unlock()
	}
}

// assistantIn is the connected runtime that runs assistant conversations (caps.assistant in its hello) whose process
// is one of pids, nil when none is.
func (b *runtimeRegistry) assistantIn(pids []int) *runtimeConn {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.conns {
		h.mu.Lock()
		ok := h.ready && h.hello.Caps.Assistant && h.pid > 0 && slices.Contains(pids, h.pid)
		h.mu.Unlock()
		if ok {
			return h
		}
	}
	return nil
}

func (b *runtimeRegistry) add(peer *rpc.Peer) *runtimeConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.serial++
	h := &runtimeConn{serial: b.serial, peer: peer, watched: map[string]bool{}}
	b.conns[h.serial] = h
	return h
}

func (b *runtimeRegistry) remove(h *runtimeConn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.conns, h.serial)
	b.recomputeOwners()
}

// recomputeOwners picks, for every listed session, the connection that answers for it. Two runtimes can list the
// same session: pi's web server lists sessions it can open from disk (status offline) while a pi in a terminal has
// one of them running. The one running it wins — a send must not reach a second writer of a file pi does not lock —
// and among equals the newest connection (a runtime that reconnected). Callers hold b.mu.
func (b *runtimeRegistry) recomputeOwners() {
	type claim struct {
		h    *runtimeConn
		live bool
	}
	best := map[string]claim{}
	for _, h := range b.conns {
		h.mu.Lock()
		for _, s := range h.sessions {
			c := claim{h: h, live: s.Status != proto.StatusOffline}
			o, seen := best[s.SessionID]
			if !seen || c.live && !o.live || c.live == o.live && h.serial > o.h.serial {
				best[s.SessionID] = c
			}
		}
		h.mu.Unlock()
	}
	b.owner = make(map[string]*runtimeConn, len(best))
	for id, c := range best {
		b.owner[id] = c.h
	}
}

// setSessions records a runtime's list and returns the watched sessions this connection has not been asked to watch
// yet (with the seq to start after).
func (b *runtimeRegistry) setSessions(h *runtimeConn, list []proto.Session) map[string]int {
	b.mu.Lock()
	defer b.mu.Unlock()
	h.mu.Lock()
	h.sessions = list
	h.mu.Unlock()
	b.recomputeOwners()
	rewatch := map[string]int{}
	now := time.Now()
	h.mu.Lock()
	for _, s := range list {
		if b.owner[s.SessionID] != h {
			continue // another runtime has it running (recomputeOwners)
		}
		b.remember(h, s.SessionID, now)
		if since, ok := b.watching[s.SessionID]; ok && !h.watched[s.SessionID] {
			h.watched[s.SessionID] = true
			rewatch[s.SessionID] = since
		}
	}
	h.mu.Unlock()
	return rewatch
}

func (b *runtimeRegistry) ownerOf(id string) *runtimeConn {
	if b == nil || id == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.owner[id]
}

// sessions lists every connected runtime's sessions, each id once.
func (b *runtimeRegistry) sessions() []proto.Session {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	conns := make([]*runtimeConn, 0, len(b.conns))
	for _, h := range b.conns {
		conns = append(conns, h)
	}
	owner := make(map[string]*runtimeConn, len(b.owner))
	for id, h := range b.owner {
		owner[id] = h
	}
	b.mu.Unlock()
	sort.Slice(conns, func(i, j int) bool { return conns[i].serial < conns[j].serial })
	var out []proto.Session
	for _, h := range conns {
		h.mu.Lock()
		for _, s := range h.sessions {
			if owner[s.SessionID] == h {
				out = append(out, s)
			}
		}
		h.mu.Unlock()
	}
	return out
}

// launcherOwners maps each launcher name the runtimes offer to the one connection that answers for it: the one
// connected first, so the form and session.start agree and do not change while both stay connected. The others'
// launcher of that name is not offered (logged once). Callers hold b.mu.
func (b *runtimeRegistry) launcherOwners() map[string]*runtimeConn {
	conns := make([]*runtimeConn, 0, len(b.conns))
	for _, h := range b.conns {
		conns = append(conns, h)
	}
	sort.Slice(conns, func(i, j int) bool { return conns[i].serial < conns[j].serial })
	owners := map[string]*runtimeConn{}
	ownerIDs := map[string]string{}
	for _, h := range conns {
		h.mu.Lock()
		rt, ok := h.hello.Runtime, h.ready && h.hello.Caps.Start
		h.mu.Unlock()
		if !ok {
			continue
		}
		for _, l := range h.launcherList() {
			name := l.Name
			if owners[name] != nil {
				b.warnOnce(fmt.Sprintf("launcher %d %s", h.serial, name), "runtime %s #%d: launcher %q is offered by runtime %s already; not offered twice",
					rt, h.serial, name, ownerIDs[name])
				continue
			}
			owners[name], ownerIDs[name] = h, rt
		}
	}
	return owners
}

// launchers are the runtimes' entries for the new-session form.
func (b *runtimeRegistry) launchers() []proto.Launcher {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []proto.Launcher
	for name, h := range b.launcherOwners() {
		l, _ := h.launcherNamed(name)
		h.mu.Lock()
		// Agent, as for a Codex launcher (proto.Launcher.Agent): what it starts is not Claude Code, so the apps do
		// not offer it where only Claude Code or Codex can run (the Persona assistant).
		agent := l.Agent
		if agent == "" {
			agent = h.hello.Agent
		}
		if agent == "" {
			agent = h.hello.Runtime
		}
		if agent == "claude" {
			agent = ""
		}
		out = append(out, proto.Launcher{Name: name, Model: l.Model, Source: h.hello.Runtime, Agent: agent,
			Assistant: h.hello.Caps.Assistant && name == h.hello.Launcher})
		h.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// launcherRuntime is the runtime offering a launcher name, nil when none does.
func (b *runtimeRegistry) launcherRuntime(name string) *runtimeConn {
	if b == nil || name == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.launcherOwners()[name]
}

func (b *runtimeRegistry) watch(h *runtimeConn, id string, since int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.watching[id]; !ok || since > cur {
		b.watching[id] = since
	}
	h.mu.Lock()
	h.watched[id] = true
	h.mu.Unlock()
}

func (b *runtimeRegistry) unwatch(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.watching, id)
	for _, h := range b.conns {
		h.mu.Lock()
		delete(h.watched, id)
		h.mu.Unlock()
	}
}

// pushed moves a watched session's seq forward (the items a runtime pushed reached the hub).
// lastPushed is the last seq pushed for a watched session (0: not watched, or nothing yet).
func (b *runtimeRegistry) lastPushed(id string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.watching[id]
}

func (b *runtimeRegistry) pushed(id string, seq int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.watching[id]; ok && seq > cur {
		b.watching[id] = seq
	}
}

// listenRuntimeSocket gives n a runtime socket of its own, at runtimePath, for the connection it serves itself
// (`--stdio` with no resident node: runtimehost.go). A resident node's Nodes share one instead.
func (n *Node) listenRuntimeSocket(ctx context.Context) (*RuntimeHost, error) {
	rh := &RuntimeHost{reg: n.runtimes, path: n.runtimePath(), version: n.Version, logger: n.logger}
	if rh.reg == nil {
		rh.reg = newRuntimeRegistry()
	}
	if rh.reg.logf == nil && n.logger != nil {
		rh.reg.logf = n.logger.Printf
	}
	if err := rh.Listen(ctx); err != nil {
		return nil, err
	}
	rh.attach(n)
	return rh, nil
}

func (n *Node) runtimePath() string {
	if n.runtimeSock != "" {
		return n.runtimeSock
	}
	return RuntimeSocketPath()
}

// runtimeEvent takes a runtime's push for the hub (its list is the host's: RuntimeHost.event).
func (n *Node) runtimeEvent(h *runtimeConn, event string, params json.RawMessage) {
	h.mu.Lock()
	ready := h.ready
	h.mu.Unlock()
	if !ready {
		return // hello first
	}
	switch event {
	case "session.items":
		var p struct {
			SessionID string       `json:"session_id"`
			Items     []proto.Item `json:"items"`
			ToSeq     *int         `json:"to_seq"`
		}
		if json.Unmarshal(params, &p) != nil || !n.runtimes.owns(h, p.SessionID) {
			return
		}
		if n.runtimeTranscript(p.SessionID) != "" {
			return // its items are the transcript's, which the node pushes itself (runtimetranscript.go)
		}
		last := 0
		if len(p.Items) > 0 {
			last = p.Items[len(p.Items)-1].Seq
		}
		if p.ToSeq != nil {
			last = max(last, *p.ToSeq)
		}
		n.runtimes.pushed(p.SessionID, last)
		if n.peer != nil {
			_ = n.peer.Notify("session.items", n.echoedRaw(p.SessionID, params, "items")) // echo.go
		}
	case "session.delta":
		var p struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(params, &p) != nil || !n.runtimes.owns(h, p.SessionID) || n.peer == nil {
			return
		}
		_ = n.peer.Notify("session.delta", params)
	case "interaction.cancel":
		if n.peer != nil {
			_ = n.peer.Notify("interaction.cancel", params)
		}
	}
}

// normalizeRuntimeSessions keeps the sessions a runtime may list (valid ids, once each) and fills what the apps expect:
// the agent and its label, the space kind (the runtime's id).
func normalizeRuntimeSessions(h *runtimeConn, list []proto.Session) []proto.Session {
	h.mu.Lock()
	rt, agent, label, caps, daemon := h.hello.Runtime, h.hello.Agent, h.hello.Name, h.hello.Caps, h.daemon != ""
	h.mu.Unlock()
	if agent == "" {
		agent = rt
	}
	if label == "" {
		label = rt
	}
	readOnly := ""
	if !caps.Send {
		readOnly = label + " 没有开放从手机发消息"
	}
	seen := map[string]bool{}
	panes := map[string]string{}
	defer func() {
		h.mu.Lock()
		if h.panes == nil {
			h.panes = map[string]string{}
		}
		for id, pane := range panes {
			h.panes[id] = pane
		}
		h.mu.Unlock()
	}()
	out := make([]proto.Session, 0, len(list))
	for _, s := range list {
		if !runtimeIDRe.MatchString(s.SessionID) || seen[s.SessionID] {
			continue
		}
		seen[s.SessionID] = true
		s.MachineID, s.State, s.PendingInteractions = "", "", 0 // the hub's to fill
		s.StartedByPersona, s.Assistant = false, false
		if s.Agent == "" {
			s.Agent = agent
		}
		if s.AgentLabel == "" && (s.Agent == rt || daemon && s.Agent != "claude") {
			// The runtime runs its own agent (DSH): the apps call it by the runtime's name. A daemon is called by the
			// name its config gives, whatever agent it runs (a platform running DSH), unless that is Claude Code.
			s.AgentLabel = label
		}
		if s.SpaceKind == "" {
			s.SpaceKind = rt
		}
		// The source fields Codex app-server sessions use too (docs/protocol/protocol.md "Codex sources and capabilities"): the apps gate
		// sending, interrupting, answering cards and every terminal control on capabilities, the hub guards sends.
		// What a runtime declared, never more: no terminal, no attach, cards always answerable (they are its own asks).
		s.SourceKind, s.SourceInstanceID, s.SourceThreadID, s.ParentThreadID = proto.SourceRuntime, rt, "", ""
		if rt == paseoRuntimeID {
			s.Host = proto.EnginePaseo // an engine host, though no terminal (#2576)
		} else {
			s.Host = ""
		}
		s.Capabilities = &proto.SessionCapabilities{Read: true, Send: caps.Send, Interrupt: caps.Interrupt, Approve: true}
		s.ReadOnlyReason, s.Attached, s.CodexRoute, s.CodexRouteNote = readOnly, false, "", ""
		// The pane is the engine's to show (pi.go joins it with the engine's pane), not part of the runtime's row.
		if s.PaneID != "" && validPane(s.PaneID) {
			panes[s.SessionID] = s.PaneID
		}
		s.PaneID, s.TabID, s.WorkspaceID = "", "", ""
		switch s.Status {
		case proto.StatusWorking, proto.StatusBlocked, proto.StatusDone, proto.StatusIdle, proto.StatusOffline:
		default:
			s.Status = proto.StatusUnknown
		}
		if s.Title == "" && s.Cwd != "" {
			s.Title = filepath.Base(s.Cwd)
		}
		if s.UpdatedAt.IsZero() {
			s.UpdatedAt = time.Now().UTC()
		}
		s.UpdatedAt = s.UpdatedAt.UTC()
		runtimeWork(&s)
		out = append(out, s)
	}
	return out
}

// runtimeWatch asks a runtime to push a session's new items, from since on; for a session whose conversation is a
// transcript here (transcript: true), only its live text.
func (n *Node) runtimeWatch(h *runtimeConn, id string, since int) {
	ctx, cancel := context.WithTimeout(context.Background(), runtimeCallTimeout)
	defer cancel()
	params := map[string]any{"session_id": id, "since_seq": since}
	if n.runtimeTranscript(id) != "" {
		params["since_seq"], params["transcript"] = 0, true
	}
	if err := h.peer.Call(ctx, "session.watch", params, nil); err != nil {
		n.logger.Printf("runtime %s: watch %s: %v", h.id(), id, err)
	}
}

// runtimeAsk forwards a runtime's question / plan / permission to the phone and returns the answer; {dismiss: true}
// when nobody answered (expired, handed back to the computer, no hub). A node going away under a resumable
// interaction answers with code node_gone: the runtime asks the next node with the same id.
func (n *Node) runtimeAsk(ctx context.Context, h *runtimeConn, params json.RawMessage) (any, error) {
	var p struct {
		Interaction proto.Interaction `json:"interaction"`
		// TimeoutSeconds: how long the runtime waits at most (default: the hook timeout).
		TimeoutSeconds int `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad interaction: %w", err)
	}
	ix := p.Interaction
	switch ix.Kind {
	case proto.IxQuestion, proto.IxPlan, proto.IxPermission:
	default:
		return nil, fmt.Errorf("unsupported interaction kind %q", ix.Kind)
	}
	if !n.runtimes.owns(h, ix.SessionID) {
		return nil, fmt.Errorf("session %s is not listed by this runtime", ix.SessionID)
	}
	rt := h.id()
	if ix.ID == "" || !runtimeIDRe.MatchString(ix.ID) {
		key := ix.ToolUseID
		if key == "" {
			key = newUUID()
		}
		sum := sha256.Sum256([]byte(rt + "\x00" + ix.SessionID + "\x00" + ix.Kind + "\x00" + key))
		ix.ID = "ix_" + hex.EncodeToString(sum[:])[:16]
	}
	// Who asks, as on a Codex card (proto.Interaction.Agent): the apps draw a runtime's card as its own agent's
	// ("Allow" / "Deny", its reason, "Always allow" only with an always_label it gives), not as Claude's, whose "Always allow for this session" a
	// runtime cannot keep. A runtime hosting Claude Code (agent "claude") gets Claude's card.
	h.mu.Lock()
	agent, label := h.hello.Agent, h.hello.Name
	h.mu.Unlock()
	if agent == "" {
		agent = rt
	}
	if label == "" {
		label = rt
	}
	// A runtime running several agents (Paseo: Claude Code and others) says which one each session runs: the card is
	// that agent's.
	if s, ok := h.row(ix.SessionID); ok && s.Agent != "" && s.Agent != agent {
		agent = s.Agent
		if s.AgentLabel != "" {
			label = s.AgentLabel
		}
	}
	if agent == "claude" {
		ix.Agent, ix.AgentLabel = "", ""
	} else {
		ix.Agent, ix.AgentLabel = agent, label
	}
	now := time.Now().UTC()
	ix.MachineID, ix.Screen, ix.Dialog, ix.Secret = "", "", nil, false
	// "Always allow for this session" kept by the hub for a runtime's permission card (#3974): what it grants is the node's word, never
	// the runtime's — one key for this runtime's tool, which the manager keeps under this session id, so it never
	// reaches another session or another tool. Only for a runtime that asked for it in its hello (caps.keep_always: it
	// keeps no rule itself; a runtime whose always is a narrower rule of its own must not get the whole tool), only
	// when the card offers it (an always_label), and never in the health space or on a card with fields.
	h.mu.Lock()
	keepAlways := h.hello.Caps.KeepAlways
	h.mu.Unlock()
	ix.AlwaysKeys = nil
	if keepAlways && ix.Kind == proto.IxPermission && strings.TrimSpace(ix.AlwaysLabel) != "" && strings.TrimSpace(ix.ToolName) != "" &&
		len(ix.Fields) == 0 && !runtimeHealth(h, ix) {
		ix.AlwaysKeys = []string{"runtime:" + rt + ":tool:" + ix.ToolName}
	}
	ix.CreatedAt = now
	timeout := nodeclient.HookTimeout
	if p.TimeoutSeconds > 0 && time.Duration(p.TimeoutSeconds)*time.Second < timeout {
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}
	ix.ExpiresAt = now.Add(timeout)
	ix.Resumable = ix.ToolUseID != "" // its id is the same when the runtime asks again through the next node
	for _, s := range h.sessionsCopy() {
		if s.SessionID == ix.SessionID {
			if ix.SessionTitle == "" {
				ix.SessionTitle = s.Title
			}
			if ix.Cwd == "" {
				ix.Cwd = s.Cwd
			}
		}
	}
	res, ok, err := n.ask(ctx, ix)
	if errors.Is(err, errNodeGone) {
		return nil, &rpc.Error{Message: "node going away", Code: "node_gone"}
	}
	if err != nil {
		return nil, err
	}
	if !ok {
		return proto.Resolution{Dismiss: true}, nil
	}
	return res, nil
}

// runtimeHealth: the card's session is in the health space (its cwd or the row's), where every write is confirmed
// (docs/assistant/health.md "Outgoing").
func runtimeHealth(h *runtimeConn, ix proto.Interaction) bool {
	if identity.SpaceOf(ix.Cwd) == identity.SpaceHealth {
		return true
	}
	s, ok := h.row(ix.SessionID)
	return ok && (s.Space == identity.SpaceHealth || identity.SpaceOf(s.Cwd) == identity.SpaceHealth)
}

// setResume records how the engine resumes the sessions this runtime lists (resume_argv), replacing the last list's.
func (h *runtimeConn) setResume(m map[string][]string) {
	h.mu.Lock()
	h.resume = m
	h.mu.Unlock()
}

// resumeArgv is how the terminal engine resumes session id, as this runtime said; nil when it did not.
func (h *runtimeConn) resumeArgv(id string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.resume[id]...)
}

// row is this runtime's row for session id.
func (h *runtimeConn) row(id string) (proto.Session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		if s.SessionID == id {
			return s, true
		}
	}
	return proto.Session{}, false
}

// sessionInPane is the session a runtime runs in the terminal pane (its list's pane_id), "" when none says so. Only
// sessions it lists as running: a pane outlives the pi that told it.
func (b *runtimeRegistry) sessionInPane(pane string) string {
	if b == nil || pane == "" {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.conns {
		h.mu.Lock()
		for _, s := range h.sessions {
			if h.panes[s.SessionID] == pane && s.Status != proto.StatusOffline {
				h.mu.Unlock()
				return s.SessionID
			}
		}
		h.mu.Unlock()
	}
	return ""
}

// rowOf is the row of the runtime that answers for session id (ownerOf), with that runtime.
func (b *runtimeRegistry) rowOf(id string) (proto.Session, *runtimeConn, bool) {
	h := b.ownerOf(id)
	if h == nil {
		return proto.Session{}, nil, false
	}
	s, ok := h.row(id)
	return s, h, ok
}

func (h *runtimeConn) sessionsCopy() []proto.Session {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]proto.Session(nil), h.sessions...)
}

// claimMethods are the calls that ask the connected runtimes about a session nobody claimed yet: what the apps do
// with a session they show (open it, page it, send to it, stop it).
var claimMethods = map[string]bool{"session.send": true, "session.items": true, "session.item": true, "session.image": true,
	"session.watch": true, "session.info": true, "session.interrupt": true}

// runtimeClaimTimeout bounds asking one runtime whether it has a session.
var runtimeClaimTimeout = 5 * time.Second

// runtimeFor is the runtime a hub call about session id goes to: the one listing it, else the one that listed it
// before (an older session it no longer lists), else — for a session this machine has nothing else for (no agent, no
// transcript) — the first connected runtime that answers for it. A session of a runtime that is not connected now is
// refused (errRuntimeAway): it is not a Claude session to resume in a terminal.
func (n *Node) runtimeFor(ctx context.Context, method, id string) (*runtimeConn, error) {
	if id == "" || !strings.HasPrefix(method, "session.") || method == "session.start" {
		return nil, nil
	}
	h, label, known := n.runtimes.belongs(id)
	if h != nil {
		return h, nil
	}
	if known {
		if claimMethods[method] || method == "session.rename" || method == "session.close" || method == "session.archive" || method == "session.unarchive" {
			return nil, errRuntimeAway(label)
		}
		return nil, nil // unwatching and the like: nothing to refuse
	}
	if !claimMethods[method] || !runtimeIDRe.MatchString(id) {
		return nil, nil
	}
	conns := n.runtimes.askable(id)
	if len(conns) == 0 || n.hasOwnSession(id) {
		return nil, nil
	}
	for _, c := range conns {
		cctx, cancel := context.WithTimeout(ctx, runtimeClaimTimeout)
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		err := c.peer.Call(cctx, "session.items", map[string]any{"session_id": id, "limit": 1}, &page)
		cancel()
		if err == nil {
			n.runtimes.claim(c, id)
			return c, nil
		}
	}
	n.runtimes.unclaim(id)
	return nil, nil
}

// hasOwnSession: this machine has the session itself (a live agent in a terminal, a Claude / Codex and compatible variants record).
func (n *Node) hasOwnSession(id string) bool {
	if _, ok := n.agent(id); ok {
		return true
	}
	if n.transcriptPath(id) != "" {
		return true
	}
	for _, p := range []string{n.codexPath(id), n.traexPath(id)} {
		if _, err := os.Stat(p); p != "" && err == nil {
			return true
		}
	}
	return false
}

// errRuntimeAway: a session of a runtime that is not connected (its app is closed, the plugin is off). Nothing was
// sent; the hub may let the app send it again.
func errRuntimeAway(label string) error {
	return &rpc.Error{Message: fmt.Sprintf("%s 没有连上这台电脑的 Persona（%s 没在运行，或插件没装好），没有发出去", label, label),
		Code: "runtime_away", Data: json.RawMessage(`{"delivered":"none"}`)}
}

// errRuntimeUnsupported: a session.* call the session's runtime has nothing for (no terminal, no TUI modes, a cap it
// did not declare). Like a terminal host lacking a capability (host.ErrUnsupported), the text starts with
// "unsupported": the hub reads that prefix (a terminal stream it cannot open falls back to reading the screen).
func errRuntimeUnsupported(label, what string) error {
	return &rpc.Error{Message: fmt.Sprintf("unsupported: %s 的会话不支持%s", label, what), Code: "unsupported"}
}

// runtimeCallParams are the parameters of a hub call about a runtime's session that the node looks at.
type runtimeCallParams struct {
	SessionID      string        `json:"session_id"`
	Text           string        `json:"text"`
	Images         []proto.Image `json:"images"`
	SinceSeq       int           `json:"since_seq"`
	FoldWork       bool          `json:"fold_work"`
	LazyImages     bool          `json:"lazy_images"`
	AfterSeq       *int          `json:"after_seq"`
	ThroughSeq     int           `json:"through_seq"`
	Title          *string       `json:"title"`
	ItemID         string        `json:"item_id"`
	PermissionMode string        `json:"permission_mode"`
	Effort         string        `json:"effort"`
}

// runtimeHandle serves a hub call for a session a runtime lists; handled is false for methods that are not about one
// session (the node answers those itself).
func (n *Node) runtimeHandle(ctx context.Context, h *runtimeConn, method string, params json.RawMessage) (res any, err error, handled bool) {
	label := h.label()
	h.mu.Lock()
	caps := h.hello.Caps
	h.mu.Unlock()
	call := func(m string, p any) (json.RawMessage, error) {
		ctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
		defer cancel()
		var out json.RawMessage
		err := h.peer.Call(ctx, m, p, &out)
		if err != nil && !isRPCError(err) {
			err = fmt.Errorf("%s: %w", label, err)
		}
		return out, err
	}
	var p runtimeCallParams
	_ = json.Unmarshal(params, &p)
	// A session whose conversation is a Claude transcript here (runtimetranscript.go): its content is the node's.
	if n.runtimeTranscript(p.SessionID) != "" {
		if transcriptCalls[method] {
			return nil, nil, false // node.handle reads the transcript (n.file)
		}
		if res, err, handled := n.runtimeTranscriptCall(ctx, h, method, p, call); handled {
			return res, err, true
		}
	}
	// A session in a terminal pane (a pi in herdr, pi.go): the pane is the engine's — the screen, the keys, closing
	// it, its tab's name — and the rest is the runtime's.
	if a, inPane := n.agent(p.SessionID); inPane && a.PaneID != "" {
		switch method {
		case "session.keys", "session.screen", "session.screen.before", "terminal.watch", "terminal.unwatch", "session.close":
			return nil, nil, false
		case "session.rename":
			if p.Title == nil || strings.TrimSpace(*p.Title) == "" {
				return nil, nil, false
			}
		}
	}
	switch method {
	case "session.items":
		out, err := call("session.items", params)
		if err != nil {
			return nil, err, true
		}
		out = n.echoedRaw(p.SessionID, out, "items") // echo.go
		if !p.FoldWork || p.AfterSeq != nil || p.ThroughSeq > 0 {
			return out, nil, true
		}
		// Pages counted back from the end fold the agent's work, as for transcripts (transcript.FoldWork).
		var page map[string]json.RawMessage
		if json.Unmarshal(out, &page) != nil {
			return out, nil, true
		}
		var items []proto.Item
		if json.Unmarshal(page["items"], &items) != nil {
			return out, nil, true
		}
		folded, _ := json.Marshal(nonNil(transcript.FoldWork(items)))
		page["items"] = folded
		return page, nil, true
	case "session.image", "session.item":
		out, err := call(method, params)
		return out, err, true
	case "session.watch":
		n.runtimes.watch(h, p.SessionID, p.SinceSeq)
		_, err := call("session.watch", map[string]any{"session_id": p.SessionID, "since_seq": p.SinceSeq})
		return okResult(err), err, true
	case "session.unwatch":
		n.runtimes.unwatch(p.SessionID)
		_, err := call("session.unwatch", map[string]any{"session_id": p.SessionID})
		return okResult(err), err, true
	case "session.send":
		if !caps.Send {
			return nil, errRuntimeUnsupported(label, "从手机发消息"), true
		}
		// A session that ran in the terminal engine and has stopped: resumed there, then sent to (pi.go).
		if argv := h.resumeArgv(p.SessionID); len(argv) > 0 && n.runtimeStatus(h, p.SessionID) == proto.StatusOffline {
			res, err := n.resumeInEngine(ctx, h, p.SessionID, argv, params)
			return res, err, true
		}
		var opts sendOpts
		_ = json.Unmarshal(params, &opts)
		err := n.sends.once(opts.ClientMsgID, func() error {
			var after int
			if opts.ClientMsgID != "" {
				after = n.runtimes.lastPushed(p.SessionID)
				if n.runtimeTranscript(p.SessionID) != "" { // its items are the transcript's: its copy comes after its end
					after = n.echoBase(p.SessionID)
				}
			}
			return n.sendEchoed(p.SessionID, opts.ClientMsgID, p.Text, len(p.Images), after, func() error { // echo.go
				_, err := call("session.send", map[string]any{"session_id": p.SessionID, "text": p.Text, "images": p.Images,
					"client_msg_id": opts.ClientMsgID})
				return err
			})
		})
		if rpcCode(err) == runtimeHandover && ctx.Value(handingOver{}) == nil {
			res, err := n.handOver(ctx, h, p.SessionID, params)
			return res, err, true
		}
		return okResult(err), err, true
	case "session.interrupt":
		if !caps.Interrupt {
			return nil, errRuntimeUnsupported(label, "中断"), true
		}
		_, err := call("session.interrupt", map[string]any{"session_id": p.SessionID})
		return okResult(err), err, true
	case "session.close":
		if !caps.Close {
			return nil, errRuntimeUnsupported(label, "从手机关闭"), true
		}
		_, err := call("session.close", map[string]any{"session_id": p.SessionID})
		return okResult(err), err, true
	case "session.archive", "session.unarchive":
		// Its own archive (archive.go): one that has none keeps the archive Persona's alone.
		if !caps.Archive || strings.Contains(p.SessionID, subagentSep) {
			return archiveUnsupported(label + " has no archive the node can drive"), nil, true
		}
		if _, err := call(method, map[string]any{"session_id": p.SessionID}); err != nil {
			return nil, err, true
		}
		return nativeArchiveResult{Native: nativeArchiveDone, Via: "runtime"}, nil, true
	case "session.rename":
		if p.Title == nil || strings.TrimSpace(*p.Title) == "" {
			return okResult(nil), nil, true // a tab name: these sessions have no tab
		}
		if !caps.Rename {
			return nil, errRuntimeUnsupported(label, "改名"), true
		}
		_, err := call("session.rename", map[string]any{"session_id": p.SessionID, "title": strings.TrimSpace(*p.Title)})
		return okResult(err), err, true
	case "session.screen":
		return map[string]any{"text": "", "status": n.runtimeStatus(h, p.SessionID)}, nil, true
	case "session.info":
		// The counts a transcript gives are unknown here: zero, which the apps read as "none yet" (an iOS app older
		// than this decodes them as required numbers).
		info := map[string]any{"session_id": p.SessionID, "agent": h.agent(), "context_tokens": 0, "output_tokens": 0,
			"compactions": 0, "turns": 0}
		for _, s := range h.sessionsCopy() {
			if s.SessionID == p.SessionID {
				info["cwd"], info["model"], info["agent"], info["last_activity"] = s.Cwd, s.Model, s.Agent, s.UpdatedAt
				info["permission_mode"], info["effort"] = s.PermissionMode, s.Effort
			}
		}
		// Not in its list (an older session it still has): what the node knows without the list.
		return info, nil, true
	case "session.keys", "session.screen.before", "terminal.watch":
		return nil, errRuntimeUnsupported(label, "终端"), true
	case "terminal.unwatch":
		return okResult(nil), nil, true
	case "session.modes":
		if !caps.Modes || strings.Contains(p.SessionID, subagentSep) {
			return nil, errRuntimeUnsupported(label, "这个操作"), true
		}
		if err := validModes(p.PermissionMode, p.Effort); err != nil {
			return nil, err, true
		}
		_, err := call("session.modes", map[string]any{"session_id": p.SessionID, "permission_mode": p.PermissionMode, "effort": p.Effort})
		return okResult(err), err, true
	case "session.queue.edit", "session.queue.send_now":
		return nil, errRuntimeUnsupported(label, "这个操作"), true
	}
	return nil, nil, false
}

func (n *Node) runtimeStatus(h *runtimeConn, id string) string {
	for _, s := range h.sessionsCopy() {
		if s.SessionID == id {
			return s.Status
		}
	}
	return proto.StatusUnknown
}

// runtimeHandover is the code of a send a runtime hands over instead of taking (it lists the session from disk, and
// has asked the program that runs it to start): handOver delivers it to the runtime that takes the session.
const runtimeHandover = "handover"

// runtimeHandoverWait bounds the wait for the runtime that takes a handed-over session.
var runtimeHandoverWait = 45 * time.Second

// handingOver marks the context of a send handOver forwards: the runtime it reaches cannot hand it over again.
type handingOver struct{}

// handOver delivers a send that from (the session's owner) handed over: to the connection that lists the session as
// running once one does (prefer_running), or to the first other runtime that answers for it (an older session it
// does not list; the claim path of runtimeFor), whichever comes first within runtimeHandoverWait. Nothing is sent
// when none does.
func (n *Node) handOver(ctx context.Context, from *runtimeConn, id string, params json.RawMessage) (any, error) {
	ctx = context.WithValue(ctx, handingOver{}, true)
	forward := func(to *runtimeConn) (any, error) {
		res, err, _ := n.runtimeHandle(ctx, to, "session.send", params)
		return res, err
	}
	deadline := time.Now().Add(runtimeHandoverWait)
	var asked time.Time
	for {
		if r, owner, ok := n.runtimes.rowOf(id); ok && owner != from && r.Status != proto.StatusOffline {
			return forward(owner)
		}
		if time.Since(asked) > 2*time.Second {
			asked = time.Now()
			for _, c := range n.runtimes.ready() {
				if c == from {
					continue
				}
				cctx, cancel := context.WithTimeout(ctx, runtimeClaimTimeout)
				err := c.peer.Call(cctx, "session.items", map[string]any{"session_id": id, "limit": 1}, nil)
				cancel()
				if err == nil {
					n.runtimes.claim(c, id)
					return forward(c)
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, refusal("not_running", fmt.Sprintf("%s 把这个会话交给运行它的程序，%d 秒内没等到它接手，这次没有发送。",
				from.label(), int(runtimeHandoverWait.Seconds())), nil)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// rpcCode is the code of an rpc error, "" for anything else.
func rpcCode(err error) string {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// runtimeStart starts a session in the runtime offering the launcher; nil when no runtime does.
func (n *Node) runtimeStart(ctx context.Context, p runtimeStartParams) (*proto.Session, error, bool) {
	h := n.runtimes.launcherRuntime(p.Launcher)
	if h == nil {
		if m, ok := n.managedLauncher(p.Launcher); ok && m.AgentType() == "agy" {
			h = n.runtimes.launcherRuntime("agy")
			if p.Model == "" {
				p.Model = m.Model
			}
		}
	}
	if h == nil || shellLauncherNames()[p.Launcher] { // the login shell's launcher of that name runs, as the form lists
		return nil, nil, false
	}
	s, err := n.runtimeStartOn(ctx, h, p)
	return s, err, true
}

// runtimeStartOn starts a session in runtime h (session.start): the directory, the first message, the launcher picked
// and what the form chose with it (model, permission mode, effort), the session to continue. Fields are sent only when
// set: a runtime that knows none of them still starts its own way.
func (n *Node) runtimeStartOn(ctx context.Context, h *runtimeConn, p runtimeStartParams) (*proto.Session, error) {
	ctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
	defer cancel()
	params := map[string]any{"cwd": p.Cwd, "prompt": p.Prompt}
	for k, v := range map[string]string{"launcher": p.Launcher, "model": p.Model, "permission_mode": p.PermissionMode,
		"effort": p.Effort, "workspace_id": p.WorkspaceID, "resume": p.Resume} {
		if v != "" {
			params[k] = v
		}
	}
	if p.Shell {
		params["shell_launcher"] = true
	}
	if p.OpenElsewhere {
		params["open_elsewhere"] = true
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	if err := h.peer.Call(ctx, "session.start", params, &res); err != nil {
		return nil, err
	}
	return n.listStarted(h, res.Session)
}

// listStarted lists a session h just started right away, before the runtime's next runtime.sessions, so the chat
// that opens next finds it. Who answers for it is decided as for any list (recomputeOwners): a runtime that starts
// sessions elsewhere (pi's companion starts them in pi-web) reports them offline, and the one running it, if it has
// listed it already, keeps it.
func (n *Node) listStarted(h *runtimeConn, started proto.Session) (*proto.Session, error) {
	list := normalizeRuntimeSessions(h, []proto.Session{started})
	if len(list) == 0 {
		return nil, fmt.Errorf("%s started a session without a valid id", h.label())
	}
	s := list[0]
	b := n.runtimes
	b.mu.Lock()
	h.mu.Lock()
	found := false
	for i := range h.sessions {
		if h.sessions[i].SessionID == s.SessionID {
			h.sessions[i], found = s, true
		}
	}
	if !found {
		h.sessions = append(h.sessions, s)
	}
	h.mu.Unlock()
	b.recomputeOwners()
	b.remember(h, s.SessionID, time.Now())
	if o := b.owner[s.SessionID]; o != nil && o != h {
		o.mu.Lock()
		for _, r := range o.sessions {
			if r.SessionID == s.SessionID {
				s = r // the running one's row: its status, title, agent
			}
		}
		o.mu.Unlock()
	}
	b.mu.Unlock()
	n.publish(n.sessionList(context.Background(), false))
	return &s, nil
}

func okResult(err error) any {
	if err != nil {
		return nil
	}
	return map[string]any{"ok": true}
}

func isRPCError(err error) bool {
	var e *rpc.Error
	return errors.As(err, &e)
}
