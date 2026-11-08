package hub

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

// What each manager last reported of its machines (docs/assistant/managers.md "What the hub keeps of each manager"): the hub
// keeps, in its own settings, every manager's own entries as its last devices.state had them: each entry, the
// computer as its node last described itself, and when it was last seen. A hub that restarts shows every manager's
// machines as last reported until their manager reports them (for the minutes after a restart the warm hand-over
// shows more: devicesWarm), and a manager that stays away, its machines so: offline, with what they are and when they
// were last seen. A report counts only for the machines the hub hands its manager (hub.json's, by owner:
// ownership.go); any other id it names is refused and logged once. A move hands the entry to the new manager.
//
// hub.json is still the list the hub hands each manager; once the managers keep their own (manager.json), this is
// what the hub knows of them.

// machineEntriesKey is the hub's own (hub_settings).
const machineEntriesKey = "hub.machine_entries"

const (
	// machineEntriesDelay gathers a burst of reports into one write.
	machineEntriesDelay = time.Second
	// machineEntriesSeenEvery: while reports come, when the machines were last seen is written at least this often;
	// every change of an entry is written within machineEntriesDelay, and a machine connected as a write goes is seen
	// then (the hub's stop writes last). A hub that crashed shows them last seen as of the last write.
	machineEntriesSeenEvery = 10 * time.Minute
)

// reportedMachines is what is kept: by manager, by machine.
type reportedMachines map[string]map[string]reportedEntry

// reportedEntry is one of a manager's machines as it last reported it.
type reportedEntry struct {
	// Cfg is the entry as the manager has it (a manager from before sends none: the one the hub handed it).
	Cfg json.RawMessage `json:"cfg"`
	// Info is the computer as its node last described itself (none: not seen since the manager had it).
	Info     proto.NodeInfo `json:"info,omitzero"`
	LastSeen time.Time      `json:"last_seen,omitzero"`
	// Tailcat: the entry has a tailcat address (left out of Cfg).
	Tailcat bool `json:"tailcat,omitempty"`
}

// loadMachineEntries reads what the managers last reported (startDevices). Unreadable: nothing until they report, and
// it is read again before the next write, which would otherwise drop the managers that did not report since.
func (h *Hub) loadMachineEntries(ctx context.Context) {
	if h.store == nil {
		return
	}
	kept, err := h.readMachineEntries(ctx)
	if err != nil {
		h.logf("machines: what the managers last reported: %v", err)
		return
	}
	d := &h.devices
	d.mu.Lock()
	d.mergeReportedLocked(kept)
	d.reportedRead = true
	d.mu.Unlock()
}

// readMachineEntries is what the setting holds, without the machines the hub hands no manager now and the managers the
// roster no longer has (dropped while the hub was stopped). An error is the database's: a value that does not read is
// logged and taken as none (a later build's, say), so it does not hold up every write after it.
func (h *Hub) readMachineEntries(ctx context.Context) (reportedMachines, error) {
	var raw json.RawMessage
	if _, err := h.store.Setting(ctx, machineEntriesKey, &raw); err != nil {
		return nil, err
	}
	var kept reportedMachines
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &kept); err != nil {
			h.logf("machines: what the managers last reported does not read, starting afresh: %v", err)
			kept = nil
		}
	}
	handed := map[string]bool{}
	for _, mc := range h.knownEntries() {
		handed[mc.ID] = true
	}
	roster := h.managerStore() != nil // without one (tests) the roster is not known: none is dropped
	for owner, list := range kept {
		if has, sure := h.rosterHas(ctx, owner); roster && sure && !has {
			delete(kept, owner)
			continue
		}
		for mid := range list {
			if !handed[mid] {
				delete(list, mid)
			}
		}
	}
	return kept, nil
}

