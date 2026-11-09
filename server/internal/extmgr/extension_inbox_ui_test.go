package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/rpc"
)

// serveInboxUIExtensionForTest has a connector (examplechat) and draws the page of its inbox items: it writes back what
// the hub told it of the item, and its "Handled" says which item it was pressed on.
func serveInboxUIExtensionForTest() {
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-chat", Version: "1.0.0",
		Namespaces: map[string]pserve.Handler{
			"connector": func(_ context.Context, method string, _ json.RawMessage) (any, error) {
				if method == "status" {
					return map[string]string{"state": "ok"}, nil
				}
				return nil, pserve.UnknownMethod(method)
			},
			"ui": func(_ context.Context, method string, params json.RawMessage) (any, error) {
				var p extension.UIParams
				_ = json.Unmarshal(params, &p)
				if p.Item == nil {
					return nil, fmt.Errorf("no item")
				}
				it := p.Item
				switch {
				case method == "action" && p.Action == "source":
					return map[string]any{"open_url": extension.InboxSourceURL(it.ID)}, nil
				case method == "action" && p.Action == "another's source":
					return map[string]any{"open_url": extension.InboxSourceURL("examplechat:2")}, nil
				case method == "action" && p.Action == "script":
					return map[string]any{"open_url": "javascript:alert(1)"}, nil
				case method == "action" && p.Action == "web":
					return map[string]any{"open_url": "https://example.com/chat/1"}, nil
				case method == "action":
					return map[string]any{"toast": p.Action + " " + it.ID}, nil
				case method == "render" && it.Extra["doc"] != nil:
					// not an item it draws (no meta to draw from): the apps keep the item's own detail
					return nil, &rpc.Error{Code: extension.CodeUnsupported, Message: "not an event"}
				case method == "render":
					extra := slices.Sorted(func(yield func(string) bool) {
						for k := range it.Extra {
							if !yield(k) {
								return
							}
						}
					})
					return map[string]any{"sections": []any{map[string]any{"items": []any{
						map[string]any{"type": "text", "text": fmt.Sprintf("%s %s|%s|%s|%s|%s|done=%v", p.Placement, it.ID, it.Title, it.Summary, it.Tier, it.Reason, it.Done)},
						map[string]any{"type": "text", "text": "meta=" + string(it.Meta)},
						map[string]any{"type": "text", "text": "extra=" + strings.Join(extra, ",")},
						map[string]any{"type": "button", "title": "已处理", "action": map[string]any{"id": "done"}}}}}}, nil
				}
				return nil, pserve.UnknownMethod(method)
			}}})
}

