package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestProviderProtocolProbe(t *testing.T) {
	for _, api := range []string{"messages", "responses", "chat"} {
		t.Run(api, func(t *testing.T) {
			var gotPath string
			var payload map[string]any
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer "+fakeKey {
					t.Errorf("method or auth: %s %q", r.Method, r.Header.Get("Authorization"))
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if strings.HasSuffix(r.URL.Path, "/chat/completions") {
					if r.Header.Get("Accept") != "text/event-stream" {
						t.Errorf("accept: %s", r.Header.Get("Accept"))
					}
					_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\n"))
				}
				if strings.HasSuffix(r.URL.Path, "/responses") {
					if r.Header.Get("Accept") != "text/event-stream" {
						t.Errorf("accept: %s", r.Header.Get("Accept"))
					}
					_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
				}
			}))
			defer gw.Close()
			s, mux := launcherTestService(t)
			in := probeInput{BaseURL: gw.URL + "/team/v1", APIKey: fakeKey, Model: "m", API: api}
			out := s.providerTest(i18n.With(context.Background(), i18n.En), in)
			path := api
			if api == "chat" {
				path = "chat/completions"
			}
			if out["ok"] != true || gotPath != "/team/v1/"+path || payload["model"] != "m" {
				t.Fatalf("out=%v path=%s body=%v", out, gotPath, payload)
			}
			if api == "chat" && (payload["stream"] != true || !strings.Contains(jsonText(payload["messages"]), "ping")) {
				t.Fatalf("chat body: %v", payload)
			}
			if api == "responses" && (payload["stream"] != true || !strings.Contains(jsonText(payload["input"]), "ping")) {
				t.Fatalf("body: %v", payload)
			}
			if api == "messages" && (payload["max_tokens"] != float64(1) || !strings.Contains(jsonText(payload["messages"]), "ping")) {
				t.Fatalf("body: %v", payload)
			}
			if api == "chat" {
				status, body := launcherCall(t, mux, "POST", "/api/v1/launchers", fmt.Sprintf(`{"name":"chat","kind":"api","api":"chat","base_url":%q,"model":"m","api_key":%q}`, gw.URL+"/team", fakeKey))
				if status != http.StatusOK && status != http.StatusCreated {
					t.Fatalf("save: %d %s", status, body)
				}
				if out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{Name: "chat"}); out["ok"] != true || gotPath != "/team/v1/chat/completions" {
					t.Fatalf("stored chat: %v %s", out, gotPath)
				}
			}
			if api == "responses" {
				_, body := launcherCall(t, mux, "POST", "/api/v1/launchers", fmt.Sprintf(`{"name":"resp","kind":"api","api":"responses","base_url":%q,"model":"m","api_key":%q}`, gw.URL+"/team", fakeKey))
				if out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{Name: "resp"}); out["ok"] != true || gotPath != "/team/v1/responses" {
					t.Fatalf("stored: %v %s %s", out, gotPath, body)
				}
				if out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{Name: "resp", API: "messages"}); out["ok"] != true || gotPath != "/team/v1/messages" {
					t.Fatalf("override: %v %s", out, gotPath)
				}
			}
		})
	}
}

func TestProviderProtocolPreset(t *testing.T) {
	var path string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
	}))
	defer gw.Close()
	s, hub, _ := modelService(t)
	hub.presets = []LauncherPreset{{ID: "team/api", Kind: "api", API: "responses", BaseURL: gw.URL + "/prefix", Model: "example"}}
	out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{Preset: "team/api"})
	if out["ok"] != true || path != "/prefix/v1/responses" || out["model"] != "example" {
		t.Fatalf("%v path=%s", out, path)
	}
}

func TestResponsesProbeRejectsUnfinishedStreams(t *testing.T) {
	for _, body := range []string{
		`data: {"type":"response.failed"}` + "\n\n",
		`data: {"type":"response.incomplete"}` + "\n\n",
		`{"error":"not a stream"}`,
		`data: {"type":"response.created"}` + "\n\n",
	} {
		gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
		s, _ := launcherTestService(t)
		out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL, Model: "m", API: "responses"})
		gw.Close()
		if out["ok"] != false || out["error"] == nil {
			t.Fatalf("body %s: %v", body, out)
		}
	}
}

