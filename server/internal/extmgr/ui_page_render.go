package extmgr

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/uistream"
)

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
