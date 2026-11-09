package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// The to-do waiting for the owner carries its open ask (#2056): what the briefing's `accept[]` row says of it, so the apps
// can show the recommendation and take it with POST /api/v1/todos/{id}/accept.

// listedTodos is GET /api/v1/todos as the apps read it: each to-do as JSON, by id.
func listedTodos(t *testing.T, s *Service) map[string]map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/todos", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Todos []map[string]any `json:"todos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, x := range out.Todos {
		id, _ := x["id"].(string)
		byID[id] = x
	}
	return byID
}

// listedAsk is the `ask` of to-do id in GET /api/v1/todos (nil: none).
func listedAsk(t *testing.T, s *Service, id string) map[string]any {
	t.Helper()
	x, ok := listedTodos(t, s)[id]
	if !ok {
		t.Fatalf("to-do %s is not listed", id)
	}
	ask, _ := x["ask"].(map[string]any)
	if _, there := x["ask"]; there && ask == nil {
		t.Fatalf("to-do %s: ask %v", id, x["ask"])
	}
	return ask
}

// acceptAt presses OK for to-do id with asked_at as a client read it.
func acceptAt(s *Service, id, askedAt string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"asked_at": askedAt})
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/todos/"+id+"/accept", strings.NewReader(string(body))))
	return rec
}

// A decision with a recommended option is on its to-do while it waits: the verdict line, the option and the round, the
// same as the briefing's row; OK with that asked_at takes it, and from then on the to-do has no ask.
func TestTodoListCarriesTheDecisionWaiting(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	if ask := listedAsk(t, s, todo.ID); ask != nil {
		t.Fatalf("an ask before any conclusion: %v", ask)
	}
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	ask := listedAsk(t, s, todo.ID)
	asked := askOf(s, todo.ID).AskedAt
	if ask == nil || ask["outcome"] != "decide" || ask["verdict"] != "🤔 要你拍板：先灰度还是直接全量" || ask["recommend"] != "先灰度 10%" {
		t.Fatalf("ask %v", ask)
	}
	at, _ := ask["asked_at"].(string)
	if got, err := time.Parse(time.RFC3339Nano, at); err != nil || !got.Equal(asked) {
		t.Fatalf("asked_at %q, the round was asked at %v", at, asked)
	}
	sent := len(sentToAgent(f))
	if rec := acceptAt(s, todo.ID, at); rec.Code != http.StatusOK {
		t.Fatalf("OK: %d %s", rec.Code, rec.Body.String())
	}
	if got := sentToAgent(f)[sent:]; len(got) != 1 || !strings.Contains(got[0], "先灰度 10%") {
		t.Fatalf("to the agent: %q", got)
	}
	if ask := listedAsk(t, s, todo.ID); ask != nil {
		t.Fatalf("still an ask once taken: %v", ask)
	}
}

// A conclusion that only tells (done, cannot be done) is an ask too, without a recommendation; the to-dos nobody waits on
// have none.
func TestTodoListCarriesAConclusionWithoutRecommendation(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	other, err := s.createPlainTodo(t.Context(), "买牛奶", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	waitsForReview(t, s, f, todo.ID, "查了，做不了")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "outcome": "cannot", "reason": "对方接口不对外开放", "link": "https://docs.example.com/api"})
	ask := listedAsk(t, s, todo.ID)
	if ask == nil || ask["outcome"] != "cannot" || ask["verdict"] != "⛔ 做不了：对方接口不对外开放" || ask["asked_at"] == nil {
		t.Fatalf("ask %v", ask)
	}
	if _, there := ask["recommend"]; there {
		t.Fatalf("a recommendation on a conclusion that only tells: %v", ask)
	}
	if ask := listedAsk(t, s, other.ID); ask != nil {
		t.Fatalf("a plain to-do has an ask: %v", ask)
	}
}

// A decision the supervisor moved on from (it sent the work back) is no longer on the to-do.
func TestTodoAskEndsWhenTheReviewMovesOn(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	if listedAsk(t, s, todo.ID) == nil {
		t.Fatal("no ask while the decision waits")
	}
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "note": "用户在对话里说了直接全量，按全量做"})
	if ask := listedAsk(t, s, todo.ID); ask != nil {
		t.Fatalf("still an ask after rework: %v", ask)
	}
}

// The apps are told to read the to-dos again when an ask comes up and when it is taken, also when nothing else of the
// to-do changes with it (a review's own change of the ledger is told before its ask is kept).
func TestTodoAskChangesTellTheApps(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "查了，做不了")
	todosEvents := func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, e := range f.events {
			if e == "todos" {
				n++
			}
		}
		return n
	}
	before := todosEvents()
	s.askConclusion(t.Context(), todoNow(s, todo.ID), conclusionInput{Outcome: outcomeCannot, Reason: "对方接口不对外开放"})
	if todosEvents() == before {
		t.Fatal("no todos event once the ask is kept")
	}
	at, _ := listedAsk(t, s, todo.ID)["asked_at"].(string)
	before = todosEvents()
	if rec := acceptAt(s, todo.ID, at); rec.Code != http.StatusOK {
		t.Fatalf("OK: %d %s", rec.Code, rec.Body.String())
	}
	if todosEvents() == before {
		t.Fatal("no todos event once the ask is taken")
	}
}

