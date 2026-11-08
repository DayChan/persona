package kb

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/nwverse/persona/server/internal/kbwire"
)

// Searching for the app (docs/assistant/knowledge.md "Search"): results are entries, not lines. Every line containing all the
// words is found (archived messages newest day first), grouped by the entry it belongs to, the entries ranked
// (name matches, then importance, relevance, how much matched, recency), a few lines kept for each. Opening one, Matches
// gives every place in that entry with a few messages around each, and Day the whole day a message is in.

// Found is one line of an entry that matched.
type Found struct {
	Path string `json:"path"` // relative to the data dir
	Line string `json:"line"`
	// N is the line's number in its file (1-based): messages are found again by it on the day page.
	N int `json:"n"`
}

// Result is one entry found by a search: the entry as ranked lists show it and its first lines.
type Result struct {
	Entry Entry   `json:"entry"`
	Count int     `json:"count"` // lines matched in all, at most foundCap
	Hits  []Found `json:"hits"`
	// InName: the name itself matched (those come first).
	InName bool `json:"in_name,omitempty"`
}

const (
	hitsPerResult = 3
	foundCap      = 99
)

// fold is the form text and queries are compared in: lowercased, then composed (NFC), so "café" typed precomposed
// finds the decomposed "cafe\u0301" macOS often writes, and the other way round. Already composed text (nearly all of
// the archive) passes norm's quick check without being copied.
func fold(s string) string { return norm.NFC.String(strings.ToLower(s)) }

// words is the query folded and split; a file without all of them is skipped whole.
func words(query string) []string { return strings.Fields(fold(query)) }

// hasAll is the whole-file check: most files have none of the words, and one pass over the file is much cheaper than
// a match per line (the archive is ~400k lines; everything is read, the oldest days too).
func hasAll(lowered string, ws []string) bool {
	for _, w := range ws {
		if !strings.Contains(lowered, w) {
			return false
		}
	}
	return true
}

// matcher returns whether a line has every word of the query (case- and normalization-insensitive, fold); nil for an
// empty query.
func matcher(query string) func(string) bool {
	words := words(query)
	if len(words) == 0 {
		return nil
	}
	return func(l string) bool {
		l = fold(l)
		for _, w := range words {
			if !strings.Contains(l, w) {
				return false
			}
		}
		return true
	}
}

// searchable reports whether a file of the data dir (path relative to it) holds text a search may return. Bookkeeping
// is not: a person's hashed identity hints (handles.json, handles.go), a connector's cursor (cursors/<source>.json),
// history imports' progress (history/, history.go) and the Moments verdicts of a project (moments-judged.json,
// moments_link.go) mean nothing to a model, and the hints are kept out of every tool and prompt.
func searchable(rel string) bool {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	switch {
	case parts[0] == "cursors" || parts[0] == "history":
		return false
	case len(parts) == 3 && parts[0] == KindPerson && parts[2] == "handles.json":
		return false
	case len(parts) == 3 && parts[0] == KindProject && parts[2] == "moments-judged.json":
		return false
	}
	return true
}

// searchable applies searchable to p, a path under the data dir.
func (e *Engine) searchable(p string) bool {
	rel, err := filepath.Rel(e.Store.Dir, p)
	return err != nil || searchable(rel)
}

// entryOf maps a path relative to the data dir to the entry it belongs to: chats/<id>/…, docs/<id>.json.
func entryOf(rel string) (kind, id string, ok bool) {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) < 2 {
		return "", "", false
	}
	switch parts[0] {
	case KindChat, KindPerson, KindProject, KindRecording:
		if len(parts) < 3 {
			return "", "", false
		}
		return parts[0], parts[1], true
	case KindDoc, KindMeeting, KindEvent:
		if id, ok := strings.CutSuffix(parts[1], ".json"); ok && len(parts) == 2 {
			return parts[0], id, true
		}
	}
	return "", "", false
}

