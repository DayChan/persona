package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The independent review (agents_review.go, #1808): starting one (the refusals and the read-only start), the seed
// (the diff and the user's asks within their budgets), the findings marker, the card when the reviewer finishes, its
// answers, the book's persistence and the reviewer hidden from the session lists. The fleet harness carries the
// machine "mac"; git.review, session.items and session.start are the fake node's.

// reviewGitFake is the node's git.review: what scopes were asked and what to answer.
type reviewGitFake struct {
	mu     sync.Mutex
	scopes []string
	answer func(scope string) (any, error)
}

func (g *reviewGitFake) nodeAnswer(_ string, params json.RawMessage) (any, error) {
	var p proto.GitReviewRequest
	_ = json.Unmarshal(params, &p)
	g.mu.Lock()
	g.scopes = append(g.scopes, p.Scope)
	a := g.answer
	g.mu.Unlock()
	if a != nil {
		return a(p.Scope)
	}
	return sampleGitReview(p.Scope), nil
}

func (g *reviewGitFake) asked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.scopes...)
}

// sampleGitReview is a review of one small change plus one of each kind of file that carries no hunks.
func sampleGitReview(scope string) proto.GitReview {
	return proto.GitReview{Root: "/code/app", Scope: scope, Base: proto.GitReviewBase{Hash: "0000000000", Short: "abc1234",
		Subject: "修复登录页", Ref: "origin/main"}, Files: []proto.GitReviewFile{
		{Path: "login.go", Status: "modified", Added: 2, Deleted: 1, Revision: "e1", Hunks: []proto.GitHunk{
			{Header: "func login", OldStart: 10, NewStart: 10, Lines: []string{" func login() {", "+\tcheck()", " }"}},
		}},
		{Path: "logo.png", Status: "modified", Binary: true},
		{Path: "secret.key", Status: "added", Refused: true},
		{Path: "big.go", Status: "modified", Added: 9000, Big: true},
	}}
}

// reviewService is a manager on the fleet harness with git.review wired.
func reviewService(t *testing.T) (*fleetTest, *reviewGitFake) {
	t.Helper()
	ft := newFleetTest(t)
	ft.f.setSessions("mac") // the fake node's machine (the node calls go to it)
	git := &reviewGitFake{}
	ft.f.answerNode("git.review", git.nodeAnswer)
	return ft, git
}

// reviewSource puts a stopped Claude session on the machine and returns it.
func reviewSource(ft *fleetTest, sid string) proto.Session {
	ft.t.Helper()
	c := proto.Session{MachineID: "mac", SessionID: sid, Title: "修登录", Cwd: "/code/app", Agent: "claude", Launcher: "cc_ds",
		Status: proto.StatusDone, State: proto.StateUnread, UpdatedAt: time.Now().Add(-time.Minute)}
	ft.d.setSessions("mac", []proto.Session{c})
	return c
}

func reviewStart(t *testing.T, ft *fleetTest, sid string) int {
	t.Helper()
	w := serve(ft.s, "POST", "/api/v1/machines/mac/sessions/"+sid+"/review", "")
	if w.Code != 200 && w.Code != 409 && w.Code != 404 {
		t.Fatalf("review start: %d %s", w.Code, w.Body)
	}
	return w.Code
}

