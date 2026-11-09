package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Subscription usage limits in the Agents business (docs/assistant/agent-work.md "Usage limits", #1710): the turn waits
// for the reset; the CLI goes on by itself or the manager continues it; no push or receipt says it ended meanwhile.

const limitText = "You've hit your session limit · resets 4:20pm (UTC)"

// limitTurn is stopped on a subscription's usage limit, as the lists have it once decorated: its reset known, and
// whether the CLI said it goes on by itself.
func limitTurn(id string, reset time.Time, resumes bool) proto.Session {
	c := apiErrorTurn(id, limitText)
	c.TurnErrorKind, c.TurnErrorResetsAt, c.TurnErrorResumes = proto.TurnErrorUsageLimit, &reset, resumes
	return c
}

func workEvents(s *Service, kind string) int {
	s.work.mu.Lock()
	defer s.work.mu.Unlock()
	n := 0
	for _, e := range s.work.testEvents {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// With its reset known a usage limit is an external wait for it, its line saying when; without, the user's card. It
// is never transient, whatever its words; a gateway's 429 with no subscription limit behind it is as before.
func TestUsageLimitTriage(t *testing.T) {
	now := time.Now()
	reset := now.Add(2 * time.Hour).Truncate(time.Minute)
	for _, tc := range []struct {
		resumes bool
		want    string
	}{{true, "自己接着做"}, {false, "重置"}} {
		w, by, skip := triageRules(limitTurn("s1", reset, tc.resumes), time.UTC, "")
		if skip || by != "rule" || w == nil || w.Phase != proto.WorkBlocked || w.On != proto.WaitExternal || w.ResetsAt == nil ||
			!w.ResetsAt.Equal(reset) || w.Wait != "" || len(w.Questions) != 0 || !managerWaits(w) ||
			!strings.Contains(w.Line, limitClock(reset, time.Now(), time.UTC)) || !strings.Contains(w.Line, tc.want) {
			t.Errorf("resumes %v: %+v %s", tc.resumes, w, by)
		}
	}
	if w, _, _ := triageRules(limitTurn("s1", reset, true), time.UTC, i18n.En); !strings.Contains(w.Line, "by itself at "+limitClock(reset, time.Now(), time.UTC)) {
		t.Errorf("in English: %q", w.Line)
	}

	unknown := limitTurn("s1", reset, false)
	unknown.TurnErrorResetsAt = nil
	w, by, _ := triageRules(unknown, time.UTC, "")
	if w == nil || by != "rule" || w.On != proto.WaitUser || w.ResetsAt != nil || len(w.Questions) != 1 ||
		w.Questions[0].Header != workT("", "header_limit") || !strings.Contains(w.Questions[0].Question, "用量额度") ||
		w.Questions[0].Options[0].Label != answerRetry || w.Questions[0].Options[1].Label != answerSkip {
		t.Fatalf("no reset known: %+v", w)
	}
	// No reset known, but its CLI said it goes on by itself: no card (its Retry would type into it), no push.
	unknown.TurnErrorResumes = true
	if w, _, _ := triageRules(unknown, time.UTC, ""); w == nil || w.On != proto.WaitExternal || len(w.Questions) != 0 || w.ResetsAt != nil ||
		w.Wait != "" || !managerWaits(w) || !limitTakenUp(unknown, time.Now()) {
		t.Fatalf("no reset known, goes on by itself: %+v", w)
	}

	c := limitTurn("s1", reset, false)
	c.TurnError = "API Error: 429 rate limit exceeded"
	if turnErrorTransient(c) {
		t.Fatal("a usage limit is never transient")
	}
	gateway := apiErrorTurn("s2", "API Error: Request rejected (429) · upstream quota reached")
	decorateLimit(&gateway)
	if w, _, _ := triageRules(gateway, time.UTC, ""); gateway.TurnErrorKind != "" || w == nil || w.On != proto.WaitTransient || w.ResetsAt != nil {
		t.Fatalf("a gateway's 429: %q %+v", gateway.TurnErrorKind, w)
	}

	// The time of day when the reset is today, the month and day as well when it is another day (tomorrow morning too).
	at := time.Date(2026, 3, 4, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		reset time.Time
		want  string
	}{{at.Add(3 * time.Hour), "13:00"}, {at.Add(-time.Minute), "09:59"}, {at.Add(3 * 24 * time.Hour), "03-07 10:00"},
		{at.Add(20 * time.Hour), "03-05 06:00"}} {
		if got := limitClock(tc.reset, at, time.UTC); got != tc.want {
			t.Errorf("limitClock(%v) = %q, want %q", tc.reset, got, tc.want)
		}
	}
	if got := limitClock(at.Add(3*time.Hour), at, time.FixedZone("UTC+8", 8*3600)); got != "21:00" { // 18:00 → 21:00 there
		t.Errorf("in the user's time zone: %q", got)
	}
}

// Its CLI not going on by itself, the manager continues it a minute or so after the reset, with the prompt for it,
// once; under auto_continue, not wake.
func TestUsageLimitIsContinuedAfterTheReset(t *testing.T) {
	s, f := workService(t)
	s.work.switches.Wake = false // a usage limit's continue is auto_continue's
	reset := time.Now().Add(time.Hour)
	f.setSessions("box", limitTurn("s1", reset, false))
	triage(t, s, "s1")
	e := entryOf(t, s, "s1")
	if e.WakeAt == nil || e.Work.On != proto.WaitExternal || e.Work.ResetsAt == nil || e.Work.Max != maxLimitContinues ||
		e.Work.Attempts != 0 || e.InteractionID != "" || len(e.Work.Questions) != 0 {
		t.Fatalf("entry %+v", e)
	}
	if d := e.WakeAt.Sub(reset); d < limitContinueAfter || d >= limitContinueAfter+limitContinueSpread {
		t.Fatalf("continued %v after the reset", d)
	}
	ctx := context.Background()
	s.workTick(ctx, reset.Add(-time.Minute))
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("sent before the reset: %v", got)
	}
	s.workTick(ctx, reset.Add(2*time.Minute))
	if got := f.sentToAgents(); len(got) != 1 || got[0] != "s1: "+prompts.MessageSourceText(prompts.RenderAgentLimitContinue(), &proto.MessageSource{Kind: proto.SourceFollowup}) {
		t.Fatalf("sent %v", got)
	}
	if e := entryOf(t, s, "s1"); e.Retries != 1 || e.WakeAt != nil {
		t.Fatalf("after the continue: %+v", e)
	}
	if workEvents(s, "limit_continued") != 1 {
		t.Fatal("no limit_continued event")
	}
	s.workTick(ctx, reset.Add(time.Hour))
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("sent again: %v", got)
	}
}

// The CLI that said it goes on by itself is left alone: no wake (typing would cancel its continue), no card; with
// auto_continue off nothing is sent either, and the line stays. Turning auto_continue off drops a continue already set;
// turning wake off does not.
func TestUsageLimitLeftAlone(t *testing.T) {
	ctx := context.Background()
	reset := time.Now().Add(time.Hour)

	s, f := workService(t)
	f.setSessions("box", limitTurn("s1", reset, true))
	triage(t, s, "s1")
	if e := entryOf(t, s, "s1"); e.WakeAt != nil || e.InteractionID != "" || e.Work.ResetsAt == nil || !strings.Contains(e.Work.Line, "自己接着做") ||
		!managerWaits(workOf("", limitTurn("s1", reset, true), &e.Work, nil)) {
		t.Fatalf("goes on by itself: %+v", e)
	}
	s.workTick(ctx, reset.Add(time.Hour))
	if got := f.sentToAgents(); len(got) != 0 || len(s.workCards()) != 0 {
		t.Fatalf("sent %v, cards %d", got, len(s.workCards()))
	}

	s, f = workService(t)
	s.work.switches.AutoContinue = false
	f.setSessions("box", limitTurn("s1", reset, false))
	triage(t, s, "s1")
	// Nothing continues it: the lists keep it for the user (a named wait), not as one that ends by itself.
	if e := entryOf(t, s, "s1"); e.WakeAt != nil || e.InteractionID != "" || !strings.Contains(e.Work.Line, "重置") ||
		managerWaits(workOf("", limitTurn("s1", reset, false), &e.Work, nil)) {
		t.Fatalf("auto_continue off: %+v", e)
	}

	s, f = workService(t)
	f.setSessions("box", limitTurn("s1", reset, false))
	triage(t, s, "s1")
	if err := s.putTriageSwitches(ctx, TriageSwitches{Enabled: true, AutoContinue: true, Decide: true}); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, s, "s1"); e.WakeAt == nil || !managerWaits(workOf("", limitTurn("s1", reset, false), &e.Work, nil)) {
		t.Fatal("wake off dropped a usage limit's continue")
	}
	if err := s.putTriageSwitches(ctx, TriageSwitches{Enabled: true, Wake: true, Decide: true}); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, s, "s1"); e.WakeAt != nil || managerWaits(workOf("", limitTurn("s1", reset, false), &e.Work, nil)) {
		t.Fatal("auto_continue off kept the continue, or the lists still say it ends by itself")
	}
	s.workTick(ctx, reset.Add(time.Hour))
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("sent %v", got)
	}
}

