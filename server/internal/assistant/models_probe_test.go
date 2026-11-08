package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const fakeKey = "sk-test-probe-0123456789"

// launcherTestService is the manager with its launcher routes (it is the handler too).
func launcherTestService(t *testing.T) (*Service, http.Handler) {
	s, _, _ := modelService(t)
	return s, s
}

func launcherCall(t *testing.T, mux http.Handler, method, path, body string) (int, string) {
	t.Helper()
	r := httptest.NewRecorder()
	mux.ServeHTTP(r, httptest.NewRequest(method, path, strings.NewReader(body)))
	return r.Code, r.Body.String()
}

func probeResult(t *testing.T, mux http.Handler, path, body string) map[string]any {
	t.Helper()
	code, raw := launcherCall(t, mux, "POST", path, body)
	if code != 200 {
		t.Fatalf("%s: %d %s", path, code, raw)
	}
	if strings.Contains(raw, fakeKey) || strings.Contains(raw, "hdr-secret-value") {
		t.Fatalf("%s leaked a secret: %s", path, raw)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The test request carries both credential headers, the version, the custom headers and a 1-token "ping".
func TestProviderTestSendsBothAuthHeaders(t *testing.T) {
	_, mux := launcherTestService(t)
	var got *http.Request
	var body map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"type":"message","content":[]}`))
	}))
	defer gw.Close()
	out := probeResult(t, mux, "/api/v1/launchers/test",
		`{"base_url":"`+gw.URL+`","api_key":"Bearer `+fakeKey+`","headers":"X-Team: hdr-secret-value","model":"glm-5.3"}`)
	if out["ok"] != true || out["model"] != "glm-5.3" {
		t.Fatalf("test failed: %v", out)
	}
	if got.URL.Path != "/v1/messages" || got.Header.Get("x-api-key") != fakeKey || got.Header.Get("Authorization") != "Bearer "+fakeKey ||
		got.Header.Get("anthropic-version") != "2023-06-01" || got.Header.Get("X-Team") != "hdr-secret-value" {
		t.Fatalf("request: %s %v", got.URL.Path, got.Header)
	}
	if body["max_tokens"] != float64(1) || body["model"] != "glm-5.3" || !strings.Contains(jsonText(body["messages"]), "ping") {
		t.Fatalf("body: %v", body)
	}
}

// A 401 reads as an auth failure, and the gateway echoing the key back does not put it in the error.
func TestProviderTest401IsAuthFailureWithoutTheKey(t *testing.T) {
	_, mux := launcherTestService(t)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key ` + r.Header.Get("x-api-key") + `"}}`))
	}))
	defer gw.Close()
	out := probeResult(t, mux, "/api/v1/launchers/test", `{"base_url":"`+gw.URL+`/v1","api_key":"`+fakeKey+`","model":"m"}`)
	msg, _ := out["error"].(string)
	if out["ok"] != false || !strings.Contains(msg, "认证失败（401）") || !strings.Contains(msg, "invalid key ***") {
		t.Fatalf("401: %v", out)
	}
}

func TestProviderTestTimesOut(t *testing.T) {
	_, mux := launcherTestService(t)
	release := make(chan struct{})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer gw.Close()
	defer close(release)
	old := probeTimeout
	probeTimeout = 200 * time.Millisecond
	defer func() { probeTimeout = old }()
	out := probeResult(t, mux, "/api/v1/launchers/test", `{"base_url":"`+gw.URL+`","api_key":"`+fakeKey+`","model":"m"}`)
	if msg, _ := out["error"].(string); out["ok"] != false || !strings.Contains(msg, "没有响应") {
		t.Fatalf("timeout: %v", out)
	}
}

