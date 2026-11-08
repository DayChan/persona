package extension

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// identityStore models stable slots and both definite and indeterminate commits.
type identityStore struct {
	*memStore
	fail, unknown bool
}

func (s *identityStore) PutExtensionPlan(ctx context.Context, rows []Record) error {
	if s.fail {
		return errors.New("synthetic database failure")
	}
	for _, row := range rows {
		if err := s.PutExtension(ctx, row); err != nil {
			return err
		}
	}
	if s.unknown {
		return &CommitUnknownError{Err: errors.New("synthetic lost reply")}
	}
	return nil
}

func (s *identityStore) DeleteExtensionRecord(ctx context.Context, row Record) error {
	if s.fail {
		return errors.New("synthetic delete failure")
	}
	if err := s.DeleteExtension(ctx, row.StorageKey()); err != nil {
		return err
	}
	if s.unknown {
		return &CommitUnknownError{Err: errors.New("synthetic lost delete reply")}
	}
	return nil
}

func identityFixture(t *testing.T, permissionFree bool, signed ...bool) (*Registry, *identityStore, func(string, string, string, ...string) (*Preview, error)) {
	t.Helper()
	store := &identityStore{memStore: newMemStore()}
	reg := NewRegistry(t.TempDir(), store, rot13{}, 202611100000)
	change := func(id, version string) func(map[string]any) {
		return func(m map[string]any) {
			m["id"], m["version"] = id, version
			m["contributes"].(map[string]any)["ui"] = []any{map[string]any{"id": "identity-page", "title": "Example", "placement": "plugin_page"}}
			if permissionFree {
				delete(m, "permissions")
				delete(m, "secrets")
				m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "identity-page", "title": "Example", "placement": "plugin_page"}}}
			}
		}
	}
	dir := t.TempDir()
	writePackage(t, dir, change("old-example", "1.0.0"))
	pv, err := reg.Preview(t.Context(), "dir:"+dir, "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := reg.Install(t.Context(), pv.ID, pv.Digest, "test")
	if err != nil {
		t.Fatal(err)
	}
	old.Record.Origin = "market:example-market/old-example"
	if err := store.PutExtension(t.Context(), old.Record); err != nil {
		t.Fatal(err)
	}
	preview := func(id, replaces, market string, upgrades ...string) (*Preview, error) {
		pkgdir := t.TempDir()
		writePackage(t, pkgdir, change(id, "2.0.0"))
		raw, err := os.ReadFile(ManifestPath(pkgdir))
		if err != nil {
			t.Fatal(err)
		}
		archive := tarGz(t, map[string]string{ManifestFile: string(raw), "bin/notes": "#!/bin/sh\n"})
		sum := sha256.Sum256(archive)
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) }))
		t.Cleanup(srv.Close)
		spec, err := ParseMarket(srv.URL + "/index.json")
		if err != nil {
			t.Fatal(err)
		}
		m, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		e := &MarketEntry{Manifest: raw, Parsed: m, Replaces: replaces, Packages: []MarketPackage{{URL: srv.URL + "/pkg.tar.gz", SHA256: hex.EncodeToString(sum[:])}}}
		mk := &Market{Spec: spec, Client: srv.Client()}
		if len(signed) > 0 && signed[0] {
			key, sign := testKey(t)
			previous := Official
			Official.SigningKey = key
			t.Cleanup(func() { Official = previous })
			spec, err = ParseMarket(Official.Spec)
			if err != nil {
				t.Fatal(err)
			}
			mk.Spec, mk.TrustedKey = spec, key
			payload, err := json.Marshal(MarketIndex{MarketVersion: 1, Name: "Example", Extensions: []MarketEntry{*e}})
			if err != nil {
				t.Fatal(err)
			}
			verified, err := ParseCatalog(sign(payload), key)
			if err != nil {
				t.Fatal(err)
			}
			e = &verified.Index.Extensions[0]
		}
		upgrade := "old-example"
		if len(upgrades) > 0 {
			upgrade = upgrades[0]
		}
		return reg.PreviewMarket(t.Context(), mk, market, e, upgrade)
	}
	return reg, store, preview
}

