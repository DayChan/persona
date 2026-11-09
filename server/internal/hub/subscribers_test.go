package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The typed live pushes encode to the same bytes as the map[string]any they replaced.
func TestLivePushesEncodeAsTheirMaps(t *testing.T) {
	ts := time.Date(2026, 11, 5, 14, 30, 0, 123456789, time.UTC)
	text := "a <b> & \"c\"\n\u2028 中文 ↓"
	live := &proto.SessionLive{Verb: "Frosting", ElapsedS: 12, TokensDown: 22800, Thinking: true, At: "2026-11-05T14:30:00Z"}
	cases := []struct {
		name  string
		typed any
		old   map[string]any
	}{
		{"delta", deltaPush{Type: "delta", MachineID: "m1", SessionID: "s1", Text: text},
			map[string]any{"type": "delta", "machine_id": "m1", "session_id": "s1", "text": text}},
		{"delta empty", deltaPush{Type: "delta"},
			map[string]any{"type": "delta", "machine_id": "", "session_id": "", "text": ""}},
		{"screen", screenPush{Type: "screen", MachineID: "m1", SessionID: "s1", Text: text, Status: "working", TS: ts},
			map[string]any{"type": "screen", "machine_id": "m1", "session_id": "s1", "text": text, "status": "working", "ts": ts}},
		{"live", livePush{Type: "live", MachineID: "m1", SessionID: "s1", Live: live},
			map[string]any{"type": "live", "machine_id": "m1", "session_id": "s1", "live": live}},
		{"live cleared", livePush{Type: "live", MachineID: "m1", SessionID: "s1"},
			map[string]any{"type": "live", "machine_id": "m1", "session_id": "s1", "live": (*proto.SessionLive)(nil)}},
	}
	for _, tc := range cases {
		got, err := json.Marshal(tc.typed)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(tc.old)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s:\n got  %s\n want %s", tc.name, got, want)
		}
	}
}

// unencodable fails the test if anything tries to encode it.
type unencodable struct{ t *testing.T }

func (u unencodable) MarshalJSON() ([]byte, error) {
	u.t.Error("encoded a push nobody follows")
	return []byte("null"), nil
}

// A delta for a session no app follows queues nothing and is never encoded; one that an app follows reaches only it.
func TestDeltaToUnsubscribedSessionQueuesNothing(t *testing.T) {
	follower := &client{out: make(chan []byte, 4), subs: map[[2]string]bool{{"m1", "s1"}: true}}
	other := &client{out: make(chan []byte, 4), subs: map[[2]string]bool{{"m1", "s2"}: true}}
	idle := &client{out: make(chan []byte, 4)}
	h := &Hub{clients: map[*client]struct{}{follower: {}, other: {}, idle: {}}}

	h.toSubscribers("m1", "s3", unencodable{t})
	h.toSubscribers("m2", "s1", unencodable{t})
	h.toSubscribersWith("m1", "s1", proto.FeatureSessionLive, unencodable{t}) // nobody asked for live
	for _, c := range []*client{follower, other, idle} {
		if n := len(c.out); n != 0 {
			t.Fatalf("queued %d messages for an unfollowed session", n)
		}
	}

	h.toSubscribers("m1", "s1", deltaPush{Type: "delta", MachineID: "m1", SessionID: "s1", Text: "hi"})
	if got, want := string(<-follower.out), `{"machine_id":"m1","session_id":"s1","text":"hi","type":"delta"}`; got != want {
		t.Errorf("follower got %s, want %s", got, want)
	}
	if len(other.out)+len(idle.out) != 0 {
		t.Error("a delta reached an app that does not follow the session")
	}
}

// benchSubscribersHub is a hub with five connected apps, the first subscribed of them following m1/s1.
func benchSubscribersHub(tb testing.TB, subscribed int) *Hub {
	tb.Helper()
	h := &Hub{clients: map[*client]struct{}{}}
	for i := 0; i < 5; i++ {
		c := &client{out: make(chan []byte, 1), subs: map[[2]string]bool{}}
		if i < subscribed {
			c.subs[[2]string{"m1", "s1"}] = true
		}
		h.clients[c] = struct{}{}
	}
	return h
}

func BenchmarkToSubscribersDelta(b *testing.B) {
	for _, subscribed := range []int{0, 1} {
		b.Run(fmt.Sprintf("subscribed=%d", subscribed), func(b *testing.B) {
			h := benchSubscribersHub(b, subscribed)
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				h.toSubscribers("m1", "s1", deltaPush{Type: "delta", MachineID: "m1", SessionID: "s1", Text: "chunk of streamed text"})
			}
		})
	}
}

