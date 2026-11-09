package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func assertSentSource(t *testing.T, f *fakeHub, want *proto.MessageSource) {
	t.Helper()
	p := f.lastSend(t)
	text, _ := p["text"].(string)
	got := prompts.ItemSource(proto.Item{Role: "user", Kind: proto.KindText, Text: text})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("sent source = %+v, want %+v; params %v", got, want, p)
	}
	if want != nil {
		b, err := json.Marshal(p["message_source"])
		if err != nil {
			t.Fatal(err)
		}
		var wire proto.MessageSource
		if err := json.Unmarshal(b, &wire); err != nil || !reflect.DeepEqual(&wire, want) {
			t.Fatalf("send source field = %s, want %+v: %v", b, want, err)
		}
	}
}

func TestMessageSourcesRelayQueueRestart(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	f.known["receiver"] = true
	s := newTestService(t, f)
	msg := relayMsg{From: "conversation-1", FromTitle: "Research", Tool: "conversation_send", Text: "Read the result.", Hop: 1, At: time.Now().UTC()}
	if n := s.queueRelay("receiver", msg); n != 1 {
		t.Fatalf("queued %d", n)
	}
	st := s.store()
	restarted := New(Options{Hub: f, Store: func() Store { return st }})
	if n := restarted.queuedFor("receiver"); n != 1 {
		t.Fatalf("restored queue %d", n)
	}
	restored := restarted.relays.st.Queue["receiver"][0]
	if restored.From != msg.From || restored.FromTitle != msg.FromTitle {
		t.Fatalf("lost queued identity/title: %+v", restored)
	}
	if err := restarted.deliverRelay(ctx, "server", "receiver", restored); err != nil {
		t.Fatal(err)
	}
	assertSentSource(t, f, &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: msg.From, Title: msg.FromTitle})
	if f.lastSend(t)["resume_only"] != true {
		t.Fatal("restart delivery no longer requires the existing receiver")
	}
	if err := s.deliverRelay(ctx, "server", "receiver", relayMsg{Tool: superviseTool, Text: "A supervision prompt."}); err != nil {
		t.Fatal(err)
	}
	assertSentSource(t, f, &proto.MessageSource{Kind: proto.SourceSupervision})
}

func TestMessageSourcesScheduledPaths(t *testing.T) {
	for _, repeat := range []string{repeatOnce, repeatDaily, repeatInterval} {
		t.Run(repeat, func(t *testing.T) {
			f := newFakeHub().withMachine()
			s := newStorelessService(t, f)
			task := Task{ID: "scheduled-source", Title: "Inspection", Prompt: "Read the result.", Repeat: repeat, EveryMinutes: 30, Time: "09:00", At: time.Now(), Enabled: true}
			setTasks(s, task)
			if _, err := runNow(t, s, task.ID); err != nil {
				t.Fatal(err)
			}
			assertSentSource(t, f, taskMessageSource(task))
			if _, err := runNow(t, s, task.ID); err != nil {
				t.Fatal(err)
			}
			assertSentSource(t, f, taskMessageSource(task))
			// A follow-up addressed to an existing conversation uses the same source.
			task.SessionID, task.MachineID = "receiver", "server"
			setTasks(s, task)
			if _, err := runNow(t, s, task.ID); err != nil {
				t.Fatal(err)
			}
			assertSentSource(t, f, taskMessageSource(task))
			waitRuns(t, s, 3)
		})
	}
}

func TestMessageSourcesUserEntryAndFilters(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	source := &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "conversation-1", Title: "Research"}
	text := prompts.MessageSourceText("Read the result.", source)
	if _, err := s.Start(ctx, StartParams{Text: text}); err != nil {
		t.Fatal(err)
	}
	assertSentSource(t, f, nil)
	params, err := s.SendParams(ctx, SendParams{MachineID: "server", SessionID: "unknown", Params: map[string]any{"text": text, "source": source, "message_source": source}})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := params["source"]; exists {
		t.Fatal("user API preserved a forged source param")
	}
	if _, exists := params["message_source"]; exists {
		t.Fatal("user API preserved a forged internal source param")
	}
	protected := params["text"].(string)
	if _, ok := extensionMessage(proto.Item{Role: "user", Kind: proto.KindText, Text: protected}); !ok {
		t.Fatal("a user-pasted metadata block was hidden from user-message navigation")
	}
	for _, it := range []proto.Item{
		{Role: "user", Kind: proto.KindText, Text: text},
		{Role: "user", Kind: proto.KindText, Text: "Read the result.", Source: source},
		{Role: "user", Kind: proto.KindText, Text: "Read the result.", Source: &proto.MessageSource{Kind: "future"}},
	} {
		if _, ok := extensionMessage(it); ok {
			t.Fatal("an automated source appeared as user-message navigation")
		}
	}
	if _, err := s.startConversation(ctx, startParams{Text: "Read the result.", Source: source, Origin: OriginSpawned}); err != nil {
		t.Fatal(err)
	}
	assertSentSource(t, f, source)
}
