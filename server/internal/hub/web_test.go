package hub

import (
	"bytes"
	"io/fs"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/nwverse/persona/server/internal/webasset"
)

func TestWebAppServesFilesAndFallsBackToIndex(t *testing.T) {
	dir := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dir, "assets"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>app</html>"), 0o644))
	must(os.WriteFile(filepath.Join(dir, "assets", "a-1.js"), []byte("js"), 0o644))
	must(os.WriteFile(filepath.Join(dir, ".secret"), []byte("no"), 0o644))
	h := webApp(dir)
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		return w
	}
	if w := get("/assets/a-1.js"); w.Code != 200 || w.Body.String() != "js" || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("asset: %d %q %q", w.Code, w.Body, w.Header().Get("Cache-Control"))
	}
	for _, p := range []string{"/", "/s/laptop/abc", "/.secret", "/../../etc/passwd"} {
		if w := get(p); w.Code != 200 || !strings.Contains(w.Body.String(), "app") || w.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: %d %q", p, w.Code, w.Body)
		}
	}
	// The installable app's files (docs/web/web.md "Install as an app"): typed, and never kept past a redeploy.
	must(os.WriteFile(filepath.Join(dir, "manifest.webmanifest"), []byte(`{"name":"Persona"}`), 0o644))
	must(os.WriteFile(filepath.Join(dir, "sw.js"), []byte("self"), 0o644))
	if w := get("/manifest.webmanifest"); w.Code != 200 || w.Header().Get("Content-Type") != "application/manifest+json" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("manifest: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
	}
	if w := get("/sw.js"); w.Code != 200 || w.Body.String() != "self" || !strings.Contains(w.Header().Get("Content-Type"), "javascript") || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("sw.js: %d %q %q", w.Code, w.Header().Get("Content-Type"), w.Header().Get("Cache-Control"))
	}
	if w := get("/assets/gone.js"); w.Code != 404 {
		t.Fatalf("missing asset: %d", w.Code)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("post: %d", w.Code)
	}
	if w := httptest.NewRecorder(); true {
		webApp(t.TempDir()).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 404 {
			t.Fatalf("no web dir: %d", w.Code)
		}
	}
}