// startReview starts a review of sid and returns the reviewer's session id (the fake node's s1, s2…).
func startReview(t *testing.T, ft *fleetTest, sid string) string {
	t.Helper()
	w := serve(ft.s, "POST", "/api/v1/machines/mac/sessions/"+sid+"/review", "")
	if w.Code != 200 {
		t.Fatalf("review start: %d %s", w.Code, w.Body)
	}
	var out struct {
		Running           bool   `json:"running"`
		ReviewerSessionID string `json:"reviewer_session_id"`
		StartedAt         string `json:"started_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Running || out.ReviewerSessionID == "" || out.StartedAt == "" {
		t.Fatalf("start answer: %+v", out)
	}
	return out.ReviewerSessionID
}

// finishReviewAs has the reviewer answer and takes its turn end, as TurnEnded would.
func finishReviewAs(t *testing.T, ft *fleetTest, reviewer, reply string) {
	t.Helper()
	ft.f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: reply})
	ft.s.reviewTurnEnded(context.Background(), "mac", reviewer)
}

// pendingReviewCard is the session's one pending review card (nil when there is none).
func pendingReviewCard(t *testing.T, s *Service, sid string) *proto.Interaction {
	t.Helper()
	for _, ix := range s.reviewCards() {
		if ix.SessionID == sid {
			return &ix
		}
	}
	return nil
}

// answerReview answers card id (manager.action), telling the error answerCard would fail on.
func answerReview(s *Service, id string, r proto.Resolution) (ActionResult, error) {
	raw, _ := json.Marshal(r)
	return s.Action(context.Background(), Action{Op: "answer", InteractionID: id, Answer: raw})
}

// ---- starting ----

// POST …/review's refusals, and what a start that goes through carries.
func TestAgentReviewStart(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source func(proto.Session) proto.Session // changes the source session before the POST
		git    func(scope string) (any, error)   // what git.review answers
		status int                               // 0: this row is answered its own way below
		code   string
	}{
		{name: "busy working", source: func(c proto.Session) proto.Session { c.Status = proto.StatusWorking; return c }, status: 409, code: "session_busy"},
		{name: "busy blocked", source: func(c proto.Session) proto.Session { c.Status = proto.StatusBlocked; return c }, status: 409, code: "session_busy"},
		{name: "busy starting", source: func(c proto.Session) proto.Session { c.Starting = true; return c }, status: 409, code: "session_busy"},
		{name: "codex agent", source: func(c proto.Session) proto.Session { c.Agent, c.AgentLabel = "codex", "Codex"; return c }, status: 409, code: "read_only_unavailable"},
		{name: "runtime agent", source: func(c proto.Session) proto.Session { c.Agent, c.AgentLabel = "dsh", "DSH"; return c }, status: 409, code: "read_only_unavailable"},
		{name: "not git", git: func(string) (any, error) {
			return nil, &rpc.Error{Code: proto.CodeNotGit, Message: "not a repository"}
		}, status: 409, code: "no_change"},
		{name: "empty review", git: func(string) (any, error) {
			return proto.GitReview{Root: "/code/app", Scope: proto.ReviewBranch, Base: proto.GitReviewBase{Short: "abc1234"}}, nil
		}, status: 409, code: "no_change"},
		{name: "start refused", git: nil, status: 0}, // answered below: the node refuses session.start
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft, git := reviewService(t)
			c := reviewSource(ft, "src1")
			if tc.source != nil {
				c = tc.source(c)
				ft.d.setSessions("mac", []proto.Session{c})
			}
			if tc.git != nil {
				git.answer = tc.git
			}
			if tc.name == "start refused" {
				ft.f.failStart = errors.New("no engine here")
			}
			w := serve(ft.s, "POST", "/api/v1/machines/mac/sessions/src1/review", "")
			switch tc.name {
			case "start refused":
				if w.Code != 409 || !strings.Contains(w.Body.String(), `"not_supported"`) || !strings.Contains(w.Body.String(), "no engine here") {
					t.Fatalf("a refused start: %d %s", w.Code, w.Body)
				}
			default:
				if w.Code != tc.status || !strings.Contains(w.Body.String(), `"`+tc.code+`"`) {
					t.Fatalf("%d %s, want %d %s", w.Code, w.Body, tc.status, tc.code)
				}
			}
			if tc.name != "start refused" && len(ft.f.starts) != 0 {
				t.Fatalf("a refused review started a session: %+v", ft.f.starts)
			}
		})
	}

	// An unknown machine and an unknown session.
	ft, _ := reviewService(t)
	reviewSource(ft, "src1")
	if w := serve(ft.s, "POST", "/api/v1/machines/nowhere/sessions/src1/review", ""); w.Code != 404 {
		t.Fatalf("unknown machine: %d %s", w.Code, w.Body)
	}
	if w := serve(ft.s, "POST", "/api/v1/machines/mac/sessions/gone/review", ""); w.Code != 404 || !strings.Contains(w.Body.String(), "no_session") {
		t.Fatalf("unknown session: %d %s", w.Code, w.Body)
	}
}

// A start that goes through: read-only by its permission mode, on the source's directory, seeded with the rendered
// prompt; the branch scope first, uncommitted when the branch has no base.
func TestAgentReviewStartReadonly(t *testing.T) {
	ft, git := reviewService(t)
	reviewSource(ft, "src1")
	ft.f.setItems(
		proto.Item{Kind: proto.KindText, Role: "assistant", Text: "我改好了"},
		proto.Item{Kind: proto.KindText, Role: "user", Text: "修一下登录页的报错"},
		proto.Item{Kind: proto.KindToolUse, Role: "assistant", ToolName: "Bash"},
	)
	reviewer := startReview(t, ft, "src1")
	if len(ft.f.starts) != 1 {
		t.Fatalf("%d starts", len(ft.f.starts))
	}
	p := ft.f.starts[0]
	if p["permission_mode"] != "plan" || p["cwd"] != "/code/app" || p["async"] != true || p["launcher"] != "cc_ds" {
		t.Fatalf("start params: %+v", p)
	}
	if p["tab_name"] != "review-"+short("src1") {
		t.Fatalf("tab name: %v", p["tab_name"])
	}
	prompt, _ := p["prompt"].(string)
	if !strings.Contains(prompt, "<review-findings count=") || !strings.Contains(prompt, "修一下登录页的报错") ||
		!strings.Contains(prompt, "abc1234") || !strings.Contains(prompt, "+\tcheck()") {
		t.Fatalf("prompt: %s", clip(prompt, 400))
	}
	if !strings.Contains(prompt, "未展示") || !strings.Contains(prompt, "二进制文件") || !strings.Contains(prompt, "太大") {
		t.Fatalf("the files without hunks are not listed: %s", clip(prompt, 400))
	}
	if asked := git.asked(); len(asked) != 1 || asked[0] != proto.ReviewBranch {
		t.Fatalf("scopes: %v", asked)
	}
	// The reviewer is in the book as running; a second review of the same session is refused.
	if w := serve(ft.s, "POST", "/api/v1/machines/mac/sessions/src1/review", ""); w.Code != 409 || !strings.Contains(w.Body.String(), "review_running") {
		t.Fatalf("second review: %d %s", w.Code, w.Body)
	}
	if got := serve(ft.s, "GET", "/api/v1/machines/mac/sessions/src1/review", ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"running":true`) {
		t.Fatalf("state while running: %d %s", got.Code, got.Body)
	}
	_ = reviewer

	// The branch has no default branch: the uncommitted scope carries the review.
	ft2, git2 := reviewService(t)
	reviewSource(ft2, "src1")
	git2.answer = func(scope string) (any, error) {
		if scope == proto.ReviewBranch {
			return nil, &rpc.Error{Code: proto.CodeNoDefaultBranch, Message: "no default branch"}
		}
		return sampleGitReview(scope), nil
	}
	startReview(t, ft2, "src1")
	if asked := git2.asked(); len(asked) != 2 || asked[0] != proto.ReviewBranch || asked[1] != proto.ReviewUncommitted {
		t.Fatalf("the fallback scope: %v", asked)
	}
	prompt, _ = ft2.f.starts[0]["prompt"].(string)
	if !strings.Contains(prompt, "未提交的改动") {
		t.Fatalf("the uncommitted scope line: %s", clip(prompt, 200))
	}
}

