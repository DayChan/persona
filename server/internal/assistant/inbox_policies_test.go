package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

func groupItem(id, feed, sender string, tier string) *InboxItem {
	return &InboxItem{ID: id, Source: "lark", Kind: KindGroup, Title: "示例项目群", Sender: "示例同事", Tier: tier, Reason: "模型的理由",
		Summary: "模型的摘要", Last: "2", TriagedAt: "2", Context: "示例同事：今天发版吗？", UpdatedAt: time.Now(),
		Lark: &InboxLark{FeedID: feed, IsGroup: true, SenderID: sender}}
}

func setPolicyFor(t *testing.T, h *inboxHub, p InboxPolicy) InboxPolicy {
	t.Helper()
	got, _, err := h.setPolicy(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The most specific policy wins: a sender's over its chat's; a policy of another source or value does not apply.
func TestPolicyOfPrecedence(t *testing.T) {
	h := muteHub(t)
	it := groupItem("lark:f1:2", "f1", "u1", TierFYI)
	setPolicyFor(t, h, InboxPolicy{Source: "lark", Tag: "chat", Value: "f1", Name: "示例项目群", Tier: TierNoise})
	setPolicyFor(t, h, InboxPolicy{Source: "mail", Tag: "sender", Value: "u1", Tier: TierAction})
	b := &h.inbox
	b.mu.Lock()
	if p := b.policyOf(it); p == nil || p.Tag != "chat" {
		t.Fatalf("chat policy: %+v", p)
	}
	b.mu.Unlock()
	setPolicyFor(t, h, InboxPolicy{Source: "lark", Tag: "sender", Value: "u1", Name: "示例同事", Tier: TierAction, Twin: TwinFirst})
	b.mu.Lock()
	defer b.mu.Unlock()
	if p := b.policyOf(it); p == nil || p.Tag != "sender" || b.policyTwin(it) != TwinFirst {
		t.Fatalf("sender policy: %+v", p)
	}
	if p := b.policyOf(groupItem("x", "f2", "u2", TierFYI)); p != nil {
		t.Fatalf("another chat got %+v", p)
	}
}

// Setting a policy from an item: bad input is refused, the open triaged items it covers get its tier (the reason
// says so, the summary stays, urgency goes with Needs you), an item being handled keeps its tier; it is listed,
// changed, kept across a restart, removed when it sets nothing, and deleting leaves the tier it gave.
func TestPolicyFromAnItem(t *testing.T) {
	h := muteHub(t)
	ctx := context.Background()
	a := groupItem("a", "f1", "u1", TierAction)
	a.Urgent = true
	h.inbox.items["a"] = a
	handled := groupItem("h", "f1", "u3", TierAction)
	handled.Agent = &InboxAgent{State: AgentRunning, SessionID: "s1"}
	handled.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
	h.inbox.items["h"] = handled
	other := groupItem("o", "f2", "u2", TierAction)
	h.inbox.items["o"] = other
	mux := h.routes()

	for body, want := range map[string]int{
		`{"item_id":"nope","tag":"chat","tier":"fyi"}`: 404,
		`{"item_id":"a","tag":"repo","tier":"fyi"}`:    400,
		`{"item_id":"a","tag":"chat","tier":"urgent"}`: 400,
		`{"item_id":"a","tag":"chat","twin":"always"}`: 400,
	} {
		if code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/policies", body); code != want {
			t.Errorf("%s: %d, want %d", body, code, want)
		}
	}
	code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/policies", `{"item_id":"a","tag":"chat","tier":"fyi"}`)
	if code != 200 || out["applied"] != float64(1) {
		t.Fatalf("set: %d %v", code, out)
	}
	if a.Tier != TierFYI || a.Reason != "按你的规则：示例项目群" || a.Summary != "模型的摘要" || a.Urgent {
		t.Fatalf("covered item: %+v", a)
	}
	if handled.Tier != TierAction || other.Tier != TierAction {
		t.Fatalf("handled %s, other %s", handled.Tier, other.Tier)
	}
	_, out = inboxCall(t, mux, "GET", "/api/v1/inbox/policies", "")
	list := out["policies"].([]any)
	if len(list) != 1 {
		t.Fatalf("policies %v", out)
	}
	p := list[0].(map[string]any)
	if p["source_name"] != "飞书" || p["value"] != "f1" || p["tier"] != "fyi" || p["name"] != "示例项目群" {
		t.Fatalf("listed %v", p)
	}
	id := p["id"].(string)
	// Setting it again from another item of the chat changes the same policy.
	if code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/policies", `{"item_id":"h","tag":"chat","tier":"noise","twin":"never"}`); code != 200 ||
		len(h.listedPolicies()) != 1 || a.Tier != TierNoise {
		t.Fatalf("again: %d %v %s", code, h.listedPolicies(), a.Tier)
	}
	if code, out := inboxCall(t, mux, "PATCH", "/api/v1/inbox/policies/"+id, `{"tier":"","twin":"never"}`); code != 200 ||
		out["policy"].(map[string]any)["twin"] != "never" || out["policy"].(map[string]any)["tier"] != nil {
		t.Fatalf("patch: %d %v", code, out)
	}
	if code, _ := inboxCall(t, mux, "PATCH", "/api/v1/inbox/policies/nope", `{"tier":"fyi"}`); code != 404 {
		t.Fatalf("patch of nothing: %d", code)
	}

	// Kept: the manager started again has it.
	h2 := New(Options{Hub: h.f, Store: func() Store { return h.st }, Logf: testLogf(t)})
	h2.inboxReady(ctx)
	if got := h2.listedPolicies(); len(got) != 1 || got[0].Twin != TwinNever {
		t.Fatalf("after a restart: %+v", got)
	}

	// Setting nothing removes it; deleting leaves what it did.
	if code, _ := inboxCall(t, mux, "PATCH", "/api/v1/inbox/policies/"+id, `{}`); code != 200 || len(h.listedPolicies()) != 0 {
		t.Fatalf("cleared: %d %v", code, h.listedPolicies())
	}
	setPolicyFor(t, h, InboxPolicy{Source: "lark", Tag: "chat", Value: "f2", Name: "另一个群", Tier: TierFYI})
	id = h.listedPolicies()[0].ID
	if code, _ := inboxCall(t, mux, "DELETE", "/api/v1/inbox/policies/"+id, ""); code != 200 || len(h.listedPolicies()) != 0 {
		t.Fatalf("delete: %d", code)
	}
	if other.Tier != TierFYI {
		t.Fatalf("deleting changed the tier: %s", other.Tier)
	}
}

// The model's triage keeps its summary, the policy decides the tier.
func TestPolicyOverridesTriage(t *testing.T) {
	_, client := newTriageModel(t, "ok", `{"tier":"fyi","summary":"示例同事问发版","reason":"群聊闲谈"}`)
	h, _ := admitHub(t, client)
	it := groupItem("lark:f1:3", "f1", "u1", "")
	it.Last, it.TriagedAt, it.Reason, it.Summary = "3", "", "", ""
	h.inbox.items[it.ID] = it
	setPolicyFor(t, h, InboxPolicy{Source: "lark", Tag: "sender", Value: "u1", Name: "示例同事", Tier: TierAction})
	if !h.triageOne(context.Background(), it) {
		t.Fatal("not triaged")
	}
	if it.Tier != TierAction || it.Summary != "示例同事问发版" || it.Reason != "按你的规则：示例同事" || it.TriagedAt != "3" {
		t.Fatalf("triaged: %+v", it)
	}
}

// The twin's part: never keeps a Needs you item from it, first hands it what is not Needs you and starts it first.
func TestPolicyTwin(t *testing.T) {
	now := time.Now()
	h := agentHub(t)
	kept := actionItem("lark:f1:12", now)
	kept.Source = "lark"
	fyi := groupItem("lark:f2:2", "f2", "u2", TierFYI)
	plain := actionItem("lark:f3:12", now.Add(time.Minute))
	plain.Source, plain.Lark = "lark", &InboxLark{FeedID: "f3"}
	for _, it := range []*InboxItem{kept, fyi, plain} {
		h.inbox.items[it.ID] = it
	}
	setPolicyFor(t, h, InboxPolicy{Source: "lark", Tag: "chat", Value: "f1", Twin: TwinNever})
	setPolicyFor(t, h, InboxPolicy{Source: "lark", Tag: "chat", Value: "f2", Twin: TwinFirst})
	got := h.inbox.escalate(now.Add(-time.Hour), now)
	if len(got) != 2 || kept.Agent != nil || fyi.Agent == nil || plain.Agent == nil {
		t.Fatalf("escalated %d: kept %+v fyi %+v plain %+v", len(got), kept.Agent, fyi.Agent, plain.Agent)
	}
	queued := []*InboxItem{plain, fyi}
	h.inbox.mu.Lock()
	h.inbox.sortQueued(queued)
	h.inbox.mu.Unlock()
	if queued[0] != fyi {
		t.Fatalf("queue: %s first", queued[0].ID)
	}
}

// A policy whose items' rows fail to save says so and leaves those items as their rows hold them: the old tier, no
// announcement, no row. The policy itself stays kept, and setting it again (the call or the route) applies it.
func TestPolicyPersistFailureRollsBack(t *testing.T) {
	h, st := newBulkFailHub(t)
	ctx := context.Background()
	mux := h.routes()
	it := mailItem("a", "shop@example.com", "Shop")
	it.Tier, it.Reason, it.Last, it.TriagedAt = TierAction, "the model's reason", "1", "1"
	h.inbox.mu.Lock()
	h.inbox.items["a"] = it
	h.inbox.mu.Unlock()
	before := stateOf(h, it)
	policy := InboxPolicy{Source: "mail", Tag: connector.TagSender, Value: "shop@example.com", Name: "Shop", Tier: TierNoise}

	st.failAll = true
	if _, applied, err := h.setPolicy(ctx, policy); !errors.Is(err, errInboxNotPersisted) || applied != 0 {
		t.Fatalf("set with the rows failing: applied=%d err=%v", applied, err)
	}
	if code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/policies", `{"item_id":"a","tag":"sender","tier":"noise"}`); code != http.StatusInternalServerError {
		t.Fatalf("the route answered %d %v with the rows failing", code, out)
	}
	if after := stateOf(h, it); after != before || changedTo(h, "a") || st.hasRow("a") {
		t.Fatalf("the failed tier stuck: %s (announced %v, row %v)", after, changedTo(h, "a"), st.hasRow("a"))
	}
	if got := h.listedPolicies(); len(got) != 1 || got[0].Tier != TierNoise {
		t.Fatalf("the policy was not kept: %+v", got)
	}

	st.failAll = false
	if _, applied, err := h.setPolicy(ctx, policy); err != nil || applied != 1 {
		t.Fatalf("retry: applied=%d err=%v", applied, err)
	}
	st.imu.Lock()
	raw := st.rows["a"]
	st.imu.Unlock()
	var row InboxItem
	if err := json.Unmarshal(raw, &row); err != nil || row.Tier != TierNoise || !changedTo(h, "a") {
		t.Fatalf("the retry did not write the tier: row %q err %v announced %v", row.Tier, err, changedTo(h, "a"))
	}
	h.inbox.mu.Lock()
	tier := it.Tier
	h.inbox.mu.Unlock()
	if tier != TierNoise || len(h.listedPolicies()) != 1 {
		t.Fatalf("after the retry: tier %s, policies %+v", tier, h.listedPolicies())
	}
}
