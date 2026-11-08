package assistant

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- mutes ----

// Pins is the settings mutes: muted spaces / agents get no push notifications (finished, needs you). Spaces are
// "machine/space name", agents "machine/session id"; Allowed are agents that keep theirs although their space is
// muted. The hub keeps it (GET / PUT /api/v1/mutes) and hands it to the manager (fleet.mutes).
type Pins struct {
	Spaces   []string `json:"spaces"`
	Sessions []string `json:"sessions"`
	Allowed  []string `json:"allowed,omitempty"`
}

// setMutes takes the hub's mutes (the hello's answer, fleet.mutes).
func (s *Service) setMutes(p assistantwire.Mutes) {
	m := Pins{Spaces: nonNil(p.Spaces), Sessions: nonNil(p.Sessions), Allowed: p.Allowed}
	s.fleet.mutesMu.Lock()
	s.fleet.mutes = &m
	s.fleet.mutesMu.Unlock()
}

// loadMutes is the mutes the hub handed over last (the hello's answer, fleet.mutes after every change); none before
// it did. They are the hub's settings: the manager never reads them from a database.
func (s *Service) loadMutes(context.Context) Pins {
	f := &s.fleet
	f.mutesMu.Lock()
	defer f.mutesMu.Unlock()
	if f.mutes == nil {
		return Pins{Spaces: []string{}, Sessions: []string{}}
	}
	return *f.mutes
}

// muted reports whether pushes about this session are off (the agent or its space is muted).
func (s *Service) muted(machineID, sessionID string) bool {
	space := ""
	if sess, ok := s.nodeSession(machineID, sessionID); ok {
		space = sess.SpaceName
	}
	return s.mutedAs(machineID, sessionID, space)
}

// mutedAs is muted for a machine under the id the apps know, its session in space (empty: none known).
func (s *Service) mutedAs(machineID, sessionID, space string) bool {
	p := s.loadMutes(context.Background())
	key := machineID + "/" + sessionID
	if slices.Contains(p.Sessions, key) {
		return true
	}
	if slices.Contains(p.Allowed, key) { // turned back on inside a muted space
		return false
	}
	return space != "" && slices.Contains(p.Spaces, machineID+"/"+space)
}

// mutedSessions are those of list whose pushes are off (the manager opens no card for them).
func (s *Service) mutedSessions(mid string, list []proto.Session) []string {
	out := []string{}
	for _, sess := range list {
		if s.mutedAs(mid, sess.SessionID, sess.SpaceName) {
			out = append(out, sess.SessionID)
		}
	}
	return out
}
