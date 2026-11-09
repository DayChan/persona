package kb

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// selectionJSON is the document #1523 gives as the defaults (without its example items and history).
const selectionJSON = `{
  "version": 1,
  "messages": {
    "order": [["mentions", "mine"], ["dm"], ["group"], ["bot"], ["official"], ["muted"], ["collapsed"], ["done"]],
    "rules": {
      "mentions":  { "on": true,  "days": null, "attachment_days": null, "include_all": true },
      "mine":      { "on": true,  "days": null, "attachment_days": null, "context": 50 },
      "dm":        { "on": true,  "days": null, "attachment_days": null },
      "group":     { "on": false, "days": null, "attachment_days": null },
      "bot":       { "on": false, "days": null, "attachment_days": null },
      "official":  { "on": false, "days": null, "attachment_days": null },
      "muted":     { "on": false, "days": null, "attachment_days": null },
      "collapsed": { "on": false, "days": null, "attachment_days": null },
      "done":      { "on": false, "days": null, "attachment_days": null }
    }
  },
  "docs": {
    "order": [["mentions", "comment_mentions"], ["authored"], ["viewed"]],
    "rules": {
      "mentions":         { "on": true, "days": null },
      "comment_mentions": { "on": true, "days": null },
      "authored":         { "on": true, "days": null },
      "viewed":           { "on": true, "days": null }
    }
  },
  "items": [],
  "history": {}
}`

func TestSelectionDefaultsAreTheIssues(t *testing.T) {
	want, err := ParseSelection([]byte(selectionJSON))
	if err != nil {
		t.Fatal(err)
	}
	got := Defaults()
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(got)
	b, _ := json.Marshal(want)
	if string(a) != string(b) {
		t.Fatalf("defaults\n%s\nwant\n%s", a, b)
	}
	// What a client reads back says every null days out loud.
	if !strings.Contains(string(a), `"group":{"on":false,"days":null,"attachment_days":null}`) {
		t.Fatalf("days not written as null: %s", a)
	}
}

// sel is Defaults changed by f.
func sel(f func(*Selection)) *Selection {
	s := Defaults()
	if f != nil {
		f(&s)
	}
	return &s
}

func setRule(s *Selection, name string, f func(*MessageRule)) {
	r := s.Messages.Rules[name]
	f(&r)
	s.Messages.Rules[name] = r
}

var testRet = Retention{TextDays: 365, AttachmentDays: 30, AttachmentDaysBySource: map[string]int{"wide": 3650}}

