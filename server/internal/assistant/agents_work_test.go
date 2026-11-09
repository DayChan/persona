package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The Agents business (agents_*.go, docs/assistant/agent-work.md): the triage of a stopped turn, what the manager does about
// it, its cards and their answers, the switches. The hub's machine "box" is the fake's; what the hub shows of it is
// the hub's tests' (hub/agents_link_test.go).

// The unit tests read the snapshot right after a change.
func init() { workPublishDelay = 0 }

func workService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub()
	f.sessions["box"] = nil
	s := newTestService(t, f)
	s.work.switches = &TriageSwitches{Enabled: true, AutoContinue: true, Wake: true, Decide: true}
	return s, f
}

var turnEnd = time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)

// stopped is a session as the hub lists it once its turn ended unseen.
func stopped(id, last string) proto.Session {
	return proto.Session{SessionID: id, Title: "修登录", Cwd: "/code/app", Status: proto.StatusDone, State: proto.StateUnread,
		UpdatedAt: turnEnd, LastText: last}
}

// apiErrorTurn is stopped with the model API error its turn ended in (the hub's turn_error).
func apiErrorTurn(id, errText string) proto.Session {
	c := stopped(id, errText)
	c.TurnError = errText
	return c
}

func entryOf(t *testing.T, s *Service, sid string) db.SessionWork {
	t.Helper()
	e, ok := s.workEntry([2]string{"box", sid})
	if !ok {
		t.Fatalf("no work entry for %s", sid)
	}
	return e
}

// triage triages sid's turn on box as the hub lists it now.
func triage(t *testing.T, s *Service, sid string) {
	t.Helper()
	ctx := context.Background()
	v := s.newWorkView(ctx)
	c, ok := v.session("box", sid)
	if !ok {
		t.Fatalf("no session %s", sid)
	}
	s.triageTurn(ctx, v, "box", c)
}

// sentToAgents are the session.send the manager made, as "session: text".
func (f *fakeHub) sentToAgents() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.agentSent {
		out = append(out, p["session_id"].(string)+": "+p["text"].(string))
	}
	return out
}

// answerCard is the user's answer to card id (manager.action).
func answerCard(t *testing.T, s *Service, id string, r proto.Resolution) {
	t.Helper()
	raw, _ := json.Marshal(r)
	if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: id, Answer: raw}); err != nil {
		t.Fatal(err)
	}
}

