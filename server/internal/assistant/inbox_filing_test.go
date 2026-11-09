package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// filingItem includes shared nested draft data and a full pending queue, including the in-memory delivery count.
func filingItem(h *inboxHub, id string) *InboxItem {
	it := mailItem(id, "sender@example.com", "Example")
	it.Title = "Example"
	now := time.Now().UTC()
	changes := []DocChange{{Field: "status", Value: "complete"}}
	it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingLooking, Since: &now}
	it.Agent = &InboxAgent{State: AgentDrafted, Wake: true, Draft: "Keep this draft", Note: "Keep this context",
		Changes: changes, DraftedAt: &now, StartedAt: &now, Back: HandlingWaiting, Lost: []string{"old-session"}}
	it.Suggestion = &InboxSuggestion{Version: 7, Kind: SuggestReply, Text: it.Agent.Draft, Note: it.Agent.Note, Changes: changes, At: now}
	for i := range 20 {
		it.Agent.queueNote(inboxNote{Name: prompts.NoteInstructed, Text: fmt.Sprintf("pending-%d", i)})
	}
	it.Chain = make([]InboxChainEntry, 1, chainMax+1)
	it.Chain[0] = InboxChainEntry{ID: "c1", Kind: ChainReceived, Text: "Original message", At: now,
		Link:     &InboxChainLink{Type: LinkSource, URL: "https://example.com/message"},
		Messages: []InboxChainMessage{{Sender: "Example", Text: "Original message", At: &now}}}
	it.ChainSeq, it.SuggestSeq = 1, 7
	h.inbox.items[id] = it
	return it
}

func filingRequest(h *inboxHub, bulk bool, ids ...string) bool {
	if !bulk {
		_, err := h.setInboxDone(context.Background(), ids[0], true, false)
		return err == errInboxNotPersisted
	}
	targets := make([]inboxBulkTarget, len(ids))
	for i, id := range ids {
		targets[i].ID = id
	}
	res := h.setInboxDoneBulk(context.Background(), targets, true, time.Now())
	return len(res.Failed) > 0
}

func TestInboxFilingFailurePreservesHandling(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprint("bulk=", bulk), func(t *testing.T) {
			h, st := newBulkFailHub(t)
			it := filingItem(h, "item")
			before, told := stateOf(h, it), it.Agent.told
			storedBefore, err := json.Marshal(it)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.inboxMemStore.PutInboxItem(context.Background(), it.ID, storedBefore, it.UpdatedAt, it.DoneAt); err != nil {
				t.Fatal(err)
			}
			agent, handling, suggestion := it.Agent, it.Handling, it.Suggestion
			st.failAll = true
			if !filingRequest(h, bulk, it.ID) {
				t.Fatal("filing did not report the failed row")
			}
			if got := stateOf(h, it); got != before || it.Agent.told != told {
				t.Fatalf("failed filing changed the item:\nbefore: %s\nafter:  %s\nnotes: %d -> %d", before, got, told, it.Agent.told)
			}
			if it.Agent != agent || it.Handling != handling || it.Suggestion != suggestion {
				t.Fatal("failed filing replaced records held by an in-flight operation")
			}
			st.imu.Lock()
			storedAfter := slices.Clone(st.rows[it.ID])
			st.imu.Unlock()
			if !bytes.Equal(storedBefore, storedAfter) || changedTo(h, it.ID) {
				t.Fatal("failed filing changed the persisted row or was announced")
			}
			st.failAll = false
			if filingRequest(h, bulk, it.ID) {
				t.Fatal("filing did not recover")
			}
			st.imu.Lock()
			var row InboxItem
			err = json.Unmarshal(st.rows[it.ID], &row)
			st.imu.Unlock()
			if err != nil || row.open() || row.Suggestion != nil || row.Agent.Wake || row.Agent.Draft != "" || row.Handling.State != HandlingDone {
				t.Fatalf("successful retry did not persist the complete closure: %s, %v", stateOf(h, &row), err)
			}
		})
	}
}

