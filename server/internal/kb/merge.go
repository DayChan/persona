package kb

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
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
// (a message the live archive already has, the same page read twice after a restart). It is dropped, against the
// file and within lines.
func (s *Store) MergeMessages(c *Chat, lines []Line) (added int, err error) {
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
	var days []string
	for _, l := range lines {
		d := l.At.In(zone).Format("2006-01-02")
		if _, ok := byDay[d]; !ok {
			days = append(days, d)
		}
		byDay[d] = append(byDay[d], l)
	}
	sort.Strings(days)
	for _, d := range days {
		p := s.path(KindChat, c.ID, "messages", d+".md")
		old, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			return added, err
		}
		if len(old) == 0 {
			old = fmt.Appendf(nil, "# %s · %s\n\n", c.Name, d)
		}
		out, n := mergeDay(string(old), byDay[d], zone)
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
		have[msg] = true
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
		if have[f] {
			continue
		}
		have[f] = true
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
