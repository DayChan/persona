package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

// What comes for an event while its conversation is being started is not in the first message (rendered before the
// start): it is still told once the start returns, and a new message that asks for work does not start a second one.
func TestInboxNotesToldWhileTheAgentStartsAreKept(t *testing.T) {
	for _, wake := range []bool{false, true} {
		t.Run(map[bool]string{false: "note", true: "waking note"}[wake], func(t *testing.T) {
			h := agentHub(t)
			h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
			it := actionItem("x", time.Now())
			it.Agent = &InboxAgent{State: AgentQueued, Task: "查 1013 是否合入"}
			h.inbox.items[it.ID] = it
			called := false
			hook := func(f *fakeHub, p map[string]any) {
				called = true
				h.inbox.mu.Lock()
				h.inbox.tell(it, inboxNote{Name: prompts.NoteNewMessages}, wake, time.Now())
				h.inbox.mu.Unlock()
			}
			h.f.mu.Lock()
			h.f.onStart, h.f.onAssistantSend = hook, hook
			h.f.mu.Unlock()
			h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true})
			h.f.mu.Lock()
			h.f.onStart, h.f.onAssistantSend = nil, nil
			h.f.mu.Unlock()
			if !called {
				t.Fatal("the start never reached the node")
			}
			h.inbox.mu.Lock()
			a := *it.Agent
			h.inbox.mu.Unlock()
			if a.SessionID == "" || a.State != AgentRunning {
				t.Fatalf("wake %v: the started conversation was not kept: %+v", wake, a)
			}
			h.f.mu.Lock()
			told := 0
			for _, p := range h.f.agentSent {
				if p["session_id"] == a.SessionID {
					told++
				}
			}
			h.f.mu.Unlock()
			if told != 1 || len(a.Pending) != 0 {
				t.Fatalf("wake %v: the note told during the start went into the conversation %d times, pending %+v", wake, told, a.Pending)
			}
		})
	}
}

// fullPending queues the most notes an event's conversation keeps waiting (tell trims to the latest 20).
func fullPending(h *inboxHub, it *InboxItem) {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	for i := range 20 {
		h.inbox.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: fmt.Sprintf("old-%d", i)}, false, time.Now())
	}
}

// With 20 notes already pending when the first message is made, a note told while the start is on its way still goes
// into the conversation: the trim to 20 keeps the list as long, so the carried notes are counted, not indexed.
func TestInboxNoteToldWhileTheAgentStartsWithFullPendingIsKept(t *testing.T) {
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	it := actionItem("x", time.Now())
	it.Agent = &InboxAgent{State: AgentQueued, Task: "查 1013 是否合入"}
	h.inbox.items[it.ID] = it
	fullPending(h, it)
	called := false
	hook := func(f *fakeHub, p map[string]any) {
		if called {
			return
		}
		called = true
		h.inbox.mu.Lock()
		h.inbox.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: "new-during-start"}, false, time.Now())
		h.inbox.mu.Unlock()
	}
	h.f.mu.Lock()
	h.f.onStart, h.f.onAssistantSend = hook, hook
	h.f.mu.Unlock()
	h.startInboxAgents(context.Background(), InboxAgentConfig{Enabled: true})
	h.f.mu.Lock()
	h.f.onStart, h.f.onAssistantSend = nil, nil
	h.f.mu.Unlock()
	if !called {
		t.Fatal("the start never reached the node")
	}
	h.inbox.mu.Lock()
	a := *it.Agent
	h.inbox.mu.Unlock()
	if a.SessionID == "" || a.State != AgentRunning {
		t.Fatalf("the started conversation was not kept: %+v", a)
	}
	h.f.mu.Lock()
	var told []string
	for _, p := range h.f.agentSent {
		if p["session_id"] == a.SessionID {
			told = append(told, p["text"].(string))
		}
	}
	h.f.mu.Unlock()
	if len(told) != 1 || !strings.Contains(told[0], "new-during-start") || strings.Contains(told[0], "old-") || len(a.Pending) != 0 {
		t.Fatalf("the note told during the start was not delivered alone: sent %q, pending %+v", told, a.Pending)
	}
}

// The same holds for a delivery: with 20 notes sent, one told while the send is on its way waits for the next one.
func TestInboxNoteToldWhileDeliveringFullPendingIsKept(t *testing.T) {
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	it := actionItem("x", time.Now())
	it.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "s1", Task: "查 1013 是否合入"}
	h.inbox.items[it.ID] = it
	fullPending(h, it)
	called := false
	h.f.mu.Lock()
	h.f.onSend = func(f *fakeHub, p map[string]any) {
		if called {
			return
		}
		called = true
		h.inbox.mu.Lock()
		h.inbox.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: "new-during-send"}, false, time.Now())
		h.inbox.mu.Unlock()
	}
	h.f.mu.Unlock()
	h.deliverNotes(context.Background(), it)
	h.f.mu.Lock()
	h.f.onSend = nil
	h.f.mu.Unlock()
	if !called {
		t.Fatal("the delivery never reached the node")
	}
	h.inbox.mu.Lock()
	pending := append([]inboxNote(nil), it.Agent.Pending...)
	h.inbox.mu.Unlock()
	if len(pending) != 1 || pending[0].Text != "new-during-send" {
		t.Fatalf("the note told during the delivery was not kept for the next one: %+v", pending)
	}
}

// An event closed while a delivery is on its way still tells its conversation to stop: the closed note is queued
// after the sent notes, so it waits for the next delivery instead of being trimmed with them.
func TestInboxClosedNoteWhileDeliveringIsKept(t *testing.T) {
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	it := actionItem("x", time.Now())
	it.Handling = &InboxHandling{State: HandlingAgent}
	it.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "s1", Task: "查 1013 是否合入"}
	h.inbox.items[it.ID] = it
	h.inbox.mu.Lock()
	h.inbox.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: "before-close"}, false, time.Now())
	h.inbox.mu.Unlock()
	called, closed := false, false
	h.f.mu.Lock()
	h.f.onSend = func(f *fakeHub, p map[string]any) {
		if called {
			return
		}
		called = true
		h.inbox.mu.Lock()
		closed = h.inbox.closeHandling(it, "replied", time.Now())
		h.inbox.mu.Unlock()
	}
	h.f.mu.Unlock()
	h.deliverNotes(context.Background(), it)
	h.f.mu.Lock()
	h.f.onSend = nil
	h.f.mu.Unlock()
	if !called || !closed {
		t.Fatalf("the close during the delivery did not run or had nothing to tell: called %v, closed %v", called, closed)
	}
	h.inbox.mu.Lock()
	pending := append([]inboxNote(nil), it.Agent.Pending...)
	h.inbox.mu.Unlock()
	if len(pending) != 1 || pending[0].Name != prompts.NoteClosed {
		t.Fatalf("the closed note told during the delivery was not kept for the next one: %+v", pending)
	}
}
