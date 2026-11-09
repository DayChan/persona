package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// reviewNode is the fake node's side of a review: the change git.review reports, the copy worktree.snapshot makes, the
// reviewer's answer, and what the manager asked of it.
type reviewNode struct {
	change    proto.GitReview
	reply     string            // the reviewer's last answer ("" none)
	snapshots []map[string]any  // worktree.snapshot params
	closed    []string          // session.close ids
	removed   []map[string]any  // worktree.remove params
	reads     map[string]string // session id → text its items carry
}

const reviewCopy = "/wt/app/review-fix-login"

// withReviewNode answers f's node calls for a review as n says. f.mu is held while an answer runs.
func withReviewNode(f *fakeHub, n *reviewNode) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nodes == nil {
		f.nodes = map[string]nodeAnswer{}
	}
	f.nodes["git.review"] = func(_ string, _ json.RawMessage) (any, error) { return n.change, nil }
	f.nodes["worktree.snapshot"] = func(_ string, raw json.RawMessage) (any, error) {
		var a map[string]any
		_ = json.Unmarshal(raw, &a)
		n.snapshots = append(n.snapshots, a)
		return proto.WorktreeSnapshot{Path: reviewCopy, Root: "/code/app", Head: "abc123"}, nil
	}
	f.nodes["session.close"] = func(_ string, raw json.RawMessage) (any, error) {
		var a struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(raw, &a)
		n.closed = append(n.closed, a.SessionID)
		return map[string]any{"ok": true}, nil
	}
	f.nodes["worktree.remove"] = func(_ string, raw json.RawMessage) (any, error) {
		var a map[string]any
		_ = json.Unmarshal(raw, &a)
		n.removed = append(n.removed, a)
		return proto.WorktreeRemoved{Removed: true}, nil
	}
	f.nodes[DeviceSessionItems] = func(_ string, raw json.RawMessage) (any, error) {
		var a struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(raw, &a)
		if text, ok := n.reads[a.SessionID]; ok {
			return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: text}}}, nil
		}
		return map[string]any{"items": f.items}, nil
	}
}

func (n *reviewNode) answer(f *fakeHub, sid, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n.reads == nil {
		n.reads = map[string]string{}
	}
	n.reads[sid] = text
}

func (n *reviewNode) cleaned(f *fakeHub) ([]string, []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(n.closed), slices.Clone(n.removed)
}

func reviewChangeOf() proto.GitReview {
	return proto.GitReview{Root: "/code/app", Scope: proto.ReviewBranch, Base: proto.GitReviewBase{Short: "0a1b2c3", Subject: "init"},
		Files: []proto.GitReviewFile{{Path: "login/view.swift", Status: "modified", Added: 1, Deleted: 1,
			Hunks: []proto.GitHunk{{OldStart: 10, NewStart: 10, Lines: []string{" let w = width", "-if landscape { crash() }", "+if landscape { layout() }"}}}}}}
}

// reviewConfigured turns the review on for s, with its own launcher and model.
func reviewConfigured(f *fakeHub) {
	f.mu.Lock()
	f.hello.Agents.Review = assistantwire.ReviewConfig{Launcher: "claude-review", Model: "review-model"}
	f.mu.Unlock()
}

// startsOf are the session.start params f got.
func startsOf(f *fakeHub) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.starts)
}

func autoReviewOf(s *Service, id string) AutoReview {
	if a := todoNow(s, id).Supervision.AutoReview; a != nil {
		return *a
	}
	return AutoReview{}
}

// supervisedReview is a supervised task whose agent just finished (the judge says done) with the review configured;
// its reviewer started (s2).
func supervisedReview(t *testing.T) (*Service, *fakeHub, Todo, *reviewNode) {
	t.Helper()
	s, f, todo := newSupervisedService(t)
	reviewConfigured(f)
	n := &reviewNode{change: reviewChangeOf()}
	withReviewNode(f, n)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "修好了，我的思路是先改 layout 再补测试，PR https://github.com/o/r/pull/901", 0)
	waitFor(t, "the reviewer", func() bool { return autoReviewOf(s, todo.ID).SessionID != "" })
	return s, f, todo, n
}

// Off unless agents.review names a launcher: a finished task waits for review at once, no copy, no second agent.
func TestAutoReviewOnlyWhenConfigured(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	n := &reviewNode{change: reviewChangeOf()}
	withReviewNode(f, n)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "修好了", 0)
	waitFor(t, "the wait for review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	if v := todoNow(s, todo.ID).Supervision; v.AutoReview != nil {
		t.Fatalf("a review ran unconfigured: %+v", v.AutoReview)
	}
	if len(n.snapshots) != 0 || len(startsOf(f)) != 1 {
		t.Fatalf("snapshots %v, starts %d", n.snapshots, len(startsOf(f)))
	}
}

