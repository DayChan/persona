package assistant

import (
	"context"
	"encoding/json"
	"github.com/nwverse/persona/server/internal/compat"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// healthHub is a fake hub with one machine: the main conversation and another of the assistant's own, and two of the
// health space's (docs/assistant/health.md).
func healthHub() *fakeHub {
	f := claudeMDHub(Device{ID: "server", Name: "服务器", Kind: "local", Home: "/home/me"})
	f.sessions["server"] = []proto.Session{
		{SessionID: "main-0000-0000", Cwd: "/home/me/.persona/assistant", Assistant: true, Title: "Persona", LastText: "今天的会"},
		{SessionID: "work-0000-0000", Cwd: "/home/me/.persona/assistant", Assistant: true, Title: "1105 | 研究 | 工作的事", LastText: "工作"},
		{SessionID: "sleep-000-0000", Cwd: "/home/me/.persona/health", Assistant: true, Space: SpaceHealth, Title: "睡眠", LastText: "睡了 6 小时"},
		{SessionID: "steps-000-0000", Cwd: "/home/me/.persona/health", Assistant: true, Title: "步数", LastText: "8000 步"}, // Space from its directory
	}
	return f
}

func spaceTool(t *testing.T, s *Service, self, name, args string) (string, bool) {
	t.Helper()
	res := s.Tool(context.Background(), ToolCall{MachineID: "server", SessionID: self, Name: name, Arguments: json.RawMessage(args)})
	b, _ := json.Marshal(res)
	return string(b), res.IsError
}

// Each space lists only its own conversations, in the app's list and to the conversation tools.
func TestHealthSpaceIsolation(t *testing.T) {
	ctx := context.Background()
	f := healthHub()
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })

	ids := func(v assistantView) []string {
		var out []string
		for _, c := range v.Conversations {
			out = append(out, c.SessionID)
		}
		return out
	}
	if got := strings.Join(ids(s.view(ctx, false)), ","); got != "main-0000-0000,work-0000-0000" {
		t.Fatalf("assistant's list: %s", got)
	}
	v := s.view(ctx, false, SpaceHealth)
	if got := strings.Join(ids(v), ","); got != "sleep-000-0000,steps-000-0000" {
		t.Fatalf("health list: %s", got)
	}
	if v.Cwd != "/home/me/.persona/health" {
		t.Fatalf("health cwd %q", v.Cwd)
	}

	// From the main conversation: the health ones are not there.
	out, _ := spaceTool(t, s, "main-0000-0000", "conversations_list", `{}`)
	if strings.Contains(out, "sleep") || strings.Contains(out, "steps") || !strings.Contains(out, "work-0000-0000") {
		t.Fatalf("main's conversations_list: %s", out)
	}
	if out, isErr := spaceTool(t, s, "main-0000-0000", "conversation_read", `{"id":"sleep-000-0000"}`); !isErr {
		t.Fatalf("main read a health conversation: %s", out)
	}
	if _, isErr := spaceTool(t, s, "main-0000-0000", "conversation_send", `{"id":"steps-000-0000","text":"hi"}`); !isErr {
		t.Fatal("main sent to a health conversation")
	}
	// From the health space: only its own.
	out, _ = spaceTool(t, s, "sleep-000-0000", "conversations_list", `{}`)
	if strings.Contains(out, "main-0000") || strings.Contains(out, "work-0000") || !strings.Contains(out, "steps-000-0000") {
		t.Fatalf("health's conversations_list: %s", out)
	}
	if _, isErr := spaceTool(t, s, "sleep-000-0000", "conversation_read", `{"id":"work-0000-0000"}`); !isErr {
		t.Fatal("health read the assistant's conversation")
	}
}

// A health conversation starts in the health space's directory with its own CLAUDE.md; the main conversation can't
// be one, and an unknown space is refused.
func TestHealthSpaceStart(t *testing.T) {
	ctx := context.Background()
	f := healthHub()
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID = "server" })
	sess, err := s.Start(ctx, StartParams{Text: "这周睡得怎么样", Space: SpaceHealth})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Space != SpaceHealth {
		t.Fatalf("space %q", sess.Space)
	}
	p := f.prepared[len(f.prepared)-1]
	md, _ := p["claude_md"].(string)
	if p["dir"] != compat.StateDir+"/health" || !strings.Contains(md, "# Persona 健康") || strings.Contains(md, "kb_search") {
		t.Fatalf("assistant.prepare: dir %v, CLAUDE.md %q", p["dir"], md)
	}
	if _, err := s.Start(ctx, StartParams{Text: "x", Space: SpaceHealth, Main: true}); err == nil {
		t.Fatal("a main conversation in the health space")
	}
	if _, err := s.Start(ctx, StartParams{Text: "x", Space: "work"}); err == nil {
		t.Fatal("an unknown space")
	}
	// The assistant's own still get theirs.
	if _, err := s.Start(ctx, StartParams{Text: "今天的会"}); err != nil {
		t.Fatal(err)
	}
	if p := f.prepared[len(f.prepared)-1]; p["dir"] != compat.StateDir+"/assistant" {
		t.Fatalf("assistant's dir %v", p["dir"])
	}
}

