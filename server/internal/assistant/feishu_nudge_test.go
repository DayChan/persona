package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The nudge under a card nobody answered (feishu_nudge.go, #6084). All ids are placeholders.

// nudgeRig is a manager with the Lark bot paired, one computer "mac" with the agent "s1", and a clock the tests move.
// service() makes another manager on the same hub, database and computer: a restart.
type nudgeRig struct {
	t   *testing.T
	f   *fakeHub
	d   *fleetDevs
	st  *memStore
	s   *Service
	now time.Time // the bot's clock

	mu         sync.Mutex
	after      *int // nudge_after as the hub's status says (nil: it says none)
	quiet      bool
	sendErr    error // what feishu.text fails with
	failures   int   // how many sends fail with sendErr before they go through (0: every one, while sendErr is set)
	texts      []map[string]any
	onSessions func() // see hookDevs
	keys       []any  // the uuid of every send tried, failed or not
}

func newNudgeRig(t *testing.T) *nudgeRig {
	t.Helper()
	r := &nudgeRig{t: t, f: newFakeHub(), d: newFleetDevs(), st: &memStore{}, now: time.Unix(1_800_000_000, 0).UTC()}
	r.d.add("mac", "Mac")
	r.d.setSessions("mac", []proto.Session{{SessionID: "s1", Title: "release"}})
	r.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_owner", PairedAt: time.Unix(100, 0),
			Features: FeishuFeatures{Main: true, Cards: true}, NudgeAfter: r.after}, nil
	})
	r.f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		var tried map[string]any
		_ = json.Unmarshal(p, &tried)
		r.keys = append(r.keys, tried["uuid"])
		if err := r.sendErr; err != nil {
			if r.failures > 0 {
				if r.failures--; r.failures == 0 {
					r.sendErr = nil // the last failure
				}
			}
			return nil, err
		}
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		r.texts = append(r.texts, m)
		return map[string]any{"message_id": "om_nudge"}, nil
	})
	r.f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	r.f.answerApp(AppNotify, NotifyQuietGet, func(json.RawMessage) (any, error) {
		r.mu.Lock()
		defer r.mu.Unlock()
		return map[string]bool{"quiet": r.quiet}, nil
	})
	r.s = r.service()
	return r
}

// hookDevs is the computers, running r.onSessions (once) when a session list is read: a moment in a sweep the test can
// put an answer in.
type hookDevs struct {
	*fleetDevs
	r *nudgeRig
}

func (h hookDevs) Sessions(id string) ([]proto.Session, bool) {
	if f := h.r.onSessions; f != nil {
		h.r.onSessions = nil
		f()
	}
	return h.fleetDevs.Sessions(id)
}

func (h hookDevs) SessionsWhere(id string, keep func(*proto.Session) bool) ([]proto.Session, bool) {
	list, ok := h.Sessions(id)
	return keptWhere(list, keep), ok
}

// service is a manager on r's hub, database and computer.
func (r *nudgeRig) service() *Service {
	s := withSettings(r.t, r.f, New(Options{Hub: r.f, Store: func() Store { return r.st }, Logf: testLogf(r.t), Devices: hookDevs{r.d, r}}))
	s.fleet.interactions = map[string]*pending{}
	s.fbot.now = func() time.Time { return r.now }
	return s
}

func (r *nudgeRig) setAfter(minutes int) {
	r.mu.Lock()
	r.after = &minutes
	r.mu.Unlock()
}

func (r *nudgeRig) setQuiet(on bool) {
	r.mu.Lock()
	r.quiet = on
	r.mu.Unlock()
}

// sent are the nudges that went out.
func (r *nudgeRig) sent() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.texts...)
}

// ask has s wait on a question and its Lark card: in the owner's DM, or in the topic of group chat (chat != "").
func (r *nudgeRig) ask(s *Service, id, chat string) proto.Interaction {
	r.t.Helper()
	ix := proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "Push it to beta?"}}}
	return r.askWith(s, ix, chat)
}

