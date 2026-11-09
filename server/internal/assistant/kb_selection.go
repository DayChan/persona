package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

// The knowledge base's selection (docs/assistant/knowledge.md "Selection", #1523): one document, kept in the settings
// table so every client and process agrees (edited through PUT and kb_history_plan), that decides what daily ingestion and the history
// import bring in from chats and documents, and for how many days. A missing document is kb.Defaults. The engine reads
// it through kbSelectionNow (cached, refreshed after a PUT and every few minutes); a document that cannot be read keeps
// everything as before the selection (kb.KeepEverything), so a database outage never drops what a chat sends, and
// prunes nothing, so it never deletes what the document keeps longer.
//
//	GET /api/v1/kb/selection                       the document (the defaults when none was saved)
//	PUT /api/v1/kb/selection                       the whole document, validated (kb.ParseSelection)
//	GET /api/v1/kb/selection/candidates?source=id  the source's chats (connectors.history_chats), to pick per-item settings

const (
	kbSelectionKey = "kb.selection"
	// kbSelectionFresh is how long the cached document is used before it is read again (another process may write it).
	kbSelectionFresh = 5 * time.Minute
	// kbSelectionRetry is how long after a failed read the database is asked again (meanwhile everything is kept).
	kbSelectionRetry = time.Minute
	kbSelectionMost  = 1 << 20 // a PUT's body
)

// kbSelectionState is the selection's part of the Service.
type kbSelectionState struct {
	mu       sync.Mutex // reading the database, and every change
	cur      atomic.Pointer[kbSelectionCached]
	failedAt atomic.Int64 // unix nanoseconds of the last failed read
}

type kbSelectionCached struct {
	sel *kb.Selection // never changed once here: a change puts a new one in
	at  time.Time
}

var errKBSelectionNoDB = i18n.Err("assistant-kb_selection.no_db")

