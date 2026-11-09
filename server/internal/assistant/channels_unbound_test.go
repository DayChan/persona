package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestChannelUnboundClearsWaitingBeforeRebind(t *testing.T) {
	for _, ix := range []proto.Interaction{
		{ID: "permission", Kind: proto.IxPermission},
		{ID: "question", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "Which option?"}}},
	} {
		t.Run(ix.ID, func(t *testing.T) {
			ctx := context.Background()
			s, f, _, resolves := newTextAskService(t, ix)
			t.Cleanup(func() { _ = s.forgetChannelTurns("examplechat") })
			s.chans.mu.Lock()
			s.chans.st.Runs = []*channel.Run{{ID: "old-turn", Channel: "examplechat"}, {ID: "other-turn", Channel: "otherchat"}}
			s.chans.st.Asks = append(s.chans.st.Asks, channel.NewAsk("otherchat", ix, time.Now()))
			s.chans.st.Seen["examplechat"] = []string{"old-message"}
			s.chans.noteEndedLocked(channel.NewAsk("examplechat", ix, time.Now()))
			s.saveChannelStateLocked()
			s.chans.mu.Unlock()
			b := &BusHub{svc: s, helloed: make(chan struct{})}
			close(b.helloed)
			if _, err := b.request(ctx, assistantwire.MethodChannelUnbound, json.RawMessage(`{"channel":"examplechat"}`)); err != nil {
				t.Fatal(err)
			}
			if w := s.channelWaiting("examplechat"); w.Answers != 0 || w.Asks != 0 {
				t.Fatalf("signed-out channel still waits: %+v", w)
			}
			if w := s.channelWaiting("otherchat"); w.Answers != 1 || w.Asks != 1 {
				t.Fatalf("another channel was changed: %+v", w)
			}
			// Reload from storage: clearing must survive an assistant restart, and message deduplication stays intact.
			reloaded := newServiceOn(t, f, s.store())
			if w := reloaded.channelWaiting("examplechat"); w.Answers != 0 || w.Asks != 0 {
				t.Fatalf("old binding restored after restart: %+v", w)
			}
			if first, err := reloaded.claimChannelMessage("examplechat", "old-message"); err != nil || first {
				t.Fatalf("deduplication lost: first=%v, err=%v", first, err)
			}
			// The fake channel is connected again, with the original card still pending on the hub.
			s.channelReceive("examplechat", channel.Message{ID: "new-message", Text: "ok"}, "new-owner-action")
			if len(*resolves) != 0 {
				t.Fatalf("ordinary message answered the previous binding's card: %v", *resolves)
			}
			if text := sendOnceText(f); !strings.Contains(text, "ok") {
				t.Fatalf("ordinary message did not reach the conversation: %q", text)
			}
		})
	}
}

func TestChannelUnboundWhileQuestionIsBeingSent(t *testing.T) {
	s, f, _, _ := newTextAskService(t)
	ctx := context.Background()
	// Unbind inside the send, before askInChannel records its successful delivery. No sleeps or timing assumptions.
	f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) {
		return map[string]any{}, s.ChannelUnbound(ctx, ChannelUnbound{Channel: "examplechat"})
	})
	s.askInChannel(ctx, "examplechat", proto.Interaction{ID: "old-permission", Kind: proto.IxPermission})
	if w := s.channelWaiting("examplechat"); w.Asks != 0 {
		t.Fatalf("an in-flight question restored the old binding: %+v", w)
	}
	// A genuinely new question after rebinding is still recorded normally.
	f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	s.askInChannel(ctx, "examplechat", proto.Interaction{ID: "new-permission", Kind: proto.IxPermission})
	if w := s.channelWaiting("examplechat"); w.Asks != 1 {
		t.Fatalf("new binding cannot receive questions: %+v", w)
	}
}

func TestChannelUnboundReportsStorageFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("read", func(t *testing.T) {
		st := &flakyScheduleStore{data: map[string][]byte{}}
		s := newServiceOn(t, newFakeHub().withMachine(), st)
		st.failReads(true)
		if err := s.ChannelUnbound(ctx, ChannelUnbound{Channel: "examplechat"}); err == nil {
			t.Fatal("unread state reported cleared")
		}
		if n := st.writes(); n != 0 {
			t.Fatalf("overwrote unread state with %d writes", n)
		}
	})
	t.Run("write", func(t *testing.T) {
		st := &scheduleWriteStore{Store: &memStore{}}
		s := newServiceOn(t, newFakeHub().withMachine(), st)
		st.failWrites(channelsStateKey)
		if err := s.ChannelUnbound(ctx, ChannelUnbound{Channel: "examplechat"}); err == nil {
			t.Fatal("failed state write reported cleared")
		}
		st.failWrites("")
		if err := s.ChannelUnbound(ctx, ChannelUnbound{Channel: "examplechat"}); err != nil {
			t.Fatalf("cleanup cannot be retried: %v", err)
		}
	})
}
