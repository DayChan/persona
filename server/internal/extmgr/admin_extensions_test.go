package extmgr

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func adminExtensionHub(t *testing.T) (*Manager, *extensionStore, *b1Admin) {
	t.Helper()
	h := newTestManager(t)
	ps := newExtensionStore()
	h.store = ps
	startTestExtensions(t, h)
	return h, ps, b1SignedIn(t, h)
}

// extensionIn is the plugin id in GET /api/v1/admin/plugins's answer (built-in ones are there too), nil when absent.
func extensionIn(list map[string]any, id string) map[string]any {
	ps, _ := list["plugins"].([]any)
	for _, p := range ps {
		if p, _ := p.(map[string]any); p["id"] == id {
			return p
		}
	}
	return nil
}

// packageTarGz packs a package directory as a .tar.gz under one top-level directory.
func packageTarGz(t *testing.T, dir string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		name := filepath.ToSlash(filepath.Join("pkg", rel))
		if fi.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: name + "/", Typeflag: tar.TypeDir, Mode: 0o755})
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: int64(fi.Mode().Perm()), Size: int64(len(b))}); err != nil {
			return err
		}
		_, err = tw.Write(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// Without the hub's word that an admin is signed in nothing here answers, the app's master token included (the
// session and its CSRF token are the hub's to check: hub/admin*.go).
func TestAdminExtensionsNeedSession(t *testing.T) {
	h, store, c := adminExtensionHub(t)
	pkg := notesPackage(t, "1.0.0")
	anon := &b1Admin{t: t, h: h, remote: b1AdminLAN}
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/plugins", ""},
		{"GET", "/api/v1/admin/extensions", ""},
		{"POST", "/api/v1/admin/plugins/preview", `{"source": "dir:` + pkg + `"}`},
		{"POST", "/api/v1/admin/plugins/install", `{"preview_id": "x", "permissions_digest": "y"}`},
		{"POST", "/api/v1/admin/plugins/example-notes/rollback", `{}`},
		{"DELETE", "/api/v1/admin/plugins/example-notes", `{}`},
	} {
		if code, out, _ := anon.do(c.method, c.path, c.body); code != 401 || out["code"] != "login_required" {
			t.Errorf("%s %s signed out: %d %v", c.method, c.path, code, out)
		}
	}
	if code, out := anon.upload("name=p.tar.gz", packageTarGz(t, pkg), "application/octet-stream"); code != 401 || out["code"] != "login_required" {
		t.Errorf("upload signed out: %d %v", code, out)
	}
	if code, out := call(t, h.routes(), "POST", "/api/v1/admin/plugins/preview", h.cfg.Token, `{"source": "dir:`+pkg+`"}`); code != 401 {
		t.Errorf("the master token is not a session: %d %v", code, out)
	}
	if code, out, _ := c.do("GET", "/api/v1/admin/plugins", ""); code != 200 {
		t.Errorf("signed in: %d %v", code, out)
	}
	if len(store.rows) != 0 {
		t.Fatal("something installed")
	}
}

