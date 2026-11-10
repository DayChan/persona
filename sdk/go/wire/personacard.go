package wire

import (
	"math"
	"strconv"
	"strings"
)

// The assistant's ```persona-card (#886) is drawn with the extensions' components (#1850): every app turns a card into
// a View by these rules and draws it with the renderer it draws extensions with. This is the reference; the cases in
// schema/extension/examples/persona-card/ (a card and the view it becomes) hold each app's own to the same.
//
//   - A card the apps cannot draw is nil, and stays the code block it is: a stat without value; a stacked_bar without
//     parts (segments and rest), with a part below 0 or parts adding up to 0; a line with fewer than two points; a bar
//     without points; a ring with fewer than two parts (after the remainder), with a part below 0 or parts adding up to 0.
//   - One section. A card's title is the section's title (a stat's is its metric's label instead); a subtitle is a
//     secondary text first.
//   - Numbers are written grouped, with at most one decimal (2,250 · 7.5), the unit after a space.
//   - stat: metric {label: title or the "value" label, value, detail: note}.
//   - stacked_bar: metric {label: the "total" label, value: total (else the parts' sum), detail: note}; a stacked bar chart with a
//     series per part (more than four: the first three and "other", the rest's sum; the rest part's tone neutral), each one
//     point labelled title or "total"; then a row per part {title: label, subtitle: note, value}. The unit is total's,
//     else the card's.
//   - ring (one day's total and its parts): a donut chart, one series of a point per part (more than eight: the first
//     seven and "other", the rest's sum), center: the total written with its unit (left out past 20 characters), detail:
//     note; then a row per part. When total is more than the parts' sum, a "remaining" part (total less the sum) comes
//     last. The unit is total's, else the card's. A stacked_bar is for comparing (days, people), a ring for one whole.
//   - line, bar: metric {label: the "current" label, value} when the card has a value; a chart of that kind with one series of the
//     points (the last 120), detail: note.

