package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func queuePolicyItem(t *testing.T, h *inboxHub, id string) *InboxItem {
	t.Helper()
	now := time.Now().UTC()
	it := &InboxItem{ID: id, Source: "example", Kind: KindDM, Tier: TierAction, Title: "Example request",
		Last: "1", TriagedAt: "1", Context: "Please review this request", UpdatedAt: now,
		Connector: &InboxConnector{Tags: []connector.Tag{{Key: connector.TagChat, Value: "chat"}}}}
	h.inbox.items[id] = it
	if ws := h.inbox.escalate(now.Add(-time.Hour), now); len(ws) != 1 || it.Agent == nil || it.Agent.State != AgentQueued {
		t.Fatalf("item did not enter the twin queue: %+v", it.Agent)
	}
	return it
}

func checkPolicyReleased(t *testing.T, it *InboxItem, lang i18n.Lang) {
	t.Helper()
	if it == nil {
		t.Fatal("released item is missing")
	}
	want := i18n.T(lang, "assistant-inbox_agent.never")
	if want == "" || want == "assistant-inbox_agent.never" {
		t.Fatal("the policy release reason is missing from the catalog")
	}
	if !it.open() || it.handled() || it.status() != it.Tier || it.Handling == nil || it.Handling.State != HandlingDone || it.Handling.Error != "" {
		t.Fatalf("item was not returned to its tier: tier=%s status=%s handling=%+v", it.Tier, it.status(), it.Handling)
	}
	if a := it.Agent; a == nil || a.State != AgentSkipped || a.SessionID != "" || a.Error != "" || len(a.Pending) != 0 || a.Wake || a.Back != "" || a.RetryAfter != nil {
		t.Fatalf("released item kept queued work: %+v", a)
	}
	if len(it.Chain) == 0 || it.Chain[len(it.Chain)-1].Kind != ChainConcluded || it.Chain[len(it.Chain)-1].Text != want {
		t.Fatalf("missing policy explanation: %+v", it.Chain)
	}
}

// A rule saved after escalation must still prevent a processing conversation, for every policy target and tier.
func TestInboxPolicyNeverReleasesQueue(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, tc := range []struct{ tag, tier string }{
			{connector.TagChat, TierAction}, {connector.TagSender, TierFYI}, {connector.TagRepo, TierNoise},
		} {
			t.Run(string(lang)+"/"+tc.tag, func(t *testing.T) {
				h := agentHub(t)
				h.f.hello.Locale = string(lang)
				it := queuePolicyItem(t, h, "queued")
				it.Tier = tc.tier
				p := InboxPolicy{Source: "example", Tag: tc.tag, Value: "example-org/example-repo", Twin: TwinNever}
				switch tc.tag {
				case connector.TagChat:
					it.Source, it.Connector = "lark", nil
					it.Lark = &InboxLark{FeedID: "chat", IsGroup: true, SenderID: "sender"}
					p.Source, p.Value = "lark", "chat"
				case connector.TagSender:
					it.Source, it.Connector = "mail", nil
					it.Mail = &InboxMail{AccountID: "account", From: MailAddress{Email: "Sender@Example.com"}}
					p.Source, p.Value = "mail", "sender@example.com"
				case connector.TagRepo:
					it.Connector.Tags = []connector.Tag{{Key: connector.TagRepo, Value: p.Value}}
				}
				setPolicyFor(t, h, p)
				h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true})
				checkPolicyReleased(t, it, lang)
				if it.Agent.Attempts != 0 || !h.st.saved(it.ID) || !changedTo(h, it.ID) {
					t.Fatalf("release started work or was not persisted and announced: %+v", it.Agent)
				}
				checkNoTwinWork(t, h)

				before := stateOf(h, it)
				pushes := h.in.pushes()
				h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true})
				if stateOf(h, it) != before || h.in.pushes() != pushes {
					t.Fatal("a repeated queue pass changed the released item again")
				}
				reloaded := New(Options{Hub: h.f, Store: func() Store { return h.st }, Logf: testLogf(t)})
				if err := reloaded.inboxReady(context.Background()); err != nil {
					t.Fatal(err)
				}
				checkPolicyReleased(t, reloaded.inbox.items[it.ID], lang)
				it.Last, it.TriagedAt = "2", "2"
				if ws := h.inbox.escalate(time.Now().Add(-time.Hour), time.Now()); len(ws) != 0 {
					t.Fatal("a new message requeued the released item")
				}
				checkNoTwinWork(t, h)
			})
		}
	}
}

// Releasing is independent of backoff, free slots, and a conversation found while recovering a previous start.
func TestInboxPolicyNeverBeforeQueueAdmission(t *testing.T) {
	for _, scenario := range []string{"backoff", "full", "adoption", "legacy"} {
		t.Run(scenario, func(t *testing.T) {
			h := agentHub(t)
			it := queuePolicyItem(t, h, "queued")
			now := time.Now().UTC()
			it.Agent.Back = HandlingWaiting
			switch scenario {
			case "backoff":
				retry := now.Add(time.Hour)
				it.Agent.RetryAfter, it.Agent.Attempts = &retry, 1
			case "full":
				busy := &InboxItem{ID: "busy", Tier: TierAction, Agent: &InboxAgent{State: AgentRunning, SessionID: "busy-session"},
					Handling: &InboxHandling{State: HandlingLooking}}
				h.inbox.items[busy.ID] = busy
			case "adoption":
				h.originSet(context.Background(), "server", "found-session", OriginInbox, it.ID, "")
				h.f.setSessions("server", proto.Session{SessionID: "found-session", Status: proto.StatusIdle})
			case "legacy":
				it.Handling = nil
			}
			setPolicyFor(t, h, InboxPolicy{Source: "example", Tag: connector.TagChat, Value: "chat", Twin: TwinNever})
			h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true, Concurrency: 1})
			checkPolicyReleased(t, it, i18n.Default)
			checkNoTwinWork(t, h)
		})
	}
}

