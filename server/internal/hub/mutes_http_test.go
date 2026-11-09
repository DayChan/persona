package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestMutesGETReadFailureAndRecovery(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			const saved = `{"spaces":["m/app"],"sessions":["m/quiet"],"allowed":["m/urgent"]}`
			st := &failingMutes{
				settingsStore: settingsStore{vals: map[string][]byte{"mutes": []byte(saved)}},
				fail:          true,
			}
			h := newTestHub(t)
			h.store = st
			mux := h.routes()
			request := func(method, body string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, "/api/v1/mutes", strings.NewReader(body))
				r.RemoteAddr = "127.0.0.1:12345"
				r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
				r.Header.Set("Accept-Language", string(lang))
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				return w
			}
			for range 2 {
				w := request(http.MethodGet, "")
				var out map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				if w.Code != http.StatusInternalServerError || out["code"] != "internal" || out["error"] != i18n.T(lang, "http-status.internal") {
					t.Fatalf("failed read: %d %s", w.Code, w.Body.String())
				}
				for _, key := range []string{"spaces", "sessions", "allowed"} {
					if _, ok := out[key]; ok {
						t.Fatalf("failed read returned preferences: %s", w.Body.String())
					}
				}
				if h.mutes != nil || st.puts != 0 || string(st.vals["mutes"]) != saved {
					t.Fatal("failed read cached or changed the saved mutes")
				}
			}

			want := Pins{Spaces: []string{"m/app"}, Sessions: []string{"m/quiet"}, Allowed: []string{"m/urgent"}}
			st.fail = false
			assertMutesResponse(t, request(http.MethodGet, ""), want)
			st.fail = true
			assertMutesResponse(t, request(http.MethodGet, ""), want)
			if st.puts != 0 || string(st.vals["mutes"]) != saved {
				t.Fatal("GET changed the saved mutes")
			}

			// A toggle after recovery preserves the other preferences in the full-document PUT.
			st.fail = false
			want.Sessions = append(want.Sessions, "m/another")
			body, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			assertMutesResponse(t, request(http.MethodPut, string(body)), want)
			assertMutesResponse(t, request(http.MethodGet, ""), want)
			if st.puts != 1 || string(st.vals["mutes"]) != string(body) {
				t.Fatal("PUT did not persist the complete mute list")
			}
			// A fresh hub must read the same list from storage, including allowed agents.
			restarted := newTestHub(t)
			restarted.store = st
			mux = restarted.routes()
			h = restarted
			assertMutesResponse(t, request(http.MethodGet, ""), want)
		})
	}
}

func TestMutesGETEmpty(t *testing.T) {
	for _, withStore := range []bool{false, true} {
		t.Run(map[bool]string{false: "no store", true: "missing setting"}[withStore], func(t *testing.T) {
			h := newTestHub(t)
			if withStore {
				h.store = &settingsStore{vals: map[string][]byte{}}
			}
			mux := http.NewServeMux()
			h.pinRoutes(mux)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/mutes", nil))
			assertMutesResponse(t, w, Pins{Spaces: []string{}, Sessions: []string{}})
		})
	}
}

func assertMutesResponse(t *testing.T, w *httptest.ResponseRecorder, want Pins) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("mutes response: %d %s", w.Code, w.Body.String())
	}
	var got Pins
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mutes = %#v, want %#v", got, want)
	}
}
