package kb

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

// ItemSource lists documents, meetings and calendar events (Feishu through the Lark extension).
type ItemSource interface {
	Items(ctx context.Context) ([]kbwire.Item, error)
	// DocTitle looks up a document's title by its link ("" when it cannot).
	DocTitle(ctx context.Context, url string) string
}

// CollectItems stores new meetings and events and fills in titles of documents seen in chats. With a selection
// (Engine.Selection), a document is stored only when it selects it (Selection.DocSelected): one it does not select is
// not written, and one already stored is left as it is (the selection stops writes; only Prune deletes, by days).
func (e *Engine) CollectItems(ctx context.Context, src ItemSource, sources ...string) error {
	if e.Store.ReadOnly() {
		return ErrReadOnly
	}
	source := ""
	if len(sources) > 0 {
		source = sources[0]
	}
	items, err := src.Items(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	sel, ret := e.selection(), e.Retention()
	for _, w := range items {
		it := ItemFromWire(w)
		it.Source = source
		if it.Kind == KindDoc && sel != nil && !sel.DocSelected(ret, DocFacts{Source: source, ID: it.ID, Signals: it.Signals}) {
			continue
		}
		old, ok := e.Store.Item(it.Kind, it.ID)
		if ok {
			// An ID alone cannot identify an owner: legacy or colliding items stay
			// with their original source unless a stored chat proves ownership.
			owner := e.itemSource(old)
			if owner != source {
				continue
			}
			if old.Source == it.Source && old.Title == it.Title && old.At.Equal(it.At) {
				if !slices.Equal(kbwire.CanonicalSignals(old.Signals), it.Signals) { // canonical: a reordering is no change
					// Only why it concerns the owner moved: keep them as the source says now, without tagging it again.
					old.Signals = it.Signals
					if err := e.Store.PutItem(old); err != nil {
						return err
					}
				}
				continue
			}
			carryOver(&it, old)
		} else {
			it.Seen = now
			if it.Kind == KindDoc && sel != nil {
				// Past its days already (an old document a source still lists): not written, as Prune would delete it.
				if days, ok := sel.DocDays(DocFacts{Source: source, ID: it.ID, Signals: it.Signals}); ok && docExpired(&it, now, days) {
					continue
				}
			}
		}
		if err := e.Store.PutItem(&it); err != nil {
			return err
		}
	}
	n := 0
	for _, it := range e.Store.Items(KindDoc) {
		if it.Title != "" || n >= 10 || ctx.Err() != nil || e.itemSource(it) != source {
			continue
		}
		if x := sel.Item(source, ItemDoc, it.ID); x != nil && !x.Read { // the user keeps it from being read
			continue
		}
		it.Source = source
		n++
		if t := src.DocTitle(ctx, it.URL); t != "" {
			it.Title = t
		} else {
			it.Title = it.URL
		}
		_ = e.Store.PutItem(it)
	}
	return nil
}

// itemSource recovers legacy attribution only from an existing source chat.
func (e *Engine) itemSource(it *Item) string {
	if it.Source != "" {
		return it.Source
	}
	if c, found := e.Store.Chat(it.Where); found {
		if c.Connector != "" {
			return c.Connector
		}
		return c.Source
	}
	return ""
}

type itemTags struct {
	Items []struct {
		ID         string     `json:"id"`
		Importance string     `json:"importance"`
		Relevance  modelScore `json:"relevance"`
		Tags       []string   `json:"tags"`
		Project    string     `json:"project"`
		Note       string     `json:"note"`
	} `json:"items"`
}

// TagItems tags documents, meetings and events that are new or changed, 40 per call.
func (e *Engine) TagItems(ctx context.Context) error {
	if e.Model == nil {
		return nil
	}
	var todo []*Item
	for _, k := range []string{KindEvent, KindMeeting, KindDoc} {
		for _, it := range e.Store.Items(k) {
			if it.TaggedAt.IsZero() && it.Title != "" && !it.Locked {
				todo = append(todo, it)
			}
		}
	}
	for len(todo) > 0 && ctx.Err() == nil {
		batch := todo[:min(40, len(todo))]
		todo = todo[len(batch):]
		byID := map[string]*Item{}
		in := prompts.KBItemsInput{Projects: e.knownProjects(), Tags: e.TopTags(40)}
		for _, it := range batch {
			key := it.Kind + "/" + it.ID
			byID[key] = it
			var at time.Time
			if !it.At.IsZero() {
				at = it.At.In(e.Store.Zone())
			}
			where := it.Where
			if c, ok := e.Store.Chat(it.Where); ok {
				where = c.Name
			}
			in.Items = append(in.Items, prompts.KBItem{Pinned: it.Pinned, Key: key, Kind: it.Kind, Title: it.Title, At: at, Where: where})
		}
		var out itemTags
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		err := e.modelFor("kb.items").JSONMax(cctx, prompts.RenderKBItems(prompts.KBOwner{Owner: e.Owner}), prompts.RenderKBItemsInput(in), 6000, &out)
		cancel()
		if err != nil {
			return err
		}
		e.Store.applyMu.Lock()
		now := time.Now().UTC()
		for _, t := range out.Items {
			it := byID[t.ID]
			if it == nil {
				continue
			}
			if _, exists := e.Store.Item(it.Kind, it.ID); !exists {
				continue
			}
			applyTags(&it.Tagging, t.Importance, int(t.Relevance), t.Tags, now)
			it.Note = oneLine(t.Note)
			if p := e.FindProject(t.Project); p != nil {
				it.Projects = addUnique(it.Projects, p.Slug)
				key := it.Kind + "/" + SafeID(it.ID)
				n := len(p.Items)
				if p.Items = addUnique(p.Items, key); len(p.Items) != n {
					_ = e.Store.PutProject(p)
				}
			}
			_ = e.Store.PutItem(it)
			e.index(it.Kind, it.ID, it.Title, it.Tagging, it.Note, it.At)
		}
		e.Store.applyMu.Unlock()
		// Ones the model skipped: mark tagged anyway so they do not come back every run.
		for _, it := range batch {
			if it.TaggedAt.IsZero() {
				it.TaggedAt = now
				_ = e.Store.PutItem(it)
			}
		}
	}
	return nil
}

// --- reading ---

// Defined in the contract package kbwire (docs/server/architecture.md "Contract packages"); the old names stay.
type (
	Entry = kbwire.Entry
	Hit   = kbwire.Hit
)

// Search finds lines containing every word of the query (case-insensitive) in summaries, metadata, timelines, the
// Markdown of the ExtraDirs and (when messages is set) the message archive; newest message days first. At most limit
// hits.
func (e *Engine) Search(query string, messages bool, limit int) []Hit {
	match := matcher(query)
	if match == nil || limit <= 0 {
		return nil
	}
	ws := words(query)
	// Files come through Find's cache (findCache.text): a file unchanged since an earlier search is one stat, not a
	// read and a fold, and one without every word is skipped whole. Search stops at limit, so it leaves the pruning
	// of files no longer there to Find, which sees them all; the cache is bounded either way (findCacheBytes).
	recording := e.Store.recordingSearchView()
	gen := e.find.begin()
	var hits []Hit
	scan := func(p string) {
		if len(hits) >= limit {
			return
		}
		t, _, ok := e.searchText(p, gen, recording)
		if !ok || !hasAll(t.lower, ws) {
			return
		}
		rel, err := filepath.Rel(e.Store.Dir, p)
		if err != nil || strings.HasPrefix(rel, "..") {
			rel = p
		}
		for _, l := range strings.Split(t.raw, "\n") {
			if len(hits) >= limit {
				return
			}
			if match(l) {
				hits = append(hits, Hit{Path: rel, Line: clip(strings.TrimSpace(l), 300)})
			}
		}
	}
	// The data dir's files come from Find's walk (searchFiles), kept while no directory changed: profile files, the
	// ExtraDirs, the message days newest first, then the Moments archive (moments.go), thousands of posts read after
	// everything else so they never crowd it out.
	o := e.searchFiles()
	for _, d := range o.files {
		scan(d.path)
	}
	for _, dir := range e.ExtraDirs {
		if len(hits) >= limit {
			break
		}
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && p != dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir // .git and the like
			}
			if !d.IsDir() && strings.HasSuffix(p, ".md") {
				scan(p)
			}
			return nil
		})
	}
	if messages {
		for _, d := range o.days {
			if len(hits) >= limit {
				break
			}
			scan(d.path)
		}
	}
	for _, d := range o.moments {
		if len(hits) >= limit {
			break
		}
		scan(d.path)
	}
	return hits
}

