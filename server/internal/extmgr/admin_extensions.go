package extmgr

import (
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The plugins page (docs/operations/admin.md "Extensions", docs/extensions/extensions.md "Admin UI"): what `personad plugin` does on the hub's
// machine, from a signed-in admin session (CSRF and the admin scope already checked by adminRoutes). Installing runs
// code on this machine; the admin password guards everything else here that is as strong (adding computers, issuing
// credentials), so the session may. Every change is audited as the admin UI.

// adminActor is who the plugin audit names for a change made here.
const adminActor = "admin UI"

// adminUploadPath is where the browser uploads a package: the one admin write whose body is not JSON.
const adminUploadPath = "/api/v1/admin/extensions/upload"

// legacyAdminUploadPath is adminUploadPath before #1844, where the admin UI out there still uploads.
const legacyAdminUploadPath = "/api/v1/admin/plugins/upload"

// Previews stage packages of up to extension.PackageMost bytes on disk for 10 minutes: one at a time, and at most
// extensionPreviewsPer in extensionPreviewWindow, so a stolen session cannot fill the disk.
const (
	extensionPreviewsPer   = 10
	extensionPreviewWindow = 10 * time.Minute
)

// extensionGate holds previews to one at a time and extensionPreviewsPer a window.
type extensionGate struct {
	mu     sync.Mutex
	busy   bool
	recent []time.Time
}

// begin takes the gate: end releases it. busy: another preview is running; retry > 0: too many lately.
func (g *extensionGate) begin(now time.Time) (end func(), retry time.Duration, busy bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy {
		return nil, 0, true
	}
	kept := g.recent[:0]
	for _, t := range g.recent {
		if now.Sub(t) < extensionPreviewWindow {
			kept = append(kept, t)
		}
	}
	g.recent = kept
	if len(g.recent) >= extensionPreviewsPer {
		return nil, g.recent[0].Add(extensionPreviewWindow).Sub(now), false
	}
	g.recent = append(g.recent, now)
	g.busy = true
	return func() {
		g.mu.Lock()
		g.busy = false
		g.mu.Unlock()
	}, 0, false
}

// AdminExtensions is GET /api/v1/admin/plugins.
type AdminExtensions struct {
	Extensions []any `json:"extensions"`
	CanInstall bool  `json:"can_install"`
	// UploadMost is the largest package the browser may upload, in bytes.
	UploadMost int64 `json:"upload_most"`
}

func (h *Manager) adminExtensionRoutes(mux *http.ServeMux) {
	ps := func(w http.ResponseWriter) *extensions {
		if h.extensions == nil {
			writeErrCode(w, http.StatusServiceUnavailable, "no_plugins", i18n.Err("hub-admin_extensions.no_extensions"))
		}
		return h.extensions
	}
	// one is the installed or known plugin {id}, or a 404.
	one := func(f func(w http.ResponseWriter, r *http.Request, ps *extensions, p *extension.Extension)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			s := ps(w)
			if s == nil {
				return
			}
			p := s.reg.Get(r.PathValue("id"))
			if p == nil {
				writeErrCode(w, http.StatusNotFound, "not_found", i18n.Err("hub-admin_extensions.not_found"))
				return
			}
			f(w, r, s, p)
		}
	}
	// gated runs a preview under the gate.
	gated := func(w http.ResponseWriter, r *http.Request, run func() (*extension.Preview, error)) {
		end, retry, busy := h.admin.extensions.begin(h.adminNow())
		switch {
		case busy:
			writeErrCode(w, http.StatusConflict, "busy", i18n.Err("hub-admin_extensions.busy"))
			return
		case retry > 0:
			retryAfter(w, retry)
			writeErrorBody(w, http.StatusTooManyRequests, map[string]any{
				"error": "too many previews", "code": "rate_limited", "retry_after": max(1, int((retry+time.Second-1)/time.Second)),
			})
			return
		}
		defer end()
		pv, err := run()
		if err != nil {
			writeExtensionResponseError(w, r, http.StatusBadRequest, "invalid_plugin", err)
			return
		}
		h.logf("admin: previewed plugin %s %s (%s)", pv.Manifest.ID, pv.Manifest.Version, map[bool]string{true: "upgrade", false: "install"}[pv.Upgrade])
		writeExtensionJSON(w, r, pv.In(i18n.OfWriter(w)))
	}

	// The markets, as the apps have them (the admin session is the gate).
	h.marketRoutesAt(mux, "/api/v1/admin/extensions/-", func(w http.ResponseWriter, r *http.Request) bool {
		if h.extensions == nil {
			writeErrCode(w, http.StatusServiceUnavailable, "no_plugins", errors.New("plugins are not set up"))
			return false
		}
		return true
	})
	h.legacyExtensionRoutes(mux, "/api/v1/admin/plugins", "/api/v1/admin/extensions")
	mux.HandleFunc("GET /api/v1/admin/extensions", func(w http.ResponseWriter, r *http.Request) {
		out := AdminExtensions{Extensions: []any{}, UploadMost: extension.PackageMost}
		if h.extensions != nil {
			for _, p := range h.extensions.reg.List() {
				out.Extensions = append(out.Extensions, detailOut(r, h.extensions.detail(r.Context(), p)))
			}
			out.CanInstall = h.extensions.reg.HasStore()
		}
		if legacyExtensionRequest(r) {
			writeJSON(w, legacyAdminExtensions(out))
			return
		}
		writeJSON(w, out)
	})
	mux.HandleFunc("GET /api/v1/admin/extensions/{id}", one(func(w http.ResponseWriter, r *http.Request, ps *extensions, p *extension.Extension) {
		writeJSON(w, detailOut(r, ps.detail(r.Context(), p)))
	}))
	h.adminExtensionMetricsRoutes(mux, ps, one)
	mux.HandleFunc("POST /api/v1/admin/extensions/preview", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Source  string `json:"source"`
			Upgrade string `json:"upgrade,omitempty"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		s := ps(w)
		if s == nil {
			return
		}
		gated(w, r, func() (*extension.Preview, error) {
			pv, err := s.reg.Preview(r.Context(), in.Source, in.Upgrade)
			if err == nil {
				h.resolveBundle(r.Context(), pv, "") // its missing dependencies, a pack's members (extension_model.go)
			}
			return pv, err
		})
	})
	// The package itself as the body (application/octet-stream), its file name and what it upgrades in the query.
	mux.HandleFunc("POST "+adminUploadPath, func(w http.ResponseWriter, r *http.Request) {
		s := ps(w)
		if s == nil {
			return
		}
		if r.ContentLength > extension.PackageMost {
			writeErrCode(w, http.StatusRequestEntityTooLarge, "too_large", i18n.Err("hub-admin_extensions.too_large", i18n.Args{"mb": extension.PackageMost >> 20}))
			return
		}
		q := r.URL.Query()
		if _, err := extension.UploadName(q.Get("name")); err != nil {
			writeExtensionResponseError(w, r, http.StatusBadRequest, "invalid_plugin", err)
			return
		}
		body := http.MaxBytesReader(w, r.Body, extension.PackageMost+1)
		gated(w, r, func() (*extension.Preview, error) {
			pv, err := s.reg.PreviewUpload(r.Context(), q.Get("name"), body, q.Get("upgrade"))
			if err == nil {
				h.resolveBundle(r.Context(), pv, "")
			}
			return pv, err
		})
	})
	mux.HandleFunc("POST /api/v1/admin/extensions/install", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			PreviewID string      `json:"preview_id"`
			Digest    string      `json:"permissions_digest"`
			Bundle    []bundleRef `json:"bundle,omitempty"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		s := ps(w)
		if s == nil {
			return
		}
		if in.Digest == "" {
			writeErrCode(w, http.StatusBadRequest, "confirm_required", i18n.Err("hub-admin_extensions.confirm_required"))
			return
		}
		p, err := h.installExtensionPlan(r.Context(), "", in.PreviewID, in.Digest, in.Bundle, adminActor)
		if err != nil {
			writeExtensionChangeError(w, r, http.StatusBadRequest, "install_failed", err)
			return
		}
		h.logf("admin: installed plugin %s %s", p.Record.ID, p.Record.Version)
		writeJSON(w, detailOut(r, s.detail(r.Context(), p)))
	})
	mux.HandleFunc("POST /api/v1/admin/extensions/{id}/upgrade", one(func(w http.ResponseWriter, r *http.Request, ps *extensions, p *extension.Extension) {
		var in struct {
			PreviewID string      `json:"preview_id"`
			Digest    string      `json:"permissions_digest"`
			Bundle    []bundleRef `json:"bundle,omitempty"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		np, err := h.installExtensionPlan(r.Context(), p.Record.ID, in.PreviewID, in.Digest, in.Bundle, adminActor)
		if err != nil {
			writeExtensionChangeError(w, r, http.StatusBadRequest, "upgrade_failed", err)
			return
		}
		h.logf("admin: upgraded plugin %s %s → %s", np.Record.ID, np.Record.PreviousVersion, np.Record.Version)
		writeJSON(w, detailOut(r, ps.detail(r.Context(), np)))
	}))
	mux.HandleFunc("POST /api/v1/admin/extensions/{id}/rollback", one(func(w http.ResponseWriter, r *http.Request, ps *extensions, p *extension.Extension) {
		np, err := ps.reg.Rollback(r.Context(), p.Record.ID, adminActor)
		if err != nil {
			writeExtensionResponseError(w, r, http.StatusBadRequest, "rollback_failed", err)
			return
		}
		h.extensionUpgrades().rolledBackByUser(r.Context(), np.Record.ID, p.Record.Version)
		h.logf("admin: rolled plugin %s back to %s", np.Record.ID, np.Record.Version)
		writeJSON(w, detailOut(r, ps.detail(r.Context(), np)))
	}))
	mux.HandleFunc("PUT /api/v1/admin/extensions/{id}/enabled", one(func(w http.ResponseWriter, r *http.Request, ps *extensions, p *extension.Extension) {
		var in struct {
			Enabled bool `json:"enabled"`
			Cascade bool `json:"cascade"` // switch off the extensions that depend on it too
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		if !in.Enabled && !h.takeDependents(w, r, p.Record.ID, in.Cascade) {
			return
		}
		if err := ps.reg.SetEnabled(r.Context(), p.Record.ID, in.Enabled, adminActor); err != nil {
			writeExtensionChangeError(w, r, http.StatusConflict, "read_only", err)
			return
		}
		writeJSON(w, detailOut(r, ps.detail(r.Context(), ps.reg.Get(p.Record.ID))))
	}))
	mux.HandleFunc("PUT /api/v1/admin/extensions/{id}/sandbox", func(w http.ResponseWriter, r *http.Request) {
		h.putSandbox(w, r, adminActor) // the admin session's checks are the mux's (extension_sandbox.go)
	})
	mux.HandleFunc("DELETE /api/v1/admin/extensions/{id}", one(func(w http.ResponseWriter, r *http.Request, ps *extensions, p *extension.Extension) {
		var in struct {
			KeepData bool `json:"keep_data"`
			Cascade  bool `json:"cascade"` // switch off the extensions that depend on it
			Members  bool `json:"members"` // a pack's members go with it
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		h.uninstallPack(w, r, p.Record.ID, uninstallOptions{keepData: in.KeepData, members: in.Members, cascade: in.Cascade, owner: true, failureStatus: http.StatusConflict, actor: adminActor})
	}))
}
