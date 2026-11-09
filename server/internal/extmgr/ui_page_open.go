package extmgr

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

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
