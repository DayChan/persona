package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The bus (docs/protocol/protocol.md "Bus"): a service — the assistant (a manager) for now — dials GET /api/v1/bus with a
// token of its own (bustokens.go) and keeps the connection up; the hub starts nothing anywhere. Over it runs
// internal/rpc, one JSON line per WebSocket text message, calls going both ways. A connection first says
// manager.hello; after that the service calls the methods registered with handleBus and publishes the events
// registered with handleBusEvent, and the hub calls it with callBus. The hub knows no service's semantics: each
// method is a handler registered by name. A new connection of an identity replaces the one before; only the current
// one may call or publish.

// busCall answers one method a service calls on its connection c; params is never empty (`{}` at least). An
// *rpc.Error it returns reaches the service with its code.
type busCall func(h *Hub, ctx context.Context, c *busConn, params json.RawMessage) (any, error)

// busEvent takes one event a service published. It runs in the connection's read loop, so it must not block.
type busEvent func(h *Hub, c *busConn, params json.RawMessage)

// The registered methods and events (handleBus, handleBusEvent), filled in by the init functions of the files that
// have the handlers, and only read after that.
var (
	busMethods = map[string]busCall{}
	busEvents  = map[string]busEvent{}
)

// handleBus registers what answers method once a connection said its handshake. Call it from an init function, next
// to the handler:
//
//	func init() { handleBus("notify.request", (*Hub).busNotifyRequest) }
func handleBus(method string, fn busCall) {
	if _, dup := busMethods[method]; dup || method == busHello {
		panic("bus: method registered twice: " + method)
	}
	busMethods[method] = fn
}

// handleBusEvent registers what takes event (a one-way message) once a connection said its handshake; unregistered
// events are dropped.
func handleBusEvent(event string, fn busEvent) {
	if _, dup := busEvents[event]; dup {
		panic("bus: event registered twice: " + event)
	}
	busEvents[event] = fn
}

func init() {
	handleBus("ping", func(*Hub, context.Context, *busConn, json.RawMessage) (any, error) { return map[string]any{}, nil })
}

// busHello is the handshake: the only call a connection may make before it went through.
const busHello = "manager.hello"

// busAssistant is the manager identity the assistant connects as.
const busAssistant = assistantwire.AssistantID

var errBusOffline = errors.New("not connected to the bus")

// errBusDropped is errBusOffline for a call whose request had gone out when the connection dropped (rpc.ErrDropped):
// the other side may have done it.
var errBusDropped = fmt.Errorf("dropped while the call was out: %w", errBusOffline)

// busState is the live connections and the roster (memory only: a service says its handshake again after a hub
// restart).
type busState struct {
	mu       sync.Mutex
	live     map[*busConn]struct{} // every open connection, handshake or not
	current  map[string]*busConn   // the roster: identity id -> the connection whose handshake went through last
	stopping bool
	// Tests shorten these; zero is the protocol's (internal/bus).
	pingEvery, pingTimeout, helloTimeout time.Duration
	// idents keeps the identities instead of h.store (tests without Postgres).
	idents busStore
	// storeErrLog throttles the line for a token lookup the store did not answer; storeDown is until when (UnixNano)
	// dials are answered 503 without a lookup after one failed.
	storeErrLog everyMinute
	storeDown   atomic.Int64
	// voidErrLog throttles the line for an install code sent over public plaintext that the store could not void
	// (managers.go, voidPlaintextCode).
	voidErrLog everyMinute
	// saidOld: the managers already said to be from before manager.json (no config.own), once each while the hub runs.
	saidOld map[string]bool
	// rosterGen numbers the roster changes (busRosterChanged): an older list never goes out after a newer one.
	rosterGen atomic.Uint64
}

// busConn is one service connection.
type busConn struct {
	ident   db.BusIdentity
	ws      *websocket.Conn
	peer    *rpc.Peer
	helloed chan struct{} // closed once a handshake went through
	once    sync.Once
	closing sync.Once

	// mutesSettled: its first word after the handshake was heard, and the mutes went once more (fleet_link.go).
	mutesSettled atomic.Bool
	// clientsSettled: likewise, and it was told what the apps can take (fleet.clients, clientsSettle).
	clientsSettled atomic.Bool

	// proofKey is the key of its manager's proofs that a request with one of its extensions' tokens came through it
	// (extwire.ProofKey of the token it connected with, extensions_proof.go): made from the token as it connects, kept
	// here only, never stored.
	proofKey string

	// guarded by busState.mu
	presence busPresence
	replaced bool // a newer connection of the identity took over
	ended    bool

	// roster is the managers event on its way to it (sendRoster): the newest list, its number and the one sent last.
	roster struct {
		mu         sync.Mutex
		want, sent uint64
		list       []busManagerBrief
		sending    bool // a rosterSender runs
	}
}

