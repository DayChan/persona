package kb

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nwverse/persona/server/internal/managerconf"
)

const (
	ingestJournal  = ".ingest.json"
	ingestReceipts = ".receipts.json"
)

// Receipts belong to the exact source/chat pair, not its sanitized directory name.
// At bounds their lifetime by text retention, including after a timezone change.
type messageReceipts struct {
	Source string               `json:"source"`
	Chat   string               `json:"chat"`
	IDs    map[string]time.Time `json:"ids"`
}

// An intent is installed before any day, receipt or metadata is replaced. Repeating
// these complete replacements is safe even if the process stopped after any one.
// Only one writer owns a Store directory; chatMu, mu and metaMu serialize its callers.
type ingestIntent struct {
	Version  int               `json:"version"`
	ChatID   string            `json:"chat_id"`
	Days     map[string][]byte `json:"days"`
	Receipts messageReceipts   `json:"receipts"`
	Meta     json.RawMessage   `json:"meta"`
}

func archiveWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return managerconf.WriteFileAtomic(path, data, 0o600)
}

func (s *Store) writeIngest(path string, data []byte) error {
	if s.ingestWrite != nil {
		return s.ingestWrite(path, data)
	}
	return archiveWrite(path, data)
}

func (s *Store) receipts(source, chat, id string) (messageReceipts, error) {
	var r messageReceipts
	ok, err := readJSON(s.path(KindChat, id, ingestReceipts), &r)
	if err != nil {
		return r, err
	}
	if !ok {
		return messageReceipts{Source: source, Chat: chat, IDs: map[string]time.Time{}}, nil
	}
	if r.Source != source || r.Chat != chat || r.IDs == nil {
		return r, fmt.Errorf("archive identity mismatch for %s", id)
	}
	return r, nil
}

// ingestState runs under chatMu. Only Ingest writes receipts, under ingestMu;
// cleanup may retire old ones while attachment downloads are in progress.
func (s *Store) ingestState(source string, sc SourceChat, cut time.Time) (*Chat, bool, messageReceipts, error) {
	id := ChatID(source, sc.ID)
	if err := s.recoverIngest(id); err != nil {
		return nil, false, messageReceipts{}, err
	}
	r, err := s.receipts(source, sc.ID, id)
	if err != nil {
		return nil, false, r, err
	}
	var c Chat
	exists, err := readJSON(s.entityPath(KindChat, id), &c)
	if err != nil {
		return nil, false, r, err
	}
	if exists && (c.Source != source || c.SourceID != sc.ID) {
		return nil, false, r, fmt.Errorf("archive identity mismatch for %s", id)
	}
	if !exists {
		c = Chat{ID: id, Source: source, SourceID: sc.ID, Kind: sc.Kind, Name: sc.Name, Created: time.Now().UTC()}
	}
	// A failed receipt cleanup must not suppress a reused ID once its retained
	// text has gone. The next commit persists this in-memory cleanup too.
	zone := s.Zone()
	for key, at := range r.IDs {
		if at.Before(cut) {
			_, err := os.Stat(s.path(KindChat, id, "messages", at.In(zone).Format("2006-01-02")+".md"))
			if os.IsNotExist(err) {
				delete(r.IDs, key)
			}
		}
	}
	return &c, exists, r, nil
}

// pendingIngestLocked prevents a later write from invalidating an unfinished
// intent's snapshots. Readers may see a partial write until retry or writable open
// recovers it; a failed recovery never admits another mutation of that chat.
func (s *Store) pendingIngestLocked(id string) error {
	_, err := os.Stat(s.path(KindChat, id, ingestJournal))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("archive recovery required for %s", id)
}

// recoverIngest is called under chatMu before a pipeline caller reads counters.
func (s *Store) recoverIngest(id string) error {
	s.lockArchive()
	defer s.unlockArchive()
	return s.recoverIngestLocked(id)
}

func (s *Store) recoverIngestLocked(id string) error {
	var in ingestIntent
	ok, err := readJSON(s.path(KindChat, id, ingestJournal), &in)
	if err != nil || !ok {
		return err
	}
	var c Chat
	if err := json.Unmarshal(in.Meta, &c); err != nil {
		return err
	}
	if in.Version != 1 || in.ChatID != id || c.ID != id || c.Source != in.Receipts.Source || c.SourceID != in.Receipts.Chat || in.Receipts.IDs == nil {
		return fmt.Errorf("invalid archive recovery for %s", id)
	}
	for day := range in.Days {
		if _, err := time.Parse("2006-01-02", day); err != nil {
			return fmt.Errorf("invalid archive day %q", day)
		}
	}
	return s.finishIngestLocked(&in)
}

