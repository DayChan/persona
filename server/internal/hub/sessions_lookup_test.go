//go:build !windows

package hub

import "testing"

// Looking up one session, or scanning for the busy ones, reads the kept list instead of a stamped copy of it. On 8,000
// sessions: decoratedSession allocates nothing (2,949,136 B before, the copy of its machine's 2,000), and updateBusy
// 664,956 B, the busy items it returns (12.3 MB before).
func TestSessionLookupsCopyNoList(t *testing.T) {
	if !isolatedSessionAllocation(t) {
		return
	}
	h := syntheticAgentHub(t)
	m := h.byID["demo-3"]
	if s, ok := h.decoratedSession(m, "session-1999"); !ok || s.Manager != h.machineOwner(m.cfg.ID) || m.sessions[1999].Manager != "" {
		t.Fatalf("decoratedSession = %q %v, kept %q: want it stamped, the kept list not", s.Manager, ok, m.sessions[1999].Manager)
	}
	for _, c := range []struct {
		name string
		f    func()
		most int
	}{
		{"decoratedSession", func() { h.decoratedSession(m, "session-1999") }, 1 << 10},
		{"updateBusy", func() { h.updateBusy() }, 684905},
	} {
		if got := allocated(c.f); got > c.most {
			t.Errorf("%s allocated %d bytes, want at most %d", c.name, got, c.most)
		}
	}
}
