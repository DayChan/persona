package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/proto"
)

// Claude Code's transcript is a tree: every line has a uuid and a parentUuid. A conversation is mostly one chain, but
// going back to an earlier message (/rewind, editing a sent message with ↑) writes the next message as a second child
// of the line before the one it replaces. The file is only ever appended to, so the turns that were gone back over
// stay in it, and a reader that takes the lines in order shows both. Claude itself continues from the last line
// written.
//
// What is followed here is the narrowest thing that identifies this: two of the user's own messages hanging from the
// same parent, the earlier one having been answered (an assistant line grew from it). That one, and everything that
// grew from it, is an abandoned branch and is left out of the items (the later one may itself be abandoned by a
// third). An earlier message nothing answered is not one: pressing Esc on a message that has no reply yet and sending
// it again (or another) writes the same two siblings, and there is nothing that was gone back over — hiding it made the
// history token move for no reason and cut the interrupted turn from the chat (#3036); the terminal shows it, so
// does the chat. Other forks are ordinary — parallel tool results, an attachment beside a reply, a hook's line — and a
// compaction starts a new chain (parentUuid null) on purpose, so the walk back from the last line, which is what a
// resume does, would lose them: it is not used.
//
// A rewind done in the terminal writes nothing either, and the next line Claude writes need not be a message: after a
// while away it writes a recap (system away_summary) hanging from where the user went back to, and that may be all the
// file says for hours. So a system line is taken for going back too, more narrowly: it has to hang beside one of the
// user's messages that was answered and whose turn ended (a turn_duration line grew from it). A system line written
// with the parent it had before a message came in (a recap worked out while the user typed) then leaves a turn still
// going alone. Across the recorded transcripts (about 19.7k, 2026-11-06) a system line beside an answered message
// turned up once, and that was a rewind; beside a message nothing answered it turned up too (#3036's case: kept).
// The message the user sent next in that transcript hung from the recap, not from where they went back to: without
// the system line, the rewind would not have been seen even then.
//
// Clients hold items they were already sent. A branch is abandoned when the next message or system line is written (or
// when the node itself rewound, File.Rewound), so the history token (Span.History) carries how many abandoned branches the file has:
// it changes then, and a client that sees it change starts over (docs/protocol/protocol.md "Continuity of items"), as it does for a
// file that was rewritten. The token is a function of the file, so it is the same across node restarts.

// treeLine is what parseLine saw of a line's place in the tree.
type treeLine struct {
	uuid, parent string
	sidechain    bool
	assistant    bool // the line is the assistant's (a reply, a tool call): the message it grew from was answered
	system       bool // a system line (a recap, a turn's duration, a notice)
	turnEnd      bool // a turn_duration line: the turn it closes ended
	// said / saidAt: the agent's words on this line (Meta.LastText / LastTextAt as it set them); spoke says it wrote
	// text at all (said is "" for botmux's sentinel alone).
	said   string
	saidAt time.Time
	spoke  bool
}

// treeRec is a line with a uuid, as the tree keeps it: prompt says it is one of the user's own messages, reply that it
// is the assistant's, system that it is a system line and turnEnd a turn_duration one.
type treeRec struct {
	uuid, parent string
	prompt       bool
	reply        bool
	system       bool
	turnEnd      bool
}

// branchTree tracks the lines read so far (file order) and which of them lie on abandoned branches.
type branchTree struct {
	recs    []treeRec
	replies map[string]bool     // lines that are the assistant's
	ends    map[string]bool     // turn_duration lines
	kids    map[string][]string // parent → the lines hanging from it
	last    map[string]string   // parent → the latest message of the user's hanging from it
	hidden  map[string]bool     // lines on abandoned branches
	roots   map[string]bool     // where each abandoned branch starts
	manual  map[string]bool     // roots set by File.Rewound / RewoundAt: they survive a rebuild
	up      map[string]string   // line → its parent
	// marked: the lines under a manual root when it was set (the root too). Read again (a rebuild), they are the turn
	// that was gone back over, not Claude going on from it: only a line not among them takes the root back. It survives
	// a rebuild with manual.
	marked map[string]bool
	// revoked: manual roots Claude went on from (a message or a reply of its written under them after they were set,
	// not one of marked): they were not gone back over after all. The File reads itself again without them (RefreshSpan).
	revoked []string
}

