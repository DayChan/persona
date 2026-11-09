package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// changedTo reports whether an inbox.changed announcement for id went out.
func changedTo(h *inboxHub, id string) bool {
	for _, c := range h.in.changes() {
		for _, x := range c.Items {
			if x.ID == id {
				return true
			}
		}
	}
	return false
}

// stateOf is the item's stored form, under the inbox lock.
func stateOf(h *inboxHub, it *InboxItem) string {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	return string(itemState(it))
}

// handlingItem is a Needs you item whose processing conversation s1 is looking into it.
func handlingItem(h *inboxHub, id string) *InboxItem {
	now := time.Now()
	it := actionItem(id, now)
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	h.inbox.items[id] = it
	it.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "s1", Of: it.Last, StartedAt: &now}
	h.inbox.noteHandling(it, "server", "s1")
	h.inbox.setHandling(it, HandlingLooking, "", now)
	return it
}

// Every pass that writes inbox rows (#4366): with the store down the item gets back what its row holds (chain,
// handling, suggestion and filing included), no change event goes out, and the next pass converges once the store is
// back. The start is the exception: its conversation is live on a machine.
func TestInboxSaveFailureRollback(t *testing.T) {
	ctx := context.Background()
	t.Run("escalation", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		now := time.Now()
		it := actionItem("a", now)
		h.inbox.items["a"] = it
		before := stateOf(h, it)
		st.failAll = true
		h.saveWrites(ctx, h.inbox.escalate(now.Add(-time.Hour), now), "its escalation")
		if stateOf(h, it) != before || it.Agent != nil || it.Handling != nil || st.hasRow("a") || changedTo(h, "a") {
			t.Fatalf("the failed escalation stuck: agent %+v handling %+v chain %d", it.Agent, it.Handling, len(it.Chain))
		}
		st.failAll = false
		if ws := h.inbox.escalate(now.Add(-time.Hour), now); len(ws) != 1 || len(h.saveWrites(ctx, ws, "")) != 0 || !st.hasRow("a") {
			t.Fatal("the next pass did not escalate it again")
		}
	})
	t.Run("adoption", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		it := actionItem("a", time.Now())
		it.Agent = &InboxAgent{State: AgentQueued, Task: "t"}
		h.inbox.items["a"] = it
		before := stateOf(h, it)
		h.originSet(ctx, "server", "s-found", OriginInbox, it.ID, "")
		h.f.setSessions("server", proto.Session{SessionID: "s-found", Status: proto.StatusIdle})
		st.failAll = true
		h.startInboxAgents(ctx, InboxAgentConfig{Enabled: true})
		if stateOf(h, it) != before || it.Agent.SessionID != "" || h.f.made != 0 {
			t.Fatalf("the failed adoption stuck: %+v", it.Agent)
		}
		st.failAll = false
		h.startInboxAgents(ctx, InboxAgentConfig{Enabled: true})
		if it.Agent.SessionID != "s-found" || !st.hasRow("a") || h.f.made != 0 {
			t.Fatalf("the next pass did not adopt it: %+v", it.Agent)
		}
	})
	t.Run("start keeps the live conversation", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		it := actionItem("a", time.Now())
		it.Agent = &InboxAgent{State: AgentQueued, Task: "t"}
		h.inbox.items["a"] = it
		st.failAll = true
		h.startInboxAgents(ctx, InboxAgentConfig{Enabled: true})
		// Undone, the next pass would start a second conversation with a fresh attempt budget.
		if a := it.Agent; a.State != AgentRunning || a.SessionID == "" || a.Attempts != 1 || h.f.made != 1 || st.hasRow("a") {
			t.Fatalf("the failed start lost the live conversation: %+v (made %d)", a, h.f.made)
		}
		st.failAll = false
		h.startInboxAgents(ctx, InboxAgentConfig{Enabled: true})
		if h.f.made != 1 {
			t.Fatalf("the next pass started another conversation: %d", h.f.made)
		}
	})
	t.Run("start refused for now", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		it := actionItem("a", time.Now())
		it.Agent = &InboxAgent{State: AgentQueued, Task: "t"}
		h.inbox.items["a"] = it
		h.f.mu.Lock()
		h.f.failAssistant = errors.New("network down") // an unknown result: a conversation may exist
		h.f.mu.Unlock()
		st.failAll = true
		h.startInboxAgents(ctx, InboxAgentConfig{Enabled: true})
		// Its backoff and spent attempt survive the failed row: the attempt budget holds.
		if a := it.Agent; a.State != AgentQueued || a.RetryAfter == nil || a.Attempts != 1 {
			t.Fatalf("the temporary refusal lost its backoff: %+v", a)
		}
	})
	t.Run("sweep", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		it := handlingItem(h, "a")
		old := time.Now().Add(-time.Hour)
		it.Agent.StartedAt = &old
		before := stateOf(h, it)
		st.failAll = true
		h.sweepInboxAgents(ctx, InboxAgentConfig{}, time.Now())
		if stateOf(h, it) != before || it.Handling.State != HandlingLooking || st.hasRow("a") {
			t.Fatalf("the failed timeout stuck: %+v %+v", it.Agent, it.Handling)
		}
		st.failAll = false
		h.sweepInboxAgents(ctx, InboxAgentConfig{}, time.Now())
		if it.Handling.State != HandlingFailed || !st.hasRow("a") {
			t.Fatalf("the next sweep did not settle it: %+v", it.Handling)
		}
	})
	t.Run("suggestion", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		it := handlingItem(h, "a")
		agent := it.Agent
		before := stateOf(h, it)
		st.failAll = true
		if _, _, err := h.suggest(ctx, "s1", suggestIn{Kind: SuggestReply, Text: "好的，周五前给"}); !errors.Is(err, errInboxNotPersisted) {
			t.Fatalf("the conversation was not told the suggestion failed: %v", err)
		}
		if stateOf(h, it) != before || it.Suggestion != nil || it.Handling.State != HandlingLooking || it.Agent != agent || changedTo(h, "a") {
			t.Fatalf("the failed suggestion stuck: %+v %+v", it.Suggestion, it.Handling)
		}
		// The older inbox_draft says the same, not that the conversation is not an inbox one.
		if _, err := h.runTool(ctx, "server", "s1", "inbox_draft", json.RawMessage(`{"text":"好的"}`)); err == nil ||
			strings.Contains(err.Error(), "not started for an inbox item") || stateOf(h, it) != before {
			t.Fatalf("inbox_draft with the store down: %v", err)
		}
		st.failAll = false
		if _, _, err := h.suggest(ctx, "s1", suggestIn{Kind: SuggestReply, Text: "好的，周五前给"}); err != nil || it.Suggestion == nil || !st.hasRow("a") {
			t.Fatalf("handing it in again: %v %+v", err, it.Suggestion)
		}
	})
	t.Run("triage", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		h.in.setModel(fakeModel(t, `{"tier":"fyi","summary":"s","reason":"r"}`))
		it := actionItem("a", time.Now())
		it.Tier, it.TriagedAt, it.Read = "", "", true // read elsewhere: triage files it, and a failed save reopens it
		h.inbox.items["a"] = it
		before := stateOf(h, it)
		st.failAll = true
		w := h.triage(ctx, it)
		h.saveWrites(ctx, []*inboxWrite{w}, "its triage")
		if w == nil || stateOf(h, it) != before || !it.open() || it.TriagedAt != "" || st.hasRow("a") || changedTo(h, "a") {
			t.Fatalf("the failed triage stuck: %+v", it)
		}
		st.failAll = false
		h.saveWrites(ctx, []*inboxWrite{h.triage(ctx, it)}, "its triage")
		if it.open() || !st.hasRow("a") {
			t.Fatalf("the next pass did not converge: open=%v", it.open())
		}
	})
	t.Run("group keeps its events", func(t *testing.T) {
		// The batch is spent once its events are written: undone, they would never come back (and a connector with
		// meta would get stale meta). Memory keeps them; the row catches up at their next save.
		h, st := newBulkFailHub(t)
		now := time.Now()
		old := &InboxItem{ID: "example:th#m1", Source: "example", Kind: KindGroup, Tier: TierFYI, Last: "m0",
			TriagedAt: "m0", UpdatedAt: now, Context: "(m1)", Group: &InboxGroup{Connector: "example", Thread: "th"}}
		h.inbox.items[old.ID] = old
		c := assistantwire.InboxSource{ID: "example", Name: "Example"}
		thing := connector.InboxThing{Kind: KindGroup, Thread: "th", At: now, Last: "batch",
			Group: &connector.ThingGroup{New: []string{"m1", "m2"}}, Context: "(m1)\n(m2)"}
		plan := &groupPlan{model: true, events: []plannedEvent{
			{existing: "example:th#m1", messages: []string{"m1"}, title: "T1", tier: TierFYI, reason: "r"},
			{messages: []string{"m2"}, title: "T2", tier: TierFYI, reason: "r"},
		}}
		st.failAll = true
		h.writeGroup(ctx, c, thing, plan)
		if h.inbox.items["example:th#m2"] == nil || old.Last != "batch" || st.hasRow("example:th#m1") {
			t.Fatalf("the events were undone: m2=%v old.Last=%q", h.inbox.items["example:th#m2"], old.Last)
		}
		st.failAll = false
		h.saveInbox(ctx, []*InboxItem{old})
		if !st.hasRow("example:th#m1") {
			t.Fatal("the next save did not catch the row up")
		}
	})
	t.Run("group racing a dismiss", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		mux := h.routes()
		now := time.Now()
		id := "example:th#m1"
		old := &InboxItem{ID: id, Source: "example", Kind: KindGroup, Tier: TierFYI, Last: "m0",
			TriagedAt: "m0", UpdatedAt: now, Context: "(m1)", Group: &InboxGroup{Connector: "example", Thread: "th"}}
		h.inbox.items[id] = old
		c := assistantwire.InboxSource{ID: "example", Name: "Example"}
		thing := connector.InboxThing{Kind: KindGroup, Thread: "th", At: now, Last: "batch",
			Group: &connector.ThingGroup{New: []string{"m1", "m2"}}, Context: "(m1)\n(m2)"}
		plan := &groupPlan{model: true, events: []plannedEvent{
			{existing: id, messages: []string{"m1", "m2"}, title: "T1", tier: TierFYI, reason: "r"},
		}}
		second := make(chan int, 1)
		st.failOnce = map[string]bool{id: true}
		st.failHook = func(failedID string) {
			go func() {
				code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/"+url.PathEscape(failedID)+"/done", `{"done":true,"dismiss":true}`)
				second <- code
			}()
			waitForItem(t, h, failedID, func(it *InboxItem) bool { return it.DoneBy == "dismissed" })
		}
		h.writeGroup(ctx, c, thing, plan)
		if code := <-second; code != 200 {
			t.Fatalf("the concurrent dismiss: %d, want 200", code)
		}
		if it := h.inbox.items[id]; it.open() || it.DoneBy != "dismissed" || st.doneAt(id) == nil {
			t.Fatalf("the event's rollback rewound the dismissal: %+v", it)
		}
	})
	t.Run("mute", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		mux := h.routes()
		seedFailItems(h, "a")
		before := stateOf(h, h.inbox.items["a"])
		st.failAll = true
		if code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/mutes", `{"item_id":"a","tag":"sender"}`); code != http.StatusInternalServerError {
			t.Fatalf("mute with the store down: %d, want 500", code)
		}
		if it := h.inbox.items["a"]; !it.open() || stateOf(h, it) != before || st.hasRow("a") {
			t.Fatalf("the failed mute filed the item in memory (chain %d) or wrote a row", len(it.Chain))
		}
		st.failAll = false
		if code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/mutes", `{"item_id":"a","tag":"sender"}`); code != 200 || out["settled"] != float64(1) {
			t.Fatalf("the idempotent retry: %d %v", code, out)
		}
	})
	t.Run("mute in a round", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		seedFailItems(h, "a")
		it := h.inbox.items["a"]
		o := mutesOf(it)[0]
		h.inbox.mutes = []InboxMute{{ID: "m1", Source: it.Source, Tag: o.Tag, Value: o.Value, Name: "x"}}
		before := stateOf(h, it)
		st.failAll = true
		if err := h.InboxCollected(ctx, assistantwire.InboxRound{Round: roundPoll}); err != nil {
			t.Fatal(err)
		}
		if !it.open() || stateOf(h, it) != before || st.hasRow("a") {
			t.Fatalf("the round's failed mute filing stuck: done %v chain %d", it.DoneAt, len(it.Chain))
		}
		st.failAll = false
		if err := h.InboxCollected(ctx, assistantwire.InboxRound{Round: roundPoll}); err != nil || it.open() || !st.hasRow("a") {
			t.Fatalf("the next round did not file it: %v open=%v", err, it.open())
		}
	})
	t.Run("task link joins the escalation", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		h.in.setModel(fakeModel(t, `{"task_id":"todo_1"}`))
		now := time.Now()
		it := actionItem("a", now)
		h.inbox.items["a"] = it
		before := stateOf(h, it)
		ws := h.inbox.escalate(now.Add(-time.Hour), now)
		if len(ws) != 1 || !h.linkTaskFor(ctx, it, []prompts.InboxTask{{ID: "todo_1"}}, ws[0]) || it.Agent.TodoID != "todo_1" {
			t.Fatalf("not linked: %+v", it.Agent)
		}
		st.failAll = true
		h.saveWrites(ctx, ws, "its escalation")
		if stateOf(h, it) != before || it.Agent != nil {
			t.Fatalf("the linked escalation was not undone: %+v", it.Agent)
		}
	})
	t.Run("mute racing a dismiss", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		mux := h.routes()
		seedFailItems(h, "a")
		second := make(chan int, 1)
		st.failOnce = map[string]bool{"a": true}
		st.failHook = func(id string) {
			go func() {
				code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/a/done", `{"done":true,"dismiss":true}`)
				second <- code
			}()
			waitForItem(t, h, id, func(it *InboxItem) bool { return it.DoneBy == "dismissed" })
		}
		if code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/mutes", `{"item_id":"a","tag":"sender"}`); code != http.StatusInternalServerError {
			t.Fatalf("the failed mute: %d, want 500", code)
		}
		if code := <-second; code != 200 {
			t.Fatalf("the concurrent dismiss: %d, want 200", code)
		}
		if it := h.inbox.items["a"]; it.open() || it.DoneBy != "dismissed" || st.doneAt("a") == nil {
			t.Fatalf("the rollback rewound the dismissal: %+v", it)
		}
	})
}

