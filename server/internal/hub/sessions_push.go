package hub

import (
	"bytes"
	"encoding/json"
	"github.com/nwverse/persona/server/internal/compat"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/proto"
)

// The session lists the apps get (docs/protocol/protocol.md "Session deltas", docs/server/server.md "What gets pushed to the app"). An app that asks for
// sessions_delta (?features= on the WebSocket) gets each machine's list once when it connects, then only what
// changed: the sessions that are new or different, each whole, and the ids that went. An older app gets the whole
// list each time, as before, though never the same list twice. A machine's list is ~1 KB a session and up to 150
// sessions, and one session changes at a time: the phone downloaded and decoded ~2.6 MB a minute of lists.
const featureSessionsDelta = "sessions_delta"

// wantsFeature: the app listed feature in the WebSocket URL's ?features= (comma-separated).
func wantsFeature(r *http.Request, feature string) bool {
	return urlFeatures(r)[feature]
}

// urlFeatures is everything the app listed in the WebSocket URL's ?features=.
func urlFeatures(r *http.Request) map[string]bool {
	set := map[string]bool{}
	for _, f := range strings.Split(r.URL.Query().Get("features"), ",") {
		if f != "" {
			set[f] = true
		}
	}
	return set
}

// sentSession is one session of a list, as sent.
type sentSession struct {
	id string
	// history: a Codex session known only from its rollout (codex_history). Not pushed to a delta app unless it has the
	// session already (a live one that ended): the app fetches the history when it wants it (GET /api/v1/sessions).
	history bool
	raw     json.RawMessage
}

// encodeSessions encodes each session of list (the same bytes json.Marshal gives) into one buffer, each raw a slice
// of it: one allocation for the list rather than a copy of every session and then a clone of its bytes, what a push
// spent most of its memory on. The raws share the buffer and are only read from then on.
func encodeSessions(list []proto.Session) []sentSession { return encodeRows(list, false, "") }

// encodeStamped is encodeSessions of list as stamped with manager (stamped), without the copy of the list: a session
// that names another manager is copied alone, into one scratch session, to be encoded.
func encodeStamped(list []proto.Session, manager string) []sentSession {
	return encodeRows(list, true, manager)
}

func encodeRows(list []proto.Session, stamp bool, manager string) []sentSession {
	var w rowWriter
	spans := make([]rowSpan, len(list))
	for i := range list {
		spans[i] = w.add(&list[i], stamp, manager, len(list)-i-1)
	}
	out := make([]sentSession, 0, len(list))
	for i, sp := range spans {
		if sp.start < 0 {
			continue // it does not encode: left out
		}
		out = append(out, sentRow(&list[i], w.row(sp)))
	}
	return out
}

// sentRow is session s sent as row.
func sentRow(s *proto.Session, row []byte) sentSession {
	return sentSession{id: s.SessionID, history: s.SourceKind == "codex_history", raw: row}
}

// rowWriter writes sessions as rows into one buffer: each what json.Marshal gives for it, written by
// proto.AppendSession straight into the buffer, or by encoding/json for a session it leaves to it. json.Encoder,
// which wrote them before, checked and copied again each session's bytes (most of what a row cost).
type rowWriter struct {
	b       []byte
	scratch *proto.Session
}

// rowSpan is where add wrote a row in the buffer; start is -1 for a session that does not encode.
type rowSpan struct{ start, end int }

// add writes s, stamped with manager when stamp (stamped, without changing s), and says where; more is how many
// rows are still to come, for the buffer to be sized from the first.
func (w *rowWriter) add(s *proto.Session, stamp bool, manager string, more int) rowSpan {
	if stamp && s.Manager != manager {
		if w.scratch == nil {
			w.scratch = new(proto.Session)
		}
		*w.scratch = *s
		w.scratch.Manager, s = manager, w.scratch
	}
	start := len(w.b)
	if w.b == nil {
		w.b = make([]byte, 0, 2<<10) // most sessions fit, so the first is not written in growing steps
	}
	b, ok := proto.AppendSession(w.b, s)
	if !ok {
		raw, err := json.Marshal(s) // escapes HTML, as the hub's json.Encoder did
		if err != nil {
			w.b = b[:start]
			return rowSpan{-1, -1}
		}
		b = append(b[:start], raw...)
	}
	w.b = b
	if start == 0 && more > 0 {
		// Sized from the first session, room for the rest to differ a little, so the buffer is rarely regrown.
		w.b = slices.Grow(w.b, len(w.b)*more*5/4)
	}
	return rowSpan{start, len(w.b)}
}

