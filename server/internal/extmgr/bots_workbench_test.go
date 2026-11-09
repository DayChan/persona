package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The workbench's transport (#1961): feishu.pin and feishu.recall take only the bot's own messages in the owner's
// private chat, and an exclusive pin takes off the other pins the bot made there, never the owner's. All ids are
// placeholders.

// sentBy says GET /messages/:id answers that the message is in chat, sent by the app appID ("" a user).
func (f *fakeOpenAPI) sentBy(id, chat, appID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sender := map[string]any{"id": "ou_me", "id_type": "open_id", "sender_type": "user"}
	if appID != "" {
		sender = map[string]any{"id": appID, "id_type": "app_id", "sender_type": "app"}
	}
	f.read[id] = []map[string]any{{"message_id": id, "chat_id": chat, "sender": sender}}
}

func (w *botWorld) feishuErr(method string, params any) error {
	raw, _ := json.Marshal(params)
	_, _, err := w.h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppFeishu, Method: method, Params: raw})
	return err
}

func TestBotPinsAndWithdrawsOnlyItsOwnMessagesInTheOwnersChat(t *testing.T) {
	w := newBotWorld(t)
	w.pair(t)
	w.api.sentBy("om_wb", "oc_p2p", "cli_test")
	w.api.sentBy("om_said", "oc_p2p", "")            // the owner's own message
	w.api.sentBy("om_group", "oc_group", "cli_test") // the bot's, in another chat
	w.api.mu.Lock()
	w.api.pins = []map[string]any{
		{"message_id": "om_old", "operator_id": "cli_test", "operator_id_type": "app_id"}, // an older workbench
		{"message_id": "om_kept", "operator_id": "ou_me", "operator_id_type": "open_id"},  // the owner pinned it
		{"message_id": "om_wb", "operator_id": "cli_test", "operator_id_type": "app_id"},
	}
	w.api.mu.Unlock()

	n := w.api.total()
	w.feishu(t, assistantwire.FeishuPin, map[string]any{"message_id": "om_wb", "exclusive": true})
	w.api.find(t, "POST /open-apis/im/v1/pins", "om_wb")
	w.api.find(t, "DELETE /open-apis/im/v1/pins/om_old")
	if got := w.api.since(n); strings.Contains(got, "pins/om_kept") || strings.Contains(got, "DELETE /open-apis/im/v1/pins/om_wb") {
		t.Fatalf("took off a pin it should keep:\n%s", got)
	}

	for _, c := range []struct{ method, id string }{
		{assistantwire.FeishuPin, "om_said"}, {assistantwire.FeishuPin, "om_group"},
		{assistantwire.FeishuRecall, "om_said"}, {assistantwire.FeishuRecall, "om_group"}, {assistantwire.FeishuRecall, ""},
	} {
		n := w.api.total()
		if err := w.feishuErr(c.method, map[string]any{"message_id": c.id}); err == nil {
			t.Fatalf("%s %q: taken", c.method, c.id)
		}
		if got := w.api.since(n); strings.Contains(got, "POST /open-apis/im/v1/pins") || strings.Contains(got, "DELETE /open-apis/im/v1/messages/") {
			t.Fatalf("%s %q reached Feishu:\n%s", c.method, c.id, got)
		}
	}

	w.feishu(t, assistantwire.FeishuRecall, map[string]any{"message_id": "om_wb"})
	w.api.find(t, "DELETE /open-apis/im/v1/messages/om_wb")
}

// The switch is off by default and set through feishu.configure like the others.
func TestBotWorkbenchSwitchIsOffByDefault(t *testing.T) {
	w := newBotWorld(t)
	st := w.feishu(t, assistantwire.FeishuStatusGet, nil)
	if f, _ := st["features"].(map[string]any); f["workbench"] != false {
		t.Fatalf("features %v", st["features"])
	}
	w.feishu(t, assistantwire.FeishuConfigure, map[string]any{"features": map[string]bool{"workbench": true}})
	st = w.feishu(t, assistantwire.FeishuStatusGet, nil)
	if f, _ := st["features"].(map[string]any); f["workbench"] != true || f["main"] != true {
		t.Fatalf("features after switching the workbench on %v", st["features"])
	}
}
