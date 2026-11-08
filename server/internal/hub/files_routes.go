package hub

import (
	"net/http"
	"strings"
)

// fileRoutes are a machine's files and folders, read and uploaded through its node.
func (h *Hub) fileRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines/{mid}/dirs", h.proxy("dirs.list", func(r *http.Request) any {
		return map[string]any{"path": r.URL.Query().Get("path")}
	}))
	api.HandleFunc("GET /api/v1/machines/{mid}/file/download", h.withMachine(h.downloadFile))
	api.HandleFunc("GET /api/v1/machines/{mid}/file/thumb", h.withMachine(h.fileThumb))
	api.HandleFunc("GET /api/v1/machines/{mid}/file/convert", h.withMachine(h.fileConvert))
	// stream=1: a binary file comes back described (stream, revision), its bytes from file/download.
	api.HandleFunc("GET /api/v1/machines/{mid}/file", h.proxy("file.read", func(r *http.Request) any {
		q := r.URL.Query()
		return map[string]any{"path": q.Get("path"), "cwd": q.Get("cwd"), "offset": atoi(q.Get("offset")), "stream": q.Get("stream") == "1"}
	}))
	// Which of the paths a conversation mentions are there on the machine (?paths=a%0Ab or ?path=a&path=b, and cwd):
	// the phone links only those.
	api.HandleFunc("GET /api/v1/machines/{mid}/stat", h.proxy("fs.stat", func(r *http.Request) any {
		q := r.URL.Query()
		paths := q["path"]
		if s := q.Get("paths"); s != "" {
			paths = append(paths, strings.Split(s, "\n")...)
		}
		return map[string]any{"paths": paths, "cwd": q.Get("cwd")}
	}))
	api.HandleFunc("GET /api/v1/machines/{mid}/fs", h.proxy("fs.list", func(r *http.Request) any {
		return map[string]any{"path": r.URL.Query().Get("path"), "cwd": r.URL.Query().Get("cwd")}
	}))
	// #2438 files-find: every file under cwd (git-aware), for the web palette's file search. Read like /fs: full
	// credentials only (an answer-only device has no machines/{mid}/ route, routeScopes has no entry for it).
	api.HandleFunc("GET /api/v1/machines/{mid}/fs/find", h.proxy("fs.find", func(r *http.Request) any {
		return map[string]any{"cwd": r.URL.Query().Get("cwd"), "limit": atoi(r.URL.Query().Get("limit"))}
	}))
	api.HandleFunc("POST /api/v1/machines/{mid}/files", h.withMachine(h.uploadFile))
}
