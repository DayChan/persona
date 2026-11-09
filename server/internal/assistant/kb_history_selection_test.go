package assistant

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// The history import reads what the knowledge base's selection says (kb_selection.go), per-item settings included:
// the same rules as daily ingestion, against a fake source and a fake clock.

// dailyConv is a conversation of n messages, one a day, the newest an hour ago; mine are the owner's.
func dailyConv(chat string, n int, mine ...int) []kb.SourceMessage {
	start := time.Now().UTC().Add(-time.Duration(n-1)*24*time.Hour - time.Hour)
	return historyConv(chat, n, start, 24*time.Hour, mine...)
}

// keptSince are the positions of msgs at or after cut.
func keptSince(msgs []kb.SourceMessage, cut time.Time) []int {
	var out []int
	for i, m := range msgs {
		if !m.At.Before(cut) {
			out = append(out, i)
		}
	}
	return out
}

// A chat the selection reads nothing of is never asked for: a per-item read: false, or a chat no rule that is on can
// store anything of (here an official account with the personal rules off).
func TestHistoryExcludedChatsAreNeverRead(t *testing.T) {
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}, {ID: "b", Kind: "dm"}, {ID: "o", Kind: "official"}},
		convs: map[string][]kb.SourceMessage{"a": historyConv("a", 30, recentStart(30, time.Hour), time.Hour),
			"b": historyConv("b", 30, recentStart(30, time.Hour), time.Hour, 3), "o": historyConv("o", 30, recentStart(30, time.Hour), time.Hour, 3)}}
	ht := newHistoryTest(t, src)
	ht.selection(func(s *kb.Selection) {
		for _, name := range []string{kb.RuleMentions, kb.RuleMine} {
			r := s.Messages.Rules[name]
			r.On = false
			s.Messages.Rules[name] = r
		}
	})
	ht.plan(true, map[string]string{"b": "off"})
	ht.run()
	if src.count("page b") != 0 || src.count("page o") != 0 {
		t.Fatalf("an excluded chat was read: %v", src.calls)
	}
	if got := ht.kept("a"); !slices.Equal(got, span(0, 29)) {
		t.Fatalf("a kept %v", got)
	}
	if ht.text("b") != "" || ht.text("o") != "" {
		t.Fatal("an excluded chat was archived")
	}
}

// A group per-item store: true is read whole; another group (the group rule off) only around the owner's messages.
func TestHistoryItemStoreReadsAGroupWhole(t *testing.T) {
	const n = 300
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "g", Kind: "group"}, {ID: "h", Kind: "group"}},
		convs: map[string][]kb.SourceMessage{"g": historyConv("g", n, recentStart(n, time.Hour), time.Hour, 100),
			"h": historyConv("h", n, recentStart(n, time.Hour), time.Hour, 100)}}
	ht := newHistoryTest(t, src)
	ht.plan(true, map[string]string{"g": "full"})
	ht.run()
	if got := ht.kept("g"); !slices.Equal(got, span(0, n-1)) {
		t.Fatalf("g kept %d", len(got))
	}
	if got := ht.kept("h"); !slices.Equal(got, span(50, 150)) {
		t.Fatalf("h kept %v", got)
	}
	if st := ht.state("g"); st.Plan == nil || !st.Plan.Whole || st.Mode != kb.HistoryFull {
		t.Fatalf("g state %+v", st)
	}
	if c, _ := ht.e.Store.Chat(kb.ChatID(testSource, "h")); c == nil || c.HistoryMode != kb.HistoryAnchor {
		t.Fatalf("h chat %+v", c)
	}
}

