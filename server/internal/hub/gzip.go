package hub

import (
	"compress/gzip"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/webasset"
)

// Responses are gzipped for clients that take it (#2149). One Feishu chat's messages came to 2.9 MB of JSON that
// gzip makes 70 KB, and the phone spent seconds receiving them; the web app's main bundle went out at full size too.
// URLSession, OkHttp, browsers and Go's client all ask for gzip and undo it themselves.
//
// Only what compresses and is worth it: text, JSON, JavaScript, XML, SVG and wasm of gzipMinSize bytes or more
// (webasset.CompressibleType; a published personad keeps the web client's hashed assets gzipped already, web.go).
// These pass as they are:
//   - WebSocket upgrades, long-lived streams (longLived), HEAD, and requests for a range;
//   - the admin pages and API: their cookie and CSRF token are secrets that a compressed size could give away (BREACH);
//   - responses that set a cookie, carry their own encoding or a range, or are an event stream.
const gzipMinSize = webasset.MinSize

var gzipWriters = sync.Pool{New: func() any { return gzip.NewWriter(io.Discard) }}

// gzipResponses is the outermost layer of routes(), outside logRequests: the handlers still get its statusWriter.
//
// A handler that declared a Content-Length and then wrote a different number of bytes broke off mid-response (a
// file download whose later chunk failed, #2367). Uncompressed, net/http would drop the connection over the short
// body and the client would notice; compressed, the length is gone and a proper gzip end would make the truncated
// body look complete. So the gzip end is not written and the connection is aborted instead.
func gzipResponses(logf func(format string, args ...any), next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !gzipWanted(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipWriter{ResponseWriter: w, declared: -1}
		next.ServeHTTP(gw, r)
		// close is not deferred: after a panic, net/http drops the response and nothing more should be written.
		if !gw.close() {
			// The request log has already counted this as a success; say that the client got a broken response.
			logf("[%s] %s %s: aborted a gzipped response after %d of %d declared bytes",
				w.Header().Get("X-Request-Id"), r.Method, loggedPath(r.URL.Path), gw.written, gw.declared)
			panic(http.ErrAbortHandler)
		}
	})
}

// gzipWanted: the client takes gzip, and the request is one whose answer may be compressed.
func gzipWanted(r *http.Request) bool {
	p := r.URL.Path
	switch {
	case r.Method == http.MethodHead, r.Method == http.MethodConnect,
		r.Header.Get("Range") != "", r.Header.Get("Upgrade") != "", longLived(p),
		p == "/admin", strings.HasPrefix(p, "/admin/"), strings.HasPrefix(p, "/api/v1/admin/"):
		return false
	}
	return acceptsGzip(r.Header.Values("Accept-Encoding"))
}

// acceptsGzip reads Accept-Encoding: gzip, or *, with a q above 0.
func acceptsGzip(values []string) bool {
	star := false
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			name, params, _ := strings.Cut(part, ";")
			q := 1.0
			for _, p := range strings.Split(params, ";") {
				if k, val, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(strings.TrimSpace(k), "q") {
					if f, err := strconv.ParseFloat(strings.TrimSpace(val), 64); err == nil {
						q = f
					}
				}
			}
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "gzip", "x-gzip":
				return q > 0
			case "*":
				star = q > 0
			}
		}
	}
	return star
}

// gzipWriter holds the start of a response until it knows whether to compress it. That is when gzipMinSize bytes
// are in, at a Flush (a stream does not wait for them), at the end, or at once when the headers already rule it out.
type gzipWriter struct {
	http.ResponseWriter
	status  int    // the status the handler gave, held until the decision
	buf     []byte // the first bytes, held until the decision
	decided bool
	gz      *gzip.Writer // nil: not compressing
	// declared is the Content-Length the handler set before compressing began (-1: none); written counts the
	// uncompressed bytes the handler wrote, to tell a complete body from a cut one once that header is gone.
	declared int64
	written  int64
}

