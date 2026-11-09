package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// channelPushService has two connected extension channels and one completed main-chat or scheduled-task turn.
func channelPushService(t *testing.T, main bool) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.ownModels = true // These tests use no model or HTTP transport.
	f.hello.Locale = "en"
	f.settings = &Settings{MachineID: "server", Main: "s1"}
	if !main {
		f.settings.Main = "main"
	}
	f.setSessions("server", proto.Session{SessionID: "s1", Assistant: true})
	f.setItems(proto.Item{ID: "q1", Role: "user", Kind: proto.KindText, Text: "Check status"},
		proto.Item{ID: "a1", Role: "assistant", Kind: proto.KindText, Text: "The full answer"})
	f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
		var views []channel.View
		for _, id := range []string{"examplechat", "otherchat"} {
			views = append(views, channel.View{ID: id, Name: id, Source: channel.SourcePlugin, Kind: channel.KindIM,
				Installed: true, Enabled: true, State: channel.StateConnected, Capabilities: channel.Capabilities{Push: true}})
		}
		return map[string]any{"channels": views}, nil
	})
	f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return FeishuStatus{}, nil })
	s := newTestService(t, f)
	s.chans.mu.Lock()
	s.loadChannelsLocked(context.Background())
	for _, id := range []string{"examplechat", "otherchat"} {
		s.channelPrefLocked(id).Features = map[string]bool{channel.FeaturePushes: true, channel.FeatureSync: true}
	}
	s.chans.mu.Unlock()
	if !main {
		setTasks(s, Task{ID: "task1", Title: "Status report", RunMachineID: "server", RunSessionID: "s1"})
	}
	return s, f
}

func enableChannelPushFeishu(f *fakeHub) {
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_owner", PairedAt: time.Unix(100, 0),
			Features: FeishuFeatures{Pushes: true, Sync: true}}, nil
	})
	f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		return map[string]any{"message_id": "om_push"}, nil
	})
}

func TestExtensionPushSkipsItsTurnEndMirror(t *testing.T) {
	for _, main := range []bool{true, false} {
		name := "scheduled task"
		if main {
			name = "main chat"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f := channelPushService(t, main)
				s.channelsPushed("server", "s1", "Status", "The notification")
				synctest.Wait()
				s.extensionMirror(context.Background(), "server", "s1")
				got := f.appParams(AppChannels, ChannelsSend)
				if len(got) != 2 {
					t.Fatalf("want one notification per channel and no mirror, got %v", got)
				}
				for _, p := range got {
					if p["text"] != "🔔 Status\n\nThe notification" || p["kind"] != channel.OutPush {
						t.Fatalf("unexpected notification: %v", p)
					}
				}
				if s.pushedLately(channel.Feishu, "server", "s1") {
					t.Fatal("an extension push marked the unconfigured Lark bot")
				}
			})
		})
	}
}

func TestExtensionPushMarksOnlyAfterAllPiecesSucceed(t *testing.T) {
	for _, failAt := range []int{0, 2} {
		name := "all pieces sent"
		if failAt != 0 {
			name = "second piece failed"
		}
		t.Run(name, func(t *testing.T) {
			s, f := channelPushService(t, true)
			f.answerApp(AppChannels, ChannelsList, func(json.RawMessage) (any, error) {
				return map[string]any{"channels": []channel.View{{ID: "examplechat", Name: "Example Chat", Source: channel.SourcePlugin,
					Kind: channel.KindIM, Installed: true, Enabled: true, State: channel.StateConnected,
					Capabilities: channel.Capabilities{Push: true, MaxText: 24}}}}, nil
			})
			attempts := 0
			f.answerApp(AppChannels, ChannelsSend, func(json.RawMessage) (any, error) {
				attempts++
				if attempts == failAt {
					return nil, errors.New("channel unavailable")
				}
				return map[string]any{}, nil
			})
			s.channelPush("server", "s1", "Status", strings.Repeat("More details. ", 8))
			if attempts < 2 {
				t.Fatalf("the notification was not split into several pieces: %d sends", attempts)
			}
			if got := s.pushedLately("examplechat", "server", "s1"); got != (failAt == 0) {
				t.Fatalf("mark=%v after failure at piece %d; all pieces must succeed", got, failAt)
			}
		})
	}
}