// The reviewer starts in the change's own copy, with its own launcher and model, read-only where the launcher can, and
// sees the task's ask, its criteria and the diff, never what the working agent said or where it works. Its blocking
// findings are the next rework round, sent to the working agent; then the reviewer is closed, its copy removed, and it
// never was one of the task's agents.
func TestAutoReviewBlockingFindingsRework(t *testing.T) {
	s, f, todo, n := supervisedReview(t)
	ctx := context.Background()
	if v := todoNow(s, todo.ID).Supervision; v.State != "" || v.AutoReview.State != autoRunning || v.AutoReview.Worktree != reviewCopy {
		t.Fatalf("while it runs: state %q, run %+v", v.State, v.AutoReview)
	}
	if len(n.snapshots) != 1 || n.snapshots[0]["cwd"] != "/code/app" {
		t.Fatalf("snapshot asked %v", n.snapshots)
	}
	starts := startsOf(f)
	if len(starts) != 2 {
		t.Fatalf("%d starts", len(starts))
	}
	r := starts[1]
	if r["cwd"] != reviewCopy || r["launcher"] != "claude-review" || r["model"] != "review-model" || r["permission_mode"] != "plan" {
		t.Fatalf("the reviewer's start: %v", r)
	}
	prompt, _ := r["prompt"].(string)
	for _, want := range []string{"任务：修登录崩溃", "iPad 横屏时登录页崩溃，修好并补测试", "- PR 合并到 master", "- 浅色 / 深色截图贴进 PR",
		"+if landscape { layout() }", "login/view.swift", `<review-findings count="N" blocking="M"/>`} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the reviewer's prompt lacks %q", want)
		}
	}
	for _, never := range []string{"我的思路是", "pull/901", "/code/app"} {
		if strings.Contains(prompt, never) {
			t.Errorf("the reviewer's prompt has the working agent's %q:\n%s", never, prompt)
		}
	}
	a := autoReviewOf(s, todo.ID)
	if !s.isReviewer("box", a.SessionID) || slices.ContainsFunc(s.taskAgents(ctx, todoNow(s, todo.ID)), func(c proto.Session) bool { return c.SessionID == a.SessionID }) {
		t.Fatalf("the reviewer is not linked apart from the task's agents: %+v", todoNow(s, todo.ID).Agents)
	}
	var link TodoAgent
	for _, ag := range todoNow(s, todo.ID).Agents {
		if ag.SessionID == a.SessionID {
			link = ag
		}
	}
	if link.Source != sourceReview || link.Via != a.ID || link.ReleasedAt == nil {
		t.Fatalf("the reviewer's link: %+v", link)
	}
	if !slices.ContainsFunc(s.todoReport(ctx).Quiet, func(q SessionRef) bool { return q.SessionID == a.SessionID }) {
		t.Fatal("the reviewer is not quiet")
	}

	n.answer(f, a.SessionID, "1. P0 login/view.swift:11 横屏仍会走旧路径 → 改成 layout()\n2. P2 命名\n<review-findings count=\"2\" blocking=\"1\"/>")
	s.autoReviewTurnEnded(ctx, "box", a.SessionID)
	v := todoNow(s, todo.ID).Supervision
	if v.State != reviewRework || v.Round != 1 || v.AutoReview.State != autoBlocking || v.AutoReview.Count != 2 || v.AutoReview.Blocking != 1 {
		t.Fatalf("after blocking findings: state %q round %d run %+v", v.State, v.Round, v.AutoReview)
	}
	if last := v.Reviews[len(v.Reviews)-1]; last.By != reviewerBy || last.Verdict != reviewRework || !strings.Contains(last.Note, "横屏仍会走旧路径") {
		t.Fatalf("the review on the ledger: %+v", last)
	}
	sent := sentToAgent(f)
	if len(sent) != 1 || !strings.Contains(sent[0], "独立评审没通过，第 1 轮返工") || !strings.Contains(sent[0], "P0 login/view.swift:11") ||
		strings.Contains(sent[0], "review-findings") {
		t.Fatalf("sent to the agent: %q", sent)
	}
	waitFor(t, "the reviewer closed and its copy removed", func() bool {
		closed, removed := n.cleaned(f)
		return slices.Equal(closed, []string{a.SessionID}) && len(removed) == 1 && removed[0]["path"] == reviewCopy && removed[0]["force"] == true
	})
	// The same turn end again decides nothing more.
	s.autoReviewTurnEnded(ctx, "box", a.SessionID)
	if got := len(sentToAgent(f)); got != 1 {
		t.Fatalf("sent %d times", got)
	}
}

