package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The workbench (feishu_workbench*.go, feishu_companions.go, #1961): off, the bot is what it was; on, /home pins one
// home card and keeps it the only one, every card gets a way back to it, /start asks for a permission mode, and a
// polling notifications companion takes the notices. All ids are placeholders.

var workbenchOnFeatures = FeishuFeatures{Main: true, Agents: true, Pushes: true, Reports: true, Workbench: true}

// workbenchEnv is the board's env with the workbench's transport calls recorded.
type workbenchEnv struct {
	*boardEnv
	pins      []map[string]any // feishu.pin
	recalls   []map[string]any // feishu.recall
	recallErr error
}

func newWorkbenchEnv(t *testing.T, f FeishuFeatures) *workbenchEnv {
	t.Helper()
	e := &workbenchEnv{boardEnv: newBoardEnv(t)}
	e.f.answerApp(AppFeishu, FeishuPin, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		e.mu.Lock()
		defer e.mu.Unlock()
		e.pins = append(e.pins, m)
		return map[string]any{}, nil
	})
	e.f.answerApp(AppFeishu, FeishuRecall, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		e.mu.Lock()
		defer e.mu.Unlock()
		e.recalls = append(e.recalls, m)
		return map[string]any{}, e.recallErr
	})
	e.setFeatures(f)
	_, _ = e.s.feishuFeatures(context.Background())
	return e
}

func (e *workbenchEnv) command(cmd string) (bool, string) {
	e.t.Helper()
	f, _ := e.s.feishuFeatures(context.Background())
	return e.s.botCommand(context.Background(), f, "om_cmd", cmd, "", "")
}

func (e *workbenchEnv) postJSON(i int) string {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if i < 0 {
		i += len(e.posts)
	}
	if i < 0 || i >= len(e.posts) {
		e.t.Fatalf("no post %d of %d", i, len(e.posts))
	}
	s, _ := e.posts[i]["card"].(string)
	return s
}

func (e *workbenchEnv) counts() (posts, pins, recalls, updates int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.posts), len(e.pins), len(e.recalls), len(e.updates)
}

// Off (the default), nothing of it exists: the commands go on as text, /start has no permission field, no card gets a
// workbench button and /help does not mention it.
func TestWorkbenchOffChangesNothing(t *testing.T) {
	e := newWorkbenchEnv(t, FeishuFeatures{Main: true, Agents: true})
	for _, cmd := range []string{"/home", "/today", "/inbox", "/工作台"} {
		if h, rest := e.command(cmd); h || rest != cmd {
			t.Fatalf("%s with the workbench off: handled %v, %q", cmd, h, rest)
		}
	}
	if js := e.s.startAgentCard(context.Background(), "").JSON(); strings.Contains(js, `"name":"mode"`) {
		t.Fatalf("/start has a permission field with the workbench off: %s", js)
	}
	if _, err := e.s.botPost(context.Background(), "", feishubot.NewCard("t", feishubot.Blue, "t").Markdown("hello"), ""); err != nil {
		t.Fatal(err)
	}
	if js := e.postJSON(-1); strings.Contains(js, `"to":"home"`) {
		t.Fatalf("a card got the workbench button with the workbench off: %s", js)
	}
	l := e.s.botLang(context.Background())
	if strings.Contains(botHelp(l, FeishuFeatures{Main: true, Agents: true}), "/home") {
		t.Fatal("/help mentions /home with the workbench off")
	}
	if _, pins, recalls, _ := e.counts(); pins+recalls != 0 {
		t.Fatalf("pins %d, recalls %d with the workbench off", pins, recalls)
	}
}

