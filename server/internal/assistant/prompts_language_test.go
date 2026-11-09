package assistant

// The inbox's and the titles' prompts in the user's language (docs/protocol/i18n.md): golden for en / ja
// (testdata/prompts/*.<locale>.golden), and zh-Hans renders the same as a hub that says no language.

import (
	"context"
	"strings"
	"testing"
	"time"
)

var promptLocales = []string{"en", "ja"}

func TestGoldenInboxTriageLanguage(t *testing.T) {
	ctx := context.Background()
	for _, locale := range append([]string{"zh-Hans"}, promptLocales...) {
		h := goldenInbox(t)
		model, calls := captureModel(t, `{"tier":"action","summary":"s","reason":"r"}`)
		h.in.model = model
		h.f.hello.Locale = locale
		it := goldenItems(t)["group"]
		h.inbox.items[it.ID] = it
		h.triageOne(ctx, it)
		c := onlyCall(t, calls())
		if locale == "zh-Hans" {
			golden(t, "inbox_triage.group.system", c.System) // the same as without a language
			continue
		}
		golden(t, "inbox_triage.group.system."+locale, c.System)
	}
}

func TestGoldenInboxDraftLanguage(t *testing.T) {
	ctx := context.Background()
	for _, locale := range append([]string{"zh-Hans"}, promptLocales...) {
		h := goldenInbox(t)
		model, calls := captureModel(t, "好的")
		h.in.models[purposeDraft] = model
		h.f.hello.Locale = locale
		it := goldenItems(t)["dm"]
		h.inbox.items[it.ID] = it
		if _, err := h.draftReply(ctx, it.ID, ""); err != nil {
			t.Fatal(err)
		}
		c := onlyCall(t, calls())
		if locale == "zh-Hans" {
			golden(t, "inbox_draft.plain.system", c.System)
			continue
		}
		golden(t, "inbox_draft.plain.system."+locale, c.System)
	}
}

func TestGoldenInboxGroupLanguage(t *testing.T) {
	for _, locale := range append([]string{"zh-Hans"}, promptLocales...) {
		g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
		model, client := newGroupModel(t, `{"tool":"messages_before","args":{"message":"m1060","n":5}}`, groupAnswerOK)
		h, _ := groupHub(t, g, client)
		h.f.hello.Owner, h.f.hello.Locale = "示例用户", locale
		h.collectInbox(context.Background(), roundPoll)
		h.waitGrouping()
		calls := model.asked()
		if len(calls) == 0 {
			t.Fatalf("%s: no call", locale)
		}
		if locale == "zh-Hans" {
			golden(t, "inbox_group.first.system", calls[0].System)
			continue
		}
		golden(t, "inbox_group.first.system."+locale, calls[0].System)
	}
}

// The topic follows the user's language; the kind stays one of the Chinese words parseTitle reads.
func TestGoldenSessionTitleLanguage(t *testing.T) {
	started := time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
	for _, c := range []struct{ locale, reply, want string }{
		{"zh-Hans", "修复 | 搜索框和顶栏重叠", "1101 | 修复 | 搜索框和顶栏重叠"},
		{"en", "修复 | Search bar overlap", "1101 | 修复 | Search bar overlap"},
		{"ja", "修复 | 検索欄と上部バー", "1101 | 修复 | 検索欄と上部バー"},
	} {
		f := newFakeHub()
		f.settings = &Settings{Timezone: "Asia/Shanghai"}
		f.hello.Locale = c.locale
		f.replies = []string{c.reply}
		s := newTestService(t, f)
		if got, _ := s.makeTitle(context.Background(), started, "搜索的时候搜索框和顶栏叠在一起了", "是两个组件一起用导致的。"); got != c.want {
			t.Fatalf("%s: %q", c.locale, got)
		}
		system := f.modelCalls()[0].System
		if c.locale == "zh-Hans" {
			golden(t, "session_title.system", system)
			continue
		}
		if strings.Contains(system, "主题用中文") {
			t.Fatalf("%s: still asks for a Chinese topic", c.locale)
		}
		golden(t, "session_title.system."+c.locale, system)
	}
}