// clip cuts s to its first n runes plus an ellipsis; a string of at most n runes comes back as it is, without a copy.
// Ranging over s steps through the same runes as []rune(s) (one U+FFFD per invalid byte), so the cut lands where it
// always did: a valid prefix is cut at its byte offset, and one with invalid bytes still has them replaced.
func clip(s string, n int) string {
	if len(s) <= n { // n bytes are at most n runes
		return s
	}
	runes := 0
	for i := range s {
		if runes == n { // rune n+1 starts at i
			if utf8.ValidString(s[:i]) {
				return s[:i] + "…"
			}
			return string([]rune(s[:i])) + "…"
		}
		runes++
	}
	return s
}

// Rank orders by importance, then relevance, then recency.
func Rank(list []Entry) {
	imp := func(s string) int {
		if s == "" {
			return 4
		}
		return int(s[1] - '0')
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if imp(a.Importance) != imp(b.Importance) {
			return imp(a.Importance) < imp(b.Importance)
		}
		if a.Relevance != b.Relevance {
			return a.Relevance > b.Relevance
		}
		return a.Updated.After(b.Updated)
	})
}

// List returns the entities of a kind as ranked entries; tag filters to those carrying it.
func (e *Engine) List(kind, tag string) []Entry {
	var out []Entry
	has := func(t []string) bool {
		if tag == "" {
			return true
		}
		for _, x := range t {
			if x == tag {
				return true
			}
		}
		return false
	}
	switch kind {
	case KindProject:
		for _, p := range e.Store.Projects() {
			if has(p.Tags) && p.Live() {
				out = append(out, Entry{Kind: kind, ID: p.Slug, Name: p.Name, OneLine: p.OneLine, Importance: p.Importance,
					Relevance: p.Relevance, Tags: p.Tags, Updated: p.Updated, Extra: p.Kind + " · " + p.Status})
			}
		}
	case KindChat:
		for _, c := range e.Store.Chats() {
			if has(c.Tags) {
				out = append(out, Entry{Kind: kind, ID: c.ID, Name: c.Name, OneLine: c.OneLine, Importance: c.Importance,
					Relevance: c.Relevance, Tags: c.Tags, Updated: c.LastAt, Extra: c.Kind, Source: c.Source})
			}
		}
	case KindPerson:
		for _, p := range e.Store.People() {
			if has(p.Tags) {
				out = append(out, Entry{Kind: kind, ID: p.ID, Name: p.Name, OneLine: p.Note, Importance: p.Importance,
					Relevance: p.Relevance, Tags: p.Tags, Updated: p.LastSeen})
			}
		}
	case KindDoc, KindMeeting, KindEvent:
		for _, it := range e.Store.Items(kind) {
			if has(it.Tags) && it.Title != "" {
				out = append(out, Entry{Kind: kind, ID: it.ID, Name: it.Title, OneLine: it.Note, Importance: it.Importance,
					Relevance: it.Relevance, Tags: it.Tags, Updated: it.At, Extra: it.URL})
			}
		}
	case KindRecording:
		for _, r := range e.Store.Recordings() {
			if has(r.Tags) {
				out = append(out, recordingEntry(r))
			}
		}
	}
	Rank(out)
	return out
}

