package node

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Antigravity CLI (agy) settings in ~/.gemini/antigravity-cli/settings.json, and Oh My Pi (omp) settings in
// ~/.omp/agent/config.yml. Both follow ClaudeSetting's shape so AgentSettings in the web / app renders them.

var agySettings = []ClaudeSetting{
	{Key: "model", Title: "默认模型", Detail: "没指定模型时 Antigravity 用哪个，比如 Gemini 4 Argon (High)", Type: "string", Default: "Gemini 4 Argon (High)"},
	{Key: "agentMode", Title: "执行模式", Detail: "改文件、跑命令时的默认模式；选 auto 会自动放行所有权限检查", Type: "enum", Options: []string{"default", "accept-edits", "auto", "plan"}, Default: "default"},
	{Key: "toolPermission", Title: "工具权限", Detail: "调用工具或跑命令前要不要问你；选 always-proceed 会自动放行所有权限检查（全部放行）", Type: "enum", Options: []string{"ask", "always-proceed"}, Default: "ask"},
	{Key: "modelProvider", Title: "模型接入方式", Detail: "oauth 走 Google / Antigravity 登录；填了 GOOGLE_GEMINI_BASE_URL + GEMINI_API_KEY 时选 gemini", Type: "enum", Options: []string{"oauth", "gemini"}, Default: "oauth"},
	{Key: "showFeedbackSurvey", Title: "显示反馈问卷", Detail: "在终端里偶尔弹出反馈调查", Type: "bool", Default: true},
}

