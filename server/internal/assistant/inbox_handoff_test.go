package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Hand to AI with write-back (inbox_handoff.go): the user hands an item to the main conversation, which may write
// back to it with inbox_suggest (item); what the user does with its suggestion is told there.

const handoffClient = "6f1c2b9e-4d3a-4c5b-8e7f-0a1b2c3d4e5f"

// handoffHub has a Feishu DM (through the plugin that took Feishu over) no processing conversation took, and the
// main conversation m1 on the assistant's machine.
func handoffHub(t *testing.T) (*inboxHub, *extensionLark, *InboxItem) {
	t.Helper()
	h := agentHub(t)
	lark := &extensionLark{}
	h.addConnector(lark, true)
	now := time.Now()
	it := &InboxItem{ID: "lark:f1:100", Source: "lark", Kind: KindDM, Account: "feishu", AccountName: "飞书", Title: "张三", Sender: "张三",
		Last: "100", Count: 1, StartedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute), Tier: TierAction, TriagedAt: "100",
		Summary: "张三问报价", Context: "飞书私聊（张三）的新消息：\n[11-05 10:00] 张三：周五前能给报价吗？",
		Lark: &InboxLark{AccountID: 1, FeedID: "f1", StartPosition: "100"}}
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	h.inbox.mu.Unlock()
	setSettings(t, h.Service, func(v *Settings) { v.Main = "m1" })
	h.f.setSessions("server", proto.Session{SessionID: "m1", Assistant: true, Status: proto.StatusIdle})
	return h, lark, it
}

func handoffCall(t *testing.T, h *inboxHub, id, body string) (int, map[string]any) {
	t.Helper()
	return inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+id+"/handoff", body)
}

// handOff hands it to the main conversation, which must answer 200.
func handOff(t *testing.T, h *inboxHub, it *InboxItem) {
	t.Helper()
	if code, out := handoffCall(t, h, it.ID, `{"text":"看下这件事","client_id":"`+handoffClient+`"}`); code != 200 {
		t.Fatalf("hand-off: %d %v", code, out)
	}
}

func mainSuggest(h *inboxHub, sid, args string) (string, error) {
	return h.runTool(context.Background(), "server", sid, "inbox_suggest", json.RawMessage(args))
}

// sendOnces are the session.send_once calls the manager made, their params.
func sendOnces(f *fakeHub) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.calls {
		if c.Method != DeviceSessionSendOnce {
			continue
		}
		var p map[string]any
		_ = json.Unmarshal(c.Params, &p)
		out = append(out, p)
	}
	return out
}

// toMain is every text sent to the main conversation m1 once-only (the user's hand-offs).
func toMain(f *fakeHub) []string {
	var out []string
	for _, p := range sendOnces(f) {
		if p["session_id"] == "m1" {
			text, _ := p["text"].(string)
			out = append(out, text)
		}
	}
	return out
}

// notesOf are the texts the manager sent conversation sid with a plain session.send (its notes, not the user's words).
func notesOf(f *fakeHub, sid string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, p := range f.agentSent {
		if p["session_id"] == sid {
			text, _ := p["text"].(string)
			out = append(out, text)
		}
	}
	return out
}

