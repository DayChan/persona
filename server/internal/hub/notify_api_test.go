package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
)

func TestNotifyRequestValidate(t *testing.T) {
	long := strings.Repeat("长", notifyTitleMost+1)
	for _, tc := range []struct {
		name, body, err, level string
	}{
		{"default level", `{"title":"t"}`, "", levelActive},
		{"time-sensitive", `{"body":"b","level":"time-sensitive"}`, "", levelTimeSensitive},
		{"empty", `{"title":"  ","body":""}`, "title or body is required", ""},
		{"critical refused", `{"title":"t","level":"critical"}`, "level must be", ""},
		{"passive refused", `{"title":"t","level":"passive"}`, "level must be", ""},
		{"session without machine", `{"title":"t","session_id":"s"}`, "go together", ""},
		{"title too long", `{"title":"` + long + `"}`, "too long", ""},
		{"id too long", `{"title":"t","id":"` + strings.Repeat("x", notifyIDMost+1) + `"}`, "too long", ""},
		{"ui too long", `{"title":"t","ui":"` + strings.Repeat("u", notifyUIMost+1) + `"}`, "ui is too long", ""},
		{"state too long", `{"title":"t","state":"` + strings.Repeat("s", notifyStateMost+1) + `"}`, "state is too long", ""},
		{"actions max 3", `{"title":"t","actions":[{"id":"1","title":"a"},{"id":"2","title":"b"},{"id":"3","title":"c"},{"id":"4","title":"d"}]}`, "cannot have more than 3", ""},
		{"action id required", `{"title":"t","actions":[{"id":"","title":"a"}]}`, "action id is required", ""},
		{"action id too long", `{"title":"t","actions":[{"id":"` + strings.Repeat("i", notifyActionIDMost+1) + `","title":"a"}]}`, "action id is too long", ""},
		{"action id duplicate", `{"title":"t","actions":[{"id":"dup","title":"a"},{"id":"dup","title":"b"}]}`, "action ids must be unique", ""},
		{"action title required", `{"title":"t","actions":[{"id":"1","title":"  "}]}`, "action title is required", ""},
		{"action title too long", `{"title":"t","actions":[{"id":"1","title":"` + strings.Repeat("字", notifyActionTitleMost+1) + `"}]}`, "action title is too long", ""},
		{"action role bad", `{"title":"t","actions":[{"id":"1","title":"a","role":"unknown"}]}`, "role must be normal or destructive", ""},
		{"action placeholder too long", `{"title":"t","actions":[{"id":"1","title":"a","input":{"placeholder":"` + strings.Repeat("p", notifyActionInputPlaceholderMost+1) + `"}}]}`, "placeholder is too long", ""},
		{"valid actions", `{"title":"t","actions":[{"id":"raise","title":"预算加 ¥10"},{"id":"note","title":"回复","input":{"placeholder":"说点什么"}},{"id":"stop","title":"停用","role":"destructive","foreground":true}],"ui":"usage","state":"range=1d"}`, "", levelActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r notifyRequest
			if err := json.Unmarshal([]byte(tc.body), &r); err != nil {
				t.Fatal(err)
			}
			err := r.validate()
			if tc.err == "" {
				if err != nil || r.Level != tc.level {
					t.Fatalf("err %v level %q", err, r.Level)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("err %v, want %q", err, tc.err)
			}
		})
	}
}

