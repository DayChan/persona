package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

var digestPattern = lazyre.New(`^[0-9a-f]{64}$`)

func (ph *pluginHost) loadIdentities() error {
	if ph.identityLoaded {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(ph.dir, ".identities.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ids := map[string]proto.PluginNodeIdentity{}
	if err == nil {
		if err = json.Unmarshal(b, &ids); err != nil {
			return err
		}
	}
	ph.identities, ph.identityLoaded = ids, true
	return nil
}

// applyIdentities runs only over the node's authenticated manager RPC channel.
// Commit the slot binding before starting a successor; stale revisions cannot reopen a retired ID.
func (ph *pluginHost) applyIdentities(slots []proto.PluginNodeIdentity) error {
	ph.lifecycle.Lock()
	defer ph.lifecycle.Unlock()
	if err := ph.loadIdentities(); err != nil {
		return err
	}
	if len(slots) == 0 {
		return nil
	}
	next := map[string]proto.PluginNodeIdentity{}
	for k, v := range ph.identities {
		next[k] = v
	}
	seen := map[string]bool{}
	reset := map[string]bool{}
	for _, s := range slots {
		if s.Retired || !extension.ValidID(s.ID) || !extension.ValidID(s.StorageID) || s.Revision <= 0 || seen[s.StorageID] {
			return errors.New("invalid extension identity slot")
		}
		seen[s.StorageID] = true
		if old, ok := next[s.StorageID]; ok && (s.Revision < old.Revision || s.Revision == old.Revision && s.ID != old.ID) {
			return errors.New("stale extension identity revision")
		}
		if _, err := os.Lstat(filepath.Join(ph.dir, s.StorageID)); err == nil && !ph.owns(s.StorageID) {
			return errors.New("extension slot is not owned by this node")
		}
		for key, old := range next {
			if key != s.StorageID && old.ID != s.ID {
				continue
			}
			if s.Revision < old.Revision || s.InstalledAt < old.InstalledAt {
				return errors.New("stale extension installation")
			}
			if key != s.StorageID && s.InstalledAt <= old.InstalledAt {
				return errors.New("extension identity belongs to another slot")
			}
			if old.Retired && key == s.StorageID && s.InstalledAt <= old.InstalledAt {
				return errors.New("retired extension installation")
			}
			if s.InstalledAt > old.InstalledAt && s.Revision > old.Revision {
				reset[key] = true // Explicit uninstall/reinstall, never an identity upgrade.
				if key != s.StorageID {
					old.Retired = true
					next[key] = old
				}
			}
		}
		next[s.StorageID] = s
	}
	public := map[string]string{}
	for slot, s := range next {
		if s.Retired {
			continue
		}
		if other, ok := public[s.ID]; ok && other != slot {
			return errors.New("extension identity belongs to another slot")
		}
		public[s.ID] = slot
	}
	ph.mu.Lock()
	var resetRuns []string
	for id, r := range ph.running {
		if reset[r.spec.StorageKey()] {
			resetRuns = append(resetRuns, id)
		}
	}
	ph.mu.Unlock()
	for _, id := range resetRuns {
		ph.stop(id)
	}
	for slot := range reset {
		if ph.owns(slot) {
			if err := os.RemoveAll(filepath.Join(ph.dir, slot)); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(ph.dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(ph.dir, ".identities-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(ph.dir, ".identities.json")); err != nil {
		return err
	}
	ph.identities = next
	ph.mu.Lock()
	var retire []string
	for id, r := range ph.running {
		if s, ok := next[r.spec.StorageKey()]; ok && (s.ID != id || r.spec.Revision != s.Revision) {
			retire = append(retire, id)
		}
	}
	ph.mu.Unlock()
	for _, id := range retire {
		ph.stop(id)
	}
	return nil
}

// checkIdentity is called with lifecycle held. Empty fields retain the same-ID wire protocol.
func (ph *pluginHost) checkIdentity(id, slot string, revision int64) error {
	if err := ph.loadIdentities(); err != nil {
		return err
	}
	if !extension.ValidID(slot) {
		return errors.New("invalid extension storage slot")
	}
	for key, s := range ph.identities {
		if !s.Retired && s.ID == id && key != slot {
			return errors.New("extension identity belongs to another slot")
		}
	}
	if s, ok := ph.identities[slot]; ok {
		if s.Retired || s.ID != id || s.Revision != revision {
			return errors.New("stale extension identity revision")
		}
	} else if slot != id || revision != 0 {
		return errors.New("extension identity has not been committed on this node")
	}
	return nil
}

func checkProgram(p proto.PluginNodeProgram) error {
	if !extension.ValidID(p.ID) {
		return fmt.Errorf("plugin id %q", p.ID)
	}
	if _, err := extension.ParseVersion(p.Version); err != nil {
		return fmt.Errorf("plugin %s version %q", p.ID, p.Version)
	}
	if p.Digest != "" && !digestPattern.MatchString(p.Digest) {
		return fmt.Errorf("plugin %s digest", p.ID)
	}
	return nil
}
