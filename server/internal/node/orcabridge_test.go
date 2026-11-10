package node

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	orcaChatID   = "claude_0b0e9c8e_6f1a_4c3e_9d2b_4a5f6e7d8c9b"
	orcaChatUUID = "0b0e9c8e-6f1a-4c3e-9d2b-4a5f6e7d8c9b"
)

// orcaChats is a fake Orca with one Claude chat (and a Codex chat and a terminal's agent, which the bridge leaves
// alone), its history, options and pending requests as the test sets them.
type orcaChats struct {
	f        *orcarttest.Fake
	mu       sync.Mutex
	state    string
	main     string // the main agent's own state when it differs from the row's ("": the row's)
	requests string // the pending requests' JSON
}

func newOrcaChats(t *testing.T) *orcaChats {
	t.Helper()
	o := &orcaChats{f: orcarttest.New(t), state: "done", requests: "[]"}
	o.trees()
	o.f.Handle("agentSession.history", func(p map[string]any) any {
		if p["sessionId"] != orcaChatID {
			return orcarttest.Fail{Code: "agent_session_identity_required"}
		}
		return map[string]any{"ok": true, "page": map[string]any{"fence": 3, "working": false},
			"providerSession": map[string]any{"key": "session_id", "id": orcaChatUUID}}
	})
	o.f.Handle("agentSession.options", func(map[string]any) any {
		return map[string]any{"current": map[string]any{"permissionMode": "accept-edits", "effort": "high", "model": "opus"}}
	})
	o.f.Handle("agentSession.currentRequests", func(p map[string]any) any {
		o.mu.Lock()
		defer o.mu.Unlock()
		marker := map[string]any{"version": "v1"}
		if p["version"] != "v1" {
			return map[string]any{"marker": marker, "json": "", "nextCursor": 0, "restart": true}
		}
		return map[string]any{"marker": marker, "json": o.requests, "nextCursor": nil}
	})
	for _, m := range []string{"agentSession.send", "agentSession.cancel", "agentSession.setOption",
		"agentSession.respondToApproval", "agentSession.respondToQuestion"} {
		o.f.Handle(m, func(map[string]any) any { return map[string]any{"ok": true, "value": map[string]any{}} })
	}
	return o
}

func (o *orcaChats) trees() {
	o.mu.Lock()
	state, main := o.state, o.main
	o.mu.Unlock()
	if main == "" {
		main = state
	}
	o.f.SetWorktrees(map[string]any{"worktreeId": "repo::/w/app", "path": "/w/app", "branch": "refs/heads/feat",
		"hostId": "local", "parentWorktreeId": "repo::/w/main", "agents": []any{
			map[string]any{"paneKey": "structured-agent-session-" + orcaChatID + ":aa", "agentType": "claude", "state": state,
				"mainAgent": map[string]any{"state": main}, "conversationName": "Refactor the parser",
				"lastAssistantMessage": "Done: tests pass.", "updatedAt": 1793850000000},
			map[string]any{"paneKey": "structured-agent-session-codex_1234abcd:bb", "agentType": "codex", "state": "done"},
			map[string]any{"paneKey": "tab-1:leaf-1", "agentType": "claude", "state": "done"},
		}})
}

func (o *orcaChats) set(state, requests string) {
	o.mu.Lock()
	o.state, o.requests = state, requests
	o.mu.Unlock()
	o.trees()
}

// writes are the params of the calls of method the fake got.
func (o *orcaChats) writes(method string) []map[string]any { return o.f.Params(method) }

// orcaBridgeNode is a node with a fake hub whose engine's bridge runs against the fake Orca.
func orcaBridgeNode(t *testing.T) (*Node, *runtimeHub, *orcaChats) {
	t.Helper()
	o := newOrcaChats(t)
	saved, savedPoll := orcaClient, orcaBridgePoll
	orcaClient = func() *orcart.Client { return &orcart.Client{MetaPath: o.f.MetaPath} }
	orcaBridgePoll = 50 * time.Millisecond
	n, hub := runtimeNode(t)
	if n.runtimeHost == nil {
		t.Fatal("no runtime host")
	}
	n.runtimeHost.setOrcaBridge(true)
	t.Cleanup(func() {
		n.runtimeHost.setOrcaBridge(false)
		orcaClient, orcaBridgePoll = saved, savedPoll
	})
	waitFor(t, "the chat listed", func() bool { _, ok := listed(t, n)[orcaChatUUID]; return ok })
	return n, hub, o
}

