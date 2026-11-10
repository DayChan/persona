package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Decision deadlines (#3127, decision_deadline.go): a decision for the owner may carry a recommended default and a
// deadline; past it unanswered, a reversible one takes the default and records so on its to-do, one that cannot be
// undone is only reminded. Without a deadline nothing changes. All ids are placeholders.

// pastDeadline moves task id's decision deadline into the past, as if it had passed.
func pastDeadline(t *testing.T, s *Service, id string) {
	t.Helper()
	s.accept.mu.Lock()
	defer s.accept.mu.Unlock()
	a := s.accept.asks[id]
	if a == nil || a.Deadline.IsZero() {
		t.Fatalf("task %s has no decision with a deadline: %+v", id, a)
	}
	a.Deadline = time.Now().Add(-time.Minute)
}

func deadlinePushes(f *fakeHub, id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.pushes {
		if p.Todo != nil && p.Todo.ID == id {
			n++
		}
	}
	return n
}

// A reversible decision past its deadline takes the recommendation as the owner's OK would: it goes to the task's agent
// (saying it was taken by default), the ask is passed, and the task no longer waits for the user.
func TestReversibleDecisionTakesTheDefaultAtTheDeadline(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%",
		"deadline": "2h", "reversible": true})
	if !strings.Contains(res, "takes the recommendation by default") {
		t.Fatalf("task_review does not say the default is taken at the deadline: %q", res)
	}
	a := askOf(s, todo.ID)
	if d := time.Until(a.Deadline); d < 119*time.Minute || d > 2*time.Hour || !a.Reversible {
		t.Fatalf("deadline %v (in %v), reversible %v", a.Deadline, d, a.Reversible)
	}
	sent := len(sentToAgent(f))
	s.sweepDecisionDeadlines(time.Now()) // not due yet
	if got := sentToAgent(f)[sent:]; len(got) != 0 || !askOf(s, todo.ID).AcceptedAt.IsZero() {
		t.Fatalf("taken before its deadline: %q", got)
	}
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	got := sentToAgent(f)[sent:]
	want := i18n.T(s.botLang(context.Background()), "assistant-reports.verdict_default_to_agent", i18n.Args{"option": "先灰度 10%"})
	if len(got) != 1 || got[0] != want {
		t.Fatalf("to the agent: %q, want %q", got, want)
	}
	if a := askOf(s, todo.ID); a.AcceptedAt.IsZero() || !a.ByDefault || a.DueAt.IsZero() {
		t.Fatalf("ask after the deadline: %+v", a)
	}
	if st := todoNow(s, todo.ID).Supervision.State; st == reviewNeedsUser {
		t.Fatalf("still waits for the user: %q", st)
	}
	s.sweepDecisionDeadlines(time.Now().Add(time.Hour))
	if got := sentToAgent(f)[sent:]; len(got) != 1 {
		t.Fatalf("taken twice: %q", got)
	}
}

// On the store, the default taken at the deadline is a decision on the task's to-do that says so: the recommendation, why
// (no answer by the deadline), and not the owner's own (an agent's source).
func TestDefaultTakenAtTheDeadlineIsADecisionOnTheTodo(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo, _, _ := conclusionServiceOn(t, database, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%",
		"deadline": "30m", "reversible": true})
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	list, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Choice != "先灰度 10%" || list[0].Question != "先灰度还是直接全量" || list[0].Source != db.DecisionAgent ||
		!strings.Contains(list[0].Reason, "到期按默认执行") {
		t.Fatalf("decisions %+v", list)
	}
}

