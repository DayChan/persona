package kb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Who holds a field (docs/assistant/knowledge.md "Extensions organizing the knowledge base"): the user > an extension > the
// built-in pipeline. A locked field without a provenance entry is the user's; one whose entry names an extension is
// that extension's. The pipeline writes only fields nobody holds (putEntity, putSummary). The user's writes (Apply)
// take over every locked field they touch; an extension's (ApplyExtension) may only touch fields that are free or
// already its own, and otherwise get a ConflictError to put to the user instead.

// UserWriter is the owner of a locked field without provenance.
const UserWriter = "user"

// ExtensionWriter is how an extension is named as a writer in provenance.
func ExtensionWriter(id string) string { return "ext:" + id }

// ConflictError: an extension's write touches fields someone else holds. Owners maps "<kind>/<id>.<field>" (the
// target's fields without the prefix) to the holder (UserWriter or "ext:<id>").
type ConflictError struct {
	Owners map[string]string
}

func (c *ConflictError) Error() string {
	keys := make([]string, 0, len(c.Owners))
	for k := range c.Owners {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + " (" + c.Owners[k] + ")"
	}
	return "held by someone else: " + strings.Join(parts, ", ")
}

// ExtensionWrite is what an extension adds to an update: who it is, what the values rest on, and fields it hands
// back to the pipeline.
type ExtensionWrite struct {
	Extension string
	Evidence  []Evidence
	Unlock    []string
}

// ExtensionResult: the entity's id, and the values the update replaced (field → its previous JSON, null when unset;
// summary is the previous summary.md text), for the extension's own undo.
type ExtensionResult struct {
	ID     string
	Before map[string]json.RawMessage
}

// UpdateFields are the update fields an update touches, as named in locked_fields (and "pinned").
func UpdateFields(u Update) []string {
	var fields []string
	for key, set := range map[string]bool{
		"name": u.Name != "", "project_kind": u.Kind != "", "status": u.Status != "", "goal": u.Goal != "", "my_role": u.MyRole != "", "note": u.Note != "", "summary": u.Summary != "",
		"importance": u.Importance != "", "relevance": u.Relevance != nil, "tags": u.Tags != nil, "dirs": len(u.Dirs) > 0,
		"aliases": len(u.Aliases)+len(u.RemoveAliases) > 0, "chats": len(u.Chats)+len(u.RemoveChats) > 0, "people": len(u.People)+len(u.RemovePeople) > 0, "projects": len(u.Projects)+len(u.RemoveProjects) > 0,
		"pinned": u.Pinned != nil,
	} {
		if set {
			fields = append(fields, key)
		}
	}
	slices.Sort(fields)
	return fields
}

// lockableFields are the fields an extension may name in unlock.
var lockableFields = []string{"aliases", "chats", "dirs", "goal", "importance", "items", "my_role", "name", "note", "people",
	"pinned", "project_kind", "projects", "relevance", "status", "summary", "tags"}

// LockableField reports whether field can be held (and so handed back).
func LockableField(field string) bool { return slices.Contains(lockableFields, field) }

// fieldRef is one field of one entity an update writes: the target's own, or the reverse side of a link.
type fieldRef struct {
	kind, id, field string
}

func (r fieldRef) key(target fieldRef) string {
	if r.kind == target.kind && r.id == target.id {
		return r.field
	}
	return r.kind + "/" + r.id + "." + r.field
}

// heldBy is who holds field of an entity's tagging: "" when nobody (the pipeline may write it).
func heldBy(t *Tagging, field string) string {
	locked := t.fieldLocked(field)
	if field == "pinned" {
		locked = t.Pinned != ""
	}
	if !locked {
		return ""
	}
	if p, ok := t.Provenance[field]; ok && p.By != "" {
		return p.By
	}
	return UserWriter
}

// tagging reads an entity's tagging; nil when it does not exist.
func (s *Store) tagging(kind, id string) (*Tagging, error) {
	var t Tagging
	exists, err := readJSON(s.entityPath(kind, id), &t)
	if err != nil || !exists {
		return nil, err
	}
	return &t, nil
}

// resolve finds the entity an update names, as apply would: "" when apply would create it (a new project) or it is
// missing (apply reports that).
func (e *Engine) resolve(kind, id string) string {
	switch kind {
	case KindProject:
		if p, ok := e.Store.Project(id); ok {
			return p.Slug
		}
		if p := e.FindProject(id); p != nil {
			return p.Slug
		}
		return ""
	case KindRecording:
		id, _ = RecordingID(id)
	}
	if t, _ := e.Store.tagging(kind, id); t == nil {
		return ""
	}
	return id
}

