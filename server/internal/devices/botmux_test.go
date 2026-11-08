package devices

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
)

// botmuxCalls lists what each node.configure said about botmux.
func botmuxCalls(seen []map[string]any) []bool {
	var out []bool
	for _, c := range seen {
		if c["method"] != "node.configure" {
			continue
		}
		if v, ok := c["botmux"].(bool); ok {
			out = append(out, v)
		}
	}
	return out
}

// Unset everywhere, botmux stays on: a hub upgraded without anyone touching the switch lists botmux as before.
func TestBotmuxDefaultsOn(t *testing.T) {
	h := newTestDevices(t)
	herdrMac(t, h, "mac", false)
	mux := h.Handler(http.NotFoundHandler())
	if code, out := call(t, mux, "GET", "/api/v1/machines/mac/botmux", ""); code != 200 || out["on"] != true {
		t.Fatalf("default: %d %v", code, out)
	}
}

// The switch answers for one machine: hub.json is only its default, the choice is kept in settings, and the node is
// told as soon as it is made.
func TestBotmuxSwitchPerMachine(t *testing.T) {
	h := newTestDevices(t)
	off := false
	m, seen := herdrMac(t, h, "mac", false)
	m.cfg.Botmux = &off
	mux := h.Handler(http.NotFoundHandler())

	if code, out := call(t, mux, "GET", "/api/v1/machines/mac/botmux", ""); code != 200 || out["on"] != false {
		t.Fatalf("hub.json's value is the default: %d %v", code, out)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/machines/mac/botmux", `{}`); code != 400 {
		t.Fatalf("a body without on: %d", code)
	}
	if code, out := call(t, mux, "PUT", "/api/v1/machines/mac/botmux", `{"on": true}`); code != 200 || out["on"] != true {
		t.Fatalf("turned on: %d %v", code, out)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if got := botmuxCalls(seen()); len(got) > 0 && got[len(got)-1] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the node was not told: %v", botmuxCalls(seen()))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if code, out := call(t, mux, "GET", "/api/v1/machines/mac/botmux", ""); code != 200 || out["on"] != true {
		t.Fatalf("stored choice wins over hub.json: %d %v", code, out)
	}
	var stored bool
	if ok, err := h.opts.Store.Setting(context.Background(), botmuxKey("mac"), &stored); !ok || err != nil || !stored {
		t.Fatalf("kept in settings: ok=%v err=%v value=%v", ok, err, stored)
	}
	// The next connection's node.configure carries the stored choice, not hub.json's.
	if !m.botmux(context.Background()) {
		t.Fatal("configureNode would send hub.json's off")
	}
	if code, out := call(t, mux, "PUT", "/api/v1/machines/mac/botmux", `{"on": false}`); code != 200 || out["on"] != false {
		t.Fatalf("turned off: %d %v", code, out)
	}
}

// The hub relays only devicewire.Routes to the manager (hub/devices_routes.go): a route served here but missing there
// is a 404 on a real hub, which the apps take for an older hub and hide the switch.
func TestWireRoutesAreServed(t *testing.T) {
	h := newTestDevices(t)
	herdrMac(t, h, "mac", false)
	fellThrough := false
	mux := h.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { fellThrough = true }))
	for _, want := range []string{"GET /api/v1/machines/{mid}/botmux", "PUT /api/v1/machines/{mid}/botmux"} {
		if !slices.Contains(devicewire.Routes, want) {
			t.Errorf("%s is not relayed by the hub", want)
		}
	}
	for _, pattern := range devicewire.Routes {
		method, path, _ := strings.Cut(pattern, " ")
		if method == "POST" || strings.HasSuffix(path, "/deploy") {
			continue // they act on the machine; that they are routed is enough to know from the PUTs and GETs
		}
		fellThrough = false
		req := httptest.NewRequest(method, strings.ReplaceAll(path, "{mid}", "mac"), strings.NewReader("{}"))
		mux.ServeHTTP(httptest.NewRecorder(), req)
		if fellThrough {
			t.Errorf("%s is relayed but not served", pattern)
		}
	}
}
