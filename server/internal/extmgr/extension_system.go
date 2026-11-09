package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Places outside the app (docs/extensions/extensions.md "System-level places"): an extension's widgets (the ui slot widget: a home
// screen widget, a Lock Screen or watch face complication) and its shortcuts (actions Shortcuts and Siri run). The apps
// list them, draw a widget for a family (held to what the family draws, kept until it is due again) and run a shortcut;
// each device says which widgets it has, so an extension's ui.invalidate wakes the devices that show it.

// widgetPushEvery is how often one device is woken for one widget at most: iOS budgets background pushes and widget
// reloads, and a widget a minute old is fine. Lifecycle changes bypass this content refresh budget.
var widgetPushEvery = 15 * time.Minute

// deviceWidgetsKey is the setting keeping the widgets each device has.
const deviceWidgetsKey = "extension_widgets"

// deviceWidgetsMost is how many widgets one device may report.
const deviceWidgetsMost = 64

// WidgetPlace is one widget an extension draws, as the apps list it.
type WidgetPlace = wire.WidgetPlace

// ShortcutPlace is one shortcut of an extension, as the apps list it.
type ShortcutPlace = wire.ShortcutPlace

// widgets keeps what the hub knows of widgets: views drawn and not yet due, the widgets each device has, when each was
// last woken for one.
type widgets struct {
	mu         sync.Mutex
	views      map[string]widgetView          // widgetCacheKey → the view
	devices    map[string][]wire.DeviceWidget // device id → its widgets
	loaded     bool                           // devices read from the store
	woken      map[string]time.Time           // device/extension/ui → when it was last woken for it
	generation map[string]uint64              // extension → lifecycle revision, fencing renders from an old runtime
}

type widgetView struct {
	view    extension.View
	expires time.Time
}

// widgetCacheKey is a view's key: whose widget, at which family, for which context.
func widgetCacheKey(ext, ui string, p extension.UIParams) string {
	return strings.Join([]string{ext, ui, p.Size, p.TZ, p.Locale, p.Platform, strconv.Itoa(p.UIVersion)}, "\x00")
}

// widgetPlaces are the widgets of the extensions that are on and running.
func (ps *extensions) widgetPlaces() []WidgetPlace {
	out := []WidgetPlace{}
	if ps == nil {
		return out
	}
	for _, p := range ps.reg.List() {
		if !p.Record.Enabled || p.Problem != "" {
			continue
		}
		if hp := ps.get(p.Record.ID); hp == nil || hp.proc == nil {
			continue
		}
		for _, u := range p.Manifest.UIs() {
			if u.Placement != "widget" {
				continue
			}
			out = append(out, WidgetPlace{Extension: p.Record.ID, ExtensionName: p.Manifest.Name, ID: u.ID, Title: u.Title,
				Families: u.Families, RefreshMinutes: int(u.Refresh() / time.Minute), Opens: u.Opens})
		}
	}
	return out
}

// shortcutPlaces are the shortcuts of the extensions that are on and running.
func (ps *extensions) shortcutPlaces() []ShortcutPlace {
	out := []ShortcutPlace{}
	if ps == nil {
		return out
	}
	for _, p := range ps.reg.List() {
		if !p.Record.Enabled || p.Problem != "" {
			continue
		}
		if hp := ps.get(p.Record.ID); hp == nil || hp.proc == nil {
			continue
		}
		for _, s := range p.Manifest.Shortcuts() {
			out = append(out, ShortcutPlace{Extension: p.Record.ID, ExtensionName: p.Manifest.Name, ID: s.ID, Title: s.Title,
				Description: s.Description, Params: s.Params, UI: s.UI})
		}
	}
	return out
}

var (
	errNotWidget   = i18n.Err("hub-extension_system.not_widget")
	errNoShortcut  = i18n.Err("hub-extension_system.no_shortcut")
	errBadFamily   = i18n.Err("hub-extension_system.bad_family")
	errShortcutArg = i18n.Err("hub-extension_system.shortcut_arg")
	// errUseWidget: a widget asked for as a page (…/-/ui/{ui}); widgets are drawn with …/-/widget/{ui}.
	errUseWidget = i18n.Err("hub-extension_system.use_widget")
)

