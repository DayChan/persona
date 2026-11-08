package assistant

import (
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func (s *Service) managerIdentity() string {
	if s.id != "" {
		return s.id
	}
	return assistantwire.AssistantID
}

// New permission preferences require an explicit owner, including the default manager.
// A delayed settings save must not follow a changed roster default or page selection.
func (s *Service) explicitSettingsTarget(w http.ResponseWriter, r *http.Request) bool {
	header := strings.TrimSpace(r.Header.Get(assistantwire.ManagerHeader))
	query := strings.TrimSpace(r.URL.Query().Get(assistantwire.ManagerQuery))
	if header != "" && query != "" && header != query {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.manager_conflict"))
		return false
	}
	target := assistantwire.NamedManager(r)
	if target == "" {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.manager_required"))
		return false
	}
	if target != s.managerIdentity() {
		apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-profile.other_manager"))
		return false
	}
	return true
}

// permissionDefaults describes this manager's implemented scoped persistence and new-session
// inheritance contract. The hub must forward it unchanged, never infer it from its own version.
// Native execution remains the node's responsibility for both Claude and Codex.
type permissionDefaults struct {
	Version int      `json:"version"`
	Manager string   `json:"manager"`
	Modes   []string `json:"modes"`
}

func (s *Service) permissionDefaults() permissionDefaults {
	return permissionDefaults{Version: 1, Manager: s.managerIdentity(), Modes: append([]string(nil), proto.PermissionModes...)}
}
