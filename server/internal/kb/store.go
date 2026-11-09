// Package kb is Persona' knowledge base under ~/.persona/data (docs/assistant/knowledge.md): every chat's full text by day,
// AI-compressed context per chat, person and project, and the tags (importance, relevance, free tags) that rank them.
// Files are the source of truth so that Claude Code (the assistant, the inbox agent, any herdr agent) can read and
// grep them directly; the hub keeps only an index in Postgres.
package kb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Kinds of entities, also the directory names under the data dir.
const (
	KindChat    = "chats"
	KindPerson  = "people"
	KindProject = "projects"
	KindDoc     = "docs"
	KindMeeting = "meetings"
	KindEvent   = "events"
)

// Tagging ranks anything in the knowledge base. The AI fills it in; Locked ones were set by hand (the app or a
// kb_update call) and the AI no longer overrides importance / relevance.
type Tagging struct {
	Importance string    `json:"importance,omitempty"` // P0 (critical) … P3 (ignorable)
	Relevance  int       `json:"relevance"`            // 0–100: how much it concerns the owner
	Tags       []string  `json:"tags,omitempty"`
	Locked     bool      `json:"locked,omitempty"`
	TaggedAt   time.Time `json:"tagged_at,omitempty"`
	// Pinned contains confirmed facts; pipeline writes cannot replace or clear it.
	Pinned string `json:"pinned,omitempty"`
	// LockedFields names update fields that automatic writes must preserve.
	LockedFields []string `json:"locked_fields,omitempty"`
	// Provenance: the extension holding a locked field (attribution.go); without an entry a locked field is the user's.
	Provenance map[string]Provenance `json:"provenance,omitempty"`
	persisted  bool
}

// Chat is one conversation: a Feishu chat (lark-<feedId>) or the mail exchanged with one address (mail-<account>-<addr>).
type Chat struct {
	ID        string   `json:"id"`
	Source    string   `json:"source"` // lark | mail
	SourceID  string   `json:"source_id"`
	Connector string   `json:"connector,omitempty"` // the connector that wrote this archive source
	Kind      string   `json:"kind"`                // dm | group | bot | mail
	Name      string   `json:"name"`
	People    []string `json:"people,omitempty"`
	Projects  []string `json:"projects,omitempty"`
	Tagging
	// Archive state: the newest position read, when the newest archived message was written, how many lines so far.
	Cursor   string    `json:"cursor,omitempty"`
	LastAt   time.Time `json:"last_at,omitempty"`
	Messages int       `json:"messages"`
	Mine     bool      `json:"mine,omitempty"` // the owner wrote in it (a mail exchange, not just mail received)
	// HistoryMode: how its older history was imported (WriteHistory): HistoryFull, every message; HistoryAnchor, only
	// windows around the owner's messages, the gaps marked by an omission line. "": none imported.
	HistoryMode string `json:"history_mode,omitempty"`
	// Rule is the selection rule that decides a plain message of the chat (Selection.UsualRule), as last written; the
	// retention ledger records only lines another rule decided (retention.go). "": none yet, or outside the selection.
	Rule string `json:"rule,omitempty"`
	// Oldest and BackfillDone were an earlier backfill's progress; kept so files that have them still read the same.
	// History imports keep theirs under history/state/ (HistoryState).
	Oldest       string `json:"oldest,omitempty"`
	BackfillDone bool   `json:"backfill_done,omitempty"`
	// Compression state: messages archived since the last summary, and the time of the last one it covered.
	Pending      int       `json:"pending"`
	CompressedAt time.Time `json:"compressed_at,omitempty"`
	CoveredUntil time.Time `json:"covered_until,omitempty"`
	OneLine      string    `json:"one_line,omitempty"` // the summary's first line, for lists
	TagRev       int       `json:"tag_rev,omitempty"`  // the tagging rules it was last tagged under (compress.go tagRev)
	Created      time.Time `json:"created"`

	// Compressions that failed in a row, and when the next try is due (compress.go compressFailed).
	CompressFailures int       `json:"compress_failures,omitempty"`
	CompressRetryAt  time.Time `json:"compress_retry_at,omitzero"`
}

