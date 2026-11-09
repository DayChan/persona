package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func extensionProviderService(t *testing.T, ext assistantwire.ModelExtension) (*Service, *fakeHub) {
	t.Helper()
	s, f, _ := modelService(t)
	f.modelExts = []assistantwire.ModelExtension{ext}
	key, err := s.sealKey("synthetic-provider-key")
	if err != nil {
		t.Fatal(err)
	}
	// Categories automatically use the first provider; only the extension will select the second.
	putLaunchers(t, s,
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "processing", Kind: "api", BaseURL: "https://api.example.com", Model: "processing-model"}, KeySealed: key},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "extension-only", Kind: "api", BaseURL: "https://api.example.com", Model: "extension-model"}, KeySealed: key})
	return s, f
}

func TestExtensionProviderDeletionProtectsItsOwnPurpose(t *testing.T) {
	for _, ext := range []assistantwire.ModelExtension{
		{ID: "curator", Name: "Curator"},
		{ID: "renamed-curator", StorageID: "curator", Name: "Renamed Curator"},
	} {
		t.Run(ext.ID, func(t *testing.T) {
			s, _ := extensionProviderService(t, ext)
			ctx := context.Background()
			id := assistantwire.ExtensionPurposePrefix + ext.StorageKey()
			if w := modelRequest(s, http.MethodPut, id, `{"revision":0,"provider":"launcher.extension-only","model":"selected-model"}`); w.Code != http.StatusOK {
				t.Fatalf("select extension provider: %d %s", w.Code, w.Body.String())
			}
			uses, err := s.providerModelUses(ctx, "launcher.extension-only")
			if err != nil || !slices.Equal(uses, []string{ext.Name}) {
				t.Fatalf("extension-only provider uses: %v %v", uses, err)
			}
			beforeProviders, err := s.readStoredLaunchers(ctx)
			if err != nil {
				t.Fatal(err)
			}
			beforePurpose := purposeOf(t, s, id)
			for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
				res, err := serveHTTP(ctx, s, assistantwire.HTTPRequest{
					Method: http.MethodDelete, Path: "/api/v1/launchers/extension-only",
					Header: map[string][]string{"Accept-Language": {string(lang)}},
				}, s.lang)
				if err != nil {
					t.Fatal(err)
				}
				var body struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(res.Body, &body); err != nil {
					t.Fatal(err)
				}
				want := i18n.T(lang, "assistant-models_launchers.err_in_use", i18n.Args{"uses": ext.Name})
				if res.Status != http.StatusConflict || body.Error != want {
					t.Fatalf("%s delete: %d %s; want 409 %q", lang, res.Status, res.Body, want)
				}
			}
			afterProviders, err := s.readStoredLaunchers(ctx)
			if err != nil || !reflect.DeepEqual(afterProviders, beforeProviders) {
				t.Fatalf("rejected deletion changed providers: %+v %v", afterProviders, err)
			}
			afterPurpose := purposeOf(t, s, id)
			if afterPurpose.Override != beforePurpose.Override || afterPurpose.Effective != beforePurpose.Effective || afterPurpose.Revision != beforePurpose.Revision {
				t.Fatalf("rejected deletion changed purpose: before %+v, after %+v", beforePurpose, afterPurpose)
			}
		})
	}
}

func TestExtensionProviderDeletionAfterSwitchOrReset(t *testing.T) {
	for _, tc := range []struct {
		name, body string
	}{
		{"switch", `{"revision":1,"provider":"launcher.processing"}`},
		{"reset", `{"revision":1,"provider":null,"model":null,"effort":null}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := extensionProviderService(t, assistantwire.ModelExtension{ID: "curator", Name: "Curator"})
			const id = "extension.curator"
			if w := modelRequest(s, http.MethodPut, id, `{"revision":0,"provider":"launcher.extension-only","model":"selected-model"}`); w.Code != http.StatusOK {
				t.Fatalf("select: %d %s", w.Code, w.Body.String())
			}
			if w := modelRequest(s, http.MethodPut, id, tc.body); w.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", tc.name, w.Code, w.Body.String())
			}
			if w := request(s, http.MethodDelete, "/api/v1/launchers/extension-only", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"deleted":true`) {
				t.Fatalf("delete released provider: %d %s", w.Code, w.Body.String())
			}
			providers, err := s.readStoredLaunchers(context.Background())
			if err != nil || len(providers) != 1 || providers[0].Name != "processing" {
				t.Fatalf("remaining providers: %+v %v", providers, err)
			}
			p := purposeOf(t, s, id)
			if p.Effective.Provider != "launcher.processing" || p.Revision != 2 {
				t.Fatalf("extension no longer resolves after deletion: %+v", p)
			}
			if tc.name == "reset" && (p.Override != (ModelBinding{}) || p.InheritedFrom == "") {
				t.Fatalf("reset did not restore inheritance: %+v", p)
			}
		})
	}
}

func TestExtensionProviderUsesFollowActivePurposes(t *testing.T) {
	t.Run("inherited", func(t *testing.T) {
		s, _ := extensionProviderService(t, assistantwire.ModelExtension{ID: "curator", Name: "Curator"})
		uses, err := s.providerModelUses(context.Background(), "launcher.processing")
		if err != nil || !slices.Contains(uses, "Curator") {
			t.Fatalf("inherited extension use missing: %v %v", uses, err)
		}
		if w := request(s, http.MethodDelete, "/api/v1/launchers/processing", ""); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "Curator") {
			t.Fatalf("delete inherited provider: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("dormant", func(t *testing.T) {
		s, f := extensionProviderService(t, assistantwire.ModelExtension{ID: "curator", Name: "Curator"})
		ctx := context.Background()
		const id = "extension.curator"
		if w := modelRequest(s, http.MethodPut, id, `{"revision":0,"provider":"launcher.extension-only"}`); w.Code != http.StatusOK {
			t.Fatalf("select: %d %s", w.Code, w.Body.String())
		}
		before, err := s.modelRecord(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		// Disabled, removed, or no longer granted models: the manager omits the purpose.
		f.modelExts = nil
		if w := request(s, http.MethodDelete, "/api/v1/launchers/extension-only", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"deleted":true`) {
			t.Fatalf("dormant purpose blocked deletion: %d %s", w.Code, w.Body.String())
		}
		after, err := s.modelRecord(ctx, id)
		if err != nil || after != before {
			t.Fatalf("deletion changed dormant record: before %+v, after %+v, error %v", before, after, err)
		}
	})
}
