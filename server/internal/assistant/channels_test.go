package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The channels' pipeline end to end (the hub's transport, the bus, the assistant) is the hub's channels_test.go and
// bots_test.go; these are the assistant's own parts.

func TestResolutionText(t *testing.T) {
	yes, no := true, false
	q := proto.Interaction{Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "a"}, {Question: "b"}}}
	for _, c := range []struct {
		ix   proto.Interaction
		r    proto.Resolution
		want string
	}{
		{q, proto.Resolution{Answers: map[string]string{"a": "1", "b": "2"}}, "已回答：1；2"},
		{proto.Interaction{Kind: proto.IxPlan}, proto.Resolution{Approve: &yes, Mode: "acceptEdits"}, "已批准，自动接受编辑"},
		{proto.Interaction{Kind: proto.IxPlan}, proto.Resolution{Approve: &no, Feedback: "拆小点"}, "已打回：拆小点"},
		{proto.Interaction{Kind: proto.IxPermission}, proto.Resolution{Allow: &yes, Always: true}, "已允许（本会话不再问）"},
		{proto.Interaction{Kind: proto.IxPermission}, proto.Resolution{Allow: &no}, "已拒绝"},
		{proto.Interaction{}, proto.Resolution{Dismiss: true}, "已忽略"},
	} {
		if got := resolutionText("", c.ix, c.r); got != c.want {
			t.Errorf("%+v: %q, want %q", c.r, got, c.want)
		}
	}
}

// Each tool call reads as one line: a verb and what it works on.
func TestBotStepLines(t *testing.T) {
	for _, c := range []struct{ tool, in, want string }{
		{"Bash", `{"command":"go test ./...","description":"跑服务端测试"}`, "运行 · 跑服务端测试"},
		{"Bash", `{"command":"sleep 60","run_in_background":true}`, "后台运行 `sleep 60`"},
		{"Read", `{"file_path":"/a/feishu.go","offset":850,"limit":101}`, "读 `feishu.go` 第 850–950 行"},
		{"Read", `{"file_path":"/a/feishu.go","offset":20}`, "读 `feishu.go` 第 20 行起"},
		{"Read", `{"file_path":"/a/feishu.go"}`, "读 `feishu.go`"},
		{"Edit", `{"file_path":"/a/b.go"}`, "改 `b.go`"},
		{"Edit", `{"file_path":"/a/b.go","change":"add"}`, "新建 `b.go`"},
		{"Write", `{"file_path":"/a/b.go"}`, "写入 `b.go`"},
		{"Grep", `{"pattern":"botStep","path":"internal","glob":"*.go"}`, "搜 `botStep` 在 internal（&#42;.go）"},
		{"Grep", `{"pattern":"x","glob":"*.go"}`, "搜 `x`（&#42;.go）"},
		{"Glob", `{"pattern":"**/*.md"}`, "搜 `**/*.md`"},
		{"Task", `{"description":"查代码","subagent_type":"Explore"}`, "派子任务 · 查代码（Explore）"},
		{"TodoWrite", `{"todos":[{"content":"a","status":"completed"},{"content":"跑测试","activeForm":"正在跑测试","status":"in_progress"}]}`, "更新待办 · 正在跑测试"},
		{"mcp__persona__phone_calendar_events", `{"start":"2026-01-01"}`, "查日历"},
		{"mcp__persona__phone_contacts_search", `{"query":"张三"}`, "查通讯录 `张三`"},
		{"mcp__persona__phone_reminders_list", `{}`, "查提醒事项"},
		{"mcp__persona__phone_reminders_complete", `{"id":"r1"}`, "完成提醒 `r1`"},
		{"mcp__persona__phone_reminders_create", `{"title":"买菜"}`, "加提醒 `买菜`"},
		{"mcp__persona__phone_calendar_update_event", `{"id":"e1","start":"2026-11-12T15:00:00+08:00"}`, "改日程 `e1`"},
		{"mcp__persona__phone_shortcuts_run", `{"name":"回家模式"}`, "运行快捷指令 `回家模式`"},
		{"mcp__persona__phone_media_play", `{"query":"lofi"}`, "放音乐 `lofi`"},
		{"mcp__persona__phone_maps_navigate", `{"name":"Cafe","latitude":31.2,"longitude":121.4}`, "在地图里导航 `Cafe`"},
		{"mcp__persona__mail_search", `{"query":"发票"}`, "查邮件 `发票`"},
		{"mcp__persona__mail_send", `{"to":"me@example.com"}`, "发邮件 `me@example.com`"},
		{"mcp__persona__kb_search", `{"query":"周报"}`, "查知识库 `周报`"},
		{"mcp__github__github_read", `{"url":"https://example.com/x"}`, "github_read `https://example.com/x`"},
	} {
		it := proto.Item{Kind: proto.KindToolUse, ToolName: c.tool, ToolInput: json.RawMessage(c.in)}
		if got := botStep("", it); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.tool, c.in, got, c.want)
		}
	}
	// The agent message's titles keep the (verb, detail) split.
	verb, detail := botStepParts(i18n.En, proto.Item{Kind: proto.KindToolUse, ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"/a/x.go","offset":1,"limit":10}`)})
	if verb != "Read" || detail != "x.go lines 1–10" {
		t.Fatalf("%q %q", verb, detail)
	}
	for _, c := range []struct {
		l              i18n.Lang
		tool, in, want string
	}{
		{i18n.En, "Task", `{"description":"look","subagent_type":"Explore"}`, "Subtask · look (Explore)"},
		{i18n.En, "Grep", `{"pattern":"x","path":"src/x_y","glob":"*.go"}`, "Search · x in src/x_y (*.go)"},
		{"", "Task", `{"description":"查代码","subagent_type":"Explore"}`, "派子任务 · 查代码（Explore）"},
		// An unknown tool goes by its name: one separator in the agent message's title.
		{"", "mcp__x__github_read_file", `{"path":"abc_def"}`, "github_read_file · abc_def"},
	} {
		verb, detail := botStepParts(c.l, proto.Item{Kind: proto.KindToolUse, ToolName: c.tool, ToolInput: json.RawMessage(c.in)})
		if got := verb + " · " + detail; got != c.want { // as cotEvents joins them
			t.Errorf("%s: %q, want %q", c.tool, got, c.want)
		}
	}
}

func TestBotStepAndStripContext(t *testing.T) {
	if s := botStep("", proto.Item{Kind: proto.KindToolUse, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"go test ./...\nmore"}`)}); s != "运行 `go test ./...`" {
		t.Fatalf("bash step %q", s)
	}
	if s := botStep("", proto.Item{Kind: proto.KindText}); s != "" {
		t.Fatalf("text step %q", s)
	}
	in := "<persona-context kind=\"context\" title=\"x\" url=\"\">\n快照\n</persona-context>\n真正的问题"
	if got := stripContext(in); got != "真正的问题" {
		t.Fatalf("strip %q", got)
	}
	for in, want := range map[string]string{
		"<persona-context kind=\"context\" title=\"x\" url=\"\">\n快照\n</persona-context>\n真正的问题":            "真正的问题",
		"<persona-context>\n快照\n</persona-context>\n<persona-context kind=\"doc\">文档</persona-context>\n问题": "问题",
		"问题\n<persona-context kind=\"context\">\n被截断":                                                       "问题",
	} {
		if got := stripContext(in); got != want {
			t.Fatalf("strip %q = %q, want %q", in, got, want)
		}
	}
}

// "/new <text>" goes on with just the text; a command the bot does not know goes on as it is (it may be the
// conversation's own).
func TestBotCommandsPassTheRestOn(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	ctx := context.Background()
	if h, rest := s.botCommand(ctx, FeishuFeatures{Main: true}, "om", "/new 帮我写周报", "", ""); h || rest != "帮我写周报" {
		t.Fatalf("/new with text: handled=%v rest=%q", h, rest)
	}
	if got := s.botTargetFor("", ""); got.Kind != targetPersona || got.SessionID != "" {
		t.Fatalf("/new points the chat at a new conversation: %+v", got)
	}
	if h, rest := s.botCommand(ctx, FeishuFeatures{}, "om", "/compact", "", ""); h || rest != "/compact" {
		t.Fatalf("an unknown command goes on as it is: handled=%v rest=%q", h, rest)
	}
}