// row is the row at sp, once every row is written (the buffer may move while they are): it has no room to grow
// into the next one.
func (w *rowWriter) row(sp rowSpan) []byte { return w.b[sp.start:sp.end:sp.end] }

// keptRow is a session's row as last encoded from the manager's list (Machine.rows): what the session's bytes in
// that list (raw), its rev and its manager give. A session decoded from the same bytes is the same session, and
// sessions.revs changes only its rev, so while all three are the same the row is too.
type keptRow struct {
	raw   string
	rev   int64
	owner string
	row   []byte
}

// encodeKept is encodeStamped of list, the kept list or what shown left of it, with raws, what each of its sessions
// came in (Machine.rawsFor; nil: encodeStamped itself). A session whose bytes, rev and manager are those of its last
// row is that row again, the same slice, so comparing it with what an app has (sessionsDelta, sameSessions) finds
// them equal at once; only the others are encoded, each into a slice of its own, so that a row kept for long does not
// hold the rest of a list's buffer.
func (m *Machine) encodeKept(list []proto.Session, owner string, raws map[string]string) []sentSession {
	if raws == nil {
		return encodeStamped(list, owner)
	}
	m.rowsMu.Lock()
	defer m.rowsMu.Unlock()
	if m.rows == nil {
		m.rows = make(map[string]keptRow, len(list))
	}
	out := make([]sentSession, 0, len(list))
	var w rowWriter // each row written here, then copied out at its size
	for i := range list {
		s := &list[i]
		raw := raws[s.SessionID] // "" for one listed twice: encoded every time
		last, had := m.rows[s.SessionID]
		if raw != "" && had && last.raw == raw && last.rev == s.Rev && last.owner == owner {
			out = append(out, sentRow(s, last.row))
			continue
		}
		w.b = w.b[:0]
		sp := w.add(s, true, owner, 0)
		if sp.start < 0 {
			continue // it does not encode: left out
		}
		row := append(make([]byte, 0, sp.end-sp.start), w.row(sp)...)
		if raw != "" {
			m.rows[s.SessionID] = keptRow{raw: raw, rev: s.Rev, owner: owner, row: row}
		}
		out = append(out, sentRow(s, row))
	}
	if len(m.rows) > len(raws)+len(raws)/4+8 { // sessions the lists no longer name: their rows go
		for id := range m.rows {
			if _, listed := raws[id]; !listed {
				delete(m.rows, id)
			}
		}
	}
	return out
}

// sessionsMessage is the whole-list message (the same bytes as marshalling the sessions themselves). Put together
// from the encoded sessions rather than marshalled: json.Marshal checks and copies every json.RawMessage again,
// half of what a list push cost, for sessions it has just encoded.
func sessionsMessage(machine string, list []sentSession) []byte {
	id, _ := json.Marshal(machine)
	size := len(`{"machine_id":,"sessions":[],"type":"sessions"}`) + len(id) + len(list)
	for _, s := range list {
		size += len(s.raw)
	}
	b := make([]byte, 0, size)
	b = append(b, `{"machine_id":`...)
	b = append(b, id...)
	b = append(b, `,"sessions":[`...)
	for i, s := range list {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, s.raw...)
	}
	return append(b, `],"type":"sessions"}`...)
}

// forDelta is what a delta app gets of list, given what it has.
func forDelta(list []sentSession, have map[string][]byte) []sentSession {
	out := make([]sentSession, 0, len(list))
	for _, s := range list {
		if _, had := have[s.id]; s.history && !had {
			continue
		}
		out = append(out, s)
	}
	return out
}

