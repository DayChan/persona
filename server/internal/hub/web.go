package hub

import (
	"bytes"
	"compress/gzip"
	"embed"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/webasset"
)

// webDist is the web client built into a published personad (scripts/release/publish.sh copies web/dist here before building);
// in other builds only the placeholder is there. Its hashed assets are kept gzipped (webasset.Precompress, run by the
// build after the copy): name.gz instead of name.
//
//go:embed all:webdist
var webDist embed.FS

// webApp serves the web client (web/, docs/web/web.md) from <data dir>/web on the hub's own port, so it shares the
// API's origin: no CORS, and the WebSocket and images authenticate the same way. Without that directory the copy built
// into personad serves (published builds), and without either every non-API path is a 404, as before. Paths without a
// file fall back to index.html (client-side routes).
//
// The built-in copy's precompressed assets (docs/web/web.md "Precompressed assets") go out as they are stored to a
// client that takes gzip, and decompressed to any other request, with the headers and bytes a plain file would
// have. The data dir's files are plain and gzipResponses compresses them as it sends them, as before.
func webApp(dir string) http.Handler {
	embedded, _ := fs.Sub(webDist, "webdist")
	return webFrom(dir, embedded)
}

func webFrom(dir string, embedded fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// The data dir wins (scripts/deploy/deploy.sh web swaps it in whole), checked on every request like before.
		var site fs.FS
		builtIn := false // site is the copy built into personad, whose assets may be stored gzipped
		if st, err := os.Stat(filepath.Join(dir, "index.html")); err == nil && !st.IsDir() {
			site = os.DirFS(dir)
		} else if embedded != nil {
			if st, err := fs.Stat(embedded, "index.html"); err == nil && !st.IsDir() {
				site, builtIn = embedded, true
			}
		}
		if site == nil {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		file := name
		asset, hidden := strings.HasPrefix(name, "assets/"), strings.HasPrefix(path.Base(name), ".")
		// In the built-in copy an asset's .gz is how another asset is stored, never a file of its own (Vite makes none).
		storedForm := builtIn && asset && strings.HasSuffix(name, webasset.Suffix)
		stored := false // file is kept as file + webasset.Suffix
		if name == "" || hidden || storedForm || !isFile(site, name) {
			switch {
			case builtIn && asset && !hidden && !storedForm && isFile(site, name+webasset.Suffix):
				stored = true
			case asset: // a missing hashed asset is a real 404, not the app
				http.NotFound(w, r)
				return
			default:
				file = "index.html"
			}
		}
		switch {
		// the service worker and the manifest (docs/web/web.md "Install as an app") are checked again on every load, like the shell
		case file == "index.html", file == "sw.js", file == "manifest.webmanifest":
			w.Header().Set("Cache-Control", "no-cache")
		case strings.HasPrefix(name, "assets/"):
			// Vite names them by content hash
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		if path.Ext(file) == ".webmanifest" { // not in Go's built-in table, and nosniff would leave it untyped
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if file == "index.html" {
			serveIndex(w, r, site)
			return
		}
		if stored {
			serveStored(w, r, site, file)
			return
		}
		f, err := site.Open(file)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		rs, ok := f.(io.ReadSeeker)
		if err != nil || !ok {
			http.NotFound(w, r)
			return
		}
		http.ServeContent(w, r, path.Base(file), st.ModTime(), rs)
	})
}

// isFile: name is in site and not a directory.
func isFile(site fs.FS, name string) bool {
	st, err := fs.Stat(site, name)
	return err == nil && !st.IsDir()
}

// serveStored serves the asset name that site keeps as name + webasset.Suffix. A request gzipResponses would
// compress (gzipWanted) gets the stored bytes, marked as gzip, which gzipResponses then passes on as they are. Any
// other (no gzip, HEAD, a range) gets them decompressed as they are read (gunzipSeeker), the same answer as for the
// plain file.
func serveStored(w http.ResponseWriter, r *http.Request, site fs.FS, name string) {
	f, err := site.Open(name + webasset.Suffix)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	rs, ok := f.(io.ReadSeeker)
	if err != nil || !ok {
		http.NotFound(w, r)
		return
	}
	// The type goes by the plain name, as http.ServeContent would; one only sniffing could tell is sent decompressed.
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" && gzipWanted(r) {
		h := w.Header()
		h.Set("Content-Type", ct)
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		http.ServeContent(w, r, path.Base(name), st.ModTime(), rs)
		return
	}
	plain, err := newGunzipSeeker(rs)
	if err != nil { // a broken build; not to be kept for a year by a cache
		w.Header().Del("Cache-Control")
		http.Error(w, "broken asset", http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, path.Base(name), st.ModTime(), plain)
}

// gunzipSeeker reads a gzip file's content as http.ServeContent reads a file: its size comes from the gzip trailer
// (ISIZE, which gzip.Reader checks at the end), so a HEAD decompresses nothing, a GET as it is sent, and a range only
// up to its end. A seek back starts decompressing again from the top.
type gunzipSeeker struct {
	src       io.ReadSeeker // the gzip bytes
	zr        *gzip.Reader
	size      int64
	pos, want int64 // where zr is, where the next Read starts
}

func newGunzipSeeker(src io.ReadSeeker) (*gunzipSeeker, error) {
	var trailer [4]byte
	if _, err := src.Seek(-int64(len(trailer)), io.SeekEnd); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(src, trailer[:]); err != nil {
		return nil, err
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(src) // reads and checks the header
	if err != nil {
		return nil, err
	}
	size := int64(trailer[0]) | int64(trailer[1])<<8 | int64(trailer[2])<<16 | int64(trailer[3])<<24
	return &gunzipSeeker{src: src, zr: zr, size: size}, nil
}

func (g *gunzipSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekCurrent:
		offset += g.want
	case io.SeekEnd:
		offset += g.size
	}
	if offset < 0 {
		return 0, errors.New("gunzipSeeker: negative position")
	}
	g.want = offset
	return offset, nil
}

func (g *gunzipSeeker) Read(p []byte) (int, error) {
	if g.want < g.pos { // back: from the top again
		if _, err := g.src.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		if err := g.zr.Reset(g.src); err != nil {
			return 0, err
		}
		g.pos = 0
	}
	if g.want > g.pos {
		n, err := io.CopyN(io.Discard, g.zr, g.want-g.pos)
		g.pos += n
		if err != nil {
			return 0, err
		}
	}
	n, err := g.zr.Read(p)
	g.pos += int64(n)
	g.want = g.pos
	return n, err
}

// serveIndex serves index.html with its <base> at the sub-path a reverse proxy serves the hub under (docs/web/web.md "Sub-path"):
// the page's files are relative to it and lib/base.ts reads it for the API, the WebSocket and the page's routes. The
// same file answers differently per X-Forwarded-Prefix, so a cache must key on it (Vary).
func serveIndex(w http.ResponseWriter, r *http.Request, site fs.FS) {
	b, err := fs.ReadFile(site, "index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var mod time.Time
	if st, err := fs.Stat(site, "index.html"); err == nil {
		mod = st.ModTime()
	}
	w.Header().Add("Vary", "X-Forwarded-Prefix")
	http.ServeContent(w, r, "index.html", mod, bytes.NewReader(withBase(b, forwardedPrefix(r))))
}

// maxPrefix bounds X-Forwarded-Prefix: a proxy's sub-path is a few short segments.
const maxPrefix = 256

// forwardedPrefix is the sub-path a reverse proxy serves the hub under, from its X-Forwarded-Prefix ("/persona",
// trailing slashes dropped), or "" at the root. Only plain path segments count: no dot segments, nothing that could
// leave the attribute (quotes, angle brackets, spaces, control characters, percent escapes); anything else is ignored.
func forwardedPrefix(r *http.Request) string {
	v := r.Header.Get("X-Forwarded-Prefix")
	if v == "" || len(v) > maxPrefix || v[0] != '/' {
		return ""
	}
	p := strings.TrimRight(v, "/")
	if p == "" {
		return ""
	}
	for _, seg := range strings.Split(p[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return ""
		}
		for i := 0; i < len(seg); i++ {
			c := seg[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~') {
				return ""
			}
		}
	}
	return p
}

// baseTag is index.html's <base> as web/index.html writes it (and Vite keeps it).
var baseTag = []byte(`<base href="/" />`)

// withBase points index.html's <base> at prefix + "/"; at the root (prefix "") the page is as it was built.
func withBase(index []byte, prefix string) []byte {
	if prefix == "" {
		return index
	}
	return bytes.Replace(index, baseTag, []byte(`<base href="`+prefix+`/" />`), 1)
}