// A decision that cannot be undone is never taken at its deadline: nothing goes to the agent, it keeps waiting for the
// user, and the user is reminded once (the decision push again, saying it is overdue).
func TestIrreversibleDecisionIsOnlyRemindedAtTheDeadline(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "要删掉旧的生产库表吗？")
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "删不删旧表", "recommend": "删掉旧表",
		"deadline": "1h", "reversible": false})
	if !strings.Contains(res, "cannot be undone") {
		t.Fatalf("task_review does not say it is only reminded: %q", res)
	}
	sent, pushes := len(sentToAgent(f)), deadlinePushes(f, todo.ID)
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	if got := sentToAgent(f)[sent:]; len(got) != 0 {
		t.Fatalf("an irreversible decision was taken: %q", got)
	}
	a := askOf(s, todo.ID)
	if !a.AcceptedAt.IsZero() || a.ByDefault || a.DueAt.IsZero() {
		t.Fatalf("ask %+v", a)
	}
	if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatal("no longer waits for the user")
	}
	if n := deadlinePushes(f, todo.ID); n != pushes+1 {
		t.Fatalf("%d reminder pushes, want 1", n-pushes)
	}
	f.mu.Lock()
	body := f.pushes[len(f.pushes)-1].Body
	f.mu.Unlock()
	if !strings.Contains(body, "撤不回") {
		t.Fatalf("the reminder does not say it is not taken: %q", body)
	}
	s.sweepDecisionDeadlines(time.Now().Add(24 * time.Hour))
	if n := deadlinePushes(f, todo.ID); n != pushes+1 {
		t.Fatalf("reminded %d times, want once", n-pushes)
	}
	// The owner can still take it.
	asked, _ := json.Marshal(a.AskedAt)
	if rec := acceptAt(s, todo.ID, strings.Trim(string(asked), `"`)); rec.Code != 200 {
		t.Fatalf("OK after the deadline: %d %s", rec.Code, rec.Body.String())
	}
	if got := sentToAgent(f)[sent:]; len(got) != 1 || !strings.Contains(got[0], "删掉旧表") {
		t.Fatalf("the owner's OK: %q", got)
	}
}

// Without a deadline a decision waits as before, however long.
func TestDecisionWithoutDeadlineWaits(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	sent, pushes := len(sentToAgent(f)), deadlinePushes(f, todo.ID)
	s.sweepDecisionDeadlines(time.Now().Add(400 * 24 * time.Hour))
	a := askOf(s, todo.ID)
	if got := sentToAgent(f)[sent:]; len(got) != 0 || !a.AcceptedAt.IsZero() || !a.DueAt.IsZero() || !a.Deadline.IsZero() {
		t.Fatalf("a decision without a deadline moved: sent %q, ask %+v", got, a)
	}
	if n := deadlinePushes(f, todo.ID); n != pushes {
		t.Fatalf("pushed %d times", n-pushes)
	}
}

// A deadline needs needs_user, a recommendation and whether it can be undone.
func TestTaskReviewDeadlineArguments(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	for _, tc := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"verdict": "needs_user", "recommend": "A", "deadline": "2h"}, "reversible"},
		{map[string]any{"verdict": "needs_user", "deadline": "2h", "reversible": true}, "recommend"},
		{map[string]any{"verdict": "accepted", "deadline": "2h", "reversible": true}, "needs_user only"},
		{map[string]any{"verdict": "needs_user", "recommend": "A", "deadline": "soon", "reversible": true}, "duration"},
		{map[string]any{"verdict": "needs_user", "recommend": "A", "deadline": "10s", "reversible": true}, "between"},
	} {
		tc.args["todo_id"] = todo.ID
		raw, _ := json.Marshal(tc.args)
		if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: error %v, want one about %q", tc.args, err, tc.want)
		}
	}
	if a := askOf(s, todo.ID); a != nil {
		t.Fatalf("a refused review left an ask: %+v", a)
	}
}

