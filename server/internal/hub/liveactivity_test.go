package hub

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apns"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The JSON is the Swift ContentState: field names and Unix-second times must not drift.
func TestFleetStateJSON(t *testing.T) {
	st := FleetState{Phase: "working", Working: 1, WorkingSince: 1761700000, Items: []FleetItem{{ID: "m/s", Title: "api", Kind: "working", Since: 1761700000}}}
	b, _ := json.Marshal(st)
	want := `{"phase":"working","needsYou":0,"working":1,"unread":0,"items":[{"id":"m/s","title":"api","place":"","kind":"working","detail":"","since":1761700000}],"workingSince":1761700000,"details":false}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

// A background workflow's item (#2935, #3437), as FleetStateTests.decodesABackgroundItem reads it: background, the line,
// and the bar's counts. A plain working item carries neither (TestFleetStateJSON), so older apps see the same JSON.
func TestFleetBackgroundItemJSON(t *testing.T) {
	st := FleetState{Phase: "working", Working: 1, WorkingSince: 1761700000, Items: []FleetItem{{ID: "m/s", Title: "audit", Kind: "working",
		Detail: "parity-audit · Verify · 169/185", Background: true, Progress: &FleetProgress{Done: 169, Started: 185}, Since: 1761700000}}}
	b, _ := json.Marshal(st)
	want := `{"phase":"working","needsYou":0,"working":1,"unread":0,"items":[{"id":"m/s","title":"audit","place":"","kind":"working","detail":"parity-audit · Verify · 169/185","background":true,"progress":{"done":169,"started":185},"since":1761700000}],"workingSince":1761700000,"details":false}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
}

func TestCoalescer(t *testing.T) {
	var c coalescer
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Kind: "working"}}}
	if now, sched := c.offer(working); now || !sched {
		t.Fatalf("first change should wait: %v %v", now, sched)
	}
	if now, sched := c.offer(working); now || sched {
		t.Fatalf("already scheduled: %v %v", now, sched)
	}
	c.sentState(working)
	if now, sched := c.offer(working); now || sched {
		t.Fatalf("unchanged: %v %v", now, sched)
	}
	asks := working
	asks.Phase, asks.NeedsYou = "needsYou", 1
	asks.Items = append([]FleetItem{{ID: "m/b", Kind: proto.IxPermission, Interaction: "ix1"}}, asks.Items...)
	if now, _ := c.offer(asks); !now {
		t.Fatal("a new request goes out at once")
	}
	c.sentState(asks)
	// The same request with the worker gone: an ordinary change.
	less := asks
	less.Working, less.Items = 0, asks.Items[:1]
	if now, sched := c.offer(less); now || !sched {
		t.Fatalf("no new request: %v %v", now, sched)
	}
}

func TestLiveActivityPayload(t *testing.T) {
	now := time.Unix(1761700000, 0)
	st := FleetState{Phase: "needsYou", NeedsYou: 2, Working: 3}
	start := liveActivityPayload("start", st, now)["aps"].(map[string]any)
	if start["event"] != "start" || start["attributes-type"] != "PersonaActivityAttributes" || start["timestamp"] != now.Unix() {
		t.Fatalf("start %v", start)
	}
	if start["alert"].(map[string]any)["body"] != "2 个需要你 · 3 个在工作" {
		t.Fatalf("alert %v", start["alert"])
	}
	if start["stale-date"] != now.Add(fleetStaleIn).Unix() || start["relevance-score"] != 1.0 {
		t.Fatalf("stale / relevance %v", start)
	}
	update := liveActivityPayload("update", st, now)["aps"].(map[string]any)
	if _, ok := update["alert"]; ok {
		t.Fatal("updates do not alert: the interaction push already did")
	}
	if _, ok := update["category"]; ok {
		t.Fatal("no category on activity pushes")
	}
	end := liveActivityPayload("end", st, now)["aps"].(map[string]any)
	if end["dismissal-date"] != now.Unix() {
		t.Fatalf("end %v", end)
	}
}

// fakeAPNs records what the hub sends.
type fakeAPNs struct {
	mu   sync.Mutex
	got  []apnsCall
	gone map[string]bool // tokens answered with 410
}

type apnsCall struct {
	Token, Topic, Type, Priority, Event string
	State                               FleetState
	Body                                []byte
	Collapse                            string // apns-collapse-id
}

func (f *fakeAPNs) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only pushes count: a node on the test machine probing local ports sends GET / here too (docs/lessons/testing.md).
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/3/device/") {
		http.NotFound(w, r)
		return
	}
	b, _ := io.ReadAll(r.Body)
	var body struct {
		APS struct {
			Event string     `json:"event"`
			State FleetState `json:"content-state"`
		} `json:"aps"`
	}
	_ = json.Unmarshal(b, &body)
	token := strings.TrimPrefix(r.URL.Path, "/3/device/")
	f.mu.Lock()
	f.got = append(f.got, apnsCall{token, r.Header.Get("apns-topic"), r.Header.Get("apns-push-type"), r.Header.Get("apns-priority"), body.APS.Event, body.APS.State, b, r.Header.Get("apns-collapse-id")})
	gone := f.gone[token]
	f.mu.Unlock()
	if gone {
		w.WriteHeader(410)
		_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
	}
}

