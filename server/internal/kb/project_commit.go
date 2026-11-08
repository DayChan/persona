package kb

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// commitProject checks the frozen model input and writes both files under the same persistence lock.
// Observers run after both writes, so a callback that forgets a source cannot be followed by a stale write.
func (e *Engine) commitProject(snapshot *Project, input prompts.KBProjectInput, d projectDigest) (err error) {
	s := e.Store
	s.metaMu.Lock()
	var current *Project
	var summaryChanged, metadataChanged bool
	defer func() {
		s.metaMu.Unlock()
		if metadataChanged {
			e.index(KindProject, current.Slug, current.Name, current.Tagging, current.OneLine, current.Updated)
		}
		if summaryChanged {
			s.notify(KindProject, snapshot.Slug, "summary")
		}
		if metadataChanged {
			s.notify(KindProject, snapshot.Slug, "metadata")
		}
	}()
	var ok bool
	current, ok = s.Project(snapshot.Slug)
	if !ok {
		return nil
	}
	before, _ := json.Marshal(current)
	frozen, _ := json.Marshal(snapshot)
	if !bytes.Equal(before, frozen) || prompts.RenderKBProjectInput(e.projectInput(current)) != prompts.RenderKBProjectInput(input) {
		return nil
	}
	if !current.fieldLocked("one_line") {
		current.OneLine = oneLine(d.OneLine)
	}
	if d.Goal != "" && !current.fieldLocked("goal") {
		current.Goal = oneLine(d.Goal)
	}
	if (d.Kind == "short" || d.Kind == "long") && !current.fieldLocked("project_kind") {
		current.Kind = d.Kind
	}
	if (d.Status == "active" || d.Status == "paused" || d.Status == "done") && current.Status != "ignored" && !current.fieldLocked("status") {
		current.Status = d.Status
	}
	protected := current.Tagging
	now := time.Now().UTC()
	applyTags(&current.Tagging, d.Importance, int(d.Relevance), d.Tags, now)
	if protected.fieldLocked("importance") {
		current.Importance = protected.Importance
	}
	if protected.fieldLocked("relevance") {
		current.Relevance = protected.Relevance
	}
	if protected.fieldLocked("tags") {
		current.Tags = protected.Tags
	}
	if kw := cleanKeywords(d.Keywords); len(kw) > 0 && !current.fieldLocked("keywords") {
		current.Keywords = kw
	}
	current.Dirty = false
	current.Updated = now
	if strings.TrimSpace(d.Summary) != "" && !current.fieldLocked("summary") {
		path := s.path(KindProject, current.Slug, "summary.md")
		next := []byte(strings.TrimSpace("# "+current.Name+"\n\n"+d.Summary) + "\n")
		old, _ := os.ReadFile(path)
		if !bytes.Equal(old, next) {
			if err = writeFile(path, next); err != nil {
				return err
			}
			summaryChanged = true
		}
	}
	if err = writeJSON(s.entityPath(KindProject, current.Slug), current); err != nil {
		return err
	}
	metadataChanged = true
	return nil
}