func TestParseDeadline(t *testing.T) {
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		in   string
		want time.Time
		err  bool
	}{
		{"", time.Time{}, false},
		{"90m", now.Add(90 * time.Minute), false},
		{"2026-11-10T18:00:00+08:00", time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC), false},
		{"2026-11-10T07:00:00Z", time.Time{}, true}, // in the past
		{"800h", time.Time{}, true},                 // past 30 days
		{"tomorrow", time.Time{}, true},
	} {
		got, err := parseDeadline(tc.in, now)
		if (err != nil) != tc.err || !got.Equal(tc.want) {
			t.Errorf("parseDeadline(%q) = %v, %v", tc.in, got, err)
		}
	}
}

// ---- ask_user ----

const askDeadlineArgsJSON = `{"deadline":"%s","reversible":%s,"questions":[{"question":"Merge the release PR now?","header":"Merge",` +
	`"recommend":"Merge","options":[{"label":"Merge","description":"CI is green"},{"label":"Wait"}]}]}`

// askWith is askTodoEnv.ask with args, the card expiring unanswered.
func (e *askTodoEnv) expireWith(args string) (proto.Interaction, string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan string, 1)
	go func() {
		text, err := e.s.toolAskUser(ctx, "server", "c1x1x1", json.RawMessage(args))
		if err != nil {
			text = "error: " + err.Error()
		}
		done <- text
	}()
	var ix proto.Interaction
	waitFor(e.t, "the question card", func() bool {
		select {
		case text := <-done: // refused before any card
			e.t.Fatalf("ask_user returned before a card: %s", text)
		default:
		}
		if qs := e.s.pendingQuestions(); len(qs) == 1 {
			ix = qs[0]
			return true
		}
		return false
	})
	if _, err := e.s.Action(ctx, Action{Op: "answer", InteractionID: ix.ID,
		Answer: json.RawMessage(`{"dismiss":true,"reason":"` + proto.DismissedExpired + `"}`)}); err != nil {
		e.t.Fatal(err)
	}
	select {
	case text := <-done:
		return ix, text
	case <-ctx.Done():
		e.t.Fatal("ask_user did not return")
		return ix, ""
	}
}

// A reversible question whose card waits until its deadline answers itself with the recommended option when it ends
// unanswered: the tool returns it, saying it was taken by default; its to-do is closed, and nothing else is told.
func TestAskUserTakesTheDefaultAtItsDeadline(t *testing.T) {
	old := askInlineSlack
	askInlineSlack = 2 * time.Minute // the card is expired at once, a minute before its 1m deadline
	t.Cleanup(func() { askInlineSlack = old })
	e := newAskTodoEnv(t, nil)
	ix, text := e.expireWith(strings.Replace(strings.Replace(askDeadlineArgsJSON, "%s", "1m", 1), "%s", "true", 1))
	if d := ix.ExpiresAt.Sub(ix.CreatedAt); d > time.Minute+time.Second {
		t.Fatalf("the card waits %v past its 1m deadline", d)
	}
	if d := ix.Questions[0].Options[0].Description; d == "CI is green" || !strings.HasSuffix(d, "\nCI is green") || ix.Questions[0].Options[0].Label != "Merge" {
		t.Fatalf("the recommended option on the card: %+v", ix.Questions[0].Options[0])
	}
	if !strings.Contains(text, "taken by default") || !strings.Contains(text, "Merge the release PR now? → Merge") {
		t.Fatalf("tool result %q", text)
	}
	todo := e.onlyAskTodo()
	if todo.DoneAt == nil {
		t.Fatal("its to-do is still open")
	}
	if got := e.told("c1x1x1"); len(got) != 0 {
		t.Fatalf("the conversation was told besides the tool result: %q", got)
	}
}

// The recommended option says on the card that it is the recommendation and what the deadline does; the label (the
// answer) stays as it was.
func TestAskUserCardMarksTheRecommendation(t *testing.T) {
	qs := []proto.Question{{Question: "Q", Options: []proto.QuestionOption{{Label: "A"}, {Label: "B", Description: "slower"}}}}
	at := time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)
	markRecommended(i18n.En, time.UTC, qs, askDeadline{At: at, Reversible: true, Recommends: []string{"B"}})
	if qs[0].Options[0].Description != "" || qs[0].Options[1].Label != "B" ||
		qs[0].Options[1].Description != "Recommended · taken if there is no answer by 11-10 09:30\nslower" {
		t.Fatalf("options %+v", qs[0].Options)
	}
}

