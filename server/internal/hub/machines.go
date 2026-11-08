package hub

import (
	"net/http"
	"slices"
	"sync"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The machines the apps see, as the manager that carries them reported (devices_reports.go): in its order, with the
// hub.json entries whose computer another entry has (absorbed) still reaching it.

const viaHub = "hub" // a path through a member hub

// pathInfo is a path as the admin page and the machine list show it (plan 5 "paths").
type pathInfo = devicewire.PathInfo

// fleet guards Hub.machines and Hub.byID, which a report replaces.
type fleet struct {
	mu sync.Mutex
}

// machinesNow is every machine the apps see, in order.
func (h *Hub) machinesNow() []*Machine {
	h.fleet.mu.Lock()
	defer h.fleet.mu.Unlock()
	return slices.Clone(h.machines)
}

// machineByID is the machine an id the apps use stands for: one the manager reported, or the machine that has the
// computer of a hub.json entry it absorbed.
func (h *Hub) machineByID(id string) *Machine {
	if id == "" {
		return nil
	}
	h.fleet.mu.Lock()
	m := h.byID[id]
	h.fleet.mu.Unlock()
	if m != nil {
		return m
	}
	d := &h.devices
	d.mu.Lock()
	to := d.absorbed[id]
	d.mu.Unlock()
	if to == "" {
		return nil
	}
	h.fleet.mu.Lock()
	defer h.fleet.mu.Unlock()
	return h.byID[to]
}

// localMachine is machineByID (the name the request handlers use).
func (h *Hub) localMachine(id string) *Machine { return h.machineByID(id) }

// machineIDs are the ids localMachine knows: the machines now, and the hub.json entries another one absorbed (the
// same computer: their id still reaches it), e.g. for the assistant, whose saved machine may be such an id. Not a
// computer waiting to be confirmed (machine_trust.go): the assistant runs on none of those.
func (h *Hub) machineIDs() []string {
	out := []string{}
	for _, m := range h.machinesNow() {
		if !h.machineUntrusted(m.cfg.ID) {
			out = append(out, m.cfg.ID)
		}
	}
	d := &h.devices
	d.mu.Lock()
	var absorbed []string
	for from := range d.absorbed {
		if !slices.Contains(out, from) && !h.machineUntrusted(from) {
			absorbed = append(absorbed, from)
		}
	}
	d.mu.Unlock()
	slices.Sort(absorbed) // the same order every time: the hello is compared with the last one
	return append(out, absorbed...)
}

// fleetRegroup: the mesh changed (the members' computers, who is the center); the manager regroups the machines.
func (h *Hub) fleetRegroup() { h.devicesConfigChanged() }

// pathInfos is a machine's paths for the admin page, as the manager reported them.
func (h *Hub) pathInfos(m *Machine) []pathInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.paths)
}

// machineList is the machines the apps see, each with the hub it is reached through and its paths.
func (h *Hub) machineList() []proto.Machine {
	self, name := h.hubID(), h.hubName()
	list := h.machinesNow()
	out := make([]proto.Machine, 0, len(list))
	for _, m := range list {
		if h.machineLeaving(m.cfg.ID) { // rejected, on its way out (machine_trust.go)
			continue
		}
		s := m.snapshot()
		if s.HubID == "" {
			s.HubID, s.HubName = self, name
		}
		if s.Route == nil {
			s.Route = &proto.MachineRoute{Via: kindOf(m.cfg)}
		}
		h.withTrust(&s, m.cfg.Kind) // trust, error_code (machine_trust.go)
		s.Manager = m.ownerOrCarrier()
		out = append(out, s)
	}
	return out
}

// recordEntries are this hub's hub.json computers as its mesh record lists them: how each one's manager says it
// reaches it now (another manager's report of it counts for nothing).
func (h *Hub) recordEntries() []recMachine {
	d := &h.devices
	d.mu.Lock()
	reach := map[string]map[string]devicewire.RecMachine{} // by manager
	for owner, list := range d.recEntries {
		reach[owner] = map[string]devicewire.RecMachine{}
		for _, e := range list {
			reach[owner][e.ID] = e
		}
	}
	d.mu.Unlock()
	var out []recMachine
	for _, mc := range h.knownEntries() {
		e := reach[h.entryOwner(mc.ID)][mc.ID]
		out = append(out, recMachine{ID: mc.ID, Cfg: mc.forMesh(), Kind: kindOf(mc), Avail: e.Avail, RTTMS: e.RTTMS})
	}
	return out
}

// machineRoutes list the machines the request may see.
func (h *Hub) machineRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines", func(w http.ResponseWriter, r *http.Request) {
		list := h.machineList()
		if em := extensionManager(r); em != "" { // an extension's token: its manager's machines only
			list = slices.DeleteFunc(list, func(m proto.Machine) bool { return h.machineOwner(m.ID) != em })
		}
		writeJSON(w, map[string]any{"machines": localizedMachines(h.requestLang(r), list)})
	})
}