// With a name the stored key and headers are used; what the request gives wins.
func TestProviderTestUsesTheStoredProvider(t *testing.T) {
	_, mux := launcherTestService(t)
	var key, team string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, team = r.Header.Get("x-api-key"), r.Header.Get("X-Team")
	}))
	defer gw.Close()
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers",
		`{"name":"gw","base_url":"`+gw.URL+`","model":"m1","api_key":"`+fakeKey+`","headers":"X-Team: hdr-secret-value"}`); code != 200 {
		t.Fatal(raw)
	}
	if out := probeResult(t, mux, "/api/v1/launchers/test", `{"name":"gw"}`); out["ok"] != true || out["model"] != "m1" || key != fakeKey || team != "hdr-secret-value" {
		t.Fatalf("stored: %v %q %q", out, key, team)
	}
	probeResult(t, mux, "/api/v1/launchers/test", `{"name":"gw","api_key":"sk-test-other","headers":""}`)
	if key != "sk-test-other" || team != "" {
		t.Fatalf("override: %q %q", key, team)
	}
	// Another address never gets the stored key or headers.
	var otherKey, otherTeam string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		otherKey, otherTeam = r.Header.Get("x-api-key"), r.Header.Get("X-Team")
	}))
	defer other.Close()
	probeResult(t, mux, "/api/v1/launchers/test", `{"name":"gw","base_url":"`+other.URL+`"}`)
	if otherKey != "" || otherTeam != "" {
		t.Fatalf("stored secrets went to another gateway: %q %q", otherKey, otherTeam)
	}
	probeResult(t, mux, "/api/v1/launchers/test", `{"name":"gw","base_url":"`+gw.URL+`/"}`)
	if key != fakeKey || team != "hdr-secret-value" {
		t.Fatalf("same gateway lost the stored secrets: %q %q", key, team)
	}
}

// The three shapes of /v1/models all read; the catalog fills in what the gateway left out.
func TestProviderDiscoverParsesThreeShapes(t *testing.T) {
	_, mux := launcherTestService(t)
	for name, body := range map[string]string{
		"data":   `{"data":[{"id":"claude-opus-4-8","display_name":"Opus 4.8"},{"id":"own-model","context_length":64000},{"id":"own-model"}]}`,
		"models": `{"models":[{"slug":"claude-opus-4-8"},{"model_id":"own-model","max_input_tokens":64000}]}`,
		"array":  `["claude-opus-4-8",{"id":"own-model","context_window":64000}]`,
	} {
		var auth string
		gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				w.WriteHeader(404)
				return
			}
			auth = r.Header.Get("Authorization") + "|" + r.Header.Get("x-api-key")
			_, _ = w.Write([]byte(body))
		}))
		out := probeResult(t, mux, "/api/v1/launchers/discover", `{"base_url":"`+gw.URL+`","api_key":"`+fakeKey+`"}`)
		gw.Close()
		var models []proto.ProviderModel
		_ = json.Unmarshal([]byte(jsonText(out["models"])), &models)
		if len(models) != 2 || out["error"] != nil {
			t.Fatalf("%s: %v", name, out)
		}
		if auth != "Bearer "+fakeKey+"|"+fakeKey {
			t.Fatalf("%s auth: %q", name, auth)
		}
		opus, own := models[0], models[1]
		if opus.ID != "claude-opus-4-8" || opus.Name != "Opus 4.8" || opus.ContextWindow != 1000000 || opus.DefaultEffort != "medium" || len(opus.Efforts) != 5 {
			t.Fatalf("%s opus: %+v", name, opus)
		}
		if own.ID != "own-model" || own.ContextWindow != 64000 || len(own.Efforts) != 0 {
			t.Fatalf("%s own: %+v", name, own)
		}
	}
}

