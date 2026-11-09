package kb

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/prompts"
)

// Change describes a persisted entity change, without exposing its contents.
// What is metadata, summary, messages, or log.
type Change struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	What string `json:"what"`
}

// ObserveChanges connects store writes, including connector and pipeline writes, to the engine's observer.
// Install observers before starting concurrent work. Callbacks run after the persistence lock is released.
func (e *Engine) ObserveChanges() {
	if e.Store == nil || e.Changed == nil {
		return
	}
	e.Store.observeMu.Lock()
	e.Store.changed = e.Changed
	e.Store.observeMu.Unlock()
}

func (s *Store) notify(kind, id, what string) {
	s.observeMu.RLock()
	f := s.changed
	s.observeMu.RUnlock()
	if f != nil {
		f(Change{Kind: kind, ID: id, What: what})
	}
}

func entityMap(v any) map[string]json.RawMessage {
	raw, _ := json.Marshal(v)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	return fields
}

func (s *Store) entityPath(kind, id string) string {
	if kind == KindDoc || kind == KindMeeting || kind == KindEvent {
		return filepath.Join(s.Dir, kind, SafeID(id)+".json")
	}
	return s.path(kind, id, "meta.json")
}

func fieldKey(field string) string {
	if field == "project_kind" {
		return "kind"
	}
	return field
}

func (t *Tagging) fieldLocked(field string) bool {
	if slices.Contains(t.LockedFields, field) {
		return true
	}
	return t.Locked && (field == "importance" || field == "relevance" || field == "tags")
}

// putEntity rereads protected fields while holding the metadata lock. A stale pipeline object cannot
// restore locked links or overwrite a hand edit made while a model or connector was running.
func (s *Store) putEntity(kind, id string, v any, t *Tagging, manual bool) (err error) {
	if s.readOnly {
		return ErrReadOnly
	}
	if kind == KindRecording {
		s.recFilesMu.Lock()
		if err := s.recoverRecordingLocked(id); err != nil {
			s.recFilesMu.Unlock()
			return err
		}
	}
	s.metaMu.Lock()
	if kind == KindChat {
		s.mu.Lock()
	}
	changed := false
	defer func() {
		if kind == KindChat {
			s.mu.Unlock()
		}
		s.metaMu.Unlock()
		if kind == KindRecording {
			s.recFilesMu.Unlock()
		}
		if err == nil && changed {
			s.notify(kind, id, "metadata")
		}
	}()
	if kind == KindChat {
		if err := s.pendingIngestLocked(id); err != nil {
			return err
		}
	}
	path := s.entityPath(kind, id)
	current := map[string]json.RawMessage{}
	exists, err := readJSON(path, &current)
	if err != nil {
		return err
	}
	if !exists && t.persisted {
		return nil
	} // forgotten during an external call
	incoming := protectedEntity(v, t, current, manual)
	before, _ := json.Marshal(current)
	after, _ := json.Marshal(incoming)
	changed = !exists || !bytes.Equal(before, after)
	if changed {
		err = writeJSON(path, incoming)
	}
	if err == nil {
		switch entity := v.(type) {
		case *Chat:
			*entity = Chat{}
		case *Person:
			*entity = Person{}
		case *Project:
			*entity = Project{}
		case *Item:
			*entity = Item{}
		case *Recording:
			*entity = Recording{}
		}
		_ = json.Unmarshal(after, v)
		t.persisted = true
	}
	return err
}

// protectedEntity merges fields held by a person or extension into a pipeline write.
func protectedEntity(v any, t *Tagging, current map[string]json.RawMessage, manual bool) map[string]json.RawMessage {
	incoming := entityMap(v)
	// Keep an explicit clear in metadata; omitting it makes detail clients fall back to cached tags.
	if t.Tags != nil && len(t.Tags) == 0 {
		incoming["tags"] = json.RawMessage(`[]`)
	}
	if len(current) > 0 {
		var protected Tagging
		raw, _ := json.Marshal(current)
		_ = json.Unmarshal(raw, &protected)
		if !manual {
			for _, field := range append([]string{"pinned", "locked_fields", "locked", "provenance"}, protected.LockedFields...) {
				key := fieldKey(field)
				if value, ok := current[key]; ok {
					incoming[key] = value
				} else {
					delete(incoming, key)
				}
			}
			if protected.Locked {
				for _, key := range []string{"importance", "relevance", "tags"} {
					if value, ok := current[key]; ok {
						incoming[key] = value
					} else {
						delete(incoming, key)
					}
				}
			}
		}
	}
	return incoming
}