// Commands run before the normal message's feature gate, so /stop and /status must check botAllowed against the
// target they resolve: a feature that is off can neither be interrupted nor shown in a status (#3903).
func TestBotCommandFeatureGate(t *testing.T) {
	f := newFakeHub().withMachine()
	f.setSessions("box", proto.Session{SessionID: "agent01", MachineID: "box", Agent: "claude"})
	var notes []string
	var posts []map[string]any
	f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(p, &m)
		notes = append(notes, m.Text)
		return map[string]any{}, nil
	})
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		posts = append(posts, m)
		return map[string]any{"message_id": "om_card"}, nil
	})
	s := newTestService(t, f)
	ctx := context.Background()
	s.setBotCurrent(botTarget{Kind: targetAgent, MachineID: "box", SessionID: "agent01"})
	s.rememberBotThread("om_agent", botTarget{Kind: targetAgent, MachineID: "box", SessionID: "agent01"}, "agent")

	// Agents feature off: /stop as a bare command and in the agent's thread interrupts nothing, and the note says why.
	for _, c := range []struct{ name, root, parent string }{
		{"agents off, current target", "", ""},
		{"agents off, agent thread", "", "om_agent"},
	} {
		before := len(f.interrupted())
		notes = nil
		if h, _ := s.botCommand(ctx, FeishuFeatures{}, "om", "/stop", c.root, c.parent); !h {
			t.Fatalf("%s: /stop was not handled", c.name)
		}
		if got := f.interrupted()[before:]; len(got) != 0 {
			t.Fatalf("%s: interrupted %v, want none", c.name, got)
		}
		want := i18n.T("", "assistant-feishu.agents_off")
		if len(notes) != 1 || notes[0] != want {
			t.Fatalf("%s: notes %v, want %q", c.name, notes, want)
		}
	}
	// Main off with Agents on and a real main conversation: /stop must reach the gate, not the main session.
	f.setSessions("server", proto.Session{SessionID: "main001", MachineID: "server", Assistant: true})
	setSettings(t, s, func(v *Settings) { v.MachineID = "server"; v.Main = "main001" })
	s.setBotCurrent(botTarget{Kind: targetMain})
	before := len(f.interrupted())
	notes = nil
	mainOff := i18n.T("", "assistant-feishu.main_off_agents_on")
	if h, _ := s.botCommand(ctx, FeishuFeatures{Agents: true}, "om", "/stop", "", ""); !h {
		t.Fatal("main off: /stop not handled")
	}
	if got := f.interrupted()[before:]; len(got) != 0 || len(notes) != 1 || notes[0] != mainOff {
		t.Fatalf("main off: interrupted %v notes %v, want none + %q", got, notes, mainOff)
	}
	// /status must not reveal a target the features do not allow, and must not post a card.
	notes, posts = nil, nil
	s.setBotCurrent(botTarget{Kind: targetAgent, MachineID: "box", SessionID: "agent01"})
	if h, _ := s.botCommand(ctx, FeishuFeatures{}, "om", "/status", "", "om_agent"); !h {
		t.Fatal("agents off /status: not handled")
	}
	if len(notes) != 1 || notes[0] != i18n.T("", "assistant-feishu.agents_off") || len(posts) != 0 {
		t.Fatalf("agents off /status: notes %v posts %d, want one agents_off note and no card", notes, len(posts))
	}
	// With Agents on, /stop interrupts as before.
	s.setBotCurrent(botTarget{Kind: targetAgent, MachineID: "box", SessionID: "agent01"})
	before = len(f.interrupted())
	if h, _ := s.botCommand(ctx, FeishuFeatures{Agents: true}, "om", "/stop", "", ""); !h {
		t.Fatal("agents on: /stop not handled")
	}
	if got := f.interrupted()[before:]; len(got) != 1 || got[0] != "box/agent01" {
		t.Fatalf("agents on: interrupted %v, want [box/agent01]", got)
	}
}

// "/stop" in an agent's thread (or replying to one of its cards) stops that agent, not the chat's current target;
// without a bound thread it stops the current target, as before (#3844).
func TestBotStopFollowsTheRepliedThread(t *testing.T) {
	f := newFakeHub().withMachine()
	f.setSessions("box", proto.Session{SessionID: "cur0001", MachineID: "box", Agent: "claude"},
		proto.Session{SessionID: "agent01", MachineID: "box", Agent: "claude"})
	s := newTestService(t, f)
	ctx := context.Background()
	s.setBotCurrent(botTarget{Kind: targetAgent, MachineID: "box", SessionID: "cur0001"})
	s.rememberBotThread("om_agent", botTarget{Kind: targetAgent, MachineID: "box", SessionID: "agent01"}, "agent")
	for _, c := range []struct {
		name         string
		root, parent string
		want         string
	}{
		{"no reply stops the current target", "", "", "box/cur0001"},
		{"the card it replies to binds the agent", "", "om_agent", "box/agent01"},
		{"the thread root binds the agent too", "om_agent", "om_reply", "box/agent01"},
		{"an unbound thread falls back to current", "om_other", "om_reply", "box/cur0001"},
	} {
		before := len(f.interrupted())
		if h, _ := s.botCommand(ctx, FeishuFeatures{Agents: true}, "om_cmd", "/stop", c.root, c.parent); !h {
			t.Fatalf("%s: /stop was not handled", c.name)
		}
		got := f.interrupted()[before:]
		if len(got) != 1 || got[0] != c.want {
			t.Fatalf("%s: interrupted %v, want [%s]", c.name, got, c.want)
		}
	}
}

// The snapshot says which channels each conversation's messages came from (newest first) and where a channel's turn is
// followed or was just answered: the hub draws the first and leaves the finished push out for the second.
func TestChannelsReportMarksAndTurns(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	s.chans.now = func() time.Time { return now }
	s.noteChannelConversation("wechat", "server", "c1")
	time.Sleep(2 * time.Millisecond)
	s.noteChannelConversation(channel.Feishu, "server", "c1")
	s.chans.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	s.chans.st.Runs = append(s.chans.st.Runs, &channel.Run{ID: "r1", Channel: "wechat", MachineID: "server", SessionID: "c2"})
	s.chans.answered = map[[2]string]time.Time{{"server", "c3"}: now.Add(-time.Minute), {"server", "old"}: now.Add(-2 * time.Hour)}
	s.chans.mu.Unlock()
	r := s.channelsReport()
	if r == nil || len(r.Marks) != 1 || r.Marks[0].SessionID != "c1" || len(r.Marks[0].Channels) != 2 || r.Marks[0].Channels[0] != channel.Feishu {
		t.Fatalf("marks %+v", r)
	}
	if len(r.Turns) != 2 || r.Turns[0].SessionID != "c2" || !r.Turns[0].Answered.IsZero() || r.Turns[1].SessionID != "c3" || r.Turns[1].Answered.IsZero() {
		t.Fatalf("turns %+v", r.Turns)
	}
}

// The turns an older hub kept in the Feishu bot's state come over to the channels' runs once.
func TestFeishuLegacyRunsMove(t *testing.T) {
	st := &memStore{}
	ctx := context.Background()
	_ = st.PutSetting(ctx, botStateKey, map[string]any{"current": map[string]any{"kind": "main"}, "seen": []string{"om_1"},
		"runs": []map[string]any{{"id": "fb_om_2", "message_id": "om_2", "target": map[string]any{"kind": "persona", "machine_id": "server", "session_id": "c1"},
			"base": 7, "card": "om_card", "reaction": "rx"}}})
	s := newServiceOn(t, newFakeHub().withMachine(), st)
	runs := s.channelRuns(channel.Feishu)
	if len(runs) != 1 || runs[0].SessionID != "c1" || runs[0].Base != 7 || runs[0].Ext["card"] != "om_card" || runs[0].MsgIDs[0] != "feishu-om_2" {
		t.Fatalf("runs %+v", runs)
	}
	var left botState
	_, _ = st.Setting(ctx, botStateKey, &left)
	if len(left.Runs) != 0 || len(left.Seen) != 1 {
		t.Fatalf("the chat's state keeps %+v", left)
	}
	again := newServiceOn(t, newFakeHub().withMachine(), st)
	if n := len(again.channelRuns(channel.Feishu)); n != 1 {
		t.Fatalf("%d runs after a second start", n)
	}
}

// A channel with nowhere to send to yet (the hub's cannot_send) is told apart from a failed send; an answer is cut to
// what the channel's messages hold.
func TestChannelSendSplitsAndKnowsCannotSend(t *testing.T) {
	f := newFakeHub().withMachine()
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
			Installed: true, State: channel.StateConnected, Capabilities: channel.Capabilities{MaxText: 200}}}}, nil
	})
	refused := false
	var sent []map[string]any
	f.answerApp(AppChannels, ChannelsSend, func(p json.RawMessage) (any, error) {
		if refused {
			return nil, &rpc.Error{Code: CodeCannotSend, Message: "nowhere yet"}
		}
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		sent = append(sent, m)
		return map[string]any{}, nil
	})
	s := newTestService(t, f)
	ctx := context.Background()
	long := ""
	for len([]rune(long)) < 500 {
		long += "会议安排在上午十点。"
	}
	if err := s.channelSend(ctx, "examplechat", "ref", long, channel.OutAnswer, "m1"); err != nil {
		t.Fatal(err)
	}
	if len(sent) < 3 || sent[0]["ref"] != "ref#0" || sent[0]["reply_to"] != "m1" || len([]rune(sent[0]["text"].(string))) > 200 {
		t.Fatalf("sent %v", sent)
	}
	refused = true
	if err := s.channelSend(ctx, "examplechat", "ref2", "短", channel.OutAsk, ""); !errors.Is(err, errCannotSend) {
		t.Fatalf("cannot_send: %v", err)
	}
}