// serveAs is an app's request as the hub relays it, from a device with role ("" a full one).
func serveAs(s *Service, role, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		r.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func serve(s *Service, method, path, body string) *httptest.ResponseRecorder {
	return serveAs(s, "", method, path, body)
}

func reportOf(t *testing.T, s *Service, sid string) (proto.WorkState, bool) {
	t.Helper()
	r := s.workReport(context.Background())
	if r == nil {
		t.Fatal("no report")
	}
	for _, w := range r.Sessions {
		if w.SessionID == sid {
			return w.Work, true
		}
	}
	return proto.WorkState{}, false
}

func TestTransientErrors(t *testing.T) {
	for text, want := range map[string]bool{
		"API Error: 503 no eligible upstream account":                                             true,
		"API Error: 529 {\"type\":\"overloaded_error\"}":                                          true,
		"API Error: Connection error.":                                                            true,
		"API Error: Request timed out.":                                                           true,
		"API Error: 429 rate limit exceeded":                                                      true,
		"API Error: Can't reach the API server — check your internet or DNS (ENOTFOUND)":          true,
		"API Error: Can't reach the API server — check your internet or DNS (EAI_AGAIN)":          true,
		"API Error: Can't reach the API server — check your internet or DNS (FailedToOpenSocket)": true,
		"API Error: The API is temporarily unavailable":                                           true,
		"API Error: No response from API (waited 5m)":                                             true,
		"API Error: Unable to connect to API (ECONNREFUSED)":                                      true,
		"API Error: Unable to connect to API: SSL certificate verification failed. See https://docs.claude.com/en/docs/claude-code/network-config": false,
		"Failed to authenticate. API Error: 401 invalid x-api-key":                                                                                 false,
		"OAuth token has expired. Please obtain a new token":                                                                                       false,
		"Invalid API key · Please run /login":                                                                                                      false,
		"There's an issue with the selected model (x). It may not exist":                                                                           false,
	} {
		if got := transientError(text); got != want {
			t.Errorf("transientError(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestTriageRules(t *testing.T) {
	ask := proto.Session{Status: proto.StatusDone, PendingInteractions: 1}
	if _, _, skip := triageRules(ask, time.UTC, ""); !skip {
		t.Fatal("a session asking on its own card is not triaged")
	}
	w, by, _ := triageRules(proto.Session{Status: proto.StatusDone, TurnError: "API Error: 503 upstream"}, time.UTC, "")
	if w == nil || w.On != proto.WaitTransient || by != "rule" {
		t.Fatalf("transient: %+v %s", w, by)
	}
	w, _, _ = triageRules(proto.Session{Status: proto.StatusDone, TurnError: "OAuth token has expired"}, time.UTC, "")
	if w == nil || w.On != proto.WaitUser || len(w.Questions) != 1 {
		t.Fatalf("sign-in: %+v", w)
	}
	w, _, _ = triageRules(proto.Session{Status: proto.StatusDone, Background: []proto.BackgroundTask{{}}}, time.UTC, "")
	if w == nil || w.On != proto.WaitExternal || w.Wait != "" {
		t.Fatalf("background: %+v", w)
	}
	w, _, _ = triageRules(proto.Session{Status: proto.StatusDone, Loop: &proto.LoopInfo{Kind: "wakeup"}}, time.UTC, "")
	if w == nil || w.On != proto.WaitExternal {
		t.Fatalf("loop: %+v", w)
	}
	if w, _, skip := triageRules(proto.Session{Status: proto.StatusDone, LastText: "改好了"}, time.UTC, ""); w != nil || skip {
		t.Fatalf("plain answer goes to the model: %+v", w)
	}
}

// codexFailedTurn is a Codex turn the model service failed, as the hub lists it (#2185): its last words are from before
// the error, which the node reported with Codex's kind.
func codexFailedTurn(id, errText, kind string) proto.Session {
	c := stopped(id, "我先跑一下测试")
	c.Agent, c.TurnError, c.TurnErrorKind = "codex", errText, kind
	return c
}

// The kind the agent's record gives decides over the words: Codex says what failed (#2185).
func TestTriageRulesCodexTurnError(t *testing.T) {
	for _, tc := range []struct {
		text, kind, on string
	}{
		{"Selected model is at capacity. Please try a different model.", proto.TurnErrorTransient, proto.WaitTransient},
		{"unexpected status 503 Service Unavailable: upstream overloaded", proto.TurnErrorTransient, proto.WaitTransient},
		{"stream error: upstream overloaded", proto.TurnErrorUser, proto.WaitUser},
		{"You've hit your usage limit.", proto.TurnErrorUser, proto.WaitUser},
		{"unexpected status 503 Service Unavailable", "", proto.WaitTransient}, // no kind: the words
		{"unexpected status 401 Unauthorized", "", proto.WaitUser},
	} {
		w, by, skip := triageRules(codexFailedTurn("s1", tc.text, tc.kind), time.UTC, "")
		if skip || w == nil || w.Phase != proto.WorkBlocked || w.On != tc.on || by != "rule" {
			t.Errorf("%q (%s): %+v %s", tc.text, tc.kind, w, by)
		}
	}
}

// A Codex turn the model service cut off gets "Continue" like Claude Code's, though its answer is no error (#2185).
func TestCodexTurnErrorIsContinued(t *testing.T) {
	s, f := workService(t)
	f.setSessions("box", codexFailedTurn("s1", "unexpected status 503 Service Unavailable", proto.TurnErrorTransient))
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	if e.WakeAt == nil || e.Work.On != proto.WaitTransient || e.Work.By != "rule" {
		t.Fatalf("entry %+v", e)
	}
	s.workTick(context.Background(), time.Now().Add(31*time.Second))
	if got := f.sentToAgents(); len(got) != 1 || got[0] != "s1: 继续" {
		t.Fatalf("sent %v", got)
	}
}

// A 503 gets "Continue" after the backoff, once; three runs of it and the user is asked.
// Claude Code's own words for a network blip (no "connection" / "timeout" in them) are retried, not a card (#4644).
func TestUnreachableAPIIsContinued(t *testing.T) {
	s, f := workService(t)
	f.setSessions("box", apiErrorTurn("s1", "API Error: Can't reach the API server — check your internet or DNS (EAI_AGAIN)"))
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	if e.WakeAt == nil || e.Work.On != proto.WaitTransient || len(e.Work.Questions) != 0 || len(f.sentToAgents()) != 0 {
		t.Fatalf("entry %+v, sent %v", e, f.sentToAgents())
	}
}

func TestTransientErrorIsContinued(t *testing.T) {
	s, f := workService(t)
	const errText = "API Error: 503 no eligible upstream account"
	f.setSessions("box", apiErrorTurn("s1", errText))
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	if e.WakeAt == nil || e.Work.On != proto.WaitTransient || len(f.sentToAgents()) != 0 {
		t.Fatalf("entry %+v, sent %v", e, f.sentToAgents())
	}
	if d := time.Until(*e.WakeAt); d < 25*time.Second || d > 35*time.Second {
		t.Fatalf("first retry in %v", d)
	}
	if w, ok := reportOf(t, s, "s1"); !ok || w.WakeAt == nil || !w.TurnAt.Equal(turnEnd) {
		t.Fatalf("the hub is told the retry: %+v", w)
	}
	ctx := context.Background()
	s.workTick(ctx, time.Now())
	if len(f.sentToAgents()) != 0 {
		t.Fatal("sent before the backoff")
	}
	s.workTick(ctx, time.Now().Add(31*time.Second))
	s.workTick(ctx, time.Now().Add(32*time.Second))
	if got := f.sentToAgents(); len(got) != 1 || got[0] != "s1: 继续" {
		t.Fatalf("sent %v", got)
	}

	// It errs again after each "Continue" (a new turn each time): the count carries over, and after the cap it is a card.
	for i := 1; ; i++ {
		s.workStarted(ctx, "box", "s1", time.Now())
		c := apiErrorTurn("s1", errText)
		c.UpdatedAt = turnEnd.Add(time.Duration(i) * time.Minute)
		f.setSessions("box", c)
		triage(t, s, "s1")
		if e := entryOf(t, s, "s1"); e.WakeAt == nil {
			break
		}
		s.workTick(ctx, time.Now().Add(time.Hour))
		if i > 5 {
			t.Fatal("never stopped retrying")
		}
	}
	if got := f.sentToAgents(); len(got) != 3 {
		t.Fatalf("继续 sent %d times: %v", len(got), got)
	}
	e = entryOf(t, s, "s1")
	if e.Work.On != proto.WaitUser || e.InteractionID == "" {
		t.Fatalf("after the cap: %+v", e)
	}
	if cards := s.workCards(); len(cards) != 1 || cards[0].Questions[0].Options[0].Label != answerRetry || cards[0].ToolName != workCardTool {
		t.Fatalf("cards %+v", cards)
	}
	if snap := s.snapshot(ctx); len(snap.Interactions) != 1 || snap.Interactions[0].ID != e.InteractionID || snap.Work == nil {
		t.Fatalf("the snapshot carries the card and the work: %+v", snap.Interactions)
	}
}

// Whatever the user did meanwhile wins: the manager sends nothing once the session moved on.
func TestWorkGuardYieldsToTheUser(t *testing.T) {
	for name, change := range map[string]func(*proto.Session){
		"working":       func(c *proto.Session) { c.Status = proto.StatusWorking },
		"new turn":      func(c *proto.Session) { c.UpdatedAt = c.UpdatedAt.Add(time.Minute) },
		"queued input":  func(c *proto.Session) { c.Queued = []string{"先别动"} },
		"seen in herdr": func(c *proto.Session) { c.Status = proto.StatusIdle },
	} {
		t.Run(name, func(t *testing.T) {
			s, f := workService(t)
			f.setSessions("box", apiErrorTurn("s1", "API Error: 503 upstream"))
			triage(t, s, "s1")
			c := apiErrorTurn("s1", "API Error: 503 upstream")
			change(&c)
			f.setSessions("box", c)
			s.workTick(context.Background(), time.Now().Add(time.Minute))
			if got := f.sentToAgents(); len(got) != 0 {
				t.Fatalf("sent %v", got)
			}
			if e := entryOf(t, s, "s1"); e.WakeAt != nil && name != "working" {
				t.Fatalf("wake left: %+v", e)
			}
		})
	}
}

// "I'll run the live tests after the deploy": the manager wakes it after the wait it named, and later ones double.
func TestExternalWaitWakes(t *testing.T) {
	s, f := workService(t)
	f.triage = `{"phase":"blocked","on":"external","line":"等部署跑完再跑线上测试",
		"wait":{"what":"部署跑完","then":"跑线上测试","check_minutes":5}}`
	reply := "已经触发部署，部署完我再跑线上测试。"
	f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: reply})
	f.setSessions("box", stopped("s1", reply))
	triage(t, s, "s1")
	if len(f.triageAsked) != 1 || !strings.Contains(f.triageAsked[0].User, "部署完我再跑线上测试") {
		t.Fatalf("the model was not shown the reply: %+v", f.triageAsked)
	}
	e := entryOf(t, s, "s1")
	if e.WakeAt == nil || e.Work.On != proto.WaitExternal || e.Work.Every != 5 {
		t.Fatalf("entry %+v", e)
	}
	if d := time.Until(*e.WakeAt); d < 4*time.Minute || d > 6*time.Minute {
		t.Fatalf("wake in %v", d)
	}
	s.workTick(context.Background(), time.Now().Add(6*time.Minute))
	got := f.sentToAgents()
	if len(got) != 1 || !strings.Contains(got[0], "部署跑完") || !strings.Contains(got[0], "先停下来问用户") {
		t.Fatalf("sent %v", got)
	}
	if e := entryOf(t, s, "s1"); e.Wakes != 1 || e.WakeAt != nil {
		t.Fatalf("after the wake: %+v", e)
	}

	if wakeAfter(5, 0) != 5*time.Minute || wakeAfter(5, 2) != 20*time.Minute || wakeAfter(5, 5) != time.Hour ||
		wakeAfter(0, 0) != 10*time.Minute || wakeAfter(120, 3) != 2*time.Hour {
		t.Fatal("wakeAfter")
	}
}

// A wait that ends in pushing code is never the manager's to end: the user is asked.
func TestRiskyNextStepIsACard(t *testing.T) {
	for _, reply := range []string{
		`{"phase":"blocked","on":"external","line":"CI 过了就推","wait":{"what":"CI 通过","then":"push 到 beta","check_minutes":5}}`,
		`{"phase":"blocked","on":"external","line":"等审核","wait":{"what":"审核","then":"部署到线上","check_minutes":30}}`,
		`{"phase":"blocked","on":"external","line":"等","wait":{"what":"CI","then":"跑一下","check_minutes":5,"risky":true}}`,
	} {
		s, f := workService(t)
		f.triage = reply
		f.setSessions("box", stopped("s1", "等着"))
		triage(t, s, "s1")
		e := entryOf(t, s, "s1")
		if e.WakeAt != nil || e.Work.On != proto.WaitUser || e.InteractionID == "" {
			t.Fatalf("%s: entry %+v", reply, e)
		}
		s.workTick(context.Background(), time.Now().Add(time.Hour))
		if got := f.sentToAgents(); len(got) != 0 {
			t.Fatalf("%s: sent %v", reply, got)
		}
	}

	s, f := workService(t)
	f.triage = `{"phase":"blocked","on":"external","line":"CI 过了就推","wait":{"what":"CI 通过","then":"push 到 beta","check_minutes":5}}`
	f.setSessions("box", stopped("s1", "CI 过了我就推"))
	triage(t, s, "s1")
	card := s.workCards()[0]
	answerCard(t, s, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: answerHold}})
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("先别做 sent %v", got)
	}

	s, f = workService(t)
	f.triage = `{"phase":"blocked","on":"external","line":"CI 过了就推","wait":{"what":"CI 通过","then":"push 到 beta","check_minutes":5}}`
	f.setSessions("box", stopped("s1", "CI 过了我就推"))
	triage(t, s, "s1")
	card = s.workCards()[0]
	answerCard(t, s, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: answerGo}})
	if got := f.sentToAgents(); len(got) != 1 || !strings.Contains(got[0], answerGo) {
		t.Fatalf("等好了就去做 sent %v", got)
	}
}

