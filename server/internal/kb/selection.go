package kb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// The selection (docs/assistant/knowledge.md "Selection", #1523): one document that decides what the knowledge base
// brings in from chats and documents, for daily ingestion and the history import alike, and for how many days. It is
// plain data: the manager keeps it in its settings table and hands it to the Engine (Engine.Selection); everything in
// this file is pure, without I/O.
//
// Rules are grouped in levels (Order). For a message, the first level with a rule that matches decides: stored when
// any of its matching rules is on (days: the largest of theirs), not stored when all of them are off. An off rule is a
// decision too: it never falls through to a lower level. A message no rule matches is not stored. A per-item setting
// (Items) comes before every level.

// SelectionVersion is the version a selection document has.
const SelectionVersion = 1

// Message rules (MessageRules.Rules keys).
const (
	RuleMentions  = "mentions"  // the message mentions the owner (an @all too, with include_all)
	RuleMine      = "mine"      // the owner sent it, or it is within context messages of one the owner sent
	RuleDM        = "dm"        // a direct chat with a person
	RuleGroup     = "group"     // a regular group: not muted, collapsed or done
	RuleBot       = "bot"       // a chat with a bot, an app, an AI agent
	RuleOfficial  = "official"  // an official or service account, a system account
	RuleMuted     = "muted"     // the owner muted the chat
	RuleCollapsed = "collapsed" // the owner folded the chat away
	RuleDone      = "done"      // the owner marked the chat done
)

// RuleItem is the deciding rule of what a per-item setting stored (Decision.Rule, the retention ledger).
const RuleItem = "item"

// MessageRuleNames are the message rules in the default order.
var MessageRuleNames = []string{RuleMentions, RuleMine, RuleDM, RuleGroup, RuleBot, RuleOfficial, RuleMuted, RuleCollapsed, RuleDone}

// DocRuleNames are the document rules in the default order: the signals a source reports (kbwire.Signals).
var DocRuleNames = kbwire.Signals

// Item kinds (SelectionItem.Kind).
const (
	ItemChat = "chat"
	ItemDoc  = "doc"
)

// Bounds a document is held to (Validate).
const (
	SelectionMostDays    = 36500 // a hundred years
	SelectionMostContext = 200
	defaultMineContext   = 50
)

// Selection is the whole document (the JSON of GET / PUT /api/v1/kb/selection).
type Selection struct {
	Version  int          `json:"version"`
	Messages MessageRules `json:"messages"`
	Docs     DocRules     `json:"docs"`
	// Items are per-chat and per-document settings, keyed by the source's stable id; they come before every rule.
	Items []SelectionItem `json:"items"`
	// History is what the history import adds per source: how far back it goes.
	History map[string]HistorySelection `json:"history"`

	// stopgap: KeepEverything's, standing in for a document that cannot be read (never stored, never sent).
	stopgap bool
}

// KeepEverything is what a selection that cannot be read stands for: every rule on, every days null, so everything
// sent is written and kept text_days, as before the selection. It is a stopgap (Stopgap): Engine.Prune deletes
// nothing under it (a line the user set to keep longer than text_days must not go because the document did not
// read), and nothing that records what the user approved goes by it.
func KeepEverything() Selection {
	s := Defaults()
	for name, r := range s.Messages.Rules {
		r.On = true
		s.Messages.Rules[name] = r
	}
	for name, r := range s.Docs.Rules {
		r.On = true
		s.Docs.Rules[name] = r
	}
	s.stopgap = true
	return s
}

// Stopgap says s is KeepEverything's stand-in for a document that cannot be read now.
func (s *Selection) Stopgap() bool { return s != nil && s.stopgap }

// MessageRules are the rules for chat messages and their priority.
type MessageRules struct {
	// Order lists the levels, highest priority first; each names the rules of one level, every rule exactly once.
	Order [][]string             `json:"order"`
	Rules map[string]MessageRule `json:"rules"`
}

// MessageRule is one message rule. Days nil: kb.text_days; AttachmentDays nil: the source's attachment days
// (kb.attachment_days_by_source, else kb.attachment_days). A line's attachment days never exceed its text days.
type MessageRule struct {
	On             bool `json:"on"`
	Days           *int `json:"days"`
	AttachmentDays *int `json:"attachment_days"`
	// IncludeAll (mentions only): an @all counts as a mention (nil: true).
	IncludeAll *bool `json:"include_all,omitempty"`
	// Context (mine only): the messages before and after each of the owner's own that count as theirs (nil: 50).
	Context *int `json:"context,omitempty"`
}