func (r *nudgeRig) askWith(s *Service, ix proto.Interaction, chat string) proto.Interaction {
	r.t.Helper()
	s.fleet.mu.Lock()
	s.fleet.interactions[ix.ID] = &pending{ix: ix}
	s.fleet.mu.Unlock()
	thread := "om_run"
	b := &s.fbot
	b.mu.Lock()
	s.keepBotCardLocked(ix.ID, &botCard{MessageID: "om_" + ix.ID, Thread: thread, ChatID: chat, Ix: ix, At: r.now})
	b.mu.Unlock()
	return ix
}

// tick moves the clock on by d and runs the sweep of s.
func (r *nudgeRig) tick(s *Service, d time.Duration) {
	r.t.Helper()
	r.now = r.now.Add(d)
	s.feishuForgetStatus() // the status is kept a few seconds of real time
	s.nudgeTick(context.Background())
}

func (r *nudgeRig) cardOf(s *Service, id string) (botCard, bool) {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(context.Background())
	c, ok := b.cards[id]
	return c, ok
}

func wantNudges(t *testing.T, r *nudgeRig, want int, when string) []map[string]any {
	t.Helper()
	got := r.sent()
	if len(got) != want {
		t.Fatalf("%s: %d nudges went out, want %d: %v", when, len(got), want, got)
	}
	return got
}

// A card unanswered for nudge_after gets one reply under it (default 60 minutes, the status says none), a card
// answered before that gets nothing, and it is never nudged twice.
func TestNudgeOnceAfterTheWait(t *testing.T) {
	r := newNudgeRig(t)
	r.ask(r.s, "ix_a", "")
	r.tick(r.s, 59*time.Minute)
	wantNudges(t, r, 0, "after 59 minutes")
	r.tick(r.s, 2*time.Minute)
	got := wantNudges(t, r, 1, "after 61 minutes")
	n := got[0]
	if n["reply_to"] != "om_ix_a" {
		t.Errorf("the nudge replies to %v, want the card", n["reply_to"])
	}
	line := i18n.T(r.s.botLang(context.Background()), "assistant-feishu_cards.nudge", i18n.Args{"what": "Push it to beta?"})
	if n["text"] != line {
		t.Errorf("text = %q, want %q", n["text"], line)
	}
	if n["no_detour"] != true {
		t.Errorf("a nudge for a card that was withdrawn must not land in the chat on its own: %v", n)
	}
	if k, _ := n["uuid"].(string); k == "" || len(k) > 46 || strings.ContainsAny(k, " .") {
		t.Errorf("uuid = %q", k)
	}
	if c, _ := r.cardOf(r.s, "ix_a"); c.NudgedAt.IsZero() {
		t.Error("the card does not record it was nudged")
	}
	r.tick(r.s, 30*time.Minute)
	r.tick(r.s, 5*time.Hour)
	wantNudges(t, r, 1, "later sweeps")
}

// nudge_after is the owner's: 30 minutes nudges at 31.
func TestNudgeAfterIsTheSetting(t *testing.T) {
	r := newNudgeRig(t)
	r.setAfter(30)
	r.ask(r.s, "ix_a", "")
	r.tick(r.s, 29*time.Minute)
	wantNudges(t, r, 0, "after 29 minutes")
	r.tick(r.s, 2*time.Minute)
	wantNudges(t, r, 1, "after 31 minutes")
}

// In the owner's private chat a reply notifies by itself, so it does not @; in a topic group it @-mentions the owner
// and stays in the topic.
func TestNudgeMentionsTheOwnerOnlyInATopicGroup(t *testing.T) {
	r := newNudgeRig(t)
	r.ask(r.s, "ix_dm", "")
	r.ask(r.s, "ix_topic", "oc_group")
	r.tick(r.s, 61*time.Minute)
	got := wantNudges(t, r, 2, "after 61 minutes")
	for _, n := range got {
		text, _ := n["text"].(string)
		switch n["reply_to"] {
		case "om_ix_dm":
			if strings.Contains(text, "<at") {
				t.Errorf("a private nudge mentions: %q", text)
			}
			if _, has := n["chat_id"]; has {
				t.Errorf("a private nudge names a chat: %v", n)
			}
			if _, has := n["in_thread"]; has {
				t.Errorf("a private nudge is in a thread: %v", n)
			}
		case "om_ix_topic":
			if !strings.HasPrefix(text, `<at user_id="ou_owner"></at> `) || !strings.Contains(text, "Push it to beta?") {
				t.Errorf("a topic nudge = %q", text)
			}
			if n["chat_id"] != "oc_group" || n["in_thread"] != true || n["no_detour"] != true {
				t.Errorf("a topic nudge goes elsewhere: %v", n)
			}
		default:
			t.Errorf("a nudge under %v", n["reply_to"])
		}
	}
}

