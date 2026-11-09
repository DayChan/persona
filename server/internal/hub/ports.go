package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

// Forwarding a machine's port to the apps (docs/operations/ports.md "Forwarding"): the app listens on the phone's loopback and opens
// one WebSocket here for each TCP connection the in-app browser makes; the hub carries its bytes as a stream on the
// connection it already has to the node (streams.Mux), and the node connects to the port on its own addresses. Bytes
// pass untouched, so the page, its WebSocket (HMR), redirects and cookies work as they do on the machine itself.
//
// Only ports one of the machine's agents listens on (Session.Ports) or that the user opened in the app (POST …/open,
// remembered for portOpenedFor) can be connected to, with a full device token: this is not a way into anything
// else on the network, nor a way to reach other ports on the machine.

const (
	portOpenedFor = 24 * time.Hour
	// maxPortStreams per machine: a page opens a handful of connections; far more is something else.
	maxPortStreams = 64
)

type portOpens struct {
	mu sync.Mutex
	m  map[string]time.Time // "mid:port" -> when the user opened it
}

func (o *portOpens) note(mid string, port int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.m == nil {
		o.m = map[string]time.Time{}
	}
	for k, at := range o.m {
		if time.Since(at) > portOpenedFor {
			delete(o.m, k)
		}
	}
	o.m[mid+":"+strconv.Itoa(port)] = time.Now()
}

func (o *portOpens) has(mid string, port int) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	at, ok := o.m[mid+":"+strconv.Itoa(port)]
	return ok && time.Since(at) < portOpenedFor
}

var streamSeq atomic.Int64

func (h *Hub) portRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines/{mid}/ports/{port}", h.withMachine(h.probePort))
	api.HandleFunc("POST /api/v1/machines/{mid}/ports/{port}/open", h.withMachine(h.openPort))
	api.HandleFunc("GET /api/v1/machines/{mid}/ports/{port}/connect", h.withMachine(h.connectPort))
}

func pathPort(r *http.Request) (int, error) {
	p, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || p <= 0 || p > 65535 {
		return 0, errors.New("bad port")
	}
	return p, nil
}

type portProbe struct {
	Port      int    `json:"port"`
	Listening bool   `json:"listening"`
	HTTP      bool   `json:"http"`
	Title     string `json:"title,omitempty"`
}

func (m *Machine) probe(ctx context.Context, port int) (portProbe, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var res portProbe
	err := m.call(ctx, "port.probe", map[string]any{"port": port}, &res)
	return res, err
}

// probePort is GET …/ports/{port}: whether something listens there, and its page's title. Nothing is opened.
func (h *Hub) probePort(w http.ResponseWriter, r *http.Request, m *Machine) {
	port, err := pathPort(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := m.probe(r.Context(), port)
	if err != nil {
		callErr(w, err)
		return
	}
	writeJSON(w, res)
}

// openPort is POST …/ports/{port}/open: the user tapped a link to a port no agent is known to listen on (a server
// they started themselves). If something answers there, it may be connected to from now on.
func (h *Hub) openPort(w http.ResponseWriter, r *http.Request, m *Machine) {
	port, err := pathPort(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	res, err := m.probe(r.Context(), port)
	if err != nil {
		callErr(w, err)
		return
	}
	if !res.Listening {
		writeErr(w, http.StatusNotFound, fmt.Errorf("nothing is listening on port %d", port))
		return
	}
	h.ports.note(m.cfg.ID, port)
	writeJSON(w, res)
}

// screenPort is macOS's Screen Sharing (VNC): the live screen of a Mac the plugin cua is on for
// (docs/extensions/computer-use.md "Live view").
const screenPort = 5900

// portAllowed: an agent on the machine listens on port, or the user opened it, or it is the screen of a Mac the
// plugin cua or ego is on for (ego's first-run setup is done over it, #1507).
func (h *Hub) portAllowed(m *Machine, port int) bool {
	if port == screenPort && h.screenAllowed(m) { // the manager's report: cua or ego is on for it
		return true
	}
	m.mu.Lock()
	for _, s := range m.sessions {
		for _, p := range s.Ports {
			if p.Port == port {
				m.mu.Unlock()
				return true
			}
		}
	}
	m.mu.Unlock()
	return h.ports.has(m.cfg.ID, port)
}

// connectPort is GET …/ports/{port}/connect: a WebSocket whose binary messages are one TCP connection's bytes to
// the port on the machine. It is refused before the upgrade (403 not allowed, 404 nothing listening).
func (h *Hub) connectPort(w http.ResponseWriter, r *http.Request, m *Machine) {
	port, err := pathPort(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if principalOf(r.Context()).Role != roleFull {
		writeErr(w, http.StatusForbidden, errors.New("this device cannot open ports"))
		return
	}
	if !h.portAllowed(m, port) {
		writeErr(w, http.StatusForbidden, fmt.Errorf("port %d is not an agent's and was not opened", port))
		return
	}
	mux := m.portStreams()
	if mux == nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("%s: %w", m.name(), errOffline))
		return
	}
	if mux.Len() >= maxPortStreams {
		writeErr(w, http.StatusTooManyRequests, errors.New("too many connections to this machine's ports"))
		return
	}
	r, untrack := h.trackRelayed(r)
	defer untrack()
	if err := r.Context().Err(); err != nil {
		callErr(w, err)
		return
	}
	id := "p" + strconv.FormatInt(streamSeq.Add(1), 10)
	if err := mux.Open(id); err != nil {
		writeErr(w, http.StatusServiceUnavailable, err)
		return
	}
	dctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	dial := map[string]any{"id": id, "port": port}
	if port == screenPort && h.extReport().ScreenKeepAwake {
		dial["keep_awake"] = true // someone is watching the Mac: it stays awake until this closes (node/awake.go)
	}
	err = m.call(dctx, "port.dial", dial, nil)
	cancel()
	if err != nil {
		mux.Abort(id, err)
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == "port_gone" {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		callErr(w, err)
		return
	}
	// noVNC may ask for the "binary" subprotocol (its frames are binary either way).
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, Subprotocols: []string{"binary"}})
	if err != nil {
		mux.Abort(id, err)
		return
	}
	c.SetReadLimit(1 << 20)
	// The handler holds the request (its context is the connection's) until the stream ends.
	ctx, done := context.WithCancel(r.Context())
	defer done()
	conn := &endConn{Conn: websocket.NetConn(ctx, c, websocket.MessageBinary), end: done}
	defer conn.Close()
	mux.Start(id, conn)
	<-ctx.Done()
	if err := r.Context().Err(); err != nil {
		_ = c.CloseNow()
		// A stream waiting for acknowledgements cannot notice the closed connection itself.
		mux.Abort(id, err)
	}
}

// endConn calls end when it is closed (the stream finished with it).
type endConn struct {
	net.Conn
	end  func()
	once sync.Once
}

func (c *endConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.end)
	return err
}

// portStreams is the stream Mux of the machine's connection now; nil while it is offline.
func (m *Machine) portStreams() *streams.Mux {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.online {
		return nil
	}
	return m.streams
}