func TestSelectionEvaluate(t *testing.T) {
	group := ChatFacts{Source: "chat", ID: "g1", Kind: "group"}
	muted := ChatFacts{Source: "chat", ID: "g2", Kind: "group", Muted: true}
	collapsed := ChatFacts{Source: "chat", ID: "g3", Kind: "group", Collapsed: true}
	done := ChatFacts{Source: "chat", ID: "g4", Kind: "group", Done: true}
	dm := ChatFacts{Source: "chat", ID: "d1", Kind: "dm"}
	mutedDM := ChatFacts{Source: "chat", ID: "d2", Kind: "dm", Muted: true}
	bot := ChatFacts{Source: "chat", ID: "b1", Kind: "dm", Bot: true}
	official := ChatFacts{Source: "chat", ID: "o1", Kind: "system"}
	plain := MessageFacts{}
	mention := MessageFacts{Mentioned: true}
	mine := MessageFacts{Mine: true}
	near := MessageFacts{NearMine: true}
	all := MessageFacts{MentionedAll: true}
	mentionDays := func(s *Selection) { setRule(s, RuleMentions, func(r *MessageRule) { r.Days = intp(3650) }) }

	for _, tc := range []struct {
		name  string
		sel   *Selection
		chat  ChatFacts
		msg   MessageFacts
		store bool
		rule  string
		days  int
		att   int
	}{
		{"defaults: a plain dm", sel(nil), dm, plain, true, RuleDM, 365, 30},
		{"defaults: a plain group", sel(nil), group, plain, false, RuleGroup, 0, 0},
		{"defaults: a bot chat", sel(nil), bot, plain, false, RuleBot, 0, 0},
		{"defaults: a system account", sel(nil), official, plain, false, RuleOfficial, 0, 0},
		{"mention in a muted chat", sel(mentionDays), muted, mention, true, RuleMentions, 3650, 30},
		{"mention in a collapsed chat", sel(mentionDays), collapsed, mention, true, RuleMentions, 3650, 30},
		{"mention in a done chat", sel(mentionDays), done, mention, true, RuleMentions, 3650, 30},
		{"plain message in a muted chat", sel(nil), muted, plain, false, RuleMuted, 0, 0},
		{"mine in a group with group off", sel(nil), group, mine, true, RuleMine, 365, 30},
		{"mine's context in a group with group off", sel(nil), group, near, true, RuleMine, 365, 30},
		{"mine's context with mine off", sel(func(s *Selection) { setRule(s, RuleMine, func(r *MessageRule) { r.On = false }) }), group, near, false, RuleMine, 0, 0},
		{"mine in a per-item read:false chat", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "chat", Kind: ItemChat, ID: "g1", Read: false})
		}), group, mine, false, RuleItem, 0, 0},
		{"a muted dm: the dm rule decides", sel(nil), mutedDM, plain, true, RuleDM, 365, 30},
		{"group off, one group stored per item", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "chat", Kind: ItemChat, ID: "g1", Read: true, Store: true})
		}), group, plain, true, RuleItem, 365, 30},
		{"group off, another group not", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "chat", Kind: ItemChat, ID: "other", Read: true, Store: true})
		}), group, plain, false, RuleGroup, 0, 0},
		{"a per-item store with days", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "chat", Kind: ItemChat, ID: "g1", Read: true, Store: true, Days: intp(90)})
		}), group, mention, true, RuleItem, 90, 30},
		{"a per-item store without days: the rule that would decide", sel(func(s *Selection) {
			mentionDays(s)
			s.SetItem(SelectionItem{Source: "chat", Kind: ItemChat, ID: "g1", Read: true, Store: true})
		}), group, mention, true, RuleMentions, 3650, 30},
		{"dms on, one dm read:false", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "chat", Kind: ItemChat, ID: "d1", Read: false})
		}), dm, mention, false, RuleItem, 0, 0},
		{"a per-item read without store: a plain dm is not stored", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "chat", Kind: ItemChat, ID: "d1", Read: true})
		}), dm, plain, false, "", 0, 0},
		{"a per-item read without store: a mention is", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "chat", Kind: ItemChat, ID: "g2", Read: true})
			s.Messages.Order = [][]string{{RuleMuted}, {RuleMentions, RuleMine}, {RuleDM}, {RuleGroup}, {RuleBot}, {RuleOfficial}, {RuleCollapsed}, {RuleDone}}
		}), muted, mention, true, RuleMentions, 365, 30},
		{"muted above mentions: a mention in a muted chat", sel(func(s *Selection) {
			s.Messages.Order = [][]string{{RuleMuted}, {RuleMentions, RuleMine}, {RuleDM}, {RuleGroup}, {RuleBot}, {RuleOfficial}, {RuleCollapsed}, {RuleDone}}
		}), muted, mention, false, RuleMuted, 0, 0},
		{"same level: mentions and mine with different days", sel(func(s *Selection) {
			mentionDays(s)
			setRule(s, RuleMine, func(r *MessageRule) { r.Days, r.AttachmentDays = intp(30), intp(7) })
			setRule(s, RuleMentions, func(r *MessageRule) { r.AttachmentDays = intp(60) })
		}), group, MessageFacts{Mine: true, Mentioned: true}, true, "mentions+mine", 3650, 60},
		{"same level: one off, one on", sel(func(s *Selection) {
			setRule(s, RuleMentions, func(r *MessageRule) { r.On = false })
		}), group, MessageFacts{Mine: true, Mentioned: true}, true, RuleMine, 365, 30},
		{"same level: both off is a decision", sel(func(s *Selection) {
			setRule(s, RuleMentions, func(r *MessageRule) { r.On = false })
			setRule(s, RuleMine, func(r *MessageRule) { r.On = false })
		}), dm, MessageFacts{Mine: true, Mentioned: true}, false, "mentions+mine", 0, 0},
		{"@all with include_all", sel(nil), group, all, true, RuleMentions, 365, 30},
		{"@all without include_all", sel(func(s *Selection) {
			setRule(s, RuleMentions, func(r *MessageRule) { r.IncludeAll = boolp(false) })
		}), group, all, false, RuleGroup, 0, 0},
		{"a connector that reports no flags: a group is a plain group", sel(func(s *Selection) {
			setRule(s, RuleGroup, func(r *MessageRule) { r.On = true })
		}), ChatFacts{Source: "chat", ID: "x", Kind: "group"}, plain, true, RuleGroup, 365, 30},
		{"a kind no rule knows is outside the selection", sel(nil), ChatFacts{Source: "mail", ID: "x", Kind: "mail"}, plain, true, "", 365, 30},
		{"no kind is outside the selection", sel(nil), ChatFacts{Source: "chat", ID: "x"}, plain, true, "", 365, 30},
		{"attachment days of the source", sel(nil), ChatFacts{Source: "wide", ID: "d", Kind: "dm"}, plain, true, RuleDM, 365, 365},
		{"attachment days within the rule's days", sel(func(s *Selection) {
			setRule(s, RuleDM, func(r *MessageRule) { r.Days, r.AttachmentDays = intp(10), intp(400) })
		}), dm, plain, true, RuleDM, 10, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.sel.Validate(); err != nil {
				t.Fatal(err)
			}
			d := tc.sel.Evaluate(testRet, tc.chat, tc.msg)
			want := Decision{Store: tc.store, Rule: tc.rule, Days: tc.days, AttachmentDays: tc.att}
			if d != want {
				t.Fatalf("got %+v, want %+v", d, want)
			}
		})
	}
}

