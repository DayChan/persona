package webasset

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Precompress keeps only big hashed text assets gzipped, reproducibly, and the rest of the site as it was.
func TestPrecompress(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("export const x = 1;\n", 100) // 2,000 bytes
	files := map[string]string{
		"assets/App-1.js":          big,
		"assets/styles-2.css":      big,
		"assets/pdf.worker-3.mjs":  big,
		"assets/icon-4.svg":        big,
		"assets/small-5.js":        "export {};",
		"assets/font-6.woff2":      big,
		"assets/.hidden.js":        big,
		"index.html":               big,
		"sw.js":                    big,
		"manifest.webmanifest":     big,
		"third-party-licenses.txt": big,
		"admin/assets/admin-7.js":  big,
	}
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := Precompress(root)
	if err != nil {
		t.Fatal(err)
	}
	stored := []string{"assets/App-1.js", "assets/styles-2.css", "assets/pdf.worker-3.mjs", "assets/icon-4.svg"}
	if st.Files != len(stored) || st.Before != int64(len(stored)*len(big)) || st.After >= st.Before {
		t.Fatalf("stats %+v", st)
	}
	for name := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		_, rawErr := os.Stat(p)
		gz, gzErr := os.ReadFile(p + Suffix)
		if !slices.Contains(stored, name) {
			if rawErr != nil || gzErr == nil {
				t.Errorf("%s: should stay as it was (raw %v, gz %v)", name, rawErr, gzErr)
			}
			continue
		}
		if rawErr == nil || gzErr != nil {
			t.Fatalf("%s: should be replaced by its gzip copy (raw %v, gz %v)", name, rawErr, gzErr)
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(zr)
		if err != nil || string(body) != files[name] {
			t.Errorf("%s: does not decode to the file: %v", name, err)
		}
		if zr.Name != "" || !zr.ModTime.IsZero() {
			t.Errorf("%s: header carries name %q / time %v: builds would differ", name, zr.Name, zr.ModTime)
		}
		if again, _ := Gzip([]byte(files[name])); !bytes.Equal(again, gz) {
			t.Errorf("%s: not reproducible", name)
		}
	}
	if st, err := Precompress(root); err != nil || st.Files != 0 {
		t.Fatalf("second run: %+v %v", st, err)
	}
}
