package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
)

// A phone whose relay registration failed says why when it registers, and the hub, which has no apns.json, answers
// that its pushes go nowhere and why (#5998): it used to send empty relay fields and nobody knew.
func TestDeviceRegistrationSaysWhichWay(t *testing.T) {
	h := newTestHub(t)
	mux := h.routes()
	register := func(body string) map[string]any {
		t.Helper()
		code, out := call(t, mux, "POST", "/api/v1/devices", h.cfg.Token, body)
		if code != 200 {
			t.Fatalf("register: %d %v", code, out)
		}
		push, _ := out["push"].(map[string]any)
		return push
	}
	got := register(`{"device_id":"phone","name":"iPhone","kind":"iphone","apns_token":"aa","apns_env":"production","topic":"app.example",
		"relay_error":"403 topic not served"}`)
	if got["route"] != "none" || got["reason"] != "relay_failed" || got["detail"] != "403 topic not served" {
		t.Fatalf("relay refused: %v", got)
	}
	got = register(`{"device_id":"phone","name":"iPhone","kind":"iphone","topic":"app.example"}`)
	if got["route"] != "none" || got["reason"] != "no_token" {
		t.Fatalf("no token: %v", got)
	}
	got = register(`{"device_id":"phone","name":"iPhone","kind":"iphone","apns_token":"aa","topic":"app.example"}`)
	if got["route"] != "none" || got["reason"] != "no_relay" {
		t.Fatalf("an app that names no relay: %v", got)
	}
	// A grant wins over whatever error came with it, and the error is not kept.
	got = register(`{"device_id":"phone","name":"iPhone","kind":"iphone","apns_token":"aa","topic":"app.example",
		"push_key":"pk","relay_url":"https://relay.example/relay","relay_grant":"g","relay_error":"stale"}`)
	if got["route"] != "relay" || got["reason"] != nil {
		t.Fatalf("relayed: %v", got)
	}
	if d, _ := h.push.device("phone"); d.RelayError != "" {
		t.Fatalf("kept %q beside a grant", d.RelayError)
	}
	// What a phone says is one bounded line.
	register(`{"device_id":"phone","name":"iPhone","kind":"iphone","apns_token":"aa","relay_error":"` + strings.Repeat("x\\n", 400) + `"}`)
	if d, _ := h.push.device("phone"); len(d.RelayError) > relayErrorMax+len("…") || strings.Contains(d.RelayError, "\n") {
		t.Fatalf("relay error %d bytes: %q", len(d.RelayError), d.RelayError)
	}
}

// With the hub's own APNs key every phone with a token goes direct, whatever its relay said.
func TestDirectHubRoutesDirect(t *testing.T) {
	h := newTestHub(t)
	h.push.direct = &apns.Client{}
	h.push.register(Device{DeviceID: "phone", APNSToken: "aa", RelayError: "403 topic not served"})
	if r, _ := h.push.routeOfID("phone"); r.Route != "direct" || r.Reason != "" {
		t.Fatalf("route %+v", r)
	}
	h.push.register(Device{DeviceID: "mac"})
	if r, _ := h.push.routeOfID("mac"); r.Route != "none" || r.Reason != "no_token" {
		t.Fatalf("no token %+v", r)
	}
}

