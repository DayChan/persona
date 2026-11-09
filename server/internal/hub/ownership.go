package hub

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Ownership (docs/assistant/managers.md, "Ownership"): everything belongs to one manager, and the hub sends what concerns a thing to
// the manager it belongs to. This file is the one place that says who that is, so moving a machine or a source to
// another manager (step 3: ownership_moves.go, ownership_sources.go) changes ownership here and nowhere else:
//
//   - a machine: one of hub.json's, the manager it was moved to (moveMachine), else the one that carries hub.json's
//     machines (devicesOwner); any other (a mesh member's), devicesOwner. Only the roster and the moves decide it,
//     never what a manager reports;
//   - a session: its machine's manager (a manager's own conversations run on its own machines; moving a machine
//     moves its sessions with it);
//   - an interaction: the manager that reported it, taken only on a machine of its own (cardAllowed);
//   - what names no thing the hub can place (the assistant's pages, the inbox, the knowledge base, the model
//     configuration, the todos, the extensions and sources…): the manager the request names (ManagerHeader, or the
//     query parameter ManagerQuery), else the default manager. An item of such a list (an inbox item, an extension)
//     is reached by naming the manager its list stamped on it.
//
// A request naming a manager the roster does not have is answered 404 no_manager; one whose manager is not connected
// 503 assistant_offline, as for the one assistant before.

const (
	// ManagerHeader names the manager a request is for (an id from GET /api/v1/managers).
	ManagerHeader = assistantwire.ManagerHeader
	// ManagerQuery is the same as a query parameter (a WebSocket, a link opened in a browser).
	ManagerQuery = assistantwire.ManagerQuery
)

// errUnknownManager: the request names a manager the roster does not have.
var errUnknownManager = errors.New("no such manager")

// errOtherManager: an extension's token is for a machine, session or card of another manager's than its own
// (extensionReach); answered 403 codeOtherManager.
var errOtherManager = errors.New("this extension's manager does not own it")

const codeOtherManager = "other_manager"

// namedManager is the manager r names itself, "" when it names none.
func namedManager(r *http.Request) string {
	return assistantwire.NamedManager(r)
}

// impliedManager is the manager r is for, "" for none: an extension's token acts as its manager (the one that knew
// the token: authorizeExtension) and only as it, so naming another (header or query) is errOtherManager; any other
// credential, the manager it names.
func impliedManager(r *http.Request) (string, error) {
	named := namedManager(r)
	if p := principalOf(r.Context()); p.Role == roleExtension && p.Manager != "" {
		if named != "" && named != p.Manager {
			return "", errOtherManager
		}
		return p.Manager, nil
	}
	return named, nil
}

// requestManager is the manager a request that names no thing goes to: the one it is for (impliedManager: the one it
// names, errUnknownManager when the roster has no such manager; an extension's, its own), else the default.
func (h *Hub) requestManager(r *http.Request) (string, error) {
	id, err := impliedManager(r)
	if err != nil {
		return "", err
	}
	if id == "" {
		return h.defaultOwner(r.Context()), nil
	}
	if !h.knownManager(r.Context(), id) {
		return "", fmt.Errorf("%w: %q", errUnknownManager, clip(id, 64))
	}
	return id, nil
}

// knownManager: id is a manager of the roster (connected or not). Without a database, the ones connected and the
// assistant the hub starts.
func (h *Hub) knownManager(ctx context.Context, id string) bool {
	if id == assistantwire.AssistantID || h.busConnOf(id) != nil {
		return true
	}
	s := h.managerStore()
	if s == nil {
		return false
	}
	b, err := s.BusIdentityByID(ctx, id)
	return err == nil && b != nil && b.Role == bus.RoleManager
}

// rosterHas: id is a manager of the roster (has), and whether the roster could tell (sure: a failed lookup is not
// "no").
func (h *Hub) rosterHas(ctx context.Context, id string) (has, sure bool) {
	if id == assistantwire.AssistantID || h.busConnOf(id) != nil {
		return true, true
	}
	s := h.managerStore()
	if s == nil {
		return false, true
	}
	b, err := s.BusIdentityByID(ctx, id)
	if err != nil {
		return false, false
	}
	return b != nil && b.Role == bus.RoleManager, true
}

// ownerCache keeps the default manager for a moment: it is asked on every forwarded request.
type ownerCache struct {
	mu    sync.Mutex
	def   string
	until time.Time
	gen   uint64 // bumped by ownersChanged: a read that began before it does not keep what it read
}

const defaultOwnerTTL = 2 * time.Second

