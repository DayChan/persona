package assistant

import (
	"context"
	"errors"
	"time"
)

// ---- seen ----

// loadSeen reads the seen times from the store, merging the ones marked meanwhile, unless that was done already. It
// answers whether this call read them; a failed read is tried again by the next call.
func (s *Service) loadSeen(ctx context.Context) bool {
	s.fleet.mu.Lock()
	loaded := s.fleet.seenLoaded
	s.fleet.mu.Unlock()
	if loaded {
		return false
	}
	st := s.fleetStore()
	if st == nil {
		return false
	}
	m, err := st.SeenTimes(ctx)
	if err != nil {
		if errors.Is(err, errNoTable) { // a store that does not keep them: nothing to read again
			s.fleet.mu.Lock()
			s.fleet.seenLoaded = true
			s.fleet.mu.Unlock()
		} else {
			s.logf("load seen times: %v", err)
		}
		return false
	}
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.seenLoaded { // another call read them meanwhile
		return false
	}
	for k, v := range s.fleet.seen { // one marked meanwhile is newer
		m[k] = v
	}
	s.fleet.seen = m
	s.fleet.seenLoaded = true
	return true
}

// retrySeen reads the seen times again if they could not be read so far, and refreshes the session lists' states
// once they are.
func (s *Service) retrySeen(ctx context.Context) {
	if !s.loadSeen(ctx) {
		return
	}
	for _, m := range s.fleetMachines() {
		s.publishSessions(m.ID)
	}
}

func (s *Service) seenAt(machine, session string) time.Time {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	return s.fleet.seen[[2]string{machine, session}]
}

// markSeen records that the user looked at a session (opened it, or it finished while open) and refreshes states.
func (s *Service) markSeen(ctx context.Context, machine, session string) {
	now := time.Now().UTC()
	s.fleet.mu.Lock()
	if s.fleet.seen == nil {
		s.fleet.seen = map[[2]string]time.Time{}
	}
	s.fleet.seen[[2]string{machine, session}] = now
	s.fleet.mu.Unlock()
	s.SessionSeen(ctx, SessionSeen{MachineID: machine, SessionID: session, At: now}) // how long a triaged turn waited
	if st := s.fleetStore(); st != nil {
		if err := st.MarkSeen(context.WithoutCancel(ctx), machine, session, now); err != nil {
			s.logf("mark seen: %v", err)
		}
	}
	s.publishSessions(machine)
}
