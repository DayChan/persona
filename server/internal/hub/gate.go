package hub

import (
	"net/http"
	"time"
)

// gate runs before authentication on every /api/ request: requests are refused here for how they arrive (plaintext
// from the internet, too many failed tokens) rather than for who sent them (docs/archive/redesign/hub-changes.md 1.4).
func (h *Hub) gate(next http.Handler) http.Handler {
	if h.cfg.Security.AllowPublicPlaintext {
		h.logf("WARNING: security.allow_public_plaintext is enabled; public API traffic can expose credentials")
	}
	h.checkPlainPeers()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil && !h.cfg.Security.AllowPublicPlaintext && h.sourceOf(r) == sourcePublic && !h.plainPeerRequest(r) {
			h.noteGatewayPlaintext(r)
			h.securityError(w, r, http.StatusForbidden, "tls_required", 0)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// noteGatewayPlaintext logs, at most every 10 minutes, that plaintext from this container's gateway was refused. The
// auth line masks the source, and the gateway is where docker-proxy hands over every connection to a port published
// on the host's loopback, an SSH tunnel's or a reverse proxy's on that host included: without a word about it, a
// tunnel that signs in fine but gets 403 tls_required on every request has no visible cause (docs/operations/docker.md
// "SSH tunnel to a hub on the same host").
func (h *Hub) noteGatewayPlaintext(r *http.Request) {
	if _, ok := forwardedOf(r); ok {
		return // a member judged it
	}
	ip := remoteIP(r)
	if !ip.IsValid() || !h.adminGateway(ip) {
		return
	}
	h.admin.mu.Lock()
	log := h.admin.apiGatewayLogged.IsZero() || time.Since(h.admin.apiGatewayLogged) > 10*time.Minute
	if log {
		h.admin.apiGatewayLogged = time.Now()
	}
	h.admin.mu.Unlock()
	if log {
		h.logf("api: plaintext from %s, this container's gateway (port mapping hides the real source), refused with "+
			"tls_required; an SSH tunnel or reverse proxy on this host needs it in security.trusted_cidrs, with the hub's "+
			"ports published on 127.0.0.1 only (docs/operations/docker.md)", ip)
	}
}
