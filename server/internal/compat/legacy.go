// Package compat contains only the pre-cutover runtime identities required for
// rolling upgrades. Do not use these names for product copy or rewrite user data.
package compat

import (
	"net/http"
	"regexp"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	Feature             = nodeproto.PersonaContracts
	StateDir            = ".jarvis"
	MCPServer           = "jarvis"
	SettingsTool        = "jarvis_settings"
	VoiceTool           = "ask_jarvis"
	RuntimeProtocol     = "jarvis-runtime.v1"
	NodeUpgrade         = "jarvis-node"
	PushInfo            = "jarvis push v1"
	GrantAAD            = "jarvis relay grant v1"
	ActivityAttributes  = "JarvisActivityAttributes"
	WakeField           = "jarvis_wake"
	StartedField        = "started_by_jarvis"
	DiscoveryService    = "_jarvis-hub._tcp"
	HubService          = "jarvisd"
	HubSocket           = "jarvisd.socket"
	HubLabel            = "com.jarvis.hub"
	NodeLabel           = "com.jarvis.node"
	PortProbeAgent      = "jarvis-ports"
	ResidentNodePattern = "([j]arvisd|[p]ersonad|[p]ersona-node) node --attach"
	DBContainer         = "jarvis-postgres"
	DBVolume            = "jarvis-pgdata"
	DBName              = "jarvis"
	DaemonBinary        = "jarvisd"
	ConnectorBinary     = "jarvis-link"
)

// EnvNames preserves canonical precedence, including explicitly empty values.
func EnvNames(name string) (current, legacy string) {
	if strings.HasPrefix(name, "PERSONA_") {
		return name, "JARVIS_" + strings.TrimPrefix(name, "PERSONA_")
	}
	if strings.HasPrefix(name, "JARVIS_") {
		return "PERSONA_" + strings.TrimPrefix(name, "JARVIS_"), name
	}
	return name, name
}
func ReservedEnv(name string) bool {
	return strings.HasPrefix(name, "PERSONA_") || strings.HasPrefix(name, "JARVIS_")
}
func StateSubpath(rel string) (string, bool) {
	for _, root := range []string{".persona", StateDir} {
		if sub, ok := strings.CutPrefix(rel, root+"/"); ok {
			return sub, true
		}
	}
	return "", false
}
func IsStateDir(name string) bool  { return name == ".persona" || name == StateDir }
func IsMCPServer(name string) bool { return name == "persona" || name == MCPServer }
func Tool(name string) string {
	if name == SettingsTool {
		return "persona_settings"
	}
	if name == VoiceTool {
		return "ask_persona"
	}
	return name
}
func LegacyTool(name string) string {
	if name == "persona_settings" {
		return SettingsTool
	}
	if name == "ask_persona" {
		return VoiceTool
	}
	return name
}
func Source(name string) string { return proto.CanonicalLauncherSource(name) }

var runtimePackages = [][2]string{{"jarvis-dsh-runtime", "persona-dsh-runtime"}, {"jarvis-pi-runtime", "persona-pi-runtime"}, {"jarvis-paseo-runtime", "persona-paseo-runtime"}, {"jarvis-agent-sdk-runtime", "persona-agent-sdk-runtime"}}

func RuntimePackage(name string) string {
	for _, pair := range runtimePackages {
		if name == pair[0] {
			return pair[1]
		}
	}
	return name
}
func LegacyRuntimePackage(name string) string {
	for _, pair := range runtimePackages {
		if name == pair[1] {
			return pair[0]
		}
	}
	return name
}

const NodeUpdateLock = ".jarvis-update.lock"

// LauncherCheckDir is the temp directory prefix older nodes ran launcher health checks in; their transcripts stay
// under Claude Code's projects for days after an upgrade.
const LauncherCheckDir = "jarvis-check-"

func LegacyURL(value string) string {
	if strings.HasPrefix(value, "persona:") {
		return "jarvis:" + strings.TrimPrefix(value, "persona:")
	}
	return value
}
func LegacySource(name string) string { return proto.LegacyLauncherSource(name) }
func HeaderName(name string) string   { return strings.Replace(name, "X-Persona-", "X-Jarvis-", 1) }

// Header reads the canonical value first. Callers must reject ambiguous aliases
// for security-sensitive headers before authorization or side-effect admission.
func Header(h http.Header, name string) string {
	if v := h.Get(name); v != "" {
		return v
	}
	return h.Get(HeaderName(name))
}
func AdminCookieNames(secure bool) []string {
	if secure {
		return []string{"persona_admin_tls", "jarvis_admin_tls"}
	}
	return []string{"persona_admin", "jarvis_admin"}
}
func DelHeader(h http.Header, name string)        { h.Del(name); h.Del(HeaderName(name)) }
func SetHeader(h http.Header, name, value string) { h.Set(name, value); h.Set(HeaderName(name), value) }
func HeaderConflict(h http.Header, name string) bool {
	a, b := h.Get(name), h.Get(HeaderName(name))
	return a != "" && b != "" && a != b
}
func ContextPattern() string { return `(?:jarvis|persona)-context` }
func ContextNames() []string { return []string{"jarvis-context", "persona-context"} }

// compat is also built on its own, copied without the rest of the server (scripts/test/test_persona_migration_edges.py),
// so it uses the standard library only: its patterns compile on first use with sync.OnceValue, not internal/lazyre.
var currentContextTags = lazy(`(</?)persona-(context|file)([[:space:]/>])`)

// lazy is regexp.MustCompile(src) on first use.
func lazy(src string) func() *regexp.Regexp {
	return sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(src) })
}

func LegacyContextTags(s string) string {
	return currentContextTags().ReplaceAllString(s, `${1}jarvis-${2}${3}`)
}
func AssistantSuffixes() []string {
	return []string{"--persona-assistant", "--jarvis-assistant", "--persona-health", "--jarvis-health"}
}
func IsAssistantTranscript(path string) bool {
	return strings.Contains(path, "-jarvis-assistant") || strings.Contains(path, "-persona-assistant")
}
func ReplyHashPrefix() string { return "jarvis:reply:" }
func CodexHashPrefix() string { return "jarvis/codex/thread/v1\x00" }
func LegacyBotCommand(cmd string) string {
	if cmd == "/jarvis" {
		return "/persona"
	}
	return cmd
}
func LegacyManagerUnit(id string) string  { return "jarvis-manager-" + id + ".service" }
func LegacyManagerLabel(id string) string { return "com.jarvis.manager." + id }
func LegacyHubDNS(id string) string       { return id + ".jarvis.invalid" }
func Looks(name string) string {
	switch name {
	case "jarvis":
		return "persona-visor"
	case "jarvis-circuit":
		return "persona-circuit"
	case "jarvis-pixel":
		return "persona-pixel"
	case "jarvis-facet":
		return "persona-facet"
	case "jarvis-blob":
		return "persona-blob"
	}
	return name
}
