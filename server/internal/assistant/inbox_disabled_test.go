package assistant

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

func disabledTwinItem(h *inboxHub, id, handling, agent, session string) *InboxItem {
	now := time.Now().UTC()
	retry := now.Add(time.Hour)
	it := &InboxItem{ID: id, Source: "example", Kind: KindDM, Tier: TierAction, Title: "Example request",
		Last: "1", TriagedAt: "1", Context: "Please review this request", UpdatedAt: now,
		Agent: &InboxAgent{State: agent, MachineID: "server", SessionID: session, Of: "1", StartedAt: &now,
			Wake: true, Back: HandlingWaiting, RetryAfter: &retry,
			Pending: []inboxNote{{Name: prompts.NoteNewMessages, Context: "Please review this request"}}}}
	if handling != "" {
		it.Handling = &InboxHandling{Owner: HandlingOwner, State: handling, MachineID: "server", SessionID: session, Since: &now}
	}
	h.inbox.items[id] = it
	return it
}

func checkTwinReleased(t *testing.T, it *InboxItem, lang i18n.Lang) {
	t.Helper()
	want := i18n.T(lang, "assistant-inbox_agent.disabled")
	if want == "assistant-inbox_agent.disabled" || want == "" {
		t.Fatal("the disabled reason is missing from the catalog")
	}
	if !it.open() || it.status() != StatusAction || it.handled() || it.Handling == nil ||
		it.Handling.State != HandlingFailed || it.Handling.Error != want || it.Suggestion != nil {
		t.Fatalf("item was not returned to Needs you: status=%s handling=%+v suggestion=%+v", it.status(), it.Handling, it.Suggestion)
	}
	if a := it.Agent; a != nil && (a.State != AgentFailed || a.Error != want || len(a.Pending) != 0 || a.Wake || a.Back != "" || a.RetryAfter != nil) {
		t.Fatalf("disabled twin kept queued work: %+v", a)
	}
	if len(it.Chain) == 0 || it.Chain[len(it.Chain)-1].Kind != ChainFailed || it.Chain[len(it.Chain)-1].Detail != want {
		t.Fatalf("missing explanation in the handling chain: %+v", it.Chain)
	}
}

func checkNoTwinWork(t *testing.T, h *inboxHub) {
	t.Helper()
	h.f.mu.Lock()
	defer h.f.mu.Unlock()
	if h.f.made != 0 || len(h.f.agentSent) != 0 {
		t.Fatalf("disabled twin dispatched work: starts=%d messages=%v", h.f.made, h.f.agentSent)
	}
}

// The normal background entry point must settle existing work even with the switch off.
func TestInboxDisabledReleasesAgents(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := agentHub(t)
				h.f.hello.Locale = string(lang)
				h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
				var items []*InboxItem
				for _, tc := range []struct{ id, handling, agent, session string }{
					{"queued", HandlingLooking, AgentQueued, ""},
					{"running", HandlingLooking, AgentRunning, "running-session"},
					{"delegated", HandlingAgent, AgentRunning, "delegated-session"},
					{"starting", HandlingLooking, AgentRunning, ""},
					{"legacy-queued", "", AgentQueued, ""},
					{"legacy-running", "", AgentRunning, "legacy-session"},
				} {
					items = append(items, disabledTwinItem(h, tc.id, tc.handling, tc.agent, tc.session))
				}
				// A partial stored item still has to leave Working without an Agent record.
				orphan := disabledTwinItem(h, "orphan", HandlingLooking, AgentRunning, "")
				orphan.Agent = nil
				items = append(items, orphan)
				fresh := &InboxItem{ID: "fresh", Source: "example", Kind: KindDM, Tier: TierAction, Last: "1", TriagedAt: "1",
					Context: "A new request", UpdatedAt: time.Now()}
				h.inbox.items[fresh.ID] = fresh

				h.handInbox(context.Background())
				synctest.Wait()
				before := map[string]string{}
				for _, it := range items {
					checkTwinReleased(t, it, lang)
					if !h.st.saved(it.ID) || !changedTo(h, it.ID) {
						t.Fatalf("release of %s was not saved and announced", it.ID)
					}
					before[it.ID] = stateOf(h, it)
				}
				if fresh.Agent != nil || fresh.Handling != nil {
					t.Fatal("a new item was promoted with the twin disabled")
				}
				h.handPass(context.Background())
				for _, it := range items {
					if got := stateOf(h, it); got != before[it.ID] {
						t.Fatalf("a repeated disabled pass changed %s again", it.ID)
					}
				}
				checkNoTwinWork(t, h)

				// The released state, including cleared pending notes, survives a manager restart.
				reloaded := New(Options{Hub: h.f, Store: func() Store { return h.st }, Logf: testLogf(t)})
				if err := reloaded.inboxReady(context.Background()); err != nil {
					t.Fatal(err)
				}
				for _, it := range items {
					got := reloaded.inbox.items[it.ID]
					if got == nil {
						t.Fatalf("%s was lost on reload", it.ID)
					}
					checkTwinReleased(t, got, lang)
				}
			})
		})
	}
}