func (t *branchTree) init() {
	if t.kids == nil {
		t.kids, t.last, t.hidden, t.roots = map[string][]string{}, map[string]string{}, map[string]bool{}, map[string]bool{}
		t.replies, t.ends, t.up = map[string]bool{}, map[string]bool{}, map[string]string{}
	}
}

// add takes the next line. drop says it hangs from an abandoned branch (its items are not shown); newly lists the
// lines it made abandoned (the earlier message of the user's with the same parent, and what grew from it). A message
// abandons that one when it was answered; a system line when it was answered and its turn ended.
func (t *branchTree) add(r treeRec) (drop bool, newly []string) {
	t.init()
	t.recs = append(t.recs, r)
	t.kids[r.parent] = append(t.kids[r.parent], r.uuid)
	if _, ok := t.up[r.uuid]; !ok {
		t.up[r.uuid] = r.parent
	}
	if r.reply {
		t.replies[r.uuid] = true
	}
	if r.turnEnd {
		t.ends[r.uuid] = true
	}
	if t.hidden[r.uuid] {
		return true, nil // a line repeated in the file (a resume copies lines)
	}
	if r.parent != "" && t.hidden[r.parent] {
		if root := t.manualRootOf(r.parent); root != "" && (r.prompt || r.reply) && !t.marked[r.uuid] {
			t.revoked = append(t.revoked, root)
		}
		t.hidden[r.uuid] = true
		return true, nil
	}
	if t.manual[r.uuid] { // read again (a rebuild): it stays rewound
		t.roots[r.uuid] = true
		t.hidden[r.uuid] = true
		return true, nil
	}
	if !(r.prompt || r.system) || r.parent == "" {
		return false, nil
	}
	prev := t.last[r.parent]
	if r.prompt {
		t.last[r.parent] = r.uuid
	}
	if prev == "" || prev == r.uuid || t.hidden[prev] {
		return false, nil
	}
	if answered, ended := t.answered(prev); !answered || !r.prompt && !ended {
		return false, nil
	}
	t.roots[prev] = true
	return false, t.hideFrom(prev)
}

// manualRootOf is the manual root an abandoned line lies under, "" when it lies under another.
func (t *branchTree) manualRootOf(uuid string) string {
	for u := uuid; t.hidden[u]; u = t.up[u] {
		if t.manual[u] {
			return u
		}
		if _, ok := t.up[u]; !ok {
			break
		}
	}
	return ""
}

// under says a line grew from another (or is it).
func (t *branchTree) under(uuid, root string) bool {
	seen := map[string]bool{}
	for u := uuid; u != "" && !seen[u]; u = t.up[u] {
		if u == root {
			return true
		}
		seen[u] = true
	}
	return false
}

// answered says an assistant line grew from a line (or from what grew from it): there is a turn that would be gone back
// over; ended that a turn_duration line did too. It looks at the lines read so far, which for a line already there is
// everything its branch wrote.
func (t *branchTree) answered(uuid string) (answered, ended bool) {
	seen := map[string]bool{}
	for todo := []string{uuid}; len(todo) > 0; {
		u := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[u] {
			continue
		}
		seen[u] = true
		answered, ended = answered || t.replies[u], ended || t.ends[u]
		if answered && ended {
			break
		}
		todo = append(todo, t.kids[u]...)
	}
	return answered, ended
}

// hideFrom marks a line and everything that grew from it abandoned; it returns the ones that were not yet.
func (t *branchTree) hideFrom(uuid string) []string {
	var out []string
	for todo := []string{uuid}; len(todo) > 0; {
		u := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if t.hidden[u] {
			continue
		}
		t.hidden[u] = true
		out = append(out, u)
		todo = append(todo, t.kids[u]...)
	}
	return out
}

// prepend adds lines that come before the ones read so far (a window earlier in the file) and works the whole tree
// out again, which a branch across the two parts needs. It returns the lines that are newly abandoned.
func (t *branchTree) prepend(older []treeRec) []string {
	if len(older) == 0 {
		return nil
	}
	was := t.hidden
	recs := append(append([]treeRec(nil), older...), t.recs...)
	*t = t.kept()
	for _, r := range recs {
		t.add(r)
	}
	for u := range t.manual {
		if _, ok := t.kids[u]; ok || t.hasRec(u) {
			t.roots[u] = true
			t.hideFrom(u)
		}
	}
	var newly []string
	for u := range t.hidden {
		if !was[u] {
			newly = append(newly, u)
		}
	}
	return newly
}

