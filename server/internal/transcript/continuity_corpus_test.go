package transcript

// Replays real transcripts (-corpus, see corpus_test.go) the way they reach the phone and checks the phone ends up
// with exactly what a cold read of the file gives:
//
//	./corpus.test -test.run CorpusContinuity -corpus <dirs> -sim 3 -out continuity.jsonl
//
// The file is written in random pieces (mid-line too). After each piece the node reads what is new and pushes it with
// its span; two clients follow the pushes with ChatStore.apply's rules (apple/Shared/ChatStore.swift): some pushes are
// lost on the way, one client joins late from a stale cache and catches up the way the hub does it (after_seq pages
// of up to 500 items, at most 8, then more_after), holes are filled with after_seq pages of random sizes, and the
// file may be cut back and rewritten or replaced (a new history). At the end each client reconnects, scrolls back to
// the start, and must hold exactly the cold read's items in order; a client that lost nothing must have received
// every item exactly once, in order.

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

var (
	simSeeds = flag.Int("sim", 0, "continuity simulation: random seeds per file")
	// simBug puts back a rule the apps had, to show the simulation catches it: "covered0" — a batch continuing from
	// line 0 (a new session watched from its first line) set no baseline, so holes after it went unnoticed.
	simBug = flag.String("simbug", "", "")
	// simRestart: the node restarts once in half the runs (a deploy): the file is opened cold again, by its end when
	// it is over twice this many bytes, and the clients catch up from what they have.
	simRestart = flag.Int64("simwindow", 256<<10, "continuity simulation: window size for the restarted node's cold read")
)

type simClient struct {
	name     string
	history  string
	covered  int
	loaded   bool // a page from the end has come (ChatStore.loadedOnce)
	items    map[string]proto.Item
	received int // items received, all batches (duplicates included)
	stream   []proto.Item
	lossless bool // never lost a push: stream must equal the file
	fills    int
	reloads  int
}

type gap int

const (
	gapNone gap = iota
	gapFill
	gapReload
)

func newClient(name string) *simClient { return &simClient{name: name, items: map[string]proto.Item{}} }

func (c *simClient) merge(items []proto.Item) {
	for _, it := range items {
		c.items[it.ID] = it
	}
	c.received += len(items)
}

func (c *simClient) reset() {
	c.items = map[string]proto.Item{}
	c.covered = 0
}

func (c *simClient) firstSeq() int {
	first := 0
	for _, it := range c.items {
		if first == 0 || it.Seq < first {
			first = it.Seq
		}
	}
	return first
}

// apply is ChatStore.apply. continues: the batch has a from_seq (pushes, catch-up, after_seq pages).
func (c *simClient) apply(items []proto.Item, sp Span, continues, moreAfter bool) gap {
	if sp.History != "" && c.history != "" && sp.History != c.history {
		c.reset()
		c.history = sp.History
		if continues && sp.From > 0 {
			return gapReload
		}
	}
	if sp.History != "" {
		c.history = sp.History
	}
	if continues {
		c.merge(items)
		if c.covered == 0 && (sp.From > 0 || *simBug == "covered0") {
			// No baseline: the page from the end on its way sets one; if it already came (and set none), take the
			// latest page now, or a hole before this batch would never be noticed.
			if c.loaded && *simBug != "noreload" {
				return gapReload
			}
			return gapNone
		}
		if sp.From > c.covered {
			return gapFill
		}
		c.covered = max(c.covered, sp.To)
		if moreAfter {
			return gapFill
		}
		return gapNone
	}
	c.loaded = true
	if c.covered > 0 && len(items) > 0 && items[0].Seq > c.covered+1 { // a latest page past what we hold: replace
		c.reset()
		c.merge(items)
		c.covered = sp.To
		return gapNone
	}
	c.merge(items)
	c.covered = max(c.covered, sp.To)
	return gapNone
}

type simRun struct {
	t     *testing.T
	f     *File
	rng   *rand.Rand
	fails []string
}

func (r *simRun) failf(format string, a ...any) {
	if len(r.fails) < 20 {
		r.fails = append(r.fails, fmt.Sprintf(format, a...))
	}
}

