package kb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Imported history: a chat's messages from before the live archive began, read backwards from a chat source and
// written into the day files where they belong (docs/assistant/knowledge.md "History import"). It is archive only: the
// lines are searchable, but they add nothing to Pending (no compression, tagging or project links), never move the
// live archive's Cursor or LastAt, and reach no inbox or reminder. What drives an import (which chats, how fast,
// when to stop) is the assistant's; this file is how its pages are written and how far it got is kept.

// History modes of a chat (Chat.HistoryMode, HistoryState.Mode).
const (
	HistoryFull   = "full"   // every message
	HistoryAnchor = "anchor" // windows around anchors (AnchorFilter), the gaps marked by an omission line
)

// States of a chat's import (HistoryState.State).
const (
	HistoryRunning = "running" // under way; a restart goes on from Cursor
	HistoryDone    = "done"    // reached the start of the conversation or the text retention
	// HistoryPaused: stopped by a temporary failure (pacing, a timeout, the source not ready) or by hand; goes on later.
	HistoryPaused = "paused"
	// HistoryError: a failure that will not pass by itself (the source refused, the login expired); not retried until
	// a person looks.
	HistoryError = "error"
	// HistoryUnreadable: the conversation cannot be read any more (left or dissolved: the source answers not found).
	// Permanent, skipped, and not a failure.
	HistoryUnreadable = "unreadable"
)