// The decision a turn ends on becomes a card; the answer goes back to the agent. Going on without it withdraws it; a
// muted session gets none.
func TestDecisionCard(t *testing.T) {
	s, f := workService(t)
	f.triage = `{"phase":"blocked","on":"user","line":"等你选方案",
		"questions":[{"question":"用哪个方案？","header":"方案","options":[{"label":"用 D4","description":"推荐"},{"label":"用 D2"}]},
		             {"question":"要不要推到 beta？","options":[{"label":"推"},{"label":"先不推"}]}]}`
	f.setSessions("box", stopped("s1", "D2 / D4 选哪个？要不要推到 beta？"))
	triage(t, s, "s1")
	cards := s.workCards()
	if len(cards) != 1 || len(cards[0].Questions) != 2 || cards[0].Questions[0].Options[0].Label != "用 D4" ||
		cards[0].SessionTitle != "修登录" || cards[0].Summary != "等你选方案" || !cards[0].ExpiresAt.Equal(turnEnd.Add(workCardTTL)) {
		t.Fatalf("cards %+v", cards)
	}
	if w, _ := reportOf(t, s, "s1"); w.InteractionID != cards[0].ID || w.Line != "等你选方案" {
		t.Fatalf("report %+v", w)
	}
	answerCard(t, s, cards[0].ID, proto.Resolution{Answers: map[string]string{"用哪个方案？": "用 D4", "要不要推到 beta？": "先不推"}})
	got := f.sentToAgents()
	if len(got) != 1 || !strings.Contains(got[0], "用哪个方案？ → 用 D4") || !strings.Contains(got[0], "先不推") {
		t.Fatalf("sent %v", got)
	}
	if e := entryOf(t, s, "s1"); e.InteractionID != "" || len(s.workCards()) != 0 {
		t.Fatalf("after the answer: %+v", e)
	}
	// Another answer to it (delivered twice) is none of the cards' any more: it goes to the questions, which have none.
	if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: cards[0].ID}); err == nil {
		t.Fatal("a second answer was taken")
	}

	// Another turn asks; the user answers in the terminal instead: the card goes.
	f.setSessions("box", stopped("s1", "x"), stopped("s2", "选哪个？"))
	triage(t, s, "s2")
	if len(s.workCards()) != 1 {
		t.Fatal("no card for s2")
	}
	s.workStarted(context.Background(), "box", "s2", time.Now())
	if len(s.workCards()) != 0 {
		t.Fatal("card not withdrawn when the agent went on")
	}

	f.muted = map[string][]string{"box": {"s3"}}
	f.setSessions("box", stopped("s3", "选哪个？"))
	triage(t, s, "s3")
	if e := entryOf(t, s, "s3"); e.Work.On != proto.WaitUser || e.InteractionID != "" || len(s.workCards()) != 0 {
		t.Fatalf("muted: %+v", e)
	}
}

func TestDoneIsTriaged(t *testing.T) {
	s, f := workService(t)
	f.triage = `{"phase":"done","line":"已合并 PR #12，测试全过","artifacts":[{"kind":"pr","ref":"https://github.com/example/app/pull/12"},{"kind":"weird","ref":"x"}]}`
	f.setSessions("box", stopped("s1", "合好了"))
	triage(t, s, "s1")
	w, ok := reportOf(t, s, "s1")
	if !ok || w.Phase != proto.WorkDone || len(w.Artifacts) != 1 || w.By != "model" {
		t.Fatalf("done: %+v", w)
	}
	// Triaged once per turn.
	triage(t, s, "s1")
	if len(f.triageAsked) != 1 {
		t.Fatalf("asked %d times", len(f.triageAsked))
	}
}

// An agent waiting for another is woken when that one stops.
func TestAgentWaitWakesWhenTheOtherStops(t *testing.T) {
	s, f := workService(t)
	other := proto.Session{SessionID: "abcdef123456", Title: "合 PR", Status: proto.StatusWorking, UpdatedAt: turnEnd}
	f.triage = `{"phase":"blocked","on":"agent","line":"等合 PR 那个做完再 rebase",
		"wait":{"what":"合 PR 做完","then":"rebase","check_minutes":30,"agent":"abcdef12"}}`
	f.setSessions("box", stopped("s1", "等隔壁合完我再 rebase"), other)
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	if e.Work.On != proto.WaitAgent || e.Work.Agent == nil || e.Work.Agent.SessionID != other.SessionID {
		t.Fatalf("entry %+v", e.Work)
	}
	s.workTick(context.Background(), time.Now())
	if len(f.sentToAgents()) != 0 {
		t.Fatal("woken while the other works")
	}
	other.Status, other.LastText = proto.StatusDone, "合好了"
	f.setSessions("box", stopped("s1", "等隔壁合完我再 rebase"), other)
	s.workTick(context.Background(), time.Now())
	if got := f.sentToAgents(); len(got) != 1 || !strings.Contains(got[0], "合好了") {
		t.Fatalf("sent %v", got)
	}
}

