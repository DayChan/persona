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

// The extension model in the hub (docs/extensions/extensions.md "Dependencies, exports and bundles", #1854): what installs
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
	deps := h.extensions.dependentsOf(ids)
	if len(deps) == 0 {
		return true
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
		return false
	}
	for _, d := range deps {
		if p := h.extensions.reg.Get(d.ID); p != nil && p.ReadOnly() {
			writeErrCode(w, http.StatusConflict, "read_only", i18n.Err("hub-extension_model.read_only", i18n.Args{"name": d.Name}))
			return false
		}
	}
	for _, d := range deps {
		if err := h.extensions.reg.SetEnabled(r.Context(), d.ID, false, actorOr(r, "personad extension")); err != nil {
			writeErrCode(w, http.StatusConflict, "read_only", err)
			return false
		}
	}
	return true
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
