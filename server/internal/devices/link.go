package devices

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// Persona Link in active mode (internal/link, docs/operations/link.md): a computer the manager cannot reach — a laptop on
// whatever network it is on — dials the hub, whose address it has; the hub checks its credential and relays the
// connection here as it is (devicewire.AcceptLink: the relay's messages are Persona Link's, its close code goes back
// to it). The manager speaks the protocol: the hello says which machine, the manager tells it the hub's addresses,
// and connects at once by asking Persona Link to open a stream (another connection the hub relays), which it joins
// to the computer's resident node; from there it is the node protocol as over ssh. The link going away takes the
// machine offline right then. The hub keeps the heartbeat with Persona Link (its side of the connection).

const kindLink = "link"

// linkOpenTimeout is how long the manager waits for Persona Link to open a stream it asked for (a setting).
const linkOpenTimeout = 15 * time.Second

// mgrLink is one machine's live link.
type mgrLink struct {
	m       *Machine
	conn    *websocket.Conn // the relay
	relayID string          // devices.accept's (devices.link_rtt names it)
	rtt     atomic.Int64    // ms: the hub's last heartbeat's round trip (devices.link_rtt)
	tokenID string          // the credential it came with
	version string          // Persona Link's build
	address string          // the hub URL it went to
	since   time.Time
	// ctx ends with the link, and every stream on it with it; its cause says why.
	ctx    context.Context
	cancel context.CancelCauseFunc

	mu      sync.Mutex
	pending map[string]chan linkStream // stream id -> the connect waiting for it
}

// linkStream is a stream Persona Link opened, or why it could not. done closes when the manager is through with it.
type linkStream struct {
	conn *websocket.Conn
	done chan struct{}
	err  error
}

// linkSet is every live link.
type linkSet struct {
	mu  sync.Mutex
	all map[*mgrLink]struct{}
}

func (s *linkSet) add(l *mgrLink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.all == nil {
		s.all = map[*mgrLink]struct{}{}
	}
	s.all[l] = struct{}{}
}

func (s *linkSet) remove(l *mgrLink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.all, l)
}

func (s *linkSet) list() []*mgrLink {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*mgrLink, 0, len(s.all))
	for l := range s.all {
		out = append(out, l)
	}
	return out
}

// sendEndpoints tells every link where the hub can be found now and what its mesh is.
func (s *linkSet) sendEndpoints(msgFor func(machine string) link.Message) {
	for _, l := range s.list() {
		msg := msgFor(l.m.cfg.ID)
		go func() { _ = l.send(msg) }()
	}
}

// closeAll ends every link with 1001: Persona Link comes back to the next manager (through the hub) quickly.
func (s *linkSet) closeAll(why string) {
	for _, l := range s.list() {
		l.close(websocket.StatusGoingAway, why)
	}
}

// acceptLink serves a Persona Link the hub relayed: its hello, then its messages until it goes away.
func (d *Devices) acceptLink(conn *websocket.Conn, a devicewire.Accept) {
	m := d.cfgMachine(a.MachineID)
	if m == nil || m.cfg.Kind != kindLink {
		_ = conn.Close(link.CloseRefused, fmt.Sprintf("hub.json has no machine %q of kind link", a.MachineID))
		return
	}
	conn.SetReadLimit(1 << 20)
	var hello link.Message
	hctx, hcancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, data, err := conn.Read(hctx)
	hcancel()
	if err == nil {
		err = json.Unmarshal(data, &hello)
	}
	if err != nil || hello.Type != "hello" || hello.Machine != m.cfg.ID {
		_ = conn.Close(link.CloseRefused, "expected the hello of machine "+m.cfg.ID)
		return
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	l := &mgrLink{m: m, conn: conn, relayID: a.ID, tokenID: a.TokenID, version: textfmt.Clip(strings.TrimSpace(hello.Version), 80),
		address: textfmt.Clip(strings.TrimSpace(hello.Address), 200), since: time.Now().UTC(), ctx: ctx, cancel: cancel,
		pending: map[string]chan linkStream{}}
	if d.stopping.Load() {
		cancel(errors.New("manager stopping"))
		_ = conn.Close(websocket.StatusGoingAway, "manager stopping")
		return
	}
	d.links.add(l)
	defer d.links.remove(l)
	if old := m.setLink(l); old != nil {
		old.close(link.CloseReplaced, "a newer link of this machine connected")
	}
	d.logf("[%s] link up: Persona Link %s via %s from %s", m.cfg.ID, l.version, l.address, a.Source)
	_ = l.send(d.linkEndpoints(m.cfg.ID))
	// The read loop is not bound to l.ctx: a link closed with a reason (replaced) still sends its close frame.
	for {
		_, data, err := conn.Read(context.Background())
		if err != nil {
			cancel(fmt.Errorf("link closed: %w", err))
			break
		}
		var msg link.Message
		if json.Unmarshal(data, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "open_failed":
			l.deliver(msg.ID, linkStream{err: fmt.Errorf("Persona Link: %s", textfmt.Clip(msg.Error, 300))})
		case "rename":
			go d.linkRename(l, msg)
		}
	}
	cause := context.Cause(ctx)
	m.dropLink(l, cause)
	d.logf("[%s] link down: %v", m.cfg.ID, cause)
}

// acceptLinkStream takes a stream Persona Link opened on the manager's asking.
func (d *Devices) acceptLinkStream(conn *websocket.Conn, a devicewire.Accept) {
	m := d.cfgMachine(a.MachineID)
	var l *mgrLink
	if m != nil {
		l = m.currentLink()
	}
	if l == nil || l.tokenID != a.TokenID || !l.wants(a.StreamID) {
		_ = conn.Close(websocket.StatusNormalClosure, "no such stream")
		return
	}
	done := make(chan struct{})
	if !l.deliver(a.StreamID, linkStream{conn: conn, done: done}) {
		_ = conn.Close(websocket.StatusNormalClosure, "stream no longer wanted")
		return
	}
	select {
	case <-done:
	case <-l.ctx.Done():
		_ = conn.Close(websocket.StatusNormalClosure, "link gone")
	}
}

func (l *mgrLink) wants(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.pending[id]
	return ok
}

// deliver hands a stream (or why there is none) to the connect waiting for it; false when none is.
func (l *mgrLink) deliver(id string, s linkStream) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	ch, ok := l.pending[id]
	if !ok {
		return false
	}
	delete(l.pending, id)
	ch <- s // buffered: one delivery per id
	return true
}

