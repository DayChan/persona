package exttest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
	"github.com/nwverse/persona/sdk/go/wire"
)

// Host is a fake hub for a program's own tests, beyond Check: it opens the protocol, then the test calls the program
// as the hub would (Call), sends it the hub's events (Hear) and answers what it asks (Answer; a connector's secrets
// are kept in memory, a channel's messages in Received). Every message both ways that the schemas describe
// (wire.Calls: the connector, the channel, the knowledge base) is held to them, and the test fails on one that does
// not hold.
//
//	h := exttest.Start(t, manifest, exttest.Serve(t, program()))
//	h.Answer("kb.list", func(json.RawMessage) (any, error) { return wire.KBListResult{Items: []wire.KBEntry{}, Tags: []string{}}, nil })
//	var st wire.ConnectorStatus
//	h.Call("connector.status", wire.ConnectorParams{Contribution: "example"}, &st)
type Host struct {
	t    testing.TB
	h    *hub
	ctx  context.Context
	Init wire.InitializeResult // what initialize answered
}

// Start opens the protocol with the program (initialize, with config if given) and fails the test if it cannot. It
// sends what the real hub does: the manifest's contributions as the active ones, its permissions as granted, every
// hub capability this SDK knows (wire.HubCapabilities). StartWith changes any of it.
func Start(t testing.TB, manifest []byte, run Run, config ...json.RawMessage) *Host {
	t.Helper()
	return StartWith(t, manifest, run, func(p *wire.InitializeParams) {
		if len(config) > 0 {
			p.Config = config[0]
		}
	})
}

// StartWith is Start with initialize's params as adjust leaves them: another grant, the hub's extension point
// versions (none by default), a locale, the user's secrets.
func StartWith(t testing.TB, manifest []byte, run Run, adjust func(*wire.InitializeParams)) *Host {
	t.Helper()
	var m wire.Manifest
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := newHub(run)
	go h.serve(ctx)
	host := &Host{t: t, h: h, ctx: ctx}
	t.Cleanup(func() {
		for _, p := range h.takeProblems() {
			t.Errorf("%s", p)
		}
	})
	info := &wire.ExtensionInfo{ID: m.ID, Version: m.Version, Dir: t.TempDir(), DataDir: t.TempDir()}
	init := wire.InitializeParams{Protocol: ext.Protocol, Extension: info, Plugin: info, Active: active(manifest),
		Hub: wire.HubInfo{Build: 0, Role: wire.HubRoleCenter, Timezone: "UTC", ExtensionPoints: map[string]int{},
			Capabilities: slices.Clone(wire.HubCapabilities)}}
	if m.Permissions != nil {
		init.Granted = *m.Permissions
	}
	if adjust != nil {
		adjust(&init)
	}
	g := init.Granted // the host refuses by what is granted, as the hub does
	h.mu.Lock()
	h.manifest = &wire.Manifest{ID: m.ID, Contributes: m.Contributes, Permissions: &g}
	h.mu.Unlock()
	if err := host.call(wire.MethodInitialize, init, &host.Init); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return host
}

// active are the ids of every contribution the manifest has, by extension point: what the hub starts a program
// with when all of them are on.
func active(manifest []byte) map[string][]string {
	var m struct {
		Contributes map[string]json.RawMessage `json:"contributes"`
	}
	_ = json.Unmarshal(manifest, &m)
	out := map[string][]string{}
	for point, raw := range m.Contributes {
		var items []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &items) != nil {
			continue
		}
		for _, it := range items {
			if it.ID != "" {
				out[point] = append(out[point], it.ID)
			}
		}
	}
	return out
}