// feishuHub is a fake hub with the Feishu bot paired with owner (when given), recording what is posted.
func feishuHub(owner string, pairedAt time.Time) (*fakeHub, *[]map[string]any) {
	f := newFakeHub().withMachine()
	var posts []map[string]any
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: owner != "", Enabled: true, Owner: owner, PairedAt: pairedAt, Features: FeishuFeatures{Main: true, Cards: true}}, nil
	})
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		posts = append(posts, m)
		return map[string]any{"message_id": "om_card"}, nil
	})
	f.answerApp(AppFeishu, FeishuUpdateCard, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		m["update"] = true
		posts = append(posts, m)
		return map[string]any{}, nil
	})
	return f, &posts
}

// Paired with someone else, paired again or unpaired while the assistant was away (the hub's feishu.owner missed):
// the next look at the bot starts the chat afresh — the main conversation, no threads, nothing waiting. A state from
// before pairings were recorded keeps what it has.
func TestFeishuChatStartsAfreshWithANewPairing(t *testing.T) {
	st := &memStore{}
	ctx := context.Background()
	old := "ou_a@100"
	_ = st.PutSetting(ctx, botStateKey, botState{Current: botTarget{Kind: targetAgent, MachineID: "server", SessionID: "a1"},
		Threads: map[string]*botThread{"om_t": {Target: botTarget{Kind: targetAgent, MachineID: "server", SessionID: "a1"}}}, Pairing: &old})
	_ = st.PutSetting(ctx, channelsStateKey, channelsState{Runs: []*channel.Run{{ID: "fb_1", Channel: channel.Feishu, MachineID: "server", SessionID: "a1"}}})
	f, _ := feishuHub("ou_b", time.Unix(200, 0))
	s := newServiceOn(t, f, st)
	if _, err := s.feishuStatus(ctx); err != nil {
		t.Fatal(err)
	}
	if cur := s.botTargetFor("", ""); cur.Kind != targetMain {
		t.Fatalf("current %+v", cur)
	}
	if got := s.botTargetFor("om_t", ""); got.Kind != targetMain || len(s.channelRuns(channel.Feishu)) != 0 {
		t.Fatalf("threads %+v, runs %v", got, s.channelRuns(channel.Feishu))
	}
	// A state from before: the pairing now is recorded, nothing reset.
	st2 := &memStore{}
	_ = st2.PutSetting(ctx, botStateKey, botState{Current: botTarget{Kind: targetPersona, MachineID: "server", SessionID: "c1"}})
	f2, _ := feishuHub("ou_b", time.Unix(200, 0))
	s2 := newServiceOn(t, f2, st2)
	_, _ = s2.feishuStatus(ctx)
	if cur := s2.botTargetFor("", ""); cur.SessionID != "c1" {
		t.Fatalf("an older state was reset: %+v", cur)
	}
	var saved botState
	_, _ = st2.Setting(ctx, botStateKey, &saved)
	if saved.Pairing == nil || *saved.Pairing != "ou_b@200" {
		t.Fatalf("pairing %v", saved.Pairing)
	}
}

// A card posted before the assistant restarted is still closed when it is answered elsewhere afterwards: where it is
// was kept in the chat's state (ids and routing only, nothing it says).
func TestFeishuCardIsClosedAfterARestart(t *testing.T) {
	st := &memStore{}
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	ix := proto.Interaction{ID: "ix_q", MachineID: "server", SessionID: "m1", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "用哪个分支？"}}}
	before := newServiceOn(t, f, st)
	before.feishuInteraction(ix)
	if len(*posts) != 1 {
		t.Fatalf("posts %v", *posts)
	}
	var kept botState
	_, _ = st.Setting(context.Background(), botStateKey, &kept)
	ref := kept.Cards["ix_q"]
	if ref == nil || ref.MessageID != "om_card" || ref.SessionID != "m1" {
		t.Fatalf("kept %+v", kept.Cards)
	}
	if b, _ := json.Marshal(kept); strings.Contains(string(b), "用哪个分支") {
		t.Fatalf("the card's text is kept: %s", b)
	}
	after := newServiceOn(t, f, st) // the assistant restarted
	after.InteractionEnded(context.Background(), InteractionEnded{Interaction: ix, Resolution: &proto.Resolution{Answers: map[string]string{"用哪个分支？": "main"}}})
	waitUntil(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(*posts) == 2 && (*posts)[1]["update"] == true && (*posts)[1]["message_id"] == "om_card"
	})
	card, _ := (*posts)[1]["card"].(string)
	if !strings.Contains(card, "✓") || strings.Contains(card, `"callback"`) {
		t.Fatalf("closed card %s", card)
	}
	kept = botState{}
	_, _ = st.Setting(context.Background(), botStateKey, &kept)
	if len(kept.Cards) != 0 {
		t.Fatalf("still kept %+v", kept.Cards)
	}
}

// A card closed some other way than feishuInteractionDone (here its node went away) forgets its record too: kept, a
// restart would close it again and write "Ended" over what it said.
func TestFeishuDetachedCardIsNotKept(t *testing.T) {
	st := &memStore{}
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	s := newServiceOn(t, f, st)
	s.feishuInteraction(proto.Interaction{ID: "ix_q", MachineID: "server", SessionID: "m1", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "q"}}})
	var kept botState
	if _, _ = st.Setting(context.Background(), botStateKey, &kept); kept.Cards["ix_q"] == nil {
		t.Fatalf("not kept: %+v", kept.Cards)
	}
	s.feishuDetach([]string{"ix_q"})
	kept = botState{}
	if _, _ = st.Setting(context.Background(), botStateKey, &kept); len(kept.Cards) != 0 {
		t.Fatalf("still kept after detach: %+v", kept.Cards)
	}
}

// A press on a card whose interaction is over (answered elsewhere, maybe while the assistant was away) closes that
// card the way an ended one looks, with no buttons left.
func TestFeishuStalePressClosesTheCard(t *testing.T) {
	st := &memStore{}
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	status := "pending" // in the history, not restored yet; then "" (no record, or the history could not be read)
	f.answerApp(AppInteractions, InteractionsGet, func(json.RawMessage) (any, error) { return map[string]any{"ok": false, "status": status}, nil })
	_ = st.PutSetting(context.Background(), botStateKey, botState{Current: botTarget{Kind: targetMain},
		Cards: map[string]*botCardRef{"ix_old": {MessageID: "om_old", Kind: proto.IxPermission, MachineID: "server", SessionID: "a1", At: time.Now()}}})
	s := newServiceOn(t, f, st)
	var a feishubot.CardAction
	a.Action.Value = map[string]any{"a": "ix", "id": "ix_old", "allow": "1"}
	// Not known to be over: the toast only; the card stays live, its record kept.
	for _, st2 := range []string{"pending", ""} {
		status = st2
		if res := s.botCardAction(context.Background(), &a, ""); res == nil || res.Card != nil || res.Toast == nil {
			t.Fatalf("%q: %+v", st2, res)
		}
		var kept botState
		_, _ = st.Setting(context.Background(), botStateKey, &kept)
		if kept.Cards["ix_old"] == nil {
			t.Fatalf("%q: record dropped", st2)
		}
	}
	status = "resolved" // over for good
	res := s.botCardAction(context.Background(), &a, "")
	if res == nil || res.Card == nil || res.Toast == nil {
		t.Fatalf("response %+v", res)
	}
	b, _ := json.Marshal(res.Card)
	if !strings.Contains(string(b), "已结束") || strings.Contains(string(b), `"callback"`) {
		t.Fatalf("card %s", b)
	}
	var kept botState
	_, _ = st.Setting(context.Background(), botStateKey, &kept)
	if len(kept.Cards) != 0 {
		t.Fatalf("still kept %+v", kept.Cards)
	}
}

