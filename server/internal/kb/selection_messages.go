package kb

import "strings"

// ChatFacts are what a source reports about a chat (kbwire.SourceChat, connectorwire.HistoryChat).
type ChatFacts struct {
	Source string // the knowledge base's source name (a connector id, or one of its archive sources)
	ID     string // the source's stable chat id (the per-item key)
	Kind   string // dm | group | bot | official | system; any other kind is outside the selection (Selected)
	// Bot: the other side is a bot (a dm with a bot, a group of bots), whatever the kind.
	Bot       bool
	Muted     bool
	Collapsed bool
	Done      bool
}

// ChatFactsOf are the facts of a chat as a daily batch lists it.
func ChatFactsOf(source string, sc SourceChat) ChatFacts {
	return ChatFacts{Source: source, ID: sc.ID, Kind: sc.Kind, Muted: sc.Muted, Collapsed: sc.Collapsed, Done: sc.Done}
}

// Selected says the selection decides about the chat: its kind is one the kind rules know (dm, group, bot,
// official, system). A chat of any other kind (mail, an issue, a pull request) or of none is kept as before the
// selection: everything, for text_days.
func (c ChatFacts) Selected() bool {
	switch c.Kind {
	case "dm", "group", "bot", "official", "system":
		return true
	}
	return false
}

// MessageFacts are what a source reports about a message, and whether it is within the mine rule's context.
type MessageFacts struct {
	Mine         bool
	Mentioned    bool // the owner by name
	MentionedAll bool // an @all: a mention only with include_all
	NearMine     bool // within Selection.MineContext messages of one the owner sent
}

// MessageFactsOf are a message's own facts (NearMine is the caller's: it knows the neighbours).
func MessageFactsOf(m SourceMessage) MessageFacts {
	return MessageFacts{Mine: m.Mine, Mentioned: m.Mentioned, MentionedAll: m.MentionedAll}
}

// ruleDays are a message rule's days, resolved: its own, else text_days; attachments its own, else the source's,
// never more than its text days. An off rule keeps text_days (the safety rule: turning a rule off only stops new
// writes, it never shortens what is kept).
func (s *Selection) ruleDays(ret Retention, source, name string) (text, att int) {
	text, att = s.ruleRaw(ret, source, name)
	return text, min(att, text)
}

// ruleRaw is ruleDays without the attachment cap.
func (s *Selection) ruleRaw(ret Retention, source, name string) (text, att int) {
	text, att = ret.Text(), ret.SourceAttachments(source)
	if r, ok := s.Messages.Rules[name]; ok && r.On {
		if r.Days != nil {
			text = *r.Days
		}
		if r.AttachmentDays != nil {
			att = *r.AttachmentDays
		}
	}
	return text, att
}

func (s *Selection) matches(name string, c ChatFacts, m MessageFacts) bool {
	switch name {
	case RuleMentions:
		r := s.Messages.Rules[RuleMentions]
		return m.Mentioned || m.MentionedAll && (r.IncludeAll == nil || *r.IncludeAll)
	case RuleMine:
		return m.Mine || m.NearMine
	case RuleDM:
		return c.Kind == "dm" && !c.Bot
	case RuleGroup:
		return c.Kind == "group" && !c.Bot && !c.Muted && !c.Collapsed && !c.Done
	case RuleBot:
		return c.Kind == "bot" || c.Bot
	case RuleOfficial:
		return c.Kind == "official" || c.Kind == "system"
	case RuleMuted:
		return c.Muted
	case RuleCollapsed:
		return c.Collapsed
	case RuleDone:
		return c.Done
	}
	return false
}

// personal: the rules a per-item "read, but do not store" keeps.
func personal(name string) bool { return name == RuleMentions || name == RuleMine }