func (g *gzipWriter) WriteHeader(code int) {
	if g.decided || code < 200 { // an informational answer (103) goes at once; the real one comes later
		g.ResponseWriter.WriteHeader(code)
		return
	}
	if g.status == 0 {
		g.status = code
	}
}

func (g *gzipWriter) Write(p []byte) (int, error) {
	if g.decided {
		if g.gz != nil {
			n, err := g.gz.Write(p)
			g.written += int64(n)
			return n, err
		}
		return g.ResponseWriter.Write(p)
	}
	g.written += int64(len(p))
	if g.status == 0 {
		g.status = http.StatusOK
	}
	g.buf = append(g.buf, p...)
	switch {
	case !g.compressible():
		return len(p), g.start(false)
	case len(g.buf) >= gzipMinSize:
		return len(p), g.start(true)
	}
	return len(p), nil
}

// Flush sends what is held, compressed when it may be, and then whatever gzip has; a stream goes out as it comes.
func (g *gzipWriter) Flush() {
	if !g.decided {
		if g.status == 0 {
			g.status = http.StatusOK
		}
		_ = g.start(g.compressible())
	}
	if g.gz != nil {
		_ = g.gz.Flush()
	}
	_ = http.NewResponseController(g.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the connection beneath: deadlines, and a hijack before anything was written.
func (g *gzipWriter) Unwrap() http.ResponseWriter { return g.ResponseWriter }

// compressible: what the response says of itself, and its first bytes, allow compressing it.
func (g *gzipWriter) compressible() bool {
	h := g.Header()
	switch {
	case g.status < 200, g.status == http.StatusNoContent, g.status == http.StatusNotModified,
		g.status == http.StatusPartialContent, h.Get("Content-Encoding") != "", h.Get("Content-Range") != "",
		len(h.Values("Set-Cookie")) > 0:
		return false
	}
	if n, err := strconv.Atoi(h.Get("Content-Length")); err == nil && n < gzipMinSize {
		return false
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		if len(g.buf) == 0 {
			return false // nothing to tell the type by
		}
		ct = http.DetectContentType(g.buf)
	}
	return webasset.CompressibleType(ct)
}

// start sends the header, compressed or as it is, and then what was held.
func (g *gzipWriter) start(compress bool) error {
	g.decided = true
	if compress {
		h := g.Header()
		if h.Get("Content-Type") == "" { // net/http would sniff the compressed bytes
			h.Set("Content-Type", http.DetectContentType(g.buf))
		}
		if n, err := strconv.ParseInt(h.Get("Content-Length"), 10, 64); err == nil && n >= 0 {
			g.declared = n
		}
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		if etag := h.Get("ETag"); etag != "" && !strings.HasPrefix(etag, "W/") {
			h.Set("ETag", "W/"+etag) // another representation of the same content
		}
		g.gz = gzipWriters.Get().(*gzip.Writer)
		g.gz.Reset(g.ResponseWriter)
	}
	g.ResponseWriter.WriteHeader(g.status)
	buf := g.buf
	g.buf = nil
	if len(buf) == 0 {
		return nil
	}
	var err error
	if g.gz != nil {
		_, err = g.gz.Write(buf)
	} else {
		_, err = g.ResponseWriter.Write(buf)
	}
	return err
}

// close ends the response: what is still held goes out as it is (it is under gzipMinSize), and gzip writes its end.
// It reports false, without writing that end, when a compressed body does not match the length the handler declared.
func (g *gzipWriter) close() bool {
	if !g.decided && (g.status != 0 || len(g.buf) > 0) {
		_ = g.start(false)
	}
	if g.gz == nil {
		return true
	}
	complete := g.declared < 0 || g.written == g.declared
	if complete {
		_ = g.gz.Close()
	}
	g.gz.Reset(io.Discard)
	gzipWriters.Put(g.gz)
	g.gz = nil
	return complete
}
