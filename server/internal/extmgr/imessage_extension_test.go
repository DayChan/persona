package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/connector/imessage"
	"github.com/nwverse/persona/server/internal/proto"
)

// imessageHub is a hub with two Macs (mac "Example Mac", mac2 "Another Mac", in Chinese), hub.json's imessage.machine
// set to fromHubJSON ("" none), on store, started.
func imessageHub(t *testing.T, store *extensionStore, fromHubJSON string) *Manager {
	t.Helper()
	h := newTestManager(t)
	h.store = store
	for _, m := range []MachineConfig{{ID: "mac", Name: "示例 Mac"}, {ID: "mac2", Name: "另一台 Mac"}} {
		mm := newMachine(h, m)
		mm.info = proto.NodeInfo{OS: "darwin"}
		h.machines, h.byID[m.ID] = append(h.machines, mm), mm
	}
	if fromHubJSON != "" {
		h.cfg.IMessage = &imessage.Config{Machine: fromHubJSON}
	}
	startTestExtensions(t, h)
	return h
}

func imessageMacOf(h *Manager) string {
	if c := h.imessage(); c != nil {
		return c.Machine()
	}
	return ""
}

// hub.json's imessage.machine moves into Settings › Extensions › Messages once (the name becomes the Mac's id, the
// extension on), and is not put back after the user turns it off; turned off, Messages reads hub.json's again.
func TestIMessageMigratesOnce(t *testing.T) {
	ctx := context.Background()
	store := newExtensionStore()
	h := imessageHub(t, store, "示例 Mac")
	if p := h.extensions.reg.Get(imessageExtensionID); p == nil || !p.Record.Enabled || string(p.Record.Config) != `{"machines":["mac"]}` {
		t.Fatalf("moved: %+v", p)
	}
	if got := imessageMacOf(h); got != "mac" {
		t.Fatalf("reads %q", got)
	}
	var done bool
	if ok, _ := store.Setting(ctx, imessageMigratedKey, &done); !ok || !done {
		t.Fatal("not marked")
	}
	// The user turns it off: Messages goes back to hub.json's.
	if err := h.extensions.reg.SetEnabled(ctx, imessageExtensionID, false, "app"); err != nil {
		t.Fatal(err)
	}
	if got := imessageMacOf(h); got != "示例 Mac" {
		t.Fatalf("off: reads %q, want hub.json's", got)
	}
	// Started again on the same store: not moved again, still off.
	h2 := imessageHub(t, store, "示例 Mac")
	if p := h2.extensions.reg.Get(imessageExtensionID); p.Record.Enabled {
		t.Fatal("turned on again by the move")
	}
	if got := imessageMacOf(h2); got != "示例 Mac" {
		t.Fatalf("again: reads %q", got)
	}
}

// A move that cannot be made leaves Messages on hub.json's: never cut off.
func TestIMessageMigrationThatFailsKeepsHubJSON(t *testing.T) {
	ctx := context.Background()
	// A name that is no machine: nothing moved, not marked (tried at the next start), hub.json's read.
	store := newExtensionStore()
	h := imessageHub(t, store, "no such mac")
	if p := h.extensions.reg.Get(imessageExtensionID); p.Record.Enabled || len(p.Record.Config) > 0 {
		t.Fatalf("moved a name that is no machine: %+v", p.Record)
	}
	var done bool
	if ok, _ := store.Setting(ctx, imessageMigratedKey, &done); ok && done {
		t.Fatal("marked done")
	}
	if got := imessageMacOf(h); got != "no such mac" {
		t.Fatalf("reads %q", got)
	}
	// The database cannot be written: hub.json's read.
	store = newExtensionStore()
	store.failPut = true
	h = imessageHub(t, store, "示例 Mac")
	if got := imessageMacOf(h); got != "示例 Mac" {
		t.Fatalf("write failed: reads %q", got)
	}
	if ok, _ := store.Setting(ctx, imessageMigratedKey, &done); ok && done {
		t.Fatal("marked done after a failed write")
	}
	// Nothing anywhere: no connector, and the app is told where to pick a Mac.
	h = imessageHub(t, newExtensionStore(), "")
	if h.imessage() != nil {
		t.Fatal("a connector with no Mac")
	}
	if code, out := call(t, h.routes(), "GET", "/api/v1/imessage", h.cfg.Token, ""); code != 404 || out["code"] != "not_configured" ||
		!strings.Contains(fmt.Sprint(out), "设置 › 扩展 › 信息") {
		t.Fatalf("not configured: %d %v", code, out)
	}
}

// One Mac only: a second is refused with the one already on named; switching to another is turning one off, then
// the other on; the connector follows at once.
func TestIMessageExtensionPicksTheMac(t *testing.T) {
	h := imessageHub(t, newExtensionStore(), "")
	mux := h.routes()
	put := func(cfg string) (int, map[string]any) {
		return call(t, mux, "PUT", "/api/v1/extensions/imessage/-/config", h.cfg.Token, `{"config":`+cfg+`}`)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/extensions/imessage/-/enabled", h.cfg.Token, `{"enabled":true}`); code != 200 {
		t.Fatalf("on: %d", code)
	}
	if v := viewOf(h, imessageExtensionID); v.Status != extensionNeedsSetup || len(v.NeedsSetup) != 1 || !strings.Contains(v.NeedsSetup[0], "选一台 Mac") {
		t.Fatalf("enabled without a Mac: %+v", v)
	}
	if got := h.extensions.reg.Get(imessageExtensionID).Full.Contributions()["connectors"]; len(got) != 1 || got[0] != imessage.ID {
		t.Fatalf("the settings page must link its connector: %v", got)
	}
	if code, _ := put(`{"machines":["mac"]}`); code != 200 || imessageMacOf(h) != "mac" {
		t.Fatalf("mac: %d %q", code, imessageMacOf(h))
	}
	if v := viewOf(h, imessageExtensionID); v.Status != "running" || len(v.NeedsSetup) != 0 {
		t.Fatalf("a Mac picked: %+v", v)
	}
	code, out := put(`{"machines":["mac","mac2"]}`)
	if code != 400 || out["code"] != "invalid_config" || out["field"] != "machines" || out["message"] != "只能读一台 Mac，先关掉「示例 Mac」" ||
		out["error"] != "只能读一台 Mac，先关掉「示例 Mac」" {
		t.Fatalf("a second Mac: %d %v", code, out)
	}
	if imessageMacOf(h) != "mac" {
		t.Fatal("the refused one changed it")
	}
	if code, _ := put(`{"machines":[]}`); code != 200 || h.imessage() != nil {
		t.Fatalf("none: %d %v", code, h.imessage() != nil)
	}
	if code, _ := put(`{"machines":["mac2"]}`); code != 200 || imessageMacOf(h) != "mac2" {
		t.Fatalf("mac2: %d %q", code, imessageMacOf(h))
	}
	var cfg json.RawMessage = h.extensions.reg.Get(imessageExtensionID).Record.Config
	if string(cfg) != `{"machines":["mac2"]}` {
		t.Fatalf("config %s", cfg)
	}
}
