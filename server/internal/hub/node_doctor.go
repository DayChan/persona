package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// #2429: the machine page's "What this computer can do" card shows what `personad node doctor` finds on a computer (docs/server/machines.md
// "Self-check"). The report names paths and the commands of the user's own hooks, and the fix rewrites files on the computer,
// so both routes are for a full device alone; an answer-only device and the tokens an extension holds are refused
// before the node hears of it. GET only reads: the hub never sends fix to node.doctor, so a query string cannot ask
// for a write. The old node that does not know the method answers "unknown method", which the web turns into "Update node".

// nodeDoctor relays method (no parameters) to the machine's node and returns the report as it is.
func (h *Hub) nodeDoctor(method string, timeout time.Duration) http.HandlerFunc {
	return h.withMachine(func(w http.ResponseWriter, r *http.Request, m *Machine) {
		if principalOf(r.Context()).Role != roleFull {
			writeErr(w, http.StatusForbidden, errors.New("a computer's self-check needs a full device"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		var out json.RawMessage
		if err := m.call(ctx, method, map[string]any{}, &out); err != nil {
			callErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(out)
	})
}

// nodeDoctorRoutes are the node's self-check and its fix.
func (h *Hub) nodeDoctorRoutes(api *http.ServeMux) {
	// #2429 node self-check (node_doctor.go): the report, and rewriting Persona's own generated files; full devices only
	api.HandleFunc("GET /api/v1/machines/{mid}/doctor", h.nodeDoctor("node.doctor", 45*time.Second))
	api.HandleFunc("POST /api/v1/machines/{mid}/doctor/fix", h.nodeDoctor("node.doctor.fix", 60*time.Second))
}
