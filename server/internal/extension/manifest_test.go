package extension

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// exampleManifest is the plan's example (docs/plans/plan-extensions.md "3"), a made-up notes service.
const exampleManifest = `{
  "manifest_version": 1,
  "id": "example-notes",
  "name": "Example Notes",
  "version": "1.2.0",
  "description": "notes into the knowledge base",
  "publisher": "example",
  "homepage": "https://example.com/persona-plugin",
  "engines": {"persona": ">=202611050000", "plugin_protocol": 1},
  "runtime": {"command": {"linux-amd64": "bin/notes-linux", "darwin-arm64": "bin/notes-mac"}, "args": ["serve"]},
  "activation_events": ["on_startup"],
  "contributes": {"connectors": [{"id": "examplenotes", "name": "Example Notes", "capabilities": ["status", "tools"], "hosts": ["notes.example.com"], "poll_seconds": 600}]},
  "configuration": {"type": "object", "properties": {
    "base_url": {"type": "string", "title": "服务地址", "format": "uri", "default": "https://notes.example.com"},
    "folders": {"type": "array", "title": "文件夹", "items": {"type": "string"}},
    "include_shared": {"type": "boolean", "default": false}
  }, "required": ["base_url"]},
  "secrets": [{"name": "api_key", "title": "API Key", "required": true}],
  "permissions": {"network": ["notes.example.com"]}
}`

// with is the example manifest with one change made to its decoded form.
func with(t *testing.T, change func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(exampleManifest), &m); err != nil {
		t.Fatal(err)
	}
	change(m)
	b, _ := json.Marshal(m)
	return b
}

