package assistant

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the nodes' events and requests ----

// NodeEvent takes a node's event before the hub does (devices.Options.Event); false: the hub does not get it (the
// manager tells it what follows instead).
func (s *Service) NodeEvent(machine, event string, params json.RawMessage) bool {
	if !s.fleetOn() {
		return true
	}
	switch event {
	case "sessions.changed":
		// The devices took this very list just before passing the event on (Machine.onEvent, and BusUp's replay of
		// it): a copy of theirs, not a second decode of the whole list on every change. Its nested slices are shared
		// with theirs, as decorated's are: never written into.
		if s.devs != nil {
			if list, ok := s.devs.Sessions(machine); ok {
				s.setSessions(machine, list)
				return false
			}
		}
		var p struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if json.Unmarshal(params, &p) == nil {
			s.setSessions(machine, p.Sessions)
		}
		return false // the hub gets the list as the apps do (fleet.sessions)
	case "sessions.revs":
		s.syncPlanInteractions(machine)
	case "session.items": // the channels' (fleet_follow.go); the apps get them from the hub
		s.followedItems(machine, params)
	case "session.delta":
		s.followedDelta(machine, params)
	case "interaction.dismiss":
		var p struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(params, &p) == nil {
			s.recordEnd(p.ID, "dismissed", nil)
			s.finishInteraction(p.ID)
		}
		return false
	case "interaction.cancel":
		var p struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(params, &p) == nil {
			s.recordEnd(p.ID, "cancelled", nil) // or the next manager would take it for one a hook may still claim
			s.finishInteraction(p.ID)
		}
		return false
	case "codex.interactions": // the Codex questions and approvals open on this machine (fleet_ix_codex.go)
		s.syncCodexInteractions(machine, params)
		return false
	case "hook.event":
		var p map[string]any
		if json.Unmarshal(params, &p) == nil {
			if sid, _ := p["session_id"].(string); sid != "" {
				// A report-only hook (node/globalhooks.go) is not Persona's hooks running in the session: it asks for
				// no card, so it must not hold the screen's back (hasHooks). Its Stop still counts for stopDuplicate.
				if p["report_only"] != true {
					s.fleet.mu.Lock()
					if s.fleet.hooked == nil {
						s.fleet.hooked = map[[2]string]bool{}
					}
					s.fleet.hooked[[2]string{machine, sid}] = true
					s.fleet.mu.Unlock()
				}
				s.noteStop(machine, sid, p)
			}
		}
		// the hub shows it to the apps (hook_event)
	}
	return true
}

// NodeRequest answers what of a node's requests is the manager's (devices.Options.Answer, with NodeAnswer's).
func (s *Service) NodeRequest(ctx context.Context, machine, method string, params json.RawMessage) (any, bool, error) {
	if !s.fleetOn() {
		return nil, false, nil
	}
	switch method {
	case "computer.elicit": // LCU asked the person something; the card is the same one as app approval
		if s.ext == nil {
			return map[string]any{"action": "cancel"}, true, nil
		}
		out, err := s.ext.AnswerComputerElicit(ctx, machine, params)
		return out, true, err
	case "interaction.create":
		var p struct {
			Interaction proto.Interaction `json:"interaction"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, true, err
		}
		p.Interaction.MachineID = machine
		// A write through a skill's gated command (node/larkgate.go): its plugin names what the ids are.
		if sum, ok := s.assistantSummary(ctx, p.Interaction.ToolName, p.Interaction.Summary); ok {
			p.Interaction.Summary = sum
		}
		r, err := s.nodeInteraction(ctx, p.Interaction)
		return r, true, err
	case "assistant.lark":
		out, err := s.assistantLark(ctx, machine)
		return out, true, err
	case proto.MethodLarkSent: // a write the gate proxy forwarded and the API took (node/gateproxy.go)
		if s.ext == nil {
			return nil, true, errors.New("lark.sent: no extensions, there is no ledger")
		}
		out, err := s.ext.RecordNodeSent(ctx, machine, params)
		return out, true, err
	case proto.MethodMailAttachment: // mail_save_attachment's bytes for the node that saves them (fleet_tools.go)
		var p proto.MailAttachmentRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, true, err
		}
		out, err := s.mailAttachment(ctx, machine, p)
		return out, true, err
	case "mcp.tools":
		var p struct {
			Assistant bool   `json:"assistant"`
			SessionID string `json:"session_id"`
			Guest     bool   `json:"guest"` // the node's word that it is a guest conversation (guest.go)
		}
		_ = json.Unmarshal(params, &p)
		return map[string]any{"tools": s.sessionMCPTools(ctx, machine, p.SessionID, p.Assistant, p.Guest)}, true, nil
	case "mcp.call":
		var p struct {
			Name      string          `json:"name"`
			Args      json.RawMessage `json:"args"`
			SessionID string          `json:"session_id"`
			Assistant bool            `json:"assistant"`
			Guest     bool            `json:"guest"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, true, err
		}
		if res, refused := s.guestRefuses(ctx, machine, p.SessionID, p.Name, p.Guest); refused {
			return res, true, nil
		}
		return s.mcpCall(ctx, machine, p.SessionID, p.Name, p.Args, p.Assistant), true, nil
	}
	return nil, false, nil
}

// FleetUp: the connection to the hub is (again) up: it gets every pending interaction, every session list and the
// fleet again (it may have restarted and know nothing).
func (s *Service) FleetUp(ctx context.Context) {
	if !s.fleetOn() {
		return
	}
	s.fleetStart(context.WithoutCancel(ctx))
	defer s.boardFleetUp() // the pending cards are read: the board may have changed while the assistant was away
	s.fleet.sentMu.Lock()
	s.fleet.sent = nil
	s.fleet.sentMu.Unlock()
	s.hub.Tell(assistantwire.EventFleetInteractions, assistantwire.FleetInteractions{Interactions: s.pendingInteractions()})
	for _, m := range s.fleetMachines() {
		s.publishSessions(m.ID)
	}
	s.liveChanged(true)
}

// FleetEvent takes the hub's fleet.* events.
func (s *Service) FleetEvent(ctx context.Context, name string, params json.RawMessage) bool {
	switch name {
	case assistantwire.EventClients:
		var c assistantwire.Clients
		if json.Unmarshal(params, &c) == nil {
			s.fleet.mu.Lock()
			s.fleet.clients = c
			s.fleet.mu.Unlock()
		}
	case assistantwire.EventMutes:
		var p assistantwire.FleetMutes
		if json.Unmarshal(params, &p) != nil || p.Mutes == nil {
			return true // not the mutes (a hub from before they came with the event): the ones handed over last stay
		}
		s.setMutes(*p.Mutes)
		for _, m := range s.fleetMachines() {
			s.publishSessions(m.ID)
		}
		s.liveChanged(false)
	case assistantwire.EventSeen:
		var p assistantwire.FleetSeen
		if json.Unmarshal(params, &p) == nil && p.MachineID != "" && p.SessionID != "" {
			if m, ok := s.fleetMachine(p.MachineID); ok {
				s.markSeen(ctx, m.ID, p.SessionID)
			}
		}
	default:
		return false
	}
	return true
}