func TestInboxDisabledPreservesSettledItems(t *testing.T) {
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	waiting := disabledTwinItem(h, "waiting", HandlingWaiting, AgentSkipped, "waiting-session")
	done := disabledTwinItem(h, "done", HandlingDone, AgentSkipped, "done-session")
	now := time.Now().UTC()
	done.DoneAt, done.DoneBy = &now, "user"
	suggestion := disabledTwinItem(h, "suggestion", HandlingDone, AgentDrafted, "suggestion-session")
	suggestion.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "Please send the details", At: now}
	suggestion.Agent.Draft = suggestion.Suggestion.Text
	h.handPass(context.Background())
	if waiting.Handling.State != HandlingWaiting || waiting.status() != StatusWorking || !waiting.open() {
		t.Fatalf("an already confirmed action changed: %+v", waiting)
	}
	if done.open() || done.Handling.State != HandlingDone || done.DoneBy != "user" {
		t.Fatalf("a filed item was reopened: %+v", done)
	}
	if suggestion.Suggestion == nil || suggestion.Suggestion.Text != "Please send the details" || suggestion.Agent.Draft != suggestion.Suggestion.Text ||
		suggestion.Handling.State != HandlingDone || suggestion.status() != StatusAction {
		t.Fatalf("an existing suggestion was discarded: %+v", suggestion)
	}
	for _, it := range []*InboxItem{waiting, done, suggestion} {
		if len(it.Agent.Pending) != 0 || it.Agent.Wake || it.Agent.Back != "" || it.Agent.RetryAfter != nil {
			t.Fatalf("settled item %s retained pending work: %+v", it.ID, it.Agent)
		}
	}
	checkNoTwinWork(t, h)
}

func TestInboxDisabledSuggestionActions(t *testing.T) {
	for _, session := range []string{"", "existing-session"} {
		for _, action := range []string{suggestReject, suggestInstruct, "handback"} {
			t.Run(session+"/"+action, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					h := agentHub(t)
					h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
					it := disabledTwinItem(h, "suggestion", HandlingDone, AgentDrafted, session)
					it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "Please send the details", At: time.Now()}
					words := "Ask for the deadline first"
					in := suggestionReq{Version: 1, Action: action}
					kind := ChainRejected
					if action == suggestInstruct {
						in.Text, kind = &words, ChainInstructed
					}
					if action == "handback" {
						in.Action, it.Suggestion.Handback, kind = suggestConfirm, true, ChainConfirmed
					}
					out, err := h.actOnSuggestion(context.Background(), it.ID, in, true)
					if err != nil {
						t.Fatal(err)
					}
					checkTwinReleased(t, &out, i18n.Default)
					if len(out.Chain) < 2 || out.Chain[len(out.Chain)-2].Kind != kind {
						t.Fatalf("the user's action was not recorded: %+v", out.Chain)
					}
					synctest.Wait()
					checkTwinReleased(t, it, i18n.Default)
					checkNoTwinWork(t, h)
				})
			})
		}
	}
}

func TestInboxDisabledDeliveryAndRollback(t *testing.T) {
	for _, path := range []string{"pass", "delivery"} {
		t.Run(path, func(t *testing.T) {
			h, st := newBulkFailHub(t)
			h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
			it := disabledTwinItem(h, "item", HandlingLooking, AgentRunning, "existing-session")
			before := stateOf(h, it)
			run := func() {
				if path == "delivery" {
					h.deliverNotes(context.Background(), it)
				} else {
					h.handPass(context.Background())
				}
			}
			st.failAll = true
			run()
			if stateOf(h, it) != before || changedTo(h, it.ID) || st.hasRow(it.ID) {
				t.Fatal("a failed release was kept or announced")
			}
			checkNoTwinWork(t, h)
			st.failAll = false
			run()
			checkTwinReleased(t, it, i18n.Default)
			if !st.hasRow(it.ID) || !changedTo(h, it.ID) {
				t.Fatal("the next release did not persist and announce the item")
			}
			checkNoTwinWork(t, h)
		})
	}
}

