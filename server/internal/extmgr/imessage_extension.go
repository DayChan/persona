package extmgr

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/nwverse/persona/server/internal/connector/imessage"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The built-in extension Messages (#1830 step 6, docs/plans/plan-extensions.md "How iMessage uses it"): which Mac's Messages
// the connector imessage reads, picked in Settings › Extensions › Messages instead of manager.json's imessage.machine. The
// connector is the one it always was (its id, inbox, knowledge base, "Hand to Persona", "Delete data"); only where its
// Mac comes from changed:
//
//   - the extension on with a Mac picked: that Mac;
//   - else manager.json's imessage.machine, as before (kept one version, read-only, never written);
//   - else no connector.
//
// hub.json's value moves into the extension once (migrateIMessage), marked so a Mac the user turns off later is not
// put back. A move that cannot be made is logged and the connector goes on with hub.json's: iMessage is never cut off.
// The Mac's node answers over plugin.node.call {id: imessage} when it says it has the built-in program, by the old
// connector.* names when it does not (Machine.callBuiltinProgram).

const imessageExtensionID = "imessage"

// imessageMigratedKey marks manager.json's imessage.machine moved into the extension (or found nothing to move).
const imessageMigratedKey = "imessage.migrated_to_extension"

const imessageConfig = `{
  "type": "object",
  "properties": {
    "machines": {"type": "array", "title": "读哪台 Mac", "description": "打开的那台 Mac 上的「信息」由 hub 只读读取（只能开一台：同一个 Apple ID 的 Mac 里是同一份）。开关即时生效。要先在那台 Mac 上给 personad 完全磁盘访问权限，见 设置 › 连接器 › 信息", "items": {"type": "string", "format": "machine", "x-os": ["darwin"]}, "uniqueItems": true, "maxItems": 1}
  }
}`

func imessageManifest(h *Manager) *extension.Manifest {
	connector, _ := json.Marshal([]extension.ConnectorItem{{ID: imessage.ID, Name: imessage.NewRemote(imessage.Config{}, "", nil).Name(),
		Capabilities: []string{"status", "tools", "events", "inbox", "archive", "machines"}}})
	return &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: imessageExtensionID, Name: "信息（iMessage）", Version: h.version,
		Description: "读你一台 Mac 上「信息」App 的短信和 iMessage（只读，不发送），交给收件箱、知识库和助手。读哪台在这里选；「交给 Persona」和删除已存的数据在 设置 › 连接器 › 信息。",
		Contributes: map[string]json.RawMessage{"connectors": connector}, Configuration: json.RawMessage(imessageConfig), Node: builtinNode("machines")}
}

// imessageExtensionMachines is the Macs picked in the extension's settings, and whether it is on.
func (h *Manager) imessageExtensionMachines() ([]string, bool) {
	if h.extensions == nil || h.extensions.reg == nil {
		return nil, false
	}
	p := h.extensions.reg.Get(imessageExtensionID)
	if p == nil {
		return nil, false
	}
	var cfg struct {
		Machines []string `json:"machines"`
	}
	_ = json.Unmarshal(p.Record.Config, &cfg)
	return cfg.Machines, p.Record.Enabled
}

// imessageMachine is the Mac the connector should read now ("" none): the extension's, else hub.json's.
func (h *Manager) imessageMachine() string {
	if machines, on := h.imessageExtensionMachines(); on && len(machines) > 0 {
		return machines[0]
	}
	if im := h.cfg.IMessage; im.Configured() {
		return im.Machine
	}
	return ""
}

