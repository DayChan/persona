package extension

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/i18n"
)

// StorageKey never changes during an installed package's identity transition.
func (r Record) StorageKey() string {
	if r.StorageID != "" {
		return r.StorageID
	}
	return r.ID
}

// IdentityDeleteStore prevents another process's committed rename being deleted
// by a stale installation snapshot. Unknown commits follow the plan recovery gate.
type IdentityDeleteStore interface {
	DeleteExtensionRecord(context.Context, Record) error
}

var ErrIdentity = i18n.Err("extension-identity.invalid")

// canonicalID is called with the registry lock held. Aliases belong only to
// committed installations; catalog declarations never create aliases.
func (r *Registry) canonicalID(id string) string {
	if r.extensions[id] != nil {
		return id
	}
	for canonical, p := range r.extensions {
		if slices.Contains(p.Record.Aliases, id) {
			return canonical
		}
	}
	return id
}

func (r *Registry) CanonicalID(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.canonicalID(id)
}

func (r *Registry) GetExact(id string) *Extension {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.extensions[id]
}

func (r *Registry) SnapshotExact(id string) *Extension {
	r.mu.Lock()
	defer r.mu.Unlock()
	return snapshotExtension(r.extensions[id])
}

func (r *Registry) storageKey(id string) string {
	if p := r.Snapshot(id); p != nil {
		return p.Record.StorageKey()
	}
	return id
}

// UpgradeID is the canonical predecessor the owner explicitly consented to upgrade.
func (pv *Preview) UpgradeID() string {
	if pv.RenameFrom != "" {
		return pv.RenameFrom
	}
	return pv.Manifest.ID
}

// ValidateReplacements checks the whole declaration graph, including entries
// whose predecessor is no longer in the catalog. No first-entry-wins identity claims.
func ValidateReplacements(entries []MarketEntry) error {
	current := map[string]bool{}
	for _, e := range entries {
		if e.Parsed != nil {
			current[e.Parsed.ID] = true
		}
	}
	successor := map[string]string{}
	predecessor := map[string]string{}
	for _, e := range entries {
		if e.Replaces == "" {
			continue
		}
		// A predecessor is retired from this catalog. This also excludes
		// acyclic replacement chains whose middle name remains a current entry.
		if current[e.Replaces] {
			return ErrIdentity
		}
		if e.Parsed == nil || !ValidID(e.Replaces) || e.Replaces == e.Parsed.ID {
			return ErrIdentity
		}
		if _, exists := successor[e.Replaces]; exists {
			return ErrIdentity
		}
		successor[e.Replaces] = e.Parsed.ID
		predecessor[e.Parsed.ID] = e.Replaces
	}
	for id := range predecessor {
		seen := map[string]bool{}
		for next := id; next != ""; next = predecessor[next] {
			if seen[next] {
				return ErrIdentity
			}
			seen[next] = true
		}
	}
	return nil
}
