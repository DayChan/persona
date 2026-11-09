package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/github"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
)

// The route a recall comes in by (docs/protocol.md "Connector API"): POST /api/v1/connectors/sent/{id}/recall.

func recallPath(id int64) string { return fmt.Sprintf("/api/v1/connectors/sent/%d/recall", id) }

// The user's own full device, having asked the user once, takes a message back: the connector is asked once with what
// the row says, the row is marked, and the answer is the row as the ledger now has it.
func TestRecallRouteTakesTheMessageBack(t *testing.T) {
	e := newRecallEnv(t)
	id := e.send(time.Hour)
	code, out := call(t, e.h.routes(), "POST", recallPath(id), deviceToken("dev_phone", roleFull), `{"confirm":true}`)
	if code != 200 || out["ok"] != true {
		t.Fatalf("%d %v", code, out)
	}
	item, _ := out["item"].(map[string]any)
	if at, _ := time.Parse(time.RFC3339Nano, fmt.Sprint(item["recalled_at"])); item["id"] != float64(id) || !at.Equal(e.now) ||
		item["message_ref"] != "41" || item["can_recall"] != false {
		t.Fatalf("item %v, the time of the recall %v", item, e.now)
	}
	if e.recalledAt(id) == nil || e.c.askedCount() != 1 || e.c.asked[0].Ref != "41" {
		t.Fatalf("marked %v, connector asked %+v", e.recalledAt(id), e.c.asked)
	}
}

// A message is taken back by the user's own device, by hand, and by nothing else: not an answer-only device, a pairing
// token (a script or an AI the user paired), an extension's token with every scope there is, the master token from
// another machine, and not the owner's master token on the hub machine either, which does read the ledger (any process
// on that machine can read it, and in the audit it is not told from a person). A refused request says why, changes
// nothing, and the connector hears of none of them.
func TestRecallRouteIsForTheUsersOwnDevicesOnly(t *testing.T) {
	e := newRecallEnv(t)
	mux := e.h.routes()
	pairing := "jp_script_" + randomHex(4)
	testDevices.Store(pairing, principal{ID: "pair", Role: roleFull, Paired: true})
	remote := func(addr, token string) int {
		req := httptest.NewRequest("POST", recallPath(1), strings.NewReader(`{"confirm":true}`))
		req.RemoteAddr = addr
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	phone := deviceToken("dev_phone", roleFull)
	taken := e.send(0)
	if code, out := call(t, mux, "POST", recallPath(taken), phone, `{"confirm":true}`); code != 200 {
		t.Fatalf("a full device: %d %v", code, out)
	}
	if e.recalledAt(taken) == nil {
		t.Fatal("a full device: the row was not marked")
	}
	id := e.send(0)
	asked := e.c.askedCount()
	// The owner reads the ledger, and cannot take anything back, and the answer says why.
	if code, _ := call(t, mux, "GET", "/api/v1/connectors/sent", e.h.cfg.Token, ""); code != 200 {
		t.Fatalf("the owner reading the ledger: %d", code)
	}
	code, out := call(t, mux, "POST", recallPath(id), e.h.cfg.Token, `{"confirm":true}`)
	if msg, _ := out["error"].(string); code != 403 || out["code"] != "own_device_only" || !strings.Contains(msg, "your own device") || !strings.Contains(msg, "owner's token") {
		t.Fatalf("the owner on the hub machine: %d %v", code, out)
	}
	denied := map[string]struct {
		token string
		code  int
	}{
		"an answer-only device": {deviceToken("dev_watch", roleAnswer), 403},
		"a pairing token":       {pairing, 403},
		"an extension's token":  {e.h.extTokens.issue("example", "Example", extension.HubScopes), 403},
		"an extension with all": {e.h.extTokens.issueAll("example-all", "Example"), 403},
		"no token":              {"", 401},
		"a token nobody has":    {"jd_nobody", 401},
	}
	for who, tc := range denied {
		if code, out := call(t, mux, "POST", recallPath(id), tc.token, `{"confirm":true}`); code != tc.code {
			t.Errorf("%s: %d %v", who, code, out)
		}
	}
	if code := remote("203.0.113.7:4000", e.h.cfg.Token); code != 403 {
		t.Errorf("the master token from another machine: %d", code)
	}
	// What the hub's own check does not stop is stopped here: the handler reads who asked as the hub passed it on.
	as := func(p principal) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", recallPath(id), strings.NewReader(`{"confirm":true}`))
		e.h.apiMux.ServeHTTP(rec, req.WithContext(withPrincipal(req.Context(), p)))
		return rec
	}
	for who, p := range map[string]principal{
		"the owner":                             {Role: roleFull, Owner: true},
		"an extension's token that got through": {ID: "extension:example", Role: roleFull, Extension: "example"},
		"an extension's token on the owner's":   {Role: roleFull, Owner: true, Extension: "example"},
		"an answer-only device":                 {ID: "dev_watch", Role: roleAnswer},
		"a pairing token":                       {ID: "pair", Role: roleFull, Paired: true},
		"a request the hub did not identify":    {Role: roleFull},
	} {
		if rec := as(p); rec.Code != 403 || bodyOf(t, rec)["code"] != "own_device_only" {
			t.Errorf("%s: %d %s", who, rec.Code, rec.Body)
		}
	}
	if e.recalledAt(id) != nil || e.c.askedCount() != asked {
		t.Fatalf("a refused request changed something: marked %v, connector asked %d times (was %d)", e.recalledAt(id), e.c.askedCount(), asked)
	}
}

