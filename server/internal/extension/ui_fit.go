package extension

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Fit makes a View a plugin drew into one an app that draws UI version uiVersion can draw at slot: items of types
// this hub does not know (a plugin written for a newer hub) are left out, a section they emptied with them; those
// newer than the app are drawn with what it knows where that says the same (a markdown as text, a table as rows, an
// empty state as a text and a button, a section's more as a button, a filter as a segmented), left out where it does
// not; refresh_after is held to its bounds. The view the extension drew is checked first (CheckView), whatever the
// app draws, so that a view is refused or drawn alike for every app; then what the app gets is checked again
// (wire.CheckFitted), a compact one to its limits. It says what types it left out.
func Fit(v *View, slot UISlot, uiVersion int, languages ...i18n.Lang) (dropped []string, err error) {
	return fit(v, slot, uiVersion, false, languages...)
}

func fit(v *View, slot UISlot, uiVersion int, native bool, languages ...i18n.Lang) (dropped []string, err error) {
	l := i18n.Default
	if len(languages) > 0 {
		l = languages[0].Or(i18n.Default)
	}
	if v == nil {
		return nil, errors.New("no view")
	}
	dropped = wire.StripAssistantFields(v)
	if v.Sections == nil {
		v.Sections = []Section{}
	}
	for i := range v.Sections {
		s := &v.Sections[i]
		s.Items = slices.DeleteFunc(s.Items, func(e UIElem) bool {
			if _, known := elemSince[e.Type]; known {
				return false
			}
			if !slices.Contains(dropped, e.Type) {
				dropped = append(dropped, e.Type)
			}
			return true
		})
	}
	switch {
	case v.RefreshAfter < 0:
		v.RefreshAfter = 0
	case v.RefreshAfter > 0 && v.RefreshAfter < RefreshLeast:
		v.RefreshAfter = RefreshLeast
	case v.RefreshAfter > RefreshMost:
		v.RefreshAfter = RefreshMost
	}
	if !native {
		if err := CheckView(v); err != nil {
			return dropped, err
		}
	}
	compact := slot.Size == SizeCompact
	if compact {
		if err := checkCompact6(v); err != nil {
			return dropped, err
		}
	}
	if uiVersion < 6 {
		downgrade6(v, uiVersion, compact, l)
	}
	if uiVersion < 5 {
		downgrade5(v, l)
	}
	if uiVersion < 4 {
		downgrade4(v, l)
	}
	if uiVersion < 3 {
		downgrade(v, uiVersion, l)
	}
	sections := v.Sections[:0]
	for _, s := range v.Sections {
		items := s.Items[:0]
		for _, e := range s.Items {
			since, known := elemSince[e.Type]
			if !known || since > uiVersion {
				if !slices.Contains(dropped, e.Type) {
					dropped = append(dropped, e.Type)
				}
				continue
			}
			items = append(items, e)
		}
		if len(items) == 0 && len(s.Items) > 0 && s.Footer == "" {
			continue
		}
		if s.Items = items; s.Items == nil {
			s.Items = []UIElem{}
		}
		sections = append(sections, s)
	}
	v.Sections = sections
	if !native {
		if err := wire.CheckFitted(v); err != nil {
			return dropped, err
		}
	}
	if compact {
		if v.Search != nil || v.Filter != nil || slices.ContainsFunc(v.Sections, func(s Section) bool { return s.More != nil }) {
			return dropped, errors.New("a card has no search, filter or more: open a page for them")
		}
		n := 0
		for _, s := range v.Sections {
			n += len(s.Items)
			for _, e := range s.Items {
				n += max(len(e.Tiles)-1, 0)
				if e.Type == "field" || e.Type == "composer" {
					return dropped, errors.New("a card has no fields: open a page for them")
				}
			}
		}
		if len(v.Sections) > CompactSections || n > CompactItems {
			return dropped, fmt.Errorf("a card has at most %d sections and %d items", CompactSections, CompactItems)
		}
	}
	return dropped, nil
}

