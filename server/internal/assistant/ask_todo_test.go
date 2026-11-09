package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// An ask_user nobody answers in time becomes a to-do that waits for the owner's call, and the call goes back to the
// conversation that asked (#1617, ask_todo.go). All ids are placeholders.

const askTodoArgs = `{"questions":[{"question":"Merge the release PR now?","header":"Merge","options":[` +
	`{"label":"Merge (Recommended)","description":"CI is green"},{"label":"Wait","description":"until the review is in"}]}]}`

// askTodoEnv is threadEnv (a paired bot, the main conversation m1 and the conversation c1x1x1 on server) where c1x1x1 asks.
type askTodoEnv struct{ *threadEnv }

func newAskTodoEnv(t *testing.T, st Store) *askTodoEnv {
	t.Helper()
	e := &askTodoEnv{newThreadEnv(t, st)}
	e.f.mu.Lock()
	e.f.known["c1x1x1"], e.f.known["m1"] = true, true
	e.f.mu.Unlock()
	e.sessions("server", proto.Session{SessionID: "m1", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "c1x1x1", Title: "Release chat", Assistant: true, Status: proto.StatusIdle})
	return e
}

// ask has c1x1x1 ask with ask_user and hands its question answer (what askOnPhone hands it when the card ends) once its
// card is out; it returns the card and the tool's result.
func (e *askTodoEnv) ask(answer string) (proto.Interaction, string) {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan string, 1)
	go func() {
		text, err := e.s.toolAskUser(ctx, "server", "c1x1x1", json.RawMessage(askTodoArgs))
		if err != nil {
			text = "error: " + err.Error()
		}
		done <- text
	}()
	var ix proto.Interaction
	waitFor(e.t, "the question out", func() bool {
		if qs := e.s.pendingQuestions(); len(qs) == 1 {
			ix = qs[0]
			return true
		}
		return false
	})
	if answer == "user spoke" {
		e.s.UserSpoke(ctx, SessionRef{MachineID: "server", SessionID: "c1x1x1"})
	} else if _, err := e.s.Action(ctx, Action{Op: "answer", InteractionID: ix.ID, Answer: json.RawMessage(answer)}); err != nil {
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

// expire is ask with the expiry askOnPhone hands the question when its card expires unanswered.
func (e *askTodoEnv) expire() (proto.Interaction, string) {
	return e.ask(`{"dismiss":true,"reason":"` + proto.DismissedExpired + `"}`)
}

// askTodos are the to-dos that came from a question.
func (e *askTodoEnv) askTodos() []Todo {
	var out []Todo
	for _, t := range e.list() {
		if t.Origin == "c1x1x1" {
			out = append(out, t)
		}
	}
	return out
}

func (e *askTodoEnv) onlyAskTodo() Todo {
	e.t.Helper()
	list := e.askTodos()
	if len(list) != 1 {
		e.t.Fatalf("%d to-dos from the question, want 1: %+v", len(list), list)
	}
	return list[0]
}

// told are the messages sent into conversation id (through the relay queue, assistant.send).
func (e *askTodoEnv) told(id string) []string {
	e.f.mu.Lock()
	defer e.f.mu.Unlock()
	var out []string
	for _, a := range e.f.sends {
		if a["session_id"] == id {
			text, _ := a["text"].(string)
			out = append(out, text)
		}
	}
	return out
}

// An ask_user whose card expires unanswered becomes one to-do: the question its title, the options (descriptions and the
// recommended one as written), when and where it was asked in its notes, the asking conversation its origin, no Agent.
// The tool says it is that to-do now and that the answer comes as a new message.
func TestExpiredAskUserBecomesATodo(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	ix, text := e.expire()
	todo := e.onlyAskTodo()
	if todo.Title != "Merge the release PR now?" || len(todo.Agents) != 0 || todo.DoneAt != nil {
		t.Fatalf("to-do %+v", todo)
	}
	for _, want := range []string{"Merge (Recommended)", "CI is green", "Wait", "until the review is in", "Release chat",
		ix.CreatedAt.In(e.s.location(context.Background())).Format("01-02 15:04")} {
		if !strings.Contains(todo.Notes, want) {
			t.Errorf("notes lack %q:\n%s", want, todo.Notes)
		}
	}
	if !strings.Contains(text, todo.ID) || !strings.Contains(text, "new message") || strings.Contains(text, "did not answer") {
		t.Fatalf("tool result: %q", text)
	}
	if got := e.told("c1x1x1"); len(got) != 0 {
		t.Fatalf("the conversation was told something already: %q", got)
	}
}

// The same card's expiry handled again (handed over twice, or by an assistant started again on the same store) makes no
// second to-do.
func TestExpiredAskUserMakesOneTodoAcrossARestart(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	ix, _ := e.expire()
	first := e.onlyAskTodo()
	if _, err := e.s.askExpired(context.Background(), ix); err != nil {
		t.Fatal(err)
	}
	again := newAskTodoEnv(t, database) // a new assistant on the same database
	got, err := again.s.askExpired(context.Background(), ix)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != first.ID {
		t.Fatalf("the restarted assistant made %s, want %s again", got.ID, first.ID)
	}
	if list := again.askTodos(); len(list) != 1 {
		t.Fatalf("%d to-dos after the restart: %+v", len(list), list)
	}
}

// Only an expiry makes a to-do: a dismissed card, one withdrawn because the user wrote in the chat, and an answered one
// do not.
func TestOnlyAnExpiredAskUserBecomesATodo(t *testing.T) {
	for _, c := range []struct{ name, answer, want string }{
		{"dismissed", `{"dismiss":true}`, "did not answer"},
		{"withdrawn: the user wrote", "user spoke", prompts.RenderQuestionUserSpoke()},
		{"answered", `{"answers":{"Merge the release PR now?":"Wait"}}`, "The user answered"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAskTodoEnv(t, nil)
			_, text := e.ask(c.answer)
			if !strings.Contains(text, c.want) {
				t.Fatalf("tool result %q, want %q in it", text, c.want)
			}
			if list := e.askTodos(); len(list) != 0 {
				t.Fatalf("to-dos: %+v", list)
			}
		})
	}
}