// A reversible question whose deadline is after the card's wait becomes a to-do as before; at the deadline the sweep
// records the default on it, closes it and tells the asking conversation it was taken by default.
func TestAskTodoTakesTheDefaultAtALaterDeadline(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	_, text := e.expireWith(strings.Replace(strings.Replace(askDeadlineArgsJSON, "%s", "6h", 1), "%s", "true", 1))
	if !strings.Contains(text, "taken by default") || !strings.Contains(text, "new message") {
		t.Fatalf("tool result %q", text)
	}
	todo := e.onlyAskTodo()
	e.s.sweepDecisionDeadlines(time.Now())
	if cur := e.get(todo.ID); cur == nil || cur.DoneAt != nil {
		t.Fatal("closed before its deadline")
	}
	e.s.sweepDecisionDeadlines(time.Now().Add(7 * time.Hour))
	waitFor(t, "the to-do closed", func() bool { cur := e.get(todo.ID); return cur != nil && cur.DoneAt != nil })
	got := e.told("c1x1x1")
	if len(got) != 1 || !strings.Contains(got[0], "按你推荐的默认选项") || !strings.Contains(got[0], "Merge") {
		t.Fatalf("told %q", got)
	}
	e.s.sweepDecisionDeadlines(time.Now().Add(8 * time.Hour))
	if got := e.told("c1x1x1"); len(got) != 1 {
		t.Fatalf("told twice: %q", got)
	}
}

// On the store the default is a decision on the question's to-do, saying it was taken at the deadline.
func TestAskTodoDefaultIsADecision(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	e.expireWith(strings.Replace(strings.Replace(askDeadlineArgsJSON, "%s", "6h", 1), "%s", "true", 1))
	todo := e.onlyAskTodo()
	e.s.sweepDecisionDeadlines(time.Now().Add(7 * time.Hour))
	waitFor(t, "the to-do closed", func() bool { cur := e.get(todo.ID); return cur != nil && cur.DoneAt != nil })
	list, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Choice != "Merge" || list[0].Source != db.DecisionAgent || !strings.Contains(list[0].Reason, "到期按默认执行") {
		t.Fatalf("decisions %+v", list)
	}
	if got := e.told("c1x1x1"); len(got) != 1 {
		t.Fatalf("told %q", got)
	}
}

// A question that cannot be undone is never answered by default: past its deadline it stays an open to-do, the tool
// says nothing was taken, and the owner is pushed one reminder.
func TestIrreversibleAskUserIsOnlyReminded(t *testing.T) {
	old := askInlineSlack
	askInlineSlack = 2 * time.Minute // the card is expired at once, a minute before its 1m deadline
	t.Cleanup(func() { askInlineSlack = old })
	e := newAskTodoEnv(t, nil)
	pushes := func() int { e.f.mu.Lock(); defer e.f.mu.Unlock(); return len(e.f.pushes) }
	before := pushes()
	_, text := e.expireWith(strings.Replace(strings.Replace(askDeadlineArgsJSON, "%s", "1m", 1), "%s", "false", 1))
	if !strings.Contains(text, "cannot be undone") || !strings.Contains(text, "nothing was taken") {
		t.Fatalf("tool result %q", text)
	}
	todo := e.onlyAskTodo()
	if todo.DoneAt != nil {
		t.Fatal("closed")
	}
	if n := pushes() - before; n != 1 {
		t.Fatalf("%d reminders, want 1", n)
	}
	e.s.sweepDecisionDeadlines(time.Now().Add(48 * time.Hour))
	if n := pushes() - before; n != 1 {
		t.Fatalf("%d reminders after the sweep, want 1", n)
	}
	if got := e.told("c1x1x1"); len(got) != 0 {
		t.Fatalf("told %q", got)
	}
	if cur := e.get(todo.ID); cur == nil || cur.DoneAt != nil {
		t.Fatal("no longer waits")
	}
}