// after is one after_seq page as the node serves it, checked: it continues exactly from afterSeq and never splits a
// transcript line.
func (r *simRun) after(afterSeq, limit int) ([]proto.Item, bool, Span) {
	items, more, sp := r.f.After(afterSeq, limit)
	if sp.From != afterSeq || sp.To < afterSeq {
		r.failf("After(%d) span %+v", afterSeq, sp)
	}
	if len(items) > 0 && items[0].Seq <= afterSeq {
		r.failf("After(%d) starts at seq %d", afterSeq, items[0].Seq)
	}
	if more {
		next, _, _ := r.f.After(items[len(items)-1].Seq, 1)
		if len(next) > 0 && next[0].Seq == items[len(items)-1].Seq {
			r.failf("After(%d, %d) split line %d", afterSeq, limit, next[0].Seq)
		}
	}
	return items, more, sp
}

// fill is AppModel.fill: after_seq pages from covered, up to 8, then the latest page.
func (r *simRun) fill(c *simClient) {
	c.fills++
	for range 8 {
		items, more, sp := r.after(c.covered, 1+r.rng.Intn(600))
		switch c.apply(items, sp, true, more) {
		case gapNone:
			return
		case gapReload:
			r.reload(c)
			return
		}
	}
	r.reload(c)
}

// reload is AppModel.reload: start over from the latest page.
func (r *simRun) reload(c *simClient) {
	c.reloads++
	items, more, sp := r.f.PageTurns(0, 2, 2000)
	_ = more
	c.reset()
	c.history = sp.History
	c.merge(items)
	c.covered = sp.To
	c.loaded = true
}

// subscribe is the hub's sendBacklog for a client with since_seq = covered (or the last two exchanges without).
func (r *simRun) subscribe(c *simClient) {
	if c.covered == 0 {
		items, more, sp := r.f.PageTurns(0, 2, 2000)
		_ = more
		r.settle(c, c.apply(items, sp, false, false))
		return
	}
	after := c.covered
	for page := 0; ; page++ {
		items, more, sp := r.after(after, 500)
		capped := more && page+1 >= 8
		g := c.apply(items, sp, true, capped)
		if g != gapNone || !more || capped {
			r.settle(c, g)
			return
		}
		after = max(sp.To, after)
	}
}

func (r *simRun) settle(c *simClient, g gap) {
	switch g {
	case gapFill:
		r.fill(c)
	case gapReload:
		r.reload(c)
	}
}

// scrollToStart pages back to the first item the way the chat loads older pages.
func (r *simRun) scrollToStart(c *simClient) {
	for i := 0; ; i++ {
		first := c.firstSeq()
		if first == 0 {
			return
		}
		items, more, _ := r.f.Page(first, 1+r.rng.Intn(300))
		if len(items) > 0 && items[len(items)-1].Seq >= first {
			r.failf("Page(before %d) reaches seq %d", first, items[len(items)-1].Seq)
		}
		c.merge(items)
		if !more || len(items) == 0 || i > 1_000_000 {
			return
		}
	}
}

func ordered(m map[string]proto.Item) []proto.Item {
	out := make([]proto.Item, 0, len(m))
	for _, it := range m {
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return part(out[i].ID) < part(out[j].ID)
	})
	return out
}

func part(id string) int {
	i := strings.LastIndexByte(id, '#')
	n := 0
	if i >= 0 {
		fmt.Sscanf(id[i+1:], "%d", &n)
	}
	return n
}

type simResult struct {
	Path       string   `json:"path"`
	Seed       int64    `json:"seed"`
	Size       int64    `json:"size"`
	Steps      int      `json:"steps"`
	Items      int      `json:"items"`
	Rewrites   int      `json:"rewrites"`
	Restarts   int      `json:"restarts"`
	Windowed   int      `json:"windowed"` // restarts whose cold read was by the end
	Dropped    int      `json:"dropped"`
	Fills      int      `json:"fills"`
	Reloads    int      `json:"reloads"`
	Received   int      `json:"received"`
	Unpaired   int      `json:"unpaired_results"` // in the cold read itself: tool results whose call is not in the file
	Fails      []string `json:"fails,omitempty"`
	ElapsedSec float64  `json:"elapsed_sec"`
}

func TestCorpusContinuity(t *testing.T) {
	if *simSeeds == 0 {
		t.Skip("-sim")
	}
	files := corpusFiles(t)
	out, err := os.Create(*corpusOut)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	start := time.Now()
	failed := 0
	for i, p := range files {
		for s := 1; s <= *simSeeds; s++ {
			res := simulate(t, p, int64(s)*7919+int64(i))
			if len(res.Fails) > 0 {
				failed++
			}
			_ = enc.Encode(res)
		}
		if i%20 == 0 {
			fmt.Fprintf(os.Stderr, "%d/%d files, %d failed runs, %v\n", i, len(files), failed, time.Since(start).Round(time.Second))
		}
	}
	fmt.Fprintf(os.Stderr, "done: %d files x %d seeds, %d failed runs, %v\n", len(files), *simSeeds, failed, time.Since(start).Round(time.Second))
	if failed > 0 {
		t.Errorf("%d runs failed (see %s)", failed, *corpusOut)
	}
}