// writes lists the fields an update writes on target and on the reverse side of its links.
func (e *Engine) writes(kind, id string, u Update) []fieldRef {
	var out []fieldRef
	for _, f := range UpdateFields(u) {
		out = append(out, fieldRef{kind, id, f})
	}
	add := func(k, field string, ids ...[]string) {
		for _, list := range ids {
			for _, other := range list {
				switch k {
				case KindProject:
					if p, ok := e.Store.Project(other); ok {
						other = p.Slug
					} else {
						continue
					}
				case KindChat:
					if _, ok := e.Store.Chat(other); !ok {
						continue
					}
				case KindPerson:
					if _, ok := e.Store.Person(other); !ok {
						continue
					}
				}
				ref := fieldRef{k, other, field}
				if !slices.Contains(out, ref) {
					out = append(out, ref)
				}
			}
		}
	}
	switch kind {
	case KindProject:
		add(KindPerson, "projects", u.People, u.RemovePeople)
		add(KindChat, "projects", u.Chats, u.RemoveChats)
	case KindChat:
		add(KindProject, "chats", u.Projects, u.RemoveProjects)
		add(KindPerson, "chats", u.RemovePeople)
	case KindPerson:
		add(KindProject, "people", u.Projects, u.RemoveProjects)
		add(KindChat, "people", u.RemoveChats)
	case KindDoc, KindMeeting, KindEvent, KindRecording:
		add(KindProject, "items", u.Projects, u.RemoveProjects)
	}
	return out
}

// conflicts are the fields of refs (and unlock on the target) held by someone other than by.
func (e *Engine) conflicts(target fieldRef, refs []fieldRef, unlock []string, by string) (map[string]string, error) {
	owners := map[string]string{}
	cache := map[fieldRef]*Tagging{}
	read := func(r fieldRef) (*Tagging, error) {
		k := fieldRef{r.kind, r.id, ""}
		if t, ok := cache[k]; ok {
			return t, nil
		}
		t, err := e.Store.tagging(r.kind, r.id)
		cache[k] = t
		return t, err
	}
	check := func(r fieldRef) error {
		t, err := read(r)
		if err != nil || t == nil {
			return err
		}
		if holder := heldBy(t, r.field); holder != "" && holder != by {
			owners[r.key(target)] = holder
		}
		return nil
	}
	if target.id != "" {
		for _, f := range unlock {
			if err := check(fieldRef{target.kind, target.id, f}); err != nil {
				return nil, err
			}
		}
	}
	for _, r := range refs {
		if r.id == "" {
			continue
		}
		if err := check(r); err != nil {
			return nil, err
		}
	}
	return owners, nil
}

// before is the current values of the target's fields an update replaces.
func (e *Engine) before(kind, id string, fields []string) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	if id == "" {
		for _, f := range fields {
			out[f] = json.RawMessage("null")
		}
		return out, nil
	}
	current := map[string]json.RawMessage{}
	if _, err := readJSON(e.Store.entityPath(kind, id), &current); err != nil {
		return nil, err
	}
	for _, f := range fields {
		if f == "summary" {
			raw, _ := json.Marshal(strings.TrimSpace(e.Store.Summary(kind, id)))
			out[f] = raw
			continue
		}
		if v, ok := current[fieldKey(f)]; ok {
			out[f] = v
		} else {
			out[f] = json.RawMessage("null")
		}
	}
	return out, nil
}

// ApplyExtension is Apply for an extension's write: it refuses (ConflictError) to touch what the user or another
// extension holds, records the extension's provenance on the locked fields it wrote, hands back w.Unlock, and
// returns the replaced values. Nothing is written when it refuses.
func (e *Engine) ApplyExtension(kind, id string, u Update, w ExtensionWrite) (ExtensionResult, error) {
	if e.Store.ReadOnly() {
		return ExtensionResult{}, ErrReadOnly
	}
	if w.Extension == "" {
		return ExtensionResult{}, errors.New("an extension write needs the extension")
	}
	for _, f := range w.Unlock {
		if !LockableField(f) {
			return ExtensionResult{}, fmt.Errorf("unlock: unknown field %q", f)
		}
	}
	by := ExtensionWriter(w.Extension)
	e.Store.applyMu.Lock()
	defer e.Store.applyMu.Unlock()
	target := fieldRef{kind: kind, id: e.resolve(kind, id)}
	var refs []fieldRef
	if u.MergeInto != "" {
		// Folding a project away rewrites all of it: nothing on it may be held by someone else.
		for _, f := range append(slices.Clone(lockableFields), "items") {
			refs = append(refs, fieldRef{kind, target.id, f})
		}
	} else {
		refs = e.writes(kind, target.id, u)
	}
	owners, err := e.conflicts(target, refs, w.Unlock, by)
	if err != nil {
		return ExtensionResult{}, err
	}
	if len(owners) > 0 {
		return ExtensionResult{}, &ConflictError{Owners: owners}
	}
	before, err := e.before(kind, target.id, UpdateFields(u))
	if err != nil {
		return ExtensionResult{}, err
	}
	var legacy bool // an extension's importance / relevance is held through locked_fields, not the user's "locked"
	if target.id != "" {
		if t, _ := e.Store.tagging(kind, target.id); t != nil {
			legacy = t.Locked
		}
	}
	if u.Source == "" && u.Log != "" {
		u.Source = w.Extension
	}
	out, err := e.apply(kind, id, u)
	if err != nil || u.MergeInto != "" {
		return ExtensionResult{ID: out, Before: before}, err
	}
	if err := e.claim(kind, out, u, by, w.Evidence, w.Unlock, legacy); err != nil {
		return ExtensionResult{ID: out, Before: before}, err
	}
	return ExtensionResult{ID: out, Before: before}, nil
}

