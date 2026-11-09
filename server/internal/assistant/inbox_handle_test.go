package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// handledHub has a Feishu DM (through the plugin that took Feishu over) whose processing conversation s1 runs on the
// assistant's machine.
func handledHub(t *testing.T) (*inboxHub, *extensionLark, *InboxItem) {
	t.Helper()
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	lark := &extensionLark{}
	h.addConnector(lark, true)
	now := time.Now()
	it := &InboxItem{ID: "lark:f1:100", Source: "lark", Kind: KindDM, Account: "feishu", AccountName: "飞书", Title: "张三", Sender: "张三",
		Last: "100", Count: 1, StartedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute), Tier: TierAction, TriagedAt: "100",
		Summary: "张三问报价", Reason: "私聊直接提问", Context: "飞书私聊（张三）的新消息：\n[11-05 10:00] 张三：周五前能给报价吗？",
		Lark: &InboxLark{AccountID: 1, FeedID: "f1", StartPosition: "100"}}
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	h.inbox.mu.Unlock()
	h.inbox.escalate(now.Add(-time.Hour), now)
	h.inbox.mu.Lock()
	it.Agent.State, it.Agent.MachineID, it.Agent.SessionID, it.Agent.Pending, it.Agent.Wake = AgentRunning, "server", "s1", nil, false
	h.inbox.noteHandling(it, "server", "s1")
	h.inbox.setHandling(it, HandlingLooking, "", now)
	h.inbox.mu.Unlock()
	h.f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking})
	return h, lark, it
}

func suggestTool(t *testing.T, h *inboxHub, args string) (string, error) {
	t.Helper()
	return h.runTool(context.Background(), "server", "s1", "inbox_suggest", json.RawMessage(args))
}

func suggestionCall(t *testing.T, h *inboxHub, id string, body string, role ...string) (int, map[string]any) {
	t.Helper()
	return inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+id+"/suggestion", body, role...)
}

func lastChain(h *inboxHub, it *InboxItem) InboxChainEntry {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	return it.Chain[len(it.Chain)-1]
}

