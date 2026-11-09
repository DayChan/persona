package assistant

import (
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
)

// feishuPushHub pairs the bot with the pushes switch on; the FeishuPost handler enters entered, blocks on release,
// then returns fail (nil for success).
func feishuPushHub(t *testing.T, fail error) (s *Service, entered, release chan struct{}) {
	f := newFakeHub().withMachine()
	f.ownModels = true // Push delivery needs no model or HTTP fixture.
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0),
			Features: FeishuFeatures{Main: true, Cards: true, Pushes: true}}, nil
	})
	entered = make(chan struct{}, 1)
	release = make(chan struct{})
	f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
		entered <- struct{}{}
		<-release
		if fail != nil {
			return nil, fail
		}
		return map[string]any{"message_id": "om_push"}, nil
	})
	return newTestService(t, f), entered, release
}

// notify_user marks the conversation "pushed" only once the first Feishu card lands: never while it is in flight,
// never when it fails (#4448).
func TestFeishuPushMarksNotifiedOnlyAfterFirstCard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, entered, release := feishuPushHub(t, nil)
		s.channelsPushed("server", "s1", "Reminder", "Details")
		<-entered
		if s.pushedLately(channel.Feishu, "server", "s1") {
			t.Fatal("marked pushed before the first card landed")
		}
		close(release)
		synctest.Wait()
		if !s.pushedLately(channel.Feishu, "server", "s1") {
			t.Fatal("not marked pushed after the first card landed")
		}
	})
}

// A first card that fails leaves no "pushed" mark, so the turn-end mirror still delivers the turn (#4448). It drives
// the public channelsPushed path, so the old code that marked synchronously before sending fails this test too.
func TestFeishuPushFailureLeavesNotifiedUnmarked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, entered, release := feishuPushHub(t, errors.New("feishu down"))
		s.channelsPushed("server", "s2", "Reminder", "Details")
		<-entered // the first post is in flight and failing
		if s.pushedLately(channel.Feishu, "server", "s2") {
			t.Fatal("marked pushed although the first card has not landed")
		}
		close(release)
		synctest.Wait()
		if s.pushedLately(channel.Feishu, "server", "s2") {
			t.Fatal("marked pushed although the first card failed")
		}
	})
}
