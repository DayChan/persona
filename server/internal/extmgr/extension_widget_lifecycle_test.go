package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
)

// takeWidgetPushes checks the wire payload as well as the exact target of every wake.
func takeWidgetPushes(t *testing.T, h *Manager) map[string]int {
	t.Helper()
	th := hubOf(h)
	th.mu.Lock()
	defer th.mu.Unlock()
	out := map[string]int{}
	for _, p := range th.pushes {
		payload, _ := p["payload"].(map[string]any)
		widget, ok := payload["extension_widget"].(map[string]any)
		if !ok {
			continue
		}
		aps, _ := payload["aps"].(map[string]any)
		devices, _ := p["devices"].([]any)
		if aps["content-available"] != float64(1) || len(devices) != 1 || len(widget) != 2 {
			t.Fatalf("widget background push: %v", p)
		}
		out[fmt.Sprint(devices[0])+"/"+fmt.Sprint(widget["extension"])+"/"+fmt.Sprint(widget["id"])]++
	}
	th.pushes = nil
	return out
}

func waitWidgetProcess(t *testing.T, h *Manager) {
	t.Helper()
	hp := h.extensions.get("example-sys")
	if hp == nil || hp.proc == nil {
		t.Fatal("widget extension has no process")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := hp.proc.Wait(ctx); err != nil {
		t.Fatalf("widget extension readiness: %v", err)
	}
}

func TestWidgetLifecycleInvalidatesViewsAndWakesReportedDevices(t *testing.T) {
	for _, change := range []string{"disable", "uninstall", "config"} {
		t.Run(change, func(t *testing.T) {
			h, _ := extensionTestHub(t)
			dir := systemPackage(t)
			installForTest(t, h, dir)
			waitWidgetProcess(t, h)
			mux := h.routes()
			request := func(method, path, body string) map[string]any {
				t.Helper()
				code, out := call(t, mux, method, path, h.cfg.Token, body)
				if code != 200 {
					t.Fatalf("%s %s: %d %v", method, path, code, out)
				}
				return out
			}
			draw := func(want string) {
				t.Helper()
				out := request("POST", "/api/v1/extensions/example-sys/-/widget/today", `{"context":{"size":"small","ui_version":3}}`)
				if !strings.Contains(fmt.Sprint(out["view"]), "value:"+want) {
					t.Fatalf("render count %s: %v", want, out)
				}
			}
			draw("1")
			draw("1")
			request("PUT", "/api/v1/devices/phone/widgets", `{"widgets":[
				{"extension":"example-sys","id":"today","family":"small"},
				{"extension":"example-sys","id":"today","family":"accessory_inline"},
				{"extension":"example-sys","id":"removed","family":"small"},
				{"extension":"other-extension","id":"today","family":"small"}]}`)
			request("PUT", "/api/v1/devices/watch/widgets", `{"widgets":[{"extension":"example-sys","id":"today","family":"accessory_inline"}]}`)
			request("PUT", "/api/v1/devices/other-phone/widgets", `{"widgets":[{"extension":"other-extension","id":"today","family":"small"}]}`)

			// Content invalidations retain their ordinary throttle and manifest scope.
			h.widgetsChanged("example-sys", map[string]bool{"": true})
			if got, want := takeWidgetPushes(t, h), (map[string]int{"phone/example-sys/today": 1, "watch/example-sys/today": 1}); !reflect.DeepEqual(got, want) {
				t.Fatalf("content wake: got %v, want %v", got, want)
			}
			draw("2")
			h.widgetsChanged("example-sys", map[string]bool{"": true})
			if got := takeWidgetPushes(t, h); len(got) != 0 {
				t.Fatalf("repeated content invalidation bypassed throttle: %v", got)
			}
			draw("3")

			w := &h.extensions.widgets
			other := widgetCacheKey("other-extension", "today", extension.UIParams{Size: "small"})
			w.mu.Lock()
			for _, key := range []string{
				widgetCacheKey("example-sys", "today", extension.UIParams{Size: "accessory_inline", Locale: "ja"}),
				widgetCacheKey("example-sys", "removed", extension.UIParams{Size: "small"}), other,
			} {
				w.views[key] = widgetView{expires: time.Now().Add(time.Hour)}
			}
			// Lifecycle wakes must also recover reports stored by an earlier manager run.
			w.devices, w.loaded = nil, false
			w.mu.Unlock()

			switch change {
			case "disable":
				request("PUT", "/api/v1/extensions/example-sys/-/enabled", `{"enabled":false}`)
			case "uninstall":
				request("DELETE", "/api/v1/extensions/example-sys/-/", "")
				if h.extensions.reg.Get("example-sys") != nil {
					t.Fatal("uninstalled extension remains in registry")
				}
			case "config":
				request("PUT", "/api/v1/extensions/example-sys/-/config", `{"config":{}}`)
			}
			want := map[string]int{"phone/example-sys/today": 1, "phone/example-sys/removed": 1, "watch/example-sys/today": 1}
			if got := takeWidgetPushes(t, h); !reflect.DeepEqual(got, want) {
				t.Fatalf("lifecycle wake after a recent content wake: got %v, want %v", got, want)
			}
			w.mu.Lock()
			_, keptOther := w.views[other]
			left := len(w.views)
			loaded := w.loaded
			w.mu.Unlock()
			if left != 1 || !keptOther || !loaded {
				t.Fatalf("clear only this extension and reload stored reports: views=%d, other=%v, loaded=%v", left, keptOther, loaded)
			}
			if change != "config" {
				if got := request("GET", "/api/v1/extensions/-/widgets", ""); strings.Contains(fmt.Sprint(got["widgets"]), "example-sys") {
					t.Fatalf("inactive widget is still listed: %v", got)
				}
				if code, out := call(t, mux, "POST", "/api/v1/extensions/example-sys/-/widget/today", h.cfg.Token, `{"context":{"size":"small"}}`); code != 404 {
					t.Fatalf("inactive widget render: %d %v", code, out)
				}
			}
			switch change {
			case "disable":
				request("PUT", "/api/v1/extensions/example-sys/-/enabled", `{"enabled":true}`)
			case "uninstall":
				installForTest(t, h, dir)
			}
			waitWidgetProcess(t, h)
			// Before expiry, the replacement process must render instead of serving count 3.
			draw("1")
			draw("1")
		})
	}
}

func TestWidgetUpgradeWakesReportsAfterDeclarationsChange(t *testing.T) {
	for _, removed := range []bool{false, true} {
		t.Run(fmt.Sprintf("removed=%v", removed), func(t *testing.T) {
			h, _ := extensionTestHub(t)
			dir := systemPackage(t)
			installForTest(t, h, dir)
			if err := h.extensions.reg.SetEnabled(t.Context(), "example-sys", false, "test"); err != nil {
				t.Fatal(err)
			}
			if err := h.setDeviceWidgets(t.Context(), "phone", []wire.DeviceWidget{{Extension: "example-sys", ID: "today", Family: "small"}}); err != nil {
				t.Fatal(err)
			}
			w := &h.extensions.widgets
			w.mu.Lock()
			w.views = map[string]widgetView{widgetCacheKey("example-sys", "today", extension.UIParams{Size: "small"}): {expires: time.Now().Add(time.Hour)}}
			w.mu.Unlock()
			path := filepath.Join(dir, extension.ManifestFile)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			manifest["version"] = "1.1.0"
			if removed {
				contributes := manifest["contributes"].(map[string]any)
				contributes["ui"] = contributes["ui"].([]any)[:1]
			}
			raw, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			pv, err := h.extensions.reg.Preview(t.Context(), "dir:"+dir, "example-sys")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.extensions.reg.Upgrade(t.Context(), "example-sys", pv.ID, pv.Digest, "test"); err != nil {
				t.Fatal(err)
			}
			if got, want := takeWidgetPushes(t, h), (map[string]int{"phone/example-sys/today": 1}); !reflect.DeepEqual(got, want) {
				t.Fatalf("upgrade wake: got %v, want %v", got, want)
			}
			w.mu.Lock()
			left := len(w.views)
			w.mu.Unlock()
			if left != 0 {
				t.Fatalf("upgrade left %d cached widget views", left)
			}
		})
	}
}