// contextLang is the language of what ctx is doing for people: its request's (i18n.With), else the user's.
func (h *Manager) contextLang(ctx context.Context) i18n.Lang {
	if l := i18n.From(ctx); i18n.Valid(l) {
		return l
	}
	if h == nil {
		return i18n.Default
	}
	return h.userLang()
}

// kindErr is a localized message that is also kind (errors.Is), for a sentinel error with detail of its own.
type kindErr struct {
	msg  *i18n.Message
	kind error
}

func (e *kindErr) Error() string   { return e.msg.Error() }
func (e *kindErr) Unwrap() []error { return []error{e.msg, e.kind} }

// localizedKind is key's text as an error that is kind.
func localizedKind(kind error, key string, args i18n.Args) error {
	return &kindErr{msg: &i18n.Message{Key: key, Args: args}, kind: kind}
}

// widgetCall draws extension id's widget ui at the family in.Context.Size: the view kept from before while it is not
// due, else the extension's ui.render held to the family (FitWidget), kept until the widget's refresh is due.
func (h *Manager) widgetCall(ctx context.Context, id, uiID string, in uiRequest) (wire.WidgetAnswer, error) {
	var out wire.WidgetAnswer
	id = h.extensions.reg.CanonicalID(id)
	w := &h.extensions.widgets
	w.mu.Lock()
	generation := w.generation[id]
	w.mu.Unlock()
	p := h.extensions.reg.Get(id)
	hp := h.extensions.get(id)
	if p == nil || !p.Record.Enabled || hp == nil || hp.proc == nil {
		return out, errNoExtension
	}
	i := slices.IndexFunc(p.Manifest.UIs(), func(u extension.UIItem) bool { return u.ID == uiID })
	if i < 0 {
		return out, errNoExtension
	}
	item := p.Manifest.UIs()[i]
	if item.Placement != "widget" {
		return out, errNotWidget
	}
	family := in.Context.Size
	if !slices.Contains(item.Families, family) {
		return out, localizedKind(errBadFamily, "hub-extension_system.bad_family_detail",
			i18n.Args{"title": item.Title, "families": strings.Join(item.Families, i18n.T(h.contextLang(ctx), "hub-extension_system.list_separator"))})
	}
	slot, _ := extension.Slot("widget")
	params := h.uiParams(item, slot, in)
	params.Size, params.State, params.Action, params.Values = family, "", "", nil
	key := widgetCacheKey(id, uiID, params)
	now := time.Now()
	w.mu.Lock()
	if w.generation[id] != generation {
		w.mu.Unlock()
		return out, errNoExtension
	}
	if v, ok := w.views[key]; ok && now.Before(v.expires) {
		w.mu.Unlock()
		return wire.WidgetAnswer{View: v.view, ExpiresAt: v.expires}, nil
	}
	w.mu.Unlock()
	cctx, cancel := context.WithTimeout(ctx, uiTimeout)
	defer cancel()
	var raw json.RawMessage
	if err := hp.proc.Call(cctx, "ui.render", params, &raw); err != nil {
		var re *rpc.Error
		if errors.As(err, &re) {
			return out, errors.New(re.Message)
		}
		return out, err
	}
	var v extension.View
	if err := json.Unmarshal(raw, &v); err != nil {
		return out, i18n.Err("hub-extension_system.widget_unsupported", i18n.Args{"err": err.Error()})
	}
	dropped, err := extension.FitWidget(&v, family, params.UIVersion, h.uiFallbackLang(ctx, params.Locale))
	if err != nil {
		return out, i18n.Err("hub-extension_system.widget_unsupported", i18n.Args{"err": err.Error()})
	}
	if len(dropped) > 0 {
		h.extensions.logf("extension %s: widget %s (%s) drew what a widget does not (%s): left out", id, uiID, family, strings.Join(dropped, ", "))
	}
	expires := now.Add(item.Refresh()).UTC().Truncate(time.Second)
	w.mu.Lock()
	if w.generation[id] != generation {
		w.mu.Unlock()
		return out, errNoExtension
	}
	if w.views == nil {
		w.views = map[string]widgetView{}
	}
	for k, old := range w.views { // drop the due ones as new ones come: the map stays the size of the widgets on screens
		if !now.Before(old.expires) {
			delete(w.views, k)
		}
	}
	w.views[key] = widgetView{view: v, expires: expires}
	w.mu.Unlock()
	return wire.WidgetAnswer{View: v, ExpiresAt: expires}, nil
}

