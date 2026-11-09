package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/update"
)

type updateCheckTransport func(*http.Request) (*http.Response, error)

func (f updateCheckTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Check both writes made by a manual check, including an install starting while the request is in flight.
func TestUpdateCheckPreservesInstall(t *testing.T) {
	for _, phase := range []string{"downloading", "waiting_idle", "restarting", "confirming", "before_confirm"} {
		for _, result := range []string{"newer", "current", "error"} {
			for _, inFlight := range []bool{false, true} {
				name := phase + "/" + result
				if inFlight {
					name += "/starts_during_check"
				}
				t.Run(name, func(t *testing.T) {
					build := int64(202611021700)
					if result == "current" {
						build = 202611021500
					}
					p := newPublisher(t, "release", build)
					h, _, _, _ := updatingHub(t, p, update.Release)
					u := h.upd
					previous := &update.Published{Manifest: &update.Manifest{Version: "previous", Build: 202611021400}}
					u.release = previous
					u.lastCheck = time.Now().Add(-time.Hour)
					beforeCheck := u.lastCheck
					activate := func() {
						u.mu.Lock()
						defer u.mu.Unlock()
						u.state, u.progress, u.errText = phase, "hub", "existing install error"
						u.running = phase != "confirming" && phase != "before_confirm"
						u.confirmed = u.running
						if phase == "before_confirm" {
							u.state = "idle" // boot has run, but confirm has not started yet.
						}
						u.busy = []busyItem{{MachineID: "box", SessionID: "s1", Title: "working"}}
					}
					var active updateView
					if !inFlight {
						activate()
						active = u.view()
					}
					app := appClient(h)
					transport := p.srv.Client().Transport
					u.client = &http.Client{Transport: updateCheckTransport(func(r *http.Request) (*http.Response, error) {
						if inFlight {
							if v := u.view(); v.State != "checking" {
								t.Errorf("inactive check state = %q", v.State)
							}
							broadcasts(app, "hub_update") // The initial checking event precedes the install.
							activate()
							active = u.view()
						} else {
							assertUpdateInstallView(t, u.view(), active)
						}
						if result == "error" {
							return nil, errors.New("publisher unavailable")
						}
						return transport.RoundTrip(r)
					})}
					api := http.NewServeMux()
					u.routes(api)
					w := httptest.NewRecorder()
					api.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/hub/update/check", nil))
					wantCode := http.StatusOK
					if result == "error" {
						wantCode = http.StatusBadGateway
					}
					if w.Code != wantCode {
						t.Fatalf("check status = %d, want %d: %s", w.Code, wantCode, w.Body.String())
					}
					v := u.view()
					assertUpdateInstallView(t, v, active)
					if len(u.settings.History) != 0 {
						t.Fatal("check recorded a held release during an active update")
					}
					if v.LastCheck == nil || !v.LastCheck.After(beforeCheck) {
						t.Fatalf("last_check was not refreshed: %+v", v.LastCheck)
					}
					if result == "error" {
						if u.release != previous {
							t.Fatal("failed check replaced the cached release")
						}
					} else if v.Latest == nil || v.Latest.Build != build {
						t.Fatalf("latest was not refreshed: %+v", v.Latest)
					}
					if result != "error" {
						var response updateView
						if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
							t.Fatal(err)
						}
						assertUpdateInstallView(t, response, active)
					}
					if len(app.out) == 0 {
						t.Fatal("check did not broadcast the refreshed view")
					}
					for len(app.out) > 0 {
						var event struct{ Update updateView }
						if err := json.Unmarshal(<-app.out, &event); err != nil {
							t.Fatal(err)
						}
						assertUpdateInstallView(t, event.Update, active)
					}
				})
			}
		}
	}
}

func assertUpdateInstallView(t *testing.T, got, want updateView) {
	t.Helper()
	if got.State != want.State || got.Progress != want.Progress || got.Error != want.Error ||
		got.Confirmed != want.Confirmed || !reflect.DeepEqual(got.Busy, want.Busy) {
		t.Fatalf("check changed the install view: got %+v, want %+v", got, want)
	}
}

func TestUpdateCheckWhileWaitingForIdle(t *testing.T) {
	p := newPublisher(t, "v2-202611021700", 202611021700)
	h, m, _, _ := updatingHub(t, p, update.Release)
	u := h.upd
	u.idleEvery = time.Hour // A later install tick must not hide a check that reset the state.
	setSessions(m, proto.Session{SessionID: "s1", Title: "working", Status: proto.StatusWorking})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := u.check(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); u.install(ctx, false) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("install did not stop")
		}
	}()
	waitFor(t, "waiting for idle", func() bool { return u.view().State == "waiting_idle" })
	before := u.view()
	if _, err := u.check(ctx); err != nil {
		t.Fatal(err)
	}
	assertUpdateInstallView(t, u.view(), before)
}