// decide runs the levels, keeping only the rules keep allows.
func (s *Selection) decide(ret Retention, c ChatFacts, m MessageFacts, keep func(string) bool) Decision {
	for _, level := range s.Messages.Order {
		var matched, on []string
		for _, name := range level {
			if keep != nil && !keep(name) || !s.matches(name, c, m) {
				continue
			}
			matched = append(matched, name)
			if s.Messages.Rules[name].On {
				on = append(on, name)
			}
		}
		if len(matched) == 0 {
			continue
		}
		if len(on) == 0 {
			return Decision{Rule: strings.Join(matched, "+")}
		}
		d := Decision{Store: true, Rule: strings.Join(on, "+")}
		for _, name := range on {
			text, att := s.ruleDays(ret, c.Source, name)
			d.Days, d.AttachmentDays = max(d.Days, text), max(d.AttachmentDays, att)
		}
		return d
	}
	return Decision{}
}

// Evaluate decides one message of a chat. A chat outside the selection (ChatFacts.Selected false) is stored as
// before it, with text_days and the source's attachment days (RuleItem is not used; Rule is "").
func (s *Selection) Evaluate(ret Retention, c ChatFacts, m MessageFacts) Decision {
	if !c.Selected() {
		text := ret.Text()
		return Decision{Store: true, Days: text, AttachmentDays: min(ret.SourceAttachments(c.Source), text)}
	}
	if it := s.Item(c.Source, ItemChat, c.ID); it != nil {
		switch {
		case !it.Read:
			return Decision{Rule: RuleItem}
		case !it.Store:
			return s.decide(ret, c, m, personal)
		}
		d := s.decide(ret, c, m, nil)
		if !d.Store || it.Days != nil {
			text := ret.Text()
			if it.Days != nil {
				text = *it.Days
			}
			att := ret.SourceAttachments(c.Source)
			if d.Store {
				att = d.AttachmentDays
			}
			return Decision{Store: true, Rule: RuleItem, Days: text, AttachmentDays: min(att, text)}
		}
		return d // the rule that would otherwise decide, with its days
	}
	return s.decide(ret, c, m, nil)
}

// UsualRule is the rule that decides a plain message of the chat (not the owner's, mentioning no one): what the
// retention ledger leaves unrecorded. "" for a chat outside the selection, or one no rule matches.
func (s *Selection) UsualRule(ret Retention, c ChatFacts) string {
	if !c.Selected() {
		return ""
	}
	return s.Evaluate(ret, c, MessageFacts{}).Rule
}

// LineDays are the text and attachment days of a stored line of a chat whose deciding rule was rule (Decision.Rule;
// a line without a ledger entry has the chat's usual rule), as the document says now: a rule's days may have changed
// since. An off rule, an unknown one and "" keep text_days; a level's union keeps the largest; a per-item setting
// that stores the chat with days applies to every line (attachments: the rule's, else the source's, within those days).
func (s *Selection) LineDays(ret Retention, source, chatID, rule string) (text, att int) {
	if days, ok := s.itemDays(source, chatID); ok {
		att = ret.SourceAttachments(source)
		if rule != "" && rule != RuleItem {
			att = 0
			for _, name := range strings.Split(rule, "+") {
				_, a := s.ruleRaw(ret, source, name)
				att = max(att, a)
			}
		}
		return days, min(att, days)
	}
	return s.lineDays(ret, source, rule)
}

// itemDays: a per-item setting stores the chat with days of its own.
func (s *Selection) itemDays(source, chatID string) (int, bool) {
	if it := s.Item(source, ItemChat, chatID); it != nil && it.Read && it.Store && it.Days != nil {
		return *it.Days, true
	}
	return 0, false
}

func (s *Selection) lineDays(ret Retention, source, rule string) (text, att int) {
	text0, att0 := ret.Text(), min(ret.SourceAttachments(source), ret.Text())
	if s == nil || rule == "" || rule == RuleItem {
		return text0, att0
	}
	for i, name := range strings.Split(rule, "+") {
		t, a := s.ruleDays(ret, source, name)
		if i == 0 {
			text, att = t, a
			continue
		}
		text, att = max(text, t), max(att, a)
	}
	return text, att
}

