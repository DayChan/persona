package assistant

import (
	"context"
	"sync"
)

// Machine trust comes from the manager's device configuration, never node.info or an MCP hint.
func (s *Service) isWorkMachine(machine string) bool {
	if s.devs != nil {
		machine = s.devs.Canonical(machine)
		for _, d := range s.devs.List() {
			if d.ID == machine && d.Trust == "work" {
				return true
			}
		}
	}
	for _, id := range s.hello(context.Background()).Assistant.WorkMachines {
		if s.sameMachine(machine, id) {
			return true
		}
	}
	return false
}

func (s *Service) sameMachine(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if s.devs != nil {
		return s.devs.Canonical(a) == s.devs.Canonical(b)
	}
	return a == b
}

// A new runtime can request tools before it has a session ID or appears in the node's list.
// Only manager-initiated starts grant that bootstrap on an explicitly selected personal machine.
type assistantStarts struct {
	mu       sync.Mutex
	machines map[string]int
}

func (s *Service) beginAssistantStart(machine string) func() {
	if s.devs != nil {
		machine = s.devs.Canonical(machine)
	}
	b := &s.startingAssistants
	b.mu.Lock()
	if b.machines == nil {
		b.machines = map[string]int{}
	}
	b.machines[machine]++
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		b.machines[machine]--
		if b.machines[machine] == 0 {
			delete(b.machines, machine)
		}
		b.mu.Unlock()
	}
}

// assistantCaller checks the connection's machine before considering the session. The node's
// assistant flag selects the tool set; it does not grant access to it. Unknown sessions on the
// configured machines are permitted during startup (including Codex's temporary proc-* IDs).
func (s *Service) assistantCaller(ctx context.Context, machine, session string) bool {
	if machine == "" || s.isWorkMachine(machine) {
		return false
	}
	cfg := s.settings(ctx)
	allowed := s.sameMachine(machine, cfg.MachineID) || s.sameMachine(machine, s.hello(ctx).Models.InboxAgentMachine)
	b := &s.startingAssistants
	id := machine
	if s.devs != nil {
		id = s.devs.Canonical(id)
	}
	b.mu.Lock()
	allowed = allowed || b.machines[id] > 0
	b.mu.Unlock()
	if session != "" && !allowed {
		s.origins.mu.Lock()
		if s.originsLocked(ctx) {
			matches := func() bool {
				o := s.origins.m[session]
				return o != nil && o.By == originByStart && s.sameMachine(machine, o.MachineID)
			}
			allowed = matches()
			if !allowed && s.refreshOriginsOnMissLocked(ctx) {
				allowed = matches()
			}
		}
		s.origins.mu.Unlock()
	}
	if !allowed {
		return false
	}
	if _, guest, err := s.guestOf(ctx, machine, session); err != nil {
		return false
	} else if guest {
		return true
	} // its tool whitelist and no-context rule are enforced separately
	if sess, known := s.nodeSession(machine, session); known {
		if sess.PaneID != "" {
			return false
		}
		if sess.Cwd != "" {
			m, ok := s.fleetMachine(machine)
			return ok && m.isAssistant(sess)
		}
	}
	return true
}

// Runtime tool discovery has no session ID, even when reconnecting an existing assistant.
// A machine the manager has started an assistant on can rediscover the schema; calls still
// require assistantCaller and the recorded session. Backfilled node claims grant neither.
func (s *Service) assistantDiscovery(ctx context.Context, machine, session string) bool {
	if s.assistantCaller(ctx, machine, session) {
		return true
	}
	if session != "" || machine == "" || s.isWorkMachine(machine) {
		return false
	}
	s.origins.mu.Lock()
	defer s.origins.mu.Unlock()
	if !s.originsLocked(ctx) {
		return false
	}
	matches := func() bool {
		for _, o := range s.origins.m {
			if o.By == originByStart && s.sameMachine(machine, o.MachineID) {
				return true
			}
		}
		return false
	}
	if matches() {
		return true
	}
	return s.refreshOriginsOnMissLocked(ctx) && matches()
}
