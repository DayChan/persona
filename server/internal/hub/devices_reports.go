package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/streams"
)

// ---- what the manager reports ----

// startDevices makes the machines of hub.json before the managers report them: shown as before a restart while they
// come back (what the last hub process saw them report, saveDevicesWarm), offline otherwise, as their manager last
// reported them (machine_entries.go); a call waits for the carrier meanwhile.
func (h *Hub) startDevices(ctx context.Context) {
	d := &h.devices
	d.mu.Lock()
	d.entries, d.started = slices.Clone(h.cfg.Machines), true
	if d.up == nil {
		d.up = make(chan struct{})
	}
	d.mu.Unlock()
	if err := h.loadMachineOwners(ctx); err != nil {
		h.logf("devices: ownership unavailable; dedicated local machines stay unassigned: %v", err)
	}
	_ = h.loadMachinesKept(ctx) // and the machines their managers keep out of hub.json (machines_kept.go)
	h.loadMachineEntries(ctx)   // what each manager last reported of them (machine_entries.go)
	var saved devicesWarm
	if h.store != nil {
		if ok, err := h.store.TakeSetting(ctx, devicesWarmKey, &saved); err != nil || !ok || time.Since(saved.SavedAt) > devicesWarmStale {
			saved.Machines = nil
		}
	}
	until := time.Now().Add(devicesWarmGrace)
	n := 0
	known := h.knownEntries()
	h.fleet.mu.Lock()
	for _, mc := range known {
		if h.byID[mc.ID] != nil {
			continue
		}
		m := newMachine(h, mc)
		m.managed, m.peer = true, &busNode{h: h, id: mc.ID, done: make(chan struct{})}
		m.owner = h.entryOwner(mc.ID)
		if e, ok := h.reportedEntryOf(m.owner, mc.ID); ok { // as its manager last reported it, until it reports again
			m.info, m.lastSeen = e.Info, e.LastSeen
		}
		m.engine = saved.Engines[mc.ID]
		if c, ok := saved.EngineHosts[mc.ID]; ok {
			m.engineHosts = &c
		}
		if w, ok := saved.Machines[mc.ID]; ok && (w.Until.IsZero() || w.Until.After(time.Now())) {
			m.info, m.sessions, m.warmUntil = w.Info, w.Sessions, until
			if !w.Until.IsZero() && w.Until.Before(until) {
				m.warmUntil = w.Until
			}
			n++
		}
		h.machines = append(h.machines, m)
		h.byID[mc.ID] = m
	}
	h.fleet.mu.Unlock()
	d.mu.Lock()
	d.warmUntil = until
	d.mu.Unlock()
	if n > 0 {
		h.logf("warm start: %d machines shown as before while the manager reconnects them", n)
	}
	time.AfterFunc(devicesWarmGrace, h.devicesWarmEnded)
}

func markUnknownLocked(list []proto.Session) {
	for i := range list {
		if list[i].Status != proto.StatusOffline {
			list[i].Status = proto.StatusUnknown
			if list[i].PendingInteractions == 0 { // the manager's states as it would show them: a card still waits
				list[i].State = proto.StateUnknown
			}
		}
	}
}

// devicesUp: the manager that carries the machines is connected and has reported them.
func (h *Hub) devicesUp() bool {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.upOpen
}

// waitDevices returns nil once the manager is up; errManagerOffline at once while it is not, unless the hub just
// started (its machines shown as before): then a call waits for it that long.
func (h *Hub) waitDevices(ctx context.Context) error {
	d := &h.devices
	d.mu.Lock()
	if d.upOpen {
		d.mu.Unlock()
		return nil
	}
	if d.up == nil {
		d.up = make(chan struct{})
	}
	up, until := d.up, d.warmUntil
	d.mu.Unlock()
	if !time.Now().Before(until) {
		return errManagerOffline
	}
	t := time.NewTimer(time.Until(until))
	defer t.Stop()
	select {
	case <-up:
		return nil
	case <-t.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	return errManagerOffline
}

// awaitManager is waitDevices for what cannot wait long (a WebSocket handshake, a peer's request): at most limit.
// True once the manager is up.
func (h *Hub) awaitManager(ctx context.Context, limit time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return h.waitDevices(ctx) == nil
}

// applyDeviceStates is applyDeviceStatesFrom the manager that carries hub.json's machines.
func (h *Hub) applyDeviceStates(st devicewire.States) { h.applyDeviceStatesFrom(devicesOwner, st) }

// applyDeviceStatesFrom takes devices.state from manager owner: every machine it carries, in the apps' order. The
// other managers' machines stay as they were, and only the machines owner carries are taken (ownership.go
// machineOwner: another manager's, and a mesh member's from any manager but the carrier, are dropped). The apps get
// the managers' machines grouped: devicesOwner's first, then the others' by id.
func (h *Hub) applyDeviceStatesFrom(owner string, st devicewire.States) {
	now := time.Now()
	type change struct {
		m              *Machine
		ended, offline bool
		began          bool // the node's connection is a new one (or the hub just met it): it knows no fs.watch (fswatch.go)
		oldMux         *streams.Mux
	}
	var changes []change
	h.fleet.mu.Lock()
	prev := h.byID
	order := make([]*Machine, 0, len(st.Machines))
	byID := map[string]*Machine{}
	for _, s := range st.Machines {
		id := s.Machine.ID
		if id == "" || byID[id] != nil {
			continue
		}
		m := prev[id]
		// Only its manager reports a machine (ownership.go): hub.json's or one a manager keeps, its owner's, derived or
		// not (another one is letting it go, or claims what is not its own); any other (a mesh member's), the
		// carrier's only.
		if known := h.knownEntry(id) != nil; known && h.entryOwner(id) != owner || !known && owner != devicesOwner {
			continue
		}
		if m == nil || !m.managed {
			// Its entry stays as it was made (it is read without m.mu everywhere, as when the hub connected it).
			m = newMachine(h, h.mirrorConfig(s))
			m.managed, m.peer = true, &busNode{h: h, id: id, done: make(chan struct{})}
		}
		m.mu.Lock()
		m.owner = owner
		was := m.online || m.warmLocked(now)
		wasConn, wasAt := m.connected, m.connectedAt
		snap := s.Machine
		// online is the node connected now, as it was when the hub connected it (self-update waits for it, the node
		// updates go to it); the manager's warm start is warmUntil, as the hub's own was (the apps see both as online).
		m.snap, m.info, m.online, m.err = &snap, s.Info, s.Connected, s.Machine.Error
		// What it is called: hub.json's entry says for one of this hub's own in hub.json (the manager follows it a moment
		// later), the manager for one it keeps (machines_kept.go) and for a member's (the member renamed it).
		if mc := h.cfgEntry(id); mc != nil && !s.Derived {
			m.setName(cmp.Or(mc.Name, id))
		} else {
			m.setName(snap.Name)
		}
		if s.Machine.LastSeen.After(m.lastSeen) {
			m.lastSeen = s.Machine.LastSeen
		}
		m.derived, m.paths = s.Derived, s.Paths
		m.connected, m.connectedAt = s.Connected, s.ConnectedAt
		m.warmUntil = s.WarmUntil
		c := change{m: m}
		c.ended = wasConn && (!s.Connected || !s.ConnectedAt.Equal(wasAt))
		c.began = s.Connected && (!wasConn || !s.ConnectedAt.Equal(wasAt))
		if c.ended || s.Connected && !wasConn {
			c.oldMux, m.streams = m.streams, nil
		}
		if s.Connected && m.streams == nil {
			m.streams = streams.New(m.notify) // forwarded ports (ports.go)
		}
		if was && !m.online && !m.warmLocked(now) {
			markUnknownLocked(m.sessions)
			c.offline = true
		}
		m.mu.Unlock()
		order = append(order, m)
		byID[id] = m
		changes = append(changes, c)
	}
	var gone []*Machine
	groups := map[string][]*Machine{owner: order}
	var unmanaged []*Machine
	for _, m := range h.machines { // the previous order
		if byID[m.cfg.ID] != nil {
			continue
		}
		if !m.managed {
			unmanaged = append(unmanaged, m) // a test's machine stays
			continue
		}
		if o := m.ownerOrCarrier(); o != owner {
			groups[o] = append(groups[o], m)
		} else {
			gone = append(gone, m)
		}
	}
	for id, m := range prev { // one not in the order (none, but a map is the truth)
		if byID[id] == nil && !slices.Contains(h.machines, m) && !m.managed {
			unmanaged = append(unmanaged, m)
		}
	}
	owners := make([]string, 0, len(groups))
	for o := range groups {
		if o != devicesOwner {
			owners = append(owners, o)
		}
	}
	sort.Strings(owners)
	all := slices.Clone(groups[devicesOwner])
	for _, o := range owners {
		all = append(all, groups[o]...)
	}
	all = append(all, unmanaged...)
	for _, m := range all {
		byID[m.cfg.ID] = m
	}
	h.machines, h.byID = all, byID
	h.fleet.mu.Unlock()

	d := &h.devices
	d.mu.Lock()
	if d.recEntries == nil {
		d.recEntries = map[string][]devicewire.RecMachine{}
	}
	d.recEntries[owner] = st.Entries
	if owner == devicesOwner { // hub.json's machines, the hub's start: the carrier's
		d.absorbed = st.Absorbed
		if d.up == nil {
			d.up = make(chan struct{})
		}
		if !d.upOpen {
			close(d.up)
			d.upOpen = true
		}
		d.warmUntil = time.Time{} // the hub's start is over: from now on a call without the manager fails at once
	}
	fresh := d.fresh[owner]
	delete(d.fresh, owner)
	d.mu.Unlock()
	h.takeMachineEntries(owner, st) // kept for when it is away (machine_entries.go)
	named := map[string]bool{}
	for _, e := range st.Entries {
		named[e.ID] = true
	}
	d.mu.Lock()
	if d.ownListHeld == nil {
		d.ownListHeld, d.lastNamed = map[string]bool{}, map[string]map[string]bool{}
	}
	heldBefore := d.ownListHeld[owner]
	d.ownListHeld[owner], d.lastNamed[owner] = st.OwnList, named
	d.mu.Unlock()
	if heldBefore != st.OwnList {
		h.devicesConfigChanged() // its hand-over carries machines, or no longer does
	}
	if st.OwnList && h.ownsList(owner) {
		go h.settleOwnList(owner) // hub.json's machines it keeps now move out (machines_kept.go)
	}
	// The rejected computers the manager let go are not kept out any more (machine_trust.go).
	h.fleetLeft(func(id string) bool { return byID[id] != nil || st.Absorbed[id] != "" })
	if fresh {
		go h.watchAgain(order)
	}

	for _, c := range changes {
		if c.oldMux != nil {
			c.oldMux.CloseAll()
		}
		if c.ended {
			h.connectionEnded(c.m, i18n.T(h.userLang(), "hub-devices_link.computer_offline"))
		}
	}
	for _, m := range gone {
		h.connectionEnded(m, i18n.T(h.userLang(), "hub-devices_link.computer_offline"))
		m.mu.Lock()
		mux := m.streams
		m.streams = nil
		m.mu.Unlock()
		if mux != nil {
			mux.CloseAll()
		}
	}
	h.broadcastMachines()
	for _, c := range changes {
		if c.offline {
			h.sessionsChanged(c.m)
		}
		if c.began {
			go h.fsWatchReconnected(c.m) // the web panels' open folders (#2439)
		}
	}
}

// mirrorConfig is a reported machine's entry: hub.json's for one of its own, else the member's, with the id the
// manager gave it.
func (h *Hub) mirrorConfig(s devicewire.State) MachineConfig {
	if mc := h.cfgEntry(s.Machine.ID); mc != nil && !s.Derived {
		return *mc
	}
	var mc MachineConfig
	_ = json.Unmarshal(s.Config, &mc)
	mc.ID, mc.Kind = s.Machine.ID, s.Machine.Kind
	if s.Machine.Name != "" {
		mc.Name = s.Machine.Name
	}
	return mc
}

// devicesConnected: the manager that carries the machines connected (its handshake). The port streams and terminals
// that ran through its last connection ended with it, as they do when a node's own connection ends (the manager ends
// them on the nodes: internal/devices hubopen.go); a terminal whose first watch waits for this connection stays.
func (h *Hub) devicesConnected(owner string) {
	d := &h.devices
	d.mu.Lock()
	delete(d.reportedForgot, owner) // a manager in the roster again: what it reports is kept
	// A new connection holds its own list only once its report says so: until then its hand-over carries what
	// hub.json stages for it, and edits are staged there (machines_kept.go).
	delete(d.ownListHeld, owner)
	delete(d.lastNamed, owner)
	if d.fresh == nil {
		d.fresh = map[string]bool{}
	}
	d.fresh[owner] = true
	d.mu.Unlock()
	for _, m := range h.machinesNow() {
		m.mu.Lock()
		mux := m.streams
		if !m.managed || mux == nil || cmp.Or(m.owner, devicesOwner) != owner {
			m.mu.Unlock()
			continue
		}
		m.streams = streams.New(m.notify)
		m.mu.Unlock()
		mux.CloseAll()
	}
	t := &h.terms
	t.mu.Lock()
	ran := map[[2]string]*termRelay{}
	for k, r := range t.relays {
		if r.synced != nil || r.observing {
			ran[k] = r
		}
	}
	t.mu.Unlock()
	for k, r := range ran {
		if m := h.machineByID(k[0]); m != nil && m.managed && m.ownerOrCarrier() == owner {
			h.terminalEnded(k[0], k[1], errAssistantOffline.Error(), r)
		}
	}
}

// watchAgain asks a manager that is new to watch the sessions the apps watch (it watches them again itself whenever
// a node reconnects). A few at a time: one slow computer does not hold up the others' items.
func (h *Hub) watchAgain(list []*Machine) {
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, m := range list {
		m.mu.Lock()
		managed := m.managed
		// The sessions the apps watch, and those the old manager was told to watch: the new one watches none of them
		// yet (watchAgainFresh).
		ids := map[string]bool{}
		for id, n := range m.watchers {
			if n > 0 {
				ids[id] = true
			}
		}
		for id := range m.nodeWatch {
			ids[id] = true
		}
		m.mu.Unlock()
		if !managed {
			continue
		}
		for id := range ids {
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() { <-sem; wg.Done() }()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				m.watchAgainFresh(ctx, id)
			}()
		}
	}
	wg.Wait()
}

// connectionEnded ends what ran on a node's connection that is gone: its terminals, the text being generated.
func (h *Hub) connectionEnded(m *Machine, why string) {
	h.forgetLiveText(m.cfg.ID) // the node that was generating it is gone: the transcript has what was finished
	h.terminalsEnded(m.cfg.ID, why)
	h.fullsOffline(m.cfg.ID) // the full herdr views ran on that connection too
	// The questions waiting on it are the manager's (assistant/fleet_ix.go NodeDetached).
}

// devicesGone: the manager that carries the machines disconnected. The machines show offline (why: it is not
// there), their terminals and streams close, their questions end; when it is back it reports them again.
func (h *Hub) devicesGone(owner string) {
	d := &h.devices
	d.mu.Lock()
	if d.upOpen && owner == devicesOwner {
		d.up = make(chan struct{})
		d.upOpen = false
	}
	d.mu.Unlock()
	// The hub's own stop (the manager gets its SIGTERM at the same time): the next hub shows them as they were
	// (startDevices). A moment's wait lets the hub's stop begin first.
	time.Sleep(500 * time.Millisecond)
	if h.stopping.Load() || h.busConnOf(owner) != nil || owner == devicesOwner && h.devicesUp() {
		return
	}
	d.mu.Lock()
	delete(d.recEntries, owner) // it reaches none of them now (the mesh record says so)
	d.mu.Unlock()
	var off []*Machine
	for _, m := range h.machinesNow() {
		m.mu.Lock()
		if !m.managed || cmp.Or(m.owner, devicesOwner) != owner {
			m.mu.Unlock()
			continue
		}
		m.mu.Unlock()
		h.offlineLocal(m, i18n.Localize(errAssistantOffline, h.userLang()))
		off = append(off, m)
	}
	for _, m := range off {
		h.connectionEnded(m, i18n.Localize(errAssistantOffline, h.userLang()))
	}
	if len(off) > 0 {
		h.machineEntriesChanged() // seen until now (offlineLocal)
		h.logf("devices: [%s] the manager is gone: %d machines offline until it is back", owner, len(off))
		h.broadcastMachines()
		for _, m := range off {
			h.sessionsChanged(m)
		}
	}
}

