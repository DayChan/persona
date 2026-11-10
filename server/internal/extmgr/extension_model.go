package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The extension model in the hub (docs/extensions/extensions.md "Dependencies, exports and packs", #1854): what installs
// together (a preview's bundle: missing dependencies, a pack's members), what a switch-off or removal takes with it
// (dependents), and telling an exporter its contributions changed.

// bundleDepth is how deep dependencies of dependencies are followed.
const bundleDepth = 4

// resolveBundle gathers the constraints of the whole graph before downloading, then builds the confirmation preview in
// dependency order.
func (h *Manager) resolveBundle(ctx context.Context, pv *extension.Preview, market string) {
	if h.extensions == nil || h.marketStore() == nil {
		return
	}
	h.stageBundle(ctx, pv, market)
}

// bundleRef is one preview of a bundle as the install request sends it back.
type bundleRef struct {
	PreviewID string `json:"preview_id"`
	Digest    string `json:"permissions_digest"`
}

// installExtensionPlan validates and commits the members and main package together; no caller installs the main
// package as a separate operation afterward. Existing consent fields and permission checks are unchanged.
func (h *Manager) installExtensionPlan(ctx context.Context, id, previewID, digest string, refs []bundleRef, actor string) (*extension.Extension, error) {
	consent := make([]extension.PreviewRef, 0, len(refs))
	for _, ref := range refs {
		consent = append(consent, extension.PreviewRef{PreviewID: ref.PreviewID, Digest: ref.Digest})
	}
	if id == "" {
		return h.extensions.reg.InstallPlan(ctx, previewID, digest, consent, actor)
	}
	return h.extensions.reg.UpgradePlan(ctx, id, previewID, digest, consent, actor)
}

// DependentView is an extension that needs another, for the refusal of switching that one off.
type DependentView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// dependentsOf follows the complete dependent graph of ids, deepest first.
func (ps *extensions) dependentsOf(ids []string) []DependentView {
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	var out []DependentView
	var visit func(string)
	visit = func(id string) {
		for _, p := range ps.reg.Dependents(id) {
			if seen[p.Record.ID] {
				continue
			}
			seen[p.Record.ID] = true
			visit(p.Record.ID)
			if p.Record.Enabled {
				out = append(out, DependentView{ID: p.Record.ID, Name: p.Full.Name})
			}
		}
	}
	for _, id := range ids {
		visit(id)
	}
	return out
}

// takeDependents asks before switching off dependents; no write precedes the full read-only check.
func (h *Manager) takeDependents(w http.ResponseWriter, r *http.Request, id string, cascade bool) bool {
	return h.takeDependentsOf(w, r, []string{id}, cascade)
}

func (h *Manager) takeDependentsOf(w http.ResponseWriter, r *http.Request, ids []string, cascade bool) bool {
	_, ok := h.takeDependentsSwitched(w, r, ids, cascade)
	return ok
}

// takeDependentsSwitched is takeDependentsOf saying which dependents it switched off (cascade), for a caller that has
// to put them back. When one cannot be switched off, those already switched go back on.
func (h *Manager) takeDependentsSwitched(w http.ResponseWriter, r *http.Request, ids []string, cascade bool) (switched []string, ok bool) {
	deps := h.extensions.dependentsOf(ids)
	if len(deps) == 0 {
		return nil, true
	}
	if !cascade {
		var names []string
		for _, d := range deps {
			names = append(names, d.Name)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "has_dependents", "dependents": deps,
			"error": i18n.T(h.requestLang(r), "hub-extension_model.has_dependents", i18n.Args{"names": strings.Join(names, i18n.T(h.requestLang(r), "hub-extension_model.list_separator"))})})
		return nil, false
	}
	for _, d := range deps {
		if p := h.extensions.reg.Get(d.ID); p != nil && p.ReadOnly() {
			writeErrCode(w, http.StatusConflict, "read_only", i18n.Err("hub-extension_model.read_only", i18n.Args{"name": d.Name}))
			return nil, false
		}
	}
	actor := actorOr(r, "personad extension")
	for _, d := range deps {
		if err := h.extensions.reg.SetEnabled(r.Context(), d.ID, false, actor); err != nil {
			h.extensions.turnBackOn(r.Context(), switched, actor)
			writeErrCode(w, http.StatusConflict, "read_only", err)
			return nil, false
		}
		switched = append(switched, d.ID)
	}
	return switched, true
}