// shortcutCall runs extension id's shortcut sid with the values: they are checked against its params (declared names
// only, the required ones there, numbers that parse, toggles true or false, one of a choice's values), then the
// extension's shortcut.run answers a sentence and, maybe, a view held to a large widget's subset.
func (h *Manager) shortcutCall(ctx context.Context, id, sid string, in wire.ShortcutRequest) (wire.ShortcutResult, error) {
	var out wire.ShortcutResult
	p := h.extensions.reg.Get(id)
	hp := h.extensions.get(id)
	if p == nil || !p.Record.Enabled || hp == nil || hp.proc == nil {
		return out, errNoExtension
	}
	i := slices.IndexFunc(p.Manifest.Shortcuts(), func(s extension.ShortcutItem) bool { return s.ID == sid })
	if i < 0 {
		return out, errNoShortcut
	}
	sc := p.Manifest.Shortcuts()[i]
	values := map[string]string{}
	for name, v := range in.Values {
		j := slices.IndexFunc(sc.Params, func(p extension.ShortcutParam) bool { return p.Name == name })
		if j < 0 {
			return out, localizedKind(errShortcutArg, "hub-extension_system.shortcut_no_param", i18n.Args{"name": name})
		}
		if problem := checkShortcutValue(sc.Params[j], v); problem != "" {
			return out, localizedKind(errShortcutArg, "hub-extension_system.shortcut_bad_value",
				i18n.Args{"title": sc.Params[j].Title, "problem": i18n.T(h.contextLang(ctx), "hub-extension_system."+problem)})
		}
		values[name] = v
	}
	for _, p := range sc.Params {
		if p.Required && values[p.Name] == "" {
			return out, localizedKind(errShortcutArg, "hub-extension_system.shortcut_missing", i18n.Args{"title": p.Title})
		}
	}
	var req uiRequest
	if in.Context != nil {
		c := in.Context
		req.Context.TZ, req.Context.Locale, req.Context.Platform, req.Context.UIVersion = c.TZ, c.Locale, c.Platform, c.UIVersion
	}
	slot, _ := extension.Slot("widget")
	ctxParams := h.uiParams(extension.UIItem{ID: sid, Placement: "widget"}, slot, req)
	params := wire.ShortcutRunParams{Contribution: sid, Values: values, UIVersion: ctxParams.UIVersion, TZ: ctxParams.TZ,
		Locale: ctxParams.Locale, Platform: ctxParams.Platform}
	cctx, cancel := context.WithTimeout(ctx, uiTimeout)
	defer cancel()
	if err := hp.proc.Call(cctx, "shortcut.run", params, &out); err != nil {
		var re *rpc.Error
		if errors.As(err, &re) {
			return out, errors.New(re.Message)
		}
		return out, err
	}
	out.Text = clipRunes(out.Text, extension.StringMost)
	if out.View != nil {
		if _, err := extension.FitWidget(out.View, "large", params.UIVersion, h.uiFallbackLang(ctx, params.Locale)); err != nil {
			return wire.ShortcutResult{}, i18n.Err("hub-extension_system.result_unsupported", i18n.Args{"err": err.Error()})
		}
	}
	return out, nil
}

