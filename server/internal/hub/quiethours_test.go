package hub

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestQuietHoursWindow(t *testing.T) {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	at := func(hh, mm int) time.Time { return time.Date(2026, 11, 6, hh, mm, 0, 0, time.UTC) }
	for _, tc := range []struct {
		name string
		q    QuietHours
		t    time.Time
		loc  *time.Location
		want bool
	}{
		{"off", QuietHours{Start: "00:00", End: "23:59"}, at(12, 0), time.UTC, false},
		{"same day inside", QuietHours{Enabled: true, Start: "13:00", End: "15:00"}, at(14, 0), time.UTC, true},
		{"same day end is outside", QuietHours{Enabled: true, Start: "13:00", End: "15:00"}, at(15, 0), time.UTC, false},
		{"start is inside", QuietHours{Enabled: true, Start: "13:00", End: "15:00"}, at(13, 0), time.UTC, true},
		{"across midnight, late", QuietHours{Enabled: true, Start: "23:00", End: "07:00"}, at(23, 30), time.UTC, true},
		{"across midnight, early", QuietHours{Enabled: true, Start: "23:00", End: "07:00"}, at(6, 59), time.UTC, true},
		{"across midnight, day", QuietHours{Enabled: true, Start: "23:00", End: "07:00"}, at(12, 0), time.UTC, false},
		{"start equals end is no window", QuietHours{Enabled: true, Start: "07:00", End: "07:00"}, at(7, 0), time.UTC, false},
		// 15:00 UTC is 00:00 in Tokyo.
		{"the fallback zone", QuietHours{Enabled: true, Start: "23:00", End: "07:00"}, at(15, 0), tokyo, true},
		{"its own zone over the fallback", QuietHours{Enabled: true, Start: "23:00", End: "07:00", TZ: "UTC"}, at(15, 0), tokyo, false},
		// No zone at all reads UTC, whatever the machine's zone is (#5878).
		{"no zone at all, inside in UTC", QuietHours{Enabled: true, Start: "13:00", End: "15:00"}, at(14, 0), nil, true},
		{"no zone at all, outside in UTC", QuietHours{Enabled: true, Start: "13:00", End: "15:00"}, at(16, 0), nil, false},
		{"bad times", QuietHours{Enabled: true, Start: "25:00", End: "07:00"}, at(1, 0), time.UTC, false},
	} {
		if got := tc.q.activeAt(tc.t, tc.loc); got != tc.want {
			t.Errorf("%s: activeAt = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestQuietHoursValidate(t *testing.T) {
	for _, q := range []QuietHours{
		{},
		{Start: "23:00", End: "07:00"},
		{Enabled: true, Start: "00:00", End: "23:59", TZ: "UTC"},
	} {
		if err := q.validate(); err != nil {
			t.Errorf("%+v: %v", q, err)
		}
	}
	for _, q := range []QuietHours{
		{Start: "", End: "07:00"},
		{Enabled: true},
		{Start: "7:00", End: "08:00"},
		{Start: "24:00", End: "08:00"},
		{Start: "23:60", End: "08:00"},
		{Start: "23:00", End: "08:00", TZ: "Not/AZone"},
	} {
		if q.validate() == nil {
			t.Errorf("%+v passed", q)
		}
	}
}

// The prefs API carries quiet hours: off before any are set, a bad window refused, a good one stored.
func TestQuietHoursPrefsAPI(t *testing.T) {
	h := newTestHub(t)
	mux := h.routes()
	code, got := call(t, mux, "GET", "/api/v1/devices/phone-q/prefs", h.cfg.Token, "")
	if code != 200 {
		t.Fatal(code, got)
	}
	if q, _ := got["quiet"].(map[string]any); q["enabled"] != false || q["start"] != "" || q["end"] != "" {
		t.Fatalf("default quiet = %v", got["quiet"])
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/devices/phone-q/prefs", h.cfg.Token, `{"quiet":{"enabled":true,"start":"9pm","end":"07:00"}}`); code != 400 {
		t.Fatalf("a bad window: %d", code)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/devices/phone-q/prefs", h.cfg.Token, `{"quiet":{"enabled":true,"start":"22:30","end":"06:45","tz":"UTC"}}`); code != 200 {
		t.Fatalf("a good window: %d", code)
	}
	// Only quiet was sent: the rest stays as it was.
	if p := h.push.prefs("phone-q"); p.Quiet != (QuietHours{Enabled: true, Start: "22:30", End: "06:45", TZ: "UTC"}) || !p.Input || p.Done != DoneLong {
		t.Fatalf("stored prefs = %+v", p)
	}
}

// A window stored before it was checked comes back off.
func TestQuietHoursStoredBadWindowIsOff(t *testing.T) {
	p := devicePrefs(json.RawMessage(`{"quiet":{"enabled":true,"start":"late","end":"07:00"}}`))
	if p.Quiet != (QuietHours{}) {
		t.Fatalf("quiet = %+v", p.Quiet)
	}
}

// Inside the phone's window: no "Agent finished", "Needs you" without sound, an assistant's reminder as usual.
// Outside it, everything as before.
func TestQuietHoursPushes(t *testing.T) {
	h, f := testLive(t)
	night := time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC)
	// The clock moves under the pusher, never swapped: a push still going out reads it (Pusher.pushed).
	var clock atomic.Int64
	clock.Store(night.UnixNano())
	h.push.now = func() time.Time { return time.Unix(0, clock.Load()).UTC() }
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneAlways,
		Quiet: QuietHours{Enabled: true, Start: "23:00", End: "07:00", TZ: "UTC"}}); err != nil {
		t.Fatal(err)
	}
	done := pushWants{Kind: "done", Took: time.Hour, Long: time.Minute}
	ix := proto.Interaction{ID: "ix1", MachineID: "m", SessionID: "s", Kind: proto.IxQuestion, Summary: "pick one"}
	aps := func(c apnsCall) map[string]any {
		t.Helper()
		var body struct {
			APS map[string]any `json:"aps"`
		}
		if err := json.Unmarshal(c.Body, &body); err != nil {
			t.Fatal(err)
		}
		return body.APS
	}

	h.notifyDone(assistantwire.AssistantID, "done", "m", "box", "s", "finished", "", done)
	h.notifyInteraction(assistantwire.AssistantID, ix, "box")
	c := f.calls(t, 1)
	time.Sleep(150 * time.Millisecond)
	f.mu.Lock()
	n := len(f.got)
	f.mu.Unlock()
	if n != 1 {
		t.Fatalf("inside the window, %d pushes: the finished one was sent", n)
	}
	if a := aps(c[0]); a["sound"] != nil || a["interruption-level"] != "passive" || a["category"] != "INTERACTION_QUESTION" {
		t.Fatalf("the card inside the window: %v", a)
	}

	h.notifyDone(assistantwire.AssistantID, "assistant", "", "Persona", "s", "reminder", "leave now", pushWants{})
	if a := aps(f.calls(t, 2)[1]); a["sound"] != "default" {
		t.Fatalf("an assistant reminder inside the window: %v", a)
	}

	clock.Store(night.Add(10 * time.Hour).UnixNano())
	h.notifyDone(assistantwire.AssistantID, "done", "m", "box", "s", "finished", "", done)
	h.notifyInteraction(assistantwire.AssistantID, ix, "box")
	for _, c := range f.calls(t, 4)[2:] {
		if a := aps(c); a["sound"] != "default" {
			t.Fatalf("outside the window: %v", a)
		}
	}
}

