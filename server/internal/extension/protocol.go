// Package extension is the extension base (docs/extensions/extensions.md, docs/plans/plan-extensions.md): the manifest
// (persona-extension.json, formerly persona-plugin.json) and its rules, the configuration form's JSON Schema subset,
// permissions, the registry of installed extensions, and the wire types of the process extension protocol. Running
// extensions is extension/host's; a program's side is extension/serve.
package extension

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Protocol is the version of the process plugin protocol: the hub sends it with initialize, the program answers with
// its own, and the hub refuses a program of another version.
const Protocol = 1

// ManifestVersion is the version of persona-plugin.json this hub reads.
const ManifestVersion = 1

// ManifestFile is the manifest's name at the root of an extension package; LegacyManifestFile is its name before
// #1844, still read when a package has no ManifestFile (docs/extensions/extensions.md "Naming and compatibility").
const (
	ManifestFile       = "persona-extension.json"
	LegacyManifestFile = "persona-plugin.json"
)

// ManifestPath is the manifest in dir: ManifestFile, or LegacyManifestFile when only that one is there.
func ManifestPath(dir string) string {
	if _, err := os.Stat(filepath.Join(dir, ManifestFile)); err != nil {
		if _, lerr := os.Stat(filepath.Join(dir, LegacyManifestFile)); lerr == nil {
			return filepath.Join(dir, LegacyManifestFile)
		}
	}
	return filepath.Join(dir, ManifestFile)
}

// hasManifest says whether dir has a manifest under either name.
func hasManifest(dir string) bool {
	_, err := os.Stat(ManifestPath(dir))
	return err == nil
}

// Methods of the base (every other method is <namespace>.<method>, the namespace being an extension point's).
const (
	MInitialize = "initialize"
	MPing       = "ping"
	// MManifest answers persona-plugin.json, before initialize too: how the hub learns what a plugin elsewhere is
	// (remote: installs). A program the hub starts from its package need not answer it.
	MManifest = "manifest"
)

// Error codes (rpc.Error.Code), for callers that act on the kind of failure.
const (
	CodeNotInitialized   = "not_initialized"
	CodeUnknownMethod    = "unknown_method"
	CodeWrongProtocol    = "wrong_protocol"
	CodeNoCapability     = "no_capability"
	CodeNoContribution   = "no_contribution"
	CodePermissionDenied = "permission_denied"
	CodeTimeout          = "timeout"
	// CodeUnsupported: not this contribution's (a quota plugin asked about a gateway it does not read).
	CodeUnsupported = "unsupported"
)

// InitializeParams is what the hub sends first.
type InitializeParams struct {
	Protocol int     `json:"protocol"`
	Hub      HubInfo `json:"hub"`
	// Extension is "extension"; the hub sends it as "plugin" too, for the programs written before #1844, and reads either.
	Extension ExtensionInfo `json:"extension"`
	// Config is the plugin's configuration with the schema's defaults filled in.
	Config json.RawMessage `json:"config,omitempty"`
	// Secrets are the plugin's own (declared in its manifest, filled in by the user), by name.
	Secrets map[string]string `json:"secrets,omitempty"`
	// Granted are the permissions the user accepted.
	Granted Permissions `json:"granted"`
	// Active are the contributions this run is for, by extension point (a switched-off connector is not in it).
	Active map[string][]string `json:"active,omitempty"`
	// ContributionsToMe are the other extensions' contributions to the points it exports (docs/extensions/extensions.md "Exported extension points"),
	// by point id; the event extension.contributions_changed says them again when they change.
	ContributionsToMe map[string][]ExportedContribution `json:"contributions_to_me,omitempty"`
}

func (p InitializeParams) MarshalJSON() ([]byte, error) {
	type plain InitializeParams
	return json.Marshal(struct {
		plain
		Legacy ExtensionInfo `json:"plugin"`
	}{plain(p), p.Extension})
}

