// Package netproxy parses the http:// proxies hub.json gives for outbound traffic (google.proxy, a connector's
// proxy) and builds HTTP clients that use them. Proxy addresses are deployment-specific: they live only in a hub's
// own hub.json, never in code, docs or tests.
package netproxy

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Parse reads an http://[user:pass@]host:port proxy (port 1-65535, 80 when left out); "" (or blanks) is no proxy
// (nil). Errors never show any part of user:pass. The URL's Host always has a port, so callers that dial it themselves
// (mail's CONNECT tunnels) need no default of their own.
func Parse(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || !wellFormed(u) {
		return nil, fmt.Errorf("proxy must be http://[user:pass@]host:port, got %q", redact(raw))
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "80")
	}
	return u, nil
}

// wellFormed reports whether u is http://[user:pass@]host[:port][/] with a host and a valid port.
func wellFormed(u *url.URL) bool {
	if u.Scheme != "http" || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	port := u.Port()
	if port == "" {
		return !strings.HasSuffix(u.Host, ":")
	}
	n, err := strconv.Atoi(port)
	return err == nil && n >= 1 && n <= 65535
}

// HTTPClient is a client with timeout that sends everything through proxy, or directly when proxy is nil. Direct
// means direct: the process's HTTP(S)_PROXY environment is not consulted, so only hub.json decides.
func HTTPClient(proxy *url.URL, timeout time.Duration) *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	if proxy != nil {
		t.Proxy = http.ProxyURL(proxy)
	}
	return &http.Client{Timeout: timeout, Transport: t}
}

// schemePrefix matches a URL's leading "scheme://".
var schemePrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://`)

// redact is raw for an error message, with everything that may be user:pass (up to the last '@') cut out. Neither
// url.Parse's error nor the URL it built is shown: a '/', '?' or '#' in a password ends the host early, so either can
// hold part of the password outside the userinfo.
func redact(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	return schemePrefix.FindString(raw[:at]) + "xxxxx" + raw[at:]
}
