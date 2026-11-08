package hub

import (
	"net/http"
)

// sessionRoutes are the conversations: the list, starting one, its items, and what the user does in it.
func (h *Hub) sessionRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/sessions", h.listSessions)
	api.HandleFunc("POST /api/v1/sessions", h.withClientOp("start", h.startSession))
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/items", h.withMachine(h.sessionItems))
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/items/{iid}/image", h.withMachine(h.sessionImage))
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/items/{iid}/full", h.withMachine(h.sessionItemFull))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/messages", h.withClientOp("message", h.withMachine(h.sendMessage)))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/attach", h.proxyBody("session.attach"))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/interrupt", h.withClientOp("interrupt", h.withMachine(h.interruptSession)))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/keys", h.proxyBody("session.keys"))
	// #2436: rewind a Claude session to before one of its messages (rewind.go); a full device only, it restores files
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/rewind", h.withClientOp("rewind", h.withMachine(h.rewindSession)))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/fork", h.withClientOp("fork", h.proxyBody("session.fork"))) // "Branch to new chat" (node/fork.go)
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/close", h.withClientOp("close", h.proxyBody("session.close")))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/queue", h.proxyBody("session.queue.edit"))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/queue/send-now", h.proxyBody("session.queue.send_now")) // #6088
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/rename", h.withClientOp("rename", h.withMachine(h.renameSession)))
	api.HandleFunc("DELETE /api/v1/sessions/{mid}/{sid}", h.withMachine(h.deleteSession))
	// Model, permission, effort and the independent ultracode switch may each be left out (ultracode.go).
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/modes", h.withClientOp("modes", h.withMachine(h.sessionModes)))
	api.HandleFunc("POST /api/v1/machines/{mid}/spaces/{wid}/rename", h.withMachine(h.renameSpace))
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/info", h.proxy("session.info", func(r *http.Request) any {
		return map[string]any{"session_id": r.PathValue("sid")}
	}))
	api.HandleFunc("POST /api/v1/sessions/{mid}/{sid}/screen/before", paneAddressFullOnly(h.proxyBody("session.screen.before")))
	api.HandleFunc("GET /api/v1/sessions/{mid}/{sid}/screen", paneAddressFullOnly(h.proxy("session.screen", func(r *http.Request) any {
		q := r.URL.Query()
		return map[string]any{"session_id": r.PathValue("sid"), "lines": atoi(q.Get("lines")), "source": q.Get("source"), "format": q.Get("format")}
	})))
}