func TestInboxFilingRollbackPrecedesQueuedHandlingSave(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprint("bulk=", bulk), func(t *testing.T) {
			h, st := newBulkFailHub(t)
			it := filingItem(h, "item")
			st.failOnce = map[string]bool{it.ID: true}
			queued, saved := make(chan struct{}), make(chan struct{})
			st.failHook = func(string) {
				go func() {
					h.inbox.mu.Lock()
					it.Title = "Concurrent title"
					it.Agent.queueNote(inboxNote{Name: prompts.NoteNewMessages, Text: "Queued update"})
					h.inbox.mu.Unlock()
					close(queued)
					h.saveInbox(context.Background(), []*InboxItem{it})
					close(saved)
				}()
				select {
				case <-queued:
				case <-time.After(5 * time.Second):
					t.Error("the concurrent update did not queue its save")
				}
			}
			if !filingRequest(h, bulk, it.ID) {
				t.Fatal("filing did not fail")
			}
			select {
			case <-saved:
			case <-time.After(5 * time.Second):
				t.Fatal("the queued save did not finish")
			}
			st.imu.Lock()
			var row InboxItem
			err := json.Unmarshal(st.rows[it.ID], &row)
			st.imu.Unlock()
			if err != nil || !row.open() || row.Suggestion == nil || !row.Agent.Wake || row.Agent.Draft != "Keep this draft" ||
				row.Handling.State != HandlingLooking || row.Title != "Concurrent title" || len(row.Agent.Pending) != 20 || row.Agent.Pending[19].Text != "Queued update" ||
				stateOf(h, &row) != stateOf(h, it) {
				t.Fatalf("the queued save kept a failed closure or lost concurrent handling: %s, %v", stateOf(h, &row), err)
			}
		})
	}
}

func TestInboxFilingFailureKeepsConcurrentUpdates(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprint("bulk=", bulk), func(t *testing.T) {
			h, st := newBulkFailHub(t)
			it := filingItem(h, "item")
			other := filingItem(h, "other")
			agent, suggestion := it.Agent, it.Suggestion
			st.failAll = true
			st.failHook = func(string) {
				h.inbox.mu.Lock()
				defer h.inbox.mu.Unlock()
				it.Title, it.Recap = "Concurrent title", "Concurrent recap"
				suggestion.Changes[0].Value = "review"
				agent.queueNote(inboxNote{Name: prompts.NoteNewMessages, Text: "Concurrent message"})
				it.Parent = &InboxParent{Kind: ParentInbox, ID: other.ID, By: ParentByUser}
				h.inbox.touched(it.ID) // a parent edit has its own ownership, separate from filing
				h.inbox.chainAdd(it, InboxChainEntry{Kind: ChainInstructed, Text: "Concurrent instruction"})
				other.Title = "Unrelated update"
			}
			if !filingRequest(h, bulk, it.ID) {
				t.Fatal("filing did not report the failed row")
			}
			if !it.open() || it.Title != "Concurrent title" || it.Recap != "Concurrent recap" || other.Title != "Unrelated update" ||
				it.Agent != agent || it.Suggestion != suggestion || !agent.Wake || agent.State != AgentDrafted ||
				len(agent.Changes) != 1 || agent.Changes[0].Value != "review" || suggestion.Changes[0].Value != "review" ||
				len(agent.Pending) != 20 || agent.Pending[19].Text != "Concurrent message" || agent.told != 21 {
				t.Fatalf("failure lost a concurrent update or retained closure: %s", stateOf(h, it))
			}
			if !reflect.DeepEqual(agent.Changes, suggestion.Changes) {
				t.Fatal("the shared nested draft changes diverged")
			}
			if len(it.Chain) != 2 || it.Chain[1].Text != "Concurrent instruction" || it.ChainSeq != 3 || it.Parent == nil || it.Parent.ID != other.ID {
				t.Fatalf("failure lost the concurrent chain/parent: %+v / %+v", it.Chain, it.Parent)
			}
		})
	}
}

// filingProbeStore observes the real per-row persistence boundary, including batch fallback and delivery saves.
type filingProbeStore struct {
	*inboxPutFailStore
	before        func(id string, data []byte)
	beforeContext func(context.Context)
}

func (s *filingProbeStore) PutInboxItem(ctx context.Context, id string, data []byte, updated time.Time, done *time.Time) error {
	if s.beforeContext != nil {
		s.beforeContext(ctx)
	}
	if s.before != nil {
		s.before(id, data)
	}
	return s.inboxPutFailStore.PutInboxItem(ctx, id, data, updated, done)
}

