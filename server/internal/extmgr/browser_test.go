package extmgr

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type browserCallSeen struct {
	Method string
	Params map[string]any
}

// egoMac is a Mac whose node answers browser.* as a node with ego lite set up does, and says which it was asked.
func egoMac(t *testing.T, h *Manager, id string) (*Machine, func() []browserCallSeen) {
	t.Helper()
	builtin := fakeNodesBuiltin
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	var mu sync.Mutex
	var seen []browserCallSeen
	var job *proto.ComputerJob
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		method, params = fakeBuiltinRoute(t, builtin, method, params) // either way the hub asks, the same answers (#1830 step 5)
		mu.Lock()
		defer mu.Unlock()
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		seen = append(seen, browserCallSeen{method, p})
		status := proto.Browser{Installed: true, Version: "0.6.2", SetUp: true, Analytics: "off", Ready: true, Checked: time.Now()}
		switch method {
		case "browser.status":
			status.Job = job
			return status, nil
		case "browser.open", "browser.install":
			job = &proto.ComputerJob{Kind: strings.TrimPrefix(method, "browser."), State: "running"}
			status.Job = job
			return status, nil
		case "browser.call":
			if p["look"] == true {
				return proto.ComputerResult{Output: json.RawMessage(`{"snapshot":"[ref=3] link \"More information\"","url":"https://example.com/"}`),
					Image: &proto.Image{Mime: "image/jpeg", Data: "AAAA"}}, nil
			}
			return proto.ComputerResult{Output: json.RawMessage(`{"value":"Example Domain"}`)}, nil
		case "browser.cookies.list", "browser.cookies.import", "browser.cookies.clear":
			out := proto.BrowserCookies{Sites: []proto.BrowserCookieSite{{Domain: "example.com", Count: 2}}}
			if cs, ok := p["cookies"].([]any); ok {
				out.Imported = len(cs)
			}
			if p["all"] == true {
				out.Removed, out.Sites = 2, nil
			}
			return out, nil
		}
		return nil, &rpc.Error{Message: "unknown method " + method}
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(h, MachineConfig{ID: id, Name: id})
	m.peer, m.online, m.info = hubSide, true, proto.NodeInfo{OS: "darwin", BuiltinPrograms: fakeBuiltinPrograms(builtin)}
	h.fleetMu.Lock()
	h.machines, h.byID[id] = append(h.machines, m), m
	h.fleetMu.Unlock()
	return m, func() []browserCallSeen {
		mu.Lock()
		defer mu.Unlock()
		return append([]browserCallSeen(nil), seen...)
	}
}

func TestBrowserResultErrorIsAToolFailure(t *testing.T) {
	for _, raw := range []string{`{"error":"page.snapshot: unsupported scope","url":"https://example.com/"}`,
		`{"error":" script failed ","title":"Example Domain"}`} {
		out := browserResult(&proto.ComputerResult{Output: json.RawMessage(raw), Image: &proto.Image{Mime: "image/png", Data: "AAAA"}})
		if !out.IsError || len(out.Content) != 2 || !strings.Contains(out.Content[0].Text, "error") {
			t.Fatalf("%s: %+v", raw, out)
		}
	}
	for _, raw := range []string{`{"snapshot":"root","title":"Example Domain"}`, `{"error":""}`, `not JSON`} {
		if out := browserResult(&proto.ComputerResult{Output: json.RawMessage(raw)}); out.IsError {
			t.Fatalf("%s: %+v", raw, out)
		}
	}
}