func TestTriageWorkSanitizes(t *testing.T) {
	others := map[string]proto.WorkAgent{"abcd1234": {MachineID: "box", SessionID: "abcd1234-x"}}
	now := time.Now()
	if w := triageWork(triageOut{Phase: "maybe"}, others, now, ""); w.Phase != "" {
		t.Fatalf("made-up phase: %+v", w)
	}
	if w := triageWork(triageOut{Phase: "blocked", On: "whoever", Line: "x"}, others, now, ""); w.On != proto.WaitUser {
		t.Fatalf("unknown on: %+v", w)
	}
	var out triageOut
	_ = json.Unmarshal([]byte(`{"phase":"blocked","on":"agent","wait":{"what":"它","agent":"abcd1234"}}`), &out)
	if w := triageWork(out, others, now, ""); w.On != proto.WaitAgent || w.Agent == nil {
		t.Fatalf("known agent: %+v", w)
	}
	_ = json.Unmarshal([]byte(`{"phase":"blocked","on":"agent","wait":{"what":"它","agent":"zzz"}}`), &out)
	if w := triageWork(out, others, now, ""); w.On != proto.WaitExternal || w.Agent != nil {
		t.Fatalf("unknown agent: %+v", w)
	}
	out = triageOut{}
	_ = json.Unmarshal([]byte(`{"phase":"blocked","on":"user","line":"点链接","link":{"url":"javascript:alert(1)"}}`), &out)
	if w := triageWork(out, others, now, ""); w.Link != nil {
		t.Fatalf("bad link kept: %+v", w.Link)
	}
	out = triageOut{}
	_ = json.Unmarshal([]byte(`{"phase":"blocked","on":"user","line":"点链接","link":{"url":"https://example.com/device","minutes":10}}`), &out)
	if w := triageWork(out, others, now, ""); w.Link == nil || w.Link.ExpiresAt == nil || w.Link.ExpiresAt.Sub(now) < 9*time.Minute {
		t.Fatalf("link: %+v", w.Link)
	}
	var qs []proto.Question
	for i := 0; i < 6; i++ {
		qs = append(qs, proto.Question{Question: strings.Repeat("问", i+1), Options: []proto.QuestionOption{{Label: "a"}, {Label: "b"}, {Label: "c"}, {Label: "d"}, {Label: "e"}}})
	}
	qs[0].Options = qs[0].Options[:1]
	got := cardQuestions(qs, "")
	if len(got) != 4 || len(got[1].Options) != 4 || len(got[0].Options) != 2 || got[0].Options[1].Label != answerSkip {
		t.Fatalf("cardQuestions %+v", got)
	}
}

func TestSummarizeWork(t *testing.T) {
	at := turnEnd
	ev := func(kind, sid string, after float64, d map[string]any) db.WorkEvent {
		if d == nil {
			d = map[string]any{}
		}
		if after > 0 {
			d["after"] = after
		}
		return db.WorkEvent{At: at.Add(time.Duration(after) * time.Second), MachineID: "box", SessionID: sid, Kind: kind, Detail: d}
	}
	s := summarizeWork(24, []db.WorkEvent{
		ev("triaged", "s1", 0, map[string]any{"phase": "done", "by": "model"}),
		ev("triaged", "s2", 0, map[string]any{"phase": "blocked", "on": "user", "by": "model"}),
		ev("triaged", "s3", 0, map[string]any{"phase": "", "by": "model"}),
		ev("failed", "s4", 0, nil),
		ev("continued", "s5", 0, nil),
		ev("seen", "s1", 60, map[string]any{"phase": "done"}),
		ev("seen", "s1", 120, map[string]any{"phase": "done"}), // the same turn: no second wait
		ev("seen", "s2", 30, map[string]any{"phase": "blocked", "on": "user"}),
		ev("resumed", "s5", 40, map[string]any{"phase": "blocked", "on": "transient", "by": "hub"}),
		ev("sample", "", 0, map[string]any{"unread": 4.0, "needs_you": 1.0}),
		ev("sample", "", 0, map[string]any{"unread": 2.0}),
	})
	if s.Triaged["done"] != 1 || s.Triaged["blocked/user"] != 1 || s.Triaged["unsure"] != 1 || s.Triaged["failed"] != 1 {
		t.Fatalf("triaged %v", s.Triaged)
	}
	if s.Actions["continued"] != 1 || s.Resumed["hub"] != 1 {
		t.Fatalf("actions %v resumed %v", s.Actions, s.Resumed)
	}
	if s.Unread.Samples != 2 || s.Unread.Avg != 3 || s.Unread.Max != 4 || s.Unread.Last != 2 {
		t.Fatalf("unread %+v", s.Unread)
	}
	if w := s.Waits["done"]; w.N != 1 || w.Median != 60 {
		t.Fatalf("waits %+v", s.Waits)
	}
	if _, ok := s.Waits["blocked/transient"]; ok {
		t.Fatal("a wait the manager ended is no wait for the user")
	}
}

func TestTriageOff(t *testing.T) {
	s, f := workService(t)
	s.work.switches = &TriageSwitches{}
	f.setSessions("box", apiErrorTurn("s1", "API Error: 503 upstream"))
	s.workTurnEnded(context.Background(), "box", "s1")
	if _, ok := s.workEntry([2]string{"box", "s1"}); ok {
		t.Fatal("triaged while off")
	}
	if r := s.workReport(context.Background()); r == nil || r.On || len(r.Sessions) != 0 {
		t.Fatalf("report while off: %+v", r)
	}
}

// Turns that ended while the manager could not hear of them are triaged when it looks.
func TestBackfillTriagesUnread(t *testing.T) {
	s, f := workService(t)
	c := apiErrorTurn("s1", "API Error: 503 upstream")
	c.UpdatedAt = time.Now().Add(-time.Hour)
	old := apiErrorTurn("s2", "API Error: 503 upstream")
	old.UpdatedAt = time.Now().Add(-3 * 24 * time.Hour)
	seen := apiErrorTurn("s3", "API Error: 503 upstream")
	seen.UpdatedAt, seen.State = time.Now().Add(-time.Hour), proto.StateIdle
	f.setSessions("box", c, old, seen)
	s.workTick(context.Background(), time.Now())
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, ok := s.workEntry([2]string{"box", "s1"}); ok {
			break
		}
		if time.Now().After(end) {
			t.Fatal("recent unread turn not triaged")
		}
	}
	for _, sid := range []string{"s2", "s3"} {
		if _, ok := s.workEntry([2]string{"box", sid}); ok {
			t.Fatalf("%s was triaged", sid)
		}
	}
	if len(f.sentToAgents()) != 0 {
		t.Fatal("sent at once")
	}

	// It connects again: the next tick looks again at once, not a minute later.
	later := apiErrorTurn("s6", "API Error: 503 upstream")
	later.UpdatedAt = time.Now().Add(-time.Minute)
	f.setSessions("box", c, old, seen, later)
	s.workTick(context.Background(), time.Now().Add(time.Second))
	time.Sleep(200 * time.Millisecond)
	if _, ok := s.workEntry([2]string{"box", "s6"}); ok {
		t.Fatal("looked again before a minute without a reconnect")
	}
	s.workReconnected()
	s.workTick(context.Background(), time.Now().Add(2*time.Second))
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if _, ok := s.workEntry([2]string{"box", "s6"}); ok {
			break
		}
		if time.Now().After(end) {
			t.Fatal("a turn that ended while it was away was not caught up after it connected")
		}
	}
}