// A reviewer linked to a todo is left out of the session list the apps get, while the node lists it.
func TestReviewerHiddenFromTheSessionList(t *testing.T) {
	f := newFakeHub()
	f.hello.Assistant.MachineID = "box"
	f.hello.Assistant.Machines = []string{"box"}
	f.devices = []Device{{ID: "box", Home: "/home/u"}}
	d := newFleetDevs()
	d.add("box", "Computer")
	st := newFleetMem()
	s := New(Options{Hub: f, Devices: d, Store: func() Store { return st }})
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "修登录崩溃", "iPad 横屏时登录页崩溃", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	d.setSessions("box", []proto.Session{{SessionID: "worker", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone},
		{SessionID: "reviewer", Cwd: reviewCopy, Agent: "claude", Status: proto.StatusWorking}})
	ids := func(list []proto.Session) []string {
		var out []string
		for _, c := range list {
			out = append(out, c.SessionID)
		}
		return out
	}
	if got := ids(s.publishList("box")); !slices.Equal(got, []string{"worker", "reviewer"}) {
		t.Fatalf("before the link: %v", got)
	}
	if err := s.linkReviewer(ctx, todo.ID, "box", "reviewer", reviewCopy, "rv_1"); err != nil {
		t.Fatal(err)
	}
	if got := ids(s.publishList("box")); !slices.Equal(got, []string{"worker"}) {
		t.Fatalf("the apps' list: %v", got)
	}
	if got := ids(s.decoratedUnarchived("box")); len(got) != 2 {
		t.Fatalf("the node's list: %v", got)
	}
}

// No blocking findings: the task waits for review, the result on its ledger, and the supervisor's one message about
// the task carries it.
func TestAutoReviewCleanWaitsForReview(t *testing.T) {
	s, f, todo, n := supervisedReview(t)
	a := autoReviewOf(s, todo.ID)
	n.answer(f, a.SessionID, "没有阻断问题。\n1. P2 login/view.swift:12 变量名可以更清楚\n<review-findings count=\"1\" blocking=\"0\"/>")
	s.autoReviewTurnEnded(context.Background(), "box", a.SessionID)
	v := todoNow(s, todo.ID).Supervision
	if v.State != reviewPending || v.Round != 0 || v.AutoReview.State != autoClean || v.AutoReview.Count != 1 ||
		!strings.Contains(v.AutoReview.Findings, "变量名可以更清楚") || strings.Contains(v.AutoReview.Findings, "review-findings") {
		t.Fatalf("after a clean review: state %q run %+v", v.State, v.AutoReview)
	}
	if len(sentToAgent(f)) != 0 {
		t.Fatalf("sent to the agent: %q", sentToAgent(f))
	}
	waitFor(t, "the supervisor's message", func() bool { return len(f.notices()) == 1 })
	msg := f.notices()[0]
	for _, want := range []string{"hub 判断做完了，等你验收", "独立评审（评审员只看了任务要求和改动", "没有阻断问题，1 条非阻断发现", "变量名可以更清楚"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message lacks %q:\n%s", want, msg)
		}
	}
}

// Blocking findings once the rework rounds are used: no more rework, the task waits for review with them attached.
func TestAutoReviewRoundLimit(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State, v.Round = reviewRework, maxReworkRounds
		return true
	}); err != nil {
		t.Fatal(err)
	}
	reviewConfigured(f)
	n := &reviewNode{change: reviewChangeOf()}
	withReviewNode(f, n)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "又改了一版", 0)
	waitFor(t, "the reviewer", func() bool { return autoReviewOf(s, todo.ID).SessionID != "" })
	a := autoReviewOf(s, todo.ID)
	n.answer(f, a.SessionID, "P0 login/view.swift:11 还是会崩\n<review-findings count=\"1\" blocking=\"1\"/>")
	s.autoReviewTurnEnded(ctx, "box", a.SessionID)
	v := todoNow(s, todo.ID).Supervision
	if v.State != reviewPending || v.Round != maxReworkRounds || v.AutoReview.State != autoBlocking || len(sentToAgent(f)) != 0 {
		t.Fatalf("over the limit: state %q round %d run %+v sent %q", v.State, v.Round, v.AutoReview, sentToAgent(f))
	}
	waitFor(t, "the supervisor's message", func() bool { return len(f.notices()) == 1 })
	if msg := f.notices()[0]; !strings.Contains(msg, "返工轮数用完了，还剩 1 条阻断问题") || !strings.Contains(msg, "还是会崩") {
		t.Fatalf("message:\n%s", msg)
	}
}