// The to-do waits for the owner's call where they look: the next briefing's "Can wait" rows and the live snapshot.
func TestAskTodoWaitsInTheBriefingAndTheSnapshot(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	e.expire()
	todo := e.onlyAskTodo()
	f := e.s.briefFactsAt(context.Background(), time.Now(), "")
	if f.LaterTotal != 1 || len(f.Later) != 1 || f.Later[0].summary != "Merge the release PR now?" {
		t.Fatalf("can wait: %d %+v", f.LaterTotal, f.Later)
	}
	snap := e.s.contextSnapshot(context.Background())
	if !strings.Contains(snap, "Merge the release PR now?") || !strings.Contains(snap, todo.ID) {
		t.Fatalf("the snapshot does not list it:\n%s", snap)
	}
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
	if f := e.s.briefFactsAt(context.Background(), time.Now(), ""); f.LaterTotal != 0 {
		t.Fatalf("a closed one still waits: %+v", f.Later)
	}
	e.s.ProfileChanged(context.Background()) // a fresh snapshot
	if snap := e.s.contextSnapshot(context.Background()); strings.Contains(snap, todo.ID) {
		t.Fatalf("a closed one is still in the snapshot:\n%s", snap)
	}
}

// A decision recorded on it (the apps' decision form) goes to the asking conversation as one message, saying when it
// asked what, and what the user chose; the to-do closes, and a second decision sends nothing.
func TestAskTodoDecisionGoesBackToTheConversationOnce(t *testing.T) {
	e := newAskTodoEnv(t, todoTestDB(t))
	ix, _ := e.expire()
	todo := e.onlyAskTodo()
	e.must("POST", "/api/v1/todos/"+todo.ID+"/decisions", `{"question":"Merge the release PR now?","choice":"Merge","reason":"CI is green"}`)
	waitFor(t, "the to-do closed", func() bool { cur := e.get(todo.ID); return cur != nil && cur.DoneAt != nil })
	got := e.told("c1x1x1")
	if len(got) != 1 {
		t.Fatalf("%d messages to the conversation, want 1: %q", len(got), got)
	}
	when := ix.CreatedAt.In(e.s.location(context.Background())).Format("01-02 15:04")
	for _, want := range []string{when, "Merge the release PR now?", "Merge", "CI is green", todo.ID} {
		if !strings.Contains(got[0], want) {
			t.Errorf("message lacks %q:\n%s", want, got[0])
		}
	}
	e.must("POST", "/api/v1/todos/"+todo.ID+"/decisions", `{"question":"Merge the release PR now?","choice":"Wait"}`)
	time.Sleep(100 * time.Millisecond) // a second message would have gone by now
	if got := e.told("c1x1x1"); len(got) != 1 {
		t.Fatalf("a second decision was sent too: %q", got)
	}
	if got := e.told("m1"); len(got) != 0 {
		t.Fatalf("the main conversation was told: %q", got)
	}
}