type filingSecondSave struct{}

func filingConversation(h *inboxHub, it *InboxItem, sid string) {
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	it.Agent.MachineID, it.Agent.SessionID = "server", sid
	it.Handling.MachineID, it.Handling.SessionID = "server", sid
	h.f.setSessions("server", proto.Session{SessionID: sid, Status: proto.StatusWorking})
}

func TestInboxFilingDoesNotDeliverBeforeCommit(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprint("bulk=", bulk), func(t *testing.T) {
			h, st := newBulkFailHub(t)
			it := filingItem(h, "item")
			filingConversation(h, it, "filing-session")
			it.Agent.Pending, it.Agent.told = nil, 0
			h.f.failSend = errors.New("synthetic offline conversation")
			st.failAll = true
			probe := &filingProbeStore{inboxPutFailStore: st, before: func(_ string, data []byte) {
				var candidate InboxItem
				if err := json.Unmarshal(data, &candidate); err != nil {
					t.Fatal(err)
				}
				if candidate.Suggestion != nil || candidate.Agent.Draft != "" || candidate.Handling.State != HandlingDone ||
					len(candidate.Agent.Pending) != 1 || candidate.Agent.Pending[0].Name != prompts.NoteClosed {
					t.Fatalf("the row did not stage the whole closure: %s", data)
				}
				// This pass runs while the candidate write is still in progress. A failed send avoids a nested save,
				// letting the test observe even the old implementation's premature closure delivery deterministically.
				h.deliverNotes(context.Background(), it)
				h.f.mu.Lock()
				defer h.f.mu.Unlock()
				for _, c := range h.f.calls {
					if c.Method == DeviceSessionSend {
						t.Error("the conversation was told to stop before its row committed")
					}
				}
			}}
			h.store = func() Store { return probe }
			if !filingRequest(h, bulk, it.ID) {
				t.Fatal("the failed candidate did not surface as a failure")
			}
			// An explicit pass removes scheduling luck from the negative delivery assertion after rollback too.
			h.deliverNotes(context.Background(), it)
			if len(h.f.sentToAgents()) != 0 || it.Suggestion == nil || !it.Agent.Wake {
				t.Fatal("the failed candidate leaked into live handling")
			}
		})
	}
}

