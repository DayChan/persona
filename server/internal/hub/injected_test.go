package hub

import (
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestWithInjected(t *testing.T) {
	h := &Hub{}
	h.takeTodoReport(assistantwire.AssistantID, &assistantwire.TodoReport{Links: []assistantwire.TodoLink{
		{MachineID: "m1", SessionID: "a", TodoID: "td_1", Title: "跑测试", Task: true},
		{MachineID: "m1", SessionID: "b", TodoID: "td_1", Title: "跑测试", Task: true}, // one task, two agents
		{MachineID: "m1", SessionID: "c", TodoID: "td_2", Title: "重名", Task: true},
		{MachineID: "m1", SessionID: "d", TodoID: "td_3", Title: "重名", Task: true},
	}})
	notice := func(title string) string {
		return prompts.RenderTaskNotice(prompts.TaskNotice{Title: title, Machine: "电脑", State: "done", Text: "好了"})
	}
	items := []proto.Item{
		{ID: "1", Role: "user", Kind: proto.KindText, Text: notice("跑测试")},
		{ID: "2", Role: "user", Kind: proto.KindText, Text: notice("重名")},
		{ID: "3", Role: "user", Kind: proto.KindText, Text: notice("跑测试"), ClientMsgID: "c1"}, // the app sent it: the user's
		{ID: "4", Role: "assistant", Kind: proto.KindText, Text: notice("跑测试")},
		{ID: "5", Role: "user", Kind: proto.KindText, Text: "帮我看看"},
	}
	want := items[0].Text
	got := h.withInjected(items)
	if in := got[0].Injected; in == nil || in.Kind != proto.InjectedTaskNotice || in.TodoID != "td_1" || in.Line != "任务「跑测试」做完了" {
		t.Fatalf("notice: %+v", in)
	}
	if got[0].Text != want {
		t.Fatal("the text the model read changed")
	}
	if in := got[1].Injected; in == nil || in.TodoID != "" {
		t.Fatalf("two tasks with the title: %+v", in)
	}
	for _, i := range []int{2, 3, 4} {
		if got[i].Injected != nil {
			t.Fatalf("item %s marked: %+v", got[i].ID, got[i].Injected)
		}
	}
}

// The manager's note to a conversation an inbox item was handed to (#1194) is one line in the user's language; the
// same words sent by an app (client_msg_id) stay the user's.
func TestWithInjectedInboxNote(t *testing.T) {
	h := &Hub{}
	note := prompts.RenderInboxHandoffNote(prompts.InboxHandoffNote{ID: "lark:f1:100", Title: "报价", Note: "示例用户驳回了你的建议。"})
	got := h.withInjected([]proto.Item{
		{ID: "1", Role: "user", Kind: proto.KindText, Text: note},
		{ID: "2", Role: "user", Kind: proto.KindText, Text: note, ClientMsgID: "c1"},
	})
	if in := got[0].Injected; in == nil || in.Kind != proto.InjectedSystem || in.State != "inbox" || in.Title != "报价" || in.Line != "收件箱「报价」有了下文" {
		t.Fatalf("note: %+v", in)
	}
	if got[0].Text != note {
		t.Fatal("the text the model read changed")
	}
	if got[1].Injected != nil {
		t.Fatalf("an app's line marked: %+v", got[1].Injected)
	}
	for l, want := range map[i18n.Lang]string{i18n.En: "Inbox: an update on “报价”", i18n.Ja: "受信箱：「报价」に動きがありました"} {
		if line := injectedLine(l, got[0].Injected); line != want {
			t.Errorf("%s: %q, want %q", l, line, want)
		}
	}
}