// recordingEntry is a recording as lists show it: Extra is the device it was made on.
func recordingEntry(r *Recording) Entry {
	return Entry{Kind: KindRecording, ID: r.ID, Name: r.Name, OneLine: r.OneLine, Importance: r.Importance,
		Relevance: r.Relevance, Tags: r.Tags, Updated: r.StartedAt, Extra: r.Device}
}

// Prompt is a short Markdown digest of the most important projects and chats, for the assistant's live snapshot.
func (e *Engine) Prompt(projects, chats int) string {
	var d prompts.KBDigest
	for _, p := range e.List(KindProject, "") {
		if len(d.Projects) >= projects || strings.HasSuffix(p.Extra, "done") || strings.HasSuffix(p.Extra, "ignored") {
			continue
		}
		d.Projects = append(d.Projects, digestEntry(p))
	}
	for _, c := range e.List(KindChat, "") {
		if len(d.Chats) >= chats || c.Importance == "P3" {
			continue
		}
		d.Chats = append(d.Chats, digestEntry(c))
	}
	return prompts.RenderKBDigest(d)
}

func digestEntry(x Entry) prompts.KBDigestEntry {
	return prompts.KBDigestEntry{ID: x.ID, Name: x.Name, Importance: x.Importance, OneLine: x.OneLine, Relevance: x.Relevance}
}

// carryOver gives an update of a stored item what the source does not send (tags, projects, note, first seen); the
// item is tagged again, as it changed. The Tagging it copies carries the mark that it was read from disk, so a write
// after the item was deleted meanwhile (a prune, a forget) does not bring it back (putEntity skips a persisted item
// that is no longer there).
func carryOver(it, old *Item) {
	it.Tagging, it.Projects, it.Note, it.Seen = old.Tagging, old.Projects, old.Note, old.Seen
	it.Tagging.TaggedAt = time.Time{}
}
