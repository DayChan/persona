package kb

import (
	"encoding/json"
	"slices"

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
