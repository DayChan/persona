package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

// A publisher can use the existing devices.call relay without requiring a new
// package selector in the manager that carries the node. No installation logic
// or arbitrary node method is exposed here.
type nodeUpdateRelayRequest struct {
	ManagerID  string          `json:"manager_id"`
	MachineUID string          `json:"machine_uid"`
	Executable string          `json:"executable"`
	OS         string          `json:"os"`
	Arch       string          `json:"arch"`
	Params     json.RawMessage `json:"params"`
}

func (h *Hub) nodeUpdateRelay(w http.ResponseWriter, r *http.Request) {
	if principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, errors.New("node updates need full credentials"))
		return
	}
	methods := map[string]string{"info": "node.info", "sessions": "sessions.list", "begin": "node.update.begin",
		"chunk": "node.update.chunk", "commit": "node.update.commit", "abort": "node.update.abort", "confirm": "node.update.confirm"}
	method, ok := methods[r.PathValue("action")]
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("unknown node update action"))
		return
	}
	var p nodeUpdateRelayRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeErr(w, http.StatusBadRequest, errors.New("expected one request object"))
		return
	}
	if p.ManagerID == "" || p.MachineUID == "" || !filepath.IsAbs(p.Executable) || p.OS == "" || p.Arch == "" {
		writeErr(w, http.StatusBadRequest, errors.New("explicit manager, machine UID, executable and platform are required"))
		return
	}
	mid := r.PathValue("mid")
	m := h.localMachine(mid)
	if m == nil {
		writeErr(w, http.StatusNotFound, errors.New("unknown machine"))
		return
	}
	if h.machineOwner(mid) != p.ManagerID {
		writeErr(w, http.StatusConflict, errors.New("machine owner changed"))
		return
	}
	ctx, cancel := context.WithTimeout(withCaller(r.Context(), p.ManagerID), 45*time.Second)
	defer cancel()
	var info proto.NodeInfo
	if err := m.call(ctx, "node.info", map[string]any{}, &info); err != nil {
		callErr(w, err)
		return
	}
	if info.MachineUID != p.MachineUID || info.Executable != p.Executable || info.OS != p.OS || info.Arch != p.Arch || h.machineOwner(mid) != p.ManagerID {
		writeErr(w, http.StatusConflict, errors.New("node identity changed"))
		return
	}
	if !info.NodeUpdate || !slices.Contains(info.Capabilities, nodeproto.NodeUpdate) {
		writeErr(w, http.StatusConflict, errors.New("resident node does not support transactional updates"))
		return
	}
	if method == "node.info" {
		writeJSON(w, info)
		return
	}
	params := p.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(params)), "{") || !json.Valid(params) {
		writeErr(w, http.StatusBadRequest, errors.New("params must be an object"))
		return
	}
	if method == "sessions.list" && string(params) != "{}" {
		writeErr(w, http.StatusBadRequest, errors.New("sessions takes no parameters"))
		return
	}
	var out json.RawMessage
	if err := m.call(ctx, method, params, &out); err != nil {
		callErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (h *Hub) nodeUpdateRelayRoutes(api *http.ServeMux) {
	api.HandleFunc("POST /api/v1/machines/{mid}/node-update/{action}", h.nodeUpdateRelay)
}
