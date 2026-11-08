package hub

import (
	"net/http"
)

// agentSettingsRoutes read and write each agent's own settings on a machine, through its node.
func (h *Hub) agentSettingsRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/machines/{mid}/claude-settings", h.proxy("claude.settings", func(r *http.Request) any {
		return map[string]any{"locale": h.requestLang(r)}
	}))
	api.HandleFunc("PUT /api/v1/machines/{mid}/claude-settings", h.proxyBody("claude.settings.set"))
	api.HandleFunc("GET /api/v1/machines/{mid}/codex-settings", h.proxy("codex.settings", nil))
	api.HandleFunc("PUT /api/v1/machines/{mid}/codex-settings", h.proxyBody("codex.settings.set"))
	api.HandleFunc("GET /api/v1/machines/{mid}/agy-settings", h.proxy("agy.settings", nil))
	api.HandleFunc("PUT /api/v1/machines/{mid}/agy-settings", h.proxyBody("agy.settings.set"))
	api.HandleFunc("GET /api/v1/machines/{mid}/omp-settings", h.proxy("omp.settings", nil))
	api.HandleFunc("PUT /api/v1/machines/{mid}/omp-settings", h.proxyBody("omp.settings.set"))
}
