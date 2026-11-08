package hub

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// An extension's token changes the permission mode of a conversation its extension opened on its manager's machine,
// and only that: another extension's conversation, one nobody opened, more than the permission mode, or a token
// without agents.permission_mode is refused before the machine hears of it, and its manager records the refusal in
// that extension's audit. Resuming a session with a permission mode is held to the same.
func TestAnExtensionChangesThePermissionModeOfItsOwnConversationsOnly(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"}}
	scoped, unscoped := extTokenPrefix+"bx-token", extTokenPrefix+"bx-unscoped"
	answer := func(method string, params json.RawMessage) (any, error) {
		if method == extwire.MethodAuthorize {
			var a extwire.Authorize
			_ = json.Unmarshal(params, &a)
			switch a.Hash {
			case hashToken(scoped):
				return extwire.Authorized{OK: true, Extension: "bx", Name: "BX",
					Scopes: []string{extension.ScopeAgentsControl, extension.ScopeAgentsPermissionMode}}, nil
			case hashToken(unscoped):
				return extwire.Authorized{OK: true, Extension: "bx", Name: "BX", Scopes: []string{extension.ScopeAgentsControl}}, nil
			}
			return extwire.Authorized{}, nil
		}
		return map[string]any{}, nil
	}
	connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, quiet)
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", answer)
	if _, err := h.moveMachine(context.Background(), "mb", "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "mb", Name: "mb"}})
	eventually(t, 5*time.Second, "mb", func() bool { return h.localMachine("mb") != nil })
	var res assistantwire.SnapshotResult
	if err := b.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "b", Seq: 1, Sessions: []assistantwire.SessionView{
		{SessionID: "b1", MachineID: "mb", Origin: "extension", OriginRef: "bx", OpenedBy: "bx"},
		{SessionID: "b2", MachineID: "mb", Origin: "extension", OriginRef: "other", OpenedBy: "other"},
		{SessionID: "b3", MachineID: "mb", Origin: "user"},
	}}, &res); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "B's conversations", func() bool { _, v := h.conversation("b1"); return v.OpenedBy == "bx" })

	do := func(tok, path, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	count := func(s string) int { return strings.Count(b.log(), s) }

	refusals := 0
	for _, c := range []struct{ name, tok, path, body, reason string }{
		{"another extension's conversation", scoped, "/api/v1/sessions/mb/b2/modes", `{"permission_mode":"bypassPermissions"}`, "not_opened"},
		{"one the user opened", scoped, "/api/v1/sessions/mb/b3/modes", `{"permission_mode":"bypassPermissions"}`, "not_opened"},
		{"one the hub does not know", scoped, "/api/v1/sessions/mb/b9/modes", `{"permission_mode":"plan"}`, "not_opened"},
		{"more than the permission mode", scoped, "/api/v1/sessions/mb/b1/modes", `{"permission_mode":"plan","model":"opus"}`, "fields"},
		{"ultracode", scoped, "/api/v1/sessions/mb/b1/modes", `{"ultracode":true}`, "fields"},
		{"no permission mode", scoped, "/api/v1/sessions/mb/b1/modes", `{"permission_mode":""}`, "fields"},
		{"without the scope", unscoped, "/api/v1/sessions/mb/b1/modes", `{"permission_mode":"plan"}`, "scope"},
		{"resuming another's with a mode", scoped, "/api/v1/sessions", `{"machine_id":"mb","resume_session_id":"b2","permission_mode":"bypassPermissions"}`, "not_opened"},
		{"resuming its own without the scope", unscoped, "/api/v1/sessions", `{"machine_id":"mb","resume_session_id":"b1","permission_mode":"plan"}`, "scope"},
	} {
		code, out := do(c.tok, c.path, c.body)
		if code != http.StatusForbidden || out["code"] != codeModeRefused || out["reason"] != c.reason {
			t.Fatalf("%s: %d %v, want 403 %s (%s)", c.name, code, out, codeModeRefused, c.reason)
		}
		refusals++
		if n := count(`"method":"audit"`); n != refusals || !strings.Contains(b.log(), `"reason":"`+c.reason+`"`) {
			t.Fatalf("%s: %d audits for %d refusals:\n%s", c.name, n, refusals, b.log())
		}
	}
	if count("session.modes") != 0 || count("session.start") != 0 {
		t.Fatalf("the machine heard of a refused change:\n%s", b.log())
	}
	if !strings.Contains(b.log(), `"extension":"bx","action":"permission_mode_refused"`) {
		t.Fatalf("the audit names the extension:\n%s", b.log())
	}

	if code, out := do(scoped, "/api/v1/sessions/mb/b1/modes", `{"permission_mode":"plan"}`); code != http.StatusOK {
		t.Fatalf("its own conversation: %d %v", code, out)
	}
	if count("session.modes") != 1 || !strings.Contains(b.log(), `"permission_mode":"plan"`) || count(`"method":"audit"`) != refusals {
		t.Fatalf("its own conversation's mode:\n%s", b.log())
	}
	if code, out := do(scoped, "/api/v1/sessions", `{"machine_id":"mb","resume_session_id":"b1","permission_mode":"plan"}`); out["code"] == codeModeRefused || count(`"method":"audit"`) != refusals {
		t.Fatalf("resuming its own with a mode: %d %v", code, out)
	}
	if code, out := do(h.cfg.Token, "/api/v1/sessions/mb/b2/modes", `{"permission_mode":"plan","model":"opus"}`); code != http.StatusOK {
		t.Fatalf("the app is not held to it: %d %v", code, out)
	}
}
