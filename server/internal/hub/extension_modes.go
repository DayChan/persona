package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// An extension's token changes the permission mode of a conversation its extension opened, and nothing else of any
// session (agents.permission_mode, docs/extensions/extensions.md "Hub API: scopes"): who opened it is what its
// manager's snapshot says (assistantwire.SessionView.OpenedBy). Approving a plan sets its session's permission mode
// too, so it is held to the same. Every refusal goes to the extension's audit in its manager (extwire.HubAudit).

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

// planApproveMode is the permission mode approving a plan sets when the answer names none (the agent's).
const planApproveMode = "bypassPermissions"

// extensionPlanRefused is why extension token p may not approve card id with mode ("" when it may), and the card's
// machine and session: approving a plan sets its session's permission mode (planApproveMode when mode is ""). Only a
// plan's answer approves, so one for a card the hub does not hold pending is refused as well.
func (h *Hub) extensionPlanRefused(p principal, id, mode string) (why, mid, sid string) {
	ix, pending := h.pendingInteraction(id)
	if !pending {
		return modeRefusedNotOpened, "", ""
	}
	if ix.Kind != proto.IxPlan {
		return "", ix.MachineID, ix.SessionID
	}
	fields := map[string]any{"permission_mode": cmp.Or(mode, planApproveMode)}
	return h.extensionModeRefused(p, ix.MachineID, ix.SessionID, fields), ix.MachineID, ix.SessionID
}

// refuseModeScope refuses a permission mode change by extension token p, not granted agents.permission_mode: the auth
// refuses the scope's one route (POST /api/v1/sessions/{mid}/{sid}/modes) before the mux routes it, so the machine,
// session and mode come from the path and the body here.
func (h *Hub) refuseModeScope(w http.ResponseWriter, r *http.Request, p principal) {
	mid, sid := "", ""
	if rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/sessions/"); ok {
		if parts := strings.Split(rest, "/"); len(parts) == 3 {
			mid, sid = parts[0], parts[1]
		}
	}
	var fields map[string]any
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&fields)
	mode, _ := fields["permission_mode"].(string)
	h.refuseExtensionMode(w, r, p, mid, sid, mode, modeRefusedScope)
}

// refuseExtensionMode answers 403 for the change to mode extension token p may not make, and has its manager record
// why. What the request named is capped: it is the caller's.
func (h *Hub) refuseExtensionMode(w http.ResponseWriter, r *http.Request, p principal, mid, sid, mode, why string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	mid, sid, mode = rowText(mid, 128), rowText(sid, 128), rowText(mode, 64)
	audit := extwire.Audit{Extension: p.Extension, Action: extwire.AuditPermissionModeRefused,
		Detail: map[string]any{"machine_id": mid, "session_id": sid, "mode": mode, "reason": why}}
	if err := h.extCallTo(ctx, p.Manager, extwire.HubAudit, audit, nil); err != nil {
		h.logf("extension %s: permission mode %q of %s/%s refused (%s); not recorded in its audit: %v", p.Extension, mode, mid, sid, why, err)
	}
	body := errorBody(http.StatusForbidden, codeModeRefused, errors.New("an extension may change only the permission mode of a conversation it opened"))
	body["reason"] = why
	writeErrorBody(w, http.StatusForbidden, body)
}
