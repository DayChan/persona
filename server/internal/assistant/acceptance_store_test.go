package assistant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
)

// An acceptance the hub cannot keep is not one: when the "Please verify" asks cannot be read, or the accepted round cannot
// be saved, every way to accept (the web button, the card's button, a reply) says the service is unavailable, never
// "already handled" or "verified", and the task does not move on. Once the store answers, the same press accepts.
func TestAcceptStoreFailureIsNotAccepted(t *testing.T) {
	ctx := context.Background()
	e := newBriefEnv(t)
	e.verify("todo_v1", "Fix the login crash", 2*time.Hour)
	unavailable := i18n.T(e.s.botLang(ctx), "http-status.unavailable")
	failPut := func(err error) {
		e.st.mu.Lock()
		e.st.failPut = err
		e.st.mu.Unlock()
	}
	stage := func() string {
		e.s.todos.mu.Lock()
		defer e.s.todos.mu.Unlock()
		return e.s.todos.todos["todo_v1"].Stage
	}
	lastNote := func() string {
		e.mu.Lock()
		defer e.mu.Unlock()
		if len(e.texts) == 0 {
			return ""
		}
		return textOf(e.texts[len(e.texts)-1])
	}
	press := func(asked string) *feishubot.CardResponse {
		var a feishubot.CardAction
		a.Action.Value = map[string]any{"a": "accept", "id": "todo_v1"}
		if asked != "" {
			a.Action.Value["asked_at"] = asked
		}
		a.Context.MessageID = "om_todo_v1"
		return e.s.botCardAction(ctx, &a, "")
	}
	asked := askOf(e.s, "todo_v1").AskedAt.Format(timeKeyLayout)
	route := func(s *Service) int {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/todos/todo_v1/accept", strings.NewReader(`{"asked_at":"`+asked+`"}`)))
		return rec.Code
	}
	notAccepted := func(what string) {
		t.Helper()
		if a := askOf(e.s, "todo_v1"); a == nil || !a.AcceptedAt.IsZero() || stage() != "" {
			t.Fatalf("%s: an unsaved acceptance took: %+v, stage %q", what, a, stage())
		}
	}

	failPut(errors.New("database unavailable"))
	if ok, err := e.s.acceptTry(ctx, "todo_v1", ""); ok || !errors.Is(err, errAcceptStore) {
		t.Fatalf("acceptTry with the save failing = %v, %v; want errAcceptStore", ok, err)
	}
	notAccepted("acceptTry")
	if r := press(""); r == nil || r.Toast == nil || r.Toast.Type != "error" || r.Toast.Content != unavailable {
		t.Fatalf("the card's button with the save failing: %+v", r)
	}
	notAccepted("the card's button")
	if r := press(asked); r == nil || r.Toast == nil || r.Toast.Type != "error" || r.Toast.Content != unavailable {
		t.Fatalf("the briefing card's button with the save failing: %+v", r)
	}
	notAccepted("the briefing card's button")
	if !e.s.acceptReply(ctx, "om_reply", "ok", false, "om_todo_v1") || lastNote() != unavailable {
		t.Fatalf("a reply with the save failing: note %q, want %q", lastNote(), unavailable)
	}
	notAccepted("a reply")
	if code := route(e.s); code != http.StatusServiceUnavailable {
		t.Fatalf("the web button with the save failing: %d, want 503", code)
	}
	notAccepted("the web button")

	failPut(nil)
	if code := route(e.s); code != http.StatusOK || askOf(e.s, "todo_v1").AcceptedAt.IsZero() || stage() != "verify" {
		t.Fatalf("the web button once the store answers: %d, stage %q", code, stage())
	}

	// A hub that just restarted, with the asks unreadable: the button of the round that waits is not "already handled".
	st := &memStore{}
	at := time.Now().Add(-30 * time.Hour).UTC()
	if err := st.PutSetting(ctx, acceptanceKey, map[string]*acceptAsk{"todo_v1": {MessageID: "om_v1", Messages: []string{"om_v1"}, AskedAt: at}}); err != nil {
		t.Fatal(err)
	}
	r := newBoardEnvOn(t, st)
	r.s.todos.mu.Lock()
	r.s.todos.todos["todo_v1"] = &Todo{ID: "todo_v1", Title: "Fix the login crash", Origin: "conv1"}
	r.s.todos.mu.Unlock()
	asked = at.Format(timeKeyLayout)
	st.mu.Lock()
	st.failSetting = errors.New("database unavailable")
	st.mu.Unlock()
	if ok, err := r.s.acceptTryFromBrief(ctx, "todo_v1", asked); ok || !errors.Is(err, errAcceptStore) {
		t.Fatalf("acceptTryFromBrief with the asks unreadable = %v, %v; want errAcceptStore", ok, err)
	}
	if code := route(r.s); code != http.StatusServiceUnavailable {
		t.Fatalf("the web button with the asks unreadable: %d, want 503", code)
	}
	st.mu.Lock()
	st.failSetting = nil
	st.mu.Unlock()
	if code := route(r.s); code != http.StatusOK || askOf(r.s, "todo_v1").AcceptedAt.IsZero() {
		t.Fatalf("the web button once the asks read: %d", code)
	}
}