// Off until the user turns it on; turning a part off stops what it had going.
func TestTriageSwitches(t *testing.T) {
	if sw := newTestService(t, newFakeHub()).triageSwitches(context.Background()); sw != (TriageSwitches{}) {
		t.Fatalf("default %+v, want all off", sw)
	}
	on := true
	f := newFakeHub()
	f.hello.Agents.Triage.Enabled = &on
	if sw := newTestService(t, f).triageSwitches(context.Background()); !sw.Enabled || sw.Decide {
		t.Fatalf("hub.json's default %+v", sw)
	}
	s, f := workService(t)
	f.triage = `{"phase":"blocked","on":"user","line":"选哪个","questions":[{"question":"A 还是 B？","options":[{"label":"A"},{"label":"B"}]}]}`
	f.setSessions("box", stopped("s1", "A 还是 B？"), apiErrorTurn("s2", "API Error: 503 upstream"))
	triage(t, s, "s1")
	triage(t, s, "s2")
	if len(s.workCards()) != 1 || entryOf(t, s, "s2").WakeAt == nil {
		t.Fatal("setup")
	}
	ctx := context.Background()
	if err := s.putTriageSwitches(ctx, TriageSwitches{Enabled: true, Wake: true}); err != nil {
		t.Fatal(err)
	}
	if len(s.workCards()) != 0 || entryOf(t, s, "s1").InteractionID != "" {
		t.Fatal("card stayed with decide off")
	}
	if entryOf(t, s, "s2").WakeAt != nil {
		t.Fatal("retry stayed with auto_continue off")
	}
	s.workTick(ctx, time.Now().Add(time.Hour))
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
	if !strings.Contains(f.eventTypes(), "agents.triage") {
		t.Fatalf("the apps were not told: %s", f.eventTypes())
	}
	if snap := f.lastSnapshot(); snap.Work == nil || !snap.Work.On || len(snap.Interactions) != 0 {
		t.Fatalf("the hub was not told: %+v", snap.Work)
	}
	// The routes: GET says them, PUT from an answer-only device is refused.
	w := serve(s, "GET", "/api/v1/agents/triage", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"wake":true`) {
		t.Fatalf("GET %d %s", w.Code, w.Body)
	}
	if w := serveAs(s, "answer", "PUT", "/api/v1/agents/triage", `{"enabled":false}`); w.Code != 403 {
		t.Fatalf("answer-only PUT %d", w.Code)
	}
}

func TestGoldenAgentTriage(t *testing.T) {
	s, f := workService(t)
	f.hello.Owner = "示例用户"
	f.devices = []Device{{ID: "box", Name: "示例电脑", Online: true, Reachable: true}}
	f.triage = `{"phase":"done","line":"做完了"}`
	reply := "修好了，PR：https://github.com/example/app/pull/7，CI 在跑，过了要我合并吗？"
	f.setItems(proto.Item{Kind: proto.KindText, Role: "user", Text: "把登录页的崩溃修好，提 PR"},
		proto.Item{Kind: proto.KindText, Role: "assistant", Text: reply})
	other := proto.Session{SessionID: "abcdef123456", Title: "跑基准", Cwd: "/code/bench", Status: proto.StatusWorking, Activity: "在跑测试"}
	f.setSessions("box", stopped("s1", reply), other)
	ctx := context.Background()
	v := s.newWorkView(ctx)
	c, _ := v.session("box", "s1")
	if _, _, err := s.triageTurnModel(ctx, v, "box", c); err != nil {
		t.Fatal(err)
	}
	if len(f.triageAsked) != 1 {
		t.Fatalf("%d triage calls", len(f.triageAsked))
	}
	call := f.triageAsked[0]
	golden(t, "agent_triage.system", prompts.RenderJSONOnly(call.System)) // as the hub's models.json sends it
	golden(t, "agent_triage.user", regexp.MustCompile(`现在：[^\n]+`).ReplaceAllString(call.User, "现在：YYYY-MM-DD HH:MM Day"))

	golden(t, "agent_wake.timer", renderWake(agentWake{Attempt: 2, Wait: "部署跑完", Then: "跑线上测试"}))
	golden(t, "agent_wake.agent", renderWake(agentWake{Attempt: 1, Wait: "合 PR 那个做完", Then: "rebase", Agent: "合 PR", AgentLine: "合好了"}))
	golden(t, "agent_wake.approved", renderWake(agentWake{Attempt: 1, Wait: "CI 通过", Then: "push 到 beta", Approved: "push 到 beta"}))
	golden(t, "agent_triage_input.approved", prompts.RenderAgentTriageInput(prompts.AgentTriageInput{Now: "2026-01-02 09:00 Fri",
		Agent: "修登录", Machine: "示例电脑", Reply: "CI 还在跑", ApprovedWait: "CI 通过", ApprovedThen: "push 到 beta"}))
	golden(t, "agent_answer", renderAnswer([]agentAnswerItem{{"用哪个方案？", "用 D4"}, {"要不要推到 beta？", "先不推"}}))
}

// workMemStore is a store with session_work and its events in memory, as Postgres keeps them.
type workMemStore struct {
	memStore
	wmu    sync.Mutex
	rows   map[[2]string]db.SessionWork
	events []db.WorkEvent
	// eventLag makes each event insert take this long, as a database round trip does (the service writes them
	// in the background, #4179).
	eventLag time.Duration
}

func (m *workMemStore) PutSessionWork(_ context.Context, w db.SessionWork) error {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	if m.rows == nil {
		m.rows = map[[2]string]db.SessionWork{}
	}
	m.rows[[2]string{w.MachineID, w.SessionID}] = w
	return nil
}

func (m *workMemStore) SessionWorks(_ context.Context, since time.Time) ([]db.SessionWork, error) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	var out []db.SessionWork
	for _, w := range m.rows {
		if w.TurnAt.After(since) || w.WakeAt != nil && w.Work.CI != nil || w.Work.RetiredAt != nil && w.UpdatedAt.After(since) {
			out = append(out, w)
		}
	}
	return out, nil
}

func (m *workMemStore) PruneSessionWork(context.Context, time.Time) error { return nil }

func (m *workMemStore) AddWorkEvent(_ context.Context, e db.WorkEvent) error {
	time.Sleep(m.eventLag)
	m.wmu.Lock()
	defer m.wmu.Unlock()
	m.events = append(m.events, e)
	return nil
}

func (m *workMemStore) WorkEvents(context.Context, time.Time) ([]db.WorkEvent, error) {
	m.wmu.Lock()
	defer m.wmu.Unlock()
	return append([]db.WorkEvent(nil), m.events...), nil
}

// The manager restarts: what it found and the cards it had out come back from the database (a card whose machine is
// not back yet once it is), the switches too; a card whose turn went on while it was away goes.
func TestWorkComesBackAfterARestart(t *testing.T) {
	f := newFakeHub()
	f.sessions["box"] = nil
	st := &workMemStore{eventLag: 20 * time.Millisecond}
	ctx := context.Background()
	s := newServiceOn(t, f, st)
	if err := s.putTriageSwitches(ctx, TriageSwitches{Enabled: true, Decide: true, Wake: true, AutoContinue: true}); err != nil {
		t.Fatal(err)
	}
	f.triage = `{"phase":"blocked","on":"user","line":"选哪个","questions":[{"question":"A 还是 B？","options":[{"label":"A"},{"label":"B"}]}]}`
	recent := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	c1, c2 := stopped("s1", "A 还是 B？"), stopped("s2", "A 还是 B？")
	c1.UpdatedAt, c2.UpdatedAt = recent, recent
	f.setSessions("box", c1, c2)
	triage(t, s, "s1")
	triage(t, s, "s2")
	if len(s.workCards()) != 2 {
		t.Fatal("setup: two cards")
	}
	first := entryOf(t, s, "s1").InteractionID

	// Restarted while the machine is not back; what the old one recorded is in the database (its writes are async).
	s.waitWorkEvents()
	f.devices = []Device{{ID: "box", Name: "box", Online: false, Reachable: false}}
	s = newServiceOn(t, f, st)
	if sw := s.triageSwitches(ctx); !sw.Enabled || !sw.Decide {
		t.Fatalf("switches %+v", sw)
	}
	snap := s.snapshot(ctx)
	if snap.Work == nil || len(snap.Work.Sessions) != 2 || len(snap.Interactions) != 0 {
		t.Fatalf("read back with the machine away: %+v %+v", snap.Work, snap.Interactions)
	}
	if entryOf(t, s, "s1").InteractionID != first {
		t.Fatal("the card was forgotten while its machine was away")
	}
	// The machine is back; s2 went on meanwhile (its turn_started was missed).
	f.devices = nil
	c2.Status = proto.StatusWorking
	f.setSessions("box", c1, c2)
	s.workTick(ctx, time.Now())
	cards := s.workCards()
	if len(cards) != 1 || cards[0].ID != first || cards[0].SessionTitle != "修登录" {
		t.Fatalf("cards %+v", cards)
	}
	if e := entryOf(t, s, "s2"); e.InteractionID != "" {
		t.Fatalf("s2 kept its card: %+v", e)
	}
	answerCard(t, s, first, proto.Resolution{Answers: map[string]string{"A 还是 B？": "A"}})
	if got := f.sentToAgents(); len(got) != 1 || !strings.Contains(got[0], "A 还是 B？ → A") {
		t.Fatalf("sent %v", got)
	}
	s.waitWorkEvents()
	if w := serve(s, "GET", "/api/v1/agents/work/stats?hours=1", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"blocked/user":2`) {
		t.Fatalf("stats %d %s", w.Code, w.Body)
	}
}

