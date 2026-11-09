package transcript

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

const refusalFallbackLine = `{"type":"system","subtype":"model_refusal_fallback","trigger":"refusal","direction":"retry","scope":"session","originalModel":"primary-model","fallbackModel":"fallback-model","requestId":"synthetic-request","apiRefusalCategory":"synthetic-policy","apiRefusalExplanation":"Upstream refused the request","refusedUserMessageUuid":"synthetic-user","retractedMessageUuids":[]}`
const refusalErrorLine = `{"type":"assistant","isApiErrorMessage":true,"error":"model_not_found","apiErrorStatus":404,"message":{"role":"assistant","model":"<synthetic>","content":[{"type":"text","text":"API Error: 404 fallback-model is not available"}]}}`

func TestModelRefusalNoticeKeepsOriginalSignal(t *testing.T) {
	for _, subtype := range []string{"model_refusal_fallback", "model_refusal_no_fallback", "model_refusal_future"} {
		t.Run(subtype, func(t *testing.T) {
			line := strings.ReplaceAll(refusalFallbackLine, "model_refusal_fallback", subtype)
			var m Meta
			it := only(t, ParseLine(1, []byte(line), &m))
			want := proto.ModelRefusal{Subtype: subtype, OriginalModel: "primary-model", FallbackModel: "fallback-model",
				Category: "synthetic-policy", Scope: "session", Explanation: "Upstream refused the request", Raw: line}
			if it.Kind != proto.KindNotice || it.Role != "system" || it.Text != line || it.ModelRefusal == nil || *it.ModelRefusal != want {
				t.Fatalf("notice lost refusal fields: %+v", it)
			}
			if (m.ModelFallback != nil) != (subtype == "model_refusal_fallback") {
				t.Fatalf("invented or lost fallback: %+v", m.ModelFallback)
			}
			if m.LastText != "" || !m.LastActivity.IsZero() || m.TurnOpen || m.TurnError != "" {
				t.Fatalf("a refusal notice became an answer or a failed turn: %+v", m)
			}
		})
	}
	for _, line := range []string{
		`{"type":"system","subtype":"model_refusal_fallback"}`,
		`{"type":"system","subtype":"model_refusal_future","apiRefusalCategory":{"unknown":true},"content":"Original refusal"}`,
	} {
		var m Meta
		it := only(t, ParseLine(1, []byte(line), &m))
		if it.Text != line || it.ModelRefusal.Raw != line || m.ModelFallback != nil {
			t.Fatalf("unknown or incomplete record was lost or became a fallback: %+v", it)
		}
	}
}