// It follows the inbox's common rules (docs/extensions/extensions.md): triage_rules, and its own fallback so no model is needed.
func inboxUIPackage(t *testing.T) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "chat"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=inboxui exec "+self+" \"$@\"\n"), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-chat", "name": "Example Chat", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/chat"}}, "activation_events": ["on_startup"],
		"configuration": {"type": "object", "properties": {"triage_rules": {"type": "string", "title": "分拣规则"}}},
		"contributes": {
			"connectors": [{"id": "examplechat", "name": "Example Chat", "capabilities": ["status", "inbox", "fallback"]}],
			"ui": [{"id": "event", "title": "事件", "placement": "inbox_item", "connector": "examplechat"}]}}`, runtime.GOOS+"-"+runtime.GOARCH)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// An inbox item's page drawn by the plugin whose connector brought the item in: listed with its connector, drawn
// from the item the manager keeps (its triage, whether it is done, its source block), for that connector's items only.
func TestExtensionInboxItemUI(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+inboxUIPackage(t)+`"}`)
	if !strings.Contains(fmt.Sprint(pv["permissions"]), "收件箱里它的连接器的条目详情（会读到那一条的标题、总结和分级）：事件") {
		t.Fatalf("drawing inbox items is shown before installing: %v", pv["permissions"])
	}
	call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	waitLong(t, "the plugin up", func() bool {
		hp := h.extensions.get("example-chat")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})

	_, places := call(t, mux, "GET", "/api/v1/plugins/-/ui?placement=inbox_item", master, "")
	list, _ := places["ui"].([]any)
	if len(list) != 1 {
		t.Fatalf("one inbox_item: %v", places)
	}
	place := list[0].(map[string]any)
	if place["plugin"] != "example-chat" || place["id"] != "event" || place["connector"] != "examplechat" || place["size"] != "full" || place["placement"] != "inbox_item" {
		t.Fatalf("listed with its connector: %v", place)
	}
	if _, all := call(t, mux, "GET", "/api/v1/plugins/-/ui", master, ""); !strings.Contains(fmt.Sprint(all["ui"]), "connector:examplechat") {
		t.Fatalf("and among every slot's: %v", all)
	}

	// The items as the manager keeps them (inbox.item answers them).
	fm := b1InboxItems(h)
	done, _ := json.Marshal(time.Now())
	fm.mu.Lock()
	fm.items["examplechat:1"] = json.RawMessage(`{"id":"examplechat:1","source":"examplechat","kind":"dm","title":"周会","summary":"周四下午开会",
		"tier":"action","reason":"要你确认时间","context":"（会话）","connector":{"thread":"chat:1","label":"私聊"}}`)
	fm.items["examplechat:2"] = json.RawMessage(`{"id":"examplechat:2","source":"examplechat","title":"旧的","done_at":` + string(done) + `,"done_by":"user"}`)
	fm.items["mail:1"] = json.RawMessage(`{"id":"mail:1","source":"mail","kind":"mail","title":"别人的","mail":{"account_id":"a1"}}`)
	fm.items["examplechat:doc"] = json.RawMessage(`{"id":"examplechat:doc","source":"examplechat","title":"文档","doc":{"notice_id":"n1"}}`)
	fm.mu.Unlock()

	code, out := call(t, mux, "POST", "/api/v1/plugins/example-chat/-/ui/event", master, `{"context": {"item": "examplechat:1", "ui_version": 2}}`)
	view := fmt.Sprint(out["view"])
	if code != 200 || !strings.Contains(view, "inbox_item examplechat:1|周会|周四下午开会|action|要你确认时间|done=false") ||
		!strings.Contains(view, "meta=null") || !strings.Contains(view, "extra=connector") || strings.Contains(view, "（会话）") {
		t.Fatalf("drawn from the item (no meta yet; its connector block): %d %s", code, view)
	}
	if _, out = call(t, mux, "POST", "/api/v1/plugins/example-chat/-/ui/event", master, `{"context": {"item": "examplechat:2"}}`); !strings.Contains(fmt.Sprint(out["view"]), "done=true") {
		t.Fatalf("a done item: %v", out)
	}
	if _, out = call(t, mux, "POST", "/api/v1/plugins/example-chat/-/ui/event", master, `{"context": {"item": "examplechat:1"}, "action": "done"}`); out["toast"] != "done examplechat:1" {
		t.Fatalf("an action is told the item too: %v", out)
	}
	// What an action opens: https, or this item's own source page; nothing else.
	for action, want := range map[string]string{"source": "persona://inbox/examplechat:1/source", "web": "https://example.com/chat/1"} {
		if code, out := call(t, mux, "POST", "/api/v1/plugins/example-chat/-/ui/event", master, `{"context": {"item": "examplechat:1"}, "action": "`+action+`"}`); code != 200 || out["open_url"] != want {
			t.Fatalf("%s opens %s: %d %v", action, want, code, out)
		}
	}
	for _, action := range []string{"another's source", "script"} {
		if code, out := call(t, mux, "POST", "/api/v1/plugins/example-chat/-/ui/event", master, `{"context": {"item": "examplechat:1"}, "action": "`+action+`"}`); code != 502 || out["open_url"] != nil {
			t.Fatalf("%s is refused: %d %v", action, code, out)
		}
	}
	for body, want := range map[string]struct {
		code int
		err  string
	}{
		`{}`:                                       {404, "not_found"},
		`{"context": {"item": "nothing:1"}}`:       {404, "not_found"},
		`{"context": {"item": "mail:1"}}`:          {403, "forbidden"},
		`{"context": {"item": "examplechat:doc"}}`: {404, "unsupported"},
	} {
		if code, out := call(t, mux, "POST", "/api/v1/plugins/example-chat/-/ui/event", master, body); code != want.code || out["code"] != want.err {
			t.Fatalf("%s: want %d %s, got %d %v", body, want.code, want.err, code, out)
		}
	}

	// The connector is not the plugin's any more (gone from the registry): not listed, not drawn.
	h.connectors.Remove("examplechat")
	if _, places = call(t, mux, "GET", "/api/v1/plugins/-/ui?placement=inbox_item", master, ""); len(places["ui"].([]any)) != 0 {
		t.Fatalf("a connector the plugin does not serve now: %v", places)
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/example-chat/-/ui/event", master, `{"context": {"item": "examplechat:1"}}`); code != 404 || out["code"] != "unsupported" {
		t.Fatalf("%d %v", code, out)
	}
}