// /home posts the home card, pins it as the bot's only pin and makes it the board; the next /home posts a fresh one,
// pins it and withdraws the one before. One too old to withdraw is redrawn to say where the workbench went.
func TestWorkbenchHomeIsPinnedAndTheOnlyOne(t *testing.T) {
	e := newWorkbenchEnv(t, workbenchOnFeatures)
	l := e.s.botLang(context.Background())
	if h, _ := e.command("/home"); !h {
		t.Fatal("/home not handled with the workbench on")
	}
	if js := e.postJSON(0); !strings.Contains(js, wbT(l, "home_title")) || !strings.Contains(js, `"to":"start"`) {
		t.Fatalf("the home card: %s", js)
	}
	e.mu.Lock()
	first := fmt.Sprint(e.pins[0]["message_id"])
	exclusive := e.pins[0]["exclusive"]
	e.mu.Unlock()
	if first != "om_board_1" || exclusive != true {
		t.Fatalf("pin %v, exclusive %v", first, exclusive)
	}
	if ref := e.s.boardRef(context.Background()); ref == nil || ref.MessageID != "om_board_1" {
		t.Fatalf("the board is %+v", ref)
	}

	e.command("/home")
	posts, pins, recalls, _ := e.counts()
	e.mu.Lock()
	gone := fmt.Sprint(e.recalls[0]["message_id"])
	e.mu.Unlock()
	if posts != 2 || pins != 2 || recalls != 1 || gone != "om_board_1" {
		t.Fatalf("second /home: posts %d, pins %d, recalls %d (%s)", posts, pins, recalls, gone)
	}
	if ref := e.s.boardRef(context.Background()); ref.MessageID != "om_board_2" {
		t.Fatalf("the board is %+v", ref)
	}

	e.mu.Lock()
	e.recallErr = errors.New("too old to withdraw")
	e.mu.Unlock()
	e.command("/home")
	e.mu.Lock()
	defer e.mu.Unlock()
	last := e.updates[len(e.updates)-1]
	if last["message_id"] != "om_board_2" || !strings.Contains(fmt.Sprint(last["card"]), wbT(l, "desk_moved_title")) {
		t.Fatalf("the old workbench was not redrawn: %v", last)
	}
}

// With the workbench on, a card the bot posts gets the workbench button (once), the home card itself none; switched
// off, the next card has none.
func TestWorkbenchButtonOnEveryCard(t *testing.T) {
	e := newWorkbenchEnv(t, workbenchOnFeatures)
	e.command("/home")
	if js := e.postJSON(0); strings.Contains(js, `"to":"home"`) {
		t.Fatalf("the home card links to itself: %s", js)
	}
	card := feishubot.NewCard("t", feishubot.Blue, "t").Markdown("an answer")
	if _, err := e.s.botPost(context.Background(), "", card, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.botPost(context.Background(), "", card, ""); err != nil { // the same card again (a retry)
		t.Fatal(err)
	}
	if js := e.postJSON(-1); strings.Count(js, `"to":"home"`) != 1 {
		t.Fatalf("the workbench button on a card: %s", js)
	}
	e.setFeatures(FeishuFeatures{Main: true, Agents: true})
	_, _ = e.s.feishuFeatures(context.Background())
	if _, err := e.s.botPost(context.Background(), "", feishubot.NewCard("t", feishubot.Blue, "t").Markdown("later"), ""); err != nil {
		t.Fatal(err)
	}
	if js := e.postJSON(-1); strings.Contains(js, `"to":"home"`) {
		t.Fatalf("the workbench button after switching it off: %s", js)
	}
}

// /start asks for a permission mode before what to do, offering every mode the agents take; the mode picked goes to
// the agent's start, one that is not a mode does not.
func TestWorkbenchStartAsksForAPermissionMode(t *testing.T) {
	e := newWorkbenchEnv(t, workbenchOnFeatures)
	js := e.s.startAgentCard(context.Background(), "").JSON()
	if !strings.Contains(js, `"name":"mode"`) || !strings.Contains(js, `"bypassPermissions"`) || !strings.Contains(js, `"acceptEdits"`) {
		t.Fatalf("the /start form: %s", js)
	}
	if strings.Index(js, `"name":"mode"`) > strings.Index(js, `"name":"prompt"`) {
		t.Fatalf("the permission field comes after what to do: %s", js)
	}

	f := newFakeHub().withMachine()
	f.sessions["box"] = nil
	f.devices = []Device{{ID: "box", Name: "box", Home: "/home/me", Online: true, Reachable: true}}
	s := newTestService(t, f)
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", Features: workbenchOnFeatures}, nil
	})
	f.answerApp(AppFeishu, FeishuText, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) { return map[string]any{"message_id": "om_started"}, nil })
	for i, c := range []struct{ mode, want string }{{"bypassPermissions", "bypassPermissions"}, {"everything", ""}, {"", ""}} {
		if err := s.botStartAgent("box", "/code", "", c.mode); err != nil {
			t.Fatalf("mode %q: %v", c.mode, err)
		}
		f.mu.Lock()
		if len(f.starts) != i+1 {
			f.mu.Unlock()
			t.Fatalf("mode %q: %d starts", c.mode, len(f.starts))
		}
		got, _ := f.starts[i]["permission_mode"].(string)
		f.mu.Unlock()
		if got != c.want {
			t.Fatalf("mode %q: permission_mode %q, want %q", c.mode, got, c.want)
		}
	}
}

