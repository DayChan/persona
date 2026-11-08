package transcript

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// What failed a Codex turn, in a rollout's words (snake_case) and an app-server's (camelCase), sorted into worth a
// "Continue" or the user's to fix (#2185).
func TestCodexTurnError(t *testing.T) {
	for _, tc := range []struct {
		name, raw, text, kind string
	}{
		{"none", ``, "", ""},
		{"null", `null`, "", ""},
		{"rollout 503 (Codex 0.166, measured)",
			`{"message":"unexpected status 503 Service Unavailable: upstream overloaded","codex_error_info":{"http_connection_failed":{"http_status_code":503}}}`,
			"unexpected status 503 Service Unavailable: upstream overloaded", proto.TurnErrorTransient},
		{"rollout 429", `{"message":"m","codex_error_info":{"http_connection_failed":{"http_status_code":429}}}`, "m", proto.TurnErrorTransient},
		{"rollout 401", `{"message":"m","codex_error_info":{"http_connection_failed":{"http_status_code":401}}}`, "m", proto.TurnErrorUser},
		{"rollout 400", `{"message":"m","codex_error_info":{"response_stream_connection_failed":{"http_status_code":400}}}`, "m", proto.TurnErrorUser},
		{"stream lost, no status", `{"message":"stream disconnected before completion","codex_error_info":{"response_stream_disconnected":{"http_status_code":null}}}`,
			"stream disconnected before completion", proto.TurnErrorTransient},
		{"retries used up on 502", `{"message":"m","codex_error_info":{"response_too_many_failed_attempts":{"http_status_code":502}}}`, "m", proto.TurnErrorTransient},
		{"overloaded by name", `{"message":"m","codex_error_info":"server_overloaded"}`, "m", proto.TurnErrorTransient},
		{"rate limit", `{"message":"m","codex_error_info":"rate_limit_exceeded"}`, "m", proto.TurnErrorTransient},
		{"usage limit", `{"message":"You've hit your usage limit.","codex_error_info":"usage_limit_exceeded"}`, "You've hit your usage limit.", proto.TurnErrorUsageLimit},
		{"usage limit, app-server", `{"message":"m","codexErrorInfo":"usageLimitExceeded"}`, "m", proto.TurnErrorUsageLimit},
		{"usage limit reached", `{"message":"m","codex_error_info":"usage_limit_reached"}`, "m", proto.TurnErrorUsageLimit},
		{"context window", `{"message":"m","codex_error_info":"context_window_exceeded"}`, "m", proto.TurnErrorUser},
		{"a kind newer Codex adds", `{"message":"m","codex_error_info":"brand_new_kind"}`, "m", proto.TurnErrorUser},
		{"other: the text decides", `{"message":"m","codex_error_info":"other"}`, "m", ""},
		{"no kind", `{"message":"m"}`, "m", ""},
		{"app-server 503", `{"message":"m","codexErrorInfo":{"httpConnectionFailed":{"httpStatusCode":503}},"additionalDetails":null}`, "m", proto.TurnErrorTransient},
		{"app-server unauthorized", `{"message":"m","codexErrorInfo":"unauthorized"}`, "m", proto.TurnErrorUser},
		{"app-server overloaded", `{"message":"m","codexErrorInfo":"serverOverloaded"}`, "m", proto.TurnErrorTransient},
		{"no message", `{"codex_error_info":"server_overloaded"}`, CodexTurnFailed, proto.TurnErrorTransient},
		{"just a message", `"boom"`, "boom", ""},
		{"not an object", `[1]`, CodexTurnFailed, ""},
	} {
		text, kind := CodexTurnError(json.RawMessage(tc.raw))
		if text != tc.text || kind != tc.kind {
			t.Errorf("%s: CodexTurnError(%s) = %q, %q; want %q, %q", tc.name, tc.raw, text, kind, tc.text, tc.kind)
		}
	}
}

// The rollout's latest turn says it failed until a turn starts again; one the user stopped is no failure.
func TestCodexRolloutTurnError(t *testing.T) {
	failed := `{"timestamp":"2026-01-01T00:00:01Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1",` +
		`"last_agent_message":null,"error":{"message":"unexpected status 503","codex_error_info":{"http_connection_failed":{"http_status_code":503}}}}}`
	var m Meta
	for i, line := range []string{
		`{"timestamp":"2026-01-01T00:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		failed,
	} {
		ParseLine(i, []byte(line), &m)
	}
	if m.TurnError != "unexpected status 503" || m.TurnErrorKind != proto.TurnErrorTransient || !m.TurnClosed {
		t.Fatalf("failed turn: %q, %q, closed %v", m.TurnError, m.TurnErrorKind, m.TurnClosed)
	}
	ParseLine(3, []byte(`{"timestamp":"2026-01-01T00:00:02Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}`), &m)
	if m.TurnError != "" || m.TurnErrorKind != "" {
		t.Fatalf("next turn started: %q, %q", m.TurnError, m.TurnErrorKind)
	}
	ParseLine(4, []byte(`{"timestamp":"2026-01-01T00:00:03Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t2","error":null}}`), &m)
	if m.TurnError != "" {
		t.Fatalf("turn ended well: %q", m.TurnError)
	}
	ParseLine(5, []byte(failed), &m)
	ParseLine(6, []byte(`{"timestamp":"2026-01-01T00:00:04Z","type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t3"}}`), &m)
	if m.TurnError != "" {
		t.Fatalf("stopped by the user: %q", m.TurnError)
	}
}