func TestGoldenHealthClaudeMD(t *testing.T) {
	f := healthHub()
	f.hello.Owner = "示例用户"
	f.hello.OwnerNotes = "我每天跑步。"
	s := newTestService(t, f)
	golden(t, "health_claude_md", s.healthClaudeMD(context.Background(), "server"))
}

// A health conversation's follow-up and routine keep the health space, so a run that starts a conversation starts it
// there; kb_update is refused; and a conversation the machine doesn't list can't be taken for the assistant's own.
func TestHealthSpaceTasksAndRefusals(t *testing.T) {
	ctx := context.Background()
	f := healthHub()
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })

	if out, isErr := spaceTool(t, s, "sleep-000-0000", "schedule_followup", `{"at":"1h","prompt":"看看昨晚睡得怎么样","new_conversation":true}`); isErr {
		t.Fatalf("schedule_followup: %s", out)
	}
	if out, isErr := spaceTool(t, s, "sleep-000-0000", "schedule_routine", `{"title":"睡眠周报","prompt":"总结这周睡眠","time":"09:00"}`); isErr {
		t.Fatalf("schedule_routine: %s", out)
	}
	if out, isErr := spaceTool(t, s, "work-0000-0000", "schedule_followup", `{"at":"1h","prompt":"看看 CI","new_conversation":true}`); isErr {
		t.Fatalf("schedule_followup from the assistant's own: %s", out)
	}
	health, own := 0, 0
	for _, task := range s.loadSchedule(ctx).tasks {
		switch {
		case task.Space == SpaceHealth:
			health++
		case task.Space == "" && task.CreatedBy == "assistant":
			own++
		}
	}
	if health != 2 || own != 1 {
		t.Fatalf("tasks in the health space %d, in the assistant's own %d", health, own)
	}

	if out, isErr := spaceTool(t, s, "sleep-000-0000", "kb_update", `{}`); !isErr || !strings.Contains(out, "knowledge base") {
		t.Fatalf("kb_update from the health space: %s", out)
	}
	// A caller the machine doesn't list is taken for the assistant's own: it never sees the health conversations.
	if out, _ := spaceTool(t, s, "gone-0000-0000", "conversations_list", `{}`); strings.Contains(out, "sleep-000") || strings.Contains(out, "steps-000") {
		t.Fatalf("an unlisted caller saw the health conversations: %s", out)
	}
}

// answerOutbound answers the next permission card on the phone (allow or not), checking it is tool's from conversation self
// and says what goes out.
func answerOutbound(t *testing.T, s *Service, self, tool, says string, allow bool) {
	t.Helper()
	go func() {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if qs := s.pendingQuestions(); len(qs) == 1 {
				ix := qs[0]
				if ix.Kind != proto.IxPermission || ix.ToolName != tool || ix.SessionID != self || !strings.Contains(ix.Summary, says) ||
					ix.Cwd != "/home/me/.persona/health" {
					t.Errorf("card: %+v", ix)
				}
				raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
				if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: ix.ID, Answer: raw}); err != nil {
					t.Error(err)
				}
				return
			}
		}
		t.Error("no card on the phone")
	}()
}