// /help with the workbench on names its commands.
func TestWorkbenchHelpNamesItsCommands(t *testing.T) {
	l := i18n.Lang("en")
	if h := botHelp(l, workbenchOnFeatures); !strings.Contains(h, "/home") || !strings.Contains(h, "/inbox") {
		t.Fatalf("help: %s", h)
	}
}

// companionsCall is one of the companions' routes, as a device with every right (the hub relays the extension's).
func (e *workbenchEnv) companionsCall(method, path, body string) (int, map[string]any) {
	e.t.Helper()
	rec := httptest.NewRecorder()
	e.s.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

const companionsBody = `{"bots":[{"kind":"inbox","name":"Inbox","icon":"📥","app_id":"cli_inboxbot01"},{"kind":"notify","name":"Notices","icon":"🔔","app_id":"cli_noticebot1"}]}`

// The companions are checked and kept; the home card links to them, and the Inbox companion's link replaces the
// /inbox button. A read-only device cannot change them.
func TestWorkbenchCompanionsAreLinkedFromTheHomeCard(t *testing.T) {
	e := newWorkbenchEnv(t, workbenchOnFeatures)
	if code, _ := e.companionsCall("PUT", "/api/v1/assistant/feishu/companions", `{"bots":[{"kind":"inbox","name":"Inbox","app_id":"not an app"}]}`); code != http.StatusBadRequest {
		t.Fatalf("a bad app id: %d", code)
	}
	if code, _ := e.companionsCall("PUT", "/api/v1/assistant/feishu/companions", `{"bots":[{"kind":"robot","name":"X","app_id":"cli_inboxbot01"}]}`); code != http.StatusBadRequest {
		t.Fatalf("an unknown kind: %d", code)
	}
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("PUT", "/api/v1/assistant/feishu/companions", strings.NewReader(companionsBody))
	r.Header.Set(RoleHeader, "read")
	e.s.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a read-only device: %d", rec.Code)
	}
	if code, out := e.companionsCall("PUT", "/api/v1/assistant/feishu/companions", companionsBody); code != http.StatusOK {
		t.Fatalf("companions: %d %v", code, out)
	}
	card, _, _ := e.s.homeCard(context.Background(), workbenchOnFeatures)
	js := card.JSON()
	if !strings.Contains(js, "appId=cli_inboxbot01") || !strings.Contains(js, "appId=cli_noticebot1") || strings.Contains(js, `"to":"inbox"`) {
		t.Fatalf("the home card's companions: %s", js)
	}
}