// toldMain waits for the main conversation to be told something containing want, and returns every note it got.
func toldMain(t *testing.T, h *inboxHub, want string) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		got := notesOf(h.f, "m1")
		if strings.Contains(strings.Join(got, "\n"), want) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("the main conversation was not told %q:\n%s", want, strings.Join(got, "\n---\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func handedChain(h *inboxHub, it *InboxItem) int {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	n := 0
	for _, e := range it.Chain {
		if e.Kind == ChainHandling && e.Text == "交给了 Persona 主对话" {
			n++
		}
	}
	return n
}

// A hand-off sends the user's text after a block naming the item to the main conversation, once per request: the
// same request again sends nothing more and answers as before; another request to the same conversation is one more
// message, recorded, but one chain entry.
func TestInboxHandoffSendsOnce(t *testing.T) {
	h, _, it := handoffHub(t)
	body := `{"text":"帮我看看怎么回","client_id":"` + strings.ToUpper(handoffClient) + `"}`
	code, out := handoffCall(t, h, it.ID, body)
	session, _ := out["session"].(map[string]any)
	if code != 200 || session["machine_id"] != "server" || session["session_id"] != "m1" {
		t.Fatalf("hand-off: %d %v", code, out)
	}
	item := out["item"].(map[string]any)
	hs, _ := item["handoffs"].([]any)
	if len(hs) != 1 || hs[0].(map[string]any)["session_id"] != "m1" || hs[0].(map[string]any)["op"] != nil || item["handling"] != nil || item["agent"] != nil {
		t.Fatalf("listed: %v", item)
	}
	sent := sendOnces(h.f)
	if len(sent) != 1 || sent[0]["session_id"] != "m1" || sent[0]["client_msg_id"] != handoffClient {
		t.Fatalf("sent: %v", sent)
	}
	text := sent[0]["text"].(string)
	for _, want := range []string{"事件 id：lark:f1:100", `item: "lark:f1:100"`, "收件箱事件 · 张三"} {
		if !strings.Contains(text, want) {
			t.Errorf("the message lacks %q:\n%s", want, text)
		}
	}
	if _, after, _ := strings.Cut(text, "</persona-context>"); after != "\n帮我看看怎么回" {
		t.Errorf("the user's words are not after the block:\n%s", text)
	}

	if code, again := handoffCall(t, h, it.ID, body); code != 200 || again["session"].(map[string]any)["session_id"] != "m1" {
		t.Fatalf("again: %d %v", code, again)
	}
	h.inbox.mu.Lock()
	n := len(it.Handoffs)
	h.inbox.mu.Unlock()
	if got := len(sendOnces(h.f)); got != 1 || n != 1 || handedChain(h, it) != 1 {
		t.Fatalf("a repeat: %d sent, %d recorded, %d chain entries", got, n, handedChain(h, it))
	}

	// No client id: the item and text make it; another text is another hand-off to the same conversation.
	for range 2 {
		if code, out := handoffCall(t, h, it.ID, `{"text":"再看一眼"}`); code != 200 {
			t.Fatalf("without a client id: %d %v", code, out)
		}
	}
	h.inbox.mu.Lock()
	n = len(it.Handoffs)
	h.inbox.mu.Unlock()
	if got := len(sendOnces(h.f)); got != 2 || n != 2 || handedChain(h, it) != 1 {
		t.Fatalf("another hand-off: %d sent, %d recorded, %d chain entries", got, n, handedChain(h, it))
	}
	if !h.st.saved(it.ID) {
		t.Fatal("the hand-off was not saved")
	}
}

// An item keeps its latest hand-offs.
func TestInboxHandoffKeepsTheLatest(t *testing.T) {
	h, _, it := handoffHub(t)
	for i := range handoffsMax + 2 {
		if code, out := handoffCall(t, h, it.ID, `{"text":"第`+string(rune('a'+i))+`次"}`); code != 200 {
			t.Fatalf("%d: %d %v", i, code, out)
		}
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if len(it.Handoffs) != handoffsMax || len(toMain(h.f)) != handoffsMax+2 {
		t.Fatalf("%d kept, %d sent", len(it.Handoffs), len(toMain(h.f)))
	}
}

// With no main conversation the hand-off starts one with the message.
func TestInboxHandoffStartsTheMainConversation(t *testing.T) {
	h, _, it := handoffHub(t)
	setSettings(t, h.Service, func(v *Settings) { v.Main = "" })
	code, out := handoffCall(t, h, it.ID, `{"text":"看下这件事","client_id":"`+handoffClient+`"}`)
	session, _ := out["session"].(map[string]any)
	if code != 200 || session["session_id"] != "s1" || h.settings(context.Background()).Main != "s1" {
		t.Fatalf("hand-off: %d %v (main %q)", code, out, h.settings(context.Background()).Main)
	}
	if text, _ := h.f.lastSend(t)["text"].(string); !strings.Contains(text, `item: "lark:f1:100"`) || !strings.HasSuffix(text, "看下这件事") {
		t.Fatalf("the first message:\n%s", text)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if len(it.Handoffs) != 1 || it.Handoffs[0].SessionID != "s1" || len(sendOnces(h.f)) != 0 {
		t.Fatalf("recorded: %+v", it.Handoffs)
	}
}

// A hand-off that does not reach the conversation records nothing; an unknown item is 404, no text 400.
func TestInboxHandoffFailures(t *testing.T) {
	h, _, it := handoffHub(t)
	h.f.mu.Lock()
	h.f.failSendOnce = map[string]error{"server/m1": errors.New("example failure")}
	h.f.mu.Unlock()
	chain := len(it.Chain)
	if code, out := handoffCall(t, h, it.ID, `{"text":"看下这件事","client_id":"`+handoffClient+`"}`); code != 502 {
		t.Fatalf("a failed send: %d %v", code, out)
	}
	h.inbox.mu.Lock()
	n, c := len(it.Handoffs), len(it.Chain)
	h.inbox.mu.Unlock()
	if n != 0 || c != chain {
		t.Fatalf("a failed hand-off recorded %d, chain %d → %d", n, chain, c)
	}
	// Sent this time: the same request goes out.
	h.f.mu.Lock()
	h.f.failSendOnce = nil
	h.f.mu.Unlock()
	handOff(t, h, it)
	if len(toMain(h.f)) != 2 || handedChain(h, it) != 1 {
		t.Fatalf("after the failure: %d sent", len(toMain(h.f)))
	}
	if code, out := handoffCall(t, h, "lark:f1:nope", `{"text":"看下"}`); code != 404 {
		t.Fatalf("an unknown item: %d %v", code, out)
	}
	if code, out := handoffCall(t, h, it.ID, `{"text":"  "}`); code != 400 {
		t.Fatalf("no text: %d %v", code, out)
	}
}

// The conversation the item was handed to writes back with inbox_suggest (item): the suggestion names it and the
// chain links it; the item's processing conversation and its state are not touched.
func TestInboxSuggestFromAHandoff(t *testing.T) {
	h, lark, it := handledHub(t)
	setSettings(t, h.Service, func(v *Settings) { v.Main = "m1" })
	h.f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking}, proto.Session{SessionID: "m1", Assistant: true})
	h.inbox.mu.Lock()
	agent, handling := *it.Agent, *it.Handling
	h.inbox.mu.Unlock()
	handOff(t, h, it)
	out, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"可以，周五上午给你","note":"上次报价 10 月","recap":"张三要报价"}`)
	if err != nil || !strings.Contains(out, "told here") {
		t.Fatalf("suggest: %q %v", out, err)
	}
	h.inbox.mu.Lock()
	g, a, hd, recap := it.Suggestion, *it.Agent, *it.Handling, it.Recap
	e := it.Chain[len(it.Chain)-1]
	l := it.listed()
	h.inbox.mu.Unlock()
	if g == nil || g.Version != 1 || g.Kind != SuggestReply || g.Conversation == nil || g.Conversation.SessionID != "m1" || recap != "张三要报价" ||
		l.Suggestion.Conversation == nil || l.Status != StatusAction {
		t.Fatalf("suggestion: %+v / %+v", g, l)
	}
	if e.Kind != ChainHandling || e.Link == nil || e.Link.SessionID != "m1" || e.Detail != "可以，周五上午给你" {
		t.Fatalf("chain: %+v", e)
	}
	if a.State != agent.State || a.SessionID != "s1" || a.Draft != "" || hd.State != handling.State || hd.SessionID != "s1" {
		t.Fatalf("the processing conversation's state moved: %+v %+v", a, hd)
	}
	// Confirmed: sent, the item filed as replied, the main conversation told, the processing one only told to stop.
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 200 || out["done_by"] != "replied" {
		t.Fatalf("confirm: %d %v", code, out)
	}
	if len(lark.got) != 1 || lark.got[0].Text != "可以，周五上午给你" {
		t.Fatalf("sent: %+v", lark.got)
	}
	told := toldMain(t, h, "已经发出")
	note := told[len(told)-1]
	for _, want := range []string{"📥 收件箱事件「张三」有了下文\n", "这是 Persona 的收件箱通知", `item: "lark:f1:100"`, "对方之后的回复不会转到这里"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "对方回了会发到这里") {
		t.Errorf("the note promises what comes later:\n%s", note)
	}
	// A Persona notice: no message id, so the hub shows the apps one line instead of a bubble of the user's.
	if in := prompts.ParseInjected(note); in == nil || in.State != "inbox" || in.Title != "张三" {
		t.Fatalf("the note is not an injected notice: %+v", in)
	}
	h.f.mu.Lock()
	for _, p := range h.f.agentSent {
		if p["client_msg_id"] != nil {
			t.Errorf("a note sent with a message id: %v", p)
		}
	}
	h.f.mu.Unlock()
	if got := toMain(h.f); len(got) != 1 {
		t.Fatalf("%d once-only sends to the main conversation, want only the hand-off", len(got))
	}
	for _, s := range notesOf(h.f, "s1") {
		if strings.Contains(s, "已经发出") {
			t.Fatalf("the processing conversation was told of the send: %s", s)
		}
	}
}

// Each kind from a hand-off conversation, for an item with no processing conversation: the chain links the
// conversation, a conclusion done files the item.
func TestInboxSuggestFromAHandoffKinds(t *testing.T) {
	h, _, it := handoffHub(t)
	handOff(t, h, it)
	for _, args := range []string{`{"item":"lark:f1:100","kind":"reply","text":"好的"}`, `{"item":"lark:f1:100","kind":"agent","text":"查一下报价单"}`} {
		if _, err := mainSuggest(h, "m1", args); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		if e := lastChain(h, it); e.Kind != ChainHandling || e.Link == nil || e.Link.SessionID != "m1" {
			t.Fatalf("%s: chain %+v", args, e)
		}
	}
	if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"conclusion","text":"只是通知","outcome":"fyi"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	st, kind, agent, handling := it.status(), it.Suggestion.Kind, it.Agent, it.Handling
	h.inbox.mu.Unlock()
	if st != StatusFYI || kind != SuggestConclusion || agent != nil || handling != nil {
		t.Fatalf("fyi: %s %s %+v %+v", st, kind, agent, handling)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":3,"action":"confirm"}`); code != 200 || out["done_by"] != "user" {
		t.Fatalf("got it: %d %v", code, out)
	}

	h2, _, it2 := handoffHub(t)
	handOff(t, h2, it2)
	if _, err := mainSuggest(h2, "m1", `{"item":"lark:f1:100","kind":"conclusion","text":"只是通知","outcome":"done"}`); err != nil {
		t.Fatal(err)
	}
	h2.inbox.mu.Lock()
	defer h2.inbox.mu.Unlock()
	if it2.open() || it2.DoneBy != doneByManager || it2.Agent != nil || it2.Handling != nil {
		t.Fatalf("done: %v %q", it2.open(), it2.DoneBy)
	}
}

