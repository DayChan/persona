package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
)

// keepStore is memStore with settings and connector secrets kept in memory.
type keepStore struct {
	*memStore
	mu       sync.Mutex
	settings map[string]string
	secrets  map[string]string
}

func newKeepStore() *keepStore {
	return &keepStore{memStore: newMemStore(), settings: map[string]string{}, secrets: map[string]string{}}
}

func (s *keepStore) Setting(_ context.Context, key string, out any) (bool, error) {
	s.mu.Lock()
	raw, ok := s.settings[key]
	s.mu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal([]byte(raw), out)
}

func (s *keepStore) PutSetting(_ context.Context, key string, v any) error {
	raw, err := json.Marshal(v)
	s.mu.Lock()
	s.settings[key] = string(raw)
	s.mu.Unlock()
	return err
}

func (s *keepStore) ConnectorSecret(_ context.Context, id string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.secrets[id]
	return v, ok, nil
}

func (s *keepStore) PutConnectorSecret(_ context.Context, id, sealed string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.secrets[id] = sealed
	return nil
}

func (s *keepStore) DeleteConnectorSecret(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.secrets, id)
	return nil
}

// signInConnector signs in by phone, code and (made-up) 2FA password, keeping its session in the hub's secrets.
type signInConnector struct {
	exampleConnector
	secrets connector.Secrets
	phone   string
	out     bool
}

func (*signInConnector) ID() string   { return "example" }
func (*signInConnector) Name() string { return "Example" }
func (*signInConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus, connector.CapTools, connector.CapActions,
		connector.CapLogin}}
}

func (c *signInConnector) LoginStep(ctx context.Context, step string, v map[string]string) (connector.Prompt, error) {
	phone := connector.Prompt{Step: "phone", Fields: []connector.Field{{Name: "phone", Label: "手机号", Placeholder: "+1 555 0100"}}}
	code := connector.Prompt{Step: "code", Fields: []connector.Field{{Name: "code", Label: "验证码"}}, Hint: "验证码发到了你别的设备上"}
	password := connector.Prompt{Step: "password", Fields: []connector.Field{{Name: "password", Label: "两步验证密码", Secret: true}}}
	switch step {
	case "":
		return phone, nil
	case "phone":
		c.phone = v["phone"]
		return code, nil
	case "code":
		if v["code"] != "12345" {
			code.Error = "验证码不对"
			return code, nil
		}
		return password, nil
	case "password":
		if v["password"] != "example-password" {
			password.Error = "密码不对"
			return password, nil
		}
		if err := c.secrets.Save(ctx, []byte("session for "+c.phone)); err != nil {
			return connector.Prompt{}, err
		}
		return connector.Prompt{Done: true, Account: "示例用户"}, nil
	}
	return phone, nil
}

func (c *signInConnector) Logout(ctx context.Context) error {
	c.out = true
	return c.secrets.Delete(ctx)
}

// Signing in goes a step at a time through the hub: each answer is the next Prompt, a wrong code asks again with
// a reason; the session ends up sealed in the hub's store, and signing out removes it.
func TestConnectorLoginSteps(t *testing.T) {
	h := newTestManager(t)
	st := newKeepStore()
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	c := &signInConnector{secrets: h.secretsFor("example")}
	if err := h.addConnector(c); err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	post := func(body string) map[string]any {
		t.Helper()
		code, out := call(t, mux, "POST", "/api/v1/connectors/example/login", h.cfg.Token, body)
		if code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		return out
	}
	if out := post(`{"step":""}`); out["step"] != "phone" {
		t.Fatalf("start %v", out)
	}
	if out := post(`{"step":"phone","values":{"phone":"+1 555 0100"}}`); out["step"] != "code" || out["hint"] == nil {
		t.Fatalf("after the phone %v", out)
	}
	if out := post(`{"step":"code","values":{"code":"00000"}}`); out["step"] != "code" || out["error"] != "验证码不对" {
		t.Fatalf("a wrong code %v", out)
	}
	if out := post(`{"step":"code","values":{"code":"12345"}}`); out["step"] != "password" {
		t.Fatalf("after the code %v", out)
	} else if f := out["fields"].([]any)[0].(map[string]any); f["secret"] != true {
		t.Fatalf("the password field is not secret: %v", f)
	}
	if out := post(`{"step":"password","values":{"password":"example-password"}}`); out["done"] != true || out["account"] != "示例用户" {
		t.Fatalf("signed in %v", out)
	}
	sealed := st.secrets["example"]
	if sealed == "" || strings.Contains(sealed, "session") || strings.Contains(sealed, "555") {
		t.Fatalf("the stored session is not sealed: %q", sealed)
	}
	if got, _ := h.secretsFor("example").Load(context.Background()); string(got) != "session for +1 555 0100" {
		t.Fatalf("load %q", got)
	}
	if got, _ := h.secretsFor("other").Load(context.Background()); got != nil {
		t.Fatalf("another connector reads it: %q", got)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/connectors/example/logout", h.cfg.Token, ""); code != 200 || !c.out {
		t.Fatalf("logout %d", code)
	}
	if _, ok := st.secrets["example"]; ok {
		t.Fatal("the session is still stored after signing out")
	}
	if code, _ := call(t, mux, "POST", "/api/v1/connectors/nobody/login", h.cfg.Token, `{}`); code != 404 {
		t.Fatalf("unknown connector: %d", code)
	}
}