func TestParseExample(t *testing.T) {
	m, err := Parse([]byte(exampleManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "example-notes" || m.Kind() != "connectors" || len(m.Connectors()) != 1 || m.Connectors()[0].PollSeconds != 600 {
		t.Fatalf("%+v", m)
	}
	if cmd, ok := m.Runtime.Command.For("linux-amd64"); !ok || cmd != "bin/notes-linux" {
		t.Fatal(cmd)
	}
	if _, ok := m.Runtime.Command.For("windows-amd64"); ok {
		t.Fatal("no windows program")
	}
	if got := m.Contributions(); len(got["connectors"]) != 1 || got["connectors"][0] != "examplenotes" {
		t.Fatal(got)
	}
	if w := m.Lint(); len(w) != 0 {
		t.Fatal(w)
	}
}

// confirm_always is an optional list of action patterns on a connector, kept as written.
func TestConnectorConfirmAlways(t *testing.T) {
	m, err := Parse(with(t, func(m map[string]any) {
		c := m["contributes"].(map[string]any)["connectors"].([]any)[0].(map[string]any)
		c["confirm_always"] = []any{"examplenotes_send", "examplenotes_*_reply"}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Connectors()[0].ConfirmAlways; len(got) != 2 || got[0] != "examplenotes_send" || got[1] != "examplenotes_*_reply" {
		t.Fatalf("%v", got)
	}
	if plain, err := Parse([]byte(exampleManifest)); err != nil || len(plain.Connectors()[0].ConfirmAlways) != 0 {
		t.Fatalf("not declared: %v %v", plain.Connectors()[0].ConfirmAlways, err)
	}
}

func TestParseRefuses(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"unknown top-level field": func(m map[string]any) { m["permission"] = map[string]any{} },
		"manifest version":        func(m map[string]any) { m["manifest_version"] = 2 },
		"bad id":                  func(m map[string]any) { m["id"] = "Bad ID" },
		"id starting with -":      func(m map[string]any) { m["id"] = "-x" },
		"no name":                 func(m map[string]any) { m["name"] = " " },
		"version":                 func(m map[string]any) { m["version"] = "1.2" },
		"homepage http":           func(m map[string]any) { m["homepage"] = "http://example.com" },
		"engines range":           func(m map[string]any) { m["engines"] = map[string]any{"persona": "~1", "plugin_protocol": 1} },
		"protocol":                func(m map[string]any) { m["engines"] = map[string]any{"plugin_protocol": 9} },
		"absolute command":        func(m map[string]any) { m["runtime"].(map[string]any)["command"] = "/usr/bin/x" },
		"command outside":         func(m map[string]any) { m["runtime"].(map[string]any)["command"] = "../x" },
		"command unclean":         func(m map[string]any) { m["runtime"].(map[string]any)["command"] = "bin/../../x" },
		"platform key":            func(m map[string]any) { m["runtime"].(map[string]any)["command"] = map[string]any{"linux": "x"} },
		"rpc value":               func(m map[string]any) { m["runtime"].(map[string]any)["rpc"] = "tcp" },
		"no way to talk":          func(m map[string]any) { m["runtime"].(map[string]any)["rpc"] = "none" },
		"hub's env": func(m map[string]any) {
			m["runtime"].(map[string]any)["env"] = map[string]any{"PERSONA_HUB_TOKEN": "x"}
		},
		"loader's env": func(m map[string]any) {
			m["runtime"].(map[string]any)["env"] = map[string]any{"LD_PRELOAD": "./x.so"}
		},
		"macOS loader's env": func(m map[string]any) {
			m["runtime"].(map[string]any)["env"] = map[string]any{"DYLD_INSERT_LIBRARIES": "./x.dylib"}
		},
		"activation":           func(m map[string]any) { m["activation_events"] = []any{"onStartup"} },
		"on_http without http": func(m map[string]any) { m["activation_events"] = []any{"on_http"} },
		"nothing contributed":  func(m map[string]any) { m["contributes"] = map[string]any{} },
		"connector id": func(m map[string]any) {
			m["contributes"] = map[string]any{"connectors": []any{map[string]any{"id": "Ex", "name": "x", "capabilities": []any{"status"}}}}
		},
		"connector field": func(m map[string]any) {
			m["contributes"] = map[string]any{"connectors": []any{map[string]any{"id": "ex", "name": "x", "capabilities": []any{"status"}, "pol": 1}}}
		},
		"confirm_always pattern": func(m map[string]any) {
			m["contributes"] = map[string]any{"connectors": []any{map[string]any{"id": "ex", "name": "x", "capabilities": []any{"status"}, "confirm_always": []any{"ex_[send"}}}}
		},
		"confirm_always empty": func(m map[string]any) {
			m["contributes"] = map[string]any{"connectors": []any{map[string]any{"id": "ex", "name": "x", "capabilities": []any{"status"}, "confirm_always": []any{""}}}}
		},
		"connector archive source": func(m map[string]any) {
			m["contributes"] = map[string]any{"connectors": []any{map[string]any{"id": "ex", "name": "x", "capabilities": []any{"archive"}, "archive_sources": []any{"Posts"}}}}
		},
		"connector archive source its own id": func(m map[string]any) {
			m["contributes"] = map[string]any{"connectors": []any{map[string]any{"id": "ex", "name": "x", "capabilities": []any{"archive"}, "archive_sources": []any{"ex"}}}}
		},
		"connector twice": func(m map[string]any) {
			c := map[string]any{"id": "ex", "name": "x", "capabilities": []any{"status"}}
			m["contributes"] = map[string]any{"connectors": []any{c, c}}
		},
		"connectors without rpc": func(m map[string]any) {
			m["runtime"] = map[string]any{"command": "bin/x", "rpc": "none", "http": true}
			m["activation_events"] = []any{"on_startup"}
		},
		"voice without http": func(m map[string]any) {
			m["contributes"] = map[string]any{"voice": []any{map[string]any{"id": "voice"}}}
		},
		"connectors without runtime": func(m map[string]any) { delete(m, "runtime"); delete(m, "activation_events") },
		"secret name":                func(m map[string]any) { m["secrets"] = []any{map[string]any{"name": "Key", "title": "k"}} },
		"secret twice": func(m map[string]any) {
			s := map[string]any{"name": "k", "title": "k"}
			m["secrets"] = []any{s, s}
		},
		"secret without title": func(m map[string]any) { m["secrets"] = []any{map[string]any{"name": "k"}} },
		"network with scheme":  func(m map[string]any) { m["permissions"] = map[string]any{"network": []any{"https://example.com"}} },
		"unknown permission":   func(m map[string]any) { m["permissions"] = map[string]any{"files": true} },
		"unknown hub scope":    func(m map[string]any) { m["permissions"] = map[string]any{"hub": []any{"kb.delete"}} },
		"bad extension to call": func(m map[string]any) {
			m["permissions"] = map[string]any{"hub": []any{"extensions.call:Bad ID"}}
		},
		"schema keyword": func(m map[string]any) {
			m["configuration"] = map[string]any{"type": "object", "properties": map[string]any{"a": map[string]any{"type": "string", "$ref": "#"}}}
		},
		"something after": nil,
	}
	for name, change := range cases {
		var data []byte
		if change == nil {
			data = []byte(exampleManifest + "{}")
		} else {
			data = with(t, change)
		}
		if _, err := Parse(data); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestLintWarnsUnknownPointAndSecretLookingConfig(t *testing.T) {
	m, err := Parse(with(t, func(m map[string]any) {
		m["contributes"].(map[string]any)["kb_sources"] = []any{map[string]any{"id": "q"}}
		m["configuration"] = map[string]any{"type": "object", "properties": map[string]any{"api_token": map[string]any{"type": "string"}}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(m.Lint(), "\n")
	if !strings.Contains(w, "contributes.kb_sources") || !strings.Contains(w, "api_token") {
		t.Fatal(w)
	}
	if _, ok := m.Contributions()["kb_sources"]; ok {
		t.Fatal("an unknown point does not take effect")
	}
}

func TestRuntimeDefaults(t *testing.T) {
	m, err := Parse(with(t, func(m map[string]any) { delete(m, "activation_events") }))
	if err != nil {
		t.Fatal(err)
	}
	if a := m.Activation(); len(a) != 1 || a[0] != OnCall {
		t.Fatal(a)
	}
	if !m.Runtime.SpeaksRPC() {
		t.Fatal("stdio is the default")
	}
	// A program serving HTTP only must run on_startup: the hub has no other way to know it is up.
	if _, err := Parse(with(t, func(m map[string]any) {
		m["runtime"] = map[string]any{"command": "bin/x", "rpc": "none", "http": true}
		delete(m, "activation_events")
		m["contributes"] = map[string]any{"voice": []any{map[string]any{"id": "voice"}}}
	})); err == nil || !strings.Contains(err.Error(), "on_startup") {
		t.Fatal(err)
	}
}

func TestDigestAndCovers(t *testing.T) {
	a, _ := Parse([]byte(exampleManifest))
	b, _ := Parse(with(t, func(m map[string]any) {
		m["permissions"] = map[string]any{"network": []any{"notes.example.com", "notes.example.com"}}
		m["version"] = "2.0.0"
	}))
	if Digest(a) != Digest(b) {
		t.Fatal("the same permissions (hosts once each) and code: the same digest, whatever the version")
	}
	c, _ := Parse(with(t, func(m map[string]any) {
		m["permissions"] = map[string]any{"network": []any{"notes.example.com"}, "hub_api": true}
	}))
	if Digest(a) == Digest(c) {
		t.Fatal("more permissions, another digest")
	}
	if !c.Permissions.Covers(a.Permissions) || a.Permissions.Covers(c.Permissions) {
		t.Fatal("covers")
	}
	lines := Explain(c)
	keys := []string{}
	for _, l := range lines {
		keys = append(keys, l.Key)
	}
	if strings.Join(keys, ",") != "runs_code,network,hub_api,secrets" {
		t.Fatal(keys)
	}
	if !strings.Contains(strings.Join(c.Lint(), "\n"), "hub_api is the old way") {
		t.Fatal("the full token asked the old way warns")
	}
}

// Scopes of the hub's API: shown by what they open, in a digest of their own, an upgrade asking for one more says so.
func TestHubScopes(t *testing.T) {
	a, _ := Parse([]byte(exampleManifest))
	s, err := Parse(with(t, func(m map[string]any) {
		m["permissions"] = map[string]any{"network": []any{"notes.example.com"}, "hub": []any{"inbox.read", "kb.read", "kb.read", "extensions.call:other"}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if Digest(a) == Digest(s) {
		t.Fatal("scopes are in the digest")
	}
	var hub PermissionLine
	for _, l := range Explain(s) {
		if l.Key == "hub_api" {
			t.Fatal("scopes are not the full token")
		}
		if l.Key == "hub" {
			hub = l
		}
	}
	if hub.Detail != "读知识库；读收件箱；调用扩展「other」的接口" {
		t.Fatalf("%+v", hub)
	}
	if len(s.Lint()) != 0 {
		t.Fatal(s.Lint())
	}
	more, _ := Parse(with(t, func(m map[string]any) {
		m["permissions"] = map[string]any{"network": []any{"notes.example.com"}, "hub": []any{"inbox.read", "kb.read", "agents.control"}}
	}))
	added := Added(s, more)
	if len(added) != 1 || added[0].Key != "hub" || added[0].Detail != "驱动 Agent（新建、发消息、打断、回答提问和批准）" {
		t.Fatalf("%+v", added)
	}
	if s.Permissions.Covers(more.Permissions) || !more.Permissions.Covers(a.Permissions) {
		t.Fatal("covers")
	}
}

func TestVersions(t *testing.T) {
	order := []string{"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0", "1.0.1", "1.10.0", "2.0.0"}
	for i := 1; i < len(order); i++ {
		if !NewerVersion(order[i], order[i-1]) || NewerVersion(order[i-1], order[i]) {
			t.Errorf("%s > %s", order[i], order[i-1])
		}
	}
	for _, bad := range []string{"1", "1.0", "01.0.0", "1.0.0+build", "v1.0.0"} {
		if _, err := ParseVersion(bad); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

func TestRanges(t *testing.T) {
	r, err := ParseRange(">=100 <200")
	if err != nil {
		t.Fatal(err)
	}
	for build, want := range map[int64]bool{99: false, 100: true, 199: true, 200: false} {
		if r.Allows(build) != want {
			t.Errorf("%d", build)
		}
	}
	for _, bad := range []string{"", "=100", "<=100", ">100", ">=x"} {
		if _, err := ParseRange(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	m, _ := Parse([]byte(exampleManifest))
	if _, err := CheckEngines(m, 202611040000); err == nil {
		t.Fatal("too old a hub")
	}
	if w, err := CheckEngines(m, 0); err != nil || w == "" {
		t.Fatal("a development build runs it, with a warning")
	}
	if w, err := CheckEngines(m, 202611060000); err != nil || w != "" {
		t.Fatal(w, err)
	}
}

func TestTargeted(t *testing.T) {
	b, err := Targeted(struct {
		Cursor string `json:"cursor"`
	}{"c1"}, "notes")
	if err != nil || string(b) != `{"contribution":"notes","cursor":"c1"}` {
		t.Fatal(string(b), err)
	}
	if b, _ := Targeted(struct{}{}, ""); string(b) != `{}` {
		t.Fatal(string(b))
	}
	if b, _ := Targeted(nil, "notes"); string(b) != `{"contribution":"notes"}` {
		t.Fatal(string(b))
	}
	if _, err := Targeted([]int{1}, "notes"); err == nil {
		t.Fatal("not an object")
	}
}

func TestQuotaAndProviders(t *testing.T) {
	quota := func(perm bool, item map[string]any) []byte {
		return with(t, func(m map[string]any) {
			m["contributes"] = map[string]any{"quota": []any{item}}
			m["permissions"] = map[string]any{"launcher_keys": perm}
		})
	}
	ok := map[string]any{"id": "example-gw", "name": "Example 网关", "hosts": []any{"gw.example.com", "*.example.net"}}
	m, err := Parse(quota(true, ok))
	if err != nil {
		t.Fatal(err)
	}
	if q := m.Quotas(); len(q) != 1 || q[0].Hosts[1] != "*.example.net" || m.Kind() != "quota" {
		t.Fatalf("%+v", q)
	}
	lines := Explain(m)
	if lines[len(lines)-2].Key != "launcher_keys" {
		t.Fatal(lines)
	}
	if _, err := Parse(quota(false, ok)); err == nil || !strings.Contains(err.Error(), "launcher_keys") {
		t.Fatal("quota without launcher_keys:", err)
	}
	if _, err := Parse(quota(true, map[string]any{"id": "x", "name": "x", "hosts": []any{"https://x"}})); err == nil {
		t.Fatal("a host with a scheme")
	}
	provider := func(item map[string]any, runtime bool) []byte {
		return with(t, func(m map[string]any) {
			m["contributes"] = map[string]any{"providers": []any{item}}
			delete(m, "permissions")
			if !runtime {
				delete(m, "runtime")
				delete(m, "activation_events")
				delete(m, "engines")
			}
		})
	}
	p := map[string]any{"id": "example-ai", "name": "Example AI", "kind": "api", "api": "responses", "base_url": "https://api.example.com/v1",
		"models": []any{map[string]any{"id": "example-large", "name": "Large"}, map[string]any{"id": "example-small"}}, "model": "example-large"}
	m, err = Parse(provider(p, false))
	if err != nil {
		t.Fatal("a declarative provider:", err)
	}
	if pr := m.Providers(); len(pr) != 1 || len(pr[0].Models) != 2 || m.Runtime != nil {
		t.Fatalf("%+v", pr)
	}
	for _, api := range []string{"messages", "responses"} {
		item := maps.Clone(p)
		item["api"] = api
		if _, err := Parse(provider(item, false)); err != nil {
			t.Fatalf("%s provider: %v", api, err)
		}
	}
	for name, change := range map[string]func(map[string]any){
		"base_url":        func(i map[string]any) { i["base_url"] = "ftp://x" },
		"kind":            func(i map[string]any) { i["kind"] = "proxy" },
		"api":             func(i map[string]any) { i["api"] = "unknown" },
		"chat api":        func(i map[string]any) { i["api"] = "chat" },
		"chat launcher":   func(i map[string]any) { i["api"], i["kind"] = "chat", "launcher" },
		"model twice":     func(i map[string]any) { i["models"] = []any{map[string]any{"id": "a"}, map[string]any{"id": "a"}} },
		"dynamic, no rpc": func(i map[string]any) { i["dynamic_models"] = true },
	} {
		item := maps.Clone(p)
		change(item)
		if _, err := Parse(provider(item, false)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	p["dynamic_models"] = true
	if _, err := Parse(provider(p, true)); err != nil {
		t.Fatal("dynamic models with a program:", err)
	}
}

func TestAPIAndAssistantPoints(t *testing.T) {
	m, err := Parse(with(t, func(m map[string]any) {
		m["runtime"].(map[string]any)["http"] = true
		m["contributes"] = map[string]any{
			"api":       []any{map[string]any{"id": "feed", "prefix": "examplefeed", "features": []any{"example_feed"}, "events": []any{"examplefeed"}}},
			"assistant": []any{map[string]any{"id": "feed", "skills": []any{"skills/feed"}, "instructions": "assistant.md"}}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if a := m.APIs(); len(a) != 1 || a[0].Prefix != "examplefeed" || len(m.Assistants()) != 1 {
		t.Fatalf("%+v", a)
	}
	for name, change := range map[string]func(m map[string]any){
		"api without http": func(m map[string]any) {
			m["contributes"] = map[string]any{"api": []any{map[string]any{"id": "x", "prefix": "x"}}}
		},
		"api not on startup": func(m map[string]any) {
			m["runtime"].(map[string]any)["http"] = true
			m["activation_events"] = []any{"on_call"}
			m["contributes"] = map[string]any{"api": []any{map[string]any{"id": "x", "prefix": "x"}}}
		},
		"prefix with a slash": func(m map[string]any) {
			m["runtime"].(map[string]any)["http"] = true
			m["contributes"] = map[string]any{"api": []any{map[string]any{"id": "x", "prefix": "a/b"}}}
		},
		"feature name": func(m map[string]any) {
			m["runtime"].(map[string]any)["http"] = true
			m["contributes"] = map[string]any{"api": []any{map[string]any{"id": "x", "prefix": "x", "features": []any{"Bad-Name"}}}}
		},
		"skill outside": func(m map[string]any) {
			m["contributes"] = map[string]any{"assistant": []any{map[string]any{"id": "x", "skills": []any{"../skills"}}}}
		},
		"instructions not md": func(m map[string]any) {
			m["contributes"] = map[string]any{"assistant": []any{map[string]any{"id": "x", "instructions": "a.txt"}}}
		},
		"assistant empty": func(m map[string]any) {
			m["contributes"] = map[string]any{"assistant": []any{map[string]any{"id": "x"}}}
		},
	} {
		if _, err := Parse(with(t, change)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestMCPServersPoint(t *testing.T) {
	server := func(change func(map[string]any)) func(m map[string]any) {
		return func(m map[string]any) {
			s := map[string]any{"id": "example", "command": "example-agent", "env": "PERSONA_EXAMPLE_AGENT",
				"args": []any{"mcp-server"}, "requires": []any{"mcp-server"}, "allow": []any{"list"}, "deny": []any{"decide"}}
			if change != nil {
				change(s)
			}
			m["contributes"] = map[string]any{"mcp_servers": []any{s}}
		}
	}
	m, err := Parse(with(t, server(nil)))
	if err != nil {
		t.Fatal(err)
	}
	if s := m.MCPServers(); len(s) != 1 || s[0].Command != "example-agent" || s[0].Deny[0] != "decide" {
		t.Fatalf("%+v", s)
	}
	for name, change := range map[string]func(map[string]any){
		"the assistant's own name": func(s map[string]any) { s["id"] = "persona" },
		"a path as command":        func(s map[string]any) { s["command"] = "/usr/bin/x" },
		"a reserved command":       func(s map[string]any) { s["command"] = "bash" },
		"env name":                 func(s map[string]any) { s["env"] = "lower" },
		"env not Persona's":        func(s map[string]any) { s["env"] = "SHELL" },
		"an interpreter":           func(s map[string]any) { s["command"] = "zsh" },
		"tool name":                func(s map[string]any) { s["allow"] = []any{"a b"} },
		"allowed and denied":       func(s map[string]any) { s["allow"] = []any{"decide"} },
		"empty required word":      func(s map[string]any) { s["requires"] = []any{" "} },
		"unknown field":            func(s map[string]any) { s["cwd"] = "/tmp" },
	} {
		if _, err := Parse(with(t, server(change))); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestUIAndEventsPoints(t *testing.T) {
	m, err := Parse(with(t, func(m map[string]any) {
		m["permissions"] = map[string]any{"launcher_keys": true}
		m["contributes"] = map[string]any{
			"ui": []any{map[string]any{"id": "home", "title": "概况", "placement": "plugin_page"},
				map[string]any{"id": "panel", "title": "面板", "placement": "launcher_section", "hosts": []any{"gw.example.com"}}},
			"events": []any{map[string]any{"id": "e", "subscribe": []any{"agent.finished", "inbox.changed"}}}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.UIs()) != 2 || len(m.Topics()) != 2 {
		t.Fatalf("%+v %v", m.UIs(), m.Topics())
	}
	if !strings.Contains(fmt.Sprint(Explain(m)), "收到 hub 的事件") {
		t.Fatal("the events are explained")
	}
	if Digest(m) == Digest(mustParse(t)(with(t, func(m map[string]any) {
		m["permissions"] = map[string]any{"launcher_keys": true}
		m["contributes"] = map[string]any{"events": []any{map[string]any{"id": "e", "subscribe": []any{"agent.finished"}}}}
	}))) {
		t.Fatal("the topics are in the digest")
	}
	for name, change := range map[string]func(m map[string]any){
		"placement": func(m map[string]any) {
			m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "x", "title": "x", "placement": "sidebar"}}}
		},
		"hosts on a page": func(m map[string]any) {
			m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "x", "title": "x", "placement": "plugin_page", "hosts": []any{"gw.example.com"}}}}
		},
		"section without keys": func(m map[string]any) {
			m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "x", "title": "x", "placement": "launcher_section", "hosts": []any{"gw.example.com"}}}}
		},
		"ui without rpc": func(m map[string]any) {
			m["runtime"].(map[string]any)["rpc"] = "none"
			m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "x", "title": "x", "placement": "plugin_page"}}}
		},
		"unknown topic": func(m map[string]any) {
			m["contributes"] = map[string]any{"events": []any{map[string]any{"id": "e", "subscribe": []any{"mail.sent"}}}}
		},
		"events not on startup": func(m map[string]any) {
			m["activation_events"] = []any{"on_call"}
			m["contributes"] = map[string]any{"events": []any{map[string]any{"id": "e", "subscribe": []any{"agent.finished"}}}}
		},
		"icon off the navigation": func(m map[string]any) {
			m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "x", "title": "x", "placement": "plugin_page", "icon": "chart"}}}
		},
		"unknown icon": func(m map[string]any) {
			m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "x", "title": "x", "placement": "nav_page", "icon": "rocket"}}}
		},
		"a page opens nothing": func(m map[string]any) {
			m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "x", "title": "x", "placement": "nav_page"},
				map[string]any{"id": "y", "title": "y", "placement": "plugin_page", "opens": "x"}}}
		},
		"a card opens what is not there": func(m map[string]any) {
			m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "x", "title": "x", "placement": "overview_card", "opens": "nav"}}}
		},
		"a card opens a card": func(m map[string]any) {
			m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "x", "title": "x", "placement": "overview_card", "opens": "y"},
				map[string]any{"id": "y", "title": "y", "placement": "overview_card"}}}
		},
	} {
		if _, err := Parse(with(t, change)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// An inbox_item draws the inbox items of one of the plugin's own connectors, named in connector; it is shown before
// installing like the other slots outside the plugin's settings.
func TestInboxItemSlot(t *testing.T) {
	item := func(extra ...any) func(m map[string]any) {
		return func(m map[string]any) {
			ui := []any{map[string]any{"id": "event", "title": "事件", "placement": "inbox_item", "connector": "examplenotes"}}
			m["contributes"].(map[string]any)["ui"] = append(ui, extra...)
		}
	}
	m, err := Parse(with(t, item()))
	if err != nil {
		t.Fatal(err)
	}
	if u := m.UIs(); len(u) != 1 || u[0].Connector != "examplenotes" || strings.Join(m.Surfaces(), ",") != "inbox_item" {
		t.Fatalf("%+v %v", u, m.Surfaces())
	}
	if !strings.Contains(fmt.Sprint(Explain(m)), "收件箱里它的连接器的条目详情（会读到那一条的标题、总结和分级）：事件") {
		t.Fatal(Explain(m))
	}
	for name, change := range map[string]func(m map[string]any){
		"no connector": func(m map[string]any) {
			m["contributes"].(map[string]any)["ui"] = []any{map[string]any{"id": "event", "title": "事件", "placement": "inbox_item"}}
		},
		"not a connector id": func(m map[string]any) {
			m["contributes"].(map[string]any)["ui"] = []any{map[string]any{"id": "event", "title": "事件", "placement": "inbox_item", "connector": "Example-Notes"}}
		},
		"another's connector": func(m map[string]any) {
			m["contributes"].(map[string]any)["ui"] = []any{map[string]any{"id": "event", "title": "事件", "placement": "inbox_item", "connector": "mail"}}
		},
		"a connector off the inbox": func(m map[string]any) {
			m["contributes"].(map[string]any)["ui"] = []any{map[string]any{"id": "x", "title": "x", "placement": "nav_page", "connector": "examplenotes"}}
		},
		"two for one connector": item(map[string]any{"id": "again", "title": "又一个", "placement": "inbox_item", "connector": "examplenotes"}),
	} {
		if _, err := Parse(with(t, change)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// The slots outside a plugin's settings are shown before installing, in the digest, and agreed to again when an
// upgrade adds one.
func TestUISurfaces(t *testing.T) {
	page := mustParse(t)(with(t, func(m map[string]any) {
		m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "home", "title": "概况", "placement": "plugin_page"}}}
	}))
	slots := mustParse(t)(with(t, func(m map[string]any) {
		m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "home", "title": "概况", "placement": "plugin_page"},
			map[string]any{"id": "usage", "title": "用量", "placement": "nav_page", "icon": "chart"},
			map[string]any{"id": "card", "title": "今天", "placement": "overview_card", "opens": "usage"},
			map[string]any{"id": "gpu", "title": "显卡", "placement": "machine_section"}}}
	}))
	if len(page.Surfaces()) != 0 || strings.Join(slots.Surfaces(), ",") != "nav_page,overview_card,machine_section" {
		t.Fatalf("%v %v", page.Surfaces(), slots.Surfaces())
	}
	if Digest(page) == Digest(slots) {
		t.Fatal("the surfaces are in the digest")
	}
	if Digest(page) != Digest(mustParse(t)(with(t, func(m map[string]any) {
		m["contributes"] = map[string]any{"ui": []any{map[string]any{"id": "other", "title": "别的", "placement": "plugin_page"}}}
	}))) {
		t.Fatal("its own pages are not")
	}
	explained := fmt.Sprint(Explain(slots))
	if !strings.Contains(explained, "主导航上的入口：用量；总览上的卡片：今天；机器详情页里的一节（会知道是哪台机器）：显卡") {
		t.Fatal(explained)
	}
	if added := fmt.Sprint(Added(page, slots)); !strings.Contains(added, "在 App 的更多位置显示自己的界面") {
		t.Fatal(added)
	}
	if added := Added(slots, slots); added != nil {
		t.Fatal(added)
	}
}

// The channel point (docs/extensions/channels.md "Manifest"): a chat app's bot bound through the plugin's own connector, or running
// from the start without one.
func TestChannelPoint(t *testing.T) {
	ch := func(extra map[string]any) map[string]any {
		c := map[string]any{"id": "examplechat", "name": "Example Chat", "kind": "im", "connector": "examplenotes", "bind": "qr",
			"capabilities": map[string]any{"images_in": true, "format": "plain", "max_text": 4000, "asks": "text", "push": true, "typing": true}}
		for k, v := range extra {
			c[k] = v
		}
		return c
	}
	conn := []any{map[string]any{"id": "examplenotes", "name": "Example Notes", "capabilities": []any{"status", "login"}}}
	m, err := Parse(with(t, func(m map[string]any) {
		m["contributes"] = map[string]any{"connectors": conn, "channel": []any{ch(nil)}}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c := m.Channels(); len(c) != 1 || c[0].Capabilities.MaxText != 4000 || c[0].Bind != "qr" || m.Kind() != "connectors" {
		t.Fatalf("%+v %s", c, m.Kind())
	}
	if !strings.Contains(fmt.Sprint(Explain(m)), "作为和 Persona 对话的渠道") {
		t.Fatal("the channel is explained")
	}
	for name, change := range map[string]func(m map[string]any){
		"a connector it does not have": func(m map[string]any) {
			m["contributes"] = map[string]any{"channel": []any{ch(nil)}}
		},
		"no connector, not on startup": func(m map[string]any) {
			m["activation_events"] = []any{"on_call"}
			m["contributes"] = map[string]any{"channel": []any{ch(map[string]any{"connector": "", "bind": ""})}}
		},
		"the hub's id": func(m map[string]any) {
			m["contributes"] = map[string]any{"connectors": conn, "channel": []any{ch(map[string]any{"id": "feishu"})}}
		},
		"a field it does not know": func(m map[string]any) {
			m["contributes"] = map[string]any{"connectors": conn, "channel": []any{ch(map[string]any{"cards": true})}}
		},
		"asks it does not know": func(m map[string]any) {
			m["contributes"] = map[string]any{"connectors": conn, "channel": []any{ch(map[string]any{"capabilities": map[string]any{"asks": "cards"}})}}
		},
		"without rpc": func(m map[string]any) {
			m["runtime"].(map[string]any)["rpc"] = "none"
			m["contributes"] = map[string]any{"channel": []any{ch(map[string]any{"connector": "", "bind": ""})}}
		},
	} {
		if _, err := Parse(with(t, change)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func mustParse(t *testing.T) func(b []byte) *Manifest {
	return func(b []byte) *Manifest {
		m, err := Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
}

// A gate file: a plain command, a script in the package, a title; credential names only; rules and actions that read.
func TestParseGate(t *testing.T) {
	good := `{"command": "feed", "script": "feed/scripts/feed.sh", "title": "示例", "env": ["FEED_BASE", "FEED_TOKEN"], "base_env": "FEED_BASE",
		"secret": ["FEED_TOKEN"], "rules": [{"method": "GET", "path": "**", "action": "allow"}], "actions": [{"pattern": "^POST /posts", "what": "发帖"}],
		"tool": {"name": "feed_api", "description": "Run feed."}}`
	if _, err := ParseGate([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]string{
		"command":      `{"command": "Feed", "script": "f/x.sh", "title": "x"}`,
		"lark-cli":     `{"command": "lark-cli", "script": "f/x.sh", "title": "x"}`,
		"script":       `{"command": "f", "script": "../x.sh", "title": "x"}`,
		"title":        `{"command": "f", "script": "f/x.sh"}`,
		"hub env":      `{"command": "f", "script": "f/x.sh", "title": "x", "env": ["PERSONA_HUB_TOKEN"]}`,
		"path":         `{"command": "f", "script": "f/x.sh", "title": "x", "env": ["PATH"]}`,
		"base_env":     `{"command": "f", "script": "f/x.sh", "title": "x", "base_env": "X"}`,
		"rule action":  `{"command": "f", "script": "f/x.sh", "title": "x", "rules": [{"method": "GET", "path": "**", "action": "maybe"}]}`,
		"action regex": `{"command": "f", "script": "f/x.sh", "title": "x", "actions": [{"pattern": "(", "what": "x"}]}`,
		"tool":         `{"command": "f", "script": "f/x.sh", "title": "x", "tool": {"name": "Bad"}}`,
		"unknown":      `{"command": "f", "script": "f/x.sh", "title": "x", "extra": 1}`,
		"auth no value": `{"command": "f", "script": "f/x.sh", "title": "x", "env": ["B", "T"], "base_env": "B",
			"auth": {"header": "X-Key"}}`,
		"auth unknown name": `{"command": "f", "script": "f/x.sh", "title": "x", "env": ["B", "T"], "base_env": "B",
			"auth": {"header": "X-Key", "value": "${OTHER}"}}`,
		"auth both": `{"command": "f", "script": "f/x.sh", "title": "x", "env": ["B", "T"], "base_env": "B",
			"auth": {"header": "X-Key", "value": "${T}", "env": true}}`,
		"auth held header": `{"command": "f", "script": "f/x.sh", "title": "x", "env": ["B", "T"], "base_env": "B",
			"auth": {"header": "Host", "value": "${T}"}}`,
		"auth no base": `{"command": "f", "script": "f/x.sh", "title": "x", "env": ["T"], "auth": {"header": "X-Key", "value": "${T}"}}`,
	} {
		if _, err := ParseGate([]byte(bad)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// How the node's proxy adds a gate's credentials (#1317): as the file says, or Authorization: Bearer for its one
// secret; none (the old way, in the assistant's environment) when it says env or cannot tell.
func TestGateProxyAuth(t *testing.T) {
	const head = `{"command": "f", "script": "f/x.sh", "title": "x", `
	for name, tc := range map[string]struct {
		file string
		want *proto.GateAuth
	}{
		"one secret": {`"env": ["B", "T"], "base_env": "B", "secret": ["T"]}`, &proto.GateAuth{Header: "Authorization", Value: "Bearer ${T}"}},
		"declared": {`"env": ["B", "K", "T"], "base_env": "B", "secret": ["K", "T"], "auth": {"header": "X-Api-Key", "value": "${K}:${T}"}}`,
			&proto.GateAuth{Header: "X-Api-Key", Value: "${K}:${T}"}},
		"env":              {`"env": ["B", "T"], "base_env": "B", "secret": ["T"], "auth": {"env": true}}`, nil},
		"two secrets":      {`"env": ["B", "K", "T"], "base_env": "B", "secret": ["K", "T"]}`, nil},
		"no base":          {`"env": ["T"], "secret": ["T"]}`, nil},
		"secret not given": {`"env": ["B"], "base_env": "B", "secret": ["T"]}`, nil},
	} {
		g, err := ParseGate([]byte(head + tc.file))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := g.ProxyAuth(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
}

// withNode is the example manifest with a program on computers: macOS ones, picked in the field machines.
func withNode(t *testing.T, change func(node, machines map[string]any)) []byte {
	t.Helper()
	return with(t, func(m map[string]any) {
		node := map[string]any{"command": map[string]any{"darwin-arm64": "node/helper", "darwin-amd64": "node/helper-x64"},
			"machines": "macs", "secrets": []any{"api_key"}}
		machines := map[string]any{"type": "array", "title": "在哪些 Mac 上运行", "items": map[string]any{"type": "string", "format": "machine", "x-os": []any{"darwin"}}}
		m["node"] = node
		m["configuration"].(map[string]any)["properties"].(map[string]any)["macs"] = machines
		if change != nil {
			change(node, machines)
		}
	})
}

func TestParseNodeProgram(t *testing.T) {
	m, err := Parse(withNode(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Node.Systems(); fmt.Sprint(got) != "[darwin]" {
		t.Fatal(got)
	}
	if f := m.NodeMachines(); f == nil || f.Name != "macs" {
		t.Fatal(f)
	}
	// Only on computers: no program on the hub (what it contributes needs none there).
	var raw map[string]any
	_ = json.Unmarshal(withNode(t, nil), &raw)
	delete(raw, "runtime")
	delete(raw, "activation_events")
	raw["contributes"] = map[string]any{"providers": []any{map[string]any{"id": "example-ai", "name": "Example AI", "kind": "api",
		"api": "responses", "base_url": "https://api.example.com/v1", "models": []any{map[string]any{"id": "example-large"}}}}}
	b, _ := json.Marshal(raw)
	if m, err := Parse(b); err != nil || m.Runtime != nil {
		t.Fatalf("a plugin whose only program runs on computers: %v", err)
	}
	// Its protocol is still the hub's to speak.
	raw["engines"] = map[string]any{"plugin_protocol": 9}
	b, _ = json.Marshal(raw)
	if _, err := Parse(b); err == nil {
		t.Fatal("a node program speaking another protocol")
	}
}

func TestParseRefusesNodeProgram(t *testing.T) {
	cases := map[string]func(node, machines map[string]any){
		"one path for every platform":   func(n, _ map[string]any) { n["command"] = "node/helper" },
		"built in (only the hub's own)": func(n, _ map[string]any) { n["command"] = "builtin" },
		"no platform":                   func(n, _ map[string]any) { n["command"] = map[string]any{} },
		"windows":                       func(n, _ map[string]any) { n["command"] = map[string]any{"windows-amd64": "node/x.exe"} },
		"outside the package":           func(n, _ map[string]any) { n["command"] = map[string]any{"darwin-arm64": "../x"} },
		"the hub's binary":              func(n, _ map[string]any) { n["command"] = map[string]any{"darwin-arm64": "personad"} },
		"rpc value":                     func(n, _ map[string]any) { n["rpc"] = "tcp" },
		"no way to talk":                func(n, _ map[string]any) { n["rpc"] = "none" },
		"keep_running":                  func(n, _ map[string]any) { n["keep_running"] = "forever" },
		"hub's env":                     func(n, _ map[string]any) { n["env"] = map[string]any{"PERSONA_HUB_TOKEN": "x"} },
		"unknown secret":                func(n, _ map[string]any) { n["secrets"] = []any{"password"} },
		"secret twice":                  func(n, _ map[string]any) { n["secrets"] = []any{"api_key", "api_key"} },
		"host": func(n, _ map[string]any) {
			n["permissions"] = map[string]any{"network": []any{"https://x.example.com"}}
		},
		"unknown permission":     func(n, _ map[string]any) { n["permissions"] = map[string]any{"camera": true} },
		"no machines field":      func(n, _ map[string]any) { delete(n, "machines") },
		"missing machines field": func(n, _ map[string]any) { n["machines"] = "computers" },
		"machines not machines":  func(_, f map[string]any) { f["items"] = map[string]any{"type": "string"} },
		"machines not a list":    func(_, f map[string]any) { delete(f, "items"); f["type"] = "string"; f["format"] = "machine" },
		"x-os missing":           func(_, f map[string]any) { delete(f["items"].(map[string]any), "x-os") },
		"x-os with no program":   func(_, f map[string]any) { f["items"].(map[string]any)["x-os"] = []any{"darwin", "linux"} },
		"unknown field":          func(n, _ map[string]any) { n["max_machines"] = 1 },
	}
	for name, change := range cases {
		if _, err := Parse(withNode(t, change)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	// Linux too: x-os may name both.
	if _, err := Parse(withNode(t, func(n, f map[string]any) {
		n["command"].(map[string]any)["linux-amd64"] = "node/helper-linux"
		f["items"].(map[string]any)["x-os"] = []any{"darwin", "linux"}
	})); err != nil {
		t.Fatal(err)
	}
}

// What installed plugins were granted holds: a manifest without a node program digests as before there were any.
func TestDigestWithoutNodeIsAsBefore(t *testing.T) {
	m, _ := Parse([]byte(exampleManifest))
	b, _ := json.Marshal(struct {
		RunsCode    bool        `json:"runs_code"`
		Permissions Permissions `json:"permissions"`
		Events      []string    `json:"events,omitempty"`
		Surfaces    []string    `json:"surfaces,omitempty"`
	}{true, m.Permissions.normalized(), m.Topics(), m.Surfaces()})
	sum := sha256.Sum256(b)
	if Digest(m) != hex.EncodeToString(sum[:]) {
		t.Fatal("the digest of a plugin without a node program changed")
	}
	n, _ := Parse(withNode(t, nil))
	if Digest(n) == Digest(m) {
		t.Fatal("a program on computers is agreed to")
	}
	more, _ := Parse(withNode(t, func(node, _ map[string]any) { node["permissions"] = map[string]any{"files": true} }))
	if Digest(more) == Digest(n) {
		t.Fatal("more asked on the computers, another digest")
	}
	hosts, _ := Parse(withNode(t, func(node, _ map[string]any) {
		node["permissions"] = map[string]any{"network": []any{"b.example.com", "a.example.com", "a.example.com"}}
	}))
	sorted, _ := Parse(withNode(t, func(node, _ map[string]any) {
		node["permissions"] = map[string]any{"network": []any{"a.example.com", "b.example.com"}}
	}))
	if Digest(hosts) != Digest(sorted) {
		t.Fatal("the same hosts in another order")
	}
}

func TestExplainAndAddedNodeProgram(t *testing.T) {
	base, _ := Parse([]byte(exampleManifest))
	n, _ := Parse(withNode(t, func(node, _ map[string]any) {
		node["permissions"] = map[string]any{"network": []any{"notes.example.com"}, "macos_privacy": true}
	}))
	keys := func(lines []PermissionLine) string {
		var out []string
		for _, l := range lines {
			out = append(out, l.Key)
		}
		return strings.Join(out, ",")
	}
	if got := keys(Explain(n)); got != "runs_code,network,secrets,node_runs_code,node_network,node_macos_privacy,node_secrets" {
		t.Fatal(got)
	}
	if l := Explain(n)[3]; !strings.Contains(l.Detail, "Mac") || strings.Contains(l.Detail, "Linux") {
		t.Fatal(l)
	}
	if got := keys(Added(base, n)); got != "node_runs_code,node_network,node_macos_privacy,node_secrets" || !NodeAdds(base, n) {
		t.Fatal(got)
	}
	if got := Added(n, n); len(got) != 0 || NodeAdds(n, n) {
		t.Fatal("nothing more", got)
	}
	linux, _ := Parse(withNode(t, func(node, f map[string]any) {
		node["command"].(map[string]any)["linux-amd64"] = "node/helper-linux"
		f["items"].(map[string]any)["x-os"] = []any{"darwin", "linux"}
		node["permissions"] = map[string]any{"network": []any{"notes.example.com"}, "macos_privacy": true}
	}))
	if got := keys(Added(n, linux)); got != "node_runs_code" || !NodeAdds(n, linux) {
		t.Fatal("Linux computers too", got)
	}
	if got := keys(Added(n, base)); got != "" {
		t.Fatal("no program on computers any more adds nothing", got)
	}
}

// Tags are the market's: held to a market entry's rules, and left out of the manifest a package or an index carries.
func TestManifestTags(t *testing.T) {
	tagged := strings.Replace(exampleManifest, `"manifest_version": 1,`, `"manifest_version": 1, "tags": ["notes", "示例"],`, 1)
	m, err := Parse([]byte(tagged))
	if err != nil || !slices.Equal(m.Tags, []string{"notes", "示例"}) {
		t.Fatalf("tags: %v %v", m, err)
	}
	for _, bad := range []string{`[""]`, `["` + strings.Repeat("x", 25) + `"]`, `["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q"]`} {
		if _, err := Parse([]byte(strings.Replace(exampleManifest, `"manifest_version": 1,`, `"manifest_version": 1, "tags": `+bad+`,`, 1))); err == nil {
			t.Errorf("tags %s: no error", bad)
		}
	}
	packed, err := PackagedManifest([]byte(tagged))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := strict(packed, &fields); err != nil || fields["tags"] != nil || fields["id"] == nil {
		t.Fatalf("packaged: %s %v", packed, err)
	}
	if p, err := Parse(packed); err != nil || p.ID != m.ID || p.Tags != nil {
		t.Fatalf("packaged reads back as %+v %v", p, err)
	}
	if same, _ := PackagedManifest([]byte(exampleManifest)); string(same) != exampleManifest {
		t.Fatal("a manifest without tags is changed")
	}
}