// inbox_suggest with item refuses a conversation the item was not handed to, an unknown item and a filed one, and
// changes nothing; without item it is what it was.
func TestInboxSuggestFromAHandoffRefused(t *testing.T) {
	h, _, it := handoffHub(t)
	reply := `{"item":"lark:f1:100","kind":"reply","text":"好的"}`
	if _, err := mainSuggest(h, "m1", reply); err == nil || !strings.Contains(err.Error(), "was not handed to this conversation") {
		t.Fatalf("not handed yet: %v", err)
	}
	handOff(t, h, it)
	h.inbox.mu.Lock()
	chain := len(it.Chain)
	h.inbox.mu.Unlock()
	if _, err := mainSuggest(h, "other", reply); err == nil || !strings.Contains(err.Error(), "was not handed to this conversation") {
		t.Fatalf("another conversation: %v", err)
	}
	if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:nope","kind":"reply","text":"好的"}`); err == nil || !strings.Contains(err.Error(), "there is no inbox item") {
		t.Fatalf("an unknown item: %v", err)
	}
	if _, err := mainSuggest(h, "m1", `{"kind":"reply","text":"好的"}`); !errors.Is(err, errNotProcessing) {
		t.Fatalf("without item: %v", err)
	}
	h.inbox.mu.Lock()
	g, c := it.Suggestion, len(it.Chain)
	h.inbox.mu.Unlock()
	if g != nil || c != chain {
		t.Fatalf("a refusal changed the item: %+v, chain %d → %d", g, chain, c)
	}
	if code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/done", `{"done":true}`); code != 200 {
		t.Fatal(code)
	}
	if _, err := mainSuggest(h, "m1", reply); err == nil || !strings.Contains(err.Error(), "already filed") {
		t.Fatalf("a filed item: %v", err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.open() || it.Suggestion != nil {
		t.Fatal("a filed item was reopened")
	}
}

// The item's own processing conversation may name it too: that is its suggestion as without item.
func TestInboxSuggestNamingItsOwnItem(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"item":"lark:f1:100","kind":"reply","text":"好的"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Suggestion == nil || it.Suggestion.Conversation != nil || it.Handling.State != HandlingDone || it.Agent.Draft != "好的" {
		t.Fatalf("%+v %+v", it.Suggestion, it.Handling)
	}
}

// Reject, instruct and handback on a hand-off conversation's suggestion are told there, wrapped to name the item,
// once each; the item's processing conversation hears nothing and keeps its state.
func TestInboxHandedSuggestionNotes(t *testing.T) {
	h, _, it := handledHub(t)
	setSettings(t, h.Service, func(v *Settings) { v.Main = "m1" })
	h.f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking}, proto.Session{SessionID: "m1", Assistant: true})
	handOff(t, h, it)
	h.inbox.mu.Lock()
	state := it.Handling.State
	h.inbox.mu.Unlock()
	if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"好的"}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"reject"}`); code != 200 || out["suggestion"] != nil {
		t.Fatalf("reject: %d %v", code, out)
	}
	told := toldMain(t, h, "驳回了你的建议")
	if n := strings.Count(strings.Join(told, "\n"), "驳回了你的建议"); n != 1 || !strings.Contains(told[len(told)-1], `item: "lark:f1:100"`) {
		t.Fatalf("told %d times:\n%s", n, told[len(told)-1])
	}
	if e := lastChain(h, it); e.Kind != ChainRejected {
		t.Fatalf("chain: %+v", e)
	}
	if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"周四"}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":2,"action":"instruct","text":"先问时间"}`); code != 200 {
		t.Fatalf("instruct: %d %v", code, out)
	}
	toldMain(t, h, "先问时间")
	if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"先问张三","handback":true}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":3,"action":"confirm"}`); code != 200 {
		t.Fatalf("handback: %d %v", code, out)
	}
	toldMain(t, h, "交回给你去做")
	if got := notesOf(h.f, "s1"); len(got) != 0 {
		t.Fatalf("the processing conversation was told: %v", got)
	}
	if got := len(toMain(h.f)); got != 1 {
		t.Fatalf("%d once-only sends to the main conversation, want only the hand-off", got)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Handling.State != state || it.Agent.SessionID != "s1" || !it.open() {
		t.Fatalf("the processing conversation's state moved: %+v", it.Handling)
	}
}