// A reviewer whose turn end nobody heard (the hub restarted meanwhile) is found by the tick: its answer ends the
// review. One that never answers fails at its deadline, and the task waits for review as without one.
func TestAutoReviewTickEndsAndTimesOut(t *testing.T) {
	s, f, todo, n := supervisedReview(t)
	ctx := context.Background()
	a := autoReviewOf(s, todo.ID)
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle},
		proto.Session{SessionID: a.SessionID, MachineID: "box", Cwd: reviewCopy, Agent: "claude", Status: proto.StatusWorking})
	s.autoReviewTick(ctx, time.Now())
	if autoReviewOf(s, todo.ID).State != autoRunning {
		t.Fatal("ended while the reviewer works")
	}
	n.answer(f, a.SessionID, "没有问题。\n<review-findings count=\"0\" blocking=\"0\"/>")
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle},
		proto.Session{SessionID: a.SessionID, MachineID: "box", Cwd: reviewCopy, Agent: "claude", Status: proto.StatusIdle})
	s.autoReviewTick(ctx, time.Now())
	if v := todoNow(s, todo.ID).Supervision; v.State != reviewPending || v.AutoReview.State != autoClean {
		t.Fatalf("after the tick: %q %+v", v.State, v.AutoReview)
	}

	s2, f2, todo2, _ := supervisedReview(t)
	s2.autoReviewTick(ctx, time.Now().Add(31*time.Minute))
	if v := todoNow(s2, todo2.ID).Supervision; v.State != reviewPending || v.AutoReview.State != autoFailed || v.AutoReview.Error != "评审超时" {
		t.Fatalf("past the deadline: %q %+v", v.State, v.AutoReview)
	}
	waitFor(t, "the supervisor's message", func() bool { return len(f2.notices()) == 1 })
	if msg := f2.notices()[0]; !strings.Contains(msg, "没评审成（评审超时），照常自己核对") {
		t.Fatalf("message:\n%s", msg)
	}
}

// The working agent starts again while its last work is under review: the review is moot, cancelled and cleaned up;
// nothing follows from its answer.
func TestAutoReviewCancelledWhenTheAgentWorks(t *testing.T) {
	s, f, todo, n := supervisedReview(t)
	a := autoReviewOf(s, todo.ID)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	if v := todoNow(s, todo.ID).Supervision; v.AutoReview.State != autoCancelled || v.State != "" {
		t.Fatalf("after the agent went on: %q %+v", v.State, v.AutoReview)
	}
	waitFor(t, "cleaned up", func() bool { closed, removed := n.cleaned(f); return len(closed) == 1 && len(removed) == 1 })
	n.answer(f, a.SessionID, "P0 x\n<review-findings count=\"1\" blocking=\"1\"/>")
	s.autoReviewTurnEnded(context.Background(), "box", a.SessionID)
	if v := todoNow(s, todo.ID).Supervision; v.Round != 0 || len(sentToAgent(f)) != 0 {
		t.Fatalf("a cancelled review reworked: %+v", v)
	}
}

