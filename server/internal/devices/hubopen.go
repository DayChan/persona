package devices

import (
	"context"
	"encoding/json"
	"time"
)

// What the hub opens on a node through the manager — a stream (a forwarded port's: port.dial, a plugin program's:
// plugin.node.dial), a terminal it watches (terminal.watch) — is the hub's as long as the bus connection it came on lasts: the hub ends its side when a new
// connection says hello (hub/devices_reports.go devicesConnected), and a hub that restarted has none of it. The manager
// ends the node's side then (Hello), as the node itself would when its own connection to the hub ended; else a
// terminal would go on streaming and a port's connection stay open on the computer with nobody at the other end.

// hubOpened is what the hub has open on the machine's node now; m.mu guards it.
type hubOpened struct {
	streams map[string]bool // streams by id
	terms   map[string]bool // watched terminals by session id
}

// noteOpened keeps what a call of the hub's opened or closed on the node.
func (m *Machine) noteOpened(method string, params json.RawMessage, err error) {
	var p struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
	}
	switch method {
	case "port.dial", "plugin.node.dial", "terminal.watch", "terminal.unwatch":
	default:
		return
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	o := &m.opened
	switch {
	case (method == "port.dial" || method == "plugin.node.dial") && err == nil && p.ID != "":
		if o.streams == nil {
			o.streams = map[string]bool{}
		}
		o.streams[p.ID] = true
	case method == "terminal.watch" && err == nil && p.SessionID != "":
		if o.terms == nil {
			o.terms = map[string]bool{}
		}
		o.terms[p.SessionID] = true
	case method == "terminal.unwatch":
		delete(o.terms, p.SessionID)
	}
}

// noteClosed: a stream or a terminal ended (the hub's stream.close, the node's stream.close or terminal.end).
func (m *Machine) noteClosed(event string, params json.RawMessage) {
	if event != "stream.close" && event != "terminal.end" {
		return
	}
	var p struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
		Control   bool   `json:"control"` // only its control ended: still watched
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if event == "stream.close" {
		delete(m.opened.streams, p.ID)
	} else if !p.Control {
		delete(m.opened.terms, p.SessionID)
	}
}

// forgetOpenedLocked: the node's connection ended, and with it all it had open; m.mu is held.
func (m *Machine) forgetOpenedLocked() { m.opened = hubOpened{} }

// endHubOpened ends on the nodes what the hub had open through the last bus connection (a new one said hello).
func (d *Devices) endHubOpened() {
	for _, m := range d.machinesNow() {
		m.mu.Lock()
		o, p := m.opened, m.peer
		m.opened = hubOpened{}
		m.mu.Unlock()
		if p == nil || len(o.streams)+len(o.terms) == 0 {
			continue // no connection: nothing of it is open on the node
		}
		d.logf("[%s] the hub's connection is new: %d streams and %d terminals of the last one end on the node", m.cfg.ID,
			len(o.streams), len(o.terms))
		for id := range o.streams {
			b, _ := json.Marshal(map[string]any{"id": id})
			m.queueNote("stream.close", b)
		}
		// On this node connection, now: before the hub asks anything through the new bus connection (a terminal an
		// app watches again).
		for id := range o.terms {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = p.Call(ctx, "terminal.unwatch", map[string]any{"session_id": id}, nil)
			}()
		}
	}
}