// No way in skips the confirmation: whoever asks, a request that does not say the user agreed recalls nothing and the
// connector is not asked. The user's own device is told what is missing; the rest are refused before it matters.
func TestRecallRouteNoPathSkipsTheConfirmation(t *testing.T) {
	e := newRecallEnv(t)
	e.h.routes()
	id := e.send(0)
	for who, tc := range map[string]struct {
		p      principal
		status int
	}{
		"the user's own device":          {principal{ID: "dev_phone", Role: roleFull}, 400},
		"the owner":                      {principal{Role: roleFull, Owner: true}, 403},
		"an owner that is also a device": {principal{ID: "dev_phone", Role: roleFull, Owner: true}, 400},
		"a pairing token":                {principal{ID: "pair", Role: roleFull, Paired: true}, 403},
		"an answer-only device":          {principal{ID: "dev_watch", Role: roleAnswer}, 403},
		"an extension":                   {principal{ID: "extension:example", Role: roleFull, Extension: "example"}, 403},
	} {
		for _, body := range []string{``, `{}`, `{"confirm":false}`, `{"confirm":null}`} {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest("POST", recallPath(id), strings.NewReader(body))
			e.h.apiMux.ServeHTTP(rec, req.WithContext(withPrincipal(req.Context(), tc.p)))
			if rec.Code != tc.status {
				t.Errorf("%s, body %q: %d %s, want %d", who, body, rec.Code, rec.Body, tc.status)
			}
		}
	}
	if e.recalledAt(id) != nil || e.c.askedCount() != 0 {
		t.Fatalf("recalled without the user's word: marked %v, connector asked %d times", e.recalledAt(id), e.c.askedCount())
	}
}

// The one confirmation is the request's own: the app's dialog sends {"confirm": true} once the user agreed. A body
// without it (none, empty, false, something else) or one that is not JSON recalls nothing, so a stray POST cannot
// delete a message; the user is not asked a second time on a card.
func TestRecallRouteNeedsTheUsersConfirmation(t *testing.T) {
	e := newRecallEnv(t)
	mux := e.h.routes()
	phone := deviceToken("dev_phone", roleFull)
	id := e.send(0)
	for body, want := range map[string]struct {
		code int
		kind string
	}{
		``:                   {400, "confirm_required"},
		`{}`:                 {400, "confirm_required"},
		`{"confirm":false}`:  {400, "confirm_required"},
		`{"confirm":null}`:   {400, "confirm_required"},
		`{"confirm":"yes"}`:  {400, "bad_request"},
		`{"confirm":1}`:      {400, "bad_request"},
		`confirm`:            {400, "bad_request"},
		`{"confirm":true`:    {400, "bad_request"},
		`{"confirmed":true}`: {400, "confirm_required"},
		`[true]`:             {400, "bad_request"},
		`{"other":"` + strings.Repeat("x", 5000) + `"}`: {413, "too_large"},
	} {
		code, out := call(t, mux, "POST", recallPath(id), phone, body)
		if code != want.code || out["code"] != want.kind {
			t.Errorf("body %.40q: %d %v", body, code, out)
		}
	}
	if e.recalledAt(id) != nil || e.c.askedCount() != 0 {
		t.Fatalf("recalled without the user's word: marked %v, connector asked %d times", e.recalledAt(id), e.c.askedCount())
	}
	if code, out := call(t, mux, "POST", recallPath(id), phone, `{"confirm":true,"other":1}`); code != 200 {
		t.Fatalf("with it: %d %v", code, out)
	}
}