// restore gives back the stored form and keeps the agent and handling records the item had (code holding them, like
// deliverNotes, still finds them), with the in-memory note count of before.
func TestInboxWriteRestoreKeepsRecords(t *testing.T) {
	h, _ := newBulkFailHub(t)
	it := handlingItem(h, "a")
	agent, handling := it.Agent, it.Handling
	h.inbox.mu.Lock()
	w := beginWrite(it)
	h.inbox.tell(it, inboxNote{Name: "new_messages", Context: "x"}, true, time.Now())
	h.inbox.failHandling(it, "timeout", time.Now())
	h.inbox.settle(w)
	held := h.inbox.holds(w)
	h.inbox.restore(w)
	h.inbox.mu.Unlock()
	if !held || it.Agent != agent || it.Handling != handling || it.Handling.State != HandlingLooking || len(it.Agent.Pending) != 0 || it.Agent.told != 0 {
		t.Fatalf("restore: held=%v agent %+v handling %+v", held, it.Agent, it.Handling)
	}
	// Something else moved it since: it is not the change's to undo.
	h.inbox.mu.Lock()
	w = beginWrite(it)
	it.Recap = "a"
	h.inbox.settle(w)
	it.Recap = "b"
	held = h.inbox.holds(w)
	h.inbox.mu.Unlock()
	if held {
		t.Fatal("a moved item still holds the change")
	}
}