func TestInboxDisabledSuggestionWriteFailure(t *testing.T) {
	h, st := newBulkFailHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	it := disabledTwinItem(h, "suggestion", HandlingDone, AgentDrafted, "existing-session")
	it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "Please send the details", At: time.Now()}
	before := stateOf(h, it)
	st.failAll = true
	in := suggestionReq{Version: 1, Action: suggestReject}
	if _, err := h.actOnSuggestion(context.Background(), it.ID, in, true); !errors.Is(err, errInboxNotPersisted) {
		t.Fatalf("failed release returned %v, want a persistence error", err)
	}
	if stateOf(h, it) != before || changedTo(h, it.ID) {
		t.Fatal("the rejected suggestion was lost despite its failed write")
	}
	st.failAll = false
	out, err := h.actOnSuggestion(context.Background(), it.ID, in, true)
	if err != nil {
		t.Fatal(err)
	}
	checkTwinReleased(t, &out, i18n.Default)
	checkNoTwinWork(t, h)
}

func TestInboxDisabledLateAgentStart(t *testing.T) {
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	it := disabledTwinItem(h, "item", HandlingLooking, AgentRunning, "parent-session")
	ctx := context.Background()
	h.handPass(ctx)
	checkTwinReleased(t, it, i18n.Default)
	h.inboxAgentStarted(ctx, "parent-session", "server", "child-session", "Inspect the request")
	if it.Handling.State != HandlingFailed || it.status() != StatusAction || it.handled() || it.Agent.State != AgentFailed {
		t.Fatalf("a late sub-session start resumed the disabled twin: %+v", it.Handling)
	}
	entry := it.Chain[len(it.Chain)-1]
	if entry.Kind != ChainAgent || entry.Link == nil || entry.Link.SessionID != "child-session" {
		t.Fatalf("the started sub-session lost its history link: %+v", entry)
	}
	checkNoTwinWork(t, h)
}

func TestInboxDisabledConfirmAgent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := agentHub(t)
		h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
		it := disabledTwinItem(h, "item", HandlingDone, AgentSkipped, "parent-session")
		it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestAgent, Text: "Inspect the request", At: time.Now(),
			MachineID: "server", Cwd: "/code/example"}
		out, err := h.actOnSuggestion(context.Background(), it.ID, suggestionReq{Version: 1, Action: suggestConfirm}, true)
		if err != nil {
			t.Fatal(err)
		}
		checkTwinReleased(t, &out, i18n.Default)
		synctest.Wait()
		h.f.mu.Lock()
		starts := len(h.f.starts)
		h.f.mu.Unlock()
		if starts != 1 {
			t.Fatalf("the explicitly confirmed Agent started %d times, want 1", starts)
		}
		checkNoTwinWork(t, h)
		linked := false
		for _, entry := range it.Chain {
			if entry.Kind == ChainAgent && entry.Link != nil && entry.Link.SessionID != "" {
				linked = true
			}
		}
		if !linked {
			t.Fatal("the confirmed Agent has no link in the item's history")
		}
	})
}

func TestInboxDisabledNewMessagesRetriage(t *testing.T) {
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	it := disabledTwinItem(h, "item", HandlingLooking, AgentRunning, "parent-session")
	h.handPass(context.Background())
	checkTwinReleased(t, it, i18n.Default)
	model, client := newTriageModel(t, "ok", `{"tier":"fyi","summary":"No response needed","reason":"Informational update"}`)
	h.in.setModel(client)
	h.inbox.mu.Lock()
	it.Last, it.Context = "2", "The request has already been resolved"
	h.inbox.mu.Unlock()
	h.triageInbox(context.Background())
	// triageInbox takes this lock before returning and releases it after saving the result.
	h.inbox.triaging.Lock()
	h.inbox.triaging.Unlock()
	h.inbox.handing.Lock()
	h.inbox.handing.Unlock()
	if len(model.asked()) != 1 || it.TriagedAt != "2" || it.Tier != TierFYI || it.Summary != "No response needed" {
		t.Fatalf("new messages did not retriage after release: calls=%d item=%+v", len(model.asked()), it)
	}
	checkNoTwinWork(t, h)
}
