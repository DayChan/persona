package extension

// Settings the user fills in for an extension that reach outside it (#5906, docs/extensions/extensions.md "Settings the user
// fills in"): an address in the settings the egress proxy lets it reach (permissions.network "config:<field>"), a
// secret shown by its last four characters once set (secrets[].hint "last4"), and a check by the extension of what is
// about to be saved (settings_check).

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/nwverse/persona/server/internal/extension/egress"
)

// ConfigNetworkPrefix starts a permissions.network entry that names a configuration field instead of a host:
// "config:<field>" lets the extension reach the https address the user wrote there, "config:<field>:http" an http one
// too.
const ConfigNetworkPrefix = "config:"

// HintLast4 is secrets[].hint for a secret shown by its last four characters once set.
const HintLast4 = "last4"

// hintMinLen: a shorter secret is not shown by its end (four characters of a short one say too much of it).
const hintMinLen = 12

// ConfiguredNetwork is one "config:<field>" entry of permissions.network.
type ConfiguredNetwork struct {
	Field string
	HTTP  bool // http allowed too, as the manifest says (":http")
}

// ParseConfigNetwork reads a permissions.network entry; ok false for a host name.
func ParseConfigNetwork(entry string) (ConfiguredNetwork, bool) {
	rest, ok := strings.CutPrefix(entry, ConfigNetworkPrefix)
	if !ok {
		return ConfiguredNetwork{}, false
	}
	field, http := strings.CutSuffix(rest, ":http")
	return ConfiguredNetwork{Field: field, HTTP: http}, true
}

// ConfiguredNetworks are permissions.network's "config:" entries.
func (p Permissions) ConfiguredNetworks() []ConfiguredNetwork {
	var out []ConfiguredNetwork
	for _, h := range p.Network {
		if c, ok := ParseConfigNetwork(h); ok {
			out = append(out, c)
		}
	}
	return out
}

// hostNames are permissions.network's host names, without the "config:" entries.
func (p Permissions) hostNames() []string {
	var out []string
	for _, h := range p.normalized().Network {
		if _, ok := ParseConfigNetwork(h); !ok {
			out = append(out, h)
		}
	}
	return out
}

// checkConfigured holds m's settings-filled parts to the schema: a "config:" entry names a string field of format uri
// with no default (a default would let it reach an address the user never wrote), a hint is one this hub knows.
func (m *Manifest) checkConfigured() []error {
	var errs []error
	cs := m.Permissions.ConfiguredNetworks()
	if len(cs) > 0 {
		sc, err := ParseSchema(m.Configuration)
		for _, c := range cs {
			var f *Field
			if err == nil && sc != nil {
				for i := range sc.Fields {
					if sc.Fields[i].Name == c.Field {
						f = &sc.Fields[i]
					}
				}
			}
			switch {
			case c.Field == "" || strings.Contains(c.Field, ":"):
				errs = append(errs, fmt.Errorf("permissions.network: %q: config:<field> or config:<field>:http", ConfigNetworkPrefix+c.Field))
			case f == nil:
				errs = append(errs, fmt.Errorf("permissions.network: configuration has no field %q", c.Field))
			case f.Type != "string" || f.Format != "uri":
				errs = append(errs, fmt.Errorf("permissions.network: configuration.%s: the address it reaches is a string of format uri", c.Field))
			case f.Default != nil:
				errs = append(errs, fmt.Errorf("permissions.network: configuration.%s: the address it reaches has a default, but only the user writes it", c.Field))
			}
		}
	}
	for _, s := range m.Secrets {
		if s.Hint != "" && s.Hint != HintLast4 {
			errs = append(errs, fmt.Errorf("secrets.%s.hint: %q (only %q)", s.Name, s.Hint, HintLast4))
		}
	}
	return errs
}

// isLoopbackHost reports whether h is a loopback host on this machine (127.0.0.1, localhost or ::1).
func isLoopbackHost(h string) bool {
	switch strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(h), "[]"), ".")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// ConfiguredTarget is the host:port a "config:" entry lets through, from the field's value now ("" when it has none
// that may be reached: unset, not a URL, http where only https is allowed, user info in it). The value is the user's:
// only the settings pages and the hub write an extension's configuration, never the extension itself.
func ConfiguredTarget(c ConfiguredNetwork, config json.RawMessage) (egress.Target, string) {
	var values map[string]any
	_ = json.Unmarshal(config, &values)
	raw, _ := values[c.Field].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return egress.Target{}, ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Opaque != "" {
		return egress.Target{}, "not a URL"
	}
	host := strings.ToLower(strings.TrimSuffix(strings.Trim(u.Hostname(), "[]"), "."))
	if host == "" || net.ParseIP(host) == nil && !hostPattern.MatchString(host) {
		return egress.Target{}, "not a host"
	}
	port := u.Port()
	switch strings.ToLower(u.Scheme) {
	case "https":
		if port == "" {
			port = "443"
		}
	case "http":
		if !c.HTTP && !isLoopbackHost(host) {
			return egress.Target{}, "http (only https)"
		}
		if port == "" {
			port = "80"
		}
	default:
		return egress.Target{}, "not http(s)"
	}
	return egress.Target{Host: host, Port: port, HTTP: strings.EqualFold(u.Scheme, "http")}, ""
}

// ConfiguredTargets are the host:ports p's "config:" entries let through with configuration config now.
func ConfiguredTargets(m *Manifest, config json.RawMessage) []egress.Target {
	var out []egress.Target
	for _, c := range m.Permissions.ConfiguredNetworks() {
		if t, _ := ConfiguredTarget(c, config); t.Host != "" {
			out = append(out, t)
		}
	}
	return out
}

// SecretHint is what a set secret shows instead of 已设置 alone: its last four characters, when its spec asks and it
// is long enough ("" else).
func SecretHint(s SecretSpec, value string) string {
	if s.Hint != HintLast4 || len([]rune(value)) < hintMinLen {
		return ""
	}
	r := []rune(value)
	return string(r[len(r)-4:])
}
