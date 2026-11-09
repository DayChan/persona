package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// POST /api/v1/notify lets an extension with the notify scope (or any full credential) alert the user's devices, at the level it
// decides: what deserves a nudge (an agent stuck, a scheduled task failing, the same session not twice in half an
// hour) is the extension's call, the delivery is the hub's (docs/extensions/extensions.md "Sending notifications", #1477).

// Notification levels a caller may ask for (UNNotificationInterruptionLevel). "critical" needs an Apple entitlement and
// is not offered; "passive" never alerts, which is not what this is for.
const (
	levelActive        = "active"
	levelTimeSensitive = "time-sensitive"
)

// notifyRequest is the body of POST /api/v1/notify.
type notifyRequest struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	MachineID string `json:"machine_id,omitempty"` // with session_id: a tap opens that session; muted sessions get nothing
	SessionID string `json:"session_id,omitempty"`
	Level     string `json:"level,omitempty"` // active (default) | time-sensitive
	// ID: the same id again replaces the earlier alert on the phone (apns-collapse-id) instead of stacking.
	ID string `json:"id,omitempty"`
	// Silent: no sound (`personad notify --silent`, docs/integrations/script-cli.md).
	Silent bool `json:"silent,omitempty"`

	// Extension notification: only accepted when the caller is an extension token (roleExtension, with the notify scope).
	UI      string              `json:"ui,omitempty"`
	State   string              `json:"state,omitempty"`
	Actions []wire.NotifyAction `json:"actions,omitempty"`
}

const (
	notifyTitleMost = 120
	notifyBodyMost  = 1000
	// notifyIDMost leaves room for notifyCollapse's prefix inside APNs' 64-byte apns-collapse-id.
	notifyIDMost  = 56
	notifyRefMost = 128 // machine_id, session_id
	notifyUIMost  = 128 // extension ui contribution id
	// notifyStateMost bounds state payload passed to ui.action.
	notifyStateMost                  = 2000
	notifyActionsMax                 = 3
	notifyActionIDMost               = 128
	notifyActionTitleMost            = 40
	notifyActionInputPlaceholderMost = 80
	// notifyPayloadMost keeps the whole push under APNs' 4 KB with room for hub_id; the relay shortens alerts
	// itself (sealFor), a direct push would be refused.
	notifyPayloadMost = 3500
	// notifyCollapse keeps a caller's ids apart from the hub's own (interaction ids).
	notifyCollapse = "notify:"
	// kindAlert is the WebSocket notify event's kind for these (docs/protocol/protocol.md "Persistent connection notifications").
	kindAlert = "alert"
)

func (r *notifyRequest) validate() error {
	r.Title, r.Body = strings.TrimSpace(r.Title), strings.TrimSpace(r.Body)
	switch {
	case r.Title == "" && r.Body == "":
		return errors.New("title or body is required")
	case utf8.RuneCountInString(r.Title) > notifyTitleMost:
		return errors.New("title is too long")
	case utf8.RuneCountInString(r.Body) > notifyBodyMost:
		return errors.New("body is too long")
	case len(r.ID) > notifyIDMost:
		return errors.New("id is too long")
	case len(r.MachineID) > notifyRefMost || len(r.SessionID) > notifyRefMost:
		return errors.New("machine_id or session_id is too long")
	case (r.MachineID == "") != (r.SessionID == ""):
		return errors.New("machine_id and session_id go together")
	}
	switch r.Level {
	case "":
		r.Level = levelActive
	case levelActive, levelTimeSensitive:
	default:
		return errors.New("level must be active or time-sensitive")
	}

	r.UI = strings.TrimSpace(r.UI)
	if len(r.UI) > notifyUIMost {
		return errors.New("ui is too long")
	}
	if len(r.State) > notifyStateMost {
		return errors.New("state is too long")
	}
	if len(r.Actions) > notifyActionsMax {
		return errors.New("actions cannot have more than 3 buttons")
	}
	seen := make(map[string]bool, len(r.Actions))
	for i := range r.Actions {
		a := &r.Actions[i]
		a.ID, a.Title = strings.TrimSpace(a.ID), strings.TrimSpace(a.Title)
		if a.ID == "" {
			return errors.New("action id is required")
		}
		if len(a.ID) > notifyActionIDMost {
			return errors.New("action id is too long")
		}
		if seen[a.ID] {
			return errors.New("action ids must be unique")
		}
		seen[a.ID] = true
		if a.Title == "" {
			return errors.New("action title is required")
		}
		if utf8.RuneCountInString(a.Title) > notifyActionTitleMost {
			return errors.New("action title is too long")
		}
		switch a.Role {
		case "", "normal", "destructive":
		default:
			return errors.New("action role must be normal or destructive")
		}
		if a.Input != nil && utf8.RuneCountInString(a.Input.Placeholder) > notifyActionInputPlaceholderMost {
			return errors.New("action input placeholder is too long")
		}
	}
	return nil
}