func TestNotifyPayload(t *testing.T) {
	p := notifyPayload(notifyRequest{Title: "卡住了", Body: "API 错误 30 分钟", MachineID: "m1", SessionID: "s1", Level: levelTimeSensitive}, "笔记本", "")
	aps := p["aps"].(map[string]any)
	if aps["interruption-level"] != levelTimeSensitive || aps["thread-id"] != "s1" || aps["sound"] != "default" {
		t.Fatalf("aps %v", aps)
	}
	alert := aps["alert"].(map[string]any)
	if alert["title"] != "卡住了" || alert["subtitle"] != "笔记本" || alert["body"] != "API 错误 30 分钟" {
		t.Fatalf("alert %v", alert)
	}
	if p["machine_id"] != "m1" || p["session_id"] != "s1" {
		t.Fatalf("ids %v", p)
	}
	if _, ok := p["extension_notification"]; ok {
		t.Fatalf("extension_notification for non-extension caller: %v", p)
	}
	// Without a session: no thread, no subtitle, nothing to open.
	p = notifyPayload(notifyRequest{Title: "t", Level: levelActive}, "", "")
	aps = p["aps"].(map[string]any)
	if _, ok := aps["thread-id"]; ok {
		t.Fatalf("thread-id without a session: %v", aps)
	}
	if _, ok := aps["alert"].(map[string]any)["subtitle"]; ok {
		t.Fatalf("subtitle without a machine: %v", aps)
	}
	// The pair is still there, empty: Android decodes the WebSocket event's ids as non-null strings.
	if p["machine_id"] != "" || p["session_id"] != "" {
		t.Fatalf("ids without a session: %v", p)
	}

	// Extension caller with actions, ui, and state:
	actions := []wire.NotifyAction{
		{ID: "raise", Title: "预算加 ¥10"},
		{ID: "note", Title: "回复", Input: &wire.NotifyActionInput{Placeholder: "说点什么"}},
		{ID: "stop", Title: "停用这个 Key", Role: "destructive", Foreground: true},
	}
	req := notifyRequest{
		Title:   "Example 网关面板",
		Body:    "今天的预算用了 90%",
		UI:      "usage",
		State:   "range=1d",
		Actions: actions,
		Level:   levelActive,
	}
	p = notifyPayload(req, "", "example-gateway")
	aps = p["aps"].(map[string]any)
	if aps["thread-id"] != "extension.example-gateway" {
		t.Fatalf("thread-id for extension: %v", aps["thread-id"])
	}
	expectedHash := actionIDsHash(actions)
	if len(expectedHash) != 8 {
		t.Fatalf("actionIDsHash length: %d, want 8", len(expectedHash))
	}
	if aps["category"] != "ext.example-gateway."+expectedHash {
		t.Fatalf("category: %v, want ext.example-gateway.%s", aps["category"], expectedHash)
	}
	en, ok := p["extension_notification"].(map[string]any)
	if !ok || en["extension"] != "example-gateway" || en["ui"] != "usage" || en["state"] != "range=1d" {
		t.Fatalf("extension_notification %v", p["extension_notification"])
	}
	enActions, ok := en["actions"].([]wire.NotifyAction)
	if !ok || len(enActions) != 3 || enActions[0].ID != "raise" {
		t.Fatalf("extension actions: %v", enActions)
	}

	// Extension caller with session_id: threads by session instead of extension.<id>.
	req.SessionID, req.MachineID = "sess-1", "m1"
	p = notifyPayload(req, "笔记本", "example-gateway")
	aps = p["aps"].(map[string]any)
	if aps["thread-id"] != "sess-1" {
		t.Fatalf("thread-id with session: %v", aps["thread-id"])
	}
}

func TestNotifyPayloadMutableContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		extID   string
		actions []wire.NotifyAction
		silent  bool
		session string
		mutable bool
	}{
		{name: "ordinary alert"},
		{name: "ordinary caller cannot set a dynamic category", actions: []wire.NotifyAction{{ID: "reply", Title: "Reply"}}},
		{name: "extension metadata only", extID: "example"},
		{name: "empty actions", extID: "example", actions: []wire.NotifyAction{}},
		{name: "extension buttons", extID: "example", actions: []wire.NotifyAction{{ID: "reply", Title: "Reply"}}, mutable: true},
		{name: "silent extension buttons", extID: "example", actions: []wire.NotifyAction{{ID: "reply", Title: "Reply"}}, silent: true, mutable: true},
		{name: "session extension buttons", extID: "example", actions: []wire.NotifyAction{{ID: "reply", Title: "Reply"}}, session: "session-1", mutable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := notifyPayload(notifyRequest{Title: "Example alert", Level: levelTimeSensitive, UI: "details", State: "range=1d", Actions: tc.actions, Silent: tc.silent, SessionID: tc.session}, "", tc.extID)
			aps := payload["aps"].(map[string]any)
			if value, present := aps["mutable-content"]; tc.mutable {
				if !present || value != 1 {
					t.Fatalf("mutable-content = %v, want 1 for dynamic buttons", value)
				}
			} else if present {
				t.Fatalf("mutable-content = %v for notification without dynamic buttons", value)
			}
			if aps["interruption-level"] != levelTimeSensitive {
				t.Fatalf("interruption-level changed: %v", aps)
			}
			if _, present := aps["sound"]; present == tc.silent {
				t.Fatalf("silent behavior changed: %v", aps)
			}
		})
	}
}