// A push goes to the notifications companion only while it polls and the workbench is on; otherwise to the chat.
// A press it relays is taken only when it needs no owner's action.
func TestWorkbenchNoticesGoToAPollingCompanion(t *testing.T) {
	e := newWorkbenchEnv(t, workbenchOnFeatures)
	e.companionsCall("PUT", "/api/v1/assistant/feishu/companions", companionsBody)

	before, _, _, _ := e.counts()
	e.s.feishuPush("", "", "Not polled yet", "nobody takes it")
	if after, _, _, _ := e.counts(); after != before+1 {
		t.Fatalf("a push with no companion polling did not go to the chat: %d → %d posts", before, after)
	}

	code, out := e.companionsCall("GET", "/api/v1/assistant/feishu/notices?wait=0", "")
	boot, _ := out["boot"].(string)
	if code != http.StatusOK || boot == "" {
		t.Fatalf("poll: %d %v", code, out)
	}
	before, _, _, _ = e.counts()
	e.s.feishuPush("", "", "Morning brief", "the day ahead")
	if after, _, _, _ := e.counts(); after != before {
		t.Fatalf("a push went to the chat while the companion polls")
	}
	_, out = e.companionsCall("GET", "/api/v1/assistant/feishu/notices?wait=0&after=0&boot="+boot, "")
	list, _ := out["notices"].([]any)
	if len(list) != 1 || !strings.Contains(fmt.Sprint(list[0]), "Morning brief") {
		t.Fatalf("notices: %v", out)
	}

	// A long poll waits for the next one.
	got := make(chan map[string]any, 1)
	go func() {
		_, out := e.companionsCall("GET", "/api/v1/assistant/feishu/notices?wait=5&after=1&boot="+boot, "")
		got <- out
	}()
	waitFor(t, "the long poll waiting", func() bool {
		e.s.companions.mu.Lock()
		defer e.s.companions.mu.Unlock()
		return e.s.companions.wait != nil
	})
	e.s.feishuPush("", "", "Second", "later")
	select {
	case out := <-got:
		if list, _ := out["notices"].([]any); len(list) != 1 || !strings.Contains(fmt.Sprint(list[0]), "Second") {
			t.Fatalf("the long poll: %v", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the long poll did not end on a notice")
	}

	l := e.s.botLang(context.Background())
	if _, out := e.companionsCall("POST", "/api/v1/assistant/feishu/notices/action", `{"action":{"value":{"a":"ix","id":"ix_1"}}}`); !strings.Contains(fmt.Sprint(out), wbT(l, "answer_in_workbench")) {
		t.Fatalf("a question answered through the companion: %v", out)
	}

	// Switched off, pushes go to the chat again.
	e.setFeatures(FeishuFeatures{Main: true, Pushes: true})
	_, _ = e.s.feishuFeatures(context.Background())
	before, _, _, _ = e.counts()
	e.s.feishuPush("", "", "Off again", "to the chat")
	if after, _, _, _ := e.counts(); after != before+1 {
		t.Fatal("a push with the workbench off did not go to the chat")
	}
}

// /inbox lists what needs the owner first, each with Done and (for a reply that can be sent) AI draft; Done files it
// and the card is redrawn without it.
func TestWorkbenchInboxCardFilesAnItem(t *testing.T) {
	e := newWorkbenchEnv(t, workbenchOnFeatures)
	now := time.Now()
	e.s.inbox.mu.Lock()
	if e.s.inbox.items == nil {
		e.s.inbox.items = map[string]*InboxItem{}
	}
	e.s.inbox.items["lark:f1:1"] = &InboxItem{ID: "lark:f1:1", Kind: KindDM, Tier: TierAction, Sender: "Alex", Summary: "asks whether the release is out",
		UpdatedAt: now.Add(-time.Hour), Lark: &InboxLark{FeedID: "f1"}}
	e.s.inbox.items["lark:f2:1"] = &InboxItem{ID: "lark:f2:1", Kind: KindDM, Tier: TierFYI, Sender: "Sam", Summary: "shared the slides",
		UpdatedAt: now, Lark: &InboxLark{FeedID: "f2"}}
	e.s.inbox.mu.Unlock()

	card, _ := e.s.inboxCard(context.Background())
	js := card.JSON()
	if a, b := strings.Index(js, "Alex"), strings.Index(js, "Sam"); a < 0 || b < 0 || a > b {
		t.Fatalf("what needs the owner is not first: %s", js)
	}
	if !strings.Contains(js, `"op":"draft"`) || !strings.Contains(js, `"op":"done"`) {
		t.Fatalf("the Inbox card's buttons: %s", js)
	}
	var a feishubot.CardAction
	a.Action.Value = map[string]any{"a": "ib", "op": "done", "id": "lark:f1:1"}
	a.Context.MessageID = "om_inbox"
	resp := e.s.botCardAction(context.Background(), &a, "")
	if resp == nil || resp.Card == nil {
		t.Fatalf("Done: %+v", resp)
	}
	redrawn, _ := json.Marshal(resp.Card.Data)
	if strings.Contains(string(redrawn), "Alex") || !strings.Contains(string(redrawn), "Sam") {
		t.Fatalf("the redrawn card: %s", redrawn)
	}
}