func TestProviderProbeFailureClasses(t *testing.T) {
	for status, code := range map[int]string{401: "auth", 403: "auth", 404: "not_found", 405: "wrong_protocol", 415: "wrong_protocol"} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer gw.Close()
			s, _ := launcherTestService(t)
			out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL, Model: "m", API: "responses"})
			if out["ok"] != false || out["code"] != code {
				t.Fatalf("%v", out)
			}
		})
	}
}

func TestProviderDiscoveryPaginationAndWindows(t *testing.T) {
	for _, kind := range []string{"after_id", "cursor", "page_token", "pageToken", "page", "next-number", "link", "links"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/prefix/v1/models" {
					t.Errorf("path: %s", r.URL.Path)
				}
				if calls == 1 {
					page := map[string]any{"data": []any{map[string]any{"id": "claude-opus-4-8", "context_window": 123}, map[string]any{"id": "own"}}}
					switch kind {
					case "after_id":
						page["has_more"], page["last_id"] = true, "own"
					case "cursor":
						page["next_cursor"] = "a+b"
					case "page_token":
						page["next_page_token"] = "a+b"
					case "pageToken":
						page["nextPageToken"] = "a+b"
					case "page":
						page["next_page"] = 2
					case "next-number":
						page["next"] = 2
						page["links"] = []any{}
					case "link":
						page["next"] = "?cursor=a%2Bb"
					case "links":
						page["links"] = map[string]string{"next": "?cursor=a%2Bb"}
					}
					_ = json.NewEncoder(w).Encode(page)
					return
				}
				key, value := kind, "a+b"
				if kind == "after_id" {
					value = "own"
				}
				if kind == "link" || kind == "links" {
					key = "cursor"
				}
				if kind == "page" || kind == "next-number" {
					key, value = "page", "2"
				}
				if r.URL.Query().Get(key) != value {
					t.Errorf("query: %s", r.URL.RawQuery)
				}
				_, _ = w.Write([]byte(`{"data":[{"id":"own","max_model_len":64000},{"id":"other","context_length":32768}]}`))
			}))
			defer gw.Close()
			s, _ := launcherTestService(t)
			out := s.providerDiscover(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL + "/prefix"})
			models, ok := out["models"].([]proto.ProviderModel)
			if !ok || out["error"] != nil || calls != 2 || len(models) != 3 {
				t.Fatalf("%v calls=%d", out, calls)
			}
			if models[0].ContextWindow != 1000000 || models[1].ContextWindow != 64000 || models[2].ContextWindow != 32768 {
				t.Fatalf("models: %+v", models)
			}
		})
	}
}

func TestProviderDiscoveryPageLimitAndLaterFailure(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			calls := 0
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if failure && calls == 2 {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_, _ = fmt.Fprintf(w, `{"data":["m"],"next_cursor":%q}`, fmt.Sprint(calls))
			}))
			defer gw.Close()
			s, _ := launcherTestService(t)
			out := s.providerDiscover(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL})
			if out["error"] == nil || len(out["models"].([]proto.ProviderModel)) != 1 {
				t.Fatalf("%v", out)
			}
			if failure && (calls != 2 || out["code"] != "auth") || !failure && calls != 100 {
				t.Fatalf("calls=%d out=%v", calls, out)
			}
		})
	}
}

func TestProviderDiscoveryRejectsUnsafeOrRepeatedPagination(t *testing.T) {
	for name, page := range map[string]string{
		"cross-origin":   `{"data":["m"],"next":"https://other.example.com/v1/models?cursor=1"}`,
		"cross-path":     `{"data":["m"],"next":"/v1/other?cursor=1"}`,
		"missing-cursor": `{"data":["m"],"has_more":true}`,
		"repeated":       `{"data":["m"],"next_cursor":"same"}`,
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(page)) }))
			defer gw.Close()
			s, _ := launcherTestService(t)
			out := s.providerDiscover(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL})
			if out["error"] == nil || calls > 2 || len(out["models"].([]proto.ProviderModel)) != 1 {
				t.Fatalf("%v calls=%d", out, calls)
			}
		})
	}
}

