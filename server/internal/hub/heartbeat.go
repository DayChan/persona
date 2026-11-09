package hub

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// wsUpgrade captures the transport without depending on websocket's private connection fields.
type wsUpgrade struct {
	http.ResponseWriter
	conn net.Conn
}

func (w *wsUpgrade) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	w.conn = conn
	return conn, rw, err
}

func setHeartbeatKeepAlive(conn net.Conn, hb heartbeatConfig) error {
	if !hb.negotiated {
		return nil // Keep the transport defaults for legacy clients.
	}
	if wrapped, ok := conn.(interface{ NetConn() net.Conn }); ok {
		conn = wrapped.NetConn() // TLS exposes its underlying transport.
	}
	if tcp, ok := conn.(interface {
		SetKeepAliveConfig(net.KeepAliveConfig) error
	}); ok {
		return tcp.SetKeepAliveConfig(net.KeepAliveConfig{
			Enable: true, Idle: time.Duration(hb.Interval) * time.Second,
			Interval: time.Duration(hb.PongTimeout) * time.Second, Count: 3,
		})
	}
	return nil
}

type wsWriter interface {
	Write(context.Context, websocket.MessageType, []byte) error
	Ping(context.Context) error
}

// writeWS keeps a pending protocol ping from blocking application replies or heartbeat changes.
func (c *client) writeWS(ctx context.Context, conn wsWriter, keepAlive func(heartbeatConfig)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	hb := defaultHeartbeat()
	ping := time.NewTicker(time.Duration(hb.Interval) * time.Second)
	defer ping.Stop()
	pong := time.NewTimer(time.Hour)
	pong.Stop()
	defer pong.Stop()
	var pongDeadline <-chan time.Time
	var pingStarted time.Time
	result := make(chan error, 1)
	c.mu.Lock()
	updates := c.meta.updates
	c.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case hb = <-updates:
			ping.Reset(time.Duration(hb.Interval) * time.Second)
			if pongDeadline != nil {
				pong.Reset(max(0, time.Duration(hb.PongTimeout)*time.Second-time.Since(pingStarted)))
			}
			keepAlive(hb)
			c.helloAck(hb)
		case b := <-c.out:
			if c.dropped.Load() {
				// queue lost a message and dropSlow is closing the client: writing what was queued after it would move
				// the client's event_seq (or since_seq, after_seq) past the loss, so a resume would skip it. Keep
				// draining instead of returning, so the 1008 "too slow" close still goes out.
				continue
			}
			wctx, wcancel := context.WithTimeout(ctx, 20*time.Second)
			err := conn.Write(wctx, websocket.MessageText, b)
			wcancel()
			if err != nil {
				return err
			}
			c.recordSocketTraffic("websocket_frame", "written", int64(len(b)), 0)
		case <-ping.C:
			if pongDeadline != nil {
				continue // Only one protocol ping may be outstanding.
			}
			pingStarted = time.Now()
			c.recordSocketTraffic("websocket_ping", "started", 0, 0)
			pong.Reset(time.Duration(hb.PongTimeout) * time.Second)
			pongDeadline = pong.C
			go func() { result <- conn.Ping(ctx) }()
		case err := <-result:
			if err != nil {
				return err
			}
			pong.Stop()
			c.recordSocketTraffic("websocket_pong", "received", 0, time.Since(pingStarted))
			pongDeadline = nil
		case <-pongDeadline:
			return errDeviceSessionPongTimeout
		}
	}
}