func TestSelectionReadChat(t *testing.T) {
	for _, tc := range []struct {
		name string
		sel  *Selection
		chat ChatFacts
		want ReadPlan
	}{
		{"a dm is read whole", sel(nil), ChatFacts{Source: "s", ID: "d", Kind: "dm"}, ReadPlan{Read: true, Whole: true, Days: 365}},
		{"a muted group: only the personal rules", sel(nil), ChatFacts{Source: "s", ID: "g", Kind: "group", Muted: true},
			ReadPlan{Read: true, Personal: true, Mine: true, Mentions: true, Context: 50, Days: 365}},
		{"a per-item read:false is never read", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "s", Kind: ItemChat, ID: "g", Read: false})
		}), ChatFacts{Source: "s", ID: "g", Kind: "group"}, ReadPlan{}},
		{"a per-item store reads it whole", sel(func(s *Selection) {
			s.SetItem(SelectionItem{Source: "s", Kind: ItemChat, ID: "g", Read: true, Store: true, Days: intp(30)})
		}), ChatFacts{Source: "s", ID: "g", Kind: "group"}, ReadPlan{Read: true, Whole: true, Days: 30}},
		{"mentions off: only the owner's", sel(func(s *Selection) {
			setRule(s, RuleMentions, func(r *MessageRule) { r.On = false })
		}), ChatFacts{Source: "s", ID: "g", Kind: "group"}, ReadPlan{Read: true, Personal: true, Mine: true, Context: 50, Days: 365}},
		{"muted above the personal rules: not read", sel(func(s *Selection) {
			s.Messages.Order = [][]string{{RuleMuted}, {RuleMentions, RuleMine}, {RuleDM}, {RuleGroup}, {RuleBot}, {RuleOfficial}, {RuleCollapsed}, {RuleDone}}
		}), ChatFacts{Source: "s", ID: "g", Kind: "group", Muted: true}, ReadPlan{}},
		{"the furthest back of what it can store", sel(func(s *Selection) {
			setRule(s, RuleMentions, func(r *MessageRule) { r.Days = intp(3650) })
		}), ChatFacts{Source: "s", ID: "g", Kind: "group"}, ReadPlan{Read: true, Personal: true, Mine: true, Mentions: true, Context: 50, Days: 3650}},
		{"outside the selection: whole", sel(nil), ChatFacts{Source: "mail", ID: "m", Kind: "mail"}, ReadPlan{Read: true, Whole: true, Days: 365}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sel.ReadChat(testRet, tc.chat); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	whole := ReadPlan{Read: true, Whole: true, Days: 365}
	mineOnly := ReadPlan{Read: true, Personal: true, Mine: true, Days: 365}
	both := ReadPlan{Read: true, Personal: true, Mine: true, Mentions: true, Days: 365}
	for _, tc := range []struct {
		p, q ReadPlan
		want bool
	}{
		{whole, mineOnly, true}, {mineOnly, whole, false}, {both, mineOnly, true}, {mineOnly, both, false},
		{mineOnly, ReadPlan{}, true}, {ReadPlan{}, mineOnly, false}, {whole, whole, false},
		{ReadPlan{Read: true, Whole: true, Days: 400}, whole, true},
		// More read around the owner's messages is wider; less is not, and a whole read reads everything anyway.
		{ReadPlan{Read: true, Personal: true, Mine: true, Context: 200, Days: 365}, ReadPlan{Read: true, Personal: true, Mine: true, Context: 50, Days: 365}, true},
		{ReadPlan{Read: true, Personal: true, Mine: true, Context: 10, Days: 365}, ReadPlan{Read: true, Personal: true, Mine: true, Context: 50, Days: 365}, false},
		{ReadPlan{Read: true, Personal: true, Mentions: true, Days: 365}, ReadPlan{Read: true, Personal: true, Mentions: true, Context: 50, Days: 365}, false},
	} {
		if got := tc.p.Wider(tc.q); got != tc.want {
			t.Errorf("%+v wider than %+v: %v", tc.p, tc.q, got)
		}
	}
}

