package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The "Can wait" section of the morning and evening briefings (briefing*.go, later.go): the questions Agents left for the
// next briefing. All ids are placeholders.

// later is a question of an Agent (session ag_<id>) on to-do todo, asked age ago, put into the queue.
func (e *briefRunEnv) later(id, todo, text string, age time.Duration) {
	e.t.Helper()
	if _, ok := e.s.todos.todos[todo]; !ok {
		e.s.todos.todos[todo] = &Todo{ID: todo, Title: todo, Origin: "conv1"}
	}
	if _, ok, _ := e.s.laterQueue(context.Background(), todo, "server", "ag_"+id, text, time.Now().Add(-age)); !ok {
		e.t.Fatalf("question %s was not queued", id)
	}
}

// later is the same for the facts-only tests.
func (e *briefEnv) later(todo, text string, age time.Duration) laterAsk {
	e.s.todos.todos[todo] = &Todo{ID: todo, Title: todo, Origin: "conv1"}
	a, ok, _ := e.s.laterQueue(context.Background(), todo, "box", "ag1", text, e.clk.Now().Add(-age))
	if !ok {
		panic("not queued")
	}
	return a
}

// The facts list the queue like the board's rows: what was asked, who asked, how long ago, a link to the Agent's chat; the
// longest waiting first.
func TestBriefListsQuestionsThatCanWait(t *testing.T) {
	e := newBriefEnv(t)
	e.later("todo_a", "发布说明里要不要写内部编号？", 5*time.Hour)
	e.later("todo_b", "新接口用 snake_case 还是 camelCase？\n两种都行", 30*time.Hour)
	e.later("todo_c", "图标要不要换？", time.Hour)

	f := e.s.briefFactsAt(context.Background(), e.clk.Now(), "")
	if f.LaterTotal != 3 || len(f.Later) != 3 {
		t.Fatalf("later: %d / %+v", f.LaterTotal, f.Later)
	}
	for i, want := range []string{"新接口用 snake_case 还是 camelCase？ 两种都行", "发布说明里要不要写内部编号？", "图标要不要换？"} {
		if f.Later[i].summary != want {
			t.Errorf("row %d: %q, want %q", i, f.Later[i].summary, want)
		}
	}
	first := f.Later[0]
	if first.waited != 30*time.Hour || first.blocking || !strings.HasPrefix(first.id, "lt_") || !strings.Contains(first.from, "Agent") ||
		first.link != testWeb+"/s/box/ag1" {
		t.Errorf("the first row: %+v", first)
	}
}

// At most briefRowsMax rows, the oldest first; the total says how many there are.
func TestBriefCutsTheQueueAtTenRows(t *testing.T) {
	e := newBriefEnv(t)
	for i := 0; i < briefRowsMax+2; i++ {
		e.later("todo_a", fmt.Sprintf("问题%02d", i), time.Duration(100-i)*time.Hour)
	}
	f := e.s.briefFactsAt(context.Background(), e.clk.Now(), "")
	if f.LaterTotal != briefRowsMax+2 || len(f.Later) != briefRowsMax || f.Later[0].summary != "问题00" || f.Later[briefRowsMax-1].summary != "问题09" {
		t.Fatalf("later: %d, %d rows, %q … %q", f.LaterTotal, len(f.Later), f.Later[0].summary, f.Later[len(f.Later)-1].summary)
	}
}

// The briefing card has a "Can wait" section after "To verify", each question with its own Handled button.
func TestBriefCardHasACanWaitSection(t *testing.T) {
	e := newBriefRunEnvWith(t, true, func(e *briefRunEnv) {
		e.later("1", "todo_a", "新接口用 snake_case 还是 camelCase？", 30*time.Hour)
		e.later("2", "todo_b", "图标要不要换？", time.Hour)
	})
	e.mirror()
	card := e.cards()[0]
	at := func(want string) int {
		t.Helper()
		i := strings.Index(card, want)
		if i < 0 {
			t.Fatalf("the card has no %q:\n%s", want, card)
		}
		return i
	}
	order := []int{at("待验收（1）"), at("请验收：修登录崩溃"), at("可以晚点答（2）"), at("新接口用"), at("图标要不要换")}
	for i := 1; i < len(order); i++ {
		if order[i] <= order[i-1] {
			t.Fatalf("out of order %v:\n%s", order, card)
		}
	}
	if n := strings.Count(card, `"a":"later_done"`); n != 2 || !strings.Contains(card, "已处理") {
		t.Fatalf("%d Handled buttons:\n%s", n, card)
	}
}

