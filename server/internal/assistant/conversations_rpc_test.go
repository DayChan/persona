package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// assistant.conversations / assistant.messages (conversations_rpc.go, #1288): what an extension granted
// assistant.read reads of the assistant's conversations.

var convDay = time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)

// conversationsService is an assistant on "server" with its own conversations (main, work, an archived one, a
// guest's), the health space's, and an agent's session; f.items is what every session.items answers.
func conversationsService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := healthHub()
	at := func(h int) time.Time { return convDay.Add(time.Duration(h) * time.Hour) }
	f.sessions["server"] = []proto.Session{
		{MachineID: "server", SessionID: "main-0000-0000", Cwd: "/home/me/.persona/assistant", Assistant: true, Title: "Persona", UpdatedAt: at(5)},
		{MachineID: "server", SessionID: "work-0000-0000", Cwd: "/home/me/.persona/assistant", Assistant: true, Title: "0102 | 研究 | 一件事", UpdatedAt: at(3), Origin: OriginTask},
		{MachineID: "server", SessionID: "old0-0000-0000", Cwd: "/home/me/.persona/assistant", Assistant: true, Title: "assistant", UpdatedAt: at(-48)},
		{MachineID: "server", SessionID: "arch-0000-0000", Cwd: "/home/me/.persona/assistant", Assistant: true, Archived: true, UpdatedAt: at(2)},
		{MachineID: "server", SessionID: "gues-0000-0000", Cwd: "/home/me/.persona/assistant", Assistant: true, UpdatedAt: at(4)},
		{MachineID: "server", SessionID: "slee-0000-0000", Cwd: "/home/me/.persona/health", Assistant: true, Space: SpaceHealth, UpdatedAt: at(4)},
		{MachineID: "server", SessionID: "agen-0000-0000", Cwd: "/home/me/code", UpdatedAt: at(4)},
	}
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	if err := s.guestSet(context.Background(), "server", "gues-0000-0000", []string{"lark_agent_reply"}); err != nil {
		t.Fatal(err)
	}
	return s, f
}

func askConversations(t *testing.T, s *Service, method, params string, out any) error {
	t.Helper()
	res, ok, err := s.conversationsRequest(context.Background(), method, json.RawMessage(params))
	if !ok {
		t.Fatalf("%s not answered", method)
	}
	if err != nil {
		return err
	}
	b, _ := json.Marshal(res)
	return json.Unmarshal(b, out)
}

func convCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

func TestExtensionConversationsListOnlyTheAssistantsOwn(t *testing.T) {
	s, _ := conversationsService(t)
	var res assistantwire.AssistantConversationsResult
	if err := askConversations(t, s, assistantwire.MethodAssistantConversations, `{"since":"2026-01-02T00:00:00Z"}`, &res); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range res.Items {
		ids = append(ids, c.ID)
	}
	// Newest first; not the archived one, the guest's, the health space's, an agent's, or one idle since before.
	if got := strings.Join(ids, ","); got != "main-0000-0000,work-0000-0000" || res.Total != 2 || res.NextOffset != nil {
		t.Fatalf("%s %+v", got, res)
	}
	if m := res.Items[0]; !m.Main || m.Title != "Persona" || m.Origin != OriginUser {
		t.Fatalf("main %+v", m)
	}
	if w := res.Items[1]; w.Main || w.Origin != OriginTask {
		t.Fatalf("work %+v", w)
	}
	res = assistantwire.AssistantConversationsResult{}
	if err := askConversations(t, s, assistantwire.MethodAssistantConversations, `{"since":"2026-01-02T00:00:00Z","include_archived":true,"limit":1,"offset":1}`, &res); err != nil {
		t.Fatal(err)
	}
	if res.Total != 3 || len(res.Items) != 1 || res.Items[0].ID != "work-0000-0000" || res.NextOffset == nil || *res.NextOffset != 2 {
		t.Fatalf("page %+v", res)
	}
	// With until, a conversation known to have started at or after it is left out (work started at 11:00).
	s.noteTitle("server", "work-0000-0000", convDay.Add(2*time.Hour), "研究", "一件事")
	for until, want := range map[string]string{"2026-01-02T10:59:00Z": "main-0000-0000", "2026-01-02T11:00:00Z": "main-0000-0000",
		"2026-01-02T11:00:01Z": "main-0000-0000,work-0000-0000"} {
		var r assistantwire.AssistantConversationsResult
		if err := askConversations(t, s, assistantwire.MethodAssistantConversations, `{"since":"2026-01-01T00:00:00Z","until":"`+until+`"}`, &r); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range r.Items {
			got = append(got, c.ID)
			if c.ID == "work-0000-0000" && (c.Started == nil || !c.Started.Equal(convDay.Add(2*time.Hour))) {
				t.Fatalf("started %+v", c)
			}
		}
		if strings.Join(got, ",") != want {
			t.Errorf("until %s: %v, want %s", until, got, want)
		}
	}
}