// Right after a restart the manager may not have its interactions back yet: the catch-up closes a kept card only once
// the history says it ended for good, never because it is missing from the cards up now.
func TestFeishuCatchUpClosesOnlyCardsThatEnded(t *testing.T) {
	st := &memStore{}
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	statuses := map[string]string{"ix_live": "pending", "ix_lost": "", "ix_done": "resolved", "ix_gone": "expired"}
	f.answerApp(AppInteractions, InteractionsGet, func(p json.RawMessage) (any, error) {
		var a struct {
			ID string `json:"interaction_id"`
		}
		_ = json.Unmarshal(p, &a)
		return map[string]any{"ok": false, "status": statuses[a.ID]}, nil
	})
	cards := map[string]*botCardRef{}
	for id := range statuses {
		cards[id] = &botCardRef{MessageID: "om_" + id, Kind: proto.IxPermission, MachineID: "server", SessionID: "a1", At: time.Now()}
	}
	_ = st.PutSetting(context.Background(), botStateKey, botState{Current: botTarget{Kind: targetMain}, Cards: cards})
	s := newServiceOn(t, f, st)
	old := botCardRecheck
	botCardRecheck = time.Hour // not again within the test
	defer func() { botCardRecheck = old }()
	s.channelsCatchUp(context.Background()) // the hub lists no card up (the manager has not restored them)
	waitUntil(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(*posts) >= 2
	})
	time.Sleep(50 * time.Millisecond)
	f.mu.Lock()
	closed := map[string]bool{}
	for _, p := range *posts {
		if p["update"] == true {
			closed[p["message_id"].(string)] = true
		}
	}
	f.mu.Unlock()
	if !closed["om_ix_done"] || !closed["om_ix_gone"] || closed["om_ix_live"] || closed["om_ix_lost"] {
		t.Fatalf("closed %v", closed)
	}
	var kept botState
	_, _ = st.Setting(context.Background(), botStateKey, &kept)
	if kept.Cards["ix_live"] == nil || kept.Cards["ix_lost"] == nil || kept.Cards["ix_done"] != nil {
		t.Fatalf("kept %v", kept.Cards)
	}
}

// Records are kept small when read too: none older than a week, at most the newest hundred.
func TestFeishuCardRecordsAreTrimmedOnLoad(t *testing.T) {
	st := &memStore{}
	cards := map[string]*botCardRef{"old": {MessageID: "om_old", At: time.Now().Add(-8 * 24 * time.Hour)}}
	for i := 0; i < botCardsKeep+5; i++ {
		cards[fmt.Sprintf("ix_%03d", i)] = &botCardRef{MessageID: "om", At: time.Now().Add(time.Duration(i) * time.Second)}
	}
	_ = st.PutSetting(context.Background(), botStateKey, botState{Current: botTarget{Kind: targetMain}, Cards: cards})
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	s := newServiceOn(t, f, st)
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(context.Background())
	n, _, old := len(b.cards), b.cards["ix_000"], b.cards["old"]
	b.mu.Unlock()
	if n != botCardsKeep || old.MessageID != "" {
		t.Fatalf("%d cards, old %+v", n, old)
	}
}

// A card that ends before (or while) it is sent to Feishu does not stay there with live buttons.
func TestFeishuCardOfAnEndedQuestionIsNotLeftOpen(t *testing.T) {
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	s := newTestService(t, f)
	ix := proto.Interaction{ID: "ix_q", MachineID: "server", SessionID: "m1", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "好吗？"}}}
	s.InteractionEnded(context.Background(), InteractionEnded{Interaction: ix})
	s.feishuInteraction(ix)
	if len(*posts) != 0 {
		t.Fatalf("an ended question was asked in Feishu: %v", *posts)
	}
	ix.ID = "ix_q2"
	s.feishuInteraction(ix)
	if len(*posts) != 1 {
		t.Fatalf("posts %v", *posts)
	}
	s.InteractionEnded(context.Background(), InteractionEnded{Interaction: ix})
	waitUntil(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(*posts) == 2 && (*posts)[1]["update"] == true
	})
}

// A run keeps its answer until the hub takes it: the hub not answering where the channel is, or the send failing, is
// tried again with the same ref.
func TestChannelAnswerWaitsForTheHub(t *testing.T) {
	f := newFakeHub().withMachine()
	down := true
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		if down {
			return nil, errors.New("the hub is restarting")
		}
		return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "Example Chat", Kind: channel.KindIM, Source: channel.SourcePlugin,
			Installed: true, State: channel.StateConnected}}}, nil
	})
	var sent []map[string]any
	f.answerApp(AppChannels, ChannelsSend, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		sent = append(sent, m)
		return map[string]any{}, nil
	})
	s := newTestService(t, f)
	r := &channel.Run{ID: "examplechat_m1", Channel: "examplechat", MachineID: "server", SessionID: "c1", ReplyTo: "m1", Base: 3, Started: time.Now()}
	prog := &chanLive{mu: newChanMutex(), checking: newChanMutex(), done: make(chan struct{})}
	s.chans.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	s.chans.st.Runs = append(s.chans.st.Runs, r)
	s.chans.live = map[string]*chanLive{r.ID: prog}
	s.chans.mu.Unlock()
	s.finishChannelRun(r, &channel.Result{Answer: "答案"})
	if n := len(s.channelRuns("examplechat")); n != 1 || len(sent) != 0 {
		t.Fatalf("with the hub away: %d runs, sent %v", n, sent)
	}
	f.mu.Lock()
	down = false
	f.mu.Unlock()
	s.forgetChannelList()
	s.finishChannelRun(r, &channel.Result{Answer: "答案"})
	if n := len(s.channelRuns("examplechat")); n != 0 || len(sent) != 1 || sent[0]["ref"] != "examplechat_m1:3" || sent[0]["text"] != "答案" {
		t.Fatalf("back: %d runs, sent %v", n, sent)
	}
}

// The text a followed conversation writes comes many times a second: one per conversation waits in line, with the
// newest text, and the important events are never crowded out.
func TestBusDeltasAreTakenTogether(t *testing.T) {
	b := NewBusHub(BusOptions{Logf: testLogf(t)})
	delta := func(sid, text string) json.RawMessage {
		raw, _ := json.Marshal(SessionDelta{MachineID: "server", SessionID: sid, Text: text})
		return raw
	}
	for i := 0; i < 5000; i++ {
		b.queueEvent(assistantwire.EventSessionDelta, delta("c1", fmt.Sprint("第", i, "个字")))
	}
	b.queueEvent(assistantwire.EventSessionDelta, delta("c2", "另一段"))
	b.queueEvent(assistantwire.EventTurnEnded, json.RawMessage(`{"machine_id":"server","session_id":"c1"}`))
	if n := len(b.events); n != 3 {
		t.Fatalf("%d events waiting, want 3", n)
	}
	first := <-b.events
	var p SessionDelta
	_ = json.Unmarshal(b.takeDelta(first.params), &p)
	if p.SessionID != "c1" || p.Text != "第4999个字" {
		t.Fatalf("delta %+v", p)
	}
}

// waitUntil waits up to 5 seconds for ok.
func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}

// pagedHub answers session.items a page at a time after the seq asked (more_after while more follow) and session.item
// whole: the transcript a run's answer is read from over the bus.
type pagedHub struct {
	*fakeHub
	items []proto.Item
	page  int
	full  map[string]string
	after []int // the after_seq of each session.items asked
}

func (h *pagedHub) DeviceCall(ctx context.Context, p assistantwire.DeviceCall) (json.RawMessage, error) {
	var a struct {
		SessionID string `json:"session_id"`
		After     int    `json:"after_seq"`
		ItemID    string `json:"item_id"`
		Lazy      bool   `json:"lazy_images"`
	}
	_ = json.Unmarshal(p.Params, &a)
	switch {
	case p.Method == DeviceSessionItems && a.SessionID == "s1" && a.Lazy:
		h.after = append(h.after, a.After)
		var page []proto.Item
		for _, it := range h.items {
			if it.Seq > a.After && len(page) < h.page {
				page = append(page, it)
			}
		}
		to := a.After
		if len(page) > 0 {
			to = page[len(page)-1].Seq
		}
		return json.Marshal(map[string]any{"items": page, "more_after": to < h.items[len(h.items)-1].Seq, "to_seq": to})
	case p.Method == "session.item" && a.SessionID == "s1":
		return json.Marshal(proto.Item{ID: a.ItemID, Role: "assistant", Kind: proto.KindText, Text: h.full[a.ItemID]})
	}
	return h.fakeHub.DeviceCall(ctx, p)
}

// The assistant reads a run's answer over the bus (channel.ReadTurn): every page after the run's base, its message
// found, the answer's cut item read whole.
func TestReadTurnOverTheBus(t *testing.T) {
	f := newFakeHub().withMachine()
	h := &pagedHub{fakeHub: f, page: 2, full: map[string]string{"a4": "the whole answer"}, items: []proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "earlier"},
		{ID: "u2", Seq: 2, Role: "user", Kind: proto.KindText, Text: "ours", ClientMsgID: "wechat-1"},
		{ID: "t3", Seq: 3, Role: "assistant", Kind: proto.KindToolUse},
		{ID: "a4", Seq: 4, Role: "assistant", Kind: proto.KindText, Text: "the whole…", FullSize: 16},
	}}
	s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return &memStore{} }, Logf: testLogf(t)}))
	rd, err := s.readTurn(context.Background(), &channel.Run{MachineID: "server", SessionID: "s1", MsgIDs: []string{"wechat-1"}})
	if err != nil || !rd.Found || rd.Answer != "the whole answer" || rd.Last != 4 {
		t.Fatalf("%+v %v", rd, err)
	}
	if !slices.Equal(h.after, []int{0, 2}) {
		t.Fatalf("pages asked after %v", h.after)
	}
}

