package node

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ClaudeSetting is one Claude Code /config option the app may change. Only these keys are read or written;
// everything else in settings.json (of each config directory, claudedirs.go) is preserved as is.
type ClaudeSetting struct {
	Key          string   `json:"key"`
	Title        string   `json:"title"`
	Detail       string   `json:"detail"`
	Type         string   `json:"type"` // bool | enum | string
	Options      []string `json:"options,omitempty"`
	Default      any      `json:"default"`
	Value        any      `json:"value"`                   // nil when unset (Claude Code uses the default)
	IgnoredValue string   `json:"ignored_value,omitempty"` // stored but not applied by Claude Code
}

var claudeSettings = []ClaudeSetting{
	{Key: "awaySummaryEnabled", Title: "回来时先说进展", Detail: "离开 5 分钟以上再回来时，Claude 先简单说说这段时间做了什么", Type: "bool", Default: true},
	{Key: "alwaysThinkingEnabled", Title: "每次都先想清楚", Detail: "每次回答前都先仔细想一想，更准，但更慢、更费额度", Type: "bool", Default: false},
	{Key: "enableWorkflows", Title: "动态工作流", Detail: "允许 Claude 一次安排好几个 Agent 一起干活，开 ultracode 要先打开它。不设时由账号方案决定，组织策略也可能不让用", Type: "bool", Default: nil},
	// settings.json accepts only low through xhigh; max belongs to session controls.
	// "auto" removes the key so Claude Code uses the model's default.
	{Key: "effortLevel", Type: "enum", Options: []string{effortAuto, "low", "medium", "high", "xhigh"}, Default: effortAuto},
	// The mode a session starts in when nothing picks one: the Agents page's "Follow" and a planned task the assistant
	// left unset (the hub reads it for the plan card, assistant/tasks_permission.go, #3026).
	{Key: proto.ClaudeDefaultModeSetting, Type: "enum", Options: permissionModes, Default: "default"},
	{Key: "language", Title: "回复语言", Detail: "Claude 回答用哪种语言，比如「中文」", Type: "string", Default: ""},
	{Key: "promptSuggestionEnabled", Title: "猜你下一句", Detail: "在输入框里先替你写出可能要说的下一句", Type: "bool", Default: true},
	{Key: "spinnerTipsEnabled", Title: "等待时的小提示", Detail: "Claude 干活时，在终端底部显示使用小技巧", Type: "bool", Default: true},
	{Key: "prefersReducedMotion", Title: "减少动画", Detail: "终端里少放动画", Type: "bool", Default: false},
	{Key: "autoUpdatesChannel", Title: "更新渠道", Detail: "Claude Code 自己更新时，装最新版还是稳定版", Type: "enum", Options: []string{"latest", "stable"}, Default: "latest"},
}

// claudeSettingsPath is settings.json of the config directory most running claudes use (claudedirs.go).
func (n *Node) claudeSettingsPath() string { return settingsFile(n.primaryClaudeDir()) }

// claudeSettingsPaths: settings.json of every config directory, each file once. A machine setting applies to all of
// them, whichever profile a claude runs with.
func (n *Node) claudeSettingsPaths() []string {
	var out []string
	for _, d := range n.claudeConfigDirs() {
		if p := settingsFile(d); !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	return out
}

func settingsFile(dir string) string {
	p := filepath.Join(dir, "settings.json")
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real // the file may be a symlink into a dotfiles repo; edit the target
	}
	return p
}

func (n *Node) readClaudeSettings() (map[string]any, error) {
	return readSettingsFile(n.claudeSettingsPath())
}

func readSettingsFile(path string) (map[string]any, error) {
	raw := map[string]any{}
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	}
	return raw, nil
}

// getClaudeSettings returns the editable options with their current values.
func (n *Node) getClaudeSettings(lang i18n.Lang) ([]ClaudeSetting, error) {
	raw, err := n.readClaudeSettings()
	if err != nil {
		return nil, err
	}
	out := make([]ClaudeSetting, len(claudeSettings))
	for i, s := range claudeSettings {
		s.Value = settingValue(raw, s.Key)
		if s.Key == proto.ClaudeDefaultModeSetting {
			s.Title = i18n.T(lang, "claude-settings.default_mode_title")
			s.Detail = i18n.T(lang, "claude-settings.default_mode_detail")
		}
		if s.Key == "effortLevel" {
			s.Title = i18n.T(lang, "claude-settings.effort_title")
			s.Detail = i18n.T(lang, "claude-settings.effort_detail")
			if value, ok := s.Value.(string); ok && value == "max" {
				// Reading must not rewrite the file or present an ignored default as applied.
				s.Value = nil
				s.IgnoredValue = value
				s.Detail = i18n.T(lang, "claude-settings.effort_max_ignored")
			}
		}
		out[i] = s
	}
	return out, nil
}

// setClaudeSetting validates and writes one option (value nil removes it, i.e. back to the default).
func (n *Node) setClaudeSetting(key string, value any) error {
	var def *ClaudeSetting
	for i := range claudeSettings {
		if claudeSettings[i].Key == key {
			def = &claudeSettings[i]
		}
	}
	if def == nil {
		return fmt.Errorf("setting %q is not editable from the app", key)
	}
	if value != nil {
		switch def.Type {
		case "bool":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be true or false", key)
			}
		case "enum":
			s, _ := value.(string)
			valid := false
			for _, o := range def.Options {
				valid = valid || o == s
			}
			if !valid {
				return fmt.Errorf("%s must be one of %v", key, def.Options)
			}
		case "string":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s must be a string", key)
			}
		}
	}
	if key == "effortLevel" && value == effortAuto {
		value = nil
	}
	for _, path := range n.claudeSettingsPaths() {
		if err := writeSetting(path, key, value); err != nil {
			return err
		}
	}
	return nil
}

// settingValue is key's value in raw (nil unset); a dotted key is one inside an object.
func settingValue(raw map[string]any, key string) any {
	if obj, leaf, nested := strings.Cut(key, "."); nested {
		m, _ := raw[obj].(map[string]any)
		return m[leaf]
	}
	return raw[key]
}

func writeSetting(path, key string, value any) error {
	raw, err := readSettingsFile(path)
	if err != nil {
		return err
	}
	if obj, leaf, nested := strings.Cut(key, "."); nested {
		m, isObject := raw[obj].(map[string]any)
		if raw[obj] != nil && !isObject {
			return fmt.Errorf("%s: %s is not an object", path, obj)
		}
		if value == nil || value == "" {
			delete(m, leaf) // the object stays, with whatever else it holds (permissions.allow …)
		} else {
			if m == nil {
				m = map[string]any{}
			}
			m[leaf] = value
			raw[obj] = m
		}
	} else if value == nil || value == "" {
		delete(raw, key)
	} else {
		raw[key] = value
	}
	b, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".persona.tmp"
	mode := os.FileMode(0o644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	if err := os.WriteFile(tmp, append(b, '\n'), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