// A deadline needs whether it can be undone and a recommendation on every question that is one of its options.
func TestAskUserDeadlineArguments(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	for _, tc := range []struct{ args, want string }{
		{`{"deadline":"1h","questions":[{"question":"Q","recommend":"A","options":[{"label":"A"},{"label":"B"}]}]}`, "reversible"},
		{`{"deadline":"1h","reversible":true,"questions":[{"question":"Q","options":[{"label":"A"},{"label":"B"}]}]}`, "recommend"},
		{`{"deadline":"1h","reversible":true,"questions":[{"question":"Q","recommend":"C","options":[{"label":"A"},{"label":"B"}]}]}`, "recommend"},
	} {
		if _, err := e.s.toolAskUser(context.Background(), "server", "c1x1x1", json.RawMessage(tc.args)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %v, want one about %q", tc.args, err, tc.want)
		}
	}
	if qs := e.s.pendingQuestions(); len(qs) != 0 {
		t.Fatalf("a refused question is out: %+v", qs)
	}
}

// Without a deadline an expired question waits as a to-do however long, as before.
func TestAskTodoWithoutDeadlineWaits(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	e.expire()
	todo := e.onlyAskTodo()
	e.s.sweepDecisionDeadlines(time.Now().Add(29 * 24 * time.Hour))
	if cur := e.get(todo.ID); cur == nil || cur.DoneAt != nil {
		t.Fatal("closed")
	}
	if got := e.told("c1x1x1"); len(got) != 0 {
		t.Fatalf("told %q", got)
	}
}

func TestGoldenDecisionDeadline(t *testing.T) {
	at := time.Date(2026, 11, 8, 15, 4, 0, 0, time.UTC)
	golden(t, "ask_todo.tool_result_default_later", prompts.RenderQuestionMovedToTodo(prompts.QuestionMovedToTodo{TodoID: "todo_ask_0a1b2c",
		Title: "Merge the release PR now?", DefaultAt: "11-08 21:04"}))
	golden(t, "ask_todo.tool_result_overdue", prompts.RenderQuestionMovedToTodo(prompts.QuestionMovedToTodo{TodoID: "todo_ask_0a1b2c",
		Title: "Merge the release PR now?", Overdue: true}))
	golden(t, "ask_todo.tool_result_defaulted", prompts.RenderQuestionDefaulted(prompts.QuestionDefaulted{Deadline: "11-08 16:04",
		TodoID: "todo_ask_0a1b2c", Choices: []prompts.AskTodoChoice{{Question: "Merge the release PR now?", Choice: "Merge"}}}))
	golden(t, "ask_todo.answer_default", renderAskDefault(askTodo{TodoID: "todo_ask_0a1b2c", Questions: []string{"Merge the release PR now?"},
		AskedAt: at, Deadline: at.Add(6 * time.Hour)}, time.UTC, []askChoice{{Question: "Merge the release PR now?", Choice: "Merge"}}))
}

// The owner answering the decision's card with a choice of their own ends its deadline, even when that answer did not
// reach the agent: the recommendation they turned down is never taken by default afterwards.
func TestOwnersOwnAnswerEndsTheDeadline(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("send fails %v", fail), func(t *testing.T) {
			s, f, todo, _, _ := conclusionService(t, false)
			waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
			reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%",
				"deadline": "2h", "reversible": true})
			a := askOf(s, todo.ID)
			if fail {
				f.mu.Lock()
				f.failSend = errors.New("node down")
				f.mu.Unlock()
			}
			if !s.acceptReply(context.Background(), "om_mine", "直接全量", false, a.MessageID) {
				t.Fatal("the reply was not taken as the decision's")
			}
			f.mu.Lock()
			f.failSend = nil
			f.mu.Unlock()
			sent := len(sentToAgent(f))
			s.accept.mu.Lock()
			if cur := s.accept.asks[todo.ID]; cur != nil {
				cur.Deadline = time.Now().Add(-time.Minute)
			}
			s.accept.mu.Unlock()
			s.sweepDecisionDeadlines(time.Now())
			for _, m := range sentToAgent(f)[sent:] {
				if strings.Contains(m, "先灰度 10%") {
					t.Fatalf("the recommendation the owner turned down was taken: %q", m)
				}
			}
		})
	}
}

