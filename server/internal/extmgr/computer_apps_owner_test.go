package extmgr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

func TestComputerAppOwnerPreconditionAndLegacyRequests(t *testing.T) {
	h, st := twoMacs(t)
	h.opts.ManagerID = "example-manager"
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		path := "/api/v1/machines/a/computer/apps"
		if method == http.MethodDelete {
			path += "/com.apple.calculator"
		}
		// Exercise the production manager handlers directly, as the bus relay does.
		run := func(query string) (int, map[string]any) {
			r := httptest.NewRequest(method, path+query, nil)
			r.SetPathValue("bundle", "com.apple.calculator")
			w := httptest.NewRecorder()
			if method == http.MethodGet {
				h.getComputerApps(w, r, &Machine{cfg: MachineConfig{ID: "a"}})
			} else {
				h.deleteComputerApp(w, r, &Machine{cfg: MachineConfig{ID: "a"}})
			}
			var out map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			return w.Code, out
		}
		if code, out := run("?expected_manager=example-other"); code != http.StatusConflict || out["code"] != "machine_owner_changed" {
			t.Fatalf("wrong owner: %d %v", code, out)
		}
		if len(st.stored(t)["a"]) != 2 || len(st.stored(t)["b"]) != 1 {
			t.Fatal("wrong owner touched settings")
		}
		if code, out := run(""); code != http.StatusOK || out["manager"] != "example-manager" {
			t.Fatalf("legacy request: %d %v", code, out)
		}
	}
}

func TestComputerAppRevocationPreservesDisabledApprovalPolicy(t *testing.T) {
	h, st := extensionTestHub(t)
	m, _ := computerMac(t, h, "example-machine")
	ctx := context.Background()
	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(`{"machines":["example-machine"],"app_approval":false}`), "test"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSetting(ctx, computerAppsKey, map[string][]approvedApp{"example-machine": {{BundleID: "org.example.editor", Name: "Example Editor"}}}); err != nil {
		t.Fatal(err)
	}
	code, out := call(t, h.routes(), http.MethodDelete, "/api/v1/machines/example-machine/computer/apps/org.example.editor?expected_manager=assistant", h.cfg.Token, "")
	if code != http.StatusOK || len(out["apps"].([]any)) != 0 || out["manager"] != "assistant" {
		t.Fatalf("revoke: %d %v", code, out)
	}
	if !h.cua().On || h.cua().approval() {
		t.Fatal("revocation changed the enabled extension or its disabled app approval policy")
	}
	if asked, allowed := h.approveApps(ctx, m, asker{}, "computer_run", []string{"Example Editor"}, true, nil); asked || !allowed {
		t.Fatalf("disabled policy must still allow without asking: asked=%v allowed=%v", asked, allowed)
	}
}

func TestGuardedComputerAppAliasRequiresItsOwner(t *testing.T) {
	h, st := twoMacs(t)
	h.opts.ManagerID = "example-manager"
	for _, query := range []string{"", "?expected_manager=example-other"} {
		r := httptest.NewRequest(http.MethodDelete, "/api/v1/machines/a/computer/apps/revoke/com.apple.calculator"+query, nil)
		r.SetPathValue("bundle", "com.apple.calculator")
		w := httptest.NewRecorder()
		h.revokeComputerApp(w, r, &Machine{cfg: MachineConfig{ID: "a"}})
		if w.Code != http.StatusConflict && w.Code != http.StatusPreconditionRequired {
			t.Fatalf("missing or different owner accepted: %d %s", w.Code, w.Body)
		}
		if len(st.stored(t)["a"]) != 2 {
			t.Fatal("guarded alias changed settings without its owner")
		}
	}
}

// Saved-approval management keeps the legacy endpoint's full-credential boundary; granting extension scopes does
// not grant this mutation, including the token representing every named extension scope.
func TestGuardedComputerAppAliasKeepsFullCredentialBoundary(t *testing.T) {
	h, st := twoMacs(t)
	h.opts.ManagerID = "example-manager"
	_, seen := computerMac(t, h, "a")
	mux := h.routes()
	tokens := []string{h.extTokens.issue("example-scoped", "Example extension", extension.HubScopes), h.extTokens.issueAll("example-all", "Example all scopes")}
	for _, token := range tokens {
		for _, path := range []string{"/api/v1/machines/a/computer/apps/com.apple.calculator", "/api/v1/machines/a/computer/apps/revoke/com.apple.calculator?expected_manager=example-manager"} {
			code, out := call(t, mux, http.MethodDelete, path, token, "")
			if code != http.StatusForbidden || out["code"] != "permission_denied" || len(st.stored(t)["a"]) != 2 {
				t.Fatalf("extension scope reached saved approvals: %d %v", code, out)
			}
		}
	}
	if code, out := call(t, mux, http.MethodDelete, "/api/v1/machines/a/computer/apps/revoke/com.apple.calculator?expected_manager=example-manager", h.cfg.Token, ""); code != http.StatusOK || len(st.stored(t)["a"]) != 1 {
		t.Fatalf("full credential could not revoke: %d %v", code, out)
	}
	if _, calls := seen(); len(calls) != 0 {
		t.Fatalf("persisted revoke called a node: %+v", calls)
	}
}
