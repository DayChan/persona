package extmgr

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/quota"
	"github.com/nwverse/persona/server/internal/rpc"
)

// serveUIExtensionForTest draws a page (a metric, a field, a button), a launcher section that reads the gateway through
// the hub, and a page the apps could not draw; it counts the agent.finished events it is told.
func serveUIExtensionForTest() {
	var hub *pserve.Hub
	var finished atomic.Int32
	var seenMu sync.Mutex
	var todoSeen string // the todo events as told (events may come at the same time): "<todo.changed {…}><todo.decision {…}>"
	ui := func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		var p struct {
			Contribution string            `json:"contribution"`
			Action       string            `json:"action"`
			Values       map[string]string `json:"values"`
			Launcher     struct {
				QueryID string `json:"query_id"`
			} `json:"launcher"`
		}
		_ = json.Unmarshal(params, &p)
		var c extension.UIParams
		_ = json.Unmarshal(params, &c)
		switch {
		case method == "action" && p.Action == "save":
			return map[string]any{"toast": "存好了：" + p.Values["note"]}, nil
		case method == "action" && p.Action == "invalidate":
			_ = hub.Notify("ui.invalidate", map[string]any{"contribution": "list"})
			_ = hub.Notify("ui.invalidate", map[string]any{"contribution": "nav"})
			_ = hub.Notify("ui.invalidate", map[string]any{"contribution": "list"})
			return map[string]any{"toast": "ok"}, nil
		case method == "action" && p.Action == "save-detail":
			return map[string]any{"toast": "存好了", "pop": true}, nil
		case method == "action" && p.Action == "push-card":
			return map[string]any{"push": map[string]any{"ui": "card"}}, nil
		case p.Contribution == "list":
			return map[string]any{"title": "笔记 " + c.State,
				"search": map[string]any{"placeholder": "搜索", "action": map[string]any{"id": "search"}},
				"filter": map[string]any{"name": "folder", "value": "all", "action": map[string]any{"id": "folder"},
					"choices": []any{map[string]any{"value": "all", "title": "全部"}, map[string]any{"value": "work", "title": "工作"}}},
				"sections": []any{map[string]any{"title": "最近", "more": map[string]any{"action": map[string]any{"id": "more"}}, "items": []any{
					map[string]any{"type": "row", "key": "n1", "title": "周会", "icon": "doc",
						"action": map[string]any{"id": "open", "push": map[string]any{"ui": "nav", "state": "n1"}}},
					map[string]any{"type": "markdown", "text": "**粗体** 和 [链接](https://example.com)"},
					map[string]any{"type": "table", "columns": []any{map[string]any{"title": "日"}, map[string]any{"title": "数"}},
						"rows": []any{[]any{"11-01", "3"}, []any{"11-02", "4"}}},
					map[string]any{"type": "empty", "title": "没有附件", "button": map[string]any{"title": "上传", "action": map[string]any{"id": "upload"}}},
					map[string]any{"type": "loading"}}}}}, nil
		case p.Contribution == "badpush":
			return map[string]any{"sections": []any{map[string]any{"items": []any{map[string]any{"type": "row", "title": "x",
				"action": map[string]any{"id": "x", "push": map[string]any{"ui": "card"}}}}}}}, nil
		case method == "action" && p.Action == "gateway":
			// Outside the launcher's page: list the launchers, open a session on one, read its gateway.
			var l struct {
				Launchers []struct {
					Name    string `json:"name"`
					BaseURL string `json:"base_url"`
				} `json:"launchers"`
			}
			if err := hub.Call(ctx, "gateway.launchers", map[string]any{}, &l); err != nil {
				return nil, err
			}
			if len(l.Launchers) != 1 || strings.HasSuffix(l.Launchers[0].BaseURL, "/v1") {
				return nil, fmt.Errorf("launchers: %+v", l.Launchers)
			}
			var o struct {
				QueryID string `json:"query_id"`
				BaseURL string `json:"base_url"`
			}
			if err := hub.Call(ctx, "gateway.open", map[string]any{"launcher": l.Launchers[0].Name}, &o); err != nil {
				return nil, err
			}
			var r quota.Response
			if err := hub.Call(ctx, "gateway.http", map[string]any{"query_id": o.QueryID, "url": "/panel",
				"headers": map[string]string{"Authorization": "Bearer " + quota.KeyPlaceholder}}, &r); err != nil {
				return nil, err
			}
			err := hub.Call(ctx, "gateway.open", map[string]any{"launcher": "nosuch"}, &o)
			return map[string]any{"toast": l.Launchers[0].Name + " " + r.Body + " " + fmt.Sprint(err != nil)}, nil
		case method == "action" && p.Action == "badge":
			_ = hub.Notify("ui.badge", map[string]any{"contribution": "nav", "text": p.Values["n"], "tone": "danger"})
			return map[string]any{"toast": "ok"}, nil
		case p.Contribution == "nav":
			said := fmt.Sprintf("%s %s %s %s %s v%d", c.Placement, c.Size, c.TZ, c.Locale, c.Platform, c.UIVersion)
			return map[string]any{"refresh_after": 5, "sections": []any{map[string]any{"items": []any{
				map[string]any{"type": "text", "text": said},
				map[string]any{"type": "chart", "kind": "bar", "unit": "tokens", "series": []any{
					map[string]any{"name": "输入", "points": []any{map[string]any{"label": "周一", "value": 1}, map[string]any{"label": "周二", "value": 2}}},
					map[string]any{"name": "输出", "points": []any{map[string]any{"label": "周一", "value": 3}, map[string]any{"label": "周二", "value": 4}}}}},
				map[string]any{"type": "stat_grid", "stats": []any{map[string]any{"label": "今天", "value": "1.2M"}, map[string]any{"label": "7 天", "value": "8M", "tone": "positive"}}},
				map[string]any{"type": "sparkle", "text": "from a newer hub"}}}}}, nil
		case p.Contribution == "card":
			return map[string]any{"sections": []any{map[string]any{"items": []any{map[string]any{"type": "metric", "label": "今天", "value": "1.2M"}}}}}, nil
		case p.Contribution == "bigcard":
			var items []any
			for range extension.CompactItems + 1 {
				items = append(items, map[string]any{"type": "divider"})
			}
			return map[string]any{"sections": []any{map[string]any{"items": items}}}, nil
		case p.Contribution == "box":
			return map[string]any{"sections": []any{map[string]any{"items": []any{map[string]any{"type": "text", "text": c.Machine.Name + " " + c.Machine.ID}}}}}, nil
		case p.Contribution == "panel":
			var r quota.Response
			if err := hub.Call(ctx, "gateway.http", map[string]any{"query_id": p.Launcher.QueryID, "url": "/panel",
				"headers": map[string]string{"Authorization": "Bearer " + quota.KeyPlaceholder}}, &r); err != nil {
				return nil, err
			}
			return map[string]any{"sections": []any{map[string]any{"items": []any{map[string]any{"type": "text", "text": r.Body}}}}}, nil
		case p.Contribution == "any":
			return nil, &rpc.Error{Code: extension.CodeUnsupported, Message: "not mine"}
		case p.Contribution == "broken":
			return map[string]any{"sections": []any{map[string]any{"items": []any{map[string]any{"type": "link", "title": "x", "url": "http://example.com"}}}}}, nil
		}
		items := []any{
			map[string]any{"type": "metric", "label": "完成的 Agent", "value": fmt.Sprint(finished.Load()), "tone": "positive"},
			map[string]any{"type": "field", "name": "note", "label": "备注", "kind": "text"},
			map[string]any{"type": "button", "title": "保存", "action": map[string]any{"id": "save", "submit": true}}}
		seenMu.Lock()
		seen := todoSeen
		seenMu.Unlock()
		if seen != "" {
			items = append(items, map[string]any{"type": "text", "text": seen})
		}
		return map[string]any{"title": "示例", "state": "page=1", "sections": []any{map[string]any{"title": "概况", "items": items}}}, nil
	}
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "example-ui", Version: cmp.Or(os.Getenv("EXAMPLE_UI_VERSION"), "1.0.0"),
		Init: func(_ context.Context, _ extension.InitializeParams, h *pserve.Hub) (map[string]any, error) {
			hub = h
			return nil, nil
		},
		Namespaces: map[string]pserve.Handler{
			"ui": func(ctx context.Context, method string, params json.RawMessage) (any, error) {
				if method != "render" && method != "action" {
					return nil, pserve.UnknownMethod(method)
				}
				return ui(ctx, method, params)
			},
			"event": func(_ context.Context, method string, params json.RawMessage) (any, error) {
				if method == "agent.finished" || method == "kb.changed" || method == "kb.forgotten" || method == "todo.changed" || method == "todo.decision" {
					finished.Add(1)
				}
				if method == "todo.changed" || method == "todo.decision" {
					seenMu.Lock()
					todoSeen += "<" + method + " " + string(params) + ">"
					seenMu.Unlock()
				}
				return nil, nil
			}}})
}

