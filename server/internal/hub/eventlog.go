package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// The event log (docs/operations/hub-restart.md "Event resume"): every message broadcastTo sends every app carries event_seq, one
// more than the last, and the hub keeps the latest ones. An app that reconnects says which stream and seq it had
// (?event_stream=&event_seq= on /api/v1/ws) and gets what it missed instead of starting over; its hello says whether
// that worked (resumed). A stopping hub hands the log to the next one through Postgres (settings "hub.event_log",
// taken once), so a deploy keeps the stream and its numbers; after a crash a new stream starts and apps refresh.
//
// What an app gets per connection (snapshots on connect, sessions_delta, a session's items and delta, terminal frames)
// is not in it: the snapshots are complete state, items resume by since_seq, terminals by epoch and after_seq.

const (
	eventLogMax    = 2000             // events kept
	eventLogBytes  = 8 << 20          // and at most this much of them
	eventLogAge    = 15 * time.Minute // and none older
	eventReplayMax = 400              // more than this to catch up on (a client's queue holds 512): start over
	eventLogKey    = "hub.event_log"
	eventLogStale  = 10 * time.Minute // a log handed over longer ago than this is not continued
)

type loggedEvent struct {
	Seq         int64     `json:"seq"`
	At          time.Time `json:"at"`
	Elicitation bool      `json:"elicitation,omitempty"`
	Body        []byte    `json:"body"` // as sent, event_seq included
}

// eventLog is guarded by Hub.mu (broadcastTo appends under it).
type eventLog struct {
	stream string
	seq    int64
	events []loggedEvent // oldest first
	bytes  int
}

type savedEventLog struct {
	Stream  string        `json:"stream"`
	Seq     int64         `json:"seq"`
	Events  []loggedEvent `json:"events"`
	SavedAt time.Time     `json:"saved_at"`
}

func newEventLog() *eventLog { return &eventLog{stream: randomHex(8)} }

// stampEvent puts "event_seq" first in the JSON object b. The log owns that key: if b already has one at its top level
// (a node's hook.event passes on what the sender put there; relayBroadcastFrom takes a manager's out itself, as an
// extension_operation does not come through here), it is taken out first. Otherwise the apps would get the key twice
// and, as they read a repeated key's last value, take the sender's number for their resume cursor.
func stampEvent(b []byte, seq int64) []byte {
	if len(b) < 2 || b[0] != '{' {
		return b
	}
	b = withoutEventSeq(b)
	head := `{"event_seq":` + strconv.FormatInt(seq, 10)
	out := make([]byte, 0, len(b)+len(head)+1)
	out = append(out, head...)
	if rest := b[1:]; len(rest) > 0 && rest[0] != '}' {
		out = append(out, ',')
	}
	return append(out, b[1:]...)
}

