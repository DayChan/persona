package devices

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/usagesource"
)

// The admin UI's usage sources page (#2595, docs/server/usage-windows.md "Account-pool gateways", docs/operations/admin.md): add, edit and remove the
// account-pool gateways whose usage windows join the sidebar's, and try one with a single read. Moved from the hub as
// it was: the hub relays the routes (devicewire.AdminUsageRoutes) from its admin UI only, so only a signed-in admin
// session gets here (every /api/v1/admin/ route; a device token, even a full one, does not open them), and says so
// in assistantwire.AdminHeader; a request without it is refused here too. Nothing answers with a key: a source says
// only whether it has one. The keys live in the manager's manager.json (0600, docs/operations/config.md "manager.json"), written
// in one step under a lock on its directory (managerconf.Edit), and a change applies at once.

const usageTestMost = 25 * time.Second

var errAdminOnly = errors.New("the usage sources page is the admin UI's")

type adminUsageSource struct {
	ID      string        `json:"id"`
	Name    string        `json:"name"`
	Type    string        `json:"type"`
	URL     string        `json:"url"`
	HasKey  bool          `json:"has_key"`
	Enabled bool          `json:"enabled"`
	Status  gatewayStatus `json:"status"`
}

type adminUsageView struct {
	Enabled     bool               `json:"enabled"`
	PollMinutes int                `json:"poll_minutes"` // in effect
	Sources     []adminUsageSource `json:"sources"`
	Limit       int                `json:"limit"`
}

// usageConfigNow is manager.json's usage as it is on disk now; the running config follows it (a hand edit is taken up the
// next time this page reads).
func (d *Devices) usageConfigNow() UsageConfig {
	disk, err := d.diskUsage()
	if err != nil {
		return d.usageGW.config()
	}
	if !reflect.DeepEqual(disk, d.usageGW.config()) {
		d.usageGW.setConfig(disk)
		d.usageWin.invalidate()
	}
	return disk
}

// diskUsage is manager.json's usage section as it is on disk now.
func (d *Devices) diskUsage() (UsageConfig, error) {
	var cfg struct {
		Usage UsageConfig `json:"usage"`
	}
	b, err := os.ReadFile(d.opts.ConfigFile)
	if err != nil { // none (or unreadable): the running config stays, as on the hub
		return cfg.Usage, err
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return cfg.Usage, err
	}
	return cfg.Usage, nil
}

func (d *Devices) adminUsageView() adminUsageView {
	cfg := d.usageConfigNow()
	v := adminUsageView{Enabled: cfg.on(), PollMinutes: int(cfg.interval() / time.Minute), Sources: []adminUsageSource{}, Limit: usageSourcesMost}
	for _, s := range cfg.Sources {
		v.Sources = append(v.Sources, adminUsageSource{ID: s.ID, Name: s.Name, Type: s.Type, URL: s.URL, HasKey: s.Key != "", Enabled: s.on(), Status: d.usageGW.status(s.ID)})
	}
	return v
}

// editUsage changes manager.json's usage and applies the result.
func (d *Devices) editUsage(edit func(u *managerconf.Object, list []json.RawMessage) (*managerconf.Object, []json.RawMessage, error)) error {
	err := managerconf.Edit(d.opts.ConfigFile, func(o *managerconf.Object) error {
		u, err := o.Object("usage")
		if err != nil {
			return err
		}
		var list []json.RawMessage
		if raw, ok := u.Get("sources"); ok && strings.TrimSpace(string(raw)) != "null" {
			if err := json.Unmarshal(raw, &list); err != nil {
				return err
			}
		}
		u, list, err = edit(u, list)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			u.Del("sources")
		} else if err := u.Set("sources", list); err != nil {
			return err
		}
		o.SetObject("usage", u)
		return nil
	})
	if err != nil {
		return err
	}
	if disk, err := d.diskUsage(); err == nil {
		d.usageGW.setConfig(disk)
		d.usageWin.invalidate()
	}
	return nil
}

