package assistant

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const fallbackRouteError = "API Error: 404 model fallback-model not available at the upstream"

func refusalFallback() *proto.ModelRefusal {
	return &proto.ModelRefusal{Subtype: "model_refusal_fallback", OriginalModel: "primary-model",
		FallbackModel: "fallback-model", Category: "synthetic-policy", Scope: "session"}
}

func TestRefusalFallbackSessionClassification(t *testing.T) {
	ft := newFleetTestNoStore(t)
	for _, tc := range []struct {
		name, text, kind, code, wantCode, wantKind string
		fallback                                   *proto.ModelRefusal
		working                                    bool
	}{
		{name: "fallback route", text: fallbackRouteError, fallback: refusalFallback(), wantCode: proto.TurnErrorModelFallbackUnavailable, wantKind: proto.TurnErrorUser},
		{name: "structured node error", text: "The selected route is unavailable", code: proto.TurnErrorModelFallbackUnavailable,
			fallback: refusalFallback(), wantCode: proto.TurnErrorModelFallbackUnavailable, wantKind: proto.TurnErrorUser},
		{name: "structured error name", text: "API Error: model_not_found", fallback: refusalFallback(), wantCode: proto.TurnErrorModelFallbackUnavailable, wantKind: proto.TurnErrorUser},
		{name: "unrelated session", text: fallbackRouteError, wantCode: proto.TurnErrorModelRoute},
		{name: "cleared fallback", text: fallbackRouteError, code: proto.TurnErrorModelFallbackUnavailable, wantCode: proto.TurnErrorModelRoute},
		{name: "nonmodel 404", text: "API Error: 404 resource not found", fallback: refusalFallback()},
		{name: "server failure", text: "API Error: 503 model unavailable at upstream", kind: proto.TurnErrorTransient,
			fallback: refusalFallback(), wantKind: proto.TurnErrorTransient},
		{name: "unknown refusal", text: fallbackRouteError, fallback: &proto.ModelRefusal{Subtype: "model_refusal_unknown", FallbackModel: "fallback-model"}, wantCode: proto.TurnErrorModelRoute},
		{name: "new turn", text: fallbackRouteError, code: proto.TurnErrorModelFallbackUnavailable, fallback: refusalFallback(), working: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := proto.Session{SessionID: "session", Agent: "claude", Status: proto.StatusDone, UpdatedAt: turnEnd,
				LastText: tc.text, TurnError: tc.text, TurnErrorKind: tc.kind, TurnErrorCode: tc.code, ModelFallback: tc.fallback}
			if tc.working {
				c.Status = proto.StatusWorking
			}
			ft.d.setSessions("mac", []proto.Session{c})
			got := listed(t, ft.s, "mac", c.SessionID)
			if got.TurnErrorCode != tc.wantCode || got.TurnErrorKind != tc.wantKind {
				t.Fatalf("classification: code=%q kind=%q, want %q %q", got.TurnErrorCode, got.TurnErrorKind, tc.wantCode, tc.wantKind)
			}
			if !tc.working && got.TurnError != tc.text {
				t.Fatalf("original failure changed: %q", got.TurnError)
			}
		})
	}
}

func TestRefusalFallbackReadiness(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			f, s, _ := readyMachine(t, proto.Session{SessionID: "m1", State: proto.StateIdle, LastText: fallbackRouteError,
				TurnError: fallbackRouteError, ModelFallback: refusalFallback(), UpdatedAt: time.Now()})
			r := getReadiness(t, s, "", string(lang))
			for _, id := range []string{StepModel, StepFirstMessage} {
				st := readinessStep(t, r, id)
				if st.State != StepProblem || st.Code != proto.TurnErrorModelFallbackUnavailable || st.Action != nil || len(st.AltActions) != 0 ||
					st.Title != i18n.T(lang, "assistant-readiness.model_fallback_unavailable_title") ||
					st.Detail != i18n.T(lang, "assistant-readiness.model_fallback_unavailable_detail") || !strings.Contains(st.Detail, "/model") {
					t.Fatalf("recovery in %s: %+v", lang, st)
				}
			}
			if r.Mode != ReadinessFull || !r.Ready {
				t.Fatalf("a session fallback blocked the configured model: %+v", r)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.checked) != 0 || f.made != 0 || len(f.agentSent) != 0 {
				t.Fatal("readiness sent work while explaining recovery")
			}
		})
	}
}

