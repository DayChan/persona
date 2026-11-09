// Package webasset is what the hub and the build agree on about the web client's files: which responses gzip makes
// worth sending compressed (CompressibleType, MinSize), and which of the built files are kept only gzipped in a
// published personad (Precompressible, Precompress; docs/web/web.md "Precompressed assets"). The hub compresses other
// responses as it sends them (internal/hub/gzip.go); these it sends as they are stored.
package webasset

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// MinSize is the smallest body worth compressing: below it gzip's header and trailer outweigh what it saves.
const MinSize = 1024

// Suffix is the name a precompressed file is kept under: its own name and this.
const Suffix = ".gz"

// CompressibleType: a media type that gzip makes much smaller, and that is not an event stream.
func CompressibleType(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	switch {
	case mt == "text/event-stream":
		return false
	case strings.HasPrefix(mt, "text/"), mt == "application/json", strings.HasSuffix(mt, "+json"),
		mt == "application/javascript", mt == "application/x-javascript", mt == "application/xml",
		strings.HasSuffix(mt, "+xml"), mt == "application/wasm":
		return true
	}
	return false
}

// Precompressible says whether the build keeps the file at name (a slash path below the web root) gzipped, when it
// has MinSize bytes or more: a file under assets/ (Vite names them by their content hash, so they never change
// under one name) whose extension is of a CompressibleType. index.html, sw.js, the manifest, the admin page's files
// and the root's other files stay as they are.
func Precompressible(name string) bool {
	base := path.Base(name)
	return strings.HasPrefix(name, "assets/") && !strings.HasPrefix(base, ".") &&
		CompressibleType(mime.TypeByExtension(path.Ext(base)))
}

// Gzip is how a precompressed file is kept: gzip at its best compression, with no name or time in its header, so
// the same input always gives the same bytes.
func Gzip(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Stats counts what Precompress did: the files it replaced, their bytes before and after.
type Stats struct {
	Files         int
	Before, After int64
}

// Precompress replaces each file below root that is Precompressible and of MinSize bytes or more with its Gzip
// copy (name + Suffix). Run it on the web client's copy before go build embeds it (scripts/release/publish.sh,
// scripts/release/build_hub_release.py, the Dockerfile); running it again changes nothing.
func Precompress(root string) (Stats, error) {
	var st Stats
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil || !Precompressible(filepath.ToSlash(rel)) {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil || len(data) < MinSize {
			return err
		}
		gz, err := Gzip(data)
		if err != nil {
			return err
		}
		if err := os.WriteFile(p+Suffix, gz, 0o644); err != nil {
			return err
		}
		st.Files++
		st.Before += int64(len(data))
		st.After += int64(len(gz))
		return os.Remove(p)
	})
	return st, err
}