// A card that was answered (anywhere), dismissed, expired or withdrawn is not nudged; neither is one whose agent is
// gone, or that is no longer pending.
func TestNudgeSkipsCardsThatAreOver(t *testing.T) {
	r := newNudgeRig(t)
	r.ask(r.s, "ix_answered", "")
	r.ask(r.s, "ix_goneagent", "")
	r.ask(r.s, "ix_notpending", "")
	r.ask(r.s, "ix_waiting", "")
	r.tick(r.s, 30*time.Minute)
	// Answered: the card is closed and forgotten (feishuInteractionDone: answered, dismissed, expired and withdrawn all end there).
	r.s.feishuInteractionDone(proto.Interaction{ID: "ix_answered", MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion}, "answered")
	// The agent went away while its interaction was still queued.
	r.s.fleet.mu.Lock()
	p := r.s.fleet.interactions["ix_goneagent"]
	p.ix.SessionID = "gone"
	r.s.fleet.mu.Unlock()
	// No longer pending, not yet cleaned from the chat's record.
	r.s.fleet.mu.Lock()
	delete(r.s.fleet.interactions, "ix_notpending")
	r.s.fleet.mu.Unlock()
	r.tick(r.s, 31*time.Minute)
	got := wantNudges(t, r, 1, "after 61 minutes")
	if got[0]["reply_to"] != "om_ix_waiting" {
		t.Fatalf("the nudge went under %v, want the one card still waiting", got[0]["reply_to"])
	}
}

// Answered after the nudge is due but before the sweep gets to it: nothing.
func TestNudgeNotSentForACardAnsweredBeforeTheSweep(t *testing.T) {
	r := newNudgeRig(t)
	ix := r.ask(r.s, "ix_a", "")
	r.now = r.now.Add(59 * time.Minute)
	r.s.feishuInteractionDone(ix, "answered")
	r.tick(r.s, 2*time.Minute)
	wantNudges(t, r, 0, "a card answered before it was due")
	if _, ok := r.cardOf(r.s, "ix_a"); ok {
		t.Error("an answered card is still kept")
	}
}

// nudge_after 0 turns nudges off. Turned on again, what is already due is let go, not nudged all at once; a card
// asked after that is nudged after its wait.
func TestNudgeOffAtZero(t *testing.T) {
	r := newNudgeRig(t)
	r.setAfter(0)
	r.ask(r.s, "ix_a", "")
	r.tick(r.s, 61*time.Minute)
	r.tick(r.s, 3*time.Hour)
	wantNudges(t, r, 0, "off")
	r.setAfter(60)
	r.tick(r.s, time.Minute)
	wantNudges(t, r, 0, "just turned on, the old card is let go")
	if c, _ := r.cardOf(r.s, "ix_a"); c.SkippedAt.IsZero() || !c.NudgedAt.IsZero() || r.s.cardNudged("ix_a") {
		t.Errorf("a card let go is recorded as %+v and shown as nudged: %v", c, r.s.cardNudged("ix_a"))
	}
	r.ask(r.s, "ix_b", "")
	r.tick(r.s, 59*time.Minute)
	wantNudges(t, r, 0, "a new card, not yet due")
	r.tick(r.s, 2*time.Minute)
	got := wantNudges(t, r, 1, "a new card, due")
	if got[0]["reply_to"] != "om_ix_b" {
		t.Errorf("nudged %v", got[0]["reply_to"])
	}
}