// shown reports whether Matches reads the file at rel (relative to the data dir) for the entry kind/id it belongs to:
// Find only counts lines from those, so every result it gives can be located once opened. Other files under an entry
// (a project's moments-judged.json, a person's handles.json) are bookkeeping, not something to show.
func shown(kind, rel string) bool {
	// rest is what follows <kind>/<id>/ (entryOf has checked there is one).
	_, rest, _ := strings.Cut(filepath.ToSlash(rel), "/")
	_, rest, _ = strings.Cut(rest, "/")
	switch kind {
	case KindChat, KindPerson, KindProject:
		if rest == "summary.md" || rest == "log.md" || rest == "meta.json" {
			return true
		}
		// Store.Days: the .md files directly in a chat's messages/.
		day, ok := strings.CutPrefix(rest, "messages/")
		return kind == KindChat && ok && strings.HasSuffix(day, ".md") && !strings.Contains(day, "/")
	case KindRecording:
		return rest == "summary.md" || rest == "transcript.md" || rest == "meta.json"
	case KindDoc, KindMeeting, KindEvent:
		return true // entryOf already keeps only <kind>/<id>.json
	}
	return false
}

// Find searches every entry and returns at most limit, ranked; none when limit <= 0.
func (e *Engine) Find(query string, limit int) []Result {
	match := matcher(query)
	if match == nil || limit <= 0 {
		return nil
	}
	ws := words(query)
	// Files are looked at in parallel (the archive is ~25 MB); what each found is added in file order, so the lines
	// kept for an entry are its profile's first, then the newest days'. The app searches as the user types, so a
	// file unchanged since the last search is not read again (findCache): one stat each instead of read + lowercase.
	files, days := e.files()
	all := append(append(make([]string, 0, len(files)+len(days)), files...), days...)
	type fileHits struct {
		key   string
		stat  fileStat
		found []Found
	}
	per := make([]fileHits, len(all))
	gen := e.find.begin()
	var next atomic.Int64
	var wg sync.WaitGroup
	for range max(1, min(8, runtime.NumCPU())) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(all) {
					return
				}
				p := all[i]
				rel, _ := filepath.Rel(e.Store.Dir, p)
				kind, id, ok := entryOf(rel)
				if !ok || !shown(kind, rel) {
					continue
				}
				fh := fileHits{key: kind + "/" + id}
				c, st, ok := e.find.text(p, gen)
				fh.stat = st
				if ok && hasAll(c.lower, ws) {
					for n, l := range strings.Split(c.raw, "\n") {
						if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "# ") && match(t) {
							fh.found = append(fh.found, Found{Path: filepath.ToSlash(rel), Line: clip(t, 300), N: n + 1})
						}
					}
				}
				per[i] = fh
			}
		}()
	}
	wg.Wait()
	e.find.prune(gen)
	// The entries the lists show depend only on the JSON files (meta.json, docs/<id>.json): kept while none changed.
	var metas []fileStat
	for i, fh := range per {
		if fh.key != "" && strings.HasSuffix(all[i], ".json") {
			metas = append(metas, fh.stat)
		}
	}
	known := e.find.entries(metas, func() map[string]Entry {
		known := map[string]Entry{}
		for _, k := range []string{KindProject, KindChat, KindPerson, KindDoc, KindMeeting, KindEvent, KindRecording} {
			for _, en := range e.List(k, "") {
				known[k+"/"+en.ID] = en
			}
		}
		return known
	})
	by := map[string]*Result{}
	add := func(key string, f Found) {
		r := by[key]
		if r == nil {
			r = &Result{Entry: known[key]}
			by[key] = r
		}
		if r.Count < foundCap {
			r.Count++
		}
		if len(r.Hits) < hitsPerResult && !hasLine(r.Hits, f.Line) {
			r.Hits = append(r.Hits, f)
		}
	}
	for _, fh := range per {
		if _, ok := known[fh.key]; !ok {
			continue // an entry the lists do not show (a doc without a title yet): nothing to open
		}
		for _, f := range fh.found {
			add(fh.key, f)
		}
	}
	// Names: an entry whose name matches is found even when no file line does.
	for key, en := range known {
		if match(en.Name) {
			if by[key] == nil {
				by[key] = &Result{Entry: en}
			}
			by[key].InName = true
		}
	}
	out := make([]Result, 0, len(by))
	for _, r := range by {
		r.Hits = sortHits(r.Hits)
		out = append(out, *r)
	}
	imp := func(s string) int {
		if s == "" {
			return 4
		}
		return int(s[1] - '0')
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.InName != b.InName {
			return a.InName
		}
		if imp(a.Entry.Importance) != imp(b.Entry.Importance) {
			return imp(a.Entry.Importance) < imp(b.Entry.Importance)
		}
		if a.Entry.Relevance != b.Entry.Relevance {
			return a.Entry.Relevance > b.Entry.Relevance
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if !a.Entry.Updated.Equal(b.Entry.Updated) {
			return a.Entry.Updated.After(b.Entry.Updated)
		}
		return a.Entry.Kind+a.Entry.ID < b.Entry.Kind+b.Entry.ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// files lists the profile files (summaries, metadata, timelines) and the message days, newest day first. The
// slices are shared with findCache: callers only read them.
func (e *Engine) files() (files, days []string) {
	c := &e.find
	c.walkMu.Lock()
	defer c.walkMu.Unlock()
	c.walkGen++
	changed := false
	sep := string(filepath.Separator)
	var walk func(dir string)
	walk = func(dir string) {
		ents, fresh := c.listDir(dir)
		changed = changed || fresh
		for _, d := range ents {
			p := filepath.Join(dir, d.name)
			switch {
			case d.dir:
				// Attachments hold no text; history/ is bookkeeping (searchable).
				if d.name != "attachments" && (dir != e.Store.Dir || d.name != "history") {
					walk(p)
				}
			case strings.Contains(p, sep+"messages"+sep):
				days = append(days, p)
			case d.name == "segments.json": // a recording's: transcript.md has the same text
			case !e.searchable(p):
			case strings.HasSuffix(p, ".md") || strings.HasSuffix(p, ".json"):
				files = append(files, p)
			}
		}
	}
	walk(e.Store.Dir)
	for d, l := range c.dirs {
		if l.gen != c.walkGen {
			delete(c.dirs, d)
			changed = true
		}
	}
	if !changed && c.walked {
		return c.files, c.days
	}
	sort.SliceStable(days, func(i, j int) bool { return filepath.Base(days[i]) > filepath.Base(days[j]) })
	c.files, c.days, c.walked = files, days, !changed
	return files, days
}

// sortHits puts what says most first: the profile, the timeline, messages, then metadata (a tag, a member's name).
func sortHits(h []Found) []Found {
	rank := func(f Found) int {
		switch {
		case strings.HasSuffix(f.Path, "/summary.md"):
			return 0
		case strings.HasSuffix(f.Path, "/log.md"):
			return 1
		case strings.Contains(f.Path, "/messages/"), strings.HasSuffix(f.Path, "/transcript.md"):
			return 2
		}
		return 3
	}
	sort.SliceStable(h, func(i, j int) bool { return rank(h[i]) < rank(h[j]) })
	return h
}

func hasLine(h []Found, line string) bool {
	for _, f := range h {
		if f.Line == line {
			return true
		}
	}
	return false
}

// Match is one place in an entry that matched: the line, and for a message the ones around it.
type Match struct {
	Path string `json:"path"`
	N    int    `json:"n"`
	Line string `json:"line"`
	// Day is a message's day ("2006-01-02"); Before / After the messages next to it that day (continuation lines
	// folded into their message).
	Day    string   `json:"day,omitempty"`
	Before []string `json:"before,omitempty"`
	After  []string `json:"after,omitempty"`
}

// Matches returns the places in one entry matching query: the profile, timeline and metadata, then messages, newest
// day first; at most limit.
func (e *Engine) Matches(kind, id, query string, limit int) []Match {
	if kind == KindRecording {
		id, _ = RecordingID(id)
	}
	match := matcher(query)
	if match == nil {
		return nil
	}
	var out []Match
	ws := words(query)
	look := func(p string, msgs bool) {
		raw, err := os.ReadFile(p)
		if err != nil || !hasAll(fold(string(raw)), ws) {
			return
		}
		rel, _ := filepath.Rel(e.Store.Dir, p)
		rel = filepath.ToSlash(rel)
		if !msgs {
			for i, l := range strings.Split(string(raw), "\n") {
				if len(out) >= limit {
					return
				}
				if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "# ") && match(t) {
					out = append(out, Match{Path: rel, N: i + 1, Line: clip(t, 600)})
				}
			}
			return
		}
		day := strings.TrimSuffix(filepath.Base(p), ".md")
		ms := messages(string(raw))
		// Newest first within the day too, as the days are.
		for i := len(ms) - 1; i >= 0; i-- {
			if len(out) >= limit {
				return
			}
			m := ms[i]
			if !match(m.text) {
				continue
			}
			mt := Match{Path: rel, N: m.n, Line: clip(m.text, 1200), Day: day}
			for j := max(0, i-2); j < i; j++ {
				mt.Before = append(mt.Before, clip(ms[j].text, 300))
			}
			for j := i + 1; j < min(len(ms), i+3); j++ {
				mt.After = append(mt.After, clip(ms[j].text, 300))
			}
			out = append(out, mt)
		}
	}
	switch kind {
	case KindChat, KindPerson, KindProject:
		for _, f := range []string{"summary.md", "log.md", "meta.json"} {
			look(e.Store.path(kind, id, f), false)
		}
		if kind == KindChat {
			days := e.Store.Days(id)
			for i := len(days) - 1; i >= 0 && len(out) < limit; i-- {
				look(e.Store.path(KindChat, id, "messages", days[i]+".md"), true)
			}
		}
	case KindRecording:
		for _, f := range []string{"summary.md", "transcript.md", "meta.json"} {
			look(e.Store.path(kind, id, f), false)
		}
	case KindDoc, KindMeeting, KindEvent:
		look(filepath.Join(e.Store.Dir, kind, SafeID(id)+".json"), false)
	}
	return out
}

type message struct {
	n    int // line number of its first line (1-based)
	text string
}

// messages splits a day file into messages: "- HH:MM name: text" and the indented lines that continue it.
func messages(raw string) []message {
	var out []message
	for i, l := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(l, "- "):
			out = append(out, message{n: i + 1, text: strings.TrimPrefix(l, "- ")})
		case strings.HasPrefix(l, "  ") && len(out) > 0:
			out[len(out)-1].text += "\n" + strings.TrimSpace(l)
		}
	}
	return out
}