// Off until turned on, then Mac by Mac: the tools, the machine's state, the jobs and the calls.
func TestEgoExtension(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	mux := h.routes()
	mac, seen := egoMac(t, h, "mac")

	if p := h.extensions.reg.Get(egoExtensionID); p == nil || p.Record.Enabled {
		t.Fatalf("built in and off: %+v", p)
	}
	if shownBrowser(t, h, mac) != nil || strings.Contains(toolNames(toolsForAgents(t, h, true)), "browser_") {
		t.Fatal("off: nothing of it")
	}
	h.refreshBrowser(ctx, mac)
	if len(seen()) != 0 {
		t.Fatal("the node is not asked while it is off")
	}

	if err := h.extensions.reg.SetEnabled(ctx, egoExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if h.browserTools() != nil {
		t.Fatal("no tools before a Mac is on")
	}
	if h.portAllowed(mac, screenPort) {
		t.Fatal("not on for this Mac: no live screen")
	}
	code, out := call(t, mux, "PUT", "/api/v1/machines/mac/browser", h.cfg.Token, `{"enabled": true}`)
	if code != 200 || out["enabled"] != true || out["ready"] != true || out["set_up"] != true {
		t.Fatalf("%d %v", code, out)
	}
	if !h.portAllowed(mac, screenPort) || h.portAllowed(mac, 5901) {
		t.Fatal("on for this Mac: its live screen (5900) and only that, without the plugin cua (#1507)")
	}
	if got := toolNames(toolsForAgents(t, h, true)); !strings.Contains(got, "browser_run") || !strings.Contains(got, "browser_look") {
		t.Fatal(got)
	}
	if strings.Contains(toolNames(toolsForAgents(t, h, false)), "browser_") {
		t.Fatal("the assistant's only: agents use ego's own skill on their machine")
	}

	code, out = call(t, mux, "POST", "/api/v1/machines/mac/browser/open", h.cfg.Token, "")
	if job, _ := out["job"].(map[string]any); code != 200 || job["kind"] != "open" {
		t.Fatalf("打开 ego lite: %d %v", code, out)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/browser/reboot", h.cfg.Token, ""); code != 404 {
		t.Fatal("only install, open and cancel")
	}

	look := h.callBrowserTool(ctx, "conv1234-5678", "browser_look", json.RawMessage(`{"machine": "mac"}`))
	if look.IsError || len(look.Content) != 2 || !strings.Contains(look.Content[0].Text, "[ref=3]") || look.Content[1].Type != "image" {
		t.Fatalf("%+v", look)
	}
	run := h.callBrowserTool(ctx, "conv1234-5678", "browser_run", json.RawMessage(`{"machine": "mac", "script": "await page.goto(\"https://example.com\"); return await page.title();"}`))
	if run.IsError || !strings.Contains(run.Content[0].Text, "Example Domain") {
		t.Fatalf("%+v", run)
	}
	var calls []browserCallSeen
	for _, c := range seen() {
		if c.Method == "browser.call" {
			calls = append(calls, c)
		}
	}
	if len(calls) != 2 || calls[0].Params["look"] != true || calls[1].Params["session"] != "persona-conv1234" || calls[1].Params["screenshot"] != true {
		t.Fatalf("the conversation's own task space, a screenshot after a run: %+v", calls)
	}
	if res := h.callBrowserTool(ctx, "c", "browser_run", json.RawMessage(`{"machine": "mac", "script": " "}`)); !res.IsError {
		t.Fatal("an empty script")
	}

	// Asked first, when the user set it: unanswered, nothing runs.
	if err := h.extensions.reg.SetConfig(ctx, egoExtensionID, json.RawMessage(`{"machines": ["mac"], "confirm": true}`), "test"); err != nil {
		t.Fatal(err)
	}
	before := len(seen())
	deskOf(h) // nobody answers on the phone
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	if res := h.callBrowserTool(short, "c", "browser_run", json.RawMessage(`{"machine": "mac", "script": "return 1"}`)); !res.IsError ||
		!strings.Contains(res.Content[0].Text, "declined") {
		t.Fatalf("%+v", res)
	}
	for _, c := range seen()[before:] {
		if c.Method == "browser.call" {
			t.Fatal("not confirmed: not run")
		}
	}

	if err := h.extensions.reg.SetEnabled(ctx, egoExtensionID, false, "test"); err != nil {
		t.Fatal(err)
	}
	if shownBrowser(t, h, mac) != nil || h.browserTools() != nil || h.portAllowed(mac, screenPort) {
		t.Fatal("off again: nothing of it")
	}
}
