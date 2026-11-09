package transcript

import (
	"bufio"
	"bytes"
	"hash/crc32"
	"io"
	"os"
	"sort"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Opening a big transcript cold (a node that just started, a session not opened for a while) used to parse all of it
// to answer "the last two exchanges": 22 s for 317 MB on a busy machine, past the hub's timeout. Now a cold open of a
// file over 2×windowBytes counts its lines (fast: no JSON) and parses only a window at the end. Pages, after_seq and
// full texts that reach before the window parse the window before it, and so on back to the first line.
// Session-level Meta (turns, tokens, compactions) needs the whole file: it is worked out when first asked for
// (Meta), outside the file's lock.
//
// The items are exactly the ones a full read gives: what a line turns into depends on earlier lines only through
// the state below, and a window starts with it as it would be — the background-task notifications already
// shown (a repeat is dropped) and the calls they name, the reports subagents handed back before their
// notification, and the last "! cmd" input (its output line becomes the call) are read from the earlier lines that
// mention them, as is ultracode's pending command (its delayed attachment repeats command output).
// Those lines are found by a byte search while counting lines, so a Claude window may start at any line; a
// Codex rollout records an item twice only within its turn, so its windows start at a turn (task_started).

// windowBytes is how much of a big file's end a cold open parses (and how much more each step back parses).
var windowBytes int64 = 8 << 20

// window is what a File keeps while lines at its start are not parsed: where each of those lines starts (and where
// the first parsed one does), and which of them carry state for later lines.
type window struct {
	starts    []int64 // starts[i]: offset of line i+1, for the lines before the first window and its first line
	notif     []notifSeed
	bash      []int // lines mentioning a "! cmd" (input or output)
	ultracode []int // lines mentioning ultracode attachments or local command output
	codex     bool
	start     int             // the first window's first line: the File's meta parses from there
	ids       map[string]bool // tasks whose earlier notifications the File's meta has seen
}

// notifSeed is a line mentioning a background-task notification, or a subagent handing its report back, and the
// task's id ("" when it could not be read).
type notifSeed struct {
	line int
	id   string
}

var (
	seedNotification = []byte("<task-notification>")
	seedBash         = []byte("<bash-")
	seedUltracode    = []byte("ultra_effort_")
	seedStdout       = []byte("local-command-stdout")
	seedHandback     = []byte(`"senderTaskId":"`) // a subagent's message (a hand-back among them): its id follows
)

// scanLines reads a file once without parsing it: where each complete line starts, which lines mention a seed marker,
// the first line, and where the last complete line ends (what follows is a line still being written). Big blocks and
// byte searches: a few hundred MB in a fraction of a second.
func scanLines(fh *os.File, size int64) (starts []int64, notif []notifSeed, bash, ultracode []int, first []byte, lastNL int64, err error) {
	const block = 4 << 20
	keep := int64(max(len(seedNotification), len(seedStdout), len(seedHandback)) - 1) // a marker split across two blocks
	buf := make([]byte, block+keep)
	type hit struct {
		off       int64
		id        string // a notification's task id; bash: "!"
		bash      bool
		ultracode bool
	}
	var hits []hit
	starts = append(starts, 0)
	firstDone := false
	for off := int64(0); off < size; off += block {
		n := min(block+keep, size-off)
		if _, err := fh.ReadAt(buf[:n], off); err != nil && err != io.EOF {
			return nil, nil, nil, nil, nil, 0, err
		}
		data := buf[:n]
		own := min(int64(block), n) // bytes this block owns; the rest is the next block's start, read for markers
		for _, m := range [][]byte{seedNotification, seedBash, seedUltracode, seedStdout, seedHandback} {
			for i := 0; ; {
				j := bytes.Index(data[i:], m)
				if j < 0 || int64(i+j) >= own {
					break
				}
				h := hit{off: off + int64(i+j), bash: bytes.Equal(m, seedBash), ultracode: bytes.Equal(m, seedUltracode) || bytes.Equal(m, seedStdout)}
				if bytes.Equal(m, seedHandback) {
					near := data[i+j+len(m) : min(len(data), i+j+len(m)+200)]
					if z := bytes.IndexByte(near, '"'); z > 0 {
						h.id = string(near[:z])
					}
				} else if !h.bash && !h.ultracode { // the task id follows the marker: "<task-notification>\n<task-id>…</task-id>"
					near := data[i+j : min(len(data), i+j+300)]
					if a := bytes.Index(near, []byte("<task-id>")); a >= 0 {
						if z := bytes.Index(near[a:], []byte("</task-id>")); z > 0 {
							h.id = string(near[a+len("<task-id>") : a+z])
						}
					}
				}
				hits = append(hits, h)
				i += j + 1
			}
		}
		if !firstDone {
			if j := bytes.IndexByte(data[:own], '\n'); j >= 0 {
				first, firstDone = append(first, data[:j]...), true
			} else {
				first = append(first, data[:own]...)
			}
		}
		for i := int64(0); i < own; {
			j := bytes.IndexByte(data[i:own], '\n')
			if j < 0 {
				break
			}
			i += int64(j) + 1
			lastNL = off + i
			starts = append(starts, lastNL)
		}
	}
	starts = starts[:len(starts)-1] // the last entry is where the line still being written (or nothing) starts
	sort.Slice(hits, func(i, j int) bool { return hits[i].off < hits[j].off })
	for _, h := range hits {
		if h.off >= lastNL {
			break
		}
		line := sort.Search(len(starts), func(i int) bool { return starts[i] > h.off }) // 1-based line holding h
		if h.ultracode {
			if len(ultracode) == 0 || ultracode[len(ultracode)-1] != line {
				ultracode = append(ultracode, line)
			}
		} else if h.bash {
			if len(bash) == 0 || bash[len(bash)-1] != line {
				bash = append(bash, line)
			}
		} else {
			notif = append(notif, notifSeed{line, h.id})
		}
	}
	return starts, notif, bash, ultracode, first, lastNL, nil
}

// openWindow reads a big file cold: counts its lines, parses the window at its end. ok is false when no turn start
// is close enough to the end to make a window worth it (the caller reads the file whole). Callers hold f.mu.
func (f *File) openWindow(fh *os.File, size int64) (fresh []proto.Item, ok bool, err error) {
	starts, notif, bash, ultracode, first, lastNL, err := scanLines(fh, size)
	if err != nil {
		return nil, false, err
	}
	n := len(starts)
	if n == 0 {
		return nil, false, nil
	}
	w := &window{starts: starts, notif: notif, bash: bash, ultracode: ultracode, codex: isCodexLine(first)}
	// The window starts at the line windowBytes before the end (Codex: the turn starting there or before).
	from := sort.Search(n, func(i int) bool { return starts[i] >= size-windowBytes })
	b := max(from, 1)
	if w.codex {
		b = f.findTurn(fh, w, b, lastNL, size-4*windowBytes)
	}
	if b <= 1 {
		return nil, false, nil
	}
	data := make([]byte, lastNL-starts[b-1])
	if _, err := fh.ReadAt(data, starts[b-1]); err != nil {
		return nil, false, err
	}
	w.start, w.starts = b, starts[:b]
	meta := f.seeded(fh, w, b, lastNL, data)
	w.ids = map[string]bool{}
	for _, m := range taskIDRe.FindAllSubmatch(data, -1) {
		w.ids[string(m[1])] = true
	}
	var kept []proto.Item
	f.cut = nil
	f.tree = f.freshTree()
	f.parseLines(data, starts[b-1], b, &meta, f.tree, &kept, &fresh)
	tail := make([]byte, size-lastNL)
	if _, err := fh.ReadAt(tail, lastNL); err != nil && err != io.EOF {
		return nil, false, err
	}
	f.meta, f.items, f.win = meta, kept, w
	f.lines, f.base, f.offset, f.partial = n, b-1, size, tail
	f.firstHash = crc32.ChecksumIEEE(first)
	f.partialMeta = true
	return fresh, true, nil
}

// seeNotifications brings the File's meta up to date before it parses new lines (data): notifications of tasks
// it has not seen yet, from lines before the first window, so a repeat arriving now is dropped as a full read
// drops it. Callers hold f.mu.
func (f *File) seeNotifications(data []byte) {
	w := f.win
	if w == nil || !f.partialMeta || !bytes.Contains(data, []byte("<task-id>")) {
		return
	}
	fresh := map[string]bool{}
	for _, m := range taskIDRe.FindAllSubmatch(data, -1) {
		if id := string(m[1]); !w.ids[id] {
			fresh[id], w.ids[id] = true, true
		}
	}
	if len(fresh) == 0 {
		return
	}
	fh, err := os.Open(f.Path)
	if err != nil {
		return
	}
	defer fh.Close()
	scratch := Meta{Sidechain: f.meta.Sidechain, Codex: w.codex, notified: f.meta.notified, taskCalls: f.meta.taskCalls,
		handbacks: f.meta.handbacks}
	last := 0
	for _, s := range w.notif {
		if s.line >= w.start {
			break
		}
		if s.line == last || (s.id != "" && !fresh[s.id]) {
			continue
		}
		last = s.line
		line := make([]byte, w.lineStart(s.line+1, 0)-w.starts[s.line-1])
		if _, err := fh.ReadAt(line, w.starts[s.line-1]); err == nil || err == io.EOF {
			ParseLine(s.line, bytes.TrimRight(line, "\n"), &scratch)
		}
	}
	f.meta.notified, f.meta.taskCalls, f.meta.handbacks = scratch.notified, scratch.taskCalls, scratch.handbacks
}

// lineStart is where line n (1-based) starts; one past the last line known gives end.
func (w *window) lineStart(n int, end int64) int64 {
	if n-1 < len(w.starts) {
		return w.starts[n-1]
	}
	return end
}

// findTurn finds the last line at or before line `at` that starts a turn, not looking at lines starting before
// floor; 0 when there is none. Lines are read back from `at` a block at a time; end is where line `at` ends at the
// latest (the start of the line after it, or the end of the last line).
func (f *File) findTurn(fh *os.File, w *window, at int, end, floor int64) int {
	hi := at
	for hi >= 1 {
		lo := hi
		for lo > 1 && w.starts[hi-1]-w.starts[lo-2] < windowBytes {
			lo--
		}
		blockEnd := end
		if hi < len(w.starts) {
			blockEnd = w.starts[hi]
		}
		data := make([]byte, blockEnd-w.starts[lo-1])
		if _, err := fh.ReadAt(data, w.starts[lo-1]); err != nil && err != io.EOF {
			return 0
		}
		for n := hi; n >= lo; n-- {
			if w.starts[n-1] < floor {
				return 0
			}
			a := w.starts[n-1] - w.starts[lo-1]
			z := int64(len(data))
			if n < hi {
				z = w.starts[n] - w.starts[lo-1]
			}
			line := bytes.TrimRight(data[a:z], "\n")
			if f.startsTurn(n, line, w.codex) {
				return n
			}
		}
		hi = lo - 1
	}
	return 0
}

// startsTurn says whether a Codex rollout's line starts a task (a turn).
func (f *File) startsTurn(_ int, line []byte, _ bool) bool {
	return bytes.Contains(line, []byte(`"type":"task_started"`))
}

// seeded is the parser state for a window starting at line b whose bytes are window: what the earlier lines leave
// behind that changes what later lines turn into. Notifications already shown (a repeat is dropped), the calls
// they name and the reports handed back: only the earlier lines for a task the window mentions are read. The pending "! cmd": the last earlier line that is a "! cmd"
// input or output decides it.
func (f *File) seeded(fh *os.File, w *window, b int, end int64, window []byte) Meta {
	readLine := func(n int) []byte {
		line := make([]byte, w.lineStart(n+1, end)-w.starts[n-1])
		if _, err := fh.ReadAt(line, w.starts[n-1]); err != nil && err != io.EOF {
			return nil
		}
		return bytes.TrimRight(line, "\n")
	}
	ids := map[string]bool{}
	for _, m := range taskIDRe.FindAllSubmatch(window, -1) {
		ids[string(m[1])] = true
	}
	scratch := Meta{Sidechain: f.meta.Sidechain, Codex: w.codex}
	last := 0
	for _, s := range w.notif {
		if s.line >= b {
			break
		}
		if s.line == last || (s.id != "" && !ids[s.id]) {
			continue
		}
		last = s.line
		if line := readLine(s.line); line != nil {
			ParseLine(s.line, line, &scratch)
		}
	}
	const unset = "\x00"
	bash := ""
	for i := len(w.bash) - 1; i >= 0; i-- {
		if w.bash[i] >= b {
			continue
		}
		probe := Meta{Sidechain: f.meta.Sidechain, Codex: w.codex, bashInput: unset}
		if line := readLine(w.bash[i]); line != nil {
			ParseLine(w.bash[i], line, &probe)
		}
		if probe.bashInput != unset {
			bash = probe.bashInput
			break
		}
	}
	ultra := Meta{Sidechain: f.meta.Sidechain, Codex: w.codex, UltracodeSince: f.meta.UltracodeSince}
	for _, n := range w.ultracode {
		if n >= b {
			break
		}
		// Request blobs repeat command output and reminders from the entire conversation. Like node's bgScan,
		// skip huge lines before allocating or decoding them, preserving the bounded cold-open window.
		if w.lineStart(n+1, end)-w.starts[n-1] > 64<<10 {
			continue
		}
		if line := readLine(n); line != nil {
			ParseLine(n, line, &ultra)
		}
	}
	return Meta{Sidechain: f.meta.Sidechain, Codex: w.codex, notified: scratch.notified, taskCalls: scratch.taskCalls,
		handbacks: scratch.handbacks, bashInput: bash,
		Ultracode: ultra.Ultracode, UltracodeAt: ultra.UltracodeAt, UltracodeSince: ultra.UltracodeSince, ultracodeCommand: ultra.ultracodeCommand}
}

var taskIDRe = lazyre.New(`<task-id>([^<]{1,200})</task-id>`)

// parseLines parses complete lines in data (starting at file offset pos, line number n) into meta: the stored (light)
// items go to kept, the ones to send (light, images kept) to sent when it is not nil. Callers hold f.mu.
func (f *File) parseLines(data []byte, pos int64, n int, meta *Meta, tree *branchTree, kept, sent *[]proto.Item) {
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return
		}
		line, at := data[:i], pos
		data, pos = data[i+1:], pos+int64(i)+1
		if len(bytes.TrimSpace(line)) > 0 {
			items := ParseLine(n, line, meta)
			if trackLine(tree, meta, items, kept, sent) { // on a branch that was gone back over (branch.go)
				n++
				continue
			}
			for _, it := range items {
				if sent != nil {
					s, _ := light(it, true)
					*sent = append(*sent, s)
				}
				k, cut := light(it, false)
				*kept = append(*kept, k)
				if cut {
					if f.cut == nil {
						f.cut = map[int]lineRef{}
					}
					f.cut[n] = lineRef{at, i}
				}
			}
		}
		n++
	}
}

