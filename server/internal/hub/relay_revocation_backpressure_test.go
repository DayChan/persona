package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/streams"
)

// relayPipeTransport upgrades over an in-memory pipe so synctest can wait until the relay's pumps are blocked.
type relayPipeTransport struct {
	handler http.Handler
	ended   chan struct{}
}

func (p relayPipeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	client, server := net.Pipe()
	w := &relayPipeWriter{ResponseRecorder: httptest.NewRecorder(), conn: server, ready: make(chan struct{})}
	go func() {
		defer close(p.ended)
		p.handler.ServeHTTP(w, r)
	}()
	select {
	case <-w.ready:
		return &http.Response{StatusCode: w.Code, Header: w.Header().Clone(), Body: client}, nil
	case <-p.ended:
		_ = client.Close()
		_ = server.Close()
		return w.Result(), nil
	}
}

type relayPipeWriter struct {
	*httptest.ResponseRecorder
	conn  net.Conn
	ready chan struct{}
}

func (w *relayPipeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	close(w.ready)
	return w.conn, bufio.NewReadWriter(bufio.NewReader(w.conn), bufio.NewWriter(w.conn)), nil
}

// A full send window leaves the stream waiting for an acknowledgement rather than reading the WebSocket or pipe.
// Revocation must abort that stream itself, without waiting for the peer to resume or answer a close handshake.
func TestRevokingMachineRelayWithFullWindow(t *testing.T) {
	for _, tc := range revocableRelayPaths {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := newTestHub(t)
				acknowledged := 0
				hubMux, nodeMux, opened := revocationNode(t, h, func(event string, raw json.RawMessage) bool {
					if event != streams.EventAck {
						return true
					}
					var p struct{ N int }
					if err := json.Unmarshal(raw, &p); err != nil {
						t.Error(err)
					}
					acknowledged += p.N
					return false
				})
				api := http.NewServeMux()
				h.portRoutes(api)
				h.previewRoutes(api)
				const id = "dev_backpressure"
				ended := make(chan struct{})
				transport := relayPipeTransport{ended: ended, handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{ID: id, Role: roleFull}))
					api.ServeHTTP(w, r)
				})}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c, _, err := websocket.Dial(ctx, "ws://hub.example.com"+tc.path, &websocket.DialOptions{
					HTTPClient: &http.Client{Transport: transport},
				})
				if err != nil {
					t.Fatal(err)
				}
				defer c.CloseNow()
				backend := <-opened
				defer backend.Close()
				drained := make(chan error, 1)
				go func() { _, err := io.Copy(io.Discard, backend); drained <- err }()
				written := make(chan error, 1)
				go func() { written <- c.Write(ctx, websocket.MessageBinary, make([]byte, 1<<20)) }()
				synctest.Wait()
				// The stream window is 512 KiB. The node consumed that much, but every acknowledgement was withheld.
				if acknowledged != 512<<10 || hubMux.Len() != 1 {
					t.Fatalf("relay did not fill its window: consumed=%d streams=%d", acknowledged, hubMux.Len())
				}
				before := time.Now()
				h.revoked(id)
				synctest.Wait()
				select {
				case <-ended:
				default:
					t.Fatal("revoked relay handler is waiting for its peer")
				}
				if !time.Now().Equal(before) {
					t.Fatal("revocation waited for a close-handshake timeout")
				}
				if n := relayRegistryCount(h, id); n != 0 || hubMux.Len() != 0 || nodeMux.Len() != 0 {
					t.Fatalf("full-window revocation leaked resources: registrations=%d hub=%d node=%d", n, hubMux.Len(), nodeMux.Len())
				}
				select {
				case err := <-drained:
					if err != nil {
						t.Fatalf("backend close: %v", err)
					}
				default:
					t.Fatal("backend connection remained open")
				}
				if _, _, err := c.Read(ctx); err == nil {
					t.Fatal("revoked WebSocket remained open")
				}
				<-written
			})
		})
	}
}