// DocRules are the rules for documents (by their signals) and their priority.
type DocRules struct {
	Order [][]string         `json:"order"`
	Rules map[string]DocRule `json:"rules"`
}

// DocRule is one document rule. Days nil: kb.text_days.
type DocRule struct {
	On   bool `json:"on"`
	Days *int `json:"days"`
}

// SelectionItem is the setting of one chat or document, before every rule.
//   - Read false: nothing of it is read or stored, not even mentions or the owner's own messages.
//   - Read and Store: everything is stored, for Days (nil: the rule that would otherwise decide, else text_days).
//   - Read without Store: only what the personal rules (mentions, mine) keep is stored.
type SelectionItem struct {
	Source string `json:"source"`
	Kind   string `json:"kind"` // ItemChat | ItemDoc
	ID     string `json:"id"`   // the source's stable id: a chat id, a document token
	Name   string `json:"name,omitempty"`
	Read   bool   `json:"read"`
	Store  bool   `json:"store"`
	Days   *int   `json:"days"`
}

// HistorySelection is the history import's setting for one source. BackDays nil: as far as the deciding rule's days.
type HistorySelection struct {
	BackDays *int `json:"back_days"`
}

func intp(n int) *int    { return &n }
func boolp(b bool) *bool { return &b }

// Defaults is the selection a missing document means (#1523).
func Defaults() Selection {
	rules := map[string]MessageRule{}
	for _, r := range MessageRuleNames {
		rules[r] = MessageRule{}
	}
	rules[RuleMentions] = MessageRule{On: true, IncludeAll: boolp(true)}
	rules[RuleMine] = MessageRule{On: true, Context: intp(defaultMineContext)}
	rules[RuleDM] = MessageRule{On: true}
	docs := map[string]DocRule{}
	for _, r := range DocRuleNames {
		docs[r] = DocRule{On: true}
	}
	return Selection{
		Version: SelectionVersion,
		Messages: MessageRules{
			Order: [][]string{{RuleMentions, RuleMine}, {RuleDM}, {RuleGroup}, {RuleBot}, {RuleOfficial}, {RuleMuted}, {RuleCollapsed}, {RuleDone}},
			Rules: rules,
		},
		Docs: DocRules{
			Order: [][]string{{kbwire.SignalMentions, kbwire.SignalCommentMentions}, {kbwire.SignalAuthored}, {kbwire.SignalViewed}},
			Rules: docs,
		},
		Items:   []SelectionItem{},
		History: map[string]HistorySelection{},
	}
}

// ErrSelection is what Validate and ParseSelection refuse a document with (wrapped).
var ErrSelection = errors.New("selection")

func selErr(f string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrSelection, fmt.Sprintf(f, a...))
}

// ParseSelection reads a whole document strictly (no unknown field, nothing after it) and validates it.
func ParseSelection(raw []byte) (Selection, error) {
	var s Selection
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Selection{}, selErr("%v", err)
	}
	if dec.More() {
		return Selection{}, selErr("more than one document")
	}
	if err := s.Validate(); err != nil {
		return Selection{}, err
	}
	return s, nil
}