// loadOlder parses the window before the parsed lines; it says how many bytes that was (0: nothing left, or the
// file changed). Callers hold f.mu.
func (f *File) loadOlder() int64 {
	if f.base == 0 || f.win == nil {
		return 0
	}
	fh, err := os.Open(f.Path)
	if err != nil {
		return 0
	}
	defer fh.Close()
	if st, err := fh.Stat(); err != nil || (f.ino != 0 && inode(st) != f.ino) {
		return 0 // replaced: the next Refresh starts over
	}
	w := f.win
	end := w.starts[f.base] // where the parsed lines start
	from := sort.Search(f.base, func(i int) bool { return w.starts[i] >= end-windowBytes })
	b := max(min(from+1, f.base), 1)
	if w.codex {
		if b = f.findTurn(fh, w, b, end, 0); b == 0 {
			b = 1 // the first line starts everything
		}
	}
	data := make([]byte, end-w.starts[b-1])
	if _, err := fh.ReadAt(data, w.starts[b-1]); err != nil && err != io.EOF {
		return 0
	}
	meta := f.seeded(fh, w, b, end, data)
	var older []proto.Item
	pre := &branchTree{}
	f.parseLines(data, w.starts[b-1], b, &meta, pre, &older, nil)
	// A branch can start in the lines before and be abandoned in the lines after: the tree is worked out again over
	// both, and what it now holds abandoned goes out of the items already there and the ones just read. A client that
	// had them sees the history token change.
	if f.tree == nil {
		f.tree = &branchTree{}
	}
	if len(f.tree.prepend(pre.recs)) > 0 || len(pre.hidden) > 0 {
		older = withoutLines(older, f.tree.hidden)
		f.items = withoutLines(f.items, f.tree.hidden)
	}
	f.items = append(older, f.items...)
	f.base = b - 1
	if f.base == 0 && !f.partialMeta {
		f.win = nil
	}
	return int64(len(data))
}