// Each continue the limit refuses at once (a reset read too early) counts; after maxLimitContinues the user is asked,
// with the answers the cards already act on. A turn that did work before the limit stopped it again starts the count
// again.
func TestUsageLimitCapIsACard(t *testing.T) {
	s, f := workService(t)
	ctx := context.Background()
	reset := time.Now().Add(-time.Minute) // read too early: each continue is refused again
	turn := func(i int, afterWork bool) db.SessionWork {
		t.Helper()
		c := limitTurn("s1", reset, false)
		c.UpdatedAt, c.TurnErrorAfterWork = turnEnd.Add(time.Duration(i)*time.Minute), afterWork
		f.setSessions("box", c)
		triage(t, s, "s1")
		return entryOf(t, s, "s1")
	}
	// continued: turn i ends on the limit, with count before the manager continues it.
	continued := func(i int, afterWork bool, count int) {
		t.Helper()
		if e := turn(i, afterWork); e.WakeAt == nil || e.Retries != count || e.Work.Attempts != count {
			t.Fatalf("turn %d: %+v", i, e)
		}
		s.workTick(ctx, time.Now().Add(5*time.Minute))
		s.workStarted(ctx, "box", "s1", time.Now())
	}
	continued(1, false, 0)
	continued(2, false, 1)
	continued(3, true, 0) // it did some work before the limit stopped it again: counted afresh
	continued(4, false, 1)
	continued(5, false, 2)
	if got := f.sentToAgents(); len(got) != 5 {
		t.Fatalf("continued %d times: %v", len(got), got)
	}
	e := turn(6, false)
	if e.Work.On != proto.WaitUser || e.InteractionID == "" || e.Retries != maxLimitContinues || e.Work.ResetsAt == nil {
		t.Fatalf("after the cap: %+v", e)
	}
	cards := s.workCards()
	if len(cards) != 1 || cards[0].Questions[0].Header != workT("", "header_limit") || cards[0].Questions[0].Options[0].Label != answerRetry ||
		!strings.Contains(cards[0].Questions[0].Question, "3 次") || !strings.Contains(cards[0].Questions[0].Question, limitText) {
		t.Fatalf("cards %+v", cards)
	}
	// Another refused turn while the card is up (a background task's report woke it): still the user's.
	s.workStarted(ctx, "box", "s1", time.Now())
	if e := turn(7, false); e.Work.On != proto.WaitUser || e.WakeAt != nil || e.InteractionID == "" {
		t.Fatalf("the count was lost with the card: %+v", e)
	}
	// "Try again" sends the continue and counts afresh, as for a transient error.
	card := s.workCards()[0]
	answerCard(t, s, card.ID, proto.Resolution{Answers: map[string]string{card.Questions[0].Question: answerRetry}})
	if got := f.sentToAgents(); got[len(got)-1] != "s1: "+continueText {
		t.Fatalf("try again sent %v", got[len(got)-1])
	}
	if e := entryOf(t, s, "s1"); e.Retries != 0 {
		t.Fatalf("after try again: %+v", e)
	}
}