// kept is a new tree with what survives a rebuild: the roots the node set, and the lines that were under them then.
func (t *branchTree) kept() branchTree { return branchTree{manual: t.manual, marked: t.marked} }

// subtree is a line and everything that grew from it, abandoned or not.
func (t *branchTree) subtree(uuid string) []string {
	var out []string
	seen := map[string]bool{}
	for todo := []string{uuid}; len(todo) > 0; {
		u := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
		todo = append(todo, t.kids[u]...)
	}
	return out
}

func (t *branchTree) hasRec(uuid string) bool {
	for _, r := range t.recs {
		if r.uuid == uuid {
			return true
		}
	}
	return false
}

// uuidOf is the transcript line an item id belongs to ("<uuid>#<n>"; a line without a uuid is "line<seq>#<n>").
func uuidOf(id string) string {
	if i := strings.LastIndexByte(id, '#'); i > 0 {
		return id[:i]
	}
	return id
}

// isMessage says the items of a line are one of the user's own messages: typed or sent, not a tool result, a
// notification a background task woke the agent with, or another agent's report.
func isMessage(items []proto.Item) bool {
	for _, it := range items {
		if it.Role == "user" && it.Kind == proto.KindText && !it.Queued && !it.Wake && it.From == "" {
			return true
		}
	}
	return false
}

// withoutLines drops the items of lines in gone.
func withoutLines(items []proto.Item, gone map[string]bool) []proto.Item {
	if len(gone) == 0 || len(items) == 0 {
		return items
	}
	out := items[:0:0]
	for _, it := range items {
		if !gone[uuidOf(it.ID)] {
			out = append(out, it)
		}
	}
	return out
}

func setOf(uuids []string) map[string]bool {
	m := make(map[string]bool, len(uuids))
	for _, u := range uuids {
		m[u] = true
	}
	return m
}

// trackLine puts a line just parsed into the tree. drop says it is on an abandoned branch: its items must not be
// kept. When it abandons an earlier branch, that branch's items are taken out of *kept and *sent (either may be nil).
func trackLine(t *branchTree, meta *Meta, items []proto.Item, kept, sent *[]proto.Item) (drop bool) {
	tl := meta.tree
	meta.tree = treeLine{}
	if tl.uuid == "" || tl.sidechain || meta.Sidechain || meta.Codex {
		return false
	}
	drop, newly := t.add(tl.rec(items))
	if len(newly) > 0 {
		gone := setOf(newly)
		if kept != nil {
			*kept = withoutLines(*kept, gone)
		}
		if sent != nil {
			*sent = withoutLines(*sent, gone)
		}
	}
	return drop
}

func (tl treeLine) rec(items []proto.Item) treeRec {
	return treeRec{uuid: tl.uuid, parent: tl.parent, prompt: isMessage(items), reply: tl.assistant, system: tl.system, turnEnd: tl.turnEnd}
}

// Tail follows the tree over lines read one at a time with ParseLine outside a File (the node's quick read of a
// transcript's end), so that the agent's last words it ends up with are not ones on an abandoned branch: a turn gone back
// over that ended in an API error would otherwise stay the session's last words, and its error the session's.
type Tail struct {
	tree    branchTree
	words   []treeLine      // the lines that said something, in order
	gone    map[string]bool // Rewound's lines still left out
	rewound []string        // Rewound's lines as it was told them
	revoked bool
}

// Rewound tells the tail about lines the node itself rewound over (File.RewoundLines): Claude writes nothing for that,
// so they and what grows from them are left out too. All of them, not only where the branch starts: that may be
// before the tail.
func (t *Tail) Rewound(uuids []string) {
	if len(uuids) > 0 {
		t.gone, t.rewound = setOf(uuids), uuids
	}
}

// Revoked says some of the lines Rewound named were not gone back over after all: Claude went on from that turn. rest is
// the ones still left out (another turn the node took for rewound), which the caller keeps instead.
func (t *Tail) Revoked() (rest []string, revoked bool) {
	if !t.revoked {
		return nil, false
	}
	for _, u := range t.rewound {
		if t.gone[u] {
			rest = append(rest, u)
		}
	}
	return rest, true
}