// A failed read of the channels' settings, their runs or the Feishu chat's state used to leave an empty copy in
// memory that the next change wrote over the stored one (every other channel's settings, the runs waiting, the chat's
// target, threads and pairing), and the first message after it wrote into a nil map: a panic that took the assistant
// down. Now nothing is written until a read succeeds, and a message is handed back to the hub to try again.
func TestChannelsReadFailureNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	store := &flakyScheduleStore{data: map[string][]byte{}}
	_ = store.PutSetting(ctx, channelsKey, map[string]*channelPrefs{"telegram": {Features: map[string]bool{"asks": false}}})
	_ = store.PutSetting(ctx, channelsStateKey, channelsState{Runs: []*channel.Run{{ID: "wechat_x_m0", Channel: "wechat_x"}},
		Seen: map[string][]string{"wechat_x": {"m0"}}})
	pairing := "ou_example"
	_ = store.PutSetting(ctx, botStateKey, botState{Current: botTarget{Kind: targetMain}, Pairing: &pairing,
		Threads: map[string]*botThread{"om_old": {Target: botTarget{Kind: targetMain}}}})
	before := store.stored()
	store.failReads(true)
	// The runs' machine is known: otherwise their watchers drop them (the machine left the configuration) while the
	// test is still looking.
	h := newServiceOn(t, newFakeHub().withMachine(), store)

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panicked while the state could not be read: %v", r)
			}
		}()
		err := h.ChannelReceive(ctx, ChannelReceive{Channel: "wechat_x", Message: []byte(`{"id":"m1","text":"示例"}`)})
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != CodeTryAgain {
			t.Fatalf("channels.receive while unreadable: %v, want %s", err, CodeTryAgain)
		}
		if first, err := h.claimBotMessage("om_new"); first || err == nil {
			t.Fatalf("a Feishu message while unreadable: first %v, err %v", first, err)
		}
		h.rememberBotThread("om_bot", botTarget{Kind: targetMain}, "示例")
		h.setBotCurrent(botTarget{Kind: targetAgent, MachineID: "server", SessionID: "s2"})
		h.noteChannelConversation("wechat_x", "server", "s1")
		h.startChannelRun(&channel.Run{ID: "wechat_x_m2", Channel: "wechat_x", MachineID: "server", SessionID: "s1"})
	}()
	if n := store.writes(); n != 0 {
		t.Fatalf("%d writes while the state could not be read", n)
	}
	for k, v := range store.stored() {
		if before[k] != v {
			t.Fatalf("%s changed while unreadable:\n before %s\n after  %s", k, before[k], v)
		}
	}

	// Readable again: what was stored is what is read, and a message is taken once.
	store.failReads(false)
	if err := h.ChannelReceive(ctx, ChannelReceive{Channel: "wechat_x", Message: []byte(`{"id":"m0","text":"示例"}`)}); err != nil {
		t.Fatalf("a message seen before: %v", err)
	}
	if first, err := h.claimChannelMessage("wechat_x", "m3"); !first || err != nil {
		t.Fatalf("a new message: first %v, err %v", first, err)
	}
	// The run started while unreadable is kept next to the stored one, each with its own fields (the load used to
	// decode into the live run, under its watcher), and both are written back.
	runs := map[string]channel.Run{}
	for _, r := range h.channelRuns("wechat_x") {
		runs[r.ID] = r
	}
	if r := runs["wechat_x_m0"]; r.Channel != "wechat_x" || r.MachineID != "" || r.SessionID != "" {
		t.Fatalf("the stored run: %+v (runs %+v)", r, runs)
	}
	if r := runs["wechat_x_m2"]; r.Channel != "wechat_x" || r.MachineID != "server" || r.SessionID != "s1" {
		t.Fatalf("the run started while unreadable: %+v (runs %+v)", r, runs)
	}
	var saved channelsState
	_, _ = store.Setting(ctx, channelsStateKey, &saved)
	if len(saved.Runs) != 2 || !slices.Contains(saved.Seen["wechat_x"], "m0") {
		t.Fatalf("the stored state: %s", store.stored()[channelsStateKey])
	}
	var prefs map[string]*channelPrefs
	_, _ = store.Setting(ctx, channelsKey, &prefs)
	if prefs["telegram"] == nil {
		t.Fatalf("the channels' settings: %s", store.stored()[channelsKey])
	}
	if first, err := h.claimBotMessage("om_new"); !first || err != nil {
		t.Fatalf("a Feishu message once readable: first %v, err %v", first, err)
	}
	var bot botState
	_, _ = store.Setting(ctx, botStateKey, &bot)
	if bot.Pairing == nil || *bot.Pairing != pairing || bot.Threads["om_old"] == nil {
		t.Fatalf("the chat's state: %s", store.stored()[botStateKey])
	}
}