// A usage limit's count is its own: an ordinary wait's never carries into it, nor the other way round.
func TestUsageLimitCountIsItsOwn(t *testing.T) {
	reset := time.Now()
	limit := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, ResetsAt: &reset}
	wait := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, Wait: "CI 跑完"}
	next := reset.Add(5 * time.Hour)
	later := proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitExternal, ResetsAt: &next}
	refused := proto.Session{}
	for _, tc := range []struct {
		name          string
		last, now     proto.WorkState
		c             proto.Session
		retries, wake int
	}{
		{"limit after limit", limit, limit, refused, 2, 0},
		{"limit after a productive turn", limit, limit, proto.Session{TurnErrorAfterWork: true}, 0, 0},
		{"limit with a later reset", limit, later, refused, 0, 0},
		{"limit after a wait", wait, limit, refused, 0, 0},
		{"wait after a limit", limit, wait, refused, 0, 0},
		{"wait after a wait", wait, wait, refused, 2, 3},
	} {
		e := db.SessionWork{Work: tc.now}
		carryCounts(&e, db.SessionWork{Work: tc.last, Retries: 2, Wakes: 3}, tc.c)
		if e.Retries != tc.retries || e.Wakes != tc.wake {
			t.Errorf("%s: retries %d wakes %d", tc.name, e.Retries, e.Wakes)
		}
	}
}

