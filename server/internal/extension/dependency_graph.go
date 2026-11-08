package extension

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// dependencyGraph is one immutable registry snapshot. Readiness is manifest/configuration availability, not whether
// an on-demand process happens to be running. Each dependency's local settings are checked at most once per snapshot.
type dependencyGraph struct {
	registry *Registry
	ctx      context.Context
	nodes    map[string]*Extension
	missing  map[string][]string
}

func (r *Registry) dependencyGraph(ctx context.Context) *dependencyGraph {
	r.mu.Lock()
	nodes := make(map[string]*Extension, len(r.extensions))
	for id, p := range r.extensions {
		nodes[id] = snapshotExtension(p)
	}
	r.mu.Unlock()
	return &dependencyGraph{registry: r, ctx: ctx, nodes: nodes, missing: map[string][]string{}}
}

// withManifest substitutes a candidate without losing the installed settings or disabled contribution switches.
func (g *dependencyGraph) withManifest(m *Manifest) {
	old := g.nodes[m.ID]
	if old != nil && (old.Full == m || old.Manifest == m) {
		return
	}
	p := &Extension{Record: Record{ID: m.ID, Version: m.Version, Source: SourceInstalled, Enabled: true}, Full: m, Manifest: m}
	if old != nil {
		p.Record = old.Record
		p.Record.Version = m.Version
		p.Manifest = m.Without(old.Record.DisabledContributions)
	}
	p.Schema, _ = ParseSchema(m.Configuration)
	g.nodes[m.ID] = p
}

func (g *dependencyGraph) localMissing(p *Extension) []string {
	l := i18n.From(g.ctx).Or(i18n.Default)
	var out []string
	if p.Problem != "" {
		out = append(out, p.ProblemText(l))
	}
	if p.ReadOnly() {
		return out
	}
	if len(p.Manifest.Secrets) > 0 || p.Manifest.Runtime != nil {
		secrets, err := g.registry.Secrets(g.ctx, p.Record.ID)
		if err != nil {
			out = append(out, StatusErrorText(l, err))
		}
		set := map[string]bool{}
		for name, value := range secrets {
			set[name] = value != ""
		}
		for _, secret := range p.Manifest.Secrets {
			if secret.Required && !set[secret.Name] {
				out = append(out, i18n.T(l, "extension-status.missing_secret", i18n.Args{"title": secret.Title}))
			}
		}
	}
	if err := p.Schema.Check(p.Record.Config); err != nil {
		out = append(out, i18n.T(l, "extension-status.missing_config", i18n.Args{"err": StatusErrorText(l, err)}))
	}
	return out
}

func (g *dependencyGraph) state(d Dependency) DependencyState {
	state := DependencyState{ID: d.ID, Version: d.Version, State: DepOK}
	p := g.nodes[d.ID]
	if p == nil {
		state.State = DepMissing
		return state
	}
	state.Installed, state.Name = p.Record.Version, p.Full.Name
	switch {
	case p.Record.Source == SourceInstalled && !d.Allows(p.Record.Version):
		state.State = DepOld
	case !p.Record.Enabled:
		state.State = DepDisabled
	case len(g.problems(d.ID)) > 0:
		state.State = DepNotReady
	}
	return state
}

func dependencyMessage(l i18n.Lang, d DependencyState) string {
	name := d.Name
	if name == "" {
		name = d.ID
	}
	switch d.State {
	case DepMissing:
		return i18n.T(l, "extension-status.dep_install", i18n.Args{"name": name})
	case DepDisabled:
		return i18n.T(l, "extension-status.dep_enable", i18n.Args{"name": name})
	case DepOld:
		return i18n.T(l, "extension-status.dep_version", i18n.Args{"name": name, "required": d.Version, "current": d.Installed})
	}
	return ""
}

func (g *dependencyGraph) problems(id string) []string {
	l := i18n.From(g.ctx).Or(i18n.Default)
	if out, ok := g.missing[id]; ok {
		return out
	}
	p := g.nodes[id]
	if p == nil {
		return []string{i18n.T(l, "extension-status.dep_install", i18n.Args{"name": id})}
	}
	out := g.localMissing(p)
	if cycle := dependencyCycle(g.nodes, id); len(cycle) > 0 {
		out = append(out, i18n.T(l, "extension-status.dependency_cycle", i18n.Args{"path": strings.Join(cycle, " → ")}))
		g.missing[id] = out
		return out
	}
	for _, dep := range p.Full.Depends {
		state := g.state(dep)
		if state.State == DepNotReady {
			out = append(out, i18n.T(l, "extension-status.dep_not_ready", i18n.Args{"name": state.Name, "err": strings.Join(g.problems(dep.ID), i18n.T(l, "extension-status.separator"))}))
		} else if message := dependencyMessage(l, state); message != "" {
			out = append(out, message)
		}
	}
	for _, point := range sortedKeys(p.Manifest.Contributes) {
		owner, name, ok := SplitExported(point)
		if !ok {
			continue
		}
		exporter := g.nodes[owner]
		if exporter == nil {
			continue
		}
		ep := exporter.Full.Exported(name)
		if ep == nil {
			out = append(out, i18n.T(l, "extension-status.dep_no_point", i18n.Args{"name": exporter.Full.Name, "point": name}))
			continue
		}
		if err := checkExported(ep, p.Manifest.Contributes[point]); err != nil {
			out = append(out, i18n.T(l, "extension-status.dep_invalid_point", i18n.Args{"name": exporter.Full.Name, "point": name, "err": StatusErrorText(l, err)}))
		}
	}
	g.missing[id] = out
	return out
}