func TestInboxBulkFilingPartialSuccessDeliversOnlyCommittedRows(t *testing.T) {
	h, st := newBulkFailHub(t)
	var items []*InboxItem
	for _, id := range []string{"a", "b", "c"} {
		it := filingItem(h, id)
		filingConversation(h, it, "conversation-"+id)
		it.Agent.Pending, it.Agent.told = nil, 0
		items = append(items, it)
	}
	beforeFailed := stateOf(h, items[1])
	st.fail = map[string]bool{"b": true}
	var mu sync.Mutex
	seen := map[string]bool{}
	probe := &filingProbeStore{inboxPutFailStore: st, before: func(id string, data []byte) {
		mu.Lock()
		defer mu.Unlock()
		if seen[id] {
			return
		}
		seen[id] = true
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		it := h.inbox.items[id]
		if it.Suggestion == nil || it.Agent.Draft == "" || !it.Agent.Wake || len(it.Agent.Pending) != 0 {
			t.Errorf("%s exposed its closure before persistence: %s", id, itemState(it))
		}
	}}
	h.store = func() Store { return probe }
	closedText := renderNote(inboxNote{Name: prompts.NoteClosed, Text: "user"}, h.owner(context.Background()))
	h.f.onSend = func(_ *fakeHub, p map[string]any) {
		sid := p["session_id"].(string)
		id := strings.TrimPrefix(sid, "conversation-")
		st.imu.Lock()
		defer st.imu.Unlock()
		var row InboxItem
		if err := json.Unmarshal(st.rows[id], &row); err != nil || row.open() || row.Suggestion != nil || row.Handling.State != HandlingDone ||
			len(row.Agent.Pending) != 1 || row.Agent.Pending[0].Name != prompts.NoteClosed || p["text"] != closedText {
			t.Errorf("delivery preceded the complete durable closure of %s: %s, %v", id, st.rows[id], err)
		}
	}
	res := h.setInboxDoneBulk(context.Background(), []inboxBulkTarget{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "a"}}, true, time.Now())
	if !slices.Equal(res.Failed, []string{"b"}) || len(res.Changed) != 2 || res.Changed[0].ID != "a" || res.Changed[1].ID != "c" {
		t.Fatalf("partial filing result: %+v", res)
	}
	for _, it := range items {
		h.deliverNotes(context.Background(), it) // serialize with tellSoon and drain each successful note
	}
	sent := h.f.sentToAgents()
	slices.Sort(sent)
	if len(sent) != 2 || sent[0] != "conversation-a: "+closedText || sent[1] != "conversation-c: "+closedText {
		t.Fatalf("closure deliveries: %q", sent)
	}
	if stateOf(h, items[1]) != beforeFailed || st.hasRow("b") || changedTo(h, "b") {
		t.Fatal("the failed row changed or was announced")
	}
	for _, it := range []*InboxItem{items[0], items[2]} {
		if it.Suggestion != nil || it.Agent.Wake || it.Agent.Draft != "" || it.Handling.State != HandlingDone || !changedTo(h, it.ID) {
			t.Fatalf("successful filing did not commit its closure: %s", stateOf(h, it))
		}
	}
	// Retrying the same bulk operation leaves the successes alone and closes only the recovered failed row.
	st.fail = nil
	res = h.setInboxDoneBulk(context.Background(), []inboxBulkTarget{{ID: "a"}, {ID: "b"}, {ID: "c"}}, true, time.Now())
	h.deliverNotes(context.Background(), items[1])
	if len(res.Failed) != 0 || len(res.Changed) != 1 || res.Changed[0].ID != "b" || len(h.f.sentToAgents()) != 3 {
		t.Fatalf("retry did not close only b once: %+v, %q", res, h.f.sentToAgents())
	}
}

func TestInboxFilingFailureRestoresCappedChain(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprint("bulk=", bulk), func(t *testing.T) {
			h, st := newBulkFailHub(t)
			it := filingItem(h, "item")
			for len(it.Chain) < chainMax {
				h.inbox.chainAdd(it, InboxChainEntry{Kind: ChainInstructed, Text: "Existing instruction"})
			}
			before := stateOf(h, it)
			st.failAll = true
			if !filingRequest(h, bulk, it.ID) || stateOf(h, it) != before {
				t.Fatal("failed filing lost the head of a full chain")
			}
		})
	}
}

func TestInboxFilingSuccessPersistsAndTellsOnce(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, why string
	}{
		{"single done", "POST", "/api/v1/inbox/item/done", `{"done":true}`, "user"},
		{"single dismiss", "POST", "/api/v1/inbox/item/done", `{"done":true,"dismiss":true}`, "dismissed"},
		{"bulk done", "POST", "/api/v1/inbox/bulk", `{"action":"done","ids":["item"]}`, "user"},
		{"bulk dismiss", "POST", "/api/v1/inbox/bulk", `{"action":"dismiss","ids":["item"]}`, "dismissed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st := newBulkFailHub(t)
			it := filingItem(h, "item")
			filingConversation(h, it, "filing-session")
			closedText := renderNote(inboxNote{Name: prompts.NoteClosed, Text: tc.why}, h.owner(context.Background()))
			h.f.onSend = func(_ *fakeHub, p map[string]any) {
				st.imu.Lock()
				defer st.imu.Unlock()
				var row InboxItem
				if err := json.Unmarshal(st.rows[it.ID], &row); err != nil || row.open() || row.DoneBy != tc.why || row.Suggestion != nil ||
					row.Agent.Wake || row.Agent.Draft != "" || row.Handling.State != HandlingDone ||
					p["session_id"] != "filing-session" || p["text"] != closedText {
					t.Errorf("closure delivery did not match the durable row: %s, %v", st.rows[it.ID], err)
				}
			}
			mux := h.routes()
			if code, _ := inboxCall(t, mux, tc.method, tc.path, tc.body); code != 200 {
				t.Fatalf("filing status: %d", code)
			}
			h.deliverNotes(context.Background(), it)
			if code, _ := inboxCall(t, mux, tc.method, tc.path, tc.body); code != 200 {
				t.Fatalf("idempotent retry status: %d", code)
			}
			h.deliverNotes(context.Background(), it)
			if got := h.f.sentToAgents(); len(got) != 1 || got[0] != "filing-session: "+closedText {
				t.Fatalf("closure deliveries: %q", got)
			}
			if len(it.Chain) != 2 || it.Chain[1].Kind != ChainDone || it.ChainSeq != 2 || st.doneAt(it.ID) == nil {
				t.Fatalf("filing/retry changed the chain or lost the row: %+v", it.Chain)
			}
		})
	}
}

