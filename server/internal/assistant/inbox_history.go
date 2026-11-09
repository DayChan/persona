package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The done items' history (GET /api/v1/inbox/done, #2200): the list shows done items for inboxKeepDone and the manager
// holds them for inboxForget, but the database keeps them for inboxKeepHistory; this pages them, newest done first. A
// manager without a database pages the done items it holds. The pages are one manager's: the hub stamps the items
// with it but does not merge managers (each one's cursor is its own), the apps ask each manager and merge.
//
// An item of the history memory no longer holds still opens (GET /api/v1/inbox/{id} reads its row, storedInboxItem)
// and can be brought back (a reopen loads its row first, restoreInbox); filing it again finds no item, as before.

const (
	inboxHistoryLimit = 50  // items in a page when the request names no limit
	inboxHistoryMax   = 200 // the most a page holds
)

var (
	errInboxHistoryLimit  = i18n.ErrEnglish("inbox-errors.history_limit", i18n.Args{"max": inboxHistoryMax})
	errInboxHistoryCursor = i18n.ErrEnglish("inbox-errors.history_cursor")
)

// inboxHistoryCursor is where the next page starts: the last item's done_at (RFC 3339) and id.
type inboxHistoryCursor struct {
	Before   string `json:"before"`
	BeforeID string `json:"before_id"`
}

// inboxHistoryPage is GET /api/v1/inbox/done's answer: next only when the page is full (the one after may still be
// empty).
type inboxHistoryPage struct {
	Items []InboxItem         `json:"items"`
	Next  *inboxHistoryCursor `json:"next,omitempty"`
}

func (s *Service) inboxHistoryRoutes() {
	// ?before=<done_at>&before_id=<id> (a page's next; none: from the newest), ?limit= (1 to inboxHistoryMax), and
	// ?sources=all for the connectors' items, as GET /api/v1/inbox.
	s.handle("GET /api/v1/inbox/done", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		q, err := inboxHistoryQuery(r.URL.Query())
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		page, err := s.inboxHistory(r.Context(), q)
		if err != nil {
			s.logf("inbox: history: %v", err)
			apierr.WriteCode(w, http.StatusServiceUnavailable, assistantwire.CodeInboxUnavailable, errInboxUnavailable)
			return
		}
		apierr.WriteJSON(w, page)
	})
}

// inboxHistoryQuery reads a history request's page.
func inboxHistoryQuery(v url.Values) (db.InboxDoneQuery, error) {
	q := db.InboxDoneQuery{Limit: inboxHistoryLimit, Connectors: v.Get("sources") == "all"}
	if raw := v.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > inboxHistoryMax {
			return q, errInboxHistoryLimit
		}
		q.Limit = n
	}
	switch raw := v.Get("before"); {
	case raw != "":
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return q, errInboxHistoryCursor
		}
		q.Before, q.BeforeID = at, v.Get("before_id")
	case v.Get("before_id") != "":
		return q, errInboxHistoryCursor
	}
	return q, nil
}

// inboxHistory is one page of the done items: the database's, or the ones memory holds when there is none.
func (s *Service) inboxHistory(ctx context.Context, q db.InboxDoneQuery) (inboxHistoryPage, error) {
	page := inboxHistoryPage{Items: []InboxItem{}}
	b := &s.inbox
	st := s.inboxDB()
	if st == nil {
		b.mu.Lock()
		defer b.mu.Unlock()
		done := b.doneItems(q)
		for _, it := range done {
			page.Items = append(page.Items, it.listed())
		}
		if last := len(done) - 1; last >= 0 && len(done) == q.Limit {
			page.Next = historyCursor(*done[last].DoneAt, done[last].ID)
		}
		return page, nil
	}
	rows, err := st.DoneInboxItems(ctx, q)
	if err != nil {
		return page, err
	}
	b.mu.Lock()
	for _, row := range rows {
		var it InboxItem
		if json.Unmarshal(row.Data, &it) != nil || it.ID == "" {
			continue
		}
		b.stored(&it)
		page.Items = append(page.Items, it.listed())
	}
	b.mu.Unlock()
	// Counted in rows, so a row that would not read does not end the history early.
	if last := len(rows) - 1; last >= 0 && len(rows) == q.Limit && rows[last].DoneAt != nil {
		page.Next = historyCursor(*rows[last].DoneAt, rows[last].ID)
	}
	return page, nil
}

