package extmgr

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

// marketRoutes are the markets' routes for the apps (a device with full rights).
func (h *Manager) marketRoutes(api *http.ServeMux) {
	h.marketRoutesAt(api, "/api/v1/extensions/-", func(w http.ResponseWriter, r *http.Request) bool {
		if principalOf(r.Context()).Role != roleFull {
			writeErr(w, http.StatusForbidden, errors.New("this device can only read and answer"))
			return false
		}
		return true
	})
}

// marketRoutesAt registers the markets' routes under base (the apps' API, or the admin UI's behind its session).
func (h *Manager) marketRoutesAt(api *http.ServeMux, base string, allow func(w http.ResponseWriter, r *http.Request) bool) {
	h.marketSettingsRoutes(api, base, allow)
	gate := func(f http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !allow(w, r) {
				return
			}
			if err := h.marketsLoad(r.Context()); err != nil {
				writeErrCode(w, http.StatusServiceUnavailable, "no_markets", err)
				return
			}
			f(w, r)
		}
	}
	fail := func(w http.ResponseWriter, err error, code string) {
		if errors.Is(err, errNoMarket) {
			writeErrCode(w, http.StatusNotFound, "not_found", err)
			return
		}
		writeErrCode(w, http.StatusBadRequest, code, err)
	}
	api.HandleFunc("GET "+base+"/markets", gate(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"markets": marketsOut(r, h.marketViews())})
	}))
	api.HandleFunc("POST "+base+"/markets", gate(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec  string `json:"spec"`
			Token string `json:"token,omitempty"`
			// TokenFrom: use the token of this registered market (one account's token reading several private
			// repositories), never seen by the caller.
			TokenFrom string `json:"token_from,omitempty"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if in.TokenFrom != "" {
			if in.Token != "" {
				fail(w, i18n.Err("hub-extension_market.token_and_token_from"), "market_failed")
				return
			}
			target, err := extension.ParseMarket(in.Spec)
			if err != nil {
				fail(w, err, "market_failed")
				return
			}
			tok, err := h.marketTokenFor(in.TokenFrom, target)
			if err != nil {
				fail(w, err, "market_failed")
				return
			}
			in.Token = tok
		}
		v, err := h.marketAdd(r.Context(), in.Spec, in.Token)
		if err != nil {
			fail(w, err, "market_failed")
			return
		}
		writeJSON(w, marketOut(r, v))
	}))
	api.HandleFunc("DELETE "+base+"/markets/{mid}", gate(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("mid")
		ms := h.marketsState()
		ms.mu.Lock()
		s, ok := ms.byID[id]
		ms.mu.Unlock()
		if !ok {
			fail(w, errNoMarket, "")
			return
		}
		if mk, err := h.market(s.rec); err == nil {
			mk.RemoveCheckout()
		}
		if err := h.marketStore().DeleteExtensionMarket(r.Context(), id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		ms.mu.Lock()
		delete(ms.byID, id)
		ms.mu.Unlock()
		writeJSON(w, map[string]any{"ok": true})
	}))
	api.HandleFunc("PUT "+base+"/markets/{mid}", gate(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Spec string `json:"spec"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		v, err := h.marketSetSpec(r.Context(), r.PathValue("mid"), in.Spec)
		if err != nil {
			fail(w, err, "market_failed")
			return
		}
		writeJSON(w, marketOut(r, v))
	}))
	api.HandleFunc("POST "+base+"/markets/{mid}/refresh", gate(func(w http.ResponseWriter, r *http.Request) {
		v, err := h.marketRead(r.Context(), r.PathValue("mid"))
		if errors.Is(err, errMarketSignature) {
			fail(w, err, "market_signature")
			return
		}
		if err != nil {
			fail(w, err, "market_unreachable")
			return
		}
		writeJSON(w, marketOut(r, v))
	}))
	api.HandleFunc("PUT "+base+"/markets/{mid}/token", gate(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Token string `json:"token"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if len(in.Token) > extension.MarketTokenMost {
			fail(w, i18n.Err("hub-extension_market.token_too_long"), "invalid_token")
			return
		}
		id := r.PathValue("mid")
		sealed := ""
		if in.Token != "" {
			var err error
			if sealed, err = (hubSealer{h}).Seal(in.Token); err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
		}
		ms := h.marketsState()
		ms.mu.Lock()
		s := ms.byID[id]
		if s != nil {
			rec := s.rec
			rec.TokenSealed = sealed
			if err := h.marketStore().PutExtensionMarket(r.Context(), rec); err != nil {
				ms.mu.Unlock()
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			s.rec = rec
		}
		ms.mu.Unlock()
		if s == nil {
			fail(w, errNoMarket, "")
			return
		}
		v, err := h.marketRead(r.Context(), id)
		if err != nil {
			fail(w, err, "market_unreachable")
			return
		}
		writeJSON(w, marketOut(r, v))
	}))
	api.HandleFunc("GET "+base+"/catalog", gate(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("refresh") == "1" {
			var wg sync.WaitGroup
			for _, v := range h.marketViews() {
				wg.Add(1)
				go func() { defer wg.Done(); _, _ = h.marketRead(r.Context(), v.ID) }()
			}
			wg.Wait()
		} else {
			h.refreshStale()
		}
		writeJSON(w, map[string]any{"markets": marketsOut(r, h.marketViews()), extKey(r, "extensions", "plugins"): h.catalogIn(h.requestLang(r), r.URL.Query().Get("q"), r.URL.Query().Get("market")),
			"platform": extension.Platform()})
	}))
	api.HandleFunc("POST "+base+"/catalog/preview", gate(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Market    string `json:"market"`
			Extension string `json:"extension"`
			Legacy    string `json:"plugin"`
			// Async: answer at once with the operation, and preview in the background (#3277).
			Async bool `json:"async,omitempty"`
		}
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if in.Extension == "" {
			in.Extension = in.Legacy
		}
		rec, e, err := h.marketEntry(in.Market, in.Extension)
		if err != nil {
			fail(w, err, "not_in_market")
			return
		}
		mk, err := h.market(rec)
		if err != nil {
			fail(w, err, "market_failed")
			return
		}
		upgrade := ""
		if cur := h.extensions.reg.Get(in.Extension); cur != nil && cur.Record.Source == extension.SourceInstalled {
			upgrade = cur.Record.ID
		} else if e.Replaces != "" {
			if old := h.extensions.reg.GetExact(e.Replaces); old != nil {
				if old.Record.Source != extension.SourceInstalled || old.Record.Origin != "market:"+rec.ID+"/"+e.Replaces || h.extensions.reg.Get(e.Parsed.ID) != nil {
					fail(w, i18n.Err("extension-identity.invalid"), "identity_conflict")
					return
				}
				upgrade = old.Record.ID
			}
		}
		kind := opInstall
		if upgrade != "" {
			kind = opUpgrade
		}
		job := previewJob{kind: kind, id: in.Extension, ofMarket: true, actor: opActor(r, "personad extension"), lang: h.requestLang(r),
			run: func(ctx context.Context) (*extension.Preview, error) {
				pv, err := h.extensions.reg.PreviewMarket(ctx, mk, rec.ID, e, upgrade)
				if err != nil {
					return nil, err
				}
				// its missing dependencies, a pack's members, side by side (extension_model.go)
				h.resolveBundle(extension.WithoutProgress(ctx), pv, rec.ID)
				return pv, nil
			}}
		op, pv, err := h.launch(r.Context(), job, nil, in.Async)
		switch {
		case in.Async:
			h.writeOp(w, op)
		case err != nil:
			writeErrCode(w, http.StatusBadRequest, "invalid_plugin", err)
		default:
			writeJSON(w, withOperation(pv.In(h.requestLang(r)), op))
		}
	}))
}