func (n *Node) agyConfigPath() string {
	dir := os.Getenv("ANTIGRAVITY_CLI_CONFIG_DIR")
	if dir == "" {
		dir = filepath.Join(n.home, ".gemini", "antigravity-cli")
	}
	p := filepath.Join(dir, "settings.json")
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

func (n *Node) getAgySettings() ([]ClaudeSetting, error) {
	raw, err := readSettingsFile(n.agyConfigPath())
	if err != nil {
		return nil, err
	}
	out := make([]ClaudeSetting, len(agySettings))
	for i, s := range agySettings {
		s.Value = raw[s.Key]
		out[i] = s
	}
	return out, nil
}

func (n *Node) setAgySetting(key string, value any) error {
	if key == "agentMode" && value == "bypassPermissions" {
		value = "auto" // older clients used Persona's permission name in the native settings field
	}
	var def *ClaudeSetting
	for i := range agySettings {
		if agySettings[i].Key == key {
			def = &agySettings[i]
		}
	}
	if def == nil {
		return fmt.Errorf("setting %q is not editable from the app", key)
	}
	if err := checkSettingValue(*def, value); err != nil {
		return err
	}
	if key == "modelProvider" && value == "oauth" {
		value = nil
	}
	return writeSetting(n.agyConfigPath(), key, value)
}

var ompSettings = []ClaudeSetting{
	{Key: "modelRoles.default", Title: "默认模型", Detail: "omp 默认使用的模型，如 google-antigravity/gemini-4-argon:high", Type: "string", Default: ""},
	{Key: "defaultThinkingLevel", Title: "思考强度", Detail: "默认思考等级；auto 由模型自己决定", Type: "enum", Options: []string{"auto", "off", "minimal", "low", "medium", "high", "xhigh", "max"}, Default: "auto"},
	{Key: "steeringMode", Title: "插话模式", Detail: "模型正在工作时发消息怎么排队", Type: "enum", Options: []string{"one-at-a-time", "immediate", "after-turn"}, Default: "one-at-a-time"},
	{Key: "hideThinkingBlock", Title: "隐藏思考过程", Detail: "在终端里折叠思考块（不影响模型思考）", Type: "bool", Default: false},
	{Key: "computer.enabled", Title: "操控电脑 (Computer Use)", Detail: "允许 omp 在 eval 中使用桌面自动化工具", Type: "bool", Default: true},
	{Key: "github.enabled", Title: "GitHub 工具", Detail: "启用内置 GitHub issue / PR / 代码工具", Type: "bool", Default: true},
	{Key: "astGrep.enabled", Title: "AST Grep 工具", Detail: "启用语法树结构搜索与重构工具", Type: "bool", Default: true},
	{Key: "autolearn.enabled", Title: "自动学习 (Autolearn)", Detail: "会话结束后自动提炼记忆", Type: "bool", Default: false},
}

func (n *Node) ompConfigPath() string {
	p := filepath.Join(n.home, ".omp", "agent", "config.yml")
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

func (n *Node) getOmpSettings() ([]ClaudeSetting, error) {
	b, err := os.ReadFile(n.ompConfigPath())
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	vals := yamlSimpleParse(string(b))
	out := make([]ClaudeSetting, len(ompSettings))
	for i, s := range ompSettings {
		if v, ok := vals[s.Key]; ok {
			s.Value = v
		}
		out[i] = s
	}
	return out, nil
}

func (n *Node) setOmpSetting(key string, value any) error {
	var def *ClaudeSetting
	for i := range ompSettings {
		if ompSettings[i].Key == key {
			def = &ompSettings[i]
		}
	}
	if def == nil {
		return fmt.Errorf("setting %q is not editable from the app", key)
	}
	if err := checkSettingValue(*def, value); err != nil {
		return err
	}
	path := n.ompConfigPath()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := yamlSimpleSet(string(b), key, value)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	mode := os.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp := path + ".persona.tmp"
	if err := os.WriteFile(tmp, []byte(text), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func yamlSimpleParse(text string) map[string]any {
	out := map[string]any{}
	var section string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		k, v, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if indent == 0 {
			if v == "" {
				section = k
				continue
			}
			section = ""
			if parsed, ok := yamlScalar(v); ok {
				out[k] = parsed
			}
		} else if indent == 2 && section != "" && v != "" {
			if parsed, ok := yamlScalar(v); ok {
				out[section+"."+k] = parsed
			}
		}
	}
	return out
}

func yamlScalar(raw string) (any, bool) {
	if i := strings.Index(raw, " #"); i >= 0 {
		raw = strings.TrimSpace(raw[:i])
	}
	switch raw {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	if len(raw) >= 2 && ((raw[0] == '"' && raw[len(raw)-1] == '"') || (raw[0] == '\'' && raw[len(raw)-1] == '\'')) {
		return raw[1 : len(raw)-1], true
	}
	return raw, raw != ""
}

func yamlFormatScalar(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case string:
		if strings.ContainsAny(x, ":#{}[]&*!|>'\"%@`") || strings.TrimSpace(x) != x {
			return fmt.Sprintf("%q", x)
		}
		return x
	default:
		return fmt.Sprint(v)
	}
}

func yamlSimpleSet(text, key string, value any) string {
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	remove := value == nil || value == ""
	parent, child, nested := strings.Cut(key, ".")
	if !nested {
		prefix := key + ":"
		for i, line := range lines {
			if len(line)-len(strings.TrimLeft(line, " ")) == 0 && strings.HasPrefix(strings.TrimSpace(line), prefix) {
				if remove {
					lines = append(lines[:i], lines[i+1:]...)
				} else {
					lines[i] = key + ": " + yamlFormatScalar(value)
				}
				return strings.Join(lines, "\n") + "\n"
			}
		}
		if !remove {
			lines = append(lines, key+": "+yamlFormatScalar(value))
		}
		return strings.Join(lines, "\n") + "\n"
	}

	secHeader := parent + ":"
	secIdx := -1
	for i, line := range lines {
		if len(line)-len(strings.TrimLeft(line, " ")) == 0 && strings.TrimSpace(line) == secHeader {
			secIdx = i
			break
		}
	}
	if secIdx >= 0 {
		endIdx := len(lines)
		for j := secIdx + 1; j < len(lines); j++ {
			trimmed := strings.TrimSpace(lines[j])
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			indent := len(lines[j]) - len(strings.TrimLeft(lines[j], " "))
			if indent == 0 {
				endIdx = j
				break
			}
			if indent == 2 && strings.HasPrefix(trimmed, child+":") {
				if remove {
					lines = append(lines[:j], lines[j+1:]...)
				} else {
					lines[j] = "  " + child + ": " + yamlFormatScalar(value)
				}
				return strings.Join(lines, "\n") + "\n"
			}
		}
		if !remove {
			next := append([]string{}, lines[:endIdx]...)
			next = append(next, "  "+child+": "+yamlFormatScalar(value))
			next = append(next, lines[endIdx:]...)
			lines = next
		}
		return strings.Join(lines, "\n") + "\n"
	}
	if !remove {
		lines = append(lines, parent+":", "  "+child+": "+yamlFormatScalar(value))
	}
	return strings.Join(lines, "\n") + "\n"
}