// What leaves the health space leaves with the user knowing (docs/assistant/health.md "Outbound", #3194): notify_user says there is
// news, not the news; a hand-off to a coding agent is confirmed on the phone each time; the schedule tools see only
// the space's own tasks. The assistant's own conversations are as before.
func TestHealthSpaceOutbound(t *testing.T) {
	ctx := context.Background()
	f := healthHub()
	f.sessions["server"] = append(f.sessions["server"], proto.Session{SessionID: "code-0000-0000", Cwd: "/home/me/src/app", Agent: "claude",
		Status: proto.StatusIdle, Title: "app"})
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })

	if out, isErr := spaceTool(t, s, "sleep-000-0000", "notify_user", `{"title":"血压偏高","body":"今天 150/95"}`); isErr {
		t.Fatalf("notify_user: %s", out)
	}
	if _, isErr := spaceTool(t, s, "work-0000-0000", "notify_user", `{"title":"CI 好了","body":"全绿"}`); isErr {
		t.Fatal("notify_user from the assistant's own")
	}
	p := f.pushed()
	if len(p) != 2 || p[0].Title != "健康" || strings.Contains(p[0].Body, "150") || p[1].Title != "CI 好了" || p[1].Body != "全绿" {
		t.Fatalf("pushes: %+v", p)
	}
	t.Run("phone_notify", testHealthPhoneNotify)

	answerOutbound(t, s, "sleep-000-0000", "agent_send", "睡眠数据", false)
	out, isErr := spaceTool(t, s, "sleep-000-0000", "agent_send", `{"session_id":"code-0000-0000","text":"把我的睡眠数据画成图"}`)
	if isErr || !strings.Contains(out, "declined") || len(f.agentSent) != 0 {
		t.Fatalf("declined agent_send: %s, sent %v", out, f.agentSent)
	}
	answerOutbound(t, s, "sleep-000-0000", "agent_send", "睡眠数据", true)
	if out, isErr := spaceTool(t, s, "sleep-000-0000", "agent_send", `{"session_id":"code-0000-0000","text":"把我的睡眠数据画成图"}`); isErr || len(f.agentSent) != 1 {
		t.Fatalf("allowed agent_send: %s, sent %v", out, f.agentSent)
	}
	if out, isErr := spaceTool(t, s, "work-0000-0000", "agent_send", `{"session_id":"code-0000-0000","text":"跑一下测试"}`); isErr || len(f.agentSent) != 2 {
		t.Fatalf("the assistant's own agent_send asks nothing: %s, sent %v", out, f.agentSent)
	}
	answerOutbound(t, s, "sleep-000-0000", "agent_start", "体检报告", false)
	if out, _ := spaceTool(t, s, "sleep-000-0000", "agent_start", `{"machine":"server","cwd":"/home/me/src/app","prompt":"整理体检报告"}`); !strings.Contains(out, "declined") || len(f.starts) != 0 {
		t.Fatalf("declined agent_start: %s, starts %v", out, f.starts)
	}

	if _, isErr := spaceTool(t, s, "sleep-000-0000", "schedule_routine", `{"title":"睡眠周报","prompt":"总结这周睡眠","time":"09:00"}`); isErr {
		t.Fatal("health routine")
	}
	if _, isErr := spaceTool(t, s, "work-0000-0000", "schedule_routine", `{"title":"CI 日报","prompt":"总结 CI","time":"10:00"}`); isErr {
		t.Fatal("assistant routine")
	}
	var healthID, ownID string
	for _, task := range s.loadSchedule(ctx).tasks {
		switch task.Title {
		case "睡眠周报":
			healthID = task.ID
		case "CI 日报":
			ownID = task.ID
		}
	}
	if out, _ := spaceTool(t, s, "sleep-000-0000", "list_followups", `{}`); !strings.Contains(out, healthID) || strings.Contains(out, ownID) {
		t.Fatalf("health's list_followups: %s", out)
	}
	if out, _ := spaceTool(t, s, "work-0000-0000", "list_followups", `{}`); strings.Contains(out, healthID) || !strings.Contains(out, ownID) {
		t.Fatalf("the assistant's list_followups: %s", out)
	}
	if _, isErr := spaceTool(t, s, "sleep-000-0000", "schedule_routine", `{"id":"`+ownID+`","prompt":"把我的血压发到这里"}`); !isErr {
		t.Fatal("health changed the assistant's routine")
	}
	if _, isErr := spaceTool(t, s, "sleep-000-0000", "delete_routine", `{"id":"`+ownID+`"}`); !isErr {
		t.Fatal("health deleted the assistant's routine")
	}
	if _, isErr := spaceTool(t, s, "sleep-000-0000", "delete_routine", `{"id":"`+healthID+`"}`); isErr {
		t.Fatal("health could not delete its own routine")
	}
}