func TestInboxFilingSuccessKeepsNewerHandling(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprint("bulk=", bulk), func(t *testing.T) {
			h, st := newBulkFailHub(t)
			it := filingItem(h, "item")
			filingConversation(h, it, "old-conversation")
			newAgent := &InboxAgent{State: AgentDrafted, MachineID: "server", SessionID: "new-conversation", Draft: "New draft", Wake: true}
			newSuggestion := &InboxSuggestion{Version: 8, Kind: SuggestReply, Text: "New draft", At: time.Now().UTC()}
			var once sync.Once
			probe := &filingProbeStore{inboxPutFailStore: st, before: func(string, []byte) {
				once.Do(func() {
					h.inbox.mu.Lock()
					it.Agent, it.Suggestion = newAgent, newSuggestion
					it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingLooking, MachineID: "server", SessionID: "new-conversation"}
					h.inbox.mu.Unlock()
				})
			}}
			h.store = func() Store { return probe }
			if filingRequest(h, bulk, it.ID) {
				t.Fatal("the filing row failed")
			}
			// The writer that replaced handling owns its following save, queued after the filing row.
			h.saveInbox(context.Background(), []*InboxItem{it})
			h.deliverNotes(context.Background(), it)
			if it.Agent != newAgent || it.Suggestion != newSuggestion || it.Agent.Draft != "New draft" || !it.Agent.Wake ||
				it.Handling.State != HandlingLooking || len(h.f.sentToAgents()) != 0 {
				t.Fatalf("an older closure stopped newer handling: %s", stateOf(h, it))
			}
			st.imu.Lock()
			var row InboxItem
			err := json.Unmarshal(st.rows[it.ID], &row)
			st.imu.Unlock()
			if err != nil || row.Suggestion == nil || row.Suggestion.Version != 8 || row.Agent.SessionID != "new-conversation" {
				t.Fatalf("the newer handling was not persisted: %+v, %v", row.Agent, err)
			}
		})
	}
}