func convItem(seq int, role, kind, text string) proto.Item {
	return proto.Item{ID: "i" + string(rune('a'+seq)), Seq: seq, TS: convDay.Add(time.Duration(seq) * time.Minute), Role: role, Kind: kind, Text: text}
}

func TestExtensionMessagesKeepOnlyWords(t *testing.T) {
	s, f := conversationsService(t)
	from := convItem(8, "user", proto.KindText, "来自另一个会话的话")
	from.From = "other-session"
	sent := convItem(9, "user", proto.KindText, prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{{ID: "todo_1", Title: "部署", Kind: "review"}}}))
	sent.ClientMsgID = "" // typed in by Persona
	long := convItem(10, "assistant", proto.KindText, "很长的回答")
	long.FullSize = 90000
	f.items = []proto.Item{
		convItem(1, "user", proto.KindText, "<persona-context kind=\"link\" title=\"一页\">页面内容</persona-context>\n我觉得应该先写测试"),
		convItem(2, "assistant", proto.KindThinking, "想一想"),
		convItem(3, "assistant", proto.KindToolUse, ""),
		convItem(4, "user", proto.KindToolResult, "工具输出"),
		convItem(5, "assistant", proto.KindText, "好的，先写测试。"),
		convItem(6, "user", proto.KindText, "/compact"),
		convItem(7, "assistant", proto.KindNotice, "压缩了上下文"),
		from, sent, long,
		convItem(11, "user", proto.KindText, "<system-reminder>提醒</system-reminder>"),
	}
	var res assistantwire.AssistantMessagesResult
	if err := askConversations(t, s, assistantwire.MethodAssistantMessages, `{"id":"main-0000-0000"}`, &res); err != nil {
		t.Fatal(err)
	}
	want := []assistantwire.AssistantMessage{
		{Seq: 1, At: convDay.Add(time.Minute), Role: "user", Text: "我觉得应该先写测试"},
		{Seq: 5, At: convDay.Add(5 * time.Minute), Role: "assistant", Text: "好的，先写测试。"},
		{Seq: 10, At: convDay.Add(10 * time.Minute), Role: "assistant", Text: "很长的回答", Truncated: true},
	}
	if len(res.Messages) != len(want) || res.NextSeq != nil {
		t.Fatalf("%+v", res)
	}
	for i := range want {
		if !res.Messages[i].At.Equal(want[i].At) || res.Messages[i].Seq != want[i].Seq || res.Messages[i].Role != want[i].Role ||
			res.Messages[i].Text != want[i].Text || res.Messages[i].Truncated != want[i].Truncated {
			t.Fatalf("%d: %+v, want %+v", i, res.Messages[i], want[i])
		}
	}
	// A time range.
	res = assistantwire.AssistantMessagesResult{}
	if err := askConversations(t, s, assistantwire.MethodAssistantMessages, `{"id":"main-0000-0000","since":"2026-01-02T09:02:00Z","until":"2026-01-02T09:09:00Z"}`, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Seq != 5 || res.NextSeq != nil {
		t.Fatalf("range %+v", res)
	}
}

