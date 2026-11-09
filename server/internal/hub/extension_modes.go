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
// manager's snapshot says (assistantwire.SessionView.OpenedBy), and a main conversation is the user's whoever opened
// it. Approving a plan sets its session's permission mode too, and so may the keys that answer a terminal's dialog (a
// screen card): both are held to the same. Every refusal goes to the extension's audit in its manager
// (extwire.HubAudit).

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
	return h.extensionNotOpened(p, mid, sid)
}

// extensionNotOpened is modeRefusedNotOpened unless session sid on machine mid is a conversation extension token p's
// extension opened, on its own manager's machine, and not a main conversation ("" then).
func (h *Hub) extensionNotOpened(p principal, mid, sid string) string {
	owner, v := h.conversation(sid)
	if p.Extension == "" || p.Manager == "" || owner != p.Manager || v.MachineID != mid || v.OpenedBy != p.Extension ||
		h.mainOf(mid, sid) {
		return modeRefusedNotOpened
	}
	return ""
}

// planApproveMode is the permission mode approving a plan sets when the answer names none (the agent's).
const planApproveMode = "bypassPermissions"

// extensionResolveRefused is why extension token p may not send res as card id's answer ("" when it may), the
// permission mode it would set ("" for a screen card's) and the card's machine and session. Approving a plan sets its
// session's mode (planApproveMode when res names none). Any answer to a screen card but a bare dismiss presses keys in
// its terminal, whichever field carries them, and a dialog's keys may switch the mode: it is held to the same, without
// telling one dialog from another. An answer that would do either to a card the hub does not hold pending is refused.
func (h *Hub) extensionResolveRefused(p principal, id string, res proto.Resolution) (why, mode, mid, sid string) {
	approve := res.Approve != nil && *res.Approve
	if approve {
		mode = cmp.Or(res.Mode, planApproveMode)
	}
	ix, pending := h.pendingInteraction(id)
	switch {
	case !pending:
		if approve || pressesKeys(res) {
			return modeRefusedNotOpened, mode, "", ""
		}
		return "", "", "", ""
	case ix.Kind == proto.IxPlan && approve:
		return h.extensionModeRefused(p, ix.MachineID, ix.SessionID, map[string]any{"permission_mode": mode}), mode, ix.MachineID, ix.SessionID
	case ix.Kind == proto.IxScreen && !bareDismiss(res):
		if !slices.Contains(p.Scopes, extwire.ScopeAgentsPermissionMode) {
			return modeRefusedScope, "", ix.MachineID, ix.SessionID
		}
		return h.extensionNotOpened(p, ix.MachineID, ix.SessionID), "", ix.MachineID, ix.SessionID
	}
	return "", "", ix.MachineID, ix.SessionID
}

// pressesKeys: res carries what a screen card's answer types or picks (assistant/fleet_ix_screen.go screenAnswer).
func pressesKeys(res proto.Resolution) bool {
	return res.Option != "" || len(res.Options) > 0 || len(res.Keys) > 0 || len(res.PreKeys) > 0 || res.Text != ""
}

// bareDismiss: res hands a screen card back to the computer and nothing else; its manager presses no key for it.
func bareDismiss(res proto.Resolution) bool {
	return res.Dismiss && !res.Cancel && (res.Approve == nil || !*res.Approve) && !pressesKeys(res)
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