// setByUser is the user switching extension id by itself (not a pack): its pack forgets switching it off, so turning
// the pack back on leaves it as the user set it. The caller holds switchMu.
func (h *Manager) setByUser(ctx context.Context, id string, on bool, actor string) error {
	ps := h.extensions
	if err := ps.reg.SetEnabled(ctx, id, on, actor); err != nil {
		return err
	}
	if err := ps.reg.ForgetPackOff(ctx, id); err != nil {
		h.logf("extensions: %s switched by itself, but its pack still remembers switching it off: %v", id, err)
	}
	return nil
}

// switchExtension is PUT …/-/enabled of the regular and admin routes; it writes the error itself and then says false.
// A pack takes its installed members with it (docs/extensions/extensions.md "Dependencies, exports and packs", #1830):
// off switches off the members that are on, after the same has_dependents question as for the pack's own
// dependents, and remembers them on the pack (Record.PackOff); on switches the pack and then those members back on,
// not the ones the user had turned off. A member switched by itself is forgotten by its pack. A member that cannot go
// back on is a warning, the pack is on either way. Switches are one at a time (switchMu), so two devices switching a
// pack, or a member switched while its pack goes off, do not lose or bring back the pack's record.
func (h *Manager) switchExtension(w http.ResponseWriter, r *http.Request, p *extension.Extension, on, cascade bool, actor string) (warnings []string, ok bool) {
	ps, id, ctx := h.extensions, p.Record.ID, r.Context()
	ps.switchMu.Lock()
	defer ps.switchMu.Unlock()
	if !p.Full.IsPack() {
		if !on && !h.takeDependents(w, r, id, cascade) {
			return nil, false
		}
		if err := h.setByUser(ctx, id, on, actor); err != nil {
			writeExtensionChangeError(w, r, http.StatusConflict, "read_only", err)
			return nil, false
		}
		return nil, true
	}
	cur := ps.reg.Snapshot(id)
	if cur == nil {
		writeExtensionResponseError(w, r, http.StatusNotFound, "not_found", errNoExtension)
		return nil, false
	}
	if on {
		if err := ps.reg.SetPackEnabled(ctx, id, true, nil, actor); err != nil {
			writeExtensionChangeError(w, r, http.StatusConflict, "read_only", err)
			return nil, false
		}
		pending, failed := ps.turnOnAll(ctx, cur.Record.PackOff, actor)
		lang := h.contextLang(ctx)
		for _, mid := range pending {
			name := mid
			if m := ps.reg.Snapshot(mid); m != nil {
				name = m.Full.Name
			}
			warnings = append(warnings, i18n.T(lang, "hub-extension_model.pack_member_not_on", i18n.Args{"name": name, "error": i18n.Localize(failed[mid], lang)}))
		}
		return warnings, true
	}
	// Only the members that are on: one already off stays as it is, and its dependents are not asked about.
	var members []string
	for _, m := range ps.packMembersOn(p) {
		members = append(members, m.ID)
	}
	cascaded, ok := h.takeDependentsSwitched(w, r, append([]string{id}, members...), cascade)
	if !ok {
		return nil, false
	}
	off := slices.Clone(cur.Record.PackOff)
	var done []string
	undo := func(err error) (warnings []string, ok bool) {
		ps.turnBackOn(ctx, append(done, cascaded...), actor) // the members first, then what depends on them
		writeExtensionChangeError(w, r, http.StatusConflict, "read_only", err)
		return nil, false
	}
	for _, mid := range members {
		m := ps.reg.Snapshot(mid)
		if m == nil || !m.Record.Enabled {
			continue // off already: the user's, left off when the pack goes on
		}
		if err := ps.reg.SetEnabled(ctx, mid, false, actor); err != nil {
			return undo(err)
		}
		done = append(done, m.Record.ID)
		if !slices.Contains(off, m.Record.ID) {
			off = append(off, m.Record.ID)
		}
	}
	if err := ps.reg.SetPackEnabled(ctx, id, false, off, actor); err != nil {
		return undo(err)
	}
	return nil, true
}