func simulate(t *testing.T, src string, seed int64) (res simResult) {
	t0 := time.Now()
	res = simResult{Path: src, Seed: seed}
	defer func() {
		if r := recover(); r != nil {
			res.Fails = append(res.Fails, fmt.Sprintf("panic: %v", r))
		}
		res.ElapsedSec = time.Since(t0).Seconds()
	}()
	data, err := os.ReadFile(src)
	if err != nil {
		res.Fails = append(res.Fails, err.Error())
		return
	}
	res.Size = int64(len(data))
	rng := rand.New(rand.NewSource(seed))
	dir, _ := os.MkdirTemp("", "sim")
	defer os.RemoveAll(dir)
	if strings.Contains(src, "/subagents/") {
		dir = filepath.Join(dir, "subagents")
		_ = os.MkdirAll(dir, 0o700)
	}
	path := filepath.Join(dir, "t.jsonl")
	_ = os.WriteFile(path, nil, 0o600)
	r := &simRun{t: t, f: openCorpusFile(path), rng: rng}

	a, b := newClient("a"), newClient("b")
	r.subscribe(a)           // A opens the chat before the first line is written (a new session)
	a.lossless = seed%3 == 0 // one run in three: nothing lost, no rewrite; the stream itself must be the file
	dropRate := 0.0
	if !a.lossless {
		dropRate = 0.05
	}
	bJoinsAt := rng.Float64() // B opens the chat after this fraction of the file, from a stale cache
	var bCache *simClient
	bJoined := false
	rewriteAt := -1.0
	if !a.lossless && rng.Intn(2) == 0 {
		rewriteAt = rng.Float64()
	}
	restartAt := -1.0
	if !a.lossless && rng.Intn(2) == 0 {
		restartAt = rng.Float64()
	}
	// Pieces: mostly small (a line being written), sometimes big (a burst after a long tool), sometimes mid-line.
	steps := max(200, min(3000, len(data)/4096))
	avg := max(1, len(data)/steps)
	push := func(c *simClient, items []proto.Item, sp Span, mayDrop bool) {
		if mayDrop && rng.Float64() < dropRate {
			res.Dropped++
			return
		}
		c.stream = append(c.stream, items...)
		r.settle(c, c.apply(items, sp, true, false))
	}
	off := 0
	for off < len(data) {
		n := 1 + rng.Intn(2*avg)
		if rng.Intn(20) == 0 {
			n *= 20
		}
		end := min(len(data), off+n)
		fh, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			res.Fails = append(res.Fails, err.Error())
			return
		}
		_, _ = fh.Write(data[off:end])
		fh.Close()
		off = end
		res.Steps++

		frac := float64(off) / float64(len(data))
		if rewriteAt >= 0 && frac >= rewriteAt {
			// The file is cut back to an earlier line and written again from there (or replaced by a new file with the
			// same start): its history changes and every client starts over.
			rewriteAt = -1
			res.Rewrites++
			cut := bytes.LastIndexByte(data[:off/2+1], '\n') + 1
			if rng.Intn(2) == 0 {
				_ = os.Truncate(path, int64(cut))
			} else {
				tmp := path + ".new"
				_ = os.WriteFile(tmp, data[:cut], 0o600)
				_ = os.Rename(tmp, path)
			}
			off = cut
		}
		fresh, sp, err := r.f.RefreshSpan()
		if err != nil {
			res.Fails = append(res.Fails, err.Error())
			return
		}
		if sp.To != sp.From || len(fresh) > 0 {
			sent := proto.WithoutImageData(fresh)
			push(a, sent, sp, !a.lossless)
			if bJoined {
				push(b, sent, sp, true)
			}
		}
		if restartAt >= 0 && frac >= restartAt {
			restartAt = -1
			res.Restarts++
			saved := windowBytes
			windowBytes = *simRestart
			r.f = openCorpusFile(path)
			if _, _, err := r.f.RefreshSpan(); err != nil {
				res.Fails = append(res.Fails, err.Error())
				return
			}
			windowBytes = saved
			if r.f.base > 0 {
				res.Windowed++
			}
			r.subscribe(a)
			if bJoined {
				r.subscribe(b)
			}
		}
		if !bJoined && bCache == nil && frac >= bJoinsAt/2 {
			// What B's disk cache will hold: A's state at this point.
			bCache = &simClient{name: "b", history: a.history, covered: a.covered, items: map[string]proto.Item{}}
			for k, v := range a.items {
				bCache.items[k] = v
			}
		}
		if !bJoined && frac >= bJoinsAt {
			bJoined = true
			if bCache != nil {
				b.history, b.covered, b.items = bCache.history, bCache.covered, bCache.items // a cache: not loaded
			}
			r.subscribe(b)
		}
	}
	if !bJoined {
		r.subscribe(b)
	}
	// The end: both reconnect (a catch-up from covered), then scroll back to the start.
	for _, c := range []*simClient{a, b} {
		r.subscribe(c)
		r.scrollToStart(c)
	}
	saved := windowBytes
	windowBytes = 1 << 50 // the reference: the whole file read at once (a page of a file read by its end stops early)
	cold := openCorpusFile(path)
	_, err = cold.Refresh()
	windowBytes = saved
	if err != nil {
		res.Fails = append(res.Fails, err.Error())
		return
	}
	want, _, _ := cold.Page(0, 1<<30)
	res.Items = len(want)
	wantJSON := mustJSON(want)
	for _, c := range []*simClient{a, b} {
		got := ordered(c.items)
		if !bytes.Equal(mustJSON(got), wantJSON) {
			res.Fails = append(res.Fails, fmt.Sprintf("client %s holds %d items, the file %d (first difference at %s)", c.name, len(got), len(want), firstDiff(got, want)))
		}
		res.Fills += c.fills
		res.Reloads += c.reloads
		res.Received += c.received
	}
	if a.lossless && !bytes.Equal(mustJSON(a.stream), wantJSON) {
		res.Fails = append(res.Fails, fmt.Sprintf("lossless stream: %d items received, the file %d (first difference at %s)", len(a.stream), len(want), firstDiff(a.stream, want)))
	}
	calls := map[string]bool{}
	for _, it := range want {
		if it.Kind == proto.KindToolUse {
			calls[it.ToolUseID] = true
		} else if it.Kind == proto.KindToolResult && !calls[it.ToolUseID] {
			res.Unpaired++
		}
	}
	res.Fails = append(res.Fails, r.fails...)
	return res
}

