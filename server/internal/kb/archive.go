package kb

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Defined in the contract package kbwire (docs/server/architecture.md "Contract packages"); the old names stay.
type (
	SourceChat    = kbwire.SourceChat
	SourceMessage = kbwire.SourceMessage
)

// Source is where chats come from (Feishu through the Lark extension).
type Source interface {
	Name() string // "lark"
	Chats(ctx context.Context, limit int) ([]SourceChat, error)
	// Messages returns messages newer than after (the latest page when after is ""), oldest first,
	// with the cursor to continue from.
	Messages(ctx context.Context, chat SourceChat, after string, limit int) (msgs []SourceMessage, newer string, more bool, err error)
	// Download saves one media path to dst.
	Download(ctx context.Context, media, dst string) error
}

// attributeChat keeps a connector's ownership separate from its archive-source name.
// Once attributed, another connector cannot append to or take over that chat.
func attributeChat(c *Chat, src Source) error {
	owner := src.Name()
	if named, ok := src.(interface{ ConnectorID() string }); ok && named.ConnectorID() != "" {
		owner = named.ConnectorID()
	}
	if c.Connector != "" && c.Connector != owner {
		return fmt.Errorf("archive connector mismatch for %s", c.ID)
	}
	c.Connector = owner
	return nil
}

// Engine runs the pipeline: archive, compress, evolve, tag, prune (docs/assistant/knowledge.md).
type Engine struct {
	// Changed is installed on Store by ObserveChanges before the pipeline starts.
	Changed func(Change)
	// MineObserved reports only owner identity metadata, never message contents.
	MineObserved func(source, name string, handles []Handle)
	Store        *Store
	Sources      []Source
	Model        Model
	ModelFor     func(string) Model
	Owner        string // how the owner is called (the AI writes the Chinese "I" for them)
	Logf         func(string, ...any)
	// ExtraDirs are directories outside the data dir that Search also reads (Markdown files, read only): notes the
	// user keeps elsewhere, e.g. a git repository of facts. Absolute paths.
	ExtraDirs []string
	// Index mirrors an entity into the hub's Postgres index (optional).
	Index func(kind, id, name string, t Tagging, oneLine string, updated time.Time)
	// Unindex removes index rows (recordings deleted or past their retention; optional).
	Unindex func(kind string, ids []string)
	// RecordingDaysFunc is how many days recordings are kept (the assistant's recording_days; nil or <= 0: 14).
	RecordingDaysFunc func() int
	// HandleKey is the hub's key for the senders' identity hints (handles.go); without it none are kept.
	HandleKey []byte

	// Limits (zero: defaults).
	ChatsPerRun  int // how many recently active chats an archive run looks at (100)
	FirstPage    int // how many messages a chat seen for the first time starts with (100)
	PagesPerChat int // pages read per chat and run (10)
	// AttachmentDays: attachments of older messages are not downloaded (they would be pruned anyway; 30).
	AttachmentDays int
	// AttachmentDaysBySource: AttachmentDays for the chats of one source (kb.attachment_days_by_source), e.g. years
	// for a chat source whose history is imported with its images; non-positive values are ignored.
	AttachmentDaysBySource map[string]int
	// TextDays: older messages are not written at all (365). Prune deletes such days, and anything that wrote them
	// again (a chat's first page, a mailbox's first read, imported history) would bring them back after every prune.
	TextDays int

	// failed: attachment downloads that failed this run (logged once per chat); atomic, as a history import
	// (WriteHistory) may download while the archive runs.
	failed atomic.Int32

	find findCache // what Find read last time, kept while the files are unchanged (find.go)

	projectRetries map[string]projectRetry // projects whose rewrite failed, by slug (RefreshProjects)
}

func (e *Engine) modelFor(id string) Model {
	if e.ModelFor != nil {
		return e.ModelFor(id)
	}
	return e.Model
}

func (e *Engine) logf(f string, a ...any) {
	if e.Logf != nil {
		e.Logf("kb: "+f, a...)
	}
}

func (e *Engine) index(kind, id, name string, t Tagging, one string, updated time.Time) {
	if e.Index != nil {
		e.Index(kind, id, name, t, one, updated)
	}
}

func or(n, d int) int {
	if n <= 0 {
		return d
	}
	return n
}

// textCutoff is the start of the oldest day Prune keeps (in the archive's zone): a message before it is past
// TextDays and is not written.
func (e *Engine) textCutoff() time.Time {
	zone := e.Store.Zone()
	d := time.Now().AddDate(0, 0, -or(e.TextDays, 365)).In(zone)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, zone)
}