// Defined in the contract package kbwire (docs/server/architecture.md "Contract packages"); the old names stay.
type DayMessage = kbwire.DayMessage

// Day returns a chat's messages of one day ("2006-01-02") and the days before and after it that have any.
func (e *Engine) Day(chatID, day string) (msgs []DayMessage, prev, next string, err error) {
	if _, perr := time.Parse("2006-01-02", day); perr != nil {
		return nil, "", "", ErrNotFound
	}
	raw, rerr := os.ReadFile(e.Store.path(KindChat, chatID, "messages", day+".md"))
	if rerr != nil {
		return nil, "", "", ErrNotFound
	}
	for _, m := range messages(string(raw)) {
		msgs = append(msgs, DayMessage{N: m.n, Text: m.text})
	}
	for _, d := range e.Store.Days(chatID) {
		if d < day {
			prev = d
		} else if d > day && next == "" {
			next = d
		}
	}
	return msgs, prev, next, nil
}

// findCacheBytes bounds what findCache keeps of the files' text (raw and lowercased); files beyond it are read every
// time, as without the cache.
const findCacheBytes = 64 << 20

// findSettle: a file changed this recently is not kept. A write within the same mtime tick and of the same size
// would otherwise go unseen; once the mtime is this old, any later write gives a newer one.
const findSettle = 2 * time.Second