// The hub restarted (and the node with it): a red-line card waiting for the user comes back under the same id, whatever
// the session looks like while its node is coming back — looked at (idle), still blocked on its own question, or not
// yet read (no time on it). Only a later turn or the agent working again ends it.
func TestRedLineCardSurvivesAHubRestart(t *testing.T) {
	f := newFakeHub()
	f.sessions["box"] = nil
	st := &workMemStore{}
	ctx := context.Background()
	s := newServiceOn(t, f, st)
	if err := s.putTriageSwitches(ctx, TriageSwitches{Enabled: true, Decide: true}); err != nil {
		t.Fatal(err)
	}
	f.triage = `{"phase":"blocked","on":"user","line":"等你定部署顺序","questions":[{"question":"先部署哪边？","options":[{"label":"先部署后端"},{"label":"先别部署"}]}]}`
	recent := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	c := stopped("s1", "先部署哪边？")
	c.UpdatedAt = recent
	f.setSessions("box", c)
	triage(t, s, "s1")
	id := entryOf(t, s, "s1").InteractionID
	if id == "" {
		t.Fatal("setup: no card")
	}
	s.waitWorkEvents()

	for _, back := range []struct {
		name string
		edit func(*proto.Session)
	}{
		{"looked at", func(c *proto.Session) { c.Status = proto.StatusIdle }},
		{"not read yet", func(c *proto.Session) { c.UpdatedAt = time.Time{} }},
		{"blocked", func(c *proto.Session) { c.Status = proto.StatusBlocked }},
	} {
		after := c
		back.edit(&after)
		f.setSessions("box", after)
		s = newServiceOn(t, f, st) // the hub has no card up: it restarted
		s.workTick(ctx, time.Now())
		if e := entryOf(t, s, "s1"); e.InteractionID != id {
			t.Fatalf("%s: the card was forgotten: %+v", back.name, e)
		}
	}
	// Its node has read the session again: the card is out, the same one, and its answer goes to the agent.
	f.setSessions("box", c)
	s = newServiceOn(t, f, st)
	s.workTick(ctx, time.Now())
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != id {
		t.Fatalf("cards %+v", cards)
	}
	answerCard(t, s, id, proto.Resolution{Answers: map[string]string{"先部署哪边？": "先别部署"}})
	if got := f.sentToAgents(); len(got) != 1 || !strings.Contains(got[0], "先别部署") {
		t.Fatalf("sent %v", got)
	}

	// A later turn ends a card read back after a restart.
	f.triage = `{"phase":"blocked","on":"user","line":"再问一次","questions":[{"question":"先部署哪边？","options":[{"label":"先部署后端"},{"label":"先别部署"}]}]}`
	c.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	f.setSessions("box", c)
	triage(t, s, "s1")
	second := entryOf(t, s, "s1").InteractionID
	if second == "" || second == id {
		t.Fatalf("setup: second card %q", second)
	}
	s.waitWorkEvents()
	later := c
	later.UpdatedAt = c.UpdatedAt.Add(time.Minute)
	f.setSessions("box", later)
	s = newServiceOn(t, f, st)
	s.workTick(ctx, time.Now())
	if e := entryOf(t, s, "s1"); e.InteractionID != "" {
		t.Fatalf("a card outlived its turn: %+v", e)
	}
}

// A pass that cannot read a machine's sessions decides nothing about them: a wake due then goes a minute later.
func TestWakeWaitsWhenTheHubCannotSay(t *testing.T) {
	s, f := workService(t)
	f.setSessions("box", apiErrorTurn("s1", "API Error: 503 upstream"))
	triage(t, s, "s1")
	f.mu.Lock()
	f.devices = []Device{{ID: "box", Name: "box", Online: true, Reachable: true}}
	delete(f.sessions, "box") // sessions.list on it fails
	f.mu.Unlock()
	now := time.Now().Add(time.Minute)
	s.workTick(context.Background(), now)
	e := entryOf(t, s, "s1")
	if e.WakeAt == nil || !e.WakeAt.After(now) || len(f.sentToAgents()) != 0 {
		t.Fatalf("entry %+v, sent %v", e, f.sentToAgents())
	}
}