func (g *dependencyGraph) ready(id string) bool {
	p := g.nodes[id]
	return p != nil && p.Record.Enabled && len(g.problems(id)) == 0
}

// dependencyPath follows the full structural graph, including disabled/unsupported records, ignoring absent nodes.
func dependencyPath(nodes map[string]*Extension, from, to string) []string {
	seen := map[string]bool{}
	var visit func(string) []string
	visit = func(id string) []string {
		p := nodes[id]
		if p == nil || seen[id] {
			return nil
		}
		if id == to {
			return []string{id}
		}
		seen[id] = true
		deps := slices.Clone(p.Full.Depends)
		slices.SortFunc(deps, func(a, b Dependency) int { return strings.Compare(a.ID, b.ID) })
		for _, dep := range deps {
			if rest := visit(dep.ID); len(rest) > 0 {
				return append([]string{id}, rest...)
			}
		}
		return nil
	}
	return visit(from)
}

func dependencyCycle(nodes map[string]*Extension, id string) []string {
	p := nodes[id]
	if p == nil {
		return nil
	}
	for _, dep := range p.Full.Depends {
		if path := dependencyPath(nodes, dep.ID, id); len(path) > 0 {
			return append([]string{id}, path...)
		}
	}
	return nil
}

// checkDependencyGraph rejects newly introduced closed edges, not unchanged cycles in older records. This lets a
// user keep their old data, repair a cycle, or install an unrelated extension. Called with r.mu held.
func (r *Registry) checkDependencyGraph(candidates ...*Manifest) error {
	return checkCandidateDependencyGraph(r.extensions, candidates...)
}

func checkCandidateDependencyGraph(installed map[string]*Extension, candidates ...*Manifest) error {
	nodes := maps.Clone(installed)
	seen := map[string]bool{}
	for _, m := range candidates {
		if seen[m.ID] {
			return i18n.Err("extension-status.duplicate_candidate", i18n.Args{"id": m.ID})
		}
		seen[m.ID] = true
		nodes[m.ID] = &Extension{Full: m}
	}
	for _, m := range candidates {
		old := installed[m.ID]
		for _, dep := range m.Depends {
			if old != nil && old.Full.DependsOn(dep.ID) {
				continue
			}
			if path := dependencyPath(nodes, dep.ID, m.ID); len(path) > 0 {
				return i18n.Err("extension-status.dependency_cycle", i18n.Args{"path": strings.Join(append([]string{m.ID}, path...), " → ")})
			}
		}
	}
	return nil
}

// ValidateDependencyGraph checks all candidate replacements together, without writing or activating any extension.
func (r *Registry) ValidateDependencyGraph(candidates ...*Manifest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.checkDependencyGraph(candidates...)
}

// DependencyOrder is the changed extensions and all their transitive dependents, dependencies first. No ids means
// all extensions. Cyclic records are still returned (their Missing diagnosis prevents activation), never deleted.
func (r *Registry) DependencyOrder(ids ...string) []*Extension {
	g := r.dependencyGraph(context.Background())
	selected := map[string]bool{}
	if len(ids) == 0 {
		for id := range g.nodes {
			selected[id] = true
		}
	} else {
		for _, id := range ids {
			selected[id] = true
		}
		for changed := true; changed; {
			changed = false
			for id, p := range g.nodes {
				if selected[id] {
					continue
				}
				for _, dep := range p.Full.Depends {
					if selected[dep.ID] {
						selected[id] = true
						changed = true
						break
					}
				}
			}
		}
	}
	var out []*Extension
	seen := map[string]bool{}
	var visit func(string)
	visit = func(id string) {
		if seen[id] || !selected[id] {
			return
		}
		seen[id] = true
		p := g.nodes[id]
		if p == nil {
			return
		}
		deps := slices.Clone(p.Full.Depends)
		slices.SortFunc(deps, func(a, b Dependency) int { return strings.Compare(a.ID, b.ID) })
		for _, dep := range deps {
			visit(dep.ID)
		}
		out = append(out, p)
	}
	for _, id := range slices.Sorted(maps.Keys(selected)) {
		visit(id)
	}
	return out
}