// A Claude chat is listed under its Claude session (its transcript), in its worktree's space, with Orca's title,
// last answer and modes in Persona's words; Codex chats and terminals are not the bridge's.
func TestOrcaBridgeListsChats(t *testing.T) {
	n, _, _ := orcaBridgeNode(t)
	s := listed(t, n)[orcaChatUUID]
	if s.Title != "Refactor the parser" || s.Cwd != "/w/app" || s.SpaceID != "repo::/w/app" || s.SpaceName != "feat" ||
		s.SpaceKind != proto.EngineOrca || s.ParentSpaceID != "repo::/w/main" || s.Status != proto.StatusDone || s.Agent != "claude" {
		t.Fatalf("row %+v", s)
	}
	if s.PermissionMode != "acceptEdits" || s.Capabilities == nil || !s.Capabilities.Send || !s.Capabilities.Interrupt ||
		s.Capabilities.Terminal || s.Capabilities.Modes == nil || !*s.Capabilities.Modes {
		t.Fatalf("modes / capabilities %+v %+v", s.PermissionMode, s.Capabilities)
	}
	for id := range listed(t, n) {
		if id == "codex_1234abcd" || id == "tab-1:leaf-1" {
			t.Errorf("listed %s", id)
		}
	}
}

// Before the transcript is on disk the bridge answers session.items with an empty page the hub can read: history,
// when present, is the transcript's version string (the hub's itemSpan), never a bool (#4886).
func TestOrcaBridgeItemsBeforeTranscriptDecodeAsSpan(t *testing.T) {
	n, _, _ := orcaBridgeNode(t)
	raw, err := nodeCall(t, n, "session.items", map[string]any{"session_id": orcaChatUUID})
	if err != nil {
		t.Fatal(err)
	}
	var page struct {
		Items   []json.RawMessage `json:"items"`
		History string            `json:"history,omitempty"`
		ToSeq   *int              `json:"to_seq,omitempty"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("hub can't read %s: %v", raw, err)
	}
	if len(page.Items) != 0 || page.History != "" || page.ToSeq == nil || *page.ToSeq != 0 {
		t.Fatalf("page %s", raw)
	}
}

// A session the bridge does not list is not Orca's: asked who owns it (session.items, runtimeFor), the bridge says
// so instead of answering an empty page, and the node does not hand the session to it. An empty page claimed every
// session the node had lost track of, an assistant conversation included, and its next message was refused with
// "This chat is no longer in Orca".
func TestOrcaBridgeDoesNotClaimOtherSessions(t *testing.T) {
	n, _, _ := orcaBridgeNode(t)
	const other = "5d2c7a10-3b4e-4f6a-8c9d-0e1f2a3b4c5d"
	_, _ = nodeCall(t, n, "session.items", map[string]any{"session_id": other})
	h, _, known := n.runtimes.belongs(other)
	if h != nil || known {
		t.Fatalf("the bridge claimed %s", other)
	}
	if n.runtimes.ownerOf(orcaChatUUID) == nil {
		t.Fatal("the bridge's own chat lost its owner")
	}
}

// A message goes to Orca's session with the envelope Orca checks; the same client_msg_id again is the same operation
// (Orca replays it); interrupting and modes are Orca's writes too.
func TestOrcaBridgeSendsInterruptsAndSetsModes(t *testing.T) {
	n, _, o := orcaBridgeNode(t)
	for range 2 {
		if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": orcaChatUUID, "text": "run the tests",
			"client_msg_id": "m-1"}); err != nil {
			t.Fatal(err)
		}
	}
	sends := o.writes("agentSession.send")
	if len(sends) != 1 { // the node sends a client_msg_id once (sendsafe.go)
		t.Fatalf("%d sends", len(sends))
	}
	env := sends[0]["envelope"].(map[string]any)
	body := sends[0]["body"].(map[string]any)
	fields := map[string]any{"body": body, "delivery": "queue-if-active"}
	fp, _ := orcart.Fingerprint("agentSession.send", orcaChatID, fields)
	if env["sessionId"] != orcaChatID || env["expectedRuntimeFence"] != float64(3) || env["payloadFingerprint"] != fp {
		b, _ := json.Marshal(sends)
		t.Fatalf("sends %s", b)
	}
	// Sent again after all (a node that restarted, its log of sends gone): the same operation, which Orca replays.
	b := newOrcaBridge(nil, nil, t.Logf)
	if a1, a2 := b.write(orcaChatID, "k"), b.write(orcaChatID, "k"); a1.OperationID != a2.OperationID {
		t.Errorf("operation ids %s / %s", a1.OperationID, a2.OperationID)
	}
	if b.write(orcaChatID, "").OperationID == b.write(orcaChatID, "").OperationID {
		t.Error("unkeyed writes share an operation id")
	}

	if _, err := nodeCall(t, n, "session.interrupt", map[string]any{"session_id": orcaChatUUID}); err != nil {
		t.Fatal(err)
	}
	if len(o.writes("agentSession.cancel")) != 1 {
		t.Error("no cancel")
	}
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": orcaChatUUID,
		"permission_mode": proto.ModeBypass, "effort": "max"}); err != nil {
		t.Fatal(err)
	}
	set := o.writes("agentSession.setOption")
	if len(set) != 2 || set[0]["key"] != "permissionMode" || set[0]["value"] != "full-access" || set[1]["key"] != "effort" ||
		set[1]["value"] != "max" {
		t.Fatalf("options %v", set)
	}
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": orcaChatUUID, "permission_mode": "plan"}); err == nil {
		t.Error("plan mode taken: Orca has none")
	}
}

// The detail is the tool's input as JSON, as Orca 1.4.246 sends a Bash approval.
const orcaApproval = `[{"itemId":"it-1","revision":2,"body":{"kind":"approval","title":"Allow Bash?",
"detail":"{\n  \"command\": \"rm -rf build\",\n  \"description\": \"Remove the build\"\n}",
"options":[{"id":"allow","label":"Allow"},{"id":"allowForSession","label":"Allow for this session"},{"id":"deny","label":"Deny"}],
"resolution":{"state":"pending"}}}]`

// Orca's pending approval is a Claude permission card on the phone; "always" answers with Orca's for-this-session
// option, at the prompt's revision.
func TestOrcaBridgeApprovalCard(t *testing.T) {
	_, hub, o := orcaBridgeNode(t)
	allow := true
	hub.mu.Lock()
	hub.resolve = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: &allow, Always: true} }
	hub.mu.Unlock()
	o.set("blocked", orcaApproval)
	waitFor(t, "the answer in Orca", func() bool { return len(o.writes("agentSession.respondToApproval")) > 0 })
	hub.mu.Lock()
	ix := hub.asked[0]
	hub.mu.Unlock()
	if ix.Kind != proto.IxPermission || ix.ToolName != "Bash" || ix.Summary != "rm -rf build" || ix.SessionID != orcaChatUUID ||
		ix.Agent != "" || !strings.Contains(string(ix.ToolInput), `"description":"Remove the build"`) {
		t.Fatalf("card %+v", ix)
	}
	got := o.writes("agentSession.respondToApproval")[0]
	if got["optionId"] != "allowForSession" || got["itemId"] != "it-1" || got["expectedRevision"] != float64(2) {
		t.Fatalf("answer %v", got)
	}
	o.set("working", "[]")
	time.Sleep(200 * time.Millisecond)
	if n := len(o.writes("agentSession.respondToApproval")); n != 1 {
		t.Errorf("answered %d times", n)
	}
}

// #3828: a subagent's approval blocks the row while the main agent works on (waiting for the subagent): the chat is
// blocked and the approval reaches the phone as a card of that subagent.
func TestOrcaBridgeSubagentApprovalCard(t *testing.T) {
	n, hub, o := orcaBridgeNode(t)
	release := make(chan struct{})
	hub.mu.Lock()
	hub.resolve = func(proto.Interaction) proto.Resolution { <-release; return proto.Resolution{Dismiss: true} }
	hub.mu.Unlock()
	defer close(release)
	o.mu.Lock()
	o.main = "working"
	o.mu.Unlock()
	o.set("blocked", strings.Replace(orcaApproval, `"revision":2,`, `"revision":2,"agentId":"sub-1",`, 1))
	waitFor(t, "the subagent's card", func() bool { hub.mu.Lock(); defer hub.mu.Unlock(); return len(hub.asked) > 0 })
	hub.mu.Lock()
	ix := hub.asked[0]
	hub.mu.Unlock()
	if ix.AgentID != "sub-1" || ix.SessionID != orcaChatUUID || ix.Kind != proto.IxPermission {
		t.Fatalf("card %+v", ix)
	}
	if s := listed(t, n)[orcaChatUUID]; s.Status != proto.StatusBlocked {
		t.Fatalf("status %s, want blocked", s.Status)
	}
}

// A card answered in Orca first leaves the phone.
func TestOrcaBridgeWithdrawsAnsweredCards(t *testing.T) {
	_, hub, o := orcaBridgeNode(t)
	release := make(chan struct{})
	hub.mu.Lock()
	hub.resolve = func(proto.Interaction) proto.Resolution { <-release; return proto.Resolution{Dismiss: true} }
	hub.mu.Unlock()
	defer close(release)
	o.set("blocked", orcaApproval)
	waitFor(t, "the card", func() bool { hub.mu.Lock(); defer hub.mu.Unlock(); return len(hub.asked) > 0 })
	o.set("working", "[]") // answered on the computer
	waitFor(t, "the card withdrawn", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		return len(hub.cancels) == 1 && hub.cancels[0] == hub.asked[0].ID
	})
	if n := len(o.writes("agentSession.respondToApproval")); n != 0 {
		t.Errorf("answered %d times", n)
	}
}

// A question's answers go back as Orca's question and option ids, a multi-select's labels joined with ", ", words
// that match no option as the written answer.
func TestOrcaBridgeQuestionCard(t *testing.T) {
	_, hub, o := orcaBridgeNode(t)
	hub.mu.Lock()
	hub.resolve = func(ix proto.Interaction) proto.Resolution {
		return proto.Resolution{Answers: map[string]string{"Which tests?": "Unit, E2E", "Where?": "staging-2"}}
	}
	hub.mu.Unlock()
	o.set("blocked", `[{"itemId":"q-1","revision":1,"body":{"kind":"question","question":"Which tests?","options":[],
"questions":[{"id":"qa","question":"Which tests?","multiSelect":true,"options":[{"id":"u","label":"Unit"},{"id":"e","label":"E2E"}]},
{"id":"qb","question":"Where?","multiSelect":false,"options":[{"id":"s","label":"staging"}]}],"resolution":{"state":"pending"}}}]`)
	waitFor(t, "the answer in Orca", func() bool { return len(o.writes("agentSession.respondToQuestion")) > 0 })
	hub.mu.Lock()
	ix := hub.asked[0]
	hub.mu.Unlock()
	if ix.Kind != proto.IxQuestion || len(ix.Questions) != 2 || !ix.Questions[0].MultiSelect || ix.Questions[0].Options[1].Label != "E2E" {
		t.Fatalf("card %+v", ix)
	}
	got, _ := json.Marshal(o.writes("agentSession.respondToQuestion")[0]["answers"])
	if want := `[{"optionIds":["u","e"],"questionId":"qa"},{"optionIds":[],"other":"staging-2","questionId":"qb"}]`; string(got) != want {
		t.Fatalf("answers %s, want %s", got, want)
	}
}