// Each reason a recall did not happen is the status docs/connectors.md gives it, with the core's code in the body, and
// the row is left as it was: 404 not_found, 409 not_recallable / too_old / already_recalled, 503 connector_unavailable
// and no_database, 502 recall_refused, 504 recall_unknown; a failure of the ledger itself is a plain 500.
func TestRecallRouteAnswersByCode(t *testing.T) {
	fails := func(e *recallEnv, err error) int64 {
		e.c.recall = func(context.Context, connector.SentMessage) error { return err }
		return e.send(0)
	}
	for _, tc := range []struct {
		name   string
		setup  func(e *recallEnv) int64
		status int
		code   string
	}{
		{"a row that is not there", func(e *recallEnv) int64 { return 99 }, 404, RecallNotFound},
		{"a ledger that is not there", func(e *recallEnv) int64 { e.h.store = newKeepStore(); return 1 }, 503, RecallNoDatabase},
		{"a kind of message it cannot take back", func(e *recallEnv) int64 {
			return e.put(db.SentAsUser{Connector: "example", Action: "example_note", MessageRef: "5"})
		}, 409, RecallNotRecallable},
		{"a write that gave no ref", func(e *recallEnv) int64 { e.c.ref = ""; return e.send(0) }, 409, RecallNotRecallable},
		{"a connector that found it could not", func(e *recallEnv) int64 {
			return fails(e, fmt.Errorf("%w: the row has no comment id", connector.ErrCannotRecall))
		}, 409, RecallNotRecallable},
		{"a message older than the window", func(e *recallEnv) int64 { return e.send(24*time.Hour + time.Second) }, 409, RecallTooOld},
		{"a message older than the connector allows", func(e *recallEnv) int64 { e.c.limit = 2 * time.Hour; return e.send(3 * time.Hour) }, 409, RecallTooOld},
		{"a message taken back before", func(e *recallEnv) int64 {
			id := e.send(0)
			if _, err := e.recall(id); err != nil {
				e.t.Fatal(err)
			}
			return id
		}, 409, RecallAlreadyRecalled},
		{"a connector not signed in", func(e *recallEnv) int64 {
			id := e.send(0)
			e.c.state.Store(connector.StateNotSignedIn)
			return id
		}, 503, RecallUnavailable},
		{"a connector that is offline", func(e *recallEnv) int64 {
			return fails(e, fmt.Errorf("%w: the computer is offline", connector.ErrRecallUnavailable))
		}, 503, RecallUnavailable},
		{"the other side refusing", func(e *recallEnv) int64 { return fails(e, errors.New("GitHub DELETE: 403 not allowed")) }, 502, RecallRefused},
		{"an outcome nobody can tell", func(e *recallEnv) int64 {
			return fails(e, fmt.Errorf("%w: the connection broke", connector.ErrRecallUnknown))
		}, 504, RecallUnknown},
		{"a ledger that cannot mark the row", func(e *recallEnv) int64 {
			fail := &atomic.Bool{}
			fail.Store(true)
			e.h.store = markFailing{e.st, fail}
			return e.send(0)
		}, 500, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecallEnv(t)
			id := tc.setup(e)
			was := e.recalledAt(id)
			code, out := call(t, e.h.routes(), "POST", recallPath(id), deviceToken("dev_phone", roleFull), `{"confirm":true}`)
			if code != tc.status {
				t.Fatalf("%d %v, want %d", code, out, tc.status)
			}
			if tc.code != "" && out["code"] != tc.code {
				t.Errorf("code %v, want %s", out["code"], tc.code)
			}
			if tc.code == "" && out["detail"] == nil && out["error"] == nil {
				t.Errorf("a failure that says nothing: %v", out)
			}
			if now := e.recalledAt(id); tc.name != "a ledger that cannot mark the row" && ((now == nil) != (was == nil) || now != nil && !now.Equal(*was)) {
				t.Errorf("recalled_at %v, was %v", now, was)
			}
		})
	}
}

// A message taken back before is a 409 that brings the row with it, as the ledger has it now (the time of the recall
// that did it), so a screen that was behind can show it taken back without asking again; neither the connector nor
// the row is touched.
func TestRecallRouteGivesBackTheRowTakenBackBefore(t *testing.T) {
	e := newRecallEnv(t)
	mux := e.h.routes()
	phone := deviceToken("dev_phone", roleFull)
	id := e.send(0)
	code, first := call(t, mux, "POST", recallPath(id), phone, `{"confirm":true}`)
	if code != 200 {
		t.Fatalf("%d %v", code, first)
	}
	e.now = e.now.Add(time.Minute)
	code, again := call(t, mux, "POST", recallPath(id), phone, `{"confirm":true}`)
	if code != 409 || again["code"] != RecallAlreadyRecalled {
		t.Fatalf("%d %v", code, again)
	}
	item, _ := again["item"].(map[string]any)
	want, _ := first["item"].(map[string]any)
	if item == nil || item["id"] != float64(id) || item["recalled_at"] == nil || item["recalled_at"] != want["recalled_at"] || item["text"] != "hi" {
		t.Fatalf("the row: %v, taken back as %v", item, want["recalled_at"])
	}
	if e.c.askedCount() != 1 {
		t.Fatalf("the connector was asked %d times", e.c.askedCount())
	}
}

// post is one recall request for row id as the user's own device, made straight on the manager's routes (e.h.routes
// must have been called) with ctx as the request's own.
func (e *recallEnv) post(ctx context.Context, id int64) *httptest.ResponseRecorder {
	return e.postBody(ctx, id, `{"confirm":true}`)
}

// postBody is post with the body the test gives.
func (e *recallEnv) postBody(ctx context.Context, id int64, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", recallPath(id), strings.NewReader(body))
	rec := httptest.NewRecorder()
	e.h.apiMux.ServeHTTP(rec, req.WithContext(withPrincipal(ctx, principal{ID: "dev_phone", Role: roleFull})))
	return rec
}

// holdFirst makes the connector hold its first recall until the returned release is closed (the test's own way out
// after 5 seconds) and then answer it with answer; every later call is done at once. maxInside says how many calls were
// inside the connector at the same time, at most.
func (e *recallEnv) holdFirst(answer error) (release func(), maxInside func() int32) {
	ch := make(chan struct{})
	var calls, inside, most atomic.Int32
	e.c.recall = func(context.Context, connector.SentMessage) error {
		n := inside.Add(1)
		defer inside.Add(-1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		if calls.Add(1) == 1 {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
			}
			return answer
		}
		return nil
	}
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }, most.Load
}

// postAsync starts a request and says where its answer arrives.
func (e *recallEnv) postAsync(ctx context.Context, id int64) <-chan *httptest.ResponseRecorder {
	out := make(chan *httptest.ResponseRecorder, 1)
	go func() { out <- e.post(ctx, id) }()
	return out
}

// answered says whether a request's answer is in yet.
func answered(c <-chan *httptest.ResponseRecorder) bool { return len(c) > 0 }

