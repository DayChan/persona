package extension

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Limits of a widget (FitWidget): the items of each family, the stats of a stat_grid, the rows, the lines of a text.
var widgetItems = map[string]int{"small": 4, "medium": 6, "large": 12}

const (
	widgetSections   = 2
	widgetStats      = 4
	widgetStatsSmall = 2
	widgetRows       = 4
	widgetLines      = 3
	widgetText       = 200
)

// widgetKinds are the components a widget draws; the rest are left out, or drawn as text (markdown, empty, loading).
var widgetKinds = []string{"metric", "stat_grid", "progress", "chart", "text", "row"}

// FitWidget makes a View an extension drew for a widget into one the family draws (docs/extensions/extensions.md "System-level places"):
// first as any view (Fit), then held to a widget's subset — a metric, a stat_grid, a progress, a chart, a few lines of
// text, a few rows, nothing to tap (a tap opens the widget's page), no search, filter or more — cutting what is too
// long rather than refusing it. A complication (accessory_*) draws one metric, progress or line of text, the first.
// It says what it left out.
func FitWidget(v *View, family string, uiVersion int, languages ...i18n.Lang) (dropped []string, err error) {
	l := i18n.Default
	if len(languages) > 0 {
		l = languages[0].Or(i18n.Default)
	}
	if !slices.Contains(WidgetFamilies, family) {
		return nil, fmt.Errorf("a widget is drawn at one of %s", strings.Join(WidgetFamilies, ", "))
	}
	slot, _ := Slot("widget")
	// a widget draws none of UI version 6 yet: it gets what an app of version 5 gets
	if dropped, err = Fit(v, slot, min(uiVersion, 5), l); err != nil {
		return dropped, err
	}
	drop := func(what string) {
		if !slices.Contains(dropped, what) {
			dropped = append(dropped, what)
		}
	}
	if v.Search != nil || v.Filter != nil {
		drop("search")
	}
	v.Search, v.Filter, v.RefreshAfter = nil, nil, 0
	complication := strings.HasPrefix(family, "accessory_")
	stats := widgetStats
	if family == "small" {
		stats = widgetStatsSmall
	}
	sections := []Section{}
	n, rows := 0, 0
	for _, s := range v.Sections {
		if s.More != nil {
			drop("more")
		}
		var kept []UIElem
		for _, e := range s.Items {
			switch e.Type {
			case "markdown":
				e = UIElem{Type: "text", Text: e.Text}
			case "empty":
				e = UIElem{Type: "text", Text: strings.TrimSpace(e.Title + "\n" + e.Detail), Style: "secondary"}
			case "loading":
				e = UIElem{Type: "text", Text: cmp.Or(e.Title, i18n.T(l, "extension-ui-fallback.loading")), Style: "secondary"}
			}
			if !slices.Contains(widgetKinds, e.Type) {
				drop(e.Type)
				continue
			}
			e.Action, e.Avatar, e.Thumbnail = nil, nil, nil
			if complication {
				if e.Type == "metric" || e.Type == "text" || e.Type == "progress" && family != "accessory_inline" {
					if e.Type == "text" {
						e.Text = widgetLinesOf(e.Text, map[bool]int{true: widgetLines, false: 1}[family == "accessory_rectangular"])
					}
					v.Sections = []Section{{Items: []UIElem{e}}}
					return dropped, nil
				}
				continue
			}
			switch e.Type {
			case "text":
				e.Text = widgetLinesOf(e.Text, widgetLines)
			case "stat_grid":
				if len(e.Stats) > stats {
					e.Stats = e.Stats[:stats]
				}
			case "row":
				if rows++; rows > widgetRows {
					continue
				}
			}
			if n < widgetItems[family] {
				kept = append(kept, e)
				n++
			}
		}
		if len(kept) > 0 && len(sections) < widgetSections {
			sections = append(sections, Section{Title: s.Title, Items: kept})
		}
	}
	if complication {
		return dropped, errors.New("a complication draws a metric, a progress or a line of text: the view has none")
	}
	if len(sections) == 0 {
		return dropped, errors.New("a widget draws a metric, a stat_grid, a progress, a chart, text or rows: the view has none")
	}
	v.Sections = sections
	return dropped, nil
}

// widgetLinesOf is text cut to at most lines lines and widgetText characters.
func widgetLinesOf(text string, lines int) string {
	parts := strings.Split(text, "\n")
	if len(parts) > lines {
		text = strings.Join(parts[:lines], "\n") + "…"
	}
	return clip(text, widgetText)
}