// offlineLocal shows m offline with error why until its manager reports it again: its streams close, its sessions'
// states are unknown (the caller ends what ran on its connection and tells the apps).
func (h *Hub) offlineLocal(m *Machine, why string) {
	m.mu.Lock()
	if m.connected { // seen until now
		m.lastSeen = time.Now().UTC()
	}
	mux := m.streams
	m.streams, m.connected = nil, false
	m.online, m.err = false, why
	m.endWarmLocked()
	if m.snap != nil {
		s := *m.snap
		s.Online, s.Error, s.Transport, s.TailcatPath, s.Link = false, m.err, "", "", nil
		m.snap = &s
	}
	markUnknownLocked(m.sessions)
	m.mu.Unlock()
	if mux != nil {
		mux.CloseAll()
	}
}

// machineConnected is the hub's part of a node's new connection (devices.connected): what it adds to the node's
// configuration and the calls right after it, and what it starts for the computer.
func (h *Hub) machineConnected(ctx context.Context, m *Machine, at time.Time) devicewire.ConnectedReply {
	m.mu.Lock()
	m.hookedAt = at
	m.mu.Unlock()
	out := devicewire.ConnectedReply{Configure: map[string]json.RawMessage{}}
	if b, err := json.Marshal(m.codexConfig(ctx)); err == nil {
		out.Configure["codex"] = b
	}
	if list := m.echoesToSend(ctx); len(list) > 0 {
		if b, err := json.Marshal(map[string]any{"echoes": list}); err == nil {
			out.Calls = append(out.Calls, devicewire.NodeCall{MachineID: m.cfg.ID, Method: "session.echoes", Params: b})
		}
	}
	return out
}

// updateNodes has each manager that carries machines redeploy its own whose node is not this build (selfupdate.go):
// the carrier always, the others connected now.
func (h *Hub) updateNodes(ctx context.Context) {
	owners := []string{devicesOwner}
	for _, mc := range h.knownEntries() {
		if o := h.machineOwner(mc.ID); !slices.Contains(owners, o) && h.busConnOf(o) != nil {
			owners = append(owners, o)
		}
	}
	for _, o := range owners {
		if err := h.callBus(ctx, o, devicewire.MethodUpdateNodes, map[string]any{}, nil); err != nil {
			h.logf("update: [%s] redeploys its machines: %v", o, err)
		}
	}
}