// An answer claimed but never sent (the assistant restarted right then) does not hold the to-do for good: once the
// claim has lapsed, the next decision goes back.
func TestAskTodoAnswerLostInARestartLetsTheNextOneGo(t *testing.T) {
	old := askClaimHold
	askClaimHold = 50 * time.Millisecond
	t.Cleanup(func() { askClaimHold = old })
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	e.expire()
	todo := e.onlyAskTodo()
	if _, ok := e.s.claimAskTodo(context.Background(), todo.ID, true); !ok { // claimed, then the process went away
		t.Fatal("no claim")
	}
	again := newAskTodoEnv(t, database)
	time.Sleep(100 * time.Millisecond)
	again.must("POST", "/api/v1/todos/"+todo.ID+"/decisions", `{"question":"Merge the release PR now?","choice":"Wait"}`)
	waitFor(t, "the to-do closed", func() bool { cur := again.get(todo.ID); return cur != nil && cur.DoneAt != nil })
	if got := again.told("c1x1x1"); len(got) != 1 || !strings.Contains(got[0], "Wait") {
		t.Fatalf("told %q", got)
	}
}

// A reply in the Lark topic the to-do is bound to is its decision too: one message to the asking conversation (the
// bot's send, so the answer comes back into the topic), and the to-do closes.
func TestAskTodoTopicReplyGoesBackToTheConversation(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	e.expire()
	todo := e.onlyAskTodo()
	e.must("PUT", "/api/v1/todos/"+todo.ID+"/thread", `{"channel":"feishu","chat_id":"`+testChat+`","thread_id":"`+testThread+`","title":"t"}`)
	e.deliver(topicEvent("om_a1", "Merge it"))
	h := onlyHandoff(t, e.f)
	if h.session != "c1x1x1" || !strings.Contains(h.text, "Merge the release PR now?") || !strings.Contains(h.text, "Merge it") {
		t.Fatalf("handed over: %+v", h)
	}
	if got := e.told("c1x1x1"); len(got) != 0 {
		t.Fatalf("sent a second time through the relay: %q", got)
	}
	if cur := e.get(todo.ID); cur == nil || cur.DoneAt == nil {
		t.Fatalf("the to-do is not closed: %+v", cur)
	}
	if list := decisionsOf(t, database, todo.ID); len(list) != 1 {
		t.Fatalf("decisions %+v", list)
	}
}

// Handled on its "Can wait" row (the button of the web app's brief item, and of the Lark briefing card) ends the question as
// closing its to-do does (#1854): the to-do closes, the snapshot stops holding the question, and the conversation that
// asked is told once that it was dropped. Pressed again, it is "already handled" and tells nothing more.
func TestAskTodoHandledInTheBriefingClosesIt(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	e.expire()
	todo := e.onlyAskTodo()
	ctx := context.Background()
	f := e.s.briefFactsAt(ctx, time.Now(), "")
	if len(f.Later) != 1 {
		t.Fatalf("can wait: %+v", f.Later)
	}
	row := f.Later[0].id
	e.must("POST", "/api/v1/later/"+row+"/handled", "")
	if cur := e.get(todo.ID); cur == nil || cur.DoneAt == nil {
		t.Fatalf("the to-do is still open: %+v", cur)
	}
	waitFor(t, "the dropped notice", func() bool { return len(e.told("c1x1x1")) > 0 })
	if got := e.told("c1x1x1"); len(got) != 1 || !strings.Contains(got[0], "没有在待办里给答案") {
		t.Fatalf("told %q, want one dropped notice", got)
	}
	e.s.ProfileChanged(ctx) // a fresh snapshot
	if snap := e.s.contextSnapshot(ctx); strings.Contains(snap, todo.ID) {
		t.Fatalf("the question still waits in the snapshot:\n%s", snap)
	}
	if w := e.do("POST", "/api/v1/later/"+row+"/handled", ""); w.Code != http.StatusConflict {
		t.Fatalf("pressed again: %d %s", w.Code, w.Body)
	}
	if _, again := e.s.claimAskTodo(ctx, todo.ID, false); again { // the drop was told: nothing tells it twice
		t.Fatal("the drop could be told again")
	}
}