// Validate checks a whole document: the version; every rule there, by known names only; the order's levels holding
// every rule exactly once; days and attachment days 1…SelectionMostDays or null; include_all only on mentions and
// context (0…SelectionMostContext) only on mine; items with a source, a kind and an id, each once, never stored
// without being read; history by source with back_days 1…SelectionMostDays or null.
func (s Selection) Validate() error {
	if s.Version != SelectionVersion {
		return selErr("version %d (want %d)", s.Version, SelectionVersion)
	}
	if err := checkOrder("messages", s.Messages.Order, MessageRuleNames); err != nil {
		return err
	}
	if err := checkRuleNames("messages", mapKeys(s.Messages.Rules), MessageRuleNames); err != nil {
		return err
	}
	for name, r := range s.Messages.Rules {
		if err := checkDays("messages."+name+".days", r.Days); err != nil {
			return err
		}
		if err := checkDays("messages."+name+".attachment_days", r.AttachmentDays); err != nil {
			return err
		}
		if r.IncludeAll != nil && name != RuleMentions {
			return selErr("messages.%s: include_all is only for mentions", name)
		}
		if r.Context != nil {
			if name != RuleMine {
				return selErr("messages.%s: context is only for mine", name)
			}
			if *r.Context < 0 || *r.Context > SelectionMostContext {
				return selErr("messages.mine.context %d (0 to %d)", *r.Context, SelectionMostContext)
			}
		}
	}
	if err := checkOrder("docs", s.Docs.Order, DocRuleNames); err != nil {
		return err
	}
	if err := checkRuleNames("docs", mapKeys(s.Docs.Rules), DocRuleNames); err != nil {
		return err
	}
	for name, r := range s.Docs.Rules {
		if err := checkDays("docs."+name+".days", r.Days); err != nil {
			return err
		}
	}
	seen := map[[3]string]bool{}
	for i, it := range s.Items {
		if it.Source == "" || it.ID == "" {
			return selErr("items[%d]: a source and an id are required", i)
		}
		if it.Kind != ItemChat && it.Kind != ItemDoc {
			return selErr("items[%d]: kind %q (chat or doc)", i, it.Kind)
		}
		key := [3]string{it.Source, it.Kind, it.ID}
		if seen[key] {
			return selErr("items[%d]: %s %s of %s is there twice", i, it.Kind, it.ID, it.Source)
		}
		seen[key] = true
		if !it.Read && it.Store {
			return selErr("items[%d]: store needs read", i)
		}
		if err := checkDays(fmt.Sprintf("items[%d].days", i), it.Days); err != nil {
			return err
		}
	}
	for src, h := range s.History {
		if src == "" {
			return selErr("history: a source without a name")
		}
		if err := checkDays("history."+src+".back_days", h.BackDays); err != nil {
			return err
		}
	}
	return nil
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func checkOrder(what string, order [][]string, names []string) error {
	seen := map[string]bool{}
	for i, level := range order {
		if len(level) == 0 {
			return selErr("%s.order[%d] is empty", what, i)
		}
		for _, r := range level {
			if !slices.Contains(names, r) {
				return selErr("%s.order: unknown rule %q", what, r)
			}
			if seen[r] {
				return selErr("%s.order: %q is there twice", what, r)
			}
			seen[r] = true
		}
	}
	for _, r := range names {
		if !seen[r] {
			return selErr("%s.order: %q is missing", what, r)
		}
	}
	return nil
}

func checkRuleNames(what string, have, names []string) error {
	for _, r := range have {
		if !slices.Contains(names, r) {
			return selErr("%s.rules: unknown rule %q", what, r)
		}
	}
	for _, r := range names {
		if !slices.Contains(have, r) {
			return selErr("%s.rules: %q is missing", what, r)
		}
	}
	return nil
}

func checkDays(what string, d *int) error {
	if d != nil && (*d < 1 || *d > SelectionMostDays) {
		return selErr("%s %d (1 to %d, or null)", what, *d, SelectionMostDays)
	}
	return nil
}

// Retention is the manager's kb retention settings the selection's null days fall back to.
type Retention struct {
	TextDays               int            // kb.text_days (<= 0: 365)
	AttachmentDays         int            // kb.attachment_days (<= 0: 30)
	AttachmentDaysBySource map[string]int // kb.attachment_days_by_source (non-positive values ignored)
}

// Text is the text days (365 by default).
func (r Retention) Text() int { return or(r.TextDays, 365) }

// SourceAttachments is a source's attachment days before any cap: its AttachmentDaysBySource, else AttachmentDays.
func (r Retention) SourceAttachments(source string) int {
	if d := r.AttachmentDaysBySource[source]; d > 0 {
		return d
	}
	return or(r.AttachmentDays, 30)
}

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

// Decision is what the selection says about one message or document.
type Decision struct {
	Store bool
	// Rule is what decided: a rule's name, the names of a level's rules joined by "+" (the matching ones that are
	// on, or all matching ones when every one is off), RuleItem, or "" when no rule matched. Retention goes by it
	// (a rule's days may change after the line is written).
	Rule string
	// Days and AttachmentDays: how long a stored line and its attachments are kept (0 when not stored).
	Days, AttachmentDays int
}

// Item is the per-item setting of a source's chat or document (nil: none).
func (s *Selection) Item(source, kind, id string) *SelectionItem {
	if s == nil {
		return nil
	}
	for i := range s.Items {
		it := &s.Items[i]
		if it.Source == source && it.Kind == kind && it.ID == id {
			return it
		}
	}
	return nil
}

// MineContext is how many messages on each side of one the owner sent count as theirs (0 when the mine rule is off).
func (s *Selection) MineContext() int {
	r, ok := s.Messages.Rules[RuleMine]
	if !ok || !r.On {
		return 0
	}
	if r.Context == nil {
		return defaultMineContext
	}
	return *r.Context
}

// BackDays is how far back the history import goes for a source (0: as far as the deciding rule's days).
func (s *Selection) BackDays(source string) int {
	if h, ok := s.History[source]; ok && h.BackDays != nil {
		return *h.BackDays
	}
	return 0
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

// DocFacts are what a source reports about a document: its stable id and its signals (kbwire.Item.Signals).
type DocFacts struct {
	Source  string
	ID      string
	Signals []string
}

// EvaluateDoc decides a document by its signals, as Evaluate a message: a per-item setting first (read false: not
// kept; store: kept, its days or else the deciding rule's or text_days; read without store: the rules decide), then
// the first level with a matching signal. A document without any signal matches no rule (not kept); a caller whose
// source reports none keeps its documents as before the selection.
func (s *Selection) EvaluateDoc(ret Retention, d DocFacts) Decision {
	var dec Decision
	for _, level := range s.Docs.Order {
		var matched, on []string
		for _, name := range level {
			if !slices.Contains(d.Signals, name) {
				continue
			}
			matched = append(matched, name)
			if s.Docs.Rules[name].On {
				on = append(on, name)
			}
		}
		if len(matched) == 0 {
			continue
		}
		if len(on) == 0 {
			dec = Decision{Rule: strings.Join(matched, "+")}
			break
		}
		dec = Decision{Store: true, Rule: strings.Join(on, "+")}
		for _, name := range on {
			days := ret.Text()
			if r := s.Docs.Rules[name]; r.Days != nil {
				days = *r.Days
			}
			dec.Days = max(dec.Days, days)
		}
		break
	}
	if it := s.Item(d.Source, ItemDoc, d.ID); it != nil {
		switch {
		case !it.Read:
			return Decision{Rule: RuleItem}
		case it.Store && it.Days != nil:
			return Decision{Store: true, Rule: RuleItem, Days: *it.Days}
		case it.Store && !dec.Store:
			return Decision{Store: true, Rule: RuleItem, Days: ret.Text()}
		}
	}
	return dec
}

// DocSelected says whether a document a source hands over is stored (CollectItems). A per-item read false keeps it
// out whatever its signals; otherwise a document without signals is stored as before the selection (its source cannot
// tell why it concerns the owner), and one with signals as EvaluateDoc decides.
func (s *Selection) DocSelected(ret Retention, d DocFacts) bool {
	if it := s.Item(d.Source, ItemDoc, d.ID); it != nil && !it.Read {
		return false
	}
	return len(d.Signals) == 0 || s.EvaluateDoc(ret, d).Store
}

// DocDays is how many days a stored document is kept, as the selection says now; ok false means it is kept as before
// the selection: indefinitely (documents had no retention before it). Only days the user set apply, so the defaults
// (every days null) delete nothing: a per-item setting that stores it with days of its own; else the deciding level's
// rules that are on, when every one of them has days (the largest). A deciding level whose rules are all off, an on
// rule with null days, a per-item read false (it only stops new writes) and a document without a matching signal
// keep it.
func (s *Selection) DocDays(d DocFacts) (days int, ok bool) {
	if it := s.Item(d.Source, ItemDoc, d.ID); it != nil {
		switch {
		case !it.Read:
			return 0, false
		case it.Store && it.Days != nil:
			return *it.Days, true
		}
	}
	for _, level := range s.Docs.Order {
		matched, on, unbounded := false, false, false
		days = 0
		for _, name := range level {
			if !slices.Contains(d.Signals, name) {
				continue
			}
			matched = true
			r := s.Docs.Rules[name]
			switch {
			case !r.On:
			case r.Days == nil:
				on, unbounded = true, true
			default:
				on, days = true, max(days, *r.Days)
			}
		}
		if !matched {
			continue
		}
		if !on || unbounded || days <= 0 {
			return 0, false
		}
		return days, true
	}
	return 0, false
}

// SetItem puts a per-item setting in, replacing the one with the same source, kind and id.
func (s *Selection) SetItem(it SelectionItem) {
	for i := range s.Items {
		if x := s.Items[i]; x.Source == it.Source && x.Kind == it.Kind && x.ID == it.ID {
			s.Items[i] = it
			return
		}
	}
	s.Items = append(s.Items, it)
}

// RemoveItem takes a per-item setting out; false when there was none.
func (s *Selection) RemoveItem(source, kind, id string) bool {
	n := len(s.Items)
	s.Items = slices.DeleteFunc(s.Items, func(x SelectionItem) bool { return x.Source == source && x.Kind == kind && x.ID == id })
	return len(s.Items) != n
}

// Clone is a deep copy (the manager hands one out per caller).
func (s Selection) Clone() Selection {
	raw, err := json.Marshal(s)
	if err != nil {
		return s
	}
	var out Selection
	if json.Unmarshal(raw, &out) != nil {
		return s
	}
	out.stopgap = s.stopgap
	return out
}
