package hub

import (
	"fmt"
	"strings"
	"testing"
)

// A direct push that stays over APNs' 4 KB even shortened (a background push with a big field of its own, as an
// extension's hub.push_background sends) is not sent, since APNs would refuse it with 413, and the device's route
// says it failed and why.
func TestDirectOversizePushIsNotSentAndIsRecorded(t *testing.T) {
	h, f := testLive(t)
	attempts := pushAttempts(h)
	h.push.sendAs(map[string]any{"aps": map[string]any{"content-available": 1}, "blob": strings.Repeat("x", 5000)},
		"", "background", "5", nil)
	if n := attempts.Load(); n != 0 {
		t.Fatalf("the hub started %d pushes over APNs' limit", n)
	}
	r, _ := h.push.routeOfID("phone")
	var n int
	if _, err := fmt.Sscanf(r.Detail, "push of %d bytes", &n); err != nil || r.Route != pushDirect || r.Reason != pushFailed ||
		n <= 5000 || !strings.Contains(r.Detail, "4096") || r.FailedAt == nil {
		t.Fatalf("route after an oversize push: %+v", r)
	}
	// A push that fits goes and clears it.
	h.push.sendAs(map[string]any{"aps": map[string]any{"content-available": 1}}, "", "background", "5", nil)
	f.calls(t, 1)
	waitRoute(t, h.push, func(r pushRoute) bool { return r.Route == pushDirect && r.Reason == "" })
}

// A push the hub cannot seal for the relay (its body does not fit even shortened) is not sent, never in the clear, and
// the device's route says it failed: it used to keep saying relay with nothing wrong while every such push was dropped.
func TestRelayUnsealablePushIsRecorded(t *testing.T) {
	h, _, _ := relayHub(t)
	attempts := pushAttempts(h)
	h.push.sendAs(map[string]any{"aps": map[string]any{"content-available": 1}, "blob": strings.Repeat("x", 3000)},
		"", "background", "5", nil)
	if n := attempts.Load(); n != 0 {
		t.Fatalf("the hub started %d relay pushes it could not seal", n)
	}
	if r, _ := h.push.routeOfID("phone"); r.Route != pushRelay || r.Reason != pushFailed || !strings.HasPrefix(r.Detail, "seal: ") ||
		!strings.Contains(r.Detail, "does not fit") {
		t.Fatalf("route after an unsealable push: %+v", r)
	}
}
