package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
)

// outcomeEnv is an assistant with a knowledge base whose weather App project keeps its code in box:/code/weather, and a
// task (a todo from conversation conv1) whose agent box/s1 worked there and said reply last.
func outcomeEnv(t *testing.T, st Store, reply string) (*Service, *fakeHub, Todo) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.setSessions("box", proto.Session{SessionID: "s1", Title: "修登录", Cwd: "/code/weather/ios"})
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: reply})
	var s *Service
	if st == nil {
		s = newKBService(t, f)
	} else {
		s = newKBServiceOn(t, f, st)
	}
	e := kbEng(t, s)
	if _, err := e.Apply(kb.KindProject, "天气 App", kb.Update{Dirs: []string{"box:/code/weather"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := e.Store.Project("天气-app")
	p.OneLine = "天气 App 的 iOS 和安卓版"
	if err := e.Store.PutProject(p); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "修 · 登录页横屏崩溃", "iPad 横屏时登录页崩溃，修好并补测试", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1", "修登录", "/code/weather/ios", "claude"); err != nil {
		t.Fatal(err)
	}
	return s, f, todoNow(s, todo.ID)
}

const outcomeReply = "修好了：登录页在横屏时用了固定宽度。PR https://github.com/o/weather/pull/12 已合并。注意 iPad 模拟器要用原版的，瘦身的复现不了。"

// A finished task's outcome, decisions and pitfalls go in its decisions (source outcome, once per completion) and its
// project's timeline; the next new agent for the project gets them before its prompt.
func TestTaskOutcomeRecorded(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo := outcomeEnv(t, database, outcomeReply)
	f.outcome = `{"outcome":"修好登录页横屏崩溃，PR 已合并","links":["https://github.com/o/weather/pull/12","not a link"],
		"decisions":[{"question":"横屏布局怎么改","choice":"改成自适应宽度","reason":"固定宽度在 iPad 上放不下"},{"question":" ","choice":"x"}],
		"pitfalls":["瘦身的 iPad 模拟器复现不了，要用原版"]}`
	done := time.Now().UTC()
	s.recordTaskOutcome(todo, done)
	if len(f.outcomeAsked) != 1 || !strings.Contains(f.outcomeAsked[0].User, "iPad 横屏时登录页崩溃") ||
		!strings.Contains(f.outcomeAsked[0].User, "box · /code/weather/ios") || !strings.Contains(f.outcomeAsked[0].User, "pull/12") {
		t.Fatalf("model asked = %+v", f.outcomeAsked)
	}
	list, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	want := [][2]string{
		{"做完的结论", "修好登录页横屏崩溃，PR 已合并\nhttps://github.com/o/weather/pull/12"},
		{"横屏布局怎么改", "改成自适应宽度"},
		{"踩过的坑", "瘦身的 iPad 模拟器复现不了，要用原版"},
	}
	if len(list) != len(want) {
		t.Fatalf("decisions = %+v", list)
	}
	for i, d := range list {
		if d.Question != want[i][0] || d.Choice != want[i][1] || d.Source != db.DecisionOutcome || d.SourceMachine != "box" ||
			d.SourceSession != "s1" || !strings.HasPrefix(d.SourceMessage, outcomeKey(todo.ID, done)) {
			t.Fatalf("decision %d = %+v", i, d)
		}
	}
	if list[1].Reason != "固定宽度在 iPad 上放不下" {
		t.Fatalf("reason = %q", list[1].Reason)
	}
	log := kbEng(t, s).Store.Log("天气-app", 10)
	if len(log) != 1 || !strings.Contains(log[0], " · 待办 · 修 登录页横屏崩溃 · 结论：修好登录页横屏崩溃，PR 已合并 https://github.com/o/weather/pull/12") {
		t.Fatalf("log = %v", log)
	}

	// The same completion again (another path, a retry): nothing new; even after a restart (its memory gone).
	s.recordTaskOutcome(todo, done)
	s.tasks.mu.Lock()
	s.tasks.outcomes = nil
	s.tasks.mu.Unlock()
	s.recordTaskOutcome(todo, done)
	if again, _ := database.ListTodoDecisions(context.Background(), todo.ID, true); len(again) != 3 || len(f.outcomeAsked) != 1 {
		t.Fatalf("again: %d decisions, %d model calls", len(again), len(f.outcomeAsked))
	}
	if log := kbEng(t, s).Store.Log("天气-app", 10); len(log) != 1 {
		t.Fatalf("log again = %v", log)
	}

	// Handed to an agent for the same todo, the outcome rows are not passed off as the user's own calls.
	if res, _ := s.TodoHandoff(context.Background(), TodoHandoff{TodoID: todo.ID, Text: "再看看"}); !strings.Contains(res.Text, "Agent 做完时记的，不是用户定的") {
		t.Fatalf("decisions handed on = %q", res.Text)
	}

	// A new agent for the project, through the hub (App "Hand to agent" → "New"): the background goes first.
	other, _ := s.createTodo(context.Background(), "加深色模式", "", "", "")
	res, err := s.TodoHandoff(context.Background(), TodoHandoff{TodoID: other.ID, Text: "加深色模式", New: true, MachineID: "box", Cwd: "/code/weather"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.Text, `<persona-context kind="context" title="项目 · 天气 App · 背景" url="">`) ||
		!strings.Contains(res.Text, "项目：天气 App 的 iOS 和安卓版") ||
		!strings.Contains(res.Text, "「修 登录页横屏崩溃」结论：修好登录页横屏崩溃，PR 已合并") ||
		!strings.HasSuffix(res.Text, "</persona-context>\n\n加深色模式") {
		t.Fatalf("handoff = %q", res.Text)
	}
	// To an agent already running (no New), or for another directory: as it was.
	if res, _ := s.TodoHandoff(context.Background(), TodoHandoff{TodoID: other.ID, Text: "加深色模式"}); res.Text != "加深色模式" {
		t.Fatalf("existing agent = %q", res.Text)
	}
	if res, _ := s.TodoHandoff(context.Background(), TodoHandoff{TodoID: other.ID, Text: "x", New: true, MachineID: "box", Cwd: "/tmp"}); res.Text != "x" {
		t.Fatalf("no project = %q", res.Text)
	}
}

// Without a model the outcome is the receipt's text, without decisions or pitfalls.
func TestTaskOutcomeWithoutAModel(t *testing.T) {
	database := todoTestDB(t)
	s, _, todo := outcomeEnv(t, database, outcomeReply) // f.outcome empty: the model fails
	s.recordTaskOutcome(todo, time.Now().UTC())
	list, _ := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if len(list) != 1 || list[0].Question != "做完的结论" || !strings.HasPrefix(list[0].Choice, "修好了：登录页在横屏时用了固定宽度。") ||
		!strings.HasSuffix(list[0].Choice, "\nhttps://github.com/o/weather/pull/12") {
		t.Fatalf("decisions = %+v", list)
	}
	if log := kbEng(t, s).Store.Log("天气-app", 10); len(log) != 1 || !strings.Contains(log[0], "· 结论：修好了：登录页") {
		t.Fatalf("log = %v", log)
	}
}

// The user marking a task done records its outcome too (in the background); a todo made by hand records nothing.
func TestUserDoneRecordsOutcome(t *testing.T) {
	s, f, todo := outcomeEnv(t, nil, outcomeReply)
	f.outcome = `{"outcome":"修好了","links":[],"decisions":[],"pitfalls":[]}`
	hand, _ := s.createTodo(context.Background(), "买菜", "", "", "")
	e := &todoEnv{t: t, s: s, f: f}
	e.must("PATCH", "/api/v1/todos/"+hand.ID, `{"done":true}`)
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
	deadline := time.Now().Add(10 * time.Second)
	var log []string
	for time.Now().Before(deadline) {
		if log = kbEng(t, s).Store.Log("天气-app", 10); len(log) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(log) != 1 || !strings.Contains(log[0], "· 待办 · 修 登录页横屏崩溃 · 结论：修好了") {
		t.Fatalf("log = %v", log)
	}
	f.mu.Lock()
	asked := len(f.outcomeAsked)
	f.mu.Unlock()
	if asked != 1 {
		t.Fatalf("model asked %d times, want once (not for the todo made by hand)", asked)
	}
}

// A task the main conversation hands out is supervised, so it closes when the main conversation accepts it
// (task_review), not by itself: accepting it records its outcome; cancelling it records none. With and without the
// database (whose ReviewDoneTodo closes it there).
func TestReviewAcceptedRecordsOutcome(t *testing.T) {
	for _, store := range []string{"memory", "postgres"} {
		t.Run(store, func(t *testing.T) {
			for _, c := range []struct {
				verdict string
				want    int
			}{{"cancelled", 0}, {"accepted", 1}} {
				var st Store
				if store == "postgres" {
					st = todoTestDB(t)
				}
				s, f, todo := outcomeEnv(t, st, outcomeReply)
				f.outcome = `{"outcome":"修好了","links":[],"decisions":[],"pitfalls":[]}`
				ctx := context.Background()
				s.superviseTodo(ctx, todo.ID, "conv1", []string{"PR 合并"}, 0)
				raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": c.verdict, "note": "看过了"})
				if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err != nil {
					t.Fatalf("%s: %v", c.verdict, err)
				}
				doneBy := db.DoneByReview
				if c.verdict == reviewCancelled {
					doneBy = db.DoneByCancelled
				}
				if done := todoNow(s, todo.ID); done.DoneAt == nil || done.DoneBy != doneBy {
					t.Fatalf("%s: not closed by the review: %+v", c.verdict, done)
				}
				s.waitTaskOutcomes() // an outcome being recorded (accepted, or wrongly for cancelled) is done by now
				log := kbEng(t, s).Store.Log("天气-app", 10)
				f.mu.Lock()
				asked := len(f.outcomeAsked)
				f.mu.Unlock()
				if len(log) != c.want || asked != c.want {
					t.Fatalf("%s: log = %v, model asked %d times, want %d", c.verdict, log, asked, c.want)
				}
				if c.want == 1 && !strings.Contains(log[0], "结论：修好了") {
					t.Fatalf("%s: log = %v", c.verdict, log)
				}
				if st, ok := st.(*db.DB); ok && c.want == 1 {
					list, _ := st.ListTodoDecisions(ctx, todo.ID, true)
					if len(list) != 1 || list[0].Source != db.DecisionOutcome || list[0].Choice != "修好了" {
						t.Fatalf("decisions = %+v", list)
					}
				}
			}
		})
	}
}

// A new agent the assistant starts (agent_start) gets its project's background before the prompt; its tab is named
// from the prompt as written.
func TestAgentStartCarriesProjectContext(t *testing.T) {
	s, f, todo := outcomeEnv(t, nil, outcomeReply)
	if err := kbEng(t, s).Store.AppendLog("天气-app", time.Now(), kb.TodoLogSource+kb.TodoLogTitle(todo.Title), "结论：修好了"); err != nil {
		t.Fatal(err)
	}
	f.tabReply = "深色模式"
	args, _ := json.Marshal(map[string]string{"machine": "box", "cwd": "/code/weather", "prompt": "加深色模式"})
	if _, err := s.runTool(context.Background(), "server", "conv9", "agent_start", args); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.starts) != 1 {
		t.Fatalf("starts = %+v", f.starts)
	}
	p, _ := f.starts[0]["prompt"].(string)
	if !strings.HasPrefix(p, `<persona-context kind="context" title="项目 · 天气 App · 背景"`) || !strings.Contains(p, "结论：修好了") ||
		!strings.HasSuffix(p, "\n\n加深色模式") {
		t.Fatalf("prompt = %q", p)
	}
	if len(f.tabAsked) == 0 || strings.Contains(f.tabAsked[len(f.tabAsked)-1], "persona-context") {
		t.Fatalf("tab named from %q", f.tabAsked)
	}
}

// What the model says is kept within bounds: one line, links that are links, at most five of each.
func TestTidyOutcome(t *testing.T) {
	var o taskOutcome
	_ = json.Unmarshal([]byte(`{"outcome":"  一\n二  ","links":["https://a/1","https://a/1","ftp://x","https://a/2","https://a/3","https://a/4"],
		"pitfalls":["a","a","","b","c","d","e","f"]}`), &o)
	got := tidyOutcome(o)
	if got.Outcome != "一 二" || len(got.Links) != 3 || got.Links[1] != "https://a/2" || len(got.Pitfalls) != 5 || got.Pitfalls[1] != "b" {
		t.Fatalf("tidy = %+v", got)
	}
}

// The timeline line keeps its links whole: a long outcome is clipped to the room they leave (kb cuts lines at 200).
func TestOutcomeLogLineKeepsLinks(t *testing.T) {
	long := strings.Repeat("修", 150)
	pr1, pr2 := "https://github.com/example/weather/pull/1234", "https://github.com/example/weather/pull/5678"
	line := outcomeLogLine(taskOutcome{Outcome: long, Links: []string{pr1, pr2}})
	if n := len([]rune(line)); n > outcomeLogMax {
		t.Fatalf("line is %d runes: %q", n, line)
	}
	if !strings.HasPrefix(line, "结论：修") || !strings.HasSuffix(line, " "+pr1+" "+pr2) || !strings.Contains(line, "…") {
		t.Fatalf("line = %q", line)
	}
	if got := outcomeLogLine(taskOutcome{Outcome: "修好了"}); got != "结论：修好了" {
		t.Fatalf("no links = %q", got)
	}
}
