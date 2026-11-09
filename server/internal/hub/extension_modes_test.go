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
	"github.com/nwverse/persona/server/internal/screen"
)

// The plan-exit dialog of a Claude Code without hooks, as its screen card shows it: picking 1 approves the plan and
// leaves plan mode for bypassPermissions.
const planExitScreen = `────────────────────────────────────────────────
 Ready to code?

 Here is Claude's plan:
 Convert HEIC before export.

 Would you like to proceed?
 ❯ 1. Yes, and bypass permissions
   2. Yes, and manually approve edits
   3. No, keep planning`

// An extension's token changes the permission mode of a conversation its extension opened on its manager's machine,
// and only that: another extension's conversation, one nobody opened, the main conversation, more than the
// permission mode, or a token without agents.permission_mode is refused before the machine hears of it, and its
// manager records the refusal in that extension's audit with the mode asked for. A token of every scope (an attached
// or relayed extension granted hub_api) has no agents.permission_mode: its manager does not open the route to it, and
// that refusal is audited too. Resuming a session with a permission mode, approving a plan (which sets its session's
// mode) and answering a terminal's dialog (whose keys may) are held to the same.
func TestAnExtensionChangesThePermissionModeOfItsOwnConversationsOnly(t *testing.T) {
	h, srv, _ := newBusHub(t)
	h.cfg.Machines = []MachineConfig{{ID: "ma", Name: "ma", Kind: "local"}, {ID: "mb", Name: "mb", Kind: "local"},
		{ID: "mb2", Name: "mb2", Kind: "local"}}
	scoped, unscoped, every := extTokenPrefix+"bx-token", extTokenPrefix+"bx-unscoped", extTokenPrefix+"bx-every"
	elsewhere := extTokenPrefix + "bx-on-c" // an extension of the same id on another manager
	dialog := screen.ParseDialog(planExitScreen)
	if dialog == nil || len(dialog.Options) != 3 || !strings.Contains(dialog.Options[0].Label, "bypass permissions") {
		t.Fatalf("the plan-exit dialog: %+v", dialog)
	}
	scrUser, scrOwn := "scr_mb_b3_"+dialog.Rev, "scr_mb_b1_"+dialog.Rev
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
			case hashToken(every): // as a manager answers for a token of every scope (extmgr extToken.opens)
				scope := extwire.RouteScopes[a.Pattern]
				return extwire.Authorized{OK: scope != "" && extension.InEveryScope(scope), Extension: "bx", Name: "BX",
					Scopes: extension.EveryScope(), Scope: scope}, nil
			}
			return extwire.Authorized{}, nil
		}
		if method == assistantwire.MethodHTTP {
			return map[string]any{"status": 200, "body": []byte(`{"ok":true}`)}, nil
		}
		return map[string]any{}, nil
	}
	connectGuardManager(t, h, srv.URL, assistantwire.AssistantID, quiet)
	b := connectGuardManager(t, h, srv.URL, "m-0b0b0b", answer)
	c := connectGuardManager(t, h, srv.URL, "m-0c0c0c", func(method string, params json.RawMessage) (any, error) {
		var a extwire.Authorize
		if _ = json.Unmarshal(params, &a); method == extwire.MethodAuthorize && a.Hash == hashToken(elsewhere) {
			return extwire.Authorized{OK: true, Extension: "bx", Name: "BX",
				Scopes: []string{extension.ScopeAgentsControl, extension.ScopeAgentsPermissionMode}}, nil
		}
		return map[string]any{}, nil
	})
	for _, mid := range []string{"mb", "mb2"} {
		if _, err := h.moveMachine(context.Background(), mid, "m-0b0b0b"); err != nil {
			t.Fatal(err)
		}
	}
	b.report(t, devicewire.State{Machine: proto.Machine{ID: "mb", Name: "mb"}}, devicewire.State{Machine: proto.Machine{ID: "mb2", Name: "mb2"}})
	eventually(t, 5*time.Second, "mb", func() bool { return h.localMachine("mb") != nil && h.localMachine("mb2") != nil })
	var res assistantwire.SnapshotResult
	if err := b.call(t, assistantwire.MethodSnapshot, assistantwire.Snapshot{Epoch: "b", Seq: 1, Sessions: []assistantwire.SessionView{
		{SessionID: "b1", MachineID: "mb", Origin: "extension", OriginRef: "bx", OpenedBy: "bx"},
		{SessionID: "b2", MachineID: "mb", Origin: "extension", OriginRef: "other", OpenedBy: "other"},
		{SessionID: "b3", MachineID: "mb", Origin: "user"},
		{SessionID: "b4", MachineID: "mb", Origin: "extension", OriginRef: "bx", OpenedBy: "bx"}, // the main one now
	}, Profile: &assistantwire.Profile{MachineID: "mb", Main: "b4"}}, &res); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "B's conversations", func() bool {
		_, v := h.conversation("b1")
		return v.OpenedBy == "bx" && h.mainOf("mb", "b4")
	})
	for _, ix := range []proto.Interaction{
		{ID: "plan_own", MachineID: "mb", SessionID: "b1", Kind: proto.IxPlan},
		{ID: "plan_other", MachineID: "mb", SessionID: "b2", Kind: proto.IxPlan},
		{ID: "perm_other", MachineID: "mb", SessionID: "b2", Kind: proto.IxPermission},
		{ID: "plan_main", MachineID: "mb", SessionID: "b4", Kind: proto.IxPlan},
		{ID: scrUser, MachineID: "mb", SessionID: "b3", Kind: proto.IxScreen, Screen: planExitScreen, Dialog: dialog},
		{ID: scrOwn, MachineID: "mb", SessionID: "b1", Kind: proto.IxScreen, Screen: planExitScreen, Dialog: dialog},
	} {
		if err := b.peer.Notify(assistantwire.EventFleetInteraction, assistantwire.FleetInteraction{Interaction: ix}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 5*time.Second, "B's cards", func() bool {
		for _, id := range []string{"plan_own", "plan_other", "perm_other", "plan_main", scrUser, scrOwn} {
			if _, ok := h.pendingInteraction(id); !ok {
				return false
			}
		}
		return true
	})

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
	resolves := func() int { // the answers relayed to B, not the token checks naming their path
		n := 0
		for _, l := range strings.Split(b.log(), "\n") {
			if strings.HasPrefix(l, assistantwire.MethodHTTP+" ") && strings.Contains(l, "/resolve") {
				n++
			}
		}
		return n
	}

	lastAuditOf := func(m *guardManager) string {
		lines := strings.Split(m.log(), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.Contains(lines[i], `"method":"audit"`) {
				return lines[i]
			}
		}
		return ""
	}
	lastAudit := func() string { return lastAuditOf(b) }

	refusals := 0
	for _, c := range []struct{ name, tok, path, body, reason, mode string }{
		{"another extension's conversation", scoped, "/api/v1/sessions/mb/b2/modes", `{"permission_mode":"bypassPermissions"}`, "not_opened", "bypassPermissions"},
		{"one the user opened", scoped, "/api/v1/sessions/mb/b3/modes", `{"permission_mode":"bypassPermissions"}`, "not_opened", "bypassPermissions"},
		{"one the hub does not know", scoped, "/api/v1/sessions/mb/b9/modes", `{"permission_mode":"plan"}`, "not_opened", "plan"},
		{"more than the permission mode", scoped, "/api/v1/sessions/mb/b1/modes", `{"permission_mode":"plan","model":"opus"}`, "fields", "plan"},
		{"ultracode", scoped, "/api/v1/sessions/mb/b1/modes", `{"ultracode":true}`, "fields", ""},
		{"no permission mode", scoped, "/api/v1/sessions/mb/b1/modes", `{"permission_mode":""}`, "fields", ""},
		{"without the scope", unscoped, "/api/v1/sessions/mb/b1/modes", `{"permission_mode":"plan"}`, "scope", "plan"},
		{"a token of every scope", every, "/api/v1/sessions/mb/b1/modes", `{"permission_mode":"plan"}`, "scope", "plan"},
		{"resuming another's with a mode", scoped, "/api/v1/sessions", `{"machine_id":"mb","resume_session_id":"b2","permission_mode":"bypassPermissions"}`, "not_opened", "bypassPermissions"},
		{"resuming its own without the scope", unscoped, "/api/v1/sessions", `{"machine_id":"mb","resume_session_id":"b1","permission_mode":"plan"}`, "scope", "plan"},
		{"resuming its own with a token of every scope", every, "/api/v1/sessions", `{"machine_id":"mb","resume_session_id":"b1","permission_mode":"plan"}`, "scope", "plan"},
		{"approving another's plan", scoped, "/api/v1/interactions/plan_other/resolve", `{"approve":true}`, "not_opened", "bypassPermissions"},
		{"approving another's plan with a mode", scoped, "/api/v1/interactions/plan_other/resolve", `{"approve":true,"mode":"acceptEdits"}`, "not_opened", "acceptEdits"},
		{"approving its own plan without the scope", unscoped, "/api/v1/interactions/plan_own/resolve", `{"approve":true}`, "scope", "bypassPermissions"},
		{"approving its own plan with a token of every scope", every, "/api/v1/interactions/plan_own/resolve", `{"approve":true}`, "scope", "bypassPermissions"},
		{"the main conversation it opened", scoped, "/api/v1/sessions/mb/b4/modes", `{"permission_mode":"plan"}`, "not_opened", "plan"},
		{"resuming the main conversation it opened", scoped, "/api/v1/sessions", `{"machine_id":"mb","resume_session_id":"b4","permission_mode":"plan"}`, "not_opened", "plan"},
		{"approving the main conversation's plan", scoped, "/api/v1/interactions/plan_main/resolve", `{"approve":true}`, "not_opened", "bypassPermissions"},
		{"its own conversation on another machine", scoped, "/api/v1/sessions/mb2/b1/modes", `{"permission_mode":"plan"}`, "not_opened", "plan"},
		{"a dialog's option in one the user opened, with agents.control only", unscoped, "/api/v1/interactions/" + scrUser + "/resolve", `{"option":"1"}`, "scope", ""},
		{"a dialog's option in one the user opened, with a token of every scope", every, "/api/v1/interactions/" + scrUser + "/resolve", `{"option":"1"}`, "scope", ""},
		{"a dialog's keys in one the user opened, with agents.control only", unscoped, "/api/v1/interactions/" + scrUser + "/resolve", `{"keys":["1"]}`, "scope", ""},
		{"a dialog's option in one the user opened", scoped, "/api/v1/interactions/" + scrUser + "/resolve", `{"option":"1"}`, "not_opened", ""},
		{"a dialog's options checked", scoped, "/api/v1/interactions/" + scrUser + "/resolve", `{"options":["1"]}`, "not_opened", ""},
		{"a dialog's field typed after its pre-keys", scoped, "/api/v1/interactions/" + scrUser + "/resolve", `{"pre_keys":["3"],"text":"go on"}`, "not_opened", ""},
		{"text typed into a dialog", scoped, "/api/v1/interactions/" + scrUser + "/resolve", `{"text":"1"}`, "not_opened", ""},
		{"a dismiss carrying keys", scoped, "/api/v1/interactions/" + scrUser + "/resolve", `{"dismiss":true,"keys":["1"]}`, "not_opened", ""},
		{"a dismiss carrying cancel", unscoped, "/api/v1/interactions/" + scrUser + "/resolve", `{"dismiss":true,"cancel":true}`, "scope", ""},
		{"an empty answer to a dialog", scoped, "/api/v1/interactions/" + scrUser + "/resolve", `{}`, "not_opened", ""},
		{"its own dialog without the scope", unscoped, "/api/v1/interactions/" + scrOwn + "/resolve", `{"option":"1"}`, "scope", ""},
		{"a dialog the hub does not hold", scoped, "/api/v1/interactions/scr_mb_b3_gone/resolve", `{"keys":["1"]}`, "not_opened", ""},
		{"an option of a dialog the hub does not hold", scoped, "/api/v1/interactions/scr_mb_b3_gone/resolve", `{"option":"1"}`, "not_opened", ""},
		{"options of a dialog the hub does not hold", scoped, "/api/v1/interactions/scr_mb_b3_gone/resolve", `{"options":["1"]}`, "not_opened", ""},
		{"pre-keys of a dialog the hub does not hold", scoped, "/api/v1/interactions/scr_mb_b3_gone/resolve", `{"pre_keys":["shift+tab"]}`, "not_opened", ""},
		{"text typed into a dialog the hub does not hold", scoped, "/api/v1/interactions/scr_mb_b3_gone/resolve", `{"text":"1"}`, "not_opened", ""},
		{"approving a card the hub does not hold", scoped, "/api/v1/interactions/no_such_card/resolve", `{"approve":true}`, "not_opened", "bypassPermissions"},
	} {
		code, out := do(c.tok, c.path, c.body)
		if code != http.StatusForbidden || out["code"] != codeModeRefused || out["reason"] != c.reason {
			t.Fatalf("%s: %d %v, want 403 %s (%s)", c.name, code, out, codeModeRefused, c.reason)
		}
		refusals++
		if n, l := count(`"method":"audit"`), lastAudit(); n != refusals || !strings.Contains(l, `"reason":"`+c.reason+`"`) ||
			!strings.Contains(l, `"mode":"`+c.mode+`"`) || !strings.Contains(l, `"extension":"bx"`) {
			t.Fatalf("%s: %d audits for %d refusals, the last %s", c.name, n, refusals, l)
		}
	}
	if l := lastAudit(); !strings.Contains(l, `"machine_id":"","mode":"bypassPermissions","reason":"not_opened","session_id":""`) {
		t.Fatalf("a card the hub does not hold names no session: %s", l)
	}
	do(every, "/api/v1/sessions/mb/b1/modes", `{"permission_mode":"plan"}`)
	refusals++
	if l := lastAudit(); !strings.Contains(l, `"machine_id":"mb","mode":"plan","reason":"scope","session_id":"b1"`) {
		t.Fatalf("the auth's refusal names the machine and session of the path: %s", l)
	}
	for _, tok := range []string{scoped, unscoped, every} {
		if strings.Contains(b.log(), strings.TrimPrefix(tok, extTokenPrefix)) {
			t.Fatalf("a token reached the manager:\n%s", b.log())
		}
	}
	if count("session.modes") != 0 || count("session.start") != 0 || resolves() != 0 {
		t.Fatalf("the machine heard of a refused change:\n%s", b.log())
	}
	if !strings.Contains(b.log(), `"extension":"bx","action":"permission_mode_refused"`) {
		t.Fatalf("the audit names the extension:\n%s", b.log())
	}
	// An extension of the same id on another manager opened none of B's conversations. Its token is turned away from
	// B's cards before any route (extensionReach); the check says the same of it by itself.
	for _, path := range []string{"/api/v1/interactions/plan_own/resolve", "/api/v1/interactions/" + scrOwn + "/resolve"} {
		if code, out := do(elsewhere, path, `{"approve":true,"option":"1"}`); code != http.StatusForbidden {
			t.Fatalf("the same extension on another manager, %s: %d %v", path, code, out)
		}
	}
	if count(`"method":"audit"`) != refusals || resolves() != 0 {
		t.Fatalf("B heard of another manager's answers:\n%s", b.log())
	}
	onC := principal{Role: roleExtension, Extension: "bx", Manager: c.id,
		Scopes: []string{extension.ScopeAgentsControl, extension.ScopeAgentsPermissionMode}}
	if why := h.extensionModeRefused(onC, "mb", "b1", map[string]any{"permission_mode": "plan"}); why != modeRefusedNotOpened {
		t.Fatalf("the same extension on another manager, B's conversation's mode: %q", why)
	}
	yes := true
	for _, id := range []string{"plan_own", scrOwn} {
		if why, _, _, _ := h.extensionResolveRefused(onC, id, proto.Resolution{Approve: &yes, Option: "1"}); why != modeRefusedNotOpened {
			t.Fatalf("the same extension on another manager, %s: %q", id, why)
		}
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
	for _, c := range []struct{ name, tok, path, body string }{
		{"approving its own plan", scoped, "/api/v1/interactions/plan_own/resolve", `{"approve":true,"mode":"acceptEdits"}`},
		{"turning down another's plan", scoped, "/api/v1/interactions/plan_other/resolve", `{"approve":false,"feedback":"not yet"}`},
		{"allowing a tool in another's", scoped, "/api/v1/interactions/perm_other/resolve", `{"allow":true}`},
		{"answering its own conversation's dialog", scoped, "/api/v1/interactions/" + scrOwn + "/resolve", `{"option":"1"}`},
		{"handing another's dialog back to the computer", unscoped, "/api/v1/interactions/" + scrUser + "/resolve", `{"dismiss":true}`},
		{"the app answering the user's dialog", h.cfg.Token, "/api/v1/interactions/" + scrUser + "/resolve", `{"option":"1"}`},
	} {
		before := resolves()
		if code, out := do(c.tok, c.path, c.body); code != http.StatusOK || resolves() != before+1 {
			t.Fatalf("%s: %d %v\n%s", c.name, code, out, b.log())
		}
	}
	if count(`"method":"audit"`) != refusals {
		t.Fatalf("an allowed answer was audited:\n%s", b.log())
	}
	if code, out := do(h.cfg.Token, "/api/v1/sessions/mb/b2/modes", `{"permission_mode":"plan","model":"opus"}`); code != http.StatusOK {
		t.Fatalf("the app is not held to it: %d %v", code, out)
	}
	if code, out := do(h.cfg.Token, "/api/v1/sessions/mb/b4/modes", `{"permission_mode":"plan"}`); code != http.StatusOK {
		t.Fatalf("the app changes the main conversation's mode: %d %v", code, out)
	}
}