// Nudges off from the start (never switched on in this process) are off too, and a hub that does not say is the default.
func TestNudgeOffFromTheStart(t *testing.T) {
	r := newNudgeRig(t)
	r.setAfter(0)
	r.ask(r.s, "ix_a", "")
	r.tick(r.s, 2*time.Hour)
	wantNudges(t, r, 0, "off from the start")
	if n := nudgeAfterOf(FeishuStatus{}); n != time.Hour {
		t.Errorf("no setting = %v, want 1h", n)
	}
	zero := 0
	if n := nudgeAfterOf(FeishuStatus{NudgeAfter: &zero}); n != 0 {
		t.Errorf("0 = %v, want off", n)
	}
}

// A nudge due in quiet hours waits for their end and goes out once, in the first sweep after; a card answered
// meanwhile gets none.
func TestNudgeWaitsOutQuietHours(t *testing.T) {
	r := newNudgeRig(t)
	r.ask(r.s, "ix_late", "")
	ix := r.ask(r.s, "ix_answered", "")
	r.setQuiet(true)
	r.tick(r.s, 61*time.Minute)
	wantNudges(t, r, 0, "due in quiet hours")
	r.tick(r.s, 5*time.Hour)
	wantNudges(t, r, 0, "still quiet")
	r.s.feishuInteractionDone(ix, "answered") // answered in the meantime
	r.setQuiet(false)
	r.tick(r.s, time.Minute)
	got := wantNudges(t, r, 1, "the first sweep after quiet hours")
	if got[0]["reply_to"] != "om_ix_late" {
		t.Errorf("nudged %v", got[0]["reply_to"])
	}
	r.tick(r.s, time.Minute)
	r.tick(r.s, time.Hour)
	wantNudges(t, r, 1, "once")
}

// Not knowing whether it is quiet (the hub does not answer) holds the nudge back rather than risking a night-time one.
func TestNudgeHeldWhenQuietHoursCannotBeRead(t *testing.T) {
	r := newNudgeRig(t)
	r.f.answerApp(AppNotify, NotifyQuietGet, func(json.RawMessage) (any, error) { return nil, errors.New("hub busy") })
	r.ask(r.s, "ix_a", "")
	r.tick(r.s, 61*time.Minute)
	wantNudges(t, r, 0, "quiet hours unknown")
	r.f.answerApp(AppNotify, NotifyQuietGet, func(json.RawMessage) (any, error) { return map[string]bool{"quiet": false}, nil })
	r.tick(r.s, time.Minute)
	wantNudges(t, r, 1, "the hub answers")
}

// A restart does not nudge a card a second time, and one asked before the restart is nudged once, whatever the
// restarted manager knows of the card (the record keeps only where it is, never what it asks).
func TestNudgeNotRepeatedAfterARestart(t *testing.T) {
	r := newNudgeRig(t)
	ix := r.ask(r.s, "ix_nudged", "")
	r.ask(r.s, "ix_later", "")
	r.tick(r.s, 61*time.Minute) // both due: both nudged
	wantNudges(t, r, 2, "before the restart")

	s2 := r.service() // restart: the card's record is read back from the database
	r.askWithoutCard(s2, ix)
	r.askWithoutCard(s2, proto.Interaction{ID: "ix_later", MachineID: "mac", SessionID: "s1", Kind: proto.IxQuestion})
	r.tick(s2, 10*time.Minute)
	r.tick(s2, 3*time.Hour)
	wantNudges(t, r, 2, "after the restart")
	if c, ok := r.cardOf(s2, "ix_nudged"); !ok || c.NudgedAt.IsZero() {
		t.Errorf("the restarted manager does not know the card was nudged: %+v (kept %v)", c, ok)
	}
}

// A restart between a card and its nudge does not lose the nudge, and the line still says what it asks (the manager
// asks the hub for the card again).
func TestNudgeAfterARestartBeforeItIsDue(t *testing.T) {
	r := newNudgeRig(t)
	ix := r.ask(r.s, "ix_a", "oc_group")
	r.tick(r.s, 20*time.Minute)
	s2 := r.service()
	r.askWithoutCard(s2, ix)
	r.tick(s2, 41*time.Minute)
	got := wantNudges(t, r, 1, "after the restart")
	if text, _ := got[0]["text"].(string); !strings.Contains(text, "Push it to beta?") || !strings.Contains(text, "<at user_id=") {
		t.Errorf("text = %q", text)
	}
	if got[0]["chat_id"] != "oc_group" || got[0]["in_thread"] != true {
		t.Errorf("the topic is not remembered across a restart: %v", got[0])
	}
}