// kbSelection is the document in force: the cached one while fresh, else read from the settings table (the defaults
// when it was never saved). An error when it cannot be read and nothing was read before; when something was, that
// still holds, and the database is not asked again for kbSelectionRetry.
func (s *Service) kbSelection(ctx context.Context) (*kb.Selection, error) {
	k := &s.kb.selection
	if c := k.cur.Load(); c != nil && time.Since(c.at) < kbSelectionFresh {
		return c.sel, nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if c := k.cur.Load(); c != nil && time.Since(c.at) < kbSelectionFresh {
		return c.sel, nil
	}
	sel, err := s.kbSelectionRead(ctx)
	if err != nil {
		if c := k.cur.Load(); c != nil { // what was read before still holds, until the next try
			k.cur.Store(&kbSelectionCached{sel: c.sel, at: time.Now().Add(kbSelectionRetry - kbSelectionFresh)})
			s.logf("kb: the selection does not read, the one read before holds meanwhile: %v", err)
			return c.sel, nil
		}
		return nil, err
	}
	k.cur.Store(&kbSelectionCached{sel: sel, at: time.Now()})
	return sel, nil
}

// kbSelectionRead reads the stored document (k.mu held).
func (s *Service) kbSelectionRead(ctx context.Context) (*kb.Selection, error) {
	st := s.store()
	if st == nil {
		return nil, errKBSelectionNoDB
	}
	var raw json.RawMessage
	found, err := st.Setting(ctx, kbSelectionKey, &raw)
	if err != nil {
		return nil, err
	}
	if !found || len(raw) == 0 || string(raw) == "null" {
		d := kb.Defaults()
		return &d, nil
	}
	sel, err := kb.ParseSelection(raw)
	if err != nil {
		return nil, err
	}
	kbSelectionTidy(&sel)
	return &sel, nil
}

// kbSelectionNow is the engine's Selection: the document in force, or kb.KeepEverything (a stopgap: everything is
// written, nothing pruned, no approval made or taken back) when it cannot be read and never was (asked again a
// minute later).
func (s *Service) kbSelectionNow() *kb.Selection {
	k := &s.kb.selection
	if c := k.cur.Load(); c != nil && time.Since(c.at) < kbSelectionFresh {
		return c.sel
	}
	if at := k.failedAt.Load(); at != 0 && time.Since(time.Unix(0, at)) < kbSelectionRetry {
		if c := k.cur.Load(); c != nil {
			return c.sel
		}
		keep := kb.KeepEverything()
		return &keep
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sel, err := s.kbSelection(ctx)
	if err != nil {
		k.failedAt.Store(time.Now().UnixNano())
		s.logf("kb: the selection does not read, everything is kept meanwhile: %v", err)
		keep := kb.KeepEverything()
		return &keep
	}
	k.failedAt.Store(0)
	return sel
}

// kbSelectionTidy gives a document the empty lists and maps a client expects instead of null.
func kbSelectionTidy(sel *kb.Selection) {
	if sel.Items == nil {
		sel.Items = []kb.SelectionItem{}
	}
	if sel.History == nil {
		sel.History = map[string]kb.HistorySelection{}
	}
}

// putKBSelection validates and stores a whole document, and puts it in force.
func (s *Service) putKBSelection(ctx context.Context, sel kb.Selection) (*kb.Selection, error) {
	k := &s.kb.selection
	k.mu.Lock()
	defer k.mu.Unlock()
	return s.putKBSelectionLocked(ctx, sel)
}

func (s *Service) putKBSelectionLocked(ctx context.Context, sel kb.Selection) (*kb.Selection, error) {
	if err := sel.Validate(); err != nil {
		return nil, err
	}
	st := s.store()
	if st == nil {
		return nil, errKBSelectionNoDB
	}
	sel = sel.Clone()
	kbSelectionTidy(&sel)
	if err := st.PutSetting(ctx, kbSelectionKey, sel); err != nil {
		return nil, err
	}
	k := &s.kb.selection
	k.cur.Store(&kbSelectionCached{sel: &sel, at: time.Now()})
	k.failedAt.Store(0)
	s.event(ctx, map[string]any{"type": "kb_selection"})
	return &sel, nil
}

// updateKBSelection changes the stored document with f (on a copy of what the database has now) and stores it; f's
// error changes nothing. changed false from f: nothing is written.
func (s *Service) updateKBSelection(ctx context.Context, f func(*kb.Selection) (bool, error)) (*kb.Selection, error) {
	k := &s.kb.selection
	k.mu.Lock()
	defer k.mu.Unlock()
	cur, err := s.kbSelectionRead(ctx)
	if err != nil {
		return nil, err
	}
	next := cur.Clone()
	changed, err := f(&next)
	if err != nil {
		return nil, err
	}
	if !changed {
		k.cur.Store(&kbSelectionCached{sel: cur, at: time.Now()})
		return cur, nil
	}
	return s.putKBSelectionLocked(ctx, next)
}

// refreshSelectionNames takes the names the source lists now into the per-item settings of its chats (by their
// stable ids), so a renamed chat keeps its setting and shows its new name.
func (s *Service) refreshSelectionNames(ctx context.Context, connector string, chats []connectorwire.HistoryChat) {
	names := map[[2]string]string{}
	for _, c := range chats {
		if c.ID != "" && c.Name != "" {
			names[[2]string{historyPlanChat{FiledAs: c.Source}.filing(connector), c.ID}] = c.Name
		}
	}
	cur, err := s.kbSelection(ctx)
	if err != nil || !kbSelectionRenamed(cur, names) {
		return
	}
	_, err = s.updateKBSelection(ctx, func(sel *kb.Selection) (bool, error) {
		changed := false
		for i := range sel.Items {
			it := &sel.Items[i]
			if n, ok := names[[2]string{it.Source, it.ID}]; ok && it.Kind == kb.ItemChat && it.Name != n {
				it.Name, changed = n, true
			}
		}
		return changed, nil
	})
	if err != nil {
		s.logf("kb: the selection's names were not refreshed: %v", err)
	}
}

func kbSelectionRenamed(sel *kb.Selection, names map[[2]string]string) bool {
	for _, it := range sel.Items {
		if n, ok := names[[2]string{it.Source, it.ID}]; ok && it.Kind == kb.ItemChat && it.Name != n {
			return true
		}
	}
	return false
}

// ---- the routes ----

func (s *Service) kbSelectionRoutes() {
	s.handle("GET /api/v1/kb/selection", s.getKBSelection)
	s.handle("PUT /api/v1/kb/selection", s.putKBSelectionRoute)
	s.handle("GET /api/v1/kb/selection/candidates", s.getKBSelectionCandidates)
}

func (s *Service) getKBSelection(w http.ResponseWriter, r *http.Request) {
	sel, err := s.kbSelection(r.Context())
	if err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, kbSelectionErr(err))
		return
	}
	apierr.WriteJSON(w, sel)
}

func (s *Service) putKBSelectionRoute(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-kb_selection.read_only_device"))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, kbSelectionMost))
	if err != nil {
		writeBodyErr(w, err, i18n.Err("assistant-kb_selection.invalid", i18n.Args{"detail": err.Error()}))
		return
	}
	sel, err := kb.ParseSelection(raw)
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, kbSelectionErr(err))
		return
	}
	saved, err := s.putKBSelection(r.Context(), sel)
	if err != nil {
		apierr.Write(w, kbSelectionStatus(err), kbSelectionErr(err))
		return
	}
	apierr.WriteJSON(w, saved)
}

// kbSelectionErr is the error a client is shown: a document refused says what is wrong with it.
func kbSelectionErr(err error) error {
	if errors.Is(err, kb.ErrSelection) {
		return i18n.Err("assistant-kb_selection.invalid", i18n.Args{"detail": err.Error()})
	}
	return err
}

