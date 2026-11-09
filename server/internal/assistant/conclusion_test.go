package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// conclusionService is newSupervisedService with the Lark bot on (task reports on, only_conclusions as given): the cards it
// posts and the notes it writes are kept.
func conclusionService(t *testing.T, onlyConclusions bool) (s *Service, f *fakeHub, todo Todo, posts, notes func() []string) {
	t.Helper()
	return conclusionServiceOn(t, nil, onlyConclusions)
}

// conclusionServiceOn is conclusionService with st as its database (nil: none).
func conclusionServiceOn(t *testing.T, st Store, onlyConclusions bool) (s *Service, f *fakeHub, todo Todo, posts, notes func() []string) {
	t.Helper()
	s, f, todo = newSupervisedServiceOn(t, st)
	s.reports.mu.Lock()
	s.reports.delay = testReportDelay
	s.reports.mu.Unlock()
	var mu sync.Mutex
	var cards, said []string
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", PublicURL: "https://hub.example.com",
			Features: FeishuFeatures{Main: true, Cards: true, Reports: true, OnlyConclusions: onlyConclusions}}, nil
	})
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m struct {
			Card string `json:"card"`
		}
		_ = json.Unmarshal(p, &m)
		mu.Lock()
		cards = append(cards, m.Card)
		mu.Unlock()
		return map[string]any{"message_id": "om_c"}, nil
	})
	f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m map[string]string
		_ = json.Unmarshal(p, &m)
		mu.Lock()
		said = append(said, m["text"])
		mu.Unlock()
		return map[string]any{}, nil
	})
	keep := func(list *[]string) func() []string {
		return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), *list...) }
	}
	return s, f, todo, keep(&cards), keep(&said)
}

// reviewTask runs task_review on todo from the main conversation and returns what it answers.
func reviewTask(t *testing.T, s *Service, todo string, args map[string]any) string {
	t.Helper()
	args["todo_id"] = todo
	raw, _ := json.Marshal(args)
	res, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw)
	if err != nil {
		t.Fatalf("task_review %v: %v", args, err)
	}
	return res
}

