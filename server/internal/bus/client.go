package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/rpc"
)

// ErrOffline: the client has no connection to the hub that said its handshake right now.
var ErrOffline = errors.New("not connected to the hub")

// stableAfter: a connection that stayed up this long starts the backoff over when it drops.
const stableAfter = time.Minute

// Client keeps a service's connection to the hub's bus up until Run's context ends: dial with the token, say the
// handshake, serve the hub's calls and events until the connection drops, dial again. Between tries it waits
// Backoff, doubling up to MaxBackoff; a rejected token (ErrRejected) waits MaxBackoff every time.
type Client struct {
	Hub        string       // the hub's base URL, http(s)://host:port
	Token      string       // the service's bus token
	HTTPClient *http.Client // optional: a TLS client pinned to the hub's key for its public port

	// HelloMethod is the handshake (manager.hello for a manager's token) and HelloParams what it says, asked afresh
	// on every connection. OnHello gets the hub's answer before the connection counts as up; an error from it drops
	// the connection.
	HelloMethod string
	HelloParams func() any
	OnHello     func(result json.RawMessage) error

	// OnRequest answers the hub's calls, OnEvent takes its one-way events (nil: calls fail, events are dropped).
	OnRequest rpc.Handler
	OnEvent   rpc.EventHandler
	// Cancels are the methods whose calls tell the hub when their caller stops waiting (rpc.Peer.Cancels).
	Cancels map[string]bool

	Logf func(format string, args ...any)
	// Zero values are the protocol's: 1 s doubling to a minute; a ping every 30 s, 20 s for its pong.
	Backoff, MaxBackoff    time.Duration
	PingEvery, PingTimeout time.Duration
	// StopWhenRevoked: Run returns ErrRevoked once the hub refuses the token itself, instead of trying it again every
	// MaxBackoff (a manager whose token only a new install code replaces).
	StopWhenRevoked bool
	// StopWhenRejected: with StopWhenRevoked, any refusal of the token (ErrRejected too: a hub from before revoked
	// tokens were told apart answers a plain 401) is taken for a revoke (the built-in manager's own service, which
	// must not run next to the child such a hub starts).
	StopWhenRejected bool

	mu   sync.Mutex
	peer *rpc.Peer     // the connection that said its handshake; nil between connections
	up   chan struct{} // closed while peer is set
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

func (c *Client) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// Run keeps the connection up until ctx ends, and returns ctx's error (or ErrRevoked, with StopWhenRevoked).
func (c *Client) Run(ctx context.Context) error {
	if c.HelloMethod == "" {
		return errors.New("bus: the client has no HelloMethod")
	}
	first, most := orDefault(c.Backoff, time.Second), orDefault(c.MaxBackoff, time.Minute)
	delay := first
	for {
		began := time.Now()
		helloed, err := c.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if helloed && time.Since(began) >= stableAfter {
			delay = first
		}
		if c.StopWhenRevoked && errors.Is(err, ErrRevoked) {
			c.logf("bus: the hub refused this token: %v; not trying again", err)
			return err
		}
		if c.StopWhenRevoked && c.StopWhenRejected && errors.Is(err, ErrRejected) {
			c.logf("bus: the hub refused this token: %v; not trying again", err)
			return fmt.Errorf("%w (%v)", ErrRevoked, err)
		}
		wait := delay
		if errors.Is(err, ErrRejected) {
			wait = most
		} else {
			delay = min(delay*2, most)
		}
		c.logf("bus: the connection to the hub ended: %v; again in %s", err, wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// session is one connection: dial, handshake, serve until it drops. helloed: the handshake went through.
func (c *Client) session(ctx context.Context) (helloed bool, err error) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ws, err := Dial(sctx, c.Hub, c.Token, c.HTTPClient)
	if err != nil {
		return false, err
	}
	defer ws.CloseNow()
	conn := NewConn(sctx, ws)
	go func() {
		err := Keepalive(sctx, ws, orDefault(c.PingEvery, PingEvery), orDefault(c.PingTimeout, PingTimeout))
		if sctx.Err() == nil {
			c.logf("bus: %v: hanging up", err)
			_ = ws.CloseNow()
		}
	}()
	p := rpc.New("s", conn)
	p.OnRequest, p.OnEvent, p.Cancels = c.OnRequest, c.OnEvent, c.Cancels
	served := make(chan error, 1)
	go func() { served <- p.Serve(sctx, conn) }()
	// Whatever closes the peer ends the session: a closed peer still reading the
	// websocket would run the hub's calls without being able to answer them.
	go func() {
		select {
		case <-p.Done():
			_ = ws.CloseNow()
		case <-sctx.Done():
		}
	}()

	var params any = map[string]any{}
	if c.HelloParams != nil {
		params = c.HelloParams()
	}
	var res json.RawMessage
	hctx, hcancel := context.WithTimeout(sctx, HelloTimeout)
	err = p.Call(hctx, c.HelloMethod, params, &res)
	hcancel()
	if err != nil {
		var coded *rpc.Error
		if errors.As(err, &coded) && coded.Code == CodeWrongIdentity {
			err = fmt.Errorf("%w: %s", ErrRejected, coded.Message)
		}
		return false, fmt.Errorf("%s: %w", c.HelloMethod, err)
	}
	if c.OnHello != nil {
		if err := c.OnHello(res); err != nil {
			return false, err
		}
	}
	c.setPeer(p)
	defer c.setPeer(nil)
	return true, closeReason(<-served)
}

// closeReason says why the hub closed the connection: a revoked token or a refused handshake is ErrRejected.
func closeReason(err error) error {
	var ce websocket.CloseError
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Code {
	case CloseRevoked:
		return fmt.Errorf("%w: %s", ErrRevoked, ce.Reason)
	case CloseRefused:
		return fmt.Errorf("%w: %s", ErrRejected, ce.Reason)
	case CloseReplaced:
		return fmt.Errorf("replaced by a newer connection: %s", ce.Reason)
	}
	return err
}

func (c *Client) setPeer(p *rpc.Peer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.up == nil {
		c.up = make(chan struct{})
	}
	switch {
	case p != nil && c.peer == nil:
		close(c.up)
	case p == nil && c.peer != nil:
		c.up = make(chan struct{})
	}
	c.peer = p
}

// Peer is the connection that said its handshake, nil while there is none.
func (c *Client) Peer() *rpc.Peer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peer
}

// Wait returns once the client is connected and its handshake went through, or when ctx ends.
func (c *Client) Wait(ctx context.Context) error {
	c.mu.Lock()
	if c.up == nil {
		c.up = make(chan struct{})
	}
	up := c.up
	c.mu.Unlock()
	select {
	case <-up:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Call calls a hub method on the current connection (ErrOffline while there is none).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	p := c.Peer()
	if p == nil {
		return ErrOffline
	}
	return p.Call(ctx, method, params, out)
}

// Notify sends the hub a one-way event on the current connection (ErrOffline while there is none).
func (c *Client) Notify(event string, params any) error {
	p := c.Peer()
	if p == nil {
		return ErrOffline
	}
	return p.Notify(event, params)
}
