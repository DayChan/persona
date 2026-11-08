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

func (h *Manager) pageAuthorize(ctx context.Context, s *pageSession) error {
	_, err := h.withUI(ctx, s.extension, s.contribution, uiRequestFromParams(s.params), func(_ *extension.Extension, hp *hubExtension, _ extension.UIItem, _ extension.UISlot, _ extension.UIParams) (extension.ActionResult, error) {
		if hp.proc != s.process || s.process.Generation() != s.run {
			return extension.ActionResult{}, pageError("expired")
		}
		return extension.ActionResult{}, nil
	})
	return err
}

func uiRequestFromParams(p extension.UIParams) uiRequest {
	var in uiRequest
	in.State = p.State
	in.Context.TZ = p.TZ
	in.Context.Locale = p.Locale
	in.Context.Platform = p.Platform
	in.Context.Size = p.Size
	in.Context.UIVersion = p.UIVersion
	if p.Item != nil {
		in.Context.Item = p.Item.ID
	}
	if p.Machine != nil {
		in.Context.Machine = p.Machine.ID
	}
	if p.Launcher != nil {
		in.Context.Launcher = p.Launcher.Name
	}
	return in
}
func (h *Manager) pageOpen(ctx context.Context, who extwire.UIPrincipal, req wire.UIStreamRequest) (json.RawMessage, error) {
	if req.Context.Stream == nil || req.Context.Stream.Protocol != 1 {
		return nil, pageError("negotiation")
	}
	var in uiRequest
	raw, _ := json.Marshal(req.Context)
	_ = json.Unmarshal(raw, &in.Context)
	in.State = req.State
	in.lang = i18n.Resolve(who.Lang)
	var frame json.RawMessage
	_, err := h.withUI(ctx, req.Extension, req.Contribution, in, func(p *extension.Extension, hp *hubExtension, item extension.UIItem, slot extension.UISlot, params extension.UIParams) (extension.ActionResult, error) {
		if !extension.FullPage(item.Placement) {
			return extension.ActionResult{}, pageError("negotiation")
		}
		s, reused, err := h.pageReserveOpen(who, req, hp.proc, slot, params)
		if err != nil {
			return extension.ActionResult{}, err
		}
		if reused {
			frame, err = h.pageRetryOpen(ctx, who, req, s)
			return extension.ActionResult{}, err
		}
		defer close(s.openDone)
		s.manifest = p.Manifest
		s.item = item
		s.components = map[string]bool{}
		for _, tag := range req.Context.Stream.Components {
			s.components[tag] = true
		}
		native, err := h.pageMount(ctx, s)
		ps := &h.uiPages
		ps.mu.Lock()
		if err == nil && !s.closed && s.run == s.process.Generation() {
			if s.latest != nil && s.latest.Generation == native.Generation && s.latest.Version > native.Version {
				native = *s.latest
			}
			s.latest = nil
			s.native = native
			err = h.pageAdvanceLocked(s, false, "")
		}
		s.busy = false
		if err != nil || s.closed || s.run == 0 || s.run != s.process.Generation() || s.suspended {
			h.pageRemoveLocked(s)
			ps.mu.Unlock()
			h.pageDispose(s)
			if err != nil {
				return extension.ActionResult{}, err
			}
			return extension.ActionResult{}, pageError("expired")
		}
		frame = streamFrame("snapshot", req.RequestID, s.tree)
		if !h.pageEmit(s.who, frame) {
			h.pageRemoveLocked(s)
			ps.mu.Unlock()
			h.pageDispose(s)
			return extension.ActionResult{}, pageError("inactive")
		}
		ps.mu.Unlock()
		return extension.ActionResult{}, nil
	})
	return frame, err
}
func (h *Manager) pageMount(ctx context.Context, s *pageSession) (pageNative, error) {
	var native pageNative
	ctx, cancel := context.WithTimeout(ctx, uiTimeout)
	defer cancel()
	// The reservation holds the lazy process alive. Bind its initialized run before
	// dispatch, so cancellation can dispose a still-awaiting source root factory.
	if waiter, ok := s.process.(interface{ Wait(context.Context) error }); ok {
		if err := waiter.Wait(ctx); err != nil {
			return native, err
		}
	}
	h.uiPages.mu.Lock()
	if s.closed || s.process.Generation() == 0 {
		h.uiPages.mu.Unlock()
		return native, pageError("expired")
	}
	s.run = s.process.Generation()
	done := make(chan struct{})
	s.mountDone = done
	s.mountCancel = cancel
	h.uiPages.mu.Unlock()
	defer close(done)
	// Transport is a host lifecycle decision, not an app-supplied UI version hint.
	transport := "stream"
	if s.legacy {
		transport = "snapshot"
	}
	err := s.process.Call(ctx, "ui.page.mount", map[string]any{"session": s.id, "params": s.params, "transport": transport}, &native)
	return native, err
}

