package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// delegationMachineTrust gives s devices with machine at trust. The environment's todo link started a token send to
// the machine, which reads s.devs (#6545): the devices are swapped only once that send is done.
func delegationMachineTrust(t *testing.T, s *Service, machine, trust string) {
	t.Helper()
	tokenSendDone(t, s, machine)
	d := newFleetDevs()
	d.add(machine, "Computer").trust = trust
	s.devs = d
}

// tokenSendDone waits until no todo-token send to machine runs (syncTodoTokens). The send clears sending under ts.mu
// after its last read, so what the test writes next comes after it.
func tokenSendDone(t *testing.T, s *Service, machine string) {
	t.Helper()
	ts := s.tokenSyncOf(machine)
	deadline := time.Now().Add(5 * time.Second)
	for {
		ts.mu.Lock()
		sending := ts.sending
		ts.mu.Unlock()
		if !sending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the todo-token send to %s did not end", machine)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDelegationProjectContextRespectsMachineTrust(t *testing.T) {
	for _, trust := range []string{"", "personal", "work"} {
		t.Run("trust="+trust, func(t *testing.T) {
			s, f, _ := outcomeEnv(t, nil, outcomeReply)
			delegationMachineTrust(t, s, "box", trust)
			ctx := context.Background()
			const prompt = "Add dark mode"
			args, _ := json.Marshal(map[string]string{"machine": "box", "cwd": "/code/weather", "prompt": prompt})
			if _, err := s.runTool(ctx, "server", "conv9", "agent_start", args); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			if len(f.starts) != 1 {
				t.Fatalf("starts = %+v", f.starts)
			}
			got, _ := f.starts[0]["prompt"].(string)
			f.mu.Unlock()
			if trust == "work" && got != prompt {
				t.Fatalf("work computer received project context: %q", got)
			}
			if trust != "work" && !strings.Contains(got, "<persona-context") {
				t.Fatalf("personal computer lost project context: %q", got)
			}
			// The app's new-Agent handoff uses the same policy as agent_start.
			other, err := s.createTodo(ctx, "Add dark mode", "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			handoff, err := s.TodoHandoff(ctx, TodoHandoff{TodoID: other.ID, Text: prompt, New: true,
				MachineID: "box", Cwd: "/code/weather"})
			if err != nil {
				t.Fatal(err)
			}
			if trust == "work" && handoff.Text != prompt || trust != "work" && !strings.Contains(handoff.Text, "<persona-context") {
				t.Fatalf("handoff = %q", handoff.Text)
			}
		})
	}
}

func TestAssistantStartRefusesWorkBeforePreparing(t *testing.T) {
	for _, guest := range []bool{false, true} {
		f := newFakeHub().withMachine()
		s := newStorelessService(t, f)
		delegationMachineTrust(t, s, "server", "work")
		p := StartParams{Text: "Private request", Untitled: true, MachineID: "server"}
		if guest {
			p.GuestTools = []string{}
		}
		if _, err := s.Start(context.Background(), p); !errors.Is(err, errWorkAssistant) {
			t.Fatalf("guest=%v: start error = %v", guest, err)
		}
		if _, err := s.prepareSpace(context.Background(), "server", ""); !errors.Is(err, errWorkAssistant) {
			t.Fatalf("prepare error = %v", err)
		}
		if len(f.prepared) != 0 || len(f.guestPrepared) != 0 || f.made != 0 {
			t.Fatalf("guest=%v: private instructions reached work computer", guest)
		}
	}
}

func TestInboxAgentMachineTrust(t *testing.T) {
	for _, trust := range []string{"", "personal", "work"} {
		for _, explicit := range []bool{false, true} {
			t.Run("trust="+trust+"/explicit="+map[bool]string{true: "true", false: "false"}[explicit], func(t *testing.T) {
				h := agentHub(t)
				machine := "server"
				cfg := InboxAgentConfig{Enabled: true}
				if explicit {
					machine, cfg.Machine = "twin", "twin"
					h.f.setSessions(machine)
				}
				delegationMachineTrust(t, h.Service, machine, trust)
				it := actionItem("event", time.Now())
				mid, sid, err := h.startInboxAgent(context.Background(), cfg, it, "Handle this message")
				if trust == "work" {
					if (!errors.Is(err, errWorkAssistant) && !errors.Is(err, errNoInboxMachine)) || !definiteStartError(err) || h.f.made != 0 || len(h.f.prepared) != 0 {
						t.Fatalf("work start: %s/%s, %v, %d prepares", mid, sid, err, len(h.f.prepared))
					}
					return
				}
				if err != nil || mid != machine || sid == "" || h.f.made != 1 {
					t.Fatalf("personal start: %s/%s, %v", mid, sid, err)
				}
			})
		}
	}
}

func TestInboxDoesNotResumeOnWorkMachine(t *testing.T) {
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	ctx := context.Background()
	delegationMachineTrust(t, h.Service, "work", "work")
	it := actionItem("event", time.Now())
	it.Agent = &InboxAgent{State: AgentRunning, MachineID: "work", SessionID: "twin-session"}
	h.inbox.items[it.ID] = it
	h.inbox.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: "Private update"}, true, time.Now())
	h.originSet(ctx, "work", "twin-session", OriginInbox, it.ID, "")
	if _, found := h.conversationsByEvent(ctx)[it.ID]; found {
		t.Fatal("work conversation eligible for adoption")
	}
	h.deliverNotes(ctx, it)
	if len(h.f.agentSent) != 0 || len(h.f.sends) != 0 || it.Handling == nil || it.Handling.State != HandlingFailed {
		t.Fatalf("resumed work conversation: sends=%v, handling=%+v", h.f.sends, it.Handling)
	}
	if it.Handling.Error != i18n.T(h.lang(ctx), "assistant-start.work_machine") {
		t.Fatalf("failure = %q", it.Handling.Error)
	}
	// A running twin with no pending notes must also leave the active queue.
	it.Agent.State, it.Agent.Pending = AgentRunning, nil
	it.Handling.State = HandlingLooking
	h.sweepInboxAgents(ctx, InboxAgentConfig{Enabled: true}, time.Now())
	if it.Handling.State != HandlingFailed {
		t.Fatalf("still active: %+v", it.Handling)
	}
}