// From the admin UI: preview (what it may do), install with the digest seen, switch off, upgrade asking for more
// (refused without the new digest), roll back, uninstall keeping its data. The audit names the admin UI.
func TestAdminExtensionsInstallUpgradeRollbackUninstall(t *testing.T) {
	h, store, c := adminExtensionHub(t)
	pkg := notesPackage(t, "1.0.0")

	code, pv, _ := c.do("POST", "/api/v1/admin/plugins/preview", `{"source": "dir:`+pkg+`"}`)
	if code != 200 || pv["permissions_digest"] == "" || len(pv["permissions"].([]any)) != 3 || pv["manifest"].(map[string]any)["id"] != "example-notes" {
		t.Fatalf("preview: %d %v", code, pv)
	}
	if code, out, _ := c.do("POST", "/api/v1/admin/plugins/install", fmt.Sprintf(`{"preview_id": %q}`, pv["preview_id"])); code != 400 || out["code"] != "confirm_required" {
		t.Fatalf("installed without a digest: %d %v", code, out)
	}
	if code, out, _ := c.do("POST", "/api/v1/admin/plugins/install", fmt.Sprintf(`{"preview_id": %q, "permissions_digest": "0000"}`, pv["preview_id"])); code != 400 || out["code"] != "install_failed" {
		t.Fatalf("installed with another digest: %d %v", code, out)
	}
	// A failed install used the preview up: preview again.
	_, pv, _ = c.do("POST", "/api/v1/admin/plugins/preview", `{"source": "dir:`+pkg+`"}`)
	code, out, _ := c.do("POST", "/api/v1/admin/plugins/install", fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	if code != 200 || out["source"] != "installed" || out["status"] != extensionNeedsSetup || out["origin"] != "dir:"+pkg {
		t.Fatalf("install: %d %v", code, out)
	}
	code, list, _ := c.do("GET", "/api/v1/admin/plugins", "")
	notes := extensionIn(list, "example-notes")
	if code != 200 || list["can_install"] != true || notes == nil || list["upload_most"].(float64) < 1<<20 {
		t.Fatalf("list: %d %v", code, list)
	}
	if len(notes["permissions"].([]any)) != 3 || notes["secrets"] == nil {
		t.Fatalf("detail in the list: %v", notes)
	}
	if code, out, _ = c.do("GET", "/api/v1/admin/plugins/nope", ""); code != 404 {
		t.Fatalf("unknown: %d %v", code, out)
	}

	if code, out, _ = c.do("PUT", "/api/v1/admin/plugins/example-notes/enabled", `{"enabled": false}`); code != 200 || out["status"] != "disabled" || out["enabled"] != false {
		t.Fatalf("off: %d %v", code, out)
	}
	c.do("PUT", "/api/v1/admin/plugins/example-notes/enabled", `{"enabled": true}`)

	// 1.1.0 reaches one more host: the upgrade says what is new and needs the new digest.
	pkg2 := notesPackage(t, "1.1.0")
	mf := filepath.Join(pkg2, "persona-extension.json")
	b, _ := os.ReadFile(mf)
	_ = os.WriteFile(mf, []byte(strings.Replace(string(b), `"network": ["notes.example.com"]`, `"network": ["notes.example.com", "files.example.com"]`, 1)), 0o644)
	code, up, _ := c.do("POST", "/api/v1/admin/plugins/preview", `{"source": "dir:`+pkg2+`", "upgrade": "example-notes"}`)
	added, _ := up["added_permissions"].([]any)
	if code != 200 || up["upgrade"] != true || up["from"] != "1.0.0" || up["more_permissions"] != true || len(added) != 1 ||
		added[0].(map[string]any)["detail"] != "files.example.com" {
		t.Fatalf("upgrade preview: %d %v", code, up)
	}
	if code, out, _ = c.do("POST", "/api/v1/admin/plugins/example-notes/upgrade", fmt.Sprintf(`{"preview_id": %q}`, up["preview_id"])); code != 400 || out["code"] != "upgrade_failed" {
		t.Fatalf("upgraded to more without consent: %d %v", code, out)
	}
	_, up, _ = c.do("POST", "/api/v1/admin/plugins/preview", `{"source": "dir:`+pkg2+`", "upgrade": "example-notes"}`)
	code, out, _ = c.do("POST", "/api/v1/admin/plugins/example-notes/upgrade", fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, up["preview_id"], up["permissions_digest"]))
	if code != 200 || out["version"] != "1.1.0" || out["previous_version"] != "1.0.0" {
		t.Fatalf("upgrade: %d %v", code, out)
	}

	if code, out, _ = c.do("POST", "/api/v1/admin/plugins/example-notes/rollback", `{}`); code != 200 || out["version"] != "1.0.0" || out["previous_version"] != nil {
		t.Fatalf("rollback: %d %v", code, out)
	}
	// The version left is skipped by auto-upgrade, as the user's rollback (#1826).
	var us extensionUpgradeSettings
	if _, err := h.store.Setting(t.Context(), extensionUpgradesKey, &us); err != nil || us.Skipped["example-notes"] != "1.1.0" ||
		us.SkippedBy["example-notes"] != skippedByUser {
		t.Fatalf("not skipped after the rollback: %+v %v", us, err)
	}
	if code, out, _ = c.do("POST", "/api/v1/admin/plugins/example-notes/rollback", `{}`); code != 400 || out["code"] != "rollback_failed" {
		t.Fatalf("rolled back twice: %d %v", code, out)
	}

	data := h.extensions.reg.DataDir("example-notes")
	_ = os.MkdirAll(data, 0o700)
	_ = os.WriteFile(filepath.Join(data, "cache"), []byte("kept"), 0o600)
	if code, out, _ = c.do("DELETE", "/api/v1/admin/plugins/example-notes", `{"keep_data": true}`); code != 200 {
		t.Fatalf("uninstall: %d %v", code, out)
	}
	if h.extensions.reg.Get("example-notes") != nil || len(store.rows) != 0 {
		t.Fatal("still installed")
	}
	if _, err := os.Stat(filepath.Join(data, "cache")); err != nil {
		t.Fatal("its data went although kept:", err)
	}
	actions := strings.Join(store.audit, "\n")
	for _, a := range []string{"install admin UI", "disabled admin UI", "enabled admin UI", "upgrade admin UI", "rollback admin UI", "uninstall admin UI"} {
		if !strings.Contains(actions, a) {
			t.Errorf("audit has no %q:\n%s", a, actions)
		}
	}
}

