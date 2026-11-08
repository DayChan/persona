package extmgr

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
)

func TestExtensionIdentitySuccessorLookupIsSourceBoundAndUnique(t *testing.T) {
	entry := func(id, replaces string) extension.MarketEntry {
		return extension.MarketEntry{Parsed: &extension.Manifest{ID: id, Version: "1.1.0"}, Replaces: replaces}
	}
	index := &extension.MarketIndex{Extensions: []extension.MarketEntry{entry("successor", "previous")}}
	ms := &markets{byID: map[string]*marketState{
		"source":    {rec: extension.MarketRecord{ID: "source", Spec: "git:https://example.com/extensions.git"}, index: index},
		"elsewhere": {rec: extension.MarketRecord{ID: "elsewhere"}, index: &extension.MarketIndex{}},
	}}
	if _, e, err := marketEntryLocked(ms, "source", "previous"); err != nil || e.Parsed.ID != "successor" {
		t.Fatalf("successor: %v %+v", err, e)
	}
	if _, _, err := marketEntryLocked(ms, "elsewhere", "previous"); err == nil {
		t.Fatal("cross-source successor discovered")
	}
	index.Extensions = append(index.Extensions, entry("other-successor", "previous"))
	if _, _, err := marketEntryLocked(ms, "source", "previous"); err == nil {
		t.Fatal("ambiguous successor chosen")
	}
	index.Extensions = append(index.Extensions, entry("previous", ""))
	if _, e, err := marketEntryLocked(ms, "source", "previous"); err != nil || e.Parsed.ID != "previous" {
		t.Fatalf("normal entry did not take precedence: %v %+v", err, e)
	}
}

