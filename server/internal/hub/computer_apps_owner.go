package hub

import (
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// This response header is minted by the hub after checking the captured routing target, not by its receiver.
const computerAppsOwnerHeader = "X-Persona-Computer-Apps-Owner"

// computerAppsRoute distinguishes the persisted approvals and the guarded mutation alias from live computer use.
func computerAppsRoute(r *http.Request) (apps, guardedAlias bool) {
	mid := r.PathValue("mid")
	if mid == "" {
		return false, false
	}
	segments := strings.SplitN(r.URL.EscapedPath(), "/", 8)
	if len(segments) < 7 || segments[1] != "api" || segments[2] != "v1" || segments[3] != "machines" || segments[5] != "computer" || segments[6] != "apps" {
		return false, false
	}
	if r.Method == http.MethodGet {
		return len(segments) == 7, false
	}
	if r.Method != http.MethodDelete || len(segments) != 8 || segments[7] == "" {
		return false, false
	}
	return true, strings.HasPrefix(segments[7], "revoke/")
}

// id is the one ownership lookup relayToManager already captured. The same id is checked and relayed, so a move
// cannot redirect a confirmation between those steps. The guarded alias cannot match a legacy receiver's DELETE.
func computerAppsPrecondition(w http.ResponseWriter, r *http.Request, id string) (guarded, alias, ok bool) {
	apps, alias := computerAppsRoute(r)
	expected := r.URL.Query().Get("expected_manager")
	if !apps {
		return false, false, true
	}
	if alias && expected == "" {
		writeErrCode(w, http.StatusPreconditionRequired, "expected_manager_required", i18n.Err("hub-computer_apps.owner_required"))
		return false, alias, false
	}
	if expected != "" && expected != id {
		writeErrCode(w, http.StatusConflict, "machine_owner_changed", i18n.Err("hub-computer_apps.owner_changed"))
		return false, alias, false
	}
	return expected != "", alias, true
}
