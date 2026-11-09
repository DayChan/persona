package hub

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestDeliverNotifyNoDevice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Hub)
	}{
		{"no pusher", func(h *Hub) { h.push = nil }},
		{"no registration", func(*Hub) {}},
		{"token without transport", func(h *Hub) {
			h.push.register(Device{DeviceID: "phone", APNSToken: "test-token"})
		}},
		{"direct without token", func(h *Hub) {
			h.push.direct = &apns.Client{}
			h.push.register(Device{DeviceID: "phone"})
		}},
		{"relay without key", func(h *Hub) {
			h.push.register(Device{DeviceID: "phone", RelayURL: "https://relay.example.com", RelayGrant: "test-grant"})
		}},
		{"preferences without transport", func(h *Hub) {
			if err := h.push.setPrefs("phone", NotifyPrefs{Input: false}); err != nil {
				t.Fatal(err)
			}
		}},
		{"client without notify capability", func(h *Hub) {
			h.clients[&client{out: make(chan []byte, 1)}] = struct{}{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			tc.setup(h)
			got, err := h.deliverNotify(notifyRequest{Title: "Build finished", Level: levelActive}, "", "", "")
			if err != nil || got["sent"] != false || got["reason"] != "no_device" {
				t.Fatalf("notify = %v, %v; want not sent with no_device", got, err)
			}
		})
	}
}

func TestDeliverNotifyFiltered(t *testing.T) {
	for _, transport := range []string{"apns", "websocket", "both"} {
		for _, tc := range []struct {
			name    string
			prefs   NotifyPrefs
			desktop bool
		}{
			{name: "needs you off", prefs: NotifyPrefs{Input: false}},
			{name: "manager muted", prefs: NotifyPrefs{Input: true, MutedManagers: []string{"manager-example"}}},
			{name: "phone muted", prefs: NotifyPrefs{Input: true, MuteWhileDesktop: true}, desktop: true},
		} {
			t.Run(transport+"/"+tc.name, func(t *testing.T) {
				h, f := testLive(t)
				if transport == "websocket" {
					h.push.direct = nil
				}
				if transport != "apns" {
					c := &client{device: "phone", out: make(chan []byte, 1), meta: clientMeta{features: map[string]bool{"ws_notify": true}}}
					h.clients[c] = struct{}{}
				}
				if tc.desktop {
					h.clients[&client{desktop: true}] = struct{}{}
				}
				h.push.desktopOnline = h.desktopOnline
				if err := h.push.setPrefs("phone", tc.prefs); err != nil {
					t.Fatal(err)
				}
				got, err := h.deliverNotify(notifyRequest{Title: "Build finished", Level: levelActive}, "", "", "manager-example")
				if err != nil || got["sent"] != false || got["reason"] != "filtered" {
					t.Fatalf("notify = %v, %v; want not sent with filtered", got, err)
				}
				for c := range h.clients {
					if len(c.out) != 0 {
						t.Fatal("filtered notification reached a WebSocket")
					}
				}
				if calls := f.calls(t, 0); len(calls) != 0 {
					t.Fatalf("filtered notification reached APNs: %v", calls)
				}
			})
		}
	}
}

