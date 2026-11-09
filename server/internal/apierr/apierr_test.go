package apierr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// langWriter is a response that knows its request's language, as the hub's request logger's does (i18n.Carrier).
type langWriter struct {
	*httptest.ResponseRecorder
	lang i18n.Lang
}

func (w langWriter) Lang() i18n.Lang { return w.lang }

func call(t *testing.T, w http.ResponseWriter, rec *httptest.ResponseRecorder, err error) map[string]any {
	t.Helper()
	Call(w, err)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// The refusals that say why the main conversation cannot start are 409 with a sentence for people in the request's
// language, the caller's words as the detail and what to do (docs/assistant/onboarding.md 5.2), wrapped or not.
func TestCallExplainsCodedRefusals(t *testing.T) {
	raw := "claude 没有启动起来（0s 后退出），消息没有送达：zsh:1: command not found: claude"
	refusal := &rpc.Error{Code: proto.CodeClaudeMissing, Message: raw, Data: json.RawMessage(`{"delivered":"none","launcher":"claude"}`)}
	for _, tc := range []struct {
		name string
		lang i18n.Lang
		err  error
		want string
	}{
		{"no language", "", refusal, "这台电脑上还没装 Claude Code。"},
		{"English", i18n.En, refusal, "Claude Code isn't installed on this computer yet."},
		{"named", "", OnMachine(fmt.Errorf("send: %w", refusal), "小林的 MacBook"), "小林的 MacBook 上还没装 Claude Code。"},
		{"Japanese", i18n.Ja, OnMachine(refusal, "小林的 MacBook"), "小林的 MacBook にはまだ Claude Code がインストールされていません。"},
	} {
		rec := httptest.NewRecorder()
		body := call(t, langWriter{rec, tc.lang}, rec, tc.err)
		if rec.Code != http.StatusConflict || body["code"] != proto.CodeClaudeMissing || body["error"] != tc.want ||
			body["detail"] != raw || body["action"] != "install_claude" || body["delivered"] != "none" {
			t.Errorf("%s: %d %v", tc.name, rec.Code, body)
		}
	}

	rec := httptest.NewRecorder()
	body := call(t, rec, rec, &rpc.Error{Code: proto.CodeLauncherMissing, Message: "cc_gone 没有启动起来",
		Data: json.RawMessage(`{"launcher":"cc_gone","machine_name":"mbp","action":"edit_provider"}`)})
	if body["error"] != "在 mbp 上找不到「cc_gone」这个模型入口，可能被删了。" || body["action"] != "edit_provider" {
		t.Errorf("launcher_missing: %v", body)
	}

	rec = httptest.NewRecorder()
	body = call(t, rec, rec, &rpc.Error{Code: proto.CodeNoMachine, Message: "assistant machine is not configured"})
	if rec.Code != http.StatusConflict || body["error"] != "Persona 要在你的一台电脑上干活，现在还没接上电脑。" || body["action"] != "add_computer" {
		t.Errorf("no_machine: %d %v", rec.Code, body)
	}
}

// A start whose new tab would not take its command (node/typedstart.go, #2022) is 503 and retryable, with nothing
// delivered, not the generic 502.
func TestCallTerminalNotReadyIsRetryable(t *testing.T) {
	rec := httptest.NewRecorder()
	body := call(t, rec, rec, &rpc.Error{Code: "terminal_not_ready", Message: "pane_send_failed: example",
		Data: json.RawMessage(`{"delivered":"none","retryable":true}`)})
	if rec.Code != http.StatusServiceUnavailable || body["code"] != "terminal_not_ready" || body["retryable"] != true || body["delivered"] != "none" {
		t.Errorf("%d %v", rec.Code, body)
	}
}

// A start whose new tab's shell could not run the launcher (#1642, node/shellmiss.go) is said as the terminal's
// problem, naming the shells and the rc file, in each language; a path the shell could not run and a missing claude
// have words of their own. The shell's line stays the detail, the remedy the launcher's.
func TestCallExplainsTerminalRefusals(t *testing.T) {
	data := func(code, kind, missing string) *rpc.Error {
		b, _ := json.Marshal(map[string]string{"delivered": "none", "launcher": "cc", "missing": missing, "kind": kind, "where": "terminal",
			"pane_shell": "bash", "launcher_shell": "zsh", "rc_file": "~/.zshrc", "line": "bash: " + missing + ": command not found"})
		return &rpc.Error{Code: code, Message: "the new tab's shell (bash) has no command " + missing, Data: b}
	}
	for _, tc := range []struct {
		name   string
		lang   i18n.Lang
		err    error
		action string
		want   []string
	}{
		{"default", "", OnMachine(data(proto.CodeLauncherMissing, "not_found", "cc"), "mbp"), "connect_model",
			[]string{"mbp 上新开的终端标签页（bash）里没有「cc」", "「cc」", "zsh（~/.zshrc）"}},
		{"English, here", i18n.En, data(proto.CodeLauncherMissing, "not_found", "cc"), "connect_model",
			[]string{"new terminal tabs on this computer (bash) has no command “cc”", "“cc”", "zsh (~/.zshrc)"}},
		{"Japanese", i18n.Ja, OnMachine(data(proto.CodeLauncherMissing, "not_found", "cc"), "mbp"), "connect_model",
			[]string{"mbp の新しいターミナルタブ（bash）には「cc」", "「cc」", "zsh（~/.zshrc）"}},
		{"a path", i18n.En, OnMachine(data(proto.CodeLauncherMissing, "no_such_file", "/opt/p/personad"), "mbp"), "connect_model",
			[]string{"new terminal tabs on mbp (bash) can't run “/opt/p/personad”", "“cc”"}},
		{"claude", i18n.En, OnMachine(data(proto.CodeClaudeMissing, "not_found", "claude"), "mbp"), "install_claude",
			[]string{"new terminal tabs on mbp (bash) can't find Claude Code (“claude”)", "“cc”"}},
	} {
		rec := httptest.NewRecorder()
		body := call(t, langWriter{rec, tc.lang}, rec, tc.err)
		text, _ := body["error"].(string)
		for _, w := range tc.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: %q lacks %q", tc.name, text, w)
			}
		}
		if rec.Code != http.StatusConflict || body["action"] != tc.action || body["where"] != "terminal" || body["pane_shell"] != "bash" ||
			!strings.HasPrefix(body["detail"].(string), "the new tab's shell") || strings.Contains(text, "{") {
			t.Errorf("%s: %d %v", tc.name, rec.Code, body)
		}
	}
	if got := Sentence(i18n.En, "input_draft", nil); got != "" {
		t.Errorf("a refusal the hub does not word: %q", got)
	}
	// A tab whose shell the node could not tell (herdr on Windows names none): the default shell, in the language.
	for l, want := range map[i18n.Lang]string{i18n.En: "(the default shell)", i18n.ZhHans: "（默认的 shell）", i18n.Ja: "（既定のシェル）"} {
		for _, code := range []string{proto.CodeLauncherMissing, proto.CodeClaudeMissing} {
			got := Sentence(l, code, map[string]any{"launcher": "cc", "missing": "cc", "kind": "not_found", "where": "terminal",
				"launcher_shell": "zsh", "rc_file": "~/.zshrc"})
			if !strings.Contains(got, want) || strings.Contains(got, "{") {
				t.Errorf("%s %s, no pane_shell: %q", l, code, got)
			}
		}
	}
}