func TestRefusalFallbackWorkNeverAutoContinues(t *testing.T) {
	s, f := workService(t)
	c := apiErrorTurn("s1", fallbackRouteError)
	c.ModelFallback = refusalFallback()
	f.setSessions("box", c)
	triage(t, s, c.SessionID)
	e := entryOf(t, s, c.SessionID)
	if e.Work.On != proto.WaitUser || e.WakeAt != nil || e.InteractionID == "" || len(e.Work.Questions) != 1 ||
		!strings.Contains(e.Work.Questions[0].Question, "/model") {
		t.Fatalf("not waiting for recovery: %+v", e)
	}
	question := e.Work.Questions[0]
	if len(question.Options) != 1 || question.Options[0].Label != answerLabel(s.lang(context.Background()), answerSkip) {
		t.Fatalf("offered an action that can resend the request: %+v", question.Options)
	}
	answerCard(t, s, e.InteractionID, proto.Resolution{Answers: map[string]string{question.Question: question.Options[0].Label}})
	s.workTick(context.Background(), time.Now().Add(time.Hour))
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("automatically continued: %v", got)
	}
}

// A saved retry from an older manager, or one scheduled before the refusal metadata reached it, must not replay.
func TestRefusalFallbackCancelsEarlierContinue(t *testing.T) {
	s, f := workService(t)
	c := apiErrorTurn("s1", fallbackRouteError)
	f.setSessions("box", c)
	triage(t, s, c.SessionID)
	e := entryOf(t, s, c.SessionID)
	if e.Work.On != proto.WaitTransient || e.WakeAt == nil {
		t.Fatalf("fixture did not queue a continuation: %+v", e)
	}
	c.ModelFallback = refusalFallback()
	f.setSessions("box", c)
	ctx := context.Background()
	s.workTick(ctx, e.WakeAt.Add(time.Second))
	if got := entryOf(t, s, c.SessionID); got.WakeAt != nil || got.Work.WakeAt != nil {
		t.Fatalf("old continuation still queued: %+v", got)
	}
	s.workTick(ctx, time.Now().Add(time.Hour))
	if got := f.sentToAgents(); len(got) != 0 {
		t.Fatalf("replayed an old continuation: %v", got)
	}
	if _, why := s.workGuard(s.newWorkView(ctx), e, true); why != "" {
		t.Fatalf("an explicit user action was blocked: %s", why)
	}
}

func TestRefusalFallbackScheduledRunNeverRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFakeHub().withMachine()
		s := New(Options{Hub: f, Logf: testLogf(t)})
		s.turnPoll, s.runRetry = time.Second, time.Minute
		task := Task{ID: "p", Title: "Example check", Prompt: "Check the example", Repeat: repeatInterval, EveryMinutes: 30,
			At: time.Now(), Enabled: true, MachineID: "server"}
		setTasks(s, task)
		f.items = []proto.Item{{ID: "failure", Role: "assistant", Kind: proto.KindText, Text: fallbackRouteError}}
		f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
		run := ScheduleRun{RefID: task.ID, Status: runOK, SessionID: "s1"}
		run.ID = s.recordRun(run)
		done := make(chan struct{})
		go func() {
			s.watchScheduledRun(run, task, Device{ID: "server", Name: "Example computer"})
			close(done)
		}()
		synctest.Wait()
		<-time.After(2 * s.turnPoll)
		synctest.Wait()
		c := apiErrorTurn("s1", fallbackRouteError)
		c.Assistant, c.ModelFallback = true, refusalFallback()
		f.setSessions("server", c)
		select {
		case <-done:
		case <-time.After(10 * s.turnPoll):
			t.Fatal("the failed run waited to retry instead of ending")
		}
		synctest.Wait()
		if f.sent() != 0 || retried(s) {
			t.Fatal("scheduled fallback failure was retried")
		}
		sc := s.loadSchedule(context.Background())
		sc.mu.Lock()
		defer sc.mu.Unlock()
		if len(sc.runs) != 1 || sc.runs[0].Status != runFailed || sc.runs[0].Error != fallbackRouteError {
			t.Fatalf("failure not retained: %+v", sc.runs)
		}
	})
}