func (f *fakeAPNs) calls(t *testing.T, n int) []apnsCall {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		if len(f.got) >= n {
			out := append([]apnsCall{}, f.got...)
			f.mu.Unlock()
			return out
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	t.Fatalf("want %d APNs calls, got %d: %+v", n, len(f.got), f.got)
	return nil
}

func testLive(t *testing.T) (*Hub, *fakeAPNs) {
	h := newTestHub(t)
	f := &fakeAPNs{gone: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h.push.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
	h.push.direct.Base = srv.URL
	h.push.register(Device{DeviceID: "phone", Name: "phone", APNSToken: "devtok", Topic: "app.example"})
	return h, f
}

func TestLiveActivityFlush(t *testing.T) {
	h, f := testLive(t)
	la := h.live
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}

	// Only a push-to-start token: start once per episode.
	la.update("phone", func(v *db.LiveActivity) { v.PushToStartToken = "p2s" })
	la.flush(working, false, false)
	la.flush(working, false, false)
	c := f.calls(t, 1)
	if c[0].Token != "p2s" || c[0].Event != "start" || c[0].Type != "liveactivity" || c[0].Topic != "app.example.push-type.liveactivity" {
		t.Fatalf("start %+v", c[0])
	}
	if c[0].State.Details {
		t.Fatal("lock screen details are off by default")
	}

	// The app reports the started activity's token: updates go there, the device's settings decide the details.
	la.update("phone", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act1", "acttok", time.Now() })
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true, LockDetails: true}); err != nil {
		t.Fatal(err)
	}
	la.flush(working, true, false)
	c = f.calls(t, 2)
	if c[1].Token != "acttok" || c[1].Event != "update" || c[1].Priority != "10" || !c[1].State.Details {
		t.Fatalf("update %+v", c[1])
	}

	// Empty for a while: end, and forget the activity token.
	la.flush(FleetState{}, false, false)
	la.mu.Lock()
	la.emptyAt = time.Now().Add(-2 * fleetEndAfter)
	la.mu.Unlock()
	la.tick()
	c = f.calls(t, 3)
	if c[2].Token != "acttok" || c[2].Event != "end" {
		t.Fatalf("end %+v", c[2])
	}
	if r := la.rows["phone"]; r.PushToken != "" || r.PushToStartToken != "p2s" {
		t.Fatalf("row after end %+v", r)
	}

	// A new episode starts again; turning the activity off stops everything.
	la.flush(working, false, false)
	f.calls(t, 4)
	_ = h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: false})
	la.flush(FleetState{}, false, false)
	la.flush(working, false, false)
	time.Sleep(50 * time.Millisecond)
	if n := len(f.calls(t, 4)); n != 4 {
		t.Fatalf("activity off still pushed: %d", n)
	}
}

func TestLiveActivityDeadToken(t *testing.T) {
	h, f := testLive(t)
	f.gone["acttok"] = true
	h.live.update("phone", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act1", "acttok", time.Now() })
	h.live.flush(FleetState{Phase: "working", Working: 1}, false, false)
	f.calls(t, 1)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		h.live.mu.Lock()
		tok := h.live.rows["phone"].PushToken
		h.live.mu.Unlock()
		if tok == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("a 410 should forget the activity token")
}

func TestLiveActivityTokenAPI(t *testing.T) {
	h := newTestHub(t)
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	post := func(body string) {
		t.Helper()
		req, _ := http.NewRequest("POST", srv.URL+"/api/v1/devices/phone/live-activity", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("%v %v", err, resp.Status)
		}
		resp.Body.Close()
	}
	post(`{"push_to_start_token":"p2s"}`)
	post(`{"activity_id":"a1","push_token":"t1"}`)
	r := h.live.rows["phone"]
	if r.PushToStartToken != "p2s" || r.ActivityID != "a1" || r.PushToken != "t1" || r.StartedAt.IsZero() {
		t.Fatalf("%+v", r)
	}
	post(`{"activity_id":"a1","push_token":"t2"}`) // rotated
	if h.live.rows["phone"].PushToken != "t2" {
		t.Fatal("rotation")
	}
	post(`{"activity_id":"old","ended":true}`) // an older activity ending must not drop the current one
	if h.live.rows["phone"].PushToken != "t2" {
		t.Fatal("ended an unrelated activity")
	}
	post(`{"activity_id":"a1","ended":true}`)
	if r := h.live.rows["phone"]; r.PushToken != "" || r.PushToStartToken != "p2s" {
		t.Fatalf("after end %+v", r)
	}
}

// TestManualLiveActivityPush sends one real update through APNs (sandbox) with this machine's ~/.persona/apns.json, to
// check a running activity end to end (docs/operations/testing.md). Opt-in: PERSONA_LA_TOKEN=<activity push token>
// PERSONA_LA_TOPIC=<app bundle id>.
func TestManualLiveActivityPush(t *testing.T) {
	token, topic := os.Getenv("PERSONA_LA_TOKEN"), os.Getenv("PERSONA_LA_TOPIC")
	if token == "" || topic == "" {
		t.Skip("set PERSONA_LA_TOKEN and PERSONA_LA_TOPIC")
	}
	p := newPusher(dataDir(), nil, t.Logf)
	if p.direct == nil {
		t.Skip("no APNs key here")
	}
	now := time.Now()
	st := FleetState{Phase: "needsYou", NeedsYou: 1, Working: 1, WorkingSince: float64(now.Add(-100 * time.Second).Unix()), Details: true,
		Items: []FleetItem{{ID: "m/s9", Title: "APNs 推送", Place: "测试", Kind: proto.IxPermission, Detail: "Bash: echo hi", Interaction: "ix9", Since: float64(now.Unix())}}}
	done := make(chan struct{})
	p.logf = func(f string, a ...any) { t.Logf(f, a...) }
	p.sendLiveActivity(Device{Name: "simulator", APNSEnv: "sandbox", Topic: topic}, token, liveActivityPayload("update", st, now), "10", func() { t.Error("token gone") })
	go func() { time.Sleep(3 * time.Second); close(done) }()
	<-done
}

// Turning lock screen details (or the activity) on / off changes what each device gets though the fleet did not:
// the next signal must push anyway.
func TestPrefsChangeResends(t *testing.T) {
	h := newTestHub(t)
	srv := httptest.NewServer(h.routes())
	defer srv.Close()
	req, _ := http.NewRequest("PUT", srv.URL+"/api/v1/devices/phone/prefs", strings.NewReader(`{"lock_details":true}`))
	req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("%v %v", err, resp.Status)
	}
	resp.Body.Close()
	h.live.mu.Lock()
	resend := h.live.resend
	h.live.mu.Unlock()
	if !resend {
		t.Fatal("a prefs change should force the next fleet push")
	}
	if p := h.push.prefs("phone"); !p.LockDetails || !p.LiveActivity {
		t.Fatalf("%+v", p)
	}
}

