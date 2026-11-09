package hub

import (
	"context"
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/link"
)

// ---- the handover ----

// devicesConfig is what the hub hands the manager now.
func (h *Hub) devicesConfig() devicewire.Config { return h.devicesConfigFor(devicesOwner) }

// devicesConfigFor is what the hub hands manager id: the machines it carries (ownership.go); the mesh's members
// only to the carrier.
func (h *Hub) devicesConfigFor(id string) devicewire.Config {
	out := devicewire.Config{Machines: []json.RawMessage{}, HubID: h.hubID(), HubName: h.hubName(), HubVersion: h.version}
	for _, mc := range h.cfgEntries() {
		if h.entryOwner(mc.ID) != id {
			continue
		}
		if b, err := json.Marshal(mc); err == nil {
			out.Machines = append(out.Machines, b)
		}
	}
	if h.cfg != nil {
		out.AutoUpdateNodes = h.cfg.AutoUpdateNodes
		out.TailcatDERPMapURL = h.cfg.Tailcat.DERPMapURL
		out.WebURL = h.webURL()
	}
	out.NodePackagesURL, out.NodePackagesProxy = h.nodePackages()
	if every, timeout, set := h.linkPingTimes(); set {
		out.LinkPingMS, out.LinkPingTimeoutMS = int(every.Milliseconds()), int(timeout.Milliseconds())
	}
	msg := h.linkEndpoints()
	out.MeshHubs = msg.MeshHubs
	for _, e := range msg.Endpoints {
		out.Endpoints = append(out.Endpoints, devicewire.LinkEndpoint{URL: e.URL, Kind: e.Kind})
	}
	if id == devicesOwner {
		out.Mesh = h.devicesMesh()
	}
	out.MachinesOwn = h.ownsList(id) // its own list is the list; the hub edits it (machines_kept.go)
	if out.MachinesOwn && h.listHeld(id) {
		// It holds its list as its own: the hand-over carries none. What hub.json stages for it is put to it
		// (settleOwnList); the first hand-over, before it said so, still carries them for it to take over.
		out.Machines = nil
	}
	if !out.MachinesOwn && h.keepsAny(id) {
		// A manager that keeps machines in its own list, on a build that no longer does: its manager.json is not
		// written over with a list without them (a phase-2 build writes every hand-over that has one).
		out.Machines = nil
		h.warnKeeperOld(id)
	}
	out.PendingUnknown = h.trustUnknown()
	for _, mid := range h.pendingMachineIDs() {
		if h.entryOwner(mid) == id {
			out.Pending = append(out.Pending, mid)
		}
	}
	return out
}

// webURL is public_url as the web app's address for Persona Link (devicewire.Config.WebURL), as addressOf reads it: an
// https one only, its sub-path kept, without a trailing slash; "" otherwise (a plain http one is how the apps reach
// the hub on the LAN).
func (h *Hub) webURL() string {
	if base, _, ok := h.addressOf(h.cfg.PublicURL); ok && strings.HasPrefix(base, "https://") {
		return base
	}
	return ""
}

// nodePackages is where the manager fetches node builds (hub.json's node_packages): its url, else this build's update
// URL (a release hub's, a Docker image built with one); "off" none.
func (h *Hub) nodePackages() (url, proxy string) {
	if h.cfg != nil {
		url, proxy = h.cfg.NodePackages.URL, h.cfg.NodePackages.Proxy
	}
	switch strings.TrimSpace(url) {
	case "off":
		return "", ""
	case "":
		url = h.build.UpdateURL
	}
	return strings.TrimRight(strings.TrimSpace(url), "/"), proxy
}

// devicesMesh is the mesh as the manager needs it (nil without one).
func (h *Hub) devicesMesh() *devicewire.Mesh {
	ms := &h.mesh
	ms.mu.Lock()
	running := ms.running
	var settle time.Time
	if running && len(ms.peers) > 0 && len(ms.links) == 0 {
		settle = ms.started.Add(meshSettle)
	}
	ms.mu.Unlock()
	if !running {
		return nil
	}
	out := &devicewire.Mesh{Member: h.isMember(), SettleUntil: settle, Hubs: map[string]devicewire.MeshHub{}}
	if h.cfg != nil {
		out.PathSwitchMS = h.cfg.Mesh.pathSwitch().Milliseconds()
	}
	if out.Member {
		out.CenterName = h.hubNameOf(h.centerID())
	}
	v := h.meshView()
	if v != nil {
		for id, vh := range v.hubs {
			if id == h.hubID() {
				continue
			}
			out.Hubs[id] = devicewire.MeshHub{Name: h.hubNameOf(id), Reachable: vh.reachable, RTTMS: v.rttTo(id)}
		}
	}
	if !out.Member {
		for _, r := range h.memberRecords() {
			mr := devicewire.MemberRecord{HubID: r.HubID}
			for _, rm := range r.Machines {
				cfg, _ := json.Marshal(rm.Cfg)
				mr.Machines = append(mr.Machines, devicewire.RecMachine{ID: rm.ID, Cfg: cfg, Kind: rm.Kind, Avail: rm.Avail, RTTMS: rm.RTTMS})
			}
			out.Members = append(out.Members, mr)
		}
	}
	return out
}

