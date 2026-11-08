package hub

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nwverse/persona/server/internal/i18n"
)

// deviceRoutes are a device's own: its registration for pushes, its sessions, and which pushes it wants.
func (h *Hub) deviceRoutes(api *http.ServeMux) {
	api.HandleFunc("POST /api/v1/devices", func(w http.ResponseWriter, r *http.Request) {
		var d Device
		if err := decodeJSONBody(w, r, &d, 16<<10); err != nil || d.DeviceID == "" {
			writeBodyErr(w, err, errors.New("device_id is required"))
			return
		}
		// Both name the device token that registered it: credential_id picks the phone a tool call or wake goes to,
		// credential stops the pushes when it is revoked (Pusher.signedOut).
		d.Locale = string(i18n.Resolve(d.Locale)) // its pushes' language; "" (an older app) is the user's
		d.CredentialID = principalOf(r.Context()).ID
		d.Credential = d.CredentialID
		if err := h.push.register(d); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		resp := map[string]any{"ok": true}
		if route, ok := h.push.routeOfID(d.DeviceID); ok {
			resp["push"] = route // the app's Settings › Notifications says which way, or why none (#5998)
		}
		writeJSON(w, resp)
	})
	api.HandleFunc("GET /api/v1/devices/{id}/sessions", h.serveDeviceSessions)
	// Which pushes this device wants (notify.go NotifyPrefs); the app's Settings › Notifications.
	api.HandleFunc("GET /api/v1/devices/{id}/prefs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, h.prefsView(h.push.prefs(r.PathValue("id"))))
	})
	api.HandleFunc("PUT /api/v1/devices/{id}/prefs", func(w http.ResponseWriter, r *http.Request) {
		prefs := h.push.prefs(r.PathValue("id"))
		if err := decodeJSONBody(w, r, &prefs, 16<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if err := prefs.Quiet.validate(); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		// only the roster's managers: a removed one's id would hold one of the 64 places for good (notify.go)
		prefs.MutedManagers = h.rosterManagers(r.Context(), prefs.MutedManagers)
		raw, _ := json.Marshal(prefs)
		prefs = devicePrefs(raw) // normalizes an unknown "done"
		if err := h.push.setPrefsBy(r.PathValue("id"), principalOf(r.Context()).ID, prefs); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.live.settingsChanged()
		writeJSON(w, h.prefsView(prefs))
	})
}
