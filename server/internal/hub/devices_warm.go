package hub

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	// devicesWarmKey is the hub's own (hub_settings): what a stopping hub process saw the managers report, for the
	// next one. The manager hands its own machines to its next process apart (internal/devices warm.go).
	devicesWarmKey   = "hub.devices_warm"
	devicesWarmGrace = 30 * time.Second
	devicesWarmStale = 2 * time.Minute
)

// devicesWarm is the hand-over: the online machines as reported, every machine's engine, when.
type devicesWarm struct {
	Machines map[string]devicesWarmMachine `json:"machines"`
	Engines  map[string]string             `json:"engines,omitempty"`
	// EngineHosts are their sets of hosts (#2576), as the managers reported them.
	EngineHosts map[string]proto.EngineChoice `json:"engine_hosts,omitempty"`
	SavedAt     time.Time                     `json:"saved_at"`
}

type devicesWarmMachine struct {
	Info     proto.NodeInfo  `json:"info"`
	Sessions []proto.Session `json:"sessions"`
	// Until: a machine only still shown from an earlier hand-over keeps that one's deadline (quick restarts in a row
	// do not keep it shown for ever); zero for one online.
	Until time.Time `json:"until,omitzero"`
}

// saveDevicesWarm hands the next hub process the hub.json machines as the managers last reported them (beginStop,
// while they are still connected): the apps see them as they were while the managers come back.
func (h *Hub) saveDevicesWarm(ctx context.Context) {
	if h.store == nil {
		return
	}
	saved := devicesWarm{Machines: map[string]devicesWarmMachine{}, Engines: map[string]string{},
		EngineHosts: map[string]proto.EngineChoice{}, SavedAt: time.Now()}
	for _, m := range h.machinesNow() {
		if h.knownEntry(m.cfg.ID) == nil {
			continue
		}
		m.mu.Lock()
		if m.managed && !m.derived {
			// Online, or still shown from the last hand-over (a second restart within the grace keeps it).
			shown := m.online || m.warmLocked(saved.SavedAt)
			if m.snap != nil && m.snap.Engine != "" {
				saved.Engines[m.cfg.ID] = m.snap.Engine
			} else if m.engine != "" {
				saved.Engines[m.cfg.ID] = m.engine
			}
			if m.snap != nil && m.snap.EngineHosts != nil {
				saved.EngineHosts[m.cfg.ID] = proto.EngineChoice{Hosts: m.snap.EngineHosts, Default: m.snap.EngineDefault}
			} else if m.engineHosts != nil {
				saved.EngineHosts[m.cfg.ID] = *m.engineHosts
			}
			if shown {
				w := devicesWarmMachine{Info: m.info, Sessions: slices.Clone(m.sessions)}
				if !m.online {
					w.Until = m.warmUntil
				}
				saved.Machines[m.cfg.ID] = w
			}
		}
		m.mu.Unlock()
	}
	if err := h.store.PutSetting(ctx, devicesWarmKey, saved); err != nil {
		h.logf("warm start: hand over the machines: %v", err)
	}
}

// devicesWarmEnded: the warm start is over; the machines no manager reported since show offline, whichever manager
// they are (one manager back does not keep the others' shown as before).
func (h *Hub) devicesWarmEnded() {
	changed := false
	for _, m := range h.machinesNow() {
		m.mu.Lock()
		if m.managed && !m.warmUntil.IsZero() && m.snap == nil {
			m.endWarmLocked()
			markUnknownLocked(m.sessions)
			changed = true
		}
		m.mu.Unlock()
	}
	if changed {
		h.broadcastMachines()
		for _, m := range h.machinesNow() {
			h.sessionsChanged(m)
		}
	}
}

// warmLocked: shown as before a restart — the hub's, the manager not back yet, or the manager's (State.WarmUntil),
// its node not back yet; m.mu is held.
func (m *Machine) warmLocked(now time.Time) bool { return !m.online && now.Before(m.warmUntil) }

func (m *Machine) endWarmLocked() { m.warmUntil = time.Time{} }