func TestIdentityRenameExplicitConfirmation(t *testing.T) {
	for _, free := range []bool{false, true} {
		t.Run(fmt.Sprint(free), func(t *testing.T) {
			r, store, preview := identityFixture(t, free)
			old := r.Snapshot("old-example")
			var changes []Change
			r.OnChange = func(c Change) { changes = append(changes, c) }
			if err := r.SetEnabled(t.Context(), "old-example", false, "test"); err != nil {
				t.Fatal(err)
			}
			old = r.Snapshot("old-example")
			slot := r.DataDir("old-example")
			if err := os.MkdirAll(slot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(slot, "data"), []byte("kept"), 0o600); err != nil {
				t.Fatal(err)
			}
			store.secrets["old-example"] = map[string]string{"synthetic": "unchanged-ciphertext"}
			pv, err := preview("new-example", "old-example", "example-market")
			if err != nil {
				t.Fatal(err)
			}
			if pv.MorePermissions || pv.Digest == "" || pv.RenameFrom != "old-example" {
				t.Fatalf("unexpected preview: %+v", pv)
			}
			if _, err = r.UpgradePlan(t.Context(), "old-example", pv.ID, "", nil, "test"); err == nil {
				t.Fatal("rename without explicit confirmation accepted")
			}
			p, err := r.UpgradePlan(t.Context(), "old-example", pv.ID, pv.Digest, nil, "test")
			if err != nil {
				t.Fatal(err)
			}
			if p.Record.StorageKey() != "old-example" || p.Record.PreviousID != "old-example" || p.Record.Enabled || p.Record.GrantedDigest != Digest(p.Full) || !reflect.DeepEqual(p.Record.Config, old.Record.Config) || !reflect.DeepEqual(p.Record.Granted, old.Record.Granted) || !p.Record.InstalledAt.Equal(old.Record.InstalledAt) {
				t.Fatalf("lost owner state: %+v", p.Record)
			}
			if len(r.List()) != 1 || r.GetExact("old-example") != nil || r.SnapshotExact("old-example") != nil || r.CanonicalID("old-example") != "new-example" || r.CanonicalID("unknown") != "unknown" || r.Get("old-example") != p {
				t.Fatal("invalid alias projection")
			}
			if store.secrets["old-example"]["synthetic"] != "unchanged-ciphertext" || r.DataDir("new-example") != slot {
				t.Fatal("storage moved")
			}
			if last := changes[len(changes)-1]; last.Kind != "renamed" || last.Old.Record.ID != "old-example" {
				t.Fatalf("wrong change: %+v", last)
			}
			loaded := NewRegistry(r.dir, store, rot13{}, 202611100000)
			if err = loaded.Load(t.Context()); err != nil {
				t.Fatal(err)
			}
			if loaded.Snapshot("old-example").Problem != "" {
				t.Fatal("restart lost package")
			}
			back, err := r.Rollback(t.Context(), "old-example", "test")
			if err != nil {
				t.Fatal(err)
			}
			if back.Record.ID != "old-example" || r.CanonicalID("new-example") != "old-example" || back.Record.StorageKey() != "old-example" || back.Record.Origin != old.Record.Origin {
				t.Fatal("rollback lost identity/provenance")
			}
			again, err := preview("new-example", "old-example", "example-market")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = r.UpgradePlan(t.Context(), "old-example", again.ID, again.Digest, nil, "test"); err != nil {
				t.Fatal(err)
			}
			if err = r.Uninstall(t.Context(), "new-example", false, "test"); err != nil {
				t.Fatal(err)
			}
			if len(store.rows) != 0 || len(store.secrets) != 0 || r.Get("new-example") != nil {
				t.Fatal("uninstall retained aliases or secrets")
			}
			if _, err = os.Stat(slot); !os.IsNotExist(err) {
				t.Fatal("uninstall retained data")
			}
		})
	}
}

