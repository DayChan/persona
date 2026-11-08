package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The computers are the manager's (docs/server/machines.md "Who connects the computers", devicewire): the manager that carries them — the
// chief, the assistant's process, during the migration to managers — connects every one and holds what is known of it.
// The hub hands it hub.json's machines as defaults (the hello's devices section, devices.config when they change),
// relays the apps' calls and streams to it (busNode: devices.call, devices.notify), and keeps what it reports
// (devices.state, devices.event) to show the apps and drive its own features. It never dials or accepts a computer;
// a Persona Link that dials the hub's address is relayed to the manager as it is (devices_relay.go).

// devicesOwner is the manager that carries the machines.
const devicesOwner = assistantwire.AssistantID

// errManagerOffline: the manager that carries the machine is not connected; an app's call is answered 503
// assistant_offline. It counts as offline (apierr.ErrOffline) for the code that only asks that.
var errManagerOffline error = managerOfflineError{}

type managerOfflineError struct{}

func (managerOfflineError) Error() string        { return errAssistantOffline.Error() }
func (managerOfflineError) Is(target error) bool { return target == apierr.ErrOffline }

// devicesState is the hub's side of the manager's machines.
type devicesState struct {
	mu sync.Mutex
	// entries are hub.json's machines and those the admin page added since (handed to the manager).
	entries []MachineConfig
	// absorbed: a hub.json entry whose computer another entry has → that machine's id; recEntries: this hub's
	// computers as its mesh record lists them (what each one's manager says of reaching it), by manager.
	absorbed   map[string]string
	recEntries map[string][]devicewire.RecMachine
	// reported: what each manager last reported of its own machines (machine_entries.go), kept in hub_settings;
	// reportedRead: the setting was read into it (at start, or before a write since); reportedForgot, entriesForgot:
	// the managers removed since, the machines removed since, not taken back from the setting read late; refused:
	// the ids each manager's last report named that are not its own (logged once); reportedDue: a write is to go;
	// reportedSaved: the last write; reportedSave keeps writes one at a time.
	reported       reportedMachines
	reportedSince  map[string]bool // the managers that reported since the hub started
	reportedRead   bool
	reportedForgot map[string]bool
	entriesForgot  map[string]bool
	refused        map[string]map[string]bool
	reportedDue    bool
	reportedSaved  time.Time
	reportedSave   sync.Mutex
	// kept: the machines their manager keeps in its own list, out of hub.json (machines_kept.go), machine → manager;
	// keptRead: that is what machinesKeptKey holds. putTried: the hub.json machines put to their manager's own list
	// (settleOwnList), each once per manager.
	kept     map[string]string
	keptRead bool
	putTried map[string]string
	// ownListHeld: each manager's last report said it holds its own list in its manager.json (States.OwnList);
	// lastNamed: the entries its last report named (accepted or not); settleDue: a settleOwnList waits for it;
	// saidLost: the kept machines its list lacked, said once; warnedOld: the keepers whose build lost machines.own,
	// said once.
	ownListHeld map[string]bool
	lastNamed   map[string]map[string]bool
	settleDue   map[string]bool
	saidLost    map[string]bool
	warnedOld   map[string]bool
	// up is closed while the manager is connected and has reported; warmUntil: until then calls wait for it (a hub
	// just restarted, the machines shown as before).
	up        chan struct{}
	upOpen    bool
	warmUntil time.Time
	// fresh: the manager's connection is new (by manager); at its first report the hub asks it to watch what the
	// apps watch.
	fresh map[string]bool
	// sent is the config last handed over; due: a devices.config is to go out.
	sent map[string][]byte // by manager
	// assign: hub.json's machines handed to another manager than the carrier (ownership.go), machine → manager.
	assign map[string]string
	// assignRead: assign is what machineOwnersKey holds (read, or nothing to read); a move writes only over a read one.
	assignRead   bool
	assignFailed bool // ownership read failed: dedicated local machines must not fall back to the carrier
	// moving keeps moves one at a time: each writes the whole of assign, from the one before it (moveMachine).
	moving  sync.Mutex
	due     bool
	started bool
	// reqs are the nodes' requests being answered (devices.request) by the manager's key; reqEnded the keys whose
	// devices.request_end came before the request did (an event is read at once, a call in its own goroutine).
	reqs     map[string]context.CancelFunc
	reqEnded map[string]time.Time
}