// FitResult makes what ui.action answered something the app can use, past the view (Fit): lines and components in
// place (UI version 5) are checked and the components fitted one by one; an older app gets neither, so it draws the
// page again.
func FitResult(res *ActionResult, slot UISlot, uiVersion int, languages ...i18n.Lang) error {
	l := i18n.Default
	if len(languages) > 0 {
		l = languages[0].Or(i18n.Default)
	}
	if uiVersion < 5 {
		res.Lines, res.Items = nil, nil
		return nil
	}
	if err := wire.CheckLines(res.Lines); err != nil {
		return err
	}
	if len(res.Items) == 0 {
		return nil
	}
	v := View{Sections: []Section{{Items: res.Items}}}
	for _, e := range res.Items {
		if e.Key == "" {
			return errors.New("items: each has the key of the one it stands for")
		}
	}
	if _, err := Fit(&v, slot, uiVersion, l); err != nil {
		return err
	}
	if len(v.Sections) == 0 || len(v.Sections[0].Items) != len(res.Items) {
		return errors.New("items: of types this app draws")
	}
	res.Items = v.Sections[0].Items
	return nil
}

// downgrade draws v3's components and fields with those of uiVersion (1 or 2), in place: what an older app gets.
func downgrade(v *View, uiVersion int, l i18n.Lang) {
	var filter []UIElem
	if f := v.Filter; f != nil && uiVersion >= 2 && len(f.Choices) <= SegmentsMost &&
		!slices.ContainsFunc(f.Choices, func(c UIChoice) bool { return utf8.RuneCountInString(cmp.Or(c.Title, c.Value)) > 12 }) {
		filter = []UIElem{{Type: "segmented", Name: f.Name, Choices: f.Choices, Value: f.Value, Action: &f.Action}}
	}
	v.Search, v.Filter = nil, nil
	if len(filter) > 0 {
		v.Sections = append([]Section{{Items: filter}}, v.Sections...)
	}
	for i := range v.Sections {
		s := &v.Sections[i]
		items := make([]UIElem, 0, len(s.Items))
		for _, e := range s.Items {
			e.Key, e.Icon, e.Avatar, e.Thumbnail = "", "", nil, nil
			if e.Action != nil {
				a := *e.Action
				a.Push = nil
				e.Action = &a
			}
			switch e.Type {
			case "markdown":
				items = append(items, UIElem{Type: "text", Text: clip(e.Text, StringMost)})
			case "table":
				for _, r := range e.Rows {
					if len(r) == 0 {
						continue
					}
					items = append(items, UIElem{Type: "row", Title: cmp.Or(clip(r[0], StringMost), "—"), Value: clip(strings.Join(r[1:], " · "), StringMost)})
				}
				if e.Caption != "" {
					items = append(items, UIElem{Type: "text", Text: e.Caption, Style: "caption"})
				}
			case "empty":
				items = append(items, UIElem{Type: "text", Text: clip(strings.TrimSpace(e.Title+"\n"+e.Detail), StringMost), Style: "secondary"})
				if b := e.Button; b != nil {
					a := b.Action
					a.Push = nil
					items = append(items, UIElem{Type: "button", Title: b.Title, Action: &a})
				}
			case "loading":
				items = append(items, UIElem{Type: "text", Text: cmp.Or(e.Title, i18n.T(l, "extension-ui-fallback.loading")), Style: "secondary"})
			default:
				items = append(items, e)
			}
		}
		if m := s.More; m != nil {
			a := m.Action
			a.Push = nil
			items = append(items, UIElem{Type: "button", Title: cmp.Or(m.Title, i18n.T(l, "extension-ui-fallback.load_more")), Action: &a})
		}
		s.More, s.Items = nil, items
	}
}

// clip is s cut to at most n characters, an ellipsis ending one that was longer.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