// For an item no processing conversation took, a confirmed agent suggestion from the main conversation starts the
// Agent as that conversation would; the chain links it and the main conversation is told.
func TestInboxHandedAgentSuggestion(t *testing.T) {
	h, _, it := handoffHub(t)
	handOff(t, h, it)
	if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"agent","text":"查一下报价单"}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 200 {
		t.Fatalf("confirm: %d %v", code, out)
	}
	h.f.mu.Lock()
	starts := len(h.f.starts)
	h.f.mu.Unlock()
	if starts != 1 {
		t.Fatalf("%d agents started", starts)
	}
	e := lastChain(h, it)
	if e.Kind != ChainAgent || e.Link == nil || e.Link.SessionID == "" || !strings.Contains(e.Text, "查一下报价单") {
		t.Fatalf("chain: %+v", e)
	}
	note := toldMain(t, h, "Agent 已经开了")
	if last := note[len(note)-1]; !strings.Contains(last, e.Link.SessionID) || !strings.Contains(last, `item: "lark:f1:100"`) {
		t.Fatalf("the note:\n%s", last)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Agent != nil || it.Handling != nil || it.Suggestion != nil || !it.open() {
		t.Fatalf("after the start: %+v %+v %+v", it.Agent, it.Handling, it.Suggestion)
	}
}