func TestExtensionIdentityCommittedOwnerAliasAndRetiredToken(t *testing.T) {
	h, phone, _ := marketDevices(t)
	mux := h.routes()
	mk := newUpgradeMarket(t)
	h.extensions.markets.client = mk.srv.Client()
	dir := helperPackage(t)
	mk.publish(dir)
	code, market := call(t, mux, "POST", "/api/v1/extensions/-/markets", phone, fmt.Sprintf(`{"spec":%q}`, mk.srv.URL+"/plugins/"))
	if code != 200 {
		t.Fatalf("market: %d %+v", code, market)
	}
	mid := market["id"].(string)
	code, pv := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"example-helper"}`, mid))
	if code != 200 {
		t.Fatalf("preview: %d %+v", code, pv)
	}
	code, installed := call(t, mux, "POST", "/api/v1/extensions/-/install", phone, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, pv["preview_id"], pv["permissions_digest"]))
	if code != 200 {
		t.Fatalf("install: %d %+v", code, installed)
	}
	reg := h.extensions.reg
	if err := reg.SetSecret(context.Background(), "example-helper", "api_key", "fixture-secret", "test"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetConfig(context.Background(), "example-helper", json.RawMessage(`{"computers":["configured-node"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	u := h.extensionUpgrades()
	u.loaded = true
	u.settings.Off = []string{"example-helper"}
	u.settings.Skipped = map[string]string{"example-helper": "1.0.9"}
	old := reg.SnapshotExact("example-helper")
	editManifest(t, dir, func(m map[string]any) { m["id"], m["version"] = "renamed-helper", "1.1.0" })
	mk.publish(dir)
	mk.mu.Lock()
	var catalog map[string]any
	if err := json.Unmarshal(mk.index, &catalog); err != nil {
		t.Fatal(err)
	}
	catalog["plugins"].([]any)[0].(map[string]any)["replaces"] = "example-helper"
	mk.index, _ = json.Marshal(catalog)
	mk.mu.Unlock()
	if _, err := h.marketRead(context.Background(), mid); err != nil {
		t.Fatal(err)
	}
	code, rename := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"renamed-helper"}`, mid))
	if code != 200 || rename["rename_from"] != "example-helper" {
		t.Fatalf("rename preview: %d %+v", code, rename)
	}
	// A configured node that cannot be reached must not let the identity commit.
	code, blocked := call(t, mux, "POST", "/api/v1/extensions/-/install", phone, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, rename["preview_id"], rename["permissions_digest"]))
	if code == 200 || reg.GetExact("example-helper") == nil || reg.GetExact("renamed-helper") != nil {
		t.Fatalf("unready node allowed identity commit: %d %+v", code, blocked)
	}
	_, _ = extensionNodeMachine(t, h, "configured-node", "linux", "amd64", false)
	code, rename = call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"renamed-helper"}`, mid))
	if code != 200 {
		t.Fatalf("ready rename preview: %d %+v", code, rename)
	}
	// The final manager guard must recheck the declaration, not trust a preview-time catalog pointer.
	staged := reg.PreviewOf(rename["preview_id"].(string))
	ms := h.marketsState()
	ms.mu.Lock()
	var declared *extension.MarketEntry
	for i := range ms.byID[mid].index.Extensions {
		if ms.byID[mid].index.Extensions[i].Parsed.ID == "renamed-helper" {
			declared = &ms.byID[mid].index.Extensions[i]
		}
	}
	declared.Replaces = ""
	ms.mu.Unlock()
	if release, err := h.guardExtensionPlan(context.Background(), []*extension.Preview{staged}); err == nil {
		if release != nil {
			release()
		}
		t.Fatal("changed predecessor declaration passed final guard")
	}
	ms.mu.Lock()
	declared.Replaces = "example-helper"
	ms.mu.Unlock()
	// Even generic install submission must act on the preview's predecessor, never create a second slot.
	code, changed := call(t, mux, "POST", "/api/v1/extensions/-/install", phone, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, rename["preview_id"], rename["permissions_digest"]))
	if code != 200 || changed["id"] != "renamed-helper" {
		t.Fatalf("rename install: %d %+v", code, changed)
	}
	current := reg.SnapshotExact("renamed-helper")
	if current == nil || current.Record.StorageKey() != old.Record.StorageKey() || reg.GetExact("example-helper") != nil {
		t.Fatalf("second installation: %+v", current)
	}
	code, detail := call(t, mux, "GET", "/api/v1/extensions/example-helper/-/", phone, "")
	if code != 200 || detail["id"] != "renamed-helper" {
		t.Fatalf("owner alias: %d %+v", code, detail)
	}
	secrets, err := reg.Secrets(context.Background(), "example-helper")
	if err != nil || secrets["api_key"] != "fixture-secret" {
		t.Fatalf("same secrets: %v %v", secrets, err)
	}
	_, own := u.auto("renamed-helper")
	if own || u.settings.Skipped[old.Record.StorageKey()] != "1.0.9" {
		t.Fatal("stable upgrade choices lost")
	}
	token := h.extTokens.issue("example-helper", "Retired fixture", []string{extension.ScopeModels})
	if auth := h.authorize(extwire.Authorize{Hash: hashToken(token), Pattern: "GET /api/v1/model-config", Method: "GET", Path: "/api/v1/model-config"}); auth.OK || auth.Extension != "" {
		t.Fatalf("retired token gained alias authority: %+v", auth)
	}
	// Rollback retains both historical names, but the successor is not installed
	// under its public ID until the owner explicitly confirms another rename.
	code, rolled := call(t, mux, "POST", "/api/v1/extensions/renamed-helper/-/rollback", phone, `{}`)
	if code != 200 || rolled["id"] != "example-helper" || reg.GetExact("renamed-helper") != nil {
		t.Fatalf("identity rollback: %d %+v", code, rolled)
	}
	items := h.catalog("", mid)
	if len(items) != 1 || items[0].RenameFrom != "example-helper" || items[0].Installed != "1.0.0" || !items[0].Upgradable {
		t.Fatalf("successor alias hid rename after rollback: %+v", items)
	}
	for _, id := range []string{"example-helper", "renamed-helper"} {
		code, rename = call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":%q}`, mid, id))
		if code != 200 || rename["rename_from"] != "example-helper" {
			t.Fatalf("re-upgrade preview through %s: %d %+v", id, code, rename)
		}
	}
	code, blocked = call(t, mux, "POST", "/api/v1/extensions/example-helper/-/upgrade", phone, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":""}`, rename["preview_id"]))
	if code == 200 || reg.GetExact("example-helper") == nil {
		t.Fatalf("rollback alias bypassed explicit confirmation: %d %+v", code, blocked)
	}
	code, changed = call(t, mux, "POST", "/api/v1/extensions/example-helper/-/upgrade", phone, fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, rename["preview_id"], rename["permissions_digest"]))
	if code != 200 || changed["id"] != "renamed-helper" || reg.SnapshotExact("renamed-helper").Record.StorageKey() != old.Record.StorageKey() {
		t.Fatalf("confirmed re-upgrade of the same slot: %d %+v", code, changed)
	}
}

// marketTestStore keeps plugins and markets (the device tokens are the hub's).
type marketTestStore struct {
	*extensionStore
}