// Other coded refusals keep the words the node wrote; OnMachine leaves them and plain errors alone.
func TestCallKeepsOtherRefusals(t *testing.T) {
	rec := httptest.NewRecorder()
	refusal := &rpc.Error{Code: "input_draft", Message: "请先处理示例终端状态。", Data: json.RawMessage(`{"delivered":"none"}`)}
	body := call(t, rec, rec, OnMachine(refusal, "mbp"))
	if rec.Code != http.StatusConflict || body["error"] != "请先处理示例终端状态。" || body["detail"] != nil || body["action"] != nil || body["machine_name"] != nil {
		t.Errorf("input_draft: %d %v", rec.Code, body)
	}
	plain := errors.New("boom")
	if OnMachine(plain, "mbp") != plain {
		t.Error("a plain error is changed")
	}
}

func TestCallExplainsAssistantControlFailure(t *testing.T) {
	const reason = "CLAUDE_CODE_EFFORT_LEVEL overrides the session effort"
	for _, tc := range []struct {
		name string
		lang i18n.Lang
		want string
	}{
		{"default", "", "Claude 未能应用这项设置：" + reason},
		{"English", i18n.En, "Claude could not apply this setting: " + reason},
		{"Chinese", i18n.ZhHans, "Claude 未能应用这项设置：" + reason},
		{"Japanese", i18n.Ja, "Claudeはこの設定を適用できませんでした：" + reason},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			refusal := &rpc.Error{Code: "assistant_control_failed", Message: reason,
				Data: json.RawMessage(`{"delivered":"none","retryable":false}`)}
			body := call(t, langWriter{rec, tc.lang}, rec, fmt.Errorf("session modes: %w", refusal))
			if rec.Code != http.StatusConflict || body["code"] != "assistant_control_failed" || body["error"] != tc.want ||
				body["detail"] != reason || body["delivered"] != "none" || body["retryable"] != false || body["action"] != nil {
				t.Fatalf("%d %v", rec.Code, body)
			}
		})
	}
}

