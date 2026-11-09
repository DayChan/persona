package extension

import (
	"errors"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
)

// StoragePermission declares logical keys owned by this installation. It grants
// no path, SQL, credential, hub or network access. Patterns are literal prefixes
// followed by one terminal '*', not regular expressions or filesystem globs.
type StoragePermission struct {
	Keys     []string `json:"keys,omitempty"`
	Patterns []string `json:"patterns,omitempty"`
	Queues   bool     `json:"queues,omitempty"`
	Blobs    bool     `json:"blobs,omitempty"`
}

const (
	StorageKeyMost      = 128
	StorageKeysMost     = 128
	StoragePatternsMost = 32
)

// ValidStorageKey rejects ambiguous names before matching the declared scope.
// Slashes are logical separators only and are never resolved as filesystem paths.
func ValidStorageKey(key string) bool {
	if key == "" || len(key) > StorageKeyMost || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
		return false
	}
	for _, part := range strings.Split(key, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, c := range key {
		if c < 'a' || c > 'z' {
			if c < 'A' || c > 'Z' {
				if c < '0' || c > '9' {
					if c != '-' && c != '_' && c != '.' && c != '/' {
						return false
					}
				}
			}
		}
	}
	return true
}

func storagePrefix(pattern string) (string, bool) {
	prefix, ok := strings.CutSuffix(pattern, "*")
	if !ok || prefix == "" || len(pattern) > StorageKeyMost || strings.Contains(prefix, "*") {
		return "", false
	}
	if !ValidStorageKey(strings.TrimSuffix(prefix, "/")) {
		return "", false
	}
	return prefix, true
}

func (p *StoragePermission) validate() error {
	if p == nil {
		return nil
	}
	if len(p.Keys)+len(p.Patterns) == 0 || len(p.Keys) > StorageKeysMost || len(p.Patterns) > StoragePatternsMost {
		return errors.New("permissions.storage: declare bounded keys or literal prefix patterns")
	}
	for _, key := range p.Keys {
		if !ValidStorageKey(key) {
			return errors.New("permissions.storage: invalid logical key")
		}
	}
	for _, pattern := range p.Patterns {
		if _, ok := storagePrefix(pattern); !ok {
			return errors.New("permissions.storage: patterns must be a nonempty logical prefix and terminal *")
		}
	}
	return nil
}

// Allows is default-deny and case-sensitive. Even a matched name remains subject
// to host identity, lifecycle, method-specific permissions and aggregate quotas.
func (p *StoragePermission) Allows(key string) bool {
	if p == nil || !ValidStorageKey(key) {
		return false
	}
	if slices.Contains(p.Keys, key) {
		return true
	}
	for _, pattern := range p.Patterns {
		if prefix, ok := storagePrefix(pattern); ok && strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func (p *StoragePermission) normalized() *StoragePermission {
	if p == nil {
		return nil
	}
	return &StoragePermission{Keys: slices.Compact(slices.Sorted(slices.Values(p.Keys))), Patterns: slices.Compact(slices.Sorted(slices.Values(p.Patterns))), Queues: p.Queues, Blobs: p.Blobs}
}

func storageLine(lang i18n.Lang, grant *StoragePermission) PermissionLine {
	keys := append(slices.Clone(grant.Keys), grant.Patterns...)
	details := []string{i18n.T(lang, "extension-storage.permission_detail", i18n.Args{"keys": strings.Join(keys, ", ")})}
	if grant.Queues {
		details = append(details, i18n.T(lang, "extension-storage.permission_queues"))
	}
	if grant.Blobs {
		details = append(details, i18n.T(lang, "extension-storage.permission_blobs"))
	}
	return PermissionLine{Key: "storage", Title: i18n.T(lang, "extension-storage.permission_title"), Detail: strings.Join(details, " ")}
}

func (p *StoragePermission) covers(q *StoragePermission) bool {
	if q == nil {
		return true
	}
	if p == nil || q.Queues && !p.Queues || q.Blobs && !p.Blobs {
		return false
	}
	for _, key := range q.Keys {
		if !p.Allows(key) {
			return false
		}
	}
	for _, pattern := range q.Patterns {
		prefix, valid := storagePrefix(pattern)
		covered := false
		if !valid {
			return false
		}
		for _, old := range p.Patterns {
			if allowed, ok := storagePrefix(old); ok && strings.HasPrefix(prefix, allowed) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}
