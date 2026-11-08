package extension

import (
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func TestIdentityPreviewPresentationDisclosesRenameToExistingClients(t *testing.T) {
	pv := &Preview{RenameFrom: "old-name", Manifest: &Manifest{ID: "new-name", Name: "Author name", Version: "2.0.0"},
		Warnings: []string{"Author diagnostic"}, Digest: "unchanged-consent"}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		out := pv.In(lang)
		want := i18n.T(lang, "extension-identity.confirmation", i18n.Args{"from": "old-name", "to": "new-name"})
		if len(out.Warnings) != 2 || out.Warnings[0] != want || out.Warnings[1] != "Author diagnostic" {
			t.Fatalf("localized identity warning %s: %+v", lang, out.Warnings)
		}
		if len(out.In(lang).Warnings) != 2 || out.Digest != pv.Digest || out.Manifest != pv.Manifest {
			t.Fatal("presentation duplicated warnings or changed consent")
		}
	}
	if len(pv.Warnings) != 1 || pv.Warnings[0] != "Author diagnostic" {
		t.Fatal("presentation mutated the staged preview")
	}
}
