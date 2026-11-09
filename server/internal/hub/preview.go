package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/preview"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Dev server previews (docs/web/preview.md): the node shows a port of its computer in a headless browser and streams it;
// the hub relays. GET …/preview/servers and POST …/preview/start go to the node as they are; …/preview/connect is a
// WebSocket whose messages are the frames of a stream to the node (preview.Frame), the hub re-framing them without
// reading them. The ports that may be shown are the ones port forwarding lets the user connect to (portAllowed):
// an agent's, or one the user opened (the panel opens the server picked first).

const (
	// previewOpenWait covers starting the browser on the computer (a second or two, more on a slow disk).
	previewOpenWait = 40 * time.Second
	// previewReadLimit: what the web sends is input and short commands.
	previewReadLimit = 1 << 20
)

func (h *Hub) previewRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines/{mid}/preview/servers", h.withMachine(h.previewServers))
	api.HandleFunc("POST /api/v1/machines/{mid}/preview/start", h.withMachine(h.previewStart))
	api.HandleFunc("GET /api/v1/machines/{mid}/preview/connect", h.withMachine(h.previewConnect))
}

// previewable refuses (and says so) a machine whose node has no previews: one from before them.
func (h *Hub) previewable(w http.ResponseWriter, r *http.Request, m *Machine) bool {
	m.mu.Lock()
	info := m.info
	m.mu.Unlock()
	if slices.Contains(info.Capabilities, nodeproto.Preview) || (info.Protocol == 0 && info.Version == "") { // "": a test's or a dev node
		return true
	}
	writeErrCode(w, http.StatusConflict, proto.CodeNodeOutdated, fmt.Errorf("%s: update Persona on this computer to preview its dev servers", m.cfg.Name))
	return false
}

// previewServers is GET …/preview/servers?cwd=: the dev servers of a session's folder (its .claude/launch.json).
func (h *Hub) previewServers(w http.ResponseWriter, r *http.Request, m *Machine) {
	cwd := r.URL.Query().Get("cwd")
	if cwd == "" {
		writeErr(w, http.StatusBadRequest, errors.New("cwd is required"))
		return
	}
	if !h.previewable(w, r, m) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	var out json.RawMessage
	if err := m.call(ctx, "preview.servers", map[string]any{"cwd": cwd}, &out); err != nil {
		callErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(out)
}

// previewStart is POST …/preview/start {cwd, name, workspace, kind}: run a launch.json entry in a new terminal tab of
// the workspace. The user saw its command and confirmed it in the panel.
func (h *Hub) previewStart(w http.ResponseWriter, r *http.Request, m *Machine) {
	if principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, errors.New("this device cannot start servers"))
		return
	}
	var body struct {
		Cwd       string `json:"cwd"`
		Name      string `json:"name"`
		Workspace string `json:"workspace"`
		Kind      string `json:"kind"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&body); err != nil || body.Cwd == "" || body.Name == "" || body.Workspace == "" {
		writeErr(w, http.StatusBadRequest, errors.New("cwd, name and workspace are required"))
		return
	}
	if !h.previewable(w, r, m) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var out json.RawMessage
	if err := m.call(ctx, "preview.start", body, &out); err != nil {
		callErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(out)
}

// previewConnect is GET …/preview/connect?port=&path=&w=&h=&dpr=&mobile=: a WebSocket to a preview of the port.
// Refused before the upgrade: 403 (a device that cannot, a port not allowed), 409 node_outdated, 503 (offline), 429.
// After it, when the node cannot open the preview: an error message {t: "error", code, error} and the close (codes
// port_gone, no_browser, browser_failed, offline, failed).
func (h *Hub) previewConnect(w http.ResponseWriter, r *http.Request, m *Machine) {
	q := r.URL.Query()
	port, err := strconv.Atoi(q.Get("port"))
	if err != nil || port <= 0 || port > 65535 {
		writeErr(w, http.StatusBadRequest, errors.New("bad port"))
		return
	}
	if principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, errors.New("this device cannot open previews"))
		return
	}
	if !h.portAllowed(m, port) {
		writeErr(w, http.StatusForbidden, fmt.Errorf("port %d is not an agent's and was not opened", port))
		return
	}
	if !h.previewable(w, r, m) {
		return
	}
	mux := m.portStreams()
	if mux == nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("%s: %w", m.cfg.Name, errOffline))
		return
	}
	if mux.Len() >= maxPortStreams {
		writeErr(w, http.StatusTooManyRequests, errors.New("too many connections to this machine"))
		return
	}
	r, untrack := h.trackRelayed(r)
	defer untrack()
	if err := r.Context().Err(); err != nil {
		callErr(w, err)
		return
	}
	num := func(k string) float64 { f, _ := strconv.ParseFloat(q.Get(k), 64); return f }
	// Upgraded before the node is asked: a browser cannot read why a WebSocket was refused, and opening can fail
	// for reasons the panel has to say (no browser on that computer, the server stopped). Those come as an error
	// message, then the close.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	defer c.CloseNow()
	stopClose := context.AfterFunc(r.Context(), func() { _ = c.CloseNow() })
	defer stopClose()
	id := "v" + strconv.FormatInt(streamSeq.Add(1), 10)
	if err := mux.Open(id); err != nil {
		previewRefuse(r.Context(), c, "offline", err)
		return
	}
	octx, cancel := context.WithTimeout(r.Context(), previewOpenWait)
	var opened struct {
		Browser string `json:"browser"`
	}
	err = m.call(octx, "preview.open", map[string]any{
		"id": id, "port": port, "path": q.Get("path"), "w": int(num("w")), "h": int(num("h")), "dpr": num("dpr"), "mobile": q.Get("mobile") == "1",
	}, &opened)
	cancel()
	if err != nil {
		mux.Abort(id, err)
		code := "failed"
		var re *rpc.Error
		if errors.As(err, &re) && re.Code != "" {
			code = re.Code
		} else if errors.Is(err, errOffline) {
			code = "offline"
		}
		previewRefuse(r.Context(), c, code, err)
		return
	}
	c.SetReadLimit(previewReadLimit)
	mine, theirs := net.Pipe()
	mux.Start(id, theirs)
	ctx, done := context.WithCancel(r.Context())
	defer done()
	defer mine.Close()
	go func() { // the web's messages, framed for the node
		defer done()
		for {
			typ, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			kind := preview.Text
			if typ == websocket.MessageBinary {
				kind = preview.Binary
			}
			if preview.WriteFrame(mine, kind, b) != nil {
				return
			}
		}
	}()
	go func() { // the node's frames, as messages
		defer done()
		br := bufio.NewReaderSize(mine, 64<<10)
		for {
			kind, b, err := preview.ReadFrame(br)
			if err != nil {
				return
			}
			typ := websocket.MessageText
			if kind == preview.Binary {
				typ = websocket.MessageBinary
			}
			if c.Write(ctx, typ, b) != nil {
				return
			}
		}
	}()
	<-ctx.Done()
	if err := r.Context().Err(); err != nil {
		_ = c.CloseNow()
		mux.Abort(id, err)
		return
	}
	_ = c.Close(websocket.StatusNormalClosure, "")
}

// previewRefuse tells the web why its preview did not open, and closes.
func previewRefuse(ctx context.Context, c *websocket.Conn, code string, err error) {
	b, _ := json.Marshal(map[string]any{"t": "error", "code": code, "error": err.Error()})
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = c.Write(wctx, websocket.MessageText, b)
	_ = c.Close(websocket.StatusNormalClosure, code)
}