func (s *Store) putSummary(kind, id, text string, manual bool) error {
	if s.readOnly {
		return ErrReadOnly
	}
	if kind == KindRecording {
		s.recFilesMu.Lock()
		if err := s.recoverRecordingLocked(id); err != nil {
			s.recFilesMu.Unlock()
			return err
		}
	}
	unlock := func() {
		s.metaMu.Unlock()
		if kind == KindRecording {
			s.recFilesMu.Unlock()
		}
	}
	s.metaMu.Lock()
	var t Tagging
	exists, err := readJSON(s.entityPath(kind, id), &t)
	if err != nil {
		unlock()
		return err
	}
	if !manual && (!exists || t.fieldLocked("summary")) {
		unlock()
		return nil
	}
	path := s.path(kind, id, "summary.md")
	next := []byte(strings.TrimSpace(text) + "\n")
	old, _ := os.ReadFile(path)
	changed := !bytes.Equal(old, next)
	if changed {
		err = writeFile(path, next)
	}
	unlock()
	if err == nil && changed {
		s.notify(kind, id, "summary")
	}
	return err
}

func removeStrings(list, remove []string) []string {
	return slices.DeleteFunc(slices.Clone(list), func(v string) bool { return slices.Contains(remove, v) })
}

func protectUpdate(u Update, t *Tagging) {
	fields := []string{}
	for key, set := range map[string]bool{
		"name": u.Name != "", "project_kind": u.Kind != "", "status": u.Status != "", "goal": u.Goal != "", "my_role": u.MyRole != "", "note": u.Note != "", "summary": u.Summary != "",
		"importance": u.Importance != "", "relevance": u.Relevance != nil, "tags": u.Tags != nil, "dirs": len(u.Dirs) > 0,
		"aliases": len(u.Aliases)+len(u.RemoveAliases) > 0, "chats": len(u.Chats)+len(u.RemoveChats) > 0, "people": len(u.People)+len(u.RemovePeople) > 0, "projects": len(u.Projects)+len(u.RemoveProjects) > 0,
	} {
		if set {
			fields = append(fields, key)
		}
	}
	for _, field := range fields {
		if u.Lock == nil || *u.Lock {
			t.LockedFields = addUnique(t.LockedFields, field)
		}
	}
	slices.Sort(t.LockedFields)
	if u.Pinned != nil {
		t.Pinned = *u.Pinned
	}
}

// unlink removes the reverse side of explicit removals; it never deletes an entity.
func (e *Engine) unlink(kind, id string, u Update) error {
	lock := func(t *Tagging, field string) {
		if u.Lock == nil || *u.Lock {
			t.LockedFields = addUnique(t.LockedFields, field)
		}
	}
	if kind == KindProject {
		for _, pid := range u.RemovePeople {
			if p, ok := e.Store.Person(pid); ok {
				p.Projects = removeStrings(p.Projects, []string{id})
				lock(&p.Tagging, "projects")
				if err := e.Store.putEntity(KindPerson, p.ID, p, &p.Tagging, true); err != nil {
					return err
				}
				e.WritePersonSummary(p.ID)
			}
		}
		for _, cid := range u.RemoveChats {
			if c, ok := e.Store.Chat(cid); ok {
				c.Projects = removeStrings(c.Projects, []string{id})
				lock(&c.Tagging, "projects")
				if err := e.Store.putEntity(KindChat, c.ID, c, &c.Tagging, true); err != nil {
					return err
				}
			}
		}
	}
	if kind == KindChat {
		for _, pid := range u.RemovePeople {
			if p, ok := e.Store.Person(pid); ok {
				p.Chats = removeStrings(p.Chats, []string{id})
				lock(&p.Tagging, "chats")
				if err := e.Store.putEntity(KindPerson, p.ID, p, &p.Tagging, true); err != nil {
					return err
				}
				e.WritePersonSummary(p.ID)
			}
		}
	}
	if kind == KindPerson {
		for _, cid := range u.RemoveChats {
			if c, ok := e.Store.Chat(cid); ok {
				c.People = removeStrings(c.People, []string{id})
				lock(&c.Tagging, "people")
				if err := e.Store.putEntity(KindChat, c.ID, c, &c.Tagging, true); err != nil {
					return err
				}
			}
		}
	}
	for _, slug := range u.RemoveProjects {
		if p, ok := e.Store.Project(slug); ok {
			switch kind {
			case KindChat:
				p.Chats = removeStrings(p.Chats, []string{id})
				lock(&p.Tagging, "chats")
			case KindPerson:
				p.People = removeStrings(p.People, []string{id})
				lock(&p.Tagging, "people")
			case KindDoc, KindMeeting, KindEvent, KindRecording:
				p.Items = removeStrings(p.Items, []string{kind + "/" + SafeID(id)})
				lock(&p.Tagging, "items")
			default:
				continue
			}
			if err := e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) pinnedPeople(ids []string) []prompts.KBPinnedPerson {
	var out []prompts.KBPinnedPerson
	for _, id := range ids {
		if p, ok := e.Store.Person(id); ok && p.Pinned != "" {
			out = append(out, prompts.KBPinnedPerson{Name: p.Name, Pinned: p.Pinned})
		}
	}
	return out
}