// Closing it without a decision tells the conversation once that the question was dropped; closing it again after a
// reopen, or deleting it then, says nothing more.
func TestAskTodoClosedWithoutADecisionTellsOnce(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	e.expire()
	todo := e.onlyAskTodo()
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
	waitFor(t, "the dropped notice", func() bool { return len(e.told("c1x1x1")) > 0 })
	got := e.told("c1x1x1")
	if len(got) != 1 || !strings.Contains(got[0], "Merge the release PR now?") || !strings.Contains(got[0], "没有在待办里给答案") {
		t.Fatalf("told %q, want one dropped notice", got)
	}
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":false}`)
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
	e.must("DELETE", "/api/v1/todos/"+todo.ID, "")
	time.Sleep(100 * time.Millisecond) // a second notice would have gone by now
	if got := e.told("c1x1x1"); len(got) != 1 {
		t.Fatalf("told again: %q", got)
	}
}

// Deleting it unanswered is closing it without a decision.
func TestAskTodoDeletedTellsTheConversation(t *testing.T) {
	e := newAskTodoEnv(t, nil)
	e.expire()
	todo := e.onlyAskTodo()
	e.must("DELETE", "/api/v1/todos/"+todo.ID, "")
	waitFor(t, "the dropped notice", func() bool { return len(e.told("c1x1x1")) > 0 })
	if got := e.told("c1x1x1"); len(got) != 1 || !strings.Contains(got[0], "没有在待办里给答案") {
		t.Fatalf("told %q, want one dropped notice", got)
	}
}

// Through the real queue: the card expires on its own, the tool gets the to-do, and its Lark card says it moved to the
// to-dos instead of "Ended".
func TestAskUserExpiringOnTheQueueMovesToTodos(t *testing.T) {
	old := askUserWait
	askUserWait = 300 * time.Millisecond
	t.Cleanup(func() { askUserWait = old })
	a := newAskRig(t)
	_, done := a.ask(t)
	text := a.result(t, done)
	if !strings.Contains(text, "todo_") || !strings.Contains(text, "new message") {
		t.Fatalf("tool result %q", text)
	}
	want := i18n.T(a.s.botLang(context.Background()), "assistant-feishu_cards.moved_to_todos")
	waitFor(t, "the Lark card closed", func() bool { return strings.Contains(a.closed(), want) })
	if strings.Contains(a.closed(), i18n.T(a.s.botLang(context.Background()), "assistant-channels.ended")) {
		t.Errorf("the card says ended: %q", a.closed())
	}
}

func TestGoldenAskTodo(t *testing.T) {
	golden(t, "ask_todo.tool_result", prompts.RenderQuestionMovedToTodo(prompts.QuestionMovedToTodo{TodoID: "todo_ask_0a1b2c", Title: "Merge the release PR now?"}))
	at := time.Date(2026, 11, 8, 15, 4, 0, 0, time.UTC)
	golden(t, "ask_todo.answer_manual", renderAskAnswer(askTodo{TodoID: "todo_ask_0a1b2c", Questions: []string{"Merge the release PR now?"}, AskedAt: at},
		time.UTC, []askChoice{{Question: "Merge the release PR now?", Choice: "Merge", Reason: "CI is green"}}, false, false))
	golden(t, "ask_todo.answer_thread", renderAskAnswer(askTodo{TodoID: "todo_ask_0a1b2c", Questions: []string{"Merge the release PR now?", "Tag it?"}, AskedAt: at},
		time.UTC, []askChoice{{Choice: "Merge it, no tag"}}, true, false))
	golden(t, "assistant_snapshot.asked", prompts.RenderSnapshot(prompts.Snapshot{Now: at, Zone: "UTC",
		Asked: []prompts.SnapshotAsked{{TodoID: "todo_ask_0a1b2c", Question: "Merge the release PR now?", At: at.Add(-3 * time.Hour)}}}))
	golden(t, "ask_todo.dropped", renderAskAnswer(askTodo{TodoID: "todo_ask_0a1b2c", Questions: []string{"Merge the release PR now?"}, AskedAt: at},
		time.UTC, nil, false, true))
}
