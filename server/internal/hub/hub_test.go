package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg.Listen != ":18660" {
		t.Fatalf("listen = %q", cfg.Listen)
	}
	if len(cfg.Token) != 48 {
		t.Fatalf("token should be 24 random bytes of hex, got %q", cfg.Token)
	}
	if other := defaultConfig(); other.Token == cfg.Token {
		t.Fatal("each default config needs its own token")
	}
	if !strings.Contains(cfg.PublicURL, ":18660") {
		t.Fatalf("public url = %q", cfg.PublicURL)
	}
	ids := map[string]bool{}
	for _, m := range cfg.Machines {
		if m.ID == "" || m.Name == "" || (m.Kind != "local" && m.Kind != "ssh") {
			t.Fatalf("bad machine %+v", m)
		}
		if m.Kind == "ssh" && m.SSHHost == "" {
			t.Fatalf("ssh machine %s has no host", m.ID)
		}
		ids[m.ID] = true
	}
	// A fresh hub knows only itself; other machines are added to hub.json (docs/operations/config.md).
	if len(cfg.Machines) != 1 {
		t.Fatalf("default machines should be just this one, got %+v", cfg.Machines)
	}
	if local := cfg.Machines[0]; local.Kind != "local" {
		t.Fatalf("the first machine should be the local hub, got %+v", local)
	}
}

// The Docker image's hub (PERSONA_LOCAL_NODE=0) starts with no machine: its own has no herdr or claude.
func TestDefaultConfigWithoutLocalNode(t *testing.T) {
	t.Setenv("PERSONA_LOCAL_NODE", "0")
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	if _, err := LoadConfig(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, ".persona", "hub.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["machines"]) != "[]" {
		t.Fatalf("machines written as %s, want [] (not omitted: a missing list would mean the default)", raw["machines"])
	}
}

// A compose file gives the database's address and the hub's own: they win over hub.json and are not written into it.
func TestEnvOverridesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	dir := filepath.Join(home, ".persona")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := `{"token":"t","database_url":"postgres://file","public_url":"http://file:18660","listen":":1"}`
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERSONA_DATABASE_URL", "postgres://env")
	t.Setenv("PERSONA_PUBLIC_URL", "https://hub.example.com")
	t.Setenv("PERSONA_LISTEN", ":18660")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://env" || cfg.PublicURL != "https://hub.example.com" || cfg.Listen != ":18660" {
		t.Fatalf("env not applied: %+v", cfg)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "hub.json")); string(b) != file {
		t.Fatalf("hub.json was rewritten: %s", b)
	}
	t.Setenv("PERSONA_DATABASE_URL", "")
	if cfg, _ := LoadConfig(); cfg.DatabaseURL != "postgres://file" {
		t.Fatalf("an empty variable must not override: %q", cfg.DatabaseURL)
	}
}

func TestNodeBinaryDirFromEnv(t *testing.T) {
	t.Setenv("PERSONA_NODE_BINARIES", "/opt/persona/nodes")
	if d := identity.NodeBinaryDir(); d != "/opt/persona/nodes" {
		t.Fatalf("node binaries in %s", d)
	}
}

func TestLoadConfigCreatesAndReloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".persona", "hub.json")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config file was not created: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode = %v", st.Mode().Perm())
	}
	again, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if again.Token != cfg.Token {
		t.Fatal("the token must survive a reload")
	}
}

// scripts/deploy/db.sh creates hub.json with only database_url before the hub's first start: the token generated then must
// be written down, or each start and `personad hub pair` would invent another one and paired phones get 401.
func TestLoadConfigPersistsTokenMissingFromFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	dir := filepath.Join(home, ".persona")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "hub.json")
	if err := os.WriteFile(path, []byte(`{"database_url":"postgres://x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	again, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token == "" || again.Token != cfg.Token {
		t.Fatalf("token changed between loads: %q then %q", cfg.Token, again.Token)
	}
	var raw map[string]any
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &raw); err != nil || raw["database_url"] != "postgres://x" || raw["token"] != cfg.Token {
		t.Fatalf("hub.json = %s", b)
	}
}

func TestLoadConfigKeepsExplicitValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	dir := filepath.Join(home, ".persona")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"listen":":1","token":"abc","public_url":"http://x","hook_timeout_minutes":0,"machines":[]}`
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":1" || cfg.Token != "abc" {
		t.Fatalf("explicit values were not kept: %+v", cfg)
	}
}

func TestLoadConfigRejectsEmptyToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	dir := filepath.Join(home, ".persona")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{"listen":":1","token":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "token is empty") {
		t.Fatalf("want a token error, got %v", err)
	}
}

func TestLoadConfigRejectsBadJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	dir := filepath.Join(home, ".persona")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hub.json"), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("want a parse error, got %v", err)
	}
}

func TestPairURLRoundTrips(t *testing.T) {
	cfg := &Config{PublicURL: "http://hub.example.com:18660", Token: "a b&c=d/é"}
	link := cfg.PairURL()
	if !strings.HasPrefix(link, "persona://pair?") {
		t.Fatalf("pair url = %q", link)
	}
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "persona" || u.Host != "pair" {
		t.Fatalf("unexpected scheme/host %q %q", u.Scheme, u.Host)
	}
	q := u.Query()
	if q.Get("url") != cfg.PublicURL {
		t.Fatalf("url = %q", q.Get("url"))
	}
	if q.Get("token") != cfg.Token {
		t.Fatalf("token = %q", q.Get("token"))
	}
}

// The Mac has no camera to scan with: `personad hub pair` prints the link too, to paste into "Connect hub".
func TestPairPrintsTheLinkForTheMac(t *testing.T) {
	cfg := &Config{PublicURL: "http://hub.example.com:18660", Token: "tok"}
	var b strings.Builder
	writePair(&b, cfg)
	out := b.String()
	if !strings.Contains(out, cfg.PairURL()) {
		t.Fatalf("pair output has no link:\n%s", out)
	}
	if !strings.Contains(out, "Server: "+cfg.PublicURL) {
		t.Fatalf("pair output lost the server line:\n%s", out)
	}
}

// newTestHub builds a Hub with no machines, no clients and push disabled, so interaction logic can be exercised offline.
func newTestHub(t *testing.T) *Hub {
	t.Helper()
	cfg := defaultConfig()
	cfg.Machines = nil
	cfg.dir = shortTempDir(t)
	h := &Hub{
		cfg: cfg, version: "test", logger: log.New(io.Discard, "", 0),
		byID: map[string]*Machine{}, clients: map[*client]struct{}{},
		calls: map[string]chan proto.ToolResult{},
		push:  newPusher(cfg.dir, nil, func(string, ...any) {}),
		// fake nodes start sessions they never list: do not wait the full listedWaitMax on each start
		listedWait: 50 * time.Millisecond,
	}
	h.live = newLiveActivities(h)
	playManagerAsks(h) // its own cards wait for h.resolve (fleet_helpers_test.go)
	h.push.hubID = h.hubID
	h.upd = newUpdater(h)
	h.upd.dir = filepath.Join(cfg.dir, "update")
	h.upd.exec = func(string) error { return errors.New("exec is not run in tests") }
	h.upd.startWatch = func(string, string) error { return nil }
	return h
}

// shortTempDir is a temporary directory short enough for the Unix sockets the hub makes under it
// (<dir>/extensions/<id>.sock, <dir>/extensions/run/<id>/http.sock). sun_path holds 104 bytes on macOS, and t.TempDir()
// under its /var/folders/…/T/ plus the test's name already takes most of them.
func shortTempDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return t.TempDir()
	}
	dir, err := os.MkdirTemp("/tmp", "ph")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestCallPhoneWithoutPhone(t *testing.T) {
	h := newTestHub(t)
	r := h.callPhone(context.Background(), "phone_device_info", json.RawMessage(`{}`))
	if !r.IsError || !strings.Contains(textOf(r), "not connected") {
		t.Fatalf("unexpected result %+v", r)
	}
}