// sessionsDelta is the message taking an app from have to list (nil: nothing changed), and what it then has. Run for
// every delta app on every push, and most pushes change nothing for a given app's machine list, so that case is
// checked first without allocating and gives back have itself. A list naming a session twice, which no node sends,
// may then hide that one of have's sessions went; the next change removes it.
func sessionsDelta(machine string, list []sentSession, have map[string][]byte) ([]byte, map[string][]byte) {
	if unchanged(list, have) {
		return nil, have
	}
	next := make(map[string][]byte, len(list))
	var upsert []json.RawMessage
	for _, s := range list {
		old, had := have[s.id]
		if s.history && !had {
			continue
		}
		next[s.id] = s.raw
		if !had || !bytes.Equal(old, s.raw) {
			upsert = append(upsert, s.raw)
		}
	}
	var remove []string
	for id := range have {
		if _, ok := next[id]; !ok {
			remove = append(remove, id)
		}
	}
	if len(upsert) == 0 && len(remove) == 0 {
		return nil, next
	}
	slices.Sort(remove)
	return deltaMessage(machine, upsert, remove), next
}

// unchanged: an app with have needs nothing for list (what forDelta shows of it is have, byte for byte).
func unchanged(list []sentSession, have map[string][]byte) bool {
	n := 0
	for _, s := range list {
		old, had := have[s.id]
		if s.history && !had {
			continue
		}
		if !had || !bytes.Equal(old, s.raw) {
			return false
		}
		n++
	}
	return n == len(have)
}

// deltaMessage is the sessions_delta message, the same bytes as marshalling a map of it (its keys sorted, empty
// arrays as []) but put together from the encoded sessions, as sessionsMessage is.
func deltaMessage(machine string, upsert []json.RawMessage, remove []string) []byte {
	id, _ := json.Marshal(machine)
	size := len(`{"machine_id":,"remove":[],"type":"sessions_delta","upsert":[]}`) + len(id) + len(upsert)
	for _, raw := range upsert {
		size += len(raw)
	}
	for _, r := range remove {
		size += len(r) + 3 // quotes and comma; escapes grow the slice
	}
	b := make([]byte, 0, size)
	b = append(b, `{"machine_id":`...)
	b = append(b, id...)
	b = append(b, `,"remove":[`...)
	for i, r := range remove {
		if i > 0 {
			b = append(b, ',')
		}
		q, _ := json.Marshal(r)
		b = append(b, q...)
	}
	b = append(b, `],"type":"sessions_delta","upsert":[`...)
	for i, raw := range upsert {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, raw...)
	}
	return append(b, `]}`...)
}

// sameSessions: a and b are the same list, session for session in the same order (what the apps read of it).
func sameSessions(a, b []sentSession) bool {
	return slices.EqualFunc(a, b, func(x, y sentSession) bool { return x.id == y.id && bytes.Equal(x.raw, y.raw) })
}