func uiPackage(t *testing.T) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "ui"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=ui exec "+self+" \"$@\"\n"), 0o755)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-ui", "name": "Example UI", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/ui"}}, "activation_events": ["on_startup"],
		"contributes": {
			"ui": [{"id": "home", "title": "概况", "placement": "plugin_page"},
			       {"id": "broken", "title": "坏的", "placement": "plugin_page"},
			       {"id": "panel", "title": "网关面板", "placement": "launcher_section", "hosts": ["127.0.0.1"]},
			       {"id": "any", "title": "任意网关", "placement": "launcher_section"},
			       {"id": "nav", "title": "用量", "placement": "nav_page", "icon": "chart"},
			       {"id": "card", "title": "用量", "placement": "overview_card", "opens": "nav"},
			       {"id": "bigcard", "title": "太大的卡片", "placement": "overview_card"},
			       {"id": "box", "title": "这台机器", "placement": "machine_section"},
			       {"id": "list", "title": "笔记", "placement": "extension_page"},
			       {"id": "badpush", "title": "乱跳", "placement": "extension_page"}],
			"events": [{"id": "agents", "subscribe": ["agent.finished"]}]},
		"permissions": {"launcher_keys": true}}`, runtime.GOOS+"-"+runtime.GOARCH)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// A plugin's declarative UI: listed where it goes, drawn by the plugin and held to what the apps draw, its actions
// answered; a launcher section reaches its gateway with the key the plugin never sees; events reach it.
func TestExtensionUIAndEvents(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	const key = "sk-example-panel-key"
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "auth=%s", r.Header.Get("Authorization"))
	}))
	defer gw.Close()
	b1Launcher(h, "panellauncher", gw.URL, key) // a launcher added in Persona: the hub holds its key
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+uiPackage(t)+`"}`)
	if !strings.Contains(fmt.Sprint(pv["permissions"]), "收到 hub 的事件") {
		t.Fatalf("the events it is told are shown before installing: %v", pv["permissions"])
	}
	call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	waitLong(t, "the plugin up", func() bool {
		hp := h.extensions.get("example-ui")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})

	b1Emit(h, "agent.finished", map[string]any{"machine_id": "mac", "machine": "Mac", "session_id": "s1", "title": "完成了"})
	_, detail := call(t, mux, "GET", "/api/v1/plugins/example-ui/-/", master, "")
	if ui, _ := detail["ui"].([]any); len(ui) != 5 || strings.Contains(fmt.Sprint(ui), "extension_page") {
		t.Fatalf("its pages (plugin_page, as the old paths call extension_page, and nav_page): %v", detail["ui"])
	}
	var view map[string]any
	waitLong(t, "the event to be counted", func() bool {
		_, out := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/home", master, `{}`)
		view, _ = out["view"].(map[string]any)
		return strings.Contains(fmt.Sprint(view), "value:1")
	})
	if view["state"] != "page=1" || view["title"] != "示例" {
		t.Fatalf("%v", view)
	}
	_, out := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/home", master, `{"action": "save", "values": {"note": "你好"}, "state": "page=1"}`)
	if out["toast"] != "存好了：你好" {
		t.Fatalf("%v", out)
	}
	for _, state := range []string{"ui.session.current", "ui.session.fixture.epoch.1"} {
		code, rendered := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/list", master, fmt.Sprintf(`{"state":%q}`, state))
		if code != http.StatusOK || !strings.Contains(fmt.Sprint(rendered["view"]), state) {
			t.Fatalf("opaque legacy state was treated as a checkpoint: %d %v", code, rendered)
		}
		code, acted := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/home", master, fmt.Sprintf(`{"action":"save","values":{"note":"fixture"},"state":%q}`, state))
		if code != http.StatusOK || acted["toast"] == nil {
			t.Fatalf("legacy action rejected opaque state: %d %v", code, acted)
		}
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/broken", master, `{}`); code != 502 || !strings.Contains(fmt.Sprint(out["error"]), "画不了") {
		t.Fatalf("a view the apps cannot draw is refused: %d %v", code, out)
	}

	_, places := call(t, mux, "GET", "/api/v1/plugins/-/ui?placement=launcher_section&launcher=panellauncher", master, "")
	if list, _ := places["ui"].([]any); len(list) != 2 || fmt.Sprint(list) != fmt.Sprint([]any{list[0], list[1]}) || !strings.Contains(fmt.Sprint(list), "id:panel") || !strings.Contains(fmt.Sprint(list), "id:any") {
		t.Fatalf("the launcher's sections (by host, and one for any gateway): %v", places)
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/any", master, `{"context": {"launcher": "panellauncher"}}`); code != 404 || out["code"] != "unsupported" {
		t.Fatalf("a section for any gateway, not this one's: %d %v", code, out)
	}
	_, out = call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/panel", master, `{"context": {"launcher": "panellauncher"}}`)
	text := fmt.Sprint(out["view"])
	if !strings.Contains(text, "auth=Bearer "+quota.KeyPlaceholder) || strings.Contains(text, key) {
		t.Fatalf("the gateway got the key and the plugin did not: %s", text)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/panel", master, `{}`); code == 200 {
		t.Fatal("a launcher section needs its launcher")
	}
	_, out = call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/nav", master, `{"action": "gateway"}`)
	if got := fmt.Sprint(out["toast"]); got != "panellauncher auth=Bearer "+quota.KeyPlaceholder+" true" {
		t.Fatalf("a page outside the launcher's reaches its gateway with the key it does not see: %v", out)
	}
}

