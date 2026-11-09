package extension

import (
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestSecretAddedOnUpgradeNeedsConsent(t *testing.T) {
	key := SecretSpec{Name: "api_key", Title: "API key", Required: true}
	optional := SecretSpec{Name: "backup_key", Title: "Backup key"}
	for _, tt := range []struct {
		name  string
		old   []SecretSpec
		next  []SecretSpec
		added string
	}{
		{name: "first required secret", next: []SecretSpec{key}, added: "API key"},
		{name: "first optional secret", next: []SecretSpec{optional}, added: "Backup key"},
		{name: "another secret", old: []SecretSpec{key}, next: []SecretSpec{key, optional}, added: "Backup key"},
		{name: "renamed secret", old: []SecretSpec{key}, next: []SecretSpec{optional}, added: "Backup key"},
		{name: "unchanged", old: []SecretSpec{key}, next: []SecretSpec{key}},
		{name: "removed", old: []SecretSpec{key}},
		{name: "reordered", old: []SecretSpec{key, optional}, next: []SecretSpec{optional, key}},
		{name: "metadata changed", old: []SecretSpec{key}, next: []SecretSpec{{Name: key.Name, Title: "Service key"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			old, next := &Manifest{Secrets: tt.old}, &Manifest{Secrets: tt.next}
			cur := &Extension{Record: Record{Granted: old.Permissions, GrantedDigest: Digest(old)}, Manifest: old, Full: old}
			if Digest(next) != cur.Record.GrantedDigest {
				t.Fatal("hub-only secrets changed the existing consent digest")
			}
			if got := MorePermissions(cur, next); got != (tt.added != "") {
				t.Errorf("MorePermissions = %v, added secret = %q", got, tt.added)
			}
			for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
				lines := AddedIn(l, old, next)
				if tt.added == "" {
					if len(lines) != 0 {
						t.Errorf("%s: unchanged secret names ask for more: %+v", l, lines)
					}
					continue
				}
				if len(lines) != 1 {
					t.Fatalf("%s: added permissions: %+v", l, lines)
				}
				want := PermissionLine{Key: "secrets", Title: i18n.T(l, "extension-permissions.secrets_more_title"), Detail: tt.added}
				if lines[0] != want {
					t.Errorf("%s: added permission = %+v, want %+v", l, lines[0], want)
				}
			}
		})
	}
}
