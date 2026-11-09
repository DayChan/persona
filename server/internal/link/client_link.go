package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// link is one live link to the hub.
type link struct {
	conn    *websocket.Conn
	ep      Endpoint
	local   netip.Addr   // this side's address of the link's TCP connection
	streams *http.Client // checked the same way (pin or system CAs); streams go where the link went
	token   string
	// named: the hub said what it calls this computer (endpoints), so it renames computers (rename.go).
	named atomic.Bool
}

func dial(ctx context.Context, c *Config) (*link, error) {
	l, ep, err := race(ctx, c.Endpoints, func(ctx context.Context, ep Endpoint) (*link, error) {
		var local netip.Addr
		client := hubClient(c, func(raw net.Conn) {
			if a, err := netip.ParseAddrPort(raw.LocalAddr().String()); err == nil {
				local = a.Addr().Unmap()
			}
		})
		conn, resp, err := websocket.Dial(ctx, wsURL(ep.URL, Path), &websocket.DialOptions{HTTPClient: client, HTTPHeader: bearer(c.Token)})
		if err != nil {
			return nil, handshakeError(resp, err)
		}
		conn.SetReadLimit(1 << 20)
		return &link{conn: conn, local: local, streams: hubClient(c, nil), token: c.Token}, nil
	}, func(l *link) { l.conn.CloseNow() })
	if err != nil {
		return nil, err
	}
	l.ep = ep
	return l, nil
}

var streamID = lazyre.New(`^[A-Za-z0-9_-]{1,64}$`)

// serve runs the link until it ends and says why.
func (l *link) serve(ctx context.Context, c *Config, o Options, rep *reporter) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	defer l.conn.CloseNow()
	defer l.streams.CloseIdleConnections()
	hello := Message{Type: "hello", Machine: c.Machine, Version: o.Version, Address: l.ep.URL, Features: []string{FeatureAttention}}
	if err := l.send(ctx, hello); err != nil {
		return err
	}
	since := time.Now()
	rep.set(func(s *Status) {
		*s = Status{State: "connected", Machine: c.Machine, HubName: c.HubName, MachineName: c.MachineName, Address: l.ep.URL,
			Kind: l.ep.Kind, Since: &since, Node: "unknown", Trust: trustOf(c), Endpoints: ranked(c.Endpoints)}
	})
	go l.renames(ctx, o, rep)
	o.Logf("link: connected to %s (%s)", l.ep.URL, l.ep.Kind)
	drop := func(err error) {
		cancel(err)
		l.conn.CloseNow()
	}
	// The heartbeat is o's until the hub says another (endpoints); beat hands the pinger the latest, the link stays.
	beat := make(chan heartbeat, 1)
	go func() {
		hb := heartbeat{every: o.PingEvery, timeout: o.PingTimeout}
		t := time.NewTicker(hb.every)
		defer t.Stop()
		// A ping waits for its pong on its own, so a reconnect or a network change is not held up behind one that is
		// out (with a slow link's heartbeat, for up to the hub's timeout). One of each kind at a time; the first that
		// fails drops the link.
		type pong struct {
			check bool
			err   error
		}
		pongs := make(chan pong)
		var beating, checking bool
		ping := func(check bool, d time.Duration) {
			go func() {
				err := l.ping(ctx, d)
				select {
				case pongs <- pong{check, err}:
				case <-ctx.Done():
				}
			}()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case p := <-pongs:
				why := "hub 没有回应心跳"
				if p.check {
					checking, why = false, "网络变了以后 hub 没有回应"
				} else {
					beating = false
				}
				if p.err != nil && ctx.Err() == nil {
					drop(fmt.Errorf("%s：%w", why, p.err))
					return
				}
			case b := <-beat:
				if b == hb {
					continue
				}
				if b.every != hb.every {
					t.Reset(b.every)
				}
				hb = b
				o.Logf("link: heartbeat every %s, %s for a pong", hb.every, hb.timeout)
			case <-t.C:
				if !beating {
					beating = true
					ping(false, hb.timeout)
				}
			case <-o.Check:
				if l.localGone() {
					drop(errors.New("网络变了，原来的地址已经不在"))
					return
				}
				if !checking {
					checking = true
					ping(true, hb.check())
				}
			case <-o.Redial:
				cancel(errRedial)
				_ = l.conn.Close(websocket.StatusNormalClosure, "reconnect")
				return
			}
		}
	}()
	for {
		_, data, err := l.conn.Read(ctx)
		if err != nil {
			if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
				return cause
			}
			return err
		}
		var m Message
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type {
		case "endpoints":
			l.endpoints(m, c, o, rep)
			select { // only the latest counts
			case <-beat:
			default:
			}
			beat <- o.heartbeatOf(m)
		case "renamed":
			l.renamed(m, c, o, rep)
		case "attention":
			if m.Attention != nil { // the status is reset on every connect: a summary never outlives its link
				rep.set(func(s *Status) { s.Attention = m.Attention })
			}
		case "node":
			rep.set(func(s *Status) {
				s.Node, s.NodeError = "offline", m.Error
				if m.Online {
					s.Node, s.NodeError = "online", ""
				}
			})
		case "open":
			if !streamID.MatchString(m.ID) {
				continue
			}
			go func(id string) {
				if err := l.forward(ctx, id, o.Target); err != nil {
					o.Logf("link: stream %s: %v", id, err)
					_ = l.send(ctx, Message{Type: "open_failed", ID: id, Error: err.Error()})
					rep.set(func(s *Status) { s.Node, s.NodeError = "offline", err.Error() })
				}
			}(m.ID)
		}
	}
}

