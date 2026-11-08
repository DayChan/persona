package link

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// Pinned is an HTTP/1.1 client that only talks to the hub whose TLS key hashes to fp (Pin): what a manager on another
// computer dials the hub's public port with (docs/assistant/managers.md).
func Pinned(fp string) *http.Client { return pinned(fp, nil) }

// pinned is an HTTP/1.1 client that only talks to the hub whose TLS key hashes to fp. onConn sees each new
// connection (the link keeps its local address, to tell when the network under it is gone).
func pinned(fp string, onConn func(net.Conn)) *http.Client {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				raw, err := d.DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				host, _, _ := net.SplitHostPort(addr)
				cfg := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
					InsecureSkipVerify: true, // the pin below is the check
					VerifyConnection: func(cs tls.ConnectionState) error {
						if len(cs.PeerCertificates) == 0 {
							return errPin
						}
						sum := sha256.Sum256(cs.PeerCertificates[0].RawSubjectPublicKeyInfo)
						if base64.RawURLEncoding.EncodeToString(sum[:]) != fp {
							return errPin
						}
						return nil
					}}
				if net.ParseIP(host) == nil {
					cfg.ServerName = host
				}
				c := tls.Client(raw, cfg)
				if err := c.HandshakeContext(ctx); err != nil {
					raw.Close()
					return nil, err
				}
				if onConn != nil {
					onConn(raw)
				}
				return c, nil
			},
			ResponseHeaderTimeout: dialTimeout,
			MaxIdleConns:          1,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// systemRoots replaces the system's CAs for systemTrusted (UseRootsForTesting); nil means the system's.
var systemRoots atomic.Pointer[x509.CertPool]

// UseRootsForTesting makes the system-trust client trust pool instead of the system's CAs, until restore is called.
// For tests (a reverse proxy with a test CA), never the keychain.
func UseRootsForTesting(pool *x509.CertPool) (restore func()) {
	old := systemRoots.Swap(pool)
	return func() { systemRoots.Store(old) }
}

// systemTrusted is the HTTP/1.1 client for a hub without a pin: it sits behind an HTTPS reverse proxy whose
// certificate comes from a public CA (docs/operations/link.md "Hub behind a reverse proxy"). The certificate must chain to the system's
// CAs and name the host dialed; nothing else is accepted. onConn is pinned's.
func systemTrusted(onConn func(net.Conn)) *http.Client {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: nil,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				raw, err := d.DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				host, _, _ := net.SplitHostPort(addr)
				c := tls.Client(raw, &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"},
					ServerName: host, RootCAs: systemRoots.Load()})
				if err := c.HandshakeContext(ctx); err != nil {
					raw.Close()
					return nil, err
				}
				if onConn != nil {
					onConn(raw)
				}
				return c, nil
			},
			ResponseHeaderTimeout: dialTimeout,
			MaxIdleConns:          1,
			IdleConnTimeout:       30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// hubClient is how c's hub is checked: pinned to its key when the pairing gave one (never anything else then), else
// with the system's CAs.
func hubClient(c *Config, onConn func(net.Conn)) *http.Client {
	if c.FP != "" {
		return pinned(c.FP, onConn)
	}
	return systemTrusted(onConn)
}

// trustOf is Status.Trust for c.
func trustOf(c *Config) string {
	if c.FP == "" {
		return TrustSystem
	}
	return ""
}

// wsURL joins path under base: base keeps the path a reverse proxy puts the hub under.
func wsURL(base, path string) string {
	return "wss" + strings.TrimPrefix(base, "https") + path
}

func bearer(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }
