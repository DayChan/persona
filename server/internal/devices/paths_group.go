package devices

import (
	"cmp"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
)

// loadPathUIDs reads which computer each path reached last time, and the ids given to members' computers.
func (d *Devices) loadPathUIDs(ctx context.Context) {
	f := &d.fleet
	f.mu.Lock()
	f.init()
	f.mu.Unlock()
	if d.opts.Store == nil {
		return
	}
	var uids, ids map[string]string
	if _, err := d.opts.Store.Setting(ctx, "mesh.path_uids", &uids); err != nil {
		d.logf("mesh: load path uids: %v", err)
	}
	if _, err := d.opts.Store.Setting(ctx, "mesh.machine_ids", &ids); err != nil {
		d.logf("mesh: load machine ids: %v", err)
	}
	f.mu.Lock()
	for k, v := range uids {
		f.uids[k] = v
	}
	for k, v := range ids {
		f.ids[k] = v
	}
	f.mu.Unlock()
}

// learnUID keeps the computer a path reached; when it is news, the machines are grouped again.
func (d *Devices) learnUID(p *nodePath, uid string) {
	if uid == "" || p == nil {
		return
	}
	f := &d.fleet
	f.mu.Lock()
	f.init()
	if f.uids[p.key] == uid {
		f.mu.Unlock()
		return
	}
	f.uids[p.key] = uid
	f.mu.Unlock()
	if d.opts.Store != nil {
		go d.savePathUIDs()
	}
	d.fleetRegroup()
	// A path joined to a computer can make it a work computer (machineTrust): its summary is taken back.
	d.spreadAttention()
}

// savePathUIDs writes which computer each path reached as it is now. The copy is taken after the previous write,
// under the lock: a write started earlier never lands after a newer one, so a restart does not read a map that
// misses a path learnt last (and list that path's computer twice).
func (d *Devices) savePathUIDs() {
	f := &d.fleet
	f.saveUIDs.Lock()
	defer f.saveUIDs.Unlock()
	f.mu.Lock()
	uids := maps.Clone(f.uids)
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.opts.Store.PutSetting(ctx, "mesh.path_uids", uids); err != nil {
		d.logf("mesh: save path uids: %v", err)
	}
}

// memberRecords are the members' computers the center reaches through them (the hub's mesh, devicewire.Mesh).
func (d *Devices) memberRecords() []devicewire.MemberRecord {
	mesh := d.config().Mesh
	if mesh == nil || mesh.Member {
		return nil
	}
	out := slices.Clone(mesh.Members)
	sort.Slice(out, func(i, j int) bool { return out[i].HubID < out[j].HubID })
	return out
}