// A selection that reads more than the owner approved (here the group rule turned on while the import runs) takes the
// approval back before the next call: the import ends, the owner is told, and nothing more is read until the plan is
// approved again; then the group is read whole.
func TestHistoryWidenedSelectionRevokesTheApproval(t *testing.T) {
	const n = 300
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "g", Kind: "group"}},
		convs: map[string][]kb.SourceMessage{"g": historyConv("g", n, recentStart(n, time.Hour), time.Hour, 250)}}
	ht := newHistoryTest(t, src)
	src.hook = func(i int, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error, bool) {
		if i == 2 {
			ht.selection(func(s *kb.Selection) {
				r := s.Messages.Rules[kb.RuleGroup]
				r.On = true
				s.Messages.Rules[kb.RuleGroup] = r
			})
		}
		return connectorwire.HistoryPage{}, nil, false
	}
	ht.plan(true, nil)
	ht.run()
	if got := src.count("page"); got != 2 {
		t.Fatalf("%d pages: a call was made after the selection widened", got)
	}
	p, _, _ := loadHistoryPlan(ht.data, testSource)
	if p.ApprovedAt != nil || p.Approved != nil {
		t.Fatalf("approval kept: %+v", p)
	}
	if len(ht.notices) != 1 || !strings.Contains(ht.notices[0], "plan_changed") {
		t.Fatalf("notices %v", ht.notices)
	}
	ht.run()
	if got := src.count("page"); got != 2 {
		t.Fatalf("read without approval: %d pages", got)
	}
	src.hook = nil
	ht.plan(true, nil)
	ht.run()
	if got := ht.kept("g"); !slices.Equal(got, span(0, n-1)) {
		t.Fatalf("kept %d after the new approval", len(got))
	}
}

// back_days bounds how far back the import reads, whatever the rule's days; going further back reads more than was
// approved: approved again, the chat is read on to its new bound.
func TestHistoryBackDaysHonoured(t *testing.T) {
	const n = 200
	msgs := dailyConv("a", n)
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": msgs}}
	ht := newHistoryTest(t, src)
	setBack := func(days int) {
		ht.selection(func(s *kb.Selection) {
			s.History = map[string]kb.HistorySelection{testSource: {BackDays: &days}}
		})
	}
	setBack(30)
	ht.plan(true, nil)
	ht.run()
	if got, want := ht.kept("a"), keptSince(msgs, ht.e.CutoffFor(30)); !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	if pages := src.count("page"); pages > 4 {
		t.Fatalf("%d pages for 30 days of one message a day", pages)
	}
	setBack(100)
	ht.run()
	if p, _, _ := loadHistoryPlan(ht.data, testSource); p.ApprovedAt != nil {
		t.Fatal("going further back kept the approval")
	}
	ht.plan(true, nil)
	ht.run()
	if got, want := ht.kept("a"), keptSince(msgs, ht.e.CutoffFor(100)); !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
}

// In a whole read the lines around the owner's messages are the mine rule's: with direct messages kept 30 days and
// the mine rule's days longer, the old messages around the owner's are kept and other old ones are not.
func TestHistoryWholeReadKeepsTheOwnersContext(t *testing.T) {
	const n = 200
	msgs := dailyConv("a", n, 20)
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": msgs}}
	ht := newHistoryTest(t, src)
	ht.selection(func(s *kb.Selection) {
		r := s.Messages.Rules[kb.RuleDM]
		days := 30
		r.Days = &days
		s.Messages.Rules[kb.RuleDM] = r
	})
	ht.plan(true, nil)
	ht.run()
	want := append(span(0, 70), keptSince(msgs, ht.e.CutoffFor(30))...)
	if got := ht.kept("a"); !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	if strings.Contains(ht.text("a"), "省略") { // i18n-ignore: archive data
		t.Fatal("a whole read wrote an omission line")
	}
}

// A search hit that says neither mine nor mention is read around as the owner's, but who sent the message is the
// page's word alone: one the page says someone else sent is never written as the owner's.
func TestHistoryAnchorWithoutFlagsKeepsTheSender(t *testing.T) {
	const n = 300
	msgs := historyConv("g", n, recentStart(n, 10*time.Minute), 10*time.Minute)
	src := positionalSource("g", msgs)
	m := msgs[150]
	src.anchors[m.At.Year()] = append(src.anchors[m.At.Year()], connectorwire.HistoryAnchor{Chat: "g", Position: m.Position, At: m.At})
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	ht.run()
	if got := ht.kept("g"); !slices.Equal(got, span(100, 200)) {
		t.Fatalf("kept %v", got)
	}
	if text := ht.text("g"); strings.Contains(text, "我:") { // i18n-ignore: archive data
		t.Fatalf("someone else's message written as the owner's:\n%s", text)
	}
	if c, _ := ht.e.Store.Chat(kb.ChatID(testSource, "g")); c == nil || c.Mine {
		t.Fatalf("chat %+v", c)
	}
}

