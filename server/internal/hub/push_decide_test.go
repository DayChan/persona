package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// A to-do whose conclusion waits for the owner's decision with a recommended option is pushed with the category whose OK
// takes it (#2056), and with what OK needs: the to-do, the round's asked_at, the recommendation.

// decideNotification is a "decide" push as the assistant sends it over the bus.
func decideNotification(t *testing.T) assistantwire.Notification {
	t.Helper()
	var n assistantwire.Notification
	if err := json.Unmarshal([]byte(`{"id":"ntf_1","kind":"decide","title":"结论：修登录崩溃","subtitle":"Persona",
		"body":"🤔 要你拍板：先灰度还是直接全量\n推荐：先灰度 10%","device_id":"m","session_id":"conv1",
		"todo":{"id":"todo_1","asked_at":"2026-11-09T05:06:07.123456789Z","recommend":"先灰度 10%"}}`), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

var decideTodo = map[string]any{"id": "todo_1", "asked_at": "2026-11-09T05:06:07.123456789Z", "recommend": "先灰度 10%", "manager": assistantwire.AssistantID}

// decideHub is a hub with one device, pushed directly (a fake APNs) and connected with ws_notify, taking "needs you"
// pushes or not.
func decideHub(t *testing.T, input bool) (*Hub, *client, *fakeAPNs) {
	t.Helper()
	f := &fakeAPNs{gone: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := &client{device: "phone", out: make(chan []byte, 8)}
	h := &Hub{clients: map[*client]struct{}{c: {}}, push: newPusher(t.TempDir(), nil, t.Logf)}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	h.push.direct = apns.NewWithKey(apnsConfig{KeyID: "K", TeamID: "T", Topic: "app.example"}, key)
	h.push.direct.Base = srv.URL
	h.push.register(Device{DeviceID: "phone", Name: "phone", APNSToken: "tok-phone", Topic: "app.example"})
	if err := h.push.setPrefs("phone", NotifyPrefs{Input: input, Done: DoneOff}); err != nil {
		t.Fatal(err)
	}
	h.onClientMessage(context.Background(), c, []byte(`{"type":"client_hello","features":["ws_notify"]}`))
	<-c.out // client_hello_ack
	return h, c, f
}

// On APNs: category TODO_DECIDE, time-sensitive, the assistant's words, and `todo` at the top level (the app's OK reads
// it from userInfo); the device that takes "needs you" pushes gets it.
func TestDecisionPushHasItsCategoryAndTodo(t *testing.T) {
	h, _, f := decideHub(t, true)
	if err := h.deliverPush(decideNotification(t)); err != nil {
		t.Fatal(err)
	}
	calls := f.calls(t, 1)
	var got struct {
		APS struct {
			Alert    map[string]string `json:"alert"`
			Category string            `json:"category"`
			Level    string            `json:"interruption-level"`
			Thread   string            `json:"thread-id"`
		} `json:"aps"`
		SessionID string         `json:"session_id"`
		Todo      map[string]any `json:"todo"`
	}
	if err := json.Unmarshal(calls[0].Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.APS.Category != "TODO_DECIDE" || got.APS.Level != "time-sensitive" || got.APS.Thread != "conv1" || got.SessionID != "conv1" {
		t.Fatalf("aps %+v session %q", got.APS, got.SessionID)
	}
	if got.APS.Alert["title"] != "结论：修登录崩溃" || got.APS.Alert["body"] != "🤔 要你拍板：先灰度还是直接全量\n推荐：先灰度 10%" {
		t.Fatalf("alert %v", got.APS.Alert)
	}
	if !reflect.DeepEqual(got.Todo, decideTodo) {
		t.Fatalf("todo %v, want %v", got.Todo, decideTodo)
	}
	// The next round of the same to-do takes the earlier one's place on the phone (one collapse id a to-do).
	next := decideNotification(t)
	next.Todo.AskedAt = next.Todo.AskedAt.Add(time.Hour)
	if err := h.deliverPush(next); err != nil {
		t.Fatal(err)
	}
	calls = f.calls(t, 2)
	if calls[0].Collapse == "" || calls[1].Collapse != calls[0].Collapse {
		t.Fatalf("collapse ids %q and %q", calls[0].Collapse, calls[1].Collapse)
	}
}

// A decision's push names the manager whose to-do it is, which OK names in ManagerHeader: another manager's, and the
// assistant's too (the default manager may be another one).
func TestDecisionPushNamesItsManager(t *testing.T) {
	n := decideNotification(t)
	if td, _ := decidePayloadIn("m-0b0b0b", "mb", "mb", "b1", n.Title, n.Body, *n.Todo)["todo"].(map[string]any); td["manager"] != "m-0b0b0b" {
		t.Fatalf("another manager's: %v", td)
	}
	if td, _ := decidePayloadIn(assistantwire.AssistantID, "m", "m", "conv1", n.Title, n.Body, *n.Todo)["todo"].(map[string]any); td["manager"] != assistantwire.AssistantID {
		t.Fatalf("the assistant's: %v", td)
	}
}

// On the WebSocket it is an "assistant" notify (a client that does not know decisions shows it as any other) with the
// same `todo`.
func TestDecisionPushOnTheWebSocket(t *testing.T) {
	h, c, _ := decideHub(t, true)
	if err := h.deliverPush(decideNotification(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-c.out:
		var got map[string]any
		_ = json.Unmarshal(raw, &got)
		if got["type"] != "notify" || got["kind"] != "assistant" || got["session_id"] != "conv1" || !reflect.DeepEqual(got["todo"], decideTodo) {
			t.Fatalf("notify %v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no notify on the WebSocket")
	}
}

// A device with "needs you" pushes off gets it neither way.
func TestDecisionPushTakesTheInputSwitch(t *testing.T) {
	h, c, f := decideHub(t, false)
	if err := h.deliverPush(decideNotification(t)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // sleep-ok: proving that no push goes out needs a quiet period; nothing can be polled for
	if n := len(f.calls(t, 0)); n != 0 {
		t.Fatalf("%d APNs pushes with needs-you off", n)
	}
	select {
	case raw := <-c.out:
		t.Fatalf("notify with needs-you off: %s", raw)
	default:
	}
}

// A decision's push without its to-do, or without the option, is refused: OK would have nothing to take.
func TestDecisionPushNeedsItsTodo(t *testing.T) {
	h, _, _ := decideHub(t, true)
	n := decideNotification(t)
	noOption := n
	noOption.Todo = &assistantwire.NotifyTodo{ID: n.Todo.ID, AskedAt: n.Todo.AskedAt}
	if err := h.deliverPush(noOption); err == nil {
		t.Fatal("a decide push without its option was taken")
	}
	raw, _ := json.Marshal(n)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	delete(m, "todo")
	raw, _ = json.Marshal(m)
	var bare assistantwire.Notification
	_ = json.Unmarshal(raw, &bare)
	if err := h.deliverPush(bare); err == nil {
		t.Fatal("a decide push without its to-do was taken")
	}
}