// askWithoutCard puts ix back among the pending ones of s, whose card is only in the database (a restart).
func (r *nudgeRig) askWithoutCard(s *Service, ix proto.Interaction) {
	s.fleet.mu.Lock()
	s.fleet.interactions[ix.ID] = &pending{ix: ix}
	s.fleet.mu.Unlock()
}

// A permission card's nudge says which tool, never the command it would run.
func TestNudgeOfAPermissionNamesNoCommand(t *testing.T) {
	r := newNudgeRig(t)
	r.askWith(r.s, proto.Interaction{ID: "ix_p", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, ToolName: "Bash",
		ToolInput: json.RawMessage(`{"command":"rm -rf /home/someone/secret-project"}`), Reason: "clean up the build"}, "")
	r.tick(r.s, 61*time.Minute)
	got := wantNudges(t, r, 1, "a permission")
	text, _ := got[0]["text"].(string)
	if !strings.Contains(text, "Bash") || strings.Contains(text, "rm -rf") || strings.Contains(text, "secret") || strings.Contains(text, "clean up") {
		t.Errorf("text = %q", text)
	}
}

// The line is one line, in the user's language: a long multi-line plan is cut to its first line.
func TestNudgeLineIsOneShortLine(t *testing.T) {
	r := newNudgeRig(t)
	ctx := context.Background()
	long := strings.Repeat("x", 300)
	for _, c := range []struct {
		name string
		ix   proto.Interaction
		want []string
	}{
		{"plan", proto.Interaction{Kind: proto.IxPlan, Plan: "1. Change the login\n2. Test it\n3. Ship"}, []string{"1. Change the login"}},
		{"question cut", proto.Interaction{Kind: proto.IxQuestion, Questions: []proto.Question{{Question: long}}}, nil},
		{"nothing to say", proto.Interaction{Kind: proto.IxQuestion}, nil},
		{"permission without a tool", proto.Interaction{Kind: proto.IxPermission}, nil},
	} {
		text := r.s.nudgeText(ctx, c.ix)
		if strings.Contains(text, "\n") || len([]rune(text)) > 140 || text == "" {
			t.Errorf("%s: %q", c.name, text)
		}
		for _, w := range c.want {
			if !strings.Contains(text, w) || strings.Contains(text, "Test it") {
				t.Errorf("%s: %q", c.name, text)
			}
		}
	}
	// All three languages have all three lines.
	for _, l := range []i18n.Lang{"en", "zh-Hans", "ja"} {
		for _, key := range []string{"nudge", "nudge_permission", "nudge_bare"} {
			got := i18n.T(l, "assistant-feishu_cards."+key, i18n.Args{"what": "W", "tool": "T"})
			if got == "" || got == "assistant-feishu_cards."+key {
				t.Errorf("%s has no %s", l, key)
			}
		}
	}
}

// A send that failed for a reason that may pass is tried again in the next sweep, under the same key, and still
// nudges once; a refusal (the card is gone) is not tried again.
func TestNudgeRetriesAFailedSendOnceItGoesOut(t *testing.T) {
	r := newNudgeRig(t)
	r.ask(r.s, "ix_a", "")
	r.mu.Lock()
	r.sendErr, r.failures = errors.New("feishu send: HTTP 502"), 2
	r.mu.Unlock()
	r.tick(r.s, 61*time.Minute)
	wantNudges(t, r, 0, "the send failed")
	if c, _ := r.cardOf(r.s, "ix_a"); !c.NudgedAt.IsZero() {
		t.Fatal("a nudge that did not go out is recorded as sent")
	}
	r.tick(r.s, time.Minute)
	wantNudges(t, r, 0, "the send failed again")
	r.tick(r.s, time.Minute)
	wantNudges(t, r, 1, "the third try")
	r.tick(r.s, time.Minute)
	wantNudges(t, r, 1, "after it went out")
	r.mu.Lock()
	keys := append([]any(nil), r.keys...)
	r.mu.Unlock()
	if len(keys) != 3 || keys[0] == nil || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Errorf("the tries went out under keys %v: a retry under a new key could land twice", keys)
	}

	for name, err := range map[string]error{
		"the chat refuses": &rpc.Error{Code: CodeChatDenied, Message: "chat denied"},
		"card withdrawn":   errors.New("feishu reply: 230011 The message has been withdrawn"),
	} {
		r := newNudgeRig(t)
		r.ask(r.s, "ix_a", "oc_group")
		r.mu.Lock()
		r.sendErr = err
		r.mu.Unlock()
		r.tick(r.s, 61*time.Minute)
		r.mu.Lock()
		r.sendErr = nil
		r.mu.Unlock()
		r.tick(r.s, 5*time.Minute)
		if got := r.sent(); len(got) != 0 {
			t.Errorf("%s: tried again: %v", name, got)
		}
	}
}

