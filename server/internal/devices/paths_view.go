package devices

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// pathInfos is a machine's paths for the admin page and the machine list.
func (d *Devices) pathInfos(m *Machine) []pathInfo {
	active := m.activePathKey()
	self := d.config().HubID
	var out []pathInfo
	for _, p := range d.pathsOf(m) {
		st := d.pathState(p)
		pi := pathInfo{Via: p.via, HubID: self, OK: st.avail && time.Now().After(st.failUntil), RTTMS: st.rtt,
			Active: p.key == active, Error: st.err}
		if p.via == viaHub {
			pi.HubID, pi.ID = p.hub, p.mid
		} else {
			pi.ID = p.src.cfg.ID
			if pi.Active {
				if t := m.transportNow(); t != "" {
					pi.Via = t // ssh that fell back to tailcat
				}
			}
		}
		if pi.Active {
			pi.OK, pi.Error = true, ""
		}
		out = append(out, pi)
	}
	return out
}

// measureNodes measures the round trip to every computer this hub drives, over the path in use.
func (d *Devices) measureNodes(ctx context.Context) {
	for _, m := range d.machinesNow() {
		p := m.activePath()
		if p == nil || !m.isOnline() {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		start := time.Now()
		err := m.call(cctx, "ping", map[string]any{}, nil)
		cancel()
		if err == nil {
			d.pathRTT(p, max(1, int(time.Since(start).Milliseconds())))
		}
	}
}

// decorate fills in what the apps see of how a machine is reached: the hub it is reached through, its own id, the
// route, its paths.
func (d *Devices) decorate(m *Machine, s proto.Machine) proto.Machine {
	cfg := d.config()
	s.HubID, s.HubName = cfg.HubID, cfg.HubName
	f := &d.fleet
	f.mu.Lock()
	for _, p := range f.paths[m] {
		if uid := f.uids[p.key]; uid != "" {
			s.MachineUID = uid
			break
		}
	}
	f.mu.Unlock()
	if uid := m.nodeUID(); uid != "" {
		s.MachineUID = uid
	}
	infos := d.pathInfos(m)
	route := &proto.MachineRoute{Via: kindOf(m.cfg)}
	if m.derived {
		route.Via = viaHub
	}
	for i, pi := range infos {
		if pi.Active || i == 0 && !s.Online {
			route = &proto.MachineRoute{Via: pi.Via}
			if pi.Via == viaHub {
				route.Hops = 1
				s.HubID, s.HubName = pi.HubID, d.hubNameOf(pi.HubID)
			}
			if s.Online && pi.RTTMS > 0 {
				rtt := pi.RTTMS
				route.RTTMS = &rtt
			}
			if pi.Active {
				break
			}
		}
	}
	s.Route = route
	if len(infos) > 1 || m.derived {
		for _, pi := range infos {
			s.Paths = append(s.Paths, proto.MachinePath{Via: pi.Via, HubID: pi.HubID, OK: pi.OK, RTTMS: pi.RTTMS, Active: pi.Active})
		}
	}
	return s
}

// measureLoop measures the round trip to every computer now and then (the choice between paths uses it).
func (d *Devices) measureLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		d.measureNodes(ctx)
	}
}
