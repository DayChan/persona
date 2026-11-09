package kb

import (
	"os"
	"time"
)

// pruneDocs deletes the documents whose time is past their days under the selection (Selection.DocDays). Only days the
// user set apply: with the defaults, or a rule turned off, nothing is deleted, as before the selection. A document is
// judged by its time (At, else when it was first seen; neither: kept), and one the user or a project holds on to is
// kept whatever its days: pinned or locked (it carries what the user confirmed) or linked to a project.
func (e *Engine) pruneDocs(now time.Time, sel *Selection) int {
	st := e.Store
	if st.ReadOnly() || sel == nil || sel.Stopgap() {
		return 0
	}
	type due struct {
		id   string
		days int
	}
	var cands []due
	for _, it := range st.Items(KindDoc) {
		if days, ok := sel.DocDays(DocFacts{Source: e.itemSource(it), ID: it.ID, Signals: it.Signals}); ok && docExpired(it, now, days) {
			cands = append(cands, due{it.ID, days})
		}
	}
	if len(cands) == 0 {
		return 0
	}
	linked := map[string]bool{}
	for _, p := range st.Projects() {
		for _, key := range p.Items {
			linked[key] = true
		}
		if m := p.Merge; m != nil {
			for _, key := range m.Items {
				linked[key] = true
			}
		}
	}
	var gone []string
	for _, c := range cands {
		if linked[KindDoc+"/"+SafeID(c.id)] {
			continue
		}
		if st.removeExpiredDoc(c.id, now, c.days) {
			gone = append(gone, c.id)
		}
	}
	e.unindex(KindDoc, gone)
	if len(gone) > 0 {
		e.logf("pruned %d document(s) past their days", len(gone))
	}
	return len(gone)
}

// docExpired says a document's time is before now less days. One without a time, held by the user (pinned, locked or
// a locked field) or tagged to a project is never past them.
func docExpired(it *Item, now time.Time, days int) bool {
	at := it.At
	if at.IsZero() {
		at = it.Seen
	}
	if at.IsZero() || it.Pinned != "" || it.Locked || len(it.LockedFields) > 0 || len(it.Projects) > 0 {
		return false
	}
	return at.Before(now.AddDate(0, 0, -days))
}

// removeExpiredDoc deletes a document's file when, read again under the metadata lock, it is still past days: a
// collect or an edit between the scan and now (a newer time, a pin) keeps it. A later write of an item read before
// the delete finds it gone and does not bring it back (putEntity skips a persisted item that is no longer there;
// CollectItems' update keeps that mark through carryOver's Tagging). A new value (a source listing it again) may.
func (s *Store) removeExpiredDoc(id string, now time.Time, days int) bool {
	s.metaMu.Lock()
	it, ok := s.Item(KindDoc, id)
	removed := ok && docExpired(it, now, days) && os.Remove(s.entityPath(KindDoc, id)) == nil
	s.metaMu.Unlock()
	if removed {
		s.notify(KindDoc, id, "metadata")
	}
	return removed
}
