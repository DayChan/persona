package extmgr

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Pick the whole reachable graph first, then download packages. When a version changes, collect the constraints again
// and drop the old candidate's dependencies.
type bundleRequirement struct {
	version, spec, prefer string
	depth                 int
}
type bundleChoice struct {
	manifest  *extension.Manifest
	installed bool
	market    extension.MarketRecord
	entry     *extension.MarketEntry
}

const bundlePlanningRounds = 128

func choiceSignature(choices map[string]bundleChoice) string {
	stable := map[string]string{}
	for id, choice := range choices {
		raw, _ := json.Marshal(choice.manifest)
		stable[id] = fmt.Sprintf("%t/%s/%x", choice.installed, choice.market.ID, sha256.Sum256(raw))
	}
	raw, _ := json.Marshal(stable)
	return string(raw)
}

func (h *Manager) bundleUnresolved(ctx context.Context, id string, why error) string {
	l := h.contextLang(ctx)
	text := i18n.Localize(why, l)
	if joined, ok := why.(interface{ Unwrap() []error }); ok {
		var parts []string
		for _, err := range joined.Unwrap() {
			parts = append(parts, i18n.Localize(err, l))
		}
		text = strings.Join(parts, i18n.T(l, "hub-extension_model.list_separator"))
	}
	return i18n.T(l, "hub-extension_model.unresolved", i18n.Args{"id": id, "why": text})
}

func bundleMarketSpec(requirements []bundleRequirement) (string, error) {
	var spec string
	for _, req := range requirements {
		if req.spec == "" {
			continue
		}
		parsed, err := extension.ParseMarket(req.spec)
		if err != nil {
			return "", err
		}
		if spec != "" && spec != parsed.String() {
			return "", i18n.Err("hub-extension_model.conflicting_markets")
		}
		spec = parsed.String()
	}
	return spec, nil
}

func (h *Manager) chooseBundle(ctx context.Context, root *extension.Manifest, market string) (map[string]bundleChoice, []string) {
	installed := map[string]*extension.Extension{}
	for _, p := range h.extensions.reg.DependencyOrder() {
		installed[p.Record.ID] = p
	}
	choices := map[string]bundleChoice{root.ID: {manifest: root}}
	var failures []string
	for round := 0; round < bundlePlanningRounds; round++ {
		if err := ctx.Err(); err != nil {
			return choices, []string{err.Error()}
		}
		requirements := map[string][]bundleRequirement{}
		visited := map[string]int{}
		var walk func(string, int)
		walk = func(id string, depth int) {
			if old, ok := visited[id]; ok && old <= depth {
				return
			}
			visited[id] = depth
			choice, ok := choices[id]
			if !ok {
				if p := installed[id]; p != nil {
					choice = bundleChoice{manifest: p.Full, installed: true}
				} else {
					return
				}
			}
			prefer := choice.market.ID
			if id == root.ID {
				prefer = market
			} else if choice.installed {
				prefer = extension.FromMarket(installed[id].Record.Origin)
			}
			add := func(child, version, spec string) {
				requirements[child] = append(requirements[child], bundleRequirement{version, spec, prefer, depth + 1})
				walk(child, depth+1)
			}
			for _, dep := range choice.manifest.Depends {
				add(dep.ID, dep.Version, "")
			}
			for _, member := range choice.manifest.ExtensionPack {
				add(member.ID, member.Version, member.Market)
			}
		}
		walk(root.ID, 0)
		next := map[string]bundleChoice{root.ID: {manifest: root}}
		failures = nil
		for _, id := range slices.Sorted(maps.Keys(requirements)) {
			reqs := requirements[id]
			if _, err := bundleMarketSpec(reqs); err != nil {
				failures = append(failures, h.bundleUnresolved(ctx, id, err))
				continue
			}
			allows := func(version string) bool {
				for _, req := range reqs {
					if !(extension.Dependency{ID: id, Version: req.version}).Allows(version) {
						return false
					}
				}
				return true
			}
			if id == root.ID {
				if !allows(root.Version) {
					failures = append(failures, h.bundleUnresolved(ctx, id, i18n.Err("hub-extension_model.root_version_constraint")))
				}
				continue
			}
			if p := installed[id]; p != nil && (p.Record.Source != extension.SourceInstalled || allows(p.Record.Version)) {
				next[id] = bundleChoice{manifest: p.Full, installed: true}
				continue
			}
			inRange := false
			for _, req := range reqs {
				inRange = inRange || req.depth <= bundleDepth
			}
			if !inRange {
				failures = append(failures, h.bundleUnresolved(ctx, id, i18n.Err("hub-extension_model.too_deep")))
				continue
			}
			choice, err := h.findBundleChoice(id, reqs, market)
			if err != nil {
				failures = append(failures, h.bundleUnresolved(ctx, id, err))
				continue
			}
			next[id] = choice
		}
		if choiceSignature(next) == choiceSignature(choices) {
			return next, failures
		}
		choices = next
	}
	return choices, append(failures, i18n.T(h.contextLang(ctx), "hub-extension_model.unstable_plan"))
}

