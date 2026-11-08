package extension

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"reflect"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
)

// catalogFields checks names only where encoding/json decodes a known struct.
// strictJSON already rejects exact duplicates everywhere. Arbitrary maps and raw
// schemas keep case-sensitive keys; unknown catalog metadata remains extensible.
func catalogFields(raw []byte, typ reflect.Type) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || typ == nil || typ == reflect.TypeFor[json.RawMessage]() {
		return nil
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if raw[0] == '[' && (typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array) {
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return err
		}
		for _, item := range items {
			if err := catalogFields(item, typ.Elem()); err != nil {
				return err
			}
		}
		return nil
	}
	if raw[0] != '{' {
		return nil
	} // Ordinary typed decoding checks value types.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if typ.Kind() == reflect.Map {
		for _, value := range fields {
			if err := catalogFields(value, typ.Elem()); err != nil {
				return err
			}
		}
		return nil
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	known := map[string]reflect.Type{}
	if typ == reflect.TypeFor[Engines]() {
		// Engines has a custom decoder with explicit rolling-upgrade aliases.
		known["persona"] = reflect.TypeFor[string]()
		known["extension_protocol"] = reflect.TypeFor[int]()
		known["plugin_protocol"] = reflect.TypeFor[int]()
	} else {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			known[name] = field.Type
		}
	}
	if typ == reflect.TypeFor[MarketIndex]() {
		known["extensions"] = reflect.TypeFor[[]MarketEntry]()
	}
	if typ == reflect.TypeFor[MarketEntry]() {
		known["manifest"] = reflect.TypeFor[Manifest]()
	}
	for name, value := range fields {
		fieldType, exact := known[name]
		if !exact {
			for protocolName := range known {
				// EqualFold follows Unicode simple folding like encoding/json, including
				// long-s and Kelvin sign. Do not normalize arbitrary map keys this way.
				if strings.EqualFold(name, protocolName) {
					return i18n.Err("extension-market_security.catalog")
				}
			}
			if typ == reflect.TypeFor[catalogSignature]() {
				return i18n.Err("extension-market_security.catalog")
			}
			continue
		}
		if typ == reflect.TypeFor[Manifest]() && name == "contributes" {
			var groups map[string]json.RawMessage
			if err := json.Unmarshal(value, &groups); err != nil {
				return err
			}
			for point, items := range groups {
				if itemType := catalogItemTypes[point]; itemType != nil {
					if err := catalogFields(items, reflect.SliceOf(itemType)); err != nil {
						return err
					}
				}
			}
		}
		if err := catalogFields(value, fieldType); err != nil {
			return err
		}
	}
	return nil
}

// These are the typed decoders used by Points.Item; extension-defined points
// remain opaque. The catalog regression checks that every built-in point is covered.
var catalogItemTypes = map[string]reflect.Type{
	"tunnels":          reflect.TypeFor[TunnelItem](),
	"skills":           reflect.TypeFor[SkillItem](),
	"prompts":          reflect.TypeFor[PromptItem](),
	"themes":           reflect.TypeFor[ThemeItem](),
	"triage_templates": reflect.TypeFor[TriageTemplateItem](),
	"agent_filters":    reflect.TypeFor[AgentFilterItem](),
	"looks":            reflect.TypeFor[LookItem](),
	"connectors":       reflect.TypeFor[ConnectorItem](),
	"quota":            reflect.TypeFor[QuotaItem](),
	"agent_sources":    reflect.TypeFor[AgentSourceItem](),
	"providers":        reflect.TypeFor[ProviderItem](),
	"api":              reflect.TypeFor[APIItem](),
	"assistant":        reflect.TypeFor[AssistantItem](),
	"mcp_servers":      reflect.TypeFor[MCPServerItem](),
	"messaging":        reflect.TypeFor[wire.MessagingItem](),
	"ui":               reflect.TypeFor[UIItem](),
	"events":           reflect.TypeFor[EventsItem](),
	"error_reports":    reflect.TypeFor[ErrorReportsItem](),
	"auth_relay":       reflect.TypeFor[AuthRelayItem](),
	"voice":            reflect.TypeFor[VoiceItem](),
	"browser":          reflect.TypeFor[ComputerItem](),
	"channel":          reflect.TypeFor[channel.Item](),
	"shortcuts":        reflect.TypeFor[ShortcutItem](),
	"computer":         reflect.TypeFor[ComputerItem](),
}

// Include the parsed projection as well as the signed entry fields: callers may
// copy an entry, but cannot replace its package or parsed manifest and retain proof.
func entryFingerprint(e *MarketEntry) [sha256.Size]byte {
	raw, _ := json.Marshal(struct {
		Entry  *MarketEntry
		Parsed *Manifest
	}{e, e.Parsed})
	return sha256.Sum256(raw)
}