// A connector without a sign-in says so.
func TestConnectorWithoutLogin(t *testing.T) {
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	_ = h.addConnector(&exampleConnector{})
	if code, _ := call(t, h.routes(), "POST", "/api/v1/connectors/example/login", h.cfg.Token, `{}`); code != 409 {
		t.Fatalf("%d", code)
	}
}

// The switch in Settings › Connectors: off stops the connector and is kept, so the next hub starts it switched off.
func TestConnectorSwitchIsKept(t *testing.T) {
	h := newTestManager(t)
	st := newKeepStore()
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	_ = h.addConnector(&exampleConnector{})
	mux := h.routes()
	code, out := call(t, mux, "PUT", "/api/v1/connectors/example/settings", h.cfg.Token, `{"enabled":false}`)
	if code != 200 || out["enabled"] != false || out["status"] != connector.StateDisabled {
		t.Fatalf("%d %v", code, out)
	}
	if len(h.connectors.Tools(context.Background())) != 0 {
		t.Fatal("a switched-off connector offers tools")
	}

	next := newTestManager(t)
	next.store = st
	next.connectors = connector.NewRegistry(nil)
	_ = next.addConnector(&exampleConnector{})
	next.restoreConnectorSwitches(context.Background())
	if next.connectors.Enabled("example") {
		t.Fatal("the next hub forgot the switch")
	}

	if code, out := call(t, mux, "PUT", "/api/v1/connectors/example/settings", h.cfg.Token, `{"enabled":true}`); code != 200 || out["enabled"] != true {
		t.Fatalf("back on: %d %v", code, out)
	}
	if !strings.Contains(st.settings[connectorSettingsKey], `"example":true`) {
		t.Fatalf("the choice to switch it back on is not kept: %s", st.settings[connectorSettingsKey])
	}
}

// A connector added to start off stays off until the user turns it on, and then stays on.
func TestConnectorThatStartsOff(t *testing.T) {
	h := newTestManager(t)
	st := newKeepStore()
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	_ = h.addConnectorOff(&exampleConnector{})
	h.restoreConnectorSwitches(context.Background())
	if h.connectors.Enabled("example") {
		t.Fatal("it started on")
	}
	if code, _ := call(t, h.routes(), "PUT", "/api/v1/connectors/example/settings", h.cfg.Token, `{"enabled":true}`); code != 200 {
		t.Fatal(code)
	}
	next := newTestManager(t)
	next.store = st
	next.connectors = connector.NewRegistry(nil)
	_ = next.addConnectorOff(&exampleConnector{})
	next.restoreConnectorSwitches(context.Background())
	if !next.connectors.Enabled("example") {
		t.Fatal("turned on by the user, it is off again after a restart")
	}
}

// "Delete saved data": the connector is switched off (and stays off), its knowledge content and inbox items go; the rest
// stays.
func TestConnectorDataDelete(t *testing.T) {
	h := newTestManager(t)
	st := newKeepStore()
	h.store = st
	h.connectors = connector.NewRegistry(nil)
	_ = h.addConnector(&exampleConnector{})
	mux := h.routes()
	// The knowledge base and the inbox are the assistant's (in this process): the manager asks it to delete the
	// source's knowledge content (kb.forget) and its inbox items (inbox.forget), which it does (tested there).
	a := forgettingAssistant(h)

	if code, _ := call(t, mux, "DELETE", "/api/v1/connectors/nope/data", h.cfg.Token, ""); code != 404 {
		t.Fatalf("an unknown connector: %d", code)
	}
	code, out := call(t, mux, "DELETE", "/api/v1/connectors/example/data", h.cfg.Token, "")
	if code != 200 || out["chats"] != 1.0 || out["people"] != 1.0 || out["posts"] != 2.0 || out["items"] != 3.0 || out["inbox"] != 1.0 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if h.connectors.Enabled("example") || !strings.Contains(st.settings[connectorSettingsKey], `"example":false`) {
		t.Fatalf("not switched off for good: %s", st.settings[connectorSettingsKey])
	}
	for _, method := range []string{assistantwire.MethodKBForget, assistantwire.MethodInboxForget} {
		if got := a.got(method); len(got) != 1 || !strings.Contains(string(got[0]), `"source":"example"`) {
			t.Fatalf("%s asked %s", method, got)
		}
	}

	// The assistant away (the knowledge base and the inbox are its): nothing goes, the app is told to try again (503).
	h.opts.Assistant = nil
	if code, out := call(t, mux, "DELETE", "/api/v1/connectors/example/data", h.cfg.Token, ""); code != 503 || out["code"] != assistantwire.CodeAssistantOffline {
		t.Fatalf("with the assistant away: %d %v", code, out)
	}
	// Back: asked again, what is left goes.
	a = forgettingAssistant(h)
	if code, out := call(t, mux, "DELETE", "/api/v1/connectors/example/data", h.cfg.Token, ""); code != 200 || out["chats"] != 1.0 ||
		len(a.got(assistantwire.MethodKBForget)) != 1 {
		t.Fatalf("asked again: %d %v", code, out)
	}
}