func TestModelRefusalFallbackLifetime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		line   string
		active bool
	}{
		{"next prompt", `{"type":"user","message":{"role":"user","content":"Another sample"}}`, true},
		{"same model answer", `{"type":"assistant","message":{"role":"assistant","model":"fallback-model","content":[{"type":"text","text":"An answer"}]}}`, true},
		{"different model answer", `{"type":"assistant","message":{"role":"assistant","model":"primary-model","content":[{"type":"text","text":"An answer"}]}}`, false},
		{"model command rejected", `{"type":"system","subtype":"local_command","content":"<local-command-stderr>Model not found</local-command-stderr>"}`, true},
		{"model command unchanged", `{"type":"system","subtype":"local_command","content":"<local-command-stdout>Kept model as fallback-model</local-command-stdout>"}`, true},
		{"model command confirmed", `{"type":"system","subtype":"local_command","content":"<local-command-stdout>Set model to primary-model</local-command-stdout>"}`, false},
		{"older model command confirmed", `{"type":"user","message":{"role":"user","content":"<local-command-stdout>Set model to primary-model</local-command-stdout>"}}`, false},
		{"unrelated notice", `{"type":"system","subtype":"informational","content":"Sample notice"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m Meta
			ParseLine(1, []byte(refusalFallbackLine), &m)
			ParseLine(2, []byte(refusalErrorLine), &m)
			ParseLine(3, []byte(tc.line), &m)
			if (m.ModelFallback != nil) != tc.active || !m.ModelFallbackObserved {
				t.Fatalf("fallback state %+v, observed %t", m.ModelFallback, m.ModelFallbackObserved)
			}
			ParseLine(4, []byte(refusalErrorLine), &m)
			if (m.TurnErrorCode == proto.TurnErrorModelFallbackUnavailable) != tc.active {
				t.Fatalf("wrong failure cause: %q", m.TurnErrorCode)
			}
		})
	}
	for _, scope := range []string{"turn", "request", "", "future"} {
		var m Meta
		ParseLine(1, []byte(strings.ReplaceAll(refusalFallbackLine, `"scope":"session"`, `"scope":"`+scope+`"`)), &m)
		ParseLine(2, []byte(refusalErrorLine), &m)
		if m.TurnErrorCode != proto.TurnErrorModelFallbackUnavailable {
			t.Fatalf("scope %q lost immediate fallback failure", scope)
		}
		m.ClaudeTurnStarted(false)
		if m.ModelFallback != nil || m.TurnErrorCode != "" {
			t.Fatalf("scope %q leaked to the next turn", scope)
		}
	}
}

func TestModelRefusalErrorRequiresSameSessionEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, line     string
		fallback, want bool
	}{
		{"no fallback", refusalErrorLine, false, false},
		{"model_not_found", strings.ReplaceAll(refusalErrorLine, `"apiErrorStatus":404,`, ""), true, true},
		{"structured 404", strings.ReplaceAll(refusalErrorLine, `"error":"model_not_found",`, ""), true, true},
		{"unrelated 404", strings.ReplaceAll(strings.ReplaceAll(refusalErrorLine, `"error":"model_not_found",`, ""), "fallback-model is not available", "The messages endpoint is not available"), true, false},
		{"other error", strings.ReplaceAll(strings.ReplaceAll(refusalErrorLine, "model_not_found", "server_error"), "404", "503"), true, false},
		{"quoted error", strings.ReplaceAll(refusalErrorLine, `"isApiErrorMessage":true`, `"isApiErrorMessage":false`), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var m Meta
			if tc.fallback {
				ParseLine(1, []byte(refusalFallbackLine), &m)
			}
			ParseLine(2, []byte(tc.line), &m)
			if (m.TurnErrorCode == proto.TurnErrorModelFallbackUnavailable) != tc.want {
				t.Fatalf("error code %q", m.TurnErrorCode)
			}
			if tc.want && (m.TurnErrorKind != proto.TurnErrorUser || m.TurnErrorResumes) {
				t.Fatalf("unavailable fallback is retryable: kind %q, resumes %t", m.TurnErrorKind, m.TurnErrorResumes)
			}
		})
	}
	var m Meta
	sidechain := strings.ReplaceAll(refusalFallbackLine, `"type":"system"`, `"type":"system","isSidechain":true`)
	if items := ParseLine(1, []byte(sidechain), &m); len(items) != 0 || m.ModelFallback != nil {
		t.Fatal("a subagent's fallback leaked into its parent")
	}
}

func TestModelRefusalFixtureLaterTurn(t *testing.T) {
	data, err := os.ReadFile("testdata/claude-refusal-fallback.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	var m Meta
	for i, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		ParseLine(i+1, line, &m)
		if i == 5 && (m.TurnErrorCode != "" || m.ModelFallback == nil) {
			t.Fatal("a new prompt must clear the error but retain the session fallback")
		}
	}
	if m.TurnErrorCode != proto.TurnErrorModelFallbackUnavailable || m.ModelFallback == nil || m.Model != "primary-model" {
		t.Fatalf("later turn lost the fallback cause or changed the observed model: %+v", m)
	}
	encoded, err := json.Marshal(proto.Session{ModelFallback: m.ModelFallback, TurnErrorCode: m.TurnErrorCode})
	if err != nil || !bytes.Contains(encoded, []byte(`"model_fallback":`)) {
		t.Fatalf("session wire lost the fallback: %s, %v", encoded, err)
	}
}

func TestObserveModelFallbackMatchesFullParse(t *testing.T) {
	data, err := os.ReadFile("testdata/claude-refusal-fallback.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	lines = append(lines,
		[]byte(`{"type":"api-request-blob","data":{"messages":[]}}`),
		[]byte(`{"type":"system","subtype":"turn_duration","durationMs":1000}`),
		[]byte(`{"type":"system","subtype":"local_command","content":"<local-command-stdout>Set model to primary-model</local-command-stdout>"}`),
		[]byte(refusalErrorLine))
	var full, observed Meta
	for n, line := range lines {
		ParseLine(n+1, line, &full)
		observed.ObserveModelFallback(line)
		if string(marshal(t, observed.ModelFallback)) != string(marshal(t, full.ModelFallback)) || observed.TurnErrorCode != full.TurnErrorCode {
			t.Fatalf("line %d: observed fallback %+v, error %q; full fallback %+v, error %q", n+1,
				observed.ModelFallback, observed.TurnErrorCode, full.ModelFallback, full.TurnErrorCode)
		}
		if observed.TurnErrorCode != "" && observed.TurnError != full.TurnError {
			t.Fatalf("line %d: error code no longer belongs to the full parser's error", n+1)
		}
		if observed.LastText != "" || observed.Turns != 0 {
			t.Fatal("fallback-only scan changed unrelated metadata")
		}
	}
}