// busPresence is a connected identity as the roster has it: who, and where its handshake says it runs.
type busPresence struct {
	ID      string `json:"id"`
	Role    string `json:"role"`
	Name    string `json:"name"`
	Host    string `json:"host,omitempty"`
	OS      string `json:"os,omitempty"`
	Arch    string `json:"arch,omitempty"`
	Deploy  string `json:"deploy,omitempty"`
	Version string `json:"version,omitempty"`
	// Capabilities are what its manager.hello names (assistantwire.Capability*).
	Capabilities []string  `json:"capabilities,omitempty"`
	Since        time.Time `json:"since"`
}

// close sends the close frame saying why; the read loop then ends the connection.
func (c *busConn) close(code websocket.StatusCode, reason string) {
	c.closing.Do(func() { go func() { _ = c.ws.Close(code, reason) }() })
}

// storeDownFor is how long after a failed token lookup the bus answers 503 without another.
var storeDownFor = 2 * time.Second

// busLookupTimeout bounds a token lookup; one that runs out is the store not answering (storeDown).
var busLookupTimeout = 5 * time.Second

// everyMinute lets one thing through a minute (a log line).
type everyMinute struct {
	mu   sync.Mutex
	next time.Time
}

// Allow: the minute since the last one let through is over.
func (e *everyMinute) Allow(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if now.Before(e.next) {
		return false
	}
	e.next = now.Add(time.Minute)
	return true
}

// busRoutes mounts the bus beside the apps' API: its token is not a device's (auth), and a member hub does not pass
// it on to its center (memberForward).
func (h *Hub) busRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET "+bus.Path, h.serveBus)
	h.relayRoutes(mux) // the manager's second channel: the connections relayed to it (devices_relay.go)
}

// busCaller is the identity a bus request's bearer token names (the header only: a token in a URL ends up in logs).
// It answers 403 / 401 / 429 itself, as the gate and auth do for the apps: plaintext from the internet is refused,
// and failures from there count against the source like a device token's.
func (h *Hub) busCaller(w http.ResponseWriter, r *http.Request) (db.BusIdentity, bool) {
	public := h.sourceOf(r) == sourcePublic
	if public && r.TLS == nil && !h.cfg.Security.AllowPublicPlaintext {
		h.securityError(w, r, http.StatusForbidden, "tls_required", 0)
		return db.BusIdentity{}, false
	}
	if public {
		if retry := h.devtok.failures.retry(authSourceKey(r), time.Now(), h.cfg.Security); retry > 0 {
			retryAfter(w, retry)
			h.securityError(w, r, http.StatusTooManyRequests, "rate_limited", 0)
			return db.BusIdentity{}, false
		}
	}
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if h.bus.storeDown.Load() > time.Now().UnixNano() {
		// The store failed a moment ago: answered without asking it again, so the dials of an outage cost no query
		// each (a manager retries at most once a minute; anyone else gets no more than this).
		writeErrCode(w, http.StatusServiceUnavailable, bus.CodeStoreUnavailable, errors.New("the hub cannot check tokens right now"))
		return db.BusIdentity{}, false
	}
	// The lookup is the hub's, not the client's: a client hanging up mid-lookup must not cancel it, or its "context
	// canceled" would pass for a store error and shut the bus for everyone (storeDown). Bounded by its own timeout.
	lookup, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), busLookupTimeout)
	ident, ok, err := h.busIdentity(lookup, tok)
	cancel()
	if err != nil && r.Context().Err() != nil {
		return db.BusIdentity{}, false // the client is gone: nobody to answer, and nothing said about the store
	}
	if err != nil {
		// The store did not answer (Postgres restarting): no verdict on the token, so 503, which a service retries.
		// A 401 here would make a standalone manager take its token for revoked and stop.
		// Logged at most once a minute. Not an authentication failure: managers behind one address reconnecting through
		// an outage would lock that address (the user's phone on it too) out of the apps' API. The next storeDownFor
		// is answered without a lookup instead.
		if h.bus.storeErrLog.Allow(time.Now()) {
			h.logf("bus: looking the token up: %v", err)
		}
		h.bus.storeDown.Store(time.Now().Add(storeDownFor).UnixNano())
		writeErrCode(w, http.StatusServiceUnavailable, bus.CodeStoreUnavailable, errors.New("the hub cannot check tokens right now"))
		return db.BusIdentity{}, false
	}
	if ok && ident.Role == bus.RoleManager { // the only role so far
		return ident, true
	}
	failures := 0
	if public {
		failures = h.devtok.failures.fail(authSourceKey(r), time.Now(), h.cfg.Security)
	}
	code := ""
	if strings.HasPrefix(tok, busTokenPrefix) {
		code = bus.CodeTokenRevoked // a bus token the hub has no identity for: revoked, or replaced by a new one
	}
	h.securityError(w, r, http.StatusUnauthorized, code, failures)
	return db.BusIdentity{}, false
}

