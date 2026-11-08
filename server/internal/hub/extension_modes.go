package hub

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
)

// An extension's token changes the permission mode of a conversation its extension opened, and nothing else of any
// session (agents.permission_mode, docs/extensions/extensions.md "Hub API: scopes"): who opened it is what its
// manager's snapshot says (assistantwire.SessionView.OpenedBy). Every refusal goes to the extension's audit in its
// manager (extwire.HubAudit).

// codeModeRefused answers a permission mode change an extension's token may not make; the body's reason says why.
const codeModeRefused = "permission_mode_refused"

// Why an extension's token may not change a session's permission mode.
const (
	modeRefusedScope     = "scope"      // its extension was not granted agents.permission_mode
	modeRefusedFields    = "fields"     // the request changes more than the permission mode
	modeRefusedNotOpened = "not_opened" // not a conversation its extension opened
)

// extensionModeRefused is why extension token p may not make the change fields asks of session sid on machine mid (""
// when it may): the permission mode alone, of a conversation its extension opened, on its own manager's machine.
func (h *Hub) extensionModeRefused(p principal, mid, sid string, fields map[string]any) string {
	if !slices.Contains(p.Scopes, extwire.ScopeAgentsPermissionMode) {
		return modeRefusedScope
	}
	if mode, ok := fields["permission_mode"].(string); !ok || mode == "" || len(fields) != 1 {
		return modeRefusedFields
	}
	owner, v := h.conversation(sid)
	if p.Extension == "" || p.Manager == "" || owner != p.Manager || v.MachineID != mid || v.OpenedBy != p.Extension {
		return modeRefusedNotOpened
	}
	return ""
}

// refuseExtensionMode answers 403 for the change extension token p may not make, and has its manager record why.
func (h *Hub) refuseExtensionMode(w http.ResponseWriter, r *http.Request, p principal, mid, sid, why string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	audit := extwire.Audit{Extension: p.Extension, Action: extwire.AuditPermissionModeRefused,
		Detail: map[string]any{"machine_id": mid, "session_id": sid, "reason": why}}
	if err := h.extCallTo(ctx, p.Manager, extwire.HubAudit, audit, nil); err != nil {
		h.logf("extension %s: permission mode of %s/%s refused (%s); not recorded in its audit: %v", p.Extension, mid, sid, why, err)
	}
	body := errorBody(http.StatusForbidden, codeModeRefused, errors.New("an extension may change only the permission mode of a conversation it opened"))
	body["reason"] = why
	writeErrorBody(w, http.StatusForbidden, body)
}