// ---- the seed's budgets ----

// The diff: a file over its share is one line and spends none of the total budget; once the total runs out the files
// after it are one lines too, and the note counts them.
func TestAgentReviewDiffBudgets(t *testing.T) {
	one := sampleGitReview(proto.ReviewBranch)
	out, omitted := reviewDiff(one)
	if !strings.Contains(out, "diff --git a/login.go b/login.go") || !strings.Contains(out, "+\tcheck()") {
		t.Fatalf("the small change's hunks: %s", out)
	}
	for _, f := range []struct{ path, note string }{{"logo.png", "二进制文件"}, {"secret.key", "未展示"}, {"big.go", "太大"}} {
		if !strings.Contains(out, f.path+" — +") || !strings.Contains(out, f.note) {
			t.Fatalf("%s is not listed as one line: %s", f.path, out)
		}
	}
	if omitted != 0 {
		t.Fatalf("omitted: %d", omitted)
	}

	// A file over the per-file budget is a one line even alone.
	over := proto.GitReview{Scope: proto.ReviewBranch, Files: []proto.GitReviewFile{{Path: "huge.go", Status: "modified",
		Added: 40000, Hunks: []proto.GitHunk{{OldStart: 1, NewStart: 1, Lines: []string{"+" + strings.Repeat("x", reviewFileBudget)}}}}}}
	out, _ = reviewDiff(over)
	if strings.Contains(out, "xxxx") || !strings.Contains(out, "huge.go — +40000/−0 行，diff 略（太大）") {
		t.Fatalf("the over-budget file: %s", clip(out, 200))
	}

	// The total budget: files after it are one lines, omitted counts them.
	var many []proto.GitReviewFile
	for i := range 20 {
		many = append(many, proto.GitReviewFile{Path: "f" + string(rune('a'+i)) + ".go", Status: "modified", Added: 100,
			Hunks: []proto.GitHunk{{OldStart: 1, NewStart: 1, Lines: []string{"+" + strings.Repeat("y", 10_000)}}}})
	}
	out, omitted = reviewDiff(proto.GitReview{Scope: proto.ReviewBranch, Files: many})
	if listed := strings.Count(out, "diff 略（超总量）"); listed != omitted || omitted == 0 {
		t.Fatalf("%d one lines for %d omitted: %s", listed, omitted, clip(out, 200))
	}
}