// The slots outside a plugin's settings: listed with their size, icon, page to open and badge; drawn with the app's
// context; a View held to what the asking app draws and to a card's size; shown to the user before installing.
func TestExtensionUISlots(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	m := &Machine{cfg: MachineConfig{ID: "box1", Name: "Studio"}}
	h.machines, h.byID["box1"] = append(h.machines, m), m
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+uiPackage(t)+`"}`)
	shown := fmt.Sprint(pv["permissions"])
	if !strings.Contains(shown, "在 App 里显示自己的界面") || !strings.Contains(shown, "主导航上的入口：用量") ||
		!strings.Contains(shown, "总览上的卡片：用量、太大的卡片") || !strings.Contains(shown, "机器详情页里的一节") {
		t.Fatalf("where it draws is shown before installing: %v", shown)
	}
	call(t, mux, "POST", "/api/v1/plugins/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	waitLong(t, "the plugin up", func() bool {
		hp := h.extensions.get("example-ui")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})

	_, all := call(t, mux, "GET", "/api/v1/plugins/-/ui", master, "")
	list := fmt.Sprint(all["ui"])
	for _, want := range []string{"id:nav", "icon:chart", "size:full", "id:card", "opens:nav", "size:compact", "id:box", "plugin_name:Example UI", "id:home"} {
		if !strings.Contains(list, want) {
			t.Fatalf("every slot's but launchers' (%s): %s", want, list)
		}
	}
	if strings.Contains(list, "id:panel") || strings.Contains(list, "badge") {
		t.Fatalf("no launcher sections, no badge yet: %s", list)
	}
	_, nav := call(t, mux, "GET", "/api/v1/plugins/-/ui?placement=nav_page", master, "")
	if l, _ := nav["ui"].([]any); len(l) != 1 {
		t.Fatalf("one slot: %v", nav)
	}

	_, out := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/nav", master,
		`{"context": {"tz": "Asia/Tokyo", "locale": "zh-Hans-CN", "platform": "web", "ui_version": 2}}`)
	view := fmt.Sprint(out["view"])
	if !strings.Contains(view, "nav_page full Asia/Tokyo zh-Hans-CN web v2") || !strings.Contains(view, "type:chart") ||
		!strings.Contains(view, "type:stat_grid") || strings.Contains(view, "sparkle") || !strings.Contains(view, "refresh_after:30") {
		t.Fatalf("drawn with the app's context, a newer hub's item left out, refresh_after raised to its least: %s", view)
	}
	_, out = call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/nav", master,
		`{"context": {"tz": "Mars/Base", "locale": "<script>", "platform": "fridge"}}`)
	view = fmt.Sprint(out["view"])
	if !strings.Contains(view, "nav_page full") || !strings.Contains(view, " v1") || strings.Contains(view, "Mars") ||
		strings.Contains(view, "script") || strings.Contains(view, "fridge") || strings.Contains(view, "type:chart") || strings.Contains(view, "stat_grid") {
		t.Fatalf("an older app: version 1, what it cannot draw left out; context it made up dropped: %s", view)
	}

	_, out = call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/card", master, `{"context": {"ui_version": 2}}`)
	if !strings.Contains(fmt.Sprint(out["view"]), "1.2M") {
		t.Fatalf("the card: %v", out)
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/bigcard", master, `{"context": {"ui_version": 2}}`); code != 502 || !strings.Contains(fmt.Sprint(out["error"]), "card") {
		t.Fatalf("a card holds %d items: %d %v", extension.CompactItems, code, out)
	}

	if code, out := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/box", master, `{}`); code != 404 {
		t.Fatalf("a machine section needs its machine: %d %v", code, out)
	}
	for _, which := range []string{"box1", "studio"} {
		_, out = call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/box", master, `{"context": {"machine": "`+which+`"}}`)
		if !strings.Contains(fmt.Sprint(out["view"]), "Studio box1") {
			t.Fatalf("the machine, by id or name (%s): %v", which, out)
		}
	}

	call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/nav", master, `{"action": "badge", "values": {"n": "12"}}`)
	waitLong(t, "the badge", func() bool {
		_, nav = call(t, mux, "GET", "/api/v1/plugins/-/ui?placement=nav_page", master, "")
		return strings.Contains(fmt.Sprint(nav["ui"]), "badge:map[text:12 tone:danger]")
	})
	call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/nav", master, `{"action": "badge", "values": {"n": "far too long a badge"}}`)
	call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/nav", master, `{"action": "badge", "values": {"n": ""}}`)
	waitLong(t, "the badge cleared (a too long one dropped)", func() bool {
		_, nav = call(t, mux, "GET", "/api/v1/plugins/-/ui?placement=nav_page", master, "")
		return !strings.Contains(fmt.Sprint(nav["ui"]), "badge")
	})
}