// claim records who holds the locked fields an applied update wrote: by "" (the user) drops provenance from them;
// an extension's name puts its provenance on them and hands back unlock. keepLegacy (extensions only) is the
// target's "locked" before the write.
func (e *Engine) claim(kind, id string, u Update, by string, evidence []Evidence, unlock []string, keepLegacy ...bool) error {
	now := time.Now().UTC()
	refs := e.writes(kind, id, u)
	byEntity := map[fieldRef][]string{}
	var order []fieldRef
	for _, r := range refs {
		k := fieldRef{r.kind, r.id, ""}
		if _, ok := byEntity[k]; !ok {
			order = append(order, k)
		}
		byEntity[k] = append(byEntity[k], r.field)
	}
	if len(unlock) > 0 {
		k := fieldRef{kind, id, ""}
		if _, ok := byEntity[k]; !ok {
			order = append(order, k)
			byEntity[k] = nil
		}
	}
	for _, k := range order {
		isTarget := k.kind == kind && k.id == id
		err := e.Store.editTagging(k.kind, k.id, func(t *Tagging) bool {
			if by == "" { // the user's write: only entities an extension holds something on change at all
				dropped := false
				for _, f := range byEntity[k] {
					if _, ok := t.Provenance[f]; ok {
						delete(t.Provenance, f)
						dropped = true
					}
				}
				if len(t.Provenance) == 0 {
					t.Provenance = nil
				}
				return dropped
			}
			if isTarget && len(keepLegacy) > 0 && !keepLegacy[0] {
				t.Locked = false
			}
			for _, f := range byEntity[k] {
				if heldBy(t, f) == "" {
					continue // written with lock: false; the pipeline may keep it
				}
				if t.Provenance == nil {
					t.Provenance = map[string]Provenance{}
				}
				p := Provenance{By: by, At: now}
				if isTarget {
					p.Evidence = evidence
				}
				t.Provenance[f] = p
			}
			if isTarget {
				for _, f := range unlock {
					t.LockedFields = slices.DeleteFunc(t.LockedFields, func(v string) bool { return v == f })
					delete(t.Provenance, f)
					if f == "pinned" {
						t.Pinned = ""
					}
				}
			}
			if len(t.Provenance) == 0 {
				t.Provenance = nil
			}
			return true
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// editTagging changes the tagging fields of an entity's metadata in place (other fields as they are on disk); it
// writes and notifies only when edit says it changed something and the file differs.
func (s *Store) editTagging(kind, id string, edit func(*Tagging) bool) (err error) {
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
	if err != nil || !exists {
		return err
	}
	var t Tagging
	raw, _ := json.Marshal(current)
	_ = json.Unmarshal(raw, &t)
	if !edit(&t) {
		return nil
	}
	next := map[string]json.RawMessage{}
	for k, v := range current {
		next[k] = v
	}
	set := func(key string, v any, empty bool) {
		if empty {
			delete(next, key)
			return
		}
		b, _ := json.Marshal(v)
		next[key] = b
	}
	set("locked", t.Locked, !t.Locked)
	set("pinned", t.Pinned, t.Pinned == "")
	set("locked_fields", t.LockedFields, len(t.LockedFields) == 0)
	set("provenance", t.Provenance, len(t.Provenance) == 0)
	before, _ := json.Marshal(current)
	after, _ := json.Marshal(next)
	changed = !bytes.Equal(before, after)
	if changed {
		err = writeJSON(path, next)
	}
	return err
}