// turnOnAll switches the installed extensions ids on that are off, in one pass: enabling does not wait for a
// dependency (one that is off leaves its dependent waiting until it goes on), so the order does not matter. pending
// are the ones still off, with why (failed).
func (ps *extensions) turnOnAll(ctx context.Context, ids []string, actor string) (pending []string, failed map[string]error) {
	failed = map[string]error{}
	for _, id := range ids {
		m := ps.reg.Snapshot(id)
		if m == nil || m.Record.Source != extension.SourceInstalled || m.Record.Enabled {
			continue // removed since, or already on again
		}
		if err := ps.reg.SetEnabled(ctx, id, true, actor); err != nil {
			failed[id] = err
			pending = append(pending, id)
		}
	}
	return pending, failed
}

// turnBackOn undoes a switch-off that could not finish: ids, which it switched off, go back on, whatever their source.
func (ps *extensions) turnBackOn(ctx context.Context, ids []string, actor string) {
	for _, id := range ids {
		if m := ps.reg.Snapshot(id); m == nil || m.Record.Enabled {
			continue
		}
		if err := ps.reg.SetEnabled(ctx, id, true, actor); err != nil && ps.logf != nil {
			ps.logf("extensions: %s was switched off by a switch that did not finish, and could not go back on: %v", id, err)
		}
	}
}

// packMembers are the installed members of pack p, to remove with it.
func (ps *extensions) packMembers(p *extension.Extension) []string {
	var out []string
	for _, m := range p.Full.ExtensionPack {
		if cur := ps.reg.Get(m.ID); cur != nil && cur.Record.Source == extension.SourceInstalled {
			out = append(out, m.ID)
		}
	}
	return out
}

// packMembersOn are the installed members of pack p that are on: the ones switching the pack off switches off.
func (ps *extensions) packMembersOn(p *extension.Extension) []DependentView {
	var out []DependentView
	for _, mid := range ps.packMembers(p) {
		if m := ps.reg.Snapshot(mid); m != nil && m.Record.Enabled {
			out = append(out, DependentView{ID: m.Record.ID, Name: m.Full.Name})
		}
	}
	return out
}

// modelChanged follows a change of one extension through the others: the ones depending on it start or wait
// (Missing), and the extensions it contributes to hear of their contributions (extension.contributions_changed).
func (ps *extensions) modelChanged(ctx context.Context, c extension.Change) []*extension.Extension {
	affected := ps.reg.DependencyOrder(c.ID)
	var to []string
	for _, p := range affected {
		if p.Record.ID != c.ID {
			ps.apply(ctx, p.Record.ID)
		}
		for _, id := range p.Full.ContributesTo() {
			if !slices.Contains(to, id) {
				to = append(to, id)
			}
		}
	}
	if c.Old != nil {
		for _, id := range c.Old.Full.ContributesTo() {
			if !slices.Contains(to, id) {
				to = append(to, id)
			}
		}
	}
	for _, id := range to {
		if hp := ps.get(id); hp != nil && hp.proc != nil {
			hp.contributionsMu.Lock()
			curr := ps.reg.ContributionsToContext(ctx, id)
			if !sameContributions(hp.contributions, curr) {
				if err := hp.proc.Notify("extension.contributions_changed", map[string]any{"contributions_to_me": curr}); err == nil {
					hp.contributions = curr
				}
			}
			hp.contributionsMu.Unlock()
		}
	}
	return affected
}

// Registry snapshots have a stable extension/item order; nil and empty maps both mean no contributions.
func sameContributions(a, b map[string][]extension.ExportedContribution) bool {
	if len(a) != len(b) {
		return false
	}
	for point, items := range a {
		other, ok := b[point]
		if !ok || !slices.EqualFunc(items, other, func(a, b extension.ExportedContribution) bool {
			return a.Extension == b.Extension && bytes.Equal(a.Item, b.Item)
		}) {
			return false
		}
	}
	return true
}