// checkShortcutValue holds a value to its param's kind: "" when it fits, else the problem's key in
// hub-extension_system.
func checkShortcutValue(p extension.ShortcutParam, v string) string {
	switch p.Kind {
	case "number":
		if _, err := strconv.ParseFloat(v, 64); err != nil && v != "" {
			return "value_not_number"
		}
	case "toggle":
		if v != "true" && v != "false" && v != "" {
			return "value_not_bool"
		}
	case "choice":
		if v != "" && !slices.ContainsFunc(p.Choices, func(c wire.Choice) bool { return c.Value == v }) {
			return "value_not_choice"
		}
	}
	if len(v) > extension.StringMost {
		return "value_too_long"
	}
	return ""
}

// deviceWidgets are the widgets each device has (device id → its widgets), as each last said; the caller holds no
// lock and reads the map under widgets.mu.
func (h *Manager) deviceWidgets(ctx context.Context) map[string][]wire.DeviceWidget {
	w := &h.extensions.widgets
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.loaded && h.store != nil {
		var stored map[string][]wire.DeviceWidget
		if _, err := h.store.Setting(ctx, deviceWidgetsKey, &stored); err == nil {
			w.devices, w.loaded = stored, true
		}
	}
	if w.devices == nil {
		w.devices = map[string][]wire.DeviceWidget{}
	}
	return w.devices
}

// setDeviceWidgets keeps the widgets device id says it has (none forgets it).
func (h *Manager) setDeviceWidgets(ctx context.Context, id string, list []wire.DeviceWidget) error {
	all := h.deviceWidgets(ctx)
	w := &h.extensions.widgets
	w.mu.Lock()
	defer w.mu.Unlock() // through the write: two devices saying theirs at once are stored in the order they were taken
	if len(list) == 0 {
		delete(all, id)
	} else {
		all[id] = list
	}
	if h.store == nil {
		return nil
	}
	return h.store.PutSetting(ctx, deviceWidgetsKey, all)
}

// widgetsDropExtension retires every cached family/context and any render still in flight, even after uninstall
// or an upgrade that removed its widget declarations. Device reports survive to target the lifecycle wake.
func (h *Manager) widgetsDropExtension(id string) {
	w := &h.extensions.widgets
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.generation == nil {
		w.generation = map[string]uint64{}
	}
	w.generation[id]++
	for k := range w.views {
		if strings.HasPrefix(k, id+"\x00") {
			delete(w.views, k)
		}
	}
}

// widgetsChanged takes what an extension said changed (ui.invalidate: its ui ids, "" for all of them): the views kept
// of them are dropped, and each device showing one of them is woken (a background push with extension_widget), once
// in widgetPushEvery per widget.
func (h *Manager) widgetsChanged(id string, ids map[string]bool) {
	if h.extensions == nil {
		return
	}
	p := h.extensions.reg.Get(id)
	if p == nil {
		return
	}
	changed := map[string]bool{}
	for _, u := range p.Manifest.UIs() {
		if u.Placement == "widget" && (ids[""] || ids[u.ID]) {
			changed[u.ID] = true
		}
	}
	if len(changed) == 0 {
		return
	}
	w := &h.extensions.widgets
	w.mu.Lock()
	for k := range w.views {
		parts := strings.SplitN(k, "\x00", 3)
		if parts[0] == id && changed[parts[1]] {
			delete(w.views, k)
		}
	}
	w.mu.Unlock()
	h.widgetsWake(id, changed, false)
}