// defaultOwner is defaultManager, kept for a couple of seconds (managersChanged drops it).
func (h *Hub) defaultOwner(ctx context.Context) string {
	c := &h.owners
	c.mu.Lock()
	if c.def != "" && time.Now().Before(c.until) {
		def := c.def
		c.mu.Unlock()
		return def
	}
	gen := c.gen
	c.mu.Unlock()
	def, err := h.defaultManagerOrErr(ctx)
	if err != nil {
		return def // the assistant, because the store did not answer: not kept
	}
	c.mu.Lock()
	if c.gen == gen { // the roster did not change while it was read
		c.def, c.until = def, time.Now().Add(defaultOwnerTTL)
	}
	c.mu.Unlock()
	return def
}

// ownersChanged forgets the kept default (the roster or the default changed).
func (h *Hub) ownersChanged() {
	c := &h.owners
	c.mu.Lock()
	c.def = ""
	c.gen++
	c.mu.Unlock()
}

// machineOwner is the manager machine mid belongs to, the only one that may act on it or its sessions.
//
// It comes from the roster and the moves alone: a machine of hub.json's is its entry's (moved, else the carrier's);
// any other (a mesh member's: only the carrier is handed the mesh) the carrier's. Nothing a manager reports about
// itself (a derived report, its snapshot's sessions, its main conversation) makes a machine its own.
func (h *Hub) machineOwner(mid string) string {
	return h.entryOwner(mid)
}

// entryOwner is the manager that carries machine mid: for one its manager keeps out of hub.json, that one
// (machines_kept.go); for hub.json's, the one it was moved to (moveMachine), else the carrier. Any other machine is the
// carrier's, and so is one whose entry was removed while its move is still kept (forgetMachineOwner drops it).
func (h *Hub) entryOwner(mid string) string {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	entries := d.entries // as cfgEntries reads them
	if !d.started && h.cfg != nil {
		entries = h.cfg.Machines
	}
	if !slices.ContainsFunc(entries, func(mc MachineConfig) bool { return mc.ID == mid }) {
		if k := d.kept[mid]; k != "" { // its manager keeps it out of hub.json (machines_kept.go)
			return k
		}
		return devicesOwner
	}
	required := false
	for _, mc := range entries {
		if mc.ID == mid {
			required = mc.OwnerRequired
			break
		}
	}
	if required && d.assignFailed {
		return unknownMachineOwner
	}
	if o := d.assign[mid]; o != "" {
		return o
	}
	if required {
		return unknownMachineOwner
	}
	return devicesOwner
}

// sessionOwner is the manager session sid on machine mid belongs to: its machine's (a manager's own conversation runs
// on a machine of its own).
func (h *Hub) sessionOwner(mid, _ string) string {
	return h.machineOwner(mid)
}

// conversationOwner is the manager whose snapshot lists conversation sid; "" for none.
func (h *Hub) conversationOwner(sid string) string {
	id, _ := h.conversation(sid)
	return id
}

// interactionOwner is the manager that reported pending interaction id; "" when it is not pending.
func (h *Hub) interactionOwner(id string) string {
	if ix, ok := h.pendingInteraction(id); ok {
		return ix.Manager
	}
	return ""
}

// ---- forwarding by owner ----

// ownerOf resolves the manager a request goes to; an error is errUnknownManager.
type ownerOf func(r *http.Request) (string, error)

// byRequest: the manager the request names, else the default.
func (h *Hub) byRequest(r *http.Request) (string, error) { return h.requestManager(r) }

// byMachine: the request's machine ({mid}) decides; a request that also names a manager still goes to the
// machine's (the machine is the thing it is about). An extension's token goes only to its own manager's machine.
func (h *Hub) byMachine(r *http.Request) (string, error) {
	mid := r.PathValue("mid")
	if mid == "" {
		mid = r.PathValue("id")
	}
	return h.extensionsOwn(r, h.machineOwner(mid))
}

// bySession: the request's session ({mid}/{sid}); an extension's token, only its own manager's.
func (h *Hub) bySession(r *http.Request) (string, error) {
	return h.extensionsOwn(r, h.sessionOwner(r.PathValue("mid"), r.PathValue("sid")))
}

// byInteraction: the pending interaction ({id}) decides (an extension's token: only its own manager's); one that is
// no longer pending goes by the request.
func (h *Hub) byInteraction(r *http.Request) (string, error) {
	if id := h.interactionOwner(r.PathValue("id")); id != "" {
		return h.extensionsOwn(r, id)
	}
	return h.requestManager(r)
}

// extensionsOwn is owner, the manager of the thing r is about; errOtherManager when r carries an extension's token
// of another manager (an extension acts on its own manager's things only, as its manager does).
func (h *Hub) extensionsOwn(r *http.Request, owner string) (string, error) {
	if p := principalOf(r.Context()); p.Role == roleExtension && p.Manager != "" && owner != p.Manager {
		return "", errOtherManager
	}
	return owner, nil
}