// told waits for the conversation to be told something containing want, and returns everything it was told.
func toldConv(t *testing.T, h *inboxHub, want string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		all := strings.Join(h.f.sentToAgents(), "\n")
		if strings.Contains(all, want) {
			return all
		}
		if time.Now().After(deadline) {
			t.Fatalf("the conversation was not told %q:\n%s", want, all)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// From a suggestion to a confirmed reply: the suggestion has a version, the old apps see it as the draft, a stale
// version is refused, the edited text is what goes out (once), the event stays open waiting for the other side and
// the conversation is told; the source then saying "replied" is Persona's own message, not the user's.
func TestInboxSuggestionConfirmReply(t *testing.T) {
	h, lark, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"可以，周四给你","note":"上次报价 10 月","recap":"张三要报价，等你定"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	l := it.listed()
	h.inbox.mu.Unlock()
	if l.Suggestion == nil || l.Suggestion.Version != 1 || l.Suggestion.Kind != SuggestReply || l.Status != StatusAction ||
		l.Handling.State != HandlingDone || l.Recap != "张三要报价，等你定" || l.Agent.State != AgentDrafted || l.Agent.Draft != "可以，周四给你" ||
		l.Agent.Note != "上次报价 10 月" {
		t.Fatalf("listed: %+v / %+v / %+v", l.Suggestion, l.Handling, l.Agent)
	}
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"可以，周四下班前给你"}`); err != nil {
		t.Fatal(err)
	}
	code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
	if code != 409 || out["code"] != "suggestion_stale" || out["item"].(map[string]any)["suggestion"].(map[string]any)["version"] != float64(2) {
		t.Fatalf("stale: %d %v", code, out)
	}
	if len(lark.got) != 0 {
		t.Fatal("a stale suggestion went out")
	}
	code, out = suggestionCall(t, h, it.ID, `{"version":2,"action":"confirm","text":"可以，周五上午给你"}`)
	if code != 200 || out["status"] != StatusWorking || out["handling"].(map[string]any)["state"] != HandlingWaiting || out["done_at"] != nil ||
		out["suggestion"] != nil {
		t.Fatalf("confirm: %d %v", code, out)
	}
	if len(lark.got) != 1 || lark.got[0].Text != "可以，周五上午给你" {
		t.Fatalf("sent: %+v", lark.got)
	}
	h.inbox.mu.Lock()
	kinds := chainKinds(it)
	confirmed := it.Chain[len(it.Chain)-2]
	h.inbox.mu.Unlock()
	if kinds[len(kinds)-2] != ChainConfirmed || kinds[len(kinds)-1] != ChainSent || !confirmed.Edited || confirmed.Detail != "可以，周五上午给你" ||
		confirmed.Text != "你确认了（改过文字）" {
		t.Fatalf("chain: %v %+v", kinds, confirmed)
	}
	toldConv(t, h, "已经发出")
	// The same confirmation again (another device): stale, nothing more goes out.
	if code, _ := suggestionCall(t, h, it.ID, `{"version":2,"action":"confirm"}`); code != 409 || len(lark.got) != 1 {
		t.Fatalf("again: %d, sent %d", code, len(lark.got))
	}

	// The plugin says the chat's newest message is mine: Persona's own, the event waits on.
	done := larkDM("f1", "100", "100", 1, time.Now(), "周五前能给报价吗？")
	done.Done = connector.DoneReplied
	lark.things = []connector.InboxThing{done}
	collectAndAdmit(h)
	h.inbox.mu.Lock()
	open, state := it.open(), it.Handling.State
	h.inbox.mu.Unlock()
	if !open || state != HandlingWaiting {
		t.Fatalf("Persona's own reply filed it: open %v, %s", open, state)
	}

	// The other side answers: one received entry, sent into the same conversation.
	h.inbox.mu.Lock()
	sentBefore := len(h.f.agentSent)
	h.inbox.mu.Unlock()
	lark.things = []connector.InboxThing{larkDM("f1", "100", "102", 2, time.Now(), "周五前能给报价吗？", "好的，等你")}
	collectAndAdmit(h)
	h.handInbox(context.Background())
	all := toldConv(t, h, "好的，等你")
	h.inbox.mu.Lock()
	if it.Handling.State != HandlingLooking || it.Agent.SessionID != "s1" || it.Chain[len(it.Chain)-1].Kind != ChainReceived {
		t.Fatalf("after the other side's answer: %+v %v", it.Handling, chainKinds(it))
	}
	h.inbox.mu.Unlock()
	if strings.Count(all, "有了新消息") != 1 || sentBefore == 0 {
		t.Fatalf("told: %s", all)
	}

	// Now the user replies in Feishu themselves: filed (replied), the conversation told to stop.
	self := larkDM("f1", "100", "102", 2, time.Now(), "好的，等你")
	self.Done = connector.DoneReplied
	lark.things = []connector.InboxThing{self}
	collectAndAdmit(h)
	h.handInbox(context.Background())
	toldConv(t, h, "自己回复了")
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.open() || it.DoneBy != "replied" || it.Chain[len(it.Chain)-1].Kind != ChainSelfReplied {
		t.Fatalf("self reply: %v %q %v", it.open(), it.DoneBy, chainKinds(it))
	}
}

// Reject and Say something hand it back to the conversation (a turn); instruct needs words.
func TestInboxSuggestionRejectInstruct(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"好的"}`); err != nil {
		t.Fatal(err)
	}
	code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"reject"}`)
	if code != 200 || out["suggestion"] != nil || out["handling"].(map[string]any)["state"] != HandlingLooking || out["status"] != StatusWorking {
		t.Fatalf("reject: %d %v", code, out)
	}
	if e := lastChain(h, it); e.Kind != ChainRejected {
		t.Fatalf("chain: %+v", e)
	}
	toldConv(t, h, "驳回了你的建议")
	if code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 409 {
		t.Fatalf("a rejected suggestion confirmed: %d", code)
	}
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"周四"}`); err != nil {
		t.Fatal(err)
	}
	if code, _ := suggestionCall(t, h, it.ID, `{"version":2,"action":"instruct"}`); code != 400 {
		t.Fatalf("instruct without words: %d", code)
	}
	code, out = suggestionCall(t, h, it.ID, `{"version":2,"action":"instruct","text":"别这么回，先问时间"}`)
	if code != 200 || out["suggestion"] != nil {
		t.Fatalf("instruct: %d %v", code, out)
	}
	if e := lastChain(h, it); e.Kind != ChainInstructed || e.Detail != "别这么回，先问时间" || e.Text != "你说：别这么回，先问时间" {
		t.Fatalf("chain: %+v", e)
	}
	toldConv(t, h, "别这么回，先问时间")
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Agent.State != AgentRunning || it.Agent.Wake { // delivered: a turn is on
		t.Fatalf("agent: %+v", it.Agent)
	}
}