// The bot's cards and lines read in the user's language; without one they are Chinese.
func TestBotTextInLanguages(t *testing.T) {
	yes := true
	if got := resolutionText(i18n.En, proto.Interaction{}, proto.Resolution{Approve: &yes, Mode: "acceptEdits"}); got != "Approved, auto-accepting edits" {
		t.Fatal(got)
	}
	if got := resolutionText("", proto.Interaction{}, proto.Resolution{Approve: &yes}); got != "已批准" {
		t.Fatal(got)
	}
	bash := proto.Item{Kind: proto.KindToolUse, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"go test ./..."}`)}
	if got := botStep(i18n.Ja, bash); got != "実行 `go test ./...`" {
		t.Fatal(got)
	}
	if got := botHelp(i18n.En, FeishuFeatures{Main: true}); !strings.Contains(got, "`/main` back to the main chat") || strings.Contains(got, "/agents") {
		t.Fatal(got)
	}
	if err := botAllowed(i18n.Ja, FeishuFeatures{}, botTarget{Kind: targetMain}); err == nil || !strings.Contains(err.Error(), "Larkボット") {
		t.Fatal(err)
	}
}

// A permission card with no summary shows the call's input with every credential's value masked, as the summaries
// and the channels' approval text do (#4734, #4762): the card goes to a Feishu chat.
func TestFeishuPermissionCardWithoutSummaryMasksCredentials(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	s := newTestService(t, f)
	ix := proto.Interaction{ID: "p1", MachineID: "server", SessionID: "a1", Kind: proto.IxPermission, ToolName: "mcp__x__call",
		ToolInput: json.RawMessage(`{"api_key":"sk-live-123","repo":"demo","auth":{"password":"hunter2"}}`)}
	b, _ := json.Marshal(s.interactionCard(context.Background(), ix))
	if got := string(b); strings.Contains(got, "sk-live-123") || strings.Contains(got, "hunter2") || !strings.Contains(got, "demo") {
		t.Fatalf("card %s", got)
	}
}

// An MCP server asking to authorize or to fill in a form says on the card what it asks: its message, the link as a
// button; a form's fields by name only (filled in the app), no inputs in Feishu.
func TestFeishuElicitationCardSaysWhatItAsks(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	s := newTestService(t, f)
	ctx := context.Background()
	link := proto.Interaction{ID: "e1", MachineID: "server", SessionID: "a1", Kind: proto.IxElicitation,
		Elicitation: &proto.Elicitation{Mode: "url", Message: "登录 GitHub 授权这个 MCP 服务", URL: "https://github.com/login/device"}}
	b, _ := json.Marshal(s.interactionCard(ctx, link))
	got := string(b)
	if !strings.Contains(got, "登录 GitHub 授权这个 MCP 服务") || !strings.Contains(got, `"default_url":"https://github.com/login/device"`) ||
		!strings.Contains(got, "去授权") || !strings.Contains(got, "要你授权：") || strings.Contains(got, `"template":"grey"`) {
		t.Fatalf("link card %s", got)
	}
	if !strings.Contains(got, "将打开：") || !strings.Contains(got, "github.com") || !strings.Contains(got, "回到 Persona App 点确认") {
		t.Fatalf("link card host / confirm note %s", got)
	}
	// A link with a user in it reads as one site and opens another: refused, as the apps refuse it.
	for _, bad := range []string{"https://github.com@evil.example/login", "https://user:pw@example.com/", "https:///example.com", "https:example.com"} {
		ix := link
		ix.Elicitation = &proto.Elicitation{Mode: "url", Message: "登录 GitHub 授权", URL: bad}
		b, _ := json.Marshal(s.interactionCard(ctx, ix))
		if c := string(b); strings.Contains(c, "evil.example") || strings.Contains(c, "去授权") || strings.Contains(c, "将打开") {
			t.Fatalf("%q: %s", bad, c)
		}
	}
	// The button opens the link exactly as given.
	if lk, host := elicitationTarget(" https://example.com/a?b=c%20d "); lk != "https://example.com/a?b=c%20d" || host != "example.com" {
		t.Fatalf("target %q %q", lk, host)
	}
	form := proto.Interaction{ID: "e2", MachineID: "server", SessionID: "a1", Kind: proto.IxElicitation,
		Elicitation: &proto.Elicitation{Mode: "form", Message: "填写连接信息", URL: "javascript:alert(1)",
			RequestedSchema: json.RawMessage(`{"properties":{"token":{"title":"访问令牌","writeOnly":true},"host":{}}}`)}}
	b, _ = json.Marshal(s.interactionCard(ctx, form))
	got = string(b)
	if !strings.Contains(got, "填写连接信息") || !strings.Contains(got, "访问令牌") || !strings.Contains(got, "host") ||
		!strings.Contains(got, "到 Persona App 里填写") || strings.Contains(got, "javascript:") || strings.Contains(got, `"tag":"input"`) ||
		strings.Contains(got, "去授权") {
		t.Fatalf("form card %s", got)
	}
}

// A synced turn goes to Feishu whole, laid out like the bot's answers (more cards when long), where it came from and
// the open button under the last card only; a reply to any card goes back to that conversation.
func TestFeishuMirrorSendsTheWholeAnswer(t *testing.T) {
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), PublicURL: "https://hub.example.com", Features: FeishuFeatures{Main: true, Sync: true}}, nil
	})
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true})
	long := "# 晚报\n\n" + strings.Repeat("今天做完了很多事情。\n\n", 700) + "| 项 | 状态 |\n| --- | --- |\n| a | 完成 |\n\n结尾这句在最后"
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: "写晚报"}, proto.Item{Role: "assistant", Kind: proto.KindText, Text: long})
	s := newTestService(t, f)
	s.feishuMirror(context.Background(), "server", "m1")
	if len(*posts) < 2 {
		t.Fatalf("one card for a long answer: %d", len(*posts))
	}
	first, _ := (*posts)[0]["card"].(string)
	last, _ := (*posts)[len(*posts)-1]["card"].(string)
	if strings.Contains(first, `"button"`) || !strings.Contains(last, `"button"`) || !strings.Contains(last, "主对话") || !strings.Contains(last, "写晚报") {
		t.Fatalf("first %s\nlast %s", first, last)
	}
	if !strings.Contains(last, "结尾这句在最后") || !strings.Contains(last, `"tag":"table"`) || strings.Contains(last, "只发了开头") {
		t.Fatalf("not the whole answer: %s", last)
	}
	if !strings.Contains(first, "晚报") || !strings.Contains(first, `"header"`) {
		t.Fatalf("no header: %s", first)
	}
	if got := s.botTargetFor("om_card", ""); got.Kind != targetPersona || got.SessionID != "m1" {
		t.Fatalf("thread %+v", got)
	}
}

// Without the hub's web address (no public_url) a card carries no "Open web version" button with an empty link, which does
// nothing and may get the whole card refused; a question card keeps its answer buttons. With one, the button is there.
func TestFeishuOpenButtonNeedsAWebAddress(t *testing.T) {
	for _, web := range []string{"", "persona://x", "https://hub.example.com/"} {
		f, posts := feishuHub("ou_a", time.Unix(100, 0))
		f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
			return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), PublicURL: web, Features: FeishuFeatures{Main: true, Cards: true}}, nil
		})
		f.settings = &Settings{MachineID: "server", Main: "a1"} // the main conversation: Feishu drives it
		f.setSessions("server", proto.Session{SessionID: "a1", Assistant: true})
		s := newTestService(t, f)
		ix := proto.Interaction{ID: "ix_q", MachineID: "server", SessionID: "a1", Kind: proto.IxQuestion,
			Questions: []proto.Question{{Question: "用哪个？", Options: []proto.QuestionOption{{Label: "甲"}, {Label: "乙"}}}}}
		s.feishuInteraction(ix)
		if len(*posts) != 1 {
			t.Fatalf("%q: posts %v", web, *posts)
		}
		card, _ := (*posts)[0]["card"].(string)
		if !strings.Contains(card, `"甲"`) || !strings.Contains(card, `"乙"`) {
			t.Fatalf("%q: answer buttons gone: %s", web, card)
		}
		hasLink := strings.Contains(card, "open_url")
		if strings.Contains(card, `"default_url":""`) || hasLink != strings.HasPrefix(web, "https://") {
			t.Fatalf("%q: link %v in %s", web, hasLink, card)
		}
		if hasLink && !strings.Contains(card, "https://hub.example.com/persona/a1") {
			t.Fatalf("link %s", card)
		}
	}
}

// A turn that ended without words says why, when the conversation says; otherwise the plain line as before.
func TestEmptyAnswerSaysWhyItStopped(t *testing.T) {
	if got := emptyAnswer("zh-Hans", "API 错误：overloaded"); got != "这一轮没有文字回答就停了：API 错误：overloaded" {
		t.Fatalf("with a reason: %q", got)
	}
	if got := emptyAnswer("zh-Hans", " "); got != "（这一轮没有文字回答）" {
		t.Fatalf("without: %q", got)
	}
	if got := emptyAnswer("en", "已中断"); !strings.Contains(got, "stopped without a text answer: 已中断") {
		t.Fatalf("en: %q", got)
	}
}

// A notify_user push goes to Feishu laid out like the bot's answers and whole: its title heads the first card, the
// open button is under the last only, and a long body is more cards instead of one Feishu refuses.
func TestFeishuPushIsLaidOutAndWhole(t *testing.T) {
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), PublicURL: "https://hub.example.com", Features: FeishuFeatures{Main: true, Pushes: true}}, nil
	})
	f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true})
	s := newTestService(t, f)
	body := strings.Repeat("巡检一切正常。\n\n", 900) + "| 机器 | 状态 |\n| --- | --- |\n| a | 好 |\n\n最后一句"
	s.feishuPush("server", "m1", "巡检结果", body)
	if len(*posts) < 2 {
		t.Fatalf("one card for a long push: %d", len(*posts))
	}
	first, _ := (*posts)[0]["card"].(string)
	last, _ := (*posts)[len(*posts)-1]["card"].(string)
	if !strings.Contains(first, "🔔 巡检结果") || strings.Contains(first, `"button"`) {
		t.Fatalf("first %s", first)
	}
	if !strings.Contains(last, `"button"`) || !strings.Contains(last, `"tag":"table"`) || !strings.Contains(last, "最后一句") {
		t.Fatalf("last %s", last)
	}
	for _, p := range *posts {
		if c, _ := p["card"].(string); len(c) > 30000 {
			t.Fatalf("card of %d bytes", len(c))
		}
	}
	if got := s.botTargetFor("om_card", ""); got.Kind != targetPersona || got.SessionID != "m1" {
		t.Fatalf("thread %+v", got)
	}
}

// The progress card's lines: the newest call still running and its age, the last steps, the subagents; counters in a
// note. Noted from the items as they come (liveNote).
func TestProgressCardLines(t *testing.T) {
	t0 := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	use := func(id, name, in string, ts time.Time) proto.Item {
		return proto.Item{Kind: proto.KindToolUse, ToolUseID: id, ToolName: name, ToolInput: json.RawMessage(in), TS: ts}
	}
	res := func(id string) proto.Item { return proto.Item{Kind: proto.KindToolResult, ToolUseID: id} }
	for _, c := range []struct {
		name  string
		items []proto.Item
		steps []string
		want  []string // each must be in the card
		not   []string
	}{
		{"open call and its age", []proto.Item{use("1", "Bash", `{"command":"go test ./..."}`, t0.Add(-40*time.Second))},
			[]string{"运行 `go test ./...`"}, []string{"正在：运行 `go test ./...`（已 40 秒）", "调用了 1 次工具"}, []string{"等 ", "读了", "子任务"}},
		{"no time on the item: when it was seen", []proto.Item{use("1", "Bash", `{"command":"ls"}`, time.Time{})},
			nil, []string{"正在：运行 `ls`（已 0 秒）"}, nil},
		{"several open: the newest", []proto.Item{
			use("1", "Bash", `{"command":"a"}`, t0.Add(-time.Minute)), use("2", "Bash", `{"command":"b"}`, t0.Add(-time.Minute)),
			use("3", "Read", `{"file_path":"/x/c.go"}`, t0.Add(-5*time.Second)), res("2")},
			nil, []string{"正在：读 `c.go`（已 5 秒） 等 1 个"}, []string{"`a`（"}},
		{"none open: no now line", []proto.Item{use("1", "Bash", `{"command":"a"}`, t0), res("1")},
			[]string{"运行 `a`"}, []string{"- 运行 `a`"}, []string{"正在："}},
		{"counters: distinct files", []proto.Item{
			use("1", "Read", `{"file_path":"/a.go"}`, t0), res("1"), use("2", "Read", `{"file_path":"/a.go"}`, t0), res("2"),
			use("3", "Read", `{"file_path":"/b.go"}`, t0), res("3"), use("4", "Edit", `{"file_path":"/a.go"}`, t0), res("4"),
			use("5", "NotebookEdit", `{"notebook_path":"/n.ipynb"}`, t0), res("5")},
			nil, []string{"调用了 5 次工具 · 读了 2 个文件 · 改了 2 个文件"}, nil},
		{"subtasks running and done", []proto.Item{
			use("1", "Task", `{"description":"查 *日志*","subagent_type":"Explore"}`, t0), use("2", "Agent", `{"description":"写测试"}`, t0), res("1")},
			nil, []string{"子任务", "- 查 &#42;日志&#42; `Explore` · 完成", "- 写测试 · 进行中"}, nil},
		{"no steps at all", nil, nil, []string{"工作中 · 已用 3 分 12 秒"}, []string{"正在：", "调用了", "子任务", "前面还有"}},
	} {
		p := &chanLive{}
		for _, it := range c.items {
			p.liveNote("", it, t0)
		}
		v := liveView{open: p.open, calls: p.calls, read: len(p.read), edited: len(p.edited), subs: p.subs, subsMore: p.subsMore, now: t0}
		b, _ := json.Marshal(progressCard("", "Persona", 192*time.Second, c.steps, "", "", v, botTarget{}))
		var s string
		for _, md := range progressLines("", c.steps, v) {
			s += md + "\n"
		}
		all := string(b) + "\n" + s
		for _, w := range c.want {
			if !strings.Contains(all, w) {
				t.Errorf("%s: no %q in\n%s\n%s", c.name, w, s, b)
			}
		}
		for _, w := range c.not {
			if strings.Contains(all, w) {
				t.Errorf("%s: %q should not be in\n%s", c.name, w, all)
			}
		}
	}
}

// Many subtasks: the last six, then "and N more"; the "N earlier steps" of more than five steps stays.
func TestProgressCardManySubtasks(t *testing.T) {
	p := &chanLive{}
	for i := range 60 {
		p.liveNote("", proto.Item{Kind: proto.KindToolUse, ToolUseID: fmt.Sprint(i), ToolName: "Task", ToolInput: json.RawMessage(fmt.Sprintf(`{"description":"t%d"}`, i))}, time.Now())
	}
	if len(p.subs) != liveSubsMost || p.subsMore != 10 || len(p.open) != liveOpenMost {
		t.Fatalf("bounds: %d subs, %d more, %d open", len(p.subs), p.subsMore, len(p.open))
	}
	steps := []string{"1", "2", "3", "4", "5", "6", "7"}
	s := strings.Join(progressLines("", steps, liveView{subs: p.subs, subsMore: p.subsMore}), "\n")
	if !strings.Contains(s, "- t59 · 进行中") || strings.Contains(s, "- t53 ") || !strings.Contains(s, "等 54 个") ||
		!strings.Contains(s, "前面还有 2 步") || strings.Contains(s, "- 2\n") {
		t.Fatalf("%s", s)
	}
}

// A held run's next turn starts from nothing: not the first turn's open calls, counters or subtasks.
func TestHeldRunsNextTurnStartsItsCountsAgain(t *testing.T) {
	s, _, _ := cotHub(t, false)
	r, prog := cotRun(s)
	s.chans.mu.Lock()
	prog.from = 1
	noteRunItems("", r, prog, []proto.Item{
		{Seq: 2, Kind: proto.KindToolUse, ToolUseID: "a", ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"/a.go"}`)},
		{Seq: 3, Kind: proto.KindToolUse, ToolUseID: "b", ToolName: "Task", ToolInput: json.RawMessage(`{"description":"查"}`)},
	}, time.Now())
	s.chans.mu.Unlock()
	if v := s.runLiveView(r.ID); v.calls != 2 || v.read != 1 || len(v.open) != 2 || len(v.subs) != 1 {
		t.Fatalf("first turn: %+v", v)
	}
	s.finishChannelRun(r, &channel.Result{Answer: "先答一部分", Held: true})
	if v := s.runLiveView(r.ID); v.calls != 0 || v.read != 0 || v.edited != 0 || len(v.open) != 0 || len(v.subs) != 0 || v.subsMore != 0 {
		t.Fatalf("second turn starts with %+v", v)
	}
}