func mustJSON(items []proto.Item) []byte {
	if items == nil {
		items = []proto.Item{} // none: the same as an empty list
	}
	b, _ := json.Marshal(items)
	return b
}

func firstDiff(got, want []proto.Item) string {
	for i := range min(len(got), len(want)) {
		if !bytes.Equal(mustJSON(got[i:i+1]), mustJSON(want[i:i+1])) {
			return fmt.Sprintf("#%d got %s/%d want %s/%d", i, got[i].ID, got[i].Seq, want[i].ID, want[i].Seq)
		}
	}
	return fmt.Sprintf("#%d (lengths differ)", min(len(got), len(want)))
}

var dumpDir = flag.String("dumpitems", "", "write each -corpus file's items (as pages carry them) to <dir>/<n>.json")

// TestCorpusDumpItems writes real sessions' items for the app's rebuild benchmark (apple/PersonaTests/RebuildTests.swift).
func TestCorpusDumpItems(t *testing.T) {
	if *dumpDir == "" {
		t.Skip("-dumpitems")
	}
	for i, p := range corpusFiles(t) {
		f := openCorpusFile(p)
		if _, err := f.Refresh(); err != nil {
			t.Fatal(err)
		}
		items, _, _ := f.Page(0, 1<<30)
		if err := os.WriteFile(filepath.Join(*dumpDir, fmt.Sprintf("%d.json", i)), mustJSON(items), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

var windowSizes = flag.String("windows", "1048576,8388608", "TestCorpusWindowed: window sizes to try, in bytes")

type windowResult struct {
	Path     string  `json:"path"`
	Size     int64   `json:"size"`
	Window   int64   `json:"window"`
	Base     int     `json:"base"`
	Lines    int     `json:"lines"`
	Items    int     `json:"items"`
	FullMS   float64 `json:"full_ms"`
	WindowMS float64 `json:"window_ms"`
	// FirstPage / FullFirstPage: items on the first page read by the end and read whole (shorter: its budget ran out).
	FirstPage     int      `json:"first_page"`
	FullFirstPage int      `json:"full_first_page"`
	Fails         []string `json:"fails,omitempty"`
}

// TestCorpusWindowed opens every big corpus file by its end (window.go) and checks it against a full read: the first
// page, the cold read's own items, full texts of items not parsed yet, every page back to the first line, after_seq
// forward from a random line, and Meta. It times both cold opens (parse + the first page).
func TestCorpusWindowed(t *testing.T) {
	if *corpusDirs == "" {
		t.Skip("-corpus")
	}
	defer func(w int64) { windowBytes = w }(windowBytes)
	var sizes []int64
	for _, s := range strings.Split(*windowSizes, ",") {
		var n int64
		fmt.Sscan(s, &n)
		sizes = append(sizes, n)
	}
	out, err := os.Create(*corpusOut)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	failed := 0
	for _, p := range corpusFiles(t) {
		for _, w := range sizes {
			st, _ := os.Stat(p)
			if st.Size() <= 2*w {
				continue
			}
			res := windowedOne(p, w)
			if len(res.Fails) > 0 {
				failed++
			}
			_ = enc.Encode(res)
		}
	}
	if failed > 0 {
		t.Errorf("%d windowed reads differ from full ones (see %s)", failed, *corpusOut)
	}
}

func windowedOne(p string, w int64) (res windowResult) {
	res = windowResult{Path: p, Window: w}
	defer func() {
		if r := recover(); r != nil {
			res.Fails = append(res.Fails, fmt.Sprintf("panic: %v", r))
		}
	}()
	fail := func(format string, a ...any) { res.Fails = append(res.Fails, fmt.Sprintf(format, a...)) }
	open := func(window int64) (*File, []proto.Item, Span, time.Duration) {
		windowBytes = window
		t0 := time.Now()
		f := openCorpusFile(p)
		fresh, sp, err := f.RefreshSpan()
		if err != nil {
			fail("%v", err)
		}
		f.PageTurns(0, 2, 2000)
		return f, fresh, sp, time.Since(t0)
	}
	full, _, _, tf := open(1 << 50)
	f, fresh, sp, tw := open(w)
	res.FullMS, res.WindowMS = float64(tf.Microseconds())/1000, float64(tw.Microseconds())/1000
	res.Base, res.Lines = f.base, f.lines
	st, _ := os.Stat(p)
	res.Size = st.Size()
	all, _, _ := full.Page(0, 1<<30)
	res.Items = len(all)
	rng := rand.New(rand.NewSource(int64(len(p)) + w))
	same := func(what string, a, b []byte) {
		if !bytes.Equal(a, b) {
			fail("%s differs", what)
		}
	}
	var tail []proto.Item
	for _, it := range all {
		if it.Seq > sp.From {
			tail = append(tail, it)
		}
	}
	same("the cold read's items", mustJSON(proto.WithoutImageData(fresh)), mustJSON(tail))
	wi, wm, ws := f.PageTurns(0, 2, 2000)
	fi, fm, fs := full.PageTurns(0, 2, 2000)
	if msg := pageSuffix(wi, wm, ws, fi, fm, fs); msg != "" {
		fail("the first page: %s", msg)
	}
	res.FirstPage, res.FullFirstPage = len(wi), len(fi)
	// Full texts / images of items before the window, in a fresh windowed read.
	if f.base > 0 && len(all) > 0 {
		g, _, _, _ := open(w)
		for k := 0; k < 3; k++ {
			it := all[rng.Intn(len(all))]
			a, errA := g.Full(it.ID)
			b, errB := full.Full(it.ID)
			if (errA == nil) != (errB == nil) || !bytes.Equal(mustJSON([]proto.Item{a}), mustJSON([]proto.Item{b})) {
				fail("Full(%s) differs", it.ID)
			}
		}
	}
	// Every page back to the first line.
	g, _, _, _ := open(w)
	var got []proto.Item
	for before := 0; ; {
		items, more, _ := g.Page(before, 1+rng.Intn(400))
		got = append(append([]proto.Item(nil), items...), got...)
		if !more || len(items) == 0 {
			break
		}
		before = items[0].Seq
	}
	same("paging back", mustJSON(got), mustJSON(all))
	// after_seq from a random line (a phone catching up after a node restart).
	h, _, _, _ := open(w)
	after := rng.Intn(max(1, res.Lines))
	got = nil
	for a := after; ; {
		items, more, span := h.After(a, 1+rng.Intn(600))
		if span.From != a {
			fail("After(%d) span %+v", a, span)
		}
		got = append(got, items...)
		if len(items) > 0 {
			a = items[len(items)-1].Seq
		}
		if !more {
			break
		}
	}
	var want []proto.Item
	for _, it := range all {
		if it.Seq > after {
			want = append(want, it)
		}
	}
	same(fmt.Sprintf("after_seq %d", after), mustJSON(got), mustJSON(want))
	// Meta of the whole file.
	am, _ := json.Marshal(f.Meta())
	bm, _ := json.Marshal(full.Meta())
	same("Meta", am, bm)
	return res
}

// The same simulation on the repository's own transcripts (testdata), in every run: random pieces, lost pushes, a
// late client from a stale cache, random fill pages, rewrites and a node restart, under fixed seeds. Every run must
// end with exactly the cold read's items (#1073).
func TestContinuityConvergesOnFixtures(t *testing.T) {
	files, err := filepath.Glob("testdata/*.jsonl")
	if err != nil || len(files) == 0 {
		t.Fatalf("no transcripts in testdata: %v", err)
	}
	seeds := 24
	if testing.Short() {
		seeds = 4
	}
	// and one long enough that a client catching up from far behind meets the hub's cap (8 pages of 500): the rest
	// comes only by following more_after
	long := filepath.Join(t.TempDir(), "claude-long.jsonl")
	if err := os.WriteFile(long, longTranscript(7000), 0o600); err != nil {
		t.Fatal(err)
	}
	files = append(files, long)
	for i, p := range files {
		n := seeds
		if p == long {
			n = max(2, seeds/3)
		}
		for s := 1; s <= n; s++ {
			seed := int64(s)*7919 + int64(i)
			if res := simulate(t, p, seed); len(res.Fails) > 0 {
				t.Errorf("%s, seed %d: %s", filepath.Base(p), seed, strings.Join(res.Fails, "; "))
			}
		}
	}
}

// longTranscript is a made-up Claude Code transcript of `turns` question-and-answer turns, each ending with its
// turn duration (a line without items).
func longTranscript(turns int) []byte {
	var b bytes.Buffer
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	parent := "null"
	line := func(n int, ts time.Time, body string) {
		uuid := fmt.Sprintf("00000000-0000-4000-8000-%012d", n)
		fmt.Fprintf(&b, `{"parentUuid":%s,"isSidechain":false,%s,"uuid":"%s","timestamp":"%s","userType":"external","entrypoint":"cli","cwd":"/work/project","sessionId":"00000000-0000-4000-8000-0000000000bb","version":"2.1.312"}`+"\n",
			parent, body, uuid, ts.Format("2006-01-02T15:04:05.000Z"))
		parent = `"` + uuid + `"`
	}
	n := 0
	for k := 0; k < turns; k++ {
		ts := t0.Add(time.Duration(k) * time.Minute)
		n++
		line(n, ts, fmt.Sprintf(`"type":"user","message":{"role":"user","content":"question %d about the route"}`, k+1))
		n++
		line(n, ts.Add(10*time.Second), fmt.Sprintf(`"type":"assistant","message":{"id":"msg_%d","type":"message","role":"assistant","model":"claude-example","content":[{"type":"text","text":"answer %d: take the river path"}],"stop_reason":"end_turn"}`, n, k+1))
		n++
		line(n, ts.Add(11*time.Second), `"type":"system","subtype":"turn_duration","durationMs":11000`)
	}
	return b.Bytes()
}

// A client holding only the first turn catches up across a long transcript: the replay stops at the hub's cap (8 pages
// of 500) and says more_after, which the client follows; it ends with exactly the cold read's items (#1073, the class
// of the web's #1118).
func TestCatchUpPastTheCapFollowsMoreAfter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, longTranscript(7000), 0o600); err != nil {
		t.Fatal(err)
	}
	f := openCorpusFile(path)
	if _, err := f.Refresh(); err != nil {
		t.Fatal(err)
	}
	want, _, _ := f.Page(0, 1<<30)
	r := &simRun{t: t, f: f, rng: rand.New(rand.NewSource(1))}
	c := newClient("late")
	first, _, sp := r.after(0, 2)
	c.apply(first, sp, true, false)
	r.subscribe(c)
	r.scrollToStart(c)
	if got := ordered(c.items); !bytes.Equal(mustJSON(got), mustJSON(want)) {
		t.Fatalf("the client holds %d items, the file %d (first difference at %s)", len(got), len(want), firstDiff(got, want))
	}
	if len(r.fails) > 0 {
		t.Fatal(strings.Join(r.fails, "; "))
	}
}