// dayBounds are the fewest and most text and attachment days any line of a chat can have (LineDays), and an
// attachment no line refers to (the source's attachment days within text_days), so pruning reads a day file only
// when its lines may differ.
func (s *Selection) dayBounds(ret Retention, source, chatID string) (minText, maxText, minAtt, maxAtt int) {
	text0, att0 := ret.Text(), min(ret.SourceAttachments(source), ret.Text())
	minText, maxText, minAtt, maxAtt = text0, text0, att0, att0
	if s == nil {
		return
	}
	addAtt := func(a int) { minAtt, maxAtt = min(minAtt, a), max(maxAtt, a) }
	if days, ok := s.itemDays(source, chatID); ok {
		minText, maxText = days, days
		addAtt(min(ret.SourceAttachments(source), days))
		for _, name := range MessageRuleNames {
			_, a := s.ruleRaw(ret, source, name)
			addAtt(min(a, days))
		}
		return
	}
	for _, name := range MessageRuleNames {
		t, a := s.ruleDays(ret, source, name)
		minText, maxText = min(minText, t), max(maxText, t)
		addAtt(a)
	}
	return
}

// ReadPlan is what reading a chat needs (the history import's plan; daily ingestion reads what a connector sends).
type ReadPlan struct {
	// Read: some rule that can match one of its messages stores it. Not read: nothing of it is stored at all.
	Read bool `json:"read,omitempty"`
	// Whole: a plain message is stored (the chat's rule, or a per-item store), so the whole chat is read.
	Whole bool `json:"whole,omitempty"`
	// Personal: only what the personal rules keep is stored (Read without Whole): the owner's messages (Mine, with
	// MineContext on each side) and the ones that mention them (Mentions).
	Personal bool `json:"personal,omitempty"`
	Mine     bool `json:"mine,omitempty"`
	Mentions bool `json:"mentions,omitempty"`
	// Context is how many messages are read on each side of the owner's (the mine rule's context, with Mine).
	Context int `json:"context,omitempty"`
	// Days is how far back anything of it can be stored (the most days of the rules that can store its messages).
	Days int `json:"days,omitempty"`
}

// Wider says p reads something q does not: a chat q left unread, a whole chat q read only around the owner's
// messages, a kind of personal message q did not look for, more around the owner's messages, or further back.
func (p ReadPlan) Wider(q ReadPlan) bool {
	switch {
	case !p.Read:
		return false
	case !q.Read, p.Whole && !q.Whole:
		return true
	case q.Whole:
		return p.Days > q.Days
	}
	return p.Mine && !q.Mine || p.Mentions && !q.Mentions || p.Mine && p.Context > q.Context || p.Days > q.Days
}

// ReadChat is the read plan of a chat: whether anything of it is stored, all of it or what the personal rules keep.
func (s *Selection) ReadChat(ret Retention, c ChatFacts) ReadPlan {
	if !c.Selected() {
		return ReadPlan{Read: true, Whole: true, Days: ret.Text()}
	}
	if it := s.Item(c.Source, ItemChat, c.ID); it != nil && !it.Read {
		return ReadPlan{}
	}
	var p ReadPlan
	plain := s.Evaluate(ret, c, MessageFacts{})
	mine := s.Evaluate(ret, c, MessageFacts{Mine: true})
	mention := s.Evaluate(ret, c, MessageFacts{Mentioned: true})
	everyone := s.Evaluate(ret, c, MessageFacts{MentionedAll: true})
	for _, d := range []Decision{plain, mine, mention, everyone} {
		if d.Store {
			p.Days = max(p.Days, d.Days)
		}
	}
	if plain.Store {
		p.Read, p.Whole = true, true
		return p
	}
	p.Mine, p.Mentions = mine.Store, mention.Store || everyone.Store
	if p.Mine {
		p.Context = s.MineContext()
	}
	p.Read = p.Mine || p.Mentions
	p.Personal = p.Read
	return p
}