func init() {
	handleBusEvent(devicewire.EventState, func(h *Hub, c *busConn, params json.RawMessage) {
		var st devicewire.States
		if err := json.Unmarshal(params, &st); err != nil {
			h.logf("bus: [%s] %s: %v", c.ident.ID, devicewire.EventState, err)
			return
		}
		h.applyDeviceStatesFrom(c.managerID(), st)
	})
	handleBusEvent(devicewire.EventNode, func(h *Hub, c *busConn, params json.RawMessage) {
		var ev devicewire.NodeEvent
		if json.Unmarshal(params, &ev) != nil || h.ownsMachine(c, ev.MachineID) != nil {
			return // only its manager speaks for a machine's node (ownership.go)
		}
		m := h.machineByID(ev.MachineID)
		if m == nil {
			return
		}
		if mux := m.portStreams(); mux != nil && mux.Event(ev.Event, ev.Params) {
			return
		}
		m.onEvent(ev.Event, ev.Params)
	})
	handleBus(devicewire.MethodRequest, func(h *Hub, ctx context.Context, c *busConn, params json.RawMessage) (any, error) {
		var p devicewire.NodeCall
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpc.Error{Code: bus.CodeInvalidParams, Message: devicewire.MethodRequest + ": " + err.Error()}
		}
		if err := h.ownsMachine(c, p.MachineID); err != nil {
			return nil, err
		}
		m := h.machineByID(p.MachineID)
		if m == nil {
			return nil, &rpc.Error{Code: devicewire.CodeNoDevice, Message: fmt.Sprintf("no machine %q", p.MachineID)}
		}
		args := p.Params
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		ctx, done := h.requestScope(ctx, requestKey(c, p.Request))
		defer done()
		return m.onRequest(ctx, p.Method, args)
	})
	handleBusEvent(devicewire.EventRequestEnd, func(h *Hub, c *busConn, params json.RawMessage) {
		var p devicewire.RequestEnd
		if json.Unmarshal(params, &p) != nil || p.Request == "" {
			return
		}
		// Its own requests only (the keys are its own); an end mark kept ahead of the request only for its own machine.
		h.requestEnded(requestKey(c, p.Request), h.ownsMachine(c, p.MachineID) == nil)
	})
	handleBus(devicewire.MethodConnected, func(h *Hub, ctx context.Context, c *busConn, params json.RawMessage) (any, error) {
		var p devicewire.Connected
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpc.Error{Code: bus.CodeInvalidParams, Message: devicewire.MethodConnected + ": " + err.Error()}
		}
		if err := h.ownsMachine(c, p.MachineID); err != nil {
			return nil, err
		}
		m := h.machineByID(p.MachineID)
		if m == nil {
			return devicewire.ConnectedReply{}, &rpc.Error{Code: devicewire.CodeNoDevice, Message: fmt.Sprintf("no machine %q", p.MachineID)}
		}
		return h.machineConnected(ctx, m, p.ConnectedAt), nil
	})
	// A service machine's credential goes only to the manager that carries it (ownership.go).
	handleBus(devicewire.MethodConnectorCred, func(h *Hub, ctx context.Context, c *busConn, params json.RawMessage) (any, error) {
		var p devicewire.ConnectorCred
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpc.Error{Code: bus.CodeInvalidParams, Message: err.Error()}
		}
		if err := h.ownsMachine(c, p.MachineID); err != nil {
			return nil, err
		}
		return h.connectorCred(ctx, p.MachineID)
	})
	handleBus(devicewire.MethodConnectorPaired, func(h *Hub, ctx context.Context, c *busConn, params json.RawMessage) (any, error) {
		var p devicewire.ConnectorCred
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpc.Error{Code: bus.CodeInvalidParams, Message: err.Error()}
		}
		if err := h.ownsMachine(c, p.MachineID); err != nil {
			return nil, err
		}
		return map[string]any{}, h.keepConnectorCred(ctx, p)
	})
}

// ---- the nodes' requests ----

// requestScope is a node's request's context: the bus connection's, ended too when the manager says the node's
// connection is gone (devices.request_end), as a node's own connection ending ends what the hub does for it.
func (h *Hub) requestScope(ctx context.Context, key string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	if key == "" {
		return ctx, cancel
	}
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ended := d.reqEnded[key]; ended {
		delete(d.reqEnded, key)
		cancel()
		return ctx, cancel
	}
	if d.reqs == nil {
		d.reqs = map[string]context.CancelFunc{}
	}
	d.reqs[key] = cancel
	return ctx, func() {
		d.mu.Lock()
		delete(d.reqs, key)
		d.mu.Unlock()
		cancel()
	}
}

// requestKey is manager c's request key: one manager's keys never name another's requests.
func requestKey(c *busConn, key string) string {
	if key == "" {
		return ""
	}
	return c.managerID() + "\x00" + key
}

// requestEnded: devices.request_end. A request that is not running yet is marked ended for when it comes, if mark.
func (h *Hub) requestEnded(key string, mark bool) {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	if cancel := d.reqs[key]; cancel != nil {
		delete(d.reqs, key)
		cancel()
		return
	}
	if !mark {
		return
	}
	now := time.Now()
	for k, at := range d.reqEnded { // its request never came (it was answered, or the bus dropped): not kept long
		if now.Sub(at) > time.Minute {
			delete(d.reqEnded, k)
		}
	}
	if d.reqEnded == nil {
		d.reqEnded = map[string]time.Time{}
	}
	d.reqEnded[key] = now
}