// fleetRegroup works out the machines and their paths again: hub.json's entries, and (on the center) the members'
// computers, grouped by the computer each path reached.
func (d *Devices) fleetRegroup() {
	members := d.memberRecords()
	cfgs := d.cfgMachines()
	f := &d.fleet
	f.mu.Lock()
	f.init()

	var all []*nodePath
	for _, m := range cfgs {
		all = append(all, cfgPath(m))
	}
	for _, r := range members {
		for _, rm := range r.Machines {
			all = append(all, &nodePath{key: "hub:" + r.HubID + "/" + rm.ID, via: viaHub, hub: r.HubID, mid: rm.ID, rec: rm})
		}
	}
	// Group by computer: one group per uid, a path whose computer is not known yet alone.
	type group struct {
		key   string
		paths []*nodePath
	}
	var groups []*group
	byKey := map[string]*group{}
	for _, p := range all {
		k := "p:" + p.key
		if uid := f.uids[p.key]; uid != "" {
			k = "u:" + uid
		}
		g := byKey[k]
		if g == nil {
			g = &group{key: k}
			byKey[k] = g
			groups = append(groups, g)
		}
		g.paths = append(g.paths, p)
	}

	newPaths := map[*Machine][]*nodePath{}
	newAbsorbed := map[*Machine]*Machine{}
	newDerived := map[string]*Machine{}
	taken := map[string]bool{}
	for _, m := range cfgs {
		taken[m.cfg.ID] = true
	}
	var listed []*Machine
	var pendingDerived []*group
	for _, g := range groups {
		var owner *Machine
		for _, p := range g.paths {
			if p.src == nil {
				continue
			}
			if owner == nil {
				owner = p.src
			} else {
				newAbsorbed[p.src] = owner
			}
		}
		if owner == nil {
			pendingDerived = append(pendingDerived, g)
			continue
		}
		newPaths[owner] = orderPaths(g.paths)
		listed = append(listed, owner)
	}
	// The members' computers: the machine made earlier for any of the group's paths is kept (its id with it).
	prevOf := map[string]*Machine{}
	for _, m := range f.derived {
		for _, p := range f.paths[m] {
			prevOf[p.key] = m
		}
	}
	for _, m := range f.derived {
		taken[m.cfg.ID] = true // ids in use stay with their machines
	}
	used := map[*Machine]bool{}
	var derivedList []*Machine
	renamed := false
	for _, g := range pendingDerived {
		var m *Machine
		for _, p := range g.paths {
			if prev := prevOf[p.key]; prev != nil && !used[prev] {
				m = prev
				break
			}
		}
		ps := orderPaths(g.paths)
		if m == nil {
			id := f.ids[g.key]
			if id == "" || taken[id] && !derivedHas(f.derived, id) {
				id = derivedID(ps[0], taken, d.hubNameOf(ps[0].hub))
			}
			taken[id] = true
			var cfg MachineConfig
			_ = json.Unmarshal(ps[0].rec.Cfg, &cfg)
			cfg.ID, cfg.Kind = id, kindHub
			if cfg.Name == "" {
				cfg.Name = ps[0].mid
			}
			m = newMachine(d, cfg)
			m.raw = ps[0].rec.Cfg
			m.derived = true
		} else if m.setName(derivedName(ps[0])) {
			renamed = true // the member renamed it: the same machine, connected as it is
		}
		used[m] = true
		if f.ids[g.key] != m.cfg.ID {
			f.ids[g.key] = m.cfg.ID
		}
		newDerived[g.key] = m
		newPaths[m] = ps
		derivedList = append(derivedList, m)
	}
	sort.Slice(derivedList, func(i, j int) bool { return derivedList[i].cfg.ID < derivedList[j].cfg.ID })

	// What changed: machines gone, machines new, paths moved.
	var gone, started []*Machine
	for _, m := range f.derived {
		if !used[m] {
			gone = append(gone, m)
		}
	}
	for _, m := range newDerived {
		if f.cancel[m] == nil {
			started = append(started, m)
		}
	}
	changed := len(gone) > 0 || len(started) > 0 || len(newAbsorbed) != len(f.absorbed) || !samePaths(f.paths, newPaths)
	for m, to := range newAbsorbed {
		if f.absorbed[m] != to {
			changed = true
		}
	}
	f.derived, f.absorbed, f.paths = newDerived, newAbsorbed, newPaths
	f.order = append(listed, derivedList...)
	d.mu.Lock()
	ctx := d.ctx
	d.mu.Unlock()
	cancels := map[*Machine]context.CancelFunc{}
	for _, m := range gone {
		cancels[m] = f.cancel[m]
		delete(f.cancel, m)
	}
	for _, m := range started {
		if ctx == nil {
			continue
		}
		mctx, cancel := context.WithCancel(ctx)
		f.cancel[m] = cancel
		m.ctx = mctx // set before its run loop starts: onEvent reads it
		d.workers.Add(1)
		go func() {
			defer d.workers.Done()
			m.run(mctx)
		}()
	}
	ids := make(map[string]string, len(f.ids))
	for k, v := range f.ids {
		if newDerived[k] != nil {
			ids[k] = v
		}
	}
	f.ids = ids
	var wake []*Machine
	if changed {
		close(f.changed)
		f.changed = make(chan struct{})
		for m := range newPaths {
			wake = append(wake, m)
		}
		wake = append(wake, cfgs...)
	}
	f.mu.Unlock()

	for m, cancel := range cancels {
		if cancel != nil {
			cancel()
		}
		d.logf("[%s] no longer reachable through any hub of the group: removed", m.cfg.ID)
		d.forgetMachine(m.cfg.ID)
	}
	for _, m := range started {
		d.logf("[%s] a member's computer: reached through %s", m.cfg.ID, d.hubNameOf(d.pathsOf(m)[0].hub))
	}
	if !changed {
		if renamed {
			d.changed()
		}
		return
	}
	for _, m := range wake {
		m.wake()
	}
	if d.opts.Store != nil {
		go d.saveMachineIDs()
	}
	d.changed()
}