func TestProviderDiscoverErrors(t *testing.T) {
	_, mux := launcherTestService(t)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") == fakeKey {
			_, _ = w.Write([]byte(`{"object":"list"}`))
			return
		}
		w.WriteHeader(401)
	}))
	defer gw.Close()
	if out := probeResult(t, mux, "/api/v1/launchers/discover", `{"base_url":"`+gw.URL+`","api_key":"`+fakeKey+`"}`); !strings.Contains(jsonText(out["error"]), "认不出") {
		t.Fatalf("unknown shape: %v", out)
	}
	if out := probeResult(t, mux, "/api/v1/launchers/discover", `{"base_url":"`+gw.URL+`","api_key":"sk-test-wrong"}`); !strings.Contains(jsonText(out["error"]), "401") {
		t.Fatalf("401: %v", out)
	}
	if out := probeResult(t, mux, "/api/v1/launchers/discover", `{"base_url":"ftp://x"}`); !strings.Contains(jsonText(out["error"]), "Base URL") {
		t.Fatalf("bad url: %v", out)
	}
	if out := probeResult(t, mux, "/api/v1/launchers/discover", `{"name":"nope"}`); !strings.Contains(jsonText(out["error"]), "nope") {
		t.Fatalf("no such: %v", out)
	}
}

// Headers are sealed like the key: GET says has_headers only, PUT without them keeps them, "" clears them; the
// new fields round-trip, an old client's PUT keeps them, and the node gets the headers in the clear.
func TestLauncherHeadersSealedAndNewFields(t *testing.T) {
	s, mux := launcherTestService(t)
	ctx := context.Background()
	create := `{"name":"gw","base_url":"https://gw.example.com","model":"glm-5.3","api_key":"` + fakeKey + `",
		"headers":"X-Team: hdr-secret-value","models":[{"id":"glm-5.3","context_window":1000000,"efforts":["low","high","max"],"default_effort":"high"}],
		"opus_model":"glm-5.3","subagent_force":true,"proxy":"http://127.0.0.1:7890","no_proxy":"localhost","timeout_ms":600000,
		"compact_window":180000,"max_context":1000000,"effort":"high"}`
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers", create); code != 200 || strings.Contains(raw, "hdr-secret-value") {
		t.Fatal(code, raw)
	}
	stored := s.storedLaunchers(ctx)
	if len(stored) != 1 || stored[0].HeadersSealed == "" || stored[0].Headers != "" || strings.Contains(jsonText(stored), "hdr-secret-value") {
		t.Fatalf("stored: %+v", stored)
	}
	_, list := launcherCall(t, mux, "GET", "/api/v1/launchers", "")
	if strings.Contains(list, "hdr-secret-value") || strings.Contains(list, fakeKey) {
		t.Fatal("secret in GET", list)
	}
	var got struct{ Launchers []LauncherView }
	_ = json.Unmarshal([]byte(list), &got)
	v := got.Launchers[0]
	if !v.HasHeaders || !v.HasKey || len(v.Models) != 1 || v.Models[0].ContextWindow != 1000000 || v.OpusModel != "glm-5.3" || !v.SubagentForce ||
		v.Proxy == "" || v.NoProxy != "localhost" || v.TimeoutMS != 600000 || v.CompactWindow != 180000 || v.MaxContext != 1000000 || v.Effort != "high" {
		t.Fatalf("view: %+v", v)
	}
	managed := mustManagedForNodes(t, s, ctx)
	if len(managed) != 1 || managed[0].Headers != "X-Team: hdr-secret-value" || !managed[0].HasHeaders || managed[0].APIKey != fakeKey {
		t.Fatalf("managed: %+v", managed)
	}
	// An app from before #2455: no new fields in its PUT; they stay.
	if code, raw := launcherCall(t, mux, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"glm-5.2"}`); code != 200 {
		t.Fatal(raw)
	}
	l := s.storedLaunchers(ctx)[0]
	if l.HeadersSealed == "" || len(l.Models) != 1 || l.Effort != "high" || l.Model != "glm-5.2" || l.KeySealed == "" {
		t.Fatalf("kept: %+v", l)
	}
	if code, raw := launcherCall(t, mux, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"glm-5.2","headers":"","effort":""}`); code != 200 {
		t.Fatal(raw)
	}
	if l := s.storedLaunchers(ctx)[0]; l.HeadersSealed != "" || l.Effort != "" {
		t.Fatalf("cleared: %+v", l)
	}
	if m := mustManagedForNodes(t, s, ctx); m[0].Headers != "" || m[0].HasHeaders {
		t.Fatalf("cleared managed: %+v", m)
	}
}