func usageEntryID(raw json.RawMessage) string {
	var e struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(raw, &e)
	return e.ID
}

// usageSourceInput is what the page sends for a source; a nil field is left as it is.
type usageSourceInput struct {
	Name     *string `json:"name"`
	Type     *string `json:"type"`
	URL      *string `json:"url"`
	Key      *string `json:"key"`
	ClearKey bool    `json:"clear_key"`
	Enabled  *bool   `json:"enabled"`
}

// validUsageKey: a bearer token is one line of printable characters.
func validUsageKey(k string) bool {
	if len(k) > 512 {
		return false
	}
	for _, r := range k {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validUsageName(n string) bool {
	return n != "" && len([]rune(n)) <= 40 && !strings.ContainsAny(n, "\r\n\t")
}

// applyUsageInput puts in over s (a new source starts empty) and checks the result; the code is the one for the page.
// The key goes with its address: changing where a source points means typing its key again, so that a saved key
// cannot be sent somewhere else without being known. Only a saved key is held back that way (a source without one
// moves freely, #1844), and an address counts as moved only when its normalized form changes: a hand-written
// "https://pool.example.com/" in manager.json is the same address as the "https://pool.example.com" the page sends,
// and a request that does not touch the address (the enable switch) never moves it.
func applyUsageInput(s UsageSourceConfig, in usageSourceInput, isNew bool) (UsageSourceConfig, string, error) {
	oldURL, oldType := sameUsageURL(s.URL), s.Type
	if in.Type != nil {
		s.Type = strings.TrimSpace(*in.Type)
	}
	if s.Type != usagesource.TypeRelay && s.Type != usagesource.TypeGeneric {
		return s, "invalid_type", i18n.Err("hub-admin_usage.invalid_type")
	}
	if in.URL != nil {
		s.URL = strings.TrimSpace(*in.URL)
	}
	norm, err := usagesource.NormalizeURL(s.URL)
	if err != nil {
		return s, "invalid_url", i18n.Err("hub-admin_usage.invalid_url")
	}
	s.URL = norm
	if in.Name != nil {
		s.Name = strings.TrimSpace(*in.Name)
	}
	if s.Name == "" && s.Type == usagesource.TypeRelay {
		s.Name = "Relay"
	}
	if !validUsageName(s.Name) {
		return s, "invalid_name", i18n.Err("hub-admin_usage.invalid_name")
	}
	moved := !isNew && (s.URL != oldURL || s.Type != oldType)
	switch {
	case in.ClearKey:
		s.Key = ""
	case in.Key != nil && strings.TrimSpace(*in.Key) != "":
		k := strings.TrimSpace(*in.Key)
		if !validUsageKey(k) {
			return s, "invalid_key", i18n.Err("hub-admin_usage.invalid_key")
		}
		s.Key = k
	case moved && s.Key != "":
		s.Key = ""
		if oldURL != "" {
			return s, "key_required", i18n.Err("hub-admin_usage.key_required")
		}
	}
	if s.Key == "" && s.Type == usagesource.TypeRelay {
		return s, "key_required", i18n.Err("hub-admin_usage.key_required")
	}
	if in.Enabled != nil {
		on := *in.Enabled
		s.Enabled = &on
	}
	return s, "", nil
}

// sameUsageURL is a saved address as the page compares it: normalized, or as written when it does not parse (it then
// differs from anything the page can send).
func sameUsageURL(raw string) string {
	if norm, err := usagesource.NormalizeURL(raw); err == nil {
		return norm
	}
	return raw
}

var usageTesting atomic.Bool

// adminUsageRoutes are the page's routes (devicewire.AdminUsageRoutes) on mux, for the hub's admin UI only.
func (d *Devices) adminUsageRoutes(mux *http.ServeMux) {
	handle := func(pattern string, fn http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(assistantwire.AdminHeader) != "1" { // not from the hub's admin UI: not this page's
				writeErrCode(w, http.StatusForbidden, "login_required", errAdminOnly)
				return
			}
			fn(w, r)
		})
	}
	handle("GET /api/v1/admin/usage-sources", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, d.adminUsageView())
	})
	// The two settings of all of them: the off switch and how often each is asked.
	handle("PUT /api/v1/admin/usage-sources", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled     *bool `json:"enabled"`
			PollMinutes *int  `json:"poll_minutes"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		if in.PollMinutes != nil && (*in.PollMinutes < 1 || *in.PollMinutes > usagePollMost) {
			writeErrCode(w, http.StatusBadRequest, "invalid_poll", i18n.Err("hub-admin_usage.invalid_poll"))
			return
		}
		err := d.editUsage(func(u *managerconf.Object, list []json.RawMessage) (*managerconf.Object, []json.RawMessage, error) {
			if in.Enabled != nil {
				if *in.Enabled {
					u.Del("enabled") // on is the default
				} else if err := u.Set("enabled", false); err != nil {
					return u, list, err
				}
			}
			if in.PollMinutes != nil {
				if *in.PollMinutes == usagePollDefault {
					u.Del("poll_minutes")
				} else if err := u.Set("poll_minutes", *in.PollMinutes); err != nil {
					return u, list, err
				}
			}
			return u, list, nil
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		d.logf("admin: usage sources settings changed")
		writeJSON(w, d.adminUsageView())
	})
	handle("POST /api/v1/admin/usage-sources", func(w http.ResponseWriter, r *http.Request) {
		var in usageSourceInput
		if !readAdminJSON(w, r, &in) {
			return
		}
		s, code, err := applyUsageInput(UsageSourceConfig{}, in, true)
		if err != nil {
			writeErrCode(w, http.StatusBadRequest, code, err)
			return
		}
		s.ID = "src-" + randomHex(4)
		tooMany := false
		err = d.editUsage(func(u *managerconf.Object, list []json.RawMessage) (*managerconf.Object, []json.RawMessage, error) {
			if len(list) >= usageSourcesMost {
				tooMany = true
				return u, list, nil
			}
			entry, err := managerconf.MarshalNoEscape(s)
			if err != nil {
				return u, list, err
			}
			return u, append(list, entry), nil
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if tooMany {
			writeErrCode(w, http.StatusConflict, "too_many", i18n.Err("hub-admin_usage.too_many", i18n.Args{"n": usageSourcesMost}))
			return
		}
		d.logf("admin: usage source %q (%s) added", s.ID, s.Type)
		d.respondUsageSource(w, s.ID)
	})
	handle("PUT /api/v1/admin/usage-sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var in usageSourceInput
		if !readAdminJSON(w, r, &in) {
			return
		}
		found, bad, badCode := false, error(nil), ""
		err := d.editUsage(func(u *managerconf.Object, list []json.RawMessage) (*managerconf.Object, []json.RawMessage, error) {
			for i, raw := range list {
				if usageEntryID(raw) != id {
					continue
				}
				found = true
				var cur UsageSourceConfig
				if err := json.Unmarshal(raw, &cur); err != nil {
					return u, list, err
				}
				next, code, err := applyUsageInput(cur, in, false)
				if err != nil {
					bad, badCode = err, code
					return u, list, nil
				}
				// Only the fields this page owns change; the rest of the entry stays as it was written.
				entry, err := managerconf.ParseObject(raw)
				if err != nil {
					return u, list, err
				}
				for k, v := range map[string]any{"name": next.Name, "type": next.Type, "url": next.URL} {
					if err := entry.Set(k, v); err != nil {
						return u, list, err
					}
				}
				if next.Key == "" {
					entry.Del("key")
				} else if err := entry.Set("key", next.Key); err != nil {
					return u, list, err
				}
				if next.Enabled == nil || *next.Enabled {
					entry.Del("enabled")
				} else if err := entry.Set("enabled", false); err != nil {
					return u, list, err
				}
				list = append([]json.RawMessage(nil), list...)
				list[i] = entry.Marshal()
				return u, list, nil
			}
			return u, list, nil
		})
		switch {
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err)
		case !found:
			writeErrCode(w, http.StatusNotFound, "not_found", i18n.Err("hub-admin_usage.not_found"))
		case bad != nil:
			writeErrCode(w, http.StatusBadRequest, badCode, bad)
		default:
			d.logf("admin: usage source %q changed", id)
			d.respondUsageSource(w, id)
		}
	})
	handle("DELETE /api/v1/admin/usage-sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		found := false
		err := d.editUsage(func(u *managerconf.Object, list []json.RawMessage) (*managerconf.Object, []json.RawMessage, error) {
			kept := make([]json.RawMessage, 0, len(list))
			for _, raw := range list {
				if usageEntryID(raw) == id {
					found = true
					continue
				}
				kept = append(kept, raw)
			}
			return u, kept, nil
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if !found {
			writeErrCode(w, http.StatusNotFound, "not_found", i18n.Err("hub-admin_usage.not_found"))
			return
		}
		d.logf("admin: usage source %q removed", id)
		writeJSON(w, map[string]any{"ok": true})
	})
	// One read-only try of the address and key on the form (or of a saved source, with its key when the address is the
	// saved one): how many accounts and windows it saw. Nothing is saved.
	handle("POST /api/v1/admin/usage-sources/test", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID       string `json:"id"`
			Type     string `json:"type"`
			URL      string `json:"url"`
			Key      string `json:"key"`
			ClearKey bool   `json:"clear_key"`
		}
		if !readAdminJSON(w, r, &in) {
			return
		}
		cfg := usagesource.Config{Type: strings.TrimSpace(in.Type), URL: in.URL, Key: strings.TrimSpace(in.Key)}
		if cfg.Type != usagesource.TypeRelay && cfg.Type != usagesource.TypeGeneric {
			writeErrCode(w, http.StatusBadRequest, "invalid_type", i18n.Err("hub-admin_usage.invalid_type"))
			return
		}
		norm, err := usagesource.NormalizeURL(cfg.URL)
		if err != nil {
			writeErrCode(w, http.StatusBadRequest, "invalid_url", i18n.Err("hub-admin_usage.invalid_url"))
			return
		}
		cfg.URL = norm
		if in.ClearKey {
			cfg.Key = ""
		} else if cfg.Key == "" && in.ID != "" { // the saved key, only to the saved address
			for _, s := range d.usageConfigNow().Sources {
				if s.ID == in.ID && sameUsageURL(s.URL) == norm && s.Type == cfg.Type {
					cfg.Key = s.Key
				}
			}
		}
		if cfg.Key != "" && !validUsageKey(cfg.Key) {
			writeErrCode(w, http.StatusBadRequest, "invalid_key", i18n.Err("hub-admin_usage.invalid_key"))
			return
		}
		if !usageTesting.CompareAndSwap(false, true) {
			writeErrCode(w, http.StatusTooManyRequests, "busy", i18n.Err("hub-admin_usage.busy"))
			return
		}
		defer usageTesting.Store(false)
		ctx, cancel := context.WithTimeout(r.Context(), usageTestMost)
		defer cancel()
		fetch := d.usageGW.fetch
		if fetch == nil {
			fetch = usagesource.Fetch
		}
		res, err := fetch(ctx, cfg, time.Now())
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "code": usagesource.CodeOf(err)})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "accounts": res.Seen.Accounts, "windows": res.Seen.Windows, "listed": res.Seen.Listed, "providers": res.Seen.Providers})
	})
}

func (d *Devices) respondUsageSource(w http.ResponseWriter, id string) {
	v := d.adminUsageView()
	for _, s := range v.Sources {
		if s.ID == id {
			writeJSON(w, map[string]any{"source": s})
			return
		}
	}
	writeJSON(w, map[string]any{"source": nil})
}