// pageBudget is how much one page may parse back: a page of a long turn (a subagent's transcript is one prompt and
// hundreds of MB of work) comes back shorter, its earlier part on the next page, instead of parsing the whole file.
func pageBudget() int64 { return 3 * windowBytes }

// ensureItems parses back until more than n items come before beforeSeq (0: the end) — one more than a page of n
// needs, so the page knows whether anything is older — or the first line is parsed, or the page's budget is spent.
// Callers hold f.mu.
func (f *File) ensureItems(beforeSeq, n int) {
	for spent := int64(0); f.base > 0 && f.before(beforeSeq) <= n && (spent < pageBudget() || f.before(beforeSeq) == 0); {
		got := f.loadOlder()
		if got == 0 {
			return
		}
		spent += got
	}
}

// ensureTurns parses back until the page PageTurns takes (`turns` exchanges or maxItems items before beforeSeq) has
// an item before it, or the first line is parsed, or the page's budget is spent and it has an item. Callers hold f.mu.
func (f *File) ensureTurns(beforeSeq, turns, maxItems int) {
	for spent := int64(0); f.base > 0; {
		end := f.before(beforeSeq)
		// Past the budget the page comes back short — but never empty while there is more: an empty page has no seq
		// to ask further back from.
		if spent >= pageBudget() && end > 0 {
			return
		}
		start, seen := end, 0
		for start > 0 && seen < turns && end-start < maxItems {
			start--
			if pagePrompt(f.items[start]) {
				seen++
			}
		}
		if start > 0 {
			return
		}
		got := f.loadOlder()
		if got == 0 {
			return
		}
		spent += got
	}
}

