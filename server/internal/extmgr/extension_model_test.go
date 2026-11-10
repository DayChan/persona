package extmgr

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

// declarativePackage writes a package with only manifest (no program).
func declarativePackage(t *testing.T, manifest string) string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// From a market: an extension's missing dependency installs with it (one screen, every digest), switching the
// dependency off asks first and takes the dependent along, and a pack installs and leaves with its members.
func TestExtensionDependenciesAndPacks(t *testing.T) {
	h, phone, _ := marketHub(t)
	mux := h.routes()
	base := declarativePackage(t, `{"manifest_version": 1, "id": "base", "name": "Base", "version": "1.2.0",
		"contributes": {"providers": [{"id": "base-p", "name": "Base P", "base_url": "https://base.example.com"}]}}`)
	addon := declarativePackage(t, `{"manifest_version": 1, "id": "addon", "name": "Addon", "version": "1.0.0",
		"depends": [{"id": "base", "version": ">=1.0.0"}],
		"contributes": {"providers": [{"id": "addon-p", "name": "Addon P", "base_url": "https://addon.example.com"}]}}`)
	pack := declarativePackage(t, `{"manifest_version": 1, "id": "both", "name": "Both", "version": "1.0.0",
		"extension_pack": [{"id": "base"}, {"id": "addon"}]}`)
	pkgs := map[string][]byte{"base.tar.gz": tarDir(t, base), "addon.tar.gz": tarDir(t, addon), "both.tar.gz": tarDir(t, pack)}
	gh := &fakeGitHub{release: "v1", files: map[string][]byte{}, assets: pkgs}
	srv := httptest.NewTLSServer(gh)
	defer srv.Close()
	gh.storage = srv.URL
	h.extensions.markets.client, h.extensions.markets.githubAPI = srv.Client(), srv.URL
	gh.files[extension.IndexFile] = marketIndex(t, "Example",
		marketEntry(t, base, "release:v1/base.tar.gz", pkgs["base.tar.gz"]),
		marketEntry(t, addon, "release:v1/addon.tar.gz", pkgs["addon.tar.gz"]),
		marketEntry(t, pack, "release:v1/both.tar.gz", pkgs["both.tar.gz"]))
	code, mk := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, `{"spec": "github:example/plugins"}`)
	if code != 200 || mk["extensions"] != float64(3) {
		t.Fatalf("%d %v", code, mk)
	}
	preview := func(id string) map[string]any {
		t.Helper()
		code, pv := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "extension": %q}`, mk["id"], id))
		if code != 200 {
			t.Fatalf("preview %s: %d %v", id, code, pv)
		}
		return pv
	}
	bundleOf := func(pv map[string]any) (ids []string, refs string) {
		var parts []string
		for _, b := range pv["bundle"].([]any) {
			b := b.(map[string]any)
			ids = append(ids, b["manifest"].(map[string]any)["id"].(string))
			parts = append(parts, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, b["preview_id"], b["permissions_digest"]))
		}
		return ids, "[" + strings.Join(parts, ",") + "]"
	}
	install := func(pv map[string]any, bundle string) (int, map[string]any) {
		return call(t, mux, "POST", "/api/v1/extensions/-/install", phone,
			fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q, "bundle": %s}`, pv["preview_id"], pv["permissions_digest"], bundle))
	}

	pv := preview("addon")
	deps := pv["depends"].([]any)
	if len(deps) != 1 || deps[0].(map[string]any)["state"] != "missing" {
		t.Fatalf("%v", pv["depends"])
	}
	ids, refs := bundleOf(pv)
	if len(ids) != 1 || ids[0] != "base" {
		t.Fatal(ids)
	}
	if code, out := install(pv, "[]"); code != 400 || h.extensions.reg.Get("base") != nil {
		t.Fatalf("without its bundle nothing installs: %d %v", code, out)
	}
	if code, out := install(pv, refs); code != 200 || out["status"] != "running" {
		t.Fatalf("%d %v", code, out)
	} else if ds, ok := out["depends"].([]any); !ok || len(ds) != 1 || ds[0].(map[string]any)["state"] != "ok" {
		t.Fatalf("installed detail includes dependencies: %v", out)
	}
	if h.extensions.reg.Get("base") == nil || extension.FromMarket(h.extensions.reg.Get("base").Record.Origin) != mk["id"] {
		t.Fatal("the dependency came from the market")
	}

	// Switching base off: asked, then both go off.
	code, out := call(t, mux, "PUT", "/api/v1/extensions/base/-/enabled", phone, `{"enabled": false}`)
	if code != 409 || out["code"] != "has_dependents" || !strings.Contains(fmt.Sprint(out["dependents"]), "addon") {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/extensions/base/-/enabled", phone, `{"enabled": false, "cascade": true}`); code != 200 {
		t.Fatal(code)
	}
	if h.extensions.reg.Get("addon").Record.Enabled || h.extensions.reg.Get("base").Record.Enabled {
		t.Fatal("both off")
	}
	_, _ = call(t, mux, "PUT", "/api/v1/extensions/base/-/enabled", phone, `{"enabled": true}`)
	_, _ = call(t, mux, "PUT", "/api/v1/extensions/addon/-/enabled", phone, `{"enabled": true}`)
	if code, out := call(t, mux, "DELETE", "/api/v1/extensions/base/-/", phone, ""); code != 409 || out["code"] != "has_dependents" {
		t.Fatalf("removing what another needs asks first: %d %v", code, out)
	}
	for _, id := range []string{"addon", "base"} {
		if code, out := call(t, mux, "DELETE", "/api/v1/extensions/"+id+"/-/", phone, ""); code != 200 {
			t.Fatalf("%s: %d %v", id, code, out)
		}
	}

	// The pack: its members in its bundle, in order, each once.
	pv = preview("both")
	ids, refs = bundleOf(pv)
	if strings.Join(ids, ",") != "base,addon" {
		t.Fatal(ids)
	}
	if code, out := install(pv, refs); code != 200 || out["kind"] != "extension_pack" {
		t.Fatalf("%d %v", code, out)
	}
	for _, id := range []string{"base", "addon", "both"} {
		if p := h.extensions.reg.Get(id); p == nil || !p.Record.Enabled {
			t.Fatalf("%s installed and on", id)
		}
	}
	outside := declarativePackage(t, `{"manifest_version":1,"id":"outside","name":"Outside","version":"1.0.0","depends":[{"id":"addon"}],"contributes":{"providers":[{"id":"outside-p","name":"Outside P","base_url":"https://outside.example.com"}]}}`)
	op, err := h.extensions.reg.Preview(t.Context(), "dir:"+outside, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.extensions.reg.Install(t.Context(), op.ID, op.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	code, out = call(t, mux, "DELETE", "/api/v1/extensions/both/-/?members=1", phone, "")
	if code != 409 || out["code"] != "has_dependents" || h.extensions.reg.Get("both") == nil || h.extensions.reg.Get("base") == nil {
		t.Fatalf("pack member's external dependents require confirmation before any removal: %d %v", code, out)
	}
	code, out = call(t, mux, "DELETE", "/api/v1/extensions/both/-/?members=1&cascade=1", phone, "")
	if code != 200 || len(out["removed_members"].([]any)) != 2 {
		t.Fatalf("%d %v", code, out)
	}
	if h.extensions.reg.Get("outside").Record.Enabled {
		t.Fatal("external dependent is stopped, not removed")
	}
	if h.extensions.reg.Get("base") != nil || h.extensions.reg.Get("addon") != nil {
		t.Fatal("the members left with the pack")
	}
}

// Switching a pack off switches its members off too (#1830), after asking about extensions outside the pack that use
// them; switching it back on turns on only the members it switched off and the user has not switched since.
func TestExtensionPackSwitchesMembers(t *testing.T) {
	h, phone, _ := marketHub(t)
	mux := h.routes()
	base := declarativePackage(t, `{"manifest_version": 1, "id": "base", "name": "Base", "version": "1.0.0",
		"contributes": {"providers": [{"id": "base-p", "name": "Base P", "base_url": "https://base.example.com"}]}}`)
	addon := declarativePackage(t, `{"manifest_version": 1, "id": "addon", "name": "Addon", "version": "1.0.0",
		"depends": [{"id": "base"}],
		"contributes": {"providers": [{"id": "addon-p", "name": "Addon P", "base_url": "https://addon.example.com"}]}}`)
	solo := declarativePackage(t, `{"manifest_version": 1, "id": "solo", "name": "Solo", "version": "1.0.0",
		"contributes": {"providers": [{"id": "solo-p", "name": "Solo P", "base_url": "https://solo.example.com"}]}}`)
	pack := declarativePackage(t, `{"manifest_version": 1, "id": "group", "name": "Group", "version": "1.0.0",
		"extension_pack": [{"id": "addon"}, {"id": "base"}, {"id": "solo"}]}`)
	pkgs := map[string][]byte{"base.tar.gz": tarDir(t, base), "addon.tar.gz": tarDir(t, addon), "solo.tar.gz": tarDir(t, solo), "group.tar.gz": tarDir(t, pack)}
	gh := &fakeGitHub{release: "v1", files: map[string][]byte{}, assets: pkgs}
	srv := httptest.NewTLSServer(gh)
	defer srv.Close()
	gh.storage = srv.URL
	h.extensions.markets.client, h.extensions.markets.githubAPI = srv.Client(), srv.URL
	gh.files[extension.IndexFile] = marketIndex(t, "Example",
		marketEntry(t, base, "release:v1/base.tar.gz", pkgs["base.tar.gz"]),
		marketEntry(t, addon, "release:v1/addon.tar.gz", pkgs["addon.tar.gz"]),
		marketEntry(t, solo, "release:v1/solo.tar.gz", pkgs["solo.tar.gz"]),
		marketEntry(t, pack, "release:v1/group.tar.gz", pkgs["group.tar.gz"]))
	code, mk := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, `{"spec": "github:example/plugins"}`)
	if code != 200 {
		t.Fatalf("%d %v", code, mk)
	}
	code, pv := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "extension": "group"}`, mk["id"]))
	if code != 200 {
		t.Fatalf("preview: %d %v", code, pv)
	}
	var refs []string
	for _, b := range pv["bundle"].([]any) {
		b := b.(map[string]any)
		refs = append(refs, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, b["preview_id"], b["permissions_digest"]))
	}
	if code, out := call(t, mux, "POST", "/api/v1/extensions/-/install", phone, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q, "bundle": [%s]}`,
		pv["preview_id"], pv["permissions_digest"], strings.Join(refs, ","))); code != 200 {
		t.Fatalf("install: %d %v", code, out)
	}
	if code, out := call(t, mux, "GET", "/api/v1/extensions/group/-/", phone, ""); code != 200 || out["pack_members_follow"] != true || out["pack_off"] != nil {
		t.Fatalf("a pack's detail says its switch takes the members: %d %v", code, out)
	}
	outside := declarativePackage(t, `{"manifest_version":1,"id":"outside","name":"Outside","version":"1.0.0","depends":[{"id":"addon"}],"contributes":{"providers":[{"id":"outside-p","name":"Outside P","base_url":"https://outside.example.com"}]}}`)
	op, err := h.extensions.reg.Preview(t.Context(), "dir:"+outside, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.extensions.reg.Install(t.Context(), op.ID, op.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	enabled := func(id string) bool { return h.extensions.reg.Get(id).Record.Enabled }
	put := func(id, body string) (int, map[string]any) {
		t.Helper()
		return call(t, mux, "PUT", "/api/v1/extensions/"+id+"/-/enabled", phone, body)
	}
	// The user's own choice before the pack goes off; something outside the pack still uses solo, and is on.
	if code, out := put("solo", `{"enabled": false}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	fan := declarativePackage(t, `{"manifest_version":1,"id":"solofan","name":"Solo Fan","version":"1.0.0","depends":[{"id":"solo"}],"contributes":{"providers":[{"id":"solofan-p","name":"Solo Fan P","base_url":"https://solofan.example.com"}]}}`)
	if fp, err := h.extensions.reg.Preview(t.Context(), "dir:"+fan, ""); err != nil {
		t.Fatal(err)
	} else if _, err = h.extensions.reg.Install(t.Context(), fp.ID, fp.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	if !enabled("solofan") {
		t.Fatal("solofan installed on")
	}
	if code, out := call(t, mux, "GET", "/api/v1/extensions/group/-/", phone, ""); code != 200 || fmt.Sprint(out["pack_on"]) != "[map[id:addon name:Addon] map[id:base name:Base]]" {
		t.Fatalf("a pack's detail names the members switching it off takes along: %d %v", code, out["pack_on"])
	}

	code, out := put("group", `{"enabled": false}`)
	if code != 409 || out["code"] != "has_dependents" || !strings.Contains(fmt.Sprint(out["dependents"]), "outside") {
		t.Fatalf("an outside dependent of a member is asked about first: %d %v", code, out)
	}
	if strings.Contains(fmt.Sprint(out["dependents"]), "solofan") {
		t.Fatalf("a member already off is not the pack's to take, nor are its dependents: %v", out["dependents"])
	}
	for _, id := range []string{"group", "base", "addon", "outside"} {
		if !enabled(id) {
			t.Fatalf("%s switched before the answer", id)
		}
	}
	code, out = put("group", `{"enabled": false, "cascade": true}`)
	if code != 200 || out["enabled"] != false || fmt.Sprint(out["pack_off"]) != "[addon base]" || out["pack_on"] != nil {
		t.Fatalf("%d %v", code, out)
	}
	for _, id := range []string{"group", "base", "addon", "solo", "outside"} {
		if enabled(id) {
			t.Fatalf("%s still on after the pack went off", id)
		}
	}
	if !enabled("solofan") {
		t.Fatal("the dependent of a member the pack left alone was switched off")
	}
	if _, d := call(t, mux, "GET", "/api/v1/extensions/base/-/", phone, ""); d["status"] != "disabled" {
		t.Fatalf("a member of a pack switched off stops: %v", d)
	}

	// Back on: the recorded members go on (addon, listed before base, which it needs, too); solo, off by the user, stays off.
	code, out = put("group", `{"enabled": true}`)
	if code != 200 || out["enabled"] != true || out["pack_off"] != nil || out["warnings"] != nil {
		t.Fatalf("%d %v", code, out)
	}
	for id, want := range map[string]bool{"group": true, "base": true, "addon": true, "solo": false, "outside": false} {
		if enabled(id) != want {
			t.Fatalf("after the pack went back on, %s on = %v, want %v", id, enabled(id), want)
		}
	}

	// Switching a member by itself takes it out of the pack's memory: switched on and off again, it stays off.
	if code, out := put("group", `{"enabled": false}`); code != 200 || fmt.Sprint(out["pack_off"]) != "[addon base]" {
		t.Fatalf("%d %v", code, out)
	}
	if code, _ := put("addon", `{"enabled": true}`); code != 200 {
		t.Fatal(code)
	}
	if code, _ := put("addon", `{"enabled": false}`); code != 200 {
		t.Fatal(code)
	}
	if got := h.extensions.reg.Get("group").Record.PackOff; fmt.Sprint(got) != "[base]" {
		t.Fatalf("pack still remembers a member the user switched: %v", got)
	}
	code, out = put("group", `{"enabled": true}`)
	if code != 200 || out["enabled"] != true || out["pack_off"] != nil {
		t.Fatalf("%d %v", code, out)
	}
	for id, want := range map[string]bool{"group": true, "base": true, "addon": false, "solo": false} {
		if enabled(id) != want {
			t.Fatalf("after the pack went back on again, %s on = %v, want %v", id, enabled(id), want)
		}
	}
	if got := h.extensions.reg.Get("group").Record.PackOff; got != nil {
		t.Fatalf("pack forgets once on: %v", got)
	}

	// A member that cannot be switched off: what this switch already switched off, members and the outside
	// dependent it cascaded to, goes back on, and the pack stays on without a record.
	if code, out := put("addon", `{"enabled": true}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if err := h.extensions.reg.SetEnabled(t.Context(), "outside", true, "test"); err != nil {
		t.Fatal(err)
	}
	store := h.store.(*marketTestStore)
	store.mu.Lock()
	store.failPutID = "base" // addon is switched off before base
	store.mu.Unlock()
	code, out = put("group", `{"enabled": false, "cascade": true}`)
	store.mu.Lock()
	store.failPutID = ""
	store.mu.Unlock()
	if code != 409 {
		t.Fatalf("%d %v", code, out)
	}
	for id, want := range map[string]bool{"group": true, "base": true, "addon": true, "outside": true, "solo": false, "solofan": true} {
		if enabled(id) != want {
			t.Fatalf("after a switch-off that failed, %s on = %v, want %v", id, enabled(id), want)
		}
	}
	if got := h.extensions.reg.Get("group").Record.PackOff; got != nil {
		t.Fatalf("a switch-off that failed records nothing: %v", got)
	}
}

// Every recorded member is tried in one pass, whatever the order; one that is refused is a pending one with why.
func TestPackTurnOnAllReportsRefusals(t *testing.T) {
	h, _, _ := marketHub(t)
	ps := h.extensions
	for _, m := range []string{
		`{"manifest_version":1,"id":"first","name":"First","version":"1.0.0","depends":[{"id":"second"}],"contributes":{"providers":[{"id":"first-p","name":"First P","base_url":"https://first.example.com"}]}}`,
		`{"manifest_version":1,"id":"second","name":"Second","version":"1.0.0","contributes":{"providers":[{"id":"second-p","name":"Second P","base_url":"https://second.example.com"}]}}`,
		`{"manifest_version":1,"id":"third","name":"Third","version":"1.0.0","contributes":{"providers":[{"id":"third-p","name":"Third P","base_url":"https://third.example.com"}]}}`,
	} {
		pv, err := ps.reg.Preview(t.Context(), "dir:"+declarativePackage(t, m), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = ps.reg.Install(t.Context(), pv.ID, pv.Digest, "test"); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"first", "second", "third"} {
		if err := ps.reg.SetEnabled(t.Context(), id, false, "test"); err != nil {
			t.Fatal(err)
		}
	}
	refused := errors.New("not now")
	ps.reg.CanEnable = func(_ context.Context, _ string, m *extension.Manifest) error {
		if m.ID == "third" {
			return refused
		}
		return nil
	}
	pending, failed := ps.turnOnAll(t.Context(), []string{"first", "second", "third", "gone"}, "test")
	if fmt.Sprint(pending) != "[third]" || !errors.Is(failed["third"], refused) || len(failed) != 1 {
		t.Fatalf("pending %v, failed %v", pending, failed)
	}
	for id, want := range map[string]bool{"first": true, "second": true, "third": false} {
		if got := ps.reg.Get(id).Record.Enabled; got != want {
			t.Fatalf("%s on = %v, want %v", id, got, want)
		}
	}
}