// A card left more than a day is let go.
func TestNudgeLetsGoOfAnOldCard(t *testing.T) {
	r := newNudgeRig(t)
	r.ask(r.s, "ix_a", "")
	r.now = r.now.Add(25 * time.Hour)
	r.tick(r.s, time.Minute)
	wantNudges(t, r, 0, "a day old")
}

// Not paired or not connected: nothing to nudge with.
func TestNudgeNeedsABotThatCanSend(t *testing.T) {
	r := newNudgeRig(t)
	r.ask(r.s, "ix_a", "")
	r.f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: false, Enabled: true, Owner: "ou_owner"}, nil
	})
	r.tick(r.s, 2*time.Hour)
	wantNudges(t, r, 0, "bot not ready")
}

// nudge_after up to six days works: a card is not let go before the wait is over, and a day after it is.
func TestNudgeLongWaitIsStillNudged(t *testing.T) {
	r := newNudgeRig(t)
	r.setAfter(2880) // 48 hours
	r.ask(r.s, "ix_a", "")
	r.tick(r.s, 47*time.Hour)
	wantNudges(t, r, 0, "after 47 hours")
	r.tick(r.s, 2*time.Hour)
	wantNudges(t, r, 1, "after 49 hours")
	r.ask(r.s, "ix_b", "")
	r.tick(r.s, 73*time.Hour) // 48 + 24 hours and some: left too long
	wantNudges(t, r, 1, "a card left a day past its wait")
}

// With no card kept the sweep does not even ask the hub for the bot's status.
func TestNudgeCostsNothingWithoutCards(t *testing.T) {
	r := newNudgeRig(t)
	r.tick(r.s, 2*time.Hour)
	r.tick(r.s, 2*time.Hour)
	if n := len(r.f.appParams(AppFeishu, FeishuStatusGet)); n != 0 {
		t.Fatalf("%d status calls with no card", n)
	}
	r.ask(r.s, "ix_a", "")
	r.tick(r.s, time.Minute)
	if n := len(r.f.appParams(AppFeishu, FeishuStatusGet)); n == 0 {
		t.Fatal("a card is kept and the bot's status was not asked")
	}
}

// Nudges off when the manager restarts, then on: the backlog of cards due is let go, as without the restart.
func TestNudgeBacklogLetGoAcrossARestartWhileOff(t *testing.T) {
	r := newNudgeRig(t)
	r.setAfter(0)
	ix := r.ask(r.s, "ix_a", "")
	r.tick(r.s, 2*time.Hour) // the sweep sees them off
	s2 := r.service()        // restart while they are off
	r.askWithoutCard(s2, ix)
	r.setAfter(60)
	r.tick(s2, time.Minute)
	r.tick(s2, time.Hour)
	wantNudges(t, r, 0, "turned on after a restart")
	if c, _ := r.cardOf(s2, "ix_a"); c.SkippedAt.IsZero() || s2.cardNudged("ix_a") {
		t.Errorf("card %+v, shown as nudged %v", c, s2.cardNudged("ix_a"))
	}
}

