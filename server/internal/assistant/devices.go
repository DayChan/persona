package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant is the chief manager, and the user's computers are its (docs/server/machines.md "Who connects the computers"): its process
// connects them itself (internal/devices), the hub only relays to it. This file is how the bus connection (bus.go)
// takes the hub's devices.* calls and events to that layer, and how the assistant's own calls to a machine go to it
// directly instead of round the hub.

// Devices is the manager's own connections to the computers (internal/devices.Devices).
type Devices interface {
	// Hello takes manager.hello's answer (its devices section); BusUp is told once the connection is up.
	Hello(res json.RawMessage)
	BusUp()
	// Request and Event take the hub's devices.* calls and events (handled false: not one).
	Request(ctx context.Context, method string, params json.RawMessage) (any, bool, error)
	Event(name string, params json.RawMessage) bool
	// Handler serves the apps' routes on machines that are the manager's, the rest going to next.
	Handler(next http.Handler) http.Handler
	// Has: a machine by this id; CallNode calls its node (apierr.ErrOffline, rpc.ErrClosed, the node's errors).
	Has(id string) bool
	CallNode(ctx context.Context, id, method string, params json.RawMessage) (json.RawMessage, error)
	// SetKeep: the sessions each machine's node lists however quiet (the snapshot's profile).
	SetKeep(keep map[string][]string)
	// What the fleet reads of the machines (fleet_machines.go): every machine, one's id as the devices know it (an id
	// another one absorbed: that one), its sessions as its node listed them, when its node's connection began, the
	// token usage.
	List() []devices.Summary
	// MachineResources returns the retained system and latest sample, including a stale sample's timestamp.
	MachineResources(id string) (*proto.Machine, *proto.MetricSample, *bool)
	Canonical(id string) string
	// Sessions copies the whole list out: the caller owns its rows (not their slices, shared and never written into).
	Sessions(id string) ([]proto.Session, bool)
	// SessionsWhere copies out only the sessions keep accepts, read in place under the devices' lock (keep must not
	// change or keep them, nor call the devices): a view that keeps a few rows of a busy machine copies only those.
	SessionsWhere(id string, keep func(*proto.Session) bool) ([]proto.Session, bool)
	Session(id, session string) (proto.Session, bool)
	ConnectedAt(id string) time.Time
	Usage() assistantwire.UsageSummary
	// Follow has the node send the manager a session's items and text as they are made (on), or stop.
	Follow(ctx context.Context, machine, session string, on bool) error
}

// localDeviceCall is a device.call the manager answers itself: a node's method on a machine it has. The hub still
// answers what it holds (assistantwire.HubDeviceMethods: devices.list with the phone, the interactions, the sessions
// as the apps get them, following a conversation for a channel, a send under the apps' once-only guard).
func (b *BusHub) localDeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, bool, error) {
	d := b.opts.Devices
	if d == nil || p.Device == assistantwire.DeviceAll || assistantwire.HubDeviceMethods[p.Method] || !d.Has(p.Device) {
		return nil, false, nil
	}
	out, err := d.CallNode(ctx, p.Device, p.Method, p.Params)
	return out, true, err
}

// keepFromSnapshot hands the devices what the snapshot's profile says each node keeps listing.
func (b *BusHub) keepFromSnapshot(s Snapshot) {
	d := b.opts.Devices
	if d == nil || s.Profile == nil || s.Profile.Keep == nil {
		return
	}
	keep := map[string][]string{}
	for _, r := range s.Profile.Keep {
		keep[r.MachineID] = append(keep[r.MachineID], r.SessionID)
	}
	d.SetKeep(keep)
}

// NodeAnswer answers the requests of a node that are the assistant's own (devices.Options.Answer): which launcher and
// model a message the node sends a conversation by itself runs on (assistant.model), and the live snapshot before
// each message (assistant.context).
func (s *Service) NodeAnswer(ctx context.Context, machine, method string, params json.RawMessage) (any, bool, error) {
	var p struct {
		SessionID string                           `json:"session_id"`
		Ack       []assistantwire.ViewErrorReceipt `json:"ack"`
	}
	switch method {
	case "assistant.model":
		_ = json.Unmarshal(params, &p)
		if !s.assistantCaller(ctx, machine, p.SessionID) {
			return map[string]any{}, true, nil
		}
		out, err := s.SendParams(ctx, SendParams{MachineID: machine, SessionID: p.SessionID, Params: map[string]any{}})
		return out, true, err
	case "assistant.context":
		_ = json.Unmarshal(params, &p)
		// The machine is the connection's, not anything in params. A guest conversation gets no snapshot: it is the
		// user's state (guest.go).
		if s.isGuest(ctx, machine, p.SessionID) {
			return map[string]any{}, true, nil
		}
		if !s.assistantCaller(ctx, machine, p.SessionID) {
			return ContextResult{}, true, nil
		}
		return s.Context(ctx, ContextParams{SessionID: p.SessionID, MachineID: machine, Ack: p.Ack}), true, nil
	}
	return nil, false, nil
}