// slowFirst makes the connector hold its first recall until the returned release is closed (the test's own way out
// after 5 seconds), with every later one done at once; answer is what the first says.
func (e *recallEnv) slowFirst(answer error) (release func()) {
	ch := make(chan struct{})
	var calls atomic.Int32
	e.c.recall = func(context.Context, connector.SentMessage) error {
		if calls.Add(1) == 1 {
			select {
			case <-ch:
			case <-time.After(5 * time.Second):
			}
			return answer
		}
		return nil
	}
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// settle gives a request that should be waiting its chance to reach the connector, and says how many times the
// connector was asked in the end.
func (e *recallEnv) settle() int {
	time.Sleep(200 * time.Millisecond)
	return e.c.askedCount()
}

func bodyOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	out := map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%d %q: %v", rec.Code, rec.Body.String(), err)
	}
	return out
}

// Two requests to take one message back at once share one recall and its successful answer: both get 200 with the
// same recalled row instead of asking the connector twice. The core leaves this to its caller.
func TestRecallRouteTwiceAtOnceTakesBackOnce(t *testing.T) {
	e := newRecallEnv(t)
	e.h.routes()
	release := e.slowFirst(nil)
	defer release()
	id := e.send(0)
	var wg sync.WaitGroup
	recs := make([]*httptest.ResponseRecorder, 2)
	start := func(i int) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs[i] = e.post(context.Background(), id)
		}()
	}
	start(0)
	eventually(t, 5*time.Second, "the first recall to reach the connector", func() bool { return e.c.askedCount() == 1 })
	start(1)
	if n := e.settle(); n != 1 {
		release()
		wg.Wait()
		t.Fatalf("the connector was asked %d times while the first recall was on its way", n)
	}
	release()
	wg.Wait()
	if recs[0].Code != 200 || recs[1].Code != 200 || bodyOf(t, recs[1])["ok"] != true {
		t.Fatalf("first %d %s, second %d %s", recs[0].Code, recs[0].Body, recs[1].Code, recs[1].Body)
	}
	if item, _ := bodyOf(t, recs[1])["item"].(map[string]any); item["recalled_at"] != bodyOf(t, recs[0])["item"].(map[string]any)["recalled_at"] {
		t.Fatalf("the second's row is %v", item)
	}
	if e.c.askedCount() != 1 || e.recalledAt(id) == nil {
		t.Fatalf("the connector was asked %d times, marked %v", e.c.askedCount(), e.recalledAt(id))
	}
}

