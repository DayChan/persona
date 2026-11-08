package egress

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"
)

const RuntimeFetchPath = "/__persona_runtime_fetch"
const RuntimeBodyMost = 8 << 20

// HEAD's entity length is metadata, not the length of the broker's POST response.
const RuntimeHeadLengthHeader = "X-Persona-Runtime-Head-Content-Length"

// runtimeFetch is a bounded bridge to the existing fixed-identity proxy policy, not
// another network policy. It is exposed only on an extension's registered unix listener.
func (p *Proxy) runtimeFetch(w http.ResponseWriter, r *http.Request, ext string) {
	// Bound broker-owned allocations globally as well as per registration.
	p.mu.Lock()
	if p.runtimeActive == nil {
		p.runtimeActive = map[string]int{}
	}
	if p.runtimeTotal >= 8 || p.runtimeActive[ext] >= 4 {
		p.mu.Unlock()
		// Rejection happens before the admitted request's body deadline. Never
		// let net/http drain an incomplete body while flushing this response.
		w.Header().Set("Connection", "close")
		http.Error(w, "fetch broker busy", http.StatusTooManyRequests)
		return
	}
	p.runtimeTotal++
	p.runtimeActive[ext]++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.runtimeTotal--
		p.runtimeActive[ext]--
		if p.runtimeActive[ext] == 0 {
			delete(p.runtimeActive, ext)
		}
		p.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()
	requestBody := r.Body
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	_ = controller.SetReadDeadline(deadline)
	_ = controller.SetWriteDeadline(deadline)
	stopClose := context.AfterFunc(ctx, func() {
		// Body.Close may wait behind a Read holding net/http's body mutex.
		// Interrupt both directions first: a raw guest may stop reading a
		// forwarded response as well as stop sending its request body.
		now := time.Now()
		_ = controller.SetReadDeadline(now)
		_ = controller.SetWriteDeadline(now)
		_ = requestBody.Close()
	})
	defer stopClose()
	r = r.WithContext(ctx)
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		URL     string            `json:"url"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 12<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		http.Error(w, "invalid fetch request", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || in.Method == "CONNECT" {
		http.Error(w, "invalid fetch target", http.StatusBadRequest)
		return
	}
	body, err := base64.StdEncoding.DecodeString(in.Body)
	if err != nil || len(body) > RuntimeBodyMost {
		http.Error(w, "fetch body too large", http.StatusRequestEntityTooLarge)
		return
	}
	out, err := http.NewRequestWithContext(r.Context(), in.Method, u.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, "invalid fetch method", http.StatusBadRequest)
		return
	}
	for k, v := range in.Headers {
		switch http.CanonicalHeaderKey(k) {
		case "Proxy-Authorization", "Proxy-Connection", "Connection", "Upgrade", "Host", "Content-Length", "Transfer-Encoding", "Trailer":
			continue
		}
		out.Header.Set(k, v)
	}
	// HTTPS is forwarded by the same policy using a TLS connection after its approved dial.
	out = out.WithContext(context.WithValue(out.Context(), runtimeFetchKey{}, true))
	p.handler(ext).ServeHTTP(&boundedResponse{ResponseWriter: w, left: RuntimeBodyMost}, out)
}

type runtimeFetchKey struct{}

type boundedResponse struct {
	http.ResponseWriter
	left int
}

func (w *boundedResponse) Write(b []byte) (int, error) {
	if len(b) > w.left {
		return 0, errors.New("runtime response exceeds 8 MiB")
	}
	n, err := w.ResponseWriter.Write(b)
	w.left -= n
	return n, err
}

// requestLifetime binds pending HTTP, tunnels and native relays to one registration.
// Cancellation on replacement, settings changes or revocation closes only that extension's I/O.
func (p *Proxy) requestLifetime(parent context.Context, ext string) (context.Context, func(), bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.rules[ext]; !ok {
		return nil, nil, false
	}
	if p.active == nil {
		p.active = map[string]map[*ioLease]struct{}{}
	}
	if p.active[ext] == nil {
		p.active[ext] = map[*ioLease]struct{}{}
	}
	if len(p.active[ext]) >= 128 {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancel(parent)
	lease := &ioLease{cancel: cancel}
	p.active[ext][lease] = struct{}{}
	var once sync.Once
	return ctx, func() { once.Do(func() { cancel(); p.mu.Lock(); delete(p.active[ext], lease); p.mu.Unlock() }) }, true
}

type ioLease struct{ cancel context.CancelFunc }

func (p *Proxy) cancelIOLocked(ext string) {
	for lease := range p.active[ext] {
		lease.cancel()
	}
	delete(p.active, ext)
}

// runtimeDial retains proxy registration policy but pins the checked DNS address
// even through an upstream CONNECT. A proxy must not resolve the hostname again.
// Every address is checked before any is dialed; dialRace then tries only those.
func (p *Proxy) runtimeDial(ctx context.Context, host, port string, configured bool) (net.Conn, error) {
	ips, err := p.resolveAll(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("egress target has no addresses")
	}
	for _, ip := range ips {
		if configured && !p.forSettings(ip, port) {
			return nil, errNotForSettings
		}
		if net.ParseIP(host) == nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() || slices.Contains(metadataIPs, ip.String())) {
			return nil, errInternal
		}
	}
	upstream := p.Upstream
	if upstream == nil {
		upstream = fromEnvironment
	}
	up, err := upstream(&url.URL{Scheme: "https", Host: net.JoinHostPort(host, port)})
	if err != nil {
		return nil, err
	}
	// Each attempt dials one checked address. Through the upstream proxy each attempt is its own CONNECT to
	// that address (the proxy never sees the name), so an address the proxy cannot reach is raced the same way.
	attempt := func(ctx context.Context, target string) (net.Conn, error) { return p.dial(ctx, "tcp", target) }
	if up != nil {
		attempt = func(ctx context.Context, target string) (net.Conn, error) { return p.connectVia(ctx, up, target) }
	}
	c, err := dialRace(ctx, ips, port, attempt)
	if err != nil {
		return nil, fmt.Errorf("egress dial failed: %w", err)
	}
	return c, nil
}

var _ io.Writer = (*boundedResponse)(nil)