func TestExtensionMessagesPageBySeq(t *testing.T) {
	s, f := conversationsService(t)
	f.itemsPage = 3
	for i := 1; i <= 12; i++ {
		role := "user"
		if i%2 == 0 {
			role = "assistant"
		}
		f.items = append(f.items, convItem(i, role, proto.KindText, "第几句"))
	}
	var got []int
	after := -1
	for range 10 {
		params := `{"id":"main-0000-0000","limit":5}`
		if after >= 0 {
			b, _ := json.Marshal(map[string]any{"id": "main-0000-0000", "limit": 5, "after_seq": after})
			params = string(b)
		}
		var res assistantwire.AssistantMessagesResult
		if err := askConversations(t, s, assistantwire.MethodAssistantMessages, params, &res); err != nil {
			t.Fatal(err)
		}
		for _, m := range res.Messages {
			got = append(got, m.Seq)
		}
		if res.NextSeq == nil {
			break
		}
		after = *res.NextSeq
	}
	if len(got) != 12 || got[0] != 1 || got[11] != 12 {
		t.Fatalf("paged %v", got)
	}
}

func TestExtensionMessagesRefuse(t *testing.T) {
	s, _ := conversationsService(t)
	var res assistantwire.AssistantMessagesResult
	for id, code := range map[string]string{"slee-0000-0000": assistantwire.CodeConversationNotFound, "gues-0000-0000": assistantwire.CodeConversationNotFound,
		"agen-0000-0000": assistantwire.CodeConversationNotFound, "nope": assistantwire.CodeConversationNotFound} {
		if err := askConversations(t, s, assistantwire.MethodAssistantMessages, `{"id":"`+id+`"}`, &res); convCode(err) != code {
			t.Errorf("%s: %v", id, err)
		}
	}
	for _, p := range []string{`{}`, `{"id":"main-0000-0000","limit":501}`, `{"id":"main-0000-0000","after_seq":-1}`, `{"id":"main-0000-0000","since":"yesterday"}`, `{"id":"main-0000-0000","text":"x"}`} {
		if err := askConversations(t, s, assistantwire.MethodAssistantMessages, p, &res); convCode(err) != bus.CodeInvalidParams {
			t.Errorf("%s: %v", p, err)
		}
	}
	var list assistantwire.AssistantConversationsResult
	for _, p := range []string{`{}`, `{"since":"2026-01-02"}`, `{"since":"2026-01-02T00:00:00Z","limit":201}`, `{"since":"2026-01-02T00:00:00Z","space":"health"}`} {
		if err := askConversations(t, s, assistantwire.MethodAssistantConversations, p, &list); convCode(err) != bus.CodeInvalidParams {
			t.Errorf("%s: %v", p, err)
		}
	}
	if _, ok, _ := s.conversationsRequest(context.Background(), "assistant.send", nil); ok {
		t.Fatal("another method answered here")
	}
}

// A long stretch without words (an agent's tool calls) ends a call with no messages but next_seq, until the words.
func TestExtensionMessagesScanOnPastToolWork(t *testing.T) {
	s, f := conversationsService(t)
	f.itemsPage = 2
	for i := 1; i <= 2*messagesScanPages+5; i++ {
		f.items = append(f.items, convItem(i, "assistant", proto.KindToolUse, ""))
	}
	last := 2*messagesScanPages + 6
	f.items = append(f.items, convItem(last, "user", proto.KindText, "做完了吗"))
	var res assistantwire.AssistantMessagesResult
	if err := askConversations(t, s, assistantwire.MethodAssistantMessages, `{"id":"main-0000-0000"}`, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 0 || res.NextSeq == nil || *res.NextSeq != 2*messagesScanPages {
		t.Fatalf("first call %+v", res)
	}
	b, _ := json.Marshal(map[string]any{"id": "main-0000-0000", "after_seq": *res.NextSeq})
	res = assistantwire.AssistantMessagesResult{}
	if err := askConversations(t, s, assistantwire.MethodAssistantMessages, string(b), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Seq != last || res.NextSeq != nil {
		t.Fatalf("second call %+v", res)
	}
}
