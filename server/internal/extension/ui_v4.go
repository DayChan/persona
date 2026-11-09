package extension

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Limits of what downgrade4 draws an older app: rows of a file tree, comments of a thread, texts of code.
const (
	oldTreeRows = 60
	oldComments = 30
	oldCodeText = 8
	// oldFilterChoices is how many choices a filter had before UI version 4.
	oldFilterChoices = 20
)

// newIcons are the icons of UI version 4: an older app gets none (its default).
var newIcons = []string{"branch", "commit", "code", "pull_request", "merge", "file"}

// downgrade4 draws UI version 4's components and fields with version 3's, in place (and then downgrade with older
// ones): tabs as a segmented, a split view as one column, a header as a text and rows, code and diffs as code texts, a
// file tree as rows, a thread as rows and markdown, a composer as a field and buttons. An action's values are left out
// (an older app sends none). What does not fit in ItemsMost is cut, with a word saying so.
func downgrade4(v *View, l i18n.Lang) {
	if t := v.Tabs; t != nil {
		choices := make([]UIChoice, 0, SegmentsMost)
		for _, it := range t.Items {
			if len(choices) < SegmentsMost || it.Value == t.Value {
				title := it.Title
				if it.Badge != "" {
					title = cmp.Or(title, it.Value) + " " + it.Badge
				}
				choices = append(choices, UIChoice{Value: it.Value, Title: clip(title, 12)})
			}
		}
		if len(choices) > SegmentsMost {
			choices = append(choices[:SegmentsMost-1], choices[len(choices)-1])
		}
		a := t.Action
		v.Sections = append([]Section{{Items: []UIElem{{Type: "segmented", Name: t.Name, Choices: choices, Value: t.Value, Action: &a}}}}, v.Sections...)
	}
	if f := v.Filter; f != nil && len(f.Choices) > oldFilterChoices {
		choices := slices.Clone(f.Choices[:oldFilterChoices-1])
		if i := slices.IndexFunc(f.Choices, func(c UIChoice) bool { return c.Value == f.Value }); i >= oldFilterChoices-1 {
			choices = append(choices, f.Choices[i])
		} else {
			choices = append(choices, f.Choices[oldFilterChoices-1])
		}
		f.Choices = choices
	}
	v.Tabs, v.Layout = nil, ""
	for _, a := range Actions(v) {
		a.Values = nil
	}
	oldIcon := func(icon string) string {
		if slices.Contains(newIcons, icon) {
			return ""
		}
		return icon
	}
	codeTexts := func(text string) []UIElem {
		var out []UIElem
		for text != "" && len(out) < oldCodeText {
			chunk := text
			if utf8.RuneCountInString(chunk) > StringMost {
				chunk = string([]rune(chunk)[:StringMost])
				if i := strings.LastIndexByte(chunk, '\n'); i > 0 {
					chunk = chunk[:i+1]
				}
			}
			text = text[len(chunk):]
			if t := strings.TrimRight(chunk, "\n"); t != "" {
				out = append(out, UIElem{Type: "text", Text: t, Style: "code"})
			}
		}
		if text != "" {
			out = append(out, UIElem{Type: "text", Text: i18n.T(l, "extension-ui-fallback.truncated"), Style: "caption"})
		}
		return out
	}
	commentItems := func(c Comment) []UIElem {
		row := UIElem{Type: "row", Title: clip(c.Author, StringMost), Badge: c.Badge}
		if !c.At.IsZero() {
			row.Value = c.At.Format("2006-01-02 15:04")
		}
		out := []UIElem{row}
		if body := strings.TrimSpace(c.Body); body != "" {
			if c.Event {
				out = []UIElem{{Type: "text", Text: clip(c.Author+" "+body, StringMost), Style: "caption"}}
			} else {
				out = append(out, UIElem{Type: "markdown", Text: clip(body, MarkdownMost)})
			}
		}
		return out
	}
	total := 0
	for i := range v.Sections {
		s := &v.Sections[i]
		s.Pane = ""
		items := make([]UIElem, 0, len(s.Items))
		for _, e := range s.Items {
			e.Icon = oldIcon(e.Icon)
			switch e.Type {
			case "header":
				text := e.Title
				var badges []string
				for _, b := range e.Badges {
					badges = append(badges, b.Text)
				}
				if len(badges) > 0 {
					text += "\n" + strings.Join(badges, " · ")
				}
				if e.Subtitle != "" {
					text += "\n" + e.Subtitle
				}
				items = append(items, UIElem{Type: "text", Text: clip(text, StringMost)})
				for _, f := range e.Facts {
					row := UIElem{Type: "row", Title: f.Value, Icon: oldIcon(f.Icon)}
					if f.Label != "" {
						row.Title, row.Value = f.Label, f.Value
					}
					items = append(items, row)
				}
			case "breadcrumb":
				var parts []string
				for _, c := range e.Crumbs {
					parts = append(parts, c.Title)
				}
				items = append(items, UIElem{Type: "text", Text: clip(strings.Join(parts, " / "), StringMost), Style: "secondary"})
			case "file_tree":
				for k, en := range e.Entries {
					if k == oldTreeRows {
						items = append(items, UIElem{Type: "text", Text: i18n.T(l, "extension-ui-fallback.files_remaining", i18n.Args{"count": len(e.Entries) - k}), Style: "caption"})
						break
					}
					row := UIElem{Type: "row", Title: clip(en.Path, StringMost), Icon: "doc"}
					if en.Kind == "dir" {
						row.Icon = "folder"
					}
					if en.Additions > 0 || en.Deletions > 0 {
						row.Value = fmt.Sprintf("+%d −%d", en.Additions, en.Deletions)
					}
					if en.Badge != nil {
						row.Badge = en.Badge
					} else if t, ok := fileStatusBadge[en.Status]; ok {
						t.Text = i18n.T(l, t.Text)
						row.Badge = &t
					}
					items = append(items, row)
				}
			case "code":
				if e.Path != "" {
					items = append(items, UIElem{Type: "text", Text: clip(e.Path, StringMost), Style: "secondary"})
				}
				items = append(items, codeTexts(e.Text)...)
			case "diff":
				row := UIElem{Type: "row", Title: clip(e.Path, StringMost), Icon: "doc"}
				if e.OldPath != "" && e.OldPath != e.Path {
					row.Subtitle = clip(e.OldPath+" →", StringMost)
				}
				row.Value = fmt.Sprintf("+%d −%d", e.Additions, e.Deletions)
				if t, ok := fileStatusBadge[e.Status]; ok {
					t.Text = i18n.T(l, t.Text)
					row.Badge = &t
				}
				items = append(items, row)
				var b strings.Builder
				for _, h := range e.Hunks {
					b.WriteString(cmp.Or(h.Header, fmt.Sprintf("@@ -%d +%d @@", h.OldStart, h.NewStart)))
					b.WriteByte('\n')
					for _, l := range h.Lines {
						b.WriteString(l)
						b.WriteByte('\n')
					}
				}
				switch {
				case e.Binary:
					items = append(items, UIElem{Type: "text", Text: i18n.T(l, "extension-ui-fallback.binary"), Style: "caption"})
				case b.Len() > 0:
					items = append(items, codeTexts(b.String())...)
				}
				for _, t := range e.Threads {
					if len(t.Comments) == 0 {
						continue
					}
					c := t.Comments[0]
					items = append(items, UIElem{Type: "text", Style: "secondary",
						Text: clip(i18n.T(l, "extension-ui-fallback.line_comment", i18n.Args{"line": t.Line, "author": c.Author, "body": strings.TrimSpace(c.Body)}), StringMost)})
				}
			case "comment_thread":
				if e.Title != "" {
					items = append(items, UIElem{Type: "text", Text: e.Title, Style: "secondary"})
				}
				for k, c := range e.Comments {
					if k == oldComments {
						items = append(items, UIElem{Type: "text", Text: i18n.T(l, "extension-ui-fallback.comments_remaining", i18n.Args{"count": len(e.Comments) - k}), Style: "caption"})
						break
					}
					items = append(items, commentItems(c)...)
				}
			case "composer":
				items = append(items, UIElem{Type: "field", Name: e.Name, Label: cmp.Or(e.Placeholder, i18n.T(l, "extension-ui-fallback.comment")), Kind: "text", Value: e.Value})
				for _, b := range e.Buttons {
					a := b.Action
					a.Submit = true
					role := ""
					if b.Tone == "danger" {
						role = "destructive"
					}
					items = append(items, UIElem{Type: "button", Title: b.Title, Action: &a, Role: role})
				}
			default:
				items = append(items, e)
			}
		}
		if total+len(items) > ItemsMost-1 {
			items = append(items[:max(ItemsMost-1-total, 0)], UIElem{Type: "text", Text: i18n.T(l, "extension-ui-fallback.too_long"), Style: "caption"})
			s.Items = items
			v.Sections = v.Sections[:i+1]
			return
		}
		total += len(items)
		s.Items = items
	}
}

// fileStatusBadge is how an older app shows a file's status.
var fileStatusBadge = map[string]Badge{
	"added":    {Text: "extension-ui-fallback.file_added", Tone: "positive"},
	"modified": {Text: "extension-ui-fallback.file_modified", Tone: "neutral"},
	"deleted":  {Text: "extension-ui-fallback.file_deleted", Tone: "danger"},
	"renamed":  {Text: "extension-ui-fallback.file_renamed", Tone: "caution"},
}