// endpoints takes in what the hub says about where it is and who it is: link.json keeps the candidates (the ones added
// here stay), the hub's name and what it calls this computer; the mesh size is only shown while connected.
func (l *link) endpoints(m Message, c *Config, o Options, rep *reporter) {
	name, machine := clipName(m.HubName), clipName(m.MachineName)
	if machine != "" {
		l.named.Store(true)
	}
	if len(m.Endpoints) > 0 || (name != "" && name != c.HubName) || (machine != "" && machine != c.MachineName) {
		var eps []Endpoint
		err := Update(o.Path, func(f *Config) error {
			if f.Token != c.Token {
				return errPairedAgain
			}
			if name != "" {
				f.HubName = name
			}
			if machine != "" {
				f.MachineName = machine
			}
			if len(m.Endpoints) > 0 {
				var manual []Endpoint
				for _, e := range f.Endpoints {
					if e.Manual {
						manual = append(manual, e)
					}
				}
				hub := make([]Endpoint, 0, len(m.Endpoints))
				for _, e := range m.Endpoints {
					if strings.HasPrefix(e.URL, "https://") {
						hub = append(hub, Endpoint{URL: e.URL, Kind: e.Kind})
					}
				}
				f.Endpoints = append(manual, hub...)
			}
			eps = f.Endpoints
			return nil
		})
		if err != nil {
			return
		}
		c.Endpoints = eps
		if name != "" {
			c.HubName = name
		}
		if machine != "" {
			c.MachineName = machine
		}
	}
	rep.set(func(s *Status) {
		s.Endpoints = ranked(c.Endpoints)
		s.HubName = c.HubName
		s.MachineName = c.MachineName
		if m.MeshHubs > 0 {
			s.MeshHubs = min(m.MeshHubs, 999)
		}
		s.Pending = m.Pending
	})
}

// heartbeat is how a link is pinged: every, and how long a pong may take; hub: the hub said so (endpoints).
type heartbeat struct {
	every, timeout time.Duration
	hub            bool
}

// heartbeatOf is the ping interval and timeout the hub asks for in m (endpoints: hub.json link), brought into range;
// o's when it asks for none or for nothing usable (older hubs, hubs that leave it at the default).
func (o Options) heartbeatOf(m Message) heartbeat {
	if every, timeout, ok := PingTimesMS(int64(m.PingMS), int64(m.PingTimeoutMS)); ok {
		return heartbeat{every, timeout, true}
	}
	return heartbeat{o.PingEvery, o.PingTimeout, false}
}

// check is how long the ping after a network change waits: checkPing, to notice a dead network quickly, but not less
// than the hub's timeout when it set one (on its slow link a pong may take that long, network change or not).
func (h heartbeat) check() time.Duration {
	if h.hub {
		return max(checkPing, h.timeout)
	}
	return checkPing
}

// clipName keeps a hub's name to one short line of printable text: it is shown in a menu and a terminal.
func clipName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80])
	}
	return s
}

func (l *link) send(ctx context.Context, m Message) error {
	b, _ := json.Marshal(m)
	ctx, cancel := context.WithTimeout(ctx, PingTimeout)
	defer cancel()
	return l.conn.Write(ctx, websocket.MessageText, b)
}

func (l *link) ping(ctx context.Context, d time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return l.conn.Ping(ctx)
}

// localGone: the address the link's TCP connection left from is no longer on this computer (another network).
func (l *link) localGone() bool {
	if !l.local.IsValid() {
		return false
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Unmap().WithZone("") == l.local.WithZone("") {
			return false
		}
	}
	return true
}

// forward joins stream id to the resident node: nothing else, and nowhere else (docs/operations/link.md "Forwarding"). It returns an
// error only when the stream could not be opened; once bytes flow, its end is the node's or the hub's business.
func (l *link) forward(ctx context.Context, id, target string) error {
	node, err := net.DialTimeout("unix", target, 2*time.Second)
	if err != nil {
		return nodeMissing(runtime.GOOS, target)
	}
	defer node.Close()
	dctx, dcancel := context.WithTimeout(ctx, dialTimeout) // the handshake only: the stream lives as long as the link
	defer dcancel()
	ws, resp, err := websocket.Dial(dctx, wsURL(l.ep.URL, StreamPath+"?id="+id), &websocket.DialOptions{
		HTTPClient: l.streams, HTTPHeader: bearer(l.token), CompressionMode: websocket.CompressionContextTakeover})
	if err != nil {
		return fmt.Errorf("开不了流：%w", handshakeError(resp, err))
	}
	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	defer nc.Close()
	// hub -> node: when the hub ends the stream, half-close so the node sees EOF and finishes. node -> hub decides
	// when the stream is over, as node.Relay does: everything the node wrote must reach the hub.
	go func() {
		_, _ = io.Copy(node, nc)
		if u, ok := node.(*net.UnixConn); ok {
			_ = u.CloseWrite()
		}
	}()
	_, _ = io.Copy(nc, node)
	return nil
}
