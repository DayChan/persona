package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestDirectAPITransportSave(t *testing.T) {
	for _, api := range []string{"", "messages", "responses"} {
		for _, field := range []string{"headers", "proxy", "no_proxy"} {
			t.Run(api+"/"+field, func(t *testing.T) {
				s, _, st := modelService(t)
				value := map[string]string{"headers": "X-Example: synthetic-header", "proxy": "http://proxy.example.com", "no_proxy": "example.com"}[field]
				body := map[string]any{"name": "direct", "kind": "api", "api": api, "base_url": "https://api.example.com", "model": "m", "api_key": "synthetic-key", field: value}
				raw, _ := json.Marshal(body)
				before, writes := providerSettings(st.memStore)
				if code, response := launcherCall(t, s, "POST", "/api/v1/launchers", string(raw)); code != 400 {
					t.Fatalf("create: %d %s", code, response)
				}
				if after, n := providerSettings(st.memStore); !reflect.DeepEqual(before, after) || n != writes {
					t.Fatal("rejected create changed storage")
				}
				delete(body, field)
				raw, _ = json.Marshal(body)
				if code, response := launcherCall(t, s, "POST", "/api/v1/launchers", string(raw)); code != 200 {
					t.Fatalf("plain create: %d %s", code, response)
				}
				if cfg, err := s.apiConfig(context.Background(), ModelBinding{Provider: "launcher.direct"}); err != nil || !cfg.Configured() {
					t.Fatalf("plain provider unusable: %v", err)
				}
				body[field] = value
				body["model"] = "changed"
				raw, _ = json.Marshal(body)
				before, writes = providerSettings(st.memStore)
				if code, response := launcherCall(t, s, "PUT", "/api/v1/launchers/direct", string(raw)); code != 400 {
					t.Fatalf("update: %d %s", code, response)
				}
				if after, n := providerSettings(st.memStore); !reflect.DeepEqual(before, after) || n != writes {
					t.Fatal("rejected update changed storage")
				}
			})
		}
	}
}

func TestDirectAPITransportStoredRepair(t *testing.T) {
	for _, api := range []string{"", "messages", "responses"} {
		t.Run(api, func(t *testing.T) {
			s, _, st := modelService(t)
			sealed, err := s.sealKey("X-Example: synthetic-header")
			if err != nil {
				t.Fatal(err)
			}
			key, err := s.sealKey("synthetic-key")
			if err != nil {
				t.Fatal(err)
			}
			putLaunchers(t, s, storedLauncher{KeySealed: key, ManagedLauncher: proto.ManagedLauncher{Name: "old", Kind: "api", API: api, BaseURL: "https://api.example.com", Model: "m", Proxy: "http://proxy.example.com", NoProxy: "example.com"}, HeadersSealed: sealed})
			body := fmt.Sprintf(`{"kind":"api","api":%q,"base_url":"https://api.example.com","model":"changed"}`, api)
			before, writes := providerSettings(st.memStore)
			if code, response := launcherCall(t, s, "PUT", "/api/v1/launchers/old", body); code != 400 {
				t.Fatalf("retained transport: %d %s", code, response)
			}
			if after, n := providerSettings(st.memStore); !reflect.DeepEqual(before, after) || n != writes {
				t.Fatal("rejected repair changed storage")
			}
			clear := strings.TrimSuffix(body, "}") + `,"headers":" \n ","proxy":" ","no_proxy":"\t"}`
			if code, response := launcherCall(t, s, "PUT", "/api/v1/launchers/old", clear); code != 200 {
				t.Fatalf("clear: %d %s", code, response)
			}
			got := s.storedLaunchers(context.Background())[0]
			if got.HeadersSealed != "" || got.Proxy != "" || got.NoProxy != "" || got.Model != "changed" {
				t.Fatalf("clear not applied: %+v", got.ManagedLauncher)
			}
			if cfg, err := s.apiConfig(context.Background(), ModelBinding{Provider: "launcher.old"}); err != nil || !cfg.Configured() {
				t.Fatalf("repaired provider unusable: %v", err)
			}
		})
	}
}