func TestIdentitySignedRenameDoesNotBackfillOldTrust(t *testing.T) {
	r, s, preview := identityFixture(t, false, true)
	if err := r.SetConfig(t.Context(), "old-example", json.RawMessage(`{"base_url":"https://notes.example.com","folders":["retained"],"include_shared":true}`), "test"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSecret(t.Context(), "old-example", "api_key", "synthetic-example-secret", "test"); err != nil {
		t.Fatal(err)
	}
	cipher := s.secrets["old-example"]["api_key"]
	if err := r.SetContribution(t.Context(), "old-example", "ui", "identity-page", false, "test"); err != nil {
		t.Fatal(err)
	}
	old := r.Snapshot("old-example")
	pv, err := preview("new-example", "old-example", "example-market")
	if err != nil {
		t.Fatal(err)
	}
	if old.Record.MarketKey != "" || r.Snapshot("old-example").Record.MarketKey != "" {
		t.Fatal("preview assigned trust to old unsigned package")
	}
	p, err := r.UpgradePlan(t.Context(), "old-example", pv.ID, pv.Digest, nil, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Record.Enabled || p.Record.MarketKey == "" || p.Record.PreviousMarketKey != "" || !reflect.DeepEqual(p.Record.Config, old.Record.Config) || !reflect.DeepEqual(p.Record.DisabledContributions, old.Record.DisabledContributions) || !reflect.DeepEqual(p.Record.Granted, old.Record.Granted) || s.secrets["old-example"]["api_key"] != cipher {
		t.Fatal("signed rename changed owner state or old trust")
	}
	plain, err := r.Secrets(t.Context(), "new-example")
	if err != nil || plain["api_key"] != "synthetic-example-secret" {
		t.Fatal("new canonical identity lost stable sealed secret", err)
	}
	back, err := r.Rollback(t.Context(), "new-example", "test")
	if err != nil {
		t.Fatal(err)
	}
	if back.Record.MarketKey != "" || back.Record.Origin != old.Record.Origin || s.secrets["old-example"]["api_key"] != cipher {
		t.Fatal("rollback granted old unsigned package new trust")
	}
}

func TestIdentityFreshInstallCannotBypassRename(t *testing.T) {
	r, _, preview := identityFixture(t, true)
	if _, err := preview("new-example", "old-example", "example-market", ""); err == nil {
		t.Fatal("ordinary installation bypassed explicit rename")
	}
	if len(r.List()) != 1 || r.GetExact("old-example") == nil {
		t.Fatal("rejection changed installed slot")
	}
	if err := r.Uninstall(t.Context(), "old-example", false, "test"); err != nil {
		t.Fatal(err)
	}
	pv, err := preview("new-example", "old-example", "example-market", "")
	if err != nil {
		t.Fatal(err)
	}
	if pv.RenameFrom != "" || pv.Upgrade {
		t.Fatal("fresh package invented a predecessor")
	}
	p, err := r.Install(t.Context(), pv.ID, pv.Digest, "test")
	if err != nil {
		t.Fatal(err)
	}
	if p.Record.StorageID != "" || len(p.Record.Aliases) != 0 || r.CanonicalID("old-example") != "old-example" || r.Get("old-example") != nil {
		t.Fatal("fresh install claimed uninstalled predecessor")
	}
}

func TestIdentityRenameRejectsWrongSourceAndClaim(t *testing.T) {
	for _, test := range []struct{ id, replaces, market string }{{"new-example", "old-example", "different-market"}, {"new-example", "unrelated", "example-market"}, {"new-example", "new-example", "example-market"}} {
		r, _, preview := identityFixture(t, true)
		if _, err := preview(test.id, test.replaces, test.market); err == nil {
			t.Fatalf("invalid transition accepted: %+v", test)
		}
		if r.GetExact("old-example") == nil || len(r.List()) != 1 {
			t.Fatal("rejection changed installation")
		}
	}
}

func TestIdentityUninstallUnknownKeepsAsideUntilRestart(t *testing.T) {
	r, s, preview := identityFixture(t, true)
	pv, err := preview("new-example", "old-example", "example-market")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.UpgradePlan(t.Context(), "old-example", pv.ID, pv.Digest, nil, "test"); err != nil {
		t.Fatal(err)
	}
	s.unknown = true
	calls := 0
	r.OnChange = func(Change) { calls++ }
	if err = r.Uninstall(t.Context(), "old-example", false, "test"); err == nil {
		t.Fatal("unknown uninstall presented as confirmed")
	}
	aside, err := filepath.Glob(filepath.Join(r.dir, "staging", "uninstall-*"))
	if err != nil || len(aside) != 1 {
		t.Fatal("unknown uninstall lost prepared files")
	}
	if calls != 0 || r.GetExact("new-example") == nil {
		t.Fatal("unknown uninstall published success")
	}
	if err = r.SetEnabled(t.Context(), "new-example", false, "test"); err == nil {
		t.Fatal("unknown uninstall allowed writes")
	}
	loaded := NewRegistry(r.dir, s, rot13{}, 202611100000)
	if err = loaded.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	if loaded.Get("old-example") != nil || loaded.Get("new-example") != nil {
		t.Fatal("restart resurrected deleted installation")
	}
	if _, err = os.Stat(aside[0]); !os.IsNotExist(err) {
		t.Fatal("restart did not reconcile committed uninstall")
	}
}

func TestIdentityStalePreviewAndTargetConflict(t *testing.T) {
	for _, mode := range []string{"target-before", "target-after", "version", "package"} {
		t.Run(mode, func(t *testing.T) {
			r, _, preview := identityFixture(t, true)
			occupy := func() {
				if err := r.AddBuiltin(&Manifest{ID: "new-example", Version: "1.0.0"}, "", false); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "target-before" {
				occupy()
				if _, err := preview("new-example", "old-example", "example-market"); err == nil {
					t.Fatal("independent target accepted")
				}
				return
			}
			pv, err := preview("new-example", "old-example", "example-market")
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "target-after":
				occupy()
			case "version":
				r.GetExact("old-example").Record.Version = "1.0.1"
			case "package":
				if err = os.WriteFile(filepath.Join(pv.root, "changed"), []byte("synthetic changed package"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = r.UpgradePlan(t.Context(), "old-example", pv.ID, pv.Digest, nil, "test"); err == nil {
				t.Fatal("stale preview accepted")
			}
			if r.GetExact("old-example") == nil {
				t.Fatal("rejection removed predecessor")
			}
		})
	}
}

func TestIdentityRenameCommitFailureAndUnknownRestart(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			r, s, preview := identityFixture(t, true)
			pv, err := preview("new-example", "old-example", "example-market")
			if err != nil {
				t.Fatal(err)
			}
			s.fail, s.unknown = !unknown, unknown
			called := 0
			r.OnChange = func(Change) { called++ }
			if _, err = r.UpgradePlan(t.Context(), "old-example", pv.ID, pv.Digest, nil, "test"); err == nil {
				t.Fatal("failure presented as success")
			}
			if called != 0 || r.GetExact("old-example") == nil || r.GetExact("new-example") != nil {
				t.Fatal("failed transition activated")
			}
			if unknown {
				if err = r.SetEnabled(t.Context(), "old-example", false, "test"); err == nil {
					t.Fatal("unknown commit did not block writes")
				}
				loaded := NewRegistry(r.dir, s, rot13{}, 202611100000)
				if err = loaded.Load(t.Context()); err != nil {
					t.Fatal(err)
				}
				if p := loaded.Snapshot("old-example"); p == nil || p.Record.ID != "new-example" || p.Problem != "" {
					t.Fatal("restart failed to reconcile committed identity")
				}
			} else {
				if s.rows["old-example"].ID != "old-example" {
					t.Fatal("definite failure changed row")
				}
				s.fail = false
				if _, err = r.UpgradePlan(t.Context(), "old-example", pv.ID, pv.Digest, nil, "test"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestIdentityReplacementGraphAndFingerprint(t *testing.T) {
	entry := func(id, old string) MarketEntry { return MarketEntry{Parsed: &Manifest{ID: id}, Replaces: old} }
	for _, entries := range [][]MarketEntry{{entry("new", "old"), entry("other", "old")}, {entry("one", "two"), entry("two", "one")}, {entry("same", "same")}, {entry("new", "../invalid")}} {
		if err := ValidateReplacements(entries); err == nil {
			t.Fatal("invalid graph accepted")
		}
	}
	e := entry("new", "old")
	before := entryFingerprint(&e)
	e.Replaces = "other"
	if before == entryFingerprint(&e) {
		t.Fatal("declaration absent from signed snapshot")
	}
}

func TestIdentityWholeCatalogPredecessorsAreRetired(t *testing.T) {
	entry := func(id, old string) MarketEntry {
		raw := with(t, func(m map[string]any) { m["id"] = id })
		parsed, err := Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return MarketEntry{Manifest: raw, Parsed: parsed, Replaces: old, Packages: []MarketPackage{{URL: "pkg.tar.gz", SHA256: strings.Repeat("a", 64)}}}
	}
	cases := []struct {
		name    string
		entries []MarketEntry
		valid   bool
	}{
		{"predecessor-current", []MarketEntry{entry("old", ""), entry("new", "old")}, false},
		{"predecessor-current-reversed", []MarketEntry{entry("new", "old"), entry("old", "")}, false},
		{"acyclic-chain", []MarketEntry{entry("middle", "old"), entry("new", "middle")}, false},
		{"acyclic-chain-reversed", []MarketEntry{entry("new", "middle"), entry("middle", "old")}, false},
		{"single-retired-predecessor", []MarketEntry{entry("new", "old"), entry("unrelated", "")}, true},
		{"independent-renames", []MarketEntry{entry("new", "old"), entry("other", "retired")}, true},
		{"ordinary-catalog", []MarketEntry{entry("old", ""), entry("new", "")}, true},
	}
	key, sign := testKey(t)
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateReplacements(test.entries); (err == nil) != test.valid {
				t.Fatalf("whole graph validity=%v: %v", test.valid, err)
			}
			raw, err := json.Marshal(MarketIndex{MarketVersion: 1, Name: "Example", Extensions: test.entries})
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = ParseIndex(raw); (err == nil) != test.valid {
				t.Fatalf("index validity=%v: %v", test.valid, err)
			}
			for _, signed := range []bool{false, true} {
				catalog, pin := raw, ""
				if signed {
					catalog, pin = sign(raw), key
				}
				if _, err = ParseCatalog(catalog, pin); (err == nil) != test.valid {
					t.Fatalf("signed=%v validity=%v: %v", signed, test.valid, err)
				}
			}
		})
	}
}