// broadcastSessions sends every app a machine's session list: an older app the whole list unless it is the list
// they last got (an interaction shown, a session seen, a node pass that changed nothing the app reads), a delta app
// what changed for it. The whole-list message (~150 KB) is put together only when the list changed and an older app
// is connected, once for all of them. The lock is held until everything is queued, so what was recorded is what the
// apps got last. list is the machine's as kept (keptSessions) or as decorated: the apps get it stamped with its
// manager either way, and it is never changed.
func (h *Hub) broadcastSessions(m *Machine, list []proto.Session) {
	owner := h.machineOwner(m.cfg.ID)
	shown := h.shown(m.cfg.ID, list)
	enc := m.encodeKept(shown, owner, m.rawsFor(list))
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	before := h.listCache.index.Revision()
	if h.listCache.machines != nil { // a paginated reader: its index reads the sessions themselves
		h.indexSessionsLocked(m.cfg.ID, stamped(shown, owner), enc)
	}
	revision := h.listCache.index.Revision()
	prev, sent := h.sessionsSent[m.cfg.ID]
	changed := !sent || !sameSessions(prev, enc)
	if changed {
		if h.sessionsSent == nil {
			h.sessionsSent = map[string][]sentSession{}
		}
		h.sessionsSent[m.cfg.ID] = enc
		h.live.changed() // what the Fleet activity is made of
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	var full []byte
	var projected [2][]byte // full as compat.ProjectJSON leaves it for apps without and with the canonical contracts
	// Delta apps that have the same map (snapshotSessions hands an app the map another has when they are equal, and the
	// apps with one map get the same next) get the same message and the same next: worked out once for them all.
	type delta struct {
		msg  []byte
		next map[string][]byte
	}
	var deltas map[uintptr]delta
	for c := range h.clients {
		var b []byte
		if c.listMode {
			if before != revision {
				c.sendJSON(map[string]any{"type": "sessions_list", "revision": revision})
			}
			continue
		}
		if c.delta {
			have := c.have[m.cfg.ID]
			key := mapIdentity(have)
			d, done := deltas[key]
			if !done {
				d.msg, d.next = sessionsDelta(m.cfg.ID, enc, have)
				if deltas == nil {
					deltas = map[uintptr]delta{}
				}
				deltas[key] = d
			}
			b, c.have[m.cfg.ID] = d.msg, d.next
		} else if changed {
			if full == nil {
				full = sessionsMessage(m.cfg.ID, enc)
			}
			canonical, k := c.canonical(), 0
			if canonical {
				k = 1
			}
			if projected[k] == nil {
				projected[k] = compat.ProjectJSON(full, canonical)
			}
			c.queueProjected(projected[k])
			continue
		}
		if b != nil {
			c.queue(b)
		}
	}
}

// snapshotSessions sends a delta app that just connected a machine's whole list (without the Codex history), what
// later deltas build on. Under sessionsMu, so no delta for the machine can come between the list and the record.
func (h *Hub) snapshotSessions(c *client, m *Machine) {
	h.sessionsMu.Lock()
	defer h.sessionsMu.Unlock()
	kept := m.keptSessions()
	shown := forDelta(m.encodeKept(h.shown(m.cfg.ID, kept), h.machineOwner(m.cfg.ID), m.rawsFor(kept)), nil)
	have := make(map[string][]byte, len(shown))
	for _, s := range shown {
		have[s.id] = s.raw
	}
	h.mu.Lock()
	for o := range h.clients { // another app that has this list: the same map, so that pushes work it out once for both
		if other := o.have[m.cfg.ID]; o != c && o.delta && len(other) == len(have) && maps.EqualFunc(other, have, bytes.Equal) {
			have = other
			break
		}
	}
	h.mu.Unlock()
	c.have[m.cfg.ID] = have
	c.queue(sessionsMessage(m.cfg.ID, shown))
}

// mapIdentity is which map have is (a map value is a pointer): the apps' maps are never changed once handed out (a
// change is a new map, sessionsDelta's next), so the same map is the same content.
func mapIdentity(have map[string][]byte) uintptr { return reflect.ValueOf(have).Pointer() }

// queue puts b on the client's way out; a client 512 messages behind is dropped (it would miss this one for good
// now that nothing is sent twice) and resyncs when it reconnects.
func (c *client) queue(b []byte) {
	c.queueProjected(compat.ProjectJSON(b, c.canonical()))
}

// canonical: the app takes the canonical contracts, which compat.ProjectJSON leaves as they are.
func (c *client) canonical() bool {
	return c.canonicalContracts.Load() || c.urlFeatures[compat.Feature]
}

// queueProjected is queue for b already as compat.ProjectJSON leaves it for the app: a message queued to several apps
// is projected once.
func (c *client) queueProjected(b []byte) {
	select {
	case c.out <- b:
	default:
		c.dropSlow()
	}
}

// dropSlow closes a client too slow to take what it is sent, in the background and once: the close waits on its stuck
// connection (up to about 10 s), and until it ends the client is still sent to, each send finding the queue full.
func (c *client) dropSlow() {
	if c.dropped.CompareAndSwap(false, true) && c.conn != nil {
		go c.conn.Close(websocket.StatusPolicyViolation, "too slow")
	}
}