// The item goes to the plugin as its JSON says: meta as it is stored, whatever adds it to the item; the source blocks
// it has; done when it has a done_at.
func TestUIItemOf(t *testing.T) {
	raw := `{"id": "lark:F1:1051", "source": "lark", "title": "排期", "summary": "定周四", "tier": "fyi", "done_at": "2026-11-04T10:00:00Z",
		"preview": "p", "context": "secret text", "meta": {"chat": "F1", "messages": [{"id": "m1051", "pos": "1051"}]},
		"lark": {"feed_id": "F1", "start_position": "1051"}, "doc": null, "count": 5, "started_at": "2026-11-04T09:30:00Z"}`
	it, source, ok := uiItemOf([]byte(raw))
	if !ok || source != "lark" || it.ID != "lark:F1:1051" || it.Title != "排期" || it.Summary != "定周四" || it.Tier != "fyi" || !it.Done {
		t.Fatalf("%v %s %+v", ok, source, it)
	}
	if it.Count != 5 || !it.StartedAt.Equal(time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)) {
		t.Fatalf("the hub's count and start: %d %v", it.Count, it.StartedAt)
	}
	if string(it.Meta) != `{"chat": "F1", "messages": [{"id": "m1051", "pos": "1051"}]}` {
		t.Fatalf("meta as it is: %s", it.Meta)
	}
	if len(it.Extra) != 1 || string(it.Extra["lark"]) != `{"feed_id": "F1", "start_position": "1051"}` {
		t.Fatalf("the blocks it has: %v", it.Extra)
	}
	b, _ := json.Marshal(it)
	if strings.Contains(string(b), "secret text") || strings.Contains(string(b), "preview") {
		t.Fatalf("nothing else: %s", b)
	}
	it, _, _ = uiItemOf([]byte(`{"id": "x:1", "source": "x"}`))
	if b, _ = json.Marshal(it); !strings.Contains(string(b), `"meta":null`) || strings.Contains(string(b), "extra") || it.Done ||
		strings.Contains(string(b), "count") || strings.Contains(string(b), "started_at") {
		t.Fatalf("no meta is null, no blocks none: %s", b)
	}
}

// Only an inbox_item opens the item's source page, and only its own item's; anything else is https.
func TestCheckOpenURL(t *testing.T) {
	inbox := extension.UIItem{ID: "event", Placement: "inbox_item", Connector: "lark"}
	page := extension.UIItem{ID: "home", Placement: "plugin_page"}
	if u := extension.InboxSourceURL("lark:F1:1051"); u != "persona://inbox/lark:F1:1051/source" {
		t.Fatal(u)
	}
	if u := extension.InboxSourceURL("x/y z"); u != "persona://inbox/x%2Fy%20z/source" {
		t.Fatal(u)
	}
	for _, ok := range []struct {
		ui   extension.UIItem
		item string
		url  string
	}{{inbox, "lark:F1:1051", "persona://inbox/lark:F1:1051/source"}, {inbox, "x/y z", "persona://inbox/x%2Fy%20z/source"},
		{inbox, "lark:F1:1051", "https://example.com/a"}, {page, "", "https://example.com/a"}, {page, "", ""}} {
		if err := checkOpenURL("x", ok.ui, ok.item, ok.url); err != nil {
			t.Errorf("%s %s %s: %v", ok.ui.Placement, ok.item, ok.url, err)
		}
	}
	for _, bad := range []struct {
		ui   extension.UIItem
		item string
		url  string
	}{{page, "lark:F1:1051", "persona://inbox/lark:F1:1051/source"}, {inbox, "lark:F1:1051", "persona://inbox/lark:F1:9999/source"},
		{inbox, "", "persona://inbox//source"}, {inbox, "lark:F1:1051", "persona://inbox/lark:F1:1051/source?x=1"},
		{inbox, "lark:F1:1051", "persona://settings"}, {inbox, "lark:F1:1051", "http://example.com"}, {inbox, "lark:F1:1051", "javascript:alert(1)"}} {
		if err := checkOpenURL("x", bad.ui, bad.item, bad.url); err == nil {
			t.Errorf("%s %s %s: allowed", bad.ui.Placement, bad.item, bad.url)
		}
	}
}
