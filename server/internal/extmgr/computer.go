package extmgr

// Operating the user's Macs for the assistant through Cua Driver: the built-in plugin cua, off until the user turns
// it on, then machine by machine (docs/extensions/computer-use.md). While it is off no node is asked about cua-driver, the
// assistant has no computer_* tools and the machines carry no Computer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lcuconfirm"
	"github.com/nwverse/persona/server/internal/proto"
)

const cuaExtensionID = "cua"

// cuaConfig is the plugin's settings: the machines it is on for (turned on and off on each machine's page) and
// whether the assistant asks on the phone before it acts.
const cuaConfig = `{
  "type": "object",
  "properties": {
    "machines": {"type": "array", "title": "能操控的电脑", "description": "打开的 Mac，助手才能操控。开关即时生效，和它机器页上的「允许助手操控这台电脑」是同一个", "items": {"type": "string", "format": "machine", "x-os": ["darwin"]}, "uniqueItems": true},
    "backend": {"type": "string", "title": "操控后端", "description": "Cua Driver 用 cua.ai 的驱动。LCU 用本机的 amontlabs/lcu（需要已安装的官方 ChatGPT 桌面版提供运行时，Persona 不下载、不登录它）。确认都在手机和网页上，不在 Claude 或 Codex 里。自动：装好了 LCU 和 ChatGPT 就用 LCU，否则用 Cua Driver。点击音效和 Agent 光标只对 Cua Driver 生效", "oneOf": [{"const": "cua", "title": "Cua Driver"}, {"const": "lcu", "title": "LCU"}, {"const": "auto", "title": "自动"}], "default": "cua"},
    "confirm": {"type": "boolean", "title": "操作前在手机上确认", "description": "打开后，助手每次点击、输入、按键、打开或关闭 App 之前先在手机上问你；关着时直接做，记进 hub 的日志", "default": false},
    "app_approval": {"type": "boolean", "title": "第一次操作一个 App 时先问", "description": "助手第一次在一台 Mac 上操作某个 App（或看整个屏幕）时在手机上问一次，选「一直允许」后记住，在机器页可以移除；终端、系统设置、钥匙串访问、密码管理器和 Persona 每次都问", "default": true},
    "no_auto_lock": {"type": "array", "title": "扩展开着时不自动锁屏", "description": "打开的 Mac 在扩展开着时一直不因空闲锁屏（node 持有 caffeinate，不改系统设置）；和 hub 断开 30 分钟后自动恢复。手动锁屏、合盖照常", "items": {"type": "string", "format": "machine", "x-os": ["darwin"]}, "uniqueItems": true, "x-confirm": "打开后这台 Mac 在扩展开着时一直不会因为空闲锁屏，坐在它前面的人能直接用它。和 hub 断开 30 分钟后自动恢复。"},
    "keep_awake": {"type": "boolean", "title": "操作或开着 VNC 时保持唤醒", "description": "助手操作那台 Mac、或有人在看它的画面时，它不睡、不因空闲锁屏；最后一次操作 10 分钟后、画面关掉后就撤掉，不改系统设置", "default": true},
    "click_sound": {"type": "string", "title": "点击音效", "description": "助手在 Mac 上点按时，那台 Mac 响一下系统提示音，坐在旁边的人听得到它在动。前台点击会把窗口提到前面、动真的鼠标；后台点击不动光标", "oneOf": [{"const": "none", "title": "无"}, {"const": "foreground", "title": "只前台点击"}, {"const": "all", "title": "前台和后台点击"}], "default": "none"},
    "show_cursor": {"type": "boolean", "title": "显示 Agent 的光标", "description": "助手和电脑上的 Agent 经 Cua Driver 操作时在屏幕上画一个光标，看 VNC 时知道它点在哪。改了会重启扩展打开的 Mac 上 Cua Driver 的后台进程，正在进行的操作会断一下", "default": true}
  }
}`