// open asks Persona Link for a stream to the resident node and waits for it.
func (l *mgrLink) open(ctx context.Context) (*websocket.Conn, chan struct{}, error) {
	id := randomHex(12)
	ch := make(chan linkStream, 1)
	l.mu.Lock()
	l.pending[id] = ch
	l.mu.Unlock()
	var err error
	if err = l.send(link.Message{Type: "open", ID: id}); err == nil {
		timer := time.NewTimer(linkOpenTimeout)
		defer timer.Stop()
		select {
		case s := <-ch:
			if s.err != nil {
				return nil, nil, s.err
			}
			return s.conn, s.done, nil
		case <-timer.C:
			err = errors.New("Persona Link did not open the stream in time")
		case <-l.ctx.Done():
			err = context.Cause(l.ctx)
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	l.mu.Lock()
	delete(l.pending, id)
	l.mu.Unlock()
	select { // delivered just now: not wanted any more
	case s := <-ch:
		if s.conn != nil {
			_ = s.conn.Close(websocket.StatusNormalClosure, "stream no longer wanted")
			close(s.done)
		}
	default:
	}
	return nil, nil, err
}

// send writes one message. Not under l.ctx: coder/websocket closes the connection when a write's context ends, and
// a link being closed with a reason (replaced) must still get its close frame out.
func (l *mgrLink) send(msg link.Message) error {
	if err := context.Cause(l.ctx); err != nil {
		return err
	}
	b, _ := json.Marshal(msg)
	ctx, cancel := context.WithTimeout(context.Background(), link.PingTimeout)
	defer cancel()
	return l.conn.Write(ctx, websocket.MessageText, b)
}

// close ends the link: its streams at once, the link itself with a close frame saying why.
func (l *mgrLink) close(code websocket.StatusCode, reason string) {
	l.cancel(errors.New(reason))
	go func() { _ = l.conn.Close(code, reason) }()
}

func (l *mgrLink) info() *proto.MachineLink {
	return &proto.MachineLink{Version: l.version, Address: l.address, Since: l.since}
}

// linkEndpoints is what machine's Persona Link is told: where the hub can be found, and what it is called and how
// many hubs its mesh has (the Mac menu bar shows both, docs/operations/link.md); the heartbeat, when hub.json sets one: Persona
// Link pings as the hub does; whether the computer still waits for the user to confirm it; and what the hub calls it.
func (d *Devices) linkEndpoints(machine string) link.Message {
	cfg := d.config()
	msg := link.Message{Type: "endpoints", HubName: cfg.HubName, MeshHubs: cfg.MeshHubs, Pending: slices.Contains(cfg.Pending, machine),
		MachineName: entryNames(cfg)[machine]}
	for _, e := range cfg.Endpoints {
		msg.Endpoints = append(msg.Endpoints, link.Endpoint{URL: e.URL, Kind: e.Kind})
	}
	if every, timeout, set := linkPing(cfg); set {
		msg.PingMS, msg.PingTimeoutMS = int(every.Milliseconds()), int(timeout.Milliseconds())
	}
	return msg
}

// linkRename asks the hub to rename the computer as its Persona Link asked (rename), and answers it renamed: the name
// the hub has now, or why not. Its endpoints carry the name too, once the hub hands the entries over again.
func (d *Devices) linkRename(l *mgrLink, msg link.Message) {
	reply := link.Message{Type: "renamed", ID: textfmt.Clip(msg.ID, 64)}
	ctx, cancel := context.WithTimeout(l.ctx, 30*time.Second)
	defer cancel()
	var out devicewire.RenameReply
	err := d.opts.Hub.Call(ctx, devicewire.MethodRename, devicewire.Rename{MachineID: l.m.cfg.ID, Name: msg.MachineName}, &out)
	var re *rpc.Error
	switch {
	case errors.As(err, &re):
		reply.Code, reply.Error = re.Code, textfmt.Clip(re.Message, 300)
	case err != nil:
		reply.Code, reply.Error = link.RenameOffline, textfmt.Clip(err.Error(), 300)
	default:
		reply.MachineName = out.Name
		d.logf("[%s] renamed by its Persona Link", l.m.cfg.ID)
	}
	_ = l.send(reply)
}

// linkRTT is devices.link_rtt: the hub measured a link's round trip.
func (d *Devices) linkRTT(p devicewire.LinkRTT) {
	d.links.mu.Lock()
	defer d.links.mu.Unlock()
	for l := range d.links.all {
		if l.relayID == p.ID {
			l.rtt.Store(int64(p.RTTMS))
		}
	}
}

// currentLink is the machine's live link; nil while there is none, or while the one there is going away.
func (m *Machine) currentLink() *mgrLink {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.link == nil || m.link.ctx.Err() != nil {
		return nil
	}
	return m.link
}

// setLink makes l the machine's link and has run connect through it now; it returns the link it replaced.
func (m *Machine) setLink(l *mgrLink) *mgrLink {
	m.mu.Lock()
	old := m.link
	m.link, m.linkDown = l, nil
	m.mu.Unlock()
	for _, ch := range []chan struct{}{m.linkUp, m.kick} {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	m.d.wakeOwnerOf(m) // the machine whose path this entry is (paths_group.go)
	m.d.changed()
	return old
}

// closeLink ends the machine's live link, if it has one, with 1001: for a machine let go or replaced (startMachines),
// whose link nothing else would end, so Persona Link dials again and finds the entry as it is now.
func (m *Machine) closeLink(why string) {
	if l := m.currentLink(); l != nil {
		l.close(websocket.StatusGoingAway, why)
	}
}

// dropLink forgets l if it is still the machine's link. A connection on it ends by itself (its stream goes with
// l.ctx); a machine that was not connected shows why the link went away.
func (m *Machine) dropLink(l *mgrLink, cause error) {
	m.mu.Lock()
	if m.link != l {
		m.mu.Unlock()
		return
	}
	m.link, m.linkDown = nil, cause
	if !m.online && cause != nil {
		m.err = cause.Error()
	}
	m.mu.Unlock()
	m.d.changed()
}

// dialLink opens a stream on the machine's link: the node's bytes, as ssh's stdout and stdin are for other machines.
func (m *Machine) dialLink(ctx context.Context) (*nodePipe, error) {
	m.mu.Lock()
	l, down := m.link, m.linkDown
	m.mu.Unlock()
	switch {
	case l == nil && down != nil:
		return nil, down
	case l == nil:
		return nil, errors.New("waiting for Persona Link")
	case l.ctx.Err() != nil:
		return nil, context.Cause(l.ctx)
	}
	conn, done, err := l.open(ctx)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(64 << 20)
	sctx, cancel := context.WithCancelCause(l.ctx)
	nc := websocket.NetConn(sctx, conn, websocket.MessageBinary)
	var once sync.Once
	return &nodePipe{r: nc, w: nc,
		lastErr: func() string {
			if c := context.Cause(sctx); c != nil && !errors.Is(c, context.Canceled) {
				return c.Error()
			}
			return ""
		},
		close: func() {
			once.Do(func() {
				cancel(nil)
				_ = nc.Close()
				close(done)
			})
		}}, nil
}

// tellLink says on the machine's link whether the manager got through to the resident node: Persona Link shows it.
func (m *Machine) tellLink(online bool, err string) {
	if m.cfg.Kind != kindLink {
		return
	}
	if l := m.currentLink(); l != nil {
		go func() { _ = l.send(link.Message{Type: "node", Online: online, Error: textfmt.Clip(err, 300)}) }()
	}
}
