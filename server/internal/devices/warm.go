package devices

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Warm start (docs/operations/hub-restart.md "Machines and sessions"): a manager that stops for a restart (the hub's restart stops it too)
// leaves the next one what its machines looked like (Postgres settings "hub.warm_machines", taken once; the key an
// older hub wrote, so the first start after the move uses what that hub left). For a while after it starts, a
// machine that was online shows as it was, its sessions included, while its node connects again: the apps see nothing
// go offline and come back, and a request for it waits for the node instead of failing. The key is the manager's
// own (its settings); the hub keeps what it saw reported in its own (hub/devices_warm.go saveDevicesWarm).

// reconnectGrace: a running hub shows a machine whose node just disconnected as it was this long, while it reconnects
// (a resident node restarting on a new binary takes about 2 s). Each machine copies it (Machine.reconnectGrace), so a
// test shortens it on its own machine instead of swapping a package var under another test's goroutines.
const reconnectGrace = 5 * time.Second

const (
	warmKey   = "hub.warm_machines"
	warmGrace = 30 * time.Second // shown as before at most this long without its node
	warmStale = 2 * time.Minute  // a hand-over older than this is not used (the hub was down, not restarting)
)

type warmMachine struct {
	Info     proto.NodeInfo  `json:"info"`
	Sessions []proto.Session `json:"sessions"`
}

type savedWarm struct {
	Machines map[string]warmMachine `json:"machines"`
	// Engines are every hub.json machine's engine: the hub shows them until the next manager reports.
	Engines map[string]string `json:"engines,omitempty"`
	// EngineHosts are their sets (#2576), next to Engines for a hub that reads only those.
	EngineHosts map[string]proto.EngineChoice `json:"engine_hosts,omitempty"`
	SavedAt     time.Time                     `json:"saved_at"`
}

// saveWarm hands the online machines to the next manager (Run, before their connections close).
func (d *Devices) saveWarm(ctx context.Context) {
	if d.opts.Store == nil {
		return
	}
	saved := savedWarm{Machines: map[string]warmMachine{}, Engines: map[string]string{},
		EngineHosts: map[string]proto.EngineChoice{}, SavedAt: time.Now()}
	for _, m := range d.machinesNow() {
		if !m.derived {
			saved.Engines[m.cfg.ID] = d.engineOf(m.cfg.ID) // before m.mu: it may read settings
			saved.EngineHosts[m.cfg.ID] = d.engineChoiceOf(m.cfg.ID)
		}
		m.mu.Lock()
		if m.online && !m.derived {
			saved.Machines[m.cfg.ID] = warmMachine{Info: m.info, Sessions: append([]proto.Session(nil), m.sessions...)}
		}
		m.mu.Unlock()
	}
	if err := d.opts.Store.PutSetting(ctx, warmKey, saved); err != nil {
		d.logf("warm start: hand over machines: %v", err)
	}
}

// loadWarm takes over what the last manager handed over, for the machines when they are made (applyWarmLocked).
func (d *Devices) loadWarm(ctx context.Context) {
	if d.opts.Store == nil {
		return
	}
	var saved savedWarm
	ok, err := d.opts.Store.TakeSetting(ctx, warmKey, &saved)
	if err != nil {
		d.logf("warm start: %v", err)
		return
	}
	if !ok || time.Since(saved.SavedAt) > warmStale || len(saved.Machines) == 0 {
		return
	}
	saved.SavedAt = time.Now() // from now on: until when the machines show as before
	d.mu.Lock()
	d.warm = &saved
	d.mu.Unlock()
	d.logf("warm start: %d machines shown as before while their nodes reconnect", len(saved.Machines))
}

// applyWarmLocked shows a machine just made as before the restart, if the last manager handed it over (d.mu held).
func (d *Devices) applyWarmLocked(m *Machine) {
	if d.warm == nil {
		return
	}
	w, ok := d.warm.Machines[m.cfg.ID]
	if !ok {
		return
	}
	delete(d.warm.Machines, m.cfg.ID)
	until := d.warm.SavedAt.Add(warmGrace)
	if !time.Now().Before(until) {
		return
	}
	ch := make(chan struct{})
	m.info, m.sessions, m.warmUntil, m.warmed = w.Info, w.Sessions, until, ch
	time.AfterFunc(time.Until(until), func() { m.warmExpired(ch) })
}

// warmLocked: shown as before the restart, its node not back yet; m.mu is held.
func (m *Machine) warmLocked(now time.Time) bool { return !m.online && now.Before(m.warmUntil) }

// endWarmLocked: its node is back (or the time is up); m.mu is held.
func (m *Machine) endWarmLocked() {
	m.warmUntil = time.Time{}
	if m.warmed != nil {
		close(m.warmed)
		m.warmed = nil
	}
}

// warmExpired: the node did not come back in time (ch: the wait that ended); the machine shows offline as it would
// have from the start.
func (m *Machine) warmExpired(ch chan struct{}) {
	m.mu.Lock()
	if m.warmed != ch || m.online {
		m.mu.Unlock()
		return
	}
	m.endWarmLocked()
	m.via, m.path = "", ""
	for i := range m.sessions {
		if m.sessions[i].Status != proto.StatusOffline {
			m.sessions[i].Status = proto.StatusUnknown
		}
	}
	why := m.err
	m.mu.Unlock()
	m.d.logf("[%s] offline: its node did not come back in time", m.cfg.ID)
	if why != "" {
		m.tellLinks(false, why)
	}
	m.d.changed()
}

// waitWarm waits, for a call made while the machine is shown as before the restart, until its node is back (true)
// or that ends without it (false).
func (m *Machine) waitWarm(ctx context.Context) bool {
	m.mu.Lock()
	ch := m.warmed
	warm := m.warmLocked(time.Now())
	m.mu.Unlock()
	if !warm || ch == nil {
		return false
	}
	select {
	case <-ch:
	case <-ctx.Done():
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peer != nil
}