// extensionReach checks, before the request is served, that an extension's token p is about its own manager's
// things only: a route of a machine ({mid}, machines/{id}), a session ({mid}/{sid}), a pending card
// (interactions/{id}) or a conversation (assistant/conversations/{id}) of another manager's is errOtherManager. What a
// route takes in its query or body is the route's to check (TestEveryExtensionRouteIsBounded). This covers the routes the hub serves itself as much
// as the ones it forwards by owner (byMachine, bySession, byInteraction check the same).
func (h *Hub) extensionReach(r *http.Request, p principal) error {
	if p.Role != roleExtension || p.Manager == "" || h.apiMux == nil {
		return nil
	}
	_, pattern := h.apiMux.Handler(r)
	route, values := routeValues(pattern, r)
	mid := values["mid"]
	if mid == "" && strings.HasPrefix(route, "/api/v1/machines/{id}") {
		mid = values["id"]
	}
	if mid != "" && h.machineOwner(mid) != p.Manager {
		return errOtherManager
	}
	if strings.HasPrefix(route, "/api/v1/interactions/{id}") {
		if o := h.interactionOwner(values["id"]); o != "" && o != p.Manager {
			return errOtherManager
		}
	}
	if strings.HasPrefix(route, "/api/v1/assistant/conversations/{id}") {
		if o := h.conversationOwner(values["id"]); o != "" && o != p.Manager {
			return errOtherManager
		}
	}
	return nil
}

// extensionManager is the manager whose extension's token r carries; "" for any other credential (then nothing is
// limited by it).
func extensionManager(r *http.Request) string {
	if p := principalOf(r.Context()); p.Role == roleExtension {
		return p.Manager
	}
	return ""
}

// routeValues are the path of a ServeMux pattern (its method left out) and the path values the mux sets for r on
// it, read before the mux serves r. Literal segments are taken as they are, {name} is one segment unescaped,
// {name...} the rest.
func routeValues(pattern string, r *http.Request) (string, map[string]string) {
	if i := strings.IndexByte(pattern, ' '); i >= 0 {
		pattern = pattern[i+1:]
	}
	values := map[string]string{}
	if !strings.HasPrefix(pattern, "/") {
		return pattern, values
	}
	pats := strings.Split(strings.TrimPrefix(pattern, "/"), "/")
	segs := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	for i, p := range pats {
		name, ok := strings.CutPrefix(p, "{")
		if !ok || !strings.HasSuffix(name, "}") || i >= len(segs) {
			continue
		}
		name = strings.TrimSuffix(name, "}")
		if rest, wild := strings.CutSuffix(name, "..."); wild {
			if v, err := url.PathUnescape(strings.Join(segs[i:], "/")); err == nil {
				values[rest] = v
			}
			break
		}
		if v, err := url.PathUnescape(segs[i]); err == nil && name != "$" {
			values[name] = v
		}
	}
	return pattern, values
}

// byConversation: the manager whose conversation {id} is; one no snapshot lists goes by the request.
// An extension's token reaches only its own manager's conversations.
func (h *Hub) byConversation(r *http.Request) (string, error) {
	if p := principalOf(r.Context()); p.Role == roleExtension && p.Manager != "" {
		if id := h.conversationOwner(r.PathValue("id")); id != "" {
			if _, err := h.extensionsOwn(r, id); err != nil {
				return "", err
			}
		}
		return h.requestManager(r)
	}
	if named := namedManager(r); named == "" {
		if id := h.conversationOwner(r.PathValue("id")); id != "" {
			return id, nil
		}
	}
	return h.requestManager(r)
}

func (h *Hub) todoOwner(id string) string {
	if id == "" {
		return ""
	}
	h.todoOwnersMu.RLock()
	defer h.todoOwnersMu.RUnlock()
	return h.todoOwners[id]
}

func (h *Hub) noteTodoOwner(id, manager string) {
	if id == "" || manager == "" {
		return
	}
	h.todoOwnersMu.Lock()
	if h.todoOwners == nil {
		h.todoOwners = make(map[string]string)
	}
	h.todoOwners[id] = manager
	h.todoOwnersMu.Unlock()
}

func (h *Hub) forgetTodoOwner(id string) {
	if id == "" {
		return
	}
	h.todoOwnersMu.Lock()
	delete(h.todoOwners, id)
	h.todoOwnersMu.Unlock()
}