func (h *Hub) serveBus(w http.ResponseWriter, r *http.Request) {
	ident, ok := h.busCaller(w, r)
	if !ok {
		return
	}
	if !h.enterWS() {
		http.Error(w, "hub stopping", http.StatusServiceUnavailable)
		return
	}
	defer h.wsHandlers.Done()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // services are not browsers: no Origin to check
		CompressionMode:    websocket.CompressionContextTakeover,
	})
	if err != nil {
		return
	}
	// The read loop is bound to the request, not to a hang-up: a connection closed with a reason still sends its
	// close frame.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	conn := bus.NewConn(ctx, ws)
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ") // the one busCaller took
	c := &busConn{ident: ident, proofKey: extwire.ProofKey(tok), ws: ws, peer: rpc.New("h", conn), helloed: make(chan struct{})}
	// A card the hub's own feature asks (fleet.ask) is withdrawn when that feature stops waiting (a tool call whose
	// caller went away, #2597).
	c.peer.Cancels = map[string]bool{assistantwire.MethodFleetAsk: true}
	c.peer.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return h.busRequest(ctx, c, method, params)
	}
	c.peer.OnEvent = func(event string, params json.RawMessage) { h.busEvent(c, event, params) }
	if !h.busAdd(c) {
		_ = ws.Close(websocket.StatusGoingAway, "hub stopping")
		return
	}
	defer h.busEnded(c)
	go func() {
		err := bus.Keepalive(ctx, ws, durationOr(h.bus.pingEvery, bus.PingEvery), durationOr(h.bus.pingTimeout, bus.PingTimeout))
		if ctx.Err() == nil {
			h.logf("bus: [%s] %v: hanging up", ident.ID, err)
			_ = ws.CloseNow()
		}
	}()
	go func() {
		limit := durationOr(h.bus.helloTimeout, bus.HelloTimeout)
		t := time.NewTimer(limit)
		defer t.Stop()
		select {
		case <-c.helloed:
		case <-ctx.Done():
		case <-t.C:
			h.logf("bus: [%s] no %s within %s: hanging up", ident.ID, busHello, limit)
			c.close(bus.CloseRefused, "no handshake in time")
		}
	}()
	_ = c.peer.Serve(ctx, conn)
}