// forgettingAssistant is a fake assistant reporting deletion counts for each type of saved content.
func forgettingAssistant(h *Manager) *fakeAssistant {
	a := withAssistant(h)
	a.on(assistantwire.MethodKBForget, func(json.RawMessage) (any, error) {
		return assistantwire.KBForgotten{Chats: 1, People: 1, Posts: 2, Items: 3}, nil
	})
	a.on(assistantwire.MethodInboxForget, func(json.RawMessage) (any, error) { return map[string]any{"deleted": 1}, nil })
	return a
}

// qrConnector signs in by a QR code to scan: the first step shows it, polling it again says it was scanned.
type qrConnector struct {
	exampleConnector
	polls int
}

func (*qrConnector) ID() string   { return "example" }
func (*qrConnector) Name() string { return "Example" }
func (*qrConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapStatus, connector.CapTools, connector.CapActions,
		connector.CapLogin}}
}

func (c *qrConnector) LoginStep(_ context.Context, step string, _ map[string]string) (connector.Prompt, error) {
	if step == "qr" {
		c.polls++
	}
	if c.polls == 2 {
		return connector.Prompt{Done: true, Account: "示例用户"}, nil
	}
	// What a connector puts in QR is not passed on: the hub draws URL.
	return connector.Prompt{Step: "qr", Kind: connector.PromptQRCode, URL: "https://example.com/scan/abc", Interval: 2,
		Hint: "用另一台设备扫码", QR: &connector.QRCode{Size: 1, Rows: []string{"1"}}}, nil
}

func (*qrConnector) Logout(context.Context) error { return nil }

// A QR step reaches the app with the code's modules, drawn by the hub from the URL; polling the same step goes on
// until Done.
func TestConnectorLoginQR(t *testing.T) {
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	if err := h.addConnector(&qrConnector{}); err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	code, out := call(t, mux, "POST", "/api/v1/connectors/example/login", h.cfg.Token, `{"step":""}`)
	if code != 200 || out["kind"] != "qr" || out["url"] != "https://example.com/scan/abc" || out["interval"] != float64(2) {
		t.Fatalf("%d %v", code, out)
	}
	qr, _ := out["qr"].(map[string]any)
	rows, _ := qr["rows"].([]any)
	want, size, err := qrRows("https://example.com/scan/abc")
	if err != nil {
		t.Fatal(err)
	}
	if qr["size"] != float64(size) || len(rows) != size || rows[0] != want[0] || size < 21 {
		t.Fatalf("qr %v", qr)
	}
	for _, r := range rows {
		if s := r.(string); len(s) != size || strings.Trim(s, "01") != "" {
			t.Fatalf("row %q", s)
		}
	}
	if _, out := call(t, mux, "POST", "/api/v1/connectors/example/login", h.cfg.Token, `{"step":"qr"}`); out["qr"] == nil {
		t.Fatalf("polling %v", out)
	}
	if _, out := call(t, mux, "POST", "/api/v1/connectors/example/login", h.cfg.Token, `{"step":"qr"}`); out["done"] != true || out["qr"] != nil {
		t.Fatalf("done %v", out)
	}
}

// A field step never carries a QR code, whatever the connector put there.
func TestWithQROnlyForQRSteps(t *testing.T) {
	p := connector.Prompt{Step: "code", URL: "https://example.com", QR: &connector.QRCode{Size: 1, Rows: []string{"1"}}}
	if err := withQR(&p); err != nil || p.QR != nil {
		t.Fatalf("%v %+v", err, p.QR)
	}
	if err := withQR(&connector.Prompt{Kind: connector.PromptQRCode}); err == nil {
		t.Fatal("a QR step without a URL")
	}
}
