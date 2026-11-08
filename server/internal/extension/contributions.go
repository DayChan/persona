package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ContributionKey names one contribution: "<point>/<id>" (Record.DisabledContributions).
func ContributionKey(point, id string) string { return point + "/" + id }

// SwitchedElsewhere says whether a contribution's switch is not the extension's own (docs/extensions/extensions.md "Contribution switches"):
// a connector's is the connector's switch (Settings › Connectors, the inbox and the archive keep using it), and a channel bound
// to a connector goes with that connector.
func (m *Manifest) SwitchedElsewhere(point, id string) bool {
	if point == "connectors" {
		return true
	}
	if point == "channel" {
		for _, c := range m.Channels() {
			if c.ID == id && c.Connector != "" {
				return true
			}
		}
	}
	return false
}

// Without is the manifest with the contributions in disabled ("<point>/<id>") taken out: what takes effect. Keys it
// does not have and the ones switched elsewhere (SwitchedElsewhere) are ignored. The manifest itself when nothing is
// taken out.
func (m *Manifest) Without(disabled []string) *Manifest {
	if m == nil || len(disabled) == 0 {
		return m
	}
	var out *Manifest
	for _, name := range sortedKeys(m.Contributes) {
		item, known := ItemOf(name)
		if !known {
			continue
		}
		var items []json.RawMessage
		if json.Unmarshal(m.Contributes[name], &items) != nil {
			continue
		}
		kept := items[:0:0]
		for _, raw := range items {
			id, err := item(raw)
			if err == nil && slices.Contains(disabled, ContributionKey(name, id)) && !m.SwitchedElsewhere(name, id) && !m.switchedByConfig(name, id) {
				continue
			}
			kept = append(kept, raw)
		}
		if len(kept) == len(items) {
			continue
		}
		if out == nil {
			c := *m
			c.Contributes = maps.Clone(m.Contributes)
			out = &c
		}
		if len(kept) == 0 {
			delete(out.Contributes, name)
		} else {
			out.Contributes[name], _ = json.Marshal(kept)
		}
	}
	if out == nil {
		return m
	}
	return out
}

// ContributionTitle is what a contribution is called for people: its name or title, else its id.
func ContributionTitle(m *Manifest, point, id string) string {
	var items []json.RawMessage
	_ = json.Unmarshal(m.Contributes[point], &items)
	item, ok := ItemOf(point)
	if !ok {
		return id
	}
	for _, raw := range items {
		if got, err := item(raw); err != nil || got != id {
			continue
		}
		var named struct {
			Name  string `json:"name"`
			Title string `json:"title"`
		}
		_ = json.Unmarshal(raw, &named)
		if t := strings.TrimSpace(named.Title); t != "" {
			return t
		}
		if n := strings.TrimSpace(named.Name); n != "" {
			return n
		}
	}
	return id
}

// offByDefaultProblems are what is wrong with OffByDefault: each must be one of the manifest's contributions, once,
// and one whose switch is the extension's.
func (m *Manifest) offByDefaultProblems() []string {
	var out []string
	seen := map[string]bool{}
	for _, key := range m.OffByDefault {
		point, id, ok := strings.Cut(key, "/")
		switch {
		case seen[key]:
			out = append(out, fmt.Sprintf("off_by_default: %q twice", key))
		case !ok || !slices.Contains(m.Items(point), id):
			out = append(out, fmt.Sprintf("off_by_default: %q is not one of the contributions (<point>/<id>)", key))
		case m.SwitchedElsewhere(point, id) || m.switchedByConfig(point, id):
			out = append(out, fmt.Sprintf("off_by_default: %q has a switch of its own (a connector's, a channel's or a setting)", key))
		}
		seen[key] = true
	}
	return out
}

// alwaysOff are the points whose contributions start switched off whatever the manifest says: the user turns each
// on (auth_relay acts with the user's account on links Agents give, docs/operations/auth-relay.md).
var alwaysOff = []string{"auth_relay"}

// StartsOff is what a new install (prev nil) or an upgrade from prev switches off before it runs: the manifest's
// OffByDefault and the contributions to alwaysOff, at an upgrade only the ones prev did not contribute (the user chose
// about the others already), added to disabled (sorted, once each).
func (m *Manifest) StartsOff(prev *Manifest, disabled []string) []string {
	out := slices.Clone(disabled)
	keys := slices.Clone(m.OffByDefault)
	for _, point := range alwaysOff {
		for _, id := range m.Items(point) {
			keys = append(keys, ContributionKey(point, id))
		}
	}
	for _, key := range keys {
		point, id, _ := strings.Cut(key, "/")
		if prev != nil && slices.Contains(prev.Items(point), id) {
			continue
		}
		if !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out
}

// ErrSwitchedElsewhere: the contribution's switch is not the extension's (a connector's).
var ErrSwitchedElsewhere = errors.New("这一项跟着连接器的开关")

// ErrSwitchedByConfig: the contribution goes with a field of the extension's configuration (an agent filter's when).
var ErrSwitchedByConfig = errors.New("这一项跟着扩展设置里的开关")

// SwitchedByConfig says whether a contribution's switch is a field of the extension's configuration, and which.
func (m *Manifest) SwitchedByConfig(point, id string) (string, bool) {
	if !m.switchedByConfig(point, id) {
		return "", false
	}
	for _, f := range m.AgentFilters() {
		if f.ID == id {
			return f.When, true
		}
	}
	return "", false
}

// SetContribution switches one contribution of extension id on or off; the extension restarts with it taken out
// (Change kind contributions). hub.json's are changed there only.
func (r *Registry) SetContribution(ctx context.Context, id, point, cid string, on bool, actor string) error {
	unlock, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer unlock()
	p, err := r.editable(id)
	if err != nil {
		return err
	}
	id = p.Record.ID
	if !slices.Contains(p.Full.Items(point), cid) {
		return fmt.Errorf("扩展 %s 没有贡献 %s/%s", id, point, cid)
	}
	if p.Full.SwitchedElsewhere(point, cid) {
		return ErrSwitchedElsewhere
	}
	if p.Full.switchedByConfig(point, cid) {
		return ErrSwitchedByConfig
	}
	key := ContributionKey(point, cid)
	r.mu.Lock()
	row := p.Record
	off := slices.DeleteFunc(slices.Clone(row.DisabledContributions), func(k string) bool { return k == key })
	if !on {
		off = append(off, key)
		slices.Sort(off)
	}
	same := slices.Equal(off, row.DisabledContributions)
	row.DisabledContributions = off
	r.mu.Unlock()
	if same {
		return nil
	}
	if err := r.put(ctx, row); err != nil {
		return err
	}
	r.mu.Lock()
	p.Record.DisabledContributions = off
	p.Manifest = p.Full.Without(off)
	r.mu.Unlock()
	unlock()
	r.audit(ctx, id, "contribution", actor, map[string]any{"contribution": key, "enabled": on})
	r.changed(Change{ID: id, Kind: "contributions"})
	return nil
}