// The asks: only the user's own text messages, the most recent kept whole, each clipped.
func TestAgentReviewAsks(t *testing.T) {
	ft, _ := reviewService(t)
	ctx := context.Background()
	var items []proto.Item
	for i := range 10 {
		letter := string(rune('a' + i))
		items = append(items,
			proto.Item{Kind: proto.KindText, Role: "assistant", Text: "回复" + letter},
			proto.Item{Kind: proto.KindText, Role: "user", Text: letter + strings.Repeat("要", 5_000)},
		)
	}
	items = append(items, proto.Item{Kind: proto.KindToolUse, Role: "user", ToolName: "Bash"})
	ft.f.setItems(items...)
	asks := ft.s.reviewAsks(ctx, "mac", "src1")
	if len(asks) != 5 { // 24000 runes: the newest five fit whole, the oldest five go
		t.Fatalf("%d asks: %v", len(asks), asks)
	}
	if !strings.HasPrefix(asks[0], "f") || !strings.HasPrefix(asks[len(asks)-1], "j") {
		t.Fatalf("not the most recent: %v…%v", clip(asks[0], 10), clip(asks[len(asks)-1], 10))
	}
	for _, a := range asks {
		if n := len([]rune(a)); n != reviewAskMax+1 { // clipped, with the ellipsis
			t.Fatalf("an ask of %d runes", n)
		}
	}
}

// ---- the marker ----

func TestParseReviewFindings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		clean string
		count int // -1: no readable count
	}{
		{"parsed", "两条发现。\n- P1 a.go:1\n\n<review-findings count=\"2\"/>", "两条发现。\n- P1 a.go:1", 2},
		{"quotes optional", "ok\n<review-findings count=7/>", "ok", 7},
		{"whitespace flexible", "ok\n<review-findings\tcount\n=\n\"9\"/>", "ok", 9},
		{"last wins", "a\n<review-findings count=1/>\nb\n<review-findings  count = \"5\" />\nc", "a\n\nb\n\nc", 5},
		{"absent", "没有问题。", "没有问题。", -1},
		{"malformed ignored", "x\n<review-findings count=\"two\"/>", "x\n<review-findings count=\"two\"/>", -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clean, count := parseReviewFindings(tc.text)
			if clean != tc.clean {
				t.Fatalf("clean: %q, want %q", clean, tc.clean)
			}
			switch {
			case tc.count < 0 && count != nil:
				t.Fatalf("count: %v, want none", *count)
			case tc.count >= 0 && (count == nil || *count != tc.count):
				t.Fatalf("count: %v, want %d", count, tc.count)
			}
		})
	}
}

// ---- the turn end ----