// On the WebSocket: the finished one is not sent inside the window, the card is marked quiet.
func TestQuietHoursWS(t *testing.T) {
	c := &client{device: "test-device", out: make(chan []byte, 8)}
	h := &Hub{clients: map[*client]struct{}{c: {}}, push: newPusher(t.TempDir(), nil, t.Logf)}
	h.push.now = func() time.Time { return time.Date(2026, 11, 6, 23, 30, 0, 0, time.UTC) }
	h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
	<-c.out // client_hello_ack
	if err := h.push.setPrefs(c.device, NotifyPrefs{Input: true, Done: DoneAlways,
		Quiet: QuietHours{Enabled: true, Start: "23:00", End: "07:00", TZ: "UTC"}}); err != nil {
		t.Fatal(err)
	}
	next := func() map[string]any {
		t.Helper()
		select {
		case raw := <-c.out:
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			return m
		default:
			return nil
		}
	}
	h.notifyDone(assistantwire.AssistantID, "done", "m", "box", "s", "finished", "", pushWants{Kind: "done", Took: time.Hour, Long: time.Minute})
	if m := next(); m != nil {
		t.Fatalf("finished inside the window: %v", m)
	}
	h.notifyInteraction(assistantwire.AssistantID, proto.Interaction{ID: "ix1", MachineID: "m", SessionID: "s", Kind: proto.IxQuestion, Summary: "pick"}, "box")
	if m := next(); m["kind"] != "interaction" || m["quiet"] != true {
		t.Fatalf("card inside the window: %v", m)
	}
	h.notifyDone(assistantwire.AssistantID, "assistant", "", "Persona", "s", "reminder", "now", pushWants{})
	if m := next(); m["kind"] != "assistant" || m["quiet"] != nil {
		t.Fatalf("reminder inside the window: %v", m)
	}
}

// Inside the window a Live Activity still starts, but without the alert that lights the screen.
func TestQuietHoursLiveActivityStartHasNoAlert(t *testing.T) {
	h, f := testLive(t)
	h.push.now = func() time.Time { return time.Date(2026, 11, 6, 2, 0, 0, 0, time.UTC) }
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true,
		Quiet: QuietHours{Enabled: true, Start: "23:00", End: "07:00", TZ: "UTC"}}); err != nil {
		t.Fatal(err)
	}
	h.live.update("phone", func(v *db.LiveActivity) { v.PushToStartToken = "p2s" })
	h.live.flush(FleetState{Phase: "needs", NeedsYou: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "needs"}}}, true, false)
	c := f.calls(t, 1)
	var body struct {
		APS map[string]any `json:"aps"`
	}
	if err := json.Unmarshal(c[0].Body, &body); err != nil {
		t.Fatal(err)
	}
	if c[0].Event != "start" {
		t.Fatalf("not a start: %+v", c[0])
	}
	if _, ok := body.APS["alert"]; ok {
		t.Fatalf("a start inside the window kept its alert: %v", body.APS)
	}
}