func TestNotifyAPI(t *testing.T) {
	h := newTestHub(t)
	c := &client{device: "phone-1", out: make(chan []byte, 8)}
	h.clients[c] = struct{}{}
	h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
	<-c.out // client_hello_ack
	h.mutes = &Pins{Spaces: []string{}, Sessions: []string{"m1/muted"}}
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	post := func(token, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/notify", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	next := func() map[string]any {
		t.Helper()
		select {
		case raw := <-c.out:
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			return m
		default:
			return nil
		}
	}

	if code, _ := post("wrong", `{"title":"t"}`); code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", code)
	}
	if code, _ := post(h.cfg.Token, `{"title":"t","level":"critical"}`); code != http.StatusBadRequest {
		t.Fatalf("bad level: %d", code)
	}

	// Non-extension caller cannot supply actions, ui, or state.
	if code, out := post(h.cfg.Token, `{"title":"t","actions":[{"id":"1","title":"btn"}]}`); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "extension") {
		t.Fatalf("non-extension actions allowed: %d %v", code, out)
	}
	if code, out := post(h.cfg.Token, `{"title":"t","ui":"usage"}`); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "extension") {
		t.Fatalf("non-extension ui allowed: %d %v", code, out)
	}
	if code, out := post(h.cfg.Token, `{"title":"t","state":"foo"}`); code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "extension") {
		t.Fatalf("non-extension state allowed: %d %v", code, out)
	}

	code, out := post(h.cfg.Token, `{"title":"会话卡住了","body":"API 错误已 30 分钟","machine_id":"m1","session_id":"s1","level":"time-sensitive"}`)
	if code != 200 || out["sent"] != true {
		t.Fatalf("send: %d %v", code, out)
	}
	m := next()
	if m == nil || m["type"] != "notify" || m["kind"] != kindAlert || m["session_id"] != "s1" || m["title"] != "会话卡住了" {
		t.Fatalf("ws notify: %v", m)
	}
	// Without a session the ids are empty strings, never null.
	if code, _ := post(h.cfg.Token, `{"title":"只是告诉你"}`); code != 200 {
		t.Fatalf("send: %d", code)
	}
	if m := next(); m == nil || m["machine_id"] != "" || m["session_id"] != "" {
		t.Fatalf("ws notify without a session: %v", m)
	}
	// The caller's id rides along, so Android replaces the earlier alert as APNs does (#5614); none without one.
	if code, _ := post(h.cfg.Token, `{"title":"进度 50%","id":"progress-1"}`); code != 200 {
		t.Fatalf("send: %d", code)
	}
	if m := next(); m == nil || m["id"] != "progress-1" {
		t.Fatalf("ws notify id: %v", m)
	}
	if code, _ := post(h.cfg.Token, `{"title":"t"}`); code != 200 {
		t.Fatalf("send: %d", code)
	}
	if m := next(); m == nil || m["id"] != nil {
		t.Fatalf("ws notify id without one: %v", m)
	}
	// Too big for APNs once encoded (escapes count): refused instead of reported as sent.
	if code, _ := post(h.cfg.Token, `{"title":"t","body":"`+strings.Repeat(`\u0001`, notifyBodyMost)+`"}`); code != http.StatusBadRequest {
		t.Fatalf("oversized: %d", code)
	}
	if code, _ := post(h.cfg.Token, `{"title":"t","machine_id":"m1","session_id":"`+strings.Repeat("s", notifyRefMost+1)+`"}`); code != http.StatusBadRequest {
		t.Fatalf("long session id: %d", code)
	}

	// Extension with notify scope calling notify with actions / ui / state:
	extTok := extTokensOf(t, h).issue("example-gateway", "Example Gateway", []string{extension.ScopeNotify})
	code, out = post(extTok, `{"title":"Example 网关面板","body":"今天的预算用了 90%","ui":"usage","state":"range=1d","actions":[{"id":"raise","title":"预算加 ¥10"},{"id":"note","title":"回复","input":{"placeholder":"说点什么"}},{"id":"stop","title":"停用这个 Key","role":"destructive","foreground":true}]}`)
	if code != 200 || out["sent"] != true {
		t.Fatalf("extension send: %d %v", code, out)
	}
	m = next()
	if m == nil || m["type"] != "notify" || m["title"] != "Example 网关面板" {
		t.Fatalf("ws notify from extension: %v", m)
	}
	en, ok := m["extension_notification"].(map[string]any)
	if !ok || en["extension"] != "example-gateway" || en["ui"] != "usage" || en["state"] != "range=1d" {
		t.Fatalf("ws notify extension_notification: %v", en)
	}
	rawActions, ok := en["actions"].([]any)
	if !ok || len(rawActions) != 3 {
		t.Fatalf("ws notify actions: %v", rawActions)
	}

	// Extension without notify scope cannot call notify.
	badExtTok := extTokensOf(t, h).issue("other-extension", "Other", []string{extension.ScopeMachinesRead})
	if code, _ := post(badExtTok, `{"title":"t"}`); code != http.StatusForbidden {
		t.Fatalf("extension without notify scope: %d", code)
	}

	// A muted session: nothing goes out, and the caller is told why.
	code, out = post(h.cfg.Token, `{"title":"t","machine_id":"m1","session_id":"muted"}`)
	if code != 200 || out["sent"] != false || out["reason"] != "muted" {
		t.Fatalf("muted: %d %v", code, out)
	}
	if m := next(); m != nil {
		t.Fatalf("muted session notified: %v", m)
	}

	// A device that turned "Needs you" off gets none.
	if err := h.push.setPrefs("phone-1", NotifyPrefs{Input: false, Done: DoneLong}); err != nil {
		t.Fatal(err)
	}
	if code, out := post(h.cfg.Token, `{"title":"t"}`); code != 200 || out["sent"] != false || out["reason"] != "filtered" {
		t.Fatalf("input off: %d %v", code, out)
	}
	if m := next(); m != nil {
		t.Fatalf("input off but notified: %v", m)
	}
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	if code, out := post(extTok, `{"title":"Build finished"}`); code != 200 || out["sent"] != false || out["reason"] != "no_device" {
		t.Fatalf("extension without a recipient: %d %v", code, out)
	}
}