func TestSelectionLineDays(t *testing.T) {
	s := sel(func(s *Selection) {
		setRule(s, RuleMentions, func(r *MessageRule) { r.Days, r.AttachmentDays = intp(3650), intp(100) })
		setRule(s, RuleGroup, func(r *MessageRule) { r.Days = intp(30) }) // off: its days do not count
		setRule(s, RuleDM, func(r *MessageRule) { r.Days = intp(60) })
		s.SetItem(SelectionItem{Source: "s", Kind: ItemChat, ID: "kept", Read: true, Store: true, Days: intp(7)})
	})
	for _, tc := range []struct {
		chat, rule string
		text, att  int
	}{
		{"c", "", 365, 30},
		{"c", RuleItem, 365, 30},
		{"c", RuleMentions, 3650, 100},
		{"c", RuleGroup, 365, 30}, // an off rule keeps text_days
		{"c", RuleDM, 60, 30},
		{"c", "dm+mentions", 3650, 100},
		{"c", "unknown", 365, 30},
		{"kept", RuleMentions, 7, 7}, // the item's days for every line
		{"kept", "", 7, 7},
	} {
		text, att := s.LineDays(testRet, "s", tc.chat, tc.rule)
		if text != tc.text || att != tc.att {
			t.Errorf("%s %q: %d/%d, want %d/%d", tc.chat, tc.rule, text, att, tc.text, tc.att)
		}
	}
	var none *Selection
	if text, att := none.LineDays(testRet, "wide", "c", RuleMentions); text != 365 || att != 365 {
		t.Errorf("no selection: %d/%d", text, att)
	}
}

func TestSelectionEvaluateDoc(t *testing.T) {
	s := sel(func(s *Selection) {
		r := s.Docs.Rules["viewed"]
		r.On, r.Days = false, intp(5)
		s.Docs.Rules["viewed"] = r
		r = s.Docs.Rules["authored"]
		r.Days = intp(900)
		s.Docs.Rules["authored"] = r
		s.SetItem(SelectionItem{Source: "s", Kind: ItemDoc, ID: "skip", Read: false})
		s.SetItem(SelectionItem{Source: "s", Kind: ItemDoc, ID: "keep", Read: true, Store: true})
	})
	for _, tc := range []struct {
		doc  DocFacts
		want Decision
	}{
		{DocFacts{Source: "s", ID: "a", Signals: []string{"viewed"}}, Decision{Rule: "viewed"}},
		{DocFacts{Source: "s", ID: "a", Signals: []string{"viewed", "authored"}}, Decision{Store: true, Rule: "authored", Days: 900}},
		{DocFacts{Source: "s", ID: "a", Signals: []string{"comment_mentions", "authored"}}, Decision{Store: true, Rule: "comment_mentions", Days: 365}},
		{DocFacts{Source: "s", ID: "a"}, Decision{}},
		{DocFacts{Source: "s", ID: "skip", Signals: []string{"mentions"}}, Decision{Rule: RuleItem}},
		{DocFacts{Source: "s", ID: "keep", Signals: []string{"viewed"}}, Decision{Store: true, Rule: RuleItem, Days: 365}},
		{DocFacts{Source: "s", ID: "keep", Signals: []string{"authored"}}, Decision{Store: true, Rule: "authored", Days: 900}},
	} {
		if got := s.EvaluateDoc(testRet, tc.doc); got != tc.want {
			t.Errorf("%+v: %+v, want %+v", tc.doc, got, tc.want)
		}
	}
}