// Person is someone the owner talks to: lark-<userId> or mail-<address>.
type Person struct {
	ID        string    `json:"id"`
	Source    string    `json:"source"`
	SourceID  string    `json:"source_id"`
	Connector string    `json:"connector,omitempty"`
	Name      string    `json:"name"`
	Note      string    `json:"note,omitempty"` // who they are to the owner, one line (AI)
	Chats     []string  `json:"chats,omitempty"`
	Projects  []string  `json:"projects,omitempty"`
	LastSeen  time.Time `json:"last_seen,omitempty"`
	Tagging
}

// Project is what a set of people and chats work towards: a short one (a feature, a launch) or a long one (a team's mission).
type Project struct {
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`   // short | long
	Status string `json:"status"` // active | paused | done | ignored (not a project: never revived, hidden) | merged
	// MergedInto: merged (DedupeProjects, or by hand) into this project; kept for its history, hidden from lists.
	MergedInto string   `json:"merged_into,omitempty"`
	Aliases    []string `json:"aliases,omitempty"`
	Goal       string   `json:"goal,omitempty"`
	MyRole     string   `json:"my_role,omitempty"`
	People     []string `json:"people,omitempty"`
	Chats      []string `json:"chats,omitempty"`
	Items      []string `json:"items,omitempty"` // "<kind>/<id>" of docs, meetings, events
	Dirs       []string `json:"dirs,omitempty"`  // code directories ("<machine>:<path>"): Agents working there belong to it
	// Keywords: words the project goes by in public talk (product, feature names), from the project rewrite; what
	// finds its Moments posts. Moments are the posts the model judged to be about it (LinkMoments).
	Keywords []string        `json:"keywords,omitempty"`
	Moments  []ProjectMoment `json:"moments,omitempty"`
	Merge    *ProjectMerge   `json:"merge,omitempty"` // merged projects: what the merge gave MergedInto, for unmerge
	Dirty    bool            `json:"dirty,omitempty"` // has news its summary does not reflect yet
	OneLine  string          `json:"one_line,omitempty"`
	Created  time.Time       `json:"created"`
	Updated  time.Time       `json:"updated"`
	Tagging
}

// ProjectMoment is a Moments post about a project, with the model's one line on how.
type ProjectMoment struct {
	ID   string    `json:"id"`
	Note string    `json:"note,omitempty"`
	At   time.Time `json:"at"`
}

// ProjectMerge is a merge as the project merged away records it (MergeProjects), so unmergeProject can undo it: its
// own status before, and what the merge added to the project kept. Merges made before it was recorded have none.
type ProjectMerge struct {
	At       time.Time `json:"at"`
	Status   string    `json:"status,omitempty"`
	Aliases  []string  `json:"aliases,omitempty"`
	People   []string  `json:"people,omitempty"`
	Chats    []string  `json:"chats,omitempty"`
	Items    []string  `json:"items,omitempty"`
	Dirs     []string  `json:"dirs,omitempty"`
	Keywords []string  `json:"keywords,omitempty"`
	Moments  []string  `json:"moments,omitempty"` // post ids
	// Evicted: posts of the kept project's (its own, or another merged project's) past maxProjectMoments that come
	// back as far as room allows when this merge is undone: those the merge pushed out, and those an unmerge of
	// another project merged into it had no room for.
	Evicted []ProjectMoment `json:"evicted,omitempty"`
	// Moved: the chats, people and items ("<kind>/<id>") that named this project and not the kept one; the merge made
	// them name the kept one instead.
	Moved []string `json:"moved,omitempty"`
	// Pinned: the kept project's pinned facts before a manual merge added this one's; unset when it did not change them.
	Pinned *string `json:"pinned,omitempty"`
	// Locks: the kept project's locks before a manual merge added this one's; unset when it did not change them.
	Locks *MergeLocks `json:"locks,omitempty"`
}

// MergeLocks are a project's locked_fields and legacy locked.
type MergeLocks struct {
	Fields []string `json:"fields,omitempty"`
	Locked bool     `json:"locked,omitempty"`
}

// Live: a project lists and is named (not merged into another).
func (p *Project) Live() bool { return p.Status != "merged" }

// Item is a document, meeting or calendar event the owner came across.
type Item struct {
	Kind     string    `json:"kind"`             // docs | meetings | events
	Source   string    `json:"source,omitempty"` // the connector that supplied the item
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	URL      string    `json:"url,omitempty"`
	At       time.Time `json:"at,omitempty"`
	Where    string    `json:"where,omitempty"` // where it came from (a chat id, "calendar"…)
	Projects []string  `json:"projects,omitempty"`
	Note     string    `json:"note,omitempty"`
	Seen     time.Time `json:"seen"`
	Signals  []string  `json:"signals,omitempty"` // kbwire.Signal*: why it concerns the owner, as the source told
	Tagging
}

// Store reads and writes the data dir. Writes of one entity are serialized; files are replaced atomically.
type Store struct {
	readOnly bool
	Dir      string
	mu       sync.Mutex
	metaMu   sync.Mutex // acquire before mu when both are needed (projectInput reads Log under metaMu)
	applyMu  sync.Mutex

	recMu             sync.Mutex                 // a recording's read-compare-write (recordings.go)
	recFilesMu        sync.RWMutex               // acquire before metaMu for recording snapshots and recovery
	recordingWrite    func(string, []byte) error // per-store candidate fault injection
	recordingReadWait func()                     // per-store read-entry observation for concurrency tests
	recordingRemove   func(string) error         // per-store commit fault injection

	dedupeMu sync.Mutex // projects/.dedupe.json's read-modify-write (dedupe.go)
	ingestMu sync.Mutex // live identity filtering through attachment preparation and commit
	// chatMu: a chat's read-modify-write of meta.json by the pipeline (Ingest, saveArchived, WriteHistory in history.go,
	// and compression's write-back, putCompressed), so none writes back a stale copy over what another wrote. Each
	// reads the chat again under it and changes only the fields it owns. (A person's edits, edit.go, are not under it.)
	chatMu    sync.Mutex
	observeMu sync.RWMutex
	changed   func(Change)
	zone      *time.Location // the user's zone, the archive's days and times are written in (zone.go); nil: time.Local
	// settled: the zone every file was last moved into without an error (Rezone skips the walk while it holds).
	settled string
	// ingestWrite is a per-store failure-injection seam; nil uses fsync and atomic replacement.
	ingestWrite func(string, []byte) error
}

func NewStore(dir string) (*Store, error) {
	for _, k := range []string{KindChat, KindPerson, KindProject, KindDoc, KindMeeting, KindEvent, KindRecording} {
		if err := os.MkdirAll(filepath.Join(dir, k), 0o700); err != nil {
			return nil, err
		}
	}
	s := &Store{Dir: dir}
	if err := s.recoverRecordings(); err != nil {
		return nil, err
	}
	if err := s.recoverIngests(); err != nil {
		return nil, err
	}
	_ = writeIfMissing(filepath.Join(dir, "README.md"), prompts.RenderKBReadme)
	return s, nil
}

var unsafeID = lazyre.New(`[^\p{L}\p{N}._@+-]+`)

// SafeID turns a source id into a directory name.
func SafeID(s string) string {
	s = unsafeID.ReplaceAllString(s, "_")
	s = strings.Trim(s, "._")
	if len(s) > 120 {
		// Cut back to a whole character: a name that is not valid UTF-8 cannot be created on macOS.
		s = strings.ToValidUTF8(s[:120], "")
	}
	return s
}

func (s *Store) path(kind, id string, parts ...string) string {
	return filepath.Join(append([]string{s.Dir, kind, SafeID(id)}, parts...)...)
}

func (s *Store) Path(kind, id string, parts ...string) string { return s.path(kind, id, parts...) }

// --- JSON entities ---

func readJSON(p string, out any) (bool, error) {
	raw, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(raw, out)
}

func writeJSON(p string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(p, append(raw, '\n'))
}

func writeFile(p string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// writeIfMissing writes what content returns to p unless p exists. content runs only then: the README renders a prompt
// template, which a store opened on an existing directory (every manager start) need not parse (#2475).
func writeIfMissing(p string, content func() string) error {
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	return writeFile(p, []byte(content()))
}

func (s *Store) Chat(id string) (*Chat, bool) {
	var c Chat
	ok, err := readJSON(s.path(KindChat, id, "meta.json"), &c)
	if !ok || err != nil {
		return nil, false
	}
	c.persisted = true
	return &c, true
}

func (s *Store) PutChat(c *Chat) error { return s.putEntity(KindChat, c.ID, c, &c.Tagging, false) }

func (s *Store) Person(id string) (*Person, bool) {
	var p Person
	ok, err := readJSON(s.path(KindPerson, id, "meta.json"), &p)
	if !ok || err != nil {
		return nil, false
	}
	p.persisted = true
	return &p, true
}

func (s *Store) PutPerson(p *Person) error {
	return s.putEntity(KindPerson, p.ID, p, &p.Tagging, false)
}

func (s *Store) Project(slug string) (*Project, bool) {
	var p Project
	ok, err := readJSON(s.path(KindProject, slug, "meta.json"), &p)
	if !ok || err != nil {
		return nil, false
	}
	p.persisted = true
	return &p, true
}

func (s *Store) PutProject(p *Project) error {
	p.Updated = time.Now().UTC()
	return s.putEntity(KindProject, p.Slug, p, &p.Tagging, false)
}

func (s *Store) Item(kind, id string) (*Item, bool) {
	var it Item
	ok, err := readJSON(filepath.Join(s.Dir, kind, SafeID(id)+".json"), &it)
	if !ok || err != nil {
		return nil, false
	}
	it.persisted = true
	return &it, true
}

func (s *Store) PutItem(it *Item) error {
	return s.putEntity(it.Kind, it.ID, it, &it.Tagging, false)
}

// IDs lists the entities of a kind (directory names, or file names without .json for items).
func (s *Store) IDs(kind string) []string {
	entries, _ := os.ReadDir(filepath.Join(s.Dir, kind))
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			out = append(out, name)
		} else if id, ok := strings.CutSuffix(name, ".json"); ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Store) Chats() []*Chat {
	var out []*Chat
	for _, id := range s.IDs(KindChat) {
		if c, ok := s.Chat(id); ok {
			out = append(out, c)
		}
	}
	return out
}

func (s *Store) People() []*Person {
	var out []*Person
	for _, id := range s.IDs(KindPerson) {
		if p, ok := s.Person(id); ok {
			out = append(out, p)
		}
	}
	return out
}

func (s *Store) Projects() []*Project {
	var out []*Project
	for _, id := range s.IDs(KindProject) {
		if p, ok := s.Project(id); ok {
			out = append(out, p)
		}
	}
	return out
}

func (s *Store) Items(kind string) []*Item {
	var out []*Item
	for _, id := range s.IDs(kind) {
		if it, ok := s.Item(kind, id); ok {
			out = append(out, it)
		}
	}
	return out
}

// --- text files ---

// Summary is the compressed context of a chat, person or project ("" when there is none yet).
func (s *Store) Summary(kind, id string) string {
	raw, _ := os.ReadFile(s.path(kind, id, "summary.md"))
	return string(raw)
}

func (s *Store) PutSummary(kind, id, text string) error {
	return s.putSummary(kind, id, text, false)
}

// AppendLog adds a line to a project's timeline (its time in the archive's zone).
func (s *Store) AppendLog(slug string, at time.Time, source, line string) (err error) {
	if s.readOnly {
		return ErrReadOnly
	}
	s.mu.Lock()
	defer func() {
		s.mu.Unlock()
		if err == nil {
			s.notify(KindProject, slug, "log")
		}
	}()
	if _, err := s.settleLogLocked(slug); err != nil {
		return err
	}
	zone := s.zoneLocked()
	return appendFile(s.path(KindProject, slug, "log.md"), logHeader(zone),
		fmt.Sprintf("- %s · %s · %s\n", at.In(zone).Format("2006-01-02 15:04"), source, oneLine(line)))
}

// Log returns the last n lines of a project's timeline.
func (s *Store) Log(slug string, n int) []string {
	s.mu.Lock()
	_, _ = s.settleLogLocked(slug)
	s.mu.Unlock()
	raw, _ := os.ReadFile(s.path(KindProject, slug, "log.md"))
	return lastLogLines(raw, n)
}

// lastLogLines are the last n "- " lines of a timeline, oldest first, taken from its end so that the lines before them
// are never collected. nil when it has none; empty when n <= 0 and it has one.
func lastLogLines(raw []byte, n int) []string {
	var lines []string
	for end := len(raw); end >= 0; {
		start := bytes.LastIndexByte(raw[:end], '\n') + 1
		if l := raw[start:end]; bytes.HasPrefix(l, []byte("- ")) {
			if n <= 0 {
				return []string{}
			}
			lines = append(lines, string(l))
			if len(lines) == n {
				break
			}
		}
		end = start - 1
	}
	slices.Reverse(lines)
	return lines
}

func appendFile(p, header, text string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() == 0 && header != "" {
		if _, err := f.WriteString(header); err != nil {
			return err
		}
	}
	_, err = f.WriteString(text)
	return err
}

// Line is one archived message.
type Line struct {
	At     time.Time
	Sender string
	Text   string
}

// Format renders a message line: "- 14:03 Alice: text" (the time in zone), continuation lines indented.
func (l Line) Format(zone *time.Location) string {
	text := strings.TrimSpace(l.Text)
	text = strings.ReplaceAll(text, "\n", "\n  ")
	// A sender is one line: a line break in a name would split the message into lines no reader takes for one.
	sender := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(l.Sender)
	return fmt.Sprintf("- %s %s: %s\n", l.At.In(zone).Format("15:04"), sender, text)
}

// AppendMessages writes lines to their day files (messages/YYYY-MM-DD.md, in the archive's zone).
func (s *Store) AppendMessages(c *Chat, lines []Line) error {
	if s.readOnly {
		return ErrReadOnly
	}
	s.mu.Lock()
	written := false
	defer func() {
		s.mu.Unlock()
		if written {
			s.notify(KindChat, c.ID, "messages")
		}
	}()
	if _, err := s.settleChatLocked(c.ID); err != nil {
		return err
	}
	zone := s.zoneLocked()
	for _, l := range lines {
		day := l.At.In(zone).Format("2006-01-02")
		header := fmt.Sprintf("# %s · %s\n\n", c.Name, day)
		if err := appendFile(s.path(KindChat, c.ID, "messages", day+".md"), header, l.Format(zone)); err != nil {
			return err
		}
		written = true
	}
	return nil
}

// Days lists a chat's day files, oldest first ("2006-01-02").
func (s *Store) Days(chatID string) []string {
	entries, _ := os.ReadDir(s.path(KindChat, chatID, "messages"))
	var out []string
	for _, e := range entries {
		if d, ok := strings.CutSuffix(e.Name(), ".md"); ok {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// Since returns the message lines written at or after t (minute precision, as stored), at most max characters,
// the newest kept when it has to cut.
func (s *Store) Since(chatID string, t time.Time, max int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.settleChatLocked(chatID)
	zone := s.chatReadZoneLocked(chatID)
	from := t.In(zone).Format("2006-01-02")
	var b strings.Builder
	for _, d := range s.Days(chatID) {
		if d < from {
			continue
		}
		raw, _ := os.ReadFile(s.path(KindChat, chatID, "messages", d+".md"))
		// The kept messages' lines, joined once below: appending each continuation line to its message would copy
		// the whole message again per line, quadratic in a long paste.
		var keep []string
		open := false // the last "- " line was kept, so the indented lines that continue it are too
		for _, l := range strings.Split(string(raw), "\n") {
			switch {
			case strings.HasPrefix(l, "- "):
				open = false
				if len(l) >= 7 {
					if at, err := time.ParseInLocation("2006-01-02 15:04", d+" "+l[2:7], zone); err == nil && at.Before(t.Truncate(time.Minute)) {
						continue
					}
				}
				keep = append(keep, l)
				open = true
			case strings.HasPrefix(l, "  ") && open:
				keep = append(keep, l)
			default:
				open = false
			}
		}
		if len(keep) > 0 {
			fmt.Fprintf(&b, "## %s\n%s\n", d, strings.Join(keep, "\n"))
		}
	}
	out := b.String()
	if len(out) > max {
		out = "…\n" + out[len(out)-max:]
	}
	return out
}

// Prune deletes message days older than textDays and attachment days older than attachmentDays.
func (s *Store) Prune(now time.Time, textDays, attachmentDays int) (removed int) {
	return s.PruneBySource(now, textDays, func(string) int { return attachmentDays })
}

// PruneBySource is Prune with each chat's attachments kept as many days as attachmentDays says for the chat's source
// (Engine.AttachmentDaysFor).
func (s *Store) PruneBySource(now time.Time, textDays int, attachmentDays func(source string) int) (removed int) {
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
	textCut := now.AddDate(0, 0, -textDays).In(zone).Format("2006-01-02")
	for _, id := range s.IDs(KindChat) {
		for _, d := range s.Days(id) {
			if d < textCut && os.Remove(s.path(KindChat, id, "messages", d+".md")) == nil {
				removed++
			}
		}
		_ = s.pruneReceipts(id, textCut, zone)
		entries, _ := os.ReadDir(s.path(KindChat, id, "attachments"))
		if len(entries) == 0 {
			continue
		}
		source := ""
		if c, ok := s.Chat(id); ok {
			source = c.Source
		}
		attCut := now.AddDate(0, 0, -attachmentDays(source)).In(zone).Format("2006-01-02")
		for _, e := range entries {
			if e.IsDir() && e.Name() < attCut && os.RemoveAll(s.path(KindChat, id, "attachments", e.Name())) == nil {
				removed++
			}
		}
	}
	return removed
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// addUnique appends v to list unless it is there already.
func addUnique(list []string, v ...string) []string {
	for _, x := range v {
		if x == "" {
			continue
		}
		found := false
		for _, y := range list {
			if y == x {
				found = true
				break
			}
		}
		if !found {
			list = append(list, x)
		}
	}
	return list
}

// FirstAt is the time of a chat's oldest archived line (zero when there is none).
func (s *Store) FirstAt(chatID string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.settleChatLocked(chatID)
	days := s.Days(chatID)
	if len(days) == 0 {
		return time.Time{}
	}
	raw, _ := os.ReadFile(s.path(KindChat, chatID, "messages", days[0]+".md"))
	for _, l := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(l, "- ") && len(l) >= 7 {
			if t, err := time.ParseInLocation("2006-01-02 15:04", days[0]+" "+l[2:7], s.chatReadZoneLocked(chatID)); err == nil {
				return t
			}
		}
	}
	return time.Time{}
}
