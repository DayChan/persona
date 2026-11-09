package node

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/proto"
)

// sync stops what should not run here and removes its packages: of a plugin still to run, the version to run and the
// one before it (what an upgrade falls back to); of one not installed any more, its data too. It removes only what this
// node made (owns): a directory without its mark is left alone, whatever its name (#1933).
func (ph *pluginHost) identityRevision() int64 {
	var revision int64
	for _, slot := range ph.identities {
		revision = max(revision, slot.Revision)
	}
	return revision
}

func (ph *pluginHost) validateSync(p proto.PluginNodeSync) error {
	if err := ph.loadIdentities(); err != nil {
		return err
	}
	if len(ph.identities) > 0 && (!p.Identity || p.Revision < ph.identityRevision()) {
		return errors.New("stale or legacy extension identity sync")
	}
	for slot := range ph.identities {
		if slices.Contains(p.Installed, slot) && !slices.ContainsFunc(p.InstalledSlots, func(s proto.PluginNodeIdentity) bool { return s.StorageID == slot }) {
			return errors.New("committed extension slot missing from identity-aware sync")
		}
	}
	for _, s := range p.InstalledSlots {
		if err := ph.checkIdentity(s.ID, s.StorageID, s.Revision); err != nil {
			return err
		}
	}
	for _, prog := range p.Programs {
		if err := checkProgram(prog); err != nil {
			return err
		}
		if err := ph.checkIdentity(prog.ID, prog.StorageKey(), prog.Revision); err != nil {
			return err
		}
	}
	return nil
}

func (ph *pluginHost) sync(p proto.PluginNodeSync) proto.PluginNodeStates {
	states, _ := ph.syncRequest(p)
	return states
}

func (ph *pluginHost) syncRequest(p proto.PluginNodeSync) (proto.PluginNodeStates, error) {
	ph.lifecycle.Lock()
	defer ph.lifecycle.Unlock()
	if err := ph.validateSync(p); err != nil {
		return ph.status(), err
	}
	want := map[string]string{}
	for _, prog := range p.Programs {
		if checkProgram(prog) == nil {
			want[prog.StorageKey()] = prog.Version
		}
	}
	ph.mu.Lock()
	var stale []string
	for id, r := range ph.running {
		if want[r.spec.StorageKey()] != r.spec.Version {
			stale = append(stale, id)
		}
	}
	ph.mu.Unlock()
	for _, id := range stale {
		ph.stop(id)
	}
	entries, _ := os.ReadDir(ph.dir)
	for _, e := range entries {
		id := e.Name()
		if !e.IsDir() || !extension.ValidID(id) || !ph.owns(id) {
			continue
		}
		version, wanted := want[id]
		if !wanted {
			if slices.ContainsFunc(p.InstalledSlots, func(s proto.PluginNodeIdentity) bool { return s.StorageID == id }) {
				// Disabled migrated installations retain rollback packages as well as data.
				continue
			}
			if slices.Contains(p.Installed, id) {
				ph.prune(id, "") // Legacy same-ID behavior retains data, not packages.
			} else {
				_ = os.RemoveAll(filepath.Join(ph.dir, id))
			}
			continue
		}
		ph.prune(id, version)
	}
	return ph.status(), nil
}

// prune removes id's packages but version and the newest one before it (all of them when version is "").
func (ph *pluginHost) prune(id, version string) {
	type older struct {
		v    extension.Version
		name string
	}
	keep, _ := extension.ParseVersion(version)
	var before []older
	entries, _ := os.ReadDir(filepath.Join(ph.dir, id))
	for _, e := range entries {
		v, err := extension.ParseVersion(e.Name())
		if !e.IsDir() || err != nil || e.Name() == version {
			continue
		}
		if version != "" && v.Compare(keep) < 0 {
			before = append(before, older{v, e.Name()})
			continue
		}
		_ = os.RemoveAll(filepath.Join(ph.dir, id, e.Name()))
	}
	slices.SortFunc(before, func(a, b older) int { return a.v.Compare(b.v) })
	for i, o := range before {
		if i < len(before)-1 { // the newest of the older ones stays
			_ = os.RemoveAll(filepath.Join(ph.dir, id, o.name))
		}
	}
}