func cuaManifest(h *Manager) *extension.Manifest {
	items, _ := json.Marshal([]extension.ComputerItem{{ID: cuaExtensionID, Name: "Cua Driver"}})
	return &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: cuaExtensionID, Name: "操控电脑（Cua）", Version: h.version,
		Description: "让助手操控你的 Mac：看窗口和屏幕、点按、输入、截图。后端用 Cua Driver，或用 LCU（amontlabs/lcu，需要官方 ChatGPT 桌面版）。确认都在手机和网页上。",
		Homepage:    "https://github.com/trycua/cua", Contributes: map[string]json.RawMessage{"computer": items},
		Configuration: json.RawMessage(cuaConfig), Node: builtinNode("machines")}
}

// cuaSettings is the plugin's state: On when it is on, then the machines it is on for and whether to ask first.
type cuaSettings struct {
	On       bool     `json:"-"`
	Machines []string `json:"machines"`
	Confirm  bool     `json:"confirm"`
	// KeepAwake: "Keep awake while operating or while VNC is open", on unless turned off (nil); the node holds
	// caffeinate (node/awake.go).
	KeepAwake *bool `json:"keep_awake"`
	// AppApproval: "Ask the first time an app is operated", on unless turned off (computer_apps.go).
	AppApproval *bool `json:"app_approval"`
	// NoAutoLock: the Macs "Don't auto-lock while the extension is on" is on for (computer_nolock.go).
	NoAutoLock []string `json:"no_auto_lock"`
	// ClickSound: "Click sound", none (or empty), foreground or all; the node plays it (node/cua_extras.go).
	ClickSound string `json:"click_sound"`
	// Backend: cua (Cua Driver, the default), lcu, or auto (lcu_status.go).
	Backend string `json:"backend"`
	// ShowCursor: "Show the agent's cursor", on unless turned off (computer_cursor.go).
	ShowCursor *bool `json:"show_cursor"`
}

// cursor: Cua Driver draws the agent's cursor.
func (s cuaSettings) cursor() bool { return s.ShowCursor == nil || *s.ShowCursor }

// backend is which driver the plugin is set to. Anything but lcu and auto is Cua Driver, including an empty value
// from a hub whose settings were saved before the choice existed.
func (s cuaSettings) backend() string {
	switch s.Backend {
	case "lcu", "auto":
		return s.Backend
	default:
		return "cua"
	}
}

// awake: a Mac it operates, or whose live screen is open, is kept awake.
func (s cuaSettings) awake() bool { return s.KeepAwake == nil || *s.KeepAwake }

func (h *Manager) cua() cuaSettings { return h.machineExtension(cuaExtensionID) }

// machineExtension is a built-in plugin that is turned on per Mac (cua, ego): on or not, its Macs, whether to ask first.
func (h *Manager) machineExtension(id string) cuaSettings {
	if h.extensions == nil || h.extensions.reg == nil {
		return cuaSettings{}
	}
	// A snapshot, not the registry's shared *Extension: SetEnabled and SetConfig write its record under the
	// registry lock while tells and refreshes read it here.
	p := h.extensions.reg.Snapshot(id)
	if p == nil || !p.Record.Enabled {
		return cuaSettings{}
	}
	s := cuaSettings{On: true}
	_ = json.Unmarshal(h.extensions.reg.Config(p), &s)
	return s
}

// operable: the plugin is on and m is a Mac (the only kind Cua Driver runs on here).
func (h *Manager) operable(m *Machine) bool {
	m.mu.Lock()
	goos := m.info.OS
	m.mu.Unlock()
	return h.cua().On && goos == "darwin"
}

// computerOf is m's proto.Machine.Computer: nil unless the plugin is on and m is a Mac; else the last status the node
// gave (for every Mac while the plugin is on, so a list of switches can say how Cua Driver is on each before it is
// turned on), with Enabled when the plugin is on for m.
func (h *Manager) computerOf(m *Machine) *proto.Computer {
	s := h.cua()
	m.mu.Lock()
	darwin, last := m.info.OS == "darwin", m.computer
	m.mu.Unlock()
	if !s.On || !darwin {
		return nil
	}
	c := proto.Computer{}
	if last != nil {
		c = *last
	}
	c.Enabled = slices.Contains(s.Machines, m.cfg.ID)
	c.NoAutoLock = c.Enabled && slices.Contains(s.NoAutoLock, m.cfg.ID)
	return &c
}