// A send that fails leaves the event Needs you without a suggestion, the chain says so.
func TestInboxSuggestionSendFails(t *testing.T) {
	h, _, it := handledHub(t)
	h.in.mu.Lock()
	h.in.conns = []connector.Connector{&failingReplier{}}
	h.in.mu.Unlock()
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"好的"}`); err != nil {
		t.Fatal(err)
	}
	code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
	if code == 200 {
		t.Fatal("a failed send answered 200")
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Suggestion != nil || it.Handling.State != HandlingFailed || it.status() != StatusAction || !it.open() ||
		it.Chain[len(it.Chain)-1].Kind != ChainFailed || it.Handling.Error != "发送没有成功" {
		t.Fatalf("after the failure: %+v %+v %v", it.Suggestion, it.Handling, chainKinds(it))
	}
}

type failingReplier struct{ extensionLark }

func (c *failingReplier) Reply(context.Context, connector.InboxReply) error {
	return errors.New("example failure")
}

// An answer-only device (the watch) confirms a reply or a conclusion as text, nothing else.
func TestInboxSuggestionWatch(t *testing.T) {
	h, lark, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"agent","text":"查一下报价单"}`); err != nil {
		t.Fatal(err)
	}
	if code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`, roleAnswer); code != 403 {
		t.Fatalf("watch starts an agent: %d", code)
	}
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"好的"}`); err != nil {
		t.Fatal(err)
	}
	if code, _ := suggestionCall(t, h, it.ID, `{"version":2,"action":"reject"}`, roleAnswer); code != 403 {
		t.Fatalf("watch rejects: %d", code)
	}
	if code, _ := suggestionCall(t, h, it.ID, `{"version":2,"action":"confirm","text":"/agent 帮我查"}`, roleAnswer); code != 403 {
		t.Fatalf("watch posts /agent: %d", code)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":2,"action":"confirm"}`, roleAnswer); code != 200 || len(lark.got) != 1 {
		t.Fatalf("watch confirms a reply: %d %v", code, out)
	}
}

// Conclusions: FYI lists it there with the conclusion until Got it; Done files it at once.
func TestInboxSuggestionConclusion(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"conclusion","text":"周报里你的两项都按计划","outcome":"fyi"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	st, tier, last := it.status(), it.Tier, it.Chain[len(it.Chain)-1]
	// One stored with a link before is listed without it: a conclusion expands inline.
	it.Chain = append(it.Chain, InboxChainEntry{Kind: ChainConcluded, Link: &InboxChainLink{Type: LinkSession, SessionID: "s1"}})
	listed := it.listed().Chain
	it.Chain = it.Chain[:len(it.Chain)-1]
	h.inbox.mu.Unlock()
	if st != StatusFYI || tier != TierFYI || last.Kind != ChainConcluded || last.Link != nil || last.Detail != "周报里你的两项都按计划" ||
		listed[len(listed)-1].Link != nil {
		t.Fatalf("fyi: %s %s %+v / %+v", st, tier, last, listed[len(listed)-1])
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 200 || out["done_by"] != "user" {
		t.Fatalf("知道了: %d %v", code, out)
	}

	h2, _, it2 := handledHub(t)
	if _, err := h2.runTool(context.Background(), "server", "s1", "inbox_suggest", json.RawMessage(`{"kind":"conclusion","text":"只是通知","outcome":"done"}`)); err != nil {
		t.Fatal(err)
	}
	h2.inbox.mu.Lock()
	defer h2.inbox.mu.Unlock()
	if it2.open() || it2.DoneBy != doneByManager || it2.Chain[len(it2.Chain)-1].Kind != ChainDone {
		t.Fatalf("done: %v %q %v", it2.open(), it2.DoneBy, chainKinds(it2))
	}
}

// A handback suggestion: confirming runs nothing, it goes back to the conversation as an instruction.
func TestInboxSuggestionHandback(t *testing.T) {
	h, lark, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"先问张三再回客户","handback":true}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	draft := it.Agent.Draft
	h.inbox.mu.Unlock()
	if draft != "" {
		t.Fatalf("a handback is no draft for the old apps: %q", draft)
	}
	if code, _ := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 200 || len(lark.got) != 0 {
		t.Fatalf("handback: %d, sent %d", code, len(lark.got))
	}
	toldConv(t, h, "交回给你去做")
}