func TestDeliverNotifyEitherChannel(t *testing.T) {
	for _, tc := range []struct {
		name string
		push bool
		ws   bool
	}{
		{"push selected and websocket filtered", true, false},
		{"websocket selected and push filtered", false, true},
		{"both selected", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, f := testLive(t)
			c := &client{device: "android", out: make(chan []byte, 1), meta: clientMeta{features: map[string]bool{"ws_notify": true}}}
			h.clients[c] = struct{}{}
			for id, input := range map[string]bool{"phone": tc.push, "android": tc.ws} {
				if err := h.push.setPrefs(id, NotifyPrefs{Input: input}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := h.deliverNotify(notifyRequest{Title: "Build finished", Level: levelActive}, "", "", "")
			if err != nil || got["sent"] != true || got["reason"] != nil {
				t.Fatalf("notify = %v, %v; want sent", got, err)
			}
			if tc.push {
				f.calls(t, 1)
			}
			if (len(c.out) == 1) != tc.ws {
				t.Fatalf("WebSocket notifications = %d, wanted notification: %v", len(c.out), tc.ws)
			}
		})
	}
}

func TestDeliverNotifyOpenDesktopFiltered(t *testing.T) {
	h, f := testLive(t)
	h.push.register(Device{DeviceID: "phone", Kind: kindMac, APNSToken: "devtok", Topic: "app.example"})
	h.clients[&client{desktop: true, pushDevice: "phone"}] = struct{}{}
	h.push.desktopOpen = h.desktopOpen
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true}); err != nil {
		t.Fatal(err)
	}
	got, err := h.deliverNotify(notifyRequest{Title: "Build finished", Level: levelActive}, "", "", "")
	if err != nil || got["sent"] != false || got["reason"] != "filtered" {
		t.Fatalf("open desktop = %v, %v; want not sent with filtered", got, err)
	}
	if calls := f.calls(t, 0); len(calls) != 0 {
		t.Fatalf("open desktop received an APNs alert: %v", calls)
	}
}

func TestNotifyPushRecipientCounts(t *testing.T) {
	build := func(i18n.Lang) map[string]any {
		return notifyPayload(notifyRequest{Title: "Build finished", Level: levelActive}, "", "")
	}
	var absent *Pusher
	if selected, reachable := absent.sendFrom("", build, "", nil, quietNone, nil); selected != 0 || reachable != 0 {
		t.Fatalf("nil pusher counts = %d, %d", selected, reachable)
	}
	for _, transport := range []string{"apns", "relay"} {
		t.Run(transport, func(t *testing.T) {
			var h *Hub
			var wait func()
			if transport == "apns" {
				var f *fakeAPNs
				h, f = testLive(t)
				wait = func() { f.calls(t, 1) }
			} else {
				var f *relayAPNs
				h, f, _ = relayHub(t)
				wait = func() { f.wait(t, 1) }
			}
			// Preferences alone do not make a device reachable on either push transport.
			if err := h.push.setPrefs("unregistered", NotifyPrefs{Input: true}); err != nil {
				t.Fatal(err)
			}
			selected, reachable := h.push.sendFrom("", build, "", pushWants{Kind: "input"}.fn(), quietNone, nil)
			if selected != 1 || reachable != 1 {
				t.Fatalf("push counts = %d, %d; want 1, 1", selected, reachable)
			}
			wait()
			if err := h.push.setPrefs("phone", NotifyPrefs{Input: false}); err != nil {
				t.Fatal(err)
			}
			selected, reachable = h.push.sendFrom("", build, "", pushWants{Kind: "input"}.fn(), quietNone, nil)
			if selected != 0 || reachable != 1 {
				t.Fatalf("filtered push counts = %d, %d; want 0, 1", selected, reachable)
			}
		})
	}
}

func TestNotifyWSRecipientCounts(t *testing.T) {
	h := newTestHub(t)
	for _, id := range []string{"selected", "filtered", "legacy"} {
		c := &client{device: id, out: make(chan []byte, 1), meta: clientMeta{features: map[string]bool{"ws_notify": id != "legacy"}}}
		h.clients[c] = struct{}{}
		if err := h.push.setPrefs(id, NotifyPrefs{Input: id != "filtered"}); err != nil {
			t.Fatal(err)
		}
	}
	build := func(i18n.Lang) map[string]any {
		return notifyPayload(notifyRequest{Title: "Build finished", Level: levelActive}, "", "")
	}
	selected, reachable := h.notifyWSFrom("", kindAlert, build, pushWants{Kind: "input"}.fn(), quietNone)
	if selected != 1 || reachable != 2 {
		t.Fatalf("WebSocket counts = %d, %d; want 1, 2", selected, reachable)
	}
	for c := range h.clients {
		select {
		case raw := <-c.out:
			var msg map[string]any
			if err := json.Unmarshal(raw, &msg); err != nil || c.device != "selected" || msg["kind"] != kindAlert {
				t.Fatalf("WebSocket recipient %s got %s (%v)", c.device, raw, err)
			}
		default:
			if c.device == "selected" {
				t.Fatal("selected WebSocket got no notification")
			}
		}
	}
}