func TestIslandSides(t *testing.T) {
	cases := []struct {
		p           IslandPrefs
		left, right string
	}{
		{IslandPrefs{}, SlotStatus, SlotStatus},
		{IslandPrefs{Preset: "a"}, SlotStatus, SlotStatus},
		{IslandPrefs{Preset: "b"}, SlotStatus, SlotTPM},
		{IslandPrefs{Preset: "c"}, SlotStatus, SlotTokens},
		{IslandPrefs{Preset: "custom", Left: SlotAssistant, Right: SlotLoad}, SlotAssistant, SlotLoad},
		{IslandPrefs{Preset: "custom", Left: "cost", Right: ""}, SlotStatus, SlotStatus},
	}
	for _, c := range cases {
		if l, r := c.p.sides(); l != c.left || r != c.right {
			t.Errorf("%+v: %s / %s, want %s / %s", c.p, l, r, c.left, c.right)
		}
	}
	if p := devicePrefs(json.RawMessage(`{"island":{"preset":"zz"}}`)); p.Island.Preset != "a" {
		t.Errorf("unknown preset kept: %+v", p.Island)
	}
}

// The device's persona rides in its pushes (the widget extension cannot read the app's settings); the JSON is what
// apple/Shared/FleetState.swift decodes. One that would bloat every push is dropped.
func TestFleetLookForDevice(t *testing.T) {
	look := &FleetLook{Design: "circuit", Light: 0xD4643F, Dark: 0xE8837C}
	d := forDevice(FleetState{Phase: "working", Working: 1}, NotifyPrefs{Look: look}, FleetStats{})
	b, _ := json.Marshal(d)
	if want := `"look":{"design":"circuit","light":13919295,"dark":15238012}`; !strings.Contains(string(b), want) {
		t.Fatalf("got %s, want %s in it", b, want)
	}
	if d := forDevice(FleetState{}, defaultPrefs(), FleetStats{}); d.Look != nil {
		t.Fatalf("a device that told nothing gets no look: %+v", d.Look)
	}
	p := devicePrefs(json.RawMessage(`{"look":{"design":"visor","light":1,"dark":2,"package":"mint/r1"}}`))
	if p.Look == nil || p.Look.Package != "mint/r1" {
		t.Fatalf("stored look: %+v", p.Look)
	}
	if p := devicePrefs(json.RawMessage(`{"look":{"design":"` + strings.Repeat("x", 40) + `"}}`)); p.Look != nil {
		t.Fatalf("an oversized look kept: %+v", p.Look)
	}
}