// The suggestion tool only works in an event's conversation, and refuses blanks and unknown kinds.
func TestInboxSuggestionToolRules(t *testing.T) {
	h, _, _ := handledHub(t)
	for _, args := range []string{`{"kind":"reply","text":"【时间】给你"}`, `{"kind":"nope","text":"x"}`, `{"kind":"reply"}`, `{}`,
		`{"kind":"conclusion","text":"x","outcome":"later"}`} {
		if _, err := suggestTool(t, h, args); err == nil {
			t.Errorf("%s accepted", args)
		}
	}
	if _, err := h.runTool(context.Background(), "server", "other", "inbox_suggest", json.RawMessage(`{"kind":"reply","text":"x"}`)); err == nil {
		t.Fatal("another conversation's suggestion accepted")
	}
	if out, err := suggestTool(t, h, `{"recap":"只更新总结"}`); err != nil || !strings.Contains(out, "recap") {
		t.Fatalf("recap only: %s %v", out, err)
	}
}

// An old draft (stored before suggestions) is listed as a reply suggestion, version 1, and can be confirmed.
func TestInboxLegacyDraftIsASuggestion(t *testing.T) {
	h := agentHub(t)
	lark := &extensionLark{}
	h.addConnector(lark, true)
	at := time.Now().Add(-time.Hour)
	it := &InboxItem{ID: "lark:f9:1", Source: "lark", Kind: KindDM, Last: "1", Tier: TierAction, UpdatedAt: at, Lark: &InboxLark{FeedID: "f9"},
		Agent: &InboxAgent{State: AgentDrafted, Draft: "旧草稿", Note: "依据", SessionID: "old", MachineID: "server", DraftedAt: &at}}
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	l := it.listed()
	h.inbox.mu.Unlock()
	if l.Suggestion == nil || l.Suggestion.Version != 1 || l.Suggestion.Text != "旧草稿" || l.Handling == nil || l.Handling.State != HandlingDone ||
		l.Agent.Draft != "旧草稿" {
		t.Fatalf("listed: %+v %+v", l.Suggestion, l.Handling)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 200 || len(lark.got) != 1 || lark.got[0].Text != "旧草稿" ||
		out["handling"].(map[string]any)["state"] != HandlingWaiting {
		t.Fatalf("confirm: %d %v", code, out)
	}
}

// The manager restarted while a conversation was being started: the event's id finds it, nothing starts again.
// One whose conversation is gone gets a new one that carries the recap and the chain.
func TestInboxConversationResumes(t *testing.T) {
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	// Drive recovery passes explicitly below, including after delivery discovers the missing conversation.
	h.inbox.handing.Lock()
	defer h.inbox.handing.Unlock()
	cfg := InboxAgentConfig{Enabled: true}
	old := time.Now().Add(-10 * time.Minute)
	it := actionItem("lark:f1:11", time.Now())
	it.Agent = &InboxAgent{State: AgentRunning, StartedAt: &old, Task: "看看"}
	h.inbox.items[it.ID] = it
	h.originSet(context.Background(), "server", "s-found", OriginInbox, it.ID)
	h.f.setSessions("server", proto.Session{SessionID: "s-found", Status: proto.StatusIdle})
	h.sweepInboxAgents(context.Background(), cfg, time.Now())
	if it.Agent.State != AgentQueued {
		t.Fatalf("an orphaned start: %+v", it.Agent)
	}
	starts := h.f.startCount()
	h.startInboxAgents(context.Background(), cfg)
	if it.Agent.SessionID != "s-found" || h.f.startCount() != starts {
		t.Fatalf("not taken back: %+v (starts %d → %d)", it.Agent, starts, h.f.startCount())
	}

	// Its conversation is gone (the machine answers without it): a waking note starts a new one, with where it got to.
	it.Recap = "已经查到合入了"
	h.inbox.mu.Lock()
	h.inbox.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: "再看看分支"}, true, time.Now())
	h.inbox.mu.Unlock()
	h.f.setSessions("server")
	h.f.mu.Lock()
	h.f.failSend = fmt.Errorf("no such session")
	h.f.mu.Unlock()
	h.deliverNotes(context.Background(), it)
	if it.Agent.SessionID != "" || !it.Agent.lost("s-found") || it.Agent.State != AgentQueued {
		t.Fatalf("gone: %+v", it.Agent)
	}
	h.f.mu.Lock()
	h.f.failSend = nil
	h.f.mu.Unlock()
	h.originsForget(t, "s-found")
	h.startInboxAgents(context.Background(), cfg)
	if it.Agent.SessionID == "" || it.Agent.SessionID == "s-found" {
		t.Fatalf("no new conversation: %+v", it.Agent)
	}
	first := h.f.lastSend(t)["text"].(string)
	for _, want := range []string{"原来处理它的那段对话不在了", "已经查到合入了", "再看看分支"} {
		if !strings.Contains(first, want) {
			t.Errorf("the new conversation's first message lacks %q:\n%s", want, first)
		}
	}
}