// AttachmentDaysFor is how many days the attachments of a source's chats are kept: its AttachmentDaysBySource,
// else AttachmentDays (30).
func (e *Engine) AttachmentDaysFor(source string) int {
	if d := e.AttachmentDaysBySource[source]; d > 0 {
		return d
	}
	return or(e.AttachmentDays, 30)
}

// Prune deletes message days past TextDays and attachment days past each chat's AttachmentDaysFor: the same days
// textCutoff and line stop writing.
func (e *Engine) Prune(now time.Time) int {
	return e.Store.PruneBySource(now, or(e.TextDays, 365), e.AttachmentDaysFor)
}

// ChatID is the knowledge-base id of a source chat.
func ChatID(source, id string) string { return SafeID(source + "-" + id) }

// PersonID is the knowledge-base id of a sender.
func PersonID(source, id string) string { return SafeID(source + "-" + id) }

// Archive reads what is new in the recently active chats of every source and appends it to the day files.
// It returns the ids of chats that got new messages.
func (e *Engine) Archive(ctx context.Context) ([]string, error) {
	var changed []string
	for _, src := range e.Sources {
		chats, err := src.Chats(ctx, or(e.ChatsPerRun, 100))
		if err != nil {
			return changed, fmt.Errorf("%s chats: %w", src.Name(), err)
		}
		for _, sc := range chats {
			if ctx.Err() != nil {
				return changed, ctx.Err()
			}
			e.failed.Store(0)
			n, err := e.archiveChat(ctx, src, sc)
			if n := e.failed.Load(); n > 0 {
				e.logf("%s: %d attachment(s) not downloaded", sc.Name, n)
			}
			if err != nil {
				e.logf("archive %s %s: %v", src.Name(), sc.ID, err)
				continue
			}
			if n > 0 {
				changed = append(changed, ChatID(src.Name(), sc.ID))
			}
		}
	}
	return changed, nil
}

func (e *Engine) archiveChat(ctx context.Context, src Source, sc SourceChat) (int, error) {
	id := ChatID(src.Name(), sc.ID)
	c, ok := e.Store.Chat(id)
	if !ok {
		c = &Chat{ID: id, Source: src.Name(), SourceID: sc.ID, Kind: sc.Kind, Name: sc.Name, Created: time.Now().UTC()}
	}
	if err := attributeChat(c, src); err != nil {
		return 0, err
	}
	if sc.Name != "" {
		c.Name = sc.Name
	}
	if sc.Kind != "" {
		c.Kind = sc.Kind
	}
	// Nothing new since the last run (the list's time is the last message's).
	if ok && !sc.UpdatedAt.IsZero() && !sc.UpdatedAt.After(c.LastAt) {
		return 0, nil
	}
	cursor := c.Cursor
	limit := 100
	if cursor == "" {
		limit = or(e.FirstPage, 100)
	}
	total := 0
	cut := e.textCutoff()
	for page := 0; page < or(e.PagesPerChat, 10); page++ {
		msgs, newer, more, err := src.Messages(ctx, sc, cursor, limit)
		if err != nil {
			// Earlier pages are in the day files already: keep their cursor, or the next run writes them again.
			if total > 0 {
				if serr := e.saveArchived(c, cursor, total, sc.UpdatedAt); serr != nil {
					e.logf("archive %s: save progress: %v", c.ID, serr)
				}
			}
			return total, err
		}
		var lines []Line
		lastAt := c.LastAt
		for _, m := range msgs {
			// Already archived: the cursor's own message, or (topic groups, read from the newest page each time) anything
			// not newer than the last archived message. Past the retention: the newest page of a quiet chat goes back years.
			if m.Position != "" && m.Position == cursor || c.Messages > 0 && !m.At.After(c.LastAt) || m.At.Before(cut) {
				continue
			}
			l := e.line(ctx, src.Download, c, m)
			if l.Text == "" {
				continue
			}
			lines = append(lines, l)
			e.notePerson(c, src.Name(), m)
			c.Mine = c.Mine || m.Mine
			if m.At.After(c.LastAt) {
				c.LastAt = m.At
			}
		}
		if err := e.Store.AppendMessages(c, lines); err != nil {
			// This page is not archived: LastAt goes back to before it, or the next run skips its messages as seen.
			// The earlier pages are, so keep their cursor as when a fetch fails.
			c.LastAt = lastAt
			if total > 0 {
				if serr := e.saveArchived(c, cursor, total, sc.UpdatedAt); serr != nil {
					e.logf("archive %s: save progress: %v", c.ID, serr)
				}
			}
			return total, err
		}
		total += len(lines)
		if newer != "" {
			cursor = newer
		}
		// Topic groups only page backwards: one page per run, the newest.
		if !more || sc.Topic || newer == "" {
			break
		}
	}
	return total, e.saveArchived(c, cursor, total, sc.UpdatedAt)
}