// Line is told about the line ParseLine just read into meta, with the items it returned.
func (t *Tail) Line(meta *Meta, items []proto.Item) {
	tl := meta.tree
	meta.tree = treeLine{}
	if tl.spoke {
		t.words = append(t.words, tl)
	}
	if tl.uuid == "" || tl.sidechain || meta.Sidechain || meta.Codex {
		return
	}
	rec := tl.rec(items)
	if (rec.prompt || rec.reply) && t.gone[rec.parent] && !t.gone[rec.uuid] {
		// Claude went on from a line Rewound named: that rewind was not one (File.RewoundAt). The lines of its turn, as
		// far back as the tail goes, stop being left out (another turn's stay out), and the tree is worked out again.
		top := rec.parent
		for seen := map[string]bool{top: true}; ; {
			u, ok := t.tree.up[top]
			if !ok || !t.gone[u] || seen[u] {
				break
			}
			seen[u], top = true, u
		}
		for _, u := range t.tree.subtree(top) {
			delete(t.gone, u)
		}
		t.revoked = true
		recs := t.tree.recs
		t.tree = branchTree{}
		for _, r := range recs {
			t.take(r)
		}
	}
	t.take(rec)
}

// take puts a line into the tree, leaving it out with what grows from it when Rewound named it.
func (t *Tail) take(rec treeRec) {
	t.tree.add(rec)
	if t.gone[rec.uuid] && !t.tree.hidden[rec.uuid] {
		t.tree.roots[rec.uuid] = true
		t.tree.hideFrom(rec.uuid)
	}
}

// Skipped is told about a line the caller left unparsed for its size (a prompt with an image or a long paste, a big
// tool result). The line still takes its place in the tree: without it a turn whose prompt is one is not found gone
// back over, and one with a big tool result in it is hidden only up to there (the chain breaks at the line). What makes
// it big is not read into the copy that is parsed: an image's data, a tool result's content, the tool's own record of
// it. meta is not touched.
func (t *Tail) Skipped(line []byte) {
	var e map[string]json.RawMessage
	if json.Unmarshal(line, &e) != nil {
		return
	}
	delete(e, "toolUseResult")
	if raw, ok := e["message"]; ok {
		var msg map[string]json.RawMessage
		if json.Unmarshal(raw, &msg) != nil {
			return
		}
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(msg["content"], &blocks) == nil {
			for _, b := range blocks {
				delete(b, "source")
				var typ string
				if json.Unmarshal(b["type"], &typ) == nil && typ == "tool_result" {
					delete(b, "content")
				}
			}
			msg["content"], _ = json.Marshal(blocks)
		}
		e["message"], _ = json.Marshal(msg)
	}
	small, err := json.Marshal(e)
	if err != nil {
		return
	}
	var m Meta
	t.Line(&m, parseLine(0, small, &m))
}

// Settle puts into meta the agent's last words on the lines still in the conversation, when words on an abandoned
// branch were among them (meta is left as it is otherwise). It says whether it changed them; LastText is "" then when
// no words in the lines read are left. A turn error said on such a branch goes too: the conversation that is left did
// not fail there, and nothing older comes back (the user went back to start over).
func (t *Tail) Settle(meta *Meta) bool {
	if meta.turnErrorLine != "" && t.tree.hidden[meta.turnErrorLine] {
		meta.clearTurnError()
	}
	dropped := false
	for _, w := range t.words {
		if t.tree.hidden[w.uuid] {
			dropped = true
			break
		}
	}
	if !dropped {
		return false
	}
	meta.LastText, meta.LastTextAt = "", time.Time{}
	for _, w := range t.words {
		if t.tree.hidden[w.uuid] {
			continue
		}
		if w.said != "" {
			meta.LastText = w.said
		}
		meta.LastTextAt = w.saidAt
	}
	return true
}

// squash drops every space: the words of a message, as the user typed them.
func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
}