func TestFleetForDevice(t *testing.T) {
	stats := FleetStats{TPM: 12000, Tokens: 42000000, Requests: 220,
		Loads: []FleetLoad{{ID: "a", Name: "A", Percent: 30}, {ID: "b", Name: "B", Percent: 70}}}
	st := FleetState{Phase: "working", Working: 1, Assistant: true}
	a := forDevice(st, defaultPrefs(), stats)
	if a.Stats != nil || a.Assistant || a.Left != SlotStatus || a.Right != SlotStatus {
		t.Fatalf("preset a: %+v", a)
	}
	b := forDevice(st, NotifyPrefs{Island: IslandPrefs{Preset: "b"}, LockDetails: true}, stats)
	if b.Stats == nil || b.Stats.TPM != 12000 || b.Stats.Tokens != 0 || !b.Details {
		t.Fatalf("preset b: %+v %+v", b, b.Stats)
	}
	c := forDevice(st, NotifyPrefs{Island: IslandPrefs{Preset: "custom", Left: SlotAssistant, Right: SlotLoad}}, stats)
	if !c.Assistant || c.Stats == nil || len(c.Stats.Loads) != 1 || c.Stats.Loads[0].ID != "b" {
		t.Fatalf("custom without a machine shows the busiest: %+v %+v", c, c.Stats)
	}
	c = forDevice(st, NotifyPrefs{Island: IslandPrefs{Preset: "custom", Left: SlotStatus, Right: SlotLoad, Machine: "a"}}, stats)
	if c.Stats.Loads[0].ID != "a" {
		t.Fatalf("chosen machine: %+v", c.Stats)
	}
}

func TestLiveActivityMetrics(t *testing.T) {
	h, f := testLive(t)
	la := h.live
	now := time.Now()
	// The island's numbers as the manager works them out from the nodes' usage (fleet.live).
	report := func(out int64) {
		st, _ := h.fleetLive()
		reportLive(t, h, st, FleetStats{TPM: out / 2, Requests: 1, Loads: []FleetLoad{{ID: "m", Name: "m", Percent: 25}}})
	}
	report(25000)
	la.update("phone", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act1", "acttok", now })
	_ = h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true, Island: IslandPrefs{Preset: "b"}})
	working := FleetState{Phase: "working", Working: 1}
	la.flush(working, false, false)
	c := f.calls(t, 1)
	if c[0].State.Right != SlotTPM || c[0].State.Stats == nil || c[0].State.Stats.TPM == 0 {
		t.Fatalf("state carries the TPM: %+v", c[0].State)
	}
	// The assistant starting is not on this device's island: no push.
	la.flush(FleetState{Phase: "working", Working: 1, Assistant: true}, false, false)
	// Numbers alone: not before live_metrics_seconds, and not when the rounded value is the same.
	report(50000)
	la.metrics(working, now.Add(10*time.Second))
	la.metrics(working, now.Add(61*time.Second))
	c = f.calls(t, 2)
	if c[1].Priority != "5" || c[1].State.Stats.TPM <= c[0].State.Stats.TPM {
		t.Fatalf("metrics push %+v %+v", c[1], c[1].State.Stats)
	}
	la.metrics(working, now.Add(200*time.Second))
	time.Sleep(50 * time.Millisecond)
	f.calls(t, 2)
}

func TestCoalescerNeedsYouBothWays(t *testing.T) {
	var c coalescer
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Kind: "working"}}}
	asks := FleetState{Phase: "needsYou", NeedsYou: 1, Working: 1,
		Items: []FleetItem{{ID: "m/b", Kind: proto.IxQuestion, Interaction: "ix1"}, {ID: "m/a", Kind: "working"}}}
	c.sentState(working)
	if now, _ := c.offer(asks); !now {
		t.Fatal("a request arriving goes out at once")
	}
	c.sentState(asks)
	// Answered (here or on the computer): the island must stop saying so at once, not after the gap.
	if now, _ := c.offer(working); !now {
		t.Fatal("a request answered goes out at once")
	}
	c.sentState(working)
	more := working
	more.Working = 2
	if now, sched := c.offer(more); now || !sched {
		t.Fatalf("an ordinary change waits: %v %v", now, sched)
	}
}

// A card with fields names no interaction on its row (the assistant's fleet_live.go): another such card of the same
// agent, after the first was answered, is still a new request and goes out at once.
func TestCoalescerNewCardWithFieldsOfTheSameAgent(t *testing.T) {
	var c coalescer
	first := FleetState{Phase: "needsYou", NeedsYou: 1, Items: []FleetItem{{ID: "m/p", Kind: proto.IxPermission, Since: 1761700000}}}
	c.sentState(first)
	second := first
	second.Items = []FleetItem{{ID: "m/p", Kind: proto.IxPermission, Since: 1761700042}}
	if now, sched := c.offer(second); !now {
		t.Fatalf("a new card goes out at once: now=%v schedule=%v", now, sched)
	}
}

// Past fleetMaxItems the items are the same three agents whether three or four need the user: a fourth arriving, or
// one past the three answered, changes only NeedsYou. The island's "N need you" must follow at once all the same
// (#4604), not after fleetMinGap at priority 5.
func TestCoalescerNeedsYouCountPastTheItems(t *testing.T) {
	var c coalescer
	three := FleetState{Phase: "needsYou", NeedsYou: 3, Items: []FleetItem{
		{ID: "m/a", Kind: proto.IxQuestion, Interaction: "ix1"},
		{ID: "m/b", Kind: proto.IxPermission, Interaction: "ix2"},
		{ID: "m/c", Kind: proto.IxPlan, Interaction: "ix3"},
	}}
	c.sentState(three)
	four := three
	four.NeedsYou = 4
	if now, sched := c.offer(four); !now {
		t.Fatalf("a fourth request goes out at once: now=%v schedule=%v", now, sched)
	}
	c.sentState(four)
	if now, sched := c.offer(three); !now {
		t.Fatalf("the fourth answered goes out at once: now=%v schedule=%v", now, sched)
	}
	c.sentState(three)
	busier := three
	busier.Working = 2
	if now, sched := c.offer(busier); now || !sched {
		t.Fatalf("an ordinary change waits: %v %v", now, sched)
	}
}