// saveMachineIDs writes the ids given to members' computers as they are now. The copy is taken after the previous
// write, under the lock: a write started earlier never lands after a newer one, and the map encoded is not the one
// the next regroup writes to.
func (d *Devices) saveMachineIDs() {
	f := &d.fleet
	f.saveIDs.Lock()
	defer f.saveIDs.Unlock()
	f.mu.Lock()
	ids := maps.Clone(f.ids)
	f.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = d.opts.Store.PutSetting(ctx, "mesh.machine_ids", ids)
}

func derivedHas(d map[string]*Machine, id string) bool {
	for _, m := range d {
		if m.cfg.ID == id {
			return true
		}
	}
	return false
}

// derivedName is what the member calls the computer of p: its hub.json name, else its id there (as a new derived
// machine is named).
func derivedName(p *nodePath) string {
	var cfg struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(p.rec.Cfg, &cfg)
	return cmp.Or(cfg.Name, p.mid)
}

// derivedID is the id a member's computer gets: its id in that member's hub.json, unless the center (or another
// member) has it already; then @<the member's name>, then the member's hub_id too, then a number.
func derivedID(p *nodePath, taken map[string]bool, hubName string) string {
	base := p.mid
	if !taken[base] {
		return base
	}
	suffix := hubSlug(hubName, p.hub)
	id := base + "@" + suffix
	if taken[id] {
		id = base + "@" + suffix + "-" + shortID(p.hub)
	}
	for n := 2; taken[id]; n++ {
		id = base + "@" + suffix + "-" + shortID(p.hub) + "-" + itoa(n)
	}
	return id
}

// orderPaths: hub.json entries in their order, then members' by hub_id and id.
func orderPaths(ps []*nodePath) []*nodePath {
	out := append([]*nodePath(nil), ps...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.src != nil) != (b.src != nil) {
			return a.src != nil
		}
		if a.src != nil {
			return false // hub.json order
		}
		if a.hub != b.hub {
			return a.hub < b.hub
		}
		return a.mid < b.mid
	})
	return out
}

func samePaths(a, b map[*Machine][]*nodePath) bool {
	if len(a) != len(b) {
		return false
	}
	for m, pa := range a {
		pb, ok := b[m]
		if !ok || len(pa) != len(pb) {
			return false
		}
		for i := range pa {
			if pa[i].key != pb[i].key || pa[i].rec.Avail != pb[i].rec.Avail {
				return false
			}
		}
	}
	return true
}

// wakeOwnerOf wakes the machine a hub.json entry's path belongs to (the entry itself, or the one that has its computer).
func (d *Devices) wakeOwnerOf(m *Machine) {
	f := &d.fleet
	f.mu.Lock()
	to := f.absorbed[m]
	f.mu.Unlock()
	if to != nil {
		to.wake()
	}
}

// parkReason says why a machine's run loop does not connect now ("" when it should): a member leaves its computers to
// the center; an entry whose computer another entry has is reached through that one.
func (d *Devices) parkReason(m *Machine) string {
	if mesh := d.config().Mesh; mesh != nil && mesh.Member {
		return "由中心 hub「" + mesh.CenterName + "」连着"
	}
	f := &d.fleet
	f.mu.Lock()
	defer f.mu.Unlock()
	if to := f.absorbed[m]; to != nil {
		return "和「" + to.name() + "」是同一台电脑"
	}
	if m.derived {
		if _, ok := f.paths[m]; !ok {
			return "removed"
		}
	}
	return ""
}

// hubSlug is a hub's name made fit for a machine id suffix: lowercase letters, digits and dashes; a name with none
// of those (say, a Chinese one) gives the start of the hub_id.
func hubSlug(name, hubID string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		return shortID(hubID)
	}
	return s
}

func shortID(hubID string) string {
	if len(hubID) > 6 {
		return hubID[:6]
	}
	return hubID
}

func itoa(n int) string { return strconv.Itoa(n) }