// View is the card as the apps draw it, or nil when they cannot (it stays a code block).
func (c PersonaCard) View() *View {
	parts := append(append([]CardPart{}, c.Segments...), c.rest()...)
	sum := 0.0
	for _, p := range parts {
		sum += p.Value
	}
	switch c.Type {
	case PersonaCardKindStat:
		if c.Value == nil {
			return nil
		}
	case PersonaCardKindStackedBar:
		if len(parts) == 0 || sum <= 0 {
			return nil
		}
		for _, p := range parts {
			if p.Value < 0 {
				return nil
			}
		}
	case PersonaCardKindRing:
		if c.Total != nil && c.Total.Value > sum {
			parts = append(parts, CardPart{Label: "剩余", Value: c.Total.Value - sum})
			sum = c.Total.Value
		}
		if len(parts) < 2 || sum <= 0 {
			return nil
		}
		for _, p := range parts {
			if p.Value < 0 {
				return nil
			}
		}
	case PersonaCardKindLine:
		if len(c.Points) < 2 {
			return nil
		}
	case PersonaCardKindBar:
		if len(c.Points) == 0 {
			return nil
		}
	default:
		return nil
	}
	s := Section{Title: c.Title, Items: []Component{}}
	if c.Subtitle != "" {
		s.Items = append(s.Items, Component{Type: ComponentText, Text: c.Subtitle, Style: TextStyleSecondary})
	}
	switch c.Type {
	case PersonaCardKindStat:
		s.Title = ""
		s.Items = append(s.Items, Component{Type: ComponentMetric, Label: or(c.Title, "数值"), Value: amount(*c.Value, c.Unit), Detail: c.Note})
	case PersonaCardKindStackedBar:
		unit, total := c.Unit, sum
		if c.Total != nil {
			unit, total = or(c.Total.Unit, c.Unit), c.Total.Value
		}
		s.Items = append(s.Items, Component{Type: ComponentMetric, Label: "合计", Value: amount(total, unit), Detail: c.Note})
		label := or(c.Title, "合计")
		series := []ChartSeries{}
		shown := parts
		if len(parts) > 4 {
			shown = parts[:3]
		}
		for i, p := range shown {
			cs := ChartSeries{Name: p.Label, Points: []ChartPoint{{Label: label, Value: p.Value, Display: amount(p.Value, unit)}}}
			if c.Rest != nil && i == len(parts)-1 {
				cs.Tone = ToneNeutral
			}
			series = append(series, cs)
		}
		if len(parts) > 4 {
			other := 0.0
			for _, p := range parts[3:] {
				other += p.Value
			}
			series = append(series, ChartSeries{Name: "其他", Tone: ToneNeutral, Points: []ChartPoint{{Label: label, Value: other, Display: amount(other, unit)}}})
		}
		s.Items = append(s.Items, Component{Type: ComponentChart, Kind: ChartKindBar, Stacked: true, Unit: unit, Series: series})
		for _, p := range parts {
			s.Items = append(s.Items, Component{Type: ComponentRow, Title: p.Label, Subtitle: p.Note, Value: amount(p.Value, unit)})
		}
	case PersonaCardKindRing:
		unit, total := c.Unit, sum
		if c.Total != nil {
			unit, total = or(c.Total.Unit, c.Unit), c.Total.Value
		}
		shown := parts
		if len(parts) > 8 {
			other := 0.0
			for _, p := range parts[7:] {
				other += p.Value
			}
			shown = append(append([]CardPart{}, parts[:7]...), CardPart{Label: "其他", Value: other})
		}
		points := make([]ChartPoint, 0, len(shown))
		for _, p := range shown {
			points = append(points, ChartPoint{Label: p.Label, Value: p.Value, Display: amount(p.Value, unit)})
		}
		center := amount(total, unit)
		if len([]rune(center)) > 20 {
			center = ""
		}
		s.Items = append(s.Items, Component{Type: ComponentChart, Kind: ChartKindDonut, Unit: unit, Detail: c.Note, Center: center, Series: []ChartSeries{{Points: points}}})
		for _, p := range parts {
			s.Items = append(s.Items, Component{Type: ComponentRow, Title: p.Label, Subtitle: p.Note, Value: amount(p.Value, unit)})
		}
	case PersonaCardKindLine, PersonaCardKindBar:
		if c.Value != nil {
			s.Items = append(s.Items, Component{Type: ComponentMetric, Label: "当前", Value: amount(*c.Value, c.Unit)})
		}
		pts := c.Points
		if len(pts) > 120 {
			pts = pts[len(pts)-120:]
		}
		points := make([]ChartPoint, 0, len(pts))
		for _, p := range pts {
			points = append(points, ChartPoint{Label: p.Label, Value: p.Value, Display: amount(p.Value, c.Unit)})
		}
		kind := ChartKindLine
		if c.Type == PersonaCardKindBar {
			kind = ChartKindBar
		}
		s.Items = append(s.Items, Component{Type: ComponentChart, Kind: kind, Unit: c.Unit, Detail: c.Note, Series: []ChartSeries{{Points: points}}})
	}
	return &View{Sections: []Section{s}}
}

func (c PersonaCard) rest() []CardPart {
	if c.Rest == nil {
		return nil
	}
	return []CardPart{*c.Rest}
}

func or(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// amount is v written grouped with at most one decimal, then the unit: "2,250 kcal", "7.5 h", "8,432".
func amount(v float64, unit string) string {
	s := Number(v)
	if unit != "" {
		s += " " + unit
	}
	return s
}

// Number writes v grouped by thousands with at most one decimal: 2,250 · 7.5 · -1,200.3 · 0.
func Number(v float64) string {
	r := math.Round(v*10) / 10
	if r == 0 {
		r = 0 // not -0
	}
	neg := r < 0
	if neg {
		r = -r
	}
	whole := math.Floor(r)
	tenth := int(math.Round((r - whole) * 10))
	if tenth == 10 {
		whole, tenth = whole+1, 0
	}
	digits := strconv.FormatFloat(whole, 'f', 0, 64)
	var b strings.Builder
	if neg && (whole > 0 || tenth > 0) {
		b.WriteByte('-')
	}
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(d)
	}
	if tenth > 0 {
		b.WriteByte('.')
		b.WriteString(strconv.Itoa(tenth))
	}
	return b.String()
}
