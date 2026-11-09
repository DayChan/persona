package extmgr

// Per-app approval for the assistant operating a Mac (docs/extensions/computer-use.md "Lock screen, staying awake, per-app
// approval" c, #1507): the first time it operates an app on a Mac (or reads its whole screen) the phone asks once,
// "Allow once" / "Always allow" / "Deny". Apps always allowed are kept by bundle id for that Mac in the hub's settings
// (computerAppsKey) and listed, and removable, on the machine's page. Some are never remembered and asked about every
// time (askedEveryTime), and so is a call whose apps cannot all be told (a script naming them by expression, a step
// with no app, a launch naming none). An app a call opens counts as one it operates.

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

const computerAppsKey = "computer.apps"

// wholeScreen stands for computer_state with no app: everything on the Mac's screen.
// i18n-ignore: canonical stored screen name, localized only in presentation copies.
const wholeScreen = "整个屏幕"

// approvedApp is an app always allowed on a Mac.
type approvedApp struct {
	BundleID string    `json:"bundle_id"`
	Name     string    `json:"name"`
	At       time.Time `json:"at"`
}

var computerAppsMu sync.Mutex

// askedEveryTime: terminals, System Settings, Keychain Access, password managers and Persona, by bundle id or by the
// name macOS shows (English or Chinese), case aside.
func askedEveryTime(a approvedApp) bool {
	if askedEveryTimeIDs[strings.ToLower(a.BundleID)] {
		return true
	}
	return slices.Contains(askedEveryTimeNames, strings.ToLower(strings.TrimSpace(a.Name)))
}

var askedEveryTimeIDs = map[string]bool{
	"com.apple.terminal": true, "com.googlecode.iterm2": true, "dev.warp.warp-stable": true, "com.mitchellh.ghostty": true,
	"io.alacritty": true, "net.kovidgoyal.kitty": true, "com.github.wez.wezterm": true,
	"com.apple.systempreferences": true, "com.apple.settings": true, "com.apple.keychainaccess": true,
	"com.apple.passwords": true, "com.1password.1password": true, "com.agilebits.onepassword7": true,
	"com.bitwarden.desktop": true, "com.lastpass.lastpass": true, "com.dashlane.dashlane": true,
	"org.keepassxc.keepassxc": true, "in.sinew.enpass-desktop": true,
}

var askedEveryTimeNames = []string{
	"terminal", "终端", "iterm", "iterm2", "warp", "ghostty", "alacritty", "kitty", "wezterm",
	"system settings", "系统设置", "system preferences", "系统偏好设置", "keychain access", "钥匙串访问",
	"passwords", "密码", "1password", "bitwarden", "lastpass", "dashlane", "keepassxc", "enpass",
	"persona", "persona link",
}

// approval: the plugin's "Ask the first time an app is operated", on unless turned off.
func (s cuaSettings) approval() bool { return s.AppApproval == nil || *s.AppApproval }

var (
	getAppLiteral = lazyre.New(`getApp\(\s*(?:"([^"\\]+)"|'([^'\\]+)')\s*\)`)
	launchLiteral = lazyre.New(`launch\(\s*(?:"([^"\\]+)"|'([^'\\]+)')\s*\)`)
)

// callApps is what a computer tool call names: its apps by name (or wholeScreen), and whether that is all it may
// touch (false: a script naming its apps by expression or none, a step with no app of its own or the call's, a
// launch_app step naming no app). An app the call opens (launch_app, cua.launch) is one it touches.
func callApps(name, app, script string, steps json.RawMessage) (apps []string, whole bool) {
	add := func(a string) {
		if a = strings.TrimSpace(a); a != "" && !slices.Contains(apps, a) {
			apps = append(apps, a)
		}
	}
	switch {
	case name == "computer_state":
		if app == "" {
			return []string{wholeScreen}, true
		}
		return []string{app}, true
	case script != "":
		for _, m := range getAppLiteral.FindAllStringSubmatch(script, -1) {
			add(m[1] + m[2])
		}
		for _, m := range launchLiteral.FindAllStringSubmatch(script, -1) {
			add(m[1] + m[2])
		}
		return apps, len(apps) > 0 && strings.Count(script, "getApp(") == len(getAppLiteral.FindAllString(script, -1)) &&
			strings.Count(script, "launch(") == len(launchLiteral.FindAllString(script, -1))
	}
	var list []map[string]json.RawMessage
	_ = json.Unmarshal(steps, &list)
	whole = len(list) > 0
	for _, s := range list {
		if opened, ok := launchTarget(s); ok {
			if opened == "" {
				whole = false
			} else {
				add(opened)
			}
			continue
		}
		var named struct {
			App string `json:"app"`
		}
		found := ""
		for k, raw := range s {
			if k == "wait_for" || k == "expect" || k == "tool" {
				continue
			}
			if json.Unmarshal(raw, &named) == nil && named.App != "" {
				found = named.App
			}
		}
		if found == "" {
			found = app
		}
		if found == "" {
			whole = false
			continue
		}
		add(found)
	}
	return apps, whole
}

