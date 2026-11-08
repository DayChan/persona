package extension

import (
	"github.com/nwverse/persona/server/internal/i18n"
	"net"
	"net/url"
	"path"
	"strings"
	"time"
)

const mirrorFetchFor = 8 * time.Second

// MarketMirrors belongs to one extension manager. Neither field changes a source
// spec, package digest, credential binding, or the official catalog signing key.
type MarketMirrors struct {
	RegistryMirror string `json:"registry_mirror"`
	CatalogMirror  string `json:"catalog_mirror"`
}

func ValidateMarketMirrors(s MarketMirrors) error {
	for _, raw := range []string{s.RegistryMirror, s.CatalogMirror} {
		if raw == "" {
			continue
		}
		u, err := url.Parse(raw)
		if err != nil || len(raw) > 2048 || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return i18n.Err("extension-market_security.mirror")
		}
		ip := net.ParseIP(u.Hostname())
		local := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
		if u.Scheme != "https" && !(u.Scheme == "http" && local) {
			return i18n.Err("extension-market_security.mirror")
		}
		if strings.Contains(u.EscapedPath(), "%") || (u.Path != "" && u.Path != "/" && path.Clean(u.Path) != strings.TrimSuffix(u.Path, "/")) {
			return i18n.Err("extension-market_security.mirror")
		}
	}
	if s.CatalogMirror != "" {
		u, _ := url.Parse(s.CatalogMirror)
		if !strings.HasSuffix(u.Path, ".json") {
			return i18n.Err("extension-market_security.catalog")
		}
	}
	return nil
}