func TestSelectionValidate(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Selection)
	}{
		{"version", func(s *Selection) { s.Version = 2 }},
		{"a rule missing from the order", func(s *Selection) { s.Messages.Order = s.Messages.Order[1:] }},
		{"a rule twice in the order", func(s *Selection) { s.Messages.Order = append(s.Messages.Order, []string{RuleDM}) }},
		{"an unknown rule in the order", func(s *Selection) { s.Messages.Order[1] = append(s.Messages.Order[1], "x") }},
		{"an empty level", func(s *Selection) { s.Messages.Order = append(s.Messages.Order, []string{}) }},
		{"an unknown rule", func(s *Selection) { s.Messages.Rules["x"] = MessageRule{} }},
		{"a rule missing", func(s *Selection) { delete(s.Messages.Rules, RuleDone) }},
		{"days 0", func(s *Selection) { setRule(s, RuleDM, func(r *MessageRule) { r.Days = intp(0) }) }},
		{"too many days", func(s *Selection) {
			setRule(s, RuleDM, func(r *MessageRule) { r.AttachmentDays = intp(SelectionMostDays + 1) })
		}},
		{"context too large", func(s *Selection) { setRule(s, RuleMine, func(r *MessageRule) { r.Context = intp(201) }) }},
		{"context on another rule", func(s *Selection) { setRule(s, RuleDM, func(r *MessageRule) { r.Context = intp(1) }) }},
		{"include_all on another rule", func(s *Selection) { setRule(s, RuleMine, func(r *MessageRule) { r.IncludeAll = boolp(true) }) }},
		{"a doc rule missing", func(s *Selection) { delete(s.Docs.Rules, "viewed") }},
		{"a doc order wrong", func(s *Selection) { s.Docs.Order = [][]string{{"viewed"}} }},
		{"an item without an id", func(s *Selection) { s.Items = append(s.Items, SelectionItem{Source: "s", Kind: ItemChat}) }},
		{"an item without a source", func(s *Selection) { s.Items = append(s.Items, SelectionItem{ID: "x", Kind: ItemChat}) }},
		{"an item of another kind", func(s *Selection) { s.Items = append(s.Items, SelectionItem{Source: "s", ID: "x", Kind: "mail"}) }},
		{"an item twice", func(s *Selection) {
			s.Items = append(s.Items, SelectionItem{Source: "s", ID: "x", Kind: ItemChat}, SelectionItem{Source: "s", ID: "x", Kind: ItemChat, Read: true})
		}},
		{"stored without read", func(s *Selection) {
			s.Items = append(s.Items, SelectionItem{Source: "s", ID: "x", Kind: ItemChat, Store: true})
		}},
		{"history back_days 0", func(s *Selection) { s.History["s"] = HistorySelection{BackDays: intp(0)} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := Defaults()
			tc.edit(&s)
			if err := s.Validate(); !errors.Is(err, ErrSelection) {
				t.Fatalf("validated: %v", err)
			}
		})
	}
	s := Defaults()
	s.Items = append(s.Items, SelectionItem{Source: "s", ID: "x", Kind: ItemChat}, SelectionItem{Source: "s", ID: "x", Kind: ItemDoc, Read: true, Store: true, Days: intp(10)})
	s.History["s"] = HistorySelection{BackDays: intp(30)}
	setRule(&s, RuleMine, func(r *MessageRule) { r.Context = intp(0) })
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if s.MineContext() != 0 || s.BackDays("s") != 30 || s.BackDays("t") != 0 {
		t.Fatalf("context %d, back days %d", s.MineContext(), s.BackDays("s"))
	}
	for _, raw := range []string{`{"version":1,"unknown":true}`, selectionJSON + `{}`, `[]`} {
		if _, err := ParseSelection([]byte(raw)); !errors.Is(err, ErrSelection) {
			t.Errorf("parsed %.40q: %v", raw, err)
		}
	}
	c := s.Clone()
	c.Items[0].Name = "changed"
	c.Messages.Rules[RuleDM] = MessageRule{}
	if s.Items[0].Name != "" || !s.Messages.Rules[RuleDM].On {
		t.Fatal("clone shares state")
	}
	if !c.RemoveItem("s", ItemChat, "x") || c.RemoveItem("s", ItemChat, "x") || len(c.Items) != 1 {
		t.Fatal("remove item")
	}
}
