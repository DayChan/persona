package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// hungChannelHub is a hub on which channel hung's program never answers a send: the call holds until its context
// ends. Any call made on a context that has ended fails with that context's error, as a real hub call does.
type hungChannelHub struct {
	Hub
	hung string
}

func (h hungChannelHub) AppCall(ctx context.Context, p AppCall) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.AppID == AppChannels && p.Method == ChannelsSend {
		var m struct{ Channel string }
		_ = json.Unmarshal(p.Params, &m)
		if m.Channel == h.hung {
			<-ctx.Done()
			return nil, ctx.Err()
		}
	}
	return h.Hub.AppCall(ctx, p)
}

// fanOutPastHungChannel runs fanOut with examplechat's program hung (examplechat comes first in the hub's list). It
// checks that otherchat's send reaches the hub at once and that fanOut returns only when examplechat's own send
// timeout ends, and returns otherchat's send.
func fanOutPastHungChannel(t *testing.T, s *Service, f *fakeHub, fanOut func()) map[string]any {
	t.Helper()
	s.hub = hungChannelHub{Hub: s.hub, hung: "examplechat"}
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fanOut()
	}()
	synctest.Wait() // every goroutine is blocked: what could go out at once has
	got := f.appParams(AppChannels, ChannelsSend)
	<-done // before any t.Fatal: synctest.Test panics when its goroutine ends while another is still blocked
	took := time.Since(start)
	if len(got) != 1 || got[0]["channel"] != "otherchat" {
		t.Fatalf("otherchat must get its send at once while examplechat hangs; sends that reached the hub: %v", got)
	}
	if took != channelSendTimeout {
		t.Fatalf("returned after %v, want examplechat's own send timeout %v", took, channelSendTimeout)
	}
	return got[0]
}

// One channel whose program hangs no longer spends the time of the channels after it: each push goes out on its own,
// and only the channel that got it is marked pushed.
func TestChannelPushReachesTheOtherChannelsPastAHungOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := channelPushService(t, true)
		got := fanOutPastHungChannel(t, s, f, func() { s.channelPush("server", "s1", "Status", "The notification") })
		if got["text"] != "🔔 Status\n\nThe notification" || got["kind"] != channel.OutPush {
			t.Fatalf("unexpected notification: %v", got)
		}
		if !s.pushedLately("otherchat", "server", "s1") || s.pushedLately("examplechat", "server", "s1") {
			t.Fatal("only the channel whose push went out may be marked pushed")
		}
	})
}

// The sync mirror of a main-chat turn reaches every channel with sync on, one hung program or not.
func TestExtensionMirrorReachesTheOtherChannelsPastAHungOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := channelPushService(t, true)
		got := fanOutPastHungChannel(t, s, f, func() { s.extensionMirror(context.Background(), "server", "s1") })
		if text, _ := got["text"].(string); !strings.Contains(text, "The full answer") || got["kind"] != channel.OutPush {
			t.Fatalf("unexpected mirror: %v", got)
		}
	})
}

// A card asked in text reaches every channel that takes it, one hung program or not; only the channel it reached
// waits for the answer.
func TestChannelInteractionAsksTheOtherChannelsPastAHungOne(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := channelPushService(t, true)
		f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
			var views []channel.View
			for _, id := range []string{"examplechat", "otherchat"} {
				views = append(views, channel.View{ID: id, Name: id, Source: channel.SourcePlugin, Kind: channel.KindIM, Installed: true,
					Enabled: true, State: channel.StateConnected, Capabilities: channel.Capabilities{Asks: channel.AsksText}})
			}
			return map[string]any{"channels": views}, nil
		})
		s.chans.mu.Lock()
		for _, id := range []string{"examplechat", "otherchat"} {
			s.channelPrefLocked(id).Features = map[string]bool{channel.FeatureAsks: true, channel.FeatureAllAsks: true}
		}
		s.chans.mu.Unlock()
		ix := proto.Interaction{ID: "ix_1", Kind: proto.IxPermission, MachineID: "server", SessionID: "s1", Cwd: "/work/project",
			ToolName: "Bash", Summary: "Run the tests"}
		got := fanOutPastHungChannel(t, s, f, func() { s.channelInteraction(ix) })
		if got["ref"] != "ask:ix_1" || got["kind"] != channel.OutAsk {
			t.Fatalf("unexpected ask: %v", got)
		}
		var waiting []string
		s.chans.mu.Lock()
		for _, a := range s.chans.st.Asks {
			waiting = append(waiting, a.Channel)
		}
		s.chans.mu.Unlock()
		if !slices.Equal(waiting, []string{"otherchat"}) {
			t.Fatalf("only otherchat got the ask, so only it may wait for an answer: %v", waiting)
		}
	})
}