// saveArchived records how far a chat's archive got: the cursor to go on from and the n lines written. c is the copy
// the run started from and changed as it read; the chat is read again under chatMu and gets only what the run owns
// (name, kind, cursor, LastAt, the owner's part, the people it noted, the counters), so what a history import or a
// compression wrote meanwhile stays. c gets the chat as written.
func (e *Engine) saveArchived(c *Chat, cursor string, n int, listed time.Time) error {
	e.Store.chatMu.Lock()
	defer e.Store.chatMu.Unlock()
	if err := e.Store.recoverIngest(c.ID); err != nil {
		return err
	}
	cur, ok := e.Store.Chat(c.ID)
	if !ok {
		cur = c
		cur.Messages, cur.Pending = 0, 0
	} else {
		cur.Name, cur.Kind = c.Name, c.Kind
		cur.Mine = cur.Mine || c.Mine
		for _, p := range c.People {
			cur.People = addUnique(cur.People, p)
		}
		if c.LastAt.After(cur.LastAt) {
			cur.LastAt = c.LastAt
		}
	}
	cur.Cursor = cursor
	cur.Messages += n
	cur.Pending += n
	if cur.LastAt.IsZero() {
		cur.LastAt = listed
	}
	if err := e.Store.PutChat(cur); err != nil {
		return err
	}
	*c = *cur
	e.index(KindChat, c.ID, c.Name, c.Tagging, c.OneLine, c.LastAt)
	return nil
}

var docLink = lazyre.New(`https?://[\w.-]+\.(?:feishu\.cn|larksuite\.com|larkoffice\.com)/(?:docx|docs|wiki|sheets|base|mindnotes|slides)/[A-Za-z0-9]+`)

// DownloadFunc saves one media path of a message to dst (Source.Download, or a connector's attachments).
type DownloadFunc func(ctx context.Context, media, dst string) error

// line turns a message into an archive line; images and files are saved under attachments/<day>/ while the message
// is within its source's attachment days (AttachmentDaysFor). download nil: none are.
func (e *Engine) line(ctx context.Context, download DownloadFunc, c *Chat, m SourceMessage) Line {
	return e.buildLine(ctx, download, c, m, false)
}

// buildLine is line; history (WriteHistory) downloads images only, writes a placeholder naming the kind of any other
// attachment, and notes no documents (imported history is archive only: nothing for the model to tag).
func (e *Engine) buildLine(ctx context.Context, download DownloadFunc, c *Chat, m SourceMessage, history bool) Line {
	sender := m.SenderName
	if m.Mine {
		sender = "我" // i18n-ignore: archive data
	}
	if sender == "" {
		sender = "?"
	}
	text := strings.TrimSpace(m.Text)
	if m.Type == "SYSTEM" && text == "" {
		return Line{}
	}
	day := m.At.In(e.Store.Zone()).Format("2006-01-02")
	recent := time.Since(m.At) <= time.Duration(e.AttachmentDaysFor(c.Source))*24*time.Hour
	if history {
		image := isImage(m.Type)
		if !image && len(m.Media) == 0 && text == "" {
			if p := mediaPlaceholder(m.Type); p != attachmentPlaceholder {
				text = p // a voice message or video that came without its media reference
			}
		}
		for i, media := range m.Media {
			if !image {
				text += " " + mediaPlaceholder(m.Type)
				continue
			}
			text += " " + e.attach(ctx, download, c, m, i, media, day, recent)
		}
		return Line{At: m.At, Sender: sender, Text: strings.TrimSpace(text)}
	}
	if !recent {
		if len(m.Media) > 0 {
			text += " " + attachmentPlaceholder
		}
		return Line{At: m.At, Sender: sender, Text: strings.TrimSpace(text)}
	}
	for i, media := range m.Media {
		text += " " + e.attach(ctx, download, c, m, i, media, day, true)
	}
	for _, u := range docLink.FindAllString(text, 5) {
		e.noteDoc(u, c, m.At)
	}
	return Line{At: m.At, Sender: sender, Text: text}
}

// attachmentPlaceholder stands for an attachment that is not saved (too old, not downloaded, or failed).
const attachmentPlaceholder = "[附件]" // i18n-ignore: archive data