// An answer-only credential (the watch) cannot send notifications.
func TestNotifyNotForAnswerOnly(t *testing.T) {
	if allowed(principal{Role: roleAnswer}, http.MethodPost, "/api/v1/notify") {
		t.Fatal("answer-only credential may notify")
	}
}

// Through APNs: the level and the prefixed collapse id reach the push; "Phone Muted" holds it while a desktop is online.
func TestNotifyAPNs(t *testing.T) {
	h, f := macHub(t)
	// Ordinary alerts do not use the Live Activity-only store from testLive.
	// Leave API authentication on this fixture's original in-memory path.
	h.store = nil
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	post := func(token, body string) {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/notify", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("notify: %v %v", err, resp)
		}
		resp.Body.Close()
	}
	post(h.cfg.Token, `{"title":"卡住了","body":"b","machine_id":"m1","session_id":"s1","level":"time-sensitive","id":"alert-1"}`)
	calls := f.calls(t, 2) // the phone and the Mac
	for _, c := range calls {
		var body map[string]any
		_ = json.Unmarshal(c.Body, &body)
		aps := body["aps"].(map[string]any)
		if c.Type != "alert" || aps["interruption-level"] != levelTimeSensitive || aps["thread-id"] != "s1" {
			t.Fatalf("push %s: %s", c.Token, c.Body)
		}
		if _, ok := aps["mutable-content"]; ok {
			t.Fatalf("ordinary alert invokes the service extension: %v", aps)
		}
	}
	for _, c := range calls {
		if c.Collapse != notifyCollapse+"alert-1" {
			t.Fatalf("collapse id %q", c.Collapse)
		}
	}

	// Extension notification carries aps.category, thread-id, and extension_notification:
	extTok := extTokensOf(t, h).issue("example-gateway", "Example Gateway", []string{extension.ScopeNotify})
	beforeExt := len(f.calls(t, 2))
	post(extTok, `{"title":"预算超了","body":"90%","ui":"usage","state":"range=1d","actions":[{"id":"raise","title":"加预算"},{"id":"stop","title":"停用","role":"destructive"}]}`)
	callsExt := f.calls(t, beforeExt+2)[beforeExt:]
	for _, c := range callsExt {
		var body map[string]any
		_ = json.Unmarshal(c.Body, &body)
		aps := body["aps"].(map[string]any)
		if aps["thread-id"] != "extension.example-gateway" {
			t.Fatalf("apns thread-id: %v", aps["thread-id"])
		}
		cat, ok := aps["category"].(string)
		if !ok || !strings.HasPrefix(cat, "ext.example-gateway.") || len(cat) != len("ext.example-gateway.")+8 {
			t.Fatalf("apns category: %v", cat)
		}
		if aps["mutable-content"] != 1.0 {
			t.Fatalf("direct APNs cannot register first-notification buttons: %v", aps)
		}
		en, ok := body["extension_notification"].(map[string]any)
		if !ok || en["extension"] != "example-gateway" || en["ui"] != "usage" {
			t.Fatalf("apns extension_notification: %v", en)
		}
	}

	// Extension metadata without buttons does not invoke the service extension.
	beforePlain := len(f.calls(t, beforeExt+2))
	post(extTok, `{"title":"预算状态","ui":"usage","state":"range=1d"}`)
	for _, c := range f.calls(t, beforePlain+2)[beforePlain:] {
		var body map[string]any
		_ = json.Unmarshal(c.Body, &body)
		aps := body["aps"].(map[string]any)
		if _, ok := aps["mutable-content"]; ok {
			t.Fatalf("extension without buttons invokes the service extension: %v", aps)
		}
		if _, ok := aps["category"]; ok {
			t.Fatalf("extension without buttons has a dynamic category: %v", aps)
		}
	}

	// "Phone Muted" with the Mac app connected: the phone gets nothing.
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, MuteWhileDesktop: true}); err != nil {
		t.Fatal(err)
	}
	h.push.desktopOnline = func() bool { return true }
	before := len(f.calls(t, beforePlain+2))
	post(h.cfg.Token, `{"title":"t"}`)
	time.Sleep(200 * time.Millisecond)
	for _, c := range f.calls(t, before)[before:] {
		if c.Token == "devtok" {
			t.Fatalf("muted phone got a push: %s", c.Body)
		}
	}
}