func TestToolResultDeliversToWaiter(t *testing.T) {
	h := newTestHub(t)
	ch := make(chan proto.ToolResult, 1)
	h.mu.Lock()
	h.calls["c1"] = ch
	h.mu.Unlock()
	h.toolResult("c1", proto.TextResult("hello", false))
	select {
	case r := <-ch:
		if textOf(r) != "hello" {
			t.Fatalf("result = %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("tool result was not delivered")
	}
	// An unknown call id is ignored rather than blocking.
	h.toolResult("nope", proto.TextResult("x", false))
}

func TestPhoneToolsPersist(t *testing.T) {
	url := os.Getenv("PERSONA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PERSONA_TEST_DATABASE_URL not set (scripts/test/test-server.sh runs this on the hub machine)")
	}
	database, err := db.Open(context.Background(), url, db.HubSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	h := newTestHub(t)
	h.store = database
	tools := []proto.Tool{{Name: "phone_device_info", Description: "info", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	h.setPhoneTools(db.PhoneToolRegistration{DeviceID: "legacy", Tools: tools})
	if got := h.phoneToolList(); len(got) != 1 || got[0].Name != "phone_device_info" {
		t.Fatalf("tools = %+v", got)
	}
	reloaded := newTestHub(t)
	reloaded.store = database
	reloaded.loadPhoneTools()
	if got := reloaded.phoneToolList(); len(got) != 1 || got[0].Name != "phone_device_info" {
		t.Fatalf("reloaded tools = %+v", got)
	}
}

func textOf(r proto.ToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

func TestPhoneToolsDeviceRouting(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	iphone := &client{hub: h, device: "credential-ios", role: roleFull, out: make(chan []byte, 8)}
	android := &client{hub: h, device: "credential-android", role: roleFull, out: make(chan []byte, 8)}
	h.clients[iphone], h.clients[android] = struct{}{}, struct{}{}
	// Existing iOS omits both new fields; Android supplies its own device ID.
	h.onClientMessage(ctx, iphone, []byte(`{"type":"register_phone","tools":[{"name":"phone_reminders_list"},{"name":"phone_device_info","description":"Detailed device information"}]}`))
	h.onClientMessage(ctx, android, []byte(`{"type":"register_phone","device_id":"android-device","platform":"android","simulator":true,"tools":[{"name":"phone_notifications_list"},{"name":"phone_device_info","description":"Info"}]}`))
	if iphone.phoneID != iphone.device || android.phoneID != "android-device" {
		t.Fatalf("device IDs: %q, %q", iphone.phoneID, android.phoneID)
	}
	tools := h.phoneToolList()
	if len(tools) != 3 {
		t.Fatalf("merged tools = %+v", tools)
	}
	for _, tool := range tools {
		if tool.Name == "phone_device_info" && tool.Description != "Detailed device information" {
			t.Fatalf("incomplete description: %+v", tool)
		}
	}
	for _, tc := range []struct {
		name string
		want *client
	}{
		{"phone_reminders_list", iphone},
		{"phone_notifications_list", android},
		{"phone_device_info", iphone},
	} {
		done := make(chan proto.ToolResult, 1)
		go func() { done <- h.callPhone(ctx, tc.name, json.RawMessage(`{}`)) }()
		var data []byte
		select {
		case data = <-iphone.out:
			if tc.want != iphone {
				t.Fatalf("%s routed to iOS", tc.name)
			}
		case data = <-android.out:
			if tc.want != android {
				t.Fatalf("%s routed to Android", tc.name)
			}
		case <-ctx.Done():
			t.Fatal("tool call was not routed")
		}
		var call struct {
			Name   string `json:"name"`
			CallID string `json:"call_id"`
		}
		if err := json.Unmarshal(data, &call); err != nil || call.Name != tc.name {
			t.Fatalf("tool call = %s, %v", data, err)
		}
		reply, _ := json.Marshal(map[string]any{"type": "tool_result", "call_id": call.CallID, "content": []proto.Content{{Type: "text", Text: tc.name}}})
		h.onClientMessage(ctx, tc.want, reply)
		select {
		case result := <-done:
			if result.IsError || textOf(result) != tc.name {
				t.Fatalf("result = %+v", result)
			}
		case <-ctx.Done():
			t.Fatal("tool result was not delivered")
		}
	}
	// With both devices real, the last active eligible connection wins.
	h.mu.Lock()
	android.sim = false
	h.mu.Unlock()
	h.onClientMessage(ctx, android, []byte(`{"type":"ping"}`))
	if got := h.phoneClient("phone_device_info"); got != android {
		t.Fatal("recently active device was not selected")
	}
	// An empty registration removes only this device's capabilities.
	h.onClientMessage(ctx, android, []byte(`{"type":"register_phone","device_id":"android-device","platform":"android","tools":[]}`))
	if h.phoneClient("phone_notifications_list") != nil || len(h.phoneToolList()) != 2 {
		t.Fatal("empty registration did not remove Android tools")
	}
	h.mu.Lock()
	delete(h.clients, iphone)
	h.mu.Unlock()
	h.revoked(iphone.device)
	if len(h.phoneToolList()) != 0 {
		t.Fatal("revoked credential retained tools")
	}
}

// A write the user approves on the phone outlasts the plain call's wait: an approval that comes late still reaches the
// agent, and a read still times out as before (#4427).
func TestCallPhoneWaitsForApproval(t *testing.T) {
	h := newTestHub(t)
	saved := phoneCallWait
	phoneCallWait = 50 * time.Millisecond
	t.Cleanup(func() { phoneCallWait = saved })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone := &client{hub: h, device: "credential-ios", role: roleFull, out: make(chan []byte, 8)}
	h.clients[phone] = struct{}{}
	h.onClientMessage(ctx, phone, []byte(`{"type":"register_phone","tools":[{"name":"phone_reminders_create"},{"name":"phone_device_info"}]}`))

	if r := h.callPhone(ctx, "phone_device_info", json.RawMessage(`{}`)); !r.IsError || !strings.Contains(textOf(r), "Timed out waiting for the iPhone") {
		t.Fatalf("read past the wait = %+v", r)
	}
	<-phone.out

	done := make(chan proto.ToolResult, 1)
	go func() { done <- h.callPhone(ctx, "phone_reminders_create", json.RawMessage(`{"title":"x"}`)) }()
	var call struct {
		CallID string `json:"call_id"`
	}
	select {
	case data := <-phone.out:
		if err := json.Unmarshal(data, &call); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("tool call was not sent")
	}
	time.Sleep(4 * phoneCallWait) // the user is still deciding
	select {
	case r := <-done:
		t.Fatalf("the write gave up while the user was deciding: %+v", r)
	default:
	}
	reply, _ := json.Marshal(map[string]any{"type": "tool_result", "call_id": call.CallID, "content": []proto.Content{{Type: "text", Text: "saved"}}})
	h.onClientMessage(ctx, phone, reply)
	select {
	case r := <-done:
		if r.IsError || textOf(r) != "saved" {
			t.Fatalf("result = %+v", r)
		}
	case <-ctx.Done():
		t.Fatal("the late approval did not reach the agent")
	}
}

// Every phone tool that waits for the user (an approval card, or a system sheet the user finishes) gets the long wait;
// a plain read or a tool that only opens another app does not (#2276–#2286).
func TestPhoneToolsThatWaitForTheUser(t *testing.T) {
	for _, name := range []string{
		"phone_calendar_update_event", "phone_calendar_delete_event", "phone_alarm_create", "phone_timer_start",
		"phone_alarm_cancel", "phone_messages_compose", "phone_call_start", "phone_shortcuts_register",
		"phone_home_control", "phone_home_run_scene", "phone_contacts_create", "phone_contacts_update",
	} {
		if !phoneConfirmedTools[name] {
			t.Errorf("%s waits for the user on the phone but gets the short wait", name)
		}
	}
	for _, name := range []string{
		"phone_maps_search_nearby", "phone_maps_navigate", "phone_media_play", "phone_media_control", "phone_media_open",
		"phone_shortcuts_run", "phone_shortcuts_list", "phone_alarms_list", "phone_home_list",
	} {
		if phoneConfirmedTools[name] {
			t.Errorf("%s asks nobody, it should not wait 30 minutes", name)
		}
	}
}