// launchTarget is the app a launch_app step opens, {"launch_app": {"name": …}} or {"tool": "launch_app", "args": {…}}:
// its name or bundle id, "" when it names neither (not the call's default app: what opens then is the backend's
// choice). ok is false for any other step.
func launchTarget(s map[string]json.RawMessage) (opened string, ok bool) {
	var tool string
	args := s["launch_app"]
	if raw, has := s["tool"]; has {
		_ = json.Unmarshal(raw, &tool)
	}
	switch {
	case tool == "launch_app":
		args = s["args"]
	case tool != "" || args == nil:
		return "", false
	}
	var a struct {
		Name     string `json:"name"`
		BundleID string `json:"bundle_id"`
	}
	_ = json.Unmarshal(args, &a)
	if opened = strings.TrimSpace(a.Name); opened == "" {
		opened = strings.TrimSpace(a.BundleID)
	}
	return opened, true
}

// appsOn is what m's Cua Driver knows of the apps named: bundle id and name, by the name given (case aside); a name
// it does not know stands for itself.
func (h *Manager) appsOn(ctx context.Context, m *Machine, names []string) []approvedApp {
	var known struct {
		Apps []struct {
			Name     string `json:"name"`
			BundleID string `json:"bundle_id"`
		} `json:"apps"`
	}
	// LCU's own elicitation names the app. Probing list_apps first would start the runtime, and might ask, before
	// this card exists.
	if h.callBackend(m) != "lcu" && slices.ContainsFunc(names, func(n string) bool { return n != wholeScreen }) {
		if res, err := h.computerCall(ctx, m, "persona-apps", "", "list_apps", map[string]any{}); err == nil {
			_ = json.Unmarshal(res.Output, &known)
		}
	}
	out := make([]approvedApp, 0, len(names))
	for _, n := range names {
		a := approvedApp{BundleID: "name:" + strings.ToLower(n), Name: n}
		if n == wholeScreen {
			a.BundleID = "screen"
		}
		for _, k := range known.Apps {
			if k.BundleID != "" && (strings.EqualFold(k.Name, n) || strings.EqualFold(k.BundleID, n)) {
				a = approvedApp{BundleID: k.BundleID, Name: k.Name}
				break
			}
		}
		out = append(out, a)
	}
	return out
}

// approvedApps is the apps always allowed on each Mac. A failed read is an error, not an empty list: the writers put
// back the whole map, so taking it for "none" would drop every other Mac's apps. A stored value that does not decode
// is not: no later read would do better, so what did decode (nothing, for broken JSON) stands and the next write
// replaces the bad value.
func (h *Manager) approvedApps(ctx context.Context) (map[string][]approvedApp, error) {
	all := map[string][]approvedApp{}
	if h.store == nil {
		return all, nil
	}
	if _, err := h.store.Setting(ctx, computerAppsKey, &all); err != nil {
		if !undecodable(err) {
			return nil, err
		}
		h.logf("%s does not decode, keeping what does: %v", computerAppsKey, err)
	}
	if all == nil { // a stored null
		all = map[string][]approvedApp{}
	}
	return all, nil
}

// undecodable: err is the stored JSON not fitting, not the store failing to answer.
func undecodable(err error) bool {
	var syntax *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syntax) || errors.As(err, &typ)
}

func (h *Manager) putApprovedApps(ctx context.Context, all map[string][]approvedApp) error {
	if h.store == nil {
		return i18n.ErrEnglish("hub-computer_apps.err_no_database")
	}
	return h.store.PutSetting(ctx, computerAppsKey, all)
}

// rememberApps keeps apps always allowed on m, writing nothing when the apps kept cannot be read. An app already kept
// is not added twice (the approval may have asked about it because its read failed).
func (h *Manager) rememberApps(ctx context.Context, m *Machine, apps []approvedApp) error {
	computerAppsMu.Lock()
	defer computerAppsMu.Unlock()
	all, err := h.approvedApps(ctx)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if slices.ContainsFunc(all[m.cfg.ID], func(k approvedApp) bool { return k.BundleID == a.BundleID }) {
			continue
		}
		a.At = time.Now().UTC()
		all[m.cfg.ID] = append(all[m.cfg.ID], a)
	}
	return h.putApprovedApps(ctx, all)
}

