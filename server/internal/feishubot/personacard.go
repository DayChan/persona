package feishubot

import (
	"encoding/json"
	"math"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The assistant's ```persona-card blocks (#886) are charts the apps draw (sdk/go/wire/personacard.go); a Feishu card
// cannot draw them, and their JSON is no answer, so each one is written as what a reader takes in at a glance
// (docs/extensions/bots.md "Cards"):
//
//   - stat: one line, the number in bold: Steps today: **8,432 steps** · +12% vs yesterday
//   - line, bar: the title and a sparkline (▁…█; a line from its lowest point to its highest, bars from 0), then the
//     points on one line (the last 31, … marking the cut) with the unit once; "Now" and the note under them
//   - stacked_bar: a two-column table, the total first, then each part with its note (past 31 rows the rest are
//     summed into one "Other" row); the card's note after it
//   - ring: the same table, with a "remaining" row last when the total is more than the parts' sum (as the apps draw it)
//   - a card the apps cannot draw either (View is nil): its title alone, or nothing
//
// The model's words are escaped (EscapeMarkdown), numbers are written as the apps write them (wire.Number). Only
// persona-card blocks change: persona-view blocks, other code, and examples inside another code block stay.

// mostPoints is how many points of a line or bar are written: a month of days.
const mostPoints = 31

var sparks = []rune("▁▂▃▄▅▆▇█")

// PersonaCards is md with each persona-card block written as numbers (a line or two, or a table), set apart from the
// text around it by blank lines.
func PersonaCards(md string) string {
	if !strings.Contains(strings.ToLower(md), "persona-card") {
		return md
	}
	var b strings.Builder
	at := 0
	for _, blk := range wire.AssistantViewBlocks(md) {
		if blk.Language != "persona-card" {
			continue
		}
		b.WriteString(md[at:blk.Start])
		if s := personaCardText(blk.Raw); s != "" {
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n\n") {
				b.WriteString("\n")
			}
			b.WriteString(s)
			if rest := md[blk.End:]; rest != "" && !strings.HasPrefix(rest, "\n") {
				b.WriteString("\n")
			}
		}
		at = blk.End
	}
	b.WriteString(md[at:])
	return b.String()
}

// personaCardText is one card as card Markdown ending in a line break, or "" when it has nothing to show.
func personaCardText(raw json.RawMessage) string {
	var c wire.PersonaCard
	if len(raw) > wire.AssistantViewMost || json.Unmarshal(raw, &c) != nil {
		return ""
	}
	title, subtitle, note, unit := words(c.Title), words(c.Subtitle), words(c.Note), words(c.Unit)
	if c.View() == nil {
		if title != "" {
			return title + "\n"
		}
		return ""
	}
	var b strings.Builder
	switch c.Type {
	case wire.PersonaCardKindStat:
		b.WriteString(joined(" · ", firstOf(title, "数值"), subtitle) + "：**" + amount(*c.Value, unit) + "**")
		if note != "" {
			b.WriteString(" · " + note)
		}
		b.WriteString("\n")
	case wire.PersonaCardKindStackedBar, wire.PersonaCardKindRing:
		parts := append([]wire.CardPart{}, c.Segments...)
		if c.Rest != nil {
			parts = append(parts, *c.Rest)
		}
		u, total := unit, 0.0
		for _, p := range parts {
			total += p.Value
		}
		if c.Total != nil {
			if c.Type == wire.PersonaCardKindRing && c.Total.Value > total {
				parts = append(parts, wire.CardPart{Label: i18n.T(i18n.ZhHans, "feishubot-personacard.remaining"), Value: c.Total.Value - total})
			}
			u, total = firstOf(words(c.Total.Unit), unit), c.Total.Value
		}
		if len(parts) > mostPoints {
			other := wire.CardPart{Label: "其他"}
			for _, p := range parts[mostPoints-1:] {
				other.Value += p.Value
			}
			parts = append(parts[:mostPoints-1:mostPoints-1], other)
		}
		b.WriteString("| " + joined(" · ", firstOf(title, "合计"), subtitle) + " | " + amount(total, u) + " |\n| --- | --- |\n")
		for _, p := range parts {
			b.WriteString("| " + firstOf(joined(" · ", words(p.Label), words(p.Note)), " ") + " | " + amount(p.Value, u) + " |\n")
		}
		if note != "" {
			b.WriteString("\n" + note + "\n")
		}
	case wire.PersonaCardKindLine, wire.PersonaCardKindBar:
		points, cut := c.Points, false
		if len(points) > mostPoints {
			points, cut = points[len(points)-mostPoints:], true
		}
		head := ""
		if title != "" {
			head = "**" + title + "**"
		}
		head = joined(" · ", head, subtitle)
		if len(points) > 1 {
			head = joined(" ", head, sparkline(points, c.Type == wire.PersonaCardKindBar))
		}
		if head != "" {
			b.WriteString(head + "\n")
		}
		var each []string
		if cut {
			each = append(each, "…")
		}
		for _, p := range points {
			each = append(each, joined(" ", words(p.Label), wire.Number(p.Value)))
		}
		b.WriteString(strings.Join(each, " · "))
		if unit != "" {
			b.WriteString("（" + unit + "）")
		}
		b.WriteString("\n")
		if c.Value != nil {
			b.WriteString("当前 **" + amount(*c.Value, unit) + "**\n")
		}
		if note != "" {
			b.WriteString(note + "\n")
		}
	}
	return b.String()
}

// sparkline is a mark per point, from ▁ at the lowest to █ at the highest; bars count from 0 (all at 0: ▁), a line
// that stays level is drawn in the middle.
func sparkline(points []wire.CardPart, fromZero bool) string {
	lo, hi := points[0].Value, points[0].Value
	for _, p := range points {
		lo, hi = math.Min(lo, p.Value), math.Max(hi, p.Value)
	}
	if fromZero {
		lo, hi = math.Min(lo, 0), math.Max(hi, 0)
	}
	top := len(sparks) - 1
	var b strings.Builder
	for _, p := range points {
		i := top / 2
		switch {
		case hi > lo:
			// Halved so that hi-lo cannot overflow to +Inf at the ends of float64.
			f := (p.Value/2 - lo/2) / (hi/2 - lo/2) * float64(top)
			i = max(0, min(top, int(math.Round(f))))
		case fromZero:
			i = 0
		}
		b.WriteRune(sparks[i])
	}
	return b.String()
}

// words is what the model wrote, on one line and shown as written.
func words(s string) string { return EscapeMarkdown(oneLine(s)) }

// amount is a number as the apps write it, then the unit: "2,250 kcal".
func amount(v float64, unit string) string { return joined(" ", wire.Number(v), unit) }

// joined is the non-empty ones of parts, sep between them.
func joined(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

func firstOf(s, def string) string {
	if s != "" {
		return s
	}
	return def
}