// A copy that cannot be made, or a reviewer that does not start, fails the run: the task waits for review as before.
func TestAutoReviewStartFailures(t *testing.T) {
	for _, c := range []struct {
		name, want string
		breakIt    func(f *fakeHub)
	}{
		{"no copy", "没能为评审建一份改动的副本", func(f *fakeHub) {
			f.nodes["worktree.snapshot"] = func(string, json.RawMessage) (any, error) { return nil, fmt.Errorf("unknown method worktree.snapshot") }
		}},
		{"no reviewer", "评审员没开起来", func(f *fakeHub) { f.failStart = fmt.Errorf("launcher not found") }},
		{"no change", "没有可评审的代码改动", func(f *fakeHub) {
			f.nodes["git.review"] = func(string, json.RawMessage) (any, error) { return proto.GitReview{Scope: proto.ReviewBranch}, nil }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			reviewConfigured(f)
			withReviewNode(f, &reviewNode{change: reviewChangeOf()})
			agentTurn(s, f, proto.StatusWorking, "", 0)
			f.mu.Lock()
			c.breakIt(f)
			f.mu.Unlock()
			agentTurn(s, f, proto.StatusDone, "修好了", 0)
			waitFor(t, "the wait for review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
			if a := autoReviewOf(s, todo.ID); a.State != autoFailed || !strings.Contains(a.Error, c.want) {
				t.Fatalf("run %+v", a)
			}
		})
	}
}

// The reviewer's count line: the last one wins and is taken out of the report; none is no count.
func TestParseReviewFindings(t *testing.T) {
	for _, c := range []struct {
		in, findings    string
		count, blocking int
		ok              bool
	}{
		{"A\n<review-findings count=\"2\" blocking=\"1\"/>", "A", 2, 1, true},
		{"A <review-findings count=3 blocking = 0 />\nB\n<review-findings count=\"1\" blocking=\"1\"/>", "A \nB", 1, 1, true},
		{"没有问题", "没有问题", 0, 0, false},
		{"<review-findings count=\"0\" blocking=\"2\"/>", "", 2, 2, true}, // blocking ones are findings too
	} {
		f, n, b, ok := parseReviewFindings(c.in)
		if f != c.findings || n != c.count || b != c.blocking || ok != c.ok {
			t.Errorf("%q: %q %d %d %v", c.in, f, n, b, ok)
		}
	}
}

// The run is on the ledger in Postgres: a manager started again on the same database knows the running review, its
// reviewer and copy, and its tick ends it from the reviewer's answer.
func TestAutoReviewSurvivesRestart(t *testing.T) {
	database, err := db.Open(context.Background(), newDatabase(t), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, f := newQuietServiceOn(t, database)
	f.judge = `{"verdict":"done","reason":"PR 已合并"}`
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"title": "修登录崩溃", "prompt": "iPad 横屏时登录页崩溃，修好并补测试",
		"machine": "box", "cwd": "/code/app", "readonly": true, "why": "最近在 box 上", "done_when": []string{"PR 合并到 master"}}}})
	if _, err := s.toolTaskPlan(context.Background(), "conv1", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	reviewConfigured(f)
	n := &reviewNode{change: reviewChangeOf()}
	withReviewNode(f, n)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "修好了", 0)
	waitFor(t, "the reviewer", func() bool { return autoReviewOf(s, todo.ID).SessionID != "" })
	a := autoReviewOf(s, todo.ID)

	again, f2 := newQuietServiceOn(t, database) // the manager after a restart: memory gone, the database kept
	reviewConfigured(f2)
	withReviewNode(f2, n)
	f2.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle},
		proto.Session{SessionID: a.SessionID, MachineID: "box", Cwd: reviewCopy, Agent: "claude", Status: proto.StatusIdle})
	if err := again.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := autoReviewOf(again, todo.ID)
	if got.ID != a.ID || got.State != autoRunning || got.SessionID != a.SessionID || got.Worktree != reviewCopy || !again.isReviewer("box", a.SessionID) {
		t.Fatalf("after the restart: %+v", got)
	}
	n.answer(f2, a.SessionID, "P1 login/view.swift:11 缺测试\n<review-findings count=\"1\" blocking=\"1\"/>")
	again.autoReviewTick(context.Background(), time.Now())
	if v := todoNow(again, todo.ID).Supervision; v.State != reviewRework || v.Round != 1 || v.AutoReview.State != autoBlocking {
		t.Fatalf("ended after the restart: %q %d %+v", v.State, v.Round, v.AutoReview)
	}
	rows, err := database.ListTodoSupervision(context.Background())
	var stored AutoReview
	if err != nil || len(rows) != 1 || json.Unmarshal(rows[0].AutoReview, &stored) != nil || stored.State != autoBlocking || stored.SessionID != a.SessionID {
		t.Fatalf("stored: %+v %v", rows, err)
	}
}

func TestGoldenAutoReview(t *testing.T) {
	golden(t, "agent_review.prompt", prompts.RenderAgentReview(prompts.AgentReview{Title: "修登录崩溃", Ask: "iPad 横屏时登录页崩溃，修好并补测试",
		DoneWhen: []string{"PR 合并到 master", "浅色 / 深色截图贴进 PR"}, Scope: reviewScopeLine(reviewChangeOf()),
		Diff: func() string { d, _ := reviewDiff(reviewChangeOf()); return d }(), Omitted: 2}))
	golden(t, "supervise.review_rework", prompts.RenderSuperviseReviewRework(prompts.SuperviseReviewRework{Round: 1, Max: 2, Count: 2, Blocking: 1,
		Findings: "1. P0 login/view.swift:11 横屏仍会走旧路径 → 改成 layout()\n2. P2 命名"}))
}
