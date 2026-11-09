package kb

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Retention by rule (#1523, docs/assistant/knowledge.md "Retention"): a day file holds lines different rules decided
// (a mention kept ten years in a group kept a month), so each chat keeps a small ledger next to its day files,
// chats/<id>/retention.json, naming the rule of every line another rule decided than the ledger's own (the chat's usual
// rule when the ledger began). It records the rule, not the number of days, so a rule's days changed later apply to
// what is stored already. Lines without an entry, and every line of a chat without a ledger (written before it), have
// the ledger's rule, else the chat's (Chat.Rule; "" is text_days).
//
// Pruning (PruneSelected) deletes a day file when every line in it is past its days, leaves it alone when none is,
// and otherwise rewrites it without the lines that are, keeping every other row byte for byte and in its order
// (headers, blank lines, hand edits). A line's attachments go with it: an attachment's days are the most of the lines
// that link it, never more than theirs; one no line links keeps the source's attachment days, as before the ledger.
//
// The safety rule: nothing here shortens what is kept unless the user set shorter days. An off rule keeps text_days
// (turning a rule off only stops new writes), and with every days null each line is kept text_days and each
// attachment the source's attachment days, exactly as Store.PruneBySource does.

const retentionFile = "retention.json"

// retentionLedger is chats/<id>/retention.json.
type retentionLedger struct {
	Version int `json:"version"`
	// Rule: what a line without an entry has (the chat's usual rule when the ledger began; never changes after).
	Rule string `json:"rule"`
	// Lines: day ("2006-01-02") → line key (lineKey) → its deciding rule (Decision.Rule).
	Lines map[string]map[string]string `json:"lines,omitempty"`
}

// lineKey is a line's key in the ledger: a hash of its minute, sender and text as Line.Format writes them (with its
// continuation lines, without the last "\n"), its attachments folded as MergeMessages compares them (dupKey).
func lineKey(msg string) string {
	sum := sha256.Sum256([]byte(dupKey(msg)))
	return hex.EncodeToString(sum[:12])
}

func (s *Store) ledgerPath(id string) string { return s.path(KindChat, id, retentionFile) }

// readLedger reads a chat's ledger; false when there is none.
func (s *Store) readLedger(id string) (retentionLedger, bool, error) {
	var l retentionLedger
	ok, err := readJSON(s.ledgerPath(id), &l)
	if err != nil {
		return retentionLedger{}, false, fmt.Errorf("retention ledger of %s: %w", id, err)
	}
	if ok && l.Version != 1 {
		return retentionLedger{}, false, fmt.Errorf("retention ledger of %s: version %d", id, l.Version)
	}
	return l, ok, nil
}

// ruleOf is the rule of a line by its day and key.
func (l *retentionLedger) ruleOf(day, key string) string {
	if r, ok := l.Lines[day][key]; ok {
		return r
	}
	return l.Rule
}

// note records that the line with key was written on day by rule. existed: a line with the same key was in the day
// already (an identical message, or a duplicate MergeMessages dropped): the entry becomes the union of both rules, so
// neither line is kept shorter than its own rule says. It reports whether the ledger changed.
func (l *retentionLedger) note(day, key, rule string, existed bool) bool {
	cur, has := l.Lines[day][key]
	switch {
	case has:
		rule = unionRules(cur, rule)
		if rule == cur {
			return false
		}
	case existed:
		rule = unionRules(l.Rule, rule)
		if rule == l.Rule {
			return false
		}
	case rule == l.Rule:
		return false
	}
	if l.Lines == nil {
		l.Lines = map[string]map[string]string{}
	}
	if l.Lines[day] == nil {
		l.Lines[day] = map[string]string{}
	}
	l.Lines[day][key] = rule
	return true
}

// unionRules joins two deciding rules ("a+b"): LineDays keeps a union its longest days.
func unionRules(a, b string) string {
	if a == b || b == "" && a != "" {
		return a
	}
	if a == "" {
		// "" is text_days: a union with it keeps text_days at least, which RuleItem stands for (LineDays).
		return unionRules(RuleItem, b)
	}
	var names []string
	for _, r := range []string{a, b} {
		for _, n := range strings.Split(r, "+") {
			if !slices.Contains(names, n) {
				names = append(names, n)
			}
		}
	}
	sort.Strings(names)
	return strings.Join(names, "+")
}

// ledgerNotes are the lines of one write with their deciding rules, for the ledger.
type ledgerNote struct {
	day, key, rule string
	existed        bool
}

// applyLedger updates a chat's ledger with notes, starting it with usual when there is none, and returns its new
// content (nil when nothing changed). A ledger that does not read is left as it is and the write goes on: pruning
// keeps every line of such a chat the most days any rule has, so nothing is lost, and no write is held up by it.
func (s *Store) applyLedger(id, usual string, notes []ledgerNote) ([]byte, error) {
	if len(notes) == 0 {
		return nil, nil
	}
	l, ok, err := s.readLedger(id)
	if err != nil {
		return nil, nil
	}
	changed := false
	if !ok {
		l = retentionLedger{Version: 1, Rule: usual}
		changed = true
	}
	for _, n := range notes {
		changed = l.note(n.day, n.key, n.rule, n.existed) || changed
	}
	if !changed {
		return nil, nil
	}
	raw, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

// dayLine is one line of a day file as pruning sees it: a message (its row and continuation rows) or a loose row (a
// row that is no header, blank, message or continuation: a hand edit, an orphan indented row), which pruning keeps
// while the file is kept.
type dayLine struct {
	first, last int    // rows, inclusive
	key         string // lineKey ("" for a loose row)
	text        string // the rows joined
}

// dayLines splits a day file into rows and its lines (headers and blank rows are neither: they always stay).
func dayLines(text string) (rows []string, endsNL bool, lines []dayLine) {
	if text != "" {
		rows = strings.Split(text, "\n")
	}
	endsNL = strings.HasSuffix(text, "\n")
	if endsNL {
		rows = rows[:len(rows)-1]
	}
	for i := 0; i < len(rows); i++ {
		r := rows[i]
		switch {
		case r == "" || strings.HasPrefix(r, "# "):
			continue
		case clockOf(r) != "":
			j := i
			for j+1 < len(rows) && strings.HasPrefix(rows[j+1], "  ") {
				j++
			}
			msg := strings.Join(rows[i:j+1], "\n")
			lines = append(lines, dayLine{first: i, last: j, key: lineKey(msg), text: msg})
			i = j
		default:
			lines = append(lines, dayLine{first: i, last: i, text: r})
		}
	}
	return rows, endsNL, lines
}

// attachmentRef is a link to a saved attachment in a line ("[附件](attachments/…)"), relative to the chat's directory.
var attachmentRef = lazyre.New(`\]\((attachments/[^()\s]*)\)`)

// PruneSelected is Prune with each line kept its own days (Selection.LineDays by its rule in the chat's ledger) and
// each attachment the most days of the lines that link it (retention.go). With a selection whose days are all null it
// removes exactly what PruneBySource with text_days and Engine.AttachmentDaysFor removes.
func (s *Store) PruneSelected(now time.Time, sel *Selection, ret Retention) (removed int) {
	if s.readOnly {
		return 0
	}
	s.chatMu.Lock()
	defer s.chatMu.Unlock()
	s.lockArchive()
	defer s.unlockArchive()
	if err := s.recoverIngestsLocked(); err != nil {
		return 0
	}
	zone := s.zoneLocked()
	cut := func(days int) string { return now.AddDate(0, 0, -days).In(zone).Format("2006-01-02") }
	for _, id := range s.IDs(KindChat) {
		removed += s.pruneChatLocked(id, sel, ret, cut)
	}
	return removed
}

func (s *Store) pruneChatLocked(id string, sel *Selection, ret Retention, cut func(int) string) (removed int) {
	var source, sourceID, rule string
	selected := true
	if c, ok := s.Chat(id); ok {
		source, sourceID, rule = c.Source, c.SourceID, c.Rule
		// A chat of a kind the selection does not decide about (mail, an issue…) keeps what daily ingestion wrote
		// for text_days, whatever a ledger made for it says of the lines it did not note.
		selected = c.Kind == "" || (ChatFacts{Kind: c.Kind}).Selected()
	}
	minText, maxText, minAtt, maxAtt := sel.dayBounds(ret, source, sourceID)
	ledger, hasLedger, lerr := s.readLedger(id)
	if lerr != nil {
		// A ledger that does not read: every line is judged by the most days any rule has (nothing is lost).
		minText, minAtt = maxText, maxAtt
	} else if !hasLedger {
		ledger = retentionLedger{Version: 1, Rule: rule}
	}
	usual := ledger.Rule // what is written back
	if !selected {
		ledger.Rule = ""
	}
	days := func(rule string) (int, int) {
		if lerr != nil {
			return maxText, maxAtt
		}
		return sel.LineDays(ret, source, sourceID, rule)
	}
	// What each day's lines link, with the most attachment days of the lines linking it: read before the lines are
	// pruned, so a file goes with the last line that linked it, not with the days of a file no line links.
	linked := map[string]map[string]int{}
	links := func(d, text string) map[string]int {
		if m, ok := linked[d]; ok {
			return m
		}
		m := map[string]int{}
		_, _, lines := dayLines(text)
		for _, l := range lines {
			refs := attachmentRef.FindAllStringSubmatch(l.text, -1)
			if len(refs) == 0 {
				continue
			}
			_, att := days(ledger.ruleOf(d, l.key))
			for _, ref := range refs {
				rel := filepath.ToSlash(filepath.Clean(ref[1]))
				m[rel] = max(m[rel], att)
			}
		}
		linked[d] = m
		return m
	}
	ledgerChanged := false
	zone := s.zoneLocked()
	for _, d := range s.Days(id) {
		p := s.path(KindChat, id, "messages", d+".md")
		switch {
		case d >= cut(minText): // within the fewest days any line has
			continue
		case d < cut(maxText): // past the most
			if os.Remove(p) == nil {
				removed++
				if _, ok := ledger.Lines[d]; ok {
					delete(ledger.Lines, d)
					ledgerChanged = true
				}
			}
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		links(d, string(raw))
		out, left, dropped := pruneDay(string(raw), func(l dayLine) bool {
			text, _ := days(ledger.ruleOf(d, l.key))
			return d >= cut(text)
		})
		switch {
		case dropped == 0:
			continue
		case left == nil:
			if os.Remove(p) != nil {
				continue
			}
		default:
			if writeFile(p, []byte(out)) != nil {
				continue
			}
		}
		removed++
		for key := range ledger.Lines[d] {
			if !left[key] {
				delete(ledger.Lines[d], key)
				ledgerChanged = true
			}
		}
		if _, ok := ledger.Lines[d]; ok && len(ledger.Lines[d]) == 0 {
			delete(ledger.Lines, d)
			ledgerChanged = true
		}
	}
	// A ledger day whose file is gone (deleted, or a write cut short before its file) has nothing to judge.
	for d := range ledger.Lines {
		if _, err := os.Stat(s.path(KindChat, id, "messages", d+".md")); os.IsNotExist(err) {
			delete(ledger.Lines, d)
			ledgerChanged = true
		}
	}
	ledger.Rule = usual
	if hasLedger && ledgerChanged {
		if raw, err := json.MarshalIndent(ledger, "", "  "); err == nil {
			_ = writeFile(s.ledgerPath(id), append(raw, '\n'))
		}
	}
	_ = s.pruneReceipts(id, cut(minText), zone)

	entries, _ := os.ReadDir(s.path(KindChat, id, "attachments"))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		d := e.Name()
		dir := s.path(KindChat, id, "attachments", d)
		switch {
		case d >= cut(minAtt):
			continue
		case d < cut(maxAtt):
			if os.RemoveAll(dir) == nil {
				removed++
			}
			continue
		}
		// Mixed: each file the most days of the lines (of this day, which link their own day's files) linking it.
		byLine, ok := linked[d]
		if !ok {
			raw, _ := os.ReadFile(s.path(KindChat, id, "messages", d+".md"))
			byLine = links(d, string(raw))
		}
		_, att0 := days("") // a file no line links: the source's attachment days, as before the ledger
		chatDir := s.path(KindChat, id)
		all, some := true, false
		_ = filepath.WalkDir(dir, func(p string, de fs.DirEntry, werr error) error {
			if werr != nil || de.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(chatDir, p)
			n, ok := byLine[filepath.ToSlash(rel)]
			if !ok {
				n = att0
			}
			if d < cut(n) {
				some = os.Remove(p) == nil || some
			} else {
				all = false
			}
			return nil
		})
		if all {
			if os.RemoveAll(dir) == nil {
				removed++
			}
		} else if some {
			removeEmptyDirs(dir)
			removed++
		}
	}
	return removed
}

// pruneDay is a day file's text without the message lines keep refuses: every other row (headers, blank rows, kept
// lines and their continuation rows, loose rows) byte for byte and in its order. A loose row always stays: dropping
// one could make the row after it (an indented one) the continuation of the message above, changing that message.
// left: the keys of the lines kept (nil when none is: the file goes); dropped: how many lines went.
func pruneDay(text string, keep func(dayLine) bool) (out string, left map[string]bool, dropped int) {
	rows, endsNL, lines := dayLines(text)
	gone := map[int]bool{}
	for _, l := range lines {
		if l.key == "" || keep(l) {
			if left == nil {
				left = map[string]bool{}
			}
			left[l.key] = true
			continue
		}
		dropped++
		for r := l.first; r <= l.last; r++ {
			gone[r] = true
		}
	}
	if dropped == 0 {
		return text, left, 0
	}
	return joinRows(rows, endsNL, gone), left, dropped
}

// joinRows is rows without the gone ones, each other row as it was with its own line end (every row but the last
// had one; the last had one when endsNL).
func joinRows(rows []string, endsNL bool, gone map[int]bool) string {
	var b strings.Builder
	for i, r := range rows {
		if gone[i] {
			continue
		}
		b.WriteString(r)
		if i < len(rows)-1 || endsNL {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// removeEmptyDirs removes the empty directories under dir (not dir itself), deepest first.
func removeEmptyDirs(dir string) {
	var dirs []string
	_ = filepath.WalkDir(dir, func(p string, de fs.DirEntry, err error) error {
		if err == nil && de.IsDir() && p != dir {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i]) // only an empty one goes
	}
}
