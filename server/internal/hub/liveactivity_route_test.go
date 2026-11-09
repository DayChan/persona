package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/relayproto"
)

// A device's push route (push_failed included) is the way of its own APNs token or relay (docs/operations/push-relay.md
// "Which way a device is pushed"). A Live Activity push goes to the activity's own token, so its answer must neither
// hide that the device's alerts fail nor flag alerts that go through.

// splitAPNs answers each token with its own status (none: 200), so a device's token and its activity's can disagree.
type splitAPNs struct {
	mu     sync.Mutex
	status map[string]int
}

func (f *splitAPNs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only pushes count: a node's port probe (GET /) can reach this server too.
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/3/device/") {
		http.NotFound(w, r)
		return
	}
	f.mu.Lock()
	status := f.status[strings.TrimPrefix(r.URL.Path, "/3/device/")]
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"reason":"BadTopic"}`))
	}
}

// pushOutcomes hands over each push result the hub notes (accepted or failed). The hub notes it after the transport
// returned, so the device's route already reflects that push.
func pushOutcomes(h *Hub) <-chan trafficKey {
	ch := make(chan trafficKey, 64)
	h.push.traffic = func(k trafficKey, _ int64, _ time.Duration) {
		if k.action != "attempt" {
			ch <- k
		}
	}
	return ch
}

// nextOutcome waits for the next result of a push from source ("app" for alerts, "liveactivity").
func nextOutcome(t *testing.T, ch <-chan trafficKey, source string) trafficKey {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case k := <-ch:
			if k.source == source {
				return k
			}
		case <-timeout:
			t.Fatalf("no %s push result", source)
		}
	}
}

// sendLiveActivityUpdate gives the phone a running activity with this token and pushes it one update.
func sendLiveActivityUpdate(h *Hub, token string) {
	h.live.update("phone", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act1", token, time.Now() })
	h.live.flush(FleetState{Phase: "working", Working: 1}, false, false)
}

func TestLiveActivitySuccessKeepsDirectAlertFailure(t *testing.T) {
	h := directHub(t, &splitAPNs{status: map[string]int{"devtok": http.StatusBadRequest}})
	h.store = newFleetStartStore()
	results := pushOutcomes(h)
	h.push.notifyDone("m1", "laptop", "s1", "Refactor", "Done", nil)
	if k := nextOutcome(t, results, "app"); k.action != "failed" {
		t.Fatalf("alert %+v", k)
	}
	if r, _ := h.push.routeOfID("phone"); r.Reason != pushFailed {
		t.Fatalf("after a refused alert: %+v", r)
	}

	sendLiveActivityUpdate(h, "acttok")
	if k := nextOutcome(t, results, "liveactivity"); k.action != "accepted" {
		t.Fatalf("live activity %+v", k)
	}
	r, _ := h.push.routeOfID("phone")
	if r.Route != pushDirect || r.Reason != pushFailed || !strings.Contains(r.Detail, "BadTopic") {
		t.Fatalf("an accepted Live Activity push hid the failing alerts: %+v", r)
	}
	// It still shows the app is installed: the device's last accepted push moves on.
	if d, _ := h.push.device("phone"); d.LastPushAt.IsZero() {
		t.Fatal("an accepted Live Activity push did not count as accepted")
	}
}

func TestLiveActivityFailureLeavesDirectRouteClean(t *testing.T) {
	h := directHub(t, &splitAPNs{status: map[string]int{"acttok": http.StatusBadRequest}})
	h.store = newFleetStartStore()
	results := pushOutcomes(h)
	h.push.notifyDone("m1", "laptop", "s1", "Refactor", "Done", nil)
	if k := nextOutcome(t, results, "app"); k.action != "accepted" {
		t.Fatalf("alert %+v", k)
	}

	sendLiveActivityUpdate(h, "acttok")
	if k := nextOutcome(t, results, "liveactivity"); k.action != "failed" || k.status != http.StatusBadRequest {
		t.Fatalf("live activity %+v", k)
	}
	if r, _ := h.push.routeOfID("phone"); r.Route != pushDirect || r.Reason != "" {
		t.Fatalf("a refused Live Activity push flagged working alerts: %+v", r)
	}
}

// Through the relay: a front refuses one push type (400) and lets the other through to the relay.
func TestLiveActivityRelayResultLeavesRoute(t *testing.T) {
	for _, c := range []struct {
		name    string
		refuse  string // the push type the front refuses
		after   string // the route's reason after the alert and the Live Activity update
		alertOK bool
	}{
		{name: "alerts fail", refuse: "alert", after: pushFailed},
		{name: "live activity fails", refuse: "liveactivity", after: "", alertOK: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _, _ := relayHubVia(t, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/v1/push") {
						body, _ := io.ReadAll(r.Body)
						r.Body = io.NopCloser(bytes.NewReader(body))
						var req relayproto.PushRequest
						_ = json.Unmarshal(body, &req)
						if req.Type == c.refuse {
							w.WriteHeader(http.StatusBadRequest)
							_, _ = w.Write([]byte(`{"error":"refused","reason":"BadTopic"}`))
							return
						}
					}
					next.ServeHTTP(w, r)
				})
			})
			h.push.retryWaits = nil
			h.store = newFleetStartStore()
			results := pushOutcomes(h)
			h.push.notifyDone("m1", "laptop", "s1", "Refactor", "Done", nil)
			if k := nextOutcome(t, results, "app"); (k.action == "accepted") != c.alertOK {
				t.Fatalf("alert %+v", k)
			}
			sendLiveActivityUpdate(h, "abcdef0123456789abcdef0123456789")
			if k := nextOutcome(t, results, "liveactivity"); (k.action == "accepted") == c.alertOK {
				t.Fatalf("live activity %+v", k)
			}
			if r, _ := h.push.routeOfID("phone"); r.Route != pushRelay || r.Reason != c.after {
				t.Fatalf("route %+v, want reason %q", r, c.after)
			}
		})
	}
}