// originsForget drops a conversation's origin (it is gone from the machine).
func (h *inboxHub) originsForget(t *testing.T, sid string) {
	t.Helper()
	h.origins.mu.Lock()
	delete(h.origins.m, sid)
	h.origins.mu.Unlock()
}

// An Agent the event's conversation starts: the chain links it, the event is In progress (Agent is working).
func TestInboxConversationStartsAnAgent(t *testing.T) {
	h, _, it := handledHub(t)
	h.inboxAgentStarted(context.Background(), "s1", "box", "s-sub", "查构建失败的原因")
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	e := it.Chain[len(it.Chain)-1]
	if e.Kind != ChainAgent || e.Link == nil || e.Link.SessionID != "s-sub" || e.Link.MachineID != "box" || e.Text != "开了子会话：查构建失败的原因" ||
		it.Handling.State != HandlingAgent || it.status() != StatusWorking {
		t.Fatalf("%+v %+v", e, it.Handling)
	}
}

// Filing an event being handled tells its conversation to stop.
func TestInboxFilingStopsTheConversation(t *testing.T) {
	h, _, it := handledHub(t)
	if code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/done", `{"done":true}`); code != 200 {
		t.Fatal(code)
	}
	toldConv(t, h, "标了已处理")
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Handling.State != HandlingDone || it.Suggestion != nil {
		t.Fatalf("%+v", it.Handling)
	}
}

// Urgent: Needs you at once while the manager prepares, the suggestion lands on it.
func TestInboxUrgentStaysNeedsYou(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	it.Urgent = true
	st := it.status()
	h.inbox.mu.Unlock()
	if st != StatusAction {
		t.Fatalf("urgent while looking: %s", st)
	}
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"马上看"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.status() != StatusAction || it.Suggestion == nil {
		t.Fatal("no suggestion on the urgent event")
	}
}

