package kb

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

// Forgotten names removed entities so callers can delete their index rows too.
type Forgotten struct {
	Chats, People, Projects []string // Projects: changed associations, marked dirty
	Posts                   []string
	Items                   map[string][]string // kind -> ids
}

// ForgetSource removes a connector's archived entities and their associations.
// archiveSources contains only legacy source names unambiguously owned by this
// connector. Explicit ownership takes precedence over names and ID prefixes.
// Callers stop collection first, including any in-flight source reads.
func (e *Engine) ForgetSource(source string, archiveSources ...string) (Forgotten, error) {
	if e.Store.ReadOnly() {
		return Forgotten{}, ErrReadOnly
	}
	sources := append([]string{source}, archiveSources...)
	for _, name := range sources {
		if name == "" || SafeID(name) != name {
			return Forgotten{}, fmt.Errorf("invalid archive source %q", name)
		}
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
	out := Forgotten{Items: map[string][]string{}}
	owned := func(owner, name, id string) bool {
		if owner != "" {
			return owner == source
		}
		return slices.Contains(sources, name) && strings.HasPrefix(id, name+"-")
	}
	names := map[string]bool{}
	chats := e.Store.Chats()
	for _, c := range chats {
		if owned(c.Connector, c.Source, c.ID) {
			out.Chats = append(out.Chats, c.ID)
			names[c.Name] = true
		}
	}
	// Old timeline lines carry a display name rather than a chat ID. A shared
	// name cannot safely identify the connector that wrote the line.
	for _, c := range chats {
		if !slices.Contains(out.Chats, c.ID) {
			delete(names, c.Name)
		}
	}
	for _, p := range e.Store.People() {
		if owned(p.Connector, p.Source, p.ID) {
			out.People = append(out.People, p.ID)
		} else {
			kept := slices.DeleteFunc(slices.Clone(p.Chats), func(id string) bool { return slices.Contains(out.Chats, id) })
			if len(kept) != len(p.Chats) {
				p.Chats = kept
				if err := e.Store.putEntity(KindPerson, p.ID, p, &p.Tagging, true); err != nil {
					return out, err
				}
			}
		}
	}
	for _, c := range chats {
		if slices.Contains(out.Chats, c.ID) {
			continue
		}
		kept := slices.DeleteFunc(slices.Clone(c.People), func(id string) bool { return slices.Contains(out.People, id) })
		if len(kept) != len(c.People) {
			c.People = kept
			if err := e.Store.putEntity(KindChat, c.ID, c, &c.Tagging, true); err != nil {
				return out, err
			}
		}
	}
	itemKeys := map[string]bool{}
	for _, kind := range []string{KindDoc, KindMeeting, KindEvent} {
		for _, it := range e.Store.Items(kind) {
			if it.Source == source || (it.Source == "" && slices.Contains(out.Chats, it.Where)) {
				out.Items[kind] = append(out.Items[kind], it.ID)
				itemKeys[kind+"/"+SafeID(it.ID)] = true
			} else if slices.Contains(out.Chats, it.Where) {
				it.Where = ""
				if err := e.Store.PutItem(it); err != nil {
					return out, err
				}
			}
		}
	}
	for _, post := range e.Store.MomentPosts() {
		if post.Source == source {
			out.Posts = append(out.Posts, post.ID)
		}
	}
	goneMoment := func(m ProjectMoment) bool { return slices.Contains(out.Posts, m.ID) }
	for _, p := range e.Store.Projects() {
		before := entityMap(p)
		p.Chats = slices.DeleteFunc(slices.Clone(p.Chats), func(id string) bool { return slices.Contains(out.Chats, id) })
		p.People = slices.DeleteFunc(slices.Clone(p.People), func(id string) bool { return slices.Contains(out.People, id) })
		p.Items = slices.DeleteFunc(slices.Clone(p.Items), func(id string) bool { return itemKeys[id] })
		p.Moments = slices.DeleteFunc(slices.Clone(p.Moments), goneMoment)
		if m := p.Merge; m != nil {
			m.Chats = slices.DeleteFunc(m.Chats, func(id string) bool { return slices.Contains(out.Chats, id) })
			m.People = slices.DeleteFunc(m.People, func(id string) bool { return slices.Contains(out.People, id) })
			m.Items = slices.DeleteFunc(m.Items, func(id string) bool { return itemKeys[id] })
			m.Moments = slices.DeleteFunc(m.Moments, func(id string) bool { return slices.Contains(out.Posts, id) })
			m.Evicted = slices.DeleteFunc(m.Evicted, goneMoment)
			m.Moved = slices.DeleteFunc(m.Moved, func(key string) bool {
				k, id, _ := strings.Cut(key, "/")
				return itemKeys[key] || k == KindChat && slices.Contains(out.Chats, id) || k == KindPerson && slices.Contains(out.People, id)
			})
		}
		judged := e.Store.judged(p.Slug)
		judgedChanged := false
		for _, id := range out.Posts {
			if _, ok := judged[id]; ok {
				delete(judged, id)
				judgedChanged = true
			}
		}
		if judgedChanged {
			if err := writeJSON(e.Store.judgedPath(p.Slug), judged); err != nil {
				return out, err
			}
		}
		if reflect.DeepEqual(before, entityMap(p)) {
			continue
		}
		p.Dirty = true
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
		if err := removeForgottenFile(e.Store.historyStatePath(id)); err != nil {
			return out, err
		}
	}
	for _, id := range out.People {
		if err := os.RemoveAll(e.Store.path(KindPerson, id)); err != nil {
			return out, err
		}
	}
	for kind, ids := range out.Items {
		for _, id := range ids {
			if err := removeForgottenFile(e.Store.entityPath(kind, id)); err != nil {
				return out, err
			}
		}
	}
	for _, id := range out.Posts {
		for _, ext := range []string{".md", ".json"} {
			if err := removeForgottenFile(filepath.Join(e.Store.momentsDir(), SafeID(id)+ext)); err != nil {
				return out, err
			}
		}
	}
	if _, err := os.Stat(e.Store.momentsDir()); err == nil {
		// Rebuild on retries too: post files may already be gone after an
		// earlier attempt failed while replacing the index.
		if _, err := e.Store.WriteMomentIndex(); err != nil {
			return out, err
		}
	}
	for _, name := range sources {
		if err := removeForgottenFile(filepath.Join(e.Store.Dir, "cursors", name+".json")); err != nil {
			return out, err
		}
		// Preserve import states for explicitly owned chats of another connector.
		remaining := false
		for _, c := range chats {
			if c.Source == name && !slices.Contains(out.Chats, c.ID) {
				remaining = true
				break
			}
		}
		if !remaining {
			if err := e.Store.forgetHistory(name); err != nil {
				return out, err
			}
		}
	}
	return out, nil
}

func removeForgottenFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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