func (h *Manager) findBundleChoice(id string, requirements []bundleRequirement, rootMarket string) (bundleChoice, error) {
	spec, err := bundleMarketSpec(requirements)
	if err != nil {
		return bundleChoice{}, err
	}
	preferences := map[string]bool{}
	var ranges []string
	for _, req := range requirements {
		if req.prefer != "" {
			preferences[req.prefer] = true
		}
		if req.version != "" && !slices.Contains(ranges, req.version) {
			ranges = append(ranges, req.version)
		}
	}
	slices.Sort(ranges)
	ms := h.marketsState()
	ms.mu.Lock()
	var order []string
	if spec != "" {
		if state := ms.withSpec(spec); state != nil {
			order = append(order, state.rec.ID)
		}
	} else {
		if rootMarket != "" && ms.byID[rootMarket] != nil {
			order = append(order, rootMarket)
		}
		for _, mid := range slices.Sorted(maps.Keys(preferences)) {
			if ms.byID[mid] != nil && !slices.Contains(order, mid) {
				order = append(order, mid)
			}
		}
		for _, mid := range slices.Sorted(maps.Keys(ms.byID)) {
			if !slices.Contains(order, mid) {
				order = append(order, mid)
			}
		}
	}
	ms.mu.Unlock()
	if len(order) == 0 {
		if spec != "" {
			return bundleChoice{}, i18n.Err("hub-extension_model.market_not_registered", i18n.Args{"spec": spec})
		}
		return bundleChoice{}, i18n.Err("hub-extension_model.not_in_markets")
	}
	var last error
	for _, mid := range order {
		rec, entry, err := h.marketEntry(mid, id)
		if err != nil {
			last = err
			continue
		}
		allowed := true
		for _, req := range requirements {
			allowed = allowed && (extension.Dependency{ID: id, Version: req.version}).Allows(entry.Parsed.Version)
		}
		if allowed {
			return bundleChoice{manifest: entry.Parsed, market: rec, entry: entry}, nil
		}
	}
	return bundleChoice{}, errors.Join(i18n.Err("hub-extension_model.all_version_constraints", i18n.Args{"ranges": strings.Join(ranges, " / ")}), last)
}

func (h *Manager) extensionPlanCandidates(ctx context.Context, root *extension.Manifest, market string) []*extension.Manifest {
	if h.marketStore() == nil {
		return nil
	}
	choices, unresolved := h.chooseBundle(ctx, root, market)
	if len(unresolved) > 0 {
		return nil
	}
	var candidates []*extension.Manifest
	for _, id := range slices.Sorted(maps.Keys(choices)) {
		if choice := choices[id]; !choice.installed {
			candidates = append(candidates, choice.manifest)
		}
	}
	return candidates
}