// The manager restarted: an answer the hub kept while it was away comes before it reported anything and still finds
// its card (the work is read back first); one for nothing it has is final (gone). A card the hub still has up stays
// up as it is, whatever the session looks like, as when the triage was the hub's.
func TestAnswerAfterARestart(t *testing.T) {
	f := newFakeHub()
	f.sessions["box"] = nil
	st := &workMemStore{}
	ctx := context.Background()
	s := newServiceOn(t, f, st)
	if err := s.putTriageSwitches(ctx, TriageSwitches{Enabled: true, Decide: true}); err != nil {
		t.Fatal(err)
	}
	f.triage = `{"phase":"blocked","on":"user","line":"选哪个","questions":[{"question":"A 还是 B？","options":[{"label":"A"},{"label":"B"}]}]}`
	recent := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	c1, c2 := stopped("s1", "A 还是 B？"), stopped("s2", "A 还是 B？")
	c1.UpdatedAt, c2.UpdatedAt = recent, recent
	f.setSessions("box", c1, c2)
	triage(t, s, "s1")
	triage(t, s, "s2")
	one, two := entryOf(t, s, "s1").InteractionID, entryOf(t, s, "s2").InteractionID

	// The hub still has s2's card up; s2 was looked at in herdr meanwhile (master kept such a card).
	f.hubCards = []proto.Interaction{s.workCards()[0], s.workCards()[1]}
	c2.Status = proto.StatusIdle
	f.setSessions("box", c1, c2)
	s = newServiceOn(t, f, st)
	answerCard(t, s, one, proto.Resolution{Answers: map[string]string{"A 还是 B？": "A"}})
	if got := f.sentToAgents(); len(got) != 1 || !strings.Contains(got[0], "A 还是 B？ → A") {
		t.Fatalf("sent %v", got)
	}
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != two {
		t.Fatalf("the card the hub has up went: %+v", cards)
	}
	raw, _ := json.Marshal(proto.Resolution{Answers: map[string]string{"A 还是 B？": "A"}})
	_, err := s.Action(ctx, Action{Op: "answer", InteractionID: one, Answer: raw})
	if re := (*rpc.Error)(nil); !errors.As(err, &re) || re.Code != CodeGone {
		t.Fatalf("a second answer: %v", err)
	}
	if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: "ix_nothing"}); !errors.As(err, new(*rpc.Error)) {
		t.Fatalf("an answer to no question: %v", err)
	}
}

// The agent cannot be sent the answer (its machine just went away): the card goes up again, under a new id.
func TestAnswerThatDidNotGoPutsTheCardBack(t *testing.T) {
	s, f := workService(t)
	f.triage = `{"phase":"blocked","on":"user","line":"选哪个","questions":[{"question":"A 还是 B？","options":[{"label":"A"},{"label":"B"}]}]}`
	f.setSessions("box", stopped("s1", "A 还是 B？"))
	triage(t, s, "s1")
	first := s.workCards()[0].ID
	f.failSend = errors.New("box: offline")
	answerCard(t, s, first, proto.Resolution{Answers: map[string]string{"A 还是 B？": "A"}})
	cards := s.workCards()
	if len(cards) != 1 || cards[0].ID == first || entryOf(t, s, "s1").InteractionID != cards[0].ID {
		t.Fatalf("cards %+v entry %+v", cards, entryOf(t, s, "s1"))
	}
	// The old card is gone while its replacement is up.
	raw, _ := json.Marshal(proto.Resolution{Answers: map[string]string{"A 还是 B？": "A"}})
	if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: first, Answer: raw}); err == nil {
		t.Fatal("the old card could still be answered")
	} else if re := (*rpc.Error)(nil); !errors.As(err, &re) || re.Code != CodeGone {
		t.Fatalf("the old card: %v", err)
	}
	f.failSend = nil
	answerCard(t, s, cards[0].ID, proto.Resolution{Answers: map[string]string{"A 还是 B？": "A"}})
	if got := f.sentToAgents(); len(got) != 1 || len(s.workCards()) != 0 {
		t.Fatalf("sent %v, cards %+v", got, s.workCards())
	}
}

// A repeated callback while the answer is still being sent (a redelivered webhook, a second tap, phone and watch
// together) is answered OK and the first instruction reaches the agent once, not twice; a different answer racing it
// is ignored.
func TestWorkCardAnswerDeliveredOnce(t *testing.T) {
	s, f := workService(t)
	f.triage = `{"phase":"blocked","on":"user","line":"选哪个","questions":[{"question":"A 还是 B？","options":[{"label":"A"},{"label":"B"}]}]}`
	f.setSessions("box", stopped("s1", "A 还是 B？"))
	triage(t, s, "s1")
	card := s.workCards()[0].ID

	inside := make(chan struct{})
	release := make(chan struct{})
	defer func() { // let the in-flight goroutine return when the test ends early
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f.onSend = func(f *fakeHub, _ map[string]any) {
		close(inside)
		<-release
	}
	rawA, _ := json.Marshal(proto.Resolution{Answers: map[string]string{"A 还是 B？": "A"}})
	rawB, _ := json.Marshal(proto.Resolution{Answers: map[string]string{"A 还是 B？": "B"}})
	var firstErr error
	done := make(chan struct{})
	go func() {
		_, firstErr = s.Action(context.Background(), Action{Op: "answer", InteractionID: card, Answer: rawA})
		close(done)
	}()
	<-inside
	// The same callback again (with a different answer) while the first send is in flight. Without the claim it
	// queues on the fake's lock and sends a second instruction once the first returns.
	type actionOut struct {
		r   ActionResult
		err error
	}
	again := make(chan actionOut, 1)
	go func() {
		r, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: card, Answer: rawB})
		again <- actionOut{r, err}
	}()
	select {
	case a := <-again:
		if a.err != nil || !a.r.OK {
			t.Fatalf("the repeated callback: %+v %v", a.r, a.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the repeated callback blocked behind the first send instead of being answered at once")
	}
	close(release)
	<-done
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	if got := f.sentToAgents(); len(got) != 1 || !strings.Contains(got[0], "A 还是 B？ → A") || strings.Contains(got[0], "→ B") {
		t.Fatalf("sent %v", got)
	}
	s.work.mu.Lock()
	left := s.work.answering[card]
	s.work.mu.Unlock()
	if left {
		t.Fatal("the answering mark was left")
	}
	// A repeat after the delivery finished is gone, as before.
	if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: card, Answer: rawA}); err == nil {
		t.Fatal("a later repeat was accepted")
	} else if re := (*rpc.Error)(nil); !errors.As(err, &re) || re.Code != CodeGone {
		t.Fatalf("a later repeat: %v", err)
	}
}