// migrateIMessage moves manager.json's imessage.machine into the extension, once: the Mac's id in its settings, the
// extension on. Nothing is moved when the extension already has a Mac. A name that matches no machine, or a write
// that fails, is logged and tried again at the next start; meanwhile the connector reads hub.json's.
func (h *Manager) migrateIMessage(ctx context.Context) {
	im := h.cfg.IMessage
	if !im.Configured() || h.store == nil || h.extensions == nil || h.extensions.reg == nil {
		return
	}
	var done bool
	if ok, err := h.store.Setting(ctx, imessageMigratedKey, &done); err == nil && ok && done {
		return
	}
	if machines, _ := h.imessageExtensionMachines(); len(machines) > 0 {
		_ = h.store.PutSetting(ctx, imessageMigratedKey, true) // the user picked one already: theirs stands
		return
	}
	id := h.machineIDFor(im.Machine)
	if id == "" {
		h.logf("imessage: manager.json's imessage.machine names no machine of this hub; 信息 reads it from manager.json")
		return
	}
	reg := h.extensions.reg
	raw, _ := json.Marshal(map[string]any{"machines": []string{id}})
	if err := reg.SetConfig(ctx, imessageExtensionID, raw, "hub"); err != nil {
		h.logf("imessage: moving manager.json's machine into 设置 › 扩展 › 信息: %v; 信息 reads it from manager.json", err)
		return
	}
	if err := reg.SetEnabled(ctx, imessageExtensionID, true, "hub"); err != nil {
		h.logf("imessage: turning on 设置 › 扩展 › 信息: %v; 信息 reads it from manager.json", err)
		return
	}
	if err := h.store.PutSetting(ctx, imessageMigratedKey, true); err != nil {
		h.logf("imessage: marking the move done: %v", err)
	}
	h.logf("imessage: manager.json's imessage.machine moved to 设置 › 扩展 › 信息 (manager.json's is read-only now)")
}

// applyIMessage makes the connector read the Mac it should (imessageMachine): registered with it, taken out when
// there is none, made again when the Mac changed.
func (h *Manager) applyIMessage() {
	want := h.imessageMachine()
	cur := h.imessage()
	if cur != nil && want != "" && cur.Machine() == want {
		return
	}
	if cur != nil {
		h.connectors.Remove(imessage.ID)
	}
	if want == "" {
		return
	}
	if err := h.addIMessage(imessage.Config{Machine: want}); err != nil {
		h.logf("imessage connector: %v", err)
	}
}

// An enabled extension without a Mac is not running yet: its form is the next step, and the connector appears once
// one is picked. Do not turn hub.json's configured fallback into a setup failure.
func (ps *extensions) imessageView(ctx context.Context, p *extension.Extension, v *ExtensionView) {
	if p.Record.ID != imessageExtensionID || !p.Record.Enabled || v.Status == extensionIncompatible || v.Status == host.StateError {
		return
	}
	if machines, _ := ps.h.imessageExtensionMachines(); len(machines) > 0 || ps.h.cfg.IMessage.Configured() {
		return
	}
	v.Status, v.NeedsSetup = extensionNeedsSetup, append(v.NeedsSetup, i18n.T(ps.h.contextLang(ctx), "hub-imessage_extension.no_mac"))
}

// imessageOneMac refuses a second Mac for Messages with the one already on named: one Mac only (the same Apple ID's Macs
// hold the same messages, read twice they would come twice).
func (h *Manager) imessageOneMac(raw json.RawMessage) *extension.FieldError {
	var next struct {
		Machines []string `json:"machines"`
	}
	if json.Unmarshal(raw, &next) != nil || len(next.Machines) <= 1 {
		return nil
	}
	cur, _ := h.imessageExtensionMachines()
	other := next.Machines[0]
	for _, m := range next.Machines {
		if slices.Contains(cur, m) {
			other = m
			break
		}
	}
	name := other
	if m := h.localMachine(other); m != nil {
		name = m.cfg.Name
	}
	return &extension.FieldError{Path: "machines", Message: i18n.T(h.userLang(), "hub-imessage_extension.one_mac", i18n.Args{"name": name})}
}

var errNoIMessageMac = i18n.Err("hub-imessage_extension.no_mac")
