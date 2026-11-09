package kb

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// MergeMessages writes lines into a chat's day files at their place in time, for messages that may be older than
// what the files hold (imported history; AppendMessages only appends, so an older message would land out of order).
// It returns how many lines it added.
//
// Each day file it touches is read whole and written back atomically (writeFile: a temporary file renamed over it),
// and the rewrite never changes what was there: every existing line stays byte for byte, in its order, including
// headers, blank lines, lines it cannot parse, lines out of time order and duplicates already present
// (TestMergeKeepsEveryLine). A new line goes in front of the first existing message line of a later minute ("- 15:04 …"
// as Line.Format writes them), after every line before it: a line it cannot read, or a message's continuation, stays
// with the line above it. A new line without a later one goes at the end. New lines going to the same place keep
// their time order, and lines of one minute the order they came in.
//
// Day files hold no message ids, so a duplicate is a line that formats the same: the same minute, sender and text
// (a message the live archive already has, the same page read twice after a restart). The attachments at the end of
// a line count the same however they were written (dupKey): the live archive links what it saved, history writes a
// placeholder naming the kind. A duplicate is dropped, against the file and within lines.
func (s *Store) MergeMessages(c *Chat, lines []Line) (added int, err error) {
	ruled := make([]ingestLine, len(lines))
	for i, l := range lines {
		ruled[i] = ingestLine{Line: l}
	}
	return s.mergeMessages(c, ruled, false, "")
}

// mergeMessages is MergeMessages; selected: the lines went through the selection, and the chat's retention ledger
// (begun with usual) records those another rule decided, written before the day files. A line dropped as a
// duplicate still notes its rule: the line there is kept as long as the longer of both.
func (s *Store) mergeMessages(c *Chat, ruled []ingestLine, selected bool, usual string) (added int, err error) {
	if s.readOnly {
		return 0, ErrReadOnly
	}
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		if added > 0 {
			s.notify(KindChat, c.ID, "messages")
		}
	}()
	if _, err := s.settleChatLocked(c.ID); err != nil {
		return 0, err
	}
	zone := s.zoneLocked()
	byDay := map[string][]Line{}
	rules := map[string][]string{}
	var days []string
	for _, l := range ruled {
		d := l.At.In(zone).Format("2006-01-02")
		if _, ok := byDay[d]; !ok {
			days = append(days, d)
		}
		byDay[d] = append(byDay[d], l.Line)
		rules[d] = append(rules[d], l.rule)
	}
	sort.Strings(days)
	olds := map[string][]byte{}
	var notes []ledgerNote
	for _, d := range days {
		p := s.path(KindChat, c.ID, "messages", d+".md")
		old, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			return added, err
		}
		if len(old) == 0 {
			old = fmt.Appendf(nil, "# %s · %s\n\n", c.Name, d)
		}
		olds[d] = old
		if !selected {
			continue
		}
		have := map[string]bool{}
		_, _, lines := dayLines(string(old))
		for _, l := range lines {
			have[l.key] = true
		}
		for i, l := range byDay[d] {
			key := lineKey(strings.TrimSuffix(l.Format(zone), "\n"))
			notes = append(notes, ledgerNote{day: d, key: key, rule: rules[d][i], existed: have[key]})
			have[key] = true
		}
	}
	if selected {
		raw, err := s.applyLedger(c.ID, usual, notes)
		if err != nil {
			return 0, err
		}
		if raw != nil {
			if err := writeFile(s.ledgerPath(c.ID), raw); err != nil {
				return 0, err
			}
		}
	}
	for _, d := range days {
		p := s.path(KindChat, c.ID, "messages", d+".md")
		out, n := mergeDay(string(olds[d]), byDay[d], zone)
		if n == 0 {
			continue
		}
		if err := writeFile(p, []byte(out)); err != nil {
			return added, err
		}
		added += n
	}
	return added, nil
}

// mergeDay is MergeMessages for one day file: its text and the lines of that day. It returns the new text and how
// many lines went in (0: the text is unchanged).
func mergeDay(text string, lines []Line, zone *time.Location) (string, int) {
	return mergeDayLines(text, lines, zone, true)
}