func TestChatProbeRejectsUnfinishedStreams(t *testing.T) {
	for _, body := range []string{
		`{"error":"not a stream"}`,
		`data: {"error":{"message":"failed"}}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n",
		`data: [DONE]` + "\n\n",
		`data: {"choices":[{"finish_reason":"stop"}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"  "}}]}` + "\n\ndata: [DONE]\n\n",
		`data: {"choices":[{"index":1,"delta":{"content":"other"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n",
		`data: {"choices":[{"finish_reason":"content_filter"}]}` + "\n\n",
		`data: {"choices":[{"delta":{"refusal":"No"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"partial"},"finish_reason":"stop"}]}` + "\n\ndata: {\"error\":{\"message\":\"failed\"}}\n\n",
		`data: {invalid}` + "\n\n",
	} {
		if chatProbeCompleted(strings.NewReader(body)) {
			t.Fatalf("accepted %q", body)
		}
	}
}

func TestChatProbeRejectsEmptyResponse(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer gw.Close()
	s, _ := launcherTestService(t)
	out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL, Model: "m", API: "chat"})
	if out["ok"] != false || out["error"] == nil {
		t.Fatalf("empty response reported success: %v", out)
	}
}

func TestChatProbeCompleted(t *testing.T) {
	for _, body := range []string{
		`data: {"choices":[{"delta":{"content":"pong"}}]}` + "\n\ndata: [DONE]\n\n",
		`data: {"choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":"length"}]}` + "\n\n",
		`data: {"choices":[{"index":0,"delta":{"content":"pong"},"finish_reason":"stop"}]}` + "\n\ndata: {\"choices\":[],\"usage\":{}}\n\n",
	} {
		if !chatProbeCompleted(strings.NewReader(body)) {
			t.Fatalf("rejected %q", body)
		}
	}
}

func TestChatProbeMultilineEvents(t *testing.T) {
	for _, ending := range []string{"\n", "\r\n"} {
		for _, tc := range []struct {
			name, body string
			ok         bool
		}{
			{"answer", "data: {\ndata: \"choices\": [{\"index\": 0,\ndata: \"delta\": {\"content\": \"pong\"},\ndata: \"finish_reason\": \"stop\"}]}\n\ndata: [DONE]\n\n", true},
			{"refusal", "data: {\ndata: \"choices\": [{\"delta\": {\"refusal\": \"No\"}}]}\n\ndata: [DONE]\n\n", false},
			{"empty", "data: {\ndata: \"choices\": [{\"finish_reason\": \"stop\"}]}\n\ndata: [DONE]\n\n", false},
			{"error after answer", "data: {\ndata: \"choices\": [{\"delta\": {\"content\": \"pong\"}, \"finish_reason\": \"stop\"}]}\n\ndata: {\ndata: \"error\": {\"message\": \"failed\"}}\n\n", false},
		} {
			t.Run(fmt.Sprintf("%q/%s", ending, tc.name), func(t *testing.T) {
				gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = w.Write([]byte(strings.ReplaceAll(tc.body, "\n", ending)))
				}))
				defer gw.Close()
				s, _ := launcherTestService(t)
				out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL, Model: "m", API: "chat"})
				if out["ok"] != tc.ok {
					t.Fatalf("probe: %v", out)
				}
			})
		}
	}
}

func TestChatProbeResponseLimit(t *testing.T) {
	// A response after the eight MiB budget must not count as a successful probe.
	stream := strings.Repeat(": ignored\n", (8<<20)/len(": ignored\n")+1) +
		`data: {"choices":[{"delta":{"content":"pong"},"finish_reason":"stop"}]}` + "\n\n"
	if chatProbeCompleted(strings.NewReader(stream)) {
		t.Fatal("accepted a response beyond the probe budget")
	}
}

func TestChatLauncherValidation(t *testing.T) {
	for _, agent := range []string{"", "claude", "codex", "agy", "omp"} {
		in := launcherInput{Name: "sample", Kind: "launcher", API: "chat", Agent: agent, BaseURL: "https://api.example.com", Model: "m"}
		if err := in.check(); err == nil {
			t.Fatalf("accepted chat launcher for %q", agent)
		}
	}
	in := launcherInput{Name: "sample", Kind: "api", API: "chat", BaseURL: "https://api.example.com", Model: "m"}
	if err := in.check(); err != nil {
		t.Fatalf("chat direct provider: %v", err)
	}
}

func TestChatProviderDiscovery(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/prefix/v1/models" || r.Header.Get("Authorization") != "Bearer "+fakeKey {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"local-model","max_model_len":32000}]}`))
	}))
	defer gw.Close()
	s, _ := launcherTestService(t)
	out := s.providerDiscover(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL + "/prefix/v1", API: "chat", APIKey: fakeKey})
	models, ok := out["models"].([]proto.ProviderModel)
	if !ok || out["error"] != nil || len(models) != 1 || models[0].ID != "local-model" || models[0].ContextWindow != 32000 {
		t.Fatalf("%v", out)
	}
}