// A default that does not reach the agent is not retried every minute (it may have reached it after all): the owner is
// reminded instead and can still take it.
func TestFailedDefaultRemindsInsteadOfRetrying(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%",
		"deadline": "2h", "reversible": true})
	pastDeadline(t, s, todo.ID)
	pushes := deadlinePushes(f, todo.ID)
	f.mu.Lock()
	f.failSend = errors.New("node down")
	f.mu.Unlock()
	s.sweepDecisionDeadlines(time.Now())
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	a := askOf(s, todo.ID)
	if !a.AcceptedAt.IsZero() || a.ByDefault || a.DueAt.IsZero() {
		t.Fatalf("ask after a failed default: %+v", a)
	}
	if n := deadlinePushes(f, todo.ID); n != pushes+1 {
		t.Fatalf("%d reminders, want 1", n-pushes)
	}
	sent := len(sentToAgent(f))
	s.sweepDecisionDeadlines(time.Now().Add(time.Hour))
	if got := sentToAgent(f)[sent:]; len(got) != 0 {
		t.Fatalf("retried: %q", got)
	}
}

// A question's default cut off before it went (the assistant stopped after claiming it) goes on the next sweep.
func TestInterruptedAskDefaultResumes(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	e.expireWith(strings.Replace(strings.Replace(askDeadlineArgsJSON, "%s", "6h", 1), "%s", "true", 1))
	todo := e.onlyAskTodo()
	b := &e.s.askTodos
	b.mu.Lock()
	for i := range b.list {
		if b.list[i].TodoID == todo.ID {
			b.list[i].DueAt, b.list[i].ByDefault = time.Now(), true // claimed long ago, never sent
		}
	}
	b.mu.Unlock()
	e.s.sweepDecisionDeadlines(time.Now().Add(7 * time.Hour))
	waitFor(t, "the to-do closed", func() bool { cur := e.get(todo.ID); return cur != nil && cur.DoneAt != nil })
	if got := e.told("c1x1x1"); len(got) != 1 || !strings.Contains(got[0], "按你推荐的默认选项") {
		t.Fatalf("told %q", got)
	}
}

// A deadline or reversible of the wrong type is refused, not taken as no deadline.
func TestAskUserDeadlineOfTheWrongType(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	args := `{"deadline":3600,"reversible":true,"questions":[{"question":"Q","recommend":"A","options":[{"label":"A"},{"label":"B"}]}]}`
	if _, err := e.s.toolAskUser(context.Background(), "server", "c1x1x1", json.RawMessage(args)); err == nil {
		t.Fatal("a numeric deadline was accepted")
	}
}

// Without a deadline the question's card, its arguments and its options are exactly what was asked.
func TestAskUserWithoutDeadlineCardUnchanged(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	ix, _ := e.expire()
	if got := ix.ExpiresAt.Sub(ix.CreatedAt); got != askUserWait {
		t.Fatalf("card waits %v, want %v", got, askUserWait)
	}
	if string(ix.ToolInput) != askTodoArgs {
		t.Fatalf("arguments rewritten: %s", ix.ToolInput)
	}
	if ix.Questions[0].Options[0].Description != "CI is green" || ix.Questions[0].Options[1].Description != "until the review is in" {
		t.Fatalf("options %+v", ix.Questions[0].Options)
	}
}