// actionIDsHash computes an 8-hex hash of the action IDs for the category identifier (ext.<extension>.<hash>).
func actionIDsHash(actions []wire.NotifyAction) string {
	ids := make([]string, len(actions))
	for i, a := range actions {
		ids[i] = a.ID
	}
	sum := sha256.Sum256([]byte(strings.Join(ids, ",")))
	return hex.EncodeToString(sum[:4])
}

// notifyPayload is the APNs alert for a caller's notification. Like the interaction alerts it threads by session and
// carries machine_id / session_id, so a tap opens the session. For extension callers (extID != ""), missing session threads
// by extension.<extID>, actions set aps.category (ext.<extID>.<hash>), and extension_notification carries the action spec.
func notifyPayload(r notifyRequest, machine, extID string) map[string]any {
	alert := map[string]any{"title": r.Title, "body": r.Body}
	if machine != "" {
		alert["subtitle"] = machine
	}
	aps := map[string]any{"alert": alert, "sound": "default", "interruption-level": r.Level}
	if r.Silent {
		delete(aps, "sound")
	}
	if r.SessionID != "" {
		aps["thread-id"] = r.SessionID
	} else if extID != "" {
		aps["thread-id"] = "extension." + extID
	}
	if extID != "" && len(r.Actions) > 0 {
		aps["category"] = "ext." + extID + "." + actionIDsHash(r.Actions)
		// Register a new category before delivery even when the app is in the background.
		aps["mutable-content"] = 1
	}
	payload := map[string]any{"aps": aps, "machine_id": r.MachineID, "session_id": r.SessionID}
	if extID != "" {
		extNotif := map[string]any{"extension": extID}
		if r.UI != "" {
			extNotif["ui"] = r.UI
		}
		if r.State != "" {
			extNotif["state"] = r.State
		}
		if len(r.Actions) > 0 {
			extNotif["actions"] = r.Actions
		}
		payload["extension_notification"] = extNotif
	}
	return payload
}

// extensionOf is the extension a caller is, "" for any other credential.
func extensionOf(p principal) string {
	if p.Role == roleExtension {
		return p.Extension
	}
	return ""
}

// machineNameFor is the name shown under a notification that opens a session: its machine's.
func (h *Hub) machineNameFor(in notifyRequest) string {
	if in.SessionID == "" {
		return ""
	}
	return h.machineName(in.MachineID)
}