// mergeDayLines is mergeDay; without dedupe a line that formats like one there goes in too (Ingest: distinct ids).
func mergeDayLines(text string, lines []Line, zone *time.Location, dedupe bool) (string, int) {
	var rows []string
	if text != "" {
		rows = strings.Split(text, "\n")
	}
	endsNL := strings.HasSuffix(text, "\n")
	if endsNL {
		rows = rows[:len(rows)-1] // the empty string after the last "\n"
	}
	// What the file holds already: every message with its continuation lines, as Line.Format would write it.
	have := map[string]bool{}
	for i := 0; i < len(rows); i++ {
		if !strings.HasPrefix(rows[i], "- ") {
			continue
		}
		msg := rows[i]
		for j := i + 1; j < len(rows) && strings.HasPrefix(rows[j], "  "); j++ {
			msg += "\n" + rows[j]
		}
		have[dupKey(msg)] = true
	}
	// The minute of every message line that has one ("" for the others): where new lines may go.
	clock := make([]string, len(rows))
	for i, r := range rows {
		clock[i] = clockOf(r)
	}

	type insert struct {
		at   int    // the index of the row it goes in front of (len(rows): the end)
		hhmm string // its minute
		text string // as Line.Format writes it, without the last "\n"
	}
	var ins []insert
	for _, l := range lines {
		f := strings.TrimSuffix(l.Format(zone), "\n")
		key := dupKey(f)
		if have[key] && dedupe {
			continue
		}
		have[key] = true
		hhmm := l.At.In(zone).Format("15:04")
		at := len(rows)
		for i, c := range clock {
			if c != "" && c > hhmm {
				at = i
				break
			}
		}
		ins = append(ins, insert{at: at, hhmm: hhmm, text: f})
	}
	if len(ins) == 0 {
		return text, 0
	}
	sort.SliceStable(ins, func(i, j int) bool {
		if ins[i].at != ins[j].at {
			return ins[i].at < ins[j].at
		}
		return ins[i].hhmm < ins[j].hhmm
	})
	var b strings.Builder
	k := 0
	for i, r := range rows {
		for ; k < len(ins) && ins[k].at == i; k++ {
			b.WriteString(ins[k].text)
			b.WriteByte('\n')
		}
		b.WriteString(r)
		if i < len(rows)-1 || endsNL || k < len(ins) {
			b.WriteByte('\n')
		}
	}
	for ; k < len(ins); k++ {
		b.WriteString(ins[k].text)
		b.WriteByte('\n')
	}
	return b.String(), len(ins)
}

// clockOf is the minute ("15:04") of a message line as Line.Format writes it, "" for any other line.
func clockOf(row string) string {
	if !strings.HasPrefix(row, "- ") || len(row) < 8 || row[7] != ' ' {
		return ""
	}
	hhmm := row[2:7]
	if _, err := time.Parse("15:04", hhmm); err != nil {
		return ""
	}
	return hhmm
}

// trailingMedia is one attachment at the end of a message as the archive writes it: "[附件]" with or without the link
// to the saved file, or a placeholder naming the kind of one imported history did not download.
var trailingMedia = lazyre.New(`\s*\[(?:附件|文件|视频|语音)\](?:\([^()\s]*\))?\s*$`) // i18n-ignore: archive data

// dupKey is what two messages (as Line.Format writes them, without the last "\n") are compared by: the text with its
// trailing attachments, however many and however written, folded into one mark. The live archive writes a file as
// "[附件](attachments/…)", or one "[附件]" for all of an old message's attachments; history writes "[文件]" per file
// and "[附件]" for an image it did not save. The same message must match either way, or every attachment at the
// boundary with the live archive would be written twice.
func dupKey(msg string) string {
	media := false
	for {
		loc := trailingMedia.FindStringIndex(msg)
		if loc == nil {
			break
		}
		msg, media = msg[:loc[0]], true
	}
	msg = strings.TrimRight(msg, " ")
	if media {
		msg += " \x00media"
	}
	return msg
}
