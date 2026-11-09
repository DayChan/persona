package extmgr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/uistream"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// PageSessionLimits are manager-only bounds, independent of extension service work.
// Zero uses defaults; larger/invalid values are clamped to these security ceilings.
type PageSessionLimits struct {
	Principal int `json:"principal,omitempty"`
	Extension int `json:"extension,omitempty"`
	Runtime   int `json:"runtime,omitempty"`
}

func boundedLimit(n, ceiling int) int {
	if n < 1 || n > ceiling {
		return ceiling
	}
	return n
}

type pageProcess interface {
	Call(context.Context, string, any, any) error
	Hold() func()
	Generation() uint64
}
type pageNative struct {
	Generation string          `json:"generation"`
	Version    int64           `json:"version"`
	RootKey    string          `json:"rootKey"`
	View       json.RawMessage `json:"view"`
}

func (n *pageNative) UnmarshalJSON(raw []byte) error {
	header, err := uistream.DecodePageSnapshot(raw)
	if err != nil {
		return err
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal(raw, &original); err != nil {
		return err
	}
	n.Generation = header.Generation
	n.Version = header.Version
	n.RootKey = header.RootKey
	n.View = original["view"]
	return nil
}

type pagePending struct {
	id   string
	body [32]byte
	done chan struct{}
}
type pageReceipt struct {
	body  [32]byte
	frame json.RawMessage
}
type pageSession struct {
	id, epoch, extension, contribution string
	who                                extwire.UIPrincipal
	process                            pageProcess
	run                                uint64
	release                            func()
	params                             extension.UIParams
	manifest                           *extension.Manifest
	item                               extension.UIItem
	components                         map[string]bool
	slot                               extension.UISlot
	tree                               *uistream.Tree
	native                             pageNative
	latest                             *pageNative // one coalesced source render, never an unbounded delta queue
	busy, suspended, closed            bool
	recovering                         bool   // coalesce source renders until recovery publication
	visibility                         uint64 // newer suspension wins over an in-flight recovery
	legacy                             bool
	legacyUse                          bool // one complete HTTP lifecycle, including its deferred pause
	openID                             string
	openBody                           [sha256.Size]byte
	openDone                           chan struct{} // duplicate opens wait outside the session mutex
	mountDone                          chan struct{}
	mountCancel                        context.CancelFunc
	disposeOnce                        sync.Once
	expires                            time.Time
	receipts                           map[string]pageReceipt
	pending                            *pagePending
	pauseDone                          chan struct{}
	order                              []string
}
type pageSessions struct {
	mu          sync.Mutex
	sessions    map[string]*pageSession
	renderSlots chan struct{} // bounded manager-wide background fitting, not guest service work
}

func pageError(code string) error { return i18n.Err("extension-stream." + code) }

// Classify only manager-owned errors, never localized strings or guest messages.
func pageFailureCode(err error) string {
	var message *i18n.Message
	if errors.As(err, &message) {
		switch message.Key {
		case "extension-stream.expired":
			return "expired"
		case "extension-stream.inactive":
			return "inactive"
		case "extension-stream.forbidden":
			return "forbidden"
		case "extension-stream.invalid":
			return "invalid"
		case "extension-stream.quota":
			return "quota"
		case "extension-stream.negotiation":
			return "negotiation"
		}
	}
	return "rejected"
}
func samePageOwner(a, b extwire.UIPrincipal) bool {
	return a.Hub == b.Hub && a.Principal == b.Principal
}
func pagePrincipalOK(w extwire.UIPrincipal) bool {
	return w.Hub != "" && w.Principal != "" && w.Connection != "" && w.Role == "full" && !w.Paired
}

// streamFrame serializes the canonical generated envelope, overriding only native
// payloads with Tree/Delta themselves, never their lossy DTO projections.
func streamFrame(kind, request string, payload any) json.RawMessage {
	env := wire.UIStreamServerEnvelope{Type: "extension_ui_stream", Kind: kind, RequestID: request}
	switch kind {
	case "snapshot":
		env.Snapshot = &wire.UIStreamSnapshot{}
	case "delta":
		env.Delta = &wire.UIStreamDelta{}
	case "receipt":
		env.Receipt = payload.(*wire.UIStreamReceipt)
	case "closed", "suspended":
		env.Session = payload.(string)
	case "error":
		env.Error = payload.(*wire.UIStreamFailure)
	}
	raw, _ := json.Marshal(env)
	if kind == "snapshot" || kind == "delta" {
		var object map[string]json.RawMessage
		_ = json.Unmarshal(raw, &object)
		object[kind], _ = json.Marshal(payload)
		raw, _ = json.Marshal(object)
	}
	return raw
}

// uiStream is called only through extwire by the hub. Every source session/recipient
// claim is discarded. A session ID is a lookup key, never a bearer credential.
func (h *Manager) uiStream(ctx context.Context, call extwire.UIStreamCall) (json.RawMessage, error) {
	if !pagePrincipalOK(call.Who) {
		return nil, pageError("forbidden")
	}
	env, err := uistream.DecodeClientEnvelope(call.Envelope)
	if err != nil || env.Request == nil {
		return nil, pageError("invalid")
	}
	raw, _ := json.Marshal(env.Request)
	req, err := uistream.DecodeRequest(raw)
	if err != nil {
		return nil, pageError("invalid")
	}
	if req.Request == "open" {
		return h.pageOpen(ctx, call.Who, req)
	}
	if req.Request == "event" && req.Interaction != nil {
		req.Session = req.Interaction.Session
		req.Epoch = req.Interaction.Epoch
	}
	ps := &h.uiPages
	ps.mu.Lock()
	s := ps.sessions[req.Session]
	if s == nil {
		ps.mu.Unlock()
		if req.Request == "close" {
			return streamFrame("closed", req.RequestID, req.Session), nil
		}
		return nil, pageError("expired")
	}
	if !samePageOwner(s.who, call.Who) || req.Epoch != s.epoch || s.closed || s.legacy && call.Who.Connection != "http" {
		ps.mu.Unlock()
		return nil, pageError("forbidden")
	}
	// Rebinding is allowed only after a connection was suspended/disconnected. A
	// second live connection cannot steal a private tree from the first.
	if s.who.Connection != call.Who.Connection && (!s.suspended || req.Request != "resume") {
		ps.mu.Unlock()
		return nil, pageError("forbidden")
	}
	if s.run == 0 || s.process.Generation() != s.run || (!s.expires.IsZero() && time.Now().After(s.expires)) {
		h.pageRemoveLocked(s)
		ps.mu.Unlock()
		h.pageDispose(s)
		return nil, pageError("expired")
	}
	if req.Request == "close" {
		h.pageRemoveLocked(s)
		ps.mu.Unlock()
		h.pageDispose(s)
		return streamFrame("closed", req.RequestID, s.id), nil
	}
	if s.pauseDone != nil && req.Request != "close" {
		done := s.pauseDone
		ps.mu.Unlock()
		select {
		case <-done:
			return h.uiStream(ctx, call)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if req.Request == "event" {
		if req.Interaction == nil {
			ps.mu.Unlock()
			return nil, pageError("invalid")
		}
		eventRaw, _ := json.Marshal(req.Interaction)
		event, err := uistream.DecodeEvent(eventRaw)
		if err != nil {
			ps.mu.Unlock()
			return nil, pageError("invalid")
		}
		decoder := json.NewDecoder(bytes.NewReader(eventRaw))
		decoder.UseNumber()
		var canonicalBody any
		if err := decoder.Decode(&canonicalBody); err != nil {
			ps.mu.Unlock()
			return nil, pageError("invalid")
		}
		canonicalRaw, _ := json.Marshal(canonicalBody)
		body := sha256.Sum256(canonicalRaw)
		// Replay before stale checks, including after source/wire revisions advance.
		if receipt, ok := s.receipts[event.EventID]; ok {
			ps.mu.Unlock()
			if receipt.body != body {
				return nil, pageError("conflict")
			}
			return pageReplayFrame(receipt.frame, req.RequestID), nil
		}
		if pending := s.pending; pending != nil && pending.id == event.EventID {
			ps.mu.Unlock()
			if pending.body != body {
				return nil, pageError("conflict")
			}
			select {
			case <-pending.done:
				return h.uiStream(ctx, call)
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if s.busy || s.suspended {
			ps.mu.Unlock()
			return nil, pageError("inactive")
		}
		action, err := s.tree.EventAction(event)
		if err != nil {
			ps.mu.Unlock()
			return nil, pageError("stale")
		}
		s.busy = true
		pending := &pagePending{id: event.EventID, body: body, done: make(chan struct{})}
		s.pending = pending
		nativeVersion := s.native.Version
		nativeGeneration := s.native.Generation
		saved := s.tree
		ps.mu.Unlock()
		defer func() {
			ps.mu.Lock()
			if s.pending == pending {
				s.pending = nil
			}
			close(pending.done)
			ps.mu.Unlock()
		}()
		// Reauthorize context/contribution/grants before asking for confirmation and
		// again after it. No session lock is held over host RPC or the source reader.
		_, err = h.withUI(ctx, s.extension, s.contribution, uiRequestFromParams(s.params), func(_ *extension.Extension, hp *hubExtension, _ extension.UIItem, _ extension.UISlot, _ extension.UIParams) (extension.ActionResult, error) {
			if hp.proc != s.process || s.process.Generation() != s.run {
				return extension.ActionResult{}, pageError("expired")
			}
			if action.Confirm != "" {
				input, _ := json.Marshal(action)
				resolution, askErr := h.waitInteraction(ctx, proto.Interaction{ID: "ix_" + randomHex(12), Kind: proto.IxPermission, ToolName: "extension.ui", Summary: action.Confirm, ToolInput: input, SessionID: s.id, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(uiTimeout)})
				if askErr != nil || resolution.Allow == nil || !*resolution.Allow || resolution.AnsweredBy != "" {
					return extension.ActionResult{}, pageError("confirmation")
				}
			}
			return extension.ActionResult{}, nil
		})
		if err == nil {
			err = h.pageAuthorize(ctx, s)
		}
		ps.mu.Lock()
		if err != nil || s.closed || s.suspended || s.tree != saved || s.latest != nil || s.process.Generation() != s.run {
			s.busy = false
			if !s.closed && s.latest != nil {
				s.native = *s.latest
				s.latest = nil
				if advanceErr := h.pageAdvanceLocked(s, false, ""); advanceErr != nil {
					h.pageRemoveLocked(s)
					go h.pageDispose(s)
				}
			}
			ps.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return nil, pageError("stale")
		}
		ps.mu.Unlock()
		cctx, cancel := context.WithTimeout(ctx, uiTimeout)
		var result struct {
			Snapshot pageNative      `json:"snapshot"`
			Status   string          `json:"status"`
			Result   json.RawMessage `json:"result"`
		}
		// The native render revision is independent of the emitted wire revision.
		err = s.process.Call(cctx, "ui.page.event", map[string]any{"session": s.id, "ticket": randomHex(16), "interaction": map[string]any{"generation": nativeGeneration, "version": nativeVersion, "eventId": event.EventID, "key": event.Key, "actionId": event.Action, "values": event.Value}}, &result)
		cancel()
		callErr := err
		var presentation json.RawMessage
		if err == nil && result.Status == "accepted" {
			presentation, err = pageResult(s, result.Result)
		}
		ps.mu.Lock()
		defer ps.mu.Unlock()
		s.busy = false
		if s.closed {
			return nil, pageError("expired")
		}
		status := "accepted"
		if err != nil || result.Status != "accepted" {
			status = "ambiguous"
		}
		latest := result.Snapshot
		if s.latest != nil && s.latest.Version > latest.Version {
			latest = *s.latest
		}
		s.latest = nil
		// Every entered/ambiguous callback consumes exactly one wire version. Even an
		// empty delta fences a no-UI write after the bounded receipt is evicted.
		if latest.Generation != "" && latest.Generation == s.native.Generation && latest.Version > s.native.Version {
			s.native = latest
		}
		if err = h.pageAdvanceLocked(s, true, req.RequestID); err != nil {
			h.pageRemoveLocked(s)
			go h.pageDispose(s)
			return nil, err
		}
		frame := pageReceiptFrame(req.RequestID, &wire.UIStreamReceipt{Session: s.id, Epoch: s.epoch, EventID: event.EventID, Version: s.tree.Version(), Status: status}, presentation)
		if len(s.order) >= 64 {
			delete(s.receipts, s.order[0])
			s.order = s.order[1:]
		}
		s.order = append(s.order, event.EventID)
		s.receipts[event.EventID] = pageReceipt{body: body, frame: frame}
		// Keep the ambiguous write receipt, but a failed HTTP load is not a ready View.
		if s.legacy && callErr != nil {
			return nil, callErr
		}
		return frame, nil
	}
	if req.Request == "suspend" {
		s.visibility++
		s.recovering = false
		s.suspended = true
		s.expires = time.Now().Add(5 * time.Minute)
		s.pauseDone = make(chan struct{})
		ps.mu.Unlock()
		if err := h.pagePause(s); err != nil {
			return nil, err
		}
		return streamFrame("suspended", req.RequestID, s.id), nil
	}
	if s.suspended && req.Request == "snapshot" {
		ps.mu.Unlock()
		return nil, pageError("inactive")
	}
	if s.busy {
		ps.mu.Unlock()
		return nil, pageError("inactive")
	}
	s.busy = true
	s.recovering = true
	visibility := s.visibility
	if req.Request == "resume" {
		s.who = call.Who
	}
	ps.mu.Unlock()
	method := "snapshot"
	if req.Request == "resume" {
		method = "resume"
	} else if req.Request != "snapshot" {
		ps.mu.Lock()
		s.busy = false
		s.recovering = false
		ps.mu.Unlock()
		return nil, pageError("invalid")
	}
	if req.Request != "suspend" {
		if err = h.pageAuthorize(ctx, s); err != nil {
			ps.mu.Lock()
			s.busy = false
			s.recovering = false
			ps.mu.Unlock()
			return nil, err
		}
	}
	var native pageNative
	cctx, cancel := context.WithTimeout(ctx, uiTimeout)
	err = s.process.Call(cctx, "ui.page."+method, map[string]any{"session": s.id}, &native)
	cancel()
	ps.mu.Lock()
	defer ps.mu.Unlock()
	s.busy = false
	s.recovering = false
	if !s.closed && s.visibility != visibility {
		s.latest = nil
		return nil, pageError("inactive")
	}
	if s.closed || err != nil || s.process.Generation() != s.run {
		if !s.closed {
			h.pageRemoveLocked(s)
			go h.pageDispose(s)
		}
		return nil, pageError("expired")
	}
	s.suspended = true // suppress deltas until the recovery snapshot is queued
	s.expires = time.Time{}
	if s.latest != nil && s.latest.Version > native.Version {
		native = *s.latest
	}
	s.latest = nil
	if native.Generation != s.native.Generation || native.Version < s.native.Version {
		h.pageRemoveLocked(s)
		go h.pageDispose(s)
		return nil, pageError("stale")
	}
	s.native = native
	if err = h.pageAdvanceLocked(s, false, ""); err != nil {
		h.pageRemoveLocked(s)
		go h.pageDispose(s)
		return nil, err
	}
	// Full snapshot always precedes newly emitted deltas after reconnect/recovery.
	frame := streamFrame("snapshot", req.RequestID, s.tree)
	if !h.pageEmit(s.who, frame) {
		h.pageRemoveLocked(s)
		go h.pageDispose(s)
		return nil, pageError("inactive")
	}
	s.suspended = false
	return frame, nil
}