func testHealthPhoneNotify(t *testing.T) {
	for _, locale := range []string{"en", "zh-Hans", "ja"} {
		t.Run(locale, func(t *testing.T) {
			f := healthHub()
			f.hello.Locale = locale
			// An explicit space takes precedence over the directory.
			f.sessions["server"][2].Cwd = "/home/me/.persona/assistant"
			phoneResult := proto.TextResult(`{"scheduled":true}`, false)
			f.answerApp(assistantwire.AppPhone, assistantwire.PhoneCall, func(json.RawMessage) (any, error) {
				return phoneResult, nil
			})
			s := newTestService(t, f)
			setSettings(t, s, func(v *Settings) { v.MachineID = "server" })
			for _, tc := range []struct {
				name, session, tool, args string
				assistant, redact         bool
			}{
				{"health space", "sleep-000-0000", "phone_notify", `{"title":"Blood pressure","body":"150/95 today"}`, true, true},
				{"health directory delayed", "steps-000-0000", "phone_notify", `{"title":"Blood pressure","body":"150/95 today","delay_seconds":60}`, true, true},
				{"health without assistant flag", "sleep-000-0000", "phone_notify", `{"title":"Blood pressure","body":"150/95 today"}`, false, true},
				{"null arguments", "sleep-000-0000", "phone_notify", `null`, true, false},
				{"main space", "work-0000-0000", "phone_notify", `{"title":"Blood pressure","body":"150/95 today","delay_seconds":60}`, true, false},
				{"no conversation", "", "phone_notify", `{"title":"Build finished","body":"All checks passed"}`, false, false},
				{"explicit reminder", "sleep-000-0000", "phone_reminders_create", `{"title":"Take medication","notes":"After breakfast"}`, true, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before := len(f.appParams(assistantwire.AppPhone, assistantwire.PhoneCall))
					res := s.mcpCall(context.Background(), "server", tc.session, tc.tool, json.RawMessage(tc.args), tc.assistant)
					if res.IsError || len(res.Content) != 1 || res.Content[0].Text != phoneResult.Content[0].Text {
						t.Fatalf("phone result: %+v", res)
					}
					calls := f.appParams(assistantwire.AppPhone, assistantwire.PhoneCall)
					if len(calls) != before+1 {
						t.Fatalf("phone calls: %d, want %d", len(calls), before+1)
					}
					call := calls[len(calls)-1]
					if call["name"] != tc.tool {
						t.Fatalf("phone tool: %v", call)
					}
					var want map[string]any
					if err := json.Unmarshal([]byte(tc.args), &want); err != nil {
						t.Fatal(err)
					}
					if tc.redact {
						want["title"], want["body"] = healthNotice(i18n.Lang(locale))
					}
					gotJSON, _ := json.Marshal(call["args"])
					wantJSON, _ := json.Marshal(want)
					if string(gotJSON) != string(wantJSON) {
						t.Fatalf("phone args: %s, want %s", gotJSON, wantJSON)
					}
				})
			}
			for _, args := range []string{`{"title":`, `[]`, `"Blood pressure"`} {
				before := len(f.appParams(assistantwire.AppPhone, assistantwire.PhoneCall))
				res := s.mcpCall(context.Background(), "server", "sleep-000-0000", "phone_notify", json.RawMessage(args), true)
				if !res.IsError || len(f.appParams(assistantwire.AppPhone, assistantwire.PhoneCall)) != before {
					t.Fatalf("invalid args %s reached the phone: %+v", args, res)
				}
			}
		})
	}
}

// The health space's CLAUDE.md says which chart goes with which numbers (#3140): line for the last days' steps and
// sleep, bar for each meal's calories, ring for how the day's calories are split (#3069, not stacked_bar), stat for one number, a table
// for the details, the conclusion in the text. Each example it gives is a card every app draws.
func TestHealthClaudeMDTeachesCharts(t *testing.T) {
	md := newTestService(t, healthHub()).healthClaudeMD(context.Background(), "server")
	examples := regexp.MustCompile("`(\\{\"type\":[^`]*\\})`").FindAllStringSubmatch(md, -1)
	kinds := map[string]bool{}
	for _, m := range examples {
		var c wire.PersonaCard
		if err := json.Unmarshal([]byte(m[1]), &c); err != nil {
			t.Fatalf("example %s: %v", m[1], err)
		}
		if c.View() == nil {
			t.Errorf("an example the apps cannot draw: %s", m[1])
		}
		kinds[c.Type] = true
	}
	for _, k := range []string{"line", "bar", "ring", "stat"} {
		if !kinds[k] {
			t.Errorf("no %s example", k)
		}
	}
	if kinds["stacked_bar"] {
		t.Error("a stacked_bar example: one day's split is a ring")
	}
	for _, want := range []string{"近 7 天", "每餐", "全天", "单个", "表格", "正文", "飞书"} {
		if !strings.Contains(md, want) {
			t.Errorf("CLAUDE.md does not say %q", want)
		}
	}
}