func TestInboxFilingConcurrentOutcomes(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		for _, firstFail := range []bool{false, true} {
			for _, secondFail := range []bool{false, true} {
				for _, reopen := range []bool{false, true} {
					name := fmt.Sprintf("bulk=%v/firstFail=%v/secondFail=%v/reopen=%v", bulk, firstFail, secondFail, reopen)
					t.Run(name, func(t *testing.T) {
						h, st := newBulkFailHub(t)
						it := filingItem(h, "item")
						filingConversation(h, it, "filing-session")
						it.Agent.Pending, it.Agent.told = nil, 0
						before := stateOf(h, it)
						second := make(chan error, 1)
						var once sync.Once
						probe := &filingProbeStore{inboxPutFailStore: st, beforeContext: func(ctx context.Context) {
							if ctx.Value(filingSecondSave{}) != nil {
								st.failAll = secondFail
								return
							}
							st.failAll = false // delivery acknowledgement saves are independent of the two request faults
							once.Do(func() {
								st.failAll = firstFail
								go func() {
									ctx := context.WithValue(context.Background(), filingSecondSave{}, true)
									_, err := h.setInboxDone(ctx, it.ID, !reopen, true)
									second <- err
								}()
								waitForItem(t, h, it.ID, nil)
							})
						}}
						h.store = func() Store { return probe }
						if failed := filingRequest(h, bulk, it.ID); failed != firstFail {
							t.Fatalf("first failure = %v, want %v", failed, firstFail)
						}
						select {
						case err := <-second:
							if (err == errInboxNotPersisted) != secondFail {
								t.Fatalf("second error = %v, secondFail = %v", err, secondFail)
							}
						case <-time.After(5 * time.Second):
							t.Fatal("the second filing did not finish")
						}
						h.deliverNotes(context.Background(), it)
						wantDone := !reopen
						if secondFail {
							wantDone = !firstFail
						}
						if it.open() == wantDone {
							t.Fatalf("final done = %v, want %v: %s", !it.open(), wantDone, stateOf(h, it))
						}
						closed := !firstFail || (!secondFail && !reopen)
						if closed {
							if it.Suggestion != nil || it.Agent.Wake || it.Agent.Draft != "" || it.Handling.State != HandlingDone {
								t.Fatalf("a committed filing lost its closure: %s", stateOf(h, it))
							}
						} else if got := stateOf(h, it); got != before {
							t.Fatalf("uncommitted filings changed handling or chain:\nbefore %s\nafter %s", before, got)
						}
						wantDeliveries := 0
						if closed {
							wantDeliveries = 1
						}
						if got := h.f.sentToAgents(); len(got) != wantDeliveries {
							t.Fatalf("closure deliveries = %q, want %d", got, wantDeliveries)
						}
						if !firstFail || !secondFail {
							st.imu.Lock()
							var row InboxItem
							err := json.Unmarshal(st.rows[it.ID], &row)
							st.imu.Unlock()
							if err != nil || stateOf(h, &row) != stateOf(h, it) {
								t.Fatalf("stored and live outcomes differ: %s / %s, %v", stateOf(h, &row), stateOf(h, it), err)
							}
						}
					})
				}
			}
		}
	}
}

func TestInboxFilingCommitsDuringEarlierDelivery(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprint("bulk=", bulk), func(t *testing.T) {
			h, st := newBulkFailHub(t)
			it := filingItem(h, "item")
			filingConversation(h, it, "filing-session")
			h.inbox.lang = nil // avoid the fake node's held lock also blocking the chain's locale lookup
			sent, release := make(chan struct{}), make(chan struct{})
			var once, releaseOnce sync.Once
			releaseSend := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseSend()
			h.f.onSend = func(_ *fakeHub, _ map[string]any) {
				once.Do(func() {
					close(sent)
					select {
					case <-release:
					case <-time.After(5 * time.Second):
						t.Error("the earlier delivery was not released")
					}
				})
			}
			delivered := make(chan struct{})
			go func() {
				h.deliverNotes(context.Background(), it)
				close(delivered)
			}()
			select {
			case <-sent:
			case <-time.After(5 * time.Second):
				t.Fatal("the earlier delivery never started")
			}
			var writeOnce sync.Once
			probe := &filingProbeStore{inboxPutFailStore: st, before: func(string, []byte) {
				writeOnce.Do(func() {
					releaseSend()
					waitForItem(t, h, it.ID, func(it *InboxItem) bool { return len(it.Agent.Pending) == 0 && !it.Agent.Wake })
				})
			}}
			h.store = func() Store { return probe }
			if filingRequest(h, bulk, it.ID) {
				t.Fatal("filing failed")
			}
			select {
			case <-delivered:
			case <-time.After(5 * time.Second):
				t.Fatal("the earlier delivery's queued save never finished")
			}
			h.deliverNotes(context.Background(), it)
			closedText := renderNote(inboxNote{Name: prompts.NoteClosed, Text: "user"}, h.owner(context.Background()))
			got := h.f.sentToAgents()
			if len(got) != 2 || got[1] != "filing-session: "+closedText || it.Suggestion != nil || it.Agent.Draft != "" ||
				it.Agent.Wake || it.Handling.State != HandlingDone || len(it.Agent.Pending) != 0 {
				t.Fatalf("earlier delivery lost the committed closure: %q / %s", got, stateOf(h, it))
			}
		})
	}
}