// Call calls the program's method as the hub does and decodes its answer into out (nil: not decoded). Params and
// answer of a method the schemas describe are held to them; an error answer comes back as an *ext.Error.
func (h *Host) Call(method string, params, out any) error {
	h.t.Helper()
	if _, ok := wire.Calls[method]; ok {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		h.h.holds(method+" (the test's params)", wire.ValidateParams(method, raw))
	}
	var raw json.RawMessage
	if err := h.call(method, params, &raw); err != nil {
		return err
	}
	snakeKeys(h.t, method, raw, 0)
	if _, ok := wire.Calls[method]; ok {
		h.h.holds(method+" (its answer)", wire.ValidateResult(method, raw))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (h *Host) call(method string, params, out any) error {
	ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
	defer cancel()
	return h.h.call(ctx, method, params, out)
}

// Hear sends the program one of the hub's events (event.kb.changed, event.agent.needs_you…), held to its schema
// when the schemas describe it.
func (h *Host) Hear(event string, params any) error {
	h.t.Helper()
	if _, ok := wire.Calls[event]; ok {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		h.h.holds(event+" (the test's params)", wire.ValidateParams(event, raw))
	}
	return h.h.notify(event, params)
}

// Answer answers the program's calls of method (kb.list, connector.posts.put…) with f, from now on. A method
// without an answer gets {} (a connector's secrets.* are kept in memory).
func (h *Host) Answer(method string, f func(params json.RawMessage) (any, error)) {
	h.h.mu.Lock()
	defer h.h.mu.Unlock()
	h.h.answers[method] = f
}

// Secret is what a connector keeps with connector.secrets.save (nil: nothing).
func (h *Host) Secret(contribution string) []byte {
	h.h.mu.Lock()
	defer h.h.mu.Unlock()
	return h.h.secrets[contribution]
}

// Events are the events the program has sent so far (connector.changed…), in order.
func (h *Host) Events() []Event {
	h.h.mu.Lock()
	defer h.h.mu.Unlock()
	return slices.Clone(h.h.events)
}

// CheckConnectors holds each connector the manifest contributes to the connector protocol (docs/protocol/connector-protocol.md),
// the read-only calls the hub makes of what its capabilities declare: status, tools.list, actions.list,
// inbox.collect, thread.page of the first thread, archive.changes, items.list, snapshot.get and options.get. Each
// answer holds to the schema (connector.schema.json); a refusal in words is fine (a backend a test cannot reach), not
// knowing a declared method or the connector is not. It never calls a write (actions.do, inbox.reply). The hub's own
// check is stricter still (`personad extension test`).
func CheckConnectors(t testing.TB, manifest []byte, run Run, config ...json.RawMessage) *Host {
	t.Helper()
	h := Start(t, manifest, run, config...)
	var m wire.Manifest
	_ = json.Unmarshal(manifest, &m)
	if m.Contributes == nil || len(m.Contributes.Connectors) == 0 {
		t.Fatal("the manifest contributes no connectors")
	}
	if err := h.call(ext.ConnectorNamespace+".status", wire.ConnectorParams{Contribution: "no-such-connector"}, nil); code(err) != wire.ErrorCodeNoContribution {
		t.Errorf("connector.status of a connector it does not have: want %s, got %v", wire.ErrorCodeNoContribution, err)
	}
	for _, c := range m.Contributes.Connectors {
		has := func(capability string) bool { return slices.Contains(c.Capabilities, capability) }
		ask := func(method string, params, out any) bool {
			t.Helper()
			err := h.Call(ext.ConnectorNamespace+"."+method, params, out)
			var e *ext.Error
			switch {
			case err == nil:
				return true
			case errors.As(err, &e) && slices.Contains([]string{wire.ErrorCodeUnknownMethod, wire.ErrorCodeNoCapability,
				wire.ErrorCodeNoContribution, wire.ErrorCodeNotInitialized}, e.Code):
				t.Errorf("connector %s: %s answered %s for what its manifest declares", c.ID, method, e.Code)
			case errors.As(err, &e) && e.Message == "":
				t.Errorf("connector %s: %s: an error without a message (it is for people)", c.ID, method)
			case !errors.As(err, &e):
				t.Fatalf("connector %s: %s: %v", c.ID, method, err)
			}
			return false
		}
		p := wire.ConnectorParams{Contribution: c.ID}
		if has(wire.ConnectorCapabilityStatus) {
			var st wire.ConnectorStatus
			if ask("status", p, &st) && st.State == wire.ConnectorStateDisabled {
				t.Errorf("connector %s: status disabled is the hub's, not a program's", c.ID)
			}
		}
		if has(wire.ConnectorCapabilityCatalog) {
			var catalog wire.ConnectorCatalog
			if ask("catalog.list", p, &catalog) {
				if err := wire.CheckConnectorCatalog(catalog); err != nil {
					t.Errorf("connector %s: catalog.list: %v", c.ID, err)
				}
			}
		}
		if has(wire.ConnectorCapabilityTools) {
			ask("tools.list", p, nil)
		}
		if has(wire.ConnectorCapabilityActions) {
			ask("actions.list", p, nil)
		}
		var things wire.ConnectorCollectResult
		if has(wire.ConnectorCapabilityInbox) {
			ask("inbox.collect", p, &things)
		}
		if has(wire.ConnectorCapabilityThreads) && len(things.Things) > 0 {
			ask("thread.page", wire.ConnectorThreadParams{Contribution: c.ID, Thread: things.Things[0].Thread, Limit: 20}, nil)
		}
		if has(wire.ConnectorCapabilityArchive) {
			ask("archive.changes", wire.ConnectorChangesParams{Contribution: c.ID, Limit: 50}, nil)
		}
		if has(wire.ConnectorCapabilityItems) {
			ask("items.list", p, nil)
		}
		if has(wire.ConnectorCapabilitySnapshot) {
			ask("snapshot.get", wire.ConnectorSnapshotParams{Contribution: c.ID, TZ: "UTC"}, nil)
		}
		if has(wire.ConnectorCapabilityOptions) {
			ask("options.get", p, nil)
		}
	}
	return h
}

// CheckChannels holds each channel the manifest contributes to the channel protocol (docs/extensions/channels.md "4.
// Contributing a channel"): channel.send of a note is sent or cannot_send, the same ref again answers the same
// (sent once), channel.typing is taken, a send without a ref is refused, and another channel is no_contribution.
// Every message both ways holds to the schema (channel.schema.json). It returns the host, with the messages the
// program handed over meanwhile in Received.
func CheckChannels(t testing.TB, manifest []byte, run Run, config ...json.RawMessage) *Host {
	t.Helper()
	h := Start(t, manifest, run, config...)
	var m wire.Manifest
	_ = json.Unmarshal(manifest, &m)
	if m.Contributes == nil || len(m.Contributes.Channel) == 0 {
		t.Fatal("the manifest contributes no channels")
	}
	send := ext.ChannelNamespace + ".send"
	for _, c := range m.Contributes.Channel {
		p := wire.ChannelSendParams{Contribution: c.ID, Ref: "exttest-1", Text: "A note from the test.", Kind: wire.ChannelSendKindNote}
		var first, second wire.ChannelSendResult
		err1 := h.Call(send, p, &first)
		err2 := h.Call(send, p, &second)
		switch {
		case err1 != nil && code(err1) != wire.ChannelErrorCodeCannotSend:
			t.Errorf("channel %s: channel.send: %v (sent, or cannot_send)", c.ID, err1)
		case code(err1) != code(err2) || first != second:
			t.Errorf("channel %s: the same ref answered %+v / %v, then %+v / %v", c.ID, first, err1, second, err2)
		}
		if err := h.Call(ext.ChannelNamespace+".typing", wire.ChannelTypingParams{Contribution: c.ID}, nil); err != nil {
			t.Errorf("channel %s: channel.typing: %v", c.ID, err)
		}
		if err := h.call(send, map[string]string{"contribution": c.ID, "text": "x", "kind": wire.ChannelSendKindNote}, nil); err == nil {
			t.Errorf("channel %s: channel.send without a ref was taken", c.ID)
		}
	}
	if err := h.call(send, wire.ChannelSendParams{Contribution: "no-such-channel", Ref: "r", Text: "x", Kind: wire.ChannelSendKindNote}, nil); code(err) != wire.ErrorCodeNoContribution {
		t.Errorf("channel.send to a channel it does not have: want %s, got %v", wire.ErrorCodeNoContribution, err)
	}
	return h
}

// Sent are the sends the program recorded with sent.record (RecordSent), in order.
func (h *Host) Sent() []wire.ConnectorSentRecordParams {
	h.h.mu.Lock()
	defer h.h.mu.Unlock()
	return slices.Clone(h.h.sent)
}

// Received are the owner's messages the program handed over (channel.receive), in order.
func (h *Host) Received() []wire.ChannelReceiveParams {
	h.h.mu.Lock()
	defer h.h.mu.Unlock()
	return slices.Clone(h.h.received)
}