// fileStat is what tells a file changed.
type fileStat struct {
	path string
	mod  int64 // UnixNano
	size int64
}

func settling(st fileStat) bool { return time.Since(time.Unix(0, st.mod)) < findSettle }

type findText struct {
	stat  fileStat
	raw   string
	lower string // raw folded (fold): the same string unless raw has upper case or decomposed text
	gen   uint64 // the last search that saw the file (prune drops the ones no search saw since)
}

// findCache is what Find keeps between searches: each file's text, and the entries of the lists while no JSON file
// changed. Everything is checked against a fresh stat on every search, so results are those of reading every file.
type findCache struct {
	mu    sync.Mutex
	gen   uint64
	texts map[string]*findText
	bytes int
	// metas: the stats of the JSON files known was built from (sorted by path as files lists them).
	metas []fileStat
	known map[string]Entry

	// The walk (files): each directory's listing while its mtime holds (adding, removing or renaming an entry
	// changes it), and the sorted result while no listing changed.
	walkMu      sync.Mutex
	walkGen     uint64
	dirs        map[string]*dirListing
	walked      bool
	files, days []string
}

type dirListing struct {
	stat fileStat
	ents []dirEnt
	gen  uint64
}

type dirEnt struct {
	name string
	dir  bool
}

// listDir returns a directory's entries sorted by name, and whether they were read now rather than kept. Called
// with walkMu held.
func (c *findCache) listDir(dir string) ([]dirEnt, bool) {
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, true
	}
	st := fileStat{path: dir, mod: fi.ModTime().UnixNano(), size: fi.Size()}
	if l := c.dirs[dir]; l != nil && l.stat == st {
		l.gen = c.walkGen
		return l.ents, false
	}
	des, err := os.ReadDir(dir)
	ents := make([]dirEnt, len(des))
	for i, d := range des {
		ents[i] = dirEnt{name: d.Name(), dir: d.IsDir()}
	}
	delete(c.dirs, dir)
	if err == nil && !settling(st) {
		if c.dirs == nil {
			c.dirs = map[string]*dirListing{}
		}
		c.dirs[dir] = &dirListing{stat: st, ents: ents, gen: c.walkGen}
	}
	return ents, true
}