// A new plugin item whose row could not be written is forgotten again (#4981): not listed, not announced, and no
// round is asked for at once, though more wait (it would only triage the same ones again). The connector hands it in
// again next round; with the store back it is triaged anew and comes in, stored.
func TestInboxAdmitRollsBackFailedRows(t *testing.T) {
	window, timeout := admitWindow, admitTimeout
	admitWindow, admitTimeout = 50*time.Millisecond, 5*time.Second
	t.Cleanup(func() { admitWindow, admitTimeout = window, timeout })
	model, client := newTriageModel(t, "slow", `{"tier":"action","summary":"示例","reason":"示例"}`)
	model.delay = 300 * time.Millisecond
	var things []connector.InboxThing
	for _, n := range []string{"21", "22", "23", "24", "25", "26"} {
		things = append(things, genericThing("example-org/example-repo#"+n, "a", "示例 "+n))
	}
	h, st := admitHub(t, client, &inboxConnector{things: things})
	listed := func() (n int) {
		for _, th := range things {
			if itemOf(h, "example", th.Thread) != nil {
				n++
			}
		}
		return n
	}

	st.writeFails(true)
	collectAndAdmit(h)
	if n := listed(); n != 0 || len(st.ids) != 0 || h.in.kicks != 0 {
		t.Fatalf("the failed admission stuck: %d listed, stored %v, %d kick(s)", n, st.ids, h.in.kicks)
	}
	for _, c := range h.in.changes() {
		if len(c.Items) != 0 {
			t.Fatalf("an item whose row failed was announced: %+v", c.Items)
		}
	}

	st.writeFails(false)
	asked := len(model.asked())
	collectAndAdmit(h)
	if n := len(model.asked()) - asked; n != triageConcurrency {
		t.Fatalf("the next round asked about %d, want %d", n, triageConcurrency)
	}
	if n := listed(); n != triageConcurrency || len(st.ids) != triageConcurrency || h.in.kicks == 0 {
		t.Fatalf("the next round: %d listed, stored %v, %d kick(s)", n, st.ids, h.in.kicks)
	}
}