func TestInboxPolicyNeverKeepsStartedAndSettledItems(t *testing.T) {
	h := agentHub(t)
	before := map[string]string{}
	for _, tc := range []struct{ id, agent, handling, session string }{
		{"running", AgentRunning, HandlingLooking, "running-session"},
		{"starting", AgentRunning, HandlingLooking, ""},
		{"queued-with-session", AgentQueued, HandlingLooking, "existing-session"},
		{"waiting", AgentSkipped, HandlingWaiting, "waiting-session"},
		{"suggestion", AgentDrafted, HandlingDone, "suggestion-session"},
		{"filed", AgentQueued, HandlingLooking, ""},
	} {
		it := queuePolicyItem(t, h, tc.id)
		it.Agent.State, it.Agent.SessionID = tc.agent, tc.session
		it.Handling.State, it.Handling.SessionID = tc.handling, tc.session
		if tc.id == "suggestion" {
			it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "Please send the details", At: time.Now()}
			it.Agent.Draft = it.Suggestion.Text
		}
		if tc.id == "filed" {
			now := time.Now()
			it.DoneAt, it.DoneBy = &now, "user"
		}
		before[it.ID] = stateOf(h, it)
	}
	setPolicyFor(t, h, InboxPolicy{Source: "example", Tag: connector.TagChat, Value: "chat", Twin: TwinNever})
	h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true})
	for id, want := range before {
		if got := stateOf(h, h.inbox.items[id]); got != want {
			t.Fatalf("policy changed %s after its start or completion", id)
		}
	}
	checkNoTwinWork(t, h)
}

func TestInboxPolicyNeverUsesEffectivePolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policies []InboxPolicy
		release  bool
	}{
		{"sender beats chat", []InboxPolicy{
			{Source: "example", Tag: connector.TagChat, Value: "chat", Twin: TwinNever},
			{Source: "example", Tag: connector.TagSender, Value: "sender", Twin: TwinFirst},
		}, false},
		{"chat beats repository", []InboxPolicy{
			{Source: "example", Tag: connector.TagRepo, Value: "repo", Twin: TwinNever},
			{Source: "example", Tag: connector.TagChat, Value: "chat", Twin: TwinFirst},
		}, false},
		{"sender blocks chat", []InboxPolicy{
			{Source: "example", Tag: connector.TagChat, Value: "chat", Twin: TwinFirst},
			{Source: "example", Tag: connector.TagSender, Value: "sender", Twin: TwinNever},
		}, true},
		{"another source", []InboxPolicy{{Source: "other", Tag: connector.TagChat, Value: "chat", Twin: TwinNever}}, false},
		{"another value", []InboxPolicy{{Source: "example", Tag: connector.TagChat, Value: "other", Twin: TwinNever}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := agentHub(t)
			it := queuePolicyItem(t, h, "queued")
			it.Connector.Tags = []connector.Tag{{Key: connector.TagRepo, Value: "repo"}, {Key: connector.TagChat, Value: "chat"},
				{Key: connector.TagSender, Value: "sender"}}
			h.inbox.policies = tc.policies
			h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true})
			if tc.release {
				checkPolicyReleased(t, it, i18n.Default)
				checkNoTwinWork(t, h)
			} else if it.Agent.State != AgentRunning || it.Agent.SessionID == "" || h.f.made != 1 {
				t.Fatalf("a nonwinning never rule blocked the item: %+v, starts=%d", it.Agent, h.f.made)
			}
		})
	}
}

// A failed item write leaves the stored queue recoverable, but cannot admit the item in this or the next pass.
func TestInboxPolicyNeverReleaseRollback(t *testing.T) {
	h, st := newBulkFailHub(t)
	it := queuePolicyItem(t, h, "queued")
	setPolicyFor(t, h, InboxPolicy{Source: "example", Tag: connector.TagChat, Value: "chat", Twin: TwinNever})
	before := stateOf(h, it)
	st.failAll = true
	h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true})
	if stateOf(h, it) != before || st.hasRow(it.ID) || changedTo(h, it.ID) {
		t.Fatal("a failed release was kept or announced")
	}
	checkNoTwinWork(t, h)
	st.failAll = false
	h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true})
	checkPolicyReleased(t, it, i18n.Default)
	if !st.hasRow(it.ID) || !changedTo(h, it.ID) {
		t.Fatal("release was not retried after the store recovered")
	}
	checkNoTwinWork(t, h)
}

// A queue persisted before a new rule is reconciled on restart without relying on the policy-saving request.
func TestInboxPolicyNeverQueueReload(t *testing.T) {
	h := agentHub(t)
	it := queuePolicyItem(t, h, "queued")
	ctx := context.Background()
	h.saveInbox(ctx, []*InboxItem{it})
	setPolicyFor(t, h, InboxPolicy{Source: "example", Tag: connector.TagChat, Value: "chat", Twin: TwinNever})
	reloaded := New(Options{Hub: h.f, Store: func() Store { return h.st }, Logf: testLogf(t)})
	if err := reloaded.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	reloaded.startInboxAgents(ctx, InboxAgentConfig{Enabled: true})
	checkPolicyReleased(t, reloaded.inbox.items[it.ID], i18n.Default)
	checkNoTwinWork(t, h)
}
