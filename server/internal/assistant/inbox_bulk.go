package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"

	"github.com/nwverse/persona/server/internal/apierr"
)

// Filing many items (or bringing them back) in one request: Clean up all over what the list shows, Mark all as seen / Clear all on
// a section, and their Undo (docs/assistant/inbox.md"Clean up all"). Done stays in Persona, as for one item: Feishu, the mail
// servers and the connectors are not told.

// inboxBulkMax caps one request (the list holds a few hundred items at most).
const inboxBulkMax = 1000

// Why an item of a bulk request was left as it was.
const (
	bulkSkipMissing = "missing" // no such item (forgotten, or never there)
	bulkSkipChanged = "changed" // new messages came into it after the list the user acted on
)

type inboxBulkTarget struct {
	ID string `json:"id"`
	// Version is the item's `version` as listed; empty skips the check (the item as it is now).
	Version string `json:"version,omitempty"`
}

type inboxBulkSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type inboxBulkResult struct {
	// Changed are the items this request moved (done or back), as listed now: "Undo" sends these back.
	Changed []InboxItem `json:"changed"`
	// Unchanged were already in the state asked for (a retried request, another device): nothing written.
	Unchanged []string        `json:"unchanged"`
	Skipped   []inboxBulkSkip `json:"skipped"`
	// Failed are the ids whose requested state could not be written or confirmed while another filing is pending.
	// The client keeps them in the list and can retry. Additive: older clients see no changed entry for those ids.
	Failed []string `json:"failed"`
}

// inboxVersion names what an item holds now (its newest message / mail / notice): it changes when something new
// comes into the item, not when it is triaged, read elsewhere or filed.
func inboxVersion(it *InboxItem) string {
	sum := sha256.Sum256([]byte(it.ID + "\x00" + it.Last))
	return hex.EncodeToString(sum[:8])
}

// Bulk actions (POST /api/v1/inbox/bulk).
const (
	// bulkDone files as handled (done_by=user): the sections' Mark all as seen / Clear all, and Mark all as done from clients
	// released before #5114, which still send it.
	bulkDone = "done"
	// bulkDismiss files as set aside (done_by=dismissed): Clean up all, and Dismiss all from clients before #5114.
	bulkDismiss = "dismiss"
	bulkReopen  = "reopen" // Move back / Undo
)

// doneByOf is done_by for the user filing an item: "Done" or "Ignore".
func doneByOf(dismiss bool) string {
	if dismiss {
		return "dismissed"
	}
	return "user"
}

// inboxBulkFilter picks the open items a bulk action without ids is for: what the list shows under a source / tier /
// kind filter. A search, or anything the client filters by itself, goes as ids.
type inboxBulkFilter struct {
	Sources string   `json:"sources,omitempty"` // "all": connectors' too (as GET /api/v1/inbox?sources=all)
	Source  string   `json:"source,omitempty"`  // lark / mail / a connector's id
	Account string   `json:"account,omitempty"` // feishu / a mail account id
	Tier    string   `json:"tier,omitempty"`    // action / fyi / noise / untriaged
	Kinds   []string `json:"kinds,omitempty"`
}

func (f *inboxBulkFilter) valid() bool {
	switch f.Tier {
	case "", TierAction, TierFYI, TierNoise, "untriaged":
		return true
	}
	return false
}

func (f *inboxBulkFilter) match(it *InboxItem) bool {
	tier := it.Tier
	if tier == "" {
		tier = "untriaged"
	}
	return it.open() && (f.Sources == "all" || it.Connector == nil) && (f.Source == "" || it.Source == f.Source) &&
		(f.Account == "" || it.Account == f.Account) && (f.Tier == "" || tier == f.Tier) &&
		(len(f.Kinds) == 0 || slices.Contains(f.Kinds, it.Kind))
}

// A list may have exposed any pending reopen, even when both the latest marker and the stored base are closed.
// Include every open state that can still commit, so admission reports Failed until the pending chain resolves.
// Filing changes only DoneAt among the filter fields; apply the other fields to the current item. Under mu.
func (b *inboxBook) matchesBulkFilter(it *InboxItem, f *inboxBulkFilter) bool {
	open := *it
	open.DoneAt = nil
	if !f.match(&open) {
		return false
	}
	if it.open() {
		return true
	}
	latest := b.filing[it.ID]
	if latest == nil || !b.holdsFiling(latest) {
		return false
	}
	for w := latest; w != nil && (!w.settled || w.failed); w = w.previousWrite {
		if !w.settled && w.applied.DoneAt == nil {
			return true
		}
	}
	committed, _ := b.filingSnapshot(it, nil)
	return committed.open()
}

// setInboxDoneBulk files (done) or brings back (!done) the targets in one go: setInboxBulk without a filter.
func (s *Service) setInboxDoneBulk(ctx context.Context, targets []inboxBulkTarget, done bool, now time.Time) inboxBulkResult {
	action := bulkReopen
	if done {
		action = bulkDone
	}
	return s.setInboxBulk(ctx, action, targets, nil, time.Time{}, now)
}