// deliverNotify sends a validated notification to the devices that take them: the reply is {"sent":true} when a
// recipient is selected, or {"sent":false,"reason":"muted"|"no_device"|"filtered"}. An error is the caller's
// (a payload too large for APNs). machine is the subtitle:
// the machine's name, "" for none. manager is whose it is (an extension's manager, a computer's for its
// `personad notify` or for a request naming that computer): devices that turned that manager's pushes off skip it;
// "" (no extension and no computer of this hub named) is the hub's own, which no device mutes.
func (h *Hub) deliverNotify(in notifyRequest, machine, extID, manager string) (map[string]any, error) {
	if in.SessionID != "" && h.muted(in.MachineID, in.SessionID) {
		return map[string]any{"sent": false, "reason": "muted"}, nil
	}
	payload := notifyPayload(in, machine, extID)
	if raw, _ := json.Marshal(payload); len(raw) > notifyPayloadMost {
		return nil, errors.New("notification is too large")
	}
	collapse := ""
	if in.ID != "" {
		collapse = notifyCollapse + in.ID
	}
	// Something that needs you: devices that turned off "Needs you" (prefs.input) get none of these either.
	wants := pushWants{Kind: "input"}.fn()
	build := func(i18n.Lang) map[string]any { return payload }
	pushSelected, pushReachable := h.push.sendFrom(manager, build, collapse, wants, quietNone, nil)
	ws := build
	if in.ID != "" {
		// The WebSocket event carries the id itself (not the push), so an Android phone replaces the earlier alert too.
		withID := maps.Clone(payload)
		withID["id"] = in.ID
		ws = func(i18n.Lang) map[string]any { return withID }
	}
	wsSelected, wsReachable := h.notifyWSFrom(manager, kindAlert, ws, wants, quietNone)
	if pushSelected+wsSelected == 0 {
		reason := "no_device"
		if pushReachable+wsReachable > 0 {
			reason = "filtered"
		}
		return map[string]any{"sent": false, "reason": reason}, nil
	}
	return map[string]any{"sent": true}, nil
}

// scriptNotify is a node's `personad notify` (node/scriptcli.go): the machine is the node's own, the session the one
// that runs in the caller's pane when a hook told the node. It carries no extension and no actions, and is the
// machine's manager's (machineOwner): a device that muted that manager gets none.
func (m *Machine) scriptNotify(params json.RawMessage) (any, error) {
	var in notifyRequest
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, err
	}
	in.Actions, in.UI, in.State = nil, "", ""
	if in.SessionID != "" {
		in.MachineID = m.cfg.ID
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	in.Body = fitBody(in, m.name())
	return m.hub.deliverNotify(in, m.name(), "", m.hub.machineOwner(m.cfg.ID))
}

// fitBody is in's body cut (ending in …) so that its push fits notifyPayloadMost. The node cuts a script's texts by
// characters (docs/integrations/script-cli.md: cut, not refused), but the payload is bounded in bytes: 1000 emoji, or 1000 of
// < > & (escaped six bytes each in JSON), would otherwise be refused and the notification lost.
func fitBody(in notifyRequest, machine string) string {
	fits := func(body string) bool {
		in.Body = body
		raw, _ := json.Marshal(notifyPayload(in, machine, ""))
		return len(raw) <= notifyPayloadMost
	}
	body := in.Body
	if fits(body) {
		return body
	}
	runes := []rune(body)
	lo, hi := 0, len(runes)-1 // the longest prefix that fits with "…" after it is in [lo, hi]
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if fits(string(runes[:mid]) + "…") {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 { // not even "…" fits (the title alone is too large): deliverNotify says so
		return body
	}
	return strings.TrimSpace(string(runes[:lo])) + "…"
}

func (h *Hub) notifyRoutes(api *http.ServeMux) {
	api.HandleFunc("POST /api/v1/notify", func(w http.ResponseWriter, r *http.Request) {
		p := principalOf(r.Context())
		var in notifyRequest
		if err := decodeJSONBody(w, r, &in, 16<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if p.Role != roleExtension && (len(in.Actions) > 0 || in.UI != "" || in.State != "") {
			writeErr(w, http.StatusBadRequest, errors.New("actions, ui and state are only accepted for extension callers"))
			return
		}
		if err := in.validate(); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		// An extension's notification names only its own manager's machine and session (ownership.go ownsRef).
		if p.Role == roleExtension && p.Manager != "" && !h.ownsRef(p.Manager, in.MachineID, in.SessionID) {
			writeErrCode(w, http.StatusForbidden, codeOtherManager, errOtherManager)
			return
		}
		// Whose it is, for the devices that muted a manager: an extension's is its manager's; any other credential's that
		// names a computer of this hub is that computer's manager's, as `personad notify` there (scriptNotify); else
		// (no machine, or an id this hub has no computer for) the hub's own, as before.
		manager := ""
		switch {
		case p.Role == roleExtension:
			manager = p.Manager
		case h.machineByID(in.MachineID) != nil:
			manager = h.machineOwner(in.MachineID)
		}
		res, err := h.deliverNotify(in, h.machineNameFor(in), extensionOf(p), manager)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, res)
	})
}