// New messages into an event waiting for the other side: a turn that only updates the recap goes back to waiting; a
// turn that ends with nothing while a suggestion stands keeps the suggestion.
func TestInboxRecapOnlyTurnGoesBack(t *testing.T) {
	h, _, it := handledHub(t)
	now := time.Now()
	h.inbox.mu.Lock()
	h.inbox.setHandling(it, HandlingWaiting, "", now)
	it.Last, it.TriagedAt = "101", "101"
	h.inbox.mu.Unlock()
	h.inbox.escalate(now.Add(-time.Hour), now)
	if _, err := suggestTool(t, h, `{"recap":"对方只说了谢谢"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	state, recap := it.Handling.State, it.Recap
	h.inbox.mu.Unlock()
	if state != HandlingWaiting || recap != "对方只说了谢谢" {
		t.Fatalf("after a recap-only turn: %s %q", state, recap)
	}

	if _, err := suggestTool(t, h, `{"kind":"reply","text":"好的"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	it.Last, it.TriagedAt = "102", "102"
	h.inbox.mu.Unlock()
	h.inbox.escalate(now.Add(-time.Hour), now)
	started := time.Now().Add(-2 * time.Minute)
	h.inbox.mu.Lock()
	it.Agent.StartedAt, it.Agent.Wake = &started, false
	h.inbox.mu.Unlock()
	h.f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusIdle})
	h.sweepInboxAgents(context.Background(), InboxAgentConfig{}, time.Now())
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Suggestion == nil || it.Handling.State != HandlingDone || it.status() != StatusAction {
		t.Fatalf("the standing suggestion was dropped: %+v %+v", it.Suggestion, it.Handling)
	}
}

// tellingLocks counts the per-item delivery locks kept.
func tellingLocks(h *inboxHub) int {
	n := 0
	h.inbox.telling.Range(func(any, any) bool { n++; return true })
	return n
}

// An item delivered to, then forgotten (its source's data deleted, or done past inboxForget), takes its delivery lock
// with it: the locks do not pile up for the life of the hub, and a late delivery to a forgotten item leaves none.
func TestInboxForgottenItemsDropTheirDeliveryLocks(t *testing.T) {
	h := agentHub(t)
	ctx := context.Background()
	const n = 6
	old := time.Now().Add(-inboxForget - time.Hour)
	var items []*InboxItem
	for i := range n {
		it := actionItem(fmt.Sprintf("x%d", i), time.Now())
		it.Source = "gone"
		it.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: fmt.Sprintf("s%d", i), Task: "查 1013 是否合入"}
		h.inbox.mu.Lock()
		h.inbox.items[it.ID] = it
		h.inbox.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: "再看看分支"}, false, time.Now())
		h.inbox.mu.Unlock()
		h.deliverNotes(ctx, it)
		items = append(items, it)
	}
	if got := tellingLocks(h); got != n {
		t.Fatalf("%d delivery locks after %d deliveries", got, n)
	}
	// Half are settled long ago: the next round forgets them.
	h.inbox.mu.Lock()
	for _, it := range items[:n/2] {
		it.Source, it.DoneAt = "kept", &old
	}
	h.inbox.mu.Unlock()
	h.collectInbox(ctx, roundPoll)
	h.inbox.mu.Lock()
	for _, it := range items[:n/2] {
		if h.inbox.items[it.ID] != nil {
			h.inbox.mu.Unlock()
			t.Fatalf("%s not forgotten", it.ID)
		}
	}
	h.inbox.mu.Unlock()
	if got := tellingLocks(h); got != n-n/2 {
		t.Fatalf("%d delivery locks after the round forgot %d of %d", got, n/2, n)
	}
	// The rest go with their source's data.
	if got, err := h.InboxForget(ctx, "gone"); err != nil || got != n-n/2 {
		t.Fatalf("forget: %d %v", got, err)
	}
	if got := tellingLocks(h); got != 0 {
		t.Fatalf("%d delivery locks after every item was forgotten", got)
	}
	// A delivery still on its way to a forgotten item sends nothing and keeps no lock.
	sentCount := func() int { h.f.mu.Lock(); defer h.f.mu.Unlock(); return len(h.f.agentSent) }
	sent := sentCount()
	h.inbox.mu.Lock()
	h.inbox.tell(items[0], inboxNote{Name: prompts.NoteInstructed, Text: "迟到的一条"}, false, time.Now())
	h.inbox.mu.Unlock()
	h.deliverNotes(ctx, items[0])
	if got, now := tellingLocks(h), sentCount(); got != 0 || now != sent {
		t.Fatalf("a late delivery to a forgotten item: %d locks, %d sent (was %d)", got, now, sent)
	}
}