// Right before the continue the session is read again: what the user did wins, a CLI that now goes on by itself is
// left to it, and a reset that moved later moves the continue.
func TestUsageLimitContinueLooksRightBefore(t *testing.T) {
	reset := time.Now().Add(time.Hour)
	for name, change := range map[string]func(*proto.Session){
		"working":      func(c *proto.Session) { c.Status = proto.StatusWorking },
		"new turn":     func(c *proto.Session) { c.UpdatedAt = c.UpdatedAt.Add(time.Minute) },
		"queued input": func(c *proto.Session) { c.Queued = []string{"先别动"} },
		"asking":       func(c *proto.Session) { c.PendingInteractions = 1 },
		"herdr":        func(c *proto.Session) { c.Status = proto.StatusIdle },
		"resumes":      func(c *proto.Session) { c.TurnErrorResumes = true },
	} {
		t.Run(name, func(t *testing.T) {
			s, f := workService(t)
			f.setSessions("box", limitTurn("s1", reset, false))
			triage(t, s, "s1")
			c := limitTurn("s1", reset, false)
			change(&c)
			f.setSessions("box", c)
			s.workTick(context.Background(), reset.Add(5*time.Minute))
			if got := f.sentToAgents(); len(got) != 0 {
				t.Fatalf("sent %v", got)
			}
			if e := entryOf(t, s, "s1"); e.WakeAt != nil && name != "working" {
				t.Fatalf("continue left: %+v", e)
			}
		})
	}

	s, f := workService(t)
	f.setSessions("box", limitTurn("s1", reset, false))
	triage(t, s, "s1")
	later := reset.Add(3 * time.Hour)
	f.setSessions("box", limitTurn("s1", later, false))
	s.workTick(context.Background(), reset.Add(5*time.Minute))
	e := entryOf(t, s, "s1")
	if len(f.sentToAgents()) != 0 || e.WakeAt == nil || e.WakeAt.Before(later) || !e.Work.ResetsAt.Equal(later) || e.Retries != 0 {
		t.Fatalf("a later reset: %+v, sent %v", e, f.sentToAgents())
	}
	s.workTick(context.Background(), later.Add(5*time.Minute))
	if got := f.sentToAgents(); len(got) != 1 {
		t.Fatalf("sent %v at the later reset", got)
	}
}