// A send that keeps failing is tried three times and then given up, not forever; the card is not shown as nudged.
func TestNudgeGivesUpAfterThreeTries(t *testing.T) {
	r := newNudgeRig(t)
	r.ask(r.s, "ix_a", "")
	r.mu.Lock()
	r.sendErr = errors.New("feishu send: HTTP 502") // every send
	r.mu.Unlock()
	r.tick(r.s, 61*time.Minute)
	for i := 0; i < 5; i++ {
		r.tick(r.s, time.Minute)
	}
	r.mu.Lock()
	tries := len(r.keys)
	r.sendErr = nil
	r.mu.Unlock()
	if tries != 3 {
		t.Fatalf("%d tries, want 3", tries)
	}
	r.tick(r.s, time.Minute)
	wantNudges(t, r, 0, "after it gave up")
	if c, _ := r.cardOf(r.s, "ix_a"); c.SkippedAt.IsZero() || !c.NudgedAt.IsZero() || r.s.cardNudged("ix_a") {
		t.Errorf("card %+v, shown as nudged %v", c, r.s.cardNudged("ix_a"))
	}
}

// A retry after Feishu's key window (an hour from the first try) could land twice: it is given up instead.
func TestNudgeDoesNotRetryPastTheKeyWindow(t *testing.T) {
	r := newNudgeRig(t)
	r.ask(r.s, "ix_a", "")
	r.mu.Lock()
	r.sendErr, r.failures = errors.New("feishu send: HTTP 502"), 1
	r.mu.Unlock()
	r.tick(r.s, 61*time.Minute) // the first try fails
	r.tick(r.s, 61*time.Minute) // the next sweep that gets to it is past the hour
	wantNudges(t, r, 0, "a retry past the key window")
	r.mu.Lock()
	tries := len(r.keys)
	r.mu.Unlock()
	if tries != 1 {
		t.Fatalf("%d sends, want the one that failed", tries)
	}
	if c, _ := r.cardOf(r.s, "ix_a"); c.SkippedAt.IsZero() || r.s.cardNudged("ix_a") {
		t.Errorf("card %+v, shown as nudged %v", c, r.s.cardNudged("ix_a"))
	}
}

// A refused nudge is not shown as sent.
func TestNudgeRefusedIsNotShownAsNudged(t *testing.T) {
	r := newNudgeRig(t)
	r.ask(r.s, "ix_a", "oc_group")
	r.mu.Lock()
	r.sendErr = &rpc.Error{Code: CodeChatDenied, Message: "chat denied"}
	r.mu.Unlock()
	r.tick(r.s, 61*time.Minute)
	if c, _ := r.cardOf(r.s, "ix_a"); c.SkippedAt.IsZero() || !c.NudgedAt.IsZero() || r.s.cardNudged("ix_a") {
		t.Errorf("card %+v, shown as nudged %v", c, r.s.cardNudged("ix_a"))
	}
}

// The card is answered in the moment between the sweep deciding on it and marking it (the hub is asked, the agent's
// list is read): nothing is sent.
func TestNudgeNotSentForACardAnsweredDuringTheSweep(t *testing.T) {
	r := newNudgeRig(t)
	ix := r.ask(r.s, "ix_a", "")
	r.onSessions = func() { r.s.feishuInteractionDone(ix, "answered") }
	r.tick(r.s, 61*time.Minute)
	if r.onSessions != nil {
		t.Fatal("the answer never came in the sweep")
	}
	wantNudges(t, r, 0, "answered during the sweep")
}

// The tries are kept with the card: a restart after a failed send does not give the next sweep a fresh hour, so a
// retry past Feishu's key window is still not made.
func TestNudgeTriesSurviveARestart(t *testing.T) {
	r := newNudgeRig(t)
	ix := r.ask(r.s, "ix_a", "")
	r.mu.Lock()
	r.sendErr, r.failures = errors.New("feishu send: HTTP 502"), 1
	r.mu.Unlock()
	r.tick(r.s, 61*time.Minute) // fails
	s2 := r.service()
	r.askWithoutCard(s2, ix)
	r.tick(s2, 61*time.Minute)
	wantNudges(t, r, 0, "a retry past the key window after a restart")
	r.mu.Lock()
	tries := len(r.keys)
	r.mu.Unlock()
	if tries != 1 {
		t.Fatalf("%d sends, want the one that failed", tries)
	}
}