// Without a data dir copy, the web client built into personad serves; the data dir wins when it has one.
func TestWebAppFallsBackToTheEmbeddedCopy(t *testing.T) {
	embedded := fstest.MapFS{"index.html": {Data: []byte("<html>embedded</html>")}, "assets/b-2.js": {Data: []byte("ejs")}}
	dir := t.TempDir()
	h := webFrom(dir, embedded)
	get := func(p string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		return w
	}
	if w := get("/machines"); w.Code != 200 || !strings.Contains(w.Body.String(), "embedded") {
		t.Fatalf("embedded index: %d %q", w.Code, w.Body)
	}
	if w := get("/assets/b-2.js"); w.Code != 200 || w.Body.String() != "ejs" {
		t.Fatalf("embedded asset: %d %q", w.Code, w.Body)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>disk</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := get("/"); !strings.Contains(w.Body.String(), "disk") {
		t.Fatalf("data dir should win: %q", w.Body)
	}
	// A build without the web client (only the placeholder) is a 404, as before.
	if w := httptest.NewRecorder(); true {
		webApp(t.TempDir()).ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 404 {
			t.Fatalf("no web client: %d", w.Code)
		}
	}
}

// Behind a reverse proxy that serves the hub under a sub-path (docs/web/web.md "Sub-path"), index.html's <base> says where.
func TestWebAppWritesTheProxyPrefixIntoBase(t *testing.T) {
	dir := t.TempDir()
	index := `<head><base href="/" /><script src="./assets/a-1.js"></script></head>app`
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "assets", "a-1.js"), []byte(`<base href="/" />`), 0o644); err != nil {
		t.Fatal(err)
	}
	h := webApp(dir)
	get := func(p string, prefix ...string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", p, nil)
		for _, v := range prefix {
			r.Header.Add("X-Forwarded-Prefix", v)
		}
		h.ServeHTTP(w, r)
		return w
	}
	for _, c := range []struct {
		path, prefix, want string
	}{
		{"/", "", `<base href="/" />`},
		{"/s/laptop/abc", "/persona", `<base href="/persona/" />`},
		{"/", "/persona/", `<base href="/persona/" />`},
		{"/", "/a/b-c_d.e~f", `<base href="/a/b-c_d.e~f/" />`},
		{"/", "/", `<base href="/" />`},
		// not a plain path: ignored
		{"/", "persona", `<base href="/" />`},
		{"/", `/x" onload="alert(1)`, `<base href="/" />`},
		{"/", "/x><script>", `<base href="/" />`},
		{"/", "/a b", `<base href="/" />`},
		{"/", "/a\tb", `<base href="/" />`},
		{"/", "/a/../b", `<base href="/" />`},
		{"/", "/a/./b", `<base href="/" />`},
		{"/", "/..", `<base href="/" />`},
		{"/", "//evil.example.com", `<base href="/" />`},
		{"/", "/a%2fb", `<base href="/" />`},
		{"/", "/" + strings.Repeat("a", maxPrefix), `<base href="/" />`},
	} {
		w := get(c.path, c.prefix)
		if w.Code != 200 || !strings.Contains(w.Body.String(), c.want) || strings.Count(w.Body.String(), "<base") != 1 {
			t.Errorf("%s with prefix %q: %d %q, want %s", c.path, c.prefix, w.Code, w.Body, c.want)
		}
		if w.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(w.Header().Get("Vary"), "X-Forwarded-Prefix") {
			t.Errorf("%s with prefix %q: Cache-Control %q, Vary %q", c.path, c.prefix, w.Header().Get("Cache-Control"), w.Header().Get("Vary"))
		}
	}
	// only index.html is rewritten: a file is served as it is
	if w := get("/assets/a-1.js", "/persona"); w.Body.String() != `<base href="/" />` {
		t.Errorf("asset rewritten: %q", w.Body)
	}
	// the length matches what is sent, and a conditional request still gets its 304
	w := get("/", "/persona")
	if got, want := w.Header().Get("Content-Length"), len(strings.Replace(index, `href="/"`, `href="/persona/"`, 1)); got != strconv.Itoa(want) {
		t.Errorf("Content-Length %s, want %d", got, want)
	}
	if lm := w.Header().Get("Last-Modified"); lm != "" {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Forwarded-Prefix", "/persona")
		r.Header.Set("If-Modified-Since", lm)
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r)
		if w2.Code != http.StatusNotModified {
			t.Errorf("conditional: %d", w2.Code)
		}
	} else {
		t.Error("no Last-Modified on index.html")
	}
}