// doneItems is q's page of the done items memory holds, ordered as the database orders them (DoneInboxItems). Under
// b.mu.
func (b *inboxBook) doneItems(q db.InboxDoneQuery) []*InboxItem {
	var out []*InboxItem
	for _, it := range b.items {
		if it.DoneAt == nil || !q.Connectors && it.Connector != nil {
			continue
		}
		if !q.Before.IsZero() && !doneBefore(*it.DoneAt, it.ID, q.Before, q.BeforeID) {
			continue
		}
		out = append(out, it)
	}
	slices.SortFunc(out, func(x, y *InboxItem) int {
		if doneBefore(*x.DoneAt, x.ID, *y.DoneAt, y.ID) {
			return 1
		}
		if doneBefore(*y.DoneAt, y.ID, *x.DoneAt, x.ID) {
			return -1
		}
		return 0
	})
	return out[:min(len(out), q.Limit)]
}

// doneBefore: an item done at at with id comes after (older than) the one done at before with beforeID in the
// history's order.
func doneBefore(at time.Time, id string, before time.Time, beforeID string) bool {
	return at.Before(before) || at.Equal(before) && id < beforeID
}

func historyCursor(at time.Time, id string) *inboxHistoryCursor {
	return &inboxHistoryCursor{Before: at.UTC().Format(time.RFC3339Nano), BeforeID: id}
}

// storedInboxItem is a done item memory no longer holds, as GET /api/v1/inbox/{id} answers it (listed, with its
// context), read from its row; false when there is none (or no database). It is not taken back into memory.
func (s *Service) storedInboxItem(ctx context.Context, id string) (InboxItem, bool, error) {
	st := s.inboxDB()
	if st == nil {
		return InboxItem{}, false, nil
	}
	rows, err := st.DoneInboxItemsByID(ctx, []string{id})
	if err != nil || len(rows) == 0 {
		return InboxItem{}, false, err
	}
	var it InboxItem
	if json.Unmarshal(rows[0], &it) != nil || it.ID != id {
		return InboxItem{}, false, nil
	}
	b := &s.inbox
	b.mu.Lock()
	b.stored(&it)
	c := it.listed()
	b.mu.Unlock()
	c.Context = it.Context
	return c, true, nil
}

// restoreInbox takes the done items of ids that only the database holds (the history past inboxForget) back into
// memory, so a reopen finds them; the ids memory has stay as they are. Only when some are missing does it read their
// rows, holding b.saving as InboxForget does, so a source's data deleted meanwhile does not come back (a reopen of
// items memory has does not wait for the saves under way). The next round forgets one that stays done.
func (s *Service) restoreInbox(ctx context.Context, ids []string) {
	st := s.inboxDB()
	if st == nil {
		return
	}
	b := &s.inbox
	missing := func() []string {
		b.mu.Lock()
		defer b.mu.Unlock()
		var out []string
		for _, id := range ids {
			if !b.has(id) && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
		return out
	}
	if len(missing()) == 0 {
		return
	}
	b.saving.Lock()
	defer b.saving.Unlock()
	gone := missing()
	if len(gone) == 0 {
		return
	}
	rows, err := st.DoneInboxItemsByID(ctx, gone)
	if err != nil {
		s.logf("inbox: read %d done items to bring back: %v", len(gone), err)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, raw := range rows {
		var it InboxItem
		if json.Unmarshal(raw, &it) != nil || it.ID == "" || it.DoneAt == nil || b.has(it.ID) {
			continue
		}
		b.stored(&it)
		b.items[it.ID] = &it
	}
}
