package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/relayproto"
)

func TestRevokedCredentialEndsOnlyItsLiveActivities(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusGone, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newTestHub(t)
				la := h.live
				state := FleetState{Phase: "needsYou", NeedsYou: 1, Items: []FleetItem{{ID: "computer/session", Title: "Review changes"}}}
				la.last, la.emptyAt, la.doneSince, la.answeredAt = state, time.Now(), time.Now(), time.Now()
				la.episode = 3
				for _, id := range []string{"phone", "tablet", "idle", "unreachable", "other"} {
					credential := "dev_revoked"
					if id == "other" {
						credential = "dev_other"
					}
					d := Device{DeviceID: id, Credential: credential, CredentialID: credential, APNSToken: id + "-device", Topic: "app.example",
						RelayURL: "https://relay.example.com", RelayGrant: "grant", PushKey: id + "-key"}
					if id == "unreachable" {
						d.RelayURL = ""
					}
					if err := h.push.register(d); err != nil {
						t.Fatal(err)
					}
					la.update(id, func(v *db.LiveActivity) {
						v.PushToStartToken, v.StartedAt = id+"-start", time.Now()
						if id != "idle" {
							v.ActivityID, v.PushToken = id+"-activity", id+"-update"
						}
					})
					la.sent[id], la.sentAt[id], la.started[id] = []byte("previous state"), time.Now(), 3
				}
				// Revocation must dismiss a running activity even if its preference has since been disabled.
				if err := h.push.setPrefs("tablet", NotifyPrefs{LiveActivity: false}); err != nil {
					t.Fatal(err)
				}
				other := la.rows["other"]
				var mu sync.Mutex
				var requests []relayproto.PushRequest
				release := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer unblock()
				h.push.relay = &http.Client{Transport: registrationTransport(func(r *http.Request) (*http.Response, error) {
					var req relayproto.PushRequest
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						return nil, err
					}
					_ = r.Body.Close()
					mu.Lock()
					requests = append(requests, req)
					mu.Unlock()
					<-release
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
				})}
				done := make(chan struct{})
				go func() { h.revoked("dev_revoked"); close(done) }()
				synctest.Wait()
				select {
				case <-done:
					t.Fatal("revocation forgot push routes before the end attempts completed")
				default:
				}
				mu.Lock()
				got := append([]relayproto.PushRequest(nil), requests...)
				mu.Unlock()
				if len(got) != 2 {
					t.Fatalf("want both running reachable activities dispatched concurrently, got %d", len(got))
				}
				for _, req := range got {
					if req.Type != "liveactivity" || req.Priority != "10" || req.APS["event"] != "end" ||
						(req.Token != "phone-update" && req.Token != "tablet-update") {
						t.Fatalf("unexpected end request: %+v", req)
					}
					if req.APS["dismissal-date"] != float64(time.Now().Unix()) || req.APS["timestamp"] != req.APS["dismissal-date"] {
						t.Fatalf("end does not dismiss immediately: %+v", req.APS)
					}
					body, _ := json.Marshal(req.APS["content-state"])
					var final FleetState
					if err := json.Unmarshal(body, &final); err != nil || !reflect.DeepEqual(final, FleetState{}) {
						t.Fatalf("end retains fleet details: %s (%v)", body, err)
					}
				}
				for _, id := range []string{"phone", "tablet", "idle", "unreachable"} {
					if _, ok := h.push.device(id); !ok {
						t.Fatalf("%s forgotten before end attempts completed", id)
					}
					if _, ok := la.rows[id]; ok {
						t.Fatalf("%s can still receive fleet updates during revocation", id)
					}
				}
				unblock()
				<-done
				synctest.Wait()
				if got := h.push.list(); len(got) != 1 || got[0].DeviceID != "other" {
					t.Fatalf("revocation did not forget exactly its devices: %+v", got)
				}
				if !reflect.DeepEqual(la.last, state) || la.episode != 3 || la.emptyAt.IsZero() || la.doneSince.IsZero() || la.answeredAt.IsZero() ||
					la.rows["other"] != other || len(la.sent) != 1 || len(la.sentAt) != 1 || len(la.started) != 1 {
					t.Fatal("revocation changed shared fleet state or retained revoked device state")
				}
				h.revoked("dev_revoked")
				h.revoked("")
				synctest.Wait()
				if len(requests) != 2 || la.rows["other"] != other {
					t.Fatal("repeated or empty revocation sent more pushes or changed another activity")
				}
			})
		})
	}
}