// devicesConfigChanged hands the manager the config again if it changed (a regroup of the mesh, an entry added, the
// hub's addresses), at most every 200 ms.
func (h *Hub) devicesConfigChanged() {
	d := &h.devices
	d.mu.Lock()
	if d.due {
		d.mu.Unlock()
		return
	}
	d.due = true
	d.mu.Unlock()
	time.AfterFunc(200*time.Millisecond, func() {
		d.mu.Lock()
		d.due = false
		d.mu.Unlock()
		for _, p := range h.busRoster() {
			if p.Role != bus.RoleManager {
				continue
			}
			b, _ := json.Marshal(h.devicesConfigFor(p.ID))
			d.mu.Lock()
			same := string(b) == string(d.sent[p.ID])
			d.mu.Unlock()
			if same {
				continue
			}
			if err := h.notifyBus(p.ID, devicewire.EventConfig, json.RawMessage(b)); err == nil {
				d.mu.Lock()
				if d.sent == nil {
					d.sent = map[string][]byte{}
				}
				d.sent[p.ID] = b
				d.mu.Unlock()
			}
		}
	})
}

// devicesHello is the hello's devices section for manager id: the machines it carries (none, for most).
func (h *Hub) devicesHello(id string) *devicewire.Config {
	cfg := h.devicesConfigFor(id)
	b, _ := json.Marshal(cfg)
	d := &h.devices
	d.mu.Lock()
	if d.sent == nil {
		d.sent = map[string][]byte{}
	}
	d.sent[id] = b
	d.mu.Unlock()
	return &cfg
}

// cfgEntries is hub.json's machines and those added since it started.
func (h *Hub) cfgEntries() []MachineConfig {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started && h.cfg != nil {
		return slices.Clone(h.cfg.Machines)
	}
	return slices.Clone(d.entries)
}

// cfgEntry is the hub.json entry with this id (nil: none).
func (h *Hub) cfgEntry(id string) *MachineConfig {
	for _, mc := range h.cfgEntries() {
		if mc.ID == id {
			return &mc
		}
	}
	return nil
}

// addMachineLive hands the manager a hub.json entry the admin page just added: it connects it now. A hub that is not
// running (tests) reads it at its next start.
func (h *Hub) addMachineLive(mc MachineConfig) {
	d := &h.devices
	d.mu.Lock()
	if !d.started {
		d.mu.Unlock()
		return
	}
	for _, e := range d.entries {
		if e.ID == mc.ID {
			d.mu.Unlock()
			return
		}
	}
	d.entries = append(d.entries, mc)
	d.mu.Unlock()
	h.setLeaving(mc.ID, false) // added again after a reject (machine_trust.go)
	h.logf("[%s] added (%s): the manager connects it", mc.ID, kindOf(mc))
	h.devicesConfigChanged()
	h.meshTouch()
}

// removeMachineLive takes a hub.json entry out of what the manager connects: it lets the computer go now. A hub that
// is not running (tests) does not read it again.
func (h *Hub) removeMachineLive(id string) {
	d := &h.devices
	d.mu.Lock()
	if !d.started {
		d.mu.Unlock()
		return
	}
	n := len(d.entries)
	d.entries = slices.DeleteFunc(d.entries, func(e MachineConfig) bool { return e.ID == id })
	gone := len(d.entries) != n
	d.mu.Unlock()
	if !gone {
		return
	}
	h.logf("[%s] removed: the manager lets it go", id)
	h.forgetMachineOwner(context.Background(), id) // added again, it starts at the carrier
	h.forgetMachineEntry(id)
	h.devicesConfigChanged()
	h.meshTouch()
}

// addedLive is the entries added while the hub runs (pendingRestart).
func (h *Hub) addedLive() []MachineConfig {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started || h.cfg == nil {
		return nil
	}
	var out []MachineConfig
	for _, e := range d.entries {
		if !slices.ContainsFunc(h.cfg.Machines, func(c MachineConfig) bool { return c.ID == e.ID }) {
			out = append(out, e)
		}
	}
	return out
}

func kindOf(c MachineConfig) string {
	if c.Kind == "" {
		return "ssh"
	}
	return c.Kind
}

// withoutName is the entry but for its name, which the running hub takes as it is (machine_rename.go).
func (c MachineConfig) withoutName() MachineConfig {
	c.Name = ""
	return c
}

// forMesh is a hub.json entry as a member tells the center about it: no ssh host, tailcat address or binary path.
func (c MachineConfig) forMesh() MachineConfig {
	c.SSHHost, c.TailcatAddr, c.RemoteBin, c.Address, c.Proxy = "", "", "", "", ""
	return c
}

// linkPingTimes are the heartbeats of the Persona Links the hub relays: hub.json's link section (config_link.go), else
// link.PingEvery and PingTimeout; tests set their own (h.linkPing). set: not the defaults, the manager is told them
// (devicesConfig) and tells Persona Link.
func (h *Hub) linkPingTimes() (every, timeout time.Duration, set bool) {
	if h.linkPing[0] > 0 {
		return h.linkPing[0], h.linkPing[1], true
	}
	if h.cfg != nil {
		if every, timeout, ok := h.cfg.Link.pingTimes(); ok {
			return every, timeout, true
		}
	}
	return link.PingEvery, link.PingTimeout, false
}

// memberRecords are the records of the hubs whose computers the center reaches through them: the other hubs that
// take this hub for their center, not seen for too long excluded.
func (h *Hub) memberRecords() []*hubRecord {
	self := h.hubID()
	ms := &h.mesh
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if !ms.running {
		return nil
	}
	var out []*hubRecord
	for id, r := range ms.records {
		if id == self || r.Center != self || time.Since(ms.seenAt[id]) > meshRecordKeep {
			continue
		}
		if _, paired := ms.peers[id]; !paired && !h.meshView().reachable(id) {
			continue // left the group (no longer paired with anyone here)
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HubID < out[j].HubID })
	return out
}