func TestDirectAPITransportPresetAndLocalization(t *testing.T) {
	for _, api := range []string{"", "messages", "responses"} {
		s, hub, _ := modelService(t)
		hub.presets = []LauncherPreset{{ID: "team/direct", Kind: "api", API: api, BaseURL: "https://api.example.com", Model: "m"}}
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			result, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{Method: "POST", Path: "/api/v1/launchers", Body: []byte(`{"name":"preset","preset":"team/direct","headers":"X-Example: synthetic-header"}`), Header: map[string][]string{"Accept-Language": {string(lang)}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var response struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(result.Body, &response); err != nil {
				t.Fatal(err)
			}
			if result.Status != 400 || response.Error != i18n.T(lang, "assistant-models_launchers.err_api_transport") {
				t.Fatalf("api=%q lang=%s: %d %s", api, lang, result.Status, result.Body)
			}
		}
	}
}

func TestDirectAPITransportProbe(t *testing.T) {
	for _, operation := range []string{"test", "discover"} {
		for _, tc := range []struct {
			name, kind, api, field, override  string
			preset, anonymous, allowed, clear bool
		}{
			{name: "stored messages headers", kind: "api", api: "messages", field: "headers"},
			{name: "stored default messages proxy", kind: "api", field: "proxy"},
			{name: "stored responses no_proxy", kind: "api", api: "responses", field: "no_proxy"},
			{name: "stored responses headers", kind: "api", api: "responses", field: "headers"},
			{name: "override to messages", kind: "api", api: "responses", field: "headers", override: "messages"},
			{name: "preset messages headers", kind: "api", api: "messages", field: "headers", preset: true},
			{name: "preset default messages headers", kind: "api", field: "headers", preset: true},
			{name: "preset responses headers", kind: "api", api: "responses", field: "headers", preset: true},
			{name: "anonymous responses headers", api: "responses", field: "headers", anonymous: true},
			{name: "anonymous messages headers", api: "messages", field: "headers", anonymous: true, allowed: true},
			{name: "stored legacy launcher headers", api: "messages", field: "headers", allowed: true},
			{name: "stored launcher proxy", kind: "launcher", api: "messages", field: "proxy", allowed: true},
			{name: "preset launcher headers", kind: "launcher", api: "messages", field: "headers", preset: true, allowed: true},
			{name: "clear stored API headers", kind: "api", api: "messages", field: "headers", clear: true, allowed: true},
		} {
			t.Run(operation+"/"+tc.name, func(t *testing.T) {
				var calls atomic.Int32
				gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					if r.Method == "GET" {
						_, _ = w.Write([]byte(`{"data":[{"id":"m"}]}`))
					} else {
						_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"pong"}]}`))
					}
				}))
				defer gw.Close()
				s, hub, _ := modelService(t)
				l := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "stored", Kind: tc.kind, API: tc.api, BaseURL: gw.URL, Model: "m"}}
				in := probeInput{Name: "stored"}
				switch tc.field {
				case "headers":
					var err error
					l.HeadersSealed, err = s.sealKey("X-Example: synthetic-header")
					if err != nil {
						t.Fatal(err)
					}
				case "proxy":
					l.Proxy = gw.URL
				case "no_proxy":
					l.NoProxy = "example.com"
				}
				putLaunchers(t, s, l)
				if tc.preset {
					hub.presets = []LauncherPreset{{ID: "team/probe", Kind: tc.kind, API: tc.api, BaseURL: gw.URL, Model: "m"}}
					in = probeInput{Preset: "team/probe", Headers: ptr("X-Example: synthetic-header")}
				}
				if tc.anonymous {
					in = probeInput{API: tc.api, BaseURL: gw.URL, Model: "m", Headers: ptr("X-Example: synthetic-header")}
				}
				if tc.clear {
					in.Headers = ptr("")
				}
				in.API = tc.override
				if tc.anonymous {
					in.API = tc.api
				}
				ctx := i18n.With(context.Background(), i18n.En)
				var out map[string]any
				if operation == "test" {
					out = s.providerTest(ctx, in)
				} else {
					out = s.providerDiscover(ctx, in)
				}
				if tc.allowed {
					if calls.Load() != 1 || out["error"] != nil {
						t.Fatalf("allowed calls=%d result=%v", calls.Load(), out)
					}
				} else if calls.Load() != 0 || out["error"] != i18n.T(i18n.En, "assistant-models_launchers.err_api_transport") {
					t.Fatalf("blocked calls=%d result=%v", calls.Load(), out)
				}
			})
		}
	}
}

func TestDirectAPITransportRuntime(t *testing.T) {
	for _, api := range []string{"", "messages", "responses"} {
		for _, field := range []string{"headers", "proxy", "no_proxy"} {
			t.Run(api+"/"+field, func(t *testing.T) {
				var calls atomic.Int32
				gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusBadRequest) }))
				defer gw.Close()
				s, _, _ := modelService(t)
				key, err := s.sealKey("synthetic-key")
				if err != nil {
					t.Fatal(err)
				}
				l := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "stored", Kind: "api", API: api, BaseURL: gw.URL, Model: "m"}, KeySealed: key}
				switch field {
				case "headers":
					l.HeadersSealed = "unreadable-sealed-header"
				case "proxy":
					l.Proxy = gw.URL
				case "no_proxy":
					l.NoProxy = "example.com"
				}
				putLaunchers(t, s, l)
				ctx := i18n.With(context.Background(), i18n.En)
				if _, err := s.apiConfig(ctx, ModelBinding{Provider: "launcher.stored"}); err == nil {
					t.Error("runtime config silently discarded transport")
				}
				if s.modelUsable(ctx, ModelBinding{Provider: "launcher.stored"}) {
					t.Error("invalid direct provider reported usable")
				}
				_, err = s.purposeModel("kb").Complete(ctx, "synthetic system", "synthetic input", 8)
				if !errors.Is(err, errAPITransport) {
					t.Errorf("runtime error=%v", err)
				}
				if calls.Load() != 0 {
					t.Errorf("invalid provider made %d requests", calls.Load())
				}
			})
		}
	}
}