// HistoryState is how far a chat's import got, kept in history/state/<chat id>.json so a run goes on after a restart.
type HistoryState struct {
	Source string `json:"source,omitempty"` // the chat's source, so forgetting it finds the file (ForgetSource)
	Mode   string `json:"mode"`
	Cursor string `json:"cursor,omitempty"` // the source's position to read on from (backwards)
	// Read and Written: messages read from the source, and lines that went into the day files (Written counts only
	// what was new: a page read twice adds nothing).
	Read      int       `json:"read"`
	Written   int       `json:"written"`
	State     string    `json:"state"`
	LastError string    `json:"last_error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// Filter: an anchor import's filter between pages (AnchorFilter.State), so it resumes mid-window.
	Filter *AnchorFilterState `json:"filter,omitempty"`
	// Positions: an anchor import that reads windows by position around anchors a source's search found.
	Positions *PositionState `json:"positions,omitempty"`
	// Plan is what the read covers (the selection's ReadPlan, its days bounded by the source's back_days): the
	// narrowest plan it ran under, so a finished read is taken up again only when the selection asks for more.
	Plan *ReadPlan `json:"plan,omitempty"`
}

// PositionState is how far an anchor import by position got, for a source whose positions are consecutive numbers
// and that can search for the anchors (the assistant's driver, assistant/kb_history_anchor.go): the chat's anchors,
// the positions read so far, the times of the messages at the windows' edges (to stamp the omission lines), and the
// calendar years the search found nothing in, which are read backwards through an AnchorFilter afterwards (with
// HistoryState.Cursor and Filter).
type PositionState struct {
	Anchors []PositionAnchor `json:"anchors,omitempty"`
	// Have: the position ranges read so far, merged ([least, most], inclusive).
	Have [][2]int64 `json:"have,omitempty"`
	// Edges: the time of the message at a window's edge, by its position.
	Edges map[int64]time.Time `json:"edges,omitempty"`
	// WindowsDone: every window is read and the gaps between them are marked.
	WindowsDone bool `json:"windows_done,omitempty"`
	// Fallback: the uncovered years still to read backwards, newest first; Year is the one the filter is in.
	Fallback []int `json:"fallback,omitempty"`
	Year     int   `json:"year,omitempty"`
	// Start: the time of the conversation's first message (position 0), once read; Probed: asked for once, so a
	// conversation that does not say is not asked again.
	Start  time.Time `json:"start,omitzero"`
	Probed bool      `json:"probed,omitempty"`
}

// PositionAnchor is one anchor of a chat: its position and time, and whether the owner sent it (the mine rule's
// context is read around it) or it only mentions the owner (only the message itself is kept).
type PositionAnchor struct {
	Pos  int64     `json:"pos"`
	At   time.Time `json:"at,omitzero"`
	Mine bool      `json:"mine,omitempty"`
}

// SearchIndexed says the knowledge base keeps a persistent search index (#1273). Until it does, search reads every
// archived line, which is too slow once millions of imported lines are in: the history import does not start a
// source that needs the index (kb.history.sources.<id>.needs_search_index, true by default). The index work flips it.
const SearchIndexed = false

// Finished: the import of the chat is over and is not taken up again by itself.
func (st HistoryState) Finished() bool {
	return st.State == HistoryDone || st.State == HistoryError || st.State == HistoryUnreadable
}

// historyStateDir holds the import states: under history/, a top-level directory that is no entity kind, so lists,
// search and pruning never see it (searchable).
func (s *Store) historyStateDir() string { return filepath.Join(s.Dir, "history", "state") }

func (s *Store) historyStatePath(chatID string) string {
	return filepath.Join(s.historyStateDir(), SafeID(chatID)+".json")
}

// HistoryState reads a chat's import state; false when there is none. A file that does not read is an error, not
// "none": the import would start over and read the whole chat again.
func (s *Store) HistoryState(chatID string) (HistoryState, bool, error) {
	var st HistoryState
	ok, err := readJSON(s.historyStatePath(chatID), &st)
	if err != nil {
		return HistoryState{}, false, fmt.Errorf("history state of %s: %w", chatID, err)
	}
	return st, ok, nil
}

// PutHistoryState writes a chat's import state (atomically: a temporary file renamed over it). st.Source is required:
// forgetting a source tells its files from those of a source whose name starts the same by it (forgetHistory).
func (s *Store) PutHistoryState(chatID string, st HistoryState) error {
	if s.readOnly {
		return ErrReadOnly
	}
	if st.Source == "" {
		return fmt.Errorf("history state of %s: no source", chatID)
	}
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = time.Now().UTC()
	}
	if st.Filter != nil {
		f := st.Filter.clean()
		st.Filter = &f
	}
	return writeJSON(s.historyStatePath(chatID), st)
}

// forgetHistory removes the import states of a source's chats (ForgetSource): the files named like one of its chats
// that say they are its. A file named so that does not read, or names no source, cannot be told from one of a source
// whose name starts the same ("a" and "a-b") and goes too: its import starts over, which only rereads what is there.
func (s *Store) forgetHistory(source string) error {
	entries, err := os.ReadDir(s.historyStateDir())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	prefix := SafeID(source) + "-"
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
			continue
		}
		var st HistoryState
		p := filepath.Join(s.historyStateDir(), name)
		if ok, err := readJSON(p, &st); ok && err == nil && st.Source != "" && st.Source != source { // another source's
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ErrDownloadAborted is what a HistoryWrite's Download returns (or wraps) to abort the whole write: nothing of the
// batch is written, so its caller can read the same messages again later and download their images then, instead of
// leaving placeholders that a later read could not replace (a line with its image differs from one without).
var ErrDownloadAborted = errors.New("history: the download was aborted")

// HistoryWrite is how WriteHistory writes one batch.
type HistoryWrite struct {
	Mode string // HistoryFull or HistoryAnchor, recorded as the chat's HistoryMode ("": left as it is)
	// Download saves an image (nil: none are). Only images are downloaded, and only within the message's attachment
	// days (the selection's decision, else Engine.AttachmentDaysFor); other attachments become a placeholder naming
	// their kind.
	Download DownloadFunc
	// Chat is the chat's facts for the selection (nil: its source, id and kind as the chat has them, no flags). A
	// chat already filed with a kind the selection does not decide about (ChatFacts.Selected) keeps that kind, as
	// daily ingestion goes by it: everything is written, for text_days.
	Chat *ChatFacts
	// NearMine says a message is within the mine rule's context of one of the owner's (the caller reads the windows
	// around them; nil: none is).
	NearMine func(SourceMessage) bool
}

// TypeOmitted is the type of an omission line (OmissionLine) among the messages an anchor import writes.
const TypeOmitted = "PERSONA_OMITTED"

// omissionSender is the sender an omission line is written with: no one.
const omissionSender = "·"

// OmissionLine is the line that marks n messages an anchor import left out, stamped at time at so it sorts into the
// gap (AnchorFilter stamps it with the oldest message left out). For callers that build windows themselves, e.g. by
// position around anchors a source's search found.
func OmissionLine(at time.Time, n int) SourceMessage {
	return SourceMessage{Type: TypeOmitted, SenderName: omissionSender, At: at,
		Text: fmt.Sprintf("[……此处省略 %d 条]", n)} // i18n-ignore: archive data
}

// WriteHistory writes imported messages of a chat into its day files (MergeMessages), returning how many lines were
// new. msgs may come in any order and may repeat what is there (duplicates are dropped). With a selection
// (Engine.Selection) only what it stores is written (Selection.Evaluate, with opt.NearMine for the mine rule's
// context), each line recorded with its rule in the chat's retention ledger; a message past its rule's days (without
// a selection: the text retention) is skipped. Omission lines have the chat's usual rule. c names the chat: by ID,
// or by Source and SourceID, with Kind and Name used when the chat is new. The chat is read again under chatMu and
// only Messages, Mine, People, HistoryMode and Rule change: never Pending, Cursor or LastAt, which the live archive
// and compression go by. c gets the chat as written.
func (e *Engine) WriteHistory(ctx context.Context, c *Chat, msgs []SourceMessage, opt HistoryWrite) (added int, err error) {
	if e.Store.ReadOnly() {
		return 0, ErrReadOnly
	}
	id := c.ID
	if id == "" {
		if c.Source == "" || c.SourceID == "" {
			return 0, errors.New("history: the chat has no id")
		}
		id = ChatID(c.Source, c.SourceID)
	}
	if len(msgs) == 0 {
		return 0, nil
	}
	// Whether the chat can be written at all, before anything is downloaded into its directory.
	source := c.Source
	facts := ChatFacts{ID: c.SourceID, Kind: c.Kind}
	outside := false // a chat already filed with a kind the selection does not decide about, as daily ingestion has it
	if stored, ok := e.Store.Chat(id); ok {
		source = stored.Source
		facts.ID, facts.Kind = stored.SourceID, stored.Kind
		outside = stored.Kind != "" && !facts.Selected()
	} else if source == "" {
		return 0, fmt.Errorf("history: %s is a new chat without its source", id)
	}
	probe := &Chat{ID: id, Source: source}
	sel, ret := e.selection(), e.Retention()
	facts.Source = source
	if opt.Chat != nil && !outside {
		facts = *opt.Chat
		facts.Source = source
	}
	selecting := sel != nil && facts.Selected()
	usual := ""
	if selecting {
		if !sel.ReadChat(ret, facts).Read {
			return 0, nil
		}
		usual = sel.UsualRule(ret, facts)
	}
	decide := func(m SourceMessage) Decision {
		switch {
		case !selecting:
			return Decision{Store: true, Days: or(e.TextDays, 365), AttachmentDays: e.AttachmentDaysFor(source)}
		case m.Type == TypeOmitted:
			text, att := sel.LineDays(ret, source, facts.ID, usual)
			return Decision{Store: true, Rule: usual, Days: text, AttachmentDays: att}
		}
		mf := MessageFactsOf(m)
		mf.NearMine = opt.NearMine != nil && opt.NearMine(m)
		return sel.Evaluate(ret, facts, mf)
	}
	var aborted error
	download := opt.Download
	if download != nil {
		download = func(ctx context.Context, ref, dst string) error {
			if aborted != nil {
				return aborted
			}
			err := opt.Download(ctx, ref, dst)
			if errors.Is(err, ErrDownloadAborted) {
				aborted = err
			}
			return err
		}
	}
	var lines []ingestLine
	var kept []SourceMessage
	for _, m := range msgs {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if aborted != nil {
			return 0, aborted
		}
		d := decide(m)
		if !d.Store || m.At.Before(e.CutoffFor(d.Days)) {
			continue
		}
		var l Line
		if m.Type == TypeOmitted {
			l = Line{At: m.At, Sender: omissionSender, Text: strings.TrimSpace(m.Text)}
		} else {
			l = e.buildLine(ctx, download, probe, m, true, d.AttachmentDays)
			kept = append(kept, m)
		}
		if l.Text != "" {
			lines = append(lines, ingestLine{Line: l, rule: d.Rule})
		}
	}
	if aborted != nil {
		return 0, aborted
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].At.Before(lines[j].At) })

	e.Store.chatMu.Lock()
	defer e.Store.chatMu.Unlock()
	if err := e.Store.recoverIngest(id); err != nil {
		return 0, err
	}
	cur, ok := e.Store.Chat(id)
	if !ok {
		if len(lines) == 0 {
			return 0, nil // nothing to keep: no empty chat
		}
		if c.Source == "" {
			return 0, fmt.Errorf("history: %s is a new chat without its source", id)
		}
		cur = &Chat{ID: id, Source: c.Source, SourceID: c.SourceID, Kind: c.Kind, Name: c.Name, Created: time.Now().UTC()}
	}
	if added, err = e.Store.mergeMessages(cur, lines, selecting, usual); err != nil {
		return added, err
	}
	for _, m := range kept {
		e.notePerson(cur, cur.Source, m)
		cur.Mine = cur.Mine || m.Mine
	}
	cur.Messages += added
	if opt.Mode != "" {
		cur.HistoryMode = opt.Mode
	}
	if selecting {
		cur.Rule = usual
	}
	if err := e.Store.PutChat(cur); err != nil {
		return added, err
	}
	updated := cur.LastAt
	if updated.IsZero() && len(lines) > 0 { // only history so far: the newest line it has
		updated = lines[len(lines)-1].At
	}
	e.index(KindChat, cur.ID, cur.Name, cur.Tagging, cur.OneLine, updated)
	*c = *cur
	return added, nil
}