// --silent drops the sound and nothing else.
func TestNotifyPayloadSilent(t *testing.T) {
	aps := notifyPayload(notifyRequest{Title: "t", Level: levelTimeSensitive, Silent: true}, "", "")["aps"].(map[string]any)
	if _, ok := aps["sound"]; ok || aps["interruption-level"] != levelTimeSensitive {
		t.Fatalf("aps %v", aps)
	}
	if aps = notifyPayload(notifyRequest{Title: "t", Level: levelActive}, "", "")["aps"].(map[string]any); aps["sound"] != "default" {
		t.Fatalf("aps %v", aps)
	}
}

// A node's `personad notify` (the hub's "notify" request) goes out like one through the HTTP API, as that machine's, with the
// same muting and the same preferences; it cannot carry an extension's actions.
func TestNodeScriptNotify(t *testing.T) {
	h := newTestHub(t)
	c := &client{device: "phone-1", out: make(chan []byte, 8)}
	h.clients[c] = struct{}{}
	h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
	<-c.out // client_hello_ack
	h.mutes = &Pins{Spaces: []string{}, Sessions: []string{"box1/muted"}}
	m := &Machine{hub: h, cfg: MachineConfig{ID: "box1", Name: "Studio"}}
	call := func(params string) (map[string]any, error) {
		t.Helper()
		out, err := m.onRequest(context.Background(), "notify", json.RawMessage(params))
		if err != nil {
			return nil, err
		}
		return out.(map[string]any), nil
	}
	next := func() map[string]any {
		t.Helper()
		select {
		case raw := <-c.out:
			var msg map[string]any
			_ = json.Unmarshal(raw, &msg)
			return msg
		default:
			return nil
		}
	}

	out, err := call(`{"title":"⚠️ Tests failed","body":"3 failed","level":"time-sensitive","silent":true,"session_id":"s1","actions":[{"id":"x","title":"x"}],"ui":"u"}`)
	if err != nil || out["sent"] != true {
		t.Fatalf("send: %v %v", out, err)
	}
	msg := next()
	if msg == nil || msg["kind"] != kindAlert || msg["machine_id"] != "box1" || msg["session_id"] != "s1" ||
		msg["title"] != "⚠️ Tests failed" || msg["subtitle"] != "Studio" || msg["body"] != "3 failed" {
		t.Fatalf("ws notify: %v", msg)
	}
	if _, ok := msg["extension_notification"]; ok {
		t.Fatalf("a node is no extension: %v", msg)
	}

	// Without a session: the machine's name still says where it came from.
	if out, err = call(`{"title":"Build done"}`); err != nil || out["sent"] != true {
		t.Fatalf("send: %v %v", out, err)
	}
	if msg = next(); msg == nil || msg["machine_id"] != "" || msg["session_id"] != "" || msg["subtitle"] != "Studio" {
		t.Fatalf("ws notify without a session: %v", msg)
	}

	if out, err = call(`{"title":"t","session_id":"muted"}`); err != nil || out["sent"] != false || out["reason"] != "muted" {
		t.Fatalf("muted: %v %v", out, err)
	}
	if msg = next(); msg != nil {
		t.Fatalf("muted session notified: %v", msg)
	}
	if _, err = call(`{"title":"  "}`); err == nil {
		t.Fatal("an empty notification is refused")
	}
	if _, err = call(`{"title":"t","level":"critical"}`); err == nil {
		t.Fatal("critical is not offered")
	}
	if err := h.push.setPrefs(c.device, NotifyPrefs{Input: false}); err != nil {
		t.Fatal(err)
	}
	if out, err = call(`{"title":"Build finished"}`); err != nil || out["sent"] != false || out["reason"] != "filtered" {
		t.Fatalf("all recipients filtered: %v %v", out, err)
	}
	if msg = next(); msg != nil {
		t.Fatalf("filtered notification sent: %v", msg)
	}
	delete(h.clients, c)
	if out, err = call(`{"title":"Build finished"}`); err != nil || out["sent"] != false || out["reason"] != "no_device" {
		t.Fatalf("no recipient: %v %v", out, err)
	}
	if out, err = call(`{"title":"Build finished","session_id":"muted"}`); err != nil || out["reason"] != "muted" {
		t.Fatalf("muted session with no recipient: %v %v", out, err)
	}
}

