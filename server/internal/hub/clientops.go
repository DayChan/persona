package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

var clientOpUUID = lazyre.New(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Body is encoded as bytes so JSONB cannot change whitespace or the original response bytes.
type clientOpResult struct {
	Status      int    `json:"status"`
	Body        []byte `json:"body"`
	ContentType string `json:"content_type,omitempty"`
}

func (h *Hub) withClientOp(kind string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Only clients that send X-Persona-Op-Id use receipts. Messages without it (today's iOS) are deduplicated by
		// client_msg_id in sendMessage (sent_messages), so a non-UUID client_msg_id must still go through.
		id := r.Header.Get("X-Persona-Op-Id")
		if id == "" {
			next(w, r)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<20))
		_ = r.Body.Close()
		if err != nil {
			writeBodyErr(w, err, err)
			return
		}
		params := normalizedOpParams(raw)
		object, _ := params.(map[string]any)
		if !clientOpUUID.MatchString(id) {
			writeErr(w, http.StatusBadRequest, errors.New("operation id must be a UUID"))
			return
		}
		id = strings.ToLower(id)
		if kind == "message" && (object != nil || len(bytes.TrimSpace(raw)) == 0) {
			if object == nil {
				object = map[string]any{}
			}
			object["client_msg_id"] = id
			params = object
			raw, _ = json.Marshal(object)
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		if h.store == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
			return
		}
		target, _ := json.Marshal(map[string]string{"path": r.URL.Path, "query": r.URL.Query().Encode()})
		valid := len(bytes.TrimSpace(raw)) == 0 || json.Valid(raw)
		canonical, _ := json.Marshal([]any{kind, json.RawMessage(target), valid, opDigestParams(kind, raw, params)})
		sum := sha256.Sum256(canonical)
		op, claimed, err := h.store.BeginClientOp(r.Context(), db.ClientOp{
			ID: id, DeviceID: principalOf(r.Context()).ID, Kind: kind, Target: target, Digest: hex.EncodeToString(sum[:]),
		})
		if err != nil || op == nil {
			writeErr(w, http.StatusServiceUnavailable, errors.New("operation receipt unavailable"))
			return
		}
		if op.Digest != hex.EncodeToString(sum[:]) || (principalOf(r.Context()).ID != "" && op.DeviceID != principalOf(r.Context()).ID) {
			writeErrorBody(w, http.StatusConflict, map[string]any{"code": "op_id_reused"})
			return
		}
		if !claimed {
			h.waitClientOp(w, r, op)
			return
		}
		h.executeClientOp(w, r, kind, id, next)
	}
}

// opDigestParams are the params a receipt's digest covers: params, but for a card's answer (resolve) its body as the
// hub and the manager read it (proto.Resolution: field names in any case), each filled-in value replaced by its key,
// and one fixed body for any that is not one. The digest is stored, and the sha256 of a short password in it could be
// guessed offline.
func opDigestParams(kind string, raw []byte, params any) any {
	if kind != "resolve" {
		return params
	}
	var r proto.Resolution
	if json.Unmarshal(raw, &r) != nil {
		return "not a resolution"
	}
	for k := range r.Values {
		r.Values[k] = k
	}
	return r
}

func normalizedOpParams(raw []byte) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value, extra any
	if dec.Decode(&value) == nil && errors.Is(dec.Decode(&extra), io.EOF) {
		return value
	}
	// Invalid JSON still gets a stable digest, allowing a deterministic 400 to be replayed.
	return raw
}

type clientOpWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *clientOpWriter) Header() http.Header { return w.header }
func (w *clientOpWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *clientOpWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.Write(b)
}

// clientOpForgetter drops a receipt claimed for nothing (db.DB): the operation's retry runs it again.
type clientOpForgetter interface {
	ForgetClientOp(ctx context.Context, id string) error
}

// clientOpRetry: an inbox reply or hand-off the manager never got (assistant_offline) or that was cut off on the way
// to it (assistant_in_doubt) keeps no receipt: the same request asked again goes to the manager, whose send id has it
// sent once (extmgr inbox_reply.go sendInboxOnce, a hand-off's session.send_once), instead of the stored refusal
// coming back for good. A notification choice and a conversation's start only release a definitely offline receipt;
// uncertain delivery stays recorded.
func clientOpRetry(kind string, body []byte) bool {
	if kind != "inbox_reply" && kind != "inbox_handoff" && kind != "choose" && kind != "assistant_start" {
		return false
	}
	var res struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &res)
	if kind == "choose" || kind == "assistant_start" { // No request reached the manager; the same op can safely retry.
		return res.Code == assistantwire.CodeAssistantOffline
	}
	return res.Code == assistantwire.CodeAssistantOffline || res.Code == assistantwire.CodeAssistantInDoubt
}

