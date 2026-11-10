package kb

import (
	"slices"
	"strings"
)

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