func (c *findCache) begin() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	return c.gen
}

// text returns a file's text and its stat, from the cache while the file is unchanged.
func (c *findCache) text(p string, gen uint64) (*findText, fileStat, bool) {
	fi, err := os.Stat(p)
	if err != nil {
		return nil, fileStat{}, false
	}
	st := fileStat{path: p, mod: fi.ModTime().UnixNano(), size: fi.Size()}
	c.mu.Lock()
	t := c.texts[p]
	if t != nil && t.stat == st {
		t.gen = gen
		c.mu.Unlock()
		return t, st, true
	}
	c.mu.Unlock()
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, st, false
	}
	s := string(raw)
	t = &findText{stat: st, raw: s, lower: fold(s), gen: gen}
	if int64(len(raw)) != st.size || settling(st) {
		return t, st, true // changing while read, or just now: used once, not kept
	}
	n := t.cost()
	c.mu.Lock()
	defer c.mu.Unlock()
	if old := c.texts[p]; old != nil {
		c.bytes -= old.cost()
		delete(c.texts, p)
	}
	if c.bytes+n <= findCacheBytes {
		if c.texts == nil {
			c.texts = map[string]*findText{}
		}
		c.texts[p] = t
		c.bytes += n
	}
	return t, st, true
}

// cost is the bytes a cached text holds (folding returns the same string when nothing changes; a decomposed or
// mixed-case file keeps a second, folded copy).
func (t *findText) cost() int {
	if t.lower == t.raw {
		return len(t.raw)
	}
	return len(t.raw) + len(t.lower)
}

// prune drops the files the search gen and those after it did not see (deleted, or no longer searched).
func (c *findCache) prune(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for p, t := range c.texts {
		if t.gen < gen {
			c.bytes -= t.cost()
			delete(c.texts, p)
		}
	}
}

// entries returns the lists' entries, built again by list unless metas (the JSON files' stats now) are those they
// were built from. The map is shared: callers only read it.
func (c *findCache) entries(metas []fileStat, list func() map[string]Entry) map[string]Entry {
	c.mu.Lock()
	if c.known != nil && slices.Equal(c.metas, metas) {
		known := c.known
		c.mu.Unlock()
		return known
	}
	c.mu.Unlock()
	known := list()
	for _, m := range metas {
		if settling(m) {
			return known // a file just written: build again next time
		}
	}
	c.mu.Lock()
	c.metas, c.known = metas, known
	c.mu.Unlock()
	return known
}
