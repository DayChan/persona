package assistant

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func assertTriageSettingsJSON(t *testing.T, status int, body map[string]any, want TriageSwitches) {
	t.Helper()
	if status != http.StatusOK || len(body) != 5 || body["enabled"] != want.Enabled ||
		body["auto_continue"] != want.AutoContinue || body["wake"] != want.Wake || body["decide"] != want.Decide || body["tool_loop"] != want.ToolLoop {
		t.Fatalf("triage settings: %d %v, want 200 %+v", status, body, want)
	}
}

// An unread saved value must not be presented as an editable default. Failed reads remain retryable, while the
// triage itself still uses hub.json's switches until it can read the user's settings.
func TestTriageSettingsReadFailureAndRecovery(t *testing.T) {
	ctx := context.Background()
	on := true
	f := newFakeHub()
	f.hello.Agents.Triage = assistantwire.TriageConfig{Enabled: &on, AutoContinue: &on, Wake: &on, Decide: &on}
	st := &memStore{}
	saved := TriageSwitches{Enabled: true, Decide: true}
	if err := st.PutSetting(ctx, triageSettingKey, saved); err != nil {
		t.Fatal(err)
	}
	s := newServiceOn(t, f, st)
	st.mu.Lock()
	st.failSetting = errors.New("database unavailable")
	st.mu.Unlock()

	for _, tc := range []struct{ lang, want string }{
		{"zh-Hans", "读取会话分诊设置失败：database unavailable"},
		{"en", "Could not read session triage settings: database unavailable"},
		{"ja", "セッションのトリアージ設定を読み込めませんでした：database unavailable"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			status, body := serveJSON(t, s, "GET", "/api/v1/agents/triage", tc.lang, "")
			if status != http.StatusServiceUnavailable || body["error"] != tc.want {
				t.Fatalf("read failure: %d %v, want 503 %q", status, body, tc.want)
			}
			for _, key := range []string{"enabled", "auto_continue", "wake", "decide"} {
				if _, ok := body[key]; ok {
					t.Fatalf("read failure includes editable switch %q: %v", key, body)
				}
			}
		})
	}
	if got := s.triageSwitches(ctx); got != (TriageSwitches{Enabled: true, AutoContinue: true, Wake: true, Decide: true}) {
		t.Fatalf("internal fallback = %+v, want hub.json's switches", got)
	}
	if status, body := serveJSON(t, s, "GET", "/api/v1/agents/triage", "en", ""); status != http.StatusServiceUnavailable {
		t.Fatalf("internal fallback was cached as settings: %d %v", status, body)
	}

	st.mu.Lock()
	st.failSetting = nil
	st.mu.Unlock()
	status, body := serveJSON(t, s, "GET", "/api/v1/agents/triage", "en", "")
	assertTriageSettingsJSON(t, status, body, saved)
	if got := s.triageSwitches(ctx); got != saved {
		t.Fatalf("recovered internal switches = %+v, want %+v", got, saved)
	}

	// Once read successfully, the known settings keep their existing cache behavior.
	st.mu.Lock()
	st.failSetting = errors.New("database unavailable again")
	st.mu.Unlock()
	status, body = serveJSON(t, s, "GET", "/api/v1/agents/triage", "en", "")
	assertTriageSettingsJSON(t, status, body, saved)
}

// No saved setting (or no store) is a valid default, not a database failure.
func TestTriageSettingsDefaults(t *testing.T) {
	on := true
	for _, tc := range []struct {
		name       string
		storeless  bool
		configured bool
	}{
		{name: "unset"},
		{name: "configured", configured: true},
		{name: "storeless", storeless: true},
		{name: "storeless configured", storeless: true, configured: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub()
			want := TriageSwitches{}
			if tc.configured {
				f.hello.Agents.Triage = assistantwire.TriageConfig{Enabled: &on, Wake: &on}
				want = TriageSwitches{Enabled: true, Wake: true}
			}
			var s *Service
			if tc.storeless {
				s = newStorelessService(t, f)
			} else {
				s = newTestService(t, f)
			}
			status, body := serveJSON(t, s, "GET", "/api/v1/agents/triage", "en", "")
			assertTriageSettingsJSON(t, status, body, want)
		})
	}
}

// PUT does not depend on reading the previous switches; validation and role checks still apply during an outage.
func TestTriageSettingsPutAfterReadFailure(t *testing.T) {
	st := &memStore{failSetting: errors.New("database unavailable")}
	s := newServiceOn(t, newFakeHub(), st)
	if status, body := serveJSON(t, s, "GET", "/api/v1/agents/triage", "en", ""); status != http.StatusServiceUnavailable {
		t.Fatalf("read failure: %d %v", status, body)
	}
	if status, body := serveJSON(t, s, "PUT", "/api/v1/agents/triage", "en", `{"enabled":"yes"}`); status != http.StatusBadRequest {
		t.Fatalf("invalid PUT: %d %v", status, body)
	}
	if w := serveAs(s, "answer", "PUT", "/api/v1/agents/triage", `{"enabled":true}`); w.Code != http.StatusForbidden {
		t.Fatalf("answer-only PUT: %d %s", w.Code, w.Body)
	}
	if st.puts != 0 {
		t.Fatalf("rejected PUT saved settings %d times", st.puts)
	}

	want := TriageSwitches{Enabled: true, AutoContinue: true, Wake: true}
	status, body := serveJSON(t, s, "PUT", "/api/v1/agents/triage", "en", `{"enabled":true,"auto_continue":true,"wake":true,"decide":false}`)
	assertTriageSettingsJSON(t, status, body, want)
	status, body = serveJSON(t, s, "GET", "/api/v1/agents/triage", "en", "")
	assertTriageSettingsJSON(t, status, body, want)

	st.mu.Lock()
	st.failSetting = nil
	st.mu.Unlock()
	var saved TriageSwitches
	if ok, err := st.Setting(context.Background(), triageSettingKey, &saved); err != nil || !ok || saved != want {
		t.Fatalf("saved switches = %+v, found %v, error %v; want %+v", saved, ok, err, want)
	}
}