// withoutEventSeq is the JSON object b without a top-level "event_seq". Every body comes from Go's encoder, which
// writes that key as it is: one without the name anywhere has none and is not decoded (most bodies, under Hub.mu);
// one with it only deeper down or as text comes back byte for byte. Only one that has it at the top level is written
// again, from a map as relayBroadcastFrom writes a manager's message, so its other keys keep their order. A case
// variant ("Event_seq") stays: the apps read exact keys, it is the sender's own field to them.
func withoutEventSeq(b []byte) []byte {
	if !hasEventSeq(b) {
		return b
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	if _, ok := m["event_seq"]; !ok {
		return b
	}
	delete(m, "event_seq")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(m) != nil {
		return b
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

var eventSeqKey = []byte(`"event_seq"`)

// hasEventSeq: b has `"event_seq"`, quotes included, anywhere. bytes.Contains would look for its first byte, a quote,
// which every key and string has, and fall back to comparing at every byte: that added some 70% to
// BenchmarkEventLogAdd. A 'q' is rare in a body, so this finds each one with IndexByte and compares the key around it.
func hasEventSeq(b []byte) bool {
	const q = len(`"event_se`) // the q's place in the key
	for i := q; i < len(b); i++ {
		j := bytes.IndexByte(b[i:], 'q')
		if j < 0 {
			return false
		}
		i += j
		if i+2 <= len(b) && bytes.Equal(b[i-q:i+2], eventSeqKey) {
			return true
		}
	}
	return false
}

// add numbers b, keeps it and returns it as it is to be sent.
func (l *eventLog) add(b []byte, elicitation bool, now time.Time) []byte {
	l.seq++
	body := stampEvent(b, l.seq)
	l.events = append(l.events, loggedEvent{Seq: l.seq, At: now, Elicitation: elicitation, Body: body})
	l.bytes += len(body)
	l.trim(now)
	return body
}

func (l *eventLog) trim(now time.Time) {
	drop := 0
	for drop < len(l.events) && (len(l.events)-drop > eventLogMax || l.bytes > eventLogBytes || now.Sub(l.events[drop].At) > eventLogAge) {
		l.bytes -= len(l.events[drop].Body)
		drop++
	}
	if drop > 0 {
		// Copying what is kept into a new array on every add cost a full log's worth (2000 events) per broadcast, under
		// Hub.mu. Slicing the front off lets append move the kept events only when the array is full, every few
		// hundred adds; clearing the dropped slots lets their bodies go now, not at that move.
		clear(l.events[:drop])
		l.events = l.events[drop:]
	}
}

// since is what a client that last saw (stream, after) missed; ok is false when that cannot be told (another stream,
// fallen out of the log, too much of it): the client starts over from the snapshots.
func (l *eventLog) since(stream string, after int64) (missed []loggedEvent, ok bool) {
	if stream == "" || stream != l.stream || after < 0 || after > l.seq {
		return nil, false
	}
	if after == l.seq {
		return nil, true
	}
	if len(l.events) == 0 || l.events[0].Seq > after+1 || l.seq-after > eventReplayMax {
		return nil, false
	}
	i := len(l.events) - int(l.seq-after)
	return l.events[i:], true
}

// saveEventLog hands the log to the next hub process (a stopping hub, beginStop).
func (h *Hub) saveEventLog(ctx context.Context) {
	if h.store == nil {
		return
	}
	h.mu.Lock()
	l := h.events
	saved := savedEventLog{Stream: l.stream, Seq: l.seq, Events: append([]loggedEvent(nil), l.events...), SavedAt: time.Now()}
	h.mu.Unlock()
	if err := h.store.PutSetting(ctx, eventLogKey, saved); err != nil {
		h.logf("event log: hand over: %v", err)
	}
}

// loadEventLog continues the stream the last hub handed over, if it did so lately; otherwise a new stream starts.
func (h *Hub) loadEventLog(ctx context.Context) {
	l := newEventLog()
	if h.store != nil {
		var saved savedEventLog
		ok, err := h.store.TakeSetting(ctx, eventLogKey, &saved)
		switch {
		case err != nil:
			h.logf("event log: take over: %v (a new stream: apps refresh)", err)
		case ok && saved.Stream != "" && time.Since(saved.SavedAt) < eventLogStale:
			l.stream, l.seq, l.events = saved.Stream, saved.Seq, saved.Events
			for _, e := range l.events {
				l.bytes += len(e.Body)
			}
			l.trim(time.Now())
			h.logf("event log: continuing stream %s at %d (%d events kept)", l.stream, l.seq, len(l.events))
		}
	}
	h.mu.Lock()
	h.events = l
	h.mu.Unlock()
}

// resumeOf is the stream and last event a reconnecting client had (?event_stream=&event_seq=); resuming is false for
// a client that has none (a first connection, an older app).
func resumeOf(r *http.Request) (stream string, after int64, resuming bool) {
	q := r.URL.Query()
	stream = q.Get("event_stream")
	after, err := strconv.ParseInt(q.Get("event_seq"), 10, 64)
	return stream, after, stream != "" && err == nil
}

// helloLocked queues the hello, which says where the client's stream stands (event_stream, event_seq) and whether it
// resumes (resumed: what it missed follows at once, it need not refetch), then those events; h.mu is held.
func (h *Hub) helloLocked(c *client, hello map[string]any, stream string, after int64, resuming bool) {
	if h.events == nil {
		h.events = newEventLog()
	}
	var missed []loggedEvent
	ok := false
	if resuming {
		missed, ok = h.events.since(stream, after)
	}
	hello["event_stream"], hello["event_seq"], hello["resumed"] = h.events.stream, h.events.seq, ok
	b, err := json.Marshal(hello)
	if err != nil {
		return
	}
	c.queue(b)
	for _, e := range missed {
		if !e.Elicitation { // only for apps that said they answer forms: it has not said so yet; pending ones follow
			c.queue(i18n.ExtensionEvent(c.lang(), localizedMachineEvent(c.lang(), e.Body)))
		}
	}
}