// fleetClock gives h.live a clock the test moves, and the device "phone" a running activity.
func fleetClock(t *testing.T) (*Hub, *fakeAPNs, *time.Time) {
	h, f := testLive(t)
	clock := time.Unix(1_800_000_000, 0)
	h.live.now = func() time.Time { return clock }
	h.live.update("phone", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act1", "acttok", clock })
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true}); err != nil {
		t.Fatal(err)
	}
	return h, f, &clock
}

func TestFleetMinGap(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	working := func(n int) FleetState {
		return FleetState{Phase: "working", Working: n, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}
	}
	t0 := *clock
	if next := la.flush(working(1), false, false); !next.IsZero() {
		t.Fatalf("first change waits for nothing: %v", next)
	}
	f.calls(t, 1)

	// An ordinary change 5 s later: its turn is fleetMinGap after the last push.
	*clock = t0.Add(5 * time.Second)
	if next := la.flush(working(2), false, false); !next.Equal(t0.Add(fleetMinGap)) {
		t.Fatalf("deferred until %v, want %v", next, t0.Add(fleetMinGap))
	}
	// A request arriving does not wait, and carries what changed meanwhile.
	*clock = t0.Add(8 * time.Second)
	asks := working(2)
	asks.Phase, asks.NeedsYou = "needsYou", 1
	asks.Items = append([]FleetItem{{ID: "m/b", Kind: proto.IxPermission, Interaction: "ix1"}}, asks.Items...)
	la.flush(asks, true, false)
	c := f.calls(t, 2)
	if c[1].Priority != "10" || c[1].State.NeedsYou != 1 || c[1].State.Working != 2 {
		t.Fatalf("request push %+v", c[1])
	}
	// Its turn come, the deferred device gets the state as it is then.
	*clock = t0.Add(8*time.Second + fleetMinGap)
	if next := la.flush(working(3), false, false); !next.IsZero() {
		t.Fatalf("due: %v", next)
	}
	c = f.calls(t, 3)
	if c[2].Priority != "5" || c[2].State.Working != 3 {
		t.Fatalf("ordinary push %+v", c[2])
	}
}

// TestFleetPacingBound runs the run loop's decisions (coalescer, flush and its next) over a fleet that changes every
// 3 s for 10 minutes, a request arriving and being answered now and then: every change reaches the device within
// fleetMinGap + fleetCoalesce (it, or a later state), a request arriving or answered at once, and far fewer pushes go
// out than there were changes.
func TestFleetPacingBound(t *testing.T) {
	h, _, clock := fleetClock(t)
	la := h.live
	start := *clock
	end := start.Add(10 * time.Minute)
	type change struct {
		at       time.Time
		critical bool
	}
	var changes []change
	var pushedAt []time.Time // each push, and the last change it carried
	var pushedUpTo []int
	var truth FleetState
	var due time.Time // the run loop's timer
	pushed := func() bool {
		la.mu.Lock()
		defer la.mu.Unlock()
		return la.sentAt["phone"].Equal(*clock)
	}
	flush := func(urgent bool) {
		if next := la.flush(truth, urgent, false); !next.IsZero() && (due.IsZero() || next.Before(due)) {
			due = next
		}
		if pushed() && (len(pushedAt) == 0 || !pushedAt[len(pushedAt)-1].Equal(*clock) || pushedUpTo[len(pushedUpTo)-1] < len(changes)-1) {
			pushedAt, pushedUpTo = append(pushedAt, *clock), append(pushedUpTo, len(changes)-1)
		}
	}
	for step := 0; clock.Before(end); step++ {
		truth = FleetState{Phase: "working", Working: 1 + step%4, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}
		if step%40 >= 20 && step%40 < 27 { // a request for 21 s, then answered
			truth.Phase, truth.NeedsYou = "needsYou", 1
			truth.Items = append([]FleetItem{{ID: "m/b", Kind: proto.IxQuestion, Interaction: fmt.Sprintf("ix%d", step/40)}}, truth.Items...)
		}
		now, schedule := la.coal.offer(truth)
		changes = append(changes, change{*clock, now})
		switch {
		case now:
			flush(true)
		case schedule:
			if at := clock.Add(fleetCoalesce); due.IsZero() || at.Before(due) {
				due = at
			}
		}
		next := clock.Add(3 * time.Second) // time passes to the next change, the timer going off on the way
		for !due.IsZero() && !due.After(next) {
			*clock, due = due, time.Time{}
			flush(false)
		}
		*clock = next
	}
	bound := fleetMinGap + fleetCoalesce
	worst := time.Duration(0)
	for i, c := range changes {
		if c.at.After(end.Add(-bound)) {
			break // still within its bound when the run ended
		}
		k := sort.Search(len(pushedUpTo), func(k int) bool { return pushedUpTo[k] >= i })
		if k == len(pushedUpTo) {
			t.Fatalf("change %d at %v never reached the device", i, c.at.Sub(start))
		}
		lag := pushedAt[k].Sub(c.at)
		if c.critical && lag != 0 {
			t.Fatalf("a request change at %v waited %v", c.at.Sub(start), lag)
		}
		worst = max(worst, lag)
	}
	if worst > bound {
		t.Fatalf("a change reached the device after %v, bound %v", worst, bound)
	}
	if len(pushedAt) > len(changes)/4 {
		t.Fatalf("%d pushes for %d changes: the gap does not hold", len(pushedAt), len(changes))
	}
	t.Logf("%d changes in 10 min: %d pushes, the slowest change after %v (bound %v)", len(changes), len(pushedAt), worst, bound)
}

