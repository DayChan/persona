package extmgr

import (
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/extension/host"
)

// relayStatusWriter notes the status the extension answered with; Unwrap keeps flushing and WebSocket upgrades working
// through it (http.ResponseController).
type relayStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *relayStatusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *relayStatusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *relayStatusWriter) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *relayStatusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// httpRoute is how a relayed request is counted: "HTTP /<first segment>" under the extension's own path, "HTTP
// /api/v1/<prefix>" for its part of the app's API (ids further down would make every request a route of its own).
func httpRoute(id, path string) string {
	for _, base := range []string{"/api/v1/extensions/", "/api/v1/plugins/"} {
		if rest, ok := strings.CutPrefix(path, base+id); ok {
			seg, _, _ := strings.Cut(strings.TrimPrefix(rest, "/"), "/")
			return "HTTP /" + seg
		}
	}
	if rest, ok := strings.CutPrefix(path, "/api/v1/"); ok {
		seg, _, _ := strings.Cut(rest, "/")
		return "HTTP /api/v1/" + seg
	}
	return "HTTP"
}

// httpCode is how a relayed request ended: "" for a success, http_4xx / http_5xx, canceled when the caller left.
func httpCode(r *http.Request, status int) string {
	switch {
	case status == 0 && r.Context().Err() != nil:
		return host.CodeCanceled
	case status >= 500 || status == 0:
		return "http_5xx"
	case status >= 400:
		return "http_4xx"
	}
	return ""
}