// waitsForReview has the task's agent finish a turn with reply until the judge's "done" puts the task up for review.
func waitsForReview(t *testing.T, s *Service, f *fakeHub, todo, reply string) {
	t.Helper()
	f.judge = `{"verdict":"done","reason":"调研有结论了"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, reply, 0)
	waitFor(t, "the review", func() bool { return todoNow(s, todo).Supervision.State == reviewPending })
}

func cardsWith(posts []string, want string) (out []string) {
	for _, p := range posts {
		if strings.Contains(p, want) {
			out = append(out, p)
		}
	}
	return out
}

// A supervised research task that ends with "cannot be done" is one card: that verdict, the reason, the link; its
// closing is not a line of the task report as well. "OK" to it is taken, and said so.
func TestSupervisedCannotBeDoneIsOneConclusionCard(t *testing.T) {
	s, f, todo, posts, notes := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "查了，对方接口不对外开放，直连做不了。文档 https://docs.example.com/api")
	waitFor(t, "the turn's report", func() bool { return len(cardsWith(posts(), "这一轮做完")) == 1 }) // the switch is off: as before
	before := len(posts())
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "outcome": "cannot", "reason": "对方接口不对外开放",
		"link": "https://docs.example.com/api", "note": "调研结论：做不了"})
	if !strings.Contains(res, "conclusion card") {
		t.Fatalf("the review's answer does not say the card went out: %s", res)
	}
	time.Sleep(6 * testReportDelay) // a closing line of the task report would be out by now
	after := posts()[before:]
	if len(after) != 1 {
		t.Fatalf("%d cards after the review, want the conclusion only:\n%s", len(after), strings.Join(after, "\n---\n"))
	}
	card := after[0]
	for _, want := range []string{"结论：修登录崩溃", "⛔ 做不了：对方接口不对外开放", "https://docs.example.com/api", "知道了", `"a":"accept"`} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, "🏁") || strings.Contains(card, "改了什么") {
		t.Fatalf("not a conclusion card:\n%s", card)
	}
	a := askOf(s, todo.ID)
	if a == nil || !a.Conclusion || a.Outcome != outcomeCannot || a.MessageID == "" {
		t.Fatalf("ask %+v", a)
	}
	if !s.acceptReply(context.Background(), "om_ok", "OK", false, a.MessageID) {
		t.Fatal("a reply to the conclusion card was not taken as one")
	}
	if a := askOf(s, todo.ID); a.AcceptedAt.IsZero() {
		t.Fatal("OK did not take it")
	}
	if n := notes(); len(n) == 0 || n[len(n)-1] != "知道了，已收下" {
		t.Fatalf("notes %q", n)
	}
}

// A conclusion with no link given takes the task's own: a web address in the review's evidence before the agent's chat.
func TestConclusionFindsItsLink(t *testing.T) {
	s, f, todo, posts, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "做完了")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "做完了",
		"checks": []ReviewCheck{{Criterion: "PR 合并到 master", Met: true, Evidence: "https://github.com/o/r/pull/7 merged"}}})
	cards := cardsWith(posts(), "结论：")
	if len(cards) != 1 || !strings.Contains(cards[0], "https://github.com/o/r/pull/7") || !strings.Contains(cards[0], "✅ 做完了：做完了") {
		t.Fatalf("cards %q", cards)
	}
}

// Deployed work keeps its "Please verify" card, now with the verdict line on top; the review that accepts the task then
// sends no second card.
func TestDeployedWorkKeepsPleaseVerifyWithTheVerdictOnTop(t *testing.T) {
	s, f, todo, posts, _ := conclusionService(t, false)
	f.judge = `{"changed":"飞书机器人多了任务进展开关","link":"https://hub.example.com/settings?tab=feishu","how":"打开设置看开关"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, deployedReply, 0)
	waitFor(t, "the 请验收 card", func() bool { a := askOf(s, todo.ID); return a != nil && a.MessageID != "" })
	card := acceptCardOf(posts())
	top, lines := strings.Index(card, "✅ 做完了：已部署，按下面验收"), strings.Index(card, "改了什么")
	if top < 0 || lines < 0 || top > lines {
		t.Fatalf("the verdict line is not on top of the three lines:\n%s", card)
	}
	f.judge = `{"verdict":"done","reason":"已部署"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, deployedReply, 0)
	waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "上线了"})
	time.Sleep(6 * testReportDelay)
	if n := len(cardsWith(posts(), "结论：")); n != 0 {
		t.Fatalf("%d conclusion cards for deployed work", n)
	}
	if n := countAcceptCards(posts()); n != 1 {
		t.Fatalf("%d 请验收 cards", n)
	}
	// The review closed the task, and its "Please verify" is what tells the owner: it still waits for them.
	ctx := context.Background()
	if _, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 1 || askOf(s, todo.ID) == nil {
		t.Fatalf("the ask is gone after the review: %d waiting", total)
	}
	var listed bool
	for _, r := range s.boardRows(ctx, s.botLang(ctx), s.webBase(ctx), time.Now()) {
		listed = listed || r.id == todo.ID
	}
	if !listed {
		t.Fatal("not on the board after the review")
	}
}

// A task sent back twice and still short goes to the owner: the card asks how to go on, not with the rework the agent
// was told.
func TestReworkOverAsksTheOwnerHowToGoOn(t *testing.T) {
	s, f, todo, posts, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "补了一部分")
	if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool { v.Round = maxReworkRounds; return true }); err != nil {
		t.Fatal(err)
	}
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "note": "把深色截图补上，再跑一遍 CI"})
	if !strings.Contains(res, "sent back 2 times") || !strings.Contains(res, "went to them in Lark") {
		t.Fatalf("answer: %s", res)
	}
	cards := cardsWith(posts(), "结论：")
	if len(cards) != 1 || !strings.Contains(cards[0], "🤔 要你拍板：返工了 2 轮还没做完，接下来怎么办？") || strings.Contains(cards[0], "深色截图") {
		t.Fatalf("cards %q", cards)
	}
}

// "Needs your decision" carries the recommended option: "OK" takes it, which goes into the task's agent; the card is
// over then.
func TestDecisionCarriesTheRecommendationAndOKTakesIt(t *testing.T) {
	s, f, todo, posts, notes := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%",
		"note": "要用户定上线方式"})
	if !strings.Contains(res, `"OK" takes the recommendation`) {
		t.Fatalf("answer: %s", res)
	}
	cards := cardsWith(posts(), "结论：")
	if len(cards) != 1 {
		t.Fatalf("%d conclusion cards", len(cards))
	}
	for _, want := range []string{"🤔 要你拍板：先灰度还是直接全量", "推荐：先灰度 10%", "按推荐来", "回「OK」就按推荐来"} {
		if !strings.Contains(cards[0], want) {
			t.Errorf("card lacks %q:\n%s", want, cards[0])
		}
	}
	mid := askOf(s, todo.ID).MessageID
	sent := len(sentToAgent(f))
	if !s.acceptReply(context.Background(), "om_ok", "OK", false, mid) {
		t.Fatal("not taken")
	}
	got := sentToAgent(f)[sent:]
	if len(got) != 1 || !strings.Contains(got[0], "先灰度 10%") {
		t.Fatalf("to the agent: %q", got)
	}
	if n := notes(); len(n) == 0 || !strings.Contains(n[len(n)-1], "按推荐来") {
		t.Fatalf("notes %q", n)
	}
	// Decided: the task no longer waits for the owner, so the agent's next stop goes back to the supervisor.
	if st := todoNow(s, todo.ID).Supervision.State; st == reviewNeedsUser {
		t.Fatalf("still waits for the owner: %q", st)
	}
	// Taken once: a second "OK" takes nothing more.
	s.acceptReply(context.Background(), "om_ok2", "OK", false, mid)
	if n := len(sentToAgent(f)); n != sent+1 {
		t.Fatalf("%d sends after a second OK", n-sent)
	}
}

// With only_conclusions on, a task's turn ending (an intermediate state) is not pushed; its conclusion still is.
func TestOnlyConclusionsPushesNoProgress(t *testing.T) {
	s, f, todo, posts, _ := conclusionService(t, true)
	waitsForReview(t, s, f, todo.ID, "查完了，可以做，方案见 https://docs.example.com/plan")
	time.Sleep(6 * testReportDelay) // a report of the turn would be out by now
	if p := posts(); len(p) != 0 {
		t.Fatalf("pushed before the conclusion:\n%s", strings.Join(p, "\n---\n"))
	}
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "reason": "能做，方案已写好", "link": "https://docs.example.com/plan"})
	time.Sleep(6 * testReportDelay)
	if p := posts(); len(p) != 1 || !strings.Contains(p[0], "✅ 做完了：能做，方案已写好") {
		t.Fatalf("posts:\n%s", strings.Join(p, "\n---\n"))
	}
}

// The same switch off: the turn's report goes out as before.
func TestWithoutOnlyConclusionsProgressIsPushed(t *testing.T) {
	s, f, todo, posts, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "查完了，可以做")
	waitFor(t, "the turn's report", func() bool { return len(cardsWith(posts(), "这一轮做完")) == 1 })
}

// The web briefing, the briefing's Lark card and the board say the conclusion in the same words as its own card.
func TestBriefingSaysTheSameVerdictAsTheCard(t *testing.T) {
	s, f, todo, posts, _ := conclusionService(t, false)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "查了，做不了")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "outcome": "cannot", "reason": "对方接口不对外开放", "link": "https://docs.example.com/api"})
	const verdict = "⛔ 做不了：对方接口不对外开放"
	if len(cardsWith(posts(), feishubot.EscapeMarkdown(verdict))) != 1 {
		t.Fatalf("the card does not say %q", verdict)
	}
	facts := s.briefFactsAt(ctx, time.Now(), "")
	body := facts.body(s, "早报")
	var row *briefBodyRow
	for i := range body.Accept {
		if body.Accept[i].ID == todo.ID {
			row = &body.Accept[i]
		}
	}
	if row == nil || row.Verdict != verdict || row.Outcome != outcomeCannot {
		t.Fatalf("the brief's row %+v", row)
	}
	card, _, _ := s.briefCard(facts, "早报", "")
	if js := card.JSON(); !strings.Contains(js, feishubot.EscapeMarkdown(verdict)) || !strings.Contains(js, "知道了") {
		t.Fatalf("the briefing card does not say it:\n%s", js)
	}
	var onBoard bool
	for _, r := range s.boardRows(ctx, s.botLang(ctx), s.webBase(ctx), time.Now()) {
		onBoard = onBoard || r.id == todo.ID && strings.Contains(r.summary, verdict)
	}
	if !onBoard {
		t.Fatal("the board does not say it")
	}
}

// A recommendation that cannot reach the task's agent is not taken: the button answers why (not "already handled"), the
// reply is told it did not go, nothing is recorded as decided, and the next press takes it once the agent is reachable.
func TestRecommendationThatCannotReachTheAgentIsNotTaken(t *testing.T) {
	s, f, todo, _, notes := conclusionService(t, false)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	a := askOf(s, todo.ID)
	f.mu.Lock()
	f.failSend = errors.New("node down")
	f.mu.Unlock()
	if ok, err := s.acceptTryFromBrief(ctx, todo.ID, a.AskedAt.UTC().Format(timeKeyLayout)); ok || err == nil {
		t.Fatalf("taken while the agent cannot be reached: %v %v", ok, err)
	}
	// The web button: 502 with the reason, not 409 "already handled" (the web app toasts it and the button works again).
	asked, _ := json.Marshal(a.AskedAt)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/todos/"+todo.ID+"/accept", strings.NewReader(`{"asked_at":`+string(asked)+`}`)))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "node down") {
		t.Fatalf("the web button: %d %s", rec.Code, rec.Body.String())
	}
	// The Lark button: an error toast with the reason, saying nothing was taken (not "kept in the todo").
	notTaken := i18n.T(s.botLang(ctx), "assistant-reports.accept_not_taken", i18n.Args{"error": "node down"})
	press := &feishubot.CardAction{}
	press.Action.Value = map[string]any{"a": "accept", "id": todo.ID}
	if r := s.botCardAction(ctx, press, ""); r == nil || r.Toast == nil ||
		r.Toast.Type != "error" || r.Toast.Content != notTaken {
		t.Fatalf("the Lark button: %+v", r)
	}
	s.acceptReply(ctx, "om_ok", "OK", false, a.MessageID)
	if n := notes(); len(n) == 0 || n[len(n)-1] != notTaken {
		t.Fatalf("the reply is not told it did not go: %q", n)
	}
	if got := askOf(s, todo.ID); !got.AcceptedAt.IsZero() || todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatalf("taken after all: %+v", got)
	}
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	if ok, err := s.acceptTry(ctx, todo.ID, ""); !ok || err != nil {
		t.Fatalf("not taken once the agent is reachable: %v %v", ok, err)
	}
	if got := sentToAgent(f); len(got) == 0 || !strings.Contains(got[len(got)-1], "先灰度 10%") {
		t.Fatalf("to the agent: %q", got)
	}
}

// A recommendation is recorded in the to-do's decisions only once it reached the agent: a send that fails records nothing,
// and the retry that goes through records it once.
func TestRecommendationIsRecordedOnlyOnceItReachedTheAgent(t *testing.T) {
	database := todoTestDB(t)
	reachA, reachB, reachM := agentReachAttempts, agentReachBackoff, agentReachMax
	agentReachAttempts, agentReachBackoff, agentReachMax = 2, time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { agentReachAttempts, agentReachBackoff, agentReachMax = reachA, reachB, reachM })
	f := newFakeHub().withMachine()
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Title: "上线", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle})
	s := newKBServiceOn(t, f, database)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "上线方式", "定上线方式", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1", "上线", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	decisions := func() []db.TodoDecisionRow {
		list, err := database.ListTodoDecisions(ctx, todo.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	a := acceptAsk{Conclusion: true, Outcome: outcomeDecide, Reason: "先灰度还是直接全量", Recommend: "先灰度 10%"}
	f.mu.Lock()
	f.failSend = errors.New("node down")
	f.mu.Unlock()
	if err := s.takeRecommendation(ctx, todo.ID, a); err == nil {
		t.Fatal("no error while the agent cannot be reached")
	}
	if list := decisions(); len(list) != 0 {
		t.Fatalf("recorded though it did not reach the agent: %+v", list)
	}
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	if err := s.takeRecommendation(ctx, todo.ID, a); err != nil {
		t.Fatal(err)
	}
	if list := decisions(); len(list) != 1 || list[0].Choice != "先灰度 10%" {
		t.Fatalf("decisions after the retry: %+v", list)
	}
}

// The web button's 502 for a recommendation that did not reach the agent says nothing was taken: it used to say the
// choice was kept in the to-do, while no decision was recorded and the ask still waits.
func TestAcceptRouteSaysTheRecommendationWasNotTaken(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo, _, _ := conclusionServiceOn(t, database, false)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	decisions := func() int {
		list, err := database.ListTodoDecisions(ctx, todo.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	before := decisions()
	a := askOf(s, todo.ID)
	f.mu.Lock()
	f.failSend = errors.New("node down")
	f.mu.Unlock()
	asked, _ := json.Marshal(a.AskedAt)
	status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/accept", "zh-Hans", `{"asked_at":`+string(asked)+`}`)
	want := i18n.T(i18n.ZhHans, "assistant-reports.accept_not_taken", i18n.Args{"error": "node down"})
	if status != http.StatusBadGateway || body["error"] != want {
		t.Fatalf("%d %v, want %q", status, body, want)
	}
	if n := decisions(); n != before {
		t.Fatalf("decisions %d → %d though nothing was taken", before, n)
	}
	if got := askOf(s, todo.ID); !got.AcceptedAt.IsZero() {
		t.Fatalf("taken after all: %+v", got)
	}
}

// The web button's 502 says it in the request's language (whatever the bot's), as its error: a plain error there became
// the generic "the request did not complete" with the sentence moved to its detail. A reason the hub words itself (an
// offline computer) reads in that language too, not the default one.
func TestAcceptRouteNotTakenReadsInTheRequestLanguage(t *testing.T) {
	for _, tc := range []struct {
		bot, request string
		want         i18n.Lang
	}{
		{"en", "zh-Hans", i18n.ZhHans},
		{"zh-Hans", "en", i18n.En},
	} {
		s, f, todo, _, _ := conclusionService(t, false)
		f.mu.Lock()
		f.hello.Locale = tc.bot
		f.mu.Unlock()
		waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
		reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
		a := askOf(s, todo.ID)
		f.mu.Lock()
		f.failSend = i18n.Err("assistant-tasks.route_offline")
		f.mu.Unlock()
		asked, _ := json.Marshal(a.AskedAt)
		status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/accept", tc.request, `{"asked_at":`+string(asked)+`}`)
		want := i18n.T(tc.want, "assistant-reports.accept_not_taken", i18n.Args{"error": i18n.T(tc.want, "assistant-tasks.route_offline")})
		if status != http.StatusBadGateway || body["error"] != want {
			t.Fatalf("bot %s, request %s: %d %v, want %q", tc.bot, tc.request, status, body, want)
		}
	}
}
