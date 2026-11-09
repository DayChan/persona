package devices

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
)

// The computers the center drives and the ways to each (docs/archive/operations/plan-hub-center.md 5, docs/server/mesh.md "Several ways"). A way —
// a path — is a hub.json entry of this hub (local, service, link, ssh, tailcat) or one of a member hub's (the center
// opens the node's stream through that hub, peer_node.go). Paths that turn out to reach the same computer (the same
// machine_uid in node.info) are one machine: listed once, connected through one path at a time (a resident node takes
// one attach and drops the one before). Which: this machine's own > a direct path > through a member, and the
// smaller round trip among equals. Sticky: the path in use stays while it works; a broken one is left at once for the
// next; a better one must stay better for mesh.path_switch_seconds before the switch, which connects the new one
// before letting the old one go.

const (
	pathClassLocal  = 0
	pathClassDirect = 1
	pathClassHub    = 2
	viaHub          = "hub" // a path through a member hub
	kindHub         = "hub" // the kind of a machine only a member has in hub.json
)

// pathCheckEvery: how often the path in use is compared with the others (a var: tests shorten it).
var pathCheckEvery = 2 * time.Second

// nodePath is one way to a computer's node.
type nodePath struct {
	key string   // "cfg:<id>" for this hub's hub.json entry, "hub:<hub_id>/<id>" for a member's
	via string   // the entry's kind (local | service | link | ssh | tailcat), or "hub"
	src *Machine // cfg: the entry whose way it is (its transports, its link, its connector)
	hub string   // hub: the member
	mid string   // hub: the computer's id in the member's hub.json
	rec devicewire.RecMachine
}

func (p *nodePath) class() int {
	switch {
	case p.via == viaHub:
		return pathClassHub
	case p.via == "local":
		return pathClassLocal
	}
	return pathClassDirect
}

// pathHealth is how a path did lately.
type pathHealth struct {
	failUntil time.Time // not tried again before this (backoff after a failure)
	fails     int
	err       string
	okAt      time.Time // last connected through it
	rtt       int       // ms: the node's round trip the last time it was in use
}

// pathInfo is a path as the admin page and the machine list show it (plan 5 "paths").
type pathInfo = devicewire.PathInfo

type fleet struct {
	mu       sync.Mutex
	saveIDs  sync.Mutex            // one write of "mesh.machine_ids" at a time, of the newest (saveMachineIDs)
	saveUIDs sync.Mutex            // one write of "mesh.path_uids" at a time, of the newest (savePathUIDs)
	derived  map[string]*Machine   // group key -> a machine only members have
	absorbed map[*Machine]*Machine // a hub.json entry whose computer another entry has -> that one
	paths    map[*Machine][]*nodePath
	ids      map[string]string // group key -> the id given to a derived machine (setting "mesh.machine_ids")
	uids     map[string]string // path key -> the machine_uid seen through it (setting "mesh.path_uids")
	health   map[string]*pathHealth
	cancel   map[*Machine]context.CancelFunc // derived machines' run loops
	changed  chan struct{}                   // closed when a regroup changed something
	order    []*Machine                      // the machines listed: hub.json's (not absorbed), then derived by id
}

func (f *fleet) init() {
	if f.paths == nil {
		f.derived, f.absorbed, f.paths = map[string]*Machine{}, map[*Machine]*Machine{}, map[*Machine][]*nodePath{}
		f.ids, f.uids, f.health = map[string]string{}, map[string]string{}, map[string]*pathHealth{}
		f.cancel, f.changed = map[*Machine]context.CancelFunc{}, make(chan struct{})
	}
}

// machinesNow is every machine the apps see, in order: hub.json's (but entries for a computer another entry has),
// then the members' computers. Before the first regroup (and in unit tests) it is hub.json's.
func (d *Devices) machinesNow() []*Machine {
	f := &d.fleet
	f.mu.Lock()
	order := f.order
	f.mu.Unlock()
	if order == nil {
		return d.cfgMachines()
	}
	return append([]*Machine(nil), order...)
}

// fleetAll is every machine object with a run loop: hub.json's, absorbed ones included, and the derived ones.
func (d *Devices) fleetAll() []*Machine {
	out := d.cfgMachines()
	f := &d.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.derived {
		out = append(out, m)
	}
	return out
}

// machineByID is the machine an id the apps use stands for: a hub.json entry (or the entry that has its computer),
// or a member's computer.
func (d *Devices) machineByID(id string) *Machine {
	if id == "" {
		return nil
	}
	entry := d.cfgMachine(id)
	f := &d.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	if m := entry; m != nil {
		if to := f.absorbed[m]; to != nil {
			return to
		}
		return m
	}
	for _, m := range f.derived {
		if m.cfg.ID == id {
			return m
		}
	}
	return nil
}

// pathsOf is a machine's paths, best first by kind (the choice also looks at health and round trips).
func (d *Devices) pathsOf(m *Machine) []*nodePath {
	f := &d.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	if ps, ok := f.paths[m]; ok {
		return append([]*nodePath(nil), ps...)
	}
	if !m.derived {
		return []*nodePath{cfgPath(m)}
	}
	return nil
}

func cfgPath(m *Machine) *nodePath {
	return &nodePath{key: "cfg:" + m.cfg.ID, via: kindOf(m.cfg), src: m}
}

func kindOf(c MachineConfig) string {
	if c.Kind == "" {
		return "ssh"
	}
	return c.Kind
}

// fleetChanged returns a channel closed at the next change of the machines or their paths.
func (d *Devices) fleetChanged() <-chan struct{} {
	f := &d.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	return f.changed
}

// hubNameOf is what a hub of the group is called ("" for an unknown one: its id).
func (d *Devices) hubNameOf(id string) string {
	cfg := d.config()
	if id == cfg.HubID {
		return cfg.HubName
	}
	if cfg.Mesh != nil {
		if hub, ok := cfg.Mesh.Hubs[id]; ok && hub.Name != "" {
			return hub.Name
		}
	}
	return id
}

// meshSettling is how long to wait before connecting anything: just started with peers, the group may have another
// center by now.
func (d *Devices) meshSettling() time.Duration {
	if mesh := d.config().Mesh; mesh != nil && time.Now().Before(mesh.SettleUntil) {
		return time.Until(mesh.SettleUntil)
	}
	return 0
}

// meshWait is closed at the next change of the hub's handover (the mesh among it).
func (d *Devices) meshWait() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cfgChanged == nil {
		d.cfgChanged = make(chan struct{})
	}
	return d.cfgChanged
}

// ShortenTimings makes the checks of tests elsewhere (the hub's end-to-end tests) quick: how often the path in use is
// compared with the others, a connector's info is read, and how long one that refused is left alone.
func ShortenTimings(pathCheck, serviceProbe, refusedWait time.Duration) {
	pathCheckEvery, serviceProbeEvery, connectorRefusedWait = pathCheck, serviceProbe, refusedWait
}