// historyLedgerRules are the rules a chat's retention ledger names, by count.
func historyLedgerRules(t *testing.T, ht *historyTest, chat string) map[string]int {
	t.Helper()
	raw, err := os.ReadFile(ht.e.Store.Path(kb.KindChat, kb.ChatID(testSource, chat), "retention.json"))
	if err != nil {
		t.Fatal(err)
	}
	var l struct {
		Lines map[string]map[string]string `json:"lines"`
	}
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, day := range l.Lines {
		for _, r := range day {
			out[r]++
		}
	}
	return out
}

// A mention kept on its own (far from every message of the owner's) is a mention, as daily ingestion judges it: with
// the mine rule ranked above mentions and kept shorter, it keeps the mentions rule, not the mine rule's days.
func TestHistoryLoneMentionIsNotNearMine(t *testing.T) {
	const n = 300
	msgs := historyConv("g", n, recentStart(n, time.Hour), time.Hour, 250)
	msgs[40].Mentioned = true
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "g", Kind: "group"}}, convs: map[string][]kb.SourceMessage{"g": msgs}}
	ht := newHistoryTest(t, src)
	ht.selection(func(s *kb.Selection) {
		s.Messages.Order = [][]string{{kb.RuleMine}, {kb.RuleMentions}, {kb.RuleDM}, {kb.RuleGroup}, {kb.RuleBot}, {kb.RuleOfficial},
			{kb.RuleMuted}, {kb.RuleCollapsed}, {kb.RuleDone}}
		r := s.Messages.Rules[kb.RuleMine]
		days := 30
		r.Days = &days
		s.Messages.Rules[kb.RuleMine] = r
	})
	ht.plan(true, nil)
	ht.run()
	if got := ht.kept("g"); !slices.Equal(got, append([]int{40}, span(200, 299)...)) {
		t.Fatalf("kept %v", got)
	}
	rules := historyLedgerRules(t, ht, "g")
	if rules[kb.RuleMentions] != 1 || rules[kb.RuleMine] != 100 {
		t.Fatalf("ledger rules %v", rules)
	}
}

// A selection that cannot be read now (the stopgap) neither runs the import nor takes its approval back: it waits,
// and goes on as approved once the document reads again.
func TestHistoryStopgapKeepsTheApproval(t *testing.T) {
	msgs := dailyConv("a", 20)
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "a", Kind: "dm"}}, convs: map[string][]kb.SourceMessage{"a": msgs}}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	def := ht.sel.Load()
	keep := kb.KeepEverything()
	ht.sel.Store(&keep)
	ht.run()
	if got := src.count("page"); got != 0 {
		t.Fatalf("%d pages under the stopgap", got)
	}
	if p, _, _ := loadHistoryPlan(ht.data, testSource); p.ApprovedAt == nil || len(ht.notices) != 0 {
		t.Fatalf("approval %+v, notices %v", p, ht.notices)
	}
	ht.sel.Store(def)
	ht.run()
	if got := ht.kept("a"); !slices.Equal(got, span(0, 19)) {
		t.Fatalf("kept %v", got)
	}
}

// More context around the owner's messages reads more: the approval is taken back before any call.
func TestHistoryLargerContextRevokesTheApproval(t *testing.T) {
	const n = 300
	src := &fakeHistory{chats: []connectorwire.HistoryChat{{ID: "g", Kind: "group"}},
		convs: map[string][]kb.SourceMessage{"g": historyConv("g", n, recentStart(n, time.Hour), time.Hour, 150)}}
	ht := newHistoryTest(t, src)
	ht.plan(true, nil)
	ht.selection(func(s *kb.Selection) {
		r := s.Messages.Rules[kb.RuleMine]
		c := 100
		r.Context = &c
		s.Messages.Rules[kb.RuleMine] = r
	})
	ht.run()
	if got := src.count("page"); got != 0 {
		t.Fatalf("%d pages after the context grew", got)
	}
	if p, _, _ := loadHistoryPlan(ht.data, testSource); p.ApprovedAt != nil || len(ht.notices) != 1 {
		t.Fatalf("approval %+v, notices %v", p, ht.notices)
	}
	ht.plan(true, nil)
	ht.run()
	if got := ht.kept("g"); !slices.Equal(got, span(50, 250)) {
		t.Fatalf("kept %v", got)
	}
}
