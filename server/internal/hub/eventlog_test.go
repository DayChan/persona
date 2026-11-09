package hub

import (
	"bytes"
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// The log owns event_seq: a body that carries its own at the top level (a node's hook.event; a manager's
// ws.broadcast loses its own earlier, in relayBroadcastFrom) goes out with the hub's only, or the apps would read the
// sender's number (the last of two) as their resume cursor. A body that has the name only deeper down or as text comes back byte for byte.
func TestStampEventOwnsTheKey(t *testing.T) {
	for in, want := range map[string]string{
		`{"event_seq":999999,"type":"task_plan"}`:                    `{"event_seq":7,"type":"task_plan"}`,
		`{"a":1,"event_seq":3,"type":"todos"}`:                       `{"event_seq":7,"a":1,"type":"todos"}`,
		`{"event_seq":1,"event_seq":2,"type":"todos"}`:               `{"event_seq":7,"type":"todos"}`,
		`{"event_seq":"x"}`:                                          `{"event_seq":7}`,
		`{"event_seq":1,"n":1.50,"big":12345678901234567890}`:        `{"event_seq":7,"big":12345678901234567890,"n":1.50}`,
		`{"event_seq":1,"note":"<b>&</b>","type":"inbox"}`:           `{"event_seq":7,"note":"<b>&</b>","type":"inbox"}`,
		`{"event_seq":1,"note":"\u003cb\u003e","type":"hook_event"}`: `{"event_seq":7,"note":"\u003cb\u003e","type":"hook_event"}`,
		// Not at the top level: the body comes back as it was, key order and spacing kept.
		`{"type":"inbox","item":{"event_seq":5}}`:     `{"event_seq":7,"type":"inbox","item":{"event_seq":5}}`,
		`{"z":1,"type":"todos","list":["event_seq"]}`: `{"event_seq":7,"z":1,"type":"todos","list":["event_seq"]}`,
		`{"type": "todos", "note": "event_seq"}`:      `{"event_seq":7,"type": "todos", "note": "event_seq"}`,
		// The apps read exact keys: a case variant is the sender's own field.
		`{"Event_seq":1,"type":"todos"}`: `{"event_seq":7,"Event_seq":1,"type":"todos"}`,
	} {
		got := stampEvent([]byte(in), 7)
		if string(got) != want {
			t.Errorf("stampEvent(%s) = %s, want %s", in, got, want)
		}
		if seqs := topLevel(t, got, "event_seq"); len(seqs) != 1 || seqs[0] != "7" {
			t.Errorf("stampEvent(%s): event_seq %v, want only the hub's 7", in, seqs)
		}
	}
}

func TestHasEventSeq(t *testing.T) {
	for in, want := range map[string]bool{
		`"event_seq"`: true, `{"event_seq":1}`: true, `{"q":"qq","last_seq":1,"event_seq":2}`: true, `["event_seq"]`: true,
		``: false, `q`: false, `"event_seq`: false, `event_seq"`: false, `{"last_seq":1,"seq":2}`: false,
		`{"Event_seq":1}`: false, `{"event_sequence":1}`: false, `{"xevent_seq":1}`: false,
	} {
		if got := hasEventSeq([]byte(in)); got != want {
			t.Errorf("hasEventSeq(%s) = %v", in, got)
		}
	}
}

// What the log keeps for a resuming app is what the apps got: the sender's event_seq taken out there too.
func TestEventLogKeepsTheStampedBody(t *testing.T) {
	l := newEventLog()
	now := time.Now()
	l.add([]byte(`{"type":"todos"}`), false, now)
	sent := l.add([]byte(`{"event_seq":999999,"type":"task_plan"}`), false, now)
	if want := `{"event_seq":2,"type":"task_plan"}`; string(sent) != want {
		t.Fatalf("sent %s, want %s", sent, want)
	}
	missed, ok := l.since(l.stream, 1)
	if !ok || len(missed) != 1 || missed[0].Seq != 2 || !bytes.Equal(missed[0].Body, sent) {
		t.Fatalf("replay after 1: %v %v, want %s", missed, ok, sent)
	}
	if l.bytes != len(`{"event_seq":1,"type":"todos"}`)+len(sent) {
		t.Fatalf("bytes = %d", l.bytes)
	}
}

// A manager's ws.broadcast with its own top-level event_seq reaches the apps with the key once, the hub's number, and
// a resuming app is replayed that same body.
func TestBusBroadcastCarriesOnlyTheHubsEventSeq(t *testing.T) {
	h, srv, _ := newBusHub(t)
	app := wsApp(t, h)
	r := helloAssistant(t, h, srv, echoService)
	msg := json.RawMessage(`{"event_seq":999999,"plan":{"event_seq":5,"id":"p1"},"type":"task_plan"}`)
	if err := r.peer.Notify("ws.broadcast", map[string]any{"message": msg}); err != nil {
		t.Fatal(err)
	}
	raw := nextTyped(t, app, "task_plan")
	if !jsonEqual(withoutSeq(raw), withoutSeq(msg)) {
		t.Fatalf("the apps got %s, want %s (with the hub's event_seq)", raw, msg)
	}
	h.mu.Lock()
	stream := h.events.stream
	var logged *loggedEvent
	for i := range h.events.events {
		if bytes.Equal(h.events.events[i].Body, raw) {
			logged = &h.events.events[i]
		}
	}
	h.mu.Unlock()
	if logged == nil {
		t.Fatalf("the log does not hold what the apps got: %s", raw)
	}
	if seqs := topLevel(t, raw, "event_seq"); len(seqs) != 1 || seqs[0] != strconv.FormatInt(logged.Seq, 10) {
		t.Fatalf("the apps got %s: event_seq %v, want only the hub's %d", raw, seqs, logged.Seq)
	}

	resumed := &client{out: make(chan []byte, 512)}
	h.mu.Lock()
	h.helloLocked(resumed, map[string]any{"type": "hello"}, stream, logged.Seq-1, true)
	h.mu.Unlock()
	var hello map[string]any
	if err := json.Unmarshal(<-resumed.out, &hello); err != nil || hello["resumed"] != true {
		t.Fatalf("hello: %v %v", hello, err)
	}
	if replayed := nextTyped(t, resumed, "task_plan"); !bytes.Equal(replayed, raw) {
		t.Fatalf("replayed %s, the apps got %s", replayed, raw)
	}
}

// nextTyped is the next message of type typ c gets, skipping others.
func nextTyped(t *testing.T, c *client, typ string) []byte {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case b := <-c.out:
			var head struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(b, &head) == nil && head.Type == typ {
				return b
			}
		case <-deadline:
			t.Fatalf("no %s within 5s", typ)
		}
	}
}

// topLevel is every value key has at the top level of the JSON object b, as written and in order: a key sent twice is
// there twice (a map would keep one).
func topLevel(t *testing.T, b []byte, key string) []string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("not a JSON object: %s", b)
	}
	var out []string
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		if k == key {
			out = append(out, string(v))
		}
	}
	return out
}
