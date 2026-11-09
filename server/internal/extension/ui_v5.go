package extension

import "github.com/nwverse/persona/server/internal/i18n"

// downgrade5 leaves out UI version 5's fields (code review, #3136), in place: a diff's context, viewed marks, generated
// files, drafts and suggestions, a thread's resolve, a code's line link. A draft thread's first comment gets a badge
// saying so; the pending review's bar becomes a section on top: a line saying how many drafts, its composer and discard
// as a button.
func downgrade5(v *View, l i18n.Lang) {
	if r := v.Review; r != nil {
		items := []UIElem{
			{Type: "text", Text: i18n.T(l, "extension-ui-fallback.review_drafts", i18n.Args{"count": r.Count}), Style: "secondary"},
			{Type: "composer", Name: r.Name, Placeholder: r.Placeholder, Buttons: r.Buttons},
		}
		if r.Discard != nil {
			items = append(items, UIElem{Type: "button", Title: i18n.T(l, "extension-ui-fallback.discard"), Action: r.Discard, Role: "destructive"})
		}
		v.Sections = append([]Section{{Items: items}}, v.Sections...)
		v.Review = nil
	}
	for i := range v.Sections {
		for j := range v.Sections[i].Items {
			e := &v.Sections[i].Items[j]
			e.Context, e.MarkViewed, e.Draft = nil, nil, nil
			e.Viewed, e.Generated, e.Suggestions = false, false, false
			e.Revision, e.Similarity, e.LineURL = "", 0, ""
			for k := range e.Entries {
				e.Entries[k].Viewed, e.Entries[k].Generated = false, false
			}
			for k := range e.Threads {
				t := &e.Threads[k]
				if t.Pending && len(t.Comments) > 0 && t.Comments[0].Badge == nil {
					t.Comments[0].Badge = &Badge{Text: i18n.T(l, "extension-ui-fallback.draft"), Tone: "caution"}
				}
				t.Pending, t.Resolve = false, nil
			}
		}
	}
}