// A machine's session.delta reaches the session's follower as the delta push, in the bytes the apps parse, and
// nobody else.
func TestSessionDeltaEventGoesToTheFollower(t *testing.T) {
	h := benchSubscribersHub(t, 1)
	m := newMachine(h, MachineConfig{ID: "m1"})
	m.onEvent("session.delta", json.RawMessage(`{"session_id":"s1","text":"hi there"}`))
	var got []string
	for c := range h.clients {
		if len(c.out) > 0 {
			got = append(got, string(<-c.out))
		}
	}
	want := `{"machine_id":"m1","session_id":"s1","text":"hi there","type":"delta"}`
	if len(got) != 1 || got[0] != want {
		t.Errorf("pushed %q, want only %s", got, want)
	}
}

// BenchmarkSessionDeltaEvent drives a streamed text chunk through the machine's event entry point, so the encoding
// the delta path really uses is measured, not just toSubscribers.
func BenchmarkSessionDeltaEvent(b *testing.B) {
	params, err := json.Marshal(map[string]string{"session_id": "s1", "text": strings.Repeat("streamed text ", 40)})
	if err != nil {
		b.Fatal(err)
	}
	for _, subscribed := range []int{0, 1} {
		b.Run(fmt.Sprintf("subscribed=%d", subscribed), func(b *testing.B) {
			m := newMachine(benchSubscribersHub(b, subscribed), MachineConfig{ID: "m1"})
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				m.onEvent("session.delta", params)
			}
		})
	}
}

// A new subscription is followed by the session's live text as the same delta push the stream sends.
func TestSendLiveTextIsTheDeltaPush(t *testing.T) {
	h := &Hub{}
	c := &client{out: make(chan []byte, 1)}
	h.sendLiveText(c, "m1", "s1")
	if len(c.out) != 0 {
		t.Fatal("sent live text for a session generating nothing")
	}
	h.rememberLiveText("m1", "s1", "so far")
	h.sendLiveText(c, "m1", "s1")
	if got, want := string(<-c.out), `{"machine_id":"m1","session_id":"s1","text":"so far","type":"delta"}`; got != want {
		t.Errorf("sent %s, want %s", got, want)
	}
}

// When a machine's live texts are forgotten (the node disconnected), each subscriber of those sessions
// gets an empty delta push to clear the streaming text, and other sessions/machines are not cleared.
func TestForgetLiveTextClearsSubscribers(t *testing.T) {
	follower1 := &client{out: make(chan []byte, 4), subs: map[[2]string]bool{{"m1", "s1"}: true}}
	followerOtherMachine := &client{out: make(chan []byte, 4), subs: map[[2]string]bool{{"m2", "s1"}: true}}
	followerOtherSession := &client{out: make(chan []byte, 4), subs: map[[2]string]bool{{"m1", "s2"}: true}}
	h := &Hub{
		clients: map[*client]struct{}{follower1: {}, followerOtherMachine: {}, followerOtherSession: {}},
		deltas: map[[2]string]string{
			{"m1", "s1"}: "in progress",
			{"m2", "s1"}: "machine 2 in progress",
			// m1/s2 has no active live text
		},
	}

	h.forgetLiveText("m1")

	// m1/s1 should have sent an empty delta to follower1
	if len(follower1.out) != 1 {
		t.Fatalf("expected 1 clear message for follower1, got %d", len(follower1.out))
	}
	want := `{"machine_id":"m1","session_id":"s1","text":"","type":"delta"}`
	if got := string(<-follower1.out); got != want {
		t.Errorf("follower1 got %s, want %s", got, want)
	}

	// m2/s1 is another machine, should not be forgotten or pushed
	if len(followerOtherMachine.out) != 0 {
		t.Fatalf("followerOtherMachine got unexpected message: %s", string(<-followerOtherMachine.out))
	}

	// m1/s2 had no delta in progress, should not receive anything
	if len(followerOtherSession.out) != 0 {
		t.Fatalf("followerOtherSession got unexpected message: %s", string(<-followerOtherSession.out))
	}

	// m1/s1 should be removed from h.deltas, m2/s1 should remain
	if _, ok := h.deltas[[2]string{"m1", "s1"}]; ok {
		t.Error("m1/s1 delta was not removed from h.deltas")
	}
	if text := h.deltas[[2]string{"m2", "s1"}]; text != "machine 2 in progress" {
		t.Errorf("m2/s1 delta changed: got %q, want %q", text, "machine 2 in progress")
	}
}