// No "Completed" (nor "Interrupted") push while a turn waits for its usage limit to reset and goes on then; one nothing
// takes up (its reset not known, or no continue coming) is pushed as a usage limit, never as a model service failure;
// a sign-in that is gone is pushed as cut off, and reads as interrupted with its code.
func TestUsageLimitPush(t *testing.T) {
	ft := newFleetTestNoStore(t)
	fastDone(ft)
	reset := time.Now().Add(time.Hour)
	run := func(id string, done proto.Session) {
		work := proto.Session{SessionID: id, Title: "修登录", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking, PaneID: "p-" + id,
			UpdatedAt: time.Now()}
		done.SessionID, done.Title, done.Cwd, done.Agent, done.PaneID, done.Status, done.UpdatedAt =
			id, work.Title, work.Cwd, work.Agent, work.PaneID, proto.StatusDone, time.Now()
		ft.sessions("mac", work)
		ft.sessions("mac", done)
	}
	run("s1", proto.Session{LastText: limitText, TurnError: limitText, TurnErrorKind: proto.TurnErrorUsageLimit, TurnErrorResetsAt: &reset,
		TurnErrorResumes: true})
	if sess := listed(t, ft.s, "mac", "s1"); !usageLimitWaiting(sess, time.Now()) || sess.TurnErrorCode != proto.TurnErrorRateLimited {
		t.Fatalf("listed: %+v", sess)
	}
	if got := ft.pushesOf(assistantwire.NotifyDone, 1, 400*time.Millisecond); len(got) != 0 {
		t.Fatalf("pushed while it waits for the reset: %+v", got)
	}

	limitPush := func(e string) string { return i18n.T(i18n.ZhHans, "push.usage_limit", i18n.Args{"error": e}) }
	for name, done := range map[string]proto.Session{
		"its reset not known": {LastText: "You've hit your session limit · resets 4:20pm"},
		// The switches off and a CLI that does not go on by itself: nothing continues it, the user is told.
		"nothing continues it": {LastText: limitText, TurnError: limitText, TurnErrorKind: proto.TurnErrorUsageLimit, TurnErrorResetsAt: &reset},
	} {
		ft = newFleetTestNoStore(t)
		fastDone(ft)
		run("s2", done)
		got := ft.pushesOf(assistantwire.NotifyDone, 1, 2*time.Second)
		if len(got) != 1 || !strings.HasPrefix(got[0].Title, "⚠️") || got[0].Body != limitPush(done.LastText) {
			t.Fatalf("%s: %+v", name, got)
		}
	}

	for _, text := range []string{"Login expired · Please run /login", "Please run /login · API Error: 403 forbidden"} {
		ft = newFleetTestNoStore(t)
		fastDone(ft)
		run("s3", proto.Session{LastText: text})
		sess := listed(t, ft.s, "mac", "s3")
		if sess.TurnError != text || sess.TurnErrorCode != proto.TurnErrorAuthFailed || sess.TurnErrorKind != proto.TurnErrorUser {
			t.Fatalf("%q listed: %q %q %q", text, sess.TurnError, sess.TurnErrorKind, sess.TurnErrorCode)
		}
		if st := taskState(Todo{}, []proto.Session{sess}); st != "interrupted" {
			t.Fatalf("%q: the task is %s", text, st)
		}
		got := ft.pushesOf(assistantwire.NotifyDone, 1, 2*time.Second)
		if len(got) != 1 || !strings.HasPrefix(got[0].Title, "⚠️") || !strings.HasPrefix(got[0].Body, interruptedSummary) {
			t.Fatalf("%q pushed as %+v", text, got)
		}
	}
}