// A package uploaded in the browser: within the size, named like a package, installed from staging.
func TestAdminExtensionsUpload(t *testing.T) {
	_, _, c := adminExtensionHub(t)
	body := packageTarGz(t, notesPackage(t, "1.0.0"))

	if code, out := c.upload("name=notes.exe", body, "application/octet-stream"); code != 400 || out["code"] != "invalid_plugin" {
		t.Fatalf("not a package's name: %d %v", code, out)
	}
	req := httptest.NewRequest("POST", adminUploadPath+"?name=notes.tar.gz", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")
	req.ContentLength = 300 << 20
	w := c.request(req)
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "too_large") {
		t.Fatalf("too large: %d %s", w.Code, w.Body)
	}

	code, pv := c.upload("name=notes-1.0.0.tar.gz", body, "application/octet-stream")
	if code != 200 || pv["manifest"].(map[string]any)["version"] != "1.0.0" {
		t.Fatalf("upload: %d %v", code, pv)
	}
	code, out, _ := c.do("POST", "/api/v1/admin/plugins/install", fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	if code != 200 || out["origin"] != "upload:notes-1.0.0.tar.gz" {
		t.Fatalf("install: %d %v", code, out)
	}
	// Uploading the same plugin again is an upgrade's business.
	if code, out = c.upload("name=notes-1.0.0.tar.gz", body, "application/octet-stream"); code != 400 || !strings.Contains(out["error"].(string), "升级") {
		t.Fatalf("again: %d %v", code, out)
	}
}

// Previews stage up to 200 MB each: at most extensionPreviewsPer in the window, failed ones counted too.
func TestAdminExtensionPreviewsAreLimited(t *testing.T) {
	h, _, c := adminExtensionHub(t)
	now := time.Now()
	h.admin.now = func() time.Time { return now }
	for i := range extensionPreviewsPer {
		if code, out, _ := c.do("POST", "/api/v1/admin/plugins/preview", `{"source": "dir:/nonexistent/plugin"}`); code != 400 || out["code"] != "invalid_plugin" {
			t.Fatalf("preview %d: %d %v", i, code, out)
		}
	}
	code, out, w := c.do("POST", "/api/v1/admin/plugins/preview", `{"source": "dir:/nonexistent/plugin"}`)
	if code != 429 || out["code"] != "rate_limited" || w.Header().Get("Retry-After") == "" {
		t.Fatalf("over the limit: %d %v", code, out)
	}
	now = now.Add(extensionPreviewWindow)
	if code, _, _ := c.do("POST", "/api/v1/admin/plugins/preview", `{"source": "dir:/nonexistent/plugin"}`); code != 400 {
		t.Fatalf("after the window: %d", code)
	}

	// One at a time.
	end, _, busy := h.admin.extensions.begin(now)
	if busy {
		t.Fatal("busy before anything runs")
	}
	if code, out, _ := c.do("POST", "/api/v1/admin/plugins/preview", `{"source": "dir:/nonexistent/plugin"}`); code != 409 || out["code"] != "busy" {
		t.Fatalf("while another runs: %d %v", code, out)
	}
	end()
}

// hub.json's plugins are changed in hub.json only: not switched, upgraded or uninstalled from here.
func TestAdminExtensionsHubJSONStays(t *testing.T) {
	h := newTestManager(t)
	h.store = newExtensionStore()
	startTestExtensions(t, h, ExtensionConfig{ID: "voice", Name: "Voice", Kind: "voice", Socket: "/nonexistent/admin-test.sock"})
	c := b1SignedIn(t, h)
	_, list, _ := c.do("GET", "/api/v1/admin/plugins", "")
	if v := extensionIn(list, "voice"); v == nil || v["read_only"] != true || v["config"] != nil {
		t.Fatalf("%v", list)
	}
	if code, out, _ := c.do("DELETE", "/api/v1/admin/plugins/voice", `{}`); code != 409 || out["code"] != "uninstall_failed" {
		t.Fatalf("uninstalled: %d %v", code, out)
	}
	if code, out, _ := c.do("PUT", "/api/v1/admin/plugins/voice/enabled", `{"enabled": false}`); code != 409 || out["code"] != "read_only" {
		t.Fatalf("switched: %d %v", code, out)
	}
	if code, out, _ := c.do("POST", "/api/v1/admin/plugins/voice/rollback", `{}`); code != 400 {
		t.Fatalf("rolled back: %d %v", code, out)
	}
	if h.extensions.reg.Get("voice") == nil {
		t.Fatal("gone")
	}
}