// The way is there but the relay would not take the last push: said, with its words, until a push goes again; a new
// registration (a new token) starts clean.
func TestRelayFailureIsShownUntilAPushGoes(t *testing.T) {
	h, f, _ := relayHub(t)
	h.push.retryWaits = nil
	f.resp = apns.Result{Status: 403, Reason: "InvalidProviderToken"}
	h.push.notifyDone("m1", "笔记本", "s1", "t", "x", nil)
	f.wait(t, 1)
	r := waitRoute(t, h.push, func(r pushRoute) bool { return r.Reason == pushFailed })
	if r.Route != "relay" || !strings.Contains(r.Detail, "InvalidProviderToken") || r.FailedAt == nil {
		t.Fatalf("after a refused push: %+v", r)
	}
	f.mu.Lock()
	f.resp = apns.Result{}
	f.mu.Unlock()
	h.push.notifyDone("m1", "笔记本", "s1", "t", "y", nil)
	f.wait(t, 2)
	waitRoute(t, h.push, func(r pushRoute) bool { return r.Route == "relay" && r.Reason == "" })

	h.push.pushed(Device{DeviceID: "phone"}, errString("503 relay down"))
	d, _ := h.push.device("phone")
	d.APNSToken = "b2c3d4e5f60718a9b2c3d4e5f60718a9"
	h.push.register(d)
	if r, _ := h.push.routeOfID("phone"); r.Reason != "" {
		t.Fatalf("a new token kept the old failure: %+v", r)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func waitRoute(t *testing.T, p *Pusher, ok func(pushRoute) bool) pushRoute {
	t.Helper()
	var r pushRoute
	for range 300 {
		r, _ = p.routeOfID("phone")
		if ok(r) {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("route stayed %+v", r)
	return r
}

// Settings › Devices: each credential's row shows the way of the device it registered last.
func TestRoutesByCredential(t *testing.T) {
	h := newTestHub(t)
	h.push.register(Device{DeviceID: "old", APNSToken: "aa", Credential: "dev_phone"})
	time.Sleep(2 * time.Millisecond)
	h.push.register(Device{DeviceID: "new", APNSToken: "bb", Credential: "dev_phone", RelayError: "301 https://relay.example/"})
	h.push.register(Device{DeviceID: "paired", APNSToken: "cc"}) // the pairing token: no row in Settings › Devices
	got := h.push.routesByCredential()
	if len(got) != 1 || got["dev_phone"].Reason != pushRelayFailed || got["dev_phone"].Detail != "301 https://relay.example/" {
		t.Fatalf("routes %+v", got)
	}
	code, out := call(t, h.routes(), "GET", "/api/v1/auth/tokens", h.cfg.Token, "")
	if push, _ := out["push"].(map[string]any); code != 200 || push["dev_phone"] == nil {
		t.Fatalf("tokens list: %d %v", code, out)
	}
}

// An Android phone or a browser that only saved notification settings never registers for APNs (it is notified over
// the WebSocket): Settings › Devices shows no push line for its token (#1911), as `hub doctor` leaves it out. An iPhone
// that registered without a token still says why.
func TestRoutesByCredentialSkipsSettingsOnlyRows(t *testing.T) {
	h := newTestHub(t)
	if err := h.push.setPrefsBy("android-1", "cred-android", defaultPrefs()); err != nil {
		t.Fatal(err)
	}
	if err := h.push.setPrefsBy("web-1", "cred-web", defaultPrefs()); err != nil {
		t.Fatal(err)
	}
	h.push.register(Device{DeviceID: "ios-1", Kind: "ios", Credential: "cred-ios", CredentialID: "cred-ios"})
	// A settings row saved after a registration under the same token does not hide the registered device, even when
	// it is the newer row (the database stamps it now(), db.SetDevicePrefs).
	h.push.register(Device{DeviceID: "ipad-1", Kind: "ipad", APNSToken: "aa", Credential: "cred-ipad", RelayError: "403 topic not served"})
	if err := h.push.setPrefsBy("ipad-browser", "cred-ipad", defaultPrefs()); err != nil {
		t.Fatal(err)
	}
	h.push.mu.Lock()
	row := h.push.devices["ipad-browser"]
	row.UpdatedAt = time.Now().Add(time.Hour)
	h.push.devices["ipad-browser"] = row
	h.push.mu.Unlock()

	got := h.push.routesByCredential()
	for _, cred := range []string{"cred-android", "cred-web"} {
		if r, ok := got[cred]; ok {
			t.Fatalf("%s: a settings-only row has a push way %+v", cred, r)
		}
	}
	if r := got["cred-ios"]; r.Route != pushNone || r.Reason != pushNoToken {
		t.Fatalf("iPhone without a token: %+v", r)
	}
	if r := got["cred-ipad"]; r.Reason != pushRelayFailed {
		t.Fatalf("registered iPad: %+v", r)
	}

	code, out := call(t, h.routes(), "GET", "/api/v1/auth/tokens", h.cfg.Token, "")
	push, _ := out["push"].(map[string]any)
	if code != 200 || len(push) != 2 || push["cred-android"] != nil || push["cred-web"] != nil || push["cred-ios"] == nil {
		t.Fatalf("tokens list: %d %v", code, out)
	}
}

// `hub doctor` lists every device and its way; a watch cannot read the list.
func TestDoctorListsPushRoutes(t *testing.T) {
	h := newTestHub(t)
	h.push.register(Device{DeviceID: "a", Name: "我的 iPhone", Kind: "iphone", APNSToken: "aa", RelayError: "403 topic not served"})
	h.push.register(Device{DeviceID: "prefs-only"}) // a browser's settings row: not a push device
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	get := func(ctx context.Context, path string) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		return http.DefaultClient.Do(req)
	}
	var out bytes.Buffer
	d := &doctorReport{out: &out}
	d.pushRoutes(context.Background(), get)
	text := out.String()
	for _, want := range []string{"有问题｜推送", "没有 apns.json", "1 台设备", "我的 iPhone（iphone）：收不到后台推送：向推送中继登记失败（403 topic not served）"} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "prefs-only") {
		t.Fatalf("doctor listed a settings-only row:\n%s", text)
	}
	if allowed(principal{Role: roleAnswer}, http.MethodGet, "/api/v1/devices/push") {
		t.Fatal("a watch may read every device's push way")
	}
	var body struct {
		Devices []pushDevice `json:"devices"`
	}
	resp, err := get(context.Background(), "/api/v1/devices/push")
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if len(body.Devices) != 1 || body.Devices[0].Push.Reason != pushRelayFailed {
		t.Fatalf("devices %+v", body.Devices)
	}
}

// An older hub has no such list: the doctor says to upgrade instead of reporting nothing.
func TestDoctorPushOnAnOlderHub(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	var out bytes.Buffer
	d := &doctorReport{out: &out}
	d.pushRoutes(context.Background(), func(ctx context.Context, path string) (*http.Response, error) {
		return http.Get(srv.URL + path)
	})
	if !strings.Contains(out.String(), "版本太旧") {
		t.Fatalf("doctor: %s", out.String())
	}
}
