package hub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"

	"github.com/nwverse/persona/server/internal/i18n"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The inbox is the manager's (internal/assistant's inbox*.go), its sources too (internal/extmgr inbox.go): the hub
// relays the apps' requests on its routes.

// inboxRoutesRelayed relays the apps' requests on the inbox's routes to the manager (http.request); while it is not
// connected they are answered 503 assistant_offline. A reply keeps its durable receipt (reply_receipts.go), except
// one refused for that (nothing went out: the same reply may go once the manager is back) or cut off on the way
// (assistant_in_doubt: it may have gone out; asked again, the manager's send id has the hub send it once:
// sendInboxOnce).
func (h *Hub) inboxRoutesRelayed(api *http.ServeMux) {
	// An item is its manager's: the request names it (the manager its list stamped on it), else the default.
	relay := func(w http.ResponseWriter, r *http.Request) { h.forwardOwned(w, r, h.byRequest) }
	for _, pattern := range assistantwire.InboxRoutes {
		if key, ok := mergedLists[pattern]; ok { // the list: every manager's items
			api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { h.mergeList(w, r, key, h.forwardHTTP) })
			continue
		}
		if pattern == "POST /api/v1/inbox/{id}/reply" {
			receipt := h.withReplyReceipt("inbox_reply", func(w http.ResponseWriter, r *http.Request) {
				h.forwardSend(w, r, managerOfContext(r))
			})
			api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
				if answerOnly(r) && !answerMayReply(w, r) {
					return
				}
				id, ok := h.owner(w, r, h.byRequest)
				if !ok {
					return
				}
				r = withManager(r, id)
				if h.busConnOf(id) == nil {
					writeErrCode(w, http.StatusServiceUnavailable, assistantwire.CodeAssistantOffline, errAssistantOffline)
					return
				}
				receipt(w, r)
			})
			continue
		}
		if pattern == "POST /api/v1/inbox/{id}/suggestion" {
			// Confirming a reply sends it: cut off on the way it is in doubt, like a reply (the manager's send id has
			// it sent once). What an answer-only device may do the manager checks (it knows the suggestion).
			// The item's manager, as for a reply (#5298).
			api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
				id, ok := h.owner(w, r, h.byRequest)
				if !ok {
					return
				}
				if h.busConnOf(id) == nil {
					writeErrCode(w, http.StatusServiceUnavailable, assistantwire.CodeAssistantOffline, errAssistantOffline)
					return
				}
				h.forwardSend(w, r, id)
			})
			continue
		}
		if pattern == "POST /api/v1/inbox/{id}/handoff" {
			// Handing an item to the main conversation sends a message there: the item's manager, kept like a reply's
			// receipt (a body client_id), in doubt when cut off on the way.
			receipt := h.withReplyReceipt("inbox_handoff", func(w http.ResponseWriter, r *http.Request) {
				h.forwardSend(w, r, managerOfContext(r))
			})
			api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
				id, ok := h.owner(w, r, h.byRequest)
				if !ok {
					return
				}
				r = withManager(r, id)
				if h.busConnOf(id) == nil {
					writeErrCode(w, http.StatusServiceUnavailable, assistantwire.CodeAssistantOffline, errAssistantOffline)
					return
				}
				receipt(w, r)
			})
			continue
		}
		api.HandleFunc(pattern, relay)
	}
}

// answerMayReply holds an answer-only device (the watch) to replying with text, here at the hub as before the inbox
// moved (the manager checks it too): no documents' status moves, no "/agent …" (posted as the user it would start an
// agent). False when it answered 403.
func answerMayReply(w http.ResponseWriter, r *http.Request) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	_ = r.Body.Close()
	if err != nil {
		writeBodyErr(w, err, err)
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var in struct {
		Text    string            `json:"text"`
		Changes []json.RawMessage `json:"changes"`
	}
	_ = json.Unmarshal(raw, &in)
	if len(in.Changes) > 0 || assistantwire.AsksAgent(in.Text) {
		writeErr(w, http.StatusForbidden, i18n.ErrEnglish("inbox-errors.reply_text_only"))
		return false
	}
	return true
}
