package kb

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Forgotten is what ForgetSource removed: the ids of its chats and people (to drop from the index too).
type Forgotten struct {
	Chats, People, Projects []string // Projects: the ones that had them, marked dirty
}

// ForgetSource removes what came from source (a connector's id, "imessage") from the knowledge base, when the user
// takes it back: its chats with their messages and summaries, its people, their places in projects and the lines of
// project timelines its chats wrote, the connector's cursor (cursors/<source>.json: archiving again starts over) and
// its chats' history import states (history/state/, HistoryState: importing again starts over too). A project that
// had them is marked dirty, so its summary is written again from what is left. Nothing else is touched. Callers stop
// the source first: a chat ingested meanwhile would come back.
func (e *Engine) ForgetSource(source string) (Forgotten, error) {
	if e.Store.ReadOnly() {
		return Forgotten{}, ErrReadOnly
	}
	e.Store.applyMu.Lock()
	defer e.Store.applyMu.Unlock()
	e.Store.ingestMu.Lock()
	defer e.Store.ingestMu.Unlock()
	e.Store.chatMu.Lock()
	defer e.Store.chatMu.Unlock()
	if err := e.Store.recoverIngests(); err != nil {
		return Forgotten{}, err
	}
	var out Forgotten
	prefix := source + "-"
	names := map[string]bool{} // the forgotten chats' names: the source of their timeline lines
	for _, c := range e.Store.Chats() {
		if c.Source == source && strings.HasPrefix(c.ID, prefix) {
			out.Chats = append(out.Chats, c.ID)
			names[c.Name] = true
		}
	}
	for _, p := range e.Store.People() {
		if p.Source == source && strings.HasPrefix(p.ID, prefix) {
			out.People = append(out.People, p.ID)
		}
	}
	for _, p := range e.Store.Projects() {
		chats := slices.DeleteFunc(slices.Clone(p.Chats), func(id string) bool { return slices.Contains(out.Chats, id) })
		people := slices.DeleteFunc(slices.Clone(p.People), func(id string) bool { return slices.Contains(out.People, id) })
		if len(chats) == len(p.Chats) && len(people) == len(p.People) {
			continue
		}
		p.Chats, p.People, p.Dirty = chats, people, true
		if err := e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, true); err != nil {
			return out, err
		}
		if err := e.Store.dropLogLines(p.Slug, func(source string) bool { return names[source] }); err != nil {
			return out, err
		}
		out.Projects = append(out.Projects, p.Slug)
	}
	e.Store.metaMu.Lock()
	defer e.Store.metaMu.Unlock()
	for _, id := range out.Chats {
		if err := os.RemoveAll(e.Store.path(KindChat, id)); err != nil {
			return out, err
		}
	}
	for _, id := range out.People {
		if err := os.RemoveAll(e.Store.path(KindPerson, id)); err != nil {
			return out, err
		}
	}
	if err := os.Remove(filepath.Join(e.Store.Dir, "cursors", source+".json")); err != nil && !os.IsNotExist(err) {
		return out, err
	}
	if err := e.Store.forgetHistory(source); err != nil {
		return out, err
	}
	return out, nil
}

// dropLogLines rewrites a project's timeline without the lines whose source ("- time · source · content") gone says.
func (s *Store) dropLogLines(slug string, gone func(source string) bool) (err error) {
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
	p := s.path(KindProject, slug, "log.md")
	raw, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	lines := strings.SplitAfter(string(raw), "\n")
	kept := lines[:0]
	for _, l := range lines {
		if rest, ok := strings.CutPrefix(l, "- "); ok {
			parts := strings.SplitN(rest, " · ", 3)
			if len(parts) == 3 && gone(parts[1]) {
				continue
			}
		}
		kept = append(kept, l)
	}
	return writeFile(p, []byte(strings.Join(kept, "")))
}
