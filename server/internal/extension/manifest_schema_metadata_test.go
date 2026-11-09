package extension

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

// The SDK schema (wire.Validate on schema/extension/manifest.schema.json) and the hub's Parse agree on the strings an
// author writes by hand: the top-level name, secret titles, homepage and the version ranges ParseRange and
// ParseVersionRange read, a connector's cli, and the hub's scopes. A value the schema and catalog CI pass but the hub refuses fails only
// at install; one the hub reads but the schema refuses fails catalog CI for a manifest that installs.
//
// Known gaps, where the schema still passes what the hub refuses: names and titles of only non-ASCII spaces or \v
// (RE2's \s is [\t\n\f\r ]), a homepage with a bad %-escape or a non-numeric port, a build number past int64, a
// duplicate secret name or dependency, and a dependency on itself. The schema refuses an upper-case HTTPS:// homepage
// on purpose (the apps open lowercase https:// links only), though the hub reads it.
func TestManifestSchemaAgreesOnMetadata(t *testing.T) {
	versionRanges := struct{ ok, bad []string }{
		ok: []string{"", ">=1.2.0", "<2.0.0", ">=1.2.0 <2.0.0", "  >=1.2.0\t<2.0.0  ", ">=1.0.0-rc.1", ">=0.0.0"},
		bad: []string{"abc", "   ", "1.2.0", ">=1.2", ">=01.2.0", "<=2.0.0", ">1.2.0", "^1.2.0", ">= 1.2.0",
			">=1.2.0,<2.0.0", ">=1.2.0+build"},
	}
	cases := []struct {
		field   string
		set     func(m map[string]any, v string)
		ok, bad []string
	}{
		{"name", func(m map[string]any, v string) { m["name"] = v },
			[]string{"Example", " Example ", "示例"}, []string{"", "   ", "\t\n"}},
		{"secrets[].title", func(m map[string]any, v string) {
			m["secrets"] = []any{map[string]any{"name": "api_key", "title": v}}
		}, []string{"API key", " API key "}, []string{"", "  "}},
		{"homepage", func(m map[string]any, v string) { m["homepage"] = v },
			[]string{"https://example.com", "https://example.com/a b?q=1#top", "https://example.com:8443/x",
				"https://user:pass@example.com", "https://a@b@example.com", "https://[::1]/", "https://例子.example/"},
			[]string{"https://", "https://@", "https://user@", "https:///path", "https://exa mple.com",
				"https://user name@example.com", "https://a^b.example", "https://a|b.example", "https://a\\b.example",
				"https://example.com/\x01", "http://example.com", "example.com"}},
		{"engines.persona", func(m map[string]any, v string) { m["engines"] = map[string]any{"persona": v} },
			[]string{"", ">=202611050000", ">=202611050000 <202701010000", "  <1\t", ">=+5", ">=007"},
			[]string{"nonsense", "   ", ">=0", ">=+0", ">=-5", "<=5", ">5", "=5", ">= 5", ">=5,<6", ">=1.0", ">=0x10"}},
		{"depends[].version", func(m map[string]any, v string) {
			m["depends"] = []any{map[string]any{"id": "example-other", "version": v}}
		}, versionRanges.ok, versionRanges.bad},
		{"extension_pack[].version", func(m map[string]any, v string) {
			delete(m, "contributes")
			m["extension_pack"] = []any{map[string]any{"id": "example-other", "version": v}}
		}, versionRanges.ok, versionRanges.bad},
		// An empty cli means the connector has none: the hub reads it, so the schema must too.
		{"contributes.connectors[].cli", func(m map[string]any, v string) {
			m["engines"] = map[string]any{"extension_protocol": 1}
			m["runtime"] = map[string]any{"command": "bin/example-agree"}
			m["contributes"] = map[string]any{"connectors": []any{map[string]any{
				"id": "exampleagree", "name": "Example", "capabilities": []any{"status"}, "cli": v}}}
		}, []string{"", "notes", "example-notes2", "a" + strings.Repeat("b", 31)},
			[]string{"Notes", "1notes", "-notes", "notes_cli", " notes", "a" + strings.Repeat("b", 32), "git", "personad"}},
		{"permissions.hub[]", func(m map[string]any, v string) {
			m["engines"] = map[string]any{"extension_protocol": 1}
			m["runtime"] = map[string]any{"command": "bin/example-agree"}
			m["permissions"] = map[string]any{"hub": []any{v}}
		}, append(slices.Clone(HubScopes), ScopeCallPrefix+"example-other"),
			[]string{"agents", "agents.everything", " kb.read", "KB.read", ScopeCallPrefix, ScopeCallPrefix + "Example"}},
	}
	for _, c := range cases {
		check := func(v string, want bool) {
			m := map[string]any{"manifest_version": 1, "id": "example-agree", "name": "Example", "version": "1.0.0",
				"contributes": map[string]any{"prompts": []any{map[string]any{"id": "p", "kind": "quick", "title": "P", "text": "Say hello."}}}}
			c.set(m, v)
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			schemaErr := wire.ValidateJSON("Manifest", raw)
			_, hubErr := Parse(raw)
			if (schemaErr == nil) != want || (hubErr == nil) != want {
				t.Errorf("%s %q: want valid=%v; schema: %v; hub: %v", c.field, v, want, schemaErr, hubErr)
			}
		}
		for _, v := range c.ok {
			check(v, true)
		}
		for _, v := range c.bad {
			check(v, false)
		}
	}
}