// A translation whose row could not be written is undone (#4981): the item holds what its row does and nothing is
// announced. The next pass, with the store back, stores it from the cache: the model is not asked again.
func TestInboxTranslateRollsBackAFailedRow(t *testing.T) {
	h, en, _, translated := translateHub(t)
	ctx := context.Background()
	h.st.writeFails(true)
	h.translateInbox(ctx)
	h.passOver()
	h.inbox.mu.Lock()
	kept := en.Translations
	h.inbox.mu.Unlock()
	if kept != nil || h.st.saved(en.ID) || changedTo(h, en.ID) || translated() != 1 {
		t.Fatalf("the failed translation stuck: %+v (stored %v, %d asked)", kept, h.st.saved(en.ID), translated())
	}

	h.st.writeFails(false)
	h.translateInbox(ctx)
	h.passOver()
	h.inbox.mu.Lock()
	tr := en.Translations["zh-Hans"]
	h.inbox.mu.Unlock()
	if tr == nil || tr.Of != en.Last || !h.st.saved(en.ID) || !changedTo(h, en.ID) || translated() != 1 {
		t.Fatalf("the next pass: %+v (stored %v, %d asked)", tr, h.st.saved(en.ID), translated())
	}
}

// changedAfter reports whether an inbox.changed announcement for id went out after the first n.
func changedAfter(h *inboxHub, n int, id string) bool {
	for _, c := range h.in.changes()[n:] {
		for _, x := range c.Items {
			if x.ID == id {
				return true
			}
		}
	}
	return false
}