func (s *Store) finishIngestLocked(in *ingestIntent) error {
	days := make([]string, 0, len(in.Days))
	for d := range in.Days {
		days = append(days, d)
	}
	sort.Strings(days)
	for _, d := range days {
		if err := s.writeIngest(s.path(KindChat, in.ChatID, "messages", d+".md"), in.Days[d]); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(in.Receipts)
	if err != nil {
		return err
	}
	if err := s.writeIngest(s.path(KindChat, in.ChatID, ingestReceipts), raw); err != nil {
		return err
	}
	if err := s.writeIngest(s.path(KindChat, in.ChatID, "meta.json"), in.Meta); err != nil {
		return err
	}
	if err := os.Remove(s.path(KindChat, in.ChatID, ingestJournal)); err != nil {
		return err
	}
	// Like WriteFileAtomic, persist the directory change where directory syncing
	// is supported, so a power cut does not resurrect a completed intent.
	if d, err := os.Open(s.path(KindChat, in.ChatID)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// commitIngest holds the day and metadata locks from snapshot through commit. The
// existing protected-field merge also applies here, so manual edits remain intact.
// The caller holds chatMu and has filtered identities before building attachments.
func (s *Store) commitIngest(c *Chat, lines []Line, r messageReceipts) (err error) {
	s.lockArchive()
	defer func() {
		s.unlockArchive()
		if err == nil {
			s.notify(KindChat, c.ID, "messages")
			s.notify(KindChat, c.ID, "metadata")
		}
	}()
	if err := s.pendingIngestLocked(c.ID); err != nil {
		return err
	}
	if _, err := s.settleChatLocked(c.ID); err != nil {
		return err
	}
	in := ingestIntent{Version: 1, ChatID: c.ID, Receipts: r, Days: map[string][]byte{}}
	for _, l := range lines {
		d := l.At.In(s.zoneLocked()).Format("2006-01-02")
		if _, ok := in.Days[d]; !ok {
			raw, err := os.ReadFile(s.path(KindChat, c.ID, "messages", d+".md"))
			if err != nil && !os.IsNotExist(err) {
				return err
			}
			if len(raw) == 0 {
				raw = fmt.Appendf(nil, "# %s · %s\n\n", c.Name, d)
			}
			in.Days[d] = raw
		}
		in.Days[d] = append(in.Days[d], l.Format(s.zoneLocked())...)
	}
	current := map[string]json.RawMessage{}
	if _, err := readJSON(s.entityPath(KindChat, c.ID), &current); err != nil {
		return err
	}
	incoming := protectedEntity(c, &c.Tagging, current, false)
	in.Meta, err = json.MarshalIndent(incoming, "", "  ")
	if err != nil {
		return err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	if err := s.writeIngest(s.path(KindChat, c.ID, ingestJournal), raw); err != nil {
		return err
	}
	if err := s.finishIngestLocked(&in); err != nil {
		return err
	}
	*c = Chat{}
	if err := json.Unmarshal(in.Meta, c); err != nil {
		return err
	}
	c.persisted = true
	return nil
}

// lockArchive follows the existing project commit order: projectInput reads Log
// while metadata is frozen, so no path may hold mu while waiting for metaMu.
func (s *Store) lockArchive() {
	s.metaMu.Lock()
	s.mu.Lock()
}

func (s *Store) unlockArchive() {
	s.mu.Unlock()
	s.metaMu.Unlock()
}

func (s *Store) recoverIngests() error {
	s.lockArchive()
	defer s.unlockArchive()
	return s.recoverIngestsLocked()
}

// recoverIngestsLocked requires both persistence locks, in metadata/day order.
func (s *Store) recoverIngestsLocked() error {
	for _, id := range s.IDs(KindChat) {
		if err := s.recoverIngestLocked(id); err != nil {
			return fmt.Errorf("recover archive %s: %w", id, err)
		}
	}
	return nil
}

// pruneReceipts runs only after the corresponding text day was removed. If its
// write fails the caller keeps retrying on later cleanup, without deleting text
// for any retained receipt. The original timestamp also works across Rezone.
func (s *Store) pruneReceipts(id, cut string, zone *time.Location) error {
	var r messageReceipts
	ok, err := readJSON(s.path(KindChat, id, ingestReceipts), &r)
	if err != nil || !ok {
		return err
	}
	changed := false
	for key, at := range r.IDs {
		if at.In(zone).Format("2006-01-02") < cut {
			// Never retire an identity while its day still exists (failed removal).
			_, err := os.Stat(s.path(KindChat, id, "messages", at.In(zone).Format("2006-01-02")+".md"))
			if errors.Is(err, os.ErrNotExist) {
				delete(r.IDs, key)
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.writeIngest(s.path(KindChat, id, ingestReceipts), raw)
}