func TestFailedExtensionPushKeepsItsMirrorAfterOtherChannelsSucceed(t *testing.T) {
	for _, main := range []bool{true, false} {
		name := "scheduled task"
		if main {
			name = "main chat"
		}
		t.Run(name, func(t *testing.T) {
			s, f := channelPushService(t, main)
			enableChannelPushFeishu(f)
			f.answerApp(AppChannels, ChannelsSend, func(raw json.RawMessage) (any, error) {
				var p struct{ Channel, Text string }
				_ = json.Unmarshal(raw, &p)
				if p.Channel == "otherchat" && strings.HasPrefix(p.Text, "🔔") {
					return nil, &rpc.Error{Code: CodeCannotSend, Message: "cannot send the notification"}
				}
				return map[string]any{}, nil
			})
			s.feishuPush("server", "s1", "Status", "The notification")
			s.channelPush("server", "s1", "Status", "The notification")
			if !s.pushedLately(channel.Feishu, "server", "s1") || !s.pushedLately("examplechat", "server", "s1") ||
				s.pushedLately("otherchat", "server", "s1") {
				t.Fatal("delivery marks must reflect each channel's own result")
			}
			s.extensionMirror(context.Background(), "server", "s1")
			got := f.appParams(AppChannels, ChannelsSend)
			if len(got) != 3 || got[2]["channel"] != "otherchat" || !strings.Contains(got[2]["text"].(string), "The full answer") {
				t.Fatalf("only the failed channel should receive the mirror: %v", got)
			}
		})
	}
}

func TestExtensionPushDoesNotSuppressFeishuMirrorOrRetry(t *testing.T) {
	s, f := channelPushService(t, true)
	enableChannelPushFeishu(f)
	s.channelPush("server", "s1", "Status", "The notification")
	ctx := context.Background()
	m := &mirrorSend{target: botTarget{MachineID: "server", SessionID: "s1"}, main: true, answer: "The full answer", turn: "a1"}
	if !s.mirrorStillDue(ctx, m) {
		t.Fatal("a successful extension push suppressed a Lark mirror retry")
	}
	s.feishuMirror(ctx, "server", "s1")
	if got := f.appParams(AppFeishu, FeishuPost); len(got) != 1 {
		t.Fatalf("a successful extension push suppressed the Lark mirror: %v", got)
	}
	s.feishuPush("server", "s1", "Status", "The notification")
	if s.mirrorStillDue(ctx, m) {
		t.Fatal("a delivered Lark push must suppress its own mirror retry")
	}
	s.feishuMirror(ctx, "server", "s1")
	if got := f.appParams(AppFeishu, FeishuPost); len(got) != 2 {
		t.Fatalf("Lark mirrored a turn after its own successful notification: %v", got)
	}
}

func TestExtensionSyncStillRunsWithPushesDisabled(t *testing.T) {
	s, f := channelPushService(t, true)
	s.channelPush("server", "s1", "Status", "The notification")
	s.chans.mu.Lock()
	s.channelPrefLocked("examplechat").Features[channel.FeaturePushes] = false
	s.chans.mu.Unlock()
	s.extensionMirror(context.Background(), "server", "s1")
	got := f.appParams(AppChannels, ChannelsSend)
	if len(got) != 3 || got[2]["channel"] != "examplechat" || !strings.Contains(got[2]["text"].(string), "The full answer") {
		t.Fatalf("sync must still work with pushes disabled: %v", got)
	}
}

func TestExtensionPushMarksOnlyAfterSuccessfulSend(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			s, f := channelPushService(t, true)
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			f.answerApp(AppChannels, ChannelsSend, func(raw json.RawMessage) (any, error) {
				var p struct{ Channel string }
				_ = json.Unmarshal(raw, &p)
				if p.Channel == "examplechat" {
					close(entered)
					<-release
					if fail {
						return nil, errors.New("channel unavailable")
					}
				}
				return map[string]any{}, nil
			})
			go func() {
				s.channelPush("server", "s1", "Status", "The notification")
				close(done)
			}()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("the notification was not sent")
			}
			markedInFlight := s.pushedLately("examplechat", "server", "s1")
			close(release)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the notification send did not finish")
			}
			if markedInFlight || s.pushedLately("examplechat", "server", "s1") == fail {
				t.Fatalf("only a completed successful send may mark the channel; in-flight=%v, failure=%v", markedInFlight, fail)
			}
		})
	}
}

func TestChannelPushMarksAreIsolatedAndExpire(t *testing.T) {
	s := &Service{}
	now := time.Unix(100, 0)
	s.chans.now = func() time.Time { return now }
	s.markPushed("examplechat", "server", "s1")
	for _, key := range [][3]string{{"otherchat", "server", "s1"}, {"examplechat", "other", "s1"}, {"examplechat", "server", "s2"}} {
		if s.pushedLately(key[0], key[1], key[2]) {
			t.Fatalf("another channel or conversation inherited the push: %v", key)
		}
	}
	if !s.pushedLately("examplechat", "server", "s1") {
		t.Fatal("the delivered push was not recorded")
	}
	now = now.Add(3 * time.Minute)
	if s.pushedLately("examplechat", "server", "s1") {
		t.Fatal("the delivered push outlived the three-minute window")
	}
	now = now.Add(time.Hour)
	s.markPushed("otherchat", "server", "s2")
	if len(s.chans.notified) != 1 {
		t.Fatalf("old channel marks were not swept: %v", s.chans.notified)
	}
}
