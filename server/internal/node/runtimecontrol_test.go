package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// paseoEngineHello is the Paseo runtime with a launcher per provider, the phone's modes and session takeovers.
func paseoEngineHello() map[string]any {
	h := paseoHello()
	h["caps"] = map[string]any{"start": true, "send": true, "interrupt": true, "rename": true, "close": true, "modes": true, "resume": true}
	h["launchers"] = []map[string]any{
		{"name": "paseo-claude", "agent": "claude", "model": "Opus 5.5", "models": []map[string]any{
			{"id": "claude-opus-5-5", "name": "Opus 5.5", "default": true, "efforts": []string{"low", "high", "max", "turbo"}},
			{"id": "", "name": "no id"}}},
		{"name": "paseo-codex", "agent": "paseo", "model": "GPT"},
		{"name": "bad name!", "agent": "claude"},
		{"name": "paseo-claude", "agent": "claude"},
	}
	return h
}

// A runtime running several agents offers a launcher for each, with its agent and models; what the form chose with
// one reaches the runtime when the session starts; runtime.launchers replaces them.
func TestRuntimeLaunchers(t *testing.T) {
	n, _ := runtimeNode(t)
	host := dialRuntime(t, n, paseoEngineHello())
	raw, err := nodeCall(t, n, "launchers.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Launchers []struct {
			proto.Launcher
			AgentType string `json:"agent_type"`
		} `json:"launchers"`
	}
	_ = json.Unmarshal(raw, &res)
	got := map[string]proto.Launcher{}
	for _, l := range res.Launchers {
		if l.Source == "paseo" {
			got[l.Name] = l.Launcher
		}
	}
	if len(got) != 2 || got["paseo-claude"].Agent != "" || got["paseo-claude"].Model != "Opus 5.5" || got["paseo-codex"].Agent != "paseo" {
		t.Fatalf("launchers %s", raw)
	}
	raw, err = nodeCall(t, n, "launchers.models", map[string]any{"name": "paseo-claude"})
	if err != nil {
		t.Fatal(err)
	}
	var models proto.LauncherModels
	_ = json.Unmarshal(raw, &models)
	if models.Agent != "claude" || len(models.Models) != 1 || !models.Models[0].Default || len(models.Models[0].Efforts) != 3 {
		t.Fatalf("models %s", raw)
	}

	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "paseo-n1", "status": "idle", "cwd": "/w/app"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	if _, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/w/app", "launcher": "paseo-codex", "model": "gpt-6",
		"permission_mode": "default", "effort": "high", "workspace_id": "wks_1", "async": true}); err != nil {
		t.Fatal(err)
	}
	var start map[string]any
	_ = json.Unmarshal(host.got("session.start")[0], &start)
	if start["launcher"] != "paseo-codex" || start["model"] != "gpt-6" || start["permission_mode"] != "default" || start["effort"] != "high" ||
		start["workspace_id"] != "wks_1" || start["shell_launcher"] != nil || start["resume"] != nil {
		t.Fatalf("started with %v", start)
	}

	_ = host.peer.Notify("runtime.launchers", map[string]any{"launchers": []map[string]any{{"name": "paseo-grok", "agent": "paseo"}}})
	waitFor(t, "the new launchers", func() bool {
		return n.runtimes.launcherRuntime("paseo-grok") != nil && n.runtimes.launcherRuntime("paseo-codex") == nil
	})
}

// The phone's permission mode and effort reach a runtime that takes them; one that does not refuses them.
func TestRuntimeModes(t *testing.T) {
	n, _ := runtimeNode(t)
	host := dialRuntime(t, n, paseoEngineHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "paseo-m1", "agent": "claude", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["paseo-m1"]; return ok })
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": "paseo-m1", "permission_mode": "acceptEdits", "effort": "max"}); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(host.got("session.modes")[0], &m)
	if m["permission_mode"] != "acceptEdits" || m["effort"] != "max" {
		t.Fatalf("modes %v", m)
	}
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": "paseo-m1", "permission_mode": "yolo"}); err == nil {
		t.Fatal("an unknown mode was passed on")
	}

	dsh := dialRuntime(t, n, dshHello()) // no caps.modes
	_ = dsh.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "session-m2", "status": "idle"}}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["session-m2"]; return ok })
	_, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": "session-m2", "effort": "high"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != "unsupported" || len(dsh.got("session.modes")) != 0 {
		t.Fatalf("a runtime without modes: %v", err)
	}
}