func TestFleetOpenPhone(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}
	app := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, phone: true, pushDevice: "phone"}
	h.mu.Lock()
	h.clients[app] = struct{}{}
	h.mu.Unlock()

	// Open: the app draws the activity from the fleet event; no push, but the hub knows what it shows.
	la.broadcast(working)
	la.flush(working, true, false)
	time.Sleep(50 * time.Millisecond)
	if n := len(f.calls(t, 0)); n != 0 {
		t.Fatalf("pushed to an open app: %d", n)
	}
	// Gone to the background (a clean close): it shows the last fleet, nothing to send.
	app.clean = true
	h.dropClient(app)
	la.flush(working, false, false)
	time.Sleep(50 * time.Millisecond)
	if n := len(f.calls(t, 0)); n != 0 {
		t.Fatalf("pushed what the app already showed: %d", n)
	}
	la.mu.Lock()
	repair := la.repair
	la.mu.Unlock()
	if repair {
		t.Fatal("a clean close needs no repair")
	}

	// Open again, then the network drops (no clean close): what it shows is unknown, the state goes at once although
	// the last update was moments ago.
	app2 := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, phone: true, pushDevice: "phone"}
	h.mu.Lock()
	h.clients[app2] = struct{}{}
	h.mu.Unlock()
	more := working
	more.Working = 2
	la.broadcast(more)
	la.flush(more, false, false)
	*clock = clock.Add(time.Second)
	h.dropClient(app2)
	la.mu.Lock()
	repair = la.repair
	la.mu.Unlock()
	if !repair {
		t.Fatal("a dropped connection should repair the activity")
	}
	if next := la.flush(more, false, false); !next.IsZero() {
		t.Fatalf("repair waited for the gap: %v", next)
	}
	if c := f.calls(t, 1); c[0].State.Working != 2 {
		t.Fatalf("repair push %+v", c[0])
	}

	// A clean close long after the last fleet event: the activity may have gone stale, push it.
	app3 := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, phone: true, pushDevice: "phone", clean: true}
	h.mu.Lock()
	h.clients[app3] = struct{}{}
	h.mu.Unlock()
	*clock = clock.Add(fleetRefresh + time.Minute)
	h.dropClient(app3)
	la.mu.Lock()
	repair = la.repair
	la.mu.Unlock()
	if !repair {
		t.Fatal("a stale activity should be refreshed when the app leaves")
	}
}

func TestSessionsSentOnce(t *testing.T) {
	h := newTestHub(t)
	app := &client{out: make(chan []byte, 16), subs: map[[2]string]bool{}, role: roleFull}
	h.clients[app] = struct{}{}
	box, mini := &Machine{cfg: MachineConfig{ID: "box"}}, &Machine{cfg: MachineConfig{ID: "mini"}}
	a := []proto.Session{{SessionID: "s1", Status: proto.StatusWorking, LastText: "reading"}}
	b := []proto.Session{{SessionID: "s1", Status: proto.StatusWorking, LastText: "writing"}}
	h.broadcastSessions(box, a)
	h.broadcastSessions(box, a) // the same again (an interaction shown, a session seen): not sent
	h.broadcastSessions(mini, a)
	h.broadcastSessions(box, b)
	h.broadcastSessions(box, a) // back to the first: the apps have b, so it goes
	var got []string
	for len(app.out) > 0 {
		var m struct {
			Machine  string          `json:"machine_id"`
			Sessions []proto.Session `json:"sessions"`
		}
		_ = json.Unmarshal(<-app.out, &m)
		got = append(got, m.Machine+":"+m.Sessions[0].LastText)
	}
	if want := []string{"box:reading", "mini:reading", "box:writing", "box:reading"}; !slices.Equal(got, want) {
		t.Fatalf("sent %v, want %v", got, want)
	}
}