func (h *Manager) guardExtensionPlan(ctx context.Context, previews []*extension.Preview) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !slices.ContainsFunc(previews, func(pv *extension.Preview) bool { return pv.Market != "" }) {
		return nil, nil
	}
	for _, pv := range previews {
		if pv.RenameFrom != "" {
			if err := h.checkRenameNodeCapabilities(ctx, h.extensions.reg.SnapshotExact(pv.RenameFrom)); err != nil {
				return nil, err
			}
		}
	}
	ms := h.marketsState()
	ms.mu.Lock()
	fail := func(err error) (func(), error) { ms.mu.Unlock(); return nil, err }
	for _, pv := range previews {
		if pv.Market == "" {
			continue
		}
		rec, entry, err := marketEntryLocked(ms, pv.Market, pv.Manifest.ID)
		if err != nil {
			return fail(err)
		}
		if pv.RenameFrom != "" {
			old := h.extensions.reg.SnapshotExact(pv.RenameFrom)
			target := h.extensions.reg.Snapshot(pv.Manifest.ID)
			if entry.Parsed.ID != pv.Manifest.ID || entry.Replaces != pv.RenameFrom || old == nil || old.Record.Source != extension.SourceInstalled || old.Record.Origin != "market:"+pv.Market+"/"+pv.RenameFrom || (target != nil && target.Record.StorageKey() != old.Record.StorageKey()) {
				return fail(i18n.Err("extension-identity.invalid"))
			}
		}
		spec, key := pv.MarketTrust()
		currentKey := rec.SigningKey
		if extension.IsOfficialMarket(rec.Spec) {
			currentKey = extension.Official.SigningKey
		}
		if spec != rec.Spec || key != currentKey {
			return fail(i18n.Err("hub-extension_model.market_trust_changed"))
		}
	}
	return ms.mu.Unlock, nil
}

func (h *Manager) stageBundle(ctx context.Context, pv *extension.Preview, market string) {
	reg := h.extensions.reg
	choices, unresolved := h.chooseBundle(ctx, pv.Manifest, market)
	pv.Unresolved = append(pv.Unresolved, unresolved...)
	var candidates []*extension.Manifest
	for _, id := range slices.Sorted(maps.Keys(choices)) {
		if choice := choices[id]; !choice.installed {
			candidates = append(candidates, choice.manifest)
		}
	}
	graphErr := reg.ValidateDependencyGraph(candidates...)
	if graphErr != nil {
		pv.Unresolved = append(pv.Unresolved, graphErr.Error())
	}
	seen := map[string]bool{}
	var order []string
	var walk func(string)
	walk = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		choice, ok := choices[id]
		if !ok {
			return
		}
		for _, dep := range choice.manifest.Depends {
			walk(dep.ID)
		}
		for _, member := range choice.manifest.ExtensionPack {
			walk(member.ID)
		}
		if id == pv.Manifest.ID || choice.installed {
			return
		}
		order = append(order, id)
	}
	walk(pv.Manifest.ID)
	// Every member is downloaded and checked at the same time (a pack of three took three downloads one after the
	// other, #3277); they join the bundle in dependency order.
	members := make([]*extension.Preview, len(order))
	why := make([]string, len(order))
	var wg sync.WaitGroup
	for i, id := range order {
		choice := choices[id]
		mk, err := h.market(choice.market)
		if err != nil {
			why[i] = h.bundleUnresolved(ctx, id, err)
			continue
		}
		upgrade := ""
		if old := reg.Snapshot(id); old != nil && old.Record.Source == extension.SourceInstalled {
			upgrade = id
		}
		var extra []*extension.Manifest
		if graphErr == nil {
			extra = candidates
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			member, err := reg.PreviewMarket(ctx, mk, choice.market.ID, choice.entry, upgrade, extra...)
			switch {
			case err != nil:
				why[i] = h.bundleUnresolved(ctx, id, err)
			case !extension.ManifestsEqual(choice.manifest, member.Manifest):
				reg.Discard(member.ID)
				why[i] = h.bundleUnresolved(ctx, id, i18n.Err("hub-extension_model.download_plan_mismatch"))
			default:
				members[i] = member
			}
		}()
	}
	wg.Wait()
	for i := range order {
		if members[i] == nil {
			pv.Unresolved = append(pv.Unresolved, why[i])
			continue
		}
		pv.Bundle = append(pv.Bundle, members[i])
	}
}
