package assistant

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/channel"
)

func TestChannelUnboundWhileMessageIsBeingForwarded(t *testing.T) {
	s, f, _, _ := newTextAskService(t)
	t.Cleanup(func() { _ = s.forgetChannelTurns("examplechat") })
	ctx := context.Background()
	forwarded := false
	// The message reaches its conversation, but signing out finishes before the node acknowledges it.
	f.answerNode(DeviceSessionSendOnce, func(string, json.RawMessage) (any, error) {
		forwarded = true
		return map[string]any{}, s.ChannelUnbound(ctx, ChannelUnbound{Channel: "examplechat"})
	})
	if err := s.channelForward(ctx, "examplechat", channel.Message{ID: "old-message", Text: "Explain this change"}); err != nil {
		t.Fatal(err)
	}
	if !forwarded {
		t.Fatal("the test did not reach message forwarding")
	}
	if w := s.channelWaiting("examplechat"); w.Answers != 0 {
		t.Fatalf("an in-flight message restored the old binding's turn: %+v", w)
	}
	reloaded := newServiceOn(t, f, s.store())
	if w := reloaded.channelWaiting("examplechat"); w.Answers != 0 {
		t.Fatalf("old turn restored after restart: %+v", w)
	}

	// A new message after rebinding starts its own turn normally.
	f.answerNode(DeviceSessionSendOnce, func(string, json.RawMessage) (any, error) { return map[string]any{}, nil })
	if err := s.channelForward(ctx, "examplechat", channel.Message{ID: "new-message", Text: "Explain another change"}); err != nil {
		t.Fatal(err)
	}
	if runs := s.channelRuns("examplechat"); len(runs) != 1 || runs[0].ID != "examplechat_new-message" {
		t.Fatalf("new binding's turn = %+v", runs)
	}
}