// TestFleetRefreshPerDevice: each device's activity is refreshed fleetRefresh after its own last push, so it never
// shows "waiting for the hub" while the hub is up (#4154). A change only another device shows (the assistant
// answering, on an island that does not show it) is no push for this one and must not put its refresh off.
func TestFleetRefreshPerDevice(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	h.push.register(Device{DeviceID: "pad", Name: "pad", APNSToken: "padtok", Topic: "app.example"})
	la.update("pad", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act2", "padact", *clock })
	if err := h.push.setPrefs("pad", NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true,
		Island: IslandPrefs{Preset: "custom", Left: SlotStatus, Right: SlotAssistant}}); err != nil {
		t.Fatal(err)
	}
	sentAt := func(device string) time.Time {
		la.mu.Lock()
		defer la.mu.Unlock()
		return la.sentAt[device]
	}
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}
	t0 := *clock
	reportLive(t, h, working, FleetStats{})
	la.flush(working, false, false)
	f.calls(t, 2)

	// 6 minutes on, the assistant answers: only the pad's island shows it.
	*clock = t0.Add(6 * time.Minute)
	answering := working
	answering.Assistant = true
	reportLive(t, h, answering, FleetStats{})
	la.flush(answering, false, false)
	f.calls(t, 3)
	if !sentAt("phone").Equal(t0) || !sentAt("pad").Equal(*clock) {
		t.Fatalf("after the assistant: phone %v, pad %v", sentAt("phone").Sub(t0), sentAt("pad").Sub(t0))
	}

	// The run loop's tick every 15 s: the phone is refreshed fleetRefresh after its own push, the pad after its own.
	refreshed := map[string]time.Time{}
	for *clock = t0.Add(6*time.Minute + 15*time.Second); !clock.After(t0.Add(fleetStaleIn + 2*time.Minute)); *clock = clock.Add(15 * time.Second) {
		la.tick()
		for _, d := range []string{"phone", "pad"} {
			if _, ok := refreshed[d]; !ok && sentAt(d).Equal(*clock) {
				refreshed[d] = *clock
			}
		}
	}
	for d, last := range map[string]time.Time{"phone": t0, "pad": t0.Add(6 * time.Minute)} {
		at, ok := refreshed[d]
		if !ok {
			t.Fatalf("%s never refreshed (last push at %v)", d, last.Sub(t0))
		}
		if wait := at.Sub(last); wait < fleetRefresh || wait > fleetRefresh+15*time.Second {
			t.Fatalf("%s refreshed %v after its last push, want about %v (stale after %v)", d, wait, fleetRefresh, fleetStaleIn)
		}
	}
	time.Sleep(50 * time.Millisecond)
	c := f.calls(t, 5)
	if len(c) != 5 {
		t.Fatalf("want one refresh per device, got %d pushes: %+v", len(c), c)
	}
	for _, x := range c[3:] {
		// Each device gets its own version: the pad's island shows the assistant, the phone's does not.
		if x.Event != "update" || x.Priority != "5" || x.State.Working != 1 || x.State.Assistant != (x.Token == "padact") {
			t.Fatalf("refresh %+v", x)
		}
	}
}

// TestFleetAssistantHoldsActivity: only the assistant answering is something to show on an island that shows it
// ("Thinking"): the hub pushes it there and does not end that activity while it answers, nor until fleetEndAfter after it
// stopped. A device whose island does not show the assistant ends as with an empty fleet, and none is started for it
// (#4398).
func TestFleetAssistantHoldsActivity(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	h.push.register(Device{DeviceID: "pad", Name: "pad", APNSToken: "padtok", Topic: "app.example"})
	la.update("pad", func(v *db.LiveActivity) { v.ActivityID, v.PushToken, v.StartedAt = "act2", "padact", *clock })
	showing := NotifyPrefs{Input: true, Done: DoneLong, LiveActivity: true, Island: IslandPrefs{Preset: "custom", Left: SlotStatus, Right: SlotAssistant}}
	if err := h.push.setPrefs("pad", showing); err != nil {
		t.Fatal(err)
	}
	// pushes counts the fake APNs calls to token with event, waiting up to 2 s for want of them (pushes go out on
	// their own goroutines). want 0 cannot be waited for: it gives a push in flight 50 ms to show up.
	pushes := func(token, event string, want int) (n int, last apnsCall) {
		count := func() (n int, last apnsCall) {
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, c := range f.got {
				if c.Token == token && c.Event == event {
					n, last = n+1, c
				}
			}
			return n, last
		}
		if want == 0 {
			time.Sleep(50 * time.Millisecond)
			return count()
		}
		for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(10 * time.Millisecond) {
			if n, last = count(); n >= want || time.Now().After(deadline) {
				return n, last
			}
		}
	}
	t0 := *clock
	la.flush(FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}, false, false)
	f.calls(t, 2)
	// A device that could have one push-started: whether only the assistant should start one is not decided yet.
	h.push.register(Device{DeviceID: "mini", Name: "mini", APNSToken: "minitok", Topic: "app.example"})
	la.update("mini", func(v *db.LiveActivity) { v.PushToStartToken = "minip2s" })
	if err := h.push.setPrefs("mini", showing); err != nil {
		t.Fatal(err)
	}

	// A minute on, the agent is done and seen, and the assistant answers: the pad shows it, the phone has nothing.
	*clock = t0.Add(time.Minute)
	answering := FleetState{Assistant: true}
	reportLive(t, h, answering, FleetStats{})
	la.flush(answering, false, false)
	if n, c := pushes("padact", "update", 2); n != 2 || !c.State.Assistant || c.State.Phase != "" {
		t.Fatalf("pad: %d updates, last %+v", n, c)
	}
	if n, _ := pushes("acttok", "update", 1); n != 1 {
		t.Fatalf("phone: %d updates, want only the first", n)
	}

	// It answers for a quarter of an hour: the phone's activity ends after fleetEndAfter, the pad's stays, refreshed.
	for *clock = clock.Add(15 * time.Second); clock.Before(t0.Add(time.Minute + fleetStaleIn)); *clock = clock.Add(15 * time.Second) {
		la.tick()
	}
	if n, _ := pushes("acttok", "end", 1); n != 1 {
		t.Fatalf("phone: %d ends, want 1", n)
	}
	if n, _ := pushes("padact", "end", 0); n != 0 || la.rows["pad"].PushToken != "padact" {
		t.Fatal("the pad's activity ended while the assistant answers")
	}
	if n, c := pushes("padact", "update", 3); n != 3 || !c.State.Assistant {
		t.Fatalf("pad: %d updates, want a refresh before it goes stale; last %+v", n, c)
	}
	if n, _ := pushes("minip2s", "start", 0); n != 0 {
		t.Fatal("only the assistant answering started an activity")
	}

	// It stops: the pad's activity ends fleetEndAfter later, as an emptied fleet's does.
	stopped := *clock
	reportLive(t, h, FleetState{}, FleetStats{})
	la.flush(FleetState{}, false, false)
	for ; clock.Before(stopped.Add(fleetEndAfter)); *clock = clock.Add(15 * time.Second) {
		la.tick()
	}
	if n, _ := pushes("padact", "end", 0); n != 0 {
		t.Fatal("the pad's activity ended at once when the assistant stopped")
	}
	la.tick()
	if n, _ := pushes("padact", "end", 1); n != 1 {
		t.Fatalf("pad: %d ends fleetEndAfter after the assistant stopped, want 1", n)
	}
}

