package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCallApps(t *testing.T) {
	for _, c := range []struct {
		name, app, script, steps string
		want                     string
		whole                    bool
	}{
		{"computer_state", "", "", "", wholeScreen, true},
		{"computer_state", "Calculator", "", "", "Calculator", true},
		{"computer_run", "", `const a = await cua.getApp("Calculator"); const b = await cua.getApp('Notes');`, "", "Calculator,Notes", true},
		{"computer_run", "", `const a = await cua.getApp(name);`, "", "", false},
		{"computer_run", "", `const a = await cua.getApp("Calculator"); const b = await cua.getApp(other);`, "", "Calculator", false},
		{"computer_run", "", `return 1`, "", "", false},
		{"computer_run", "", "", `[{"click": {"app": "Calculator", "name": "6"}}, {"click": {"app": "Calculator", "name": "7"}}]`, "Calculator", true},
		{"computer_run", "Notes", "", `[{"tool": "press_key", "args": {"key": "return"}}]`, "Notes", true},
		{"computer_run", "", "", `[{"tool": "press_key", "args": {"key": "return"}}]`, "", false},
		// An app the call opens is one it touches, not the call's default app.
		{"computer_run", "Calculator", "", `[{"launch_app": {"name": "Terminal"}}]`, "Terminal", true},
		{"computer_run", "Calculator", "", `[{"tool": "launch_app", "args": {"name": "Terminal"}}, {"click": {"name": "7"}}]`, "Terminal,Calculator", true},
		{"computer_run", "", "", `[{"launch_app": {"bundle_id": "com.apple.Terminal"}}]`, "com.apple.Terminal", true},
		{"computer_run", "Calculator", "", `[{"launch_app": {}}, {"click": {"name": "7"}}]`, "Calculator", false},
		{"computer_run", "Calculator", "", `[{"tool": "launch_app"}]`, "", false},
		{"computer_run", "", `const a = await cua.getApp("Calculator"); await cua.launch("Terminal");`, "", "Calculator,Terminal", true},
		{"computer_run", "", `await cua.launch('Terminal'); return 1`, "", "Terminal", true},
		{"computer_run", "", `const a = await cua.getApp("Calculator"); await cua.launch(name);`, "", "Calculator", false},
	} {
		apps, whole := callApps(c.name, c.app, c.script, json.RawMessage(c.steps))
		if strings.Join(apps, ",") != c.want || whole != c.whole {
			t.Errorf("%s %q %q %q: %v %v, want %q %v", c.name, c.app, c.script, c.steps, apps, whole, c.want, c.whole)
		}
	}
}

// The first time an app is operated on a Mac the phone is asked; "Always allow" keeps it (by bundle id) until removed. A
// terminal is asked about every time, with no "Always allow", and so is a script whose apps cannot all be read.
func TestComputerAppsAreAskedOnce(t *testing.T) {
	h, _ := extensionTestHub(t)
	h.opts.AssistantMachine = func() string { return "assistant-box" } // where the assistant's conversations run
	ctx := context.Background()
	mux := h.routes()
	_, seen := computerMac(t, h, "mac")
	deskOf(h) // the cards wait on the phone (the hub's queue) until answered
	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(`{"machines": ["mac"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	run := func(steps string) chan proto.ToolResult {
		out := make(chan proto.ToolResult, 1)
		go func() {
			out <- h.callComputerTool(ctx, "conv1234", "computer_run", json.RawMessage(`{"machine": "mac", "steps": `+steps+`}`))
		}()
		return out
	}
	calc := `[{"click": {"app": "Calculator", "name": "6"}}]`

	done := run(calc)
	card := answerCard(t, h, true, true)
	if !strings.Contains(card.Summary, "「Calculator」") || card.AlwaysLabel != "以后操作「Calculator」不再问" {
		t.Fatalf("asked about Calculator, with 一直允许: %+v", card)
	}
	// In the conversation that operates the Mac: there the chat shows the card (#2558).
	if card.SessionID != "conv1234" || card.MachineID != "assistant-box" {
		t.Fatalf("card of %s/%s, not the assistant's conversation", card.MachineID, card.SessionID)
	}
	if res := <-done; res.IsError {
		t.Fatalf("%+v", res)
	}
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if res := h.callComputerTool(short, "conv1234", "computer_run", json.RawMessage(`{"machine": "mac", "steps": `+calc+`}`)); res.IsError {
		t.Fatalf("always allowed: not asked again: %+v", res)
	}
	// Calculator is allowed, but a call in it that opens Terminal asks about Terminal, every time.
	for _, args := range []string{
		`{"machine": "mac", "app": "Calculator", "steps": [{"launch_app": {"name": "Terminal"}}]}`,
		`{"machine": "mac", "script": "const a = await cua.getApp(\"Calculator\"); await cua.launch(\"Terminal\"); return 1"}`,
	} {
		done := make(chan proto.ToolResult, 1)
		go func() { done <- h.callComputerTool(ctx, "conv1234", "computer_run", json.RawMessage(args)) }()
		if card := answerCard(t, h, true, false); !strings.Contains(card.Summary, "「Terminal」") || strings.Contains(card.Summary, "Calculator") || card.AlwaysLabel != "" {
			t.Fatalf("%s: asked about Terminal alone, with no 一直允许: %+v", args, card)
		}
		if res := <-done; res.IsError {
			t.Fatalf("%+v", res)
		}
	}
	code, out := call(t, mux, "GET", "/api/v1/machines/mac/computer/apps", h.cfg.Token, "")
	apps, _ := out["apps"].([]any)
	if code != 200 || len(apps) != 1 || apps[0].(map[string]any)["bundle_id"] != "com.apple.calculator" || apps[0].(map[string]any)["name"] != "Calculator" {
		t.Fatalf("kept by bundle id: %d %v", code, out)
	}

	term := `[{"click": {"app": "Terminal", "name": "x"}}]`
	for range 2 {
		done = run(term)
		if card = answerCard(t, h, true, true); card.AlwaysLabel != "" {
			t.Fatalf("a terminal: asked every time, never kept: %+v", card)
		}
		<-done
	}
	go func() { _ = answerCard(t, h, false, false) }()
	if res := h.callComputerTool(ctx, "conv1234", "computer_run", json.RawMessage(`{"machine": "mac", "script": "const a = await cua.getApp(name); return 1"}`)); !res.IsError ||
		!strings.Contains(res.Content[0].Text, "declined") {
		t.Fatalf("a script naming its apps by expression: asked, declined: %+v", res)
	}

	if code, out := call(t, mux, "DELETE", "/api/v1/machines/mac/computer/apps/com.apple.calculator", h.cfg.Token, ""); code != 200 || len(out["apps"].([]any)) != 0 {
		t.Fatalf("removed: %d %v", code, out)
	}
	done = run(calc)
	answerCard(t, h, true, false)
	<-done

	if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(`{"machines": ["mac"], "app_approval": false}`), "test"); err != nil {
		t.Fatal(err)
	}
	_, before := seen()
	if res := h.callComputerTool(short, "conv1234", "computer_run", json.RawMessage(`{"machine": "mac", "steps": `+term+`}`)); res.IsError {
		t.Fatalf("turned off: nothing asked: %+v", res)
	}
	if _, after := seen(); len(after) != len(before)+1 {
		t.Fatal("turned off: no list_apps either, only the call")
	}
}