// The AI twin's sweep leaves an item handed to the main conversation as it is: the main conversation is no
// processing conversation, so it never times out or fails there.
func TestInboxSweepLeavesAHandoffAlone(t *testing.T) {
	h, _, it := handoffHub(t)
	handOff(t, h, it)
	if _, err := mainSuggest(h, "m1", `{"item":"lark:f1:100","kind":"reply","text":"好的"}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	chain := len(it.Chain)
	h.inbox.mu.Unlock()
	h.sweepInboxAgents(context.Background(), InboxAgentConfig{Enabled: true}, time.Now().Add(24*time.Hour))
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Agent != nil || it.Handling != nil || it.Suggestion == nil || len(it.Chain) != chain || it.status() != StatusAction {
		t.Fatalf("the sweep moved it: %+v %+v %v", it.Agent, it.Handling, chainKinds(it))
	}
}

// A note for an item without a title still reads as the inbox's notice: the item's id stands in.
func TestInboxHandoffNoteWithoutATitle(t *testing.T) {
	note := renderHandoffNote(inboxNote{Name: prompts.NoteRejected, Text: "好的"}, "示例用户", "lark:f1:100", "")
	if in := prompts.ParseInjected(note); in == nil || in.State != "inbox" || in.Title != "lark:f1:100" {
		t.Fatalf("ParseInjected = %+v\n%s", in, note)
	}
}

func TestGoldenInboxHandoff(t *testing.T) {
	golden(t, "inbox_handoff.message", prompts.RenderInboxHandoff(prompts.InboxHandoff{ID: "lark:f1:100", Title: "示例客户", Text: "看下这件事，帮我想想怎么回"}))
	for name, n := range map[string]inboxNote{
		"sent":          {Name: prompts.NoteSent, Text: "周五上午给你", Edited: true},
		"rejected":      {Name: prompts.NoteRejected, Text: "好的"},
		"instructed":    {Name: prompts.NoteInstructed, Text: "先问时间"},
		"handback":      {Name: prompts.NoteHandback, Text: "先问示例同事"},
		"agent_started": {Name: prompts.NoteAgentStarted, Text: "查一下报价单", Machine: "box", Session: "s-sub"},
	} {
		golden(t, "inbox_handoff.note_"+name, renderHandoffNote(n, "示例用户", "lark:f1:100", "示例客户"))
	}
}