// TestFleetBackgroundRefreshFromLastUpdate: an app that goes to the background may have left its activity's
// stale-date as an earlier update set it (it does not update for a fleet that changes nothing it shows, nor for a
// settings change), so the hub is only sure of fleetOpenFresh after the last fleet event: the refresh comes by then,
// not fleetRefresh after the event (#4294).
func TestFleetBackgroundRefreshFromLastUpdate(t *testing.T) {
	h, f, clock := fleetClock(t)
	la := h.live
	app := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, phone: true, pushDevice: "phone"}
	h.mu.Lock()
	h.clients[app] = struct{}{}
	h.mu.Unlock()
	sentAt := func() time.Time {
		la.mu.Lock()
		defer la.mu.Unlock()
		return la.sentAt["phone"]
	}

	// 0:00 the app drew a fleet that, as far as the hub knows, changed nothing: its stale-date may be as near as 5:00
	// (an earlier update at -10:00, LiveActivityController.worthUpdating).
	working := FleetState{Phase: "working", Working: 1, Items: []FleetItem{{ID: "m/a", Title: "api", Kind: "working"}}}
	t0 := *clock
	la.broadcast(working)
	la.flush(working, false, false)

	// 3:00 gone to the background: what it shows is known, nothing to send now.
	*clock = t0.Add(3 * time.Minute)
	app.clean = true
	h.dropClient(app)
	time.Sleep(50 * time.Millisecond)
	if n := len(f.calls(t, 0)); n != 0 {
		t.Fatalf("pushed what the app already showed: %d", n)
	}

	// The run loop's tick: the refresh comes before 5:00.
	var refreshed time.Time
	for *clock = clock.Add(15 * time.Second); !clock.After(t0.Add(fleetStaleIn)); *clock = clock.Add(15 * time.Second) {
		la.tick()
		if sentAt().Equal(*clock) {
			refreshed = *clock
			break
		}
	}
	if refreshed.IsZero() {
		t.Fatal("never refreshed")
	}
	if at := refreshed.Sub(t0); at >= 5*time.Minute {
		t.Fatalf("refreshed %v after the last fleet event, want before 5m0s (the stale-date it may have)", at)
	}
	if c := f.calls(t, 1); c[0].Event != "update" || c[0].State.Working != 1 {
		t.Fatalf("refresh %+v", c[0])
	}

	// Gone to the background fleetOpenFresh or more after the last fleet event: the state goes at once.
	app2 := &client{out: make(chan []byte, 64), subs: map[[2]string]bool{}, role: roleFull, phone: true, pushDevice: "phone", clean: true}
	h.mu.Lock()
	h.clients[app2] = struct{}{}
	h.mu.Unlock()
	la.broadcast(working)
	*clock = clock.Add(fleetOpenFresh)
	h.dropClient(app2)
	la.mu.Lock()
	repair := la.repair
	la.mu.Unlock()
	if !repair {
		t.Fatal("an activity that may be near its stale-date should be refreshed when the app leaves")
	}
}
