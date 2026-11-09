package kb

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The mine rule's context in daily ingestion (#1523): the context messages before and after each of the owner's own
// count as theirs. Batches come one at a time, so for each chat the last context messages are kept on disk,
// select/<chat id>.json (a top-level directory that is no entity kind: never searched or listed), with how many of the
// next ones still follow an owner's message: a restart loses nothing. Messages kept stored only carry their id; the
// others are kept whole (without the senders' identity hints, which never go to disk) until they are stored with an
// owner's message or fall out of the window.

const selectDir = "select"

// mineWindow is a chat's context between batches.
type mineWindow struct {
	Source string `json:"source"`
	Chat   string `json:"chat"` // the source's chat id
	// Before: the last messages, oldest first, at most the context.
	Before []windowMessage `json:"before,omitempty"`
	// After: how many of the next messages still count as the owner's.
	After int `json:"after,omitempty"`
}

// windowMessage is one message of the window: whole until stored, then only its id.
type windowMessage struct {
	Stored bool          `json:"stored,omitempty"`
	M      SourceMessage `json:"m"`
}

func (s *Store) mineWindowPath(chatID string) string {
	return filepath.Join(s.Dir, selectDir, SafeID(chatID)+".json")
}

// mineWindow reads a chat's context (an empty one when there is none). One of another chat is an error.
func (s *Store) mineWindow(chatID, source, chat string) (mineWindow, error) {
	var w mineWindow
	ok, err := readJSON(s.mineWindowPath(chatID), &w)
	if err != nil {
		return mineWindow{}, fmt.Errorf("selection context of %s: %w", chatID, err)
	}
	if !ok {
		return mineWindow{Source: source, Chat: chat}, nil
	}
	if w.Source != source || w.Chat != chat {
		return mineWindow{}, fmt.Errorf("selection context identity mismatch for %s", chatID)
	}
	return w, nil
}

func (s *Store) putMineWindow(chatID string, w mineWindow) error {
	if len(w.Before) == 0 && w.After == 0 {
		err := os.Remove(s.mineWindowPath(chatID))
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return writeJSON(s.mineWindowPath(chatID), w)
}

// seen: the window holds a message with this id already (a batch delivered again).
func (w *mineWindow) seen(id string) bool {
	if id == "" {
		return false
	}
	for _, x := range w.Before {
		if x.M.ID == id {
			return true
		}
	}
	return false
}

// push adds a message to the window, keeping the last n.
func (w *mineWindow) push(m SourceMessage, stored bool, n int) {
	if n <= 0 {
		w.Before = nil
		return
	}
	if stored {
		m = SourceMessage{ID: m.ID, At: m.At}
	} else {
		m.SenderHandles = nil
	}
	w.Before = append(w.Before, windowMessage{Stored: stored, M: m})
	if len(w.Before) > n {
		w.Before = append([]windowMessage(nil), w.Before[len(w.Before)-n:]...)
	}
}

// forgetSelect removes the contexts of a source's chats (ForgetSource), as forgetHistory its import states.
func (s *Store) forgetSelect(source string) error {
	dir := filepath.Join(s.Dir, selectDir)
	entries, err := os.ReadDir(dir)
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
		var w mineWindow
		p := filepath.Join(dir, name)
		if ok, err := readJSON(p, &w); ok && err == nil && w.Source != "" && w.Source != source {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