func (h *Hub) executeClientOp(w http.ResponseWriter, r *http.Request, kind, id string, next http.HandlerFunc) {
	state := "unknown"
	var result json.RawMessage
	finished := false
	persist := func() error {
		// A disconnected caller must not prevent the operation's outcome from being recorded.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return h.store.FinishClientOp(ctx, id, state, result)
	}
	defer func() {
		if !finished {
			_ = persist() // A panic may have happened after the side effect.
		}
	}()
	capture := &clientOpWriter{header: make(http.Header)}
	next(capture, r)
	capture.WriteHeader(http.StatusOK)
	state = clientOpState(capture.status, capture.body.Bytes())
	saved := clientOpResult{Status: capture.status, Body: capture.body.Bytes(), ContentType: capture.header.Get("Content-Type")}
	result, _ = json.Marshal(saved)
	var err error
	if f, ok := h.store.(clientOpForgetter); ok && clientOpRetry(kind, saved.Body) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = f.ForgetClientOp(ctx, id)
		cancel()
		if err != nil { // kept, then: the stored answer comes back
			err = persist()
		}
	} else {
		err = persist()
	}
	finished = true
	if err != nil {
		writeErrorBody(w, http.StatusServiceUnavailable, map[string]any{"state": "unknown"})
		return
	}
	for key, values := range capture.header {
		w.Header()[key] = values
	}
	writeClientOpResult(w, saved)
}

func clientOpState(status int, body []byte) string {
	var reply struct {
		Delivered string `json:"delivered"`
	}
	_ = json.Unmarshal(body, &reply)
	if reply.Delivered == "unknown" || reply.Delivered == "yes" && status >= 400 {
		return "unknown"
	}
	if status >= 200 && status < 300 {
		return "done"
	}
	if status == http.StatusBadRequest || status == http.StatusConflict {
		return "failed"
	}
	return "unknown"
}

func writeClientOpResult(w http.ResponseWriter, result clientOpResult) {
	if result.ContentType != "" {
		w.Header().Set("Content-Type", result.ContentType)
	}
	localized := i18n.InboxJSON(i18n.OfWriter(w), result.Body)
	if !bytes.Equal(localized, result.Body) {
		w.Header().Del("Content-Length")
	}
	w.WriteHeader(result.Status)
	_, _ = w.Write(localized)
}

func (h *Hub) waitClientOp(w http.ResponseWriter, r *http.Request, op *db.ClientOp) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		switch op.State {
		case "done", "failed":
			var result clientOpResult
			if json.Unmarshal(op.Result, &result) != nil || result.Status < 200 || result.Status > 599 {
				writeErrorBody(w, http.StatusServiceUnavailable, map[string]any{"state": "unknown"})
				return
			}
			writeClientOpResult(w, result)
			return
		case "unknown":
			// Start, checkout and reply retries must not repeat side effects or replace a recorded HTTP outcome.
			// A recovered pending operation has no result and remains explicitly unknown.
			if op.Kind == "start" || op.Kind == "assistant_start" || op.Kind == "worktree_create" || op.Kind == "lark_send" || op.Kind == "inbox_reply" || op.Kind == "inbox_handoff" || op.Kind == "lark_thread_reply" {
				var result clientOpResult
				if json.Unmarshal(op.Result, &result) == nil && result.Status >= 200 && result.Status <= 599 {
					writeClientOpResult(w, result)
					return
				}
			}
			writeErrorBody(w, http.StatusAccepted, map[string]any{"state": "unknown"})
			return
		}
		select {
		case <-ctx.Done():
			writeErrorBody(w, http.StatusAccepted, map[string]any{"state": "pending"})
			return
		case <-ticker.C:
			fresh, err := h.store.ClientOp(ctx, op.ID)
			if ctx.Err() != nil {
				writeErrorBody(w, http.StatusAccepted, map[string]any{"state": "pending"})
				return
			}
			if err != nil || fresh == nil {
				writeErr(w, http.StatusServiceUnavailable, errors.New("operation receipt unavailable"))
				return
			}
			op = fresh
		}
	}
}

func (h *Hub) getClientOp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !clientOpUUID.MatchString(id) {
		writeErr(w, http.StatusBadRequest, errors.New("operation id must be a UUID"))
		return
	}
	if h.store == nil {
		writeErr(w, http.StatusServiceUnavailable, errors.New("no database"))
		return
	}
	op, err := h.store.ClientOp(r.Context(), strings.ToLower(id))
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	if op == nil || (principalOf(r.Context()).ID != "" && op.DeviceID != principalOf(r.Context()).ID) {
		writeErr(w, http.StatusNotFound, errors.New("no such operation"))
		return
	}
	writeJSON(w, op)
}

func (h *Hub) startClientOps(ctx context.Context) error {
	if err := h.store.RecoverClientOps(ctx); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			cleanCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := h.store.CleanupClientOps(cleanCtx, time.Now().UTC().AddDate(0, 0, -30))
			cancel()
			if err != nil && ctx.Err() == nil {
				h.logf("clean client operations: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return nil
}