// decidePushes are the "decide" pushes asked of the hub, as JSON (what goes over the bus).
func decidePushes(t *testing.T, f *fakeHub) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, n := range f.pushed() {
		if n.Kind != "decide" {
			continue
		}
		raw, _ := json.Marshal(n)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		out = append(out, m)
	}
	return out
}

// A decision with a recommendation is pushed once as a "decide" push: the to-do, the round's asked_at (what OK sends to
// the accept route) and the option, with the card's words; it opens the conversation the task came from.
func TestDecisionPushCarriesTheTodoAndItsRound(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	pushes := decidePushes(t, f)
	if len(pushes) != 1 {
		t.Fatalf("%d decide pushes: %v", len(pushes), f.pushed())
	}
	p := pushes[0]
	td, _ := p["todo"].(map[string]any)
	at, _ := td["asked_at"].(string)
	got, err := time.Parse(time.RFC3339Nano, at)
	if td == nil || td["id"] != todo.ID || td["recommend"] != "先灰度 10%" || err != nil || !got.Equal(askOf(s, todo.ID).AskedAt) {
		t.Fatalf("push's todo %v (the round: %v)", td, askOf(s, todo.ID).AskedAt)
	}
	if p["session_id"] != todo.Origin {
		t.Fatalf("opens %v, the task came from %s", p["session_id"], todo.Origin)
	}
	title, _ := p["title"].(string)
	body, _ := p["body"].(string)
	if !strings.Contains(title, "修登录崩溃") || !strings.Contains(body, "🤔 要你拍板：先灰度还是直接全量") || !strings.Contains(body, "推荐：先灰度 10%") {
		t.Fatalf("push says %q / %q", title, body)
	}
	if rec := acceptAt(s, todo.ID, at); rec.Code != http.StatusOK {
		t.Fatalf("the push's OK: %d %s", rec.Code, rec.Body.String())
	}
}

// A decision without a recommended option gets no decide push: there is nothing for OK to take.
func TestDecisionWithoutRecommendationIsNotPushedForOK(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量"})
	if pushes := decidePushes(t, f); len(pushes) != 0 {
		t.Fatalf("decide pushes %v", pushes)
	}
}

// An OK from an earlier round (the decision was asked again since) takes nothing: 409, and the agent hears nothing; the
// current round's OK takes the current recommendation.
func TestOKOfAnEarlierRoundTakesNothing(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	pushes := decidePushes(t, f)
	if len(pushes) != 1 {
		t.Fatalf("%d decide pushes", len(pushes))
	}
	first, _ := pushes[0]["todo"].(map[string]any)
	time.Sleep(2 * time.Millisecond) // sleep-ok: the next round is asked at another time
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "灰度比例定多少", "recommend": "灰度 5%"})
	pushes = decidePushes(t, f)
	if len(pushes) != 2 {
		t.Fatalf("%d decide pushes", len(pushes))
	}
	second, _ := pushes[1]["todo"].(map[string]any)
	if first["asked_at"] == second["asked_at"] {
		t.Fatalf("two rounds, one asked_at %v", first["asked_at"])
	}
	sent := len(sentToAgent(f))
	if rec := acceptAt(s, todo.ID, first["asked_at"].(string)); rec.Code != http.StatusConflict {
		t.Fatalf("the earlier round's OK: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(sentToAgent(f)); n != sent {
		t.Fatalf("the earlier round's OK sent %d to the agent", n-sent)
	}
	if rec := acceptAt(s, todo.ID, second["asked_at"].(string)); rec.Code != http.StatusOK {
		t.Fatalf("this round's OK: %d %s", rec.Code, rec.Body.String())
	}
	if got := sentToAgent(f)[sent:]; len(got) != 1 || !strings.Contains(got[0], "灰度 5%") {
		t.Fatalf("to the agent: %q", got)
	}
}

// With the Lark bot off the decision is pushed all the same. The hub taking a push does not say any phone shows its OK,
// so the supervisor still asks them as before, and is told OK on the push may take it first.
func TestDecisionWithoutTheBotIsPushedAndTheSupervisorStillAsks(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	if len(decidePushes(t, f)) != 1 {
		t.Fatalf("no decide push with the bot off: %v", f.pushed())
	}
	if !strings.Contains(res, "Ask them") || !strings.Contains(res, "review it again once they decide") ||
		!strings.Contains(res, "A push with your recommendation also went to their phone") || strings.Contains(res, "Don't ask them again") {
		t.Fatalf("answer: %s", res)
	}
}

// With the bot on, the card and the push both go, once each, and the supervisor hears of both.
func TestDecisionWithTheBotIsCardedAndPushedOnce(t *testing.T) {
	s, f, todo, posts, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	if n := len(cardsWith(posts(), "结论：")); n != 1 {
		t.Fatalf("%d conclusion cards", n)
	}
	if n := len(decidePushes(t, f)); n != 1 {
		t.Fatalf("%d decide pushes", n)
	}
	if !strings.Contains(res, "Don't ask them again") || !strings.Contains(res, "went to their phone") {
		t.Fatalf("answer: %s", res)
	}
}

// A task from a muted conversation gets no decision push (its ask is still on the to-do).
func TestDecisionOfAMutedConversationIsNotPushed(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	setMutes(s, Pins{Sessions: []string{"server/" + todo.Origin}})
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	if pushes := decidePushes(t, f); len(pushes) != 0 {
		t.Fatalf("decide pushes for a muted conversation: %v", pushes)
	}
	if listedAsk(t, s, todo.ID) == nil {
		t.Fatal("no ask on the to-do")
	}
}
