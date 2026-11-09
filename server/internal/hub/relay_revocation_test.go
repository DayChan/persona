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
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/preview"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

var revocableRelayPaths = []struct {
	name, path string
	framed     bool
}{
	{"live screen", "/api/v1/machines/m1/ports/5900/connect", false},
	{"forwarded port", "/api/v1/machines/m1/ports/5173/connect", false},
	{"preview", "/api/v1/machines/m1/preview/connect?port=5173", true},
}

// revocationNode gives each port or preview stream a separate backend pipe. Tests observe the far end closing,
// not just the browser socket, so a stranded screen stream (and its keep-awake lease) cannot pass unnoticed.
func revocationNode(t *testing.T, h *Hub, hubEvents ...func(string, json.RawMessage) bool) (*streams.Mux, *streams.Mux, <-chan net.Conn) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	hubMux, nodeMux := streams.New(hubSide.Notify), streams.New(nodeSide.Notify)
	hubSide.OnEvent = func(event string, p json.RawMessage) {
		for _, allow := range hubEvents {
			if !allow(event, p) {
				return
			}
		}
		hubMux.Event(event, p)
	}
	nodeSide.OnEvent = func(event string, p json.RawMessage) { nodeMux.Event(event, p) }
	opened := make(chan net.Conn, 16)
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "port.dial" && method != "preview.open" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if err := nodeMux.Open(p.ID); err != nil {
			return nil, err
		}
		mine, theirs := net.Pipe()
		nodeMux.Start(p.ID, theirs)
		opened <- mine
		return map[string]any{}, nil
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _ = hubSide.Serve(ctx, n2hR) }()
	go func() { defer wg.Done(); _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() {
		cancel()
		_ = h2nW.Close()
		_ = n2hW.Close()
		hubMux.CloseAll()
		nodeMux.CloseAll()
		wg.Wait()
		close(opened)
		for c := range opened {
			_ = c.Close()
		}
	})
	m := newMachine(h, MachineConfig{ID: "m1", Name: "Test computer"})
	m.peer, m.online, m.streams = hubSide, true, hubMux
	m.sessions = []proto.Session{{SessionID: "s1", Ports: []proto.Port{{Port: 5173}}}}
	h.byID[m.cfg.ID] = m
	h.ext.report.Store(&extwire.Report{Machines: map[string]extwire.MachineExtras{"m1": {ScreenPort: true}}, ScreenKeepAwake: true})
	return hubMux, nodeMux, opened
}

func relayRegistryCount(h *Hub, id string) int {
	h.ext.calls.mu.Lock()
	defer h.ext.calls.mu.Unlock()
	return len(h.ext.calls.byID[id])
}

func relayRoundTrip(t *testing.T, c *websocket.Conn, backend net.Conn, framed bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = backend.SetDeadline(time.Now().Add(5 * time.Second))
	defer backend.SetDeadline(time.Time{})
	const input, output = "client input", "server frame"
	if err := c.Write(ctx, websocket.MessageBinary, []byte(input)); err != nil {
		t.Fatal(err)
	}
	var b []byte
	var err error
	if framed {
		var kind byte
		kind, b, err = preview.ReadFrame(bufio.NewReader(backend))
		if kind != preview.Binary {
			t.Fatalf("input frame kind = %v", kind)
		}
	} else {
		b = make([]byte, len(input))
		_, err = io.ReadFull(backend, b)
	}
	if err != nil || string(b) != input {
		t.Fatalf("backend input = %q, %v", b, err)
	}
	if framed {
		err = preview.WriteFrame(backend, preview.Binary, []byte(output))
	} else {
		_, err = backend.Write([]byte(output))
	}
	if err != nil {
		t.Fatal(err)
	}
	if typ, b, err := c.Read(ctx); err != nil || typ != websocket.MessageBinary || string(b) != output {
		t.Fatalf("browser frame = %v %q, %v", typ, b, err)
	}
}