// refreshComputer asks m's node what it has of Cua Driver now, when the plugin is on for m; the machine list goes out
// again when that changed.
func (h *Manager) refreshComputer(ctx context.Context, m *Machine) *proto.Computer {
	if c := h.computerOf(m); c == nil || !m.isOnline() {
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	asked := time.Now()
	var c proto.Computer
	if err := m.callBuiltin(ctx, "computer.status", map[string]any{"backend": h.cua().backend()}, &c); err != nil {
		if !strings.Contains(err.Error(), "unknown method") {
			return h.computerOf(m)
		}
		c = proto.Computer{Note: i18n.T(i18n.Default, "hub-computer.note_outdated")}
	}
	if h.keepComputer(m, c, asked) {
		h.broadcastMachines()
	}
	return h.computerOf(m)
}

// keepComputer stores what m's node answered, unless what is kept is newer: by when the node answered (its checked,
// set last), so a status asked after an install but answered before it does not undo it; an older node's answers,
// which have none, by when they were asked. True when what is kept changed.
func (h *Manager) keepComputer(m *Machine, c proto.Computer, asked time.Time) bool {
	when := c.Checked
	if when.IsZero() {
		when = asked
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if when.Before(m.computerAt) {
		return false
	}
	old := m.computer
	m.computer, m.computerAt = &c, when
	return old == nil || !sameComputer(*old, c)
}

// sameComputer: the same apart from when it was checked; the job and the displays by value.
func sameComputer(a, b proto.Computer) bool {
	a.Checked, b.Checked = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

// computerChanged: the plugin was turned on or off, or its machines changed: ask the Macs again (every one while it is
// on: the settings' list says how Cua Driver is on each).
func (h *Manager) computerChanged(ctx context.Context) {
	for _, m := range h.machinesNow() {
		if c := h.computerOf(m); c != nil && m.isOnline() {
			go h.refreshComputer(ctx, m)
		}
		go h.tellNoLock(ctx, m) // on, or off again (also once the plugin is off)
		go h.tellCursor(ctx, m)
	}
	h.broadcastMachines()
}

// getComputer: GET /api/v1/machines/{mid}/computer, what the machine has of Cua Driver now (the machine page asks
// when it opens). 404 while the plugin is off or it is not a Mac.
func (h *Manager) getComputer(w http.ResponseWriter, r *http.Request, m *Machine) {
	c := h.refreshComputer(r.Context(), m)
	if c == nil {
		writeErr(w, http.StatusNotFound, i18n.Err("hub-computer.err_off"))
		return
	}
	writeJSON(w, i18n.ComputerStatus(h.requestLang(r), c))
}

// putComputer: PUT /api/v1/machines/{mid}/computer {"enabled": bool}, the machine page's switch: it goes into the
// plugin's machines.
func (h *Manager) putComputer(w http.ResponseWriter, r *http.Request, m *Machine) {
	var in struct {
		Enabled *bool `json:"enabled"`
		// "Don't auto-lock while the extension is on" for this Mac (the app asked first).
		NoAutoLock *bool `json:"no_auto_lock"`
	}
	if err := decodeJSONBody(w, r, &in, 4<<10); err != nil {
		h.writeComputerBodyErr(w, r, err)
		return
	}
	if !h.operable(m) {
		writeErr(w, http.StatusNotFound, i18n.Err("hub-computer.err_off"))
		return
	}
	if in.Enabled != nil {
		if err := h.setExtensionMachine(r.Context(), cuaExtensionID, m.cfg.ID, *in.Enabled); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if in.NoAutoLock != nil {
		if err := h.setExtensionList(r.Context(), cuaExtensionID, "no_auto_lock", m.cfg.ID, *in.NoAutoLock); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		h.logf("[%s] no auto lock %v", m.cfg.ID, *in.NoAutoLock)
	}
	writeJSON(w, i18n.ComputerStatus(h.requestLang(r), h.refreshComputer(r.Context(), m)))
}

// setExtensionMachine turns a per-Mac plugin on or off for a machine: its config's machines (the machine page's switch,
// the same as the plugin page's).
func (h *Manager) setExtensionMachine(ctx context.Context, extension, machine string, on bool) error {
	return h.setExtensionList(ctx, extension, "machines", machine, on)
}

// setExtensionList puts machine in, or takes it out of, the plugin's machine list key.
func (h *Manager) setExtensionList(ctx context.Context, extension, key, machine string, on bool) error {
	p := h.extensions.reg.Get(extension)
	if p == nil {
		return i18n.ErrEnglish("hub-computer.err_no_extension", i18n.Args{"extension": extension})
	}
	cfg := map[string]any{}
	_ = json.Unmarshal(h.extensions.reg.Config(p), &cfg)
	var list []string
	if raw, err := json.Marshal(cfg[key]); err == nil {
		_ = json.Unmarshal(raw, &list)
	}
	list = slices.DeleteFunc(list, func(id string) bool { return id == machine })
	if on {
		list = append(list, machine)
	}
	cfg[key] = list
	raw, _ := json.Marshal(cfg)
	return h.extensions.reg.SetConfig(ctx, extension, raw, "app")
}

// postComputerJob: POST /api/v1/machines/{mid}/computer/{install|grant|cancel}, the machine page's "Install Cua
// Driver", "Grant access" and "Cancel". The node runs the job in the background; its state comes back as Computer.job
// (the page asks again while it runs). Also "Lock" (lock: the node sleeps the display now) and the app's word that it
// is unlocking the Mac over its live screen (unlocking: only logged, who and which Mac; the password goes to the Mac
// alone), #1507 a-2.
func (h *Manager) postComputerJob(w http.ResponseWriter, r *http.Request, m *Machine) {
	action := r.PathValue("action")
	if action != "install" && action != "grant" && action != "cancel" && action != "lock" && action != "unlocking" {
		writeErr(w, http.StatusNotFound, i18n.ErrEnglish("hub-computer.err_unknown_action"))
		return
	}
	if c := h.computerOf(m); c == nil || !c.Enabled {
		writeErr(w, http.StatusNotFound, i18n.Err("hub-computer.err_not_enabled"))
		return
	}
	if action == "unlocking" {
		who := principalOf(r.Context()).Name
		if who == "" {
			who = "the hub's token"
		}
		h.logf("[%s] unlocked over its live screen from %s", m.cfg.ID, who)
		writeJSON(w, i18n.ComputerStatus(h.requestLang(r), h.computerOf(m)))
		return
	}
	if !m.isOnline() {
		writeErr(w, http.StatusConflict, i18n.Err("hub-computer.err_offline"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	asked := time.Now()
	var c proto.Computer
	if err := m.callBuiltin(ctx, "computer."+action, map[string]any{"backend": h.cua().backend()}, &c); err != nil {
		if strings.Contains(err.Error(), "unknown method") {
			err = i18n.Err("hub-computer." + map[bool]string{true: "err_outdated_lock", false: "err_outdated_install"}[action == "lock"])
		}
		h.writeComputerErr(w, r, http.StatusConflict, err)
		return
	}
	h.logf("[%s] computer %s", m.cfg.ID, action)
	if h.keepComputer(m, c, asked) {
		h.broadcastMachines()
	}
	writeJSON(w, i18n.ComputerStatus(h.requestLang(r), h.computerOf(m)))
}

// writeComputerErr localizes recognized node text only at the HTTP boundary, after RPC has erased Go error types.
// Keep apierr's status/code/retry metadata and the original error for clients with no language preference.
func (h *Manager) writeComputerErr(w http.ResponseWriter, r *http.Request, status int, err error) {
	l := h.requestLang(r)
	body := apierr.Body(status, "", err)
	body["error"] = i18n.ComputerText(l, i18n.Localize(err, l))
	writeErrorBody(w, status, body)
}

func (h *Manager) writeComputerBodyErr(w http.ResponseWriter, r *http.Request, err error) {
	var large *http.MaxBytesError
	if errors.As(err, &large) {
		writeErrCode(w, http.StatusRequestEntityTooLarge, proto.CodeTooLarge,
			i18n.ErrEnglish("hub-computer.err_body_large", i18n.Args{"limit": large.Limit}))
		return
	}
	writeErr(w, http.StatusBadRequest, i18n.ErrEnglish("hub-computer.err_bad_body", i18n.Args{"err": err.Error()}))
}

// ---- the assistant's tools ----
// Tool diagnostics below are model instructions and raw runtime output, not client interface copy.

// computerTools are the assistant's tools while the plugin is on for some machine.
func (h *Manager) computerTools() []proto.Tool {
	if s := h.cua(); !s.On || len(s.Machines) == 0 {
		return nil
	}
	return []proto.Tool{mcpTool("computer_state"), mcpTool("computer_run")}
}

func isComputerTool(name string) bool { return name == "computer_state" || name == "computer_run" }

// computerMost: the most of a tool's JSON the model gets back (a window's tree runs long; it can narrow with query).
const computerMost = 60000

// callComputerTool runs computer_state or computer_run for the assistant's conversation sessionID.
func (h *Manager) callComputerTool(ctx context.Context, sessionID, name string, args json.RawMessage) proto.ToolResult {
	var a struct {
		Machine    string          `json:"machine"`
		App        string          `json:"app"`
		Query      string          `json:"query"`
		Screenshot *bool           `json:"screenshot"`
		Script     string          `json:"script"`
		Steps      json.RawMessage `json:"steps"`
		Observe    json.RawMessage `json:"observe"`
		TimeoutMS  int             `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return proto.TextResult("bad arguments: "+err.Error(), true)
	}
	m, err := h.computerMachine(a.Machine)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	session := "persona-" + convID(sessionID)
	lcu := h.callBackend(m) == "lcu"
	declined := func(what string) proto.ToolResult {
		h.logf("[%s] %s declined on the phone (%s)", m.cfg.ID, name, what)
		return proto.TextResult("The user declined this on the phone. Do not retry it unless they ask.", true)
	}
	apps, whole := callApps(name, a.App, a.Script, a.Steps)
	if name == "computer_state" {
		if _, ok := h.approveApps(ctx, m, h.assistantAsker(sessionID, m.cfg.ID), name, apps, whole, args); !ok {
			return declined("look")
		}
		shot := a.Screenshot == nil || *a.Screenshot
		return h.computerState(ctx, m, session, sessionID, a.App, a.Query, shot)
	}
	var tool string
	in := map[string]any{}
	switch {
	case a.Script != "" && len(a.Steps) > 0:
		return proto.TextResult("give script or steps, not both", true)
	case a.Script != "":
		if lcu {
			tool, in["code"] = "js", a.Script
			if a.TimeoutMS > 0 {
				in["timeout_ms"] = a.TimeoutMS
			}
		} else {
			tool, in["script"] = "run_script", a.Script
			if a.TimeoutMS > 0 {
				in["timeout_ms"] = a.TimeoutMS
			}
		}
	case len(a.Steps) > 0:
		if lcu {
			code, err := lcuconfirm.StepsJS(a.App, a.Steps)
			if err != nil {
				return proto.TextResult(err.Error(), true)
			}
			tool, in["code"] = "js", code
		} else {
			tool, in["steps"] = "run_steps", a.Steps
			if a.App != "" {
				in["app"] = a.App
			}
			if len(a.Observe) > 0 {
				in["observe"] = a.Observe
			}
		}
	default:
		return proto.TextResult("give script (run_script) or steps (run_steps)", true)
	}
	what := computerSummary(h.userLang(), m.cfg.Name, tool, a.Script, a.Steps)
	asked, ok := h.approveApps(ctx, m, h.assistantAsker(sessionID, m.cfg.ID), name, apps, whole, args)
	if !ok || (h.cua().Confirm && !asked && !h.approve(ctx, h.assistantAsker(sessionID, m.cfg.ID), name, what, args)) {
		return declined(computerLogWhat(tool, a.Script, a.Steps))
	}
	start := time.Now()
	res, err := h.computerCall(ctx, m, session, sessionID, tool, in)
	h.logf("[%s] computer_run for %s: %s, %v, %s", m.cfg.ID, convID(sessionID), computerLogWhat(tool, a.Script, a.Steps),
		time.Since(start).Round(time.Millisecond), map[bool]string{true: "failed", false: "ok"}[err != nil])
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	return computerResult(res)
}

// computerMachine is the machine the assistant named, when the plugin is on for it, it is a Mac and it is online.
func (h *Manager) computerMachine(name string) (*Machine, error) {
	m := h.machineByName(strings.TrimSpace(name))
	if m == nil {
		return nil, fmt.Errorf("no machine %q (machines: %s)", name, h.computerMachineIDs())
	}
	if c := h.computerOf(m); c == nil || !c.Enabled {
		return nil, fmt.Errorf("operating %s is not turned on (on for: %s); the user turns it on on the machine's page in the app", m.cfg.ID, h.computerMachineIDs())
	}
	if !m.isOnline() {
		return nil, fmt.Errorf("%s is offline", m.cfg.ID)
	}
	return m, nil
}

func (h *Manager) computerMachineIDs() string {
	ids := h.cua().Machines
	if len(ids) == 0 {
		return "none"
	}
	return strings.Join(ids, ", ")
}

// computerState: one window of app (its accessibility tree, and its screenshot), or without app the whole screen and
// the apps and windows on it.
func (h *Manager) computerState(ctx context.Context, m *Machine, session, conversation, app, query string, shot bool) proto.ToolResult {
	if h.callBackend(m) == "lcu" {
		res, err := h.computerCall(ctx, m, session, conversation, "js", map[string]any{"code": lcuconfirm.StateJS(app, query)})
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		if !shot {
			res.Image = nil
		}
		return computerResult(res)
	}

	if app == "" {
		tree, err := h.computerCall(ctx, m, session, conversation, "get_accessibility_tree", map[string]any{})
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		out := computerResult(tree)
		if shot {
			desk, err := h.computerCall(ctx, m, session, conversation, "get_desktop_state", map[string]any{})
			if err != nil {
				return proto.TextResult(err.Error(), true)
			}
			if desk.Image != nil {
				out.Content = append(out.Content, proto.Content{Type: "image", Data: desk.Image.Data, MimeType: desk.Image.Mime})
			}
		}
		return out
	}
	// The window cua-driver's own lookup picks (the app's frontmost), then its state under the same session label, so
	// its element tokens work in computer_run.
	script := "const a = await cua.getApp(" + jsString(app) + "); return {pid: a.pid, window_id: a.windowId};"
	found, err := h.computerCall(ctx, m, session, conversation, "run_script", map[string]any{"script": script})
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	var r struct {
		OK    bool `json:"ok"`
		Value struct {
			PID      int `json:"pid"`
			WindowID int `json:"window_id"`
		} `json:"value"`
	}
	if json.Unmarshal(found.Output, &r) != nil || !r.OK || r.Value.WindowID == 0 {
		res := computerResult(found)
		res.IsError = true
		return res
	}
	in := map[string]any{"pid": r.Value.PID, "window_id": r.Value.WindowID, "include_screenshot": shot}
	if query != "" {
		in["query"] = query
	}
	state, err := h.computerCall(ctx, m, session, conversation, "get_window_state", in)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	return computerResult(state)
}

// callBackend is what this call should drive. Explicit lcu always does. auto does once a status has come back saying
// the Mac is on LCU; until then the node is told auto and decides itself, and the hub still speaks Cua Driver's tools
// (the node translates them when it picks LCU).
func (h *Manager) callBackend(m *Machine) string {
	switch h.cua().backend() {
	case "lcu":
		return "lcu"
	case "auto":
		if c := h.computerOf(m); c != nil && c.Backend == "lcu" {
			return "lcu"
		}
		return "auto"
	default:
		return "cua"
	}
}

// computerCall runs one computer-use tool on m's node.
func (h *Manager) computerCall(ctx context.Context, m *Machine, session, conversation, tool string, args map[string]any) (*proto.ComputerResult, error) {
	var res proto.ComputerResult
	s := h.cua()
	backend := h.callBackend(m)
	if err := m.callBuiltin(ctx, "computer.call", map[string]any{
		"session": session, "conversation": conversation, "tool": tool, "args": args,
		"backend": backend, "keep_awake": s.awake(), "click_sound": s.ClickSound,
	}, &res); err != nil {
		if backend == "lcu" && strings.Contains(err.Error(), "is not a tool") {
			return nil, errors.New(i18n.T(h.userLang(), "hub-computer.err_outdated_lcu"))
		}
		return nil, err
	}
	return &res, nil
}

// computerResult is a tool's answer for the model: its JSON (cut at computerMost), then its screenshot.
func computerResult(r *proto.ComputerResult) proto.ToolResult {
	text := r.Text
	if len(r.Output) > 0 {
		text = string(r.Output)
	}
	if len(text) > computerMost {
		cut := computerMost
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + fmt.Sprintf("\n… (%d more bytes: narrow it with query)", len(text)-cut)
	}
	out := proto.TextResult(text, false)
	if r.Image != nil {
		out.Content = append(out.Content, proto.Content{Type: "image", Data: r.Image.Data, MimeType: r.Image.Mime})
	}
	return out
}

// computerSummary is what the confirmation card says, in l: on which Mac, a script's first line or the steps' actions.
// script != "" covers Cua Driver's run_script and LCU's js (tool name differs; a.Script is still passed).
func computerSummary(l i18n.Lang, machine, tool, script string, steps json.RawMessage) string {
	if script != "" {

		line, _, _ := strings.Cut(strings.TrimSpace(script), "\n")
		return i18n.T(l, "hub-computer.confirm_script", i18n.Args{"machine": machine, "line": line})
	}
	return i18n.T(l, "hub-computer.confirm_steps", i18n.Args{"machine": machine,
		"steps": strings.Join(stepActions(steps), i18n.T(l, "hub-computer.list_sep"))})
}

// computerLogWhat is the log's account of a run: the kind and the actions, never what is typed or clicked.
func computerLogWhat(tool, script string, steps json.RawMessage) string {
	if script != "" {
		return fmt.Sprintf("run_script (%d bytes)", len(script))
	}
	return "run_steps [" + strings.Join(stepActions(steps), " ") + "]"
}

// stepActions are the tools of run_steps' steps: {"tool": "click", …} or {"click": {…}}; a check alone is "check".
func stepActions(steps json.RawMessage) []string {
	var list []map[string]json.RawMessage
	_ = json.Unmarshal(steps, &list)
	out := make([]string, 0, len(list))
	for _, s := range list {
		var tool string
		if raw, ok := s["tool"]; ok {
			_ = json.Unmarshal(raw, &tool)
		}
		for k := range s {
			if tool == "" && k != "args" && k != "wait_for" && k != "expect" && k != "tool" {
				tool = k
			}
		}
		if tool == "" {
			tool = "check"
		}
		out = append(out, tool)
	}
	return out
}

// jsString is s as a JavaScript string literal.
func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// convID is the start of a conversation's id: the Cua Driver session label of its calls, and how the log names it.
func convID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