// attach saves a message's i-th media under attachments/<day>/messages/ and returns the link the line gets, or the
// placeholder when it is not saved.
func (e *Engine) attach(ctx context.Context, download DownloadFunc, c *Chat, m SourceMessage, i int, media, day string, recent bool) string {
	if !recent || download == nil {
		return attachmentPlaceholder
	}
	name := attachmentName(m.ID, i, media, m.Type)
	dst := e.Store.path(KindChat, c.ID, "attachments", day, "messages", name)
	if err := download(ctx, media, dst); err != nil {
		e.failed.Add(1)
		return attachmentPlaceholder
	}
	rel, _ := filepath.Rel(e.Store.path(KindChat, c.ID), dst)
	return fmt.Sprintf("%s(%s)", attachmentPlaceholder, rel)
}

// A filename must keep the raw identity, including punctuation, case and long
// suffixes. Lowercase full hashes stay distinct on case-insensitive filesystems.
// Missing IDs have no replay identity: give each delivery's attachment its own
// name instead of overwriting the previous anonymous message's file. Existing
// links and files keep their names; no legacy filename is reused or rewritten.
func attachmentName(id string, i int, media, typ string) string {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
	if id == "" {
		key = "anonymous-" + strings.ToLower(rand.Text())
	}
	return fmt.Sprintf("message-%s-%d%s", key, i, extOf(media, typ))
}

// isImage: a message whose media are pictures (a rich-text post's are its inline images).
func isImage(typ string) bool { return typ == "IMAGE" || typ == "POST" }

// mediaPlaceholder names the kind of an attachment imported history does not download.
func mediaPlaceholder(typ string) string {
	switch typ {
	case "VIDEO", "MEDIA":
		return "[视频]" // i18n-ignore: archive data
	case "FILE":
		return "[文件]" // i18n-ignore: archive data
	case "AUDIO", "VOICE":
		return "[语音]" // i18n-ignore: archive data
	}
	return attachmentPlaceholder
}

func extOf(media, typ string) string {
	if ext := filepath.Ext(strings.SplitN(media, "?", 2)[0]); ext != "" && len(ext) <= 6 {
		return ext
	}
	if typ == "IMAGE" || typ == "POST" {
		return ".jpg"
	}
	return ""
}

// notePerson links the sender and the chat both ways.
func (e *Engine) notePerson(c *Chat, source string, m SourceMessage) {
	if m.Mine {
		if e.MineObserved != nil {
			handles := slices.Clone(m.SenderHandles)
			if m.SenderID != "" {
				handles = append(handles, Handle{Kind: source, Value: m.SenderID})
			}
			e.MineObserved(source, m.SenderName, handles)
		}
		return
	}
	if m.SenderID == "" {
		return
	}
	pid := PersonID(source, m.SenderID)
	p, ok := e.Store.Person(pid)
	if !ok {
		p = &Person{ID: pid, Source: source, SourceID: m.SenderID, Name: m.SenderName, Connector: c.Connector}
	}
	if p.Connector != "" && p.Connector != c.Connector {
		return
	}
	attributed := false
	if p.Connector == "" && c.Connector != "" {
		known := len(p.Chats) > 0
		for _, id := range p.Chats {
			if id == c.ID {
				continue
			}
			other, exists := e.Store.Chat(id)
			if !exists || other.Connector != c.Connector {
				known = false
				break
			}
		}
		if known {
			p.Connector = c.Connector
			attributed = true
		}
	}
	if m.SenderName != "" {
		p.Name = m.SenderName
	}
	before, seen := len(p.Chats), p.LastSeen
	p.Chats = addUnique(p.Chats, c.ID)
	if m.At.After(p.LastSeen) {
		p.LastSeen = m.At
	}
	c.People = addUnique(c.People, pid)
	e.addHandles(pid, m.SenderHandles)
	// Only write when something a reader cares about changed (not on every message of a busy group).
	if !ok || attributed || len(p.Chats) != before || p.LastSeen.Sub(seen) > time.Hour {
		if err := e.Store.PutPerson(p); err == nil {
			e.index(KindPerson, p.ID, p.Name, p.Tagging, p.Note, p.LastSeen)
		}
	}
}

// noteDoc remembers a document link seen in a chat.
func (e *Engine) noteDoc(u string, chat *Chat, at time.Time) {
	id := SafeID(u[strings.LastIndex(u, "/")+1:])
	if _, ok := e.Store.Item(KindDoc, id); ok {
		return
	}
	_ = e.Store.PutItem(&Item{Kind: KindDoc, ID: id, URL: u, At: at, Where: chat.ID, Source: chat.Connector, Seen: time.Now().UTC()})
}