// Rewound is told by the node that the session was rewound in its terminal to before a message of the user's: the
// occurrence-th one with these words counting from the end. Claude writes nothing for that until the next message, so
// from now on that message and everything after it is left out here too. It says whether it found the message, and
// where the transcript stands (the token has changed: clients start over).
func (f *File) Rewound(prompt string, occurrence int) (Span, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := squash(prompt)
	if f.tree == nil || want == "" || occurrence < 1 {
		return Span{}, false
	}
	seen := 0
	for i := len(f.items) - 1; i >= 0; i-- {
		it := f.items[i]
		if !userMessage(it) || squash(it.Text) != want {
			continue
		}
		if seen++; seen < occurrence {
			continue
		}
		return f.rewoundAt(uuidOf(it.ID))
	}
	return Span{}, false
}

// RewoundAt is Rewound for the message on the line uuid: the node saw on the terminal that Claude was rewound to before
// it (node/terminalrewind.go, #5396). False when the file does not have it or it is left out already. Taken back by
// itself when Claude goes on from that message's turn after all (branchTree.add).
func (f *File) RewoundAt(uuid string) (Span, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tree == nil || uuid == "" || f.tree.hidden[uuid] {
		return Span{}, false
	}
	return f.rewoundAt(uuid)
}

func (f *File) rewoundAt(uuid string) (Span, bool) {
	if !f.tree.hasRec(uuid) {
		return Span{}, false
	}
	if f.tree.manual == nil {
		f.tree.manual, f.tree.marked = map[string]bool{}, map[string]bool{}
	}
	f.tree.manual[uuid], f.tree.roots[uuid] = true, true
	for _, u := range f.tree.subtree(uuid) {
		f.tree.marked[u] = true
	}
	f.items = withoutLines(f.items, setOf(f.tree.hideFrom(uuid)))
	return Span{f.history(), f.lines, f.lines}, true
}

// userMessage says an item is one of the user's own messages (isMessage).
func userMessage(it proto.Item) bool {
	return it.Role == "user" && it.Kind == proto.KindText && !it.Queued && !it.Wake && it.From == ""
}

// LastPrompt is the user's last message in the conversation shown, when a rewind in the terminal to before it is what
// the file would look like: it was answered and its turn ended, and every line written since grew from it (a rewind
// writes nothing). Before is the message before it, First that there is none.
type LastPrompt struct {
	UUID, Text, Before string
	First              bool
}

// LastPrompt: see the type. False when the last message is not such a one.
func (f *File) LastPrompt() (LastPrompt, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tree == nil || len(f.tree.recs) == 0 {
		return LastPrompt{}, false
	}
	// The last message, then the one before it, parsing back (a big file is read from its end) when the lines parsed
	// do not have them.
	before := func(end int) int {
		for i := end - 1; i >= 0; i-- {
			if userMessage(f.items[i]) {
				return i
			}
		}
		return -1
	}
	var p LastPrompt
	i := before(len(f.items))
	for ; i < 0; i = before(len(f.items)) {
		if f.loadOlder() == 0 {
			return LastPrompt{}, false
		}
	}
	p.UUID, p.Text = uuidOf(f.items[i].ID), f.items[i].Text
	for {
		if j := before(i); j >= 0 {
			p.Before = f.items[j].Text
			break
		}
		if f.base == 0 {
			p.First = true
			break
		}
		if f.loadOlder() == 0 {
			return LastPrompt{}, false
		}
		i = -1
		for k, it := range f.items {
			if uuidOf(it.ID) == p.UUID {
				i = k
				break
			}
		}
		if i < 0 {
			return LastPrompt{}, false
		}
	}
	if p.UUID == "" || (!p.First && p.Before == "") || f.tree.hidden[p.UUID] {
		return LastPrompt{}, false
	}
	if answered, ended := f.tree.answered(p.UUID); !answered || !ended {
		return LastPrompt{}, false
	}
	last := f.tree.recs[len(f.tree.recs)-1].uuid
	if !f.tree.under(last, p.UUID) {
		return LastPrompt{}, false
	}
	return p, true
}

// RewoundLines lists the lines on the branches Rewound left out (where each starts and everything that grew from it),
// for a reader that sees only part of the file (Tail.Rewound).
func (f *File) RewoundLines() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tree == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for u := range f.tree.manual {
		for _, v := range f.tree.subtree(u) {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

// branchToken is the part of the history token that says how many abandoned branches the file has.
func (f *File) branchToken() string {
	if f.tree == nil || len(f.tree.roots) == 0 {
		return ""
	}
	return fmt.Sprintf(".r%d", len(f.tree.roots))
}