// With nothing in the queue the card has no such section (not an empty one every morning).
func TestBriefCardLeavesOutAnEmptyCanWaitSection(t *testing.T) {
	e := newBriefRunEnv(t, true)
	e.mirror()
	if card := e.cards()[0]; strings.Contains(card, "晚点答") || strings.Contains(card, "later_done") {
		t.Fatalf("an empty queue is on the card:\n%s", card)
	}
}

// Too many questions are not cut off in the middle of the card: briefRowsMax of them, the rest counted, inside the size Lark takes.
func TestBriefCardCountsQuestionsThatDoNotFit(t *testing.T) {
	e := newBriefRunEnvWith(t, true, func(e *briefRunEnv) {
		for i := 0; i < 13; i++ {
			e.later(fmt.Sprint(i), "todo_a", fmt.Sprintf("问题%02d", i), time.Duration(100-i)*time.Hour)
		}
	})
	e.mirror()
	card := e.cards()[0]
	if n := strings.Count(card, `"a":"later_done"`); n != briefRowsMax {
		t.Errorf("%d Handled buttons, want %d", n, briefRowsMax)
	}
	for _, want := range []string{"可以晚点答（13）", "问题09", "还有 3 项"} {
		if !strings.Contains(card, want) {
			t.Errorf("the card does not say %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, "问题10") {
		t.Errorf("an eleventh question is on the card:\n%s", card)
	}
	if len(card) > reportCardMaxBytes {
		t.Errorf("the card is %d bytes, past the %d it may take", len(card), reportCardMaxBytes)
	}
}

// The Handled button of the card takes the question out of the queue: the next briefing does not list it. Pressed again,
// or for a question that is not there, it answers "already handled".
func TestBriefHandledButtonOfTheCard(t *testing.T) {
	e := newBriefRunEnvWith(t, true, func(e *briefRunEnv) {
		e.later("1", "todo_a", "新接口用 snake_case 还是 camelCase？", 30*time.Hour)
		e.later("2", "todo_b", "图标要不要换？", time.Hour)
	})
	ctx := context.Background()
	first := e.s.briefFactsAt(ctx, time.Now(), "")
	if first.LaterTotal != 2 {
		t.Fatalf("later: %d", first.LaterTotal)
	}
	press := func(id string) *feishubot.CardResponse {
		var a feishubot.CardAction
		a.Action.Value = map[string]any{"a": "later_done", "id": id}
		a.Context.MessageID = "om_brief_1"
		return e.s.botCardAction(ctx, &a, "")
	}
	if r := press(first.Later[0].id); r == nil || r.Toast == nil || r.Toast.Type != "success" || !strings.Contains(r.Toast.Content, "已标记为处理过") {
		t.Fatalf("the press: %+v", r)
	}
	next := e.s.briefFactsAt(ctx, time.Now(), "")
	if next.LaterTotal != 1 || len(next.Later) != 1 || next.Later[0].summary != "图标要不要换？" {
		t.Fatalf("the next briefing: %d / %+v", next.LaterTotal, next.Later)
	}
	for _, id := range []string{first.Later[0].id, "lt_unknown"} {
		if r := press(id); r == nil || r.Toast == nil || r.Toast.Type != "info" || !strings.Contains(r.Toast.Content, "已经处理过了") {
			t.Fatalf("pressed again (%s): %+v", id, r)
		}
	}
	if n := e.s.briefFactsAt(ctx, time.Now(), "").LaterTotal; n != 1 {
		t.Fatalf("the second press changed the queue: %d", n)
	}
}

// The `brief` item carries the queue (its id is the one the web app's button sends), left out when it is empty.
func TestBriefItemCarriesTheQueue(t *testing.T) {
	e := newBriefRunEnvWith(t, true, func(e *briefRunEnv) {
		e.later("1", "todo_a", "新接口用 snake_case 还是 camelCase？", 30*time.Hour)
		e.later("2", "todo_b", "图标要不要换？", time.Hour)
	})
	items := e.briefItems()
	var b briefBody
	if len(items) != 1 || json.Unmarshal(items[0].Body, &b) != nil {
		t.Fatalf("items: %+v", items)
	}
	if b.LaterTotal != 2 || len(b.Later) != 2 || b.Later[0].Title != "新接口用 snake_case 还是 camelCase？" || !strings.HasPrefix(b.Later[0].ID, "lt_") ||
		b.Later[0].WaitedS < 30*3600 || b.Later[0].Link != testWeb+"/s/server/ag_1" || b.Later[1].Title != "图标要不要换？" {
		t.Fatalf("later: %d / %+v", b.LaterTotal, b.Later)
	}
	if raw := string(newBriefRunEnv(t, true).briefItems()[0].Body); strings.Contains(raw, `"later`) {
		t.Fatalf("an empty queue is in the body: %s", raw)
	}
}

// The push says how many can wait with the other counts.
func TestBriefPushCountsQuestionsThatCanWait(t *testing.T) {
	f := &briefFacts{l: i18n.ZhHans, Accept: make([]boardRow, 2), Later: make([]boardRow, 2), LaterTotal: 4}
	if got := f.countsIn(i18n.ZhHans); got != "待验收 2 · 可晚点答 4" {
		t.Fatalf("counts: %q", got)
	}
	if got := (&briefFacts{l: i18n.ZhHans, Later: make([]boardRow, 1), LaterTotal: 1}).countsIn(i18n.ZhHans); got != "可晚点答 1" {
		t.Fatalf("only a question that can wait: %q", got)
	}
}

// The model is told how many questions can wait and not which: they are in the card or the web app. With a card, and
// without one (when it would list the other rows itself).
func TestBriefModelIsToldHowManyNotWhich(t *testing.T) {
	for name, more := range map[string]func(e *briefRunEnv){
		"with a card":    nil,
		"without a card": func(e *briefRunEnv) { e.features(FeishuFeatures{Main: true}) },
	} {
		e := newBriefRunEnvWith(t, true, func(e *briefRunEnv) {
			if more != nil {
				more(e)
			}
			e.later("1", "todo_a", "新接口用 snake_case 还是 camelCase？", 30*time.Hour)
			e.later("2", "todo_b", "图标要不要换？", time.Hour)
		})
		sent, _ := e.f.lastSend(t)["text"].(string)
		if !strings.Contains(sent, "可以晚点答的问题 2 件") || strings.Contains(sent, "snake_case") || strings.Contains(sent, "图标要不要换") {
			t.Errorf("%s: the message to the model:\n%s", name, sent)
		}
	}
	plain, _ := newBriefRunEnv(t, true).f.lastSend(t)["text"].(string)
	if strings.Contains(plain, "晚点答") {
		t.Errorf("an empty queue is told to the model:\n%s", plain)
	}
}

// POST /api/v1/later/{id}/handled: the web app's Handled button. The first press takes the question out of the queue, a
// second one (or an unknown id) is 409, a read-only device 403 and leaves it there; and the hub relays the route.
func TestBriefLaterRoute(t *testing.T) {
	e := newBriefEnv(t)
	a := e.later("todo_a", "图标要不要换？", time.Hour)
	b := e.later("todo_b", "要不要写内部编号？", time.Hour)
	post := func(id, role string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/v1/later/"+id+"/handled", nil)
		if role != "" {
			req.Header.Set(RoleHeader, role)
		}
		e.s.ServeHTTP(rec, req)
		return rec.Code
	}
	ctx := context.Background()
	if code := post(a.ID, "read"); code != http.StatusForbidden || len(e.s.laterOpen(ctx, e.clk.Now())) != 2 {
		t.Fatalf("a read-only device: %d", code)
	}
	if code := post(a.ID, ""); code != http.StatusOK {
		t.Fatalf("handled: %d", code)
	}
	if open := e.s.laterOpen(ctx, e.clk.Now()); len(open) != 1 || open[0].ID != b.ID {
		t.Fatalf("queue: %+v", open)
	}
	for _, id := range []string{a.ID, "lt_unknown"} {
		if code := post(id, ""); code != http.StatusConflict {
			t.Fatalf("pressed again (%s): %d", id, code)
		}
	}
	found := false
	for _, r := range assistantwire.Routes {
		found = found || r == "POST /api/v1/later/{id}/handled"
	}
	if !found {
		t.Fatal("the hub does not relay the route (assistantwire.Routes)")
	}
}

// A Handled press whose change can't be written, or whose queue can't be read (a restart on an unreadable store), is an
// error: 503 for the web app and an error toast on the card, never "already handled". The question stays, and the next
// press once the store is back takes it out.
func TestBriefLaterRouteOnAStoreFailure(t *testing.T) {
	e := newBriefEnv(t)
	a := e.later("todo_a", "图标要不要换？", time.Hour)
	ctx := context.Background()
	post := func(s *Service, id string) int {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/later/"+id+"/handled", nil))
		return rec.Code
	}
	press := func(s *Service, id string) *feishubot.CardResponse {
		var c feishubot.CardAction
		c.Action.Value = map[string]any{"a": "later_done", "id": id}
		return s.botCardAction(ctx, &c, "")
	}
	unavailable := i18n.T(e.s.botLang(ctx), "http-status.unavailable")
	failed := func(s *Service, what string) {
		t.Helper()
		if code := post(s, a.ID); code != http.StatusServiceUnavailable {
			t.Errorf("%s: the route answered %d, want 503", what, code)
		}
		if r := press(s, a.ID); r == nil || r.Toast == nil || r.Toast.Type != "error" || r.Toast.Content != unavailable {
			t.Errorf("%s: the card answered %+v", what, r)
		}
	}

	e.st.failPut = errors.New("database unavailable")
	failed(e.s, "not written")
	e.st.failPut = nil

	restarted := newBoardEnvOn(t, e.st) // its to-dos are in memory: the question's one is still open
	restarted.s.todos.todos["todo_a"] = &Todo{ID: "todo_a", Title: "todo_a", Origin: "conv1"}
	e.st.failSetting = errors.New("database unavailable")
	failed(restarted.s, "not read")
	e.st.failSetting = nil

	if code := post(restarted.s, a.ID); code != http.StatusOK {
		t.Fatalf("the press after the store is back: %d", code)
	}
	if open := restarted.s.laterOpen(ctx, e.clk.Now()); len(open) != 0 {
		t.Fatalf("queue: %+v", open)
	}
}

// Who asked is the Agent by its title; an Agent that is gone, or has no title, is named by its to-do.
func TestBriefNamesAQuestionsAgentByItsTodoWhenItHasNoTitle(t *testing.T) {
	e := newBriefEnv(t)
	e.later("todo_a", "有标题的 Agent 问的", 2*time.Hour)
	e.s.todos.todos["todo_b"] = &Todo{ID: "todo_b", Title: "迁移数据库", Origin: "conv1"}
	e.s.laterQueue(context.Background(), "todo_b", "box", "ag_gone", "不在了的 Agent 问的", e.clk.Now().Add(-time.Hour))
	f := e.s.briefFactsAt(context.Background(), e.clk.Now(), "")
	if len(f.Later) != 2 || f.Later[0].from != "来自 Agent「登录修复」" || f.Later[1].from != "来自 Agent「迁移数据库」" {
		t.Fatalf("who asked: %+v", f.Later)
	}
}

// The push of a run says how many questions can wait with the other counts, from the facts the run started with.
func TestBriefRunPushSaysHowManyCanWait(t *testing.T) {
	e := newBriefRunEnvWith(t, true, func(e *briefRunEnv) {
		e.later("1", "todo_a", "新接口用 snake_case 还是 camelCase？", 30*time.Hour)
		e.later("2", "todo_b", "图标要不要换？", time.Hour)
	})
	if got := e.s.briefPush(e.sess.SessionID, i18n.ZhHans, true); !strings.Contains(got, "可晚点答 2") {
		t.Fatalf("the push: %q", got)
	}
}

// Every section over-full at once: the card stays inside Lark's element and size limits, and each "and N more" line is the last
// line of its section (never after a section that lost its heading).
func TestBriefCardWithEverySectionOverfullStaysInsideTheLimits(t *testing.T) {
	e := newBriefRunEnvWith(t, true, func(e *briefRunEnv) {
		var sessions []proto.Session
		for i := 0; i < 12; i++ {
			e.waiting(fmt.Sprintf("ix_more_%02d", i), fmt.Sprintf("再来一个问题%02d", i), time.Duration(i+1)*time.Minute)
			e.verify(fmt.Sprintf("todo_more_%02d", i), fmt.Sprintf("另一件事%02d", i), "改了点什么", time.Duration(i+2)*time.Hour)
			e.later(fmt.Sprint(i), "todo_a", fmt.Sprintf("可以晚点答的问题%02d", i), time.Duration(100-i)*time.Hour)
			at := time.Now().Add(-time.Duration(i+1) * time.Minute)
			e.s.todos.todos[fmt.Sprintf("todo_done_%02d", i)] = &Todo{ID: fmt.Sprintf("todo_done_%02d", i), Title: fmt.Sprintf("做完的事%02d", i), Origin: "conv1", DoneAt: &at}
			e.working(fmt.Sprintf("todo_w%02d", i), fmt.Sprintf("在做的事%02d", i), "develop", time.Duration(i+1)*time.Hour, &sessions)
		}
		e.f.setSessions("server", sessions...)
	})
	e.mirror()
	card := e.cards()[0]
	var v struct {
		Body struct {
			Elements []json.RawMessage `json:"elements"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(card), &v); err != nil {
		t.Fatal(err)
	}
	if len(card) > reportCardMaxBytes || len(v.Body.Elements) > reportCardMaxElements {
		t.Fatalf("the card is %d bytes and %d elements; Lark takes %d and %d", len(card), len(v.Body.Elements), reportCardMaxBytes, reportCardMaxElements)
	}
}