func kbSelectionStatus(err error) int {
	if errors.Is(err, kb.ErrSelection) {
		return http.StatusBadRequest
	}
	return http.StatusServiceUnavailable
}

// kbSelectionCandidate is one chat of GET /api/v1/kb/selection/candidates.
type kbSelectionCandidate struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Source is what a per-item setting of the chat names (the source it is filed under).
	Source    string `json:"source"`
	Bot       bool   `json:"bot,omitempty"`
	Muted     bool   `json:"muted,omitempty"`
	Collapsed bool   `json:"collapsed,omitempty"`
	Done      bool   `json:"done,omitempty"`
	Estimate  int    `json:"estimate,omitempty"`
	// Item is its per-item setting (null: none).
	Item *kb.SelectionItem `json:"item"`
	// Read is what the selection reads of it now: whole, personal (only the owner's messages and mentions) or none;
	// Days how far back that goes.
	Read string `json:"read"`
	Days int    `json:"days,omitempty"`
}

type kbSelectionCandidates struct {
	Source string `json:"source"`
	// Cached: the import is running or stopped, so the source was not asked; these are the chats its plan listed.
	Cached   bool                   `json:"cached,omitempty"`
	ListedAt time.Time              `json:"listed_at,omitzero"`
	Chats    []kbSelectionCandidate `json:"chats"`
}

func (s *Service) getKBSelectionCandidates(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	source := r.URL.Query().Get("source")
	if source == "" {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-kb_selection.no_source"))
		return
	}
	e, err := s.kbGet(ctx)
	switch {
	case errors.Is(err, errKBOff):
		apierr.WriteCode(w, http.StatusServiceUnavailable, assistantwire.CodeKBDisabled, errKBRouteOff)
		return
	case err != nil:
		apierr.WriteCode(w, http.StatusServiceUnavailable, assistantwire.CodeKBUnavailable, errKBRouteNotOpen)
		return
	}
	sel, err := s.kbSelection(ctx)
	if err != nil {
		apierr.Write(w, http.StatusServiceUnavailable, kbSelectionErr(err))
		return
	}
	out := kbSelectionCandidates{Source: source, Chats: []kbSelectionCandidate{}}
	var chats []historyPlanChat
	data := e.Store.Dir
	_, stopped := readHistoryStop(data)
	if stopped || s.historyBusyElsewhere(data) {
		// The import's calls are paced: the list is not asked for between them, nor while it is stopped.
		if p, ok, err := loadHistoryPlan(data, source); err == nil && ok {
			chats, out.ListedAt = p.Chats, p.GeneratedAt
		}
		out.Cached = true
	} else {
		list, err := s.historyListChats(ctx, data, source)
		var h *historyHalt
		switch {
		case errors.Is(err, errHistoryUnavailable):
			apierr.Write(w, http.StatusNotFound, i18n.Err("assistant-kb_selection.no_history", i18n.Args{"source": source}))
			return
		case errors.As(err, &h):
			apierr.Write(w, http.StatusServiceUnavailable, i18n.Err("assistant-kb_selection.list_stopped", i18n.Args{"reason": historyReasonText(s.requestCtxLang(ctx), h.why)}))
			return
		case err != nil:
			apierr.Write(w, http.StatusBadGateway, i18n.Err("assistant-kb_selection.list_failed", i18n.Args{"code": historyErrCode(err)}))
			return
		}
		s.refreshSelectionNames(ctx, source, list)
		if sel, err = s.kbSelection(ctx); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, kbSelectionErr(err))
			return
		}
		p := &historyPlan{Source: source}
		p.regenerate(list, time.Now())
		chats, out.ListedAt = p.Chats, p.GeneratedAt
	}
	ret := e.Retention()
	for _, c := range chats {
		filing := c.filing(source)
		cand := kbSelectionCandidate{ID: c.ID, Name: c.Name, Kind: c.Kind, Source: filing, Bot: c.Bot, Muted: c.Muted, Collapsed: c.Collapsed,
			Done: c.Done, Estimate: c.Estimate, Read: "none"}
		if it := sel.Item(filing, kb.ItemChat, c.ID); it != nil {
			cp := *it
			cand.Item = &cp
		}
		switch need := historyNeed(sel, ret, source, c); {
		case need.Whole:
			cand.Read, cand.Days = "whole", need.Days
		case need.Read:
			cand.Read, cand.Days = "personal", need.Days
		}
		out.Chats = append(out.Chats, cand)
	}
	sort.SliceStable(out.Chats, func(i, j int) bool { return out.Chats[i].Estimate > out.Chats[j].Estimate })
	apierr.WriteJSON(w, out)
}