func (p *InitializeParams) UnmarshalJSON(data []byte) error {
	type plain InitializeParams
	var v struct {
		plain
		Legacy *ExtensionInfo `json:"plugin"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*p = InitializeParams(v.plain)
	if p.Extension == (ExtensionInfo{}) && v.Legacy != nil {
		p.Extension = *v.Legacy
	}
	return nil
}

// HubInfo is the hub a plugin runs under.
type HubInfo struct {
	Build int64 `json:"build"`
	// ExtensionPoints are the extension points the hub has and the version of each (connectors: the connector
	// protocol's).
	ExtensionPoints map[string]int `json:"extension_points"`
	// Role is the hub's place in a group of hubs (docs/server/mesh.md): "center" (alone, or the group's center: it serves
	// the apps and drives the computers) or "member" (another hub is the center; work the center does for the group,
	// like answering a chat service's realtime events, is not this one's). A change restarts the plugin.
	Role string `json:"role,omitempty"`
	// Timezone is the user's time zone by name (IANA, as their devices say it): for the times a plugin shows people
	// ("today", a message's time). Empty while only the hub's own clock is known. Read at each start.
	Timezone string `json:"timezone,omitempty"`
	// Locale is the user's language (zh-Hans, en, ja; docs/protocol/i18n.md "Protocol"): what a plugin writes for people itself
	// (its status, sign-in hints, a bot's replies) reads in it. Read at each start; empty means zh-Hans.
	Locale string `json:"locale,omitempty"`
	// Capabilities are what this hub does that a plugin may go by ("inbox_group": it relates a connector's batches of
	// new messages to inbox events, docs/assistant/inbox.md "Event synthesis"). Absent or without one: the hub does not do it.
	Capabilities []string `json:"capabilities,omitempty"`
	// URL and Token are the hub's API on this machine and a token of the scopes it was granted (permissions.hub; also
	// PERSONA_HUB_URL / PERSONA_HUB_TOKEN when the hub runs it): a new one each start, revoked when it stops for good.
	// An extension granted the full token the old way (hub_api) has it in the environment only. On a manager other
	// than the built-in, URL is that manager's relay to the hub on this machine's loopback (docs/assistant/managers.md
	// "Extensions and the hub's API").
	URL    string   `json:"url,omitempty"`
	Token  string   `json:"token,omitempty"`
	Scopes []string `json:"scopes,omitempty"`
	// Manager is the id of the manager that runs the extension, when it is not the built-in one (also
	// PERSONA_MANAGER when it runs the program); absent: the built-in manager, or an older build.
	Manager string `json:"manager,omitempty"`
}

// Hub capabilities (HubInfo.Capabilities).
const (
	HubInboxGroup = "inbox_group"
)

// HubCapabilities are this hub's.
func HubCapabilities() []string { return []string{HubInboxGroup} }

// ExtensionInfo is the plugin as the hub installed it.
type ExtensionInfo struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Dir     string `json:"dir,omitempty"`
	DataDir string `json:"data_dir,omitempty"`
}

// InitializeResult is the program's answer: who it is and what it contributes (a subset of its manifest's).
type InitializeResult struct {
	Protocol    int                        `json:"protocol"`
	ID          string                     `json:"id"`
	Version     string                     `json:"version"`
	Contributes map[string]json.RawMessage `json:"contributes,omitempty"`
	// UIPageCancellation negotiates request-ID scoped rpc.cancel for page lifecycle calls only.
	UIPageCancellation int `json:"ui_page_cancellation,omitempty"`
}

// Target is the field every namespaced call carries: which of the plugin's contributions to that extension point it
// is for ("" when the plugin has only one).
type Target struct {
	Contribution string `json:"contribution,omitempty"`
}

// Targeted is params (an object) with the contribution a namespaced call is for added; params as they are when
// contribution is "".
func Targeted(params any, contribution string) (json.RawMessage, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if contribution == "" {
		return b, nil
	}
	m := map[string]json.RawMessage{}
	if string(b) != "null" {
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("params of a namespaced call must be an object: %w", err)
		}
	}
	m["contribution"], _ = json.Marshal(contribution)
	return json.Marshal(m)
}
