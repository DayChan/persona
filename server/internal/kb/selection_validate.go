package kb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

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