// The reviewer's last reply becomes the source session's pending card; a reviewer that said nothing fails the review.
func TestAgentReviewTurnEnd(t *testing.T) {
	ft, _ := reviewService(t)
	reviewSource(ft, "src1")
	reviewer := startReview(t, ft, "src1")

	finishReviewAs(t, ft, reviewer, "两条发现：\n- P1 login.go:11 少了空值检查\n建议补上。\n\n<review-findings count=\"2\"/>")
	ix := pendingReviewCard(t, ft.s, "src1")
	if ix == nil {
		t.Fatal("no card")
	}
	if ix.Kind != proto.IxReview || ix.ToolName != assistantwire.ReviewCardTool || !strings.HasPrefix(ix.ID, "review_") {
		t.Fatalf("card: %+v", ix)
	}
	if ix.FindingsCount == nil || *ix.FindingsCount != 2 || strings.Contains(ix.Findings, "review-findings") {
		t.Fatalf("findings: %+v", ix.FindingsCount)
	}
	if ix.Findings != "两条发现：\n- P1 login.go:11 少了空值检查\n建议补上。" {
		t.Fatalf("findings text: %q", ix.Findings)
	}
	if ix.ReviewSession != reviewer || ix.MachineID != "mac" || ix.SessionID != "src1" {
		t.Fatalf("card points elsewhere: %+v", ix)
	}
	if !strings.Contains(ix.Summary, "2") {
		t.Fatalf("summary: %q", ix.Summary)
	}
	if !ix.ExpiresAt.After(time.Now().Add(reviewCardTTL - time.Hour)) {
		t.Fatalf("expires: %v", ix.ExpiresAt)
	}
	if snap := ft.s.snapshot(context.Background()); len(snap.Interactions) == 0 {
		t.Fatal("the snapshot does not carry the card")
	}
	// A reviewer that said nothing fails the review: no card, and a new one may start.
	ft2, _ := reviewService(t)
	reviewSource(ft2, "src1")
	finishReviewAs(t, ft2, startReview(t, ft2, "src1"), "")
	if ix := pendingReviewCard(t, ft2.s, "src1"); ix != nil {
		t.Fatalf("a failed review left a card: %+v", ix)
	}
	startReview(t, ft2, "src1") // review_running cleared
}

// ---- the answers ----

// Send carries the findings to the SOURCE session and closes the card; a failed send keeps it; dismiss closes it.
func TestAgentReviewAnswer(t *testing.T) {
	ft, _ := reviewService(t)
	reviewSource(ft, "src1")
	reviewer := startReview(t, ft, "src1")
	finishReviewAs(t, ft, reviewer, "一条发现。\n<review-findings count=\"1\"/>")
	ix := pendingReviewCard(t, ft.s, "src1")

	// A failed send keeps the card.
	ft.f.failSend = errors.New("mac is offline")
	if _, err := answerReview(ft.s, ix.ID, proto.Resolution{Action: "send"}); err == nil {
		t.Fatal("the failed send was not told")
	}
	if kept := pendingReviewCard(t, ft.s, "src1"); kept == nil || kept.ID != ix.ID {
		t.Fatalf("the card did not stay: %+v", kept)
	}
	ft.f.failSend = nil

	if _, err := answerReview(ft.s, ix.ID, proto.Resolution{Action: "send"}); err != nil {
		t.Fatal(err)
	}
	sent := ft.f.sentToAgents()
	if len(sent) != 1 || !strings.HasPrefix(sent[0], "src1: ") {
		t.Fatalf("sent: %v", sent)
	}
	if !strings.Contains(sent[0], "一条发现。") || !strings.Contains(sent[0], "共提出 1 条发现") || strings.Contains(sent[0], "review-findings") {
		t.Fatalf("the sent text: %s", clip(sent[0], 300))
	}
	if kept := pendingReviewCard(t, ft.s, "src1"); kept != nil {
		t.Fatalf("the card stayed after the send: %+v", kept)
	}

	// Dismiss closes the card without sending anything.
	reviewer = startReview(t, ft, "src1")
	finishReviewAs(t, ft, reviewer, "又一条。\n<review-findings count=\"1\"/>")
	ix = pendingReviewCard(t, ft.s, "src1")
	if _, err := answerReview(ft.s, ix.ID, proto.Resolution{Dismiss: true}); err != nil {
		t.Fatal(err)
	}
	if kept := pendingReviewCard(t, ft.s, "src1"); kept != nil {
		t.Fatalf("the card stayed after the dismiss: %+v", kept)
	}
	if sent := ft.f.sentToAgents(); len(sent) != 1 {
		t.Fatalf("the dismiss sent something: %v", sent)
	}

	// Anything else is a bad request.
	reviewer = startReview(t, ft, "src1")
	finishReviewAs(t, ft, reviewer, "x\n<review-findings count=\"0\"/>")
	ix = pendingReviewCard(t, ft.s, "src1")
	if _, err := ft.s.Action(context.Background(), Action{Op: "answer", InteractionID: ix.ID,
		Answer: json.RawMessage(`{"action":"reply"}`)}); err == nil || !strings.Contains(err.Error(), "send") {
		t.Fatalf("a wrong action: %v", err)
	}
	// A cancel with an answer in it is refused as everywhere else.
	if _, err := ft.s.Action(context.Background(), Action{Op: "answer", InteractionID: ix.ID,
		Answer: json.RawMessage(`{"cancel":true,"action":"send"}`)}); err == nil {
		t.Fatal("a mixed cancel was taken")
	}
}