// UI version 3: the new components, search, filter and more reach an app that draws them; an older one gets them
// drawn with what it knows; pushes go to the extension's own whole pages only; ui.invalidate reaches the apps once a
// second, as extension_ui and never plugin_ui; extension_page is plugin_page under the old paths.
func TestExtensionUIVersion3(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	defer func(w time.Duration) { invalidateWait = w }(invalidateWait)
	invalidateWait = 50 * time.Millisecond
	_, pv := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, `{"source": "dir:`+uiPackage(t)+`"}`)
	call(t, mux, "POST", "/api/v1/extensions/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	waitLong(t, "the extension up", func() bool {
		hp := h.extensions.get("example-ui")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})

	_, cur := call(t, mux, "GET", "/api/v1/extensions/-/ui?placement=extension_page", master, "")
	_, old := call(t, mux, "GET", "/api/v1/plugins/-/ui?placement=plugin_page", master, "")
	if c, o := fmt.Sprint(cur["ui"]), fmt.Sprint(old["ui"]); !strings.Contains(c, "id:list") || !strings.Contains(c, "id:home") ||
		strings.Contains(c, "plugin_page") || !strings.Contains(o, "id:list") || strings.Contains(o, "extension_page") {
		t.Fatalf("one slot under both names, each path answering its own:\n%s\n%s", c, o)
	}

	_, out := call(t, mux, "POST", "/api/v1/extensions/example-ui/-/ui/list", master, `{"context": {"ui_version": 3}, "state": "p1"}`)
	view := fmt.Sprint(out["view"])
	for _, want := range []string{"type:markdown", "type:table", "type:empty", "type:loading", "search:map", "filter:map", "more:map",
		"icon:doc", "key:n1", "push:map[state:n1 ui:nav]"} {
		if !strings.Contains(view, want) {
			t.Fatalf("an app of UI version 3 gets %s: %s", want, view)
		}
	}
	_, out = call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/list", master, `{"context": {"ui_version": 2}}`)
	view = fmt.Sprint(out["view"])
	for _, gone := range []string{"markdown", "type:table", "type:empty", "type:loading", "search", "filter", "more:", "icon:", "key:", "push"} {
		if strings.Contains(view, gone) {
			t.Fatalf("an app of UI version 2 does not get %s: %s", gone, view)
		}
	}
	for _, want := range []string{"type:segmented", "title:11-01 type:row value:3", "title:上传 type:button", "title:加载更多 type:button", "加载中…", "**粗体**"} {
		if !strings.Contains(view, want) {
			t.Fatalf("an app of UI version 2 gets %s instead: %s", want, view)
		}
	}
	_, out = call(t, mux, "POST", "/api/v1/plugins/example-ui/-/ui/list", master, `{}`)
	if view = fmt.Sprint(out["view"]); strings.Contains(view, "segmented") || !strings.Contains(view, "加载更多") {
		t.Fatalf("an app of UI version 1: no segmented for the filter, the rest as for 2: %s", view)
	}

	if code, out := call(t, mux, "POST", "/api/v1/extensions/example-ui/-/ui/badpush", master, `{"context": {"ui_version": 3}}`); code != 502 || !strings.Contains(fmt.Sprint(out["error"]), "不是一整页") {
		t.Fatalf("a push to a card is refused: %d %v", code, out)
	}
	if code, out := call(t, mux, "POST", "/api/v1/extensions/example-ui/-/ui/list", master, `{"context": {"ui_version": 3}, "action": "push-card"}`); code != 502 {
		t.Fatalf("an answer pushing a card is refused: %d %v", code, out)
	}
	_, out = call(t, mux, "POST", "/api/v1/extensions/example-ui/-/ui/list", master, `{"context": {"ui_version": 3}, "action": "save-detail"}`)
	if out["pop"] != true || out["toast"] != "存好了" {
		t.Fatalf("pop: %v", out)
	}
	_, out = call(t, mux, "POST", "/api/v1/extensions/example-ui/-/ui/list", master, `{"context": {"ui_version": 2}, "action": "save-detail"}`)
	if _, has := out["pop"]; has {
		t.Fatalf("no pop for an app that does not know it: %v", out)
	}

	uiTold := func() []map[string]any {
		th := hubOf(h)
		th.mu.Lock()
		defer th.mu.Unlock()
		var out []map[string]any
		for _, m := range th.broadcasts {
			if t, _ := m["type"].(string); strings.HasPrefix(t, "plugin_ui") || strings.HasPrefix(t, "extension_ui") {
				if m["invalidate"] == true {
					out = append(out, m)
				}
			}
		}
		return out
	}
	before := len(uiTold())
	call(t, mux, "POST", "/api/v1/extensions/example-ui/-/ui/list", master, `{"action": "invalidate"}`)
	eventually(t, 10*time.Second, "the invalidations told", func() bool { return len(uiTold()) >= before+2 })
	time.Sleep(200 * time.Millisecond)
	got := uiTold()[before:]
	if len(got) > 2 {
		t.Fatalf("three ui.invalidate of two pages in a second are two messages: %v", got)
	}
	if fmt.Sprint(got) != "[map[extension:example-ui id:list invalidate:true type:extension_ui] map[extension:example-ui id:nav invalidate:true type:extension_ui]]" {
		t.Fatalf("one extension_ui per page, no plugin_ui: %v", got)
	}
}