// widgetsWake uses the persisted device reports, not the current manifest: nil ids means every reported widget
// of the extension, including removed contributions. Lifecycle changes force a wake even after a content push.
func (h *Manager) widgetsWake(id string, ids map[string]bool, force bool) {
	devices := h.deviceWidgets(context.Background())
	now := time.Now()
	w := &h.extensions.widgets
	w.mu.Lock()
	if w.woken == nil {
		w.woken = map[string]time.Time{}
	}
	wake := map[string][]string{} // device → widgets to wake it for
	for dev, list := range devices {
		for _, dw := range list {
			k := dev + "/" + dw.Extension + "/" + dw.ID
			if dw.Extension != id || (ids != nil && !ids[dw.ID]) || slices.Contains(wake[dev], dw.ID) ||
				(!force && now.Sub(w.woken[k]) < widgetPushEvery) {
				continue
			}
			w.woken[k] = now
			wake[dev] = append(wake[dev], dw.ID)
		}
	}
	w.mu.Unlock()
	for dev, uis := range wake {
		for _, ui := range uis {
			h.pushBackground(map[string]any{"aps": map[string]any{"content-available": 1},
				"extension_widget": wire.WidgetPush{Extension: id, ID: ui}}, []string{dev})
		}
	}
}

func (h *Manager) extensionSystemRoutes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/extensions/-/widgets", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, wire.WidgetPlaces{Widgets: h.extensions.widgetPlaces()})
	})
	api.HandleFunc("POST /api/v1/extensions/{id}/-/widget/{ui}", func(w http.ResponseWriter, r *http.Request) {
		var in uiRequest
		if !readExtensionJSON(w, r, &in) {
			return
		}
		res, err := h.widgetCall(r.Context(), r.PathValue("id"), r.PathValue("ui"), in)
		switch {
		case errors.Is(err, errNoExtension):
			writeErrCode(w, http.StatusNotFound, "not_found", err)
		case errors.Is(err, errNotWidget), errors.Is(err, errBadFamily):
			writeErrCode(w, http.StatusBadRequest, "bad_request", err)
		case err != nil:
			writeErrCode(w, http.StatusBadGateway, "plugin_failed", err)
		default:
			writeJSON(w, res)
		}
	})
	api.HandleFunc("GET /api/v1/extensions/-/shortcuts", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, wire.ShortcutPlaces{Shortcuts: h.extensions.shortcutPlaces()})
	})
	api.HandleFunc("POST /api/v1/extensions/{id}/-/shortcut/{sid}", func(w http.ResponseWriter, r *http.Request) {
		var in wire.ShortcutRequest
		if !readExtensionJSON(w, r, &in) {
			return
		}
		res, err := h.shortcutCall(r.Context(), r.PathValue("id"), r.PathValue("sid"), in)
		switch {
		case errors.Is(err, errNoExtension), errors.Is(err, errNoShortcut):
			writeErrCode(w, http.StatusNotFound, "not_found", err)
		case errors.Is(err, errShortcutArg):
			writeErrCode(w, http.StatusBadRequest, "bad_request", err)
		case err != nil:
			writeErrCode(w, http.StatusBadGateway, "plugin_failed", err)
		default:
			writeJSON(w, res)
		}
	})
	// Which widgets this device has on its screens (the app says it as they change): an extension's ui.invalidate
	// wakes it for those only.
	api.HandleFunc("PUT /api/v1/devices/{id}/widgets", func(w http.ResponseWriter, r *http.Request) {
		var in wire.DeviceWidgets
		if !readExtensionJSON(w, r, &in) {
			return
		}
		if len(in.Widgets) > deviceWidgetsMost {
			writeErrCode(w, http.StatusBadRequest, "bad_request", fmt.Errorf("at most %d widgets", deviceWidgetsMost))
			return
		}
		var list []wire.DeviceWidget
		for _, dw := range in.Widgets {
			if dw.Extension == "" || dw.ID == "" || !slices.Contains(extension.WidgetFamilies, dw.Family) {
				writeErrCode(w, http.StatusBadRequest, "bad_request", errors.New("a widget is {extension, id, family}"))
				return
			}
			if !slices.ContainsFunc(list, func(o wire.DeviceWidget) bool { return o == dw }) {
				list = append(list, dw)
			}
		}
		if err := h.setDeviceWidgets(r.Context(), r.PathValue("id"), list); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if list == nil {
			list = []wire.DeviceWidget{}
		}
		writeJSON(w, wire.DeviceWidgets{Widgets: list})
	})
}