// Three requests for one row at once, the first taking the message back: the connector is asked once, by the first;
// all three get the same 200 and recalled row. A request after the flight still finds it already taken back.
func TestRecallRouteThreeAtOnceFirstSucceeds(t *testing.T) {
	e := newRecallEnv(t)
	e.h.routes()
	release, maxInside := e.holdFirst(nil)
	defer release()
	id := e.send(0)
	first := e.postAsync(context.Background(), id)
	eventually(t, 5*time.Second, "the first recall to reach the connector", func() bool { return e.c.askedCount() == 1 })
	second, third := e.postAsync(context.Background(), id), e.postAsync(context.Background(), id)
	if n := e.settle(); n != 1 || answered(first) || answered(second) || answered(third) {
		release()
		t.Fatalf("while the first recall was on its way: the connector asked %d times, answers in: %v %v %v", n, answered(first), answered(second), answered(third))
	}
	release()
	won := <-first
	if won.Code != 200 {
		t.Fatalf("the first: %d %s", won.Code, won.Body)
	}
	for i, c := range []<-chan *httptest.ResponseRecorder{second, third} {
		select {
		case rec := <-c:
			item, _ := bodyOf(t, rec)["item"].(map[string]any)
			if rec.Code != 200 || bodyOf(t, rec)["ok"] != true || item["recalled_at"] == nil {
				t.Errorf("waiter %d: %d %s", i, rec.Code, rec.Body)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("waiter %d is still waiting after the first recall ended", i)
		}
	}
	if e.c.askedCount() != 1 || maxInside() != 1 || e.recalledAt(id) == nil {
		t.Fatalf("the connector was asked %d times, %d at once, marked %v", e.c.askedCount(), maxInside(), e.recalledAt(id))
	}
}

// Three requests for one row at once, the first failing: the other two wait for it and get the same real refusal
// (502 recall_refused), not a generic busy answer. They do not ask the connector themselves, so it is asked once,
// never two at a time. The user's next tap is a new recall and goes through.
func TestRecallRouteThreeAtOnceFirstFails(t *testing.T) {
	e := newRecallEnv(t)
	e.h.routes()
	release, maxInside := e.holdFirst(errors.New("GitHub DELETE: 403 not allowed"))
	defer release()
	id := e.send(0)
	first := e.postAsync(context.Background(), id)
	eventually(t, 5*time.Second, "the first recall to reach the connector", func() bool { return e.c.askedCount() == 1 })
	second, third := e.postAsync(context.Background(), id), e.postAsync(context.Background(), id)
	if n := e.settle(); n != 1 || answered(second) || answered(third) {
		release()
		t.Fatalf("while the first recall was on its way: the connector asked %d times, waiters answered: %v %v", n, answered(second), answered(third))
	}
	release()
	if rec := <-first; rec.Code != 502 || bodyOf(t, rec)["code"] != RecallRefused {
		t.Fatalf("the first: %d %s", rec.Code, rec.Body)
	}
	for i, c := range []<-chan *httptest.ResponseRecorder{second, third} {
		select {
		case rec := <-c:
			if rec.Code != 502 || bodyOf(t, rec)["code"] != RecallRefused || bodyOf(t, rec)["item"] != nil {
				t.Errorf("waiter %d: %d %s", i, rec.Code, rec.Body)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("waiter %d is still waiting after the first recall ended", i)
		}
	}
	if e.c.askedCount() != 1 || maxInside() != 1 || e.recalledAt(id) != nil {
		t.Fatalf("the connector was asked %d times, %d at once, marked %v", e.c.askedCount(), maxInside(), e.recalledAt(id))
	}
	if rec := e.post(context.Background(), id); rec.Code != 200 || e.c.askedCount() != 2 || e.recalledAt(id) == nil {
		t.Fatalf("the next tap: %d %s, asked %d, marked %v", rec.Code, rec.Body, e.c.askedCount(), e.recalledAt(id))
	}
}

// An unknown outcome is shared as unknown, never as success and never hidden behind a generic busy response. The row
// stays unmarked, and a later explicit request can reconcile it with the connector.
func TestRecallRouteConcurrentUnknownStaysUnknown(t *testing.T) {
	e := newRecallEnv(t)
	e.h.routes()
	release, maxInside := e.holdFirst(fmt.Errorf("%w: the answer was lost", connector.ErrRecallUnknown))
	defer release()
	id := e.send(0)
	first := e.postAsync(context.Background(), id)
	eventually(t, 5*time.Second, "the first recall to reach the connector", func() bool { return e.c.askedCount() == 1 })
	second := e.postAsync(context.Background(), id)
	if n := e.settle(); n != 1 || answered(second) {
		release()
		t.Fatalf("while the first recall was on its way: asked %d, waiter answered %v", n, answered(second))
	}
	release()
	for name, answer := range map[string]<-chan *httptest.ResponseRecorder{"first": first, "waiter": second} {
		rec := <-answer
		if rec.Code != 504 || bodyOf(t, rec)["code"] != RecallUnknown || bodyOf(t, rec)["ok"] == true {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if e.c.askedCount() != 1 || maxInside() != 1 || e.recalledAt(id) != nil {
		t.Fatalf("the connector was asked %d times, %d at once, marked %v", e.c.askedCount(), maxInside(), e.recalledAt(id))
	}
	if rec := e.post(context.Background(), id); rec.Code != 200 || e.c.askedCount() != 2 || e.recalledAt(id) == nil {
		t.Fatalf("the next tap: %d %s, asked %d, marked %v", rec.Code, rec.Body, e.c.askedCount(), e.recalledAt(id))
	}
}

// A request that does not say the user agreed does not take a place behind a recall on its way: it is told at once.
func TestRecallRouteUnconfirmedDoesNotWait(t *testing.T) {
	e := newRecallEnv(t)
	e.h.routes()
	release, _ := e.holdFirst(nil)
	defer release()
	id := e.send(0)
	first := e.postAsync(context.Background(), id)
	eventually(t, 5*time.Second, "the first recall to reach the connector", func() bool { return e.c.askedCount() == 1 })
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.postBody(context.Background(), id, `{}`) }()
	select {
	case rec := <-done:
		if rec.Code != 400 || bodyOf(t, rec)["code"] != "confirm_required" {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a request without the user's word waits behind a recall")
	}
	release()
	if rec := <-first; rec.Code != 200 {
		t.Fatalf("the first: %d %s", rec.Code, rec.Body)
	}
}

// A request that is waiting for another recall of its row stops when its own client goes away, without asking the
// connector; the recall it waited for is not affected.
func TestRecallRouteWaitingStopsWithTheRequest(t *testing.T) {
	e := newRecallEnv(t)
	e.h.routes()
	release := e.slowFirst(nil)
	defer release()
	id := e.send(0)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- e.post(context.Background(), id) }()
	eventually(t, 5*time.Second, "the first recall to reach the connector", func() bool { return e.c.askedCount() == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	waiting := make(chan *httptest.ResponseRecorder, 1)
	go func() { waiting <- e.post(ctx, id) }()
	if n := e.settle(); n != 1 {
		t.Fatalf("the connector was asked %d times", n)
	}
	cancel()
	select {
	case rec := <-waiting:
		if rec.Code < 400 {
			t.Fatalf("a request that gave up got %d %s", rec.Code, rec.Body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a request whose client went away is still waiting")
	}
	release()
	if rec := <-first; rec.Code != 200 || e.c.askedCount() != 1 || e.recalledAt(id) == nil {
		t.Fatalf("the first: %d %s, asked %d, marked %v", rec.Code, rec.Body, e.c.askedCount(), e.recalledAt(id))
	}
}

// What one recall waits for is the same row's: a recall on its way holds up no other message's.
func TestRecallRouteHoldsUpOnlyItsOwnRow(t *testing.T) {
	e := newRecallEnv(t)
	e.h.routes()
	release := e.slowFirst(nil)
	defer release()
	slow, other := e.send(0), e.send(0)
	first := make(chan *httptest.ResponseRecorder, 1)
	go func() { first <- e.post(context.Background(), slow) }()
	eventually(t, 5*time.Second, "the first recall to reach the connector", func() bool { return e.c.askedCount() == 1 })
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- e.post(context.Background(), other) }()
	select {
	case rec := <-done:
		if rec.Code != 200 || e.recalledAt(other) == nil || e.recalledAt(slow) != nil {
			t.Fatalf("%d %s, marked %v and %v", rec.Code, rec.Body, e.recalledAt(other), e.recalledAt(slow))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a recall of another row waits for this one")
	}
	release()
	if rec := <-first; rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

// readRows is the ledger as GET /api/v1/connectors/sent gives it to the user's own device, by row id.
func (e *recallEnv) readRows(t *testing.T) map[int64]map[string]any {
	t.Helper()
	code, out := call(t, e.h.routes(), "GET", "/api/v1/connectors/sent?limit=200", deviceToken("dev_phone", roleFull), "")
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	rows := map[int64]map[string]any{}
	items, _ := out["items"].([]any)
	for _, it := range items {
		m := it.(map[string]any)
		rows[int64(m["id"].(float64))] = m
	}
	return rows
}

// at puts a row of its own in the ledger, sent age before the clock.
func (e *recallEnv) at(age time.Duration, action string) int64 {
	e.t.Helper()
	id, err := e.st.RecordSentAsUser(context.Background(), db.SentAsUser{At: e.now.Add(-age), Connector: "example", Action: action, MessageRef: "41"})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// Each row of the ledger says whether its message can be taken back now (can_recall, with the very answer RecallSent
// acts on), besides recalled_at once it was. Nothing else of a row changes, so an older app that does not know the new
// field reads the page as it did.
func TestSentLedgerRowsSayWhetherTheyCanBeTakenBack(t *testing.T) {
	const h = time.Hour
	e := newRecallEnv(t)
	e.now = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	young := e.at(h, "example_send")
	atTheLimit := e.at(24*h, "example_send")
	late := e.at(24*h+time.Nanosecond, "example_send")
	note := e.at(h, "example_note")
	done := e.at(2*h, "example_send")
	if ok, err := e.st.MarkSentAsUserRecalled(context.Background(), done, e.now.Add(-h)); !ok || err != nil {
		t.Fatal(ok, err)
	}
	noRef, err := e.st.RecordSentAsUser(context.Background(), db.SentAsUser{At: e.now.Add(-h), Connector: "example", Action: "example_send"})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := e.st.RecordSentAsUser(context.Background(), db.SentAsUser{At: e.now.Add(-h), Connector: "nobody", Action: "nobody_send", MessageRef: "7"})
	if err != nil {
		t.Fatal(err)
	}

	rows := e.readRows(t)
	for id, want := range map[int64]bool{young: true, atTheLimit: true, late: false, note: false, done: false, noRef: false, gone: false} {
		row := rows[id]
		if can, ok := row["can_recall"].(bool); !ok || can != want {
			t.Errorf("row %d %v: can_recall %v, want %v", id, row["action"], row["can_recall"], want)
		}
	}
	if rows[done]["recalled_at"] == nil || rows[young]["recalled_at"] != nil {
		t.Errorf("recalled_at: %v and %v", rows[done]["recalled_at"], rows[young]["recalled_at"])
	}
	for _, key := range []string{"id", "at", "connector", "action", "target", "text", "summary", "origin", "let_through", "message_ref"} {
		if _, ok := rows[young][key]; !ok && key != "message_ref" {
			t.Errorf("a row lost %q: %v", key, rows[young])
		}
	}
}

// can_recall follows the stricter of sent_recall_hours (24 when it is 0) and the connector's own limit, counted from
// the send, to the nanosecond: a message of exactly that age can be taken back, one a nanosecond older cannot. A
// connector that is not available cannot take anything back now.
func TestSentLedgerRowsFollowTheWindow(t *testing.T) {
	const h = time.Hour
	for _, tc := range []struct {
		name  string
		hours int
		limit time.Duration // the connector's
		edge  time.Duration // the oldest message that can be taken back; 0: any age
	}{
		{"the default window", 0, 0, 24 * h},
		{"two days", 48, 0, 48 * h},
		{"an hour, and a connector that allows two days", 1, 48 * h, h},
		{"the default, and a connector that allows two hours", 0, 2 * h, 2 * h},
		{"a connector's limit of a nanosecond over the window", 0, 24*h + time.Nanosecond, 24 * h},
		{"no window", -1, 0, 0},
		{"no window, and a connector that allows two hours", -1, 2 * h, 2 * h},
		{"hours that count as a hundred years", math.MaxInt, 0, 100 * 365 * 24 * h},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newRecallEnv(t)
			e.now = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
			e.h.cfg.SentRecallHours = tc.hours
			e.c.limit = tc.limit
			edge := tc.edge
			if edge == 0 {
				edge = 100 * 365 * 24 * h
			}
			at, past := e.at(edge, "example_send"), e.at(edge+time.Nanosecond, "example_send")
			rows := e.readRows(t)
			if rows[at]["can_recall"] != true {
				t.Errorf("a message of exactly the limit: %v", rows[at])
			}
			if want := tc.edge == 0; rows[past]["can_recall"] != want {
				t.Errorf("a message a nanosecond older: can_recall %v, want %v", rows[past]["can_recall"], want)
			}
		})
	}
	t.Run("a connector that is not available", func(t *testing.T) {
		e := newRecallEnv(t)
		id := e.send(time.Hour)
		e.c.state.Store(connector.StateNotSignedIn)
		if row := e.readRows(t)[id]; row["can_recall"] != false {
			t.Fatalf("%v", row)
		}
	})
}

// What a recall did shows in the ledger at once: the row says when it was taken back and that it cannot be again, and
// the others are as they were.
func TestSentLedgerRowsFollowARecall(t *testing.T) {
	e := newRecallEnv(t)
	taken, other := e.send(0), e.send(0)
	before := e.readRows(t)
	if before[taken]["can_recall"] != true || before[taken]["recalled_at"] != nil {
		t.Fatalf("%v", before[taken])
	}
	if code, out := call(t, e.h.routes(), "POST", recallPath(taken), deviceToken("dev_phone", roleFull), `{"confirm":true}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	after := e.readRows(t)
	if got := after[taken]; got["can_recall"] != false || got["recalled_at"] == nil {
		t.Errorf("the row taken back: %v", got)
	}
	if got := after[other]; got["can_recall"] != true || got["recalled_at"] != nil {
		t.Errorf("the other row: %v", got)
	}
}

// End to end: a comment the real GitHub connector wrote through the gate is taken back by the route against a fake
// GitHub. The ledger offers the recall, the user's device asks for it, GitHub is asked once to delete exactly that
// comment, and the ledger shows it taken back; asking again deletes nothing more.
func TestRecallRouteTakesBackAGitHubComment(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" { // a node's port probe (node/ports.go), not this test's client
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/example-org/example-repo/issues/12/comments":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":3000000001,"html_url":"https://github.com/example-org/example-repo/issues/12#issuecomment-3000000001"}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/example-org/example-repo/issues/comments/3000000001":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	h := newTestManager(t)
	st := newSentLedgerStore()
	h.store = st
	h.connectors = h.newConnectors([]connector.Rule{{Connector: "github", Action: "github_comment", Decision: connector.Allow}})
	gh := github.New(github.Config{Auth: github.AuthConfig{FromMachine: "mac"}, APIRoot: srv.URL}, "Mac", func(context.Context, string) (string, error) {
		return "gho_example000000000000000000000000000001", nil
	})
	if err := h.addConnector(signedInGitHub{gh}); err != nil {
		t.Fatal(err)
	}
	args := json.RawMessage(`{"repo":"example-org/example-repo","number":12,"body":"Looks good."}`)
	if r := h.callConnector(context.Background(), asker{}, "github_comment", args); r.IsError {
		t.Fatalf("write: %+v", r)
	}
	mux := h.routes()
	phone := deviceToken("dev_phone", roleFull)
	read := func() map[string]any {
		t.Helper()
		code, out := call(t, mux, "GET", "/api/v1/connectors/sent", phone, "")
		items, _ := out["items"].([]any)
		if code != 200 || len(items) != 1 {
			t.Fatalf("%d %v", code, out)
		}
		return items[0].(map[string]any)
	}
	row := read()
	id := int64(row["id"].(float64))
	if row["can_recall"] != true || row["message_ref"] != "3000000001" || row["recalled_at"] != nil {
		t.Fatalf("before: %v", row)
	}

	code, out := call(t, mux, "POST", recallPath(id), phone, `{"confirm":true}`)
	if item, _ := out["item"].(map[string]any); code != 200 || item["recalled_at"] == nil || item["can_recall"] != false {
		t.Fatalf("recall: %d %v", code, out)
	}
	if after := read(); after["recalled_at"] == nil || after["can_recall"] != false {
		t.Fatalf("after: %v", after)
	}
	if code, out := call(t, mux, "POST", recallPath(id), phone, `{"confirm":true}`); code != 409 || out["code"] != RecallAlreadyRecalled {
		t.Fatalf("again: %d %v", code, out)
	}
	mu.Lock()
	defer mu.Unlock()
	const token = "Bearer gho_example000000000000000000000000000001"
	want := "POST /repos/example-org/example-repo/issues/12/comments " + token + "\nDELETE /repos/example-org/example-repo/issues/comments/3000000001 " + token
	if strings.Join(seen, "\n") != want {
		t.Fatalf("GitHub saw %v", seen)
	}
}

// The id in the path is a row's: a number from 1. Anything else is a bad request that reaches nothing, and a number
// no row has is a 404.
func TestRecallRouteIdIsARowNumber(t *testing.T) {
	e := newRecallEnv(t)
	mux := e.h.routes()
	phone := deviceToken("dev_phone", roleFull)
	id := e.send(0)
	for _, bad := range []string{"0", "-1", "abc", "1.5", "0x1", "99999999999999999999", "%2e"} {
		if code, out := call(t, mux, "POST", "/api/v1/connectors/sent/"+bad+"/recall", phone, `{"confirm":true}`); code != 400 || out["code"] != "bad_request" {
			t.Errorf("%s: %d %v", bad, code, out)
		}
	}
	if code, out := call(t, mux, "POST", recallPath(id+1), phone, `{"confirm":true}`); code != 404 || out["code"] != RecallNotFound {
		t.Errorf("a row that is not there: %d %v", code, out)
	}
	if e.recalledAt(id) != nil || e.c.askedCount() != 0 {
		t.Fatalf("marked %v, connector asked %d times", e.recalledAt(id), e.c.askedCount())
	}
}

// auditedLedger is the ledger in memory that also keeps the audit (plugin_audit).
type auditedLedger struct {
	*sentLedgerStore
	audit *auditingStore
}

func (a auditedLedger) AddExtensionAudit(ctx context.Context, id, action, actor string, detail json.RawMessage) error {
	return a.audit.AddExtensionAudit(ctx, id, action, actor, detail)
}

// A recall that took a message back is recorded in plugin_audit like a sign-in or a switch: the connector in the
// plugin column, which device asked and which row and kind of message, never
// its text. Nothing is recorded for what did not take anything back (refused by the route or by the connector,
// not asked for, already taken back), and a record that cannot be written does not undo the recall.
func TestRecallRouteIsAudited(t *testing.T) {
	e := newRecallEnv(t)
	aud := newAuditingStore()
	e.h.store = auditedLedger{e.st, aud}
	mux := e.h.routes()
	phone := deviceToken("dev_phone", roleFull)

	refused := e.send(0)
	e.c.recall = func(context.Context, connector.SentMessage) error {
		return errors.New("GitHub DELETE: 403 not allowed")
	}
	for name, tc := range map[string]struct {
		token, body string
		id          int64
		status      int
	}{
		"refused by the connector": {phone, `{"confirm":true}`, refused, 502},
		"without the user's word":  {phone, `{}`, refused, 400},
		"from an answer-only one":  {deviceToken("dev_watch", roleAnswer), `{"confirm":true}`, refused, 403},
		"from the owner":           {e.h.cfg.Token, `{"confirm":true}`, refused, 403},
		"a row that is not there":  {phone, `{"confirm":true}`, 99, 404},
	} {
		if code, out := call(t, mux, "POST", recallPath(tc.id), tc.token, tc.body); code != tc.status {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	e.c.recall = nil
	if rows := aud.take(); len(rows) != 0 {
		t.Fatalf("recorded what took nothing back: %+v", rows)
	}

	byPhone, byOwner := e.send(0), e.send(0)
	// The owner's token cannot take a message back, so what it asks leaves no record of a recall either.
	if code, out := call(t, mux, "POST", recallPath(byOwner), e.h.cfg.Token, `{"confirm":true}`); code != 403 {
		t.Fatalf("the owner: %d %v", code, out)
	}
	if rows := aud.take(); len(rows) != 0 {
		t.Fatalf("recorded what the owner asked: %+v", rows)
	}
	if code, out := call(t, mux, "POST", recallPath(byPhone), phone, `{"confirm":true}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	rows := aud.take()
	if len(rows) != 1 || rows[0].ID != "example" || rows[0].Action != auditConnectorRecall || rows[0].Actor != "device:dev_phone" ||
		len(rows[0].Detail) != 2 || rows[0].Detail["row"] != float64(byPhone) || rows[0].Detail["action"] != "example_send" {
		t.Fatalf("row %d: recorded %+v, want %s by device:dev_phone", byPhone, rows, auditConnectorRecall)
	}
	if code, _ := call(t, mux, "POST", recallPath(byPhone), phone, `{"confirm":true}`); code != 409 {
		t.Fatalf("again: %d", code)
	}
	if rows := aud.take(); len(rows) != 0 {
		t.Fatalf("recorded a second recall of the same row: %+v", rows)
	}

	aud.mu.Lock()
	aud.fail = true
	aud.mu.Unlock()
	away := e.send(0)
	if code, out := call(t, mux, "POST", recallPath(away), phone, `{"confirm":true}`); code != 200 || e.recalledAt(away) == nil {
		t.Fatalf("with the audit away: %d %v, marked %v", code, out, e.recalledAt(away))
	}
}

// What a failure says never carries the message's text or summary: not the route's refusals, not the connector's
// words, not the plain 500. (The row an already_recalled answer brings is the row, by design.)
func TestRecallRouteFailureAnswersDoNotCarryTheText(t *testing.T) {
	const secret = "SECRETTEXT-9f3a"
	for name, tc := range map[string]struct {
		err    error
		status int
	}{
		"refused":     {errors.New("GitHub DELETE: 403 not allowed"), 502},
		"unknown":     {fmt.Errorf("%w: the connection broke", connector.ErrRecallUnknown), 504},
		"unavailable": {fmt.Errorf("%w: offline", connector.ErrRecallUnavailable), 503},
		"cannot":      {fmt.Errorf("%w: no comment id", connector.ErrCannotRecall), 409},
	} {
		t.Run(name, func(t *testing.T) {
			e := newRecallEnv(t)
			e.c.recall = func(context.Context, connector.SentMessage) error { return tc.err }
			id := e.put(db.SentAsUser{Connector: "example", Action: "example_send", MessageRef: "41", Text: secret, Summary: secret})
			code, out := call(t, e.h.routes(), "POST", recallPath(id), deviceToken("dev_phone", roleFull), `{"confirm":true}`)
			if code != tc.status || strings.Contains(fmt.Sprint(out), secret) {
				t.Fatalf("%d %v", code, out)
			}
		})
	}
	for name, tc := range map[string]struct {
		setup  func(e *recallEnv, id int64)
		status int
		body   string
	}{
		"too old":       {func(e *recallEnv, id int64) { e.now = e.now.Add(48 * time.Hour) }, 409, `{"confirm":true}`},
		"not confirmed": {func(e *recallEnv, id int64) {}, 400, `{}`},
		"a ledger that cannot mark the row": {func(e *recallEnv, id int64) {
			fail := &atomic.Bool{}
			fail.Store(true)
			e.h.store = markFailing{e.st, fail}
		}, 500, `{"confirm":true}`},
	} {
		t.Run(name, func(t *testing.T) {
			e := newRecallEnv(t)
			id := e.put(db.SentAsUser{Connector: "example", Action: "example_send", MessageRef: "41", Text: secret, Summary: secret})
			tc.setup(e, id)
			code, out := call(t, e.h.routes(), "POST", recallPath(id), deviceToken("dev_phone", roleFull), tc.body)
			if code != tc.status || strings.Contains(fmt.Sprint(out), secret) {
				t.Fatalf("%d %v", code, out)
			}
		})
	}
}
