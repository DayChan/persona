package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/extension/uistream"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

type pageTestHub struct {
	mu     sync.Mutex
	frames []extwire.UITarget
}

func (p *pageTestHub) Call(context.Context, string, any, any) error { return nil }
func (p *pageTestHub) Notify(event string, params any) error {
	if event == extwire.EventUIStream {
		p.mu.Lock()
		p.frames = append(p.frames, params.(extwire.UITarget))
		p.mu.Unlock()
	}
	return nil
}
func (p *pageTestHub) all() []extwire.UITarget {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]extwire.UITarget(nil), p.frames...)
}

// Real SDK source process and manager integration under a sanctioned injected
// host. This deliberately does not claim production sandbox or renderer E2E.
func pageProcessManager(t *testing.T, configure ...func(*host.Spec)) (*Manager, *host.Proc, *pageTestHub) {
	t.Helper()
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Fatal("Bun is required to build the real source fixture")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the injected source host")
	}
	root := shortTempDir(t)
	program := filepath.Join(root, "program.mjs")
	source, err := filepath.Abs("testdata/ui-page-program.tsx")
	if err != nil {
		t.Fatal(err)
	}
	buildCtx, stopBuild := context.WithTimeout(t.Context(), 30*time.Second)
	defer stopBuild()
	cmd := exec.CommandContext(buildCtx, bun, "build", source, "--target=node", "--outfile="+program)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build source: %v %s", err, output)
	}
	output := &pageTestHub{}
	h := New(Options{Dir: root, Config: &Config{dir: root}, Hub: output})
	h.extensions = newExtensions(root, nil, func(string, ...any) {})
	manifest := &extension.Manifest{ManifestVersion: 1, ID: "example-page", Name: "Synthetic page", Version: "1.0.0", Contributes: map[string]json.RawMessage{"ui": json.RawMessage(`[{"id":"home","title":"Synthetic page","placement":"extension_page"}]`)}}
	if err := h.extensions.reg.AddHubJSON(manifest, nil, true); err != nil {
		t.Fatal(err)
	}
	var process *host.Proc
	spec := host.Spec{ID: "example-page", Command: node, Args: []string{program}, Dir: root, CleanEnv: true, RPC: true, OnCall: true,
		Init: func(context.Context) (extension.InitializeParams, error) {
			return extension.InitializeParams{Protocol: 1}, nil
		},
		OnEvent: func(event string, raw json.RawMessage) {
			if event == "ui.page.snapshot" {
				h.pageSnapshot("example-page", process, raw)
			}
		},
	}
	for _, configure := range configure {
		configure(&spec)
	}
	process = host.Start(t.Context(), spec)
	h.extensions.byID[manifest.ID] = &hubExtension{id: manifest.ID, proc: process}
	t.Cleanup(func() { h.pageDropExtension(""); process.Stop(); h.pageStopDelivery() })
	return h, process, output
}

var pageWho = extwire.UIPrincipal{Hub: "example-hub", Principal: "example-device", Connection: "example-connection", Role: "full", Lang: "en"}

