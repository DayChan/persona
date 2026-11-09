package hub

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/pushcrypto"
	"github.com/nwverse/persona/server/internal/relay"
)

// relayAPNs is APNs behind a real relay.Server.
type relayAPNs struct {
	mu   sync.Mutex
	got  []apns.Request
	gone bool
	resp apns.Result // any other answer (zero: 200)
}

func (f *relayAPNs) Send(_ context.Context, r apns.Request) (apns.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, r)
	if f.gone {
		return apns.Result{Status: 410, Reason: "Unregistered"}, nil
	}
	if f.resp.Status != 0 {
		return f.resp, nil
	}
	return apns.Result{Status: 200}, nil
}

func (f *relayAPNs) wait(t *testing.T, n int) []apns.Request {
	t.Helper()
	for range 300 {
		f.mu.Lock()
		if len(f.got) >= n {
			out := append([]apns.Request(nil), f.got...)
			f.mu.Unlock()
			return out
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("want %d pushes at the relay's APNs, got %d", n, len(f.got))
	return nil
}

// relayHub: a hub without apns.json and a phone registered with a relay (as the app does: its grant, its key).
func relayHub(t *testing.T) (*Hub, *relayAPNs, *ecdh.PrivateKey) {
	t.Helper()
	return relayHubVia(t, nil)
}

// relayHubVia is relayHub with the relay's handler behind front (nil: none).
func relayHubVia(t *testing.T, front func(http.Handler) http.Handler) (*Hub, *relayAPNs, *ecdh.PrivateKey) {
	t.Helper()
	h := newTestHub(t)
	if h.push.direct != nil {
		t.Fatal("test hub has an APNs key")
	}
	f := &relayAPNs{}
	rs, err := relay.New(relay.Config{Topics: []string{"app.example"}}, f, bytes.Repeat([]byte{3}, 32), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler = rs.Handler()
	if front != nil {
		handler = front(handler)
	}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	// The phone registers with the relay itself.
	resp, err := srv.Client().Post(srv.URL+"/relay/v1/register", "application/json",
		strings.NewReader(`{"apns_token":"a1b2c3d4e5f60718a1b2c3d4e5f60718","env":"sandbox","topic":"app.example"}`))
	if err != nil {
		t.Fatal(err)
	}
	var reg struct{ Grant string }
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	resp.Body.Close()
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	h.push.register(Device{DeviceID: "phone", Name: "phone", Kind: "iphone", APNSToken: "a1b2c3d4e5f60718a1b2c3d4e5f60718", APNSEnv: "sandbox",
		Topic: "app.example", PushKey: base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes()), RelayURL: srv.URL + "/relay/", RelayGrant: reg.Grant})
	return h, f, priv
}

// opened is what the phone's notification service extension sees after decrypting.
func opened(t *testing.T, priv *ecdh.PrivateKey, r apns.Request) map[string]any {
	t.Helper()
	var env map[string]any
	if err := json.Unmarshal(r.Body, &env); err != nil {
		t.Fatal(err)
	}
	sealed, _ := base64.StdEncoding.DecodeString(env["e"].(string))
	plain, err := pushcrypto.Open(priv, sealed)
	if err != nil {
		t.Fatalf("phone cannot open: %v", err)
	}
	var p map[string]any
	_ = json.Unmarshal(plain, &p)
	return p
}

func TestRelayInteractionIsSealed(t *testing.T) {
	h, f, priv := relayHub(t)
	if !h.push.ready() {
		t.Fatal("not ready with a relayed phone")
	}
	ix := proto.Interaction{ID: "ix_secret", Kind: proto.IxPermission, MachineID: "m1", SessionID: "sess_secret", SessionTitle: "重构支付", Summary: "Bash: rm -rf build"}
	h.push.notifyInteraction(ix, "笔记本")
	r := f.wait(t, 1)[0]
	if r.PushType != "alert" || r.Token != "a1b2c3d4e5f60718a1b2c3d4e5f60718" || r.Env != "sandbox" {
		t.Fatalf("request %+v", r)
	}
	// Ids with "_", which base64 never has: "s1" turned up by chance in the ciphertext about one run in five.
	for _, leak := range []string{"重构支付", "rm -rf", "笔记本", "ix_secret", "授权", "INTERACTION", "sess_secret"} {
		if bytes.Contains(r.Body, []byte(leak)) || strings.Contains(r.CollapseID, leak) {
			t.Fatalf("relay saw %q: %s", leak, r.Body)
		}
	}
	if r.CollapseID == "" || r.CollapseID == "ix_secret" {
		t.Fatalf("collapse id %q", r.CollapseID)
	}
	p := opened(t, priv, r)
	aps := p["aps"].(map[string]any)
	alert := aps["alert"].(map[string]any)
	if alert["body"] != "Bash: rm -rf build" || aps["category"] != "INTERACTION_PERMISSION" || p["interaction_id"] != "ix_secret" || p["session_id"] != "sess_secret" {
		t.Fatalf("opened %v", p)
	}
	if iat, _ := p["iat"].(float64); time.Since(time.Unix(int64(iat), 0)) > time.Minute {
		t.Fatalf("iat %v", p["iat"])
	}
	// Which hub sent it (its group, for an app paired with several): inside the sealed payload, not before the relay.
	if p["hub_id"] != h.hubID() || bytes.Contains(r.Body, []byte(h.hubID())) {
		t.Fatalf("hub_id %v in %v", p["hub_id"], p)
	}
}

func TestRelayDoneAndWake(t *testing.T) {
	h, f, priv := relayHub(t)
	h.push.notifyDone("m1", "笔记本", "s1", "重构支付", "改好了", nil)
	// A reason with "_", which base64 never has: "tool" turned up by chance in the ciphertext.
	h.push.wake("tool_call")
	got := f.wait(t, 2)
	byType := map[string]apns.Request{}
	for _, r := range got {
		byType[r.PushType] = r
	}
	if p := opened(t, priv, byType["alert"]); p["aps"].(map[string]any)["alert"].(map[string]any)["body"] != "改好了" {
		t.Fatalf("done %v", p)
	}
	bg := byType["background"]
	if bg.Priority != "5" || bytes.Contains(bg.Body, []byte("tool_call")) {
		t.Fatalf("wake %+v %s", bg, bg.Body)
	}
	if p := opened(t, priv, bg); p["persona_wake"] != "tool_call" {
		t.Fatalf("wake %v", p)
	}
}

func TestRelayLiveActivity(t *testing.T) {
	h, f, _ := relayHub(t)
	h.store = newFleetStartStore() // transport fixture still needs a committed start budget
	h.live.update("phone", func(v *db.LiveActivity) { v.PushToStartToken = "abcdef0123456789abcdef0123456789" })
	h.live.flush(FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}, false, false)
	r := f.wait(t, 1)[0]
	if r.PushType != "liveactivity" || r.Token != "abcdef0123456789abcdef0123456789" || !bytes.Contains(r.Body, []byte(`"event":"start"`)) {
		t.Fatalf("live activity %+v %s", r, r.Body)
	}
}

func TestRelayGoneForgetsDevice(t *testing.T) {
	h, f, _ := relayHub(t)
	f.gone = true
	h.push.notifyDone("m1", "笔记本", "s1", "t", "x", nil)
	f.wait(t, 1)
	for range 200 {
		if _, ok := h.push.device("phone"); !ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("device kept after the relay said its token is gone")
}

// heldPush holds every push that reaches next until release is closed; arrived gets one value per push held.
type heldPush struct {
	arrived chan struct{}
	release chan struct{}
}

func newHeldPush() *heldPush {
	return &heldPush{arrived: make(chan struct{}, 8), release: make(chan struct{})}
}

func (hp *heldPush) front(match string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, match) {
				hp.arrived <- struct{}{}
				<-hp.release
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (hp *heldPush) waitArrived(t *testing.T) {
	t.Helper()
	select {
	case <-hp.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the push never reached APNs")
	}
}

// deviceRows records the device rows the pusher writes (the rest of Store is unused).
type deviceRows struct {
	sharedStore
	mu      sync.Mutex
	rows    map[string]db.Device
	deletes int
}

func (s *deviceRows) UpsertDevice(_ context.Context, d db.Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[d.DeviceID] = d
	return nil
}

func (s *deviceRows) SetDevicePrefs(_ context.Context, id, _ string, prefs json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := s.rows[id]
	d.DeviceID, d.Prefs = id, prefs
	s.rows[id] = d
	return nil
}

func (s *deviceRows) DeleteDevice(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.rows, id)
	s.deletes++
	return nil
}

func (s *deviceRows) row(id string) (db.Device, bool, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.rows[id]
	return d, ok, s.deletes
}

// lockDetailsOn: the default settings with one changed, to see they survive.
func lockDetailsOn() NotifyPrefs {
	p := defaultPrefs()
	p.LockDetails = true
	return p
}

// waitGoneHandled waits until the pusher has acted on a gone answer: it logged that it kept the device, or the
// device is no longer there.
func waitGoneHandled(t *testing.T, p *Pusher, mu *sync.Mutex, logs *[]string) {
	t.Helper()
	for range 500 {
		if _, ok := p.device("phone"); !ok {
			return
		}
		mu.Lock()
		for _, l := range *logs {
			if strings.Contains(l, "its old token is gone; kept") {
				mu.Unlock()
				return
			}
		}
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the gone answer was never handled")
}

// assertKeptNewToken: the late gone for the old token left the device that registered newTok, in memory and in the
// store, with its settings.
func assertKeptNewToken(t *testing.T, p *Pusher, rows *deviceRows, newTok string) {
	t.Helper()
	d, ok := p.device("phone")
	if !ok || d.APNSToken != newTok {
		t.Fatalf("device after the old token's gone: %+v (kept %v)", d, ok)
	}
	if !devicePrefs(d.Prefs).LockDetails {
		t.Fatalf("settings lost: %s", d.Prefs)
	}
	row, ok, deletes := rows.row("phone")
	if !ok || row.APNSToken != newTok || deletes != 0 {
		t.Fatalf("store row %+v kept %v, %d deletes", row, ok, deletes)
	}
}

// A relay that says the old token is gone after the app registered a new one does not delete the new registration.
func TestRelayLateGoneKeepsReregisteredDevice(t *testing.T) {
	hp := newHeldPush()
	h, f, _ := relayHubVia(t, hp.front("/v1/push"))
	f.gone = true
	rows := &deviceRows{rows: map[string]db.Device{}}
	var logMu sync.Mutex
	var logs []string
	h.push.logf = func(format string, a ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, a...))
		logMu.Unlock()
	}
	h.push.store = rows
	if err := h.push.setPrefs("phone", lockDetailsOn()); err != nil {
		t.Fatal(err)
	}
	old, _ := h.push.device("phone")
	h.push.notifyDone("m1", "笔记本", "s1", "t", "x", nil)
	hp.waitArrived(t)

	renewed := old
	renewed.APNSToken = "b2c3d4e5f60718a9b2c3d4e5f60718a9"
	renewed.Prefs = nil // registration carries the token, not the settings
	h.push.register(renewed)
	close(hp.release)

	f.wait(t, 1)
	waitGoneHandled(t, h.push, &logMu, &logs)
	assertKeptNewToken(t, h.push, rows, renewed.APNSToken)
}

// The same with the hub's own APNs key: a 410 for the old token keeps a device that registered a new one.
func TestDirectLateGoneKeepsReregisteredDevice(t *testing.T) {
	h := newTestHub(t)
	hp := newHeldPush()
	direct := &fakeAPNs{gone: map[string]bool{"oldtok": true}}
	srv := httptest.NewServer(hp.front("/3/device/")(direct))
	t.Cleanup(srv.Close)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h.push.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
	h.push.direct.Base = srv.URL
	rows := &deviceRows{rows: map[string]db.Device{}}
	var logMu sync.Mutex
	var logs []string
	h.push.logf = func(format string, a ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, a...))
		logMu.Unlock()
	}
	h.push.store = rows
	h.push.register(Device{DeviceID: "phone", Name: "phone", APNSToken: "oldtok", Topic: "app.example"})
	if err := h.push.setPrefs("phone", lockDetailsOn()); err != nil {
		t.Fatal(err)
	}
	h.push.notifyDone("m1", "笔记本", "s1", "t", "x", nil)
	hp.waitArrived(t)
	h.push.register(Device{DeviceID: "phone", Name: "phone", APNSToken: "newtok", Topic: "app.example"})
	close(hp.release)

	if c := direct.calls(t, 1); c[0].Token != "oldtok" {
		t.Fatalf("pushed to %q", c[0].Token)
	}
	waitGoneHandled(t, h.push, &logMu, &logs)
	assertKeptNewToken(t, h.push, rows, "newtok")

	// A 410 for the token the device still has drops it, from the store too.
	direct.mu.Lock()
	direct.gone["newtok"] = true
	direct.mu.Unlock()
	h.push.notifyDone("m1", "笔记本", "s1", "t", "x", nil)
	hp.waitArrived(t)
	for range 300 {
		if _, ok := h.push.device("phone"); !ok {
			if _, kept, _ := rows.row("phone"); kept {
				t.Fatal("store row kept after APNs said the current token is gone")
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("device kept after APNs said its current token is gone")
}

// flakyRelay stands in front of the relay and fails the first pushes as told: 0 drops the connection unanswered (what a
// TLS handshake or connection that timed out comes to), any other value answers with that status. Later pushes go
// through to the relay.
type flakyRelay struct {
	fail []int

	mu    sync.Mutex
	tries int
	logs  []string
}

func (f *flakyRelay) front(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/push") {
			next.ServeHTTP(w, r)
			return
		}
		f.mu.Lock()
		f.tries++
		n := f.tries
		f.mu.Unlock()
		if n > len(f.fail) {
			next.ServeHTTP(w, r)
			return
		}
		if code := f.fail[n-1]; code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"error":"failed on purpose"}`))
			return
		}
		if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
			_ = conn.Close()
		}
	})
}

// hub makes the relay hub behind f, with short waits between tries and the pusher's log kept.
func (f *flakyRelay) hub(t *testing.T) (*Hub, *relayAPNs, *ecdh.PrivateKey) {
	t.Helper()
	h, apns, priv := relayHubVia(t, f.front)
	h.push.retryWaits = []time.Duration{time.Millisecond, time.Millisecond}
	h.push.logf = func(format string, a ...any) {
		f.mu.Lock()
		f.logs = append(f.logs, fmt.Sprintf(format, a...))
		f.mu.Unlock()
	}
	return h, apns, priv
}

func (f *flakyRelay) seen() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tries, append([]string(nil), f.logs...)
}

// waitLog waits for a pusher log line that contains want and returns the log so far.
func (f *flakyRelay) waitLog(t *testing.T, want string) []string {
	t.Helper()
	for range 300 {
		if _, logs := f.seen(); strings.Contains(strings.Join(logs, "\n"), want) {
			return logs
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, logs := f.seen()
	t.Fatalf("no log line with %q in %q", want, logs)
	return nil
}

// A dropped connection to the relay used to lose the push (#2150): it is tried again and arrives.
func TestRelayTriesAPushAgainAfterTheNetworkDroppedIt(t *testing.T) {
	fr := &flakyRelay{fail: []int{0, 0}}
	h, f, priv := fr.hub(t)
	h.push.notifyDone("m1", "笔记本", "s1", "重构支付", "改好了", nil)
	if p := opened(t, priv, f.wait(t, 1)[0]); p["aps"].(map[string]any)["alert"].(map[string]any)["body"] != "改好了" {
		t.Fatalf("opened %v", p)
	}
	fr.waitLog(t, "alert push went through on try 3")
	if tries, _ := fr.seen(); tries != 3 {
		t.Fatalf("tries = %d, want 3", tries)
	}
}

func TestRelayTriesALiveActivityAgainWhenTheRelayIsBusy(t *testing.T) {
	fr := &flakyRelay{fail: []int{http.StatusServiceUnavailable}}
	h, f, _ := fr.hub(t)
	h.store = newFleetStartStore() // push-to-start requires durable authorization, even in a transport test
	h.live.update("phone", func(v *db.LiveActivity) { v.PushToStartToken = "abcdef0123456789abcdef0123456789" })
	h.live.flush(FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}, false, false)
	if r := f.wait(t, 1)[0]; r.PushType != "liveactivity" {
		t.Fatalf("push %+v", r)
	}
	if tries, _ := fr.seen(); tries != 2 {
		t.Fatalf("tries = %d, want 2", tries)
	}
}

// Every try failing, the push is given up after the waits, and the log says how many tries it had.
func TestRelayGivesUpAfterItsTries(t *testing.T) {
	fr := &flakyRelay{fail: []int{0, 0, 0}}
	h, f, _ := fr.hub(t)
	h.push.notifyDone("m1", "笔记本", "s1", "t", "x", nil)
	fr.waitLog(t, "(3 tries)")
	f.mu.Lock()
	pushes := len(f.got)
	f.mu.Unlock()
	if tries, _ := fr.seen(); tries != 3 || pushes != 0 {
		t.Fatalf("tries = %d, pushes = %d; want 3 and none", tries, pushes)
	}
}

// A refusal is the relay's answer: tried once, not again.
func TestRelayDoesNotTryARefusalAgain(t *testing.T) {
	fr := &flakyRelay{fail: []int{http.StatusBadRequest}}
	h, _, _ := fr.hub(t)
	h.push.notifyDone("m1", "笔记本", "s1", "t", "x", nil)
	logs := fr.waitLog(t, "relay phone: 400")
	time.Sleep(20 * time.Millisecond) // a retry would come after a millisecond
	if tries, _ := fr.seen(); tries != 1 || strings.Contains(strings.Join(logs, "\n"), "tries") {
		t.Fatalf("tries = %d, log %q; want one try", tries, logs)
	}
}

// APNs' permanent rejection behind the relay is tried once and logged with APNs' reason; its trouble that may pass is
// tried again. The relay used to answer both with 502, so a rejected push went to APNs three times.
func TestRelayDoesNotTryAnAPNsRejectionAgain(t *testing.T) {
	for _, c := range []struct {
		apns  apns.Result
		tries int
		log   string
	}{
		{apns.Result{Status: 400, Reason: "BadCollapseId"}, 1, "relay phone: 422 apns 400 BadCollapseId"},
		{apns.Result{Status: 503, Reason: "ServiceUnavailable"}, 3, "(3 tries)"},
	} {
		fr := &flakyRelay{}
		h, f, _ := fr.hub(t)
		f.mu.Lock()
		f.resp = c.apns
		f.mu.Unlock()
		h.push.notifyDone("m1", "笔记本", "s1", "t", "x", nil)
		fr.waitLog(t, c.log)
		time.Sleep(20 * time.Millisecond) // a retry would come after a millisecond
		f.mu.Lock()
		sent := len(f.got)
		f.mu.Unlock()
		if tries, logs := fr.seen(); tries != c.tries || sent != c.tries {
			t.Errorf("APNs %d: relay saw %d pushes, APNs %d, want %d; log %q", c.apns.Status, tries, sent, c.tries, logs)
		}
	}
}

// Without the phone's key there is nothing to seal to: no push, never one in the clear.
func TestRelayNeedsAKey(t *testing.T) {
	h, f, _ := relayHub(t)
	d, _ := h.push.device("phone")
	d.PushKey = ""
	h.push.register(d)
	if h.push.ready() {
		t.Fatal("ready without a key")
	}
	h.push.notifyDone("m1", "笔记本", "s1", "t", "x", nil)
	d.PushKey = "bm90IGEga2V5" // garbage
	h.push.register(d)
	h.push.notifyDone("m1", "笔记本", "s1", "t", "x", nil)
	time.Sleep(200 * time.Millisecond)
	if len(f.got) != 0 {
		t.Fatalf("pushed %d", len(f.got))
	}
}

// A hub with its own key keeps pushing straight to APNs, even for a phone that also registered with a relay.
func TestDirectKeyWinsOverRelay(t *testing.T) {
	h, relayed, _ := relayHub(t)
	direct := &fakeAPNs{gone: map[string]bool{}}
	srv := httptest.NewServer(direct)
	t.Cleanup(srv.Close)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h.push.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
	h.push.direct.Base = srv.URL
	h.push.notifyDone("m1", "笔记本", "s1", "t", "改好了", nil)
	c := direct.calls(t, 1)
	if c[0].Token != "a1b2c3d4e5f60718a1b2c3d4e5f60718" || c[0].Type != "alert" {
		t.Fatalf("direct %+v", c[0])
	}
	if !strings.Contains(string(c[0].Body), `"hub_id":"`+h.hubID()+`"`) {
		t.Fatalf("direct push without the hub's hub_id: %s", c[0].Body)
	}
	time.Sleep(100 * time.Millisecond)
	if len(relayed.got) != 0 {
		t.Fatal("went through the relay too")
	}
}

func TestRegisterDropsABadRelayURL(t *testing.T) {
	p := newPusher(t.TempDir(), nil, t.Logf)
	for _, u := range []string{"ftp://x/relay", "relay", "https://x/relay?y=1", "http://relay.example/relay"} {
		p.register(Device{DeviceID: "d", RelayURL: u, RelayGrant: "g", PushKey: "k"})
		if d, _ := p.device("d"); d.RelayURL != "" || d.RelayGrant != "" {
			t.Errorf("%q kept: %+v", u, d)
		}
	}
	for _, u := range []string{"https://relay.example/relay", "http://127.0.0.1:1234/relay", "http://localhost/relay"} {
		p.register(Device{DeviceID: "d", RelayURL: u, RelayGrant: "g", PushKey: "k"})
		if d, _ := p.device("d"); d.RelayURL != u {
			t.Errorf("%q dropped", u)
		}
	}
}

// The relay's secret was replaced: the old grant gets 401, the hub registers the token again and the push goes out.
func TestRelayRenewsAStaleGrant(t *testing.T) {
	h, f, priv := relayHub(t)
	d, _ := h.push.device("phone")
	d.RelayGrant = "c3RhbGU" // not this relay's
	h.push.register(d)
	h.push.notifyDone("m1", "笔记本", "s1", "t", "改好了", nil)
	r := f.wait(t, 1)[0]
	if p := opened(t, priv, r); p["aps"].(map[string]any)["alert"].(map[string]any)["body"] != "改好了" {
		t.Fatalf("opened %v", p)
	}
	d2, _ := h.push.device("phone")
	if d2.RelayGrant == "c3RhbGU" || d2.RelayGrant == "" {
		t.Fatalf("grant not renewed: %q", d2.RelayGrant)
	}
	// The app registers again with the grant it cached: the renewed one stays.
	h.push.register(d)
	if d3, _ := h.push.device("phone"); d3.RelayGrant != d2.RelayGrant {
		t.Fatalf("app's stale grant undid the renewal: %q", d3.RelayGrant)
	}
	// A grant the app newly got (it registered with the relay itself) still replaces it.
	d.RelayGrant = "bmV3"
	h.push.register(d)
	if d4, _ := h.push.device("phone"); d4.RelayGrant != "bmV3" {
		t.Fatalf("new grant ignored: %q", d4.RelayGrant)
	}
}

// A long question or command would not fit the relay's limit: the texts are shortened, the push still goes out.
func TestRelayShortensWhatDoesNotFit(t *testing.T) {
	h, f, priv := relayHub(t)
	long := strings.Repeat("很长的问题", 1200)
	h.push.notifyInteraction(proto.Interaction{ID: "ix", Kind: proto.IxQuestion, MachineID: "m1", SessionID: "s1", SessionTitle: "t",
		Questions: []proto.Question{{Question: long}}}, "笔记本")
	r := f.wait(t, 1)[0]
	p := opened(t, priv, r)
	body, _ := p["aps"].(map[string]any)["alert"].(map[string]any)["body"].(string)
	if !strings.HasPrefix(body, "很长的问题") || len(body) >= len(long) || p["interaction_id"] != "ix" {
		t.Fatalf("body %d bytes, %v", len(body), p["interaction_id"])
	}
	if len(r.Body) > 4096 {
		t.Fatalf("APNs payload %d bytes", len(r.Body))
	}
}

// TestManualRelayPush pushes through a running relay the way a hub without apns.json does, for the simulator's
// end-to-end check (scripts/test/relay_sim_e2e.sh). Opt-in: PERSONA_RELAY_URL, PERSONA_RELAY_GRANT, PERSONA_PUSH_KEY
// (the phone's public key); PERSONA_RELAY_KIND is "interaction" (default) or "done".
func TestManualRelayPush(t *testing.T) {
	url, grant, key := os.Getenv("PERSONA_RELAY_URL"), os.Getenv("PERSONA_RELAY_GRANT"), os.Getenv("PERSONA_PUSH_KEY")
	if url == "" || grant == "" || key == "" {
		t.Skip("set PERSONA_RELAY_URL, PERSONA_RELAY_GRANT and PERSONA_PUSH_KEY")
	}
	accepted := make(chan string, 4)
	p := newPusher(t.TempDir(), nil, func(f string, a ...any) {
		line := fmt.Sprintf(f, a...)
		t.Log(line)
		if strings.HasPrefix(line, "relay simulator") {
			accepted <- line
		}
	})
	p.register(Device{DeviceID: "sim", Name: "simulator", Kind: "iphone", PushKey: key, RelayURL: url, RelayGrant: grant})
	switch os.Getenv("PERSONA_RELAY_KIND") {
	case "done":
		p.notifyDone("m1", "笔记本", "s1", "重构支付模块", "改好了，测试全过", nil)
	default:
		p.notifyInteraction(proto.Interaction{ID: "ix_e2e", Kind: proto.IxPermission, MachineID: "m1", SessionID: "s1",
			SessionTitle: "重构支付模块", Summary: "Bash: swift test"}, "笔记本")
	}
	select {
	case line := <-accepted:
		if !strings.Contains(line, "accepted") {
			t.Fatal(line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("relay did not answer")
	}
}