// Status codes and retryability stay stable; only exact standard status text is translated for an explicit locale.
func TestStandardStatusTextLocalization(t *testing.T) {
	for status, key := range standardStatusKeys {
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja, ""} {
			rec := httptest.NewRecorder()
			original := errors.New(http.StatusText(status))
			Write(langWriter{rec, lang}, status, original)
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			want := original.Error()
			if lang != "" {
				want = i18n.T(lang, key)
			}
			if body["error"] != want || body["code"] != Body(status, "", original)["code"] || rec.Code != status {
				t.Fatalf("%d %s: %v, want %q", status, lang, body, want)
			}
		}
	}
	rec := httptest.NewRecorder()
	Write(langWriter{rec, i18n.Ja}, 404, errors.New("custom diagnostic"))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] != "custom diagnostic" {
		t.Fatalf("custom text changed: %v", body)
	}
}

func TestRPCFailureLocalePreservesOutcomeAndDiagnostics(t *testing.T) {
	cases := []struct {
		name string
		err  error
		key  string
	}{
		{"offline", ErrOffline, "rpc-errors.offline"},
		{"closed", fmt.Errorf("read: %w", rpc.ErrClosed), "rpc-errors.unreachable"},
		{"timeout", fmt.Errorf("read: %w", context.DeadlineExceeded), "rpc-errors.timeout"},
		{"internal", errors.New("example raw diagnostic"), "rpc-errors.failed"},
		{"draft", &rpc.Error{Code: "input_draft", Message: "original terminal instructions", Data: json.RawMessage(`{"delivered":"none","retryable":false,"draft":"example user draft"}`)}, "rpc-errors.input_draft"},
		{"unknown delivery", &rpc.Error{Code: "timeout", Message: "original diagnostic", Data: json.RawMessage(`{"delivered":"unknown","retryable":false}`)}, "rpc-errors.timeout"},
		{"partial delivery", &rpc.Error{Code: "internal", Message: "original diagnostic", Data: json.RawMessage(`{"delivered":"yes","retryable":false}`)}, "rpc-errors.failed"},
		{"busy", &rpc.Error{Code: "attach_busy", Message: "original diagnostic"}, "rpc-errors.attach_busy"},
		{"terminal not ready", &rpc.Error{Code: "terminal_not_ready", Message: "original diagnostic", Data: json.RawMessage(`{"delivered":"none","retryable":true}`)}, "rpc-errors.terminal_not_ready"},
		{"old node", &rpc.Error{Code: "node_outdated", Message: "original diagnostic"}, "rpc-errors.node_outdated"},
		// a Claude session outside Persona's terminals: refused before anything reached it, or handed over unconfirmed
		{"peer refused", &rpc.Error{Code: "peer_unsupported", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none","pid":42}`)}, "rpc-errors.peer_unsupported"},
		{"peer gone", &rpc.Error{Code: "peer_unreachable", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.peer_unreachable"},
		{"peer long", &rpc.Error{Code: "peer_too_long", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.peer_too_long"},
		{"peer command", &rpc.Error{Code: "peer_command", Message: "node's own words", Data: json.RawMessage(`{"delivered":"none"}`)}, "rpc-errors.peer_command"},
		{"peer unconfirmed", &rpc.Error{Code: "peer_unconfirmed", Message: "node's own words", Data: json.RawMessage(`{"delivered":"unknown"}`)}, "rpc-errors.peer_unconfirmed"},
	}
	for _, tc := range cases {
		legacy := httptest.NewRecorder()
		before := call(t, legacy, legacy, tc.err)
		for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			rec := httptest.NewRecorder()
			after := call(t, langWriter{rec, lang}, rec, tc.err)
			if rec.Code != legacy.Code || after["error"] != i18n.T(lang, tc.key) {
				t.Fatalf("%s %s: %d %v", tc.name, lang, rec.Code, after)
			}
			for _, key := range []string{"code", "retryable", "delivered", "draft", "clearable"} {
				if after[key] != before[key] {
					t.Fatalf("%s %s changed %s: before=%v after=%v", tc.name, lang, key, before, after)
				}
			}
			if after["detail"] != before["error"] {
				t.Fatalf("diagnostic lost: before=%v after=%v", before, after)
			}
		}
	}
	raw := &rpc.Error{Code: "external_custom", Message: "external author text"}
	rec := httptest.NewRecorder()
	if body := call(t, langWriter{rec, i18n.Ja}, rec, raw); body["error"] != raw.Message {
		t.Fatal("external custom message guessed")
	}
	typed := i18n.ErrEnglish("inbox-errors.no_item")
	rec = httptest.NewRecorder()
	if body := call(t, langWriter{rec, i18n.Ja}, rec, typed); body["error"] != i18n.T(i18n.Ja, "inbox-errors.no_item") {
		t.Fatal("typed summary overwritten")
	}
}

// The apps still tell a timeout by these words in the hub's summary (iOS APIError.isTimeoutText keeps it quiet, Android's
// herdr notice explains it), so the sentence that now says what to do next keeps them (#5031).
func TestTimeoutSummaryKeepsTheWordsTheAppsMatch(t *testing.T) {
	for lang, words := range map[i18n.Lang]string{i18n.En: "timed out", i18n.ZhHans: "超时"} {
		if s := i18n.T(lang, "rpc-errors.timeout"); !strings.Contains(strings.ToLower(s), words) {
			t.Errorf("%s: %q no longer says %q", lang, s, words)
		}
	}
}

func TestInvalidJSONAndInDoubtLocale(t *testing.T) {
	var v struct {
		Count int `json:"count"`
	}
	decodeErr := json.Unmarshal([]byte(`{"count":"bad"}`), &v)
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		rec := httptest.NewRecorder()
		Write(langWriter{rec, lang}, 400, decodeErr)
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["error"] != i18n.T(lang, "http-status.bad_request") || body["detail"] != decodeErr.Error() || body["code"] != "bad_request" {
			t.Fatal(body)
		}
		doubt := LocalizedBody(502, "assistant_in_doubt", rpc.ErrDropped, lang)
		if doubt["error"] != i18n.T(lang, "rpc-errors.in_doubt") || doubt["delivered"] != nil {
			t.Fatal("delivery certainty invented", doubt)
		}
	}
	if RPCText(i18n.Ja, "third-party raw text") != "third-party raw text" || RPCText("", rpc.ErrClosed.Error()) != rpc.ErrClosed.Error() {
		t.Fatal("raw/legacy stream text changed")
	}
	if RPCText(i18n.Ja, rpc.ErrClosed.Error()) != i18n.T(i18n.Ja, "rpc-errors.unreachable") {
		t.Fatal("stream error not localized")
	}
}

// Describe keeps the summary and adds the cause it hides; Reason gives only the cause (#5728).
func TestDescribeKeepsTheSummaryAndTheCause(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"error":"the request did not complete","detail":"connection refused"}`, "the request did not complete：connection refused"},
		{`{"error":"plain"}`, "plain"},
		{`{"error":"same","detail":"same"}`, "same"},
		{`{"error":"summary","detail":{"k":1}}`, "summary"},
		{`{"detail":"only the cause"}`, "only the cause"},
		{`not json`, ""},
	} {
		if got := Describe([]byte(c.body)); got != c.want {
			t.Errorf("Describe(%s) = %q, want %q", c.body, got, c.want)
		}
	}
}