// A bypass list that is the gateway's host alone (iOS's Proxy › No Proxy, #6238) follows the gateway to a new address,
// as the web form sends it back unchanged; any other list stays as it is.
func TestNoProxyOfTheGatewayFollowsItsAddress(t *testing.T) {
	s, mux := launcherTestService(t)
	ctx := context.Background()
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers", `{"name":"gw","base_url":"https://GW.example.com/anthropic","model":"m1",
		"api_key":"`+fakeKey+`","no_proxy":"gw.example.com"}`); code != 200 {
		t.Fatal(code, raw)
	}
	for _, tc := range []struct{ body, want string }{
		{`{"base_url":"https://other.example.com/anthropic","model":"m1","no_proxy":"gw.example.com"}`, "other.example.com"},
		{`{"base_url":"https://third.example.com","model":"m1"}`, "third.example.com"},                          // a client without the field
		{`{"base_url":"https://third.example.com","model":"m1","no_proxy":"gw.example.com"}`, "gw.example.com"}, // same address
		{`{"base_url":"https://fourth.example.com","model":"m1","no_proxy":"third.example.com, localhost"}`, "third.example.com, localhost"},
		{`{"base_url":"https://fourth.example.com","model":"m1","no_proxy":"FOURTH.example.com,"}`, "FOURTH.example.com,"},
		{`{"base_url":"https://fifth.example.com","model":"m1","no_proxy":"FOURTH.example.com,"}`, "fifth.example.com"},
	} {
		if code, raw := launcherCall(t, mux, "PUT", "/api/v1/launchers/gw", tc.body); code != 200 {
			t.Fatal(code, raw)
		}
		if got := s.storedLaunchers(ctx)[0].NoProxy; got != tc.want {
			t.Errorf("%s: no_proxy %q, want %q", tc.body, got, tc.want)
		}
	}
}

// The launcher's own environment (#5953) is sealed like the key: GET says has_env only, PUT without it keeps it, ""
// clears it, and the node gets it in the clear.
func TestLauncherEnvSealed(t *testing.T) {
	s, mux := launcherTestService(t)
	ctx := context.Background()
	create := `{"name":"gw","base_url":"https://gw.example.com","model":"glm-5.3","api_key":"` + fakeKey + `",
		"env":"# no proxy for this one\nHTTPS_PROXY=\nX_TEAM_TOKEN=env-secret-value"}`
	if code, raw := launcherCall(t, mux, "POST", "/api/v1/launchers", create); code != 200 || strings.Contains(raw, "env-secret-value") {
		t.Fatal(code, raw)
	}
	stored := s.storedLaunchers(ctx)
	if len(stored) != 1 || stored[0].EnvSealed == "" || stored[0].Env != "" || strings.Contains(jsonText(stored), "env-secret-value") {
		t.Fatalf("stored: %+v", stored)
	}
	_, list := launcherCall(t, mux, "GET", "/api/v1/launchers", "")
	var got struct{ Launchers []LauncherView }
	_ = json.Unmarshal([]byte(list), &got)
	if strings.Contains(list, "env-secret-value") || len(got.Launchers) != 1 || !got.Launchers[0].HasEnv {
		t.Fatal("GET:", list)
	}
	if m := mustManagedForNodes(t, s, ctx); len(m) != 1 || !m[0].HasEnv || m[0].Env != "# no proxy for this one\nHTTPS_PROXY=\nX_TEAM_TOKEN=env-secret-value" {
		t.Fatalf("managed: %+v", m)
	}
	if code, raw := launcherCall(t, mux, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"glm-5.2"}`); code != 200 || s.storedLaunchers(ctx)[0].EnvSealed == "" {
		t.Fatal("a PUT without env dropped it:", raw)
	}
	if code, raw := launcherCall(t, mux, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"glm-5.2","env":"BAD NAME=1"}`); code == 200 || strings.Contains(raw, "BAD NAME") {
		t.Fatal("a bad line went in, or the error quotes it:", code, raw)
	}
	if code, raw := launcherCall(t, mux, "PUT", "/api/v1/launchers/gw", `{"base_url":"https://gw.example.com","model":"glm-5.2","env":""}`); code != 200 {
		t.Fatal(raw)
	}
	if l, m := s.storedLaunchers(ctx)[0], mustManagedForNodes(t, s, ctx)[0]; l.EnvSealed != "" || m.Env != "" || m.HasEnv {
		t.Fatalf("cleared: %+v / %+v", l, m)
	}
}

// kind:api Messages providers go to the nodes; Responses ones do not.
func TestManagedForNodesSendsMessagesAPIProviders(t *testing.T) {
	s, _ := launcherTestService(t)
	ctx := context.Background()
	key, _ := s.sealKey(fakeKey)
	_ = s.putSetting(ctx, launchersManagedKey, []storedLauncher{
		{ManagedLauncher: proto.ManagedLauncher{Name: "msg", Kind: "api", API: "messages", BaseURL: "https://a.example.com", Model: "m"}, KeySealed: key},
		{ManagedLauncher: proto.ManagedLauncher{Name: "plain", Kind: "api", BaseURL: "https://b.example.com", Model: "m"}, KeySealed: key},
		{ManagedLauncher: proto.ManagedLauncher{Name: "resp", Kind: "api", API: "responses", BaseURL: "https://c.example.com", Model: "m"}, KeySealed: key},
	})
	var names []string
	for _, l := range mustManagedForNodes(t, s, ctx) {
		names = append(names, l.Name)
	}
	if strings.Join(names, ",") != "msg,plain" {
		t.Fatalf("managed: %v", names)
	}
}

func TestLauncherProviderFieldsAreChecked(t *testing.T) {
	ok := launcherInput{Name: "gw", BaseURL: "https://gw.example.com", Model: "m"}
	if err := ok.check(); err != nil {
		t.Fatal(err)
	}
	neg, bad, empty := -1, "huge", ""
	header := "no colon here"
	models := func(m ...proto.ProviderModel) *[]proto.ProviderModel { return &m }
	for name, in := range map[string]launcherInput{
		"effort":         {Effort: &bad},
		"model effort":   {Models: models(proto.ProviderModel{ID: "a", Efforts: []string{"low", "turbo"}})},
		"default effort": {Models: models(proto.ProviderModel{ID: "a", Efforts: []string{"low"}, DefaultEffort: "max"})},
		"no id":          {Models: models(proto.ProviderModel{ID: " "})},
		"duplicate":      {Models: models(proto.ProviderModel{ID: "a"}, proto.ProviderModel{ID: "a"})},
		"window":         {Models: models(proto.ProviderModel{ID: "a", ContextWindow: -5})},
		"timeout":        {TimeoutMS: &neg},
		"compact":        {CompactWindow: &neg},
		"max context":    {MaxContext: &neg},
		"proxy":          {Proxy: &bad},
		"proxy password": {Proxy: ptr("http://user:pw-secret@127.0.0.1:7890")},
		"headers":        {Headers: &header},
		"env name":       {Env: ptr("1ST=x")},
		"env line":       {Env: ptr("FOO=1\njust words")},
		"env export":     {Env: ptr("export =1")},
		"reserved name":  {Name: "discover"},
		"url":            {BaseURL: "gw.example.com"},
	} {
		in.Name, in.BaseURL, in.Model = orStr(in.Name, ok.Name), orStr(in.BaseURL, ok.BaseURL), ok.Model
		if in.check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (launcherInput{Name: "gw", BaseURL: ok.BaseURL, Model: "m", Effort: &empty, Headers: &empty, Env: &empty}).check(); err != nil {
		t.Fatal("empty effort / headers / env rejected:", err)
	}
	if err := (launcherInput{Name: "gw", BaseURL: ok.BaseURL, Model: "m", Env: ptr("# a note\n\nexport HTTP_PROXY=\nno_proxy=*.example.com\nA_1='x y'\nexport\tB=1\n\uFEFFC=2 # c")}).check(); err != nil {
		t.Fatal("a good environment rejected:", err)
	}
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func TestCatalogLookup(t *testing.T) {
	for _, id := range []string{"claude-opus-4-8", "anthropic/claude-opus-4-8", "Claude-Opus-4-8[1m]", "claude-opus-4-8-20260101"} {
		if m, ok := catalogModel(id); !ok || m.ContextWindow != 1000000 {
			t.Errorf("%s: %+v %v", id, m, ok)
		}
	}
	if _, ok := catalogModel("nobody-knows"); ok {
		t.Fatal("unknown model found")
	}
	// What the gateway said wins; a [1m] id unknown to the catalog is 1M.
	if m := enrichModel(proto.ProviderModel{ID: "glm-5.3", ContextWindow: 128000}); m.ContextWindow != 128000 || m.DefaultEffort != "high" {
		t.Fatalf("enrich: %+v", m)
	}
	if m := enrichModel(proto.ProviderModel{ID: "own[1m]"}); m.ContextWindow != 1000000 {
		t.Fatalf("1m: %+v", m)
	}
	for id, m := range modelCatalog() {
		if m.ID == "" || m.Name == "" || m.ContextWindow < 0 {
			t.Errorf("catalog %s: %+v", id, m)
		}
		for _, e := range append(append([]string{}, m.Efforts...), m.DefaultEffort) {
			if err := checkEffort("model", id, e); err != nil {
				t.Error(err)
			}
		}
	}
}

func ptr[T any](v T) *T { return &v }

// A redirect is not followed: Go would carry x-api-key and the custom headers to the other host.
func TestProviderProbeDoesNotFollowRedirects(t *testing.T) {
	_, mux := launcherTestService(t)
	var leaked string
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("x-api-key") + r.Header.Get("X-Team")
	}))
	defer elsewhere.Close()
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
	}))
	defer gw.Close()
	probeResult(t, mux, "/api/v1/launchers/test", `{"base_url":"`+gw.URL+`","api_key":"`+fakeKey+`","headers":"X-Team: hdr-secret-value","model":"m"}`)
	probeResult(t, mux, "/api/v1/launchers/discover", `{"base_url":"`+gw.URL+`","api_key":"`+fakeKey+`","headers":"X-Team: hdr-secret-value"}`)
	if leaked != "" {
		t.Fatalf("followed the redirect with the credentials: %q", leaked)
	}
}

// An oversized probe body is refused with 413 before anything is sent.
func TestProviderProbeBodyTooLarge(t *testing.T) {
	_, mux := launcherTestService(t)
	big := `{"base_url":"https://gw.example.com","model":"` + strings.Repeat("m", 2<<20) + `"}`
	if code, _ := launcherCall(t, mux, "POST", "/api/v1/launchers/test", big); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code %d", code)
	}
}

// Recorded 403 bodies (#4482). Anthropic's region block comes before the key is looked at; the rest are a key that
// lacks access, at Anthropic and at other vendors, and keep the plain 403 text.
const (
	anthropicRegionBlocked = `{"type":"error","error":{"type":"forbidden","message":"Request not allowed"}}`
	anthropicPermission    = `{"type":"error","error":{"type":"permission_error","message":"Your API key does not have permission to use the specified resource."}}`
	openAIStyleForbidden   = `{"error":{"message":"The API key doesn't have permission to access this model","type":"permission_denied_error"}}`
	plainForbidden         = `<html><body>403 Forbidden</body></html>`
)

func forbiddenGateway(t *testing.T, body string) *httptest.Server {
	t.Helper()
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(gw.Close)
	return gw
}

// Anthropic's region block reads as the network's region, not the key, names vendors that open here, and carries a
// code; test and discover alike.
func TestProviderProbeRegionBlocked(t *testing.T) {
	_, mux := launcherTestService(t)
	gw := forbiddenGateway(t, anthropicRegionBlocked)
	for _, path := range []string{"/api/v1/launchers/test", "/api/v1/launchers/discover"} {
		out := probeResult(t, mux, path, `{"base_url":"`+gw.URL+`","api_key":"`+fakeKey+`","model":"claude-sonnet-5-5"}`)
		msg := jsonText(out["error"])
		if out["code"] != probeCodeRegionBlocked || !strings.Contains(msg, "地区") || !strings.Contains(msg, "DeepSeek") ||
			!strings.Contains(msg, "智谱 GLM") || strings.Contains(msg, "Anthropic 官方、") || strings.Contains(msg, "国际版") ||
			strings.Contains(msg, "没有权限") || strings.Contains(msg, "Request not allowed") {
			t.Fatalf("%s: %v", path, out)
		}
	}
}

// Other 403s keep the 403 text with what the gateway said, and no code.
func TestProviderTestOtherForbiddenUnchanged(t *testing.T) {
	_, mux := launcherTestService(t)
	for name, c := range map[string]struct{ body, said string }{
		"anthropic permission": {anthropicPermission, "does not have permission"},
		"other vendor":         {openAIStyleForbidden, "access this model"},
		"html":                 {plainForbidden, "403 Forbidden"},
	} {
		gw := forbiddenGateway(t, c.body)
		out := probeResult(t, mux, "/api/v1/launchers/test", `{"base_url":"`+gw.URL+`","api_key":"`+fakeKey+`","model":"m"}`)
		msg := jsonText(out["error"])
		if _, has := out["code"]; has || !strings.Contains(msg, "没有权限（403）") || !strings.Contains(msg, c.said) {
			t.Fatalf("%s: %v", name, out)
		}
	}
}

// The region block is said in the request's language, the vendors' names too.
func TestProviderTestRegionBlockedEnglish(t *testing.T) {
	s, _ := launcherTestService(t)
	gw := forbiddenGateway(t, anthropicRegionBlocked)
	out := s.providerTest(i18n.With(context.Background(), i18n.En), probeInput{BaseURL: gw.URL, APIKey: fakeKey, Model: "m"})
	msg := jsonText(out["error"])
	if out["code"] != probeCodeRegionBlocked || !strings.Contains(msg, "region") || !strings.Contains(msg, "DeepSeek, ") {
		t.Fatalf("en: %v", out)
	}
}

func TestRegionBlockedBodies(t *testing.T) {
	untyped := `{"error":{"message":"Request not allowed."}}`
	for _, c := range []struct {
		body      string
		anthropic bool
		want      bool
	}{
		{anthropicRegionBlocked, true, true},
		{anthropicRegionBlocked, false, true}, // a relay passing Anthropic's block through
		{untyped, true, true},
		{untyped, false, false}, // a generic phrase from some other gateway
		{anthropicPermission, true, false},
		{openAIStyleForbidden, false, false},
		{plainForbidden, true, false},
		{`{"error":{"type":"permission_error","message":"Request not allowed"}}`, true, false},
	} {
		if got := regionBlocked([]byte(c.body), c.anthropic); got != c.want {
			t.Errorf("regionBlocked(%s, %v) = %v", c.body, c.anthropic, got)
		}
	}
}

// A non-Anthropic gateway answering 403 with the bare generic phrase keeps its own 403 text (#4482 review).
func TestProviderTestGenericNotAllowedElsewhere(t *testing.T) {
	_, mux := launcherTestService(t)
	gw := forbiddenGateway(t, `{"error":{"message":"Request not allowed"}}`)
	out := probeResult(t, mux, "/api/v1/launchers/test", `{"base_url":"`+gw.URL+`","api_key":"`+fakeKey+`","model":"m"}`)
	msg := jsonText(out["error"])
	if _, has := out["code"]; has || !strings.Contains(msg, "没有权限（403）") || !strings.Contains(msg, "Request not allowed") {
		t.Fatalf("%v", out)
	}
}
