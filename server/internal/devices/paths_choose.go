package devices

import (
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
)

// pathState is what is known of a path now: whether it can be tried, its round trip (ms, 0 unknown).
type pathState struct {
	avail     bool
	rtt       int
	failUntil time.Time
	err       string
}

func (d *Devices) pathState(p *nodePath) pathState {
	f := &d.fleet
	f.mu.Lock()
	var hl pathHealth
	if x := f.health[p.key]; x != nil {
		hl = *x
	}
	f.mu.Unlock()
	st := pathState{failUntil: hl.failUntil, err: hl.err, rtt: hl.rtt}
	switch p.via {
	case viaHub:
		var hub devicewire.MeshHub
		if mesh := d.config().Mesh; mesh != nil {
			hub = mesh.Hubs[p.hub]
		}
		st.avail = hub.Reachable && p.rec.Avail
		if !hub.Reachable {
			st.err = "「" + d.hubNameOf(p.hub) + "」现在连不上"
		}
		if hub.Reachable && hub.RTTMS >= 0 && p.rec.RTTMS > 0 {
			st.rtt = hub.RTTMS + p.rec.RTTMS
		}
	default:
		avail, rtt := p.src.reachableHere()
		st.avail = avail
		if rtt > 0 {
			st.rtt = rtt
		}
		if !avail {
			if why := p.src.unavailableWhy(); why != "" {
				st.err = why // what keeps it from being tried says more than how the last try ended
			}
		}
	}
	return st
}

// pathBefore orders two available paths: class, then a known round trip before an unknown one, then the smaller.
func pathBefore(a *nodePath, as pathState, b *nodePath, bs pathState) bool {
	if a.class() != b.class() {
		return a.class() < b.class()
	}
	if (as.rtt > 0) != (bs.rtt > 0) {
		return as.rtt > 0
	}
	return as.rtt < bs.rtt
}

// clearlyBetter: q is worth leaving p for — a better kind of path, or the same kind with a clearly smaller round trip.
func clearlyBetter(q *nodePath, qs pathState, p *nodePath, ps pathState) bool {
	if q.class() != p.class() {
		return q.class() < p.class()
	}
	return qs.rtt > 0 && ps.rtt > 0 && qs.rtt*10 < ps.rtt*7 && ps.rtt-qs.rtt >= 5
}

// choosePath is the path a machine should connect through now; nil and how long to wait when none can be tried.
func (d *Devices) choosePath(m *Machine) (*nodePath, time.Duration) {
	var best *nodePath
	var bestSt pathState
	wait := time.Duration(0)
	now := time.Now()
	for _, p := range d.pathsOf(m) {
		st := d.pathState(p)
		if !st.avail {
			continue
		}
		if now.Before(st.failUntil) {
			if d := st.failUntil.Sub(now); wait == 0 || d < wait {
				wait = d
			}
			continue
		}
		if best == nil || pathBefore(p, st, best, bestSt) {
			best, bestSt = p, st
		}
	}
	return best, wait
}

// betterPath is a path clearly better than the one in use that has stayed so for mesh.path_switch_seconds (since
// tracks when each became better); nil for none.
func (d *Devices) betterPath(m *Machine, cur *nodePath, since map[string]time.Time) *nodePath {
	curSt := d.pathState(cur)
	now := time.Now()
	var pick *nodePath
	var pickSt pathState
	seen := map[string]bool{}
	for _, p := range d.pathsOf(m) {
		if p.key == cur.key {
			continue
		}
		st := d.pathState(p)
		if !st.avail || now.Before(st.failUntil) || !clearlyBetter(p, st, cur, curSt) {
			continue
		}
		seen[p.key] = true
		if since[p.key].IsZero() {
			since[p.key] = now
		}
		if now.Sub(since[p.key]) < d.pathSwitch() {
			continue
		}
		if pick == nil || pathBefore(p, st, pick, pickSt) {
			pick, pickSt = p, st
		}
	}
	for k := range since {
		if !seen[k] {
			delete(since, k)
		}
	}
	return pick
}

// pathEnded records how a connection through p went: a path that held for a while is fine again at once; one that
// failed waits, longer each time in a row (2 s doubling to a minute).
func (d *Devices) pathEnded(p *nodePath, online bool, err error, held time.Duration, planned bool) {
	f := &d.fleet
	f.mu.Lock()
	f.init()
	hl := f.health[p.key]
	if hl == nil {
		hl = &pathHealth{}
		f.health[p.key] = hl
	}
	switch {
	case planned:
		hl.fails, hl.failUntil, hl.err = 0, time.Time{}, ""
	case online && held > time.Minute:
		hl.fails = 0
		hl.failUntil = time.Now().Add(2 * time.Second)
	default:
		hl.fails = min(hl.fails+1, 6)
		hl.failUntil = time.Now().Add(min(time.Second<<hl.fails, time.Minute))
	}
	if err != nil && !planned {
		hl.err = err.Error()
	}
	f.mu.Unlock()
}

// pathConnected records that p reached the node.
func (d *Devices) pathConnected(p *nodePath) {
	f := &d.fleet
	f.mu.Lock()
	f.init()
	hl := f.health[p.key]
	if hl == nil {
		hl = &pathHealth{}
		f.health[p.key] = hl
	}
	hl.okAt, hl.err = time.Now(), ""
	f.mu.Unlock()
}

// pathRTT records the node's round trip through p (the path in use, measureNodes).
func (d *Devices) pathRTT(p *nodePath, rtt int) {
	f := &d.fleet
	f.mu.Lock()
	f.init()
	hl := f.health[p.key]
	if hl == nil {
		hl = &pathHealth{}
		f.health[p.key] = hl
	}
	if hl.rtt == 0 || abs(rtt-hl.rtt) > max(2, hl.rtt/5) { // noise does not count as a change
		hl.rtt = rtt
	}
	f.mu.Unlock()
}

// pathSwitch is how long a better path must stay better before the switch (mesh.path_switch_seconds).
func (d *Devices) pathSwitch() time.Duration {
	if mesh := d.config().Mesh; mesh != nil && mesh.PathSwitchMS > 0 {
		return time.Duration(mesh.PathSwitchMS) * time.Millisecond
	}
	return 30 * time.Second
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