// approveApps asks on the phone about the apps this call names that m has not always allowed (or every time: those
// never remembered, a call whose apps are not all known). asked: a card was shown; ok: go ahead.
func (h *Manager) approveApps(ctx context.Context, m *Machine, by asker, tool string, names []string, whole bool, args json.RawMessage) (asked, ok bool) {
	if !h.cua().approval() {
		return false, true
	}
	apps := h.appsOn(ctx, m, names)
	all, err := h.approvedApps(ctx)
	if err != nil {
		h.logf("[%s] reading the apps always allowed: %v", m.cfg.ID, err) // asked, as if none were
	}
	kept := all[m.cfg.ID]
	var need []approvedApp
	remember := whole
	for _, a := range apps {
		switch {
		case askedEveryTime(a):
			need, remember = append(need, a), false
		case !slices.ContainsFunc(kept, func(k approvedApp) bool { return k.BundleID == a.BundleID }):
			need = append(need, a)
		}
	}
	if whole && len(need) == 0 {
		return false, true
	}
	l := h.userLang()
	shown := make([]string, 0, len(need))
	for _, a := range need {
		name := a.Name
		if a.BundleID == "screen" {
			name = i18n.T(l, "hub-computer_apps.whole_screen")
		}
		shown = append(shown, i18n.T(l, "hub-computer_apps.app", i18n.Args{"name": name}))
	}
	appList := strings.Join(shown, i18n.T(l, "hub-computer_apps.list_sep"))
	summary := i18n.T(l, "hub-computer_apps.ask_apps", i18n.Args{"machine": m.cfg.Name, "apps": appList})
	if len(need) == 0 {
		summary = i18n.T(l, "hub-computer_apps.ask_run", i18n.Args{"machine": m.cfg.Name})
	}
	if !whole {
		summary = i18n.T(l, "hub-computer_apps.ask_unknown", i18n.Args{"summary": summary})
	}
	ix := proto.Interaction{
		ID: "ix_" + randomHex(6), MachineID: by.machine, SessionID: by.session, Kind: proto.IxPermission, ToolName: tool, Summary: summary,
		ToolInput: args, SessionTitle: "Persona", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(15 * time.Minute).UTC(),
	}
	if remember {
		ix.AlwaysLabel = i18n.T(l, "hub-computer_apps.always", i18n.Args{"apps": appList})
	}
	r, _ := h.waitInteraction(ctx, ix)
	ok = r.Allow != nil && *r.Allow
	if ok && r.Always && remember {
		if err := h.rememberApps(ctx, m, need); err != nil {
			h.logf("[%s] keeping the apps allowed: %v", m.cfg.ID, err)
		} else {
			h.logf("[%s] %d app(s) always allowed for the assistant", m.cfg.ID, len(need))
		}
	}
	return true, ok
}

// computerAppsOwner binds an opt-in request to the manager whose settings the client displayed. The hub routes
// machine requests to their current owner, regardless of ManagerHeader; checking here also works through an older
// hub. The receiver's identity is immutable, so a delayed handover cannot turn this into another manager's write.
func (h *Manager) computerAppsOwner(w http.ResponseWriter, r *http.Request) bool {
	if expected := r.URL.Query().Get("expected_manager"); expected != "" && expected != h.computerAppsManager() {
		writeErrCode(w, http.StatusConflict, "machine_owner_changed", i18n.Err("hub-computer_apps.owner_changed"))
		return false
	}
	return true
}

func (h *Manager) computerAppsManager() string {
	return cmp.Or(h.opts.ManagerID, assistantwire.AssistantID)
}

// getComputerApps: GET /api/v1/machines/{mid}/computer/apps, the apps always allowed on this Mac.
func (h *Manager) getComputerApps(w http.ResponseWriter, r *http.Request, m *Machine) {
	if !h.computerAppsOwner(w, r) {
		return
	}
	all, err := h.approvedApps(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	apps := slices.Clone(all[m.cfg.ID])
	for n := range apps {
		if apps[n].BundleID == "screen" && apps[n].Name == wholeScreen {
			apps[n].Name = i18n.T(h.requestLang(r), "hub-computer_apps.whole_screen")
		}
	}
	if apps == nil {
		apps = []approvedApp{}
	}
	// The identity acknowledges support for expected_manager; old clients ignore this extra field.
	writeJSON(w, map[string]any{"apps": apps, "manager": h.computerAppsManager()})
}

// revokeComputerApp is safe through an older hub: legacy managers do not match this alias, while this receiver
// requires and enforces its immutable identity before delegating to the existing persisted mutation.
func (h *Manager) revokeComputerApp(w http.ResponseWriter, r *http.Request, m *Machine) {
	if r.URL.Query().Get("expected_manager") == "" {
		writeErrCode(w, http.StatusPreconditionRequired, "expected_manager_required", i18n.Err("hub-computer_apps.owner_required"))
		return
	}
	h.deleteComputerApp(w, r, m)
}

// deleteComputerApp: DELETE /api/v1/machines/{mid}/computer/apps/{bundle}, removing only the saved approval.
func (h *Manager) deleteComputerApp(w http.ResponseWriter, r *http.Request, m *Machine) {
	if !h.computerAppsOwner(w, r) {
		return
	}
	id := r.PathValue("bundle")
	computerAppsMu.Lock()
	all, err := h.approvedApps(r.Context())
	if err == nil {
		all[m.cfg.ID] = slices.DeleteFunc(all[m.cfg.ID], func(a approvedApp) bool { return a.BundleID == id })
		err = h.putApprovedApps(r.Context(), all)
	}
	computerAppsMu.Unlock()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	h.getComputerApps(w, r, m)
}