// ---- persistence ----

// The book is kept in the store; a restart puts pending cards back and fails reviews that ran past their deadline.
func TestAgentReviewPersistence(t *testing.T) {
	ft, _ := reviewService(t)
	reviewSource(ft, "src1")
	reviewer := startReview(t, ft, "src1")
	finishReviewAs(t, ft, reviewer, "发现。\n<review-findings count=\"1\"/>")
	ix := pendingReviewCard(t, ft.s, "src1")
	if ix == nil {
		t.Fatal("no card")
	}
	// A review still running when the manager went away.
	stale := agentReview{ID: "review_stale", MachineID: "mac", SessionID: "src2", ReviewerID: "s9", ReviewerMachineID: "mac",
		StartedAt: time.Now().Add(-reviewDeadline - time.Minute), State: reviewStateRunning}
	ft.s.reviewPut(stale)

	// A new manager on the same store.
	restarted := newFleetTest(t)
	restarted.f.setSessions("mac")
	restarted.s.store = func() Store { return ft.st }
	if !restarted.s.loadReviews(context.Background()) {
		t.Fatal("the book did not load")
	}
	if card := pendingReviewCard(t, restarted.s, "src1"); card == nil || card.ID != ix.ID || card.Findings != "发现。" {
		t.Fatalf("the card did not come back: %+v", card)
	}
	restarted.s.reviews.mu.Lock()
	var failed *agentReview
	for i := range restarted.s.reviews.reviews {
		if restarted.s.reviews.reviews[i].ID == "review_stale" {
			failed = &restarted.s.reviews.reviews[i]
		}
	}
	restarted.s.reviews.mu.Unlock()
	if failed == nil || failed.State != reviewStateFailed {
		t.Fatalf("the stale review: %+v", failed)
	}
}

// A second finished review of the same source replaces its pending card.
func TestAgentReviewReplacement(t *testing.T) {
	ft, _ := reviewService(t)
	reviewSource(ft, "src1")
	finishReviewAs(t, ft, startReview(t, ft, "src1"), "第一轮。\n<review-findings count=\"1\"/>")
	first := pendingReviewCard(t, ft.s, "src1")
	// A new review starts: the pending card of the old one goes.
	second := startReview(t, ft, "src1")
	if kept := pendingReviewCard(t, ft.s, "src1"); kept != nil {
		t.Fatalf("the old card stayed: %+v", kept)
	}
	finishReviewAs(t, ft, second, "第二轮。\n<review-findings count=\"0\"/>")
	ix := pendingReviewCard(t, ft.s, "src1")
	if ix == nil || ix.ID == first.ID || ix.Findings != "第二轮。" {
		t.Fatalf("the new card: %+v", ix)
	}
	ft.s.reviews.mu.Lock()
	ids := map[string]bool{}
	for _, e := range ft.s.reviews.reviews {
		ids[e.ID] = true
	}
	ft.s.reviews.mu.Unlock()
	if ids[first.ID] {
		t.Fatalf("the replaced review is still in the book: %v", ids)
	}
}

// ---- hidden from the lists ----

// The reviewer never shows in the apps' session lists; the source session does.
func TestAgentReviewHiddenFromList(t *testing.T) {
	ft, _ := reviewService(t)
	c := reviewSource(ft, "src1")
	reviewer := startReview(t, ft, "src1")
	// Both sessions as the node lists them.
	reviewing := proto.Session{MachineID: "mac", SessionID: reviewer, Title: "review-src1", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()}
	ft.d.setSessions("mac", []proto.Session{c, reviewing})
	ft.event("mac", "sessions.changed", map[string]any{"sessions": []proto.Session{c, reviewing}})
	list := ft.lastList("mac")
	ids := map[string]bool{}
	for _, sess := range list {
		ids[sess.SessionID] = true
	}
	if ids[reviewer] || !ids["src1"] {
		t.Fatalf("the list: %v", ids)
	}
	// The manager's own view still finds the reviewer.
	if _, ok := ft.s.decoratedSession("mac", reviewer); !ok {
		t.Fatal("the manager lost the reviewer row")
	}
}