// While an answer is being sent, a tick can find the card "not out" and put it back under the same id: once the
// delivery finished, the turn's work no longer names the card, so the resurrected card must go too. Otherwise a later
// tap on it finds and sends the same instruction again.
func TestDeliveredCardResurrectedByTickGoesAfterSend(t *testing.T) {
	s, f := workService(t)
	f.triage = `{"phase":"blocked","on":"user","line":"选哪个","questions":[{"question":"A 还是 B？","options":[{"label":"A"},{"label":"B"}]}]}`
	f.setSessions("box", stopped("s1", "A 还是 B？"))
	triage(t, s, "s1")
	card := s.workCards()[0].ID
	v := s.newWorkView(context.Background()) // the session view from before the send blocks it
	v.machines()                             // read the device and session before the fake's lock is held by the send
	v.sessions("box")

	inside := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f.onSend = func(f *fakeHub, _ map[string]any) {
		close(inside)
		<-release
	}
	raw, _ := json.Marshal(proto.Resolution{Answers: map[string]string{"A 还是 B？": "A"}})
	finished := make(chan struct{})
	go func() {
		_, _ = s.Action(context.Background(), Action{Op: "answer", InteractionID: card, Answer: raw})
		close(finished)
	}()
	<-inside
	// A tick while the send is in flight (the delay keeps its snapshot publish off the fake's held lock).
	workPublishDelay = time.Hour
	s.cardTick(context.Background(), v, [2]string{"box", "s1"}, turnEnd.Add(time.Minute))
	workPublishDelay = 0
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != card {
		t.Fatalf("the tick did not put the card back: %+v", cards)
	}
	close(release)
	<-finished
	if cards := s.workCards(); len(cards) != 0 {
		t.Fatalf("the resurrected card stayed after the delivery: %+v", cards)
	}
	if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: card, Answer: raw}); err == nil {
		t.Fatal("the resurrected card could still be answered")
	} else if re := (*rpc.Error)(nil); !errors.As(err, &re) || re.Code != CodeGone {
		t.Fatalf("the resurrected card: %v", err)
	}
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("sent %v", got)
	}
}

// Each wake looks at its session right before it is sent, not at what the pass read: the user went on with the
// second while the first was being sent.
func TestWakeLooksRightBefore(t *testing.T) {
	s, f := workService(t)
	f.setSessions("box", apiErrorTurn("s1", "API Error: 503 upstream"), apiErrorTurn("s2", "API Error: 503 upstream"))
	triage(t, s, "s1")
	triage(t, s, "s2")
	f.onSend = func(f *fakeHub, p map[string]any) {
		for i := range f.sessions["box"] {
			f.sessions["box"][i].Status = proto.StatusWorking // the user typed into the other one meanwhile
		}
	}
	s.workTick(context.Background(), time.Now().Add(time.Minute))
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("sent %v", got)
	}
}

// The user's "Do it once it's ready" holds while the agent keeps waiting: the next turn, still waiting (asked as the user's again,
// in other words), is woken instead of carded again, and the wake says the step is approved (#3824). The user's own
// message ends it.
func TestApprovedWaitIsNotAskedAgain(t *testing.T) {
	s, f := workService(t)
	f.triage = `{"phase":"blocked","on":"external","line":"CI 过了就推","wait":{"what":"CI 通过","then":"push 到 beta","check_minutes":5}}`
	f.setSessions("box", stopped("s1", "CI 过了我就推"))
	triage(t, s, "s1")
	card := s.workCards()[0]
	answerCard(t, s, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: answerGo}})
	s.workStarted(context.Background(), "box", "s1", time.Now()) // the answer set it going

	// It checked, CI still runs; the model makes the push the user's, in its own words.
	f.triage = `{"phase":"blocked","on":"user","line":"CI 还在跑，过了再推","wait":{"what":"CI 跑完","then":"把分支 push 到 beta","check_minutes":5,"risky":true,"approved":true},
		"questions":[{"question":"CI 过了要推到 beta 吗？","options":[{"label":"推"},{"label":"先不推"}]}]}`
	next := stopped("s1", "CI 还在跑，过了我就推到 beta")
	next.UpdatedAt = turnEnd.Add(10 * time.Minute)
	f.setSessions("box", next)
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	if len(s.workCards()) != 0 || e.InteractionID != "" || e.WakeAt == nil || e.Work.On != proto.WaitExternal {
		t.Fatalf("asked again: %+v, cards %d", e, len(s.workCards()))
	}
	if !strings.Contains(f.triageAsked[len(f.triageAsked)-1].User, "用户已经同意") {
		t.Fatal("the triage was not told of the approval")
	}
	before := len(f.sentToAgents())
	s.workTick(context.Background(), time.Now().Add(6*time.Minute))
	got := f.sentToAgents()
	if len(got) != before+1 || !strings.Contains(got[len(got)-1], "用户已经同意") {
		t.Fatalf("wake %v", got[before:])
	}

	// A riskier step than the one approved is asked, even when the model calls it the same.
	s.workStarted(context.Background(), "box", "s1", time.Now())
	f.triage = `{"phase":"blocked","on":"external","line":"等 CI","wait":{"what":"CI 跑完","then":"push 并部署到线上","check_minutes":5,"approved":true}}`
	next.UpdatedAt = turnEnd.Add(20 * time.Minute)
	f.setSessions("box", next)
	triage(t, s, "s1")
	if e := entryOf(t, s, "s1"); e.InteractionID == "" || e.WakeAt != nil {
		t.Fatalf("a riskier step was not asked: %+v", e)
	}

	// Same words, another target: the model says it is not the approved step, and the user is asked.
	s3, f3 := workService(t)
	f3.triage = `{"phase":"blocked","on":"external","line":"CI 过了就推","wait":{"what":"CI 通过","then":"push 到 beta","check_minutes":5}}`
	f3.setSessions("box", stopped("s1", "CI 过了我就推"))
	triage(t, s3, "s1")
	card = s3.workCards()[0]
	answerCard(t, s3, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: answerGo}})
	s3.workStarted(context.Background(), "box", "s1", time.Now())
	f3.triage = `{"phase":"blocked","on":"user","line":"CI 过了 push 到 master","wait":{"what":"CI 通过","then":"push 到 master","check_minutes":5,"risky":true}}`
	next = stopped("s1", "CI 过了我就 push 到 master")
	next.UpdatedAt = turnEnd.Add(10 * time.Minute)
	f3.setSessions("box", next)
	triage(t, s3, "s1")
	if e := entryOf(t, s3, "s1"); e.InteractionID == "" || e.WakeAt != nil {
		t.Fatalf("another target rode on the approval: %+v", e)
	}
	// "Don't do it yet" on that card takes the earlier yes back.
	card = s3.workCards()[0]
	answerCard(t, s3, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: answerHold}})
	s3.work.mu.Lock()
	_, kept := s3.work.approved[[2]string{"box", "s1"}]
	s3.work.mu.Unlock()
	if kept {
		t.Fatal("the approval outlived 先别做")
	}

	// The user writes to it themselves: their earlier yes is gone.
	s2, f2 := workService(t)
	f2.triage = `{"phase":"blocked","on":"external","line":"CI 过了就推","wait":{"what":"CI 通过","then":"push 到 beta","check_minutes":5}}`
	f2.setSessions("box", stopped("s1", "CI 过了我就推"))
	triage(t, s2, "s1")
	card = s2.workCards()[0]
	answerCard(t, s2, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: answerGo}})
	s2.workStarted(context.Background(), "box", "s1", time.Now().Add(3*time.Minute)) // not the manager's send
	next = stopped("s1", "CI 过了我就推")
	next.UpdatedAt = turnEnd.Add(10 * time.Minute)
	f2.setSessions("box", next)
	triage(t, s2, "s1")
	if e := entryOf(t, s2, "s1"); e.InteractionID == "" {
		t.Fatalf("approval outlived the user's own message: %+v", e)
	}
}
