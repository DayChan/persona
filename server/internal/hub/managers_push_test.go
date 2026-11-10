package hub

// Per-manager push switches (docs/assistant/managers.md "Clients"): a device that turned a manager's pushes off gets none of
// what that manager asks for, whichever way it asks, and everything else as before.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A (the assistant) and B (m-0b0b0b) each ask for every kind of push about their own things: an assistant's
// reminder, a finished turn, a reply reminder, a card, an extension's push (app.call and POST /api/v1/notify with
// its token) and `personad notify` on their computer. The phone turned B off: it gets all of A's and none of B's, on
// APNs and on its WebSocket; the tablet gets both. The hub's own pushes are no manager's.
func TestAMutedManagersPushesSkipTheDevice(t *testing.T) {
	h, url, a, b := reachHub(t)
	f := &fakeAPNs{gone: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h.push.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
	h.push.direct.Base = srv.URL
	apps := map[string]*client{}
	for _, d := range []string{"phone", "tablet"} {
		h.push.register(Device{DeviceID: d, Name: d, APNSToken: "tok-" + d, Topic: "app.example"})
		c := &client{device: d, out: make(chan []byte, 64)}
		h.mu.Lock()
		h.clients[c] = struct{}{}
		h.mu.Unlock()
		h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
		<-c.out // client_hello_ack
		apps[d] = c
	}
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneAlways, MutedManagers: []string{"m-0b0b0b"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.push.setPrefs("tablet", NotifyPrefs{Input: true, Done: DoneAlways}); err != nil {
		t.Fatal(err)
	}

	push := func(g *guardManager, n assistantwire.Notification) {
		t.Helper()
		if err := g.call(t, assistantwire.MethodNotify, assistantwire.NotifyRequest{Notification: n}, nil); err != nil {
			t.Fatalf("%s %s: %v", n.Kind, n.Title, err)
		}
	}
	for _, m := range []struct {
		g                *guardManager
		mark, mid, sid   string
		card, extensions string
	}{{b, "bmark", "mb", "b1", "ix_b", "m-0b0b0b"}, {a, "amark", "ma", "s3", "ix_a", ""}} {
		push(m.g, assistantwire.Notification{Kind: assistantwire.NotifyPush, Title: m.mark + "-assistant"})
		push(m.g, assistantwire.Notification{Kind: assistantwire.NotifyDone, DeviceID: m.mid, SessionID: m.sid, Title: m.mark + "-done", TookMS: 1})
		push(m.g, assistantwire.Notification{Kind: assistantwire.NotifyReply, DeviceID: m.mid, SessionID: m.sid, Title: m.mark + "-reply"})
		push(m.g, assistantwire.Notification{Kind: assistantwire.NotifyInput, Interaction: &proto.Interaction{ID: m.card}})
		alert := map[string]any{"aps": map[string]any{"alert": map[string]any{"title": m.mark + "-extension"}}}
		params, _ := json.Marshal(map[string]any{"payloads": map[string]any{"zh-Hans": alert, "en": alert, "ja": alert}})
		if err := m.g.call(t, assistantwire.MethodApp, assistantwire.AppCall{AppID: extwire.AppHub, Method: extwire.HubPushLocalized, Params: params}, nil); err != nil {
			t.Fatalf("%s hub.push_localized: %v", m.mark, err)
		}
		if _, err := h.localMachine(m.mid).onRequest(context.Background(), "notify", json.RawMessage(`{"title":"`+m.mark+`-script","body":"x"}`)); err != nil {
			t.Fatalf("%s personad notify: %v", m.mark, err)
		}
		if m.extensions != "" { // B knows the extension token bx-token (reachHub)
			if code, out := extDo(t, url, "POST", "/api/v1/notify", `{"title":"`+m.mark+`-http","body":"x","machine_id":"mb","session_id":"b1"}`, nil); code != http.StatusOK {
				t.Fatalf("B's extension's POST /api/v1/notify: %d %s", code, out)
			}
		}
		// A full credential's POST /api/v1/notify naming the manager's machine is that manager's, as personad notify is.
		if code := fullNotify(t, h, url, `{"title":"`+m.mark+`-full","body":"x","machine_id":"`+m.mid+`","session_id":"`+m.sid+`"}`); code != http.StatusOK {
			t.Fatalf("%s full credential's POST /api/v1/notify: %d", m.mark, code)
		}
	}
	// What each device got: B's card shows its summary B-CARD, A's A-CARD.
	// and one naming no machine: the hub's own, for every device
	if code := fullNotify(t, h, url, `{"title":"hubown-full","body":"x"}`); code != http.StatusOK {
		t.Fatalf("a full credential's POST /api/v1/notify naming no machine: %d", code)
	}
	bMarks := []string{"bmark-assistant", "bmark-done", "bmark-reply", "B-CARD", "bmark-extension", "bmark-script", "bmark-http", "bmark-full"}
	aMarks := []string{"amark-assistant", "amark-done", "amark-reply", "A-CARD", "amark-extension", "amark-script", "amark-full", "hubown-full"}
	all := f.calls(t, 2*len(aMarks)+len(bMarks))
	time.Sleep(200 * time.Millisecond)
	all = f.calls(t, len(all))
	apnsOf := func(token string) string {
		var got []string
		for _, c := range all {
			if c.Token == token {
				got = append(got, string(c.Body))
			}
		}
		return strings.Join(got, "\n")
	}
	wsOf := func(d string) string { return appGot(apps[d], 300*time.Millisecond) }
	phoneWS, tabletWS := wsOf("phone"), wsOf("tablet")
	for _, mark := range bMarks {
		if strings.Contains(apnsOf("tok-phone"), mark) || strings.Contains(phoneWS, mark) {
			t.Errorf("the phone turned B off but got %s", mark)
		}
		if !strings.Contains(apnsOf("tok-tablet"), mark) {
			t.Errorf("the tablet missed B's %s on APNs", mark)
		}
	}
	for _, mark := range aMarks {
		for _, tok := range []string{"tok-phone", "tok-tablet"} {
			if !strings.Contains(apnsOf(tok), mark) {
				t.Errorf("%s missed A's %s on APNs", tok, mark)
			}
		}
	}
	// The WebSocket carries what the hub decides (not an extension's own APNs payloads).
	for _, mark := range []string{"amark-assistant", "amark-done", "amark-reply", "A-CARD", "amark-script", "amark-full", "hubown-full"} {
		if !strings.Contains(phoneWS, mark) || !strings.Contains(tabletWS, mark) {
			t.Errorf("A's %s on the WebSocket: phone %v, tablet %v", mark, strings.Contains(phoneWS, mark), strings.Contains(tabletWS, mark))
		}
	}
	for _, mark := range []string{"bmark-assistant", "bmark-done", "bmark-reply", "B-CARD", "bmark-script", "bmark-http", "bmark-full"} {
		if !strings.Contains(tabletWS, mark) {
			t.Errorf("the tablet missed B's %s on the WebSocket", mark)
		}
	}
}

// A full credential's POST /api/v1/notify is a manager's only when it names a computer of this hub: one naming no
// machine, or an id this hub has no computer for, is the hub's own, which reaches a device that turned every manager
// off, as before.
func TestAFullNotifyForNoComputerIsTheHubsOwn(t *testing.T) {
	h, url, _, _ := reachHub(t)
	f := &fakeAPNs{gone: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h.push.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
	h.push.direct.Base = srv.URL
	h.push.register(Device{DeviceID: "desk", Name: "desk", APNSToken: "tok-desk", Topic: "app.example"})
	if err := h.push.setPrefs("desk", NotifyPrefs{Input: true, Done: DoneAlways, MutedManagers: []string{"assistant", "m-0b0b0b"}}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"title":"MUTED-ma","body":"x","machine_id":"ma","session_id":"s3"}`, // the assistant's computer: muted
		`{"title":"own-none","body":"x"}`,
		`{"title":"own-unknown","body":"x","machine_id":"nowhere","session_id":"s9"}`,
	} {
		if code := fullNotify(t, h, url, body); code != http.StatusOK {
			t.Fatalf("%s: %d", body, code)
		}
	}
	all := f.calls(t, 2)
	time.Sleep(200 * time.Millisecond)
	all = f.calls(t, len(all))
	var got []string
	for _, c := range all {
		got = append(got, string(c.Body))
	}
	joined := strings.Join(got, "\n")
	if len(all) != 2 || !strings.Contains(joined, "own-none") || !strings.Contains(joined, "own-unknown") || strings.Contains(joined, "MUTED") {
		t.Fatalf("the device got: %s", joined)
	}
}

// The prefs API keeps the muted managers per device, trimmed, without repeats and only the roster's (a removed
// manager's id would take one of the 64 places for good), and always answers a list (its presence tells an app the
// hub has the switch); a PUT of other fields keeps them.
func TestPrefsMutedManagersAPI(t *testing.T) {
	h, srv, _ := newBusHub(t)
	busToken(t, h, "m-0b0b0b", "B")
	do := func(method, body string) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+"/api/v1/devices/d1/prefs", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %v", method, resp.StatusCode, out)
		}
		return out
	}
	if got := do("GET", ""); !reflect.DeepEqual(got["muted_managers"], []any{}) {
		t.Fatalf("default: %v", got["muted_managers"])
	}
	got := do("PUT", `{"muted_managers":[" m-0b0b0b ","m-0b0b0b","","assistant","m-dead00"]}`)
	if !reflect.DeepEqual(got["muted_managers"], []any{"m-0b0b0b", "assistant"}) || got["done"] != DoneLong {
		t.Fatalf("stored (m-dead00 is no manager of the roster): %v", got)
	}
	if got := do("PUT", `{"done":"always"}`); !reflect.DeepEqual(got["muted_managers"], []any{"m-0b0b0b", "assistant"}) {
		t.Fatalf("after another change: %v", got["muted_managers"])
	}
	if got := do("PUT", `{"muted_managers":null}`); !reflect.DeepEqual(got["muted_managers"], []any{}) {
		t.Fatalf("null: %v", got["muted_managers"])
	}
	// Removed managers' ids take no place: 70 stale ones before a real one still keep the real one.
	stale := make([]string, 0, 71)
	for i := range 70 {
		stale = append(stale, fmt.Sprintf("m-%06x", 0xa00000+i))
	}
	raw, _ := json.Marshal(map[string]any{"muted_managers": append(stale, "m-0b0b0b")})
	if got := do("PUT", string(raw)); !reflect.DeepEqual(got["muted_managers"], []any{"m-0b0b0b"}) {
		t.Fatalf("stale ids: %v", got["muted_managers"])
	}
	// More managers in the roster than places: the first 64 stay.
	many := make([]string, 0, 80)
	for i := range 80 {
		id := fmt.Sprintf("m-%06x", 0xb00000+i)
		busToken(t, h, id, id)
		many = append(many, id)
	}
	raw, _ = json.Marshal(map[string]any{"muted_managers": many})
	if got, _ := do("PUT", string(raw))["muted_managers"].([]any); len(got) != maxMutedManagers || got[0] != many[0] {
		t.Fatalf("bounded: %d", len(got))
	}
	if (NotifyPrefs{MutedManagers: []string{""}}).mutes("") {
		t.Fatal("the hub's own pushes are nobody's to mute")
	}
}

// rosterReads is the database, counting the roster reads; away: they fail, as a database that does not answer.
type rosterReads struct {
	*db.DB
	away           atomic.Bool
	lists, lookups atomic.Int32
}

func (r *rosterReads) BusIdentities(ctx context.Context) ([]db.BusIdentity, error) {
	r.lists.Add(1)
	if r.away.Load() {
		return nil, errors.New("database away")
	}
	return r.DB.BusIdentities(ctx)
}

func (r *rosterReads) BusIdentityByID(ctx context.Context, id string) (*db.BusIdentity, error) {
	r.lookups.Add(1)
	if r.away.Load() {
		return nil, errors.New("database away")
	}
	return r.DB.BusIdentityByID(ctx, id)
}

// The muted ids are checked against one read of the roster, not one lookup each; when the roster cannot answer, every
// id is kept (it may be a manager the database would have named).
func TestPrefsMutedManagersWhenTheRosterCannotAnswer(t *testing.T) {
	h, srv, store := newBusHub(t)
	busToken(t, h, "m-0b0b0b", "B")
	reads := &rosterReads{DB: store}
	h.bus.idents = reads
	put := func(body string) []any {
		t.Helper()
		req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/devices/d1/prefs", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT: %d %v", resp.StatusCode, out)
		}
		got, _ := out["muted_managers"].([]any)
		return got
	}
	body := `{"muted_managers":["m-0b0b0b","m-0c0c0c","m-0d0d0d","m-dead00"]}`
	busToken(t, h, "m-0c0c0c", "C")
	busToken(t, h, "m-0d0d0d", "D")
	reads.lists.Store(0)
	reads.lookups.Store(0)
	if got := put(body); !reflect.DeepEqual(got, []any{"m-0b0b0b", "m-0c0c0c", "m-0d0d0d"}) {
		t.Fatalf("the roster answering: %v", got)
	}
	if l, k := reads.lists.Load(), reads.lookups.Load(); l != 1 || k != 0 {
		t.Fatalf("roster reads for four ids: %d lists, %d lookups; want one list", l, k)
	}
	reads.away.Store(true)
	h.expireOwners() // the roster read a moment ago is not kept any more
	if got := put(body); !reflect.DeepEqual(got, []any{"m-0b0b0b", "m-0c0c0c", "m-0d0d0d", "m-dead00"}) {
		t.Fatalf("the roster not answering keeps every id: %v", got)
	}
}

// fullNotify is POST /api/v1/notify with the hub's full credential: its status.
func fullNotify(t *testing.T, h *Hub, url, body string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", url+"/api/v1/notify", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A Live Activity counts the agents of one manager (fleetLiveOf: the default's, else the assistant's): a device that
// turned that manager's pushes off still gets the activity, without the start alert that lights the screen.
func TestALiveActivityStartsWithoutAlertForAMutedManager(t *testing.T) {
	h, f := testLive(t)
	h.push.register(Device{DeviceID: "tablet", Name: "tablet", APNSToken: "tabtok", Topic: "app.example"})
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true, MutedManagers: []string{assistantwire.AssistantID}}); err != nil {
		t.Fatal(err)
	}
	if err := h.push.setPrefs("tablet", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true, MutedManagers: []string{"m-0b0b0b"}}); err != nil {
		t.Fatal(err)
	}
	h.live.update("phone", func(v *db.LiveActivity) { v.PushToStartToken = "p2s-phone" })
	h.live.update("tablet", func(v *db.LiveActivity) { v.PushToStartToken = "p2s-tablet" })
	h.live.flush(FleetState{Phase: "needs", NeedsYou: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "needs"}}}, true, false)
	alert := map[string]bool{}
	for _, c := range f.calls(t, 2) {
		var body struct {
			APS map[string]any `json:"aps"`
		}
		if err := json.Unmarshal(c.Body, &body); err != nil {
			t.Fatal(err)
		}
		if c.Event != "start" {
			t.Fatalf("not a start: %+v", c)
		}
		_, alert[c.Token] = body.APS["alert"]
	}
	if alert["p2s-phone"] || !alert["p2s-tablet"] {
		t.Fatalf("start alerts: %v (the phone muted the activity's manager, the tablet another one)", alert)
	}
}