func TestCheckView(t *testing.T) {
	ratio := 0.4
	ok := extension.View{Sections: []extension.Section{{Items: []extension.UIElem{
		{Type: "text", Text: "x"}, {Type: "progress", Label: "l", Ratio: &ratio, Tone: "caution"},
		{Type: "row", Title: "r", Badge: &extension.Badge{Text: "b", Tone: "danger"}, Action: &extension.UIAction{ID: "a"}},
		{Type: "link", Title: "l", URL: "https://example.com"}, {Type: "field", Name: "f", Label: "F", Kind: "choice", Choices: []extension.UIChoice{{Value: "1"}}},
		{Type: "divider"},
		{Type: "chart", Kind: "bar", Unit: "tokens", Stacked: true, Series: []extension.ChartSeries{
			{Name: "in", Tone: "positive", Points: []extension.ChartPoint{{Label: "一", Value: 1}, {Label: "二", Value: -2, Display: "-2"}}},
			{Name: "out", Points: []extension.ChartPoint{{Label: "一", Value: 3}, {Label: "二", Value: 4}}}}},
		{Type: "stat_grid", Stats: []extension.Stat{{Label: "a", Value: "1"}, {Label: "b", Value: "2", Detail: "+3%", Tone: "positive"}}},
		{Type: "segmented", Name: "range", Value: "7d", Action: &extension.UIAction{ID: "range"}, Choices: []extension.UIChoice{{Value: "1d", Title: "今天"}, {Value: "7d", Title: "7 天"}}}}}}}
	if err := extension.CheckView(&ok); err != nil {
		t.Fatal(err)
	}
	bad := map[string]extension.UIElem{
		"unknown type":   {Type: "html"},
		"tone":           {Type: "metric", Label: "a", Value: "b", Tone: "#ff0000"},
		"http link":      {Type: "link", Title: "x", URL: "http://example.com"},
		"secret value":   {Type: "field", Name: "s", Label: "S", Kind: "secret", Value: "leak"},
		"button":         {Type: "button", Title: "x"},
		"ratio":          {Type: "progress", Label: "x", Ratio: func() *float64 { v := 1.5; return &v }()},
		"long text":      {Type: "text", Text: strings.Repeat("长", extension.StringMost+1)},
		"empty choices":  {Type: "field", Name: "c", Label: "C", Kind: "choice"},
		"action without": {Type: "row", Title: "x", Action: &extension.UIAction{}},
		"chart kind":     {Type: "chart", Kind: "pie", Series: []extension.ChartSeries{{Points: []extension.ChartPoint{{Label: "a", Value: 1}}}}},
		"chart empty":    {Type: "chart", Kind: "bar"},
		"chart labels": {Type: "chart", Kind: "line", Series: []extension.ChartSeries{
			{Name: "a", Points: []extension.ChartPoint{{Label: "x", Value: 1}}}, {Name: "b", Points: []extension.ChartPoint{{Label: "y", Value: 1}}}}},
		"chart unnamed": {Type: "chart", Kind: "line", Series: []extension.ChartSeries{
			{Points: []extension.ChartPoint{{Label: "x", Value: 1}}}, {Points: []extension.ChartPoint{{Label: "x", Value: 1}}}}},
		"chart tone":    {Type: "chart", Kind: "bar", Series: []extension.ChartSeries{{Tone: "blue", Points: []extension.ChartPoint{{Label: "x"}}}}},
		"segment one":   {Type: "segmented", Name: "r", Action: &extension.UIAction{ID: "r"}, Choices: []extension.UIChoice{{Value: "a"}}, Value: "a"},
		"segment value": {Type: "segmented", Name: "r", Action: &extension.UIAction{ID: "r"}, Choices: []extension.UIChoice{{Value: "a"}, {Value: "b"}}, Value: "c"},
		"segment act":   {Type: "segmented", Name: "r", Choices: []extension.UIChoice{{Value: "a"}, {Value: "b"}}, Value: "a"},
		"stats empty":   {Type: "stat_grid"},
		"stats many":    {Type: "stat_grid", Stats: make([]extension.Stat, extension.StatsMost+1)},
		"table cells":   {Type: "table", Columns: []wire.TableColumn{{Title: "a"}, {Title: "b"}}, Rows: [][]string{{"1"}}},
		"row pictures":  {Type: "row", Title: "x", Icon: "doc", Avatar: &wire.Avatar{Text: "A"}},
		"avatar empty":  {Type: "row", Title: "x", Avatar: &wire.Avatar{}},
		"md http link":  {Type: "markdown", Text: "[x](http://example.com)"},
		"md image":      {Type: "markdown", Text: "![x](https://example.com/a.png)"},
		"md too long":   {Type: "markdown", Text: strings.Repeat("长", extension.MarkdownMost+1)},
		"empty button":  {Type: "empty", Title: "x", Button: &wire.EmptyButton{Title: "y"}},
		"push state":    {Type: "row", Title: "x", Action: &extension.UIAction{ID: "x", Push: &extension.Push{State: strings.Repeat("s", extension.StateMost+1)}}},
	}
	md := extension.View{Sections: []extension.Section{{Items: []extension.UIElem{{Type: "markdown", Text: "a `![not](an image)` b, [ok](https://example.com)"}}}}}
	if err := extension.CheckView(&md); err != nil {
		t.Errorf("an https link: %v", err)
	}
	twice := extension.View{Sections: []extension.Section{{Items: []extension.UIElem{{Type: "text", Text: "a", Key: "k"}, {Type: "text", Text: "b", Key: "k"}}}}}
	if extension.CheckView(&twice) == nil {
		t.Error("a key twice: drawn")
	}
	filter := extension.View{Sections: []extension.Section{}, Filter: &wire.Filter{Name: "f", Value: "z", Action: extension.UIAction{ID: "f"},
		Choices: []extension.UIChoice{{Value: "a"}, {Value: "b"}}}}
	if extension.CheckView(&filter) == nil {
		t.Error("a filter's value not among its choices: drawn")
	}
	for name, e := range bad {
		if err := extension.CheckView(&extension.View{Sections: []extension.Section{{Items: []extension.UIElem{e}}}}); err == nil {
			t.Errorf("%s: drawn", name)
		}
	}
}