func relayBackendClosed(t *testing.T, backend net.Conn) {
	t.Helper()
	_ = backend.SetReadDeadline(time.Now().Add(5 * time.Second))
	var b [1]byte
	if n, err := backend.Read(b[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("backend must close without more input: n=%d, err=%v", n, err)
	}
}

func TestRevokingEndsMachineRelays(t *testing.T) {
	for _, tc := range revocableRelayPaths {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
			hubMux, nodeMux, opened := revocationNode(t, h)
			ended := make(chan struct{}, 8)
			routes := h.routes()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				routes.ServeHTTP(w, r)
				if r.URL.Query().Has("relay_test") {
					ended <- struct{}{}
				}
			}))
			t.Cleanup(srv.Close)
			goneID, gone := relayDevice(t, srv, h.cfg.Token, "Revoked device", "phone")
			liveID, live := relayDevice(t, srv, h.cfg.Token, "Other device", "phone")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			type connection struct {
				ws      *websocket.Conn
				backend net.Conn
			}
			open := func(token string) connection {
				t.Helper()
				u, _ := url.Parse(wsURL(srv, tc.path))
				q := u.Query()
				q.Set("token", token) // Browser screen and preview clients send credentials in the query.
				q.Set("relay_test", "1")
				u.RawQuery = q.Encode()
				c, _, err := websocket.Dial(ctx, u.String(), nil)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = c.CloseNow() })
				select {
				case backend := <-opened:
					t.Cleanup(func() { _ = backend.Close() })
					relayRoundTrip(t, c, backend, tc.framed)
					return connection{c, backend}
				case <-ctx.Done():
					t.Fatal("node never opened the relay")
					return connection{}
				}
			}
			// More than one connection per credential: revocation must cancel every registered call.
			goneConns := []connection{open(gone), open(gone)}
			liveConn := open(live)
			if code, out := call(t, srv.Config.Handler, "DELETE", "/api/v1/auth/tokens/"+goneID, h.cfg.Token, ""); code != http.StatusOK {
				t.Fatalf("revoke: %d %v", code, out)
			}
			for _, c := range goneConns {
				readCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				_, _, err := c.ws.Read(readCtx)
				stillOpen := readCtx.Err() != nil
				stop()
				if err == nil || stillOpen {
					t.Fatalf("revoked credential's WebSocket remained open: %v", err)
				}
				relayBackendClosed(t, c.backend)
			}
			if _, ok := h.authenticate(ctx, gone); ok {
				t.Fatal("revoked credential still authenticates")
			}
			relayRoundTrip(t, liveConn.ws, liveConn.backend, tc.framed)
			if n := relayRegistryCount(h, goneID); n != 0 {
				t.Fatalf("revoked credential retained %d registrations", n)
			}
			if n := relayRegistryCount(h, liveID); n != 1 {
				t.Fatalf("other credential has %d registrations, want 1", n)
			}
			if hubMux.Len() != 1 || nodeMux.Len() != 1 {
				t.Fatalf("only the other credential's stream should remain: hub=%d node=%d", hubMux.Len(), nodeMux.Len())
			}
			_ = liveConn.ws.CloseNow()
			relayBackendClosed(t, liveConn.backend)
			for range 3 {
				select {
				case <-ended:
				case <-ctx.Done():
					t.Fatal("relay handler did not return")
				}
			}
			if n := relayRegistryCount(h, liveID); n != 0 {
				t.Fatalf("normally closed connection retained %d registrations", n)
			}
			if hubMux.Len() != 0 || nodeMux.Len() != 0 {
				t.Fatalf("closed connections retained streams: hub=%d node=%d", hubMux.Len(), nodeMux.Len())
			}
		})
	}
}

func TestMachineRelayRevokedAfterAuthentication(t *testing.T) {
	for _, tc := range revocableRelayPaths {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{}, rev: map[string]bool{}}
			hubMux, nodeMux, _ := revocationNode(t, h)
			token := "jd_" + randomHex(24)
			const id = "dev_racing"
			if err := h.store.CreateDeviceToken(context.Background(), db.DeviceToken{ID: id, Role: roleFull}, hashToken(token)); err != nil {
				t.Fatal(err)
			}
			api := http.NewServeMux()
			h.portRoutes(api)
			h.previewRoutes(api)
			authed, release, ended := make(chan struct{}), make(chan struct{}), make(chan struct{})
			srv := httptest.NewServer(h.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(ended)
				close(authed)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				api.ServeHTTP(w, r)
			})))
			t.Cleanup(srv.Close)
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				c, _, err := websocket.Dial(ctx, wsURL(srv, tc.path), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
				if err == nil {
					defer c.CloseNow()
					_, _, err = c.Read(ctx)
				}
				result <- err
			}()
			select {
			case <-authed:
			case <-ctx.Done():
				t.Fatal("request never passed authentication")
			}
			h.revoked(id)
			unblock()
			select {
			case err := <-result:
				if err == nil || ctx.Err() != nil {
					t.Fatalf("request authenticated before revocation remained open: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("request authenticated before revocation remained open")
			}
			select {
			case <-ended:
			case <-ctx.Done():
				t.Fatal("revoked relay handler did not return")
			}
			if n := relayRegistryCount(h, id); n != 0 || hubMux.Len() != 0 || nodeMux.Len() != 0 {
				t.Fatalf("revocation race leaked resources: registrations=%d hub=%d node=%d", n, hubMux.Len(), nodeMux.Len())
			}
		})
	}
}
