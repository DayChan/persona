package extension

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestStoragePermissionDefaultDenyAndLogicalPatterns(t *testing.T) {
	grant := &StoragePermission{Keys: []string{"preferences"}, Patterns: []string{"state/*"}}
	for _, key := range []string{"preferences", "state/topics", "state/nested/value"} {
		if !grant.Allows(key) {
			t.Fatalf("declared key refused: %q", key)
		}
	}
	for _, key := range []string{"", "Preferences", "other/preferences", "state", "state/", "state/../private", "state//x", "/state/x", "state\\x", "state/%2e%2e/x", "state/\x00x", strings.Repeat("x", StorageKeyMost+1)} {
		if grant.Allows(key) {
			t.Fatalf("undeclared or ambiguous key admitted: %q", key)
		}
	}
	var absent *StoragePermission
	if absent.Allows("preferences") {
		t.Fatal("missing storage grant admitted data access")
	}
}

func TestStoragePermissionRejectsUnboundedOrAmbiguousDeclarations(t *testing.T) {
	for _, grant := range []*StoragePermission{
		{}, {Keys: []string{"../x"}}, {Keys: []string{"x/"}}, {Patterns: []string{"*"}},
		{Patterns: []string{"state/?"}}, {Patterns: []string{"state/**"}}, {Patterns: []string{"state/../*"}},
		{Keys: make([]string, StorageKeysMost+1)}, {Patterns: make([]string, StoragePatternsMost+1)},
	} {
		if err := (Permissions{Storage: grant}).validate(); err == nil {
			t.Fatalf("invalid declaration accepted: %+v", grant)
		}
	}
	if err := (Permissions{Storage: &StoragePermission{Keys: []string{"preferences"}, Patterns: []string{"state/*"}}}).validate(); err != nil {
		t.Fatal(err)
	}
}

func TestStorageConsentCannotBeInheritedFromHubOrNetwork(t *testing.T) {
	asked := Permissions{Storage: &StoragePermission{Keys: []string{"preferences"}}}
	for _, old := range []Permissions{{}, {HubAPI: true}, {Network: []string{"example.com"}}} {
		if old.Covers(asked) {
			t.Fatal("unrelated permission covered new storage authority")
		}
	}
	old := Permissions{Storage: &StoragePermission{Patterns: []string{"state/*"}}}
	if !old.Covers(Permissions{Storage: &StoragePermission{Keys: []string{"state/a"}, Patterns: []string{"state/nested/*"}}}) {
		t.Fatal("narrower declaration unnecessarily requires broader authority")
	}
	for _, next := range []*StoragePermission{{Patterns: []string{"sta*"}}, {Keys: []string{"other"}}, {Patterns: []string{"state/*"}, Queues: true}, {Patterns: []string{"state/*"}, Blobs: true}} {
		if old.Covers(Permissions{Storage: next}) {
			t.Fatalf("expanded storage permission bypassed consent: %+v", next)
		}
	}
}

func TestStorageDigestNormalizesDeclarationsWithoutAliasing(t *testing.T) {
	first := &Manifest{Permissions: Permissions{Storage: &StoragePermission{Keys: []string{"b", "a", "a"}, Patterns: []string{"state/z*", "state/a*"}}}}
	second := &Manifest{Permissions: Permissions{Storage: &StoragePermission{Keys: []string{"a", "b"}, Patterns: []string{"state/a*", "state/z*"}}}}
	if Digest(first) != Digest(second) {
		t.Fatal("equivalent storage declarations change the consent digest")
	}
	copy := first.Permissions.normalized()
	copy.Storage.Keys[0] = "another"
	if first.Permissions.Storage.Keys[0] != "b" {
		t.Fatal("normalization aliases a mutable granted key list")
	}
	second.Permissions.Storage.Queues = true
	if Digest(first) == Digest(second) {
		t.Fatal("queue authority does not change consent")
	}
}

func TestStorageConsentDisclosesDeclaredKeysAndSeparateFlags(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		grant := &StoragePermission{Keys: []string{"preferences"}, Patterns: []string{"state/*"}}
		plain := storageLine(lang, grant)
		if !strings.Contains(plain.Detail, "preferences") || !strings.Contains(plain.Detail, "state/*") || strings.Contains(plain.Detail, "extension-storage.") {
			t.Fatal("storage consent lost declarations or translation", lang, plain)
		}
		grant.Queues, grant.Blobs = true, true
		full := storageLine(lang, grant)
		for _, key := range []string{"permission_queues", "permission_blobs"} {
			text := i18n.T(lang, "extension-storage."+key)
			if !strings.Contains(full.Detail, text) || strings.Contains(plain.Detail, text) || strings.Contains(text, "extension-storage.") {
				t.Fatal("queue/blob authority not distinctly disclosed", lang, key)
			}
		}
	}
}
