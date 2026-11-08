package extmgr

import (
	"context"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

const marketSettingsKey = "extension_market_settings"

// Read directly from Postgres: failed reads/writes remain retryable and opening
// settings does not initialize markets or contact any catalog.
func (h *Manager) marketSettings(ctx context.Context) (extension.MarketMirrors, error) {
	var out extension.MarketMirrors
	if h.store == nil {
		return out, i18n.Err("extension-market_security.no_store")
	}
	_, err := h.store.Setting(ctx, marketSettingsKey, &out)
	if err == nil {
		err = extension.ValidateMarketMirrors(out)
	}
	return out, err
}

func (h *Manager) configureMarket(m *extension.Market) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := h.marketSettings(ctx)
	if err != nil {
		return err
	}
	m.RegistryMirror, m.CatalogMirror = s.RegistryMirror, s.CatalogMirror
	return nil
}

func (h *Manager) marketSettingsRoutes(api *http.ServeMux, base string, allow func(http.ResponseWriter, *http.Request) bool) {
	api.HandleFunc("GET "+base+"/market-settings", func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r) {
			return
		}
		out, err := h.marketSettings(r.Context())
		if err != nil {
			writeErrCode(w, http.StatusServiceUnavailable, "market_settings_failed", i18n.Err("hub-extension_market.settings_load_failed"))
			return
		}
		writeJSON(w, out)
	})
	api.HandleFunc("PUT "+base+"/market-settings", func(w http.ResponseWriter, r *http.Request) {
		if !allow(w, r) {
			return
		}
		var in extension.MarketMirrors
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if err := extension.ValidateMarketMirrors(in); err != nil {
			writeErrCode(w, http.StatusBadRequest, "invalid_market_settings", i18n.Err("hub-extension_market.settings_invalid"))
			return
		}
		if h.store == nil || h.store.PutSetting(r.Context(), marketSettingsKey, in) != nil {
			writeErrCode(w, http.StatusServiceUnavailable, "market_settings_failed", i18n.Err("hub-extension_market.settings_save_failed"))
			return
		}
		writeJSON(w, in)
	})
}
