package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// titleTopic takes markdown only for syntax at the start of a line and a paired inline wrap; the same characters
// mid-line are text (#3869). Tag blocks keep coming out whole.
func TestTitleTopic(t *testing.T) {
	for in, want := range map[string]string{
		"修复 C# 编译错误":                     "修复 C# 编译错误",
		"A* 寻路":                          "A* 寻路",
		"跟进 #3471":                       "跟进 #3471",
		"a>b":                            "a>b",
		"### 修复 C#":                      "修复 C#",
		"> 引用风格标题":                       "引用风格标题",
		"- 待办":                           "待办",
		"+ 加号列表":                         "加号列表",
		">x 无空格引用":                       "x 无空格引用",
		"`code` 包裹":                      "code 包裹",
		"**加粗**标题":                       "加粗标题",
		"**没闭合的加粗":                       "**没闭合的加粗", // a lone marker is not a wrap
		"`只有左反引号":                        "`只有左反引号",
		"⏰ 定时任务":                         "定时任务",
		"提醒 ⏰ 开会":                        "提醒 ⏰ 开会", // the clock only goes at the start of a line
		`<at id="u1">某人</at> 跟进`:         "跟进",
		`<at>某人 跟进`:                      `<at>某人 跟进`, // a tag without its closing pair is not a block, and the > stays
		"# A\n- B\n* C#":                 "A B C#",
		"> - 嵌套的引用列表":                    "嵌套的引用列表",
		"- # 修复 #3471":                   "修复 #3471",
		">>>> 多层引用":                      "多层引用",
		"####### 七级井号":                   "####### 七级井号", // seven hashes are not a heading
		"##no-space":                     "##no-space",
		strings.Repeat(">", 20000) + "x": "x", // a long quote chain is one anchored, linear match
		strings.Repeat("修", 20):          strings.Repeat("修", maxTitleTopic),
	} {
		if got := titleTopic(in); got != want {
			t.Errorf("titleTopic(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTitle(t *testing.T) {
	for _, c := range []struct {
		out, kind, topic string
		ok               bool
	}{
		{"修复 | 搜索框和顶栏重叠", "修复", "搜索框和顶栏重叠", true},
		{"  \n「研究｜竞品定价」\n", "研究", "竞品定价", true},
		{"1101 | 发布 | 周五灰度安排。", "发布", "周五灰度安排", true}, // a date it added anyway, a full stop
		{"闲聊 | 周末去哪", "", "周末去哪", false},              // not a kind
		{"修复 | 搜索框在搜索时和顶栏重叠而且键盘弹起后错位", "修复", "搜索框在搜索时和顶栏重叠而且键盘弹起后错位", false},
		{"修复搜索框", "", "", false},
		{"", "", "", false},
	} {
		kind, topic, ok := parseTitle(c.out)
		if kind != c.kind || topic != c.topic || ok != c.ok {
			t.Errorf("parseTitle(%q) = %q %q %v", c.out, kind, topic, ok)
		}
	}
}

// MMDD is the day in the user's time zone though the hub runs in UTC; a broken answer is asked for once more, then
// cut or replaced by "explore | the first 14 characters".
func TestMakeTitle(t *testing.T) {
	started := time.Date(2026, 11, 1, 16, 30, 0, 0, time.UTC) // 00:30 on 11-02 in Shanghai
	ctx := context.Background()
	first, reply := "搜索的时候搜索框和顶栏叠在一起了，帮我看看", "是 safeAreaBar 和搜索框抽屉一起用导致的……"
	model := func(replies ...string) (*Service, *fakeHub) {
		f := newFakeHub()
		f.settings = &Settings{Timezone: "Asia/Shanghai"}
		f.replies = replies
		return newTestService(t, f), f
	}

	s, f := model("修复 | 搜索框和顶栏重叠")
	if got, _ := s.makeTitle(ctx, started, first, reply); got != "1102 | 修复 | 搜索框和顶栏重叠" {
		t.Fatalf("%q", got)
	}
	if c := f.modelCalls(); len(c) != 1 || !strings.Contains(c[0].User, first) || !strings.Contains(c[0].User, reply) {
		t.Fatalf("the model reads the first exchange: %+v", c)
	}

	s, f = model("Bug fix: search bar", "修复 | 搜索框和顶栏重叠")
	if got, _ := s.makeTitle(ctx, started, first, reply); got != "1102 | 修复 | 搜索框和顶栏重叠" {
		t.Fatalf("retried once: %q", got)
	}
	if c := f.modelCalls(); len(c) != 2 || !strings.Contains(c[1].User, "Bug fix: search bar") {
		t.Fatalf("the retry says what was wrong: %+v", c)
	}

	s, _ = model("修复 | 搜索框在搜索时和顶栏重叠而且键盘弹起后错位")
	if got, _ := s.makeTitle(ctx, started, first, reply); got != "1102 | 修复 | 搜索框在搜索时和顶栏重叠而且" {
		t.Fatalf("a topic still too long is cut: %q", got)
	}

	s, _ = model("随便聊聊")
	if got, _ := s.makeTitle(ctx, started, "<persona-context>\n今天\n</persona-context>\n**帮我** 看看明天的会议安排和要准备的材料", ""); got != "1102 | 探索 | 帮我 看看明天的会议安排和要" {
		t.Fatalf("the fallback: %q", got)
	}
	if got, _ := s.makeTitle(ctx, started, "<persona-context>\n今天\n</persona-context>\n**帮我** 看看明天的会议安排和要准备的材料", ""); got != "1102 | 探索 | 帮我 看看明天的会议安排和要" {
		t.Fatalf("the fallback past a <persona-context>: %q", got)
	}
	s, f = model()
	f.noModel = true
	if got, _ := s.makeTitle(ctx, started, "", ""); got != "1102 | 探索 | 新对话" {
		t.Fatalf("no model, no message: %q", got)
	}
}

// shortTitleCalls gives each title call d and the whole naming room for a few of them, for this test.
func shortTitleCalls(t *testing.T, d time.Duration) {
	t.Helper()
	call, budget := titleCallTimeout, titleBudget
	titleCallTimeout, titleBudget = d, 20*d
	t.Cleanup(func() { titleCallTimeout, titleBudget = call, budget })
}

// titleService is a test service in Shanghai whose title model gives replies, each as answers says.
func titleService(t *testing.T, replies []string, answers ...fakeAnswer) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	f.replies, f.answers = replies, answers
	return newTestService(t, f), f
}

const (
	searchAsk   = "搜索的时候搜索框和顶栏叠在一起了，帮我看看"
	searchReply = "是 safeAreaBar 和搜索框抽屉一起用导致的"
	searchTitle = "修复 | 搜索框和顶栏重叠"
)

// The title model is slower than one call's time now and then (a thinking model behind the gateway: p90 over 20 s,
// #1532): the call that ran out is made once more, and the conversation gets the model's title, not the first message
// cut short.
func TestMakeTitleOutlastsASlowCall(t *testing.T) {
	shortTitleCalls(t, 200*time.Millisecond)
	started := time.Date(2026, 11, 1, 16, 30, 0, 0, time.UTC)
	s, f := titleService(t, []string{searchTitle}, fakeAnswer{delay: 2 * time.Second}) // the next answer comes at once
	got, final := s.makeTitle(context.Background(), started, searchAsk, searchReply)
	if got != "1102 | "+searchTitle || !final {
		t.Fatalf("a slow call is made again: %q %v", got, final)
	}
	if n := len(f.modelCalls()); n != 2 {
		t.Fatalf("asked %d times, want 2", n)
	}
}

// A thinking model spends its thinking from the reply's max_tokens: a reply that stopped there is half a title even when
// it has the format (#1532). It is not taken: the model is asked again with more room.
func TestMakeTitleDropsACutOffReply(t *testing.T) {
	started := time.Date(2026, 11, 1, 16, 30, 0, 0, time.UTC)
	s, _ := titleService(t, []string{"修复 | 搜索", searchTitle}, fakeAnswer{stop: "max_tokens"})
	if got, _ := s.makeTitle(context.Background(), started, searchAsk, searchReply); got != "1102 | "+searchTitle {
		t.Fatalf("a cut-off reply is not a title: %q", got)
	}
}

// Each way an answer goes wrong has its own second call: a call that ran out of time does not use up the format's
// retry, nor a cut-off reply the call that ran out after it.
func TestMakeTitleRetriesDontCrowdEachOtherOut(t *testing.T) {
	shortTitleCalls(t, 200*time.Millisecond)
	started := time.Date(2026, 11, 1, 16, 30, 0, 0, time.UTC)
	slow := fakeAnswer{delay: 2 * time.Second}
	for _, c := range []struct {
		name    string
		replies []string
		answers []fakeAnswer
	}{
		{"timed out, then the wrong format", []string{"", "Bug fix: search bar", searchTitle}, []fakeAnswer{slow}},
		{"cut off, then timed out", []string{"修复 | 搜索", "", searchTitle}, []fakeAnswer{{stop: "max_tokens"}, slow}},
	} {
		s, f := titleService(t, c.replies, c.answers...)
		got, final := s.makeTitle(context.Background(), started, searchAsk, searchReply)
		if got != "1102 | "+searchTitle || !final || len(f.modelCalls()) != 3 {
			t.Errorf("%s: %q %v after %d calls", c.name, got, final, len(f.modelCalls()))
		}
	}
	// An empty answer is a broken format like any other: asked once more, then the fallback.
	s, f := titleService(t, []string{""})
	if got, final := s.makeTitle(context.Background(), started, searchAsk, searchReply); got != "1102 | 探索 | 搜索的时候搜索框和顶栏叠在一" || !final || len(f.modelCalls()) != 2 {
		t.Fatalf("empty answers: %q %v after %d calls", got, final, len(f.modelCalls()))
	}
}

// When the model can't be reached (its gateway down), the conversation shows the stand-in ("explore | the first
// message") and is named again after a later turn, from its first turn, three namings in all; a title the user gave
// meanwhile stays (#1532). A model that isn't set up answers the same every time: that fallback is final.
func TestTitleNamedAgainAfterTheModelWasOutOfReach(t *testing.T) {
	shortTitleCalls(t, 200*time.Millisecond)
	ctx := context.Background()
	started := time.Now() // a conversation still waiting for its title a day on is forgotten
	shanghai, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	day := titleDay(started, shanghai)
	standIn := day + " | 探索 | 搜索的时候搜索框和顶栏叠在一"
	s, f := titleService(t, nil)
	f.pageTurns = true
	f.setItems(proto.Item{Role: "user", Kind: proto.KindText, Text: searchAsk},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: searchReply},
		proto.Item{Role: "user", Kind: proto.KindText, Text: "顺便把设置页的开关也对齐一下"},
		proto.Item{Role: "assistant", Kind: proto.KindText, Text: "开关已经对齐了"})
	model := func(down bool, reply string) {
		f.mu.Lock()
		f.modelDown, f.noModel, f.replies, f.asked = down, false, []string{reply}, nil
		f.mu.Unlock()
	}
	title := func(id string) string {
		got, ok := s.conversationTitles()
		if !ok {
			t.Fatal("titles unread")
		}
		return got[id].title
	}

	model(true, "")
	s.titleNew("server", "c1", started)
	s.titleAfterTurn(ctx, "server", "c1")
	if got := title("c1"); got != standIn {
		t.Fatalf("the stand-in while the model is out of reach: %q", got)
	}
	model(false, searchTitle)
	s.titleAfterTurn(ctx, "server", "c1")
	if got := title("c1"); got != day+" | "+searchTitle {
		t.Fatalf("named after the next turn: %q", got)
	}
	if c := f.modelCalls(); len(c) != 1 || !strings.Contains(c[0].User, searchAsk) || strings.Contains(c[0].User, "设置页") {
		t.Fatalf("named from the first turn, not the newest: %+v", c)
	}
	model(false, "功能 | 别的事")
	s.titleAfterTurn(ctx, "server", "c1")
	if got := title("c1"); got != day+" | "+searchTitle {
		t.Fatalf("a conversation the model named is named once: %q", got)
	}

	model(true, "")
	s.titleNew("server", "c2", started)
	s.titleAfterTurn(ctx, "server", "c2")
	s.titleSet("server", "c2", "我起的名字", titleByUser)
	model(false, searchTitle)
	s.titleAfterTurn(ctx, "server", "c2")
	if got := title("c2"); got != "我起的名字" {
		t.Fatalf("the user's title stays: %q", got)
	}

	model(true, "")
	s.titleNew("server", "c3", started)
	for range 3 {
		s.titleAfterTurn(ctx, "server", "c3")
	}
	model(false, searchTitle)
	s.titleAfterTurn(ctx, "server", "c3")
	if got := title("c3"); got != standIn {
		t.Fatalf("three namings that never got an answer are the last: %q", got)
	}

	f.mu.Lock()
	f.noModel = true // the model isn't usable as configured: it says so at once, every time
	f.mu.Unlock()
	s.titleNew("server", "c4", started)
	s.titleAfterTurn(ctx, "server", "c4")
	model(false, searchTitle)
	s.titleAfterTurn(ctx, "server", "c4")
	if got := title("c4"); got != standIn {
		t.Fatalf("a model that turned the naming down is not asked again: %q", got)
	}
}