// ensureAfter parses back until everything after afterSeq is parsed. Callers hold f.mu.
func (f *File) ensureAfter(afterSeq int) {
	for f.base > afterSeq && f.loadOlder() > 0 {
	}
}

// fullMeta works out the session-level Meta of a file opened by its end: every line, in order, as a full read
// would. It parses outside the lock (it takes as long as a full read), then catches up with lines read meanwhile.
func (f *File) fullMeta() Meta {
	f.mu.Lock()
	if !f.partialMeta {
		m := f.meta
		f.mu.Unlock()
		return m
	}
	end, lines, gen, path := f.offset-int64(len(f.partial)), f.lines, f.gen, f.Path
	m := Meta{Sidechain: f.meta.Sidechain}
	f.mu.Unlock()
	if parseRange(path, 0, end, 1, &m) != nil {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.meta
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gen != gen || !f.partialMeta {
		return f.meta
	}
	if f.lines > lines {
		if parseRange(f.Path, end, f.offset-int64(len(f.partial)), lines+1, &m) != nil {
			return f.meta
		}
	}
	f.meta, f.partialMeta = m, false
	if f.base == 0 {
		f.win = nil
	}
	return m
}

// parseRange runs the lines in [from, to) of a file (the first numbered n) through ParseLine for their Meta.
func parseRange(path string, from, to int64, n int, m *Meta) error {
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	r := bufio.NewReaderSize(io.NewSectionReader(fh, from, to-from), 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			if body := line[:len(line)-1]; len(bytes.TrimSpace(body)) > 0 {
				ParseLine(n, body, m)
			}
			n++
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
