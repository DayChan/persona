package hub

import (
	"net/http"
	"time"
)

// gitRoutes are the Git panel's reads and writes on a machine's repositories, through its node.
func (h *Hub) gitRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines/{mid}/git/status", h.proxy("git.status", func(r *http.Request) any {
		return map[string]any{"cwd": r.URL.Query().Get("cwd")}
	}))
	api.HandleFunc("POST /api/v1/machines/{mid}/git/pull", h.proxyBody("git.pull"))
	// One version of a file, written into the node's cache for comparing (docs/protocol/protocol.md "File preview").
	api.HandleFunc("GET /api/v1/machines/{mid}/git/blob", h.proxy("git.blob", func(r *http.Request) any {
		q := r.URL.Query()
		return map[string]any{"cwd": q.Get("cwd"), "path": q.Get("path"), "rev": q.Get("rev")}
	}))
	api.HandleFunc("GET /api/v1/machines/{mid}/git/diff", h.proxy("git.diff", func(r *http.Request) any {
		q := r.URL.Query()
		return map[string]any{"cwd": q.Get("cwd"), "path": q.Get("path"), "staged": q.Get("staged") == "1"}
	}))
	api.HandleFunc("GET /api/v1/machines/{mid}/git/show", h.proxy("git.show", func(r *http.Request) any {
		return map[string]any{"cwd": r.URL.Query().Get("cwd"), "hash": r.URL.Query().Get("hash")}
	}))
	// Every file a scope changed with its hunks, for the web's Review panel (gitreview.go, #151).
	api.HandleFunc("GET /api/v1/machines/{mid}/git/review", h.withMachine(h.gitReview))
	api.HandleFunc("GET /api/v1/machines/{mid}/git/log", h.proxy("git.log", func(r *http.Request) any {
		q := r.URL.Query()
		return map[string]any{"cwd": q.Get("cwd"), "skip": atoi(q.Get("skip")), "limit": atoi(q.Get("limit"))}
	}))
	// The Git panel's writes and the branch's pull request (#6121, node gitwrite.go / gitpr.go): each one a click of
	// the user's that the client confirmed; a commit's hooks and a push may take a while.
	api.HandleFunc("GET /api/v1/machines/{mid}/git/branches", h.proxy("git.branches", func(r *http.Request) any {
		return map[string]any{"cwd": r.URL.Query().Get("cwd")}
	}))
	api.HandleFunc("GET /api/v1/machines/{mid}/git/pr", h.proxy("git.pr", func(r *http.Request) any {
		return map[string]any{"cwd": r.URL.Query().Get("cwd")}
	}))
	api.HandleFunc("POST /api/v1/machines/{mid}/git/stage", h.proxyBody("git.stage"))
	api.HandleFunc("POST /api/v1/machines/{mid}/git/commit", h.proxyBodyFor("git.commit", 5*time.Minute))
	api.HandleFunc("POST /api/v1/machines/{mid}/git/push", h.proxyBodyFor("git.push", 3*time.Minute))
	api.HandleFunc("POST /api/v1/machines/{mid}/git/switch", h.proxyBody("git.switch"))
	api.HandleFunc("POST /api/v1/machines/{mid}/git/stash", h.proxyBody("git.stash"))
	api.HandleFunc("POST /api/v1/machines/{mid}/git/pr", h.proxyBodyFor("git.pr_create", 2*time.Minute))
	api.HandleFunc("POST /api/v1/machines/{mid}/git/pr/merge", h.proxyBodyFor("git.pr_merge", 3*time.Minute))
}