// On a machine whose engine is Paseo every start goes to it: its own launchers, a launcher of the login shell's (Paseo
// picks its provider of that name), a session to continue; with Paseo away nothing starts.
func TestEngineRuntimeStarts(t *testing.T) {
	n, _ := runtimeNode(t)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	_, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/w/app", "launcher": "claude", "async": true})
	if rpcCode(err) != engineMissing {
		t.Fatalf("Paseo away: %v", err)
	}
	host := dialRuntime(t, n, paseoEngineHello())
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "paseo-s1", "status": "idle", "cwd": "/w/app"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	raw, err := nodeCall(t, n, "session.start", map[string]any{"cwd": "/w/app", "launcher": "claude", "async": true, "effort": "high"})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	_ = json.Unmarshal(raw, &res)
	if res.Session.SessionID != "paseo-s1" || res.Session.SourceKind != proto.SourceRuntime {
		t.Fatalf("started %s", raw)
	}
	var start map[string]any
	_ = json.Unmarshal(host.got("session.start")[0], &start)
	if start["launcher"] != "claude" || start["shell_launcher"] != true || start["effort"] != "high" {
		t.Fatalf("started with %v", start)
	}
}

// A message to a Claude session not running anywhere, on a machine whose engine is Paseo: Paseo takes it over (the
// same id, its transcript the conversation) and the message goes to it there — never a claude --resume in a terminal.
func TestEngineRuntimeContinuesASession(t *testing.T) {
	n, _ := runtimeNode(t)
	sdkTranscript(t, n, sdkSession, "where we left off")
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	host := dialRuntime(t, n, paseoEngineHello())
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": sdkSession, "transcript": sdkSession, "agent": "claude",
				"status": "idle", "cwd": "/w/app"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sdkSession, "text": "carry on", "client_msg_id": "m-c1"}); err != nil {
		t.Fatal(err)
	}
	var start map[string]any
	_ = json.Unmarshal(host.got("session.start")[0], &start)
	if start["resume"] != sdkSession || start["cwd"] != "/w/app" {
		t.Fatalf("taken over with %v", start)
	}
	var send map[string]any
	_ = json.Unmarshal(host.got("session.send")[0], &send)
	if send["session_id"] != sdkSession || send["text"] != "carry on" || send["client_msg_id"] != "m-c1" {
		t.Fatalf("sent %v", send)
	}
	// From now on it is Paseo's session: the next message goes straight to it.
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{
		"session_id": sdkSession, "transcript": sdkSession, "agent": "claude", "status": "idle", "cwd": "/w/app"}}})
	waitFor(t, "listed by Paseo", func() bool { return listed(t, n)[sdkSession].SourceKind == proto.SourceRuntime })
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sdkSession, "text": "and more", "client_msg_id": "m-c2"}); err != nil {
		t.Fatal(err)
	}
	if len(host.got("session.start")) != 1 || len(host.got("session.send")) != 2 {
		t.Fatalf("starts %d sends %d", len(host.got("session.start")), len(host.got("session.send")))
	}
}

// A runtime that cannot take sessions over: a message to one not running is refused, nothing sent.
func TestEngineRuntimeWithoutResume(t *testing.T) {
	n, _ := runtimeNode(t)
	sdkTranscript(t, n, sdkSession, "old")
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	hello := paseoEngineHello()
	hello["caps"] = map[string]any{"start": true, "send": true}
	host := dialRuntime(t, n, hello)
	_, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sdkSession, "text": "x", "client_msg_id": "m-r"})
	if rpcCode(err) != notRunning || len(host.got("session.start")) != 0 || len(host.got("session.send")) != 0 {
		t.Fatalf("without resume: %v", err)
	}
}

// A session a running claude has open: Paseo is told, takes it only when that claude is its own, and refuses
// otherwise — the refusal says where it is open, nothing is sent.
func TestEngineRuntimeSessionOpenElsewhere(t *testing.T) {
	n, _ := runtimeNode(t)
	sdkTranscript(t, n, sdkSession, "open in a terminal")
	dir := filepath.Join(n.home, ".claude", "sessions")
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, strconv.Itoa(os.Getpid())+".json"), []byte(`{"sessionId":"`+sdkSession+`","entrypoint":"cli"}`), 0o600)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	host := dialRuntime(t, n, paseoEngineHello())
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return nil, &rpc.Error{Message: "not ours", Code: "open_elsewhere"}
		}
		return map[string]any{"ok": true}, nil
	}
	_, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sdkSession, "text": "x", "client_msg_id": "m-e"})
	if rpcCode(err) != openElsewhere || !strings.Contains(err.Error(), "电脑上的一个终端") {
		t.Fatalf("open elsewhere: %v", err)
	}
	var start map[string]any
	_ = json.Unmarshal(host.got("session.start")[0], &start)
	if start["open_elsewhere"] != true || len(host.got("session.send")) != 0 {
		t.Fatalf("started with %v, sends %d", start, len(host.got("session.send")))
	}
}