func TestInboxUnrelatedSaveExcludesPendingFilings(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			h, st := newBulkFailHub(t)
			it := filingItem(h, "item")
			var writes []*inboxFiling
			// Hold the requests just before their save turn, without relying on mutex waiter scheduling.
			h.inbox.mu.Lock()
			for range count {
				w := h.inbox.beginFiling(it, time.Now(), "user")
				h.inbox.file(it, w.now, w.why)
				w.close = true
				h.inbox.settleFiling(w)
				writes = append(writes, w)
			}
			it.Title = "Unrelated saved title"
			h.inbox.mu.Unlock()
			h.saveInbox(context.Background(), []*InboxItem{it})
			st.imu.Lock()
			var row InboxItem
			err := json.Unmarshal(st.rows[it.ID], &row)
			st.imu.Unlock()
			if err != nil || !row.open() || row.Title != "Unrelated saved title" || row.Suggestion == nil || !row.Agent.Wake ||
				row.Agent.Draft != "Keep this draft" || len(row.Chain) != 1 || row.ChainSeq != 1 {
				t.Fatalf("unrelated save made a pending filing durable: %s, %v", stateOf(h, &row), err)
			}
			st.failAll = true
			for _, w := range writes {
				if failed := h.saveInboxFilings(context.Background(), []*inboxFiling{w}); !failed[it.ID] {
					t.Fatal("the pending filing did not fail")
				}
			}
			if stateOf(h, &row) != stateOf(h, it) {
				t.Fatalf("rollback lost the unrelated save: %s / %s", stateOf(h, &row), stateOf(h, it))
			}
		})
	}
}

func TestInboxNewerFilingCommitSupersedesOlderPendingWrite(t *testing.T) {
	h, st := newBulkFailHub(t)
	it := filingItem(h, "item")
	var writes []*inboxFiling
	h.inbox.mu.Lock()
	for _, why := range []string{"user", "dismissed"} {
		w := h.inbox.beginFiling(it, time.Now(), why)
		h.inbox.file(it, w.now, w.why)
		w.close = true
		h.inbox.settleFiling(w)
		writes = append(writes, w)
	}
	h.inbox.mu.Unlock()
	if failed := h.saveInboxFilings(context.Background(), writes[1:]); len(failed) != 0 {
		t.Fatal("the newer filing failed")
	}
	st.failAll = true
	if failed := h.saveInboxFilings(context.Background(), writes[:1]); !failed[it.ID] {
		t.Fatal("the superseded filing did not report its failed row")
	}
	st.imu.Lock()
	var row InboxItem
	err := json.Unmarshal(st.rows[it.ID], &row)
	st.imu.Unlock()
	if err != nil || it.open() || it.DoneBy != "dismissed" || len(it.Chain) != 2 || it.Chain[1].ID != "c3" ||
		stateOf(h, &row) != stateOf(h, it) {
		t.Fatalf("the older failed row rewound the committed filing: %s / %s, %v", stateOf(h, &row), stateOf(h, it), err)
	}
}

func TestInboxFilingPreservesHandlingChangedBeforeSave(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("fail=", fail), func(t *testing.T) {
			h, st := newBulkFailHub(t)
			it := filingItem(h, "item")
			filingConversation(h, it, "old-conversation")
			h.inbox.mu.Lock()
			w := h.inbox.beginFiling(it, time.Now(), "user")
			h.inbox.file(it, w.now, w.why)
			w.close = true
			h.inbox.settleFiling(w)
			// A newer result lands after the request's action, before its row snapshot can acquire saving.
			it.Agent = &InboxAgent{State: AgentDrafted, MachineID: "server", SessionID: "new-conversation", Draft: "New draft", Wake: true}
			it.Suggestion = &InboxSuggestion{Version: 8, Kind: SuggestReply, Text: "New draft", At: time.Now().UTC()}
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingLooking, MachineID: "server", SessionID: "new-conversation"}
			h.inbox.mu.Unlock()
			st.failAll = fail
			if failed := h.saveInboxFilings(context.Background(), []*inboxFiling{w}); failed[it.ID] != fail {
				t.Fatalf("failed ids = %v, want failure %v", failed, fail)
			}
			h.deliverNotes(context.Background(), it)
			if it.open() != fail || it.Suggestion == nil || it.Suggestion.Version != 8 || it.Agent.Draft != "New draft" ||
				!it.Agent.Wake || it.Handling.State != HandlingLooking || len(h.f.sentToAgents()) != 0 {
				t.Fatalf("filing closed newer handling before its own save: %s", stateOf(h, it))
			}
		})
	}
}