// A browser only ever writes its prefs (Settings › Notifications) and never registers a push token: the device row its prefs made
// takes the device token that wrote them, so revoking that token (Settings › Devices) forgets it, as it does a phone.
// A device already registered keeps the credential it registered with.
func TestPrefsOnlyDeviceGoesWithItsCredential(t *testing.T) {
	h := newTestHub(t)
	if err := h.push.setPrefsBy("web-1", "dev_web", NotifyPrefs{Input: true, Done: DoneAlways}); err != nil {
		t.Fatal(err)
	}
	h.push.register(Device{DeviceID: "phone", APNSToken: "t", Credential: "dev_phone", CredentialID: "dev_phone"})
	if err := h.push.setPrefsBy("phone", "dev_web", NotifyPrefs{Input: false, Done: DoneLong}); err != nil {
		t.Fatal(err)
	}
	if d, ok := h.push.device("phone"); !ok || d.Credential != "dev_phone" {
		t.Fatalf("phone's credential = %+v", d)
	}
	h.push.signedOut("dev_web")
	if _, ok := h.push.device("web-1"); ok {
		t.Fatal("the browser's prefs outlived its credential")
	}
	if _, ok := h.push.device("phone"); !ok {
		t.Fatal("revoking the browser forgot the phone")
	}
}

// A script's notification within the node's character limits always goes out (docs/integrations/script-cli.md: cut, not
// refused), even when its bytes would not fit a push: 1000 emoji, or 1000 of < (six bytes each in JSON), #3798.
func TestNodeScriptNotifyCutsToFit(t *testing.T) {
	h := newTestHub(t)
	c := &client{device: "phone-1", out: make(chan []byte, 8), meta: clientMeta{features: map[string]bool{"ws_notify": true}}}
	h.clients[c] = struct{}{}
	m := &Machine{hub: h, cfg: MachineConfig{ID: "box1", Name: "Studio"}}
	for _, body := range []string{strings.Repeat("😀", notifyBodyMost), strings.Repeat("<", notifyBodyMost), strings.Repeat("a<b>", notifyBodyMost/4)} {
		in := notifyRequest{Title: strings.Repeat("标", notifyTitleMost), Body: body, Level: levelActive} // as validate leaves it
		params, _ := json.Marshal(in)
		out, err := m.onRequest(context.Background(), "notify", params)
		if err != nil || out.(map[string]any)["sent"] != true {
			t.Fatalf("body of %d bytes: %v %v", len(body), out, err)
		}
		cut := fitBody(in, "Studio")
		if !strings.HasSuffix(cut, "…") || !strings.HasPrefix(body, strings.TrimSuffix(cut, "…")) {
			t.Fatalf("cut body %q", cut)
		}
		in.Body = cut
		raw, _ := json.Marshal(notifyPayload(in, "Studio", ""))
		if len(raw) > notifyPayloadMost || len(raw) < notifyPayloadMost-16 {
			t.Fatalf("cut payload is %d bytes, want just under %d", len(raw), notifyPayloadMost)
		}
	}
	// One that fits is left as it is.
	if in := (notifyRequest{Title: "t", Body: strings.Repeat("a", notifyBodyMost)}); fitBody(in, "Studio") != in.Body {
		t.Fatal("a body that fits was cut")
	}
}
