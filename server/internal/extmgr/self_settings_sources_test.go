package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// sourcesHub is a manager with two built-in plugins: one whose settings declare triage_rules, one without.
func sourcesHub(t *testing.T) *Manager {
	t.Helper()
	h := newTestManager(t)
	reg := extension.NewRegistry(t.TempDir(), nil, nil, 0)
	add := func(id, name, schema string) {
		m := &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: id, Name: name, Version: "1.0.0",
			Engines: extension.Engines{ExtensionProtocol: extension.Protocol}, Configuration: json.RawMessage(schema)}
		if err := reg.AddBuiltin(m, "/opt/example", true); err != nil {
			t.Fatal(err)
		}
	}
	add("example-chat", "示例聊天", `{"type":"object","properties":{"account":{"type":"integer","default":0},
		"triage_rules":{"type":"string","title":"分拣规则"}}}`)
	add("example-other", "示例其他", `{"type":"object","properties":{"rules":{"type":"string"}}}`)
	if err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.extensions = newExtensions(t.TempDir(), nil, h.logf)
	h.extensions.reg = reg
	if err := reg.SetConfig(context.Background(), "example-chat", json.RawMessage(`{"account":2}`), "test"); err != nil {
		t.Fatal(err)
	}
	return h
}

// A plugin that declares triage_rules has an entry; writing it is a card on the phone (before → after), and only
// approved does it set that one setting, the others kept; "" clears it.
func TestSelfSettingsSourceTriageRules(t *testing.T) {
	h := sourcesHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The list is the assistant's, with the hub's settings and the manager's (the hub asks for the manager's:
	// extwire.HubSettingsList).
	out, err := h.hubCall(ctx, extwire.HubSettingsList, nil)
	raw, _ := json.Marshal(out)
	list := string(raw)
	if err != nil || !strings.Contains(list, `"key":"inbox.rules.example-chat","title":"示例聊天的分拣规则"`) || strings.Contains(list, "inbox.rules.example-other") {
		t.Fatalf("list: %v\n%s", err, list)
	}
	if got, err := h.toolPersonaSettings(ctx, "server", "s1", json.RawMessage(`{"action":"get","key":"inbox.rules.example-chat"}`)); err != nil || got != "（无）" {
		t.Fatalf("get: %q %v", got, err)
	}
	// The same through the hub's hand-over (extwire.HubSettingsTool).
	if out, err := h.hubCall(ctx, extwire.HubSettingsTool, json.RawMessage(`{"machine_id":"server","session_id":"s1",
		"arguments":{"action":"get","key":"inbox.rules.example-chat"}}`)); err != nil || out.(map[string]any)["text"] != "（无）" {
		t.Fatalf("get through the hub: %v %v", out, err)
	}

	set := func(allow bool, value string) (string, proto.Interaction) {
		t.Helper()
		// The card is the hub's (interactions.wait): the phone answers it.
		th := hubOf(h)
		var ix proto.Interaction
		th.mu.Lock()
		th.answer = func(asked proto.Interaction) proto.Resolution {
			ix = asked
			return proto.Resolution{Allow: &allow}
		}
		th.mu.Unlock()
		out, err := h.toolPersonaSettings(ctx, "server", "s1", json.RawMessage(`{"action":"set","key":"inbox.rules.example-chat","value":`+value+`}`))
		if err != nil {
			out = "error: " + err.Error()
		}
		if ix.ID == "" || ix.MachineID != "server" || ix.SessionID != "s1" || ix.ToolName != "persona_settings" || ix.Kind != proto.IxPermission {
			t.Fatalf("the card: %+v", ix)
		}
		return out, ix
	}
	config := func() map[string]any {
		var c map[string]any
		_ = json.Unmarshal(h.extensions.reg.Get("example-chat").Record.Config, &c)
		return c
	}

	out2, ix := set(false, `"机器人发的都算噪音"`)
	if ix.Summary != "改示例聊天的分拣规则：（无） → 机器人发的都算噪音" || !strings.Contains(out2, "declined") || config()["triage_rules"] != nil {
		t.Fatalf("declined: %q %q %v", ix.Summary, out2, config())
	}
	out2, _ = set(true, `"机器人发的都算噪音"`)
	if c := config(); c["triage_rules"] != "机器人发的都算噪音" || c["account"] != float64(2) || !strings.Contains(out2, "updated") {
		t.Fatalf("approved: %q %v", out2, c)
	}
	if got, _ := h.toolPersonaSettings(ctx, "server", "s1", json.RawMessage(`{"action":"get","key":"inbox.rules.example-chat"}`)); got != "机器人发的都算噪音" {
		t.Fatalf("get after: %q", got)
	}
	if out, _ := h.toolPersonaSettings(ctx, "server", "s1", json.RawMessage(`{"action":"set","key":"inbox.rules.example-chat","value":"机器人发的都算噪音"}`)); !strings.Contains(out, "没有变化") {
		t.Fatalf("unchanged: %q", out)
	}
	if _, ix = set(true, `""`); !strings.HasSuffix(ix.Summary, "→ （无）") {
		t.Fatalf("clear card: %q", ix.Summary)
	}
	if c := config(); c["triage_rules"] != nil || c["account"] != float64(2) {
		t.Fatalf("cleared: %v", c)
	}
}