// setInboxBulk files (done / dismiss) or brings back (reopen) the targets, or with a filter the open items it picks
// that arrived by asOf, in one go, written and announced once: what one item's "Done" does (setInboxDone), many times.
// Filing is idempotent (an item already done keeps when and why it was done) and leaves what changed since the list
// the user acted on (a target's version, or under a filter what arrived after asOf), so what came in meanwhile is
// not filed unseen. Bringing back does not check it (an item done meanwhile by a reply elsewhere comes back too, as
// with "Move back" on one item).
func (s *Service) setInboxBulk(ctx context.Context, action string, targets []inboxBulkTarget, filter *inboxBulkFilter,
	asOf, now time.Time) inboxBulkResult {
	done := action != bulkReopen
	res := inboxBulkResult{Changed: []InboxItem{}, Unchanged: []string{}, Skipped: []inboxBulkSkip{}, Failed: []string{}}
	var writes []*inboxFiling
	seen := map[string]bool{}
	b := &s.inbox
	b.mu.Lock()
	if filter != nil {
		targets = nil
		for _, it := range b.items {
			if b.matchesBulkFilter(it, filter) {
				if it.arrivedAfter(asOf) {
					res.Skipped = append(res.Skipped, inboxBulkSkip{it.ID, bulkSkipChanged})
				} else {
					targets = append(targets, inboxBulkTarget{ID: it.ID})
				}
			}
		}
		slices.SortFunc(targets, func(a, b inboxBulkTarget) int { return strings.Compare(a.ID, b.ID) })
		slices.SortFunc(res.Skipped, func(a, b inboxBulkSkip) int { return strings.Compare(a.ID, b.ID) })
	}
	for _, t := range targets {
		if seen[t.ID] {
			continue
		}
		seen[t.ID] = true
		it, ok := b.items[t.ID]
		pending := b.filing[t.ID]
		switch {
		case !ok:
			res.Skipped = append(res.Skipped, inboxBulkSkip{t.ID, bulkSkipMissing})
		case pending != nil && b.holdsFiling(pending):
			// Its optimistic marker may still roll back. Only a committed state can be acknowledged unchanged.
			res.Failed = append(res.Failed, t.ID)
		case done && !it.open(), !done && it.open():
			res.Unchanged = append(res.Unchanged, t.ID)
		case done && t.Version != "" && t.Version != inboxVersion(it):
			res.Skipped = append(res.Skipped, inboxBulkSkip{t.ID, bulkSkipChanged})
		default:
			w := b.beginFiling(it, now, doneByOf(action == bulkDismiss))
			if done {
				b.file(it, now, w.why)
				w.close = true
			} else {
				it.DoneAt, it.DoneBy = nil, ""
			}
			b.settleFiling(w)
			writes = append(writes, w)
		}
	}
	b.mu.Unlock()
	failed := s.saveInboxFilings(ctx, writes)
	b.mu.Lock()
	for _, w := range writes {
		if failed[w.it.ID] {
			res.Failed = append(res.Failed, w.it.ID)
		} else {
			res.Changed = append(res.Changed, w.it.listed())
		}
	}
	b.mu.Unlock()
	slices.Sort(res.Failed)
	return res
}

var errBulkTooMany = i18n.ErrEnglish("inbox-errors.bulk_limit")

// inboxBulkReply is POST /api/v1/inbox/bulk's answer: the result, the changed ids, and the request that undoes it.
type inboxBulkReply struct {
	Action string `json:"action"`
	inboxBulkResult
	IDs  []string       `json:"ids"`
	Undo *inboxBulkUndo `json:"undo,omitempty"`
}

type inboxBulkUndo struct {
	Action string   `json:"action"`
	IDs    []string `json:"ids"`
}

func (s *Service) inboxBulkRoutes() {
	// Many items at once: {"done": true|false, "items": [{"id", "version"}]} (#1105; POST /api/v1/inbox/bulk is the
	// general one).
	s.handle("POST /api/v1/inbox/done", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			Done  *bool             `json:"done"`
			Items []inboxBulkTarget `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Done == nil {
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.bulk_done_required"))
			return
		}
		if len(in.Items) > inboxBulkMax {
			apierr.Write(w, http.StatusRequestEntityTooLarge, errBulkTooMany)
			return
		}
		apierr.WriteJSON(w, s.setInboxDoneBulk(r.Context(), in.Items, *in.Done, time.Now()))
	})
	// {"action": "done"|"dismiss"|"reopen", "items"?: [{"id","version"}], "ids"?: [...], "filter"?: {...}, "as_of"?}:
	// the ids (items) the user sees, or the filter's open items as of the list (as_of from GET /api/v1/inbox).
	s.handle("POST /api/v1/inbox/bulk", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			Action string            `json:"action"`
			Items  []inboxBulkTarget `json:"items"`
			IDs    []string          `json:"ids"`
			Filter *inboxBulkFilter  `json:"filter"`
			AsOf   time.Time         `json:"as_of"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		for _, id := range in.IDs {
			in.Items = append(in.Items, inboxBulkTarget{ID: id})
		}
		switch {
		case in.Action != bulkDone && in.Action != bulkDismiss && in.Action != bulkReopen:
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.bulk_action"))
			return
		case in.Filter != nil && len(in.Items) > 0, in.Filter == nil && in.Items == nil:
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.bulk_targets"))
			return
		case in.Filter != nil && (in.Action == bulkReopen || in.AsOf.IsZero() || !in.Filter.valid()):
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.bulk_filter"))
			return
		case len(in.Items) > inboxBulkMax:
			apierr.Write(w, http.StatusRequestEntityTooLarge, errBulkTooMany)
			return
		}
		res := s.setInboxBulk(r.Context(), in.Action, in.Items, in.Filter, in.AsOf, time.Now())
		out := inboxBulkReply{Action: in.Action, inboxBulkResult: res, IDs: make([]string, 0, len(res.Changed))}
		for _, it := range res.Changed {
			out.IDs = append(out.IDs, it.ID)
		}
		if in.Action != bulkReopen && len(out.IDs) > 0 {
			out.Undo = &inboxBulkUndo{Action: bulkReopen, IDs: out.IDs}
		}
		apierr.WriteJSON(w, out)
	})
}