func pageCall(t *testing.T, h *Manager, who extwire.UIPrincipal, request wire.UIStreamRequest) (json.RawMessage, error) {
	t.Helper()
	raw, err := json.Marshal(wire.UIStreamClientEnvelope{Type: "extension_ui_stream", Request: &request})
	if err != nil {
		t.Fatal(err)
	}
	return h.uiStream(t.Context(), extwire.UIStreamCall{Who: who, Envelope: raw})
}
func pageOpenTest(t *testing.T, h *Manager, who extwire.UIPrincipal) *uistream.Tree {
	t.Helper()
	req := wire.UIStreamRequest{Request: "open", Protocol: 1, RequestID: "open-" + randomHex(8), Extension: "example-page", Contribution: "home", Context: &wire.UIContext{UIVersion: 6, Platform: "web", Stream: &wire.UIStreamCapability{Protocol: 1, Components: []string{"text", "button", "row", "code"}}}}
	raw, err := pageCall(t, h, who, req)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(raw, &envelope)
	tree, err := uistream.DecodeSnapshot(envelope["snapshot"])
	if err != nil {
		t.Fatalf("snapshot: %v %s", err, raw)
	}
	return tree
}
func pageTree(h *Manager, id string) *uistream.Tree {
	h.uiPages.mu.Lock()
	defer h.uiPages.mu.Unlock()
	return h.uiPages.sessions[id].tree
}
func pageEvent(t *testing.T, tree *uistream.Tree, key, id string) wire.UIStreamRequest {
	t.Helper()
	snapshot := tree.Snapshot()
	action := ""
	for _, item := range snapshot.View.Sections[0].Items {
		if item.Key == key && item.Action != nil {
			action = item.Action.ID
		}
	}
	if action == "" {
		t.Fatalf("missing callback %s", key)
	}
	return wire.UIStreamRequest{Request: "event", Protocol: 1, RequestID: id, Interaction: &wire.UIStreamEvent{Protocol: 1, Session: tree.Session(), Epoch: tree.Epoch(), Version: tree.Version(), EventID: id, Key: key, Action: action, Event: "press", Value: json.RawMessage(`{}`)}}
}
func pageStats(t *testing.T, process *host.Proc) map[string]int {
	t.Helper()
	var out map[string]int
	if err := process.Call(t.Context(), "test.stats", nil, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func pageAwait(t *testing.T, condition func() bool) {
	t.Helper()
	end := time.Now().Add(3 * time.Second)
	for time.Now().Before(end) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for source page")
}

func TestPageSourceSDKEventFenceAndReceipts(t *testing.T) {
	h, process, transport := pageProcessManager(t)
	tree := pageOpenTest(t, h, pageWho)
	raw, _ := tree.MarshalJSON()
	if !bytes.Contains(raw, []byte(`"truncated":false`)) || !bytes.Contains(raw, []byte(`"text":""`)) {
		t.Fatalf("lost native presence: %s", raw)
	}
	initial := pageEvent(t, tree, "no-ui", "first")
	initial.Interaction.Value = json.RawMessage(`{"b":"2","a":"1"}`)
	first, err := pageCall(t, h, pageWho, initial)
	if err != nil {
		t.Fatal(err)
	}
	if pageTree(h, tree.Session()).Version() != 2 || pageStats(t, process)["writes"] != 1 {
		t.Fatal("no-UI callback failed to consume revision")
	}
	replay, err := pageCall(t, h, pageWho, initial)
	if err != nil || !bytes.Equal(first, replay) {
		t.Fatalf("replay %s %v", replay, err)
	}
	retryRequest := initial
	retryEvent := *initial.Interaction
	retryEvent.Value = json.RawMessage(`{"a":"1","b":"2"}`)
	retryRequest.Interaction = &retryEvent
	retryRequest.RequestID = "retry-correlation"
	retryFrame, err := pageCall(t, h, pageWho, retryRequest)
	if err != nil {
		t.Fatal(err)
	}
	var correlated map[string]json.RawMessage
	_ = json.Unmarshal(retryFrame, &correlated)
	if string(correlated["request_id"]) != `"retry-correlation"` || pageStats(t, process)["writes"] != 1 {
		t.Fatal("semantic retry lost correlation or repeated a write")
	}
	changed := initial
	event := *initial.Interaction
	event.Value = json.RawMessage(`{"changed":true}`)
	changed.Interaction = &event
	if _, err := pageCall(t, h, pageWho, changed); err == nil {
		t.Fatal("changed receipt body accepted")
	}
	for i := 0; i < 70; i++ {
		current := pageTree(h, tree.Session())
		request := pageEvent(t, current, "no-ui", strconvID(i))
		if _, err := pageCall(t, h, pageWho, request); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pageCall(t, h, pageWho, initial); err == nil {
		t.Fatal("evicted unchanged write replayed")
	}
	if pageStats(t, process)["writes"] != 71 {
		t.Fatal("duplicate executed a write")
	}
	pageAwait(t, func() bool { return len(transport.all()) >= 72 })
	for _, packet := range transport.all() {
		if packet.Who != pageWho {
			t.Fatal("source chose another recipient")
		}
		var frame map[string]json.RawMessage
		_ = json.Unmarshal(packet.Envelope, &frame)
		if bytes.Equal(frame["kind"], []byte(`"delta"`)) {
			var delta wire.UIStreamDelta
			_ = json.Unmarshal(frame["delta"], &delta)
			if len(delta.Operations) != 0 || delta.Version != delta.BaseVersion+1 {
				t.Fatal("no-UI fence replaced root or skipped revisions")
			}
		}
	}
	request := pageEvent(t, pageTree(h, tree.Session()), "increment", "increment")
	if _, err := pageCall(t, h, pageWho, request); err != nil {
		t.Fatal(err)
	}
	current := pageTree(h, tree.Session())
	if !bytes.Contains(current.ViewJSON(), []byte(`"text":"2"`)) {
		t.Fatal("coalesced native render not published")
	}
}
func strconvID(i int) string { return fmt.Sprintf("event-%d", i) }

func TestPageSourceAsyncSuspendResumeOwnershipAndExpiry(t *testing.T) {
	h, process, transport := pageProcessManager(t)
	tree := pageOpenTest(t, h, pageWho)
	if err := process.Call(t.Context(), "test.update", map[string]int{"count": 9}, nil); err != nil {
		t.Fatal(err)
	}
	pageAwait(t, func() bool { return pageTree(h, tree.Session()).Version() > tree.Version() })
	for _, spoof := range []extwire.UIPrincipal{{Hub: pageWho.Hub, Principal: "another-device", Connection: pageWho.Connection, Role: "full"}, {Hub: "another-hub", Principal: pageWho.Principal, Connection: pageWho.Connection, Role: "full"}, {Hub: pageWho.Hub, Principal: pageWho.Principal, Connection: "another-connection", Role: "full"}} {
		request := wire.UIStreamRequest{Request: "snapshot", Protocol: 1, RequestID: "spoof", Session: tree.Session(), Epoch: tree.Epoch(), Version: tree.Version()}
		if _, err := pageCall(t, h, spoof, request); err == nil {
			t.Fatal("spoofed owner/connection/group accepted")
		}
	}
	suspend := wire.UIStreamRequest{Request: "suspend", Protocol: 1, RequestID: "suspend", Session: tree.Session(), Epoch: tree.Epoch()}
	if _, err := pageCall(t, h, pageWho, suspend); err != nil {
		t.Fatal(err)
	}
	before := pageStats(t, process)
	time.Sleep(40 * time.Millisecond)
	after := pageStats(t, process)
	if after["uiTicks"] != before["uiTicks"] || after["serviceTicks"] <= before["serviceTicks"] {
		t.Fatal("visibility stopped service or retained UI polling")
	}
	if _, err := pageCall(t, h, pageWho, pageEvent(t, pageTree(h, tree.Session()), "no-ui", "hidden")); err == nil {
		t.Fatal("hidden event executed")
	}
	who := pageWho
	who.Connection = "replacement-connection"
	raw, err := pageCall(t, h, who, wire.UIStreamRequest{Request: "resume", Protocol: 1, RequestID: "resume", Session: tree.Session(), Epoch: tree.Epoch(), Version: tree.Version()})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"kind":"snapshot"`)) {
		t.Fatal("resume did not send full snapshot")
	}
	pageAwait(t, func() bool {
		frames := transport.all()
		return len(frames) > 0 && frames[len(frames)-1].Who.Connection == who.Connection
	})
	h.pageBusReset()
	who.Connection = "after-bus-restart"
	if _, err := pageCall(t, h, who, wire.UIStreamRequest{Request: "resume", Protocol: 1, RequestID: "bus-resume", Session: tree.Session(), Epoch: tree.Epoch(), Version: tree.Version()}); err != nil {
		t.Fatal(err)
	}
	ticks := pageStats(t, process)["uiTicks"]
	pageAwait(t, func() bool { return pageStats(t, process)["uiTicks"] > ticks })
	h.pageDisconnect(who)
	h.pageSweep(time.Now().Add(6 * time.Minute))
	if _, err := pageCall(t, h, who, wire.UIStreamRequest{Request: "resume", Protocol: 1, RequestID: "expired", Session: tree.Session(), Epoch: tree.Epoch(), Version: tree.Version()}); err == nil {
		t.Fatal("expired session retained")
	}
	if _, err := pageCall(t, h, who, wire.UIStreamRequest{Request: "close", Protocol: 1, RequestID: "close", Session: tree.Session(), Epoch: tree.Epoch()}); err != nil {
		t.Fatal("close not idempotent")
	}
}

func TestPageSourceQuotasAndRevocation(t *testing.T) {
	h, _, _ := pageProcessManager(t)
	var first *uistream.Tree
	for i := 0; i < 8; i++ {
		tree := pageOpenTest(t, h, pageWho)
		if first == nil {
			first = tree
		}
	}
	request := wire.UIStreamRequest{Request: "open", Protocol: 1, RequestID: "quota", Extension: "example-page", Contribution: "home", Context: &wire.UIContext{Stream: &wire.UIStreamCapability{Protocol: 1, Components: []string{"text", "button", "row", "code"}}, UIVersion: 6}}
	if _, err := pageCall(t, h, pageWho, request); err == nil {
		t.Fatal("principal quota bypassed")
	}
	h.pageDropExtension("example-page")
	if _, err := pageCall(t, h, pageWho, pageEvent(t, first, "no-ui", "revoked")); err == nil {
		t.Fatal("revoked callback executed")
	}
}

func TestPageSourceChangedConfirmationCannotRun(t *testing.T) {
	h, process, _ := pageProcessManager(t)
	tree := pageOpenTest(t, h, pageWho)
	h.opts.Ask = func(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
		if ix.Summary != "Synthetic confirmation" {
			t.Fatal("did not use saved displayed action")
		}
		if err := process.Call(ctx, "test.prompt", map[string]any{"prompt": "Changed synthetic confirmation", "count": 4}, nil); err != nil {
			t.Fatal(err)
		}
		allow := true
		return proto.Resolution{Allow: &allow}, nil
	}
	if _, err := pageCall(t, h, pageWho, pageEvent(t, tree, "confirm", "confirm")); err == nil {
		t.Fatal("changed confirmation ran")
	}
	if pageStats(t, process)["writes"] != 0 {
		t.Fatal("stale approval executed write")
	}
	pageAwait(t, func() bool { return pageTree(h, tree.Session()).Version() > tree.Version() })
}