// announcedNothing reports whether every inbox.changed so far named no item.
func announcedNothing(h *inboxHub) bool {
	for _, c := range h.in.changes() {
		if len(c.Items) != 0 {
			return false
		}
	}
	return true
}

// storedItem is what the store's row of id holds (the zero item without one).
func storedItem(t *testing.T, st *inboxPutFailStore, id string) InboxItem {
	t.Helper()
	st.imu.Lock()
	raw := st.rows[id]
	st.imu.Unlock()
	var it InboxItem
	if raw != nil {
		if err := json.Unmarshal(raw, &it); err != nil {
			t.Fatal(err)
		}
	}
	return it
}

// mailItemsOf are the inbox's mail items.
func mailItemsOf(h *inboxHub) []*InboxItem {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	var out []*InboxItem
	for _, it := range h.inbox.items {
		if it.Mail != nil {
			out = append(out, it)
		}
	}
	return out
}

// roundHub is newBulkFailHub with the example connector (a plugin's when extension) holding things.
func roundHub(t *testing.T, extension bool, things ...connector.InboxThing) (*inboxHub, *inboxPutFailStore, *inboxConnector) {
	t.Helper()
	h, st := newBulkFailHub(t)
	c := &inboxConnector{things: things}
	h.addConnector(c, extension)
	return h, st, c
}