// mergeReportedLocked takes from kept what the hub has not heard since: the managers that did not report since (their
// own report is all they have), machine by machine (a move or a removal may have handed one of them an entry
// meanwhile), but the managers and machines removed since; d.mu is held.
func (d *devicesState) mergeReportedLocked(kept reportedMachines) {
	if d.reported == nil {
		d.reported = reportedMachines{}
	}
	for id, list := range kept {
		if d.reportedSince[id] || d.reportedForgot[id] {
			continue
		}
		for mid, e := range list {
			if d.entriesForgot[mid] {
				continue
			}
			if d.reported[id] == nil {
				d.reported[id] = map[string]reportedEntry{}
			}
			if _, has := d.reported[id][mid]; !has {
				d.reported[id][mid] = e
			}
		}
	}
}

// reportedEntryOf is machine mid as manager owner last reported it.
func (h *Hub) reportedEntryOf(owner, mid string) (reportedEntry, bool) {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.reported[owner][mid]
	return e, ok
}

// takeMachineEntries keeps what manager owner reports of its own machines (applyDeviceStatesFrom): every entry it
// has, from the report's entries and, for one it connects, its state; an id the hub does not hand it is refused and
// logged the first time it names it.
func (h *Hub) takeMachineEntries(owner string, st devicewire.States) {
	states := map[string]devicewire.State{}
	cfgs := map[string]json.RawMessage{}
	var ids []string
	add := func(id string) {
		if id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, s := range st.Machines {
		if id := s.Machine.ID; id != "" && !s.Derived { // a mesh member's machine is no entry of its own
			if _, ok := states[id]; !ok {
				states[id] = s
			}
			add(id)
		}
	}
	for _, e := range st.Entries {
		if _, ok := cfgs[e.ID]; !ok && e.ID != "" {
			cfgs[e.ID] = e.Cfg
		}
		add(e.ID)
	}
	handed := map[string]MachineConfig{}
	for _, mc := range h.knownEntries() { // hub.json's and those their managers keep (machines_kept.go)
		handed[mc.ID] = mc
	}
	next := map[string]reportedEntry{}
	var refused []string
	for _, id := range ids {
		mc, ok := handed[id]
		if !ok || h.entryOwner(id) != owner {
			refused = append(refused, id)
			continue
		}
		src := cfgs[id]
		if keptEntry(src, id) == nil {
			src = states[id].Config
		}
		if keptEntry(src, id) == nil {
			src, _ = json.Marshal(mc)
		}
		e := reportedEntry{Cfg: keptEntry(src, id), Tailcat: hasTailcat(src)}
		if s, ok := states[id]; ok {
			e.Info, e.LastSeen = s.Info, s.Machine.LastSeen
		}
		next[id] = e
	}

	d := &h.devices
	d.mu.Lock()
	if d.reportedForgot[owner] { // removed from the roster: a report it sent before it was hung up
		d.mu.Unlock()
		return
	}
	prev := d.reported[owner]
	changed := prev == nil || len(prev) != len(next)
	for id, e := range next {
		p, had := prev[id]
		if e.Info.Hostname == "" && e.Info.Version == "" { // not described now (offline, absorbed): as last seen
			e.Info = p.Info
		}
		if p.LastSeen.After(e.LastSeen) {
			e.LastSeen = p.LastSeen
		}
		if !had || !sameReported(p, e) {
			changed = true
		}
		next[id] = e
	}
	if d.reported == nil {
		d.reported = reportedMachines{}
	}
	for mid, k := range d.kept { // a kept machine its report lacks (its list does not read): as last reported
		if k == owner {
			if _, has := next[mid]; !has {
				if p, had := prev[mid]; had {
					next[mid] = p
				}
			}
		}
	}
	d.reported[owner] = next
	if d.reportedSince == nil {
		d.reportedSince = map[string]bool{}
	}
	d.reportedSince[owner] = true
	due := changed || time.Since(d.reportedSaved) > machineEntriesSeenEvery
	was := d.refused[owner]
	now := map[string]bool{}
	var fresh []string
	for _, id := range refused {
		now[id] = true
		if !was[id] {
			fresh = append(fresh, id)
		}
	}
	if d.refused == nil {
		d.refused = map[string]map[string]bool{}
	}
	d.refused[owner] = now
	d.mu.Unlock()
	for _, id := range fresh {
		h.logf("machines: [%s] %s's report names it, but it is not that manager's machine now (moved or removed): not kept", id, owner)
	}
	if due {
		h.machineEntriesChanged()
	}
}

// sameReported: a and b differ at most in when the machine was last seen.
func sameReported(a, b reportedEntry) bool {
	a.LastSeen, b.LastSeen = time.Time{}, time.Time{}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// hasTailcat: entry raw has a tailcat address.
func hasTailcat(raw json.RawMessage) bool {
	var e struct {
		TailcatAddr string `json:"tailcat_addr"`
	}
	return json.Unmarshal(raw, &e) == nil && e.TailcatAddr != ""
}

// seedMachineEntry is what the hub shows of a machine it just put to manager o (machines_kept.go), until o reports it.
func (h *Hub) seedMachineEntry(o, mid string, entry json.RawMessage) {
	cfg := keptEntry(entry, mid)
	if cfg == nil {
		return
	}
	d := &h.devices
	d.mu.Lock()
	if d.reported == nil {
		d.reported = reportedMachines{}
	}
	if d.reported[o] == nil {
		d.reported[o] = map[string]reportedEntry{}
	}
	e := d.reported[o][mid]
	e.Cfg, e.Tailcat = cfg, hasTailcat(entry)
	d.reported[o][mid] = e
	d.mu.Unlock()
	h.machineEntriesChanged()
}

// keptEntry is entry id as the hub keeps it, nil when raw is no entry with that id: without its secrets (the tailcat
// address, a proxy's password), which stay with its manager; the rest as the manager has it, keys the hub does not
// know too.
func keptEntry(raw json.RawMessage, id string) json.RawMessage {
	var o map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &o) != nil || o == nil {
		return nil
	}
	var got string
	if json.Unmarshal(o["id"], &got) != nil || got != id {
		return nil
	}
	delete(o, "tailcat_addr")
	if p, ok := o["proxy"]; ok {
		var s string
		if json.Unmarshal(p, &s) != nil {
			delete(o, "proxy")
		} else if strings.Contains(s, "@") {
			if u, err := url.Parse(s); err == nil && u.User != nil {
				u.User = url.User(u.User.Username())
				o["proxy"], _ = json.Marshal(u.String())
			} else {
				delete(o, "proxy")
			}
		}
	}
	b, err := json.Marshal(o)
	if err != nil {
		return nil
	}
	return b
}

// forgetMachineEntry drops machine mid from what the managers reported: its entry was removed.
func (h *Hub) forgetMachineEntry(mid string) {
	d := &h.devices
	d.mu.Lock()
	if d.entriesForgot == nil {
		d.entriesForgot = map[string]bool{}
	}
	d.entriesForgot[mid] = true
	for _, list := range d.reported {
		delete(list, mid)
	}
	d.mu.Unlock()
	h.machineEntriesChanged()
}

// handMachineEntry moves what manager from last reported of machine mid to manager to, which carries it now: shown
// as last reported until to reports it, a restart meanwhile included.
func (h *Hub) handMachineEntry(mid, from, to string) {
	d := &h.devices
	d.mu.Lock()
	e, ok := d.reported[from][mid]
	if ok {
		delete(d.reported[from], mid)
		if d.reported[to] == nil {
			d.reported[to] = map[string]reportedEntry{}
		}
		if _, has := d.reported[to][mid]; !has {
			d.reported[to][mid] = e
		}
	}
	d.mu.Unlock()
	if ok {
		h.machineEntriesChanged()
	}
}

// forgetMachineEntries drops what manager id reported: it was removed from the roster. Its machines, back with the
// carrier (releaseMachines), keep what it reported of them until the carrier reports them.
func (h *Hub) forgetMachineEntries(id string) {
	d := &h.devices
	d.mu.Lock()
	var mids []string
	for mid := range d.reported[id] {
		mids = append(mids, mid)
	}
	d.mu.Unlock()
	owners := map[string]string{}
	for _, mid := range mids {
		owners[mid] = h.entryOwner(mid)
	}
	d.mu.Lock()
	for mid, e := range d.reported[id] {
		o := owners[mid]
		if o == "" || o == id {
			continue
		}
		if d.reported[o] == nil {
			d.reported[o] = map[string]reportedEntry{}
		}
		if _, has := d.reported[o][mid]; !has {
			d.reported[o][mid] = e
		}
	}
	if d.reportedForgot == nil {
		d.reportedForgot = map[string]bool{}
	}
	d.reportedForgot[id] = true
	delete(d.reported, id)
	delete(d.refused, id)
	delete(d.recEntries, id)
	d.mu.Unlock()
	h.machineEntriesChanged()
}

// machineEntriesChanged writes what the managers reported a moment from now: one write for a burst of reports.
func (h *Hub) machineEntriesChanged() {
	if h.store == nil {
		return
	}
	d := &h.devices
	d.mu.Lock()
	if d.reportedDue {
		d.mu.Unlock()
		return
	}
	d.reportedDue = true
	d.mu.Unlock()
	time.AfterFunc(machineEntriesDelay, func() {
		d.reportedSave.Lock() // due until it writes: a report meanwhile is in this write
		defer d.reportedSave.Unlock()
		d.mu.Lock()
		d.reportedDue = false
		d.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		h.writeMachineEntries(ctx)
	})
}

// saveMachineEntries writes what the managers reported, each machine last seen as the hub saw it last (the session
// lists it got since a report count too). One write at a time, each with what is there as it starts. While the
// setting has not been read (that failed at start) it is read first, so the managers that did not report since stay;
// still unreadable, nothing is written.
func (h *Hub) saveMachineEntries(ctx context.Context) {
	if h.store == nil {
		return
	}
	d := &h.devices
	d.reportedSave.Lock()
	defer d.reportedSave.Unlock()
	h.writeMachineEntries(ctx)
}

// writeMachineEntries is saveMachineEntries with d.reportedSave held.
func (h *Hub) writeMachineEntries(ctx context.Context) {
	d := &h.devices
	d.mu.Lock()
	read := d.reportedRead
	d.mu.Unlock()
	if !read {
		kept, err := h.readMachineEntries(ctx)
		if err != nil {
			h.logf("machines: what the managers last reported, not written: %v", err)
			d.mu.Lock()
			d.reportedSaved = time.Now() // tried again with the next change, not with every report
			d.mu.Unlock()
			return
		}
		d.mu.Lock()
		d.mergeReportedLocked(kept)
		d.reportedRead = true
		d.mu.Unlock()
	}
	type seenBy struct {
		owner string
		at    time.Time
	}
	seen := map[string]seenBy{}
	now := time.Now().UTC()
	for _, m := range h.machinesNow() {
		owner := m.ownerOrCarrier()
		m.mu.Lock()
		at := m.lastSeen
		if m.connected { // its node is connected: seen now (a hub stopping writes this last)
			at = now
		}
		if m.managed && !m.derived && !at.IsZero() {
			seen[m.cfg.ID] = seenBy{owner, at}
		}
		m.mu.Unlock()
	}
	d.mu.Lock()
	out := make(reportedMachines, len(d.reported))
	for owner, list := range d.reported {
		kept := make(map[string]reportedEntry, len(list))
		for mid, e := range list {
			if s, ok := seen[mid]; ok && s.owner == owner && s.at.After(e.LastSeen) {
				e.LastSeen = s.at
				list[mid] = e
			}
			kept[mid] = e
		}
		out[owner] = kept
	}
	d.mu.Unlock()
	err := h.store.PutSetting(ctx, machineEntriesKey, out)
	if err != nil {
		// Tried again with the next change, or machineEntriesSeenEvery on: not with every report meanwhile.
		h.logf("machines: what the managers last reported, not written: %v", err)
	}
	d.mu.Lock()
	d.reportedSaved = time.Now()
	d.mu.Unlock()
}