// A file counter at its cap says so: "1000+".
func TestProgressCardFileCountAtItsCap(t *testing.T) {
	b, _ := json.Marshal(progressCard("", "Persona", time.Second, nil, "", "", liveView{calls: 1, read: liveFilesMost, edited: 3}, botTarget{}))
	if !strings.Contains(string(b), "读了 1000+ 个文件 · 改了 3 个文件") {
		t.Fatalf("%s", b)
	}
	b, _ = json.Marshal(progressCard(i18n.En, "Persona", time.Second, nil, "", "", liveView{calls: 1, edited: 1}, botTarget{}))
	if !strings.Contains(string(b), "1 tool call · changed 1 file") {
		t.Fatalf("%s", b)
	}
}

// slowReactHub is a fake hub on which putting the "OnIt" reaction on message slow takes a while (Feishu slow to
// answer, as it is now and then).
type slowReactHub struct {
	*fakeHub
	slow string
}

func (h slowReactHub) AppCall(ctx context.Context, p AppCall) (json.RawMessage, error) {
	if p.AppID == AppFeishu && p.Method == FeishuReact {
		var m map[string]any
		_ = json.Unmarshal(p.Params, &m)
		if m["message_id"] == h.slow && m["emoji"] == "OnIt" {
			time.Sleep(300 * time.Millisecond)
		}
	}
	return h.fakeHub.AppCall(ctx, p)
}

// Messages sent to the bot one right after another go into the conversation in the order they came, and the one
// answer covering them replies to the last. Each used to be handled on its own: one slower to pass on (its reaction,
// a picture to download, a quote to read) was overtaken by the next, the conversation read them the wrong way round,
// and the answer for all of them went under the first message, the others folded into it.
func TestFeishuMessagesGoInInTheOrderTheyCame(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.answerApp(AppFeishu, FeishuReact, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		return map[string]any{"reaction_id": fmt.Sprint("rx_", m["message_id"])}, nil
	})
	f.answerApp(AppFeishu, FeishuUnreact, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	st := &memStore{}
	s := withSettings(t, f, New(Options{Hub: slowReactHub{fakeHub: f, slow: "om_1"}, Store: func() Store { return st }, Logf: testLogf(t)}))
	ctx := context.Background()
	for _, m := range []struct{ id, text string }{{"om_1", "一"}, {"om_2", "二"}, {"om_3", "三"}} {
		raw, _ := json.Marshal(map[string]any{"message": map[string]any{"message_id": m.id, "chat_id": "oc_1", "chat_type": "p2p",
			"message_type": "text", "content": fmt.Sprintf(`{"text":%q}`, m.text)}})
		if err := s.FeishuMessage(ctx, FeishuMessage{Event: raw}); err != nil {
			t.Fatal(err)
		}
	}
	sent := func() []string {
		f.mu.Lock()
		defer f.mu.Unlock()
		var ids []string
		for _, c := range f.calls {
			if c.Method == DeviceSessionSendOnce {
				var p map[string]any
				_ = json.Unmarshal(c.Params, &p)
				ids = append(ids, fmt.Sprint(p["client_msg_id"]))
			}
		}
		return ids
	}
	waitUntil(t, func() bool { return len(sent()) == 3 })
	if got := strings.Join(sent(), " "); got != "feishu-om_1 feishu-om_2 feishu-om_3" {
		t.Fatalf("went in as %s", got)
	}
	waitUntil(t, func() bool { return len(s.channelRuns(channel.Feishu)) == 1 })
	r := s.channelRuns(channel.Feishu)[0]
	if r.ReplyTo != "om_3" || len(r.Folded) != 2 || r.Folded[0].ReplyTo != "om_1" || r.Folded[1].ReplyTo != "om_2" {
		t.Fatalf("the answer replies to %s, folded %+v", r.ReplyTo, r.Folded)
	}
	if got := strings.Join(r.MsgIDs, " "); got != "feishu-om_1 feishu-om_2 feishu-om_3" {
		t.Fatalf("msg ids %s", got)
	}
}