// marketHub is a test hub whose store keeps plugins and markets; phone is a device with full rights, reader one that
// can only read and answer (their tokens the hub's: the relayed requests say whose they are).
func marketHub(t *testing.T) (h *Manager, phone, reader string) {
	h = newTestManager(t)
	h.store = &marketTestStore{extensionStore: newExtensionStore()}
	startTestExtensions(t, h)
	return h, deviceToken("dev_phone", roleFull), deviceToken("dev_watch", roleAnswer)
}

// tarDir packs dir as a .tar.gz package.
func tarDir(t *testing.T, dir string) []byte {
	t.Helper()
	var buf strings.Builder
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		info, _ := d.Info()
		hdr, _ := tar.FileInfoHeader(info, "")
		hdr.Name = rel
		if d.IsDir() {
			hdr.Name += "/"
		}
		_ = tw.WriteHeader(hdr)
		if !d.IsDir() {
			b, _ := os.ReadFile(p)
			_, _ = tw.Write(b)
		}
		return nil
	})
	_ = tw.Close()
	_ = gz.Close()
	return []byte(buf.String())
}

// fakeGitHub serves a repository's contents and release assets the way GitHub's API does, and remembers which
// requests carried the token.
type fakeGitHub struct {
	mu      sync.Mutex
	files   map[string][]byte // path in the repository → content
	assets  map[string][]byte // release asset name → content
	token   string
	authed  []string
	leaked  bool
	release string
	storage string // where release assets redirect to: another host
	stall   bool   // assets send half and then nothing (a download that stalls)
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	auth := r.Header.Get("Authorization")
	if auth != "" {
		g.authed = append(g.authed, r.URL.Path)
	}
	if strings.HasPrefix(r.URL.Path, "/s3/") {
		g.leaked = g.leaked || auth != ""
		b := g.assets[strings.TrimPrefix(r.URL.Path, "/s3/")]
		if g.stall {
			g.mu.Unlock()
			_, _ = w.Write(b[:len(b)/2])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			g.mu.Lock()
			return
		}
		_, _ = w.Write(b)
		return
	}
	if g.token != "" && auth != "Bearer "+g.token {
		http.NotFound(w, r)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/repos/example/plugins/contents/"):
		b, ok := g.files[strings.TrimPrefix(r.URL.Path, "/repos/example/plugins/contents/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	case r.URL.Path == "/repos/example/plugins/releases/tags/"+g.release:
		var assets []map[string]string
		for name := range g.assets {
			assets = append(assets, map[string]string{"name": name, "url": "https://" + r.Host + "/repos/example/plugins/releases/assets/" + name})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"assets": assets})
	case strings.HasPrefix(r.URL.Path, "/repos/example/plugins/releases/assets/"):
		// GitHub redirects to storage on another host (g.storage, the same handler on another server), which must not get the token.
		http.Redirect(w, r, g.storage+"/s3/"+strings.TrimPrefix(r.URL.Path, "/repos/example/plugins/releases/assets/"), http.StatusFound)
	default:
		http.NotFound(w, r)
	}
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func marketIndex(t *testing.T, name string, entries ...map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{"market_version": 1, "name": name, "description": "示例市场", "plugins": entries})
	return b
}

func marketEntry(t *testing.T, dir, url string, pkg []byte, tags ...string) map[string]any {
	manifest, err := os.ReadFile(filepath.Join(dir, extension.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"manifest": json.RawMessage(manifest), "tags": tags,
		"packages": []any{map[string]any{"url": url, "sha256": sum(pkg), "size": len(pkg)}}}
}

func setVersion(t *testing.T, dir, version string) {
	p := filepath.Join(dir, extension.ManifestFile)
	b, _ := os.ReadFile(p)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	m["version"] = version
	b, _ = json.Marshal(m)
	_ = os.WriteFile(p, b, 0o644)
}

// A private GitHub repository as a market: added with its token, its plugins in the catalog and searchable,
// installed from a phone with full rights (the package from a release, its sha256 checked), upgraded and removed
// there; the token goes to the API only, never to the storage a release asset redirects to.
func TestExtensionMarketGitHub(t *testing.T) {
	h, phone, reader := marketHub(t)
	mux := h.routes()
	dir := uiPackage(t)
	v1 := tarDir(t, dir)
	setVersion(t, dir, "1.1.0")
	v2 := tarDir(t, dir)

	gh := &fakeGitHub{token: "ghp_example", release: "v1", files: map[string][]byte{}, assets: map[string][]byte{"example-ui-1.0.0.tar.gz": v1}}
	srv := httptest.NewTLSServer(gh)
	defer srv.Close()
	storage := httptest.NewTLSServer(gh)
	defer storage.Close()
	// storage.example.com (the test certificate's name) reaches the storage server.
	gh.storage = "https://storage.example.com"
	tr := srv.Client().Transport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == "storage.example.com:443" {
			addr = storage.Listener.Addr().String()
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	h.extensions.markets.client, h.extensions.markets.githubAPI = &http.Client{Transport: tr}, srv.URL
	setIndex := func(entries ...map[string]any) {
		gh.mu.Lock()
		gh.files[extension.IndexFile] = marketIndex(t, "Example plugins", entries...)
		gh.mu.Unlock()
	}
	setIndex(marketEntry(t, dir, "release:v1/example-ui-1.0.0.tar.gz", v1, "面板"))
	// the index above was written with 1.1.0's manifest; v1's must say 1.0.0
	setVersion(t, dir, "1.0.0")
	setIndex(marketEntry(t, dir, "release:v1/example-ui-1.0.0.tar.gz", v1, "面板"))

	if code, _ := call(t, mux, "POST", "/api/v1/plugins/-/markets", reader, `{"spec": "github:example/plugins"}`); code != 403 {
		t.Fatalf("an answer-only device adds a market: %d", code)
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/markets", phone, `{"spec": "github:example/plugins"}`); code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "token") {
		t.Fatalf("a private repository without its token: %d %v", code, out)
	}
	code, mk := call(t, mux, "POST", "/api/v1/plugins/-/markets", phone, `{"spec": "https://github.com/example/plugins", "token": "ghp_example"}`)
	if code != 200 || mk["name"] != "Example plugins" || mk["plugins"] != float64(1) || mk["has_token"] != true || mk["spec"] != "github:example/plugins" {
		t.Fatalf("%d %v", code, mk)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/plugins/-/markets", phone, `{"spec": "github:example/plugins", "token": "ghp_example"}`); code != 400 {
		t.Fatal("the same market twice")
	}
	_, cat := call(t, mux, "GET", "/api/v1/plugins/-/catalog?q=面板", phone, "")
	items, _ := cat["plugins"].([]any)
	if len(items) != 1 {
		t.Fatalf("searched by tag: %v", cat)
	}
	it := items[0].(map[string]any)
	if it["id"] != "example-ui" || it["installed"] != nil || it["problem"] != nil || !strings.Contains(fmt.Sprint(it["permissions"]), "收到 hub 的事件") {
		t.Fatalf("%v", it)
	}
	if _, cat := call(t, mux, "GET", "/api/v1/plugins/-/catalog?q=nothing-like-it", phone, ""); len(cat["plugins"].([]any)) != 0 {
		t.Fatal("search")
	}

	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "plugin": "example-ui"}`, mk["id"]))
	if pv["market"] != mk["id"] || pv["preview_id"] == nil {
		t.Fatalf("%v", pv)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/plugins/-/install", reader, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 403 {
		t.Fatal("an answer-only device installs")
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", phone, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 || out["version"] != "1.0.0" {
		t.Fatalf("installed from the phone: %d %v", code, out)
	}
	if p := h.extensions.reg.Get("example-ui"); p == nil || extension.FromMarket(p.Record.Origin) != mk["id"] {
		t.Fatalf("its origin: %+v", p)
	}

	// A new version in the market: the catalog says so, the phone upgrades.
	gh.mu.Lock()
	gh.release, gh.assets = "v2", map[string][]byte{"example-ui-1.1.0.tar.gz": v2}
	gh.mu.Unlock()
	setVersion(t, dir, "1.1.0")
	setIndex(marketEntry(t, dir, "release:v2/example-ui-1.1.0.tar.gz", v2))
	_, cat = call(t, mux, "GET", "/api/v1/plugins/-/catalog?refresh=1", phone, "")
	it = cat["plugins"].([]any)[0].(map[string]any)
	if it["installed"] != "1.0.0" || it["upgradable"] != true || it["from_this_market"] != true {
		t.Fatalf("%v", it)
	}
	_, pv = call(t, mux, "POST", "/api/v1/plugins/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "plugin": "example-ui"}`, mk["id"]))
	if pv["upgrade"] != true {
		t.Fatalf("%v", pv)
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/example-ui/-/upgrade", phone, fmt.Sprintf(`{"preview_id": %q}`, pv["preview_id"])); code != 200 || out["version"] != "1.1.0" {
		t.Fatalf("upgraded from the phone: %d %v", code, out)
	}

	// A package that is not what the index pins is refused (the cache has the pinned one: downloaded again).
	_ = os.RemoveAll(h.packageCache())
	setIndex(marketEntry(t, dir, "release:v2/example-ui-1.1.0.tar.gz", v1))
	call(t, mux, "POST", "/api/v1/plugins/-/markets/"+mk["id"].(string)+"/refresh", phone, "")
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "plugin": "example-ui"}`, mk["id"])); code != 400 || !strings.Contains(fmt.Sprint(out["error"]), "sha256") {
		t.Fatalf("a package not pinned: %d %v", code, out)
	}

	if code, _ := call(t, mux, "DELETE", "/api/v1/plugins/example-ui/-/", phone, ""); code != 200 || h.extensions.reg.Get("example-ui") != nil {
		t.Fatalf("removed from the phone: %d", code)
	}
	gh.mu.Lock()
	leaked := gh.leaked
	gh.mu.Unlock()
	if leaked {
		t.Fatal("the token went to the storage a release asset redirects to")
	}
	if code, _ := call(t, mux, "DELETE", "/api/v1/plugins/-/markets/"+mk["id"].(string), phone, ""); code != 200 {
		t.Fatal(code)
	}
	if _, out := call(t, mux, "GET", "/api/v1/plugins/-/markets", phone, ""); len(out["markets"].([]any)) != 0 {
		t.Fatal(out)
	}
}

// A website as a market (packages beside the index); a plugin installed from elsewhere is still the hub machine's
// to remove; a package not for this platform is shown with why.
func TestExtensionMarketSite(t *testing.T) {
	h, phone, _ := marketHub(t)
	mux := h.routes()
	dir := uiPackage(t)
	pkg := tarDir(t, dir)
	other := marketEntry(t, dir, "pkg/x.tar.gz", pkg)
	other["manifest"] = json.RawMessage(strings.Replace(string(other["manifest"].(json.RawMessage)), `"example-ui"`, `"example-other"`, 1))
	other["packages"] = []any{map[string]any{"platforms": []string{"plan9-mips"}, "url": "pkg/x.tar.gz", "sha256": sum(pkg)}}
	index := marketIndex(t, "Site plugins", marketEntry(t, dir, "pkg/example-ui.tar.gz", pkg), other, map[string]any{"manifest": map[string]any{"id": "Bad"}})
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/plugins/" + extension.IndexFile:
			_, _ = w.Write(index)
		case "/plugins/pkg/example-ui.tar.gz":
			_, _ = w.Write(pkg)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	h.extensions.markets.client = srv.Client()

	code, mk := call(t, mux, "POST", "/api/v1/plugins/-/markets", phone, fmt.Sprintf(`{"spec": %q}`, srv.URL+"/plugins/"))
	if code != 200 || mk["plugins"] != float64(2) || len(mk["warnings"].([]any)) != 1 {
		t.Fatalf("%d %v", code, mk)
	}
	_, cat := call(t, mux, "GET", "/api/v1/plugins/-/catalog", phone, "")
	for _, x := range cat["plugins"].([]any) {
		if it := x.(map[string]any); it["id"] == "example-other" && !strings.Contains(fmt.Sprint(it["problem"]), "plan9-mips") && !strings.Contains(fmt.Sprint(it["problem"]), extension.Platform()) {
			t.Fatalf("%v", it)
		}
	}
	_, pv := call(t, mux, "POST", "/api/v1/plugins/-/catalog/preview", phone, fmt.Sprintf(`{"market": %q, "plugin": "example-ui"}`, mk["id"]))
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", phone, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("%d %v", code, out)
	}

	// Installed from a directory on the hub's machine: the phone may not remove it, nor install from a source.
	h2, phone2, _ := marketHub(t)
	mux2 := h2.routes()
	_, pv = call(t, mux2, "POST", "/api/v1/plugins/-/preview", h2.cfg.Token, `{"source": "dir:`+dir+`"}`)
	call(t, mux2, "POST", "/api/v1/plugins/-/install", h2.cfg.Token, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
	if code, _ := call(t, mux2, "DELETE", "/api/v1/plugins/example-ui/-/", phone2, ""); code != 403 {
		t.Fatalf("the phone removed a plugin not from a market: %d", code)
	}
	if code, _ := call(t, mux2, "POST", "/api/v1/plugins/-/preview", phone2, `{"source": "dir:`+dir+`"}`); code != 403 {
		t.Fatalf("the phone previewed a directory: %d", code)
	}
}