func durationOr(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// busRequest is the connection's dispatcher: its handshake, then busMethods on the current connection only.
func (h *Hub) busRequest(ctx context.Context, c *busConn, method string, params json.RawMessage) (any, error) {
	if len(params) == 0 || string(params) == "null" {
		params = json.RawMessage(`{}`)
	}
	if method == busHello {
		return h.managerHello(ctx, c, params)
	}
	if !h.busCurrent(c) {
		return nil, &rpc.Error{Code: bus.CodeStaleConnection, Message: "say " + busHello + " on the current connection first"}
	}
	h.mutesSettle(c)
	h.clientsSettle(c)
	fn, ok := busMethods[method]
	if !ok {
		return nil, &rpc.Error{Code: bus.CodeUnknownMethod, Message: "unknown method " + clip(method, 80)}
	}
	return fn(h, ctx, c, params)
}

func (h *Hub) busEvent(c *busConn, event string, params json.RawMessage) {
	if !h.busCurrent(c) {
		return // before the handshake, or replaced: not heard
	}
	h.mutesSettle(c)
	h.clientsSettle(c)
	if f := busEvents[event]; f != nil {
		f(h, c, params)
	}
}

// refuse answers a handshake it will not take and hangs up a moment later, once the answer went out.
func (c *busConn) refuse(h *Hub, err error) (any, error) {
	h.logf("bus: [%s] %v: hanging up", c.ident.ID, err)
	time.AfterFunc(200*time.Millisecond, func() { c.close(bus.CloseRefused, "wrong identity") })
	return nil, &rpc.Error{Code: bus.CodeWrongIdentity, Message: err.Error()}
}

// busManagerHello is what manager.hello says that the hub keeps for now; the rest (apps, the machine's summary) is
// the follow-up's.
type busManagerHello struct {
	ID           string   `json:"id"`
	Host         string   `json:"host"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Deploy       string   `json:"deploy"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
}

// busHelloReply is manager.hello's answer: who the hub knows the service as, the managers, and the assistant's hello
// (assistantwire.Hello: the user's profile as the hub has it, where the assistant runs; empty when not set).
type busHelloReply struct {
	Name     string            `json:"name"`
	Role     string            `json:"role"`
	Managers []busManagerBrief `json:"managers"`
	assistantwire.Hello
	// Devices: hub.json's machines and what the manager that carries them needs (devices_handover.go); only to it.
	Devices *devicewire.Config `json:"devices,omitempty"`
}

type busManagerBrief struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
	Online bool   `json:"online"`
}

func (h *Hub) managerHello(ctx context.Context, c *busConn, params json.RawMessage) (any, error) {
	var p busManagerHello
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &rpc.Error{Code: bus.CodeInvalidParams, Message: err.Error()}
	}
	if p.ID != c.ident.ID {
		return c.refuse(h, fmt.Errorf("manager.hello says %q but the token is %q's", clip(p.ID, 64), c.ident.ID))
	}
	if err := h.busAccept(ctx, c, busPresence{Host: clip(p.Host, 200), OS: clip(p.OS, 40), Arch: clip(p.Arch, 40),
		Deploy: clip(p.Deploy, 40), Version: clip(p.Version, 80), Capabilities: capabilityList(p.Capabilities)}); err != nil {
		return nil, err
	}
	if c.ident.Role == bus.RoleManager && !slices.Contains(p.Capabilities, assistantwire.CapabilityConfigOwn) {
		h.bus.mu.Lock()
		said := h.bus.saidOld[c.ident.ID]
		if h.bus.saidOld == nil {
			h.bus.saidOld = map[string]bool{}
		}
		h.bus.saidOld[c.ident.ID] = true
		h.bus.mu.Unlock()
		if !said {
			h.logf("bus: [%s] is from before manager.json (no %s): the hello no longer carries the manager's defaults "+
				"(owner, notes, Agents, knowledge base, models), so it runs on its built-in ones and its knowledge base "+
				"does not open; update it", c.ident.ID, assistantwire.CapabilityConfigOwn)
		}
	}
	reply := busHelloReply{Name: c.ident.Name, Role: c.ident.Role, Managers: h.busManagers(ctx), Hello: h.assistantHelloFor(ctx, c.ident.ID),
		Devices: h.devicesHello(c.ident.ID)}
	if c.ident.ID == assistantwire.AssistantID && p.Deploy != "child" { // elsewhere, or a service of its own
		reply.LegacyConfig = h.legacyConfig()
	}
	return reply, nil
}