// precompressedSite is site as a published personad embeds it: written out, run through webasset.Precompress (what
// the build does), read back with no modification times (like embed.FS).
func precompressedSite(t testing.TB, site fstest.MapFS) fstest.MapFS {
	t.Helper()
	dir := t.TempDir()
	for name, f := range site {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := webasset.Precompress(dir); err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		rel, _ := filepath.Rel(dir, p)
		out[filepath.ToSlash(rel)] = &fstest.MapFile{Data: data}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The built-in copy's precompressed assets answer every request as the plain files did: the oracle is the same site
// not precompressed, behind the same gzipResponses. Only a gzipped answer may differ, by having a Content-Length;
// it is then exactly the stored bytes, not compressed again.
func TestWebAppServesPrecompressedAssetsAsThePlainFiles(t *testing.T) {
	js := strings.Repeat("export const answer = 42; // a hashed chunk\n", 100)
	plain := fstest.MapFS{
		"index.html":              {Data: []byte("<html>" + strings.Repeat("<p>app</p>", 200) + "</html>")},
		"sw.js":                   {Data: []byte(js)},
		"assets/App-1.js":         {Data: []byte(strings.Repeat(js, 25))}, // past io.Copy's 32 KB buffer
		"assets/styles-2.css":     {Data: []byte(strings.Repeat("a{color:red}\n", 200))},
		"assets/icon-3.png":       {Data: append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{0, 1, 2, 3}, 600)...)},
		"assets/small-4.js":       {Data: []byte("export {};")},
		"admin/index.html":        {Data: []byte("<html>admin</html>")},
		"admin/assets/admin-5.js": {Data: []byte(js)},
	}
	stored := precompressedSite(t, plain)
	if _, ok := stored["assets/App-1.js"]; ok || stored["assets/App-1.js.gz"] == nil || stored["sw.js"] == nil {
		t.Fatalf("not precompressed as the build does: %v", slices.Sorted(maps.Keys(stored)))
	}
	oracle := gzipResponses(t.Logf, webFrom(t.TempDir(), plain))
	h := gzipResponses(t.Logf, webFrom(t.TempDir(), stored))
	browser := "gzip, deflate, br, zstd"
	requests := []struct {
		name, method string
		header       map[string]string
	}{
		{"gzip", "GET", map[string]string{"Accept-Encoding": browser}},
		{"identity", "GET", nil},
		{"gzip;q=0", "GET", map[string]string{"Accept-Encoding": "gzip;q=0, br"}},
		{"HEAD", "HEAD", map[string]string{"Accept-Encoding": browser}},
		{"Range", "GET", map[string]string{"Accept-Encoding": browser, "Range": "bytes=10-99"}},
		{"suffix Range", "GET", map[string]string{"Range": "bytes=-50"}},
		{"If-Modified-Since", "GET", map[string]string{"Accept-Encoding": browser, "If-Modified-Since": "Mon, 02 Jan 2006 15:04:05 GMT"}},
	}
	paths := []string{"/assets/App-1.js", "/assets/styles-2.css", "/assets/icon-3.png", "/assets/small-4.js", "/",
		"/s/laptop/abc", "/sw.js", "/admin/assets/admin-5.js", "/assets/gone-6.js", "/assets/App-1.js.gz"}
	decoded := func(w *httptest.ResponseRecorder) string {
		if w.Header().Get("Content-Encoding") == "gzip" {
			return gunzip(t, w.Body.Bytes())
		}
		return w.Body.String()
	}
	for _, p := range paths {
		for _, rq := range requests {
			serve := func(h http.Handler) *httptest.ResponseRecorder {
				r := httptest.NewRequest(rq.method, p, nil)
				for k, v := range rq.header {
					r.Header.Set(k, v)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			want, got := serve(oracle), serve(h)
			wantH, gotH := want.Header().Clone(), got.Header().Clone()
			if wantH.Get("Content-Encoding") == "gzip" { // compressed as it went: no length to compare
				gotH.Del("Content-Length")
			}
			if got.Code != want.Code || !reflect.DeepEqual(gotH, wantH) || decoded(got) != decoded(want) {
				t.Errorf("%s %s: got %d %v (%d body bytes), want %d %v (%d)", rq.name, p, got.Code, gotH, got.Body.Len(),
					want.Code, wantH, want.Body.Len())
			}
			if gz := stored[strings.TrimPrefix(p, "/")+webasset.Suffix]; gz != nil && got.Header().Get("Content-Encoding") == "gzip" &&
				!bytes.Equal(got.Body.Bytes(), gz.Data) {
				t.Errorf("%s %s: compressed again instead of sent as stored", rq.name, p)
			}
		}
	}
	// A stored copy that does not decompress is the build's fault: an error, not one a cache keeps for a year.
	stored["assets/broken-7.js.gz"] = &fstest.MapFile{Data: []byte("not gzip")}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/assets/broken-7.js", nil))
	if w.Code != http.StatusInternalServerError || w.Header().Get("Cache-Control") != "" {
		t.Fatalf("broken stored asset: %d, Cache-Control %q", w.Code, w.Header().Get("Cache-Control"))
	}
}