// A task gets no receipt while one of its agents waits for a usage limit to reset and goes on then; once the wait is
// over with nothing having gone on, it gets the interrupted one, as it does at once when nothing would continue it.
// The assistant sees the task waiting for the reset, not interrupted.
func TestUsageLimitReceipt(t *testing.T) {
	s, f := newTaskService(t)
	startedTask(t, s, f)
	ctx := context.Background()
	stop := func(reset time.Time, resumes bool) {
		f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusWorking,
			State: proto.StateWorking, UpdatedAt: time.Now()})
		s.taskWorking(ctx, "box", "s1")
		c := limitTurn("s1", reset, resumes)
		c.MachineID, c.Agent, c.UpdatedAt = "box", "claude", time.Now()
		f.setSessions("box", c)
		s.taskTurnEnded(ctx, "box", "s1")
	}
	stop(time.Now().Add(time.Hour), true)
	if n := strings.Count(feedKinds(s, "conv1"), "receipt"); n != 0 {
		t.Fatalf("%d receipts while it waits for the reset", n)
	}
	todo := onlyTodo(s)
	if st := taskState(todo, s.taskAgents(ctx, todo)); st != "usage_limit" || taskStateLabel(st) == "" {
		t.Fatalf("task state %q", st)
	}

	// The wait ends in a moment and nothing went on: the receipt then says it was interrupted.
	stop(time.Now().Add(-limitResumeGrace+time.Second), true)
	if n := strings.Count(feedKinds(s, "conv1"), "receipt"); n != 0 {
		t.Fatalf("%d receipts before the wait was over", n)
	}
	waitFor(t, "the interrupted receipt", func() bool { return strings.Count(feedKinds(s, "conv1"), "receipt") == 1 })
	items, _, _ := s.feed(ctx, "conv1", time.Time{})
	if !strings.Contains(string(items[len(items)-1].Body), `"state":"interrupted"`) {
		t.Fatalf("receipt %s", items[len(items)-1].Body)
	}

	// Nothing would continue it (its CLI does not, and no continue is set): the receipt comes at once.
	stop(time.Now().Add(time.Hour), false)
	if n := strings.Count(feedKinds(s, "conv1"), "receipt"); n != 2 {
		t.Fatalf("%d receipts for a limit nothing takes up", n)
	}
}

// The task's state for the assistant: an agent waiting for its usage limit to reset makes it usage_limit, and is the
// one it is shown with; one cut off otherwise still makes it interrupted.
func TestUsageLimitTaskState(t *testing.T) {
	now := time.Now()
	limited := limitTurn("s1", now.Add(time.Hour), false)
	limited.State = proto.StateUnread
	cut := apiErrorTurn("s2", "API Error: 503 upstream")
	done := stopped("s3", "改好了")
	done.UpdatedAt = turnEnd.Add(time.Hour)
	if st := taskState(Todo{}, []proto.Session{limited, done}); st != "usage_limit" {
		t.Fatalf("state %q", st)
	}
	if lead := taskLead("usage_limit", []proto.Session{limited, done}); lead.SessionID != "s1" {
		t.Fatalf("shown with %s", lead.SessionID)
	}
	if st := taskState(Todo{}, []proto.Session{limited, cut}); st != "interrupted" {
		t.Fatalf("with one cut off: %q", st)
	}
	past := limitTurn("s1", now.Add(-limitResumeGrace-time.Minute), false)
	if st := taskState(Todo{}, []proto.Session{past}); st != "interrupted" {
		t.Fatalf("long past its reset: %q", st)
	}
}

func TestGoldenAgentLimitContinue(t *testing.T) {
	golden(t, "agent_limit_continue", prompts.RenderAgentLimitContinue())
}

// The wait's wakes end a turn each: a task agent's usage-limit wait is looked at again once, not once per turn; an
// earlier look takes over.
func TestUsageLimitLooksOncePerAgent(t *testing.T) {
	s, _ := newTaskService(t)
	ctx := context.Background()
	at := time.Now().Add(time.Hour)
	for range 5 {
		s.taskTurnEndedAfter(ctx, "box", "s1", at)
	}
	s.taskTurnEndedAfter(ctx, "box", "s1", at.Add(time.Minute)) // a later one: the earlier look sets it if need be
	ref := SessionRef{MachineID: "box", SessionID: "s1"}
	s.tasks.mu.Lock()
	n, got := len(s.tasks.limitLooks), s.tasks.limitLooks[ref]
	s.tasks.mu.Unlock()
	if n != 1 || !got.Equal(at) {
		t.Fatalf("%d looks, at %v", n, got)
	}
	earlier := at.Add(-30 * time.Minute)
	s.taskTurnEndedAfter(ctx, "box", "s1", earlier)
	s.tasks.mu.Lock()
	got = s.tasks.limitLooks[ref]
	s.tasks.mu.Unlock()
	if !got.Equal(earlier) {
		t.Fatalf("the earlier look did not take over: %v", got)
	}
}