// capabilityList is a hello's capabilities, bounded: at most 32 names of at most 64 bytes.
func capabilityList(in []string) []string {
	var out []string
	for _, c := range in {
		if c = strings.TrimSpace(c); c != "" && len(c) <= 64 && len(out) < 32 && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// busAccept makes c its identity's current connection, and hangs up the one it replaces.
func (h *Hub) busAccept(ctx context.Context, c *busConn, p busPresence) error {
	p.ID, p.Role, p.Name, p.Since = c.ident.ID, c.ident.Role, c.ident.Name, time.Now().UTC()
	h.bus.mu.Lock()
	if c.replaced || c.ended {
		h.bus.mu.Unlock()
		return &rpc.Error{Code: bus.CodeStaleConnection, Message: "a newer connection took over"}
	}
	if h.bus.current == nil {
		h.bus.current = map[string]*busConn{}
	}
	old := h.bus.current[c.ident.ID]
	h.bus.current[c.ident.ID] = c
	c.presence = p
	if old != nil && old != c {
		old.replaced = true
	}
	h.bus.mu.Unlock()
	c.once.Do(func() { close(c.helloed) })
	if old == c {
		return nil // a second handshake on the same connection
	}
	if old != nil {
		old.close(bus.CloseReplaced, "replaced by a newer connection")
	}
	h.logf("bus: [%s] connected: host %q %s/%s, version %q", p.ID, p.Host, p.OS, p.Arch, p.Version)
	h.busSeen(ctx, c.ident.ID, &db.BusIdentity{Host: p.Host, OS: p.OS, Arch: p.Arch, Deploy: p.Deploy, Version: p.Version})
	h.busRosterChanged(ctx, c)
	if c.ident.ID == assistantwire.AssistantID {
		go h.assistantConnected()
	} else {
		h.managerReconnected(c.ident.ID) // its tools are asked for again (manager_state.go)
	}
	if c.ident.Role == bus.RoleManager {
		h.devicesConnected(c.ident.ID)    // its first report: the sessions the apps watch, watched again (devices_reports.go)
		if _, ok := h.mutesEvent(); !ok { // not in the hello's answer: handed over once they can be read (fleet_link.go)
			go h.mutesRetry(c.ident.ID)
		}
	}
	return nil
}

func (h *Hub) busAdd(c *busConn) bool {
	h.bus.mu.Lock()
	defer h.bus.mu.Unlock()
	if h.bus.stopping {
		return false
	}
	if h.bus.live == nil {
		h.bus.live = map[*busConn]struct{}{}
	}
	h.bus.live[c] = struct{}{}
	return true
}

func (h *Hub) busEnded(c *busConn) {
	h.bus.mu.Lock()
	delete(h.bus.live, c)
	c.ended = true
	current := h.bus.current[c.ident.ID] == c
	if current {
		delete(h.bus.current, c.ident.ID)
	}
	h.bus.mu.Unlock()
	// Each of its connections, a replaced one too: a token its manager dropped while this one went could not be told.
	h.dropLeftManager(c.ident.ID)
	if current {
		h.extLost.lost(c.ident.ID, time.Now()) // its extensions may still call for a while (extensions_proof.go)
		h.logf("bus: [%s] disconnected", c.ident.ID)
		if c.ident.Role == bus.RoleManager {
			go h.devicesGone(c.ident.ID) // its machines (devices_reports.go)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		h.busSeen(ctx, c.ident.ID, nil)
		h.busRosterChanged(ctx, nil)
	}
}

func (h *Hub) busCurrent(c *busConn) bool {
	h.bus.mu.Lock()
	defer h.bus.mu.Unlock()
	return h.bus.current[c.ident.ID] == c
}

// busConnOf is identity id's current connection, nil while it has none.
func (h *Hub) busConnOf(id string) *busConn {
	h.bus.mu.Lock()
	defer h.bus.mu.Unlock()
	return h.bus.current[id]
}

// busRoster is who is connected now, by id.
func (h *Hub) busRoster() []busPresence {
	h.bus.mu.Lock()
	out := make([]busPresence, 0, len(h.bus.current))
	for _, c := range h.bus.current {
		out = append(out, c.presence)
	}
	h.bus.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// busManagers are the managers the hub has tokens for, and which of them are connected.
func (h *Hub) busManagers(ctx context.Context) []busManagerBrief {
	out := []busManagerBrief{}
	s := h.busStore()
	if s == nil {
		return out
	}
	list, err := h.busIdentities(ctx, s)
	if err != nil {
		h.logf("bus: identities: %v", err)
	}
	for _, b := range list {
		out = append(out, busManagerBrief{ID: b.ID, Name: b.Name, Role: b.Role, Online: h.busConnOf(b.ID) != nil})
	}
	return out
}

// busRosterChanged tells the connected managers who is online now (event managers), except the one whose
// handshake answer already says so. It does not wait for the writes: a manager whose connection takes none (a sleeping
// laptop) would hold up the others, the handshake of one that just connected (busAccept) and the teardown of one that
// left (busEnded) until its keepalive hangs up.
func (h *Hub) busRosterChanged(ctx context.Context, except *busConn) {
	gen := h.bus.rosterGen.Add(1) // before the list: the list of a later change sees every change before it
	h.forgetIdentities()          // and it is read afresh
	list := h.busManagers(ctx)
	h.bus.mu.Lock()
	var to []*busConn
	for _, c := range h.bus.current {
		if c != except {
			to = append(to, c)
		}
	}
	h.bus.mu.Unlock()
	for _, c := range to {
		c.sendRoster(gen, list)
	}
}

// sendRoster queues list, roster change gen, for c unless a newer one is queued already, and starts c's sender when
// none runs: one per connection, so one that takes no writes keeps a single goroutine until it hangs up.
func (c *busConn) sendRoster(gen uint64, list []busManagerBrief) {
	r := &c.roster
	r.mu.Lock()
	defer r.mu.Unlock()
	if gen <= r.want {
		return
	}
	r.want, r.list = gen, list
	if !r.sending {
		r.sending = true
		go c.rosterSender()
	}
}

// rosterSender writes c's queued list until the newest one went out (or failed: the connection is ending).
func (c *busConn) rosterSender() {
	r := &c.roster
	r.mu.Lock()
	for r.sent < r.want {
		r.sent = r.want
		list := r.list
		r.mu.Unlock()
		_ = c.peer.Notify("managers", map[string]any{"managers": list})
		r.mu.Lock()
	}
	r.sending = false
	r.mu.Unlock()
}

// callBus calls method on service id's current connection: errBusOffline while it has none, or when it drops
// meanwhile (errBusDropped, which is errBusOffline too, when the request had gone out). Without a deadline on ctx it
// gives up after two minutes.
func (h *Hub) callBus(ctx context.Context, id, method string, params, out any) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	return h.callBusAsIs(ctx, id, method, params, out)
}

// callBusAsIs is callBus with ctx's own deadline, or none: a call to a machine's node through the manager waits as
// long as its caller would have for the node itself (a plugin's nodes.call, an upload).
func (h *Hub) callBusAsIs(ctx context.Context, id, method string, params, out any) error {
	c := h.busConnOf(id)
	if c == nil {
		return fmt.Errorf("%s: %w", id, errBusOffline)
	}
	if params == nil {
		params = map[string]any{}
	}
	err := c.peer.Call(ctx, method, params, out)
	switch {
	case errors.Is(err, rpc.ErrDropped):
		return fmt.Errorf("%s: %w", id, errBusDropped)
	case errors.Is(err, rpc.ErrClosed):
		return fmt.Errorf("%s: %w", id, errBusOffline)
	}
	return err
}

// notifyBus sends service id a one-way event on its current connection.
func (h *Hub) notifyBus(id, event string, params any) error {
	c := h.busConnOf(id)
	if c == nil {
		return fmt.Errorf("%s: %w", id, errBusOffline)
	}
	return c.peer.Notify(event, params)
}

// busHangUp closes every open connection of identity id, with code and reason, and the relays it took
// (devices_relay.go cutRelays: a revoked or replaced manager keeps no Persona Link or stream).
func (h *Hub) busHangUp(id string, code websocket.StatusCode, reason string) {
	defer h.cutRelays(id, "")
	h.bus.mu.Lock()
	var cut []*busConn
	for c := range h.bus.live {
		if c.ident.ID == id {
			cut = append(cut, c)
		}
	}
	h.bus.mu.Unlock()
	for _, c := range cut {
		c.close(code, reason)
	}
}

// stopBus refuses new connections and returns the open ones, for the shutdown to close (stopWebSockets).
func (h *Hub) stopBus() []*busConn {
	h.bus.mu.Lock()
	defer h.bus.mu.Unlock()
	h.bus.stopping = true
	out := make([]*busConn, 0, len(h.bus.live))
	for c := range h.bus.live {
		out = append(out, c)
	}
	return out
}

// busSeen keeps in the roster that identity id is connected now (where: its handshake's place) or just dropped, and
// tells the apps (managers.go).
func (h *Hub) busSeen(ctx context.Context, id string, where *db.BusIdentity) {
	if s := h.managerStore(); s != nil {
		if err := s.SeeBusIdentity(ctx, id, where); err != nil {
			h.logf("bus: [%s] roster: %v", id, err)
		}
	}
	h.managersChanged()
}