// pageReserveOpen keeps idempotency metadata only on quota-bounded live sessions.
// Request IDs select an allocation within an authenticated owner, not across owners.
func (h *Manager) pageReserveOpen(who extwire.UIPrincipal, req wire.UIStreamRequest, process pageProcess, slot extension.UISlot, params extension.UIParams) (*pageSession, bool, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, false, pageError("invalid")
	}
	body := sha256.Sum256(raw)
	ps := &h.uiPages
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, s := range ps.sessions {
		if s.openID != req.RequestID || !samePageOwner(s.who, who) {
			continue
		}
		if s.openBody != body || s.extension != req.Extension || s.contribution != req.Contribution || s.process != process {
			return nil, false, pageError("invalid")
		}
		if s.who.Connection != who.Connection && !s.suspended {
			return nil, false, pageError("forbidden")
		}
		return s, true, nil
	}
	s, err := h.pageReserveLocked(who, req.Extension, req.Contribution, process, slot, params)
	if err != nil {
		return nil, false, err
	}
	s.openID, s.openBody, s.openDone = req.RequestID, body, make(chan struct{})
	return s, false, nil
}

func (h *Manager) pageRetryOpen(ctx context.Context, who extwire.UIPrincipal, req wire.UIStreamRequest, s *pageSession) (json.RawMessage, error) {
	select {
	case <-s.openDone:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	ps := &h.uiPages
	ps.mu.Lock()
	if s.closed || s.tree == nil || s.run == 0 || s.process.Generation() != s.run || !s.expires.IsZero() && time.Now().After(s.expires) {
		ps.mu.Unlock()
		return nil, pageError("expired")
	}
	if s.who.Connection != who.Connection && !s.suspended {
		ps.mu.Unlock()
		return nil, pageError("forbidden")
	}
	if s.suspended {
		resume := wire.UIStreamRequest{Request: "resume", Protocol: 1, RequestID: req.RequestID, Session: s.id, Epoch: s.epoch, Version: s.tree.Version()}
		ps.mu.Unlock()
		raw, _ := json.Marshal(wire.UIStreamClientEnvelope{Type: "extension_ui_stream", Request: &resume})
		return h.uiStream(ctx, extwire.UIStreamCall{Who: who, Envelope: raw})
	}
	// A duplicate read must not allocate, restart a callback, or wait on guest work.
	frame := streamFrame("snapshot", req.RequestID, s.tree)
	if !h.pageEmit(s.who, frame) {
		ps.mu.Unlock()
		return nil, pageError("inactive")
	}
	ps.mu.Unlock()
	return frame, nil
}

func (h *Manager) pageReserve(who extwire.UIPrincipal, ext, ui string, process pageProcess, slot extension.UISlot, params extension.UIParams) (*pageSession, error) {
	ps := &h.uiPages
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return h.pageReserveLocked(who, ext, ui, process, slot, params)
}

// The caller holds uiPages.mu, shared by native and legacy reservation paths.
func (h *Manager) pageReserveLocked(who extwire.UIPrincipal, ext, ui string, process pageProcess, slot extension.UISlot, params extension.UIParams) (*pageSession, error) {
	ps := &h.uiPages
	limits := h.cfg.UIPageSessions
	total, owner, extensionTotal := 0, 0, 0
	for _, s := range ps.sessions {
		total++
		if samePageOwner(s.who, who) {
			owner++
		}
		if s.extension == ext {
			extensionTotal++
		}
	}
	if owner >= boundedLimit(limits.Principal, 8) || extensionTotal >= boundedLimit(limits.Extension, 64) || total >= boundedLimit(limits.Runtime, 256) {
		return nil, pageError("quota")
	}
	if ps.sessions == nil {
		ps.sessions = map[string]*pageSession{}
	}
	s := &pageSession{id: randomHex(24), epoch: randomHex(24), who: who, extension: ext, contribution: ui, process: process, release: process.Hold(), params: params, slot: slot, busy: true, receipts: map[string]pageReceipt{}}
	ps.sessions[s.id] = s
	return s, nil
}

type pageRenderState struct {
	id, epoch  string
	native     pageNative
	manifest   *extension.Manifest
	item       extension.UIItem
	components map[string]bool
	slot       extension.UISlot
	params     extension.UIParams
	tree       *uistream.Tree
}

func pageRenderInput(s *pageSession) pageRenderState {
	params := s.params
	if s.legacy {
		// HTTP source context retains the caller's version, while the manager's
		// private callback tree needs canonical keys. Downgrade only its response.
		params.UIVersion = extension.UIVersion
	}
	return pageRenderState{id: s.id, epoch: s.epoch, native: s.native, manifest: s.manifest, item: s.item, components: s.components, slot: s.slot, params: params, tree: s.tree}
}

// pagePrepare is pure bounded tree work, independent of session mutation and RPC.
func (h *Manager) pagePrepare(s pageRenderState, fence bool, request string) (*uistream.Tree, json.RawMessage, error) {
	n := s.native
	if n.Generation == "" || n.Version < 1 || n.Version > uistream.VersionMost || n.RootKey == "" || len(n.View) > uistream.FrameMost {
		return nil, nil, pageError("invalid")
	}
	if s.manifest != nil {
		var view extension.View
		if err := json.Unmarshal(n.View, &view); err != nil {
			return nil, nil, pageError("invalid")
		}
		if err := checkPushes(s.manifest, s.item, &view, nil); err != nil {
			return nil, nil, err
		}
	}
	fitted, err := extension.FitNative(n.View, s.slot, s.params.UIVersion, h.uiFallbackLang(context.Background(), s.params.Locale))
	if err != nil {
		return nil, nil, err
	}
	if s.components != nil {
		var view wire.View
		_ = json.Unmarshal(fitted, &view)
		for _, section := range view.Sections {
			for _, item := range section.Items {
				if !s.components[item.Type] {
					return nil, nil, pageError("negotiation")
				}
			}
		}
	}
	version := int64(1)
	if s.tree != nil {
		version = s.tree.Version()
		if fence || !bytes.Equal(s.tree.ViewJSON(), fitted) {
			version++
		}
	}
	raw, _ := json.Marshal(map[string]any{"protocol": 1, "session": s.id, "epoch": s.epoch, "version": version, "root_key": n.RootKey, "view": fitted})
	next, err := uistream.DecodeSnapshot(raw)
	if err != nil {
		return nil, nil, err
	}
	if s.tree == nil {
		return next, nil, nil
	}
	delta, snapshot, err := uistream.Diff(s.tree, next)
	if err != nil {
		return nil, nil, err
	}
	if next.Version() == s.tree.Version() {
		return next, nil, nil
	}
	if snapshot {
		return next, streamFrame("snapshot", request, next), nil
	}
	return next, streamFrame("delta", request, delta), nil
}

func (h *Manager) pageAdvanceLocked(s *pageSession, fence bool, request string) error {
	next, frame, err := h.pagePrepare(pageRenderInput(s), fence, request)
	if err != nil {
		return err
	}
	return h.pagePublishLocked(s, next, frame)
}

func (h *Manager) pagePublishLocked(s *pageSession, next *uistream.Tree, frame json.RawMessage) error {
	s.tree = next
	if len(frame) > 0 && !s.suspended && !s.legacy && !h.pageEmit(s.who, frame) {
		return pageError("inactive")
	}
	return nil
}

// pageSnapshot runs on the source reader: validate and coalesce, never call back
// into Proc.Call or wait for an in-flight event while holding a page lock.
func (h *Manager) pageSnapshot(ext string, process pageProcess, raw json.RawMessage) {
	if len(raw) > uistream.FrameMost {
		return
	}
	var notice struct {
		Session  string     `json:"session"`
		Snapshot pageNative `json:"snapshot"`
	}
	if json.Unmarshal(raw, &notice) != nil {
		return
	}
	ps := &h.uiPages
	ps.mu.Lock()
	defer ps.mu.Unlock()
	s := ps.sessions[notice.Session]
	if s == nil || s.closed || s.suspended && !s.recovering || s.extension != ext || s.process != process {
		return
	}
	if s.native.Generation == "" {
		if s.busy && notice.Snapshot.Version > 0 && notice.Snapshot.Version <= uistream.VersionMost {
			n := notice.Snapshot
			s.latest = &n
		}
		return
	}
	if process.Generation() != s.run || notice.Snapshot.Generation != s.native.Generation || notice.Snapshot.Version <= s.native.Version {
		return
	}
	if s.latest == nil || notice.Snapshot.Version > s.latest.Version {
		n := notice.Snapshot
		s.latest = &n
	}
	if s.busy {
		return
	}
	s.busy = true
	if ps.renderSlots == nil {
		ps.renderSlots = make(chan struct{}, 4)
	}
	go h.pageRender(s)
}

// One worker per session fits and diffs coalesced notices off the source reader
// and outside the global session mutex. Lifecycle changes can interrupt work;
// publication is still serialized with snapshots, fences and visibility.
func (h *Manager) pageRender(s *pageSession) {
	ps := &h.uiPages
	for {
		ps.mu.Lock()
		if s.closed || s.suspended || s.latest == nil {
			s.busy = false
			ps.mu.Unlock()
			return
		}
		input := pageRenderInput(s)
		input.native = *s.latest
		s.latest = nil
		if input.native.Version <= s.native.Version {
			ps.mu.Unlock()
			continue
		}
		slots := ps.renderSlots
		ps.mu.Unlock()
		slots <- struct{}{}
		ps.mu.Lock()
		if s.closed || s.suspended {
			s.busy = false
			ps.mu.Unlock()
			<-slots
			return
		}
		ps.mu.Unlock()
		next, frame, err := h.pagePrepare(input, false, "")
		<-slots
		ps.mu.Lock()
		if s.closed {
			s.busy = false
			ps.mu.Unlock()
			return
		}
		if s.process.Generation() != s.run || s.tree != input.tree {
			err = pageError("expired")
		}
		if err == nil {
			s.native = input.native
			err = h.pagePublishLocked(s, next, frame)
		}
		if err != nil {
			s.busy = false
			h.pageRemoveLocked(s)
			ps.mu.Unlock()
			h.pageDispose(s)
			return
		}
		ps.mu.Unlock()
	}
}
func (h *Manager) pageRemoveLocked(s *pageSession) {
	if s.closed {
		return
	}
	s.closed = true
	delete(h.uiPages.sessions, s.id)
}
func (h *Manager) pageDispose(s *pageSession) {
	s.disposeOnce.Do(func() {
		if s.release != nil {
			defer s.release()
		}
		// Cancel an awaiting mount before waiting for its RPC dispatch to finish;
		// its guest factory can remain unresolved until dispose reaches the bridge.
		if s.mountCancel != nil {
			s.mountCancel()
		}
		if s.mountDone != nil {
			<-s.mountDone
		}
		if s.run == 0 || s.process.Generation() != s.run {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.process.Call(ctx, "ui.page.dispose", map[string]any{"session": s.id}, nil)
	})
}
func (h *Manager) pageDropExtension(ext string) {
	ps := &h.uiPages
	ps.mu.Lock()
	var gone []*pageSession
	for _, s := range ps.sessions {
		if ext == "" || s.extension == ext {
			h.pageRemoveLocked(s)
			gone = append(gone, s)
		}
	}
	ps.mu.Unlock()
	h.pageDisposeAll(gone)
}
func pageBatch(sessions []*pageSession, before, work func(*pageSession)) {
	jobs := make(chan *pageSession)
	var workers sync.WaitGroup
	for i := 0; i < min(8, len(sessions)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for s := range jobs {
				work(s)
			}
		}()
	}
	for _, s := range sessions {
		if before != nil {
			before(s)
		}
		jobs <- s
	}
	close(jobs)
	workers.Wait()
}
func (h *Manager) pageDisposeAll(gone []*pageSession) {
	pageBatch(gone, func(s *pageSession) {
		h.pageEmit(s.who, streamFrame("closed", "", s.id))
	}, h.pageDispose)
}

func (h *Manager) pageDisconnect(who extwire.UIPrincipal) {
	ps := &h.uiPages
	ps.mu.Lock()
	var paused []*pageSession
	for _, s := range ps.sessions {
		if samePageOwner(s.who, who) && s.who.Connection == who.Connection {
			s.who.Connection = ""
			s.visibility++
			s.recovering = false
			s.suspended = true
			s.expires = time.Now().Add(5 * time.Minute)
			if s.pauseDone == nil {
				s.pauseDone = make(chan struct{})
				paused = append(paused, s)
			}
		}
	}
	ps.mu.Unlock()
	h.pagePauseAll(paused)
}

func (h *Manager) pagePause(s *pageSession) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := s.process.Call(ctx, "ui.page.suspend", map[string]any{"session": s.id}, nil)
	ps := &h.uiPages
	ps.mu.Lock()
	if s.pauseDone != nil {
		close(s.pauseDone)
		s.pauseDone = nil
	}
	ps.mu.Unlock()
	return err
}
func (h *Manager) pagePauseAll(paused []*pageSession) {
	pageBatch(paused, nil, func(s *pageSession) { _ = h.pagePause(s) })
}
func (h *Manager) pageSweep(now time.Time) {
	ps := &h.uiPages
	ps.mu.Lock()
	var gone []*pageSession
	for _, s := range ps.sessions {
		if (!s.expires.IsZero() && !now.Before(s.expires)) || s.run != 0 && s.process.Generation() != s.run {
			h.pageRemoveLocked(s)
			gone = append(gone, s)
		}
	}
	ps.mu.Unlock()
	h.pageDisposeAll(gone)
}
func (h *Manager) pageBusReset() {
	ps := &h.uiPages
	ps.mu.Lock()
	var paused []*pageSession
	for _, s := range ps.sessions {
		if !s.legacy && !s.suspended {
			s.who.Connection = ""
			s.visibility++
			s.recovering = false
			s.suspended = true
			s.expires = time.Now().Add(5 * time.Minute)
			s.pauseDone = make(chan struct{})
			paused = append(paused, s)
		}
	}
	ps.mu.Unlock()
	if len(paused) > 0 {
		go h.pagePauseAll(paused)
	}
}

func (h *Manager) pageLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			h.pageDropExtension("")
			return
		case now := <-ticker.C:
			h.pageSweep(now)
		}
	}
}