// byTodo: the manager that owns todo {id}; when not known or not named, goes by the request.
// An extension's token reaches only its own manager's todos.
func (h *Hub) byTodo(r *http.Request) (string, error) {
	if p := principalOf(r.Context()); p.Role == roleExtension && p.Manager != "" {
		if id := h.todoOwner(r.PathValue("id")); id != "" {
			if _, err := h.extensionsOwn(r, id); err != nil {
				return "", err
			}
		}
		return h.requestManager(r)
	}
	if named := namedManager(r); named == "" {
		if id := h.todoOwner(r.PathValue("id")); id != "" {
			return id, nil
		}
	}
	return h.requestManager(r)
}

// owner resolves r's manager with by, answering 404 no_manager itself (403 codeOtherManager for an extension's token
// of another manager); false once it answered.
func (h *Hub) owner(w http.ResponseWriter, r *http.Request, by ownerOf) (string, bool) {
	id, err := by(r)
	if errors.Is(err, errOtherManager) {
		writeErrCode(w, http.StatusForbidden, codeOtherManager, err)
		return "", false
	}
	if err != nil {
		writeErrCode(w, http.StatusNotFound, "no_manager", err)
		return "", false
	}
	return id, true
}

// forwardOwned is forwardHTTP to the manager by resolves.
func (h *Hub) forwardOwned(w http.ResponseWriter, r *http.Request, by ownerOf) {
	if id, ok := h.owner(w, r, by); ok {
		h.forwardHTTP(w, r, id)
	}
}

// managerKey carries a resolved manager in a request's context (a handler wrapped around the forwarding, such as a
// reply's receipt, resolves it first).
type managerKey struct{}

func withManager(r *http.Request, id string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), managerKey{}, id))
}

// managerOfContext is the manager withManager put on r; the assistant the hub starts when none.
func managerOfContext(r *http.Request) string {
	if id, ok := r.Context().Value(managerKey{}).(string); ok && id != "" {
		return id
	}
	return assistantwire.AssistantID
}

// managerID is the manager c is; the assistant the hub starts for none (a test that plays the bus).
func (c *busConn) managerID() string {
	if c == nil || c.ident.ID == "" {
		return assistantwire.AssistantID
	}
	return c.ident.ID
}

// ownsMachine: manager c carries machine mid (what only its manager may do or ask for: report it, answer for its
// node, its credential); else a coded refusal.
func (h *Hub) ownsMachine(c *busConn, mid string) error {
	if h.machineOwner(mid) != c.managerID() {
		return &rpc.Error{Code: devicewire.CodeNoDevice, Message: fmt.Sprintf("machine %q is another manager's", clip(mid, 64))}
	}
	return nil
}

// ownsRef: what manager owner says may name machine mid and session sid (either may be ""): only a machine of its
// own. A session named without its machine goes by where it is (the machines that list it, the manager whose
// conversation it is): none of another manager's. What names neither is its own.
func (h *Hub) ownsRef(owner, mid, sid string) bool {
	if mid != "" {
		return h.machineOwner(mid) == owner
	}
	if sid == "" || owner == devicesOwner && !h.anyMoved() { // nothing moved: every machine is the carrier's
		return true
	}
	if id, _ := h.conversation(sid); id != "" && id != owner {
		return false
	}
	for _, m := range h.machinesNow() {
		if _, ok := m.session(sid); ok && h.machineOwner(m.cfg.ID) != owner {
			return false
		}
	}
	return true
}

// anyMoved: some machine is carried by another manager than the carrier.
func (h *Hub) anyMoved() bool {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.assign) > 0
}

// cardAllowed: manager owner may report pending card ix: one naming a machine, or a session, only on a machine of its
// own (ownsRef), so no card looks like it comes from another manager's session and gets its answer; a card naming
// neither is its own.
func (h *Hub) cardAllowed(owner string, ix proto.Interaction) bool {
	return h.ownsRef(owner, ix.MachineID, ix.SessionID)
}

// callerKey carries the manager whose device.call a node call is (withCaller).
type callerKey struct{}

// withCaller marks ctx as manager id's device.call: the node is reached only while the machine is id's (busNode.Call).
func withCaller(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, callerKey{}, id)
}

// callerOf is the manager withCaller put on ctx.
func callerOf(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(callerKey{}).(string)
	return id, ok
}

// deviceCallAllowed: manager id may make device.call p: on a machine of its own only (the calls about every machine,
// devices.list, answer with its own machines). Another manager's machine, its sessions and its node are refused
// no_device and never reached; the assistant the hub starts is a manager like the others.
func (h *Hub) deviceCallAllowed(id string, p assistantwire.DeviceCall) error {
	if p.Device == assistantwire.DeviceAll || h.machineOwner(p.Device) == id {
		return nil
	}
	return &rpc.Error{Code: devicewire.CodeNoDevice, Message: fmt.Sprintf("%s on machine %q: another manager's", clip(p.Method, 64), clip(p.Device, 64))}
}