// A message that comes while the previous run's answer waits for a progress update to let go of it is not folded
// into that run: the answer goes out under the earlier message, and the later message's answer would carry it again.
func TestFeishuRunBeingAnsweredIsNotFolded(t *testing.T) {
	s, posts, _ := cotHub(t, true)
	r, prog := cotRun(s)
	r.MsgIDs = []string{"feishu-m1"}
	prog.mu.Lock() // a progress update is on it
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		s.finishChannelRun(r, &channel.Result{Answer: "答一"})
	}()
	time.Sleep(50 * time.Millisecond) // the answer waits for the update
	next := &channel.Run{ID: "fb_m2", Channel: channel.Feishu, MachineID: "server", SessionID: "c1", ReplyTo: "om_m2", Label: "Persona",
		Started: time.Now(), MsgIDs: []string{"feishu-m2"}}
	started := make(chan struct{})
	go func() {
		defer close(started)
		s.startChannelRun(next)
	}()
	time.Sleep(50 * time.Millisecond)
	prog.mu.Unlock()
	for _, ch := range []chan struct{}{finished, started} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out")
		}
	}
	s.chans.mu.Lock()
	folded, ids := len(next.Folded), slices.Clone(next.MsgIDs)
	s.chans.mu.Unlock()
	if folded != 0 || len(ids) != 1 {
		t.Fatalf("the next run took in the one being answered: %d folded, msg ids %v", folded, ids)
	}
	if len(*posts) != 1 || !strings.Contains((*posts)[0]["card"].(string), "答一") || (*posts)[0]["reply_to"] != "om_m1" {
		t.Fatalf("posts %v", *posts)
	}
}

// feishuEvent is the owner's text message id in the bot's chat, as feishu.message hands it over.
func feishuEvent(id, text string) FeishuMessage {
	raw, _ := json.Marshal(map[string]any{"message": map[string]any{"message_id": id, "chat_id": "oc_1", "chat_type": "p2p",
		"message_type": "text", "content": fmt.Sprintf(`{"text":%q}`, text)}})
	return FeishuMessage{Event: raw}
}

// sentOnce are the client_msg_ids of the messages that went into a conversation, in order.
func sentOnce(f *fakeHub) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, c := range f.calls {
		if c.Method == DeviceSessionSendOnce {
			var p map[string]any
			_ = json.Unmarshal(c.Params, &p)
			ids = append(ids, fmt.Sprint(p["client_msg_id"]))
		}
	}
	return ids
}

// A message that is not handled still passes the turn on: one redelivered, one taken when the bot's status could not
// be read (the hub hands it again), a command, one the chat may not send. The message after each goes in.
func TestFeishuMessageNotHandledPassesTheTurnOn(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { return map[string]any{"reaction_id": "rx"}, nil })
	f.answerApp(AppFeishu, FeishuUnreact, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerApp(AppFeishu, FeishuText, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	st := &memStore{}
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t)}))
	s.fbot.mu.Lock()
	s.fbot.turnWait = time.Hour // a turn not passed on would hang the test, not wait it out
	s.fbot.mu.Unlock()
	ctx := context.Background()
	if err := s.FeishuMessage(ctx, feishuEvent("om_1", "一")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(sentOnce(f)) == 1 })
	steps := []struct {
		what   string
		before func()
		ev     FeishuMessage
		failed bool
	}{
		{what: "redelivered", ev: feishuEvent("om_1", "一")},
		{what: "a command", ev: feishuEvent("om_help", "/help")},
		{what: "the bot's status unread", failed: true, ev: feishuEvent("om_x", "状态读不到"), before: func() {
			f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return nil, errors.New("hub away") })
		}},
		{what: "the chat may not send", ev: feishuEvent("om_off", "关着"), before: func() {
			f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
				return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0)}, nil
			})
		}},
	}
	for _, st := range steps {
		if st.before != nil {
			st.before()
		}
		if err := s.FeishuMessage(ctx, st.ev); (err != nil) != st.failed {
			t.Fatalf("%s: %v", st.what, err)
		}
	}
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), Features: FeishuFeatures{Main: true}}, nil
	})
	if err := s.FeishuMessage(ctx, feishuEvent("om_2", "二")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(sentOnce(f)) == 2 })
	if got := strings.Join(sentOnce(f), " "); got != "feishu-om_1 feishu-om_2" {
		t.Fatalf("went in as %s", got)
	}
}

// stuckHub is a fake hub on which downloading message stuck's picture does not answer until let go.
type stuckHub struct {
	*fakeHub
	stuck   string
	letGo   chan struct{}
	pending chan struct{}
}

func (h stuckHub) AppCall(ctx context.Context, p AppCall) (json.RawMessage, error) {
	if p.AppID == AppFeishu && p.Method == FeishuResource {
		var m map[string]any
		_ = json.Unmarshal(p.Params, &m)
		if m["message_id"] == h.stuck {
			close(h.pending)
			select {
			case <-h.letGo:
			case <-ctx.Done():
			}
			return nil, errors.New("too slow")
		}
	}
	return h.fakeHub.AppCall(ctx, p)
}

// A message stuck before it goes into the conversation (a picture slow to download) holds the next one up only so
// long: the next goes in without it.
func TestFeishuStuckMessageHoldsTheNextOnlySoLong(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { return map[string]any{"reaction_id": "rx"}, nil })
	f.answerApp(AppFeishu, FeishuUnreact, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerApp(AppFeishu, FeishuText, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	h := stuckHub{fakeHub: f, stuck: "om_pic", letGo: make(chan struct{}), pending: make(chan struct{})}
	defer close(h.letGo)
	st := &memStore{}
	s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return st }, Logf: testLogf(t)}))
	s.fbot.mu.Lock()
	s.fbot.turnWait = 200 * time.Millisecond
	s.fbot.mu.Unlock()
	ctx := context.Background()
	pic, _ := json.Marshal(map[string]any{"message": map[string]any{"message_id": "om_pic", "chat_id": "oc_1", "chat_type": "p2p",
		"message_type": "image", "content": `{"image_key":"img_1"}`}})
	if err := s.FeishuMessage(ctx, FeishuMessage{Event: pic}); err != nil {
		t.Fatal(err)
	}
	<-h.pending
	if err := s.FeishuMessage(ctx, feishuEvent("om_2", "二")); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return len(sentOnce(f)) == 1 })
	if got := sentOnce(f)[0]; got != "feishu-om_2" {
		t.Fatalf("went in %s", got)
	}
}

// The Feishu bot's first message to a new conversation (/new, or no main conversation yet) goes in with
// assistant.send: no client message id. A second one sent before it is answered is folded in by startChannelRun; the
// one answer still holds the first message's answer (#4746).
func TestNewConversationsFirstMessageFoldedIsAnswered(t *testing.T) {
	f := newFakeHub().withMachine()
	h := &pagedHub{fakeHub: f, page: 3, items: []proto.Item{
		{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "first"},
		{ID: "t2", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash"},
		{ID: "a3", Seq: 3, Role: "assistant", Kind: proto.KindText, Text: "first answer"},
		{ID: "u4", Seq: 4, Role: "user", Kind: proto.KindText, Text: "second", ClientMsgID: "feishu-om2"},
		{ID: "a5", Seq: 5, Role: "assistant", Kind: proto.KindText, Text: "second answer"},
		{ID: "d6", Seq: 6, Role: "system", Kind: "turn_duration", TurnDuration: &proto.TurnDuration{DurationMS: 5}},
	}}
	s := withSettings(t, f, New(Options{Hub: h, Store: func() Store { return &memStore{} }, Logf: testLogf(t)}))
	// As feishu_message.go's rt.isNew path leaves it, then as its session.send path does.
	s.startChannelRun(&channel.Run{ID: "fb_om1", Channel: channel.Feishu, ReplyTo: "om1", MachineID: "server", SessionID: "s1",
		TargetKind: targetPersona, Started: time.Now().UTC(), Ext: map[string]string{}})
	s.startChannelRun(&channel.Run{ID: "fb_om2", Channel: channel.Feishu, ReplyTo: "om2", MachineID: "server", SessionID: "s1",
		TargetKind: targetPersona, Base: 3, MsgIDs: []string{"feishu-om2"}, Probe: "second", Started: time.Now().UTC(),
		Ext: map[string]string{}})
	runs := s.channelRuns(channel.Feishu)
	if len(runs) != 1 || runs[0].ID != "fb_om2" || len(runs[0].Folded) != 1 {
		t.Fatalf("runs %+v", runs)
	}
	rd, err := s.readTurn(context.Background(), &runs[0])
	if err != nil || !rd.Found || rd.Answer != "first answer\n\nsecond answer" {
		t.Fatalf("%+v %v", rd, err)
	}
}