// round hands the inbox one round of the sources and waits for the passes it leaves running (triage, translation).
func (h *inboxHub) round(t *testing.T, kind string) {
	t.Helper()
	ctx := context.Background()
	if err := h.InboxCollected(ctx, h.in.read(ctx, kind)); err != nil {
		t.Fatal(err)
	}
	h.inbox.triaging.Lock()
	h.inbox.triaging.Unlock()
	h.passOver()
}

// What a round changed in items whose rows then fail is undone (#91): the item holds what its row does and nothing is
// announced. The sources hand the same things in again, so the next round, with the store back, brings them in.
func TestInboxRoundRollsBackFailedRows(t *testing.T) {
	now := time.Now()
	thread := thing("a", now).Thread
	t.Run("a new item is forgotten", func(t *testing.T) {
		h, st, _ := roundHub(t, false, thing("a", now))
		st.failAll = true
		h.round(t, roundPoll)
		if it := itemOf(h, "example", thread); it != nil || !announcedNothing(h) {
			t.Fatalf("the failed new item stuck: %+v", it)
		}
		st.failAll = false
		h.round(t, roundPoll)
		if it := itemOf(h, "example", thread); it == nil || !st.hasRow(it.ID) || !changedTo(h, it.ID) {
			t.Fatalf("the next round did not bring it in: %+v", it)
		}
	})
	t.Run("a plugin's new item a rule tiered is forgotten", func(t *testing.T) {
		h, st, _ := roundHub(t, true, thing("a", now))
		st.failAll = true
		h.round(t, roundPoll)
		if it := itemOf(h, "example", thread); it != nil || !announcedNothing(h) {
			t.Fatalf("the failed new item stuck: %+v", it)
		}
		st.failAll = false
		h.round(t, roundPoll)
		if it := itemOf(h, "example", thread); it == nil || !st.hasRow(it.ID) {
			t.Fatalf("the next round did not bring it in: %+v", it)
		}
	})
	t.Run("a new move is undone", func(t *testing.T) {
		h, st, c := roundHub(t, false, thing("a", now.Add(-time.Minute)))
		h.round(t, roundPoll)
		it := itemOf(h, "example", thread)
		before, n := stateOf(h, it), len(h.in.changes())
		c.things = []connector.InboxThing{thing("b", now)}
		st.failAll = true
		h.round(t, roundPoll)
		if stateOf(h, it) != before || itemOf(h, "example", thread) != it || changedAfter(h, n, it.ID) {
			t.Fatalf("the failed move stuck: last %q, count %d, chain %d", it.Last, it.Count, len(it.Chain))
		}
		st.failAll = false
		h.round(t, roundPoll)
		if it.Last != "b" || it.Count != 2 || storedItem(t, st, it.ID).Last != "b" {
			t.Fatalf("the next round did not move it: last %q, count %d, stored %q", it.Last, it.Count, storedItem(t, st, it.ID).Last)
		}
	})
	t.Run("only the failed rows are undone", func(t *testing.T) {
		one, two := thing("a", now.Add(-time.Minute)), thing("a", now.Add(-time.Minute))
		two.Thread = "example-org/example-repo#13"
		h, st, c := roundHub(t, false, one, two)
		h.round(t, roundPoll)
		a, b := itemOf(h, "example", one.Thread), itemOf(h, "example", two.Thread)
		before := stateOf(h, a)
		one.Last, two.Last = "b", "b"
		c.things = []connector.InboxThing{one, two}
		st.fail = map[string]bool{a.ID: true}
		h.round(t, roundPoll)
		if stateOf(h, a) != before || b.Last != "b" || storedItem(t, st, b.ID).Last != "b" {
			t.Fatalf("failed row: last %q; written row: last %q, stored %q", a.Last, b.Last, storedItem(t, st, b.ID).Last)
		}
	})
	t.Run("a thing that is over leaves its item open", func(t *testing.T) {
		h, st, c := roundHub(t, false, thing("a", now))
		h.round(t, roundPoll)
		it := itemOf(h, "example", thread)
		before := stateOf(h, it)
		over := thing("a", now)
		over.Done = connector.DoneRead
		c.things = []connector.InboxThing{over}
		st.failAll = true
		h.round(t, roundPoll)
		if !it.open() || stateOf(h, it) != before {
			t.Fatalf("the failed filing stuck: done by %q", it.DoneBy)
		}
		st.failAll = false
		h.round(t, roundPoll)
		if it.open() || st.doneAt(it.ID) == nil {
			t.Fatalf("the next round did not file it: open %v", it.open())
		}
	})
	t.Run("an item its connector settles stays open", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		h.addConnector(&settlingConnector{}, false)
		old := thing("a", now)
		old.Thread, old.Tier = "old:1", TierFYI
		h.inbox.mu.Lock()
		h.inbox.applyConnector("example", "Example", []connector.InboxThing{old}, now)
		h.inbox.mu.Unlock()
		it := itemOf(h, "example", old.Thread)
		before := stateOf(h, it)
		st.failAll = true
		h.round(t, roundPoll)
		if !it.open() || stateOf(h, it) != before {
			t.Fatalf("the failed settling stuck: done by %q, reason %q", it.DoneBy, it.Reason)
		}
		st.failAll = false
		h.round(t, roundPoll)
		if it.open() || it.Reason != "示例：按新规则合并" || st.doneAt(it.ID) == nil {
			t.Fatalf("the next round did not settle it: open %v, reason %q", it.open(), it.Reason)
		}
	})
	t.Run("an item moved and muted in one round is undone once", func(t *testing.T) {
		tagged := func(last string, at time.Time) connector.InboxThing {
			th := thing(last, at)
			th.Tags = []connector.Tag{{Key: connector.TagSender, Value: "bob", Name: "bob"}}
			return th
		}
		h, st, c := roundHub(t, false, tagged("a", now.Add(-time.Minute)))
		h.round(t, roundPoll)
		it := itemOf(h, "example", thread)
		before := stateOf(h, it)
		h.inbox.mu.Lock()
		h.inbox.mutes = []InboxMute{{ID: "m1", Source: "example", Tag: connector.TagSender, Value: "bob", Name: "bob"}}
		h.inbox.mu.Unlock()
		c.things = []connector.InboxThing{tagged("b", now)}
		st.failAll = true
		h.round(t, roundPoll)
		if !it.open() || stateOf(h, it) != before {
			t.Fatalf("the failed move and filing stuck: last %q, done by %q", it.Last, it.DoneBy)
		}
		st.failAll = false
		h.round(t, roundPoll)
		if it.open() || it.Last != "b" || st.doneAt(it.ID) == nil {
			t.Fatalf("the next round: open %v, last %q", it.open(), it.Last)
		}
	})

	mail := func(uid uint32) assistantwire.InboxMailSummary {
		return assistantwire.InboxMailSummary{Mailbox: "INBOX", UID: uid, Subject: "示例", From: MailAddress{Email: "x@example.com"},
			Date: now.UTC().Format(time.RFC3339), Snippet: fmt.Sprintf("摘要 %d", uid)}
	}
	// mailHub is a hub whose mail account has had UID 3 read into an item, stored.
	mailHub := func(t *testing.T) (*inboxHub, *inboxPutFailStore, *InboxItem) {
		t.Helper()
		h, st := newBulkFailHub(t)
		h.in.mail = &fakeMail{
			accounts: []assistantwire.InboxMailAccount{{ID: "a1", Email: "me@example.com", Messages: []assistantwire.InboxMailSummary{mail(3)}}},
			bodies:   map[uint32]assistantwire.InboxMailMessage{3: {Text: "正文 3"}, 4: {Text: "正文 4"}},
		}
		h.round(t, roundFull)
		items := mailItemsOf(h)
		if len(items) != 1 || items[0].BodyOf != "3" || items[0].Preview != "正文 3" || !st.hasRow(items[0].ID) {
			t.Fatalf("the first round: %+v", items)
		}
		return h, st, items[0]
	}
	newMail := func(h *inboxHub) {
		h.in.mu.Lock()
		h.in.mail.accounts[0].Messages = []assistantwire.InboxMailSummary{mail(3), mail(4)}
		h.in.mu.Unlock()
	}
	t.Run("new mail and its text are forgotten", func(t *testing.T) {
		h, st := newBulkFailHub(t)
		h.in.mail = &fakeMail{
			accounts: []assistantwire.InboxMailAccount{{ID: "a1", Email: "me@example.com", Messages: []assistantwire.InboxMailSummary{mail(3)}}},
			bodies:   map[uint32]assistantwire.InboxMailMessage{3: {Text: "正文 3"}},
		}
		st.failAll = true
		h.round(t, roundFull)
		if items := mailItemsOf(h); len(items) != 0 || !announcedNothing(h) {
			t.Fatalf("the failed mail item stuck: %+v", items)
		}
		st.failAll = false
		h.round(t, roundFull)
		if items := mailItemsOf(h); len(items) != 1 || items[0].Preview != "正文 3" || !st.hasRow(items[0].ID) {
			t.Fatalf("the next round did not bring it in: %+v", items)
		}
	})
	t.Run("new mail in an open item is undone with its text", func(t *testing.T) {
		h, st, it := mailHub(t)
		before := stateOf(h, it)
		newMail(h)
		st.failAll = true
		h.round(t, roundFull)
		if stateOf(h, it) != before || it.Last != "3" || it.Preview != "正文 3" {
			t.Fatalf("the failed mail stuck: last %q, body of %q, preview %q", it.Last, it.BodyOf, it.Preview)
		}
		st.failAll = false
		h.round(t, roundFull)
		if it.Last != "4" || it.BodyOf != "4" || it.Preview != "正文 4" || storedItem(t, st, it.ID).Preview != "正文 4" {
			t.Fatalf("the next round: last %q, body of %q, preview %q", it.Last, it.BodyOf, it.Preview)
		}
	})
	t.Run("a move between two steps is the other's", func(t *testing.T) {
		// The owner dismisses the item while the round reads the new mail's text (b.mu let go), and that row is
		// written; the round's own row then fails. Undone, the item keeps the dismissal its row holds.
		h, st, it := mailHub(t)
		newMail(h)
		var dismissed string
		h.in.mu.Lock()
		h.in.mail.fetched = func(uint32) {
			if code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+url.PathEscape(it.ID)+"/done", `{"done":true,"dismiss":true}`); code != 200 {
				t.Errorf("dismiss: %d", code)
			}
			dismissed = stateOf(h, it)
			st.failAll = true
		}
		h.in.mu.Unlock()
		h.round(t, roundFull)
		if it.open() || it.DoneBy != "dismissed" || st.doneAt(it.ID) == nil || stateOf(h, it) != dismissed {
			t.Fatalf("the round's undo rewound the dismissal: open %v, done by %q", it.open(), it.DoneBy)
		}
	})
}
