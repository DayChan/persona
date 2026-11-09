package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/relayproto"
)

// The real revocation transaction deletes persisted activity rows before revoked
// runs. The final push must use the in-memory token without recreating those rows.
func TestRevokeLiveActivitiesWithDatabase(t *testing.T) {
	for _, route := range []string{"tokens/direct", "admin/relay"} {
		t.Run(route, func(t *testing.T) {
			store := setupTestDB(t)
			h := newTestHub(t)
			h.store, h.push.store = store, store
			h.push.retryWaits = nil
			h.admin.container = func() bool { return false }
			ctx := context.Background()
			for _, id := range []string{"dev_revoked", "dev_kept"} {
				if err := store.CreateDeviceToken(ctx, db.DeviceToken{ID: id, Name: "Phone", Kind: "phone", Role: roleFull}, hashToken("jd_"+id)); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range []string{"phone-one", "phone-two", "phone-kept"} {
				credential := "dev_revoked"
				if id == "phone-kept" {
					credential = "dev_kept"
				}
				if err := h.push.register(Device{DeviceID: id, Name: "Phone", Kind: "iphone",
					Credential: credential, CredentialID: credential, APNSToken: id + "-device", Topic: "app.example",
					PushKey: id + "-key", RelayURL: "https://relay.example.com", RelayGrant: "grant-" + id}); err != nil {
					t.Fatal(err)
				}
				if err := h.live.updateContext(ctx, id, func(v *db.LiveActivity) {
					v.ActivityID, v.PushToken, v.PushToStartToken, v.StartedAt = id+"-activity", id+"-update", id+"-start", time.Now()
				}); err != nil {
					t.Fatal(err)
				}
			}
			type delivery struct {
				request    relayproto.PushRequest
				registered bool
			}
			pushed := make(chan delivery, 3)
			record := func(request relayproto.PushRequest, id string) {
				_, registered := h.push.device(id)
				pushed <- delivery{request, registered}
			}
			h.push.relay = &http.Client{Transport: registrationTransport(func(r *http.Request) (*http.Response, error) {
				var request relayproto.PushRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					return nil, err
				}
				_ = r.Body.Close()
				id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer grant-")
				record(request, id)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true}`)), Request: r}, nil
			})}
			if route == "tokens/direct" {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/3/device/") {
						http.NotFound(w, r)
						return
					}
					var body struct {
						APS map[string]any `json:"aps"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					token := strings.TrimPrefix(r.URL.Path, "/3/device/")
					record(relayproto.PushRequest{Type: r.Header.Get("apns-push-type"), Priority: r.Header.Get("apns-priority"), Token: token, APS: body.APS}, strings.TrimSuffix(token, "-update"))
				}))
				t.Cleanup(server.Close)
				key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				h.push.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
				h.push.direct.Base = server.URL
			}
			if _, ok := h.authenticate(ctx, "jd_dev_revoked"); !ok {
				t.Fatal("credential did not authenticate before revocation")
			}
			mux := h.routes()
			before := time.Now().Unix()
			if route == "admin/relay" {
				const csrf = "revocation-test-csrf"
				session := "revocation-test-" + randomHex(16)
				sessionHash := sha256Hex(session)
				if err := store.CreateAdminSession(ctx, db.AdminSession{IDHash: sessionHash, CSRF: csrf, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := store.DeleteAdminSession(ctx, sessionHash); err != nil {
						t.Errorf("delete test admin session: %v", err)
					}
				})
				admin := &adminClient{t: t, h: mux, remote: "127.0.0.1:12345", csrf: csrf,
					cookie: &http.Cookie{Name: "persona_admin", Value: session}}
				if code, out, _ := admin.do(http.MethodDelete, "/api/v1/admin/devices/dev_revoked", ""); code != http.StatusOK {
					t.Fatalf("admin revoke: %d %v", code, out)
				}
			} else if code, out := call(t, mux, http.MethodDelete, "/api/v1/auth/tokens/dev_revoked", h.cfg.Token, ""); code != http.StatusOK {
				t.Fatalf("token revoke: %d %v", code, out)
			}
			if len(pushed) != 2 {
				t.Fatalf("revocation returned before both end pushes: got %d", len(pushed))
			}
			seen := map[string]bool{}
			for range 2 {
				got := <-pushed
				r := got.request
				if !got.registered || r.Type != "liveactivity" || r.Priority != "10" || r.APS["event"] != "end" {
					t.Fatalf("end dispatched after forgetting its target or with wrong metadata: %+v", got)
				}
				dismissal, ok := r.APS["dismissal-date"].(float64)
				if !ok || dismissal != r.APS["timestamp"] || dismissal < float64(before) || dismissal > float64(time.Now().Unix()) {
					t.Fatalf("end did not dismiss immediately: %v", r.APS)
				}
				b, err := json.Marshal(r.APS["content-state"])
				if err != nil {
					t.Fatal(err)
				}
				var state FleetState
				if err := json.Unmarshal(b, &state); err != nil || !reflect.DeepEqual(state, FleetState{}) {
					t.Fatalf("end retained fleet details: %s (%v)", b, err)
				}
				seen[r.Token] = true
			}
			if len(seen) != 2 || !seen["phone-one-update"] || !seen["phone-two-update"] {
				t.Fatalf("wrong activity tokens ended: %v", seen)
			}
			if _, ok := h.authenticate(ctx, "jd_dev_revoked"); ok {
				t.Fatal("revoked credential still authenticates")
			}
			if got := h.push.list(); len(got) != 1 || got[0].DeviceID != "phone-kept" {
				t.Fatalf("wrong registrations retained in memory: %+v", got)
			}
			devices, err := store.Devices(ctx)
			if err != nil || len(devices) != 1 || devices[0].DeviceID != "phone-kept" {
				t.Fatalf("wrong registrations retained in database: %+v (%v)", devices, err)
			}
			activities, err := store.LiveActivities(ctx)
			if err != nil || len(activities) != 1 || activities[0].DeviceID != "phone-kept" || activities[0].PushToken != "phone-kept-update" {
				t.Fatalf("retired activity rows returned or unrelated activity changed: %+v (%v)", activities, err)
			}
			h.live.mu.Lock()
			defer h.live.mu.Unlock()
			if len(h.live.rows) != 1 || h.live.rows["phone-kept"].PushToken != "phone-kept-update" {
				t.Fatalf("wrong activities retained in memory: %+v", h.live.rows)
			}
		})
	}
}
