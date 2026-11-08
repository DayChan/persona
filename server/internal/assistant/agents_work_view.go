package assistant

import (
	"context"
	"sort"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the hub's machines and sessions, as one pass sees them ----

// workView asks the hub for each thing once in a pass: the machines, a machine's sessions as the apps get them (with
// the hub's work on them) and which of them are muted.
type workView struct {
	s       *Service
	ctx     context.Context
	devices map[string]Device // nil: not asked yet
	lists   map[string][]proto.Session
	muted   map[string]map[string]bool
	failed  map[string]bool // the machines whose sessions the hub did not answer with: as if they were away
}

func (s *Service) newWorkView(ctx context.Context) *workView {
	return &workView{s: s, ctx: ctx, lists: map[string][]proto.Session{}, muted: map[string]map[string]bool{},
		failed: map[string]bool{}}
}

func (v *workView) device(id string) (Device, bool) {
	if v.devices == nil {
		v.devices = map[string]Device{}
		for _, d := range v.s.devices(v.ctx) {
			v.devices[d.ID] = d
		}
	}
	d, ok := v.devices[id]
	return d, ok
}

func (v *workView) machines() []Device {
	v.device("")
	out := make([]Device, 0, len(v.devices))
	for _, d := range v.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// sessions are machine's sessions as the apps get them (sessions.list, with muted: which have pushes off).
func (v *workView) sessions(machine string) []proto.Session {
	if list, ok := v.lists[machine]; ok {
		return list
	}
	var res struct {
		Sessions []proto.Session `json:"sessions"`
		Muted    []string        `json:"muted"`
	}
	if err := v.s.call(v.ctx, machine, DeviceSessionsList, map[string]any{}, &res); err != nil {
		v.s.logf("work: sessions of %s: %v", machine, err)
		v.failed[machine] = true
	}
	v.lists[machine] = res.Sessions
	muted := map[string]bool{}
	for _, id := range res.Muted {
		muted[id] = true
	}
	v.muted[machine] = muted
	return res.Sessions
}

func (v *workView) session(machine, sid string) (proto.Session, bool) {
	for _, c := range v.sessions(machine) {
		if c.SessionID == sid {
			return c, true
		}
	}
	return proto.Session{}, false
}

func (v *workView) isMuted(machine, sid string) bool {
	v.sessions(machine)
	return v.muted[machine][sid]
}
