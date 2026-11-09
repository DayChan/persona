//go:build !windows

package hub

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
)

// firstScreenKB are the sizes of the files a cold browser fetches for the web client's first screen (en, the
// 2026-11 build: index.html, the entry, its preloads and the catalog), largest first.
var firstScreenKB = []int{1053, 376, 208, 177, 129, 92, 58, 24, 8, 6, 6, 6, 5, 4, 3, 2, 2, 2, 1, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0}

// firstScreenSite is a site with files of those sizes, cut from this package's Go sources (text that compresses
// about as well as the minified chunks), and the paths a browser asks for.
func firstScreenSite(b *testing.B) (fstest.MapFS, []string) {
	names, err := filepath.Glob("*.go")
	if err != nil {
		b.Fatal(err)
	}
	slices.Sort(names)
	var text []byte
	for _, n := range names {
		src, err := os.ReadFile(n)
		if err != nil {
			b.Fatal(err)
		}
		text = append(text, src...)
	}
	site := fstest.MapFS{"index.html": {Data: []byte("<html>" + string(text[:7<<10]) + "</html>")}}
	paths := []string{"/"}
	for i, kb := range firstScreenKB {
		n := max(kb<<10, 200)
		name := fmt.Sprintf("assets/chunk-%02d.js", i)
		site[name] = &fstest.MapFile{Data: text[:n]}
		text = text[n:]
		paths = append(paths, "/"+name)
	}
	return site, paths
}

// BenchmarkWebFirstScreen serves one cold first screen to a browser (Accept-Encoding: gzip, ...) through gzipResponses:
// compressed as it goes (gzip6, a data dir copy or a build that skipped the precompress step) and as stored (the
// built-in copy of a published personad). wire-B is what goes out.
func BenchmarkWebFirstScreen(b *testing.B) {
	plain, paths := firstScreenSite(b)
	for _, c := range []struct {
		name string
		site fstest.MapFS
	}{{"gzip6", plain}, {"stored", precompressedSite(b, plain)}} {
		b.Run(c.name, func(b *testing.B) {
			h := gzipResponses(func(string, ...any) {}, webFrom(b.TempDir(), c.site))
			sent := 0
			b.ReportAllocs()
			for b.Loop() {
				sent = 0
				for _, p := range paths {
					r := httptest.NewRequest(http.MethodGet, p, nil)
					r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
					w := &discardResponse{}
					h.ServeHTTP(w, r)
					sent += w.n
				}
			}
			b.ReportMetric(float64(sent), "wire-B")
		})
	}
}
